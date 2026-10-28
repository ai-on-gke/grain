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

# Build and publish Grain's three release images from a Grain CI release job.
# The registry login is an operator-owned CI capability, not repository YAML:
# `grain repo ci-capabilities -set package-publish bwsalmon/grain`, which
# publishes as this deployment's own GitHub credential for the repository --
# the one the git proxy's ladder resolves, re-minted from an App's key where
# that is what backs it, and never a registry token of its own. A CI job that
# was granted none stops here, before the build, rather than an hour later at
# the push: see the login block below.
#
# Two phases, because a release has to be able to look at what it built
# before it publishes it:
#
#   build   build the guest, the Kubernetes sandbox image and the grain
#           image, check the grain image's own record of which commit it
#           came from, and push nothing that anybody resolves. The two
#           sandbox images are the exception that proves it: their
#           immutable sha- tags are pushed (not signed) here, because the
#           grain image names them by the digest a registry assigns on
#           push (`guest:sha-<short>@sha256:...`), and nothing resolves
#           those tags until a grain image naming them is published.
#           What it built is recorded in $RELEASE_IMAGES_STATE (default
#           /tmp/release-images.env) for the steps that follow.
#   push    sign the two sandbox images by the digests `build` recorded,
#           publish the grain image's immutable sha- tag and sign it,
#           then move all three branch tags forward onto this commit
#           where the commit they hold is an ancestor of it
#           (tag_may_move, below). Never `latest`: see
#           `promote` below.
#   all     both, in one process. The default, so a hand-run or a
#           bootstrap `bash scripts/release-images.sh` behaves as it
#           always did.
#
# And one phase that is not part of a release at all:
#
#   guest-cache
#           the guest half of `build` and nothing else: pull
#           `guest:inputs-<key>` if it exists, build and publish it if it
#           does not, and touch no other name. .grain/workflows/image.yml
#           runs this on `workflow_dispatch` so that a branch which
#           changes a guest input can warm the cache before it merges
#           rather than making every job that resolves a guest build one
#           inline until it does. It publishes only content-addressed
#           tags -- nothing that a deployment resolves moves -- which is
#           what makes it safe to run from a branch at all; see the job's
#           own comment for the trust that is still being placed in one.
#
# And one that is not part of a merge's release:
#
#   promote [vX.Y.Z]
#           point `latest` -- on guest, k8s-sandbox and grain alike -- at
#           the last promoted release. `latest` means exactly that: the
#           commit the git tag `latest` names, which
#           scripts/release-from-trunk.sh moves when it promotes a soaked
#           release candidate to `vM.X.0`. It is never main's head; that
#           is `main`, and the current candidate is `candidate`. With no argument the
#           release is whatever the git `latest` tag names, and the
#           `vX.Y.Z` tag at that same commit says what else to call it.
#           With one, that release is published as itself, and `latest`
#           moves onto it only if the git `latest` tag agrees -- so the
#           image tag can never say something the git tag does not.
#           The guest and k8s-sandbox images are a registry retag of the
#           `sha-<short>` images the release job already published and
#           tested for that commit. The grain image is rebuilt, once per
#           release and only to stamp the release name into it: from the
#           same commit, naming the same pinned sandbox images, with
#           RELEASE=vX.Y.Z -- the candidate's build could only carry the
#           rc tag, which is what the UI would otherwise show. A replayed
#           promotion finds `grain:vX.Y.Z` already labelled with its
#           release and rebuilds nothing. If any of the three `sha-`
#           images is missing it fails, naming them, and moves nothing.
#           .grain/workflows/promote.yml runs it; this is the only thing
#           that moves `latest`.
#
#   rc [vX.Y.0-rc.N]
#           publish a release candidate the way `promote` publishes a
#           release (docs/design/release-from-trunk.md, RFT-4): the guest
#           and k8s-sandbox `sha-<short>` images retagged, grain rebuilt
#           from the same commit with RELEASE=vX.Y.0-rc.N, and all three
#           published as `:vX.Y.0-rc.N`, `:candidate` and `:rc` -- the two
#           moving names the candidate (and its `rc` alias) upgrade channel
#           resolves.
#           With no argument the rc is the one the remote's
#           refs/heads/candidate is on, which scripts/release-from-trunk.sh
#           force-moves onto each cut. `:candidate` and `:rc` move only
#           when the rc is the newest rc tag the remote has, so a
#           replayed older rc publishes itself and never moves the
#           channel backwards.
#
#   pending <vX.Y.0>
#           stage a release whose soak passed, for a person to approve
#           (docs/design/release-from-trunk.md, §4): exactly `promote`'s
#           publishing of the commit the git tag vX.Y.0-pending names --
#           guest and k8s-sandbox retagged, grain rebuilt with
#           RELEASE=vX.Y.0 -- but published under `:vX.Y.0-pending`
#           alone. Neither `:vX.Y.0` nor `latest` moves, and no upgrade
#           channel resolves `-pending`. `promote vX.Y.0` then finds the
#           stamped `:vX.Y.0-pending` build and only retags it, so
#           approving builds and tests nothing.
#
#   discard-pending <vX.Y.0>
#           mark a pending release a person rejected: `:vX.Y.0-rejected`
#           beside each image's `:vX.Y.0-pending`. The `-pending` tags
#           themselves are left for cmd/ghcr-prune -- GHCR deletes a
#           version, not a tag, and the guest and k8s-sandbox ones share
#           theirs with the commit's sha- tags -- which is safe because
#           a later `pending` or `promote` reuses a stamped build only
#           when its revision label is the commit it is publishing.
#
#   discard-rc <vX.Y.0-rc.N>
#           mark a release candidate that failed its staging soak:
#           `:vX.Y.0-rc.N-discarded` beside each image's `:vX.Y.0-rc.N`,
#           so a deployment on the candidate channel can tell that the rc
#           it runs was dropped. `:candidate` and `:rc` stay where they
#           are -- the next rc moves them forward; nothing moves them
#           back.
#
# And one that publishes nothing:
#
#   may-move <image:branch-tag> <commit> <branch>
#           answer whether `push` would move that branch tag onto
#           <commit> (tag_may_move, below): exit 0 to move, 1 to stay,
#           with the reason on stdout. tests/deploy drives it.
#
# The phases exist so that .grain/workflows/release.yml can put a test
# between them -- it runs tests/container against the image the build
# phase just produced, and the push phase never happens if that suite
# fails. The gate is CI's rather than this script's: `all` builds and
# publishes with nothing in between, which is what an operator running
# this by hand is asking for.
set -euo pipefail
trap 'echo "release-images.sh: aborting: line $LINENO: $BASH_COMMAND" >&2' ERR

# tag_may_move <image:branch-tag> <commit> <branch>: whether a release may
# move a branch's moving tag onto the commit it released -- forward only
# (grain/task-4432). It returns 0 when the tag may move onto <commit> and
# 1 when it must stay where it is, and says why on stdout in one line
# either way. It changes nothing: publish_images asks it once, of the
# grain image's branch tag, and moves every image's branch tag on its
# answer. `release-images.sh may-move` asks it and nothing else, which is
# how tests/deploy drives it.
#
# The rule it replaced was "move only when <commit> is still the branch
# tip". That kept overlapping releases from moving a tag backwards, and
# it also meant that during a run of merges no release ever moved it: by
# the time a twenty-minute release finished, the branch had moved on, so
# `grain:main` sat on f09c277 through four merges on 2026-10-16. What the
# tip rule protected is kept: the tag moves only when the commit it holds
# now is an ancestor of <commit>, so two releases finishing in either
# order leave it on the newer one.
#
# What the tag holds is read from the image itself -- its
# org.opencontainers.image.revision label, which every build stamps
# (Dockerfile, GIT_REVISION) -- by `docker buildx imagetools inspect`,
# a manifest and a config blob read out of the registry rather than a
# pull. .github/workflows/build-artifacts.yml's `may_move` reads the
# same label, and the two publishers answer the same way.
#
# It fails closed. Anything it cannot establish -- no buildx to read the
# label with, a registry that will not answer, an image with no label,
# a commit git cannot find, history that has diverged -- leaves the tag
# where it is and says so. A tag left behind is moved forward by the next
# release that can tell; a tag moved backwards is a deployment pulling an
# older build than the one it asked for. The one exception is a tag that
# does not exist at all, which has nothing to go backwards from: it is
# created when <commit> is the branch tip, which is the rule every branch
# tag was created under before this.
#
# The body is a subshell, so the `exit` in move and stay ends it alone,
# and it runs without -e: every failure in it is a reason to stay, said.
tag_may_move() (
  set +e
  target="$1"
  commit="$2"
  ref="$3"
  short="${commit:0:7}"
  remote="${RELEASE_IMAGES_REMOTE:-origin}"

  move() {
    echo "release-images.sh may-move: $* -- moving $target to $short"
    exit 0
  }
  stay() {
    echo "release-images.sh may-move: $* -- leaving $target where it is"
    exit 1
  }

  # The tip rule, for the two cases where nothing names what the tag
  # holds and it is still safe to answer: the tag does not exist, or this
  # machine has no way to read its label without pulling it.
  tip_rule() {
    local tip
    tip="$(git ls-remote "$remote" "refs/heads/$ref" 2>/dev/null | awk 'NR == 1 {print $1}')"
    if [ -n "$tip" ] && [ "$tip" = "$commit" ]; then
      move "$1, and $short is still $ref's tip"
    fi
    stay "$1, and $ref is at ${tip:-a commit $remote would not name}, not $short"
  }

  if ! docker buildx version >/dev/null 2>&1; then
    tip_rule "no docker buildx here to read which commit $target holds"
  fi

  if ! answer="$(docker buildx imagetools inspect "$target" \
      --format '{{index .Image.Config.Labels "org.opencontainers.image.revision"}}' 2>&1)"; then
    case "$answer" in
      *"not found"* | *"manifest unknown"* | *MANIFEST_UNKNOWN* | *NAME_UNKNOWN*)
        tip_rule "$target does not exist yet"
        ;;
    esac
    stay "could not read $target's revision label: $(printf '%s' "$answer" | tail -n 1)"
  fi
  held="$(printf '%s' "$answer" | tr -d '[:space:]')"
  if ! [[ "$held" =~ ^[0-9a-f]{40}$ ]]; then
    stay "$target names no commit in its org.opencontainers.image.revision label (${held:-empty}), so whether $short is ahead of it cannot be told"
  fi
  if [ "$held" = "$commit" ]; then
    move "$target already holds $short; republishing it"
  fi

  # Both commits, and the branch's tip as $remote has it now, with the
  # history between them, in this checkout: a shallow one is deepened from
  # the branch. A merge-base asked of a shallow history can call an
  # ancestor unrelated, which this would read as "do not move" -- safe, but
  # wrong for no reason.
  have() { git cat-file -e "$1^{commit}" 2>/dev/null; }
  unshallow=()
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    unshallow=(--unshallow)
  fi
  tip_ref="refs/release-images-may-move/$ref"
  if ! git fetch -q "${unshallow[@]}" "$remote" "+refs/heads/$ref:$tip_ref" 2>/dev/null; then
    stay "could not fetch $ref from $remote, so whether $short is on it cannot be told"
  fi
  tip="$(git rev-parse -q --verify "$tip_ref^{commit}" 2>/dev/null || true)"
  git update-ref -d "$tip_ref" 2>/dev/null || true
  if [ -z "$tip" ]; then
    stay "$remote names no $ref, so whether $short is on it cannot be told"
  fi
  if ! have "$held"; then
    tip_rule "$target holds ${held:0:7}, which neither this checkout nor $remote's $ref has"
  fi
  if ! have "$commit"; then
    stay "$target holds ${held:0:7}, which neither this checkout nor $remote's $ref has, so whether $short is ahead of it cannot be told"
  fi

  # The branch's tag names a build of the branch: a commit that is not on
  # it -- a release run started by hand for some other commit, or one a
  # force-push has since dropped -- never moves it, however it relates to
  # what the tag holds. Before forward-only this was the tip rule's job.
  if ! git merge-base --is-ancestor "$commit" "$tip" 2>/dev/null; then
    stay "$short is not on $ref (at ${tip:0:7})"
  fi

  if git merge-base --is-ancestor "$held" "$commit" 2>/dev/null; then
    move "$target holds ${held:0:7}, an ancestor of $short"
  fi
  if git merge-base --is-ancestor "$commit" "$held" 2>/dev/null; then
    stay "$target holds ${held:0:7}, which is newer than $short -- a later release finished first"
  fi
  stay "$target holds ${held:0:7}, which is neither behind nor ahead of $short (rewritten history?) -- move it by hand if $short is what $ref should run"
)

