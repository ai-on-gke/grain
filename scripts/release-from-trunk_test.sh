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

# The staging server: a directory served over HTTP.
mkdir -p "$work/www/api"
echo '<!doctype html><html></html>' > "$work/www/index.html"
echo '[]' > "$work/www/api/tasks"
echo '[]' > "$work/www/api/persistent-sandboxes"
staging=""
if command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
  port=$((20000 + RANDOM % 20000))
  (cd "$work/www" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
  server_pid=$!
  for _ in $(seq 50); do
    curl -s -o /dev/null "http://127.0.0.1:$port/" && break
    sleep 0.1
  done
  staging="http://127.0.0.1:$port"
else
  echo "skip the soak-passing cases: python3 or curl is not installed"
fi

# setup <never-promote>: a fresh remote whose main is c0..c4, v2.1.0
# promoted (and latest) on c0, and a clone of it to run the train in.
# Sets c0..c4.
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
    git tag -a v2.1.0 -m "Release v2.1.0"
    git tag -a latest -m "Latest release v2.1.0"
    for i in 1 2 3 4; do git commit -q --allow-empty -m "c$i"; done
    git remote add origin "$work/remote.git"
    git push -q origin main --tags
  )
  c=()
  mapfile -t c < <(git -C "$work/seed" rev-list --reverse main)
  c0="${c[0]}" c1="${c[1]}" c2="${c[2]}" c3="${c[3]}" c4="${c[4]}"
  git clone -q "$work/remote.git" "$work/clone"
  echo '[]' > "$work/tasks.json"
  echo '[]' > "$work/sandboxes.json"
  runs
}

# tag_remote <tag> <commit> [<hours-ago>]: an annotated tag on the remote,
# made that long ago.
tag_remote() {
  (
    cd "$work/seed"
    GIT_COMMITTER_DATE="@$((now - ${3:-0} * 3600)) +0000" git tag -a "$1" "$2" -m "$1"
    git push -q origin "refs/tags/$1"
  )
}

# runs <commit>...: the CI listing, all three required suites green on
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

# staging <commit> [<events-json>]: grain-candidate running commit, onto
# which it refreshed 23h ago, an exploration run finished since, and
# /api/config reporting <tag>.
staging_on() {
  local deployed=$((now - 23 * 3600))
  cat > "$work/sandboxes.json" <<EOF
[{"name":"grain-candidate","state":"running","commit":"$1","restarts":1,
  "lastRefresh":{"at":"$(iso "$deployed")","to":"$1","reason":"candidate moved"},"events":${3:-[]}}]
EOF
  printf '{"version":{"release":"%s","commit":"%s"}}\n' "$2" "$1" > "$work/www/api/config"
  cat > "$work/tasks.json" <<EOF
[{"id":"700","title":"Daily exploratory UI testing and defect discovery on staging","state":"completed",
  "createdAt":"$(iso $((deployed + 60)))","startedAt":"$(iso $((deployed + 120)))"}]
EOF
}

