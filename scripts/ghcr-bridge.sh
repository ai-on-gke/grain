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

# The one-time GHCR bridge (docs/release-assets.md, "The GHCR bridge").
#
# OSS installs from before the Docker Hub move run builds that only ever
# poll ghcr.io/bwsalmon/grain/grain:<channel>. This copies one build that
# carries the release-asset loader there, registry-side and by digest, so
# those installs upgrade onto it once and from then on read release assets
# (with docker.io/grainai/grain as the fallback). It publishes nothing new:
# every image it writes is already on Docker Hub, byte for byte.
#
#   ghcr-bridge.sh main                  # :main, :sha-<7> from docker.io/grainai/grain:main
#   ghcr-bridge.sh latest vX.Y.Z         # :latest, :vX.Y.Z, :candidate, :rc from that release
#
# The daemon image goes to ghcr.io/bwsalmon/grain/grain; the sandbox images
# its stamp names go to the names the old flow used, guest and k8s-sandbox,
# under the same tags. Nothing is deleted.
#
# Run it from a clone of ai-on-gke/grain with origin's tags fetched, after
# `docker login ghcr.io -u bwsalmon` with a classic PAT holding
# write:packages. DRY_RUN=1 makes every check and prints the copies without
# writing anything (no login needed).
#
# Refuses, before writing anything:
#   - `latest` from anything but a promoted vX.Y.Z: the git tag must exist,
#     contain the loader (LOADER_COMMIT), and docker.io/grainai/grain:vX.Y.Z
#     must be the digest docker.io/grainai/grain:latest names -- never a
#     main build;
#   - `main` from a build that does not contain the loader;
#   - a daemon whose org.opencontainers.image.revision is not the commit
#     the tag says, whose -flag-names lacks -upgrade-image-source, or whose
#     stamped guest is not $SRC/sandbox:sha-<7> at the digest it pins;
#   - any copy whose digest on GHCR differs from its Docker Hub source.
set -euo pipefail

SRC="${GHCR_BRIDGE_SRC:-docker.io/grainai}"
DST="${GHCR_BRIDGE_DST:-ghcr.io/bwsalmon/grain}"
# The merge of task 2951 (PR #661): the release-asset loader.
LOADER_COMMIT="${GHCR_BRIDGE_LOADER_COMMIT:-23c3045d}"
DRY_RUN="${DRY_RUN:-}"

die() { echo "ghcr-bridge.sh: $*" >&2; exit 1; }
say() { echo "ghcr-bridge.sh: $*"; }

usage() {
  sed -n '/^#   ghcr-bridge.sh/p' "$0" | sed 's/^#   /usage: /' >&2
  exit 2
}

# digest <ref>: the manifest (or index) digest a tag names now.
digest() {
  docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])'
}

# revision <ref>: the org.opencontainers.image.revision label of linux/amd64.
revision() {
  docker buildx imagetools inspect "$1" --format '{{json .Image}}' |
    python3 -c '
import json, sys
d = json.load(sys.stdin)
if "config" not in d:
    d = d.get("linux/amd64") or next(iter(d.values()))
print((d.get("config") or {}).get("Labels", {}).get("org.opencontainers.image.revision", ""))'
}


