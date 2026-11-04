terraform {
  required_version = ">= 1.6.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

variable "project_id" {
  type        = string
  description = "GCP project that hosts the ledger reference stack."
}

variable "region" {
  type        = string
  default     = "asia-south1"
  description = "Region close to the primary user base."
}

resource "google_pubsub_topic" "payments" {
  name = "ledger-payments"
}

resource "google_sql_database_instance" "ledger" {
  name             = "ledger-postgres"
  database_version = "POSTGRES_15"
  region           = var.region

  settings {
    tier = "db-f1-micro"
  }

  deletion_protection = false
}

resource "google_container_cluster" "ledger" {
  name               = "ledger-gke"
  location           = var.region
  initial_node_count = 1

  deletion_protection = false
}

output "pubsub_topic" {
  value = google_pubsub_topic.payments.name
}

output "sql_connection_name" {
  value = google_sql_database_instance.ledger.connection_name
}

output "gke_cluster" {
  value = google_container_cluster.ledger.name
}
