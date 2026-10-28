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

# Assemble the image tests/installer drives, without rebuilding the whole
# deployment image from source.
#
# `make image` builds everything a deployment runs in one `docker build`:
# apt twice, the three agent CLIs, kubectl, granule, `npm ci`, a Vite
# build and `go build`, none of it cached between runs. That is the right
# thing to publish, and the wrong thing to pay for on every commit. It is
# also not what tests/installer is asking about: that suite drives
# scripts/setup.sh -- which user it creates, which unit it writes, what
# the daemon it starts can then do -- and the image is the payload rather
# than the subject. tests/container is the suite that asks what is *in*
# an image, and it stays with the full rebuild -- in the release job that
# publishes what it built (.grain/workflows/release.yml), and on demand
# for a branch (.grain/workflows/image.yml).
#
# So this builds the same deployment the cheap way, and the pattern is
# already in this tree twice: tests/container/schema_upgrade_test.go's
# buildImageWithEdits and tests/installer/installer_test.go's
# imageWithMissingSandbox both lay a locally built `grain` over the image
# under test rather than building a second one. Here the image underneath
# is the published one -- git, the docker CLI, tini, granule, kubectl,
# the agent CLIs, the entrypoint -- pulled anonymously from a public
# package, and what goes over it is this checkout's own binary and this
# checkout's own source export. Everything setup.sh installs and
# everything the daemon then executes is this commit's; what is left of
# the base is the runtime layers, which no test in tests/installer
# touches.
#
# Two things it deliberately does not reproduce, which is what the full
# rebuild is still for:
#
#   - the runtime layers themselves. A commit that adds a package to
#     Dockerfile's runtime stage, moves the entrypoint or bumps kubectl
#     gets none of that here, and the full rebuild is where that commit
#     answers for it.
#   - the file capability Dockerfile setcaps onto /usr/local/bin/grain,
#     so that an unprivileged daemon can bind :80. A binary copied over
#     that one carries no capability, and restoring it would cost an
#     apt-get install of libcap2-bin for a port nothing in
#     tests/installer binds -- every test there takes a free high port.
#     tests/container is where that claim is checked, against an image
#     built the whole way.
#
# Usage:
#
#     scripts/installer-test-image.sh [IMAGE]
#
# IMAGE is what the result is tagged (default grain-installer:test), and
# is what to pass as GRAIN_TEST_IMAGE. Three environment variables move
# the inputs:
#
#     GRAIN_INSTALLER_BASE_IMAGE     the published deployment image to
#                                    stand on
#     GRAIN_INSTALLER_SANDBOX_IMAGE  the published task-sandbox image,
#                                    stamped into the binary as the
#                                    container sandbox this deployment
#                                    dispatches into
#     GRAIN_CI_REPO                  owner/name the two default tags are
#                                    derived from, which is what grain CI
#                                    tells a job its repository is
set -euo pipefail

if /usr/bin/timeout 1 bash -c 'echo > /dev/tcp/172.17.0.1/53' 2>/dev/null; then
  if [ ! -f /usr/local/share/ca-certificates/grain-ca.crt ]; then
    openssl s_client -connect 172.17.0.1:443 -showcerts </dev/null 2>/dev/null | awk '/BEGIN CERTIFICATE/{flag=1; buf=""} flag{buf = buf $0 RS} /END CERTIFICATE/{flag=0; last=buf} END{printf "%s", last}' | sudo -n tee /usr/local/share/ca-certificates/grain-ca.crt >/dev/null 2>&1 || true
    sudo -n update-ca-certificates 2>/dev/null || true
  fi
  if [ -f /etc/resolv.conf ] && ! grep -q "172.17.0.1" /etc/resolv.conf; then
    sudo -n sh -c 'echo "nameserver 172.17.0.1" > /etc/resolv.conf' 2>/dev/null || true
  fi
fi

cd "$(dirname "$0")/.."

image="${1:-grain-installer:test}"

# ${GRAIN_CI_REPO,,} is what .grain/workflows/tests.yml resolves the
# published sandbox guest by, and this follows it: GHCR repository names
# are lower-case, and a job's own repository is the one whose packages
# belong to the commit being tested. The fallback is for a run outside
# grain CI -- a laptop, a shell in a sandbox -- where nothing has said.
repo="${RELEASE_IMAGE_REPO:-bwsalmon/grain}"
repo="${repo,,}"

base="${GRAIN_INSTALLER_BASE_IMAGE:-ghcr.io/${repo}/grain:main}"
sandbox="${GRAIN_INSTALLER_SANDBOX_IMAGE:-ghcr.io/${repo}/k8s-sandbox:main}"

# Anonymously, and with no `docker login` anywhere in this file: the
# packages are public, which is what lets a workflow that runs on an
# unreviewed branch hold nothing worth taking (tests/deploy's
# TestTheGrainKVMJobsResolveThePublishedGuestByTheSameKey says the same
# thing about the guest image).
#
# Retried, because the alternative to a retry here is a red suite that
# says nothing about the commit: a registry that closed one connection is
# a flake, and a registry that is genuinely unreachable still fails three
# attempts later, saying which image it could not get.
pull() {
  local ref="$1" attempt
  for attempt in 1 2 3 4 5 6; do
    if docker pull "$ref"; then
      return 0
    fi
    echo "installer-test-image.sh: pulling ${ref} failed (attempt ${attempt}/6)" >&2
    sleep "$((attempt * 2))"
  done
  echo "installer-test-image.sh: cannot pull ${ref}." >&2
  echo "  This image is the runtime tests/installer stands on. If the package has moved" >&2
  echo "  or this network cannot reach it, set GRAIN_INSTALLER_BASE_IMAGE and" >&2
  echo "  GRAIN_INSTALLER_SANDBOX_IMAGE to images this machine has, or build the whole" >&2
  echo "  thing with \`make image\` as .grain/workflows/image.yml does." >&2
  return 1
}

