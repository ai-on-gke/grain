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

# End-to-end walkthrough of IAP sign-in (design/design/authentication.design.md,
# docs/access-code.md) against a real `grain demo` behind iapsim.go, a
# simulated IAP: an ES256 issuer with a JWKS, a reverse proxy that adds
# X-Goog-IAP-JWT-Assertion the way IAP does, and a fake Cloud Identity.
# API and CLI cases use curl and the grain CLI; UI cases run browser.mjs in
# a real Chromium (Playwright from ui/node_modules).
#
#   tests/iap/walkthrough.sh [OUT]       # OUT defaults to /tmp/iap-walkthrough
#
# Needs: go, curl, jq, node with ui/node_modules installed, an embedded UI
# build (cd ui && npm ci && npm run build), and Playwright's Chromium
# (PLAYWRIGHT_BROWSERS_PATH, default /var/cache/playwright). Set GRAIN to
# a prebuilt binary to skip `go build`. Ports 4300, 18080, 18443 and 18091
# must be free. Writes OUT/results.md (one row per check), OUT/*.png and
# the demo/iapsim logs, and exits 1 if any check failed.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${1:-/tmp/iap-walkthrough}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}

AUD=/projects/123/global/backendServices/456
ISS=https://iap.test.local
ISSUER=http://127.0.0.1:18080
PROXY=http://127.0.0.1:18443
DEMO=http://127.0.0.1:4300
CODE_FILE=$OUT/access-code
# A non-loopback address of this host, to reach the demo "over the network".
NETIP=$(hostname -I 2>/dev/null | awk '{print $1}')
NET=http://$NETIP:4300
IAP_FLAGS=(-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks")

RESULTS=$OUT/results.md
FAILS=0
printf '| Case | Check | Expected | Got | Result |\n|---|---|---|---|---|\n' > "$RESULTS"

# check CASE "what" EXPECTED GOT [OTHERWISE] -- passes when GOT matches the
# extended regex EXPECTED. OTHERWISE, when given, is what a mismatch is
# recorded as instead of FAIL ("N/A: ..." for work that has not landed,
# "DIFFERS: ..." where the code deliberately does otherwise), and it is
# not counted as a failure.
check() {
	local res=PASS
	if ! [[ $4 =~ $3 ]]; then
		if [[ -n ${5:-} ]]; then res=$5; else res=FAIL; FAILS=$((FAILS + 1)); fi
	fi
	local got=${4//|/\\|}
	got=${got//$'\n'/ }
	printf '| %s | %s | `%s` | `%s` | %s |\n' "$1" "$2" "$3" "${got:0:160}" "$res" | tee -a "$RESULTS"
}

PIDS=()
cleanup() { for p in "${PIDS[@]}" ${DEMO_PID:-}; do kill "$p" 2>/dev/null; done; }
trap cleanup EXIT

if [[ -z ${GRAIN:-} ]]; then
	(cd "$ROOT" && go build -o "$OUT/grain" ./cmd/grain) || exit 2
	GRAIN=$OUT/grain
fi
(cd "$ROOT" && go build -o "$OUT/iapsim" tests/iap/iapsim.go) || exit 2

"$OUT/iapsim" -issuer 127.0.0.1:18080 -proxy 127.0.0.1:18443 -upstream "$DEMO" -aud "$AUD" -iss "$ISS" \
	-group ops@example.com=carol@example.com > "$OUT/iapsim.log" 2>&1 &
PIDS+=($!)

# Application Default Credentials for the group lookup: a service account
# whose token_uri is iapsim's /token, so CloudIdentityGroupChecker's real
# OAuth and HTTP path runs against the fake Cloud Identity.
openssl genrsa 2048 2>/dev/null > "$OUT/sa.key"
jq -n --rawfile k "$OUT/sa.key" --arg t "$ISSUER/token" \
	'{type:"service_account",project_id:"iapsim",private_key_id:"1",private_key:$k,client_email:"grain-daemon@iapsim.iam.gserviceaccount.com",client_id:"1",token_uri:$t}' \
	> "$OUT/adc.json"
# No ambient credentials anywhere else: no gcloud config, no metadata server.
export HOME=$OUT/home
mkdir -p "$HOME"
unset GOOGLE_APPLICATION_CREDENTIALS GRAIN_SERVER GRAIN_TOKEN GRAIN_IAP_AUDIENCE GRAIN_REQUIRE_IAP GRAIN_ALLOWED_PRINCIPALS
export GCE_METADATA_HOST=127.0.0.1:9 # refuse, so DefaultCredentials never waits on 169.254.169.254

# start_demo NAME [flags...]: (re)start the demo on 0.0.0.0:4300, so it is
# reachable both from loopback and from $NET.
start_demo() {
	local name=$1
	shift
	stop_demo
	mkdir -p "$OUT/tmp-$name"
	TMPDIR=$OUT/tmp-$name "$GRAIN" demo -open=false -addr 0.0.0.0:4300 -as owner@example.com -access-code-file "$CODE_FILE" "$@" \
		> "$OUT/demo-$name.log" 2>&1 &
	DEMO_PID=$!
	DEMO_LOG=$OUT/demo-$name.log
	for _ in $(seq 100); do
		curl -s -o /dev/null "$DEMO/healthz" && break
		sleep 0.2
	done
	CODE=$(cat "$CODE_FILE")
	DB=$(ls "$OUT"/tmp-"$name"/grain-demo-*/grain.db 2>/dev/null | head -1)
	curl -s -X POST "$ISSUER/stats/reset"
}
stop_demo() {
	if [[ -n ${DEMO_PID:-} ]]; then
		kill "$DEMO_PID" 2>/dev/null
		wait "$DEMO_PID" 2>/dev/null
		DEMO_PID=
	fi
}

# tok EMAIL [query...]: an assertion from the issuer.
tok() {
	local email=$1 q
	shift
	q="email=$email&aud=$AUD&iss=$ISS"
	for a in "$@"; do q="$q&$a"; done
	curl -s "$ISSUER/sign?$q"
}
# code_of CURL-ARGS...: the HTTP status only.
code_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# st_body CURL-ARGS...: "STATUS BODY".
st_body() {
	local f
	f=$(mktemp)
	local s
	s=$(curl -s -o "$f" -w '%{http_code}' "$@")
	echo "$s $(head -c 800 "$f")"
	rm -f "$f"
}
as() { echo "X-Iapsim-Email: $1"; }
# cloud_identity_calls: the calls the fake Cloud Identity and its token
# endpoint have had since the last reset (not the JWKS or the proxy).
cloud_identity_calls() { curl -s "$ISSUER/stats" | jq -c 'with_entries(select(.key != "jwks" and .key != "proxied"))'; }
A=X-Goog-IAP-JWT-Assertion
browser() { (cd "$ROOT/ui" && OUT=$OUT PROXY=$PROXY DEMO=$DEMO NET=$NET CODE=$CODE node "$ROOT/tests/iap/browser.mjs") >> "$RESULTS" 2> "$OUT/browser-$MODE.log" || FAILS=$((FAILS + 1)); }

echo "## IAP on, default allowlist (the owner)" | tee -a "$RESULTS"
start_demo iap "${IAP_FLAGS[@]}"

# 1. Allowed person: UI, API, CLI, no access code.
check 1 "API: GET /api/auth/user through the proxy" '^200 .*"user":"owner@example.com".*"authMethod":"iap"' \
	"$(st_body -H "$(as owner@example.com)" "$PROXY/api/auth/user")"
check 1 "API: GET /api/tasks through the proxy" '^200' "$(code_of -H "$(as owner@example.com)" "$PROXY/api/tasks")"
check 1 "API: POST /api/tasks through the proxy" '^20[01] .*"author":"owner@example.com"' \
	"$(st_body -H "$(as owner@example.com)" -H 'Content-Type: application/json' -d '{"title":"filed through IAP by curl"}' "$PROXY/api/tasks")"
check 1 "API: GET /api/whoami (operator-attribution spec §5)" '^200 .*owner@example.com' \
	"$(st_body -H "$(as owner@example.com)" "$PROXY/api/whoami")" "N/A: GET /api/whoami not on main (operator-attribution spec §5)"
# The CLI has no IAP mode yet (authentication.design.md rollout step 3,
# which would fetch the ID token itself); today it carries a Google ID token
# as -token/$GRAIN_ID_TOKEN, which IAP checks. The CLI holds no access code
# (empty HOME, no data dir).
export GRAIN_ID_TOKEN=iapsim-idtoken.owner@example.com
cli_list=$(GRAIN_SERVER=$PROXY "$GRAIN" list 2>&1)
check 1 "CLI: grain list through the proxy, no code" 'filed through IAP by curl' "$cli_list"
cli_create=$(GRAIN_SERVER=$PROXY "$GRAIN" -json create -title "filed through IAP by the CLI" 2>&1)
cli_id=$(jq -r '.id // empty' <<<"$cli_create" 2>/dev/null)
check 1 "CLI: grain create through the proxy, no code" '^[0-9]+$' "$cli_id"
[[ -n $cli_id ]] && check 1 "CLI: the created task's author" '^owner@example.com human$' \
	"$(curl -s -H "$(as owner@example.com)" "$PROXY/api/tasks/$cli_id" | jq -r '"\(.author) \(.authorKind)"')"
check 1 "CLI without IAP (the ID token straight to grain) is refused" 'access code|401|single-tenant' \
	"$(GRAIN_SERVER=$DEMO "$GRAIN" list 2>&1 | head -3)"
unset GRAIN_ID_TOKEN
MODE=iap browser

# 3. Bad assertions: straight to the demo (as a request that bypassed the
# load balancer would), each with the valid access code too, so a fall-through
# to the code would show as 200.
for row in "wrong signature (signed by an unpublished key, kid k1)|kid=k1&key=k2" \
	"corrupted signature byte|badsig=1" \
	"unknown kid|kid=k2" \
	"wrong aud|AUD" \
	"wrong iss|ISS" \
	"expired|exp=-10m" \
	"issued in the future|iat=10m"; do
	name=${row%%|*}
	q=${row#*|}
	case $q in
	AUD) t=$(curl -s "$ISSUER/sign?email=owner@example.com&aud=/projects/999/global/backendServices/1&iss=$ISS") ;;
	ISS) t=$(curl -s "$ISSUER/sign?email=owner@example.com&aud=$AUD&iss=https://cloud.google.com/iap") ;;
	*) t=$(tok owner@example.com "$q") ;;
	esac
	check 3 "$name, no code" '^401' "$(st_body -H "$A: $t" "$DEMO/api/auth/user")"
	check 3 "$name, plus the valid code" '^401' "$(st_body -H "$A: $t" -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"
done
check 3 "garbled token, no code" '^401' "$(st_body -H "$A: not.a.jwt" "$DEMO/api/auth/user")"
check 3 "garbled token, plus the valid code" '^401' "$(st_body -H "$A: garbage" -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"
check 3 "garbled token, plus the access cookie (browser)" '^401' \
	"$(st_body -H "$A: garbage" -H "Accept: text/html" -H "Authorization: Bearer $CODE" "$DEMO/")"
check 3 "refused assertions are logged" 'refused.*(signature|expired|issuer|audience)' \
	"$(grep -iE 'assertion' "$DEMO_LOG" | head -3)"

# 4. Spoofed plaintext identity headers.
check 4 "X-Goog-Authenticated-User-Email alone" '^401' \
	"$(st_body -H 'X-Goog-Authenticated-User-Email: accounts.google.com:owner@example.com' "$DEMO/api/auth/user")"
check 4 "X-Grain-User alone" '^401' "$(st_body -H 'X-Grain-User: owner@example.com' "$DEMO/api/auth/user")"
check 4 "code + X-Goog-Authenticated-User-Email: mallory is not mallory" '^200 .*"authMethod":"trusted"' \
	"$(st_body -H "Authorization: Bearer $CODE" -H 'X-Goog-Authenticated-User-Email: accounts.google.com:mallory@example.com' "$DEMO/api/auth/user")"
check 4 "code + X-Grain-User: mallory is not mallory" '^200 .*"authMethod":"trusted"' \
	"$(st_body -H "Authorization: Bearer $CODE" -H 'X-Grain-User: mallory@example.com' "$DEMO/api/auth/user")"
check 4 "assertion(owner) + X-Goog-Authenticated-User-Email: mallory is owner" '^200 .*"user":"owner@example.com"' \
	"$(st_body -H "$A: $(tok owner@example.com)" -H 'X-Goog-Authenticated-User-Email: accounts.google.com:mallory@example.com' "$DEMO/api/auth/user")"
check 4 "assertion(owner) + X-Grain-User: mallory is owner" '^200 .*"user":"owner@example.com"' \
	"$(st_body -H "$A: $(tok owner@example.com)" -H 'X-Grain-User: mallory@example.com' "$DEMO/api/auth/user")"
sleep 0.5
check 4 "X-Goog-Authenticated-User-Email is logged" 'ignored the unsigned X-Goog-Authenticated-User-Email' "$(cat "$DEMO_LOG")"
check 4 "X-Grain-User is logged" 'ignored the unsigned X-Grain-User' "$(grep -c 'X-Grain-User' "$DEMO_LOG")" \
	"DIFFERS: not logged by design -- grain's own UI sends X-Grain-User on every request (ownClientIdentityHeaders)"

# 5. An assertion plus the access code: the assertion wins.
check 5 "assertion(owner) + code: the caller is the person" '^200 .*"user":"owner@example.com".*"authMethod":"iap"' \
	"$(st_body -H "$A: $(tok owner@example.com)" -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"
t5=$(curl -s -H "$A: $(tok owner@example.com)" -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' \
	-d '{"title":"assertion plus code"}' "$DEMO/api/tasks")
check 5 "assertion(owner) + code: a task filed is the person's" '^owner@example.com human$' "$(jq -r '"\(.author) \(.authorKind)"' <<<"$t5")"
check 5 "assertion(mallory, not allowed) + code: refused, not trusted" '^403' \
	"$(st_body -H "$A: $(tok mallory@example.com)" -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"

# 9. CSRF with a valid assertion.
post_task() { st_body -H 'Content-Type: application/json' -d '{"title":"csrf probe"}' "$@" "$PROXY/api/tasks"; }
check 9 "same-origin POST (Origin = proxy)" '^201' "$(post_task -H "$(as owner@example.com)" -H "Origin: $PROXY" -H 'Sec-Fetch-Site: same-origin')"
check 9 "cross-origin POST (Origin: https://evil.example)" '^403' "$(post_task -H "$(as owner@example.com)" -H 'Origin: https://evil.example' -H 'Sec-Fetch-Site: cross-site')"
check 9 "cross-site POST (Sec-Fetch-Site only)" '^403' "$(post_task -H "$(as owner@example.com)" -H 'Sec-Fetch-Site: cross-site')"
check 9 "Origin: null" '^403' "$(post_task -H "$(as owner@example.com)" -H 'Origin: null')"
check 9 "IAP cookie, no Origin/Sec-Fetch-Site" '^403' "$(post_task -H "$(as owner@example.com)" -H 'Cookie: GCP_IAP_UID=x')"
check 9 "cross-origin DELETE" '^403' \
	"$(st_body -X DELETE -H "$(as owner@example.com)" -H 'Origin: https://evil.example' "$PROXY/api/tasks/1")"

echo "## IAP on, -ui-allowed-principals with a fake Cloud Identity" | tee -a "$RESULTS"
ALLOW='user:alice@example.com,domain:corp.example,serviceAccount:bot@proj.iam.gserviceaccount.com,group:ops@example.com'
GOOGLE_APPLICATION_CREDENTIALS=$OUT/adc.json start_demo allowlist "${IAP_FLAGS[@]}" \
	-ui-allowed-principals "$ALLOW" -ui-cloud-identity-endpoint "$ISSUER"
who() { st_body -H "$(as "$1")" "$PROXY/api/auth/user"; }
check 2 "user:alice admitted" '^200 .*"user":"alice@example.com"' "$(who alice@example.com)"
check 2 "user: entry is case-insensitive (ALICE@Example.com)" '^200 .*"user":"alice@example.com"' "$(who ALICE@Example.com)"
check 2 "bob (no entry) refused, API" '^403 .*bob@example.com' "$(who bob@example.com)"
check 2 "the owner is not in a replaced list" '^403' "$(who owner@example.com)"
check 2 "domain:corp.example admits dev@corp.example" '^200 .*"user":"dev@corp.example"' "$(who dev@corp.example)"
check 2 "domain: is exact (dev@sub.corp.example)" '^403' "$(who dev@sub.corp.example)"
check 2 "serviceAccount: admits bot" '^200 .*"user":"bot@proj.iam.gserviceaccount.com"' "$(who bot@proj.iam.gserviceaccount.com)"
tb=$(curl -s -H "$(as bot@proj.iam.gserviceaccount.com)" -H 'Content-Type: application/json' -d '{"title":"filed by the bot"}' "$PROXY/api/tasks")
check 2 "serviceAccount: principal kind" '^bot@proj.iam.gserviceaccount.com service$' "$(jq -r '"\(.author) \(.authorKind)"' <<<"$tb")"
check 2 "a service account never matches a user: entry (alice's domain SA)" '^403' "$(who other@proj.iam.gserviceaccount.com)"
curl -s -X POST "$ISSUER/stats/reset"
check 2 "group: member carol admitted (fake lookup)" '^200 .*"user":"carol@example.com"' "$(who carol@example.com)"
check 2 "group: non-member dave refused" '^403 .*dave@example.com' "$(who dave@example.com)"
check 2 "the fake Cloud Identity was asked" '"checkTransitiveMembership":[1-9]' "$(curl -s "$ISSUER/stats")"
curl -s -X POST "$ISSUER/stats/reset"
who alice@example.com >/dev/null
check 2 "a direct entry is decided without asking Cloud Identity" '^\{\}$' "$(cloud_identity_calls)"

# 8. Attribution: alice files, comments, approves and closes; the access
# code does the same. The API shows author and closer; the approval actor
# is only in the store, so it is read from there.
api() { curl -s -H "$(as alice@example.com)" -H 'Content-Type: application/json' "$@"; }
tid=$(api -d '{"title":"attribution probe"}' "$PROXY/api/tasks" | jq -r .id)
api -d '{"body":"a comment from alice"}' "$PROXY/api/tasks/$tid/comments" > /dev/null
api -X POST "$PROXY/api/tasks/$tid/approve" > /dev/null
api -d '{"reason":"done"}' "$PROXY/api/tasks/$tid/close" > /dev/null
curl -s -H "$(as alice@example.com)" "$PROXY/api/tasks/$tid" > "$OUT/attr-task.json"
check 8 "file: task author (API)" '^alice@example.com human' "$(jq -r '"\(.author) \(.authorKind)"' "$OUT/attr-task.json")"
check 8 "comment: author (API)" '^alice@example.com human$' \
	"$(jq -r '.comments[] | select(.body=="a comment from alice") | "\(.author) \(.authorKind)"' "$OUT/attr-task.json")"
check 8 "close: transition by (API)" '^human alice@example.com$' "$(jq -r '.transitions[] | select(.state=="closed") | .by' "$OUT/attr-task.json")"
check 8 "file+approve: store's origin and approval actors" '^human\|alice@example.com\|human\|alice@example.com$' \
	"$(sqlite3 "$DB" "select origin_actor_kind, origin_actor_id, approval_actor_kind, approval_actor_id from task where id=$tid")"
check 8 "close: store's task_event actor" '^closed\|human\|alice@example.com$' \
	"$(sqlite3 "$DB" "select kind, actor_kind, actor_id from task_event where task_id=$tid and kind='closed'")"
check 8 "the recorded source is iap ({human, email, iap}; spec OA-1.1)" 'iap' \
	"$(sqlite3 "$DB" "select group_concat(name) from pragma_table_info('task') where name like '%actor_source%'" 2>&1) $(jq -r '.authorSource // "(no authorSource in the API)"' "$OUT/attr-task.json")" \
	"N/A: Principal.Source (OA-1.1) not on main"
cid=$(curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' -d '{"title":"filed with the access code"}' "$DEMO/api/tasks" | jq -r .id)
curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' -d '{"body":"a comment with the code"}' "$DEMO/api/tasks/$cid/comments" > /dev/null
curl -s -H "Authorization: Bearer $CODE" "$DEMO/api/tasks/$cid" > "$OUT/attr-code.json"
check 8 "access code: recorded as the deployment's actor" '^owner@example.com human$' "$(jq -r '"\(.author) \(.authorKind)"' "$OUT/attr-code.json")"
check 8 "access code: shown as 'operator (access code)' (spec OA-1.2.2)" 'operator \(access code\)' \
	"$(jq -c '{author,authorKind,authorLabel}' "$OUT/attr-code.json")" "N/A: <p>Label (OA-1.2.2) not on main"
MODE=allowlist ATTR_TASK=$tid browser

echo "## group: entries with no lookup configured" | tee -a "$RESULTS"
start_demo nolookup "${IAP_FLAGS[@]}" -ui-allowed-principals 'user:alice@example.com,group:ops@example.com'
check 2 "no ADC: a direct entry still admits alice" '^200 .*alice@example.com' "$(who alice@example.com)"
check 2 "no ADC: group-dependent carol refused, fails closed" '^403 .*(credentials|group)' "$(who carol@example.com)"
check 2 "no ADC: the Cloud Identity stand-in was never called" '^\{\}$' "$(cloud_identity_calls)"
GOOGLE_APPLICATION_CREDENTIALS=$OUT/adc.json start_demo nogroups "${IAP_FLAGS[@]}" \
	-ui-allowed-principals 'user:alice@example.com,domain:corp.example' -ui-cloud-identity-endpoint "$ISSUER"
who alice@example.com > /dev/null
check 2 "no group: entries, lookup available: carol refused" '^403' "$(who carol@example.com)"
check 2 "no group: entries: Cloud Identity never called (not even a token)" '^\{\}$' "$(cloud_identity_calls)"

echo "## No IAP configured" | tee -a "$RESULTS"
start_demo noiap
check 6 "no code: 401" '^401' "$(code_of "$DEMO/api/auth/user")"
check 6 "bearer code: 200 trusted" '^200 .*"authMethod":"trusted"' "$(st_body -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"
check 6 "bearer code from the network ($NETIP)" '^200' "$(code_of -H "Authorization: Bearer $CODE" "$NET/api/auth/user")"
check 6 "a browser gets the login page" '^401 .*[Aa]ccess code' "$(st_body -H 'Accept: text/html' "$DEMO/")"
jar=$OUT/noiap.jar
check 6 "POST /api/auth/access-code sets grain_access" '^200' \
	"$(code_of -c "$jar" -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}" "$DEMO/api/auth/access-code")"
check 6 "the cookie authenticates" '^200' "$(code_of -b "$jar" "$DEMO/api/auth/user")"
check 6 "a wrong code is refused" '^(401|403)' "$(code_of -H 'Content-Type: application/json' -d '{"code":"nope"}' "$DEMO/api/auth/access-code")"
check 6 "a valid-looking assertion does not authenticate" '^401' "$(code_of -H "$A: $(tok owner@example.com)" "$DEMO/api/auth/user")"
check 6 "... and is logged" 'no IAP audience is configured' "$(cat "$DEMO_LOG")"
MODE=noiap browser

echo "## -ui-require-iap" | tee -a "$RESULTS"
start_demo requireiap "${IAP_FLAGS[@]}" -ui-require-iap
jar=$OUT/require.jar
check 7 "loopback, no forwarding headers: bearer works" '^200 .*"authMethod":"trusted"' \
	"$(st_body -H "Authorization: Bearer $CODE" "$DEMO/api/auth/user")"
check 7 "loopback, no forwarding headers: login sets a cookie" '^200' \
	"$(code_of -c "$jar" -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}" "$DEMO/api/auth/access-code")"
check 7 "loopback, no forwarding headers: cookie works" '^200' "$(code_of -b "$jar" "$DEMO/api/auth/user")"
check 7 "network ($NETIP): bearer refused" '^(401|403)' "$(st_body -H "Authorization: Bearer $CODE" "$NET/api/auth/user")"
check 7 "network ($NETIP): cookie refused" '^(401|403)' "$(code_of -b "$jar" "$NET/api/auth/user")"
check 7 "network ($NETIP): login refused" '^403' \
	"$(st_body -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}" "$NET/api/auth/access-code")"
