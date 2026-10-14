data "aws_caller_identity" "me" {}

resource "aws_kms_key" "k" {
  description             = "cycle key ${var.variant}"
  enable_key_rotation     = true
  rotation_period_in_days = var.variant == "a" ? 90 : 180

  policy = jsonencode({
    Version = "2012-10-17"
    Id      = "cycle"
    Statement = [{
      Sid       = "Root"
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.me.account_id}:root" }
      Action    = "kms:*"
      Resource  = "*"
    }]
  })

  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_kms_key" "other" {
  description = "cycle alias target b"
}

resource "aws_kms_alias" "a" {
  name          = "alias/cycle-key"
  target_key_id = var.variant == "a" ? aws_kms_key.k.key_id : aws_kms_key.other.arn
}

data "aws_kms_key" "by_alias" {
  key_id = aws_kms_alias.a.name
}

output "alias_key" {
  value = data.aws_kms_key.by_alias.id
}
