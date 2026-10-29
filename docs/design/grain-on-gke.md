# Grain on GKE, with hostd dialling in from the cloudtop

Status: proposed. Nothing in this document is built yet. Its
requirements are the stages at the end (GKE-1 to GKE-9). Each stage is
filed as its own task, held for approval and chained on the stage before
it, once this doc merges. Implementing commits carry
`Design-Item: docs/design/grain-on-gke.md#GKE-<n>`. Everything before
*The stages* records decisions and the reasons for them. Those sections
are not requirements, so their headings carry no IDs.

This doc supersedes `docs/design/grit-split.md` (task 1327) as the plan
for this deployment. The grit split is **deferred, not abandoned**: what
GS-1 and GS-2 merged stays, and *What happens to the grit work* says what
a later split would reuse from this design.

## Decisions already made

These were made by the operator on 2026-10-27, through the director, on
task 859. They are the brief, not open questions.

1. **grain runs on the GKE cluster** (`kubernetes-granule`, the cluster
   and node pool this deployment's sandboxes already use). **Only
   `grain hostd` stays on the cloudtop.** It runs what needs the user's
   corp identity there: host-run CLIs such as `agy`, `run: host`
   capability servers such as `gcp`, `gchat` and `host-script`, and
   anything that needs the operator's own corp credentials.
2. **CI stays inside grain.** Grain's CI engine keeps its
   controller-driven step model. Grain drives each step; there is no job
   pod that runs a whole job on its own.
3. **Sandbox access moves to granule's dial-out channel**
   (gke-labs/granule, [Task #1456](/tasks/1456)). `kubectl exec` remains
   only for bootstrap and fallback.
4. **Keep everything GS-1 and GS-2 merged** (#1329, #1330). That covers
   `grit serve`, `/v1`, `pkg/gritapi`, `pkg/attest`, `pkg/treeid`, the
   `grit/vX.Y.Z` tags and the decoupled `pkg/pkgcache`. The grit boundary
   is kept: `pkg/grit/independence_test.go` stays, and no grain-core
   import goes into `pkg/grit/...`.
5. **Remote executors and hostd become one concept, hostd, and there may
   be any number of them.** hostd's dial-in is built on the executor
   connection that already exists (`pkg/modelexec`). No second protocol
   is invented.
6. **hostd placement is explicit configuration.** A model connection,
   or a CLI framework's run location, may say *run on hostd `<name>`* (or
   on a hostd label) with **ambient** credentials. A capability server
   becomes `run: hostd` with a hostd name or label. Nothing falls over to
   another hostd, or to the container, unless the operator configured a
   fallback list.

## Why

Grain runs on the cloudtop today and reaches every sandbox pod through
the API server. Every step, every file write and every `granule forward`
is a `kubectl exec` (`mcp.KubectlExecRunner`, with
`orchestrator.K8sGranuleSandboxes.execRunner` adding `granule exec` as
its `Through`). The route is apiserver → konnectivity → kubelet → the
pod. It fails intermittently with:

```
unable to upgrade connection: error dialing backend: dial timeout, backstop
```

Each failure is a failed step or a dropped forward. A CI step that loses
its exec has no way back to the command, so the job fails, and on a bad
afternoon that kept main red for hours. Nothing grain does on the
cloudtop can repair that route, because the route is the cluster's
control plane.

Granule's dial-out mode (#1456) reverses the direction. The pod-side
wrapper connects out to its controller. Once grain is inside the
cluster, that connection is pod-to-pod over the VPC, and the API server
is used only to create, watch and delete pods. That is the whole
reliability argument: the control plane leaves the data path.

Moving grain into the cluster also moves the package cache, the git
proxy and the model proxy beside the sandboxes. Package bytes stay
inside GCP rather than crossing to a cloudtop and back, and a sandbox
reaches those services at a cluster address rather than through a
reverse forward.

What the cloudtop still has that the cluster cannot have is the user's
corp identity. That is why hostd stays there. Because hostd now dials
grain, the cloudtop needs no inbound route from anything.

## Terms

- **Wrapper.** Granule's pod-side process, in the pod's `sandbox`
  container and outside the VM. It boots the guest, and in dial-out mode
  it is the client end of the sandbox channel.
- **Guest.** The VM the wrapper boots. The agent's tools and a CI job's
  steps run here.
- **Acceptor.** The server end of the sandbox channel, inside grain.
- **Sandbox channel.** One wrapper's mTLS connection to the acceptor,
  multiplexing exec, file put and forwards.
- **hostd.** A process outside the cluster that dials grain and runs
  allowlisted programs under its host user's identity. Today's
  socket-attached `grain hostd` and today's `grain executor` are both
  hostds after GKE-5.
- **Ambient credential.** A login that belongs to a hostd's host user,
  such as `agy`'s corp login or `gcloud`'s user credentials. Grain
  stores nothing for it.

## Non-goals

- Running CI as self-driving job pods (Argo or grit's `grit pod`). CI
  stays controller-driven (decision 2).
- Splitting grit out as a service now (decision 4).
- Multi-replica grain. This design runs one replica and fences it (see
  *High availability*). Whether to go further is an open question.
- Changing the plain `k8s-container` backend (`orchestrator.K8sSandboxes`)
  beyond what falls out of grain being in-cluster. It has no wrapper and
  keeps `kubectl exec`.
- Remote sandbox pools (`docs/design/remote-sandbox-pools.md`). That
  doc assumes a daemon on a host reaching clusters from outside. It
  should be re-scoped against this one before it is built, and nothing
  here depends on it.

## What exists today

Check this before designing anything new. Most of the in-cluster shape is
already built and tested.

| Piece | Where | State |
|---|---|---|
| grain as a Deployment, granule sandboxes | `deploy/kubernetes/grain-granule.yaml` | Exists. One replica, `Recreate`, uid 1000, RWO `grain-data` PVC, least-privilege Role, PriorityClasses. Self-upgrade is refused with `-k8s-granule-sandboxes` (`cmd/grain` `checkConfig`). |
| grain as a Deployment, container sandboxes | `deploy/kubernetes/grain.yaml` | Exists, with the self-upgrade, run pods (`runhost.Kubernetes`) and `-k8s-sandbox-service`. |
| Sandbox-facing Service | `-k8s-sandbox-service` (`grain-sandbox`) | Exists. Sandboxes reach the git proxy, model proxies and package cache at its ClusterIP, so an adopted run survives a daemon roll. |
| Readopting sandbox pods after a restart | `K8sGranuleSandboxes.Readopt`, `7c5386a0d`, `698fd097f` | Exists for pods. Commands running in them are **not** reattached. |
| Run-host adoption across `rollout restart deploy/grain` | `runhost.Kubernetes`, `docs/test-plans/2026-10-15.md` §9 | Unit-tested and walked on kind with a stand-in daemon. The real-image walk is still listed as not proved. |
| NetworkPolicy for `network: none` | `deploy/kubernetes/networkpolicy.yaml` | Exists. Egress only to grain pods; no ingress. Assumes nothing dials a sandbox. |
| GitHub webhook gateway behind IAP | `deploy/kubernetes/github-webhook.yaml`, README §"standalone GitHub App bot" | Exists. `/webhooks/github` goes to a non-IAP backend and `/*` goes to `Service/grain` behind IAP. |
| IAP backend reconciliation | `pkg/iapreconciler` | Exists. |
| Postgres store | `-db-driver=postgres`, `docs/cloudsql-postgres.md` | Exists, including Cloud SQL Auth Proxy, startup retries, and `POST /api/database/upgrade`, which migrates a running SQLite store into Postgres (`pkg/ui/database.go`). |
| State repo dump and restore | `pkg/staterepo` (`Export`, `Import`, `ImportMigrated`) | Exists. A whole-database text dump to grain/config, and a destructive restore into an empty store. |
| Secrets | `pkg/secrets` | An encrypted file in the secrets repo. The private key is one file on the daemon's disk and is never in a repo. |
| Remote executors | `pkg/modelexec`, `/api/executors/connect` and `/stream`, executor tokens, `install.sh` | Exists. The executor dials grain, says hello with a token, stages files and runs CLIs through `RemoteHost` (an `agent.Host`), tunnels MCP back (`mcpGrants`), carries capability MCP streams (`capabilitymcp.go`), and can authenticate to IAP (`iap_auth.go`). |
| hostd | `pkg/hostd`, `-hostd-socket`, `/run/grain-hostd/hostd.sock`, `/etc/grain/hostd.yaml` | Exists. Unix socket, one process per connection, protocol 3 with detached starts, attach, drain and cutover. It relies on **same paths**: the daemon stages files under `GRAIN_DATA_DIR`, and hostd sees them at the same path. |
| `infra` verdicts and automatic re-runs | `pkg/orchestrator/infraverdict.go` | Exists for release gates: up to `maxInfraRetries` (2) re-runs of jobs that ended cancelled, errored or interrupted. |
| CI and package cache behind interfaces | GS-1 (`ci.Runner`, `pkgcache.Store`, `pkgcache.Admin`) | Merged. |

The rendezvous relay named in the executor brief no longer exists. It
was removed on 2026-10-20 (`docs/relay-removed.md`). With grain behind a
public Gateway a hostd dials it directly, so nothing needs to replace it.

So the new work is not "make grain run in Kubernetes". It is:

1. the sandbox channel;
2. reattach, both across a dropped channel and across a grain restart;
3. hostd over the network, merged with executors;
4. the production shape around the existing manifests: Cloud SQL,
   Gateway, IAP, Workload Identity and the fence;
5. the migration.

## Topology

```
cloudtop                       GKE Gateway (IAP)              GKE cluster, namespace grain
--------                       -----------------              ----------------------------
grain hostd  --dials out-->    /api/hostd/*      ------->     Deployment grain (1 replica)
 allowlist                     /*  (UI, API)     ------->       :8080  UI / API / hostd
 agy, gcp, gchat ...           /webhooks/github  ------->       :4244  sandbox-access acceptor
                                 (no IAP, to grain-webhook)     :...   git proxy, model proxy,
browser      ------------->    /*                                      package cache

                                                              Service grain          -> :8080
                                                              Service grain-sandbox  -> proxies
                                                              Service grain-access   -> :4244

                                                              sandbox pod
                                                                wrapper --mTLS--> grain-access
                                                                guest   -------> grain-sandbox

grain --(Workload Identity)--> Cloud SQL (Postgres), Secret Manager, GCS
grain --(API server)---------> create / watch / delete pods only
```

### The daemon: a Deployment, one replica, fenced

Start from `deploy/kubernetes/grain-granule.yaml` and keep it a
**Deployment** with `replicas: 1` and `strategy: Recreate`. It does not
become a StatefulSet.

- The self-upgrade (`pkg/upgrade`'s `stageKube`), the Role's
  `deployments/grain` patch grant, and the run-host roll that was walked
  in `docs/test-plans/2026-10-15.md` §9 all target a Deployment.
- A StatefulSet would give stable identity and `volumeClaimTemplates`.
  With one replica, a store in Postgres and a single named PVC, neither
  is needed.
- `Recreate` is not enough on its own to rule out two daemons. A node
  partition can leave the old pod running while its replacement starts.
  The fence in *High availability* closes that gap. `maxSurge: 0` cannot
  close it.

The production manifest is a new overlay,
`deploy/kubernetes/gke/` (kustomize over `grain-granule.yaml`), so the
generic manifests stay generic. It adds the Cloud SQL connector,
Workload Identity, the access port and Service, the Gateway, the
PodMonitoring and the NetworkPolicies below.

The self-upgrade for `-k8s-granule-sandboxes` has to be enabled as part
of GKE-7. It is refused today. Until it is enabled, an upgrade is the
deploy script's `kubectl set image`.

### The state store: Cloud SQL for PostgreSQL

**Recommendation: Cloud SQL** (Postgres 16, private IP, regional HA as
the operator chooses), reached through the Cloud SQL Go connector or an
auth-proxy sidecar using Workload Identity and IAM database
authentication. grain already supports this
(`docs/cloudsql-postgres.md`).

Why Cloud SQL rather than in-cluster Postgres (CloudNativePG, which
grit-split chose for grit's own store):

1. **It is reachable from both sides of the cutover.** The cloudtop
   daemon can move onto Cloud SQL first, with the existing
   `POST /api/database/upgrade`, while it is still on the cloudtop
   (GKE-1). The cutover then moves only the process, not the data. The
   rollback is the reverse move, with no data copy in either direction
   (*Migration*). An in-cluster database could not serve the cloudtop
   daemon without exposing Postgres outside the cluster.
2. **Grain's store is the deployment.** It holds every task,
   conversation, run and verdict. Point-in-time recovery, automated
   backups, maintenance windows and a failover replica are product
   features in Cloud SQL. With CNPG they are operator and barman
   configuration that this deployment would have to run and drill
   itself.
3. **It keeps the store off the node pool.** The node pool is sized for
   nested-virt sandboxes and runs privileged pods. A database there
   competes with sandboxes for disk IOPS and shares a blast radius with
   them.
4. **IAM authentication.** grain's Workload Identity service account
   logs in as an IAM database user. No password is stored, which removes
   `postgres.DatabasePasswordSecret` from this deployment.

What in-cluster Postgres would win: lower cost at this size, no Cloud
SQL API dependency, and the same shape grit-split chose. That is a
legitimate choice for a cost-sensitive deployment, and the overlay keeps
it possible by taking the DSN from a Secret. It is listed under *Open
questions*, with Cloud SQL as the recommendation.

The `grain-data` PVC stays. It holds what is not in the store: repository
checkouts, agent HOMEs, transcripts, CI artifacts and the job cache, and
the package cache's objects. All of it can be rebuilt except the
transcripts and artifacts of recent runs (*Migration*). Size it from the
cloudtop's current `du` of those directories plus 50%, on `pd-balanced`
or `hyperdisk-balanced`, and keep it `ReadWriteOnce`. The
`runhost-rwx.yaml` option still applies if run pods are spread across
nodes.

### Secrets

grain's credential store stays what it is: the encrypted secrets file in
the secrets repo, sealed to a private key. Two things move.

- **The private key.** It moves from the cloudtop's data dir into Secret
  Manager, as `grain-secrets-key`. It is mounted into the daemon pod
  read-only through the Secret Manager CSI driver (GKE's
  `secrets-store-gke` add-on), authorised by Workload Identity. It is
  never a Kubernetes Secret object, so `get secrets` in the namespace
  does not reach it, and every read is a Data Access audit entry. This
  mirrors what `deploy/terraform/gcp/secrets.tf` already does for the
  GCE shape.
- **Bootstrap credentials** (the state-repo token, the secrets-repo
  token, the GitHub App key and the webhook relay secret) go into Secret
  Manager the same way, one secret each, using the same
  `push-secrets.sh` convention.

The per-sandbox access material (*Sandbox access*) is the one thing
grain writes as Kubernetes Secrets, because the kubelet has to mount it
into a pod grain creates. Each is owned by its pod, so it is deleted
with the pod.

### Guest-facing services: package cache, git proxy, model proxy

These already run inside the grain process and are already advertised at
`-k8s-sandbox-service`'s ClusterIP. In-cluster, that means:

- **`Service/grain-sandbox`** exposes the git proxy, the model proxies,
  and the package cache's interception listeners (53, 80 and 443). A
  guest on an open network (`CHV_NET_MODE=inline`) reaches them
  directly at that IP. The `granule forward` tunnels over `kubectl exec`
  (`k8sGranuleSandbox.startForward`) are no longer needed for these
  guests.
- **`network: none` guests have no NIC.** They still get those services
  on guest loopback, but carried as forwards over the sandbox channel
  (*Sandbox access*) instead of `granule forward` over `kubectl exec`.
  `ReservedGuestPorts` is unchanged.
- **The package cache stays in-process** behind GS-1's `pkgcache.Store`
  and `pkgcache.Admin`, with its object store on the PVC. A GCS-backed
  `pkgcache.Store` is a later option (GS-3 sketched one) and is not
  needed for this move. Package bytes now go cluster → GCP egress and
  stay in GCP.
- Each service keeps its per-sandbox authentication: git proxy scoped
  tokens and model-proxy tokens. A guest that reaches the Service IP
  still gets only what its own token allows.

`Service/grain-access` is separate from `grain-sandbox` and exposes only
the acceptor port (4244). That separation is what makes the egress rules
in *Security* expressible.

### UI and API ingress

Use a GKE Gateway (`gke-l7-global-external-managed`) with IAP enabled on
the `grain` backend through a `GCPBackendPolicy`. `pkg/iapreconciler`
already manages the IAP membership. Routes:

| Path | Backend | Auth |
|---|---|---|
| `/*` | `Service/grain:80` | IAP (the operator's Google identity). grain does not check the IAP assertion today; GKE-7 adds that check with `pkg/jwks` (which already reads IAP's key map), so a request that reaches port 8080 without passing the Gateway is refused. |
| `/webhooks/github` | `Service/grain-webhook` | GitHub's HMAC. No IAP, as today. |
| `/api/hostd/connect`, `/api/hostd/install.sh`, `/api/executors/*` | `Service/grain` | IAP (hostd's ID token for the IAP audience, `modelexec.IAPTokenProvider`), then the hostd token in the hello frame. |

The hostd routes need their own `GCPBackendPolicy` with
`timeoutSec: 86400`. On the global external load balancer a backend
service's timeout is also the longest a WebSocket may live. The default
of 30 s would reset every hostd twice a minute. hostd reconnects anyway
(*Disconnection*), so a daily reset is harmless.

The DNS name and the IAP OAuth client are open questions.

The sandbox acceptor is **never** on the Gateway. It is a ClusterIP
Service reachable only from pods in the namespace that NetworkPolicy
admits.

### Workload Identity

grain's Kubernetes ServiceAccount `grain/grain` is bound to a GCP service
account, `grain-daemon@…`, granted:

- `roles/cloudsql.client` and `roles/cloudsql.instanceUser` on the
  instance;
- `roles/secretmanager.secretAccessor` on each named secret, and not
  project-wide;
- `roles/storage.objectAdmin` on the backups and artifacts bucket, if
  one is used;
- nothing else.

In particular it gets no project-level IAM, and it does not get the
capability servers' GCP permissions. Those stay with the hostd that runs
`gcp` under the user's ambient identity (*hostd*), or with a capability
key as today (`docs/gcp-key-permission-sets.md`).

**GitHub.** The GitHub App private key is in Secret Manager. grain mints
installation tokens as it does now. Workload Identity Federation into
GitHub does not exist, so the key remains a stored secret, read through
WI.

Sandbox pods run as a different ServiceAccount, `grain-sandbox`, with
**no** WI binding and `automountServiceAccountToken: false`. The
metadata server is blocked for them by GKE's metadata server
(`--workload-metadata=GKE_METADATA` on the node pool) together with the
egress policy below.

### High availability

One replica, fenced:

- **The fence.** At startup, before anything else that writes, grain
  takes a Postgres session-level advisory lock,
  `pg_try_advisory_lock(<hash of deployment id>)`, on a dedicated
  connection. It holds that connection for the life of the process. If
  the lock is held, grain logs who holds it (a `daemon_lease` row: pod
  name, host, start time, build) and retries for at most
  `-fence-wait` (default 2 m). Then it exits non-zero, so the Deployment
  backs off rather than running a second writer.
- **Losing the lock.** If the lock's connection drops, grain stops
  dispatch and the CI engine at once. It tries to retake the lock for
  15 s, and exits if it cannot. Exiting is safe because every running
  thing is reattachable (*Restart*).
- A partitioned old pod therefore loses its lock with its database
  connection and stops. A new pod cannot take the lock until it has.
  This is the same fence the cutover uses (*Migration*). It is also what
  makes two daemons at once impossible, rather than just unlikely.

Restart time is the outage. With reattach (GKE-4) a restart costs a
pause, not lost work. Whether to go further (a warm standby that waits
on the lock, or real multi-replica) is an open question. The fence is
the prerequisite for either.

## Sandbox access

### The channel

Granule's wrapper, in dial-out mode (#1456), connects to
`grain-access.grain.svc:4244`. It holds one mTLS connection per
sandbox, multiplexed, carrying:

- **exec**, with streaming stdin, stdout and stderr, a real exit code,
  signals, and an exec id that survives reconnects;
- **attach** to an exec id from a byte cursor. The wrapper keeps a
  bounded spool of each live exec's output, so a reconnecting client
  loses nothing within the spool;
- **batched file put** (path, mode, owner and bytes, many files per
  request), which replaces `PlaceFile`'s one exec per file;
- **forwards** in both directions: a guest-loopback port carried to a
  grain-side address, used for `network: none` guests and for
  capability ports (`docs/capabilities-spec.md` §5.3.1).

The exact wire format is granule's to define in #1456. This doc fixes
what grain needs from it, which is the list above plus:

1. a per-sandbox token checked by the acceptor on top of the client
   certificate;
2. exec ids and spools that survive the **acceptor** going away, so a
   grain restart can reattach (*Restart*);
3. a heartbeat both ends can time out on;
4. a Go client package grain can vendor (`third_party/granule`) and an
   in-process fake for tests.

If #1456 lands without any of those, GKE-2 files the gap against
gke-labs/granule rather than working around it in grain.

**Grain runs the acceptor.** The new package `pkg/sandboxaccess` holds
it. It owns:

- the listener;
- the per-deployment CA (an ECDSA key in Secret Manager);
- per-sandbox credential minting;
- the table from sandbox name to live channel;
- the reattach timers.

It is not under `pkg/grit/...` and adds no grit imports. It sits beside
`pkg/granule` and is neutral in the GS-2 sense: it imports `pkg/granule`
and the standard library only, so a later grit can reuse it. The
independence test gets an allowlist entry for it.

### Credentials, per sandbox

When `K8sGranuleSandboxes.create` builds a pod, grain first mints:

- a client certificate from its CA, with `CN = <sandbox name>`, a URI
  SAN of `grain-sandbox://<deployment>/<sandbox>`, and an expiry equal to
  the sandbox's longest life plus a margin (a CI job's timeout, or a
  persistent sandbox's TTL, capped at 7 days, with rotation over the
  channel before expiry for persistent sandboxes);
- a 256-bit random token. Its SHA-256 is stored on the sandbox's row.

Both go into a Kubernetes Secret, `grain-access-<pod>`, whose
`ownerReference` is the pod. It is mounted read-only **into the wrapper
container only**, at `/var/run/granule/access/`. It is never placed in
the guest's disk or kernel command line, and never in an environment
variable. The Role gains `secrets: create, delete`. RBAC cannot narrow
`create` by name, so *Security* states the residual risk; the
`ownerReference` means a Secret never outlives its pod.

The acceptor admits a connection only if:

1. the certificate chains to the deployment CA;
2. the presented token hashes to the stored hash for the certificate's
   sandbox;
3. that sandbox is live in grain's own view (`K8sGranuleSandboxes.live`
   or a readoptable pod).

A token or certificate for sandbox A cannot open B's channel. A
reaped sandbox's credential is dead the moment its row is.

### Which runners move

| Today | After |
|---|---|
| `K8sGranuleSandboxes.execRunner` → `mcp.KubectlExecRunner{Through: granuleExecPrefix}` | `sandboxaccess.Runner`, which implements the same `sandboxRunner` / `mcp` runner interface (`Run`, `RunStream`) over the channel. |
| `k8sGranuleSandbox.PlaceFile` and `ConfigureGitCredentials`: one exec each | Batched file put. |
| `startForward`: `granule forward` over `kubectl exec -i`, with backoff | Inline guests: none (they reach `grain-sandbox` directly). `network: none` guests and capability ports: forwards on the channel. |
| CI's `ci.Sandboxes` adapter in `cmd/grain` over `orchestrator.Sandboxes` | Unchanged in shape. Its steps run on the same `sandboxaccess.Runner`, so CI picks up the channel through the orchestrator backend. `ci.Sandboxes` itself does not change. |
| `mcp.KubectlExecRunner` | Kept. It is used for bootstrap (see below) and fallback. |

Selection is a flag, `-k8s-granule-access=dialout|kubectl`, defaulting to
`kubectl` until GKE-8. It also requires that grain is in-cluster
(`KUBERNETES_SERVICE_HOST` is set and `-k8s-sandbox-service` resolves),
because a cloudtop grain has no address a pod can dial.

**Bootstrap and fallback.** `kubectl exec` is used for exactly three
things:

1. the readiness probe that already exists (`GranuleReadinessExec`);
2. if the channel has not come up within `-k8s-granule-access-timeout`
   (default 60 s) after the pod is Ready, one diagnostic exec that reads
   the wrapper's dial-out log, which then goes into the acquire error;
3. an operator's own debugging.

There is no silent fallback that runs a step over `kubectl exec` when
the channel is missing. Falling back would bring back the failure this
design removes, and it would hide that the channel is broken. A sandbox
whose channel never connects fails `Acquire` with a reason that says so.
For a CI job that is an `infra` verdict (below).

### A dropped channel

The wrapper reconnects with backoff (granule's). The acceptor keeps each
exec's client-side state (its writers, cursor and exit waiter) for
`-k8s-granule-reattach-timeout`, default 2 m.

- **The wrapper reconnects within the timeout.** The acceptor re-attaches
  every exec the runner still waits on, from its cursor. The step's
  output continues with no gap or duplicate. The step never sees the
  drop. It is logged and counted (`grain_sandbox_channel_reconnects_total`).
- **It does not reconnect in time.** Every exec on that sandbox ends
  with a typed error, `sandboxaccess.ErrChannelLost`, carrying the
  sandbox and how long it was gone:
  - a **CI step** that ends this way concludes its job `errored`, with
    the job's fault recorded as `infra: sandbox channel lost`. The
    existing `infra` handling (`infraverdict.go`) re-runs the job
    automatically, uncached and once per fault, up to `maxInfraRetries`.
    This extends today's release-gate behaviour to every run, merge-queue
    and task runs included. A channel loss never counts as a failure of
    the tree and never files a repair task.
  - an **agent tool call** returns a tool error saying the sandbox is
    unreachable. The orchestrator marks the sandbox unhealthy and
    rebuilds it, as it does today for a dead pod.
  - the pod is then checked. A pod that is gone or terminal goes through
    the existing reap. A pod that is alive but silent is deleted, because
    a wrapper that cannot reach grain cannot be trusted to have stopped.

### Restart: reattach rather than interrupt

Today a grain restart readopts sandbox **pods** but loses every
**command** in them. `StoredRunStore.RecoverInterrupted` stamps every
unfinished CI run cancelled-and-interrupted, and the reconciler re-runs
the branch. This design keeps the commands.

1. **Persist the attachment.** When a step starts, the CI engine writes
   `(run, job, step, sandbox, exec id, cursor)` to a new
   `ci_step_attachment` table, and updates the cursor at least every
   10 s and at step end. Agent tool calls are short and stay unpersisted:
   a restart fails the in-flight call and the agent retries.
2. **On startup, before `RecoverInterrupted`:**
   - readopt the sandbox pods (exists);
   - wait up to the reattach timeout for their wrappers to dial the new
     acceptor;
   - for each unfinished run, rebuild the engine's in-memory state for
     its jobs from the store, and re-attach each running step by exec id
     from its stored cursor.
3. **What reattach yields.**
   - A step whose exec is still running carries on.
   - A step whose exec finished while grain was down gets its real exit
     code from the wrapper, and the job continues from the next step.
   - A step whose sandbox did not come back, or whose wrapper no longer
     knows the exec id, ends with `ErrChannelLost`. The job goes to the
     `infra` path above.
4. **`RecoverInterrupted` then applies only** to runs with no
   attachable step: a run that was queued but had no sandbox, or one on
   a backend without the channel. The interrupted stamp remains the
   right answer for those. Its doc comment and the method are renamed
   `RecoverUnattachable` so the name stays true.

This builds on the engine's existing step model. A step is already a
discrete exec, which is exactly what reattach needs, and is the
practical reason decision 2 keeps that model. A run's verdict can only
be written by the engine after reattach. Nothing in this path writes a
verdict on a guess.

Agent runs already survive a daemon roll through run pods
(`runhost.Kubernetes`, walked in `docs/test-plans/2026-10-15.md` §9).
Their sandbox's tool calls go over the channel after the roll like any
other.

## hostd

### One concept: hostd, built on the executor connection

A remote executor and a hostd are the same thing: a host outside the
cluster that connects to grain and runs work under that host's
identity. After GKE-5 there is one concept, **hostd**, and there may be
any number of them.

- **Transport.** It is the existing executor connection
  (`/api/executors/connect`, `/stream`, `modelexec`'s frames). It
  already has token auth in the hello frame, revocation, instance ids,
  remote staging (`mkdir_temp`, `write_file`, `read_tail` and the
  rest), `exec`, MCP tunnel streams with `mcpGrants`, and capability MCP
  streams. It is renamed in the API (`/api/hostd/connect`; the old paths
  stay as aliases for one release) and gains three things:
  1. **allowlist enforcement on the hostd side.** Every `exec` names an
     allowlist entry, and hostd checks it exactly as `pkg/hostd` checks
     a socket request today, using the same code (`hostd.Allowlist`,
     re-read on every request, root-owned `/etc/grain/hostd.yaml`). An
     executor with no allowlist file refuses every exec. That is a
     behaviour change for today's executors and is migrated explicitly
     (*Migration of executors*);
  2. **detached starts, attach and drain** from hostd protocol 2. A
     host CLI outlives a grain restart, and the restarted grain
     reattaches over the new connection;
  3. **stdio capability streams.** Grain opens a stream that asks hostd
     to start a `capabilities:` allowlist entry and carry its stdio. This
     is today's socket protocol, one process per stream instead of per
     connection.
- **Same paths no longer holds.** Today the socket hostd does no
  staging, because daemon and host share `/var/lib/grain`. Over the
  network, staging uses the executor's remote filesystem frames into a
  hostd-owned root, `/var/lib/grain-hostd/runs/<run>/`. Each CLI
  entry's `paths_under` and `cwd_under` name that root and nothing
  else. `agent.HostdHost` becomes the socket-only adapter, and
  `modelexec.RemoteHost` (renamed `hostd.RemoteHost`) is what every
  networked hostd uses.
- **The socket stays for a co-located daemon.** `-hostd-socket` and
  `/run/grain-hostd` keep working for today's cloudtop install until the
  cutover, and for single-machine installs after it. In the registry a
  socket hostd is simply a hostd named `local` whose transport is the
  socket.

### What a hostd offers

The hello frame carries what the hostd offers, recomputed from the
allowlist on every connect and on `SIGHUP`:

- `name`, its registered name (see below), and `instance`, its process
  instance;
- `identity`: the host user and, where it can be read without a prompt,
  the ambient logins (`gcloud config get account`, the `agy` login
  account), shown in the UI and never trusted for authorisation;
- `clis`: allowlisted CLI entries with versions;
- `capabilities`: allowlisted capability entries;
- `capacity`: concurrent CLI runs and concurrent capability processes,
  from the allowlist's new top-level `capacity:` block, with defaults
  equal to today's executor `Concurrency`;
- `labels`: from the allowlist's `labels:`, such as
  `corp-identity: bsalmon` or `region: us-west`.

### Many hostds: registration, routing, quota

**Registration.** A hostd exists in grain before it connects. Settings →
Hostds (today's Settings → Executors) creates a row
`hostd(name, owner, labels, created_by, token_hash, revoked_at)` and
shows the token once. `name` is unique per deployment, for example
`bsalmon-cloudtop`. `owner` is the person whose identity the hostd
carries, and is required. The install command
(`/api/hostd/install.sh`) is shown with the name and token filled in.

**Identity is the token's, not the hello's.** This fixes §3 of
`docs/test-runs/2026-10-01-remote-executor-relay-hardening.md`.
`sameExecutorPrincipal` already refuses a different token while the
holder is live, but two gaps remain:

- connections under the shared `cfg.Token`, which has no token id, can
  still evict each other;
- once the ping timeout reaps a connection, any token may claim its
  instance id.

After GKE-5, the hostd's name comes from the token's row. The hello's
`name` must match it, or the connection is refused. A connection may
replace only a connection of the **same hostd name**. The shared token
is removed: every hostd has its own token. Rotating means issuing a
second token for the same name, connecting with it, and then revoking
the old one. Both tokens are the same principal by name, so rotation
needs no eviction rule of its own.

**Routing.** Every host-run CLI start and every `run: hostd` capability
server start names its hostd in one of two ways:

- **an explicit pin**, `hostd: bsalmon-cloudtop`, on the model
  connection, the framework's run location or the capability server
  instance;
- **a pool selector**, `hostd: {labels: {corp-identity: bsalmon}}`. Grain
  chooses among connected hostds that match, offer the entry and have
  free capacity, by least load, and records which one it chose.

A selector that matches nothing connected is treated exactly like a pin
whose hostd is disconnected.

**Concurrency and quota, per hostd.** `agents.go`'s `connectionLease`
pool is keyed by `(connection, hostd)` for any connection with ambient
credentials. An ambient login's quota belongs to that host's user, so a
usage-limit pause (for example a Claude connection's) pauses that
connection **on that hostd only**. Capacity from the hello caps
everything on the hostd.

### Placement in settings

**Models (Settings → Models).** A model connection or a CLI framework's
run location gets **Run on**, with one of three values:

- `container`, the default;
- `hostd <name>`;
- `hostd label <k=v>`.

It also gets **Credentials**:

- `stored`, today's behaviour;
- `ambient`, allowed only for a hostd placement. Grain stores no key, and
  the CLI uses whatever login the host user has.

An optional ordered **Fallback** list names other placements and is
empty by default.

- While the named hostd is disconnected, the connection shows
  *unavailable: hostd `<name>` disconnected (since …)* everywhere it is
  listed.
- Runs routed to it park (see *Disconnection*), not fail.
- Nothing moves to another hostd or to the container unless Fallback
  says so, and when it does, the run records that it fell back.

**Capability servers (Settings → Capabilities).** `run: host` becomes
`run: hostd` plus `hostd: <name>` or `hostd: {labels: …}`. Each server
**instance** is pinned to one hostd. For example:

```yaml
gcp-bsalmon:
  type: gcp
  run: hostd
  hostd: bsalmon-cloudtop     # ambient gcloud credentials of that user
gcp-oncall:
  type: gcp
  run: hostd
  hostd: oncall-host
```

Two hostds may run the same server type as separate named instances,
and grants name the instance (`gcp-bsalmon`), not the type. A server's
methods are offered only while its hostd is connected. Otherwise the
server reports `needs-hostd:<name>` in its status and in the capability
broker's answer, the grant is unavailable, and a run that asked for it
parks.

**UI.** Both forms get a **hostd picker**. It is populated from
registered hostds and shows connection state and **identity** (owner,
host user and ambient logins), so it is visible whose credentials a
model or server uses. The task page, the run record and the capability
audit record `hostd` (name and instance) for every host-run CLI and
every capability call served by a hostd.

### How host-run CLIs and capability servers reach sandboxes and grain

Everything is carried on the one hostd connection, multiplexed. No
traffic goes from a sandbox to a hostd, or from a hostd to a sandbox,
directly.

**A host-run CLI (`agy`):**

```
agy (on the cloudtop, user's corp login)
 └─ its MCP server `grain mcpserver …` (argv issued by grain, staged by grain)
     └─ MCP tunnel stream on the hostd connection   [mcpGrants: only argvs grain issued to this hostd's runs]
         └─ grain: the run's MCP tools
             └─ sandbox channel → wrapper → guest   (exec / file put)
```

`agy`'s PreToolUse hook (`writeAgyHome`'s `hooks.json`) runs `grain` on
the host. That `grain` talks to the same tunnel. Model traffic for an
ambient CLI goes straight from the CLI to its provider under the user's
login, as it does today. Transcripts are read back with `read_tail`.

**A `run: hostd` capability server (`gcp`, `gchat`, `host-script`):**

```
guest tool call ─ sandbox channel/Service → grain capability proxy (protocol.Proxy)
   → stdio capability stream on the hostd connection → hostd starts the allowlisted entry
```

Grain stays the policy point. The server's credentials stay on the host
as ambient credentials. hostd only ever moves bytes for a stream grain
opened. For a `host-script` server that runs `kubectl` against this
cluster (the `kubectl-sandboxes` script), the script runs with the
host user's own kube credentials. The `host-gke` server proposed in
`docs/design/remote-sandbox-pools.md` would work the same way.

### Disconnection

A hostd connection is down when its ping times out (existing, 30 s) or
its socket closes. While it is down:

- **A run waiting to start a host-run CLI on it** parks. It is not
  dispatched and not failed, and its reason is
  `waiting for hostd <name> (disconnected since …)`. It resumes when
  that hostd reconnects. With a Fallback list it moves to the next
  placement instead, and records that it did.
- **A host-run CLI already running on it** is detached on the host
  (protocol 2). It keeps running until its own deadline, spooling its
  output. Grain holds the run in a `hostd-disconnected` state for
  `-hostd-reattach-timeout`, default 15 m. If the hostd reconnects in
  that window, grain re-attaches from its cursor and the run never
  noticed. If it does not, the run fails with
  `hostd <name> was unreachable for 15m` as an infrastructure failure,
  retried by the ordinary retry policy, and the detached process is
  cancelled once the hostd comes back.
- **A capability server on it** reports `needs-hostd:<name>`. An
  in-flight call returns a tool error that names the hostd. New runs
  that need the grant park.
- **Nothing silently falls back.** No CLI moves to the container, and
  no server moves to another hostd, without an operator-written Fallback
  list.
- An operator-visible notification, `hostd-disconnected:<name>`, is
  raised after 5 minutes and clears itself on reconnect.

## Security

Running grain in-cluster with a dial-in channel brings back a control
network that `kubectl exec` did not have. The properties below are what
keep it from being a way in.

1. **The channel terminates in the wrapper, outside the VM.** The guest
   runs the agent's code and is untrusted. The wrapper, in the pod's
   `sandbox` container, is granule's code. The client key and token are
   mounted only into that container. The guest reaches the wrapper only
   through vsock exec and virtio devices that granule defines, which
   expose no file of the wrapper's. A full guest compromise does not
   yield the credential.
2. **The guest has no route to the acceptor.** NetworkPolicy alone
   cannot express this. A policy selects pods, and with
   `CHV_NET_MODE=inline` the guest **is** the pod's IP and MAC, so the
   wrapper's and the guest's packets are indistinguishable to it. The
   design therefore enforces it inside the pod, where the guest cannot
   reach:
   - granule's wrapper installs a filter in the pod's network namespace,
     on the guest's tap or the inline datapath, that drops every
     guest-originated packet to `grain-access`'s ClusterIP and to the
     grain pod's port 4244. The wrapper's own dial-out leaves from the
     pod namespace's stack, not the guest's interface, so it is
     unaffected. This is a requirement on #1456. GKE-2 verifies it with
     a guest that tries to connect and must time out.
   - `network: none` guests have no NIC at all (`CHV_NET_MODE=none`), so
     they have no route by construction.
   - Even a guest that reached the port could not complete the TLS
     handshake. It lacks the client key, and the acceptor's listener
     requires a client certificate before any application byte.
   - The acceptor is on its own port and its own Service, which serves
     nothing else. Reaching it is useless without the key, and the key
     is not reachable from the guest.

   **Settled (GKE-2): the in-pod drop, no NAT mode.** #1456's inline
   mode keeps the two apart. granule's netshim installs TC drop filters
   on the guest's tap for every address in `NETSHIM_CONTROLLER_IPS`
   (IPv4, ARP, IPv6 and neighbour solicitation;
   `third_party/granule/pkg/netshim/splice.go`, `controllerDropRules`),
   and passes return traffic from those addresses up to the pod's own
   stack, where the wrapper's dial-out lives. The drop matches the
   address the guest sent to, before DNAT, so grain names the acceptor's
   addresses explicitly in `GRANULE_ACCESS_CONTROLLER_IPS`: the
   `grain-access` ClusterIP and the grain pod's IP. A dial name alone
   would not tell granule them. `scripts/sandbox-access-guest-probe.sh`
   verifies this on a real guest. The wrapper connects and runs execs
   over the channel. From inside the guest, a connection to the acceptor
   fails, and one to a control listener on a sibling address and the
   same port succeeds.
3. **Dial-out only.** Nothing dials a sandbox pod. Two NetworkPolicies
   apply:
   - **`grain-sandbox-egress`** selects every sandbox pod except
     `grain.dev/network: none` ones, which only
     `grain-sandbox-network-none` selects (policies are additive, so
     selecting them here would hand them the internet rule). Its egress
     allows `grain-sandbox`'s ports, `grain-access:4244`, DNS, and the
     internet for open-network tasks. It denies the metadata server
     (`169.254.169.254/32`), the node and pod CIDRs other than grain's,
     and the GKE control plane. Its ingress is empty.
   - **`grain-access-ingress`** selects the grain pod. Its ingress
     admits port 4244 only from sandbox pods (the
     `app.kubernetes.io/managed-by=grain`,
     `app.kubernetes.io/component=sandbox` labels `pkg/k8s` stamps on
     every one), the guest-facing ports only from the same pods, and
     port 8080 only from the Google load balancer ranges the Gateway's
     traffic and health checks come from. Nothing in the namespace
     reaches 8080: grain polls `grain-webhook`, not the other way round.
   - These policies are only objects on a cluster that does not
     enforce NetworkPolicy, so `scripts/deploy-gke.sh` refuses to adopt
     one that runs neither Dataplane V2 nor network policy enforcement.
   - The wrapper's egress requirement (4244) and the guest's (the
     proxies and the internet) are separate rules with separate
     comments, so a later change to one cannot widen the other without
     its own review. The in-pod filter in (2) is what actually separates
     them on the wire. The policies bound what the pod as a whole can
     reach.
   - `networkpolicy.yaml`'s `network: none` policy stays, with the
     acceptor port added to its single egress peer.
4. **Tokens are per sandbox.** There is one certificate and one token
   per sandbox, bound to its name, checked against grain's live set,
   and dead when the sandbox is reaped. Nothing is shared between
   sandboxes, and there is no deployment-wide sandbox secret. A
   compromised wrapper (not just a guest) can drive only its own
   sandbox's channel. That is no more than its own VM, which it already
   controls.

   **Residual risk:** the grain ServiceAccount gains
   `secrets: create, delete` in its namespace. Anyone who can act as
   grain could already create privileged pods there, so this adds no
   reach. It is stated so a reviewer does not have to discover it.
5. **hostd's credential can only do what hostd does.** A hostd token:
   - opens one hostd connection under its registered name;
   - answers requests grain makes on it (exec of allowlisted entries,
     staging under its own root, stdio streams);
   - opens MCP tunnel streams only for argvs grain issued to runs on
     **this** connection (`mcpGrants`, unchanged);
   - opens capability MCP streams only for routes grain registered on
     this connection.

   It is not an agent API token, a UI session, a git proxy credential,
   or a sandbox credential. The routes that accept it accept nothing
   else, and every other route refuses it (`accesscode_internal_test.go`'s
   route table grows the hostd routes). It is revocable from Settings
   with immediate disconnection.

   hostd still enforces its own allowlist on the host, unchanged, so a
   compromised grain gets at most what the host's root-owned allowlist
   lets it start. That is the same guarantee as today (and the same
   accepted non-goal about a CLI's own config,
   `docs/design/cli-run-location.md` §Security).
6. **Whose identity a run used is recorded.** Every run that used a
   hostd records the hostd's name, instance and owner. Every capability
   call served by a hostd records the same in the capability audit.
7. **The API server is a smaller target.** grain's `pods/exec` grant
   stays, for bootstrap and fallback, but it is now exercised rarely. A
   spike in `pods/exec` in the audit log becomes a signal rather than
   background noise.

## Migration

The live deployment moves in three steps. Each can be reversed on its
own, and no step runs two daemons at once.

### Step 1, on the cloudtop: the store moves to Cloud SQL (GKE-1)

1. Terraform the Cloud SQL instance (private IP plus a public IP
   reachable only through the Cloud SQL Auth Proxy), the IAM user, and
   the database.
2. Run the Cloud SQL Auth Proxy on the cloudtop (`docs/cloudsql-postgres.md`
   §1) under the cloudtop's own credentials.
3. Settings → Database → upgrade to Postgres with **migrate data**
   (`POST /api/database/upgrade`). This exists today. It copies the
   SQLite store across, switches `db-config.json`, and restarts.
4. The fence (advisory lock) ships in this stage, so it is live from the
   first Postgres boot.

**Rollback:** switch `db-config.json` back to SQLite and restore the
pre-upgrade store from the state repo dump (`staterepo.Import`), which
is the existing restore. Everything after that dump is lost, so the
rollback window is short, and the step is boring enough that it should
not need one.

### Step 2: prepare the cluster side, with no daemon running there (GKE-7)

1. Apply the overlay with `replicas: 0`: Secret Manager entries, Workload
   Identity, the PVC, Services, NetworkPolicies, the Gateway, IAP and
   DNS.
2. Copy the secrets key and bootstrap tokens into Secret Manager
   (`push-secrets.sh`).
3. Pre-seed the PVC from the cloudtop with a one-off Job that pulls a
   tarball from a GCS staging object:
   - transcripts and CI artifacts of the last 30 days, which are worth
     keeping;
   - the package cache's objects, optionally (they rebuild, but a warm
     cache saves an afternoon of misses);
   - **not** checkouts, agent HOMEs or the job cache, which rebuild.
4. Register the cloudtop's hostd as `bsalmon-cloudtop` (the deploy
   script, [Task #1461](/tasks/1461)) and restart it with dial-in
   enabled **alongside** the socket, pointed at the Gateway. One hostd
   process serves both. Until step 3.4 there is no daemon behind the
   Gateway, so the dial-in side retries quietly while the socket keeps
   serving the cloudtop daemon.

### Step 3: the cutover (GKE-8)

1. **Drain the cloudtop daemon.** Pause dispatch (the existing pause),
   and let running agent runs and CI runs finish, up to a window the
   operator picks (for example 30 m).
   - Anything still running at the end is stopped through the existing
     upgrade path.
   - Its agent runs are recovered as interrupted attempts and
     re-dispatched by the new daemon.
   - Its CI runs are stamped interrupted and re-run.
   - Sandbox pods are already in the cluster and are readopted.
2. **Force a final state-repo export** from the cloudtop daemon, then
   stop it (`systemctl stop grain-daemon`) and `disable` it.
   - Its advisory lock goes with its connection, and that is the moment
     the cluster daemon could take over.
   - Step 4 below needs that lock released, so a cloudtop daemon that
     somehow restarted would block the cluster daemon rather than run
     beside it.
3. **Final PVC sync** of transcripts and artifacts since step 2's copy.
4. **Scale the cluster Deployment to 1.** It takes the lock, opens the
   same Cloud SQL database, readopts sandbox pods by label, and
   reconnects the hostd (now dial-in only).
5. **Rewrite placements.** Today's `run: host`, `GRAIN_*_RUN=host` and
   Settings → Models "Host" become `run: hostd, hostd: bsalmon-cloudtop`.
   The deploy script (#1461) applies this through a state-repo pull
   request against grain/config, so the change is reviewed and recorded
   like any settings change.
6. **Flip sandbox access** to `-k8s-granule-access=dialout` once one
   canary task and one CI run pass on the channel.
7. **Unpause.** DNS for the UI now points at the Gateway. The cloudtop's
   old UI port is closed.

**Rollback**, at any point before step 7 or within the soak:

1. scale the cluster Deployment to 0 and wait for its lock to drop;
2. `systemctl enable --now grain-daemon` on the cloudtop. It is still
   configured for the same Cloud SQL database, so no data moves;
3. the placements rewrite is reverted by reverting its grain/config pull
   request;
4. hostd goes back to socket mode by restarting it without `-connect`.

Because the store never moved a second time, rollback loses nothing but
what was in flight. In-flight work is handled the same way as on any
daemon restart.

**Two daemons at once** is excluded by the fence, not by procedure. The
procedure keeps it from being tested.

### Migration of executors

Existing executor tokens become hostds through an explicit step, not
implicitly:

- GKE-5's schema migration creates a `hostd` row for every live executor
  token, named after its last-seen host name and owned by the token's
  creator.
- It marks each row `allowlist: legacy`. A legacy hostd may run the
  binaries it announced in its hello (today's behaviour) **for one
  release**. Settings → Hostds shows a banner on each one until its host
  runs `grain hostd migrate`, which writes an allowlist from those
  binaries and reconnects as a normal hostd.
- After that release, legacy hostds refuse every exec.
- `cfg.Token` (the shared executor token) is refused at startup with a
  message pointing at this section.

### What happens to in-flight tasks

| What | At the cutover |
|---|---|
| Queued tasks, parked tasks, open pull requests, merge queue | In the database. Unaffected. |
| Running agent runs | Drained. Any left are interrupted attempts, re-dispatched. |
| Running CI runs | Drained. Any left are interrupted, re-run (`Run.Interrupted`). |
| Sandbox pods | Already in the cluster. Readopted by the new daemon. |
| Persistent sandboxes | Readopted. Their next tool call goes over whichever access mode is set. |
| Host-run CLIs on the cloudtop's hostd | Drained. A detached process that outlives the drain belongs to the same hostd process that now also dials in, so the cluster grain re-attaches to it over that connection (protocol 2 attach). Its staged files stay in the cloudtop's data dir, where the socket path put them; new runs stage under hostd's own root. |
| Scheduled work | Resumes from the store on unpause. |

## What happens to the grit work

The grit split is **deferred, not abandoned**.

- **Kept, as merged:** GS-1's `ci.Runner`, `pkgcache.Store` and
  `pkgcache.Admin`, and the import-boundary test. GS-2's neutral
  packages (`pkg/workflows`, `pkg/treeid`, `pkg/attest`, `pkg/gritapi`),
  `grit serve`, `/v1`, `pkg/grit/...`, `pkg/grit/independence_test.go`,
  and the `grit/vX.Y.Z` tags. Nothing in this design deletes them, and
  no stage adds a grain-core import under `pkg/grit/...`.
- **Used by this design:** grain wires its CI through `ci.Runner`, as
  today, and the package cache through `pkgcache.Store` and
  `pkgcache.Admin`. Moving the cache's objects to GCS later is a new
  `Store`, not a refactor.
- **Not built now:** GS-3 (deploy grit), GS-4 (remote CI with shadow
  and attestations) and GS-5 (cut over CI to grit). Their tasks
  (#1331–#1333) are closed as deferred, with a link to this doc.
- **What a later split reuses from this design:**
  - **The granule dial-out channel and `pkg/sandboxaccess`.** It is
    neutral by construction, so grit's executor can drive job pods with
    it instead of the `grit pod` self-driving model. That makes
    controller-driven grit possible, and grit-split's *Reaching job
    pods* section shows why that was hard over `kubectl exec`.
  - **The in-cluster package cache**, already beside the sandboxes,
    which grit's `/pkg/v1` would front.
  - **Cloud SQL**, if the operator wants grit's run store there rather
    than in CNPG. One instance can hold a second database.
  - **The fence and Gateway pattern** for a second in-cluster service.
- `docs/design/grit-split.md` gets a status line saying so and linking
  here.

## Observability

- Metrics on the existing `/metrics`:
  - `grain_sandbox_channel_connected` (gauge, per backend);
  - `grain_sandbox_channel_reconnects_total`;
  - `grain_sandbox_channel_lost_total{kind=ci|agent}`;
  - `grain_sandbox_reattach_total{outcome}`;
  - `grain_hostd_connected{name}`;
  - `grain_hostd_parked_runs{name}`;
  - `grain_fence_held`.
- System → Sandboxes shows each sandbox's access mode, channel state and
  last reconnect. System → Hostds shows each hostd's connection,
  capacity in use, parked runs and identity.
- `pods/exec` calls in the API server audit log should fall to
  bootstrap only. GKE-8's soak checks that they do.

## Open questions (for the operator)

1. **Cloud SQL or in-cluster Postgres?** The recommendation is Cloud
   SQL, for the reasons in *The state store*, chiefly that the cloudtop
   can move onto it first and roll back without copying data. Also: the
   tier (`db-custom-2-8192` is the suggested start), and whether to use
   regional HA (about 2× cost) or zonal with PITR.
2. **DNS and IAP naming.**
   - The UI's hostname. The suggestion is
     `grain.<operator's domain>`, with a Google-managed certificate.
   - Whether hostd uses the same hostname (simpler) or a separate
     `hostd.<domain>` with its own IAP audience (which lets hostd's
     ID-token audience differ from the UI's).
   - Which OAuth client IAP uses.
   - Whether hostd authenticates to IAP with the user's own identity
     (programmatic IAP with a desktop OAuth client) or with a service
     account key on the cloudtop. The suggestion is the user's identity,
     which needs no key file.
3. **HA.** One fenced replica (this design), a warm standby waiting on
   the fence, or real multi-replica (which needs reconcile-loop
   sharding, and is out of scope). The suggestion is one fenced replica
   now. Revisit once restart reattach (GKE-4) has shown how long a
   restart actually costs.
4. **Reattach timeouts.** The suggested defaults are 2 m for the
   sandbox channel and 15 m for hostd. The 15 m for hostd assumes a
   cloudtop network blip or reboot. Is that right for this deployment?
5. **Region and project.** Is grain's Deployment in the same project
   and region as the sandbox node pool (the suggestion), and is Cloud
   SQL in the same region?

## The stages

GKE-1 to GKE-9 are dependency-ordered. Each is filed as its own task once
this doc merges, held for approval, with `depends_on` naming the stages
listed under it. Implementing commits carry
`Design-Item: docs/design/grain-on-gke.md#GKE-<n>`.

Three lines of work run in parallel and meet at the cutover:

- sandbox access: GKE-2 (after granule #1456) → GKE-3 → GKE-4;
- hostd: GKE-5 → GKE-6;
- deployment: GKE-1 → GKE-7.

GKE-8 waits for GKE-4, GKE-6, GKE-7 and #1461, and GKE-9 follows GKE-8.

### The fence, and this deployment's store on Cloud SQL {#GKE-1}

Depends on: nothing.

- Add the advisory-lock fence and the `daemon_lease` row (*High
  availability*): `-fence-wait`, exit on loss, and log and expose who
  holds it. Postgres only: a SQLite store is one file on one disk, and
  this stage does not change how it is guarded.
- Terraform the Cloud SQL instance, database, IAM user and backups,
  beside the existing `terraform` job.
- Document and run step 1 of *Migration* on this deployment: the
  cloudtop daemon moves to Cloud SQL through the Auth Proxy with
  `POST /api/database/upgrade`.
- Tests: two daemons against one Postgres (the second waits, then
  exits); the lock connection killed mid-run (dispatch stops and the
  process exits); the `postgres` CI job runs the fence tests.

### The sandbox-access acceptor and granule client {#GKE-2}

Depends on: gke-labs/granule [Task #1456](/tasks/1456) landing and
being vendored into `third_party/granule`.

- `pkg/sandboxaccess`: the acceptor, the deployment CA in Secret
  Manager, per-sandbox certificate and token minting into an
  owner-referenced Secret mounted into the wrapper container only, the
  admission checks, and the live-channel table. It is neutral: the
  independence test's allowlist gains it, and it imports no grain-core
  package.
- `K8sGranuleSandboxes.create` mints the credentials and sets the
  wrapper's dial-out env and mount. Pods are dial-out capable whatever
  the mode, so turning it on needs no new pods.
- Settle the inline-network question in *Security* (2) with granule
  (an in-pod drop, or a NAT mode), and verify it with a guest that must
  fail to connect to 4244.
- Role: `secrets: create, delete`. The NetworkPolicies
  `grain-sandbox-egress` and `grain-access-ingress`.
- Tests: the acceptor against granule's fake wrapper (admission,
  wrong sandbox's token, reaped sandbox, expired certificate, guest
  without a key).

### Runners on the channel, with reattach on a dropped channel {#GKE-3}

Depends on: GKE-2.

- `sandboxaccess.Runner` implementing `Run` and `RunStream`. Batched
  file put for `PlaceFile` and `ConfigureGitCredentials`. Channel
  forwards for `network: none` guests and capability ports. No
  `granule forward` for inline guests when grain is in-cluster.
- `-k8s-granule-access=dialout|kubectl` (default `kubectl`), refused
  off-cluster. `KubectlExecRunner` kept for bootstrap and diagnostics
  only, with no silent fallback.
- Reattach within `-k8s-granule-reattach-timeout`. `ErrChannelLost`
  leads to an `errored` job with an `infra: sandbox channel lost` fault
  and an automatic uncached re-run. Extend `infraverdict.go`'s re-run to
  every run's channel-lost jobs. Agent tool calls get a tool error and
  a sandbox rebuild.
- Tests: a channel cut mid-step reattaches with no gap or duplicate
  output; a cut beyond the timeout gives an `infra` verdict and one
  re-run; a channel that never connects fails `Acquire` with the
  wrapper's log in the error; `ci.Sandboxes` jobs run green over the
  channel with the fake wrapper.

### Reattach across a grain restart {#GKE-4}

Depends on: GKE-3.

- The `ci_step_attachment` table and cursor checkpoints. Startup
  reattach before recovery. `RecoverInterrupted` renamed to
  `RecoverUnattachable` and narrowed to runs with nothing to attach.
- The engine rebuilds a run's in-memory job state from the store.
- Tests: kill grain mid-step, restart, and the step continues and the
  run concludes with its real verdict; a step that finished while grain
  was down yields its real exit code; a sandbox gone while grain was
  down gives `infra` and a re-run; a run with no sandbox yet is still
  stamped interrupted.

### hostd and executors become one, dialling in {#GKE-5}

Depends on: nothing (it can proceed in parallel with GKE-2 to GKE-4).

- The `hostd` registry (name, owner, labels, token hash and revocation)
  and Settings → Hostds, which replaces Settings → Executors.
  `/api/hostd/connect`, `/api/hostd/install.sh`, with `/api/executors/*`
  as aliases for one release.
- **Identity from the token**: the hello's name must match the token's
  hostd, replacement only within one name, and the shared `cfg.Token`
  removed. This closes relay-hardening §3.
- Allowlist enforcement in the networked hostd using `pkg/hostd`'s
  checks. The `capacity:` and `labels:` allowlist blocks. Offers in the
  hello. Protocol 2's detached starts, attach and drain on the
  connection. Stdio capability streams.
- Staging into hostd's own root. `modelexec.RemoteHost` becomes
  `hostd.RemoteHost`, and `agent.HostdHost` stays for the socket.
- Routing by pin or label selector. Per-hostd capacity, and
  per-(connection, hostd) usage-limit pauses for ambient credentials.
- Disconnection: parked runs with a reason, re-attach within
  `-hostd-reattach-timeout`, `hostd-disconnected` notifications, and no
  implicit fallback.
- The hostd name, instance and owner recorded on runs and in the
  capability audit.
- The executor migration (legacy allowlist, `grain hostd migrate`).
- Tests: a token for hostd A claiming B's name is refused; rotation
  with two tokens; an allowlist refusal over the network matches the
  socket path; a disconnect parks, and a reconnect resumes and
  re-attaches a detached CLI; the access-route table in
  `accesscode_internal_test.go`.

### hostd placement for models and capability servers {#GKE-6}

Depends on: GKE-5.

- Settings → Models: **Run on** (`container`, `hostd <name>`,
  `hostd label`), **Credentials** (`stored`, `ambient`) and **Fallback**.
  Unavailable state while the hostd is disconnected.
- Settings → Capabilities: `run: hostd` with `hostd: <name>` or
  `hostd: {labels}`, named instances per hostd, grants that name the
  instance, and `needs-hostd:<name>` status.
- The hostd picker on both forms, showing identity. The task, run and
  audit views show the hostd that served them.
- Migration: `run: host`, `GRAIN_*_RUN=host` and Models "Host" map to
  the single registered hostd. The state-repo import maps them too, so
  an old grain/config still loads.
- UI screenshots per the repo's UI rule, and a Playwright spec for the
  picker and the unavailable state.

### The production overlay: Gateway, IAP, Workload Identity, secrets {#GKE-7}

Depends on: GKE-1.

- `deploy/kubernetes/gke/`: a kustomize overlay over
  `grain-granule.yaml` with the Cloud SQL connector, WI, Secret Manager
  CSI mounts, `Service/grain-sandbox` and `Service/grain-access`, the
  NetworkPolicies, the Gateway and HTTPRoutes, the `GCPBackendPolicy`
  for IAP and the hostd WebSocket timeout, and `grain-webhook`.
- Terraform for the GCP service account and bindings, the Secret
  Manager secrets, IAP, DNS and the certificate.
- Check the IAP assertion (`x-goog-iap-jwt-assertion`, with `pkg/jwks`
  and the backend's audience) on every UI and API request when
  `-iap-audience` is set.
- Enable the self-upgrade for `-k8s-granule-sandboxes` (lift
  `checkConfig`'s refusal; `stageKube` already flushes run hosts).
- Walk `docs/test-plans/2026-10-15.md` §9's unproved items on this
  overlay: the real image, a sandbox push through the Service, and
  `rollout restart` mid-run.
- Tests: `terraform validate` in the `terraform` job; kustomize build
  checked in CI; manifest lint that the sandbox ServiceAccount has no
  WI annotation and no automounted token.

### Cut over this deployment {#GKE-8}

Depends on: GKE-4, GKE-6, GKE-7, and the deploy script
[Task #1461](/tasks/1461).

- Steps 2 and 3 of *Migration*, as a runbook in `docs/hand-runs/` and
  as far as possible in #1461's script. The script registers the
  cloudtop as hostd `bsalmon-cloudtop`, installs it in dial-in mode,
  and rewrites placements through a grain/config pull request.
- Flip `-k8s-granule-access=dialout` after a canary task and CI run.
- A two-week soak:
  - `pods/exec` in the API server audit log at bootstrap only;
  - no `dial timeout, backstop` failures;
  - channel reconnects and `infra` re-runs tracked;
  - one deliberate `rollout restart` mid-CI-run that concludes with a
    real verdict.
- Rollback rehearsed once before the cutover, on a staging namespace.

### After the soak {#GKE-9}

Depends on: GKE-8.

- Remove the cloudtop daemon's systemd unit and data (keep one cold
  backup), and the `granule forward`-over-exec path for in-cluster
  grain.
- Make `dialout` the default for `-k8s-granule-sandboxes` when
  in-cluster. Keep `kubectl` for the off-cluster `gke-sandboxes`
  shape, which still has no address a pod can dial.
- Update `deploy/kubernetes/README.md`, `docs/k8s-granule-design.md`,
  `docs/design/cli-run-location.md` (hostd over the network) and
  `docs/non-disruptive-upgrade.md`.
- Remove the `/api/executors/*` aliases and the legacy-allowlist
  allowance.
