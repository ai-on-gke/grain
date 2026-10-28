#!/bin/bash
# Leaves the sandbox guest image this commit needs in the local docker
# daemon: pulled from the content-addressed cache when there is one to
# pull, built -- and retried once -- when there is not.
#
# This is the "Resolve the sandbox guest" step of
# .grain/workflows/tests.yml and of .grain/workflows/kvm-route-stress.yml,
# which used to be ~40 lines of shell written out twice. It is a script
# now for two reasons. The lesser one is that those two copies had to
# stay identical by hand -- a stress run against a differently-built
# guest is not evidence about the guest the other job boots. The greater
# one is the paragraph below.
#
# What the build is, as of the Granulefile conversion
# ---------------------------------------------------
#
# Read the two history sections below with this in front of them, because
# they describe a pipeline that no longer exists. The guest is built by
# `granule granulefile build -f Granulefile`, and a Granulefile is a
# Dockerfile with `VM PACK` in it: every `RUN` in it -- every apt-get,
# every curl -- runs in an ordinary docker build on *this* host, and the
# packing is one more `RUN`, in a container. **Nothing boots.** There is
# no build VM, so there is no netshim in front of one, so there is no
# 8.8.8.8 default to override and no guest console to follow; the build
# resolves names exactly the way every other fetch in the job does.
# `granule granulefile build -print -f Granulefile` prints the whole
# generated Dockerfile if you would rather check that than believe it.
#
# What that costs this script is a little dead weight it keeps on
# purpose: follow_consoles below finds no granule-vm-* containers and
# writes nothing, which is the correct behaviour for a build that boots
# nothing and the right behaviour again on the day one does.
#
# Why a build's output does not go to the step log any more
# ---------------------------------------------------------
#
# A guest build prints tens of megabytes: when this was written that was
# a docker build of the granule base and then ~37 minutes of a VM booting
# and provisioning itself with `set -x` on; it is a single long docker
# build now, and just as large. A grain CI step keeps 256KB of what it
# printed in memory
# and 64KB of it in the database (pkg/ci: maxStepOutput,
# MaxStoredStepOutput), head and tail with the middle elided -- so a step
# that builds the guest twice keeps the first 16KB of attempt 1 and the
# last 48KB of attempt 2, and *everything else is gone*.
#
# That is exactly the shape of the only interesting failure this step
# has. On grain/task-371's 41a36da the job went green in 1h11m with
# `granulectl 17s, base 22s, boot-provision-commit 2212s` -- a 22-second
# base build is a warm docker cache, which in a fresh sandbox means an
# earlier build in the same job warmed it. Attempt 1 had failed and the
# retry had saved the job; and nobody could say what attempt 1 died of,
# because its failure, and the "retrying once" line under it, were in
# the elided middle. A retry whose failures are unreadable is a retry
# that hides a regression until the day both attempts fail.
#
# So each attempt's output goes to its own file, and what reaches the
# step log is bounded and chosen:
#
#   - a progress line every GRAIN_GUEST_BUILD_HEARTBEAT seconds, carrying
#     the elapsed time, the line count and the last line the build
#     printed -- which is what somebody following a live step wanted from
#     the raw stream anyway, and is also how a build that has *stopped*
#     printing anything is recognisable while it is still happening;
#   - the tail of a failed attempt, the lines in it that look like an
#     error, and the build VM's own console, printed when it fails and
#     printed *again* at the end of the step, so the surviving 48KB holds
#     it however long the attempt after it runs for;
#   - the build's own phase timings, which are the line a later "why is
#     this an hour" works from.
#
# The console was the one of those that was not merely a smaller version
# of something the log had -- back when there was one. `guest build`
# boots the base image in
# a container and waits for the guest inside it to answer; when that
# container exits first it says "the guest container exited before its
# guest became reachable" and nothing else (guestbuild.waitForGuest), and
# it runs the container with --rm, so by the time anything else could
# `docker logs` it there is nothing left to read. Reproduced in a task
# sandbox on 2026-09-20, that message was the entire account of a failed
# build. So each attempt follows every granule-vm-* container into a file
# as it appears, the way tests.yml's suite steps do, and a failed attempt
# prints the tail of each. `granulefile build` boots nothing, so today
# that finds nothing and costs nothing; see the section at the top.
#
# The whole of it is a few KB, so nothing here is elided at all.
#
# What attempt 1 has actually been dying of
# -----------------------------------------
#
# Run with this instrumentation in a task sandbox on 2026-09-20 -- when
# the build still booted a VM -- a cold build failed after 17 minutes
# with apt inside that VM unable to reach one host:
#
#   Could not connect to packages.cloud.google.com:80
#   (142.251.218.110), connection timed out
#   E: Failed to fetch .../google-cloud-cli_585.0.0-0_amd64...deb
#
# Every other fetch in that build worked -- deb.debian.org,
# download.docker.com, apt.releases.hashicorp.com (35.8MB of terraform),
# kind.sigs.k8s.io, nodejs.org -- and the setup script's own retry of the
# block failed the same way a second time, so it is that host rather
# than the guest's network or its resolver. The name resolved; it is the
# route to Google's archive that timed out. Which is also why the retry
# in this script is load-bearing: on grain/task-371's 41a36da the
# attempt after it reached the same archive and the build finished.
#
# So a failure here is worth reading before it is worth fixing: if it is
# this, the fix is somewhere in what the guest installs from Google's
# archive (the Granulefile's RUN steps) or in this deployment's egress,
# and neither is anything this script should paper over.
#
# Usage:
#   IMAGE=grain-guest:e2e-test ./scripts/granule-guest-resolve.sh
#
# It takes no arguments. It used to forward everything it did not read
# itself to scripts/granule/build-guest.sh and thence to `guest build`;
# that script is gone and `granulefile build`'s argv is fixed here, so an
# argument would be silently dropped rather than obeyed, and this refuses
# one instead.
#
# Environment:
#   IMAGE                          the tag to leave behind (required)
#   GRAIN_CI_REPO                  owner/name, for the cache registry
#   GRAIN_GUEST_BUILD_ALWAYS=1     skip the pull and build, which is how
#                                  to ask a CI sandbox what a cold build
#                                  does on a branch whose guest inputs
#                                  are unchanged
#   GRAIN_GUEST_BUILD_ATTEMPTS     how many tries (default 2)
#   GRAIN_GUEST_BUILD_HEARTBEAT    seconds between progress lines (60)
#   GRAIN_GUEST_BUILD_LOG_LINES    lines of a failed attempt to print (120)
#   GRAIN_GUEST_BUILD_LOGS         where the per-attempt logs go
set -euo pipefail
cd "$(dirname "$0")/.."
repo_root="$(pwd)"

