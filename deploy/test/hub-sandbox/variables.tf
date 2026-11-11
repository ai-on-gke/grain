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

variable "project_id" {
  description = "GCP sandbox project ID to deploy the test registry into (for example my-gcp-sandbox)."
  type        = string

  validation {
    condition     = trimspace(var.project_id) != ""
    error_message = "project_id must not be empty."
  }
}

variable "region" {
  description = "GCP region for the subnet, Cloud NAT, and Cloud KMS signing key ring."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "GCP zone for the single test registry GCE VM."
  type        = string
  default     = "us-central1-a"
}

variable "compute_zones" {
  description = "GCE zones within var.region eligible for hosted Grain VM placement (compute.zones)."
  type        = list(string)
  default     = ["us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"]

  validation {
    condition     = length(var.compute_zones) > 0 && alltrue([for z in var.compute_zones : can(regex("^[a-z][a-z0-9-]*[a-z0-9]$", trimspace(z)))])
    error_message = "compute_zones must contain at least one valid GCE zone name."
  }
}

variable "name_prefix" {
  description = "Short prefix for sandbox resources (3-12 lowercase alphanumeric/hyphen characters so sslip.io domains fit GCP's 63-char managed SSL certificate limit)."
  type        = string
  default     = "grain-reg"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,10}[a-z0-9]$", var.name_prefix))
    error_message = "name_prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ so derived service-account, LB, and managed SSL certificate domains fit GCP length limits."
  }
}

variable "registry_image" {
  description = "Container image reference for grain-registry in Artifact Registry (for example us-docker.pkg.dev/my-gcp-sandbox/grain/grain-registry:latest)."
  type        = string

  validation {
    condition     = trimspace(var.registry_image) != ""
    error_message = "registry_image must not be empty."
  }
}

variable "admin_email" {
  description = "Primary human email admitted through Cloud IAP and granted registry admin/creator rights (for example alice@example.com)."
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", trimspace(var.admin_email)))
    error_message = "admin_email must be a valid email address."
  }
}

variable "extra_users" {
  description = "Additional human email addresses admitted through Cloud IAP and granted registry creator access."
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for u in var.extra_users : can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", trimspace(u)))
    ])
    error_message = "Every entry in extra_users must be a valid email address."
  }
}

variable "allowed_domains" {
  description = "Explicit auth.allowed_domains list for registry.yaml. When empty, derived from admin_email and extra_users."
  type        = list(string)
  default     = []
}

variable "managed_entry_names" {
  description = "Optional connected entry names (<name>.<instance_domain>) to include on the Google-managed TLS certificate for zero-config trusted HTTPS routing."
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for n in var.managed_entry_names : can(regex("^[a-z][a-z0-9-]{1,30}[a-z0-9]$", n))
    ])
    error_message = "Every entry in managed_entry_names must be a valid grain-registry entry name (^[a-z][a-z0-9-]{1,30}[a-z0-9]$)."
  }
}

variable "tls_mode" {
  description = "TLS certificate strategy on the HTTPS Load Balancer: 'hybrid' (Google-managed cert for registry_host + managed_entry_names, plus self-signed wildcard fallback for *.instance_domain), 'managed' (Google-managed cert only), or 'self-signed' (immediate self-signed cert for registry_host + *.instance_domain)."
  type        = string
  default     = "hybrid"

  validation {
    condition     = contains(["hybrid", "managed", "self-signed"], var.tls_mode)
    error_message = "tls_mode must be one of 'hybrid', 'managed', or 'self-signed'."
  }
}

variable "registry_host" {
  description = "Optional custom DNS hostname for the registry control plane. Leave empty to derive <name_prefix>-<lb-ip>.sslip.io."
  type        = string
  default     = ""

  validation {
    condition     = var.registry_host == "" || can(regex("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$", lower(trimspace(var.registry_host))))
    error_message = "registry_host must be empty or a valid lowercase DNS hostname without scheme or port."
  }
}

variable "instance_domain" {
  description = "Optional custom DNS suffix for routed entries (<entry>.<instance_domain>). Leave empty to derive inst-<name_prefix>-<lb-ip>.sslip.io."
  type        = string
  default     = ""

  validation {
    condition     = var.instance_domain == "" || can(regex("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$", lower(trimspace(var.instance_domain))))
    error_message = "instance_domain must be empty or a valid lowercase DNS suffix without scheme or port."
  }
}

