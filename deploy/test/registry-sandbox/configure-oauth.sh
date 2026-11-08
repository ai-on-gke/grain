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

# Switch the sandbox registry's IAP-protected backend service from the
# Google-managed OAuth client to a custom OAuth client.
#
# Why: Cloud IAP's Google-managed OAuth client only admits users who belong to
# the same Google Cloud organization as the project. When the sandbox project
# lives in a different organization from the people being admitted (for
# example a sandbox-folder project admitting @google.com users), those users
# get IAP's "You don't have access" page even though they hold
# roles/iap.httpsResourceAccessor. A custom OAuth client, created by hand in
# the Cloud Console (the IAP OAuth Admin API that could create one was shut
# down in March 2026), lifts that restriction.
#
# The client secret is read from a file or from a hidden prompt and passed to
# gcloud via --flags-file using a process-substituted descriptor, so it never
# appears in argv, shell history, the repository, or Terraform state. Terraform
# (lb.tf) ignores the OAuth client fields on the backend service, so re-running
# up.sh keeps this setting.

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-}"
NAME_PREFIX="${NAME_PREFIX:-grain-reg}"
CLIENT_ID=""
CLIENT_SECRET_FILE=""
SHOW_ONLY=0

usage() {
  cat <<'EOF'
Usage:
  ./deploy/test/registry-sandbox/configure-oauth.sh \
    --project <gcp-project-id> \
    --client-id <id>.apps.googleusercontent.com \
    [--client-secret-file <path>]

  ./deploy/test/registry-sandbox/configure-oauth.sh --project <id> --show

Options:
  --project <id>               GCP sandbox project ID (required)
  --client-id <id>             OAuth 2.0 Web client ID created in the Cloud Console
  --client-secret-file <path>  File holding the client secret (default: hidden prompt)
  --prefix <prefix>            Resource name prefix (default: grain-reg)
  --show                       Print the backend's current IAP OAuth setting and exit
  -h, --help                   Show this help text

Before running, create the client in the Cloud Console of the same project:
  1. Google Auth Platform > Branding: app name, support email. Audience:
     "External" (unless every admitted user is in the project's organization),
     and keep Publishing status in "Testing" with only the admitted users added
     as test users (avoid publishing the consent screen globally unless needed;
     IAP still enforces roles/iap.httpsResourceAccessor either way).
  2. Google Auth Platform > Clients > Create client > "Web application".
  3. Run this script with the client ID and secret it shows.
  4. Edit the client and add the authorized redirect URI this script prints:
       https://iap.googleapis.com/v1/oauth/clientIds/<CLIENT_ID>:handleRedirect
EOF
}

require_arg() {
  if [[ "$1" -lt 2 ]]; then
    echo "configure-oauth.sh: option '$2' requires an argument" >&2
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
    --client-id)
      require_arg "$#" "$1"
      CLIENT_ID="$2"
      shift 2
      ;;
    --client-secret-file)
      require_arg "$#" "$1"
      CLIENT_SECRET_FILE="$2"
      shift 2
      ;;
    --prefix)
      require_arg "$#" "$1"
      NAME_PREFIX="$2"
      shift 2
      ;;
    --show)
      SHOW_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "configure-oauth.sh: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${PROJECT_ID}" ]]; then
  echo "configure-oauth.sh: --project is required" >&2
  usage >&2
  exit 2
fi

if ! [[ "${PROJECT_ID}" =~ ^[a-z][a-z0-9.:-]*[a-z0-9]$ ]]; then
  echo "configure-oauth.sh: invalid --project '${PROJECT_ID}'" >&2
  exit 2
fi

if ! [[ "${NAME_PREFIX}" =~ ^[a-z][a-z0-9-]{1,10}[a-z0-9]$ ]]; then
  echo "configure-oauth.sh: --prefix must match ^[a-z][a-z0-9-]{1,10}[a-z0-9]$" >&2
  exit 2
fi

for tool in gcloud jq; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "configure-oauth.sh: required tool not found: ${tool}" >&2
    exit 1
  fi
done

BACKEND="${NAME_PREFIX}-iap-bs"

if [[ "${SHOW_ONLY}" -eq 1 ]]; then
  gcloud compute backend-services describe "${BACKEND}" --global \
    --project "${PROJECT_ID}" --format=json |
    jq '{enabled: .iap.enabled, oauth2ClientId: (if (.iap.oauth2ClientId // "") == "" then "(Google-managed)" else .iap.oauth2ClientId end)}'
  exit 0
fi

if [[ -z "${CLIENT_ID}" ]]; then
  echo "configure-oauth.sh: --client-id is required (or pass --show)" >&2
  usage >&2
  exit 2
fi

if ! [[ "${CLIENT_ID}" =~ ^[A-Za-z0-9.-]+\.apps\.googleusercontent\.com$ ]]; then
  echo "configure-oauth.sh: --client-id must look like <id>.apps.googleusercontent.com" >&2
  exit 2
fi

client_secret=""
if [[ -n "${CLIENT_SECRET_FILE}" ]]; then
  if [[ ! -r "${CLIENT_SECRET_FILE}" ]]; then
    echo "configure-oauth.sh: cannot read --client-secret-file ${CLIENT_SECRET_FILE}" >&2
    exit 1
  fi
  client_secret="$(tr -d '[:space:]' <"${CLIENT_SECRET_FILE}")"
else
  if [[ ! -t 0 ]]; then
    echo "configure-oauth.sh: no --client-secret-file and stdin is not a terminal" >&2
    exit 2
  fi
  read -r -s -p "OAuth client secret for ${CLIENT_ID}: " client_secret
  echo >&2
fi

if [[ -z "${client_secret}" ]]; then
  echo "configure-oauth.sh: empty client secret" >&2
  exit 2
fi

echo "Updating IAP on backend service ${BACKEND} in ${PROJECT_ID} to OAuth client ${CLIENT_ID}..." >&2
# The OAuth client secret is piped via bash's builtin printf -> jq -Rs
# into a JSON flags file read through a process-substituted file descriptor
# (--flags-file=<( ... )), so the secret never appears in argv, shell history,
# or process listings. Using gcloud directly avoids corp mTLS auth failures
# (ACCESS_TOKEN_TYPE_UNSUPPORTED) that occur with raw curl calls.
if ! gcloud compute backend-services update "${BACKEND}" --global \
  --project "${PROJECT_ID}" \
  --flags-file=<(
    printf '%s' "${client_secret}" |
      jq -Rs --arg id "${CLIENT_ID}" \
        '{"--iap": ("enabled,oauth2-client-id=" + $id + ",oauth2-client-secret=" + (rtrimstr("\n")))}'
  ) >/dev/null; then
  unset client_secret
  echo "configure-oauth.sh: updating backend service ${BACKEND} failed" >&2
  exit 1
fi
unset client_secret

now_id="$(gcloud compute backend-services describe "${BACKEND}" --global \
  --project "${PROJECT_ID}" --format='value(iap.oauth2ClientId)')"
if [[ "${now_id}" != "${CLIENT_ID}" ]]; then
  echo "configure-oauth.sh: backend reports oauth2ClientId='${now_id}', expected ${CLIENT_ID}" >&2
  exit 1
fi

cat >&2 <<EOF
Done. IAP on ${BACKEND} now uses OAuth client ${CLIENT_ID}.

Make sure the client lists this authorized redirect URI (Google Auth Platform >
Clients > ${CLIENT_ID}):
  https://iap.googleapis.com/v1/oauth/clientIds/${CLIENT_ID}:handleRedirect

Then sign in again in a fresh browser window (IAP caches the old session
cookie for a few minutes).
EOF
