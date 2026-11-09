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
#   3. Programmatic IAP authentication with an openid+email service-account access token
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
  --insecure-tls           Pass -k to curl instead of trusting the generated self-signed
                           certificate (only needed before a managed cert is ACTIVE)
  -h, --help               Show this help text
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "verify.sh: option '$2' requires an argument" >&2
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
    --entry)
      require_arg "$#" "$1"
      ENTRY_NAME="$2"
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

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "verify.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "verify.sh: --prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$" >&2
  exit 2
fi

if ! [[ "${ZONE}" =~ ^[a-z][a-z0-9-]*[a-z0-9]$ ]]; then
  echo "verify.sh: invalid --zone '${ZONE}'" >&2
  exit 2
fi

if [[ -n "${ENTRY_NAME}" ]] && ! [[ "${ENTRY_NAME}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
  echo "verify.sh: invalid --entry '${ENTRY_NAME}'" >&2
  exit 2
fi

if [[ -z "${STATE_BUCKET}" ]]; then
  STATE_BUCKET="${PROJECT_ID}-${NAME_PREFIX}-tfstate"
fi

if ! [[ "${STATE_BUCKET}" =~ ^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$ ]]; then
  echo "verify.sh: invalid --state-bucket '${STATE_BUCKET}'" >&2
  exit 2
fi

for cmd in gcloud terraform jq curl; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "verify.sh: required command '${cmd}' is not installed" >&2
    exit 1
  fi
done

if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -r "${HOME}/.gcp-service-account.json" ]]; then
  export GOOGLE_APPLICATION_CREDENTIALS="${HOME}/.gcp-service-account.json"
elif [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -z "${GOOGLE_OAUTH_ACCESS_TOKEN:-}" && ! -r "${HOME}/.config/gcloud/application_default_credentials.json" ]]; then
  GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
  export GOOGLE_OAUTH_ACCESS_TOKEN
fi

tmp_dir="$(mktemp -d)"
chmod 0700 "${tmp_dir}"
tmp_override=""
cleanup() {
  rm -rf "${tmp_dir}"
  if [[ -n "${tmp_override}" ]]; then
    rm -f "${tmp_override}"
  fi
}
trap cleanup EXIT

if [[ "${LOCAL_STATE}" -eq 1 ]]; then
  umask 077
  tmp_override="${SCRIPT_DIR}/backend_override.tf"
  cat > "${tmp_override}" <<'EOF'
terraform {
  backend "local" {}
}
EOF
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false >/dev/null
else
  project_num="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
  if ! [[ "${project_num}" =~ ^[0-9]+$ ]]; then
    echo "verify.sh: failed to resolve numeric projectNumber for '${PROJECT_ID}'" >&2
    exit 1
  fi
  bucket_proj_num="$(gcloud storage buckets describe "gs://${STATE_BUCKET}" --project="${PROJECT_ID}" --raw --format='value(projectNumber)' 2>/dev/null || true)"
  if [[ -z "${bucket_proj_num}" ]]; then
    echo "verify.sh: cannot read state bucket gs://${STATE_BUCKET}" >&2
    exit 1
  fi
  if [[ "${bucket_proj_num}" != "${project_num}" ]]; then
    echo "verify.sh: refusing to read state from gs://${STATE_BUCKET}: bucket belongs to project number ${bucket_proj_num}, not ${PROJECT_ID} (${project_num})" >&2
    exit 1
  fi
  terraform -chdir="${SCRIPT_DIR}" init -reconfigure -input=false \
    -backend-config="bucket=${STATE_BUCKET}" \
    -backend-config="prefix=registry-sandbox/${NAME_PREFIX}" >/dev/null 2>&1 || true
fi

get_tf_output() {
  local out_name="$1"
  local val
  val="$(terraform -chdir="${SCRIPT_DIR}" output -raw "${out_name}" 2>/dev/null || true)"
  if [[ -z "${val}" && "${LOCAL_STATE}" -eq 0 ]]; then
    val="$(gcloud storage cat "gs://${STATE_BUCKET}/registry-sandbox/${NAME_PREFIX}/default.tfstate" 2>/dev/null | jq -r ".outputs.${out_name}.value // empty" 2>/dev/null || true)"
  fi
  printf '%s' "${val}"
}

registry_host="$(get_tf_output registry_host)"
instance_domain="$(get_tf_output instance_domain)"
lb_ip="$(get_tf_output lb_ip)"
verifier_sa="$(get_tf_output verifier_service_account)"
tls_mode="$(get_tf_output tls_mode)"

