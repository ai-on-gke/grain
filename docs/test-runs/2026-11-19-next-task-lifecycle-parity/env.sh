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

# Real-daemon environment for docs/test-plans/2026-11-19.md §5:
# - ghsim on 127.0.0.1:4294 (acme/widgets + fork alice/widgets + /_sim/refuse-merges)
# - iapsim issuer on 127.0.0.1:4292 and proxy on 127.0.0.1:4293
# - real `grain daemon` on 127.0.0.1:4291 (-sandbox-port-base=0, -host-sandboxes,
#   -max-workers=2, -max-mergers=1, SQLite or PostgreSQL)
# - real IAP principals:
#   - alice@example.com  -> grain/operator    (unlimited human operator)
#   - bob@example.com    -> grain/contributor (scoped operator: tasks:write, no approve/unlimited)
#   - carol@example.com  -> grain/viewer      (read-only viewer, no merge:write)
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
FAKE_AGY="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py"

DAEMON_PORT=${DAEMON_PORT:-4291}
ISSUER_PORT=${ISSUER_PORT:-4292}
PROXY_PORT=${PROXY_PORT:-4293}
GHSIM_PORT=${GHSIM_PORT:-4294}

DAEMON="http://127.0.0.1:${DAEMON_PORT}"
ISSUER="http://127.0.0.1:${ISSUER_PORT}"
PROXY="http://127.0.0.1:${PROXY_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_s5?sslmode=disable}

export PYTHONDONTWRITEBYTECODE=1
chmod +x "$FAKE_AGY"

stop_env() {
	local work=${1:-${WORK_DIR:-/tmp/walk-s5}}
	local p pid
	for p in daemon iapsim ghsim; do
		if [[ -f "$work/$p.pid" ]]; then
			pid=$(cat "$work/$p.pid")
			kill "$pid" 2>/dev/null || true
			for _ in $(seq 80); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.1
			done
			rm -f "$work/$p.pid"
		fi
	done
}

