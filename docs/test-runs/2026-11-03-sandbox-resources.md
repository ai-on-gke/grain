# 2026-11-03 — Sandbox resources walkthrough (test plan §8)

Walkthrough of [docs/test-plans/2026-11-03.md](../test-plans/2026-11-03.md) §8
"What is NOT proved" (PRs #527, #532, #442, #524, #483, #522, #510), run by
task 2714 in a grain sandbox VM. Scripts are in
[`2026-11-03-sandbox-resources/`](2026-11-03-sandbox-resources/); the two
Go walkthroughs are opt-in tests in `pkg/orchestrator`.

## Environment

| | |
|---|---|
| Kernel | `6.1.0-54-amd64` (Debian), 7 vCPU Intel Xeon @ 2.60GHz, 30 GiB RAM, no swap, 30 GB root disk |
| cgroups | v2 (`cgroup2fs`) |
| `/dev/kvm` | present (`crw-rw---- root kvm`, user in `kvm`) |
| Docker | 29.9.2 (client and server), user in `docker` |
| kind | v0.32.0, node image `kindest/node:v1.36.1` (pinned by `scripts/prepare-real-k8s-ci.sh`) |
| Calico | v3.33.0 (pinned manifest, as the `real kubernetes` job) |
| kubectl | client v1.35.9 |
| Go | 1.26.10 |
| Guest image | `ghcr.io/bwsalmon/grain/guest:inputs-fd821ce5…` pulled by `scripts/granule-guest-resolve.sh` (cache hit), tagged `grain-guest:e2e-test` |
| codex credentials | none in the sandbox |

Extra packages installed: `iproute2 iptables nftables dnsmasq-base e2fsprogs socat`
(what the real-vm job installs), and `zstd` by `prepare-real-k8s-ci.sh`.

## Results

| # | Item | Outcome |
|---|---|---|
| 1a | Container cgroup `memory.max` = guest + max(1 GiB, 25%) on a real Granule VM | **Pass**: 2 GiB guest, `memory.max` = 3221225472 (3072 MiB) |
| 1b | VMM survives a page-cache-heavy multi-GB write | **Pass, but the run does not discriminate**: see below |
| 1c | Capacity shows the headroom charged | **Bug found and fixed** (pool breakdown left headroom out). Checked through the adapter's unit test, not on a running daemon |
| 2 | Open-network pod: internet yes; metadata, ClusterIP and other pods no; grain pod and DNS yes. `network: none` reaches nothing | **Pass**: 18/18 probes as expected, with a control pod showing every block comes from the policy |
| 3 | Package-cache CA survives a restart with an adopted sandbox; rotates when deleted | **Pass** on real Docker containers through the daemon's own functions. Not a full `grain daemon` restart |
| 4 | codex `config.toml`/`environments.toml` (no credentials, so a stub run) | **Pass**: written as documented. The live MCP-only probe was **skipped** (no codex credentials) |

### 1. Memory on a real Granule VM

`GRAIN_REAL_VM_TESTS=1 go test ./pkg/orchestrator/ -run '^TestGranuleVMMemoryHeadroomOnARealVM$' -v`
([`granule_memory_real_test.go`](../../pkg/orchestrator/granule_memory_real_test.go)).
It builds the vendored `granule` CLI and creates the VM through
`GranuleSandboxes.Acquire` with `Shape{CPUs: 2, MemoryMB: 2048, DiskGB: 12}`,
the same path the daemon takes. It reads the VM container's cgroup from
`/proc/<pid>/cgroup` and then, inside the guest:

1. writes and reads back a 1.5 GiB file (`free -m`: 1779 MiB buff/cache of 1967),
2. `dd if=/dev/urandom` a 4 GiB file with `conv=fsync`,
3. reads it back, deletes both files and syncs.

Results: `memory.max` = 3072 MiB. All three steps succeeded. The container was
still running with `OOMKilled=false`. `memory.events` showed `max 0, oom 0, oom_kill 0`,
and `memory.peak` = 2190110720 (2088 MiB). The test took 317 s.

**Caveat (not a pass for the headroom itself):** peak was 2088 MiB, below
the old guest + 512 MiB limit (2560 MiB). On this host and guest, the
host-side page cache charged to the VM's cgroup never got close to the
old limit, so this run shows that the 3 GiB limit is applied and is enough. It
does **not** show that the old limit would have OOM-killed the VMM. That would
need a run at the old limit. `GranuleConfig` cannot be told to apply one,
because the size flags win over `CreateArgs`, and this run did not build one by hand.

The test is opt-in and is not in the real-vm job's `-run` list (several GB
of IO and five minutes). That list is in `.github/workflows/tests.yml`, a
trust-boundary path, and is left alone here.

