#!/usr/bin/env bash
# e2e-ipv6.sh - end-to-end IPv6 checks for a granule guest on docker, kind
# and GKE: section 8.3 of docs/ipv6-dual-stack-design.md.
#
# Usage:
#   test/e2e-ipv6.sh [options] TARGET...
#
# Targets:
#   docker      a dual-stack docker network and a v6-only one (--ipv4=false,
#               docker 27 or later; skipped on an older daemon)
#   kind        kind-dual and kind-ipv6
#   kind-dual   a kind cluster with networking.ipFamily: dual
#   kind-ipv6   a kind cluster with networking.ipFamily: ipv6
#   gke         a dual-stack (--stack-type=ipv4-ipv6, Dataplane V2) GKE
#               cluster, created and torn down here. Needs gcloud
#               authenticated against $GKE_PROJECT, and costs money while it
#               runs. GKE offers no IPv6-only stack type, so there is no
#               gke-ipv6. GKE_EXISTING=1 with GKE_NAME runs the checks on a
#               cluster that is already up, and leaves it standing.
#
# Options:
#   --image=REF   the granule image under test (default: $GRANULE_IMAGE, or
#                 granule-e2e:alpine built from this tree with
#                 GUEST_DISTRO=alpine if that is unset and missing)
#   --keep        leave networks, containers and clusters behind for a look
#   -h, --help    this text
#
# The guest has to have busybox's nc, ping, wget and nslookup in it, which
# the alpine guest does and the minbase Debian one does not: the checks run
# a listener inside the guest and probe outwards from it. The image is the
# one thing taken from outside; every check is a statement about it.
#
# Every check prints one line, PASS, FAIL or SKIP, and the run exits non-zero
# if any check failed. A SKIP is a check this host cannot make (no IPv6
# route off the host, a docker too old for --ipv4=false), never a failure
# turned quiet.
#
# What each target checks, and why it is enough:
#
#   docker, on each network: the guest holds the container's v6 address; it
#   pings the network's v6 gateway; a peer container reaches a port in the
#   guest over v6; a port published on [::] reaches the guest, both through
#   ::1 and through the bridge's own v6 address; the guest's resolv.conf
#   holds only nameservers of a family it has (the v6 fallback, on the
#   v6-only network); and the guest resolves a name over v6, from a CoreDNS
#   peer on the same network, and from its own resolver when the host has a
#   route off the box for that family.
#
#   kind and GKE: the pod's v6 address answers from the guest; a
#   SingleStack IPv6 ClusterIP Service in front of it works; the guest
#   resolves kubernetes.default through a v6 kube-dns address (the cluster's
#   own on a v6-only cluster, a SingleStack IPv6 Service in front of the
#   kube-dns pods on a dual-stack one, whose kube-dns is IPv4); and the
#   guest reaches a peer pod, on another node where there is one, over v6 --
#   kindnet's point-to-point routes on kind, Dataplane V2's on GKE.
#
# kind and GKE nodes are given every image they run rather than pulling
# any: a CI runner behind a TLS-intercepting proxy has a docker that trusts
# it and a kind node that does not, and GKE has nowhere to pull a locally
# built granule image from.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

IMAGE="${GRANULE_IMAGE:-}"
KEEP=false
TARGETS=()

PEER_IMAGE="${E2E_PEER_IMAGE:-busybox:1.36}"
COREDNS_IMAGE="${E2E_COREDNS_IMAGE:-coredns/coredns:1.11.3}"
GKE_PROJECT="${GKE_PROJECT:-grain-sandbox-dev}"
GKE_REGION="${GKE_REGION:-us-central1}"
GKE_ZONE="${GKE_ZONE:-${GKE_REGION}-a}"
GKE_NAME="${GKE_NAME:-granule-ipv6-e2e-$(date +%m%d%H%M)}"

# What the guest serves, and the peer pod: fixed strings, so a check that
# gets one back got it from the right end.
TOKEN="granule-v6-ok"
PEER_TOKEN="peer-v6-ok"

usage() { sed -n '2,/^$/s/^# \{0,1\}//p' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --image=*) IMAGE="${1#*=}" ;;
    --keep) KEEP=true ;;
    -h|--help) usage; exit 0 ;;
    docker|kind|kind-dual|kind-ipv6|gke) TARGETS+=("$1") ;;
    *) echo "e2e-ipv6: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done
