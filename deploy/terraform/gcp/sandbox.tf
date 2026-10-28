provider "google" {
  alias   = "sandbox"
  project = var.sandbox_project_id
  region  = var.region
}

locals {
  create_sandbox = var.sandbox_project_id != "" ? 1 : 0
}

# -----------------------------------------------------------------------------
# 1. Organization Policies (Restricting External Endpoints)
# -----------------------------------------------------------------------------

# Prevent external IPs on VMs (ensures no VM can have a public IP)
resource "google_project_organization_policy" "sandbox_no_external_ip" {
  count      = local.create_sandbox
  provider   = google.sandbox
  project    = var.sandbox_project_id
  constraint = "compute.vmExternalIpAccess"

  list_policy {
    deny {
      all = true
    }
  }
}

# Enforce Public Access Prevention for all buckets in the sandbox project
resource "google_project_organization_policy" "sandbox_public_access_prevention" {
  count      = local.create_sandbox
  provider   = google.sandbox
  project    = var.sandbox_project_id
  constraint = "storage.publicAccessPrevention"

  boolean_policy {
    enforced = true
  }
}

# Stop the sandbox project's default service accounts (created when the Compute
# or App Engine API is first enabled) from being granted roles/editor
# automatically. Defense in depth behind the per-account serviceAccountUser
# grants below: an account an agent should never act as should not be an
# editor either. Only affects default accounts created after it is applied.
resource "google_project_organization_policy" "sandbox_no_default_sa_grants" {
  count      = local.create_sandbox
  provider   = google.sandbox
  project    = var.sandbox_project_id
  constraint = "iam.automaticIamGrantsForDefaultServiceAccounts"

  boolean_policy {
    enforced = true
  }
}

# Prevent GKE/Compute resources from being externally accessible where applicable
# (Blocking public IPs inherently forces components to be private, but we can also
# enforce requiring OS Login which boosts security over external endpoints)
resource "google_project_organization_policy" "sandbox_require_oslogin" {
  count      = local.create_sandbox
  provider   = google.sandbox
  project    = var.sandbox_project_id
  constraint = "compute.requireOsLogin"

  boolean_policy {
    enforced = true
  }
}

