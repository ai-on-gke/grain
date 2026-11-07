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

# Idempotently create or update a `connected` entry in the sandbox registry and
# mint a fresh `grc_...` registration credential, storing only its SHA-256
# digest in `registry.entry_credentials` and notifying running router caches.

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
ZONE="${ZONE:-us-central1-a}"
ENTRY_NAME=""
OWNER_EMAIL=""
DISPLAY_NAME=""
PURPOSE=""
HOST_KIND="workstation"
REGISTRY_HOST=""
INSTANCE_DOMAIN=""
TOKEN_FILE=""
ALLOW_VERIFIER_SA=0
REVOKE_PREVIOUS=0
ALLOW_PRINCIPALS=()

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/register-entry.sh \
    --project <gcp-project-id> \
    --entry <entry-name> \
    --owner <owner@example.com> \
    [options]

Options:
  --project <id>               GCP sandbox project ID (required)
  --entry <name>               Entry name, e.g. my-daemon (required)
  --owner <email>              Owner email address (required)
  --display-name <text>        Optional catalog display name
  --purpose <text>             Optional catalog purpose description
  --host-kind <kind>           workstation (default), kubernetes, or other
  --allow-principal <p>        Additional principal for entry access_list, e.g. user:peer@example.com (repeatable)
  --allow-verifier-sa          Also allow <prefix>-ver@<project>.iam.gserviceaccount.com on entry access_list
  --revoke-previous            Revoke existing unrevoked credentials for this entry when minting the new token
  --prefix <prefix>            Resource name prefix (default: grain-reg)
  --zone <zone>                GCE VM zone (default: us-central1-a)
  --registry-host <host>       Optional registry hostname (auto-discovered from VM metadata if omitted)
  --instance-domain <domain>   Optional instance domain (auto-discovered from VM metadata if omitted)
  --token-file <path>          Write the minted grc_... token to <path> (0600) instead of stdout
  -h, --help                   Show this help text
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "register-entry.sh: option '$2' requires an argument" >&2
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
      shift 2
      ;;
    --owner)
      require_arg "$#" "$1"
      OWNER_EMAIL="$2"
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
    --host-kind)
      require_arg "$#" "$1"
      HOST_KIND="$2"
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
    --revoke-previous)
      REVOKE_PREVIOUS=1
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
    --registry-host)
      require_arg "$#" "$1"
      REGISTRY_HOST="$2"
      shift 2
      ;;
    --instance-domain)
      require_arg "$#" "$1"
      INSTANCE_DOMAIN="$2"
      shift 2
      ;;
    --token-file)
      require_arg "$#" "$1"
      TOKEN_FILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "register-entry.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" || -z "${ENTRY_NAME}" || -z "${OWNER_EMAIL}" ]]; then
  echo "register-entry.sh: --project, --entry, and --owner are required" >&2
  usage >&2
  exit 2
fi

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "register-entry.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "register-entry.sh: --prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$" >&2
  exit 2
fi

