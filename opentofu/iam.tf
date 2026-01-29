# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_iam_user" "u" {
  name = "opensnack-user"
}

resource "aws_iam_policy" "p" {
  name = "opensnack-policy"
  path = "/"
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect   = "Allow",
      Action   = ["s3:ListBucket"],
      Resource = "*"
    }]
  })
}

resource "aws_iam_user_policy_attachment" "attach" {
  user       = aws_iam_user.u.name
  policy_arn = aws_iam_policy.p.arn
}

output "iam_user" {
  value = aws_iam_user.u.name
}
