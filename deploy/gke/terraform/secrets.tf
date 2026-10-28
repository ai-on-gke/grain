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

# Secret Manager secret containers and per-secret IAM bindings for Grain on GKE.
#
# Secret versions (material) are never stored in Terraform state; they are
# pushed out-of-band via `scripts/deploy-gke.sh` (`gcloud secrets versions add --data-file=-`).

locals {
  grain_secret_ids = toset([
    "grain-secrets-key",
    "grain-sandbox-ca-key",
    "grain-state-repo-token",
    "grain-secrets-repo-token",
    "grain-github-app-private-key",
    "grain-github-webhook-secret",
    "grain-github-webhook-relay-secret",
  ])
}

resource "google_secret_manager_secret" "grain" {
  for_each  = local.grain_secret_ids
  project   = var.project_id
  secret_id = each.value
  labels    = var.labels

  replication {
    auto {}
  }

  depends_on = [google_project_service.services]
}

resource "google_secret_manager_secret_iam_member" "grain_daemon_accessor" {
  for_each  = google_secret_manager_secret.grain
  project   = var.project_id
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.grain_daemon.email}"
}
