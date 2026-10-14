resource "aws_sqs_queue" "q" {
  name                       = "cycle-queue"
  visibility_timeout_seconds = var.variant == "a" ? 30 : 60
}