dry_run=false
phase="all"
promote_request=""
rc_request=""
pending_request=""
# stamp_tag is the release name stamped into a rebuilt daemon image when
# it is not the name it is published under: `pending` publishes
# vX.Y.0-pending stamped vX.Y.0. Empty means promote_tag.
stamp_tag=""

while [ $# -gt 0 ]; do
  case "$1" in
    -n|--dry-run)
      dry_run=true
      shift
      ;;
    build | push | all | guest-cache)
      phase="$1"
      shift
      ;;
    promote)
      phase="$1"
      shift
      if [ $# -gt 0 ] && [[ "$1" != -* ]]; then
        promote_request="$1"
        shift
      fi
      ;;
    rc)
      phase="$1"
      shift
      if [ $# -gt 0 ] && [[ "$1" != -* ]]; then
        rc_request="$1"
        shift
      fi
      ;;
    discard-rc)
      phase="$1"
      shift
      if [ $# -eq 0 ] || [[ "$1" == -* ]]; then
        echo "release-images.sh: usage: release-images.sh discard-rc <vX.Y.0-rc.N>" >&2
        exit 2
      fi
      rc_request="$1"
      shift
      ;;
    pending | discard-pending)
      phase="$1"
      shift
      if [ $# -eq 0 ] || [[ "$1" == -* ]]; then
        echo "release-images.sh: usage: release-images.sh $phase <vX.Y.0>" >&2
        exit 2
      fi
      pending_request="$1"
      shift
      ;;
    may-move)
      shift
      if [ $# -ne 3 ]; then
        echo "release-images.sh: usage: release-images.sh may-move <image:branch-tag> <commit> <branch>" >&2
        exit 2
      fi
      status=0
      tag_may_move "$@" || status=$?
      exit "$status"
      ;;
    *)
      echo "release-images.sh: usage: release-images.sh [--dry-run|-n] [build|push|all|guest-cache|promote [vX.Y.Z]|rc [vX.Y.0-rc.N]|pending <vX.Y.0>|discard-pending <vX.Y.0>|discard-rc <vX.Y.0-rc.N>|may-move <image:branch-tag> <commit> <branch>]" >&2
      exit 1
      ;;
  esac
done

setting() {
  local variable="$1"
  local file="$2"
  local value="${!variable:-}"
  if [ -z "$value" ] && [ -r "$file" ]; then
    value="$(< "$file")"
  fi
  printf '%s' "$value"
}

registry="$(setting PACKAGE_PUBLISH_REGISTRY /home/debian/.package-publish-registry)"
username="$(setting PACKAGE_PUBLISH_USERNAME /home/debian/.package-publish-username)"
token="$(setting PACKAGE_PUBLISH_TOKEN /home/debian/.package-publish-token)"

# `registry-push` is the older shape of the same grant: a registry token an
# operator pasted, rather than this deployment's own credential for the
# repository. It is read only where `package-publish` placed nothing, so a
# deployment still granting it goes on releasing while it is switched over.
# Its registry replaces the one named above only if it names one: an
# operator who set PACKAGE_PUBLISH_REGISTRY and relies on an existing
# `docker login` (no token) publishes there, not to ghcr.io.
if [ -z "$token" ]; then
  legacy_registry="$(setting REGISTRY_PUSH_REGISTRY /home/debian/.registry-push-registry)"
  registry="${legacy_registry:-$registry}"
  username="$(setting REGISTRY_PUSH_USERNAME /home/debian/.registry-push-username)"
  token="$(setting REGISTRY_PUSH_TOKEN /home/debian/.registry-push-token)"
fi

# A direct bootstrap normally has only `gh auth login`; Grain CI supplies
# the three files above. Prefer either explicit source, then let gh provide
# the same username/token. An existing `docker login` needs neither.
registry="${registry:-ghcr.io}"
if [ -z "$username" ] && [ -z "$token" ] &&
    command -v gh >/dev/null 2>&1 && gh auth status -h github.com >/dev/null 2>&1; then
  username="$(gh api user --jq .login)"
  token="$(gh auth token -h github.com)"
  echo "release-images.sh: using the existing gh auth login for $username"
fi
if { [ -n "$username" ] && [ -z "$token" ]; } ||
   { [ -z "$username" ] && [ -n "$token" ]; }; then
  echo "release-images.sh: registry username and token must be supplied together" >&2
  exit 1
fi

GRAIN_CI_REPO="${GRAIN_CI_REPO:-}"
if [ -z "$GRAIN_CI_REPO" ] && command -v gh >/dev/null 2>&1; then
  GRAIN_CI_REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || true)"
fi
GRAIN_CI_COMMIT="${GRAIN_CI_COMMIT:-$(git rev-parse HEAD)}"
GRAIN_CI_REF="${GRAIN_CI_REF:-$(git branch --show-current)}"
if [ -z "$GRAIN_CI_REPO" ] || [ -z "$GRAIN_CI_REF" ]; then
  echo "release-images.sh: run from a GitHub checkout recognized by gh, or set GRAIN_CI_REPO and GRAIN_CI_REF" >&2
  exit 1
fi
export GRAIN_CI_REPO GRAIN_CI_COMMIT GRAIN_CI_REF

RELEASE_IMAGE_REPO="${RELEASE_IMAGE_REPO:-bwsalmon/grain}"
repo="${RELEASE_IMAGE_REPO,,}"
commit="$GRAIN_CI_COMMIT"
# The branch's moving tag: '/' becomes '..', exactly as pkg/upgrade's
# TagForBranch resolves it (see there for why '..' and not '-', #2386).
branch="${GRAIN_CI_REF//\//..}"
short="${commit:0:7}"
root="$registry/${RELEASE_IMAGE_REPO,,}"
guest_base="$root/guest-base:sha-$short"
guest="$root/guest:sha-$short"
# The granule image the guest builds below are packed onto, built out of
# third_party/granule by scripts/granule/build-granule-image.sh -- which
# names the same default, and is where the whole of why lives.
granule_image="granule:vendored"
k8s_sandbox_base="$root/k8s-sandbox-base:sha-$short"
k8s_sandbox="$root/k8s-sandbox:sha-$short"
# The daemon image, built from ./cmd/grain alongside the guest and the
# Kubernetes sandbox image.
grain="$root/grain:sha-$short"
core_main=./cmd/grain

# Where the build phase records what it built. Steps of a Grain CI job
# share a sandbox but not a shell, so a variable does not reach the step
# after this one: the file is how the build tells the suite which image to
# drive, and tells the push which images to publish.
state="${RELEASE_IMAGES_STATE:-/tmp/release-images.env}"

