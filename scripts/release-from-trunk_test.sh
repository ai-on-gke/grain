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

# Unit tests for scripts/release-from-trunk.sh: each case builds a bare
# remote shaped like grain's main, hands the train JSON shaped the way the
# release gate answers, stubs the image publisher, and checks the tags,
# the candidate pointer and the images the run left behind.
# tests/deploy/release_trunk_scripts_test.go runs this in CI; by hand:
#
#   bash scripts/release-from-trunk_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

train="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-from-trunk.sh"
work="$(mktemp -d)"
server_pid=""
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
unset GRAIN_API_URL GRAIN_API_TOKEN GRAIN_RELEASE_GATE_URL GRAIN_RELEASE_GATE_TOKEN RELEASE_NEVER_PROMOTE RELEASE_MAJOR RELEASE_FLOOR_MINOR
unset GITHUB_ACTIONS GITHUB_TOKEN GH_TOKEN GITHUB_API_URL GITHUB_REPOSITORY RELEASE_CI_SOURCE RELEASE_GITHUB_REPO RELEASE_GITHUB_API_URL
export HOME="$work/home"
mkdir -p "$HOME"

failures=0
cases=0
out=""
status=0
now="$(date +%s)"

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# The image publisher: logs what it was asked.
cat > "$work/images.sh" <<EOF
#!/bin/sh
echo "\$*" >> "$work/images.log"
EOF
chmod +x "$work/images.sh"

# A fake GitHub API: a directory served over HTTP (query strings are
# ignored, so each path is one answer).
mkdir -p "$work/www"
api=""
if command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
  port=$((20000 + RANDOM % 20000))
  (cd "$work/www" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
  server_pid=$!
  for _ in $(seq 50); do
    curl -s -o /dev/null "http://127.0.0.1:$port/" && break
    sleep 0.1
  done
  api="http://127.0.0.1:$port"
else
  echo "skip the GitHub API cases: python3 or curl is not installed"
fi

# setup <never-promote> [<subject>...]: a fresh remote whose main is
# c0..c4 (or c0 and a commit per subject given), v2.1.0 promoted (and
# latest) on c0 two days ago, and a clone of it to run the train in.
# Sets c0..c4 (c0..cN).
setup() {
  rm -rf "$work/remote.git" "$work/seed" "$work/clone" "$work/images.log"
  : > "$work/images.log"
  git init -q --bare -b main "$work/remote.git"
  git init -q -b main "$work/seed"
  (
    cd "$work/seed"
    mkdir scripts
    printf 'RELEASE_MAJOR=2\nRELEASE_FLOOR_MINOR=3\nRELEASE_NEVER_PROMOTE="%s"\n' "$1" > scripts/release.conf
    git add scripts/release.conf
    git commit -q -m c0
    GIT_COMMITTER_DATE="@$((now - 48 * 3600)) +0000" git tag -a v2.1.0 -m "Release v2.1.0"
    GIT_COMMITTER_DATE="@$((now - 48 * 3600)) +0000" git tag -a latest -m "Latest release v2.1.0"
    if [ $# -gt 1 ]; then
      for subject in "${@:2}"; do git commit -q --allow-empty -m "$subject"; done
    else
      for i in 1 2 3 4; do git commit -q --allow-empty -m "c$i"; done
    fi
    git remote add origin "$work/remote.git"
    git push -q origin main --tags
  )
  c=()
  mapfile -t c < <(git -C "$work/seed" rev-list --reverse main)
  c0="${c[0]}" c1="${c[1]:-}" c2="${c[2]:-}" c3="${c[3]:-}" c4="${c[4]:-}"
  git clone -q "$work/remote.git" "$work/clone"
  runs
  qualification none
  rc_hold none
}

# tag_remote <tag> <commit> [<hours-ago>]: an annotated tag on the remote,
# made that long ago (48h by default).
tag_remote() {
  (
    cd "$work/seed"
    GIT_COMMITTER_DATE="@$((now - ${3:-48} * 3600)) +0000" git tag -a "$1" "$2" -m "$1"
    git push -q origin "refs/tags/$1"
  )
}

# runs <commit>...: the CI listing, tests, release and nightly green on
# each commit given.
runs() {
  local sha wf first=1
  {
    printf '{"enabled":true,"runs":['
    for sha in "$@"; do
      for wf in tests release nightly; do
        [ $first = 1 ] || printf ','
        first=0
        printf '{"id":"%s-%s","commit":"%s","ref":"main","startedAt":"%s","finishedAt":"%s","conclusion":"success","jobs":[{"workflowPath":".grain/workflows/%s.yml","jobId":"%s","conclusion":"success","finishedAt":"%s"}]}' \
          "$sha" "$wf" "$sha" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" "$wf" "$wf" "$(iso $((now - 3600)))"
      done
    done
    printf ']}'
  } > "$work/runs.json"
}

# qualification <none|pass|flake|infra|fail> [<commit>] [<app>]: what
# check-qualification reads for the rc on commit -- nightly.yml's jobs
# (grain CI runs, one per attempt: the first runs every Stage 1 job, a
# retry only upgrade-test, which is the one that fails) and the
# grain/release-qualification check run, posted by app (grain's own,
# grain-test, unless it names another).
stage1_ids=(go-long staterepo-soak loadtest scalability merge-stress long-term-test long-sim postgres-stress
  tunnel-stress ui-e2e-long installer-variants vulns cmd-grain-stress upgrade-test)
qualification() {
  local sha="${2:-}" app="${3:-grain-test}" app_id=42 check=success attempts=() a id t jobs runs=() ids
  [ "$app" = grain-test ] || app_id=15368
  case "$1" in
    none) check="" ;;
    pass) attempts=(success) ;;
    flake) attempts=(failure) ;;
    infra) attempts=(cancelled) ;;
    fail) attempts=(failure failure) ;;
  esac
  t=$((now - 7200))
  ids=("${stage1_ids[@]}")
  for a in ${attempts[@]+"${attempts[@]}"}; do
    jobs=()
    for id in "${ids[@]}"; do
      jobs+=("$(printf '{"workflowPath":".github/workflows/nightly.yml","jobId":"%s","conclusion":"%s"}' "$id" \
        "$([ "$id" = upgrade-test ] && echo "$a" || echo success)")")
    done
    runs+=("$(printf '{"id":"n%s","commit":"%s","ref":"rc","startedAt":"%s","jobs":[%s]}' "$t" "$sha" "$(iso "$t")" "$(IFS=,; echo "${jobs[*]}")")")
    ids=(upgrade-test)
    t=$((t + 600))
  done
  printf '{"enabled":true,"runs":[%s]}' "$(IFS=,; echo "${runs[*]}")" > "$work/qual-runs.json"
  if [ -n "$check" ]; then
    printf '{"check_runs":[{"id":1,"name":"grain/release-qualification","head_sha":"%s","status":"completed","conclusion":"%s","started_at":"%s","app":{"id":%s,"slug":"%s"}}]}' \
      "$sha" "$check" "$(iso "$now")" "$app_id" "$app" > "$work/qual-checks.json"
  else
    echo '{"check_runs":[]}' > "$work/qual-checks.json"
  fi
}

