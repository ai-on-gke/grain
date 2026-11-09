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

# Live walkthrough of docs/test-plans/2026-11-07.md §3 items 1-3 (RBAC role
# resolution, limited-role filing/edit holds, per-tool MCP checks and the
# gma_ owner downgrade) against a real `grain daemon` behind
# tests/iap/iapsim.go (IAP + Cloud Identity stand-in) and regsim.go
# (grain-registry assertion stand-in for delegate: callers). A second daemon
# whose Cloud Identity endpoint is unreachable proves group lookups fail
# closed. Item 4 (browser) is browser-walkthrough.mjs, started at the end
# against the same daemon.
#
#   docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh [WORK_DIR]
set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
WORK=${1:-/tmp/rbac-roles-walkthrough}
chmod -R u+w "$WORK" 2>/dev/null || true
rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/data2" "$WORK/sandboxes" "$WORK/home"

export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
GRAIN=${GRAIN:-}
if [[ -z $GRAIN ]]; then
	(cd "$ROOT" && go build -o "$WORK/grain" ./cmd/grain) || exit 2
	GRAIN=$WORK/grain
fi
(cd "$ROOT" && go build -o "$WORK/iapsim" tests/iap/iapsim.go) || exit 2
(cd "$ROOT" && go build -o "$WORK/regsim" "$SCRIPT_DIR/regsim.go") || exit 2

AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:18280"
PROXY="http://127.0.0.1:18490"
REG="http://127.0.0.1:18380"
REG_ISS="https://registry.test.local"
REG_AUD="https://grain.test.local"
DAEMON="http://127.0.0.1:14320"
DOWN_DAEMON="http://127.0.0.1:14321"
IAP_HDR="X-Goog-IAP-JWT-Assertion"
REG_HDR="X-Grain-Registry-Assertion"

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
	printf '| %s | %s | `%s` | `%s` | %s |\n' "$id" "$desc" "$expected" "${clean_got:0:160}" "$res" | tee -a "$RESULTS"
}

PIDS=()
# shellcheck disable=SC2317
cleanup() {
	for p in "${PIDS[@]}"; do
		kill "$p" 2>/dev/null || true
	done
}
trap cleanup EXIT

GOPATH_CACHE=$(go env GOPATH)
GOCACHE_DIR=$(go env GOCACHE)
export GOPATH="$GOPATH_CACHE" GOCACHE="$GOCACHE_DIR"
export HOME="$WORK/home"
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT
unset GRAIN_IAP_AUDIENCE GRAIN_IAP_ISSUER GRAIN_IAP_KEYS_URL GRAIN_REQUIRE_IAP GRAIN_ALLOWED_PRINCIPALS GRAIN_CLOUD_IDENTITY_ENDPOINT
unset GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
export GRAIN_SANDBOX_CLI=0
export GCE_METADATA_HOST="127.0.0.1:9"

# Cloud Identity stand-in: devs@ holds alice (also user: viewer), dana, erin.
"$WORK/iapsim" -issuer 127.0.0.1:18280 -proxy 127.0.0.1:18490 -upstream "$DAEMON" \
	-aud "$AUD" -iss "$ISS" \
	-group "devs@example.com=alice@example.com,dana@example.com,erin@example.com" \
	>"$WORK/iapsim.log" 2>&1 &
PIDS+=($!)
"$WORK/regsim" -addr 127.0.0.1:18380 -iss "$REG_ISS" -aud "$REG_AUD" >"$WORK/regsim.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && curl -sf "$REG/jwks" >/dev/null && break; sleep 0.1; done

openssl genrsa 2048 2>/dev/null >"$WORK/sa.key"
jq -n --rawfile k "$WORK/sa.key" --arg t "$ISSUER/token" \
	'{type:"service_account",project_id:"my-proj",private_key_id:"1",private_key:$k,client_email:"grain-daemon@my-proj.iam.gserviceaccount.com",client_id:"1",token_uri:$t}' \
	>"$WORK/adc.json"

start_daemon() { # data_dir addr cloud_identity_endpoint log
	GOOGLE_APPLICATION_CREDENTIALS="$WORK/adc.json" "$GRAIN" daemon \
		-data-dir "$1" -sandbox-port-base=0 \
		-host-sandboxes -allow-unisolated-host-sandboxes -sandbox-dir "$WORK/sandboxes" \
		-ui-addr "$2" -as "owner@example.com" -ci=false \
		-repo "acme/app" -default-target-repo "acme/app" \
		-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
		-ui-cloud-identity-endpoint "$3" \
		-ui-registry-issuer "$REG_ISS" -ui-registry-audience "$REG_AUD" -ui-registry-keys-url "$REG/jwks" \
		>"$4" 2>&1 &
	PIDS+=($!)
	for _ in $(seq 300); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' "http://$2/" || true) != "000" ]] && break
		sleep 0.1
	done
}
start_daemon "$WORK/data" 127.0.0.1:14320 "$ISSUER" "$WORK/daemon.log"
# Port 9 on loopback is closed: every group lookup this daemon makes errors.
start_daemon "$WORK/data2" 127.0.0.1:14321 "http://127.0.0.1:9" "$WORK/daemon-down.log"

CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
CODE2=$(tr -d '[:space:]' <"$WORK/data2/local-api-token")
sign_jwt() { curl -sf "$ISSUER/sign?email=$1&aud=$AUD&iss=$ISS"; }
reg_jwt() { # person [act] [access]
	curl -sf "$REG/sign?email=$1&act=${2:-}&access=${3:-write}&app=release-console"
}

BODY="$WORK/body.json"
req() { # base hdr method path [body]
	local base=$1 hdr=$2 method=$3 path=$4 body=${5:-}
	local args=(-s -o "$BODY" -w '%{http_code}' -X "$method" -H "$hdr")
	[[ -n $body ]] && args+=(-H "Content-Type: application/json" -d "$body")
	curl "${args[@]}" "$base$path"
}
as() { # email method path [body] -- fresh IAP assertion each call
	req "$DAEMON" "$IAP_HDR: $(sign_jwt "$1")" "${@:2}"
}
as_down() { req "$DOWN_DAEMON" "$IAP_HDR: $(sign_jwt "$1")" "${@:2}"; }
as_reg() { # person act access method path [body]
	req "$DAEMON" "$REG_HDR: $(reg_jwt "$1" "$2" "$3")" "${@:4}"
}
code() { req "$DAEMON" "Authorization: Bearer $CODE" "$@"; }
code_down() { req "$DOWN_DAEMON" "Authorization: Bearer $CODE2" "$@"; }
body() { tr -d '\n' <"$BODY"; }
gacc() { "$GRAIN" -server "$DAEMON" -token "$CODE" access "$@" 2>&1; }
# Workstation CLI through the IAP proxy as a person (iapsim's ID token stand-in).
gcli() { # email args...
	local who=$1
	shift
	GRAIN_ID_TOKEN="iapsim-idtoken.$who" "$GRAIN" -server "$PROXY" "$@" 2>&1
}
count_tasks() { code GET "/api/tasks?status=all&limit=500" >/dev/null; jq 'if type=="array" then length else (.tasks|length) end' "$BODY"; }
mcp() { # base hdr path method params
	curl -s -X POST -H "$2" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
		-d "$(jq -cn --arg m "$4" --argjson p "${5:-{\}}" '{jsonrpc:"2.0",id:1,method:$m,params:$p}')" "$1$3"
}

# ==============================================================================
# ITEM 1: multi-principal resolution, serviceAccount reason, delegate label,
# fail-closed group lookup, whoami, access-code elevation and logout.
# ==============================================================================
out=$(gacc allow --role grain/viewer --reason "alice reads" "user:alice@example.com")
check "1.1" "grain access allow user:alice as grain/viewer" 'allowed user:alice@example.com' "$out"
out=$(gacc allow --role grain/contributor --reason "devs file tasks" "group:devs@example.com")
check "1.2" "grain access allow group:devs as grain/contributor" 'allowed group:devs@example.com' "$out"
out=$(gacc allow --role grain/viewer --reason "vic reads" "user:vic@example.com")
check "1.3" "grain access allow user:vic (viewer only, no group)" 'allowed user:vic@example.com' "$out"
out=$(gacc allow --role grain/developer --reason "no such role" "user:x@example.com")
rc=$?
check "1.4" "unknown role grain/developer is refused by the CLI (plan names a role grain does not ship)" "^[^0].*|.*(unknown|no scope set|not a).*" "$rc $out"
out=$(gacc list)
check "1.5" "grain access list shows both alice and devs rows with roles" 'user:alice@example.com.*grain/viewer' "$(echo "$out" | tr '\n' ' ')"
echo "$out" >"$WORK/access-list-1.txt"

