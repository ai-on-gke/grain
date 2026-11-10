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

# Publish Grain's release images as assets on this repository's own GitHub
# releases -- release-asset contract v2, docs/release-assets.md -- rather
# than as registry packages. It needs nothing but the repository's own
# GITHUB_TOKEN with `contents: write`.
#
# Only immutable sha-<short> and vX.Y.Z releases carry images; a vX.Y.Z
# holds its own copies of its commit's sha- tarballs, so pruning sha-
# releases can never break it. Every other release -- channel-<branch>,
# the release train's -rc.N / -pending / -discarded / -rejected /
# -retracted -- holds images.json alone (schema 2), whose images each name
# the sha- release holding their asset. There is no release named
# `latest`: promote marks vX.Y.Z GitHub's latest release and deletes a
# legacy release of that name.
#
#   package
#           `docker save | gzip` the grain, guest and k8s-sandbox images
#           `release-images.sh build` (RELEASE_IMAGES_TARGET=assets) left
#           named in $RELEASE_IMAGES_STATE into $RELEASE_ASSETS_DIR, each
#           with a `<asset>.sha256` sidecar, and write images.json beside
#           them. An image at or over GitHub's 2 GiB per-asset limit fails.
#   validate [DIR]
#           check DIR's images.json against the contract, and every asset
#           and sidecar it names against it.
#   publish <release>...
#           publish DIR's images.json (and, on a sha- release, its assets)
#           to each release, creating it (and its tag, at the manifest's
#           commit) if it does not exist. See release_kind.
#   promote [vX.Y.Z]
#           publish vX.Y.Z -- the release the git tag `latest` names, with
#           no argument -- with copies of its commit's sha-<short> assets,
#           marked GitHub's latest release when the git tag latest agrees.
#   retag <from-sha-release> <version> <release>...
#           the same for any other name: the train's vX.Y.0-rc.N /
#           channel-candidate, vX.Y.0-pending and the markers.
#   rc [vX.Y.Z-rc.N] | pending <vX.Y.Z> | discard-rc <vX.Y.Z-rc.N> |
#   discard-pending <vX.Y.Z> | retract <vX.Y.Z>
#           release-images.sh's release-train phases, so that
#           RELEASE_IMAGES="bash scripts/release-assets.sh" publishes the
#           train's names: an rc as vX.Y.Z-rc.N and, when it is the newest
#           rc, channel-candidate; a pending release as vX.Y.Z-pending
#           (version vX.Y.Z); and the vX.Y.Z-rc.N-discarded,
#           vX.Y.Z-rejected and vX.Y.Z-retracted markers.
#   train-tag <tag>
#           the phase a pushed train tag asks for (release-train-assets.yml)
#   prune   delete sha- releases past the newest $RELEASE_RETAIN_SHA
#           (default 50) that no channel, train release or GitHub's latest
#           release points at
#   mirror <release> <registry/prefix> [image...]
#           for a cluster, which cannot `docker load`: fetch and load the
#           release, then tag each image (all three by default) as
#           <registry/prefix>/<name>:<tag> -- grain/guest:sha-x becomes
#           <prefix>/guest:sha-x -- push it, and print `<image>=<pushed
#           ref>` per image.
#   fetch <release> [DIR]
#           download a release's images.json and the assets it names into
#           DIR and validate them; `latest` is GitHub's latest release. A
#           channel with no images.json is mid-update: exit 75
#           (EX_TEMPFAIL), try again later.
#   load [DIR]
#           validate DIR, then `docker load` every image and check the
#           loaded image IDs and refs are the ones the manifest names.
#   promote-version [vX.Y.Z]
#           print the release promote would publish: vX.Y.Z, or the one
#           the git tag latest names.
#   cli-validate [DIR]
#           check DIR ($RELEASE_CLI_DIR, default /tmp/release-cli) holds
#           the grain CLI archives scripts/build-cli.sh writes --
#           grain-<linux|darwin>-<amd64|arm64>.tar.gz -- and a checksums.txt
#           in sha256sum's format naming exactly those four, which match.
#   cli-publish <release> [DIR]
#           upload DIR's four archives and then checksums.txt to the
#           vX.Y.Z or vX.Y.Z-rc.N release, which promote or train-tag must
#           already have published: the files GET /cli/install.sh fetches.
#           They are never replaced: a release that already carries
#           checksums.txt is left as it is. A release that does not
#           exist fails it, or with RELEASE_CLI_SKIP_UNRELEASED=true is
#           warned of and skipped.
#   agentd-validate [DIR] [--require-signature]
#           check DIR ($RELEASE_AGENTD_DIR, default /tmp/release-agentd)
#           holds static grain-agentd-linux-{amd64,arm64} binaries and
#           matching .manifest.json files, and verify any signature (and
#           rotation chain) against the pinned release public key.
#   agentd-sign [DIR]
#           sign DIR's grain-agentd manifests with $AGENTD_RELEASE_SIGNING_KEY
#           (OpenSSL Ed25519) and optional $AGENTD_RELEASE_ROTATIONS, then
#           validate them with --require-signature. Warns when no signing key
#           is configured unless REQUIRE_AGENTD_SIGNING=true.
#   agentd-publish <release> [DIR]
#           upload DIR's grain-agentd binaries and manifests to <release>
#           (vX.Y.Z or vX.Y.Z-rc.N), manifests last. A release whose
#           manifests are already signed is never overwritten.
#
# --dry-run (or DRY_RUN=true) does everything that reads and prints every
# write it would make instead of making it.
set -euo pipefail

dry_run="${DRY_RUN:-false}"
if [ "${1:-}" = --dry-run ]; then
  dry_run=true
  shift
fi
cmd="${1:-}"
[ $# -gt 0 ] && shift

repo="${RELEASE_ASSETS_REPO:-${GITHUB_REPOSITORY:-ai-on-gke/grain}}"
repo="${repo,,}"
api_url="${GITHUB_API_URL:-https://api.github.com}"
server_url="${GITHUB_SERVER_URL:-https://github.com}"
token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
dir="${RELEASE_ASSETS_DIR:-/tmp/release-assets}"
state="${RELEASE_IMAGES_STATE:-/tmp/release-images.env}"
remote="${RELEASE_IMAGES_REMOTE:-origin}"
# GitHub refuses a release asset of 2 GiB or more.
max_asset_bytes="${RELEASE_ASSETS_MAX_BYTES:-2147483648}"

# The manifest's keys and the asset each is published as. The loader reads
# the names out of images.json; these are only what this edition picks.
image_keys=(grain guest k8s-sandbox)
declare -A asset_of=(
  [grain]=grain-image.tar.gz
  [guest]=guest-image.tar.gz
  [k8s-sandbox]=k8s-sandbox-image.tar.gz
)

say() { echo "release-assets.sh: $*"; }
die() {
  echo "release-assets.sh: $*" >&2
  exit 1
}

need() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "needs $c on PATH"
  done
}

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

# --- the manifest ---------------------------------------------------------

