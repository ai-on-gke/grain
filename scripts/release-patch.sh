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

# Make a patch release vX.Y.Z (Z >= 1) the way
# docs/release-candidate-management.md section 9 says, instead of by
# tagging main's head (which is how v3.0.1 and v3.0.2 shipped ~25 unsoaked
# feat: commits each as "patches").
#
#   release-patch.sh [--no-wait] <vX.Y.Z> <sha>...
#       Create release/vX.Y.Z from vX.Y.<Z-1>, `git cherry-pick -x` each
#       <sha> onto it in the order given, and push the branch. release.yml
#       answers the push (it builds release/v*) and publishes the head's
#       sha-<short> images. Then wait for that run to go green and tag, as
#       below; --no-wait stops after the push.
#
#   release-patch.sh <vX.Y.Z>
#       Resume: release/vX.Y.Z is already pushed. Wait until release.yml is
#       green on its head, run scripts/patch-tag-guard.sh, and push the
#       annotated tag vX.Y.Z on that head. It never moves `latest` and never
#       promotes: both stay a person's act (section 9, step 6).
#
# It refuses a feat: pick (a patch carries fixes; set ALLOW_FEAT=1 to say
# otherwise on purpose), an existing vX.Y.Z tag, and an existing
# release/vX.Y.Z branch when given commits to pick.
#
# Whether release.yml is green is asked of GitHub with `gh run list`, so
# this needs a `gh` logged in to the repository. It never sets a git
# identity: commits are authored by whoever runs it.
#
# Environment:
#   RELEASE_PATCH_REMOTE        remote to push to (origin).
#   RELEASE_PATCH_REPO          owner/name for gh (default: gh's own idea).
#   RELEASE_PATCH_WORKFLOW      workflow file gh is asked about (release.yml).
#   RELEASE_PATCH_WAIT_SECONDS  how long to wait for it (10800, 3h).
#   RELEASE_PATCH_POLL_SECONDS  how often to ask (60).
#   ALLOW_FEAT                  1 lets a feat: commit be picked.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
remote="${RELEASE_PATCH_REMOTE:-origin}"
workflow="${RELEASE_PATCH_WORKFLOW:-release.yml}"
wait_seconds="${RELEASE_PATCH_WAIT_SECONDS:-10800}"
poll_seconds="${RELEASE_PATCH_POLL_SECONDS:-60}"
patch_pattern='^v([0-9]+)\.([0-9]+)\.([0-9]+)$'
feat_pattern='^feat(\([^)]*\))?!?: '

die() {
  echo "release-patch.sh: $*" >&2
  exit 1
}

usage() {
  echo "usage: release-patch.sh [--no-wait] <vX.Y.Z> [<sha>...]" >&2
  exit 2
}

no_wait=false
if [ "${1:-}" = "--no-wait" ]; then
  no_wait=true
  shift
