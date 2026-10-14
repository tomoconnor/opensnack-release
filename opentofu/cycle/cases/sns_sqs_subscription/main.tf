resource "aws_sns_topic" "t" {
  name = "cycle-events"
}

resource "aws_sqs_queue" "q" {
  name = "cycle-events-sink"
}

resource "aws_sqs_queue_policy" "p" {
  queue_url = aws_sqs_queue.q.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.q.arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_sns_topic.t.arn } }
    }]
  })
}

resource "aws_sns_topic_subscription" "s" {
  topic_arn            = aws_sns_topic.t.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.q.arn
  raw_message_delivery = var.variant == "a"
  filter_policy = jsonencode({
    kind = var.variant == "a" ? ["widget"] : ["widget", "scrap"]
  })
}
