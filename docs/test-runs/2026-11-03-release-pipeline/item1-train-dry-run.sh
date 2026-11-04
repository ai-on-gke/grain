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

# §5 item 1: the release train on this repository's real history, in a
# scratch clone whose origin is a local bare repository. Nothing is pushed
# anywhere but that bare repository, and the task checkout is never the
# train's working directory.
#
#   bash item1-train-dry-run.sh <path-to-grain-checkout> <scratch-dir>
#
# Prints every command's exit code and every tag message it made.
set -uo pipefail

src="$(cd "$1" && pwd)"
scratch="$2"
train="$src/scripts/release-from-trunk.sh"
rm -rf "$scratch"
mkdir -p "$scratch"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null HOME="$scratch/home"
mkdir -p "$HOME"
unset GRAIN_API_URL GRAIN_API_TOKEN GITHUB_ACTIONS GITHUB_TOKEN GH_TOKEN RELEASE_SHORT_SOAK_OK
now="$(date +%s)"
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

# origin: the checkout's origin/main as main, and its tags; no branches else.
git init -q --bare -b main "$scratch/origin.git"
git -C "$src" push -q "$scratch/origin.git" "refs/remotes/origin/main:refs/heads/main" 'refs/tags/*:refs/tags/*'
git clone -q "$scratch/origin.git" "$scratch/clone"
head="$(git -C "$scratch/clone" rev-parse HEAD)"
echo "== scratch origin $scratch/origin.git, main at ${head:0:12}; tags: $(git -C "$scratch/clone" tag | paste -sd' ')"

cat > "$scratch/images.sh" <<EOF
#!/bin/sh
echo "\$*" >> "$scratch/images.log"
EOF
chmod +x "$scratch/images.sh"
: > "$scratch/images.log"

# tests, release and nightly green on main's head.
printf '{"enabled":true,"runs":[' > "$scratch/runs.json"
first=1
for wf in tests release nightly; do
  [ $first = 1 ] || printf ',' >> "$scratch/runs.json"
  first=0
  printf '{"id":"%s","commit":"%s","ref":"main","startedAt":"%s","finishedAt":"%s","conclusion":"success","jobs":[{"workflowPath":".grain/workflows/%s.yml","jobId":"%s","conclusion":"success","finishedAt":"%s"}]}' \
    "$wf" "$head" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" "$wf" "$wf" "$(iso $((now - 3600)))" >> "$scratch/runs.json"
done
printf ']}' >> "$scratch/runs.json"
echo '{"enabled":true,"runs":[]}' > "$scratch/qual-runs.json"
echo '{"check_runs":[]}' > "$scratch/qual-checks.json"

run() { # run <label> <VAR=value...> -- <args...>
  local label="$1" status=0
  shift
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  echo
  echo "== $label: ${envs[*]} release-from-trunk.sh $*"
  (cd "$scratch/clone" && env RELEASE_RUNS_JSON="$scratch/runs.json" \
    RELEASE_QUALIFICATION_RUNS_JSON="$scratch/qual-runs.json" RELEASE_QUALIFICATION_CHECKS_JSON="$scratch/qual-checks.json" \
    RELEASE_CI_SOURCE=grain RELEASE_IMAGES="$scratch/images.sh" RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off RELEASE_QUALIFICATION_APP=grain \
    RELEASE_TRAIN_OUTPUT="$scratch/train.out" RELEASE_NOW="$now" RELEASE_MAJOR=9 RELEASE_FLOOR_MINOR=1 \
    RELEASE_MIN_INTERVAL_MINUTES=100000000 "${envs[@]}" bash "$train" "$@" 2>&1) || status=$?
  echo "== exit $status"
}
tagmsg() {
  echo "-- tag $1 on origin: $(git -C "$scratch/origin.git" rev-parse -q --verify "refs/tags/$1^{commit}" | cut -c1-12)"
  git -C "$scratch/origin.git" for-each-ref --format='%(contents)' "refs/tags/$1" | sed 's/^/   | /'
}

# 1a. A cut inside the minimum interval is held without FORCE ...
run "1a interval, no FORCE" --
# ... and FORCE cuts rc.1 anyway.
run "1b FORCE" FORCE=true --
tagmsg v9.1.0-rc.1
echo "-- candidate on origin: $(git -C "$scratch/origin.git" rev-parse -q --verify refs/heads/candidate | cut -c1-12)"

