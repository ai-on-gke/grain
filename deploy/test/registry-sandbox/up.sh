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

# Provision a disposable test instance of grain-registry in a GCP sandbox
# project: builds and pushes Dockerfile.registry to Artifact Registry, applies
# the Terraform configuration (GCE VM + local PostgreSQL container + Cloud KMS
# ES256 key + Global External Application Load Balancer with IAP), waits for
# health checks, and optionally registers one or more connected entries.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

PROJECT_ID="${PROJECT_ID:-}"
ADMIN_EMAIL="${ADMIN_EMAIL:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
REGION="${REGION:-us-central1}"
ZONE="${ZONE:-us-central1-a}"
AR_LOCATION="${AR_LOCATION:-us}"
AR_REPO="${AR_REPO:-grain}"
IMAGE_TAG="${IMAGE_TAG:-}"
REGISTRY_IMAGE="${REGISTRY_IMAGE:-}"
SKIP_IMAGE_BUILD=0
TLS_MODE="${TLS_MODE:-hybrid}"
WAIT_CERT=1
CERT_TIMEOUT_SECS="${CERT_TIMEOUT_SECS:-900}"
STATE_BUCKET="${STATE_BUCKET:-}"
LOCAL_STATE=0
TOKEN_OUT_DIR=""
ALLOW_VERIFIER_ON_ENTRIES=1

EXTRA_USERS=()
ENTRIES=()

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/up.sh \
    --project <gcp-project-id> \
    --admin <owner@example.com> \
    [options]

Options:
  --project <id>             GCP sandbox project ID (required)
  --admin <email>            Primary admin/owner email admitted through Cloud IAP (required)
  --extra-user <email>       Additional human email admitted through Cloud IAP (repeatable)
  --entry <name[:owner]>     Connected entry to pre-declare on managed TLS and register (repeatable; owner defaults to --admin)
  --prefix <prefix>          Resource name prefix, 3-12 chars (default: grain-reg)
  --region <region>          GCP region for subnet/NAT/KMS (default: us-central1)
  --zone <zone>              GCP zone for the test VM (default: us-central1-a)
  --ar-location <loc>        Artifact Registry location (default: us)
  --ar-repo <name>           Artifact Registry Docker repository name (default: grain)
  --image-tag <tag>          Image tag when building Dockerfile.registry (default: sandbox-<git-sha>)
  --image <ref>              Full pre-built image reference (skips image build unless omitted)
  --skip-image-build         Skip building/pushing Dockerfile.registry (requires --image)
  --tls-mode <mode>          hybrid (default), managed, or self-signed
  --no-wait-cert             Do not wait for Google-managed SSL certificate ACTIVE status
  --cert-timeout <secs>      Max seconds to wait for Google-managed SSL cert (default: 900)
  --state-bucket <bucket>    GCS bucket name for Terraform state (default: <project>-<prefix>-tfstate)
  --local-state              Store Terraform state locally instead of GCS
  --token-out-dir <dir>      Directory (0700) where minted <entry>.token files are written
  --no-verifier-on-entries   Do not add the verifier SA to registered entries' access_list
  -h, --help                 Show this help text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project)
      PROJECT_ID="$2"
      shift 2
      ;;
    --admin)
      ADMIN_EMAIL="$2"
      shift 2
      ;;
    --extra-user)
      EXTRA_USERS+=("$2")
      shift 2
      ;;
    --entry)
      ENTRIES+=("$2")
      shift 2
      ;;
    --prefix)
      NAME_PREFIX="$2"
      shift 2
      ;;
    --region)
      REGION="$2"
      shift 2
      ;;
    --zone)
      ZONE="$2"
      shift 2
      ;;
    --ar-location)
      AR_LOCATION="$2"
      shift 2
      ;;
    --ar-repo)
      AR_REPO="$2"
      shift 2
      ;;
    --image-tag)
      IMAGE_TAG="$2"
      shift 2
      ;;
    --image)
      REGISTRY_IMAGE="$2"
      shift 2
      ;;
    --skip-image-build)
      SKIP_IMAGE_BUILD=1
      shift
      ;;
    --tls-mode)
      TLS_MODE="$2"
      shift 2
      ;;
    --no-wait-cert)
      WAIT_CERT=0
      shift
      ;;
    --cert-timeout)
      CERT_TIMEOUT_SECS="$2"
      shift 2
      ;;
    --state-bucket)
      STATE_BUCKET="$2"
      shift 2
      ;;
    --local-state)
      LOCAL_STATE=1
      shift
      ;;
    --token-out-dir)
      TOKEN_OUT_DIR="$2"
      shift 2
      ;;
    --no-verifier-on-entries)
      ALLOW_VERIFIER_ON_ENTRIES=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "up.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" || -z "${ADMIN_EMAIL}" ]]; then
  echo "up.sh: --project and --admin are required" >&2
  usage >&2
  exit 2
