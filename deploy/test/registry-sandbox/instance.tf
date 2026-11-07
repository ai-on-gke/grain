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

locals {
  derived_domains = distinct([
    for e in local.human_emails : split("@", e)[1]
    if length(split("@", e)) == 2
  ])

  effective_allowed_domains = length(var.allowed_domains) > 0 ? [
    for d in var.allowed_domains : lower(trimspace(d))
  ] : local.derived_domains

  registry_vm_config = jsonencode({
    registry_image     = trimspace(var.registry_image)
    registry_host      = local.registry_host
    instance_domain    = local.instance_domain
    iap_audience       = local.iap_audience
    kms_key_name       = local.kms_key_id
    allowed_domains    = local.effective_allowed_domains
    admins             = ["user:${lower(trimspace(var.admin_email))}"]
    creators           = [for e in local.human_emails : "user:${e}"]
    service_principals = local.effective_verifier_sa != "" ? ["serviceAccount:${local.effective_verifier_sa}"] : []
  })
}

resource "google_compute_instance" "registry" {
  name                      = "${var.name_prefix}-vm"
  machine_type              = var.machine_type
  zone                      = var.zone
  tags                      = ["${var.name_prefix}-vm"]
  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = var.boot_disk_gb
      type  = "pd-balanced"
    }
  }

  # No external IP address; outbound internet access goes through Cloud NAT and
  # inbound traffic arrives exclusively through the Global HTTPS LB and IAP TCP.
  network_interface {
    subnetwork = google_compute_subnetwork.this.id
  }

  service_account {
    email  = local.vm_sa_email
    scopes = ["cloud-platform"]
  }

  metadata = {
    enable-oslogin        = "TRUE"
    grain-registry-config = local.registry_vm_config
  }

  metadata_startup_script = file("${path.module}/files/startup.sh")

  depends_on = [
    google_compute_router_nat.this,
    google_project_iam_member.vm_ar_reader,
    google_kms_crypto_key_iam_member.vm_signer,
    google_kms_crypto_key_iam_member.vm_viewer,
  ]
}

resource "google_compute_instance_group_membership" "registry" {
  zone           = var.zone
  instance_group = google_compute_instance_group.registry.name
  instance       = google_compute_instance.registry.self_link
}