# 1c. A dry run while it qualifies: the tags of the clone are left as fetched.
run "1c DRY_RUN while qualifying" DRY_RUN=true --

# 1d. nightly.yml (all 14 Stage 1 jobs) and grain/release-qualification green on it.
jobs=""
for id in go-long staterepo-soak loadtest scalability merge-stress long-term-test long-sim postgres-stress \
  tunnel-stress ui-e2e-long installer-variants vulns cmd-grain-stress upgrade-test; do
  jobs="${jobs:+$jobs,}{\"workflowPath\":\".github/workflows/nightly.yml\",\"jobId\":\"$id\",\"conclusion\":\"success\"}"
done
printf '{"enabled":true,"runs":[{"id":"n1","commit":"%s","ref":"v9.1.0-rc.1","startedAt":"%s","jobs":[%s]}]}' \
  "$head" "$(iso $((now - 7200)))" "$jobs" > "$scratch/qual-runs.json"
printf '{"check_runs":[{"id":1,"name":"grain/release-qualification","head_sha":"%s","status":"completed","conclusion":"success","started_at":"%s","app":{"id":1,"slug":"grain"}}]}' \
  "$head" "$(iso "$now")" > "$scratch/qual-checks.json"
: > "$scratch/train.out"
run "1d qualification PASS" --
tagmsg v9.1.0-pending
echo "-- outputs:"; sed 's/^/   /' "$scratch/train.out"

# 1e. approve: an agent is refused; a person approves a qualified release
# without RELEASE_SHORT_SOAK_OK, since there is no soak to be short.
run "1e approve by an agent" GRAIN_CI_REQUESTED_BY_KIND=agent -- approve v9.1.0
run "1f approve by a person" GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=walkthrough@example.com -- approve v9.1.0
tagmsg v9.1.0
tagmsg latest
echo "-- v9.1.0-pending left on origin: $(git -C "$scratch/origin.git" tag -l v9.1.0-pending)"

# 1g. A soak-era -pending tag (what PR #515's train wrote: Soak-Seconds,
# Soak-Forced and the SHORT SOAK line) still needs its soak.
c="$(git -C "$scratch/clone" rev-parse HEAD~1)"
(
  cd "$scratch/clone" || exit 1
  export GIT_COMMITTER_NAME=walkthrough GIT_COMMITTER_EMAIL=walkthrough@example.com
  GIT_COMMITTER_DATE="@$((now - 7200)) +0000" git tag -a v9.2.0-rc.1 "$c" -m "Release candidate v9.2.0-rc.1"
  GIT_COMMITTER_DATE="@$((now - 3600)) +0000" git tag -a v9.2.0-pending "$c" -m "Release v9.2.0 pending approval" \
    -m "From v9.2.0-rc.1, soaked 1h00m of 24h (FORCE=true)." \
    -m "SHORT SOAK: under the 22h minimum; approving it needs short_soak_ok: true." \
    -m "Soak-Seconds: 3600
Soak-Forced: true"
  git push -q origin refs/tags/v9.2.0-rc.1 refs/tags/v9.2.0-pending
)
tagmsg v9.2.0-pending
run "1g soak-era approve without RELEASE_SHORT_SOAK_OK" GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=walkthrough@example.com -- approve v9.2.0
echo "-- v9.2.0 on origin after the refusal: '$(git -C "$scratch/origin.git" tag -l v9.2.0)'"
run "1h soak-era approve, RELEASE_SHORT_SOAK_OK=1 (not \"true\")" GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=walkthrough@example.com RELEASE_SHORT_SOAK_OK=1 -- approve v9.2.0
run "1i soak-era approve with RELEASE_SHORT_SOAK_OK=true" GRAIN_CI_REQUESTED_BY_KIND=human GRAIN_CI_REQUESTED_BY=walkthrough@example.com RELEASE_SHORT_SOAK_OK=true -- approve v9.2.0
tagmsg v9.2.0
echo
echo "== images asked for:"; sed 's/^/   /' "$scratch/images.log"
echo "== the scratch clone's local git config (no identity may be written):"
git -C "$scratch/clone" config --local --list | grep -i '^user\.' || echo "   (no user.* keys)"
