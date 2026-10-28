#!/usr/bin/env bash
# forward-e2e.sh -- check `granule forward` against a real VM, both ways.
#
#   test/forward-e2e.sh <vm|container/name|pod/name> [granule forward flags]
#
# e.g.  test/forward-e2e.sh web
#       test/forward-e2e.sh container/myvm
#       test/forward-e2e.sh pod/granule-gke-example -c web -n default
#
# Needs: a running granule VM whose guest image carries this tree's
# granule-agent (granule guest rebase brings an older one up to date),
# `granule` on PATH (go build -o granule ./cmd/granule), python3 here, and
# busybox nc or python3 in the guest. It works whether the guest has a
# network or not -- try it on a CHV_NET_MODE=none VM, which is the case
# the feature exists for.
#
# What it does:
#   1. starts a tiny HTTP server on this machine, on a free port;
#   2. runs granule forward -R <guest 127.0.0.1:18080> -> that server,
#      and -L <free local port> -> <guest 127.0.0.1:18081>;
#   3. in the guest, fetches http://127.0.0.1:18080/ (guest -> caller);
#   4. in the guest, starts a one-shot listener on 127.0.0.1:18081 and
#      connects to it from here through the local port (caller -> guest).
set -euo pipefail

target=${1:?usage: $0 <vm|container/name|pod/name> [granule forward flags]}
shift
extra=("$@")

workdir=$(mktemp -d)
cleanup() {
  [[ -n ${fwd_pid:-} ]] && kill "$fwd_pid" 2>/dev/null || true
  [[ -n ${http_pid:-} ]] && kill "$http_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# How to run a command in the guest, for each kind of target.
guest_exec() {
  case $target in
    pod/*) kubectl exec -i "${target#pod/}" "${kube_args[@]}" -- granule exec -- "$@" ;;
    container/*) docker exec -i "${target#container/}" granule exec -- "$@" ;;
    *) granule vm exec "$target" -- "$@" ;;
  esac
}
kube_args=()
for ((i = 0; i < ${#extra[@]}; i++)); do
  case ${extra[i]} in
    -n|-c|-context|-kubeconfig)
      flag=${extra[i]}; [[ $flag == -context || $flag == -kubeconfig ]] && flag=-$flag
      kube_args+=("$flag" "${extra[i+1]}"); i=$((i + 1)) ;;
  esac
done

echo "forward-e2e: hello from the caller" > "$workdir/index.html"
python3 -u -m http.server --bind 127.0.0.1 0 --directory "$workdir" > "$workdir/http.log" 2>&1 &
http_pid=$!
for _ in $(seq 50); do
  port=$(sed -n 's/.*port \([0-9]*\).*/\1/p' "$workdir/http.log" | head -1)
  [[ -n $port ]] && break
  sleep 0.1
done
[[ -n ${port:-} ]] || { echo "http server did not start" >&2; exit 1; }

granule forward "$target" "${extra[@]}" -json \
  -R "127.0.0.1:18080=127.0.0.1:$port" \
  -L "127.0.0.1:0=127.0.0.1:18081" > "$workdir/bindings.json" &
fwd_pid=$!
for _ in $(seq 600); do
  [[ -s $workdir/bindings.json ]] && break
  kill -0 "$fwd_pid" 2>/dev/null || { echo "granule forward exited before binding" >&2; exit 1; }
  sleep 0.1
done
local_addr=$(python3 -c 'import json,sys; print([b["listen"] for b in json.load(open(sys.argv[1]))["bindings"] if b["direction"]=="local"][0])' "$workdir/bindings.json")
echo "bindings: $(cat "$workdir/bindings.json")"

echo "guest -> caller:"
got=$(guest_exec sh -c 'wget -qO- http://127.0.0.1:18080/index.html 2>/dev/null || python3 -c "import urllib.request;print(urllib.request.urlopen(\"http://127.0.0.1:18080/index.html\").read().decode(),end=\"\")"')
[[ $got == "forward-e2e: hello from the caller" ]] || { echo "FAIL: guest fetched '$got'" >&2; exit 1; }
echo "  ok: $got"

echo "caller -> guest:"
guest_exec sh -c 'echo "forward-e2e: hello from the guest" | (nc -l -s 127.0.0.1 -p 18081 || nc -l 127.0.0.1 18081) >/dev/null 2>&1' &
listener_pid=$!
got=""
for _ in $(seq 50); do
  got=$(python3 - "$local_addr" <<'PY' || true
import socket, sys
host, port = sys.argv[1].rsplit(":", 1)
s = socket.create_connection((host, int(port)), timeout=5)
s.shutdown(socket.SHUT_WR)
data = b""
while chunk := s.recv(4096):
    data += chunk
print(data.decode().strip())
PY
)
  [[ -n $got ]] && break
  sleep 0.2
done
wait "$listener_pid" 2>/dev/null || true
[[ $got == "forward-e2e: hello from the guest" ]] || { echo "FAIL: caller read '$got'" >&2; exit 1; }
echo "  ok: $got"
echo "PASS"
