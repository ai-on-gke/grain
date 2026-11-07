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

# Live end-to-end walkthrough of docs/test-plans/2026-11-04.md §3
# ("Access and identity", items 1-7 + responsive light/dark screenshots)
# against a real `grain daemon` behind `tests/iap/iapsim.go` and its Cloud
# Identity group resolver stand-in, plus `grain demo` layout checks.
#
#   docs/test-runs/2026-11-04-access-identity/walkthrough.sh [WORK_DIR]
set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
WORK=${1:-/tmp/access-identity-walkthrough}
chmod -R u+w "$WORK" 2>/dev/null || true
rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/repos" "$WORK/home" "$SCRIPT_DIR/screenshots"

export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
GRAIN=${GRAIN:-/home/debian/bin/grain}
if [[ ! -x $GRAIN ]]; then
	(cd "$ROOT" && go build -o "$WORK/grain" ./cmd/grain) || exit 2
	GRAIN=$WORK/grain
fi

AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:18280"
DAEMON="http://127.0.0.1:14310"
DOWN_DAEMON="http://127.0.0.1:14311"
SANDBOX_SHIM_PORT=19728
ECHO_PORT=19729
IAP_HDR="X-Goog-IAP-JWT-Assertion"

RESULTS="$WORK/results.md"
FAILS=0
printf '| ID | Check | Expected | Got | Result |\n|---|---|---|---|---|\n' >"$RESULTS"

