# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

output "cluster_name" {
  description = "Name of the created or adopted GKE cluster."
  value       = local.cluster_name
}

output "region" {
  description = "GCP region of the GKE cluster."
  value       = var.region
}

output "gateway_ip" {
  description = "Global static IPv4 address reserved for the GKE Gateway (grain-gateway-ip)."
  value       = google_compute_global_address.gateway_ip.address
}

output "gateway_url" {
  description = "HTTPS URL of the IAP-protected Grain UI and hostd endpoint."
  value       = "https://${var.domain}"
}

output "grain_daemon_service_account" {
  description = "Email of the grain-daemon GCP service account bound to Kubernetes ServiceAccount grain/grain."
  value       = google_service_account.grain_daemon.email
}

output "cloudsql_connection_name" {
  description = "Cloud SQL connection name (PROJECT:REGION:INSTANCE) when postgres_mode == \"cloudsql\"."
  value       = var.postgres_mode == "cloudsql" ? google_sql_database_instance.grain[0].connection_name : ""
}

output "cloudsql_private_ip" {
  description = "Private IPv4 address of the Cloud SQL instance when postgres_mode == \"cloudsql\"."
  value       = var.postgres_mode == "cloudsql" ? google_sql_database_instance.grain[0].private_ip_address : ""
}

output "staging_bucket" {
  description = "GCS bucket name used for PVC migration staging and scheduled backups."
  value       = google_storage_bucket.staging.name
}

output "iap_client_id" {
  description = "IAP OAuth 2.0 client ID, if configured."
  value       = local.iap_client_id
}
