#!/usr/bin/env bash
# rootless-vm.sh - boot a granule VM container with no host root: in a user
# namespace, with /dev/kvm reached through the host's kvm group, and no
# CAP_SYS_ADMIN anywhere. docs/rootless.md is what this verifies.
#
# Usage:
#   test/rootless-vm.sh [MODE...]
#
# Modes (default: both):
#   unshare   `unshare --user --map-root-user --net --mount`: container root
#             is the caller's own uid, the shim runs from the host's
#             filesystem, and the image's files are the caller's.
#   crun      a rootless crun container whose root is the first subuid of
#             the caller's /etc/subuid range, on a rootfs unpacked as those
#             subuids (what rootless storage hands a runtime), keeping the
#             caller's groups (run.oci.keep_original_groups) so the host's
#             kvm group reaches /dev/kvm, with podman's rootless capability
#             set plus CAP_NET_ADMIN and nothing else.
#
# Environment:
#   GRANULE_IMAGE       the granule image whose cloud-hypervisor, kernel and
#                       guest disk are booted (default
#                       ghcr.io/gke-labs/granule/alpine:latest); its guest
#                       needs busybox's ping, which the alpine one has
#   GRANULE_SHIM        a static granule-shim to boot with (default: built
#                       from this tree, CGO_ENABLED=0, which is what is
#                       under test; the image's own is not used)
#   CRUN                the crun to use (default: crun 1.24, fetched and
#                       checked against a pinned sha256 -- Debian
#                       bookworm's 1.8.1 cannot create a rootless container
#                       on recent kernels at all)
#   GITHUB_PROXY        as for the Dockerfile: where to fetch github release
#                       assets from when github.com is not reachable directly
#   VIRTIOFSD_ROOTFS    a directory holding a root filesystem with
#                       /usr/libexec/virtiofsd in it (Debian trixie's, say);
#                       when set, crun mode also shares a directory into the
#                       guest through it, running unprivileged in the
#                       container. Unset, that check is skipped.
#   REQUIRE_KVM=1       fail rather than skip when /dev/kvm is unusable
#   KEEP=1              leave the work directory behind
#
# Every check prints one line, PASS, FAIL or SKIP, and the run exits non-zero
# if any check failed. Without a /dev/kvm this caller can open read-write,
# the whole run is a SKIP (unless REQUIRE_KVM=1), since nothing it checks
# can be checked without booting.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GRANULE_IMAGE=${GRANULE_IMAGE:-ghcr.io/gke-labs/granule/alpine:latest}
CRUN_VERSION=1.24
CRUN_SHA256_amd64=4b0787622b76beb08c4150f7440b399cde97c14e1f928efe2513f63667fd02a5

# The guest's side of the network, and the peer it should reach across the
# splice: what a CNI's veth (or pasta) would give the container.
GUEST_IP=10.88.0.2
PEER_IP=10.88.0.1

PASSED=0 FAILED=0 SKIPPED=0 SCOPE=setup
FAILURES=()
pass() { PASSED=$((PASSED + 1)); echo "PASS [$SCOPE] $1"; }
fail() { FAILED=$((FAILED + 1)); FAILURES+=("[$SCOPE] $1"); echo "FAIL [$SCOPE] $1${2:+: $2}"; }
skip() { SKIPPED=$((SKIPPED + 1)); echo "SKIP [$SCOPE] $1${2:+: $2}"; }
note() { echo "     [$SCOPE] $*"; }

# check NAME WANT CMD...: PASS when CMD's output contains WANT.
check() {
  local name="$1" want="$2" out
  shift 2
  if out=$("$@" 2>&1) && grep -qF -- "$want" <<<"$out"; then
    pass "$name"
  else
    fail "$name" "wanted \"$want\", got: $(tr '\n' ' ' <<<"$out" | cut -c1-400)"
  fi
}

summary() {
  echo
  echo "rootless-vm: $PASSED passed, $FAILED failed, $SKIPPED skipped"
  for f in "${FAILURES[@]}"; do echo "  failed: $f"; done
  [[ $FAILED -eq 0 ]]
}

