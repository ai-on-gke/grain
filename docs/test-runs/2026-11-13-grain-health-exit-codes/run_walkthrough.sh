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

# Executes all §3 walkthrough items (docs/test-plans/2026-11-13-afternoon.md §3)
# against a live `grain daemon` over SQLite and/or PostgreSQL, recording stdout,
# stderr and exit code ($?) for every state under $WORK_DIR/captures/.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

STRICT_NO_COUNTERS=${STRICT_NO_COUNTERS:-1}

assert_eq() {
	local desc=$1 got=$2 want=$3
	if [[ "$got" != "$want" ]]; then
		echo "ASSERTION FAILED [$STORE] $desc: got=$(printf '%q' "$got") want=$(printf '%q' "$want")" >&2
		return 1
	fi
}

assert_contains() {
	local desc=$1 haystack=$2 needle=$3
	if [[ "$haystack" != *"$needle"* ]]; then
		echo "ASSERTION FAILED [$STORE] $desc: missing $(printf '%q' "$needle") in:" >&2
		echo "$haystack" >&2
		return 1
	fi
}

assert_not_contains() {
	local desc=$1 haystack=$2 needle=$3
	if [[ "$haystack" == *"$needle"* ]]; then
		echo "ASSERTION FAILED [$STORE] $desc: unexpected $(printf '%q' "$needle") in:" >&2
		echo "$haystack" >&2
		return 1
	fi
}

record_summary() {
	local label=$1 jq_expr=$2
	local rc out err json_rc json_err json_valid
	rc=$(cat "$WORK_DIR/captures/${label}.rc")
	out=$(cat "$WORK_DIR/captures/${label}.stdout")
	err=$(cat "$WORK_DIR/captures/${label}.stderr")
	json_rc=$(cat "$WORK_DIR/captures/${label}-json.rc")
	json_err=$(cat "$WORK_DIR/captures/${label}-json.stderr")
	if jq -e "$jq_expr" "$WORK_DIR/captures/${label}-json.stdout" >/dev/null 2>&1; then
		json_valid="valid"
	else
		json_valid="INVALID"
	fi
	local has_no_counters="no"
	if [[ "$out" == *"no counters: nothing on this daemon registers any"* ]]; then
		has_no_counters="yes"
	fi
	printf "[%s] %-28s text(rc=%s stderr=%q no_counters=%s) json(rc=%s stderr_bytes=%d jq=%s)\n" \
		"$STORE" "$label" "$rc" "$err" "$has_no_counters" "$json_rc" "${#json_err}" "$json_valid"
}

