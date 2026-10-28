# The patches grain carries on the vendored granule

`third_party/granule` is a source snapshot of granule, the successor to
the `third_party/kontur` this repository vendored until 2026-09-21, and
`go.mod` points the module at it with a `replace`. Everything that builds
the `granule` CLI here -- the `Dockerfile`, `scripts/release-images.sh`,
`scripts/granule-guest-resolve.sh`, `scripts/granule-guest-probe.sh` --
builds it out of that directory, so a patch applied here is a patch this
deployment runs.

This file is what `third_party/kontur/VENDORED.md`'s "Local patches"
section used to be, moved outside the vendored tree because *inside* it
is where it did not survive: that section was explicit that it was "the
only thing standing between them and a wholesale `git archive` deleting
them silently", and then the whole directory was replaced and it went
with the directory. The rule it stated is still the right one, and is
the rule here:

> A patch here is allowed to exist for the length of a round trip, and is
> not allowed to be undocumented.

A change granule needs belongs on granule's own `main` and reaches this
copy by a resync. A patch listed below is one this deployment is running
ahead of that round trip, is to be **dropped by re-vendoring** rather
than re-applied by hand, and is to be deleted from this file the moment
the resync that carries it lands.

## Synced at

`gke-labs/granule` `main` at `a4b4e4b` (`a4b4e4b801b5a3efb0b070d8004ac7f075af7d22`,
"Merge pull request #227 from gke-labs/grain/049f1e09/task-1478", 2026-10-28).

The tree was replaced wholesale from a `git archive` of `a4b4e4b`, with the
local patches listed under "Patches in flight" below re-applied on top.

