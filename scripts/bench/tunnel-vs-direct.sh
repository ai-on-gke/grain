#!/usr/bin/env bash
# Measure a sandbox's control-plane traffic over the direct bridge route and
# over byte-stream tunnels. scripts/bench/README.md says what each carrier
# is; docs/test-runs/2026-10-24-tunnel-vs-direct.md is what it measured.
#
#   scripts/bench/tunnel-vs-direct.sh prepare        # data under $BENCH_DIR
#   scripts/bench/tunnel-vs-direct.sh local          # carriers 1 and 2
#   scripts/bench/tunnel-vs-direct.sh remote POD     # carriers 3 and 4
#   scripts/bench/tunnel-vs-direct.sh fanout POD     # 1/8/32 kubectl exec tunnels
#   scripts/bench/tunnel-vs-direct.sh soak POD DUR   # idle + busy tunnel soak
#
# Results are JSON lines in $BENCH_DIR/results/<carrier>.jsonl; summarize.py
# turns them into the report's tables.
set -euo pipefail

BENCH_DIR="${BENCH_DIR:-/var/tmp/bench}"
IMAGE="${IMAGE:-mirror.gcr.io/library/golang:1.26-bookworm}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$BENCH_DIR/tunnelbench"
SERVER_PORT=4295
STATS_PORT=4296
# A short run (QUICK=1) for checking the harness itself.
QUICK="${QUICK:-0}"

mkdir -p "$BENCH_DIR/results"

build() {
  (cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./tests/bench/tunnelbench)
}

prepare() {
  build
  mkdir -p "$BENCH_DIR/git" "$BENCH_DIR/lists"
  # The git proxy's stand-in: a mirror of bwsalmon/grain, plus a copy whose
  # main is 100 commits behind so a fetch has something to bring in.
  local origin="${GRAIN_REMOTE:-https://github.com/ai-on-gke/grain.git}"
  if [[ ! -d "$BENCH_DIR/git/grain.git" ]]; then
    git clone -q --mirror "$origin" "$BENCH_DIR/git/grain.git"
  fi
  if [[ ! -d "$BENCH_DIR/git/grain-old.git" ]]; then
    git clone -q --bare --single-branch -b main "$BENCH_DIR/git/grain.git" "$BENCH_DIR/git/grain-old.git"
    git -C "$BENCH_DIR/git/grain-old.git" update-ref refs/heads/main \
      "$(git -C "$BENCH_DIR/git/grain.git" rev-parse main~100)"
    git -C "$BENCH_DIR/git/grain-old.git" reflog expire --expire=now --all
    git -C "$BENCH_DIR/git/grain-old.git" gc -q --prune=now
  fi
  # The package cache's stand-in for Go: a GOPROXY file tree of the repo's
  # modules, served at /files/.
  (cd "$REPO_ROOT" && GOMODCACHE="$BENCH_DIR/gomod" GOFLAGS=-modcacherw go mod download)
  cp "$REPO_ROOT/go.mod" "$REPO_ROOT/go.sum" "$BENCH_DIR/lists/"
  # go.mod replaces granule with a directory; only its go.mod is read.
  mkdir -p "$BENCH_DIR/lists/third_party/granule"
  cp "$REPO_ROOT/third_party/granule/go.mod" "$BENCH_DIR/lists/third_party/granule/"
  # npm and apt are replayed: the sizes a real `npm ci` of ui/ and a real
  # `apt-get install postgresql-15` fetch, fetched at their concurrency.
  python3 - "$REPO_ROOT/ui/package-lock.json" > "$BENCH_DIR/lists/npm-urls.txt" <<'EOF'
import json, sys
lock = json.load(open(sys.argv[1]))
print("\n".join(sorted({v["resolved"] for k, v in lock["packages"].items() if k and v.get("resolved")})))
EOF
  # GET rather than HEAD: the registry leaves Content-Length off some HEADs.
  xargs -P 32 -n 1 curl -sSL -m 60 -o /dev/null -w '%{size_download}\n' \
    < "$BENCH_DIR/lists/npm-urls.txt" > "$BENCH_DIR/lists/npm-sizes.txt"
  docker run --rm "$IMAGE" sh -c 'apt-get update -qq >/dev/null && apt-get install --print-uris -qq -y postgresql-15' \
    | awk '/^.http/ {print $3}' > "$BENCH_DIR/lists/apt-sizes.txt"
}

