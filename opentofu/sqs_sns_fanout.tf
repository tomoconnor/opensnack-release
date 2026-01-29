# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_sns_topic" "fanout" {
  name = "opensnack-fanout"
}

resource "aws_sqs_queue" "fanq" {
  name = "opensnack-fanq"
}

resource "aws_sns_topic_subscription" "sub_fanout" {
  topic_arn = aws_sns_topic.fanout.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.fanq.arn
}

output "fanout_topic" {
  value = aws_sns_topic.fanout.arn
}
