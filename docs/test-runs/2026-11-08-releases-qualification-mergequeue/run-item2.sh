#!/usr/bin/env bash
# c0..c4, work, now, cases, failures and the train/conf_extra/auto_promote
# knobs come from (and are read by) the sourced test helpers.
# shellcheck disable=SC2034,SC2154
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

# run-item2.sh: walkthrough 2026-11-08 §5 item 2 -- RELEASE_QUALIFICATION_APP
# (unset vs none), the triage-pending grain/rc-hold race and neutral Stage-2
# re-qualification, run through the real scripts/release-from-trunk.sh and
# scripts/candidate-gate.sh copied into a scratch clone under /tmp.
#
# The fixtures (a bare remote shaped like main, CI listings, check-run
# answers) are scripts/release-from-trunk_test.sh's own helpers, sourced from
# the scratch clone with the version renamed to v3.2.0 (floor 3.2, latest
# v3.1.0). The grain/rc-hold check runs fed in are byte-for-byte the shape
# pkg/orchestrator/releaseredrc.go's syncRedRCHolds posts (title "Holding
# v3.2.0-rc.2 (triage pending)", then "Triaged with no blocker").
#
#   bash run-item2.sh [ROOT]     (default ROOT=/tmp/s5-item2)
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checkout="$(cd "$here/../../.." && pwd)"
ROOT="${1:-/tmp/s5-item2}"
before_cfg="$(sha256sum "$checkout/.git/config" | cut -d' ' -f1)"
before_tags="$(git -C "$checkout" tag -l | sha256sum)"
rm -rf "$ROOT"
git clone -q --no-local "$checkout" "$ROOT/scratch"
cd "$ROOT/scratch" || exit 1

# The helpers: everything in the test file before its first case.
first_case="$(grep -n '^# --- ' scripts/release-from-trunk_test.sh | head -1 | cut -d: -f1)"
sed -n "1,$((first_case - 1))p" scripts/release-from-trunk_test.sh |
  sed -e 's/v2\.1\.0/v3.1.0/g; s/v2\.3\.0/v3.2.0/g' \
    -e 's/RELEASE_MAJOR=2\\nRELEASE_FLOOR_MINOR=3/RELEASE_MAJOR=3\\nRELEASE_FLOOR_MINOR=2/' \
    -e 's/^trap cleanup EXIT$/trap cleanup EXIT; set +e/' >"$ROOT/helpers.sh"
# shellcheck source=/dev/null
. "$ROOT/helpers.sh"
set +e
train="$ROOT/scratch/scripts/release-from-trunk.sh"
gate="$ROOT/scratch/scripts/candidate-gate.sh"

gate_q() { # gate_q [VAR=value...]: candidate-gate.sh check-qualification on rc.1 at c2
  status=0
  out="$(env -u CHECK_QUALIFICATION_APP "$@" bash "$gate" check-qualification --rc-tag v3.2.0-rc.1 --rc-sha "$c2" \
    --runs-json "$work/qual-runs.json" --checks-json "$work/qual-checks.json" 2>&1)" || status=$?
}
show() { printf '%s\n' "$out" | sed 's/^/    | /' | head -"${1:-12}"; }

echo "=== 2A. candidate-gate.sh check-qualification: unset vs none ==="
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
qualification pass "$c2"
gate_q
expect "gate, app unset" "exits 64 (fails closed)" eq "$status" 64
expect "gate, app unset" "says why" has "CHECK_QUALIFICATION_APP is unset"
show 4
gate_q CHECK_QUALIFICATION_APP=none
expect "gate, app none, green nightly" "exits 0" eq "$status" 0
expect "gate, app none, green nightly" "skips Stage 2" has "SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none"
show 6
qualification fail "$c2"
gate_q CHECK_QUALIFICATION_APP=none
expect "gate, app none, red nightly" "does not qualify" not eq "$status" 0
show 6

