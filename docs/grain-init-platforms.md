# `grain init`'s platforms

[`grain init`](grain-init.md) brings up one of four deployment shapes,
named by `-platform` (`pkg/hostinit.Platform`). All four share the same
process -- preflight, then build/configure, then
[`ProvisionOverAPI`](grain-init.md) once something answers at
`-ui-addr` -- but differ in what host or cluster they target, which
`pkg/orchestrator.Sandboxes` implementation they end up running, and
what each needs before `grain init` can bring it up at all.

All four are implemented. The first two rows share almost every step --
preflight, docker, Go, the sandbox network and its gateway, the grain
binary, `CAP_NET_BIND_SERVICE`, the claude CLI, the GitHub credential and its connections,
`ProvisionOverAPI` -- differing only where the sandbox backend
genuinely does: no `/dev/kvm` check, no granule CLI, no three guest
images, and a different flag (`-container-git-proxy-host` instead of
`-granule-git-proxy-host`) naming the same gateway address to the
daemon. The two Kubernetes rows share almost none of that with those
two, or with *each other*: neither touches docker, Go or the claude CLI
at all, since the daemon each brings up is a pod built by CI, not a
process this host builds -- each preflight reads a cluster instead of a
host, and the rest of each path is `kubectl apply` once, a rollout
wait, and the same `ProvisionOverAPI`. What the two Kubernetes rows do
not share with each other is the preflight and the manifest itself --
see "Why `k8s-container` and `k8s-granule` are not one flag" below for
why that is by design, not an opportunity to converge later.

