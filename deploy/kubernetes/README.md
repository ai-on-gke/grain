# Running grain in a Kubernetes cluster

grain's original deployment shape is one machine: a `docker run` of the
published image, driven by `scripts/setup.sh`, with each task's sandbox a
granule-managed VM on that same host. That is still supported and still
the right answer for a machine one operator owns.

This directory is the other shape. grain runs as a Deployment, each
task's sandbox is a pod created and destroyed with the run, and upgrades
are a rollout rather than a swapped binary. Nothing about how tasks are
filed, dispatched, reviewed or merged changes — what changes is where a
sandbox comes from and what the deployment sits on.

Everything below -- the three prerequisites, the apply, and the
provisioning steps -- is also what `grain init -platform=k8s-container`
checks and performs for you against a cluster `kubectl` already reaches
(see [docs/k8s-init-design.md](../../docs/k8s-init-design.md)). This
document is still the thing to read first: it says what each step means
and why, which `grain init` only enforces. A second Kubernetes shape,
every sandbox a real VM rather than a plain pod, is its own section
near the end of this document -- ["`k8s-granule`: a real VM per sandbox
instead"](#k8s-granule-a-real-vm-per-sandbox-instead).

## What it costs

Say the trade out loud before installing anything, because it is not the
one the single-node deployment makes.

A sandbox here is a **container**, so a run shares the node's kernel with
every other pod scheduled there. A granule sandbox is a VM and does not.
Against a task's own agent that is a real difference: kernel-level escape
from a container is a shorter path than escape from a hypervisor.
Against the thing grain's isolation is mostly for — one task not seeing
the next task's checkout, credentials or leftover processes — it is the
same guarantee, and it comes from the same place: the sandbox is created
for one run and deleted with it.

What you get for it is that the scheduling, the sizing, the placement and
the teardown are the cluster's own primitives. Sandboxes spread across
nodes instead of contending for one machine, a node that dies takes its
sandboxes and nothing else, and the deployment's capacity is the
cluster's rather than a number an operator sized by hand.

## Before you apply anything

Three things have to exist first.

**A sandbox image.** CI publishes `ghcr.io/bwsalmon/grain/k8s-sandbox`
with all three supported agent CLIs (`claude`, `agy`, and `codex`), `git`,
`bash`, `curl`, and a non-root user with a writable home. Each grain build
has its same-commit image stamped in as the `-k8s-sandbox-image` default;
the manifest below names the branch tag explicitly. The shipped image
deliberately has no Docker or kind, so ordinary deployments do not need
privileged sandbox pods. A deployment that needs nested containers must
build its own image and opt into `-k8s-sandbox-privileged` knowingly —
and relax the `grain` Namespace's `pod-security.kubernetes.io/enforce`
label from `baseline` to `privileged` with it, since baseline Pod
Security admission refuses a privileged sandbox pod. That label is what
stops the daemon's own ServiceAccount, which can create pods, from
creating a privileged or host-mounting one; see the comment above the
Namespace in `grain.yaml`.

**A CNI that enforces NetworkPolicy, if you want `network: none` tasks.**
See `networkpolicy.yaml`, which says what to check and in what order.
Without it, a task asking for no network access is refused rather than
silently given a networked pod.

**A storage class that gives ReadWriteOnce volumes.** grain's state is an
embedded SQLite database on a filesystem, with one writer.

## The daemon does not run as root

`grain.yaml` runs the pod as uid/gid **1000**, with `fsGroup: 1000` on the
`grain-data` volume and `HOME=/data/home`. The reasoning is beside the
`securityContext` in that file; the short version is that the agent CLIs a
dispatch runs — `claude`, `agy`, `codex` — run in the daemon's own
container rather than in the sandbox, so as root they could rewrite the
pinned binaries the image was built with, and one of them was doing
exactly that.

Two consequences, and the first bites deployments that already exist.

**Upgrading a deployment that ran as root.** Everything under
`grain-data` — the store, the secrets database, the checkouts, the
transcripts — is owned by uid 0 on a volume written by an older
`grain.yaml`. On most CSI drivers the kubelet repairs that by itself:
`fsGroup` is applied to the volume recursively at mount time, so the new
pod's uid reaches its own files through the group. On a driver that does
not implement `fsGroup` (several NFS and CIFS drivers, anything mounted
with `fsGroupPolicy: None`) it does not, and the init container fails on
`mkdir /data/home` before the daemon starts — deliberately, because the
alternative is a daemon that comes up and cannot write its store.

Either way the one-off fix is a `chown`, and taking it before the apply
costs nothing on a driver that did not need it:

```sh
# 1. Stop the daemon. ReadWriteOnce means nothing else can mount the
#    volume while the current pod holds it, and a Recreate rollout is
#    already an outage.
kubectl -n grain scale deployment/grain --replicas=0
kubectl -n grain rollout status deployment/grain --timeout=2m

# 2. Hand the volume to the uid grain.yaml now names.
kubectl -n grain apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: grain-data-chown
spec:
  backoffLimit: 1
  template:
    spec:
      restartPolicy: Never
      securityContext:
        runAsUser: 0
      containers:
        - name: chown
          image: ghcr.io/bwsalmon/grain/grain:main
          command: ["/bin/sh", "-c", "chown -R 1000:1000 /data"]
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: grain-data
EOF
kubectl -n grain wait --for=condition=complete job/grain-data-chown --timeout=30m

# 3. Apply the new manifest and let the rollout bring the daemon back.
kubectl apply -f deploy/kubernetes/grain.yaml
kubectl -n grain rollout status deployment/grain --timeout=10m
kubectl -n grain delete job grain-data-chown
```

`chown -R` over a volume holding many repository checkouts is minutes,
not seconds; the `wait` above allows for that. A run in flight when the
replica count goes to zero is killed and recovered as a failed attempt,
the same as one killed by an upgrade.

**Changing the uid.** A cluster that assigns uids of its own — OpenShift
SCCs are the usual case — should replace all three numbers
(`runAsUser`, `runAsGroup`, `fsGroup`) with its own and run the `chown`
above against that uid. Nothing in the image knows the number: there is
no passwd entry for it, and none is needed. What must not change is that
`fsGroup` can reach the volume and that `HOME` points somewhere on it.

Sandbox pods are unaffected by any of this. They run as whatever their
own image declares — uid 1000 `agent` in the shipped one, which is what
`-k8s-workspace=/home/agent` names — and they share no filesystem with
the daemon, only `kubectl exec`.

## Installing

```sh
# Edit the three CHANGEME values in grain.yaml first -- the image, the
# default target repo, and the cosign.pub the Upgrade button verifies
# images against (docs/image-signing.md). Upgrading a
# deployment that predates the securityContext? Take the chown above
# before this apply.
kubectl apply -f deploy/kubernetes/grain.yaml

# Optional, and only with the checks in its header done:
kubectl apply -f deploy/kubernetes/networkpolicy.yaml

# The UI has no authentication of its own. Tunnel to it rather than
# exposing the Service until something authenticates in front of it.
kubectl -n grain port-forward svc/grain 8080:80
```

Then open `http://127.0.0.1:8080`, and give the deployment a GitHub
credential on the Settings pane — the same first step a single-node
deployment takes.

## What is different in the UI

Several controls in the System overlay are reported as unavailable, and
that is deliberate rather than unfinished. Each one assumes a machine
this deployment owns, and under Kubernetes there is no such machine:

- **Reboot host** and **Shut down host** would take out a node running
  other people's workloads.
- **Restart grain** is the Deployment controller's job. `kubectl -n grain
  rollout restart deployment/grain` is the same action, done by the thing
  that owns the pod's lifecycle.
- **Top** and the host's own load and disk figures would describe a node
  shared with workloads that have nothing to do with grain, labelled as
  though they were this deployment's. `kubectl top node` is the honest
  version.

For the same reason, **Settings → Models** does not offer **Host** as a
run location for any agent CLI. Kubernetes has no host executor and no
`grain hostd`. Every CLI (claude, codex, agy and custom commands) runs as
a subprocess in the daemon pod's own container, which is what **Auto**
and **Container** both mean here. `PUT /api/settings/cli-run` refuses
`host` with a 400. The daemon refuses to start with `-claude-run=host`,
`-codex-run=host`, `-agy-run=host` or `-hostd-socket`. A `host` stored by
an earlier install runs in the container instead: the daemon logs it once
and Settings shows a warning until it is changed
([design/design/cli-run-location.design.md](../../design/design/cli-run-location.design.md)).

The **Sandbox health** pane stays: those are pods this deployment really
does own. Two of its figures mean something slightly different here —
load average and memory come from the node's `/proc` rather than the
pod's cgroup, so they report the pressure a sandbox is contending with
rather than its own share of it.

## Upgrading

The Upgrade pane repoints this Deployment at the image CI published for
the branch you name, and Kubernetes rolls it out. There is no binary to
install and no container to restart, so the pipeline is shorter than the
host deployment's — but it is the same button, with the same branch
picker and the same progress log.

The rollout is a `Recreate`, not a rolling update: two grain processes
against one SQLite store is data loss, so the old pod is stopped before
the new one starts and the UI is briefly down. A run in flight when that
happens is killed and its sandbox pod is left behind; the next process
reaps it at startup, and the run itself is recovered as a failed attempt
the same way a killed single-node daemon's runs are.

Rolling back is `kubectl -n grain rollout undo deployment/grain`, which
is Kubernetes' own record of what was running before rather than
anything grain keeps.

One thing this path does less of than the host one, and it is worth
knowing: when the new build knows a different database schema, the rows
are still exported and the new build's first start still rebuilds the
store from them, but the *rehearsal* of that import is skipped. The
rehearsal works by handing the new build the export directory read-only
at the same path, which needs a shared filesystem that a pod in a cluster
does not have. So a bad import is found on the new pod's first start
rather than before the cut-over. Take a volume snapshot before upgrading
across a schema change if the deployment's history matters.

**The upgrade checks this Deployment's `args` against the image it is
about to roll out**, and the step is called `unit flags` in the progress
log. The args are the command line this deployment runs with, and you
applied them once: the button rewrites only the image in the same spec,
so a build that has dropped a flag meets an args list that still passes
it, and a rollback meets one written for something newer. The daemon
starts either way and ignores what it does not understand — it used to
exit instead, which on one replica with `Recreate` is a
`CrashLoopBackOff` and an outage — so the step is the place you find out.
It names the flags the new build would ignore and says to edit
`deployment/grain`'s args and re-apply; it never refuses the upgrade,
because the upgrade that removes a flag is exactly the one an already
applied Deployment needs. A build too old to answer the question reads as
"not checked" rather than as a disagreement. The same list, for the build
running now, is on the Sandboxes pane under Daemon.

**That same Daemon line reports how many times this pod has started**,
which is the other half of the failure the flag check exists for: a
build that exits on a flag it does not define takes the one replica into
`CrashLoopBackOff`, and a deployment that is restarting rather than
running looks healthy in every other figure on the pane, because every
one of them is taken by a process that is up. The count is the kubelet's
own `.status.containerStatuses[].restartCount` for this pod, read at
startup through the `pods get` the Role already grants, and the daemon
learns which pod it is from `GRAIN_POD_NAME` — `metadata.name` through
the downward API, beside the `GRAIN_POD_IP` the git proxy uses. A
Deployment without that variable reports no count and says why rather
than reporting zero.

It is not the same figure a host deployment reports, and the pane says
which one it has. A host counts starts of `grain-daemon.service` in the
journal over the last 30 minutes; this counts restarts since *this pod*
was created, with no window, so it reads "started 4 times since this pod
was created". Two consequences worth knowing: a pod that crash-looped
this morning and has been up since still reports those restarts, and the
count goes back to zero at every rollout, because a rollout is a new
pod. What it is good for is the question it was added for — is this
deployment restarting right now — where the number climbs while you
watch it.

Keeping the *sandbox* image in step is not part of the button either. On
the single-node deployment an upgrade pulls the guest image the new build
names, because both live on one host; here the sandbox image is a flag
this Deployment was started with, so moving it is an edit to
`grain.yaml` and an apply.

## Sandbox priorities: who gets the cluster when it is full

`grain.yaml` and `grain-granule.yaml` both define four cluster-scoped
PriorityClasses; grain sets one of the last three on every sandbox pod
it creates:

| PriorityClass       | value   | preemptionPolicy       | used by |
|---------------------|---------|------------------------|---------|
| `grain-daemon`      | 2000000 | `Never`                | the grain daemon's own pod (set in the Deployment) |
| `grain-interactive` | 1000000 | `PreemptLowerPriority` | runs of interactive tasks: Director sessions, chats |
| `grain-agent`       | 100000  | `Never`                | ordinary agent runs, base-branch repairs, persistent sandboxes |
| `grain-ci`          | 1000    | `Never`                | CI jobs |

None is `globalDefault`, so nothing else in the cluster changes
priority. They exist for a cluster with no room left. With every
sandbox pod at the same priority the scheduler has nothing it may
evict, so an interactive session a person is waiting on fails with
`0/3 nodes are available: 3 Insufficient cpu ... No preemption victims
found`. Now its pod preempts CI pods instead. The scheduler evicts the
lowest-priority pods that make enough room, so it takes CI pods before
agent runs' pods. Agent runs and CI jobs never preempt anything; they
wait for room as before. The daemon's own pod runs at `grain-daemon`,
above every sandbox class, so no sandbox can preempt grain itself.

**On a cluster shared with other workloads**, every pod below
`grain-interactive` is a candidate victim, and a pod with no
PriorityClass runs at 0 -- below `grain-ci` -- so other workloads at the
default priority are preempted before grain's own CI pods. Give those
workloads a PriorityClass above `grain-interactive`, or clear the
interactive class name in Settings, if that is not what you want.

A CI job whose pod was preempted (a `DisruptionTarget` condition, or the
pod gone mid-job) is retried once in a fresh sandbox as infrastructure,
not counted as a test failure. If it is preempted again it is reported
*errored*, which grain CI re-runs, and never *failure*.

The names are settings: **Settings → Sandbox** (or `grain settings
-k8s-priority-class-interactive/-agent/-ci`, or the daemon's flags of
the same names, which seed them), defaulting to the three above. An
empty name sets no priority class on that kind of pod. **Apply the
manifest before upgrading to a grain that sets them.** A pod naming a
PriorityClass the cluster does not have is refused by the API server,
and grain reports that as `the cluster has no PriorityClass named
"grain-ci" -- apply deploy/kubernetes/grain.yaml ...`. If you cannot
create cluster-scoped objects, clear the three names in Settings
instead, and drop `priorityClassName: grain-daemon` from the Deployment. The daemon itself needs no new RBAC: the classes come from the
manifest, not from grain.

## Operating notes

- **One deployment per namespace.** The sweep for sandboxes a dead
  process or a failed delete left behind -- run at startup and again on
  every reconcile tick -- deletes every grain sandbox pod in the
  namespace it is not itself holding, so two deployments sharing one
  namespace would reap each other's live runs within a tick.
- **Sandbox pods are named `grain-sandbox-<task>-<attempt>`**, so
  `kubectl -n grain get pods -l app.kubernetes.io/component=sandbox`
  lists exactly the runs in flight, and a pod's name says which attempt
  of which task it belongs to.
- **Sandboxes reach the git proxy and the model proxies through the
  `grain-sandbox` Service**, not through the daemon pod's own IP: a
  sandbox is handed those addresses once, when it is created, and a run
  can outlive the daemon pod that created it (run hosts, a rollout). The
  daemon resolves the Service at startup (`-k8s-sandbox-service`) and
  hands out its cluster IP, on the ports `-sandbox-port-base` fixes
  (9728 for the git proxy, 9744-9746 for the model proxies). Moving the
  block means moving the Service's ports with it. The package and Actions
  caches stay on the pod IP.
- **A sandbox that will not start** is almost always the image: `kubectl
  -n grain describe pod grain-sandbox-...` shows the pull failure, and
  grain reports the same reason on the failed attempt rather than only
  "the sandbox never became ready".
- **The daemon's own logs** are `kubectl -n grain logs deploy/grain`, and
  the same lines are in the UI's Logs tab — which reads them with that
  same command, through this pod's own ServiceAccount (the Role's
  `pods/log`). The tab offers two sources here rather than the four a
  host deployment shows: there is no journal in this image, no
  `grain-daemon.service` or `grain-config-sync.service` anywhere in the
  cluster, and `dmesg` would be the *node's* kernel log if an
  unprivileged container could read it. The daemon's log and the git
  proxy's audit log are the two that mean anything in a pod, and they are
  the two that are there. What a *crash-looping* daemon failed on is the
  previous container's log — `kubectl -n grain logs deploy/grain
  --previous`, which is what the System pane's Daemon line points at when
  it has counted restarts.
- **A shell in the daemon pod is not root.** `kubectl -n grain exec -it
  deploy/grain -- bash` lands as uid 1000 with `HOME=/data/home` — the
  daemon's own home, so an interactive `claude` or `agy` login there is
  the one the daemon then uses. `apt-get install` in that pod does not
  work, and that is the point: what the image carries is settled when the
  image is built. Add a second container to the pod, or build an image.
- **Warm headroom on an autoscaling cluster.** grain re-reads the
  allocatable CPU and memory of its Ready, schedulable sandbox nodes on
  every reconcile tick, so a node a cluster autoscaler adds grows the
  sandbox budget, and a node it removes shrinks it, without a restart (a
  failed or empty reading keeps the last one). A burst that outruns the
  nodes already up waits for a new one: a sandbox pod may stay
  Unschedulable for six minutes (`DefaultK8sUnschedulableTimeout`) and
  take ten to become Ready (`DefaultK8sReadyTimeout`), enough for GKE to
  boot and register a node and pull the image onto it. To not wait at
  all, apply `headroom.yaml`: low-priority (`grain-headroom`, value
  `-10`) pause pods, each about one sandbox in size, that hold spare
  nodes Ready and that any sandbox pod preempts at once. Edit its
  `nodeSelector` (`grain.dev/kvm: "true"`) to the pool
  `-k8s-node-selector` names and its requests to the sandbox shape, then
  size it with `kubectl -n grain scale deployment/grain-headroom
  --replicas=N`.

## `k8s-granule`: a real VM per sandbox instead

Everything above is `grain.yaml`: every sandbox is a plain pod, sharing
its node's kernel with everything else scheduled there. `grain-granule.yaml`
is the other Kubernetes shape — every sandbox is a privileged pod
booting a real `cloud-hypervisor` VM over `/dev/kvm`, the same isolation
the single-node deployment's granule VMs give, scheduled by the cluster
instead of run on one box. See
[docs/k8s-granule-design.md](../../docs/k8s-granule-design.md) for the
full design and
[docs/grain-init-platforms.md](../../docs/grain-init-platforms.md) for
how it sits next to the three other `grain init` platforms;
`grain init -platform=k8s-granule` checks and performs everything below
for you against a cluster `kubectl` already reaches, the same way
`-platform=k8s-container` does for `grain.yaml`.

**This needs a privileged namespace, and `grain init`/this README will
not relax one for you.** `grain.yaml`'s own `Namespace` object carries
`pod-security.kubernetes.io/enforce: baseline`, which refuses the
privileged, `/dev/kvm`-mounted pods this platform's sandboxes all are
— so `grain-granule.yaml` ships **no Namespace object at all**. Create
and label it yourself, once, before applying anything:

```sh
kubectl create namespace grain
kubectl label namespace grain pod-security.kubernetes.io/enforce=privileged --overwrite
```

Relaxing Pod Security admission is a cluster-wide decision that outlives
this one deployment — it bounds every pod anything ever creates in this
namespace, not only grain's own — which is too large a thing for a
bring-up command to perform quietly on an operator's behalf. Run the
same two commands before `grain init -platform=k8s-granule`, too; its
own preflight only ever reads this label back, never sets it.

**This needs nested virtualization on every node that schedules a
sandbox**, which is not something `grain.yaml`'s namespace needs at
all. `/dev/kvm` existing on a node is not enough to prove it: a VM on
an emulated CPU boots, just unusably slowly, so the only honest check
is scheduling the real image and watching it actually boot one in the
time real acceleration takes — which is exactly what `grain init`'s own
preflight does, and what an operator standing this up by hand should
do too before trusting anything further.

**`-k8s-granule-image` wants a packed guest, not a sandbox image and not
the granule runtime image either.** It names a self-contained image with
its own guest disk and kernel already baked in — built from
`third_party/granule`'s own `Dockerfile` (`docker build -t granule .` is
the whole recipe; see `third_party/granule/deploy/k8s/gke.md`) — not
`ghcr.io/bwsalmon/grain/k8s-sandbox`, which has no guest to boot at all,
and not `granule:vendored` either (`-granule-runtime-image`'s own
default, what `scripts/granule/build-granule-image.sh` produces): that
image carries no guest disk, so a pod running it exits immediately with
`granule: disk[0] /var/lib/granule/guest/disk.img: no such file or
directory` rather than booting — the mistake its own name invites, since
"the granule runtime image" is also, correctly, this *other* image's own
established name elsewhere in this repository. Nothing publishes a
packed guest image under a stable tag yet; `grain-granule.yaml` ships
with a `CHANGEME` there for exactly that reason.

One deployment per namespace applies here just as it does to
`grain.yaml` — never run both manifests in the same namespace, since a
namespace relaxed for `k8s-granule`'s sandboxes is a namespace
`k8s-container`'s own design argues must never exist.

## Controller outside the cluster (`gke-sandboxes`)

Both `grain.yaml` (`k8s-container`) and `grain-granule.yaml`
(`k8s-granule`) run the grain daemon *inside* the cluster as a
Deployment. In the hybrid shape (`grain init -platform=gke-sandboxes`),
the grain controller — the daemon, its SQLite or Postgres store, its
git proxy, and its package cache — stays on its existing host outside
the cluster, while sandbox VMs run as privileged `k8s-granule` pods on a
nested-virtualization GKE Standard cluster.

What differs when the controller is outside the cluster:

- **No in-cluster `Deployment` or `Service` for the daemon.** The cluster
  hosts only sandbox pods (`app=grain-sandbox`) in the `grain` namespace
  plus `daemonset/granule-device-plugin` in `kube-system`.
- **A self-contained least-privilege `/var/lib/grain/kubeconfig`.**
  Instead of mounting an in-cluster ServiceAccount token at
  `/var/run/secrets/kubernetes.io/serviceaccount`, the off-cluster
  daemon reads `/var/lib/grain/kubeconfig` (`daemon.DefaultKubeconfigPath`,
  used automatically whenever `-k8s-kubeconfig` is unset and that file
  exists). `grain init -platform=gke-sandboxes` creates the `grain`
  namespace (`pod-security.kubernetes.io/enforce=privileged`), creates a
  `grain` ServiceAccount + Role + RoleBinding scoped strictly to `pods`
  (`get`, `list`, `watch`, `create`, `delete`), `pods/exec` (`create`),
  and `pods/log` (`get`), defines the three sandbox `PriorityClasses`
  (`grain-interactive`, `grain-agent`, `grain-ci`), and writes a
  bearer-token kubeconfig for that ServiceAccount to
  `/var/lib/grain/kubeconfig`.
- **Host-to-pod port forwarding over `pods/exec`.** Because a sandbox pod
  in GKE cannot dial the off-cluster controller's loopback ports directly,
  `orchestrator.K8sGranuleSandboxes` starts `granule forward` tunnels over
  `kubectl exec -i` into each sandbox pod (`daemon.EffectiveK8sKubeconfig`),
  exposing the controller's git proxy, package cache, and model proxies on
  `127.0.0.1` inside the guest VM.
- **One-command setup and verification.** `grain init -platform=gke-sandboxes`
  creates or reuses the GKE Standard cluster and nested-virtualization
  node pool (`--enable-nested-virtualization`, e.g. `c4-standard-96-lssd`
  with `--disk-type=hyperdisk-balanced` or `n2-standard-*` with
  `--disk-type=pd-balanced`), installs
  `third_party/granule/deploy/k8s/kvm-device-plugin.yaml`, applies the
  `grain` namespace and least-privilege RBAC, writes
  `/var/lib/grain/kubeconfig`, and verifies the cluster with both
  `scripts/platform-verify/k8s-granule.sh` and a throwaway sandbox that
  clones through the git proxy and fetches through the package cache.
  Once verified, switch **[Settings → Sandboxes](/settings/sandboxes)**
  to `kubernetes-granule`.

## Running grain as a standalone GitHub App bot on GKE

To have a GKE-hosted grain deployment act under a dedicated `<slug>[bot]`
identity and react immediately to GitHub webhooks (PR state changes,
`@<slug>` mentions, review comments, check runs) without exposing `Service/grain`
outside Google Cloud IAP (what the bot's commands do, including `take over`, is in
[docs/github-bot.md](../../docs/github-bot.md)):

1. **Register the GitHub App and write its credential**:
   ```sh
   grain controller bootstrap-github-app \
     -name grain-bot \
     -webhook-url https://<your-domain>/webhooks/github \
     -git-credential grain-bot \
     -data-dir /path/to/data
   ```
   (Omitting `-installation-id` lets grain resolve each repository's
   installation ID automatically from the App's RS256 JWT across any
   organization or repository the App is installed on.)

2. **Deploy the companion webhook gateway (`github-webhook.yaml`)**:
   ```sh
   kubectl -n grain create secret generic grain-github-webhook \
     --from-literal=webhook-secret='<github-app-webhook-secret>' \
     --from-literal=relay-secret="$(openssl rand -hex 32)"
   kubectl apply -f deploy/kubernetes/github-webhook.yaml
   ```
   Route `/webhooks/github` on your GKE `Ingress` / Gateway to
   `Service/grain-webhook` (which uses a non-IAP `BackendConfig`), keeping
   `/*` routed to `Service/grain` behind IAP.

3. **Connect `Deployment/grain` to the webhook relay**:
   Add `-github-webhook-url=http://grain-webhook.grain.svc.cluster.local` to
   `Deployment/grain`'s args and mount `GITHUB_WEBHOOK_RELAY_SECRET` from the
   same `grain-github-webhook` Secret. The daemon long-polls `grain-webhook`
   and wakes its reconcile cycle within 1 second whenever an event arrives.
