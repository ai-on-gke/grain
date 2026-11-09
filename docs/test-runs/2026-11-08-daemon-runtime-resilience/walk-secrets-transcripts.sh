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

# End-to-end walkthrough of §6.1:
#   - Live -secrets-backend=secretmanager + -object-store on a real `grain daemon`
#   - Out-of-band 404 (deleted) and 400 FAILED_PRECONDITION (DESTROYED) in Secret Manager
#   - `grain secrets list` `(missing in Secret Manager)` annotation + stderr hint + `grain secrets delete`
#   - ErrGoneUpstream resilience during task transcript redaction & upload
#   - Fail-closed redaction on Secret Manager 503 & 403 (zero unredacted bytes uploaded or served)
#   - Raw `.jsonl` retention in `transcripts.unflushed/` across SM 503/403 & GCS 503, and automatic retry recovery

set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
ROOT=/tmp/walk-s6-secrets
rm -rf "$ROOT"
mkdir -p "$ROOT/data/secrets/github" "$ROOT/sandboxes" "$ROOT/bin"

ACTIVE_SECRET="super-secret-active-val-998877"
SM_PREFIX="projects/test-proj/secrets/grain-s6-"
SM_TOKEN="fake-sm-token"
SM_URL="http://127.0.0.1:4281"
GCS_URL="http://127.0.0.1:4282"
GH_HOST="127.0.0.1:4283"
UI_ADDR="127.0.0.1:4280"
API_TOKEN="walk-s6-local-api-token"

cleanup() {
	if [ -f "$ROOT/daemon.pid" ]; then
		kill "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
	fi
	if [ -f "$ROOT/upstreams.pid" ]; then
		kill "$(cat "$ROOT/upstreams.pid")" 2>/dev/null || true
	fi
}
trap cleanup EXIT

echo "=== Building fake_upstreams ==="
(cd /home/debian/work && go build -o "$ROOT/bin/fake_upstreams" ./docs/test-runs/2026-11-08-daemon-runtime-resilience/fake_upstreams.go)

"$ROOT/bin/fake_upstreams" \
	-sm-addr 127.0.0.1:4281 \
	-sm-token "$SM_TOKEN" \
	-sm-prefix "$SM_PREFIX" \
	-gcs-addr 127.0.0.1:4282 \
	-gcs-bucket grain-s6-bucket \
	-forbidden-secret "$ACTIVE_SECRET" \
	-gh-addr "$GH_HOST" \
	>"$ROOT/upstreams.log" 2>&1 &
echo $! >"$ROOT/upstreams.pid"

for _ in $(seq 1 50); do
	if curl -fsS "$SM_URL/control/stats" >/dev/null 2>&1 && curl -fsS "$GCS_URL/control/objects" >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
done

printf '%s\n' "$API_TOKEN" >"$ROOT/data/local-api-token"
chmod 600 "$ROOT/data/local-api-token"
printf '{"directConfigWrites": true}\n' >"$ROOT/data/state-repo.json"
printf 'git:\n  - credential: anonymous\n' >"$ROOT/data/connections.yaml"
printf 'walkthrough-dummy-gemini-key\n' >"$ROOT/gemini-key"

cat >"$ROOT/bin/agy" <<EOF
#!/bin/sh
echo '{"event":"init","init":{"cwd":"/w","tools":["run_command"],"permission_mode":"bypass"}}'
echo '{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"command":"echo token"}}}}'
echo '{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":"token=${ACTIVE_SECRET}"}}}'
echo '{"event":"result","result":{"status":"SUCCESS","response":"Completed check with token ${ACTIVE_SECRET} ok."}}'
EOF
chmod 755 "$ROOT/bin/agy"

export GRAIN_SECRET_MANAGER_ENDPOINT="$SM_URL"
export GRAIN_SECRET_MANAGER_TOKEN="$SM_TOKEN"
export GRAIN_OBJECT_STORE_GCS_ENDPOINT="$GCS_URL"

