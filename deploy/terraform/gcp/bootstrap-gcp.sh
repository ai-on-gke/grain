#!/usr/bin/env bash
# One-time setup, run once by a human with project-owner rights.
#
# Mirrors v1's own bootstrap script's mechanism: it creates the
# Terraform state bucket and the deployer service account CI (or a human
# applying by hand) runs as, and -- given --repo -- workload identity
# federation so CI authenticates to GCP with no long-lived key at all.
# Idempotent: safe to re-run after changing anything.
#
#   ./deploy/terraform/gcp/bootstrap-gcp.sh --project my-staging-project
#   ./deploy/terraform/gcp/bootstrap-gcp.sh --project my-staging-project --repo my-org/my-config-repo
#
set -euo pipefail

PROJECT_ID=""
GITHUB_REPO=""
REGION="us-central1"
NAME_PREFIX="grain"
BUCKET=""
# Name-prefixed, unlike v1's bootstrap, which hardcodes "github" for
# both. That difference is deliberate and load-bearing: a staging
# deployment is expected to share a project with a v1 deployment (and
# with another v2 one), and a workload identity pool is a project-level
# resource. Defaulting these to "github" would make this script *update*
# whatever provider v1's own bootstrap created there -- rewriting its
# attribute condition to name whatever --repo was passed here. Same repo,
# and that is a no-op nobody notices; a different one, and v1's deploy
# workflow silently loses its ability to authenticate at all. Prefixing
# means the two never touch. Override only to share a pool deliberately.
# Empty here, derived from NAME_PREFIX after argument parsing (--prefix
# may not have been seen yet) unless --pool/--provider say otherwise.
POOL_ID=""
PROVIDER_ID=""

usage() {
  sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Options:
  --project    PROJECT_ID     GCP project (required)
  --repo       OWNER/NAME     if set, wires workload identity federation for this
                              GitHub repo's Actions workflows; omit for a purely
                              by-hand deployment
  --region     REGION         default: us-central1
  --prefix     PREFIX         resource-name prefix, must match name_prefix in
                              your tfvars. default: grain
  --bucket     NAME           Terraform state bucket. default: PROJECT-PREFIX-tfstate
  --pool       ID             workload identity pool id. default: PREFIX. Prefixed
                              rather than "github" so bootstrapping this in a
                              project that already runs a grain deployment never
                              rewrites that one's provider -- see the comment on
                              POOL_ID
  --provider   ID             workload identity provider id. default: PREFIX
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --project) PROJECT_ID="$2"; shift 2 ;;
    --repo)    GITHUB_REPO="$2"; shift 2 ;;
    --region)  REGION="$2"; shift 2 ;;
    --prefix)  NAME_PREFIX="$2"; shift 2 ;;
    --bucket)  BUCKET="$2"; shift 2 ;;
    --pool)    POOL_ID="$2"; shift 2 ;;
    --provider) PROVIDER_ID="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$PROJECT_ID" ] || { echo "--project is required" >&2; exit 2; }
