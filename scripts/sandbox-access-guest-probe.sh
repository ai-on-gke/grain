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

# sandbox-access-guest-probe.sh -- verify that a guest in
# CHV_NET_MODE=inline cannot reach grain's sandbox-access acceptor while
# its wrapper's dial-out to it works (docs/design/grain-on-gke.md,
# Security 2; GKE-2).
#
#   scripts/sandbox-access-guest-probe.sh
#
# Needs /dev/kvm read-write, docker (to unpack GRANULE_IMAGE, default the
# published alpine guest), unshare/nsenter/ip and go. Modelled on
# third_party/granule/test/sandbox-access-vm.sh, which checks the same
# property for granule's listen mode.
#
# Inside an unprivileged user + network namespace laid out like a sandbox
# pod (eth0 10.88.0.2, default route via 10.88.0.1), with a peer namespace
# standing in for the cluster: 10.88.0.1 is the gateway, 10.88.1.1:7443
# is grain's acceptor (pkg/sandboxaccess, run by its
# TestVM_GuestCannotReachAcceptor) and 10.88.1.2:7443 a control listener.
# granule-shim boots the guest with GRANULE_ACCESS_DIAL at the acceptor
# and GRANULE_ACCESS_CONTROLLER_IPS naming it, as K8sGranuleSandboxes
# sets them; the test then has the guest connect to both addresses. The
# acceptor must be unreachable and the control listener reachable.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GRANULE=$ROOT/third_party/granule
GRANULE_IMAGE=${GRANULE_IMAGE:-ghcr.io/gke-labs/granule/alpine:latest}
POD_IP=10.88.0.2
GATEWAY_IP=10.88.0.1
ACCEPTOR_IP=10.88.1.1
CONTROL_IP=10.88.1.2
PORT=7443

PASSED=0 FAILED=0
FAILURES=()
pass() { PASSED=$((PASSED + 1)); echo "PASS $1"; }
fail() { FAILED=$((FAILED + 1)); FAILURES+=("$1"); echo "FAIL $1${2:+: $2}"; }
summary() {
  echo
  echo "sandbox-access-guest-probe: $PASSED passed, $FAILED failed"
  for f in "${FAILURES[@]}"; do echo "  failed: $f"; done
  [[ $FAILED -eq 0 ]]
}

# The part that runs as root inside the user + network namespace.
if [[ ${1:-} == __inner ]]; then
  W=$2
  set -e
  mount -t tmpfs tmpfs /run
  ip link set lo up
  ip link add eth0 type veth peer name peer0
  unshare --net sleep 900 &
  echo $! >"$W/peer.pid"
  sleep 0.5
  peer=$(cat "$W/peer.pid")
  ip link set peer0 netns "$peer"
  nsenter -t "$peer" -n sh -c "ip link set lo up && ip addr add $GATEWAY_IP/24 dev peer0 &&
    ip addr add $ACCEPTOR_IP/32 dev peer0 && ip addr add $CONTROL_IP/32 dev peer0 && ip link set peer0 up"
  ip addr add $POD_IP/24 dev eth0
  ip link set eth0 up
  ip route add default via $GATEWAY_IP
  set +e

  # grain's acceptor, in the peer namespace; it mints the credentials
  # granule-shim starts with.
  nsenter -t "$peer" -n env SANDBOXACCESS_VM_DIR="$W" SANDBOXACCESS_VM_LISTEN=$ACCEPTOR_IP:$PORT \
    SANDBOXACCESS_VM_CONTROL=$CONTROL_IP:$PORT \
    "$W/sandboxaccess.test" -test.run 'TestVM_' -test.v -test.count=1 >"$W/acceptor.log" 2>&1 &
  acc=$!
  for _ in $(seq 100); do [[ -e $W/minted ]] && break; sleep 0.1; done

  G=$W/img/var/lib/granule/guest
  A=$W/access
  export CHV_BINARY_PATH=$W/img/usr/local/bin/cloud-hypervisor CHV_KERNEL=$G/vmlinux \
    CHV_DISK_IMAGE=$G/disk.img CHV_DISK_OVERLAY_PATH=$W/state/overlay.qcow2 CHV_MEMORY_MB=512 CHV_NET_MODE=inline
  export GRANULE_ACCESS_DIAL=$ACCEPTOR_IP:$PORT GRANULE_ACCESS_SANDBOX_ID=sandbox-vm \
    GRANULE_ACCESS_TOKEN_FILE=$A/token GRANULE_ACCESS_CA_FILE=$A/ca.crt \
    GRANULE_ACCESS_CERT_FILE=$A/tls.crt GRANULE_ACCESS_KEY_FILE=$A/tls.key \
    GRANULE_ACCESS_CONTROLLER_IPS=$ACCEPTOR_IP
  mkdir -p "$W/state"
  "$W/granule-shim" run >"$W/console.log" 2>&1 &
  vm=$!
  if "$W/granule-shim" ready -timeout 120s; then echo READY; fi
  wait $acc
  echo "TEST-EXIT $?"
  kill -TERM $vm
  wait $vm
  kill "$peer" 2>/dev/null
  exit 0
