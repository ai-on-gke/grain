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

# Shared real-daemon environment for docs/test-plans/2026-11-19.md §7
# (cross-task agent halting rail, gitproxy in-place review commit attribution,
# and trust-boundary/v31 live hold triggers). Runs against SQLite by default
# or PostgreSQL with STORE=postgres (PG_DSN).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4290}
GHSIM_PORT=${GHSIM_PORT:-4294}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"

chmod +x "$HERE/fake-agy.py"

stop_env() {
	local work=${1:-${WORK_DIR:-/home/debian/walk-s7}}
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

seed_repo() {
	local bare=$1 name=$2
	git init -q --bare -b main "$bare"
	git -C "$bare" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p \
		"$tmp/pkg/gitproxy" \
		"$tmp/pkg/widgets" \
		"$tmp/pkg/ui" \
		"$tmp/pkg/model" \
		"$tmp/pkg/agentd" \
		"$tmp/cmd/grain-agentd" \
		"$tmp/scripts" \
		"$tmp/lib"

	printf 'package gitproxy\n\nfunc Allow(repo string) bool { return repo != "" }\n' >"$tmp/pkg/gitproxy/proxy.go"
	printf 'package widgets\n\nfunc Validate(s string) bool { return len(s) > 0 }\n' >"$tmp/pkg/widgets/validate.go"
	printf 'package lib\n\nconst F1 = 1\n' >"$tmp/lib/f1.go"
	printf 'package lib\n\nconst F2 = 2\n' >"$tmp/lib/f2.go"

	# trust-boundary/v31 newly classified surfaces
	printf 'package ui\n\nfunc AcceptFindingsStub() {}\n' >"$tmp/pkg/ui/accept_findings.go"
	printf 'package model\n\nfunc ReviewFindingsAcceptStub() {}\n' >"$tmp/pkg/model/review_findings_accept.go"
	printf 'package ui\n\nfunc VerifierSessionStub() {}\n' >"$tmp/pkg/ui/verifier_session.go"
	printf 'package agentd\n\nfunc UpdateStub() {}\n' >"$tmp/pkg/agentd/update.go"
	printf 'package main\n\nfunc main() {}\n' >"$tmp/cmd/grain-agentd/main.go"
	printf '#!/usr/bin/env bash\necho build-agentd\n' >"$tmp/scripts/build-agentd.sh"

	# trust-boundary/v32..v34 surfaces (on HEAD)
	printf 'package model\n\nfunc MergeGatesStub() {}\n' >"$tmp/pkg/model/merge_gates.go"
	printf 'package ui\n\nfunc AgentCrossTaskStub() {}\n' >"$tmp/pkg/ui/agent_cross_task.go"
	printf 'package ui\n\nfunc NotificationsStub() {}\n' >"$tmp/pkg/ui/notifications.go"
	printf 'package ui\n\nfunc RefsStub() {}\n' >"$tmp/pkg/ui/refs.go"

	printf '# acme/%s\n' "$name" >"$tmp/README.md"
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed $name"
	git -C "$tmp" push -q "$bare" main
	rm -rf "$tmp"
}

init_env() {
	WORK_DIR=${1:-/home/debian/walk-s7}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs"

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	seed_repo "$WORK_DIR/widgets.git" widgets
	seed_repo "$WORK_DIR/gadgets.git" gadgets

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
	"$work/bin/ghsim" \
		-addr "$GHSIM" \
		-bare "$work/widgets.git" \
		-bare2 "$work/gadgets.git" \
		-default main >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && \
			curl -sf "http://$GHSIM/repos/acme/gadgets" >/dev/null && return 0
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
		PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_s7?sslmode=disable}
		STORE_FLAGS=(-postgres-url "$PG_DSN")
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
		-repo acme/gadgets \
		-default-target-repo acme/widgets \
		-poll-interval "${POLL_INTERVAL:-300ms}" \
		-max-workers "${MAX_WORKERS:-4}" \
		-max-mergers "${MAX_MERGERS:-1}" \
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
					sleep 0.5
				fi
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM PG_DSN
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

sql_exec() {
	local stmt=$1
	if [[ ${STORE:-sqlite} == postgres ]]; then
		psql "$PG_DSN" -v ON_ERROR_STOP=1 -qAt -c "$stmt"
	else
		sqlite3 -cmd ".timeout 10000" "$DB" "$stmt"
	fi
}
