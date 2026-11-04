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
# releases -- release-asset contract v1, docs/release-assets.md -- rather
# than as registry packages. It needs nothing but the repository's own
# GITHUB_TOKEN with `contents: write`.
#
#   package
#           `docker save | gzip` the grain, guest and k8s-sandbox images
#           `release-images.sh build` (RELEASE_IMAGES_TARGET=assets) left
#           named in $RELEASE_IMAGES_STATE into $RELEASE_ASSETS_DIR, each
#           with a `<asset>.sha256` sidecar, and write images.json beside
#           them. An image at or over GitHub's 2 GiB per-asset limit fails.
#   validate [DIR]
#           check DIR's images.json against contract v1, and every asset
#           and sidecar it names against it.
#   publish <release>...
#           upload DIR to each release, creating it (and its tag, at the
#           manifest's commit) if it does not exist. See release_kind for
#           which releases are immutable and which move, and how.
#   promote [vX.Y.Z]
#           publish the sha-<short> release of vX.Y.Z's commit -- the
#           release the git tag `latest` names, with no argument -- as
#           vX.Y.Z, and as `latest` when the git tag `latest` agrees. The
#           tarballs are the sha- release's own; only images.json's
#           version and createdAt differ.
#   retag <from-release> <version> <release>...
#           the same for any other name: the release train's
#           vX.Y.0-rc.N / channel-candidate, vX.Y.0-pending, and the
#           -discarded / -rejected / -retracted markers.
#   rc [vX.Y.Z-rc.N] | pending <vX.Y.Z> | discard-rc <vX.Y.Z-rc.N> |
#   discard-pending <vX.Y.Z> | retract <vX.Y.Z>
#           release-images.sh's release-train phases, so that
#           RELEASE_IMAGES="bash scripts/release-assets.sh" publishes the
#           train's names as release assets: an rc as vX.Y.Z-rc.N and, when
#           it is the newest rc, channel-candidate; a pending release as
#           vX.Y.Z-pending (version vX.Y.Z); and the vX.Y.Z-rc.N-discarded,
#           vX.Y.Z-rejected and vX.Y.Z-retracted markers. Each is the
#           sha-<short> release of the commit the train's git tag names.
#   train-tag <tag>
#           the phase a pushed train tag asks for (release-train-assets.yml)
#   mirror <release> <registry/prefix> [image...]
#           for a cluster, which cannot `docker load`: fetch and load the
#           release, then tag each image (all three by default) as
#           <registry/prefix>/<name>:<tag> -- grain/guest:sha-x becomes
#           <prefix>/guest:sha-x -- push it, and print `<image>=<pushed
#           ref>` per image. The deployment's own registry or
#           sandbox_image_mirror is the prefix (docs/release-assets.md).
#   fetch <release> [DIR]
#           download a release's images.json and assets into DIR and
#           validate them. A moving release with no images.json is
#           mid-update: exit 75 (EX_TEMPFAIL), try again later.
#   load [DIR]
#           validate DIR, then `docker load` every image and check the
#           loaded image IDs and refs are the ones the manifest names.
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
      (if .schema != 1 then "schema is \(.schema|tojson), not 1" else empty end),
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
    printf 'release-assets.sh: %s does not meet release-asset contract v1:\n' "$manifest" >&2
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
  json="$(jq -n --arg r "github.com/$repo" --arg c "$commit" --arg v "$version" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{schema: 1, repository: $r, commit: $c, version: $v, createdAt: $t, images: {}}')"
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
    json="$(jq --arg k "$key" --arg a "$asset" --arg s "$sum" --arg i "$id" --arg ref "$ref" \
      '.images[$k] = {asset: $a, sha256: $s, imageId: $i, ref: $ref}' <<< "$json")"
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

# Which releases move. sha-<short>, vX.Y.Z and every vX.Y.Z-<suffix>
# (rc.N, pending, rc.N-discarded, rejected, retracted) are immutable: an
# asset is only ever added, never replaced. channel-<branch> moves forward
# only, onto a commit its current one is an ancestor of (the rule
# release-images.sh's tag_may_move applies to registry branch tags).
# `latest` moves exactly when the git tag `latest` names the commit, which
# is how a rollback moves it back.
release_kind() {
  case "$1" in
    channel-*) echo channel ;;
    latest) echo latest ;;
    sha-* | v[0-9]*) echo immutable ;;
    *) die "$1 is not a release name contract v1 publishes (sha-<short>, channel-<branch>, vX.Y.Z[-...], latest); never a bare branch name" ;;
  esac
}

remote_commit_of_tag() {
  git ls-remote --tags "$remote" "refs/tags/$1" "refs/tags/$1^{}" 2>/dev/null |
    awk '{ c[$2] = $1 } END { t = "refs/tags/'"$1"'"; if ((t "^{}") in c) print c[t "^{}"]; else if (t in c) print c[t] }'
}