ca_copied=false
cleanup_ca() {
  if [ "${ca_copied:-false}" = true ] && command -v rm >/dev/null 2>&1; then
    rm -f "${repo_root:-.}/.grain-ca.crt" 2>/dev/null || true
    ca_copied=false
  fi
}
trap cleanup_ca EXIT INT TERM

if [ -f /usr/local/share/ca-certificates/grain-ca.crt ] && [ ! -f "$repo_root/.grain-ca.crt" ] && command -v cp >/dev/null 2>&1; then
  cp /usr/local/share/ca-certificates/grain-ca.crt "$repo_root/.grain-ca.crt" 2>/dev/null || true
  ca_copied=true
fi

IMAGE="${IMAGE:?IMAGE names the tag this leaves behind}"

# Loudly, because the two callers were still passing `-ready-timeout 10m
# -shutdown-timeout 3m` -- `guest build -setup`'s flags, for the build VM
# that is gone -- long after anything read them. A dropped argument that
# nobody is told about is how a job goes on believing it asked for
# something.
if [ "$#" -ne 0 ]; then
  echo "granule-guest-resolve.sh: takes no arguments, got: $*" >&2
  # Phrased without naming the build command, because
  # TestEveryGuestBuildPacksOntoAGranuleImageThisRepositoryBuilds scans
  # these scripts for that spelling and would read this sentence as an
  # invocation that forgot its -kontur-image.
  echo "granule-guest-resolve.sh: this script's build argv is fixed below; there is no longer a build VM to pass timeouts or a resolver to" >&2
  exit 2
