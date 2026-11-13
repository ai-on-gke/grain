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

# Shared helpers for docs/test-plans/2026-11-13.md §3 (live events and task
# detail). Source it; then:
#
#   start_daemon <sqlite|postgres> <work-dir> <port> [grain-binary] [flags...]
#   api <METHOD> <path> [json]       # as the daemon's own local API token
#   stop_daemon
#
# The daemon is a real `grain daemon -sandbox-port-base=0` with
# -host-sandboxes and the fake antigravity CLI next to this file (no model
# credentials exist in the sandbox). PostgreSQL is the local cluster
# (role debian/walk); each work dir gets its own database.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
PGURL=${PGURL:-postgres://debian:walk@127.0.0.1:5432}

pg_db() { echo "walk_$(basename "$1" | tr -c 'a-z0-9\n' '_')"; }

reset_store() { # reset_store <kind> <work-dir>
	local kind=$1 work=$2
	rm -rf "$work"
	mkdir -p "$work/data" "$work/sbx" "$work/prompts"
	if [[ $kind == postgres ]]; then
		psql -X -q "$PGURL/postgres?sslmode=disable" -c "DROP DATABASE IF EXISTS $(pg_db "$work")" -c "CREATE DATABASE $(pg_db "$work")"
	fi
}

store_flags() { # store_flags <kind> <work-dir>
	[[ $1 == postgres ]] && echo "-postgres-url $PGURL/$(pg_db "$2")?sslmode=disable" || true
}

sql() { # sql <kind> <work-dir> <statement>: run SQL against the store
	if [[ $1 == postgres ]]; then
		psql -X -q -At -v ON_ERROR_STOP=1 "$PGURL/$(pg_db "$2")?sslmode=disable" -c "$3"
	else
		sqlite3 -bail "$2/data/store/grain.db" "$3"
	fi
}

# start_ghsim <work-dir> <port> [kind]: the GitHub boundary. acme/widgets is a bare
# repository served by 2026-11-11-durable-tasks/ghsim.go (reused, not
# copied); the daemon gets a fake bot token for it.
start_ghsim() {
	local work=$1 port=$2 tmp
	GHSIM="127.0.0.1:$port"
	mkdir -p "$work/bin"
	(cd "$HERE/../../.." && go build -o "$work/bin/ghsim" docs/test-runs/2026-11-11-durable-tasks/ghsim.go)
	git init -q --bare -b main "$work/widgets.git"
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	echo "# acme/widgets" >"$tmp/README.md"
	git -C "$tmp" add . && git -C "$tmp" -c user.name=Walk -c user.email=seed@example.com commit -qm "chore: seed"
	git -C "$tmp" push -q "$work/widgets.git" main
	rm -rf "$tmp"
	"$work/bin/ghsim" -addr "$GHSIM" -bare "$work/widgets.git" -default main >"$work/ghsim.log" 2>&1 </dev/null &
	echo $! >"$work/ghsim.pid"
	echo ghp_walkthrough >"$work/bot.token"
	printf 'git:\n  - credential: bot\n    token_file: %s\n' "$work/bot.token" >"$work/seed-git.yaml"
	if [[ ${3:-sqlite} == postgres ]]; then
		GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN="$PGURL/$(pg_db "$work")?sslmode=disable" \
			"$GRAIN" connections -data-dir "$work/data" import "$work/seed-git.yaml" >"$work/connections.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$work/data" import "$work/seed-git.yaml" >"$work/connections.log" 2>&1
	fi
	for _ in $(seq 50); do curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && return 0; sleep 0.1; done
	return 1
}

stop_ghsim() { kill "$(cat "$1/ghsim.pid" 2>/dev/null)" 2>/dev/null || true; }

start_daemon() { # start_daemon <kind> <work-dir> <port> [binary] [flags...]
	KIND=$1 WORK=$2 PORT=$3
	local bin=${4:-$GRAIN}
	shift 4 2>/dev/null || shift $#
	DAEMON="http://127.0.0.1:$PORT"
	# shellcheck disable=SC2046
	WALK_DIR="$WORK" setsid "$bin" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" \
		-host-sandboxes -sandbox-dir "$WORK/sbx" -sandbox-port-base=0 -poll-interval 1s \
		-as alice@example.com -max-mergers 0 \
		-agent-framework antigravity -agy-path "$HERE/fake-agy.sh" \
		${GHSIM:+-github-host "$GHSIM" -github-insecure-http -repo acme/widgets} \
		$(store_flags "$KIND" "$WORK") "$@" >>"$WORK/daemon.log" 2>&1 </dev/null &
	echo $! >"$WORK/daemon.pid"
	for _ in $(seq 120); do
		kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { echo "daemon exited; see $WORK/daemon.log" >&2; return 1; }
		[[ -s $WORK/data/local-api-token ]] && curl -fs -o /dev/null \
			-H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "$DAEMON/api/config" && return 0
		sleep 0.5
	done
	echo "daemon did not come up; see $WORK/daemon.log" >&2
	return 1
}

stop_daemon() {
	local pid
	pid=$(cat "$WORK/daemon.pid" 2>/dev/null) || return 0
	kill "$pid" 2>/dev/null || true
	for _ in $(seq 100); do kill -0 "$pid" 2>/dev/null || return 0; sleep 0.1; done
	kill -9 "$pid" 2>/dev/null || true
}

token() { cat "$WORK/data/local-api-token"; }

api() { # api <METHOD> <path> [json]: prints the body
	curl -sS -X "$1" -H "Authorization: Bearer $(token)" -H 'Content-Type: application/json' \
		${3:+--data "$3"} "$DAEMON$2"
}

setup_agent() { # one antigravity connection run by fake-agy.sh
	api PUT /api/config '{"maxWorkers":2,"defaultModel":"fake-model","agentBackends":[
	  {"id":"agy-walk","name":"Walk agy","framework":"antigravity","cliPath":"'"$HERE/fake-agy.sh"'","models":["fake-model"]}]}' >/dev/null
	api PUT /api/agent-backends/agy-walk/credential '{"value":"walk-key-not-real"}' >/dev/null
}
