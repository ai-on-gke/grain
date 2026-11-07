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

# Verify a live GCP sandbox grain-registry deployment end-to-end:
#   1. Public /healthz and /.well-known/jwks.json on the HTTPS Load Balancer
#   2. Unauthenticated front-door rejection by Cloud IAP
#   3. Programmatic IAP authentication via IAM Credentials signJwt (zero SA keys)
#   4. Catalog inspection via GET /api/directory (version, health/status, queue counts)
#   5. Repository route resolution via GET /api/route
#   6. End-to-end HTTP request routed through <entry>.<instance_domain> over the
#      connected daemon's outbound WebSocket mux tunnel

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PROJECT_ID="${PROJECT_ID:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
ZONE="${ZONE:-us-central1-a}"
STATE_BUCKET="${STATE_BUCKET:-}"
LOCAL_STATE=0
ENTRY_NAME=""
INSECURE_TLS=0

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/verify.sh --project <gcp-project-id> [options]

Options:
  --project <id>           GCP sandbox project ID (required)
  --prefix <prefix>        Resource name prefix (default: grain-reg)
  --zone <zone>            GCE VM zone (default: us-central1-a)
  --entry <name>           Specific connected entry name to verify (default: all connected entries)
  --state-bucket <bucket>  GCS bucket holding Terraform state (default: <project>-<prefix>-tfstate)
  --local-state            Read Terraform outputs from local state
  --insecure-tls           Pass -k to curl (use when tls_mode=self-signed or before managed cert is ACTIVE)
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
    --zone)
      ZONE="$2"
      shift 2
      ;;
    --entry)
      ENTRY_NAME="$2"
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
    --insecure-tls)
      INSECURE_TLS=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "verify.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" ]]; then
  echo "verify.sh: --project is required" >&2
  usage >&2
  exit 2
fi

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -r "${HOME}/.gcp-service-account.json" ]]; then
  export GOOGLE_APPLICATION_CREDENTIALS="${HOME}/.gcp-service-account.json"
elif [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -z "${GOOGLE_OAUTH_ACCESS_TOKEN:-}" && ! -r "${HOME}/.config/gcloud/application_default_credentials.json" ]]; then
  GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
  export GOOGLE_OAUTH_ACCESS_TOKEN
fi

