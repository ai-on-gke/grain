#!/usr/bin/env bash
# The base images granule builds on, pinned by digest, and the tooling
# that keeps them pinned.
#
# Every FROM in the files below names its image as tag@sha256:<digest>:
# the tag says what it is, the digest is what is pulled. Without the
# digest, anyone who can move the tag -- Docker Hub, or a registry mirror
# in the path -- chooses the Go toolchain that builds granule's binaries
# and the userland every guest is built and packed in. The same goes for
# guestpack.DefaultPackerImage, which every `granule guest pack` and
# Granulefile build runs its packing in.
#
# usage: deploy/base-images.sh lint|check|update|digest REF
#
#   lint         fail if a FROM in a pinned file (or DefaultPackerImage)
#                names a registry image without a digest. Offline; CI
#                runs it on every change.
#   check        resolve each pinned tag on its registry now and report
#                the ones whose tag has moved off the pinned digest.
#                Exits 1 if any has.
#   update       as check, then rewrite every moved digest in place. The
#                result is an ordinary change to review and land: a
#                digest bump moves the guest rootfs key (see
#                deploy/guest-rootfs-key.sh), so it republishes the rootfs.
#   digest REF   print the digest REF resolves to, e.g. to pass with a
#                GO_VERSION or GUEST_ALPINE_VERSION override (see the top
#                of the Dockerfile).
#
# Bumping is a deliberate, reviewed step rather than something a robot
# lands: run `deploy/base-images.sh update`, read the diff, open a pull
# request. The weekly "base images" workflow (.grain/workflows/
# base-images.yml) runs `check` so that a tag that has moved -- usually a
# Debian or Alpine security rebuild -- is noticed within a week.
#
# check, update and digest need `docker buildx imagetools` and registry
# access; lint needs neither.
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)

# The Dockerfiles whose every FROM must be pinned. Not
# deploy/github-runner-images/Dockerfile, whose only FROM is the granule
# runtime image build.sh has just built locally.
DOCKERFILES=(
  Dockerfile
  benchmarks/gke/assets.Dockerfile
)
# Go sources carrying a pinned image reference as a string constant.
GO_PINS=(
  pkg/guestpack/guestpack.go
)

usage() {
  sed -n '/^# usage:/,/^# access;/p' "${BASH_SOURCE[0]}" | sed -e 's/^# \{0,1\}//' >&2
  exit 2
}

digest_re='@sha256:[0-9a-f]{64}$'

# from_refs FILE prints "LINE<TAB>REF" for each FROM in FILE that names a
# registry image, with ${ARG} and $ARG expanded against the defaults the
# file declares, and FROM scratch and FROM <earlier stage> left out.
from_refs() {
  awk '
    function expand(s,    out, name, m) {
      out = ""
      while (match(s, /\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*/)) {
        name = substr(s, RSTART, RLENGTH)
        gsub(/[${}]/, "", name)
        out = out substr(s, 1, RSTART - 1) ((name in arg) ? arg[name] : "${" name "}")
        s = substr(s, RSTART + RLENGTH)
      }
      return out s
    }
    toupper($1) == "ARG" {
      for (i = 2; i <= NF; i++) {
        if (split($i, kv, "=") >= 2 && !(kv[1] in arg)) {
          v = substr($i, length(kv[1]) + 2)
          gsub(/^"|"$/, "", v)
          arg[kv[1]] = v
        }
      }
      next
    }
    toupper($1) == "FROM" {
      i = 2
      while ($i ~ /^--/) i++
      ref = expand($i)
      if (toupper($(i + 1)) == "AS") stage[tolower($(i + 2))] = 1
      if (ref == "scratch" || (tolower(ref) in stage)) next
      printf "%d\t%s\n", NR, ref
    }
  ' "$1"
}

# go_refs FILE prints "LINE<TAB>REF" for each image constant in FILE.
go_refs() {
  grep -nE '^const [A-Za-z]*Image = "' "$1" | sed -E 's/^([0-9]+):[^"]*"([^"]*)".*/\1\t\2/'
}

# pins prints "FILE:LINE<TAB>REF" for every pinned reference.
pins() {
  local f
  for f in "${DOCKERFILES[@]}"; do from_refs "$repo_root/$f" | sed "s|^|$f:|"; done
  for f in "${GO_PINS[@]}"; do go_refs "$repo_root/$f" | sed "s|^|$f:|"; done
}

lint() {
  local bad=0 where ref
  while IFS=$'\t' read -r where ref; do
    if [[ ! $ref =~ $digest_re ]]; then
      echo "$where: $ref is not pinned by digest; name it as tag@sha256:<digest> (deploy/base-images.sh digest $ref)" >&2
      bad=1
    fi
  done < <(pins)
  if [[ $bad == 0 ]]; then echo "every base image is pinned by digest"; fi
  return "$bad"
}

resolve() {
  # Read to the end rather than exiting at the first match: an early exit
  # would leave docker writing into a closed pipe, which pipefail reports.
  docker buildx imagetools inspect "$1" </dev/null | awk '$1 == "Digest:" && d == "" { d = $2 } END { print d }'
}

# check_or_update prints each moved pin as "OLD NEW TAG"; with update=true
# it also rewrites OLD to NEW in every pinned file. A digest names one
# image wherever it appears, so a plain substitution moves every copy of
# it -- the Dockerfile's four Debian stages and DefaultPackerImage
# together, and an ARG default along with the FROM that reads it.
check_or_update() {
  local update=$1 moved=0 where ref tag old new f
  declare -A seen=()
  lint >/dev/null
  while IFS=$'\t' read -r where ref; do
    [[ -z ${seen[$ref]:-} ]] || continue
    seen[$ref]=1
    tag=${ref%@*}
    old=${ref##*@}
    new=$(resolve "$tag")
    [[ $new =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "could not resolve $tag" >&2; exit 1; }
    if [[ $new == "$old" ]]; then
      echo "$tag: up to date"
      continue
    fi
    moved=1
    echo "$tag: $old -> $new"
    if [[ $update == true ]]; then
      for f in "${DOCKERFILES[@]}" "${GO_PINS[@]}"; do
        sed -i "s/$old/$new/g" "$repo_root/$f"
      done
    fi
  done < <(pins)
  if [[ $update == true ]]; then return 0; fi
  return "$moved"
}

case ${1:-} in
  lint) [[ $# -eq 1 ]] || usage; lint ;;
  check) [[ $# -eq 1 ]] || usage; check_or_update false ;;
  update) [[ $# -eq 1 ]] || usage; check_or_update true ;;
  digest) [[ $# -eq 2 ]] || usage; resolve "$2" ;;
  *) usage ;;
esac
