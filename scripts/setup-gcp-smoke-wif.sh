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

# setup-gcp-smoke-wif.sh -- one-time (and idempotent) operator setup for
# .github/workflows/gcp-smoke.yml using Google Cloud Workload Identity
# Federation (no long-lived service account keys).
#
# Run by a human operator with project Owner / IAM Admin rights on a dedicated
# or shared smoke-test GCP project. Never run automatically by CI.
#
# Usage:
#   ./scripts/setup-gcp-smoke-wif.sh <gcp-project-id> [options]
#   ./scripts/setup-gcp-smoke-wif.sh --project <gcp-project-id> [options]
#
# What this configures:
#   1. Enables the IAM, IAM Credentials, STS, Compute Engine, GKE, IAP, OS
#      Login, and Cloud Resource Manager APIs.
#   2. Creates (or undeletes/updates) Workload Identity Pool `grain-gcp-smoke`
#      and OIDC provider `github` for `https://token.actions.githubusercontent.com`,
#      restricted by CEL `--attribute-condition` to `ai-on-gke/grain` (and
#      optionally to a single git ref via `--ref`, e.g. `refs/heads/main`;
#      left unpinned to `refs/heads/main` by default so `workflow_dispatch` on
#      `candidate` / release branches and sibling workflows in `.github/workflows/`
#      that read `GCP_WIF_PROVIDER` on `candidate` and `v*` tags also work).
#   3. Creates service account `grain-gcp-smoke@<project>.iam.gserviceaccount.com`
#      with the least roles `scripts/gce-vm-smoke.sh --iap`,
#      `scripts/gke-cluster-smoke.sh`, and the cleanup sweep require:
#        - Project roles:
#            roles/compute.instanceAdmin.v1   (VM + boot disk create/SSH metadata/delete, subnet list)
#            roles/compute.osLogin            (SSH login when OS Login is enforced on the project)
#            roles/iap.tunnelResourceAccessor (SSH over `--tunnel-through-iap`)
#            roles/container.admin            (GKE cluster + node-pool lifecycle and kubectl checks)
#        - Self-scoped SA role (NOT project-wide):
#            roles/iam.serviceAccountUser on `grain-gcp-smoke@...` itself so GKE
#            can attach the smoke SA as its node service account without granting
#            actAs on the project's default Compute Engine SA or any other SA.
#   4. Binds `roles/iam.workloadIdentityUser` on the service account for the
#      pool's `attribute.repository/<owner>/<repo>` principalSet.
#   5. Ensures the target VPC network (`default` in `us-central1` unless
#      overridden or skipped via `--skip-network-prereqs`) has:
#        - an ingress firewall rule admitting TCP port 22 from IAP's forwarding
#          range `35.235.240.0/20` (for `gce-vm-smoke.sh --iap`), and
#        - a Cloud Router + Cloud NAT gateway in the region (so
#          `gke-cluster-smoke.sh`'s `--enable-private-nodes` nodes can pull
#          `nginx:alpine` from Docker Hub).
#   6. Prints the repository VARIABLES (`GCP_WIF_PROVIDER`, `GCP_SA_EMAIL`, and
#      optional `GCP_PROJECT_ID`) to set on GitHub, plus per-run cost notes.

set -euo pipefail

PROJECT_ID=""
GITHUB_REPO="ai-on-gke/grain"
GIT_REF=""
POOL_ID="grain-gcp-smoke"
PROVIDER_ID="github"
SA_NAME="grain-gcp-smoke"
NETWORK="default"
REGION="us-central1"
SKIP_NETWORK_PREREQS=0
ALLOW_DEFAULT_COMPUTE_SA=0
DRY_RUN=0