# --- inside `unshare --user --map-root-user --net --mount` ----------------
#
# The script re-executes itself here, as the namespace's root. Everything
# below runs with the caller's own uid on the host and no capability
# outside the namespace.
if [[ ${1:-} == __unshare-inner ]]; then
  W=$2
  set -e
  mount -t tmpfs tmpfs /run
  ip link set lo up
  ip link add eth0 type veth peer name peer0
  # The peer goes into a second network namespace -- still this user
  # namespace's -- so that reaching it from the guest crosses the splice
  # rather than this namespace's own stack.
  unshare --net sleep 600 &
  echo $! >"$W/peer.pid"
  sleep 0.5
  ip link set peer0 netns "$(cat "$W/peer.pid")"
  nsenter -t "$(cat "$W/peer.pid")" -n sh -c "ip addr add $PEER_IP/24 dev peer0 && ip link set peer0 up"
  ip addr add $GUEST_IP/24 dev eth0
  ip link set eth0 up
  ip route add default via $PEER_IP
  set +e

  G=$W/img/var/lib/granule/guest
  export CHV_BINARY_PATH=$W/img/usr/local/bin/cloud-hypervisor CHV_KERNEL=$G/vmlinux \
    CHV_DISK_IMAGE=$G/disk.img CHV_DISK_OVERLAY_PATH=$W/state/overlay.qcow2 CHV_MEMORY_MB=512
  mkdir -p "$W/state"
  "$W/granule-shim" run >"$W/unshare-console.log" 2>&1 &
  vm=$!
  echo "uid_map: $(tr -s ' ' <"/proc/self/uid_map")"
  echo "groups: $(id -G)"
  if "$W/granule-shim" ready -timeout 60s; then echo READY; fi
  "$W/granule-shim" exec -- ip -4 -o addr show eth0
  "$W/granule-shim" exec -u root -- ping -c 3 -W 2 $PEER_IP
  kill -TERM $vm
  wait $vm
  echo "shim exited $?"
  kill "$(cat "$W/peer.pid")" 2>/dev/null
  exit 0
fi

# --- host side -------------------------------------------------------------

