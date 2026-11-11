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
  required_services = toset([
    "artifactregistry.googleapis.com",
    "cloudkms.googleapis.com",
    "compute.googleapis.com",
    "iam.googleapis.com",
    "iap.googleapis.com",
    "secretmanager.googleapis.com",
    "sqladmin.googleapis.com",
  ])
}

resource "google_project_service" "enabled" {
  for_each           = local.required_services
  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "this" {
  name                    = "${var.name_prefix}-net"
  auto_create_subnetworks = false

  depends_on = [google_project_service.enabled["compute.googleapis.com"]]
}

resource "google_compute_subnetwork" "this" {
  name                     = "${var.name_prefix}-subnet"
  ip_cidr_range            = "10.42.0.0/24"
  region                   = var.region
  network                  = google_compute_network.this.id
  private_ip_google_access = true
}

# Cloud NAT gives the private-IP-only VM outbound package and container-registry
# egress while satisfying org policies that forbid external VM IPs.
resource "google_compute_router" "this" {
  name    = "${var.name_prefix}-router"
  region  = var.region
  network = google_compute_network.this.id
}

resource "google_compute_router_nat" "this" {
  name                               = "${var.name_prefix}-nat"
  router                             = google_compute_router.this.name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

# Allow Google Global External Application Load Balancer proxies and health
# checkers to reach grain-registry's main (:8080) and public (:8081) listeners.
resource "google_compute_firewall" "lb_to_registry" {
  name    = "${var.name_prefix}-allow-lb"
  network = google_compute_network.this.name

  allow {
    protocol = "tcp"
    ports    = ["8080", "8081"]
  }

  source_ranges = [
    "130.211.0.0/22",
    "35.191.0.0/16",
  ]
  target_tags = ["${var.name_prefix}-vm"]
}

# Allow Cloud IAP TCP forwarding (`gcloud compute ssh --tunnel-through-iap`)
# to SSH only (`enable-oslogin = "TRUE"`) for administrative operations
# (`register-entry.sh`, `up.sh` reconciliation, break-glass hosted VM checks, and diagnostics).
resource "google_compute_firewall" "iap_to_registry" {
  name    = "${var.name_prefix}-allow-iap-tcp"
  network = google_compute_network.this.name

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["${var.name_prefix}-vm", "${var.name_prefix}-sql", "grain-instance"]
}

# Allow the grain-registry VM to probe and proxy HTTP (:8080) traffic to hosted
# Grain GCE VM instances (`grain-vm-*`, tagged `grain-instance`) over the VPC.
resource "google_compute_firewall" "registry_to_hosted_vms" {
  name    = "${var.name_prefix}-allow-reg-to-vms"
  network = google_compute_network.this.name

  allow {
    protocol = "tcp"
    ports    = ["8080"]
  }

  source_tags = ["${var.name_prefix}-vm"]
  target_tags = ["grain-instance"]
}

# Allow the grain-registry VM (per-instance role/DB provisioning) and hosted
# Grain GCE VM instances (`grain-instance`) to reach the shared PostgreSQL pool
# on TCP:5432.
resource "google_compute_firewall" "to_instance_sql" {
  name    = "${var.name_prefix}-allow-to-sql"
  network = google_compute_network.this.name

  allow {
    protocol = "tcp"
    ports    = ["5432"]
  }

  source_tags = ["${var.name_prefix}-vm", "grain-instance"]
  target_tags = ["${var.name_prefix}-sql"]
}
