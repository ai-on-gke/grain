#!/usr/bin/env bash
# Build every Granule in this directory with granule, and check that the
# image each one produces is the machine the file asked for.
#
# The files here are the specification's own worked examples
# (docs/granulefile.md). `go test ./pkg/granulefile` checks that every
# one of them parses, validates and compiles into the build granule
# would run -- that needs no docker and runs in CI. This script is the
# other half: it runs those builds, and then boots what they produced.
#
# What each example directory may carry:
#
#   Granule             the file itself (required)
#   <context files>     whatever its COPY lines name
#   build-args          KEY=VALUE lines, passed as -build-arg
#   expect-dockerfile   substrings the compiled build must contain
#   expect-unit         substrings one generated support file must contain
#   expect-boot         substrings the guest phase must contain
#   expect-build-error  the build must fail, saying this
#   needs-kvm           the build boots a guest itself (a VM-stage RUN,
#                       a VM SNAPSHOT), so without /dev/kvm there is
#                       nothing of this example to run
#   image-checks        shell run on the host with $IMAGE set
#   guest-checks        `granule guest verify` checks, run inside the
#                       booted guest -- these need /dev/kvm
#
# The rootfs and machine references the examples name are the published
# ones, which is what the specification says; --rootfs and --vm-image
# point them at images built from this checkout instead, which is what a
# run before those images are published has to do. With no --vm-image,
# the examples that start from a machine are given one this run packs
# for itself: what their own files name there is an illustration, not an
# image anybody publishes.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

granule=""
rootfs=""
vm_image=""
kontur_image=""
keep=false
only=""
tag_prefix="granule-example"

usage() {
  cat <<'USAGE'
usage: test/granulefiles/run.sh [flags]

Builds every example Granule with granule and verifies the result.

  -granule <path>      the granule binary to build with (default: built
                       from this checkout into a temporary directory)
  -rootfs <ref>        guest rootfs image the packing and wrapping examples
                       build on
                       (default: the published reference they name)
  -vm-image <ref>      machine the unpacking and deriving examples start
                       from (default: one packed from the guest rootfs
                       above, since the reference they name is the
                       specification's illustration and is published by
                       nobody)
  -kontur-image <ref>  granule image supplying the VMM half (default:
                       granule:latest, as the builds themselves default)
  -only <name>         run one example, by directory name or a prefix of it
  -keep                leave the built images behind instead of removing
                       each one once it has been checked
  -h, --help           this
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    -granule) granule="$2"; shift 2 ;;
    -rootfs) rootfs="$2"; shift 2 ;;
    -vm-image) vm_image="$2"; shift 2 ;;
    -kontur-image) kontur_image="$2"; shift 2 ;;
    -only) only="$2"; shift 2 ;;
    -keep) keep=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! command -v docker >/dev/null 2>&1; then
  echo "no docker: these examples are built by docker, and there is nothing to run them with" >&2
  exit 1
fi

if [ -z "$granule" ]; then
  granule="$(mktemp -d)/granule"
  echo "== building the granule CLI"
  ( cd "$repo" && go build -o "$granule" ./cmd/granule )
fi

# "granule doctor" decides, rather than a [ -w /dev/kvm ] here: it knows
# that a rootful docker can pass a /dev/kvm this user cannot open, and it
# says why when the answer is no.
kvm=false
if kvm_report="$("$granule" doctor -only kvm,tun 2>&1)"; then
  kvm=true
else
  echo "== no usable /dev/kvm: the guest checks will be skipped, the builds will not"
  echo "$kvm_report" | sed 's/^/   /'
fi

passed=0
skipped=0
failed=()

# A build's own log is megabytes of buildkit; it is worth a reader's
# screen only when the build failed, or when a check on what it produced
# did.
log="$(mktemp -d)/build.log"

check_substrings() {
  # check_substrings <file of wanted substrings> <file to look in> <what it is>
  local wanted="$1" haystack="$2" what="$3" line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    if ! grep -qF -- "$line" "$haystack"; then
      echo "   $what does not contain: $line" >&2
      return 1
    fi
  done < "$wanted"
  return 0
}

# The machine the unpacking and composing examples start from. Their own
# files name ghcr.io/example/my-vm:latest, which is the reference the
# specification prints and not an image anybody publishes -- so a run
# given no -vm-image builds one instead of failing on a pull: a bare
# VM PACK of the guest rootfs this run is already using, which is a
# machine in every respect those examples read. Built once, on the first
# example that needs it, since a pack is a minute and most runs need
# none.
built_vm_image=""
ensure_vm_image() {
  if [ -n "$vm_image" ]; then
    return 0
  fi
  local dir tag
  tag="$tag_prefix-source-vm:test"
  echo "   no -vm-image: building $tag for the examples that start from a machine"
  dir="$(mktemp -d)"
  {
    echo 'ARG ROOTFS=ghcr.io/gke-labs/granule-guest-rootfs:debian12'
    # shellcheck disable=SC2016  # granule expands this, not the shell
    echo 'FROM ${ROOTFS}'
    echo 'VM PACK'
  } > "$dir/Granule"
  local build=(granulefile build -f "$dir/Granule" -t "$tag")
  [ -n "$kontur_image" ] && build+=(-kontur-image "$kontur_image")
  [ -n "$rootfs" ] && build+=(-guest-image "$rootfs" -build-arg "ROOTFS=$rootfs")
  if ! "$granule" "${build[@]}" "$dir" > "$log" 2>&1; then
    echo "   building a machine for it to start from failed:" >&2
    tail -60 "$log" >&2
    return 1
  fi
  vm_image="$tag"
  built_vm_image="$tag"
  return 0
}