# rc_hold <none|held|completed> [<commit>] [<summary>]: what rc_hold_summary
# reads for the dropped rc on commit.
rc_hold() {
  local sha="${2:-}" summary="${3:-v2.3.0-rc.2 is held until these blockers of v2.3.0-rc.1 land: task 101.}"
  case "$1" in
    none)
      echo '{"check_runs":[]}' > "$work/hold-checks.json"
      ;;
    held)
      jq -cn --arg sha "$sha" --arg s "$summary" --arg t "$(iso $((now - 300)))" \
        '{check_runs:[{id:1,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t,output:{title:"Holding v2.3.0-rc.2",summary:$s},app:{id:42,slug:"grain-test"}}]}' \
        > "$work/hold-checks.json"
      ;;
    completed)
      jq -cn --arg sha "$sha" --arg t1 "$(iso $((now - 600)))" --arg t2 "$(iso $((now - 60)))" \
        '{check_runs:[
          {id:1,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t1,output:{title:"Holding v2.3.0-rc.2",summary:"held earlier"},app:{id:42,slug:"grain-test"}},
          {id:2,name:"grain/rc-hold",head_sha:$sha,status:"completed",conclusion:"success",started_at:$t2,output:{title:"Blockers landed",summary:"Every blocker landed."},app:{id:42,slug:"grain-test"}}
        ]}' > "$work/hold-checks.json"
      ;;
  esac
}

train() { # train [VAR=value...]: run the train in the clone
  status=0
  out="$(cd "$work/clone" && env RELEASE_RUNS_JSON="$work/runs.json" \
    RELEASE_QUALIFICATION_RUNS_JSON="$work/qual-runs.json" RELEASE_QUALIFICATION_CHECKS_JSON="$work/qual-checks.json" \
    RELEASE_HOLD_CHECKS_JSON="$work/hold-checks.json" \
    RELEASE_IMAGES="$work/images.sh" RELEASE_NOTES_TASK=off RELEASE_NOW="$now" RELEASE_QUALIFICATION_APP=grain-test \
    "$@" bash "$train" 2>&1)" || status=$?
}

remote_tag() { git -C "$work/remote.git" rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null || true; }
remote_candidate() { git -C "$work/remote.git" rev-parse -q --verify refs/heads/candidate 2>/dev/null || true; }

expect() { # expect <case> <condition-description> <test-command...>
  local name="$1" what="$2"
  shift 2
  cases=$((cases + 1))
  if "$@"; then
    echo "ok   $name: $what"
  else
    echo "FAIL $name: $what"
    printf '%s\n' "$out" | sed 's/^/    /'
    echo "    images: $(paste -sd'|' "$work/images.log")"
    failures=$((failures + 1))
  fi
}
eq() { [ "$1" = "$2" ]; }
has() { [[ "$out" == *"$1"* ]]; }
no_line() { ! grep -q "$1" "$2"; }
images_were() { [ "$(paste -sd'|' "$work/images.log")" = "$1" ]; }

# --- the first cut from trunk ---
setup ""
runs "$c3"
train
expect "first cut" "exits 0" eq "$status" 0
expect "first cut" "tags v2.3.0-rc.1 (the floor minor) on the newest green commit" eq "$(remote_tag v2.3.0-rc.1)" "$c3"
expect "first cut" "force-moves candidate onto it" eq "$(remote_candidate)" "$c3"
expect "first cut" "publishes its images" images_were "rc v2.3.0-rc.1"
expect "first cut" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
expect "first cut" "names the release kind" has "make a minor release: v2.3.0"

# --- the next run: the rc is in qualification, nightly.yml not run yet ---
: > "$work/images.log"
runs "$c3" "$c4"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "qualifying" "exits 0" eq "$status" 0
expect "qualifying" "asks the qualification gate" has "PENDING nightly: nightly.yml has not run on v2.3.0-rc.1"
expect "qualifying" "cuts nothing while one rc qualifies" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "qualifying" "publishes nothing" images_were ""
expect "qualifying" "outputs the verdict" grep -qxF "qualification=pending" "$work/train.out"

# --- a first failure: re-run once, not dropped ---
qualification flake "$c3"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "flake" "exits 0" eq "$status" 0
expect "flake" "asks for the one retry" grep -qxF "qualification_rerun=nightly:upgrade-test:flake" "$work/train.out"
expect "flake" "names the rc to re-run it on" grep -qxF "qualifying_rc=v2.3.0-rc.1" "$work/train.out"
expect "flake" "drops nothing" eq "$(remote_tag v2.3.0-rc.1-discarded)" ""

# --- infra: re-run, never red ---
qualification infra "$c3"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "infra" "exits 0" eq "$status" 0
expect "infra" "outputs infra" grep -qxF "qualification=infra" "$work/train.out"
expect "infra" "asks for a re-run" grep -qxF "qualification_rerun=nightly:upgrade-test:infra" "$work/train.out"
expect "infra" "drops nothing" eq "$(remote_tag v2.3.0-rc.1-discarded)" ""

# --- confirmed red: dropped, rc.2 cut ---
qualification fail "$c3"
runs "$c3" "$c4"
: > "$work/images.log"
train
expect "red" "exits 0" eq "$status" 0
expect "red" "marks rc.1 discarded" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c3"
expect "red" "cuts rc.2 of the same minor on main" eq "$(remote_tag v2.3.0-rc.2)" "$c4"
expect "red" "discards then publishes" images_were "discard-rc v2.3.0-rc.1|rc v2.3.0-rc.2"
out="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0-rc.1-discarded)"
expect "red" "the discard tag records the failing job" has "upgrade-test: failed again on its retry"

# --- rc.2 red too: the release/v2.3.0 convergence branch, from rc.2 ---
qualification fail "$c4"
: > "$work/images.log"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "converge" "exits 0" eq "$status" 0
expect "converge" "marks rc.2 discarded" eq "$(remote_tag v2.3.0-rc.2-discarded)" "$c4"
expect "converge" "makes release/v2.3.0 from rc.2" eq "$(git -C "$work/remote.git" rev-parse -q --verify refs/heads/release/v2.3.0)" "$c4"
expect "converge" "outputs it" grep -qxF "convergence_branch=release/v2.3.0" "$work/train.out"
expect "converge" "cuts no rc.3 before a fix is on it" eq "$(remote_tag v2.3.0-rc.3)" ""

# A fix cherry-picked onto the branch, green on release.yml alone.
(
  cd "$work/seed"
  git fetch -q origin release/v2.3.0
  git checkout -q -b release/v2.3.0 FETCH_HEAD
  git commit -q --allow-empty -m "fix: the regression" -m "Backported-from: 1234567"
  git push -q origin release/v2.3.0
  git checkout -q main
)
fix="$(git -C "$work/remote.git" rev-parse refs/heads/release/v2.3.0)"
cat > "$work/runs.json" <<EOF
{"runs":[{"id":"b","commit":"$fix","ref":"release/v2.3.0","startedAt":"$(iso $((now - 600)))","jobs":[{"workflowPath":".github/workflows/release.yml","jobId":"release","conclusion":"success"}]}]}
EOF
: > "$work/images.log"
train
expect "converge, fixed" "exits 0" eq "$status" 0
expect "converge, fixed" "cuts rc.3 on the branch's fix" eq "$(remote_tag v2.3.0-rc.3)" "$fix"
expect "converge, fixed" "says where it was cut" has "converges on release/v2.3.0"
expect "converge, fixed" "publishes it" images_were "rc v2.3.0-rc.3"

# rc.3 red as well: RELEASE_MAX_RC_ATTEMPTS (3) is spent; a person decides.
qualification fail "$fix"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "escalated" "exits 1" eq "$status" 1
expect "escalated" "says so" has "has had 3 release candidates dropped, RELEASE_MAX_RC_ATTEMPTS (3)"
expect "escalated" "outputs it" grep -qxF "escalated_version=v2.3.0" "$work/train.out"
expect "escalated" "cuts no rc.4" eq "$(remote_tag v2.3.0-rc.4)" ""

