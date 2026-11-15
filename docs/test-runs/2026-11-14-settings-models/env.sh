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

# docs/test-plans/2026-11-14.md §4: a real `grain daemon` over SQLite
# (STORE=sqlite) or PostgreSQL (STORE=postgres) on 127.0.0.1:4280, ghsim
# on 4284, iapsim (JWKS 4295, IAP proxy 4296) for the viewer and limited
# operator, and fakeprovider.go standing in for api.anthropic.com (the
# only stubbed external service; /etc/hosts maps the name to 127.0.0.1
# and only the daemon trusts fakeprovider's CA through SSL_CERT_FILE).
# Agent runs go to the 2026-11-13 fake agy CLI.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
SHARED="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes"
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK_DIR=${WORK_DIR:-/home/debian/walk-models}
DAEMON_PORT=4280
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:4284"
IAP_ISSUER="http://127.0.0.1:4295"
IAP_PROXY="http://127.0.0.1:4296"
IAP_AUD="/projects/123456/global/backendServices/789012"
IAP_ISS="https://iap.test.local"
FAKE_CTL="http://127.0.0.1:4291"
FAKE_CA=${FAKE_CA:-/home/debian/fake-ca.pem}
PG_DSN="postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable"
export PYTHONDONTWRITEBYTECODE=1

stop_pid() {
	[[ -f "$WORK_DIR/$1.pid" ]] || return 0
	local pid
	pid=$(cat "$WORK_DIR/$1.pid")
	kill "$pid" 2>/dev/null || true
	for _ in $(seq 80); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	rm -f "$WORK_DIR/$1.pid"
}

stop_env() { stop_pid daemon; stop_pid iapsim; stop_pid ghsim; }

init_env() {
	stop_env
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR"/{data,sandboxes,bin,home,runs,captures}
	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk OWNER grain;"
	fi
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$SHARED/ghsim.go" && go build -o "$WORK_DIR/bin/iapsim" tests/iap/iapsim.go)
	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	echo "# acme/widgets" >"$tmp/README.md"
	git -C "$tmp" add . && git -C "$tmp" -c user.name=Seed -c user.email=seed@example.com commit -qm "chore: seed"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main
	rm -rf "$tmp"
	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo "walkthrough-gemini-key" >"$WORK_DIR/gemini.key"
	echo "sk-ant-api03-walkthrough" >"$WORK_DIR/anthropic.key"
	echo '{"directConfigWrites": true}' >"$WORK_DIR/data/state-repo.json"
	# anthro discovers (no models listed); walk-a/-b/-c pin one model each
	# so the default/fallback order is observable at dispatch.
	cat >"$WORK_DIR/seed.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
models:
  - id: anthro
    name: Fake Anthropic
    framework: claude
    credential_file: $WORK_DIR/anthropic.key
  - id: conn-a
    framework: antigravity
    models: [walk-a]
    credential_file: $WORK_DIR/gemini.key
  - id: conn-b
    framework: antigravity
    models: [walk-b]
    concurrency: 1
    credential_file: $WORK_DIR/gemini.key
  - id: conn-c
    framework: antigravity
    models: [walk-c]
    credential_file: $WORK_DIR/gemini.key
default_model: walk-a
EOF
}

# import_seed records seed.yaml's connections once the store exists.
import_seed() {
	[[ -f "$WORK_DIR/.seeded" ]] && return 0
	local flags=(-data-dir "$WORK_DIR/data")
	if [[ ${STORE:-sqlite} == postgres ]]; then flags+=(-db-driver postgres -db-dsn "$PG_DSN"); fi
	"$GRAIN" connections "${flags[@]}" import "$WORK_DIR/seed.yaml" >>"$WORK_DIR/connections.log" 2>&1
	touch "$WORK_DIR/.seeded"
}

start_sims() {
	"$WORK_DIR/bin/ghsim" -addr "$GHSIM" -bare "$WORK_DIR/widgets.git" -default main >"$WORK_DIR/ghsim.log" 2>&1 &
	echo $! >"$WORK_DIR/ghsim.pid"
	"$WORK_DIR/bin/iapsim" -issuer 127.0.0.1:4295 -proxy 127.0.0.1:4296 -upstream "$DAEMON" \
		-aud "$IAP_AUD" -iss "$IAP_ISS" >"$WORK_DIR/iapsim.log" 2>&1 &
	echo $! >"$WORK_DIR/iapsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && curl -sf "$IAP_ISSUER/jwks" >/dev/null && return 0
		sleep 0.1
	done
	echo "sims failed" >&2
	return 1
}

start_daemon() {
	stop_pid daemon
	local store_flags=()
	if [[ ${STORE:-sqlite} == postgres ]]; then store_flags=(-postgres-url "$PG_DSN"); fi
	touch "$WORK_DIR/daemon.log"
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$WORK_DIR/home" \
		WALK_DIR="$WORK_DIR" \
		SSL_CERT_FILE="$FAKE_CA" \
		PYTHONDONTWRITEBYTECODE=1 \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$GRAIN" daemon \
		-data-dir "$WORK_DIR/data" \
		-sandbox-dir "$WORK_DIR/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-deployment-owner alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-ui-iap-audience "$IAP_AUD" \
		-ui-iap-issuer "$IAP_ISS" \
		-ui-iap-keys-url "${IAP_ISSUER}/jwks" \
		-poll-interval "${POLL_INTERVAL:-1s}" \
		-max-workers "${MAX_WORKERS:-4}" \
		-max-mergers 0 \
		-agent-framework antigravity \
		-agy-path "$SHARED/fake-agy.py" \
		-gemini-api-key-file "$WORK_DIR/gemini.key" \
		-github-host "$GHSIM" \
		-github-insecure-http \
		"${store_flags[@]}" "$@" >>"$WORK_DIR/daemon.log" 2>&1 </dev/null &
	echo $! >"$WORK_DIR/daemon.pid"
	for _ in $(seq 150); do
		if [[ -s "$WORK_DIR/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				export CODE
				import_seed
				return 0
			fi
		fi
		kill -0 "$(cat "$WORK_DIR/daemon.pid")" 2>/dev/null || { tail -n 40 "$WORK_DIR/daemon.log" >&2; return 1; }
		sleep 0.2
	done
	echo "daemon did not come up" >&2
	return 1
}

# api METHOD PATH [BODY]: as the deployment owner (local access code).
api() {
	local method=$1 path=$2 body=${3:-}
	curl -sS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		${body:+-d "$body"} "$DAEMON$path"
}

# code_as EMAIL METHOD PATH: HTTP status of a call through the IAP proxy as EMAIL.
code_as() {
	curl -s -o "$WORK_DIR/captures/last.body" -w '%{http_code}' -X "$2" -H "X-Iapsim-Email: $1" "$IAP_PROXY$3"
}

# discover ID: POST the route as the owner, compact JSON.
discover() { api POST "/api/deployment/agent-backends/$1/discover" | jq -c '{asked, error, discovered: .backend.discovered}'; }

fake_models() { printf '%s\n' "$@" | curl -sf -X PUT --data-binary @- "$FAKE_CTL/models"; }
fake_mode() { curl -sf -X PUT -d "$1" "$FAKE_CTL/mode"; }
