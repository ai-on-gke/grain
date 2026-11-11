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

# End-to-end walkthrough of §6.2:
#   - Live `grain host` unix-socket listener + `grain daemon -hostd-socket ... -upgrade-src-dir ...`
#   - Healthy baseline on GET /api/upgrade, GET /api/sandboxes, GET /api/settings/cli-run, GET /api/capability-servers/host-entries
#   - SIGSTOP host process (bound socket with unread accept backlog)
#   - Cold probe records unreachable failure; 8 concurrent clients poll every 1s across 40s (<200ms, <=1 background probe)
#   - SIGCONT recovery & socket-inode replacement recovery on host restart

set -euo pipefail

GRAIN=${GRAIN:-/home/debian/bin/grain}
ROOT=/tmp/walk-s6-hostexec
rm -rf "$ROOT"
mkdir -p "$ROOT/data/secrets/github" "$ROOT/sandboxes" "$ROOT/bin" "$ROOT/host-state" "$ROOT/src-repo"

UI_ADDR="127.0.0.1:4284"
GH_HOST="127.0.0.1:4285"
API_TOKEN="walk-s6-hostexec-token"
SOCK="$ROOT/hostd.sock"

cleanup() {
	if [ -f "$ROOT/host.pid" ]; then
		kill -CONT "$(cat "$ROOT/host.pid")" 2>/dev/null || true
		kill "$(cat "$ROOT/host.pid")" 2>/dev/null || true
	fi
	if [ -f "$ROOT/daemon.pid" ]; then
		kill "$(cat "$ROOT/daemon.pid")" 2>/dev/null || true
	fi
	if [ -f "$ROOT/upstreams.pid" ]; then
		kill "$(cat "$ROOT/upstreams.pid")" 2>/dev/null || true
	fi
	sudo rm -f "$ROOT/hostd.yaml" 2>/dev/null || true
}
trap cleanup EXIT

(cd /home/debian/work && \
	go build -o "$ROOT/bin/fake_upstreams" ./docs/test-runs/2026-11-08-daemon-runtime-resilience/fake_upstreams.go && \
	go build -o "$ROOT/bin/walk_hostexec_probe" ./docs/test-runs/2026-11-08-daemon-runtime-resilience/walk_hostexec_probe.go)

"$ROOT/bin/fake_upstreams" -sm-addr 127.0.0.1:4286 -gcs-addr 127.0.0.1:4287 -gh-addr "$GH_HOST" >"$ROOT/upstreams.log" 2>&1 &
echo $! >"$ROOT/upstreams.pid"

# Minimal git repo for -upgrade-src-dir so GET /api/upgrade has Upgrader enabled
(
	cd "$ROOT/src-repo"
	git init -q -b main
	git config user.name "walk"
	git config user.email "walk@example.invalid"
	echo "ok" >README.md
	git add README.md
	git commit -q -m "init"
)

cat >"$ROOT/bin/agy" <<'EOF'
#!/bin/sh
echo '{"event":"result","result":{"status":"SUCCESS","response":"ok"}}'
EOF
chmod 755 "$ROOT/bin/agy"

cat >"$ROOT/hostd.yaml.tmp" <<EOF
clis:
  agy:
    path: $ROOT/bin/agy
    cwd_under: [/tmp]
capabilities:
  walk-echo:
    command: /bin/cat
EOF
sudo install -o root -g root -m 0644 "$ROOT/hostd.yaml.tmp" "$ROOT/hostd.yaml"
rm -f "$ROOT/hostd.yaml.tmp"

echo "=== Starting real grain host on $SOCK ==="
"$GRAIN" host -allowlist "$ROOT/hostd.yaml" -socket "$SOCK" -state-dir "$ROOT/host-state" >"$ROOT/host.log" 2>&1 &
echo $! >"$ROOT/host.pid"
for _ in $(seq 1 50); do
	[ -S "$SOCK" ] && break
	sleep 0.1
done
echo "grain host up (pid $(cat "$ROOT/host.pid")), inode=$(stat -c '%i' "$SOCK")"

printf '%s\n' "$API_TOKEN" >"$ROOT/data/local-api-token"
chmod 600 "$ROOT/data/local-api-token"
printf '{"directConfigWrites": true}\n' >"$ROOT/data/state-repo.json"
printf 'git:\n  - credential: anonymous\n' >"$ROOT/data/connections.yaml"
printf 'walkthrough-dummy-gemini-key\n' >"$ROOT/gemini-key"

echo "=== Starting real grain daemon with -hostd-socket & -upgrade-src-dir ==="
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
	-hostd-socket "$SOCK" \
	-upgrade-src-dir "$ROOT/src-repo" \
	-upgrade-install-path "$ROOT/bin/installed-grain" \
	>"$ROOT/daemon.log" 2>&1 &
echo $! >"$ROOT/daemon.pid"

for _ in $(seq 1 100); do
	if curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/upgrade" >/dev/null 2>&1; then
		break
	fi
	sleep 0.2
done
echo "grain daemon up (pid $(cat "$ROOT/daemon.pid"))"

echo "=== Verifying healthy baseline on /api/upgrade, /api/sandboxes, /api/settings/cli-run, /api/capability-servers/host-entries ==="
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/upgrade" >"$ROOT/upgrade_healthy.json"
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >"$ROOT/health_healthy.json"
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/settings/cli-run" >"$ROOT/clirun_healthy.json"
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/capability-servers/host-entries" >"$ROOT/hostentries_healthy.json"