if [ -n "$GITHUB_REPO" ]; then
  case "$GITHUB_REPO" in
    */*) : ;;
    *) echo "--repo must be OWNER/NAME" >&2; exit 2 ;;
  esac
fi
BUCKET="${BUCKET:-${PROJECT_ID}-${NAME_PREFIX}-tfstate}"
POOL_ID="${POOL_ID:-$NAME_PREFIX}"
PROVIDER_ID="${PROVIDER_ID:-$NAME_PREFIX}"

# No adoption of an unprefixed "github" pool here, deliberately, unlike
# v1's bootstrap script: in a shared project that pool was v1's,
# and adopting it is precisely the collision these names prevent.

DEPLOYER="${NAME_PREFIX}-deployer"
DEPLOYER_EMAIL="${DEPLOYER}@${PROJECT_ID}.iam.gserviceaccount.com"

say() { printf '\n== %s\n' "$*"; }

say "Enabling APIs"
gcloud services enable --project="$PROJECT_ID" \
  cloudresourcemanager.googleapis.com \
  compute.googleapis.com \
  dns.googleapis.com \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  iap.googleapis.com \
  secretmanager.googleapis.com \
  serviceusage.googleapis.com \
  storage.googleapis.com \
  sts.googleapis.com

say "Terraform state bucket: gs://$BUCKET"
if ! gcloud storage buckets describe "gs://$BUCKET" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://$BUCKET" \
    --project="$PROJECT_ID" --location="$REGION" --uniform-bucket-level-access
fi
gcloud storage buckets update "gs://$BUCKET" --project="$PROJECT_ID" \
  --versioning --public-access-prevention

say "Deployer service account: $DEPLOYER_EMAIL"
if ! gcloud iam service-accounts describe "$DEPLOYER_EMAIL" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud iam service-accounts create "$DEPLOYER" --project="$PROJECT_ID" \
    --display-name="grain v2 staging deployer${GITHUB_REPO:+ ($GITHUB_REPO)}"
fi

say "Granting the deployer what Terraform needs"
# compute.admin covers every google_compute_* resource this module
# creates -- the instance, the disk, the network/firewalls, and the
# whole load balancer chain (health check, backend service, URL map,
# managed cert, target proxy, forwarding rule, global address).
# dns.admin only matters if dns_managed_zone is set; harmless otherwise.
# iap.admin is what lets Terraform's google_iap_brand/google_iap_client
# (iap.tf) and the roles/iap.httpsResourceAccessor bindings apply at
# all. resourcemanager.projectIamAdmin is what lets iam.tf's own
# google_project_iam_member resources grant roles in the first place.
# secretmanager.admin is secrets.tf: creating the secrets that hold the
# host's credentials and setting who may read them. It can also read
# them back, which is no more than this account could already do: it
# can grant itself anything (projectIamAdmin) and mints the minter's
# key itself.
#
# storage.admin is project-level, unlike the two bucket-scoped storage
# roles below, and is there for exactly one thing: deploy/terraform-apply.sh
# creating the state bucket when it does not exist yet. A bucket-scoped
# grant cannot do that -- it is made *on* a bucket, so the bucket has to
# exist for the grant to be made at all, which is the chicken-and-egg
# that made a first deploy fail on "storage: bucket doesn't exist".
#
# It is a real widening: the deployer can read, write and delete every
# bucket in the project, not just its own state. Two things bound what
# that costs. It is CI's identity, reachable only through the workload
# identity provider below, whose attribute condition pins it to one
# repository. And the deployer already holds
# resourcemanager.projectIamAdmin above, so it could always have granted
# itself this and more -- naming the role here makes an existing
# capability legible rather than adding a new one.
for role in \
  roles/artifactregistry.admin \
  roles/compute.admin \
  roles/dns.admin \
  roles/iam.serviceAccountAdmin \
  roles/iam.serviceAccountUser \
  roles/iap.admin \
  roles/resourcemanager.projectIamAdmin \
  roles/secretmanager.admin \
  roles/serviceusage.serviceUsageAdmin \
  roles/serviceusage.serviceUsageConsumer \
  roles/storage.admin
do
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${DEPLOYER_EMAIL}" --role="$role" \
    --condition=None --quiet >/dev/null
  echo "  $role"
done

# Still granted on the bucket itself as well as project-wide above: the
# bucket-scoped pair is what a deployer needs in the ordinary case, and
# keeping them means revoking storage.admin later leaves a working
# deployment rather than one that cannot read its own state.
for role in roles/storage.objectAdmin roles/storage.legacyBucketReader; do
  gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" --project="$PROJECT_ID" \
    --member="serviceAccount:${DEPLOYER_EMAIL}" --role="$role" >/dev/null
  echo "  $role on gs://$BUCKET"
done

if [ -n "$GITHUB_REPO" ]; then
  say "Workload identity federation for $GITHUB_REPO"
  if ! gcloud iam workload-identity-pools describe "$POOL_ID" \
       --project="$PROJECT_ID" --location=global >/dev/null 2>&1; then
    gcloud iam workload-identity-pools create "$POOL_ID" \
      --project="$PROJECT_ID" --location=global \
      --display-name="GitHub Actions"
  fi

  # The attribute condition is the security boundary: without it, *any*
  # GitHub repository in the world could mint tokens for this project.
  if ! gcloud iam workload-identity-pools providers describe "$PROVIDER_ID" \
       --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" >/dev/null 2>&1; then
    gcloud iam workload-identity-pools providers create-oidc "$PROVIDER_ID" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
      --display-name="GitHub Actions OIDC" \
      --issuer-uri="https://token.actions.githubusercontent.com" \
      --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner" \
      --attribute-condition="assertion.repository == '${GITHUB_REPO}'"
  else
    gcloud iam workload-identity-pools providers update-oidc "$PROVIDER_ID" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
      --attribute-condition="assertion.repository == '${GITHUB_REPO}'" \
      --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner"
  fi

  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
  POOL_NAME="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}"
  PROVIDER_NAME="${POOL_NAME}/providers/${PROVIDER_ID}"

  gcloud iam service-accounts add-iam-policy-binding "$DEPLOYER_EMAIL" \
    --project="$PROJECT_ID" \
    --role="roles/iam.workloadIdentityUser" \
    --member="principalSet://iam.googleapis.com/${POOL_NAME}/attribute.repository/${GITHUB_REPO}" \
    --quiet >/dev/null
fi

cat <<DONE

== Done.

Put this in your backend.hcl:

  bucket = "${BUCKET}"
  prefix = "${NAME_PREFIX}/staging"

and set project_id = "${PROJECT_ID}", deployer_member =
"serviceAccount:${DEPLOYER_EMAIL}" in your tfvars.

Next: terraform init -backend-config=backend.hcl, terraform apply, then
run deploy/push-secrets.sh once (see this directory's README) to put a GitHub
token and a Gemini API key into Secret Manager for the host.
DONE

# The two values a config repo's deploy workflow needs as secrets, which
# only exist when --repo wired workload identity at all. Printed here
# rather than left to be reconstructed by hand: the provider is a long
# resource name with a project *number* in it, and getting it wrong
# surfaces as an authentication failure in CI rather than anything that
# points back here.
if [ -n "$GITHUB_REPO" ]; then
  cat <<DONE
Set these two repository secrets on ${GITHUB_REPO} (the names are the
ones terraform/gcp's own deploy workflow reads):

  GCP_V2_WORKLOAD_IDENTITY_PROVIDER
    ${PROVIDER_NAME}

  GCP_V2_DEPLOYER_SERVICE_ACCOUNT
    ${DEPLOYER_EMAIL}

DONE
fi