case "${BASH_SOURCE[0]}" in
  */*) scripts_dir="${BASH_SOURCE[0]%/*}" ;;
  *) scripts_dir=. ;;
esac
repo_root="$(cd "$scripts_dir/.." && pwd)"
# shellcheck source=scripts/grain-ci-host.sh
. "$scripts_dir/grain-ci-host.sh"
trap cleanup_ca EXIT INT TERM

ensure_direct_registry_route() {
  tune_host || return 0
  local reg="${1:-$registry}"
  [ -n "$reg" ] || return 0
  # A loopback or port-qualified registry is one somebody runs themselves:
  # public DNS has no answer for it worth pinning in /etc/hosts.
  case "$reg" in
    *:* | localhost | 127.*) return 0 ;;
  esac
  if [ -f /etc/hosts ] && grep -qE "(^|[[:space:]])${reg}([[:space:]]|$)" /etc/hosts 2>/dev/null; then
    return 0
  fi
  local ip=""
  if command -v curl >/dev/null 2>&1; then
    ip="$(curl -s --connect-timeout 3 "https://dns.google/resolve?name=${reg}&type=A" 2>/dev/null | grep -oE '"data":"[0-9.]+"' | cut -d'"' -f4 | head -n1 || true)"
    if [ -z "$ip" ]; then
      ip="$(curl -s --connect-timeout 3 -H "accept: application/dns-json" "https://1.1.1.1/dns-query?name=${reg}&type=A" 2>/dev/null | grep -oE '"data":"[0-9.]+"' | cut -d'"' -f4 | head -n1 || true)"
    fi
  fi
  if [ -n "$ip" ]; then
    if [ "$(id -u)" = 0 ]; then
      printf '%s %s\n' "$ip" "$reg" >> /etc/hosts 2>/dev/null || true
    elif command -v sudo >/dev/null 2>&1; then
      printf '%s %s\n' "$ip" "$reg" | sudo -n tee -a /etc/hosts >/dev/null 2>&1 || true
    fi
  fi
}

# The remote the promotion reads its tags from. Overridable for the
# tests, which hand it a repository of their own.
remote="${RELEASE_IMAGES_REMOTE:-origin}"
release_tag_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'
rc_tag_pattern='^v[0-9]+\.[0-9]+\.0-rc\.[0-9]+$'

# The version this build carries -- stamped into both daemon binaries
# (pkg/version.Release, which `grain version --json` and the UI report)
# and labelled onto both images (org.opencontainers.image.version) --
# worked out by scripts/build-version.sh from the tags and the branch the
# release event named: v2.3.0-dev.<n>+<sha7> on main, v2.2.0-rc.1.<n>+<sha7>
# on candidate (docs/release-candidate-management.md, "Build versions").
# Its --fetch brings the tags down first, since a CI checkout is not
# promised to have them. A RELEASE the caller gave wins, as it always
# has. Only the phases that build ask: `promote` stamps its own
# RELEASE=vX.Y.Z, and `push` publishes what `build` stamped.
build_version=""
case "$phase" in
  build | all)
    if [ -n "${RELEASE:-}" ]; then
      build_version="$RELEASE"
    elif command -v git >/dev/null 2>&1; then
      version_args=(--branch "$GRAIN_CI_REF" --commit "$commit")
      [ "$dry_run" = true ] || version_args+=(--fetch "$remote")
      build_version="$(bash "$scripts_dir/build-version.sh" "${version_args[@]}" || true)"
    fi
    if [ -n "$build_version" ]; then
      RELEASE="$build_version"
      export RELEASE
    else
      echo "release-images.sh: WARNING: no version for $commit on $GRAIN_CI_REF (scripts/build-version.sh found no tags to count from); both daemon images report themselves unversioned" >&2
    fi
    ;;
esac

# The commit tag $1 names in $remote_tags (`git ls-remote --tags` output),
# peeled through an annotated tag -- which every tag the cut makes is --
# so that it is a commit and not the tag object.
peeled_tag() {
  local name="$1" sha ref peeled="" direct=""
  while read -r sha ref; do
    case "$ref" in
      "refs/tags/$name^{}") peeled="$sha" ;;
      "refs/tags/$name") direct="$sha" ;;
    esac
  done <<< "$remote_tags"
  printf '%s' "${peeled:-$direct}"
}

# Decide what `promote` retags, from the remote's tags alone: sets
# promote_tag (the vX.Y.Z name), promote_commit, latest_commit (what the
# git `latest` tag names) and promote_latest (whether `latest` moves).
# $1 is the release asked for by name, or empty for "whatever `latest`
# names".
select_promotion() {
  local requested="$1" sha ref name
  if ! remote_tags="$(git ls-remote --tags "$remote")"; then
    echo "release-images.sh: could not list $remote's tags" >&2
    return 1
  fi
  latest_commit="$(peeled_tag latest)"
  promote_tag=""
  if [ -n "$requested" ]; then
    if ! [[ "$requested" =~ $release_tag_pattern ]]; then
      echo "release-images.sh: $requested is not a release tag (vX.Y.Z): only a promoted release is retagged" >&2
      return 1
    fi
    promote_tag="$requested"
    promote_commit="$(peeled_tag "$requested")"
    if [ -z "$promote_commit" ]; then
      echo "release-images.sh: $remote has no tag $requested" >&2
      return 1
    fi
  else
    if [ -z "$latest_commit" ]; then
      echo "release-images.sh: $remote has no \`latest\` tag, so no release has been promoted to retag" >&2
      return 1
    fi
    promote_commit="$latest_commit"
    # Every vX.Y.Z at that commit, the highest of them winning: a
    # re-promotion of an unchanged candidate can leave two.
    promote_tag="$(
      while read -r sha ref; do
        name="${ref#refs/tags/}"
        name="${name%"^{}"}"
        if [[ "$name" =~ $release_tag_pattern ]] && [ "$(peeled_tag "$name")" = "$promote_commit" ]; then
          printf '%s\n' "$name"
        fi
      done <<< "$remote_tags" | sort -u -V | tail -n 1
    )"
    if [ -z "$promote_tag" ]; then
      echo "release-images.sh: \`latest\` names $promote_commit, which no vX.Y.Z tag names: the cut tags a release and \`latest\` together, so this is a tag somebody moved by hand -- tag the release, or move \`latest\` back" >&2
      return 1
    fi
  fi
  promote_latest=false
  moving=()
  if [ "$promote_commit" = "$latest_commit" ]; then
    promote_latest=true
    moving=(latest)
  fi
}

# Decide what `pending` publishes: sets promote_tag (vX.Y.0-pending, the
# name published), stamp_tag (vX.Y.0, the name stamped) and
# promote_commit (what the git tag vX.Y.0-pending names). Nothing moves
# with it. $1 is the release, vX.Y.0.
select_pending() {
  local requested="$1"
  if ! [[ "$requested" =~ $release_tag_pattern ]]; then
    echo "release-images.sh: $requested is not a release tag (vX.Y.Z)" >&2
    return 1
  fi
  if ! remote_tags="$(git ls-remote --tags "$remote")"; then
    echo "release-images.sh: could not list $remote's tags" >&2
    return 1
  fi
  promote_tag="$requested-pending"
  stamp_tag="$requested"
  promote_commit="$(peeled_tag "$promote_tag")"
  if [ -z "$promote_commit" ]; then
    echo "release-images.sh: $remote has no tag $promote_tag: scripts/release-from-trunk.sh tags a soaked rc vX.Y.0-pending before it stages its images" >&2
    return 1
  fi
  if [ -n "$(peeled_tag "$requested")" ]; then
    echo "release-images.sh: $requested is already promoted; there is nothing pending to stage" >&2
    return 1
  fi
  moving=()
}

# Decide what `rc` publishes, from the remote's refs alone: sets
# promote_tag (the vX.Y.0-rc.N name), promote_commit and moving (the
# channel tags that move with it: candidate and rc, or none). $1 is the rc
# asked for by name, or empty for "whichever rc candidate is on".
select_rc() {
  local requested="$1" candidate_commit sha ref name newest
  if ! remote_tags="$(git ls-remote --tags "$remote")"; then
    echo "release-images.sh: could not list $remote's tags" >&2
    return 1
  fi
  candidate_commit="$(git ls-remote --heads "$remote" candidate | awk '$2 == "refs/heads/candidate" {print $1}')"
  promote_tag="$requested"
  if [ -n "$requested" ]; then
    if ! [[ "$requested" =~ $rc_tag_pattern ]]; then
      echo "release-images.sh: $requested is not a release candidate tag (vX.Y.0-rc.N)" >&2
      return 1
    fi
    promote_commit="$(peeled_tag "$requested")"
    if [ -z "$promote_commit" ]; then
      echo "release-images.sh: $remote has no tag $requested" >&2
      return 1
    fi
  else
    if [ -z "$candidate_commit" ]; then
      echo "release-images.sh: $remote has no candidate branch, so there is no release candidate to publish" >&2
      return 1
    fi
    promote_commit="$candidate_commit"
    promote_tag="$(
      while read -r sha ref; do
        name="${ref#refs/tags/}"
        name="${name%"^{}"}"
        if [[ "$name" =~ $rc_tag_pattern ]] && [ "$(peeled_tag "$name")" = "$promote_commit" ]; then
          printf '%s\n' "$name"
        fi
      done <<< "$remote_tags" | sort -u -V | tail -n 1
    )"
    if [ -z "$promote_tag" ]; then
      echo "release-images.sh: candidate is $promote_commit, which no vX.Y.0-rc.N tag names: scripts/release-from-trunk.sh tags an rc and moves candidate together, so this is a candidate somebody moved by hand" >&2
      return 1
    fi
  fi
  newest="$(
    while read -r sha ref; do
      name="${ref#refs/tags/}"
      name="${name%"^{}"}"
      [[ "$name" =~ $rc_tag_pattern ]] && printf '%s\n' "$name"
    done <<< "$remote_tags" | sort -u -V | tail -n 1
  )"
  moving=()
  if [ "$newest" = "$promote_tag" ]; then
    moving=(candidate rc)
  else
    echo "release-images.sh: $remote's newest release candidate is $newest, not $promote_tag, so only $promote_tag is published and candidate and rc stay put"
  fi
}

# Shallow-fetch refs/tags/$promote_tag from $remote into an empty $1 git
# directory, borrowing $repo_root/.git/objects when fetching from origin so
# the git proxy tunnel only sends the tag delta rather than a full pack, and
# retrying transient tunnel drops.
fetch_promote_tag_into() {
  local work="$1" depth="${2:-1}" url attempt
  url="$(git -C "$repo_root" remote get-url "$remote" 2>/dev/null || printf '%s' "$remote")"
  git -C "$work" init -q
  if [ "$remote" = "origin" ] && [ -d "$repo_root/.git/objects" ]; then
    mkdir -p "$work/.git/objects/info"
    printf '%s\n' "$repo_root/.git/objects" >"$work/.git/objects/info/alternates"
  fi
  for attempt in 1 2 3; do
    if git -C "$work" fetch -q --depth "$depth" --no-tags "$url" "refs/tags/$promote_tag"; then
      return 0
    fi
    [ "$attempt" -lt 3 ] && sleep "$attempt"
  done
  return 1
}

# Whether $promote_tag's own Dockerfile takes MAIN_GRAIN, asked of a
# shallow fetch of the tag since this job's checkout is main or
# candidate's head rather than the release. Also sets
# promote_is_root_commit (whether the tag is a root or initial cutover
# commit).
release_takes_main_grain() {
  local work answer=1
  promote_is_root_commit=false
  work="$(mktemp -d)"
  if ! fetch_promote_tag_into "$work" 2; then
    rm -rf "$work"
    echo "release-images.sh: could not fetch $promote_tag from $remote to inspect its Dockerfile" >&2
    exit 1
  fi
  if git -C "$work" show FETCH_HEAD:Dockerfile 2>/dev/null | grep -q '^ARG MAIN_GRAIN='; then
    answer=0
  fi
  if ! git -C "$work" rev-parse --verify -q "FETCH_HEAD^" >/dev/null 2>&1 ||
     ! git -C "$work" cat-file -e "FETCH_HEAD^:Dockerfile" 2>/dev/null; then
    promote_is_root_commit=true
  fi
  rm -rf "$work"
  return "$answer"
}

ensure_transparent_cache
ensure_network_mtu
ensure_direct_registry_route "$registry"

stage_grain_ca

if [ "$dry_run" = true ]; then
  echo "release-images.sh: [dry-run] repository: $GRAIN_CI_REPO (ref: $GRAIN_CI_REF, commit: $commit)"
  echo "release-images.sh: [dry-run] registry: $registry, root: $root"
  echo "release-images.sh: [dry-run] images: guest=$guest, k8s_sandbox=$k8s_sandbox, grain=$grain"
  echo "release-images.sh: [dry-run] base images: guest_base=$guest_base, k8s_sandbox_base=$k8s_sandbox_base"
  echo "release-images.sh: [dry-run] daemon image: $grain from $core_main (install track oss)"
  case "$phase" in
    build | all)
      if [ -n "$build_version" ]; then
        echo "release-images.sh: [dry-run] version: $build_version, stamped into the daemon binary as -X github.com/ai-on-gke/grain/pkg/version.Release=$build_version and labelled onto $grain as org.opencontainers.image.version=$build_version"
      else
        echo "release-images.sh: [dry-run] version: none; the daemon image would report itself unversioned"
      fi
      ;;
  esac
  case "$phase" in
    build)
      echo "release-images.sh: [dry-run] phase 'build': would build $guest, $k8s_sandbox and $grain, publishing the two sandbox images so the daemon image can name them by digest (state: $state)"
      ;;
    push)
      echo "release-images.sh: [dry-run] phase 'push': would publish and sign $grain to $registry, and move the $branch tags of all three"
      ;;
    guest-cache)
      echo "release-images.sh: [dry-run] phase 'guest-cache': would resolve and build guest cache inputs"
      ;;
    all)
      echo "release-images.sh: [dry-run] phase 'all': would build and publish $guest, $k8s_sandbox and $grain to $registry"
      ;;
    promote)
      select_promotion "$promote_request"
      targets="$promote_tag"
      [ "$promote_latest" = true ] && targets="$targets and latest"
      echo "release-images.sh: [dry-run] phase 'promote': would retag $root/{guest,k8s-sandbox}:sha-${promote_commit:0:7} as $targets"
      if release_takes_main_grain; then
        echo "release-images.sh: [dry-run] and rebuild $root/grain:sha-${promote_commit:0:7} from $promote_commit ($core_main) with RELEASE=$promote_tag as $targets, unless $root/grain:$promote_tag already reports $promote_tag"
      else
        echo "release-images.sh: [dry-run] and rebuild $root/grain:sha-${promote_commit:0:7} from $promote_commit with RELEASE=$promote_tag as $targets, unless $root/grain:$promote_tag already reports $promote_tag"
      fi
      echo "release-images.sh: [dry-run] and point $root/grain:sha-${promote_commit:0:7} at the stamped $root/grain:$promote_tag"
      if [ "$promote_latest" != true ]; then
        echo "release-images.sh: [dry-run] latest stays put: the git tag latest names ${latest_commit:-nothing}, not $promote_tag"
      fi
      ;;
    rc)
      select_rc "$rc_request"
      targets="$promote_tag"
      [ "${#moving[@]}" -eq 0 ] || targets="$targets, ${moving[*]}"
      echo "release-images.sh: [dry-run] phase 'rc': would retag $root/{guest,k8s-sandbox}:sha-${promote_commit:0:7} as $targets"
      echo "release-images.sh: [dry-run] and rebuild $root/grain:sha-${promote_commit:0:7} from $promote_commit ($core_main) with RELEASE=$promote_tag as $targets, unless $root/grain:$promote_tag already reports $promote_tag"
      if [ "${#moving[@]}" -eq 0 ]; then
        echo "release-images.sh: [dry-run] candidate and rc stay put"
      fi
      ;;
    pending)
      select_pending "$pending_request"
      echo "release-images.sh: [dry-run] phase 'pending': would retag $root/{guest,k8s-sandbox}:sha-${promote_commit:0:7} as $promote_tag, and rebuild $root/grain:sha-${promote_commit:0:7} from $promote_commit with RELEASE=$stamp_tag as $promote_tag, unless it already reports $stamp_tag at $promote_commit; $stamp_tag and latest stay put"
      ;;
    discard-pending)
      echo "release-images.sh: [dry-run] phase 'discard-pending': would tag $root/{grain,guest,k8s-sandbox}:$pending_request-pending as $pending_request-rejected"
      ;;
    discard-rc)
      if ! [[ "$rc_request" =~ $rc_tag_pattern ]]; then
        echo "release-images.sh: $rc_request is not a release candidate tag (vX.Y.0-rc.N)" >&2
        exit 1
      fi
      echo "release-images.sh: [dry-run] phase 'discard-rc': would tag $root/{grain,guest,k8s-sandbox}:$rc_request as $rc_request-discarded, and leave candidate and rc where they are"
      ;;
  esac
  exit 0
fi

check_ghcr_token_scopes() {
  [ "$registry" = "ghcr.io" ] || return 0
  case "$token" in
    github_pat_*)
      echo "release-images.sh: the publishing credential for $GRAIN_CI_REPO is a fine-grained personal access token (github_pat_...), which ghcr.io does not accept for container packages (docs/ci-capabilities.md)." >&2
      echo "release-images.sh: configure a GitHub App installation with 'packages: write' or a classic PAT (ghp_...) with 'write:packages' scope in Settings -> Git Connections (or set release=off for $GRAIN_CI_REPO if another deployment publishes its releases)." >&2
      echo "release-images.sh: stopping before the build rather than at the push: docker login ghcr.io succeeds without checking token scopes, and ghcr.io refuses the push after the images are built." >&2
      exit 1
      ;;
    ghp_*)
      command -v curl >/dev/null 2>&1 || return 0
      local headers line lower scopes="" has_scopes=false
      headers="$(curl -sSI --connect-timeout 5 -H "Authorization: Bearer $token" "${GITHUB_API_URL:-https://api.github.com}/rate_limit" 2>/dev/null || true)"
      while IFS= read -r line; do
        line="${line%$'\r'}"
        lower="${line,,}"
        case "$lower" in
          x-oauth-scopes:*)
            has_scopes=true
            scopes="${lower#x-oauth-scopes:}"
            ;;
        esac
      done <<< "$headers"
      if [ "$has_scopes" = true ] && [[ ", ${scopes//,/ , } ," != *" write:packages "* ]]; then
        scopes="${scopes#"${scopes%%[![:space:]]*}"}"
        echo "release-images.sh: the GitHub credential for $GRAIN_CI_REPO is a classic PAT with scopes '${scopes:-none}', which is missing 'write:packages' (docs/ci-capabilities.md)." >&2
        echo "release-images.sh: configure a GitHub App installation with 'packages: write' or grant 'write:packages' on the classic PAT in Settings -> Git Connections (or set release=off for $GRAIN_CI_REPO if another deployment publishes its releases)." >&2
        echo "release-images.sh: stopping before the build rather than at the push: docker login ghcr.io succeeds without checking token scopes, and ghcr.io refuses the push with 'permission_denied: The token provided does not match expected scopes.' after the images are built." >&2
        exit 1
      fi
      ;;
  esac
}

if [ -n "$token" ]; then
  check_ghcr_token_scopes
  printf '%s' "$token" | docker login "$registry" -u "$username" --password-stdin
else
  # Nothing above supplied a login, so the only thing that could publish is
  # a `docker login` somebody already did. In Grain CI nobody did: a job's
  # sandbox is built fresh, and the one thing that puts a registry
  # credential in it is the `package-publish` CI capability. Say so here
  # rather than letting the release build for the best part of an hour and
  # die at the push -- which is what it did for every merge from
  # 199b85bcd to 37fe68494, each one ending in ghcr.io's own
  # "unauthenticated: User cannot be authenticated with the token
  # provided" after the images were already built and tested.
  #
  # The grant is deliberately not something this file can give itself
  # (.grain/README.md: a workflow cannot widen what it is trusted with), so
  # the only useful thing to print is the operator command that does.
  if [ "${GRAIN_CI:-}" = true ]; then
    echo "release-images.sh: no publishing credential reached this job, so there is nothing to publish $registry images as." >&2
    echo "release-images.sh: Grain CI places one at /home/debian/.package-publish-{registry,username,token} when the repository is granted the 'package-publish' CI capability, which is operator-owned repository configuration and cannot be granted from a workflow file (docs/ci-capabilities.md):" >&2
    echo "release-images.sh:     grain repo ci-capabilities -set package-publish $GRAIN_CI_REPO" >&2
    echo "release-images.sh: stopping before the build rather than at the push: an anonymous push is refused by the registry, an hour of building and testing later." >&2
    exit 1
  fi
  # A hand-run is a different matter: the operator is watching, may have
  # logged in already, and may want `build` for its own sake, which
  # publishes nothing at all on a guest cache hit.
  echo "release-images.sh: using Docker's existing credentials for $registry"
fi
unset token PACKAGE_PUBLISH_TOKEN REGISTRY_PUSH_TOKEN

push_image() {
  local image="$1" attempt log
  log="$(mktemp)"
  for attempt in 1 2 3; do
    if docker push "$image" 2>&1 | tee "$log"; then
      rm -f "$log"
      return 0
    fi
    if grep -qE 'permission_denied|does not match expected scopes|insufficient_scope|unauthorized: authentication required' "$log" 2>/dev/null; then
      rm -f "$log"
      echo "release-images.sh: registry refused push of $image due to missing package write permission (ensure the credential for ${GRAIN_CI_REPO:-this repository} has 'write:packages' / 'packages: write')" >&2
      return 1
    fi
    if [ "$attempt" -lt 3 ]; then
      sleep $((20 * attempt))
    fi
  done
  rm -f "$log"
  echo "could not push $image in three attempts" >&2
  return 1
}

# repository_of is $1 without its tag or digest; the colon a tag follows
# is told from a registry host's port by the slash after it.
repository_of() {
  local repo="${1%%@*}"
  case "${repo##*/}" in *:*) repo="${repo%:*}" ;; esac
  printf '%s' "$repo"
}

