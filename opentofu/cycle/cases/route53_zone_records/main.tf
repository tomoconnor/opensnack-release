resource "aws_route53_zone" "z" {
  name    = "Team.Example.com."
  comment = "factory ${var.variant}"
  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

locals {
  b   = var.variant == "b"
  ttl = local.b ? 600 : 300
  # A TXT value longer than 255 characters has to be split into quoted
  # strings; the provider adds the outer quotes.
  dkim = "v=DKIM1; k=rsa; p=${join("", [for i in range(6) : "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA${i}"])}"
}

resource "aws_route53_record" "a" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "www.team.example.com"
  type    = "A"
  ttl     = local.ttl
  records = local.b ? ["192.0.2.3"] : ["192.0.2.1", "192.0.2.2"]
}

resource "aws_route53_record" "aaaa" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "www.team.example.com"
  type    = "AAAA"
  ttl     = local.ttl
  records = [local.b ? "2001:db8::2" : "2001:db8::1"]
}

resource "aws_route53_record" "cname" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "docs.team.example.com"
  type    = "CNAME"
  ttl     = local.ttl
  records = [local.b ? "www2.team.example.com" : "www.team.example.com"]
}

resource "aws_route53_record" "wildcard" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "*.widgets.team.example.com"
  type    = "A"
  ttl     = local.ttl
  records = [local.b ? "192.0.2.20" : "192.0.2.10"]
}

resource "aws_route53_record" "txt" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "team.example.com"
  type    = "TXT"
  ttl     = local.ttl
  records = [
    local.b ? "v=spf1 include:_spf.example.com -all" : "v=spf1 -all",
    "${substr(local.dkim, 0, 255)}\" \"${substr(local.dkim, 255, -1)}",
  ]
}

resource "aws_route53_record" "mx" {
  zone_id = aws_route53_zone.z.zone_id
  name    = "team.example.com"
  type    = "MX"
  ttl     = local.ttl
  records = local.b ? ["5 mail.team.example.com"] : ["10 mail1.team.example.com", "20 mail2.team.example.com"]
}
