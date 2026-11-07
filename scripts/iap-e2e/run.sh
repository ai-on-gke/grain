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

# scripts/iap-e2e/run.sh — Real end-to-end Cloud Run Direct IAP access
# qualification script for grain (Task #2774, replacing Task #2512).
#
# Deploys grain to Cloud Run with IAP enabled directly on the service
# (--iap, default *.run.app URL; no load balancer, certificate, or DNS),
# creates two temporary GCP service accounts:
#   - SA-A: granted roles/iap.httpsResourceAccessor AND on grain's allowlist
#     (`grain access allow serviceAccount:... --reason "iap e2e"` over
#     loopback on images that keep the allowlist in the database, and
#     -ui-allowed-principals on older v3.1.x images)
#   - SA-B: granted roles/iap.httpsResourceAccessor, NOT on the allowlist
# exercises Cases 1..8 against real Google-signed tokens and real IAP
# X-Goog-IAP-JWT-Assertion headers, writes machine-readable JSON + Markdown
# reports, and always tears down all GCP and local resources on exit.
#
# Usage:
#   scripts/iap-e2e/run.sh [IMAGE] [options]
#
# Options:
#   --image IMAGE       Container image to deploy (default: current RC tag or
#                       docker.io/grainai/grain:main)
#   --rc-tag TAG        Shorthand for ${PACKAGE_PUBLISH_REGISTRY}/${RELEASE_IMAGE_REPO}/grain:TAG
#   --project PROJECT   GCP project ID (default: $GCP_PROJECT, gcloud default,
#                       or bsalmon-sandbox)
#   --region REGION     Cloud Run region (default: us-central1)
#   --service NAME      Cloud Run service name prefix (default: grain-iap-e2e)
#   --out DIR           Directory for report.json and report.md
#                       (default: /tmp/grain-iap-e2e)
#   --grain-bin PATH    Path to grain CLI binary (built from ./cmd/grain if unset)
#   --keep              Keep Cloud Run service and SAs running after exit
#                       (for manual inspection only; never used in CI)
#   --local-sim         Internal self-test mode used by scripts/iap-e2e/run_test.sh
#   -h, --help          Show this help text

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

if [[ -f "${REPO_ROOT}/scripts/release.conf" ]]; then
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/scripts/release.conf"
fi
# shellcheck source=scripts/release-image-repos.sh
source "${REPO_ROOT}/scripts/release-image-repos.sh"
IMAGE_REGISTRY="${PACKAGE_PUBLISH_REGISTRY:-$RELEASE_IMAGE_REGISTRY_DEFAULT}"
IMAGE_REPO="${RELEASE_IMAGE_REPO:-$RELEASE_IMAGE_REPO_DEFAULT}"

usage() {
  sed -n '16,45s/^# \{0,1\}//p' "${BASH_SOURCE[0]}"
}

resolve_default_image() {
  local latest_rc latest_rel
  latest_rc="$(git -C "$REPO_ROOT" tag -l 'v*-rc.*' --sort=-v:refname 2>/dev/null | head -n1 || true)"
  if [[ -n "$latest_rc" ]]; then
    local base_ver="${latest_rc%%-rc.*}"
    latest_rel="$(git -C "$REPO_ROOT" tag -l "$base_ver" 2>/dev/null || true)"
    if [[ -z "$latest_rel" ]]; then
      printf '%s/%s/grain:%s\n' "$IMAGE_REGISTRY" "$IMAGE_REPO" "$latest_rc"
      return
    fi
  fi
  printf '%s/%s/grain:main\n' "$IMAGE_REGISTRY" "$IMAGE_REPO"
}

IMAGE=""
RC_TAG=""
PROJECT="${GCP_PROJECT:-}"
REGION="${GCP_REGION:-us-central1}"
SERVICE="grain-iap-e2e"
OUT_DIR="/tmp/grain-iap-e2e"
GRAIN_BIN="${GRAIN_BIN:-}"
KEEP=0
LOCAL_SIM=0
SIM_INJECT_FAIL_CASE="${GRAIN_IAP_E2E_SIM_FAIL_CASE:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)
      [[ $# -ge 2 ]] || { echo "error: --image requires a value" >&2; exit 2; }
      IMAGE="$2"; shift 2 ;;
    --image=*)
      IMAGE="${1#*=}"; shift ;;
    --rc-tag)
      [[ $# -ge 2 ]] || { echo "error: --rc-tag requires a value" >&2; exit 2; }
      RC_TAG="$2"; shift 2 ;;
    --rc-tag=*)
      RC_TAG="${1#*=}"; shift ;;
    --project)
      [[ $# -ge 2 ]] || { echo "error: --project requires a value" >&2; exit 2; }
      PROJECT="$2"; shift 2 ;;
    --project=*)
      PROJECT="${1#*=}"; shift ;;
    --region)
      [[ $# -ge 2 ]] || { echo "error: --region requires a value" >&2; exit 2; }
      REGION="$2"; shift 2 ;;
    --region=*)
      REGION="${1#*=}"; shift ;;
    --service)
      [[ $# -ge 2 ]] || { echo "error: --service requires a value" >&2; exit 2; }
      SERVICE="$2"; shift 2 ;;
    --service=*)
      SERVICE="${1#*=}"; shift ;;
    --out)
      [[ $# -ge 2 ]] || { echo "error: --out requires a value" >&2; exit 2; }
      OUT_DIR="$2"; shift 2 ;;
    --out=*)
      OUT_DIR="${1#*=}"; shift ;;
    --grain-bin)
      [[ $# -ge 2 ]] || { echo "error: --grain-bin requires a value" >&2; exit 2; }
      GRAIN_BIN="$2"; shift 2 ;;
    --grain-bin=*)
      GRAIN_BIN="${1#*=}"; shift ;;
    --keep)
      KEEP=1; shift ;;
    --local-sim)
      LOCAL_SIM=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    -*)
      echo "error: unknown flag: $1" >&2
      usage >&2
      exit 2 ;;
    *)
      if [[ -z "$IMAGE" ]]; then
        IMAGE="$1"; shift
      else
        echo "error: unexpected positional argument: $1" >&2
        exit 2
      fi ;;
  esac
done

if [[ -n "$RC_TAG" && -z "$IMAGE" ]]; then
  IMAGE="${IMAGE_REGISTRY}/${IMAGE_REPO}/grain:${RC_TAG}"
fi
if [[ -z "$IMAGE" ]]; then
  IMAGE="$(resolve_default_image)"
fi

if [[ "$LOCAL_SIM" -eq 0 && -z "$PROJECT" ]]; then
  PROJECT="$(gcloud config get-value project 2>/dev/null || true)"
  PROJECT="${PROJECT:-bsalmon-sandbox}"
fi
PROJECT="${PROJECT:-local-sim-project}"

for cmd in curl jq openssl python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "error: required command '$cmd' is not installed" >&2
    exit 2
  fi
done
if [[ "$LOCAL_SIM" -eq 0 ]] && ! command -v gcloud >/dev/null 2>&1; then
  echo "error: required command 'gcloud' is not installed" >&2
  exit 2
fi

mkdir -p "$OUT_DIR"
WORK_TMP="$(mktemp -d -t grain-iap-e2e.XXXXXX)"
chmod 0700 "$WORK_TMP"
CASES_JSONL="${OUT_DIR}/.cases.$$.jsonl"
: > "$CASES_JSONL"
REPORT_WRITTEN=0

START_EPOCH="$(date +%s)"
START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
DEPLOY_START_EPOCH=0
DEPLOY_END_EPOCH=0
TEARDOWN_DONE=0
TEARDOWN_STATUS="pending"

SA_A_NAME="${SERVICE}-sa-a"
SA_B_NAME="${SERVICE}-sa-b"
SA_A_EMAIL="${SA_A_NAME}@${PROJECT}.iam.gserviceaccount.com"
SA_B_EMAIL="${SA_B_NAME}@${PROJECT}.iam.gserviceaccount.com"
SA_A_KEY="${WORK_TMP}/sa-a.json"
SA_B_KEY="${WORK_TMP}/sa-b.json"

PROJECT_NUMBER="000000000000"
IAP_AUDIENCE=""
SERVICE_URL=""
DIRECT_URL=""

SIM_PIDS=()

log() {
  printf '[%s] %s\n' "$(date -u +%H:%M:%SZ)" "$*" >&2
}

stop_sim_pids() {
  local pid
  for pid in "${SIM_PIDS[@]:-}"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  SIM_PIDS=()
}