start_server() {
  pkill -f "tunnelbench serve" || true
  "$BIN" serve -addr ":$SERVER_PORT" -git-root "$BENCH_DIR/git" -files "$BENCH_DIR/gomod/cache/download" \
    > "$BENCH_DIR/server.log" 2>&1 &
  sleep 1
}

# controller_cpu: the controller's CPU seconds so far -- the endpoint
# server, the tunnel's forward process and its carrier command (docker exec,
# kubectl), and the daemons a local carrier passes through.
calc() { awk "BEGIN { printf \"%.3f\", $* }"; }

controller_cpu() {
  local s f d
  s="$(curl -sf "http://127.0.0.1:$SERVER_PORT/rusage" || echo 0)"
  f="$(curl -sf "http://127.0.0.1:$STATS_PORT/" || echo 0)"
  d="$("$BIN" cpu -comm dockerd,containerd,kubectl)"
  calc "$s + $f + $d"
}

# run CARRIER URL EXEC... :: FLAGS -- one load workload. "::" rather than
# "--" because EXEC may hold a "--" of its own (kubectl exec POD --).
run() {
  local carrier="$1" url="$2"
  shift 2
  local exe=()
  while [[ "$1" != "::" ]]; do exe+=("$1"); shift; done
  shift
  local c0 c1 out
  c0="$(controller_cpu)"
  out="$("${exe[@]}" /usr/local/bin/tunnelbench load -carrier "$carrier" -url "$url" "$@")"
  c1="$(controller_cpu)"
  echo "$out" | jq -c --argjson cc "$(calc "$c1 - $c0")" '. + {controller_cpu_s: $cc}' \
    | tee -a "$BENCH_DIR/results/$carrier.jsonl"
}

# tool CARRIER URL NAME EXEC... :: PRE SCRIPT: one real tool run in the
# sandbox, three times, after an untimed PRE; records SCRIPT's wall time and
# the sandbox's CPU (the tool's own plus the tunnel agent's).
tool() {
  local carrier="$1" url="$2" name="$3"
  shift 3
  local exe=()
  while [[ "$1" != "::" ]]; do exe+=("$1"); shift; done
  shift
  local pre="$1" script="$2" i c0 c1 a0 a1 t
  for i in 1 2 3; do
    "${exe[@]}" bash -c "U=$url; chmod -R u+w /tmp/w 2>/dev/null; rm -rf /tmp/w; mkdir -p /tmp/w; cd /tmp/w; $pre" >/dev/null 2>&1
    c0="$(controller_cpu)"
    a0="$("${exe[@]}" /usr/local/bin/tunnelbench cpu -comm tunnelbench)"
    t="$("${exe[@]}" bash -c "U=$url; cd /tmp/w; TIMEFORMAT='%R %U %S'; { time { $script ; } >/dev/null 2>&1 ; } 2>&1 | tail -1")"
    a1="$("${exe[@]}" /usr/local/bin/tunnelbench cpu -comm tunnelbench)"
    c1="$(controller_cpu)"
    read -r wall u s <<<"$t"
    jq -nc --arg carrier "$carrier" --arg w "$name" --argjson i "$i" --argjson wall "$wall" \
      --argjson sc "$(calc "$u + $s + $a1 - $a0")" --argjson cc "$(calc "$c1 - $c0")" \
      '{carrier: $carrier, workload: $w, rep: $i, wall_s: $wall, sandbox_cpu_s: $sc, controller_cpu_s: $cc}' \
      | tee -a "$BENCH_DIR/results/$carrier.jsonl"
  done
}

