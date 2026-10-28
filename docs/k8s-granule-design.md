# Serving granule-in-Kubernetes from `grain init`

**Status: implemented.** See "What building this actually settled"
near the end of this document for where the implementation follows
this proposal literally and where it diverges. This is the design for
the fourth and last of `grain init`'s deployment shapes
(`pkg/hostinit.PlatformK8sGranule`, `-platform=k8s-granule`; see
[grain-init-platforms.md](grain-init-platforms.md) for the full table).
[k8s-init-design.md](k8s-init-design.md) designed the third row,
`k8s-container`, and named this one explicitly rather than covering it,
because the two platforms' preflights say opposite things about the same
property. This document makes that argument in full and settles the rest
of what `k8s-granule` needs; it does not implement any of it.
Implementation should follow review of this document, and should land
after `k8s-container`'s own implementation does, for a reason given below
under "What this depends on."

## Why this cannot be a mode of `k8s-container`

Restating `k8s-init-design.md`'s table, because it is the whole reason
these are two `Platform` values rather than one `-target=k8s` with a
flag:

| | `k8s-container` | `k8s-granule` |
|---|---|---|
| privileged pods | **forbidden** -- `pod-security.kubernetes.io/enforce: baseline` on the `grain` Namespace exists to prevent them | **required** |
| nested virtualization | irrelevant | **required** on every node that schedules a sandbox |
| `/dev/kvm` | not needed | needed *inside the pod* |
| isolation | pod shares its node's kernel | VM, own kernel -- the same boundary `linux-kvm` gives, cluster-scheduled |

A cluster preflighted and admission-configured for one of these rows
fails the other's preflight outright, by design: a `baseline` namespace
refuses every pod `k8s-granule` needs to create, and a `privileged`
namespace is exactly the thing `k8s-container`'s own README calls out as
the hole its preflight exists to not open. There is no single check that
could tell an operator "this cluster is ready for k8s" that means
anything for both, so `grain init` must not pretend one exists. Each
platform's preflight checks only its own requirement and refuses loudly
if it finds the other platform's assumption instead -- never adapts.

## What this depends on

`third_party/granule/deploy/k8s/gke.md` is the tested half of this: a
real GKE cluster, created with `--enable-nested-virtualization`, running
a privileged pod whose single container is the `granule` image itself,
booting a cloud-hypervisor VM over `/dev/kvm` with no other cluster setup
-- validated end to end, including guest exec over vsock, inline
networking, pod-to-pod guest traffic, resize, and `CHV_SETUP_SCRIPT`.
That document is granule's own walkthrough, independent of grain; nothing
here needs to re-prove it.

What `k8s-container`'s implementation gives this platform, once it lands,
is `pkg/k8s.Client` (pod create/delete/wait-ready/list, the `kubectl
exec` transport) and `orchestrator.K8sSandboxes` as a worked example of
how that client composes into a `Sandboxes` backend grain's dispatcher
can drive. "What it reuses" below says exactly how much of that is
reusable as-is versus merely a pattern to repeat. Writing this design
before `k8s-container` has merged would mean designing against code that
does not exist yet under a name that might still change; waiting for it
costs nothing, since this platform was already refused in preflight with
no implementation behind the refusal.

## 1. Where the namespace relaxation comes from

`deploy/kubernetes/README.md` already answers this for the escape hatch
`k8s-container` offers (`-k8s-sandbox-privileged`, for a deployment that
needs nested docker or kind in its *sandbox* pods): the operator "must
build its own image and opt into `-k8s-sandbox-privileged` knowingly --
and relax the `grain` Namespace's `pod-security.kubernetes.io/enforce`
label from `baseline` to `privileged` with it." `k8s-granule` needs the
same relaxation unconditionally -- not as an opt-in escape hatch but as
the entire premise of the platform.

**`grain init` should refuse until the operator has made this change
themselves, reporting it as a `StepManual` with the exact command,** the
same way `checkCommand` already reports a missing `make` or `node`:

```
kubectl label namespace grain pod-security.kubernetes.io/enforce=privileged --overwrite
```

Reasons this is the right shape, not merely the cautious one:

- **The blast radius outlives this deployment.** Pod Security admission
  on a Namespace is not scoped to grain's own ServiceAccount -- it is a
  standing property of the namespace that bounds *every* pod anyone or
  anything ever creates there, including a future workload with no
  relationship to grain at all. A single-host platform's preflight never
  has to weigh this, because nothing on a bare host is a shared security
  boundary the way a namespace's admission policy is; the moment `grain
  init` starts making cluster-wide policy decisions on an operator's
  behalf, it is doing something none of the other three platforms do
  anywhere else in this package, and it should not slip in disguised as
  "provisioning."
- **It would be the one step in this platform that is not idempotent in
  the safe direction.** Every other `StepDone`/`StepSkipped` in this
  package either changes nothing on a second run or changes the same
  thing again harmlessly. Flipping a namespace-wide admission label is a
  one-way door for every other workload sharing that namespace (the
  README's own operating note: "one deployment per namespace," because
  the startup sweep already assumes it owns the namespace) -- but "this
  deployment owns the namespace" is an assumption about *this*
  deployment, not a license to change what the namespace permits for
  anything else an operator might add to it later without rereading
  `grain init`'s own output.
- **It costs the operator one command, already in their own hands.** This
  is not a case like the `uid-1000` chown migration, where `grain init`
  genuinely cannot run the fix itself (no access to the cluster's own
  service account with cluster-admin, possibly, or simply "this is risky
  enough that `deploy/kubernetes/README.md` already reports it as a
  `[manual]` block rather than scripting it"). It is the same shape as
  that migration: a precise command, reported rather than run, because
  the operator is the one who should be looking at it when it executes.

The preflight check itself is cheap and honest: `kubectl get namespace
grain -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'`
and compare to `privileged`. Unlike the `k8s-container` checks this
mirrors (sandbox image pullable, storage class binds RWO), there is
nothing to create and tear down here -- reading the live label is the
entire check, and it is unambiguous: either the namespace already says
`privileged` or it does not. This check is a **gate**, in
`checksFor`'s own vocabulary: nothing else in this platform's preflight
list -- not the nested-virt check below, not the image check -- can run
against a namespace that would refuse the very pod those checks need to
schedule, so a failure here aborts the rest of the list immediately,
exactly as `checkGOOS` does for the two implemented platforms.

One consequence worth naming: if the namespace does not exist at all
yet (a first-time `k8s-granule` deployment with no prior `k8s-container`
history in it), the same `StepManual` applies -- `kubectl create
namespace grain` first, then the label command, or a single `kubectl
apply` of a Namespace manifest that already carries the label, which the
operator writes or copies from this platform's own manifest (below) and
applies themselves before running `grain init`. `grain init` never runs
`kubectl create namespace` or `kubectl apply -f` against anything that
sets this label; it only ever reads it.

## 2. Node-level nested virtualization

The single-host lesson (`third_party/granule/deploy/k8s/gke.md`, "On a
plain GCE instance": "`/dev/kvm` should exist... Both can pass on a
machine where acceleration silently falls back to emulation, though...
A granule VM on an emulated CPU boots and is unusably slow rather than
failing") applies here with one extra twist: from outside a pod, before
scheduling anything, `grain init` cannot even read `/dev/kvm`'s
permission bits on a node the way it can read a file on the host it
is running on. There is no `System.Run` against a node from a process
sitting outside the cluster. Nor should this check reach for a
cloud-specific API (`gcloud container node-pools describe`) to read back
`--enable-nested-virtualization`: `grain init`'s other three platforms
never ask what cloud they are on, and a check that only works on GKE
would be a check this platform's own table does not promise -- the
manifest and the preflight have to work the same way whether the nodes
came from GKE, a bare-metal `kubeadm` cluster with VMX-capable hardware,
or anything else that satisfies the same two primitives (nested virt,
`/dev/kvm`).

**The honest check is to schedule a real, bounded-wait pod and watch
whether it boots a VM in the time real acceleration takes -- not whether
`/dev/kvm` merely exists.** Concretely: a throwaway pod running the
*actual* sandbox image (not a no-op, unlike `k8s-container`'s pull
check), privileged, with the `/dev/kvm` hostPath every manifest below
needs, `CHV_NET_MODE: none` (no network needed to prove this), and the
same `granule ready -timeout 0` readiness probe
`gke-pod-example.yaml` ships. Wait for `Ready` with a deadline measured
in low tens of seconds, not minutes -- `gke.md`'s own validated numbers
put a real boot at 2.3s end to end (`granule-agent.service` active at
1.8s) on real KVM, so a deadline of, say, 30s gives a cold image pull
room to finish before the boot itself is even timed, while still being
far short of what an emulated boot would need. A pod that fails to
reach `Ready` in that window is reported as "nested virtualization is not
enabled, not accelerated, or `/dev/kvm` is not reachable from the pod" --
one `StepManual` covering all three, since this check cannot tell them
apart and does not need to: the fix an operator reaches for is the same
either way (confirm the node pool's nested-virt flag, confirm the KVM
kernel module is loaded on the node, confirm no PSP/Gatekeeper policy is
silently stripping the hostPath or the privileged flag before it reaches
the kubelet).

This is a strictly stronger check than `PlatformLinuxKVM`'s own
`checkCommand("/dev/kvm", ..., "test", "-e", "/dev/kvm")` preflight,
which only proves the device node exists and never boots anything --
that platform accepts the single-host version of exactly the silent-
emulation risk this section opens with, and has for as long as it has
existed. `k8s-granule` can afford to do better here specifically because
proving the real thing (schedule the actual sandbox image, wait out a
real boot) costs about the same as the cheaper, weaker check `k8s-
container`'s own pull-check already pays for (one throwaway pod, created
and deleted) -- it is not a new category of preflight, just a tighter
bound on the same one. Nothing here obligates tightening
`PlatformLinuxKVM`'s own check retroactively; that is a separate,
smaller task if anyone wants it, not a prerequisite for this one.

