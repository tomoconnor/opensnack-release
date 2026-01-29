# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_sqs_queue" "testq" {
  name = "opensnack-test-q"
}

# Send a message
resource "aws_sqs_queue_policy" "queue_policy" {
  queue_url = aws_sqs_queue.testq.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.testq.arn
      Principal = "*"
    }]
  })
}

resource "aws_sqs_queue_redrive_allow_policy" "rdap" {
  queue_url = aws_sqs_queue.testq.id
  redrive_allow_policy = jsonencode({
    redrivePermission = "allowAll"
  })
}

output "queue_url" {
  value = aws_sqs_queue.testq.id
}