# A qualified rc.3 is promoted from the branch, and approve deletes it.
setup ""
tag_remote v2.3.0-rc.1 "$c1"
tag_remote v2.3.0-rc.1-discarded "$c1"
tag_remote v2.3.0-rc.2 "$c2"
tag_remote v2.3.0-rc.2-discarded "$c2"
git -C "$work/seed" push -q origin "$c2:refs/heads/release/v2.3.0"
tag_remote v2.3.0-rc.3 "$c2"
qualification pass "$c2"
train
expect "rc.3 qualifies" "stages v2.3.0-pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
approve() { # approve <approve|reject> <version> [VAR=value...]
  local what="$1" v="$2"
  shift 2
  status=0
  out="$(cd "$work/clone" && env RELEASE_IMAGES="$work/images.sh" "$@" bash "$train" "$what" "$v" 2>&1)" || status=$?
}
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "rc.3 approved" "exits 0" eq "$status" 0
expect "rc.3 approved" "deletes release/v2.3.0" eq "$(git -C "$work/remote.git" rev-parse -q --verify refs/heads/release/v2.3.0 || true)" ""

# --- the legacy rc RELEASE_NEVER_PROMOTE names is dropped without qualifying ---
setup "v1.14.0 v2.3.0-rc.1"
tag_remote v2.3.0-rc.1 "$c1" 40
runs "$c3"
train
expect "never-promote" "exits 0" eq "$status" 0
expect "never-promote" "marks v2.3.0-rc.1 discarded" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c1"
expect "never-promote" "cuts rc.2 of the same minor, not a new minor" eq "$(remote_tag v2.3.0-rc.2)" "$c3"
expect "never-promote" "discards then publishes" images_were "discard-rc v2.3.0-rc.1|rc v2.3.0-rc.2"
expect "never-promote" "does not skip to v2.4" eq "$(remote_tag v2.4.0-rc.1)" ""

# --- a placeholder release: its minor is used up, but notes skip it ---
# v2.5.0 is tagged on main only so main and the train skip to v2.6, and
# RELEASE_NEVER_PROMOTE names it (scripts/release.conf).
setup "v2.5.0"
tag_remote v2.4.0 "$c1"
tag_remote v2.5.0 "$c2"
runs "$c3"
train
expect "placeholder" "exits 0" eq "$status" 0
expect "placeholder" "targets v2.6.0, notes from v2.4.0" has "next minor v2.6.0; notes from v2.4.0"
expect "placeholder" "never patches the placeholder" eq "$(remote_tag v2.5.1-rc.1)" ""
expect "placeholder" "cuts v2.6.0-rc.1 after the placeholder" eq "$(remote_tag v2.6.0-rc.1)" "$c3"
expect "placeholder" "cuts no v2.5.0 rc" eq "$(remote_tag v2.5.0-rc.1)" ""
expect "placeholder" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
expect "placeholder" "publishes only the rc" images_were "rc v2.6.0-rc.1"

# --- a stale RELEASE_MAJOR never moves the version backwards ---
# v3.0.0 is promoted while release.conf still says RELEASE_MAJOR=2 (and
# RELEASE_FLOOR_MINOR=3, which is v2's floor, not v3's).
setup ""
tag_remote v3.0.0 "$c1"
runs "$c3"
train
expect "stale major" "exits 0" eq "$status" 0
expect "stale major" "says RELEASE_MAJOR is behind" has "RELEASE_MAJOR 2 is behind the released v3"
expect "stale major" "targets v3.1.0, notes from v3.0.0" has "next minor v3.1.0; notes from v3.0.0"
expect "stale major" "cuts v3.1.0-rc.1" eq "$(remote_tag v3.1.0-rc.1)" "$c3"
expect "stale major" "cuts no v2 rc" eq "$(remote_tag v2.3.0-rc.1)" ""
expect "stale major" "publishes only the rc" images_were "rc v3.1.0-rc.1"

# --- a dropped rc with nothing newer known-good: nothing to cut ---
setup "v2.3.0-rc.1"
tag_remote v2.3.0-rc.1 "$c3" 40
runs "$c2" "$c3"
train
expect "nothing newer" "exits 0" eq "$status" 0
expect "nothing newer" "says there is nothing to cut" has "no new LKG commit on main since ${c3:0:12}; nothing to cut"
expect "nothing newer" "cuts no rc.2 on an older green commit" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "nothing newer" "only marks the dropped rc" images_were "discard-rc v2.3.0-rc.1"

# --- grain/rc-hold: held while open, released after completion, absent cuts ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification fail "$c2"
rc_hold held "$c2" "v2.3.0-rc.2 is held until these blockers of v2.3.0-rc.1 land: task 101."
runs "$c2" "$c3"
: > "$work/images.log"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, held" "exits 0" eq "$status" 0
expect "rc-hold, held" "drops rc.1 in phase 1" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c2"
expect "rc-hold, held" "says rc.2 is held with its summary" \
  has "phase 2: v2.3.0-rc.2 is held by grain/rc-hold on ${c2:0:12}: v2.3.0-rc.2 is held until these blockers of v2.3.0-rc.1 land: task 101."
expect "rc-hold, held" "emits rc_held=v2.3.0-rc.2" grep -qxF "rc_held=v2.3.0-rc.2" "$work/train.out"
expect "rc-hold, held" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "rc-hold, held" "leaves candidate on rc.1" eq "$(remote_candidate)" "$c2"
expect "rc-hold, held" "only marks rc.1 discarded" images_were "discard-rc v2.3.0-rc.1"

# Still held on a later run (already discarded), even if another app posts a
# completed check run of the same name.
jq -cn --arg sha "$c2" --arg t1 "$(iso $((now - 300)))" --arg t2 "$(iso $((now - 60)))" \
  '{check_runs:[
    {id:1,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t1,output:{title:"Holding v2.3.0-rc.2",summary:"still waiting on task 101"},app:{id:42,slug:"grain-test"}},
    {id:2,name:"grain/rc-hold",head_sha:$sha,status:"completed",conclusion:"success",started_at:$t2,output:{title:"Done",summary:"forged"},app:{id:15368,slug:"github-actions"}}
  ]}' > "$work/hold-checks.json"
: > "$work/images.log"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, foreign completion" "exits 0" eq "$status" 0
expect "rc-hold, foreign completion" "still held by grain's check" \
  has "phase 2: v2.3.0-rc.2 is held by grain/rc-hold on ${c2:0:12}: still waiting on task 101"
expect "rc-hold, foreign completion" "emits rc_held=v2.3.0-rc.2" grep -qxF "rc_held=v2.3.0-rc.2" "$work/train.out"
expect "rc-hold, foreign completion" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "rc-hold, foreign completion" "publishes nothing" images_were ""

# Blockers land: the newest grain/rc-hold check completes, so rc.2 is cut.
rc_hold completed "$c2"
: > "$work/images.log"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, completed" "exits 0" eq "$status" 0
expect "rc-hold, completed" "cuts rc.2 once completed" eq "$(remote_tag v2.3.0-rc.2)" "$c3"
expect "rc-hold, completed" "moves candidate onto rc.2" eq "$(remote_candidate)" "$c3"
expect "rc-hold, completed" "publishes rc.2" images_were "rc v2.3.0-rc.2"
expect "rc-hold, completed" "emits no rc_held" no_line '^rc_held=' "$work/train.out"

