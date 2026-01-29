# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  s3_use_path_style           = true

  endpoints {
    s3             = var.opensnack_endpoint
    s3control      = "${var.opensnack_endpoint}/s3-control"
    apigateway     = "${var.opensnack_endpoint}/apigateway"
    apigatewayv2   = "${var.opensnack_endpoint}/apigatewayv2"
    cloudformation = "${var.opensnack_endpoint}/cloudformation"
    cloudwatch     = "${var.opensnack_endpoint}/logs"
    logs           = "${var.opensnack_endpoint}/logs"
    dynamodb       = "${var.opensnack_endpoint}/dynamodb"
    ec2            = "${var.opensnack_endpoint}/ec2"
    es             = "${var.opensnack_endpoint}/es"
    elasticache    = "${var.opensnack_endpoint}/elasticache"
    firehose       = "${var.opensnack_endpoint}/firehose"
    iam            = "${var.opensnack_endpoint}/iam"
    kinesis        = "${var.opensnack_endpoint}/kinesis"
    kms            = "${var.opensnack_endpoint}/kms"
    lambda         = "${var.opensnack_endpoint}/lambda"
    rds            = "${var.opensnack_endpoint}/rds"
    redshift       = "${var.opensnack_endpoint}/redshift"
    route53        = "${var.opensnack_endpoint}/route53"
    secretsmanager = "${var.opensnack_endpoint}/secretsmanager"
    ses            = "${var.opensnack_endpoint}/ses"
    sns            = "${var.opensnack_endpoint}/sns"
    sqs            = "${var.opensnack_endpoint}/sqs"
    ssm            = "${var.opensnack_endpoint}/ssm"
    stepfunctions  = "${var.opensnack_endpoint}/stepfunctions"
    sts            = "${var.opensnack_endpoint}/sts"
  }
}

variable "opensnack_endpoint" {
  type = string
  # nip.io wildcard lets AWS SDK subdomains (e.g., 000000000000.127.0.0.1.nip.io)
  # resolve back to localhost, which S3 Control requires.
  default = "http://127.0.0.1.nip.io:4566"
}
