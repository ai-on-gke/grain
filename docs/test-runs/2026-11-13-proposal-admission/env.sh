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

# Shared real-daemon environment for docs/test-plans/2026-11-13.md §5
# (proposal and grant admission). Starts ghsim on 127.0.0.1:4284 and a real
# `grain daemon -sandbox-port-base=0` with `-host-sandboxes` on 127.0.0.1:4280,
# over SQLite by default or PostgreSQL with STORE=postgres (PG_DSN).
# Adapted from docs/test-runs/2026-11-11-durable-tasks/env.sh.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4280}
GHSIM_PORT=${GHSIM_PORT:-4284}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"

chmod +x "$HERE/fake-agy.py"

stop_env() {
	local work=${1:-${WORK_DIR:-/tmp/walk-admission}}
	local p pid
	for p in daemon ghsim; do
		if [[ -f "$work/$p.pid" ]]; then
			pid=$(cat "$work/$p.pid")
			kill "$pid" 2>/dev/null || true
			for _ in $(seq 50); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.1
			done
		fi
	done
}

init_env() {
	WORK_DIR=${1:-/tmp/walk-admission}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs"

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.grain/workflows"
	cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
	cat >"$tmp/.grain/workflows/ci.yml" <<'EOF'
name: ci
on:
  push:
  pull_request:
jobs:
  unit:
    runs-on: linux
    capabilities: []
    steps:
      - name: unit
        run: |
          echo "=== RUN   TestWidgetsPass"
          echo "--- PASS: TestWidgetsPass (0.01s)"
          echo "PASS"
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed widgets"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main
	rm -rf "$tmp"

	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo "walkthrough-gemini-key" >"$WORK_DIR/gemini.key"
	echo '{"directConfigWrites": true}' >"$WORK_DIR/data/state-repo.json"
	cat >"$WORK_DIR/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
EOF
	cat >"$WORK_DIR/seed-models.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: $WORK_DIR/gemini.key
default_model: gemini-3.1-pro
EOF
	"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" -addr "$GHSIM" -bare "$work/widgets.git" -default main >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && return 0
		sleep 0.1
	done
	echo "ghsim failed to start; see $work/ghsim.log" >&2
	return 1
}

start_daemon() {
	local bin=${1:-$GRAIN}
	local work=${2:-$WORK_DIR}
	shift 2 || true
	if [[ -f "$work/daemon.pid" ]]; then
		local oldpid
		oldpid=$(cat "$work/daemon.pid")
		kill "$oldpid" 2>/dev/null || true
		for _ in $(seq 50); do kill -0 "$oldpid" 2>/dev/null || break; sleep 0.1; done
	fi
	touch "$work/daemon.log"
	STORE_FLAGS=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		STORE_FLAGS=(-postgres-url "${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable}")
	fi
	local offset
	offset=$(wc -c <"$work/daemon.log")
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$work/home" \
		WALK_DIR="$work" \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$bin" daemon \
		-data-dir "$work/data" \
		-sandbox-dir "$work/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval "${POLL_INTERVAL:-500ms}" \
		-max-workers "${MAX_WORKERS:-1}" \
		-max-mergers 0 \
		-agent-framework antigravity \
		-agy-path "$HERE/fake-agy.py" \
		-gemini-api-key-file "$work/gemini.key" \
		-github-host "$GHSIM" \
		-github-insecure-http \
		"${STORE_FLAGS[@]}" \
		"$@" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"
	for _ in $(seq 120); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				if [[ -f "$work/seed-models.yaml" && ! -f "$work/.models-seeded" ]]; then
					"$bin" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					touch "$work/.models-seeded"
					sleep 0.7
				fi
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; tail of $work/daemon.log:" >&2
			tail -c +"$((offset + 1))" "$work/daemon.log" | tail -n 30 >&2
			return 1
		fi
		sleep 0.2
	done
	echo "daemon did not come up; see $work/daemon.log" >&2
	return 1
}

api() {
	local method=$1 path=$2 body=${3:-}
	if [[ -n "$body" ]]; then
		curl -sS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -sS -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}
