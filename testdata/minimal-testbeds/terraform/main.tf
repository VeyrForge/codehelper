locals {
  name_prefix = "codehelper-${var.region}"
}

module "vpc" {
  source = "./modules/vpc"
  region = var.region
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]

  filter {
    name   = "name"
    values = [var.ami_name]
  }
}

resource "aws_instance" "web" {
  ami           = data.aws_ami.ubuntu.id
  instance_type = "t3.micro"
  subnet_id     = module.vpc.public_subnet_id

  tags = {
    Name = local.name_prefix
  }
}
