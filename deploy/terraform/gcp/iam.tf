# Three service accounts, mirroring v1's iam.tf's shape
# but retargeted at pkg/gcpsetup's naming and capability surface:
#
#   host   -- what the VM itself runs as. Holds no project-wide secret
#             grant; secrets.tf grants it read on each of this
#             deployment's own secrets (the GitHub PAT, the model keys,
#             -- if minted -- the minter's key), which push-secrets.sh
#             writes after the apply, never through Terraform.
#   agent  -- the narrow account pkg/capability/gcpkey mints per-task
#             keys for, and the account this module's own
#             agent_can_manage_compute_instances/agent_can_manage_gke
#             grant against. Named agent_account_id, defaulting to
#             pkg/gcpsetup.DefaultAgentAccountID so a deployment running
#             `grain daemon -gcp-agent-service-account
#             <this account's email>` (which deploy.sh always passes
#             explicitly regardless) needs no coincidence to work.
#   minter -- mints and revokes the agent account's keys
#             (pkg/capability/gcpkey.Provider's own MinterCredential),
#             and -- with enable_gemini_key -- administers API keys
#             project-wide for pkg/capability/geminikey. Its own key
#             never touches Terraform state; see variables.tf's
#             deployer_member and push-secrets.sh.
#
# v1's iam.tf additionally has the controller *impersonate* its agent
# account (host_impersonates_agent) rather than hold a standing key for
# it -- v2 has no equivalent yet (pkg/capability/gcpkey authenticates as
# the minter using its own key file, not impersonation), so this module
# does not attempt to build that half; see this module's README, "What
# this does not have yet."

resource "google_service_account" "host" {
  account_id   = "${var.name_prefix}-host"
  display_name = "grain v2 staging host VM (${var.name_prefix})"
  description  = "Attached to the staging host instance. Reads its runtime credentials from this deployment's own Secret Manager secrets (secrets.tf), and nothing else secret."
}

resource "google_project_iam_member" "host" {
  for_each = toset(["roles/logging.logWriter", "roles/monitoring.metricWriter"])
  project  = var.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.host.email}"
}

# setup.sh pulls the sandbox image (the repository's Granulefile, built
# and published by scripts/release-images.sh, which carries the guest as well as the container),
# and when granule_oci_image points at an Artifact Registry in this
# project that needs the host's own service account to read it -- since
# instance.tf's own scopes = ["cloud-platform"] only grants the *scope*,
# not the IAM role itself. Conditioned on the image variable being set,
# not directly on enable_granule_sandboxes, so turning that variable back
# off after it was once configured does not immediately revoke a grant a
# rollback might still want -- matching the "belt, not just suspenders"
# reasoning instance.tf's own precondition otherwise enforces at apply
# time.
#
# The GCS grant that used to sit here is gone with the bucket: there is
# no guest disk to fetch from one any more.
resource "google_project_iam_member" "host_reads_granule_registry" {
  count   = var.granule_oci_image != "" ? 1 : 0
  project = var.project_id
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.host.email}"
}

locals {
  # true whenever anything below needs the agent/minter accounts to
  # exist at all -- mirrors v1's iam.tf's own
  # agent_account_needed, restated for v2's three gates instead of v1's
  # four (v2 has no agent_service_account_roles list of its own; every
  # role the agent account holds here comes from one of the three named
  # capabilities below).
  agent_account_needed = var.enable_gemini_key || var.agent_can_manage_compute_instances || var.agent_can_manage_gke || var.agent_can_manage_cloud_sql

  agent_gke_roles = var.agent_can_manage_gke ? [
    "roles/container.admin",        # create/resize/delete clusters and node pools, plus the Kubernetes API objects on them
    "roles/artifactregistry.admin", # create/delete repositories, push/pull images
  ] : []

  agent_cloud_sql_roles = var.agent_can_manage_cloud_sql ? [
    "roles/cloudsql.admin", # create/configure/delete Cloud SQL instances, databases, and users
  ] : []

  # Only compute.instanceAdmin.v1 and compute.osLogin -- see below for
  # why iap.tunnelResourceAccessor is granted separately, unconditioned.
  agent_conditioned_compute_roles = var.agent_can_manage_compute_instances ? [
    "roles/compute.instanceAdmin.v1", # create, delete, start, stop, list, get, setMetadata (SSH keys)
    "roles/compute.osLogin",          # SSH: OS Login provisions the POSIX account
  ] : []

  # Matches google_compute_instance.host's own name/zone (instance.tf).
  grain_host_resource = "projects/${var.project_id}/zones/${var.zone}/instances/${var.name_prefix}-host"
}

resource "google_service_account" "agent" {
  count        = local.agent_account_needed ? 1 : 0
  account_id   = var.agent_account_id
  display_name = "grain v2 sandboxed agents (${var.name_prefix})"
  description  = "pkg/capability/gcpkey mints a fresh, short-lived key for this account per task; pkg/capability/geminikey mints Gemini API keys against the project this account's grants below allow."
}

