resource "aws_sqs_queue" "dlq" {
  name                      = "cycle-full-dlq"
  message_retention_seconds = 1209600
  redrive_allow_policy = jsonencode({
    redrivePermission = "allowAll"
  })
  tags = {
    Role = "dlq"
  }
}

resource "aws_sqs_queue" "q" {
  name                       = "cycle-full"
  delay_seconds              = var.variant == "a" ? 0 : 5
  max_message_size           = 2048
  message_retention_seconds  = 86400
  receive_wait_time_seconds  = 10
  visibility_timeout_seconds = var.variant == "a" ? 30 : 90
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.variant == "a" ? 4 : 5
  })
  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_sqs_queue_policy" "p" {
  queue_url = aws_sqs_queue.q.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "AllowSNS"
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.q.arn
    }]
  })
}