teardown() {
  local exit_code=$?
  set +e
  if [[ "$TEARDOWN_DONE" -eq 1 ]]; then
    return "$exit_code"
  fi
  TEARDOWN_DONE=1

  if [[ "$LOCAL_SIM" -eq 1 ]]; then
    stop_sim_pids
    rm -rf "$WORK_TMP"
    [[ "$REPORT_WRITTEN" -eq 1 || "$exit_code" -ne 0 ]] && rm -f "$CASES_JSONL"
    TEARDOWN_STATUS="completed (local-sim processes and temp directory removed)"
    return "$exit_code"
  fi

  if [[ "$KEEP" -eq 1 ]]; then
    log "KEEP=1 set: leaving Cloud Run service ${SERVICE} and service accounts in ${PROJECT}; removing local key dir ${WORK_TMP}"
    rm -rf "$WORK_TMP"
    [[ "$REPORT_WRITTEN" -eq 1 || "$exit_code" -ne 0 ]] && rm -f "$CASES_JSONL"
    TEARDOWN_STATUS="skipped (--keep)"
    return "$exit_code"
  fi

  log "Tearing down Cloud Run service and service accounts in ${PROJECT}..."
  local td_ok=1

  for sa in "$SA_A_EMAIL" "$SA_B_EMAIL"; do
    gcloud iap web remove-iam-policy-binding \
      --project="$PROJECT" \
      --resource-type=cloud-run \
      --region="$REGION" \
      --service="$SERVICE" \
      --member="serviceAccount:${sa}" \
      --role="roles/iap.httpsResourceAccessor" \
      --quiet >/dev/null 2>&1 || true
  done

  if gcloud run services describe "$SERVICE" --project="$PROJECT" --region="$REGION" >/dev/null 2>&1; then
    if ! gcloud run services delete "$SERVICE" --project="$PROJECT" --region="$REGION" --quiet >/dev/null 2>&1; then
      log "WARNING: failed to delete Cloud Run service ${SERVICE}"
      td_ok=0
    fi
  fi
  DEPLOY_END_EPOCH="$(date +%s)"

  for sa in "$SA_A_EMAIL" "$SA_B_EMAIL"; do
    if gcloud iam service-accounts describe "$sa" --project="$PROJECT" >/dev/null 2>&1; then
      if ! gcloud iam service-accounts delete "$sa" --project="$PROJECT" --quiet >/dev/null 2>&1; then
        log "WARNING: failed to delete service account ${sa}"
        td_ok=0
      fi
    fi
  done

  rm -rf "$WORK_TMP"
  if [[ "$td_ok" -eq 1 ]]; then
    TEARDOWN_STATUS="clean (Cloud Run service, IAP bindings, service accounts, and local keys deleted)"
    log "Teardown complete: nothing left running in ${PROJECT}."
  else
    TEARDOWN_STATUS="partial (see warnings in stderr log)"
  fi
  return "$exit_code"
}
trap teardown EXIT INT TERM

ensure_grain_bin() {
  if [[ -n "$GRAIN_BIN" && -x "$GRAIN_BIN" ]]; then
    return
  fi
  if [[ -z "$GRAIN_BIN" ]] && command -v grain >/dev/null 2>&1 && [[ "$LOCAL_SIM" -eq 0 ]]; then
    # Build from workspace when go + cmd/grain are present so CLI fixes in this
    # checkout are exercised; otherwise use grain on PATH.
    if command -v go >/dev/null 2>&1 && [[ -d "${REPO_ROOT}/cmd/grain" ]]; then
      GRAIN_BIN="${WORK_TMP}/grain"
      log "Building grain CLI from ${REPO_ROOT}/cmd/grain..."
      (cd "$REPO_ROOT" && go build -o "$GRAIN_BIN" ./cmd/grain)
      return
    fi
    GRAIN_BIN="$(command -v grain)"
    return
  fi
  GRAIN_BIN="${WORK_TMP}/grain"
  log "Building grain binary from ${REPO_ROOT}/cmd/grain..."
  (cd "$REPO_ROOT" && go build -o "$GRAIN_BIN" ./cmd/grain)
}

generate_access_code() {
  python3 - <<'PY'
import secrets
alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
groups = ["".join(secrets.choice(alphabet) for _ in range(8)) for _ in range(4)]
print("-".join(groups))
PY
}

# Mint an RS256 service-account self-signed JWT for Cloud Run Direct IAP.
# Arguments: <sa-key-json> <audience> [iat_offset_sec] [exp_offset_sec]
mint_sa_jwt() {
  local key_file="$1"
  local aud="$2"
  local iat_off="${3:-0}"
  local exp_off="${4:-3600}"
  python3 - "$key_file" "$aud" "$iat_off" "$exp_off" <<'PY'
import base64
import json
import subprocess
import sys
import tempfile
import time

key_file, aud, iat_off, exp_off = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
with open(key_file, "r", encoding="utf-8") as fh:
    sa = json.load(fh)

now = int(time.time())
hdr = {"alg": "RS256", "typ": "JWT"}
if sa.get("private_key_id"):
    hdr["kid"] = sa["private_key_id"]
claims = {
    "iss": sa["client_email"],
    "sub": sa["client_email"],
    "aud": aud,
    "iat": now + iat_off,
    "exp": now + exp_off,
}
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode("ascii")
signing_input = f"{b64(json.dumps(hdr, separators=(',', ':')).encode())}.{b64(json.dumps(claims, separators=(',', ':')).encode())}"

with tempfile.NamedTemporaryFile("w", delete=True) as pem_tmp:
    pem_tmp.write(sa["private_key"])
    pem_tmp.flush()
    sig = subprocess.check_output(
        ["openssl", "dgst", "-sha256", "-sign", pem_tmp.name],
        input=signing_input.encode("ascii"),
    )

print(f"{signing_input}.{b64(sig)}")
PY
}

# Mint an attacker-signed ES256 X-Goog-IAP-JWT-Assertion with an ephemeral P-256 key.
# Arguments: <email> <iap_audience> [issuer]
mint_forged_iap_assertion() {
  local email="$1"
  local aud="$2"
  local iss="${3:-https://cloud.google.com/iap}"
  python3 - "$email" "$aud" "$iss" <<'PY'
import base64
import json
import subprocess
import sys
import tempfile
import time

email, aud, iss = sys.argv[1], sys.argv[2], sys.argv[3]

def der_to_raw_ecdsa(der: bytes, size: int = 32) -> bytes:
    if der[0] != 0x30:
        raise ValueError("expected ASN.1 SEQUENCE")
    idx = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)
    if der[idx] != 0x02:
        raise ValueError("expected INTEGER r")
    r_len = der[idx + 1]
    r = int.from_bytes(der[idx + 2 : idx + 2 + r_len], "big")
    idx = idx + 2 + r_len
    if der[idx] != 0x02:
        raise ValueError("expected INTEGER s")
    s_len = der[idx + 1]
    s = int.from_bytes(der[idx + 2 : idx + 2 + s_len], "big")
    return r.to_bytes(size, "big") + s.to_bytes(size, "big")

now = int(time.time())
hdr = {"alg": "ES256", "typ": "JWT", "kid": "forged-attacker-key"}
claims = {
    "iss": iss,
    "sub": f"accounts.google.com:{email}",
    "email": email,
    "aud": aud,
    "iat": now,
    "exp": now + 3600,
}
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode("ascii")
signing_input = f"{b64(json.dumps(hdr, separators=(',', ':')).encode())}.{b64(json.dumps(claims, separators=(',', ':')).encode())}"

with tempfile.NamedTemporaryFile("w+", delete=True) as key_tmp:
    subprocess.run(
        ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", key_tmp.name],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    der_sig = subprocess.check_output(
        ["openssl", "dgst", "-sha256", "-sign", key_tmp.name],
        input=signing_input.encode("ascii"),
    )

raw_sig = der_to_raw_ecdsa(der_sig, 32)
print(f"{signing_input}.{b64(raw_sig)}")
PY
}

# Perform an HTTP request and write headers/body/status into $WORK_TMP/http_*.
# Sets globals: HTTP_STATUS, HTTP_HEADERS_FILE, HTTP_BODY_FILE.
HTTP_SEQ=0
http_call() {
  HTTP_SEQ=$((HTTP_SEQ + 1))
  HTTP_HEADERS_FILE="${WORK_TMP}/http_${HTTP_SEQ}.hdr"
  HTTP_BODY_FILE="${WORK_TMP}/http_${HTTP_SEQ}.body"
  HTTP_STATUS="$(
    curl -sS -D "$HTTP_HEADERS_FILE" -o "$HTTP_BODY_FILE" -w '%{http_code}' "$@"
  )"
}

header_present() {
  local name="$1"
  grep -qi "^${name}:" "$HTTP_HEADERS_FILE"
}

extract_cookie_pair() {
  local cookie_name="$1"
  python3 - "$HTTP_HEADERS_FILE" "$cookie_name" <<'PY'
import sys
hdr_file, name = sys.argv[1], sys.argv[2]
prefix = name.lower() + "="
with open(hdr_file, "r", encoding="utf-8", errors="replace") as fh:
    for line in fh:
        if line.lower().startswith("set-cookie:"):
            rest = line.split(":", 1)[1].strip()
            pair = rest.split(";", 1)[0].strip()
            if pair.lower().startswith(prefix):
                print(pair)
                sys.exit(0)
PY
}

record_case() {
  local num="$1"
  local id="$2"
  local title="$3"
  local status="$4"
  local evidence="$5"
  if [[ -n "$SIM_INJECT_FAIL_CASE" && "$SIM_INJECT_FAIL_CASE" == "$num" ]]; then
    status="FAIL"
    evidence="injected failure for self-test (${evidence})"
  fi
  jq -cn \
    --argjson num "$num" \
    --arg id "$id" \
    --arg title "$title" \
    --arg status "$status" \
    --arg evidence "$evidence" \
    '{case: $num, id: $id, title: $title, status: $status, evidence: $evidence}' >> "$CASES_JSONL"
  log "Case ${num} (${id}): ${status} — ${evidence}"
}

# ---------------------------------------------------------------------------
# Cloud Run vs Local Simulation Setup & Redeploy
# ---------------------------------------------------------------------------

enable_gcp_apis() {
  local api err_file="${WORK_TMP}/api_enable.err"
  for api in run.googleapis.com iap.googleapis.com artifactregistry.googleapis.com; do
    log "Ensuring GCP API ${api} is enabled in ${PROJECT}..."
    if ! gcloud services enable "$api" --project="$PROJECT" --quiet 2>"$err_file"; then
      echo "BLOCKER: enabling API '${api}' in project '${PROJECT}' failed (requires serviceusage.services.enable beyond current grant):" >&2
      cat "$err_file" >&2
      exit 1
    fi
  done
}

