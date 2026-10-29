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

# Least-privilege GCP service accounts and Workload Identity bindings for Grain on GKE.
#
# Per docs/design/grain-on-gke.md ("Workload Identity"):
# - grain-daemon@... is bound to Kubernetes ServiceAccount grain/grain and gets:
#   - roles/cloudsql.client and roles/cloudsql.instanceUser (for Cloud SQL IAM auth)
#   - roles/secretmanager.secretAccessor on each named secret (never project-wide)
#   - roles/secretmanager.secretVersionAdder on grain-sandbox-ca-key (to bootstrap the sandbox-access CA)
#   - roles/storage.objectAdmin on the staging/backups bucket (never project-wide)
#   - nothing else.
# - Sandbox pods run as Kubernetes ServiceAccount grain-sandbox with NO Workload
#   Identity binding and automountServiceAccountToken: false.

resource "google_service_account" "node_pool" {
  account_id   = "grain-gke-node"
  display_name = "Grain GKE node pool service account"
  project      = var.project_id

  depends_on = [google_project_service.services]
}

resource "google_project_iam_member" "node_pool_default" {
  project = var.project_id
  role    = "roles/container.defaultNodeServiceAccount"
  member  = "serviceAccount:${google_service_account.node_pool.email}"
}

resource "google_service_account" "grain_daemon" {
  account_id   = "grain-daemon"
  display_name = "Grain daemon Workload Identity service account"
  project      = var.project_id

  depends_on = [google_project_service.services]
}

resource "google_service_account_iam_member" "grain_daemon_workload_identity" {
  service_account_id = google_service_account.grain_daemon.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${var.namespace}/grain]"

  depends_on = [
    google_container_cluster.grain,
    data.google_container_cluster.existing,
  ]
}

resource "google_project_iam_member" "grain_daemon_cloudsql_client" {
  count   = var.postgres_mode == "cloudsql" ? 1 : 0
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.grain_daemon.email}"
}

resource "google_project_iam_member" "grain_daemon_cloudsql_instance_user" {
  count   = var.postgres_mode == "cloudsql" ? 1 : 0
  project = var.project_id
  role    = "roles/cloudsql.instanceUser"
  member  = "serviceAccount:${google_service_account.grain_daemon.email}"
}

locals {
  staging_bucket_name = var.staging_bucket_name != "" ? var.staging_bucket_name : "${var.project_id}-grain-staging"
}

resource "google_storage_bucket" "staging" {
  name                        = local.staging_bucket_name
  project                     = var.project_id
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  labels                      = var.labels

  lifecycle_rule {
    condition {
      age = 30
    }
    action {
      type = "Delete"
    }
  }

  depends_on = [google_project_service.services]
}

resource "google_storage_bucket_iam_member" "grain_daemon_staging_admin" {
  bucket = google_storage_bucket.staging.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.grain_daemon.email}"
}