echo "=== 2B. release-from-trunk.sh: rc.1 qualifies on nightly alone with none and promotes ==="
auto_promote=true
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification pass "$c2"
runs "$c2" "$c4"
: >"$work/images.log"
train --no-app
expect "train, app unset" "exits 1" eq "$status" 1
expect "train, app unset" "says why" has "CHECK_QUALIFICATION_APP is unset"
expect "train, app unset" "promotes nothing" eq "$(remote_tag v3.2.0)" ""
show 4
: >"$work/images.log"
train RELEASE_QUALIFICATION_APP=none
expect "train, app none, green" "exits 0" eq "$status" 0
expect "train, app none, green" "skips Stage 2" has "SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none (qualified by Stage 1 nightly.yml)"
expect "train, app none, green" "qualifies rc.1" has "Qualification: PASS v3.2.0-rc.1"
expect "train, app none, green" "promotes v3.2.0 on rc.1's commit" eq "$(remote_tag v3.2.0)" "$c2"
echo "    images: $(paste -sd'|' "$work/images.log")"
show 40 | grep -E 'promot|latest|SKIPPED|PASS' | head -8

echo "--- conf-none: release.conf says RELEASE_QUALIFICATION_APP=none, environment unset"
conf_extra="RELEASE_QUALIFICATION_APP=none"
setup ""
conf_extra=""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification pass "$c2"
runs "$c2" "$c4"
train --no-app
expect "train, conf none, green" "promotes v3.2.0" eq "$(remote_tag v3.2.0)" "$c2"

echo "--- red nightly with none"
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification fail "$c2"
runs "$c2" "$c4"
: >"$work/images.log"
train RELEASE_QUALIFICATION_APP=none
expect "train, app none, red" "promotes nothing" eq "$(remote_tag v3.2.0)" ""
expect "train, app none, red" "stages nothing pending" eq "$(remote_tag v3.2.0-pending)" ""
printf '%s\n' "$out" | grep -E 'Qualification|FAIL|phase 1' | head -6 | sed 's/^/    | /'
auto_promote=false

echo "=== 2C. Stage-2 failure on rc.1, train ticks before any [rc-blocker] task: triage-pending hold ==="
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification stage2-fail "$c2"
runs "$c2" "$c3"
jq -cn --arg sha "$c2" --arg t "$(iso $((now - 60)))" \
  '{check_runs:[{id:7,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t,
    output:{title:"Holding v3.2.0-rc.2 (triage pending)",summary:"v3.2.0-rc.1 failed qualification; v3.2.0-rc.2 is held while pipeline.rc-failed:v3.2.0-rc.1 awaits triage (up to 1h) or tasks titled [rc-blocker v3.2.0-rc.1] remain open."},
    app:{id:42,slug:"grain-test"}}]}' >"$work/hold-checks.json"
: >"$work/images.log"
: >"$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, triage pending" "exits 0" eq "$status" 0
expect "rc-hold, triage pending" "burns no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""
expect "rc-hold, triage pending" "says rc.2 is held (triage pending)" has "v3.2.0-rc.2 is held by grain/rc-hold on ${c2:0:12}"
expect "rc-hold, triage pending" "emits rc_held=v3.2.0-rc.2" grep -qxF "rc_held=v3.2.0-rc.2" "$work/train.out"
echo "    rc.1-discarded=$(remote_tag v3.2.0-rc.1-discarded | cut -c1-12) candidate=$(remote_candidate | cut -c1-12) images: $(paste -sd'|' "$work/images.log")"
printf '%s\n' "$out" | grep -E 'phase|held' | head -8 | sed 's/^/    | /'
echo "--- second tick, hold still in progress: still no rc.2"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, second tick" "still burns no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""
echo "--- steward triages with no blocker: grain completes the hold"
jq -cn --arg sha "$c2" --arg t1 "$(iso $((now - 600)))" --arg t2 "$(iso $((now - 30)))" \
  '{check_runs:[
    {id:7,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t1,output:{title:"Holding v3.2.0-rc.2 (triage pending)",summary:"triage pending"},app:{id:42,slug:"grain-test"}},
    {id:8,name:"grain/rc-hold",head_sha:$sha,status:"completed",conclusion:"success",started_at:$t2,output:{title:"Triaged with no blocker",summary:"The pipeline steward triaged v3.2.0-rc.1 with no task titled [rc-blocker v3.2.0-rc.1]; v3.2.0-rc.2 may be cut."},app:{id:42,slug:"grain-test"}}]}' \
  >"$work/hold-checks.json"