create_service_account_with_key() {
  local name="$1"
  local email="$2"
  local key_out="$3"
  if ! gcloud iam service-accounts describe "$email" --project="$PROJECT" >/dev/null 2>&1; then
    log "Creating service account ${email}..."
    gcloud iam service-accounts create "$name" \
      --project="$PROJECT" \
      --display-name="grain IAP E2E (${name})" \
      --quiet >/dev/null
    # Eventual consistency on newly created service accounts before key creation.
    local attempt
    for attempt in $(seq 1 15); do
      if gcloud iam service-accounts describe "$email" --project="$PROJECT" >/dev/null 2>&1; then
        break
      fi
      sleep 2
    done
  fi
  rm -f "$key_out"
  gcloud iam service-accounts keys create "$key_out" \
    --project="$PROJECT" \
    --iam-account="$email" \
    --quiet >/dev/null
  chmod 0600 "$key_out"
}

# allow_sa_a_cmd prints a POSIX sh snippet that puts SA-A on the daemon's
# database allowlist with `grain access allow` once the daemon at $2 answers,
# using the access code in $3/local-api-token. It does nothing on an image
# whose grain ($1) has no `grain access` (v3.1.x keeps the allowlist in
# -ui-allowed-principals). It holds no comma or single quote, so it can sit
# in gcloud's --args and inside sh -c '...'.
allow_sa_a_cmd() {
  local bin="$1" server="$2" data_dir="$3"
  printf '%s' "if ${bin} access --help >/dev/null 2>&1; then i=0; until GRAIN_SERVER=${server} GRAIN_DATA_DIR=${data_dir} ${bin} access allow --reason \"iap e2e\" serviceAccount:${SA_A_EMAIL}; do i=\$((i+1)); [ \$i -ge 120 ] && break; sleep 1; done; fi"
}

deploy_cloud_run_grain() {
  local require_iap="$1"
  local enable_iap="$2"
  local seeded_code="$3"

  local req_flag=""
  if [[ "$require_iap" -eq 1 ]]; then
    req_flag="-ui-require-iap"
  fi
  local iap_flag="--iap"
  if [[ "$enable_iap" -eq 0 ]]; then
    iap_flag="--no-iap"
  fi

  # The daemon runs in the background so the container can put SA-A on
  # the allowlist over loopback with the access code once it answers
  # (allow_sa_a_cmd). Images older than the database allowlist have no
  # `grain access` and take SA-A from -ui-allowed-principals instead.
  # gcloud splits --args on commas, so neither command may contain one.
  local daemon_cmd allow_cmd boot_cmd
  daemon_cmd="/usr/local/bin/grain daemon -data-dir=/tmp/grain-data -disable-native-sandboxes -sandbox-port-base=0 -as=operator@example.com -ui-addr=0.0.0.0:4242 -ui-iap-audience=${IAP_AUDIENCE} -ui-allowed-principals=serviceAccount:${SA_A_EMAIL} ${req_flag}"
  allow_cmd="$(allow_sa_a_cmd /usr/local/bin/grain http://127.0.0.1:4242 /tmp/grain-data)"
  boot_cmd="mkdir -p /tmp/grain-data && printf '%s\n' \"\$GRAIN_E2E_BOOTSTRAP_CODE\" > /tmp/grain-data/local-api-token && chmod 0600 /tmp/grain-data/local-api-token && unset GRAIN_E2E_BOOTSTRAP_CODE && exec /usr/bin/tini -s -- /bin/sh -c '${daemon_cmd} & d=\$!; ${allow_cmd}; wait \$d'"

  local deploy_log="${WORK_TMP}/gcloud_deploy.log"
  log "Deploying ${SERVICE} (${iap_flag}, require_iap=${require_iap}) from ${IMAGE}..."
  if [[ "$DEPLOY_START_EPOCH" -eq 0 ]]; then
    DEPLOY_START_EPOCH="$(date +%s)"
  fi

  # Note: gcloud run deploy --iap automatically attempts to bind
  # service-<PROJECT_NUMBER>@gcp-sa-iap.iam.gserviceaccount.com as roles/run.invoker;
  # if the caller lacks run.services.setIamPolicy, gcloud exits non-zero even
  # when the service revision itself is Ready. Check service readiness after.
  if ! gcloud run deploy "$SERVICE" \
    --project="$PROJECT" \
    --region="$REGION" \
    --image="$IMAGE" \
    --port=4242 \
    --cpu=1 \
    --memory=1Gi \
    --min-instances=1 \
    --max-instances=1 \
    --no-cpu-throttling \
    --no-allow-unauthenticated \
    "$iap_flag" \
    --command="/bin/sh" \
    --args="-c,${boot_cmd}" \
    --set-env-vars="GRAIN_E2E_BOOTSTRAP_CODE=${seeded_code}" \
    --quiet >"$deploy_log" 2>&1; then
    local ready_status
    ready_status="$(gcloud run services describe "$SERVICE" --project="$PROJECT" --region="$REGION" --format='value(status.url)' 2>/dev/null || true)"
    if [[ -z "$ready_status" ]]; then
      log "ERROR: Cloud Run deployment failed:"
      cat "$deploy_log" >&2
      exit 1
    fi
    log "Note: gcloud run deploy exited non-zero after creating revision (inspecting IAM policy binding next)..."
  fi

  SERVICE_URL="$(gcloud run services describe "$SERVICE" --project="$PROJECT" --region="$REGION" --format='value(status.url)')"
  DIRECT_URL="$SERVICE_URL"

  if [[ "$enable_iap" -eq 1 ]]; then
    local iap_sa="service-${PROJECT_NUMBER}@gcp-sa-iap.iam.gserviceaccount.com"
    local iam_err="${WORK_TMP}/run_iam.err"
    if ! gcloud run services add-iam-policy-binding "$SERVICE" \
      --project="$PROJECT" \
      --region="$REGION" \
      --member="serviceAccount:${iap_sa}" \
      --role="roles/run.invoker" \
      --quiet >/dev/null 2>"$iam_err"; then
      log "Note: service-level roles/run.invoker binding returned non-zero ($(tr '\n' ' ' < "$iam_err")); relying on project-level invoker binding if present."
    fi
  fi
}

