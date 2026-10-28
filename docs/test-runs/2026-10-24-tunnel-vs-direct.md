# Tunnel vs direct: sandbox control-plane traffic, 24 October 2026

grain/task-483, for the interfaces design (task 481) and its follow-up (task
482). The question: which of a sandbox's traffic to the controller (MCP, git
proxy, package cache, model proxy, bulk) should take the **direct** bridge
route and which can go through a **tunnel**. The tunnels measured are
`docker exec -i`, `kubectl exec -i` and `kubectl port-forward`, each carrying
`pkg/tunnel/mux`.

The harness is `scripts/bench/` (README there). All raw numbers below come
from its `results/*.jsonl`, run through `scripts/bench/summarize.py`.

## Summary

- **The local tunnel costs almost nothing in latency.** Over `docker exec -i`
  plus mux, an MCP-sized call takes 0.86 ms at p50, against 0.20 ms direct.
  git and `go mod download` are within 2 to 6% of direct. The costs are
  **throughput** and **CPU**: one stream peaks at about 100 MiB/s and 16
  streams together at about 390 MiB/s, against 3 to 11 GiB/s direct. Each
  GiB costs about 5 CPU-seconds in the sandbox and about 10 on the
  controller, against about 0.3 each direct.
- **The remote tunnels add about 2 to 6 ms on top of distance.** The
  controller here is about 34 ms from the cluster's API server. An MCP-sized
  call takes 39.7 ms at p50 over `kubectl exec` and 36.1 ms over
  `kubectl port-forward`. Latency stayed flat from 1 to 32 concurrent streams
  and from 1 to 64 separate tunnels. Throughput is the hard limit: 19 MiB/s
  for one stream and 38 to 49 MiB/s per tunnel.
- **Recommendation:**
  - Tunnel MCP, git and the model proxy.
  - Send bulk transfers direct whenever a direct route exists.
  - Locally, the package cache can be tunnelled if direct is unavailable,
    but direct is cheaper.
  - For remote sandboxes, **co-locate the package cache with the cluster**
    instead of tunnelling it.
  - Over `kubectl`, stop counting by call concurrency and count by bytes:
    one tunnel is acceptable up to about 40 MiB/s of combined traffic. At 64
    tunnels (128 exec sessions) from one controller, the first session
    failure appeared.

## Environment

| | |
|---|---|
| Controller (all carriers) | grain sandbox VM (cloud-hypervisor guest), 8 vCPU Intel Xeon @ 2.60GHz, 15 GiB, Debian, kernel 6.1.0-54. Runs on GCE in us-west1 (The Dalles, Oregon; egress 35.233.217.181). Docker 29.9.2, Go 1.26.10, kubectl v1.36.5-gke.100, Google Cloud SDK 590.0.0 |
| Local sandbox (carriers 1, 2) | a Docker container (`mirror.gcr.io/library/golang:1.26-bookworm`) on the controller, on its own bridge `bench-net` 192.168.77.0/24 with the controller at the gateway 192.168.77.1 |
| Remote cluster (carriers 3, 4) | `bench-tunnel-483`: GKE 1.36.4-gke.1495002, zonal us-central1-a, one n2-standard-8 node (nested virtualization enabled), Container-Optimized OS, kernel 6.12.105+, containerd 2.2.7. Private nodes (org policy `constraints/container.managed.enablePrivateNodes` requires it), public control-plane endpoint 34.68.114.158, project bsalmon-fun-sandbox. The task's service-account key stopped authenticating ("Invalid JWT Signature") before the delete, so the deletion went to a follow-up task |
| Remote sandbox | a plain pod from the same golang image (see "What was not measured") |
| Distance | controller to API server: TCP connect 33 to 38 ms. Setting up a `kubectl exec POD -- true` session: p50 0.52 s, worst 0.61 s, over 10 tries |
| grain | base `835b687df`. granule vendored in `third_party/granule` as `github.com/bwsalmon/granule v0.0.0-20260921040231-1d6b7e1fad96`, not exercised (see below) |

## Carriers

1. **direct**: the sandbox dials 192.168.77.1:4295 over the Docker bridge.
2. **docker-exec**: `tunnelbench forward -- docker exec -i bench-sbx tunnelbench agent`.
   The sandbox's clients dial 127.0.0.1:4295. Each connection becomes a mux
   stream over the exec's stdio, which the controller splices to the
   endpoint server.
