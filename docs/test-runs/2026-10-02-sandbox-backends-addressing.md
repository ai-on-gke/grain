# Sandbox backends: host removal, granule addressing and NetworkNone isolation (2026-10-02 §5)

This is a walkthrough of §5 of `docs/test-plans/2026-10-02.md`. The plan
file is on `candidate` and lands on `main` with task 1528. It ran against
`main` at `5a63bb2d1`, which contains every commit the plan names: `git
branch -r --contains 241229f0e` lists `origin/main`. Task 1577.

## Result

The walkthrough found three bugs, all fixed on this branch, and two
problems that are filed as a follow-up rather than fixed.

- **Fixed (isolation, fail-closed):** granule refused every NetworkNone
  sandbox. To find the VM container's bridge port, grain `docker exec`ed a
  sysfs script. In a granule VM container, `sh` is granule-shim, so the
  script ran in the *guest* and listed virtio interfaces, never the
  container's veth. Every acquire failed with "only 0 host bridge port(s)
  were found". The failure was fail-closed, so nothing was exposed, but
  NetworkNone was unusable on granule. The repo's own real-VM test
  (`TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM`) failed the
  same way on `main`.
- **Fixed:** after `docker network rm` of grain's sandbox network, the
  next acquire failed, and only the acquire after it recreated the network.
- **Fixed (agent-facing):** `run_command` reported a command's *own*
  exit 124 or 137 as "Killed after 300s by run_command's default bound
  ... Pass a larger `timeout`", even after four seconds.
- **Follow-up filed (no exposure):** two problems with standing sandboxes.
  The standing-sandbox supervisor is never run by the daemon:
  `reconcileStandingSandboxes` has no caller. Separately, granule
  `Serve` fails with "Access denied" from `systemd-run` under grain's
  guest image and its `debian` SSH user. Because of the first problem,
  step 6 (kill a standing sandbox and time its restart while polling
  `/api/sandboxes`) cannot run against a daemon at all.
- Passes: the four daemon-start cases; addresses (allocation, `docker
  inspect` and the guest all agree); the nftables chain and its bridge
  prerouting jump; spoofed-source probes, which are dropped once the
  first fix is in; every granule call going through `-granule-command`;
  and the a74486317 probe matrix.

## Environment

| | |
|---|---|
| Host | Grain sandbox VM, Debian 12, kernel `6.1.0-53-amd64`, 4 vCPU, 7.8 GiB. The VM itself runs under granule (nested KVM). |
| KVM | **Yes.** `/dev/kvm` is present (`crw-rw---- root:kvm`) and the run user is in `kvm`. **Backend: granule VMs** (real cloud-hypervisor). |
| Docker | 29.8.2. docker0 is `10.240.0.1/24`, default address pools are `10.241.0.0/16`, and there is no IPv6 on the default bridge. |
| nftables | `nftables v1.0.6 (Lester Gooch #5)`, installed with apt for this run; the stock image has no `nft`. Run through `sudo -n nft`. |
| Guest image | `ghcr.io/bwsalmon/grain/guest:main`, pulled 2026-10-03. Building it locally (`buildGranuleGuestImage`) failed in this sandbox: curl exit 60 on `download.docker.com` during the docker-ce layer. That is an environment TLS/proxy problem, not a code one, so the published image was tagged `grain-guest:e2e-test` for the real-VM tests. |
| grain | `go build ./cmd/grain` at `main` `5a63bb2d1`; granule built from `third_party/granule` (`./cmd/granule`) |
| macOS | **Not runnable here.** Only `GOOS=darwin go vet ./pkg/console/ ./pkg/sysstat/` ran; it is clean. `GOOS=darwin go vet ./pkg/orchestrator/` is also clean with this branch's new Linux-only file. |

## Stubs

- **GitHub:** no daemon here dispatched a task, so nothing called GitHub
  and githubsim was not needed. A full agent dispatch needs GitHub and a
  scriptable agent. As in the 2026-10-01 §6 run, the sandbox side of a
  dispatch was instead driven from grain's own code (see Method).
