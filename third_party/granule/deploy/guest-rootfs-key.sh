#!/usr/bin/env bash
# The content key of a granule guest rootfs: a short hash of everything
# that decides what is inside it, and of nothing else.
#
# It exists because the published rootfs used to be tagged
# :<variant>-<commit>, which minted a new immutable tag on every commit
# whether or not the operating system in it had changed -- and since the
# rootfs stages build an OS and nothing of granule's (granule's own
# guest-side half goes on at pack time; see the Dockerfile's "guest-parts"
# stage), almost none of those commits changed it. A tag named after the
# recipe instead is one a consumer can pin and reason about: two tags with
# the same key were built from the same instructions, and a new key means
# somebody changed the OS.
#
# What goes into it:
#
#   - the values of the build args the rootfs stages read, resolved
#     against the Dockerfile's own defaults so that a default changing
#     changes the key, and
#   - the text of the Dockerfile's guest rootfs recipe -- the region
#     between its ">>> guest rootfs recipe" and "<<< guest rootfs recipe"
#     markers -- with comments and blank lines removed, exactly as the
#     builder removes them, so that rewording a comment does not mint a
#     new rootfs.
#
# What deliberately does not:
#
#   - the commit, the branch, the tree's cleanliness, and everything else
#     in this repository. That is the whole point.
#   - what the distribution archives happen to contain today. The base
#     images are pinned by digest, so bumping one is a recipe change and
#     moves the key, but a `debootstrap` of bookworm (or an `apk add`) is
#     named by the recipe, not pinned by it, so two builds a month apart
#     under one key differ by whatever Debian shipped in between. The key
#     names the recipe, and the publish refuses to move a key tag that
#     already exists (see deploy/publish-images.sh), so what is pinned
#     stays what was pinned. When the reason to rebuild is exactly that
#     upstream moved -- a point release, a security update -- set
#     ROOTFS_REVISION to something new (a date reads well) and the key
#     moves with it.
#
# The key is conservative rather than minimal: it covers both distro
# stages' text and all the rootfs build args, so a change that only the
# Alpine variant cares about also moves the Debian key. That costs a
# rebuild of a rootfs that would have come out the same, and it is the
# side to err on -- the other error is a stale pin, which is silent.
#
# usage: deploy/guest-rootfs-key.sh [--manifest] [NAME=VALUE ...]
#
#   NAME=VALUE   a build arg this rootfs is built with, in the same form
#                `docker build --build-arg` takes. Args that do not reach
#                the rootfs (GUEST_DISK_EXTRA_MB and friends) may be
#                passed and are ignored, so a caller can hand over the
#                whole set it builds with; an arg this script has never
#                heard of is an error rather than an omission.
#   --manifest   print the text that is hashed, not the hash, which is
#                what to look at when a key moved and nobody knows why.
#
# Environment:
#   ROOTFS_REVISION   an arbitrary string mixed into the key, empty by
#                     default. The way to mint a new rootfs when the
#                     recipe has not changed but what it builds has.
#   DOCKERFILE        the Dockerfile to read (default: this repo's own).
set -euo pipefail

manifest_only=false

# The build args the rootfs stages read. A new ARG down there belongs in
# this list; see the marker comment in the Dockerfile.
ROOTFS_INPUTS=(
  GUEST_ALPINE_DIGEST
  GUEST_ALPINE_VERSION
  GUEST_DISTRO
  GUEST_KERNEL_PACKAGE
  GUEST_READY_FILE
  GUEST_SETUP_SCRIPT
  GUEST_SUITE
)

# Build args that exist and do not reach the rootfs: the disk the rootfs
# is packed into, the runtime image, the host-side kernel. Listed rather
# than ignored wholesale so that an arg belonging to neither list stops
# this script -- which is what makes "the key covers every input" a
# property somebody maintains rather than a claim in a comment.
NOT_ROOTFS_INPUTS=(
  CLOUD_HYPERVISOR_VERSION
  GITHUB_PROXY
  GO_DIGEST
  GO_VERSION
  GRANULE_KERNEL_SHA256
  GRANULE_KERNEL_VERSION
  GUEST_DISK_EXTRA_MB
  GUEST_DISK_MODE
  GUEST_DNS
  TARGETARCH
)

