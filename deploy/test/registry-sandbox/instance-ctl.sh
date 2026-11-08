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

# Non-interactive helper for creating, inspecting, and deleting hosted GCE VM
# entries (`kind = instance`) on a sandbox `grain-registry` over IAP SSH when
# running from a service account (such as a CI/sandbox verifier) without an
# interactive browser OAuth session. Human users can create and resize hosted
# VMs directly from the registry UI (`/new?kind=instance`) or `grain registry
# create --shape n2-standard-4 --disk-gb 150`.

set -euo pipefail

ACTION=""
PROJECT_ID="${PROJECT_ID:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
ZONE="${ZONE:-us-central1-a}"
ENTRY_NAME=""
OWNER_EMAIL=""
SHAPE="n2-standard-4"
DISK_GB=0
DISPLAY_NAME=""
PURPOSE=""
ALLOW_VERIFIER_SA=0
PURGE=0
WAIT=1
TIMEOUT_SECS=600

ALLOW_PRINCIPALS=()

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/instance-ctl.sh <create|status|delete> [options]

Actions:
  create                     Create a hosted GCE VM entry (kind = instance) and wait for running
  status                     Print catalog and placement JSON for a hosted VM entry
  delete                     Soft-delete (or --purge) a hosted VM entry and wait for teardown

Options:
  --project <id>             GCP sandbox project ID (required)
  --entry <name>             Entry subdomain name, 3-32 lowercase [a-z0-9-] (required)
  --owner <email>            Owner email for create (required on create)
  --shape <name>             VM shape on create (default: n2-standard-4)
  --disk-gb <gb>             Custom boot disk size in GiB on create, 20-4000 (0 = shape default)
  --display-name <text>      Optional human-readable display name on create
  --purpose <text>           Optional purpose description on create
  --allow-principal <p>      Grant routed access to user:<email>, group:<email>, or serviceAccount:<email>
  --allow-verifier-sa        Admit the sandbox verifier SA to the entry's registry access_list
  --purge                    With delete: immediately purge VM, database, and service account
  --no-wait                  Return immediately without waiting for background reconciliation
  --timeout <secs>           Max seconds to wait for reconciliation (default: 600)
  --prefix <prefix>          Sandbox resource prefix (default: grain-reg)
  --zone <zone>              Registry VM zone (default: us-central1-a)
  -h, --help                 Show this help text
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "instance-ctl.sh: option '$2' requires an argument" >&2
    usage >&2
    exit 2
  fi
}

if [[ $# -gt 0 ]]; then
  case "$1" in
    create|status|delete)
      ACTION="$1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
  esac
fi

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
      shift 2
      ;;
    --owner)
      require_arg "$#" "$1"
      OWNER_EMAIL="$2"
      shift 2
      ;;
    --shape)
      require_arg "$#" "$1"
      SHAPE="$2"
      shift 2
      ;;
    --disk-gb)
      require_arg "$#" "$1"
      DISK_GB="$2"
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
    --allow-principal)
      require_arg "$#" "$1"
      ALLOW_PRINCIPALS+=("$2")
      shift 2
      ;;
    --allow-verifier-sa)
      ALLOW_VERIFIER_SA=1
      shift
      ;;
    --purge)
      PURGE=1
      shift
      ;;
    --no-wait)
      WAIT=0
      shift
      ;;
    --timeout)
      require_arg "$#" "$1"
      TIMEOUT_SECS="$2"
      shift 2
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
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "instance-ctl.sh: unknown option or action: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${ACTION}" ]]; then
  echo "instance-ctl.sh: action (create, status, or delete) is required" >&2
  usage >&2
  exit 2
fi

if [[ -z "${PROJECT_ID}" || -z "${ENTRY_NAME}" ]]; then
  echo "instance-ctl.sh: --project and --entry are required" >&2
  usage >&2
  exit 2
fi

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "instance-ctl.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "instance-ctl.sh: invalid --prefix '${NAME_PREFIX}'" >&2
  exit 2
fi

