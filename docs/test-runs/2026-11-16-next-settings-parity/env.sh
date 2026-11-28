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

# Shared real-daemon environment for docs/test-plans/2026-11-16.md §9
# (/next/settings parity with classic over PostgreSQL + SQLite spot check).
# Reuses docs/test-runs/2026-11-13-grain-health-exit-codes/env.sh and adds:
#   - gitforges (HTTPS smart-git on 127.0.0.1:4294 + SSH git on 127.0.0.1:4295)
#   - fake-codex.py for per-connection Codex auth.json verification
#   - SSL_CERT_FILE pointing to gitforges' CA so gitproxy speaks real HTTPS.

set -euo pipefail

HERE9=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT9=$(cd "$HERE9/../../.." && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$HERE9/../2026-11-13-grain-health-exit-codes/env.sh"

HTTPS_GIT_PORT=${HTTPS_GIT_PORT:-4294}
SSH_GIT_PORT=${SSH_GIT_PORT:-4295}
HTTPS_GIT_ADDR="127.0.0.1:${HTTPS_GIT_PORT}"
SSH_GIT_ADDR="127.0.0.1:${SSH_GIT_PORT}"
HTTPS_GIT_TOKEN=${HTTPS_GIT_TOKEN:-walk_https_secret_pat_99}

stop_gitforges() {
	local work=${1:-${WORK_DIR:-/tmp/walk-settings-parity}}
	if [[ -f "$work/gitforges.pid" ]]; then
		local pid
		pid=$(cat "$work/gitforges.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 40); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/gitforges.pid"
	fi
}

stop_agentd() {
	local work=${1:-${WORK_DIR:-/tmp/walk-settings-parity}}
	if [[ -f "$work/agentd.pid" ]]; then
		local pid
		pid=$(cat "$work/agentd.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 40); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$work/agentd.pid"
	fi
}

stop_all() {
	local work=${1:-${WORK_DIR:-/tmp/walk-settings-parity}}
	stop_agentd "$work"
	stop_gitforges "$work"
	stop_env "$work"
}

init_walk_env() {
	local work=${1:-/tmp/walk-settings-parity}
	stop_all "$work"
	init_env "$work"
	mkdir -p "$work/gitroot/acme" "$work/certs"

	# Seed two bare repos served by gitforges: acme/https-repo.git and acme/ssh-repo.git
	for r in https-repo ssh-repo; do
		git init -q --bare -b main "$work/gitroot/acme/$r.git"
		git -C "$work/gitroot/acme/$r.git" config http.receivepack true
		local tmp
		tmp=$(mktemp -d)
		git -C "$tmp" init -q -b main
		echo "# acme/$r (seeded for section 9 walkthrough)" >"$tmp/README.md"
		echo "walkthrough-$r-marker" >"$tmp/MARKER.txt"
		git -C "$tmp" add .
		git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed $r"
		git -C "$tmp" push -q "$work/gitroot/acme/$r.git" main
		rm -rf "$tmp"
	done

	(cd "$ROOT9" && go build -o "$work/bin/gitforges" "$HERE9/gitforges.go")
	chmod +x "$HERE9/fake-codex.py"
}

start_gitforges() {
	local work=${1:-$WORK_DIR}
	stop_gitforges "$work"
	"$work/bin/gitforges" \
		-https-addr "$HTTPS_GIT_ADDR" \
		-ssh-addr "$SSH_GIT_ADDR" \
		-root "$work/gitroot" \
		-cert-dir "$work/certs" \
		-https-token "$HTTPS_GIT_TOKEN" \
		-log "$work/gitforges-audit.jsonl" >"$work/gitforges.log" 2>&1 &
	echo $! >"$work/gitforges.pid"
	for _ in $(seq 50); do
		if [[ -s "$work/certs/ca.pem" && -s "$work/certs/known_hosts" ]] && \
			curl -sf --cacert "$work/certs/ca.pem" "https://$HTTPS_GIT_ADDR/healthz" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.1
	done
	echo "gitforges failed to start; see $work/gitforges.log" >&2
	return 1
}

start_walk_daemon() {
	local work=${1:-$WORK_DIR}
	shift || true
	stop_daemon "$work"
	touch "$work/daemon.log"
	local store_flags=()
	if [[ ${STORE:-postgres} == postgres ]]; then
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
		SSL_CERT_FILE="$work/certs/ca.pem" \
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
		-max-workers "${MAX_WORKERS:-2}" \
		-max-mergers 0 \
		-agent-framework antigravity \
		-agy-path "$HERE/fake-agy.py" \
		-codex-path "$HERE9/fake-codex.py" \
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
					if [[ ${STORE:-postgres} == sqlite ]]; then
						"$GRAIN" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					else
						"$GRAIN" connections -data-dir "$work/data" -db-driver postgres -db-dsn "$PG_DSN" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
					fi
					touch "$work/.models-seeded"
					sleep 0.6
				fi
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM PG_DSN HTTPS_GIT_ADDR SSH_GIT_ADDR HTTPS_GIT_TOKEN
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

grain_cli() {
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$WORK_DIR/home" \
		GRAIN_SERVER="$DAEMON" \
		GRAIN_TOKEN="$CODE" \
		GRAIN_DATA_DIR="$WORK_DIR/data" \
		"$GRAIN" "$@"
}
