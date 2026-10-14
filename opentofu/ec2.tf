# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-2023*-x86_64"]
  }
}

resource "aws_instance" "vm" {
  ami           = data.aws_ami.al2023.id
  instance_type = "t3.micro"
}

# resource "aws_ebs_volume" "vol" {
#   size = 8
#   availability_zone = "us-east-1a"
# }

# resource "aws_volume_attachment" "va" {
#   volume_id   = aws_ebs_volume.vol.id
#   instance_id = aws_instance.vm.id
#   device_name = "/dev/sdh"
# }

output "instance_id" {
  value = aws_instance.vm.id
}
# output "volume_id" {
#   value = aws_ebs_volume.vol.id
# }