st=$(as alice@example.com GET /api/auth/whoami)
check "1.6" "alice whoami readable (200)" '^200$' "$st"
cp "$BODY" "$WORK/whoami-alice.json"
as alice@example.com GET /api/auth/user >/dev/null
cp "$BODY" "$WORK/auth-user-alice.json"
check "1.7" "alice's /api/auth/user access lists both matched roles" 'grain/contributor.*grain/viewer|grain/viewer.*grain/contributor' "$(body)"
st=$(as alice@example.com POST /api/tasks '{"title":"alice via group","description":"union of viewer and contributor","noRepo":true}')
check "1.8" "alice (user viewer + group contributor) can file tasks: roles union" '^201$' "$st"
st=$(as vic@example.com POST /api/tasks '{"title":"vic write","description":"viewer only","noRepo":true}')
check "1.9" "vic (viewer only) refused tasks:write" '^403$' "$st"
st=$(as vic@example.com GET /api/auth/whoami)
check "1.10" "vic (viewer) can still read whoami" '^200$' "$st"
st=$(as nobody@example.com GET /api/auth/whoami)
cp "$BODY" "$WORK/whoami-nobody.json"
check "1.11" "unlisted nobody@ can read whoami, which reports authenticated:false" '^200 .*"authenticated":false' "$st $(body)"
st=$(as nobody@example.com GET /api/tasks)
check "1.11b" "unlisted nobody@ refused every scoped route, naming Settings -> Access" '^403 .*Settings -(>|\\u003e) Access' "$st $(body)"

out=$(gacc allow --role grain/viewer "serviceAccount:ci-bot@my-proj.iam.gserviceaccount.com")
rc=$?
check "1.12" "serviceAccount: without --reason refused" "^[1-9].*reason" "$rc $out"
out=$(gacc allow --role grain/viewer --reason "ci reads status" "serviceAccount:ci-bot@my-proj.iam.gserviceaccount.com")
check "1.13" "serviceAccount: with --reason admitted" 'allowed serviceAccount:ci-bot' "$out"
st=$(as ci-bot@my-proj.iam.gserviceaccount.com GET /api/auth/whoami)
check "1.14" "service account session admitted, viewer role" '^200$' "$st"
st=$(as ci-bot@my-proj.iam.gserviceaccount.com POST /api/tasks '{"title":"sa write","description":"x","noRepo":true}')
check "1.15" "viewer service account refused tasks:write" '^403$' "$st"
st=$(as ci-bot@my-proj.iam.gserviceaccount.com GET /api/auth/whoami)

out=$(gacc allow --label release-console "delegate:console@my-proj.iam.gserviceaccount.com")
rc=$?
check "1.16" "delegate: without --reason refused" "^[1-9].*reason" "$rc $out"
out=$(gacc allow --label "Bad Label" --reason "console" "delegate:console@my-proj.iam.gserviceaccount.com")
rc=$?
check "1.17" "delegate: with a malformed --label refused" "^[1-9]" "$rc $out"
out=$(gacc allow --label release-console --reason "release console acts for people" "delegate:console@my-proj.iam.gserviceaccount.com")
check "1.18" "delegate: with --label and --reason admitted" 'allowed delegate:console' "$out"
code GET /api/access/principals >/dev/null
lu_before=$(jq -r '[.. | objects | select((.entry? // "") | startswith("delegate:"))][0] | "\(.role) \(.label) \(.lastUsedAt // .last_used_at // "null")"' "$BODY")
check "1.19" "delegate row defaults to grain/delegated, keeps label, never used" '^grain/delegated release-console null$' "$lu_before"

st=$(as_reg dana@example.com console@my-proj.iam.gserviceaccount.com write GET /api/auth/whoami)
check "1.20" "dana via delegate:console admitted" '^200$' "$st"
cp "$BODY" "$WORK/whoami-delegated.json"
check "1.21" "delegated whoami attributes the person via the delegate label" 'dana@example.com.*release-console|release-console.*dana@example.com' "$(body)"
code GET /api/access/principals >/dev/null
lu_after=$(jq -r '[.. | objects | select((.entry? // "") | startswith("delegate:"))][0] | (.lastUsedAt // .last_used_at // "null")' "$BODY")
check "1.22" "delegate last_used_at stamped after use" '^[0-9]{4}-' "$lu_after"
st=$(as_reg dana@example.com console@my-proj.iam.gserviceaccount.com write POST /api/tasks '{"title":"delegated filing","description":"dana via console","noRepo":true}')
check "1.23" "dana (contributor) via delegate (delegated) can file a task" '^201$' "$st"
DEL_TASK=$(jq -r '.id // .task.id' "$BODY")
code GET "/api/tasks/$DEL_TASK" >/dev/null
check "1.24" "delegated task attributed to dana (via release-console)" 'dana@example.com' "$(body)"
st=$(as_reg vic@example.com console@my-proj.iam.gserviceaccount.com write POST /api/tasks '{"title":"vic via delegate","description":"x","noRepo":true}')
check "1.25" "viewer vic via a write delegate still refused: intersection, not union" '^403$' "$st"
st=$(as_reg dana@example.com console@my-proj.iam.gserviceaccount.com read POST /api/tasks '{"title":"read-only delegation","description":"x","noRepo":true}')
check "1.26" "act.access=read delegation refuses writes" '^403$' "$st"
st=$(as_reg dana@example.com stranger@my-proj.iam.gserviceaccount.com write GET /api/tasks)
check "1.27" "unlisted delegating app refused" '^(401|403)$' "$st"
st=$(as_reg dana@example.com console@my-proj.iam.gserviceaccount.com write POST /api/access/principals '{"entry":"user:zed@example.com","role":"grain/viewer","reason":"x"}')
check "1.28" "delegated caller cannot edit Settings -> Access" '^403$' "$st"