fi

for cmd in gcloud terraform jq curl; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "up.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

if [[ "${SKIP_IMAGE_BUILD}" -eq 0 ]] && ! command -v docker >/dev/null 2>&1; then
  echo "up.sh: 'docker' is required to build Dockerfile.registry" >&2
  exit 1
fi

active_acct="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -n1 || true)"
if [[ -z "${active_acct}" ]]; then
  echo "up.sh: no active gcloud credentials found. Authenticate first (or grant GCP sandbox capability)." >&2
  exit 1
fi

if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -r "${HOME}/.gcp-service-account.json" ]]; then
  export GOOGLE_APPLICATION_CREDENTIALS="${HOME}/.gcp-service-account.json"
elif [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -z "${GOOGLE_OAUTH_ACCESS_TOKEN:-}" && ! -r "${HOME}/.config/gcloud/application_default_credentials.json" ]]; then
  GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
  export GOOGLE_OAUTH_ACCESS_TOKEN
fi

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if [[ -z "${IMAGE_TAG}" ]]; then
  short_sha="$(git -C "${REPO_ROOT}" rev-parse --short=12 HEAD 2>/dev/null || date +%Y%m%d%H%M%S)"
  IMAGE_TAG="sandbox-${short_sha}"
fi

if [[ -z "${REGISTRY_IMAGE}" ]]; then
  REGISTRY_IMAGE="${AR_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/grain-registry:${IMAGE_TAG}"
fi

log() {
  printf '[registry-sandbox/up] %s\n' "$*"
}

log "Enabling required GCP APIs in ${PROJECT_ID}..."
gcloud services enable \
  artifactregistry.googleapis.com \
  cloudkms.googleapis.com \
  compute.googleapis.com \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  iap.googleapis.com \
  storage.googleapis.com \
  --project="${PROJECT_ID}" \
  --quiet

ensure_gcs_bucket() {
  local bucket="$1"
  if ! gcloud storage buckets describe "gs://${bucket}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud storage buckets create "gs://${bucket}" \
      --project="${PROJECT_ID}" \
      --location="${REGION}" \
      --uniform-bucket-level-access \
      --public-access-prevention \
      --quiet
  fi
}

if [[ "${LOCAL_STATE}" -eq 0 ]]; then
  log "Ensuring GCS Terraform state bucket gs://${STATE_BUCKET}..."
  ensure_gcs_bucket "${STATE_BUCKET}"
fi

