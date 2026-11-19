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

# End-to-end walkthrough runner for docs/test-plans/2026-11-19.md §8:
#   Part 1: Command-backed forge cut-body handling, signed-URL redaction,
#           and 304/204/HEAD draining (direct + via live grain hostd / host-git).
#   Part 2: GitHub Actions run-log zip fallback (grain ci log in a real host
#           sandbox, API, and /next/ci in Playwright).
#   Part 3: Pipeline steward cold-start wake on pending pipeline notifications
#           vs SubjectStewardRegression exclusion.
# Exercises real `grain daemon -sandbox-port-base=0` over SQLite and PostgreSQL.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
FAKE_AGY="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py"
WORK=${WORK:-/tmp/walk-s8}
DAEMON_PORT=${DAEMON_PORT:-4280}
MOCK_PORT=${MOCK_PORT:-4284}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
MOCK="http://127.0.0.1:${MOCK_PORT}"
PG_DSN=${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk_s8?sslmode=disable}
export PYTHONDONTWRITEBYTECODE=1
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}

mkdir -p "$WORK/bin" "$WORK/mockbin" "$WORK/results" "$WORK/screenshots"
cp "$HERE/mock_forgecli.py" "$WORK/mockbin/forgecli"
chmod +x "$WORK/mockbin/forgecli" "$FAKE_AGY"

# Build helper binaries if missing or stale.
if [[ ! -x "$WORK/bin/grain-forgecli" || "$HERE/grain_forgecli.go" -nt "$WORK/bin/grain-forgecli" ]]; then
	(cd "$ROOT" && go build -o "$WORK/bin/grain-forgecli" "$HERE/grain_forgecli.go")
fi
if [[ ! -x "$WORK/bin/probe-commandforge" || "$HERE/probe_commandforge.go" -nt "$WORK/bin/probe-commandforge" ]]; then
	(cd "$ROOT" && go build -o "$WORK/bin/probe-commandforge" "$HERE/probe_commandforge.go")
fi
if [[ ! -x "$WORK/bin/mock-actions" || "$HERE/mock_actions.go" -nt "$WORK/bin/mock-actions" ]]; then
	(cd "$ROOT" && go build -o "$WORK/bin/mock-actions" "$HERE/mock_actions.go")
fi

