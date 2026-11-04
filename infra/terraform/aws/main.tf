terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region
}

variable "region" {
  type    = string
  default = "ap-south-1"
}

variable "eks_role_arn" {
  type        = string
  description = "Existing IAM role ARN with AmazonEKSClusterPolicy."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs for the EKS control plane."
}

variable "db_password" {
  type      = string
  sensitive = true
}

resource "aws_eks_cluster" "ledger" {
  name     = "ledger-eks"
  role_arn = var.eks_role_arn

  vpc_config {
    subnet_ids = var.subnet_ids
  }
}

resource "aws_db_instance" "ledger" {
  identifier        = "ledger-postgres"
  engine            = "postgres"
  engine_version    = "15"
  instance_class    = "db.t4g.micro"
  allocated_storage = 20
  username          = "ledger"
  password          = var.db_password

  skip_final_snapshot = true
}

output "eks_cluster" {
  value = aws_eks_cluster.ledger.name
}

output "rds_endpoint" {
  value = aws_db_instance.ledger.endpoint
}
