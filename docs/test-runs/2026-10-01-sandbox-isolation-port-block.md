# Sandbox network isolation and the fixed sandbox port block on a real sandbox (2026-10-01)

Walkthrough of `docs/test-plans/2026-10-01.md` §6 against `main` at
`2771cad65`, which contains every commit the plan names: `c3b8c7c0e`
`93702cc71` `81c8c9f06` `e18a4c613` `9a153282e` `07d076fe2` `e62a44b52`
`b2505035a` `77dd6da67` `1dc3b7a5d` `f6b6647fe`. Task 1415.

## Result

Mostly a pass, with two bugs found and fixed on this branch, and four
observations that need a decision rather than a fix.

- **Fixed:** NetworkNone on granule failed every Acquire on Docker 29
  (`docker inspect` prints an unset IPv6 address as `invalid IP`). It
  failed closed, but NetworkNone could not be used at all.
- **Fixed:** the granule unit-test fakes ran a guest's root maintenance
  scripts on the test host. One `go test ./pkg/orchestrator` restarted
  the host's docker until systemd stopped retrying.
- Once the first fix was in, the NetworkOpen/NetworkNone allow/deny
  matrix matched expectations on real KVM VMs. Fail-closed held, the
  local container hardening held, and the port block behaved as
  described, with one exception: a taken git proxy port stops more than
  the git proxy (finding 3).

## Environment

| | |
|---|---|
| Host | Grain sandbox VM, Debian 12 (bookworm), kernel `6.1.0-53-amd64`, 4 vCPU (Intel Xeon 6985P-C), 7.8 GiB |
| KVM | `/dev/kvm` present (`crw-rw---- root:kvm`); the run user is in `kvm`. **Backend: granule VMs** (real cloud-hypervisor under nested KVM). `-container-sandboxes` was exercised as well. |
| Docker | 29.8.1 (API 1.56), docker0 `10.240.0.1/24`. The default bridge has **no IPv6**. |
| Guest images | `ghcr.io/bwsalmon/grain/guest:latest` (built 2026-09-25) and `ghcr.io/bwsalmon/grain/guest:main` (built 2026-10-01 07:47, after `9a153282e`). The matrix below is from `:main`; see finding 4 for `:latest`. |
| Container sandbox image | `grain-probe:local`: `debian:bookworm-slim` plus curl, iproute2, python3, procps, libcap2-bin and iputils-ping |
| grain | `go build ./cmd/grain` at `main` `2771cad65`; granule CLI from `third_party/granule` |

**Does the unprivileged VM container still boot cloud-hypervisor? Yes.**
Every VM in this run booted and answered `granule exec`. Their containers
(`docker inspect`) had `Privileged=false`, `CapDrop=[ALL]`,
`CapAdd=[CAP_NET_ADMIN]`, devices `/dev/kvm` and `/dev/net/tun` only, and
`SecurityOpt=[no-new-privileges]`. PID 1 in each container showed
`CapEff: 0000000000001000` (NET_ADMIN only) and `NoNewPrivs: 1`.

## Method, and what is not a real dispatch

No production agent framework can be scripted, and a full dispatch also
needs a GitHub stand-in and an agent binary. So the probes do not go
through a task. Instead, `cmd/grain/sandbox_isolation_real_test.go`
(opt-in, `GRAIN_ISOLATION_PROBE=1`, run as root) builds the sandbox side
of a dispatch from the daemon's own code:

- `startPackageCache` behind `NewDirectWithPortBlock(docker0, 9728)`;
- its resolver attached through `sandboxDNSEgress`;
- an `orchestrator.GranuleSandboxes` with `GitProxyHost` and `DNSEgress`
  set the way `runDaemon` sets them;
- `orchestrator.ConfigurePackageCacheTransparent`, which is what
  `applyPackageCache` calls on a dispatch.

It then `Acquire`s one NetworkOpen and one NetworkNone VM and runs
`sandboxProbeScript` in each through `RunInSandbox`, the same transport
an agent's tool calls use. Acquire is the step that applies NetworkNone:
resolver restriction, host nft chain, guest route removal and the git
proxy check.

The daemon's dispatch handling of a failed Acquire (retry, park, the
error shown on the task) is therefore **not** exercised here. Only the
Acquire error itself is.

Run:

```
go test -c -o /tmp/grain.test ./cmd/grain
sudo env PATH=/tmp/gbin:$PATH HOME=/root GRAIN_ISOLATION_PROBE=1 \
  GRAIN_ISOLATION_PROBE_IMAGE=ghcr.io/bwsalmon/grain/guest:main \
  GRAIN_ISOLATION_PROBE_CONTAINER_IMAGE=grain-probe:local \
  GRAIN_ISOLATION_PROBE_GATEWAY=10.240.0.1 \
  /tmp/grain.test -test.run TestSandboxIsolationProbes -test.v
```

### Probe script

This is the exact text of `sandboxProbeScript` in the harness. It runs as
the guest's ordinary user (`debian`, passwordless sudo, as every grain
guest has):

- `uname -r`, `ip -4 route`, `ip -6 route`, `ip -br addr`,
  `/etc/resolv.conf`.
- `grep -E '^(CapEff|CapBnd|NoNewPrivs)' /proc/self/status`.
- `curl -m3 http://169.254.169.254/`.
- **Route restored:** `sudo ip route add default via $GW`, then the
  metadata curl again and `curl -m5 http://1.1.1.1/`, then the route is
  deleted again. This tests the host-side half that the guest cannot
  undo.
- `getent ahosts example.com`, `getent ahostsv6 example.com`,
  `getent hosts api.github.com`, `getent hosts deb.debian.org`.
- An IPv6 DNS query to the gateway resolver, if the guest has an IPv6
  default route.
- `curl -m5 https://example.com/`, `curl -m5 http://1.1.1.1/`,
  `curl -m5 https://api.github.com/zen`.
- Through the package cache's own URL: `GET $CACHE/github/repos/golang/go`
  and `POST $CACHE/github/repos/golang/go/issues`.
- `curl https://deb.debian.org/debian/dists/bookworm/Release` (DNS
  interception) and `curl $CACHE/npm/left-pad`.
- **Gateway ARP spoof:** a raw `AF_PACKET` socket on the interface
  `ip route get $GW` names sends two ARP replies:
  - sender IP = gateway, Ethernet and ARP source = the guest's own MAC;
  - the same with a forged source MAC `02:00:00:de:ad:01`.

  After both VMs have sent theirs, the first VM's `ip neigh show $GW` is
  compared with docker0's MAC.

`containerProbeScript` (local containers) adds:

- `/sys/fs/cgroup/pids.max`;
- a Python fork loop of up to 6000 sleeping children (each killed and
  reaped afterwards);
- an `AF_PACKET` raw socket open;
- `su -c id root`.

## Matrix

Guest image `:main`. The GCE metadata rule from `deploy.sh` was installed
on the host for the final run (finding 5).

| Probe | Expected | NetworkOpen VM | NetworkNone VM | Local container |
|---|---|---|---|---|
| CapEff / NoNewPrivs of the probe process | (container: none / 1) | 0 / 0 (guest user; in-VM sudo is by design) | 0 / 0 | **CapEff 0, CapBnd 0, NoNewPrivs 1** ✅ |
| VM container privileges (host side) | NET_ADMIN + kvm/tun only | ✅ CapEff `0x1000`, NoNewPrivs 1 | ✅ same | n/a |
| pids limit | 4096 (containers) | n/a (VM containers carry no `PidsLimit`) | n/a | ✅ `pids.max` 4096, fork refused after 4093 children |
| `curl 169.254.169.254` | denied | denied with the deploy rule ✅ (**200 without it**, finding 5) | denied (no route) ✅ | denied with the deploy rule ✅ (200 without it) |
| metadata with the default route put back by root | denied | — | **denied** (nft drop) ✅ | — |
| internet (1.1.1.1) with the route put back by root | — | 301 | **denied** (nft drop counter rose) ✅ | — |
| DNS `example.com` A / AAAA | Open: resolves; None: NXDOMAIN | resolves ✅ | NXDOMAIN (`getent` exit 2) ✅ | — |
| DNS `api.github.com` | None: NXDOMAIN | resolves | NXDOMAIN ✅ | — |
| DNS `deb.debian.org` (intercepted) | resolves to the cache | `10.240.0.1` ✅ | `10.240.0.1` ✅ | — |
| IPv6 DNS / IPv6 egress | None: denied | **skipped**: no IPv6 on this docker bridge | **skipped** (same) | — |
| `https://example.com` | Open: allowed; None: denied | 200 ✅ | can't resolve ✅ | 200 ✅ |
| `http://1.1.1.1` (literal IP) | Open: allowed; None: denied | 301 ✅ | connect refused, no route ✅ | — |
| `https://api.github.com/zen` | None: denied | timeout (this sandbox host cannot reach GitHub directly: environment) | can't resolve ✅ | — |
| `POST $CACHE/github/.../issues` | refused | 405 ✅ | 405 ✅ | — |
| `GET $CACHE/github/...` | allowed, read-only | 404 from upstream path mapping (it reached the handler) | 404 (same) | — |
| package fetch via interception (`deb.debian.org` Release) | allowed | 200 ✅ | 200 ✅ | — |
| package fetch via cache URL (`npm/left-pad`) | allowed | 200 ✅ | 200 ✅ | — |
| gateway ARP spoof + forged MAC | not delivered | ✅ not redirected (tap ingress u32 filters: the spoofed sender IP never matches); the neighbour's gateway entry stays docker0's `da:37:ad:6f:5b:b8` | ✅ same | raw socket refused (`EPERM`, no CAP_NET_RAW) ✅ |
| setuid escalation | refused | — | — | `su: cannot set groups: Operation not permitted` ✅ |
| NetworkNone Acquire | refused | — | — | ✅ refused: "the local container backend cannot withhold the internet while retaining access to grain's git proxy" |

