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
  count        = var.create_vm_service_account ? 1 : 0
  account_id   = "${var.name_prefix}-vm"
  display_name = "Grain Registry Sandbox VM (${var.name_prefix})"

  depends_on = [google_project_service.enabled["iam.googleapis.com"]]
}

resource "google_project_iam_member" "vm_ar_reader" {
  count   = var.create_vm_service_account ? 1 : 0
  project = var.project_id
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.vm[0].email}"
}

resource "google_project_iam_member" "vm_log_writer" {
  count   = var.create_vm_service_account ? 1 : 0
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.vm[0].email}"
}

locals {
  # No roles/iam.serviceAccountUser: the registry grants itself that role on
  # each per-instance service account it creates (compute.grant_self_act_as,
  # default true) through roles/iam.serviceAccountAdmin's
  # iam.serviceAccounts.setIamPolicy, so it never needs actAs project-wide.
  # See docs/hub.md, "IAM the registry needs".
  hosted_vm_registry_roles = toset([
    "roles/compute.instanceAdmin.v1",
    "roles/iam.serviceAccountAdmin",
    "roles/secretmanager.admin",
    "roles/cloudsql.client",
  ])
}

# Grant the registry service account permissions to manage hosted GCE VM
# instances (`grain-vm-*`), per-instance service accounts (`grain-sa-*`),
# per-instance secrets, and shared SQL database connections. When
# `create_vm_service_account` is false (sandbox projects without
# `resourcemanager.projects.setIamPolicy`), the VM runs under the Compute Engine
# default service account which already holds `roles/editor` in the sandbox.
resource "google_project_iam_member" "vm_hosted_provisioning" {
  for_each = var.create_vm_service_account ? local.hosted_vm_registry_roles : toset([])
  project  = var.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.vm[0].email}"
}

resource "google_service_account" "verifier" {
  count        = var.create_verifier_sa && trimspace(var.verifier_service_account_email) == "" ? 1 : 0
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
  vm_sa_email = length(google_service_account.vm) > 0 ? google_service_account.vm[0].email : "${data.google_project.this.number}-compute@developer.gserviceaccount.com"
  effective_verifier_sa = trimspace(var.verifier_service_account_email) != "" ? trimspace(var.verifier_service_account_email) : (
    length(google_service_account.verifier) > 0 ? google_service_account.verifier[0].email : ""
  )
  kms_key_id = var.existing_kms_key_id != "" ? var.existing_kms_key_id : google_kms_crypto_key.signing[0].id
}

# `pkg/hub/providers/gcp` lists key versions (`roles/cloudkms.viewer`) and
# fetches public keys + signs assertions (`roles/cloudkms.signerVerifier`).
resource "google_kms_crypto_key_iam_member" "vm_signer" {
  crypto_key_id = local.kms_key_id
  role          = "roles/cloudkms.signerVerifier"
  member        = "serviceAccount:${local.vm_sa_email}"
}

resource "google_kms_crypto_key_iam_member" "vm_viewer" {
  crypto_key_id = local.kms_key_id
  role          = "roles/cloudkms.viewer"
  member        = "serviceAccount:${local.vm_sa_email}"
}
