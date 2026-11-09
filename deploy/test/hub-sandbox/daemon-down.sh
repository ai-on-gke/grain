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

# Tear down one (or all) disposable test `grain daemon` GCE VM(s) created by
# `daemon-up.sh`: removes the connected entry from the sandbox registry, deletes
# the GCE VM and its boot disk, cleans up the IAP SSH firewall rule when no test
# daemons remain, and deletes pushed `grain-daemon` image tags.

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-}"
ENTRY_NAME="${ENTRY_NAME:-sandbox-daemon}"
ENTRY_SET=0
ALL_DAEMONS=0
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
ZONE="${ZONE:-us-central1-a}"
STATE_BUCKET="${STATE_BUCKET:-}"
LOCAL_STATE=0
KEEP_ENTRY=0
KEEP_IMAGES=0

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/hub-sandbox/daemon-down.sh --project <gcp-project-id> [options]

Options:
  --project <id>           GCP sandbox project ID (required)
  --entry <name>           Connected entry name to tear down (default: sandbox-daemon)
  --all                    Tear down all test daemon VMs matching --prefix
  --prefix <prefix>        Sandbox registry resource prefix (default: grain-reg)
  --zone <zone>            GCP zone for the test daemon VM (default: us-central1-a)
  --state-bucket <bucket>  GCS bucket holding sandbox manifests (default: <project>-<prefix>-tfstate)
  --local-state            Skip reading/cleaning GCS image manifests
  --keep-entry             Leave the connected entry row in the sandbox registry database
  --keep-images            Do not delete grain-daemon image tags pushed by daemon-up.sh
  -h, --help               Show this help text
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "daemon-down.sh: option '$2' requires an argument" >&2
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
    --entry)
      require_arg "$#" "$1"
      ENTRY_NAME="$2"
      ENTRY_SET=1
      shift 2
      ;;
    --all)
      ALL_DAEMONS=1
      shift
      ;;
    --prefix)
      require_arg "$#" "$1"
      NAME_PREFIX="$2"
      shift 2
      ;;
    --zone)
      require_arg "$#" "$1"
      ZONE="$2"
      shift 2
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
    --keep-entry)
      KEEP_ENTRY=1
      shift
      ;;
    --keep-images)
      KEEP_IMAGES=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "daemon-down.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" ]]; then
  echo "daemon-down.sh: --project is required" >&2
  usage >&2
  exit 2
fi

if [[ "${ALL_DAEMONS}" -eq 1 && "${ENTRY_SET}" -eq 1 ]]; then
  echo "daemon-down.sh: --all and --entry cannot be used together" >&2
  exit 2
fi

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "daemon-down.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "daemon-down.sh: --prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$" >&2
  exit 2
fi