# Fail-closed group lookups on the daemon whose Cloud Identity is unreachable.
code_down POST /api/access/principals '{"entry":"user:alice@example.com","role":"grain/viewer","reason":"alice reads"}' >/dev/null
code_down POST /api/access/principals '{"entry":"group:devs@example.com","role":"grain/contributor","reason":"devs"}' >/dev/null
st=$(as_down alice@example.com GET /api/auth/user)
check "1.29" "group lookup down: alice still admitted on her direct user: row" '^200$' "$st"
cp "$BODY" "$WORK/auth-user-alice-groupdown.json"
check "1.30" "group lookup down: alice's roles are only grain/viewer" '"grain/viewer"' "$(body)"
check "1.30b" "group lookup down: grain/contributor not granted" '^0$' "$(grep -c 'grain/contributor' "$BODY")"
st=$(as_down alice@example.com POST /api/tasks '{"title":"escalate while lookup down","description":"x","noRepo":true}')
check "1.31" "group lookup down: alice refused tasks:write (fails closed to user: role)" '^403$' "$st"
n_down0=$(code_down GET "/api/tasks?status=all&limit=500" >/dev/null; jq 'length' "$BODY")
st=$(as_down erin@example.com GET /api/tasks)
check "1.32" "group lookup down: group-only erin refused" '^(401|403)$' "$st"
check "1.33" "refusal message is the generic lookup-unavailable text" 'lookup unavailable' "$(body)"
st=$(as_down erin@example.com POST /api/tasks '{"title":"erin while lookup down","description":"x","noRepo":true}')
code_down GET "/api/tasks?status=all&limit=500" >/dev/null
check "1.34" "no task filed on the lookup-down daemon (erin $st)" "^$n_down0$" "$(jq 'length' "$BODY")"

# Access-code elevation and logout (browser cookie session over the IAP proxy).
JAR="$WORK/jar.txt"
rm -f "$JAR"
curl -s -c "$JAR" -b "$JAR" -o /dev/null "$PROXY/_iapsim/login?email=vic@example.com&next=/"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d '{"title":"vic before code","description":"x","noRepo":true}' "$PROXY/api/tasks")
check "1.35" "vic's browser session refused before the access code" '^403$' "$st"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d "{\"code\":\"$CODE\"}" "$PROXY/api/auth/access-code")
check "1.36" "vic presents the deployment access code" '^(200|204)$' "$st"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d '{"title":"vic with code","description":"access code lifts the role","noRepo":true}' "$PROXY/api/tasks")
cp "$BODY" "$WORK/vic-with-code.json"
# FINDING F1: behind IAP the verified person wins (verifierAccessCodeWins is
# false for IAP), so the cookie POST /api/auth/access-code just set is ignored.
check "1.37" "access-code cookie does NOT lift an IAP-verified viewer (finding F1)" '^403$' "$st"
st=$(code POST /api/tasks '{"title":"bare access code","description":"no IAP assertion","noRepo":true}')
check "1.37b" "the bare access code (no assertion, unlimited admin) files a task" '^201$' "$st"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' "$PROXY/api/auth/logout")
check "1.38" "logout answers" '^(200|204|302|303)$' "$st"
curl -s -c "$JAR" -b "$JAR" -o /dev/null "$PROXY/_iapsim/login?email=vic@example.com&next=/"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d '{"title":"vic after logout","description":"x","noRepo":true}' "$PROXY/api/tasks")
check "1.39" "after logout vic is back to viewer (403)" '^403$' "$st"
st=$(curl -s -c "$JAR" -b "$JAR" -o "$BODY" -w '%{http_code}' "$PROXY/api/auth/whoami")
check "1.40" "after logout whoami still readable (200)" '^200$' "$st"