fi

need=()
for c in docker unshare nsenter ip go; do command -v "$c" >/dev/null 2>&1 || need+=("$c"); done
if [[ ${#need[@]} -gt 0 ]]; then
  fail "host tools" "missing: ${need[*]}"
  summary
  exit
fi
if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
  fail "/dev/kvm is usable" "this caller (uid $(id -u), groups $(id -G)) cannot open it read-write"
  summary
  exit
fi

W=$(mktemp -d "${TMPDIR:-/tmp}/sandbox-access-probe.XXXXXX")
chmod 0711 "$W"
cleanup() {
  [[ -f $W/peer.pid ]] && kill "$(cat "$W/peer.pid")" 2>/dev/null
  if [[ ${KEEP:-} == 1 ]]; then echo "work directory left at $W"; else rm -rf "$W"; fi
}
trap cleanup EXIT

if ! (cd "$GRANULE" && CGO_ENABLED=0 go build -trimpath -o "$W/granule-shim" ./cmd/granule-shim) ||
  ! (cd "$ROOT" && CGO_ENABLED=0 go test -c -o "$W/sandboxaccess.test" ./pkg/sandboxaccess); then
  fail "build granule-shim and the pkg/sandboxaccess test binary"
  summary
  exit
fi

mirror_ref="127.0.0.1:9729/ghcr/${GRANULE_IMAGE#ghcr.io/}"
if ! docker pull -q "$GRANULE_IMAGE" >/dev/null 2>&1; then
  if [[ $GRANULE_IMAGE == ghcr.io/* ]] && docker pull -q "$mirror_ref" >/dev/null 2>&1; then
    docker tag "$mirror_ref" "$GRANULE_IMAGE"
  else
    fail "pull $GRANULE_IMAGE"
    summary
    exit
  fi
fi
mkdir -p "$W/img"
cid=$(docker create "$GRANULE_IMAGE") && docker export "$cid" | tar -C "$W/img" -xf - 2>/dev/null
docker rm "$cid" >/dev/null 2>&1

out=$(timeout 420 unshare --user --map-root-user --net --mount "$0" __inner "$W" 2>&1)
echo "$out" | sed 's/^/     /'
sed 's/^/     acceptor: /' "$W/acceptor.log"

if grep -q '^READY$' <<<"$out"; then pass "the guest boots under granule-shim in inline mode with dial-out configured"; else
  fail "the guest boots" "$(tail -c 800 "$W/console.log" | tr '\n' ' ')"
fi
if grep -q -- '--- PASS: TestVM_GuestCannotReachAcceptor' "$W/acceptor.log" && grep -q '^TEST-EXIT 0$' <<<"$out"; then
  pass "the wrapper dials out to grain's acceptor; the guest reaches a control listener but not the acceptor"
else
  fail "TestVM_GuestCannotReachAcceptor" "$(grep -E -- '--- (FAIL|SKIP)|_test.go:[0-9]+' "$W/acceptor.log" | head -10 | tr '\n' ' ')"
fi
summary
