terraform {
  required_version = ">= 1.6.0"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

variable "location" {
  type    = string
  default = "southeastasia"
}

variable "sql_admin_password" {
  type      = string
  sensitive = true
}

resource "azurerm_resource_group" "ledger" {
  name     = "ledger-rg"
  location = var.location
}

resource "azurerm_servicebus_namespace" "ledger" {
  name                = "ledger-servicebus"
  location            = azurerm_resource_group.ledger.location
  resource_group_name = azurerm_resource_group.ledger.name
  sku                 = "Standard"
}

resource "azurerm_servicebus_queue" "payments" {
  name         = "payments"
  namespace_id = azurerm_servicebus_namespace.ledger.id
}

resource "azurerm_kubernetes_cluster" "ledger" {
  name                = "ledger-aks"
  location            = azurerm_resource_group.ledger.location
  resource_group_name = azurerm_resource_group.ledger.name
  dns_prefix          = "ledger"

  default_node_pool {
    name       = "default"
    node_count = 1
    vm_size    = "Standard_B2s"
  }

  identity {
    type = "SystemAssigned"
  }
}

resource "azurerm_mssql_server" "ledger" {
  name                         = "ledger-sql"
  resource_group_name          = azurerm_resource_group.ledger.name
  location                     = azurerm_resource_group.ledger.location
  version                      = "12.0"
  administrator_login          = "ledgeradmin"
  administrator_login_password = var.sql_admin_password
}

resource "azurerm_mssql_database" "ledger" {
  name      = "ledger"
  server_id = azurerm_mssql_server.ledger.id
  sku_name  = "Basic"
}

output "servicebus_queue" {
  value = azurerm_servicebus_queue.payments.name
}

output "aks_cluster" {
  value = azurerm_kubernetes_cluster.ledger.name
}

output "sql_server" {
  value = azurerm_mssql_server.ledger.fully_qualified_domain_name
}
