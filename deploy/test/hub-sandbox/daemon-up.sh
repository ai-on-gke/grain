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

# Provision a dedicated, disposable test `grain daemon` GCE VM inside the GCP
# sandbox project and connect it to the sandbox `grain-registry` instance over
# an outbound WebSocket mux tunnel (`-hub-connect`).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

PROJECT_ID="${PROJECT_ID:-}"
OWNER_EMAIL="${OWNER_EMAIL:-}"
ENTRY_NAME="${ENTRY_NAME:-sandbox-daemon}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
REGION="${REGION:-us-central1}"
ZONE="${ZONE:-us-central1-a}"
MACHINE_TYPE="${MACHINE_TYPE:-n2-standard-2}"
BOOT_DISK_GB="${BOOT_DISK_GB:-20}"
AR_LOCATION="${AR_LOCATION:-us}"
AR_REPO="${AR_REPO:-grain}"
IMAGE_TAG="${IMAGE_TAG:-}"
DAEMON_IMAGE="${DAEMON_IMAGE:-}"
SKIP_IMAGE_BUILD=0
STATE_BUCKET="${STATE_BUCKET:-}"
LOCAL_STATE=0
ALLOW_VERIFIER_SA=1
SKIP_VERIFY=0
DISPLAY_NAME=""
PURPOSE="Disposable sandbox test grain daemon connected over outbound WebSocket mux tunnel"

ALLOW_PRINCIPALS=()
TARGET_REPOS=()

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/hub-sandbox/daemon-up.sh \
    --project <gcp-project-id> \
    --owner <owner@example.com> \
    [options]

Options:
  --project <id>             GCP sandbox project ID (required)
  --owner <email>            Owner email for the connected entry (required)
  --entry <name>             Connected entry name in the registry (default: sandbox-daemon)
  --allow-principal <p>      Additional principal admitted to the entry and daemon, e.g. user:peer@example.com (repeatable)
  --repo <owner/name>        Repository advertised by the test daemon for /api/route (repeatable; default: ai-on-gke/grain)
  --display-name <text>      Display name in the registry catalog (default: Sandbox Test Daemon (<entry>))
  --purpose <text>           Purpose description in the registry catalog
  --machine-type <type>      GCE machine type for the test daemon VM (default: n2-standard-2; e2-* is refused, no nested virtualization)
  --boot-disk-gb <gb>        Boot disk size in GiB, 10-200 (default: 20)
  --prefix <prefix>          Sandbox registry resource prefix (default: grain-reg)
  --region <region>          GCP region of the sandbox subnet (default: us-central1)
  --zone <zone>              GCP zone for the test daemon VM (default: us-central1-a)
  --ar-location <loc>        Artifact Registry location (default: us)
  --ar-repo <name>           Artifact Registry Docker repository (default: grain)
  --image-tag <tag>          Image tag when building the test daemon image (default: sandbox-<git-sha>)
  --image <ref>              Full pre-built daemon image reference
  --skip-image-build         Skip building/pushing the daemon image (requires --image)
  --state-bucket <bucket>    GCS bucket holding sandbox state/manifests (default: <project>-<prefix>-tfstate)
  --local-state              Do not read/write GCS image manifests
  --no-verifier-sa           Do not add the sandbox verifier SA to the entry's registry access_list
  --skip-verify              Skip running verify.sh --entry <entry> after registration
  -h, --help                 Show this help text
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "daemon-up.sh: option '$2' requires an argument" >&2
    usage >&2
    exit 2
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project)
      require_arg "$#" "$1"
      PROJECT_ID="$2"
      shift 2
      ;;
    --owner)
      require_arg "$#" "$1"
      OWNER_EMAIL="$2"
      shift 2
      ;;
    --entry)
      require_arg "$#" "$1"
      ENTRY_NAME="$2"
      shift 2
      ;;
    --allow-principal)
      require_arg "$#" "$1"
      ALLOW_PRINCIPALS+=("$2")
      shift 2
      ;;
    --repo)
      require_arg "$#" "$1"
      TARGET_REPOS+=("$2")
      shift 2
      ;;
    --display-name)
      require_arg "$#" "$1"
      DISPLAY_NAME="$2"
      shift 2
      ;;
    --purpose)
      require_arg "$#" "$1"
      PURPOSE="$2"
      shift 2
      ;;
    --machine-type)
      require_arg "$#" "$1"
      MACHINE_TYPE="$2"
      shift 2
      ;;
    --boot-disk-gb)
      require_arg "$#" "$1"
      BOOT_DISK_GB="$2"
      shift 2
      ;;
    --prefix)
      require_arg "$#" "$1"
      NAME_PREFIX="$2"
      shift 2
      ;;
    --region)
      require_arg "$#" "$1"
      REGION="$2"
      shift 2
      ;;
    --zone)
      require_arg "$#" "$1"
      ZONE="$2"
      shift 2
      ;;
    --ar-location)
      require_arg "$#" "$1"
      AR_LOCATION="$2"
      shift 2
      ;;
    --ar-repo)
      require_arg "$#" "$1"
      AR_REPO="$2"
      shift 2
      ;;
    --image-tag)
      require_arg "$#" "$1"
      IMAGE_TAG="$2"
      shift 2
      ;;
    --image)
      require_arg "$#" "$1"
      DAEMON_IMAGE="$2"
      shift 2
      ;;
    --skip-image-build)
      SKIP_IMAGE_BUILD=1
      shift
      ;;
    --state-bucket)
      require_arg "$#" "$1"
      STATE_BUCKET="$2"
      shift 2
      ;;
    --local-state)
      LOCAL_STATE=1
      shift
      ;;
    --no-verifier-sa)
      ALLOW_VERIFIER_SA=0
      shift
      ;;
    --skip-verify)
      SKIP_VERIFY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "daemon-up.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" || -z "${OWNER_EMAIL}" ]]; then
  echo "daemon-up.sh: --project and --owner are required" >&2
  usage >&2
  exit 2
