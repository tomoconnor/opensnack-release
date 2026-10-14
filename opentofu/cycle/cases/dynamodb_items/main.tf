resource "aws_dynamodb_table" "t" {
  name         = "cycle-items"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "N"
  }
}

resource "aws_dynamodb_table_item" "a" {
  table_name = aws_dynamodb_table.t.name
  hash_key   = aws_dynamodb_table.t.hash_key
  range_key  = aws_dynamodb_table.t.range_key
  item = jsonencode({
    pk    = { S = "config" }
    sk    = { N = "1" }
    value = { S = var.variant == "a" ? "blue" : "green" }
    tags  = { SS = ["x", "y"] }
    nested = { M = {
      count = { N = var.variant == "a" ? "3" : "4" }
      on    = { BOOL = true }
    } }
  })
}

resource "aws_dynamodb_table_item" "b" {
  table_name = aws_dynamodb_table.t.name
  hash_key   = aws_dynamodb_table.t.hash_key
  range_key  = aws_dynamodb_table.t.range_key
  item = jsonencode({
    pk   = { S = "config" }
    sk   = { N = "2" }
    list = { L = [{ S = "a" }, { N = "2" }] }
  })
}
