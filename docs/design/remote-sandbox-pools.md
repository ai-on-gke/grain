# Remote sandbox pools

Status: proposed. Nothing in this document is built yet.

Today a daemon runs every sandbox on one backend, chosen at startup.
`pkg/daemon/run.go` builds exactly one `orchestrator.Sandboxes` from the
`-container-sandboxes`, `-granule-sandboxes`, `-k8s-sandboxes`,
`-k8s-granule-sandboxes` or `-host-sandboxes` flag, and
`model.Config.SandboxBackend` (`pkg/model/sandbox_backend.go`) records
which one. The Kubernetes backends (`pkg/k8s`, `orchestrator.K8sSandboxes`,
`orchestrator.K8sGranuleSandboxes`, `docs/k8s-init-design.md`,
`docs/k8s-granule-design.md`) assume grain runs *in* the cluster:

- `pkg/k8s` authenticates as the pod's mounted ServiceAccount;
- sandboxes reach the git and model proxies at the daemon pod's IP or at
  `-k8s-sandbox-service`'s cluster IP (`EffectiveK8sGitProxyHost`,
  `AdvertiseViaK8sService`);
- `deploy/kubernetes/networkpolicy.yaml` lets a no-network sandbox reach
  only the grain pod.

This document is about a different setup. A deployment keeps its host
backend (granule or container sandboxes on the grain host) and also
attaches one or more GKE clusters as **remote sandbox pools**. Tasks and
CI jobs can then run in either place. The daemon stays on the host, and
everything it does to a cluster goes in from outside, under the host's
own GCP identity.

## Terms

- **Sandbox pool**: somewhere a sandbox can be built, with its own
  backend and its own budget. Every deployment has the implicit pool
  `host`, which is today's backend. A remote pool is a registered
  cluster plus a namespace.
- **Workload class**: what `model.Ledger` already calls a "pool"
  (`model.PoolAgent`, `model.PoolCI`). That name is taken, so this
  document only ever says "sandbox pool" for the new idea. In code the
  new key is `SandboxPool`, and `Ledger`'s existing `Pool` field keeps
  its meaning.

## Non-goals

- Running the daemon itself in the cluster. That is
  `docs/k8s-init-design.md`, and it is unchanged.
- Non-GKE clusters in the first version. The design keeps the
  credential source pluggable (D-2), but only GKE gets built.
- Autoscaling decisions. GKE's cluster autoscaler or Autopilot owns node
  count. Grain only caps what it asks for.

## Registration {#RSP-1}

A pool is a row in a new table, `sandbox_pool`. It is exported to the
state repo as `tables/sandbox_pool.json`, like `persistent_sandbox`
(`docs/persistent-sandboxes.md`):

| field | example | meaning |
|---|---|---|
| `name` | `gke-us1` | Unique. Used in `sandbox_pool` preferences. `host` is reserved. |
| `kind` | `gke` | The credential and endpoint source. Only `gke` exists at first. |
| `project`, `location`, `cluster` | `my-proj`, `us-central1`, `grain-pool` | The GKE cluster. `location` is a region or a zone. |
| `namespace` | `grain-sbx` | Where sandbox pods, the relay and per-task namespaces' parent RBAC go. |
| `backend` | `kubernetes` / `kubernetes-granule` | `model.SandboxBackendKubernetes` or `model.SandboxBackendKubernetesGranule`. |
| `budget` | `{cpus: 64, memoryMB: 262144, maxSandboxes: 40}` | The ceiling grain commits in this pool (RSP-3). |
| `overcommit` | `{cpu: 1.0, memory: 1.0}` | As `model.Budget`. Defaults to 1.0 for both, because pod requests are real reservations. |
| `identity` | `grain-pool-gke-us1@my-proj.iam.gserviceaccount.com` | The service account the daemon impersonates for this pool (RSP-2). Empty means it uses the host's ADC directly. |
| `image`, `nodeSelector`, `workspace` | | Per-pool versions of `-k8s-sandbox-image`, `-k8s-node-selector` and `-k8s-workspace`. Empty means grain's defaults. |
| `networkPolicy` | `enforced` / `unenforced` / `unknown` | Written by the attach probe (below), never typed in. It is the per-pool `NetworkPolicyEnforced`. |
| `state` | `enabled` / `draining` / `disabled` | `draining` places nothing new and lets live sandboxes finish. |
| `order` | `10` | Fallback order (RSP-3). |