setup_local_sim() {
  log "Starting local simulation environment (--local-sim)..."
  PROJECT_NUMBER="123456789012"
  IAP_AUDIENCE="/projects/${PROJECT_NUMBER}/locations/${REGION}/services/${SERVICE}"

  # Create RSA keys and service-account JSON files for SA-A and SA-B.
  python3 - "$SA_A_KEY" "$SA_A_EMAIL" "$SA_B_KEY" "$SA_B_EMAIL" "$WORK_TMP" <<'PY'
import json
import subprocess
import sys

sa_a_key, sa_a_email, sa_b_key, sa_b_email, work_tmp = sys.argv[1:6]
for idx, (out_path, email) in enumerate([(sa_a_key, sa_a_email), (sa_b_key, sa_b_email)], start=1):
    pem_path = f"{work_tmp}/priv_{idx}.pem"
    pub_path = f"{work_tmp}/pub_{idx}.pem"
    subprocess.run(["openssl", "genrsa", "-out", pem_path, "2048"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(["openssl", "rsa", "-in", pem_path, "-pubout", "-out", pub_path], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    with open(pem_path, "r", encoding="utf-8") as fh:
        priv_pem = fh.read()
    doc = {
        "type": "service_account",
        "project_id": "local-sim-project",
        "private_key_id": f"sim-key-{idx}",
        "private_key": priv_pem,
        "client_email": email,
    }
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(doc, fh)
PY
  chmod 0600 "$SA_A_KEY" "$SA_B_KEY"

  # Pick 3 ephemeral loopback ports: issuer/JWKS, IAP proxy, grain upstream, direct non-IAP proxy.
  read -r SIM_ISSUER_PORT SIM_PROXY_PORT SIM_UPSTREAM_PORT SIM_DIRECT_PORT < <(
    python3 - <<'PY'
import socket
socks = []
for _ in range(4):
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    socks.append(s)
ports = [s.getsockname()[1] for s in socks]
for s in socks:
    s.close()
print(*ports)
PY
  )

  SERVICE_URL="http://127.0.0.1:${SIM_PROXY_PORT}"
  DIRECT_URL="http://127.0.0.1:${SIM_DIRECT_PORT}"
  SIM_ISSUER_URL="http://127.0.0.1:${SIM_ISSUER_PORT}"
  SIM_UPSTREAM_URL="http://127.0.0.1:${SIM_UPSTREAM_PORT}"

  # Start a local IAP edge simulator that validates RS256 SA self-signed JWTs
  # (aud == "${SERVICE_URL}/*"), checks IAP membership (SA-A and SA-B), strips
  # caller X-Goog-* headers, and signs real ES256 X-Goog-IAP-JWT-Assertion
  # tokens published at ${SIM_ISSUER_URL}/jwks.
  python3 - "$SIM_ISSUER_PORT" "$SIM_PROXY_PORT" "$SIM_DIRECT_PORT" "$SIM_UPSTREAM_URL" "$SERVICE_URL" "$IAP_AUDIENCE" "$SA_A_EMAIL" "$SA_B_EMAIL" "${WORK_TMP}/pub_1.pem" "${WORK_TMP}/pub_2.pem" <<'PY' &
import base64
import http.client
import http.server
import json
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

(
    issuer_port,
    proxy_port,
    direct_port,
    upstream_url,
    service_url,
    iap_aud,
    sa_a_email,
    sa_b_email,
    pub_a_path,
    pub_b_path,
) = sys.argv[1:11]
issuer_port, proxy_port, direct_port = int(issuer_port), int(proxy_port), int(direct_port)
up_parsed = urllib.parse.urlparse(upstream_url)

ec_tmp = tempfile.NamedTemporaryFile("w+", delete=False)
subprocess.run(["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", ec_tmp.name], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
ec_pub_der = subprocess.check_output(["openssl", "ec", "-in", ec_tmp.name, "-pubout", "-outform", "DER"], stderr=subprocess.DEVNULL)
# SubjectPublicKeyInfo for P-256 ends with 0x04 || X(32) || Y(32)
xy = ec_pub_der[-65:]
assert xy[0] == 4
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode("ascii")
jwks_bytes = json.dumps({
    "keys": [{
        "kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "sim-iap-k1",
        "x": b64(xy[1:33]), "y": b64(xy[33:65]),
    }]
}).encode()

def der_to_raw(der: bytes) -> bytes:
    idx = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)
    rl = der[idx + 1]
    r = int.from_bytes(der[idx + 2 : idx + 2 + rl], "big")
    idx = idx + 2 + rl
    sl = der[idx + 1]
    s = int.from_bytes(der[idx + 2 : idx + 2 + sl], "big")
    return r.to_bytes(32, "big") + s.to_bytes(32, "big")

def sign_iap_assertion(email: str) -> str:
    now = int(time.time())
    hdr = {"alg": "ES256", "typ": "JWT", "kid": "sim-iap-k1"}
    claims = {
        "iss": "https://cloud.google.com/iap",
        "sub": f"accounts.google.com:{email}",
        "email": email,
        "aud": iap_aud,
        "iat": now,
        "exp": now + 600,
    }
    inp = f"{b64(json.dumps(hdr, separators=(',', ':')).encode())}.{b64(json.dumps(claims, separators=(',', ':')).encode())}"
    der_sig = subprocess.check_output(["openssl", "dgst", "-sha256", "-sign", ec_tmp.name], input=inp.encode())
    return f"{inp}.{b64(der_to_raw(der_sig))}"

pub_keys = {sa_a_email: pub_a_path, sa_b_email: pub_b_path}
expected_aud = f"{service_url}/*"

def verify_caller_jwt(auth_hdr: str):
    if not auth_hdr or not auth_hdr.startswith("Bearer "):
        return None, "missing bearer token"
    tok = auth_hdr[len("Bearer "):].strip()
    parts = tok.split(".")
    if len(parts) != 3:
        return None, "malformed jwt"
    pad = lambda s: s + "=" * (-len(s) % 4)
    try:
        hdr = json.loads(base64.urlsafe_b64decode(pad(parts[0])))
        claims = json.loads(base64.urlsafe_b64decode(pad(parts[1])))
        sig = base64.urlsafe_b64decode(pad(parts[2]))
    except Exception as exc:
        return None, f"decode error: {exc}"
    if hdr.get("alg") != "RS256":
        return None, "unsupported alg"
    email = claims.get("iss")
    if not email or claims.get("sub") != email or email not in pub_keys:
        return None, "unknown issuer"
    if claims.get("aud") != expected_aud:
        return None, "Invalid JWT audience"
    now = int(time.time())
    if int(claims.get("exp", 0)) <= now:
        return None, "JWT expired"
    with tempfile.NamedTemporaryFile("wb", delete=True) as sig_f:
        sig_f.write(sig)
        sig_f.flush()
        res = subprocess.run(
            ["openssl", "dgst", "-sha256", "-verify", pub_keys[email], "-signature", sig_f.name],
            input=f"{parts[0]}.{parts[1]}".encode("ascii"),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if res.returncode != 0:
            return None, "invalid signature"
    return email, None

def forward_to_upstream(handler, extra_headers, strip_x_goog):
    length = int(handler.headers.get("Content-Length", "0"))
    body = handler.rfile.read(length) if length > 0 else b""
    fwd_headers = {}
    for k, v in handler.headers.items():
        lk = k.lower()
        if lk in ("connection", "transfer-encoding", "content-length"):
            continue
        if strip_x_goog and lk.startswith("x-goog-"):
            continue
        fwd_headers[k] = v
    fwd_headers["Host"] = handler.headers.get("Host", f"127.0.0.1:{proxy_port}")
    fwd_headers["X-Forwarded-For"] = "203.0.113.42"
    fwd_headers["X-Forwarded-Proto"] = "https"
    for k, v in extra_headers.items():
        fwd_headers[k] = v
    conn = http.client.HTTPConnection(up_parsed.hostname, up_parsed.port, timeout=30)
    try:
        conn.request(handler.command, handler.path, body=body, headers=fwd_headers)
        resp = conn.getresponse()
        resp_body = resp.read()
    except OSError:
        handler.send_response(503)
        handler.end_headers()
        conn.close()
        return
    handler.send_response(resp.status)
    for k, v in resp.getheaders():
        if k.lower() in ("transfer-encoding", "connection"):
            continue
        handler.send_header(k, v)
    handler.end_headers()
    handler.wfile.write(resp_body)
    conn.close()

class IssuerHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path == "/jwks":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(jwks_bytes)
        else:
            self.send_response(404)
            self.end_headers()

class IAPProxyHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def _handle(self):
        auth = self.headers.get("Authorization", "")
        if not auth:
            accept = self.headers.get("Accept", "")
            if self.command == "GET" and "text/html" in accept and not self.path.startswith("/api/"):
                self.send_response(302)
                self.send_header("Location", "https://accounts.google.com/o/oauth2/v2/auth")
                self.send_header("X-Goog-IAP-Generated-Response", "true")
                self.end_headers()
                return
            msg = b"Invalid IAP credentials: empty token"
            self.send_response(401)
            self.send_header("X-Goog-IAP-Generated-Response", "true")
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)
            return
        email, err = verify_caller_jwt(auth)
        if err is not None:
            msg = f"Invalid IAP credentials: {err}".encode()
            self.send_response(401)
            self.send_header("X-Goog-IAP-Generated-Response", "true")
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)
            return
        assertion = sign_iap_assertion(email)
        forward_to_upstream(
            self,
            {
                "X-Goog-IAP-JWT-Assertion": assertion,
                "X-Goog-Authenticated-User-Email": f"accounts.google.com:{email}",
                "Authorization": "Bearer iap-service-agent-invoker-token",
            },
            strip_x_goog=True,
        )
    do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = _handle

class DirectNetworkHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def _handle(self):
        forward_to_upstream(self, {}, strip_x_goog=False)
    do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = _handle

for port, cls in [(issuer_port, IssuerHandler), (proxy_port, IAPProxyHandler), (direct_port, DirectNetworkHandler)]:
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), cls)
    threading.Thread(target=srv.serve_forever, daemon=True).start()

while True:
    time.sleep(3600)
PY
  SIM_PIDS+=("$!")

  local attempt
  for attempt in $(seq 1 30); do
    if curl -fsS "${SIM_ISSUER_URL}/jwks" >/dev/null 2>&1; then
      break
    fi
    sleep 0.2
  done
}

SIM_DAEMON_PID=""
deploy_local_sim_grain() {
  local require_iap="$1"
  local seeded_code="$2"

  if [[ -n "$SIM_DAEMON_PID" ]] && kill -0 "$SIM_DAEMON_PID" 2>/dev/null; then
    kill "$SIM_DAEMON_PID" 2>/dev/null || true
    wait "$SIM_DAEMON_PID" 2>/dev/null || true
  fi

  local data_dir="${WORK_TMP}/sim-data-${require_iap}"
  mkdir -p "$data_dir"
  printf '%s\n' "$seeded_code" > "${data_dir}/local-api-token"
  chmod 0600 "${data_dir}/local-api-token"

  local extra_args=()
  if [[ "$require_iap" -eq 1 ]]; then
    extra_args+=("-ui-require-iap")
  fi

  "$GRAIN_BIN" daemon \
    -data-dir="$data_dir" \
    -disable-native-sandboxes \
    -sandbox-port-base=0 \
    -as=operator@example.com \
    -ui-addr="127.0.0.1:${SIM_UPSTREAM_PORT}" \
    -ui-allowed-hosts="127.0.0.1:${SIM_PROXY_PORT},127.0.0.1:${SIM_DIRECT_PORT}" \
    -ui-iap-audience="$IAP_AUDIENCE" \
    -ui-iap-issuer="https://cloud.google.com/iap" \
    -ui-iap-keys-url="${SIM_ISSUER_URL}/jwks" \
    -ui-allowed-principals="serviceAccount:${SA_A_EMAIL}" \
    "${extra_args[@]}" \
    >"${WORK_TMP}/sim-daemon-${require_iap}.log" 2>&1 &
  SIM_DAEMON_PID="$!"
  SIM_PIDS+=("$SIM_DAEMON_PID")
  sh -c "$(allow_sa_a_cmd "$GRAIN_BIN" "http://127.0.0.1:${SIM_UPSTREAM_PORT}" "$data_dir")" \
    >>"${WORK_TMP}/sim-daemon-${require_iap}.log" 2>&1

  local attempt tok
  tok="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}/*")"
  for attempt in $(seq 1 50); do
    http_call -H "Authorization: Bearer ${tok}" "${SERVICE_URL}/api/auth/user" || true
    if [[ "$HTTP_STATUS" == "200" ]]; then
      return 0
    fi
    sleep 0.2
  done
  log "ERROR: simulated grain daemon did not become ready (last HTTP status ${HTTP_STATUS})"
  cat "${WORK_TMP}/sim-daemon-${require_iap}.log" >&2 || true
  exit 1
}