check 7 "loopback + X-Forwarded-For: bearer refused" '^(401|403)' \
	"$(st_body -H "Authorization: Bearer $CODE" -H 'X-Forwarded-For: 203.0.113.7' "$DEMO/api/auth/user")"
check 7 "loopback + X-Forwarded-For: cookie refused" '^(401|403)' "$(code_of -b "$jar" -H 'X-Forwarded-For: 203.0.113.7' "$DEMO/api/auth/user")"
check 7 "loopback + Forwarded: bearer refused" '^(401|403)' "$(code_of -H "Authorization: Bearer $CODE" -H 'Forwarded: for=203.0.113.7' "$DEMO/api/auth/user")"
check 7 "through the proxy (the code is not needed): the IAP person" '^200 .*"authMethod":"iap"' \
	"$(st_body -H "$(as owner@example.com)" -H "Authorization: Bearer $CODE" "$PROXY/api/auth/user")"
check 7 "network browser: no login page" '^(401|403)' "$(st_body -H 'Accept: text/html' "$NET/")"
check 7 "loopback CLI (kubectl exec break-glass) still works with the code" 'proposed|queued' \
	"$(GRAIN_SERVER=$DEMO GRAIN_TOKEN=$CODE "$GRAIN" list 2>&1 | head -3)"
MODE=requireiap browser
stop_demo

out=$("$GRAIN" demo -open=false -addr 127.0.0.1:4300 -ui-require-iap 2>&1)
check 7 "grain demo -ui-require-iap with no audience refuses to start" 'needs an IAP audience' "$out"
out=$(timeout 20 "$GRAIN" daemon -data-dir "$OUT/daemon-data" -sandbox-port-base=0 -host-sandboxes -sandbox-dir "$OUT/daemon-sb" -ui-addr 127.0.0.1:4301 -ui-require-iap 2>&1)
check 7 "grain daemon -ui-require-iap with no audience refuses to start" 'needs an IAP audience' "$out"
out=$(GRAIN_IAP_AUDIENCE=$AUD timeout 5 "$GRAIN" demo -open=false -addr 127.0.0.1:4300 -ui-require-iap -access-code-file "$CODE_FILE" 2>&1)
check 7 "... and \$GRAIN_IAP_AUDIENCE counts as an audience (starts)" 'serving fake tasks' "$out"

echo
echo "failures: $FAILS (results in $RESULTS)"
[[ $FAILS -eq 0 ]]