# suite CARRIER URL EXEC...: every workload over one carrier. EXEC runs a
# command in the sandbox (docker exec CTR, kubectl exec POD --). The tool
# scripts' $U is the sandbox's own, expanded there rather than here.
# shellcheck disable=SC2016
suite() {
  local carrier="$1" url="$2"
  shift 2
  local exe=("$@")
  local n1=500 n8=200 n32=100 s1=10 s8=5 s32=3 big=$((1 << 30))
  if [[ "$QUICK" == 1 ]]; then n1=50 n8=20 n32=10 s1=2 s8=1 s32=1 big=$((64 << 20)); fi
  : > "$BENCH_DIR/results/$carrier.jsonl"
  run "$carrier" "$url" "${exe[@]}" :: -workload rpc -c 1 -n "$n1"
  run "$carrier" "$url" "${exe[@]}" :: -workload rpc -c 8 -n "$n8"
  run "$carrier" "$url" "${exe[@]}" :: -workload rpc -c 32 -n "$n32"
  run "$carrier" "$url" "${exe[@]}" :: -workload sse -c 1 -n "$s1"
  run "$carrier" "$url" "${exe[@]}" :: -workload sse -c 8 -n "$s8"
  run "$carrier" "$url" "${exe[@]}" :: -workload sse -c 32 -n "$s32"
  run "$carrier" "$url" "${exe[@]}" :: -workload bulk -c 1 -size "$big"
  run "$carrier" "$url" "${exe[@]}" :: -workload bulk -c 16 -size $((64 << 20))
  run "$carrier" "$url" "${exe[@]}" :: -workload replay -c 16 -list /tmp/lists/npm-sizes.txt
  run "$carrier" "$url" "${exe[@]}" :: -workload replay -c 1 -list /tmp/lists/apt-sizes.txt
  tool "$carrier" "$url" git-clone-shallow "${exe[@]}" :: true 'git clone -q --depth 1 $U/git/grain.git'
  tool "$carrier" "$url" git-clone-full "${exe[@]}" :: true 'git clone -q $U/git/grain.git'
  tool "$carrier" "$url" git-fetch-100 "${exe[@]}" :: 'git clone -q $U/git/grain-old.git g' 'cd g && git fetch -q $U/git/grain.git main'
  tool "$carrier" "$url" go-mod-download "${exe[@]}" :: true 'cp -r /tmp/lists/go.mod /tmp/lists/go.sum /tmp/lists/third_party . && GOPROXY=$U/files GOSUMDB=off GOFLAGS=-mod=mod GOMODCACHE=/tmp/w/m go mod download'
}

# sandbox_container: the local stand-in for a granule VM's container, on a
# bridge of its own whose gateway is the controller (as 192.168.9.1 is).
sandbox_container() {
  docker rm -f bench-sbx >/dev/null 2>&1 || true
  docker network inspect bench-net >/dev/null 2>&1 ||
    docker network create --subnet 192.168.77.0/24 --gateway 192.168.77.1 bench-net >/dev/null
  docker run -d --name bench-sbx --network bench-net "$IMAGE" sleep infinity >/dev/null
  docker cp "$BIN" bench-sbx:/usr/local/bin/tunnelbench
  docker cp "$BENCH_DIR/lists" bench-sbx:/tmp/lists
}

local_carriers() {
  build
  start_server
  sandbox_container
  suite direct "http://192.168.77.1:$SERVER_PORT" docker exec bench-sbx
  "$BIN" forward -target "127.0.0.1:$SERVER_PORT" -stats "127.0.0.1:$STATS_PORT" -- \
    docker exec -i bench-sbx /usr/local/bin/tunnelbench agent -listen "127.0.0.1:$SERVER_PORT" \
    > "$BENCH_DIR/forward-docker.log" 2>&1 &
  local fwd=$!
  sleep 2
  suite docker-exec "http://127.0.0.1:$SERVER_PORT" docker exec bench-sbx
  kill "$fwd"
}

# The pod exists already (README); this copies the harness into it.
remote_setup() {
  local pod="$1"
  build
  start_server
  kubectl cp "$BIN" "$pod:/usr/local/bin/tunnelbench"
  kubectl cp "$BENCH_DIR/lists" "$pod:/tmp/lists"
}