There are no secrets in the row, so none can reach the export.
Credentials are always derived from the host's ambient identity at run
time (RSP-2). Runtime state (health, live commitment, last auth error)
is kept in memory and in the status API, and is never exported, the
same way the overcommit controller's live ratios are not
(`docs/design/auto-overcommit.md`). `grain state check` validates rows:
names are unique, the backend is known, the budget is non-zero, and
`host` is not used as a name.

**Where it shows up.** Settings → Sandboxes gets a "Sandbox pools"
section under today's backend selector. The selector now describes the
`host` pool and is otherwise unchanged. Each remote pool shows its row,
health (RSP-2), live/budget commitment and an Attach / Drain / Remove
control. System → Sandboxes groups live sandboxes by pool. The API is
`GET/PUT /api/sandbox-pools` and `POST /api/sandbox-pools/{name}/attach`,
and the CLI is `grain pool list|add|attach|drain|remove`. An agent with
an admin capability (`pkg/capability/admincaps`) registers a pool through
the same API. Because the row lives in grain/config, a pull request to
grain/config is also a valid way to add one.

**Attach** is the step that makes a row usable. It is idempotent and
runs again on every `attach` call and after every daemon start. It runs
under the host's ADC, not the narrower pool identity, because it is the
only step that needs to create things:

1. `container.projects.locations.clusters.get` reads the endpoint
   (preferring the DNS-based control-plane endpoint where it is enabled)
   and the cluster CA. It refuses a cluster whose control plane the host
   cannot reach, and says which endpoint it tried.
2. It creates the namespace, a `grain-sandbox` ServiceAccount with
   `automountServiceAccountToken: false` for sandbox pods, and a
   `grain-daemon` Role/RoleBinding for the pool identity: pods,
   pods/exec, pods/log, PVCs, services, networkpolicies, and
   serviceaccounts/token for RSP-7. It also creates the
   `roles/container.clusterViewer` IAM binding the identity needs to
   authenticate at all.
3. It applies the relay (RSP-4) and the pool's NetworkPolicies.
4. It probes. A plain pod has to schedule, and on `kubernetes-granule` a
   pod has to see `/dev/kvm` (`docs/k8s-granule-design.md` §2).
   A labelled no-network pod has to fail to reach the internet *and*
   reach the relay. The probe's result is written to `networkPolicy`.
   This turns the operator assertion that `-k8s-network-policy` stands
   for today into a measured one.

`grain pool create` is optional sugar. Through the same ambient
credential it creates a cluster suited to the backend: Autopilot for
`kubernetes`, and Standard with a nested-virtualization node pool for
`kubernetes-granule`. It then registers and attaches the cluster. It
uses `pkg/gcpsetup`'s pattern of one admin credential building the
clients.

## Auth from outside the cluster {#RSP-2}

**Root of trust.** The host's ambient GCP identity is the installing
user's `gcloud auth application-default login`. The daemon reaches it
through `grain hostd`, as `"run": "host"` capability servers already do
(`docs/capabilities.md`, `docs/gcp-key-permission-sets.md` "ambient").
The daemon may be containerised and cannot read the user's ADC itself.

**A new in-tree host server, `host-gke`.** It is started by hostd the
way `host-git` is (`docs/git-hosts.md`). It serves tools to the daemon,
not `mint_` grants to tasks:

- `cluster_info {project, location, cluster}` returns the endpoint and
  CA.