fi

# What a guest is packed onto, built here on a cache miss rather than
# pulled: scripts/granule/build-granule-image.sh, which names the same
# default and holds why this is not granulefile's own `granule:latest`.
granule_image="granule:vendored"
attempts="${GRAIN_GUEST_BUILD_ATTEMPTS:-2}"
heartbeat="${GRAIN_GUEST_BUILD_HEARTBEAT:-60}"
log_lines="${GRAIN_GUEST_BUILD_LOG_LINES:-120}"
logdir="${GRAIN_GUEST_BUILD_LOGS:-/tmp/guest-build}"

# Bytes, not just lines: a build that fails inside `npm` or `apt` can
# print a single line of tens of KB, and the point of this whole script
# is that what it prints fits in what a step keeps. 16KB an attempt, an
# attempt's report printed twice (where it happened, and again at the
# end), two attempts: ~68KB worst case against a 48KB tail and a 16KB
# head, which is the one shape here that is allowed to reach the cap --
# and it reaches it with the end-of-step copies, which are the ones
# written to be read.
log_bytes=16384

mkdir -p "$logdir"

# The cache key, in the one expression every file that resolves this
# image derives it with -- tests/deploy's
# TestEveryGuestCacheKeyIsDerivedTheSameWay holds the publisher
# (scripts/release-images.sh) and every consumer to the same text,
# because a key derived any other way misses for ever rather than
# failing. GRAIN_CI_REPO is grain CI's spelling of GITHUB_REPOSITORY.
mapfile -t inputs < <(./scripts/granule/guest-inputs.sh guest)
key="$(git rev-parse "${inputs[@]/#/HEAD:}" | sha256sum | cut -c1-32)"
cached="ghcr.io/${GRAIN_CI_REPO,,}/guest:inputs-${key}"

# What a failed attempt left behind, printed small. Kept in a variable as
# well as printed, so that it can be printed a second time at the end of
# the step: attempt 2 runs for half an hour after this, and the last
# thing a step said is the part of it that survives.
failures=""

report_attempt_failure() {
  local attempt="$1" log="$2" status="$3" elapsed="$4" consoles="$5"
  local report=""
  report+="===== guest build attempt ${attempt} failed (exit ${status}, after ${elapsed}s) ====="$'\n'
  report+="$(grep -n -i -E 'error|fail|cannot|could not|denied|timeout|timed out|no space|refused|unreachable|resolve' "$log" |
    tail -n 40 | cut -c1-400 || true)"$'\n'
  report+="===== the last ${log_lines} lines of attempt ${attempt} ====="$'\n'
  report+="$(tail -n "$log_lines" "$log" | tail -c "$log_bytes" || true)"$'\n'
  # The build VM's own console, which granulectl does not print in the one
  # failure mode where it is the only evidence there is: waitForGuest
  # gives up with "the guest container exited before its guest became
  # reachable" and says nothing about why, and the container is gone by
  # the time anything after the build could `docker logs` it.
  local console
  for console in "$consoles"/*.log; do
    # -s, not -e: the netns container beside every VM prints nothing, and
    # a heading over nothing is the kind of noise that makes a report
    # look like it is padding rather than reporting.
    [ -s "$console" ] || continue
    report+="===== build VM console ${console##*/} ====="$'\n'
    report+="$(tail -n 80 "$console" | tail -c 8192 || true)"$'\n'
  done
  report+="===== disk and memory after attempt ${attempt} ====="$'\n'
  report+="$( { df -h /; free -m; } 2>&1 || true)"$'\n'
  printf '%s\n' "$report"
  failures+="$report"$'\n'
}