# push_pinned pushes $1, a `repo:tag`, and sets $pinned to
# `repo:tag@sha256:...`: the digest the registry now serves for that tag,
# as the push itself reported it ("<tag>: digest: sha256:... size: N").
# Read off the push rather than out of the local image's RepoDigests,
# which can hold more than one digest for the same repository -- a guest
# cache hit was pulled under one name and is pushed under another.
push_pinned() {
  local image="$1" attempt log digest
  pinned=""
  log="$(mktemp)"
  for attempt in 1 2 3; do
    if docker push "$image" 2>&1 | tee "$log"; then
      digest="$(grep -oE 'digest: sha256:[0-9a-f]{64}' "$log" | tail -n 1 || true)"
      rm -f "$log"
      digest="${digest#digest: }"
      if [ -z "$digest" ]; then
        echo "release-images.sh: docker push $image reported no digest, so there is nothing to stamp the grain image with" >&2
        return 1
      fi
      pinned="$image@$digest"
      echo "release-images.sh: published $pinned"
      return 0
    fi
    if grep -qE 'permission_denied|does not match expected scopes|insufficient_scope|unauthorized: authentication required' "$log" 2>/dev/null; then
      rm -f "$log"
      echo "release-images.sh: registry refused push of $image due to missing package write permission (ensure the credential for ${GRAIN_CI_REPO:-this repository} has 'write:packages' / 'packages: write')" >&2
      return 1
    fi
    if [ "$attempt" -lt 3 ]; then
      sleep $((20 * attempt))
    fi
  done
  rm -f "$log"
  echo "could not push $image in three attempts" >&2
  return 1
}