- `access_token {impersonate?}` returns an OAuth access token and its
  expiry. With `impersonate` set, it calls
  `iamcredentials.generateAccessToken` for the pool's `identity` using
  the user's ADC. That needs `roles/iam.serviceAccountTokenCreator` on
  the identity, which attach grants. Without it, the token is the ADC's
  own.
- `adc_status` reports whether the ADC refreshes, its account, and the
  error if it does not.

It runs on the host under the user's account, so the refresh token
never leaves the host. The daemon only ever holds access tokens, which
last at most an hour.

**How kubectl uses it.** `pkg/k8s.Client` gains `Kubeconfig` (and
`Context`) fields that are passed as `--kubeconfig`. For each remote
pool the daemon writes `<state-dir>/pools/<name>/kubeconfig` (mode
0600). It names the cluster's endpoint and CA, and has a `user.tokenFile`
pointing at `<state-dir>/pools/<name>/token`. A per-pool refresher in
the daemon asks `host-gke` for a new token when 40% of the current
token's lifetime remains, and replaces the file atomically. kubectl
re-reads `tokenFile` on every invocation, so the many short-lived
`kubectl` calls `pkg/k8s` and `mcp.KubectlExecRunner` already make pick
up a fresh token without changing anything else. Long-lived streams
(exec sessions, the relay tunnel) only authenticate when they start, so
a token expiring mid-stream does not cut them. This is chosen over a
`gke-gcloud-auth-plugin` exec entry because it needs neither gcloud nor
the plugin in the daemon's image, and because a plugin run per kubectl
call would make one hostd round trip per tool call.

**Least privilege.** The daemon's day-to-day identity in a pool is the
impersonated `identity` service account, bound by attach's Role to the
pool's namespace (and to the per-task namespaces of RSP-7). It is not
the operator's user, who is usually a project owner and so effectively
cluster-admin. A pool with an empty `identity` works but is flagged in
Settings as running with the operator's full rights.

**When ADC expires.** The user's refresh token can stop working: a
reauthentication policy, a password change, a revoked grant, or the
user running `gcloud auth application-default revoke`. Then:

1. The refresher's next `access_token` call fails. `host-gke` returns
   the ADC's own error. The pool's health becomes `auth-failing`, with
   the error and the time the current token expires.
2. Dispatch stops placing anything new in the pool (RSP-3 falls back).
   Live sandboxes keep working on the token already in the file, for up
   to its remaining lifetime of about 24 minutes or more. Refreshing at
   40% remaining is what buys that window.
3. The daemon raises a deployment-health finding
   (`pkg/capability/deploymenthealth`) and an inbox item: "Sandbox pool
   gke-us1 cannot authenticate: <error>. On the grain host, run
   `gcloud auth application-default login` as <user>." The same banner
   is shown on the pool in Settings.
4. When the token finally expires, live runs in the pool lose tool
   access. The orchestrator reports their next tool call as a transport
   failure, and they are retried like any run whose sandbox died. They
   are not marked failed by the task's own fault.
5. Recovery needs no action in grain. The refresher retries every
   minute with backoff to five minutes, and the first success clears the
   finding and reopens the pool.

`adc_status` is also polled every 15 minutes while the token is healthy.
Many reauthentication policies only show up at refresh, so this gives
the operator warning before the hour boundary that would otherwise
reveal it.

**Fallback without ambient credentials.** A host with no ADC (for
example, a headless VM with no user login) can instead set
`GOOGLE_APPLICATION_CREDENTIALS` for hostd to a service-account key or a
workload-identity-federation config. `host-gke` reads ADC the same way,
so nothing else changes. Grain does not ask an operator to paste a key
into its own secret store for this.

## Scheduling {#RSP-3}

**Preference.** `sandbox_pool` is a new optional field on a task, a
template, a repo's configuration and a CI workflow job (`sandbox_pool:`
next to the job's shape keys). Its values are a pool name, `host` or
`any`. The most specific setting wins: task, then template, then CI job,
then repo, then the deployment's `default_sandbox_pool` (default
`host`, which leaves today's behaviour unchanged). A companion
`sandbox_pool_strict: true` turns off fallback.

