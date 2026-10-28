#!/usr/bin/env bash
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

# Initialise, validate and apply the terraform/gcp module against a
# config repo's own tfvars and backend.
#
# One script for every deployment of that module: bwsalmon/agents runs
# both a main and a staging one, and they differ only in which
# CONFIG_DIR and TFVARS_FILE they point at. Nothing here is per-
# deployment, which is why TFVARS_FILE has no default -- a wrong one
# would apply another deployment's configuration to this one's state.
#
# Creates the Terraform state bucket if it is missing, so a deployment's
# first apply does not fail on a bucket nobody has made yet -- see "the
# state bucket" below for what it will and will not create.
#
# Called from a config repo's deploy workflow, out of the grain checkout
# it makes -- so a config repo never encodes grain's layout and cannot
# drift from it. See terraform/gcp/README.md.
#
# Required env:
#   CONFIG_DIR         absolute path to the directory holding backend.hcl
#                      and the tfvars file
#   TFVARS_FILE        tfvars basename within CONFIG_DIR
#   DEPLOY_GENERATION  the token the host watches for
# Optional env:
#   TF_APPLY_MAX_ATTEMPTS  stock-out retries (default 5)
#   TF_APPLY_RETRY_DELAY   first backoff, seconds, doubling (default 60)
set -euo pipefail

config_dir="${CONFIG_DIR:?CONFIG_DIR is not set: the directory holding backend.hcl and the tfvars}"
deploy_generation="${DEPLOY_GENERATION:?DEPLOY_GENERATION is not set}"
tfvars_file="${TFVARS_FILE:?TFVARS_FILE is not set: the tfvars basename within CONFIG_DIR}"
max_attempts="${TF_APPLY_MAX_ATTEMPTS:-5}"
delay="${TF_APPLY_RETRY_DELAY:-60}"

for f in backend.hcl "$tfvars_file"; do
  [ -f "$config_dir/$f" ] || {
    echo "::error::$config_dir/$f does not exist" >&2
    exit 1
  }
done

# The module this deploys is the parent of this directory -- so the
# caller never names grain's internal layout and a move here breaks
# nothing downstream.
module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$module_dir"

# --- the state bucket --------------------------------------------------
#
# `terraform init` against a bucket that does not exist fails with
# "Failed to get existing workspaces: querying Cloud Storage failed:
# storage: bucket doesn't exist", which says nothing about what to do.
# A first deploy of a new deployment hits it every time: bootstrap-gcp.sh
# creates the bucket, and until someone has run it there is nothing to
# init against.
#
# So create it here when it is missing, with the same three protections
# bootstrap-gcp.sh applies -- uniform bucket-level access, versioning,
# public access prevention. Versioning is the one that matters most: it
# is what makes a corrupted or truncated state file recoverable, and a
# bucket created without it cannot be fixed retroactively for the state
# it has already lost.
#
# This needs storage.buckets.create on the project, which the deployer
# holds via roles/storage.admin (bootstrap-gcp.sh grants it). Its other
# storage grants are scoped to the bucket itself and so cannot help
# create one.
#
# Only ever creates the name this deployment's own config already
# derives -- see the guard below on why it will not create an arbitrary
# one.
bucket="$(sed -n 's/^[[:space:]]*bucket[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' \
  "$config_dir/backend.hcl" | head -1)"
[ -n "$bucket" ] || {
  echo "::error::$config_dir/backend.hcl names no bucket" >&2
  exit 1
}

if ! gcloud storage buckets describe "gs://$bucket" >/dev/null 2>&1; then
  # A typo in backend.hcl would otherwise be answered by creating a
  # *fresh, empty* bucket: init would succeed against no state at all and
  # the apply would set about building a second copy of the deployment
  # alongside the real one. Before this step existed, the same typo
  # simply failed init and someone noticed.
  #
  # So only the name bootstrap-gcp.sh itself would have chosen is created
  # here -- "<project_id>-<name_prefix>-tfstate", both read from this
  # deployment's own tfvars. Anything else is a name a human chose
  # deliberately (bootstrap's own --bucket), and creating it is theirs to
  # do rather than a typo this script should act on.
  tfvar() {
    sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" \
      "$config_dir/$tfvars_file" | head -1
  }
  project="$(tfvar project_id)"
  region="$(tfvar region)"
  prefix="$(tfvar name_prefix)"
  : "${region:=us-central1}"
  : "${prefix:=grain}"

  expected="${project}-${prefix}-tfstate"
  if [ -z "$project" ] || [ "$bucket" != "$expected" ]; then
    echo "::error::state bucket gs://$bucket does not exist, and is not the name this config implies (gs://$expected)." >&2
    echo "Refusing to create it: a mistyped bucket name would otherwise start a deployment over against empty state." >&2
    echo "Create it deliberately, from a grain checkout:" >&2
    echo "  ./deploy/terraform/gcp/bootstrap-gcp.sh --project ${project:-PROJECT} --prefix ${prefix} --bucket $bucket" >&2
    exit 1
  fi

  echo "--- creating the state bucket gs://$bucket (it does not exist yet)"
  gcloud storage buckets create "gs://$bucket" \
    --project="$project" --location="$region" --uniform-bucket-level-access