#### 1c. Capacity: bug found and fixed

`SandboxHealthAdapter.BudgetAndReconciliation` (`pkg/daemon/ui_adapters.go`),
which backs `GET /api/sandboxes` `budget` and the Sandbox health page, set
`committedMemoryMB` from `Ledger.Live()`, which charges each VM guest + headroom.
But it built `pools[].memoryMB` from the bare `h.Shape.MemoryMB`. With one 2 GiB
VM held, the page read "Committed: 3 GiB" over "Pools: Agent: 1 (2 vCPU / 2 GiB)".
The pools never added up to the total, and the headroom looked uncharged.

Deterministic reproduction: `TestSandboxBudgetPoolsIncludeTheVMMHeadroom`
(`pkg/daemon/ui_adapters_test.go`). Before the fix it fails with
`Pools = [{Pool:agent Count:1 CPUs:2 MemoryMB:2048}], want one pool charged the same 3072 MiB as CommittedMemoryMB`.
Fix: each holder is now charged with `ledger.VMMHeadroom.Charge(h.Shape)`, as
`Live` does. The admission arithmetic itself (`Ledger.Live`, `Budget`) was
already charging the headroom and is unchanged.

Not run: a `grain daemon` with a Granule run host holding a real sandbox, to
read `/api/sandboxes` live. Getting a sandbox held needs a dispatched run
(an agent credential) or a CI job against a repo, and neither was set up here.

### 2. Egress on kind + Calico

[`egress.sh`](2026-11-03-sandbox-resources/egress.sh), after
`scripts/prepare-real-k8s-ci.sh`. It builds a single-node kind cluster with Calico,
applies `deploy/kubernetes/grain.yaml` (Deployment scaled to 0, as
`stopShippedDaemon`) and `deploy/kubernetes/networkpolicy.yaml` as shipped,
then starts:

- `grain-proxy`: nginx labelled `app.kubernetes.io/name=grain` (grain's pod),
- `victim`: nginx with a ClusterIP Service,
- a fake metadata server: `169.254.169.254/32` added to the kind node's `lo`, and
  hostNetwork nginx answering on it,
- probe pods: `open` (labelled `app.kubernetes.io/managed-by=grain`,
  `app.kubernetes.io/component=sandbox`, as `K8sSandboxes` labels them), `none`
  (the same plus `grain.dev/network=none`) and `control` (no grain labels).

```
grain pod 10.244.6.197, victim pod 10.244.6.198, victim ClusterIP 10.96.152.72, kube-dns 10.96.0.10
open     internet https://example.com     reach=1 want=1 ok
open     metadata 169.254.169.254         reach=0 want=0 ok
open     ClusterIP Service 10.96.152.72   reach=0 want=0 ok
open     another pod 10.244.6.198         reach=0 want=0 ok
open     grain pod 10.244.6.197           reach=1 want=1 ok
open     cluster DNS                      reach=1 want=1 ok
none     internet https://example.com     reach=0 want=0 ok
none     metadata 169.254.169.254         reach=0 want=0 ok
none     ClusterIP Service 10.96.152.72   reach=0 want=0 ok
none     another pod 10.244.6.198         reach=0 want=0 ok
none     grain pod 10.244.6.197           reach=1 want=1 ok
none     cluster DNS                      reach=0 want=0 ok
control  internet https://example.com     reach=1 want=1 ok
control  metadata 169.254.169.254         reach=1 want=1 ok
control  ClusterIP Service 10.96.152.72   reach=1 want=1 ok
control  another pod 10.244.6.198         reach=1 want=1 ok
control  grain pod 10.244.6.197           reach=1 want=1 ok
control  cluster DNS                      reach=1 want=1 ok
```

The control row is what makes the `open` zeros meaningful: the same cluster
lets an unselected pod reach the metadata address, the Service and the pod.
The probe pods were plain `kubectl run` pods with the sandbox labels, not
pods built by `K8sSandboxes.Acquire`. The labels were taken from
`pkg/k8s` (`ManagedByLabel`, `SandboxLabel`) and `orchestrator.NetworkLabel`.
`hostinit` re-applying the file on upgrade was not exercised.

### 3. Package-cache CA across a restart with adoption

`GRAIN_REAL_DOCKER_TESTS=1 go test ./pkg/orchestrator/ -run '^TestPackageCacheCASurvivesARestartWithAnAdoptedContainer$' -v`
([`packagecache_ca_real_test.go`](../../pkg/orchestrator/packagecache_ca_real_test.go)).
It uses the functions the daemon calls: `pkgcache.LoadOrCreateCA` on
`<dir>/package-cache-ca.pem`, `ContainerSandboxes.Acquire` + `PrepareSandbox` for
a new sandbox, and a fresh `ContainerSandboxes.Readopt` + `RefreshAdoptedSandbox`
for the container a previous "process" left running. A TLS listener issuing
from the CA stands in for the cache's interception listener. The container
runs `curl` against it with normal verification (`buildpack-deps:bookworm-curl`).

- First start: the file is created with mode 0600, and a new sandbox verifies.
- Restart, file in place: same root certificate, and the readopted container still verifies.
- File deleted: the next load mints a different CA, again 0600. Before refresh,
  the adopted container fails verification (`tls: bad record MAC` on the
  server), which shows the check discriminates. After `RefreshAdoptedSandbox` it
  verifies, and a new sandbox trusts the new CA from the start.

PASS in 13 s. Not run: an actual `grain daemon` restart adopting a CI
sandbox. This is the library sequence the daemon drives, run on real
containers. The near-expiry rotation is covered by the unit test
`TestLoadOrCreateCARenewsNearExpiryCA`, and was not repeated.

### 4. codex isolation

No codex/OpenAI credentials exist in the sandbox, so the live MCP-only probe
across profiles was **skipped**.
[`codex-stub.sh`](2026-11-03-sandbox-resources/codex-stub.sh) instead runs one
stub run through `codex.New` (what the daemon constructs), with the freshly
built `grain` binary as its MCP server and a fake `codex` that reports
`codex-cli 0.166.0`, answers `debug models` with a catalog carrying
`apply_patch_tool_type`, and copies the `CODEX_HOME` it was given.

- `config.toml` (0600): `approval_policy = "never"`, `sandbox_mode = "read-only"`,
  `web_search = "disabled"`, `include_environment_context = false`; `[features]`
  with `shell_tool`, `unified_exec`, `view_image`, `apps`, `plugins`,
  `multi_agent`, `multi_agent_v2` all `false`; `code_mode_host = true`; skills
  off; one `[mcp_servers.grain-sandbox]` on loopback with a bearer header,
  `required = true`, `tool_timeout_sec = 3720`, and only the granted tools
  enabled and pre-approved; `model_catalog_json` pointing at the stripped catalog.
- `environments.toml` (0600): `default = "none"`, `include_local = false`.
- `model-catalog.json`: `apply_patch_tool_type` is `null`.
- The run ended with `final answer: "stub done"`. The private home was
  removed afterwards (the copy is the script's).

The pins (#510, #522) were not re-verified against real CLIs.

## Not run, and why

- A real `grain daemon` with a Granule run host reporting capacity live (item 1c), and a
  real daemon restart adopting a CI sandbox (item 3): both need a dispatched run
  or a CI job with a repository behind it. The pieces were run directly instead, as above.
- The live codex MCP-only probe (item 4): no credentials.
- A run at the old guest + 512 MiB limit, to show the headroom is what saves the VMM (item 1b).
- `guestforward` (#524) is not one of §8's "NOT proved" items and was not
  exercised: it is the kubernetes-granule forward into sandbox pods, and the VM
  here was a local Docker-backed Granule VM.
