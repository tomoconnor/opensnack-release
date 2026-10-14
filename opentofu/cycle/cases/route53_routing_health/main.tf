resource "aws_route53_zone" "z" {
  name    = "north.example.com"
  comment = "routing policies"
}

locals {
  b = var.variant == "b"
}

resource "aws_route53_health_check" "primary" {
  fqdn              = "primary.north.example.com"
  port              = 443
  type              = "HTTPS"
  resource_path     = local.b ? "/healthz" : "/"
  failure_threshold = local.b ? 5 : 3
  request_interval  = 30

  tags = {
    Name    = "primary"
    Variant = var.variant
  }
}

resource "aws_route53_record" "primary" {
  zone_id         = aws_route53_zone.z.zone_id
  name            = "assets.north.example.com"
  type            = "A"
  ttl             = 60
  records         = ["192.0.2.1"]
  set_identifier  = "primary"
  health_check_id = aws_route53_health_check.primary.id

  failover_routing_policy {
    type = "PRIMARY"
  }
}

resource "aws_route53_record" "secondary" {
  zone_id        = aws_route53_zone.z.zone_id
  name           = "assets.north.example.com"
  type           = "A"
  ttl            = 60
  records        = [local.b ? "192.0.2.3" : "192.0.2.2"]
  set_identifier = "secondary"

  failover_routing_policy {
    type = "SECONDARY"
  }
}

resource "aws_route53_record" "latency" {
  for_each       = toset(["us-east-1", "eu-west-1"])
  zone_id        = aws_route53_zone.z.zone_id
  name           = "api.north.example.com"
  type           = "CNAME"
  ttl            = local.b ? 120 : 60
  records        = ["api-${each.key}.north.example.com"]
  set_identifier = each.key

  latency_routing_policy {
    region = each.key
  }
}

resource "aws_route53_record" "geo" {
  for_each = {
    europe  = { continent = "EU", country = null }
    default = { continent = null, country = "*" }
  }
  zone_id        = aws_route53_zone.z.zone_id
  name           = "worker.north.example.com"
  type           = "TXT"
  ttl            = 60
  records        = ["route ${each.key} ${var.variant}"]
  set_identifier = each.key

  geolocation_routing_policy {
    continent = each.value.continent
    country   = each.value.country
  }
}

resource "aws_route53_record" "multi" {
  for_each                         = toset(["one", "two"])
  zone_id                          = aws_route53_zone.z.zone_id
  name                             = "multi.north.example.com"
  type                             = "A"
  ttl                              = 60
  records                          = [each.key == "one" ? "192.0.2.51" : "192.0.2.52"]
  set_identifier                   = each.key
  multivalue_answer_routing_policy = true
}

resource "aws_route53_delegation_set" "d" {
  reference_name = "north"
}

resource "aws_route53_zone" "reused" {
  name              = "reused.example.com"
  delegation_set_id = aws_route53_delegation_set.d.id
  comment           = local.b ? "reused b" : "reused a"
}
