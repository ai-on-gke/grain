# Verifying a `grain init` platform end to end

Three scripts, one per platform that can be verified without a second
machine, each answering the same question:

> does `grain init -platform=X` reach **exit 0** -- "the deployment is
> actually up and provisioned" -- on a host or cluster built from nothing?

That is a narrower claim than "implemented", and the distinction is the
reason these exist.
[docs/grain-init-platforms.md](../../docs/grain-init-platforms.md)'s "What
has actually been run, and when" table cites results these produced; this
is how to produce them again.

| script | platform | what it builds |
|---|---|---|
| `linux-platform.sh` | `linux-kvm`, `linux-container` | a fresh GCE instance, Debian 12 |
| `k8s-container.sh` | `k8s-container` | a 1-node GKE cluster, no nested virt |
| `k8s-granule.sh` | `k8s-granule` | a 1-node GKE cluster with nested virt |

```bash
PLATFORM=linux-kvm NAME=verify-kvm MACHINE_TYPE=n2-standard-8 NESTED=1 \
  ./scripts/platform-verify/linux-platform.sh
./scripts/platform-verify/k8s-container.sh            # create, prove, destroy
./scripts/platform-verify/k8s-granule.sh --keep       # leave the cluster up
```

Every one of them tears its resources down on exit, on failure and on
Ctrl-C. `--keep` is the only way to leave something running, and an
argument none of them recognises is refused rather than treated as "not
`--keep`".

## Not the same as the smoke scripts

[`scripts/gce-vm-smoke.sh`](../gce-vm-smoke.sh) and
[`scripts/gke-cluster-smoke.sh`](../gke-cluster-smoke.sh) ask *"is this
credential good enough to stand something up?"* -- a question about the
credential, answered before blaming a deployment or an agent. These ask
*"does this platform's whole bring-up work?"* -- a question about grain.
Run a smoke script when a create fails for reasons that look like
permissions; run one of these when the platform table's claim needs
re-earning.

## Why they are not run from CI

They cost a VM or a cluster per run, several minutes each, and need cloud
credentials a task's sandbox does not have -- the controller host's own
service account answers 403 to `gcloud container clusters list`. So these
are an operator's tool, run deliberately, which is also why each one
leaves a record worth pasting into the table rather than only an exit
code.

## Things they encode that cost a run to learn

Each is commented with its own reasoning; these are the ones that bite
more than once.

- **`-ui-addr` does not tell the daemon where to listen.** It is where
  `grain init`'s own last four steps *reach* the daemon back. Pass it
  alone and a healthy daemon sits on 4242 while init polls elsewhere and
  stops at "confirm the daemon's API is reachable ... did not answer
  within 30s". A non-default port has to be given twice, once via
  `-daemon-arg -ui-addr=...`. `linux-platform.sh` does that.
- **`grain init` must run as root** on the Linux platforms. It installs
  docker and Go, creates the daemon's system account and links the claude
  CLI; run unprivileged it correctly reports those as `[manual]` and does
  nothing else.
- **Deleting a GKE cluster does not reclaim its PersistentVolume's disk,**
  and neither does deleting the namespace first: PV deletion is
  asynchronous and the CSI controller goes away with the cluster. Both
  k8s scripts sweep unattached `pvc-*` disks after the delete, because
  every GKE run before that sweep existed left a 20 GiB volume behind.
- **`grain init` substitutes none of a manifest's `CHANGEME` values.** They
  are operator-edited by design, so these scripts render a copy and pass
  it with `-k8s-manifest` / `-k8s-granule-manifest`.
- **A packed guest cannot be pulled inside the nested-virt preflight's
  30s window.** `k8s-granule.sh` therefore runs init twice: the first pass
  pulls, the second times the boot. See grain task 34.
- **Validate before spending.** Each script runs `grain init -dry-run`
  before creating anything, because the first version of `k8s-granule.sh`
  lost a seven-minute cluster to a missing `-repo-root`.
