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

# Live API/CLI half of docs/test-plans/2026-11-18.md §1 (capability server
# pages and start-failure reporting) against a real `grain daemon
# -sandbox-port-base=0` over SQLite and PostgreSQL. Reuses the daemon/ghsim
# harness from docs/test-runs/2026-11-13-grain-health-exit-codes/env.sh.
#
#   STORES="sqlite postgres" ./run_walkthrough.sh
#
# Every step asserts; the first failure exits non-zero with the evidence.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$HERE/../2026-11-13-grain-health-exit-codes/env.sh"

GIT_ID="https://grain.dev/capabilities/git"
SECRET_VALUE="walk-secret-value-$$-do-not-leak"

fail() {
	echo "ASSERTION FAILED [$STORE] $*" >&2
	exit 1
}

ok() { echo "[$STORE] ok: $*"; }

git_status() {
	api GET /api/capability-servers | jq -c --arg id "$GIT_ID" '.[] | select(.id == $id) | .status'
}

# put_status prints the HTTP code and leaves the body in $WORK_DIR/put.body.
put_status() {
	curl -sS -o "$WORK_DIR/put.body" -w '%{http_code}' -X PUT \
		-H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d "$1" "$DAEMON/api/capability-servers"
}

git_body() {
	local args=$1
	printf '{"id":"%s","config":{"command":"grain","args":%s,"builtin":"git","config":{"upstream":"http://127.0.0.1:9","repos":["acme/widgets"],"token":"$secret:walk-git/token"}}}' "$GIT_ID" "$args"
}

health_rc() {
	local rc=0
	env -i PATH="/home/debian/bin:/usr/bin:/bin" HOME="$WORK_DIR/home" GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" \
		"$GRAIN" health >"$WORK_DIR/captures/$1.stdout" 2>"$WORK_DIR/captures/$1.stderr" || rc=$?
	echo "$rc"
}

