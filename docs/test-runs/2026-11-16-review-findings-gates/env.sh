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

# Shared real-daemon environment for docs/test-plans/2026-11-16.md §3
# (review findings, Accept findings, Release hold, targeted-review gate,
# declared credentials/pushes, and port grant filtering).
# Runs ghsim (with Hub JWKS signer) on 127.0.0.1:4294/4295 and a real
# `grain daemon -sandbox-port-base=0` on 127.0.0.1:4290/4291 over SQLite
# (STORE=sqlite) or PostgreSQL (STORE=postgres).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
STORE=${STORE:-sqlite}
if [[ "$STORE" == postgres ]]; then
	DAEMON_PORT=${DAEMON_PORT:-4291}
	GHSIM_PORT=${GHSIM_PORT:-4295}
else
	DAEMON_PORT=${DAEMON_PORT:-4290}
	GHSIM_PORT=${GHSIM_PORT:-4294}
fi
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
HUB_AUD="https://walk.example.com"
PG_DB=${PG_DB:-grain_review_gates}
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/${PG_DB}?sslmode=disable}
export PYTHONDONTWRITEBYTECODE=1

chmod +x "$HERE/fake-agy.py"

stop_daemon() {
	local work=${1:-${WORK_DIR:-/tmp/walk-review-gates-${STORE}}}
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
	local work=${1:-${WORK_DIR:-/tmp/walk-review-gates-${STORE}}}
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
	WORK_DIR=${1:-/tmp/walk-review-gates-${STORE}}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/runs"

	if [[ "$STORE" == postgres ]]; then
		sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS ${PG_DB} WITH (FORCE);"
		sudo -u postgres psql -q -c "CREATE DATABASE ${PG_DB} OWNER grain;"
	fi

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")

	git init -q --bare -b main "$WORK_DIR/widgets.git"
	git -C "$WORK_DIR/widgets.git" config http.receivepack true

	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.grain/workflows" "$tmp/.grain/triggers" "$tmp/.grain/templates" \
		"$tmp/.grain/schedules" "$tmp/pkg/ci" "$tmp/payments" "$tmp/auth"

	cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
	cat >"$tmp/notes.txt" <<'EOF'
line1: shared
line2: main-base
EOF
	echo "package ci" >"$tmp/pkg/ci/ci.go"
	echo "package payments" >"$tmp/payments/charge.go"
	echo "package auth" >"$tmp/auth/token.go"

	cat >"$tmp/.grain/settings.yml" <<'YAML'
port-branches: [main, candidate]
YAML

	cat >"$tmp/.grain/workflows/ci.yml" <<'YAML'
name: ci
on:
  push:
  pull_request:
jobs:
  unit:
    steps:
      - name: unit
        run: echo "PASS"
YAML

	cat >"$tmp/.grain/triggers/every-pr.yml" <<'YAML'
on: pull_request
paths: ["**"]
template: pr-review
gate: true
human: on-findings
auto-approve: true
YAML

	cat >"$tmp/.grain/triggers/payments.yml" <<'YAML'
on: pull_request
paths: ["payments/**"]
gate: true
human: on-findings
sensitive: true
auto-approve: true
YAML

	cat >"$tmp/.grain/triggers/auth.yml" <<'YAML'
on: pull_request
paths: ["auth/**"]
gate: true
human: on-findings
sensitive: true
auto-approve: true
YAML

	cat >"$tmp/.grain/templates/pr-review.yml" <<'YAML'
name: PR review
title: "Code review: {{task_title}} ({{pr}})"
body: |
  WALK-CODE-REVIEW: review the diff and leave findings.
YAML

	cat >"$tmp/.grain/templates/release-note.yml" <<'YAML'
name: release-note
title: "Release note: {{title}}"
body: |
  WALK-RELEASE-NOTE: update notes.txt and open a pull request.
fields:
  - name: title
    label: Title
capabilities:
  - "https://grain.dev/capabilities/github#read-issues={}"
  - "https://grain.dev/capabilities/api#ci-read"
YAML

	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed acme/widgets"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" main

	# Create paired candidate branch with a conflicting edit on notes.txt so
	# mechanical cherry-pick conflicts and grain files an agent port task.
	git -C "$tmp" checkout -qb candidate
	cat >"$tmp/notes.txt" <<'EOF'
line1: shared
line2: candidate-divergent
EOF
	git -C "$tmp" add notes.txt
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: diverge candidate notes.txt"
	git -C "$tmp" push -q "$WORK_DIR/widgets.git" candidate
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
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" -addr "$GHSIM" -bare "$work/widgets.git" -default main -audience "$HUB_AUD" >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && \
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
	if [[ "$STORE" == postgres ]]; then
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
		-deployment-owner alice@example.com \
		-as alice@example.com \
		-ui-hub-issuer grain-registry \
		-ui-hub-audience "$HUB_AUD" \
		-ui-hub-keys-url "http://${GHSIM}/.well-known/jwks.json" \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval "${POLL_INTERVAL:-500ms}" \
		-max-workers "${MAX_WORKERS:-2}" \
		-max-mergers 0 \
		-max-ci-jobs 2 \
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
					if [[ "$STORE" == sqlite ]]; then
						"$GRAIN" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$WORK_DIR/connections.log" 2>&1
					else
						"$GRAIN" connections -data-dir "$work/data" -db-driver postgres -db-dsn "$PG_DSN" import "$work/seed-models.yaml" >>"$WORK_DIR/connections.log" 2>&1
					fi
					touch "$work/.models-seeded"
					sleep 0.6
				fi
				ALICE_JWT=$(curl -fsS "http://$GHSIM/_sim/jwt?email=alice@example.com")
				BOB_JWT=$(curl -fsS "http://$GHSIM/_sim/jwt?email=bob@example.com")
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM PG_DSN ALICE_JWT BOB_JWT STORE
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
		curl -fsS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -fsS -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}