# validate_manifest DIR: images.json in DIR is contract v1, and every
# asset it names that is present in DIR matches its sha256 and sidecar.
# With require_assets=true a missing asset is a failure too.
validate_manifest() {
  local d="$1" require_assets="${2:-true}" manifest="$1/images.json" problems key asset sum side
  need jq
  [ -s "$manifest" ] || die "$manifest is missing or empty"
  problems="$(jq -r '
    def hex($n): type == "string" and test("^[0-9a-f]{" + ($n|tostring) + "}$");
    . as $m |
    [
      (if .schema != 1 and .schema != 2 then "schema is \(.schema|tojson), not 1 or 2" else empty end),
      (if (.repository|type) != "string" or (.repository|test("^github\\.com/[a-z0-9._-]+/[a-z0-9._-]+$")|not)
        then "repository \(.repository|tojson) is not github.com/<owner>/<name>" else empty end),
      (if (.commit|hex(40)|not) then "commit \(.commit|tojson) is not a 40-hex commit" else empty end),
      (if (.version|type) != "string" or .version == "" then "version \(.version|tojson) is not a non-empty string" else empty end),
      (if (.createdAt|type) != "string" or (.createdAt|test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$")|not)
        then "createdAt \(.createdAt|tojson) is not an RFC 3339 UTC time" else empty end),
      (if (.images|type) != "object" then "images is not an object"
       elif (.images|keys) != ["grain","guest","k8s-sandbox"] then "images has keys \(.images|keys|tojson), not grain, guest and k8s-sandbox"
       else
         (.images|to_entries[]|.key as $k|.value|
           (if (.asset|type) != "string" or (.asset|test("^[A-Za-z0-9._-]+\\.tar\\.gz$")|not) then "\($k).asset \(.asset|tojson) is not a <name>.tar.gz" else empty end),
           (if (.sha256|hex(64)|not) then "\($k).sha256 \(.sha256|tojson) is not 64 hex" else empty end),
           (if (.imageId|type) != "string" or (.imageId|test("^sha256:[0-9a-f]{64}$")|not) then "\($k).imageId \(.imageId|tojson) is not sha256:<64 hex>" else empty end),
           (if $m.schema == 2 and ((.release|type) != "string" or (.release|test("^(sha-[0-9a-f]{12}|v[0-9]+\\.[0-9]+\\.[0-9]+)$")|not))
             then "\($k).release \(.release|tojson) is not the sha-<12 hex> or vX.Y.Z release that holds the asset" else empty end),
           (if $m.schema == 1 and has("release") then "\($k) has a release, which schema 1 does not" else empty end),
           (if (.ref|type) != "string" or (.ref|test("^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$")|not)
             then "\($k).ref \(.ref|tojson) is not a local repo:tag (no digest)"
            elif (.ref|split("/")[0]|test("[.:]")) or (.ref|split("/")|length) < 2 then "\($k).ref \(.ref|tojson) names a registry host or none at all; a manifest ref is local, <edition>/<image>:<tag>"
            else empty end))
       end),
      (if (.images|type) == "object" and ([.images[].asset]|unique|length) != ([.images[].asset]|length) then "two images share an asset name" else empty end),
      (if (.images|type) == "object" and (.commit|hex(40)) and ([.images[].ref|type] == ["string","string","string"])
          and ([.images[].ref|endswith(":sha-" + $m.commit[0:12])]|all|not)
        then "not every ref is tagged sha-\(.commit[0:12]), the commit the manifest names" else empty end)
    ] | .[]' "$manifest" 2>&1)" || die "$manifest is not JSON: $problems"
  if [ -n "$problems" ]; then
    printf 'release-assets.sh: %s does not meet the release-asset contract (schema 1 or 2):\n' "$manifest" >&2
    printf '  %s\n' "$problems" >&2
    exit 1
  fi
  for key in "${image_keys[@]}"; do
    asset="$(jq -r --arg k "$key" '.images[$k].asset' "$manifest")"
    sum="$(jq -r --arg k "$key" '.images[$k].sha256' "$manifest")"
    if [ ! -f "$d/$asset" ]; then
      [ "$require_assets" = true ] && die "$d has no $asset, which images.json names for $key"
      continue
    fi
    [ -f "$d/$asset.sha256" ] || die "$d has $asset but no $asset.sha256"
    side="$(cat "$d/$asset.sha256")"
    [ "$side" = "$sum  $asset" ] || die "$asset.sha256 reads '$side', not the '$sum  $asset' sha256sum writes for the manifest's sha256"
    (cd "$d" && sha256sum --quiet -c "$asset.sha256") >&2 || die "$asset does not match its sha256 $sum"
  done
}

# rewrite_manifest SRC DST VERSION: SRC's images.json as a promoted or
# retagged release's, with only version and createdAt changed.
rewrite_manifest() {
  jq --arg v "$3" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '.version = $v | .createdAt = $t' "$1/images.json" > "$2/images.json.tmp"
  mv "$2/images.json.tmp" "$2/images.json"
}

package() {
  need docker jq gzip sha256sum
  [ -f "$state" ] || die "$state does not exist: run RELEASE_IMAGES_TARGET=assets scripts/release-images.sh build first"
  # shellcheck source=/dev/null
  . "$state"
  [ "${RELEASE_IMAGE_TARGET:-}" = assets ] ||
    die "$state was built for ${RELEASE_IMAGE_TARGET:-a registry}, not release assets: its images are pinned to registry digests (RELEASE_IMAGES_TARGET=assets)"
  local commit="$RELEASE_IMAGE_COMMIT" version="${RELEASE_IMAGE_VERSION:-}" key ref asset size id sum json
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "$state names no commit"
  [ -n "$version" ] || version="sha-${commit:0:12}"
  declare -A ref_of=(
    [grain]="$RELEASE_IMAGE_GRAIN"
    [guest]="$RELEASE_IMAGE_GUEST"
    [k8s-sandbox]="$RELEASE_IMAGE_K8S_SANDBOX"
  )
  mkdir -p "$dir"
  rm -f "$dir"/images.json "$dir"/*.tar.gz "$dir"/*.tar.gz.sha256
  # Schema 2: every image names the release that holds its asset -- this
  # commit's sha- release, which is also the one these assets go on.
  json="$(jq -n --arg r "github.com/$repo" --arg c "$commit" --arg v "$version" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{schema: 2, repository: $r, commit: $c, version: $v, createdAt: $t, images: {}}')"
  for key in "${image_keys[@]}"; do
    ref="${ref_of[$key]}"
    asset="${asset_of[$key]}"
    id="$(docker image inspect --format '{{.Id}}' "$ref")" || die "$ref, the $key image $state names, is not in the local image store"
    say "saving $ref as $asset"
    docker save "$ref" | gzip -n > "$dir/$asset"
    size="$(stat -c %s "$dir/$asset")"
    if [ "$size" -ge "$max_asset_bytes" ]; then
      die "$asset is $size bytes, at or over GitHub's per-asset limit of $max_asset_bytes (2 GiB): $ref is too big to publish as a release asset"
    fi
    (cd "$dir" && sha256sum "$asset" > "$asset.sha256")
    sum="$(cut -d' ' -f1 "$dir/$asset.sha256")"
    json="$(jq --arg k "$key" --arg a "$asset" --arg s "$sum" --arg i "$id" --arg ref "$ref" --arg rel "sha-${commit:0:12}" \
      '.images[$k] = {asset: $a, sha256: $s, imageId: $i, ref: $ref, release: $rel}' <<< "$json")"
    say "$asset: $size bytes, sha256 $sum, image $id"
  done
  printf '%s\n' "$json" > "$dir/images.json"
  validate_manifest "$dir"
  say "packaged $commit ($version) into $dir"
}

# --- the GitHub API ----------------------------------------------------------

# api METHOD URL-or-path [--data JSON | --upload FILE TYPE]: the HTTP
# status on stdout, the body in $scratch/body.
api() {
  local method="$1" url="$2" args=()
  shift 2
  case "$url" in https://* | http://*) ;; *) url="$api_url$url" ;; esac
  args=(-sS -o "$scratch/body" -w '%{http_code}' -X "$method"
    -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  case "${1:-}" in
    --data) args+=(-H 'Content-Type: application/json' --data-binary "$2") ;;
    # -T streams the file rather than reading 2 GiB into memory.
    --upload) args+=(-H "Content-Type: $3" -T "$2") ;;
  esac
  local attempt status
  for attempt in 1 2 3; do
    status="$(curl "${args[@]}" "$url" || echo 000)"
    case "$status" in
      000 | 5??) sleep $((5 * attempt)) ;;
      *) break ;;
    esac
  done
  printf '%s' "$status"
}

# write METHOD URL ...: api, unless this is a dry run, where it says what
# it would have done and answers 200 with an empty body.
write() {
  if [ "$dry_run" = true ]; then
    echo "release-assets.sh: [dry-run] would $1 ${2%%\?*}${3:+ $3}${4:+ ${4##*/}}" >&2
    printf '{}' > "$scratch/body"
    printf 200
    return 0
  fi
  api "$@"
}

ok() { [[ "$1" =~ ^2[0-9][0-9]$ ]]; }

# release_json TAG: the release's JSON in $scratch/release, or return 1 if
# there is none.
release_json() {
  local status
  if [ -z "$token" ] && [ "$dry_run" = true ]; then
    return 1
  fi
  status="$(api GET "/repos/$repo/releases/tags/$1")"
  case "$status" in
    200) cp "$scratch/body" "$scratch/release" ;;
    404) return 1 ;;
    *) die "GET release $1 answered $status: $(head -c 300 "$scratch/body")" ;;
  esac
}

# asset_list RELEASE-ID: "<id> <name>" per asset of the release.
asset_list() {
  local page=1 status n
  : > "$scratch/assets"
  while :; do
    status="$(api GET "/repos/$repo/releases/$1/assets?per_page=100&page=$page")"
    ok "$status" || die "listing assets of release $1 answered $status"
    jq -r '.[] | "\(.id) \(.name)"' "$scratch/body" >> "$scratch/assets"
    n="$(jq length "$scratch/body")"
    [ "$n" -lt 100 ] && break
    page=$((page + 1))
  done
  cat "$scratch/assets"
}

# Which releases are which (release-asset contract v2):
#   holder   sha-<short> and vX.Y.Z: the only releases that carry
#            tarballs, a vX.Y.Z its own copies of its commit's sha-
#            assets. Immutable: an asset is only ever added, never
#            replaced.
#   pointer  every vX.Y.Z-<suffix> (rc.N, pending, rc.N-discarded,
#            rejected, retracted): images.json alone, naming the sha-
#            release that holds each asset. Immutable.
#   channel  channel-<branch>: images.json alone, moved forward only, onto
#            a commit its current one is an ancestor of (the rule
#            release-images.sh's tag_may_move applies to registry branch
#            tags).
# There is no release named `latest`: the latest channel is GitHub's own
# latest-release marker, which promote sets on vX.Y.Z (make_latest) when
# the git tag latest names its commit.
release_kind() {
  case "$1" in
    sha-*) echo holder ;;
    channel-*) echo channel ;;
    v[0-9]*) if [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then echo holder; else echo pointer; fi ;;
    latest) die "there is no release named latest: the latest channel is GitHub's latest-release marker on a vX.Y.Z (contract v2)" ;;
    *) die "$1 is not a release name the contract publishes (sha-<short>, channel-<branch>, vX.Y.Z[-...]); never a bare branch name" ;;
  esac
}

