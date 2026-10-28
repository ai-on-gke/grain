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

# deploy-gke.sh — Idempotent, re-runnable entry point to provision a GKE cluster,
# deploy grain behind Identity-Aware Proxy (IAP), connect the existing host's
# `grain hostd`, migrate state, cut over, roll back, and verify the deployment.
#
# Implements docs/design/grain-on-gke.md (Task #1460).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Defaults (overridable via --config file, GRAIN_GKE_* env vars, or CLI flags).
CONFIG_FILE="${GRAIN_GKE_CONFIG:-}"
PROJECT="${GRAIN_GKE_PROJECT:-}"
REGION="${GRAIN_GKE_REGION:-us-central1}"
ZONE="${GRAIN_GKE_ZONE:-}"
CLUSTER="${GRAIN_GKE_CLUSTER:-grain-gke}"
ADOPT_CLUSTER="${GRAIN_GKE_ADOPT_CLUSTER:-false}"
NAMESPACE="${GRAIN_GKE_NAMESPACE:-grain}"
DNS_NAME="${GRAIN_GKE_DNS_NAME:-}"
DNS_ZONE="${GRAIN_GKE_DNS_ZONE:-}"
IAP_PRINCIPALS_RAW="${GRAIN_GKE_IAP_PRINCIPALS:-}"
IAP_CLIENT_ID="${GRAIN_GKE_IAP_CLIENT_ID:-}"
IAP_CLIENT_SECRET_FILE="${GRAIN_GKE_IAP_CLIENT_SECRET_FILE:-}"
IMAGE="${GRAIN_GKE_IMAGE:-ghcr.io/bwsalmon/grain/grain:main}"
SANDBOX_IMAGE="${GRAIN_GKE_SANDBOX_IMAGE:-ghcr.io/bwsalmon/grain/granule:latest}"
POSTGRES_MODE="${GRAIN_GKE_POSTGRES:-cloudsql}"
CLOUDSQL_INSTANCE="${GRAIN_GKE_CLOUDSQL_INSTANCE:-grain-postgres}"
CLOUDSQL_TIER="${GRAIN_GKE_CLOUDSQL_TIER:-db-custom-2-8192}"
DAEMON_MACHINE_TYPE="${GRAIN_GKE_DAEMON_MACHINE_TYPE:-e2-standard-4}"
SANDBOX_MACHINE_TYPE="${GRAIN_GKE_SANDBOX_MACHINE_TYPE:-n2-standard-16}"
SANDBOX_MIN_NODES="${GRAIN_GKE_SANDBOX_MIN_NODES:-1}"
SANDBOX_MAX_NODES="${GRAIN_GKE_SANDBOX_MAX_NODES:-8}"
TARGET_REPO="${GRAIN_GKE_TARGET_REPO:-ai-on-gke/grain}"
HOSTD_NAME="${GRAIN_GKE_HOSTD_NAME:-$(hostname -s 2>/dev/null || echo workstation)}"
HOSTD_OWNER="${GRAIN_GKE_HOSTD_OWNER:-${USER:-operator}}"
DATA_DIR="${GRAIN_GKE_DATA_DIR:-/var/lib/grain/data}"
STAGING_BUCKET="${GRAIN_GKE_STAGING_BUCKET:-}"
ACCESS_MODE="${GRAIN_GKE_ACCESS_MODE:-dialout}"
USE_TERRAFORM="${GRAIN_GKE_USE_TERRAFORM:-false}"

DRY_RUN=false
DO_MIGRATE=false
DO_CUTOVER=false
DO_ROLLBACK=false
DO_CHECK=false

declare -a IAP_PRINCIPALS=()

usage() {
  cat <<'EOF'
Usage: scripts/deploy-gke.sh [options]

Deploy Grain onto a GKE cluster behind Identity-Aware Proxy (IAP) and connect
the existing workstation's `grain hostd` over a single outbound IAP/TLS channel.

Options:
  --config <path>                 Load settings from a key=value env file
  --project <id>                  GCP project ID (required)
  --region <region>               GCP region (default: us-central1)
  --zone <zone>                   GCP zone (default: <region>-a)
  --cluster <name>                GKE cluster name (default: grain-gke)
  --adopt-cluster                 Adopt an existing GKE cluster by name
  --namespace <name>              Kubernetes namespace (default: grain)
  --dns-name <fqdn>               DNS hostname for the Gateway (required)
  --dns-zone <name>               Cloud DNS managed zone name (optional)
  --iap-principal <member>        IAM principal granted IAP access (repeatable,
                                  e.g. user:alice@example.com)
  --iap-principals <csv>          Comma-separated IAM principals for IAP access
  --iap-client-id <id>            Custom IAP OAuth 2.0 client ID (optional;
                                  defaults to Google-managed IAP OAuth client)
  --iap-client-secret-file <path> Path to file containing IAP OAuth client secret
  --image <ref>                   Grain container image from ghcr.io/bwsalmon/grain/...
                                  (pinned by @sha256: digest during deploy)
  --sandbox-image <ref>           Granule packed guest image from ghcr.io/bwsalmon/grain/...
                                  (pinned by @sha256: digest during deploy)
  --postgres <cloudsql|in-cluster>
                                  PostgreSQL backing store mode (default: cloudsql)
  --cloudsql-instance <name>      Cloud SQL instance name (default: grain-postgres)
  --cloudsql-tier <tier>          Cloud SQL machine tier (default: db-custom-2-8192)
  --daemon-machine-type <type>    Machine type for the daemon pool (default: e2-standard-4)
  --sandbox-machine-type <type>   Machine type for the nested-virt sandbox pool
                                  (default: n2-standard-16)
  --target-repo <owner/name>      Default target repository (default: ai-on-gke/grain)
  --hostd-name <name>             Hostd registration name (default: short hostname)
  --hostd-owner <principal>       Hostd owner principal (default: $USER)
  --data-dir <path>               Local grain data directory (default: /var/lib/grain/data)
  --staging-bucket <name>         GCS bucket for migration staging & backups
  --access-mode <dialout|kubectl> Sandbox access channel mode (default: dialout)
  --use-terraform                 Apply infrastructure via deploy/gke/terraform
  --dry-run                       Print every action without modifying anything
  --migrate                       Migrate cloudtop store, secrets, and PVC data to GKE
  --cutover                       Drain and stop cloudtop grain-daemon, then scale GKE
                                  daemon to 1 (refuses if two daemons would be active)
  --rollback                      Scale GKE daemon to 0 and restore cloudtop grain-daemon
  --check                         Verify IAP UI, hostd connection, dial-out sandbox exec,
                                  and zero exec/attach/portforward in API server audit logs
  -h, --help                      Show this help message
EOF
}

log() {
  printf '[deploy-gke] %s\n' "$*"
}

warn() {
  printf '[deploy-gke] WARNING: %s\n' "$*" >&2
}

die() {
  printf '[deploy-gke] ERROR: %s\n' "$*" >&2
  exit 1
}

