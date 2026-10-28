# Serving a Kubernetes deployment from `grain init`

**Status: implemented** (`pkg/hostinit/k8s.go`), proven end to end
against a real `kind` cluster. See "What building this actually
settled" at the end of this document for where the implementation
follows this design exactly, where it diverges and why, and what the
real-cluster run found that no fake ever could have. The rest of this
document is the original proposal, left as written. `grain init` (`pkg/hostinit`)
takes a bare Linux host to a running deployment end to end
(docs/design.md and `pkg/hostinit`'s own doc comment cover that path in
full). This is a design for the third of `grain init`'s four deployment
shapes (see [grain-init-platforms.md](grain-init-platforms.md) for the
full table); it now has a settled name, `pkg/hostinit.PlatformK8sContainer`
(`-platform=k8s-container`), rather than the `-target=k8s` this document
originally argued against below -- the argument stands, only the flag
it was made in terms of has changed.

## Which "k8s" this is about

The repo has two things that could be called "running grain on
Kubernetes," and they want opposite preflights:

- **`deploy/kubernetes/`** runs the grain *daemon* as a Deployment, and
  each task's sandbox is an ordinary pod, sharing the node's kernel. Its
  own README is explicit that this must never be privileged: the
  `grain` Namespace's `pod-security.kubernetes.io/enforce: baseline`
  label is what stops the daemon's own ServiceAccount, which can create
  pods, from creating a privileged or host-mounting one.
- **`third_party/granule/deploy/k8s/gke.md`** runs granule's own VM
  isolation *inside* a pod, on a nested-virt-capable node
  (`--enable-nested-virtualization`, a real `/dev/kvm` in the pod). The
  kernel isolation is the same as the single-host deployment's; only the
  scheduling is the cluster's. It needs a privileged pod -- the thing the
  first bullet's preflight exists to prevent.

**This proposal is about the first one only.** A daemon-on-GKE-with-
granule-sandboxes target is a legitimate future thing but needs a
preflight that contradicts this one (nested virt + privileged pods
required vs. forbidden), and conflating them in one `-target=k8s` flag
would make neither preflight trustworthy. If that target is ever wanted,
it should be its own design, explicitly named (`-target=gke-granule` or
similar), not a variant of this one. *(That platform is now built:
`pkg/hostinit.PlatformK8sGranule`, `-platform=k8s-granule`
(`pkg/hostinit/k8s_granule.go`), kept separate from `k8s-container` for
exactly the reason above -- see docs/k8s-granule-design.md.)*

## The seam, made literal

Of `grain init`'s steps, the last four talk to nothing but the daemon's
own HTTP API: `ensureDaemonReachable`, `ensureTeamTargetRepos`,
`ensureTeamGitCredential` and `checkSandboxBudget`. Every one of them
makes zero `System.Run` calls -- only `HTTPGet`/`HTTPRequest` against
`opts.UIAddr`. They do not care whether the thing answering at
`UIAddr` is a systemd unit on a GCE box or a Deployment in a cluster.

This is no longer only an observation about the code -- it is now also
an exported function, `hostinit.ProvisionOverAPI(ctx, sys, opts, r,
attempted)`, added alongside this proposal
(`pkg/hostinit/steps.go`). `initFor` calls it for the host target it
already builds and starts a daemon for; a k8s-targeting command would
call the exact same function, unchanged, once it has a daemon answering
at some reachable `UIAddr`. There is nothing left to design in that
part: the reuse this proposal is about is this one function call.

## What replaces the host-bound steps

`grain init`'s other fifteen steps -- apt, the Go tarball, `go build`,
three `docker build`s, a docker network, `setcap`, a systemd unit -- are
all host-bound and have no k8s equivalent. In their place:

### Preflight (refuse rather than guess, same as today)

`deploy/kubernetes/README.md`'s "Before you apply anything" section
lists three prerequisites an operator currently checks by reading prose.
All three are machine-checkable, and this proposal reports all of them
together before anything is applied -- the same preflight shape
`pkg/hostinit/preflight.go` already uses for `make`/`node`/`npm`/`/dev/kvm`
on the host path:

1. **The sandbox image is pullable.** Concretely: create a throwaway pod
   whose only container runs `-k8s-sandbox-image` with a no-op command,
   wait for it to reach `Running` (or `ImagePullBackOff`, which is the
   failure), then delete it. This is the same "read the real thing
   rather than guess" instinct `ensureSandboxNetwork` applies to the
   docker gateway: a registry HEAD request can be right about
   authentication and wrong about a node-local pull secret or mirror.
