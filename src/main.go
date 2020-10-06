package main

import (
	"context"
	"encoding/json"
	"fmt"
        "encoding/base64"
	"math/rand"
	"time"
	"encoding/hex"
	"os"
	"log"
	"bytes"
	"regexp"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
        "github.com/aws/aws-sdk-go-v2/aws"
        "github.com/aws/aws-sdk-go-v2/service/s3/s3manager"
        "github.com/aws/aws-sdk-go-v2/aws/external"
        "github.com/aws/aws-sdk-go-v2/service/rekognition"
        "github.com/aws/aws-sdk-go-v2/service/sns"
        "github.com/aws/aws-sdk-go-v2/service/iotdataplane"
)

//This struct is to match the incoming json from the AXIS camera and image as base64
//https://github.com/aintegration/acaps/tree/master/Publisher#mqtt-device-status-publish
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

//This is the filename generator for s3
func TempFileName(prefix, suffix string) string {
    randBytes := make([]byte, 16)
    rand.Seed(time.Now().UnixNano())
    rand.Read(randBytes)
    return prefix+hex.EncodeToString(randBytes)+suffix
}

//to make it pretty for the cloudwatch logs
func jsonPrettyPrint(in string) string {
    var out bytes.Buffer
    err := json.Indent(&out, []byte(in), "", "\t")
    if err != nil {
        return in
    }
    return out.String()
}

//function to upload both image and rekognition output to s3
func UploadBytesToS3(src []byte, bucket string, key string, config aws.Config) {

    reader := bytes.NewReader(src)

    uploader := s3manager.NewUploader(config)

    numBytes, err := uploader.Upload(&s3manager.UploadInput{
            Bucket: aws.String(bucket), // Bucket to be used
            Key:    aws.String(key),    // Name of the file to be saved
            Body:   reader,               // File
        })

        if err != nil {
            log.Fatalf("Unable to upload item %q, %v", key, err)
        }

        fmt.Println("uploaded bytes ", numBytes)

}

//This function is to post back the match to mqtt if the regexp matched the text so we can do other things with the result
func PublishToIoT(topicName string, message string, ctx context.Context) {

  config, _ := external.LoadDefaultAWSConfig()
  config.EndpointResolver = aws.ResolveWithEndpointURL(os.Getenv("IOT_ENDPOINT"))

  dataplaneClient := iotdataplane.New(config)

  params := &iotdataplane.PublishInput{
    Topic:   aws.String(topicName), // Required
    Payload: []byte(message),
    Qos:     aws.Int64(1),
  }

  req := dataplaneClient.PublishRequest(params)

  res, err := req.Send(ctx)

  if err != nil {
	log.Println("Error publishing",err)
  }

  log.Print(res)



}

//Old function when we used to publish to SNS, keeping this if we would like to use this in the future
//Did I hear a PR to make this a bit more dynamic?
func PublishMessage(topicARN string, messageStr string, config aws.Config, ctx context.Context) {

    snsClient := sns.New(config)

    req := snsClient.PublishRequest(&sns.PublishInput{
        TopicArn: aws.String( topicARN),
        Message:  aws.String(messageStr),
        MessageStructure: aws.String("json"),
    })

    res, err := req.Send(ctx)
    if err != nil {log.Fatal(err)
    }

    log.Print(res)

}

//This function will send the image to rekognition service and use OCR function to extract all text
//in the image and match it against the regexp and then publish the result to mqtt
func RekonImage(src []byte,  config aws.Config, outputTopic string, ctx context.Context) (string) {

	var validMatch = regexp.MustCompile(os.Getenv("REGEXP_MATCH"))

        rekonSvc := rekognition.New(config)

        input := &rekognition.DetectTextInput{Image: &rekognition.Image{Bytes: src} }

        req := rekonSvc.DetectTextRequest(input)
        resp, err := req.Send(context.TODO())
        if err != nil {
            fmt.Println("Error ",resp)
        }

	//log.Println(resp.TextModelVersion)

	// detect text and publish to topic
	for _,service := range resp.TextDetections {
              fmt.Println("All text results: ", *service.DetectedText)
	      if (validMatch.MatchString(*service.DetectedText)) {
		fmt.Println("Regexp text match: ", *service.DetectedText)
	        jsonResult := fmt.Sprintf("{ \"match\": \"%s\" }",validMatch.Find([]byte(*service.DetectedText)))
		PublishToIoT(outputTopic, jsonResult, ctx)
	      }
        }

        output, err := json.MarshalIndent(resp, "", "\t")
	if err != nil {
	  log.Println("Unmarshalling error", err)
	}
	//log.Println(jsonPrettyPrint(string(output)))
	return jsonPrettyPrint(string(output))

}

type Response events.APIGatewayProxyResponse

// Handler for iot when we get the json from the IOT rule
func Handler(ctx context.Context, mqttMessage MqttMessage) {

   config, _ := external.LoadDefaultAWSConfig()
   log.Println("handler called")


        dec, err := base64.StdEncoding.DecodeString(mqttMessage.Image)
        if err != nil {
          fmt.Println("Error", err)
        }

        tmpFile := TempFileName("result/"+mqttMessage.Tags.Device+"-",".jpg")

        // Use the rekongition function above
        rekonOutput := RekonImage(dec, config, os.Getenv("IOT_OUTPUT_TOPIC"), ctx)

	mqttMessageMarshal, _ := json.Marshal(mqttMessage)
        // use functions above to upload to s3
        UploadBytesToS3(dec, os.Getenv("S3_OUTPUT_BUCKET"), tmpFile, config)
        UploadBytesToS3([]byte(rekonOutput), os.Getenv("S3_OUTPUT_BUCKET"), tmpFile + "-rekognition-result.json", config)
	//To debug
        UploadBytesToS3([]byte(mqttMessageMarshal), os.Getenv("S3_OUTPUT_BUCKET"), tmpFile + "-struct.json", config)

        // send output to SNS, not used anymore
        //PublishMessage(os.Getenv("SNS_OUTPUT_ARN"), rekonOutput, config, ctx)


}

func main() {
	lambda.Start(Handler)
}