remote_commit_of_tag() {
  git ls-remote --tags "$remote" "refs/tags/$1" "refs/tags/$1^{}" 2>/dev/null |
    awk '{ c[$2] = $1 } END { t = "refs/tags/'"$1"'"; if ((t "^{}") in c) print c[t "^{}"]; else if (t in c) print c[t] }'
}

# may_move RELEASE COMMIT HELD: 0 if a channel holding HELD may move onto
# COMMIT, saying why either way.
may_move() {
  local release="$1" commit="$2" held="$3"
  if [ -z "$held" ]; then
    say "$release: holds no manifest yet; publishing ${commit:0:12}"
    return 0
  fi
  if [ "${force_move:-false}" = true ]; then
    say "$release: moving onto ${commit:0:12}, the newest rc, from ${held:0:12}"
    return 0
  fi
  if [ "$held" = "$commit" ]; then
    say "$release: already holds ${commit:0:12}; republishing it"
    return 0
  fi
  git cat-file -e "$held^{commit}" 2>/dev/null || git fetch -q "$remote" "$held" 2>/dev/null || true
  if git merge-base --is-ancestor "$held" "$commit" 2>/dev/null; then
    say "$release: ${held:0:12} is an ancestor of ${commit:0:12}; moving forward"
    return 0
  fi
  say "$release: holds ${held:0:12}, which is not an ancestor of ${commit:0:12} (or cannot be told); leaving it where it is"
  return 1
}

content_type() {
  case "$1" in
    *.json) echo application/json ;;
    *.sha256 | *.txt) echo text/plain ;;
    *) echo application/gzip ;;
  esac
}

# holder_files: the tarballs and sidecars a holder carries, then images.json.
holder_files() {
  local key asset
  for key in "${image_keys[@]}"; do
    asset="$(jq -r --arg k "$key" '.images[$k].asset' "$dir/images.json")"
    printf '%s\n%s\n' "$asset" "$asset.sha256"
  done
  echo images.json
}

upload() {
  local id="$1" name="$2" status upload_url
  upload_url="${upload_base:-https://uploads.github.com/repos/$repo/releases/$id/assets}?name=$name"
  status="$(write POST "$upload_url" --upload "$dir/$name" "$(content_type "$name")")"
  ok "$status" || die "uploading $name to release $id answered $status: $(head -c 300 "$scratch/body")"
  say "uploaded $name"
}

delete_asset() {
  local status
  status="$(write DELETE "/repos/$repo/releases/assets/$1")"
  ok "$status" || [ "$status" = 404 ] || die "deleting asset $1 answered $status"
}

# point_tag TAG COMMIT: create or force-move a lightweight tag.
point_tag() {
  local status
  status="$(write PATCH "/repos/$repo/git/refs/tags/$1" --data "$(jq -nc --arg s "$2" '{sha: $s, force: true}')")"
  if [ "$status" = 422 ] || [ "$status" = 404 ]; then
    status="$(write POST "/repos/$repo/git/refs" --data "$(jq -nc --arg r "refs/tags/$1" --arg s "$2" '{ref: $r, sha: $s}')")"
  fi
  ok "$status" || die "pointing tag $1 at $2 answered $status: $(head -c 300 "$scratch/body")"
}

# get_asset ASSET-ID OUT: one release asset's bytes, through the API, with
# the same retries as api; 1 if it could not be read.
get_asset() {
  local attempt status
  for attempt in 1 2 3; do
    status="$(curl -sSL -o "$2" -w '%{http_code}' ${token:+-H "Authorization: Bearer $token"} \
      -H 'Accept: application/octet-stream' "$api_url/repos/$repo/releases/assets/$1" || echo 000)"
    case "$status" in
      000 | 5??) sleep $((5 * attempt)) ;;
      *) break ;;
    esac
  done
  ok "$status"
}

held_version() {
  jq -r '.version // empty' "$scratch/held.json" 2>/dev/null || true
}

# check_holders RELEASE: every release the manifest's images name, other
# than RELEASE itself, exists and carries the image's asset and sidecar --
# so a pointer is never published to assets that are not there.
check_holders() {
  local self="$1" holder key asset
  while read -r holder; do
    [ "$holder" = "$self" ] && continue
    if [ -z "$token" ] && [ "$dry_run" = true ]; then
      say "[dry-run] would check that $holder holds the assets $self points at"
      continue
    fi
    release_json "$holder" || die "$self would point at $holder, which does not exist"
    asset_list "$(jq -r .id "$scratch/release")" | awk '{print $2}' > "$scratch/holder-assets"
    for key in "${image_keys[@]}"; do
      [ "$(jq -r --arg k "$key" '.images[$k].release' "$dir/images.json")" = "$holder" ] || continue
      asset="$(jq -r --arg k "$key" '.images[$k].asset' "$dir/images.json")"
      if ! grep -qx "$asset" "$scratch/holder-assets" || ! grep -qx "$asset.sha256" "$scratch/holder-assets"; then
        die "$self would point at $holder for $asset, which $holder does not carry"
      fi
    done
  done < <(jq -r '[.images[].release // empty] | unique | .[]' "$dir/images.json")
}

# swap_manifest ID: replace a channel's images.json. Asset names are
# unique on a release, so the new manifest goes up as images.json.next,
# the old images.json is deleted, and .next is renamed into its place. A
# loader that finds no images.json (or only .next) in between treats the
# channel as mid-update and retries on its next poll; it never falls back.
swap_manifest() {
  local id="$1" old next
  # Left behind by a publisher that died mid-move (or by the older
  # rename-aside scheme): never the manifest a loader should read.
  while read -r old; do
    delete_asset "$old"
  done < <(awk '$2 == "images.json.next" || $2 == "images.json.prev" {print $1}' "$scratch/existing")
  cp "$dir/images.json" "$dir/images.json.next"
  upload "$id" images.json.next
  next="$(jq -r '.id // empty' "$scratch/body")"
  while read -r old; do
    delete_asset "$old"
  done < <(awk '$2 == "images.json" || $2 ~ /\.tar\.gz(\.sha256)?$/ {print $1}' "$scratch/existing")
  if [ "$dry_run" = true ]; then
    echo "release-assets.sh: [dry-run] would rename images.json.next to images.json" >&2
  else
    [ -n "$next" ] || die "uploading images.json.next to release $id returned no asset id"
    status="$(write PATCH "/repos/$repo/releases/assets/$next" --data '{"name":"images.json"}')"
    ok "$status" || die "renaming images.json.next into place on release $id answered $status"
  fi
  rm -f "$dir/images.json.next"
}