# Absent: no grain/rc-hold check run at all means no hold.
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
tag_remote v2.3.0-rc.1-discarded "$c2" 2
rc_hold none
runs "$c2" "$c3"
: > "$work/images.log"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, absent" "exits 0" eq "$status" 0
expect "rc-hold, absent" "cuts rc.2 with no hold check" eq "$(remote_tag v2.3.0-rc.2)" "$c3"
expect "rc-hold, absent" "publishes rc.2" images_were "rc v2.3.0-rc.2"
expect "rc-hold, absent" "emits no rc_held" no_line '^rc_held=' "$work/train.out"

# No token and no file: the hold cannot be read, and the train says so
# rather than passing over it in silence.
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
tag_remote v2.3.0-rc.1-discarded "$c2" 2
runs "$c2" "$c3"
train RELEASE_HOLD_CHECKS_JSON= GITHUB_TOKEN= GH_TOKEN=
expect "rc-hold, no token" "exits 0" eq "$status" 0
expect "rc-hold, no token" "says it has no grain/rc-hold to read" has "no GitHub token, so no grain/rc-hold check run to read on ${c2:0:12}"
expect "rc-hold, no token" "cuts rc.2" eq "$(remote_tag v2.3.0-rc.2)" "$c3"

# Hardening: RELEASE_QUALIFICATION_APP unset refuses rather than trust a foreign
# completion, numeric app id matches, whitespace-only summary still holds, and a
# malformed check-run response fails closed.
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
tag_remote v2.3.0-rc.1-discarded "$c2" 2
runs "$c2" "$c3"
jq -cn --arg sha "$c2" --arg t1 "$(iso $((now - 300)))" --arg t2 "$(iso $((now - 60)))" \
  '{check_runs:[
    {id:1,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t1,output:{title:"Holding v2.3.0-rc.2",summary:"waiting on task 101"},app:{id:42,slug:"grain-test"}},
    {id:2,name:"grain/rc-hold",head_sha:$sha,status:"completed",conclusion:"success",started_at:$t2,output:{title:"Done",summary:"forged"},app:{id:15368,slug:"github-actions"}}
  ]}' > "$work/hold-checks.json"
: > "$work/images.log"
train RELEASE_QUALIFICATION_APP=
expect "rc-hold, no app configured" "exits 1" eq "$status" 1
expect "rc-hold, no app configured" "says why" has "RELEASE_QUALIFICATION_APP is unset, so no grain/rc-hold check run can be trusted"
expect "rc-hold, no app configured" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "rc-hold, no app configured" "publishes nothing" images_were ""

jq -cn --arg sha "$c2" --arg t "$(iso $((now - 300)))" \
  '{check_runs:[{id:1,name:"grain/rc-hold",head_sha:$sha,status:"in_progress",started_at:$t,output:{title:"Holding v2.3.0-rc.2",summary:"  \n\t  "},app:{id:42,slug:"grain-test"}}]}' \
  > "$work/hold-checks.json"
: > "$work/train.out"
train RELEASE_QUALIFICATION_APP=42 RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "rc-hold, whitespace summary & numeric app" "exits 0" eq "$status" 0
expect "rc-hold, whitespace summary & numeric app" "falls back to title instead of skipping hold" \
  has "phase 2: v2.3.0-rc.2 is held by grain/rc-hold on ${c2:0:12}: Holding v2.3.0-rc.2"
expect "rc-hold, whitespace summary & numeric app" "emits rc_held=v2.3.0-rc.2" grep -qxF "rc_held=v2.3.0-rc.2" "$work/train.out"
expect "rc-hold, whitespace summary & numeric app" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""

echo '{"message":"bad gateway"}' > "$work/hold-checks.json"
train
expect "rc-hold, malformed json" "exits 1" eq "$status" 1
expect "rc-hold, malformed json" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""

# --- a same-named check run from another app qualifies nothing ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification pass "$c2" github-actions
runs "$c2" "$c4"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "foreign check run" "exits 0" eq "$status" 0
expect "foreign check run" "says it ignored it" has "IGNORED grain/release-qualification: 1 from github-actions, not grain-test"
expect "foreign check run" "still waits for grain's own" has "Qualification: PENDING"
expect "foreign check run" "stages no release" eq "$(remote_tag v2.3.0-pending)" ""
expect "foreign check run" "publishes nothing" images_were ""
qualification pass "$c2"
train RELEASE_QUALIFICATION_APP=
expect "no app configured" "exits 1" eq "$status" 1
expect "no app configured" "says why" has "CHECK_QUALIFICATION_APP is unset"
expect "no app configured" "stages no release" eq "$(remote_tag v2.3.0-pending)" ""

# --- a hand-cut rc.1 of the next version still waits out the interval ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
tag_remote v2.4.0-rc.1 "$c3" 1
qualification pass "$c2"
runs "$c2" "$c4"
train
expect "hand-cut next rc.1" "exits 0" eq "$status" 0
expect "hand-cut next rc.1" "still waits RELEASE_MIN_INTERVAL_MINUTES" has "under RELEASE_MIN_INTERVAL_MINUTES (120)"
expect "hand-cut next rc.1" "so cuts no v2.4.0-rc.2 inside it" eq "$(remote_tag v2.4.0-rc.2)" ""

# --- qualification passes: staged as a pending release ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
qualification pass "$c2"
runs "$c2" "$c4"
: > "$work/train.out"
train RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "qualified" "exits 0" eq "$status" 0
expect "qualified" "tags v2.3.0-pending on the rc's commit" eq "$(remote_tag v2.3.0-pending)" "$c2"
expect "qualified" "pushes no v2.3.0" eq "$(remote_tag v2.3.0)" ""
expect "qualified" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
expect "qualified" "stages its images; promotes nothing" images_were "pending v2.3.0"
expect "qualified" "outputs the pending version" grep -qxF "pending_version=v2.3.0" "$work/train.out"
expect "qualified" "says a person approves it" has "no notification that v2.3.0 is ready to promote: approve it with \`grain release approve v2.3.0\`"
expect "qualified" "waits RELEASE_MIN_INTERVAL_MINUTES before the next rc" has "under RELEASE_MIN_INTERVAL_MINUTES (120)"
expect "qualified" "so cuts no v2.4.0-rc.1 yet" eq "$(remote_tag v2.4.0-rc.1)" ""
msg="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0-pending)"
out="$msg"
expect "qualified" "the pending tag names the rc it came from" has "From v2.3.0-rc.1, which passed Stage 1"
expect "qualified" "and records the qualification for approve" has "Qualification: PASS"

# --- later, while v2.3.0 is pending, the next minor's rc is cut ---
: > "$work/images.log"
train RELEASE_MIN_INTERVAL_MINUTES=0
expect "pending, next rc" "exits 0" eq "$status" 0
expect "pending, next rc" "says v2.3.0 is pending" has "v2.3.0 (${c2:0:12}, from v2.3.0-rc.1) is pending a person's approval"
expect "pending, next rc" "keeps it pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
expect "pending, next rc" "cuts v2.4.0-rc.1 after it" eq "$(remote_tag v2.4.0-rc.1)" "$c4"
expect "pending, next rc" "publishes the rc" images_were "rc v2.4.0-rc.1"

# --- the next rc qualifying waits behind the pending release ---
qualification pass "$c4"
: > "$work/images.log"
train
expect "pending, next rc passes" "exits 0" eq "$status" 0
expect "pending, next rc passes" "says it waits" has "v2.4.0-rc.1 qualified, but v2.3.0 is still pending approval"
expect "pending, next rc passes" "stages no v2.4.0-pending" eq "$(remote_tag v2.4.0-pending)" ""
expect "pending, next rc passes" "promotes nothing" images_were ""

