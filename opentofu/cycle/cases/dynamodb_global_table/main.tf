resource "aws_dynamodb_table" "route" {
  name             = "cycle-sleigh-route"
  billing_mode     = "PAY_PER_REQUEST"
  hash_key         = "stop"
  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  attribute {
    name = "stop"
    type = "S"
  }

  replica {
    region_name = "eu-west-1"
  }

  dynamic "replica" {
    for_each = var.variant == "b" ? ["ap-northeast-1"] : []
    content {
      region_name = replica.value
    }
  }

  tags = {
    variant = var.variant
  }
}

resource "aws_dynamodb_table_item" "lapland" {
  table_name = aws_dynamodb_table.route.name
  hash_key   = aws_dynamodb_table.route.hash_key
  item       = jsonencode({ stop = { S = "lapland" }, order = { N = var.variant == "a" ? "1" : "2" } })
}