This sync carries PRs #213–#227 on top of `4411ce4`, including:
- `pkg/sandbox` (`cmd/granule-shim/access.go`, `pkg/netshim/splice.go`,
  `docs/sandbox-access-api.md`, `deploy/k8s/`): the sandbox access API
  (gke-labs/granule#226 / task 1456, `e50c6c6`) providing multiplexed
  reverse dial-out (`Server.DialOut` / `Acceptor`) and direct listen-mode
  (`Server.Serve` / `sandbox.Dial`) transport over TLS/mTLS + bearer token,
  with `Exec`/`StartExec`, `Reattach` (16 MiB per-exec ring buffer and 10 min
  completed-session retention across wrapper reconnects), `Signal`/`Kill`,
  batched `PutFiles`/`PutFile`, `GetFile`, `OpenForwardStream`, `Health`,
  keep-alive heartbeats, TC `u32` `ControllerIPs` drop rules on `tap` ingress,
  and an in-process fake wrapper hook (`ServerConfig.DialGuest`).
- `pkg/relay` (`cmd/granule-relay`, `pkg/sandbox/relay.go`): Kubernetes API
  subresource / dial-out relay (`sandbox.DialRelay`, gke-labs/granule#227 /
  task 1478, `a4b4e4b`).
- `pkg/guestforward` (`cmd/granule-shim`, `pkg/agent/forward.go`,
  `pkg/cli/forward.go`): upstream multiplexed TCP/Unix port forwarding
  (gke-labs/granule#225, `0068252`).
- `pkg/extent` (`cmd/granule-extentd`, `pkg/storage`, `pkg/blockdev`):
  content-addressed extent storage and FUSE block device backend.

`Dockerfile`, `go.mod`, `go.sum`, `vendor/` and `pkg/guestimage`'s contract
are untouched, so `scripts/granule/build-granule-image.sh` and
`scripts/granule/guest-inputs.sh` need no change.

## Patches in flight

- **Legacy `pkg/forward` compatibility for `pkg/granule/forward.go` (`granule forward -kube` / `granule-shim forward`).**
  `pkg/forward`, `pkg/execwire`, `pkg/agent/forward.go`, `pkg/guestexec/forward.go`,
  `cmd/granule-shim/main.go`, `pkg/cli/forward.go`, and `pkg/cli/vm.go`
  retain `pkg/forward` alongside upstream's `pkg/guestforward` (`gke-labs/granule#13`,
  grain task 480 / task 721) until `pkg/granule/forward.go` and live
  `kubernetes-granule` sandbox pods migrate to `pkg/guestforward` / `pkg/sandbox`.
- **Per-VM cgroup memory limits and positive `oom_score_adj` for Docker-backed VM containers.**
  `pkg/vmspec/spec.go`, `pkg/cli/vm.go` and `pkg/dockervm/docker.go` add
  `-memory-limit-mb` (defaulting to guest memory ceiling plus
  `VMMMemoryOverheadMB = 512` MiB for cloud-hypervisor overhead) and
  `-oom-score-adj` (defaulting to `DefaultOOMScoreAdj = 500`), and pass
  `--oom-score-adj`, `--memory <limit>m` and `--memory-swap <limit>m` on
  `docker run` when starting `granule-vm-<name>` (grain task 500). This
  bounds each VM container's `memory.max` and `memory.swap.max` (to 0)
  and ensures the kernel OOM killer picks a sandbox VM before the host
  daemon or `hostd` (`-900`).
- **Sanitized GCP project placeholder in IPv6 e2e docs and scripts.**
  `.grain/workflows/e2e-ipv6-gke.yml`, `docs/ipv6-dual-stack-design.md`,
  and `test/e2e-ipv6.sh` replace a contributor-specific GCP project name
  with `grain-sandbox-dev` so `tests/hygiene` (`TestCoreSaysNoGoogleTokens`)
  stays clean (`91c0dfffd`).
- **`pkg/sandbox.Server`: shell-quote `handlePutFiles` `trap`/directory-error commands and enforce per-exec replay buffer & retention bounds.**
  `pkg/sandbox/server.go` and `pkg/sandbox/sandbox_test.go` fix single-quote
  escaping in `handlePutFiles`'s `trap ... EXIT` cleanup command and directory
  check error message (which otherwise left `tmpQuoted` paths and `sf.dstPath`
  unquoted inside single quotes, breaking paths with spaces/quotes or allowing
  shell injection), reject `FileUpload.Owner` values starting with `-`, and
  enforce the documented 16 MiB per-exec replay ring buffer
  (`MaxExecBufferBytes`) and 10-minute completed-session eviction
  (`CompletedExecRetention`) on `Server.execs`.

## Landed upstream

These six were listed here as in flight, and all are on granule's own
`main` now. The sync above carries them, so grain no longer patches
anything to get them.

- **Sandbox guests cannot ARP-spoof the shared docker bridge.**
  `pkg/netshim/splice.go` and `setup.go` guard netshim's tap -> ext
  direction with tc u32 filters that redirect only IPv4 frames carrying
  the guest's own MAC and source address, and ARP frames whose Ethernet
  source, sender hardware and sender protocol addresses are the guest's
  own (or 0.0.0.0, an RFC 5227 probe); everything else is dropped
  (gke-labs/granule#168, grain's #2395). Without it a compromised guest
  could answer ARP for the docker gateway -- where the git and model
  proxies listen -- or for a neighbouring sandbox. Carried here from
  `9a153282e`, and verified upstream by `pkg/netshim/guard_test.go`
  writing forged frames to a real tap on a real kernel.
- **Docker-backed VM containers run unprivileged.** `pkg/dockervm` grants
  `--device /dev/kvm`, `--device /dev/net/tun`, `--cap-drop ALL`,
  `--cap-add NET_ADMIN` and `--security-opt no-new-privileges` instead of
  `--privileged` (gke-labs/granule#169, grain's #2396). Without it the
  container has every capability and host device and no seccomp or
  AppArmor confinement, around a VMM that opens paths taken from guest
  images a repository may have chosen. Carried here from `07d076fe2`,
  and verified upstream by booting a VM with it: `Privileged=false`,
  `CapDrop=[ALL]`, guest up and running.

  These two were the pair this file warned about most: both quiet enough
  that dropping either failed no build and no test in grain while leaving
  every sandbox running with the isolation gone, and both already lost
  once to an unmentioned re-vendor. They are upstream now, so the way to
  lose them is for upstream to lose them, which is a different and much
  louder problem.

- **`curl --resolve` is not pointed at a link-local nameserver.** The
  `fetch-chv` and `fetch-kernel` stages pick the nameserver they force
  `github.com:443` to resolve to from `/etc/resolv.conf`, skipping the
  addresses that cannot serve it; `169\.254\.` is now among them
  (gke-labs/granule#164). Every GCE instance resolves through
  `169.254.169.254`, the metadata server, which completes the TLS
  handshake and then demands a client certificate -- so no guest image
  built at all on a GCE host, `curl: (56) ... tlsv13 alert certificate
  required`, while the same request without `--resolve` returned 200.
  Found bringing a deployment up on GCE, where nested virtualization
  makes a granule host worth having in the first place.

- **A guest is told the console its architecture actually has.**
  `pkg/config/config.go`'s `GuestConsole` answers `ttyAMA0` on arm64 and
  `ttyS0` otherwise, off `runtime.GOARCH`, and `pkg/vmspec/spec.go`'s
  derived cmdline takes its `console=` from it. aarch64 has no ISA ports --
  cloud-hypervisor gives the guest an ARM PL011 -- so a guest told
  `ttyS0` there prints nothing and never becomes reachable, reported only
  as "vsock port 1024 is not being listened on inside the guest yet".
  Written on grain's `main` (`b351292fc`), proposed as
  gke-labs/granule#163, and merged there as `5950ab3`, which is the sync
  above. `tests/deploy`'s
  `TestAGranuleGuestIsToldTheConsoleItsArchitectureHas` still checks both
  halves in the vendored tree: it was written to catch a resync dropping a
  local patch, and now catches an upstream regression reaching grain,
  which is worth keeping for the same reason the `noresume` check is.
- **A granule VM boots with `noresume`.** `pkg/vmspec/spec.go`'s derived
  cmdline and `pkg/config/config.go`'s `defaultCmdline` both carry
  `noresume`. A Debian initramfs otherwise waits ~105 seconds for a
  `RESUME=` device that a freshly booted guest never has (grain/task-593).
  `tests/deploy`'s `TestTheSandboxGuestNeverWaitsForASuspendResumeDevice`
  still checks both spellings in the vendored tree, so a resync that
  loses them fails grain's own CI.
- **`guest build` takes a resolver.** `pkg/cli/guest.go` has a
  `-setup`-route-only `-dns` flag, and `Options.DNS` (a `*string`) is
  plumbed through `pkg/guestbuild` and `pkg/guestboot`. Since `8ff4bfb`
  (gke-labs/granule#119) the flag is spelled and defaulted like
  `vm create -dns` -- its default is `vmspec.Defaults().DNS` and an
  empty value leaves the image's own resolver alone -- which is the
  same behaviour as before, now stated on the flag itself. grain's own
  pipeline does not reach it, because grain builds with
  `granule granulefile build` and that boots nothing.
  `TestTheGuestBuildNeedsNoResolverBecauseItBootsNothing` records why.

## Still unchecked on arm64

Nothing else in granule's own code mentions an architecture -- the only
`arm64` in the tree is inside vendored `golang.org/x/sys` -- so the console
was the first of that class of bug to be found rather than necessarily the
last. One specific thing remains unchecked: a guest's graceful shutdown
goes through a PL061 GPIO on aarch64 rather than ACPI. grain's own guest
images now enable `acpid` and load `evdev` so that the press is acted on
(`TestTheSandboxGuestCanActOnThePowerButton`), which fixed a 20-second
forced teardown on every sandbox; whether granule's own bundled guest does
the same has not been looked at.

## Grain edits that this sync dropped

Three edits had been made to `third_party/granule` in this repository
without being listed here. The sync to `36a6dd8` replaced each of them
with upstream's version:

- `Dockerfile`: `c32323ecd` gave the `fetch-chv` and `fetch-kernel`
  stages an apt config forcing IPv4 with retries and timeouts. Upstream
  has since changed those stages its own way: curl `--resolve` through
  the build's nameserver, and plain-http apk repositories for the Alpine
  rootfs. Upstream's version is what ships now -- as amended by
  gke-labs/granule#164, which these same two stages then needed before
  they would fetch anything on GCE (see "Landed upstream"). **Twice now
  these two stages have cost a patch to make a release binary
  downloadable**, so a resync that touches them is worth re-testing on a
  host whose resolver is not an ordinary one, rather than assumed.
- `.grain/prompts/{01,08,09}*.md` and `pkg/cli/prompts_test.go`:
  `245690dd8` gave granule's own nightly prompts the "only report what
  the user needs to address" policy. Those prompts belong to granule's
  nightly suite, not grain's, so upstream's wording is what ships. A
  follow-up against `gke-labs/granule` asks whether it wants the policy.
- `.grain/workflows/cache-probe.yml`: upstream deleted it in `f4abeaf`,
  and the vendored copy had kept it.

The `noresume` and resolver patches were first written on the vendored
kontur (grain tasks 371 and 401). Re-vendoring then dropped them once
already, unmentioned, in #1725. That is why this file lives outside the
vendored tree.