if [[ "${SKIP_IMAGE_BUILD}" -eq 0 ]]; then
  log "Ensuring Artifact Registry repository ${AR_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}..."
  if ! gcloud artifacts repositories describe "${AR_REPO}" \
    --project="${PROJECT_ID}" \
    --location="${AR_LOCATION}" >/dev/null 2>&1; then
    gcloud artifacts repositories create "${AR_REPO}" \
      --project="${PROJECT_ID}" \
      --location="${AR_LOCATION}" \
      --repository-format=docker \
      --description="Grain container images" \
      --quiet
  fi

  gcloud auth configure-docker "${AR_LOCATION}-docker.pkg.dev" --quiet

  log "Building Dockerfile.registry -> ${REGISTRY_IMAGE}..."
  staged_grain_ca=0
  if [[ -f /usr/local/share/ca-certificates/grain-ca.crt && ! -f "${REPO_ROOT}/.grain-ca.crt" ]]; then
    cp /usr/local/share/ca-certificates/grain-ca.crt "${REPO_ROOT}/.grain-ca.crt"
    staged_grain_ca=1
  fi
  make -C "${REPO_ROOT}" image-registry IMAGE_REGISTRY="${REGISTRY_IMAGE}"
  if [[ "${staged_grain_ca}" -eq 1 ]]; then
    rm -f "${REPO_ROOT}/.grain-ca.crt"
  fi

  log "Pushing ${REGISTRY_IMAGE}..."
  if ! docker push "${REGISTRY_IMAGE}"; then
    log "Direct docker push failed (for example due to a sandbox OCI pull-cache proxy on *.pkg.dev); publishing runtime image via Cloud Build..."
    gcloud services enable cloudbuild.googleapis.com --project="${PROJECT_ID}" --quiet
    ensure_gcs_bucket "${STATE_BUCKET}"
    cb_dir="$(mktemp -d)"
    cid="$(docker create "${REGISTRY_IMAGE}")"
    docker cp "${cid}:/usr/local/bin/grain-registry" "${cb_dir}/grain-registry"
    docker rm -f "${cid}" >/dev/null
    chmod 0755 "${cb_dir}/grain-registry"
    awk '
      /^ARG RUNTIME_IMAGE=/ { print; next }
      /^FROM \$\{RUNTIME_IMAGE\}/ { in_runtime=1 }
      in_runtime {
        sub(/^COPY --from=build \/out\/grain-registry /, "COPY grain-registry ")
        print
      }
    ' "${REPO_ROOT}/Dockerfile.registry" > "${cb_dir}/Dockerfile"
    gcloud builds submit "${cb_dir}" \
      --project="${PROJECT_ID}" \
      --gcs-source-staging-dir="gs://${STATE_BUCKET}/cloudbuild/source" \
      --gcs-log-dir="gs://${STATE_BUCKET}/cloudbuild/logs" \
      --tag="${REGISTRY_IMAGE}" \
      --quiet
    rm -rf "${cb_dir}"
  fi

  if [[ "${LOCAL_STATE}" -eq 0 ]]; then
    tmp_manifest="$(mktemp)"
    gcloud storage cp "gs://${STATE_BUCKET}/pushed-images.txt" "${tmp_manifest}" 2>/dev/null || true
    if ! grep -Fxq "${REGISTRY_IMAGE}" "${tmp_manifest}" 2>/dev/null; then
      printf '%s\n' "${REGISTRY_IMAGE}" >> "${tmp_manifest}"
      gcloud storage cp "${tmp_manifest}" "gs://${STATE_BUCKET}/pushed-images.txt" --quiet
    fi
    rm -f "${tmp_manifest}"
  fi
fi

tmp_work_dir="$(mktemp -d)"
chmod 0700 "${tmp_work_dir}"
tmp_tfvars="${tmp_work_dir}/terraform.tfvars.json"
tmp_override=""
cleanup() {
  rm -rf "${tmp_work_dir}" "${REPO_ROOT}/.grain-ca.crt"
  if [[ -n "${tmp_override}" ]]; then
    rm -f "${tmp_override}"
  fi
}
trap cleanup EXIT

if [[ "${LOCAL_STATE}" -eq 1 ]]; then
  tmp_override="${SCRIPT_DIR}/backend_override.tf"
  cat > "${tmp_override}" <<'EOF'
terraform {
  backend "local" {}
}
EOF
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false
else
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false \
    -backend-config="bucket=${STATE_BUCKET}" \
    -backend-config="prefix=registry-sandbox/${NAME_PREFIX}"
fi

extra_users_json="[]"
if [[ "${#EXTRA_USERS[@]}" -gt 0 ]]; then
  extra_users_json="$(printf '%s\n' "${EXTRA_USERS[@]}" | jq -R . | jq -s .)"
fi

entries_json="[]"
if [[ "${#ENTRIES[@]}" -gt 0 ]]; then
  entry_names=()
  for spec in "${ENTRIES[@]}"; do
    ename="${spec%%:*}"
    entry_names+=("${ename}")
  done
  entries_json="$(printf '%s\n' "${entry_names[@]}" | jq -R . | jq -s 'unique')"
fi

log "Checking IAM permissions for ${active_acct} in ${PROJECT_ID}..."
access_tok="$(gcloud auth print-access-token)"
perm_resp="$(curl -fsSL -X POST \
  -H "Authorization: Bearer ${access_tok}" \
  -H "Content-Type: application/json" \
  "https://cloudresourcemanager.googleapis.com/v1/projects/${PROJECT_ID}:testIamPermissions" \
  -d '{
    "permissions": [
      "cloudkms.cryptoKeys.setIamPolicy",
      "iap.webServices.setIamPolicy",
      "resourcemanager.projects.setIamPolicy",
      "iam.serviceAccounts.getAccessToken",
      "iap.tunnelInstances.accessViaIAP"
    ]
  }')"

has_perm() {
  jq -e --arg p "$1" '(.permissions // []) | index($p) != null' <<<"${perm_resp}" >/dev/null 2>&1
}

