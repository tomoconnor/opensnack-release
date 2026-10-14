resource "aws_route53_zone" "z" {
  name = "orders.example.com"
}

locals {
  b = var.variant == "b"
}

resource "aws_route53_record" "origin" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "origin.orders.example.com"
  type    = "A"
  ttl     = 60
  records = ["192.0.2.10"]
}

# An alias to another record in the same zone.
resource "aws_route53_record" "app" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "app.orders.example.com"
  type    = "A"

  alias {
    name                   = aws_route53_record.origin.fqdn
    zone_id                = aws_route53_zone.z.zone_id
    evaluate_target_health = local.b
  }
}

# Aliases to AWS service endpoints, using their documented hosted zone IDs.
resource "aws_route53_record" "cdn" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "cdn.orders.example.com"
  type    = "A"

  alias {
    name                   = local.b ? "d222222abcdef8.cloudfront.net" : "d111111abcdef8.cloudfront.net"
    zone_id                = "Z2FDTNDATAQYW2"
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "site" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "orders.example.com"
  type    = "A"

  alias {
    name                   = "s3-website-us-east-1.amazonaws.com"
    zone_id                = "Z3AQBSTGFYJSTF"
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "blue" {
  zone_id        = aws_route53_zone.z.zone_id
  name           = "lb.orders.example.com"
  type           = "A"
  ttl            = 60
  set_identifier = "blue"
  records        = ["192.0.2.21"]

  weighted_routing_policy {
    weight = local.b ? 50 : 90
  }
}

resource "aws_route53_record" "green" {
  zone_id        = aws_route53_zone.z.zone_id
  name           = "lb.orders.example.com"
  type           = "A"
  ttl            = 60
  set_identifier = "green"
  records        = [local.b ? "192.0.2.23" : "192.0.2.22"]

  weighted_routing_policy {
    weight = local.b ? 50 : 10
  }
}