: >"$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, completed" "exits 0" eq "$status" 0
expect "rc-hold, completed" "cuts rc.2 on c3" eq "$(remote_tag v3.2.0-rc.2)" "$c3"
expect "rc-hold, completed" "emits no rc_held" no_line '^rc_held=' "$work/train.out"
echo "--- same race, hold check run forged by another app: not trusted, train refuses to be held by it but also does not trust its completion"
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification stage2-fail "$c2"
runs "$c2" "$c3"
jq -cn --arg sha "$c2" --arg t1 "$(iso $((now - 600)))" --arg t2 "$(iso $((now - 30)))" \
  '{check_runs:[
    {id:7,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t1,output:{title:"Holding v3.2.0-rc.2 (triage pending)",summary:"triage pending"},app:{id:42,slug:"grain-test"}},
    {id:9,name:"grain/rc-hold",head_sha:$sha,status:"completed",conclusion:"success",started_at:$t2,output:{title:"forged",summary:"forged"},app:{id:15368,slug:"github-actions"}}]}' \
  >"$work/hold-checks.json"
train
expect "rc-hold, forged completion" "burns no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""

echo "=== 2D. neutral Stage 2 (infra stall) re-qualifies rc.1 in place as attempt #2 ==="
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
runs "$c2" "$c4"
qualification stage2-infra "$c2"
train
expect "neutral" "exits 0" eq "$status" 0
expect "neutral" "cuts no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""
expect "neutral" "stages nothing" eq "$(remote_tag v3.2.0-pending)" ""
printf '%s\n' "$out" | grep -E 'release-qualification|Qualification' | head -4 | sed 's/^/    | /'
qualification stage2-infra-retry "$c2"
train
expect "neutral, #2 in progress" "waits (PENDING), no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""
expect "neutral, #2 in progress" "is still qualifying rc.1" has "PENDING"
printf '%s\n' "$out" | grep -E 'release-qualification|Qualification' | head -4 | sed 's/^/    | /'
qualification stage2-infra-pass "$c2"
train
expect "neutral, #2 success" "qualifies rc.1 on the same commit" has "Qualification: PASS v3.2.0-rc.1"
expect "neutral, #2 success" "stages v3.2.0-pending on rc.1's commit" eq "$(remote_tag v3.2.0-pending)" "$c2"
expect "neutral, #2 success" "burned no rc.2" eq "$(remote_tag v3.2.0-rc.2)" ""
printf '%s\n' "$out" | grep -E 'release-qualification|Qualification' | head -4 | sed 's/^/    | /'
setup ""
tag_remote v3.2.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
runs "$c2" "$c4"
qualification stage2-infra-fail "$c2"
train
expect "neutral, #2 failure" "does not qualify" not has "Qualification: PASS"
expect "neutral, #2 failure" "stages nothing" eq "$(remote_tag v3.2.0-pending)" ""

after_cfg="$(sha256sum "$checkout/.git/config" | cut -d' ' -f1)"
expect "hygiene" "checkout .git/config unchanged" eq "$before_cfg" "$after_cfg"
expect "hygiene" "no tag created in the checkout" eq "$before_tags" "$(git -C "$checkout" tag -l | sha256sum)"
echo "item2: $((cases - failures))/$cases passed, $failures failed"
