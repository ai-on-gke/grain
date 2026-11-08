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

# Item 2 of docs/test-plans/2026-11-08-afternoon.md §1: a real grain daemon
# behind the IAP simulator (tests/iap/iapsim.go) with allowed principals at
# the reader (grain/viewer), writer (grain/contributor) and admin
# (grain/operator) roles, plus two custom roles that split the scopes the
# Deployment credentials card needs: capadmin (capabilities:admin, no
# settings:write or secrets:write) and secretsonly (settings:write and
# secrets:write, no capabilities:admin). It curls every route the card
# reads and writes as each identity and leaves the stack running for the
# browser pass (walkthrough.mjs ROLE_BASE=http://127.0.0.1:18490).
#
#   docs/test-runs/2026-11-08-deployment-credentials-card/roles.sh [WORK_DIR]
#
# GRAIN may name a prebuilt grain binary.

set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
WORK=${1:-/tmp/credentials-card-roles}
rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/home"

GRAIN=${GRAIN:-}
if [[ -z $GRAIN ]]; then
	(cd "$ROOT" && go build -o "$WORK/grain" ./cmd/grain) || exit 2
	GRAIN=$WORK/grain
fi
(cd "$ROOT" && go build -o "$WORK/iapsim" tests/iap/iapsim.go) || exit 2

AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:18280"
PROXY="http://127.0.0.1:18490"
DAEMON="http://127.0.0.1:14320"

export HOME="$WORK/home"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT
export GRAIN_SANDBOX_CLI=0 GCE_METADATA_HOST="127.0.0.1:9"

setsid "$WORK/iapsim" -issuer 127.0.0.1:18280 -proxy 127.0.0.1:18490 -upstream "$DAEMON" \
	-aud "$AUD" -iss "$ISS" >"$WORK/iapsim.log" 2>&1 </dev/null &
echo $! >"$WORK/iapsim.pid"
for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && break; sleep 0.1; done

openssl genrsa 2048 2>/dev/null >"$WORK/sa.key"
jq -n --rawfile k "$WORK/sa.key" --arg t "$ISSUER/token" \
	'{type:"service_account",project_id:"my-proj",private_key_id:"1",private_key:$k,client_email:"grain-daemon@my-proj.iam.gserviceaccount.com",client_id:"1",token_uri:$t}' \
	>"$WORK/adc.json"

GOOGLE_APPLICATION_CREDENTIALS="$WORK/adc.json" setsid "$GRAIN" daemon \
	-data-dir "$WORK/data" -sandbox-port-base=0 \
	-host-sandboxes -allow-unisolated-host-sandboxes -sandbox-dir "$WORK/sandboxes" \
	-ui-addr 127.0.0.1:14320 -as "owner@example.com" -ci=false \
	-repo "acme/app" -default-target-repo "acme/app" \
	-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
	-ui-cloud-identity-endpoint "$ISSUER" \
	>"$WORK/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK/daemon.pid"
for _ in $(seq 600); do
	[[ -s $WORK/data/local-api-token ]] && [[ $(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true) != "000" ]] && break
	sleep 0.1
done
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
op() { curl -s -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" "$@"; }

# Two custom roles that split the card's three scopes apart.
op -X PUT "$DAEMON/api/api-scope-sets" -d '{"set":{"name":"capadmin","description":"capabilities:admin only","scopes":["capabilities:admin","settings:read","secrets:read","capabilities:read"]}}' >/dev/null
op -X PUT "$DAEMON/api/api-scope-sets" -d '{"set":{"name":"secretsonly","description":"settings and secrets writes, no capabilities:admin","scopes":["settings:write","settings:read","secrets:write","secrets:read","capabilities:read"]}}' >/dev/null

gacc() { "$GRAIN" -server "$DAEMON" -token "$CODE" access "$@" 2>&1; }
gacc allow --role grain/viewer --reason "walkthrough reader" "user:reader@example.com"
gacc allow --role grain/contributor --reason "walkthrough writer" "user:writer@example.com"
gacc allow --role grain/operator --reason "walkthrough admin" "user:admin@example.com"
gacc allow --role capadmin --reason "walkthrough capadmin" "user:capadmin@example.com"
gacc allow --role secretsonly --reason "walkthrough secretsonly" "user:secretsonly@example.com"

# Seed the secrets item 3 looks for, as the operator.
for s in github-app/app-id github-app/private-key registry-push/token cosign-key/key cosign-password/value unclaimed-thing/token; do
	op -X PUT "$DAEMON/api/secrets/$s" -d '{"value":"s3cr3t-'"${s//\//-}"'-VALUE"}' >/dev/null
done

RESULTS="$WORK/routes.md"
printf '| Identity | Route | Status |\n|---|---|---|\n' >"$RESULTS"
as() { # email method path [body]
	local args=(-s -o "$WORK/body.json" -w '%{http_code}' -X "$2" -H "X-Iapsim-Email: $1")
	[[ -n ${4:-} ]] && args+=(-H "Content-Type: application/json" -d "$4")
	curl "${args[@]}" "$PROXY$3"
}
for who in reader writer admin capadmin secretsonly; do
	email="$who@example.com"
	row() { printf '| %s | %s | %s |\n' "$who" "$1" "$2" | tee -a "$RESULTS"; }
	row "GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write" \
		"$(as "$email" GET /api/config >/dev/null; jq -r '[.access.full, (.access.scopes|index("capabilities:admin")!=null), (.access.scopes|index("settings:write")!=null), (.access.scopes|index("secrets:write")!=null)]|map(tostring)|join("/")' "$WORK/body.json")"
	row "GET /api/settings" "$(as "$email" GET /api/settings)"
	row "GET /api/secrets" "$(as "$email" GET /api/secrets)"
	if grep -q 'VALUE' "$WORK/body.json"; then row "GET /api/secrets leaks a value" "LEAK"; fi
	row "PUT /api/settings {githubHost}" "$(as "$email" PUT /api/settings '{"githubHost":"github.com"}')"
	row "PUT /api/secrets/$who-probe/token" "$(as "$email" PUT "/api/secrets/$who-probe/token" '{"value":"x"}')"
	row "DELETE /api/secrets/unclaimed-thing/nope" "$(as "$email" DELETE /api/secrets/unclaimed-thing/nope)"
	row "DELETE /api/secrets/$who-probe" "$(as "$email" DELETE "/api/secrets/$who-probe")"
done
echo "results: $RESULTS"
echo "stack left running: daemon pid $(cat "$WORK/daemon.pid"), iapsim pid $(cat "$WORK/iapsim.pid"); proxy $PROXY"