# ==============================================================================
# ITEM 2: limited-role filing and edit holds (#766) across REST, CLI and MCP.
# vic = grain/viewer, dana = grain/contributor (group devs).
# ==============================================================================
code POST /api/tasks '{"title":"operator target","description":"edited by limited roles","noRepo":true}' >/dev/null
TARGET=$(jq -r '.id // .task.id' "$BODY")
code GET "/api/tasks/$TARGET" >/dev/null
cp "$BODY" "$WORK/target-before.json"
n0=$(count_tasks)

st=$(as vic@example.com PATCH "/api/tasks/$TARGET" '{"title":"viewer renamed"}')
check "2.1" "viewer PATCH task refused" '^403$' "$st"
st=$(as vic@example.com POST "/api/tasks/$TARGET/approve" '{}')
check "2.2" "viewer approve refused" '^403$' "$st"
st=$(as vic@example.com POST "/api/tasks/$TARGET/force-merge" '{}')
check "2.3" "viewer force-merge refused" '^403$' "$st"
st=$(as vic@example.com PUT "/api/secrets/demo-secret/value" '{"value":"x"}')
check "2.4" "viewer secret set refused" '^403$' "$st"
st=$(as vic@example.com POST /api/access/principals '{"entry":"user:vic2@example.com","role":"grain/operator","reason":"x"}')
check "2.5" "viewer Settings -> Access add refused" '^403$' "$st"
st=$(as vic@example.com GET /api/fs/task/)
check "2.6" "viewer grain fs (task://) read allowed (read scope)" '^200$' "$st"
st=$(as vic@example.com GET /api/logs)
check "2.7" "viewer daemon logs readable: deployment:read is a non-dangerous *:read" '^200$' "$st"

st=$(as dana@example.com POST /api/tasks '{"title":"contributor dangerous","description":"x","noRepo":true,"capabilities":["api:config:write"]}')
check "2.8" "contributor filing with a dangerous api: grant is held or refused" '^(201|403)$' "$st"
dang_body=$(body)
DANG_TASK=$(jq -r '.id // .task.id // empty' "$BODY")
if [[ -n $DANG_TASK ]]; then
	code GET "/api/tasks/$DANG_TASK" >/dev/null
	check "2.8b" "dangerous filing held for approval (not queued)" '"(status|state)":"(held|unapproved|pending_approval|awaiting_approval|proposed)"|"approved":false|"held' "$(body)"
else
	check "2.8b" "dangerous filing refused with a role message" 'grain/contributor|role' "$dang_body"
fi
st=$(as dana@example.com POST /api/tasks '{"title":"contributor durable","description":"x","noRepo":true,"admission":"durable"}')
check "2.9" "contributor admission above standard held or refused" '^(201|403)$' "$st"
dur_body=$(body)
DUR_TASK=$(jq -r '.id // .task.id // empty' "$BODY")
if [[ -n $DUR_TASK ]]; then
	code GET "/api/tasks/$DUR_TASK" >/dev/null
	check "2.9b" "durable filing held for a person" '"held|unapproved|approval' "$(body)"
	cp "$BODY" "$WORK/durable-task.json"
	st=$(as dana@example.com POST "/api/tasks/$DUR_TASK/approve" '{}')
	check "2.10" "contributor cannot approve their own held task" '^403$' "$st"
else
	check "2.9b" "durable filing refused with a role message" 'admission|role' "$dur_body"
fi
st=$(as dana@example.com PATCH "/api/tasks/$TARGET" '{"admission":"bypass"}')
check "2.11" "contributor PATCH admission bypass on another task refused or held" '^(403|202)$|^200$' "$st"
code GET "/api/tasks/$TARGET" >/dev/null
check "2.11b" "target task admission not raised" '^(null|""|"standard")$' "$(jq -c '.admission // .task.admission // null' "$BODY")"
st=$(as dana@example.com POST "/api/tasks/$TARGET/force-merge" '{}')
check "2.12" "contributor force-merge refused" '^403$' "$st"
st=$(as dana@example.com PUT "/api/secrets/demo-secret/value" '{"value":"x"}')
check "2.13" "contributor secret rotation refused" '^403$' "$st"
st=$(as dana@example.com POST /api/access/principals '{"entry":"user:dana@example.com","role":"grain/operator","reason":"self-promote"}')
check "2.14" "contributor self-promotion refused" '^403$' "$st"
st=$(as dana@example.com GET /api/logs)
check "2.15" "contributor daemon logs readable (deployment:read via grain/viewer)" '^200$' "$st"
code GET /api/secrets >/dev/null
check "2.16" "no secret was created by refused calls" '^0$' "$(grep -c 'demo-secret' "$BODY")"