Host-side state while the VMs were held: `nft list table inet
grain_network_none` showed one `ip saddr 10.240.0.4 jump vm_g_9002_1`,
with the chain `ip daddr 10.240.0.0/24 accept; ip daddr 10.240.0.1
accept; counter drop` (5 packets dropped by the route-restored probes).
No link-local or IPv6 accept was present (`c3b8c7c0e`). Each tap's
ingress carried three u32 filters: IPv4 with the guest's MAC and source
address; ARP with the guest's MAC, sender MAC and sender IP; and ARP
with sender IP 0.0.0.0. There was no catch-all redirect.

### Fail-closed (§6 step 3)

`findNFTBinary` falls back to `/usr/sbin/nft` and `/sbin/nft`, so a PATH
shim alone cannot hide nft. Two variants were run.

1. **A failing `nft` first on PATH** (`exit 1`, "refusing"). The
   NetworkNone Acquire failed:

   > granule VM "g-9002-1" was to have no network access, and this
   > host's firewall could not be set to drop its traffic (installing
   > nftables chain vm_g_9002_1: exit status 1 (nft shim: refusing …))
   > -- refusing to hand over a sandbox whose only isolation would be a
   > default route its own guest has root enough to put back. NetworkNone
   > on granule needs nft on this host…

2. **`/usr/sbin/nft` moved aside** (this throwaway sandbox only, restored
   afterwards). The same error, with "nft binary not found".

In both cases the VM was torn down: no `granule-vm-*` container was
left, and no probe ran in it. ✅

## Port block (§6 step 4)

Real `grain daemon` built from `main`, run with
`-granule-sandboxes -granule-ssh-user debian -granule-workspace
/home/debian -granule-git-proxy-host 10.240.0.1` and otherwise
defaults.

- Startup log: `every sandbox-facing service binds a reserved port in
  9728-9983 -- whatever has to reach them needs that one range (the
  package cache's interception listeners and resolver are outside it,
  on 443, 80 and 53)` ✅
- `ss -ltnp` showed the six services: git proxy `*:9728` (+0), package
  cache `*:9729` (+1), Actions cache `*:9730` (+2), claude `*:9744`
  (+16), openai `*:9745` (+17), gemini `*:9746` (+18). UI on
  `127.0.0.1:8420`. ✅
- Restart: identical ports. ✅
- One offset port occupied, then restart:

| Occupied | Other services | Log |
|---|---|---|
| 9729 (package cache) | all others up ✅ | `not running a package cache: listen tcp 0.0.0.0:9729: bind: address already in use -- sandboxes will download their packages … from the internet` |
| 9745 (openai proxy) | all others up ✅ | `not serving every model proxy: openai: listen tcp 0.0.0.0:9745: bind: address already in use -- a task granted the capability for one that is missing will be refused` |
| 9730 (Actions cache) | all others up ✅ | `not keeping an Actions cache: listen tcp 0.0.0.0:9730: bind: address already in use -- every CI job will build from cold` |
| 9728 (git proxy) | **only 9729, 9730 and the UI up; no model proxies; no dispatch** (finding 3) | `starting git proxy: listen tcp 0.0.0.0:9728: bind: address already in use -- the UI/API server above is still up, but nothing is dispatching or reconciling tasks until this is fixed and the process is restarted` |

- `-sandbox-port-base=0`: all six services moved to ephemeral ports
  (32787–40683, inside `ip_local_port_range` 32768–60999), with the log
  line `-sandbox-port-base=0: every sandbox-facing service takes an
  ephemeral port, renumbered on each restart`. ✅

## Findings

### 1. NetworkNone granule Acquire fails on Docker 29 (fixed)

Reproduction on docker 29.8.1:

```
$ docker run -d --name iptest busybox sleep 300
$ docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}|{{.IPPrefixLen}}|{{.Gateway}}|{{.GlobalIPv6Address}}|{{.GlobalIPv6PrefixLen}}|{{.IPv6Gateway}}{{"\n"}}{{end}}' iptest
10.240.0.2|24|10.240.0.1|invalid IP|0|invalid IP
```

Docker 29 holds these fields as `netip.Addr`, and a template prints its
zero value as `invalid IP`. `parseGuestNetworks` rejected that, so every
NetworkNone Acquire failed with:

> sandbox resolver could not be told to stop forwarding its queries
> upstream (docker inspect granule-vm-g-9002-1: invalid container IP
> "invalid IP")

This happened before any rule was written. It failed closed, but
NetworkNone was unusable on such a host.

Fix: `unsetAddr` in `pkg/orchestrator/granule_firewall.go` reads
`invalid IP` and `<nil>` as absent. The captured line is now a case in
`TestGranuleFirewall_ParseGuestNetworks`.

### 2. Granule unit tests ran guest root maintenance on the host (fixed)

Found when docker died in the middle of this walkthrough. The host's sudo
journal showed that `go test ./pkg/orchestrator` itself ran:

```
sudo -n sed -i -e s#172.30.255.1/24#10.240.0.1/24#g -e s#172.31.0.0/16#10.241.0.0/16#g /etc/docker/daemon.json
sudo -n systemctl restart docker
```

It did this once per granule test, until `docker.service` hit
`start-limit-hit` and stayed down.

Deterministic reproduction on a grain sandbox, before the fix:
`go test ./pkg/orchestrator -run '^TestSandboxImageReplacesTheImageInTheCreateArgs$'`
causes exactly one docker restart (`journalctl -u docker | grep -c
Stopping` goes up by one).

Cause: the fake `docker exec` runs the guest's argv on the host.
`guestDockerNetworkRepair`'s target falls back to the host's default
gateway, `172.17.0.1`. The sandbox's `daemon.json` names
`172.17.0.1:39319` as its registry mirror, so the "conflict" matches and
the "repair" runs. `guestKVMGrant` (usermod) and `guestNetworkNoneCommand`
(`ip route del default`) go through the same `sudo -n`.
`cmd/grain`'s fakes had the same problem, and a Release's nft cleanup ran
the host's real `nft` through sudo.

Fix: the fakes in `pkg/orchestrator/granule_sandboxes_test.go`,
`cmd/grain/daemon_granule_wiring_test.go` and
`cmd/grain/daemon_true_e2e_test.go` now put a refusing `sudo` ahead of
the real one for guest commands. The wiring tests also get a no-op
`nft`.

Verified with the host's sudo journal: the full `pkg/orchestrator` suite
and `cmd/grain -run 'Granule|Sandbox|Dispatch|Network'` now run **no**
sudo command on the host. Before the fix there was one docker restart
per granule test, plus nft list, flush and delete calls.

### 3. A taken git-proxy port stops more than the git proxy (fixed)

With 9728 occupied, the daemon serves the UI and the two caches. It
starts no model proxy and dispatches nothing, and it says so plainly.
Failing dispatch is defensible, since every dispatch needs the git
proxy. But the plan's claim that "a taken port fails only that service"
is true only for the package cache, the Actions cache and the model
proxies. The early return also skips the model proxies, which does not
look intended.