wait_for_iap_ready() {
  log "Waiting for Cloud Run + IAP IAM propagation at ${SERVICE_URL}..."
  local attempt tok body_preview
  for attempt in $(seq 1 36); do
    tok="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}/*")"
    http_call -H "Authorization: Bearer ${tok}" "${SERVICE_URL}/api/auth/user" || true
    if [[ "$HTTP_STATUS" == "200" ]]; then
      log "IAP + grain ready after ${attempt} attempt(s)."
      return 0
    fi
    if [[ "$HTTP_STATUS" == "403" ]] && ! header_present "x-goog-iap-generated-response"; then
      body_preview="$(head -c 300 "$HTTP_BODY_FILE" | tr '\n' ' ')"
      if grep -q "is not on the allowed users and groups list" "$HTTP_BODY_FILE"; then
        echo "BLOCKER: IAP admitted SA-A and grain verified it, but grain refused it: ${body_preview}" >&2
        echo "Cause: SA-A is not on grain's allowlist. On images that keep it in the database the container adds it with 'grain access allow serviceAccount:${SA_A_EMAIL} --reason \"iap e2e\"' once the daemon answers; check the Cloud Run logs for that command's error. Images from 992714cd until service accounts were allowed back refuse serviceAccount: entries outright -- see scripts/iap-e2e/README.md." >&2
        exit 1
      fi
      if grep -qi "Error: Forbidden" "$HTTP_BODY_FILE" && ! jq -e . "$HTTP_BODY_FILE" >/dev/null 2>&1; then
        echo "BLOCKER: IAP admitted SA-A, but Cloud Run ingress returned 403 Forbidden before reaching grain." >&2
        echo "Cause: service-${PROJECT_NUMBER}@gcp-sa-iap.iam.gserviceaccount.com lacks roles/run.invoker (run.routes.invoke) on ${SERVICE}, and the active credential lacks run.services.setIamPolicy (roles/run.admin)." >&2
        exit 1
      fi
      log "Unexpected 403 from upstream: ${body_preview}"
    fi
    sleep 5
  done
  echo "ERROR: timed out waiting for SA-A admission through IAP at ${SERVICE_URL} (last status ${HTTP_STATUS})" >&2
  head -c 400 "$HTTP_BODY_FILE" >&2 || true
  exit 1
}

# ---------------------------------------------------------------------------
# Main Execution
# ---------------------------------------------------------------------------

ensure_grain_bin
SEEDED_ACCESS_CODE="$(generate_access_code)"

if [[ "$LOCAL_SIM" -eq 1 ]]; then
  DEPLOY_START_EPOCH="$(date +%s)"
  setup_local_sim
  deploy_local_sim_grain 0 "$SEEDED_ACCESS_CODE"
else
  enable_gcp_apis
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')"
  if [[ -z "$PROJECT_NUMBER" ]]; then
    echo "error: could not resolve projectNumber for project '${PROJECT}'" >&2
    exit 1
  fi
  IAP_AUDIENCE="/projects/${PROJECT_NUMBER}/locations/${REGION}/services/${SERVICE}"

  create_service_account_with_key "$SA_A_NAME" "$SA_A_EMAIL" "$SA_A_KEY"
  create_service_account_with_key "$SA_B_NAME" "$SA_B_EMAIL" "$SA_B_KEY"

  deploy_cloud_run_grain 0 1 "$SEEDED_ACCESS_CODE"

  for sa in "$SA_A_EMAIL" "$SA_B_EMAIL"; do
    log "Granting roles/iap.httpsResourceAccessor on ${SERVICE} to ${sa}..."
    gcloud iap web add-iam-policy-binding \
      --project="$PROJECT" \
      --resource-type=cloud-run \
      --region="$REGION" \
      --service="$SERVICE" \
      --member="serviceAccount:${sa}" \
      --role="roles/iap.httpsResourceAccessor" \
      --quiet >/dev/null
  done

  wait_for_iap_ready
fi