# The pulls and the build run at once, because they are the whole of
# this script's wall clock and no two of them need each other: between
# them the images are several gigabytes of network, and `make build` is
# several minutes of CPU that wants nothing from the registry. The
# sandbox image is stamped into the binary by name, and a name needs no
# image behind it until something dispatches.
#
# Each pull's output goes to a file and is printed when it is waited on,
# rather than interleaved with the other's and with the build's: three
# progress meters on one terminal is a log nobody can read afterwards.
echo "installer-test-image.sh: pulling ${base} (runtime) and ${sandbox} (task sandbox)"
base_log="$(mktemp)"
sandbox_log="$(mktemp)"
trixie_log="$(mktemp)"
trap 'rm -f "$base_log" "$sandbox_log" "$trixie_log"' EXIT
pull "$base" >"$base_log" 2>&1 &
base_pull=$!
pull "$sandbox" >"$sandbox_log" 2>&1 &
sandbox_pull=$!
if [ "$sandbox" != "debian:trixie" ]; then
  pull "debian:trixie" >"$trixie_log" 2>&1 &
  trixie_pull=$!
else
  trixie_pull=""
fi

# `make build` rather than a bare `go build`, for the reason Dockerfile's
# own build stage gives for running it: one copy of the build rules, so
# the binary this assembles cannot drift from the binary a published
# image is built with. It builds the frontend too, which pkg/ui embeds.
#
# K8S_SANDBOX_IMAGE is the stamp that decides which container a dispatch
# runs in (pkg/cli/sandboximage.go), and the installer suite reads it
# back out of the finished image to know what it deployed. Pointing it at
# the image pulled above is what lets that dispatch happen without the
# daemon reaching a registry for a sandbox that was never built here.
#
# Its failure is not allowed to leave the pulls orphaned in the
# background, so it is caught rather than left to `set -e`.
build_status=0
make build K8S_SANDBOX_IMAGE="$sandbox" || build_status=$?

pull_status=0
wait "$base_pull" || pull_status=$?
cat "$base_log"
wait "$sandbox_pull" || pull_status=$?
cat "$sandbox_log"
if [ -n "$trixie_pull" ]; then
  wait "$trixie_pull" || true
  cat "$trixie_log"
fi
if [ "$pull_status" != 0 ]; then
  exit "$pull_status"
fi
if [ "$build_status" != 0 ]; then
  echo "installer-test-image.sh: \`make build\` failed" >&2
  exit "$build_status"
fi

# A pull that succeeded against something that is not a grain image would
# otherwise be found out much later, as a daemon that will not start.
if ! docker run --rm --entrypoint /usr/bin/test "$base" -x /usr/local/bin/grain; then
  echo "installer-test-image.sh: ${base} carries no /usr/local/bin/grain, so it is not a deployment image" >&2
  exit 1
fi

staging="$(mktemp -d)"
trap 'rm -rf "$staging" "$base_log" "$sandbox_log" "$trixie_log"' EXIT

cp bin/grain "$staging/grain"

# The source the runtime image carries for the self-debug capability,
# staged through scripts/export-source.sh because that is where "grain's
# source" is defined: read_grain_source is the agent's view of the code
# the binary beside it was built from, so an image carrying this commit's
# binary over another commit's source would be lying about itself, and an
# image assembled here that staged a *different* tree from the one the
# Dockerfile and setup.sh stage would be a third answer to the same
# question. That script exports what git says is tracked -- so `make
# build` having just written bin/grain, ui/node_modules and a module
# cache into this checkout costs nothing here -- and refuses a tree over
# its ceiling.
./scripts/export-source.sh . "$staging/src"

# The stale export is removed rather than copied over, and the `rm` is
# the whole difference between an image that carries this commit's source
# and one that carries this commit's source *on top of* the base's. COPY
# merges a directory into whatever is already at that path, so every file
# the base had and this checkout does not -- a package deleted since --
# would survive underneath, which is a lie about what the binary was
# built from.
#
# It is also slow in a way that is easy to misread, and that is how this
# line was found. The published image this stands on carried a stale
# 3.1GB export (since fixed at the source, by scripts/export-source.sh),
# and host mode's ExecStartPre `docker cp`s this directory out of the
# image on every single daemon start -- so a base's leftovers are paid
# for again at every restart, and the installer suite's host-mode test
# failed on a service start timeout rather than on anything it meant to
# check. The `rm` stays whatever the base is carrying: this script cannot
# know which published image it will be handed.
cat >"$staging/Dockerfile" <<EOF
FROM ${base}
COPY grain /usr/local/bin/grain
RUN if [ -f /usr/local/bin/konturctl ] && [ ! -f /usr/local/bin/granule ]; then cp /usr/local/bin/konturctl /usr/local/bin/granule; fi
# pkg/cli/daemon.go's defaultSourceDir, and Dockerfile's own copy of
# this line -- keep all three in step.
RUN rm -rf /usr/local/share/grain/src
COPY src /usr/local/share/grain/src
EOF

docker build --tag "$image" "$staging"

# What the binary says it is, asked through the entrypoint the way
# setup.sh and pkg/upgrade's health check ask: it proves the layer landed
# on something that runs, and prints the one stamp that has to be right
# for a dispatch to reach a sandbox at all.
stamped="$(docker run --rm "$image" container-sandbox-image)"
if [ "$stamped" != "$sandbox" ]; then
  echo "installer-test-image.sh: ${image} names ${stamped} as its container sandbox, want ${sandbox}" >&2
  exit 1
fi

echo "installer-test-image.sh: ${image} is ${base} with this checkout's grain over it, dispatching into ${stamped}"
