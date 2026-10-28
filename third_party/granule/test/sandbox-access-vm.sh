#!/usr/bin/env bash
# sandbox-access-vm.sh -- boot a real guest under this tree's granule-shim
# with the sandbox access API on a TLS TCP listener, and drive it the way a
# Kubernetes deployment does: from another network namespace, directly (an
# in-cluster controller) and through granule relay (an off-cluster one).
#
#   test/sandbox-access-vm.sh
#
# Needs: /dev/kvm read-write, docker (to unpack GRANULE_IMAGE, default the
# published alpine guest), unshare/nsenter/ip, openssl and go.
#
# What it does, inside an unprivileged user + network namespace laid out
# like a pod (eth0 with 10.88.0.2, its peer 10.88.0.1 in a namespace of its
# own, standing in for the relay's or controller's pod):
#   1. mints a CA, a listener certificate and a per-sandbox token;
#   2. runs granule-shim with GRANULE_ACCESS_LISTEN=tcp://0.0.0.0:7443 and
#      inline networking, so the guest owns the pod's address and has open
#      egress across netshim's splice;
#   3. runs pkg/sandbox's TestVM_AccessOverTCPListen from the peer
#      namespace: refusals, exec, a batched file put and reattach after a
#      dropped connection, directly and through an in-process relay, and a
#      check that the guest neither sees the token nor reaches the listener;
#   4. checks granule-shim unlinked the token file before boot.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GRANULE_IMAGE=${GRANULE_IMAGE:-ghcr.io/gke-labs/granule/alpine:latest}
GUEST_IP=10.88.0.2
PEER_IP=10.88.0.1
PORT=7443

PASSED=0 FAILED=0
FAILURES=()
pass() { PASSED=$((PASSED + 1)); echo "PASS $1"; }
fail() { FAILED=$((FAILED + 1)); FAILURES+=("$1"); echo "FAIL $1${2:+: $2}"; }
summary() {
  echo
  echo "sandbox-access-vm: $PASSED passed, $FAILED failed"
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
  nsenter -t "$peer" -n sh -c "ip link set lo up && ip addr add $PEER_IP/24 dev peer0 && ip link set peer0 up"
  ip addr add $GUEST_IP/24 dev eth0
  ip link set eth0 up
  ip route add default via $PEER_IP
  set +e

  G=$W/img/var/lib/granule/guest
  export CHV_BINARY_PATH=$W/img/usr/local/bin/cloud-hypervisor CHV_KERNEL=$G/vmlinux \
    CHV_DISK_IMAGE=$G/disk.img CHV_DISK_OVERLAY_PATH=$W/state/overlay.qcow2 CHV_MEMORY_MB=512
  export GRANULE_ACCESS_LISTEN=tcp://0.0.0.0:$PORT GRANULE_ACCESS_SANDBOX_ID=sandbox-vm \
    GRANULE_ACCESS_TOKEN_FILE=$W/token GRANULE_ACCESS_TLS_CERT_FILE=$W/tls.crt GRANULE_ACCESS_TLS_KEY_FILE=$W/tls.key
  mkdir -p "$W/state"
  "$W/granule-shim" run >"$W/console.log" 2>&1 &
  vm=$!
  if "$W/granule-shim" ready -timeout 120s; then echo READY; fi
  if [[ -e $W/token ]]; then echo TOKEN-FILE-LEFT; else echo TOKEN-FILE-GONE; fi
  # /proc/<pid>/environ is a process's environment as it was exec'd, so
  # what matters is what granule-shim's children inherited.
  chv=$(pgrep -P $vm -f cloud-hypervisor | head -1)
  if [[ -z $chv ]]; then echo TOKEN-ENV-UNKNOWN
  elif tr '\0' '\n' <"/proc/$chv/environ" | grep -q '^GRANULE_ACCESS_TOKEN'; then echo TOKEN-ENV-LEFT
  else echo TOKEN-ENV-GONE; fi
  unset GRANULE_ACCESS_LISTEN GRANULE_ACCESS_TOKEN_FILE
  nsenter -t "$peer" -n env \
    GRANULE_ACCESS_VM_ADDR=$GUEST_IP:$PORT GRANULE_ACCESS_VM_TOKEN="$(cat "$W/token.keep")" \
    GRANULE_ACCESS_VM_CA="$W/ca.crt" GRANULE_ACCESS_VM_NAME=sandbox-vm \
    "$W/sandbox.test" -test.run 'TestVM_' -test.v -test.count=1 2>&1
  echo "TEST-EXIT $?"
  kill -TERM $vm
  wait $vm
  kill "$peer" 2>/dev/null
  exit 0
fi

need=()
for c in docker unshare nsenter ip openssl go; do command -v "$c" >/dev/null 2>&1 || need+=("$c"); done
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

W=$(mktemp -d "${TMPDIR:-/tmp}/granule-access-vm.XXXXXX")
chmod 0711 "$W"
cleanup() {
  [[ -f $W/peer.pid ]] && kill "$(cat "$W/peer.pid")" 2>/dev/null
  if [[ ${KEEP:-} == 1 ]]; then echo "work directory left at $W"; else rm -rf "$W"; fi
}
trap cleanup EXIT

if ! (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$W/granule-shim" ./cmd/granule-shim &&
  CGO_ENABLED=0 go test -c -o "$W/sandbox.test" ./pkg/sandbox); then
  fail "build granule-shim and the pkg/sandbox test binary"
  summary
  exit
fi

# A CA, a listener certificate for the pod's name and address, and a token.
(
  cd "$W" &&
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=access-test-ca \
      -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign \
      -keyout ca.key -out ca.crt &&
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=sandbox-vm -keyout tls.key -out tls.csr &&
    printf 'subjectAltName=DNS:sandbox-vm,IP:%s\nextendedKeyUsage=serverAuth\n' "$GUEST_IP" >ext.cnf &&
    openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 1 -extfile ext.cnf -out tls.crt &&
    openssl rand -hex 24 >token && cp token token.keep && chmod 0644 tls.key
) >"$W/openssl.log" 2>&1 || {
  fail "mint the test CA, certificate and token" "$(tail -c 400 "$W/openssl.log")"
  summary
  exit
}

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

if grep -q '^READY$' <<<"$out"; then pass "the guest boots under granule-shim with the access listener configured"; else
  fail "the guest boots" "$(tail -c 800 "$W/console.log" | tr '\n' ' ')"
fi
if grep -q 'serving the sandbox access API on' "$W/console.log"; then pass "granule-shim serves the access API"; else
  fail "granule-shim serves the access API" "$(grep '^granule' "$W/console.log" | tail -5 | tr '\n' ' ')"
fi
if grep -q '^TOKEN-FILE-GONE$' <<<"$out"; then pass "granule-shim unlinks the token file"; else fail "granule-shim unlinks the token file"; fi
if grep -q '^TOKEN-ENV-GONE$' <<<"$out"; then pass "cloud-hypervisor does not inherit the token's variables"; else fail "cloud-hypervisor does not inherit the token's variables"; fi
if grep -q -- '--- PASS: TestVM_AccessOverTCPListen' <<<"$out" && grep -q '^TEST-EXIT 0$' <<<"$out"; then
  pass "exec, PutFiles and reattach over TCP listen, directly and through granule relay; the guest cannot reach the listener or see the token"
else
  fail "TestVM_AccessOverTCPListen" "$(grep -E -- '--- (FAIL|SKIP)|_test.go:[0-9]+' <<<"$out" | head -10 | tr '\n' ' ')"
fi
summary
