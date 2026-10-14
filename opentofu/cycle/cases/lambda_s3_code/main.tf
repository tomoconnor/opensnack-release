data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/build/s3fn-${var.variant}.zip"

  source {
    filename = "index.mjs"
    content  = <<-EOT
      export const handler = async (event) => ({ variant: "${var.variant}", event });
    EOT
  }
}

resource "aws_s3_bucket" "code" {
  bucket = "cycle-lambda-code"
}

resource "aws_s3_object" "code" {
  bucket = aws_s3_bucket.code.id
  key    = "functions/s3fn.zip"
  source = data.archive_file.code.output_path
  etag   = data.archive_file.code.output_md5
}

resource "aws_iam_role" "fn" {
  name = "cycle-s3fn-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

resource "aws_lambda_function" "fn" {
  function_name    = "cycle-s3fn"
  description      = "code from S3 (${var.variant})"
  role             = aws_iam_role.fn.arn
  handler          = "index.handler"
  runtime          = "nodejs22.x"
  s3_bucket        = aws_s3_object.code.bucket
  s3_key           = aws_s3_object.code.key
  source_code_hash = data.archive_file.code.output_base64sha256
  architectures    = ["arm64"]
  timeout          = var.variant == "a" ? 10 : 20
  layers           = ["arn:aws:lambda:us-east-1:000000000000:layer:shared:${var.variant == "a" ? 1 : 2}"]

  reserved_concurrent_executions = var.variant == "a" ? 5 : 10

  ephemeral_storage {
    size = var.variant == "a" ? 512 : 1024
  }

  logging_config {
    log_format = "JSON"
    log_group  = "/custom/cycle-s3fn"
  }

  tracing_config {
    mode = var.variant == "a" ? "PassThrough" : "Active"
  }

  dead_letter_config {
    target_arn = "arn:aws:sqs:us-east-1:000000000000:cycle-dlq"
  }

  vpc_config {
    subnet_ids         = ["subnet-0a1b2c3d", "subnet-1a2b3c4d"]
    security_group_ids = ["sg-0a1b2c3d"]
  }
}

resource "aws_lambda_function_event_invoke_config" "fn" {
  function_name                = aws_lambda_function.fn.function_name
  maximum_retry_attempts       = var.variant == "a" ? 1 : 0
  maximum_event_age_in_seconds = 300
}