check() {
	local id=$1 desc=$2 expected=$3 got=$4
	local res="PASS"
	if ! [[ $got =~ $expected ]]; then
		res="FAIL"
		FAILS=$((FAILS + 1))
	fi
	local clean_got=${got//|/\\|}
	clean_got=${clean_got//$'\n'/ }
	# shellcheck disable=SC2016
	printf '| %s | %s | `%s` | `%s` | %s |\n' "$id" "$desc" "$expected" "${clean_got:0:180}" "$res" | tee -a "$RESULTS"
}

PIDS=()
# shellcheck disable=SC2317
cleanup() {
	for p in "${PIDS[@]}"; do
		kill "$p" 2>/dev/null || true
	done
}
trap cleanup EXIT

# Preserve Go caches before switching HOME to a clean directory with no ambient credentials.
GOPATH_CACHE=$(go env GOPATH)
GOCACHE_DIR=$(go env GOCACHE)
export GOPATH="$GOPATH_CACHE"
export GOCACHE="$GOCACHE_DIR"
export HOME="$WORK/home"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT
unset GRAIN_IAP_AUDIENCE GRAIN_IAP_ISSUER GRAIN_IAP_KEYS_URL GRAIN_REQUIRE_IAP GRAIN_ALLOWED_PRINCIPALS GRAIN_CLOUD_IDENTITY_ENDPOINT
unset GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
export GRAIN_SANDBOX_CLI=0
export GCE_METADATA_HOST="127.0.0.1:9"

# 0. Build iapsim, initialize local bare git repo acme/app.git, and generate ADC JSON for Cloud Identity stub.
(cd "$ROOT" && go build -o "$WORK/iapsim" tests/iap/iapsim.go) || exit 2

git init --bare "$WORK/repos/app.git" >/dev/null 2>&1
mkdir -p "$WORK/seed-repo"
(
	cd "$WORK/seed-repo" || exit 1
	git init -b main >/dev/null 2>&1
	git config user.name "Test Seed"
	git config user.email "seed@example.com"
	echo "# acme/app" >README.md
	git add README.md
	git commit -m "initial commit" >/dev/null 2>&1
	git push "$WORK/repos/app.git" main >/dev/null 2>&1
)

"$WORK/iapsim" \
	-issuer 127.0.0.1:18280 \
	-proxy 127.0.0.1:18490 \
	-upstream "$DAEMON" \
	-aud "$AUD" \
	-iss "$ISS" \
	-group "ops@example.com=carol@example.com" \
	>"$WORK/iapsim.log" 2>&1 &
SIM_PID=$!
PIDS+=("$SIM_PID")
for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && break; sleep 0.1; done

openssl genrsa 2048 2>/dev/null >"$WORK/sa.key"
jq -n --rawfile k "$WORK/sa.key" --arg t "$ISSUER/token" \
	'{type:"service_account",project_id:"my-proj",private_key_id:"1",private_key:$k,client_email:"grain-daemon@my-proj.iam.gserviceaccount.com",client_id:"1",token_uri:$t}' \
	>"$WORK/adc.json"

# Start main real `grain daemon` with:
# - simulated IAP verifier (-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url)
# - Cloud Identity group resolver stub (-ui-cloud-identity-endpoint + GOOGLE_APPLICATION_CREDENTIALS)
# - deprecated -ui-allowed-principals carrying user:, serviceAccount:, and domain: entries (for Item 2 one-time migration)
GOOGLE_APPLICATION_CREDENTIALS="$WORK/adc.json" "$GRAIN" daemon \
	-data-dir "$WORK/data" \
	-host-sandboxes -sandbox-dir "$WORK/sandboxes" \
	-ui-addr 127.0.0.1:14310 \
	-as "owner@example.com" \
	-ci=false \
	-repo "acme/app" \
	-default-target-repo "acme/app" \
	-gcp-project "my-proj" \
	-ui-iap-audience "$AUD" \
	-ui-iap-issuer "$ISS" \
	-ui-iap-keys-url "$ISSUER/jwks" \
	-ui-cloud-identity-endpoint "$ISSUER" \
	-ui-allowed-principals "user:seed@example.com,serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com,domain:example.com" \
	>"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
PIDS+=("$DAEMON_PID")

for _ in $(seq 120); do
	code=$(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true)
	[[ $code != "000" ]] && break
	sleep 0.1
done

ACCESS_CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
sign_jwt() {
	local q="email=$1&aud=$AUD&iss=$ISS"
	shift
	for a in "$@"; do q="$q&$a"; done
	curl -sf "$ISSUER/sign?$q"
}

ALICE_JWT=$(sign_jwt "alice@example.com")
BOB_JWT=$(sign_jwt "bob@example.com")
CAROL_JWT=$(sign_jwt "carol@example.com")
DAVE_JWT=$(sign_jwt "dave@example.com")
FRANK_JWT=$(sign_jwt "frank@example.com")
AUDITOR_JWT=$(sign_jwt "auditor@example.com")
SA_BOT_JWT=$(sign_jwt "release-bot@my-proj.iam.gserviceaccount.com")
POOL_SA_JWT=$(sign_jwt "grain-sb-agent-0@my-proj.iam.gserviceaccount.com")

iap_req() { # jwt method path [body]
	local jwt=$1 method=$2 path=$3 body=${4:-}
	if [[ -n $body ]]; then
		curl -s -o "$WORK/body.json" -w '%{http_code}' -X "$method" \
			-H "$IAP_HDR: $jwt" -H "Content-Type: application/json" \
			-d "$body" "$DAEMON$path"
	else
		curl -s -o "$WORK/body.json" -w '%{http_code}' -X "$method" \
			-H "$IAP_HDR: $jwt" "$DAEMON$path"
	fi
}

code_req() { # method path [body] [extra_hdr...]
	local method=$1 path=$2 body=${3:-}
	shift $(( $# >= 3 ? 3 : $# ))
	local hdrs=(-H "Authorization: Bearer $ACCESS_CODE")
	for h in "$@"; do hdrs+=(-H "$h"); done
	if [[ -n $body ]]; then
		curl -s -o "$WORK/body.json" -w '%{http_code}' -X "$method" \
			"${hdrs[@]}" -H "Content-Type: application/json" \
			-d "$body" "$DAEMON$path"
	else
		curl -s -o "$WORK/body.json" -w '%{http_code}' -X "$method" \
			"${hdrs[@]}" "$DAEMON$path"
	fi
}

# ==============================================================================
# ITEM 1: Live allowlist edits (CLI, API, UI) + Cloud Identity group resolver stub
# ==============================================================================
st=$(iap_req "$ALICE_JWT" GET "/api/whoami")
check "1.1" "alice@example.com initially refused (403) before allowlist add" '^403$' "$st"
check "1.2" "initial refusal message points to Settings -> Access" 'Settings -> Access' "$(jq -r '.error' "$WORK/body.json")"

cli_out=$("$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access allow --reason "admit alice as primary operator" "user:alice@example.com" 2>&1)
check "1.3" "grain access allow user:alice@example.com succeeds" 'allowed user:alice@example.com' "$cli_out"

st=$(iap_req "$ALICE_JWT" GET "/api/whoami")
check "1.4" "alice@example.com admitted on very next request with no daemon restart" '^200$' "$st"
check "1.5" "alice whoami is verified IAP human" '"label":"alice@example.com".*"verified":true.*"allowed":true' "$(tr -d '\n' <"$WORK/body.json")"

# Deny alice via CLI -> refused immediately on next request, pointing to Settings -> Access; then re-allow.
"$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access deny "user:alice@example.com" >/dev/null 2>&1
st=$(iap_req "$ALICE_JWT" GET "/api/whoami")
check "1.6" "grain access deny revokes alice@example.com immediately without restart" '^403$' "$st"
check "1.7" "post-deny 403 body points to Settings -> Access" 'Settings -> Access' "$(jq -r '.error' "$WORK/body.json")"
"$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access allow --reason "restore alice operator access" "user:alice@example.com" >/dev/null

# Live API allowlist add, role PATCH, and DELETE by verified IAP operator alice@example.com
st=$(iap_req "$BOB_JWT" GET "/api/whoami")
check "1.8" "bob@example.com refused before API allowlist add" '^403$' "$st"
st=$(iap_req "$ALICE_JWT" POST "/api/access/principals" '{"entry":"user:bob@example.com","role":"grain/viewer","reason":"bob viewer via API"}')
check "1.9" "API POST /api/access/principals adds bob@example.com live" '^201$' "$st"
st=$(iap_req "$BOB_JWT" GET "/api/whoami")
check "1.10" "bob@example.com admitted immediately as grain/viewer" '^200$' "$st"
st=$(iap_req "$BOB_JWT" POST "/api/tasks" '{"title":"viewer write check","description":"viewer lacks tasks:write","noRepo":true}')
check "1.11" "bob at grain/viewer is refused tasks:write (403)" '^403$' "$st"

st=$(iap_req "$ALICE_JWT" PATCH "/api/access/principals/user%3Abob%40example.com" '{"role":"grain/contributor","reason":"promote bob"}')
check "1.12" "API PATCH /api/access/principals/{entry} updates bob role live" '^200$' "$st"
st=$(iap_req "$BOB_JWT" POST "/api/tasks" '{"title":"contributor write check","description":"promoted to contributor","noRepo":true}')
check "1.13" "bob promoted to grain/contributor can file tasks immediately without restart (201)" '^201$' "$st"

st=$(iap_req "$ALICE_JWT" DELETE "/api/access/principals/user%3Abob%40example.com")
check "1.14" "API DELETE /api/access/principals/{entry} removes bob live" '^200$' "$st"
st=$(iap_req "$BOB_JWT" GET "/api/whoami")
check "1.15" "bob refused (403) immediately after API DELETE" '^403$' "$st"

# Group membership admission via Cloud Identity resolver stub (-ui-cloud-identity-endpoint)
st=$(iap_req "$CAROL_JWT" GET "/api/whoami")
check "1.16" "carol@example.com refused before group:ops@example.com is added" '^403$' "$st"
"$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access allow --role "grain/contributor" --reason "ops on-call group" "group:ops@example.com" >/dev/null
st=$(iap_req "$CAROL_JWT" GET "/api/whoami")
check "1.17" "carol@example.com admitted via group:ops@example.com through Cloud Identity stub" '^200$' "$st"
st=$(iap_req "$CAROL_JWT" POST "/api/tasks" '{"title":"group contributor task","description":"admitted via ops@example.com","noRepo":true}')
check "1.18" "carol receives group role grain/contributor and can file a task (201)" '^201$' "$st"
st=$(iap_req "$DAVE_JWT" GET "/api/whoami")
check "1.19" "non-member dave@example.com still refused (403) by Cloud Identity stub" '^403$' "$st"
adc_tok=$(curl -sf -X POST "$ISSUER/token" | jq -r '.access_token')
check "1.20" "Cloud Identity stub group lookup returns groups/ops@example.com" '"name": *"groups/ops@example.com"' "$(curl -sf -H "Authorization: Bearer $adc_tok" "$ISSUER/v1/groups:lookup?groupKey.id=ops@example.com")"

# ==============================================================================
# ITEM 2: Service accounts (reason required, kind=service attribution, gcp-key pool SA planted in SQLite refused, -ui-allowed-principals drops SA)
# ==============================================================================
sa_cli_err=$("$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access allow --role "grain/contributor" "serviceAccount:release-bot@my-proj.iam.gserviceaccount.com" 2>&1)
sa_cli_rc=$?
check "2.1" "CLI refuses serviceAccount: entry without --reason" '^[1-9]' "$sa_cli_rc"
check "2.2" "CLI refusal explains reason is required for serviceAccount:" 'reason' "$sa_cli_err"

st=$(iap_req "$ALICE_JWT" POST "/api/access/principals" '{"entry":"serviceAccount:release-bot@my-proj.iam.gserviceaccount.com","role":"grain/contributor"}')
check "2.3" "API refuses serviceAccount: entry without reason (400)" '^400$' "$st"
check "2.4" "API refusal explains reason is required" 'reason' "$(cat "$WORK/body.json")"

st=$(iap_req "$ALICE_JWT" POST "/api/access/principals" '{"entry":"serviceAccount:release-bot@my-proj.iam.gserviceaccount.com","role":"grain/contributor","reason":"release pipeline files tasks through IAP"}')
check "2.5" "API admits serviceAccount:release-bot with reason" '^201$' "$st"

st=$(iap_req "$SA_BOT_JWT" GET "/api/whoami")
check "2.6" "admitted service account passes sign-in (200)" '^200$' "$st"
check "2.7" "whoami reports kind=service, source=iap, verified=true" '"kind":"service".*"source":"iap".*"verified":true' "$(tr -d '\n' <"$WORK/body.json")"

st=$(iap_req "$SA_BOT_JWT" POST "/api/tasks" '{"title":"Automated release check","description":"Filed by IAP service account","repo":"acme/app"}')
check "2.8" "service account files task under grain/contributor" '^201$' "$st"
SA_TASK_ID=$(jq -r '.id' "$WORK/body.json")
check "2.9" "task filed by SA carries authorKind=service and SA email label" '"author":"release-bot@my-proj.iam.gserviceaccount.com".*"authorKind":"service".*"authorLabel":"release-bot@my-proj.iam.gserviceaccount.com"' "$(tr -d '\n' <"$WORK/body.json")"

DB_FILE="$WORK/data/store/grain.db"
sa_db_row=$(sqlite3 "$DB_FILE" "SELECT origin_actor_kind || '|' || origin_actor_id || '|' || origin_actor_source FROM task WHERE id = '$SA_TASK_ID';")
check "2.10" "SQLite stores origin_actor_kind=service, source=iap for SA task" '^service\|release-bot@my-proj\.iam\.gserviceaccount\.com\|iap$' "$sa_db_row"

# Register a gcp-key pool (grain-sb-agent-{0,1}@my-proj.iam.gserviceaccount.com), then verify API and sign-in both refuse it!
code_req POST "/api/gcp-keys" '{"name":"sandbox-pool","projectId":"my-proj","serviceAccountEmail":"grain-sb-agent@my-proj.iam.gserviceaccount.com","serviceAccountCount":2}' >/dev/null

# API refuses adding a gcp-key pool SA directly
st=$(code_req POST "/api/access/principals" '{"entry":"serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com","role":"grain/contributor","reason":"try pool SA"}')
check "2.11" "API refuses adding gcp-key pool SA to allowlist" '^400$' "$st"

# Plant gcp-key pool SA directly into SQLite `allowed_principal` and verify sign-in STILL refuses it!
sqlite3 "$DB_FILE" "INSERT INTO allowed_principal (entry, added_by, added_at, reason, role) VALUES ('serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com', 'sqlite-plant', datetime('now'), 'planted directly in SQLite', 'grain/contributor');"
planted_count=$(sqlite3 "$DB_FILE" "SELECT COUNT(*) FROM allowed_principal WHERE entry = 'serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com';")
check "2.12" "gcp-key pool SA row is present in SQLite allowed_principal" '^1$' "$planted_count"

st=$(iap_req "$POOL_SA_JWT" GET "/api/whoami")
check "2.13" "gcp-key pool SA planted in SQLite is STILL refused (403) at sign-in" '^403$' "$st"
check "2.14" "pool SA refusal explains task keys are never admitted" 'never admitted|gcp-key|task' "$(cat "$WORK/body.json")"
sqlite3 "$DB_FILE" "DELETE FROM allowed_principal WHERE entry = 'serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com';"

# Check one-time -ui-allowed-principals import dropped serviceAccount: & domain: and warned in log + API
st=$(code_req GET "/api/access/principals")
check "2.15" "-ui-allowed-principals imported user:seed@example.com once and surfaced dropped entries" '"droppedFlagEntries":\["serviceAccount:dropped-ci@my-proj\.iam\.gserviceaccount\.com","domain:example\.com"\]' "$(tr -d '\n' <"$WORK/body.json")"
check "2.16" "daemon log warns about dropped serviceAccount: flag entry" 'WARNING: dropped "serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com" from -ui-allowed-principals during migration' "$(cat "$WORK/daemon.log")"

# ==============================================================================
# ITEM 3: api:access:write held by an agent (person-only, refused on sandbox-cli, needs reason, AU-6.4 allowlist refusal, and reason in config_audit & Settings -> Audit)
# ==============================================================================
# Enable externalAgentApi for Items 4 & 5, and declare custom scope set acme/audit-config-only (settings:read only) for Item 5.
st_ext=$(code_req PUT "/api/settings" '{"externalAgentApi":true}')
st=$(code_req PUT "/api/api-scope-sets" '{"set":{"name":"acme/audit-config-only","description":"Read-only config audit role","scopes":["settings:read"]}}' "X-Grain-Acting-Reason: declare audit-only scope set for walkthrough")
check "3.0" "enabled externalAgentApi and declared acme/audit-config-only scope set" '^200\|200$' "$st_ext|$st"

# 3a. Task without api:access:write tries to propose a child task holding api:access:write -> refused (person-only grant).
st=$(iap_req "$ALICE_JWT" POST "/api/tasks" '{"title":"Unprivileged parent task","description":"no access:write","noRepo":true,"capabilities":["api:tasks:write"]}')
PLAIN_TASK=$(jq -r '.id' "$WORK/body.json")
st=$(code_req POST "/api/tasks" '{"title":"Escalation attempt","description":"wants access:write","noRepo":true,"capabilities":["api:access:write"]}' "X-Grain-Acting-Task: $PLAIN_TASK")
check "3.1" "agent without api:access:write cannot grant person-only api:access:write (403)" '^403$' "$st"
check "3.2" "escalation refusal names api:access:write" 'access:write' "$(cat "$WORK/body.json")"

# 3b. Person creates task holding api:access:write + api:settings:write + api:tasks:read.
st=$(code_req POST "/api/tasks" '{"title":"Privileged config agent","description":"holds person-granted access:write and settings:write","noRepo":true,"capabilities":["api:access:write","api:settings:write","api:tasks:read"]}')
check "3.3" "person can grant api:access:write and api:settings:write on task creation" '^201$' "$st"
PRIV_TASK=$(jq -r '.id' "$WORK/body.json")

# Refused on sandbox-cli channel!
st=$(code_req POST "/api/access/principals" '{"entry":"user:mallory@example.com","role":"grain/viewer","reason":"from sandbox"}' \
	"X-Grain-Acting-Task: $PRIV_TASK" "X-Grain-Channel: sandbox-cli" "X-Grain-Acting-Reason: from sandbox")
check "3.4" "api:access:write is refused (403) on sandbox-cli channel" '^403$' "$st"
check "3.5" "sandbox-cli refusal names sandbox CLI restriction" 'sandbox' "$(cat "$WORK/body.json")"

# Refused on MCP/API channel when X-Grain-Acting-Reason is missing!
st=$(code_req POST "/api/access/principals" '{"entry":"user:mallory@example.com","role":"grain/viewer"}' \
	"X-Grain-Acting-Task: $PRIV_TASK")
check "3.6" "api:access:write without X-Grain-Acting-Reason is refused (403)" '^403$' "$st"
check "3.7" "missing-reason refusal asks for reason" 'reason' "$(cat "$WORK/body.json")"

# Even WITH person-granted api:access:write AND X-Grain-Acting-Reason, AU-6.4 refuses agent edits to allowlist!
st=$(code_req POST "/api/access/principals" '{"entry":"user:mallory@example.com","role":"grain/viewer","reason":"with reason"}' \
	"X-Grain-Acting-Task: $PRIV_TASK" "X-Grain-Acting-Reason: agent allowlist attempt")
check "3.8" "AU-6.4 refuses agent write to /api/access/principals even with reason (403)" '^403$' "$st"
check "3.9" "AU-6.4 refusal states only a person changes the allowed users and groups list" 'only a person changes the allowed users and groups list' "$(cat "$WORK/body.json")"

# Agent dangerous write that agents MAY perform (PUT /api/settings under api:settings:write) requires X-Grain-Acting-Reason and records it in config_audit!
st=$(code_req PUT "/api/settings" '{"pollInterval":"25s"}' "X-Grain-Acting-Task: $PRIV_TASK")
check "3.10" "agent PUT /api/settings without X-Grain-Acting-Reason refused (403)" '^403$' "$st"
st=$(code_req PUT "/api/settings" '{"pollInterval":"25s"}' \
	"X-Grain-Acting-Task: $PRIV_TASK" "X-Grain-Acting-Reason: enable telemetry check from task run")
check "3.11" "agent PUT /api/settings with X-Grain-Acting-Reason succeeds (200)" '^200$' "$st"

st=$(code_req GET "/api/deployment/config-audit")
check "3.12" "config_audit API records both allowlist reason and agent X-Grain-Acting-Reason" \
	'enable telemetry check from task run.*release pipeline files tasks through IAP|release pipeline files tasks through IAP.*enable telemetry check from task run' \
	"$(tr -d '\n' <"$WORK/body.json")"
cp "$WORK/body.json" "$WORK/config-audit.json"

sql_reasons=$(sqlite3 "$DB_FILE" "SELECT actor_id || '|' || actor_source || '|' || summary FROM config_audit ORDER BY id;")
check "3.13" "SQLite config_audit stores actor_id, actor_source, and summary with reason for allowlist and agent writes" \
	'alice@example\.com\|iap\|.*release pipeline files tasks through IAP' "$sql_reasons"

# ==============================================================================
# ITEM 4: Labels everywhere (one task touched by verified person, access code, external agent, and grain itself)
# ==============================================================================
# Mint an owned external agent token ("triage-bot" for alice@example.com)
st=$(iap_req "$ALICE_JWT" POST "/api/external-agents/tokens" '{"name":"triage-bot","scopes":["tasks:read","tasks:write","dispatch:read"]}')
check "4.1" "alice mints owned external agent token triage-bot" '^201$' "$st"
TRIAGE_TOKEN=$(jq -r '.token' "$WORK/body.json")

# 1) Verified person (alice@example.com via IAP) files the task unapproved, then approves and comments.
st=$(iap_req "$ALICE_JWT" POST "/api/tasks" '{"title":"Cross-surface attribution walkthrough","description":"One task touched by IAP person, access code, external agent, and grain.","noRepo":true,"approved":false}')
check "4.2" "alice files task via IAP" '^201$' "$st"
LABEL_TASK=$(jq -r '.id' "$WORK/body.json")
iap_req "$ALICE_JWT" POST "/api/tasks/$LABEL_TASK/approve" '{}' >/dev/null
iap_req "$ALICE_JWT" POST "/api/tasks/$LABEL_TASK/comments" '{"body":"Verified person comment from IAP."}' >/dev/null

# 2) Access code (operator (access code)) comments and pauses the task; alice resumes; access code pauses again
#    -> grain itself (PrincipalAutomation, label "grain") writes the pause/resume notes AND task stays paused for pause banner!
code_req POST "/api/tasks/$LABEL_TASK/comments" '{"body":"Access code comment from loopback."}' >/dev/null
code_req POST "/api/tasks/$LABEL_TASK/pause" '{}' >/dev/null
iap_req "$ALICE_JWT" POST "/api/tasks/$LABEL_TASK/resume" '{}' >/dev/null
code_req POST "/api/tasks/$LABEL_TASK/pause" '{}' >/dev/null

# 3) Owned external agent (external agent triage-bot for alice@example.com) comments with X-Grain-Client provenance.
st=$(curl -s -o "$WORK/body.json" -w '%{http_code}' -X POST \
	-H "Authorization: Bearer $TRIAGE_TOKEN" \
	-H "X-Grain-Client: triage-runner/1.2" \
	-H "Content-Type: application/json" \
	-d '{"body":"External agent comment on behalf of alice."}' \
	"$DAEMON/api/tasks/$LABEL_TASK/comments")
check "4.3" "owned external agent posts comment on task" '^20[014]$' "$st"

# Also create an interactive chat task touched by alice, access code, and external agent so TaskChat.jsx is checked too!
st=$(iap_req "$ALICE_JWT" POST "/api/tasks" '{"title":"Interactive chat attribution check","description":"Initial chat prompt from alice.","noRepo":true,"interactive":true}')
CHAT_TASK=$(jq -r '.id' "$WORK/body.json")
code_req POST "/api/tasks/$CHAT_TASK/comments" '{"body":"Follow-up in chat from access code."}' >/dev/null
curl -s -o /dev/null -X POST \
	-H "Authorization: Bearer $TRIAGE_TOKEN" \
	-H "Content-Type: application/json" \
	-d '{"body":"Follow-up in chat from external agent."}' \
	"$DAEMON/api/tasks/$CHAT_TASK/comments"

# Start a tiny Node proxy shim so literal `grain task read <id>` (sandboxcli) talks to $DAEMON/api!
node -e '
const http = require("node:http");
const target = new URL(process.argv[1]);
const token = process.argv[2];
http.createServer((req, res) => {
  const opts = {
    hostname: target.hostname,
    port: target.port,
    path: "/api" + req.url,
    method: req.method,
    headers: { ...req.headers, authorization: "Bearer " + token, host: target.host },
  };
  const pr = http.request(opts, (up) => {
    res.writeHead(up.statusCode, up.headers);
    up.pipe(res);
  });
  req.pipe(pr);
}).listen(Number(process.argv[3]), "127.0.0.1");
' "$DAEMON" "$ACCESS_CODE" "$SANDBOX_SHIM_PORT" &
PIDS+=("$!")
sleep 0.3

# Surface A: Workstation CLI `grain get`
"$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" get "$LABEL_TASK" >"$WORK/grain-get.txt"
check "4.4" "grain get shows Filed by, pause banner, transitions, and all 4 actor labels" \
	'filed by: *alice@example\.com' "$(cat "$WORK/grain-get.txt")"
check "4.5" "grain get shows operator (access code), external agent triage-bot for alice@example.com, and grain" \
	'operator \(access code\).*external agent triage-bot for alice@example\.com' "$(tr '\n' ' ' <"$WORK/grain-get.txt")"
raw_get=$(grep -Ec '\{human, *operator\}|by operator[^ (]' "$WORK/grain-get.txt" || true)
check "4.6" "grain get contains zero raw {human, operator} or unlabelled operator" '^0$' "$raw_get"

# Surface B: Sandbox CLI `grain task read` (task://<id>/record.md)
GRAIN_SANDBOX_CLI=1 GRAIN_SANDBOX_PROXY="http://127.0.0.1:$SANDBOX_SHIM_PORT" GRAIN_SANDBOX_TOKEN="shim" \
	"$GRAIN" task read "$LABEL_TASK" >"$WORK/grain-task-read.txt"
check "4.7" "sandbox CLI grain task read shows Filed by alice@example.com and all 4 labels" \
	'Filed by: alice@example\.com.*operator \(access code\).*grain \(automation\).*external agent triage-bot for alice@example\.com' \
	"$(tr '\n' ' ' <"$WORK/grain-task-read.txt")"
raw_read=$(grep -Ec '\{human, *operator\}|\] operator \(human\):' "$WORK/grain-task-read.txt" || true)
check "4.8" "grain task read contains zero raw {human, operator}" '^0$' "$raw_read"

# Surface C: Controller MCP `POST /api/mcp` -> `grain_get_task` (summary, conversation, full)
for view in summary conversation full; do
	curl -s -X POST "$DAEMON/api/mcp" \
		-H "Authorization: Bearer $TRIAGE_TOKEN" \
		-H "Content-Type: application/json" \
		-H "Accept: application/json" \
		-d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"grain_get_task\",\"arguments\":{\"id\":\"$LABEL_TASK\",\"view\":\"$view\"}}}" \
		>"$WORK/mcp-$view.json"
done
mcp_summary_txt=$(jq -r '.result.content[0].text' "$WORK/mcp-summary.json")
mcp_conv_txt=$(jq -r '.result.content[0].text' "$WORK/mcp-conversation.json")
mcp_full_txt=$(jq -r '.result.content[0].text' "$WORK/mcp-full.json")

check "4.9" "MCP grain_get_task (summary) shows identical labels in last events" \
	'grain \(automation\): Paused by operator \(access code\).*external agent triage-bot for alice@example\.com' \
	"$(echo "$mcp_summary_txt" | tr '\n' ' ')"
check "4.10" "MCP grain_get_task (conversation) shows all 4 labels and provenance" \
	'alice@example\.com.*operator \(access code\).*grain.*external agent triage-bot for alice@example\.com.*triage-runner/1\.2' \
	"$(echo "$mcp_conv_txt" | tr '\n' ' ')"
check "4.11" "MCP grain_get_task (full) carries authorLabel, byLabel, and paused.byLabel" \
	'"authorLabel":"alice@example\.com".*"byLabel":"operator \(access code\)"' \
	"$(echo "$mcp_full_txt" | tr -d '\n')"

# ==============================================================================
# ITEM 5: Settings -> Audit with partial scopes (settings:read only)
# ==============================================================================
"$GRAIN" -server "$DAEMON" -token "$ACCESS_CODE" access allow --role "acme/audit-config-only" --reason "auditor partial scope check" "user:auditor@example.com" >/dev/null
st=$(code_req POST "/api/external-agents/tokens" '{"name":"audit-reader","scopes":["settings:read"]}')
AUDIT_ONLY_TOKEN=$(jq -r '.token' "$WORK/body.json")

st=$(iap_req "$AUDITOR_JWT" GET "/api/deployment/config-audit")
check "5.1" "settings:read caller (IAP custom role) reads /api/deployment/config-audit (200)" '^200$' "$st"
st=$(iap_req "$AUDITOR_JWT" GET "/api/deployment/audit-logs")
check "5.2" "settings:read caller gets 403 on /api/deployment/audit-logs (needs secrets:read)" '^403$' "$st"
st=$(iap_req "$AUDITOR_JWT" GET "/api/deployment/secret-audit")
check "5.3" "settings:read caller gets 403 on /api/deployment/secret-audit (needs secrets:read)" '^403$' "$st"

# ==============================================================================
# ITEM 7: CLI credential edges (GRAIN_USER -> -as, bad GRAIN_TOKEN -> exit 3 naming source)
# ==============================================================================
node -e '
const http = require("node:http");
http.createServer((req, res) => {
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(JSON.stringify([{ id: "1", title: "user=" + (req.headers["x-forwarded-user"] || ""), state: "queued", priority: 5 }]));
}).listen(Number(process.argv[1]), "127.0.0.1");
' "$ECHO_PORT" &
PIDS+=("$!")
sleep 0.2

grain_user_out=$(GRAIN_USER="dana@example.com" "$GRAIN" -server "http://127.0.0.1:$ECHO_PORT" list 2>&1)
check "7.1" "GRAIN_USER=dana@example.com sets -as default (X-Forwarded-User: dana@example.com)" 'user=dana@example\.com' "$grain_user_out"

grain_as_override=$(GRAIN_USER="dana@example.com" "$GRAIN" -server "http://127.0.0.1:$ECHO_PORT" -as "explicit@example.com" list 2>&1)
check "7.2" "explicit -as flag overrides GRAIN_USER" 'user=explicit@example\.com' "$grain_as_override"

grain_post_verb=$(GRAIN_USER="dana@example.com" "$GRAIN" list -server "http://127.0.0.1:$ECHO_PORT" -as "post-verb@example.com" 2>&1)
check "7.2b" "post-verb -as flag also overrides GRAIN_USER" 'user=post-verb@example\.com' "$grain_post_verb"

bad_env_out=$(GRAIN_TOKEN="bad-secret-value" "$GRAIN" -server "$DAEMON" list 2>&1)
bad_env_rc=$?
bad_auth_out=$(GRAIN_AUTH_TOKEN="bad-auth-value" "$GRAIN" -server "$DAEMON" list 2>&1)
bad_auth_rc=$?
bad_flag_out=$("$GRAIN" -server "$DAEMON" -token "bad-flag-value" list 2>&1)
bad_flag_rc=$?

check "7.3" "bad GRAIN_TOKEN exits with code 3" '^3$' "$bad_env_rc"
# shellcheck disable=SC2016
check "7.4" "bad GRAIN_TOKEN error names \$GRAIN_TOKEN as its source" '\$GRAIN_TOKEN' "$bad_env_out"
# shellcheck disable=SC2016
check "7.5" "bad GRAIN_AUTH_TOKEN exits with code 3 and names \$GRAIN_AUTH_TOKEN" '^3 \|.*\$GRAIN_AUTH_TOKEN' "$bad_auth_rc | $bad_auth_out"
check "7.6" "bad -token flag exits with code 3 and names -token" '^3 \|.*-token' "$bad_flag_rc | $bad_flag_out"

# ==============================================================================
# PLAYWRIGHT BROWSER WALKTHROUGH & SCREENSHOT MATRIX (1440 & 390, light & dark)
# ==============================================================================
jq -n \
	--arg outDir "$SCRIPT_DIR" \
	--arg daemonURL "$DAEMON" \
	--arg accessCode "$ACCESS_CODE" \
	--arg aliceJWT "$ALICE_JWT" \
	--arg frankJWT "$FRANK_JWT" \
	--arg auditorJWT "$AUDITOR_JWT" \
	--arg auditOnlyToken "$AUDIT_ONLY_TOKEN" \
	--arg labelTaskID "$LABEL_TASK" \
	--arg chatTaskID "$CHAT_TASK" \
	'{outDir:$outDir, daemonURL:$daemonURL, verifierDownURL:"", accessCode:$accessCode, aliceJWT:$aliceJWT, frankJWT:$frankJWT, auditorJWT:$auditorJWT, auditOnlyToken:$auditOnlyToken, labelTaskID:$labelTaskID, chatTaskID:$chatTaskID}' \
	>"$WORK/browser-env.json"

node "$SCRIPT_DIR/browser-walkthrough.mjs" "$WORK/browser-env.json" main
browser_rc=$?
check "UI.MAIN" "Playwright main browser checks and 1440/390 light/dark screenshot suite" '^0$' "$browser_rc"

# Second daemon restart check for Item 2: -ui-allowed-principals is ignored once DB allowlist is non-empty!
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
GOOGLE_APPLICATION_CREDENTIALS="$WORK/adc.json" "$GRAIN" daemon \
	-data-dir "$WORK/data" \
	-host-sandboxes -sandbox-dir "$WORK/sandboxes" \
	-ui-addr 127.0.0.1:14310 \
	-as "owner@example.com" \
	-ci=false \
	-repo "acme/app" \
	-default-target-repo "acme/app" \
	-gcp-project "my-proj" \
	-ui-iap-audience "$AUD" \
	-ui-iap-issuer "$ISS" \
	-ui-iap-keys-url "$ISSUER/jwks" \
	-ui-cloud-identity-endpoint "$ISSUER" \
	-ui-allowed-principals "user:second-boot-ignored@example.com" \
	>"$WORK/daemon-restart.log" 2>&1 &
DAEMON_PID=$!
PIDS+=("$DAEMON_PID")
for _ in $(seq 100); do
	code=$(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true)
	[[ $code != "000" ]] && break
	sleep 0.1
done
ACCESS_CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
SECOND_JWT=$(sign_jwt "second-boot-ignored@example.com")
st=$(iap_req "$SECOND_JWT" GET "/api/whoami")
check "2.17" "on restart with non-empty DB allowlist, -ui-allowed-principals is ignored" '^403$' "$st"
st=$(iap_req "$ALICE_JWT" GET "/api/whoami")
check "2.18" "DB allowlist entries persist across daemon restart" '^200$' "$st"

# Stop main daemon before starting DOWN_DAEMON so sandbox proxy ports (9728+) are free.
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true

# ==============================================================================
# ITEM 6: Verifier down -> 503 + Retry-After: 5 on API calls and usable HTML page in browser
# ==============================================================================
mkdir -p "$WORK/down-data" "$WORK/down-sandboxes"
"$GRAIN" daemon \
	-data-dir "$WORK/down-data" \
	-host-sandboxes -sandbox-dir "$WORK/down-sandboxes" \
	-ui-addr 127.0.0.1:14311 \
	-ci=false \
	-ui-iap-audience "$AUD" \
	-ui-iap-issuer "$ISS" \
	-ui-iap-keys-url "http://127.0.0.1:18299/jwks" \
	>"$WORK/down-daemon.log" 2>&1 &
DOWN_PID=$!
PIDS+=("$DOWN_PID")
for _ in $(seq 100); do
	code=$(curl -s -o /dev/null -w '%{http_code}' "$DOWN_DAEMON/" || true)
	[[ $code != "000" ]] && break
	sleep 0.1
done
DOWN_CODE=$(tr -d '[:space:]' <"$WORK/down-data/local-api-token")

curl -s -D "$WORK/down-api-hdr.txt" -o "$WORK/down-api-body.txt" \
	-H "$IAP_HDR: $ALICE_JWT" "$DOWN_DAEMON/api/tasks"
check "6.1" "API returns 503 Service Unavailable when verifier JWKS endpoint is down" '^HTTP/1\.1 503 ' "$(head -n 1 "$WORK/down-api-hdr.txt")"
check "6.2" "503 API response carries Retry-After: 5 header" 'Retry-After: 5' "$(tr -d '\r' <"$WORK/down-api-hdr.txt")"
check "6.3" "503 API response is sanitized upstream_unavailable JSON with correlation ref" '"code":"upstream_unavailable".*"ref":"[0-9a-f]+"' "$(cat "$WORK/down-api-body.txt")"
down_ref=$(jq -r '.ref' "$WORK/down-api-body.txt")
check "6.3b" "daemon log records correlation ref and underlying JWKS fetch error (callers receive 503)" "ref=$down_ref.*fetching IAP public keys|could not verify X-Goog-IAP-JWT-Assertion yet.*callers receive 503" "$(tr '\n' ' ' <"$WORK/down-daemon.log")"

st_with_code=$(curl -s -o /dev/null -w '%{http_code}' -H "$IAP_HDR: $ALICE_JWT" -H "Authorization: Bearer $DOWN_CODE" "$DOWN_DAEMON/api/tasks")
check "6.4" "access code does not bypass 503 when verifier is down" '^503$' "$st_with_code"

jq --arg u "$DOWN_DAEMON" '.verifierDownURL = $u' "$WORK/browser-env.json" >"$WORK/browser-down-env.json"
node "$SCRIPT_DIR/browser-walkthrough.mjs" "$WORK/browser-down-env.json" verifier-down
down_browser_rc=$?
check "UI.6" "Playwright verifier-down 503 browser check and screenshot" '^0$' "$down_browser_rc"

# Build contact-sheet.png across all 16 core shots (4 surfaces x 2 widths x 2 themes) + partial audit + verifier down!
montage \
	-label '%f' \
	-font DejaVu-Sans -pointsize 12 \
	-background '#1e1e24' -fill '#f0f0f4' \
	-geometry '340x240+10+10>' \
	-tile 4x \
	"$SCRIPT_DIR/screenshots/access-1440-light.png" \
	"$SCRIPT_DIR/screenshots/access-1440-dark.png" \
	"$SCRIPT_DIR/screenshots/access-390-light.png" \
	"$SCRIPT_DIR/screenshots/access-390-dark.png" \
	"$SCRIPT_DIR/screenshots/audit-1440-light.png" \
	"$SCRIPT_DIR/screenshots/audit-1440-dark.png" \
	"$SCRIPT_DIR/screenshots/audit-390-light.png" \
	"$SCRIPT_DIR/screenshots/audit-390-dark.png" \
	"$SCRIPT_DIR/screenshots/task-1440-light.png" \
	"$SCRIPT_DIR/screenshots/task-1440-dark.png" \
	"$SCRIPT_DIR/screenshots/task-390-light.png" \
	"$SCRIPT_DIR/screenshots/task-390-dark.png" \
	"$SCRIPT_DIR/screenshots/sidebar-1440-light.png" \
	"$SCRIPT_DIR/screenshots/sidebar-1440-dark.png" \
	"$SCRIPT_DIR/screenshots/sidebar-390-light.png" \
	"$SCRIPT_DIR/screenshots/sidebar-390-dark.png" \
	"$SCRIPT_DIR/screenshots/audit-partial-1440-light.png" \
	"$SCRIPT_DIR/screenshots/verifier-down-1440-light.png" \
	"$SCRIPT_DIR/contact-sheet.png"

cp "$RESULTS" "$SCRIPT_DIR/results.md"
cp "$WORK/grain-get.txt" "$SCRIPT_DIR/grain-get.txt"
cp "$WORK/grain-task-read.txt" "$SCRIPT_DIR/grain-task-read.txt"
cp "$WORK/mcp-summary.json" "$SCRIPT_DIR/mcp-summary.json"
cp "$WORK/mcp-conversation.json" "$SCRIPT_DIR/mcp-conversation.json"
cp "$WORK/config-audit.json" "$SCRIPT_DIR/config-audit.json"

echo "Walkthrough complete: FAILS=$FAILS"
exit $((FAILS > 0))
