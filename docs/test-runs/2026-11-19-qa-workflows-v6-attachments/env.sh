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

# Shared real-daemon environment for docs/test-plans/2026-11-19.md §2
# (QA workflows v6 consolidation, specification attachment hydration,
# propose_dag decomposition, ephemeral gitproxy shallow mirrors, and /next/ chips).
# Starts auth-enforcing ghsim on 127.0.0.1:4284 and a real `grain daemon` on
# 127.0.0.1:4280 over SQLite (STORE=sqlite) or PostgreSQL (STORE=postgres).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4280}
GHSIM_PORT=${GHSIM_PORT:-4284}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_qav6?sslmode=disable}
export PYTHONDONTWRITEBYTECODE=1

chmod +x "$HERE/fake-agy.py"

stop_daemon() {
	local work=${1:-${WORK_DIR:-/tmp/walk-qa-v6}}
	if [[ -f "$work/daemon.pid" ]]; then
		local pid
		pid=$(cat "$work/daemon.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 80); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/daemon.pid"
	fi
}

stop_env() {
	local work=${1:-${WORK_DIR:-/tmp/walk-qa-v6}}
	stop_daemon "$work"
	if [[ -f "$work/ghsim.pid" ]]; then
		local pid
		pid=$(cat "$work/ghsim.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 50); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/ghsim.pid"
	fi
}

init_env() {
	WORK_DIR=${1:-/tmp/walk-qa-v6}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs" "$WORK_DIR/captures"

	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_qav6 WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_qav6 OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p \
		"$tmp/.grain/workflows" \
		"$tmp/.github/workflows" \
		"$tmp/pkg/ui" \
		"$tmp/pkg/orchestrator" \
		"$tmp/pkg/model" \
		"$tmp/pkg/caller" \
		"$tmp/ui-ng/src/app/shared"

	cat >"$tmp/go.mod" <<'EOF'
module example.com/acme/widgets

go 1.24
EOF
	cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
	cat >"$tmp/.grain/settings.yml" <<'EOF'
ci_mode: grain
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
          echo "PASS"
EOF
	cat >"$tmp/.github/workflows/tests.yml" <<'EOF'
name: Tests
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo ok
EOF
	cat >"$tmp/pkg/ui/qa_api.go" <<'EOF'
package ui

// RenderSummary formats a workflow badge summary.
func RenderSummary(flow string) string {
	return flow
}
EOF
	cat >"$tmp/pkg/ui/qa_api_test.go" <<'EOF'
package ui

import "testing"

func TestRenderSummary(t *testing.T) {
	if got := RenderSummary("standard-workflow"); got != "standard-workflow" {
		t.Fatalf("got %q", got)
	}
}
EOF
	cat >"$tmp/pkg/orchestrator/engine.go" <<'EOF'
package orchestrator

// Dispatch schedules a task run.
func Dispatch(id string) string {
	return "dispatch:" + id
}
EOF
	cat >"$tmp/pkg/model/store.go" <<'EOF'
package model

// Record persists an item.
func Record(k, v string) string {
	return k + "=" + v
}
EOF
	cat >"$tmp/pkg/caller/caller.go" <<'EOF'
package caller

import "example.com/acme/widgets/pkg/ui"

// Format delegates to pkg/ui.
func Format(flow string) string {
	return ui.RenderSummary(flow)
}
EOF
	cat >"$tmp/ui-ng/src/app/shared/chips.ts" <<'EOF'
export function formatChip(label: string): string {
  return label;
}
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed acme/widgets"
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
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$WORK_DIR/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	fi
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	local require_auth=${REQUIRE_GIT_AUTH:-true}
	"$work/bin/ghsim" -addr "$GHSIM" -bare "$work/widgets.git" -default main -require-git-auth="$require_auth" >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && return 0
		sleep 0.1
	done
	echo "ghsim failed to start; see $work/ghsim.log" >&2
	return 1
}

start_daemon() {
	local work=${1:-$WORK_DIR}
	shift || true
	stop_daemon "$work"
	touch "$work/daemon.log"
	local store_flags=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		store_flags=(-postgres-url "$PG_DSN")
	fi
	local port_base=${PORT_BASE:-0}
	local offset
	offset=$(wc -c <"$work/daemon.log")
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$work/home" \
		WALK_DIR="$work" \
		PYTHONDONTWRITEBYTECODE=1 \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$GRAIN" daemon \
		-data-dir "$work/data" \
		-sandbox-dir "$work/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base="$port_base" \
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
		"${store_flags[@]}" \
		"$@" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"
	for _ in $(seq 120); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				if [[ -f "$work/seed-models.yaml" && ! -f "$work/.models-seeded" ]]; then
					if [[ ${STORE:-sqlite} == sqlite ]]; then
						"$GRAIN" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					else
						"$GRAIN" connections -data-dir "$work/data" -db-driver postgres -db-dsn "$PG_DSN" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					fi
					touch "$work/.models-seeded"
					sleep 0.6
				fi
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM PG_DSN
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; tail of $work/daemon.log:" >&2
			tail -c +"$((offset + 1))" "$work/daemon.log" | tail -n 40 >&2
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
		if [[ "$body" == @* ]]; then
			curl -fsS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" --data-binary "$body" "$DAEMON$path"
		else
			curl -fsS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
		fi
	else
		curl -fsS -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}

api_status() {
	local method=$1 path=$2 body=${3:-} out_file=$4
	if [[ -n "$body" ]]; then
		curl -sS -o "$out_file" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -sS -o "$out_file" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}

store_exec() {
	local sql=$1
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -q -h 127.0.0.1 -U grain -d grain_walk_qav6 -c "$sql"
	fi
}

store_query() {
	local sql=$1
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -qAt -h 127.0.0.1 -U grain -d grain_walk_qav6 -c "$sql"
	fi
}

wait_task_state() {
	local id=$1 want_re=$2 max_iters=${3:-120}
	for _ in $(seq "$max_iters"); do
		local st
		st=$(api GET "/api/tasks/$id" | jq -r '.task.state // .state')
		if [[ "$st" =~ ^($want_re)$ ]]; then
			echo "$st"
			return 0
		fi
		sleep 0.5
	done
	local final_st
	final_st=$(api GET "/api/tasks/$id" | jq -r '.task.state // .state')
	echo "timeout waiting for task $id to match $want_re (got $final_st)" >&2
	return 1
}