train() { # train [VAR=value...]: run the train in the clone
  status=0
  out="$(cd "$work/clone" && env RELEASE_RUNS_JSON="$work/runs.json" RELEASE_SANDBOXES_JSON="$work/sandboxes.json" \
    RELEASE_TASKS_JSON="$work/tasks.json" RELEASE_IMAGES="$work/images.sh" RELEASE_NOTES_TASK=off \
    RELEASE_STAGING_URL="${staging:-http://127.0.0.1:1}" RELEASE_NOW="$now" "$@" bash "$train" 2>&1)" || status=$?
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

# --- a second run the same day: the rc is soaking ---
: > "$work/images.log"
runs "$c3" "$c4"
train
expect "soaking" "exits 0" eq "$status" 0
expect "soaking" "says the soak is in progress" has "soak in progress (0h / 24h)"
expect "soaking" "cuts nothing" eq "$(remote_tag v2.3.0-rc.2)" ""
expect "soaking" "publishes nothing" images_were ""

# --- the legacy rc RELEASE_NEVER_PROMOTE names is dropped without a soak ---
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
expect "placeholder" "targets v2.6.0, notes from v2.4.0" has "target v2.6.0; its notes are from v2.4.0"
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
expect "stale major" "targets v3.1.0, notes from v3.0.0" has "target v3.1.0; its notes are from v3.0.0"
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

if [ -n "$staging" ]; then
  # --- a soak that passes stages a pending release, then cuts the next minor ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 25
  git -C "$work/seed" push -q --force origin "$c2:refs/heads/candidate"
  staging_on "$c2" v2.3.0-rc.1
  runs "$c2" "$c4"
  train
  expect "soak passes" "exits 0" eq "$status" 0
  expect "soak passes" "tags v2.3.0-pending on the rc's commit" eq "$(remote_tag v2.3.0-pending)" "$c2"
  expect "soak passes" "pushes no v2.3.0" eq "$(remote_tag v2.3.0)" ""
  expect "soak passes" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
  expect "soak passes" "cuts v2.4.0-rc.1 on the newer known-good commit" eq "$(remote_tag v2.4.0-rc.1)" "$c4"
  expect "soak passes" "moves candidate onto that" eq "$(remote_candidate)" "$c4"
  expect "soak passes" "stages, then publishes the next rc; promotes nothing" images_were "pending v2.3.0|rc v2.4.0-rc.1"
  expect "soak passes" "says a person approves it" has "no notification that v2.3.0 is ready to promote: approve it with \`grain release approve v2.3.0\`"
  msg="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0-pending)"
  out="$msg"
  expect "soak passes" "the pending tag names the rc it came from" has "From v2.3.0-rc.1"
  expect "soak passes" "and the soak verdict" has "staging soak passed"

  # --- a second daily run while v2.3.0 is pending ---
  : > "$work/images.log"
  train
  expect "pending, next day" "exits 0" eq "$status" 0
  expect "pending, next day" "says v2.3.0 is pending" has "v2.3.0 (${c2:0:12}, from v2.3.0-rc.1) is pending a person's approval"
  expect "pending, next day" "does not promote it" eq "$(remote_tag v2.3.0)" ""
  expect "pending, next day" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
  expect "pending, next day" "keeps it pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
  expect "pending, next day" "lets v2.4.0-rc.1 soak" has "v2.4.0-rc.1 (${c4:0:12}) soak in progress"
  expect "pending, next day" "publishes nothing" images_were ""

  # --- the next minor's rc passing its soak waits behind the pending release ---
  git -C "$work/seed" fetch -q origin --tags
  git -C "$work/seed" tag -d v2.4.0-rc.1 >/dev/null
  git -C "$work/seed" push -q origin :refs/tags/v2.4.0-rc.1
  tag_remote v2.4.0-rc.1 "$c4" 25
  staging_on "$c4" v2.4.0-rc.1
  : > "$work/images.log"
  train
  expect "pending, next rc passes" "exits 0" eq "$status" 0
  expect "pending, next rc passes" "says it waits" has "v2.4.0-rc.1 passed its soak, but v2.3.0 is still pending approval"
  expect "pending, next rc passes" "stages no v2.4.0-pending" eq "$(remote_tag v2.4.0-pending)" ""
  expect "pending, next rc passes" "keeps v2.3.0 pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
  expect "pending, next rc passes" "promotes nothing" images_were ""

  # --- approve without a person: refused ---
  approve() { # approve <approve|reject> <version> [VAR=value...]
    local what="$1" v="$2"
    shift 2
    status=0
    out="$(cd "$work/clone" && env RELEASE_IMAGES="$work/images.sh" "$@" bash "$train" "$what" "$v" 2>&1)" || status=$?
  }
  approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=agent GRAIN_CI_REQUESTED_BY=1236
  expect "approve by an agent" "exits 1" eq "$status" 1
  expect "approve by an agent" "says only a person may" has "only a person may approve v2.3.0"
  expect "approve by an agent" "tags nothing" eq "$(remote_tag v2.3.0)" ""
  approve approve v2.3.0
  expect "approve by nobody" "is refused too" eq "$status" 1

  # --- a person approves: promoted ---
  : > "$work/images.log"
  approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=alice@example.com
  expect "approve" "exits 0" eq "$status" 0
  expect "approve" "tags v2.3.0 on the pending commit" eq "$(remote_tag v2.3.0)" "$c2"
  expect "approve" "moves latest onto it" eq "$(remote_tag latest)" "$c2"
  expect "approve" "deletes v2.3.0-pending" eq "$(remote_tag v2.3.0-pending)" ""
  expect "approve" "retags the staged images" images_were "promote v2.3.0"
  msg="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0)"
  out="$msg"
  expect "approve" "the release tag names who approved it" has "approved by alice@example.com"
  approve approve v2.3.0 GRAIN_CI_REQUESTED_BY_KIND=human
  expect "approve twice" "is refused: nothing is pending" eq "$status" 1

  # --- after approval the train carries on with the next minor ---
  : > "$work/images.log"
  train
  expect "after approve" "v2.4.0-rc.1 may now be staged" eq "$(remote_tag v2.4.0-pending)" "$c4"
  expect "after approve" "stages it" images_were "pending v2.4.0"

  # --- a person rejects: the failed-soak path ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 25
  staging_on "$c2" v2.3.0-rc.1
  runs "$c2" "$c3"
  train
  expect "reject" "v2.3.0 is pending" eq "$(remote_tag v2.3.0-pending)" "$c2"
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
  tag_remote v2.6.0-rc.1 "$c3" 25
  git -C "$work/seed" push -q --force origin "$c3:refs/heads/candidate"
  staging_on "$c3" v2.6.0-rc.1
  runs "$c3" "$c4"
  train
  expect "after a placeholder" "exits 0" eq "$status" 0
  expect "after a placeholder" "tags v2.6.0-pending on the rc's commit" eq "$(remote_tag v2.6.0-pending)" "$c3"
  expect "after a placeholder" "writes v2.6.0's notes from v2.4.0, not the placeholder" has "release-notes task for v2.6.0 (notes from v2.4.0)"
  expect "after a placeholder" "cuts v2.7.0-rc.1 next" eq "$(remote_tag v2.7.0-rc.1)" "$c4"

  # --- a soak that is pending waits ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 25
  staging_on "$c2" v2.3.0-rc.1
  echo '[]' > "$work/tasks.json"
  runs "$c4"
  train
  expect "soak pending" "exits 0" eq "$status" 0
  expect "soak pending" "says so" has "soak is pending"
  expect "soak pending" "promotes nothing" eq "$(remote_tag v2.3.0)" ""
  expect "soak pending" "cuts nothing" eq "$(remote_tag v2.3.0-rc.2)" ""

  # --- staging rolled back: dropped, rc.2 cut, and triage asked for ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 25
  staging_on "$c1" v2.3.0-rc.1
  runs "$c4"
  train
  expect "soak fails on staging" "exits 1, so the workflow files triage" eq "$status" 1
  expect "soak fails on staging" "says why" has "failed its soak on staging itself"
  expect "soak fails on staging" "promotes nothing" eq "$(remote_tag v2.3.0)" ""
  expect "soak fails on staging" "leaves latest alone" eq "$(remote_tag latest)" "$c0"
  expect "soak fails on staging" "marks rc.1 discarded" eq "$(remote_tag v2.3.0-rc.1-discarded)" "$c2"
  expect "soak fails on staging" "cuts rc.2 of the same minor" eq "$(remote_tag v2.3.0-rc.2)" "$c4"
  expect "soak fails on staging" "discards then publishes" images_were "discard-rc v2.3.0-rc.1|rc v2.3.0-rc.2"
  msg="$(git -C "$work/remote.git" for-each-ref --format='%(contents)' refs/tags/v2.3.0-rc.1-discarded)"
  out="$msg"
  expect "soak fails on staging" "the discard tag records the failing signal" has "supervisor: grain-candidate is running"

  # --- a soak failing only on a filed defect needs no triage ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c2" 25
  staging_on "$c2" v2.3.0-rc.1
  jq '. + [{"id":"710","title":"Blank inbox","state":"queued","generatedFrom":"700"}]' "$work/tasks.json" > "$work/t" && mv "$work/t" "$work/tasks.json"
  runs "$c4"
  train
  expect "soak fails on a defect" "exits 0: the defect is the triage" eq "$status" 0
  expect "soak fails on a defect" "drops rc.1 and cuts rc.2" eq "$(remote_tag v2.3.0-rc.2)" "$c4"

  # --- after a drop, the next cut must be strictly after the dropped commit ---
  setup ""
  tag_remote v2.3.0-rc.1 "$c3" 25
  staging_on "$c1" v2.3.0-rc.1
  runs "$c2" "$c3"
  train
  expect "strictly newer" "cuts no rc.2 at or behind the dropped rc" eq "$(remote_tag v2.3.0-rc.2)" ""
fi

# --- no release gate and no files: refused, nothing done ---
setup ""
status=0
out="$(cd "$work/clone" && env RELEASE_IMAGES="$work/images.sh" RELEASE_NOW="$now" bash "$train" 2>&1)" || status=$?
expect "no gate" "exits 1" eq "$status" 1
expect "no gate" "names the capability" has "release-gate-read"
expect "no gate" "tags nothing" eq "$(remote_tag v2.3.0-rc.1)" ""

# --- GitHub Actions: main's verdicts are its workflow runs ---
# github_runs <commit>: GitHub's answer for each required workflow, green
# on commit, served where RELEASE_GITHUB_API_URL points.
github_runs() {
  local wf
  for wf in tests release nightly; do
    mkdir -p "$work/www/gh/repos/o/r/actions/workflows/$wf.yml"
    printf '{"total_count":1,"workflow_runs":[{"id":1,"path":".github/workflows/%s.yml","head_branch":"main","head_sha":"%s","event":"%s","status":"completed","conclusion":"success","run_started_at":"%s","updated_at":"%s","repository":{"full_name":"o/r"},"head_repository":{"full_name":"o/r"}}]}\n' \
      "$wf" "$1" "$([ "$wf" = nightly ] && echo schedule || echo push)" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" \
      > "$work/www/gh/repos/o/r/actions/workflows/$wf.yml/runs"
  done
}
if [ -n "$staging" ]; then
  setup ""
  runs
  github_runs "$c2"
  train RELEASE_RUNS_JSON= GITHUB_ACTIONS=true GITHUB_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_API_URL="$staging/gh"
  expect "github actions" "exits 0" eq "$status" 0
  expect "github actions" "reads the workflow runs" has "read 3 GitHub Actions runs of o/r's tests release nightly on main"
  expect "github actions" "cuts rc.1 on the commit GitHub Actions passed" eq "$(remote_tag v2.3.0-rc.1)" "$c2"
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

train RELEASE_CI_SOURCE=jenkins
expect "unknown source" "is refused" has "RELEASE_CI_SOURCE is jenkins, not grain or github"

# --- a dry run pushes nothing ---
setup ""
runs "$c3"
train DRY_RUN=true
expect "dry run" "exits 0" eq "$status" 0
expect "dry run" "says what it would push" has "[dry-run] would git push"
expect "dry run" "pushes no tag" eq "$(remote_tag v2.3.0-rc.1)" ""
expect "dry run" "moves no candidate" eq "$(remote_candidate)" ""
expect "dry run" "publishes nothing" images_were ""

echo
echo "$((cases - failures))/$cases passed"
[ "$failures" -eq 0 ]
