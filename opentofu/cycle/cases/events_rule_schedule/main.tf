resource "aws_sqs_queue" "daily" {
  name = "cycle-events-daily"
}

resource "aws_cloudwatch_event_rule" "daily" {
  name                = "cycle-nightly-daily"
  description         = "Nightly's daily reminder (${var.variant})"
  schedule_expression = "rate(5 minutes)"
  state               = var.variant == "a" ? "ENABLED" : "DISABLED"
}

resource "aws_cloudwatch_event_target" "to_queue" {
  rule      = aws_cloudwatch_event_rule.daily.name
  target_id = "daily-queue"
  arn       = aws_sqs_queue.daily.arn
  input     = jsonencode({ reminder = "daily", variant = var.variant })
}
