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

# Unit tests for scripts/release-notes.sh: its Breaking changes section,
# write (the notes file and the GitHub Release body) and check-breaking
# (the promotion gate), against a throwaway repository of pull request
# merges and direct commits. tests/deploy/release_trunk_scripts_test.go
# runs this in CI; by hand:
#
#   bash scripts/release-notes_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

notes="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-notes.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
unset RELEASE_MAJOR RELEASE_ALLOW_BREAKING

failures=0
cases=0
out=""
status=0

# run <args...>: the script's output in $out and its exit status in $status.
run() {
  status=0
  out="$(cd "$work/repo" && bash "$notes" "$@" 2>&1)" || status=$?
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

lacks() { # lacks <case> <substring>
  cases=$((cases + 1))
  if [[ "$out" == *"$2"* ]]; then
    echo "FAIL $1: output has \"$2\""
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

g() { git -C "$work/repo" "$@" >/dev/null; }

# commit <subject> [<body>]: an empty commit on the current branch.
commit() {
  if [ "$#" -gt 1 ]; then
    g commit -q --allow-empty -m "$1" -m "$2"
  else
    g commit -q --allow-empty -m "$1"
  fi
}

# pr <number> <title> <branch commit subject> [<branch commit body>]: a
# pull request of one commit, merged onto main the way GitHub merges one.
pr() {
  local n="$1" title="$2"
  shift 2
  g checkout -q -b "pr-$n"
  commit "$@"
  g checkout -q main
  g merge -q --no-ff "pr-$n" -m "Merge pull request #$n from example/pr-$n" -m "$title"
}

conf() { # conf <major>: commit scripts/release.conf with RELEASE_MAJOR=<major>.
  mkdir -p "$work/repo/scripts"
  printf '# settings\nRELEASE_MAJOR=%s\nRELEASE_FLOOR_MINOR=0\n' "$1" >"$work/repo/scripts/release.conf"
  g add scripts/release.conf
  g commit -q -m "chore: set RELEASE_MAJOR to $1"
}

mkdir -p "$work/repo"
g init -q -b main
conf 3
g tag v3.0.0
pr 1 "feat: add the widget" "feat: add the widget"
commit "fix: mend the gadget"
g tag v3.1.0
pr 2 "refactor: shuffle the config loader" "refactor: shuffle the config loader" \
  "Loads it lazily.

BREAKING CHANGE: GRAIN_CONFIG is read once,
at start, and kept until exit.

Origin: task 1"
commit "feat(api)!: drop the v1 endpoints"
commit "docs: say so"
g tag v3.2.0
pr 3 "chore: tidy" "fix!: stop accepting empty names"
g tag v3.3.0

# --- the notes ---------------------------------------------------------------

run v3.1.0 v3.0.0 v3.1.0
check "no breaking section without breaking changes" 0 "# v3.1.0" "## Features" \
  "- feat: add the widget (#1)" "- fix: mend the gadget ("
lacks "no breaking section without breaking changes: none" "## Breaking changes"

run v3.2.0 v3.1.0 v3.2.0
check "breaking changes listed first" 0 "## Breaking changes

- refactor: shuffle the config loader (#2)
  - GRAIN_CONFIG is read once, at start, and kept until exit.
- feat(api)!: drop the v1 endpoints (" "## Features

- feat(api)!: drop the v1 endpoints (" "## Other changes

- refactor: shuffle the config loader (#2)
- docs: say so ("
lacks "a commit with no marker is not breaking" "## Breaking changes

- docs"

run v3.3.0 v3.2.0 v3.3.0
check "a marker on a pull request's own commit breaks it" 0 "## Breaking changes

- chore: tidy (#3)"

run v3.1.0 v3.0.0
check "too few arguments" 2 "usage:"
run check-breaking v3.1.0 v3.0.0
check "check-breaking wants three arguments" 2 "usage:"
run v3.1.0 v3.0.0 v3.1.0 /nonexistent/highlights.md
check "a missing highlights file is refused" 2 "does not exist"

# --- check-breaking ----------------------------------------------------------

run check-breaking v3.1.0 v3.0.0 v3.1.0
check "nothing breaking promotes" 0 "no breaking changes since v3.0.0"

run check-breaking v3.2.0 v3.1.0 v3.2.0
check "breaking changes in a minor release are refused" 3 "refusing to promote v3.2.0" \
  "feat(api)!: drop the v1 endpoints" "RELEASE_MAJOR=3" "RELEASE_ALLOW_BREAKING=v3.2.0"

RELEASE_ALLOW_BREAKING=v3.2.0 run check-breaking v3.2.0 v3.1.0 v3.2.0
check "a person's override for the release promotes it" 0 "RELEASE_ALLOW_BREAKING=v3.2.0 overrides"

RELEASE_ALLOW_BREAKING=v3.1.0 run check-breaking v3.2.0 v3.1.0 v3.2.0
check "an override for another release does not" 3 "refusing to promote v3.2.0"

RELEASE_ALLOW_BREAKING=1 run check-breaking v3.2.0 v3.1.0 v3.2.0
check "an override must name the release" 3 "refusing to promote v3.2.0"

# A major bump: RELEASE_MAJOR raised on main, and the release is v4.0.0.
conf 4
g tag v4.0.0
run check-breaking v4.0.0 v3.2.0 v4.0.0
check "breaking changes in the major release RELEASE_MAJOR asks for promote" 0 \
  "a major release over v3.2.0, as RELEASE_MAJOR=4 asks"

run check-breaking v4.0.0 "$(git -C "$work/repo" rev-parse v3.2.0)" v4.0.0
check "the previous release named by commit is read from its tag" 0 "RELEASE_MAJOR=4 asks"

# latest (and an rc) on the previous release's commit, as on v3.1.0 in
# this repository: git describe would name latest, which has no major.
g tag -a latest -m "Latest release v3.2.0" v3.2.0
g tag -a v3.2.0-rc.1 -m "rc" v3.2.0
run check-breaking v4.0.0 "$(git -C "$work/repo" rev-parse v3.2.0)" v4.0.0
check "the previous release named by commit is read past latest on it" 0 "RELEASE_MAJOR=4 asks"
run check-breaking v4.0.0 latest v4.0.0
check "the previous release named as latest is read from its release tag" 0 "RELEASE_MAJOR=4 asks"
g tag -d latest
g tag -d v3.2.0-rc.1

run check-breaking v3.4.0 v3.2.0 v4.0.0
check "RELEASE_MAJOR raised does not let a minor release through" 3 "refusing to promote v3.4.0"

RELEASE_MAJOR=3 run check-breaking v4.0.0 v3.2.0 v4.0.0
check "a major release RELEASE_MAJOR does not ask for is refused" 3 "refusing to promote v4.0.0" "RELEASE_MAJOR=3"

# A bare BREAKING CHANGE trailer with no description text after the colon still
# triggers check-breaking rather than slipping past the gate.
g checkout -q -b bare-breaking v4.0.0
commit "refactor: drop legacy flag" "BREAKING CHANGE:"
g tag v4.1.0
g checkout -q main
g branch -q -D bare-breaking
run check-breaking v4.1.0 v4.0.0 v4.1.0
check "a bare BREAKING CHANGE trailer still trips check-breaking" 3 "refusing to promote v4.1.0" "- refactor: drop legacy flag"
g tag -d v4.1.0

# --- write -------------------------------------------------------------------

run write --body "$work/body.md" v3.2.0 v3.1.0 v3.2.0
check "write names the files it wrote" 0 "$work/repo/docs/releases/v3.2.0.md" "$work/body.md"
want="$(cd "$work/repo" && bash "$notes" v3.2.0 v3.1.0 v3.2.0)"
out="$(cat "$work/repo/docs/releases/v3.2.0.md")"
cases=$((cases + 1))
if [ "$out" = "$want" ]; then
  echo "ok   write's notes file is the notes"
else
  echo "FAIL write's notes file is not the notes:"
  diff <(printf '%s\n' "$want") <(printf '%s\n' "$out") | sed 's/^/    /' || true
  failures=$((failures + 1))
fi
out="$(cat "$work/body.md")"
check "the release body is the notes without their title" 0 "## Breaking changes" "## Fixes"
cases=$((cases + 1))
if [ "$out" = "$(printf '%s\n' "$want" | tail -n +3)" ]; then
  echo "ok   the release body starts at the Released line"
else
  echo "FAIL the release body is not the notes past their title:"
  printf '%s\n' "$out" | sed 's/^/    /'
  failures=$((failures + 1))
fi

printf '# v3.2.0\n\nWritten by a person.\n' >"$work/repo/docs/releases/v3.2.0.md"
run write --body "$work/body.md" v3.2.0 v3.1.0 v3.2.0
check "write keeps a notes file already there" 0 "already there; keeping it"
out="$(cat "$work/repo/docs/releases/v3.2.0.md")"
check "write keeps a notes file already there: unchanged" 0 "Written by a person."
out="$(cat "$work/body.md")"
check "the body comes from the notes file kept" 0 "Written by a person."
lacks "the body comes from the notes file kept: untitled" "# v3.2.0"

RUNNER_TEMP="$work/runner" run write --notes "$work/out/notes.md" v3.1.0 v3.0.0 v3.1.0
check "write's default body is in RUNNER_TEMP" 0 "$work/out/notes.md" "$work/runner/release-body-v3.1.0.md"
cases=$((cases + 1))
if [ -s "$work/out/notes.md" ] && [ -s "$work/runner/release-body-v3.1.0.md" ]; then
  echo "ok   write's files are written"
else
  echo "FAIL write's files are missing"
  failures=$((failures + 1))
fi

run write --bogus v3.1.0 v3.0.0 v3.1.0
check "write refuses an unknown flag" 2 "usage:"

echo
if [ "$failures" -gt 0 ]; then
  echo "${failures} of ${cases} cases failed"
  exit 1
fi
echo "all ${cases} cases passed"