2. **A NetworkPolicy-enforcing CNI, if the deployment will ever dispatch
   a `network: none` task.** Conditional, not universal -- a deployment
   that never asks for network isolation does not need this, and the
   README already says what happens without it: the task is refused
   rather than silently given a networked pod, which is safe but worth
   knowing about in advance rather than at first dispatch. Needs a flag
   or a read of the team's own config to know whether to check at all.
3. **The named StorageClass gives ReadWriteOnce volumes.** The hardest
   of the three to check honestly: a StorageClass does not declare
   access modes itself, a PVC's binding does. Proposed check: create a
   scratch PVC against the named class requesting RWO, wait for it to
   bind, then delete both it and the PV it bound (or rely on the
   class's own reclaim policy). Anything less than an actual bind is a
   guess the README's own wording ("a storage class offering
   ReadWriteOnce volumes") does not license.

### Apply (once, not a rollout)

I would not have `grain init` own the rollout. `deploy/kubernetes/README.md`
is explicit that upgrades are a rollout and restarts are the Deployment
controller's job (`kubectl -n grain rollout restart deployment/grain`) --
reimplementing `kubectl apply` / `rollout status` / `rollout undo` badly,
inside a command whose whole reason to exist on the host side is getting
a human past the parts that are tedious rather than the parts that are
risky, seems like the wrong trade. The proposed shape is: preflight,
`kubectl apply -f <manifest>` **once** (StepDone the first time,
StepSkipped if the Deployment already exists -- same get-or-create bar
every other step in this package holds itself to), then hand off to
`ProvisionOverAPI`. A second run of the command does not re-apply over a
deployment an operator may have since edited by hand; it goes straight
to preflight (cheap, re-checks nothing changed) and provisioning
(already idempotent).

This is the one call in this proposal I'd most want pushback on. The
case for owning more (apply *and* wait out the rollout *and* run the
`chown` migration when it's needed) is that it would make the k8s path
as genuinely one-command as the host path is today. The case against is
everything above.

### Reaching the API

The host path's `UIAddr` is loopback -- this process and the daemon run
on the same machine. A k8s deployment's API is not reachable that way
by default; the existing `-ui-addr` flag already means "where the
steps below reach the daemon back," and the proposed design keeps that
contract rather than growing a second one: the operator points
`-ui-addr` at whatever already reaches the Service (a `kubectl
port-forward` they started themselves, or an internal address this
process can reach). `grain init` does not start or manage a
port-forward subprocess -- same reasoning as not owning the rollout: a
backgrounded `kubectl port-forward` is a process this command would
have to supervise and does not control the lifecycle of.

### The uid-1000 migration

`deploy/kubernetes/README.md` already documents the `chown -R 1000:1000
/data` migration, for a deployment that predates the current
`securityContext`, in exactly the `[manual]` idiom `grain init` uses
elsewhere: a precise command reported rather than performed. Detecting
"this deployment needs it" (old image, root-owned volume) and printing
that block as a `StepManual` fits this package's existing vocabulary
with no new concept needed.

## Open questions this proposal is deliberately not settling

1. **Is `grain init` even the right home?** `grain setup gcp`
   (`pkg/gcpsetup`) is the existing precedent for a sibling subcommand
   rather than a mode of `init` -- and it is also meant to be re-run from
   `grain sync` to reconcile drift, a shape this k8s path might want too
   (an operator edits `grain.yaml` by hand and wants the same
   provisioning re-checked). Against that: the four steps being reused
   are `init`'s own tail, sharing `init`'s `Step`/`Result`/`StepStatus`
   vocabulary, its `-dry-run`, and its exit-code contract (0 / 1 / 10)
   that any scripting around it already depends on. My own lean is
   `grain init -target=k8s` over a new `grain setup k8s`, because the
   output a caller has to parse is identical either way and there's
   genuinely nothing in the k8s path that behaves more like
   `gcpsetup`'s IAM-bootstrap-with-no-daemon-at-the-end shape than like
   `init`'s own. Worth arguing about before building either.
2. **What flag surface names the manifest, the image, the StorageClass,
   whether `network: none` needs checking, and the kubeconfig
   context/namespace?** Sketched above only as "the named X" -- real
   flags are implementation, not design, but whoever builds this should
   decide whether they mirror `deploy/kubernetes/grain.yaml`'s own flags
   (`-k8s-sandbox-image`, `-k8s-workspace`, `-k8s-sandbox-privileged`) or
   invent `grain init`-specific ones.
3. **Where does the admin-password file (`ensureAdminAccount`'s
   `DataDir/admin-password`) land for a k8s caller?** On the host path,
   `DataDir` is the daemon's own `-data-dir`, readable by the same
   operator who ran `grain init` because it's the same machine. For k8s
   there is no such shared filesystem -- the password has to land
   somewhere on the *operator's* machine (cwd, `~/.grain/<context>/`,
   something flag-named), which means `Options.DataDir` cannot keep
   meaning "the daemon's data directory" for this target. This is a
   real incompatibility in the `Options` struct `ProvisionOverAPI`
   takes, not a cosmetic one, and needs a decision before the function
   can be called from outside `pkg/hostinit` with a straight face.

## Explicitly out of scope here

Implementation of any of the above, the GKE-nested-virt-granule target,
and the exact preflight commands beyond the shape described. The
single-host path is the one that matters most right now; this is filed
so the cluster design can be argued about in the background rather than
forgotten.

## What building this actually settled

The three preflight prerequisites, the apply-once shape, and
`ProvisionOverAPI` unchanged are all exactly as designed above --
`pkg/hostinit/k8s.go` implements them more or less literally, each
check creating the real throwaway object this document proposed
(a pod for the image, a PVC for the storage class, a pod under a
deny-all-egress policy for NetworkPolicy enforcement) rather than
reading a proxy for it. One addition the design did not anticipate:
the storage-class check pairs its scratch PVC with a pod that mounts
it, not a bare PVC. A bare PVC never binds against a StorageClass
whose `volumeBindingMode` is `WaitForFirstConsumer` -- the default for
`kind`'s own class, and for several real cloud provisioners (EBS
gp2/gp3 among them) -- so the check as originally proposed would have
refused exactly the clusters most likely to run this platform. Found
by actually running it against a `kind` cluster, not by reasoning
about the design.