missing_roles=()
if ! has_perm "cloudkms.cryptoKeys.setIamPolicy"; then
  missing_roles+=("roles/cloudkms.admin (missing cloudkms.cryptoKeys.setIamPolicy)")
fi
if ! has_perm "iap.webServices.setIamPolicy"; then
  missing_roles+=("roles/iap.admin (missing iap.webServices.setIamPolicy)")
fi
if [[ "${#missing_roles[@]}" -gt 0 ]]; then
  echo "up.sh: ${active_acct} is missing required IAM roles in ${PROJECT_ID}:" >&2
  for r in "${missing_roles[@]}"; do
    echo "  - ${r}" >&2
  done
  echo "Grant those roles (plus roles/iap.tunnelResourceAccessor and roles/compute.osAdminLogin for IAP SSH entry registration) and re-run up.sh." >&2
  exit 1
fi

create_vm_sa_json="true"
if ! has_perm "resourcemanager.projects.setIamPolicy"; then
  log "Note: resourcemanager.projects.setIamPolicy not held; using default Compute Engine service account for the VM."
  create_vm_sa_json="false"
fi

create_verifier_sa_json="true"
verifier_sa_email=""
if ! has_perm "iam.serviceAccounts.getAccessToken" && [[ "${active_acct}" == *.gserviceaccount.com ]]; then
  log "Note: iam.serviceAccounts.getAccessToken not held; admitting active service account ${active_acct} as verifier."
  create_verifier_sa_json="false"
  verifier_sa_email="${active_acct}"
fi

has_iap_ssh=0
if has_perm "iap.tunnelInstances.accessViaIAP"; then
  has_iap_ssh=1
fi

jq -n \
  --arg project_id "${PROJECT_ID}" \
  --arg region "${REGION}" \
  --arg zone "${ZONE}" \
  --arg name_prefix "${NAME_PREFIX}" \
  --arg registry_image "${REGISTRY_IMAGE}" \
  --arg admin_email "${ADMIN_EMAIL}" \
  --arg tls_mode "${TLS_MODE}" \
  --arg verifier_sa_email "${verifier_sa_email}" \
  --argjson create_vm_sa "${create_vm_sa_json}" \
  --argjson create_verifier_sa "${create_verifier_sa_json}" \
  --argjson extra_users "${extra_users_json}" \
  --argjson managed_entry_names "${entries_json}" \
  '{
    project_id: $project_id,
    region: $region,
    zone: $zone,
    name_prefix: $name_prefix,
    registry_image: $registry_image,
    admin_email: $admin_email,
    tls_mode: $tls_mode,
    create_vm_service_account: $create_vm_sa,
    create_verifier_sa: $create_verifier_sa,
    verifier_service_account_email: $verifier_sa_email,
    extra_users: $extra_users,
    managed_entry_names: $managed_entry_names
  }' > "${tmp_tfvars}"

# Cloud KMS key rings and crypto keys cannot be deleted in GCP. Re-import them
# if a previous `down.sh` removed the state, and ensure at least one ENABLED
# signing key version exists.
kr_name="${NAME_PREFIX}-kr"
key_name="${NAME_PREFIX}-jwt"
kr_id="projects/${PROJECT_ID}/locations/${REGION}/keyRings/${kr_name}"
key_id="${kr_id}/cryptoKeys/${key_name}"

if gcloud kms keyrings describe "${kr_name}" --location="${REGION}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
  if ! terraform -chdir="${SCRIPT_DIR}" state list 2>/dev/null | grep -Fxq 'google_kms_key_ring.this[0]'; then
    log "Importing pre-existing KMS key ring ${kr_id}..."
    terraform -chdir="${SCRIPT_DIR}" import -input=false -var-file="${tmp_tfvars}" 'google_kms_key_ring.this[0]' "${kr_id}"
  fi
fi

if gcloud kms keys describe "${key_name}" --keyring="${kr_name}" --location="${REGION}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
  if ! terraform -chdir="${SCRIPT_DIR}" state list 2>/dev/null | grep -Fxq 'google_kms_crypto_key.signing[0]'; then
    log "Importing pre-existing KMS crypto key ${key_id}..."
    terraform -chdir="${SCRIPT_DIR}" import -input=false -var-file="${tmp_tfvars}" 'google_kms_crypto_key.signing[0]' "${key_id}"
  fi
  enabled_versions="$(gcloud kms keys versions list \
    --key="${key_name}" \
    --keyring="${kr_name}" \
    --location="${REGION}" \
    --project="${PROJECT_ID}" \
    --filter="state=ENABLED" \
    --format="value(name)" | wc -l | tr -d ' ')"
  if [[ "${enabled_versions}" -eq 0 ]]; then
    log "Creating a fresh ENABLED Cloud KMS key version on ${key_id}..."
    gcloud kms keys versions create \
      --key="${key_name}" \
      --keyring="${kr_name}" \
      --location="${REGION}" \
      --project="${PROJECT_ID}" \
      --primary \
      --quiet
  fi