3. **kubectl-exec**: the same, with `kubectl exec -i POD --` in place of
   `docker exec -i`.
4. **kubectl-port-forward**: the agent in the pod accepts one TCP connection
   on :9000. `kubectl port-forward pod/POD 19000:9000` carries it, and the
   controller's forward dials 127.0.0.1:19000 and runs mux on that one
   connection.

In every tunnel the sandbox originates the streams, which is the direction
grain's traffic goes.

## Results

### MCP-sized JSON-RPC round trip

A `tools/call` of `run_command echo hello`, about 130 bytes each way. "conc"
is the number of concurrent streams through one tunnel, each with its own
keep-alive connection.

| carrier | conc | p50 ms | p95 ms | p99 ms | calls/s | errors | sandbox CPU ms/call | controller CPU ms/call |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| direct | 1 | 0.202 | 0.356 | 0.523 | 4503 | 0 | 0.206 | 0.426 |
| docker-exec | 1 | 0.864 | 1.12 | 1.45 | 1123 | 0 | 0.38 | 1.08 |
| kubectl-exec | 1 | 39.7 | 43.4 | 82.4 | 24.4 | 0 | 0.63 | 3.99 |
| kubectl-port-forward | 1 | 36.1 | 39.3 | 77.5 | 26.4 | 0 | 0.362 | 1.73 |
| direct | 8 | 0.245 | 0.605 | 1.3 | 25807 | 0 | 0.0869 | 0.095 |
| docker-exec | 8 | 0.933 | 1.38 | 1.86 | 8044 | 0 | 0.2 | 0.383 |
| kubectl-exec | 8 | 39.9 | 43.7 | 53.9 | 198 | 0 | 0.224 | 0.901 |
| kubectl-port-forward | 8 | 36.5 | 38.3 | 68.1 | 206 | 0 | 0.192 | 0.506 |
| direct | 32 | 0.404 | 1.98 | 2.66 | 50145 | 0 | 0.0597 | 0.055 |
| docker-exec | 32 | 1.45 | 3.35 | 12.6 | 16867 | 0 | 0.124 | 0.2 |
| kubectl-exec | 32 | 40.3 | 42.2 | 45.1 | 785 | 0 | 0.124 | 0.359 |
| kubectl-port-forward | 32 | 36.7 | 48 | 72 | 832 | 0 | 0.144 | 0.362 |

Per-call CPU at conc 1 includes the fixed cost of an idle tunnel
(keep-alive pings, polling) spread over the calls, which is why it falls as
conc rises.

#### N sandboxes, not N streams: separate kubectl exec tunnels

Each tunnel ran `rpc -c 1 -n 200` through its own `kubectl exec -i` agent
session. The load generator inside the pod was started by a second
`kubectl exec`, so the controller held 2N exec sessions to the one API
server.

| tunnels | exec sessions | completed | p50 ms (median of tunnels) | p95 ms (median) | p95 ms (worst tunnel) | p99 ms (worst) | calls/s (all) |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 2 | 1/1 | 36.1 | 38.2 | 38.2 | 53.4 | 27 |
| 8 | 16 | 8/8 | 39.1 | 41.5 | 42.5 | 51.4 | 208 |
| 32 | 64 | 32/32 | 38.5 | 45.5 | 57.8 | 121.6 | 792 |
| 64 | 128 | 63/64 | 39.6 | 50.6 | 65.2 | 141.6 | 1507 |

At 64 tunnels, one of the 128 `kubectl exec` sessions failed to start
(`dial tcp 34.68.114.158:443: connect: connection timed out`). That is the
first failure seen at any concurrency.

### Model proxy: streamed 2,000-token completion (stub upstream)

The stub sends 2,000 SSE `content_block_delta` events (about 240 KB),
flushed one by one, with no model latency between them. Real model
latency (tens of ms per token) is far above every number here.