if ! [[ "${ZONE}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "register-entry.sh: invalid --zone '${ZONE}'" >&2
  exit 2
fi

if ! [[ "${ENTRY_NAME}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
  echo "register-entry.sh: invalid entry name '${ENTRY_NAME}' (must match ^[a-z][a-z0-9-]{1,30}[a-z0-9]$)" >&2
  exit 2
fi

if ! [[ "${OWNER_EMAIL}" =~ ^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
  echo "register-entry.sh: invalid --owner email '${OWNER_EMAIL}'" >&2
  exit 2
fi

for p in "${ALLOW_PRINCIPALS[@]}"; do
  if ! [[ "${p}" =~ ^(user|group|serviceAccount):[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
    echo "register-entry.sh: invalid --allow-principal '${p}' (expected user:<email>, group:<email>, or serviceAccount:<email>)" >&2
    exit 2
  fi
done

if [[ -n "${REGISTRY_HOST}" ]] && ! [[ "${REGISTRY_HOST}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "register-entry.sh: invalid --registry-host '${REGISTRY_HOST}'" >&2
  exit 2
fi

if [[ -n "${INSTANCE_DOMAIN}" ]] && ! [[ "${INSTANCE_DOMAIN}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "register-entry.sh: invalid --instance-domain '${INSTANCE_DOMAIN}'" >&2
  exit 2
fi

case "${HOST_KIND}" in
  workstation|kubernetes|other) ;;
  *)
    echo "register-entry.sh: invalid --host-kind '${HOST_KIND}' (must be workstation, kubernetes, or other)" >&2
    exit 2
    ;;
esac

for cmd in gcloud jq openssl sha256sum base64; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "register-entry.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

access_list_json="[]"
if [[ "${#ALLOW_PRINCIPALS[@]}" -gt 0 ]]; then
  access_list_json="$(printf '%s\n' "${ALLOW_PRINCIPALS[@]}" | jq -R . | jq -s 'unique')"
fi

token="grc_$(openssl rand -hex 32)"
token_sha256_hex="$(printf '%s' "${token}" | sha256sum | awk '{print $1}')"
vm_name="${NAME_PREFIX}-vm"
default_ver_principal="serviceAccount:${NAME_PREFIX}-ver@${PROJECT_ID}.iam.gserviceaccount.com"

payload_json="$(jq -n \
  --arg entry_name "${ENTRY_NAME}" \
  --arg owner_email "${OWNER_EMAIL}" \
  --arg display_name "${DISPLAY_NAME}" \
  --arg purpose "${PURPOSE}" \
  --arg host_kind "${HOST_KIND}" \
  --arg default_ver "${default_ver_principal}" \
  --argjson allow_verifier "${ALLOW_VERIFIER_SA}" \
  --argjson revoke_previous "${REVOKE_PREVIOUS}" \
  --argjson access_list "${access_list_json}" \
  --arg token_sha256_hex "${token_sha256_hex}" \
  '{
    entry_name: $entry_name,
    owner_email: ($owner_email | ascii_downcase),
    display_name: $display_name,
    purpose: $purpose,
    host_kind: $host_kind,
    default_ver: $default_ver,
    allow_verifier: $allow_verifier,
    revoke_previous: $revoke_previous,
    access_list: $access_list,
    token_sha256_hex: $token_sha256_hex
  }')"

# The payload carries operator-supplied text (--display-name, --purpose,
# owners, principals) into a root shell on the VM. Ship it base64-encoded so a
# quote in any of those values cannot break out of the remote script.
payload_b64="$(printf '%s' "${payload_json}" | base64 | tr -d '\n')"

remote_out="$(gcloud compute ssh "${vm_name}" \
  --project="${PROJECT_ID}" \
  --zone="${ZONE}" \
  --tunnel-through-iap \
  --quiet \
  --command="sudo bash -s" <<EOF
set -euo pipefail
payload="\$(printf '%s' '${payload_b64}' | base64 -d)"
entry_name="\$(jq -r '.entry_name' <<<"\${payload}")"
owner_email="\$(jq -r '.owner_email' <<<"\${payload}")"
display_name="\$(jq -r '.display_name' <<<"\${payload}")"
purpose="\$(jq -r '.purpose' <<<"\${payload}")"
host_kind="\$(jq -r '.host_kind' <<<"\${payload}")"
allow_verifier="\$(jq -r '.allow_verifier' <<<"\${payload}")"
revoke_previous="\$(jq -r '.revoke_previous' <<<"\${payload}")"
default_ver="\$(jq -r '.default_ver' <<<"\${payload}")"
token_sha256_hex="\$(jq -r '.token_sha256_hex' <<<"\${payload}")"
meta_cfg="\$(curl -fsSL -H 'Metadata-Flavor: Google' 'http://metadata.google.internal/computeMetadata/v1/instance/attributes/grain-registry-config' 2>/dev/null || echo '{}')"
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
cred_uuid="\$(cat /proc/sys/kernel/random/uuid)"

docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry \
  -v entry_uuid="\${entry_uuid}" \
  -v cred_uuid="\${cred_uuid}" \
  -v entry_name="\${entry_name}" \
  -v owner_email="\${owner_email}" \
  -v display_name="\${display_name}" \
  -v purpose="\${purpose}" \
  -v host_kind="\${host_kind}" \
  -v visibility="\${visibility}" \
  -v access_list="\${access_list}" \
  -v revoke_previous="\${revoke_previous}" \
  -v token_sha256_hex="\${token_sha256_hex}" >/dev/null <<'SQL'
BEGIN;

INSERT INTO registry.owners (email)
VALUES (lower(:'owner_email'))
ON CONFLICT (email) DO NOTHING;

WITH existing AS (
  SELECT id FROM registry.entries
  WHERE name = :'entry_name' AND deleted_at IS NULL
  FOR UPDATE
),
updated AS (
  UPDATE registry.entries
  SET owner_email    = lower(:'owner_email'),
      visibility     = :'visibility',
      access_list    = (:'access_list')::jsonb,
      display_name   = CASE WHEN :'display_name' <> '' THEN :'display_name' ELSE display_name END,
      purpose        = CASE WHEN :'purpose' <> '' THEN :'purpose' ELSE purpose END,
      host_kind      = :'host_kind',
      lifecycle_state = 'running',
      version        = version + 1,
      updated_at     = now()
  WHERE id IN (SELECT id FROM existing)
  RETURNING id
),
inserted AS (
  INSERT INTO registry.entries (
    id, name, kind, owner_email, listed, visibility, access_list,
    display_name, purpose, tags, lifecycle_state, version, host_kind,
    created_at, updated_at
  )
  SELECT
    (:'entry_uuid')::uuid, :'entry_name', 'connected', lower(:'owner_email'),
    TRUE, :'visibility', (:'access_list')::jsonb,
    :'display_name', :'purpose', '[]'::jsonb, 'running', 1, :'host_kind',
    now(), now()
  WHERE NOT EXISTS (SELECT 1 FROM existing)
  RETURNING id
),
target AS (
  SELECT id FROM updated
  UNION ALL
  SELECT id FROM inserted
)
INSERT INTO registry.entry_credentials (
  id, entry_id, token_sha256, label, created_by, created_at
)
SELECT
  (:'cred_uuid')::uuid, id, decode(:'token_sha256_hex', 'hex'),
  'register-entry.sh', lower(:'owner_email'), now()
FROM target;

WITH revoked AS (
  UPDATE registry.entry_credentials
  SET revoked_at = now()
  WHERE (:'revoke_previous')::int = 1
    AND entry_id = (SELECT id FROM registry.entries WHERE name = :'entry_name' AND deleted_at IS NULL)
    AND id <> (:'cred_uuid')::uuid
    AND revoked_at IS NULL
  RETURNING id, entry_id
),
kicked AS (
  DELETE FROM registry.tunnel_sessions
  WHERE entry_id IN (SELECT DISTINCT entry_id FROM revoked)
    AND (cred_id IS NULL OR cred_id IN (SELECT id FROM revoked))
  RETURNING entry_id
)
UPDATE registry.entries
SET last_disconnect_at     = now(),
    last_disconnect_reason = 'credential revoked',
    updated_at             = now()
WHERE id IN (SELECT DISTINCT entry_id FROM kicked);

SELECT pg_notify('registry_entries', :'entry_name');
SELECT pg_notify('registry_catalog', :'entry_name');
SELECT pg_notify('registry_tunnels', :'entry_name') WHERE (:'revoke_previous')::int = 1;

COMMIT;
SQL

cfg_json="\$(curl -fsSL -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/instance/attributes/grain-registry-config)"
jq -n \
  --arg registry_host "\$(jq -r '.registry_host' <<<"\${cfg_json}")" \
  --arg instance_domain "\$(jq -r '.instance_domain' <<<"\${cfg_json}")" \
  '{registry_host: \$registry_host, instance_domain: \$instance_domain}'
EOF
)"

if [[ -z "${REGISTRY_HOST}" ]]; then
  REGISTRY_HOST="$(jq -r '.registry_host // empty' <<<"${remote_out}")"
fi
if [[ -z "${INSTANCE_DOMAIN}" ]]; then
  INSTANCE_DOMAIN="$(jq -r '.instance_domain // empty' <<<"${remote_out}")"
fi

if ! [[ "${REGISTRY_HOST}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "register-entry.sh: invalid registry_host '${REGISTRY_HOST}'" >&2
  exit 1
fi
if ! [[ "${INSTANCE_DOMAIN}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "register-entry.sh: invalid instance_domain '${INSTANCE_DOMAIN}'" >&2
  exit 1
fi

registry_url="https://${REGISTRY_HOST}"
entry_url="https://${ENTRY_NAME}.${INSTANCE_DOMAIN}"
jwks_url="${registry_url}/.well-known/jwks.json"

if [[ -n "${TOKEN_FILE}" ]]; then
  umask 077
  printf '%s\n' "${token}" > "${TOKEN_FILE}"
  chmod 0600 "${TOKEN_FILE}"
  token_display="<written to ${TOKEN_FILE}>"
  quoted_token_file="$(printf '%q' "${TOKEN_FILE}")"
  reg_token_arg="\$(cat ${quoted_token_file})"
else
  token_display="${token}"
  reg_token_arg="${token}"
fi

allowed_principals_csv="user:${OWNER_EMAIL}"
for p in "${ALLOW_PRINCIPALS[@]}"; do
  if [[ "${p}" == user:* || "${p}" == group:* ]]; then
    allowed_principals_csv="${allowed_principals_csv},${p}"
  fi
done

cat <<INSTRUCTIONS
Registered connected entry '${ENTRY_NAME}' on ${registry_url}
  Entry URL:          ${entry_url}
  Owner:              ${OWNER_EMAIL}
  Registration token: ${token_display}

To connect a setup.sh-installed grain-daemon.service on the daemon host:
  1. Configure the registry assertion verifier + outbound tunnel in /etc/default/grain-daemon:
     sudo tee -a /etc/default/grain-daemon >/dev/null <<'DAEMON_EOF'
     GRAIN_EXTRA_DOCKER_RUN_ARGS="-e GRAIN_REGISTRY_CONNECT=true -e GRAIN_REGISTRY_ISSUER=grain-registry -e GRAIN_REGISTRY_AUDIENCE=${entry_url} -e GRAIN_REGISTRY_KEYS_URL=${jwks_url} -e GRAIN_ALLOWED_PRINCIPALS=${allowed_principals_csv}"
     DAEMON_EOF
     sudo systemctl restart grain-daemon.service

  2. Store the grc_... token and trigger the initial heartbeat + outbound tunnel:
     GRAIN_REGISTRY_TOKEN="${reg_token_arg}" grain registry register \\
       -registry-url "${registry_url}" \\
       "${ENTRY_NAME}"

  (If running 'grain daemon' directly without systemd/Docker, pass:
    -registry-connect \\
    -ui-registry-issuer=grain-registry \\
    -ui-registry-audience=${entry_url} \\
    -ui-registry-keys-url=${jwks_url} \\
    -ui-allowed-principals=${allowed_principals_csv})
INSTRUCTIONS