run_for_store() {
	export STORE=$1
	local work="/tmp/walk-health-${STORE}"
	trap 'stop_env "$work"' RETURN

	echo "================================================================"
	echo "Running §3 grain health walkthrough on STORE=${STORE} (${work})"
	echo "================================================================"

	init_env "$work"
	start_ghsim "$work"
	start_daemon "$work"

	api PUT "/api/deployment" '{"targetRepos":["acme/widgets"]}' >/dev/null
	api PUT "/api/repos/acme/widgets/declarations" '{"declarations":true}' >/dev/null
	# Give initial repo declaration sync a moment to settle cleanly.
	sleep 1.5

	# ------------------------------------------------------------------
	# 1a. Clean healthy daemon
	# ------------------------------------------------------------------
	capture_health "1a-clean" health
	capture_health "1a-clean-json" health -json
	capture_health "1a-clean-prejson" -json health
	record_summary "1a-clean" '.enabled == true and ((.dispatchBlocked // []) | length == 0) and ((.capabilityServers // []) | length == 0) and ((.staleSchedules // []) | length == 0) and ((.refusedDeclarations // []) | length == 0)'
	assert_eq "1a text rc" "$(cat "$work/captures/1a-clean.rc")" "0"
	assert_eq "1a text stderr" "$(cat "$work/captures/1a-clean.stderr")" ""
	assert_contains "1a text stdout" "$(cat "$work/captures/1a-clean.stdout")" "no counters: nothing on this daemon registers any"
	assert_eq "1a json rc" "$(cat "$work/captures/1a-clean-json.rc")" "0"
	assert_eq "1a json stderr" "$(cat "$work/captures/1a-clean-json.stderr")" ""
	assert_eq "1a pre-verb -json rc" "$(cat "$work/captures/1a-clean-prejson.rc")" "0"
	jq -e '.enabled == true' "$work/captures/1a-clean-json.stdout" >/dev/null
	jq -e '.enabled == true' "$work/captures/1a-clean-prejson.stdout" >/dev/null

	# ------------------------------------------------------------------
	# 1b. Live WARNING-only state #1: MergeLoginWarnings (GRAIN_MERGE_LOGINS=octocat)
	# ------------------------------------------------------------------
	api PUT "/api/deployment" '{"targetRepos":["acme/widgets"]}' >/dev/null
	curl -fsS -X POST "http://$GHSIM/_sim/merge-logins?value=octocat" >/dev/null
	start_daemon "$work"
	capture_health "1b-warning-mergelogin" health
	capture_health "1b-warning-mergelogin-json" health -json
	record_summary "1b-warning-mergelogin" '(.mergeLoginWarnings | length == 1) and (.mergeLoginWarnings[0].login == "octocat")'
	assert_eq "1b text rc" "$(cat "$work/captures/1b-warning-mergelogin.rc")" "0"
	assert_eq "1b text stderr" "$(cat "$work/captures/1b-warning-mergelogin.stderr")" ""
	assert_contains "1b text stdout" "$(cat "$work/captures/1b-warning-mergelogin.stdout")" "WARNING: reviewed-merge logins that belong to a user account:"
	assert_not_contains "1b no counters suppressed" "$(cat "$work/captures/1b-warning-mergelogin.stdout")" "no counters"
	assert_eq "1b json rc" "$(cat "$work/captures/1b-warning-mergelogin-json.rc")" "0"
	assert_eq "1b json stderr" "$(cat "$work/captures/1b-warning-mergelogin-json.stderr")" ""
	jq -e '(.mergeLoginWarnings | length == 1) and (.mergeLoginWarnings[0].login == "octocat")' "$work/captures/1b-warning-mergelogin-json.stdout" >/dev/null

	curl -fsS -X POST "http://$GHSIM/_sim/merge-logins?value=" >/dev/null
	start_daemon "$work"

	# ------------------------------------------------------------------
	# 1c. Live WARNING-only state #2: CIWaits (resume_later CI wait > 1h)
	# ------------------------------------------------------------------
	curl -fsS -X POST "http://$GHSIM/_sim/seed-actions" -H "Content-Type: application/json" -d '{"runs":[{"id":5001,"name":"CI","path":".github/workflows/ci.yml","headBranch":"main","headSha":"0000000000000000000000000000000000000001","event":"pull_request","status":"in_progress"}]}' >/dev/null
	cat >"$work/action-default.json" <<'EOF'
{
  "kind": "resume_later",
  "push_branch": "HEAD",
  "mcp_args": {
    "reason": "waiting on CI verdict for walkthrough",
    "when": {"ci": "gha:acme:widgets:5001"}
  }
}
EOF
	local ciwait_task
	ciwait_task=$(api POST "/api/tasks" '{"title":"Walkthrough CI wait task","repo":"acme/widgets","approved":true}' | jq -r '.id')
	for _ in $(seq 80); do
		local st
		st=$(api GET "/api/tasks/$ciwait_task" | jq -r '.state')
		if [[ "$st" == "deferred" ]]; then
			break
		fi
		sleep 0.25
	done
	rm -f "$work/action-default.json"
	# Backdate the suspended CI wait to 2 hours ago (> CIWaitHealthAge = 1h).
	local two_hours_ago
	two_hours_ago=$(python3 -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=2)).strftime("%Y-%m-%d %H:%M:%S+00:00"))')
	local two_hours_minus_5s
	two_hours_minus_5s=$(python3 -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=2, seconds=5)).strftime("%Y-%m-%d %H:%M:%S+00:00"))')
	local two_hours_plus_2s
	two_hours_plus_2s=$(python3 -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=1, minutes=59, seconds=58)).strftime("%Y-%m-%d %H:%M:%S+00:00"))')
	store_exec "UPDATE task_run SET started_at='${two_hours_minus_5s}', finished_at='${two_hours_ago}' WHERE task_id='${ciwait_task}';"
	store_exec "UPDATE task SET deferred_at='${two_hours_ago}' WHERE id='${ciwait_task}';"
	store_exec "UPDATE task_suspension SET created_at='${two_hours_plus_2s}' WHERE task_id='${ciwait_task}';"

	capture_health "1c-warning-ciwait" health
	capture_health "1c-warning-ciwait-json" health -json
	record_summary "1c-warning-ciwait" '(.ciWaits | length == 1) and (.ciWaits[0].taskId == "'"$ciwait_task"'")'
	assert_eq "1c text rc" "$(cat "$work/captures/1c-warning-ciwait.rc")" "0"
	assert_eq "1c text stderr" "$(cat "$work/captures/1c-warning-ciwait.stderr")" ""
	assert_contains "1c text stdout" "$(cat "$work/captures/1c-warning-ciwait.stdout")" "WARNING: tasks waiting on CI for more than 1h0m0s (resume_later):"
	if [[ "$STRICT_NO_COUNTERS" == "1" ]]; then
		assert_not_contains "1c no counters suppressed" "$(cat "$work/captures/1c-warning-ciwait.stdout")" "no counters"
	fi
	assert_eq "1c json rc" "$(cat "$work/captures/1c-warning-ciwait-json.rc")" "0"
	assert_eq "1c json stderr" "$(cat "$work/captures/1c-warning-ciwait-json.stderr")" ""
	jq -e '(.ciWaits | length == 1)' "$work/captures/1c-warning-ciwait-json.stdout" >/dev/null

	api POST "/api/tasks/${ciwait_task}/close" '{}' >/dev/null
	if [[ "$STRICT_NO_COUNTERS" == "1" ]]; then
		api GET "/api/sandboxes" | jq -e '((.ciWaits // []) | length == 0)' >/dev/null
	fi

	# ------------------------------------------------------------------
	# 1d. Live ALERT-only state: StoreSwitch (db-config.json switched live)
	# ------------------------------------------------------------------
	cat >"$work/data/db-config.json" <<'EOF'
{"driver":"postgres","dsn":"postgres://127.0.0.1:5432/grain_switched_other?sslmode=disable"}
EOF
	for _ in $(seq 60); do
		if api GET "/api/sandboxes" | jq -e '.daemon.storeSwitch != null' >/dev/null 2>&1; then
			break
		fi
		sleep 0.25
	done
	capture_health "1d-alert-storeswitch" health
	capture_health "1d-alert-storeswitch-json" health -json
	record_summary "1d-alert-storeswitch" '.daemon.storeSwitch != null'
	assert_eq "1d text rc" "$(cat "$work/captures/1d-alert-storeswitch.rc")" "0"
	assert_eq "1d text stderr" "$(cat "$work/captures/1d-alert-storeswitch.stderr")" ""
	assert_contains "1d text stdout" "$(cat "$work/captures/1d-alert-storeswitch.stdout")" "ALERT (since "
	if [[ "$STRICT_NO_COUNTERS" == "1" ]]; then
		assert_not_contains "1d no counters suppressed" "$(cat "$work/captures/1d-alert-storeswitch.stdout")" "no counters"
	fi
	assert_eq "1d json rc" "$(cat "$work/captures/1d-alert-storeswitch-json.rc")" "0"
	assert_eq "1d json stderr" "$(cat "$work/captures/1d-alert-storeswitch-json.stderr")" ""
	jq -e '.daemon.storeSwitch != null' "$work/captures/1d-alert-storeswitch-json.stdout" >/dev/null

	rm -f "$work/data/db-config.json"
	start_daemon "$work"

	# ------------------------------------------------------------------
	# 2a. Single FAILING #1: DispatchBlocked (git-proxy port occupied)
	# ------------------------------------------------------------------
	stop_daemon "$work"
	local aligned_base
	aligned_base=$(find_free_aligned_port_base)
	socat "TCP-LISTEN:${aligned_base},bind=127.0.0.1,fork,reuseaddr" /dev/null </dev/null >/dev/null 2>&1 &
	echo $! >"$work/squat_gitproxy.pid"
	sleep 0.2
	PORT_BASE="$aligned_base" start_daemon "$work"
	for _ in $(seq 60); do
		if api GET "/api/config" | jq -e '.reconcilerDown == true and (.dispatchBlocked | length == 1)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.1
	done
	local blocked_task
	blocked_task=$(api POST "/api/tasks" '{"title":"Task held behind blocked dispatch","repo":"acme/widgets","approved":true}' | jq -r '.id')

	capture_health "2a-failing-dispatch" health
	capture_health "2a-failing-dispatch-json" health -json
	record_summary "2a-failing-dispatch" '(.dispatchBlocked | length == 1) and (.dispatchBlocked[0].name == "git-proxy")'
	assert_eq "2a text rc" "$(cat "$work/captures/2a-failing-dispatch.rc")" "1"
	assert_eq "2a text stderr" "$(cat "$work/captures/2a-failing-dispatch.stderr")" "grain: health: failing: dispatch is blocked"
	assert_contains "2a text stdout" "$(cat "$work/captures/2a-failing-dispatch.stdout")" "FAILING: dispatch is blocked -- no task is dispatched until this clears:"
	if [[ "$STRICT_NO_COUNTERS" == "1" ]]; then
		assert_not_contains "2a no counters suppressed" "$(cat "$work/captures/2a-failing-dispatch.stdout")" "no counters"
	fi
	assert_eq "2a json rc" "$(cat "$work/captures/2a-failing-dispatch-json.rc")" "1"
	assert_eq "2a json stderr" "$(cat "$work/captures/2a-failing-dispatch-json.stderr")" ""
	jq -e '(.dispatchBlocked | length == 1) and (.dispatchBlocked[0].name == "git-proxy")' "$work/captures/2a-failing-dispatch-json.stdout" >/dev/null

	api POST "/api/tasks/${blocked_task}/close" '{}' >/dev/null
	stop_daemon "$work"
	kill "$(cat "$work/squat_gitproxy.pid")" 2>/dev/null || true
	rm -f "$work/squat_gitproxy.pid"
	start_daemon "$work"

	# ------------------------------------------------------------------
	# 2b. Single FAILING #2: CapabilityServers (built-in git port 4254 occupied)
	# ------------------------------------------------------------------
	socat TCP-LISTEN:4254,bind=127.0.0.1,fork,reuseaddr /dev/null </dev/null >/dev/null 2>&1 &
	echo $! >"$work/squat4254.pid"
	sleep 0.2
	api PUT "/api/secrets/walk-git/token" '{"value":"walk-fake-upstream-token"}' >/dev/null
	api PUT "/api/capability-servers" '{"id":"https://grain.dev/capabilities/git","config":{"command":"grain","args":["capability","git"],"builtin":"git","config":{"upstream":"http://127.0.0.1:9","repos":["acme/widgets"],"token":"$secret:walk-git/token"}}}' >/dev/null
	for _ in $(seq 60); do
		if api GET "/api/sandboxes" | jq -e '(.capabilityServers | length == 1)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.2
	done

	capture_health "2b-failing-capserver" health
	capture_health "2b-failing-capserver-json" health -json
	record_summary "2b-failing-capserver" '(.capabilityServers | length == 1) and (.capabilityServers[0].id == "https://grain.dev/capabilities/git")'
	assert_eq "2b text rc" "$(cat "$work/captures/2b-failing-capserver.rc")" "1"
	assert_eq "2b text stderr" "$(cat "$work/captures/2b-failing-capserver.stderr")" "grain: health: failing: 1 capability server(s) out of use"
	assert_contains "2b text stdout" "$(cat "$work/captures/2b-failing-capserver.stdout")" "FAILING: capability servers out of use -- no grant is minted from one until it attaches again:"
	assert_not_contains "2b no counters suppressed" "$(cat "$work/captures/2b-failing-capserver.stdout")" "no counters"
	assert_eq "2b json rc" "$(cat "$work/captures/2b-failing-capserver-json.rc")" "1"
	assert_eq "2b json stderr" "$(cat "$work/captures/2b-failing-capserver-json.stderr")" ""
	jq -e '(.capabilityServers | length == 1) and (.capabilityServers[0].id == "https://grain.dev/capabilities/git")' "$work/captures/2b-failing-capserver-json.stdout" >/dev/null

	# Release 4254 and restart daemon so git capability server attaches cleanly (0 failing).
	kill "$(cat "$work/squat4254.pid")" 2>/dev/null || true
	rm -f "$work/squat4254.pid"
	start_daemon "$work"
	for _ in $(seq 50); do
		if api GET "/api/sandboxes" | jq -e '((.capabilityServers // []) | length == 0)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.2
	done

	# ------------------------------------------------------------------
	# 2c. Single FAILING #3: StaleSchedules (schedule > 2x cadence without run)
	# ------------------------------------------------------------------
	local sched_id
	sched_id=$(api POST "/api/schedules" '{"title":"Hourly widget audit","body":"Audit widgets","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":1},"enabled":true}' | jq -r '.id')
	local four_hours_ago
	four_hours_ago=$(python3 -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=4)).strftime("%Y-%m-%d %H:%M:%S+00:00"))')
	store_exec "UPDATE schedule SET created_at='${four_hours_ago}', last_run_at='${four_hours_ago}' WHERE id='${sched_id}';"

	capture_health "2c-failing-schedule" health
	capture_health "2c-failing-schedule-json" health -json
	record_summary "2c-failing-schedule" '(.staleSchedules | length == 1) and (.staleSchedules[0].id == "'"$sched_id"'")'
	assert_eq "2c text rc" "$(cat "$work/captures/2c-failing-schedule.rc")" "1"
	assert_eq "2c text stderr" "$(cat "$work/captures/2c-failing-schedule.stderr")" "grain: health: failing: 1 stale schedule(s)"
	assert_contains "2c text stdout" "$(cat "$work/captures/2c-failing-schedule.stdout")" "FAILING: schedules with no run in more than twice their cadence:"
	assert_not_contains "2c no counters suppressed" "$(cat "$work/captures/2c-failing-schedule.stdout")" "no counters"
	assert_eq "2c json rc" "$(cat "$work/captures/2c-failing-schedule-json.rc")" "1"
	assert_eq "2c json stderr" "$(cat "$work/captures/2c-failing-schedule-json.stderr")" ""
	jq -e '(.staleSchedules | length == 1) and (.staleSchedules[0].id == "'"$sched_id"'")' "$work/captures/2c-failing-schedule-json.stdout" >/dev/null

	# Disable schedule temporarily so 2d tests RefusedDeclarations in isolation.
	api PATCH "/api/schedules/${sched_id}" '{"enabled":false}' >/dev/null

	# ------------------------------------------------------------------
	# 2d. Single FAILING #4: RefusedDeclarations (malformed .grain/ YAML on main)
	# ------------------------------------------------------------------
	local tmp_clone
	tmp_clone=$(mktemp -d)
	git clone -q "$work/widgets.git" "$tmp_clone"
	mkdir -p "$tmp_clone/.grain/templates"
	cat >"$tmp_clone/.grain/templates/broken.yml" <<'EOF'
