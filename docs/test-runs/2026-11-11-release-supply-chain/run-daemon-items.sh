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

# Walkthrough 2026-11-11 §7 items 2 and 3 against a real daemon that
# start-daemon.sh started (with GRAIN_RELEASE_LOOP_IDLE_AFTER=1m) over
# trainsim.go. Item 2: Stage 1 repairs from simulated nightly.yml runs the
# daemon polls. Item 3: pipeline.release-loop-idle raised, suppressed and
# cleared.
#
#   bash run-daemon-items.sh <root> [daemon-port]
set -uo pipefail
root="${1:?root}"
port="${2:-4420}"
D="http://127.0.0.1:$port"
S="http://127.0.0.1:$((port + 10))"
code="$(cat "$root/code")"
api() { curl -fsS -H "Authorization: Bearer $code" "$D$1"; }
sim() { curl -fsS -X POST "$S$1"; }
enc() { jq -rn --arg s "$1" '$s|@uri'; }
step() { echo; echo "=== $*"; }
repairs() { # every repair filed so far, closed ones included (/api/tasks lists open tasks)
  for id in $(seq 2 40); do
    api "/api/tasks/$id" 2>/dev/null | jq -e '.author=="stage1-repair"' >/dev/null 2>&1 || continue
    api "/api/tasks/$id" | jq -r '"    repair \(.id) [\(.state)] p\(.priority) \"\(.title)\" goal=\(.goal.kind)/\(.goal.onMet) met=\(.goal.met // false) comments=\(.comments | length)\n      evidence: \(.goal.evidence)"'
  done
}
ticks() {
  api /api/tasks | jq -r '[.[] | select(.title | test("release train"; "i"))] | "    release-train ticks: \(length) (\(map("\(.id):\(.state)") | join(", ")))"'
}
notes() {
  api /api/notifications | jq -r --arg f "$1" '.[] | select((.subject | startswith($f)) and .clearedAt == null) | "    [\(.severity)] \(.subject): \(.title)\n      \(.body | gsub("\n+"; " "))"'
}
# /api/notifications lists cleared notifications too (clearedAt set).
openidle() { api /api/notifications | jq '[.[] | select((.subject | startswith("pipeline.release-loop-idle")) and .clearedAt == null)] | length'; }
run() { # run <ref> <event> <conclusion> <jobs>
  local id
  id="$(sim "/_run?ref=$(enc "$1")&event=$2&conclusion=$3&jobs=$(enc "$4")")"
  echo "    nightly.yml run $id on $1 ($2) concluded $3"
}
settle() { sleep "${1:-10}"; }
ALLGREEN="idle-gate:success;go, shuffled and repeated:success;sustained load:success;release image vulnerabilities:success;stage 1 qualification verdict:success"
RED="idle-gate:success;go, shuffled and repeated:failure;sustained load:success;release image vulnerabilities:success;stage 1 qualification verdict:failure"
RED2="idle-gate:success;go, shuffled and repeated:success;sustained load:failure;release image vulnerabilities:success;stage 1 qualification verdict:failure"
GATED="idle-gate:success;go, shuffled and repeated:skipped;sustained load:skipped;release image vulnerabilities:skipped;stage 1 qualification verdict:skipped"

echo "# item 2: one repair per red Stage 1"
c1="$(sim "/_commit?msg=$(enc 'feat: widgets one')")"
sim "/_green?sha=$c1"
sim "/_tag?name=v1.1.0-rc.1" >/dev/null
step "2a. rc.1's Stage 1 is red"
run v1.1.0-rc.1 push failure "$RED"; settle
repairs; ticks
step "2b. the scheduled nightly on main is red too"
run main schedule failure "$RED2"; settle
repairs
step "2c. an idle-gated scheduled nightly on main (every job but idle-gate skipped) concludes green"
run main schedule success "$GATED"; settle 15
repairs
step "2d. rc.2's Stage 1 is green"
sim "/_tag?name=v1.1.0-rc.2" >/dev/null
run v1.1.0-rc.2 push success "$ALLGREEN"; settle 15
repairs; ticks
step "2e. a later red after the repair closed files a new one"
run v1.1.0-rc.2 workflow_dispatch failure "$RED"; settle
repairs
step "2f. a flake retry (workflow_dispatch jobs=go-long) re-runs only the failed job, green; the rest skipped"
run v1.1.0-rc.2 workflow_dispatch success "idle-gate:success;go, shuffled and repeated:success;sustained load:skipped;release image vulnerabilities:skipped;stage 1 qualification verdict:success"; settle 15
repairs; ticks
grep -E "Stage 1 run|stage1|nightly-green" "$root/daemon.log" | sed 's/^/    log: /' | tail -12

echo; echo "# item 3: pipeline.release-loop-idle (GRAIN_RELEASE_LOOP_IDLE_AFTER=1m)"
# rc.2 released as v1.1.0, rc.1 dropped: no live rc.
sim "/_tag?name=v1.1.0&ref=v1.1.0-rc.2" >/dev/null
sim "/_tag?name=v1.1.0-rc.1-discarded&ref=v1.1.0-rc.1" >/dev/null
step "3a. main gains a chore-only green commit: nothing is owed"
c2="$(sim "/_commit?msg=$(enc 'chore: tidy')")"; sim "/_green?sha=$c2"
settle 140
notes pipeline.release-loop-idle || true
echo "    (open loop-idle notifications: $(openidle))"
step "3b. a green fix lands: raised after the 1m threshold"
c3="$(sim "/_commit?msg=$(enc 'fix: widgets two')")"; sim "/_green?sha=$c3"
settle 150
notes pipeline.release-loop-idle
step "3c. a staged v1.2.0-pending explains the wait: cleared"
sim "/_tag?name=v1.2.0-pending&ref=main" >/dev/null
settle 90
echo "    (open loop-idle notifications: $(openidle))"
step "3d. the -pending is withdrawn: raised again"
sim "/_untag?name=v1.2.0-pending"
settle 150
notes pipeline.release-loop-idle
step "3e. the train cuts v1.2.0-rc.1: cleared"
sim "/_tag?name=v1.2.0-rc.1" >/dev/null
settle 90
echo "    (open loop-idle notifications: $(openidle))"
grep -iE "release-loop|loop-idle|release loop" "$root/daemon.log" | sed 's/^/    log: /' | tail -8
step "every notification the daemon holds"
api /api/notifications | jq -r '.[] | "    [\(.severity)] \(.subject) raised \(.raisedAt) cleared \(.clearedAt // "-")"'
