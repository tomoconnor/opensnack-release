data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/build/esmfn.zip"

  source {
    filename = "app.py"
    content  = "def handler(event, context):\n    return len(event[\"Records\"])\n"
  }
}

resource "aws_iam_role" "fn" {
  name = "cycle-esmfn-role"
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
  function_name    = "cycle-esmfn"
  role             = aws_iam_role.fn.arn
  handler          = "app.handler"
  runtime          = "python3.12"
  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
}

resource "aws_sqs_queue" "jobs" {
  name = "cycle-jobs-to-app"
}

resource "aws_lambda_event_source_mapping" "jobs" {
  event_source_arn                   = aws_sqs_queue.jobs.arn
  function_name                      = aws_lambda_function.fn.arn
  batch_size                         = var.variant == "a" ? 10 : 50
  maximum_batching_window_in_seconds = var.variant == "a" ? 0 : 5
  enabled                            = var.variant == "a"
  function_response_types            = ["ReportBatchItemFailures"]

  filter_criteria {
    filter {
      pattern = jsonencode({ body = { failed = [false] } })
    }
  }

  scaling_config {
    maximum_concurrency = var.variant == "a" ? 2 : 5
  }

  tags = {
    Team = "team"
  }
}
