resource "aws_s3_bucket" "b" {
  bucket        = "cycle-objects"
  force_destroy = true
}

resource "aws_s3_bucket_versioning" "v" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_object" "o" {
  bucket       = aws_s3_bucket_versioning.v.bucket
  key          = "dir/hello.txt"
  content      = "hello from variant ${var.variant}\n"
  content_type = "text/plain"
  tags = {
    Name    = "hello"
    Variant = var.variant
  }
}

resource "aws_s3_object_copy" "c" {
  bucket            = aws_s3_bucket_versioning.v.bucket
  key               = "copies/hello.txt"
  source            = "${aws_s3_object.o.bucket}/${aws_s3_object.o.key}"
  tagging_directive = "REPLACE"
  tags = {
    Copy = "yes"
  }
}
