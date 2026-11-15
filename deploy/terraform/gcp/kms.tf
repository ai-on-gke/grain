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

# The Cloud KMS key that wraps this deployment's secrets private key
# (design/design/secrets-key-protection.design.md §3a, Phase 2).
#
# secrets.key on the data disk is the one file that opens every
# credential this deployment holds. Wrapped, it is
# grain-secret-key-kms-v1:<this key>:<ciphertext>: useless without
# decrypt on this key, which only the host's own account holds. A PD
# snapshot, an export bundle or the secrets-key escrow in Secret Manager
# then carries nothing anyone else can open, every unwrap is a Cloud
# Audit Logs Data Access entry, and disabling a version or the grant
# below stops the next start from unwrapping it.
#
# The daemon unwraps once at start and keeps the key in memory, so this
# costs one decrypt per restart. If KMS cannot be reached or refuses,
# the daemon still starts and serves; anything that needs a secret fails
# with "KMS unreachable / permission denied for <key>" until KMS answers.
#
# files/deploy.sh hands the key's name to setup.sh, which wraps the key
# file through the daemon once (`grain state key wrap -kms`), and then
# replaces the secrets-key escrow with the wrapped form.
#
# A key ring and a key cannot be deleted from Cloud KMS, only their
# versions destroyed, so a destroy here schedules the versions for
# destruction and leaves the names taken: re-creating this deployment
# under the same name_prefix needs `terraform import` of both. Run
# `grain state key unwrap` on the host before destroying them, or the
# secrets on its disk cannot be read again.

resource "google_project_service" "cloudkms" {
  count              = var.secrets_key_kms ? 1 : 0
  project            = var.project_id
  service            = "cloudkms.googleapis.com"
  disable_on_destroy = false
}

# In the deployment's region, beside the host that uses it.
resource "google_kms_key_ring" "secrets" {
  count    = var.secrets_key_kms ? 1 : 0
  project  = var.project_id
  name     = "${var.name_prefix}-secrets"
  location = var.region

  depends_on = [google_project_service.cloudkms]
}

resource "google_kms_crypto_key" "secrets_key" {
  count    = var.secrets_key_kms ? 1 : 0
  name     = "secrets-key"
  key_ring = google_kms_key_ring.secrets[0].id
  purpose  = "ENCRYPT_DECRYPT"
  labels   = var.labels

  # KEK rotation is free and invisible: the decrypt response says when
  # the wrapped key is under a version that is no longer the primary, and
  # the daemon rewraps it at that start. It does not rotate the secrets
  # key itself.
  rotation_period = var.secrets_key_kms_rotation_period

  version_template {
    algorithm        = "GOOGLE_SYMMETRIC_ENCRYPTION"
    protection_level = "SOFTWARE"
  }
}

# The host's account, on this key only. Encrypt is needed as well as
# decrypt: the first wrap, and every rewrap after a rotation, encrypt.
resource "google_kms_crypto_key_iam_member" "host_wraps_secrets_key" {
  count         = var.secrets_key_kms ? 1 : 0
  crypto_key_id = google_kms_crypto_key.secrets_key[0].id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = "serviceAccount:${google_service_account.host.email}"
}

# Lets files/deploy.sh replace the secrets-key escrow with the wrapped
# form, on that one secret and only to add a version: secretVersionAdder
# carries no access, list or destroy.
resource "google_secret_manager_secret_iam_member" "host_escrows_wrapped_secrets_key" {
  count     = var.secrets_key_kms ? 1 : 0
  project   = var.project_id
  secret_id = google_secret_manager_secret.host["secrets-key"].secret_id
  role      = "roles/secretmanager.secretVersionAdder"
  member    = "serviceAccount:${google_service_account.host.email}"
}