**Eligibility comes before preference.** A pool is only a candidate if
it can honour the run:

- its health is `ok` and its state is `enabled`;
- `NetworkNone` needs `networkPolicy: enforced`, or `kubernetes-granule`
  (whose isolation is the guest's missing NIC,
  `K8sGranuleSandboxes.IsolatesNetwork`);
- the run's shape fits a node in the pool (from the attach probe's node
  allocatable);
- the run needs nothing host-local. Self-debug mounts grain's source
  (`ContainerConfig.GrainSourceDir`), and a persistent sandbox with a
  `host_port` binds the host. Both pin a run to `host`. That rule lives
  in one function so a new host-bound feature has one place to declare
  it.

**Choosing.** Dispatch tries the preferred pool. If that pool is
ineligible or its budget refuses, and the preference is not strict, it
tries the other eligible pools in `order`, with `host` at order 0
unless configured otherwise. If none admit, the candidate waits. The
skip reason names every pool tried and why ("gke-us1: 63.5/64 CPUs
committed; host: memory"), which extends the wording
`dispatch`'s capacity skip already uses. A strict preference waits
rather than fall back, and says so.

A pod can be admitted by grain's budget and still sit `Unschedulable`
because the cluster is short of nodes and the autoscaler has not
caught up. `K8sConfig.UnschedulableTimeout` already turns that into an
Acquire failure. For a remote pool, that failure releases the charge
and dispatch retries the run on the next eligible pool *within the
same dispatch* before giving up. The pool is not marked unhealthy for
it, because one unschedulable pod is a capacity fact, not a fault.

**Budget and ledger become per pool.** `model.Limits.Budget` becomes
`Budgets map[string]model.Budget`, keyed by sandbox pool. `host` is
built exactly as today, from `withHostCapacity`, the overcommit
controller and the disk floor. A remote pool's `Budget.Host` is its
declared `budget`, and its overcommit comes from the row. The overcommit
controller does not apply to it, since it reads the grain host's PSI,
and `DiskFloor` is zero. `model.Ledger`'s key gains `SandboxPool`
alongside the existing workload-class `Pool`. `Live`, `LiveIn` and
`Count` take the sandbox pool, and every admission (`AdmitAs`,
`AdmitException`, `Charge`) names one. The counts in `Limits`
(`Workers`, `Mergers`, `CIJobs`) stay deployment-wide. They bound
agent processes and daemon work, which all run on the host whichever
pool the sandbox is in. A remote pool's `maxSandboxes` is an extra cap
on that pool.

**Repair reserve.** Each pool's `Budget` has its own `RepairReserve`.
`repairAdmission` (`pkg/dispatch/repair.go`) tries the same pool order
as an ordinary run, each with `AdmitsRepair` against that pool's live
commitment. Preemption (`o.preempt`) only happens inside the pool that
will take the repair, and only after every eligible pool's reserve has
refused. Stopping work in one pool cannot make room in another, so
that would be preemption for nothing. A remote pool's default reserve is
the same fraction as the host's (`DefaultReserveMemoryMB`'s rule applied
to its declared budget).

## Networking {#RSP-4}

**What a remote sandbox has to reach.** It needs the git proxy, the
model proxies, and the package cache where it is used. It does not need
the MCP tool server. That runs on the daemon side as
`grain mcpserver -k8s-pod` (`agent.KubePod.MCPServerArgs`) and reaches
*into* the pod with `kubectl exec`, so tools need no route from the
cluster to the host (RSP-5). The git and model proxies already
authenticate every request with a per-sandbox token
(`gitproxy` tokens refreshed by `keepSandboxTokenFresh`, and
`modelproxy.Endpoint.authenticate`). A route to them is not by itself a
credential.

**Options considered.**

