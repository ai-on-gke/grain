#!/bin/bash
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

# Reconstruct one task per merged pull request from this repository's own
# git history: the pull request number, the branch, when work on it
# started (the first commit on the branch), when it stopped (the last),
# when it merged, how many commits it took, and the branch's own head
# subject as the task's title.
set -uo pipefail
cd /home/debian/work
git log --merges --format='%H %P %cI %s' main | while read -r merge p1 p2 mergedAt subject; do
  case "$subject" in
    "Merge pull request #"*) ;;
    *) continue ;;
  esac
  [ -n "${p2:-}" ] || continue
  pr=$(printf '%s' "$subject" | sed -n 's/^Merge pull request #\([0-9]*\) from \(.*\)$/\1/p')
  branch=$(printf '%s' "$subject" | sed -n 's/^Merge pull request #\([0-9]*\) from \(.*\)$/\2/p')
  [ -n "$pr" ] || continue
  # Oldest and newest commit on the branch, and the count.
  first=$(git log --format="%aI" "$p1..$p2" | tail -1)
  last=$(git log -1 --format='%aI' "$p2")
  title=$(git log -1 --format='%s' "$p2")
  n=$(git rev-list --count "$p1..$p2")
  [ -n "$first" ] || continue
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$pr" "$branch" "$first" "$last" "$mergedAt" "$n" "$p2" "$title"
done
