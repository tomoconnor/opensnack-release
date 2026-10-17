resource "aws_s3_bucket" "b" {
  bucket        = "cycle-lifecycle"
  force_destroy = true
}

resource "aws_s3_bucket_versioning" "b" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_lifecycle_configuration" "b" {
  bucket     = aws_s3_bucket.b.id
  depends_on = [aws_s3_bucket_versioning.b]

  rule {
    id     = "old-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days           = var.variant == "a" ? 30 : 60
      newer_noncurrent_versions = 3
    }
    noncurrent_version_transition {
      noncurrent_days = 7
      storage_class   = "STANDARD_IA"
    }
  }

  rule {
    id     = "logs"
    status = var.variant == "a" ? "Enabled" : "Disabled"
    filter { prefix = "logs/" }
    expiration { days = 90 }
    transition {
      days          = 30
      storage_class = "GLACIER"
    }
    abort_incomplete_multipart_upload { days_after_initiation = 2 }
  }

  rule {
    id     = "temp"
    status = "Enabled"
    filter {
      and {
        prefix                   = "tmp/"
        object_size_greater_than = 1024
        tags = {
          tier = "scratch"
        }
      }
    }
    expiration { expired_object_delete_marker = true }
  }

  rule {
    id     = "tagged"
    status = "Enabled"
    filter {
      tag {
        key   = "retain"
        value = "no"
      }
    }
    expiration { date = "2027-01-01T00:00:00Z" }
  }
}
