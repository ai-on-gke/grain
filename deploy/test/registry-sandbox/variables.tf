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
}

variable "instance_domain" {
  description = "Optional custom DNS suffix for routed entries (<entry>.<instance_domain>). Leave empty to derive inst-<name_prefix>-<lb-ip>.sslip.io."
  type        = string
  default     = ""
}

variable "machine_type" {
  description = "GCE machine type for the single test registry + PostgreSQL VM."
  type        = string
  default     = "e2-small"
}

variable "boot_disk_gb" {
  description = "Boot persistent disk size in GiB for the test registry VM."
  type        = number
  default     = 20
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
}

variable "existing_kms_key_id" {
  description = "Optional full Cloud KMS CryptoKey resource ID (projects/.../locations/.../keyRings/.../cryptoKeys/...) to reuse instead of creating one."
  type        = string
  default     = ""
}