api_as() {
	local jwt=$1 method=$2 path=$3 body=${4:-}
	if [[ -n "$body" ]]; then
		curl -sS -w '\n%{http_code}' -X "$method" -H "X-Grain-Hub-Assertion: $jwt" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -sS -w '\n%{http_code}' -X "$method" -H "X-Grain-Hub-Assertion: $jwt" "$DAEMON$path"
	fi
}

store_exec() {
	local sql=$1
	if [[ "$STORE" == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -q -h 127.0.0.1 -U grain -d "$PG_DB" -c "$sql"
	fi
}

store_query() {
	local sql=$1
	if [[ "$STORE" == sqlite ]]; then
		sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -tA -h 127.0.0.1 -U grain -d "$PG_DB" -c "$sql"
	fi
}

setup_principals_and_repo() {
	# Create repo-scoped writer role (holds tasks:write + merge:force on acme/widgets, NOT full access)
	api PUT /api/api-scope-sets '{"set":{"name":"widgets/writer","description":"Repo-scoped writer on acme/widgets","scopes":["tasks:read","tasks:write","ci:read","ci:write","repos:read","merge:force"],"repos":["acme/widgets"]}}' >/dev/null
	api POST /api/access/principals '{"entry":"user:bob@example.com","role":"widgets/writer","reason":"walkthrough repo-scoped writer"}' >/dev/null
	# Enable declarations and grain CI mode on acme/widgets
	api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
	api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >/dev/null
}

wait_declarations_synced() {
	local want_commit=${1:-}
	for _ in $(seq 80); do
		local st
		st=$(api GET /api/repos/acme/widgets/settings)
		local commit err
		commit=$(python3 -c 'import json,sys; d=json.load(sys.stdin).get("declarationsStatus") or {}; print(d.get("commit",""))' <<<"$st")
		err=$(python3 -c 'import json,sys; d=json.load(sys.stdin).get("declarationsStatus") or {}; print(d.get("lastError",""))' <<<"$st")
		if [[ -n "$commit" ]]; then
			if [[ -z "$want_commit" || "$commit" == "$want_commit" ]]; then
				if [[ -n "$err" ]]; then
					echo "declaration sync recorded error: $err" >&2
				fi
				return 0
			fi
		fi
		sleep 0.25
	done
	echo "timed out waiting for declarations sync (want=$want_commit)" >&2
	api GET /api/repos/acme/widgets/settings >&2
	return 1
}

force_declaration_resync() {
	# declarationPollInterval is 2m; reset checked_at in the store so the next 500ms tick polls immediately.
	store_exec "UPDATE repo_declaration_sync SET checked_at = '2020-01-01T00:00:00Z' WHERE owner = 'acme' AND name = 'widgets';"
}