echo "=== Starting grain daemon (-secrets-backend=secretmanager -object-store=gs://...) ==="
PATH="$ROOT/bin:$PATH" "$GRAIN" daemon \
	-data-dir "$ROOT/data" \
	-sandbox-dir "$ROOT/sandboxes" \
	-host-sandboxes \
	-sandbox-port-base=0 \
	-ui-addr "$UI_ADDR" \
	-as walkthrough-owner \
	-poll-interval 250ms \
	-max-workers 1 \
	-max-mergers 0 \
	-agy-path "$ROOT/bin/agy" \
	-gemini-api-key-file "$ROOT/gemini-key" \
	-github-host "$GH_HOST" \
	-github-insecure-http \
	-secrets-backend secretmanager \
	-secrets-sm-prefix "$SM_PREFIX" \
	-object-store "gs://grain-s6-bucket/instances/walk-s6/" \
	>"$ROOT/daemon.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 100); do
	if curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/tasks" >/dev/null 2>&1; then
		break
	fi
	sleep 0.2
done
echo "daemon up (pid $(cat "$ROOT/daemon.pid"))"

echo "=== Configuring stub-agy agent backend ==="
curl -fsS -X PUT "http://$UI_ADDR/api/deployment/agent-backends/stub-agy" \
	-H "Authorization: Bearer $API_TOKEN" \
	-H "Content-Type: application/json" \
	-d '{"id":"stub-agy","name":"Stub Agy","framework":"antigravity","models":["gemini-2.5-pro"],"enabled":true}' >/dev/null
curl -fsS -X PUT "http://$UI_ADDR/api/agent-backends/stub-agy/credential" \
	-H "Authorization: Bearer $API_TOKEN" \
	-H "Content-Type: application/json" \
	-d '{"value":"walkthrough-dummy-gemini-key"}' >/dev/null

echo "=== Storing 3 secrets via grain secrets set ==="
printf '%s' "$ACTIVE_SECRET" | "$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" set active-token value
printf 'deleted-val-111' | "$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" set deleted-upstream value
printf 'destroyed-val-222' | "$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" set destroyed-version value

echo "=== Out-of-band: deleting deleted-upstream (404) & destroying destroyed-version (400 FAILED_PRECONDITION) ==="
curl -fsS -X POST "$SM_URL/control/delete-secret?resource=${SM_PREFIX}deleted-upstream"
curl -fsS -X POST "$SM_URL/control/destroy-all-versions?resource=${SM_PREFIX}destroyed-version"

echo "=== Running grain secrets list ==="
"$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" list \
	>"$ROOT/list1.stdout" 2>"$ROOT/list1.stderr"
echo "--- stdout ---"
cat "$ROOT/list1.stdout"
echo "--- stderr ---"
cat "$ROOT/list1.stderr"

grep -q 'active-token: value$' "$ROOT/list1.stdout"
grep -q 'deleted-upstream: value (missing in Secret Manager)' "$ROOT/list1.stdout"
grep -q 'destroyed-version: value (missing in Secret Manager)' "$ROOT/list1.stdout"
grep -q '2 catalogued key(s) are gone from Secret Manager' "$ROOT/list1.stderr"
grep -q 'grain secrets delete <secret> <key>' "$ROOT/list1.stderr"

echo "=== Reconciling deleted-upstream with grain secrets delete ==="
"$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" delete deleted-upstream value
"$GRAIN" secrets -data-dir "$ROOT/data" -secrets-backend secretmanager -secrets-sm-prefix "$SM_PREFIX" list \
	>"$ROOT/list2.stdout" 2>"$ROOT/list2.stderr"
echo "--- stdout after delete ---"
cat "$ROOT/list2.stdout"
echo "--- stderr after delete ---"
cat "$ROOT/list2.stderr"
if grep -q 'deleted-upstream' "$ROOT/list2.stdout"; then
	echo "FAIL: deleted-upstream still in list after delete" >&2
	exit 1
fi
grep -q '1 catalogued key(s) are gone from Secret Manager' "$ROOT/list2.stderr"

file_task_and_wait() {
	local title="$1"
	local resp tid
	resp=$(curl -sS -X POST "http://$UI_ADDR/api/tasks" \
		-H "Authorization: Bearer $API_TOKEN" \
		-H "Content-Type: application/json" \
		-d "{\"title\":\"$title\",\"description\":\"run check\",\"noRepo\":true,\"approved\":true,\"agentFramework\":\"antigravity\",\"capabilities\":[]}")
	tid=$(python3 -c 'import json,sys; d=json.load(sys.stdin); assert "id" in d, d; print(d["id"])' <<<"$resp")
	for _ in $(seq 1 80); do
		local run_id
		run_id=$(sqlite3 "$ROOT/data/store/grain.db" "SELECT id FROM task_run WHERE task_id='$tid' AND finished_at IS NOT NULL ORDER BY attempt DESC LIMIT 1;" 2>/dev/null || true)
		if [ -n "$run_id" ] && [ ! -f "$ROOT/data/state/transcripts/$run_id" ]; then
			sleep 0.3
			echo "$tid $run_id"
			return 0
		fi
		sleep 0.25
	done
	echo "FAIL: task $tid never finished" >&2
	return 1
}

