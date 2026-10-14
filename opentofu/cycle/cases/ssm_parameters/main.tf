resource "aws_kms_key" "k" {
  description = "cycle ssm key"
}

resource "aws_ssm_parameter" "plain" {
  name        = "/cycle/app/name"
  type        = "String"
  value       = "factory-${var.variant}"
  description = "app name"

  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_ssm_parameter" "secret" {
  name   = "/cycle/app/db/password"
  type   = "SecureString"
  value  = "secret-value-${var.variant}"
  key_id = aws_kms_key.k.key_id
}

# No key_id: SSM uses the AWS managed key alias/aws/ssm
resource "aws_ssm_parameter" "default_key" {
  name  = "/cycle/app/db/token"
  type  = "SecureString"
  value = "token-${var.variant}"
}

resource "aws_ssm_parameter" "list" {
  name  = "/cycle/app/regions"
  type  = "StringList"
  value = var.variant == "a" ? "us-east-1,eu-west-1" : "us-east-1,eu-west-1,ap-south-1"
}

data "aws_ssm_parameters_by_path" "all" {
  path      = "/cycle/app"
  recursive = true
  depends_on = [
    aws_ssm_parameter.plain,
    aws_ssm_parameter.secret,
    aws_ssm_parameter.default_key,
    aws_ssm_parameter.list,
  ]
}

data "aws_ssm_parameters_by_path" "top" {
  path       = "/cycle/app"
  depends_on = [aws_ssm_parameter.plain, aws_ssm_parameter.list]
}

output "names" {
  value = data.aws_ssm_parameters_by_path.all.names
}

output "top_names" {
  value = data.aws_ssm_parameters_by_path.top.names
}
