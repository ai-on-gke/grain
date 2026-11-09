#!/usr/bin/env bash
# ok/bad always succeed, so `A && ok || bad` is an if-then-else in this file.
# shellcheck disable=SC2015
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

# run-item3.sh DRIVER [ROOT] [PORT]: walkthrough 2026-11-08 §5 item 3 --
# durable release_approval on a repository whose ci_mode is unset, against a
# real grain daemon (DRIVER sqlite or postgres; postgres reads $PG_URL) and
# githubsim (start-item3.sh). The release-pending watcher polls a
# repository's tags once per 10 minutes, remembered in memory, so a daemon
# restart is what makes it poll again: every "tick" below is a restart.
set -uo pipefail
driver="${1:?sqlite|postgres}"
ROOT="${2:-/tmp/s5-item3/$driver}"
PORT="${3:-4393}"
SIM="http://127.0.0.1:$((PORT + 10))"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
G="${GRAIN:-/home/debian/bin/grain}"
pass=0
fail=0
ok() { echo "PASS: $*"; pass=$((pass + 1)); }
bad() { echo "FAIL: $*"; fail=$((fail + 1)); }
start() {
  bash "$here/start-item3.sh" "$ROOT" "$driver" "$PORT" | tail -1
  GRAIN_SERVER="http://127.0.0.1:$PORT"
  GRAIN_TOKEN="$(cat "$ROOT/code")"
  export GRAIN_SERVER GRAIN_TOKEN
}
polls() { curl -fsS "$SIM/_calls" | jq '[(. // [])[] | select(.Path | test("matching-refs/tags"))] | length'; }
wait_poll() { # wait_poll N: until the watcher has polled the tags N times in all
  for _ in $(seq 1 120); do [ "$(polls)" -ge "$1" ] && return 0; sleep 0.5; done
  return 1
}
dispatches() { curl -fsS "$SIM/_dispatches" | jq --arg v "$1" '[.[] | select(.workflow == "promote-release.yml" and .inputs.version == $v)] | length'; }
pending_notes() { curl -fsS -H "Authorization: Bearer $GRAIN_TOKEN" "$GRAIN_SERVER/api/notifications" |
  jq -r --arg s "release.pending:$1" '[.[] | select((.subject // .Subject // "") | startswith($s)) | select((.cleared_at // .clearedAt // .ClearedAt // null) == null)] | length'; }
pending_any() { curl -fsS -H "Authorization: Bearer $GRAIN_TOKEN" "$GRAIN_SERVER/api/notifications" | jq -c --arg s "release.pending:$1" '[.[] | select(tostring | contains($s))]'; }

rm -rf "$ROOT"
mkdir -p "$ROOT"
echo "=== boot ($driver) ==="
start
n0="$(polls)"
"$G" repo add acme/widgets >/dev/null
"$G" repo list
wait_poll $((n0 + 1)) && ok "watcher polled acme/widgets (ci_mode unset, no .grain/workflows) once repo was added" || bad "no poll"
"$G" repo work-ownership acme/widgets | head -1