fi
[ $# -ge 1 ] || usage
version="$1"
shift
picks=("$@")

if ! [[ "$version" =~ $patch_pattern ]] || [ "${BASH_REMATCH[3]}" -eq 0 ]; then
  die "$version is not a patch release (vX.Y.Z with Z >= 1); minor releases come from the train"
fi
prev="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] - 1))"
branch="release/$version"

git fetch -q --tags "$remote" "+refs/heads/main:refs/remotes/$remote/main"
if git ls-remote --exit-code --tags "$remote" "refs/tags/$version" >/dev/null 2>&1; then
  die "$remote already has the tag $version"
fi
git rev-parse -q --verify "refs/tags/$prev^{commit}" >/dev/null \
  || die "$remote has no tag $prev to cut $branch from"
remote_branch="$(git ls-remote --heads "$remote" "refs/heads/$branch" | cut -f1)"

if [ ${#picks[@]} -gt 0 ]; then
  [ -z "$remote_branch" ] || die "$remote already has $branch (at ${remote_branch:0:12}); run \`release-patch.sh $version\` to tag it, or delete it to start again"
  [ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree has uncommitted changes"

  for sha in "${picks[@]}"; do
    git rev-parse -q --verify "$sha^{commit}" >/dev/null || die "$sha is not a commit here"
    subject="$(git log -1 --format=%s "$sha")"
    if [[ "$subject" =~ $feat_pattern ]] && [ "${ALLOW_FEAT:-}" != 1 ]; then
      die "$sha is a feature, not a fix: \"$subject\" (a patch carries fixes; ALLOW_FEAT=1 to pick it anyway)"
    fi
    if git merge-base --is-ancestor "$sha" "$prev"; then
      die "$prev already carries $sha (\"$subject\")"
    fi
  done

  git checkout -q -b "$branch" "$prev"
  for sha in "${picks[@]}"; do
    if ! git cherry-pick -x "$sha"; then
      die "cherry-pick of $sha conflicted on $branch; resolve it (git cherry-pick --continue), push $branch yourself, then run \`release-patch.sh $version\`"
    fi
  done
  git push -q "$remote" "refs/heads/$branch:refs/heads/$branch"
  remote_branch="$(git rev-parse HEAD)"
  echo "release-patch.sh: pushed $branch (${remote_branch:0:12}): $prev plus ${#picks[@]} cherry-picks"
  if [ "$no_wait" = true ]; then
    echo "release-patch.sh: once $workflow is green on it, run \`release-patch.sh $version\` to tag it"
    exit 0
  fi
else
  [ -n "$remote_branch" ] || die "$remote has no $branch; give the commits to cherry-pick onto $prev"
  git fetch -q "$remote" "+refs/heads/$branch:refs/remotes/$remote/$branch"
fi
head="$remote_branch"

# The newest $workflow run on $branch's head: "<status> <conclusion>", or
# nothing when there is none yet.
release_run() {
  local repo_args=()
  [ -z "${RELEASE_PATCH_REPO:-}" ] || repo_args=(--repo "$RELEASE_PATCH_REPO")
  gh run list ${repo_args[@]+"${repo_args[@]}"} --workflow "$workflow" --branch "$branch" \
    --json headSha,status,conclusion --limit 20 \
    | jq -r --arg sha "$head" '[.[] | select(.headSha == $sha)][0] // empty | "\(.status) \(.conclusion)"'
}

command -v gh >/dev/null 2>&1 || die "gh is not on PATH: it is how this asks whether $workflow is green on $branch"
deadline=$(($(date +%s) + wait_seconds))
while :; do
  run="$(release_run)"
  case "$run" in
    "completed success") break ;;
    completed\ *)
      die "$workflow on $branch (${head:0:12}) finished ${run#completed }; fix it on $branch and run \`release-patch.sh $version\` again"
      ;;
  esac
  if [ "$(date +%s)" -ge "$deadline" ]; then
    die "$workflow on $branch (${head:0:12}) is ${run:-not started} after ${wait_seconds}s; run \`release-patch.sh $version\` again once it is green"
  fi
  echo "release-patch.sh: waiting for $workflow on $branch (${head:0:12}): ${run:-not started}"
  sleep "$poll_seconds"
done
echo "release-patch.sh: $workflow is green on $branch (${head:0:12})"

git tag -a "$version" -m "Release $version: $prev plus cherry-picked fixes from $branch" "$head"
if ! PATCH_GUARD_REMOTE="$remote" bash "$here/patch-tag-guard.sh" "$version"; then
  git tag -d "$version" >/dev/null
  die "refusing to push $version"
fi
git push -q "$remote" "refs/tags/$version"
echo "release-patch.sh: pushed $version at ${head:0:12}"
echo "release-patch.sh: next (a person): move \`latest\` only if no newer release exists, dispatch promote.yml with version=$version, and land docs/releases/$version.md on main (docs/release-candidate-management.md, section 9)"