**The one deliberate divergence** is "Reaching the API." This document
argues grain init should never start or manage a port-forward
subprocess, the same way it does not own the rollout, and should
instead expect an operator to point `-ui-addr` at a route they already
have. The task that built this platform asked for the opposite:
`-platform=k8s-container` starts its own `kubectl port-forward
svc/grain <port>:80`, scoped to the lifetime of one `grain init`
invocation -- started immediately before `ProvisionOverAPI` and
stopped immediately after, never left running once the process exits.
`K8sOptions.ManagePortForward=false` (`-k8s-manage-port-forward=false`
on the command line) restores this document's original behavior
exactly: grain init assumes whatever `-ui-addr` names is already
reachable and starts nothing of its own. See `pkg/hostinit/k8s.go`'s
own top-of-file comment for the reasoning in full; the short version is
that without a default of some kind, provisioning a `k8s-container`
deployment would need a manual step on every single run, not only the
first one, which the three `ProvisionOverAPI` steps this whole proposal
reuses were never written to expect.

**Open question 3** (where `Options.DataDir` points for a caller with
no shared filesystem) is resolved pragmatically rather than redesigned:
`DataDir` keeps its one meaning in the `Options` struct, but for
`-platform=k8s-container` it names a directory on the *operator's* own
machine -- where `ensureAdminAccount` writes `admin-password`, and
where this platform's preflight writes its own scratch manifests
(`<DataDir>/k8s-preflight/*.yaml`) so a test (or a human re-reading a
log) can tell `kubectl apply -f <path>` is the same path on every run
against the same `DataDir`. **Open question 1** (is `grain init` the
right home) and **open question 2** (the exact flag surface) are
answered simply by what got built: `grain init -platform=k8s-container`
with a `-k8s-*` flag per `K8sOptions` field, listed in `grain init
-help`.

**Found only by running it for real**, twice, independently of
anything this design anticipated:

- `deploy/kubernetes/grain.yaml`'s own `securityContext` was broken --
  every pod failed `[FATAL tini (12)] exec /usr/local/bin/grain failed:
  Operation not permitted`. The Dockerfile `setcap`s
  `/usr/local/bin/grain` with `cap_net_bind_service=+ep`; the manifest
  dropped every capability without adding that one back, and a kernel
  refuses to exec a binary carrying a file capability its container's
  bounding set cannot satisfy -- not a missing feature, a pod that
  never starts. Fixed alongside this platform (`add: ["NET_BIND_SERVICE"]`
  beside the existing `drop: ["ALL"]`), with a test
  (`tests/deploy/kubernetes_manifest_test.go`) that would have caught
  it, because nothing that already existed in this repository's test
  suite starts the container the manifest describes.
- `kubectl auth can-i create namespaces` -- the permission check for
  the one cluster-scoped object `grain.yaml` creates -- writes a
  `Warning: resource 'namespaces' is not namespace scoped` line to
  stderr ahead of its actual `yes`/`no` verdict on stdout.
  `hostinit.System.Run` returns stdout and stderr merged (its own doc
  comment says so), so an exact-equality comparison against the whole
  output read that warning-plus-verdict string as "no" on a cluster
  that had actually said "yes". Fixed by comparing only kubectl's last
  line (`lastLine`), the same fix in spirit as the two bugs this
  platform's own design doc cites as precedent (a docker error string,
  `exec.LookPath` resolving against the wrong PATH) -- invisible to
  every fake, caught only by a real `kubectl` against a real cluster.
