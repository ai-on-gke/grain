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

# Shared real-daemon environment for docs/test-plans/2026-11-19.md §3
# (QA S3 git merge-base resolution, QAEngineRev=7 rule severities,
# grain-qa-stage/<id> CI ref propagation, and direct-human S3 bypass).
# Runs on SQLite (STORE=sqlite) or PostgreSQL (STORE=postgres).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4282}
GHSIM_PORT=${GHSIM_PORT:-4286}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
HUB_ISSUER="https://hub.example.test"
HUB_AUDIENCE="https://grain.example.test"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_s3?sslmode=disable}
export PYTHONDONTWRITEBYTECODE=1

chmod +x "$HERE/fake-agy.py"

stop_daemon() {
	local work=${1:-${WORK_DIR:-/tmp/walk-qa-s3}}
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
	local work=${1:-${WORK_DIR:-/tmp/walk-qa-s3}}
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
	WORK_DIR=${1:-/tmp/walk-qa-s3}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs" "$WORK_DIR/captures"

	if [[ ${STORE:-sqlite} == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_s3 WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_s3 OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/cred_probe" "$HERE/cred_probe.go")

	# Primary repo: acme/widgets with root R0, fork-point C1 (base of task
	# branches), and 70 diverged commits on main (exceeding --depth=64!).
	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true
	git -C "$WORK_DIR/widgets.git" config uploadpack.allowFilter true

	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.grain/workflows" "$tmp/.github/workflows" "$tmp/pkg/base"
	cat >"$tmp/go.mod" <<'EOF'
module example.com/widgets

go 1.24
EOF
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
    runs-on: ubuntu-latest
    capabilities: []
    steps:
      - name: unit
        run: |
          echo "GRAIN_CI_PASS"
EOF
	cat >"$tmp/.github/workflows/ci.yml" <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo ok
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: initial root commit R0"
	ROOT_SHA=$(git -C "$tmp" rev-parse HEAD)

	# Commit C1: adds pkg/base with a valid Go test (so deleting it later triggers test-integrity-guard).
	cat >"$tmp/pkg/base/base.go" <<'EOF'
package base

func Value() int { return 1 }
EOF
	cat >"$tmp/pkg/base/base_test.go" <<'EOF'
package base

import "testing"

func TestValue(t *testing.T) {
	if got := Value(); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "feat: add pkg/base at fork point C1"
	FORK_SHA=$(git -C "$tmp" rev-parse HEAD)

	# Keep temporary base branches for the deleted-base tests.
	git -C "$tmp" branch temp-base "$FORK_SHA"
	git -C "$tmp" branch temp-base-salvage "$FORK_SHA"
	git -C "$tmp" branch fork-point "$FORK_SHA"

	# Advance main by 70 commits (>64 shallow horizon) so mirror fetch MUST deepen!
	mkdir -p "$tmp/docs/changelog"
	for i in $(seq 1 70); do
		printf "main-advance-%03d\n" "$i" >"$tmp/docs/changelog/entry-$(printf '%03d' "$i").txt"
		git -C "$tmp" add "$tmp/docs/changelog/entry-$(printf '%03d' "$i").txt"
		git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: main advance $i"
	done
	MAIN_SHA=$(git -C "$tmp" rev-parse HEAD)

	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main temp-base temp-base-salvage fork-point
	rm -rf "$tmp"

	# Secondary repo: acme/secondary for secondary task.Writes RBAC testing.
	git init -q --bare -b main "$WORK_DIR/secondary.git"
	git -C "$WORK_DIR/secondary.git" config http.receivepack true
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	echo "# acme/secondary" >"$tmp/README.md"
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed secondary"
	git -C "$tmp" push -q "$WORK_DIR/secondary.git" main
	rm -rf "$tmp"

	cat >"$WORK_DIR/shas.env" <<EOF
ROOT_SHA=$ROOT_SHA
FORK_SHA=$FORK_SHA
MAIN_SHA=$MAIN_SHA
EOF

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
	export WORK_DIR ROOT_SHA FORK_SHA MAIN_SHA
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" \
		-addr "$GHSIM" \
		-owner acme \
		-repo widgets \
		-bare "$work/widgets.git" \
		-secondary-repo secondary \
		-bare-secondary "$work/secondary.git" \
		-default main \
		-hub-issuer "$HUB_ISSUER" \
		-hub-audience "$HUB_AUDIENCE" \
		>"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/.well-known/jwks.json" >/dev/null && return 0
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
		store_flags=(-db-driver postgres -db-dsn "$PG_DSN")
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
		-deployment-owner alice@example.com \
		-ui-hub-issuer "$HUB_ISSUER" \
		-ui-hub-audience "$HUB_AUDIENCE" \
		-ui-hub-keys-url "http://${GHSIM}/.well-known/jwks.json" \
		-repo acme/widgets \
		-repo acme/secondary \
		-default-target-repo acme/widgets \
		-poll-interval "${POLL_INTERVAL:-400ms}" \
		-max-workers "${MAX_WORKERS:-1}" \
		-max-mergers 0 \
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
	local tmp code
	tmp=$(mktemp)
	if [[ -n "$body" ]]; then
		code=$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path")
	else
		code=$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path")
	fi
	if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
		echo "api $method $path failed ($code): $(cat "$tmp")" >&2
		rm -f "$tmp"
		return 1
	fi
	cat "$tmp"
	rm -f "$tmp"
}

mint_jwt() {
	local body=$1
	curl -fsS -X POST -H "Content-Type: application/json" -d "$body" "http://$GHSIM/_sim/mint-jwt" | jq -r '.token'
}