1. *Expose an authenticated endpoint on the host.* Put the proxies
   behind TLS on an address the cluster can dial. That works only where
   the host is reachable from GCP, which a workstation or cloudtop behind
   NAT and corporate ingress usually is not. It also moves token-gated
   but otherwise unhardened listeners from a host-only bridge
   (`192.168.9.1`) onto a routable address, where every bug in them is
   reachable from the network.
2. *Sandboxes dial a tunnel endpoint on the host* (`pkg/tunnel` over
   WebSocket, as model machines dial `modelexec`). This has the same
   reachability problem as option 1, in the other direction.
3. *A per-cluster relay pod, connected by the daemon through the API
   server.* The daemon is the only side that dials, and it only dials
   the control-plane endpoint it already needs.

**Choice: option 3.** Attach installs `grain-relay` in the pool's
namespace: a one-replica Deployment running the grain image as
`grain relay`, and a ClusterIP Service in front of it. The relay has no
ServiceAccount token, no secrets and no egress. It listens on the
Service's ports: one per entry of `K8sServiceFrontedNames()` (the git
proxy and each model-proxy vendor), plus the package cache's
interception ports if the pool opts in.

The daemon keeps one `kubectl exec -i grain-relay-<hash> -- grain relay
-attach` per pool. Its stdin and stdout carry a `pkg/tunnel/mux`
session, the multiplexer `modelexec` already uses, with its
flow-control and abuse limits (`abuse_test.go`). When a sandbox
connects to the relay's port *P*, the relay opens a mux stream whose
only content is the listener index. The daemon maps that index to a
**fixed local destination**, the git proxy or a model proxy, and copies
bytes. The relay never names an address, so a compromised relay can
only reach the endpoints the pool was already given. The fabric
advertises the relay Service's cluster IP for those names to that
pool's sandboxes. This is `Direct.AdvertiseVia`, made per pool, which
`AdvertiseViaK8sService` already does for one cluster. Sandboxes
therefore see the same URLs and tokens as on the host.

If the exec session drops (API-server restart, network blip, daemon
restart), the relay holds new connections for up to 30 seconds waiting
for the tunnel to return before refusing them. A supervisor in the
daemon reconnects with backoff, and pool health shows `relay-down`
after 60 seconds. Throughput is bounded by the control plane's
streaming path. That is fine for clones of grain-sized repos and model
traffic, but large-repo pools may need a faster path later
(open question).

**Trust boundary.** The cluster is a less-trusted zone than the host.
Its nodes are shared with whatever else the project runs, and its
operators may differ from the host's. The design keeps every credential
that matters outside it:

- The cluster holds no grain secret. The relay has none. Sandbox pods
  get only their own git and model tokens, exactly as on the host, and
  a stolen token dies with its sandbox. Model-provider keys stay on the
  host behind the model proxy.
- The pool identity can only touch its namespace(s). Compromising the
  cluster gives no path to the host, because the host is never dialled
  from the cluster.
- The daemon's stream handler accepts only the fixed listener indices.
  It does not accept relay-chosen destinations, and does not reach
  hostd, the UI or the daemon API.

**NetworkPolicy.** Attach applies a pool-specific variant of
`deploy/kubernetes/networkpolicy.yaml`:

- `grain-sandbox-network-none`: pods labelled `grain.dev/network: none`
  have no ingress, and egress only to pods labelled
  `app.kubernetes.io/component: relay`, not `app.kubernetes.io/name:
  grain`. There is no grain pod in this cluster.
- `grain-relay`: ingress only from `app.kubernetes.io/component:
  sandbox` pods in the namespace, and no egress.
- `grain-sandbox-default`: network-enabled sandboxes get ordinary
  egress but **no ingress**. They also get no egress to the metadata
  server (`169.254.169.254/32`), so a sandbox cannot borrow the node's
  GCP identity. On GKE with Workload Identity the metadata server is
  already scoped to the pod's KSA, which has no binding. The policy is
  belt and braces for clusters without Workload Identity.

The attach probe (RSP-1) checks these policies and does not just assume
them.

