variable "region" {
  type        = string
  description = "AWS region"
  default     = "us-east-1"
}

variable "ami_name" {
  type    = string
  default = "ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"
}
