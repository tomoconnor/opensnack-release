data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/build/urlfn.zip"

  source {
    filename = "app.py"
    content  = "def handler(event, context):\n    return {\"statusCode\": 200, \"body\": \"hello\"}\n"
  }
}

resource "aws_iam_role" "fn" {
  name = "cycle-urlfn-role"
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
  function_name    = "cycle-urlfn"
  role             = aws_iam_role.fn.arn
  handler          = "app.handler"
  runtime          = "python3.13"
  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  publish          = true
}

resource "aws_lambda_alias" "live" {
  name             = "live"
  function_name    = aws_lambda_function.fn.function_name
  function_version = aws_lambda_function.fn.version
}

resource "aws_lambda_function_url" "public" {
  function_name      = aws_lambda_function.fn.function_name
  authorization_type = "NONE"

  cors {
    allow_credentials = true
    allow_origins     = var.variant == "a" ? ["https://app.example"] : ["https://app.example", "https://team.example"]
    allow_methods     = ["GET", "POST"]
    allow_headers     = ["content-type"]
    expose_headers    = ["x-request-id"]
    max_age           = var.variant == "a" ? 60 : 300
  }
}

resource "aws_lambda_function_url" "live" {
  function_name      = aws_lambda_function.fn.function_name
  qualifier          = aws_lambda_alias.live.name
  authorization_type = "AWS_IAM"
  invoke_mode        = var.variant == "a" ? "BUFFERED" : "RESPONSE_STREAM"
}
