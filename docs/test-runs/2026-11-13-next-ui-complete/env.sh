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

# Shared real-daemon environment for docs/test-plans/2026-11-13.md §1
# (The complete Angular UI at /next/). Starts multi-repo ghsim (5 connected
# repos) on 127.0.0.1:4294, iapsim (JWKS 4295, IAP proxy 4296), and a real
# `grain daemon -sandbox-port-base=0` (SQLite) on 127.0.0.1:4292 with 2
# configured instance repos (acme/widgets, acme/api), seeded through the API
# with tasks, sessions, processes, CI runs, viewer + limited roles, and a
# personal token.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
DAEMON_PORT=${DAEMON_PORT:-4292}
GHSIM_PORT=${GHSIM_PORT:-4294}
IAP_ISSUER_PORT=${IAP_ISSUER_PORT:-4295}
IAP_PROXY_PORT=${IAP_PROXY_PORT:-4296}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
IAP_ISSUER="http://127.0.0.1:${IAP_ISSUER_PORT}"
IAP_PROXY="http://127.0.0.1:${IAP_PROXY_PORT}"
IAP_AUD="/projects/123456/global/backendServices/789012"
IAP_ISS="https://iap.test.local"

chmod +x "$HERE/fake-agy.py"

stop_env() {
	local work=${1:-${WORK_DIR:-/tmp/walk-next-ui}}
	local p pid
	for p in daemon iapsim ghsim; do
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

init_env() {
	WORK_DIR=${1:-/tmp/walk-next-ui}
	stop_env "$WORK_DIR"
	rm -rf "$WORK_DIR"
	mkdir -p "$WORK_DIR/data" "$WORK_DIR/sandboxes" "$WORK_DIR/bin" "$WORK_DIR/home" "$WORK_DIR/bares"

	(cd "$ROOT" && go build -o "$WORK_DIR/bin/ghsim" "$HERE/ghsim.go")
	(cd "$ROOT" && go build -o "$WORK_DIR/bin/iapsim" tests/iap/iapsim.go)

	for r in widgets api docs infra mobile; do
		git init -q --bare -b main "$WORK_DIR/bares/${r}.git"
		git -C "$WORK_DIR/bares/${r}.git" config http.receivepack true
		local tmp
		tmp=$(mktemp -d)
		git -C "$tmp" init -q -b main
		mkdir -p "$tmp/.grain/workflows"
		printf "# acme/%s\n" "$r" >"$tmp/README.md"
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
        run: echo "PASS"
EOF
		git -C "$tmp" add .
		git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed $r"
		git -C "$tmp" push -q "$WORK_DIR/bares/${r}.git" main
		git -C "$tmp" branch release/1.0
		git -C "$tmp" push -q "$WORK_DIR/bares/${r}.git" release/1.0
		if [[ "$r" == "widgets" ]]; then
			git -C "$tmp" checkout -qb feat/submit-seed
			printf "\nSeeded submit change.\n" >>"$tmp/README.md"
			git -C "$tmp" commit -qam "feat: seeded submit change"
			git -C "$tmp" push -q "$WORK_DIR/bares/${r}.git" feat/submit-seed
		fi
		rm -rf "$tmp"
	done

	echo "ghp_walkthrough" >"$WORK_DIR/bot.token"
	echo "walkthrough-model-key" >"$WORK_DIR/model.key"
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
  - id: primary-agy
    framework: antigravity
    models: [claude-sonnet-4-6, gemini-2.5-pro]
    credential_file: $WORK_DIR/model.key
default_model: claude-sonnet-4-6
EOF
	"$GRAIN" connections -data-dir "$WORK_DIR/data" import "$WORK_DIR/seed-git.yaml" >"$WORK_DIR/connections.log" 2>&1
}

start_ghsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/ghsim" -addr "$GHSIM" -bares-dir "$work/bares" -repos "widgets,api,docs,infra,mobile" -token "ghp_walkthrough" -default main >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"
	for _ in $(seq 50); do
		curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && return 0
		sleep 0.1
	done
	echo "ghsim failed to start; see $work/ghsim.log" >&2
	return 1
}

start_iapsim() {
	local work=${1:-$WORK_DIR}
	"$work/bin/iapsim" \
		-issuer "127.0.0.1:${IAP_ISSUER_PORT}" \
		-proxy "127.0.0.1:${IAP_PROXY_PORT}" \
		-upstream "$DAEMON" \
		-aud "$IAP_AUD" \
		-iss "$IAP_ISS" >"$work/iapsim.log" 2>&1 &
	echo $! >"$work/iapsim.pid"
	for _ in $(seq 50); do
		curl -sf "$IAP_ISSUER/jwks" >/dev/null && return 0
		sleep 0.1
	done
	echo "iapsim failed to start; see $work/iapsim.log" >&2
	return 1
}