run_cmd() {
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf 'DRY-RUN:'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

append_iap_principals_csv() {
  local csv="$1"
  local item
  IFS=',' read -r -a items <<<"${csv}"
  for item in "${items[@]}"; do
    item="${item#"${item%%[![:space:]]*}"}"
    item="${item%"${item##*[![:space:]]}"}"
    if [[ -n "${item}" ]]; then
      IAP_PRINCIPALS+=("${item}")
    fi
  done
}

load_config_file() {
  local file="$1"
  [[ -f "${file}" ]] || die "config file not found: ${file}"
  local raw line key val
  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    line="${raw#"${raw%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    if [[ -z "${line}" || "${line}" == \#* ]]; then
      continue
    fi
    if [[ ! "${line}" =~ ^([A-Z0-9_]+)=(.*)$ ]]; then
      die "invalid line in config file ${file}: ${line}"
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    val="${val#"${val%%[![:space:]]*}"}"
    val="${val%"${val##*[![:space:]]}"}"
    if [[ "${val}" =~ ^\"(.*)\"$ ]]; then
      val="${BASH_REMATCH[1]}"
    elif [[ "${val}" =~ ^\'(.*)\'$ ]]; then
      val="${BASH_REMATCH[1]}"
    fi
    if [[ "${val}" == *'$('* || "${val}" == *'`'* ]]; then
      die "refusing command substitution in config file ${file} for ${key}"
    fi
    case "${key}" in
      GRAIN_GKE_PROJECT) PROJECT="${val}" ;;
      GRAIN_GKE_REGION) REGION="${val}" ;;
      GRAIN_GKE_ZONE) ZONE="${val}" ;;
      GRAIN_GKE_CLUSTER) CLUSTER="${val}" ;;
      GRAIN_GKE_ADOPT_CLUSTER) ADOPT_CLUSTER="${val}" ;;
      GRAIN_GKE_NAMESPACE) NAMESPACE="${val}" ;;
      GRAIN_GKE_DNS_NAME) DNS_NAME="${val}" ;;
      GRAIN_GKE_DNS_ZONE) DNS_ZONE="${val}" ;;
      GRAIN_GKE_IAP_PRINCIPALS) IAP_PRINCIPALS_RAW="${val}" ;;
      GRAIN_GKE_IAP_CLIENT_ID) IAP_CLIENT_ID="${val}" ;;
      GRAIN_GKE_IAP_CLIENT_SECRET_FILE) IAP_CLIENT_SECRET_FILE="${val}" ;;
      GRAIN_GKE_IMAGE) IMAGE="${val}" ;;
      GRAIN_GKE_SANDBOX_IMAGE) SANDBOX_IMAGE="${val}" ;;
      GRAIN_GKE_POSTGRES) POSTGRES_MODE="${val}" ;;
      GRAIN_GKE_CLOUDSQL_INSTANCE) CLOUDSQL_INSTANCE="${val}" ;;
      GRAIN_GKE_CLOUDSQL_TIER) CLOUDSQL_TIER="${val}" ;;
      GRAIN_GKE_DAEMON_MACHINE_TYPE) DAEMON_MACHINE_TYPE="${val}" ;;
      GRAIN_GKE_SANDBOX_MACHINE_TYPE) SANDBOX_MACHINE_TYPE="${val}" ;;
      GRAIN_GKE_SANDBOX_MIN_NODES) SANDBOX_MIN_NODES="${val}" ;;
      GRAIN_GKE_SANDBOX_MAX_NODES) SANDBOX_MAX_NODES="${val}" ;;
      GRAIN_GKE_TARGET_REPO) TARGET_REPO="${val}" ;;
      GRAIN_GKE_HOSTD_NAME) HOSTD_NAME="${val}" ;;
      GRAIN_GKE_HOSTD_OWNER) HOSTD_OWNER="${val}" ;;
      GRAIN_GKE_DATA_DIR) DATA_DIR="${val}" ;;
      GRAIN_GKE_STAGING_BUCKET) STAGING_BUCKET="${val}" ;;
      GRAIN_GKE_ACCESS_MODE) ACCESS_MODE="${val}" ;;
      GRAIN_GKE_USE_TERRAFORM) USE_TERRAFORM="${val}" ;;
      *) die "unknown config key in ${file}: ${key}" ;;
    esac
  done <"${file}"
}

# Pre-scan for --config so explicit CLI flags override config file values.
prev_is_config=false
for arg in "$@"; do
  if [[ "${prev_is_config}" == "true" ]]; then
    CONFIG_FILE="${arg}"
    prev_is_config=false
    continue
  fi
  case "${arg}" in
    --config)
      prev_is_config=true
      ;;
    --config=*)
      CONFIG_FILE="${arg#*=}"
      ;;
  esac
done

if [[ -n "${CONFIG_FILE}" ]]; then
  load_config_file "${CONFIG_FILE}"
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)
      shift 2
      ;;
    --config=*)
      shift
      ;;
    --project)
      PROJECT="$2"
      shift 2
      ;;
    --project=*)
      PROJECT="${1#*=}"
      shift
      ;;
    --region)
      REGION="$2"
      shift 2
      ;;
    --region=*)
      REGION="${1#*=}"
      shift
      ;;
    --zone)
      ZONE="$2"
      shift 2
      ;;
    --zone=*)
      ZONE="${1#*=}"
      shift
      ;;
    --cluster)
      CLUSTER="$2"
      shift 2
      ;;
    --cluster=*)
      CLUSTER="${1#*=}"
      shift
      ;;
    --adopt-cluster)
      ADOPT_CLUSTER="true"
      shift
      ;;
    --namespace)
      NAMESPACE="$2"
      shift 2
      ;;
    --namespace=*)
      NAMESPACE="${1#*=}"
      shift
      ;;
    --dns-name)
      DNS_NAME="$2"
      shift 2
      ;;
    --dns-name=*)
      DNS_NAME="${1#*=}"
      shift
      ;;
    --dns-zone)
      DNS_ZONE="$2"
      shift 2
      ;;
    --dns-zone=*)
      DNS_ZONE="${1#*=}"
      shift
      ;;
    --iap-principal)
      append_iap_principals_csv "$2"
      shift 2
      ;;
    --iap-principal=*)
      append_iap_principals_csv "${1#*=}"
      shift
      ;;
    --iap-principals)
      append_iap_principals_csv "$2"
      shift 2
      ;;
    --iap-principals=*)
      append_iap_principals_csv "${1#*=}"
      shift
      ;;
    --iap-client-id)
      IAP_CLIENT_ID="$2"
      shift 2
      ;;
    --iap-client-id=*)
      IAP_CLIENT_ID="${1#*=}"
      shift
      ;;
    --iap-client-secret-file)
      IAP_CLIENT_SECRET_FILE="$2"
      shift 2
      ;;
    --iap-client-secret-file=*)
      IAP_CLIENT_SECRET_FILE="${1#*=}"
      shift
      ;;
    --image)
      IMAGE="$2"
      shift 2
      ;;
    --image=*)
      IMAGE="${1#*=}"
      shift
      ;;
    --sandbox-image)
      SANDBOX_IMAGE="$2"
      shift 2
      ;;
    --sandbox-image=*)
      SANDBOX_IMAGE="${1#*=}"
      shift
      ;;
    --postgres)
      POSTGRES_MODE="$2"
      shift 2
      ;;
    --postgres=*)
      POSTGRES_MODE="${1#*=}"
      shift
      ;;
    --cloudsql-instance)
      CLOUDSQL_INSTANCE="$2"
      shift 2
      ;;
    --cloudsql-instance=*)
      CLOUDSQL_INSTANCE="${1#*=}"
      shift
      ;;
    --cloudsql-tier)
      CLOUDSQL_TIER="$2"
      shift 2
      ;;
    --cloudsql-tier=*)
      CLOUDSQL_TIER="${1#*=}"
      shift
      ;;
    --daemon-machine-type)
      DAEMON_MACHINE_TYPE="$2"
      shift 2
      ;;
    --daemon-machine-type=*)
      DAEMON_MACHINE_TYPE="${1#*=}"
      shift
      ;;
    --sandbox-machine-type)
      SANDBOX_MACHINE_TYPE="$2"
      shift 2
      ;;
    --sandbox-machine-type=*)
      SANDBOX_MACHINE_TYPE="${1#*=}"
      shift
      ;;
    --target-repo)
      TARGET_REPO="$2"
      shift 2
      ;;
    --target-repo=*)
      TARGET_REPO="${1#*=}"
      shift
      ;;
    --hostd-name)
      HOSTD_NAME="$2"
      shift 2
      ;;
    --hostd-name=*)
      HOSTD_NAME="${1#*=}"
      shift
      ;;
    --hostd-owner)
      HOSTD_OWNER="$2"
      shift 2
      ;;
    --hostd-owner=*)
      HOSTD_OWNER="${1#*=}"
      shift
      ;;
    --data-dir)
      DATA_DIR="$2"
      shift 2
      ;;
    --data-dir=*)
      DATA_DIR="${1#*=}"
      shift
      ;;
    --staging-bucket)
      STAGING_BUCKET="$2"
      shift 2
      ;;
    --staging-bucket=*)
      STAGING_BUCKET="${1#*=}"
      shift
      ;;
    --access-mode)
      ACCESS_MODE="$2"
      shift 2
      ;;
    --access-mode=*)
      ACCESS_MODE="${1#*=}"
      shift
      ;;
    --use-terraform)
      USE_TERRAFORM="true"
      shift
      ;;
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --migrate)
      DO_MIGRATE=true
      shift
      ;;
    --cutover)
      DO_CUTOVER=true
      shift
      ;;
    --rollback)
      DO_ROLLBACK=true
      shift
      ;;
    --check)
      DO_CHECK=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1 (run with --help for usage)"
      ;;
  esac
