resource "aws_dynamodb_table" "t" {
  name           = "cycle-provisioned"
  billing_mode   = "PROVISIONED"
  hash_key       = "id"
  read_capacity  = var.variant == "a" ? 5 : 10
  write_capacity = var.variant == "a" ? 5 : 8

  attribute {
    name = "id"
    type = "S"
  }
  attribute {
    name = "email"
    type = "S"
  }

  global_secondary_index {
    name            = "by_email"
    hash_key        = "email"
    projection_type = "ALL"
    read_capacity   = var.variant == "a" ? 2 : 4
    write_capacity  = 2
  }

  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  server_side_encryption {
    enabled = true
  }

  deletion_protection_enabled = false
}
