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

# Shared real-daemon environment for docs/test-plans/2026-11-13-afternoon.md §1
# (task_source_template / templateId and the TemplateID authority boundary).
# Starts ghsim on 127.0.0.1:4285 and a real `grain daemon -sandbox-port-base=0`
# with `-host-sandboxes` on 127.0.0.1:4281, over SQLite or (STORE=postgres)
# PostgreSQL. Sourced by the item*.sh scripts.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4281}
GHSIM_PORT=${GHSIM_PORT:-4285}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"

chmod +x "$HERE/fake-agy.py"

stop_env() {
	local work=${1:-${WORK_DIR:-/tmp/walk-source-template}}
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

recreate_pg_db() {
	local db=$1
	PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d postgres -qc \
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '${db}' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
	PGPASSWORD=grain dropdb -h 127.0.0.1 -U grain --if-exists "$db" 2>/dev/null
	PGPASSWORD=grain createdb -h 127.0.0.1 -U grain "$db"
}

sql_query() {
	if [[ ${STORE:-sqlite} == postgres ]]; then
		PGPASSWORD=grain psql "$PG_DSN" -Atqc "$1"
	else
		sqlite3 "$WORK_DIR/data/store/grain.db" "$1"
	fi
}

init_env() {
	WORK_DIR=$1
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs"
	if [[ ${STORE:-sqlite} == postgres ]]; then
		recreate_pg_db "${PG_DB:-grain_walk_st}"
		export PG_DSN="postgres://grain:grain@127.0.0.1:5432/${PG_DB:-grain_walk_st}?sslmode=disable"
	fi
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.grain/workflows" "$tmp/.grain/templates" "$tmp/.grain/triggers"
	printf '# acme/widgets\nline-on-main\n' >"$tmp/README.md"
	printf '# Changelog\n- initial seed\n' >"$tmp/CHANGELOG.md"
	echo 'port-branches: [main, release]' >"$tmp/.grain/settings.yml"
	cat >"$tmp/.grain/templates/release-note.yml" <<'EOF'
title: Cut walk release note
base: main
nothing-needed: close
capabilities: ["https://grain.dev/capabilities/github#read-issues"]
push:
  - refs/tags/walk-*
body: |
  Cut a walk release note and verify ref push and issue read access.
EOF
	cat >"$tmp/.grain/templates/plain-note.yml" <<'EOF'
title: Plain walk template
base: main
nothing-needed: close
body: |
  Plain template without push or template-declared capabilities.
EOF
	cat >"$tmp/.grain/triggers/walk-merge.yml" <<'EOF'
on: merge
branches: [main]
paths: [CHANGELOG.md]
template: release-note
auto-approve: true
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
        run: echo PASS
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed widgets"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main
	# Paired branch `release` diverges on README.md, so a merge to main that
	# touches README.md cannot be cherry-picked and files an agent port task.
	git -C "$tmp" checkout -qb release
	printf '# acme/widgets\nline-on-release-diverged\n' >"$tmp/README.md"
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qam "chore: diverge release README"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" release
	rm -rf "$tmp"

	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo "walkthrough-model-key" >"$WORK_DIR/gemini.key"
	echo '{"directConfigWrites": true}' >"$WORK_DIR/data/state-repo.json"
	printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK_DIR/bot.token" >"$WORK_DIR/seed-git.yaml"
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
	local bin=${1:-$GRAIN} work=${2:-$WORK_DIR}
	local store_flags=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		store_flags=(-postgres-url "$PG_DSN")
	fi
	touch "$work/daemon.log"
	env -i PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" HOME="$work/home" WALK_DIR="$work" \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$bin" daemon -data-dir "$work/data" -sandbox-dir "$work/sandboxes" -host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" -sandbox-port-base=0 -as alice@example.com \
		-repo acme/widgets -default-target-repo acme/widgets -poll-interval 500ms \
		-max-workers 1 -max-mergers 0 -agy-path "$HERE/fake-agy.py" \
		-gemini-api-key-file "$work/gemini.key" -github-host "$GHSIM" -github-insecure-http \
		"${store_flags[@]}" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"
	for _ in $(seq 150); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				if [[ ! -f "$work/.models-seeded" ]]; then
					"$bin" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					touch "$work/.models-seeded"
					sleep 0.5
				fi
				export WORK_DIR="$work" CODE DAEMON GHSIM
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early:" >&2
			tail -n 30 "$work/daemon.log" >&2
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

task_state() { api GET "/api/tasks/$1" | jq -r '(.task // .).state // empty'; }

wait_task_state() {
	local id=$1 want=$2 timeout_s=${3:-45} st=""
	for _ in $(seq $((timeout_s * 5))); do
		st=$(task_state "$id")
		if [[ "$st" =~ ^($want)$ ]]; then
			echo "$st"
			return 0
		fi
		sleep 0.2
	done
	echo "timeout waiting for task $id ~$want (last: $st)" >&2
	return 1
}