| carrier | conc | first token p50 ms | whole stream p50 ms | p95 ms | errors | sandbox CPU s | controller CPU s |
|---|---:|---:|---:|---:|---:|---:|---:|
| direct | 1 | 0.408 | 13.6 | 23.7 | 0 | 0.157 | 0.186 |
| docker-exec | 1 | 1.29 | 8.18 | 14.2 | 0 | 0.12 | 0.328 |
| kubectl-exec | 1 | 42.9 | 81.5 | 249 | 0 | 0.193 | 0.526 |
| kubectl-port-forward | 1 | 37.4 | 55.5 | 173 | 0 | 0.132 | 0.48 |
| direct | 8 | 2.53 | 13.8 | 19.8 | 0 | 0.154 | 0.393 |
| docker-exec | 8 | 6.36 | 14.5 | 28.5 | 0 | 0.164 | 0.534 |
| kubectl-exec | 8 | 58 | 90 | 136 | 0 | 0.329 | 1.02 |
| kubectl-port-forward | 8 | 73.8 | 109 | 139 | 0 | 0.321 | 1.36 |
| direct | 32 | 8.45 | 34.7 | 84.3 | 0 | 0.275 | 0.768 |
| docker-exec | 32 | 17.7 | 48 | 74.1 | 0 | 0.305 | 0.738 |
| kubectl-exec | 32 | 80.5 | 132 | 202 | 0 | 0.293 | 0.913 |
| kubectl-port-forward | 32 | 287 | 368 | 695 | 0 | 0.843 | 2.17 |

At conc 1, docker-exec beat direct on whole-stream time. Both are within
noise of a single short run. The 32-stream port-forward row is a single
run, and the only one where port-forward is clearly worse than exec. One
reading: all 32 streams share one TCP connection through one SPDY stream,
so they queue behind each other.

### Bulk: 1 GiB single stream, and 16 × 64 MiB in parallel

| carrier | streams | wall s | MiB/s | errors | sandbox CPU s/GiB | controller CPU s/GiB |
|---|---:|---:|---:|---:|---:|---:|
| direct | 1 | 0.338 | 3032 | 0 | 0.325 | 0.266 |
| docker-exec | 1 | 10.3 | 99.2 | 0 | 5.12 | 10.1 |
| kubectl-exec | 1 | 54.3 | 18.9 | 0 | 5.16 | 16.3 |
| kubectl-port-forward | 1 | 52.5 | 19.5 | 0 | 4.68 | 11.1 |
| direct | 16 | 0.092 | 11187 | 0 | 0.302 | 0.359 |
| docker-exec | 16 | 2.62 | 391 | 0 | 4.67 | 8.56 |
| kubectl-exec | 16 | 27.1 | 37.7 | 0 | 5.5 | 12.1 |
| kubectl-port-forward | 16 | 21 | 48.8 | 0 | 5.04 | 9.61 |

**Remotely, single-stream throughput is limited by mux's flow control, not
by the carrier.** `pkg/tunnel/mux` gives each stream a fixed 1 MiB receive
window (`initialWindow`), so one stream moves at most 1 MiB per round trip.
About 40 to 50 ms through kubectl gives a ceiling of about 20 to 25 MiB/s,
and 19 MiB/s was measured. Sixteen streams reach only 2 to 2.5 times that
(38 to 49 MiB/s). That is the carrier's own ceiling: the API server, then
kubelet's streaming.

Locally the picture is different. The idle round trip is under 1 ms, so the
window would allow about 1 GiB/s, but one stream reached about 100 MiB/s
and sixteen about 390 MiB/s. That points at `docker exec`'s stdio relay
(docker client, dockerd, containerd-shim, process) and at mux's per-frame
CPU, not at the window.

A larger or auto-tuned window would lift the remote single-stream numbers;
it would not lift either per-tunnel ceiling.

### Package cache

The npm and apt rows replay the real fetch: the real sizes, fetched at the
package manager's own concurrency.
- npm: `npm ci` of `ui/` fetches 378 tarballs, 150 MiB, 16 at a time.
- apt: `apt-get install postgresql-15` on bookworm fetches 36 debs, 65 MiB,
  one at a time.

Unpacking and installing cost the same on every carrier, so they are left
out.

