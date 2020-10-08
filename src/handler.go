package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
	"github.com/aws/aws-sdk-go-v2/service/rekognition"
	"github.com/aws/aws-sdk-go-v2/service/rekognition/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/caarlos0/env/v6"
)

// MqttMessage struct is to match the incoming json from the AXIS camera and image as base64
// https://github.com/aintegration/acaps/tree/master/Publisher#mqtt-device-status-publish
type MqttMessage struct {
	Tags struct {
		Device string `json:"device"`
		Name   string `json:"name"`
		Port   int    `json:"port"`
	} `json:"tags"`
	Values struct {
		State bool `json:"state"`
	} `json:"values"`
	Timestamp int64  `json:"timestamp"`
	Clock     int    `json:"clock"`
	LocalTime string `json:"localtime"`
	Image     string `json:"image"`
	Topic     string `json:"topic"`
	Event     string `json:"event"`
}

type lambdaConfig struct {
	IOTEndpoint    string `env:"IOT_ENDPOINT,required"`
	IOTOutputTopic string `env:"IOT_OUTPUT_TOPIC,required"`

	RegexpMatch  string `env:"REGEXP_MATCH,required"`
	OutputBucket string `env:"S3_OUTPUT_BUCKET,required"`
//	OutputSNS    string `env:"SNS_OUTPUT_ARN,required"`
}

// This is the filename generator for s3
func tempFileName(prefix, suffix string) string {
	randBytes := make([]byte, 16)
	rand.Seed(time.Now().UnixNano())
	rand.Read(randBytes)

	return prefix + hex.EncodeToString(randBytes) + suffix
}

// to make it pretty for the cloudwatch logs (?)
func jsonPrettyPrint(in string) string {
	var out bytes.Buffer
	err := json.Indent(&out, []byte(in), "", "\t")
	if err != nil {
		return in
	}
	return out.String()
}

// function to upload both image and rekognition output to s3
func uploadBytesToS3(ctx context.Context, config aws.Config, src []byte, bucket, key string) error {
	reader := bytes.NewReader(src)

	// fix bug with bucket names with dots
	client := s3.New(s3.Options{
		Credentials:  config.Credentials,
		Region:       config.Region,
		UsePathStyle: true,
	})
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), // Bucket to be used
		Key:    aws.String(key),    // Name of the file to be saved
		Body:   reader,             // File
	})
	if err != nil {
		log.Printf("unable to upload item %q, %v", key, err)
		return err
	}

	return nil
}

// this function is to post back the match to mqtt if the regexp matched the text so we can do other things with the result
func publishToIoT(ctx context.Context, config aws.Config, IOTEndpoint, topicName, message string) error {
	// config.EndpointResolver = aws.ResolveWithEndpointURL(IOTEndpoint) // needed?

	client := iotdataplane.NewFromConfig(config)
	resp, err := client.Publish(ctx, &iotdataplane.PublishInput{
		Topic:   aws.String(topicName),
		Payload: []byte(message),
		Qos:     aws.Int32(1),
	})
	if err != nil {
		log.Println("Error publishing", err)
		return err
	}

	log.Printf("iotdataplane publish response: %s", resp)
	return nil
}

//Old function when we used to publish to SNS, keeping this if we would like to use this in the future
//Did I hear a PR to make this a bit more dynamic?
func publishMessage(ctx context.Context, config aws.Config, topicARN, messageStr string) error {
	client := sns.NewFromConfig(config)

	resp, err := client.Publish(ctx, &sns.PublishInput{
		TopicArn:         aws.String(topicARN),
		Message:          aws.String(messageStr),
		MessageStructure: aws.String("json"),
	})
	if err != nil {
		log.Printf("error publishing to SNS topic: %s", err)
		return err
	}

	log.Printf("SNS publish response: %s", *resp.MessageId)
	return nil
}

//This function will send the image to rekognition service and use OCR function to extract all text
//in the image and match it against the regexp and then publish the result to mqtt
func rekonImage(ctx context.Context, config aws.Config, src []byte, IOTEndpoint, regexpMatch, outputTopic string) (string, error) {
	var validMatch = regexp.MustCompile(regexpMatch)

	client := rekognition.NewFromConfig(config)

	resp, err := client.DetectText(ctx, &rekognition.DetectTextInput{
		Image: &types.Image{
			Bytes: src,
		},
	})
	if err != nil {
		log.Printf("rekognition errror: %s", err)
		return "", err
	}

	//log.Println(resp.TextModelVersion)

	// detect text and publish to topic
	for _, service := range resp.TextDetections {
		log.Printf("all text results: %s", *service.DetectedText)
		if validMatch.MatchString(*service.DetectedText) {
			log.Printf("regexp text match: %s", *service.DetectedText)
			jsonResult := fmt.Sprintf("{ \"match\": \"%s\" }", validMatch.Find([]byte(*service.DetectedText)))

			publishToIoT(ctx, config, IOTEndpoint, outputTopic, jsonResult)
		}
	}

	output, err := json.MarshalIndent(resp, "", "\t")
	if err != nil {
		log.Printf("unmarshalling error: %s", err)
		return "", err
	}
	//log.Println(jsonPrettyPrint(string(output)))

	return jsonPrettyPrint(string(output)), nil
}

// Handler for iot when we get the json from the IOT rule
func handler(ctx context.Context, mqttMessage MqttMessage) error {
	config, err := config.LoadDefaultConfig()
	if err != nil {
		return err
	}

	// parse env vars
	envcfg := &lambdaConfig{}
	if err := env.Parse(envcfg); err != nil {
		panic(err)
	}

	image, err := base64.StdEncoding.DecodeString(mqttMessage.Image)
	if err != nil {
		log.Printf("decode error: %s", err)
		return err
	}

	tmpFile := tempFileName(fmt.Sprintf("result/%s-", mqttMessage.Tags.Device), ".jpg")

	// Use the rekongition function above
	rekonOutput, err := rekonImage(ctx, config, image, envcfg.IOTEndpoint, envcfg.RegexpMatch, envcfg.IOTOutputTopic)
	if err != nil {
		return err
	}

	mqttMessageMarshal, _ := json.Marshal(mqttMessage)
	// use functions above to upload to s3
	uploadBytesToS3(ctx, config, image, envcfg.OutputBucket, tmpFile)
	uploadBytesToS3(ctx, config, []byte(rekonOutput), envcfg.OutputBucket, tmpFile+"-rekognition-result.json")
	// debug
	uploadBytesToS3(ctx, config, []byte(mqttMessageMarshal), envcfg.OutputBucket, tmpFile+"-struct.json")

	// send output to SNS, not used anymore
	// PublishMessage(ctx, config, envcfg.OutputSNS, envcfg.RegexpMatch,rekonOutput)

	return nil
}

func main() {
	lambda.Start(handler)
}