start_env() {
	STORE=${STORE:-sqlite}
	WORK_DIR=${1:-/tmp/walk-s5-${STORE}}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs" "$WORK_DIR/git/acme" "$WORK_DIR/git/alice"

	if [[ "$STORE" == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_s5 WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_s5 OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/iapsim" tests/iap/iapsim.go && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	# Seed acme/widgets.git
	git init -q --bare -b main "$WORK_DIR/git/acme/widgets.git"
	git -C "$WORK_DIR/git/acme/widgets.git" config http.receivepack true
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
        run: echo PASS
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed widgets"
	git -C "$tmp" push -q "$WORK_DIR/git/acme/widgets.git" main

	# Seed fork alice/widgets.git with branch fix-branch ahead of main
	git init -q --bare -b main "$WORK_DIR/git/alice/widgets.git"
	git -C "$WORK_DIR/git/alice/widgets.git" config http.receivepack true
	git -C "$tmp" push -q "$WORK_DIR/git/alice/widgets.git" main
	git -C "$tmp" checkout -q -b fix-branch
	echo "fork fix" >"$tmp/fork-fix.txt"
	git -C "$tmp" add fork-fix.txt
	git -C "$tmp" -c user.name="Alice" -c user.email="alice@example.com" commit -qm "feat: alice fork fix"
	git -C "$tmp" push -q "$WORK_DIR/git/alice/widgets.git" fix-branch
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

	if [[ "$STORE" == sqlite ]]; then
		"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$WORK_DIR/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
	fi

	# Start iapsim and ghsim
	"$WORK_DIR/bin/iapsim" -issuer "127.0.0.1:${ISSUER_PORT}" -proxy "127.0.0.1:${PROXY_PORT}" \
		-upstream "$DAEMON" -aud "$AUD" -iss "$ISS" >"$WORK_DIR/iapsim.log" 2>&1 &
	echo $! >"$WORK_DIR/iapsim.pid"

	"$WORK_DIR/bin/ghsim" -addr "$GHSIM" -git-root "$WORK_DIR/git" -default main >"$WORK_DIR/ghsim.log" 2>&1 &
	echo $! >"$WORK_DIR/ghsim.pid"

	for _ in $(seq 60); do
		curl -sf "$ISSUER/jwks" >/dev/null && curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && break
		sleep 0.1
	done

	local store_flags=()
	if [[ "$STORE" == postgres ]]; then
		store_flags=(-postgres-url "$PG_DSN")
	fi

	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$WORK_DIR/home" \
		WALK_DIR="$WORK_DIR" \
		PYTHONDONTWRITEBYTECODE=1 \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$GRAIN" daemon \
		-data-dir "$WORK_DIR/data" \
		-sandbox-dir "$WORK_DIR/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval 500ms \
		-max-workers 2 \
		-max-mergers 1 \
		-agent-framework antigravity \
		-agy-path "$FAKE_AGY" \
		-gemini-api-key-file "$WORK_DIR/gemini.key" \
		-github-host "$GHSIM" \
		-github-insecure-http \
		-ui-iap-audience "$AUD" \
		-ui-iap-issuer "$ISS" \
		-ui-iap-keys-url "$ISSUER/jwks" \
		"${store_flags[@]}" >>"$WORK_DIR/daemon.log" 2>&1 </dev/null &
	echo $! >"$WORK_DIR/daemon.pid"

	CODE=""
	for _ in $(seq 150); do
		if [[ -s "$WORK_DIR/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				break
			fi
		fi
		sleep 0.2
	done
	if [[ -z "$CODE" ]] || ! curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
		echo "daemon did not come up; tail of $WORK_DIR/daemon.log:" >&2
		tail -n 40 "$WORK_DIR/daemon.log" >&2
		return 1
	fi

	# Seed model connection now that the store is initialized.
	if [[ "$STORE" == sqlite ]]; then
		"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-models.yaml" >>"$WORK_DIR/connections.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$WORK_DIR/data" -db-driver postgres -db-dsn "$PG_DSN" import "$WORK_DIR/seed-models.yaml" >>"$WORK_DIR/connections.log" 2>&1
	fi

	# Add the three real IAP users: unlimited operator, scoped contributor, viewer.
	curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"entry":"user:alice@example.com","role":"grain/operator","reason":"walkthrough unlimited operator"}' \
		"$DAEMON/api/access/principals" >/dev/null
	curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"entry":"user:bob@example.com","role":"grain/contributor","reason":"walkthrough scoped operator"}' \
		"$DAEMON/api/access/principals" >/dev/null
	curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"entry":"user:carol@example.com","role":"grain/viewer","reason":"walkthrough read-only viewer"}' \
		"$DAEMON/api/access/principals" >/dev/null

	# Also configure a custom scoped operator WITH dispatch:write & tasks:write
	# (no tasks:admin / approve) so we can verify unapproved Run now is gated on approve.
	curl -sf -X PUT -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"set":{"name":"walk-dispatcher","description":"tasks:write + dispatch:write without approve","scopes":["tasks:read","tasks:write","dispatch:read","dispatch:write","ci:read","repos:read","settings:read","boards:read","capabilities:read","schedules:read","templates:read","suites:read","releases:read","memory:read","release-notes:read","sessions:read","caches:read","access:read","connections:read","secrets:read","deployment:read","sandboxes:read","qa:read"]}}' \
		"$DAEMON/api/api-scope-sets" >/dev/null
	curl -sf -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"entry":"user:dave@example.com","role":"walk-dispatcher","reason":"walkthrough dispatcher without approve"}' \
		"$DAEMON/api/access/principals" >/dev/null

	# Configure acme/widgets with ciMode=none & mergeRecheck=off so real PRs
	# proceed directly to the live merge queue during the Retry merge walkthrough.
	curl -sf -X PUT -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d '{"ciMode":"none","mergeRecheck":"off"}' \
		"$DAEMON/api/repos/acme/widgets/settings" >/dev/null

	export WORK_DIR CODE DAEMON PROXY ISSUER GHSIM STORE PG_DSN
	echo "READY STORE=$STORE WORK_DIR=$WORK_DIR DAEMON=$DAEMON PROXY=$PROXY GHSIM=http://$GHSIM"
}