# --- approve without a person: refused ---
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=agent GRAIN_CI_REQUESTED_BY=1236
expect "approve by an agent" "exits 1" eq "$status" 1
expect "approve by an agent" "says only a person may" has "only a person may approve v2.3.0"
expect "approve by an agent" "tags nothing" eq "$(remote_tag v2.3.0)" ""
approve approve v2.3.0
expect "approve by nobody" "is refused too" eq "$status" 1

# --- a dry-run approve leaves the checkout's tags as they were ---
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human DRY_RUN=true
expect "approve, dry run" "exits 0" eq "$status" 0
expect "approve, dry run" "pushes no v2.3.0" eq "$(remote_tag v2.3.0)" ""
expect "approve, dry run" "leaves no local v2.3.0 tag" eq "$(git -C "$work/clone" tag -l v2.3.0)" ""
expect "approve, dry run" "puts the local v2.3.0-pending back" eq "$(git -C "$work/clone" rev-parse -q --verify 'refs/tags/v2.3.0-pending^{commit}')" "$c2"
expect "approve, dry run" "puts the local latest back" eq "$(git -C "$work/clone" rev-parse -q --verify 'refs/tags/latest^{commit}')" "$c0"

# --- a person approves a qualified release: promoted, no soak asked ---
: > "$work/images.log"
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "approve" "exits 0" eq "$status" 0
expect "approve" "tags v2.3.0 on the pending commit" eq "$(remote_tag v2.3.0)" "$c2"
expect "approve" "moves latest onto it" eq "$(remote_tag latest)" "$c2"
expect "approve" "deletes v2.3.0-pending" eq "$(remote_tag v2.3.0-pending)" ""
expect "approve" "retags the staged images" images_were "promote v2.3.0"
expect "approve" "says it was qualified" has "v2.3.0-rc.1, ${c2:0:12}, qualified"
msg="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0)"
out="$msg"
expect "approve" "the release tag names who approved it" has "approved by alice@example.com"
expect "approve" "and no short-soak override" eval '! has "short soak"'
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human
expect "approve twice" "is refused: nothing is pending" eq "$status" 1

# --- after approval the train carries on with the next minor ---
: > "$work/images.log"
train
expect "after approve" "v2.4.0-rc.1 may now be staged" eq "$(remote_tag v2.4.0-pending)" "$c4"
expect "after approve" "stages it" images_were "pending v2.4.0"

# --- a person rejects ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
qualification pass "$c2"
runs "$c2" "$c3"
train
expect "reject" "v2.3.0 is pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
train FORCE=true
expect "reject" "v2.4.0-rc.1 was cut on top of it" eq "$(remote_tag v2.4.0-rc.1)" "$c3"
: > "$work/images.log"
approve reject v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=agent
expect "reject by an agent" "is refused" eq "$status" 1
approve reject v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "reject" "exits 0" eq "$status" 0
expect "reject" "deletes v2.3.0-pending" eq "$(remote_tag v2.3.0-pending)" ""
expect "reject" "pushes no v2.3.0" eq "$(remote_tag v2.3.0)" ""
expect "reject" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
expect "reject" "marks rc.1 discarded" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c2"
expect "reject" "drops the next minor's rc cut on top of it" eq "$(remote_tag v2.4.0-rc.1-discarded)" "$c3"
expect "reject" "marks the images" images_were "discard-rc v2.3.0-rc.1|discard-pending v2.3.0|discard-rc v2.4.0-rc.1"
: > "$work/images.log"
runs "$c2" "$c3" "$c4"
train
expect "after reject" "cuts rc.2 of the same minor" eq "$(remote_tag v2.3.0-rc.2)" "$c4"
expect "after reject" "publishes it" images_were "rc v2.3.0-rc.2"

# --- staging the release after a placeholder writes notes from the real one ---
setup "v2.5.0"
tag_remote v2.4.0 "$c1"
tag_remote v2.5.0 "$c2"
tag_remote v2.6.0-rc.1 "$c3" 3
qualification pass "$c3"
runs "$c3" "$c4"
train
expect "after a placeholder" "exits 0" eq "$status" 0
expect "after a placeholder" "tags v2.6.0-pending on the rc's commit" eq "$(remote_tag v2.6.0-pending)" "$c3"
expect "after a placeholder" "writes v2.6.0's notes from v2.4.0, not the placeholder" has "release-notes task for v2.6.0 (notes from v2.4.0)"

# --- a -pending tag from the soak era still needs its soak ---
setup ""
tag_remote v2.3.0-rc.1 "$c2" 3
tag_remote v2.3.0-pending "$c2" 1
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "approve, soak-era" "is refused on the tags' 2h" has "v2.3.0 soaked 2h00m of 24h, under the 22h minimum"
expect "approve, soak-era" "tags nothing" eq "$(remote_tag v2.3.0)" ""
: > "$work/images.log"
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com RELEASE_SHORT_SOAK_OK=true
expect "approve, short_soak_ok" "exits 0" eq "$status" 0
expect "approve, short_soak_ok" "tags v2.3.0" eq "$(remote_tag v2.3.0)" "$c2"
out="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0)"
expect "approve, short_soak_ok" "the release tag records the override" \
  has "approved by alice@example.com; short soak (soaked 2h00m of 24h, under the 22h minimum) accepted with short_soak_ok: true"

# --- approving a minor release that carries a breaking change ---
setup "" c1 "feat(api)!: drop the v1 endpoints" c3
tag_remote v2.3.0-rc.1 "$c2" 3
qualification pass "$c2"
runs "$c2" "$c3"
train
expect "breaking" "v2.3.0 is staged" eq "$(remote_tag v2.3.0-pending)" "$c2"
: > "$work/images.log"
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "approve, breaking" "is refused" eq "$status" 1
expect "approve, breaking" "names the change" has "feat(api)!: drop the v1 endpoints"
expect "approve, breaking" "says what would let it through" has "approve it with RELEASE_ALLOW_BREAKING=v2.3.0; nothing was done"
expect "approve, breaking" "tags nothing" eq "$(remote_tag v2.3.0)" ""
expect "approve, breaking" "moves no latest" eq "$(remote_tag latest)" "$c0"
expect "approve, breaking" "publishes nothing" images_were ""
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com RELEASE_ALLOW_BREAKING=v2.3.0
expect "approve, RELEASE_ALLOW_BREAKING" "exits 0" eq "$status" 0
expect "approve, RELEASE_ALLOW_BREAKING" "tags v2.3.0" eq "$(remote_tag v2.3.0)" "$c2"
out="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0)"
expect "approve, RELEASE_ALLOW_BREAKING" "the release tag records the override" \
  has "approved by alice@example.com; breaking changes since v2.1.0 accepted with RELEASE_ALLOW_BREAKING=v2.3.0"

# --- a breaking change in the major release RELEASE_MAJOR asks for ---
setup "" c1 "feat(api)!: drop the v1 endpoints" c3
tag_remote v3.0.0-rc.1 "$c2" 3
(cd "$work/seed" && git tag -a v3.0.0-pending "$c2" -m "Release v3.0.0 pending approval" -m "Qualification: PASS" &&
  git push -q origin refs/tags/v3.0.0-pending)
approve approve v3.0.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "approve, unasked major" "is refused while release.conf says RELEASE_MAJOR=2" eq "$status" 1
approve approve v3.0.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com RELEASE_MAJOR=3
expect "approve, major" "exits 0" eq "$status" 0
expect "approve, major" "says why" has "a major release over v2.1.0, as RELEASE_MAJOR=3 asks"
expect "approve, major" "tags v3.0.0" eq "$(remote_tag v3.0.0)" "$c2"