fi

# Outside the `if`, deliberately: a bucket created by an older run of
# this script, or by hand without them, converges on the protections
# rather than keeping whatever it was made with. Idempotent.
gcloud storage buckets update "gs://$bucket" \
  --versioning --public-access-prevention >/dev/null

echo "--- terraform init"
terraform init -input=false -backend-config="$config_dir/backend.hcl"

echo "--- terraform validate"
terraform validate -no-color

# GCP stock-outs (ZONE_RESOURCE_POOL_EXHAUSTED and its siblings) are a
# common, transient failure creating a VM or disk in a given zone -- a
# plain retry a few minutes later routinely succeeds once the zone frees
# up capacity, so back off and retry instead of failing the whole
# rollout. Any other failure still fails immediately: retrying a bad
# config or a real quota limit only burns the backoff budget.
#
# google_compute_instance.host's own metadata update (grain-deploy-generation,
# every apply that changes anything in grain_config -- see instance.tf) is a
# second source of a transient, retry-worthy failure, and not a GCP stock-out
# at all: the google provider retries that update itself, to absorb a 412
# where another update raced it for the instance's metadata fingerprint, but
# hardcodes the *whole* retry -- GET the instance, POST the new metadata, wait
# for the resulting operation to finish -- to a one-minute budget it never
# derives from this resource's own `timeouts` (confirmed in the pinned
# hashicorp/google ~> 6.8 provider's resourceComputeInstanceUpdate, which
# calls transport_tpg.Retry with no Timeout set, so transport_tpg.Retry's own
# default of 1*time.Minute applies regardless of how long a real update is
# allowed to take). Ordinary GCP latency can eat that minute with no fault of
# ours, and the provider then reports exactly "Error: timeout while waiting
# for state to become 'success' (timeout: 1m0s)" and abandons an update that
# was very likely about to succeed. A plain retry of the whole `terraform
# apply` is safe here the same way it is for a stock-out: this resource's
# state was never advanced, so the next attempt just retries the same
# SetMetadata call. See bwsalmon/agents#636.

# Terraform renders a resource's prior state in full wherever it diffs
# it. Refresh reads the instance's real metadata back on every run, and
# push-secrets.sh used to write every secret into that metadata -- so an
# apply that replaced the host (a bigger boot_disk_gb, a new boot_image
# or machine_type) printed every one of them, the minter account's
# private key included, into a log everyone who can read the workflow run
# can read (bwsalmon/agents#653, where exactly that happened). GitHub
# Actions masks the values a workflow handed it as secrets; it cannot
# mask the minter key, which push-secrets.sh mints during the run.
#
# The secrets live in Secret Manager now (secrets.tf), and Terraform
# never holds a version of one. But a deployment that predates that
# still has them in metadata and in state until its first apply after
# it, and that apply *removes* the attributes -- an in-place diff that
# prints each old value -- whenever migrate-secrets.sh below has not
# already taken them off the instance. So the filter stays, keyed on the
# old attribute names.
#
# The apply is filtered before it reaches the log or "$out": PEM
# private-key bodies go, and so do the values of the metadata keys
# push-secrets.sh used to write. Redacting rather than silencing the apply
# altogether, because this log is the first real plan a deployment ever
# gets -- a config repo's plan.yml runs unauthenticated on purpose -- so
# which resource changed, which attribute, and that it was replaced all
# have to survive. Ordinary applies print none of this anyway
# (ignore_changes covers them); this is the replacement case.
#
# Keyed on the metadata names rather than on the values, because the
# values are exactly what this script must never be trusted to have seen.
# The identifiers around them stay: private_key_id in particular names
# which key to revoke if one did leak before this existed.
redact() {
  awk '
    inkey {
      if ($0 ~ /END[A-Z ]*PRIVATE KEY-----/) inkey = 0
      next
    }
    /"(grain-github-token|grain-github-app-id|grain-github-app-installation-id|grain-github-app-private-key|grain-gemini-api-key|grain-claude-oauth-token|grain-openai-api-key|grain-secrets-key|grain-image-pull-token|grain-gcp-minter-key)"/ && / = "/ {
      sub(/ = ".*/, " = (redacted by terraform-apply.sh)")
    }
    /BEGIN[A-Z ]*PRIVATE KEY-----/ {
      print "                        # (private key redacted by terraform-apply.sh)"
      fflush()
      if ($0 !~ /END[A-Z ]*PRIVATE KEY-----/) inkey = 1
      next
    }
    # fflush on every line: awk block-buffers a pipe, and without this a
    # 45-minute apply would print nothing until it had finished.
    { print; fflush() }
  '
}

