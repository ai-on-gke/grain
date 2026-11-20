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

# Shared real-daemon environment for docs/test-plans/2026-11-19.md §4
# (/next/processes parity, template priority/interactive, scoped RBAC, and
# daemon woken-cycle scheduling) over SQLite (STORE=sqlite) or PostgreSQL
# (STORE=postgres).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4290}
SIM_PORT=${SIM_PORT:-4294}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
SIM="127.0.0.1:${SIM_PORT}"
PG_DB=${PG_DB:-grain_walk_proc}
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/${PG_DB}?sslmode=disable}
export PYTHONDONTWRITEBYTECODE=1

chmod +x "$HERE/fake-agy.py"

stop_daemon() {
	local work=${1:-${WORK_DIR:-/tmp/walk-proc}}
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
	local work=${1:-${WORK_DIR:-/tmp/walk-proc}}
	stop_daemon "$work"
	if [[ -f "$work/sim.pid" ]]; then
		local pid
		pid=$(cat "$work/sim.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 50); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/sim.pid"
	fi
}

seed_bare_repo() {
	local bare=$1 name=$2 extra_branch=${3:-}
	git init -q --bare -b main "$bare"
	git -C "$bare" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	cat >"$tmp/README.md" <<EOF
# acme/${name}
EOF
	git -C "$tmp" add README.md
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed ${name}"
	git -C "$tmp" push -q "$bare" main
	if [[ -n "$extra_branch" ]]; then
		git -C "$tmp" checkout -qb "$extra_branch"
		echo "release branch ${extra_branch}" >>"$tmp/README.md"
		git -C "$tmp" add README.md
		git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed ${extra_branch}"
		git -C "$tmp" push -q "$bare" "$extra_branch"
	fi
	rm -rf "$tmp"
}

init_env() {
	WORK_DIR=${1:-/tmp/walk-proc}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs" "$WORK_DIR/repos"

	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS ${PG_DB} WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE ${PG_DB} OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/walk-sim" "$HERE/sim.go")

	seed_bare_repo "$WORK_DIR/repos/widgets.git" widgets "release/v1"
	seed_bare_repo "$WORK_DIR/repos/docs.git" docs ""
	seed_bare_repo "$WORK_DIR/repos/gadgets.git" gadgets ""

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

start_sim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/walk-sim" -addr "$SIM" -repos-dir "$work/repos" -iap-aud "walk-iap-aud" -iap-iss "https://walk-iap.example" >"$work/sim.log" 2>&1 &
	echo $! >"$work/sim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$SIM/_sim/iap-keys" >/dev/null && return 0
		sleep 0.1
	done
	echo "walk-sim failed to start; see $work/sim.log" >&2
	return 1
}

store_exec() {
	local sql=$1
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -v ON_ERROR_STOP=1 -q -h 127.0.0.1 -U grain -d "$PG_DB" -c "$sql"
	fi
}

seed_custom_fields() {
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		store_exec "
			INSERT OR REPLACE INTO custom_field (name, label, type, options, description, default_value, position, multiple, show_on_row) VALUES
				('priority', 'Priority', 'enum', '[\"low\",\"high\"]', 'Task triage priority', 'low', 0, 0, 1),
				('requested-by', 'Requested by', 'text', '[]', 'Requesting team', '', 1, 0, 0),
				('urgent', 'Urgent', 'bool', '[]', 'Urgent flag', '', 2, 0, 0),
				('tags', 'Tags', 'text', '[]', 'Area tags', '', 3, 1, 0);
		"
	else
		store_exec "
			INSERT INTO custom_field (name, label, type, options, description, default_value, position, multiple, show_on_row) VALUES
				('priority', 'Priority', 'enum', '[\"low\",\"high\"]', 'Task triage priority', 'low', 0, false, true),
				('requested-by', 'Requested by', 'text', '[]', 'Requesting team', '', 1, false, false),
				('urgent', 'Urgent', 'bool', '[]', 'Urgent flag', '', 2, false, false),
				('tags', 'Tags', 'text', '[]', 'Area tags', '', 3, true, false)
			ON CONFLICT (name) DO UPDATE SET
				label = EXCLUDED.label,
				type = EXCLUDED.type,
				options = EXCLUDED.options,
				description = EXCLUDED.description,
				default_value = EXCLUDED.default_value,
				position = EXCLUDED.position,
				multiple = EXCLUDED.multiple,
				show_on_row = EXCLUDED.show_on_row;
		"
	fi
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
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval "${POLL_INTERVAL:-1s}" \
		-max-workers "${MAX_WORKERS:-1}" \
		-max-mergers 0 \
		-agent-framework antigravity \
		-agy-path "$HERE/fake-agy.py" \
		-gemini-api-key-file "$work/gemini.key" \
		-github-host "$SIM" \
		-github-insecure-http \
		-ui-iap-audience "walk-iap-aud" \
		-ui-iap-issuer "https://walk-iap.example" \
		-ui-iap-keys-url "http://${SIM}/_sim/iap-keys" \
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
					seed_custom_fields
					touch "$work/.models-seeded"
					sleep 0.6
				fi
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON SIM PG_DSN
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; tail of $work/daemon.log:" >&2
			tail -c +"$((offset + 1))" "$work/daemon.log" | tail -n 50 >&2
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
		curl -fsS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -fsS -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}

mint_iap() {
	local email=$1
	curl -fsS "http://${SIM}/_sim/mint-iap?email=${email}"
}

seed_rbac_and_repos() {
	# Allow acme/widgets, acme/docs, acme/gadgets as target repos.
	api PUT /api/settings '{"targetRepos":["acme/widgets","acme/docs","acme/gadgets"]}' >/dev/null

	# Scope sets for real scoped RBAC walkthrough:
	# 1. walk/suites-widgets-only: suites:write & templates:write restricted to acme/widgets
	api PUT /api/api-scope-sets '{"set":{"name":"walk/suites-widgets-only","description":"Write suites and templates on acme/widgets only","scopes":["suites:write","templates:write"],"repos":["acme/widgets"]}}' >/dev/null
	# 2. walk/bob-suites-a-only: read all repos, write suites/templates only on acme/widgets (NOT acme/gadgets)
	api PUT /api/api-scope-sets '{"set":{"name":"walk/bob-suites-a-only","description":"Read all; suites:write on acme/widgets only","scopes":["tasks:read","schedules:read","templates:read","suites:read"],"includes":["walk/suites-widgets-only"]}}' >/dev/null
	# 3. walk/carol-no-tmpl-write: suites:write everywhere, NO templates:write
	api PUT /api/api-scope-sets '{"set":{"name":"walk/carol-no-tmpl-write","description":"Write suites without templates:write","scopes":["tasks:read","schedules:read","templates:read","suites:read","suites:write"]}}' >/dev/null

	# Admit alice (operator), bob (suites:write on repo A only), carol (no templates:write).
	api POST /api/access/principals '{"entry":"user:alice@example.com","role":"grain/operator","reason":"walkthrough operator"}' >/dev/null || true
	api POST /api/access/principals '{"entry":"user:bob@example.com","role":"walk/bob-suites-a-only","reason":"walkthrough repo-scoped suites:write"}' >/dev/null
	api POST /api/access/principals '{"entry":"user:carol@example.com","role":"walk/carol-no-tmpl-write","reason":"walkthrough no templates:write"}' >/dev/null
}
