#!/usr/bin/env bash
# §5 item 2: release-notes.sh's breaking-change section, check-breaking and
# write, on a scratch branch of this repository's real history (v3.1.0 to
# origin/main, plus a feat!: commit pushed directly and a merged pull
# request whose commit carries a BREAKING CHANGE: trailer).
#
#   bash item2-breaking-gate.sh <path-to-grain-checkout> <scratch-dir>
set -uo pipefail

src="$(cd "$1" && pwd)"
scratch="$2"
notes="$src/scripts/release-notes.sh"
rm -rf "$scratch"
mkdir -p "$scratch"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null HOME="$scratch/home"
export GIT_AUTHOR_NAME=walkthrough GIT_AUTHOR_EMAIL=walkthrough@example.com
export GIT_COMMITTER_NAME=walkthrough GIT_COMMITTER_EMAIL=walkthrough@example.com
unset RELEASE_MAJOR RELEASE_ALLOW_BREAKING RUNNER_TEMP
mkdir -p "$HOME"

git init -q --bare -b main "$scratch/origin.git"
git -C "$src" push -q "$scratch/origin.git" "refs/remotes/origin/main:refs/heads/main" 'refs/tags/*:refs/tags/*'
git clone -q "$scratch/origin.git" "$scratch/clone"
cd "$scratch/clone" || exit 1
git checkout -q -b scratch/breaking
git commit -q --allow-empty -m "feat(api)!: drop the v1 task API" -m "Callers move to /api/v2."
git checkout -q -b pr-branch HEAD
git commit -q --allow-empty -m "refactor(store): rename the run table" \
  -m "BREAKING CHANGE: a v3.1 state dump no longer imports without grain state migrate."
git commit -q --allow-empty -m "test(store): cover the rename"
git checkout -q scratch/breaking
git merge -q --no-ff pr-branch -m "Merge pull request #9001 from example/rename-run-table" -m "refactor(store): rename the run table"
git commit -q --allow-empty -m "fix: an ordinary fix after them"
git tag -a v3.2.0-pending -m "scratch pending" HEAD
echo "== scratch/breaking at $(git rev-parse --short=12 HEAD), $(git rev-list --count v3.1.0..HEAD) commits past v3.1.0"

run() { # run <label> <VAR=value...> -- <args...>
  local label="$1" status=0
  shift
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  echo
  echo "== $label: ${envs[*]} release-notes.sh $*"
  env "${envs[@]}" bash "$notes" "$@" > "$scratch/out" 2> "$scratch/err" || status=$?
  sed 's/^/   | /' "$scratch/out" | head -n "${HEAD_LINES:-40}"
  [ ! -s "$scratch/err" ] || sed 's/^/   ! /' "$scratch/err"
  echo "== exit $status"
}

HEAD_LINES=14 run "2a notes" -- v3.2.0 v3.1.0 HEAD
run "2b check-breaking, minor bump" -- check-breaking v3.2.0 v3.1.0 HEAD
run "2c check-breaking, RELEASE_ALLOW_BREAKING=v3.2.0" RELEASE_ALLOW_BREAKING=v3.2.0 -- check-breaking v3.2.0 v3.1.0 HEAD
run "2d check-breaking, RELEASE_ALLOW_BREAKING for another version" RELEASE_ALLOW_BREAKING=v3.3.0 -- check-breaking v3.2.0 v3.1.0 HEAD
run "2e check-breaking, major bump with RELEASE_MAJOR=3 in release.conf" -- check-breaking v4.0.0 v3.1.0 HEAD
run "2f check-breaking, major bump, RELEASE_MAJOR=4" RELEASE_MAJOR=4 -- check-breaking v4.0.0 v3.1.0 HEAD
run "2g check-breaking, minor bump, RELEASE_MAJOR=4" RELEASE_MAJOR=4 -- check-breaking v3.2.0 v3.1.0 HEAD
run "2h check-breaking, from the latest tag (latest = v3.1.0)" RELEASE_MAJOR=4 -- check-breaking v4.0.0 latest HEAD
run "2i check-breaking on origin/main's real history" -- check-breaking v3.2.0 v3.1.0 origin/main

HEAD_LINES=3 run "2j write" -- write --body "$scratch/body.md" v3.2.0 v3.1.0 HEAD
echo "-- the notes file without its title and blank line, against the body:"
tail -n +3 docs/releases/v3.2.0.md | diff - "$scratch/body.md" && echo "   identical"
echo "-- the body's first lines:"; head -n 10 "$scratch/body.md" | sed 's/^/   | /'
echo "-- the body's breaking section:"; sed -n '/^## Breaking changes/,/^## Features/p' "$scratch/body.md" | sed 's/^/   | /'
HEAD_LINES=3 run "2k write again (the file is kept)" -- write --body "$scratch/body2.md" v3.2.0 v3.1.0 HEAD
cmp "$scratch/body.md" "$scratch/body2.md" && echo "   the second body is the same"
HEAD_LINES=3 run "2l write with the default body path (TMPDIR)" TMPDIR="$scratch/tmp" -- write --notes "$scratch/notes-l.md" v3.2.0 v3.1.0 HEAD
ls "$scratch/tmp" 2>&1 | sed 's/^/   /'