name: [broken, template]
title:
  nested: map_not_allowed_here
EOF
	git -C "$tmp_clone" add .grain/templates/broken.yml
	git -C "$tmp_clone" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "test: push malformed .grain template"
	git -C "$tmp_clone" push -q origin main
	rm -rf "$tmp_clone"

	# Expire declarationPollInterval (2m) so the next 500ms reconcile tick re-reads acme/widgets.
	store_exec "UPDATE repo_declaration_sync SET checked_at='${four_hours_ago}' WHERE owner='acme' AND name='widgets';"

	for _ in $(seq 60); do
		if api GET "/api/sandboxes" | jq -e '(.refusedDeclarations | length == 1)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.25
	done

	capture_health "2d-failing-decl" health
	capture_health "2d-failing-decl-json" health -json
	record_summary "2d-failing-decl" '(.refusedDeclarations | length == 1) and (.refusedDeclarations[0].repo == "acme/widgets")'
	assert_eq "2d text rc" "$(cat "$work/captures/2d-failing-decl.rc")" "1"
	assert_eq "2d text stderr" "$(cat "$work/captures/2d-failing-decl.stderr")" "grain: health: failing: 1 repository declaration set(s) not read in full"
	assert_contains "2d text stdout" "$(cat "$work/captures/2d-failing-decl.stdout")" "FAILING: repository declarations not read in full:"
	assert_not_contains "2d no counters suppressed" "$(cat "$work/captures/2d-failing-decl.stdout")" "no counters"
	assert_eq "2d json rc" "$(cat "$work/captures/2d-failing-decl-json.rc")" "1"
	assert_eq "2d json stderr" "$(cat "$work/captures/2d-failing-decl-json.stderr")" ""
	jq -e '(.refusedDeclarations | length == 1) and (.refusedDeclarations[0].repo == "acme/widgets")' "$work/captures/2d-failing-decl-json.stdout" >/dev/null

	# ------------------------------------------------------------------
	# 2e. Combined FAILING (3 simultaneous sections while dispatch is live):
	#     CapabilityServers + StaleSchedules + RefusedDeclarations
	# ------------------------------------------------------------------
	api PATCH "/api/schedules/${sched_id}" '{"enabled":true}' >/dev/null
	store_exec "UPDATE schedule SET created_at='${four_hours_ago}', last_run_at='${four_hours_ago}' WHERE id='${sched_id}';"
	stop_daemon "$work"
	socat TCP-LISTEN:4254,bind=127.0.0.1,fork,reuseaddr /dev/null </dev/null >/dev/null 2>&1 &
	echo $! >"$work/squat4254.pid"
	sleep 0.2
	start_daemon "$work"
	for _ in $(seq 60); do
		if api GET "/api/sandboxes" | jq -e '(.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.2
	done

	capture_health "2e-failing-combined3" health
	capture_health "2e-failing-combined3-json" health -json
	record_summary "2e-failing-combined3" '(.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)'
	assert_eq "2e text rc" "$(cat "$work/captures/2e-failing-combined3.rc")" "1"
	assert_eq "2e text stderr" "$(cat "$work/captures/2e-failing-combined3.stderr")" "grain: health: failing: 1 capability server(s) out of use, 1 stale schedule(s), 1 repository declaration set(s) not read in full"
	assert_contains "2e cap section" "$(cat "$work/captures/2e-failing-combined3.stdout")" "FAILING: capability servers out of use"
	assert_contains "2e sched section" "$(cat "$work/captures/2e-failing-combined3.stdout")" "FAILING: schedules with no run in more than twice their cadence:"
	assert_contains "2e decl section" "$(cat "$work/captures/2e-failing-combined3.stdout")" "FAILING: repository declarations not read in full:"
	assert_not_contains "2e no counters suppressed" "$(cat "$work/captures/2e-failing-combined3.stdout")" "no counters"
	assert_eq "2e json rc" "$(cat "$work/captures/2e-failing-combined3-json.rc")" "1"
	assert_eq "2e json stderr" "$(cat "$work/captures/2e-failing-combined3-json.stderr")" ""
	jq -e '(.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)' "$work/captures/2e-failing-combined3-json.stdout" >/dev/null

	# ------------------------------------------------------------------
	# 3. In-sandbox `grain health` and `grain read deploy://health` parity
	#    while the 3 FAILING sections above are live on the daemon!
	# ------------------------------------------------------------------
	cat >"$work/in-sandbox-probe.py" <<PY