publish_one() {
  local release="$1" kind commit version id="" held="" held_asset status name prerelease=true make_latest=false listed missing
  upload_base=""
  rm -f "$scratch/held.json"
  kind="$(release_kind "$release")"
  commit="$(jq -r .commit "$dir/images.json")"
  version="$(jq -r .version "$dir/images.json")"
  if [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    prerelease=false
    # GitHub's latest-release marker is the latest channel; it follows the
    # git tag latest, which is how a rollback moves it back.
    [ "$(remote_commit_of_tag latest)" = "$commit" ] && make_latest=true
  fi
  if [ "$kind" = holder ]; then
    if [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      [ "$version" = "$release" ] || die "$release cannot hold a manifest whose version is $version"
    else
      [ "$release" = "sha-${commit:0:12}" ] || die "$release cannot hold ${commit:0:12}'s assets"
    fi
    [ "$(jq -r --arg r "$release" '[.images[].release] | all(. == $r)' "$dir/images.json")" = true ] ||
      die "a holder's images.json must name itself as every image's release"
  else
    check_holders "$release"
  fi

  if release_json "$release"; then
    id="$(jq -r .id "$scratch/release")"
    upload_base="$(jq -r '.upload_url | sub("\\{.*$"; "")' "$scratch/release")"
    asset_list "$id" > "$scratch/existing"
    # The manifest the release holds -- images.json, or the images.json.next
    # a publisher that died mid-swap left in its place. One that is there
    # but cannot be read is not "no manifest": taking it for one would let
    # may_move move a channel onto a commit its current one is not an
    # ancestor of, or an immutable release be published over. Fail closed.
    held_asset="$(awk '$2 == "images.json" {print $1; exit}' "$scratch/existing")"
    [ -n "$held_asset" ] || held_asset="$(awk '$2 == "images.json.next" {print $1; exit}' "$scratch/existing")"
    if [ -n "$held_asset" ]; then
      get_asset "$held_asset" "$scratch/held.json" ||
        die "could not read $release's manifest (asset $held_asset); refusing to publish over it"
      held="$(jq -r '.commit // empty' "$scratch/held.json" 2>/dev/null || true)"
      [[ "$held" =~ ^[0-9a-f]{40}$ ]] || die "$release's manifest (asset $held_asset) names no commit; refusing to publish over it"
    fi
  else
    : > "$scratch/existing"
  fi

  case "$kind" in
    holder | pointer)
      if [ -n "$held" ] && [ "$held" != "$commit" ]; then
        die "$release is immutable and already holds ${held:0:12}, not ${commit:0:12}"
      fi
      ;;
    channel)
      may_move "$release" "$commit" "$held" || return 0
      if [ "$held" = "$commit" ] && [ "$version" = "$(held_version)" ] &&
          [ "$(grep -c ' images.json$' "$scratch/existing")" = 1 ] && ! grep -q ' images.json.next$' "$scratch/existing"; then
        say "$release already points at ${commit:0:12} ($version); nothing to move"
        return 0
      fi
      ;;
  esac

  if [ -z "$id" ]; then
    # A release on a tag that does not exist creates the tag at
    # target_commitish; one that does (vX.Y.Z, the train's) keeps it.
    status="$(write POST "/repos/$repo/releases" --data "$(jq -nc --arg t "$release" --arg c "$commit" --arg v "$version" \
      --argjson p "$prerelease" --arg l "$make_latest" \
      '{tag_name: $t, target_commitish: $c, name: $t, body: "Grain \($v) (\($c[0:12])) as release assets: docs/release-assets.md.", prerelease: $p, make_latest: $l}')")"
    if [ "$status" = 422 ] && release_json "$release"; then
      # Created by a concurrent publisher since it was looked up.
      cp "$scratch/release" "$scratch/body"
      asset_list "$(jq -r .id "$scratch/release")" > "$scratch/existing"
    else
      ok "$status" || die "creating release $release answered $status: $(head -c 300 "$scratch/body")"
    fi
    id="$(jq -r '.id // "dry-run"' "$scratch/body")"
    upload_base="$(jq -r '(.upload_url // "") | sub("\\{.*$"; "")' "$scratch/body")"
    if [ "$make_latest" = true ]; then
      say "created release $release, marked GitHub's latest release"
    else
      say "created release $release"
    fi
  elif [ "$make_latest" = true ]; then
    status="$(write PATCH "/repos/$repo/releases/$id" --data '{"make_latest":"true"}')"
    ok "$status" || die "marking $release latest answered $status"
    say "marked $release latest"
  fi
  [ "$kind" = channel ] && point_tag "$release" "$commit"

  case "$kind" in
    holder)
      while read -r name; do
        if grep -q " $name\$" "$scratch/existing"; then
          say "$release already has $name; an immutable release's assets are never replaced"
          continue
        fi
        upload "$id" "$name"
      done < <(holder_files)
      ;;
    pointer)
      grep -q ' images.json$' "$scratch/existing" || upload "$id" images.json
      ;;
    channel)
      swap_manifest "$id"
      ;;
  esac

  # Fail closed: a release missing any asset is not a release.
  if [ "$dry_run" != true ]; then
    listed="$(asset_list "$id" | awk '{print $2}')"
    missing=""
    if [ "$kind" = holder ]; then
      while read -r name; do
        grep -qx "$name" <<< "$listed" || missing="$missing $name"
      done < <(holder_files)
    fi
    [ "$(grep -cx images.json <<< "$listed")" = 1 ] || missing="$missing images.json"
    [ -z "$missing" ] || die "$release is missing$missing after publishing"
  fi
  say "published $release: ${commit:0:12} ($version)"
}

publish() {
  [ $# -gt 0 ] || die "publish needs at least one release name"
  need jq curl git
  [ -n "$token" ] || [ "$dry_run" = true ] || die "publishing needs GITHUB_TOKEN (permissions: contents: write)"
  local release
  for release in "$@"; do
    # A holder's assets must all be here; a pointer's live on its holders.
    if [ "$(release_kind "$release")" = holder ]; then
      validate_manifest "$dir"
    else
      validate_manifest "$dir" false
    fi
    publish_one "$release"
  done
}

# prune: delete sha- releases (and their tags) past the newest
# $RELEASE_RETAIN_SHA (default 50), except any release another one's
# images.json points at -- a channel, a train marker, GitHub's latest
# release. vX.Y.Z (which holds its own copies) and every other release
# are never pruned.
prune() {
  need jq curl
  [ -n "$token" ] || [ "$dry_run" = true ] || die "pruning needs GITHUB_TOKEN (permissions: contents: write)"
  local keep="${RELEASE_RETAIN_SHA:-${RELEASE_ASSETS_KEEP:-50}}" page=1 status n tag id i=0
  [[ "$keep" =~ ^[0-9]+$ ]] || die "RELEASE_RETAIN_SHA=$keep is not a number"
  if [ -z "$token" ]; then
    say "[dry-run] no GITHUB_TOKEN to list releases with; would keep the newest $keep sha- releases and every one pointed at"
    return 0
  fi
  : > "$scratch/releases"
  while :; do
    status="$(api GET "/repos/$repo/releases?per_page=100&page=$page")"
    ok "$status" || die "listing releases answered $status"
    jq -r '.[] | "\(.created_at) \(.id) \(.tag_name) \([.assets[]? | select(.name == "images.json" or .name == "images.json.next" or .name == "images.json.prev") | .id] | join(","))"' \
      "$scratch/body" >> "$scratch/releases"
    n="$(jq length "$scratch/body")"
    [ "$n" -lt 100 ] && break
    page=$((page + 1))
  done
  : > "$scratch/referenced"
  # Every manifest a release carries -- images.json, and images.json.next
  # (or a legacy .prev) mid-swap -- is read through the API, and one that
  # cannot be read stops the prune: a holder left off this list for a
  # transient error would be deleted while a channel or train release
  # still points at it. GitHub's latest release is one of the releases
  # listed, so it is read here too.
  local manifests asset
  while read -r _ id tag manifests; do
    case "$tag" in sha-*) continue ;; esac
    for asset in ${manifests//,/ }; do
      get_asset "$asset" "$scratch/ref.json" || die "could not read $tag's manifest (asset $asset); pruning nothing"
      jq -r '.images | to_entries | .[] | .value.release // empty' "$scratch/ref.json" >> "$scratch/referenced" ||
        die "$tag's manifest (asset $asset) is not one this can read; pruning nothing"
    done
  done < "$scratch/releases"
  while read -r _ id tag _; do
    i=$((i + 1))
    [ "$i" -le "$keep" ] && continue
    if grep -qx "$tag" "$scratch/referenced"; then
      say "keeping $tag: a channel or release points at it"
      continue
    fi
    status="$(write DELETE "/repos/$repo/releases/$id")"
    ok "$status" || die "deleting release $tag answered $status"
    status="$(write DELETE "/repos/$repo/git/refs/tags/$tag")"
    ok "$status" || [ "$status" = 422 ] || [ "$status" = 404 ] || die "deleting tag $tag answered $status"
    say "pruned $tag"
  done < <(awk '$3 ~ /^sha-[0-9a-f]{12}$/' "$scratch/releases" | sort -r)
}

# --- reading releases -------------------------------------------------------

# base_of RELEASE: where RELEASE's assets download from. `latest` is
# GitHub's latest-release marker, never a release of that name.
base_of() {
  if [ "$1" = latest ]; then
    printf '%s/%s/releases/latest/download' "$server_url" "$repo"
  else
    printf '%s/%s/releases/download/%s' "$server_url" "$repo" "$1"
  fi
}

# download BASE NAME OUT
download() {
  local status
  status="$(curl -sSL -o "$3" -w '%{http_code}' ${token:+-H "Authorization: Bearer $token"} "$1/$2" || echo 000)"
  ok "$status"
}

# fetch_manifest RELEASE OUT: RELEASE's images.json alone. A channel (or
# latest) without one is mid-update: exit 75 (EX_TEMPFAIL), try again.
fetch_manifest() {
  local release="$1" out="$2"
  mkdir -p "$out"
  if ! download "$(base_of "$release")" images.json "$out/images.json"; then
    rm -f "$out/images.json"
    case "$release" in
      channel-* | latest)
        echo "release-assets.sh: $release has no images.json: it is mid-update (or does not exist yet); try again later" >&2
        exit 75
        ;;
    esac
    die "$release has no images.json"
  fi
  validate_manifest "$out" false
}

# fetch RELEASE [DIR]: images.json and every asset, each from the release
# its image names (schema 2) or from RELEASE itself (schema 1).
fetch() {
  local release="${1:?fetch needs a release name}" out="${2:-$dir}" key asset holder
  need curl jq sha256sum
  fetch_manifest "$release" "$out"
  for key in "${image_keys[@]}"; do
    asset="$(jq -r --arg k "$key" '.images[$k].asset' "$out/images.json")"
    holder="$(jq -r --arg k "$key" --arg r "$release" '.images[$k].release // $r' "$out/images.json")"
    download "$(base_of "$holder")" "$asset" "$out/$asset" || die "$holder has no $asset, which $release names"
    download "$(base_of "$holder")" "$asset.sha256" "$out/$asset.sha256" || die "$holder has no $asset.sha256"
  done
  validate_manifest "$out"
  say "fetched $release ($(jq -r .commit "$out/images.json" | cut -c1-12)) into $out"
}

