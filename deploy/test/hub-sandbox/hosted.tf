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

# Hosted GCE VM instances (`kind = instance`, `grain-vm-*`) can run in a
# project separate from the registry/hub (`hosted_project_id`). In that
# cross-project mode this file provisions, in the hosted project:
#
#   - a dedicated VPC + subnet (`<prefix>-hosted-net` / `<prefix>-hosted-subnet`)
#     with Private Google Access and Cloud NAT egress (image pulls, apt),
#   - bidirectional VPC Network Peering with the registry VPC (`<prefix>-net`)
#     so the registry VM proxies to hosted VMs' private IPs and hosted VMs
#     reach the shared PostgreSQL pool VM over the peering,
#   - ingress firewall rules (registry VM -> hosted VMs on TCP:8080, IAP SSH),
#   - optionally (`manage_hosted_project_iam`) the project IAM bindings the
#     registry VM's service account needs to create hosted VMs and their
#     per-instance service accounts there.
#
# Firewall rules across a peering cannot match source tags/service accounts,
# so cross-project rules use CIDR ranges.

locals {
  hosted_project_id = trimspace(var.hosted_project_id) != "" ? trimspace(var.hosted_project_id) : var.project_id
  hosted_cross      = local.hosted_project_id != var.project_id

  # Roles the registry VM service account needs in the hosted project to
  # manage GCE instance lifecycle and per-instance service accounts. Secret
  # Manager secrets stay in the registry project and the instance SQL pool is
  # password-authenticated, so no secretmanager/cloudsql roles are required in
  # the hosted project.
  # No roles/iam.serviceAccountUser: the registry grants itself that role on
  # each per-instance service account it creates (compute.grant_self_act_as,
  # default true) through roles/iam.serviceAccountAdmin's
  # iam.serviceAccounts.setIamPolicy, so it never needs actAs project-wide.
  # See docs/hub.md, "IAM the registry needs".
  hosted_project_registry_roles = toset([
    "roles/compute.instanceAdmin.v1",
    "roles/iam.serviceAccountAdmin",
  ])

  hosted_network_name = local.hosted_cross ? google_compute_network.hosted[0].name : google_compute_network.this.name
  hosted_subnet_name  = local.hosted_cross ? google_compute_subnetwork.hosted[0].name : google_compute_subnetwork.this.name
  hosted_subnet_cidr  = local.hosted_cross ? google_compute_subnetwork.hosted[0].ip_cidr_range : google_compute_subnetwork.this.ip_cidr_range
}

provider "google" {
  alias       = "hosted"
  project     = local.hosted_project_id
  region      = var.region
  zone        = var.zone
  credentials = trimspace(var.hosted_credentials_file) != "" ? file(trimspace(var.hosted_credentials_file)) : null
}

resource "google_project_service" "hosted" {
  provider           = google.hosted
  for_each           = local.hosted_cross ? toset(["compute.googleapis.com", "iam.googleapis.com"]) : toset([])
  project            = local.hosted_project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "hosted" {
  provider                = google.hosted
  count                   = local.hosted_cross ? 1 : 0
  project                 = local.hosted_project_id
  name                    = "${var.name_prefix}-hosted-net"
  auto_create_subnetworks = false

  depends_on = [google_project_service.hosted]
}

resource "google_compute_subnetwork" "hosted" {
  provider                 = google.hosted
  count                    = local.hosted_cross ? 1 : 0
  project                  = local.hosted_project_id
  name                     = "${var.name_prefix}-hosted-subnet"
  ip_cidr_range            = var.hosted_subnet_cidr
  region                   = var.region
  network                  = google_compute_network.hosted[0].id
  private_ip_google_access = true
}

resource "google_compute_router" "hosted" {
  provider = google.hosted
  count    = local.hosted_cross ? 1 : 0
  project  = local.hosted_project_id
  name     = "${var.name_prefix}-hosted-router"
  region   = var.region
  network  = google_compute_network.hosted[0].id
}

resource "google_compute_router_nat" "hosted" {
  provider                           = google.hosted
  count                              = local.hosted_cross ? 1 : 0
  project                            = local.hosted_project_id
  name                               = "${var.name_prefix}-hosted-nat"
  router                             = google_compute_router.hosted[0].name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

# Peering changes on one network must be serialized, so the hosted side waits
# for the registry side.
resource "google_compute_network_peering" "registry_to_hosted" {
  count        = local.hosted_cross ? 1 : 0
  name         = "${var.name_prefix}-to-hosted"
  network      = google_compute_network.this.self_link
  peer_network = google_compute_network.hosted[0].self_link
}

resource "google_compute_network_peering" "hosted_to_registry" {
  provider     = google.hosted
  count        = local.hosted_cross ? 1 : 0
  name         = "${var.name_prefix}-hosted-to-reg"
  network      = google_compute_network.hosted[0].self_link
  peer_network = google_compute_network.this.self_link

  depends_on = [google_compute_network_peering.registry_to_hosted]
}

# Registry VM -> hosted VMs (`grain-instance`) health probes and HTTP proxying.
resource "google_compute_firewall" "hosted_from_registry" {
  provider = google.hosted
  count    = local.hosted_cross ? 1 : 0
  project  = local.hosted_project_id
  name     = "${var.name_prefix}-hosted-allow-reg"
  network  = google_compute_network.hosted[0].name

  allow {
    protocol = "tcp"
    ports    = ["8080"]
  }

  source_ranges = ["${google_compute_instance.registry.network_interface[0].network_ip}/32"]
  target_tags   = ["grain-instance"]
}

# Cloud IAP TCP forwarding for break-glass SSH (`/dev/kvm` checks, logs).
resource "google_compute_firewall" "hosted_iap" {
  provider = google.hosted
  count    = local.hosted_cross ? 1 : 0
  project  = local.hosted_project_id
  name     = "${var.name_prefix}-hosted-allow-iap"
  network  = google_compute_network.hosted[0].name

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["grain-instance"]
}

# Hosted VMs (peered subnet) -> shared PostgreSQL pool VM in the registry VPC.
resource "google_compute_firewall" "sql_from_hosted" {
  count   = local.hosted_cross ? 1 : 0
  name    = "${var.name_prefix}-allow-hosted-to-sql"
  network = google_compute_network.this.name

  allow {
    protocol = "tcp"
    ports    = ["5432"]
  }

  source_ranges = [google_compute_subnetwork.hosted[0].ip_cidr_range]
  target_tags   = ["${var.name_prefix}-sql"]
}

resource "google_project_iam_member" "hosted_registry" {
  provider = google.hosted
  for_each = local.hosted_cross && var.manage_hosted_project_iam ? local.hosted_project_registry_roles : toset([])
  project  = local.hosted_project_id
  role     = each.value
  member   = "serviceAccount:${local.vm_sa_email}"
}
