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

published_digest_fn="$(fn published_digest)"
commit_already_published_fn="$(fn commit_already_published)"
reuse_published_sandbox_fn="$(fn reuse_published_sandbox)"
verify_reused_sandbox_for_signing_fn="$(fn verify_reused_sandbox_for_signing)"
repository_of_fn="$(fn repository_of)"
for name in published_digest commit_already_published reuse_published_sandbox verify_reused_sandbox_for_signing; do
  if [ -z "$(fn "$name")" ]; then
    echo "FAIL release-images.sh defines no $name"
    exit 1
  fi
done

d_grain="sha256:1111111111111111111111111111111111111111111111111111111111111111"
d_k8s="sha256:7becfcf764df75e90a427f30173b3e6c2a8e7228b3410ec4a56b2b09dc28cff9"
d_other="sha256:9999999999999999999999999999999999999999999999999999999999999999"
c_full="46fdc1104ba8c6febd3abf9820dfb0055240421c"
c_other="46fdc11fffffffffffffffffffffffffffffffff"
fake_key="$tmp_root/fake.key"
printf 'stub key\n' > "$fake_key"

# commit_already_published accepts a grain:sha- tag whose revision label matches
# the full 40-char commit (and verifies its signature when signing_key is set),
# rejects a 7-char collision, treats missing as absent (1), and fails closed (2)
# on a transient, auth, or empty-output registry error or signature check error.
# shellcheck disable=SC2317 # stubs below are invoked by eval'd functions
run_commit_published_case() {
  local inspect_rc="$1" inspect_out="$2" key="${3:-}" sig_rc="${4:-0}" inspect_err="${5:-}"
  status=0
  out="$(
    eval "$repository_of_fn"
    eval "$commit_already_published_fn"
    registry_env() { "$@"; }
    sleep() { :; }
    signed_with_our_key() { echo "verified=$1"; return "$sig_rc"; }
    docker() {
      if [ "$1" = buildx ] && [ "$2" = version ]; then return 0; fi
      [ -z "$inspect_out" ] || printf '%s\n' "$inspect_out"
      [ -z "$inspect_err" ] || printf '%s\n' "$inspect_err" >&2
      return "$inspect_rc"
    }
    signing_key="$key" grain="docker.io/grainai/grain:sha-46fdc11" commit="$c_full" commit_already_published 2>&1
  )" || status=$?
}

run_commit_published_case 0 "$d_grain $c_full"
check "commit_already_published matches same full commit" 0

run_commit_published_case 0 "$d_grain $c_full" "" 0 "WARNING: credential helper deprecation notice"
check "commit_already_published ignores stderr warnings on exit 0" 0

run_commit_published_case 0 "$d_grain $c_other"
check "commit_already_published rejects a 7-char short-sha collision" 1

run_commit_published_case 0 "malformed-output"
check "commit_already_published fails closed (2) on malformed exit-0 output" 2 \
  "unexpected imagetools inspect output for docker.io/grainai/grain:sha-46fdc11"

run_commit_published_case 0 ""
check "commit_already_published fails closed (2) on empty exit-0 output" 2 \
  "unexpected imagetools inspect output for docker.io/grainai/grain:sha-46fdc11: no output"

run_commit_published_case 1 "" "" 0 "ERROR: docker.io/grainai/grain:sha-46fdc11: not found"
check "commit_already_published treats missing tag as absent (1)" 1

run_commit_published_case 1 "" "" 0 "ERROR: unexpected status: 502 Bad Gateway"
check "commit_already_published fails closed (2) on transient registry error" 2 \
  "could not check whether docker.io/grainai/grain:sha-46fdc11 is already published"

run_commit_published_case 1 ""
check "commit_already_published fails closed (2) on empty error output" 2 \
  "could not check whether docker.io/grainai/grain:sha-46fdc11 is already published"

run_commit_published_case 1 "" "" 0 "ERROR: UNAUTHORIZED: authentication required"
check "commit_already_published fails closed (2) on registry auth error" 2 \
  "could not check whether docker.io/grainai/grain:sha-46fdc11 is already published"

run_commit_published_case 0 "$d_grain $c_full" "$fake_key" 0
check "commit_already_published verifies grain signature when signing_key is readable" 0 \
  "verified=docker.io/grainai/grain@$d_grain"