tmp_dir="$(mktemp -d)"
tmp_override=""
cleanup() {
  rm -rf "${tmp_dir}"
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
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false >/dev/null
else
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false \
    -backend-config="bucket=${STATE_BUCKET}" \
    -backend-config="prefix=registry-sandbox/${NAME_PREFIX}" >/dev/null
fi

registry_host="$(terraform -chdir="${SCRIPT_DIR}" output -raw registry_host)"
instance_domain="$(terraform -chdir="${SCRIPT_DIR}" output -raw instance_domain)"
verifier_sa="$(terraform -chdir="${SCRIPT_DIR}" output -raw verifier_service_account)"
tls_mode="$(terraform -chdir="${SCRIPT_DIR}" output -raw tls_mode)"

if [[ "${tls_mode}" == "self-signed" ]]; then
  INSECURE_TLS=1
fi

curl_tls_flags=()
if [[ "${INSECURE_TLS}" -eq 1 ]]; then
  curl_tls_flags+=(-k)
fi

sign_iap_jwt() {
  local target_host="$1"
  local now exp claim_file jwt_file key_file key_email kid hdr_b64 pay_b64 sig_b64
  now="$(date +%s)"
  exp=$(( now + 300 ))

  # If running with a local service-account JSON key file matching verifier_sa
  # (such as grain's gcp#gcp-key capability), sign the RS256 IAP assertion
  # locally with openssl so roles/iam.serviceAccountTokenCreator is not needed.
  key_file="${GOOGLE_APPLICATION_CREDENTIALS:-}"
  if [[ -z "${key_file}" && -f "${HOME}/.gcp-service-account.json" ]]; then
    key_file="${HOME}/.gcp-service-account.json"
  fi
  if [[ -n "${key_file}" && -r "${key_file}" ]]; then
    key_email="$(jq -r '.client_email // empty' "${key_file}" 2>/dev/null || true)"
    if [[ "${key_email}" == "${verifier_sa}" ]]; then
      kid="$(jq -r '.private_key_id // empty' "${key_file}")"
      jq -r '.private_key' "${key_file}" > "${tmp_dir}/sa.pem"
      chmod 0600 "${tmp_dir}/sa.pem"
      hdr_b64="$(jq -cn --arg kid "${kid}" '{alg:"RS256",typ:"JWT"} + (if $kid != "" then {kid:$kid} else {} end)' | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')"
      pay_b64="$(jq -cn \
        --arg iss "${verifier_sa}" \
        --arg sub "${verifier_sa}" \
        --arg aud "https://${target_host}/*" \
        --argjson iat "${now}" \
        --argjson exp "${exp}" \
        '{iss:$iss,sub:$sub,aud:$aud,iat:$iat,exp:$exp}' | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')"
      sig_b64="$(printf '%s.%s' "${hdr_b64}" "${pay_b64}" | openssl dgst -sha256 -sign "${tmp_dir}/sa.pem" -binary | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')"
      rm -f "${tmp_dir}/sa.pem"
      printf '%s.%s.%s' "${hdr_b64}" "${pay_b64}" "${sig_b64}"
      return 0
    fi
  fi

  claim_file="${tmp_dir}/claim.json"
  jwt_file="${tmp_dir}/token.jwt"
  jq -n \
    --arg iss "${verifier_sa}" \
    --arg sub "${verifier_sa}" \
    --arg aud "https://${target_host}/*" \
    --argjson iat "${now}" \
    --argjson exp "${exp}" \
    '{iss: $iss, sub: $sub, aud: $aud, iat: $iat, exp: $exp}' > "${claim_file}"
  gcloud iam service-accounts sign-jwt \
    --iam-account="${verifier_sa}" \
    --project="${PROJECT_ID}" \
    "${claim_file}" \
    "${jwt_file}" >/dev/null
  tr -d '\r\n' < "${jwt_file}"
}

echo "==> [1/5] Checking public /healthz and /.well-known/jwks.json on https://${registry_host}..."
curl -fsS "${curl_tls_flags[@]}" "https://${registry_host}/healthz" >/dev/null
jwks_json="$(curl -fsS "${curl_tls_flags[@]}" "https://${registry_host}/.well-known/jwks.json")"
kid="$(jq -r '.keys[0].kid // empty' <<<"${jwks_json}")"
crv="$(jq -r '.keys[0].crv // empty' <<<"${jwks_json}")"
if [[ -z "${kid}" || "${crv}" != "P-256" ]]; then
  echo "verify.sh: unexpected JWKS payload: ${jwks_json}" >&2
  exit 1
fi
echo "    PASS: JWKS published Cloud KMS P-256 key (kid=${kid})"

echo "==> [2/5] Checking Cloud IAP blocks unauthenticated requests to /api/directory..."
unauth_code="$(curl -sS "${curl_tls_flags[@]}" -o /dev/null -w '%{http_code}' "https://${registry_host}/api/directory")"
if [[ "${unauth_code}" != "302" && "${unauth_code}" != "401" ]]; then
  echo "verify.sh: expected HTTP 302 or 401 from IAP on unauthenticated /api/directory, got ${unauth_code}" >&2
  exit 1
fi
echo "    PASS: Unauthenticated /api/directory rejected at IAP edge (HTTP ${unauth_code})"

if [[ -z "${verifier_sa}" ]]; then
  echo "verify.sh: verifier_service_account is disabled; skipping authenticated checks."
  exit 0
fi

echo "==> [3/5] Querying GET /api/directory through Cloud IAP as ${verifier_sa}..."
reg_jwt="$(sign_iap_jwt "${registry_host}")"
dir_json="$(curl -fsS "${curl_tls_flags[@]}" \
  -H "Authorization: Bearer ${reg_jwt}" \
  "https://${registry_host}/api/directory")"
jq . <<<"${dir_json}"

if [[ -n "${ENTRY_NAME}" ]]; then
  mapfile -t targets < <(jq -r --arg n "${ENTRY_NAME}" '.entries[] | select(.name == $n) | .name' <<<"${dir_json}")
  if [[ "${#targets[@]}" -eq 0 ]]; then
    echo "verify.sh: entry '${ENTRY_NAME}' not found in /api/directory" >&2
    exit 1
  fi
else
  mapfile -t targets < <(jq -r '.entries[] | select(.kind == "connected") | .name' <<<"${dir_json}")
fi

if [[ "${#targets[@]}" -eq 0 ]]; then
  echo "    NOTE: No connected entries registered yet."
  exit 0
fi

for name in "${targets[@]}"; do
  entry_obj="$(jq -c --arg n "${name}" '.entries[] | select(.name == $n)' <<<"${dir_json}")"
  status="$(jq -r '.status' <<<"${entry_obj}")"
  ver="$(jq -r '.grain_version // empty' <<<"${entry_obj}")"
  hb_age="$(jq -r '.heartbeat_age_seconds // "null"' <<<"${entry_obj}")"
  active_tasks="$(jq -r '.active_tasks' <<<"${entry_obj}")"
  awaiting_reply="$(jq -r '.awaiting_reply_tasks' <<<"${entry_obj}")"
  queue_depth="$(jq -r '.queue_depth' <<<"${entry_obj}")"
  echo "    Entry '${name}': status=${status} version=${ver:-unknown} heartbeat_age_s=${hb_age} active_tasks=${active_tasks} awaiting_reply=${awaiting_reply} queue_depth=${queue_depth}"

  if [[ "${status}" != "online" ]]; then
    echo "verify.sh: expected entry '${name}' status=online, got '${status}'" >&2
    exit 1
  fi

  echo "==> [4/5] Checking GET /api/route for entry '${name}'..."
  scope_repo="$(jq -r '.scopes[0].repo // "ai-on-gke/grain"' <<<"${entry_obj}")"
  route_json="$(curl -fsS "${curl_tls_flags[@]}" \
    -H "Authorization: Bearer ${reg_jwt}" \
    "https://${registry_host}/api/route?repo=${scope_repo}")"
  jq . <<<"${route_json}"

  entry_host="${name}.${instance_domain}"
  echo "==> [5/5] Routing HTTP request through https://${entry_host}/api/auth/user over outbound tunnel..."
  # When tls_mode=hybrid and <name> was not pre-listed in managed_entry_names,
  # *.instance_domain uses the self-signed wildcard fallback certificate.
  entry_curl_flags=("${curl_tls_flags[@]}")
  if [[ "${tls_mode}" == "hybrid" && "${#entry_curl_flags[@]}" -eq 0 ]]; then
    if ! curl -fsS --max-time 5 "https://${entry_host}/api/auth/user" >/dev/null 2>&1; then
      entry_curl_flags+=(-k)
    fi
  fi
  entry_jwt="$(sign_iap_jwt "${entry_host}")"
  auth_user_json="$(curl -fsS "${entry_curl_flags[@]}" \
    -H "Authorization: Bearer ${entry_jwt}" \
    "https://${entry_host}/api/auth/user")"
  jq . <<<"${auth_user_json}"

  auth_method="$(jq -r '.authMethod // empty' <<<"${auth_user_json}")"
  if [[ "${auth_method}" != "registry" ]]; then
    echo "verify.sh: expected connected daemon authMethod='registry' on routed request, got '${auth_method}'" >&2
    exit 1
  fi

  # Also verify that the connected daemon's own operator allowlist refuses the
  # read-only verifier service account on operator-scoped endpoints (/api/tasks).
  tasks_code="$(curl -sS "${entry_curl_flags[@]}" -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${entry_jwt}" \
    "https://${entry_host}/api/tasks")"
  if [[ "${tasks_code}" != "403" ]]; then
    echo "verify.sh: expected HTTP 403 for non-operator verifier SA on /api/tasks, got ${tasks_code}" >&2
    exit 1
  fi
  echo "    PASS: Routed request reached '${name}' daemon (authMethod=registry) and enforced operator allowlist (403 on /api/tasks for verifier SA)."
done

echo "All sandbox verification checks passed."
