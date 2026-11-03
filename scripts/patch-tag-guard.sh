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

# Refuse to publish or promote a patch tag (vX.Y.Z, Z >= 1) that was not
# made the way docs/release-candidate-management.md section 9 says: a
# release/vX.Y.Z branch cut from vX.Y.<Z-1> with fixes cherry-picked onto
# it (`git cherry-pick -x`).
#
# v3.0.1 and v3.0.2 were hand-tagged on main's head instead, carrying
# hundreds of unsoaked commits -- `feat:` among them -- past v3.0.0: minor
# releases labelled as patches. This is what release.yml and promote.yml
# (.github/ and .grain/) run before they publish anything, so that can't
# happen again without somebody noticing.
#
#   patch-tag-guard.sh [--latest] [<ref>...]
#
# Each <ref> is a tag name or a full ref (refs/tags/vX.Y.Z, GITHUB_REF or
# GRAIN_CI_REF as the workflow has it). Empty arguments, refs that are not
# tags and tags that are not patch releases (vX.Y.0, rc, pending, latest)
# pass without a look. --latest also guards every vX.Y.Z tag naming the
# commit the git tag `latest` names, which is what
# `scripts/release-images.sh promote` publishes when it is given no version.
#
# A patch tag vX.Y.Z passes when either holds:
#
#   A. It came from its release branch: release/vX.Y.Z exists and contains
#      the tagged commit, vX.Y.<Z-1> is an ancestor of it, and none of the
#      commits in vX.Y.<Z-1>..vX.Y.Z is also on main -- cherry-picks have
#      SHAs of their own, so a branch that is just main fails here, and
#      so does any branch when main is nowhere to be seen.
#   B. It carries only fixes: every non-merge commit in vX.Y.0..vX.Y.Z has
#      a `fix:` / `fix(<scope>):` subject.
#
# Otherwise it exits 1, printing the `feat:` subjects (and how many other
# non-fix ones) the tag carries.
#
# Environment:
#   PATCH_GUARD_REMOTE  remote to read main and release/* from (origin).
#   PATCH_GUARD_FETCH   1 (default) fetches the tags and branches it needs
#                       from the remote first, unshallowing a shallow
#                       checkout; 0 uses only what is already local.
set -euo pipefail

remote="${PATCH_GUARD_REMOTE:-origin}"
fetch="${PATCH_GUARD_FETCH:-1}"
patch_pattern='^v([0-9]+)\.([0-9]+)\.([0-9]+)$'
fix_pattern='^fix(\([^)]*\))?!?: '
feat_pattern='^feat(\([^)]*\))?!?: '

check_latest=false
refs=()
for arg in "$@"; do
  case "$arg" in
    --latest) check_latest=true ;;
    -h|--help)
      sed -n '/^# Refuse/,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
      exit 0
      ;;
    -*)
      echo "patch-tag-guard.sh: unknown flag $arg" >&2
      exit 2
      ;;
    *) refs+=("$arg") ;;
  esac
done

have_remote=false
if git remote get-url "$remote" >/dev/null 2>&1; then
  have_remote=true
fi

fetched=false
fetch_history() {
  [ "$fetch" = 1 ] && [ "$have_remote" = true ] && [ "$fetched" = false ] || return 0
  fetched=true
  local args=(-q --tags --force)
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    args+=(--unshallow)
  fi
  if ! git fetch "${args[@]}" "$remote" \
    "+refs/heads/main:refs/remotes/$remote/main" \
    "+refs/heads/release/*:refs/remotes/$remote/release/*"; then
    echo "patch-tag-guard.sh: WARNING: could not fetch tags and branches from $remote; judging from what is local" >&2
  fi
}

# commit_of <ref>: the commit a ref names, or nothing.
commit_of() {
  git rev-parse -q --verify "$1^{commit}" 2>/dev/null || true
}

# branch_commit <name>: the remote's copy of a branch, else the local one.
branch_commit() {
  local c
  c="$(commit_of "refs/remotes/$remote/$1")"
  [ -n "$c" ] || c="$(commit_of "refs/heads/$1")"
  printf '%s' "$c"
}

if [ "$check_latest" = true ]; then
  fetch_history
  latest_commit="$(commit_of refs/tags/latest)"
  if [ -n "$latest_commit" ]; then
    while IFS= read -r name; do
      [ -n "$name" ] && refs+=("$name")
    done < <(git tag --points-at "$latest_commit" | grep -E "$patch_pattern" || true)
  fi
