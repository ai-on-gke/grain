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

# End-to-end walkthrough of §6.3:
#   Part A (PR #912 — 1-hour system-schedule first-firing grace):
#     1. Boot fresh `grain daemon` (-max-workers 1 -sandbox-port-base=0) on an
#        empty SQLite DB under faketime at 2026-11-09 02:59:30 UTC (x10).
#     2. Create user daily schedules firing at 03:00 UTC and 03:02 UTC against a
#        local git-http-backend repo (acme/widgets).
#     3. Advance across 03:00:00..03:05:00 UTC: verify all seeded system:*
#        schedules get NextRunAt >= +1h (zero system:* firings across 03:00..03:05),
#        while the user schedules fire at 03:00 & 03:02 UTC and succeed cleanly.
#     4. Also set one existing system schedule's NextRunAt to 2026-11-09T04:10:00Z
#        (<1h after 03:50:00 UTC), restart the daemon at 2026-11-09 03:50:00 UTC on
#        the existing SQLite DB, and verify persisted NextRunAt values on ALL
#        system:* rows are preserved unchanged (never pushed forward again).
#   Part B (PR #915 — `grain-agentd` placement refusal diagnostics & zero `hostd` leaks):
#     5. Enroll a real `grain-agentd` ("agentd-walk") via `grain host invite` +
#        `grain-agentd -join` alongside a co-located `grain host`.
#     6. Attempt via REST (`PUT /api/capability-servers`, `POST /api/capability-servers/test`),
#        pre-placed runtime attach, and Playwright Chromium UI (`/settings/capabilities`)
#        to place a capability server onto `agentd-walk`; verify clear `grain-agentd` /
#        `grain host` / `"the host agent refused:"` diagnostics and zero `hostd` leaks.

set -euo pipefail

GRAIN=${GRAIN:-/home/debian/bin/grain}
AGENTD=${AGENTD:-/home/debian/bin/grain-agentd}
ROOT=/tmp/walk-s6-sched-agentd
rm -rf "$ROOT"
mkdir -p "$ROOT/data/secrets/github" "$ROOT/sandboxes" "$ROOT/bin" "$ROOT/git-root" \
	"$ROOT/host-local/state" "$ROOT/agentd-home/.config/grain" "$ROOT/agentd-state"

UI_ADDR="127.0.0.1:4290"
GH_HOST="127.0.0.1:4291"
API_TOKEN="walk-s6-sched-token"
LOCAL_SOCK="$ROOT/host-local/hostd.sock"