- **Model sessions:** none ran.
- Nothing else is stubbed. The tests use real granule, docker,
  cloud-hypervisor VMs, nftables on the host, and grain's own
  `GranuleSandboxes`, `sandboxaddr.Pool`, package cache and supervisor.

## Method

- **Steps 1 and 4 of the setup:** a real `grain daemon` and the real
  `scripts/setup.sh`.
- **Steps 2–5:** the opt-in harness
  `pkg/orchestrator/granule_backends_walkthrough_real_test.go`, committed
  on this branch as `TestGranuleBackendsWalkthroughOnRealVMs`. It builds
  `GranuleSandboxes` the way `runDaemon` does under
  `-granule-sandbox-cidr` and `-granule-command`: a real `sandboxaddr`
  pool (`10.189.0.0/16`) on a docker network grain creates, with granule
  reached through a wrapper script that logs its argv and execs the
  locally built granule. It acquires a NetworkNone and a NetworkOpen VM
  and logs what each step saw.
- **Existing coverage, run as well:** the real-VM tests
  `TestGranuleSandboxAddressesAgainstRealVMs` and
  `TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM`
  (`GRAIN_REAL_VM_TESTS=1`), and the a74486317 harness
  `TestSandboxIsolationProbesOnRealGranuleVMs` (as root).
- **Step 6:** `TestStandingSandboxRestartOnRealVMWalkthrough` in the same
  file. It runs the supervisor's `Reconcile` back to back against a
  served granule VM, kills the VM container, and polls `GetActive` every
  20 ms. It is committed failing at the step it cannot pass yet; see
  finding 4.

```
GRAIN_BACKENDS_WALKTHROUGH=1 GRAIN_BACKENDS_WALKTHROUGH_IMAGE=ghcr.io/bwsalmon/grain/guest:main \
  go test ./pkg/orchestrator -run 'TestGranuleBackendsWalkthroughOnRealVMs|TestStandingSandboxRestartOnRealVMWalkthrough' -v -timeout 30m
GRAIN_REAL_VM_TESTS=1 go test ./pkg/orchestrator \
  -run 'TestGranuleSandboxAddressesAgainstRealVMs|TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM' -v
go test -c -o /tmp/grain.test ./cmd/grain
sudo env PATH=/tmp/gbin:$PATH HOME=/root GRAIN_ISOLATION_PROBE=1 \
  GRAIN_ISOLATION_PROBE_IMAGE=ghcr.io/bwsalmon/grain/guest:main GRAIN_ISOLATION_PROBE_GATEWAY=10.240.0.1 \
  /tmp/grain.test -test.run TestSandboxIsolationProbesOnRealGranuleVMs -test.v -test.timeout 20m
```

## 1. Daemon start cases (real `grain daemon`, scratch `-data-dir`s)

| Case | Command | Exit | What it said |
|---|---|---|---|
| No backend flag | `grain daemon -data-dir /tmp/d1` | 2 | `grain daemon: no sandbox backend selected: pass -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, or -disable-native-sandboxes (host sandboxes, which this used to fall back to, have been removed)` |
| `-host-sandboxes` | `grain daemon -data-dir /tmp/d2 -host-sandboxes` | 2 | `WARNING: starting anyway, ignoring 2 flag(s) this build does not define: ... -host-sandboxes`, plus the unit/image mismatch hint, then the same "no sandbox backend selected" refusal |
| Stored `host` + `-container-sandboxes` | First start with `-container-sandboxes -container-git-proxy-host 10.240.0.1` (stored `container`), then `UPDATE grain_config SET sandbox_backend='host'` and restart | runs (killed by `timeout 20`, exit 124) | `loadConfig: stored sandbox backend is "host", which this daemon cannot build -- running with "container" instead; choose one of the available backends in the Settings pane`. The stored value stays `host` until somebody chooses another. |
| `setup.sh` | `sudo GRAIN_HOST_SANDBOXES=1 bash scripts/setup.sh` | 2 | `setup.sh: GRAIN_HOST_SANDBOXES=1 is no longer supported.` and a paragraph naming the alternatives. No `grain` user and no `/usr/local/bin/grain` were created. (Run without sudo, it stops first at the root check, exit 1.) |

As expected. Two observations, neither a bug:

- An unknown flag such as `-host-sandboxes` is ignored with a warning,
  not refused. That is the documented upgrade/rollback behaviour, and
  the start is still refused because no backend is selected.
- On this 7.8 GiB host, the daemon's default 8 GiB reservation leaves a
  sandbox budget of "0.0 GiB", so it would admit no sandbox at all. It
  says so at startup.

## 2. Addresses, nftables and grain's docker network (granule, KVM)

With the fixes on this branch:

| Sandbox | Allocated (pool) | `docker inspect` | Guest `ip -4 addr` |
|---|---|---|---|
| `w1-1` NetworkNone | 10.189.0.32 | `grain-walk-…=10.189.0.32 gw=10.189.0.1` | `inet 10.189.0.32/16` |
| `w2-1` NetworkOpen | 10.189.0.64 | `grain-walk-…=10.189.0.64 gw=10.189.0.1` | `inet 10.189.0.64/16` |
| `w3-1` after `docker network rm` | 10.189.0.96 | `grain-walk-…=10.189.0.96` | n/a |

Docker network: `driver=bridge subnet=10.189.0.0/16`, created by grain
("created docker network … with subnet 10.189.0.0/16").
`TestGranuleSandboxAddressesAgainstRealVMs` passed as well (98.7 s):
parent, child and unrelated VMs held their allocated addresses, and the
family block was released with its parent.

`sudo nft list ruleset`, grain's tables only, with NetworkNone `w1-1` live:

```
table inet grain_network_none {
	chain forward { type filter hook forward priority filter - 10; policy accept;
		ip saddr 10.189.0.32 jump vm_g_w1_1 }
	chain input { type filter hook input priority filter - 10; policy accept;
		ip saddr 10.189.0.32 jump vm_g_w1_1 }
	chain vm_g_w1_1 { ip daddr 10.189.0.1 tcp dport 35893 accept
		counter packets 0 bytes 0 drop }
}
table bridge grain_network_none {
	chain forward { type filter hook forward priority -10; policy accept;
		ip saddr 10.189.0.32 jump vm_g_w1_1 }
	chain prerouting { type filter hook prerouting priority -10; policy accept;
		iifname "vetha14caa9" ether type != arp jump vm_g_w1_1 }
	chain vm_g_w1_1 { ip daddr 10.189.0.1 tcp dport 35893 accept
		counter packets 0 bytes 0 drop }
}
```

`vetha14caa9` is the VM container's own host veth. Before the fix there
was no ruleset, because the acquire was refused (finding 1).

`checkAllocatedAddress` failing an acquire on a wrong address was not
reproduced on real VMs. Docker either assigns the requested `--ip` or
refuses the `docker run`, so this run found no way to make it assign
another one. The unit tests remain its only coverage.

## 3. NetworkNone probes, including a spoofed source (from inside the guest)

From the NetworkNone guest `w1-1` (10.189.0.32). The service port on the
gateway is a wildcard-bound listener declared as a sandbox service port.
The "other host port" is a second wildcard listener that is not one.

| Probe | Result |
|---|---|
| Real source → sandbox service port on the gateway | reached (`grain-sandbox-service`) |
| Real source → other host port on the gateway | dropped (curl 28) |
| Real source → NetworkOpen neighbour 10.189.0.64:22 | dropped |
| `sudo ip addr add 10.189.200.200/16 dev eth0` and `ip route replace default via 10.189.0.1` | succeeded: the guest has root |
| Spoofed 10.189.200.200 → internet 1.1.1.1 | dropped (curl 28) |
| Spoofed → other host port on the gateway | dropped (curl 28) |
| Spoofed → the host's primary (non-gateway) address | dropped (curl 28) |
| Spoofed → NetworkOpen neighbour :22 (`curl --interface … telnet://`) | dropped (curl 28) |
| NetworkOpen guest → internet 1.1.1.1 (control) | reached (HTTP 301) |

After the fix, `TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM`
also passes on the default bridge. That covers the guest re-adding its
default route, internet dropped, the service port allowed and other host
ports dropped, the bridge prerouting jump present, and a spoofed `.250`
source dropped.

