# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_sns_topic" "test" {
  name = "opensnack-sns-test"
}

resource "aws_sns_topic_subscription" "sub" {
  topic_arn = aws_sns_topic.test.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.testq.arn
}

resource "aws_sns_topic_policy" "policy" {
  arn = aws_sns_topic.test.arn
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect    = "Allow",
      Principal = "*",
      Action    = "sns:Publish",
      Resource  = aws_sns_topic.test.arn
    }]
  })
}

output "sns_topic_arn" {
  value = aws_sns_topic.test.arn
}