- The preflight's own scratch objects (the throwaway pod, PVC and
  NetworkPolicy each check creates and deletes) used a non-blocking
  delete (`--wait=false`) for cleanup. A pod's default
  `terminationGracePeriodSeconds` is 30s, so a second `grain init` run
  within that window raced the first run's own cleanup: `kubectl apply`
  patched the still-terminating object rather than creating a fresh
  one, and the object disappearing mid-`kubectl wait` (the first run's
  delete finally completing) read as a bare "NotFound" rather than a
  timeout -- a storage class that works perfectly reported as refused.
  Fixed by waiting for each scratch object's deletion to actually
  finish before the check returns, plus a delete-before-create at the
  start of each check so a run killed by something less polite than
  its own cleanup (an operator's Ctrl-C, a lost SSH session) cannot
  wedge every later one either.

**Found by the 10-04 walkthrough** against a fresh `kind` cluster
(`docs/test-runs/2026-10-04-grain-init-k8s-container.md`), all fixed:

- `-admin-account` defaulted to the user running `grain init`, but the
  pod's daemon runs as the image's uid-1000 `grain` account and seeds
  that as its only admin. Init exited 0 having given a password to an
  ordinary member account. It now defaults to `grain` on both
  Kubernetes platforms.
- `-github-token-file` was silently dropped: the host credential ladder
  writes under `-data-dir`, which here is not the daemon's. The token
  now goes to the default team as its github.com git connection (`POST
  /api/teams/default/git-connections`; it was `PUT .../github-credential`
  before that route was retired, grain/task-3929): one more API-only step, run
  after `ProvisionOverAPI`'s five while the port-forward is still up.
- An unpullable sandbox image was also reported as a storage class
  without ReadWriteOnce, because the storage check's consuming pod runs
  the same image. A bound PVC is now reported as such.
- Scratch-pod deletes now pass `--grace-period=1`. `sleep` ignores
  SIGTERM as PID 1, so each delete sat out the full 30s, and that was
  most of an idempotent re-run's time (72s, now about 15s).

## Controller outside the cluster (`grain init -platform=gke-sandboxes`)

`PlatformK8sContainer` (`-platform=k8s-container`) and
`PlatformK8sGranule` (`-platform=k8s-granule`) both assume the grain
controller itself runs as a `Deployment` inside the cluster (`grain.yaml`
or `grain-granule.yaml`). The Stage 1 off-cluster sandbox architecture
(`PlatformGKESandboxes`, `-platform=gke-sandboxes`, implemented in
`pkg/hostinit/gke_sandboxes.go`) serves the hybrid deployment where the
controller stays on its existing host outside the cluster and dispatches
`kubernetes-granule` sandbox pods into a dedicated GKE Standard cluster:

1. **Cluster and nested-virtualization node pool (`gcloud`)**:
   Idempotently creates or reuses a GKE Standard cluster and a node pool
   with `--enable-nested-virtualization` and `UBUNTU_CONTAINERD` (for
   example `c4-standard-96-lssd` with `--disk-type=hyperdisk-balanced`,
   or `n2-standard-*` with `--disk-type=pd-balanced`).
2. **Granule KVM device plugin (`kubectl`)**:
   Applies `third_party/granule/deploy/k8s/kvm-device-plugin.yaml` and
   waits for `daemonset/granule-device-plugin` in `kube-system` so nodes
   advertise `granule.io/kvm`.
3. **Privileged `grain` namespace and least-privilege RBAC**:
   Creates the `grain` Namespace labelled with
   `pod-security.kubernetes.io/enforce=privileged` and a `grain`
   ServiceAccount + Role/RoleBinding granting only `pods` (`get`, `list`,
   `watch`, `create`, `delete`), `pods/exec` (`create`), and `pods/log`
   (`get`).
4. **`/var/lib/grain/kubeconfig`**:
   Mints a bearer token for the `grain` ServiceAccount and writes a
   self-contained kubeconfig to `/var/lib/grain/kubeconfig`
   (`daemon.DefaultKubeconfigPath`), which the off-cluster daemon uses
   automatically for both pod lifecycle and `granule forward` tunnels.
5. **End-to-end verification**:
   Runs `scripts/platform-verify/k8s-granule.sh --keep` and boots one
   throwaway sandbox pod that clones through the git proxy and fetches
   through the package cache before cleaning up.