load() {
  local d="${1:-$dir}" key asset ref id got
  need docker
  validate_manifest "$d"
  for key in "${image_keys[@]}"; do
    asset="$(jq -r --arg k "$key" '.images[$k].asset' "$d/images.json")"
    ref="$(jq -r --arg k "$key" '.images[$k].ref' "$d/images.json")"
    id="$(jq -r --arg k "$key" '.images[$k].imageId' "$d/images.json")"
    docker load -i "$d/$asset" >&2
    got="$(docker image inspect --format '{{.Id}}' "$ref" 2>/dev/null)" || die "loading $asset left no $ref"
    [ "$got" = "$id" ] || die "$ref is $got after loading $asset, not the $id images.json names"
    say "loaded $ref ($id)"
  done
}

mirror() {
  local release="${1:?mirror needs <release> <registry/prefix>}" prefix="${2:?mirror needs a registry/prefix to push to}" key ref target keys
  shift 2
  need docker jq
  prefix="${prefix%/}"
  if [ $# -gt 0 ]; then keys=("$@"); else keys=("${image_keys[@]}"); fi
  # RELEASE_ASSETS_REUSE=true mirrors the assets already in $dir.
  if [ "${RELEASE_ASSETS_REUSE:-false}" != true ] || [ ! -f "$dir/images.json" ]; then
    fetch "$release" "$dir" >&2
  fi
  load "$dir" >&2
  for key in "${keys[@]}"; do
    ref="$(jq -er --arg k "$key" '.images[$k].ref' "$dir/images.json")" || die "$release's images.json has no $key image"
    target="$prefix/${ref#*/}"
    docker tag "$ref" "$target"
    if [ "$dry_run" = true ]; then
      echo "release-assets.sh: [dry-run] would docker push $target" >&2
    else
      docker push "$target" >&2
    fi
    printf '%s=%s\n' "$key" "$target"
  done
}

# source_into RELEASE COMMIT: RELEASE's images.json in $dir, unless $dir
# already holds that commit's (the release job's own, in a dry run). A
# pointer needs nothing else: its images stay on their holder.
source_into() {
  local release="$1" commit="$2"
  if [ -f "$dir/images.json" ] && [ "$(jq -r .commit "$dir/images.json")" = "$commit" ]; then
    validate_manifest "$dir" false
    say "using the ${commit:0:12} manifest already in $dir"
    return 0
  fi
  fetch_manifest "$release" "$dir"
  [ "$(jq -r .commit "$dir/images.json")" = "$commit" ] || die "$release holds $(jq -r .commit "$dir/images.json"), not $commit"
}

# promote_version [vX.Y.Z]: the release promote publishes -- the one
# given, or the newest vX.Y.Z the git tag latest names.
promote_version() {
  local version="${1:-}" latest
  if [ -z "$version" ]; then
    latest="$(remote_commit_of_tag latest)"
    [ -n "$latest" ] || die "the git tag latest names nothing on $remote, and no version was given"
    version="$(git ls-remote --tags "$remote" 'refs/tags/v*' |
      awk -v c="$latest" '$1 == c { t = $2; sub(/^refs\/tags\//, "", t); sub(/\^\{\}$/, "", t); print t }' |
      grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
    [ -n "$version" ] || die "no vX.Y.Z tag names ${latest:0:12}, the commit the git tag latest names"
  fi
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$version is not a vX.Y.Z release"
  printf '%s\n' "$version"
}

promote() {
  need git jq
  local version commit sha_release
  version="$(promote_version "${1:-}")"
  commit="$(remote_commit_of_tag "$version")"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "$remote has no tag $version"
  sha_release="sha-${commit:0:12}"
  # A vX.Y.Z that already holds its copies (a rollback re-pointing latest
  # at it, or a re-run) is its own source: its sha- release may since have
  # been pruned.
  if download "$(base_of "$version")" images.json "$scratch/probe-version.json" &&
      [ "$(jq -r .commit "$scratch/probe-version.json" 2>/dev/null)" = "$commit" ]; then
    sha_release="$version"
  fi
  say "promoting $sha_release as $version"
  # A release promoted before its commit was published as release assets
  # has no sha- release to promote. Asked for by name, that fails; on the
  # automatic run every push makes, it is said and skipped.
  if [ -z "${1:-}" ] && ! { [ -f "$dir/images.json" ] && [ "$(jq -r .commit "$dir/images.json")" = "$commit" ]; } &&
      ! download "$(base_of "$sha_release")" images.json "$scratch/probe.json"; then
    echo "::warning::release-assets.sh: $version (${commit:0:12}) has no $sha_release release: it predates release assets, so there is nothing to promote as $version"
    return 0
  fi
  # vX.Y.Z holds its own copies of the sha- release's tarballs, so that
  # pruning sha- releases can never break it: they are downloaded (unless
  # the release job left them in $dir), checked, and uploaded again.
  if [ -f "$dir/images.json" ] && [ "$(jq -r .commit "$dir/images.json")" = "$commit" ] &&
      validate_manifest "$dir" true 2>/dev/null; then
    say "using the ${commit:0:12} assets already in $dir"
  else
    fetch "$sha_release" "$dir"
  fi
  [ "$(jq -r .commit "$dir/images.json")" = "$commit" ] || die "$sha_release holds $(jq -r .commit "$dir/images.json"), not $commit"
  rewrite_manifest "$dir" "$dir" "$version"
  jq --arg r "$version" '.schema = 2 | .images |= map_values(.release = $r)' "$dir/images.json" > "$dir/images.json.tmp"
  mv "$dir/images.json.tmp" "$dir/images.json"
  # publish_one marks it GitHub's latest when the git tag latest names
  # its commit.
  publish "$version"
  delete_legacy_latest
}

# delete_legacy_latest: contract v2 has no release tagged latest -- the
# latest channel is GitHub's own latest-release marker -- so a release of
# that name left from v1 is deleted. The git tag latest stays: it is the
# person's approval promote reads.
delete_legacy_latest() {
  local status
  release_json latest || return 0
  status="$(write DELETE "/repos/$repo/releases/$(jq -r .id "$scratch/release")")"
  ok "$status" || die "deleting the legacy release named latest answered $status"
  say "deleted the legacy release named latest (the git tag latest is kept)"
}

retag() {
  need jq
  local from="${1:?retag needs <from-release> <version> <release>...}" version="${2:?retag needs a version}" commit
  shift 2
  [ $# -gt 0 ] || die "retag needs at least one release to publish"
  case "$from" in
    sha-*) ;;
    *) die "retag publishes from an immutable sha-<short> release, not $from" ;;
  esac
  if [ -f "$dir/images.json" ] && [ "sha-$(jq -r .commit "$dir/images.json" | cut -c1-12)" = "$from" ]; then
    commit="$(jq -r .commit "$dir/images.json")"
  else
    fetch_manifest "$from" "$dir"
    commit="$(jq -r .commit "$dir/images.json")"
  fi
  source_into "$from" "$commit"
  rewrite_manifest "$dir" "$dir" "$version"
  publish "$@"
}

# commit_of TAG: the commit TAG names on $remote, or fail.
commit_of() {
  local c
  c="$(remote_commit_of_tag "$1")"
  [[ "$c" =~ ^[0-9a-f]{40}$ ]] || die "$remote has no tag $1"
  printf '%s' "$c"
}

# from_commit COMMIT VERSION RELEASE...: retag COMMIT's sha- release.
from_commit() {
  local commit="$1" version="$2"
  shift 2
  retag "sha-${commit:0:12}" "$version" "$@"
}

