#!/usr/bin/env bash
# granule-guest-probe.sh -- write down what a sandbox guest image actually
# contains, in a form two of them can be diffed by.
#
# It exists for one comparison: the guest .github/workflows/build-artifacts.yml
# serves from its cache against the guest a build from scratch produces.
# That workflow keys its cache on `git rev-parse` over the paths
# scripts/granule/guest-inputs.sh names, so a commit touching none of them
# republishes an image an earlier commit built. tests/deploy checks that
# list against what the build scripts read, which catches an input that is
# a path in this repository and cannot catch one that is not: an upstream
# package version, a base image tag resolved at build time, a default that
# changed inside a tool the Granulefile's RUN steps shell out to. If one of those
# moves, the hash does not. This is what
# .github/workflows/nightly.yml's guest-cache-drift job compares to find
# out whether it did.
#
# Comparing the two images bit for bit is not the point and would not
# work: they are 15GB of ext4 built minutes apart, so every timestamp,
# every inode order and every log differs while nothing that matters does.
# What is compared instead is what a stale guest would get *wrong*, split
# into two files because the two halves mean opposite things:
#
#   pinned   what this repository decides, so a difference is a bug: the
#            Go toolchain go.mod names, the Node version the Granulefile
#            pins, the npm wrapper and both tools' settings, kind, the
#            set of Debian packages installed, docker answering, the
#            "debian" account's groups, the units and files the Granulefile
#            writes, and the module and npm caches' contents. All of that
#            is a function of the eight paths the cache key covers, so two
#            guests built from the same key have to agree on every line of
#            it. A difference means an input escaped the key.
#   drift    what the guest picked up from outside this repository: the
#            Debian package versions, the guest kernel, the base image's
#            own userland, and the granule and cloud-hypervisor binaries in
#            the image around the guest. These are *expected* to differ on
#            a cached guest, and deliberately so -- guest-inputs.sh's own
#            header argues for freezing the outside world between changes
#            to those paths -- so this half is a measure of how stale the
#            cache has got, not a verdict on it.
#
# Reading the guest means booting it: the image is a granule image whose
# guest disk is an ext4 filesystem inside it, and nothing on a runner can
# read that without either root and a loop mount or the VM the image
# exists to boot. So this runs `granule vm run`, which creates a VM,
# waits for its guest, runs one command in it and deletes it -- the same
# granule built from the same vendored tree (third_party/granule) that
# the guest itself is built with, so that the thing doing the reading and
# the thing that was read cannot skew.
#
# Needs: docker, /dev/kvm, and Go (to build granule, unless GRANULE
# names one).
#
# Usage:
#   ./scripts/granule-guest-probe.sh <image> <outdir>
#
# Writes three files under <outdir>: `pinned`, `drift` and `raw` (the
# probe's own output, markers and all, for when the two halves do not
# look like they came from the machine they were supposed to).
set -euo pipefail

