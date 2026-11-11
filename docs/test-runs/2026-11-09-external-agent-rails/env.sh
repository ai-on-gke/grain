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

# Environment for docs/test-plans/2026-11-09.md §7 (copied from
# ../2026-11-08-attribution-external-rails/env.sh, task 3639): a real `grain daemon`
# (SQLite, control plane only) behind tests/iap/iapsim.go (a local IAP
# JWKS issuer + proxy) admitting alice@ and bob@, with GitHub played by
# ghsim.go (githubsim over HTTP, acme/widgets whose default branch is
# trunk).
#
#   source env.sh; start_env [WORK]   # WORK defaults to /var/tmp/ext-rails
#   stop_env
#
# Helpers once started: as EMAIL METHOD PATH [BODY] (fresh IAP assertion),
# code METHOD PATH [BODY] (the shared access code), agent TASK METHOD PATH
# [BODY] (the access code + X-Grain-Acting-Task, which is what the
# daemon's sandbox API proxy forwards for a run), tok TOKEN METHOD PATH
# [BODY] (a gma_ credential). Each prints the HTTP status and leaves the
# body in $BODY.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:4292"
PROXY="http://127.0.0.1:4293"
DAEMON="http://127.0.0.1:4291"
GHSIM="127.0.0.1:4294"
IAP_HDR="X-Goog-IAP-JWT-Assertion"

start_env() {
	WORK=${1:-/var/tmp/ext-rails}
	stop_env
	rm -rf "$WORK"
	mkdir -p "$WORK/data" "$WORK/home" "$WORK/bin"
	(cd "$ROOT" && go build -o "$WORK/bin/iapsim" tests/iap/iapsim.go && go build -o "$WORK/bin/ghsim" "$HERE/../2026-11-08-attribution-external-rails/ghsim.go") || return 2

	# acme/widgets: default branch trunk, plus release/v1.
	git init -q --bare -b trunk "$WORK/widgets.git"
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b trunk
	echo "# acme/widgets" >"$tmp/README.md"
	git -C "$tmp" add README.md
	git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed"
	git -C "$tmp" push -q "$WORK/widgets.git" trunk trunk:release/v1
	rm -rf "$tmp"

	export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
	unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
	"$WORK/bin/iapsim" -issuer "${ISSUER#http://}" -proxy "${PROXY#http://}" -upstream "$DAEMON" -aud "$AUD" -iss "$ISS" \
		>"$WORK/iapsim.log" 2>&1 &
	echo $! >"$WORK/iapsim.pid"
	"$WORK/bin/ghsim" -addr "$GHSIM" -bare "$WORK/widgets.git" -default trunk >"$WORK/ghsim.log" 2>&1 &
	echo $! >"$WORK/ghsim.pid"
	for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && break; sleep 0.1; done

	# A GitHub credential, so tasks may target acme/widgets (ghsim does not check it).
	echo "ghp_walkthrough" >"$WORK/bot.token"
	printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/bot.token" >"$WORK/seed.yaml"
	"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed.yaml" >"$WORK/connections.log" 2>&1 || return 2

	# shellcheck disable=SC2086 # DAEMON_EXTRA_FLAGS is split on purpose
	"$GRAIN" daemon -data-dir "$WORK/data" -ui-addr "${DAEMON#http://}" -sandbox-port-base=0 \
		-disable-native-sandboxes -ci=false -poll-interval 2s -as owner@example.com \
		-repo acme/widgets -default-target-repo acme/widgets \
		-github-host "$GHSIM" -github-insecure-http \
		-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
		-ui-allowed-principals user:alice@example.com,user:bob@example.com \
		${DAEMON_EXTRA_FLAGS:-} >"$WORK/daemon.log" 2>&1 &
	echo $! >"$WORK/daemon.pid"
	for _ in $(seq 300); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true) != "000" ]] && break
		sleep 0.1
	done
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db"
	BODY="$WORK/body.json"
	export WORK CODE DB BODY
}

stop_env() {
	local p pid
	for p in daemon iapsim ghsim; do
		[[ -f "$WORK/$p.pid" ]] || continue
		pid=$(cat "$WORK/$p.pid")
		kill "$pid" 2>/dev/null
		for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	done
	return 0
}

sign_jwt() { curl -sf "$ISSUER/sign?email=$1&aud=$AUD&iss=$ISS"; }
req() { # base method path body headers...
	local base=$1 method=$2 path=$3 body=$4
	shift 4
	local args=(-s -o "$BODY" -w '%{http_code}' -X "$method")
	for h in "$@"; do args+=(-H "$h"); done
	[[ -n $body ]] && args+=(-H "Content-Type: application/json" -d "$body")
	curl "${args[@]}" "$base$path"
}
as() { req "$DAEMON" "$2" "$3" "${4:-}" "$IAP_HDR: $(sign_jwt "$1")"; }
code() { req "$DAEMON" "$1" "$2" "${3:-}" "Authorization: Bearer $CODE"; }
agent() { req "$DAEMON" "$2" "$3" "${4:-}" "Authorization: Bearer $CODE" "X-Grain-Acting-Task: $1" "X-Grain-Acting-Reason: walkthrough"; }
tok() { req "$DAEMON" "$2" "$3" "${4:-}" "Authorization: Bearer $1" "X-Grain-Acting-Reason: walkthrough"; }
body() { tr -d '\n' <"$BODY"; }
gacc() { "$GRAIN" -server "$DAEMON" -token "$CODE" access "$@" 2>&1; }
sql() { sqlite3 -json "$DB" "$1"; }

# propose TASK ARGS_JSON: the scripted task agent. It calls the real
# propose_task tool the way a run's agent does -- through `grain mcpserver
# -interactive -server ... -task TASK`, the MCP server a run's agent CLI is
# handed -- and prints the tool's answer. The new task's id is in
# $WORK/proposed.id.
propose() {
	local in="$WORK/propose.$$.jsonl"
	printf '%s\n' \
		'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"scripted-agent","version":"1"}}}' \
		'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"propose_task\",\"arguments\":$2}}" >"$in"
	mkdir -p "$WORK/sandbox-$1"
	local text
	text=$( (cat "$in"; sleep 2) | GRAIN_LOCAL_API_TOKEN_FILE="$WORK/data/local-api-token" timeout 15 "$GRAIN" mcpserver -interactive \
		-server "$DAEMON" -task "$1" -sandbox-root "$WORK/sandbox-$1" -propose-repo acme/widgets 2>>"$WORK/mcpserver.log" |
		jq -r 'select(.id==2) | .result.content[0].text // .error.message')
	rm -f "$in"
	sed -n 's|.*/tasks/\([0-9][0-9]*\).*|\1|p' <<<"$text" | head -1 >"$WORK/proposed.id"
	echo "$text"
}