cleanup() {
	for pf in "$ROOT/agentd.pid" "$ROOT/host.pid" "$ROOT/daemon.pid" "$ROOT/upstreams.pid"; do
		if [ -f "$pf" ]; then
			kill "$(cat "$pf")" 2>/dev/null || true
		fi
	done
	sudo rm -f "$ROOT/host-local/hostd.yaml" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Building fake_upstreams & faketime_go_exec ==="
(cd /home/debian/work && \
	go build -o "$ROOT/bin/fake_upstreams" ./docs/test-runs/2026-11-08-daemon-runtime-resilience/fake_upstreams.go)
gcc -O2 -Wall -Wextra -o "$ROOT/bin/faketime_go_exec" \
	/home/debian/work/docs/test-runs/2026-11-08-daemon-runtime-resilience/faketime_go_exec.c

# Create bare git repo acme/widgets.git for user schedule tasks to clone
git init --bare -q --initial-branch=main "$ROOT/git-root/acme/widgets.git"
WORK_TMP="$ROOT/widgets-init"
git clone -q "$ROOT/git-root/acme/widgets.git" "$WORK_TMP"
(
	cd "$WORK_TMP"
	git config user.name "walk"
	git config user.email "walk@example.invalid"
	echo "hello widgets" >README.md
	git add README.md
	git commit -q -m "initial commit"
	git push -q origin main
)
rm -rf "$WORK_TMP"

"$ROOT/bin/fake_upstreams" \
	-sm-addr 127.0.0.1:4292 \
	-gcs-addr 127.0.0.1:4293 \
	-gh-addr "$GH_HOST" \
	-git-root "$ROOT/git-root" >"$ROOT/upstreams.log" 2>&1 &
echo $! >"$ROOT/upstreams.pid"

cat >"$ROOT/bin/agy" <<'EOF'
#!/bin/sh
echo '{"event":"init","init":{"cwd":"/w","tools":["run_command"],"permission_mode":"bypass"}}'
echo '{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"command":"echo ok"}}}}'
echo '{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":"ok"}}}'
echo '{"event":"result","result":{"status":"SUCCESS","response":"Completed user scheduled job ok."}}'
EOF
chmod 755 "$ROOT/bin/agy"

printf '%s\n' "$API_TOKEN" >"$ROOT/data/local-api-token"
chmod 600 "$ROOT/data/local-api-token"
printf '{"directConfigWrites": true}\n' >"$ROOT/data/state-repo.json"
printf 'walkthrough-dummy-gemini-key\n' >"$ROOT/gemini-key"
printf 'dummy\n' | "$GRAIN" secrets -data-dir "$ROOT/data" set walkthrough-init key >/dev/null

LIBFAKETIME="/usr/lib/x86_64-linux-gnu/faketime/libfaketimeMT.so.1"

echo "=== PART A.1: Booting fresh grain daemon at faketime 2026-11-09 02:59:30 UTC (x10) ==="
LD_PRELOAD="$LIBFAKETIME" FAKETIME="@2026-11-09 02:59:30 x10" \
	FAKETIME_NO_CACHE=1 FAKETIME_DONT_FAKE_MONOTONIC=1 \
	GCE_METADATA_HOST=127.0.0.1:1 PATH="$ROOT/bin:$PATH" \
	"$ROOT/bin/faketime_go_exec" "$GRAIN" daemon \
	-data-dir "$ROOT/data" \
	-sandbox-dir "$ROOT/sandboxes" \
	-host-sandboxes \
	-sandbox-port-base=0 \
	-ui-addr "$UI_ADDR" \
	-as walkthrough-owner \
	-poll-interval 1s \
	-max-workers 1 \
	-max-mergers 0 \
	-agy-path "$ROOT/bin/agy" \
	-gemini-api-key-file "$ROOT/gemini-key" \
	-github-host "$GH_HOST" \
	-github-insecure-http \
	>"$ROOT/daemon-boot1.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 100); do
	if curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/schedules" >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
done

api_call() {
	local method=$1 path=$2 body=${3:-}
	local out="$ROOT/last_api.json"
	local code
	if [ -n "$body" ]; then
		code=$(curl -sS -o "$out" -w "%{http_code}" -X "$method" \
			-H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
			-d "$body" "http://$UI_ADDR$path")
	else
		code=$(curl -sS -o "$out" -w "%{http_code}" -X "$method" \
			-H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR$path")
	fi
	if [ "$code" -ge 400 ]; then
		echo "API ERROR $method $path -> HTTP $code: $(cat "$out")" >&2
		return 1
	fi
	cat "$out"
}

# Configure stub-agy backend immediately (we are around 02:59:35..02:59:45 fake UTC!)
api_call PUT /api/deployment/agent-backends/stub-agy \
	"{\"id\":\"stub-agy\",\"name\":\"Stub Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$ROOT/bin/agy\",\"models\":[\"gemini-2.5-pro\"]}" >/dev/null
api_call PUT /api/agent-backends/stub-agy/credential \
	'{"value":"walkthrough-dummy-gemini-key"}' >/dev/null
api_call PUT /api/github-tokens/default \
	'{"value":"ghp_walkthrough_stub_token"}' >/dev/null

# Register acme/widgets as an allowed target repo backed by local git-http-backend
api_call POST /api/repos "{\"repo\":\"acme/widgets\"}" >/dev/null

# Create two user daily schedules due inside 03:00..03:05 UTC (03:00 and 03:02 UTC)
api_call POST /api/schedules \
	'{"title":"User 03:00 job","description":"Run user job at 03:00 UTC","repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"03:00"}}' >"$ROOT/user_sched_1.json"
api_call POST /api/schedules \
	'{"title":"User 03:02 job","description":"Run user job at 03:02 UTC","repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"03:02"}}' >"$ROOT/user_sched_2.json"

echo "Created user schedules; waiting ~36 real seconds (~6 fake minutes, advancing past 03:05:00 UTC)..."
sleep 36

curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/schedules" >"$ROOT/schedules_after_0305.json"
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/tasks" >"$ROOT/tasks_after_0305.json"

# Gracefully stop daemon #1
kill -TERM "$(cat "$ROOT/daemon.pid")"
wait "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
rm -f "$ROOT/daemon.pid"

echo "=== Verifying SQLite schedule & task rows after crossing 03:00:00..03:05:00 UTC ==="
sqlite3 "$ROOT/data/store/grain.db" "SELECT id, recurrence_kind, next_run_at, last_run_at FROM schedule ORDER BY id;" >"$ROOT/sqlite_sched_boot1.txt"
cat "$ROOT/sqlite_sched_boot1.txt"

python3 - "$ROOT/schedules_after_0305.json" "$ROOT/tasks_after_0305.json" "$ROOT/data/store/grain.db" <<'PY'
import json, sqlite3, sys

scheds = json.load(open(sys.argv[1]))
tasks = json.load(open(sys.argv[2]))
con = sqlite3.connect(sys.argv[3])

sys_scheds = [s for s in scheds if s["id"].startswith("system:")]
user_scheds = [s for s in scheds if not s["id"].startswith("system:")]
assert len(sys_scheds) >= 5, f"expected >=5 seeded system schedules, got {sys_scheds}"

for s in sys_scheds:
    assert s.get("lastRunAt") in (None, ""), f"system schedule {s['id']} fired within first hour! {s}"
    assert s["nextRunAt"] >= "2026-11-09T03:59:30Z", f"system schedule {s['id']} nextRunAt < +1h grace: {s['nextRunAt']}"

for s in user_scheds:
    assert s.get("lastRunAt") not in (None, ""), f"user schedule {s['id']} did not fire! {s}"

scheduled_tasks = [t for t in tasks if t.get("scheduled")]
assert len(scheduled_tasks) == 2, f"expected only the 2 user schedules to file tasks, got {scheduled_tasks}"
assert all(t["title"].startswith("User 03:0") for t in scheduled_tasks), scheduled_tasks

runs = list(con.execute("SELECT id, outcome FROM task_run WHERE id NOT LIKE '1-%' ORDER BY id"))
print("Scheduled task runs completed during 03:00..03:05 UTC:", runs)
assert len(runs) == 2 and all(r[1] in ("succeeded", "nothing_needed", "completed", "done") for r in runs), runs
print(f"PASS Part A.1: {len(sys_scheds)} system schedules held past +1h grace (0 firings), {len(scheduled_tasks)} user schedule tasks fired & completed ({[r[1] for r in runs]})!")
PY

echo "=== PART A.2: Testing restart at 2026-11-09 03:50:00 UTC preserves persisted NextRunAt ==="
# Set system:memory-agent's persisted next_run_at to 2026-11-09 04:10:00 +0000 UTC (only 20m after 03:50:00 UTC!)
# so we ALSO prove a <1h persisted NextRunAt is NOT pushed forward on restart!
sqlite3 "$ROOT/data/store/grain.db" \
	"UPDATE schedule SET next_run_at = '2026-11-09 04:10:00 +0000 UTC' WHERE id = 'system:memory-agent';"
sqlite3 "$ROOT/data/store/grain.db" \
	"SELECT id, next_run_at FROM schedule WHERE id LIKE 'system:%' ORDER BY id;" >"$ROOT/before_restart_next.txt"

# Note: also remove state-repo schedule dump if state-repo exported before our direct SQLite test edit, or let's update via API before stopping!
# Wait: on daemon boot, loadStateRepo only runs when SQLite DB is empty! Let's verify!
LD_PRELOAD="$LIBFAKETIME" FAKETIME="@2026-11-09 03:50:00" \
	FAKETIME_NO_CACHE=1 FAKETIME_DONT_FAKE_MONOTONIC=1 \
	GCE_METADATA_HOST=127.0.0.1:1 PATH="$ROOT/bin:$PATH" \
	"$ROOT/bin/faketime_go_exec" "$GRAIN" daemon \
	-data-dir "$ROOT/data" \
	-sandbox-dir "$ROOT/sandboxes" \
	-host-sandboxes \
	-sandbox-port-base=0 \
	-ui-addr "$UI_ADDR" \
	-as walkthrough-owner \
	-poll-interval 1s \
	-max-workers 1 \
	-max-mergers 0 \
	-agy-path "$ROOT/bin/agy" \
	-gemini-api-key-file "$ROOT/gemini-key" \
	-github-host "$GH_HOST" \
	-github-insecure-http \
	>"$ROOT/daemon-boot2.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 100); do
	if curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/schedules" >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
done
# Wait 3 reconcile cycles at 03:50:00..03:50:03 UTC so reconcileSystemSchedules runs!
sleep 3.5

sqlite3 "$ROOT/data/store/grain.db" \
	"SELECT id, next_run_at FROM schedule WHERE id LIKE 'system:%' ORDER BY id;" >"$ROOT/after_restart_next.txt"

echo "Before 03:50:00 UTC restart:"
cat "$ROOT/before_restart_next.txt"
echo "After 03:50:00 UTC restart (3 reconcile cycles):"
cat "$ROOT/after_restart_next.txt"
diff -u "$ROOT/before_restart_next.txt" "$ROOT/after_restart_next.txt"
echo "PASS Part A.2: persisted NextRunAt timestamps on all system:* rows preserved byte-for-byte across restart!"

kill -TERM "$(cat "$ROOT/daemon.pid")"
wait "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
rm -f "$ROOT/daemon.pid"

echo "=== PART B: Live grain-agentd placement refusal diagnostics (REST, CLI, & Playwright UI) ==="
cat >"$ROOT/hostd.yaml.tmp" <<EOF
clis:
  agy:
    path: $ROOT/bin/agy
    cwd_under: [/tmp]
capabilities:
  walk-echo:
    command: /bin/cat
EOF
sudo install -o root -g root -m 0644 "$ROOT/hostd.yaml.tmp" "$ROOT/host-local/hostd.yaml"
rm -f "$ROOT/hostd.yaml.tmp"

"$GRAIN" host -allowlist "$ROOT/host-local/hostd.yaml" -socket "$LOCAL_SOCK" -state-dir "$ROOT/host-local/state" >"$ROOT/host.log" 2>&1 &
echo $! >"$ROOT/host.pid"
for _ in $(seq 1 50); do
	[ -S "$LOCAL_SOCK" ] && break
	sleep 0.1
done

GCE_METADATA_HOST=127.0.0.1:1 PATH="$ROOT/bin:$PATH" "$GRAIN" daemon \
	-data-dir "$ROOT/data" \
	-sandbox-dir "$ROOT/sandboxes" \
	-host-sandboxes \
	-sandbox-port-base=0 \
	-ui-addr "$UI_ADDR" \
	-as walkthrough-owner \
	-poll-interval 1s \
	-max-workers 1 \
	-max-mergers 0 \
	-agy-path "$ROOT/bin/agy" \
	-gemini-api-key-file "$ROOT/gemini-key" \
	-github-host "$GH_HOST" \
	-github-insecure-http \
	-hostd-socket "$LOCAL_SOCK" \
	>"$ROOT/daemon-partb.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 100); do
	if curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/hostds" >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
done

# 1. Create enrollment invite via CLI `grain host invite`
INVITE_OUT=$(GRAIN_SERVER="http://$UI_ADDR" GRAIN_TOKEN="$API_TOKEN" GRAIN_API_TOKEN="$API_TOKEN" "$GRAIN" host invite -name walk-agentd-code)
echo "Invite snippet:"
echo "$INVITE_OUT"
JOIN_CODE=$(printf '%s\n' "$INVITE_OUT" | grep -oE 'ghe_[A-Za-z0-9_-]+' | head -n 1)
echo "Extracted join code: ${JOIN_CODE:0:10}..."

# 2. Verify `grain-agentd -check` refuses a capabilities: section in its own allowlist
cat >"$ROOT/bad-agentd.yaml" <<EOF
clis:
  agy:
    path: $ROOT/bin/agy
    cwd_under: [/tmp]
capabilities:
  walk-echo:
    command: /bin/cat
EOF
chmod 600 "$ROOT/bad-agentd.yaml"
if "$AGENTD" -allowlist "$ROOT/bad-agentd.yaml" -check >"$ROOT/agentd_check.out" 2>&1; then
	echo "FAIL: grain-agentd -check unexpectedly accepted capabilities: section!" >&2
	exit 1
fi
echo "grain-agentd -check refusal: $(cat "$ROOT/agentd_check.out")"

# 3. Enroll real `grain-agentd` ("agentd-walk") via -join
cat >"$ROOT/agentd-home/.config/grain/agentd.yaml" <<EOF
clis:
  agy:
    path: $ROOT/bin/agy
    cwd_under: [/tmp]
EOF
chmod 700 "$ROOT/agentd-home" "$ROOT/agentd-home/.config" "$ROOT/agentd-home/.config/grain"
chmod 600 "$ROOT/agentd-home/.config/grain/agentd.yaml"

HOME="$ROOT/agentd-home" "$AGENTD" \
	-server "http://$UI_ADDR" \
	-join "$JOIN_CODE" \
	-name "agentd-walk" \
	-allowlist "$ROOT/agentd-home/.config/grain/agentd.yaml" \
	-key-file "$ROOT/agentd-home/.config/grain/agentd-key" \
	-token-file "$ROOT/agentd-home/.config/grain/agentd-token" \
	-socket "$ROOT/agentd-state/agentd.sock" \
	-state-dir "$ROOT/agentd-state" \
	>"$ROOT/agentd.log" 2>&1 &
echo $! >"$ROOT/agentd.pid"

# Wait for pending join and accept it as "agentd-walk"
PENDING_ID=""
for _ in $(seq 1 50); do
	PENDING_ID=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/hostds/pending" | \
		python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")')
	[ -n "$PENDING_ID" ] && break
	sleep 0.2
done
[ -n "$PENDING_ID" ] || { echo "FAIL: pending join did not arrive; agentd.log:"; cat "$ROOT/agentd.log"; exit 1; }

curl -fsS -X POST -H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
	-d '{"name":"agentd-walk"}' \
	"http://$UI_ADDR/api/hostds/pending/$PENDING_ID/accept" >"$ROOT/accepted_host.json"

# Wait until agentd-walk is connected
for _ in $(seq 1 50); do
	CONN=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/hostds" | \
		python3 -c 'import json,sys; hs=[h for h in json.load(sys.stdin) if h["name"]=="agentd-walk"]; print("1" if hs and hs[0].get("connected") else "0")')
	[ "$CONN" = "1" ] && break
	sleep 0.2
done
echo "Confirmed agentd-walk connected!"

# 4. Pre-place a capability server while agentd-walk is briefly paused/disconnected so we also test runtime attach refusal!
kill "$(cat "$ROOT/agentd.pid")" 2>/dev/null || true
wait "$(cat "$ROOT/agentd.pid")" 2>/dev/null || true
for _ in $(seq 1 50); do
	CONN=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/hostds" | \
		python3 -c 'import json,sys; hs=[h for h in json.load(sys.stdin) if h["name"]=="agentd-walk"]; print("1" if hs and hs[0].get("connected") else "0")')
	[ "$CONN" = "0" ] && break
	sleep 0.1
done

curl -fsS -X PUT -H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
	-d '{"id":"https://walk.example/cap/preplaced","config":{"command":"walk-echo","run":"host","hostd":"agentd-walk"}}' \
	"http://$UI_ADDR/api/capability-servers" >/dev/null

# Reconnect real grain-agentd using its saved key-bound credential
HOME="$ROOT/agentd-home" "$AGENTD" \
	-server "http://$UI_ADDR" \
	-name "agentd-walk" \
	-allowlist "$ROOT/agentd-home/.config/grain/agentd.yaml" \
	-key-file "$ROOT/agentd-home/.config/grain/agentd-key" \
	-token-file "$ROOT/agentd-home/.config/grain/agentd-token" \
	-socket "$ROOT/agentd-state/agentd.sock" \
	-state-dir "$ROOT/agentd-state" \
	>"$ROOT/agentd2.log" 2>&1 &
echo $! >"$ROOT/agentd.pid"

for _ in $(seq 1 50); do
	CONN=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/hostds" | \
		python3 -c 'import json,sys; hs=[h for h in json.load(sys.stdin) if h["name"]=="agentd-walk"]; print("1" if hs and hs[0].get("connected") else "0")')
	[ "$CONN" = "1" ] && break
	sleep 0.2
done

# Wait for pre-placed server status to record the grain-agentd / grain host attach refusal
for _ in $(seq 1 30); do
	curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/capability-servers" >"$ROOT/cap_servers_list.json"
	if grep -q "runs agent CLIs only (grain-agentd)" "$ROOT/cap_servers_list.json"; then
		break
	fi
	sleep 0.3
done

# Also verify live RefusedError ("the host agent refused:") directly against the running grain-agentd unix socket!
cat >"$ROOT/check_refused.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := hostd.Client{Socket: os.Args[1]}
	_, err := c.Run(ctx, hostd.Request{Kind: hostd.KindCapability, Name: "walk-echo"}, nil, nil, nil)
	if err == nil {
		os.Exit(1)
	}
	fmt.Println(err.Error())
}
GO
REFUSED_LIVE=$(cd /home/debian/work && go run "$ROOT/check_refused.go" "$ROOT/agentd-state/agentd.sock")
echo "Live grain-agentd RefusedError -> $REFUSED_LIVE"

