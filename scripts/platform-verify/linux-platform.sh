#!/usr/bin/env bash
# scripts/platform-verify/linux-platform.sh — bring one of `grain init`'s two Linux platforms
# up on a brand-new GCE instance, from nothing, and then throw the instance
# away.
#
# Why re-run something that already worked
# ----------------------------------------
# docs/grain-init-platforms.md calls all four platforms implemented, and the
# two Linux rows were proven by real bring-ups (linux-kvm 12m14s on a bare
# host, linux-container 176s). But those runs predate the preflight, adopt
# mode, the non-root daemon account, and the sandbox-budget refusal -- every
# one of which changes what init does before it starts a daemon. So the table
# currently vouches for a `grain init` nobody has run end to end. This is the
# clean-room run that makes "implemented" mean current main.
#
# What the instance needs that init will not install
# --------------------------------------------------
# pkg/hostinit/preflight.go demands three things and installs none of them:
# `make`, `node`/`npm`, and -- on linux-kvm only -- `/dev/kvm`. Everything
# else (docker, Go, the claude CLI) init installs itself, so this script
# deliberately installs *only* the three, to keep the clean-room honest: if
# init has grown a dependency it does not install and does not demand, this
# is the run that finds out.
#
# And two things init cannot bootstrap at all, which docs/grain-init.md's
# "Before you start" spells out: the grain binary that runs it, and a checkout
# at -repo-root to build from. A bare host can run neither `make build` nor
# `grain init` until those exist by other means. So the binary is
# cross-compiled on the Mac and the tree arrives as a `git archive` tarball --
# which also means no GitHub credential is ever copied to a disposable VM, for
# a private repository.
#
# Usage:
#   PLATFORM=linux-kvm       NAME=grain-p3-kvm MACHINE_TYPE=n2-standard-8 NESTED=1 ./scripts/platform-verify/linux-platform.sh
#   PLATFORM=linux-container NAME=grain-p3-ctr MACHINE_TYPE=e2-standard-4 NESTED=0 ./scripts/platform-verify/linux-platform.sh
#   ./scripts/platform-verify/linux-platform.sh --keep       # leave the instance up
#
# The instance is deleted on exit unless --keep, on failure and Ctrl-C too.

set -uo pipefail

PLATFORM="${PLATFORM:-linux-kvm}"
NAME="${NAME:-grain-p3-$RANDOM}"
ZONE="${ZONE:-us-east1-b}"
PROJECT="${PROJECT:-grain-host}"
MACHINE_TYPE="${MACHINE_TYPE:-n2-standard-8}"
# linux-kvm needs nested virtualization; linux-container exists precisely
# because a host has none, so asking for it there would be testing the wrong
# row. E2 cannot do it at all, which is why that row uses one.
NESTED="${NESTED:-1}"
REF="${REF:-origin/main}"
# The repository this verifies is the one this script lives in, so the
# root is two directories up rather than a sibling checkout. These ran
# from ~/Developer/grain-gcp before they moved here, where "../grain"
# was right; REPO= still overrides it for a checkout elsewhere.
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# 4242 is hostinit.DefaultUIAddr's own port (it was 8420 before), and the default here for a
# reason worth writing down: -ui-addr does NOT tell the daemon where to
# listen. Its help says so -- "host:port the daemon's UI/API answers on over
# loopback -- must match -ui-addr if -daemon-arg overrides it, since this is
# where the steps below reach it back" -- but it is easy to read as
# configuration rather than as an address to poll. Passing -ui-addr alone
# leaves the daemon on 4242 and sends init looking somewhere else, so every
# run ends at "confirm the daemon's API is reachable ... did not answer
# within 30s" with a perfectly healthy daemon running beside it. Both of this
# script's first real runs died exactly there.
#
# So: a non-default port is passed twice, once to the daemon and once to
# init, which is what the flag help asks for.
UI_PORT="${UI_PORT:-4242}"

KEEP=0
# Arguments are validated rather than sniffed. The first version of this
# tested only `[ "$1" = "--keep" ]`, so any other argument -- a typo, a
# `--help` this never had -- fell through as "not --keep" and the script
# went on to build a grain binary and create a cluster. That is the wrong
# response to an argument nobody meant, from a script whose next step
# spends money, so an unknown one stops it here.
usage() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}
case "${1:-}" in
  "")       ;;
  --keep)   KEEP=1 ;;
  -h|--help) usage 0 ;;
  *)        printf 'unknown argument: %s\n\n' "$1" >&2; usage 2 ;;
esac

