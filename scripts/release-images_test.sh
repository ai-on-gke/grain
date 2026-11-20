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

src_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
images="$src_root/scripts/release-images.sh"

tmp_root="$(mktemp -d)"
cleanup() { rm -rf "$tmp_root"; }
trap cleanup EXIT

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

fetch_promote_tag_into_fn="$(fn fetch_promote_tag_into)"
release_takes_main_grain_fn="$(fn release_takes_main_grain)"
if [ -z "$fetch_promote_tag_into_fn" ] || [ -z "$release_takes_main_grain_fn" ]; then
  echo "FAIL release-images.sh lacks fetch_promote_tag_into or release_takes_main_grain"
  exit 1
fi

# Helper: build a scratch git repository containing the files passed as
# <relpath>=<srcfile-or-content-with-@file> pairs and an annotated tag $2.
# Prints "<dir> <commit>".
make_release_tree_repo() {
  local dir="$1" tag="$2"
  shift 2
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  local spec path val
  for spec in "$@"; do
    path="${spec%%=*}"
    val="${spec#*=}"
    mkdir -p "$dir/$(dirname "$path")"
    if [[ "$val" == @* ]]; then
      cp "${val#@}" "$dir/$path"
    else
      printf '%s\n' "$val" > "$dir/$path"
    fi
  done
  git -C "$dir" add .
  git -C "$dir" \
    -c user.name=test -c user.email=test@example.com \
    commit -q -m "release $tag"
  git -C "$dir" \
    -c user.name=test -c user.email=test@example.com \
    tag -a "$tag" -m "Release $tag"
  git -C "$dir" rev-parse HEAD
}

run_release_takes_main_grain() { # <remote> <repo_root> <tag> <commit> [git_config_global]
  local r_remote="$1" r_root="$2" r_tag="$3" r_commit="$4" r_cfg="${5:-/dev/null}"
  (
    set -euo pipefail
    export GIT_CONFIG_GLOBAL="$r_cfg" GIT_CONFIG_SYSTEM=/dev/null
    eval "$fetch_promote_tag_into_fn"
    eval "$release_takes_main_grain_fn"
    # Variables read and written by the eval'd functions from release-images.sh.
    # shellcheck disable=SC2034
    remote="$r_remote" repo_root="$r_root" promote_tag="$r_tag" promote_commit="$r_commit"
    promote_takes_hub="" promote_takes_registry="" promote_predates_dockerhub=""
    rc=0
    release_takes_main_grain || rc=$?
    printf 'rc=%d hub=%s registry=%s predates_dockerhub=%s\n' \
      "$rc" "$promote_takes_hub" "$promote_takes_registry" "$promote_predates_dockerhub"
  ) 2>&1
}

# Regression test for R3: release_takes_main_grain must detect hub and
# registry on a tag whose tree holds the real (~140 KB) scripts/release-images.sh
# and Dockerfile, including from a detached tag checkout (as publish-release.yml
# runs). Piping `git show` into `grep -q` under `set -o pipefail` died with
# SIGPIPE (141) once the file exceeded a pipe buffer and silently set
# promote_takes_hub=false.
real_repo="$tmp_root/real-tree"
real_commit="$(make_release_tree_repo "$real_repo" v3.13.0 \
  "Dockerfile=@$src_root/Dockerfile" \
  "Dockerfile.hub=@$src_root/Dockerfile.hub" \
  "Dockerfile.registry=@$src_root/Dockerfile.registry" \
  "scripts/release-image-repos.sh=@$src_root/scripts/release-image-repos.sh" \
  "scripts/release-images.sh=@$src_root/scripts/release-images.sh")"
tag_checkout="$tmp_root/tag-checkout"
git clone -q "$real_repo" "$tag_checkout"
git -C "$tag_checkout" -c advice.detachedHead=false checkout -q --detach v3.13.0

for attempt in 1 2 3 4 5; do
  status=0
  out="$(run_release_takes_main_grain origin "$tag_checkout" v3.13.0 "$real_commit")" || status=$?
  check "real-sized tree from detached tag checkout detects hub and registry (attempt $attempt)" 0 \
    "rc=0 hub=true registry=true predates_dockerhub=false"
done

# Oversized (>256 KB) Dockerfile and release-images.sh with the matched lines
# near line 2 (maximizing unread trailing bytes after the first match).
padding="$tmp_root/padding.txt"
printf '# %078d\n' 0 | awk '{ for (i = 0; i < 3400; i++) print }' > "$padding"