# One `granule granulefile build`, wherever the binary came from, packed
# onto the granule image this repository builds rather than onto the
# `granule:latest` granulefile would otherwise write into the Dockerfile
# it generates -- a Docker Hub reference nothing here produces. See
# scripts/granule/build-granule-image.sh.
granulefile_build() {
  if command -v granule >/dev/null 2>&1; then
    granule granulefile build -kontur-image "$granule_image" "$@"
  else
    go run github.com/bwsalmon/granule/cmd/granule granulefile build -kontur-image "$granule_image" "$@"
  fi
}

# Leave a guest image tagged "$guest" behind, built via declarative Granulefile.
resolve_guest() {
  mapfile -t guest_inputs < <(./scripts/granule/guest-inputs.sh guest)
  guest_key="$(git rev-parse "${guest_inputs[@]/#/HEAD:}" | sha256sum | cut -c1-32)"
  guest_cache="$root/guest:inputs-$guest_key"
  local guest_hit=false
  if docker pull "$guest_cache"; then
    docker tag "$guest_cache" "$guest"
    echo "release-images.sh: sandbox guest cache hit $guest_cache; nothing to build"
    if [ "$phase" = guest-cache ]; then
      return 0
    fi
    guest_hit=true
  else
    echo "release-images.sh: sandbox guest cache miss $guest_cache; building the guest"

    # Before either half: both of them pack onto it, and on a base cache
    # hit the guest build below is still the one that needs it. Relabelled
    # as this repository's, because everything built on it inherits the
    # vendored Dockerfile's org.opencontainers.image.source and GHCR files
    # a package by that label alone -- these images are pushed.
    IMAGE="$granule_image" GUEST_SOURCE_REPO="https://github.com/${RELEASE_IMAGE_REPO,,}" \
      ./scripts/granule/build-granule-image.sh
  fi

  mapfile -t base_inputs < <(./scripts/granule/guest-inputs.sh base)
  base_key="$(git rev-parse "${base_inputs[@]/#/HEAD:}" | sha256sum | cut -c1-32)"
  base_cache="$root/guest-base:$base_key"
  if docker pull "$base_cache"; then
    docker tag "$base_cache" granule-guest-base:vendored
    docker tag "$base_cache" "$guest_base"
  else
    if [ "$guest_hit" = true ]; then
      IMAGE="$granule_image" GUEST_SOURCE_REPO="https://github.com/${RELEASE_IMAGE_REPO,,}" \
        ./scripts/granule/build-granule-image.sh
    fi
    granulefile_build -f Granulefile.guest-base -t granule-guest-base:vendored
    docker tag granule-guest-base:vendored "$base_cache"
    docker tag granule-guest-base:vendored "$guest_base"
    push_image "$base_cache"
  fi

  if [ "$guest_hit" = true ]; then
    return 0
  fi

  granulefile_build -f Granulefile -t "$guest"
  docker tag "$guest" "$guest_cache"
  push_image "$guest_cache"
  echo "release-images.sh: published $guest_cache"
}

# Whether daemon image $1 is core grain (`./cmd/grain`), reporting no
# edition in `grain version --json`.
check_edition() {
  local image="$1" main="$2" answered
  answered="$(docker run --rm "$image" version --json | tr -d ' \n')"
  if [[ "$answered" == *'"edition":'* ]]; then
    echo "release-images.sh: $image was built from $main, core grain, but reports an edition: $answered" >&2
    exit 1
  fi
}

# Build daemon image $1 from main package $2, naming the two sandbox images
# build_images just pinned, and check what it says of itself before
# anything may publish it.
build_daemon_image() {
  local image="$1" main="$2" pair question want answered labelled stamp_dir container stamped
  make image IMAGE="$image" MAIN_grain="$main" SANDBOX_IMAGE="$guest_pinned" K8S_SANDBOX_IMAGE="$k8s_sandbox_pinned" \
    GRAIN_IMAGE_REF="$image" GIT_REVISION="$commit" RELEASE="${RELEASE:-}"

  # The stamp is the point of pushing the sandboxes first, so it is read
  # back off the image the way a deployment reads it -- `grain
  # sandbox-image` and `grain k8s-sandbox-image` -- rather than trusted
  # to have survived a Dockerfile ARG and a Makefile -X on their way in.
  for pair in "sandbox-image $guest_pinned" "k8s-sandbox-image $k8s_sandbox_pinned"; do
    read -r question want <<< "$pair"
    answered="$(docker run --rm "$image" "$question")"
    if [ "$answered" != "$want" ]; then
      echo "release-images.sh: $image answers \`$question\` with ${answered:-nothing}, not the $want it was built to name" >&2
      exit 1
    fi
  done

  # The label is the record, so a release whose label does not name this
  # commit does not get published at all.
  if ! labelled="$(docker image inspect \
      --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image" 2>&1)"; then
    echo "release-images.sh: could not read $image's revision label: $labelled" >&2
    exit 1
  fi
  if [ "$labelled" != "$commit" ]; then
    echo "release-images.sh: $image is labelled with ${labelled:-no commit}, not $commit" >&2
    exit 1
  fi

  # The binary's own stamp (`go version -m`, which pkg/version reads back
  # for the UI's version line) is the second record, and it is checked
  # where it can be: it must agree with the label, and a build that could
  # not take one says so here rather than in the middle of a build log.
  # Read with the host's go, which built granule above -- the runtime
  # image carries no toolchain -- and skipped where there is none, since a
  # missing `go` is a reason to publish an unverified second record rather
  # than to lose the release.
  if command -v go >/dev/null 2>&1; then
    stamp_dir="$(mktemp -d)"
    container="$(docker create "$image")"
    docker cp "$container:/usr/local/bin/grain" "$stamp_dir/grain" >/dev/null
    docker rm "$container" >/dev/null
    stamped="$(go version -m "$stamp_dir/grain" | awk -F= '$1 ~ /[\t ]vcs\.revision$/ { print $2 }')"
    rm -rf "$stamp_dir"
    if [ -z "$stamped" ]; then
      echo "release-images.sh: $image carries no commit stamp -- its revision label is the whole record;" \
        "scripts/buildvcs.sh printed git's own reason earlier in this log"
    elif [ "$stamped" != "$commit" ]; then
      echo "release-images.sh: $image was built from $stamped, not the $commit this release is for" >&2
      exit 1
    fi
  fi
  if [ -n "${RELEASE:-}" ]; then
    check_release_stamp "$image" "$RELEASE"
  fi

  check_edition "$image" "$main"
}

build_images() {
  resolve_guest

  stage_grain_ca

  # Kubernetes runs task sandboxes directly as unprivileged pods rather than
  # booting the Granule guest above. Built here in the order the images
  # depend on each other: both sandboxes before the grain image that names
  # them.
  docker build --target k8s-sandbox-base -f Dockerfile.k8s-sandbox -t "$k8s_sandbox_base" .
  docker build -f Dockerfile.k8s-sandbox -t "$k8s_sandbox" .

  # Published and pinned before the grain image is built, so that
  # the reference stamped into it is the digest and not only the tag
  # (docs/image-signing.md). A tag is a name the registry lets anybody
  # with push access repoint; an upgrade verifies the digest it pulls,
  # and every VM and pod is then created from the stamped reference --
  # which, stamped by digest, is that same verified image rather than
  # whatever the tag names by the time a sandbox starts. The registry
  # assigns the digest on push, so the push has to come first.
  #
  # Only the immutable sha- tags, which name this commit and are resolved
  # by nothing until a grain image naming them is published: the branch
  # tags still move in the push phase, after the suite has passed. A
  # release that fails its suite leaves these two behind with no grain
  # image to name them, which is what ghcr-prune's sweep is for.
  #
  # Pushed, but not signed: a signature is the release key vouching for
  # an image, and nothing has vouched for these until the suites between
  # this phase and `push` have passed. publish_images signs them, by the
  # digests recorded here, before it publishes the grain image that names
  # them -- so a deployment verifying signatures refuses an orphan left
  # by a failed release, rather than trusting it.
  push_pinned "$guest_base"
  guest_base_pinned="$pinned"
  push_pinned "$guest"
  guest_pinned="$pinned"
  push_pinned "$k8s_sandbox_base"
  k8s_sandbox_base_pinned="$pinned"
  push_pinned "$k8s_sandbox"
  k8s_sandbox_pinned="$pinned"

  # An image published to a registry that cannot say which commit it came
  # from outlives the build that made it, so this release insists on being
  # able to say it -- but it insists on it of the *image*, below, rather
  # than by passing BUILDVCS=true here.
  #
  # BUILDVCS=true means "stamp the commit or fail", and what it makes the
  # release depend on is not the commit -- which is not in doubt; $commit is
  # what the release event named -- but on git inside the build container
  # being able to read the `.git` this build context copies in. Every way
  # that fails is environmental (scripts/buildvcs.sh lists them: ownership,
  # a builder git older than the one that wrote the checkout, a .git that
  # does not describe a repository from where the build runs), none of them
  # says anything about the source being built, and all of them arrive as
  # `error obtaining VCS status: exit status 128` with git's own
  # explanation swallowed. Failing there used to fail a release that had
  # already published the guest and the sandbox image, over a record this
  # script is holding in a variable; since the phase split it fails one
  # that has published neither, which is the right way round but not a
  # reason to go back to a flag that says nothing about the source.
  #
  # So the build resolves -buildvcs the way every other build of this
  # checkout does, and what is checked is the artifact. GIT_REVISION is the
  # commit as a label on the image (org.opencontainers.image.revision),
  # which is what CI's publish job reads back off a moving tag to decide
  # whether it would move backwards -- passed in rather than derived, so it
  # is right whatever the builder's git could make of the tree.
  #
  # RELEASE is the version stamped into the binary (pkg/version.Release)
  # and labelled onto the image: the caller's when it set one, otherwise
  # the build version worked out above (v2.3.0-dev.<n>+<sha7> on main,
  # v2.2.0-rc.1.<n>+<sha7> on candidate). Never a bare vX.Y.Z here: that
  # is `promote`'s to stamp, by rebuilding.
  build_daemon_image "$grain" "$core_main"

  cat > "$state" <<EOF
# Written by scripts/release-images.sh for $commit. The steps after the
# build read this: the suite that drives the image, and the push phase
# that publishes it.
RELEASE_IMAGE_COMMIT='$commit'
RELEASE_IMAGE_GUEST_BASE='$guest_base'
RELEASE_IMAGE_GUEST='$guest'
RELEASE_IMAGE_K8S_SANDBOX_BASE='$k8s_sandbox_base'
RELEASE_IMAGE_K8S_SANDBOX='$k8s_sandbox'
RELEASE_IMAGE_GRAIN='$grain'
# Already published by the build, and stamped into the grain image by
# these references; the push phase signs them.
RELEASE_IMAGE_GUEST_BASE_PINNED='$guest_base_pinned'
RELEASE_IMAGE_GUEST_PINNED='$guest_pinned'
RELEASE_IMAGE_K8S_SANDBOX_BASE_PINNED='$k8s_sandbox_base_pinned'
RELEASE_IMAGE_K8S_SANDBOX_PINNED='$k8s_sandbox_pinned'
EOF
  cleanup_ca
  echo "release-images.sh: built $grain, naming $guest_pinned and $k8s_sandbox_pinned; $state names them"
}

# Signing (docs/image-signing.md, grain/task-1773). A signature covers a
# manifest digest, not a tag, so each image is signed once, by digest,
# right after its immutable sha- tag is pushed: the branch tag pushed
# below and the guest and k8s-sandbox `latest`/vX.Y.Z that `promote`
# retags registry-side all name that same digest and so carry the same
# signature, with nothing to re-sign. The grain image `promote` rebuilds
# to stamp its release is a new digest, and is signed there like any
# other. Deployments verify it with the matching public key before an
# upgrade runs anything (pkg/upgrade/verify.go, scripts/setup.sh).
#
# The key is a cosign private key placed in this sandbox as a file, the
# way every capability credential is, and never held in repository YAML:
# $IMAGE_SIGNING_KEY, by default the path the `image-signing` CI
# capability places it at, with its password beside it. Keyed rather than
# keyless because a Grain CI runner has no OIDC identity Fulcio would
# certify -- the only token it could mint is the deployment's own service
# account, which every sandbox granted that file shares -- and because
# keyless would publish every digest to the public Rekor log. So
# --tlog-upload=false, and verifiers pass --insecure-ignore-tlog=true.
#
# No key, no signature: the release goes ahead unsigned and says so, as
# every release before this did, unless REQUIRE_IMAGE_SIGNING=true. A key
# with no cosign to use it is a failure, though -- a release that was
# given a key and quietly did not sign is the worst of both.
signing_key="${IMAGE_SIGNING_KEY:-/home/debian/.image-signing-key}"
signing_password_file="${IMAGE_SIGNING_PASSWORD_FILE:-/home/debian/.image-signing-password}"
# REQUIRE_IMAGE_SIGNING=1 means what =true does: an operator who sets the
# usual spelling of "on" must not get an unsigned release and a warning.
case "${REQUIRE_IMAGE_SIGNING:-false}" in
  1 | [Tt]rue | TRUE | [Yy]es | YES) require_signing=true ;;
  *) require_signing=false ;;