[ "${#TARGETS[@]}" -gt 0 ] || { usage >&2; exit 2; }

PASSED=0 FAILED=0 SKIPPED=0
FAILURES=()
SCOPE=""
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
    fail "$name" "wanted \"$want\", got: $(tr '\n' ' ' <<<"$out" | cut -c1-300)"
  fi
}

# retry SECONDS CMD...: CMD until it succeeds, once a second.
retry() {
  local deadline=$(( $(date +%s) + $1 ))
  shift
  until "$@" >/dev/null 2>&1; do
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}

need() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || { echo "e2e-ipv6: needs $c on PATH" >&2; exit 2; }
  done
}

CLEANUPS=()
cleanup() {
  local i
  $KEEP && { [ "${#CLEANUPS[@]}" -eq 0 ] || echo "e2e-ipv6: --keep: leaving behind what these would remove: ${CLEANUPS[*]}"; return; }
  for (( i=${#CLEANUPS[@]}-1; i>=0; i-- )); do
    eval "${CLEANUPS[i]}" >/dev/null 2>&1 || true
  done
  CLEANUPS=()
}
trap cleanup EXIT
trap 'exit 130' INT TERM

need docker
if [ -z "$IMAGE" ]; then
  IMAGE=granule-e2e:alpine
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    build=(docker build --build-arg GUEST_DISTRO=alpine -t "$IMAGE")
    # Inside a grain sandbox github.com is reached only through grain's
    # proxy, which the Dockerfile's kernel and cloud-hypervisor downloads
    # take when GITHUB_PROXY names it; deploy/publish-images.sh decides the
    # same way.
    if grep -qs "BEGIN CERTIFICATE" /usr/local/share/ca-certificates/grain-ca.crt /etc/ssl/certs/grain-ca.pem; then
      ns=$(awk '/^nameserver/ && $2 !~ /^(8\.8\.|127\.|0\.)/ {print $2; exit}' /etc/resolv.conf)
      [ -z "$ns" ] || build+=(--build-arg "GITHUB_PROXY=$ns")
    fi
    echo "e2e-ipv6: building $IMAGE from $REPO_ROOT: ${build[*]}"
    "${build[@]}" "$REPO_ROOT" || exit 1
  fi
fi

# The guest's listener: answers any request on port 80 with $TOKEN, after
# reading the request through to its blank line -- a reply that closes with
# the request unread is reset rather than delivered. nc -ll keeps listening
# after each connection; setsid and the redirects let the exec that starts
# it return.
LISTENER='cat > /tmp/granule-e2e-reply <<"EOF"
#!/bin/sh
while IFS= read -r l; do l=${l%$(printf "\r")}; [ -z "$l" ] && break; done
printf "HTTP/1.0 200 OK\r\nContent-Length: '$(( ${#TOKEN} + 1 ))'\r\n\r\n'"$TOKEN"'\n"
EOF
chmod +x /tmp/granule-e2e-reply
setsid nc -ll -p 80 -e /tmp/granule-e2e-reply >/dev/null 2>&1 </dev/null &
sleep 1
netstat -ltn | grep -q ":::80 "'

# The guest's v6 address, without its prefix length.
has_addr6() { "${GX[@]}" "ip -6 addr show dev eth0 scope global" 2>/dev/null | grep -qF "inet6 $1/"; }

# One host-side HTTP GET, curl or busybox wget on the host network.
host_get() {
  if command -v curl >/dev/null 2>&1; then
    curl -sS -g -m 10 "$1"
  else
    docker run --rm --network host "$PEER_IMAGE" wget -qO- -T 10 "$1"
  fi
}

# ---------------------------------------------------------------- docker

docker_network() {
  # docker_network LABEL SUBNET6 [create args...]
  local label="$1" subnet6="$2"
  shift 2
  local net="granule-e2e6-$label" guest="granule-e2e6-$label-vm" dns="granule-e2e6-$label-dns"
  local prefix="${subnet6%::/*}" gw6 dns6 ip6 port out
  gw6="$prefix::1"
  dns6="$prefix::53"
  SCOPE="docker $label"

  docker rm -f "$guest" "$dns" >/dev/null 2>&1
  docker network rm "$net" >/dev/null 2>&1
  if ! out=$(docker network create --ipv6 --subnet "$subnet6" --gateway "$gw6" "$@" "$net" 2>&1); then
    fail "create the network" "$out"
    return
  fi
  CLEANUPS+=("docker network rm $net")

  # A CoreDNS peer that knows one name, so the guest's v6 resolution is
  # checked against a server on its own segment rather than against
  # whatever the host can reach.
  local corefile
  corefile=$(mktemp)
  printf '.:53 {\n  hosts {\n    %s granule-e2e.test\n  }\n}\n' "$prefix::99" > "$corefile"
  chmod 644 "$corefile"
  CLEANUPS+=("rm -f $corefile" "docker rm -f $dns")
  if ! out=$(docker run -d --name "$dns" --network "$net" --ip6 "$dns6" \
      -v "$corefile:/Corefile:ro" "$COREDNS_IMAGE" -conf /Corefile 2>&1); then
    fail "start the CoreDNS peer" "$out"
  fi

  CLEANUPS+=("docker rm -f $guest")
  if ! out=$(docker run -d --name "$guest" --network "$net" \
      --device /dev/kvm --device /dev/net/tun \
      --cap-drop=ALL --cap-add=NET_ADMIN --security-opt no-new-privileges \
      -p '[::]::80' "$IMAGE" 2>&1); then
    fail "start the guest" "$out"
    return
  fi
  GX=(docker exec "$guest" sh -c)

  ip6=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$net\").GlobalIPv6Address}}" "$guest")
  if [ -z "$ip6" ]; then
    fail "the container has a v6 address" "docker gave it none"
    return
  fi
  if retry 90 has_addr6 "$ip6"; then
    pass "the guest holds the container's v6 address $ip6"
  else
    fail "the guest holds the container's v6 address $ip6" "$("${GX[@]}" 'ip -6 addr' 2>&1 | tr '\n' ' ')"
    docker logs --tail 40 "$guest" 2>&1 | sed 's/^/     | /'
    return
  fi
  note "guest v6 routes: $("${GX[@]}" 'ip -6 route show dev eth0' 2>&1 | tr '\n' ';')"

  check "the guest pings the gateway $gw6 over v6" " 0% packet loss" \
    "${GX[@]}" "ping -6 -c 3 -W 2 $gw6"

  if ! out=$("${GX[@]}" "$LISTENER" 2>&1); then
    fail "start a listener on [::]:80 in the guest" "$out"
    return
  fi

  check "a peer container reaches the guest's port 80 over v6" "$TOKEN" \
    docker run --rm --network "$net" "$PEER_IMAGE" wget -qO- -T 10 "http://[$ip6]/"

  port=$(docker port "$guest" 80/tcp | sed -n 's/^\[::\]:\([0-9]*\)$/\1/p' | head -1)
  if [ -z "$port" ]; then
    fail "-p [::]::80 publishes a v6 port" "docker port says: $(docker port "$guest" 80/tcp | tr '\n' ' ')"
  else
    check "-p [::]:$port reaches the guest through [::1]" "$TOKEN" host_get "http://[::1]:$port/"
    check "-p [::]:$port reaches the guest through the bridge's [$gw6]" "$TOKEN" host_get "http://[$gw6]:$port/"
  fi

  # Name resolution. What the guest was given has to be usable: no
  # nameserver of a family it lacks, and on a v6-only network the v6
  # fallback, since docker's own resolver (127.0.0.11) is the container's
  # loopback and no use to the guest.
  local resolv
  resolv=$("${GX[@]}" 'cat /etc/resolv.conf' 2>&1)
  note "guest resolv.conf: $(grep nameserver <<<"$resolv" | tr '\n' ';')"
  if [ "$label" = v6only ]; then
    if grep -q '^nameserver [0-9.]*$' <<<"$resolv" || ! grep -q '^nameserver .*:' <<<"$resolv"; then
      fail "a v6-only guest is given v6 nameservers only" "$(tr '\n' ' ' <<<"$resolv")"
    else
      pass "a v6-only guest is given v6 nameservers only ($(sed -n 's/^nameserver //p' <<<"$resolv" | tr '\n' ' '| sed 's/ $//'))"
    fi
  fi
  check "the guest resolves a name over v6 from a peer nameserver [$dns6]" "$prefix::99" \
    "${GX[@]}" "nslookup granule-e2e.test $dns6"
  local first
  first=$(sed -n 's/^nameserver //p' <<<"$resolv" | head -1)
  case "$first" in
    *:*) if [ -z "$(ip -6 route show default 2>/dev/null)" ]; then
           skip "the guest resolves example.com through its own resolver $first" "this host has no IPv6 default route"
           first=""
         fi ;;
    "") fail "the guest has a nameserver" "$(tr '\n' ' ' <<<"$resolv")"; first="" ;;
  esac
  [ -z "$first" ] || check "the guest resolves example.com through its own resolver $first" "Address" \
    "${GX[@]}" "nslookup example.com"
}