**a74486317 harness (`TestSandboxIsolationProbesOnRealGranuleVMs`, as
root): PASS, 53.6 s.** The NetworkNone guest (bridge port `vethe14b25e`
firewalled; gateway accepts tcp 53, 80, 443, 9728 and 9729, plus udp)
gave:

- **Network:** no default route. Metadata, and internet with the route
  restored, both time out. `example.com` and `api.github.com` do not
  resolve, and 1.1.1.1 is refused.
- **Package cache:** `deb.debian.org` resolves to the gateway, and
  package fetches through interception and the cache URL return 200. A
  GitHub API GET through the cache URL returns 404 and a POST returns 403.
- **ARP:** the gratuitous ARP spoof of the gateway did not displace the
  neighbour's entry, which stays the gateway's own `16:0a:96:92:3c:42`.

All of that matches the 2026-10-01 §6 matrix.

## 4. `docker network rm` between dispatches

Before the fix, after releasing `w1-1`/`w2-1` and running `docker
network rm grain-walk-…`:

```
first Acquire after docker network rm failed after 598ms: orchestrator: creating granule VM "g-w3-1":
  granule: vm create g-w3-1: exit status 1: granule: starting VM container granule-vm-g-w3-1: docker run ... (network not found)
second Acquire after docker network rm: err=<nil> after 4.28s
```

After the fix, the first acquire succeeds in 4.6 s. The wrapper log
shows the refused `vm create` followed by its retry, and the network is
recreated ("created docker network …").

## 5. `-granule-command` through a logging wrapper

`GranuleConfig.Command` was the one-word wrapper, the same thing
`-granule-command /path/to/wrapper` produces. Its argv log, abridged:

```
vm list -state-dir …                 # Exists, before the create
vm create g-w1-1 -state-dir … -backend docker -kontur-image … -docker-run-opt --network … --ip 10.189.0.32
vm list -state-dir …
vm create g-w2-1 …
vm list -state-dir …                 # Exists, on Release
vm delete g-w1-1 -state-dir …
vm list -state-dir …
vm delete g-w2-1 …
vm list …  vm create g-w3-1 …  vm list …  vm delete g-w3-1 …  vm list …   # incl. ReapOrphans' List
```

Create, exists (as `vm list`), list and delete all went through the
wrapper, and nothing read the state directory directly.

Observation: the guest transport (`granule exec`/SSH) and grain's own
`docker` calls are not routed through the command. That covers the
firewall, `docker inspect`, `network create` and the bridge-port lookup.
With a granule on another machine, NetworkNone and assigned addresses
would act on *this* host's docker. This is consistent with 3deec5d47's
stated scope (macOS → a local Linux VM), but worth knowing before trying
`ssh otherhost granule`.

## 6. Standing sandbox killed under the supervisor

Not runnable against a daemon, for two reasons:

- `reconcileStandingSandboxes` (pkg/orchestrator/standing_sandboxes.go)
  has no caller in pkg/ or cmd/.
- Nothing writes `standing_sandbox` rows outside tests, and
  `/api/sandboxes` does not report standing sandboxes.

Driving the supervisor directly
(`TestStandingSandboxRestartOnRealVMWalkthrough`) on a real VM fails at
the first launch:

```
orchestrator: error starting standing sandbox "walk": orchestrator: starting "standing-walk" in sandbox "walk":
  exit 1: Failed to start transient service unit: Access denied
```

`servingSandbox.start` runs `systemd-run` as the SSH user. With grain's
guest image that is `debian`, and an unprivileged user cannot create a
system unit. `pkg/lifecycle` serves through the same path. The restart
time and the `GetActive` latency during the launch could therefore not
be measured. Filed as a follow-up (auto_merge false); it needs a
decision on which user a served command runs as.

**Addendum, 2026-10-03 (task 1617).** The first bullet above was wrong.
`reconcileStandingSandboxes` is not called by name. It is registered as
the `standing-sandboxes` entry in `Reconcilers()` (pkg/orchestrator/cycle.go),
so the daemon runs it every tick. With `Deps.StandingSandboxes` nil, it
uses the package's default supervisor and serves through `deps.Sandboxes`.
`standing_sandbox` rows are settings tables bound to grain/config
(pkg/staterepo/bind.go), so they are declared there and not through an
API. The second bullet still holds: no API or UI reads `GetActive`.