for dir in "$here"/*/; do
  name="$(basename "$dir")"
  [ -f "$dir/Granule" ] || continue
  if [ -n "$only" ]; then
    case "$name" in
      "$only"|"$only"-*) ;;
      *) continue ;;
    esac
  fi

  tag="$tag_prefix-${name#*-}:test"
  echo "== $name"

  # An example whose build boots a guest -- a RUN in a VM stage, a
  # VM SNAPSHOT -- has no offline half to fall back on: granule packs
  # the machine, boots it, runs the stage in it and commits what comes
  # out. Without /dev/kvm that is not a build that produces less, it is
  # a build that cannot start.
  if [ -f "$dir/needs-kvm" ] && [ "$kvm" != true ]; then
    echo "   skipped (no /dev/kvm, and this example's build boots a guest)"
    skipped=$((skipped + 1))
    continue
  fi

  # A file that starts from a machine needs one to exist first.
  if grep -qE '^ARG (VM_IMAGE|SOURCE_VM|BASE_VM)=' "$dir/Granule"; then
    if ! ensure_vm_image; then
      failed+=("$name")
      continue
    fi
  fi

  args=(granulefile build -f "$dir/Granule" -t "$tag")
  [ -n "$kontur_image" ] && args+=(-kontur-image "$kontur_image")
  # The guest rootfs a VM WRAP builds its machine out of is granule's
  # own rather than anything the file names, so it is a flag of the
  # build rather than a build argument.
  [ -n "$rootfs" ] && args+=(-guest-image "$rootfs")

  # The references the examples name, pointed at whatever this run
  # holds. Only passed where the file declares the ARG: docker warns
  # about a build argument nothing consumed, and the warning would be
  # this script's rather than the example's.
  if [ -n "$rootfs" ] && grep -q '^ARG ROOTFS=' "$dir/Granule"; then
    args+=(-build-arg "ROOTFS=$rootfs")
  fi
  if [ -n "$vm_image" ]; then
    grep -q '^ARG VM_IMAGE=' "$dir/Granule" && args+=(-build-arg "VM_IMAGE=$vm_image")
    grep -q '^ARG SOURCE_VM=' "$dir/Granule" && args+=(-build-arg "SOURCE_VM=$vm_image")
    grep -q '^ARG BASE_VM=' "$dir/Granule" && args+=(-build-arg "BASE_VM=$vm_image")
  fi
  if [ -f "$dir/build-args" ]; then
    while IFS= read -r kv; do
      [ -n "$kv" ] || continue
      case "$kv" in \#*) continue ;; esac
      args+=(-build-arg "$kv")
    done < "$dir/build-args"
  fi

  # The compiled build, before anything is built from it: what the file
  # says has to survive into it, or the image would be right by accident.
  if [ -f "$dir/expect-dockerfile" ]; then
    if ! "$granule" "${args[@]}" -print "$dir" > "$log" 2>&1; then
      echo "   compiling the file failed:" >&2
      cat "$log" >&2
      failed+=("$name")
      continue
    fi
    if ! check_substrings "$dir/expect-dockerfile" "$log" "the compiled build"; then
      cat "$log" >&2
      failed+=("$name")
      continue
    fi
  fi

  if [ -f "$dir/expect-build-error" ]; then
    # A construct the specification defines and this tree does not build
    # yet. It has to be refused, by name, rather than dropped: an
    # instruction that is silently discarded is a machine that comes up
    # without the configuration the file asked for.
    if "$granule" "${args[@]}" "$dir" > "$log" 2>&1; then
      echo "   the build succeeded, and this example must be refused" >&2
      cat "$log" >&2
      failed+=("$name")
      continue
    fi
    if ! check_substrings "$dir/expect-build-error" "$log" "the refusal"; then
      cat "$log" >&2
      failed+=("$name")
      continue
    fi
    echo "   refused, as it must be: $(tr '\n' ' ' < "$dir/expect-build-error")"
    passed=$((passed + 1))
    continue
  fi

  if ! "$granule" "${args[@]}" "$dir" > "$log" 2>&1; then
    echo "   build failed:" >&2
    tail -60 "$log" >&2
    failed+=("$name")
    continue
  fi
  echo "   built $tag"

  ok=true
  if [ -f "$dir/image-checks" ]; then
    if IMAGE="$tag" bash "$dir/image-checks"; then
      echo "   image checks passed"
    else
      echo "   image checks failed" >&2
      ok=false
    fi
  fi

  if [ -f "$dir/guest-checks" ] && [ "$ok" = true ]; then
    if [ "$kvm" = true ]; then
      verify=(guest verify -image "$tag" -checks "$dir/guest-checks" -no-cache)
      [ -n "$kontur_image" ] && verify+=(-kontur-image "$kontur_image")
      if "$granule" "${verify[@]}"; then
        echo "   guest checks passed"
      else
        echo "   guest checks failed" >&2
        ok=false
      fi
    else
      echo "   guest checks skipped (no /dev/kvm)"
      skipped=$((skipped + 1))
    fi
  fi

  if [ "$keep" = false ]; then
    # Removed as we go: each machine carries its own guest disk, and a
    # dozen of them do not fit on the disk of the machine that built
    # them.
    docker rmi -f "$tag" >/dev/null 2>&1 || true
  fi

  if [ "$ok" = true ]; then
    passed=$((passed + 1))
  else
    failed+=("$name")
  fi
done

if [ -n "$built_vm_image" ] && [ "$keep" = false ]; then
  docker rmi -f "$built_vm_image" >/dev/null 2>&1 || true
fi

echo
echo "== $passed examples passed, ${#failed[@]} failed, $skipped guest check sets skipped"
if [ ${#failed[@]} -gt 0 ]; then
  printf '   failed: %s\n' "${failed[*]}" >&2
  exit 1
fi
