resource "aws_sns_topic" "t" {
  name = "cycle-topic"
  tags = {
    Team    = "team"
    Variant = var.variant
  }
}