esac

# cosign itself, from the Go module proxy when this sandbox has none:
# pinned, so what signs a release is a version somebody chose. The
# sandbox already has Go -- the guest build needs it.
cosign_version="${COSIGN_VERSION:-v2.4.1}"
ensure_cosign() {
  command -v cosign >/dev/null 2>&1 && return 0
  echo "release-images.sh: installing cosign $cosign_version"
  if ! GOBIN="${TMPDIR:-/tmp}/release-images-cosign" go install "github.com/sigstore/cosign/v2/cmd/cosign@$cosign_version"; then
    echo "release-images.sh: a signing key is at $signing_key but cosign could not be installed, so nothing can be signed" >&2
    return 1
  fi
  PATH="${TMPDIR:-/tmp}/release-images-cosign:$PATH"
}

# $2, when given, is the `repo@sha256:...` to sign -- push_pinned's
# answer -- and otherwise it is read out of $1's RepoDigests.
sign_image() {
  local image="$1" repo digest_ref="${2:-}" attempt
  if [ ! -r "$signing_key" ]; then
    if [ "$require_signing" = true ]; then
      echo "release-images.sh: REQUIRE_IMAGE_SIGNING is set and there is no signing key at $signing_key -- grant this job the image-signing CI capability" >&2
      return 1
    fi
    echo "release-images.sh: WARNING: no signing key at $signing_key, so $image is published unsigned and a deployment verifying signatures will refuse it"
    return 0
  fi
  ensure_cosign || return 1
  repo="${image%:*}"
  [ -n "$digest_ref" ] || digest_ref="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image" \
    | awk -v repo="$repo" 'index($0, repo "@sha256:") == 1 {print; exit}')"
  if [ -z "$digest_ref" ]; then
    echo "release-images.sh: $image has no digest from $repo after its push, so there is nothing to sign" >&2
    return 1
  fi
  for attempt in 1 2 3; do
    if COSIGN_PASSWORD="$(cat "$signing_password_file" 2>/dev/null || true)" \
      cosign sign --yes --key "$signing_key" --tlog-upload=false "$digest_ref"; then
      echo "release-images.sh: signed $digest_ref ($image)"
      return 0
    fi
    if [ "$attempt" -lt 3 ]; then
      sleep $((20 * attempt))
    fi
  done
  echo "could not sign $digest_ref in three attempts" >&2
  return 1
}

publish_images() {
  ensure_direct_registry_route "$registry"
  # The two sandbox images' sha- tags were published by the build, which
  # had to have their digests to stamp into the grain image. They are
  # signed here, by those digests, now that the release's suites have
  # passed -- and before the grain image naming them is published, so no
  # published grain ever names a sandbox that is not yet signed.
  if [ -n "${guest_base_pinned:-}" ]; then
    sign_image "$guest_base" "$(repository_of "$guest_base_pinned")@${guest_base_pinned#*@}"
  fi
  sign_image "$guest" "$(repository_of "$guest_pinned")@${guest_pinned#*@}"
  if [ -n "${k8s_sandbox_base_pinned:-}" ]; then
    sign_image "$k8s_sandbox_base" "$(repository_of "$k8s_sandbox_base_pinned")@${k8s_sandbox_base_pinned#*@}"
  fi
  sign_image "$k8s_sandbox" "$(repository_of "$k8s_sandbox_pinned")@${k8s_sandbox_pinned#*@}"
  # The daemon image, signed before any moving tag names it, so no tag a
  # deployment resolves ever points at an image that is not yet signed.
  push_image "$grain"
  sign_image "$grain"

  # Immutable tags are published for every merge. Moving tags move forward
  # only: onto this commit when the commit the grain image's branch tag
  # holds now is an ancestor of it, so overlapping releases cannot let an
  # older build overwrite a newer one merely because it finished last --
  # and, unlike the "only from the branch tip" rule this replaced, a run of
  # merges no longer keeps every release from moving it (grain/task-4432).
  # tag_may_move decides, failing closed, and says why.
  # The branch tag is the only moving tag a merge moves: `latest` is the
  # last promoted release, and `promote` is the one thing that moves it.
  # grain:<branch> decides for all three: the tags move together, so what
  # one holds is what they all hold.
  if ! tag_may_move "$root/grain:$branch" "$commit" "$GRAIN_CI_REF"; then
    echo "published immutable sha-$short images; moving tags stay put"
    return 0
  fi

  for pair in \
    "$guest_base $root/guest-base:$branch" \
    "$guest $root/guest:$branch" \
    "$k8s_sandbox_base $root/k8s-sandbox-base:$branch" \
    "$k8s_sandbox $root/k8s-sandbox:$branch" \
    "$grain $root/grain:$branch"; do
    read -r source target <<< "$pair"
    docker tag "$source" "$target"
    push_image "$target"
  done
}

# Publish a release's images under vX.Y.Z (and `latest`). Every source is
# checked before anything moves, so a release with one of its images
# missing moves none of them rather than leaving `latest` naming a grain
# from one commit and a guest from another.
#
# The guest and the Kubernetes sandbox are retagged registry-side: they
# carry no version anybody reads. The daemon image (`grain`) is rebuilt,
# once per release, because the one built when the commit was the
# candidate carries the version Go read off that commit -- the rc tag,
# since vX.Y.Z did not exist yet -- and that is what the UI's sidebar
# footer and /api/config's version.release would go on showing
# (grain/task-3064). The rebuild is of the same commit and the same main,
# naming the same pinned sandbox images and the same image reference the
# candidate's build stamped -- read back off that image -- so the release
# name is the only thing that differs. It is then published as vX.Y.Z,
# labelled with the release, and every later tick sees that label and
# rebuilds nothing, so `latest`'s digest does not churn.
promote_images() {
  local image source missing=() targets tag daemon main
  select_promotion "$promote_request"
  targets=("$promote_tag")
  if [ "$promote_latest" = true ]; then
    targets+=(latest)
  else
    echo "release-images.sh: the git tag latest names ${latest_commit:-nothing}, not $promote_tag ($promote_commit), so only $promote_tag is published and latest stays put"
  fi
  publish_release
}

# Publish a release candidate's images under vX.Y.0-rc.N (and candidate
# and rc): exactly a promotion's publishing, of the rc tag's commit, with
# the rc stamped instead of a release -- see promote_images for why each
# image is retagged or rebuilt.
rc_images() {
  local targets
  select_rc "$rc_request"
  targets=("$promote_tag" ${moving[@]+"${moving[@]}"})
  publish_release
}

