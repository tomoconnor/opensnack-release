resource "aws_s3_bucket" "b" {
  bucket = "cycle-site"
}

resource "aws_s3_bucket_website_configuration" "w" {
  bucket = aws_s3_bucket.b.id

  index_document {
    suffix = "index.html"
  }

  error_document {
    key = var.variant == "a" ? "error.html" : "404.html"
  }

  routing_rule {
    condition {
      key_prefix_equals = "docs/"
    }
    redirect {
      replace_key_prefix_with = "documents/"
    }
  }
}

# A bucket in another region, so GetBucketLocation and the website
# endpoint are exercised outside us-east-1.
resource "aws_s3_bucket" "west" {
  provider = aws.west
  bucket   = "cycle-site-west"
}

resource "aws_s3_bucket_website_configuration" "west" {
  provider = aws.west
  bucket   = aws_s3_bucket.west.id

  redirect_all_requests_to {
    host_name = var.variant == "a" ? "example.com" : "example.org"
    protocol  = "https"
  }
}