usage() {
  cat <<'USAGE'
Usage:
  ./scripts/setup-gcp-smoke-wif.sh <project-id> [options]
  ./scripts/setup-gcp-smoke-wif.sh --project <project-id> [options]

Options:
  --project ID               GCP project ID (required; may also be the first positional arg)
  --repo OWNER/NAME          GitHub repository allowed by WIF (default: ai-on-gke/grain)
  --ref GIT_REF              Additionally restrict OIDC attribute condition to assertion.ref == GIT_REF
                             (e.g. refs/heads/main). Omitted by default so workflow_dispatch on
                             candidate/branches and release workflows sharing GCP_WIF_PROVIDER work.
  --pool ID                  Workload Identity Pool ID (default: grain-gcp-smoke)
  --provider ID              Workload Identity Provider ID (default: github)
  --service-account NAME     Service account ID without @... domain (default: grain-gcp-smoke)
  --network NAME             VPC network used by smoke runs (default: default)
  --region REGION            Region of the smoke zone (default: us-central1 for us-central1-a)
  --skip-network-prereqs     Do not check/create IAP SSH firewall rule or Cloud NAT on --network
  --allow-default-compute-sa Also grant roles/iam.serviceAccountUser on the project's default
                             Compute Engine SA (only needed if running SERVICE_ACCOUNT_SHAPE=default)
  --dry-run                  Print the gcloud commands that would run without mutating GCP
  -h, --help                 Show this help text and exit
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --*=*) set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
  esac
  case "$1" in
    --project)         PROJECT_ID="${2:-}"; shift 2 ;;
    --repo)            GITHUB_REPO="${2:-}"; shift 2 ;;
    --ref)             GIT_REF="${2:-}"; shift 2 ;;
    --pool)            POOL_ID="${2:-}"; shift 2 ;;
    --provider)        PROVIDER_ID="${2:-}"; shift 2 ;;
    --service-account) SA_NAME="${2:-}"; shift 2 ;;
    --network)         NETWORK="${2:-}"; shift 2 ;;
    --region)          REGION="${2:-}"; shift 2 ;;
    --skip-network-prereqs)     SKIP_NETWORK_PREREQS=1; shift ;;
    --allow-default-compute-sa) ALLOW_DEFAULT_COMPUTE_SA=1; shift ;;
    --dry-run)         DRY_RUN=1; shift ;;
    -h|--help)         usage; exit 0 ;;
    -*)
      echo "setup-gcp-smoke-wif.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      if [[ -z "$PROJECT_ID" ]]; then
        PROJECT_ID="$1"
        shift
      else
        echo "setup-gcp-smoke-wif.sh: unexpected positional argument: $1" >&2
        usage >&2
        exit 2
      fi
      ;;
  esac
done

if [[ -z "$PROJECT_ID" ]]; then
  echo "setup-gcp-smoke-wif.sh: a GCP project ID is required (this script never runs on its own)." >&2
  usage >&2
  exit 2
fi