# Workstation CLI as each person through the IAP proxy. design/spec/cli
# cli.spec.md #CLI-10: a 401/403 exits 3 (4 is "daemon unreachable").
out=$(gcli vic@example.com create -title "cli viewer" -no-repo -body "x")
rc=$?
check "2.17" "CLI viewer grain create exits 3 naming the role" '^3 .*grain/viewer' "$rc $out"
out=$(gcli vic@example.com access allow --role grain/operator --reason x "user:vic@example.com")
rc=$?
check "2.18" "CLI viewer grain access allow exits 3" '^3 .*access:write' "$rc $out"
out=$(gcli vic@example.com approve "$TARGET")
rc=$?
check "2.19" "CLI viewer grain approve exits 3" '^3 ' "$rc $out"
out=$(gcli vic@example.com logs "$TARGET")
rc=$?
check "2.20" "CLI viewer grain logs <task> passes the role gate (not exit 3)" '^(0|2 .*has not run yet)' "$rc $out"
out=$(gcli dana@example.com force-merge "$TARGET" "rbac walkthrough")
rc=$?
check "2.21" "CLI contributor grain force-merge exits 3" '^3 ' "$rc $out"
out=$(gcli dana@example.com access role --reason x "user:alice@example.com" grain/operator)
rc=$?
check "2.22" "CLI contributor grain access role exits 3" '^3 .*grain/contributor' "$rc $out"
out=$(gcli dana@example.com create -title "cli contributor" -no-repo -body "x" -capability "api:config:write")
rc=$?
check "2.22b" "CLI contributor dangerous -capability refused or held" '^(3|0) ' "$rc $out"
echo "$out" >"$WORK/cli-contributor-dangerous.txt"
CLI_DANG=$(echo "$out" | grep -oE '^[0-9]+$|task #?[0-9]+' | grep -oE '[0-9]+' | head -1)

code GET "/api/tasks/$TARGET" >/dev/null
check "2.23" "target task unchanged after every refused edit" "$(jq -r '.title // .task.title' "$WORK/target-before.json")" "$(jq -r '.title // .task.title' "$BODY")"
n1=$(count_tasks)
expected_new=0
[[ -n $DANG_TASK ]] && expected_new=$((expected_new + 1))
[[ -n $DUR_TASK ]] && expected_new=$((expected_new + 1))
[[ $rc == 0 ]] && expected_new=$((expected_new + 1))
check "2.24" "only held filings were stored ($n0 + $expected_new)" "^$((n0 + expected_new))$" "$n1"

