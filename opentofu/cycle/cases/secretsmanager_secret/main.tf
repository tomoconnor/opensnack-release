data "aws_caller_identity" "me" {}

resource "aws_secretsmanager_secret" "s" {
  name                    = "cycle/secret"
  description             = "cycle secret ${var.variant}"
  recovery_window_in_days = 0

  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_secretsmanager_secret_version" "v" {
  secret_id     = aws_secretsmanager_secret.s.id
  secret_string = jsonencode({ user = "worker", pass = "secret-value" })
}

resource "aws_secretsmanager_secret_policy" "p" {
  secret_arn = aws_secretsmanager_secret.s.arn
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "ReadOnly"
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.me.account_id}:root" }
      Action    = var.variant == "a" ? "secretsmanager:GetSecretValue" : "secretsmanager:*"
      Resource  = "*"
    }]
  })
}

data "aws_secretsmanager_secret_version" "current" {
  secret_id  = aws_secretsmanager_secret.s.id
  depends_on = [aws_secretsmanager_secret_version.v]
}

output "user" {
  value     = jsondecode(data.aws_secretsmanager_secret_version.current.secret_string).user
  sensitive = true
}