if ! [[ "${ZONE}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "daemon-down.sh: invalid --zone '${ZONE}'" >&2
  exit 2
fi

if [[ "${ALL_DAEMONS}" -eq 0 ]] && ! [[ "${ENTRY_NAME}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
  echo "daemon-down.sh: invalid --entry '${ENTRY_NAME}'" >&2
  exit 2
fi

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if ! [[ "${STATE_BUCKET}" =~ ^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$ ]]; then
  echo "daemon-down.sh: invalid --state-bucket '${STATE_BUCKET}'" >&2
  exit 2
fi

for cmd in gcloud jq base64; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "daemon-down.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

log() {
  printf '[hub-sandbox/daemon-down] %s\n' "$*"
}

tmp_dir="$(mktemp -d)"
chmod 0700 "${tmp_dir}"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

owns_state_bucket=0
if [[ "${LOCAL_STATE}" -eq 0 ]]; then
  project_num="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
  if ! [[ "${project_num}" =~ ^[0-9]+$ ]]; then
    echo "daemon-down.sh: failed to resolve numeric projectNumber for '${PROJECT_ID}'" >&2
    exit 1
  fi
  desc_err=""
  if bucket_proj_num="$(gcloud storage buckets describe "gs://${STATE_BUCKET}" --project="${PROJECT_ID}" --raw --format='value(projectNumber)' 2>&1)"; then
    if [[ "${bucket_proj_num}" != "${project_num}" ]]; then
      echo "daemon-down.sh: refusing to touch gs://${STATE_BUCKET}: bucket belongs to project number ${bucket_proj_num}, not ${PROJECT_ID} (${project_num})" >&2
      exit 1
    fi
    owns_state_bucket=1
  else
    desc_err="${bucket_proj_num}"
    if ! [[ "${desc_err}" =~ ([Nn]ot[[:space:]]?[Ff]ound|404) ]]; then
      echo "daemon-down.sh: failed to inspect state bucket gs://${STATE_BUCKET}: ${desc_err}" >&2
      exit 1
    fi
  fi
fi

target_entries=()
target_vms=()

if [[ "${ALL_DAEMONS}" -eq 1 ]]; then
  mapfile -t found_rows < <(gcloud compute instances list \
    --project="${PROJECT_ID}" \
    --filter="labels.grain-sandbox-role=test-daemon AND labels.grain-sandbox-prefix=${NAME_PREFIX}" \
    --format="csv[no-heading](name,zone.basename(),labels.grain-sandbox-entry)")
  for row in "${found_rows[@]}"; do
    [[ -z "${row}" ]] && continue
    IFS=',' read -r vname vzone ventry <<<"${row}"
    if [[ -n "${vname}" && -n "${vzone}" ]]; then
      target_vms+=("${vname}:${vzone}")
    fi
    if [[ "${ventry}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
      target_entries+=("${ventry}")
    fi
  done
else
  target_entries+=("${ENTRY_NAME}")
  vm_candidate="${NAME_PREFIX}-daemon-${ENTRY_NAME}"
  if gcloud compute instances describe "${vm_candidate}" --project="${PROJECT_ID}" --zone="${ZONE}" >/dev/null 2>&1; then
    target_vms+=("${vm_candidate}:${ZONE}")
  fi
fi

for vm_spec in "${target_vms[@]}"; do
  vname="${vm_spec%%:*}"
  vzone="${vm_spec#*:}"
  log "Deleting test daemon GCE VM ${vname} (${vzone})..."
  gcloud compute instances delete "${vname}" \
    --project="${PROJECT_ID}" \
    --zone="${vzone}" \
    --delete-disks=all \
    --quiet
done

if [[ "${KEEP_ENTRY}" -eq 0 && "${#target_entries[@]}" -gt 0 ]]; then
  reg_vm_name="${NAME_PREFIX}-vm"
  reg_status="$(gcloud compute instances describe "${reg_vm_name}" --project="${PROJECT_ID}" --zone="${ZONE}" --format='value(status)' 2>/dev/null || true)"
  if [[ "${reg_status}" == "RUNNING" ]]; then
    entries_b64="$(printf '%s\n' "${target_entries[@]}" | jq -R . | jq -s 'unique' | base64 | tr -d '\n')"
    log "Removing connected entry record(s) (${target_entries[*]}) from ${reg_vm_name}..."
    gcloud compute ssh "${reg_vm_name}" \
      --project="${PROJECT_ID}" \
      --zone="${ZONE}" \
      --tunnel-through-iap \
      --quiet \
      --command="sudo bash -s" <<EOF || log "Warning: could not remove entry from ${reg_vm_name} over IAP SSH (continuing)."
set -euo pipefail
entries_json="\$(printf '%s' '${entries_b64}' | base64 -d)"
mapfile -t names < <(jq -r '.[]' <<<"\${entries_json}")
for ename in "\${names[@]}"; do
  [[ -z "\${ename}" ]] && continue
  docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
    -v entry_name="\${ename}" >/dev/null <<'SQL'
BEGIN;
DELETE FROM registry.entries
WHERE name = :'entry_name' AND kind = 'connected';
SELECT pg_notify('registry_entries', :'entry_name');
SELECT pg_notify('registry_catalog', :'entry_name');
SELECT pg_notify('registry_tunnels', :'entry_name');
COMMIT;
SQL
done
EOF
  fi
fi

remaining_count="$(gcloud compute instances list \
  --project="${PROJECT_ID}" \
  --filter="labels.grain-sandbox-role=test-daemon AND labels.grain-sandbox-prefix=${NAME_PREFIX}" \
  --format="value(name)" | wc -l | tr -d ' ')"

if [[ "${remaining_count}" -eq 0 ]]; then
  fw_name="${NAME_PREFIX}-allow-daemon-iap-ssh"
  if gcloud compute firewall-rules describe "${fw_name}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    log "Removing IAP SSH firewall rule ${fw_name}..."
    gcloud compute firewall-rules delete "${fw_name}" --project="${PROJECT_ID}" --quiet
  fi

  if [[ "${KEEP_IMAGES}" -eq 0 && "${LOCAL_STATE}" -eq 0 && "${owns_state_bucket}" -eq 1 ]]; then
    tmp_images="${tmp_dir}/pushed-daemon-images.txt"
    if gcloud storage cp "gs://${STATE_BUCKET}/pushed-daemon-images.txt" "${tmp_images}" 2>/dev/null; then
      while IFS= read -r img; do
        [[ -z "${img}" ]] && continue
        if [[ "${img}" == -* || "${img}" != *-docker.pkg.dev/"${PROJECT_ID}"/* || "${img}" =~ [[:space:]] ]]; then
          log "Skipping unexpected image reference in pushed-daemon-images.txt: ${img}"
          continue
        fi
        log "Deleting pushed test daemon image ${img}..."
        gcloud artifacts docker images delete "${img}" --delete-tags --quiet || true
      done < "${tmp_images}"
      gcloud storage rm "gs://${STATE_BUCKET}/pushed-daemon-images.txt" --quiet 2>/dev/null || true
    fi
  fi
fi

log "Test daemon teardown complete."