train_rc() {
  need git jq
  local rc="${1:-}" candidate newest commit
  if [ -z "$rc" ]; then
    candidate="$(git ls-remote "$remote" refs/heads/candidate | awk 'NR == 1 {print $1}')"
    [ -n "$candidate" ] || die "$remote has no candidate branch, and no rc was given"
    rc="$(git ls-remote --tags "$remote" 'refs/tags/v*-rc.*' |
      awk -v c="$candidate" '$1 == c { t = $2; sub(/^refs\/tags\//, "", t); sub(/\^\{\}$/, "", t); print t }' |
      grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' | sort -V | tail -n 1 || true)"
    [ -n "$rc" ] || die "no rc tag names ${candidate:0:12}, the commit candidate is on"
  fi
  [[ "$rc" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] || die "$rc is not a vX.Y.Z-rc.N release candidate"
  commit="$(commit_of "$rc")"
  newest="$(git ls-remote --tags "$remote" 'refs/tags/v*-rc.*' | awk '{ t = $2; sub(/^refs\/tags\//, "", t); sub(/\^\{\}$/, "", t); print t }' |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' | sort -uV | tail -n 1 || true)"
  if [ "$newest" = "$rc" ]; then
    force_move=true from_commit "$commit" "$rc" "$rc" channel-candidate
  else
    say "$rc is not the newest rc ($newest): publishing it alone, leaving channel-candidate where it is"
    from_commit "$commit" "$rc" "$rc"
  fi
}

train_phase() {
  local phase="$1" v="${2:-}"
  case "$phase" in
    rc) train_rc "$v" ;;
    pending)
      [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "pending needs a vX.Y.Z, not '$v'"
      from_commit "$(commit_of "$v-pending")" "$v" "$v-pending"
      ;;
    discard-rc)
      [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] || die "discard-rc needs a vX.Y.Z-rc.N, not '$v'"
      from_commit "$(commit_of "$v")" "$v" "$v-discarded"
      ;;
    discard-pending)
      [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "discard-pending needs a vX.Y.Z, not '$v'"
      local c
      c="$(remote_commit_of_tag "$v-pending")"
      [ -n "$c" ] || c="$(commit_of "$v")"
      from_commit "$c" "$v" "$v-rejected"
      ;;
    retract)
      [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "retract needs a vX.Y.Z, not '$v'"
      from_commit "$(commit_of "$v")" "$v" "$v-retracted"
      ;;
  esac
}

# train_tag TAG: publish what a pushed release-train tag names.
train_tag() {
  local tag="${1:?train-tag needs a tag}"
  case "$tag" in
    v*-rc.* | v*-pending | v*-retracted)
      # The release of a tag the train has pushed, never a tag of its own:
      # publishing a release on a tag that does not exist creates it, and
      # release-from-trunk.sh reads -discarded and -retracted tags to drop
      # an rc or skip a release. A dispatch of release-train-assets.yml
      # naming a marker nobody pushed must not plant one.
      [[ "$(remote_commit_of_tag "$tag")" =~ ^[0-9a-f]{40}$ ]] ||
        die "$tag is not a tag the release train has pushed to $remote: train-tag publishes a pushed tag's release, never creates the tag"
      ;;
  esac
  case "$tag" in
    v*-rc.*-discarded) train_phase discard-rc "${tag%-discarded}" ;;
    v*-rc.*) train_phase rc "$tag" ;;
    v*-pending) train_phase pending "${tag%-pending}" ;;
    v*-retracted) train_phase retract "${tag%-retracted}" ;;
    # vX.Y.Z and latest are a person's approval (promote-release.yml) or
    # the train's promotion, published by promote.yml / promote-release.yml
    # / publish-release.yml with `promote`: never by a train-tag run, which
    # any rc tag push or dispatch of release-train-assets.yml starts.
    v[0-9]*.[0-9]*.[0-9]* | latest) die "$tag is a promoted release, not a release-train tag: promote.yml and promote-release.yml publish it (release-assets.sh promote)" ;;
    *) die "$tag is not a tag the release train publishes" ;;
  esac
}

# --- the grain CLI -------------------------------------------------------
#
# vX.Y.Z and vX.Y.Z-rc.N releases also carry the grain CLI as
# scripts/build-cli.sh builds it, for GET /cli/install.sh
# (pkg/ui/cli_install.sh): four archives and a checksums.txt the script
# checks the one it downloads against. They sit beside the release's
# images.json, which does not name them.
cli_archives=(grain-linux-amd64.tar.gz grain-linux-arm64.tar.gz grain-darwin-amd64.tar.gz grain-darwin-arm64.tar.gz)

# validate_cli DIR: the four archives, each holding a grain, and a
# checksums.txt of `<sha256>  <archive>` lines naming each of them once,
# and nothing else, that they match.
validate_cli() {
  local d="$1" a bad
  need sha256sum tar
  [ -d "$d" ] || die "$d is not a directory of grain CLI archives (scripts/build-cli.sh DIR)"
  for a in "${cli_archives[@]}"; do
    [ -s "$d/$a" ] || die "$d has no $a (scripts/build-cli.sh writes it)"
  done
  [ -f "$d/checksums.txt" ] || die "$d has no checksums.txt"
  bad="$(grep -nvE '^[0-9a-f]{64}  grain-(linux|darwin)-(amd64|arm64)\.tar\.gz$' "$d/checksums.txt" || true)"
  [ -z "$bad" ] || die "checksums.txt has lines that are not sha256sum's '<hex>  grain-<os>-<arch>.tar.gz': $bad"
  for a in "${cli_archives[@]}"; do
    [ "$(awk -v a="$a" '$2 == a' "$d/checksums.txt" | wc -l)" = 1 ] || die "checksums.txt does not name $a exactly once"
  done
  [ "$(wc -l < "$d/checksums.txt")" = "${#cli_archives[@]}" ] || die "checksums.txt names more than the ${#cli_archives[@]} archives"
  (cd "$d" && sha256sum --quiet -c checksums.txt) >&2 || die "the archives in $d do not match their checksums.txt"
  for a in "${cli_archives[@]}"; do
    tar -tzf "$d/$a" 2>/dev/null | grep -x grain > /dev/null || die "$d/$a holds no grain binary"
  done
}

