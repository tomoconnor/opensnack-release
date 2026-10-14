resource "aws_sqs_queue" "parcels" {
  name = "cycle-events-parcels"
}

resource "aws_cloudwatch_event_bus" "orders" {
  name        = "cycle-orders"
  description = var.variant == "a" ? "orders bus" : "orders bus, revised"
  tags = {
    Team = "team"
  }
}

resource "aws_cloudwatch_event_rule" "heavy" {
  name           = "cycle-heavy-parcels"
  event_bus_name = aws_cloudwatch_event_bus.orders.name
  description    = var.variant == "a" ? "heavy parcels" : "very heavy parcels"
  event_pattern = jsonencode({
    source      = ["example.orders"]
    detail-type = [{ prefix = "Parcel" }]
    detail = {
      weight = [{ numeric = [">", var.variant == "a" ? 100 : 200] }]
      worker    = [{ anything-but = ["Intruder"] }]
    }
  })
  tags = {
    Variant = var.variant
  }
}

resource "aws_cloudwatch_event_target" "to_queue" {
  rule           = aws_cloudwatch_event_rule.heavy.name
  event_bus_name = aws_cloudwatch_event_bus.orders.name
  target_id      = "parcels"
  arn            = aws_sqs_queue.parcels.arn

  input_transformer {
    input_paths = {
      weight = "$.detail.weight"
      worker    = "$.detail.worker"
    }
    input_template = <<-TPL
      {"weight": <weight>, "msg": "<worker> lifted a heavy parcel"}
    TPL
  }

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }
}