# --- after a drop, the next cut must be strictly after the dropped commit ---
setup ""
tag_remote v2.3.0-rc.1 "$c3" 3
qualification fail "$c3"
runs "$c2" "$c3"
train
expect "strictly newer" "drops rc.1" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c3"
expect "strictly newer" "cuts no rc.2 at or behind the dropped rc" eq "$(remote_tag v2.3.0-rc.2)" ""

# --- the minimum interval between releases ---
setup ""
tag_remote v2.2.0 "$c1" 1
runs "$c3"
train
expect "interval" "exits 0" eq "$status" 0
expect "interval" "says the last release is too recent" has "v2.2.0 was tagged 60m ago, under RELEASE_MIN_INTERVAL_MINUTES (120)"
expect "interval" "cuts nothing" eq "$(remote_tag v2.3.0-rc.1)" ""
train RELEASE_MIN_INTERVAL_MINUTES=30
expect "interval, shorter" "cuts once the interval has passed" eq "$(remote_tag v2.3.0-rc.1)" "$c3"
setup ""
tag_remote v2.2.0 "$c1" 1
runs "$c3"
train FORCE=true
expect "interval, forced" "FORCE=true cuts inside the interval" eq "$(remote_tag v2.3.0-rc.1)" "$c3"

# --- every trunk release increments minor; docs/chores/tests alone wait ---
guard="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/patch-tag-guard.sh"
setup "" "fix(ui): keep the drawer open" "refactor(store): split the run queries" "docs: say how to approve"
runs "$c3"
train RELEASE_FLOOR_MINOR=0
expect "fixes make a minor" "exits 0" eq "$status" 0
expect "fixes make a minor" "cuts the next minor, not a patch" eq "$(remote_tag v2.2.0-rc.1)" "$c3"
expect "fixes make a minor" "never cuts v2.1.1-rc.1 from main" eq "$(remote_tag v2.1.1-rc.1)" ""
qualification pass "$c3"
train RELEASE_FLOOR_MINOR=0
expect "fixes make a minor" "the minor rc qualifies and is staged" eq "$(remote_tag v2.2.0-pending)" "$c3"
approve approve v2.2.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com RELEASE_FLOOR_MINOR=0
expect "fixes make a minor" "approve tags v2.2.0" eq "$(remote_tag v2.2.0)" "$c3"
status=0
out="$(cd "$work/clone" && bash "$guard" --latest v2.2.0 2>&1)" || status=$?
expect "fixes make a minor" "patch-tag-guard passes the promoted minor and --latest" eq "$status" 0

setup "" "fix(ui): a crash"
runs "$c1"
train RELEASE_FLOOR_MINOR=0
expect "fix alone" "a single fix: on main still cuts the next minor" eq "$(remote_tag v2.2.0-rc.1)" "$c1"
expect "fix alone" "no patch rc" eq "$(remote_tag v2.1.1-rc.1)" ""

# After a cherry-picked hotfix patch v2.1.1 on release/v2.1.1, main still
# cuts v2.2.0-rc.1 (never v2.1.2-rc.1).
setup "" "fix(api): hotfix" "fix(ui): another fix"
(
  cd "$work/seed"
  git checkout -q -b release/v2.1.1 v2.1.0
  git cherry-pick --allow-empty -x "$c1" >/dev/null
  GIT_COMMITTER_DATE="@$((now - 36 * 3600)) +0000" git tag -a v2.1.1 -m "Release v2.1.1"
  git push -q origin release/v2.1.1 refs/tags/v2.1.1
  git checkout -q main
)
runs "$c2"
train RELEASE_FLOOR_MINOR=0
expect "after patch branch" "exits 0" eq "$status" 0
expect "after patch branch" "cuts v2.2.0-rc.1 on main after the patch branch" eq "$(remote_tag v2.2.0-rc.1)" "$c2"
expect "after patch branch" "never cuts v2.1.2-rc.1" eq "$(remote_tag v2.1.2-rc.1)" ""
expect "after patch branch" "writes notes from v2.1.1" has "notes from v2.1.1"

setup "" "fix: a crash" "feat(api): a new endpoint" "chore: tidy"
runs "$c3"
train RELEASE_FLOOR_MINOR=0
expect "feature" "a feat: cuts the next minor" eq "$(remote_tag v2.2.0-rc.1)" "$c3"

setup "" "fix!: change the default" "test: more"
runs "$c2"
train RELEASE_FLOOR_MINOR=0
expect "breaking" "a breaking fix!: cuts the next minor" eq "$(remote_tag v2.2.0-rc.1)" "$c2"

setup "" "docs: the README" "chore(deps): bump" "test: cover it"
runs "$c3"
: > "$work/train.out"
train RELEASE_FLOOR_MINOR=0 RELEASE_TRAIN_OUTPUT="$work/train.out"
expect "docs only" "exits 0" eq "$status" 0
expect "docs only" "releases nothing" eq "$(git -C "$work/remote.git" tag -l 'v2.*-rc.*')" ""
expect "docs only" "says it waits" has "holds only docs, chores, tests and the like; nothing to release"
expect "docs only" "outputs it" grep -qxF "release_waits=$c3" "$work/train.out"

setup "" "fix: a crash"
runs "$c1"
train
expect "minor under the floor" "a release past v2.1 under RELEASE_FLOOR_MINOR (3) steps up to v2.3.0" eq "$(remote_tag v2.3.0-rc.1)" "$c1"