if [[ "$GITHUB_REPO" != */* || "$GITHUB_REPO" == */*/* ]]; then
  echo "setup-gcp-smoke-wif.sh: --repo must have the form OWNER/NAME (got '$GITHUB_REPO')" >&2
  exit 2
fi

for val in "$PROJECT_ID" "$GITHUB_REPO" "$GIT_REF"; do
  if [[ "$val" == *"'"* ]]; then
    echo "setup-gcp-smoke-wif.sh: single quotes are not allowed in project/repo/ref values" >&2
    exit 2
  fi
done

say() { printf '\n== %s\n' "$*"; }

run_gcloud() {
  if [[ $DRY_RUN -eq 1 ]]; then
    printf '  [dry-run] gcloud'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  gcloud "$@"
}

if [[ $DRY_RUN -eq 0 ]]; then
  command -v gcloud >/dev/null 2>&1 || {
    echo "setup-gcp-smoke-wif.sh: gcloud CLI is not installed" >&2
    exit 2
  }
  active_account="$(gcloud config get-value account 2>/dev/null || true)"
  if [[ -z "$active_account" || "$active_account" == "(unset)" ]]; then
    echo "setup-gcp-smoke-wif.sh: no active gcloud account; run 'gcloud auth login' first" >&2
    exit 2
  fi
fi

SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
ATTR_MAPPING="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner,attribute.ref=assertion.ref"
ATTR_CONDITION="assertion.repository == '${GITHUB_REPO}'"
if [[ -n "$GIT_REF" ]]; then
  ATTR_CONDITION="${ATTR_CONDITION} && assertion.ref == '${GIT_REF}'"
fi

APIS=(
  iam.googleapis.com
  iamcredentials.googleapis.com
  sts.googleapis.com
  compute.googleapis.com
  container.googleapis.com
  iap.googleapis.com
  oslogin.googleapis.com
  cloudresourcemanager.googleapis.com
)

PROJECT_ROLES=(
  roles/compute.instanceAdmin.v1
  roles/compute.osLogin
  roles/iap.tunnelResourceAccessor
  roles/container.admin
)

say "Enabling required Google Cloud APIs in project $PROJECT_ID"
run_gcloud services enable --project="$PROJECT_ID" "${APIS[@]}"

if [[ $DRY_RUN -eq 1 ]]; then
  PROJECT_NUMBER="<PROJECT_NUMBER>"
else
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
  if [[ -z "$PROJECT_NUMBER" ]]; then
    echo "setup-gcp-smoke-wif.sh: could not resolve projectNumber for '$PROJECT_ID'" >&2
    exit 1
  fi
fi

POOL_RESOURCE="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}"
PROVIDER_RESOURCE="${POOL_RESOURCE}/providers/${PROVIDER_ID}"
WIF_PRINCIPAL_SET="principalSet://iam.googleapis.com/${POOL_RESOURCE}/attribute.repository/${GITHUB_REPO}"

say "Configuring Workload Identity Pool '$POOL_ID'"
if [[ $DRY_RUN -eq 1 ]]; then
  run_gcloud iam workload-identity-pools create "$POOL_ID" \
    --project="$PROJECT_ID" --location=global \
    --display-name="GitHub Actions gcp-smoke"
else
  pool_state="$(gcloud iam workload-identity-pools describe "$POOL_ID" \
    --project="$PROJECT_ID" --location=global --format='value(state)' 2>/dev/null || true)"
  if [[ -z "$pool_state" ]]; then
    gcloud iam workload-identity-pools create "$POOL_ID" \
      --project="$PROJECT_ID" --location=global \
      --display-name="GitHub Actions gcp-smoke"
  elif [[ "$pool_state" == "DELETED" ]]; then
    gcloud iam workload-identity-pools undelete "$POOL_ID" \
      --project="$PROJECT_ID" --location=global
  else
    echo "  pool '$POOL_ID' already exists (state: $pool_state)"
  fi
fi

say "Configuring OIDC Provider '$PROVIDER_ID' ($ATTR_CONDITION)"
if [[ $DRY_RUN -eq 1 ]]; then
  run_gcloud iam workload-identity-pools providers create-oidc "$PROVIDER_ID" \
    --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
    --display-name="GitHub Actions OIDC" \
    --issuer-uri="https://token.actions.githubusercontent.com" \
    --attribute-mapping="$ATTR_MAPPING" \
    --attribute-condition="$ATTR_CONDITION"
else
  provider_state="$(gcloud iam workload-identity-pools providers describe "$PROVIDER_ID" \
    --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
    --format='value(state)' 2>/dev/null || true)"
  if [[ -z "$provider_state" ]]; then
    gcloud iam workload-identity-pools providers create-oidc "$PROVIDER_ID" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
      --display-name="GitHub Actions OIDC" \
      --issuer-uri="https://token.actions.githubusercontent.com" \
      --attribute-mapping="$ATTR_MAPPING" \
      --attribute-condition="$ATTR_CONDITION"
  else
    if [[ "$provider_state" == "DELETED" ]]; then
      gcloud iam workload-identity-pools providers undelete "$PROVIDER_ID" \
        --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID"
    fi
    gcloud iam workload-identity-pools providers update-oidc "$PROVIDER_ID" \
      --project="$PROJECT_ID" --location=global --workload-identity-pool="$POOL_ID" \
      --attribute-mapping="$ATTR_MAPPING" \
      --attribute-condition="$ATTR_CONDITION" >/dev/null
    echo "  updated OIDC provider '$PROVIDER_ID' attribute mapping and condition"
  fi
fi

say "Configuring Service Account $SA_EMAIL"
if [[ $DRY_RUN -eq 1 ]]; then
  run_gcloud iam service-accounts create "$SA_NAME" \
    --project="$PROJECT_ID" \
    --display-name="GitHub Actions GCE/GKE smoke ($GITHUB_REPO)"
else
  if ! gcloud iam service-accounts describe "$SA_EMAIL" --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$SA_NAME" \
      --project="$PROJECT_ID" \
      --display-name="GitHub Actions GCE/GKE smoke ($GITHUB_REPO)"
  else
    sa_disabled="$(gcloud iam service-accounts describe "$SA_EMAIL" \
      --project="$PROJECT_ID" --format='value(disabled)' 2>/dev/null || true)"
    if [[ "$sa_disabled" == "True" ]]; then
      gcloud iam service-accounts enable "$SA_EMAIL" --project="$PROJECT_ID"
    fi
    echo "  service account '$SA_EMAIL' already exists"
  fi
fi

say "Binding least-privilege project roles on $PROJECT_ID"
for role in "${PROJECT_ROLES[@]}"; do
  run_gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="$role" \
    --condition=None \
    --quiet >/dev/null
  echo "  $role"
done

say "Binding roles/iam.serviceAccountUser on $SA_EMAIL (self-attachment for GKE nodes)"
run_gcloud iam service-accounts add-iam-policy-binding "$SA_EMAIL" \
  --project="$PROJECT_ID" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/iam.serviceAccountUser" \
  --condition=None \
  --quiet >/dev/null
echo "  roles/iam.serviceAccountUser on $SA_EMAIL"

if [[ $ALLOW_DEFAULT_COMPUTE_SA -eq 1 ]]; then
  DEFAULT_COMPUTE_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
  say "Binding roles/iam.serviceAccountUser on $DEFAULT_COMPUTE_SA (--allow-default-compute-sa)"
  run_gcloud iam service-accounts add-iam-policy-binding "$DEFAULT_COMPUTE_SA" \
    --project="$PROJECT_ID" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="roles/iam.serviceAccountUser" \
    --condition=None \
    --quiet >/dev/null
  echo "  roles/iam.serviceAccountUser on $DEFAULT_COMPUTE_SA"
fi

say "Binding roles/iam.workloadIdentityUser for $WIF_PRINCIPAL_SET"
run_gcloud iam service-accounts add-iam-policy-binding "$SA_EMAIL" \
  --project="$PROJECT_ID" \
  --member="$WIF_PRINCIPAL_SET" \
  --role="roles/iam.workloadIdentityUser" \
  --condition=None \
  --quiet >/dev/null
echo "  roles/iam.workloadIdentityUser <- $WIF_PRINCIPAL_SET"

if [[ $SKIP_NETWORK_PREREQS -eq 0 ]]; then
  say "Checking network prerequisites on '$NETWORK' ($REGION) for IAP SSH and private GKE nodes"
  if [[ $DRY_RUN -eq 1 ]]; then
    run_gcloud compute firewall-rules create gcp-smoke-allow-iap-ssh \
      --project="$PROJECT_ID" --network="$NETWORK" \
      --direction=INGRESS --action=ALLOW --rules=tcp:22 \
      --source-ranges=35.235.240.0/20
    run_gcloud compute routers create gcp-smoke-router \
      --project="$PROJECT_ID" --network="$NETWORK" --region="$REGION"
    run_gcloud compute routers nats create gcp-smoke-nat \
      --project="$PROJECT_ID" --router=gcp-smoke-router --region="$REGION" \
      --nat-all-subnet-ip-ranges --auto-allocate-nat-external-ips
  else
    if ! gcloud compute networks describe "$NETWORK" --project="$PROJECT_ID" >/dev/null 2>&1; then
      echo "  creating missing VPC network '$NETWORK' and subnet in '$REGION'"
      if [[ "$NETWORK" == "default" ]]; then
        gcloud compute networks create "$NETWORK" \
          --project="$PROJECT_ID" --subnet-mode=auto
      else
        gcloud compute networks create "$NETWORK" \
          --project="$PROJECT_ID" --subnet-mode=custom
        gcloud compute networks subnets create "${NETWORK}-${REGION}" \
          --project="$PROJECT_ID" --network="$NETWORK" --region="$REGION" \
          --range="10.128.0.0/20" --enable-private-ip-google-access
      fi
    fi

    fw_rules="$(gcloud compute firewall-rules list --project="$PROJECT_ID" \
      --format="value[separator=' | '](network.basename(),name,direction,disabled,sourceRanges.list(),allowed[].map().firewall_rule().list(),targetTags.list())" 2>/dev/null || true)"
    iap_ssh_rule="$(awk -F ' \\| ' -v net="$NETWORK" '
      function admits_iap(ranges,   n, i, r) {
        n = split(ranges, r, ",")
        for (i = 1; i <= n; i++)
          if (r[i] == "35.235.240.0/20" || r[i] == "0.0.0.0/0") return 1
        return 0
      }
      function reaches_ssh(allowed,   n, i, a, lo_hi) {
        n = split(allowed, a, ",")
        for (i = 1; i <= n; i++) {
          if (a[i] == "tcp" || a[i] == "tcp:22") return 1
          if (a[i] ~ /^tcp:[0-9]+-[0-9]+$/) {
            split(substr(a[i], 5), lo_hi, "-")
            if (lo_hi[1] + 0 <= 22 && 22 <= lo_hi[2] + 0) return 1
          }
        }
        return 0
      }
      $1 == net && $3 == "INGRESS" && $4 == "False" && $7 == "" && admits_iap($5) && reaches_ssh($6) {
        print $2; exit
      }' <<<"$fw_rules")"
    if [[ -n "$iap_ssh_rule" ]]; then
      echo "  IAP TCP-22 ingress already admitted on '$NETWORK' by rule '$iap_ssh_rule'"
    elif gcloud compute firewall-rules describe gcp-smoke-allow-iap-ssh --project="$PROJECT_ID" >/dev/null 2>&1; then
      gcloud compute firewall-rules update gcp-smoke-allow-iap-ssh \
        --project="$PROJECT_ID" \
        --rules=tcp:22 \
        --source-ranges=35.235.240.0/20 \
        --no-disabled >/dev/null
      echo "  re-enabled firewall rule 'gcp-smoke-allow-iap-ssh' on '$NETWORK'"
    else
      gcloud compute firewall-rules create gcp-smoke-allow-iap-ssh \
        --project="$PROJECT_ID" \
        --network="$NETWORK" \
        --direction=INGRESS \
        --action=ALLOW \
        --rules=tcp:22 \
        --source-ranges=35.235.240.0/20 \
        --description="Allow IAP TCP forwarding to port 22 for gce-vm-smoke.sh --iap" >/dev/null
      echo "  created firewall rule 'gcp-smoke-allow-iap-ssh' (35.235.240.0/20 -> tcp:22) on '$NETWORK'"
    fi

    existing_nat=""
    while read -r router_name; do
      [[ -n "$router_name" ]] || continue
      nats="$(gcloud compute routers nats list --project="$PROJECT_ID" \
        --router="$router_name" --region="$REGION" --format='value(name)' 2>/dev/null || true)"
      if [[ -n "$nats" ]]; then
        existing_nat="${router_name}/${nats%%$'\n'*}"
        break
      fi
    done < <(gcloud compute routers list --project="$PROJECT_ID" \
      --filter="region:(${REGION}) AND network.basename()=${NETWORK}" \
      --format='value(name)' 2>/dev/null || true)

    if [[ -n "$existing_nat" ]]; then
      echo "  Cloud NAT already present on '$NETWORK' in '$REGION' ($existing_nat)"
    else
      if ! gcloud compute routers describe gcp-smoke-router \
          --project="$PROJECT_ID" --region="$REGION" >/dev/null 2>&1; then
        gcloud compute routers create gcp-smoke-router \
          --project="$PROJECT_ID" --network="$NETWORK" --region="$REGION" >/dev/null
      fi
      gcloud compute routers nats create gcp-smoke-nat \
        --project="$PROJECT_ID" --router=gcp-smoke-router --region="$REGION" \
        --nat-all-subnet-ip-ranges --auto-allocate-nat-external-ips >/dev/null
      echo "  created Cloud Router + Cloud NAT 'gcp-smoke-router/gcp-smoke-nat' in '$REGION' (needed by private GKE nodes to pull nginx:alpine)"
    fi
  fi
fi

cat <<DONE

== Done.

Set these repository VARIABLES (NOT secrets) on ${GITHUB_REPO} under:
  Settings -> Secrets and variables -> Actions -> Variables

  GCP_WIF_PROVIDER=${PROVIDER_RESOURCE}
  GCP_SA_EMAIL=${SA_EMAIL}

Optional repository variable (inferred automatically from GCP_SA_EMAIL when it
ends in @${PROJECT_ID}.iam.gserviceaccount.com, or set explicitly):

  GCP_PROJECT_ID=${PROJECT_ID}

Or with the GitHub CLI:
  gh variable set GCP_WIF_PROVIDER --repo ${GITHUB_REPO} --body '${PROVIDER_RESOURCE}'
  gh variable set GCP_SA_EMAIL     --repo ${GITHUB_REPO} --body '${SA_EMAIL}'
  gh variable set GCP_PROJECT_ID   --repo ${GITHUB_REPO} --body '${PROJECT_ID}'

Cost & cleanup notes:
  - Per smoke run (~14 minutes total): ~\$0.01-\$0.04 (one e2-micro VM + 10 GB
    disk for ~90s, plus a 1->2 node e2-medium zonal GKE cluster + 50 GB disks
    for ~12m; \$0 GKE management fee if covered by the billing account's single
    free zonal cluster tier, or ~\$0.025 otherwise). Daily cron is ~\$0.40-\$1.20/mo
    (plus ~\$1/mo if gcp-smoke-nat was created in a project with no existing NAT).
  - Both scripts delete their VM/cluster in an EXIT trap on success or failure,
    and .github/workflows/gcp-smoke.yml's 'Delete anything left behind' step
    (if: always()) sweeps both the current run's resources and any stale
    purpose=gce-vm-smoke / purpose=gke-cluster-smoke resources older than 2h.
DONE