# ==============================================================================
# ITEM 3: per-tool MCP checks on /api/mcp and /mcp, plus gma_ owner downgrade.
# ==============================================================================
tools_for() { # base hdr path -> sorted top-level tool names
	mcp "$1" "$2" "$3" tools/list | sed -n 's/^data: //p;/^{/p' | jq -r '.result.tools[].name' 2>/dev/null | sort
}
routes_for() { # base hdr path -> sorted api_* route tools the caller may invoke
	mcp "$1" "$2" "$3" tools/call '{"name":"grain_explore_scope","arguments":{"view":"index"}}' | sed -n 's/^data: //p;/^{/p' |
		jq -r '.result.content[0].text' 2>/dev/null | grep -oE 'api_[a-z0-9_]+' | sort -u
}
refused() { echo "$1" | grep -qiE 'HTTP 40[13]|holds no|no grant|not allowed|refus|forbidden|cannot|wider than|"isError": *true'; }
invoke_all() { # base hdr path toolsfile leakfile -> count of calls not refused
	local leaks=0 t resp
	while read -r t; do
		[[ -z $t ]] && continue
		resp=$(mcp "$1" "$2" "$3" tools/call "$(jq -cn --arg t "$t" '{name:"grain_invoke",arguments:{tool:$t,reason:"rbac walkthrough",arguments:{id:"'"$TARGET"'",body:{title:"mcp leak",description:"x",noRepo:true}}}}')")
		if ! refused "$resp"; then
			leaks=$((leaks + 1))
			echo "$t: ${resp:0:300}" >>"$5"
		fi
	done <"$4"
	echo "$leaks"
}
OP_TOOLS=$(tools_for "$DAEMON" "Authorization: Bearer $CODE" /api/mcp)
VIEW_TOOLS=$(tools_for "$DAEMON" "$IAP_HDR: $(sign_jwt vic@example.com)" /api/mcp)
echo "$OP_TOOLS" >"$WORK/tools-operator.txt"
echo "$VIEW_TOOLS" >"$WORK/tools-viewer.txt"
check "3.1" "/api/mcp tools/list: viewer sees the compact meta-tools" 'grain_explore_scope.*grain_invoke' "$(echo "$VIEW_TOOLS" | tr '\n' ' ')"
OP_R=$(routes_for "$DAEMON" "Authorization: Bearer $CODE" /api/mcp)
V_R=$(routes_for "$DAEMON" "$IAP_HDR: $(sign_jwt vic@example.com)" /api/mcp)
C_R=$(routes_for "$DAEMON" "$IAP_HDR: $(sign_jwt dana@example.com)" /api/mcp)
echo "$OP_R" >"$WORK/routes-operator.txt"
echo "$V_R" >"$WORK/routes-viewer.txt"
echo "$C_R" >"$WORK/routes-contributor.txt"
op_n=$(echo "$OP_R" | grep -c .)
v_n=$(echo "$V_R" | grep -c .)
c_n=$(echo "$C_R" | grep -c .)
check "3.2" "explore_scope routes shrink with role (operator > contributor > viewer > 0)" 'ok' "$([[ $op_n -gt $c_n && $c_n -gt $v_n && $v_n -gt 0 ]] && echo ok || echo "op=$op_n c=$c_n v=$v_n")"
check "3.3" "viewer's routes are all GETs (no create/update/delete/approve/merge/detach)" '^$' "$(echo "$V_R" | grep -E '^api_(create|update|delete|approve|merge|set|revoke|detach|post|put|patch|rotate|force)' | head -3 | tr '\n' ' ')"
check "3.3b" "capabilities:detach / config:write / dispatch routes absent for viewer" '^$' "$(echo "$V_R" | grep -E 'detach|set_config|update_config|dispatch' | grep -vE '^api_(get|list)' | head -3 | tr '\n' ' ')"
comm -23 <(echo "$OP_R") <(echo "$V_R") >"$WORK/omitted-viewer.txt"
comm -23 <(echo "$OP_R") <(echo "$C_R") >"$WORK/omitted-contributor.txt"
nb=$(count_tasks)
lv=$(invoke_all "$DAEMON" "$IAP_HDR: $(sign_jwt vic@example.com)" /api/mcp "$WORK/omitted-viewer.txt" "$WORK/mcp-viewer-leaks.txt")
check "3.4" "viewer grain_invoke refused on all $(grep -c . "$WORK/omitted-viewer.txt") omitted routes (/api/mcp)" '^0$' "$lv"
lc=$(invoke_all "$DAEMON" "$IAP_HDR: $(sign_jwt dana@example.com)" /api/mcp "$WORK/omitted-contributor.txt" "$WORK/mcp-contributor-leaks.txt")
check "3.5" "contributor grain_invoke refused on all $(grep -c . "$WORK/omitted-contributor.txt") omitted routes (/api/mcp)" '^0$' "$lc"
resp=$(mcp "$DAEMON" "$IAP_HDR: $(sign_jwt vic@example.com)" /api/mcp tools/call '{"name":"grain_submit_task","arguments":{"title":"viewer mcp submit","description":"x","noRepo":true}}')
check "3.6" "viewer grain_submit_task refused" 'HTTP 403|holds no|grain/viewer' "$resp"
check "3.7" "no task or state created by refused MCP calls" "^$nb$" "$(count_tasks)"
code GET "/api/tasks/$TARGET" >/dev/null
check "3.7b" "target task title untouched by MCP calls" "^operator target$" "$(jq -r '.title // .task.title' "$BODY")"