big_dockerfile="$tmp_root/Dockerfile.big"
{
  printf 'FROM scratch\nARG MAIN_GRAIN=./cmd/grain\n'
  cat "$padding"
} > "$big_dockerfile"

big_hub_script="$tmp_root/release-images-hub.big"
{
  printf '#!/usr/bin/env bash\nbuild_hub_image() {\n  :\n}\n'
  cat "$padding"
} > "$big_hub_script"

big_hub_repo="$tmp_root/big-hub"
big_hub_commit="$(make_release_tree_repo "$big_hub_repo" v3.14.0 \
  "Dockerfile=@$big_dockerfile" \
  "Dockerfile.hub=FROM scratch" \
  "scripts/release-image-repos.sh=RELEASE_IMAGE_REGISTRY_DEFAULT=docker.io" \
  "scripts/release-images.sh=@$big_hub_script")"
status=0
out="$(run_release_takes_main_grain "$big_hub_repo" "$big_hub_repo" v3.14.0 "$big_hub_commit")" || status=$?
check "oversized (>256 KB) Dockerfile and hub script avoid SIGPIPE" 0 \
  "rc=0 hub=true registry=true predates_dockerhub=false"

# Pre-rename oversized (>256 KB) release-images.sh with build_registry_image()
# and Dockerfile.registry (no Dockerfile.hub).
big_reg_script="$tmp_root/release-images-reg.big"
{
  printf '#!/usr/bin/env bash\nbuild_registry_image() {\n  :\n}\n'
  cat "$padding"
} > "$big_reg_script"

big_reg_repo="$tmp_root/big-reg"
big_reg_commit="$(make_release_tree_repo "$big_reg_repo" v3.5.0 \
  "Dockerfile=@$big_dockerfile" \
  "Dockerfile.registry=FROM scratch" \
  "scripts/release-image-repos.sh=RELEASE_IMAGE_REGISTRY_DEFAULT=docker.io" \
  "scripts/release-images.sh=@$big_reg_script")"
status=0
out="$(run_release_takes_main_grain "$big_reg_repo" "$big_reg_repo" v3.5.0 "$big_reg_commit")" || status=$?
check "pre-rename oversized (>256 KB) registry script detects registry only" 0 \
  "rc=0 hub=false registry=true predates_dockerhub=false"

# Release predating hub/registry images.
no_hub_repo="$tmp_root/no-hub"
no_hub_commit="$(make_release_tree_repo "$no_hub_repo" v2.0.0 \
  "Dockerfile=FROM scratch" \
  "scripts/release-image-repos.sh=RELEASE_IMAGE_REGISTRY_DEFAULT=docker.io" \
  "scripts/release-images.sh=#!/usr/bin/env bash")"
status=0
out="$(run_release_takes_main_grain "$no_hub_repo" "$no_hub_repo" v2.0.0 "$no_hub_commit")" || status=$?
check "tree without Dockerfile.hub/registry reports neither" 0 \
  "rc=1 hub=false registry=false predates_dockerhub=false"

# git grep -G ignores grep.patternType=fixed and grep.patternType=extended in
# user config (under extended, `^build_hub_image()` would match a prefix like
# `build_hub_image_helper`, while under fixed `^` would be literal).
cfg_fixed="$tmp_root/gitconfig-fixed"
printf '[grep]\n\tpatternType = fixed\n' > "$cfg_fixed"
status=0
out="$(run_release_takes_main_grain "$big_hub_repo" "$big_hub_repo" v3.14.0 "$big_hub_commit" "$cfg_fixed")" || status=$?
check "grep.patternType=fixed in gitconfig does not break detection" 0 \
  "rc=0 hub=true registry=true predates_dockerhub=false"

cfg_extended="$tmp_root/gitconfig-extended"
printf '[grep]\n\tpatternType = extended\n' > "$cfg_extended"
helper_only_repo="$tmp_root/helper-only"
helper_only_commit="$(make_release_tree_repo "$helper_only_repo" v3.15.0 \
  "Dockerfile=FROM scratch" \
  "Dockerfile.hub=FROM scratch" \
  "scripts/release-image-repos.sh=RELEASE_IMAGE_REGISTRY_DEFAULT=docker.io" \
  "scripts/release-images.sh=build_hub_image_helper() { :; }")"
status=0
out="$(run_release_takes_main_grain "$helper_only_repo" "$helper_only_repo" v3.15.0 "$helper_only_commit" "$cfg_extended")" || status=$?
check "grep.patternType=extended does not falsely match build_hub_image_helper" 0 \
  "rc=1 hub=false registry=false predates_dockerhub=false"

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