if [ "$#" -ne 2 ] || [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  cat >&2 <<'USAGE'
usage: granule-guest-probe.sh <image> <outdir>

  <image>   a sandbox guest image (ghcr.io/.../guest:..., or a local tag)
  <outdir>  directory to write `pinned`, `drift` and `raw` into

environment:
  GRANULE                    granule to drive the VM with; built from
                               third_party/granule when unset
  GUEST_PROBE_READY_TIMEOUT    how long to wait for the guest to answer
                               (default 6m)
USAGE
  exit 2
fi

image="$1"
outdir="$2"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v docker >/dev/null 2>&1; then
  echo "granule-guest-probe.sh: docker not found" >&2
  exit 1
fi
if [ ! -e /dev/kvm ]; then
  echo "granule-guest-probe.sh: /dev/kvm is not present -- reading a guest image means booting it" >&2
  exit 1
fi

mkdir -p "$outdir"
workdir="$(mktemp -d)"
container=""
cleanup() {
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

granule="${GRANULE:-}"
if [ -z "$granule" ]; then
  echo "granule-guest-probe: building granule from granule" >&2
  # -buildvcs through scripts/buildvcs.sh, as every build of this checkout
  # goes: `go build -buildvcs=auto` does not mean "stamp the commit if you
  # can" but "stop the build if you cannot", and a diagnostic that refuses
  # to look at a guest image because git could not be read -- over a
  # commit stamp on a binary this script deletes when it exits -- would be
  # the least useful failure here. Resolved from the repository root,
  # which is the .git `go build` consults for a module sitting inside it.
  vcs="$(cd "$repo_root" && ./scripts/buildvcs.sh "${BUILDVCS:-auto}")"
  (cd "$repo_root" && go build -buildvcs="$vcs" -o "$workdir/granule" github.com/bwsalmon/granule/cmd/granule)
  granule="$workdir/granule"
fi

# --- The image around the guest, read without booting anything.
#
# The final granule image is `FROM scratch` (third_party/granule/Dockerfile)
# -- two static binaries, the guest's kernel and initramfs, and the guest
# disk -- so there is no shell in it to ask anything. `docker cp` out of a
# created-but-never-started container is what reads a file from an image
# that cannot run one, and `tar -xO` takes the content out of the stream
# docker cp writes, so the hash is of the file rather than of a tar header
# carrying the minute it was fetched.
#
# disk.img is deliberately not among them: it is the 15GB the rest of this
# script boots, and hashing it would compare two filesystems built minutes
# apart, which is the comparison that cannot work.
container="$(docker create "$image")"
{
  for path in \
    /usr/local/bin/granule \
    /usr/local/bin/cloud-hypervisor \
    /var/lib/granule/guest/vmlinux \
    /var/lib/granule/guest/vmlinuz \
    /var/lib/granule/guest/initrd.img
  do
    key="image$(printf '%s' "$path" | tr '/' '.')"
    if docker cp "$container:$path" - > "$workdir/file.tar" 2>/dev/null; then
      printf '%s.sha256: %s\n' "$key" "$(tar -xOf "$workdir/file.tar" | sha256sum | cut -c1-64)"
    else
      printf '%s.sha256: <absent>\n' "$key"
    fi
  done
} > "$workdir/image-side"
docker rm -f "$container" >/dev/null
container=""

# --- The guest itself.
#
# One command, because it is one boot: everything below runs inside the
# VM, prints to granule's stdout (its own progress goes to stderr), and
# the markers are what tells a truncated run from a complete one.
#
# `set -e` is deliberately not used in here. A probe that stops at the
# first thing an image happens not to have would report the *shape* of
# that image rather than its contents, and two such reports agreeing means
# nothing; every value below is instead read with a fallback, so a missing
# thing shows up as a line saying so and the comparison sees it.
probe=$(cat <<'PROBE'
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

# kv <key> <command...>: the command's first line of output, or <absent>.
kv() {
  k=$1
  shift
  v=$("$@" 2>/dev/null | head -1)
  [ -n "$v" ] || v="<absent>"
  printf '%s: %s\n' "$k" "$v"
}

# sha <key> <file>: what the file contains, as one line.
sha() {
  if [ -r "$2" ]; then
    printf '%s: %s\n' "$1" "$(sha256sum "$2" | cut -c1-64)"
  else
    printf '%s: <absent>\n' "$1"
  fi
}

# body <key> <file>: the file's own non-blank lines, sorted, so a config
# a build wrote differently shows up as the line that differs rather than
# as a hash nobody can read.
body() {
  if [ -r "$2" ]; then
    awk -v k="$1" 'NF { print k ": " $0 }' "$2" | sort
  else
    printf '%s: <absent>\n' "$1"
  fi
}

dpkg-query -W -f='${db:Status-Abbrev}\t${Package}\t${Version}\n' 2>/dev/null \
  | awk -F'\t' '$1 ~ /^ii/ { print $2 "\t" $3 }' | sort > /tmp/probe-packages

echo "#### grain-guest-probe pinned ####"

# The toolchains, and the two files that decide how they behave offline.
kv go.version /usr/local/go/bin/go version
kv go.goroot.version cat /usr/local/go/VERSION
body go.env /home/debian/.config/go/env
kv node.version /usr/local/bin/node --version
kv npm.version /usr/local/lib/nodejs/bin/npm --version
sha npm.wrapper.sha256 /usr/local/bin/npm
body npmrc.global /usr/local/lib/nodejs/etc/npmrc
body npmrc.debian /home/debian/.npmrc
kv kind.version /usr/local/bin/kind version

# docker has to be running, not merely installed: it is started during
# the provision and enabled for every boot after it, and a guest whose
# dockerd does not come up is one every dispatched task finds out about.
# Retried, because this runs moments after the guest answered and
# docker.service may still be starting -- a race here would report a
# difference between two identical images.
#
# The symlinks rather than `systemctl is-enabled`: the answer is the same
# and this one does not depend on systemd answering on a bus, so a guest
# whose systemd is wedged still reports what its image was built to
# enable instead of a row of <absent>. The whole directory, because a
# package whose postinst enables a unit of its own is exactly the kind of
# difference between two provisions worth seeing.
ls /etc/systemd/system/multi-user.target.wants 2>/dev/null | sort \
  | sed 's/^/unit.multi-user.wants: /'
docker_state=failed
i=0
while [ "$i" -lt 30 ]; do
  if docker info >/dev/null 2>&1; then
    docker_state=ok
    break
  fi
  i=$((i + 1))
  sleep 2
done
printf 'docker.info: %s\n' "$docker_state"
body docker.daemon-json /etc/docker/daemon.json

# The account, and the files and units the Granulefile writes around it.
kv account.debian.shell sh -c 'getent passwd debian | cut -d: -f7'
kv account.debian.groups sh -c 'id -nG debian | tr " " "\n" | sort | tr "\n" " " | sed "s/ $//"'
body sudoers.90-debian /etc/sudoers.d/90-debian
kv nsswitch.hosts sh -c 'grep "^hosts:" /etc/nsswitch.conf'
body sysctl.99-grain-kind /etc/sysctl.d/99-grain-kind.conf
sha growfs.script.sha256 /usr/local/sbin/grain-growfs
kv unit.grain-growfs sh -c 'test -e /etc/systemd/system/sysinit.target.wants/grain-growfs.service && echo enabled || echo missing'

# The set of packages, without their versions. A package appearing or
# disappearing means the provision step resolved differently -- which is
# exactly the kind of outside change the cache key cannot see -- while
# the versions move under it constantly and are in the drift half below.
printf 'packages.count: %s\n' "$(wc -l < /tmp/probe-packages | tr -d ' ')"
awk -F'\t' '{ print "package: " $1 }' /tmp/probe-packages

# The two caches that make .grain/workflows/tests.yml's dependency installs
# fast and reproducible. A module or package that is in one guest and not
# the other means two supposedly identical guest builds can behave
# differently, even though an open-network job could refill the cache.
#
# .ziphash rather than .info: it is written for a module whose zip was
# actually downloaded and verified, which is what "the cache holds this
# dependency" means. Modules the graph names but never fetches leave a
# .mod behind and nothing else.
mods=/tmp/probe-mods
find /home/debian/go/pkg/mod/cache/download -name '*.ziphash' 2>/dev/null \
  | sed -e 's|^/home/debian/go/pkg/mod/cache/download/||' -e 's|/@v/|@|' -e 's|\.ziphash$||' \
  | sort > "$mods"
printf 'gomodcache.count: %s\n' "$(wc -l < "$mods" | tr -d ' ')"
sed 's/^/gomod: /' "$mods"

# npm's cache is content-addressed and its index is JSON, so the tarball
# URLs it recorded are the readable form of "which packages are in here".
pkgs=/tmp/probe-npm
grep -rho 'https://registry\.npmjs\.org/[^"]*\.tgz' /home/debian/.npm/_cacache/index-v5 2>/dev/null \
  | sed 's|^https://registry\.npmjs\.org/||' | sort -u > "$pkgs"
printf 'npmcache.count: %s\n' "$(wc -l < "$pkgs" | tr -d ' ')"
sed 's/^/npmpkg: /' "$pkgs"

echo "#### grain-guest-probe drift ####"

# Everything below moves without any commit here moving, which is the
# trade guest-inputs.sh's header describes rather than a fault.
kv os.pretty-name sh -c '. /etc/os-release && echo "$PRETTY_NAME"'
kv os.version-id sh -c '. /etc/os-release && echo "$VERSION_ID"'
kv kernel.release uname -r
awk -F'\t' '{ print "package.version: " $1 " " $2 }' /tmp/probe-packages

echo "#### grain-guest-probe end ####"
PROBE
)

# Nine characters, and it has to stay under twelve: netshim derives the
# VM's tap device name from it as "tap-<name>", Linux caps an interface
# name at 15 characters, and vmspec.Validate rejects anything longer
# before the VM is ever created. "guest-probe-$$" was over that cap on
# every host, which is what a stub granule had no way to say -- the
# first real run of nightly.yml's guest-cache-drift job died on it in
# three seconds. Random rather than the pid, and shaped like granule's own
# throwaway build VMs ("gb-xxxxxx", internal/guestbuild's buildName), so
# the two probes one run makes cannot collide with each other or with a
# VM an earlier run left behind.
vm="gp-$(od -An -N3 -tx1 /dev/urandom | tr -d ' ')"
state="$workdir/state"
mkdir -p "$state"
echo "granule-guest-probe: booting ${image} as VM ${vm}" >&2
# -disk-mode=overlay for the reason pkg/orchestrator's own real-VM test
# gives: the guest writes into a thin qcow2 of its own, and everything
# from dockerd starting to the growfs unit needs a root it can write to.
# No -guest-user: the probe reads root-owned files under /etc and the
# "debian" account's caches alike, and granule authorizes this boot's key
# for root regardless of which second account it is asked for.
"$granule" vm run "$vm" \
  -state-dir "$state" \
  -granule-image "$image" \
  -disk-mode=overlay \
  -ready-timeout "${GUEST_PROBE_READY_TIMEOUT:-6m}" \
  -- /bin/sh -c "$probe" > "$outdir/raw"

# The markers are the difference between "this guest has nothing in it"
# and "this probe stopped half way", and only one of those is worth
# failing a comparison over. A run that ends without its end marker is
# the second, so it fails here rather than producing two short files that
# happen to match.
if ! grep -qx '#### grain-guest-probe end ####' "$outdir/raw"; then
  echo "granule-guest-probe.sh: the probe of ${image} did not run to completion; see ${outdir}/raw" >&2
  exit 1
fi

awk -v pinned="$outdir/pinned" -v drift="$outdir/drift" '
  /^#### grain-guest-probe pinned ####$/ { out = pinned; next }
  /^#### grain-guest-probe drift ####$/  { out = drift;  next }
  /^#### grain-guest-probe end ####$/    { out = ""; next }
  out != "" { print > out }
' "$outdir/raw"

# The image-side hashes belong to the drift half: they are the granule
# build around the guest, and what moves them is the Docker Hub base and
# the Go image the vendored Dockerfile builds with rather than anything
# here.
cat "$workdir/image-side" >> "$outdir/drift"

# A fingerprint that is missing its expensive halves would compare clean
# against another one missing the same halves, which is the one outcome
# worth refusing outright: a green comparison that checked nothing. So
# the shape of what was collected is asserted before it is handed on.
#
# The floors are well under what a guest of this repository actually
# carries -- measured on a dispatched sandbox in September 2026: 259
# packages, 39 modules with a downloaded zip (grain's build list, not its
# whole go.sum), 313 npm tarballs. Low enough that a dependency being
# dropped does not fail this, high enough that an empty cache cannot pass
# as one.
require() {
  local what="$1" pattern="$2" least="$3" found
  found="$(grep -c -- "$pattern" "$outdir/pinned" || true)"
  if [ "$found" -lt "$least" ]; then
    echo "granule-guest-probe.sh: ${image}: ${found} ${what} in the fingerprint, expected at least ${least} -- the probe ran but did not find the guest it was pointed at" >&2
    exit 1
  fi
}
require "Go toolchain lines" '^go\.version: go version go1\.' 1
require "Node lines" '^node\.version: v' 1
require "installed packages" '^package: ' 100
require "cached Go modules" '^gomod: ' 10
require "cached npm packages" '^npmpkg: ' 50

echo "granule-guest-probe: ${image}: $(wc -l < "$outdir/pinned" | tr -d ' ') pinned lines, $(wc -l < "$outdir/drift" | tr -d ' ') drift lines" >&2