import json, os, subprocess
env = dict(os.environ)
env.pop("GRAIN_SERVER", None)
env.pop("GRAIN_TOKEN", None)
env.pop("GRAIN_CONTEXT", None)
grain_bin = "${GRAIN}"
results = {}
for name, argv in [
    ("grain_health", [grain_bin, "health"]),
    ("deploy_health", [grain_bin, "read", "deploy://health"]),
    ("deploy_health_json", [grain_bin, "read", "deploy://health.json"]),
]:
    p = subprocess.run(argv, capture_output=True, text=True, env=env)
    parsed = json.loads(p.stdout)
    inner = parsed.get("health") if isinstance(parsed.get("health"), dict) else parsed
    results[name] = {
        "rc": p.returncode,
        "stderr": p.stderr,
        "stdout_bytes": len(p.stdout),
        "capabilityServers": len(inner.get("capabilityServers") or []),
        "staleSchedules": len(inner.get("staleSchedules") or []),
        "refusedDeclarations": len(inner.get("refusedDeclarations") or []),
    }
    with open("${work}/captures/3-sandbox-" + name + ".stdout", "w") as f:
        f.write(p.stdout)
    with open("${work}/captures/3-sandbox-" + name + ".stderr", "w") as f:
        f.write(p.stderr)
    with open("${work}/captures/3-sandbox-" + name + ".rc", "w") as f:
        f.write(str(p.returncode) + "\n")