The `Access denied` is fixed. The unit is now started through `sudo -n`
with `--uid` set to the exec's own account, and `Output` reads the
journal through sudo too. On the same kind of KVM host, with
`ghcr.io/bwsalmon/grain/guest:main`, the walkthrough now passes:

```
first launch took 43.879s
docker kill granule-vm-g-walk: err=<nil>
reconcile pass 1 took 21ms: failed=false restarts=0     (probe miss 1/2)
reconcile pass 2 took 6.055s: failed=false restarts=1   (probe miss 2/2, relaunched)
restarted 6.076s after the kill, over 2 reconcile passes
GetActive polled 2493 times over 50.356s; slowest 17.02µs at +49.068s
--- PASS: TestStandingSandboxRestartOnRealVMWalkthrough (53.38s)
```

## Findings and fixes

1. **NetworkNone on granule refused on every acquire.** Fixed in
   `fix(granule): find a NetworkNone VM's bridge port from the host, not
   from inside its guest`. `inspectGuestPorts` now asks the kernel first:
   - `docker inspect` gives the VM container's id and pid.
   - `/proc/<pid>/cgroup` must name that container, checked both before
     and after. A daemon in its own pid namespace, or a reused pid, is
     refused rather than firewalling another namespace's ports.
   - `RTM_GETNSID` by pid gives the namespace id.
   - The ports are the bridge-enslaved host links whose
     `IFLA_LINK_NETNSID` is that id.

   This needs no privilege and reads nothing the guest controls. The
   `docker exec` script stays as the fallback, and both failures are
   named in the error.

   One gap: a containerised daemon (setup.sh container mode runs without
   `--pid host`) cannot see the VM container's pid. It falls back to the
   script and so still fails closed, which is the pre-fix behaviour for
   that mode. It needs `--pid host`, or another route, before NetworkNone
   works there.

   Tests:
   - `TestPidInContainerReadsDockersCgroupPaths` (both cgroup drivers,
     v1, a foreign cgroup, an id prefix).
   - `TestContainerNetnsPortsFindsTheContainersOwnVeth`: a real busybox
     container, compared against the in-container view. It skips without
     docker or a local image.
   - The real-VM test, whose stale `routeRes.Text != ""` assertion
     (run_command results now carry `exit=`/`stdout:`/`stderr:` sections)
     is fixed with `guestStdout`.
2. **First acquire after `docker network rm` failed.** Fixed in
   `fix(granule): recreate a removed sandbox network on the acquire that
   finds it gone`. A create that fails while grain's network is gone
   recreates the network, deletes any state left by the refused create,
   and creates the VM again, once. Regression test:
   `TestAGranuleAcquireRecreatesARemovedSandboxNetwork`, which fails
   before the fix and passes after.
3. **`run_command` misattributed a command's own 124/137** to its bound.
   Fixed in `fix(mcp): only report run_command's bound when it has
   actually elapsed`. Regression test:
   `TestSSHRunCommandLeavesACommandsOwn124And137Alone`, which fails
   before the fix and passes after.
4. **Standing sandboxes are unwired, and granule serving fails as a
   non-root user.** Follow-up filed; see §6.

`make security` passes on this branch (`go vet ./...` and
`tests/security` with `-race`), and `go test ./pkg/orchestrator/` and
`go test ./pkg/mcp -run 'TestSSHRunCommand|TestRunCommand'` pass. No
change touches `third_party/granule`, so `docs/granule-local-patches.md`
is unchanged.

## Not done

- **macOS consoles and host stats** (`2e1756451` and the related
  commits) could not be run; only the darwin vet above ran.
- **`-container-sandboxes` dispatch probes** were not re-run: KVM was
  available, so the plan's granule path was taken. The container backend
  was exercised only through the daemon-start cases.
- **A real agent dispatch through the daemon** (task → sandbox) was not
  run. It needs GitHub and a scriptable agent, as in the 2026-10-01 §6
  run, so the backend was driven directly.
