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

data "google_container_cluster" "existing" {
  count    = var.adopt_existing_cluster ? 1 : 0
  name     = var.cluster_name
  location = var.region
  project  = var.project_id

  # grain's sandbox NetworkPolicies (the acceptor port, `network: none`
  # pods, the metadata server) are only objects on a cluster that does
  # not enforce them, so an adopted cluster must run Dataplane V2 or
  # network policy enforcement (docs/design/grain-on-gke.md, Security).
  lifecycle {
    postcondition {
      condition     = self.datapath_provider == "ADVANCED_DATAPATH" || try(self.network_policy[0].enabled, false)
      error_message = "The adopted cluster does not enforce NetworkPolicy (neither Dataplane V2 nor network policy enforcement); grain's sandbox NetworkPolicies would be ignored."
    }
  }
}

resource "google_container_cluster" "grain" {
  count    = var.adopt_existing_cluster ? 0 : 1
  name     = var.cluster_name
  location = var.region
  project  = var.project_id

  network    = google_compute_network.grain.id
  subnetwork = google_compute_subnetwork.grain.id

  remove_default_node_pool = true
  initial_node_count       = 1

  # Dataplane V2 enforces Kubernetes NetworkPolicy objects natively.
  datapath_provider = "ADVANCED_DATAPATH"

  ip_allocation_policy {
    cluster_secondary_range_name  = "pods"
    services_secondary_range_name = "services"
  }

  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  gateway_api_config {
    channel = "CHANNEL_STANDARD"
  }

  secret_manager_config {
    enabled = true
  }

  release_channel {
    channel = "REGULAR"
  }

  resource_labels = var.labels

  depends_on = [google_project_service.services]
}

locals {
  cluster_name = var.adopt_existing_cluster ? data.google_container_cluster.existing[0].name : google_container_cluster.grain[0].name
}

resource "google_container_node_pool" "daemon" {
  name       = "grain-daemon-pool"
  location   = var.region
  project    = var.project_id
  cluster    = local.cluster_name
  node_count = var.daemon_node_count

  node_locations = [var.zone]

  node_config {
    machine_type    = var.daemon_machine_type
    disk_size_gb    = 100
    disk_type       = "pd-balanced"
    service_account = google_service_account.node_pool.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    labels = {
      "grain.dev/pool" = "daemon"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }
}

resource "google_container_node_pool" "sandboxes" {
  name     = "grain-sandbox-pool"
  location = var.region
  project  = var.project_id
  cluster  = local.cluster_name

  node_locations = [var.zone]

  autoscaling {
    min_node_count = var.sandbox_min_nodes
    max_node_count = var.sandbox_max_nodes
  }

  node_config {
    machine_type    = var.sandbox_machine_type
    disk_size_gb    = var.sandbox_disk_size_gb
    disk_type       = "pd-balanced"
    image_type      = "UBUNTU_CONTAINERD"
    service_account = google_service_account.node_pool.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    # Nested virtualization for granule cloud-hypervisor /dev/kvm VMs.
    advanced_machine_features {
      threads_per_core             = 0
      enable_nested_virtualization = true
    }

    labels = {
      "grain.dev/pool"        = "sandboxes"
      "grain.dev/nested-virt" = "true"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }
}
