output "instance_id" {
  value = aws_instance.web.id
}

output "subnet_id" {
  value = module.vpc.public_subnet_id
}