| `-platform` | sandbox backend | isolation | needs | gives up | status |
|---|---|---|---|---|---|
| `linux-kvm` | `orchestrator.GranuleSandboxes` | VM, own kernel (cloud-hypervisor over `/dev/kvm`) | `/dev/kvm`, docker, and the three guest images this package builds from `third_party/granule` | the ten-plus minutes a full build takes, and a C toolchain + node/npm this package will never install for you | **implemented** -- this is `grain init`'s original target; see [grain-init.md](grain-init.md) for the full step list |
| `linux-container` | `orchestrator.ContainerSandboxes` | container, shares the host's kernel | docker; explicitly *not* `/dev/kvm`, nested virt, or any guest image | kernel-level isolation between a sandbox and the host -- a sandbox escape reaches the host directly, which `linux-kvm`'s VM boundary does not allow | **implemented** -- the cheapest proof the table above generalises beyond one row: nothing here for `grain init` to build but the grain binary itself, so it comes up in well under a minute rather than `linux-kvm`'s ten-plus |
| `k8s-container` | `orchestrator.K8sSandboxes` | pod, shares its node's kernel | a cluster with a node that can fit one sandbox pod (2 CPU / 8 GiB by default -- a smaller node leaves every run's pod Unschedulable, which fails the run after six minutes (`orchestrator.DefaultK8sUnschedulableTimeout`); init's "check a node can fit one sandbox pod" step reports it up front, naming the `sandboxCpus`/`sandboxMemoryMb`/`sandboxDiskGb` to lower), a pullable sandbox image, ReadWriteOnce storage, and -- for any team that ever dispatches a `network: none` task -- a NetworkPolicy-enforcing CNI | kernel-level isolation (same trade as `linux-container`, scheduled by a cluster instead of run on one box); pods must **never** be privileged -- that restriction is the whole point of keeping this separate from `k8s-granule` | **implemented** -- preflight and apply/provision live in `pkg/hostinit/k8s.go`; proven end to end against a real `kind` cluster, which is also where it found and fixed a pre-existing bug in `deploy/kubernetes/grain.yaml` itself (see that file's own `NET_BIND_SERVICE` comment) -- see [k8s-init-design.md](k8s-init-design.md) for the design and where the implementation diverges from it |
| `k8s-granule` | `orchestrator.K8sGranuleSandboxes`, a privileged pod per run | VM, own kernel (cloud-hypervisor over `/dev/kvm`) | nested-virt-capable nodes, a real `/dev/kvm` inside the pod, and privileged pods **required** | the restriction `k8s-container` depends on (no privileged pods) -- a cluster cannot run both targets under one security policy, which is why these are two `Platform` values rather than two modes of one `-target=k8s` | **implemented** -- preflight and apply/provision live in `pkg/hostinit/k8s_granule.go`; the nested-virt check (the real test of this platform, not merely `/dev/kvm`'s existence) is proven against a real `kind` cluster with `/dev/kvm` mounted in and a locally built `third_party/granule` image, booting a VM and answering `granule ready` in about 11s, and on 10-05 end to end on `kind` with the published `ghcr.io/bwsalmon/grain/guest:latest` (exit 0). A node with no `/dev/kvm` at all is `[refused]` by name: sandbox and preflight pods mount it as a `CharDevice` hostPath, so the kubelet's `FailedMount` says so rather than creating an empty directory at `/dev/kvm` on the node -- see [k8s-granule-design.md](k8s-granule-design.md) for the design and its own "What building this actually settled" section for where the implementation diverges |
| `gke-sandboxes` | `orchestrator.K8sGranuleSandboxes` driven by an off-cluster controller via `/var/lib/grain/kubeconfig` and `granule forward` | VM, own kernel (cloud-hypervisor over `/dev/kvm`) | `gcloud`, `kubectl`, `gke-gcloud-auth-plugin`, and a GCP sandbox project with nested virtualization enabled | running the controller itself as an in-cluster Deployment (the controller stays on its existing host while sandbox pods run on GKE Standard) | **implemented** -- idempotent and resumable GKE Standard cluster + nested-virt node pool + KVM device plugin + least-privilege `grain` ServiceAccount RBAC + `/var/lib/grain/kubeconfig` + verification live in `pkg/hostinit/gke_sandboxes.go`; see [k8s-init-design.md](k8s-init-design.md) |

## What has actually been run, and when

"Implemented" above means the code exists and was exercised. This
section is the narrower claim: which platforms have been taken from
nothing to a provisioned deployment, on what, and what the run cost.
It is here because the table's own word drifts -- a platform proven on
`kind` months ago and a platform proven on GKE today both read
"implemented", and the difference matters when one of them is about to
be relied on.

`grain init`'s exit codes are the measure: **0** means the deployment is
up and provisioned, **10** means manual or refused steps remain
(docs/grain-init.md's "The three exit codes"). Only an exit 0 counts
below.

The runs are repeatable:
[`scripts/platform-verify/`](../scripts/platform-verify) holds one
script per platform, each of which builds its host or cluster from
nothing, asserts the exit code and tears everything down again. A row
here that has gone stale is re-earned by running the matching script,
not by re-reading the code.

| `-platform` | last proven | on | exit | wall clock |
|---|---|---|---|---|
| `linux-kvm` | 2026-10-04 | fresh GCE `n2-standard-8`, nested virt, Debian 12 | **0** | 790s |
| `linux-container` | 2026-10-04 | fresh GCE `e2-standard-4`, no nested virt, Debian 12 | **0** | 219s + a re-run |
| `k8s-container` | grain task 27 | a real 1-node GKE cluster | preflight only | -- |
| `k8s-granule` | 2026-10-04 | a real 1-node GKE cluster, `n2-standard-8`, `UBUNTU_CONTAINERD`, nested virt | **0** | ~2 passes |

Three things that record says and the table above does not:

- **`k8s-granule` had never booted a guest on a real cluster until
  2026-10-04.** Its preflight was proven, on `kind` and then on GKE by
  task 27, but task 27's GKE run stopped at a wrong-image mistake and
  nothing booted. The run that finished reported `a privileged pod ...
  booted a VM over /dev/kvm and answered ready`, then applied the
  manifest, completed the rollout, reached the API and provisioned the
  admin account -- exit 0. Getting there needed
  `deploy/kubernetes/grain-granule.yaml` to stop passing `-upgrade-image`,
  which `cmd/grain`'s own `checkConfig` refuses with
  `-k8s-granule-sandboxes`, so the Deployment could never start.
- **`k8s-container` is the one row whose last real run is older than
  the changes since.** Task 27 proved its whole preflight against GKE
  -- a real ghcr pull, a real ReadWriteOnce bind -- but not an exit 0,
  and that was before the sandbox-budget refusal and the adopt-mode
  work landed. It is the next one to re-run.
- **Both Linux rows needed two passes, for a reason worth knowing
  before running either.** `-ui-addr` does not tell the daemon where to
  listen; it is where init's own last four steps *reach* the daemon
  back, and the daemon takes its address from its own `-ui-addr`
  default unless `-daemon-arg -ui-addr=...` says otherwise. Passing
  only the former leaves a healthy daemon on 4242 while init polls
  somewhere else and ends at "confirm the daemon's API is reachable ...
  did not answer within 30s". The flag help says this; it still reads
  like configuration. A non-default port has to be given twice.

Re-running is the normal path and the second pass is cheap: on
`linux-container` the re-run adopted the daemon the first pass had
already started ("a daemon is already running ... grain init adopts it
as-is"), skipped every step already done, and went from exit 10 to exit
0.

`orchestrator.HostSandboxes` -- running agents directly in host
directories, no container or VM boundary at all -- is not a fifth
platform here. The [README](../README.md) already says that shape is no
longer supported; `grain init` has no flag value for it and never will.

## What `linux-container` actually had to answer

Nearly every step `linux-container` needs already existed for
`linux-kvm`'s sake: `ensureSandboxNetwork` does not care which backend
ends up joining the network it reads a gateway off of, so the same
function, the same default network name
(`hostinit.DefaultSandboxNetworkName`, which happens to already equal
`orchestrator.DefaultContainerNetwork`), and the same refusal (no
manual fallback, ever) carry over unchanged. The one real question was
what a container sandbox reaches the daemon's git proxy and model
proxies at, since this platform has no granule VM and so no
`-granule-create-arg --network=...` wiring an operator already trusts.
The answer is the same network's gateway `linux-kvm` already reads,
just passed to the daemon under a different name
(`-container-sandbox-network` and `-container-git-proxy-host` in place
of granule's own `-docker-run-opt --network=` and
`-granule-git-proxy-host`) -- read before the daemon ever starts, the
same way, so the daemon's own lazy network creation
(`orchestrator.ContainerSandboxes.ensureNetwork`) finds the network
`grain init` already made and never guesses a gateway of its own.

## Why `k8s-container` and `k8s-granule` are not one flag

Both rows are "run grain on Kubernetes" in the loose sense, and an
operator could easily reach for one `-platform=k8s`. They are kept as
two names because their preflights say opposite things about the same
property -- whether a sandbox pod may be privileged -- and a flag that
silently picked one meaning would make neither check trustworthy: a
cluster preflighted for `k8s-container`'s "never privileged" and then
quietly run as `k8s-granule` would be handed privileged pods its own
check had just refused, and the reverse would refuse nested-virt
scheduling a `k8s-granule` deployment actually needs. `k8s-init-design.md`
makes this argument in full for the design it covers (`k8s-container`);
[k8s-granule-design.md](k8s-granule-design.md) restates it symmetrically
for the row covered here, and settles the rest of what this platform
needs now that it, too, has a design rather than only a reserved name.

## Migrating a local install to GKE

`scripts/migrate-local-to-gke.sh` moves an existing local install onto
GKE. A local install is `linux-kvm` or `linux-container`, set up by
`grain init` or `scripts/setup.sh`: a data directory under
`/var/lib/grain`, `grain-daemon.service`, `grain-hostd.service` and
`/etc/grain/hostd.yaml`. After the migration the controller runs in a
cluster on the `k8s-granule` backend
(`deploy/kubernetes/grain-granule.yaml`), and `grain hostd` keeps
running on the host and dials out to it. So does a `grain
model-server`, for host-bound CLIs such as `agy` on a Cloudtop. It is
the same deployment, with the same store, history, secrets key,
installation id and state repository; only the place the controller
runs has changed.

This script keeps the store the local daemon already has: SQLite moves
onto the `grain-data` PVC, and a Postgres recorded in `db-config.json`
is reached from the cluster instead. It copies the whole data directory
so the in-cluster controller comes up with all existing checkouts,
history and configuration intact.

```sh
sudo scripts/migrate-local-to-gke.sh --dry-run \
  --cluster grain-sandboxes --project my-proj --location us-central1-a
sudo scripts/migrate-local-to-gke.sh \
  --cluster grain-sandboxes --project my-proj --location us-central1-a
```

The stages are below. Every one is idempotent, so a re-run after a
failure picks up where the last run stopped:

1. **Preflight.** The data directory must hold `store/grain.db` or
   `db-config.json`, and `secrets/secrets.key`. It warns when
   `installation-id` or `state-repo` is missing. `grain-hostd.service`
   and its allowlist must exist. The controller image, the packed
   guest for `-k8s-granule-image` and the default target repository
   are read from the local `grain-daemon.service` unless flags give
   them.
2. **Cluster.** The script needs an admin kubeconfig: `--kubeconfig`,
   or one that `--cluster/--project/--location` fetch with `gcloud`.
   With `--provision` it first runs `grain init -platform=gke-sandboxes`.
   `gke-sandboxes`' own `/var/lib/grain/kubeconfig` is not enough on
   its own: it is a least-privilege ServiceAccount that can create
   sandbox pods but not a PVC, a Secret or a Deployment, and the script
   says so. The namespace is created and labelled
   `pod-security.kubernetes.io/enforce=privileged` if needed, and the
   `granule-device-plugin` DaemonSet must already be in `kube-system`.
   The RBAC and PriorityClasses come with the manifest.
3. **Drain and stop.** `grain pause -set` stops new dispatch, and
   `--drain-wait` gives in-flight runs time to finish. Then
   `grain-daemon.service` is stopped, disabled and **masked**, and a
   `grain-daemon` container is removed. Masking means a reboot or a
   re-run of `setup.sh` cannot start a second controller against the
   same state repository. `systemctl mask` refuses a unit whose own
   file is in `/etc/systemd/system`, where `setup.sh` puts it, so that
   file is moved to `grain-daemon.service.pre-gke` first. A `tar.gz` of the data directory goes to
   `/var/backups/grain/grain-data-<UTC timestamp>.tar.gz`.
4. **State and secrets.** `Secret/grain-migration` carries
   `secrets.key` and the shared token (`/etc/grain/hostd.token`,
   created if missing). The rendered manifest is applied at `replicas:
   0`, which creates the PVC without letting the controller write an
   empty store into it first. A transfer pod (uid 1000, the controller
   image by default) receives the data directory as a `tar` stream, so
   every file lands owned by uid 1000. `/data/.migrated-from-local`
   marks the copy as done, and a re-run skips it; `--recopy` copies
   again and is refused while the controller runs. The rendered
   manifest also adds `-hostd-token-file`, `-executor-addr=:4243` and
   `-executor-token-file`, all pointing at the Secret's token.
5. **Controller.** The script scales `Deployment/grain` to 1 and waits
   for `kubectl rollout status`.
6. **This host.** By default `grain-gke-forward.service` runs `kubectl
   port-forward svc/grain` to `127.0.0.1:4280` as hostd's user;
   `--server-url` names a Gateway/IAP URL to use instead. A drop-in,
   `grain-hostd.service.d/gke.conf`, re-runs hostd's own `ExecStart`
   with `-server <url> -token-file /etc/grain/hostd.token`. The
   allowlist, its built-in capability servers (`grain-gcp`,
   `grain-host-script`, `grain-gchat`, ...) and the local socket stay
   as they were. When `agy` is installed (or with `--model-server
   yes`), `grain-model-server.service` runs `grain model-server
   -server <url> -token-file ...`, using the same token.
7. **Readiness.** There is no `/api/healthz`, so the script polls
   `/api/auth/access-code`, the pod's own readiness probe. It
   authenticates with the migrated `local-api-token`, which the
   controller accepts over the port-forward because the request reaches
   it on loopback. It sets `sandboxBackend=kubernetes-granule` through
   `PUT /api/settings`. Without that, the stored local backend stays
   in the database: the controller falls back to its flags on every
   start and says so. The script then waits until every `run: host`
   server in `/api/capability-servers` is `attached` and this host
   appears in `/api/executors`, and finally lifts the dispatch pause.

`--rollback` reverses this. It scales `Deployment/grain` to 0, then
copies the PVC back into the data directory and moves the local copy
aside to `<data-dir>.pre-rollback-<timestamp>`; `--no-sync-back` skips
the copy. It removes the drop-in, the forward unit and the model-server
unit, restarts hostd on its socket, unmasks `grain-daemon.service`,
moves its unit file back from `grain-daemon.service.pre-gke`, starts it,
and lifts the pause.

`scripts/migrate-local-to-gke_test.sh` runs every stage against a local
install laid out in a scratch directory and stubbed `kubectl`,
`systemctl`, `curl`, `docker` and `grain`.
`tests/deploy/migrate_local_to_gke_test.go` runs that suite in CI. It
also decodes the manifest the migration renders, checking it against
`grain-granule.yaml`'s objects and `grain daemon`'s own flags.
