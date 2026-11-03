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

# sweep.sh <symbols.txt>: docs/test-plans/2026-11-03.md §10 items 1 and 2,
# run with the sandbox's own grain from inside a real sandbox container
# (tests/e2e/sandbox_cli_edges_walkthrough_test.go feeds it through
# `docker exec -i`). Every command line prints as
#   $ grain <args>
#     exit=<n> secs=<t>
#     | <stdout and stderr, first lines>
# and the grep walk prints one tab-separated row per (root, symbol).
syms=$1
cd "$HOME" || exit 1

show() {
  local t0=$EPOCHREALTIME
  timeout "${TIMEOUT:-60}" grain "$@" > /tmp/sweep.out 2> /tmp/sweep.err
  local rc=$?
  local t1=$EPOCHREALTIME
  echo "\$ grain $*"
  printf '  exit=%s secs=%s stdout=%sB stderr=%sB\n' "$rc" "$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2f", b-a}')" "$(wc -c < /tmp/sweep.out)" "$(wc -c < /tmp/sweep.err)"
  { head -${LINES_SHOWN:-4} /tmp/sweep.out; head -${LINES_SHOWN:-4} /tmp/sweep.err; } | cut -c1-240 | sed 's/^/  | /'
}

echo "== environment"
echo "whoami=$(whoami) home=$HOME grain=$(command -v grain)"
grain --help 2>&1 | head -1
show ls src://
show ls src://pkg
show ls src://core

echo "== 1. grep walk: one symbol per package, pkg/orchestrator .. pkg/ui"
printf 'root\tpackage\tsymbol\texit\thits\ttruncated\tsecs\tfirst line\n'
while read -r pkg sym _; do
  for root in src://pkg src://; do
    t0=$EPOCHREALTIME
    grain grep "^(func|type) (\([^)]*\) )?$sym[ (\[]" "$root" > /tmp/g.out 2> /tmp/g.err
    rc=$?
    t1=$EPOCHREALTIME
    hits=$(grep -c "pkg/$pkg/\|^$pkg/" /tmp/g.out)
    trunc=$(cat /tmp/g.out /tmp/g.err | grep -c truncated)
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$root" "$pkg" "$sym" "$rc" "$hits" "$trunc" \
      "$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2f", b-a}')" "$(head -1 /tmp/g.out /tmp/g.err | grep -v '^==>' | grep . | head -1 | cut -c1-120)"
  done
done < "$syms"

echo "== 1b. the symbol only pkg/ui defines, whole-tree and by glob"
show grep 'func RotateLocalAPIToken' src://pkg
show grep 'func RotateLocalAPIToken' src://
show grep 'func RotateLocalAPIToken' src://pkg --glob 'ui/*.go'
show grep 'func RotateLocalAPIToken' src://core/pkg
show grep 'NoSuchSymbolAnywhere_2716' src://
echo "== 1c. --max"
show grep 'func ' src://pkg/ui --max 1
show grep 'func ' src://pkg/ui --max 1 --glob 'accesscode.go'
show grep 'func ' src:// --max 3
show grep 'func ' src://pkg/ui --max 0
show grep 'func ' src://pkg/ui --max -1
show grep 'func ' src://pkg/ui --max x
show grep 'func ' src://pkg/ui --max 1e3
show grep 'func RotateLocalAPIToken' src://pkg --max 100000

echo "== 2. grain log -n"
show log -n 5
show log -n
show log -n 0
show log --lines
show log -n 5 --tail 100
show log daemon -n 2

echo "== 2b. every command and subcommand with no argument"
LINES_SHOWN=2 TIMEOUT=30
for c in ls read grep "grep x" merge-queue status notify api quarantine metrics health log playbooks cap \
  "ci list" "ci show" "ci log" "ci test-history" "ci artifact" "ci artifact go" "ci wait" "ci run-job" "ci retrigger" \
  "pr status" "pr base-status" "pr open" "pr reply" "pr resolve" "pr review-comment" \
  "git revert" "handover write" \
  "task list" "task read" "task prompt" "task transcript" "task wait" "task message" "task propose" \
  "memory list" "memory read" "memory write" "memory doc-write" "memory doc" \
  "release-notes stats" "release-notes publish" "issue list" "issue read" "db query" \
  ci pr git handover task memory release-notes issue db; do
  # shellcheck disable=SC2086
  show $c
done