## Tool transport {#RSP-5}

Tools keep the transport they have: `mcp.KubectlExecRunner`, with the
granule wrapper on `kubernetes-granule`, run by `grain mcpserver
-k8s-pod` with the pool's `--kubeconfig` added. Nothing in the agent
or MCP layers changes beyond passing the pool's kubeconfig path.

**Latency.** Each tool call is one `kubectl exec`: process start, a TLS
handshake to the remote control plane, the SPDY/WebSocket upgrade, and
the hop to the kubelet. In-cluster that costs about 50–100 ms. From a
host to a GKE regional endpoint it is about 150–400 ms, depending on
distance. An agent run makes hundreds of tool calls, so that adds
minutes per run. That is acceptable for a first version but worth
measuring. Phase RSP-17 adds a **persistent exec channel**: one
`kubectl exec -i … grain toolhost` per sandbox, carrying a mux session
over which commands, file reads and writes run as streams. That turns
the per-call cost into a stream open, and is the same pattern as the
relay. It is built only if the per-run metric (tool-call latency
p50/p95 by pool, added in RSP-11) shows it is needed.

**Reliability.** An exec stream can drop mid-command. Today that comes
back as an error from the runner. For remote pools:

- connection-level failures are classified separately (an API server
  that is unreachable, or a stream reset before an exit status) and
  surfaced to the agent as "transport lost; the command may or may not
  have run", never as the command's own failure;
- read-only tools (file reads, listings) retry once automatically;
- commands do not retry automatically.

A run whose pool health goes `auth-failing` past its token, or
`unreachable` for more than 5 minutes, is ended as a sandbox loss and
retried. That is the same path as a granule VM that died.

## Reaping and daemon restarts {#RSP-6}

**Labels.** Every remote pod carries the existing `ManagedByLabel` and
`SandboxLabel`, plus `grain.dev/deployment: <deployment id>` and
`grain.dev/pool: <name>`. `K8sSandboxes.ReapOrphans` today deletes
every labelled sandbox pod in the namespace that this process does not
hold, and leaves separation between deployments to the namespace. A
remote cluster is likely to be shared, so listing gains the deployment
selector, and a pod from another deployment is never touched even if an
operator points two pools at one namespace.

**Per pool.** The orchestrator's single `Sandboxes` becomes a
`PooledSandboxes` router. It holds one `Sandboxes` per pool and routes
`Acquire` by the chosen pool. Sandbox names carry the pool
(`<pool>/<pod>`), so `Release`, `Readopt` and the exec runner find their
pool with no lookup table. `ReapOrphans` runs per pool:

- At startup, after readoption (`K8sSandboxes.Readopt`,
  `docs/non-disruptive-upgrade.md`), each healthy pool reaps unclaimed
  pods. A pool that is unreachable or failing auth at startup is
  **not** reaped, and startup does not wait for it. Its reap runs when
  it first becomes healthy. Runs that were live there are given a
  readoption grace (10 minutes) before being retried elsewhere, so a
  daemon restart during an ADC lapse does not throw away work that is
  still running.
- Every 15 minutes, each healthy pool also sweeps pods with this
  deployment's label that are older than 10 minutes and that no ledger
  entry or building sandbox claims. This catches deletes that failed
  while a pool was unreachable. On the host that failure mode is rare
  enough that a startup reap is enough, and remotely it is not.

**What a restart does to remote sandboxes.** They keep running, since
they are in another cluster, and this is better than the host backends,
where a restart can take the backend with it. In-flight tool `exec`
streams die with the old daemon, as they do today. The relay tunnel
drops and the new daemon re-establishes it. The relay's 30-second hold
covers a fast restart, and a slower one shows up as a failed git or
model request that the client retries. Git-proxy tokens survive because
the token store is on disk (`pkg/gitproxy/tokens.go`). The refresher
restarts with the readopted run.