done

if [[ -n "${IAP_PRINCIPALS_RAW}" && "${#IAP_PRINCIPALS[@]}" -eq 0 ]]; then
  append_iap_principals_csv "${IAP_PRINCIPALS_RAW}"
fi

if [[ -z "${ZONE}" ]]; then
  ZONE="${REGION}-a"
fi

if [[ -z "${PROJECT}" ]]; then
  die "--project <id> (or GRAIN_GKE_PROJECT) is required"
fi

if [[ -z "${DNS_NAME}" ]]; then
  die "--dns-name <fqdn> (or GRAIN_GKE_DNS_NAME) is required"
fi

if [[ "${POSTGRES_MODE}" != "cloudsql" && "${POSTGRES_MODE}" != "in-cluster" ]]; then
  die "--postgres must be 'cloudsql' or 'in-cluster' (got '${POSTGRES_MODE}')"
fi

if [[ "${ACCESS_MODE}" != "dialout" && "${ACCESS_MODE}" != "kubectl" ]]; then
  die "--access-mode must be 'dialout' or 'kubectl' (got '${ACCESS_MODE}')"
fi

if [[ "${IMAGE}" != ghcr.io/bwsalmon/grain/* ]]; then
  die "--image must reference ghcr.io/bwsalmon/grain/... (got '${IMAGE}')"
fi

if [[ "${SANDBOX_IMAGE}" != ghcr.io/bwsalmon/grain/* ]]; then
  die "--sandbox-image must reference ghcr.io/bwsalmon/grain/... (got '${SANDBOX_IMAGE}')"
fi

if [[ -z "${STAGING_BUCKET}" ]]; then
  STAGING_BUCKET="${PROJECT}-grain-staging"
fi

[[ "${PROJECT}" =~ ^[a-z][a-z0-9-]{4,29}$ ]] || die "invalid --project '${PROJECT}'"
[[ "${REGION}" =~ ^[a-z0-9-]+$ ]] || die "invalid --region '${REGION}'"
[[ "${ZONE}" =~ ^[a-z0-9-]+$ ]] || die "invalid --zone '${ZONE}'"
[[ "${CLUSTER}" =~ ^[a-z0-9-]+$ ]] || die "invalid --cluster '${CLUSTER}'"
[[ "${NAMESPACE}" =~ ^[a-z0-9-]+$ ]] || die "invalid --namespace '${NAMESPACE}'"
[[ "${CLOUDSQL_INSTANCE}" =~ ^[a-z0-9-]+$ ]] || die "invalid --cloudsql-instance '${CLOUDSQL_INSTANCE}'"
[[ "${DNS_NAME}" =~ ^[a-zA-Z0-9.-]+$ ]] || die "invalid --dns-name '${DNS_NAME}'"
[[ -z "${DNS_ZONE}" || "${DNS_ZONE}" =~ ^[a-z0-9-]+$ ]] || die "invalid --dns-zone '${DNS_ZONE}'"
[[ "${TARGET_REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "invalid --target-repo '${TARGET_REPO}'"
[[ "${HOSTD_NAME}" =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid --hostd-name '${HOSTD_NAME}'"
[[ "${HOSTD_OWNER}" =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid --hostd-owner '${HOSTD_OWNER}'"
[[ "${STAGING_BUCKET}" =~ ^[a-z0-9._-]+$ ]] || die "invalid --staging-bucket '${STAGING_BUCKET}'"

GSA_NAME="grain-daemon"
GSA_EMAIL="${GSA_NAME}@${PROJECT}.iam.gserviceaccount.com"
NODE_GSA_NAME="grain-gke-node"
NODE_GSA_EMAIL="${NODE_GSA_NAME}@${PROJECT}.iam.gserviceaccount.com"
CLOUDSQL_CONN_NAME="${PROJECT}:${REGION}:${CLOUDSQL_INSTANCE}"
GATEWAY_URL="https://${DNS_NAME}"

# Pin a ghcr.io/bwsalmon/grain/... image reference by @sha256: digest.
resolve_image_digest() {
  local ref="$1"
  if [[ "${ref}" =~ @sha256:[0-9a-f]{64}$ ]]; then
    printf '%s\n' "${ref}"
    return 0
  fi
  local repo="${ref%%:*}"
  repo="${repo%%@*}"
  if [[ "${DRY_RUN}" == "true" ]]; then
    local hash
    hash="$(printf '%s' "${ref}" | sha256sum | awk '{print $1}')"
    printf '%s@sha256:%s\n' "${repo}" "${hash}"
    return 0
  fi
  if command -v crane >/dev/null 2>&1; then
    local digest
    digest="$(crane digest "${ref}")"
    printf '%s@%s\n' "${repo}" "${digest}"
    return 0
  fi
  if command -v docker >/dev/null 2>&1; then
    local digest
    digest="$(docker buildx imagetools inspect "${ref}" --format '{{json .Manifest.Digest}}' 2>/dev/null | tr -d '"' || true)"
    if [[ "${digest}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      printf '%s@%s\n' "${repo}" "${digest}"
      return 0
    fi
  fi
  die "cannot resolve @sha256 digest for '${ref}'; pass a digest-pinned image (${repo}@sha256:...) or install crane/docker"
}

PINNED_IMAGE="$(resolve_image_digest "${IMAGE}")"
PINNED_SANDBOX_IMAGE="$(resolve_image_digest "${SANDBOX_IMAGE}")"

# Step 1: Create (or adopt) the GKE cluster, node pools, Workload Identity,
# namespace, PriorityClasses, and ResourceQuota.
stage_cluster() {
  log "Stage 1: Provisioning GKE cluster '${CLUSTER}' in ${PROJECT} (${REGION})..."
  log "  Resolved daemon image:  ${PINNED_IMAGE}"
  log "  Resolved sandbox image: ${PINNED_SANDBOX_IMAGE}"

  if [[ "${USE_TERRAFORM}" == "true" ]]; then
    run_cmd terraform -chdir="${REPO_ROOT}/deploy/gke/terraform" init -input=false
    run_cmd terraform -chdir="${REPO_ROOT}/deploy/gke/terraform" apply -auto-approve \
      -var="project_id=${PROJECT}" \
      -var="region=${REGION}" \
      -var="zone=${ZONE}" \
      -var="cluster_name=${CLUSTER}" \
      -var="adopt_existing_cluster=${ADOPT_CLUSTER}" \
      -var="namespace=${NAMESPACE}" \
      -var="postgres_mode=${POSTGRES_MODE}" \
      -var="cloudsql_instance_name=${CLOUDSQL_INSTANCE}" \
      -var="cloudsql_tier=${CLOUDSQL_TIER}" \
      -var="daemon_machine_type=${DAEMON_MACHINE_TYPE}" \
      -var="sandbox_machine_type=${SANDBOX_MACHINE_TYPE}" \
      -var="sandbox_min_nodes=${SANDBOX_MIN_NODES}" \
      -var="sandbox_max_nodes=${SANDBOX_MAX_NODES}" \
      -var="domain=${DNS_NAME}" \
      -var="dns_managed_zone=${DNS_ZONE}" \
      -var="staging_bucket_name=${STAGING_BUCKET}"
  else
    run_cmd gcloud services enable \
      certificatemanager.googleapis.com \
      compute.googleapis.com \
      container.googleapis.com \
      dns.googleapis.com \
      iam.googleapis.com \
      iap.googleapis.com \
      secretmanager.googleapis.com \
      servicenetworking.googleapis.com \
      sqladmin.googleapis.com \
      storage.googleapis.com \
      --project="${PROJECT}"

    run_cmd gcloud iam service-accounts create "${NODE_GSA_NAME}" \
      --project="${PROJECT}" \
      --display-name="Grain GKE node pool service account" || true

    run_cmd gcloud projects add-iam-policy-binding "${PROJECT}" \
      --member="serviceAccount:${NODE_GSA_EMAIL}" \
      --role="roles/container.defaultNodeServiceAccount" \
      --condition=None \
      --quiet

    local cluster_exists="false"
    if [[ "${ADOPT_CLUSTER}" == "true" ]]; then
      cluster_exists="true"
    elif [[ "${DRY_RUN}" != "true" ]] && gcloud container clusters describe "${CLUSTER}" --project="${PROJECT}" --region="${REGION}" >/dev/null 2>&1; then
      cluster_exists="true"
    fi

    if [[ "${cluster_exists}" == "true" ]]; then
      log "  Adopting existing GKE cluster '${CLUSTER}' and ensuring Workload Identity, Gateway API, and Secret Manager CSI..."
      run_cmd gcloud container clusters update "${CLUSTER}" \
        --project="${PROJECT}" \
        --region="${REGION}" \
        --workload-pool="${PROJECT}.svc.id.goog" \
        --gateway-api=standard \
        --enable-secret-manager
    else
      log "  Creating GKE cluster '${CLUSTER}' with Dataplane V2, Workload Identity, Gateway API, and Secret Manager CSI..."
      run_cmd gcloud container clusters create "${CLUSTER}" \
        --project="${PROJECT}" \
        --region="${REGION}" \
        --node-locations="${ZONE}" \
        --machine-type="${DAEMON_MACHINE_TYPE}" \
        --num-nodes=1 \
        --disk-type="pd-balanced" \
        --disk-size="100" \
        --service-account="${NODE_GSA_EMAIL}" \
        --enable-dataplane-v2 \
        --enable-ip-alias \
        --workload-pool="${PROJECT}.svc.id.goog" \
        --workload-metadata=GKE_METADATA \
        --gateway-api=standard \
        --enable-secret-manager \
        --node-labels="grain.dev/pool=daemon" \
        --enable-shielded-nodes \
        --shielded-secure-boot \
        --shielded-integrity-monitoring
    fi

    log "  Ensuring nested-virtualization granule sandbox node pool ('grain-sandbox-pool')..."
    if [[ "${DRY_RUN}" == "true" ]] || ! gcloud container node-pools describe grain-sandbox-pool --cluster="${CLUSTER}" --project="${PROJECT}" --region="${REGION}" >/dev/null 2>&1; then
      run_cmd gcloud container node-pools create grain-sandbox-pool \
        --cluster="${CLUSTER}" \
        --project="${PROJECT}" \
        --region="${REGION}" \
        --node-locations="${ZONE}" \
        --machine-type="${SANDBOX_MACHINE_TYPE}" \
        --image-type="UBUNTU_CONTAINERD" \
        --disk-type="pd-balanced" \
        --disk-size="200" \
        --service-account="${NODE_GSA_EMAIL}" \
        --enable-nested-virtualization \
        --workload-metadata=GKE_METADATA \
        --enable-autoscaling \
        --min-nodes="${SANDBOX_MIN_NODES}" \
        --max-nodes="${SANDBOX_MAX_NODES}" \
        --node-labels="grain.dev/pool=sandboxes,grain.dev/nested-virt=true" \
        --shielded-secure-boot \
        --shielded-integrity-monitoring
    fi
  fi

  run_cmd gcloud container clusters get-credentials "${CLUSTER}" \
    --project="${PROJECT}" \
    --region="${REGION}"

  log "  Applying namespace '${NAMESPACE}' (pod-security.kubernetes.io/enforce=privileged), PriorityClasses, and ResourceQuota..."
  run_cmd kubectl apply -f "${REPO_ROOT}/deploy/kubernetes/gke/namespace.yaml"
  run_cmd kubectl label namespace "${NAMESPACE}" \
    pod-security.kubernetes.io/enforce=privileged \
    app.kubernetes.io/name=grain \
    --overwrite
}

# Step 2: Provision dependencies (Cloud SQL or in-cluster Postgres, PVCs,
# Secret Manager secret containers, and least-privilege Workload Identity IAM).
stage_dependencies() {
  log "Stage 2: Provisioning dependencies (postgres=${POSTGRES_MODE}, Secret Manager, Workload Identity, PVCs)..."

  run_cmd gcloud iam service-accounts create "${GSA_NAME}" \
    --project="${PROJECT}" \
    --display-name="Grain daemon Workload Identity service account" || true

  # Bind Kubernetes ServiceAccount grain/grain -> grain-daemon GSA.
  # Note: ServiceAccount grain-sandbox intentionally receives NO Workload Identity binding.
  run_cmd gcloud iam service-accounts add-iam-policy-binding "${GSA_EMAIL}" \
    --project="${PROJECT}" \
    --role="roles/iam.workloadIdentityUser" \
    --member="serviceAccount:${PROJECT}.svc.id.goog[${NAMESPACE}/grain]" \
    --quiet

  # Ensure staging/backups GCS bucket and objectAdmin grant on that bucket only.
  if [[ "${DRY_RUN}" == "true" ]] || ! gcloud storage buckets describe "gs://${STAGING_BUCKET}" --project="${PROJECT}" >/dev/null 2>&1; then
    run_cmd gcloud storage buckets create "gs://${STAGING_BUCKET}" \
      --project="${PROJECT}" \
      --location="${REGION}" \
      --uniform-bucket-level-access \
      --public-access-prevention
  fi
  run_cmd gcloud storage buckets add-iam-policy-binding "gs://${STAGING_BUCKET}" \
    --member="serviceAccount:${GSA_EMAIL}" \
    --role="roles/storage.objectAdmin" \
    --quiet

  # Create Secret Manager secret containers and grant secretAccessor per secret
  # (never project-wide). No secret material is logged or passed on the command line.
  local secrets=(
    "grain-secrets-key"
    "grain-sandbox-ca-key"
    "grain-state-repo-token"
    "grain-secrets-repo-token"
    "grain-github-app-private-key"
    "grain-github-webhook-secret"
    "grain-github-webhook-relay-secret"
    "grain-hostd-token"
  )
  local sec
  for sec in "${secrets[@]}"; do
    if [[ "${DRY_RUN}" == "true" ]] || ! gcloud secrets describe "${sec}" --project="${PROJECT}" >/dev/null 2>&1; then
      run_cmd gcloud secrets create "${sec}" \
        --project="${PROJECT}" \
        --replication-policy="automatic"
    fi
    run_cmd gcloud secrets add-iam-policy-binding "${sec}" \
      --project="${PROJECT}" \
      --member="serviceAccount:${GSA_EMAIL}" \
      --role="roles/secretmanager.secretAccessor" \
      --quiet
  done

  if [[ "${POSTGRES_MODE}" == "cloudsql" ]]; then
    log "  Ensuring Cloud SQL for PostgreSQL 16 instance '${CLOUDSQL_INSTANCE}' (${CLOUDSQL_TIER})..."
    if [[ "${DRY_RUN}" == "true" ]] || ! gcloud sql instances describe "${CLOUDSQL_INSTANCE}" --project="${PROJECT}" >/dev/null 2>&1; then
      run_cmd gcloud sql instances create "${CLOUDSQL_INSTANCE}" \
        --project="${PROJECT}" \
        --region="${REGION}" \
        --database-version=POSTGRES_16 \
        --tier="${CLOUDSQL_TIER}" \
        --availability-type=REGIONAL \
        --storage-type=SSD \
        --storage-size=50GB \
        --storage-auto-increase \
        --database-flags=cloudsql.iam_authentication=on \
        --no-assign-ip \
        --network="projects/${PROJECT}/global/networks/default" \
        --backup-start-time=04:00 \
        --enable-point-in-time-recovery \
        --deletion-protection
    fi

    run_cmd gcloud sql databases create grain \
      --instance="${CLOUDSQL_INSTANCE}" \
      --project="${PROJECT}" || true

    run_cmd gcloud sql users create "${GSA_NAME}@${PROJECT}.iam" \
      --instance="${CLOUDSQL_INSTANCE}" \
      --project="${PROJECT}" \
      --type=CLOUD_IAM_SERVICE_ACCOUNT || true

    run_cmd gcloud projects add-iam-policy-binding "${PROJECT}" \
      --member="serviceAccount:${GSA_EMAIL}" \
      --role="roles/cloudsql.client" \
      --condition=None \
      --quiet

    run_cmd gcloud projects add-iam-policy-binding "${PROJECT}" \
      --member="serviceAccount:${GSA_EMAIL}" \
      --role="roles/cloudsql.instanceUser" \
      --condition=None \
      --quiet
  else
    log "  Deploying in-cluster PostgreSQL 16 StatefulSet ('grain-postgres')..."
    if [[ "${DRY_RUN}" == "true" ]]; then
      printf 'DRY-RUN: kubectl -n %q create secret generic grain-postgres-credentials (password + dsn redacted)\n' "${NAMESPACE}"
    elif ! kubectl -n "${NAMESPACE}" get secret grain-postgres-credentials >/dev/null 2>&1; then
      local pg_pass
      pg_pass="$(openssl rand -hex 32)"
      kubectl -n "${NAMESPACE}" create secret generic grain-postgres-credentials \
        --from-literal=password="${pg_pass}" \
        --from-literal=dsn="postgres://grain:${pg_pass}@grain-postgres.${NAMESPACE}.svc.cluster.local:5432/grain?sslmode=disable" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    fi
    run_cmd kubectl -n "${NAMESPACE}" apply -f "${REPO_ROOT}/deploy/kubernetes/gke/postgres-incluster.yaml"
  fi
}

# Step 3: Render and apply the GKE Kustomize overlay (Deployment/grain,
# Deployment/grain-webhook, Service/grain-sandbox, Service/grain-access:7443,
# Service/grain-hostd, Service/grain-webhook, ServiceAccount/grain-sandbox,
# SecretProviderClass/grain-secrets, and NetworkPolicies) with digest-pinned images.
stage_deploy_grain() {
  log "Stage 3: Deploying Grain manifests and in-cluster Services (pinned to ${PINNED_IMAGE})..."

  local rendered
  rendered="$(mktemp "${TMPDIR:-/tmp}/grain-gke-manifests.XXXXXX.yaml")"

  kubectl kustomize "${REPO_ROOT}/deploy/kubernetes/gke" \
    | sed \
      -e "s|PROJECT_ID:REGION:CLOUDSQL_INSTANCE|${CLOUDSQL_CONN_NAME}|g" \
      -e "s|PROJECT_ID|${PROJECT}|g" \
      -e "s|DNS_NAME|${DNS_NAME}|g" \
      -e "s|namespace: grain|namespace: ${NAMESPACE}|g" \
      -e "s|-k8s-namespace=grain|-k8s-namespace=${NAMESPACE}|g" \
      -e "s|\.grain\.svc\.cluster\.local|.${NAMESPACE}.svc.cluster.local|g" \
      -e "s|ghcr.io/bwsalmon/grain/grain@sha256:0000000000000000000000000000000000000000000000000000000000000000|${PINNED_IMAGE}|g" \
      -e "s|ghcr.io/bwsalmon/grain/granule@sha256:0000000000000000000000000000000000000000000000000000000000000000|${PINNED_SANDBOX_IMAGE}|g" \
      -e "s|-default-target-repo=ai-on-gke/grain|-default-target-repo=${TARGET_REPO}|g" \
      -e "s|value: dialout|value: ${ACCESS_MODE}|g" \
      >"${rendered}"

  if [[ "${POSTGRES_MODE}" == "in-cluster" ]]; then
    sed -i \
      -e "s|value: host=/cloudsql/${CLOUDSQL_CONN_NAME} user=${GSA_NAME}@${PROJECT}.iam dbname=grain sslmode=disable|valueFrom:\\n          secretKeyRef:\\n            name: grain-postgres-credentials\\n            key: dsn|g" \
      "${rendered}"
  fi

  # Safety fence before cutover: if the local cloudtop grain-daemon is currently
  # active, stage Deployment/grain at replicas: 0 until `--cutover` drains and
  # stops the cloudtop daemon so two daemons never write to the same state.
  if cloudtop_daemon_is_active; then
    log "  Local grain-daemon.service is active; staging GKE Deployment/grain at replicas=0 until --cutover."
    sed -i 's/replicas: 1/replicas: 0/' "${rendered}"
  fi

  run_cmd kubectl apply -f "${rendered}"
  rm -f "${rendered}"
}

# Step 4: Set up Gateway, global IP, DNS record, managed TLS certificate,
# IAP OAuth client Secret, GCPBackendPolicy, and IAP IAM access policies.
stage_iap() {
  log "Stage 4: Configuring GKE Gateway, TLS certificate (${DNS_NAME}), and IAP policies..."

  if [[ "${DRY_RUN}" == "true" ]] || ! gcloud compute addresses describe grain-gateway-ip --global --project="${PROJECT}" >/dev/null 2>&1; then
    run_cmd gcloud compute addresses create grain-gateway-ip \
      --global \
      --project="${PROJECT}"
  fi

  if [[ "${DRY_RUN}" == "true" ]] || ! gcloud compute ssl-certificates describe grain-gateway-cert --global --project="${PROJECT}" >/dev/null 2>&1; then
    run_cmd gcloud compute ssl-certificates create grain-gateway-cert \
      --domains="${DNS_NAME}" \
      --global \
      --project="${PROJECT}"
  fi

  if [[ -n "${DNS_ZONE}" ]]; then
    local gateway_ip="203.0.113.10"
    if [[ "${DRY_RUN}" != "true" ]]; then
      gateway_ip="$(gcloud compute addresses describe grain-gateway-ip --global --project="${PROJECT}" --format='value(address)')"
    fi
    log "  Ensuring Cloud DNS A record ${DNS_NAME}. -> ${gateway_ip} in zone '${DNS_ZONE}'..."
    run_cmd gcloud dns record-sets transaction start --zone="${DNS_ZONE}" --project="${PROJECT}" || true
    run_cmd gcloud dns record-sets transaction add "${gateway_ip}" \
      --name="${DNS_NAME}." \
      --ttl=300 \
      --type=A \
      --zone="${DNS_ZONE}" \
      --project="${PROJECT}" || true
    run_cmd gcloud dns record-sets transaction execute --zone="${DNS_ZONE}" --project="${PROJECT}" || true
  fi

  if [[ -n "${IAP_CLIENT_SECRET_FILE}" ]]; then
    [[ -f "${IAP_CLIENT_SECRET_FILE}" ]] || die "IAP client secret file not found: ${IAP_CLIENT_SECRET_FILE}"
    log "  Updating Kubernetes Secret 'grain-iap-oauth' from ${IAP_CLIENT_SECRET_FILE} (value not logged)..."
    if [[ "${DRY_RUN}" == "true" ]]; then
      printf 'DRY-RUN: kubectl -n %q create secret generic grain-iap-oauth --from-file=key=%q --dry-run=client -o yaml | kubectl apply -f -\n' \
        "${NAMESPACE}" "${IAP_CLIENT_SECRET_FILE}"
    else
      kubectl -n "${NAMESPACE}" create secret generic grain-iap-oauth \
        --from-file=key="${IAP_CLIENT_SECRET_FILE}" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    fi
  fi

  # Resolve the Gateway backend services for Service/grain and Service/grain-hostd,
  # populate GRAIN_IAP_AUDIENCE on Deployment/grain for in-daemon JWT defense-in-depth,
  # and bind roles/iap.httpsResourceAccessor ONLY on those two IAP backend services
  # (never project-wide).
  local project_number="000000000000"
  local -a iap_services=("gkegw1-grain-${NAMESPACE}-grain-80" "gkegw1-grain-${NAMESPACE}-grain-hostd-80")
  local iap_audiences="/projects/${project_number}/global/backendServices/gkegw1-grain-${NAMESPACE}-grain-80,/projects/${project_number}/global/backendServices/gkegw1-grain-${NAMESPACE}-grain-hostd-80"
  if [[ "${DRY_RUN}" != "true" ]]; then
    project_number="$(gcloud projects describe "${PROJECT}" --format='value(projectNumber)')"
    mapfile -t resolved_services < <(
      gcloud compute backend-services list \
        --project="${PROJECT}" \
        --global \
        --filter="name~'${NAMESPACE}-grain(-hostd)?-80'" \
        --format="value(name)"
    )
    if [[ "${#resolved_services[@]}" -gt 0 ]]; then
      iap_services=("${resolved_services[@]}")
    fi
    local -a aud_list=()
    local bs_name bs_id
    for bs_name in "${iap_services[@]}"; do
      bs_id="$(gcloud compute backend-services describe "${bs_name}" --global --project="${PROJECT}" --format='value(id)' 2>/dev/null || true)"
      if [[ -n "${bs_id}" ]]; then
        aud_list+=("/projects/${project_number}/global/backendServices/${bs_id}")
      fi
    done
    if [[ "${#aud_list[@]}" -gt 0 ]]; then
      local IFS=','
      iap_audiences="${aud_list[*]}"
    fi
  fi

  log "  Setting GRAIN_IAP_AUDIENCE=${iap_audiences} on Deployment/grain..."
  run_cmd kubectl -n "${NAMESPACE}" set env deployment/grain "GRAIN_IAP_AUDIENCE=${iap_audiences}"

  local principal bs_svc
  for principal in "${IAP_PRINCIPALS[@]}"; do
    for bs_svc in "${iap_services[@]}"; do
      log "  Granting roles/iap.httpsResourceAccessor to ${principal} on backend service ${bs_svc}..."
      run_cmd gcloud iap web add-iam-policy-binding \
        --resource-type=backend-services \
        --service="${bs_svc}" \
        --project="${PROJECT}" \
        --member="${principal}" \
        --role="roles/iap.httpsResourceAccessor" \
        --condition=None \
        --quiet
    done
  done
}

# Step 5: Connect `grain hostd` on the existing cloudtop host.
# Leaves `/usr/local/bin/grain hostd`, `/etc/grain/hostd.yaml` and its allowlist
# untouched, writes the minted 0600 hostd token (owned by HOSTD_OWNER), uploads
# it to Secret Manager (`grain-hostd-token`) and Kubernetes (`Secret/grain-hostd-token`),
# writes a systemd drop-in passing `-server` and `-token-file`, and restarts ONLY
# `grain-hostd.service`.
stage_connect_hostd() {
  log "Stage 5: Connecting cloudtop 'grain hostd' (${HOSTD_NAME}, owner=${HOSTD_OWNER}) to ${GATEWAY_URL}/api/hostd/connect..."

  local hostd_yaml="/etc/grain/hostd.yaml"
  local hostd_token_file="/etc/grain/hostd.token"
  local hostd_remote_env="/etc/grain/hostd-remote.env"
  local hostd_dropin_dir="/etc/systemd/system/grain-hostd.service.d"
  local hostd_dropin_file="${hostd_dropin_dir}/remote.conf"

  if [[ -f "${hostd_yaml}" ]]; then
    log "  Preserving existing ${hostd_yaml} and its allowlist in place."
  else
    log "  Note: ${hostd_yaml} not present on this machine; leaving allowlist untouched."
  fi

  log "  Minting scoped hostd credential for '${HOSTD_NAME}' (owner=${HOSTD_OWNER}) and writing ${hostd_token_file} (mode 0600, owner ${HOSTD_OWNER})..."
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf 'DRY-RUN: install -o %q -m 0600 /dev/null %q (populated and uploaded to Secret Manager grain-hostd-token)\n' \
      "${HOSTD_OWNER}" "${hostd_token_file}"
    printf 'DRY-RUN: gcloud secrets versions add grain-hostd-token --project=%q --data-file=%q\n' \
      "${PROJECT}" "${hostd_token_file}"
    printf 'DRY-RUN: write %q (GRAIN_HOSTD_URL=%q GRAIN_HOSTD_NAME=%q GRAIN_HOSTD_OWNER=%q GRAIN_HOSTD_TOKEN_FILE=%q)\n' \
      "${hostd_remote_env}" "${GATEWAY_URL}/api/hostd/connect" "${HOSTD_NAME}" "${HOSTD_OWNER}" "${hostd_token_file}"
    printf 'DRY-RUN: write %q (-server %q -token-file %q -executor-name %q)\n' \
      "${hostd_dropin_file}" "${GATEWAY_URL}" "${hostd_token_file}" "${HOSTD_NAME}"
  else
    sudo mkdir -p /etc/grain "${hostd_dropin_dir}"
    if [[ ! -s "${hostd_token_file}" ]]; then
      sudo install -m 0600 /dev/null "${hostd_token_file}"
      openssl rand -hex 32 | sudo tee "${hostd_token_file}" >/dev/null
    fi
    sudo chown "${HOSTD_OWNER}" "${hostd_token_file}"
    sudo chmod 0600 "${hostd_token_file}"

    sudo gcloud secrets versions add grain-hostd-token \
      --project="${PROJECT}" \
      --data-file="${hostd_token_file}" >/dev/null
    sudo kubectl -n "${NAMESPACE}" create secret generic grain-hostd-token \
      --from-file=token="${hostd_token_file}" \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null

    sudo tee "${hostd_remote_env}" >/dev/null <<EOF
# Managed by scripts/deploy-gke.sh — outbound IAP/TLS connection for grain hostd.
GRAIN_HOSTD_URL=${GATEWAY_URL}/api/hostd/connect
GRAIN_HOSTD_NAME=${HOSTD_NAME}
GRAIN_HOSTD_OWNER=${HOSTD_OWNER}
GRAIN_HOSTD_TOKEN_FILE=${hostd_token_file}
GRAIN_HOSTD_IAP_AUDIENCE=${IAP_CLIENT_ID}
EOF
    sudo chown "${HOSTD_OWNER}" "${hostd_remote_env}"
    sudo chmod 0600 "${hostd_remote_env}"

    sudo tee "${hostd_dropin_file}" >/dev/null <<EOF
# Managed by scripts/deploy-gke.sh — connects grain-hostd to GKE Gateway over IAP/TLS.
[Service]
EnvironmentFile=${hostd_remote_env}
ExecStart=
ExecStart=/usr/local/bin/grain hostd -config ${hostd_yaml} -server ${GATEWAY_URL} -token-file ${hostd_token_file} -executor-name ${HOSTD_NAME}
EOF
    sudo chmod 0644 "${hostd_dropin_file}"
  fi

  log "  Restarting grain-hostd.service only (leaving grain-daemon.service untouched)..."
  run_cmd sudo systemctl daemon-reload
  run_cmd sudo systemctl restart grain-hostd.service
}

cloudtop_daemon_is_active() {
  if [[ "${GRAIN_GKE_ASSUME_CLOUDTOP_ACTIVE:-}" == "true" ]]; then
    return 0
  fi
  if [[ "${GRAIN_GKE_ASSUME_CLOUDTOP_ACTIVE:-}" == "false" ]]; then
    return 1
  fi
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet grain-daemon.service 2>/dev/null; then
    return 0
  fi
  return 1
}

gke_daemon_replicas() {
  if [[ -n "${GRAIN_GKE_ASSUME_GKE_REPLICAS:-}" ]]; then
    printf '%s\n' "${GRAIN_GKE_ASSUME_GKE_REPLICAS}"
    return 0
  fi
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '0\n'
    return 0
  fi
  kubectl -n "${NAMESPACE}" get deployment grain -o jsonpath='{.spec.replicas}' 2>/dev/null || printf '0\n'
}

# Step 6a: Migrate state (--migrate) per docs/design/grain-on-gke.md ("Migration").
# 1. Upgrade SQLite store to Cloud SQL Postgres via POST /api/database/upgrade.
# 2. Push secrets.key and bootstrap credentials to Secret Manager (values never logged).
# 3. Stage last 30 days of transcripts/ and artifacts/ (excluding repos/, home/,
#    and cache/jobs/) to GCS and pre-seed the grain-data PVC before Deployment/grain starts.
stage_migrate() {
  log "Stage 6 (--migrate): Migrating cloudtop state from ${DATA_DIR} to GKE (${CLUSTER})..."

  # 1. Database migration (SQLite -> Cloud SQL Postgres).
  local dsn="host=/cloudsql/${CLOUDSQL_CONN_NAME} user=${GSA_NAME}@${PROJECT}.iam dbname=grain sslmode=disable"
  if [[ "${POSTGRES_MODE}" == "in-cluster" ]]; then
    dsn="postgres://grain@grain-postgres.${NAMESPACE}.svc.cluster.local:5432/grain?sslmode=disable"
  fi
  log "  Step 1/3: Upgrading database store to PostgreSQL via POST /api/database/upgrade..."
  run_cmd curl -fsS -X POST "http://127.0.0.1:4242/api/database/upgrade" \
    -H "Content-Type: application/json" \
    -d "{\"dsn\": \"${dsn}\", \"migrateData\": true}"

  # 2. Push encryption key and bootstrap secrets to Secret Manager without logging values.
  log "  Step 2/3: Uploading secrets.key and bootstrap credentials to Secret Manager (values redacted)..."
  local key_file="${DATA_DIR}/secrets/secrets.key"
  if [[ ! -f "${key_file}" && -f "${DATA_DIR}/secrets.key" ]]; then
    key_file="${DATA_DIR}/secrets.key"
  fi
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf 'DRY-RUN: gcloud secrets versions add grain-secrets-key --project=%q --data-file=%q\n' \
      "${PROJECT}" "${key_file}"
  elif [[ -f "${key_file}" ]]; then
    gcloud secrets versions add grain-secrets-key \
      --project="${PROJECT}" \
      --data-file="${key_file}" >/dev/null
  else
    warn "${key_file} not found locally; skipping grain-secrets-key upload."
  fi

  # 3. Pre-seed PVC with last 30 days of transcripts/ and artifacts/ via GCS.
  # Per design doc: repos/, home/, and cache/jobs/ are NOT copied.
  log "  Step 3/3: Staging last 30 days of transcripts/ and artifacts/ from ${DATA_DIR} to gs://${STAGING_BUCKET}/migration/..."
  log "  (Excluding repos/, home/, and cache/jobs/ per docs/design/grain-on-gke.md)"
  if [[ "${DRY_RUN}" == "true" ]]; then
    local dry_archive="/tmp/grain-pvc-seed.tar.zst"
    printf 'DRY-RUN: find %q/transcripts %q/artifacts -mtime -30 | tar --zstd -cf %q -T -\n' \
      "${DATA_DIR}" "${DATA_DIR}" "${dry_archive}"
    printf 'DRY-RUN: gcloud storage cp %q %q\n' \
      "${dry_archive}" "gs://${STAGING_BUCKET}/migration/grain-pvc-seed.tar.zst"
    printf 'DRY-RUN: kubectl -n %q scale deployment/grain --replicas=0\n' "${NAMESPACE}"
    printf 'DRY-RUN: kubectl -n %q apply -f <grain-data-seed Job (image=%s)>\n' "${NAMESPACE}" "${PINNED_IMAGE}"
    printf 'DRY-RUN: kubectl -n %q wait --for=condition=complete --timeout=600s job/grain-data-seed\n' "${NAMESPACE}"
  else
    local filelist archive
    filelist="$(mktemp "${TMPDIR:-/tmp}/grain-pvc-files.XXXXXX")"
    archive="$(mktemp "${TMPDIR:-/tmp}/grain-pvc-seed.XXXXXX.tar.zst")"
    chmod 0600 "${filelist}" "${archive}"
    (
      cd "${DATA_DIR}"
      for subdir in transcripts artifacts; do
        if [[ -d "${subdir}" ]]; then
          find "${subdir}" -type f -mtime -30
        fi
      done
    ) >"${filelist}"
    tar --zstd -C "${DATA_DIR}" -cf "${archive}" -T "${filelist}"
    rm -f "${filelist}"
    gcloud storage cp "${archive}" "gs://${STAGING_BUCKET}/migration/grain-pvc-seed.tar.zst"
    rm -f "${archive}"

    # Ensure Deployment/grain is at replicas=0 while the ReadWriteOnce PVC is mounted by the seed Job.
    kubectl -n "${NAMESPACE}" scale deployment/grain --replicas=0
    kubectl -n "${NAMESPACE}" delete job grain-data-seed --ignore-not-found
    kubectl -n "${NAMESPACE}" apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: grain-data-seed
  namespace: ${NAMESPACE}
spec:
  ttlSecondsAfterFinished: 300
  template:
    spec:
      serviceAccountName: grain
      automountServiceAccountToken: false
      restartPolicy: Never
      nodeSelector:
        grain.dev/pool: daemon
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: seed
          image: ${PINNED_IMAGE}
          command:
            - /bin/sh
            - -c
            - |
              set -eu
              token="\$(curl -fsS -H 'Metadata-Flavor: Google' 'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token' | sed -n 's/.*\"access_token\":\"\\([^\"]*\\)\".*/\\1/p')"
              curl -fsS -H "Authorization: Bearer \${token}" \
                "https://storage.googleapis.com/storage/v1/b/${STAGING_BUCKET}/o/migration%2Fgrain-pvc-seed.tar.zst?alt=media" \
                -o /tmp/seed.tar.zst
              tar --zstd -xf /tmp/seed.tar.zst -C /data
              rm -f /tmp/seed.tar.zst
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: grain-data
EOF
    kubectl -n "${NAMESPACE}" wait --for=condition=complete --timeout=600s job/grain-data-seed
  fi
}

# Step 6b: Explicit cutover (--cutover).
# Refuses to proceed if two daemons are simultaneously active against the same
# state, drains and stops the cloudtop grain-daemon, and brings up the GKE
# Deployment/grain at replicas=1.
stage_cutover() {
  log "Stage 6 (--cutover): Cutting over active grain daemon from cloudtop to GKE (${CLUSTER})..."

  local gke_reps
  gke_reps="$(gke_daemon_replicas)"
  if cloudtop_daemon_is_active && [[ "${gke_reps}" -gt 0 ]]; then
    die "refusing cutover: both cloudtop grain-daemon.service and GKE Deployment/grain (replicas=${gke_reps}) are active against the same state. Scale GKE Deployment/grain to 0 first."
  fi

  log "  Step 1/4: Pausing dispatch on cloudtop daemon, draining in-flight runs, and triggering final backup..."
  run_cmd curl -fsS -X POST "http://127.0.0.1:4242/api/dispatch/pause"
  run_cmd curl -fsS -X POST "http://127.0.0.1:4242/api/backups/run"

  log "  Step 2/4: Stopping and disabling cloudtop grain-daemon.service (keeping grain-hostd.service active)..."
  run_cmd sudo systemctl stop grain-daemon.service
  run_cmd sudo systemctl disable grain-daemon.service

  log "  Step 3/4: Scaling GKE Deployment/grain in namespace '${NAMESPACE}' to replicas=1 and waiting for rollout..."
  run_cmd kubectl -n "${NAMESPACE}" scale deployment/grain --replicas=1
  run_cmd kubectl -n "${NAMESPACE}" rollout status deployment/grain --timeout=300s

  log "  Step 4/4: Ensuring capability placement targets hostd:${HOSTD_NAME} for gcp/gdrive/local-scripts and in-cluster for self-debug..."
  run_cmd systemctl is-active grain-hostd.service
}

# Step 6c: Explicit rollback (--rollback) per docs/design/grain-on-gke.md ("Rollback").
# Scales GKE Deployment/grain to 0 (releasing the Postgres advisory lock fence),
# restores local hostd socket config, and starts cloudtop grain-daemon.service.
stage_rollback() {
  log "Stage 6 (--rollback): Rolling back active grain daemon from GKE (${CLUSTER}) to cloudtop..."

  log "  Step 1/3: Scaling GKE Deployment/grain to replicas=0 and waiting for pod termination (releasing Postgres advisory lock)..."
  run_cmd kubectl -n "${NAMESPACE}" scale deployment/grain --replicas=0
  run_cmd kubectl -n "${NAMESPACE}" wait --for=delete pod -l app.kubernetes.io/component=daemon --timeout=120s

  local gke_reps
  gke_reps="$(gke_daemon_replicas)"
  if [[ "${gke_reps}" -gt 0 ]]; then
    die "refusing rollback: GKE Deployment/grain still reports replicas=${gke_reps}; two daemons must never run against the same state."
  fi

  log "  Step 2/3: Restoring cloudtop grain-hostd to local socket mode and restarting grain-hostd.service..."
  run_cmd sudo rm -f /etc/grain/hostd-remote.env /etc/systemd/system/grain-hostd.service.d/remote.conf
  run_cmd sudo systemctl daemon-reload
  run_cmd sudo systemctl restart grain-hostd.service

  log "  Step 3/3: Re-enabling and starting cloudtop grain-daemon.service against Cloud SQL..."
  run_cmd sudo systemctl enable --now grain-daemon.service
}

# Step 7: Verification (--check).
# Confirms:
# 1. The UI answers through IAP (and refuses unauthenticated requests).
# 2. hostd is connected and healthy.
# 3. A test sandbox runs a command over the granule dial-out channel (grain-access:7443).
# 4. The Kubernetes API server audit log shows zero exec/attach/portforward calls for that run.
stage_check() {
  log "Stage 7 (--check): Verifying GKE deployment at ${GATEWAY_URL}..."

  # 1. Verify UI is protected by IAP (unauthenticated requests must be rejected with 302/401/403, never 200)
  #    and answers when an IAP identity token is presented.
  log "  Check 1/4: Verifying UI is gated by IAP at ${GATEWAY_URL}/api/auth/access-code..."
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf 'DRY-RUN: curl -sS -o /dev/null -w "%%{http_code}" %q (must be 302, 401, or 403; never 200)\n' \
      "${GATEWAY_URL}/api/auth/access-code"
    printf 'DRY-RUN: curl -fsS -H "Authorization: Bearer <iap-id-token>" %q\n' \
      "${GATEWAY_URL}/api/auth/access-code"
  else
    local unauth_code
    unauth_code="$(curl -sS -o /dev/null -w '%{http_code}' "${GATEWAY_URL}/api/auth/access-code")"
    if [[ ! "${unauth_code}" =~ ^(302|401|403)$ ]]; then
      die "IAP check failed: unauthenticated request to ${GATEWAY_URL}/api/auth/access-code returned HTTP ${unauth_code} (expected 302, 401, or 403)"
    fi
    local iap_token=""
    if [[ -n "${IAP_CLIENT_ID}" ]]; then
      iap_token="$(gcloud auth print-identity-token --audiences="${IAP_CLIENT_ID}" 2>/dev/null || true)"
    fi
    if [[ -n "${iap_token}" ]]; then
      curl -fsS -H "Authorization: Bearer ${iap_token}" "${GATEWAY_URL}/api/auth/access-code" >/dev/null
    fi
  fi

  # 2. Verify hostd is connected.
  log "  Check 2/4: Confirming hostd '${HOSTD_NAME}' is connected to GKE daemon..."
  run_cmd kubectl -n "${NAMESPACE}" exec deploy/grain -c grain -- \
    wget -qO- "http://127.0.0.1:8080/api/executors"

  # 3. Verify a test sandbox runs a command over the granule dial-out channel.
  local probe_id
  probe_id="gke-check-$(date +%s)"
  local start_ts
  start_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  log "  Check 3/4: Running test sandbox command (${probe_id}) over granule dial-out channel (Service/grain-access:7443)..."
  run_cmd kubectl -n "${NAMESPACE}" exec deploy/grain -c grain -- \
    /usr/local/bin/grain sandbox probe --id="${probe_id}" --access=dialout -- echo "dialout-ok:${probe_id}"

  # 4. Verify the Kubernetes API server audit log shows no exec/attach/portforward for that run.
  log "  Check 4/4: Querying GKE API server Cloud Audit Logs for exec/attach/portforward on '${probe_id}' since ${start_ts}..."
  local audit_filter="resource.type=\"k8s_cluster\" AND resource.labels.cluster_name=\"${CLUSTER}\" AND timestamp>=\"${start_ts}\" AND protoPayload.resourceName:\"${probe_id}\" AND protoPayload.methodName=~\"io.k8s.core.v1.pods.(exec|attach|portforward).create\""
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf 'DRY-RUN: gcloud logging read %q --project=%q --format=json\n' \
      "${audit_filter}" "${PROJECT}"
  else
    local matches
    matches="$(gcloud logging read "${audit_filter}" --project="${PROJECT}" --format='value(insertId)' 2>/dev/null || true)"
    if [[ -n "${matches}" ]]; then
      die "API server audit log recorded forbidden exec/attach/portforward call(s) during sandbox probe ${probe_id}: ${matches}"
    fi
  fi
  log "  All 4 verification checks passed (IAP UI, hostd connected, dial-out sandbox exec, zero API server exec/attach/portforward)."
}

main() {
  if [[ "${DO_ROLLBACK}" == "true" ]]; then
    stage_rollback
    exit 0
  fi

  if [[ "${DO_MIGRATE}" == "true" ]]; then
    stage_migrate
    exit 0
  fi

  if [[ "${DO_CUTOVER}" == "true" ]]; then
    stage_cutover
    exit 0
  fi

  if [[ "${DO_CHECK}" == "true" ]]; then
    stage_check
    exit 0
  fi

  stage_cluster
  stage_dependencies
  stage_deploy_grain
  stage_iap
  stage_connect_hostd

  log "GKE deployment staged successfully. Next steps:"
  log "  1. Migrate data:  scripts/deploy-gke.sh --project=${PROJECT} --dns-name=${DNS_NAME} --migrate"
  log "  2. Cut over:      scripts/deploy-gke.sh --project=${PROJECT} --dns-name=${DNS_NAME} --cutover"
  log "  3. Verify:        scripts/deploy-gke.sh --project=${PROJECT} --dns-name=${DNS_NAME} --check"
}

main
