variable "region" {
  type = string
}

resource "aws_subnet" "public" {
  # stub — no real provider required for indexing
  cidr_block = "10.0.1.0/24"
}

output "public_subnet_id" {
  value = aws_subnet.public.id
}
