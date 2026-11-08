#!/bin/sh
# Start a `grain host -join` for /tmp/host-<name> in the background (its
# log is /tmp/host-<name>/host.log), against the walkthrough daemon.
#
#   join-host.sh <name> <enrollment-code> [extra grain host flags...]
set -eu
name=$1
code=$2
shift 2
root=/tmp/host-$name
cd "$root"
HOME=$root/home PATH=$root/home/bin:$PATH nohup /home/debian/bin/grain host \
	-server http://127.0.0.1:4280 -join "$code" \
	-key-file "$root/home/hostd-key" -token-file "$root/home/hostd-token" \
	-allowlist "$root/hostd.yaml" -socket "$root/hostd.sock" -state-dir "$root/state" \
	"$@" >"$root/host.log" 2>&1 &
echo $! >"$root/host.pid"