python3 - "$ROOT/upgrade_healthy.json" "$ROOT/health_healthy.json" "$ROOT/clirun_healthy.json" "$ROOT/hostentries_healthy.json" <<'PY'
import json, sys
up = json.load(open(sys.argv[1]))
hl = json.load(open(sys.argv[2]))
cr = json.load(open(sys.argv[3]))
he = json.load(open(sys.argv[4]))
assert up["enabled"] is True, up
assert hl["hostExecutor"]["hostd"]["reachable"] is True, hl["hostExecutor"]
assert cr["hostExecutor"]["hostd"]["reachable"] is True, cr["hostExecutor"]
assert he["available"] is True and "walk-echo" in he["entries"], he
print("PASS Baseline: upgrade.enabled=true, hostd.reachable=true, capability entries=", he["entries"])
PY

echo "=== Sending SIGSTOP to grain host (pid $(cat "$ROOT/host.pid")) ==="
kill -STOP "$(cat "$ROOT/host.pid")"

echo "=== Allowing initial cold probe on /api/upgrade to record unreachable state ==="
T0_COLD=$(date +%s%3N)
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/upgrade" >"$ROOT/upgrade_cold.json"
T1_COLD=$(date +%s%3N)
echo "Cold probe completed in $((T1_COLD - T0_COLD))ms"
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >"$ROOT/health_cold.json"
python3 - "$ROOT/health_cold.json" <<'PY'
import json, sys
hl = json.load(open(sys.argv[1]))
assert hl["hostExecutor"]["hostd"]["reachable"] is False, hl["hostExecutor"]
print("Confirmed hostd.reachable=false recorded:", hl["hostExecutor"]["hostd"].get("error", "")[:120])
PY

echo "=== Running 8-client x 40s concurrent poll across 15s TTL expirations (including host-entries) ==="
"$ROOT/bin/walk_hostexec_probe" \
	-base-url "http://$UI_ADDR" \
	-token "$API_TOKEN" \
	-sock "$SOCK" \
	-daemon-pid "$(cat "$ROOT/daemon.pid")" \
	-clients 8 \
	-seconds 40 \
	-max-ms 200 \
	-include-host-entries

echo "=== Verifying /api/capability-servers/host-entries under SIGSTOP (<200ms, 0 leaked sockets) ==="
T0_HE=$(date +%s%3N)
curl -fsS --max-time 4 -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/capability-servers/host-entries" >"$ROOT/hostentries_sigstop.json"
T1_HE=$(date +%s%3N)
ELAPSED_HE=$((T1_HE - T0_HE))
echo "host-entries returned in ${ELAPSED_HE}ms: $(cat "$ROOT/hostentries_sigstop.json")"
if [ "$ELAPSED_HE" -ge 200 ]; then
	echo "FAIL: expected host-entries <200ms under SIGSTOP, took ${ELAPSED_HE}ms" >&2
	exit 1
fi

echo "=== Sending SIGCONT to grain host and measuring recovery ==="
kill -CONT "$(cat "$ROOT/host.pid")"
T0_REC=$(date +%s%3N)
RECOVERED=0
for poll in $(seq 1 20); do
	P0=$(date +%s%3N)
	curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >"$ROOT/health_rec.json"
	P1=$(date +%s%3N)
	if python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d["hostExecutor"]["hostd"]["reachable"] else 1)' "$ROOT/health_rec.json"; then
		echo "SIGCONT recovered on poll #$poll (poll took $((P1 - P0))ms, total elapsed $((P1 - T0_REC))ms)"
		RECOVERED=1
		break
	else
		echo "  poll #$poll ($((P1 - P0))ms, elapsed $((P1 - T0_REC))ms): still serving cached stale failure while waiting for TTL/refresh"
	fi
	sleep 1
done
if [ "$RECOVERED" -ne 1 ]; then
	echo "FAIL: host did not recover after SIGCONT!" >&2
	exit 1
fi

echo "=== Testing socket-file inode replacement on host restart ==="
OLD_INO=$(stat -c '%i' "$SOCK")
kill -STOP "$(cat "$ROOT/host.pid")"
# Record unreachable state in cache first:
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >/dev/null
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >"$ROOT/health_pre_restart.json"
python3 -c 'import json,sys; assert not json.load(open(sys.argv[1]))["hostExecutor"]["hostd"]["reachable"]' "$ROOT/health_pre_restart.json"

# Kill old host, unlink socket, start fresh grain host binding a new socket inode:
kill -9 "$(cat "$ROOT/host.pid")" 2>/dev/null || true
wait "$(cat "$ROOT/host.pid")" 2>/dev/null || true
rm -f "$SOCK"
"$GRAIN" host -allowlist "$ROOT/hostd.yaml" -socket "$SOCK" -state-dir "$ROOT/host-state" >"$ROOT/host2.log" 2>&1 &
echo $! >"$ROOT/host.pid"
for _ in $(seq 1 50); do
	[ -S "$SOCK" ] && break
	sleep 0.05
done
NEW_INO=$(stat -c '%i' "$SOCK")
echo "Replaced socket inode: old=$OLD_INO new=$NEW_INO"

R0=$(date +%s%3N)
curl -fsS -H "Authorization: Bearer $API_TOKEN" "http://$UI_ADDR/api/sandboxes" >"$ROOT/health_post_restart.json"
R1=$(date +%s%3N)
python3 - "$ROOT/health_post_restart.json" "$((R1 - R0))" <<'PY'
import json, sys
hl = json.load(open(sys.argv[1]))
assert hl["hostExecutor"]["hostd"]["reachable"] is True, hl["hostExecutor"]
print(f"PASS Socket replacement: recovered IMMEDIATELY on poll #1 in {sys.argv[2]}ms!")
PY

echo "=== DONE walk-hostexec.sh ==="