fi

log "Applying Terraform configuration..."
terraform -chdir="${SCRIPT_DIR}" apply -auto-approve -input=false -var-file="${tmp_tfvars}"

registry_url="$(terraform -chdir="${SCRIPT_DIR}" output -raw registry_url)"
registry_host="$(terraform -chdir="${SCRIPT_DIR}" output -raw registry_host)"
instance_domain="$(terraform -chdir="${SCRIPT_DIR}" output -raw instance_domain)"
jwks_url="$(terraform -chdir="${SCRIPT_DIR}" output -raw jwks_url)"
lb_ip="$(terraform -chdir="${SCRIPT_DIR}" output -raw lb_ip)"
vm_name="$(terraform -chdir="${SCRIPT_DIR}" output -raw vm_name)"
managed_cert_name="$(terraform -chdir="${SCRIPT_DIR}" output -raw managed_cert_name)"

if [[ "${has_iap_ssh}" -eq 1 ]]; then
  log "Waiting for VM ${vm_name} startup & reconciling grain-registry over IAP SSH..."
  reconciled=0
  for attempt in $(seq 1 36); do
    if gcloud compute ssh "${vm_name}" \
      --project="${PROJECT_ID}" \
      --zone="${ZONE}" \
      --tunnel-through-iap \
      --quiet \
      --command="test -x /usr/local/sbin/grain-registry-reconcile && sudo /usr/local/sbin/grain-registry-reconcile" >/dev/null 2>&1; then
      reconciled=1
      break
    fi
    log "  [attempt ${attempt}/36] VM startup still in progress; retrying in 10s..."
    sleep 10
  done
  if [[ "${reconciled}" -ne 1 ]]; then
    echo "up.sh: timed out waiting for ${vm_name} to reconcile over IAP SSH" >&2
    exit 1
  fi
else
  log "Note: iap.tunnelInstances.accessViaIAP not held by ${active_acct}; relying on GCE startup script reconciliation."
fi

log "Waiting for Global HTTPS Load Balancer (${lb_ip}) to serve ${jwks_url}..."
lb_ready=0
for attempt in $(seq 1 48); do
  jwks_resp="$(curl -skf --max-time 10 --resolve "${registry_host}:443:${lb_ip}" "${jwks_url}" 2>/dev/null || true)"
  if [[ -n "${jwks_resp}" ]] && [[ "$(jq -r '.keys | length' <<<"${jwks_resp}" 2>/dev/null || echo 0)" -ge 1 ]]; then
    lb_ready=1
    break
  fi
  log "  [attempt ${attempt}/48] LB/VM startup propagating; retrying in 10s..."
  sleep 10
done
if [[ "${lb_ready}" -ne 1 ]]; then
  echo "up.sh: timed out waiting for HTTPS LB to serve /.well-known/jwks.json" >&2
  gcloud compute instances get-serial-port-output "${vm_name}" --zone="${ZONE}" --project="${PROJECT_ID}" 2>/dev/null | tail -n 60 >&2 || true
  exit 1
fi

if [[ -n "${managed_cert_name}" && "${WAIT_CERT}" -eq 1 ]]; then
  log "Waiting up to ${CERT_TIMEOUT_SECS}s for Google-managed SSL certificate '${managed_cert_name}' to become ACTIVE..."
  deadline=$(( SECONDS + CERT_TIMEOUT_SECS ))
  while (( SECONDS < deadline )); do
    cert_json="$(gcloud compute ssl-certificates describe "${managed_cert_name}" --global --project="${PROJECT_ID}" --format=json)"
    cert_status="$(jq -r '.managed.status // "UNKNOWN"' <<<"${cert_json}")"
    domain_summary="$(jq -r '.managed.domainStatus | to_entries | map("\(.key)=\(.value)") | join(", ")' <<<"${cert_json}")"
    if [[ "${cert_status}" == "ACTIVE" ]]; then
      log "Google-managed SSL certificate '${managed_cert_name}' is ACTIVE (${domain_summary})."
      break
    fi
    log "  certificate status=${cert_status} (${domain_summary}); checking again in 15s..."
    sleep 15
  done