# copy <src-ref@digest> <dst-repo> <tag>...: registry-side, digest-preserving.
copy() {
  local src="$1" dst="$2" want got tag args=()
  shift 2
  want="${src##*@}"
  for tag in "$@"; do args+=(--tag "$dst:$tag"); done
  if [ -n "$DRY_RUN" ]; then
    say "would copy $src -> $(printf '%s ' "${@/#/$dst:}")"
    return 0
  fi
  docker buildx imagetools create "${args[@]}" "$src" >/dev/null
  for tag in "$@"; do
    got="$(digest "$dst:$tag")"
    [ "$got" = "$want" ] || die "$dst:$tag is $got after the copy, not $want"
    say "$dst:$tag = $got"
  done
}

[ $# -ge 1 ] || usage
channel="$1"
command -v docker >/dev/null || die "docker is not installed"
docker buildx version >/dev/null 2>&1 || die "docker buildx is not available"
command -v python3 >/dev/null || die "python3 is not installed"
git rev-parse --git-dir >/dev/null 2>&1 || die "run this from a clone of ai-on-gke/grain"
git cat-file -e "$LOADER_COMMIT^{commit}" 2>/dev/null ||
  die "$LOADER_COMMIT (the release-asset loader) is not in this clone; git fetch origin --tags"

case "$channel" in
  main)
    [ $# -eq 1 ] || usage
    src_tag=main
    ;;
  latest)
    [ $# -eq 2 ] || usage
    version="$2"
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "latest needs a released vX.Y.Z, not '$version'"
    src_tag="$version"
    ;;
  *) usage ;;
esac

daemon_digest="$(digest "$SRC/grain:$src_tag")" || die "cannot read $SRC/grain:$src_tag"
commit="$(revision "$SRC/grain@$daemon_digest")"
[ -n "$commit" ] || die "$SRC/grain:$src_tag names no revision"
git cat-file -e "$commit^{commit}" 2>/dev/null || die "$SRC/grain:$src_tag is $commit, which this clone does not have; git fetch origin"
git merge-base --is-ancestor "$LOADER_COMMIT" "$commit" ||
  die "$SRC/grain:$src_tag is $commit, which does not contain the release-asset loader ($LOADER_COMMIT)"
short="${commit:0:7}"

if [ "$channel" = latest ]; then
  tagged="$(git rev-parse "$version^{commit}" 2>/dev/null)" || die "no git tag $version"
  [ "$tagged" = "$commit" ] || die "$SRC/grain:$version is $commit, but git tag $version is $tagged"
  latest_digest="$(digest "$SRC/grain:latest")"
  [ "$latest_digest" = "$daemon_digest" ] ||
    die "$SRC/grain:latest is $latest_digest, not $version ($daemon_digest): bridge latest only from the promoted release"
  tags=(latest "$version" candidate rc)
else
  tags=(main "sha-$short")
fi

# The daemon must know -upgrade-image-source, or it is not the loader.
docker pull --quiet "$SRC/grain@$daemon_digest" >/dev/null
docker run --rm "$SRC/grain@$daemon_digest" daemon -flag-names | grep -q '^upgrade-image-source\( \|$\)' ||
  die "$SRC/grain@$daemon_digest has no -upgrade-image-source flag"

# Sandbox images carry no revision label: the guest is the digest the
# daemon is stamped with, and sandbox-k8s the same CI run's sha-<7>.
stamp="$(docker run --rm "$SRC/grain@$daemon_digest" sandbox-image)"
case "$stamp" in
  "$SRC/sandbox:sha-$short@sha256:"*) sandbox_digest="${stamp##*@}" ;;
  *) die "$SRC/grain@$daemon_digest is stamped with sandbox image '$stamp', not $SRC/sandbox:sha-$short@..." ;;
esac
[ "$(digest "$SRC/sandbox:sha-$short")" = "$sandbox_digest" ] ||
  die "$SRC/sandbox:sha-$short no longer names $sandbox_digest, the digest $SRC/grain@$daemon_digest is stamped with"
k8s_digest="$(digest "$SRC/sandbox-k8s:sha-$short")" || die "cannot read $SRC/sandbox-k8s:sha-$short"

say "bridging $channel from $commit ($SRC/grain:$src_tag) as: ${tags[*]}"
copy "$SRC/sandbox@$sandbox_digest" "$DST/guest" "${tags[@]}"
copy "$SRC/sandbox-k8s@$k8s_digest" "$DST/k8s-sandbox" "${tags[@]}"
# The daemon last: an old install that sees it pulls the sandbox it names.
copy "$SRC/grain@$daemon_digest" "$DST/grain" "${tags[@]}"

say "record in docs/release-assets.md (\"Last GHCR publish\"):"
bt='`'
printf "| %s | $bt%s$bt | %s | $bt%s$bt | $bt%s$bt | $bt%s$bt |\n" \
  "$(date -u +%Y-%m-%d)" "$channel" "$(printf "$bt:%s$bt " "${tags[@]}")" "${commit:0:12}" "$daemon_digest" "$sandbox_digest / $k8s_digest"