run_docker() {
  SCOPE=docker
  local version major
  version=$(docker version -f '{{.Server.Version}}' 2>/dev/null)
  major=${version%%.*}
  note "docker $version; daemon.json: $(tr -d ' \n' < /etc/docker/daemon.json 2>/dev/null || echo none)"
  # Neither "ipv6": true nor "ip6tables": true is needed in daemon.json for
  # this: --ipv6 on a user-defined network is enough, and ip6tables is on
  # by default from docker 27. A daemon that has it turned off fails the
  # -p checks below, which is the news.
  docker pull -q "$PEER_IMAGE" >/dev/null && docker pull -q "$COREDNS_IMAGE" >/dev/null || {
    fail "pull $PEER_IMAGE and $COREDNS_IMAGE"; return; }
  docker_network dual fd00:dead:beef::/64
  SCOPE="docker v6only"
  if [[ "$major" =~ ^[0-9]+$ ]] && [ "$major" -ge 27 ]; then
    docker_network v6only fd00:dead:cafe::/64 --ipv4=false
  else
    skip "the v6-only network" "--ipv4=false needs docker 27 or later, this is ${version:-unknown}"
  fi
}

# ---------------------------------------------------------------- k8s

DEVICE_PLUGIN_IMAGE=$(grep -o 'ghcr.io/squat/generic-device-plugin@sha256:[0-9a-f]*' "$REPO_ROOT/deploy/k8s/kvm-device-plugin.yaml")
LOCAL_DEVICE_PLUGIN=granule-e2e/generic-device-plugin:pinned