resource "google_service_account" "minter" {
  count        = local.agent_account_needed ? 1 : 0
  account_id   = var.minter_account_id
  display_name = "grain v2 GCP key minter (${var.name_prefix})"
  description  = "Mints and revokes the agent account's per-task keys; never the agent account's own credential. See push-secrets.sh for how its own key reaches the host."
}

# pkg/capability/gcpkey.Provider's own requirement: the minter mints and
# revokes keys *for* the agent account, which needs this role scoped to
# that one account -- mirrors v1's iam.tf's host_manages_agent_keys,
# restated for a deployment where the minter (not the host) holds the
# standing credential that makes the call.
resource "google_service_account_iam_member" "minter_manages_agent_keys" {
  count              = local.agent_account_needed ? 1 : 0
  service_account_id = google_service_account.agent[0].name
  role               = "roles/iam.serviceAccountKeyAdmin"
  member             = "serviceAccount:${google_service_account.minter[0].email}"
}

# Lets deployer_member mint (and invalidate) the minter's own key without
# that key ever passing through Terraform -- see variables.tf's
# deployer_member and push-secrets.sh, and v1's iam.tf's
# deployer_manages_agent_keys for the same reasoning applied to v1's
# equivalent account.
resource "google_service_account_iam_member" "deployer_manages_minter_keys" {
  count              = local.agent_account_needed && var.deployer_member != "" ? 1 : 0
  service_account_id = google_service_account.minter[0].name
  role               = "roles/iam.serviceAccountKeyAdmin"
  member             = var.deployer_member
}

# The Generative Language and API Keys APIs -- pkg/capability/geminikey's
# own `apikeys.googleapis.com` calls fail with API-not-enabled until this
# exists. disable_on_destroy is false: a `terraform destroy` (or turning
# enable_gemini_key back off) must not reach into the project and disable
# an API something else in it might also depend on.
resource "google_project_service" "generativelanguage" {
  count              = var.enable_gemini_key ? 1 : 0
  project            = var.project_id
  service            = "generativelanguage.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "apikeys" {
  count              = var.enable_gemini_key ? 1 : 0
  project            = var.project_id
  service            = "apikeys.googleapis.com"
  disable_on_destroy = false
}

# Lets the minter account mint and revoke Gemini API keys -- project-wide,
# since `gcloud services api-keys create/delete` operate at the project
# level, not against a single resource. Granted to the minter, not the
# agent, the same "the account holding a per-task credential must not be
# able to mint more of itself" reasoning v1's iam.tf's own
# host_gemini_keys comment gives for v1.
resource "google_project_iam_member" "minter_gemini_keys" {
  count   = var.enable_gemini_key ? 1 : 0
  project = var.project_id
  role    = "roles/serviceusage.apiKeysAdmin"
  member  = "serviceAccount:${google_service_account.minter[0].email}"

  depends_on = [google_project_service.generativelanguage, google_project_service.apikeys]
}

# Compute Engine API -- needed before agent_conditioned_compute_roles
# below means anything.
resource "google_project_service" "compute" {
  count              = var.agent_can_manage_compute_instances ? 1 : 0
  project            = var.project_id
  service            = "compute.googleapis.com"
  disable_on_destroy = false
}

# Compute instance lifecycle and SSH, everywhere in this project except
# this deployment's own host VM -- mirrors v1's iam.tf's
# agent_compute exactly, including why iap.tunnelResourceAccessor
# (below, separately) is not conditioned the same way: GCP does not
# reliably support excluding one instance from a project-level grant of
# that specific role (confirmed live, against v1's own deployment --
# doing so denied *all* tunnel access rather than excluding just the
# one instance).
resource "google_project_iam_member" "agent_compute" {
  for_each = toset(local.agent_conditioned_compute_roles)
  project  = var.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.agent[0].email}"

  condition {
    title       = "exclude-grain-host"
    description = "Instance management and SSH everywhere in this project except this deployment's own host VM."
    expression  = "resource.type != \"compute.googleapis.com/Instance\" || resource.name != \"${local.grain_host_resource}\""
  }

  depends_on = [google_project_service.compute]
}

resource "google_project_iam_member" "agent_iap_tunnel" {
  count   = var.agent_can_manage_compute_instances ? 1 : 0
  project = var.project_id
  role    = "roles/iap.tunnelResourceAccessor"
  member  = "serviceAccount:${google_service_account.agent[0].email}"
}

# Allow members in iap_members to access the instance UI via IAP TCP port tunneling
# (roles/iap.tunnelResourceAccessor and roles/compute.viewer for instance lookup).
resource "google_project_iam_member" "iap_members_tunnel" {
  for_each = toset(var.iap_members)
  project  = var.project_id
  role     = "roles/iap.tunnelResourceAccessor"
  member   = each.value
}