# 5. Test REST PUT /api/capability-servers and POST /api/capability-servers/test while agentd-walk is connected
PUT_CODE=$(curl -sS -o "$ROOT/put_refusal.json" -w "%{http_code}" -X PUT \
	-H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
	-d '{"id":"https://walk.example/cap/live","config":{"command":"walk-echo","run":"host","hostd":"agentd-walk"}}' \
	"http://$UI_ADDR/api/capability-servers")
echo "REST PUT /api/capability-servers -> HTTP $PUT_CODE: $(cat "$ROOT/put_refusal.json")"

TEST_CODE=$(curl -sS -o "$ROOT/test_refusal.json" -w "%{http_code}" -X POST \
	-H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
	-d '{"id":"https://walk.example/cap/live","config":{"command":"walk-echo","run":"host","hostd":"agentd-walk"}}' \
	"http://$UI_ADDR/api/capability-servers/test")
echo "REST POST /api/capability-servers/test -> HTTP $TEST_CODE: $(cat "$ROOT/test_refusal.json")"

python3 - "$PUT_CODE" "$ROOT/put_refusal.json" "$TEST_CODE" "$ROOT/test_refusal.json" "$ROOT/cap_servers_list.json" "$REFUSED_LIVE" <<'PY'
import json, re, sys