**Upgrades.** The relay runs the grain image at the daemon's version.
After an upgrade, re-attach rolls the relay Deployment to the new image.
The mux handshake carries a protocol version, and the daemon accepts
the previous version, so the order of rollout does not matter. Sandbox
pods keep their image until they end. A pool's sandbox image is
re-resolved per Acquire, as it is today.

**Removing a pool.** `drain` places nothing new and waits for live
sandboxes to finish. `remove` then reaps, deletes the relay and the
NetworkPolicies, and deletes the namespace only if attach created it.
Removing a row in grain/config that still has live sandboxes leaves the
pool draining until they finish. It does not orphan them.

## Handing a cluster to tasks: `k8s-cluster` {#RSP-7}

A new in-tree capability `k8s-cluster` grants a task a namespace-scoped
kubeconfig on a registered pool's cluster, for tasks that deploy to or
test against Kubernetes:

```yaml
capabilities:
  - capability: k8s-cluster
    with: {pool: gke-us1, role: edit, quota: {cpu: 8, memory: 16Gi}}
```

- **Mint.** It creates namespace `grain-t<task>-<attempt>` (labelled with
  the deployment and task) with a `ResourceQuota` and `LimitRange` from
  `quota`, and a `NetworkPolicy` that denies ingress from outside the
  namespace. In that namespace it creates a ServiceAccount, and a
  RoleBinding to the built-in `edit` or `view` ClusterRole. It then
  requests a bound token for it (`kubectl create token --duration=1h`).
  The kubeconfig, with the endpoint, CA and that token, is written to
  `$KUBECONFIG` in the sandbox. The pool identity has
  `create namespaces` (cluster scope, through a ClusterRole limited by an
  admission rule to names with the `grain-t` prefix) and `bind` on
  `edit`/`view`. That is the one cluster-scoped right attach grants, and
  only pools that enable `k8s-cluster` get it.
- **Refresh.** The capability refreshes the token every 30 minutes and
  rewrites the file through the sandbox's exec. This is the same
  lifecycle as `keepSandboxTokenFresh`.
- **Release.** It deletes the namespace, which revokes the token with
  its ServiceAccount. `with: {keep: true}` leaves the namespace for a
  human, for at most 24 hours, after which the pool sweep (RSP-6)
  deletes it.
- **Reachability.** The sandbox needs a route to the control plane. A
  sandbox in the same pool reaches the cluster on its internal endpoint.
  A host sandbox, or one in another pool, uses the public or DNS
  endpoint and needs network access. A `NetworkNone` run cannot hold
  this grant, and dispatch says so.
- **Budget.** A task's own workloads are bounded by the quota, not by
  the pool's sandbox budget. The quota's sum over live grants is shown
  next to the pool's budget in Settings.

This is separate from `gcp-key`. A task that also needs GCP APIs gets a
`gcp-key` grant minted by an ambient admin, as today
(`docs/gcp-key-permission-sets.md`). `k8s-cluster` hands out only
Kubernetes RBAC, so a task never needs a GCP identity just to use the
cluster.

## Phasing

Each phase is one pull request and can be tested on its own. The
`real Kubernetes sandboxes` kind job is currently out of
`.grain/workflows/tests.yml` (grain/task-161). RSP-8 brings back a
version of it as a separate, non-gating workflow, and it becomes gating
once it is green on main for a week. Phases marked *kind* add to that
workflow. The daemon runs **outside** the kind cluster with a
kubeconfig, which is exactly the remote-pool situation.

### P0: kind workflow for out-of-cluster tests {#RSP-8}

Bring back the real-Kubernetes job with the daemon outside the cluster.
Fix the cluster-creation failure that took it out, and run
`k8s_sandboxes_real_test.go` with `GRAIN_REAL_K8S_TESTS=1` against a
kubeconfig. *Test:* the workflow is green twice in a row.

### P1: pool table, API, export {#RSP-9}

Add the `sandbox_pool` schema, `GET/PUT /api/sandbox-pools`, the state
repo export and import, `statecheck` rules, and a read-only Settings
list. The implicit `host` pool appears in it. Nothing is scheduled
remotely yet. *Test:* round-trip export and import, and statecheck unit
tests.

