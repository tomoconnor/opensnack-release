resource "aws_cloudfront_cache_policy" "c" {
  name        = "cycle-cache-policy"
  comment     = "cache ${var.variant}"
  min_ttl     = 1
  default_ttl = var.variant == "a" ? 3600 : 7200
  max_ttl     = 86400

  parameters_in_cache_key_and_forwarded_to_origin {
    enable_accept_encoding_gzip   = true
    enable_accept_encoding_brotli = true

    cookies_config {
      cookie_behavior = "none"
    }
    headers_config {
      header_behavior = "whitelist"
      headers {
        items = ["Origin"]
      }
    }
    query_strings_config {
      query_string_behavior = "whitelist"
      query_strings {
        items = var.variant == "a" ? ["page"] : ["page", "lang"]
      }
    }
  }
}

resource "aws_cloudfront_origin_request_policy" "o" {
  name    = "cycle-origin-request-policy"
  comment = "origin ${var.variant}"

  cookies_config {
    cookie_behavior = "none"
  }
  headers_config {
    header_behavior = "whitelist"
    headers {
      items = ["Accept-Language"]
    }
  }
  query_strings_config {
    query_string_behavior = var.variant == "a" ? "all" : "none"
  }
}

resource "aws_cloudfront_origin_access_control" "oac" {
  name                              = "cycle-policies-oac"
  description                       = "oac ${var.variant}"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = var.variant == "a" ? "always" : "no-override"
  signing_protocol                  = "sigv4"
}

data "aws_cloudfront_origin_request_policy" "cors" {
  name = "Managed-CORS-S3Origin"
}