# The workload every cluster runs: the granule pod, a peer pod kept off its
# node where the cluster has another, and SingleStack IPv6 Services in
# front of the granule pod and of kube-dns.
k8s_manifests() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata: {name: granule-e2e6, labels: {app: granule-e2e6}}
spec:
  tolerations: [{key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule}]
  containers:
  - name: vm
    image: $IMAGE
    imagePullPolicy: IfNotPresent
    resources: {limits: {devic.es/kvm: "1", devic.es/tun: "1"}}
    securityContext: {capabilities: {drop: [ALL], add: [NET_ADMIN]}, allowPrivilegeEscalation: false}
---
apiVersion: v1
kind: Pod
metadata: {name: granule-e2e6-peer}
spec:
  tolerations: [{key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule}]
  affinity:
    podAntiAffinity:
      preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector: {matchLabels: {app: granule-e2e6}}
  containers:
  - name: peer
    image: $PEER_IMAGE
    imagePullPolicy: IfNotPresent
    command: [sh, -c, 'mkdir -p /www && echo $PEER_TOKEN > /www/index.html && exec httpd -f -p 8080 -h /www']
---
apiVersion: v1
kind: Service
metadata: {name: granule-e2e6}
spec:
  ipFamilyPolicy: SingleStack
  ipFamilies: [IPv6]
  selector: {app: granule-e2e6}
  ports: [{port: 80, targetPort: 80}]
---
apiVersion: v1
kind: Service
metadata: {name: granule-e2e6-kube-dns, namespace: kube-system}
spec:
  ipFamilyPolicy: SingleStack
  ipFamilies: [IPv6]
  selector: {k8s-app: kube-dns}
  ports:
  - {name: dns, port: 53, protocol: UDP, targetPort: 53}
  - {name: dns-tcp, port: 53, protocol: TCP, targetPort: 53}
EOF
}

# Every kubectl call names its context: a gcloud or kind run elsewhere on
# the host switches the current one under a run in progress.
KCTX=""
kubectl() { command kubectl --context "$KCTX" "$@"; }

