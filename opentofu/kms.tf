# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

resource "aws_kms_key" "k" {
  description = "OpenSnack test key"
}

output "kms_key_id" {
  value = aws_kms_key.k.key_id
}
