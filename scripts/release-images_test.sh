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

# Unit tests for functions of scripts/release-images.sh, each lifted out of
# the script and run alone. tests/deploy runs it
# (TestReleaseTrainShellSuites); by hand:
#
#   bash scripts/release-images_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

images="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-images.sh"

failures=0
cases=0
out=""
status=0

# fn <name>: the definition of function <name> in release-images.sh.
fn() {
  sed -n "/^$1() {/,/^}/p" "$images"
}

check() { # check <case> <want-status> <want-substring>...
  local name="$1" want="$2" s
  shift 2
  cases=$((cases + 1))
  if [ "$status" != "$want" ]; then
    echo "FAIL $name: exit $status, want $want"
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  for s in "$@"; do
    if [[ "$out" != *"$s"* ]]; then
      echo "FAIL $name: output lacks \"$s\""
      printf '%s\n' "$out" | sed 's/^/    /'
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}

check_reviewed_merge_fn="$(fn check_reviewed_merge)"
if [ -z "$check_reviewed_merge_fn" ]; then
  echo "FAIL release-images.sh defines no check_reviewed_merge"
  exit 1
fi

# A skipped reviewed-merge gate names the value that skipped it, so the
# release log records what the operator actually set.
for value in 0 false False FALSE no No NO; do
  status=0
  out="$(
    eval "$check_reviewed_merge_fn"
    short=abc1234 GRAIN_CI_REF=refs/heads/main REQUIRE_REVIEWED_MERGE="$value" check_reviewed_merge 2>&1
  )" || status=$?
  check "REQUIRE_REVIEWED_MERGE=$value skips the gate and says so" 0 \
    "WARNING: REQUIRE_REVIEWED_MERGE=$value, so abc1234 is published without checking that it reached refs/heads/main through a reviewed merge"
done

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