# k8s_checks: everything after the cluster exists and has the images.
k8s_checks() {
  local out pod6 peer6 svc6 dns6 nodes
  kubectl apply -f "$REPO_ROOT/deploy/k8s/kvm-device-plugin.yaml" >/dev/null || { fail "apply the device plugin"; return; }
  if [ -n "${K8S_DEVICE_PLUGIN_IMAGE:-}" ]; then
    kubectl -n kube-system set image ds/granule-device-plugin "generic-device-plugin=$K8S_DEVICE_PLUGIN_IMAGE" >/dev/null
  fi
  if ! out=$(kubectl -n kube-system rollout status ds/granule-device-plugin --timeout=180s 2>&1); then
    fail "the device plugin rolls out" "$out"
    return
  fi
  # A cluster that outlives the run (GKE_EXISTING, --keep aside) is left
  # without this run's pods, so the next run boots its own guests rather
  # than checking this one's again.
  kubectl delete pod granule-e2e6 granule-e2e6-peer --ignore-not-found --wait >/dev/null 2>&1
  CLEANUPS+=("kubectl delete pod granule-e2e6 granule-e2e6-peer --ignore-not-found --wait=false" "kubectl delete svc granule-e2e6 --ignore-not-found")
  if ! out=$(k8s_manifests | kubectl apply -f - 2>&1); then
    fail "apply the granule pod, peer and Services" "$out"
    return
  fi
  CLEANUPS+=("kubectl -n kube-system delete svc granule-e2e6-kube-dns --ignore-not-found")
  if ! out=$(kubectl wait --for=condition=Ready pod/granule-e2e6 pod/granule-e2e6-peer --timeout=300s 2>&1); then
    fail "the granule and peer pods are Ready" "$out"
    kubectl describe pod granule-e2e6 granule-e2e6-peer 2>&1 | tail -30 | sed 's/^/     | /'
    return
  fi
  nodes=$(kubectl get pod granule-e2e6 granule-e2e6-peer -o jsonpath='{.items[*].spec.nodeName}')
  note "granule pod and peer on: $nodes"
  GX=(kubectl exec granule-e2e6 -- sh -c)

  pod6=$(kubectl get pod granule-e2e6 -o jsonpath='{.status.podIPs[*].ip}' | tr ' ' '\n' | grep : | head -1)
  peer6=$(kubectl get pod granule-e2e6-peer -o jsonpath='{.status.podIPs[*].ip}' | tr ' ' '\n' | grep : | head -1)
  svc6=$(kubectl get svc granule-e2e6 -o jsonpath='{.spec.clusterIP}')
  if [ -z "$pod6" ] || [ -z "$peer6" ]; then
    fail "the pods have v6 addresses" "granule: '${pod6}', peer: '${peer6}'"
    return
  fi

  if retry 120 has_addr6 "$pod6"; then
    pass "the guest holds the pod's v6 address $pod6"
  else
    fail "the guest holds the pod's v6 address $pod6" "$("${GX[@]}" 'ip -6 addr' 2>&1 | tr '\n' ' ')"
    kubectl logs --tail=40 granule-e2e6 2>&1 | sed 's/^/     | /'
    return
  fi
  note "guest v6 routes: $("${GX[@]}" 'ip -6 route show dev eth0' 2>&1 | tr '\n' ';')"
  if ! out=$("${GX[@]}" "$LISTENER" 2>&1); then
    fail "start a listener on [::]:80 in the guest" "$out"
    return
  fi

  check "the pod's v6 address $pod6 answers from the guest" "$TOKEN" \
    kubectl exec granule-e2e6-peer -- wget -qO- -T 10 "http://[$pod6]/"
  check "a v6 ClusterIP Service [$svc6] reaches the guest" "$TOKEN" \
    kubectl exec granule-e2e6-peer -- wget -qO- -T 10 "http://[$svc6]/"
  check "the guest reaches the peer pod [$peer6] over v6" "$PEER_TOKEN" \
    "${GX[@]}" "wget -qO- -T 10 'http://[$peer6]:8080/'"

  # kubernetes.default through a v6 kube-dns: the guest's own resolver
  # where the cluster's DNS Service is v6, the SingleStack IPv6 one in
  # front of the same pods where it is not.
  dns6=$(kubectl -n kube-system get svc kube-dns -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
  note "guest resolv.conf: $("${GX[@]}" 'cat /etc/resolv.conf' 2>&1 | tr '\n' ';')"
  # The name in full: busybox nslookup applies no search list, and a v6-only
  # kind cluster's CoreDNS answers a bare "kubernetes.default." with the
  # SERVFAIL of an upstream it has no route to.
  local kname=kubernetes.default.svc.cluster.local kip
  kip=$(kubectl get svc kubernetes -o jsonpath='{.spec.clusterIP}')
  case "$dns6" in
    *:*) check "the guest resolves kubernetes.default through its v6 kube-dns [$dns6]" "$kip" \
           "${GX[@]}" "nslookup $kname" ;;
    *)   dns6=$(kubectl -n kube-system get svc granule-e2e6-kube-dns -o jsonpath='{.spec.clusterIP}')
         check "the guest resolves kubernetes.default through a v6 kube-dns Service [$dns6]" "$kip" \
           "${GX[@]}" "nslookup $kname $dns6" ;;
  esac
}

