data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/build/fn-${var.variant}.zip"

  source {
    filename = "app.py"
    content  = <<-EOT
      import os

      def handler(event, context):
          return {"greeting": os.environ.get("GREETING"), "variant": "${var.variant}"}
    EOT
  }
}

resource "aws_iam_role" "fn" {
  name = "cycle-lambda-role"
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
  function_name    = "cycle-fn"
  role             = aws_iam_role.fn.arn
  handler          = "app.handler"
  runtime          = "python3.12"
  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  memory_size      = var.variant == "a" ? 128 : 256
  timeout          = 5
  publish          = true

  environment {
    variables = {
      GREETING = var.variant == "a" ? "hello" : "bonjour"
    }
  }

  tags = {
    Team = "team"
  }
}

resource "aws_lambda_alias" "live" {
  name             = "live"
  description      = "current release"
  function_name    = aws_lambda_function.fn.function_name
  function_version = aws_lambda_function.fn.version
}

resource "aws_lambda_permission" "sns" {
  statement_id  = "AllowSNS"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.fn.function_name
  qualifier     = aws_lambda_alias.live.name
  principal     = "sns.amazonaws.com"
  source_arn    = "arn:aws:sns:us-east-1:000000000000:cycle-topic"
}
