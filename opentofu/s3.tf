# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_s3_bucket" "test_bucket" {
  bucket = "opensnack-test-bucket"
}
resource "aws_s3_object" "hello" {
  bucket  = aws_s3_bucket.test_bucket.id
  key     = "hello.txt"
  content = "Hello from OpenSnack!"
}

resource "aws_s3_object" "nested" {
  bucket  = aws_s3_bucket.test_bucket.id
  key     = "a/b/c/data.json"
  content = jsonencode({ value = 123 })
}

output "hello_etag" {
  value = aws_s3_object.hello.etag
}

output "nested_etag" {
  value = aws_s3_object.nested.etag
}