with open("${work}/captures/3-sandbox-summary.json", "w") as f:
    json.dump(results, f, indent=2)
print(json.dumps(results))
PY
	cat >"$work/action-default.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "python3 -B ${work}/in-sandbox-probe.py",
        "description": "probing in-sandbox grain health and deploy://health"
      }
    },
    {
      "tool": "comment_on_issue",
      "args": {
        "comment": "in-sandbox health probe complete",
        "summary": "Verified in-sandbox grain health exits 0 while 3 checks are FAILING"
      }
    }
  ]
}
EOF
	local sb_task
	sb_task=$(api POST "/api/tasks" '{"title":"In-sandbox health parity check","repo":"acme/widgets","grants":["https://grain.dev/capabilities/api#self-debug"],"approved":true}' | jq -r '.id')
	for _ in $(seq 80); do
		local st
		st=$(api GET "/api/tasks/$sb_task" | jq -r '.state')
		if [[ "$st" == "closed" || "$st" == "awaiting_reply" || "$st" == "done" || -f "$work/captures/3-sandbox-summary.json" ]]; then
			if [[ -f "$work/captures/3-sandbox-summary.json" ]]; then
				break
			fi
		fi
		sleep 0.25
	done
	rm -f "$work/action-default.json"
	echo "[${STORE}] 3-sandbox-summary: $(jq -c . "$work/captures/3-sandbox-summary.json")"
	jq -e '
		.grain_health.rc == 0 and .grain_health.stderr == "" and
		.grain_health.capabilityServers == 1 and .grain_health.staleSchedules == 1 and .grain_health.refusedDeclarations == 1 and
		.deploy_health.rc == 0 and .deploy_health.stderr == "" and
		.deploy_health.capabilityServers == 1 and .deploy_health.staleSchedules == 1 and .deploy_health.refusedDeclarations == 1 and
		.deploy_health_json.rc == 0 and .deploy_health_json.stderr == ""
	' "$work/captures/3-sandbox-summary.json" >/dev/null

	# ------------------------------------------------------------------
	# 2f. Combined ALL FOUR FAILING sections active simultaneously:
	#     DispatchBlocked + CapabilityServers + StaleSchedules + RefusedDeclarations
	# ------------------------------------------------------------------
	stop_daemon "$work"
	aligned_base=$(find_free_aligned_port_base)
	socat "TCP-LISTEN:${aligned_base},bind=127.0.0.1,fork,reuseaddr" /dev/null </dev/null >/dev/null 2>&1 &
	echo $! >"$work/squat_gitproxy.pid"
	sleep 0.2
	PORT_BASE="$aligned_base" start_daemon "$work"
	for _ in $(seq 80); do
		if api GET "/api/sandboxes" | jq -e '(.dispatchBlocked | length == 1) and (.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)' >/dev/null 2>&1; then
			break
		fi
		sleep 0.2
	done

	capture_health "2f-failing-all4" health
	capture_health "2f-failing-all4-json" health -json
	record_summary "2f-failing-all4" '(.dispatchBlocked | length == 1) and (.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)'
	assert_eq "2f text rc" "$(cat "$work/captures/2f-failing-all4.rc")" "1"
	assert_eq "2f text stderr" "$(cat "$work/captures/2f-failing-all4.stderr")" "grain: health: failing: dispatch is blocked, 1 capability server(s) out of use, 1 stale schedule(s), 1 repository declaration set(s) not read in full"
	assert_contains "2f dispatch section" "$(cat "$work/captures/2f-failing-all4.stdout")" "FAILING: dispatch is blocked"
	assert_contains "2f cap section" "$(cat "$work/captures/2f-failing-all4.stdout")" "FAILING: capability servers out of use"
	assert_contains "2f sched section" "$(cat "$work/captures/2f-failing-all4.stdout")" "FAILING: schedules with no run in more than twice their cadence:"
	assert_contains "2f decl section" "$(cat "$work/captures/2f-failing-all4.stdout")" "FAILING: repository declarations not read in full:"
	assert_not_contains "2f no counters suppressed" "$(cat "$work/captures/2f-failing-all4.stdout")" "no counters"
	assert_eq "2f json rc" "$(cat "$work/captures/2f-failing-all4-json.rc")" "1"
	assert_eq "2f json stderr" "$(cat "$work/captures/2f-failing-all4-json.stderr")" ""
	jq -e '(.dispatchBlocked | length == 1) and (.capabilityServers | length == 1) and (.staleSchedules | length == 1) and (.refusedDeclarations | length == 1)' "$work/captures/2f-failing-all4-json.stdout" >/dev/null

	stop_env "$work"
	echo "[${STORE}] ALL CHECKS PASSED"
}

if [[ -n ${STORE:-} ]]; then
	run_for_store "$STORE"
else
	run_for_store sqlite
	run_for_store postgres
fi