# Follows every VM container this build starts into a file of its own, as
# it appears. `granulectl guest build` runs its VM with --rm, so a
# `docker logs` after the fact finds nothing: a console has to be
# followed while it is being printed or not read at all.
follow_consoles() {
  local dir="$1" c name
  while true; do
    for c in $(docker ps -aq --filter 'name=granule-vm-' 2>/dev/null); do
      if [ ! -e "${dir}/${c}.started" ]; then
        touch "${dir}/${c}.started"
        name="$(docker inspect -f '{{.Name}}' "$c" 2>/dev/null | tr -d /)"
        (docker logs -f "$c" > "${dir}/${name:-$c}.log" 2>&1 &)
      fi
    done
    sleep 2
  done
}

# One build, with its output in a file and a progress line on a clock.
#
# The heartbeat is not decoration: this is a step that can run for an
# hour, and until it existed a watcher had the choice between tens of MB
# of buildkit and apt (which the step log then ate) and nothing at all.
# The last line the build printed is what says whether an hour-old build
# is working or wedged.
run_attempt() {
  local attempt="$1"
  local log="${logdir}/attempt-${attempt}.log"
  local consoles="${logdir}/consoles-${attempt}"
  : > "$log"
  mkdir -p "$consoles"
  echo "sandbox guest build: attempt ${attempt} -- output in ${log}, progress every ${heartbeat}s"

  follow_consoles "$consoles" &
  local follower=$!
  local started=$SECONDS
  # -kontur-image, because granulefile's own default for the image a
  # guest is packed onto is `granule:latest`, which is a Docker Hub
  # reference nothing here produces: the build above leaves the vendored
  # one behind under this tag instead. See
  # scripts/granule/build-granule-image.sh.
  if command -v granule >/dev/null 2>&1; then
    granule granulefile build -kontur-image "$granule_image" -f Granulefile -t "$IMAGE" > "$log" 2>&1 &
  else
    go run github.com/bwsalmon/granule/cmd/granule granulefile build -kontur-image "$granule_image" -f Granulefile -t "$IMAGE" > "$log" 2>&1 &
  fi
  local pid=$!

  local last=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    sleep 5
    if [ $(( SECONDS - last )) -lt "$heartbeat" ]; then
      continue
    fi
    last=$SECONDS
    local lines tip
    lines="$(wc -l < "$log" | tr -d ' ')"
    # tr rather than a quoting dance: buildkit redraws with \r, and a
    # bare last line of a redrawn frame is unreadable otherwise.
    tip="$(tail -c 4096 "$log" | tr '\r' '\n' | grep -v '^$' | tail -n 1 | cut -c1-160)"
    echo "sandbox guest build: attempt ${attempt} running, $(( SECONDS - started ))s elapsed, ${lines} lines, last: ${tip}"
  done

  # `wait` on a pid this shell started reports the build's own status;
  # the `|| status=$?` is what keeps the `set -e` above from making a
  # failed attempt the end of the step rather than the start of a retry.
  local status=0
  wait "$pid" || status=$?
  local elapsed=$(( SECONDS - started ))
  # After the build and before the report, so that a console still being
  # written is finished with rather than cut off mid-line.
  sleep 2
  kill "$follower" 2>/dev/null || true
  if [ "$status" = 0 ]; then
    echo "sandbox guest build: attempt ${attempt} succeeded in ${elapsed}s"
    # granule's own lines -- which file and stage it built, and the
    # size of what it left -- and the last few lines so the log still
    # shows the image being committed. The per-phase `build-guest:`
    # timings this used to grep for went with that script; per-step
    # timings are now only in buildkit's output in $log.
    grep -E '^(building|built) |`docker run --privileged' "$log" || true
    tail -n 5 "$log"
    return 0
  fi
  report_attempt_failure "$attempt" "$log" "$status" "$elapsed" "$consoles"
  return "$status"
}