**Which node the throwaway pod lands on is the one real gap**, and it is
worth naming rather than glossing over: an untargeted pod proves nested
virt on whichever node the scheduler happens to choose, which on a
cluster with more than one node pool may not be the pool that will
actually run sandboxes. The fix is the same one `K8sConfig.NodeSelector`
already gives operators for pinning sandbox pods to a build-sized node
pool: a flag naming the node selector this check (and every real sandbox
pod this platform ever creates) should carry, defaulting to none on a
single-pool cluster. A heterogeneous cluster that gives no selector gets
exactly the honest limitation the single-host platform already has --
this proves one node, not every node -- which is a limitation worth
documenting in the check's own `StepSkipped`/`StepManual` detail text
rather than hiding.

## 3. Which manifest

This platform needs its own manifest, not a flag-selected variant of
`deploy/kubernetes/grain.yaml`, for a reason stronger than tidiness: the
two platforms' manifests must never be interchangeable, for the same
reason their `Platform` names are not one flag. Concretely, three things
differ, and none of them are a single flag flip:

- **The Namespace block is not bundled into this manifest's `kubectl
  apply` at all** -- see §1. `grain.yaml` applies its Namespace (with
  `enforce: baseline`) as part of one `kubectl apply -f grain.yaml`; a
  `k8s-granule` manifest that did the same with `enforce: privileged`
  would be `grain init` performing exactly the relaxation §1 argues it
  must not perform. The manifest this platform ships should therefore
  either omit the Namespace object entirely (documented as "created and
  labelled by the operator first, per the preflight's own instructions")
  or, if a copy is useful as a reference for operators writing their own,
  live in the same file as a comment-only example the apply step never
  applies -- not as a live object in the YAML `grain init` runs.
- **The daemon's own pod is still never privileged.** This is worth
  stating plainly because it is easy to assume otherwise: only *sandbox*
  pods need `/dev/kvm` and `privileged: true`. The grain daemon's own
  Deployment, ServiceAccount and Role are identical in shape to
  `k8s-container`'s -- same uid 1000, same `securityContext`, same RBAC
  verbs (creating a privileged pod is an admission decision the
  Namespace label governs, not an RBAC verb the Role needs more of). The
  manifest reuses that block near-verbatim.
- **The Deployment's own flags differ**: `-k8s-granule-image` names the
  packed guest image (§4) rather than `k8s-sandbox`, plus whatever new
  flags name the node selector (§2) and the `/dev/kvm` hostPath mount
  that every sandbox pod this backend creates needs -- `K8sConfig` has no
  field for a hostPath device mount today (it was never needed for plain
  container sandboxes), so this is real new surface on the config struct
  behind these flags, not a value slotted into an existing one.

None of that is achievable as "the same `grain.yaml`, minus a label" --
it is a distinct file, call it `deploy/kubernetes/grain-granule.yaml`,
documented in a `k8s-granule`-specific section of
`deploy/kubernetes/README.md` the way the chown migration and the
`-k8s-sandbox-privileged` escape hatch already get their own call-outs,
not a new top-level doc (that README is already the home for "what has
to exist before you apply anything" prose; this platform's version of
that prose belongs beside the existing one, not duplicated elsewhere).

## 4. Whether the guest images are built or pulled

Pulled -- and this is a cleaner story than `linux-kvm`'s, not a
diminished one. `PlatformLinuxKVM` builds three images locally
(`granulefile build` against `Granulefile.guest-base` then
`Granulefile`, ~12 minutes) because the point of that platform is a
single host building exactly the guest it will run, with no registry in
the loop at all. `gke.md`'s walkthrough never does that: its guest disk
image and kernel are baked into the `granule` OCI image itself at
*build* time (the main README's "Guest disk image and kernel"; `CHV_
DISK_IMAGE`/`CHV_KERNEL` left unset so `granule run`'s own defaults take
over), so running it anywhere -- a cluster or a single `kubectl run` --
needs nothing but that one image, already pulled the way any pod image
is.

That means `k8s-granule`'s analogue of `k8s-container`'s
`-k8s-sandbox-image` is a **packed guest image** -- a self-contained
image with its own guest disk and kernel already baked in, the shape
`Granulefile`'s own build produces, not `GranuleRuntimeImage`/
`DefaultGranuleRuntimeImage` ("granule:vendored", what
`scripts/granule/build-granule-image.sh` produces for `-platform=linux-kvm`
and `-platform=linux-container`). That second image is a real
footgun here specifically: it is also, legitimately, called "the
granule runtime image" elsewhere in this same package, and it has no
guest disk at all, so a pod running it exits immediately rather than
booting -- confirmed against a real cluster running this platform for
the first time (see "What building this actually settled" below).
It is built once (by CI, the same `ghcr.io/bwsalmon/grain/...` scheme `k8s-sandbox` already uses --
see `deploy/kubernetes/README.md`'s "A sandbox image" and
`.grain/workflows/promote.yml`'s `latest` tag across
`ghcr.io/bwsalmon/grain/{grain,guest,k8s-sandbox}`) and pulled by every
pod this backend creates, exactly as `k8s-sandbox` is today. **This
image already exists.**
`.github/workflows/build-artifacts.yml`'s `sandbox-guest` job pushes it
under two immutable shapes on every commit that reaches it --
`guest:sha-<short-commit>` and the content-addressed
`guest:inputs-<hash>` a build whose guest inputs did not change reuses
-- and this is the same artifact `-platform=linux-kvm`'s own
`-sandbox-image` names (`pkg/daemon`'s `DefaultSandboxImage`, stamped to
`guest:sha-<commit>` at build time via `SANDBOX_IMAGE`/`-X
pkg/daemon.DefaultSandboxImage`). So `-k8s-granule-image` defaults to exactly
that: `DefaultSandboxImage`, the same stamped value, the same way
`-k8s-sandbox-image` defaults to `defaultK8sSandboxImage`. (A moving
`guest:main` is also published -- `publish-grain-image`'s own `retag`,
alongside the grain image's own moving tag -- but `grain init`'s
per-build default has no need of one: it is stamped to the exact
commit it was built from, the same reason `-sandbox-image`'s own
default does not use it either.) Whether
`deploy/kubernetes/grain-granule.yaml`'s own, separate
`-k8s-granule-image` -- the daemon's flag, not `grain init`'s -- should
name `guest:main` instead of its current `CHANGEME` is a real question
this change does not answer; see "What this does not settle" below.

What the preflight checks about that image is folded into §2's single
throwaway pod: a pull failure and a boot failure are both just "this pod
never became Ready," and both are reported the same way, because
distinguishing "wrong tag" from "right tag, no nested virt" from outside
the pod is exactly as hard as it already is for `k8s-container`'s own
pull check, and that check does not try either -- `ImagePullBackOff`
either shows up in the pod's own status (worth surfacing in the refusal
text the same way, since `kubectl describe pod` already would) or the
pod pulls fine and simply never boots in time.

Per-deployment guest customization was settled after this was written:
`model.Config.SandboxImage` names a custom OCI image built `FROM`
`guest-base` (`docs/sandbox-image-contract.md`), and `kubernetes-granule`
runs it the same way `linux-kvm` does, with no derivation on any host.
(The setup script this paragraph used to weigh, and `gke.md`'s boot-time
`CHV_SETUP_SCRIPT` alternative to it, are both moot.) Left open
otherwise, same as the flag-naming questions `k8s-init-design.md` left open for `k8s-
container`.

## 5. What it reuses

`ProvisionOverAPI` is unchanged, as it is for every platform -- nothing
in this document touches it, and nothing should.

Beyond that, the honest answer is: **less of `k8s-container`'s own apply
path than its manifest's resemblance suggests, but a real amount of its
underlying machinery.**

What carries over directly: `pkg/k8s.Client`'s pod lifecycle -- create,
delete, `WaitReady`, `ListSandboxes` for the startup sweep -- does not
care what is running inside the container it is managing, so creating a
privileged pod with a hostPath volume is new *arguments* to the same
calls, not new code in that package. The preflight shape (`platformCheck`,
`checksFor`, gate-vs-ordinary checks, `StepManual`/`StepRefused`) is
exactly `k8s-container`'s own vocabulary, extended with the two checks
above in the same list style.

What does not carry over: `orchestrator.K8sSandboxes` itself is the
wrong shape to extend, not the right one to configure differently.
Its `execRunner` wires `mcp.KubectlExecRunner` straight at the sandbox
container (`kubectl exec <pod> -c <container> -- <argv>`) because a
plain sandbox pod's main process *is* the thing a tool call should reach.
A granule pod's main process is the VM container's `granule run`, not a
shell an agent's tool calls should land in directly -- every command has
to go one layer deeper, over granule's own vsock-based exec protocol
(the main README's "Execing into a VM"; `gke-pod-example.yaml`'s own
comment: "`kubectl exec <pod> -- granule exec -- <command>` reaches the
guest over the VM's own vsock device"). That is a one-line difference in
shape (wrap the argv in `["granule", "exec", "--"]` before handing it to
the same `KubectlExecRunner`) but it is a difference in a type
(`orchestrator.Sandboxes`) `K8sSandboxes` does not expose a seam for
today, which means this platform needs its own
`orchestrator.Sandboxes` implementation -- plausibly a small one, built
by composing `pkg/k8s.Client` and a wrapped `mcp.KubectlExecRunner` the
way `K8sSandboxes` already demonstrates how to, but a new type with its
own tests, not a configuration flag on an existing one. Readiness is the
one place this platform's pod spec already does the hard part for free:
`gke-pod-example.yaml`'s own `readinessProbe` (`granule ready -timeout
0`) means `WaitReady`'s existing poll-the-pod's-own-Ready-condition logic
needs no change at all to mean "the guest has booted," not just "the
container started" -- that part really is free reuse, not merely similar
code.

So: `ProvisionOverAPI` unchanged; `pkg/k8s.Client` reused unchanged;
the preflight *pattern* reused and extended with two new checks; a new,
small `orchestrator.Sandboxes` implementation that is not
`K8sSandboxes` configured differently, built the same way `K8sSandboxes`
was; and a new manifest, not a variant of the existing one. That is
real, substantial reuse at the plumbing layer and real, substantial new
code at the backend layer -- worth saying in that much detail rather than
"mostly the same as k8s-container," because an implementer who expected
the latter would start by trying to add a `Privileged` path to
`K8sSandboxes` and discover partway through that the exec transport
underneath it is the wrong shape for what this platform actually runs.

## Open questions this proposal is deliberately not settling

1. **The granule-runtime image's exact publish target and tag scheme**
   (§4) -- name, build trigger, and whether it is promoted the same way
   `k8s-sandbox` is in `.grain/workflows/promote.yml`. This is
   infrastructure work independent of `grain init`'s own code and can
   proceed in parallel with everything else here once agreed.
2. **The node-selector flag's exact name and default**, and whether
   `grain init` should be able to discover node pools at all (e.g.
   reading `kubectl get nodes -o jsonpath=...` for a label an operator
   already put on the nested-virt pool) rather than requiring the
   operator to name a selector explicitly. Leaning toward "require it
   explicitly" for the same reason `k8s-container`'s own NetworkPolicy
   check needs an explicit opt-in flag rather than probing: a cluster
   that silently reinterprets "no selector given" as "any node" would be
   the same kind of guess this whole package refuses to make elsewhere.
3. **~~Whether `SandboxSetupScript` should thread through as a pod-spec
   env var~~** -- settled: an operator who needs guest customization
   builds their own image `FROM guest-base` and names it in
   `model.Config.SandboxImage` (§4's closing paragraph).
4. **The exact new `K8sConfig`-adjacent struct** the new
   `orchestrator.Sandboxes` implementation needs (device mounts, the
   wrapped exec runner, resource ceilings mirroring
   `gke-pod-example.yaml`'s `CHV_CPUS_MAX`/`CHV_MEMORY_MAX_MB`) -- real
   implementation, deliberately left for whoever builds this rather than
   designed field-by-field here.
5. **Everything `k8s-init-design.md` itself left open** (is `grain init`
   the right home versus a sibling subcommand; the admin-password
   file's landing spot with no shared filesystem) applies identically
   here, once `k8s-container` settles it -- this document does not
   re-argue those questions, only inherits whatever `k8s-container`
   decides, since both platforms share `ProvisionOverAPI` and the same
   `-ui-addr` contract.

## Explicitly out of scope here

Implementation of any of the above; the exact preflight command
invocations beyond the shape described; the granule-runtime image's
publish pipeline; and reconsidering `PlatformLinuxKVM`'s own, weaker
`/dev/kvm` check, which this document's §2 argues this platform can
improve on but does not require improving elsewhere.

## What building this actually settled

The preflight shape, §1's refusal-not-relaxation, and §2's
scheduled-boot nested-virt check are all exactly as designed above --
`pkg/hostinit/k8s_granule.go` implements them more or less literally.
§2's own check was proven against a real cluster, not only reasoned
about: a `kind` cluster with the host's `/dev/kvm` bind-mounted into
the node container (`extraMounts`, the same shape `gke.md`'s own
"stand-in pass on a `kind` cluster" used) and a `third_party/granule`
image built locally with `docker build -t granule .` -- the exact
one-line recipe `gke.md` gives, no `grain` Granulefile pipeline
involved at all, confirming §4's own argument that this platform's
image is a different, simpler kind of artifact than `linux-kvm`'s
three. Applying this design's own privileged, `/dev/kvm`-mounted,
`readinessProbe`-carrying pod manifest against that cluster reached
`Ready` in about 11 seconds end to end, inside the "low tens of
seconds" bound §2 derives from `gke.md`'s validated numbers, and
`kubectl exec <pod> -- granule exec -- uname -a` reached the guest
exactly as §5 describes. `grain init -platform=k8s-granule -dry-run`
against that same cluster (labelled `pod-security.kubernetes.io/
enforce=privileged` by hand first, per §1) ran every preflight check
for real and reported every one correctly -- cluster-reachable,
namespace label, RBAC, and the nested-virt check itself.

**What this proves and what it does not.** The nested-virt check --
the one real, novel claim this platform makes over
`PlatformLinuxKVM`'s weaker `/dev/kvm`-exists check -- is proven end to
end. The apply-and-provision path one level up (`kubectl apply -f
grain-granule.yaml`, the rollout wait, `ProvisionOverAPI`) is *not*
independently proven against a real cluster the way `k8s-container`'s
was, for a reason worth stating plainly rather than letting a green
preflight imply more than it does: that path is close to line-for-line
the same code `k8s-container`'s own `pkg/hostinit/k8s.go` already runs
and which `kind`-cluster testing already validated there, so the
marginal risk in this platform's own version of it is low -- but
"close to the same code, reasoned about" is a different, weaker claim
than "run for real," and a real attempt did surface one thing worth
recording for whoever next tries it: the published
`ghcr.io/bwsalmon/grain/grain:main` image a fresh `grain-granule.yaml`
pulls could not be pulled from inside this sandbox's own `kind` node
(`tls: failed to verify certificate: x509: certificate signed by
unknown authority` -- the sandbox's own egress proxy, not a cluster or
manifest problem; a plain `docker pull` on the same host succeeded),
and a build of this repository's own Dockerfile from source was out of
this task's time budget to stand up only to re-prove a path the
`k8s-container` effort already proved for the code it shares. Treat
the apply/rollout/provision path as inherited confidence from
`k8s-container`, not as independently re-validated here.

**Three open questions, settled by what got built, not by further
design:**

- **§4 (image publish target)** turned out to already be settled, just
  not noticed: CI has published the packed guest per-commit
  (`guest:sha-<commit>`, `guest:inputs-<hash>`) all along, the same
  artifact `-platform=linux-kvm`'s own `-sandbox-image` pulls. `grain
  init`'s own `-k8s-granule-image` flag now defaults to it
  (`defaultSandboxImage`, stamped the way `-k8s-sandbox-image`'s own
  default is). What is still a literal `CHANGEME` is
  `deploy/kubernetes/grain-granule.yaml`'s own `-k8s-granule-image` --
  the *daemon's* flag, read for the lifetime of the deployment to
  create ongoing sandbox pods, not `grain init`'s one-shot preflight --
  because that still wants a moving tag (`guest:main` or similar) and
  none is published; a per-commit tag edited into a manifest by hand
  would go stale the moment the next commit lands. That gap is
  unchanged by this.
- **Open question 2 (the node-selector flag)** is settled as
  `-k8s-node-selector`, one `"key=value"` pair, repeatable, with no
  discovery -- and, specifically, the *same* flag name
  `-platform=k8s-container` already uses for its own
  `K8sConfig.NodeSelector`, reused rather than given a
  `-k8s-granule-`-prefixed name of its own. The two platforms' pods
  never coexist in one cluster (docs/k8s-granule-design.md's own
  argument, §1), so there is no ambiguity in sharing the name, and a
  shared name is one less flag for an operator who runs both platforms
  against different clusters at different times to keep straight. The
  same reuse applies to `-k8s-namespace`, `-k8s-deployment`,
  `-k8s-service`, `-k8s-context`, `-k8s-kubeconfig` and
  `-k8s-manage-port-forward` at both `grain init`'s own flag layer and
  `cmd/grain daemon`'s: only the image flag (`-k8s-granule-image`,
  since the two platforms' images are not interchangeable at all) and
  the backend selector itself (`-k8s-granule-sandboxes`) needed new
  names.
- **§5's "exact new `K8sConfig`-adjacent struct"** is
  `orchestrator.K8sGranuleConfig`, documented in
  `pkg/orchestrator/k8s_granule_sandboxes.go` beside the
  `K8sGranuleSandboxes` it configures.

**One place this document's own §5 was not quite right.** §5 says
"`pkg/k8s.Client` reused unchanged." The *methods* are unchanged in
signature -- `Create`, `Delete`, `WaitReady`, `ListSandboxes` all take
the same arguments and mean the same thing for either platform's pods,
exactly as predicted. What is not unchanged is `PodOptions` and
`Manifest`, the values those methods are handed: this platform needed
three fields neither had before --

- `HostDevices`, for the `/dev/kvm` hostPath volume and mount §2's
  manifest needs and no sandbox pod ever did;
- `ReadinessExec`, because a sandbox pod's `Ready` condition meaning
  "the container started" is exactly right for `k8s-container` and
  exactly wrong here -- without a `readinessProbe`, a granule pod
  reports `Ready` the instant its container starts, which is to say
  before cloud-hypervisor has booted anything, which would have made
  §2's own nested-virt check meaningless (`kubectl wait
  --for=condition=Ready` would return instantly whether or not the VM
  ever came up); and
- `NoCommand`, found only by reading `pkg/k8s.Manifest`'s existing
  code rather than by reasoning about the pod spec in the abstract:
  `PodOptions.Command`, empty, has always defaulted to
  `DefaultCommand` (a shell that sits still waiting to be exec'd
  into) -- exactly right for a sandbox pod's own container, and
  exactly wrong for a granule pod, whose one container *is* the thing
  to run. Kubernetes' pod-spec `command` field overrides a container's
  `ENTRYPOINT`, not its `CMD` -- `gke-pod-example.yaml`'s own `args:
  ["run"]` deliberately uses the *other* override, leaving
  `granule-shim`'s `ENTRYPOINT` in place -- so handing granule's own
  image `DefaultCommand` as `command` would not add a sidecar
  behaviour, it would replace the VM boot with an idle shell, silently.
  `NoCommand` omits the `command` key entirely, the one shape
  `PodOptions` had no way to ask for before.

So: `pkg/k8s.Client`'s own exported methods, unchanged; `PodOptions`
and `Manifest`, extended by three fields that exist for no reason
`k8s-container` has ever needed; a new, small `orchestrator.Sandboxes`
implementation, exactly as §5 predicted; and the granule-wrapped exec
transport (`wrapGranuleExec`, `["granule", "exec", "--"]` prefixed onto
every argv before it reaches the same `mcp.KubectlExecRunner`), exactly
the one-line difference in shape §5 described.

**Network isolation turned out stronger than `k8s-container`'s own,
not merely different.** `K8sConfig.NetworkPolicyEnforced` exists
because a plain sandbox pod's "no network" is a promise only a
NetworkPolicy object can keep, and only on a cluster whose CNI actually
enforces one -- an operator's assertion grain cannot check from inside
a pod. A granule guest's "no network" is `CHV_NET_MODE=none`, a
property of the VM itself: the guest has no network device at all,
regardless of what the surrounding pod or the cluster's CNI can reach.
`K8sGranuleSandboxes.IsolatesNetwork()` returns `true`
unconditionally -- there is no cluster-dependent flag to assert here,
because there is nothing for a cluster to get wrong.

**What was deliberately left out, named rather than silently
skipped:**

- **Self-upgrade.** `cmd/grain daemon`'s own `checkConfig` refuses
  `-upgrade-image` together with `-k8s-granule-sandboxes` outright,
  with a message naming why: `k8s-container`'s own upgrade path
  health-checks a candidate image with a plain, unprivileged throwaway
  pod, and this platform's pods are never that -- a real health check
  here would need its own privileged, `/dev/kvm`-mounted variant, which
  is new design and new code this task did not build. An operator
  upgrades this Deployment's image by hand
  (`kubectl set image deployment/<name> <container>=<image>`) until
  that exists.
- **The Sandbox health pane.** `K8sSandboxes` answers `Health`,
  `SandboxCount` and `AgentSandboxCount`; `K8sGranuleSandboxes` does
  not yet. Dispatch, tool calls, git credentials and capability
  placement all work without them -- they are what the UI's own
  Sandboxes pane reads to show load, memory and disk per live sandbox,
  not anything the dispatcher itself needs -- but an operator running
  this platform today sees no per-sandbox figures there. Worth a
  follow-up task of its own, not a blocker for this one.
- **Live backend switching in Settings.** `model.SandboxBackendKubernetesGranule`
  is a real value now, which Settings' own generic backend picker lists
  for any deployment started with every `-k8s-granule-*` flag it needs
  -- but switching *to* it from a running process would still try to
  construct a `K8sGranuleSandboxes` this process was never given the
  plumbing for, the same restart-required shape every other backend
  switch already has (`sandboxBackendAvailable`'s own doc comment:
  "after a restart").

## What the first real-GKE run found

`grain init -platform=k8s-granule` against a real GKE cluster (1 node,
`n2-standard-8`, `UBUNTU_CONTAINERD`, `--enable-nested-virtualization`)
surfaced three rough edges the `kind`-cluster pass above never would
have, all since fixed:

- **§1's refusal was reported as `StepRefused`, which this document's
  own wording ("a `StepManual` with the exact command") never meant --
  and `StepRefused.Command` is never printed at all** (`cmd/grain`'s
  `printInitResult` only walks `Command` for `StepManual`), so an
  operator saw the refusal's prose but never the command to fix it.
  `checkK8sGranuleNamespacePodSecurity` now reports `StepManual` in
  both branches, exactly as this document always said it should.
- **The nested-virt check's own failure advice named a pod it had
  already deleted.** `kubectl -n grain describe pod
  grain-init-preflight-granule` cannot work once `checkK8sGranuleNestedVirt`'s
  own `defer cleanup()` has run, and it always has by the time an
  operator reads the message. The check now captures phase, container
  state, recent events and recent log output *before* cleanup deletes
  the pod, and folds that into `Detail` directly -- the operator should
  not have to go looking themselves. The same failure path had an
  unrelated bug worth naming: the old message's `(%s)` reason
  parenthetical read `()` whenever the container's state was not
  `waiting` (e.g. a container that started and exited), which the new
  diagnosis text replaces outright.
- **The flag's own name invited the one wrong guess an operator
  reaches for first.** `-k8s-granule-image` and this section's own
  prose called the thing it wants "the granule runtime image" --
  which is also, correctly, the established name for
  `GranuleRuntimeImage`/`DefaultGranuleRuntimeImage` ("granule:vendored"),
  a real but *different* and non-bootable artifact. A pod running
  `granule:vendored` here fails with `granule: disk[0]
  /var/lib/granule/guest/disk.img: no such file or directory` and exits
  immediately -- which the preflight, unable to tell a pull failure
  from a boot failure from outside the pod (this section's own
  argument above), reported as a nested-virt failure rather than a
  wrong-image one. This document, `K8sGranuleOptions.Image`'s own doc
  comment and every `-k8s-granule-image` flag help string now call the
  wanted artifact a **packed guest image** instead, name
  `GranuleRuntimeImage`/"granule:vendored" explicitly as the wrong
  guess, and the check now tells the two failure modes apart where it
  can: a container whose state is `terminated` (exited, rather than
  still running and merely never answering its readinessProbe) gets a
  message pointing at `-k8s-granule-image` first, not the nested-virt
  checklist.

Still true after this pass: §4's own publish-target gap. The full
`k8s-granule` boot -- `kubectl apply -f grain-granule.yaml`, the
rollout, `ProvisionOverAPI` -- was not reached on the real cluster
either, because reaching it needs exactly the artifact §4 says does
not exist yet: a packed guest pushed somewhere this cluster can pull
from, at the scale (~8GiB) that implies. Finding this flag's own
rough edges is, if anything, the evidence that stood in for actually
clearing that bar.

## `-k8s-granule-image` defaults to the guest CI already publishes

The "does not exist yet" above was wrong about availability, not about
a stable tag: `.github/workflows/build-artifacts.yml`'s `sandbox-guest`
job has published the packed guest per-commit all along
(`guest:sha-<short-commit>`, and `guest:inputs-<hash>` for a build
whose guest inputs did not change), the same artifact
`-platform=linux-kvm`'s own `-sandbox-image` names. `grain init`'s own
`-k8s-granule-image` flag now defaults to it --
`cmd/grain`'s `defaultSandboxImage`, stamped to that same-commit tag at
build time exactly the way `-k8s-sandbox-image` defaults to
`defaultK8sSandboxImage` -- and `K8sGranuleOptions.Image`'s own
`Options.validate` no longer refuses an empty one, since the flag
layer never hands it one. An operator still names their own image
explicitly to override it (a team layer, a different base); a default
that cannot be pulled fails at the preflight's own throwaway pod, as a
pull problem, the same as any other wrong `-k8s-granule-image` --
`checkK8sGranuleNestedVirt`'s existing `terminated`-state diagnosis
(added the pass before this one, above) already tells that apart from
a genuine nested-virt failure.

What this does not settle: `deploy/kubernetes/grain-granule.yaml`'s own
`-k8s-granule-image` -- the *daemon's* flag, naming the image its
orchestrator pulls for every sandbox pod it creates over the
deployment's life, not `grain init`'s one-shot preflight -- still ships
`CHANGEME`, left alone here. CI does in fact move `guest:main` (the
`publish-grain-image` job's `retag` of the guest alongside the grain
image's own moving tag, gated on `tests/container`/`tests/installer`
passing), the same shape `grain.yaml`'s own
`-k8s-sandbox-image=ghcr.io/bwsalmon/grain/k8s-sandbox:main` already
uses -- so whether `grain-granule.yaml` should name `guest:main` the
same way, rather than `CHANGEME`, is a real question this change does
not answer; it is the stable-tag question this document's §4 already
left open, now narrower than "none exists" but still a separate
decision from `grain init`'s own default above.

## Migrating a local install onto this platform

`scripts/migrate-local-to-gke.sh` moves an existing single-host
deployment onto this platform. It does not apply
`grain-granule.yaml` as shipped. It renders the file first, filling in
the three CHANGEMEs from the local daemon's own flags, and changing two
more things:

- **`replicas: 0` on first apply.** The controller only starts once the
  local data directory has been copied into `grain-data` through a uid
  1000 transfer pod. A controller that came up first would create an
  empty store on the PVC and start its own installation, and the copy
  would then land on top of a live daemon.
- **A Secret for the off-cluster processes.** `Secret/grain-migration`
  is mounted read-only at `/etc/grain-secrets`, and its `hostd-token` is
  passed as `-hostd-token-file`, `-executor-token-file` and
  `-executor-addr=:4243`. The host's own `grain hostd -server` and
  `grain model-server -server` both dial in with that one token. That
  is how `run: host` capability servers and ambient CLIs such as `agy`
  keep working once the controller is in the cluster.
  `pkg/daemon.Config.HostdRemote` (comment on that field) describes the
  hostd side of it.

The namespace label and the device plugin are checked the way §1 and
§2 describe. The script creates and labels the namespace the same way
`gke-sandboxes`' RBAC manifest does, and refuses a cluster with no
`granule-device-plugin` rather than installing one. That install is
`grain init -platform=gke-sandboxes`'s job, and `--provision` runs it.
[grain-init-platforms.md](grain-init-platforms.md#migrating-a-local-install-to-gke)
lists the stages, the rollback and how it is tested.