| carrier | conc | wall s | MiB/s | errors | p50 ms/object | p95 ms/object | sandbox CPU s/GiB | controller CPU s/GiB |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| npm: direct | 16 | 0.022 | 6779 | 0 | 0.358 | 3.48 | 0.504 | 0.981 |
| npm: docker-exec | 16 | 0.408 | 369 | 0 | 4.14 | 120 | 5.25 | 9.2 |
| npm: kubectl-exec | 16 | 3.55 | 42.3 | 0 | 52.3 | 793 | 5.78 | 15.9 |
| npm: kubectl-port-forward | 16 | 4.18 | 35.9 | 0 | 43.7 | 1125 | 5.66 | 11.3 |
| apt: direct | 1 | 0.031 | 2078 | 0 | 0.385 | 5.08 | 0.519 | 1.62 |
| apt: docker-exec | 1 | 0.714 | 91.1 | 0 | 2.17 | 193 | 5.64 | 11.4 |
| apt: kubectl-exec | 1 | 4.92 | 13.2 | 0 | 42.5 | 895 | 5.59 | 20.3 |
| apt: kubectl-port-forward | 1 | 4.73 | 13.7 | 0 | 39.3 | 808 | 4.83 | 11.4 |

The real `go mod download` of the repo's `go.mod` is in the next table.
It downloads 48 modules (111 MiB of zips) through a GOPROXY file tree and
extracts them.

### Real tools: git and go mod download (median of 3)

Each cell is wall time, with sandbox and controller CPU-seconds in
brackets. The controller's figure leaves out `git http-backend` and
`pack-objects`, which run as children of the endpoint server. That cost is
the same on every carrier, so the differences between columns are the
carrier's own.

| workload | direct | docker-exec | kubectl-exec | kubectl-port-forward |
|---|---:|---:|---:|---:|
| git clone --depth 1 | 2.97 s (sbx 2.2 / ctl 0.3) | 2.99 s (sbx 2.7 / ctl 1.2) | 4.06 s (sbx 1.9 / ctl 1.0) | 3.74 s (sbx 1.9 / ctl 0.7) |
| git clone (full, 182 MiB mirror) | 11.72 s (sbx 20.3 / ctl 0.8) | 12.41 s (sbx 22.6 / ctl 2.6) | 15.97 s (sbx 17.2 / ctl 3.7) | 15.24 s (sbx 17.0 / ctl 2.5) |
| git fetch of 100 commits | 1.60 s (sbx 1.2 / ctl 0.1) | 1.68 s (sbx 1.3 / ctl 0.3) | 2.09 s (sbx 1.1 / ctl 0.2) | 1.95 s (sbx 1.1 / ctl 0.1) |
| go mod download (48 modules) | 4.04 s (sbx 11.3 / ctl 0.1) | 4.30 s (sbx 11.5 / ctl 1.4) | 8.58 s (sbx 10.6 / ctl 2.1) | 8.29 s (sbx 10.3 / ctl 1.4) |

git is bound by server-side pack generation and client-side index work,
not by the carrier. Even remotely, the tunnel adds about 1 s to a shallow
clone and about 4 s to a full one.

### Stability (soak)

Two `kubectl exec -i` tunnels to the pod ran side by side for 60 minutes:
- **idle**: one MCP-sized call a minute.
- **busy**: a call, then 100 ms of sleep, over and over.

Each call had a 10 s timeout. Each tunnel's load generator ran under its own
`kubectl exec`, so four exec sessions to the API server were held for the
whole hour.

| tunnel | calls | errors | drops | p50 ms | p95 ms | p99 ms | sandbox CPU s (load + agent, 1 h) |
|---|---:|---:|---:|---:|---:|---:|---:|
| idle | 60 | 0 | 0 | 40.2 | 71.4 | 167 | 11.8 |
| busy | 25,189 | 0 | 0 | 40.3 | 47.6 | 109 | 22.2 |

No session dropped in the hour, and latency did not drift. Nothing
reconnected, so there was no reconnect time to measure. The floor on a
reconnect is the cost of setting up a new exec session, about 0.5 s (see
Environment). A real `granule forward` or exec tunnel must still
reconnect on its own: the API server can restart, and GKE control-plane
upgrades and node or kubelet restarts will end every exec session.

## Recommendation for task 481

