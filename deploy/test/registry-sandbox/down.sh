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

# Tear down the disposable test grain-registry instance in a GCP sandbox project:
# runs `terraform destroy`, schedules any enabled Cloud KMS signing key versions
# for destruction, deletes pushed sandbox image tags, and removes the GCS
# Terraform state bucket.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PROJECT_ID="${PROJECT_ID:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
REGION="${REGION:-us-central1}"
ZONE="${ZONE:-us-central1-a}"
STATE_BUCKET="${STATE_BUCKET:-}"
LOCAL_STATE=0
KEEP_IMAGES=0
KEEP_STATE_BUCKET=0

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/down.sh --project <gcp-project-id> [options]

Options:
  --project <id>           GCP sandbox project ID (required)
  --prefix <prefix>        Resource name prefix used during up.sh (default: grain-reg)
  --region <region>        GCP region used during up.sh (default: us-central1)
  --zone <zone>            GCP zone used during up.sh (default: us-central1-a)
  --state-bucket <bucket>  GCS bucket holding Terraform state (default: <project>-<prefix>-tfstate)
  --local-state            Destroy using local Terraform state instead of GCS
  --keep-images            Do not delete grain-registry image tags pushed by up.sh
  --keep-state-bucket      Do not delete the GCS Terraform state bucket after destroy
  -h, --help               Show this help text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project)
      PROJECT_ID="$2"
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
    --state-bucket)
      STATE_BUCKET="$2"
      shift 2
      ;;
    --local-state)
      LOCAL_STATE=1
      shift
      ;;
    --keep-images)
      KEEP_IMAGES=1
      shift
      ;;
    --keep-state-bucket)
      KEEP_STATE_BUCKET=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "down.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" ]]; then
  echo "down.sh: --project is required" >&2
  usage >&2
  exit 2
fi

for cmd in gcloud terraform jq; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "down.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -r "${HOME}/.gcp-service-account.json" ]]; then
  export GOOGLE_APPLICATION_CREDENTIALS="${HOME}/.gcp-service-account.json"
elif [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -z "${GOOGLE_OAUTH_ACCESS_TOKEN:-}" && ! -r "${HOME}/.config/gcloud/application_default_credentials.json" ]]; then
  GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
  export GOOGLE_OAUTH_ACCESS_TOKEN
fi

log() {
  printf '[registry-sandbox/down] %s\n' "$*"
}

tmp_work_dir="$(mktemp -d)"
chmod 0700 "${tmp_work_dir}"
tmp_tfvars="${tmp_work_dir}/terraform.tfvars.json"
tmp_images="${tmp_work_dir}/pushed-images.txt"
tmp_override=""
cleanup() {
  rm -rf "${tmp_work_dir}"
  if [[ -n "${tmp_override}" ]]; then
    rm -f "${tmp_override}"
  fi
}
trap cleanup EXIT

have_state=1
if [[ "${LOCAL_STATE}" -eq 1 ]]; then
  tmp_override="${SCRIPT_DIR}/backend_override.tf"
  cat > "${tmp_override}" <<'EOF'
terraform {
  backend "local" {}
}
EOF
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false
else
  if gcloud storage buckets describe "gs://${STATE_BUCKET}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud storage cp "gs://${STATE_BUCKET}/pushed-images.txt" "${tmp_images}" 2>/dev/null || true
    terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false \
      -backend-config="bucket=${STATE_BUCKET}" \
      -backend-config="prefix=registry-sandbox/${NAME_PREFIX}"
  else
    log "State bucket gs://${STATE_BUCKET} does not exist; skipping terraform destroy."
    have_state=0
  fi
fi

if [[ "${have_state}" -eq 1 ]]; then
  jq -n \
    --arg project_id "${PROJECT_ID}" \
    --arg region "${REGION}" \
    --arg zone "${ZONE}" \
    --arg name_prefix "${NAME_PREFIX}" \
    '{
      project_id: $project_id,
      region: $region,
      zone: $zone,
      name_prefix: $name_prefix,
      registry_image: "unused-during-destroy",
      admin_email: "destroy@example.com"
    }' > "${tmp_tfvars}"

  log "Destroying Terraform-managed sandbox resources in ${PROJECT_ID}..."
  terraform -chdir="${SCRIPT_DIR}" destroy -auto-approve -input=false -var-file="${tmp_tfvars}"
fi

# Ensure all enabled Cloud KMS signing key versions are scheduled for destruction
# (GCP retains the empty KeyRing/CryptoKey metadata shell at zero cost).
kr_name="${NAME_PREFIX}-kr"
key_name="${NAME_PREFIX}-jwt"
if gcloud kms keys describe "${key_name}" --keyring="${kr_name}" --location="${REGION}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
  mapfile -t enabled_vers < <(gcloud kms keys versions list \
    --key="${key_name}" \
    --keyring="${kr_name}" \
    --location="${REGION}" \
    --project="${PROJECT_ID}" \
    --filter="state=ENABLED" \
    --format="value(name)")
  for ver in "${enabled_vers[@]}"; do
    [[ -z "${ver}" ]] && continue
    ver_num="${ver##*/}"
    log "Scheduling KMS key version ${ver_num} for destruction..."
    gcloud kms keys versions destroy "${ver_num}" \
      --key="${key_name}" \
      --keyring="${kr_name}" \
      --location="${REGION}" \
      --project="${PROJECT_ID}" \
      --quiet || true
  done
fi

if [[ "${KEEP_IMAGES}" -eq 0 && -s "${tmp_images}" ]]; then
  while IFS= read -r img; do
    [[ -z "${img}" ]] && continue
    log "Deleting pushed sandbox image ${img}..."
    gcloud artifacts docker images delete "${img}" --delete-tags --quiet || true
  done < "${tmp_images}"
fi

if [[ "${LOCAL_STATE}" -eq 0 && "${KEEP_STATE_BUCKET}" -eq 0 ]]; then
  if gcloud storage buckets describe "gs://${STATE_BUCKET}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    log "Removing GCS state bucket gs://${STATE_BUCKET}..."
    gcloud storage rm -r "gs://${STATE_BUCKET}" --quiet
  fi
fi

rm -rf "${SCRIPT_DIR}/.terraform" "${SCRIPT_DIR}/.terraform.lock.hcl"
log "Teardown complete."
