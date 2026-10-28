# Tunnel vs direct benchmark

Measures a sandbox's control-plane traffic to the controller over the routes
grain's interfaces design (task 481) can choose between:

| carrier | what carries the bytes |
|---|---|
| `direct` | the sandbox container on a Docker bridge of its own (`bench-net`, 192.168.77.0/24) reaching the controller at its gateway, as a granule VM reaches 192.168.9.1 today |
| `docker-exec` | `docker exec -i` stdio into the sandbox container, with `pkg/tunnel/mux` on top: the shape of `granule forward` (gke-labs/granule task 480) without the agent→vsock hop |
| `kubectl-exec` | `kubectl exec -i` stdio into a pod on a GKE cluster, with `pkg/tunnel/mux` on top |
| `kubectl-port-forward` | `pkg/tunnel/mux` over one connection through `kubectl port-forward` to the same pod |

In every tunnelled carrier the sandbox runs `tunnelbench agent`, which listens
on `127.0.0.1:4295` in the sandbox and turns each TCP connection into a mux
stream; the controller runs `tunnelbench forward`, which runs the carrier
command (or dials the port-forward) and splices each stream to the endpoint
server. That is the direction grain's traffic goes: the sandbox dials out.

## Pieces

- `tests/bench/tunnelbench/` -- one static Go binary (under `tests/` so the gate builds and vets it; `scripts/` is not a Go test shard):
  - `serve`: the controller's endpoints. `/rpc` answers an MCP `tools/call`-sized
    JSON-RPC request; `/sse` is a stub model upstream streaming a
    2,000-token completion with no model latency; `/bulk?n=` sends n bytes;
    `/git/` is `git http-backend` over a mirror of bwsalmon/grain (the git
    proxy's stand-in); `/files/` is a GOPROXY tree of the repo's modules (the
    package cache's stand-in for Go).
  - `forward` / `agent`: the two ends of the tunnel.
  - `load`: one workload from inside the sandbox, one JSON line out.
  - `cpu`: CPU seconds of processes by command name.
- `tunnel-vs-direct.sh` -- the driver (`prepare`, `local`, `remote POD`,
  `fanout POD`, `soak POD DUR`). Results go to `$BENCH_DIR/results/*.jsonl`
  (default `BENCH_DIR=/var/tmp/bench`). `QUICK=1` makes every workload small,
  for checking the harness itself.
- `summarize.py` -- the report's tables from those results.

## Workloads

- **rpc**: MCP-sized JSON-RPC round trips; 1, 8 and 32 concurrent streams.
- **sse**: streamed 2,000-token completions; 1, 8 and 32 at once.
- **bulk**: one 1 GiB stream; 16 x 64 MiB streams at once.
- **replay**: a package manager's fetch pattern with the real sizes --
  `npm ci` of `ui/` (378 tarballs, 16 at a time, npm's own concurrency) and
  `apt-get install postgresql-15` on Debian bookworm (36 debs, one at a time,
  as apt fetches from one host). Replayed rather than run because what the
  carrier sees is the bytes and the request pattern; npm's and dpkg's own
  install work is the same on every carrier.
- **git**: real `git clone --depth 1`, full `git clone`, and `git fetch` of 100
  commits onto a clone 100 commits behind, over smart HTTP.
- **go mod download**: the real command for the repo's `go.mod`, against the
  GOPROXY tree.

Sandbox CPU is the load generator's (or the tool's) plus the tunnel agent's.
Controller CPU is the endpoint server's, the forward process's and its
carrier command's (`docker exec`/`kubectl` client), plus `dockerd`,
`containerd*` and every `kubectl` on the controller.

## Running it

Local carriers need Docker and Go:

    scripts/bench/tunnel-vs-direct.sh prepare
    scripts/bench/tunnel-vs-direct.sh local
    scripts/bench/summarize.py

Remote carriers need a GKE cluster and a pod whose image has `git`, `go` and
`bash` (`mirror.gcr.io/library/golang:1.26-bookworm` pulls on a private node
without NAT). The cluster is created as `scripts/gke-cluster-smoke.sh` does
(private nodes, which this project's org policy requires, and a public
control-plane endpoint with no authorized networks so the controller can
reach it):

    gcloud container clusters create bench-tunnel --zone=us-central1-a \
      --num-nodes=1 --machine-type=n2-standard-8 --enable-nested-virtualization \
      --disk-size=50 --disk-type=pd-balanced --no-enable-autoupgrade \
      --service-account="$(gcloud config get-value account)" \
      --enable-private-nodes --enable-ip-alias --master-ipv4-cidr=172.31.X.Y/28 \
      --no-enable-master-authorized-networks
    kubectl run bench-sbx --image=mirror.gcr.io/library/golang:1.26-bookworm \
      --restart=Never -- sleep infinity
    scripts/bench/tunnel-vs-direct.sh remote bench-sbx
    scripts/bench/tunnel-vs-direct.sh fanout bench-sbx
    scripts/bench/tunnel-vs-direct.sh soak bench-sbx 60m
    gcloud container clusters delete bench-tunnel --zone=us-central1-a --quiet

Delete the cluster afterwards.