apply() {
  terraform apply -input=false -auto-approve -no-color \
    -var-file="$config_dir/$tfvars_file" \
    -var="deploy_generation=$deploy_generation" 2>&1 | redact | tee "$out"
}

# --- secrets still in instance metadata ------------------------------
#
# A deployment that predates secrets.tf carries its secrets as instance
# metadata, and instance.tf no longer ignores those attributes: the full
# apply below would delete them. migrate-secrets.sh copies them into
# Secret Manager first, and needs the secrets to exist to do it -- so
# when the instance this state already knows about still carries any,
# apply just the secrets and their read grants (nothing on the instance)
# and migrate before going on. Skipped on a first deploy, which has no
# instance yet, and on every deploy after the one that migrated.
#
# Not retried: a stock-out cannot happen here, and a failure leaves the
# attributes where they were, for the next run to try again.
migrate_metadata_secrets() {
  local outputs
  outputs="$(terraform output -json 2>/dev/null)" || return 0
  export PROJECT INSTANCE ZONE
  PROJECT="$(jq -r '.project_id.value // empty' <<<"$outputs" 2>/dev/null)" || return 0
  INSTANCE="$(jq -r '.instance_name.value // empty' <<<"$outputs")"
  ZONE="$(jq -r '.zone.value // empty' <<<"$outputs")"
  [ -n "$PROJECT" ] && [ -n "$INSTANCE" ] && [ -n "$ZONE" ] || return 0
  "$module_dir/deploy/migrate-secrets.sh" --pending || return 0

  echo "--- moving $INSTANCE's secrets from instance metadata into Secret Manager"
  terraform apply -input=false -auto-approve -no-color \
    -var-file="$config_dir/$tfvars_file" \
    -var="deploy_generation=$deploy_generation" \
    -target=google_secret_manager_secret_iam_member.host_reads \
    -target=google_secret_manager_secret_iam_member.deployer_writes 2>&1 | redact
  "$module_dir/deploy/migrate-secrets.sh"
}
migrate_metadata_secrets

echo "--- terraform apply"
out="$(mktemp)"
attempt=1
while true; do
  if apply; then
    exit 0
  fi
  if [ "$attempt" -ge "$max_attempts" ] \
     || ! grep -qiE "RESOURCE_POOL_EXHAUSTED|does not have enough resources available|timeout while waiting for state to become 'success' \\(timeout: 1m0s\\)" "$out"; then
    # Name only what a reader could not diagnose from the Terraform error
    # itself, and match on something that cannot appear in a *successful*
    # run's noise. An earlier version of this grepped for "iap_brand",
    # which the provider emits as a deprecation warning on every single
    # run whether or not anything failed -- so the hint fired on every
    # failure and talked over the real error underneath it.
    #
    # A 409 alreadyExists is the one worth a hint: in a project shared
    # with another grain deployment it means a resource name is claimed,
    # and the Terraform error names the resource but not the fix.
    if grep -q 'Error 409' "$out" && grep -qi 'already exists' "$out"; then
      echo "::error::A resource this deployment wants already exists in the project," \
           "which usually means another grain deployment there already owns that name." \
           "The Terraform error above names it. Set a distinct agent_account_id or" \
           "minter_account_id in $tfvars_file (or a distinct name_prefix) -- never" \
           "reuse the other deployment's account, which carries its grants, not this" \
           "one's. See terraform/gcp/README.md, \"Sharing a project\"."
    fi
    exit 1
  fi
  echo "::warning::terraform apply hit a transient failure (a stock-out, or the provider's own one-minute metadata-update timeout; attempt $attempt/$max_attempts); retrying in ${delay}s"
  sleep "$delay"
  attempt=$((attempt + 1))
  delay=$((delay * 2))
done
