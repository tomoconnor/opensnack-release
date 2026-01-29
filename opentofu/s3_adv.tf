# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_s3_bucket" "adv" {
  bucket = "opensnack-advanced"
}

resource "aws_s3_bucket_versioning" "ver" {
  bucket = aws_s3_bucket.adv.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_acl" "acl" {
  bucket = aws_s3_bucket.adv.id
  acl    = "private"
}


output "s3_bucket" {
  value = aws_s3_bucket.adv.bucket
}