run_commit_published_case 0 "$d_grain $c_full" "$fake_key" 1
check "commit_already_published ignores unsigned grain when signing_key is readable (1)" 1 \
  "carries no signature made with $fake_key; rebuilding sandboxes from source"

run_commit_published_case 0 "$d_grain $c_full" "$fake_key" 2
check "commit_already_published fails closed (2) when grain signature check errors" 2

# published_digest parses stdout independently of stderr warnings and fails
# closed (2) on empty error output, auth errors, and malformed stdout.
# shellcheck disable=SC2317 # stubs below are invoked by eval'd functions
run_published_digest_case() {
  local inspect_rc="$1" inspect_out="$2" inspect_err="${3:-}"
  status=0
  out="$(
    eval "$published_digest_fn"
    registry_env() { "$@"; }
    sleep() { :; }
    docker() {
      if [ "$1" = buildx ] && [ "$2" = version ]; then return 0; fi
      [ -z "$inspect_out" ] || printf '%s\n' "$inspect_out"
      [ -z "$inspect_err" ] || printf '%s\n' "$inspect_err" >&2
      return "$inspect_rc"
    }
    published_digest "docker.io/grainai/sandbox-k8s:sha-46fdc11" 2>&1
  )" || status=$?
}

run_published_digest_case 0 "$d_k8s" "WARNING: buildx deprecation notice"
check "published_digest ignores stderr warnings on exit 0" 0 "$d_k8s"

run_published_digest_case 0 "not-a-digest"
check "published_digest fails closed (2) on malformed exit-0 output" 2 \
  "unexpected imagetools inspect output for docker.io/grainai/sandbox-k8s:sha-46fdc11"

run_published_digest_case 0 ""
check "published_digest fails closed (2) on empty exit-0 output" 2 \
  "unexpected imagetools inspect output for docker.io/grainai/sandbox-k8s:sha-46fdc11: no output"

run_published_digest_case 1 ""
check "published_digest fails closed (2) on empty error output" 2 \
  "could not check whether docker.io/grainai/sandbox-k8s:sha-46fdc11 is already published"

run_published_digest_case 1 "" "ERROR: UNAUTHORIZED: authentication required"
check "published_digest fails closed (2) on registry auth error" 2 \
  "could not check whether docker.io/grainai/sandbox-k8s:sha-46fdc11 is already published"

# reuse_published_sandbox pulls by digest, tags locally, and sets $pinned; when
# given an expected digest or signing_key, it verifies both before pulling, and
# returns 2 on any mismatch, missing signature, verification error, pull failure,
# or local docker tag failure.
# shellcheck disable=SC2317 # stubs below are invoked by eval'd functions
run_reuse_sandbox_case() {
  local pull_rc="$1" expected="${2:-}" key="${3:-}" sig_rc="${4:-0}" tag_rc="${5:-0}"
  status=0
  out="$(
    pinned=""
    eval "$repository_of_fn"
    eval "$published_digest_fn"
    eval "$reuse_published_sandbox_fn"
    registry_env() { "$@"; }
    sleep() { :; }
    signed_with_our_key() { echo "verified=$1"; return "$sig_rc"; }
    docker() {
      case "$1 $2" in
        "buildx version") return 0 ;;
        "buildx imagetools") printf '%s\n' "$d_k8s"; return 0 ;;
        "pull "*)
          echo "pull-attempt=$2"
          [ "$pull_rc" -eq 0 ] || return "$pull_rc"
          echo "pulled=$2"
          return 0
          ;;
        "tag "*)
          [ "$tag_rc" -eq 0 ] || return "$tag_rc"
          echo "tagged=$2->$3"
          return 0
          ;;
      esac
    }
    grain="docker.io/grainai/grain:sha-46fdc11" signing_key="$key" \
      reuse_published_sandbox "docker.io/grainai/sandbox-k8s:sha-46fdc11" "$expected" 2>&1
    rc=$?
    echo "pinned=$pinned"
    exit "$rc"
  )" || status=$?
}

run_reuse_sandbox_case 0
check "reuse_published_sandbox pulls by digest and tags locally" 0 \
  "pulled=docker.io/grainai/sandbox-k8s@$d_k8s" \
  "tagged=docker.io/grainai/sandbox-k8s@$d_k8s->docker.io/grainai/sandbox-k8s:sha-46fdc11" \
  "pinned=docker.io/grainai/sandbox-k8s:sha-46fdc11@$d_k8s"

