# granule as agent substrate's micro-VM runtime

> **Note**: This project has been renamed from **kontur** to **granule** (`konturctl` -> `granule`, `kontur` -> `granule-shim`, `Konturfile` -> `Granule` / `Granulefile`).


A candidate design for making granule the thing that actually runs a
sandbox in [agent substrate](https://github.com/agent-substrate/substrate):
what the seam between the two is, what would have to change on each side,
and whether the whole idea earns its cost.

Nothing here has been built. This is the document you read before
deciding to spend a quarter on it.

Both halves are checked against source, with file and symbol names given
so a reader can go and disagree:

- granule at `aac3d96` (this tree).
- agent substrate at `2c429a9` (2026-09-03), the public repo, Apache-2.0,
  and — its own README's words — "not an officially supported Google
  product", "in early development", "the APIs are almost guaranteed to
  change". Everything below is written against a moving target.

## The headline: the slot is not empty

The task was framed as adding granule as substrate's "uvm runtime". It is
worth saying plainly, first, that substrate already has one.

`cmd/ateom-microvm` is about 9,000 lines of Go that runs an actor inside
a [Kata Containers](https://katacontainers.io/) guest on
[cloud-hypervisor](https://www.cloudhypervisor.org/) — the same VMM
granule drives — with checkpoint/restore over `userfaultfd` demand paging,
virtio-fs-shared OCI bundles, host-backed durable volumes, and a
`SandboxClass` of `microvm` wired through the CRDs, the controller, the
node daemon and the e2e suite. It is not a stub. `cmd/ateom-microvm/run.go`
alone is 1,112 lines.

So this is not "fill an empty slot". It is "replace, or sit beside, an
incumbent that already does the hard parts". That changes the answer at
the end of this document, and it changes which questions are worth
asking. The interesting ones are: what does substrate demand of a
micro-VM runtime that granule cannot do; and what does granule do that
`ateom-microvm` does not.

## Substrate in one page, for a runtime author

Only the parts a runtime implementer has to care about.

**Actors on workers.** An *actor* is one sandboxed workload instance; a
*worker* is a long-lived Kubernetes pod waiting for one. A `WorkerPool`
CRD is reconciled by `atecontroller` into a `Deployment` of worker pods.
Actors are multiplexed across workers over time — suspended to object
storage when idle, restored onto whichever worker is free when traffic
arrives. The north-star numbers in `docs/architecture.md` are 100ms p95
activation, 1 billion actors per cluster, 1000 wakeups/second.

**Two node-level components.** `atelet` is a DaemonSet on each node: it
pulls images, assembles OCI bundles, fetches sandbox assets, and moves
snapshots to and from GCS/S3. `ateom` is a container *inside each worker
pod* that drives the sandbox runtime. The split exists so the pod's
lifecycle is decoupled from the sandboxed workload's.

**One ateom binary per sandbox class.** `ateom-gvisor` (default) and
`ateom-microvm` today, selected by `WorkerPool.spec.sandboxClass` plus
`WorkerPool.spec.workerImage`
(`cmd/atecontroller/internal/controllers/workerpool_apply.go`). The
runtime binaries themselves are *not* baked into the worker image: a
cluster-scoped `SandboxConfig` names content-addressed asset URLs
(`cloud-hypervisor`, `virtiofsd`, `kata-kernel`, `kata-image`, or
gVisor's `runsc`) which atelet fetches, verifies by SHA256, caches and
prewarms (`cmd/atelet/sandbox_prewarm.go`), and whose paths it passes to
ateom as `runtime_asset_paths`.

**The workload is OCI containers.** `WorkloadSpec` is a list of
`Container`s, each with a name, an optional HTTP `Readyz` probe, and four
kinds of volume mount (durable-dir, CSI, system-info, image). atelet
composes each container's bundle rootfs from the node's cached image
layers; ateom shares it into the guest and asks the guest agent to create
and start it.

**Ingress is HTTP, not exec.** There is no exec RPC anywhere in
substrate's API surface — not in `ateom.proto`, not in `atelet.proto`,
not in `ateapi.proto`. `atenet-router` resolves
`<actor>.<atespace>.actors.resources.substrate.ate.dev`, resumes the
actor, and opens a TLS tunnel to `atunnel`, which ateom hosts, which
forwards to the actor over its veth. A non-default port is reached with
an HTTP `CONNECT`. Actors are servers.

**Unprivileged workers.** Neither class runs privileged. `ateom-microvm`
gets a named capability set (the gVisor set plus `FSETID` and
`DAC_READ_SEARCH` for virtiofsd), `seccomp: Unconfined`, `AppArmor:
Unconfined`, and `/dev/kvm` from a device plugin atelet hosts
(`ate.dev/kvm` in `internal/deviceplugin`), with micro-VM pools tainted
`ate.dev/sandboxClass` so only capable nodes get them.

## The seam, concretely: the `Ateom` service

`internal/proto/ateompb/ateom.proto`. Six RPCs, and a two-state machine
per ateom — "available" or "executing", one actor at a time
(`internal/ateomcapacity`: `actorsPerAteom = 1`).

| RPC | What it must do |
|---|---|
| `RunWorkload` | Cold-boot an actor's containers. Carries identity, `WorkloadSpec`, `runtime_asset_paths`, an optional `EgressGateway`, and `cpu_milli`/`memory_bytes` from the ActorTemplate's limits. Available → executing. |
| `CheckpointWorkload` | Write the running actor's state into a local directory, at `FULL` or `DATA` scope, and reset to blank. Returns the list of files it wrote for atelet to upload. Executing → available. |
| `RestoreWorkload` | Restore from a snapshot atelet has already downloaded — at `FULL`, `DATA`, or `DATA_ON_GOLDEN` scope, the last combining the template's golden snapshot's memory with this actor's durable data. Available → executing. |
| `TerminateWorkload` | Stop and delete the workload, clean up network and bundle overlays. |
| `GetWorkloadStats` | Verified stats read for a caller asserting an `actor_uid`. `NOT_FOUND` on mismatch, `FAILED_PRECONDITION` when executing but not yet measurable. |
| `GetActiveWorkloadStats` | Discovery read with no asserted identity; returns either a self-describing sample or a `NoSampleReason`. |

That is the entire contract. It is small, well documented, and it is the
best specification of "what an agent-sandbox runtime must do" that either
project has written down. The integration shape follows from it directly,
so the three-shapes question the earlier sketch of this document agonized
over does not arise: **an integration is a new binary, `cmd/ateom-granule`,
serving this service, plus a `SandboxClass` value**. Not a library
substrate imports, not a per-host daemon.

The mechanical additions around it are small and enumerable:

- `SandboxClass` constant in `pkg/api/v1alpha1/sandboxconfig_types.go`,
  and the `+kubebuilder:validation:Enum=gvisor;microvm` markers there and
  in `workerpool_types.go`.
- Six existing switch points on the class:
  `cmd/atelet/sandbox_prewarm.go:148`, `cmd/atelet/main.go:929`,
  `workerpool_apply.go:327/370/423`, `internal/ateattr/ateattr.go:339`,
  `cmd/ateapi/internal/controlapi/converter.go`.
- `SANDBOX_CLASS_*` enum in `ateom.proto`, plus the asset names the new
  class expects and its `ValidatingAdmissionPolicy`
  (`manifests/ate-install/sandboxconfig-validation.yaml`).
- A capability set and pod shape in `workerpool_apply.go`. granule needs
  `/dev/net/tun` in addition to `/dev/kvm`; substrate's `SandboxDevices`
  list has only KVM today, with a comment saying tun is left to the
  runtime's own capability set — so `NET_ADMIN` covers it.

None of that is the hard part.

## What substrate demands, and what granule has

| Requirement | granule today | Verdict |
|---|---|---|
| Run N OCI containers from host-composed bundles inside the guest | Nothing. `pkg/agent` runs `<shell> -c <line>` as a login user in a fixed disk-image rootfs | **Structural gap.** See mismatch 2 |
| Reset to blank and accept a different actor in the same pod | `granule run` *is* PID 1 of its container; VM life == container life | **Structural gap.** See mismatch 1 |
| Checkpoint to a directory for upload; restore in a different pod on a different node | `pkg/hypervisor` has `Pause`/`Resume`/`Snapshot`, and restores via `--restore source_url=file://...,resume=true` at VMM launch, from the same `CHV_SNAPSHOT_PATH` that VM wrote | Primitives yes, machinery no. See mismatch 3 |
| Restore one golden snapshot into many actors | One snapshot, one VM, one path | **Structural gap.** See mismatch 4 |
| `DATA` and `DATA_ON_GOLDEN` scopes over durable volumes | No volume concept at all | Real work |
| Per-container HTTP readiness | A readiness signal exists at the VM level: `granule ready` (a mode of the container binary) says whether the guest answers *and*, where one is configured, whether the guest has created the file it declares its own workload up with (`-ready-file`). It is what `granule vm create -wait`/`vm wait`/`vm status` ask and what the pod manifests under `deploy/k8s/` set as the VM container's `readinessProbe`. It stops at one signal per VM -- per-*container* readiness inside the guest needs mismatch 2 first | Signal yes, per-container no |
| Resource stats, two sources, epoch-aware | None. cloud-hypervisor's API socket is wired for `ping`/power/pause/snapshot/resize only | Medium |
| Size the sandbox from `cpu_milli`/`memory_bytes` | `CHV_CPUS`/`CHV_MEMORY_MB` at boot, live `granule resize` within ceilings fixed at boot | Close. Ceilings can't be raised without a recreate |
| Interior netns, veth, tap with fixed MACs, atunnel ingress, tunneled egress | `netshim` splices the VM onto *the pod's own* interface, one VM per netns | **Wrong shape.** See mismatch 5 |
| Cluster DNS in the guest | Nothing writes a resolver; it depends on what the rootfs shipped | Small, and a known rough edge |
| Unprivileged worker | `privileged: true` for `/dev/kvm`, under docker and in a pod alike; no `--seccomp` passed to the VMM | Medium; substrate has already done this work for its own classes |
| Importable from another Go module | Everything is under `pkg/` | Trivial but blocking |

## Six structural mismatches

The table's "structural gap" rows, in the order that matters.

### 1. The pod outlives the sandbox; in granule it doesn't

`ateom` is a long-lived gRPC server. It boots an actor, checkpoints it,
resets to blank, and takes a different actor — in the same pod, without
restarting. That reset is most of what `cmd/ateom-microvm/shutdown.go`
and `internal/kata/cleanup_linux.go` are for.

granule has no such mode. `granule run` is the container's entrypoint,
holds the VMM as its child, streams the serial console to the container's
stdout, and exits when the VM does. There is no "boot me another one".
`granule` manages VM *pods*, which is the same assumption one level up.

So `ateom-granule` cannot shell out to `granule run`. It has to drive
`pkg/hypervisor.Runner` in-process — which means exporting it, and
which means the console no longer lands in `kubectl logs` for free
(substrate has `internal/actorlog` for that instead).

### 2. Substrate's workload is a container; granule's is a disk image

This is the deepest one. Substrate's actor is an OCI image. atelet pulls
it, composes a bundle rootfs from cached layers
(`imagecache.SetupBundleRootfs`), and ateom shares the merged tree into
the guest over virtio-fs and asks the Kata agent inside the guest to
`CreateContainer`/`StartContainer` against the bundle's `config.json`.
The guest is a *container host*, and the Kata agent is what makes it one.

granule's guest is the workload: a disk image, built by
`granule guest build` (boot, provision, scrub identity, commit) or by
`CHV_SETUP_SCRIPT` at boot, running whatever its init runs. There is no
OCI runtime in it, and `granule-agent` speaks `pkg/execwire` — one
shell line, a user, an optional tty — not an OCI spec.

Closing this means putting a container runtime in granule's guest, giving
granule a virtio-fs share, and teaching `execwire` (or replacing it with)
the Kata agent protocol. At which point the guest is a Kata guest and the
question is what granule is still contributing. This is not a gap to close
on the way to something; it is the reason the answer at the end is what
it is.

### 3. Restore is cross-pod and cross-node

granule's restore is a VMM launch flag pointed at the directory that same
VM's `CHV_SNAPSHOT_PATH` named. Substrate's is a `vm.restore` API call
into a pod that has never seen this actor, with:

- fresh tap file descriptors passed in, because the snapshot's virtio-net
  device is fd-backed and CH demands new FDs
  (`cmd/ateom-microvm/internal/ch/restorefds.go`);
- socket paths inside the snapshot rewritten for the new pod
  (`rewriteSnapshotSocketPaths` in `restore.go`);
- a deliberate memory-restore mode — `OnDemand` (userfaultfd demand
  paging) versus `Copy` — chosen by *reading the VMM's version*, because
  cloud-hypervisor ≥53.0 background-prefaults every registered page and
  refuses `vm.snapshot` until it finishes
  (`internal/ch/prefault.go`, and `restoreMemMode` in `restore.go`).

That last detail is the single best illustration of the gap in maturity.
granule has never had to think about it, because it restores at launch and
never restores onto a different host. Substrate has a named constant, a
version range with an open upper bound, and a comment explaining which
way to be wrong.

### 4. Golden-snapshot fan-out is the default path, not an optimization

Creating an `ActorTemplate` triggers a one-off "golden" boot, checkpointed
once. **Every new actor of that template is then restored from that one
shared snapshot** rather than cold-booted. Under
`onResume.fromData: Golden` it is also what a `DATA`-scope actor comes
back on.

granule's model is one snapshot per VM. Fanning one out to N VMs needs the
restore path to be read-only with respect to the snapshot, and needs a
guest-identity scrub with no current equivalent — otherwise every actor
shares a machine-id and whatever was in RAM when it was taken. Substrate
solved the networking half of this by *fixing* both MACs
(`hostVethMAC`/`actorGuestMAC` in `cmd/ateom-microvm/net.go`) so a
snapshot's frozen ARP cache stays valid on any pod. granule assigns the
guest whatever MAC the container runtime gave the pod, which is precisely
the assumption that breaks.

An earlier sketch of this design listed snapshot fan-out as a large but
optional granule feature. It is not optional: without it there is no
integration at all.

### 5. `netshim` is the wrong shape here, not a placement problem

granule's networking thesis is "the VM *is* the sandbox": `netshim` runs as
an init container, takes over the address and MAC the container runtime
assigned the pod, and hands them to the guest, so from outside the pod
looks like an ordinary single-container pod.

Substrate needs the opposite. The pod is a worker that hosts many actors
over its life and has its own identity, `atunnel` listener and TLS
certificates; the actor gets an *interior* network namespace with a veth,
a tap cross-connected to it by tc mirred-redirect filters, fixed MACs for
snapshot survival, a link-local gateway at `169.254.17.1`, and egress
either direct or tunneled through an `EgressGateway`.

`netshim` (`pkg/netshim`, ~650 lines) does not fit that, and neither
does `pkg/dockervm`'s third-container-holds-the-namespace trick.
`ateom-granule` would use substrate's `internal/ateomnet` and drop netshim
entirely. That is fine — but it is another large piece of granule that the
integration discards rather than uses.

### 6. Nothing execs

granule's single best feature is `granule exec`: vsock, no guest
networking, no SSH, stdout and stderr kept apart, exit code in its own
frame. It is the reason grain — which already runs its agents in granule
VMs — can use granule at all.

Substrate has no exec. Actors are HTTP servers reached through a tunnel.
An `ateom-granule` would carry `pkg/guestexec` and `pkg/execwire`
along as dead weight, useful only for debugging.

The three gaps in granule's exec path that an agent platform hits on day
one — readiness (since closed at the VM level by `granule ready` and the
guest's own `-ready-file`, and still open per *container* inside the
guest), no per-exec cwd or
environment (`pkg/agent`'s
`start` uses `cmd.Dir` = the account's home and a hardcoded `PATH`,
`HOME`, `USER`, `LOGNAME`, `SHELL`, `TERM`), no cancellation — are all
real and all worth fixing. They are just not on substrate's critical
path, because substrate never calls exec.

Cancellation is closed since this was written: `cmd/granule-agent` gives
each connection a context of its own, `pkg/agent` ends the command's
whole process group (SIGTERM, then SIGKILL) when that context is
cancelled or the client goes away, and `execwire.TypeSignal` lets a
client interrupt a command while keeping the session open to read what it
prints on the way out.

So is the per-exec working directory and environment: `execwire.Request`
carries `Dir` and `Env`, the agent overlays them on the login environment
rather than replacing it, and it reports through `Response.Features`
whether it honoured them. `TERM` was the last part of that hardcoded
environment that no request could reach, and it is carried too
(`Request.Term`): a client asking for a pty sends the terminal type it is
sitting in front of, instead of leaving a real terminal on the `dumb` the
agent's own empty environment produced. Readiness is closed too, as far
as one VM can close it: the guest declares its own workload up by
creating a file, and what stands is per-container readiness *inside* the
guest, which has no meaning until there are containers in there to be
ready.

## What granule would actually contribute

Two things, and they are both real.

**A host-file copy-on-write rootfs.** `ateom-microvm` captures the
container's rootfs delta in a **tmpfs overlay inside guest RAM**. That is
what makes a `FULL` snapshot self-contained, and it is elegant — but it
means a build-heavy actor's `node_modules` is charged as memory, in the
guest's working set and in every snapshot of it. granule's answer is
`CHV_DISK_MODE=overlay`: `pkg/qcow2` creates a QCOW2 overlay with 2MB clusters
and opportunistic reflink cloning, so N VMs share one backing image with no copy.
For coding agents — substrate's README names Claude Code and Codex as
target workloads — that is a directly relevant cost. It is also
~360 lines with no dependencies, which makes it a plausible contribution
*to* `ateom-microvm` rather than a reason to replace it.

**Elastic guest memory.** Substrate sizes the VM from the ActorTemplate's
limits at boot, and on a `FULL` restore "the size baked into the snapshot
is authoritative". granule boots at 256MiB with a 2048MiB ceiling and
grows: `granule resize` drives `vm.resize`, and with `CHV_MEM_AGENT` the
guest asks for growth itself when `/proc/pressure/memory` says so
(`pkg/memagent`, plus `cmd/granule-mem-agent` in the guest). Density
is substrate's whole thesis; an actor that costs its working set instead
of its limit is on exactly that axis.

Everything else granule has, substrate has an equivalent of, usually a
more developed one. `pkg/hypervisor` versus `internal/ch`:
substrate's is smaller in line count but knows about `restorefds`,
`merge`, `guestclock` and `prefault`. `granule` versus atelet: a
lifecycle over a local docker daemon versus a real controller.

## What substrate has already solved that granule hasn't

Worth listing, because these are the items granule's own roadmap would
otherwise have to work through:

- **Unprivileged VM workers.** A named capability set with a comment per
  capability explaining which process needs it and why, rather than
  `privileged: true`. Plus a `/dev/kvm` device plugin hosted by the
  existing node DaemonSet, so a worker gets the device without the
  privilege.
- **Node eligibility as scheduling.** `ate.dev/kvm` as an extended
  resource, `ate.dev/sandboxClass` taints on micro-VM pools, and device
  presence checked at atelet startup — instead of "make sure the node
  pool had nested virtualization enabled at creation".
- **Runtime assets out of the image.** Content-addressed, SHA256-verified,
  prewarmed on config change with jitter across the fleet. granule bakes
  guest image and kernel into its OCI image, which is simple and makes
  cold pulls expensive.
- **Demand-paged restore, with a version-gated workaround** — see
  mismatch 3.
- **Actor log forwarding, OTLP relay, per-actor attribution of stats.**

## Changes needed, on each side

### On the granule side

Ordered smallest first. Items 1–4 are worth doing regardless of
substrate, which is the main argument for them.

1. **A supported package.** Everything importable is under `pkg/`.
   `pkg/hypervisor` (Runner + APIClient), `pkg/qcow2` and the
   client half of `pkg/guestexec` would have to move behind
   `pkg/` or `client/`. `execwire`'s package comment says the protocol
   carries no version negotiation because both ends ship from one commit;
   an external importer breaks that, so this is also where a handshake or
   an explicit "these must match" statement gets decided. *Small, blocks
   everything.*
2. **Readiness.** *Done, at the VM level, in both halves.* `granule ready`
   is a mode a container probe can call: the rendered pod manifest sets
   it as the VM container's `readinessProbe`, so the pod's Ready
   condition reports on the guest, and `granule vm create -wait`/`vm
   wait`/`vm status` ask the same question through the same mode. The
   guest-declared signal this used to list as outstanding is there too:
   `-ready-file` (`GRANULE_READY_FILE` on the container) names a path the
   guest creates when its own workload is up, checked on top of the agent
   answering, and the two conditions are reported apart --
   `guestexec.ErrNotDeclared`, exit status 2 -- so "still booting" and
   "booted, workload not up" are different answers rather than one "not
   ready". It cost item 1's versioning question nothing, which is worth
   recording: nothing new travels on the `execwire` connection for any of
   it (the declaration is an ordinary `test -e` command line and an
   ordinary exit status), the mode is a new argv rather than a new field,
   and a VM whose image predates it is detected by that image's own
   "unknown mode" line and asked both conditions through `granule exec`
   instead. A probe outside the container, keyed on a message the older
   binary already printed, is the cheapest form the compatibility
   question can take -- and the reason this one did not need a handshake.
   What is left is still the part above it: readiness per *container*
   inside the guest needs item 8's container model first. *Small.*
3. **Per-exec cwd and environment.** ~~`Dir` and `Env` on
   `execwire.Request`, honoured in `pkg/agent`'s `start`, with
   `loginEnv` as the base rather than the whole. This is what stops
   consumers baking their toolchain into `/usr/local/bin` to land inside
   a fixed `PATH` — which is exactly why grain does.~~ *Done*, and it
   brought the first answer to item 1's versioning question with it:
   `Response.Features` names the optional parts of the protocol an agent
   implements, and `ReadRequest` refuses a field it has never heard of,
   so a mismatched pair says so instead of quietly running the wrong
   thing.
4. **Cancellation.** ~~Per-connection context for `agent.Serve`,
   cancelled when the connection drops, plus a signal frame. Without it a
   timed-out tool call leaves a process running in a long-lived
   sandbox.~~ *Done.* Both halves landed, and the signal frame is
   announced through item 3's `Response.Features` rather than through a
   mechanism of its own: a frame type whose absence changes what happens
   gets a feature name (`execwire.FeatureSignal`) exactly as an optional
   request field does, so a client needing a frame an older agent does
   not have reports the mismatch instead of being stepped over in
   silence. That is a name for the mismatch, not negotiation — the
   ship-from-one-commit rule is unchanged, and an external importer would
   still be the thing that breaks it.
5. **A VM lifecycle that isn't a process lifecycle.** `Runner` usable by
   a long-lived server that boots, checkpoints, tears down and boots
   again — mismatch 1. *Medium.*
6. **Restore into a foreign host.** `vm.restore` over the API rather than
   `--restore` at launch: fd passing, socket-path rewriting, an explicit
   memory-restore mode, and the VMM-version gate. Substrate's
   `internal/ch` is the reference implementation; this is largely
   transcription, which is itself the argument against doing it. *Large.*
7. **Snapshot fan-out with identity scrub.** Mismatch 4. *Large, and
   mandatory for this integration rather than optional.*
8. **A container runtime in the guest.** Mismatch 2. *Very large, and the
   one that decides the whole question.*

### On the substrate side

If someone did this anyway, substrate's share is genuinely modest, which
says good things about the `Ateom` seam:

- A `granule` `SandboxClass` constant, the two `+kubebuilder` enum
  markers, and the six switch points listed above.
- `SANDBOX_CLASS_KONTUR` in `ateom.proto`, plus asset names and a
  `ValidatingAdmissionPolicy` for the new class's `SandboxConfig`.
- A capability set and pod shape in `workerpool_apply.go`; granule's
  needs are close enough to `ateomMicroVMCapabilities` that reuse is
  plausible. `/dev/net/tun` needs no new device grant given `NET_ADMIN`,
  matching the existing comment in `internal/deviceplugin`.
- e2e fixtures: `internal/e2e/sandbox.go` hardcodes the micro-VM class
  name in several places and would want a general mechanism.
- Nothing in the control plane, atenet, or the snapshot storage path.

The asymmetry is the finding. Substrate is *built* to take a third
runtime. granule is not built to be one.

## Does it make sense?

Three different propositions get three different answers.

**"Replace `ateom-microvm` with granule": no.** granule would have to grow
an in-guest container runtime, cross-node restore, golden-snapshot
fan-out, durable volumes, stats and a readiness story, while discarding
`netshim`, `granule`, the environment-variable configuration model and
`granule run`'s PID-1 shape — that is, nearly everything that is
distinctively granule. What survives is `pkg/hypervisor` and
`pkg/qcow2`. Substrate already has a better version of the first.
The end state is `ateom-microvm` with a different disk overlay helper, reached
by a year of work.

**"Add granule as a third `SandboxClass` alongside the other two": not on
the evidence available.** A third class has to be better at something for
somebody. On isolation it is identical — same VMM, same KVM boundary, and
substrate's confinement of it is *stronger* today (unprivileged worker,
per-capability justification, device plugin) than granule's
`privileged: true` with no `--seccomp`. On latency there is no case:
substrate restores from a golden snapshot with demand paging against a
100ms target, while granule's measured cold boot is ~1.3s bare and ~2.3s
as a GKE pod, with no pooling and no restore-into-a-fresh-pod path at
all — and this repo's own benchmark notes record the same boot taking
10–16s on a contended nested host. On density, granule's elastic memory is
a real advantage, but it is one feature, not a runtime.

**"Land granule's two good ideas in `ateom-microvm`": yes, and this is
the recommendation.** The VMDK overlay rootfs and guest-driven memory
growth are both genuine, both small, both aimed at costs substrate has
chosen to carry, and neither requires granule to become something else.
Two upstream contributions of a few hundred lines each, against a project
that publishes a contribution guide and holds a weekly community meeting,
is a far better use of the same effort than a third sandbox class.

**And for granule itself:** the honest conclusion is that substrate is not
granule's market. Substrate is a Kubernetes control plane with a
PostgreSQL state store, a DNS mesh, an Envoy `ext_proc` router and a CRD
surface — for people running a million actors. granule is one OCI image
you can `docker run` on a KVM host to get a VM, with an exec path that
works before the guest has networking. Those are different products for
different people, and the audience granule serves — grain among them,
today — is precisely the one that does not want substrate's control
plane.

The most valuable thing to take from this exercise is not an
integration. It is that `ateom.proto` is a rigorous, externally-written
specification of what a production agent-sandbox runtime has to do, and
that reading it names five things granule is missing which have nothing to
do with substrate: readiness, resource stats, VM lifecycle independent of
process lifecycle, restore into a fresh host, and snapshot fan-out. The
first of those has since been built as far as it goes without an in-guest
container model — `granule ready`, the guest's own `-ready-file` on top of
it, and the pod readiness probe on top of that — which is the one place
this document has been overtaken.

## If you did it anyway: what a spike must prove

Roughly a month, in this order, stopping at the first thing that fails.
Each step is chosen to fail fast on a structural mismatch rather than to
build toward the happy path.

1. **A container in granule's guest.** Put an OCI runtime in the reference
   guest, hand it a bundle over virtio-fs, and start it. If this is not
   comfortable within a week, mismatch 2 has answered the whole question
   and the rest is moot.
2. **Boot latency on substrate's real nodes**, with its node type and
   nesting depth: 20 boots, p50 and p99. `benchmarks/standalone/run.sh`
   and `benchmarks/gke/run.sh` already do the measuring; the number they
   produce on substrate's hardware is what decides whether latency is a
   reason to be here.
3. **Restore into a pod that has never seen the VM**: snapshot on one
   host, restore on another, with fresh tap FDs and rewritten socket
   paths. Then two VMs from one snapshot, checked for whether they are
   actually independent.
4. **Only then, the `Ateom` service** — `RunWorkload`, `Checkpoint`,
   `Restore`, `Terminate` — run against substrate's own e2e suite, which
   is the cheapest correctness oracle either project has.
