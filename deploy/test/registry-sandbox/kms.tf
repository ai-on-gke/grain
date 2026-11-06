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

data "google_project" "this" {
  project_id = var.project_id
}

resource "google_service_account" "vm" {
  account_id   = "${var.name_prefix}-vm"
  display_name = "Grain Registry Sandbox VM (${var.name_prefix})"

  depends_on = [google_project_service.enabled["iam.googleapis.com"]]
}

resource "google_project_iam_member" "vm_ar_reader" {
  project = var.project_id
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_project_iam_member" "vm_log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_service_account" "verifier" {
  count        = var.create_verifier_sa ? 1 : 0
  account_id   = "${var.name_prefix}-ver"
  display_name = "Grain Registry Sandbox Verifier (${var.name_prefix})"

  depends_on = [google_project_service.enabled["iam.googleapis.com"]]
}

# Cloud KMS key rings and crypto keys cannot be deleted in GCP (only their key
# versions are destroyed). `up.sh` automatically imports pre-existing resources
# on re-runs, or callers can pass `existing_kms_key_id`.
resource "google_kms_key_ring" "this" {
  count    = var.existing_kms_key_id == "" ? 1 : 0
  name     = "${var.name_prefix}-kr"
  location = var.region

  depends_on = [google_project_service.enabled["cloudkms.googleapis.com"]]
}

resource "google_kms_crypto_key" "signing" {
  count                      = var.existing_kms_key_id == "" ? 1 : 0
  name                       = "${var.name_prefix}-jwt"
  key_ring                   = google_kms_key_ring.this[0].id
  purpose                    = "ASYMMETRIC_SIGN"
  destroy_scheduled_duration = "86400s"

  version_template {
    algorithm        = "EC_SIGN_P256_SHA256"
    protection_level = "SOFTWARE"
  }
}

locals {
  kms_key_id = var.existing_kms_key_id != "" ? var.existing_kms_key_id : google_kms_crypto_key.signing[0].id
}

# `pkg/registry/providers/gcp` lists key versions (`roles/cloudkms.viewer`) and
# fetches public keys + signs assertions (`roles/cloudkms.signerVerifier`).
resource "google_kms_crypto_key_iam_member" "vm_signer" {
  crypto_key_id = local.kms_key_id
  role          = "roles/cloudkms.signerVerifier"
  member        = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_kms_crypto_key_iam_member" "vm_viewer" {
  crypto_key_id = local.kms_key_id
  role          = "roles/cloudkms.viewer"
  member        = "serviceAccount:${google_service_account.vm.email}"
}