# kind_load IMAGE...: each image as a single-platform archive. "kind load
# docker-image" fails on a multi-platform image in a containerd-backed
# docker ("content digest ... not found"); an archive of the one platform
# does not.
kind_load() {
  local cluster="$1" img tar
  shift
  tar=$(mktemp)
  for img in "$@"; do
    docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null || return 1
    docker save --platform "linux/$(docker version -f '{{.Server.Arch}}')" "$img" -o "$tar" 2>/dev/null ||
      docker save "$img" -o "$tar" || return 1
    kind load image-archive "$tar" --name "$cluster" >/dev/null || return 1
  done
  rm -f "$tar"
}

run_kind() {
  local family="$1" cluster="granule-e2e6-$1" cfg out
  SCOPE="kind $family"
  need kind kubectl
  cfg=$(mktemp)
  cat > "$cfg" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: $family
nodes:
- role: control-plane
- role: worker
EOF
  kind delete cluster --name "$cluster" >/dev/null 2>&1
  CLEANUPS+=("kind delete cluster --name $cluster" "rm -f $cfg")
  KCTX="kind-$cluster"
  if ! out=$(kind create cluster --name "$cluster" --config "$cfg" --wait 3m 2>&1); then
    fail "create the cluster" "$(tail -5 <<<"$out" | tr '\n' ' ')"
    return
  fi
  docker pull -q "$DEVICE_PLUGIN_IMAGE" >/dev/null && docker tag "$DEVICE_PLUGIN_IMAGE" "$LOCAL_DEVICE_PLUGIN"
  if ! kind_load "$cluster" "$IMAGE" "$PEER_IMAGE" "$LOCAL_DEVICE_PLUGIN"; then
    fail "load the images into the cluster"
    return
  fi
  K8S_DEVICE_PLUGIN_IMAGE="$LOCAL_DEVICE_PLUGIN" k8s_checks
}

# ---------------------------------------------------------------- gke

# gke_import IMAGE...: each image straight into every node's containerd,
# through a privileged pod per node that enters the host's mount namespace.
# The project need not have a registry the cluster can pull from, and the
# image under test is usually one nobody has published.
gke_import() {
  local tar pods pod img
  tar=$(mktemp)
  kubectl apply -f - >/dev/null <<EOF || return 1
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: granule-e2e6-importer, namespace: kube-system}
spec:
  selector: {matchLabels: {app: granule-e2e6-importer}}
  template:
    metadata: {labels: {app: granule-e2e6-importer}}
    spec:
      hostPID: true
      tolerations: [{operator: Exists}]
      containers:
      - name: importer
        image: $PEER_IMAGE
        command: [sleep, "86400"]
        securityContext: {privileged: true}
        volumeMounts: [{name: drop, mountPath: /drop}]
      volumes: [{name: drop, hostPath: {path: /var/lib/granule-e2e6, type: DirectoryOrCreate}}]
EOF
  CLEANUPS+=("kubectl -n kube-system delete ds granule-e2e6-importer --ignore-not-found")
  kubectl -n kube-system rollout status ds/granule-e2e6-importer --timeout=300s >/dev/null || return 1
  pods=$(kubectl -n kube-system get pods -l app=granule-e2e6-importer -o name)
  for img in "$@"; do
    docker save --platform linux/amd64 "$img" -o "$tar" 2>/dev/null || docker save "$img" -o "$tar" || return 1
    for pod in $pods; do
      kubectl -n kube-system cp "$tar" "${pod#pod/}:/drop/image.tar" || return 1
      kubectl -n kube-system exec "${pod#pod/}" -- nsenter -t 1 -m -- \
        ctr -n k8s.io images import /var/lib/granule-e2e6/image.tar >/dev/null || return 1
      kubectl -n kube-system exec "${pod#pod/}" -- rm -f /drop/image.tar
    done
  done
  rm -f "$tar"
}

