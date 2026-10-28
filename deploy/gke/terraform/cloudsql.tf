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

resource "google_sql_database_instance" "grain" {
  count               = var.postgres_mode == "cloudsql" ? 1 : 0
  name                = var.cloudsql_instance_name
  project             = var.project_id
  region              = var.region
  database_version    = "POSTGRES_16"
  deletion_protection = var.cloudsql_deletion_protection

  settings {
    tier              = var.cloudsql_tier
    availability_type = var.cloudsql_availability_type
    disk_size         = var.cloudsql_disk_size_gb
    disk_type         = "PD_SSD"
    disk_autoresize   = true

    database_flags {
      name  = "cloudsql.iam_authentication"
      value = "on"
    }

    ip_configuration {
      ipv4_enabled                                  = false
      private_network                               = google_compute_network.grain.id
      enable_private_path_for_google_cloud_services = true
    }

    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      start_time                     = "04:00"
    }

    user_labels = var.labels
  }

  depends_on = [google_service_networking_connection.private_vpc_connection]
}

resource "google_sql_database" "grain" {
  count    = var.postgres_mode == "cloudsql" ? 1 : 0
  name     = "grain"
  project  = var.project_id
  instance = google_sql_database_instance.grain[0].name
}

resource "google_sql_user" "grain_daemon" {
  count    = var.postgres_mode == "cloudsql" ? 1 : 0
  name     = trimsuffix(google_service_account.grain_daemon.email, ".gserviceaccount.com")
  project  = var.project_id
  instance = google_sql_database_instance.grain[0].name
  type     = "CLOUD_IAM_SERVICE_ACCOUNT"
}