echo "=== Dispatching Task 1 (ErrGoneUpstream on destroyed-version + active-token redaction) ==="
read -r T1 R1 < <(file_task_and_wait "task-1-gone-resilience")
echo "Task 1=$T1 Run 1=$R1"

curl -fsS "$GCS_URL/control/objects" >"$ROOT/gcs_after_t1.json"
python3 - "$ROOT/gcs_after_t1.json" "$R1" "$ACTIVE_SECRET" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
r1, secret = sys.argv[2], sys.argv[3]
assert data["leakCount"] == 0, f"LEAK DETECTED: {data}"
objs = data["objects"]
jsonl_key = f"instances/walk-s6/transcripts/{r1}.jsonl"
md_key = f"instances/walk-s6/transcripts/{r1}.md"
assert jsonl_key in objs, f"missing {jsonl_key} in {list(objs)}"
assert md_key in objs, f"missing {md_key} in {list(objs)}"
assert "[redacted: secret active-token/value]" in objs[jsonl_key] and secret not in objs[jsonl_key], objs[jsonl_key]
assert "[redacted: secret active-token/value]" in objs[md_key] and secret not in objs[md_key], objs[md_key]
print("PASS Task 1: both .jsonl and .md uploaded with [redacted: secret active-token/value] and 0 leaks!")
PY

echo "=== Waiting 31s for active-token 30s Secret Manager cache TTL to expire ==="
sleep 31

echo "=== Injecting SM 503 on active-token + GCS 503 on PUT, then dispatching Task 2 ==="
curl -fsS -X POST "$SM_URL/control/set-access-status?resource=${SM_PREFIX}active-token&status=503"
curl -fsS -X POST "$GCS_URL/control/set-put-status?status=503"

read -r T2 R2 < <(file_task_and_wait "task-2-sm-503-fail-closed")
echo "Task 2=$T2 Run 2=$R2"

echo "Checking unflushed dir and SQLite/UI transcript while SM=503 & GCS=503..."
ls -la "$ROOT/data/state/transcripts.unflushed"
test -f "$ROOT/data/state/transcripts.unflushed/$R2"
test -f "$ROOT/data/state/transcripts.unflushed/$R2.md"
if [ -f "$ROOT/data/state/transcripts/$R2" ]; then
	echo "FAIL: expected live file $R2 to be unlinked after run end" >&2
	exit 1
fi