Fixed in grain/task-1518: the model proxies now start in `run()`, beside
the two caches, so a taken 9728 fails the git proxy and halts dispatch
(deliberately, since nothing can be checked out without it) while the
UI, both caches and all three model proxies stay up.
`cmd/grain/daemon_port_taken_test.go` drives `run()` with the git
proxy's reserved port held and asserts exactly that.

### 4. A source-built daemon runs a guest that predates the ARP guard (observation)

An unstamped build resolves `ghcr.io/bwsalmon/grain/guest:latest` ("stamped
into this build; nothing pinned one"). `scripts/release-images.sh` only
moves `:latest` on a `latest` or `v*` release ref, and the current one
was built on 2026-09-25.

With that image, the tap had only the old catch-all redirect. The
NetworkNone guest's gratuitous ARP for the gateway **poisoned the Open
guest's neighbour cache**: its gateway entry became the spoofer's MAC
`ea:fb:4f:b0:b0:0f` instead of docker0's `da:37:ad:6f:5b:b8`.

`9a153282e` lives in the netshim inside the guest image, not in the
daemon, so it protects a deployment only once that deployment's guest
image carries it. Release builds stamp their own `sha-` guest, so this is
expected behaviour of `:latest`. It is recorded so nobody reads a
dev-build ARP result as a regression.

### 5. GCE metadata is blocked only by `deploy.sh` (observation)

`81c8c9f06` is a host rule installed by
`deploy/terraform/gcp/files/deploy.sh`: raw PREROUTING
`-d 169.254.169.254 -j DROP`. A daemon started any other way, including
`scripts/setup.sh` host installs, leaves it reachable from NetworkOpen
guests and containers. On this host a metadata endpoint answered with
200.

With exactly the `deploy.sh` rule installed, containers and the Open
guest timed out, and the host itself still reached the endpoint (as
designed). NetworkNone guests are blocked by the nft chain regardless.
Whether `setup.sh` should install the same rule is a deployment
decision, not something to change here.

Decided in grain/task-1519: `scripts/setup.sh` now installs the same
script and unit (`ensure_metadata_block`) on any GCE instance, detected
from DMI, before it starts anything. `GRAIN_BLOCK_METADATA=1` forces it on
other hosts and `GRAIN_BLOCK_METADATA=0` opts out. It is not done by the
daemon at startup, because the daemon is not root and must not be able to
write raw PREROUTING.

### 6. Smaller observations

- A NetworkNone chain accepts the guest's whole docker segment
  (`10.240.0.0/24`), so a NetworkNone guest can still reach other
  sandboxes' VMs on the same bridge. This is by design ("its own
  segment"); the ARP guard stops impersonation, not reachability.
- VM containers carry no pids limit. Only `-container-sandboxes` sets
  `--pids-limit 4096`.
- `GET $CACHE/github/...` is still allowed read-only from NetworkNone, by
  design (`93702cc71`). `api.github.com` neither resolves nor connects
  from NetworkNone.

## Skipped, and why

- **IPv6 DNS and IPv6 egress for NetworkNone.** This host's docker bridge
  has no IPv6, so the guests had only link-local IPv6 and no IPv6 route.
  There was nothing to send an IPv6 query over. The ruleset was checked
  for the absence of an IPv6 accept, but the path was not exercised.
  **Skipped, not passed.**
- **Open-guest reachability of `api.github.com`.** This sandbox host
  cannot reach GitHub directly, so the timeout says nothing about grain.
- **Dispatch-level handling of a refused NetworkNone Acquire** (task
  parked or failed, error shown on the task). Not exercised; see Method.
- **"A sandbox that outlives a restart still works."** Ports were shown
  identical across restarts. A VM was not kept alive across a daemon
  restart.

## Tests run

- `go test ./pkg/orchestrator/` passed (twice, before and after the
  fixture change).
- `go test ./cmd/grain/ -run 'Sandbox|PortBlock|Fabric|Isolation|Granule|Dispatch|Network'`
  passed.
- `make security` (`go vet ./...` plus `tests/security` under `-race`)
  passed.
- The harness ran as above: both probe tests `--- PASS` on `:main` with
  the Docker 29 fix.