if ! [[ "${registry_host}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "verify.sh: invalid registry_host '${registry_host}' in Terraform outputs" >&2
  exit 1
fi
if ! [[ "${instance_domain}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]; then
  echo "verify.sh: invalid instance_domain '${instance_domain}' in Terraform outputs" >&2
  exit 1
fi
if ! [[ "${lb_ip}" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
  echo "verify.sh: invalid lb_ip '${lb_ip}' in Terraform outputs" >&2
  exit 1
fi
if [[ -n "${verifier_sa}" ]] && ! [[ "${verifier_sa}" =~ ^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\.gserviceaccount\.com$ ]]; then
  echo "verify.sh: invalid verifier_service_account '${verifier_sa}' in Terraform outputs" >&2
  exit 1
fi

# verify.sh sends an IAP-accepted bearer token for the verifier service account
# with every authenticated request, so it never turns TLS verification off on
# its own: the self-signed wildcard certificate Terraform generated (public
# `selfsigned_cert_pem` output) is trusted alongside the system roots instead.
# `--insecure-tls` remains an explicit operator opt-in.
selfsigned_pem="$(get_tf_output selfsigned_cert_pem)"

curl_tls_flags=()
if [[ "${INSECURE_TLS}" -eq 1 ]]; then
  curl_tls_flags+=(-k)
elif [[ "${selfsigned_pem}" == *"BEGIN CERTIFICATE"* ]]; then
  ca_bundle="${tmp_dir}/ca-bundle.pem"
  : > "${ca_bundle}"
  for cand in "${CURL_CA_BUNDLE:-}" "${SSL_CERT_FILE:-}" \
    /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
    if [[ -n "${cand}" && -r "${cand}" ]]; then
      cat "${cand}" >> "${ca_bundle}"
      printf '\n' >> "${ca_bundle}"
    fi
  done
  printf '%s\n' "${selfsigned_pem}" >> "${ca_bundle}"
  curl_tls_flags+=(--cacert "${ca_bundle}")
elif [[ "${tls_mode}" != "managed" ]]; then
  echo "verify.sh: tls_mode=${tls_mode} but Terraform state has no selfsigned_cert_pem output; re-run up.sh to refresh outputs, or pass --insecure-tls" >&2
  exit 1
fi

mint_iap_bearer_token() {
  local active_acct iap_client_id
  active_acct="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -n1 || true)"
  iap_client_id="$(gcloud compute backend-services describe "${NAME_PREFIX}-iap-bs" \
    --global --project="${PROJECT_ID}" --format='value(iap.oauth2ClientId)' 2>/dev/null || true)"

  # When configure-oauth.sh has switched the backend service to a custom OAuth
  # 2.0 client, Cloud IAP expects an OIDC ID token whose audience is that
  # client ID. Otherwise (Google-managed IAP client), IAP accepts a
  # service-account OAuth2 access token carrying `openid` and `userinfo.email`.
  if [[ -n "${iap_client_id}" ]]; then
    if [[ "${active_acct}" == "${verifier_sa}" ]]; then
      gcloud auth print-identity-token --audiences="${iap_client_id}"
      return 0
    fi
    gcloud auth print-identity-token \
      --impersonate-service-account="${verifier_sa}" \
      --audiences="${iap_client_id}" \
      --include-email
    return 0
  fi

  if [[ "${active_acct}" == "${verifier_sa}" ]]; then
    gcloud auth print-access-token --scopes="openid,https://www.googleapis.com/auth/userinfo.email"
    return 0
  fi
  gcloud auth print-access-token \
    --impersonate-service-account="${verifier_sa}" \
    --scopes="openid,https://www.googleapis.com/auth/userinfo.email"
}

reg_curl_flags=("${curl_tls_flags[@]}" --resolve "${registry_host}:443:${lb_ip}")

echo "==> [1/5] Checking public /healthz and /.well-known/jwks.json on https://${registry_host}..."
curl -fsS "${reg_curl_flags[@]}" "https://${registry_host}/healthz" >/dev/null
jwks_json="$(curl -fsS "${reg_curl_flags[@]}" "https://${registry_host}/.well-known/jwks.json")"
kid="$(jq -r '.keys[0].kid // empty' <<<"${jwks_json}")"
crv="$(jq -r '.keys[0].crv // empty' <<<"${jwks_json}")"
if [[ -z "${kid}" || "${crv}" != "P-256" ]]; then
  echo "verify.sh: unexpected JWKS payload: ${jwks_json}" >&2
  exit 1
fi
echo "    PASS: JWKS published Cloud KMS P-256 key (kid=${kid})"

echo "==> [2/5] Checking Cloud IAP blocks unauthenticated requests to /api/directory..."
unauth_code="$(curl -sS "${reg_curl_flags[@]}" -o /dev/null -w '%{http_code}' "https://${registry_host}/api/directory")"
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
iap_tok="$(mint_iap_bearer_token)"
dir_json="$(curl -fsS "${reg_curl_flags[@]}" \
  -H @<(printf 'Authorization: Bearer %s\n' "${iap_tok}") \
  "https://${registry_host}/api/directory")"
jq . <<<"${dir_json}"

if [[ -n "${ENTRY_NAME}" ]]; then
  mapfile -t targets < <(jq -r --arg n "${ENTRY_NAME}" '.entries[] | select(.name == $n) | .name' <<<"${dir_json}")
  if [[ "${#targets[@]}" -eq 0 ]]; then
    echo "verify.sh: entry '${ENTRY_NAME}' not found in /api/directory" >&2
    exit 1
  fi
else
  mapfile -t targets < <(jq -r '.entries[] | select(.kind == "connected" or .kind == "instance") | .name' <<<"${dir_json}")
fi

if [[ "${#targets[@]}" -eq 0 ]]; then
  echo "    NOTE: No entries registered yet."
  exit 0
fi

for name in "${targets[@]}"; do
  entry_obj="$(jq -c --arg n "${name}" '.entries[] | select(.name == $n)' <<<"${dir_json}")"
  entry_kind="$(jq -r '.kind // "connected"' <<<"${entry_obj}")"
  status="$(jq -r '.status' <<<"${entry_obj}")"
  ver="$(jq -r '.grain_version // empty' <<<"${entry_obj}")"
  hb_age="$(jq -r '.heartbeat_age_seconds // "null"' <<<"${entry_obj}")"
  active_tasks="$(jq -r '.active_tasks' <<<"${entry_obj}")"
  awaiting_reply="$(jq -r '.awaiting_reply_tasks' <<<"${entry_obj}")"
  queue_depth="$(jq -r '.queue_depth' <<<"${entry_obj}")"
  echo "    Entry '${name}' (${entry_kind}): status=${status} version=${ver:-unknown} heartbeat_age_s=${hb_age} active_tasks=${active_tasks} awaiting_reply=${awaiting_reply} queue_depth=${queue_depth}"

  if [[ "${status}" != "online" ]]; then
    if [[ -z "${ENTRY_NAME}" && "${hb_age}" == "null" ]]; then
      echo "    NOTE: Entry '${name}' (${entry_kind}) is not online yet; skipping route check (pass --entry ${name} to require online)."
      continue
    fi
    echo "verify.sh: expected entry '${name}' status=online, got '${status}'" >&2
    exit 1
  fi

  if ! [[ "${name}" =~ ^[a-z][a-z0-9-]{1,30}[a-z0-9]$ ]]; then
    echo "verify.sh: refusing invalid entry name '${name}' from /api/directory" >&2
    exit 1
  fi

  echo "==> [4/5] Checking GET /api/route for entry '${name}'..."
  scope_repo="$(jq -r '.scopes[0].repo // "ai-on-gke/grain"' <<<"${entry_obj}")"
  route_json="$(curl -fsS --get "${reg_curl_flags[@]}" \
    -H @<(printf 'Authorization: Bearer %s\n' "${iap_tok}") \
    --data-urlencode "repo=${scope_repo}" \
    "https://${registry_host}/api/route")"
  jq . <<<"${route_json}"

  entry_host="${name}.${instance_domain}"
  transport_desc="outbound tunnel"
  if [[ "${entry_kind}" == "instance" ]]; then
    transport_desc="private VPC HTTP"
  fi
  echo "==> [5/5] Routing HTTP request through https://${entry_host}/api/auth/whoami over ${transport_desc}..."
  # When tls_mode=hybrid and <name> was not pre-listed in managed_entry_names,
  # *.instance_domain uses the self-signed wildcard fallback certificate, which
  # curl_tls_flags already trusts (pinned, not -k).
  entry_curl_flags=("${curl_tls_flags[@]}" --resolve "${entry_host}:443:${lb_ip}")
  whoami_json="$(curl -fsS "${entry_curl_flags[@]}" \
    -H @<(printf 'Authorization: Bearer %s\n' "${iap_tok}") \
    "https://${entry_host}/api/auth/whoami")"
  jq . <<<"${whoami_json}"
  # /api/auth/whoami is answered by the daemon itself, so the version it
  # reports must match the grain_version its heartbeat put in the catalog.
  whoami_ver="$(jq -r '.version // empty' <<<"${whoami_json}")"
  if [[ -z "${whoami_ver}" || ( -n "${ver}" && "${whoami_ver}" != "${ver}" ) ]]; then
    echo "verify.sh: /api/auth/whoami via '${entry_host}' reported version '${whoami_ver}', catalog says '${ver}'" >&2
    exit 1
  fi

  # The connected daemon evaluates the verified registry assertion against its
  # own allowlist: the read-only verifier SA must get the daemon's 403 on
  # operator-scoped /api/tasks (which proves the request reached the daemon).
  tasks_body="${tmp_dir}/tasks-${name}.body"
  tasks_code="$(curl -sS "${entry_curl_flags[@]}" -o "${tasks_body}" -w '%{http_code}' \
    -H @<(printf 'Authorization: Bearer %s\nAccept: application/json\n' "${iap_tok}") \
    "https://${entry_host}/api/tasks")"
  if [[ "${tasks_code}" != "403" ]]; then
    echo "verify.sh: expected HTTP 403 for non-operator verifier SA on /api/tasks, got ${tasks_code} ($(cat "${tasks_body}"))" >&2
    exit 1
  fi
  if ! grep -Fq "allowed users and groups" "${tasks_body}"; then
    echo "verify.sh: 403 on /api/tasks did not come from the connected daemon's allowlist: $(cat "${tasks_body}")" >&2
    exit 1
  fi
  echo "    PASS: Routed request reached '${name}' daemon over its outbound tunnel (/api/auth/whoami 200; /api/tasks refused by the daemon's allowlist)."
done

echo "All sandbox verification checks passed."
