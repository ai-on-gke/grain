#!/usr/bin/env bash
# A `go test -exec` program that runs each compiled test binary as an
# unprivileged account when the step itself is root.
#
# Usage:
#   go test -exec "$PWD/scripts/exec-unprivileged.sh" ./pkg/...
#
# Why: PostgreSQL's initdb and postgres refuse to run as root ("initdb:
# error: cannot be run as root"), so the postgres job's tests, which start
# their own server through postgres.EnsureRunning, cannot pass in a
# sandbox whose CI steps run as uid 0. The build still happens as the
# step's own user, against its own module and build caches; only the test
# binary drops to GRAIN_UNPRIVILEGED_USER (default "debian", the sandbox
# image's own account).
#
# Not root, or no such account: the binary runs as it is, and anything
# that cannot run as root says so itself.
set -euo pipefail

bin=$1
shift
user="${GRAIN_UNPRIVILEGED_USER:-debian}"

if [ "$(id -u)" != 0 ] || ! id -u "$user" >/dev/null 2>&1; then
  exec "$bin" "$@"
fi

# go test builds into a 0700 directory of root's, which the account
# cannot reach, so the binary is copied somewhere it can.
dir=$(mktemp -d /var/tmp/grain-unprivileged-test.XXXXXX)
trap 'rm -rf "$dir"' EXIT
chmod 0755 "$dir"
install -m 0755 "$bin" "$dir/$(basename "$bin")"

status=0
runuser -u "$user" -- "$dir/$(basename "$bin")" "$@" || status=$?
exit "$status"
