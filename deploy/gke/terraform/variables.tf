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

# Terraform module for deploying Grain on GKE (docs/design/grain-on-gke.md).
#
# Provisions:
# - VPC network, subnet with pod/service secondary ranges, proxy-only subnet,
#   Cloud Router + Cloud NAT, and Private Service Access for Cloud SQL;
# - GKE cluster (or adopts an existing cluster) with Workload Identity,
#   Dataplane V2 (NetworkPolicy enforcement), Gateway API, Secret Manager CSI
#   driver, a daemon node pool, and a nested-virtualization granule sandbox
#   node pool with GKE_METADATA;
# - Cloud SQL for PostgreSQL 16 (private IP + IAM authentication) when
#   postgres_mode == "cloudsql";
# - Least-privilege grain-daemon GCP service account bound to Kubernetes
#   ServiceAccount grain/grain via Workload Identity;
# - Secret Manager secret containers (no secret values in Terraform state);
# - Global static IP, managed TLS certificate, Cloud DNS A record, and IAP
#   OAuth brand/client configuration.

variable "project_id" {
  description = "GCP project ID hosting the GKE cluster, Cloud SQL instance, and Secret Manager secrets."
  type        = string
}

variable "region" {
  description = "GCP region for the GKE cluster, VPC subnetwork, and Cloud SQL instance."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "Primary GCP zone for zonal resources and single-zone node pools."
  type        = string
  default     = "us-central1-a"
}

variable "cluster_name" {
  description = "Name of the GKE cluster to create or adopt."
  type        = string
  default     = "grain-gke"
}

variable "adopt_existing_cluster" {
  description = "When true, adopt an existing GKE cluster named var.cluster_name instead of creating a new google_container_cluster."
  type        = bool
  default     = false
}

variable "namespace" {
  description = "Kubernetes namespace in which the grain daemon and sandbox pods run."
  type        = string
  default     = "grain"
}

variable "daemon_machine_type" {
  description = "Machine type for the GKE node pool running the grain daemon and github-webhook."
  type        = string
  default     = "e2-standard-4"
}

variable "daemon_node_count" {
  description = "Number of nodes in the daemon node pool."
  type        = number
  default     = 1
}

variable "sandbox_machine_type" {
  description = "Machine type for the nested-virtualization granule sandbox node pool (must support Intel VT-x / AMD-V nested virtualization, e.g. n2-standard-16 or c3-standard-22)."
  type        = string
  default     = "n2-standard-16"
}

variable "sandbox_min_nodes" {
  description = "Minimum number of nodes in the autoscaled granule sandbox node pool."
  type        = number
  default     = 1
}

variable "sandbox_max_nodes" {
  description = "Maximum number of nodes in the autoscaled granule sandbox node pool."
  type        = number
  default     = 8
}

variable "sandbox_disk_size_gb" {
  description = "Boot/ephemeral disk size in GiB for each granule sandbox node."
  type        = number
  default     = 200
}

variable "postgres_mode" {
  description = "PostgreSQL deployment mode: \"cloudsql\" (recommended, Cloud SQL for PostgreSQL 16 with private IP and IAM auth) or \"in-cluster\" (StatefulSet in the grain namespace)."
  type        = string
  default     = "cloudsql"

  validation {
    condition     = contains(["cloudsql", "in-cluster"], var.postgres_mode)
    error_message = "postgres_mode must be either \"cloudsql\" or \"in-cluster\"."
  }
}

variable "cloudsql_instance_name" {
  description = "Name of the Cloud SQL for PostgreSQL 16 instance when postgres_mode == \"cloudsql\"."
  type        = string
  default     = "grain-postgres"
}

variable "cloudsql_tier" {
  description = "Machine tier for the Cloud SQL instance."
  type        = string
  default     = "db-custom-2-8192"
}

variable "cloudsql_availability_type" {
  description = "Cloud SQL availability type: \"REGIONAL\" for HA or \"ZONAL\"."
  type        = string
  default     = "REGIONAL"
}

variable "cloudsql_disk_size_gb" {
  description = "Initial storage size in GiB for the Cloud SQL instance."
  type        = number
  default     = 50
}

variable "cloudsql_deletion_protection" {
  description = "Enable deletion protection on the Cloud SQL instance."
  type        = bool
  default     = true
}

variable "domain" {
  description = "Fully qualified DNS name for the IAP-protected GKE Gateway (e.g. \"grain.example.com\")."
  type        = string
}

variable "dns_managed_zone" {
  description = "Optional existing Cloud DNS managed zone name in which to create the A record for var.domain. Leave empty if DNS is managed externally."
  type        = string
  default     = ""
}

variable "iap_allowed_members" {
  description = "IAM principals granted roles/iap.httpsResourceAccessor on the UI and hostd backend services (e.g. [\"user:operator@example.com\"])."
  type        = list(string)
  default     = []
}

variable "create_iap_brand" {
  description = "Whether to create the project's OAuth consent screen brand and IAP OAuth client via Terraform."
  type        = bool
  default     = false
}

variable "iap_support_email" {
  description = "Support email address for the IAP OAuth brand when create_iap_brand is true."
  type        = string
  default     = ""
}

variable "iap_client_id" {
  description = "Existing IAP OAuth 2.0 client ID when create_iap_brand is false."
  type        = string
  default     = ""
}

variable "iap_client_secret" {
  description = "Existing IAP OAuth 2.0 client secret when create_iap_brand is false (prefer passing viaSecret Manager or kubectl secret outside Terraform state)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "staging_bucket_name" {
  description = "Optional GCS bucket name for scheduled backups and PVC migration staging. Defaults to \"<project_id>-grain-staging\"."
  type        = string
  default     = ""
}

variable "labels" {
  description = "Resource labels applied to GCP resources created by this module."
  type        = map(string)
  default = {
    app        = "grain"
    managed-by = "terraform"
  }
}