# gma_ token owned by olivia (operator), then downgrade olivia to viewer.
code PUT /api/settings '{"externalAgentApi":true}' >/dev/null
gacc allow --role grain/operator --reason "olivia operates" "user:olivia@example.com" >/dev/null
code POST /api/external-agents/tokens '{"name":"olivia-laptop","scopeSet":"grain/operator","owner":"olivia@example.com"}' >/dev/null
cp "$BODY" "$WORK/gma-issue.json"
GMA=$(jq -r '.token // empty' "$BODY")
check "3.8" "operator-scoped gma_ token issued for olivia" '^gma_' "$GMA"
G_OP=$(tools_for "$DAEMON" "Authorization: Bearer $GMA" /mcp)
g_op_n=$(echo "$G_OP" | grep -c .)
resp=$(mcp "$DAEMON" "Authorization: Bearer $GMA" /mcp tools/call '{"name":"grain_submit_task","arguments":{"title":"gma while operator","description":"x","noRepo":true}}')
echo "$resp" >"$WORK/gma-call-op.json"
check "3.9" "gma_ token can file while its owner is operator" '"id"|[Tt]ask' "$resp"
out=$(gacc role --reason "downgrade" "user:olivia@example.com" grain/viewer)
check "3.10" "grain access role downgrades olivia to grain/viewer" 'olivia@example.com' "$out"
G_V=$(tools_for "$DAEMON" "Authorization: Bearer $GMA" /mcp)
echo "$G_OP" >"$WORK/routes-gma-operator-owner.txt"
echo "$G_V" >"$WORK/routes-gma-viewer-owner.txt"
g_v_n=$(echo "$G_V" | grep -c .)
resp=$(mcp "$DAEMON" "Authorization: Bearer $GMA" /mcp tools/list)
check "3.11" "downgraded owner's operator gma_ refused on /mcp tools/list (fix F2; was $g_op_n tools)" "wider than its owner's current role" "$resp"
comm -23 <(echo "$G_OP") <(echo "$G_V") >"$WORK/omitted-gma.txt"
nb=$(count_tasks)
lg=$(invoke_all "$DAEMON" "Authorization: Bearer $GMA" /mcp "$WORK/omitted-gma.txt" "$WORK/mcp-gma-leaks.txt")
check "3.12" "downgraded owner's gma_ refused on all $(grep -c . "$WORK/omitted-gma.txt") omitted routes (/mcp)" '^0$' "$lg"
resp=$(mcp "$DAEMON" "Authorization: Bearer $GMA" /mcp tools/call '{"name":"api_create_task","arguments":{"title":"gma after downgrade","description":"x","noRepo":true}}')
check "3.13" "downgraded owner's gma_ api_create_task on /mcp refused" "wider than its owner's current role" "$resp"
resp=$(mcp "$DAEMON" "Authorization: Bearer $GMA" /api/mcp tools/call '{"name":"grain_invoke","arguments":{"tool":"api_create_task","arguments":{"body":{"title":"gma api mcp","description":"x","noRepo":true}}}}')
check "3.14" "downgraded owner's gma_ grain_invoke on /api/mcp refused" "wider than its owner's current role" "$resp"
st=$(req "$DAEMON" "Authorization: Bearer $GMA" POST /api/tasks '{"title":"gma rest","description":"x","noRepo":true}')
check "3.14b" "downgraded owner's gma_ REST POST /api/tasks refused" '^403$' "$st"
code POST /api/external-agents/tokens '{"name":"olivia-reader","scopeSet":"grain/viewer","owner":"olivia@example.com"}' >/dev/null
GMA_V=$(jq -r '.token // empty' "$BODY")
G_VV=$(tools_for "$DAEMON" "Authorization: Bearer $GMA_V" /mcp)
check "3.14c" "a viewer-scoped token re-issued for olivia works, within viewer ($(echo "$G_VV" | grep -c .) tools, none create/update)" 'ok' "$([[ $(echo "$G_VV" | grep -c .) -gt 0 ]] && ! echo "$G_VV" | grep -qE '^api_(create|update|delete|set)_' && echo ok || echo bad)"
check "3.15" "no task created by the downgraded gma_ token" "^$nb$" "$(count_tasks)"
gacc deny "user:olivia@example.com" >/dev/null
resp=$(mcp "$DAEMON" "Authorization: Bearer $GMA" /mcp tools/call '{"name":"api_create_task","arguments":{"title":"gma after removal","description":"x","noRepo":true}}')
echo "$resp" >"$WORK/gma-removed-owner.json"
# Observation O4: an owner the allowlist no longer matches is outside F2's cap.
check "3.16" "gma_ of a removed owner: recorded (see O4)" '.' "$resp"
check "3.17" "tasks after owner removal (recorded, see O4)" '^[0-9]+$' "$(count_tasks)"

cp "$RESULTS" "$SCRIPT_DIR/results.md"
for f in whoami-alice.json auth-user-alice.json auth-user-alice-groupdown.json whoami-delegated.json whoami-nobody.json \
	vic-with-code.json tools-operator.txt tools-viewer.txt routes-operator.txt routes-viewer.txt routes-contributor.txt \
	routes-gma-operator-owner.txt routes-gma-viewer-owner.txt cli-contributor-dangerous.txt gma-removed-owner.json \
	access-list-1.txt gma-call-op.json durable-task.json mcp-viewer-leaks.txt mcp-contributor-leaks.txt mcp-gma-leaks.txt; do
	[[ -f $WORK/$f ]] && cp "$WORK/$f" "$SCRIPT_DIR/$f"
done
echo "FAILS=$FAILS"
if [[ ${HOLD:-} == 1 ]]; then
	echo "holding daemon at $DAEMON / proxy $PROXY; Ctrl-C to stop"
	wait
fi
exit $((FAILS > 0))