fi

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "daemon-up.sh: --prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$" >&2
  exit 2
fi

if ! [[ "${ENTRY_NAME}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --entry '${ENTRY_NAME}' (must match ^[a-z][a-z0-9-]{1,30}[a-z0-9]$)" >&2
  exit 2
fi

if ! [[ "${OWNER_EMAIL}" =~ ^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
  echo "daemon-up.sh: invalid --owner email '${OWNER_EMAIL}'" >&2
  exit 2
fi

for p in "${ALLOW_PRINCIPALS[@]}"; do
  if ! [[ "${p}" =~ ^(user|group|serviceAccount):[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
    echo "daemon-up.sh: invalid --allow-principal '${p}' (expected user:<email>, group:<email>, or serviceAccount:<email>)" >&2
    exit 2
  fi
done

if [[ "${#TARGET_REPOS[@]}" -eq 0 ]]; then
  TARGET_REPOS=("ai-on-gke/grain")
fi

for r in "${TARGET_REPOS[@]}"; do
  if ! [[ "${r}" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    echo "daemon-up.sh: invalid --repo '${r}' (expected <owner>/<name>)" >&2
    exit 2
  fi
done

if ! [[ "${REGION}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --region '${REGION}'" >&2
  exit 2
fi

if ! [[ "${ZONE}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --zone '${ZONE}'" >&2
  exit 2
fi

if ! [[ "${MACHINE_TYPE}" =~ ^[a-z][a-z0-9-]{1,40}[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --machine-type '${MACHINE_TYPE}'" >&2
  exit 2
fi

if [[ "${MACHINE_TYPE}" == e2-* ]]; then
  echo "daemon-up.sh: --machine-type '${MACHINE_TYPE}': E2 machine types do not support nested virtualization; use an N2 or C4 machine type (for example n2-standard-2)" >&2
  exit 2
fi

if ! [[ "${BOOT_DISK_GB}" =~ ^[0-9]+$ ]] || [[ "${BOOT_DISK_GB}" -lt 10 || "${BOOT_DISK_GB}" -gt 200 ]]; then
  echo "daemon-up.sh: --boot-disk-gb must be an integer between 10 and 200" >&2
  exit 2
fi

if ! [[ "${AR_LOCATION}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --ar-location '${AR_LOCATION}'" >&2
  exit 2
fi

if ! [[ "${AR_REPO}" =~ ^[a-z][a-z0-9-]{1,61}[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --ar-repo '${AR_REPO}'" >&2
  exit 2
fi

if [[ "${SKIP_IMAGE_BUILD}" -eq 1 && -z "${DAEMON_IMAGE}" ]]; then
  echo "daemon-up.sh: --skip-image-build requires --image" >&2
  exit 2
fi

if [[ -n "${IMAGE_TAG}" ]] && ! [[ "${IMAGE_TAG}" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]]; then
  echo "daemon-up.sh: invalid --image-tag '${IMAGE_TAG}'" >&2
  exit 2
fi

if [[ -n "${DAEMON_IMAGE}" ]] && ! [[ "${DAEMON_IMAGE}" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]]; then
  echo "daemon-up.sh: invalid --image '${DAEMON_IMAGE}'" >&2
  exit 2
fi

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if ! [[ "${STATE_BUCKET}" =~ ^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$ ]]; then
  echo "daemon-up.sh: invalid --state-bucket '${STATE_BUCKET}'" >&2
  exit 2
fi

if [[ -z "${DISPLAY_NAME}" ]]; then
  DISPLAY_NAME="Sandbox Test Daemon (${ENTRY_NAME})"
fi

for cmd in gcloud jq curl; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "daemon-up.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

if [[ "${SKIP_IMAGE_BUILD}" -eq 0 ]]; then
  for cmd in docker go npm make; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
      echo "daemon-up.sh: '${cmd}' is required to build the test grain daemon image" >&2
      exit 1
    fi
  done
fi

active_acct="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -n1 || true)"
if [[ -z "${active_acct}" ]]; then
  echo "daemon-up.sh: no active gcloud credentials found. Authenticate first (or grant GCP sandbox capability)." >&2
  exit 1
fi

if [[ -z "${IMAGE_TAG}" ]]; then
  short_sha="$(git -C "${REPO_ROOT}" rev-parse --short=12 HEAD 2>/dev/null || date +%Y%m%d%H%M%S)"
  IMAGE_TAG="sandbox-${short_sha}"
fi

if [[ -z "${DAEMON_IMAGE}" ]]; then
  DAEMON_IMAGE="${AR_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/grain-daemon:${IMAGE_TAG}"
fi

log() {
  printf '[hub-sandbox/daemon-up] %s\n' "$*"
}

project_num="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
if ! [[ "${project_num}" =~ ^[0-9]+$ ]]; then
  echo "daemon-up.sh: failed to resolve numeric projectNumber for '${PROJECT_ID}'" >&2
  exit 1
fi

ensure_gcs_bucket_owned() {
  local bucket="$1"
  local bucket_proj_num
  bucket_proj_num="$(gcloud storage buckets describe "gs://${bucket}" --project="${PROJECT_ID}" --raw --format='value(projectNumber)' 2>/dev/null || true)"
  if [[ -n "${bucket_proj_num}" && "${bucket_proj_num}" != "${project_num}" ]]; then
    echo "daemon-up.sh: refusing to use gs://${bucket}: bucket belongs to project number ${bucket_proj_num}, not ${PROJECT_ID} (${project_num})" >&2
    exit 1
  fi
}

if [[ "${LOCAL_STATE}" -eq 0 ]]; then
  ensure_gcs_bucket_owned "${STATE_BUCKET}"
fi

reg_vm_name="${NAME_PREFIX}-vm"
log "Inspecting sandbox registry VM ${reg_vm_name} in ${PROJECT_ID} (${ZONE})..."
if ! reg_vm_json="$(gcloud compute instances describe "${reg_vm_name}" --project="${PROJECT_ID}" --zone="${ZONE}" --format=json 2>/dev/null)"; then
  echo "daemon-up.sh: sandbox registry VM '${reg_vm_name}' not found in ${PROJECT_ID} (${ZONE}). Run ./deploy/test/hub-sandbox/up.sh first." >&2
  exit 1
fi

reg_cfg_raw="$(jq -r '.metadata.items[]? | select(.key == "grain-registry-config") | .value' <<<"${reg_vm_json}")"
registry_host="$(jq -r '.registry_host // empty' <<<"${reg_cfg_raw}")"
instance_domain="$(jq -r '.instance_domain // empty' <<<"${reg_cfg_raw}")"
vm_sa_email="$(jq -r '.serviceAccounts[0].email // empty' <<<"${reg_vm_json}")"

if [[ -z "${registry_host}" || -z "${instance_domain}" ]]; then
  echo "daemon-up.sh: failed to read registry_host/instance_domain from ${reg_vm_name} metadata" >&2
  exit 1
fi
if [[ -z "${vm_sa_email}" ]]; then
  vm_sa_email="${project_num}-compute@developer.gserviceaccount.com"
fi

tmp_dir="$(mktemp -d)"
chmod 0700 "${tmp_dir}"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

if [[ "${SKIP_IMAGE_BUILD}" -eq 0 ]]; then
  log "Building static grain binary (with embedded UI and stamped release version)..."
  make -C "${REPO_ROOT}" frontend grain

  img_ctx="${tmp_dir}/image"
  install -d -m 0755 "${img_ctx}"
  install -m 0755 "${REPO_ROOT}/bin/grain" "${img_ctx}/grain"

  # Extract the exact pinned runtime base image digest from Dockerfile.hub
  # so both sandbox images share the same pinned Debian Bookworm base.
  runtime_base="$(awk -F= '/^ARG RUNTIME_IMAGE=/ { print $2; exit }' "${REPO_ROOT}/Dockerfile.hub")"
  if [[ -z "${runtime_base}" ]]; then
    runtime_base="debian:bookworm-slim"
  fi

  cat > "${img_ctx}/Dockerfile" <<DOCKER_EOF
FROM ${runtime_base}

LABEL org.opencontainers.image.title="grain-daemon-sandbox" \\
      org.opencontainers.image.description="Disposable test grain daemon for registry sandbox verification."

RUN apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 update && \\
    DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 install -y --no-install-recommends \\
        ca-certificates \\
        curl \\
        git \\
        tini && \\
    rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 1000 grain && \\
    useradd --uid 1000 --gid 1000 --create-home --home-dir /var/lib/grain --shell /bin/bash grain

COPY grain /usr/local/bin/grain

USER grain
WORKDIR /var/lib/grain

EXPOSE 8080

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/grain"]
DOCKER_EOF

  gcloud auth configure-docker "${AR_LOCATION}-docker.pkg.dev" --quiet

  log "Building lightweight grain-daemon image -> ${DAEMON_IMAGE}..."
  docker build -t "${DAEMON_IMAGE}" "${img_ctx}"

  log "Pushing ${DAEMON_IMAGE}..."
  if ! docker push "${DAEMON_IMAGE}"; then
    log "Direct docker push failed (for example due to a sandbox OCI pull-cache proxy on *.pkg.dev); publishing runtime image via Cloud Build..."
    gcloud services enable cloudbuild.googleapis.com --project="${PROJECT_ID}" --quiet
    if [[ "${LOCAL_STATE}" -eq 0 ]]; then
      ensure_gcs_bucket_owned "${STATE_BUCKET}"
      gcloud builds submit "${img_ctx}" \
        --project="${PROJECT_ID}" \
        --gcs-source-staging-dir="gs://${STATE_BUCKET}/cloudbuild/source" \
        --gcs-log-dir="gs://${STATE_BUCKET}/cloudbuild/logs" \
        --tag="${DAEMON_IMAGE}" \
        --quiet
    else
      gcloud builds submit "${img_ctx}" \
        --project="${PROJECT_ID}" \
        --tag="${DAEMON_IMAGE}" \
        --quiet
    fi
  fi

  if [[ "${LOCAL_STATE}" -eq 0 ]]; then
    if gcloud storage buckets describe "gs://${STATE_BUCKET}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
      for manifest_name in pushed-daemon-images.txt pushed-images.txt; do
        tmp_manifest="${tmp_dir}/${manifest_name}"
        gcloud storage cp "gs://${STATE_BUCKET}/${manifest_name}" "${tmp_manifest}" 2>/dev/null || true
        if ! grep -Fxq "${DAEMON_IMAGE}" "${tmp_manifest}" 2>/dev/null; then
          printf '%s\n' "${DAEMON_IMAGE}" >> "${tmp_manifest}"
          gcloud storage cp "${tmp_manifest}" "gs://${STATE_BUCKET}/${manifest_name}" --quiet
        fi
      done
    fi
  fi
fi

# Build the daemon's allowed_principals list (owner + human/group principals).
daemon_principals=("user:${OWNER_EMAIL,,}")
for p in "${ALLOW_PRINCIPALS[@]}"; do
  if [[ "${p}" == user:* || "${p}" == group:* ]]; then
    kind="${p%%:*}"
    addr="${p#*:}"
    daemon_principals+=("${kind}:${addr,,}")
  fi
done
daemon_principals_json="$(printf '%s\n' "${daemon_principals[@]}" | jq -R . | jq -s 'unique')"
target_repos_json="$(printf '%s\n' "${TARGET_REPOS[@]}" | jq -R . | jq -s 'unique')"

cfg_file="${tmp_dir}/grain-daemon-config.json"
jq -n \
  --arg daemon_image "${DAEMON_IMAGE}" \
  --arg entry_name "${ENTRY_NAME}" \
  --arg registry_host "${registry_host}" \
  --arg instance_domain "${instance_domain}" \
  --arg display_name "${DISPLAY_NAME}" \
  --arg purpose "${PURPOSE}" \
  --argjson allowed_principals "${daemon_principals_json}" \
  --argjson target_repos "${target_repos_json}" \
  '{
    daemon_image: $daemon_image,
    entry_name: $entry_name,
    registry_host: $registry_host,
    instance_domain: $instance_domain,
    display_name: $display_name,
    purpose: $purpose,
    allowed_principals: $allowed_principals,
    target_repos: $target_repos
  }' > "${cfg_file}"

fw_name="${NAME_PREFIX}-allow-daemon-iap-ssh"
net_name="${NAME_PREFIX}-net"
subnet_name="${NAME_PREFIX}-subnet"
daemon_tag="${NAME_PREFIX}-daemon-vm"
daemon_vm_name="${NAME_PREFIX}-daemon-${ENTRY_NAME}"

if ! gcloud compute firewall-rules describe "${fw_name}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
  log "Creating IAP SSH firewall rule ${fw_name} (tcp:22 only) for tag ${daemon_tag}..."
  gcloud compute firewall-rules create "${fw_name}" \
    --project="${PROJECT_ID}" \
    --network="${net_name}" \
    --direction=INGRESS \
    --action=ALLOW \
    --rules=tcp:22 \
    --source-ranges=35.235.240.0/20 \
    --target-tags="${daemon_tag}" \
    --quiet
fi

if gcloud compute instances describe "${daemon_vm_name}" --project="${PROJECT_ID}" --zone="${ZONE}" >/dev/null 2>&1; then
  log "Updating metadata on existing test daemon VM ${daemon_vm_name}..."
  gcloud compute instances add-metadata "${daemon_vm_name}" \
    --project="${PROJECT_ID}" \
    --zone="${ZONE}" \
    --metadata="enable-oslogin=TRUE" \
    --metadata-from-file="grain-daemon-config=${cfg_file},startup-script=${SCRIPT_DIR}/files/daemon-startup.sh" \
    --quiet
else
  log "Creating private-IP test daemon GCE VM ${daemon_vm_name} (${MACHINE_TYPE}) in ${ZONE}..."
  gcloud compute instances create "${daemon_vm_name}" \
    --project="${PROJECT_ID}" \
    --zone="${ZONE}" \
    --machine-type="${MACHINE_TYPE}" \
    --subnet="${subnet_name}" \
    --no-address \
    --image-family=debian-12 \
    --image-project=debian-cloud \
    --boot-disk-size="${BOOT_DISK_GB}GB" \
    --boot-disk-type=pd-balanced \
    --service-account="${vm_sa_email}" \
    --scopes="https://www.googleapis.com/auth/cloud-platform.read-only" \
    --shielded-secure-boot \
    --shielded-vtpm \
    --shielded-integrity-monitoring \
    --tags="${daemon_tag}" \
    --labels="grain-sandbox-role=test-daemon,grain-sandbox-prefix=${NAME_PREFIX},grain-sandbox-entry=${ENTRY_NAME}" \
    --metadata="enable-oslogin=TRUE" \
    --metadata-from-file="grain-daemon-config=${cfg_file},startup-script=${SCRIPT_DIR}/files/daemon-startup.sh" \
    --quiet
fi

log "Waiting for VM ${daemon_vm_name} startup & reconciling grain-daemon over IAP SSH..."
reconciled=0
for attempt in $(seq 1 36); do
  if gcloud compute ssh "${daemon_vm_name}" \
    --project="${PROJECT_ID}" \
    --zone="${ZONE}" \
    --tunnel-through-iap \
    --quiet \
    --command="test -x /usr/local/sbin/grain-daemon-reconcile && sudo /usr/local/sbin/grain-daemon-reconcile" >/dev/null 2>&1; then
    reconciled=1
    break
  fi
  log "  [attempt ${attempt}/36] VM startup still in progress; retrying in 10s..."
  sleep 10
done
if [[ "${reconciled}" -ne 1 ]]; then
  echo "daemon-up.sh: timed out waiting for ${daemon_vm_name} to reconcile over IAP SSH" >&2
  exit 1
fi

log "Registering connected entry '${ENTRY_NAME}' on https://${registry_host}..."
tok_file="${tmp_dir}/entry.token"
reg_args=(
  --project "${PROJECT_ID}"
  --prefix "${NAME_PREFIX}"
  --zone "${ZONE}"
  --entry "${ENTRY_NAME}"
  --owner "${OWNER_EMAIL}"
  --display-name "${DISPLAY_NAME}"
  --purpose "${PURPOSE}"
  --host-kind workstation
  --registry-host "${registry_host}"
  --instance-domain "${instance_domain}"
  --revoke-previous
  --token-file "${tok_file}"
)
for p in "${ALLOW_PRINCIPALS[@]}"; do
  reg_args+=(--allow-principal "${p}")
done
if [[ "${ALLOW_VERIFIER_SA}" -eq 1 ]]; then
  reg_args+=(--allow-verifier-sa)
fi
"${SCRIPT_DIR}/register-entry.sh" "${reg_args[@]}" >/dev/null

registry_url="https://${registry_host}"
entry_url="https://${ENTRY_NAME}.${instance_domain}"
jwks_url="${registry_url}/.well-known/jwks.json"

log "Running 'grain hub register' on ${daemon_vm_name} to establish heartbeat & outbound tunnel..."
reg_payload_b64="$(jq -n \
  --rawfile tok "${tok_file}" \
  --arg registry_url "${registry_url}" \
  --arg entry_name "${ENTRY_NAME}" \
  '{
    token: ($tok | sub("[\r\n]+$"; "")),
    registry_url: $registry_url,
    entry_name: $entry_name
  }' | base64 | tr -d '\n')"

gcloud compute ssh "${daemon_vm_name}" \
  --project="${PROJECT_ID}" \
  --zone="${ZONE}" \
  --tunnel-through-iap \
  --quiet \
  --command="sudo bash -s" <<EOF
set -euo pipefail
payload="\$(printf '%s' '${reg_payload_b64}' | base64 -d)"
tok="\$(jq -r '.token' <<<"\${payload}")"
reg_url="\$(jq -r '.registry_url' <<<"\${payload}")"
entry="\$(jq -r '.entry_name' <<<"\${payload}")"
GRAIN_HUB_TOKEN="\${tok}" /usr/local/bin/grain hub register -hub-url "\${reg_url}" "\${entry}"
EOF

if [[ "${SKIP_VERIFY}" -eq 0 ]]; then
  log "Verifying end-to-end directory status, route resolution, and tunnel routing..."
  verify_args=(
    --project "${PROJECT_ID}"
    --prefix "${NAME_PREFIX}"
    --zone "${ZONE}"
    --entry "${ENTRY_NAME}"
  )
  if [[ "${LOCAL_STATE}" -eq 1 ]]; then
    verify_args+=(--local-state)
  else
    verify_args+=(--state-bucket "${STATE_BUCKET}")
  fi
  # Give the outbound WebSocket mux tunnel a few seconds to complete its handshake
  # after the initial heartbeat before running verify.sh.
  verified=0
  for attempt in $(seq 1 6); do
    if "${SCRIPT_DIR}/verify.sh" "${verify_args[@]}"; then
      verified=1
      break
    fi
    if [[ "${attempt}" -lt 6 ]]; then
      log "  [attempt ${attempt}/6] Waiting 3s for outbound tunnel session to settle..."
      sleep 3
    fi
  done
  if [[ "${verified}" -ne 1 ]]; then
    echo "daemon-up.sh: end-to-end verification failed for '${ENTRY_NAME}'" >&2
    exit 1
  fi
fi

allowed_csv="$(jq -r 'join(",")' <<<"${daemon_principals_json}")"

cat <<SUMMARY

================================================================================
Test Grain Daemon Connected & Ready
================================================================================
  Project:            ${PROJECT_ID}
  Daemon GCE VM:      ${daemon_vm_name} (${ZONE}, ${MACHINE_TYPE}, private IP)
  Daemon Image:       ${DAEMON_IMAGE}
  Entry Name:         ${ENTRY_NAME}
  Entry URL:          ${entry_url}
  Registry URL:       ${registry_url}
  JWKS URL:           ${jwks_url}
  Owner:              ${OWNER_EMAIL}
  Allowed Principals: ${allowed_csv}

Access in browser (via Cloud IAP -> Registry -> Outbound Mux Tunnel -> Daemon):
  1. Directory catalog: ${registry_url}/
  2. Routed Grain UI:   ${entry_url}/
     (Note: if '${ENTRY_NAME}' was not pre-listed on --entry during up.sh,
      *.${instance_domain} uses the sandbox's self-signed wildcard fallback
      certificate; click through in the browser, or re-run up.sh with
      --entry ${ENTRY_NAME} for a Google-managed certificate.)

Re-verify end-to-end from CLI:
  ${SCRIPT_DIR}/verify.sh --project ${PROJECT_ID} --prefix ${NAME_PREFIX} --zone ${ZONE} --entry ${ENTRY_NAME}

Tear down this test daemon cleanly (keeps the sandbox registry running):
  ${SCRIPT_DIR}/daemon-down.sh --project ${PROJECT_ID} --prefix ${NAME_PREFIX} --zone ${ZONE} --entry ${ENTRY_NAME}
================================================================================
SUMMARY