# -----------------------------------------------------------------------------
# 2. VPC Setup (No Public Ingress, Allowed Egress)
# -----------------------------------------------------------------------------
resource "google_compute_network" "sandbox_net" {
  count                   = local.create_sandbox
  provider                = google.sandbox
  name                    = "${var.name_prefix}-sandbox-net"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "sandbox_subnet" {
  count                    = local.create_sandbox
  provider                 = google.sandbox
  name                     = "${var.name_prefix}-sandbox-subnet"
  region                   = var.region
  network                  = google_compute_network.sandbox_net[0].id
  ip_cidr_range            = "10.10.0.0/16"
  private_ip_google_access = true
}

# Cloud Router and NAT for isolated egress internet access
resource "google_compute_router" "sandbox_nat_router" {
  count    = local.create_sandbox
  provider = google.sandbox
  name     = "${var.name_prefix}-sandbox-router"
  region   = var.region
  network  = google_compute_network.sandbox_net[0].id
}


resource "google_compute_router_nat" "sandbox_nat" {
  count                              = local.create_sandbox
  provider                           = google.sandbox
  name                               = "${var.name_prefix}-sandbox-nat"
  region                             = var.region
  router                             = google_compute_router.sandbox_nat_router[0].name
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

# Firewall rule to allow Identity-Aware Proxy (IAP) TCP forwarding to VMs
resource "google_compute_firewall" "sandbox_iap_ssh" {
  count         = local.create_sandbox
  provider      = google.sandbox
  name          = "${var.name_prefix}-sandbox-allow-iap-ssh"
  network       = google_compute_network.sandbox_net[0].name
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# -----------------------------------------------------------------------------
# 3. Minting Service Group / Account
# -----------------------------------------------------------------------------
# "create gemini keys and add keys to existing service accounts"

resource "google_service_account" "sandbox_minter" {
  count        = local.create_sandbox
  provider     = google.sandbox
  project      = var.sandbox_project_id
  account_id   = "${var.name_prefix}-sb-minter"
  display_name = "Sandbox Minter Service Account"
}

# To add keys to the sandbox agent accounts -- and only those. The minter
# (pkg/capability/gcpkey) mints per-task keys for sandbox_agents[*]; a
# project-wide grant would let it mint a key for any account in the project,
# the default compute account (often roles/editor) included. Mirrors iam.tf's
# minter_manages_agent_keys.
resource "google_service_account_iam_member" "sandbox_minter_key_admin" {
  count              = local.create_sandbox == 1 ? length(google_service_account.sandbox_agents) : 0
  provider           = google.sandbox
  service_account_id = google_service_account.sandbox_agents[count.index].name
  role               = "roles/iam.serviceAccountKeyAdmin"
  member             = "serviceAccount:${google_service_account.sandbox_minter[0].email}"
}

# To create/manage API Keys (e.g. Gemini API keys via GCP API Keys service)
resource "google_project_iam_member" "sandbox_minter_api_keys" {
  count    = local.create_sandbox
  provider = google.sandbox
  project  = var.sandbox_project_id
  role     = "roles/serviceusage.apiKeysAdmin"
  member   = "serviceAccount:${google_service_account.sandbox_minter[0].email}"
}

# To access Vertex AI / Gemini APIs directly if needed instead of raw keys
resource "google_project_iam_member" "sandbox_minter_gemini" {
  count    = local.create_sandbox
  provider = google.sandbox
  project  = var.sandbox_project_id
  role     = "roles/aiplatform.user"
  member   = "serviceAccount:${google_service_account.sandbox_minter[0].email}"
}


# -----------------------------------------------------------------------------
# 4. Agent Service Accounts (10x)
# -----------------------------------------------------------------------------

resource "google_service_account" "sandbox_agents" {
  count        = var.sandbox_project_id != "" ? 10 : 0
  provider     = google.sandbox
  project      = var.sandbox_project_id
  account_id   = "${var.name_prefix}-sb-agent-${count.index}"
  display_name = "Sandbox Agent Service Account ${count.index}"
}

# Allow agents to create and manage VMs
resource "google_project_iam_member" "sandbox_agents_compute_admin" {
  # count loop over agents
  count    = var.sandbox_project_id != "" ? 10 : 0
  provider = google.sandbox
  project  = var.sandbox_project_id
  role     = "roles/compute.admin"
  member   = "serviceAccount:${google_service_account.sandbox_agents[count.index].email}"
}

# Allow agents to create and manage GKE clusters
resource "google_project_iam_member" "sandbox_agents_gke_admin" {
  count    = var.sandbox_project_id != "" ? 10 : 0
  provider = google.sandbox
  project  = var.sandbox_project_id
  role     = "roles/container.admin"
  member   = "serviceAccount:${google_service_account.sandbox_agents[count.index].email}"
}

# Allow each agent to attach itself -- and no other account -- to the VMs and
# clusters it creates. Granted per account rather than project-wide: with
# roles/compute.admin above, a project-wide serviceAccountUser would let an
# agent start a VM as any account in the project (the default compute account,
# often roles/editor) and take its token. Mirrors iam.tf's agent_acts_as_self,
# and has the same consequence: create instances and node pools with
# `--service-account=<the agent's own email>` (or `--no-service-account
# --no-scopes`), since the default compute account is no longer usable.
resource "google_service_account_iam_member" "sandbox_agents_sa_user" {
  count              = var.sandbox_project_id != "" ? 10 : 0
  provider           = google.sandbox
  service_account_id = google_service_account.sandbox_agents[count.index].name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.sandbox_agents[count.index].email}"
}

# Allow agents to log into VMs via Identity-Aware Proxy (IAP)
resource "google_project_iam_member" "sandbox_agents_iap_tunnel" {
  count    = var.sandbox_project_id != "" ? 10 : 0
  provider = google.sandbox
  project  = var.sandbox_project_id
  role     = "roles/iap.tunnelResourceAccessor"
  member   = "serviceAccount:${google_service_account.sandbox_agents[count.index].email}"
}
