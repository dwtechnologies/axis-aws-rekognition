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
                --output-template-file template_out.yaml \
                --s3-bucket $(ARTIFACTS_BUCKET)

sam_deploy = aws --profile $(AWS_PROFILE) cloudformation deploy \
                --template-file template_out.yaml \
                --stack-name $(STACK_NAME) \
		--region $(AWS_DEFAULT_REGION) \
                --capabilities CAPABILITY_IAM \
                --parameter-overrides \
                        $(shell cat parameters.conf) \
		--tags Project=$(PROJECT) Owner=$(OWNER) \
                --no-fail-on-empty-changeset


build:
	cd src/; GOOS=linux go build -ldflags="-s -w" -o main && zip deployment.zip main

clean:
	@rm -rf src/deployment.zip template_out.yaml

deploy:
	make build
	$(call sam_package)
	$(call sam_deploy)
	make clean