need() {
  local missing=()
  for c in "$@"; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
  [[ ${#missing[@]} -eq 0 ]] && return 0
  echo "${missing[*]}"
  return 1
}

# fetch_github URL OUT: github release assets, through GITHUB_PROXY first
# when it is set (with -k: the proxy's certificate is not github's, and the
# caller's pinned sha256 is what vouches for the file), as the Dockerfile's
# fetch-chv does.
fetch_github() {
  local url=$1 out=$2 h resolve=()
  if [[ -n ${GITHUB_PROXY:-} ]]; then
    for h in github.com objects.githubusercontent.com release-assets.githubusercontent.com; do
      resolve+=(--resolve "$h:443:$GITHUB_PROXY")
    done
    curl --connect-timeout 10 --retry 3 -fsSL -k "${resolve[@]}" -o "$out" "$url" && return 0
  fi
  curl --connect-timeout 10 --retry 3 -fsSL -o "$out" "$url"
}

# as_subroot CMD...: CMD as root of a user namespace mapping 0 to the first
# of the caller's subuids (and subgids) -- the owner of everything in the
# crun container's rootfs, and the only account that can write it.
as_subroot() {
  unshare --map-users="$SUBUID,0,65536" --map-groups="$SUBGID,0,65536" --setuid 0 --setgid 0 "$@"
}

cleanup() {
  [[ -n ${CRUN_BIN:-} && -d ${W:-}/crun-state ]] && "$CRUN_BIN" --root "$W/crun-state" delete -f granule-rootless >/dev/null 2>&1
  [[ -f ${W:-}/peer.pid ]] && kill "$(cat "$W/peer.pid")" 2>/dev/null
  if [[ ${KEEP:-} == 1 ]]; then
    echo "work directory left at $W"
  elif [[ -n ${W:-} && -d $W ]]; then
    [[ -d $W/bundle/rootfs && -n ${SUBUID:-} ]] && as_subroot rm -rf "$W/bundle/rootfs"
    rm -rf "$W"
  fi
}

MODES=("$@")
[[ ${#MODES[@]} -eq 0 ]] && MODES=(unshare crun)

if [[ -r /dev/kvm && -w /dev/kvm ]]; then
  note "/dev/kvm: $(ls -ln /dev/kvm | awk '{print $1, "uid", $3, "gid", $4}'), this caller is uid $(id -u) groups $(id -G)"
else
  if [[ ${REQUIRE_KVM:-} == 1 ]]; then
    fail "/dev/kvm is usable" "this caller (uid $(id -u), groups $(id -G)) cannot open it read-write"
  else
    skip "everything" "no /dev/kvm this caller can open read-write"
  fi
  summary
  exit
fi

if missing=$(need docker unshare nsenter ip curl); then :; else
  fail "host tools" "missing: $missing"
  summary
  exit
fi

W=$(mktemp -d "${TMPDIR:-/tmp}/granule-rootless.XXXXXX")
# Searchable (not readable) by the subuids, whose root has to reach the
# bundle crun mounts from.
chmod 0711 "$W"
trap cleanup EXIT

if [[ -n ${GRANULE_SHIM:-} ]]; then
  cp "$GRANULE_SHIM" "$W/granule-shim"
elif ! (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$W/granule-shim" ./cmd/granule-shim); then
  fail "build granule-shim from this tree"
  summary
  exit
fi
chmod 0755 "$W/granule-shim"

# In a grain K8s sandbox, grain configures the authenticated GHCR package
# cache mirror at http://127.0.0.1:9729/v2/ghcr in
# /etc/docker/certs.d/ghcr.io/hosts.toml, which classic dockerd (without the
# containerd snapshotter) ignores while registry-mirrors only covers
# docker.io. Pull ghcr.io/* through 127.0.0.1:9729/ghcr/* when that mirror is
# present or when a direct pull of ghcr.io/* fails.
pull_granule_image() {
  local mirror_ref="127.0.0.1:9729/ghcr/${GRANULE_IMAGE#ghcr.io/}"
  if [[ $GRANULE_IMAGE == ghcr.io/* ]] && grep -q '127.0.0.1:9729/v2/ghcr' /etc/docker/certs.d/ghcr.io/hosts.toml 2>/dev/null; then
    if docker pull -q "$mirror_ref" >/dev/null && docker tag "$mirror_ref" "$GRANULE_IMAGE"; then
      return 0
    fi
  fi
  if docker pull -q "$GRANULE_IMAGE" >/dev/null; then
    return 0
  fi
  if [[ $GRANULE_IMAGE == ghcr.io/* ]]; then
    if docker pull -q "$mirror_ref" >/dev/null && docker tag "$mirror_ref" "$GRANULE_IMAGE"; then
      return 0
    fi
  fi
  return 1
}

if ! pull_granule_image; then
  fail "pull $GRANULE_IMAGE"
  summary
  exit
fi
docker_export() {
  local cid
  cid=$(docker create "$GRANULE_IMAGE") || return 1
  docker export "$cid"
  docker rm "$cid" >/dev/null
}

run_unshare() {
  SCOPE=unshare
  mkdir -p "$W/img"
  if ! docker_export | tar -C "$W/img" -xf -; then
    fail "unpack $GRANULE_IMAGE"
    return
  fi
  local out
  out=$(timeout 180 unshare --user --map-root-user --net --mount "$0" __unshare-inner "$W" 2>&1)
  note "$(grep -E '^(uid_map|groups):' <<<"$out" | tr '\n' ' ')"
  if grep -q '^READY$' <<<"$out"; then pass "the guest boots and answers granule ready"; else
    fail "the guest boots and answers granule ready" "$(tail -c 600 "$W/unshare-console.log" | tr '\n' ' ')"
  fi
  if grep -q "inet $GUEST_IP/24" <<<"$out"; then pass "the guest holds the namespace's $GUEST_IP"; else
    fail "the guest holds the namespace's $GUEST_IP" "$(tr '\n' ' ' <<<"$out" | cut -c1-400)"
  fi
  if grep -q ' 0% packet loss' <<<"$out"; then pass "the guest reaches $PEER_IP across netshim's splice"; else
    fail "the guest reaches $PEER_IP across netshim's splice" "$(grep -A3 'PING' <<<"$out" | tr '\n' ' ')"
  fi
  if grep -q 'cloud-hypervisor exited cleanly' "$W/unshare-console.log"; then pass "SIGTERM shuts the guest down cleanly"; else
    fail "SIGTERM shuts the guest down cleanly" "$(grep '^granule:' "$W/unshare-console.log" | tail -3 | tr '\n' ' ')"
  fi
}

# crun_config KEEP_GROUPS ARGS_JSON ENV_JSON: the bundle's config.json.
crun_config() {
  local keep=$1 args=$2 env=$3 caps nofile
  caps='["CAP_CHOWN","CAP_DAC_OVERRIDE","CAP_FOWNER","CAP_FSETID","CAP_KILL","CAP_NET_BIND_SERVICE","CAP_SETFCAP","CAP_SETGID","CAP_SETPCAP","CAP_SETUID","CAP_SYS_CHROOT","CAP_NET_ADMIN"]'
  nofile=$(ulimit -Hn)
  jq --argjson caps "$caps" --argjson args "$args" --argjson env "$env" --argjson keep "$keep" \
    --argjson subuid "$SUBUID" --argjson subgid "$SUBGID" --argjson nofile "$nofile" '
    .process.terminal = false
    | .process.user = {uid: 0, gid: 0}
    | .process.args = $args
    | .process.env += $env
    | .process.capabilities = {bounding: $caps, effective: $caps, permitted: $caps}
    | .process.noNewPrivileges = true
    | .process.rlimits = [{type: "RLIMIT_NOFILE", hard: $nofile, soft: $nofile}]
    | .root.readonly = false
    | .annotations = (if $keep then {"run.oci.keep_original_groups": "1"} else {} end)
    | .linux.uidMappings = [{containerID: 0, hostID: $subuid, size: 65536}]
    | .linux.gidMappings = [{containerID: 0, hostID: $subgid, size: 65536}]
    | .linux.namespaces |= map(select(.type != "cgroup"))
    | .mounts |= map(select(.destination != "/sys/fs/cgroup"))
    | .mounts += [{destination: "/run", type: "tmpfs", source: "tmpfs", options: ["nosuid", "nodev", "mode=755"]}]
    | .linux.devices = [
        {path: "/dev/kvm", type: "c", major: 10, minor: 232, fileMode: 432, uid: 0, gid: 0},
        {path: "/dev/net/tun", type: "c", major: 10, minor: 200, fileMode: 438, uid: 0, gid: 0}]
  ' "$W/bundle/spec.json" >"$W/bundle/config.json"
}

crun() { "$CRUN_BIN" --root "$W/crun-state" --cgroup-manager=disabled "$@"; }

# crun_start ARGS_JSON ENV_JSON [KEEP_GROUPS]: start the container in the
# background, console to $W/crun-console.log, and give it a network the
# way a CNI would: a veth in its namespace, peer in another, plumbed from
# outside by the namespace's owner (this caller) through nsenter.
crun_start() {
  local args=$1 env=$2 keep=${3:-true} pid i
  crun_config "$keep" "$args" "$env"
  (cd "$W/bundle" && crun run granule-rootless >"$W/crun-console.log" 2>&1) &
  CRUN_JOB=$!
  for i in $(seq 50); do
    pid=$(crun state granule-rootless 2>/dev/null | jq -r '.pid // empty')
    [[ -n $pid && $pid != 0 ]] && break
    sleep 0.2
  done
  [[ -n ${pid:-} ]] || return 1
  CRUN_PID=$pid
  # The namespace's root is a subuid, which cannot write into $W, so the
  # peer's pid comes back on stdout rather than in a file.
  nsenter -t "$pid" -U -n sh -c "
    set -e
    ip link add eth0 type veth peer name peer0
    unshare --net sleep 600 </dev/null >/dev/null 2>&1 &
    peer=\$!
    echo \$peer
    sleep 0.5
    ip link set peer0 netns \$peer
    nsenter -t \$peer -n sh -c 'ip addr add $PEER_IP/24 dev peer0 && ip link set peer0 up'
    ip addr add $GUEST_IP/24 dev eth0
    ip link set eth0 up
    ip route add default via $PEER_IP
  " >"$W/peer.pid"
}

# crun_stop: SIGTERM, as a runtime's stop would, and wait for the shim to
# shut the guest down and exit.
crun_stop() {
  crun kill granule-rootless TERM 2>/dev/null
  local i
  for i in $(seq 60); do
    kill -0 "$CRUN_JOB" 2>/dev/null || break
    sleep 0.5
  done
  crun delete -f granule-rootless >/dev/null 2>&1
  [[ -f $W/peer.pid ]] && kill "$(cat "$W/peer.pid")" 2>/dev/null
  rm -f "$W/peer.pid"
}

cexec() { crun exec granule-rootless "$@"; }

ENTRY='["/busybox", "sh", "/granule-entry.sh"]'

run_crun() {
  SCOPE=crun
  local missing
  if ! missing=$(need newuidmap newgidmap jq busybox); then
    skip "everything" "missing: $missing (uidmap, jq, busybox-static)"
    return
  fi
  SUBUID=$(awk -F: -v u="$(id -un)" '$1 == u && $3 >= 65536 {print $2; exit}' /etc/subuid)
  SUBGID=$(awk -F: -v u="$(id -un)" '$1 == u && $3 >= 65536 {print $2; exit}' /etc/subgid)
  if [[ -z $SUBUID || -z $SUBGID ]]; then
    skip "everything" "no 65536-id range for $(id -un) in /etc/subuid and /etc/subgid"
    return
  fi
  if [[ -n ${CRUN:-} ]]; then
    CRUN_BIN=$CRUN
  else
    local arch sha
    arch=$(dpkg --print-architecture 2>/dev/null || uname -m)
    sha=CRUN_SHA256_$arch
    if [[ -z ${!sha:-} ]]; then
      skip "everything" "no pinned crun for $arch; set CRUN"
      return
    fi
    CRUN_BIN=$W/crun
    if ! fetch_github "https://github.com/containers/crun/releases/download/$CRUN_VERSION/crun-$CRUN_VERSION-linux-$arch" "$CRUN_BIN" ||
      ! echo "${!sha}  $CRUN_BIN" | sha256sum -c --quiet -; then
      fail "fetch crun $CRUN_VERSION"
      return
    fi
    chmod +x "$CRUN_BIN"
  fi
  note "$("$CRUN_BIN" --version | head -1), container root is host uid $SUBUID"

  # The rootfs is unpacked by the subuids themselves, so it is theirs, the
  # way rootless storage (containers/storage, Silo's) leaves an image.
  mkdir -p "$W/bundle" "$W/crun-state"
  chmod 0777 "$W/bundle"
  if ! docker_export | as_subroot sh -c "mkdir '$W/bundle/rootfs' && tar -C '$W/bundle/rootfs' -xf - && mkdir -p '$W/bundle/rootfs/var/lib/granule/state' '$W/bundle/rootfs/share'"; then
    fail "unpack $GRANULE_IMAGE as subuid $SUBUID"
    return
  fi
  as_subroot sh -c "cat >'$W/bundle/rootfs/usr/local/bin/granule-shim'" <"$W/granule-shim"
  as_subroot sh -c "cat >'$W/bundle/rootfs/busybox' && chmod 0755 '$W/bundle/rootfs/busybox'" <"$(command -v busybox)"
  echo "written by the host" | as_subroot sh -c "cat >'$W/bundle/rootfs/share/hello.txt'"
  local vfs=false
  if [[ -n ${VIRTIOFSD_ROOTFS:-} && -x $VIRTIOFSD_ROOTFS/usr/libexec/virtiofsd ]]; then
    tar -C "$VIRTIOFSD_ROOTFS" -cf - . | as_subroot sh -c "mkdir '$W/bundle/rootfs/vfs' && tar -C '$W/bundle/rootfs/vfs' -xf - 2>/dev/null"
    vfs=true
  fi
  # The entrypoint: wait for the network crun_start plumbs in (its
  # default route is the last of it), start
  # virtiofsd if there is one, then become the shim.
  as_subroot sh -c "cat >'$W/bundle/rootfs/granule-entry.sh'" <<'EOF'
until /busybox ip route | /busybox grep -q '^default'; do /busybox sleep 0.2; done
if [ -x /vfs/usr/libexec/virtiofsd ]; then
  /vfs/lib64/ld-linux-x86-64.so.2 --library-path /vfs/usr/lib/x86_64-linux-gnu \
    /vfs/usr/libexec/virtiofsd --socket-path=/run/virtiofsd.sock --shared-dir=/share \
    --sandbox=chroot --modcaps=-mknod >/run/virtiofsd.log 2>&1 &
  while [ ! -S /run/virtiofsd.sock ]; do /busybox sleep 0.2; done
fi
exec /usr/local/bin/granule-shim run
EOF
  (cd "$W/bundle" && crun spec --rootless && mv config.json spec.json)

  local env='["CHV_MEMORY_MB=512", "CHV_DISK_OVERLAY_PATH=/var/lib/granule/state/overlay.qcow2"]'
  if $vfs; then
    env='["CHV_MEMORY_MB=512", "CHV_DISK_OVERLAY_PATH=/var/lib/granule/state/overlay.qcow2", "CHV_FS=tag=share,socket=/run/virtiofsd.sock"]'
  fi

  # First boot.
  if ! crun_start "$ENTRY" "$env"; then
    fail "start the container" "$(tail -c 400 "$W/crun-console.log")"
    return
  fi
  check "container root is subuid $SUBUID" "0 $SUBUID 65536" sh -c "tr -s ' ' </proc/$CRUN_PID/uid_map | sed 's/^ //'"
  local capeff
  capeff=$(awk '/^CapEff/ {print $2}' "/proc/$CRUN_PID/status")
  if [[ -z $capeff ]] || (((0x$capeff >> 21) & 1)); then fail "the container holds no CAP_SYS_ADMIN" "CapEff $capeff"; else
    pass "the container holds no CAP_SYS_ADMIN (CapEff $capeff)"
  fi
  note "in the container: $(cexec /busybox sh -c 'id; /busybox ls -ln /dev/kvm' 2>&1 | tr '\n' ' ')"
  if cexec /usr/local/bin/granule-shim ready -timeout 60s; then pass "the guest boots and answers granule ready"; else
    fail "the guest boots and answers granule ready" "$(tail -c 600 "$W/crun-console.log" | tr '\n' ' ')"
    crun_stop
    return
  fi
  check "the guest holds the namespace's $GUEST_IP" "inet $GUEST_IP/24" cexec /usr/local/bin/granule-shim exec -- ip -4 -o addr show eth0
  check "the guest reaches $PEER_IP across netshim's splice" " 0% packet loss" cexec /usr/local/bin/granule-shim exec -u root -- ping -c 3 -W 2 $PEER_IP
  check "the overlay is created on the subuid-owned rootfs" "$SUBUID $SUBUID" sh -c "stat -c '%n %u %g' '$W/bundle/rootfs/var/lib/granule/state/overlay.qcow2'"
  cexec /usr/local/bin/granule-shim exec -u root -- sh -c 'echo first-boot >/root/rootless-marker && sync' >/dev/null 2>&1
  if $vfs; then
    check "the guest reads a virtio-fs share from an unprivileged virtiofsd" "written by the host" \
      cexec /usr/local/bin/granule-shim exec -u root -- sh -c 'mkdir -p /mnt/share && mount -t virtiofs share /mnt/share && cat /mnt/share/hello.txt'
    cexec /usr/local/bin/granule-shim exec -u root -- sh -c 'echo from-the-guest >/mnt/share/back.txt && chown 1234:1234 /mnt/share/back.txt' >/dev/null 2>&1
    check "a guest write to the share lands as a subuid on the host" "$((SUBUID + 1234)) $((SUBGID + 1234))" \
      sh -c "stat -c '%u %g' '$W/bundle/rootfs/share/back.txt'"
  else
    skip "virtio-fs through an unprivileged virtiofsd" "VIRTIOFSD_ROOTFS unset"
  fi
  crun_stop
  check "SIGTERM shuts the guest down cleanly" "cloud-hypervisor exited cleanly" cat "$W/crun-console.log"

  # Second boot: the overlay from the first is found, reused and kept.
  if ! crun_start "$ENTRY" "$env"; then
    fail "restart the container" "$(tail -c 400 "$W/crun-console.log")"
    return
  fi
  if cexec /usr/local/bin/granule-shim ready -timeout 60s; then
    check "a second boot keeps the guest's disk" "first-boot" cexec /usr/local/bin/granule-shim exec -u root -- cat /root/rootless-marker
  else
    fail "a second boot keeps the guest's disk" "the guest did not come back: $(tail -c 400 "$W/crun-console.log" | tr '\n' ' ')"
  fi
  crun_stop

  # Without the host's groups, the shim's preflight refuses before
  # anything boots (so the container exits non-zero, which is the point),
  # and says what to change -- unless the device is open
  # to everyone, in which case there is nothing to refuse.
  if [[ $(stat -c %a /dev/kvm) == *6 ]]; then
    skip "without keep_original_groups the shim names the fix" "/dev/kvm is mode $(stat -c %a /dev/kvm) on this host"
  else
    crun_config false '["/usr/local/bin/granule-shim", "run"]' '["CHV_NET_MODE=none", "CHV_DISK_OVERLAY_PATH=/var/lib/granule/state/overlay.qcow2"]'
    check "without keep_original_groups the shim names the fix" "keep_original_groups" \
      sh -c "cd '$W/bundle' && timeout 60 '$CRUN_BIN' --root '$W/crun-state' --cgroup-manager=disabled run granule-rootless-nogroups; true"
    "$CRUN_BIN" --root "$W/crun-state" delete -f granule-rootless-nogroups >/dev/null 2>&1
  fi
}

for mode in "${MODES[@]}"; do
  case $mode in
    unshare) run_unshare ;;
    crun) run_crun ;;
    *) SCOPE=setup fail "unknown mode $mode (want unshare or crun)" ;;
  esac
done
summary
