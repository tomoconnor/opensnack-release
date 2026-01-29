# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_lambda_function" "test" {
  function_name = "opensnack-lambda"

  role    = "arn:aws:iam::000000000000:role/fake"
  handler = "index.handler"
  runtime = "nodejs18.x"

  filename         = "doesnotmatter.zip"
  source_code_hash = filebase64sha256("doesnotmatter.zip")
}

output "lambda_name" {
  value = aws_lambda_function.test.function_name
}