# What promote_images and rc_images share: publish $promote_commit's
# images as each of targets, the first of which is the name stamped into
# the daemon image ($promote_tag) and the rest the moving tags that go
# with it.
publish_release() {
  local image source missing=() tag daemon main
  local promoted_short="${promote_commit:0:7}"
  ensure_direct_registry_route "$registry"
  local daemons=()
  if release_takes_main_grain; then
    daemons=("grain $core_main")
  else
    daemons=("grain ")
  fi
  # The daemons' sha- builds are only the rebuilds' source. Until the
  # tick below moves <daemon>:sha-<short> onto the stamped vX.Y.Z it is a
  # version of its own, which cmd/ghcr-prune deletes after -keep-for like
  # any other commit's build -- and requiring it on every later tick
  # turned each one red two days after the release (v1.9.0,
  # grain/task-3867). The guest and k8s-sandbox sha- tags share their
  # version with vX.Y.Z, so prune keeps them, and they are what is
  # retagged here on every tick.
  local stamped=() stamp="${stamp_tag:-$promote_tag}"
  for daemon in "${daemons[@]}"; do
    read -r image main <<< "$daemon"
    if [ -n "$stamp_tag" ]; then
      # `pending`: a :vX.Y.0-pending a rejected release left behind on
      # another commit says the same version, so the commit is asked too.
      if stamped_build "$root/$image:$promote_tag" "$stamp"; then
        stamped+=("$image")
      fi
    elif [ "$(published_release "$root/$image:$promote_tag")" = "$stamp" ]; then
      stamped+=("$image")
    elif stamped_build "$root/$image:$promote_tag-pending" "$stamp"; then
      # Approving a pending release (scripts/release-from-trunk.sh
      # approve): `pending` already built and stamped this, so promoting
      # it is a retag of :vX.Y.0-pending and nothing is rebuilt.
      echo "release-images.sh: $root/$image:$promote_tag-pending is $promote_commit stamped $stamp; promoting it as built"
      retag_image "$root/$image:$promote_tag-pending" "$root/$image:$promote_tag"
      stamped+=("$image")
    fi
  done
  if [ "$promote_is_root_commit" = true ] &&
      { ! image_exists "$root/guest:sha-$promoted_short" ||
        ! image_exists "$root/k8s-sandbox:sha-$promoted_short" ||
        { [[ " ${stamped[*]} " != *" grain "* ]] && ! image_exists "$root/grain:sha-$promoted_short"; }; } &&
      image_exists "$root/guest:main" &&
      image_exists "$root/k8s-sandbox:main" &&
      image_exists "$root/grain:main"; then
    echo "release-images.sh: $promote_tag ($promote_commit) is the initial cutover commit and has no sha-$promoted_short images; seeding from :main"
    for image in guest k8s-sandbox grain; do
      retag_image "$root/$image:main" "$root/$image:sha-$promoted_short"
    done
    for image in guest-base k8s-sandbox-base; do
      if image_exists "$root/$image:main"; then
        retag_image "$root/$image:main" "$root/$image:sha-$promoted_short"
      fi
    done
  fi
  for image in guest k8s-sandbox "${daemons[@]%% *}"; do
    if [[ " ${stamped[*]} " == *" $image "* ]]; then
      continue
    fi
    source="$root/$image:sha-$promoted_short"
    if ! image_exists "$source"; then
      missing+=("$source")
    fi
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "release-images.sh: $promote_tag is $promote_commit, and the release job never published ${missing[*]} for it." >&2
    echo "release-images.sh: promotion publishes what was built and tested for that commit, so nothing was moved. Re-run that commit's release (POST /api/repos/$GRAIN_CI_REPO/release-runs) and promote again." >&2
    return 1
  fi

  # The daemon images first: they are the step that can fail, and a
  # failed rebuild should leave every tag where it was.
  for daemon in "${daemons[@]}"; do
    read -r image main <<< "$daemon"
    if [[ " ${stamped[*]} " == *" $image "* ]]; then
      echo "release-images.sh: $root/$image:$promote_tag already reports $stamp; nothing to rebuild"
    else
      rebuild_release_grain "$root/$image:sha-$promoted_short" "$root/$image:$promote_tag" "$main"
    fi
  done

  for image in guest k8s-sandbox; do
    source="$root/$image:sha-$promoted_short"
    for tag in "${targets[@]}"; do
      retag_image "$source" "$root/$image:$tag"
    done
  done
  for image in guest-base k8s-sandbox-base; do
    source="$root/$image:sha-$promoted_short"
    if image_exists "$source"; then
      for tag in "${targets[@]}"; do
        retag_image "$source" "$root/$image:$tag"
      done
    fi
  done
  for daemon in "${daemons[@]}"; do
    read -r image main <<< "$daemon"
    for tag in "${targets[@]:1}"; do
      retag_image "$root/$image:$promote_tag" "$root/$image:$tag"
    done
    # And <daemon>:sha-<short> onto the stamped release too. A deployment
    # that went onto this commit while it was the candidate runs that
    # sha- build and names it as its own image -- which is the image the
    # config repo's `grain state check` job runs (pkg/daemon/ci.go's
    # ConfigRepoCISource). Left on the candidate's build, it is a version
    # no kept tag shares, so cmd/ghcr-prune deleted it after -keep-for
    # and every config check of that deployment failed pulling it
    # (grain:sha-adea62d, v1.9.0, grain/task-3926). On the release's
    # version it is kept as long as vX.Y.Z is, and a tick after the prune
    # puts back a tag already lost. The two builds are the same commit
    # with the same pinned images and image reference; only the release
    # string they report differs.
    retag_image "$root/$image:$promote_tag" "$root/$image:sha-$promoted_short"
  done
  echo "release-images.sh: $root/{guest,k8s-sandbox}:sha-$promoted_short are now also ${targets[*]}, and $root/grain:$promote_tag is $promote_commit stamped $promote_tag, as is its sha-$promoted_short$([ "${#targets[@]}" -le 1 ] || echo " and ${targets[*]:1}")"
}

# Stage a pending release: the commit vX.Y.0-pending names, published as
# :vX.Y.0-pending with vX.Y.0 stamped into the daemon image and nothing
# else moved (select_pending).
pending_images() {
  local targets
  select_pending "$pending_request"
  targets=("$promote_tag")
  publish_release
}

# Mark a pending release a person rejected: <image>:vX.Y.0-rejected
# beside every image it was staged as. Nothing that resolves moves.
discard_pending() {
  local image images=(grain guest k8s-sandbox) marked=()
  if ! [[ "$pending_request" =~ $release_tag_pattern ]]; then
    echo "release-images.sh: $pending_request is not a release tag (vX.Y.Z)" >&2
    return 1
  fi
  ensure_direct_registry_route "$registry"
  for image in "${images[@]}"; do
    if image_exists "$root/$image:$pending_request-pending"; then
      retag_image "$root/$image:$pending_request-pending" "$root/$image:$pending_request-rejected"
      marked+=("$image")
    fi
  done
  echo "release-images.sh: $pending_request is rejected${marked[*]:+: ${marked[*]} now also carry $pending_request-rejected}; $pending_request and latest were never moved"
}

# Mark a release candidate that failed its soak: <image>:vX.Y.0-rc.N-discarded
# beside every image the rc was published as. candidate and rc are not
# touched. An image the rc was never published as has nothing to mark,
# and is said so; a deployment never ran it.
discard_rc() {
  local image images=(grain guest k8s-sandbox) marked=() unpublished=()
  if ! [[ "$rc_request" =~ $rc_tag_pattern ]]; then
    echo "release-images.sh: $rc_request is not a release candidate tag (vX.Y.0-rc.N)" >&2
    return 1
  fi
  ensure_direct_registry_route "$registry"
  for image in "${images[@]}"; do
    if image_exists "$root/$image:$rc_request"; then
      retag_image "$root/$image:$rc_request" "$root/$image:$rc_request-discarded"
      marked+=("$image")
    else
      unpublished+=("$image")
    fi
  done
  if [ "${#unpublished[@]}" -gt 0 ]; then
    echo "release-images.sh: $rc_request was never published as ${unpublished[*]}, so there is nothing of theirs to mark"
  fi
  echo "release-images.sh: $rc_request is discarded${marked[*]:+: ${marked[*]} now also carry $rc_request-discarded}; candidate and rc stay where they are"
}

# The release a published grain image was stamped with, from its
# org.opencontainers.image.version label: asked of the registry where
# buildx is here, and of a pulled copy where it is not or the registry's
# answer carried no label. Empty for an image that does not exist or was
# never stamped. A release job's image carries its build version
# (v2.2.0-rc.1.<n>+<sha7>), which is never the bare vX.Y.Z a promotion
# compares it with, so a promotion still rebuilds it.
published_release() {
  local ref="$1" label=""
  if docker buildx version >/dev/null 2>&1; then
    label="$(docker buildx imagetools inspect "$ref" \
      --format '{{ index .Image.Config.Labels "org.opencontainers.image.version" }}' 2>/dev/null || true)"
  fi
  if [ -z "$label" ] && docker pull -q "$ref" >/dev/null 2>&1; then
    label="$(docker image inspect \
      --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$ref" 2>/dev/null || true)"
  fi
  [ "$label" != "<no value>" ] || label=""
  printf '%s' "$label"
}

# Whether the published image $1 is a stamped build of release $2 from
# $promote_commit: its version label says $2 and its revision label says
# the commit. The commit matters for a `-pending` tag, which a rejected
# release can leave behind on another commit with the same version.
stamped_build() {
  local ref="$1" want="$2" revision=""
  [ "$(published_release "$ref")" = "$want" ] || return 1
  if docker buildx version >/dev/null 2>&1; then
    revision="$(docker buildx imagetools inspect "$ref" \
      --format '{{ index .Image.Config.Labels "org.opencontainers.image.revision" }}' 2>/dev/null || true)"
  fi
  if [ -z "$revision" ] || [ "$revision" = "<no value>" ]; then
    revision="$(docker image inspect \
      --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$ref" 2>/dev/null || true)"
  fi
  [ "$revision" = "$promote_commit" ]
}

# Whether $1 reports release $2 the way a deployment reads it: `grain
# version --json`, out of the binary the image runs.
check_release_stamp() {
  local image="$1" want="$2" answered
  answered="$(docker run --rm "$image" version --json | tr -d ' \n')"
  if [[ "$answered" != *"\"release\":\"$want\""* ]]; then
    echo "release-images.sh: $image reports ${answered:-nothing} from \`grain version --json\`, not release $want" >&2
    return 1
  fi
}

