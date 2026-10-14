# A key ARN rather than an aws_kms_key, so this case only exercises Logs
# (opensnack doesn't check that the key exists).
data "aws_caller_identity" "me" {}
data "aws_region" "here" {}

locals {
  kms_key_arn = "arn:aws:kms:${data.aws_region.here.region}:${data.aws_caller_identity.me.account_id}:key/1234abcd-12ab-34cd-56ef-1234567890ab"
}

resource "aws_cloudwatch_log_group" "g" {
  name              = "/opensnack/cycle/orders"
  retention_in_days = var.variant == "a" ? 14 : 30
  kms_key_id        = local.kms_key_arn
  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_cloudwatch_log_stream" "s" {
  name           = "orders-1"
  log_group_name = aws_cloudwatch_log_group.g.name
}

resource "aws_cloudwatch_log_metric_filter" "errors" {
  name           = "orders-errors"
  log_group_name = aws_cloudwatch_log_group.g.name
  pattern        = var.variant == "a" ? "{ $.level = \"error\" }" : "[ip, user, status = 5*, bytes]"

  metric_transformation {
    name          = "OrderErrors"
    namespace     = "ExampleOrders"
    value         = "1"
    default_value = "0"
  }
}