fi

failed=0
checked=" "
for ref in ${refs[@]+"${refs[@]}"}; do
  [ -n "$ref" ] || continue
  case "$ref" in
    refs/tags/*) tag="${ref#refs/tags/}" ;;
    refs/*) continue ;;
    *) tag="$ref" ;;
  esac
  if ! [[ "$tag" =~ $patch_pattern ]] || [ "${BASH_REMATCH[3]}" -eq 0 ]; then
    echo "patch-tag-guard.sh: $tag is not a patch tag (vX.Y.Z, Z >= 1); nothing to guard"
    continue
  fi
  case "$checked" in *" $tag "*) continue ;; esac
  checked="$checked$tag "
  major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}"
  base="v$major.$minor.0"
  prev="v$major.$minor.$((patch - 1))"
  branch="release/$tag"

  fetch_history
  commit="$(commit_of "refs/tags/$tag")"
  if [ -z "$commit" ]; then
    echo "patch-tag-guard.sh: $tag: no such tag here, so there is nothing to vouch for what would be published as it" >&2
    failed=1
    continue
  fi

  # A. Cut from its own release branch.
  why_not_branch=""
  branch_head="$(branch_commit "$branch")"
  prev_commit="$(commit_of "refs/tags/$prev")"
  main_commit="$(branch_commit main)"
  if [ -z "$branch_head" ]; then
    why_not_branch="there is no $branch branch"
  elif ! git merge-base --is-ancestor "$commit" "$branch_head"; then
    why_not_branch="$branch does not contain ${commit:0:12}"
  elif [ -z "$prev_commit" ]; then
    why_not_branch="there is no $prev tag to have cut $branch from"
  elif ! git merge-base --is-ancestor "$prev_commit" "$commit"; then
    why_not_branch="$prev is not an ancestor of $tag"
  elif [ -z "$main_commit" ]; then
    # Without main there is no telling cherry-picks from main's own
    # commits, so the branch vouches for nothing; B still may.
    why_not_branch="there is no main here to tell $branch's cherry-picks from main's own commits"
  else
    off_main="$(git rev-list --count "$prev_commit..$commit" ^"$main_commit")"
    total="$(git rev-list --count "$prev_commit..$commit")"
    if [ "$off_main" != "$total" ]; then
      why_not_branch="$((total - off_main)) of the $total commits in $prev..$tag are main's own, not cherry-picks"
    fi
  fi
  if [ -z "$why_not_branch" ]; then
    echo "patch-tag-guard.sh: $tag (${commit:0:12}) was cut on $branch from $prev; ok"
    continue
  fi

  # B. Only fixes since vX.Y.0.
  base_commit="$(commit_of "refs/tags/$base")"
  if [ -z "$base_commit" ]; then
    echo "patch-tag-guard.sh: $tag: $why_not_branch, and there is no $base tag to count its commits from" >&2
    failed=1
    continue
  fi
  subjects="$(git log --no-merges --format=%s "$base_commit..$commit")"
  feats="$(printf '%s\n' "$subjects" | grep -E "$feat_pattern" || true)"
  others="$(printf '%s\n' "$subjects" | grep -v -E "$fix_pattern" | grep -v -E "$feat_pattern" | grep -c . || true)"
  if [ -z "$feats" ] && [ "${others:-0}" -eq 0 ]; then
    echo "patch-tag-guard.sh: $tag (${commit:0:12}) carries only fix: commits past $base; ok"
    continue
  fi

  total="$(printf '%s\n' "$subjects" | grep -c . || true)"
  {
    echo "patch-tag-guard.sh: refusing $tag (${commit:0:12}): $why_not_branch, and $base..$tag is not fixes only ($total commits)."
    if [ -n "$feats" ]; then
      echo "patch-tag-guard.sh: $tag carries these feat: commits:"
      printf '%s\n' "$feats" | sed 's/^/  /'
    fi
    if [ "${others:-0}" -gt 0 ]; then
      echo "patch-tag-guard.sh: and $others other commits that are not fix:"
    fi
    echo "patch-tag-guard.sh: a patch release is $prev plus cherry-picked fixes on $branch: scripts/release-patch.sh $tag <sha>... (docs/release-candidate-management.md, section 9)."
  } >&2
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::error::$tag is not a patch release: $why_not_branch, and it carries non-fix commits past $base"
  fi
  failed=1
done

exit "$failed"