put_code = int(sys.argv[1])
put_body = json.load(open(sys.argv[2]))
test_code = int(sys.argv[3])
test_body = json.load(open(sys.argv[4]))
cap_list = json.load(open(sys.argv[5]))
refused_live = sys.argv[6]

for label, code, body in (("PUT", put_code, put_body), ("POST /test", test_code, test_body)):
    assert code == 400, (label, code)
    err_msg = body.get("error", "")
    assert "runs agent CLIs only (grain-agentd)" in err_msg, (label, err_msg)
    assert "enrolled with `grain host`" in err_msg, (label, err_msg)
    assert not re.search(r'(?<![/\w.-])hostds?(?![\w./-])', err_msg, re.I), f"hostd leak in {label}: {err_msg}"

pre = [s for s in cap_list if s["id"] == "https://walk.example/cap/preplaced"][0]
st_err = pre.get("status", {}).get("error", "")
assert "runs agent CLIs only (grain-agentd)" in st_err and "grain host" in st_err, pre
assert not re.search(r'(?<![/\w.-])hostds?(?![\w./-])', st_err, re.I), f"hostd leak in status error: {st_err}"

assert refused_live.startswith("the host agent refused: "), refused_live
assert not re.search(r'(?<![/\w.-])hostds?(?![\w./-])', refused_live, re.I), f"hostd leak in RefusedError: {refused_live}"
print("PASS REST placement diagnostics, pre-placed attach refusal & live RefusedError hygiene!")
PY

# 6. Run Playwright Chromium UI verification on /settings/hostds and /settings/capabilities
(
	cd /home/debian/work/ui && \
	GRAIN_URL="http://$UI_ADDR" \
	GRAIN_CODE="$API_TOKEN" \
	AGENTD_NAME="agentd-walk" \
	PREPLACED_ID="https://walk.example/cap/preplaced" \
	node ../docs/test-runs/2026-11-08-daemon-runtime-resilience/check_agentd_ui.mjs
)

# 7. Run CLI host-wording regression tests
(cd /home/debian/work && go test ./pkg/cli/... -run TestEveryCommandsHelpSaysHostNotHostd && go test ./cmd/grain-agentd/...)

echo "=== DONE walk-schedules-agentd.sh ==="