# Rebuild the daemon image $1 (a release job's sha- tag) as $2 from
# $promote_commit with RELEASE=$promote_tag and main package $3, verify
# it, and publish and sign it. An empty $3 is a release whose Dockerfile
# takes no MAIN_GRAIN. Built from a clean checkout of the release tag
# rather than from this job's own tree, which is main or candidate's head:
# the Dockerfile, the Makefile and the source are the release's own.
rebuild_release_grain() {
  local source="$1" target="$2" main="${3:-}" question want answered work go_version
  local guest_ref k8s_ref image_ref
  docker pull "$source"
  # What the candidate's build stamped, so the rebuild stamps the same.
  guest_ref="$(docker run --rm "$source" sandbox-image)"
  k8s_ref="$(docker run --rm "$source" k8s-sandbox-image)"
  image_ref="$(docker run --rm "$source" image)"
  if [ -z "$guest_ref" ] || [ -z "$k8s_ref" ] || [ -z "$image_ref" ]; then
    echo "release-images.sh: $source did not say which sandbox images and image reference it was built with (sandbox-image: ${guest_ref:-nothing}, k8s-sandbox-image: ${k8s_ref:-nothing}, image: ${image_ref:-nothing}), so there is nothing to rebuild it with" >&2
    return 1
  fi

  work="$(mktemp -d)"
  if ! fetch_promote_tag_into "$work"; then
    rm -rf "$work"
    echo "release-images.sh: could not fetch $promote_tag from $remote to rebuild $target" >&2
    return 1
  fi
  git -C "$work" -c advice.detachedHead=false checkout -q --detach FETCH_HEAD
  if [ -f "$work/.git/objects/info/alternates" ]; then
    git -C "$work" repack -a -d -q
    rm -f "$work/.git/objects/info/alternates"
  fi
  if [ "$(git -C "$work" rev-parse HEAD)" != "$promote_commit" ]; then
    echo "release-images.sh: $promote_tag checked out as $(git -C "$work" rev-parse HEAD), not $promote_commit" >&2
    return 1
  fi

  # A release cut before the Dockerfile took a RELEASE (v1.8.0 and
  # earlier) would drop the build argument on the floor and stamp
  # nothing, so its copy here gets the two words it is missing. Nothing
  # else about the build changes, and nothing is committed anywhere.
  if ! grep -q '^ARG RELEASE=' "$work/Dockerfile"; then
    if ! grep -q '^RUN make build ' "$work/Dockerfile"; then
      echo "release-images.sh: $promote_tag's Dockerfile has neither ARG RELEASE nor a \`RUN make build\` line to add one to, so it cannot be stamped" >&2
      return 1
    fi
    # shellcheck disable=SC2016 # ${RELEASE} is for the Dockerfile, not this shell
    sed -i 's/^\(RUN make build .*\)$/ARG RELEASE=\n\1 RELEASE=${RELEASE}/' "$work/Dockerfile"
    # The checkout's .git goes into the build with it, and `go build`
    # stamps vcs.modified from `git status`: left as an edit, the patch
    # makes the release report itself as `<short>-dirty`. Hidden from
    # git, it is what it is -- a build instruction, not the release's
    # source.
    git -C "$work" update-index --skip-worktree Dockerfile
    echo "release-images.sh: $promote_tag's Dockerfile predates ARG RELEASE; added it to the copy built here"
  fi
  if [ -f /usr/local/share/ca-certificates/grain-ca.crt ]; then
    cp /usr/local/share/ca-certificates/grain-ca.crt "$work/.grain-ca.crt"
  fi

  # The arguments `make image` passes, given directly: a release's own
  # Makefile may not pass RELEASE, and the label has to be there whatever
  # the release's Dockerfile says.
  # go.mod's toolchain line when it has one, its `go` directive when it
  # does not -- the Makefile's GO_VERSION rule, which says why.
  go_version="$(sed -n 's/^toolchain[[:space:]]\{1,\}go//p' "$work/go.mod")"
  [ -n "$go_version" ] || go_version="$(sed -n 's/^go[[:space:]]\{1,\}//p' "$work/go.mod")"
  local source_label=()
  if [ "$registry" = "ghcr.io" ]; then
    source_label=(--label org.opencontainers.image.source="https://github.com/${RELEASE_IMAGE_REPO,,}")
  fi
  (
    cd "$work"
    docker build \
      --build-arg GO_IMAGE="${GO_IMAGE:-golang}" \
      ${go_version:+--build-arg GO_VERSION="$go_version"} \
      --build-arg BUILDVCS=auto \
      ${main:+--build-arg MAIN_GRAIN="$main"} \
      --build-arg SANDBOX_IMAGE="$guest_ref" \
      --build-arg K8S_SANDBOX_IMAGE="$k8s_ref" \
      --build-arg GRAIN_IMAGE_REF="$image_ref" \
      --build-arg GIT_REVISION="$promote_commit" \
      "${source_label[@]}" \
      --build-arg RELEASE="${stamp_tag:-$promote_tag}" \
      --label org.opencontainers.image.version="${stamp_tag:-$promote_tag}" \
      -t "$target" -f Dockerfile .
  )
  rm -rf "$work"

  for question in "sandbox-image $guest_ref" "k8s-sandbox-image $k8s_ref" "image $image_ref"; do
    read -r question want <<< "$question"
    answered="$(docker run --rm "$target" "$question")"
    if [ "$answered" != "$want" ]; then
      echo "release-images.sh: the rebuilt $target answers \`$question\` with ${answered:-nothing}, not the $want $source was built with" >&2
      return 1
    fi
  done
  check_release_stamp "$target" "${stamp_tag:-$promote_tag}"
  if [ -n "$main" ]; then
    check_edition "$target" "$main"
  fi
  # Built from a clean checkout of the tag, so a rebuild that says its
  # tree was modified is one whose checkout this script dirtied.
  answered="$(docker run --rm "$target" version --json | tr -d ' \n')"
  if [[ "$answered" == *'"modified":true'* ]]; then
    echo "release-images.sh: the rebuilt $target reports a modified tree ($answered), although it was built from a clean checkout of $promote_tag" >&2
    return 1
  fi

  push_image "$target"
  sign_image "$target"
  echo "release-images.sh: published $target, rebuilt from $promote_commit with RELEASE=${stamp_tag:-$promote_tag}"
}

# Whether a tag exists in the registry, asked of the registry rather than
# by pulling it.
image_exists() {
  if docker buildx version >/dev/null 2>&1; then
    docker buildx imagetools inspect "$1" >/dev/null
  else
    docker manifest inspect "$1" >/dev/null
  fi
}

# Point $2 at whatever $1 is: one manifest copied registry-side by
# imagetools where buildx is here, a pull, tag and push where it is not.
retag_image() {
  local attempt
  if docker buildx version >/dev/null 2>&1; then
    for attempt in 1 2 3; do
      if docker buildx imagetools create --tag "$2" "$1"; then
        return 0
      fi
      if [ "$attempt" -lt 3 ]; then
        sleep $((20 * attempt))
      fi
    done
    echo "could not retag $1 as $2 in three attempts" >&2
    return 1
  fi
  docker pull "$1"
  docker tag "$1" "$2"
  push_image "$2"
}

# A `push` of its own publishes what an earlier `build` in this same
# sandbox left behind, named by the file that build wrote rather than by
# re-deriving the tags: the images that were tested are the images that
# get published, and a push with no build in front of it says so instead
# of pushing whatever happens to carry the right name.
if [ "$phase" = push ]; then
  if [ ! -r "$state" ]; then
    echo "release-images.sh: $state does not exist: \`push\` publishes what \`build\` built, in the same job" >&2
    exit 1
  fi
  # shellcheck source=/dev/null
  . "$state"
  if [ "${RELEASE_IMAGE_COMMIT:-}" != "$commit" ]; then
    echo "release-images.sh: $state was written for ${RELEASE_IMAGE_COMMIT:-no commit}, not the $commit this release is for" >&2
    exit 1
  fi
  guest_base="${RELEASE_IMAGE_GUEST_BASE:-$guest_base}"
  guest="$RELEASE_IMAGE_GUEST"
  k8s_sandbox_base="${RELEASE_IMAGE_K8S_SANDBOX_BASE:-$k8s_sandbox_base}"
  k8s_sandbox="$RELEASE_IMAGE_K8S_SANDBOX"
  grain="$RELEASE_IMAGE_GRAIN"
  guest_base_pinned="${RELEASE_IMAGE_GUEST_BASE_PINNED:-}"
  guest_pinned="${RELEASE_IMAGE_GUEST_PINNED:-}"
  k8s_sandbox_base_pinned="${RELEASE_IMAGE_K8S_SANDBOX_BASE_PINNED:-}"
  k8s_sandbox_pinned="${RELEASE_IMAGE_K8S_SANDBOX_PINNED:-}"
  if [[ "$guest_pinned" != *@sha256:* || "$k8s_sandbox_pinned" != *@sha256:* ]]; then
    echo "release-images.sh: $state names no pinned sandbox images, so there is nothing to sign them by -- it was written by a build older than the one publishing it; run \`build\` again" >&2
    exit 1
  fi
fi

# A release that must be signed and has no key to sign it with stops
# here, before the build, rather than at the first signature an hour in.
if [ "$require_signing" = true ] && [ ! -r "$signing_key" ] &&
    { [ "$phase" = build ] || [ "$phase" = push ] || [ "$phase" = all ] || [ "$phase" = promote ] || [ "$phase" = rc ] || [ "$phase" = pending ]; }; then
  echo "release-images.sh: REQUIRE_IMAGE_SIGNING is set and there is no signing key at $signing_key -- grant this job the image-signing CI capability" >&2
  exit 1
fi

case "$phase" in
  build) build_images ;;
  # The guest and nothing else: no Kubernetes image, no grain image, no
  # tag that names this commit or this branch, and no $state file, since
  # there is no push phase after it to read one. What it leaves behind is
  # a `guest:inputs-<key>` in the registry -- which is exactly what the
  # jobs that resolve a guest look for, and is all any of them wants.
  guest-cache) resolve_guest ;;
  push) publish_images ;;
  promote) promote_images ;;
  rc) rc_images ;;
  pending) pending_images ;;
  discard-pending) discard_pending ;;
  discard-rc) discard_rc ;;
  all)
    build_images
    publish_images
    ;;
esac