if ! [[ "${ZONE}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "instance-ctl.sh: invalid --zone '${ZONE}'" >&2
  exit 2
fi

if ! [[ "${ENTRY_NAME}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
  echo "instance-ctl.sh: invalid --entry '${ENTRY_NAME}'" >&2
  exit 2
fi

if ! [[ "${SHAPE}" =~ ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ ]]; then
  echo "instance-ctl.sh: invalid --shape '${SHAPE}'" >&2
  exit 2
fi

if ! [[ "${DISK_GB}" =~ ^[0-9]+$ ]]; then
  echo "instance-ctl.sh: invalid --disk-gb '${DISK_GB}' (must be 0 or integer 20-4000)" >&2
  exit 2
fi
if [[ "${DISK_GB}" -ne 0 ]] && (( DISK_GB < 20 || DISK_GB > 4000 )); then
  echo "instance-ctl.sh: --disk-gb '${DISK_GB}' out of range (must be between 20 and 4000)" >&2
  exit 2
fi

if ! [[ "${TIMEOUT_SECS}" =~ ^[0-9]+$ ]] || (( TIMEOUT_SECS < 1 )); then
  echo "instance-ctl.sh: invalid --timeout '${TIMEOUT_SECS}'" >&2
  exit 2
fi

if [[ "${ACTION}" == "create" ]]; then
  if [[ -z "${OWNER_EMAIL}" ]] || ! [[ "${OWNER_EMAIL}" =~ ^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
    echo "instance-ctl.sh: create requires a valid --owner email" >&2
    exit 2
  fi
fi

for p in "${ALLOW_PRINCIPALS[@]}"; do
  if ! [[ "${p}" =~ ^(user|group|serviceAccount):[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
    echo "instance-ctl.sh: invalid --allow-principal '${p}'" >&2
    exit 2
  fi
done

for cmd in gcloud jq base64; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "instance-ctl.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

vm_name="${NAME_PREFIX}-vm"
default_ver="serviceAccount:${NAME_PREFIX}-ver@${PROJECT_ID}.iam.gserviceaccount.com"

access_list_json="[]"
if [[ "${#ALLOW_PRINCIPALS[@]}" -gt 0 ]]; then
  access_list_json="$(printf '%s\n' "${ALLOW_PRINCIPALS[@]}" | jq -R . | jq -s .)"
fi

payload_json="$(jq -n \
  --arg action "${ACTION}" \
  --arg entry_name "${ENTRY_NAME}" \
  --arg owner_email "${OWNER_EMAIL}" \
  --arg shape "${SHAPE}" \
  --arg display_name "${DISPLAY_NAME}" \
  --arg purpose "${PURPOSE}" \
  --arg default_ver "${default_ver}" \
  --argjson disk_gb "${DISK_GB}" \
  --argjson allow_verifier "${ALLOW_VERIFIER_SA}" \
  --argjson purge "${PURGE}" \
  --argjson wait_flag "${WAIT}" \
  --argjson timeout_secs "${TIMEOUT_SECS}" \
  --argjson access_list "${access_list_json}" \
  '{
    action: $action,
    entry_name: $entry_name,
    owner_email: ($owner_email | ascii_downcase),
    shape: $shape,
    disk_gb: $disk_gb,
    display_name: $display_name,
    purpose: $purpose,
    default_ver: $default_ver,
    allow_verifier: $allow_verifier,
    purge: $purge,
    wait_flag: $wait_flag,
    timeout_secs: $timeout_secs,
    access_list: $access_list
  }')"

payload_b64="$(printf '%s' "${payload_json}" | base64 | tr -d '\n')"

gcloud compute ssh "${vm_name}" \
  --project="${PROJECT_ID}" \
  --zone="${ZONE}" \
  --tunnel-through-iap \
  --quiet \
  --command="sudo bash -s" <<EOF
set -euo pipefail
payload="\$(printf '%s' '${payload_b64}' | base64 -d)"
action="\$(jq -r '.action' <<<"\${payload}")"
entry_name="\$(jq -r '.entry_name' <<<"\${payload}")"
owner_email="\$(jq -r '.owner_email' <<<"\${payload}")"
shape="\$(jq -r '.shape' <<<"\${payload}")"
disk_gb="\$(jq -r '.disk_gb' <<<"\${payload}")"
display_name="\$(jq -r '.display_name' <<<"\${payload}")"
purpose="\$(jq -r '.purpose' <<<"\${payload}")"
allow_verifier="\$(jq -r '.allow_verifier' <<<"\${payload}")"
purge_flag="\$(jq -r '.purge' <<<"\${payload}")"
wait_flag="\$(jq -r '.wait_flag' <<<"\${payload}")"
timeout_secs="\$(jq -r '.timeout_secs' <<<"\${payload}")"
default_ver="\$(jq -r '.default_ver' <<<"\${payload}")"

query_entry_json() {
  docker exec -i grain-registry-postgres psql -t -A -U grain -d grain_registry \
    -v entry_name="\${entry_name}" <<'SQL'
SELECT COALESCE((
  SELECT row_to_json(t)::text
  FROM (
    SELECT e.name, e.kind, e.owner_email, e.lifecycle_state, e.status_detail,
           e.shape, COALESCE(e.boot_disk_gb, s.boot_disk_gb, 0) AS boot_disk_gb,
           s.machine_type, e.zone, e.gce_instance_id, host(e.internal_ip) AS internal_ip,
           e.backend_url, e.service_account, e.sql_instance, e.db_name,
           e.grain_version, e.deleted_at, e.purged_at
    FROM registry.entries e
    LEFT JOIN registry.shapes s ON s.name = e.shape
    WHERE e.name = :'entry_name'
    ORDER BY e.created_at DESC
    LIMIT 1
  ) t
), '{}');
SQL
}

if [[ "\${action}" == "status" ]]; then
  query_entry_json
  exit 0
fi

if [[ "\${action}" == "create" ]]; then
  meta_cfg="\$(curl -fsSL -H 'Metadata-Flavor: Google' 'http://metadata.google.internal/computeMetadata/v1/instance/attributes/grain-registry-config' 2>/dev/null || echo '{}')"
  compute_zone="\$(jq -r '(.compute_zones // ["us-central1-a"])[0]' <<<"\${meta_cfg}")"
  access_list="\$(jq -cn --argjson base "\$(jq -c '.access_list' <<<"\${payload}")" --argjson meta "\${meta_cfg}" --argjson av "\${allow_verifier}" --arg dv "\${default_ver}" '
    if \$av == 1 then
      (\$base + (if ((\$meta.service_principals // []) | length) > 0 then \$meta.service_principals else [\$dv] end)) | unique
    else
      \$base | unique
    end
  ')"
  visibility="owner"
  if [[ "\$(jq -r 'length' <<<"\${access_list}")" -gt 0 ]]; then
    visibility="shared"
  fi
  entry_uuid="\$(cat /proc/sys/kernel/random/uuid)"

  shape_ok="\$(docker exec -i grain-registry-postgres psql -t -A -U grain -d grain_registry -v shape="\${shape}" -c "SELECT count(*) FROM registry.shapes WHERE name = :'shape';")"
  if [[ "\${shape_ok}" != "1" ]]; then
    echo "instance-ctl.sh: unknown shape '\${shape}'" >&2
    exit 1
  fi

  docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
    -v entry_uuid="\${entry_uuid}" \
    -v entry_name="\${entry_name}" \
    -v owner_email="\${owner_email}" \
    -v shape="\${shape}" \
    -v disk_gb="\${disk_gb}" \
    -v compute_zone="\${compute_zone}" \
    -v display_name="\${display_name}" \
    -v purpose="\${purpose}" \
    -v visibility="\${visibility}" \
    -v access_list="\${access_list}" >/dev/null <<'SQL'
BEGIN;

INSERT INTO registry.owners (email)
VALUES (lower(:'owner_email'))
ON CONFLICT (email) DO NOTHING;

INSERT INTO registry.entries (
  id, name, kind, owner_email, listed, visibility, access_list,
  display_name, purpose, tags, lifecycle_state, version,
  shape, boot_disk_gb, zone, generation, created_at, updated_at
)
VALUES (
  (:'entry_uuid')::uuid, :'entry_name', 'instance', lower(:'owner_email'),
  TRUE, :'visibility', (:'access_list')::jsonb,
  :'display_name', :'purpose', '[]'::jsonb, 'creating', 1,
  :'shape', NULLIF((:'disk_gb')::int, 0), :'compute_zone', 1, now(), now()
);

COMMIT;
SQL

  if [[ "\${wait_flag}" -eq 0 ]]; then
    query_entry_json
    exit 0
  fi

  deadline=\$((SECONDS + timeout_secs))
  while (( SECONDS < deadline )); do
    cur="\$(query_entry_json)"
    state="\$(jq -r '.lifecycle_state // empty' <<<"\${cur}")"
    if [[ "\${state}" == "running" ]]; then
      printf '%s\n' "\${cur}"
      exit 0
    fi
    if [[ "\${state}" == "failed" ]]; then
      printf '%s\n' "\${cur}" >&2
      exit 1
    fi
    sleep 5
  done
  echo "instance-ctl.sh: timed out waiting for '\${entry_name}' to reach running; latest state:" >&2
  query_entry_json >&2
  exit 1
fi

if [[ "\${action}" == "delete" ]]; then
  docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
    -v entry_name="\${entry_name}" \
    -v purge_flag="\${purge_flag}" >/dev/null <<'SQL'
UPDATE registry.entries
SET lifecycle_state = 'deleting',
    deleted_at      = COALESCE(deleted_at, now()),
    purge_after     = CASE WHEN (:'purge_flag')::int = 1 THEN now() ELSE COALESCE(purge_after, now() + interval '30 days') END,
    db_purge_after  = CASE WHEN (:'purge_flag')::int = 1 THEN now() ELSE COALESCE(db_purge_after, now() + interval '7 days') END,
    version         = version + 1,
    updated_at      = now()
WHERE name = :'entry_name' AND kind = 'instance' AND deleted_at IS NULL;
SQL

  if [[ "\${wait_flag}" -eq 0 ]]; then
    query_entry_json
    exit 0
  fi

  deadline=\$((SECONDS + timeout_secs))
  while (( SECONDS < deadline )); do
    cur="\$(query_entry_json)"
    if [[ "\${cur}" == "{}" ]]; then
      echo '{"deleted":true,"purged":true}'
      exit 0
    fi
    state="\$(jq -r '.lifecycle_state // empty' <<<"\${cur}")"
    purged="\$(jq -r '.purged_at // empty' <<<"\${cur}")"
    if [[ "\${purge_flag}" -eq 1 ]]; then
      if [[ -n "\${purged}" && "\${purged}" != "null" ]]; then
        printf '%s\n' "\${cur}"
        exit 0
      fi
    elif [[ "\${state}" == "deleted" ]]; then
      printf '%s\n' "\${cur}"
      exit 0
    fi
    sleep 5
  done
  echo "instance-ctl.sh: timed out waiting for '\${entry_name}' deletion; latest state:" >&2
  query_entry_json >&2
  exit 1
fi
EOF