echo "=== 3A. v1.4.0-pending approved from the CLI before the watcher's next poll ==="
curl -fsS -X POST "$SIM/_tag?name=v1.4.0-pending"
n1="$(polls)"
"$G" release approve -repo acme/widgets v1.4.0 >"$ROOT/approve-1.txt" 2>&1 &
a1=$!
"$G" release approve -repo acme/widgets v1.4.0 >"$ROOT/approve-2.txt" 2>&1 &
a2=$!
wait $a1
r1=$?
wait $a2
r2=$?
echo "approve #1 rc=$r1: $(head -c 300 "$ROOT/approve-1.txt")"
echo "approve #2 rc=$r2: $(head -c 300 "$ROOT/approve-2.txt")"
[ $((r1 == 0 ? 1 : 0)) -ne $((r2 == 0 ? 1 : 0)) ] && ok "exactly one of two concurrent approves succeeded" || bad "concurrent approves: rc $r1 / $r2"
# The approve reads the -pending tag through the same matching-refs endpoint
# the watcher polls, so the count moves by the approve's own read; the
# watcher's own next poll is releasePendingPollInterval (10m) after boot.
echo "note: $(($(polls) - n1)) matching-refs read(s) since the tag appeared (the approve's own tag reads; the watcher next polls 10m after boot)"
sleep 1
[ "$(dispatches v1.4.0)" = 1 ] && ok "promote-release.yml dispatched once for v1.4.0" || bad "dispatches for v1.4.0: $(dispatches v1.4.0)"
"$G" release approve -repo acme/widgets v1.4.0 >"$ROOT/approve-3.txt" 2>&1 && bad "a later approve succeeded: $(cat "$ROOT/approve-3.txt")" || ok "a third approve is refused: $(head -c 200 "$ROOT/approve-3.txt")"
for tick in 1 2 3; do
  n="$(polls)"
  start >/dev/null
  wait_poll $((n + 1)) || bad "tick $tick: no poll after restart"
  sleep 2
  echo "tick $tick (restart): release.pending:v1.4.0 open=$(pending_notes v1.4.0) dispatches=$(dispatches v1.4.0)"
done
[ "$(pending_any v1.4.0)" = "[]" ] && ok "release.pending:v1.4.0 never raised across 3 watcher polls / restarts" || bad "raised: $(pending_any v1.4.0)"
[ "$(dispatches v1.4.0)" = 1 ] && ok "still one promote-release.yml dispatch for v1.4.0 after restarts" || bad "dispatches: $(dispatches v1.4.0)"
"$G" release approve -repo acme/widgets v1.4.0 >"$ROOT/approve-4.txt" 2>&1 && bad "approve after restart succeeded" || ok "approve after restart refused: $(head -c 200 "$ROOT/approve-4.txt")"

echo "=== 3B. v1.4.1-pending raised by the watcher first, then approved ==="
curl -fsS -X POST "$SIM/_tag?name=v1.4.1-pending"
n="$(polls)"
start >/dev/null
wait_poll $((n + 1))
for _ in $(seq 1 20); do [ "$(pending_notes v1.4.1)" -ge 1 ] && break; sleep 0.5; done
[ "$(pending_notes v1.4.1)" -ge 1 ] && ok "watcher raised release.pending:v1.4.1" || bad "not raised: $(pending_any v1.4.1)"
pending_any v1.4.1 | jq -c '.[0] | {subject, title, severity}' 2>/dev/null | head -c 300
echo
"$G" release approve -repo acme/widgets v1.4.1 >"$ROOT/approve-b.txt" 2>&1 && ok "CLI approve of v1.4.1: $(head -c 200 "$ROOT/approve-b.txt")" || bad "approve v1.4.1: $(cat "$ROOT/approve-b.txt")"
sleep 1
[ "$(pending_notes v1.4.1)" = 0 ] && ok "release.pending:v1.4.1 cleared on approve" || bad "still open: $(pending_any v1.4.1)"
for tick in 1 2; do
  n="$(polls)"
  start >/dev/null
  wait_poll $((n + 1)) || bad "tick $tick: no poll"
  sleep 2
done
[ "$(pending_notes v1.4.1)" = 0 ] && ok "release.pending:v1.4.1 did not re-raise across 2 more polls / restarts (tag still on the remote)" || bad "re-raised: $(pending_any v1.4.1)"
[ "$(dispatches v1.4.1)" = 1 ] && ok "one promote-release.yml dispatch for v1.4.1" || bad "dispatches for v1.4.1: $(dispatches v1.4.1)"
curl -fsS "$SIM/_dispatches" | jq -c '.[] | {workflow, ref, version: .inputs.version, decision: .inputs.decision}' | tee "$ROOT/dispatches.json"
kill "$(cat "$ROOT/daemon.pid")" "$(cat "$ROOT/sim.pid")" 2>/dev/null
echo "item3 ($driver): $pass passed, $fail failed"