# --- rollback: retract a bad release, latest goes back ---
setup "" "feat: one" "fix: two" "fix: three"
tag_remote v2.2.0 "$c1"
git -C "$work/seed" tag -fa latest "$c1" -m "Latest release v2.2.0" >/dev/null
git -C "$work/seed" push -q --force origin refs/tags/latest
rollback() { # rollback [VAR=value...] -- <args>
  local envs=()
  while [ $# -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  status=0
  out="$(cd "$work/clone" && env RELEASE_IMAGES="$work/images.sh" ${envs[@]+"${envs[@]}"} bash "$train" rollback "$@" 2>&1)" || status=$?
}
rollback GRAIN_CI_REQUESTED_BY_KIND=agent -- --bad v2.2.0
expect "rollback by an agent" "is refused" eq "$status" 1
expect "rollback by an agent" "says only a person may" has "only a person may rollback v2.2.0"
expect "rollback by an agent" "tags nothing" eq "$(remote_tag v2.2.0-retracted)" ""
rollback GRAIN_CI_REQUESTED_BY_KIND=human -- --bad v2.2.0 --to v2.3.0
expect "rollback forward" "is refused" eq "$status" 1
: > "$work/images.log"
: > "$work/train.out"
rollback GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com RELEASE_TRAIN_OUTPUT="$work/train.out" -- \
  --bad v2.2.0 --reason "loses tasks on upgrade"
expect "rollback" "exits 0" eq "$status" 0
expect "rollback" "tags v2.2.0-retracted on it" eq "$(remote_tag v2.2.0-retracted)" "$c1"
expect "rollback" "never moves v2.2.0" eq "$(remote_tag v2.2.0)" "$c1"
expect "rollback" "moves latest back to v2.1.0" eq "$(remote_tag latest)" "$c0"
expect "rollback" "marks the images, and moves :latest back" images_were "retract v2.2.0|promote v2.1.0"
expect "rollback" "outputs it" grep -qxF "retracted_version=v2.2.0" "$work/train.out"
out="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.2.0-retracted)"
expect "rollback" "the retracted tag says who and why" has "Retracted by alice@example.com: loses tasks on upgrade"
: > "$work/images.log"
rollback GRAIN_CI_REQUESTED_BY_KIND=human -- --bad v2.2.0
expect "rollback twice" "exits 0" eq "$status" 0
expect "rollback twice" "does nothing" has "already retracted and latest is on v2.1.0"
expect "rollback twice" "publishes nothing" images_were ""
# The train after a rollback: fixes alone never patch a retracted release.
runs "$c3"
train RELEASE_FLOOR_MINOR=0
expect "after rollback" "the next release is the next minor" eq "$(remote_tag v2.3.0-rc.1)" "$c3"
expect "after rollback" "no patch of the retracted release" eq "$(remote_tag v2.2.1-rc.1)" ""
expect "after rollback" "notes skip the retracted release" has "notes from v2.1.0"

# --- no release gate and no files: refused, nothing done ---
setup ""
status=0
out="$(cd "$work/clone" && env RELEASE_IMAGES="$work/images.sh" RELEASE_NOW="$now" bash "$train" 2>&1)" || status=$?
expect "no gate" "exits 1" eq "$status" 1
expect "no gate" "names the capability" has "release-gate-read"
expect "no gate" "tags nothing" eq "$(remote_tag v2.3.0-rc.1)" ""

# --- GitHub Actions: the verdicts are its workflow runs and jobs ---
# github_runs <commit>: GitHub's answer for each required workflow, green
# on commit, served where RELEASE_GITHUB_API_URL points.
github_runs() {
  local wf
  for wf in tests release; do
    mkdir -p "$work/www/gh/repos/o/r/actions/workflows/$wf.yml"
    printf '{"total_count":1,"workflow_runs":[{"id":1,"path":".github/workflows/%s.yml","head_branch":"main","head_sha":"%s","event":"push","status":"completed","conclusion":"success","run_started_at":"%s","updated_at":"%s","repository":{"full_name":"o/r"},"head_repository":{"full_name":"o/r"}}]}\n' \
      "$wf" "$1" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" \
      > "$work/www/gh/repos/o/r/actions/workflows/$wf.yml/runs"
  done
}
if [ -n "$api" ]; then
  setup ""
  runs
  github_runs "$c2"
  train RELEASE_RUNS_JSON= RELEASE_QUALIFICATION_RUNS_JSON= RELEASE_QUALIFICATION_CHECKS_JSON= GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$api/gh"
  expect "github actions" "exits 0" eq "$status" 0
  expect "github actions" "reads the workflow runs" has "read 2 GitHub Actions runs of o/r's tests release on main"
  expect "github actions" "cuts rc.1 on the commit GitHub Actions passed" eq "$(remote_tag v2.3.0-rc.1)" "$c2"

  # The rc's nightly.yml run, its jobs (a flake retried in a second
  # attempt) and the check run.
  mkdir -p "$work/www/gh/repos/o/r/actions/workflows/nightly.yml" "$work/www/gh/repos/o/r/actions/runs/7" "$work/www/gh/repos/o/r/commits/$c2"
  printf '{"total_count":1,"workflow_runs":[{"id":7,"head_sha":"%s"}]}' "$c2" > "$work/www/gh/repos/o/r/actions/workflows/nightly.yml/runs"
  {
    printf '{"total_count":16,"jobs":['
    first=1
    for name in "go, shuffled and repeated (1)" "state repository soak" "sustained load" "metadata and model scalability" \
      "merge liveness stress" "end-to-end long term tests" "long randomized cluster simulation" "postgres concurrency and stress" \
      "reverse tunnel multiplexer stress" "ui e2e, repeated (1)" "installer trixie and hostmode" "govulncheck" \
      "pkg/cli stress under load" "upgrade from previous versions"; do
      [ $first = 1 ] || printf ','
      first=0
      printf '{"run_id":7,"run_attempt":1,"workflow_name":"nightly","head_sha":"%s","name":"%s","status":"completed","conclusion":"%s","created_at":"%s"}' \
        "$c2" "$name" "$([ "$name" = govulncheck ] && echo failure || echo success)" "$(iso $((now - 3600)))"
    done
    printf ',{"run_id":7,"run_attempt":2,"workflow_name":"nightly","head_sha":"%s","name":"govulncheck","status":"completed","conclusion":"success","created_at":"%s"}' "$c2" "$(iso $((now - 1800)))"
    printf ']}'
  } > "$work/www/gh/repos/o/r/actions/runs/7/jobs"
  printf '{"check_runs":[{"id":9,"name":"grain/release-qualification","head_sha":"%s","status":"completed","conclusion":"success","started_at":"%s","app":{"id":42,"slug":"grain-test"}}]}' \
    "$c2" "$(iso $((now - 600)))" > "$work/www/gh/repos/o/r/commits/$c2/check-runs"
  : > "$work/images.log"
  train RELEASE_RUNS_JSON= RELEASE_QUALIFICATION_RUNS_JSON= RELEASE_QUALIFICATION_CHECKS_JSON= RELEASE_HOLD_CHECKS_JSON= GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$api/gh"
  expect "github actions, qualified" "exits 0" eq "$status" 0
  expect "github actions, qualified" "reads the retried job as a flake" has "PASS vulns: passed on its one retry (failure,success): a flake"
  expect "github actions, qualified" "stages the release" eq "$(remote_tag v2.3.0-pending)" "$c2"

  # Reading commits/<floor>/check-runs for grain/rc-hold over GitHub's API.
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 3
  tag_remote v2.3.0-rc.1-discarded "$c2" 2
  github_runs "$c3"
  mkdir -p "$work/www/gh/repos/o/r/commits/$c2"
  printf '{"check_runs":[{"id":11,"name":"grain/rc-hold","head_sha":"%s","status":"in_progress","started_at":"%s","output":{"title":"Holding v2.3.0-rc.2","summary":"waiting on task 55"},"app":{"id":42,"slug":"grain-test"}}]}' \
    "$c2" "$(iso $((now - 300)))" > "$work/www/gh/repos/o/r/commits/$c2/check-runs"
  : > "$work/train.out"
  train RELEASE_RUNS_JSON= RELEASE_HOLD_CHECKS_JSON= RELEASE_TRAIN_OUTPUT="$work/train.out" GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$api/gh"
  expect "github actions, rc-hold" "exits 0" eq "$status" 0
  expect "github actions, rc-hold" "holds rc.2 over HTTP" has "phase 2: v2.3.0-rc.2 is held by grain/rc-hold on ${c2:0:12}: waiting on task 55"
  expect "github actions, rc-hold" "emits rc_held" grep -qxF "rc_held=v2.3.0-rc.2" "$work/train.out"
  expect "github actions, rc-hold" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""
  printf '{"check_runs":[{"id":11,"name":"grain/rc-hold","head_sha":"%s","status":"completed","conclusion":"success","started_at":"%s","app":{"id":42,"slug":"grain-test"}}]}' \
    "$c2" "$(iso $((now - 60)))" > "$work/www/gh/repos/o/r/commits/$c2/check-runs"
  train RELEASE_RUNS_JSON= RELEASE_HOLD_CHECKS_JSON= GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$api/gh"
  expect "github actions, rc-hold completed" "cuts rc.2 once completed" eq "$(remote_tag v2.3.0-rc.2)" "$c3"

  # Flooding commits/<floor>/check-runs with 100 foreign runs per page fails closed
  # instead of dropping grain's hold off the end of page 1.
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 3
  tag_remote v2.3.0-rc.1-discarded "$c2" 2
  github_runs "$c3"
  mkdir -p "$work/www/gh/repos/o/r/commits/$c2"
  jq -cn --arg sha "$c2" --arg t "$(iso $((now - 60)))" \
    '{check_runs: [range(1; 101) | {id: ., name: "grain/rc-hold", head_sha: $sha, status: "completed", conclusion: "success", started_at: $t, app: {id: 15368, slug: "github-actions"}}]}' \
    > "$work/www/gh/repos/o/r/commits/$c2/check-runs"
  train RELEASE_RUNS_JSON= RELEASE_HOLD_CHECKS_JSON= RELEASE_GITHUB_PAGES=1 GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$api/gh"
  expect "github actions, rc-hold flooded" "exits 1 rather than fail open" eq "$status" 1
  expect "github actions, rc-hold flooded" "cuts no rc.2" eq "$(remote_tag v2.3.0-rc.2)" ""
fi

setup ""
train RELEASE_RUNS_JSON= RELEASE_CI_SOURCE=github
expect "github actions, no token" "exits 1" eq "$status" 1
expect "github actions, no token" "names the token" has "set GITHUB_TOKEN (actions: read)"
expect "github actions, no token" "tags nothing" eq "$(remote_tag v2.3.0-rc.1)" ""

setup ""
runs "$c3"
train RELEASE_CI_SOURCE=github
expect "github actions, from a file" "RELEASE_RUNS_JSON still stands in" eq "$(remote_tag v2.3.0-rc.1)" "$c3"

train RELEASE_QUALIFICATION_RUNS_JSON= RELEASE_CI_SOURCE=github
expect "github actions, no token for the rc" "exits 1" eq "$status" 1
expect "github actions, no token for the rc" "names the token" has "no GitHub token to read nightly.yml's runs on v2.3.0-rc.1"

train RELEASE_CI_SOURCE=jenkins
expect "unknown source" "is refused" has "RELEASE_CI_SOURCE is jenkins, not grain or github"

# --- a dry run pushes nothing and leaves the checkout as it was ---
setup ""
runs "$c3"
git -C "$work/clone" config --local --unset-all user.name 2>/dev/null || true
train DRY_RUN=true
expect "dry run" "exits 0" eq "$status" 0
expect "dry run" "says what it would push" has "[dry-run] would git push"
expect "dry run" "pushes no tag" eq "$(remote_tag v2.3.0-rc.1)" ""
expect "dry run" "moves no candidate" eq "$(remote_candidate)" ""
expect "dry run" "publishes nothing" images_were ""
expect "dry run" "leaves no local rc tag behind" eq "$(git -C "$work/clone" tag -l 'v2.3.0-rc.*')" ""
expect "dry run" "writes no user.name into .git/config" eq "$(git -C "$work/clone" config --local --get user.name || true)" ""
expect "dry run" "writes no user.email into .git/config" eq "$(git -C "$work/clone" config --local --get user.email || true)" ""
train
expect "a real run" "writes no identity into .git/config either" eq "$(git -C "$work/clone" config --local --get user.name || true)" ""

# --- approve promotes only a commit on main or release/vX.Y.Z ---
setup ""
(
  cd "$work/seed"
  git checkout -q -b side "$c1"
  git commit -q --allow-empty -m "not on main"
  git push -q origin side
  git checkout -q main
)
side="$(git -C "$work/seed" rev-parse side)"
tag_remote v2.3.0-rc.1 "$side"
(
  cd "$work/seed"
  git tag -a v2.3.0-pending "$side" -m "Release v2.3.0 pending approval" -m "Qualification: PASS"
  git push -q origin refs/tags/v2.3.0-pending
)
: > "$work/images.log"
approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "pending off main" "is refused" eq "$status" 1
expect "pending off main" "says why" has "which is on neither main nor release/v2.3.0"
expect "pending off main" "tags no v2.3.0" eq "$(remote_tag v2.3.0)" ""
expect "pending off main" "leaves latest on v2.1.0" eq "$(remote_tag latest)" "$c0"
expect "pending off main" "publishes nothing" images_were ""

# --- the release train's tag grant (RELEASE_PUSH_GRANT=train, playbook 0.4) ---
setup ""
runs "$c3"
train RELEASE_PUSH_GRANT=train
expect "tag grant" "exits 0" eq "$status" 0
expect "tag grant" "cuts v2.3.0-rc.1" eq "$(remote_tag v2.3.0-rc.1)" "$c3"
expect "tag grant" "force-moves candidate" eq "$(remote_candidate)" "$c3"

approve approve v2.3.0 RELEASE_PUSH_GRANT=train GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "tag grant, approve" "is refused" eq "$status" 2
expect "tag grant, approve" "says it is the approval's run" has "approve needs the approval's own run"
approve reject v2.3.0 RELEASE_PUSH_GRANT=train GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
expect "tag grant, reject" "is refused" eq "$status" 2

train RELEASE_PUSH_GRANT=everything
expect "tag grant, unknown" "is refused" has "RELEASE_PUSH_GRANT is everything, not train"

# A train that tried to push a promotion's ref is refused before the push,
# with or without the grant: a copy of the script whose cut pushes latest,
# v2.3.0 or main instead of the rc tag.
mutant() { # mutant <ref> [VAR=value...]: run a train whose rc push is the LKG to <ref>
  local ref="$1"
  shift
  mkdir -p "$work/mutant"
  cp "$(dirname "$train")/candidate-gate.sh" "$work/mutant/"
  sed "s|push \"refs/tags/\$new_rc\"|push --force \"\$lkg:$ref\"|" "$train" > "$work/mutant/release-from-trunk.sh"
  grep -qF "push --force \"\$lkg:$ref\"" "$work/mutant/release-from-trunk.sh" || { echo "mutant: the rc push was not found"; exit 1; }
  status=0
  out="$(cd "$work/clone" && env RELEASE_RUNS_JSON="$work/runs.json" \
    RELEASE_QUALIFICATION_RUNS_JSON="$work/qual-runs.json" RELEASE_QUALIFICATION_CHECKS_JSON="$work/qual-checks.json" \
    RELEASE_IMAGES="$work/images.sh" RELEASE_NOTES_TASK=off RELEASE_NOW="$now" "$@" bash "$work/mutant/release-from-trunk.sh" 2>&1)" || status=$?
}
for grant in "" train; do
  for ref in refs/tags/latest refs/tags/v2.3.0; do
    setup ""
    runs "$c3"
    mutant "$ref" RELEASE_PUSH_GRANT="$grant"
    expect "train pushing $ref${grant:+ (grant)}" "exits 1" eq "$status" 1
    expect "train pushing $ref${grant:+ (grant)}" "says why" has "the train never pushes $ref"
    expect "train pushing $ref${grant:+ (grant)}" "leaves latest on v2.1.0" eq "$(remote_tag latest)" "$c0"
    expect "train pushing $ref${grant:+ (grant)}" "pushes no v2.3.0" eq "$(remote_tag v2.3.0)" ""
    expect "train pushing $ref${grant:+ (grant)}" "moves no candidate" eq "$(remote_candidate)" ""
  done
done
setup ""
runs "$c3"
mutant refs/heads/main RELEASE_PUSH_GRANT=train
expect "grant pushing main" "exits 1" eq "$status" 1
expect "grant pushing main" "says it is outside the grant" has "refs/heads/main is outside the release train's tag grant"
expect "grant pushing main" "leaves main where it was" eq "$(git -C "$work/remote.git" rev-parse refs/heads/main)" "$c4"

echo
echo "$((cases - failures))/$cases passed"
[ "$failures" -eq 0 ]