run_gke() {
  local net="$GKE_NAME" out
  SCOPE="gke dual"
  need gcloud kubectl
  local g=(gcloud --quiet --project "$GKE_PROJECT")
  note "project $GKE_PROJECT, zone $GKE_ZONE, cluster $GKE_NAME${GKE_EXISTING:+ (existing, left standing)}"
  KCTX="gke_${GKE_PROJECT}_${GKE_ZONE}_${GKE_NAME}"
  if [ -n "${GKE_EXISTING:-}" ]; then
    gke_credentials && gke_workload
    return
  fi

  # A network of its own, with a dual-stack subnet (ULA internal IPv6),
  # Private Google Access and Cloud NAT: the project's org policy requires
  # private nodes, which otherwise reach neither Google's images nor the
  # peer's.
  CLEANUPS+=("${g[*]} compute networks delete $net")
  "${g[@]}" compute networks create "$net" --subnet-mode=custom --enable-ula-internal-ipv6 >/dev/null &&
  CLEANUPS+=("${g[*]} compute networks subnets delete $net --region $GKE_REGION") &&
  "${g[@]}" compute networks subnets create "$net" --network "$net" --region "$GKE_REGION" \
    --range 10.90.0.0/20 --stack-type IPV4_IPV6 --ipv6-access-type INTERNAL \
    --enable-private-ip-google-access >/dev/null &&
  CLEANUPS+=("${g[*]} compute routers delete $net --region $GKE_REGION") &&
  "${g[@]}" compute routers create "$net" --network "$net" --region "$GKE_REGION" >/dev/null &&
  "${g[@]}" compute routers nats create "$net" --router "$net" --region "$GKE_REGION" \
    --auto-allocate-nat-external-ips --nat-all-subnet-ip-ranges >/dev/null || {
    fail "create the dual-stack network"; return; }

  CLEANUPS+=("${g[*]} container clusters delete $GKE_NAME --zone $GKE_ZONE")
  if ! out=$("${g[@]}" container clusters create "$GKE_NAME" --zone "$GKE_ZONE" \
      --network "$net" --subnetwork "$net" --enable-ip-alias \
      --stack-type ipv4-ipv6 --enable-dataplane-v2 --enable-private-nodes \
      --num-nodes 2 --machine-type n2-standard-4 --image-type COS_CONTAINERD \
      --enable-nested-virtualization --no-enable-autoupgrade --enable-dns-access \
      --labels purpose=granule-ipv6-e2e 2>&1); then
    fail "create the cluster" "$(tail -3 <<<"$out" | tr '\n' ' ')"
    return
  fi
  gke_credentials && gke_workload
}

# The DNS-based control-plane endpoint rather than the IP one: a host whose
# egress goes through a proxy that admits names, not addresses, reaches
# only the former.
gke_credentials() {
  gcloud --quiet --project "$GKE_PROJECT" container clusters get-credentials "$GKE_NAME" \
    --zone "$GKE_ZONE" --dns-endpoint >/dev/null || { fail "get the cluster's credentials"; return 1; }
}

gke_workload() {
  docker pull -q "$PEER_IMAGE" >/dev/null
  if ! gke_import "$IMAGE"; then
    fail "import $IMAGE into the nodes"
    return
  fi
  k8s_checks
}

# ---------------------------------------------------------------- main

for t in "${TARGETS[@]}"; do
  case "$t" in
    docker) run_docker ;;
    kind) run_kind dual; cleanup; run_kind ipv6 ;;
    kind-dual) run_kind dual ;;
    kind-ipv6) run_kind ipv6 ;;
    gke) run_gke ;;
  esac
  cleanup
done

echo
echo "e2e-ipv6: $PASSED passed, $FAILED failed, $SKIPPED skipped"
for f in "${FAILURES[@]}"; do echo "  FAILED $f"; done
[ "$FAILED" -eq 0 ]