run_reuse_sandbox_case 1
check "reuse_published_sandbox refuses (2) when published digest cannot be pulled" 2 \
  "refusing to rebuild and overwrite the immutable sha- tag"

run_reuse_sandbox_case 0 "" "" 0 1
check "reuse_published_sandbox fails closed (2) when docker tag fails without retrying pull" 2 \
  "pulled docker.io/grainai/sandbox-k8s@$d_k8s but could not tag it locally as docker.io/grainai/sandbox-k8s:sha-46fdc11" \
  "pinned="
cases=$((cases + 1))
if [ "$(grep -c '^pull-attempt=' <<< "$out")" -ne 1 ]; then
  echo "FAIL reuse_published_sandbox retried pull on docker tag failure"
  printf '%s\n' "$out" | sed 's/^/    /'
  failures=$((failures + 1))
else
  echo "ok   reuse_published_sandbox does not retry pull on docker tag failure"
fi

run_reuse_sandbox_case 0 "$d_k8s" "$fake_key" 0
check "reuse_published_sandbox accepts matching expected digest and valid signature" 0 \
  "verified=docker.io/grainai/sandbox-k8s@$d_k8s" \
  "pulled=docker.io/grainai/sandbox-k8s@$d_k8s"

run_reuse_sandbox_case 0 "$d_other" "$fake_key" 0
check "reuse_published_sandbox refuses (2) when tag digest differs from signed grain stamp" 2 \
  "does not match $d_other stamped into the signed"

run_reuse_sandbox_case 0 "$d_k8s" "$fake_key" 1
check "reuse_published_sandbox refuses (2) when published sandbox is unsigned under signing_key" 2 \
  "carries no signature made with $fake_key; refusing to reuse an unsigned image"

run_reuse_sandbox_case 0 "$d_k8s" "$fake_key" 2
check "reuse_published_sandbox refuses (2) when sandbox signature check errors" 2

# verify_reused_sandbox_for_signing stops `push` from signing an unverified
# reused sandbox if `build` ran without signing_key.
# shellcheck disable=SC2317 # stubs below are invoked by eval'd functions
run_verify_reused_case() {
  local reused="$1" sig_rc="$2"
  status=0
  out="$(
    eval "$verify_reused_sandbox_for_signing_fn"
    signed_with_our_key() { echo "verified=$1"; return "$sig_rc"; }
    signing_key="$fake_key" \
      verify_reused_sandbox_for_signing "docker.io/grainai/sandbox-k8s:sha-46fdc11" "docker.io/grainai/sandbox-k8s@$d_k8s" "$reused" 2>&1
  )" || status=$?
}

run_verify_reused_case true 0
check "verify_reused_sandbox_for_signing allows verified reused sandbox" 0 \
  "verified=docker.io/grainai/sandbox-k8s@$d_k8s"

run_verify_reused_case true 1
check "verify_reused_sandbox_for_signing refuses unsigned reused sandbox" 1 \
  "refusing to sign an unverified reused image"

run_verify_reused_case false 1
check "verify_reused_sandbox_for_signing skips freshly built sandbox" 0

# image_built_on binds a reused k8s-sandbox-base (whose digest no grain image
# stamps) to the stamp-verified k8s-sandbox built on it.
image_built_on_fn="$(fn image_built_on)"
if [ -z "$image_built_on_fn" ]; then
  echo "FAIL release-images.sh defines no image_built_on"
  exit 1
fi
# shellcheck disable=SC2317 # stubs below are invoked by eval'd functions
run_image_built_on_case() {
  local want_base="$1" want_image="$2"
  status=0
  out="$(
    eval "$image_built_on_fn"
    docker() {
      case "${*: -1}" in
        base) printf "%s" "$want_base" ;;
        image) printf "%s" "$want_image" ;;
      esac
    }
    image_built_on base image 2>&1
  )" || status=$?
}

run_image_built_on_case "sha256:a sha256:b " "sha256:a sha256:b sha256:c "
check "image_built_on accepts an image whose layers extend the base's" 0

run_image_built_on_case "sha256:a sha256:x " "sha256:a sha256:b sha256:c "
check "image_built_on refuses a base that is not the image's base (replayed base)" 1

run_image_built_on_case "" "sha256:a sha256:b "
check "image_built_on refuses a base with no layers" 1

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
