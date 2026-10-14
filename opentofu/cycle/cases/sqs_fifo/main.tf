resource "aws_sqs_queue" "dlq" {
  name       = "cycle-fifo-dlq.fifo"
  fifo_queue = true
}

resource "aws_sqs_queue" "f" {
  name                        = "cycle-fifo.fifo"
  fifo_queue                  = true
  content_based_deduplication = var.variant == "b"
  deduplication_scope         = "messageGroup"
  fifo_throughput_limit       = "perMessageGroupId"
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 3
  })
  tags = {
    Variant = var.variant
  }
}
