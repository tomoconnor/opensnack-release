# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_secretsmanager_secret" "sec" {
  name = "opensnack-secret"
}

resource "aws_secretsmanager_secret_version" "ver" {
  secret_id     = aws_secretsmanager_secret.sec.id
  secret_string = "my-secret-value"
}

output "secret_arn" {
  value = aws_secretsmanager_secret.sec.arn
}



resource "aws_ssm_parameter" "param" {
  name  = "/opensnack/testparam"
  type  = "String"
  value = "hello"
}

output "param_value" {
  value = aws_ssm_parameter.param.value
  sensitive = true
}