### P2: `host-gke` and the token refresher {#RSP-10}

Add the `host-gke` server (with `cluster_info`, `access_token` with
impersonation, and `adc_status`), the per-pool kubeconfig with
`tokenFile`, the refresher, pool health, and the deployment-health
finding and inbox item for an ADC lapse. *Test:* run against a fake
token source (an expiring token, then a failing refresh, then
recovery). The `tokenFile` path is checked against kind using a
ServiceAccount token as the "GCP" token. A manual live test runs
against a real GKE cluster, gated on an environment variable like
`TestLiveMintUseRelease`.

### P3: `PooledSandboxes`, kubeconfig-aware `pkg/k8s`, per-pool ledger {#RSP-11}

Add `k8s.Client.Kubeconfig`, the `PooledSandboxes` router, pool-qualified
sandbox names, the ledger key `SandboxPool`, `Limits.Budgets`, deployment
labels, and the per-pool tool-latency metric. Scheduling stays
`host`-only except for an explicit task-level `sandbox_pool`. *Test:*
ledger and budget unit tests. *kind:* a task with
`sandbox_pool: kind-pool` runs its tools in a kind pod while the host
pool keeps working.

### P4: relay {#RSP-12}

Add `grain relay`, the mux-over-exec tunnel, fixed-destination mapping,
per-pool `AdvertiseVia`, the relay manifests and the NetworkPolicies.
*Test:* mux mapping unit tests, including a refused unknown index.
*kind (with Calico, as the old job installed):* a sandbox clones and
pushes through the relay; a no-network sandbox reaches the relay and
not the internet; killing the exec session mid-clone recovers within
the hold window.

### P5: scheduling {#RSP-13}

Add `sandbox_pool` on templates, repos and CI jobs, plus
`default_sandbox_pool`, strictness, eligibility, fallback order, the
Unschedulable retry on the next pool, per-pool repair reserves and
preemption, skip-reason wording, and the pool shown on task and CI rows.
*Test:* dispatch unit tests in `pkg/dispatch` with two fake pools,
covering fallback, strict waiting, an ineligible `NetworkNone` run, and
a repair admitted on the second pool's reserve.

### P6: attach, create, Settings controls {#RSP-14}

Add the attach steps and probes, `grain pool attach|drain|remove|create`,
and the Settings form and controls (with screenshots). *Test:* attach
against kind is idempotent when run twice, the probe correctly reports
`networkPolicy` with and without Calico, and drain then remove leaves
nothing labelled.

### P7: restart, reaping, upgrades {#RSP-15}

Add deferred reaping for unreachable pools, the readoption grace, the
periodic sweep, relay re-rollout on upgrade, and mux protocol
versioning. *kind:* restart the daemon mid-run, and the run continues
on the same pod. Make the pool unreachable at startup, and then
nothing is reaped until it returns.

### P8: `k8s-cluster` capability {#RSP-16}

Add mint, refresh and release, quota, `keep`, and the sweep of kept
namespaces. *kind:* a task applies a Deployment in its namespace,
cannot list another namespace, and its token stops working after
release.

### P9: persistent exec channel (only if P3's metric warrants it) {#RSP-17}

Add `grain toolhost` and a mux-backed `ExecRunner` for remote pools.
*Test:* a latency comparison in the kind workflow, and runner unit
tests for a dropped session.

## Open questions

These are raised as review comments on the pull request, each with a
recommended answer:

- Should the daemon impersonate a per-pool service account, or use the
  operator's ADC directly?
- Is the API server tunnel's throughput enough, or should a direct path
  be planned?
- Does `default_sandbox_pool` stay `host`, or should CI default to a
  remote pool?
- Should the host's package cache be relayed to remote pools?
- Should `k8s-cluster`'s cluster-scoped namespace creation be allowed,
  or should a task only get pre-created namespaces?