start_daemon() {
	local work=${1:-$WORK_DIR}
	shift 1 || true
	touch "$work/daemon.log"
	env -i \
		PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
		HOME="$work/home" \
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
		-default-target-repo acme/widgets \
		-ui-iap-audience "$IAP_AUD" \
		-ui-iap-issuer "$IAP_ISS" \
		-ui-iap-keys-url "${IAP_ISSUER}/jwks" \
		-poll-interval 1h \
		-max-workers 1 \
		-max-mergers 0 \
		-agy-path "$HERE/fake-agy.py" \
		-github-host "$GHSIM" \
		-github-insecure-http \
		"$@" >>"$work/daemon.log" 2>&1 </dev/null &
	echo $! >"$work/daemon.pid"

	for _ in $(seq 120); do
		if [[ -s "$work/data/local-api-token" ]]; then
			CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
			if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
				"$GRAIN" connections -data-dir "$work/data" import "$work/seed-models.yaml" >>"$work/connections.log" 2>&1
				sleep 0.5
				DB="$work/data/store/grain.db"
				export WORK_DIR="$work" CODE DB DAEMON GHSIM IAP_ISSUER IAP_PROXY IAP_AUD IAP_ISS
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
		curl -sS -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -sS -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path"
	fi
}

seed_daemon() {
	# Pause dispatch so seeded queued tasks and CI runs remain in deterministic states
	api POST /api/pipeline-pause '{"paused":true,"reason":"walkthrough seeding"}' >/dev/null

	# Configure exactly 2 instance repos (while ghsim has 5 connected repos)
	api POST /api/repos '{"repo":"acme/widgets"}' >/dev/null
	api POST /api/repos '{"repo":"acme/api"}' >/dev/null

	# 1. Seed tasks across states: proposed, queued, awaiting_reply, awaiting_submit, closed
	local p_id q_id r_id s_id c_id pr_num
	p_id=$(api POST /api/tasks '{"title":"Seeded proposal on widgets","description":"Proposal brief","repo":"acme/widgets","approved":false}' | jq -r .id)
	q_id=$(api POST /api/tasks '{"title":"Seeded queued task on api","description":"Queued brief","repo":"acme/api","approved":true}' | jq -r .id)
	r_id=$(api POST /api/tasks '{"title":"Seeded parked question","description":"Question brief","repo":"acme/widgets","approved":true}' | jq -r .id)
	api POST "/api/tasks/${r_id}/messages" '{"summary":"Should we keep v1 compatibility?","body":"Checking backwards compatibility requirements."}' >/dev/null

	pr_num=$(curl -sS -X POST -H "Authorization: token ghp_walkthrough" -H "Content-Type: application/json" \
		-d '{"title":"Seeded submit PR","head":"feat/submit-seed","base":"main","body":"Seeded PR"}' \
		"http://$GHSIM/repos/acme/widgets/pulls" | jq -r .number)
	s_id=$(api POST /api/pull-requests/adopt "{\"pr\":\"acme/widgets#${pr_num}\"}" | jq -r '.taskId // .task.id // .id // empty')

	c_id=$(api POST /api/tasks '{"title":"Seeded closed task","description":"Closed brief","repo":"acme/widgets","approved":false}' | jq -r .id)
	api POST "/api/tasks/${c_id}/close" '{}' >/dev/null

	# 2. Seed standing console session
	api POST /api/console-sessions '{"id":"triage-room","title":"Triage Room"}' >/dev/null || true

	# 3. Seed template, schedule, and suite
	local tmpl_id
	tmpl_id=$(api POST /api/templates '{"name":"Dependency audit","title":"Audit dependencies","description":"Check outdated modules."}' | jq -r .id)
	api POST /api/schedules '{"title":"Nightly widgets check","description":"Run nightly check","repo":"acme/widgets","base":"main","recurrence":{"kind":"daily","timeOfDay":"03:00","timeZone":"UTC"}}' >/dev/null
	api POST /api/suites "{\"name\":\"Release readiness\",\"items\":[{\"templateId\":\"${tmpl_id}\"}]}" >/dev/null

	# 4. Enable Grain CI on acme/widgets and trigger a CI run
	api PUT /api/deployment/repos/acme/widgets/config '{"ciMode":"grain"}' >/dev/null || true
	api POST /api/ci/runs '{"repo":"acme/widgets","ref":"main"}' >/dev/null || true

	# 5. Seed RBAC principals (viewer + contributor) and a limited personal token
	api POST /api/access/principals '{"entry":"user:viewer@example.com","role":"grain/viewer","reason":"walkthrough viewer check"}' >/dev/null
	api POST /api/access/principals '{"entry":"user:contributor@example.com","role":"grain/contributor","reason":"walkthrough contributor check"}' >/dev/null
	LIMITED_TOKEN=$(api POST /api/external-agents/tokens '{"name":"walk-limited-token","scopes":["tasks:read","settings:read"]}' | jq -r .token)

	cat >"$WORK_DIR/seeded.json" <<EOF
{
  "daemonUrl": "$DAEMON",
  "iapProxyUrl": "$IAP_PROXY",
  "accessCode": "$CODE",
  "limitedToken": "$LIMITED_TOKEN",
  "dbPath": "$DB",
  "proposalId": "$p_id",
  "queuedId": "$q_id",
  "questionId": "$r_id",
  "submitId": "$s_id",
  "closedId": "$c_id"
}
EOF
	export LIMITED_TOKEN SEED_PROPOSAL_ID="$p_id" SEED_QUEUED_ID="$q_id" SEED_QUESTION_ID="$r_id" SEED_SUBMIT_ID="$s_id" SEED_CLOSED_ID="$c_id"
}
