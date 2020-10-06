.PHONY: build clean deploy

ENVIRONMENT        ?= prod
PROJECT            =  "aws rekognition power to axis"
OWNER		   = "innovation"
STACK_NAME         =  axis-aws-rekognition
ARTIFACTS_BUCKET   =  s3-bucket-to-upload-the-lambda-artifact-for-deploy
AWS_DEFAULT_REGION ?= eu-west-1
AWS_PROFILE ?= your-aws-cli-profile

sam_package = aws --profile $(AWS_PROFILE) cloudformation package \
                --template-file template.yml \
                --output-template-file bin/sam-out.yaml \
                --s3-bucket $(ARTIFACTS_BUCKET)

sam_deploy = aws --profile $(AWS_PROFILE) cloudformation deploy \
                --template-file bin/sam-out.yaml \
                --stack-name $(STACK_NAME) \
		--region $(AWS_DEFAULT_REGION) \
                --capabilities CAPABILITY_IAM \
                --parameter-overrides \
                        $(shell cat parameters.conf) \
		--tags Project=$(PROJECT) Owner=$(OWNER) \
                --no-fail-on-empty-changeset



build:
	cd src 
	go get github.com/aws/aws-lambda-go/events
	go get github.com/aws/aws-lambda-go/lambda
	go get github.com/aws/aws-sdk-go-v2/aws
	go get github.com/aws/aws-sdk-go-v2/aws/external
	go get github.com/aws/aws-sdk-go-v2/service/iotdataplane
	go get github.com/aws/aws-sdk-go-v2/service/rekognition
	go get github.com/aws/aws-sdk-go-v2/service/s3/s3manager
	go get github.com/aws/aws-sdk-go-v2/service/sns
	cd ..
	env GOOS=linux go build -ldflags="-s -w" -o bin/main src/main.go
	cd bin && zip main.zip * 

clean:
	rm -rf ./bin

deploy: clean build
#	sls deploy --verbose

	$(call sam_package)
	$(call sam_deploy)
	@rm -rf bin/
	