# cli_publish RELEASE [DIR]: upload DIR's archives, then checksums.txt,
# to RELEASE. checksums.txt goes last: a release carrying it carries all
# four, and is never written again. Archives a run that died before
# checksums.txt left are deleted and uploaded again, since nothing can
# install them without it.
cli_publish() {
  local release="${1:?cli-publish needs a vX.Y.Z or vX.Y.Z-rc.N release}" d="${2:-${RELEASE_CLI_DIR:-/tmp/release-cli}}" id id_left name listed missing
  need curl jq
  [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || die "$release is not a vX.Y.Z or vX.Y.Z-rc.N release: only those carry the grain CLI"
  [ -n "$token" ] || [ "$dry_run" = true ] || die "publishing needs GITHUB_TOKEN (permissions: contents: write)"
  validate_cli "$d"
  upload_base=""
  if release_json "$release"; then
    id="$(jq -r .id "$scratch/release")"
    upload_base="$(jq -r '.upload_url | sub("\\{.*$"; "")' "$scratch/release")"
    asset_list "$id" > "$scratch/existing"
  elif [ "$dry_run" = true ]; then
    say "[dry-run] would upload the grain CLI to $release, which promote or train-tag publishes"
    id=dry-run
    : > "$scratch/existing"
  elif [ "${RELEASE_CLI_SKIP_UNRELEASED:-false}" = true ]; then
    echo "::warning::release-assets.sh: there is no release $release to carry the grain CLI; skipping it"
    return 0
  else
    die "there is no release $release: publish it (promote, or train-tag for an rc) before its grain CLI"
  fi
  if grep -q ' checksums.txt$' "$scratch/existing"; then
    for name in "${cli_archives[@]}"; do
      grep -q " $name\$" "$scratch/existing" || die "$release carries checksums.txt but no $name"
    done
    say "$release already carries the grain CLI; its archives are never replaced"
    return 0
  fi
  for name in "${cli_archives[@]}"; do
    while read -r id_left; do
      say "deleting the $name an earlier run left on $release without its checksums.txt"
      delete_asset "$id_left"
    done < <(awk -v n="$name" '$2 == n {print $1}' "$scratch/existing")
  done
  for name in "${cli_archives[@]}" checksums.txt; do
    dir="$d" upload "$id" "$name"
  done
  # Fail closed: a release missing any of them cannot be installed from.
  if [ "$dry_run" != true ]; then
    listed="$(asset_list "$id" | awk '{print $2}')"
    missing=""
    for name in "${cli_archives[@]}" checksums.txt; do
      grep -qx "$name" <<< "$listed" || missing="$missing $name"
    done
    [ -z "$missing" ] || die "$release is missing$missing after publishing the grain CLI"
  fi
  say "published the grain CLI on $release: ${cli_archives[*]} checksums.txt"
}

# --- grain-agentd per-platform binaries and signed manifests -----------------

agentd_platforms=("linux/amd64" "linux/arm64")
agentd_binaries=(grain-agentd-linux-amd64 grain-agentd-linux-arm64)
agentd_manifests=(grain-agentd-linux-amd64.manifest.json grain-agentd-linux-arm64.manifest.json)
default_agentd_pub="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/pkg/agentd/release/agentd-release.pub"

# agentd_pubkey_to_pem INPUT OUT_PEM: write SubjectPublicKeyInfo PEM for INPUT
# (either a PEM string/file or "ed25519:<base64>").
agentd_pubkey_to_pem() {
  local src="$1" out="$2" b64 padded raw_len
  if printf '%s\n' "$src" | grep -q -- '-----BEGIN PUBLIC KEY-----'; then
    printf '%s\n' "$src" > "$out"
    return 0
  fi
  src="$(printf '%s\n' "$src" | grep -vE '^[[:space:]]*(#|$)' | head -n 1 | tr -d ' \t\r\n')"
  [[ "$src" == ed25519:* ]] || die "invalid release public key (want PUBLIC KEY PEM or ed25519:<base64>)"
  b64="${src#ed25519:}"
  case $(( ${#b64} % 4 )) in
    0) padded="$b64" ;;
    2) padded="${b64}==" ;;
    3) padded="${b64}=" ;;
    *) die "invalid base64 in ed25519 public key" ;;
  esac
  printf '%s' "$padded" | base64 -d > "$out.raw" 2>/dev/null || die "could not base64-decode ed25519 public key"
  raw_len="$(stat -c %s "$out.raw")"
  [ "$raw_len" = 32 ] || die "ed25519 public key decoded to $raw_len bytes, want 32"
  {
    printf '%s\n' '-----BEGIN PUBLIC KEY-----'
    (printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00'; cat "$out.raw") | base64 -w 64
    printf '%s\n' '-----END PUBLIC KEY-----'
  } > "$out"
  rm -f "$out.raw"
}

agentd_key_id_of_pem() {
  local der_len
  openssl pkey -pubin -in "$1" -outform DER > "$scratch/keyid.der" 2>/dev/null || die "$1 is not a valid Ed25519 PUBLIC KEY PEM"
  der_len="$(stat -c %s "$scratch/keyid.der")"
  [ "$der_len" = 44 ] || die "$1 is not an Ed25519 SubjectPublicKeyInfo key (DER length $der_len, want 44)"
  tail -c 32 "$scratch/keyid.der" | sha256sum | cut -c1-16
}

agentd_canonical_manifest() {
  jq -r '
    "grain-agentd-release-manifest/v1",
    "schema: \(.schema)",
    "version: \(.version)",
    "platform: \(.platform)",
    "asset: \(.asset)",
    "sha256: \(.sha256)",
    "size: \(.size)",
    "min_contract_features: \(.min_contract_features | join(","))",
    (if ((.features // []) | length) > 0 then "features: \(.features | join(","))" else empty end),
    "key_id: \(.key_id)"
  ' "$1"
}

agentd_decode_sig() {
  local s="$1" out="$2" b64 padded len
  [[ "$s" == ed25519:* ]] || die "signature must start with ed25519:"
  b64="${s#ed25519:}"
  case $(( ${#b64} % 4 )) in
    0) padded="$b64" ;;
    2) padded="${b64}==" ;;
    3) padded="${b64}=" ;;
    *) die "invalid base64 in ed25519 signature" ;;
  esac
  printf '%s' "$padded" | base64 -d > "$out" 2>/dev/null || die "could not decode ed25519 signature"
  len="$(stat -c %s "$out")"
  [ "$len" = 64 ] || die "ed25519 signature decoded to $len bytes, want 64"
}

agentd_resolve_pinned_pem() {
  local out="$1"
  if [ -n "${AGENTD_RELEASE_PUBLIC_KEY:-}" ]; then
    agentd_pubkey_to_pem "$AGENTD_RELEASE_PUBLIC_KEY" "$out"
  elif [ -n "${AGENTD_RELEASE_PUBLIC_KEY_FILE:-}" ]; then
    [ -f "$AGENTD_RELEASE_PUBLIC_KEY_FILE" ] || die "no public key file $AGENTD_RELEASE_PUBLIC_KEY_FILE"
    agentd_pubkey_to_pem "$(cat "$AGENTD_RELEASE_PUBLIC_KEY_FILE")" "$out"
  else
    [ -f "$default_agentd_pub" ] || die "no embedded public key at $default_agentd_pub"
    agentd_pubkey_to_pem "$(cat "$default_agentd_pub")" "$out"
  fi
}

agentd_verify_manifest_sig() {
  local mf="$1" pinned_pem="$2" pinned_id target_id rot_len idx found_start prev_id old_str new_str issued sig_str old_id new_id
  pinned_id="$(agentd_key_id_of_pem "$pinned_pem")"
  target_id="$(jq -r '.key_id // ""' "$mf")"
  rot_len="$(jq -r '(.rotations // []) | length' "$mf")"
  cp "$pinned_pem" "$scratch/active-signer.pem"

  if [ "$rot_len" -gt 0 ]; then
    [ "$rot_len" -le 8 ] || die "$mf carries $rot_len key rotations (max 8)"
    found_start=false
    prev_id=""
    for (( idx = 0; idx < rot_len; idx++ )); do
      old_str="$(jq -r ".rotations[$idx].old_public_key" "$mf")"
      new_str="$(jq -r ".rotations[$idx].new_public_key" "$mf")"
      issued="$(jq -r ".rotations[$idx].issued_at" "$mf")"
      sig_str="$(jq -r ".rotations[$idx].signature" "$mf")"
      agentd_pubkey_to_pem "$old_str" "$scratch/rot-old.pem"
      agentd_pubkey_to_pem "$new_str" "$scratch/rot-new.pem"
      old_id="$(agentd_key_id_of_pem "$scratch/rot-old.pem")"
      new_id="$(agentd_key_id_of_pem "$scratch/rot-new.pem")"
      [ "$old_id" != "$new_id" ] || die "$mf rotation[$idx] has identical old and new key $old_id"
      if [ "$idx" -gt 0 ] && [ "$old_id" != "$prev_id" ]; then
        die "$mf rotation[$idx] old key $old_id does not chain from $prev_id"
      fi
      prev_id="$new_id"
      if [ "$old_id" = "$pinned_id" ]; then
        found_start=true
      fi
      printf 'grain-agentd-key-rotation/v1\nold_public_key: %s\nnew_public_key: %s\nissued_at: %s\n' \
        "$old_str" "$new_str" "$issued" > "$scratch/rot.msg"
      agentd_decode_sig "$sig_str" "$scratch/rot.sig"
      openssl pkeyutl -verify -pubin -inkey "$scratch/rot-old.pem" -rawin \
        -in "$scratch/rot.msg" -sigfile "$scratch/rot.sig" >/dev/null 2>&1 ||
        die "$mf rotation[$idx] ($old_id -> $new_id) failed signature verification"
    done
    if [ "$pinned_id" = "$target_id" ]; then
      [ "$prev_id" = "$pinned_id" ] || die "$mf rotations end at $prev_id while key_id is $target_id"
    else
      [ "$found_start" = true ] || die "$mf signed by $target_id does not chain from pinned key $pinned_id"
      [ "$prev_id" = "$target_id" ] || die "$mf rotation chain ends at $prev_id, not key_id $target_id"
      cp "$scratch/rot-new.pem" "$scratch/active-signer.pem"
    fi
  else
    [ "$pinned_id" = "$target_id" ] ||
      die "$mf is signed by key $target_id, not pinned release key $pinned_id (and carries no key rotation)"
  fi

  agentd_canonical_manifest "$mf" > "$scratch/manifest.msg"
  agentd_decode_sig "$(jq -r '.signature' "$mf")" "$scratch/manifest.sig"
  openssl pkeyutl -verify -pubin -inkey "$scratch/active-signer.pem" -rawin \
    -in "$scratch/manifest.msg" -sigfile "$scratch/manifest.sig" >/dev/null 2>&1 ||
    die "$mf failed Ed25519 signature verification for key $target_id"
}

validate_agentd() {
  local d="${1:-${RELEASE_AGENTD_DIR:-/tmp/release-agentd}}" require_sig=false plat bin mf problems size sum sig magic
  if [ "${2:-}" = --require-signature ]; then
    require_sig=true
  fi
  need jq sha256sum openssl
  [ -d "$d" ] || die "$d is not a directory of grain-agentd release assets (scripts/build-agentd.sh DIR)"

  for plat in "${agentd_platforms[@]}"; do
    bin="grain-agentd-${plat%/*}-${plat#*/}"
    mf="$bin.manifest.json"
    [ -s "$d/$bin" ] || die "$d has no $bin (scripts/build-agentd.sh writes it)"
    [ -s "$d/$mf" ] || die "$d has no $mf (scripts/build-agentd.sh writes it)"

    problems="$(jq -r --arg p "$plat" --arg a "$bin" '
      [
        (if type != "object" then "root must be a JSON object" else empty end),
        (if ([keys[] | select(. as $k | ["schema","version","platform","asset","sha256","size","min_contract_features","features","key_id","signature","rotations"] | index($k) | not)] | length) > 0
          then "manifest has unknown fields" else empty end),
        (if .schema != 1 then "schema must be 1" else empty end),
        (if (.version | type) != "string" or (.version | test("^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$") | not)
          then "invalid version \(.version | tojson)" else empty end),
        (if .platform != $p then "platform is \(.platform | tojson), want \($p | tojson)" else empty end),
        (if .asset != $a then "asset is \(.asset | tojson), want \($a | tojson)" else empty end),
        (if (.sha256 | type) != "string" or (.sha256 | test("^[0-9a-f]{64}$") | not)
          then "invalid sha256" else empty end),
        (if (.size | type) != "number" or .size <= 0 then "invalid size" else empty end),
        (if (.min_contract_features | type) != "array" or (.min_contract_features | length) == 0
            or ([.min_contract_features[] | (type == "string" and test("^[a-z0-9_]+$"))] | all | not)
          then "invalid min_contract_features" else empty end),
        (if ((.signature // "") == "") != ((.key_id // "") == "")
          then "key_id and signature must either both be set or both be empty" else empty end)
      ] | .[]' "$d/$mf" 2>&1)" || die "$d/$mf is not valid JSON: $problems"
    [ -z "$problems" ] || die "$d/$mf does not meet the grain-agentd manifest contract: $problems"

    size="$(stat -c %s "$d/$bin")"
    [ "$size" = "$(jq -r '.size' "$d/$mf")" ] || die "$d/$bin is $size bytes, but $mf says $(jq -r '.size' "$d/$mf")"
    sum="$(sha256sum "$d/$bin" | awk '{print $1}')"
    [ "$sum" = "$(jq -r '.sha256' "$d/$mf")" ] || die "$d/$bin has sha256 $sum, but $mf says $(jq -r '.sha256' "$d/$mf")"

    # Static ELF check: ELF magic 7f 45 4c 46, and no PT_INTERP when readelf is installed.
    magic="$(head -c 4 "$d/$bin" | od -An -tx1 | tr -d ' \n')"
    if [ "$magic" = "7f454c46" ] && command -v readelf >/dev/null 2>&1; then
      if readelf -l "$d/$bin" 2>/dev/null | grep -q 'INTERP'; then
        die "$d/$bin is dynamically linked (has PT_INTERP); grain-agentd release binaries must be static (CGO_ENABLED=0)"
      fi
    fi

    sig="$(jq -r '.signature // ""' "$d/$mf")"
    if [ "$require_sig" = true ] && [ -z "$sig" ]; then
      die "$d/$mf is unsigned (--require-signature)"
    fi
    if [ -n "$sig" ]; then
      agentd_resolve_pinned_pem "$scratch/pinned-agentd.pub"
      agentd_verify_manifest_sig "$d/$mf" "$scratch/pinned-agentd.pub"
    fi
  done
}

agentd_sign() {
  local d="${1:-${RELEASE_AGENTD_DIR:-/tmp/release-agentd}}" key_file="${AGENTD_RELEASE_SIGNING_KEY:-}" rot_file="${AGENTD_RELEASE_ROTATIONS:-}" require_signing=false key_id mf sig_b64 rots_json
  need jq sha256sum openssl
  case "${REQUIRE_AGENTD_SIGNING:-false}" in
    1 | [Tt]rue | TRUE | [Yy]es | YES) require_signing=true ;;
  esac
  validate_agentd "$d"
  if [ -z "$key_file" ]; then
    if [ "$require_signing" = true ]; then
      die "AGENTD_RELEASE_SIGNING_KEY is not set and REQUIRE_AGENTD_SIGNING=true"
    fi
    echo "WARNING: AGENTD_RELEASE_SIGNING_KEY is not set; leaving grain-agentd manifests in $d unsigned" >&2
    return 0
  fi
  [ -f "$key_file" ] || die "AGENTD_RELEASE_SIGNING_KEY $key_file does not exist"
  openssl pkey -in "$key_file" -pubout -out "$scratch/signer.pub" 2>/dev/null ||
    die "AGENTD_RELEASE_SIGNING_KEY is not a valid OpenSSL Ed25519 private key PEM"
  key_id="$(agentd_key_id_of_pem "$scratch/signer.pub")"

  rots_json="[]"
  if [ -n "$rot_file" ]; then
    [ -f "$rot_file" ] || die "AGENTD_RELEASE_ROTATIONS $rot_file does not exist"
    rots_json="$(jq -c '.' "$rot_file")" || die "$rot_file is not valid JSON"
  fi

  for mf in "${agentd_manifests[@]}"; do
    jq --arg kid "$key_id" --argjson rots "$rots_json" \
      '.key_id = $kid | .signature = "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==" | if ($rots | length) > 0 then .rotations = $rots else del(.rotations) end' \
      "$d/$mf" > "$d/$mf.tmp"
    agentd_canonical_manifest "$d/$mf.tmp" > "$scratch/to-sign.msg"
    openssl pkeyutl -sign -inkey "$key_file" -rawin -in "$scratch/to-sign.msg" -out "$scratch/signed.sig" ||
      die "openssl pkeyutl -sign failed on $mf"
    sig_b64="ed25519:$(base64 -w 0 < "$scratch/signed.sig")"
    jq --arg sig "$sig_b64" '.signature = $sig' "$d/$mf.tmp" > "$d/$mf"
    rm -f "$d/$mf.tmp"
  done

  validate_agentd "$d" --require-signature
  say "signed grain-agentd manifests in $d with key $key_id"
}

agentd_publish() {
  local release="${1:?agentd-publish needs a vX.Y.Z or vX.Y.Z-rc.N release}" d="${2:-${RELEASE_AGENTD_DIR:-/tmp/release-agentd}}" id id_left name listed missing already_complete=true
  need curl jq
  [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || die "$release is not a vX.Y.Z or vX.Y.Z-rc.N release: only those carry grain-agentd"
  [ -n "$token" ] || [ "$dry_run" = true ] || die "publishing needs GITHUB_TOKEN (permissions: contents: write)"
  validate_agentd "$d"
  upload_base=""
  if release_json "$release"; then
    id="$(jq -r .id "$scratch/release")"
    upload_base="$(jq -r '.upload_url | sub("\\{.*$"; "")' "$scratch/release")"
    asset_list "$id" > "$scratch/existing"
  elif [ "$dry_run" = true ]; then
    say "[dry-run] would upload grain-agentd to $release, which promote or train-tag publishes"
    id=dry-run
    : > "$scratch/existing"
  elif [ "${RELEASE_AGENTD_SKIP_UNRELEASED:-${RELEASE_CLI_SKIP_UNRELEASED:-false}}" = true ]; then
    echo "::warning::release-assets.sh: there is no release $release to carry grain-agentd; skipping it"
    return 0
  else
    die "there is no release $release: publish it (promote, or train-tag for an rc) before its grain-agentd binaries"
  fi

  for name in "${agentd_binaries[@]}" "${agentd_manifests[@]}"; do
    if ! grep -q " $name\$" "$scratch/existing"; then
      already_complete=false
      break
    fi
  done
  if [ "$already_complete" = true ]; then
    say "$release already carries grain-agentd (${agentd_binaries[*]}); its assets are never replaced"
    return 0
  fi

  for name in "${agentd_binaries[@]}" "${agentd_manifests[@]}"; do
    while read -r id_left; do
      say "deleting partial $name an earlier run left on $release"
      delete_asset "$id_left"
    done < <(awk -v n="$name" '$2 == n {print $1}' "$scratch/existing")
  done

  # Upload binaries first, then manifests last so the presence of both
  # manifests marks a complete upload.
  for name in "${agentd_binaries[@]}" "${agentd_manifests[@]}"; do
    dir="$d" upload "$id" "$name"
  done

  if [ "$dry_run" != true ]; then
    listed="$(asset_list "$id" | awk '{print $2}')"
    missing=""
    for name in "${agentd_binaries[@]}" "${agentd_manifests[@]}"; do
      grep -qx "$name" <<< "$listed" || missing="$missing $name"
    done
    [ -z "$missing" ] || die "$release is missing$missing after publishing grain-agentd"
  fi
  say "published grain-agentd on $release: ${agentd_binaries[*]} ${agentd_manifests[*]}"
}

case "$cmd" in
  package) package ;;
  rc | pending | discard-rc | discard-pending | retract) train_phase "$cmd" "$@" ;;
  prune) prune ;;
  train-tag) train_tag "$@" ;;
  validate) validate_manifest "${1:-$dir}" && say "${1:-$dir}/images.json meets release-asset contract v2" ;;
  publish) publish "$@" ;;
  promote) promote "$@" ;;
  retag) retag "$@" ;;
  fetch) fetch "$@" ;;
  load) load "$@" ;;
  mirror) mirror "$@" ;;
  promote-version) need git && promote_version "$@" ;;
  cli-validate) validate_cli "${1:-${RELEASE_CLI_DIR:-/tmp/release-cli}}" && say "${1:-${RELEASE_CLI_DIR:-/tmp/release-cli}} holds the grain CLI: ${cli_archives[*]} checksums.txt" ;;
  cli-publish) cli_publish "$@" ;;
  agentd-validate)
    if [ "${1:-}" = --require-signature ]; then
      validate_agentd "${RELEASE_AGENTD_DIR:-/tmp/release-agentd}" --require-signature
      say "${RELEASE_AGENTD_DIR:-/tmp/release-agentd} holds signed grain-agentd: ${agentd_binaries[*]}"
    else
      validate_agentd "${1:-${RELEASE_AGENTD_DIR:-/tmp/release-agentd}}" "${2:-}"
      say "${1:-${RELEASE_AGENTD_DIR:-/tmp/release-agentd}} holds grain-agentd: ${agentd_binaries[*]}"
    fi
    ;;
  agentd-sign) agentd_sign "$@" ;;
  agentd-publish) agentd_publish "$@" ;;
  *)
    sed -n '17,108p' "$0" >&2
    exit 2
    ;;
esac
