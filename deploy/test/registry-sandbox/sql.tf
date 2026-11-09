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

# Shared private-VPC PostgreSQL 16 pool (`instance_sql.sql_instances`) backing
# per-instance databases (`grain_<entry>`) for hosted GCE VM instances.
# Running a dedicated private-IP GCE VM on `grain-reg-subnet` works in standard
# sandbox projects without requiring `servicenetworking.services.addPeering`.

resource "tls_private_key" "sql_admin" {
  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

locals {
  sql_admin_password = substr(sha256(tls_private_key.sql_admin.private_key_pem), 0, 48)
  sql_vm_config = jsonencode({
    admin_password = local.sql_admin_password
  })
}

resource "google_compute_address" "instance_sql" {
  name         = "${var.name_prefix}-sql-ip"
  region       = var.region
  address_type = "INTERNAL"
  subnetwork   = google_compute_subnetwork.this.id
}

resource "google_compute_instance" "instance_sql" {
  name                      = "${var.name_prefix}-sql"
  machine_type              = var.sql_machine_type
  zone                      = var.zone
  tags                      = ["${var.name_prefix}-sql"]
  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = var.sql_boot_disk_gb
      type  = "pd-balanced"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.this.id
    network_ip = google_compute_address.instance_sql.address
  }

  service_account {
    email  = local.vm_sa_email
    scopes = ["https://www.googleapis.com/auth/cloud-platform.read-only"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata = {
    enable-oslogin   = "TRUE"
    grain-sql-config = local.sql_vm_config
  }

  metadata_startup_script = file("${path.module}/files/sql-startup.sh")

  lifecycle {
    ignore_changes = [metadata_startup_script]
  }

  depends_on = [
    google_compute_router_nat.this,
  ]
}
