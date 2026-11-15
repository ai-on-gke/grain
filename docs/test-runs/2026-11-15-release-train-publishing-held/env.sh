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

# Real-daemon environment for docs/test-plans/2026-11-15.md §5 (release-train
# schedules held while publishing is held). Starts ghsim on 127.0.0.1:4294
# serving acme/grain -- a repository seeded with this checkout's own .grain/
# and .github/workflows/ -- and a real `grain daemon -sandbox-port-base=0` on
# 127.0.0.1:4290 over SQLite (STORE=sqlite) or PostgreSQL (STORE=postgres).
#
# The seeded .grain/ is byte-identical to HEAD's but for two extra schedules
# declared for release/v3.5.1 (§5 item 4): walk-release-v351.yml (firing an
# unbound walkthrough template of the same name) and
# walk-release-v351-template.yml (firing the release-train template). Declarations refuse `every` under 10 minutes,
# so the 20-minute cadence is left as committed and missed ticks are made by
# moving a schedule's next_run_at into the past (overdue below); the
# reconcile cycle itself runs every POLL_INTERVAL (1s).
# Dispatch is paused (POST /api/pipeline-pause) so filed tasks stay open and
# nothing but the schedules under test acts on them; schedules still fire.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4290}
GHSIM_PORT=${GHSIM_PORT:-4294}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_rt?sslmode=disable}

kill_pidfile() {
	local f=$1 pid
	[[ -f "$f" ]] || return 0
	pid=$(cat "$f")
	kill "$pid" 2>/dev/null || true
	for _ in $(seq 80); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.1
	done
	rm -f "$f"
}

stop_env() {
	local work=${1:-${WORK_DIR:-/var/tmp/walk-rt}}
	kill_pidfile "$work/daemon.pid"
	kill_pidfile "$work/ghsim.pid"
}

init_env() {
	WORK_DIR=${1:-/var/tmp/walk-rt}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/captures"

	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_rt WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_rt OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	git init -q --bare -b main "$WORK_DIR/grain.git"
	git -C "$WORK_DIR/grain.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	# This repository's own declarations, as committed at HEAD.
	git -C "$ROOT" archive HEAD .grain .github/workflows README.md | tar -x -C "$tmp"
	# A template with no base of its own, so the firing really goes into
	# release/v3.5.1.
	cat >"$tmp/.grain/templates/walk-release-v351.yml" <<EOF
name: Walkthrough release/v3.5.1 tick
title: Walkthrough tick on release/v3.5.1
body: Walkthrough-only template; close with nothing_needed.
EOF
	cat >"$tmp/.grain/schedules/walk-release-v351.yml" <<EOF
name: Walkthrough release/v3.5.1 tick
# Walkthrough-only (docs/test-plans/2026-11-15.md §5 item 4): a cadence
# held only while its own base branch's publishing is held.
template: walk-release-v351
base: release/v3.5.1
every:
  minutes: 20
skip-when: publishing-held
EOF
	# The release train's own template on the same branch: the template
	# binds base: main, which beats the schedule's base (placementOf), so
	# this firing goes into main and is held with it.
	cat >"$tmp/.grain/schedules/walk-release-v351-template.yml" <<EOF
name: Walkthrough release/v3.5.1 train (template-bound)
template: release-train
base: release/v3.5.1
every:
  minutes: 20
skip-when: publishing-held
EOF
	git -C "$tmp" add -A
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed acme/grain from this repository's .grain/"
	git -C "$tmp" push -q "$WORK_DIR/grain.git" main
	git -C "$tmp" push -q "$WORK_DIR/grain.git" main:release/v3.5.1
	git -C "$tmp" rev-parse HEAD >"$WORK_DIR/seed.sha"
	rm -rf "$tmp"

	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo '{"directConfigWrites": true}' >"$WORK_DIR/data/state-repo.json"
	cat >"$WORK_DIR/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK_DIR/bot.token
EOF
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$WORK_DIR/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	fi
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" -addr "$GHSIM" -owner acme -repo grain -bare "$work/grain.git" -default main >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/grain" >/dev/null && return 0
		sleep 0.1
	done
	echo "ghsim failed to start; see $work/ghsim.log" >&2
	return 1
}

start_daemon() {
	local work=${1:-$WORK_DIR}
	kill_pidfile "$work/daemon.pid"
	touch "$work/daemon.log"
	local store_flags=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		store_flags=(-postgres-url "$PG_DSN")
	fi
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$work/home" \
		GCE_METADATA_HOST="127.0.0.1:9" \
		GRAIN_RELEASE_NOT_PUBLISHING_AFTER="${NOT_PUBLISHING_AFTER:-3h}" \
		setsid "$GRAIN" daemon \
		-data-dir "$work/data" \
		-sandbox-dir "$work/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/grain \
		-default-target-repo acme/grain \
		-poll-interval "${POLL_INTERVAL:-1s}" \
		-max-workers 1 \
		-github-host "$GHSIM" \
		-github-insecure-http \
		"${store_flags[@]}" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"
	for _ in $(seq 150); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				export WORK_DIR="$work" CODE DAEMON GHSIM PG_DSN
				return 0
			fi
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; tail of $work/daemon.log:" >&2
			tail -n 40 "$work/daemon.log" >&2
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

# seed_runs posts the sim's whole GitHub Actions run list (a JSON array).
seed_runs() {
	curl -fsS -X POST -H "Content-Type: application/json" -d "{\"runs\": $1}" "http://$GHSIM/_sim/seed-actions"
}

store_q() {
	if [[ ${STORE:-sqlite} == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" -header -column "$WORK_DIR/data/store/grain.db" "$1"
	else
		PGPASSWORD=grain psql -h 127.0.0.1 -U grain -d grain_walk_rt -c "$1"
	fi
}

grain_cli() {
	env -i PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" HOME="$WORK_DIR/home" \
		GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" "$GRAIN" "$@"
}