variable "machine_type" {
  description = "GCE machine type for the single test registry + PostgreSQL VM. This VM only runs the control plane and needs no nested virtualization, so a small E2 type is fine here; hosted grain VMs take their machine types from the N2/C4 compute.shapes in files/startup.sh instead."
  type        = string
  default     = "e2-small"
}

variable "boot_disk_gb" {
  description = "Boot persistent disk size in GiB for the test registry VM."
  type        = number
  default     = 20
}

variable "sql_machine_type" {
  description = "GCE machine type for the shared instance_sql PostgreSQL pool VM. It runs only PostgreSQL and needs no nested virtualization, so a small E2 type is fine here."
  type        = string
  default     = "e2-small"
}

variable "sql_boot_disk_gb" {
  description = "Boot persistent disk size in GiB for the shared instance_sql PostgreSQL pool VM."
  type        = number
  default     = 50
}

variable "hosted_daemon_image" {
  description = "Container image reference run by hosted GCE VMs (kind = instance) to serve grain daemon."
  type        = string
  default     = "docker.io/grainai/grain:main"

  validation {
    condition     = trimspace(var.hosted_daemon_image) != ""
    error_message = "hosted_daemon_image must not be empty."
  }
}

variable "create_vm_service_account" {
  description = "Create a dedicated least-privilege VM service account and bind project-level Artifact Registry/Logging roles (requires roles/resourcemanager.projectIamAdmin). Set false to run the VM as the default Compute Engine service account (<number>-compute@developer.gserviceaccount.com)."
  type        = bool
  default     = true
}

variable "create_verifier_sa" {
  description = "Create a dedicated service account granted IAP access and registry service_principals reader access for non-interactive verification (verify.sh)."
  type        = bool
  default     = true
}

variable "verifier_service_account_email" {
  description = "Optional existing service account email (such as the deployer service account) to admit to IAP and registry service_principals instead of creating a separate verifier SA."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.verifier_service_account_email) == "" || can(regex("^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\\.gserviceaccount\\.com$", trimspace(var.verifier_service_account_email)))
    error_message = "verifier_service_account_email must be empty or a valid *.gserviceaccount.com email."
  }
}

variable "existing_kms_key_id" {
  description = "Optional full Cloud KMS CryptoKey resource ID (projects/.../locations/.../keyRings/.../cryptoKeys/...) to reuse instead of creating one."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.existing_kms_key_id) == "" || can(regex("^projects/[a-z][a-z0-9.:-]*[a-z0-9]/locations/[a-z0-9-]+/keyRings/[A-Za-z0-9_-]+/cryptoKeys/[A-Za-z0-9_-]+$", trimspace(var.existing_kms_key_id)))
    error_message = "existing_kms_key_id must be empty or match projects/<id>/locations/<loc>/keyRings/<ring>/cryptoKeys/<key>."
  }
}

variable "hosted_project_id" {
  description = "GCP project where hosted GCE VM instances (kind = instance, grain-vm-*) and their per-instance service accounts are created. Empty (default) means the registry project. A different project gets its own peered VPC (hosted.tf)."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.hosted_project_id) == "" || can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", trimspace(var.hosted_project_id)))
    error_message = "hosted_project_id must be empty or a valid GCP project ID."
  }
}

variable "hosted_credentials_file" {
  description = "Optional service account key file used for resources in hosted_project_id when the default credentials cannot manage that project. Empty uses Application Default Credentials."
  type        = string
  default     = ""
}

variable "hosted_subnet_cidr" {
  description = "Primary IPv4 range of the hosted VM subnet in hosted_project_id (cross-project mode only). Must not overlap the registry subnet (10.42.0.0/24) because the two VPCs are peered."
  type        = string
  default     = "10.43.0.0/24"

  validation {
    condition     = can(cidrhost(var.hosted_subnet_cidr, 0)) && var.hosted_subnet_cidr != "10.42.0.0/24"
    error_message = "hosted_subnet_cidr must be a valid CIDR distinct from the registry subnet 10.42.0.0/24."
  }
}

variable "manage_hosted_project_iam" {
  description = "Bind roles/compute.instanceAdmin.v1 and roles/iam.serviceAccountAdmin in hosted_project_id to the registry VM service account (requires resourcemanager.projects.setIamPolicy there). Set false when an administrator grants them out of band (see README)."
  type        = bool
  default     = false
}