fi

if [[ -n "${TOKEN_OUT_DIR}" ]]; then
  install -d -m 0700 "${TOKEN_OUT_DIR}"
fi

all_humans=("${ADMIN_EMAIL}")
for u in "${EXTRA_USERS[@]}"; do
  if [[ -n "${u}" ]]; then
    all_humans+=("${u}")
  fi
done

if [[ "${#ENTRIES[@]}" -gt 0 ]]; then
  if [[ "${has_iap_ssh}" -eq 1 ]]; then
    for spec in "${ENTRIES[@]}"; do
      entry="${spec%%:*}"
      entry_owner="${ADMIN_EMAIL}"
      if [[ "${spec}" == *:* && -n "${spec#*:}" ]]; then
        entry_owner="${spec#*:}"
      fi
      log "Registering connected entry '${entry}' owned by ${entry_owner}..."
      reg_args=(
        --project "${PROJECT_ID}"
        --prefix "${NAME_PREFIX}"
        --zone "${ZONE}"
        --entry "${entry}"
        --owner "${entry_owner}"
        --registry-host "${registry_host}"
        --instance-domain "${instance_domain}"
      )
      for h in "${all_humans[@]}"; do
        if [[ "${h,,}" != "${entry_owner,,}" ]]; then
          reg_args+=(--allow-principal "user:${h,,}")
        fi
      done
      if [[ "${ALLOW_VERIFIER_ON_ENTRIES}" -eq 1 ]]; then
        reg_args+=(--allow-verifier-sa)
      fi
      if [[ -n "${TOKEN_OUT_DIR}" ]]; then
        reg_args+=(--token-file "${TOKEN_OUT_DIR}/${entry}.token")
      fi
      "${SCRIPT_DIR}/register-entry.sh" "${reg_args[@]}"
    done
  else
    log "Skipping IAP SSH entry bootstrap (missing roles/iap.tunnelResourceAccessor on ${active_acct})."
    log "Run register-entry.sh from an IAP-authorized workstation (or grant roles/iap.tunnelResourceAccessor + roles/compute.osAdminLogin):"
    for spec in "${ENTRIES[@]}"; do
      entry="${spec%%:*}"
      entry_owner="${ADMIN_EMAIL}"
      if [[ "${spec}" == *:* && -n "${spec#*:}" ]]; then
        entry_owner="${spec#*:}"
      fi
      log "  ./deploy/test/registry-sandbox/register-entry.sh --project ${PROJECT_ID} --prefix ${NAME_PREFIX} --zone ${ZONE} --entry ${entry} --owner ${entry_owner} --allow-verifier-sa"
    done
  fi
fi

admitted_summary="${ADMIN_EMAIL}"
if [[ "${#EXTRA_USERS[@]}" -gt 0 ]]; then
  admitted_summary="${ADMIN_EMAIL}, $(IFS=', '; echo "${EXTRA_USERS[*]}")"
fi

cat <<SUMMARY

================================================================================
Grain Registry Sandbox Instance Ready
================================================================================
  Project:          ${PROJECT_ID}
  Registry URL:     ${registry_url}
  Instance Domain:  *.${instance_domain}
  JWKS URL:         ${jwks_url}
  Load Balancer IP: ${lb_ip}
  TLS Mode:         ${TLS_MODE}
  Admitted Users:   ${admitted_summary}

Sign in (browser):
  Open ${registry_url}/api/directory (or ${registry_url}/) and sign in with
  ${ADMIN_EMAIL} via Google Cloud IAP.

Register / rotate a connected daemon entry:
  ${SCRIPT_DIR}/register-entry.sh \\
    --project ${PROJECT_ID} \\
    --prefix ${NAME_PREFIX} \\
    --zone ${ZONE} \\
    --entry my-daemon \\
    --owner ${ADMIN_EMAIL}

Verify catalog, route resolution, and end-to-end tunnel reachability:
  ${SCRIPT_DIR}/verify.sh \\
    --project ${PROJECT_ID} \\
    --prefix ${NAME_PREFIX} \\
    --zone ${ZONE}

Tear down cleanly:
  ${SCRIPT_DIR}/down.sh \\
    --project ${PROJECT_ID} \\
    --prefix ${NAME_PREFIX} \\
    --region ${REGION} \\
    --zone ${ZONE}
================================================================================
SUMMARY