curl -fsS "$GCS_URL/control/objects" >"$ROOT/gcs_after_t2_503.json"
UI_T2_DURING_503=$(curl -sS -w "\nHTTP_STATUS:%{http_code}" -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/tasks/$T2/attempts/1/transcript")
SQLITE_T2=$(sqlite3 "$ROOT/data/store/grain.db" "SELECT coalesce(length(transcript), 0), CASE WHEN coalesce(instr(transcript, '$ACTIVE_SECRET'), 0) > 0 THEN 'LEAKED_IN_SQLITE' ELSE 'CLEAN_OR_EMPTY' END FROM task_run WHERE id='$R2';")
echo "SQLite check for $R2: $SQLITE_T2"
if [ "$SQLITE_T2" != "0|CLEAN_OR_EMPTY" ]; then
	echo "FAIL: expected empty SQLite transcript column during fail-closed FlushRunEnd, got $SQLITE_T2" >&2
	exit 1
fi
if printf '%s' "$UI_T2_DURING_503" | grep -q "$ACTIVE_SECRET"; then
	echo "FAIL: UI /api/tasks/$T2/attempts/1/transcript leaked raw ACTIVE_SECRET while SM=503!" >&2
	exit 1
fi
echo "PASS UI & SQLite transcript check clean during SM=503: $(tail -n 1 <<<"$UI_T2_DURING_503")"

python3 - "$ROOT/gcs_after_t2_503.json" "$R2" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
r2 = sys.argv[2]
assert data["leakCount"] == 0, f"LEAK DETECTED: {data}"
r2_attempts = [a for a in data["putAttempts"] if r2 in a["name"]]
assert len(r2_attempts) == 0, f"expected 0 GCS PUT attempts when SM failed closed with 503, got {r2_attempts}"
print("PASS Task 2 SM 503: 0 GCS PUT attempts, 0 leaks, raw .jsonl retained in transcripts.unflushed!")
PY

echo "=== Switching SM to 403 and GCS to 200 (testing 403 fail-closed across periodic retry) ==="
curl -fsS -X POST "$SM_URL/control/set-access-status?resource=${SM_PREFIX}active-token&status=403"
curl -fsS -X POST "$GCS_URL/control/set-put-status?status=200"
sleep 6
test -f "$ROOT/data/state/transcripts.unflushed/$R2"
curl -fsS "$GCS_URL/control/objects" >"$ROOT/gcs_after_403.json"
python3 - "$ROOT/gcs_after_403.json" "$R2" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
r2 = sys.argv[2]
assert data["leakCount"] == 0
assert f"instances/walk-s6/transcripts/{r2}.jsonl" not in data["objects"], "403 must fail closed!"
print("PASS SM 403: periodic retry still failed closed, raw .jsonl still retained!")
PY

echo "=== Switching SM to 200 and GCS to 503, dispatching Task 3 (testing object-store 503 retention) ==="
curl -fsS -X POST "$SM_URL/control/set-access-status?resource=${SM_PREFIX}active-token&status=200"
curl -fsS -X POST "$GCS_URL/control/set-put-status?status=503"
read -r T3 R3 < <(file_task_and_wait "task-3-gcs-503-retain")
echo "Task 3=$T3 Run 3=$R3"
test -f "$ROOT/data/state/transcripts.unflushed/$R2"
test -f "$ROOT/data/state/transcripts.unflushed/$R3"

echo "=== Restoring GCS to 200 and waiting for periodic sync retry ==="
curl -fsS -X POST "$GCS_URL/control/set-put-status?status=200"
for _ in $(seq 1 25); do
	if [ ! -f "$ROOT/data/state/transcripts.unflushed/$R2" ] && \
		[ ! -f "$ROOT/data/state/transcripts.unflushed/$R2.md" ] && \
		[ ! -f "$ROOT/data/state/transcripts.unflushed/$R3" ] && \
		[ ! -f "$ROOT/data/state/transcripts.unflushed/$R3.md" ]; then
		break
	fi
	sleep 0.5
done

for f in "$R2" "$R2.md" "$R3" "$R3.md"; do
	if [ -f "$ROOT/data/state/transcripts.unflushed/$f" ]; then
		echo "FAIL: unflushed file $f still on disk after recovery!" >&2
		ls -la "$ROOT/data/state/transcripts.unflushed" >&2
		exit 1
	fi
done

curl -fsS "$GCS_URL/control/objects" >"$ROOT/gcs_after_recovery.json"
python3 - "$ROOT/gcs_after_recovery.json" "$R2" "$R3" "$ACTIVE_SECRET" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
r2, r3, secret = sys.argv[2], sys.argv[3], sys.argv[4]
assert data["leakCount"] == 0, f"LEAK DETECTED: {data}"
objs = data["objects"]
for rid in (r2, r3):
    for ext in (".jsonl", ".md"):
        k = f"instances/walk-s6/transcripts/{rid}{ext}"
        assert k in objs, f"missing recovered {k} in {list(objs)}"
        assert "[redacted: secret active-token/value]" in objs[k] and secret not in objs[k], objs[k]
print("PASS Recovery: both R2 and R3 .jsonl + .md transcripts redacted, uploaded to object store, and unlinked from transcripts.unflushed!")
PY

UI_T2_AFTER=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/tasks/$T2/attempts/1/transcript")
if printf '%s' "$UI_T2_AFTER" | grep -q "$ACTIVE_SECRET"; then
	echo "FAIL: /api/tasks/$T2/attempts/1/transcript leaked raw ACTIVE_SECRET after recovery!" >&2
	exit 1
fi
printf '%s' "$UI_T2_AFTER" | grep -q '\[redacted: secret active-token/value\]'
echo "PASS Post-recovery UI transcript serves redacted narrative cleanly!"

echo "=== Daemon log excerpts (secrets & transcripts) ==="
grep -E 'grain secrets:|transcript' "$ROOT/daemon.log" || true
echo "=== DONE walk-secrets-transcripts.sh ==="
