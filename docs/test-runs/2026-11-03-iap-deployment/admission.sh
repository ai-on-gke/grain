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

# §4 items 2 and 3: the default allowlist live (gcp-key pool refused, owner
# admitted) and forged-after-expired logging, against a real `grain demo`
# behind tests/iap/iapsim.go. Needs GRAIN (a built binary), go, curl, jq and
# free ports 4300/18080/18443.
#
#   docs/test-runs/2026-11-03-iap-deployment/admission.sh [OUT]
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
OUT=${1:-/tmp/iap-admission}
mkdir -p "$OUT"
GRAIN=${GRAIN:-$HOME/bin/grain}
AUD=/projects/123/global/backendServices/456
ISS=https://iap.test.local
ISSUER=http://127.0.0.1:18080
DEMO=http://127.0.0.1:4300
A=X-Goog-IAP-JWT-Assertion
FAILS=0

(cd "$ROOT" && go build -o "$OUT/iapsim" tests/iap/iapsim.go) || exit 2
"$OUT/iapsim" -issuer 127.0.0.1:18080 -proxy 127.0.0.1:18443 -upstream "$DEMO" -aud "$AUD" -iss "$ISS" \
	>"$OUT/iapsim.log" 2>&1 &
SIM=$!
mkdir -p "$OUT/tmp"
# No -ui-allowed-principals and no $GRAIN_ALLOWED_PRINCIPALS: the default
# allowlist. The demo's stored config carries a gcp-key pool
# (grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com).
env -u GRAIN_ALLOWED_PRINCIPALS TMPDIR="$OUT/tmp" "$GRAIN" demo -open=false -addr 127.0.0.1:4300 \
	-as owner@example.com -access-code-file "$OUT/access-code" \
	-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
	>"$OUT/demo.log" 2>&1 &
DEMO_PID=$!
trap 'kill $DEMO_PID $SIM 2>/dev/null' EXIT
for _ in $(seq 150); do curl -s -o /dev/null "$DEMO/healthz" && break; sleep 0.2; done

tok() { local q="email=$1&aud=$AUD&iss=$ISS"; shift; for a in "$@"; do q="$q&$a"; done; curl -s "$ISSUER/sign?$q"; }
get() { curl -s -o "$OUT/body" -w '%{http_code}' -H "$A: $1" "$DEMO/api/auth/user"; }
check() { # what expected got
	if [[ $3 =~ $2 ]]; then echo "PASS  $1: $3"; else echo "FAIL  $1: want /$2/, got $3"; FAILS=$((FAILS + 1)); fi
}
logged() { grep -c -- "$1" "$OUT/demo.log"; }

# --- item 2: the default allowlist ---
check "owner admitted" '^200$' "$(get "$(tok owner@example.com)")"
check "owner is iap-verified" '"authMethod":"iap"' "$(cat "$OUT/body")"
POOL=grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com
check "gcp-key pool SA refused" '^403$' "$(get "$(tok $POOL)")"
check "refusal names the pool SA" "$POOL" "$(cat "$OUT/body")"
check "pool SA refusal logged" '^1$' "$(logged "refused verified principal \"$POOL\"")"
check "pool SA with mixed case refused" '^403$' "$(get "$(tok Grain-SB-Agent-0@Grain-Sandbox-Dev.iam.gserviceaccount.com)")"
check "daemon SA (provisioned) admitted" '^200$' "$(get "$(tok grain-daemon@grain-sandbox-dev.iam.gserviceaccount.com)")"
check "stranger refused" '^403$' "$(get "$(tok stranger@example.com)")"
check "stranger refusal logged" '^1$' "$(logged 'refused verified principal "stranger@example.com"')"
check "owner never logged as refused" '^0$' "$(logged 'refused verified principal "owner@example.com"')"

# --- item 3: forged after expired, same audience ---
before_exp=$(grep -c 'expired' "$OUT/demo.log")
before_sig=$(grep -ci 'signature\|forged\|verification' "$OUT/demo.log")
check "expired assertion 401" '^401$' "$(get "$(tok owner@example.com exp=-10m)")"
check "forged assertion (unpublished key k2) 401" '^401$' "$(get "$(tok owner@example.com kid=k1 key=k2)")"
sleep 0.5
check "expired logged" "^[$((before_exp + 1))-9]" "$(grep -c 'expired' "$OUT/demo.log")"
check "forged logged after the expired one" "^[$((before_sig + 1))-9]" "$(grep -ci 'signature\|forged\|verification' "$OUT/demo.log")"
check "a second forged one is still 401" '^401$' "$(get "$(tok owner@example.com kid=k1 key=k2)")"
check "forged logged once, not per request" '^1$' "$(logged 'crypto/ecdsa: verification error')"
check "own-audience refusals do not say to set the audience" '^0$' "$(grep 'refused an X-Goog' "$OUT/demo.log" | grep -c 'set -ui-iap-audience')"

grep -i 'assertion\|principal\|refus' "$OUT/demo.log" >"$OUT/admission-log-lines.txt"
echo "FAILS=$FAILS"
exit $((FAILS > 0))
