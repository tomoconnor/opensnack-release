resource "aws_iam_role" "repl" {
  name = "cycle-replication"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "s3.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_s3_bucket" "home" {
  bucket        = "cycle-repl-home"
  force_destroy = true
}

resource "aws_s3_bucket" "stop" {
  provider      = aws.west
  bucket        = "cycle-repl-stop"
  force_destroy = true
}

resource "aws_s3_bucket_versioning" "home" {
  bucket = aws_s3_bucket.home.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_versioning" "stop" {
  provider = aws.west
  bucket   = aws_s3_bucket.stop.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_replication_configuration" "r" {
  depends_on = [aws_s3_bucket_versioning.home, aws_s3_bucket_versioning.stop]
  role       = aws_iam_role.repl.arn
  bucket     = aws_s3_bucket.home.id

  rule {
    id       = "presents"
    priority = 2
    status   = "Enabled"
    filter { prefix = var.variant == "a" ? "" : "presents/" }
    delete_marker_replication { status = var.variant == "a" ? "Disabled" : "Enabled" }
    destination {
      bucket        = aws_s3_bucket.stop.arn
      storage_class = "STANDARD"
    }
  }

  rule {
    id       = "tagged"
    priority = 1
    status   = "Enabled"
    filter {
      and {
        prefix = "list/"
        tags   = { naughty = "true" }
      }
    }
    delete_marker_replication { status = "Disabled" }
    destination {
      bucket        = aws_s3_bucket.stop.arn
      storage_class = "STANDARD_IA"
      metrics {
        status = "Enabled"
        event_threshold { minutes = 15 }
      }
      replication_time {
        status = "Enabled"
        time { minutes = 15 }
      }
    }
  }
}

resource "aws_s3_object" "present" {
  depends_on = [aws_s3_bucket_replication_configuration.r]
  bucket     = aws_s3_bucket.home.id
  key        = "presents/bike.txt"
  content    = "a bike for alf"
}