resource "google_project_iam_member" "iap_members_compute_viewer" {
  for_each = toset(var.iap_members)
  project  = var.project_id
  role     = "roles/compute.viewer"
  member   = each.value
}

# GKE and Artifact Registry APIs.
resource "google_project_service" "container" {
  count              = var.agent_can_manage_gke ? 1 : 0
  project            = var.project_id
  service            = "container.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "artifactregistry" {
  count              = var.agent_can_manage_gke ? 1 : 0
  project            = var.project_id
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
}

# GKE cluster and Artifact Registry repository lifecycle, project-wide --
# see variables.tf's agent_can_manage_gke for why this is unconditioned,
# unlike agent_compute above: a cluster is a project resource like any
# other, with no equivalent "this deployment's own resource" to exclude
# the way the host VM itself can be.
resource "google_project_iam_member" "agent_gke" {
  for_each = toset(local.agent_gke_roles)
  project  = var.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.agent[0].email}"

  depends_on = [google_project_service.container, google_project_service.artifactregistry]
}

# Cloud SQL Admin API.
resource "google_project_service" "sqladmin" {
  count              = var.agent_can_manage_cloud_sql ? 1 : 0
  project            = var.project_id
  service            = "sqladmin.googleapis.com"
  disable_on_destroy = false
}

# Cloud SQL instance lifecycle, project-wide.
resource "google_project_iam_member" "agent_cloud_sql" {
  for_each = toset(local.agent_cloud_sql_roles)
  project  = var.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.agent[0].email}"

  depends_on = [google_project_service.sqladmin]
}

# Neither container.admin nor compute.instanceAdmin.v1 can create the
# thing it names, on its own: a GKE node pool and a GCE instance both run
# as some service account, and GCP refuses to attach one unless the
# caller separately holds iam.serviceAccountUser on *that* account
# (confirmed live against v1's own deployment, its iam.tf's
# agent_acts_as_self_for_gke_nodes, bwsalmon/agents#146). Granting it
# here, on the agent account acting as itself, is what makes
# `--service-account=<agent email>` work -- and is the identity worth
# using in both cases: the project's default Compute Engine service
# account often carries broader legacy roles than this one, so pointing a
# node pool or an instance at it would be a privilege escalation for
# anything that ends up running there.
#
# This used to be conditioned on agent_can_manage_gke alone, which left
# agent_can_manage_compute_instances granting a set of roles that could
# not actually create an instance: a bare `gcloud compute instances
# create` defaults to attaching the project's default Compute Engine
# service account and fails with "The user does not have access to
# service account '<N>-compute@developer.gserviceaccount.com'", naming a
# permission nothing in this file grants. Widening the count fixes the
# `--service-account=<agent email>` form; the *default* form still fails,
# deliberately -- see this module's README, "Creating a VM as the agent",
# for the flags a task should use instead, including
# `--no-service-account --no-scopes` for an instance that needs no GCP
# identity at all.
resource "google_service_account_iam_member" "agent_acts_as_self" {
  count              = var.agent_can_manage_gke || var.agent_can_manage_compute_instances ? 1 : 0
  service_account_id = google_service_account.agent[0].name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.agent[0].email}"
}

# Renamed when the count above stopped being GKE-specific. Without this,
# the next apply against an existing deployment destroys the binding and
# recreates it under the new address -- a window in which a running task
# holding a key for this account cannot create anything.
moved {
  from = google_service_account_iam_member.agent_acts_as_self_for_gke_nodes
  to   = google_service_account_iam_member.agent_acts_as_self
}

# -----------------------------------------------------------------------------
# Organization Policies (Restricting Public Infrastructure)
# -----------------------------------------------------------------------------

# Restrict public endpoints on GKE clusters to ensure clusters are private
resource "google_project_organization_policy" "restrict_public_endpoints" {
  count      = var.enforce_private_infrastructure_policies ? 1 : 0
  project    = var.project_id
  constraint = "constraints/gke.restrictPublicEndpoints"

  boolean_policy {
    enforced = true
  }
}

# Deny external IP allocation on VM instances and GKE node pools
resource "google_project_organization_policy" "no_external_ip" {
  count      = var.enforce_private_infrastructure_policies ? 1 : 0
  project    = var.project_id
  constraint = "constraints/compute.vmExternalIpAccess"

  list_policy {
    deny {
      all = true
    }
  }
}

# Restrict public IP on Cloud SQL instances to ensure databases are private
resource "google_project_organization_policy" "restrict_public_ip" {
  count      = var.enforce_private_infrastructure_policies ? 1 : 0
  project    = var.project_id
  constraint = "constraints/sql.restrictPublicIp"

  boolean_policy {
    enforced = true
  }
}