usage() {
  sed -n '/^# usage:/,/^set -euo/p' "${BASH_SOURCE[0]}" | sed -e '$d' -e 's/^# \{0,1\}//' >&2
  exit 2
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
dockerfile=${DOCKERFILE:-$script_dir/../Dockerfile}
revision=${ROOTFS_REVISION:-}

command -v sha256sum >/dev/null || { echo "missing required command: sha256sum" >&2; exit 1; }

declare -A given=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --manifest) manifest_only=true ;;
    -h|--help) usage ;;
    -*) echo "unknown option: $1" >&2; usage ;;
    *=*)
      name=${1%%=*}
      case " ${ROOTFS_INPUTS[*]} " in
        *" $name "*) given[$name]=${1#*=} ;;
        *)
          case " ${NOT_ROOTFS_INPUTS[*]} " in
            *" $name "*) ;;  # a real arg, and not one of the rootfs's
            *)
              echo "$name is not a build arg this script knows about." >&2
              echo "If it changes what is inside the guest rootfs, add it to ROOTFS_INPUTS in ${BASH_SOURCE[0]}; if it does not, add it to NOT_ROOTFS_INPUTS." >&2
              exit 1
              ;;
          esac
          ;;
      esac
      ;;
    *) echo "expected NAME=VALUE, got: $1" >&2; usage ;;
  esac
  shift
done

[[ -f $dockerfile ]] || { echo "no Dockerfile at $dockerfile" >&2; exit 1; }

# An arg's default is part of the recipe just as much as the stages'
# text: a Dockerfile that changes GUEST_SUITE's default builds a
# different OS for every caller who does not override it.
arg_default() {
  local name=$1 line value
  line=$(grep -m1 -E "^ARG[[:space:]]+${name}=" "$dockerfile" || true)
  [[ -n $line ]] || {
    echo "$name is listed as a rootfs input but $dockerfile declares no default for it" >&2
    exit 1
  }
  value=${line#*=}
  # ARG FOO="" and ARG FOO=bar are both written in this file; strip one
  # layer of quoting so the two forms of an empty value hash alike.
  case $value in
    \"*\") value=${value:1:${#value}-2} ;;
    \'*\') value=${value:1:${#value}-2} ;;
  esac
  printf '%s' "$value"
}

# The recipe's own text. The markers are the contract with whoever edits
# the Dockerfile, so a missing one is a hard failure: it means the region
# was renamed or removed, and a key computed over whatever awk found
# instead would be silently wrong.
region=$(awk '
  /^# >>> guest rootfs recipe/ { inside = 1; seen_start = 1; next }
  /^# <<< guest rootfs recipe/ { inside = 0; seen_end = 1; next }
  inside { print }
  END { if (!seen_start || !seen_end) exit 1 }
' "$dockerfile") || {
  echo "$dockerfile has no '# >>> guest rootfs recipe' / '# <<< guest rootfs recipe' pair to hash" >&2
  echo "one of the markers was removed or renamed; put it back, or this key names nothing." >&2
  exit 1
}
[[ -n $region ]] || { echo "the guest rootfs recipe region in $dockerfile is empty" >&2; exit 1; }

# Comments are dropped before hashing so that this repository's habit of
# explaining itself at length does not republish an operating system. A
# Dockerfile heredoc is the one construct where a line starting with '#'
# is content rather than a comment, so refuse to guess at one instead of
# hashing something the builder would not agree with.
if grep -qE '<<-?[[:space:]]*[A-Za-z"'"'"']' <<<"$region"; then
  echo "the guest rootfs recipe contains what looks like a heredoc, where stripping '#' lines is not safe." >&2
  echo "rewrite it without one, or teach $0 to hash the region verbatim." >&2
  exit 1
fi
recipe=$(grep -vE '^[[:space:]]*(#.*)?$' <<<"$region" | sed -e 's/[[:space:]]*$//')
recipe_hash=$(printf '%s\n' "$recipe" | sha256sum | cut -d' ' -f1)

# Values reach the manifest escaped: GUEST_SETUP_SCRIPT is routinely a
# whole shell script, and a newline in a NAME=VALUE line would let two
# different sets of args produce one manifest.
escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' | awk '{ printf "%s%s", sep, $0; sep = "\\n" } END { print "" }'
}

write_manifest() {
  # Versioned so that a later change to what is hashed -- a new input, a
  # different canonical form -- announces itself as a new set of keys
  # rather than as two spellings of one.
  echo "granule guest rootfs content key v1"
  echo "revision=$(escape "$revision")"
  # The inputs are printed in the order they are listed above, which is
  # alphabetical, so the manifest does not depend on the order a caller
  # happened to pass its --build-args in.
  local name value
  for name in "${ROOTFS_INPUTS[@]}"; do
    if [[ -v given[$name] ]]; then
      value=${given[$name]}
    else
      value=$(arg_default "$name")
    fi
    echo "$name=$(escape "$value")"
  done
  echo "recipe=sha256:$recipe_hash"
}
manifest=$(write_manifest)

if [[ $manifest_only == true ]]; then
  printf '%s\n' "$manifest"
  exit 0
fi

# Twelve hex characters: enough that a collision is not a thing that
# happens, short enough to read out of a tag and type into a `docker
# pull`. Nothing here is a security boundary -- the key says which recipe
# built an image, and the registry says what the bytes are.
printf '%s\n' "$manifest" | sha256sum | cut -c1-12
