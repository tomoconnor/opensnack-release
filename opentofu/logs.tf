# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_cloudwatch_log_group" "lg" {
  name = "/opensnack/logs/test"
}

resource "aws_cloudwatch_log_stream" "ls" {
  log_group_name = aws_cloudwatch_log_group.lg.name
  name           = "stream1"
}

output "log_group" {
  value = aws_cloudwatch_log_group.lg.name
}
output "log_stream" {
  value = aws_cloudwatch_log_stream.ls.name
}