remote_carriers() {
  local pod="$1" fwd pf
  remote_setup "$pod"
  "$BIN" forward -target "127.0.0.1:$SERVER_PORT" -stats "127.0.0.1:$STATS_PORT" -- \
    kubectl exec -i "$pod" -- /usr/local/bin/tunnelbench agent -listen "127.0.0.1:$SERVER_PORT" \
    > "$BENCH_DIR/forward-kexec.log" 2>&1 &
  fwd=$!
  sleep 3
  suite kubectl-exec "http://127.0.0.1:$SERVER_PORT" kubectl exec "$pod" --
  kill "$fwd"

  kubectl exec "$pod" -- sh -c "nohup /usr/local/bin/tunnelbench agent -listen 127.0.0.1:$SERVER_PORT -mux-listen 127.0.0.1:9000 >/tmp/agent-pf.log 2>&1 &"
  kubectl port-forward "pod/$pod" 19000:9000 > "$BENCH_DIR/port-forward.log" 2>&1 &
  pf=$!
  sleep 2
  "$BIN" forward -target "127.0.0.1:$SERVER_PORT" -stats "127.0.0.1:$STATS_PORT" -dial 127.0.0.1:19000 \
    > "$BENCH_DIR/forward-pf.log" 2>&1 &
  fwd=$!
  sleep 2
  suite kubectl-port-forward "http://127.0.0.1:$SERVER_PORT" kubectl exec "$pod" --
  kill "$fwd" "$pf"
}

# soak POD DUR: an idle and a busy kubectl-exec tunnel side by side for DUR.
# The idle one is asked once a minute, the busy one ten times a second;
# every drop and recovery is logged.
soak() {
  local pod="$1" dur="$2"
  remote_setup "$pod"
  local p
  for p in 8710 8711; do
    "$BIN" forward -target "127.0.0.1:$SERVER_PORT" -- \
      kubectl exec -i "$pod" -- /usr/local/bin/tunnelbench agent -listen "127.0.0.1:$p" \
      > "$BENCH_DIR/soak-forward-$p.log" 2>&1 &
  done
  sleep 3
  kubectl exec "$pod" -- /usr/local/bin/tunnelbench load -carrier soak-idle -url http://127.0.0.1:8710 \
    -workload soak -dur "$dur" -interval 60s > "$BENCH_DIR/results/soak-idle.jsonl" 2> "$BENCH_DIR/soak-idle.log" &
  kubectl exec "$pod" -- /usr/local/bin/tunnelbench load -carrier soak-busy -url http://127.0.0.1:8711 \
    -workload soak -dur "$dur" -interval 100ms > "$BENCH_DIR/results/soak-busy.jsonl" 2> "$BENCH_DIR/soak-busy.log" &
  wait
}

# fanout POD: N sandboxes rather than N streams -- N kubectl exec tunnels at
# once, each carrying one MCP-sized call at a time -- plus how long a new
# kubectl exec session takes to set up, which is the floor on a reconnect.
fanout() {
  local pod="$1" n i pids loads
  remote_setup "$pod"
  : > "$BENCH_DIR/results/fanout.jsonl"
  for n in ${FANOUT:-1 8 32}; do
    pids=()
    for ((i = 0; i < n; i++)); do
      "$BIN" forward -target "127.0.0.1:$SERVER_PORT" -- \
        kubectl exec -i "$pod" -- /usr/local/bin/tunnelbench agent -listen "127.0.0.1:$((8720 + i))" \
        > "$BENCH_DIR/fanout-forward-$i.log" 2>&1 &
      pids+=($!)
    done
    sleep $((3 + n / 4))
    loads=()
    for ((i = 0; i < n; i++)); do
      kubectl exec "$pod" -- /usr/local/bin/tunnelbench load -carrier "kubectl-exec-x$n" \
        -url "http://127.0.0.1:$((8720 + i))" -workload rpc -c 1 -n 200 \
        >> "$BENCH_DIR/results/fanout.jsonl" &
      loads+=($!)
    done
    wait "${loads[@]}" || true
    kill "${pids[@]}" 2>/dev/null || true
    sleep 2
  done
  for ((i = 0; i < 10; i++)); do
    { TIMEFORMAT='%R'; time kubectl exec "$pod" -- true; } 2>&1 | tail -1
  done > "$BENCH_DIR/results/exec-setup.txt"
}

case "${1:-}" in
  fanout) fanout "$2" ;;
  prepare) prepare ;;
  local) local_carriers ;;
  remote) remote_carriers "$2" ;;
  soak) soak "$2" "$3" ;;
  *) echo "usage: $0 prepare | local | remote POD | soak POD DUR" >&2; exit 2 ;;
esac
