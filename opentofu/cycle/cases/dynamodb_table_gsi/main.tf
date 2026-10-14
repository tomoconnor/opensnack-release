resource "aws_dynamodb_table" "t" {
  name         = "cycle-orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "customer"
  range_key    = "order_id"

  attribute {
    name = "customer"
    type = "S"
  }
  attribute {
    name = "order_id"
    type = "S"
  }
  attribute {
    name = "status"
    type = "S"
  }
  attribute {
    name = "total"
    type = "N"
  }
  dynamic "attribute" {
    for_each = var.variant == "b" ? ["region"] : []
    content {
      name = attribute.value
      type = "S"
    }
  }

  global_secondary_index {
    name               = "by_status"
    hash_key           = "status"
    range_key          = "total"
    projection_type    = "INCLUDE"
    non_key_attributes = ["note"]
  }
  dynamic "global_secondary_index" {
    for_each = var.variant == "b" ? ["region"] : []
    content {
      name            = "by_region"
      hash_key        = "region"
      projection_type = "KEYS_ONLY"
    }
  }

  local_secondary_index {
    name            = "by_total"
    range_key       = "total"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = var.variant == "a"
  }

  tags = {
    env  = "cycle"
    tier = var.variant == "a" ? "gold" : "silver"
  }
}
