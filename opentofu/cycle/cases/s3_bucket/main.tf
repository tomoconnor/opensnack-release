resource "aws_s3_bucket" "b" {
  bucket = "cycle-bucket-${var.variant == "a" ? "x" : "x"}"
}

resource "aws_s3_bucket_versioning" "v" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration {
    status = var.variant == "a" ? "Enabled" : "Suspended"
  }
}