# A miss is ordinary -- every change to a guest input is one -- so a
# failed pull is reported and swallowed rather than left to `set -e`.
# GRAIN_GUEST_BUILD_ALWAYS is for the other question: what a *cold* build
# does in this sandbox, asked from a branch whose guest inputs are
# unchanged and whose cache would therefore answer in ten seconds.
if [ -z "${GRAIN_GUEST_BUILD_ALWAYS:-}" ] && docker pull "$cached"; then
  docker tag "$cached" "$IMAGE"
  echo "sandbox guest cache: hit ${cached}"
else
  if [ -n "${GRAIN_GUEST_BUILD_ALWAYS:-}" ]; then
    echo "sandbox guest cache: GRAIN_GUEST_BUILD_ALWAYS is set, so ${cached} is not consulted -- building the guest"
  else
    echo "sandbox guest cache: miss ${cached} -- building the guest"
  fi

  # The granule image the guest is packed onto, built out of the vendored
  # tree before any attempt so that both attempts use the one image and a
  # failure here is not mistaken for a failed guest build. Its output goes
  # to a file for the reason every other build's does -- a step keeps
  # 64KB -- and what reaches the log is a line, or the tail of it when it
  # fails, which is the only time anybody wants the rest.
  granule_log="${logdir}/granule-image.log"
  granule_started=$SECONDS
  echo "granule image: building ${granule_image} from third_party/granule -- output in ${granule_log}"
  if ! IMAGE="$granule_image" ./scripts/granule/build-granule-image.sh > "$granule_log" 2>&1; then
    echo "granule image: build failed; the last ${log_lines} lines follow" >&2
    tail -n "$log_lines" "$granule_log" | tail -c "$log_bytes" >&2
    exit 1
  fi
  echo "granule image: built ${granule_image} in $(( SECONDS - granule_started ))s"

  # There was a block here that stood a caching dnsmasq up on this
  # sandbox's docker bridge and passed `-dns` at it, because a build VM
  # that names no resolver gets 8.8.8.8 across two NATs
  # (netshim.DefaultDNS) and that is what reddened this step: 268MB of
  # apt archives at 28MB/s, and then "could not resolve nodejs.org".
  # There is no build VM any more (see the top of this file): the build
  # is `granulefile build`, a docker build on this host, so it resolves
  # through docker's own resolver and there is nothing for a `-dns` to
  # override -- and `granulefile build` has no such flag to pass it to
  # in any case. What stood here spent ~10s installing dnsmasq and then
  # handed the address to run_attempt, which shifts off the attempt
  # number and discards the rest, so the build never saw it. Removed
  # rather than fixed: the failure it was for cannot happen to a build
  # that boots nothing.

  # Twice, and only here. "Nothing in this job retries" is a rule about
  # the suite, whose flakes are the thing being measured; this is a
  # ~25-minute download of ~1GB, and its flakes are somebody else's
  # weather.
  built=""
  for attempt in $(seq 1 "$attempts"); do
    if run_attempt "$attempt"; then
      built=1
      break
    fi
    if [ "$attempt" != "$attempts" ]; then
      echo "sandbox guest build: attempt ${attempt} failed, retrying" >&2
    fi
  done

  # Printed here rather than only where they happened, because this is
  # the end of the step and the end of a step is the part a capped log
  # keeps. A green build with this in it is the interesting case: it says
  # the retry was load-bearing, and what it was load-bearing against.
  if [ -n "$failures" ]; then
    echo "===== what the failed attempt(s) of this build said ====="
    printf '%s\n' "$failures"
  fi

  if [ -z "$built" ]; then
    echo "sandbox guest build: failed ${attempts} times; the guest this job needs cannot be built in this sandbox" >&2
    exit 1
  fi
fi

docker image inspect "$IMAGE" >/dev/null
df -h /
