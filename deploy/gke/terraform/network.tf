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
    "certificatemanager.googleapis.com",
    "compute.googleapis.com",
    "container.googleapis.com",
    "dns.googleapis.com",
    "iam.googleapis.com",
    "iap.googleapis.com",
    "secretmanager.googleapis.com",
    "servicenetworking.googleapis.com",
    "sqladmin.googleapis.com",
    "storage.googleapis.com",
  ])
}

resource "google_project_service" "services" {
  for_each           = local.required_services
  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "grain" {
  name                    = "${var.cluster_name}-net"
  project                 = var.project_id
  auto_create_subnetworks = false

  depends_on = [google_project_service.services]
}

resource "google_compute_subnetwork" "grain" {
  name                     = "${var.cluster_name}-subnet"
  project                  = var.project_id
  region                   = var.region
  network                  = google_compute_network.grain.id
  ip_cidr_range            = "10.40.0.0/20"
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = "10.48.0.0/14"
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = "10.52.0.0/20"
  }
}

resource "google_compute_subnetwork" "proxy_only" {
  name          = "${var.cluster_name}-proxy-only"
  project       = var.project_id
  region        = var.region
  network       = google_compute_network.grain.id
  ip_cidr_range = "10.129.0.0/23"
  purpose       = "REGIONAL_MANAGED_PROXY"
  role          = "ACTIVE"
}

resource "google_compute_router" "grain" {
  name    = "${var.cluster_name}-router"
  project = var.project_id
  region  = var.region
  network = google_compute_network.grain.id
}

resource "google_compute_router_nat" "grain" {
  name                               = "${var.cluster_name}-nat"
  project                            = var.project_id
  region                             = var.region
  router                             = google_compute_router.grain.name
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

resource "google_compute_global_address" "private_service_range" {
  count         = var.postgres_mode == "cloudsql" ? 1 : 0
  name          = "${var.cluster_name}-psa-range"
  project       = var.project_id
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 16
  network       = google_compute_network.grain.id
}

resource "google_service_networking_connection" "private_vpc_connection" {
  count                   = var.postgres_mode == "cloudsql" ? 1 : 0
  network                 = google_compute_network.grain.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_service_range[0].name]
}