PIDS=()
cleanup() {
	local pid
	for pid in "${PIDS[@]:-}"; do
		[[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
	done
	sleep 0.3
	for pid in "${PIDS[@]:-}"; do
		[[ -n "$pid" ]] && kill -9 "$pid" 2>/dev/null || true
	done
	PIDS=()
}
trap cleanup EXIT

stop_bg() {
	local pidfile=$1
	if [[ -f "$pidfile" ]]; then
		local pid
		pid=$(cat "$pidfile")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 50); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		kill -9 "$pid" 2>/dev/null || true
		rm -f "$pidfile"
	fi
}

reset_pg() {
	sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk_s8 WITH (FORCE);"
	sudo -u postgres psql -q -c "CREATE DATABASE grain_walk_s8 OWNER grain;"
}

seed_bare_repo() {
	local bare=$1
	rm -rf "$bare"
	git init -q --bare -b main "$bare"
	git -C "$bare" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.github/workflows"
	cat >"$tmp/README.md" <<'EOF'
# acme/widgets
EOF
	cat >"$tmp/.github/workflows/ci.yml" <<'EOF'
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo ok
EOF
	git -C "$tmp" add .
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed"
	git -C "$tmp" push -q "$bare" main
	rm -rf "$tmp"
}

wait_daemon() {
	local dir=$1
	for _ in $(seq 120); do
		if [[ -s "$dir/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$dir/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				return 0
			fi
		fi
		if [[ -f "$dir/daemon.pid" ]] && ! kill -0 "$(cat "$dir/daemon.pid")" 2>/dev/null; then
			echo "daemon exited early; log tail:" >&2
			tail -n 50 "$dir/daemon.log" >&2
			return 1
		fi
		sleep 0.2
	done
	echo "daemon failed to become ready" >&2
	tail -n 50 "$dir/daemon.log" >&2
	return 1
}

api_code_body() {
	local method=$1 path=$2 body=${3:-}
	local tmp
	tmp=$(mktemp)
	local status
	if [[ -n "$body" ]]; then
		status=$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
			-H "Authorization: Bearer $CODE" \
			-H "Content-Type: application/json" \
			-d "$body" "$DAEMON$path")
	else
		status=$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
			-H "Authorization: Bearer $CODE" "$DAEMON$path")
	fi
	printf '%s\n' "$status"
	cat "$tmp"
	rm -f "$tmp"
}

store_sql() {
	local store=$1 dir=$2 sql=$3
	if [[ "$store" == "sqlite" ]]; then
		sqlite3 -cmd ".timeout 5000" "$dir/data/store/grain.db" "$sql"
	else
		PGPASSWORD=grain psql -q -t -A -h 127.0.0.1 -U grain -d grain_walk_s8 -c "$sql"
	fi
}

# ============================================================================
# PART 1: Command-backed forge cut-body, signed-URL redaction, 304/204/HEAD
# ============================================================================
run_part1() {
	local store=$1
	local relay=$2 # "direct" or "hostd"
	echo "================================================================"
	echo "PART 1: Command-backed forge (store=$store, relay=$relay)"
	echo "================================================================"
	local dir="$WORK/p1-${store}-${relay}"
	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/hostd.pid"
	rm -rf "$dir"
	mkdir -p "$dir/data" "$dir/sandboxes" "$dir/home" "$dir/state" "$dir/hostd-state"
	[[ "$store" == "postgres" ]] && reset_pg

	local hostd_flags=()
	local hostd_sock=""
	if [[ "$relay" == "hostd" ]]; then
		# Create root-owned allowlist for grain hostd offering host-git via grain-forgecli.
		local allow="/tmp/walk-s8-hostd-${store}.yaml"
		cat >"$dir/hostd.yaml" <<EOF
capabilities:
  host-git:
    command: $WORK/bin/grain-forgecli
    args: [capability, host-git]
    set_env:
      PATH: "$WORK/mockbin:/home/debian/bin:/usr/local/bin:/usr/bin:/bin"
      FORGECLI_STATE: "$dir/state"
EOF
		sudo cp "$dir/hostd.yaml" "$allow"
		sudo chown root:root "$allow"
		sudo chmod 0644 "$allow"
		hostd_sock="/tmp/walk-s8-hostd-${store}.sock"
		rm -f "$hostd_sock"
		env -i \
			PATH="$WORK/mockbin:/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
			HOME="$dir/home" \
			FORGECLI_STATE="$dir/state" \
			"$WORK/bin/grain-forgecli" hostd \
			-allowlist "$allow" \
			-socket "$hostd_sock" \
			-state-dir "$dir/hostd-state" >"$dir/hostd.log" 2>&1 </dev/null &
		echo $! >"$dir/hostd.pid"
		PIDS+=($!)
		for _ in $(seq 50); do
			[[ -S "$hostd_sock" ]] && break
			sleep 0.1
		done
		if [[ ! -S "$hostd_sock" ]]; then
			echo "hostd failed to start:" >&2
			cat "$dir/hostd.log" >&2
			return 1
		fi
		hostd_flags=(-hostd-socket "$hostd_sock")
	fi

	# 1a. Run probe-commandforge (304, 204, HEAD, text/html cut, signed-URL cut, pre-header stderr).
	local probe_out="$WORK/results/p1-probe-${store}-${relay}.json"
	if [[ "$relay" == "hostd" ]]; then
		env -i \
			PATH="$WORK/mockbin:/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
			HOME="$dir/home" \
			FORGECLI_STATE="$dir/state" \
			"$WORK/bin/probe-commandforge" -state-dir "$dir/state" -hostd-socket "$hostd_sock" >"$probe_out"
	else
		env -i \
			PATH="$WORK/mockbin:/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
			HOME="$dir/home" \
			FORGECLI_STATE="$dir/state" \
			"$WORK/bin/probe-commandforge" -state-dir "$dir/state" >"$probe_out"
	fi
	echo "--- probe-commandforge ($store / $relay) ---"
	jq '{viaHostd, forge_api_timeout_seconds, "304": ."304_not_modified", "204": ."204_no_content", head: .head_drain, cut_html, cut_signed_url, preheader_signed_url, logs_leak_any_secret, logs_contain_context_canceled}' "$probe_out"

	# 1b. Start real grain-forgecli daemon and verify via GET /api/ci/runs/.../log.
	echo '{"directConfigWrites": true}' >"$dir/data/state-repo.json"
	local store_flags=()
	[[ "$store" == "postgres" ]] && store_flags=(-postgres-url "$PG_DSN")

	env -i \
		PATH="$WORK/mockbin:/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$dir/home" \
		FORGECLI_STATE="$dir/state" \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$WORK/bin/grain-forgecli" daemon \
		-data-dir "$dir/data" \
		-sandbox-dir "$dir/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo depot.code.forge.test/acme/widgets \
		-default-target-repo depot.code.forge.test/acme/widgets \
		-poll-interval 2s \
		-max-workers 1 \
		-max-mergers 0 \
		"${store_flags[@]}" \
		"${hostd_flags[@]}" >"$dir/daemon.log" 2>&1 </dev/null &
	echo $! >"$dir/daemon.pid"
	PIDS+=($!)
	wait_daemon "$dir"

	# Register ambient forgecli git connection for depot.code.forge.test and set CI mode to github.
	api_code_body POST "/api/deployment/git-connections" \
		'{"host":"depot.code.forge.test","kind":"github","type":"ambient","source":"forgecli"}' >"$dir/conn.out"
	echo "Add ambient connection: HTTP $(head -n1 "$dir/conn.out")"
	api_code_body PUT "/api/repos/depot.code.forge.test%2Facme/widgets/ci-mode" \
		'{"ciMode":"github"}' >"$dir/cimode.out" || true
	# Also ensure repo_config has ci_mode=github via deployment repos API if URL encoding differs.
	if [[ "$(head -n1 "$dir/cimode.out")" != "200" ]]; then
		api_code_body PUT "/api/deployment/repos/depot.code.forge.test/acme/widgets/config" \
			'{"ciMode":"github"}' >"$dir/cimode2.out" || true
		echo "Set ciMode fallback: HTTP $(head -n1 "$dir/cimode2.out")"
	fi

	# Case 1A: Cut-body 200 with signed redirect URL on stderr (run 101 so fresh cache).
	echo '{"job_log":"cut_signed_url","run_zip":"cut_signed_url"}' >"$dir/state/mode.json"
	api_code_body GET "/api/ci/runs/gha:depot.code.forge.test:acme:widgets:101/log?job=build" >"$dir/case1a.out"
	local c1a_code c1a_body
	c1a_code=$(head -n1 "$dir/case1a.out")
	c1a_body=$(tail -n +2 "$dir/case1a.out")
	echo "Case 1A (cut-body + signed URL): HTTP $c1a_code | body=$c1a_body"
	if grep -q "SUPER_SECRET_SIG" "$dir/case1a.out" "$dir/daemon.log" "$dir/hostd.log" 2>/dev/null; then
		echo "FAIL: SUPER_SECRET_SIG leaked!" >&2
	else
		echo "OK: SUPER_SECRET_SIG absent from API response and daemon/hostd logs"
	fi
	if grep -q 'pipelines.example.com/logs/42?\[redacted\]' "$dir/case1a.out"; then
		echo "OK: API response contains redacted URL"
	fi
	if grep -q 'pipelines.example.com/logs/42?\[redacted\]' "$dir/daemon.log" "$dir/hostd.log" 2>/dev/null; then
		echo "OK: daemon/hostd logs contain redacted URL"
	fi

	# Case 1A-bis: Pre-header failure with signed redirect URL on stderr (run 102).
	echo '{"job_log":"preheader_signed_url","run_zip":"not_found"}' >"$dir/state/mode.json"
	api_code_body GET "/api/ci/runs/gha:depot.code.forge.test:acme:widgets:102/log?job=build" >"$dir/case1abis.out"
	echo "Case 1A-bis (pre-header stderr + signed URL): HTTP $(head -n1 "$dir/case1abis.out") | body=$(tail -n +2 "$dir/case1abis.out")"
	if grep -q "PREHEADER_SECRET_SIG" "$dir/case1abis.out"; then
		echo "DEFECT DETECTED: PREHEADER_SECRET_SIG leaked into API response on pre-header forge CLI failure!"
	else
		echo "OK: PREHEADER_SECRET_SIG redacted on pre-header failure"
	fi

	# Case 1B: Cut-body 200 text/html partial non-login HTML (run 103).
	echo '{"job_log":"cut_html","run_zip":"not_found"}' >"$dir/state/mode.json"
	api_code_body GET "/api/ci/runs/gha:depot.code.forge.test:acme:widgets:103/log?job=build" >"$dir/case1b.out"
	echo "Case 1B (cut text/html non-login): HTTP $(head -n1 "$dir/case1b.out") | body=$(tail -n +2 "$dir/case1b.out")"

	# Case 1D: Cut-body triggers run-log zip fallback through command-backed forge (run 104).
	echo '{"job_log":"cut_signed_url","run_zip":"zip_ok"}' >"$dir/state/mode.json"
	api_code_body GET "/api/ci/runs/gha:depot.code.forge.test:acme:widgets:104/log?job=test%2Funit" >"$dir/case1d.out"
	echo "Case 1D (cut-body -> zip fallback on test/unit): HTTP $(head -n1 "$dir/case1d.out") | body=$(tail -n +2 "$dir/case1d.out")"

	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/hostd.pid"
	sudo rm -f "/tmp/walk-s8-hostd-${store}.yaml" "/tmp/walk-s8-hostd-${store}.sock" 2>/dev/null || true
}

# ============================================================================
# PART 2: GitHub Actions run-log zip fallback (grain ci log, API, /next/ci)
# ============================================================================
run_part2() {
	local store=$1
	echo "================================================================"
	echo "PART 2: GitHub Actions run-log zip fallback (store=$store)"
	echo "================================================================"
	local dir="$WORK/p2-${store}"
	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/mock.pid"
	rm -rf "$dir"
	mkdir -p "$dir/data" "$dir/sandboxes" "$dir/home" "$dir/runs"
	[[ "$store" == "postgres" ]] && reset_pg

	seed_bare_repo "$dir/widgets.git"
	"$WORK/bin/mock-actions" -addr "127.0.0.1:${MOCK_PORT}" -bare "$dir/widgets.git" >"$dir/mock.log" 2>&1 </dev/null &
	echo $! >"$dir/mock.pid"
	PIDS+=($!)
	for _ in $(seq 50); do
		curl -sf "$MOCK/_mock/counts" >/dev/null && break
		sleep 0.1
	done

	echo "ghp_walkthrough" >"$dir/bot.token"
	echo "walkthrough-gemini-key" >"$dir/gemini.key"
	echo '{"directConfigWrites": true}' >"$dir/data/state-repo.json"
	cat >"$dir/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $dir/bot.token
EOF
	cat >"$dir/seed-models.yaml" <<EOF
git:
  - credential: bot
    token_file: $dir/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: $dir/gemini.key
default_model: gemini-3.1-pro
EOF
	if [[ "$store" == "sqlite" ]]; then
		"$GRAIN" connections -data-dir "$dir/data" import "$dir/seed-git.yaml" >"$dir/conn.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$dir/data" -db-driver postgres -db-dsn "$PG_DSN" import "$dir/seed-git.yaml" >"$dir/conn.log" 2>&1
	fi

	local store_flags=()
	[[ "$store" == "postgres" ]] && store_flags=(-postgres-url "$PG_DSN")

	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$dir/home" \
		WALK_DIR="$dir" \
		PYTHONDONTWRITEBYTECODE=1 \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$GRAIN" daemon \
		-data-dir "$dir/data" \
		-sandbox-dir "$dir/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval 500ms \
		-max-workers 1 \
		-max-mergers 0 \
		-agent-framework antigravity \
		-agy-path "$FAKE_AGY" \
		-gemini-api-key-file "$dir/gemini.key" \
		-github-host "127.0.0.1:${MOCK_PORT}" \
		-github-insecure-http \
		"${store_flags[@]}" >"$dir/daemon.log" 2>&1 </dev/null &
	echo $! >"$dir/daemon.pid"
	PIDS+=($!)
	wait_daemon "$dir"

	if [[ "$store" == "sqlite" ]]; then
		"$GRAIN" connections -data-dir "$dir/data" import "$dir/seed-models.yaml" >>"$dir/conn.log" 2>&1
	else
		"$GRAIN" connections -data-dir "$dir/data" -db-driver postgres -db-dsn "$PG_DSN" import "$dir/seed-models.yaml" >>"$dir/conn.log" 2>&1
	fi
	sleep 0.6

	# Set acme/widgets CI mode to github.
	api_code_body PUT "/api/repos/acme/widgets/ci-mode" '{"ciMode":"github"}' >"$dir/cimode.out"
	echo "Set acme/widgets ciMode=github: HTTP $(head -n1 "$dir/cimode.out")"

	# 2A. Exercise grain ci log from a real host-sandbox task with api:ci-read@acme/widgets!
	curl -sf -X POST "$MOCK/_mock/reset" >/dev/null
	cat >"$dir/action-default.json" <<'EOF'
{
  "kind": "mcp_sequence",
  "calls": [
    {"tool": "run_command", "args": {"command": "grain ci log gha:acme:widgets:42 --job build", "description": "reading build job log"}},
    {"tool": "run_command", "args": {"command": "grain ci log gha:acme:widgets:42 --job test/unit", "description": "reading test/unit job log"}},
    {"tool": "run_command", "args": {"command": "grain ci log gha:acme:widgets:42 --job lint", "description": "reading lint job log"}},
    {"tool": "comment_on_issue", "args": {"comment": "Fetched all 3 fallback job logs via grain ci log"}}
  ]
}
EOF
	api_code_body POST "/api/tasks" '{
		"title": "Read fallback Actions logs in sandbox",
		"description": "Exercise grain ci log across all 3 jobs of run 42",
		"repo": "acme/widgets",
		"approved": true,
		"grants": ["https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]
	}' >"$dir/task-create.out"
	local tid
	tid=$(tail -n +2 "$dir/task-create.out" | jq -r '.id // .task.id // empty')
	echo "Created sandbox task #$tid (HTTP $(head -n1 "$dir/task-create.out"))"
	if [[ -n "$tid" ]]; then
		for _ in $(seq 120); do
			local st
			st=$(api_code_body GET "/api/tasks/$tid" | tail -n +2 | jq -r '.state // .task.state // empty')
			if [[ "$st" == "awaiting_reply" || "$st" == "closed" || "$st" == "completed" || "$st" == "failed" ]]; then
				echo "Sandbox task #$tid reached state: $st"
				break
			fi
			sleep 0.5
		done
		if [[ -f "$dir/runs/mcp-${tid}.jsonl" ]]; then
			echo "--- Sandbox grain ci log outputs ($store) ---"
			jq . "$dir/runs/mcp-${tid}.jsonl" | tee "$WORK/results/p2-sandbox-${store}.json"
		fi
	fi
	echo "--- Mock counts after sandbox grain ci log (Run 42) ---"
	curl -sf "$MOCK/_mock/counts" | jq . | tee "$WORK/results/p2-counts-after-sandbox-${store}.json"

	# 2B: In-progress job (Run 43) must NOT fetch run zip.
	curl -sf -X POST "$MOCK/_mock/reset" >/dev/null
	api_code_body GET "/api/ci/runs/gha:acme:widgets:43/log?job=build" >"$dir/case2b.out"
	echo "Case 2B (in_progress job, run 43): HTTP $(head -n1 "$dir/case2b.out") | body=$(tail -n +2 "$dir/case2b.out")"
	echo "Counts after run 43:"
	curl -sf "$MOCK/_mock/counts" | jq .

	# 2C: 64 MiB shared step-file cap (Run 44: 20 x 5 MiB = 100 MiB uncompressed).
	curl -sf -X POST "$MOCK/_mock/reset" >/dev/null
	curl -sf -X POST "$MOCK/_mock/config?run44TrailingNL=0" >/dev/null
	api_code_body GET "/api/ci/runs/gha:acme:widgets:44/log?job=heavy" >"$dir/case2c.out"
	echo "Case 2C (100 MiB zip bomb, no trailing NL): HTTP $(head -n1 "$dir/case2c.out") | metadata=$(tail -n +2 "$dir/case2c.out" | jq '{run, job, jobId, bytes, truncated, logLen: (.log | length)}')"

	# 2D: Dual-failure error & negative TTL (Run 45).
	curl -sf -X POST "$MOCK/_mock/reset" >/dev/null
	curl -sf -X POST "$MOCK/_mock/config?run45Mode=empty_and_404" >/dev/null
	api_code_body GET "/api/ci/runs/gha:acme:widgets:45/log?job=build" >"$dir/case2d-1.out"
	local c1_hits
	c1_hits=$(curl -sf "$MOCK/_mock/counts" | jq '."/repos/acme/widgets/actions/runs/45/logs" // 0')
	echo "Case 2D-1 (dual failure empty+404, build): HTTP $(head -n1 "$dir/case2d-1.out") | run45ZipHits=$c1_hits | body=$(tail -n +2 "$dir/case2d-1.out")"

	# Rapid second read on lint -> must use negative cache (hit count stays 1).
	api_code_body GET "/api/ci/runs/gha:acme:widgets:45/log?job=lint" >"$dir/case2d-2.out"
	local c2_hits
	c2_hits=$(curl -sf "$MOCK/_mock/counts" | jq '."/repos/acme/widgets/actions/runs/45/logs" // 0')
	echo "Case 2D-2 (rapid second read, lint): HTTP $(head -n1 "$dir/case2d-2.out") | run45ZipHits=$c2_hits"

	# Wait past actionsLiveTTL (15s) -> switch to empty_and_empty and confirm re-fetch!
	echo "Sleeping 16s for actionsLiveTTL (15s) to expire..."
	sleep 16
	curl -sf -X POST "$MOCK/_mock/config?run45Mode=empty_and_empty" >/dev/null
	api_code_body GET "/api/ci/runs/gha:acme:widgets:45/log?job=lint" >"$dir/case2d-3.out"
	local c3_hits
	c3_hits=$(curl -sf "$MOCK/_mock/counts" | jq '."/repos/acme/widgets/actions/runs/45/logs" // 0')
	echo "Case 2D-3 (after 16s TTL expiry, empty+empty, lint): HTTP $(head -n1 "$dir/case2d-3.out") | run45ZipHits=$c3_hits | body=$(tail -n +2 "$dir/case2d-3.out")"

	echo "--- Running Playwright /next/ci walkthrough (store=$store) ---"
	DAEMON_URL="$DAEMON" MOCK_URL="$MOCK" GRAIN_TOKEN="$CODE" SHOT_DIR="$WORK/screenshots/${store}" \
		node "$HERE/walk_next_ci.mjs" | tee "$WORK/results/p2-playwright-${store}.json"

	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/mock.pid"
}

# ============================================================================
# PART 3: Pipeline steward cold-start wake (SQLite and PostgreSQL)
# ============================================================================
run_part3() {
	local store=$1
	local scenario=$2 # "open_publishing" or "regression_only"
	echo "================================================================"
	echo "PART 3: Pipeline steward cold-start (store=$store, scenario=$scenario)"
	echo "================================================================"
	local dir="$WORK/p3-${store}-${scenario}"
	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/mock.pid"
	rm -rf "$dir"
	mkdir -p "$dir/data" "$dir/sandboxes" "$dir/home"
	[[ "$store" == "postgres" ]] && reset_pg

	seed_bare_repo "$dir/widgets.git"
	"$WORK/bin/mock-actions" -addr "127.0.0.1:${MOCK_PORT}" -bare "$dir/widgets.git" >"$dir/mock.log" 2>&1 </dev/null &
	echo $! >"$dir/mock.pid"
	PIDS+=($!)
	for _ in $(seq 50); do
		curl -sf "$MOCK/_mock/counts" >/dev/null && break
		sleep 0.1
	done

	echo '{"directConfigWrites": true}' >"$dir/data/state-repo.json"
	local store_flags=()
	[[ "$store" == "postgres" ]] && store_flags=(-postgres-url "$PG_DSN")

	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$dir/home" \
		GCE_METADATA_HOST="127.0.0.1:9" \
		setsid "$GRAIN" daemon \
		-data-dir "$dir/data" \
		-sandbox-dir "$dir/sandboxes" \
		-host-sandboxes \
		-ui-addr "127.0.0.1:${DAEMON_PORT}" \
		-sandbox-port-base=0 \
		-as alice@example.com \
		-repo acme/widgets \
		-default-target-repo acme/widgets \
		-poll-interval 500ms \
		-max-workers 1 \
		-max-mergers 0 \
		-github-host "127.0.0.1:${MOCK_PORT}" \
		-github-insecure-http \
		"${store_flags[@]}" >"$dir/daemon.log" 2>&1 </dev/null &
	echo $! >"$dir/daemon.pid"
	PIDS+=($!)
	wait_daemon "$dir"

	# Wait for system:pipeline-steward schedule to be seeded.
	for _ in $(seq 60); do
		if api_code_body GET "/api/schedules" | tail -n +2 | jq -e '(if type == "array" then . else .schedules end) | map(select(.id == "system:pipeline-steward")) | length == 1' >/dev/null 2>&1; then
			break
		fi
		sleep 0.25
	done

	echo "Initial system:pipeline-steward schedule:"
	api_code_body GET "/api/schedules" | tail -n +2 | jq '(if type == "array" then . else .schedules end) | map(select(.id == "system:pipeline-steward")) | .[0] | {id, enabled, lastRunAt, nextRunAt}'

	# Seed a green base_branch_ci row so base branches are green (and zero steward tasks exist).
	local now_iso sql_now sql_past sql_fail
	now_iso=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
	if [[ "$store" == "sqlite" ]]; then
		sql_now=$(date -u +"%Y-%m-%d %H:%M:%S +0000 UTC")
		sql_past=$(date -u -d "2 minutes ago" +"%Y-%m-%d %H:%M:%S +0000 UTC")
		sql_fail=$(date -u -d "1 hour ago" +"%Y-%m-%d %H:%M:%S +0000 UTC")
	else
		sql_now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
		sql_past=$(date -u -d "2 minutes ago" +"%Y-%m-%dT%H:%M:%SZ")
		sql_fail=$(date -u -d "1 hour ago" +"%Y-%m-%dT%H:%M:%SZ")
	fi
	store_sql "$store" "$dir" \
		"INSERT INTO base_branch_ci (owner, name, branch, run_id, \"commit\", started_at, conclusion) VALUES ('acme', 'widgets', 'main', 'r-green-1', '1111111111111111111111111111111111111111', '$sql_now', 'success');"

	# Raise the notification via real publishing-held state or POST /api/notifications.
	if [[ "$scenario" == "open_publishing" ]]; then
		# Persist a failing base_branch_release row so reconcilePublishingNotifications keeps pipeline.not-publishing open.
		store_sql "$store" "$dir" \
			"INSERT INTO base_branch_release (owner, name, branch, \"commit\", run_id, started_at, finished_at, conclusion, detail, failures, failing_since, attempts) VALUES ('acme', 'widgets', 'main', '1111111111111111111111111111111111111111', 'rel-1', '$sql_fail', '$sql_fail', 'failure', 'publish failed in walkthrough', 3, '$sql_fail', 3);"
		api_code_body POST "/api/notifications" \
			'{"subject":"pipeline.not-publishing:acme/widgets@main","title":"Publishing held on acme/widgets main","body":"Walkthrough cold-start publishing notification","severity":"warning","repo":"acme/widgets"}' >"$dir/notif.out"
	else
		api_code_body POST "/api/notifications" \
			'{"subject":"pipeline.steward-regression","title":"Steward report regression","body":"Walkthrough self-wake exclusion check","severity":"warning"}' >"$dir/notif.out"
	fi
	echo "Raised notification: HTTP $(head -n1 "$dir/notif.out") | $(tail -n +2 "$dir/notif.out" | jq -c '{id: .notification.id, subject: .notification.subject, family: .notification.family}')"

	# Make system:pipeline-steward immediately due so the live daemon's reconcileSchedule ticks it.
	store_sql "$store" "$dir" \
		"UPDATE schedule SET next_run_at = '$sql_past' WHERE id = 'system:pipeline-steward';"

	# Wait for the schedule tick to process (next_run_at moves into the future).
	for _ in $(seq 60); do
		local next_at
		next_at=$(api_code_body GET "/api/schedules" | tail -n +2 | jq -r '(if type == "array" then . else .schedules end) | map(select(.id == "system:pipeline-steward")) | .[0].nextRunAt')
		if [[ "$next_at" > "$now_iso" ]]; then
			break
		fi
		sleep 0.25
	done

	echo "After tick - schedule:"
	api_code_body GET "/api/schedules" | tail -n +2 | jq '(if type == "array" then . else .schedules end) | map(select(.id == "system:pipeline-steward")) | .[0] | {id, enabled, lastRunAt, nextRunAt}'
	echo "After tick - steward tasks:"
	api_code_body GET "/api/tasks" | tail -n +2 | jq '(if type == "array" then . else .tasks end) | map(select(.templateId == "system:pipeline-steward")) | map({id, state, templateId, tags})'
	echo "After tick - DB task_notification_subscription:"
	store_sql "$store" "$dir" "SELECT task_id, family FROM task_notification_subscription ORDER BY task_id, family;"
	echo "After tick - DB scheduled_firing_skip:"
	store_sql "$store" "$dir" "SELECT schedule_id, reason, outcome FROM scheduled_firing_skip WHERE schedule_id = 'system:pipeline-steward';"
	echo "After tick - daemon log lines mentioning pipeline steward:"
	grep -i "pipeline steward\|system:pipeline-steward" "$dir/daemon.log" || true

	stop_bg "$dir/daemon.pid"
	stop_bg "$dir/mock.pid"
}

PARTS=${PARTS:-"1 2 3"}
for p in $PARTS; do
	case "$p" in
	1)
		run_part1 sqlite direct
		run_part1 sqlite hostd
		run_part1 postgres hostd
		;;
	2)
		run_part2 sqlite
		run_part2 postgres
		;;
	3)
		run_part3 sqlite open_publishing
		run_part3 sqlite regression_only
		run_part3 postgres open_publishing
		run_part3 postgres regression_only
		;;
	esac
done

echo "================================================================"
echo "ALL WALKTHROUGH PARTS COMPLETED"
echo "================================================================"
