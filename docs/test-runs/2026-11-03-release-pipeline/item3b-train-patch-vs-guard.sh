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

# §5 item 3, beside it: a patch release the train cuts by itself from main
# (fixes alone since the last release make vX.Y.Z+1) against
# scripts/patch-tag-guard.sh, which release.yml and promote.yml run on the
# tag. A throwaway repository with a local bare origin.
#
#   bash item3b-train-patch-vs-guard.sh <path-to-grain-checkout> <scratch-dir>
set -uo pipefail

src="$(cd "$1" && pwd)"
scratch="$2"
rm -rf "$scratch"
mkdir -p "$scratch"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null HOME="$scratch/home"
export GIT_AUTHOR_NAME=walkthrough GIT_AUTHOR_EMAIL=walkthrough@example.com
export GIT_COMMITTER_NAME=walkthrough GIT_COMMITTER_EMAIL=walkthrough@example.com
unset GRAIN_API_URL GRAIN_API_TOKEN GITHUB_ACTIONS RELEASE_MAJOR RELEASE_FLOOR_MINOR RELEASE_NEVER_PROMOTE
mkdir -p "$HOME"
now="$(date +%s)"
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

git init -q --bare -b main "$scratch/origin.git"
git init -q -b main "$scratch/seed"
cd "$scratch/seed" || exit 1
mkdir scripts
printf 'RELEASE_MAJOR=3\nRELEASE_FLOOR_MINOR=0\n' > scripts/release.conf
git add scripts && git commit -q -m "chore: release.conf"
GIT_COMMITTER_DATE="@$((now - 48 * 3600)) +0000" git tag -a v3.2.0 -m "Release v3.2.0"
GIT_COMMITTER_DATE="@$((now - 48 * 3600)) +0000" git tag -a latest -m "Latest release v3.2.0"
git commit -q --allow-empty -m "fix(ui): keep the drawer open"
git commit -q --allow-empty -m "refactor(store): split the run queries"
git commit -q --allow-empty -m "docs: say how to approve"
git remote add origin "$scratch/origin.git"
git push -q origin main --tags
head="$(git rev-parse HEAD)"
git clone -q "$scratch/origin.git" "$scratch/clone"

printf '{"enabled":true,"runs":[' > "$scratch/runs.json"
first=1
for wf in tests release; do
  [ $first = 1 ] || printf ',' >> "$scratch/runs.json"
  first=0
  printf '{"id":"%s","commit":"%s","ref":"main","startedAt":"%s","finishedAt":"%s","conclusion":"success","jobs":[{"workflowPath":".grain/workflows/%s.yml","jobId":"%s","conclusion":"success","finishedAt":"%s"}]}' \
    "$wf" "$head" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" "$wf" "$wf" "$(iso $((now - 3600)))" >> "$scratch/runs.json"
done
printf ']}' >> "$scratch/runs.json"

echo "== the train on main (fix:, refactor:, docs: since v3.2.0)"
status=0
(cd "$scratch/clone" && env RELEASE_RUNS_JSON="$scratch/runs.json" RELEASE_CI_SOURCE=grain RELEASE_IMAGES=off \
  RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off RELEASE_NOW="$now" bash "$src/scripts/release-from-trunk.sh" 2>&1) || status=$?
echo "== exit $status"
rc="$(git -C "$scratch/origin.git" tag -l 'v3.2.1-rc.*' | tail -n 1)"
echo "-- the rc it cut: ${rc:-none}"

# What approve does once it qualifies: tag v3.2.1 on the rc's commit.
git -C "$scratch/origin.git" tag -a v3.2.1 -m "Release v3.2.1" "$head"
echo
echo "== patch-tag-guard.sh v3.2.1 (as release.yml runs it on the tag, and promote.yml on latest)"
status=0
(cd "$scratch/clone" && git fetch -q --tags && bash "$src/scripts/patch-tag-guard.sh" v3.2.1 2>&1) || status=$?
echo "== exit $status"
