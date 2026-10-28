# Where this deployment's credentials live: one Secret Manager secret per
# value deploy/push-secrets.sh writes, readable by the host's own service
# account and nobody else this module grants anything to (#2376).
#
# They used to be custom instance metadata (grain-github-token,
# grain-gcp-minter-key, ...), which has no access control of its own:
# anything that could reach the metadata server could read every one of
# them unauthenticated, anyone holding compute.instances.get on the host
# could read them through the Compute API, and Terraform's refresh copied
# them all into the state file. A secret here is reached only with an
# access token for an identity this file grants
# roles/secretmanager.secretAccessor on *that secret* -- per secret, never
# project-wide -- every read is a Data Access audit log entry, and nothing
# about the instance carries a value any more.
#
# Terraform creates the secrets and their IAM, never a version: the
# values are still pushed by push-secrets.sh after the apply, so none of
# them is in a .tf file, a tfvars file or the state. deploy/migrate-secrets.sh
# moves a deployment that predates this file off instance metadata;
# deploy/terraform-apply.sh runs it on its own before the apply that
# would otherwise drop the old attributes. See this module's README,
# "Secrets never touch Terraform".
#
# The keys below are the whole contract between three files: secret
# "<name_prefix>-<key>" is what push-secrets.sh writes, and
# grain_config.secret_ids (instance.tf) is how files/deploy.sh finds it.
locals {
  host_secret_keys = toset([
    "github-token",
    "github-app-id",
    "github-app-installation-id",
    "github-app-private-key",
    "gemini-api-key",
    "claude-oauth-token",
    "openai-api-key",
    "secrets-key",
    "image-pull-token",
    "gcp-minter-key",
  ])
}

# disable_on_destroy is false for the same reason as every other API this
# module enables: a destroy must not reach into the project and switch off
# something another deployment there also uses.
resource "google_project_service" "secretmanager" {
  project            = var.project_id
  service            = "secretmanager.googleapis.com"
  disable_on_destroy = false
}

# Prefixed with name_prefix, like every other project-level name here, so
# a main and a staging deployment sharing one project each get their own.
resource "google_secret_manager_secret" "host" {
  for_each  = local.host_secret_keys
  project   = var.project_id
  secret_id = "${var.name_prefix}-${each.key}"
  labels    = var.labels

  replication {
    auto {}
  }

  depends_on = [google_project_service.secretmanager]
}

# The only read grant: the host's own account, secret by secret.
# files/deploy.sh reads with the token the metadata server hands that
# account, which containers and sandbox guests cannot reach
# (grain-block-metadata.service, deploy.sh's ensure_metadata_block).
resource "google_secret_manager_secret_iam_member" "host_reads" {
  for_each  = google_secret_manager_secret.host
  project   = var.project_id
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.host.email}"
}

# Lets deployer_member add a version and destroy the ones it replaces --
# what push-secrets.sh does -- without being able to read any of them
# back: secretVersionManager carries no secretmanager.versions.access.
# Whoever applies this module already has more (roles/secretmanager.admin,
# bootstrap-gcp.sh), so this is for a deployer_member that pushes secrets
# without applying Terraform, and to keep who writes here legible.
resource "google_secret_manager_secret_iam_member" "deployer_writes" {
  for_each  = var.deployer_member != "" ? google_secret_manager_secret.host : {}
  project   = var.project_id
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretVersionManager"
  member    = var.deployer_member
}