| traffic | route | why |
|---|---|---|
| **MCP** (sandbox MCP server, grain-tool calls back to the controller) | **tunnel is fine**, local or remote | Local: +0.7 ms. Remote: +2 to 6 ms over distance. Flat up to 32 streams and 64 tunnels. Tool calls take seconds, so neither is visible. |
| **git proxy** | **tunnel is fine** | Within 6% of direct locally. Remotely, +1 s on a shallow clone and +4 s on a full one, much of it distance. Pack generation dominates. |
| **model proxy** | **tunnel is fine** | First token +1 ms locally and +3 to 9 ms remotely, against model latency of hundreds of ms. 2k-token streams stay far below any carrier's throughput. Prefer `kubectl exec` over `kubectl port-forward` when many streams share one tunnel (see the 32-stream row). |
| **bulk** (artifacts, uploads, image or layer pulls, anything over about 100 MiB) | **direct whenever available** | A tunnel is 30 to 100 times slower and costs about 15 CPU-seconds per GiB across both ends, against about 0.6 direct. Remotely, one stream does 19 MiB/s. |
| **package cache, local sandboxes** | **prefer direct; tunnel acceptable** | In wall time it hardly matters (`go mod download` 4.0 s vs 4.3 s; the npm fetch 0.02 s vs 0.4 s), but per byte the tunnel costs 10 to 20 times the CPU. A cold cache moves enough bytes for that to add up. |
| **package cache, remote sandboxes** | **co-locate a cache with the cluster** | Tunnelled, `go mod download` doubles (4.0 s to 8.6 s); the npm fetch takes 3.6 s and the apt fetch 4.9 s. Each tarball takes at least one round trip (p50 about 40 to 50 ms, p95 about 0.8 s). It all counts against the same ~40 MiB/s per tunnel that MCP and git share, and costs the controller 11 to 20 CPU-seconds per GiB. A pull-through cache in the cluster's region, reached over pod networking, removes all of that. Keep the tunnel as the fallback route to the controller's cache. |

**Where kubectl carriers stop being acceptable.** Latency is not the
limit: with 64 tunnels, the worst tunnel's p95 was 65 ms against a 36 ms
p50. Two other things are:
- **Bytes per tunnel.** Each tunnel tops out at about 40 to 50 MiB/s, and
  a single stream at about 20 MiB/s. Any one sandbox doing a bulk or
  package transfer saturates its own tunnel and drags its MCP calls up
  behind it.
- **Session count against one API server.** The first session failure
  came at 128 concurrent exec sessions from one controller. Plan for no
  more than about 32 tunnelled sandboxes per controller per cluster until
  that is measured further.

If grain needs either limit raised, it should use a route that does not
pass through the API server.

**Mux follow-up.** The 1 MiB fixed per-stream window in `pkg/tunnel/mux`
caps every single tunnelled stream at window ÷ RTT. If any tunnelled
traffic needs more than about 20 MiB/s remotely, the window wants to grow
with the bandwidth-delay product.

## What was not measured, and why

- **The granule hops.** grain's sandbox (where this ran) is itself a
  granule guest, and the granule CLI is not installed in it, so the local
  carriers are Docker containers, not granule VMs. That leaves out two
  extra hops: the direct route's virtio-net/TAP hop into the guest, and
  the tunnel's agent→vsock hop. The remote pod is a plain pod, not a
  `kubernetes-granule` pod, for the same reason and to keep within the
  run's time. Both missing hops are sub-millisecond, in-host copies; they
  add CPU per byte but no round trips. Re-run with `granule forward`
  (gke-labs/granule task 480) once it lands. Swap the `forward` command in
  `tunnel-vs-direct.sh local` for it; nothing else changes.
- **Carrier 5 (real L3 to the cluster).** The nodes are private with no
  Cloud NAT (org policy), and this controller is outside the VPC, so the
  pod had no direct route to it. Instead, the distance is given by the
  TCP connect time above (33 to 38 ms). The kubectl rows sit 2 to 6 ms
  above it. A controller VM inside the VPC would give the true reference.
- **A 2-hour soak, and a deliberate API-server or kubelet disruption.** The
  soak ran for 60 minutes to fit the run's 2-hour budget. GKE's control
  plane and kubelet cannot be restarted on demand from outside the
  cluster without a node SSH route or an upgrade operation, so no
  disruption was injected. The reconnect floor is the cost of a new exec
  session, measured above: about 0.5 s.
- **Real `npm ci` and `apt-get install`.** These were replayed, as
  described in the package cache section. The installs' own work does not
  depend on the carrier.
- **A separate project for the cluster.** Only bsalmon-fun-sandbox was
  available to this task. The cluster and the controller were in
  different regions, which is the part that matters for distance.