say()  { printf '\n\033[1m==> [%s] %s\033[0m\n' "$PLATFORM" "$1"; }
ok()   { printf '    \033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { printf '    \033[31mFAIL\033[0m  %s\n' "$1"; }
info() { printf '    %s\n' "$1"; }

G=(gcloud --project "$PROJECT")
SSH=("${G[@]}" compute ssh "$NAME" --zone "$ZONE")

created=0
# shellcheck disable=SC2317 # reached through the trap below, not in sequence
cleanup() {
  local rc=$?
  if [ "$created" = "1" ] && [ "$KEEP" = "0" ]; then
    say "teardown"
    "${G[@]}" compute instances delete "$NAME" --zone "$ZONE" --quiet 2>&1 | tail -2
  elif [ "$created" = "1" ]; then
    say "kept"
    info "delete it with: gcloud compute instances delete $NAME --zone $ZONE --project $PROJECT --quiet"
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- artifacts
say "artifacts (built here, not on the host)"
work="$(mktemp -d)"
# The tree first, and then the binary *from that tree* rather than from the
# working checkout. They have to be one commit or this is not a clean-room
# run of anything in particular: a binary from whatever branch happens to be
# checked out, driving a tarball of main, tests a pair that exists nowhere.
( cd "$REPO" && git archive --format=tar.gz --prefix=grain/ -o "$work/repo.tar.gz" "$REF" ) \
  || { bad "git archive $REF failed"; exit 1; }
sha="$(cd "$REPO" && git rev-parse --short "$REF")"
ok "tree $REF ($sha): $(du -h "$work/repo.tar.gz" | cut -f1)"
mkdir -p "$work/src" && tar -C "$work/src" -xzf "$work/repo.tar.gz"
GOOS=linux GOARCH=amd64 go build -C "$work/src/grain" -o "$work/grain" ./cmd/grain \
  || { bad "cross-compiling grain for linux/amd64 from $REF failed"; exit 1; }
ok "grain linux/amd64 built from the same tree: $(du -h "$work/grain" | cut -f1)"

# ----------------------------------------------------------------- instance
say "instance"
if "${G[@]}" compute instances describe "$NAME" --zone "$ZONE" >/dev/null 2>&1; then
  info "$NAME already exists -- reusing it, and not deleting it on exit"
  KEEP=1
else
  args=(compute instances create "$NAME" --zone "$ZONE"
        --machine-type "$MACHINE_TYPE"
        --image-family debian-12 --image-project debian-cloud
        --boot-disk-size 100GB --boot-disk-type pd-balanced)
  [ "$NESTED" = "1" ] && args+=(--enable-nested-virtualization)
  "${G[@]}" "${args[@]}" --quiet 2>&1 | tail -2 || { bad "instance create failed"; exit 1; }
  created=1
  ok "created $NAME ($MACHINE_TYPE, nested=$NESTED)"
fi

# A fresh Debian image refuses port 22 for the first half-minute or so, so
# this retries. It sleeps *first*, and reports what the last attempt actually
# said, because the previous version did neither: it passed a flag gcloud
# does not accept (--tunnel-through-iap=false), so every attempt died on
# argument parsing in microseconds, all thirty retries burned in under a
# second, and the failure was reported as "ssh never came up" -- which looked
# like a slow boot and was a typo.
say "waiting for ssh"
last=""
for i in $(seq 1 30); do
  sleep 10
  if last="$("${SSH[@]}" --command true 2>&1)"; then ok "ssh is up after $((i*10))s"; break; fi
  if [ "$i" = 30 ]; then
    bad "ssh never came up; the last attempt said:"
    printf '%s\n' "$last" | tail -3
    exit 1
  fi
done

# ------------------------------------------------- only what init demands
say "installing only what preflight demands and init never installs"
# shellcheck disable=SC2016 # expressions run in remote bash, not local shell
"${SSH[@]}" --command 'set -e
sudo DEBIAN_FRONTEND=noninteractive apt-get -qq update
# make (build-essential), node and npm. Deliberately NOT docker, NOT Go, NOT
# the claude CLI: grain init installs those, and letting it is the test.
sudo DEBIAN_FRONTEND=noninteractive apt-get -qq install -y \
  build-essential nodejs npm >/dev/null
echo "    make $(make --version | head -1 | awk "{print \$3}")"
echo "    node $(node --version)  npm $(npm --version)"
echo "    docker: $(command -v docker || echo "absent (init installs it)")"
echo "    go: $(command -v go || echo "absent (init installs it)")"
echo "    /dev/kvm: $(test -e /dev/kvm && echo present || echo absent)"' 2>&1 | tail -8

# ----------------------------------------------------------------- copy in
say "copying the binary and the tree"
"${G[@]}" compute scp "$work/grain" "$work/repo.tar.gz" "$NAME:~/" --zone "$ZONE" >/dev/null 2>&1 \
  || { bad "scp failed"; exit 1; }
"${SSH[@]}" --command 'set -e; chmod +x ~/grain; rm -rf ~/grain-src; mkdir -p ~/grain-src;
tar -C ~/grain-src -xzf ~/repo.tar.gz; rm -f ~/repo.tar.gz; ls ~/grain-src/grain/go.mod >/dev/null; echo "    tree extracted"' 2>&1 | tail -2
ok "in place"

# --------------------------------------------------------------- grain init
# Under sudo, and with absolute paths. grain init installs docker and the Go
# toolchain, creates the daemon's system account and links the claude CLI onto
# PATH -- all of which need root, and none of which it escalates for by
# itself: run as an ordinary user it reports those four as [manual] and
# everything downstream as skipped, which is correct behaviour and not a
# deployment. "~" is spelled out because it would be /root once sudo has it.
#
# The exit code is taken without a pipe anywhere between grain and this
# shell. The previous version ran `grain init ... | tail -45` *inside* the
# remote shell, so ssh returned tail's status -- always 0 -- and a run with
# four outstanding [manual] steps was reported here as "exited 0", which is
# exactly what docs/grain-init.md says 0 must never mean. Output goes to a
# file and is read back after the status is in hand.
# shellcheck disable=SC2016 # remote expansion intended
rhome="$("${SSH[@]}" --command 'echo $HOME' 2>/dev/null | tr -d '\r')"
info "remote home: $rhome"
say "grain init -platform=$PLATFORM (as root)"
out="$work/init.log"
start=$(date +%s)
daemon_arg=""
[ "$UI_PORT" != "4242" ] && daemon_arg="-daemon-arg -ui-addr=127.0.0.1:$UI_PORT"
"${SSH[@]}" --command "sudo $rhome/grain init -platform=$PLATFORM \
  -repo-root $rhome/grain-src/grain \
  -data-dir $rhome/grain-data \
  $daemon_arg \
  -ui-addr 127.0.0.1:$UI_PORT" > "$out" 2>&1
rc=$?
elapsed=$(( $(date +%s) - start ))
tail -45 "$out"
say "grain init exited $rc after ${elapsed}s"
case "$rc" in
  0)  ok "0 = the deployment is up and provisioned (docs/grain-init.md's own contract)" ;;
  10) info "10 = ran to completion with manual or refused steps left" ;;
  *)  bad "$rc = a genuine failure" ;;
esac

# ------------------------------------------------------------------ verify
# Exit code alone has misled here before, so ask the deployment itself.
say "is it actually serving?"
# Polls rather than asking once, and says how long it took, because init's own
# step gives up at 30s and "did not answer within 30s" does not distinguish a
# daemon that needs 40 from one that is never going to answer. When it never
# answers, the journal is dumped here -- the first two runs of this script
# reported the symptom on both platforms and then deleted the only host that
# could explain it.
#
# `|| echo` is not used on the curl: curl -w prints its own "000" on a failed
# connection, so the two concatenate into "000unreachable". The status is
# captured on its own line instead.
"${SSH[@]}" --command "
printf '    daemon unit:   %s\n' \"\$(systemctl is-active grain-daemon 2>/dev/null || true)\"
code=000
for i in \$(seq 1 18); do
  code=\$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:$UI_PORT/api/config 2>/dev/null)
  [ \"\$code\" != 000 ] && { printf '    UI on $UI_PORT:    http=%s after %ss\n' \"\$code\" \"\$((i*5))\"; break; }
  sleep 5
done
[ \"\$code\" = 000 ] && printf '    UI on $UI_PORT:    never answered in 90s\n'
printf '    docker:        %s\n' \"\$(docker --version 2>/dev/null || true)\"
printf '    go:            %s\n' \"\$(/usr/local/go/bin/go version 2>/dev/null || true)\"
printf '    images:\n'; docker images --format '      {{.Repository}}:{{.Tag}} {{.Size}}' 2>/dev/null | grep -E 'granule|guest' || printf '      none\n'
if [ \"\$code\" = 000 ]; then
  printf '\n    --- systemctl status grain-daemon ---\n'
  systemctl status grain-daemon.service --no-pager 2>&1 | head -12
  printf '\n    --- journalctl -u grain-daemon (last 40) ---\n'
  sudo journalctl -u grain-daemon.service -n 40 --no-pager 2>&1 | tail -40
  printf '\n    --- listening sockets ---\n'
  sudo ss -ltnp 2>/dev/null | head -12
fi
" 2>&1 | tail -80

exit $rc