SA_A_TOKEN="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}/*")"
SA_B_TOKEN="$(mint_sa_jwt "$SA_B_KEY" "${SERVICE_URL}/*")"

# Positive control for Case 6 before enabling -ui-require-iap: verify the
# seeded access code is genuinely valid on this daemon configuration and
# capture a real grain_access session cookie.
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Origin: ${SERVICE_URL}" \
  -H "Content-Type: application/json" \
  -d "{\"code\":\"${SEEDED_ACCESS_CODE}\"}" \
  "${SERVICE_URL}/api/auth/access-code"
REV1_CODE_POST_STATUS="$HTTP_STATUS"
SEEDED_ACCESS_COOKIE="$(extract_cookie_pair "grain_access" || true)"

# ---------------------------------------------------------------------------
# Case 1: SA-A admitted via API and CLI with no access code
# ---------------------------------------------------------------------------
c1_ok=1
c1_notes=()

http_call -H "Authorization: Bearer ${SA_A_TOKEN}" "${SERVICE_URL}/api/auth/user"
if [[ "$HTTP_STATUS" == "200" ]]; then
  auth_user="$(jq -r '.user // empty' "$HTTP_BODY_FILE")"
  auth_method="$(jq -r '.authMethod // empty' "$HTTP_BODY_FILE")"
  auth_bool="$(jq -r '.authenticated // false' "$HTTP_BODY_FILE")"
  if [[ "$auth_bool" == "true" && "$auth_user" == "$SA_A_EMAIL" && "$auth_method" == "iap" ]]; then
    c1_notes+=("GET /api/auth/user -> 200 (authenticated=true, user=${auth_user}, authMethod=${auth_method})")
  else
    c1_ok=0
    c1_notes+=("GET /api/auth/user unexpected payload: $(tr -d '\n' < "$HTTP_BODY_FILE")")
  fi
else
  c1_ok=0
  c1_notes+=("GET /api/auth/user -> ${HTTP_STATUS}")
fi

http_call -H "Authorization: Bearer ${SA_A_TOKEN}" "${SERVICE_URL}/api/whoami"
if [[ "$HTTP_STATUS" == "200" ]]; then
  # Newer daemons nest the principal under .actor (operator-attribution
  # spec §5.2); older ones put it at the top level.
  w_user="$(jq -r '.actor.id // .principal // .email // .user // empty' "$HTTP_BODY_FILE")"
  w_kind="$(jq -r '.actor.kind // .kind // empty' "$HTTP_BODY_FILE")"
  w_src="$(jq -r '.source // .actor.source // .authMethod // empty' "$HTTP_BODY_FILE")"
  c1_notes+=("GET /api/whoami -> 200 (principal=${w_user}, kind=${w_kind}, source=${w_src})")
  if [[ "$w_user" != *"$SA_A_EMAIL"* || "$w_kind" != "service" || "$w_src" != "iap" ]]; then
    c1_ok=0
  fi
elif [[ "$HTTP_STATUS" == "404" ]]; then
  c1_notes+=("GET /api/whoami -> 404 (not exposed on main; principal/kind/source verified via /api/auth/user + task attribution)")
else
  c1_ok=0
  c1_notes+=("GET /api/whoami -> unexpected ${HTTP_STATUS}")
fi

cli_list_out="${WORK_TMP}/cli_list.json"
cli_list_err="${WORK_TMP}/cli_list.err"
if GOOGLE_APPLICATION_CREDENTIALS="$SA_A_KEY" \
   GRAIN_AUDIENCE="${SERVICE_URL}/*" \
   GRAIN_TOKEN="" GRAIN_AUTH_TOKEN="" GRAIN_ID_TOKEN="" GRAIN_TUNNEL_TOKEN="" \
   "$GRAIN_BIN" -server "$SERVICE_URL" -json list >"$cli_list_out" 2>"$cli_list_err"; then
  if jq -e 'type == "array"' "$cli_list_out" >/dev/null 2>&1; then
    c1_notes+=("grain CLI (-server ${SERVICE_URL}, GRAIN_AUDIENCE=${SERVICE_URL}/*, no access code) -> exit 0, JSON array")
  else
    c1_ok=0
    c1_notes+=("grain CLI output was not a JSON array")
  fi
else
  c1_ok=0
  c1_notes+=("grain CLI failed: $(tr '\n' ' ' < "$cli_list_err")")
fi

if [[ "$c1_ok" -eq 1 ]]; then
  record_case 1 "sa-a-admitted" "SA-A admitted to API and CLI through IAP with no access code" "PASS" "$(IFS='; '; echo "${c1_notes[*]}")"
else
  record_case 1 "sa-a-admitted" "SA-A admitted to API and CLI through IAP with no access code" "FAIL" "$(IFS='; '; echo "${c1_notes[*]}")"
fi

# ---------------------------------------------------------------------------
# Case 2: SA-B admitted by IAP and refused by grain (403 API + HTML refusal page)
# ---------------------------------------------------------------------------
c2_ok=1
c2_notes=()

http_call -H "Authorization: Bearer ${SA_B_TOKEN}" "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]] && ! header_present "x-goog-iap-generated-response"; then
  err_msg="$(jq -r '.error // empty' "$HTTP_BODY_FILE" 2>/dev/null || true)"
  if [[ "$err_msg" == *"$SA_B_EMAIL"* ]]; then
    c2_notes+=("GET /api/tasks as SA-B -> 403 JSON from grain naming ${SA_B_EMAIL}")
  else
    c2_ok=0
    c2_notes+=("GET /api/tasks 403 body did not name ${SA_B_EMAIL}: ${err_msg}")
  fi
else
  c2_ok=0
  c2_notes+=("GET /api/tasks as SA-B -> ${HTTP_STATUS} (iap-generated=$(header_present x-goog-iap-generated-response && echo yes || echo no))")
fi

http_call \
  -H "Authorization: Bearer ${SA_B_TOKEN}" \
  -H "Accept: text/html,application/xhtml+xml" \
  "${SERVICE_URL}/"
if [[ "$HTTP_STATUS" == "403" ]] && grep -qF "$SA_B_EMAIL" "$HTTP_BODY_FILE" && grep -qi "verified by iap" "$HTTP_BODY_FILE"; then
  c2_notes+=("GET / (Accept: text/html) as SA-B -> 403 HTML refusal page ('Signed in as ${SA_B_EMAIL} (verified by iap)')")
else
  c2_ok=0
  c2_notes+=("GET / HTML as SA-B -> ${HTTP_STATUS} (missing SA-B email or 'verified by iap' in refusal page)")
fi

if [[ "$c2_ok" -eq 1 ]]; then
  record_case 2 "sa-b-refused-by-grain" "SA-B admitted by IAP and refused by grain (403 JSON + HTML refusal page)" "PASS" "$(IFS='; '; echo "${c2_notes[*]}")"
else
  record_case 2 "sa-b-refused-by-grain" "SA-B admitted by IAP and refused by grain (403 JSON + HTML refusal page)" "FAIL" "$(IFS='; '; echo "${c2_notes[*]}")"
fi

# ---------------------------------------------------------------------------
# Case 3: No token — IAP refuses with 401 or 302 redirect; grain never reached
# ---------------------------------------------------------------------------
c3_ok=1
c3_notes=()

http_call "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "401" || "$HTTP_STATUS" == "302" ]] && header_present "x-goog-iap-generated-response"; then
  c3_notes+=("GET /api/tasks (no token) -> ${HTTP_STATUS} with X-Goog-IAP-Generated-Response: true")
else
  c3_ok=0
  c3_notes+=("GET /api/tasks (no token) -> ${HTTP_STATUS} (missing X-Goog-IAP-Generated-Response)")
fi

http_call -H "Accept: text/html,application/xhtml+xml" "${SERVICE_URL}/"
if [[ "$HTTP_STATUS" == "302" || "$HTTP_STATUS" == "401" ]] && header_present "x-goog-iap-generated-response"; then
  loc_hdr="$(grep -i '^location:' "$HTTP_HEADERS_FILE" | head -n1 | tr -d '\r' | cut -d' ' -f2- || true)"
  c3_notes+=("GET / HTML (no token) -> ${HTTP_STATUS} with X-Goog-IAP-Generated-Response: true${loc_hdr:+ (Location: ${loc_hdr%%\?*})}")
else
  c3_ok=0
  c3_notes+=("GET / HTML (no token) -> ${HTTP_STATUS} (missing X-Goog-IAP-Generated-Response)")
fi

if [[ "$c3_ok" -eq 1 ]]; then
  record_case 3 "no-token-refused-by-iap" "Unauthenticated requests refused at IAP edge before reaching grain" "PASS" "$(IFS='; '; echo "${c3_notes[*]}")"
else
  record_case 3 "no-token-refused-by-iap" "Unauthenticated requests refused at IAP edge before reaching grain" "FAIL" "$(IFS='; '; echo "${c3_notes[*]}")"
fi

# ---------------------------------------------------------------------------
# Case 4: Expired or wrong-audience ID token refused
# ---------------------------------------------------------------------------
c4_ok=1
c4_notes=()

EXPIRED_TOKEN="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}/*" -1200 -600)"
http_call -H "Authorization: Bearer ${EXPIRED_TOKEN}" "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "401" ]] && header_present "x-goog-iap-generated-response"; then
  c4_notes+=("expired SA-A JWT (exp=now-600s) -> 401 at IAP edge")
else
  c4_ok=0
  c4_notes+=("expired SA-A JWT -> unexpected ${HTTP_STATUS}")
fi

WRONG_AUD_TOKEN="$(mint_sa_jwt "$SA_A_KEY" "https://wrong-audience.example.com/*")"
http_call -H "Authorization: Bearer ${WRONG_AUD_TOKEN}" "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "401" ]] && header_present "x-goog-iap-generated-response"; then
  c4_notes+=("wrong-audience SA-A JWT (aud=https://wrong-audience.example.com/*) -> 401 at IAP edge")
else
  c4_ok=0
  c4_notes+=("wrong-audience SA-A JWT -> unexpected ${HTTP_STATUS}")
fi

NO_WILDCARD_AUD_TOKEN="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}")"
http_call -H "Authorization: Bearer ${NO_WILDCARD_AUD_TOKEN}" "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "401" ]] && header_present "x-goog-iap-generated-response"; then
  c4_notes+=("missing-wildcard SA-A JWT (aud=${SERVICE_URL}) -> 401 at IAP edge")
else
  c4_ok=0
  c4_notes+=("missing-wildcard SA-A JWT -> unexpected ${HTTP_STATUS}")
fi

if [[ "$c4_ok" -eq 1 ]]; then
  record_case 4 "bad-token-refused" "Expired and wrong-audience tokens refused with 401" "PASS" "$(IFS='; '; echo "${c4_notes[*]}")"
else
  record_case 4 "bad-token-refused" "Expired and wrong-audience tokens refused with 401" "FAIL" "$(IFS='; '; echo "${c4_notes[*]}")"
fi

# ---------------------------------------------------------------------------
# Case 5: Forged X-Goog-IAP-JWT-Assertion stripped or ignored, no admission
# ---------------------------------------------------------------------------
c5_ok=1
c5_notes=()
FORGED_ASSERTION="$(mint_forged_iap_assertion "$SA_A_EMAIL" "$IAP_AUDIENCE")"

# 5a: Unauthenticated caller supplying forged X-Goog-IAP-JWT-Assertion for SA-A
http_call \
  -H "X-Goog-IAP-JWT-Assertion: ${FORGED_ASSERTION}" \
  -H "X-Goog-Authenticated-User-Email: accounts.google.com:${SA_A_EMAIL}" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "401" || "$HTTP_STATUS" == "302" ]] && header_present "x-goog-iap-generated-response"; then
  c5_notes+=("unauthenticated + forged X-Goog-IAP-JWT-Assertion -> ${HTTP_STATUS} at IAP edge")
else
  c5_ok=0
  c5_notes+=("unauthenticated + forged assertion -> unexpected ${HTTP_STATUS}")
fi

# 5b: Authenticated SA-B caller injecting forged X-Goog-IAP-JWT-Assertion +
# plaintext spoof headers claiming SA-A. IAP strips caller X-Goog-* headers
# and injects SA-B's real assertion; grain refuses SA-B with 403.
http_call \
  -H "Authorization: Bearer ${SA_B_TOKEN}" \
  -H "X-Goog-IAP-JWT-Assertion: ${FORGED_ASSERTION}" \
  -H "X-Goog-Authenticated-User-Email: accounts.google.com:${SA_A_EMAIL}" \
  -H "X-Forwarded-User: ${SA_A_EMAIL}" \
  -H "X-Grain-User: ${SA_A_EMAIL}" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]] && grep -qF "$SA_B_EMAIL" "$HTTP_BODY_FILE"; then
  c5_notes+=("SA-B + forged X-Goog-IAP-JWT-Assertion claiming SA-A -> stripped by IAP, refused 403 as ${SA_B_EMAIL}")
else
  c5_ok=0
  c5_notes+=("SA-B + forged assertion claiming SA-A -> unexpected ${HTTP_STATUS}: $(head -c 200 "$HTTP_BODY_FILE" | tr '\n' ' ')")
fi

# Case 5 status is finalized after Case 6 also checks direct-to-grain forged assertion verification when available.

# ---------------------------------------------------------------------------
# Case 7: CSRF — cross-origin state-changing requests refused
# ---------------------------------------------------------------------------
c7_ok=1
c7_notes=()
csrf_payload='{"title":"CSRF probe (must not be created)","body":"cross-origin write test","noRepo":true}'

# 7a: Cross-origin Origin header
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Origin: https://evil.example" \
  -H "Content-Type: application/json" \
  -d "$csrf_payload" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]]; then
  c7_notes+=("POST /api/tasks with Origin: https://evil.example -> 403")
else
  c7_ok=0
  c7_notes+=("Origin: https://evil.example -> unexpected ${HTTP_STATUS}")
fi

# 7b: Cross-site Sec-Fetch-Site without Origin
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Sec-Fetch-Site: cross-site" \
  -H "Content-Type: application/json" \
  -d "$csrf_payload" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]]; then
  c7_notes+=("Sec-Fetch-Site: cross-site -> 403")
else
  c7_ok=0
  c7_notes+=("Sec-Fetch-Site: cross-site -> unexpected ${HTTP_STATUS}")
fi

# 7c: Sandboxed iframe Origin: null
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Origin: null" \
  -H "Content-Type: application/json" \
  -d "$csrf_payload" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]]; then
  c7_notes+=("Origin: null -> 403")
else
  c7_ok=0
  c7_notes+=("Origin: null -> unexpected ${HTTP_STATUS}")
fi

# 7d: Cookie-bearing browser POST missing both Origin and Sec-Fetch-Site
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Cookie: GCP_IAP_UID=1" \
  -H "Content-Type: application/json" \
  -d "$csrf_payload" \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "403" ]]; then
  c7_notes+=("Cookie-bearing POST with no Origin/Sec-Fetch-Site -> 403")
else
  c7_ok=0
  c7_notes+=("Cookie-bearing POST with no Origin/Sec-Fetch-Site -> unexpected ${HTTP_STATUS}")
fi

# 7e: Same-origin positive control
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Origin: ${SERVICE_URL}" \
  -H "Sec-Fetch-Site: same-origin" \
  -H "Content-Type: application/json" \
  -d '{"title":"Same-origin CSRF positive control","body":"allowed","noRepo":true}' \
  "${SERVICE_URL}/api/tasks"
if [[ "$HTTP_STATUS" == "201" ]]; then
  c7_notes+=("same-origin POST (Origin: ${SERVICE_URL}) -> 201")
else
  c7_ok=0
  c7_notes+=("same-origin POST -> unexpected ${HTTP_STATUS}")
fi

# ---------------------------------------------------------------------------
# Case 8: Attribution — task filed, commented, and closed by SA-A is recorded
# as SA-A (kind=service, source=iap), not operator
# ---------------------------------------------------------------------------
c8_ok=1
c8_notes=()

cli_create_out="${WORK_TMP}/cli_create.json"
cli_create_err="${WORK_TMP}/cli_create.err"
if GOOGLE_APPLICATION_CREDENTIALS="$SA_A_KEY" \
   GRAIN_AUDIENCE="${SERVICE_URL}/*" \
   GRAIN_TOKEN="" GRAIN_AUTH_TOKEN="" GRAIN_ID_TOKEN="" GRAIN_TUNNEL_TOKEN="" \
   "$GRAIN_BIN" -server "$SERVICE_URL" -json create \
     -no-repo \
     -title "IAP E2E attribution verification" \
     -body "Filed by SA-A through Cloud Run Direct IAP" >"$cli_create_out" 2>"$cli_create_err"; then
  created_id="$(jq -r '.id // .task.id // empty' "$cli_create_out")"
  if [[ -n "$created_id" ]]; then
    GOOGLE_APPLICATION_CREDENTIALS="$SA_A_KEY" \
      GRAIN_AUDIENCE="${SERVICE_URL}/*" \
      "$GRAIN_BIN" -server "$SERVICE_URL" comment "$created_id" "Comment from SA-A over IAP" >/dev/null 2>&1 || c8_ok=0
    GOOGLE_APPLICATION_CREDENTIALS="$SA_A_KEY" \
      GRAIN_AUDIENCE="${SERVICE_URL}/*" \
      "$GRAIN_BIN" -server "$SERVICE_URL" close "$created_id" >/dev/null 2>&1 || c8_ok=0

    http_call -H "Authorization: Bearer ${SA_A_TOKEN}" "${SERVICE_URL}/api/tasks/${created_id}"
    if [[ "$HTTP_STATUS" == "200" ]]; then
      t_author="$(jq -r '.author // empty' "$HTTP_BODY_FILE")"
      t_kind="$(jq -r '.authorKind // empty' "$HTTP_BODY_FILE")"
      c_author="$(jq -r '.comments[0].author // empty' "$HTTP_BODY_FILE")"
      c_kind="$(jq -r '.comments[0].authorKind // empty' "$HTTP_BODY_FILE")"
      tr_by="$(jq -r '.transitions[-1].by // empty' "$HTTP_BODY_FILE")"
      # Newer daemons say who closed the task in actor/actorKind and leave
      # by as the bare ID; older ones wrote by as "<kind> <id>".
      tr_actor="$(jq -r '.transitions[-1] | if .actorKind then "\(.actorKind) \(.actor)" else empty end' "$HTTP_BODY_FILE")"
      if [[ -n "$tr_actor" ]]; then
        tr_by="$tr_actor"
      fi
      if [[ "$t_author" == "$SA_A_EMAIL" && "$t_kind" == "service" && "$c_author" == "$SA_A_EMAIL" && "$c_kind" == "service" && "$tr_by" == "service ${SA_A_EMAIL}" ]]; then
        c8_notes+=("task #${created_id}: author=${t_author} (authorKind=${t_kind}), comment.author=${c_author} (${c_kind}), close transition.by='${tr_by}' (daemon -as=operator@example.com not used)")
      else
        c8_ok=0
        c8_notes+=("task #${created_id} attribution mismatch: author=${t_author}/${t_kind}, comment=${c_author}/${c_kind}, transition.by=${tr_by}")
      fi
    else
      c8_ok=0
      c8_notes+=("GET /api/tasks/${created_id} -> ${HTTP_STATUS}")
    fi
  else
    c8_ok=0
    c8_notes+=("grain create returned empty task id: $(tr '\n' ' ' < "$cli_create_out")")
  fi
else
  c8_ok=0
  c8_notes+=("grain create as SA-A failed: $(tr '\n' ' ' < "$cli_create_err")")
fi

# ---------------------------------------------------------------------------
# Case 6: Redeploy with -ui-require-iap and verify access code is refused over
# the network (both through IAP and directly without IAP)
# ---------------------------------------------------------------------------
c6_ok=1
c6_notes=()

if [[ "$REV1_CODE_POST_STATUS" == "200" && -n "$SEEDED_ACCESS_COOKIE" ]]; then
  c6_notes+=("baseline without -ui-require-iap accepted seeded access code (200 + grain_access cookie)")
else
  c6_ok=0
  c6_notes+=("baseline access-code control failed before redeploy (status=${REV1_CODE_POST_STATUS})")
fi

if [[ "$LOCAL_SIM" -eq 1 ]]; then
  deploy_local_sim_grain 1 "$SEEDED_ACCESS_CODE"
else
  deploy_cloud_run_grain 1 1 "$SEEDED_ACCESS_CODE"
  wait_for_iap_ready
fi

SA_A_TOKEN="$(mint_sa_jwt "$SA_A_KEY" "${SERVICE_URL}/*")"
SA_B_TOKEN="$(mint_sa_jwt "$SA_B_KEY" "${SERVICE_URL}/*")"

# 6a: GET /api/auth/access-code reports requireIAP=true, accessCodeAccepted=false
http_call -H "Authorization: Bearer ${SA_A_TOKEN}" "${SERVICE_URL}/api/auth/access-code"
if [[ "$HTTP_STATUS" == "200" ]]; then
  req_iap="$(jq -r '.requireIAP | tostring' "$HTTP_BODY_FILE")"
  acc_code="$(jq -r '.accessCodeAccepted | tostring' "$HTTP_BODY_FILE")"
  if [[ "$req_iap" == "true" && "$acc_code" == "false" ]]; then
    c6_notes+=("GET /api/auth/access-code -> 200 (requireIAP=true, accessCodeAccepted=false)")
  else
    c6_ok=0
    c6_notes+=("GET /api/auth/access-code unexpected body: $(tr -d '\n' < "$HTTP_BODY_FILE")")
  fi
else
  c6_ok=0
  c6_notes+=("GET /api/auth/access-code -> ${HTTP_STATUS}")
fi

# 6b: POST /api/auth/access-code with the valid seeded code is refused with 403 over the network
http_call \
  -X POST \
  -H "Authorization: Bearer ${SA_A_TOKEN}" \
  -H "Origin: ${SERVICE_URL}" \
  -H "Content-Type: application/json" \
  -d "{\"code\":\"${SEEDED_ACCESS_CODE}\"}" \
  "${SERVICE_URL}/api/auth/access-code"
if [[ "$HTTP_STATUS" == "403" ]] && grep -q -- "-ui-require-iap" "$HTTP_BODY_FILE"; then
  c6_notes+=("POST /api/auth/access-code with valid code -> 403 (-ui-require-iap)")
else
  c6_ok=0
  c6_notes+=("POST /api/auth/access-code with valid code -> unexpected ${HTTP_STATUS}")
fi

# 6c: Valid grain_access session cookie cannot admit non-allowlisted SA-B under -ui-require-iap
if [[ -n "$SEEDED_ACCESS_COOKIE" ]]; then
  http_call \
    -H "Authorization: Bearer ${SA_B_TOKEN}" \
    -H "Cookie: ${SEEDED_ACCESS_COOKIE}" \
    "${SERVICE_URL}/api/tasks"
  if [[ "$HTTP_STATUS" == "403" ]]; then
    c6_notes+=("SA-B + valid grain_access cookie -> 403 (cookie ignored under -ui-require-iap)")
  else
    c6_ok=0
    c6_notes+=("SA-B + valid grain_access cookie -> unexpected ${HTTP_STATUS}")
  fi
fi

# 6d + 5c: Direct network leg without IAP in front (verifying grain's own gate
# refuses bearer access code, grain_access cookie, HTML login page, and forged
# X-Goog-IAP-JWT-Assertion directly against Google's public keys).
run_direct_leg() {
  local base_url="$1"
  local invoker_bearer="${2:-}"

  if [[ -z "$invoker_bearer" ]]; then
    http_call -H "Authorization: Bearer ${SEEDED_ACCESS_CODE}" "${base_url}/api/tasks"
    if [[ "$HTTP_STATUS" == "401" ]] && grep -q -- "-ui-require-iap" "$HTTP_BODY_FILE"; then
      c6_notes+=("direct network Bearer <access-code> -> 401 (-ui-require-iap)")
    elif [[ "$LOCAL_SIM" -eq 0 && "$HTTP_STATUS" == "401" ]] && ! jq -e . "$HTTP_BODY_FILE" >/dev/null 2>&1; then
      # Cloud Run's ingress validates every Authorization: Bearer as a Google
      # ID token, even with allUsers as invoker, so a bearer access code is
      # refused before it can reach grain on *.run.app.
      c6_notes+=("direct network Bearer <access-code> -> 401 at Cloud Run ingress (never reaches grain)")
    else
      c6_ok=0
      c6_notes+=("direct network Bearer <access-code> -> unexpected ${HTTP_STATUS}")
    fi
  fi

  # X-Grain-Access-Code passes Cloud Run's ingress, so grain itself must
  # refuse it. Images that predate the header ignore it and still answer 401.
  http_call "${auth_args[@]}" -H "X-Grain-Access-Code: ${SEEDED_ACCESS_CODE}" "${base_url}/api/tasks"
  if [[ "$HTTP_STATUS" == "401" ]] && grep -q -- "-ui-require-iap" "$HTTP_BODY_FILE"; then
    c6_notes+=("direct network X-Grain-Access-Code -> 401 (-ui-require-iap)")
  else
    c6_ok=0
    c6_notes+=("direct network X-Grain-Access-Code -> unexpected ${HTTP_STATUS}")
  fi

  local auth_args=()
  if [[ -n "$invoker_bearer" ]]; then
    auth_args=(-H "Authorization: Bearer ${invoker_bearer}")
  fi

  if [[ -n "$SEEDED_ACCESS_COOKIE" ]]; then
    http_call "${auth_args[@]}" -H "Cookie: ${SEEDED_ACCESS_COOKIE}" "${base_url}/api/tasks"
    if [[ "$HTTP_STATUS" == "401" ]] && grep -q -- "-ui-require-iap" "$HTTP_BODY_FILE"; then
      c6_notes+=("direct network Cookie grain_access -> 401 (-ui-require-iap)")
    else
      c6_ok=0
      c6_notes+=("direct network Cookie grain_access -> unexpected ${HTTP_STATUS}")
    fi
  fi

  http_call "${auth_args[@]}" -H "Accept: text/html,application/xhtml+xml" "${base_url}/"
  if [[ "$HTTP_STATUS" == "401" ]] && ! grep -qi "<input" "$HTTP_BODY_FILE"; then
    c6_notes+=("direct network GET / HTML -> 401 IAP-required page (no access-code form)")
  else
    c6_ok=0
    c6_notes+=("direct network GET / HTML -> unexpected ${HTTP_STATUS}")
  fi

  http_call \
    "${auth_args[@]}" \
    -H "X-Goog-IAP-JWT-Assertion: ${FORGED_ASSERTION}" \
    -H "X-Goog-Authenticated-User-Email: accounts.google.com:${SA_A_EMAIL}" \
    "${base_url}/api/tasks"
  if [[ "$HTTP_STATUS" == "401" ]]; then
    c5_notes+=("direct-to-grain forged X-Goog-IAP-JWT-Assertion -> 401 signature rejected by grain verifier")
  else
    c5_ok=0
    c5_notes+=("direct-to-grain forged assertion -> unexpected ${HTTP_STATUS}")
  fi
}

if [[ "$LOCAL_SIM" -eq 1 ]]; then
  run_direct_leg "$DIRECT_URL" ""
else
  deploy_cloud_run_grain 1 0 "$SEEDED_ACCESS_CODE"
  if gcloud run services add-iam-policy-binding "$SERVICE" \
    --project="$PROJECT" \
    --region="$REGION" \
    --member="allUsers" \
    --role="roles/run.invoker" \
    --quiet >/dev/null 2>&1; then
    run_direct_leg "$DIRECT_URL" ""
    gcloud run services remove-iam-policy-binding "$SERVICE" \
      --project="$PROJECT" \
      --region="$REGION" \
      --member="allUsers" \
      --role="roles/run.invoker" \
      --quiet >/dev/null 2>&1 || true
  else
    RUNNER_INVOKER_TOKEN="$(gcloud auth print-identity-token --audiences="$DIRECT_URL" 2>/dev/null || gcloud auth print-identity-token)"
    run_direct_leg "$DIRECT_URL" "$RUNNER_INVOKER_TOKEN"
  fi
fi

c6_notes+=("loopback break-glass covered by the simulated suite (task 2510)")

# Record Cases 5, 6, 7, 8 in numerical order 5..8!
if [[ "$c5_ok" -eq 1 ]]; then
  record_case 5 "forged-iap-assertion-rejected" "Forged X-Goog-IAP-JWT-Assertion stripped by IAP and rejected by grain" "PASS" "$(IFS='; '; echo "${c5_notes[*]}")"
else
  record_case 5 "forged-iap-assertion-rejected" "Forged X-Goog-IAP-JWT-Assertion stripped by IAP and rejected by grain" "FAIL" "$(IFS='; '; echo "${c5_notes[*]}")"
fi

if [[ "$c6_ok" -eq 1 ]]; then
  record_case 6 "require-iap-refuses-access-code" "Redeploy with -ui-require-iap refuses access code over the network" "PASS" "$(IFS='; '; echo "${c6_notes[*]}")"
else
  record_case 6 "require-iap-refuses-access-code" "Redeploy with -ui-require-iap refuses access code over the network" "FAIL" "$(IFS='; '; echo "${c6_notes[*]}")"
fi

if [[ "$c7_ok" -eq 1 ]]; then
  record_case 7 "csrf-cross-origin-refused" "Cross-origin state-changing requests refused with 403" "PASS" "$(IFS='; '; echo "${c7_notes[*]}")"
else
  record_case 7 "csrf-cross-origin-refused" "Cross-origin state-changing requests refused with 403" "FAIL" "$(IFS='; '; echo "${c7_notes[*]}")"
fi

if [[ "$c8_ok" -eq 1 ]]; then
  record_case 8 "attribution-sa-a" "Task filed by SA-A recorded as service SA-A, not operator" "PASS" "$(IFS='; '; echo "${c8_notes[*]}")"
else
  record_case 8 "attribution-sa-a" "Task filed by SA-A recorded as service SA-A, not operator" "FAIL" "$(IFS='; '; echo "${c8_notes[*]}")"
fi

# Run teardown BEFORE writing final report so teardown status and exact runtime
# are recorded in report.json and report.md!
teardown

END_EPOCH="$(date +%s)"
END_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
DURATION_SEC=$((END_EPOCH - START_EPOCH))
INSTANCE_SEC=0
if [[ "$DEPLOY_START_EPOCH" -gt 0 ]]; then
  if [[ "$DEPLOY_END_EPOCH" -eq 0 ]]; then
    DEPLOY_END_EPOCH="$END_EPOCH"
  fi
  INSTANCE_SEC=$((DEPLOY_END_EPOCH - DEPLOY_START_EPOCH))
fi

REPORT_JSON="${OUT_DIR}/report.json"
REPORT_MD="${OUT_DIR}/report.md"

python3 - "$CASES_JSONL" "$REPORT_JSON" "$REPORT_MD" \
  "$START_UTC" "$END_UTC" "$DURATION_SEC" "$INSTANCE_SEC" \
  "$PROJECT" "$PROJECT_NUMBER" "$REGION" "$SERVICE" "$IMAGE" \
  "$SERVICE_URL" "$IAP_AUDIENCE" "$SA_A_EMAIL" "$SA_B_EMAIL" \
  "$TEARDOWN_STATUS" "$LOCAL_SIM" <<'PY'
import json
import sys

(
    cases_file,
    report_json_path,
    report_md_path,
    start_utc,
    end_utc,
    duration_sec_s,
    instance_sec_s,
    project,
    project_number,
    region,
    service,
    image,
    service_url,
    iap_audience,
    sa_a_email,
    sa_b_email,
    teardown_status,
    local_sim_s,
) = sys.argv[1:19]

duration_sec = int(duration_sec_s)
instance_sec = int(instance_sec_s)
local_sim = local_sim_s == "1"

with open(cases_file, "r", encoding="utf-8") as fh:
    cases = [json.loads(line) for line in fh if line.strip()]
cases.sort(key=lambda c: c["case"])

passed = sum(1 for c in cases if c["status"] == "PASS")
failed = len(cases) - passed
overall = "PASS" if failed == 0 and len(cases) == 8 else "FAIL"

# Cloud Run Tier-1 us-central1 list rates (no-cpu-throttling / min-instances=1):
# $0.00002400 / vCPU-second + $0.00000250 / GiB-second; IAP & IAM bindings are $0.
est_usd = 0.0 if local_sim else round(instance_sec * (0.00002400 + 0.00000250), 6)

report = {
    "suite": "cloud-run-direct-iap-e2e",
    "mode": "local-sim" if local_sim else "cloud-run-iap",
    "overall": overall,
    "passed": passed,
    "failed": failed,
    "startedAt": start_utc,
    "completedAt": end_utc,
    "durationSeconds": duration_sec,
    "target": {
        "project": project,
        "projectNumber": project_number,
        "region": region,
        "service": service,
        "image": image,
        "serviceURL": service_url,
        "callerJWTAAudience": f"{service_url}/*",
        "iapAssertionAudience": iap_audience,
        "saA": sa_a_email,
        "saB": sa_b_email,
    },
    "cost": {
        "cloudRunInstanceSeconds": instance_sec,
        "vcpuSeconds": 0 if local_sim else instance_sec,
        "gibSeconds": 0 if local_sim else instance_sec,
        "estimatedUSD": est_usd,
        "note": (
            "local simulation ($0.00)"
            if local_sim
            else f"1 vCPU + 1 GiB Cloud Run min-instance for ~{instance_sec}s in {region} (~${est_usd:.4f} USD list rate, <$0.01)"
        ),
    },
    "teardown": teardown_status,
    "cases": cases,
}

with open(report_json_path, "w", encoding="utf-8") as fh:
    json.dump(report, fh, indent=2)
    fh.write("\n")

lines = [
    "# Cloud Run Direct IAP End-to-End Qualification Report",
    "",
    f"- **Result:** **{overall}** ({passed}/{len(cases)} passed)",
    f"- **Mode:** `{report['mode']}`",
    f"- **Image:** `{image}`",
    f"- **Project / Region / Service:** `{project}` (`{project_number}`) / `{region}` / `{service}`",
    f"- **Service URL:** `{service_url}`",
    f"- **IAP Assertion Audience (`-ui-iap-audience`):** `{iap_audience}`",
    f"- **Programmatic Caller JWT Audience:** `{service_url}/*` (RS256 service-account JWT in `Authorization: Bearer`)",
    f"- **Identities:** `SA-A` = `{sa_a_email}` (allowed), `SA-B` = `{sa_b_email}` (not on allowlist)",
    f"- **Window:** `{start_utc}` -> `{end_utc}` (`{duration_sec}s`)",
    f"- **Estimated Cost:** `{report['cost']['note']}`",
    f"- **Teardown:** `{teardown_status}`",
    "",
    "## Cases",
    "",
    "| # | ID | Case | Result | Evidence |",
    "| :-: | --- | --- | :-: | --- |",
]
for c in cases:
    ev = c["evidence"].replace("|", "\\|")
    lines.append(f"| {c['case']} | `{c['id']}` | {c['title']} | **{c['status']}** | {ev} |")
lines.append("")

with open(report_md_path, "w", encoding="utf-8") as fh:
    fh.write("\n".join(lines))
PY
REPORT_WRITTEN=1
rm -f "$CASES_JSONL"

log "Wrote ${REPORT_JSON} and ${REPORT_MD}"

if [[ "$(jq -r '.overall' "$REPORT_JSON")" != "PASS" ]]; then
  log "Qualification FAILED — see ${REPORT_MD}"
  exit 1
fi

log "All 8 IAP qualification cases PASSED in ${DURATION_SEC}s."
exit 0
