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

`gke-labs/granule` `main` at `1463ed6` (`1463ed6097335b986e3bbdb37f25c16c28fa1e74`,
"Merge pull request #244 from gke-labs/grain/098073e4/task-3349-scrub-the-contributor-gcp",
2026-11-06).

The tree was replaced wholesale from a `git archive` of `1463ed6` with no
local patches on top -- `third_party/granule` is byte-identical to upstream
(grain task 3350). The previous sync was `b2fda8b` (`b2fda8bb`, grain task
3024).

This sync carries PR #244 (`9de041c`) on top of `b2fda8b`, which drops the
contributor GCP project default from `.github/workflows/e2e-ipv6-gke.yml`,
`docs/github-actions-ci.md`, `docs/ipv6-dual-stack-design.md` and
`test/e2e-ipv6.sh` (resolving `$GKE_PROJECT` from `GKE_PROJECT` or gcloud's
configured project instead), retiring the last local patch grain carried so
`tests/hygiene` (`TestCoreSaysNoGoogleTokens`) passes on an unmodified
archive. Nothing touches Go code, `Dockerfile`, `go.mod`, `go.sum` or
`vendor/`.

### What the `b2fda8b` sync carried

The `b2fda8b` sync (grain task 3024) carried PRs #242 and #243 on top of
`a0aeae5` (neither touching Go code, `go.mod`, `go.sum` or `vendor/`):
- granule's `.grain/workflows` are retired in favour of the GitHub Actions
  ports in `.github/workflows`, with `.grain/schedules` that review failed
  scheduled Actions runs (gke-labs/granule#242). Neither directory is read
  in this repository: grain and GitHub only read the root `.grain/` and
  `.github/`. The `Dockerfile` changes only in two comments that named
  `.grain/workflows`.
- `test/e2e-ipv6.sh` applies the KVM device plugin with the preloaded
  image already substituted, and dumps the plugin's pods, logs and events
  when its rollout times out (gke-labs/granule#243).

It also dropped the legacy `pkg/forward` compatibility this file listed as
in flight: `pkg/forward`, `pkg/execwire`'s `ForwardSpecs`,
`pkg/agent/forward.go`'s `serveLegacyForward`, `pkg/guestexec/forward.go`,
`cmd/granule-shim`'s `forward` mode, and the `-kube` path in
`pkg/cli/forward.go` / `pkg/cli/vm.go` (`gke-labs/granule#13`, grain task
480 / task 721). Upstream never carried them. Grain stopped using them when
`pkg/granule/forward.go` moved to a `pkg/guestforward.Forwarder` over
`kubectl exec -i <pod> -c sandbox -- granule forward-stdio` (grain task
2601); the guest only names which listener took a connection, never where
to dial, so the controller still reaches nothing beyond the configured
targets (`TestForwardKubectlRefusesOpenOutsideRemotePorts`). Every
`kubernetes-granule` pod started since the `a4b4e4b` resync (`b0b39e23`,
#1456) runs a shim and guest agent that speak `pkg/guestforward`.
`pkg/trustboundary`'s tests and `tests/deploy`'s
`TestGitHubCIChangesMirrorsGrainTestsWorkflowPaths` and
`TestTheGranuleJobRunsTheVendoredModulesOwnTests` now name
`third_party/granule/pkg/guestforward` where they named `pkg/forward`.

### What the `a0aeae5` sync carried

The `a0aeae5` sync (grain task 2167, module path switched by grain task
2973) carried PRs #228–#241 on top of `a4b4e4b`, including:
- `pkg/vmspec`, `pkg/hypervisor`, `pkg/config`, `pkg/cgroupmem`: a VM
  that fills its RAM with page cache is no longer OOM-killed from the host
  (gke-labs/granule#238 / task 2577, `c35ae79`).
  `vmspec.VMSpec.ContainerMemoryLimitMB()` defaults the container limit to
  guest + `ContainerMemoryHeadroomMB` = max(512 MiB, guest/16); a
  virtio-balloon with free page reporting (`CHV_BALLOON`, on by default)
  lets the guest hand freed pages back; and writable disks are opened
  `direct=on` (`CHV_DISK_DIRECT`, `auto` by default) so the VMM's disk I/O
  stops filling host page cache charged to the VM's cgroup. Grain still
  passes its own, larger `-memory-limit-mb` (`orchestrator.GranuleMemoryLimitMB`);
  `TestGranuleMemoryLimitIsNeverBelowGranulesOwn` checks it stays at or
  above granule's.
- `pkg/dockervm`, `pkg/cli/vm.go`: per-VM cgroup memory limits and positive
  `oom_score_adj` for Docker-backed VM containers (gke-labs/granule#234,
  `31d88cf`), which this file listed as in flight.
- `pkg/sandbox`: `handlePutFiles` quoting and per-exec replay bounds
  (gke-labs/granule#235, `40f699f`), listed as in flight; and
  `Server.DialOut` keeps retrying with capped backoff after an
  `ErrAuthFailed` refusal rather than giving up, wrapping the last refusal
  in the error it returns when its context ends (gke-labs/granule#230,
  `762bdb8`). Grain's `pkg/sandboxaccess` and `pkg/orchestrator` tests of
  a refused wrapper now wait for the acceptor to count the refusal and
  then cancel the wrapper.
- `cmd/granule-shim`: the resize FIFO is held open across writers
  (gke-labs/granule#228) and its tests restore the log writer they found
  (gke-labs/granule#236).
- The Go module rename to `github.com/gke-labs/granule`
  (gke-labs/granule#237, `42b30d9`). Grain imports, requires, replaces
  and `go build`s the vendored tree under that path since grain task
  2973; the sync's `sed` rewriting it back to `github.com/bwsalmon/granule`
  is gone.
- GitHub Actions CI for granule itself (`.github/`, `docs/github-actions-ci.md`,
  gke-labs/granule#240) and the `CHV_DISK_DIRECT` benchmark
  (gke-labs/granule#241). Neither runs in this repository: GitHub only
  reads workflows from the root `.github/`.
- `pkg/config`: `GRANULE_WORKLOAD_ENV`, a comma-separated list of names
  whose values in the VM container's environment are added to the guest
  workload's (`CHV_CONTAINER_OVERRIDE`'s `env`). Only
  `deploy/github-runner-images` sets it, to hand Actions Runner
  Controller's `ACTIONS_RUNNER_INPUT_JITCONFIG` to the runner in the
  guest; no image grain builds or runs sets it, so nothing in a grain
  sandbox pod's environment reaches its guest by this route. That
  image's `build.sh` downloads the latest `actions/runner` release
  without a checksum; grain does not build it.

`Dockerfile` changes only by its `debian:bookworm-slim` digest
(gke-labs/granule#233); `go.sum`, `vendor/` and `pkg/guestimage`'s contract
are unchanged (its two files change only by import path), so
`scripts/granule/build-granule-image.sh` and `scripts/granule/guest-inputs.sh`
need no change.

## Patches in flight

None.

## Landed upstream

These were listed here as in flight, and all are on granule's own
`main` now. The syncs carry them, so grain no longer patches anything
to get them.

- **IPv6 e2e GKE project resolved from `GKE_PROJECT` or `gcloud`**
  (gke-labs/granule#244, grain task 3349). `.github/workflows/e2e-ipv6-gke.yml`,
  `docs/ipv6-dual-stack-design.md`, `docs/github-actions-ci.md` and
  `test/e2e-ipv6.sh` no longer default to a contributor-specific GCP project
  name (`91c0dfffd`), so `tests/hygiene` (`TestCoreSaysNoGoogleTokens`) stays
  clean without patching the vendored copy.

- **Per-VM cgroup memory limits and positive `oom_score_adj` for
  Docker-backed VM containers** (gke-labs/granule#234, grain task 500).
  `-memory-limit-mb` and `-oom-score-adj` (default `DefaultOOMScoreAdj =
  500`) on `granule vm create`, applied as `--oom-score-adj`, `--memory`
  and `--memory-swap` on `docker run`, so the kernel OOM killer picks a
  sandbox VM before the host daemon or `hostd` (`-900`). The default limit
  is now guest + max(512 MiB, guest/16) (gke-labs/granule#238); grain
  never relies on it and always passes guest + `max(1 GiB, 25%)`
  (`orchestrator.GranuleMemoryLimitMB`, grain task 2593).
- **`pkg/sandbox.Server` `handlePutFiles` quoting and per-exec replay
  bounds** (gke-labs/granule#235): single-quote escaping in the `trap ...
  EXIT` cleanup and directory error, `FileUpload.Owner` values starting
  with `-` refused, and the 16 MiB `MaxExecBufferBytes` / 10-minute
  `CompletedExecRetention` limits enforced.

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