# may_move RELEASE COMMIT HELD: 0 if a moving release holding HELD may
# move onto COMMIT, saying why either way.
may_move() {
  local release="$1" commit="$2" held="$3" latest
  case "$(release_kind "$release")" in
    latest)
      latest="$(remote_commit_of_tag latest)"
      if [ "$latest" = "$commit" ]; then
        say "latest: the git tag latest names ${commit:0:12}; moving the release onto it"
        return 0
      fi
      say "latest: the git tag latest names ${latest:-nothing}, not ${commit:0:12}; leaving the release where it is"
      return 1
      ;;
  esac
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
    *.sha256) echo text/plain ;;
    *) echo application/gzip ;;
  esac
}

# The files a release carries, images.json last.
release_files() {
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

held_version() {
  jq -r '.version // empty' "$scratch/held.json" 2>/dev/null || true
}

publish_one() {
  local release="$1" kind commit version id held="" status name prerelease=true make_latest=false listed missing
  upload_base=""
  rm -f "$scratch/held.json"
  kind="$(release_kind "$release")"
  commit="$(jq -r .commit "$dir/images.json")"
  version="$(jq -r .version "$dir/images.json")"
  case "$release" in
    v[0-9]*.[0-9]*.[0-9]*) [[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && prerelease=false ;;
  esac

  if release_json "$release"; then
    id="$(jq -r .id "$scratch/release")"
    upload_base="$(jq -r '.upload_url | sub("\\{.*$"; "")' "$scratch/release")"
    asset_list "$id" > "$scratch/existing"
    if grep -q ' images.json$' "$scratch/existing"; then
      curl -sSL ${token:+-H "Authorization: Bearer $token"} -H 'Accept: application/octet-stream' -o "$scratch/held.json" \
        "$api_url/repos/$repo/releases/assets/$(awk '$2 == "images.json" {print $1}' "$scratch/existing")" || true
      held="$(jq -r '.commit // empty' "$scratch/held.json" 2>/dev/null || true)"
    fi
  else
    id=""
    : > "$scratch/existing"
  fi

  case "$kind" in
    immutable)
      if [ -n "$held" ] && [ "$held" != "$commit" ]; then
        die "$release is immutable and already holds ${held:0:12}, not ${commit:0:12}"
      fi
      ;;
    *)
      may_move "$release" "$commit" "$held" || return 0
      if [ "$held" = "$commit" ] && [ "$(jq -r .version "$dir/images.json")" = "$(held_version)" ]; then
        missing=""
        while read -r name; do
          grep -q " $name\$" "$scratch/existing" || missing="$missing $name"
        done < <(release_files)
        if [ -z "$missing" ]; then
          say "$release already carries every asset of ${commit:0:12} ($version); nothing to move"
          return 0
        fi
      fi
      ;;
  esac

  if [ -z "$id" ]; then
    # A release on a tag that does not exist creates the tag at
    # target_commitish; one that does (vX.Y.Z, latest) keeps it.
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
    say "created release $release"
  elif [ "$kind" = channel ]; then
    point_tag "$release" "$commit"
  fi

  if [ "$kind" = immutable ]; then
    while read -r name; do
      if grep -q " $name\$" "$scratch/existing"; then
        say "$release already has $name; an immutable release's assets are never replaced"
        continue
      fi
      upload "$id" "$name"
    done < <(release_files)
  else
    # Non-atomic by design (docs/release-assets.md): images.json goes
    # first and comes back last, so a moving release without one is
    # mid-update and a loader tries again rather than falling back.
    local manifest_id
    manifest_id="$(awk '$2 == "images.json" {print $1}' "$scratch/existing")"
    [ -n "$manifest_id" ] && delete_asset "$manifest_id"
    while read -r name; do
      [ "$name" = images.json ] && continue
      while read -r old; do
        delete_asset "$old"
      done < <(awk -v n="$name" '$2 == n {print $1}' "$scratch/existing")
      upload "$id" "$name"
    done < <(release_files)
    upload "$id" images.json
  fi

  # Fail closed: a release missing any asset is not a release.
  if [ "$dry_run" != true ]; then
    listed="$(asset_list "$id" | awk '{print $2}')"
    missing=""
    while read -r name; do
      grep -qx "$name" <<< "$listed" || missing="$missing $name"
    done < <(release_files)
    [ -z "$missing" ] || die "$release is missing$missing after publishing"
  fi
  say "published $release: ${commit:0:12} ($version)"
}

publish() {
  [ $# -gt 0 ] || die "publish needs at least one release name"
  need jq curl git
  [ -n "$token" ] || [ "$dry_run" = true ] || die "publishing needs GITHUB_TOKEN (permissions: contents: write)"
  validate_manifest "$dir"
  local release
  for release in "$@"; do
    publish_one "$release"
  done
}

# --- reading releases -------------------------------------------------------

download() {
  local release="$1" name="$2" out="$3" status
  status="$(curl -sSL -o "$out" -w '%{http_code}' ${token:+-H "Authorization: Bearer $token"} \
    "$server_url/$repo/releases/download/$release/$name" || echo 000)"
  ok "$status"
}

fetch() {
  local release="${1:?fetch needs a release name}" out="${2:-$dir}" key asset
  need curl jq sha256sum
  mkdir -p "$out"
  if ! download "$release" images.json "$out/images.json"; then
    rm -f "$out/images.json"
    case "$release" in
      channel-* | latest)
        echo "release-assets.sh: $release has no images.json: it is mid-update (or does not exist yet); try again later" >&2
        exit 75
        ;;
    esac
    die "$release has no images.json"
  fi
  for key in "${image_keys[@]}"; do
    asset="$(jq -r --arg k "$key" '.images[$k].asset // empty' "$out/images.json")"
    [ -n "$asset" ] || die "$release's images.json names no $key asset"
    download "$release" "$asset" "$out/$asset" || die "$release has no $asset"
    download "$release" "$asset.sha256" "$out/$asset.sha256" || die "$release has no $asset.sha256"
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

# source_into RELEASE: the release's assets in $dir, unless $dir already
# holds that commit's packaged assets (the release job's own, in a dry run).
source_into() {
  local release="$1" commit="$2"
  if [ -f "$dir/images.json" ] && [ "$(jq -r .commit "$dir/images.json")" = "$commit" ]; then
    validate_manifest "$dir"
    say "using the ${commit:0:12} assets already in $dir"
    return 0
  fi
  fetch "$release" "$dir" > /dev/null
  [ "$(jq -r .commit "$dir/images.json")" = "$commit" ] || die "$release holds $(jq -r .commit "$dir/images.json"), not $commit"
}

promote() {
  need git jq
  local version="${1:-}" latest commit sha_release releases
  latest="$(remote_commit_of_tag latest)"
  if [ -z "$version" ]; then
    [ -n "$latest" ] || die "the git tag latest names nothing on $remote, and no version was given"
    version="$(git ls-remote --tags "$remote" 'refs/tags/v*' |
      awk -v c="$latest" '$1 == c { t = $2; sub(/^refs\/tags\//, "", t); sub(/\^\{\}$/, "", t); print t }' |
      grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
    [ -n "$version" ] || die "no vX.Y.Z tag names ${latest:0:12}, the commit the git tag latest names"
  fi
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$version is not a vX.Y.Z release"
  commit="$(remote_commit_of_tag "$version")"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "$remote has no tag $version"
  sha_release="sha-${commit:0:12}"
  say "promoting $sha_release as $version"
  # A release promoted before its commit was published as release assets
  # has no sha- release to promote. Asked for by name, that fails; on the
  # automatic run every push makes, it is said and skipped.
  if [ -z "${1:-}" ] && ! { [ -f "$dir/images.json" ] && [ "$(jq -r .commit "$dir/images.json")" = "$commit" ]; } &&
      ! download "$sha_release" images.json "$scratch/probe.json"; then
    echo "::warning::release-assets.sh: $version (${commit:0:12}) has no $sha_release release: it predates release assets, so there is nothing to promote as $version and latest"
    return 0
  fi
  source_into "$sha_release" "$commit"
  rewrite_manifest "$dir" "$dir" "$version"
  validate_manifest "$dir"
  releases=("$version")
  [ "$latest" = "$commit" ] && releases+=(latest)
  publish "${releases[@]}"
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
    fetch "$from" "$dir" > /dev/null
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
    v*-rc.*-discarded) train_phase discard-rc "${tag%-discarded}" ;;
    v*-rc.*) train_phase rc "$tag" ;;
    v*-pending) train_phase pending "${tag%-pending}" ;;
    v*-retracted) train_phase retract "${tag%-retracted}" ;;
    v[0-9]*.[0-9]*.[0-9]* | latest) promote "${tag#latest}" ;;
    *) die "$tag is not a tag the release train publishes" ;;
  esac
}

case "$cmd" in
  package) package ;;
  rc | pending | discard-rc | discard-pending | retract) train_phase "$cmd" "$@" ;;
  train-tag) train_tag "$@" ;;
  validate) validate_manifest "${1:-$dir}" && say "${1:-$dir}/images.json meets release-asset contract v1" ;;
  publish) publish "$@" ;;
  promote) promote "$@" ;;
  retag) retag "$@" ;;
  fetch) fetch "$@" ;;
  load) load "$@" ;;
  mirror) mirror "$@" ;;
  *)
    sed -n '17,51p' "$0" >&2
    exit 2
    ;;
esac
