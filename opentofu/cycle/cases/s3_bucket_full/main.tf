# S3 doesn't check the key exists, and an aws_kms_key would outlive destroy
# (it only schedules deletion), so the case names a key ARN directly.
data "aws_caller_identity" "me" {}

locals {
  kms_key_arn = "arn:aws:kms:us-east-1:${data.aws_caller_identity.me.account_id}:key/0b7c5c6e-3f43-4b8c-9a40-5d8a2f1c9e11"
}

resource "aws_s3_bucket" "b" {
  bucket = "cycle-full"
  tags = {
    Name    = "cycle-full"
    Variant = var.variant
  }
}

resource "aws_s3_bucket_versioning" "v" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "e" {
  bucket = aws_s3_bucket.b.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = var.variant == "a" ? "AES256" : "aws:kms"
      kms_master_key_id = var.variant == "a" ? null : local.kms_key_arn
    }
    bucket_key_enabled = var.variant == "b"
  }
}

resource "aws_s3_bucket_public_access_block" "p" {
  bucket                  = aws_s3_bucket.b.id
  block_public_acls       = true
  block_public_policy     = var.variant == "a"
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "o" {
  bucket = aws_s3_bucket.b.id
  rule {
    object_ownership = var.variant == "a" ? "BucketOwnerEnforced" : "BucketOwnerPreferred"
  }
}

resource "aws_s3_bucket_cors_configuration" "c" {
  bucket = aws_s3_bucket.b.id

  cors_rule {
    allowed_headers = ["*"]
    allowed_methods = ["GET", "HEAD"]
    allowed_origins = ["https://example.com"]
    expose_headers  = ["ETag"]
    max_age_seconds = var.variant == "a" ? 3000 : 600
  }

  cors_rule {
    id              = "uploads"
    allowed_methods = ["PUT"]
    allowed_origins = ["*"]
  }
}

resource "aws_s3_bucket_policy" "pol" {
  bucket = aws_s3_bucket.b.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = var.variant == "a" ? "DenyInsecureTransport" : "DenyPlainHTTP"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.b.arn, "${aws_s3_bucket.b.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}