run_for_store() {
	export STORE=$1
	local work="/tmp/walk-capsrv-${STORE}"
	init_env "$work"
	trap 'stop_env "$work"' EXIT
	start_ghsim "$work"
	start_daemon "$work"
	ok "daemon up ($("$GRAIN" version 2>/dev/null | head -1 || true))"

	# Item 1/4: built-in servers list with their configure schemas; a secret
	# reference is bound and its value never appears in any response.
	api PUT "/api/secrets/walk-git/token" "{\"value\":\"$SECRET_VALUE\"}" >/dev/null
	local listed
	listed=$(api GET /api/capability-servers)
	for b in git figma gchat gcp host-script; do
		jq -e --arg id "https://grain.dev/capabilities/$b" 'any(.[]; .id == $id)' <<<"$listed" >/dev/null ||
			fail "built-in $b missing from /api/capability-servers"
	done
	ok "built-in git, figma, gchat, gcp, host-script listed"

	# Item 5: port 4254 occupied -> failed (could not start ...) and retried.
	socat TCP-LISTEN:4254,bind=127.0.0.1,fork,reuseaddr /dev/null </dev/null >/dev/null 2>&1 &
	echo $! >"$work/squat4254.pid"
	sleep 0.3
	local code
	code=$(put_status "$(git_body '["capability","git"]')")
	[[ $code == 200 ]] || fail "PUT git config: $code $(cat "$work/put.body")"
	grep -q "$SECRET_VALUE" "$work/put.body" && fail "PUT response leaks the secret value"
	local st=""
	for _ in $(seq 100); do
		st=$(git_status)
		[[ $(jq -r '.startError // ""' <<<"$st") == *"port 4254 in use"* ]] && break
		sleep 0.2
	done
	[[ $(jq -r '.startError // ""' <<<"$st") == *"could not start: port 4254 in use"* ]] ||
		fail "git status with 4254 squatted: $st"
	[[ $(jq -r '.configRefused // false' <<<"$st") == false ]] || fail "start failure reported as configRefused: $st"
	ok "4254 squatted -> state=$(jq -r .state <<<"$st") startError=$(jq -r .startError <<<"$st")"
	api GET /api/capability-servers | grep -q "$SECRET_VALUE" && fail "GET /api/capability-servers leaks the secret value"
	ok "secret value absent from PUT/GET responses"
	local rc
	rc=$(health_rc 5-squatted)
	[[ $rc == 1 ]] || fail "grain health rc with 4254 squatted = $rc"
	grep -q "$GIT_ID" "$work/captures/5-squatted.stdout" || fail "grain health does not name git"
	ok "grain health exits 1 naming $GIT_ID"

	# Free the port: auto-attach within one retry interval (1m) plus slack.
	kill "$(cat "$work/squat4254.pid")" 2>/dev/null || true
	rm -f "$work/squat4254.pid"
	local freed_at=$SECONDS
	for _ in $(seq 400); do
		st=$(git_status)
		[[ $(jq -r .state <<<"$st") == attached ]] && break
		sleep 0.25
	done
	[[ $(jq -r .state <<<"$st") == attached ]] || fail "git did not attach after 4254 freed: $st"
	ok "4254 freed -> attached after $((SECONDS - freed_at))s without a save"
	for _ in $(seq 40); do
		rc=$(health_rc 5-freed)
		[[ $rc == 0 ]] && break
		sleep 0.25
	done
	[[ $rc == 0 ]] || fail "grain health rc after attach = $rc: $(cat "$work/captures/5-freed.stdout")"
	ok "grain health exits 0 once attached (drop alert cleared)"

	# Item 6: each bad -listen form is refused as config (400, naming it),
	# including when smuggled behind a value-taking flag.
	for bad in 0.0.0.0:4254 10.0.0.1:4254 127.0.0.1:abc 127.0.0.1:99999 127.0.0.1: :4254 localhost; do
		for args in "[\"capability\",\"git\",\"-listen\",\"$bad\"]" "[\"capability\",\"git\",\"-upstream\",\"--\",\"-listen\",\"$bad\"]"; do
			code=$(put_status "$(git_body "$args")")
			[[ $code == 400 ]] || fail "-listen $bad ($args) accepted: $code $(cat "$work/put.body")"
		done
	done
	st=$(git_status)
	[[ $(jq -r .state <<<"$st") == attached ]] || fail "a refused -listen disturbed the attached server: $st"
	ok "bad -listen forms refused with 400; attached server untouched"
	code=$(put_status "$(git_body '["capability","git","-listen","127.0.0.1:4254"]')")
	[[ $code == 200 ]] || fail "loopback -listen refused: $code $(cat "$work/put.body")"
	ok "loopback -listen 127.0.0.1:4254 accepted"

	# Item 6 (CLI): the list renders states for people.
	env -i PATH="/home/debian/bin:/usr/bin:/bin" HOME="$work/home" GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" \
		"$GRAIN" capability-servers list >"$work/captures/list.stdout" 2>&1 || true
	grep -q "needs-hostd:" "$work/captures/list.stdout" && fail "capability-servers list prints wire state needs-hostd:"
	ok "grain capability-servers list renders without wire states"

	# Item 3 (removal): deleting the server removes it from the list.
	api GET /api/capability-servers/usage >"$work/captures/usage.json" || fail "GET /api/capability-servers/usage"
	ok "usage read answers ($(jq -c 'if type == "array" then length else keys end' "$work/captures/usage.json"))"
	code=$(curl -sS -o "$work/put.body" -w '%{http_code}' -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		-d "{\"id\":\"$GIT_ID\"}" "$DAEMON/api/capability-servers/remove")
	[[ $code == 400 ]] && grep -q "cannot be removed: disable it instead" "$work/put.body" || fail "removing built-in git: $code $(cat "$work/put.body")"
	ok "removing built-in git refused: disable it instead"
	api POST /api/capability-servers/disable "{\"id\":\"$GIT_ID\"}" >/dev/null || fail "disable git"
	for _ in $(seq 40); do
		st=$(git_status)
		[[ $(jq -r .state <<<"$st") == disabled ]] && break
		sleep 0.25
	done
	[[ $(jq -r .state <<<"$st") == disabled ]] || fail "git not disabled: $st"
	rc=$(health_rc 7-disabled)
	[[ $rc == 0 ]] || fail "grain health rc with git disabled = $rc: $(cat "$work/captures/7-disabled.stdout")"
	ok "git disabled; grain health exits 0 (a disabled server raises no drop alert)"
	local custom="https://capabilities.example.com/walk"
	api PUT /api/capability-servers "{\"id\":\"$custom\",\"config\":{\"command\":\"/bin/false\"}}" >/dev/null || fail "add custom server"
	api POST /api/capability-servers/remove "{\"id\":\"$custom\"}" >/dev/null || fail "remove custom server"
	api GET /api/capability-servers | jq -e --arg id "$custom" 'all(.[]; .id != $id)' >/dev/null || fail "custom server still listed"
	ok "custom server added then removed"

	stop_env "$work"
	trap - EXIT
}

for s in ${STORES:-sqlite postgres}; do
	run_for_store "$s"
done
echo "ALL PASSED"
