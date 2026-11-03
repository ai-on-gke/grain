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

# §5 item 3: scripts/patch-tag-guard.sh against this repository's real
# v3.0.1, v3.0.2 and v3.1.0 tags, and scripts/release-patch.sh v3.1.1 in a
# scratch clone whose origin is a local bare repository, with a stub `gh`
# that reports release.yml green on the branch's head.
#
#   bash item3-patch-guard.sh <path-to-grain-checkout> <scratch-dir> <fix-sha> [<feat-sha>]
set -uo pipefail

src="$(cd "$1" && pwd)"
scratch="$2"
fix="$3"
feat="${4:-}"
guard="$src/scripts/patch-tag-guard.sh"
patch="$src/scripts/release-patch.sh"
rm -rf "$scratch"
mkdir -p "$scratch/bin"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null HOME="$scratch/home"
export GIT_AUTHOR_NAME=walkthrough GIT_AUTHOR_EMAIL=walkthrough@example.com
export GIT_COMMITTER_NAME=walkthrough GIT_COMMITTER_EMAIL=walkthrough@example.com
unset GITHUB_ACTIONS ALLOW_FEAT
mkdir -p "$HOME"

git init -q --bare -b main "$scratch/origin.git"
git -C "$src" push -q "$scratch/origin.git" "refs/remotes/origin/main:refs/heads/main" 'refs/tags/*:refs/tags/*'
git clone -q "$scratch/origin.git" "$scratch/clone"
cd "$scratch/clone" || exit 1

run() { # run <label> <command...>
  local label="$1" status=0
  shift
  echo
  echo "== $label: $*"
  "$@" > "$scratch/out" 2>&1 || status=$?
  sed 's/^/   | /' "$scratch/out" | head -n 30
  echo "== exit $status"
}

for t in v3.0.1 v3.0.2 v3.1.0; do
  run "3a guard on the real $t" bash "$guard" "$t"
done
run "3b guard on refs/tags/v3.0.2 as GITHUB_REF has it, under GitHub Actions" env GITHUB_ACTIONS=true bash "$guard" refs/tags/v3.0.2
run "3c guard --latest (latest is v3.1.0)" bash "$guard" --latest
run "3d guard on a branch ref and an empty ref (release.yml on main, a dispatch)" bash "$guard" refs/heads/main ""

# The stub gh: `gh run list ... --json headSha,status,conclusion` answers
# with release.yml green on whatever release/v3.1.1 is on the remote.
cat > "$scratch/bin/gh" <<EOF
#!/bin/sh
echo "gh \$*" >> "$scratch/gh.log"
sha=\$(git -C "$scratch/origin.git" rev-parse -q --verify refs/heads/release/v3.1.1)
printf '[{"headSha":"%s","status":"completed","conclusion":"success"}]\n' "\$sha"
EOF
chmod +x "$scratch/bin/gh"
export PATH="$scratch/bin:$PATH"

if [ -n "$feat" ]; then
  run "3e release-patch.sh refuses a feat: pick" bash "$patch" --no-wait v3.1.1 "$feat"
fi
run "3f release-patch.sh with a commit v3.1.0 already has" bash "$patch" --no-wait v3.1.1 "$(git rev-parse 'v3.1.0^{commit}')"
run "3g release-patch.sh on a minor version" bash "$patch" v3.2.0 "$fix"
run "3h release-patch.sh v3.1.1 $fix" env RELEASE_PATCH_POLL_SECONDS=1 bash "$patch" v3.1.1 "$fix"
echo "-- release/v3.1.1 on origin: $(git -C "$scratch/origin.git" rev-parse -q --verify refs/heads/release/v3.1.1 | cut -c1-12)"
echo "-- v3.1.1 on origin: $(git -C "$scratch/origin.git" rev-parse -q --verify 'refs/tags/v3.1.1^{commit}' | cut -c1-12)"
echo "-- latest on origin (must be unmoved, v3.1.0's 8a740f996c22): $(git -C "$scratch/origin.git" rev-parse -q --verify 'refs/tags/latest^{commit}' | cut -c1-12)"
echo "-- the v3.1.1 tag message:"
git -C "$scratch/origin.git" for-each-ref --format='%(taggername) %(taggeremail)%0a%(contents)' refs/tags/v3.1.1 | sed 's/^/   | /'
echo "-- the pick (git log v3.1.0..v3.1.1):"
git log --format='   %h %s%n%w(0,6,6)%(trailers:key=(cherry picked from commit,only=false)%n)' v3.1.0..v3.1.1 2>/dev/null | head -n 4
git log -1 --format=%B v3.1.1 | grep -F "cherry picked from commit" | sed 's/^/   /'
echo "-- range-diff of the pick against $fix (empty apart from the cherry-pick line):"
git range-diff "$fix^!" "v3.1.0..v3.1.1" | sed 's/^/   /'
echo "-- files the pick touches:"
git diff --stat v3.1.0 v3.1.1 | sed 's/^/   /'
run "3i guard on the v3.1.1 it made" bash "$guard" v3.1.1
run "3j release-patch.sh v3.1.1 again (the tag exists)" bash "$patch" v3.1.1
echo "-- gh was asked:"; sed 's/^/   /' "$scratch/gh.log"
echo "-- the scratch clone's local git config:"
git config --local --list | grep -i '^user\.' || echo "   (no user.* keys)"
