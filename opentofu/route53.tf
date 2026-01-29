# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_route53_zone" "zone" {
  name = "opensnack.zone."
}

resource "aws_route53_record" "arec" {
  zone_id = aws_route53_zone.zone.zone_id
  name    = "test.opensnack.zone"
  type    = "A"
  ttl     = 300
  records = ["1.2.3.4"]
}

output "zone_id" {
  value = aws_route53_zone.zone.zone_id
}
