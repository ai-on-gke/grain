# Moving CI (grit) and the package cache into the cluster

> **Superseded on 2026-10-27 by
> [`docs/design/grain-on-gke.md`](grain-on-gke.md) (updated 2026-10-31).**
> CI stays inside grain rather than splitting out into a separate service
> (and on 2026-10-31 the GKE controller cutover in `grain-on-gke.md` was
> also retired, keeping `grain daemon` on the host while sandboxes use
> GKE via `-platform=gke-sandboxes`). The grit split remains **deferred, not
> abandoned**: everything GS-1 and GS-2 merged (`ci.Runner`, the
> `pkgcache` interfaces, the neutral packages, `grit serve`, `/v1`,
> `pkg/gritapi`, `pkg/attest`, `pkg/treeid`, `pkg/grit/independence_test.go`
> and the `grit/vX.Y.Z` tags) stays. GS-3 to GS-5 (#1331–#1333) are not
> being built now. `grain-on-gke.md` §"What happens to the grit work"
> says what a later split would reuse. The rest of this document is kept
> as the record of that design.

Status (before it was superseded): proposed. Nothing in this document is built yet. Its requirements
are the stages at the end (GS-1 to GS-5). Task 1327 has already filed
each stage as a held task (GS-1 to GS-5 are #1329 to #1333), so the
design planner (`docs/design-docs.md`) should find them open when this
doc merges. GS-2 to GS-5 change in this revision, so it should supersede
or amend #1330 to #1333 rather than file new stages. Everything before
*The stages* records decisions and the reasons for them. Those sections
are not requirements, so their headings carry no IDs.

This doc replaces task 1324's draft (branch `grain/049f1e09/task-1324`,
head `cc64e32fd80d`). That draft recommended running jobs on Argo
Workflows. Its analysis of the `pkg/ci` boundary is reused here, checked
again against the current tree. Its Argo analysis is kept as a recorded
alternative (*Recorded alternative: Argo Workflows*).

**Amended after the first merge** (PR bwsalmon/grain#4969). The operator
made further decisions on task 1327 after it closed, and answered every
open question the first draft asked. They are numbered 4 to 12 below.
In short, they replace the first draft's choices in these places:

- grit no longer fetches from GitHub: grain hands it the tree (*Tree
  handoff*), so grit has no GitHub App;
- every run returns a signed attestation that grain verifies before
  anything counts as green (*Attestations*);
- grit is a separate service with its own binary, image, release, state
  and keys, and knows nothing about tasks (*Grit as an independent
  service*);
- grit has no secret store: grain passes the secrets each job needs
  with the request (*Secrets and trust*);
- grain's own sandboxes stay off the cluster package cache, so the
  pass-through, its fallback and its local tier are gone (*Package
  cache*);
- the run store is in-cluster Postgres, and grit runs on the existing
  `kubernetes-granule` cluster and node pool (*Storage*, *Budget and
  scheduling*);
- the model-proxy capabilities reach CI jobs through a reverse tunnel
  over grain's outbound connection (*The model-proxy relay*).

## Decisions already made

The operator made these decisions. This doc works within them and does
not reopen them:

1. **CI moves out of grain into "grit", in the remote GKE cluster.**
   Grit is grain's own CI engine (today's `pkg/ci`), moved into the
   cluster. It is not Argo, Tekton, ARC, Buildkite or Cloud Build.
2. **Grain keeps everything else**, on a single node with the local
   `granule` backend. That node is the cloudtop itself
   (a 96 vCPU workstation with about 236 GB RAM), and its size is
   the whole agent and chat budget. Grain keeps:
   - tasks, agents and chats, and their sandboxes;
   - the merge queue, base-branch gates, repair filing and auto-merge
     decisions;
   - the UI;
   - the `grain-ci` capability;
   - every secret and every secret policy.
3. **The package cache (`pkg/pkgcache`) and the Actions cache move into
   the cluster with grit, and serve grit's CI job pods only.** Grain's
   local sandboxes keep the package access they have today. (The first
   draft had grain's sandboxes read the cluster cache through a
   pass-through on the grain host. That is no longer the plan; it is
   recorded as a possible later step under *Package cache*.)
4. **Grain hands grit the full tree to test.** Grit never fetches from
   GitHub (*Tree handoff*).
5. **Grit returns a signed proof of work for every run**, an in-toto
   Statement in a DSSE envelope, and grain verifies it before anything
   counts as green (*Attestations*).
6. **Grit is completely independent of grain at runtime.** It has its
   own deployment, state, storage, keys and releases, and grain is one
   client. The code stays in this repository, with a separate binary,
   release and image. A CI import lint keeps grit's packages off
   grain-only ones. Moving grit to its own repository is a possible
   later step (*Grit as an independent service*).
7. **Authentication is layered and swappable; authorization is inside
   grit.** Grain always dials out, and events come back on the same
   connection. IAP is allowed in `grain-sandbox-dev`. mTLS is kept only
   as a recorded alternative (*Connectivity and auth*).
8. **Secrets are passed with each request, and grain decides which
   ones.** Grit has no secret store and no secret policy. CI jobs get
   the same temporary credentials grain would give a sandbox, minted by
   grain and scoped per job (*Secrets and trust*).
9. **Model-proxy capabilities reach CI jobs through a relay in the
   cluster**, carried back to grain's own model proxies over a reverse
   tunnel on grain's outbound connection (*The model-proxy relay*).
10. **Grit's run store is in-cluster Postgres** run by CloudNativePG, not
    Cloud SQL. Grit lives in the same GKE cluster grain uses today for its
    `kubernetes-granule` sandboxes, in `grain-sandbox-dev`, in its own
    namespaces `grit` and `grit-jobs`, on the **existing node pool with no
    autoscaling** for now (*Storage*, *Budget and scheduling*).
11. **Operations:** two grit replicas with a lease; the old CI history
    stays in grain with no backfill, and grit's job cache starts cold;
    the Go build cache is split into trusted and untrusted namespaces;
    the cluster cache pulls from Docker Hub anonymously (there is no
    account).
12. **The `kubernetes-granule` agent backend is demoted, not deleted,
    after the cutover:** the code stays, documented as not recommended
    across networks, and it is turned off on this deployment.

## Why

On this deployment grain's daemon runs on a cloudtop
(a single workstation). It runs every sandbox on a remote GKE cluster
through the `kubernetes-granule` backend
(`orchestrator.K8sGranuleSandboxes`). Every interaction with a sandbox
goes through the API server's exec and port-forward path:

- `kubectl exec <pod> -- granule exec -- <argv>` for setup writes, tool
  calls, CI steps and capability placement;
- `granule forward` over `kubectl exec -i` for the git proxy, the
  package cache and the model proxy.

That path keeps failing with
`error dialing backend: dial timeout, backstop`.

Where it breaks matters for the fix. The error comes from the control
plane's connection *to a kubelet*. Exec, attach, port-forward and
`kubectl logs` all use that connection. Create, get, list, watch and
delete do not, because they go between the API server and etcd. Today
grain uses the fragile half for everything that touches a sandbox.

Most of the cluster's capacity, bandwidth and churn is CI. A full
`tests.yml` suite is about 20 jobs. Each job is a privileged pod booting
a cloud-hypervisor VM, and every package it installs crosses the WAN
twice: pod, then `kubectl exec` forward, then the cloudtop's cache, then
back. A grain restart also kills every CI job in flight:
`StoredRunStore.RecoverInterrupted` marks them `Interrupted` on the next
start.

Moving CI and the package cache next to the job pods fixes all three:

- job traffic stays on the pod network;
- package bytes stay inside GCP;
- grain's restarts stop reaching CI.

Grain, now on a single node with local `granule`, no longer touches the
cluster's exec path at all.

## Terms

- **grit**: the CI service. Today's `pkg/ci` engine, run as a service.
  It runs in the cluster in remote mode and in-process in embedded mode.
- **Client**: anything that calls grit's API with its own credential.
  Grain is one client. Grit records the client on every run and in every
  attestation, and authorizes each call by the client's scopes.
- **Remote mode**: grain talks to a grit deployment over HTTPS, through
  `gritapi.Client`.
- **Embedded mode**: grit's service runs inside the grain process,
  behind the same interface (`grit.Local`). This is single-box mode and
  `grain demo`, and it is today's behaviour.
- **Tree**: the full set of files a run tests, named by its tree
  identity (*Tree handoff*). A run may also name the commit the tree came
  from, but grit treats that as a label.
- **Attestation**: the signed in-toto Statement grit returns for a run
  (*Attestations*).
- **Mirror**: grain's local copy of grit's run records. It lives in the
  `ci_run`/`ci_job`/`ci_step` tables grain already has, and every reader
  in grain reads it.
- **Pod agent**: `grit-pod`, a small binary in each CI job pod. It boots
  the guest, runs the job's steps in it, and pushes events to grit over
  the pod network.
- **Model-proxy relay**: a ClusterIP service in `grit` that job pods
  call for model APIs, and that forwards each call to grain's model
  proxy over a reverse stream on grain's outbound connection.

## Non-goals

- Moving agents, chats or their sandboxes into the cluster. That would
  keep the exec path this design removes.
- Adopting an external CI system now (decision 1). Argo and Google
  Cloud Build are recorded below as possible later backends behind
  grit's execution interface.
- Putting grain's own sandboxes on the cluster package cache (decision
  3).
- Moving grit into a repository of its own. It is a possible later step,
  which the import lint keeps cheap.
- Changing what `.grain/workflows` means. A workflow that passes today
  passes on grit, with the same verdict and the same job-cache key.
- Returning to GitHub's checks. This deployment runs `ci_mode: grain` on
  purpose.

## What exists today

This section was checked against the tree at `e4afa8747`. Where the
brief's numbers differ, the measured ones are given.

- **`pkg/ci` is 34.8k lines.** 17.8k of them are non-test code, in 40
  files (39 in `pkg/ci`, one in `pkg/ci/cli`). It holds:
  - the engine (`engine.go`, `queue.go`, `plan.go`);
  - the gate and budget (`gate.go`);
  - the job executor (`job_executor.go`, `sandbox.go`, `live.go`,
    `collect.go`);
  - the run stores (`runstore.go`, `runstore_stored.go`);
  - the job cache (`cache.go`, `dircache.go`, `guestcache.go`);
  - artifacts (`artifacts.go`, `dirartifacts.go`);
  - quarantine (`quarantine.go`);
  - shared faults (`sharedfault.go`, `sharedfaultreport.go`);
  - release secrets (`release_secrets.go`);
  - the base gate (`basegate.go`) and cron (`cron.go`).
- **`ci.Service`** (`pkg/ci/service.go`) exports `Start`, `Get`, `List`,
  `Find`, `Cancel`, `MarkFlake`, `CacheStats`, `ClearCache`,
  `EvictBaseTransitionFailures`, `ClearArtifacts`, `ArtifactStats`,
  `RunArtifacts`, `OpenArtifact`, `WithArtifacts` and `WorkflowOptions`.
  - `daemon.LiveCI` (`pkg/daemon/ci.go:63`, an
    `atomic.Pointer[ci.Service]`) holds it.
  - `daemon.BuildCIService` (`pkg/daemon/ci.go:2236`) builds it.
- **65 non-test files outside `pkg/ci` import `pkg/ci`** (the brief said
  about 50). By package:

  | Package | Files |
  |---|---|
  | `pkg/orchestrator` | 18 |
  | `pkg/mcp` | 13 |
  | `pkg/ui` | 12 |
  | `pkg/daemon` | 9 |
  | `pkg/repodecl` | 4 |
  | `pkg/demo` | 4 |
  | `pkg/cli` | 3 |
  | `pkg/namespace` | 1 |
  | `pkg/cifault` | 1 |

  Most of these use `pkg/ci` as a **library**, not as a service: types,
  `Parse`, `PlanRun`, `RequiredFor`, `AttributeRun`,
  `RecentSharedFaults`. Only 8 files hold the concrete `*ci.Service`:
  - `daemon/ci.go` and `daemon/ci_capability_pass.go`;
  - `demo/demo_fakeci.go` and `demo/demo_observability.go`;
  - `orchestrator/basebranchci.go`, `orchestrator/basebranchrelease.go`
    and `orchestrator/recheck.go`, through type assertions;
  - `ui/cistorage.go` and `ui/repo_release_workflows.go`.

  A large group reads `ci.RunStore` directly rather than going through
  the service: `pkg/mcp`, `pkg/namespace`, `pkg/ui/cifault.go` and
  `pkg/orchestrator`.
- **Narrow interfaces already exist:**
  - `orchestrator.CIRunner` (`Start`, `Get`, `Cancel`), with
    `CIRunLister` and `CICacheEvictor` beside it;
  - `ui.CIRunner` and `ui.CIArtifacts`.
- **The engine reaches machines through two small interfaces.** One is
  `ci.Sandboxes` (`Acquire`). The other is `ci.Sandbox` (`Exec`,
  `Release`, and `ExecStream` on `StreamingSandbox`), in
  `pkg/ci/sandbox.go`. `daemon.CiSandboxes` implements them on grain's
  `orchestrator.Sandboxes`, so CI jobs:
  - are charged to the same `model.Ledger` as agents;
  - clone through grain's git proxy with a minted sandbox token;
  - get capability material from grain's registry
    (`CiGrantReservations`).
- **The package cache** is `pkg/pkgcache`, 16k lines of which about 7k
  are non-test. `pkg/daemon/packagecache.go` wires it. One HTTP
  service serves:
  - apt, as an HTTP proxy;
  - PyPI at `/pypi/`, npm at `/npm/` and Go modules at `/go/`;
  - Docker Hub as a registry mirror at `/v2/`;
  - GitHub release downloads and the Playwright CDNs;
  - the Go build cache at `/gocache/` (a GOCACHEPROG backend).

  It also has a DNS interceptor (`dns.go`) and transparent TLS
  interception with its own CA (`ca.go`), so a client with no proxy
  setting is still cached. It stores only immutable, content-named
  objects. It never stores indexes (`InRelease`, simple-index pages,
  manifests, packuments, `@v/list`): those are forwarded every time. It
  enforces read-only access for `networkAccess: none` sandboxes
  (`restrict.go`). Sandboxes reach it on the address they reach the
  grain host at. On this deployment that is `http://127.0.0.1:9729`
  inside the guest (`hostinit.DefaultGKESandboxesPackageCacheURL`).
- **The Actions cache** (`pkg/actionscache`, `pkg/daemon/actionscache.go`)
  is used only by CI. Its only consumers are:
  - `daemon.CiActionsCacheScope` and `BuildCIService`'s `ActionsCache`
    hook, which serve `.github/workflows` jobs run under act;
  - the settings form (`ui/settings.go`) and the CLI wiring that starts
    it.

  No agent sandbox path sets `ACTIONS_CACHE_URL`. So it moves with grit.
- **`pkg/grit` and `cmd/grit`** (380 lines) run one GitHub Actions job
  under `act` in a disposable VM. `pkg/ci/workflow.go` and
  `pkg/daemon/ci.go` use `pkg/grit.ActScript` to run `.github/workflows`
  jobs in a grain sandbox.
- **Merge-queue batch commits are already on GitHub.** No
  `refs/grain/batch/*` ref exists. `startMergeBatchOf` creates or
  force-resets one *branch* per base, `grain-merge-queue/<deployment>/<base>`
  (`model.MergeBatchRef`), through the GitHub API, and merges the batch
  members into it there. Task branches reach GitHub when an agent pushes
  through grain's git proxy. CI is started only for pushed commits.
- **`grain-ci` is a grant, not an executor.** The brief describes it as
  CI inside an agent's own sandbox, but `pkg/capability/grainci` mints
  nothing and places nothing. It records that grain CI judges a task's
  branch, and it registers the read-only CI tools. It stays in grain
  unchanged, and grit never sees it.
- **Workflows on this repository ask for these capabilities:**
  - required: `gemini-key`, `github-sandbox`, `package-publish` (3
    jobs) and `gcp-key`;
  - optional: `image-signing`, `claude-proxy` and `openai-proxy`.

## Architecture

```
 cloudtop (single node, 96 vCPU)                GKE cluster, grain-sandbox-dev (existing node pool)
 ┌───────────────────────────────┐              ┌──────────────────────────────────────────────┐
 │ grain daemon                  │ one HTTPS    │ namespace grit                               │
 │   orchestrator, merge queue,  │ session out  │   Gateway (TLS, IAP) ─▶ grit API (2 replicas)│
 │   UI, MCP, mirror (ci_run...) │ ───────────▶ │    ├─ engine, gate, job cache, tree store    │
 │   secret store and policy     │ /v1 calls,   │    ├─ attestation signer (key in Cloud KMS)  │
 │   attestation verifier        │ tree packs,  │    ├─ pkgcache, Actions cache (job pods only)│
 │   gritapi.Client, outbox      │ events back, │    ├─ git server (stored trees for jobs)     │
 │   model proxies ◀── reverse   │ reverse      │    ├─ model-proxy relay (ClusterIP)          │
 │    (claude, openai, gemini)   │ streams      │    └─ grit-ingest (ClusterIP only)           │
 │ local granule sandboxes       │              │ CloudNativePG cluster (run store)            │
 │   (agents, chats, grain/config│              │ namespace grit-jobs (ResourceQuota)          │
 │    checks); package access    │              │   job pod = grit-pod + granule guest         │
 │    unchanged                  │              │    ─ pod network ─▶ ingest, cache, git, relay │
 └───────────────────────────────┘              │ GCS bucket (cache, logs, artifacts, trees,   │
                                                │   packages, attestations, Postgres backups)  │
                                                └──────────────────────────────────────────────┘
```

All traffic between the two sides is opened by grain. Nothing in the
cluster dials the cloudtop. Requests flow up grain's connection; events,
and the model calls CI jobs make, come back down the same connection.

## Grit as an independent service

Grit knows trees, workflows, jobs, results and attestations. It knows
nothing about tasks, pull requests, the merge queue, repairs or
auto-merge.

- **Attribution is opaque.** A run carries a `labels` map from its
  client (grain sends `taskId`, `requestedBy`, `mergeBatch`, `baseGate`).
  Grit stores it, returns it, filters on it, and copies it into the
  attestation. It never interprets it.
- **Its own everything:** deployment (`deploy/grit/`), Postgres, GCS
  bucket, signing and encryption keys, release cadence and version
  number. Grain is one client among possibly several. A grit outage
  costs grain verdicts, never correctness, and a grain outage costs grit
  nothing.
- **Same repository, separate artifact.** The code stays in
  bwsalmon/grain. `cmd/grit` is its own binary, built into its own image
  (`ghcr.io/bwsalmon/grit`), and released under its own tags
  (`grit/vX.Y.Z`). Grain's release does not ship grit and grain's
  self-upgrade never touches it.
- **A possible later step** is moving grit and the neutral packages into
  their own repository. The import lint is what keeps that a `git mv`
  rather than a refactor. Nothing in this plan depends on it.

### Neutral packages

The code both sides need moves into packages with no grain-only
dependency:

| Package | Contents | From |
|---|---|---|
| `pkg/granule` | the granule driver (`run`, `ready`, `exec` over vsock) | already neutral; checked by the lint |
| `pkg/workflows` | the `.grain/workflows` parser and planner: `Parse`, `PlanRun`, `Request`, `Plan`, the job-cache fingerprints and path filters, `RemovedGateJobs`, quarantine parsing, cron translation, and the run, job and step result types | `pkg/ci`'s `workflow.go`, `yaml.go`, `actions_needs.go`, `plan.go`, `fingerprint.go`, `paths.go`, `filter.go`, `requirements.go` (the pure half), `basegate.go` (the pure half), `quarantine.go`, `cron.go`, `ci.go`, `results.go` |
| `pkg/treeid` | tree identity, the canonical archive and pack helpers (*Tree handoff*) | new |
| `pkg/attest` | the in-toto Statement and predicate types, DSSE signing and verification, key-set fetching (on top of `pkg/jwks`) | new |
| `pkg/gritapi` | the `/v1` wire types and `gritapi.Client`, including the session, retries and the event follower (the outbox itself is a grain table and stays in grain) | new |

`pkg/ci` stays, as **grain's** side of CI: the mirror store, the router,
attribution to tasks, shared faults, test history, failure summaries,
the release-secret binding and the verdict computation (`RequiredFor`
and the merge rules). It re-exports the moved types as aliases during
GS-2, so the 65 importing files move over in their own time.

### The import lint

A test in the `lint` job, `pkg/grit/independence_test.go`, runs
`go list -deps ./cmd/grit ./pkg/grit/...` and fails if any dependency
under `github.com/ai-on-gke/grain/` is outside an allowlist:

- `pkg/grit/...` and `cmd/grit`;
- the neutral packages above;
- leaf utilities with no grain state: `pkg/tunnel/...`, `pkg/jwks`,
  `pkg/redact`, `pkg/version`, `pkg/pkgcache`, `pkg/actionscache`.

It also fails if a neutral package imports `pkg/model`, `pkg/orchestrator`,
`pkg/daemon`, `pkg/ci`, `pkg/ui`, `pkg/mcp` or `pkg/capability`. An
allowlist rather than a denylist means a new grain package is off-limits
by default. Adding a package to the list is a reviewed change to the
test, in the style of `no_capabilities_test.go`. The direction grain to
grit is allowed: `pkg/daemon` links `pkg/grit` for embedded mode.

## The boundary

The rule is simple:

- Anything that needs tasks, pull requests, the merge queue, a person,
  or a secret's value at rest stays in grain.
- Anything that needs job machines, the job cache, or the run record of
  record moves to grit.
- Library code that both sides need moves to the neutral packages, with
  no I/O beyond what the package is for.

The import lint above keeps grit off grain. An import-boundary test in
grain (GS-1) keeps grain's packages off the execution packages, with one
exception: the embedded-mode wiring in `pkg/daemon`.

### `pkg/ci`, file by file

**Moves to `pkg/workflows`, the neutral library both sides use** (*Neutral
packages*):

- `ci.go` and `results.go`: the types (`Run`, `JobRun`, `StepRun`,
  `Conclusion`, `Event`, `Priority`, `CancelSource`), less the
  task-shaped fields, which become `labels`.
- `plan.go` (`Request`, `Plan`, `PlanRun`), `workflow.go`, `yaml.go` and
  `actions_needs.go`.
- `fingerprint.go` and `paths.go`: the job-cache keys. Both sides
  compute them and show them.
- `requirements.go`'s pure half (`Granted`, `Refusal`) and
  `basegate.go`'s pure half (`RemovedGateJobs`).
- `filter.go`.
- `quarantine.go` (parse and apply).
- `cron.go`: schedule translation. Grain's scheduler still fires
  schedules, so a scheduled run is just a `Start`.
- The `cache.go` interface and the `storagebounds.go` types.

**Stays in `pkg/ci`, as grain's side of CI:**

- `runstore.go` (the `RunStore` interface and `MemoryRunStore`) and
  `runstore_stored.go`: the mirror. Grit has its own store
  (`pkg/grit/store`), because `runstore_stored.go` sits on `pkg/model`,
  which grit may not import. The two agree on the wire types, not on
  tables.
- `requirements.go`'s `RequiredFor`, and the merge rules.
- `attribution.go`, `sharedfault.go`, `sharedfaultreport.go`,
  `diagnostics.go`, `failure_summary_store.go`, `gotest_logs.go` and
  `test_history.go`.
- `release_secrets.go`: the names, their validation, **and the binding
  of secrets to jobs**. Deciding which job of which run gets which
  release secret is secret policy, and secret policy is grain's alone
  (*Secrets and trust*).
- The `store.go` interface.

**Moves to grit** (`pkg/grit/...`, still compiled into grain for
embedded mode):

| File | Destination |
|---|---|
| `service.go` | `pkg/grit`: `Service`, the implementation behind both `grit.Local` and the `/v1` server |
| `engine.go`, `queue.go`, `job_executor.go` | `pkg/grit/engine` |
| `gate.go` (budget, priority, age boost) | `pkg/grit/engine` |
| `sandbox.go`, `live.go`, `collect.go` (the step loop and output collection) | `pkg/grit/steps`. The embedded engine and `grit-pod` run the same code |
| `cache.go` implementations, `dircache.go`, `guestcache.go` | `pkg/grit/cache`: `DirCache` for embedded mode, plus a new `GCSCache` |
| `artifacts.go` implementation, `dirartifacts.go` | `pkg/grit/artifacts`: `DirArtifacts`, plus a new `GCSArtifacts` |
| `basegate.go`'s `checkBaseGate` | `pkg/grit`. It runs at `Start`, against the base tree grain sends (*Tree handoff*) |
| `service.go`'s `vouchForCommit` and `checkReleaseRef` | **Grain**, not grit: they decide whether a run is a trusted release, which is secret policy. Grit only checks that the client holds the `release` scope |

`pkg/ci/cli` stays. It speaks to grain's HTTP API, which still answers.

### Outside `pkg/ci`

| Where | What happens |
|---|---|
| `pkg/daemon/ci.go` | Split. `CiSandboxes`, `CiWorkflowSource`, `CiJobCache`, `CiArtifactStore`, `CiScarceCapability` and `CiActionsCacheScope` move to grit's wiring, and stay in grain only for embedded mode. `CiGrantReservations` stays in grain in both modes: it is where grain mints each job's credentials, which in remote mode go into the request's sealed secrets (*Secrets and trust*). `LiveCI` becomes an `atomic.Pointer[ci.Runner]`. `CiGate` wraps the interface. `CiRepoCapabilities` and `CiRepoReleaseSecrets` stay grain's and feed the per-request secrets; nothing about them is pushed to grit. `CiMergeQueuePriority` becomes priority pushes. |
| `pkg/daemon/ci_statecheck.go` | Stays in grain. `grain/config` is controller-local with no forge to fetch from, so its checks always run embedded (*Requests that stay in grain*). |
| `pkg/daemon/ci_preempt.go`, `ciJobStuck` | Embedded mode only. In remote mode agents and CI no longer share one ledger, so the deadlock these exist for cannot happen. |
| `pkg/daemon/ci_capability_pass.go`, `ci_policy.go` | Stay in grain. They decide which jobs get which secrets, in both modes. |
| `pkg/daemon/actionscache.go`, `pkg/actionscache` | Move to grit. Job pods reach the Actions cache as a ClusterIP Service, and its storage moves to GCS. |
| `pkg/daemon/packagecache.go`, `pkg/pkgcache` | Unchanged for grain: its in-process cache keeps serving grain's own sandboxes. A second instance of `pkg/pkgcache`, built into grit's image, serves grit's job pods in the cluster (*Package cache*). |
| `pkg/daemon/servers.go` | Stays. Its `StoredRunStore` becomes the mirror's store. |
| `pkg/orchestrator` (`mergebatch.go`, `recheck.go`, `basebranchci.go`, `basebranchdispatch.go`, `basebranchrelease.go`, `baserepair*.go`, `basecistreak.go`, `infraverdict.go`, `scheduledworkflow.go`, `standingwork.go`, `taskpause.go`, `sync.go`, `mergerules.go`, `cistarvation.go`, `persistent_refresh.go`, `ci.go`) | Stays. Each file moves from `*ci.Service` to `ci.Runner` (GS-1) and reads runs through `Get`/`Find`, which the mirror answers. In remote mode a job counts towards a verdict only once its attestation has verified (*Attestations*). |
| `pkg/mcp` (`wait_for_checks`, `pull_request_status`, `run_ci_job`, `list_ci_runs`, `get_ci_log`, artifacts, faults, quarantine, test history) | Stays and reads the mirror. `get_ci_log` reads through to grit for output past the mirror's bound. |
| `pkg/ui` (CI pages, `cifault.go`, `cistorage.go`, `basebranchgate.go`, `repo_release_workflows.go`, `repo_workflows.go`, settings) | Stays. Storage stats and clearing call through to grit. Release secrets stay in grain's encrypted store, as today. |
| `pkg/cifault`, `pkg/namespace`, `pkg/repodecl`, `pkg/cli`, `pkg/demo` | Stay as library users. `pkg/demo` keeps its fake CI behind `ci.Runner`. |
| `pkg/capability/grainci` | Stays unchanged. |

### `pkg/grit` and `cmd/grit`

**Grit absorbs `pkg/grit`**, so the name means one thing:

- The act runner moves unchanged to `pkg/grit/actions` (`ActScript`,
  `RunnerImages`). It has three callers.
- `pkg/grit` becomes the service.
- `cmd/grit` becomes the service binary, with these subcommands:
  - `grit serve` runs the API, the engine and the event log;
  - `grit act` is today's one-job Actions runner, and the old
    flag-only form still works when the first argument is a flag;
  - `grit pod` is the pod agent, baked into the job image;
  - `grit verify` checks an attestation against the published keys;
  - `grit client` manages clients, their scopes and quotas.

Giving the service a new name instead would leave two CI-ish runners in
the tree, one of them called grit and not being grit.

### What does not split cleanly

- **Verdict reuse keyed on base commits.** A job-cache entry records the
  base commit a failure was seen against. The engine discards a cached
  failure whose base differs (`DirCache.PurgeNegativeEntries`). Grit owns
  the cache, so the lookup and the discard happen in grit. *Knowing* that
  a base branch moved from red to green is grain's job
  (`basebranchci.go`). So `EvictBaseTransitionFailures` becomes an RPC
  that grain calls at that transition, with the same answer shape
  `(removed, ok, err)`. It goes through the outbox (*Network
  partition*): the eviction is an optimisation, and a late one only
  costs a cache miss.
- **Flakes and rechecks.**
  - The *mechanism* is grit's. `Request.Recheck` runs only the named jobs
    and bypasses the cache. `MarkFlake` stamps `JobRun.Flake`, so
    `QueryTestHistory` reads a flake rather than a failure.
  - The *decision* is grain's: `recheck.go`, and the base confirmations
    in `basebranchci.go`. Deciding needs the merge queue and the state
    of the base branch.
  - `MarkFlake` becomes an RPC, and its result reaches the mirror as a
    `job.updated` event, so test history and metrics agree with grit.
- **Shared-fault attribution to tasks.** `ci.RecentSharedFaults` is
  pure over a list of runs. Grain runs it over the mirror, which holds
  every run, because grain starts every run. Mapping a fault to a task
  (`ci.TaskIDFromRef`) and to an open PR that fixes it (`pkg/cifault`)
  needs grain's tasks and PRs, so that stays in grain unchanged. Grit
  computes no faults.
- **Quarantine.** Today the engine `cat`s `.grain/quarantine.yml` out of
  the job's own sandbox, and only on the paths `trustboundary` guards.
  Grit reads the same file from the **base tree** grain sends (trust
  rules never come from the submitted tree, *Tree handoff*), at plan
  time, and applies it to job conclusions. *Whether* a change to that
  file may merge is still grain's decision (*Secrets and trust*). The
  quarantine MCP tool keeps parsing with `workflows.LoadQuarantine`.
- **Release runs and promote.**
  - Grain alone decides *that* a release or promote run happens, and
    that it is trusted: `basebranchrelease.go`, the release workflows
    page, the scheduler, `checkReleaseRef` and `vouchForCommit` all stay
    in grain.
  - Grain sends `event: release` (or `workflow_dispatch`) on `Start`.
    Grit accepts a release-event run only from a client holding the
    `release` scope for that repository (*Authorization in grit*). It
    does not re-check refs or commits: it has no view of either, and
    the secrets that make a release dangerous come from grain anyway.
  - A promote run that writes to GitHub does so with credentials grain
    minted for that job and passed in the request (*Secrets and
    trust*).
- **Merge-queue batch CI and rechecks.**
  - The batch is built on GitHub, as today. Grain fetches the batch
    commit through its own git proxy and hands grit its tree like any
    other (*Tree handoff*). Grit gives batches no special handling.
  - Grain calls `Start` with `labels.mergeBatch` set, which grit only
    stores.
  - A batch whose rank changes while it queues gets a
    `POST /v1/runs/{id}:priority`. Today `CiMergeQueuePriority` is read
    on every admission attempt, which a network round trip cannot do.
    Grain pushes rank changes instead, and grit re-ranks pending jobs.
  - Bisecting and recheck decisions stay in `mergebatch.go` and
    `recheck.go`, and their starts and flake marks are ordinary API
    calls.
- **Capability grants.** `CiGrantReservations` keeps minting a
  capability's material at admission from grain's registry, in both
  modes. In remote mode the material goes into the request, sealed to
  grit's encryption key and addressed to the job that needs it
  (*Secrets and trust*). Grit holds no material of its own. The
  model-proxy capabilities (`claude-proxy`, `openai-proxy`,
  `gemini-proxy`) reach jobs through the model-proxy relay (*The
  model-proxy relay*). Until GS-4 lands it, workflows that ask for them
  run embedded.
- **Requests that stay in grain.** Remote mode is therefore a *router*,
  not a switch. It sends two kinds of request to the embedded service:
  - `grain/config` checks;
  - until the relay lands in GS-4, workflows that ask for a model-proxy
    capability.

  Everything else goes to grit. Both write into the same mirror. The
  routing rule lives in one function, so the UI can say which executor
  ran a run (the mirror's `origin` column).
- **Live cost.** CI job sandboxes are metered into grain's
  `orchestrator.MeterFor` rows today. In remote mode each job's shape and
  timestamps arrive in events, and the existing live-cost computation
  runs over the mirror.

## The API

The API is HTTPS with JSON bodies, under `/v1`. One Go interface,
`ci.Runner` (GS-1), carries `ci.Service`'s whole exported method set.
It has two implementations:

- `grit.Local`, the in-process service;
- `gritapi.Client`, this API.

Both pass one contract suite (GS-2). The types on the wire are
`pkg/workflows`' and `pkg/gritapi`'s, and grain's `pkg/ci` aliases them,
so the mirror needs no translation.

Every route is authorized by the calling client's scopes (*Authorization
in grit*), shown in the last column.

### Routes

| Route | Body / query | Answer | Replaces | Scope |
|---|---|---|---|---|
| `GET /v1/info` | | `{api: "grit.v1", minor, build, epoch, features[], settings, keys}` | | any client |
| `POST /v1/trees:negotiate` | `{repo, want: {gitTree, sha256}, have[]}` | `{present: bool, haves[]}`: whether grit already holds the tree, and which of the offered commits it holds | | `submit` |
| `PUT /v1/trees/{sha256}` | a git pack (*Tree handoff*) | `201`, or `200` if already held | | `submit` |
| `POST /v1/runs` | `StartRun` (below) | `201` with a new run, or `200` with the run already in flight for the same plan or `requestId` | `Start` | `submit`; `release` for release-event runs |
| `GET /v1/runs/{id}` | | the run, or `404` | `Get` | `read` |
| `GET /v1/runs` | `repo, tree, commit, event, state, since, label.<k>, sort, order, limit, pageToken` | a page plus `nextPageToken` | `Find`, `List` | `read` |
| `POST /v1/runs/{id}:cancel` | `{reason, by}` | `{cancelled: bool}` | `Cancel` | `cancel` |
| `POST /v1/runs/{id}:mark-flake` | `{jobs[], rechecks}` | `{marked: bool}` | `MarkFlake` | `submit` |
| `POST /v1/runs/{id}:priority` | `{priority, rank}` | `204` | `CiMergeQueuePriority` | `submit` |
| `GET /v1/runs/{id}/attestation` | | the DSSE envelope (*Attestations*) | | `read` |
| `GET /v1/runs/{id}/jobs/{job}/attempts/{n}/steps/{s}/log` | `offset, limit, follow` | bytes; `Grit-Log-Complete: true` once the step has ended | step output past the mirror's bound | `read` |
| `GET /v1/runs/{id}/artifacts` | | artifacts, each with its sha256 | `RunArtifacts`, `WithArtifacts` | `read` |
| `GET /v1/artifacts/{ref}` | `Range` | bytes, streamed from GCS through grit | `OpenArtifact` | `read` |
| `GET /v1/artifacts:stats`, `POST /v1/artifacts:clear` | | stats; `{removed, bytes}` | `ArtifactStats`, `ClearArtifacts` | `read`; `cache-admin` |
| `GET /v1/cache:stats`, `POST /v1/cache:clear` | `{kind?}` | stats; `{removed}` | `CacheStats`, `ClearCache` | `read`; `cache-admin` |
| `POST /v1/cache:evict-base-failures` | `{repo, baseTree}` | `{removed, ok}` | `EvictBaseTransitionFailures` | `cache-admin` |
| `POST /v1/workflow-options` | `{repo, tree, event}` | workflow options | `WorkflowOptions` | `read` |
| `GET /v1/capacity` | | queue depth by priority class, pending and running pods, node-pool size, oldest pending, the client's quota use | System page, `ci_queueing` causes | `read` |
| `GET /v1/events` | `after, wait` | see *Event stream* | | `read` |
| `GET /v1/session` | WebSocket upgrade | the multiplexed session (*Connectivity and auth*) | | any client |
| `GET /.well-known/grit-keys.json` | | the signing and encryption key sets (*Key management*) | | unauthenticated |

The first draft's `PUT /v1/repos/{owner}/{name}/policy` and its
release-secret routes are gone. Grit stores no policy and no secret:
everything grain's policy decides arrives on the request it applies to.

The package cache's own routes (stats and flush) are in *Package cache*.
They sit behind the same Gateway, under `/pkg/v1`, and need the
`cache-admin` scope.

`StartRun` is the wire half of `ci.Request`, plus what grain used to set
in-process:

```json
{
  "requestId": "grain:<deployment>:<sha256 of the normalized request>",
  "repo": "bwsalmon/grain",
  "tree": {"gitTree": "<sha1 or sha256 tree id>", "sha256": "<canonical archive digest>"},
  "commit": "<sha, optional label>", "ref": "grain-merge-queue/049f1e09/main",
  "event": "pull_request",
  "baseTree": {"gitTree": "…", "sha256": "…"}, "baseCommit": "<sha>",
  "workflow": "", "jobs": [], "jobFilter": "",
  "gateOnBase": true, "recheck": false,
  "priority": "normal", "rank": 0,
  "deployedImages": [], "lifecycleImage": "",
  "labels": {"taskId": "1327", "requestedBy": "merge-queue",
             "mergeBatch": "mb-…", "baseGate": ""},
  "secrets": {"kid": "grit-enc-2026-10", "sealed": "<base64 HPKE ciphertext>"},
  "secretManifest": [{"job": "release/publish", "names": [
      {"name": "package-publish", "version": "inst-tok-…", "kind": "minted", "expires": "…"}]}]
}
```

- `tree` and `baseTree` must already be held (`trees:negotiate`, then
  `PUT /v1/trees`). A run naming a tree grit does not hold is refused
  with `409 tree-missing`, and the client uploads and retries.
- The workflows to run are read from `tree`. The trust rules (the
  required-job set the base gate compares against, the quarantine file,
  which workflows are declared) are read from `baseTree`, never from
  `tree` (*Tree handoff*).
- `secrets` is one sealed blob holding every job's material, and
  `secretManifest` is its plaintext table of contents: job, name,
  version, never a value (*Secrets and trust*). Grit refuses a request
  whose decrypted contents do not match the manifest.
- `labels` is opaque. Grit stores and echoes it and copies it into the
  attestation.
- The fields that were `json:"-"` on `ci.Request` (`Capabilities`,
  `ReleaseSecrets`, `TrustedRelease`, `TrustedSchedule`,
  `ReleaseRefVouched`) are gone from the wire entirely. Grain uses them
  to decide what goes into `secrets`; grit never sees them.

### Event stream

`GET /v1/events?after=<cursor>&wait=30s` is a long poll. It answers at
once if events are waiting, and otherwise after up to `wait`:

```json
{"epoch": "g-7d1e…", "cursor": "184467", "events": [
  {"seq": "184466", "v": 1, "type": "job.updated", "run": "r-3f9c2a",
   "job": {…the whole ci.JobRun, step output bounded…}},
  {"seq": "184467", "v": 1, "type": "run.updated", "run": "r-3f9c2a",
   "runState": {…the ci.Run without jobs…}}
]}
```

- **Level, not edge.** Every event carries the whole current state of
  the run or job it names, so applying one is an upsert. Applying one
  twice or out of order is harmless: each record carries a `revision`,
  and an older revision never overwrites a newer one.
- **Event types:**
  - `run.created` and `run.updated`;
  - `job.updated`;
  - `job.output`: live output, at most every 5s per step, as
    `ci.LiveOutputInterval`;
  - `run.deleted`, for retention;
  - `run.attested`, carrying the run's DSSE envelope once it is signed.
- **Durable cursor.** Events are rows in grit's Postgres (`grit_event`,
  `seq bigserial`), written in the same transaction as the state they
  describe, and kept for 7 days. Grain stores `(epoch, cursor)` in its
  own database in the same transaction as the mirror writes for that
  batch. So a grain crash mid-batch replays the batch, and replay is
  harmless.
- **Gaps.** A cursor older than retention gets `410 Gone`. So does an
  `epoch` that is not grit's current one, as after a restored or
  replaced database. Grain then resyncs:
  1. It pages
     `GET /v1/runs?since=<oldest unfinished run in the mirror>&sort=updated`.
  2. It upserts everything it gets.
  3. It resumes from the cursor the resync's first page carried.

  It raises `grit-resync` so the operator knows.
- **Events come back on grain's own connection.** The cloudtop accepts
  no inbound connections, so there are no webhooks. Grain holds the long
  poll open as a stream on its multiplexed session (*Connectivity and
  auth*), so events arrive as soon as they are written; over plain HTTPS
  the same route works as an ordinary long poll.

**Neither side loses a verdict when the other restarts:**

- *Grit restarts or fails over.* Job pods keep running, because their
  steps are driven by `grit-pod` inside the pod, not by grit
  (*Reaching job pods*). `grit-pod` spools its events to an `emptyDir`
  and retries ingest, so output from the gap arrives late but complete.
  On start, the new leader:
  - lists `grit-jobs` pods by label;
  - reconciles each pod with its job record;
  - writes events for whatever changed while it was down.

  Grain's long poll gets connection errors, backs off, and resumes from
  its cursor.
- *Grain restarts.* Grit notices nothing. Grain reads its stored cursor
  and catches up. Remote runs are not marked `Interrupted`, because
  `RecoverInterrupted` applies only to runs whose `origin` is `local`.
- *Both restart.* The same, because both sides keep their state on disk.

### Versioning and skew

- **Major version** is in the path (`/v1`). A breaking change becomes
  `/v2`, and grit serves both for at least one grain release cycle.
- **Minor version** is in `GET /v1/info` and the `Grit-API-Minor` header.
  Minors are additive only: new fields, routes and event types. Both
  sides ignore unknown fields and unknown event types, and log them once.
- **Builds.** Grain sends `Grain-Build` on every call. Grit records it on
  the runs grain starts, beside grit's own build, so `ci.Build` says which
  grit build ran a job.
- **Feature checks.** Grain reads `/v1/info` on start and every 5
  minutes.
  - A missing major refuses remote mode, and the System page says so.
  - A missing feature turns that feature off. For example, without
    `cache:evict-base-failures`, grain lets the cache entries expire
    instead.
  - It never fails a run.
- **Job-cache keys** must not include grit's build. They may include
  the `grit-pod` step-semantics version, which is bumped only when step
  semantics change, so a grit upgrade does not empty the cache.

## Tree handoff

**Grain hands grit the full tree to test. Grit never fetches from GitHub
and holds no GitHub credential.** This replaces the first draft's
"GitHub is the only transport" and grit's read-only GitHub App.

Grain already has every tree it tests: task branches arrive through its
git proxy, and merge-queue batches, base branches, release refs and
schedules are refs it can fetch through the same proxy. `grain/config`
is a tree like any other, so in principle its checks could run on grit
too; they stay embedded for now because they are tiny and need
controller-local state.

### Tree identity

A tree is named by two digests, both computed from the tree's content
alone, never from a commit, a time or a path on disk:

- **`gitTree`**: the git tree object id, in the repository's object
  format (SHA-1 today, SHA-256 when a repository moves). It is what git
  tools show, and what grain and grit use to dedupe objects.
- **`sha256`**: the sha256 of the tree's **canonical archive**. This is
  the collision-resistant identity, and it is the attestation's subject
  digest. `pkg/treeid` defines the canonical archive: a POSIX ustar
  stream of every entry in the tree, sorted by path bytes, with mode
  `0644`, `0755`, `120000` (symlink) or directory as git records it,
  mtime 0, uid and gid 0, empty user and group names, and pax headers
  only for paths longer than ustar allows. Submodules are recorded as a
  zero-length entry naming the gitlink commit and are not expanded.
  `git archive` is not used, because it stamps the current time on a
  bare tree.

Both sides compute both digests from the git objects, and grit refuses a
pack whose tree does not hash to what the request named. The commit, when
there is one, is carried beside the tree as a label: it lets a human and
`git` find the tree, and the attestation names it, but nothing in grit
depends on it. Merge-queue batch commits get no special handling; a
batch is a tree like any other.

### Transfer

- **Format: a git pack**, as `git pack-objects --thin` produces it, of
  the objects reachable from the commit (or from the bare tree) minus
  those grit already holds. A pack is git's own deduplicating format, so
  grain needs no new code to produce it and grit can index it with
  `git index-pack --fix-thin`. It carries history as well as the tree,
  so a job that runs `git log` or `git describe` still works.
- **Negotiation.** Before uploading, grain calls
  `POST /v1/trees:negotiate` with the tree it wants tested and up to 32
  recent commits it believes grit holds (the base commit, the last tested
  commit of the same branch, the merge-queue base). Grit answers which it
  holds, and whether it already holds the tree. Grain then packs only the
  difference. A tree grit already holds costs one round trip and no
  upload, which is the common case for rechecks, base confirmations and
  merge batches built from tested branches.
- **Over grain's outbound connection.** The pack goes up grain's session
  as `PUT /v1/trees/{sha256}`, streamed, never through a third store.
- **Storage.** Grit keeps one bare repository per client and repository
  on a PD-backed volume, mirrored to GCS (`trees/`) for recovery.
  Trees are kept 30 days after their last run, and objects reachable
  from a tree in use are never pruned.
- **Size limits.** 2 GiB for the first pack of a repository, 256 MiB for
  any later pack, 4 GiB of objects per repository and client in total,
  and a canonical archive of at most 1 GiB. These are per-client
  settings in grit. Exceeding one answers `413` with the limit, and grain
  concludes the run `errored` as infrastructure, never red.
- **Checkout in the job.** `grit-pod` clones the tree from grit's own
  read-only git server (`pkg/gitproxy` in a mode that serves grit's
  stored repositories instead of proxying a forge), at the address the
  guest uses today (`127.0.0.1:9728`), with a per-job token naming the
  one repository. Then it checks out the requested tree.

### Trust rules come from the base tree

A workflow file in the submitted tree decides *what* a job runs, as
today, so an agent's branch can change its own CI. It never decides what
the run is *allowed* to do, or which jobs are required:

- The required-job set the base gate compares against
  (`GateOnBase` → `workflows.RemovedGateJobs`), the declared workflow
  set, and the quarantine file are all read from **`baseTree`**, the base
  branch's tree, which grain sends with the run.
- **How grit gets the base copy.** It is an ordinary tree upload: grain
  negotiates and packs `baseTree` exactly as it does `tree`, and in
  practice it is almost always already held, because every base-branch
  run uploaded it. Grit checks the base pack against both digests, like
  any tree.
- **How it is verified.** Grit cannot check that `baseTree` really is
  the base branch's tree, since it never talks to GitHub, and it does not
  need to: grain is the policy owner and the only party that knows what
  the base is. Instead the attestation records `baseTree` and
  `baseCommit` in its predicate, and grain's verifier checks that they
  equal the base it meant before accepting the result (*Attestations*).
  A lying client can only mislead grit about its own runs.
- Which secrets a job gets is decided in grain from grain's own copy of
  the base workflows (*Secrets and trust*), so a head tree cannot ask for
  more.

**Rejected: grit fetching from GitHub with its own App** (the first
draft). It made grit a GitHub client, gave it a credential to rotate and
guard, failed on GitHub's "not our ref" window right after a push, and
could not test a tree that is not on a forge. Handing over the tree
removes all four.

## Connectivity and auth

The cloudtop and the cluster share no L3 network, and the cloudtop
accepts no inbound connections. So **grain always dials grit, and grit
never dials grain.** Anything grit has to say to grain, or ask of it,
comes back on a connection grain opened.

### The session

Grain keeps one long-lived **session** to grit: `GET /v1/session`,
upgraded to a WebSocket (`pkg/tunnel/wsupgrade`, because Google's front
end passes only WebSocket upgrades), with `pkg/tunnel/mux` running over
it. This is the same pairing `pkg/modelexec` already uses for remote
executors dialing grain, reused with the roles reversed. On the session:

- grain opens streams for API calls, tree uploads and the event long
  poll;
- **grit** opens streams back to grain for the model-proxy relay (*The
  model-proxy relay*). Grain answers only the stream types it offered
  when it opened the session (`model-proxy`), and refuses anything else.

Every route also works as plain HTTPS without the session, so `curl`,
other clients and the contract tests need nothing special. Only the
reverse streams need the session. Grain keeps two sessions open (one to
each grit replica, through the load balancer) and reconnects with
backoff.

### Authentication, in layers

Each layer can be swapped without touching the others:

1. **Transport:** a GKE Gateway (`gke-l7-global-external-managed`) with
   a Google-managed certificate. Its DNS name follows the convention
   `deploy/terraform/gcp/dns.tf` already uses:
   `grit-<a>-<b>-<c>-<d>.sslip.io` from the Gateway's reserved global
   address, or `grit.<zone>` when a Cloud DNS managed zone is configured.
   This deployment uses the sslip.io name until it has a zone. Grain pins
   the name and checks `/v1/info`'s `epoch`, which may change only
   through a 410 resync.
2. **Client credential, inside grit:** every client has a grit client
   token, a 256-bit random secret that grit stores only as a hash, sent
   as `Grit-Client: <id>.<secret>`. Grit's authorization (below) keys on
   the client this resolves to, whatever sits in front. GS-2 builds this
   layer, so an off-cluster `grit serve` and the contract tests are
   authenticated the same way production is. The token lives in grain's
   secret store, is rotated by issuing a second token and revoking the
   first, and never reaches a job. HMAC request signing over the same
   secret (`Grit-Signature: t=…,v1=…` over method, path, body digest and
   time) is an option for clients behind a TLS-terminating proxy they do
   not trust; grain does not need it, because nothing terminates TLS
   between grain and Google's front end.
3. **Edge identity: IAP** on the Gateway's backend services, which the
   operator has allowed in `grain-sandbox-dev` (GS-3). Grain's
   identity is a Google service account, `grain-client@<project>`. The
   cloudtop's ADC impersonates it to mint a Google-signed ID token, with
   the IAP OAuth client as audience, sent as `Authorization: Bearer` and
   renewed at 50 minutes. Grit verifies the `x-goog-iap-jwt-assertion`
   header with `pkg/jwks` (signature, `aud`, expiry), and requires the
   IAP principal to match the one recorded for the client token. A leaked
   client token alone is therefore useless from outside, and revoking the
   impersonation grant cuts grain off without touching grit.

A NetworkPolicy lets only the Gateway's proxy ranges reach the API port.
`grit-ingest`, the git server and the model-proxy relay are ClusterIP
only and have no route from outside. Nothing secret goes into the state
repository.

**Recorded alternative: mTLS** through a `ServerTLSPolicy` and a
Certificate Authority Service CA, with a 30-day client certificate grain
renews at 20 days. It is not needed now that IAP is allowed, and is kept
only as the swap for layer 3 if IAP ever is not.

### Authorization in grit

Grit decides what each client may do. Grain configures nothing in grit
at runtime; clients and their scopes are grit's own configuration
(`grit client add|scope|revoke`, stored in grit's Postgres).

- **Scopes, per client and per repository** (`bwsalmon/grain`, or `*`):
  - `submit`: upload trees, start runs, mark flakes, push priorities;
  - `read`: runs, logs, artifacts, attestations, stats, events;
  - `cancel`: cancel runs, the client's own by default and any run with
    `cancel:any`;
  - `cache-admin`: clear the job cache and artifacts, evict base
    failures, flush the package cache;
  - `release`: start runs whose event is `release` or
    `workflow_dispatch`, which are the runs that get publishing
    credentials and get `grit-release` priority.
- **Quotas and rate limits, per client:** concurrent running jobs,
  pending jobs, job-hours per day, tree bytes stored, upload bytes per
  hour, and requests per second (token bucket). A request over a limit is
  answered `429` with `Retry-After`. A run over its client's job quota
  queues, with cause `quota`, rather than failing.
- Grain is configured with every scope on its repositories. The
  operator's own principal gets `read` only, for debugging with `curl`.

### Network partition

- **Mutations go through an outbox.** Cancel, mark-flake, priority
  and cache-eviction calls are written to a grain table
  (`grit_outbox`) and sent in order with exponential backoff, capped at
  1 minute. Each is idempotent:
  - cancelling a finished run answers `false`;
  - a repeated flake mark is a no-op;
  - a priority push carries the rank's own version, and an older one is
    ignored.
- **Starting a run needs no outbox.** Every caller re-asks on its own
  reconcile tick, and `requestId` dedupes. So a start that was sent but
  never answered is joined on retry, not doubled. The `requestId` hash
  covers the normalized request without `secrets`, because a retry
  carries freshly minted credentials; a joined start's new secrets
  replace the old ones for jobs that have not yet started.
- **In-flight runs keep running** in grit. Their verdicts wait in the
  event log until grain reconnects.
- **No verdict comes from a timeout on grain's side.** While grit is
  unreachable, a run grain is waiting on is *unknown*, not red:
  - the merge queue and the base gates hold;
  - the run counts against no task's failure streak;
  - `wait_for_checks` answers "CI service unreachable for N minutes
    (infrastructure)", so agents do not chase it.

  Run and job deadlines are enforced in grit, which has the clock that
  matters.
- **Verdict timeouts.** A started run with no event for its workflow's
  longest `timeout-minutes` plus 30 minutes is polled directly
  (`GET /v1/runs/{id}`). If grit answers `404` (it lost the run, for
  example after a restore), grain re-starts it with the same
  `requestId`. It never invents a conclusion.
- **Telling the operator.** After 5 minutes unreachable, or 5 minutes of
  event lag, grain raises a `grit-unreachable` deployment-health
  notification. It clears itself once the cursor is current again.

## Reaching job pods

This is the reliability fix. In remote mode **nothing execs into a job
pod, port-forwards to one, or reads its logs through the API server.**

- **Grit's API-server use.** Grit uses the API server only to create,
  watch, list and delete pods, per-run Secrets and NetworkPolicies. None
  of these touches a kubelet.
- **The driver.** A new `ci.Sandboxes` implementation,
  `pkg/grit/podnet`, creates the pod and waits for it to be ready through
  a watch. The pod's per-job Secret holds only the job spec and a
  per-job ingest token. The job's secrets are never written to a
  Kubernetes object: `grit pod` fetches them from grit's memory over
  `grit-ingest` when the guest is ready, using that token, once
  (*Secrets and trust*).
- **The job pod** is today's `K8sGranuleSandboxes.podOptions` pod:
  privileged, with `/dev/kvm`, the nested-virt node selector, and
  `CHV_*` sized from the job's `sandbox:`. One thing changes: the
  container's command is `grit pod` instead of the image's idle
  entrypoint. `grit pod`:
  1. starts `granule run` and waits for `granule ready`;
  2. drives the guest locally with `granule exec` over vsock, inside the
     container, with no network involved. This works the same for
     `networkAccess: none` guests, which have no NIC at all;
  3. runs the job's steps with `pkg/grit/steps`, the same step loop the
     embedded engine runs;
  4. exits with the job's result.
- **Events over the pod network.** `grit pod` pushes events to
  `grit-ingest.grit.svc:8443` (TLS with grit's in-cluster CA), using
  the per-run token mounted from the run's Secret. It sends step start
  and finish, bounded live output every 5 seconds, full step logs on
  completion (grit writes them to GCS), artifacts, and the job result.
  It spools to an `emptyDir` and retries, so a grit restart loses
  nothing.
- **Services over the pod network.** Grit's git server (the checkout),
  the package cache, the Actions cache and the model-proxy relay are
  reached as ClusterIP Services. The guest still reaches them at the
  addresses and environment variables it uses today (`127.0.0.1:9728`,
  `:9729`, and the model-proxy base URLs). The pod end of each granule forward dials
  the Service, instead of tunnelling back to grain over `kubectl exec`.
  A `networkAccess: none` job's NetworkPolicy allows egress only to
  those Services and to `grit-ingest`.
- **Cancellation** deletes the pod with a grace period. `grit pod` traps
  SIGTERM, tells the guest, uploads what artifacts it can, and flushes
  its spool within `terminationGracePeriodSeconds`.

**Grit's executor interface.** The engine's own seam stays where it is
today:

- the engine owns the plan, the DAG, the job cache and the gate;
- `ci.Sandboxes` plus `pkg/grit/steps` run one job attempt.

`podnet` is grit's remote-mode executor, and embedded mode uses
`daemon.CiSandboxes`. A later swap to another executor, or a second
backend for some jobs, would sit behind the same seam. *What a pluggable
backend needs* (under the Cloud Build alternative) lists what the seam
must carry for Argo or Cloud Build to slot in.

Embedded mode keeps today's path: the engine execs into a local granule
sandbox, with no cluster involved.

## The model-proxy relay

`live-frameworks.yml` and similar workflows ask for `claude-proxy`,
`openai-proxy` or `gemini-proxy`. Today those place a per-run token and
a base URL for grain's own model proxies (`pkg/modelproxy`), which run
on the cloudtop and hold the real model credentials. The cluster cannot
dial the cloudtop, so the calls travel back down grain's session.

- **Direction stays outbound-only.** When grain opens its session it
  offers the `model-proxy` stream type. For each model request a job
  makes, grit opens a stream back to grain on that session and sends
  the HTTP request down it; grain hands it to its local model proxy and
  streams the answer back. Server-sent-event streaming responses pass
  through unbuffered.
- **In the cluster** grit runs a ClusterIP **model-proxy relay**
  (`grit-model-relay.grit.svc`), part of the grit Deployment. A job
  pod's granule forward exposes it in the guest at the same base URLs
  and under the same environment variables a grain sandbox gets today
  (`ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, the Gemini equivalent), so
  workflows do not change.
- **Per-run, per-job tokens.** For each job whose policy allows a
  model-proxy capability, grain mints a short-lived token scoped to that
  run and job with the existing `modelproxy` capability provider, and
  sends it under the secrets rules like any other secret (job-addressed,
  sealed, named and versioned in the manifest). The relay checks that the
  token is one it was handed for the calling job, and stamps each
  forwarded request with the run and job (`Grit-Run`, `Grit-Job`). Grain's
  proxy checks the token again, as it does for a sandbox, and applies the
  same quotas, usage-limit pauses and audit. Real model credentials never
  leave the cloudtop. Grain revokes the token's lease when the job ends
  (it learns that from the event stream), and the token also expires at
  the job's `timeout-minutes`. Grit cannot mint tokens: it can only pass
  on ones grain gave it, for the job grain gave them to.
- **Attested.** The relay counts the calls it forwards, and the job's
  predicate names them: provider, model, and request count
  (*Attestations*).
- **Failure.** If no session offers `model-proxy` when a job makes a
  call, or the stream breaks mid-call, the relay answers `503` with
  `Grit-Infra: model-relay-unavailable`. `grit pod` records that on the
  step, and the job concludes `errored` as an infrastructure failure,
  never red, so grain's `infraverdict.go` treats it as no answer.
- **What it reuses:** `pkg/tunnel/mux` for the streams and
  `pkg/tunnel/wsupgrade` for the upgrade, as `pkg/modelexec` does;
  `pkg/modelproxy`'s token checks and quotas unchanged on grain's side;
  and the guest-side granule forward the capability already uses to
  place its endpoint, pointed at the relay Service instead of a
  `kubectl exec` tunnel.
- **Rollout:** GS-4. Until it lands, workflows that ask for a
  model-proxy capability stay embedded on the cloudtop (*Requests that
  stay in grain*).

**Possible future work, not part of this plan:** the same reverse-proxy
pattern could carry other host-run capability servers on the cloudtop
to CI jobs in the cluster, so that a capability whose server cannot
leave the cloudtop no longer forces its workflows to run embedded.

## Package cache

### Where it runs

- **In the cluster:** `pkgcache` is a Deployment in namespace `grit`
  with two replicas, built from the same `pkg/pkgcache` code and shipped
  in grit's image. Each replica has a local SSD or PD hot tier, and all
  share one GCS store (*Storage and sizing*). It serves **grit's CI job
  pods only**, over the pod network at a ClusterIP Service, through the
  pod's forward of `127.0.0.1:9729`. Pods are identified by source
  namespace (NetworkPolicy) plus the per-job token. `networkAccess: none`
  jobs are marked read-only, as `restrict.go` does today.
- **Nothing else reaches it.** It has no route from outside the cluster.
  It is not an open proxy, and `CONNECT` is allowed only to the
  download-only hosts `connect.go` already lists. Its own CA, which job
  guests trust, is generated in the cluster and never leaves it.
- **On the grain host nothing changes.** Grain's in-process package
  cache, its listeners, DNS interceptor, transparent HTTPS with grain's
  CA, and its local disk store keep serving grain's own sandboxes
  exactly as today. Embedded-mode CI jobs use it too.

### Recorded for later: a grain pass-through to the cluster cache

The first draft had grain's local cache become a pass-through to the
cluster cache (at `127.0.0.1:9729` in the guest), with an upstream
fallback and a small local tier for the Go build cache. **That is
removed from the plan.** After the cutover grain's sandboxes are on the
cloudtop and CI is in the cluster, so the two caches serve disjoint
traffic, and a pass-through would only add a WAN hop and GCP egress to
grain's package fetches.

It would be worth building if grain's own sandboxes start to suffer from
what a shared cache fixes: for example, if the cloudtop's cache keeps
hitting Docker Hub's anonymous pull limit or upstream outages that the
cluster cache has already absorbed, or if agent and CI sandboxes would
gain from sharing warm Go build outputs. The trigger is measured: a
sustained upstream-error or rate-limit rate on the cloudtop's cache, or
a cold-miss rate on objects the cluster cache already holds, that costs
agents measurable time. The first draft's design (a circuit-breaking
pass-through, index requests straight to upstream, a local Go tier) is
in this doc's history (PR bwsalmon/grain#4969) for that day.

### Docker Hub without an account

There is no Docker Hub account, so the cluster cache pulls anonymously.
Docker Hub limits anonymous pulls per source IP over six hours, and the
cluster's nodes egress through a small set of NAT addresses. Many CI
pods starting at once (a full suite, a merge batch) could hit the limit
together, and an image pull that fails that way is an infrastructure
failure.

Mitigations that need no account, in order of preference:

1. **Prefer non-Docker-Hub copies of base images.** Workflows and the
   job image reference `mirror.gcr.io/library/<image>` (Google's
   Docker Hub mirror, no limit for GCP clients) or the GHCR copies the
   repository already publishes, instead of `docker.io`. A lint in
   GS-3's image checks flags new `docker.io` references in this
   repository's workflows.
2. **Cache layers aggressively.** Layers are content-addressed and
   immutable, so the cache keeps them for the full 30-day bound, and the
   cluster cache rewrites `docker.io` manifest-by-digest requests to be
   served from its store without contacting Docker Hub. Only tag
   resolution reaches Docker Hub, and the cache keeps a short (5-minute)
   negative-and-positive tag cache to coalesce a burst of pods asking
   for the same tag.
3. **Fall back to `mirror.gcr.io` for `library/` images** when Docker
   Hub answers `429`, which serves the same digests.

A `429` from Docker Hub is reported as an infrastructure failure with a
cause the System page shows, never as red.

### Cache keys

Keys are unchanged: `(kind, key)` as `pkgcache.Cache` files them today.

- **Kinds:** apt, pypi, docker, npm, go, github, playwright, gobuild.
- **Keys** are the upstream URL of an immutable object, or a content
  digest:
  - a container layer is filed by its digest and hash-checked on the way
    in;
  - a Go build output is filed by its output ID and verified on read.

The kinds this design covers, as the brief asks:

| Ecosystem | Today | In the cluster |
|---|---|---|
| apt | HTTP proxy for archive hosts; `.deb`s cached, indexes forwarded | same |
| pip | `/pypi/` with rewritten links; wheels and sdists cached | same |
| npm | `/npm/`; tarballs cached | same |
| Go | `/go/` module proxy (zips, `.mod`), `/gocache/` build cache | same, with the build cache split by trust (below) |
| Docker Hub | `/v2/` registry mirror; layers by digest | same, pulled anonymously; see *Docker Hub without an account* |
| GitHub releases, Playwright CDNs | transparent, by DNS | same |

**The Go build cache's trust split.** Action entries (action ID to
output ID) cannot be verified on read; only outputs can. A sandbox that
writes an action entry can therefore poison a later build that trusts
it. Today one cache serves everything on one host. In the cluster it
would serve every CI run, including release runs. **Decided:** split
action entries into two namespaces:

- **`trusted`** is written only by runs a client marks
  `trustedCache: true` in `StartRun`. Grit accepts that mark only from a
  client holding the `release` scope on the repository. Grain sets it on
  base-branch, release and scheduled runs, the same runs it trusts
  today. Every job reads `trusted`, but only those runs write to it.
- **`untrusted`** is written and read by every other run (task branches,
  merge batches). Trusted runs never read it.

Outputs stay shared, because they are content-verified. Grit judges
none of this itself: whether a run is trusted is grain's policy, and
grit only checks that the client may say so.

### Storage and sizing

- **Store of record: GCS**, prefix `pkgcache/<kind>/<sha256(key)>`, with
  `Meta` in object metadata. Replicas share it, so a pod restart or a
  second replica costs nothing.
- **Hot tier:** each replica keeps a 100 GiB local SSD or pd-balanced
  LRU in front of GCS, using the existing `pkgcache.Cache` on disk.
- **Bound:** `DefaultMaxBytes` is 20 GiB on one host today. The cluster
  default is 200 GiB in GCS, enforced by a sweeper using the same LRU
  accounting. Recency is tracked by object `Custom-Time`, updated at
  most once a day on a hit. A lifecycle rule (`daysSinceCustomTime: 30`)
  is the backstop. `DefaultMaxEntryBytes` is unchanged.
- **Stats and flush** for the cluster cache are `/pkg/v1/stats` and
  `/pkg/v1/flush?kind=`. Grain's System → CI grit panel shows them
  behind a 30-second cache. Grain's own package-cache panel keeps
  showing grain's local cache, unchanged.

### The Actions cache

The Actions cache moves with grit as a ClusterIP Service in `grit`,
storing in GCS (`actions-cache/`, 7-day lifecycle). Only CI uses it
(*What exists today*). The scope rule (`CiActionsCacheScope`, no write
to another ref's scope) is unchanged. Its settings (`ActionsCacheEnabled`,
`ActionsCacheSizeGB`) become grit settings, shown read-only on
Settings → CI.

## Storage

- **Run store: in-cluster PostgreSQL run by CloudNativePG** (decision
  10), in namespace `grit`. Grit has its own schema in `pkg/grit/store`
  (runs, jobs, steps, `grit_event`, `grit_request` for the `requestId`
  index, `grit_tree`, `grit_attestation`, `grit_client` and its scopes and
  quotas). It does not reuse `pkg/model`, which the import lint forbids.
  - **Why CloudNativePG.** It is a CNCF project built for Kubernetes
    alone: one operator and one `Cluster` resource, with no Patroni,
    etcd or sidecar stack to run beside it. It ships backup to object
    storage with WAL archiving and point-in-time recovery, works with
    GKE Workload Identity for GCS, and handles failover, switchover and
    rolling minor upgrades itself. Zalando's operator (Patroni and Spilo)
    and Crunchy PGO (pgBackRest and more components) both do the job
    with more moving parts. A bare StatefulSet would leave backups,
    failover and upgrades entirely to us.
  - **Shape.** One `Cluster` with two instances (a primary and a
    streaming replica, on different nodes by anti-affinity), matching
    grit's two replicas. Grit connects through the operator's `-rw`
    Service with a password the operator generates into a Kubernetes
    Secret. The operator itself is pinned and installed from
    `deploy/grit/cnpg/`.
  - **PVC sizing.** Each instance gets a 50 GiB `pd-ssd` data volume and
    a 10 GiB WAL volume, on a StorageClass with volume expansion on.
    Full step logs, artifacts and trees live in GCS, so Postgres holds
    only records, bounded step output, events (7 days) and attestation
    envelopes. At this deployment's rate (about 2,600 CI sandbox-hours
    in 6 days, on the order of 10,000 job rows a day) that is a few GB
    a month at the mirror's retention, so 50 GiB is several months of
    headroom. Grit raises an alert at 70% full; the volume grows online.
  - **Backups to the GCS bucket.** The barman-cloud plugin archives WAL
    continuously to `gs://grit-<project>/pg/` and a `ScheduledBackup`
    takes a base backup daily. They are kept 14 days, which allows
    point-in-time recovery to any moment in that window. GS-3 includes a
    restore drill into a scratch `Cluster`. A restored database gets a
    new `epoch`, so grain resyncs through the 410 path (*Event stream*).
  - **Upgrades.** Minor versions roll by changing the `Cluster`'s image:
    the operator updates the replica, switches over, then updates the old
    primary. Major versions are a planned step: take a fresh backup, then
    use CloudNativePG's in-place major upgrade (or, if that is not
    available in the pinned version, bootstrap a new `Cluster` with
    `bootstrap.initdb.import` from the old one and switch grit's
    connection over). The operator is upgraded by bumping its pinned
    manifest, which leaves running clusters alone.
- **Job cache, artifacts, logs, trees and attestations: one GCS bucket**,
  `grit-<project>`, with uniform access. Only grit's Workload Identity
  service account is `objectAdmin`. The bucket is the one piece of GS-3's
  Terraform, beside the existing `terraform` job's. Prefixes and
  lifecycle rules:

  | Prefix | Contents | Lifecycle |
  |---|---|---|
  | `cache/` | `GCSCache`, implementing the job-cache interfaces, with entry metadata in object metadata | 30 days, as `DefaultCacheMaxAge` |
  | `artifacts/` | run artifacts | 14 days |
  | `logs/` | full step logs | 14 days |
  | `trees/` | the tree store's recovery copy (*Tree handoff*) | 30 days after the last run that used the tree |
  | `attestations/` | every signed envelope, by run and by subject digest | 400 days |
  | `actions-cache/` | the Actions cache | 7 days |
  | `pkgcache/` | the package cache | sweeper plus 30 days since last hit |
  | `pg/` | Postgres WAL and base backups | 14 days, managed by barman-cloud |

  The byte bounds in the storage-bounds types become grit settings, and
  grit enforces them with a sweeper, as `storagebounds.go` does today.
- **Grain's existing CI history stays where it is** (decided). It is not
  backfilled into grit. The mirror *is* the same tables, so old runs stay
  visible in every CI page beside new ones.
  - A new `origin` column (`local` or `grit:<epoch>`) says which executor
    wrote a row.
  - Merge batches, base-branch rows and release records that name old
    run IDs keep resolving locally.
  - Grit's job cache starts cold (decided). The cloudtop's `DirCache` keys would
    mostly miss anyway, because the sandbox image differs, so this costs
    about one full suite per repository.
  - The cloudtop's job-cache and artifact directories are deleted after a
    two-week soak (GS-5).
- **What grain keeps locally:**
  - *The mirror*, under grain's existing retention
    (`StoredRunRetention`) and step-output bounds. Every `RunStore`
    reader is unchanged: `pkg/mcp`, `pkg/namespace`, the CI pages,
    `metrics.CIQueueing`, test history and shared faults.
  - *Not artifacts or full logs.* They are read through grit on demand.
  - *Cache, artifact, package-cache and capacity statistics*, behind a
    30-second cache.
  - *Every attestation* for every run grain started, in a new
    `ci_attestation` table keyed by run and subject digest, kept as long
    as the run's mirror rows and for at least 400 days for runs that
    gated a merge or a release (*Attestations*).

## Budget and scheduling

- **Grit uses the cluster's existing node pool, with no autoscaling up
  for now** (decision 10). Today that pool is 3 nodes with about 287
  vCPU allocatable, and its nested virtualization already runs CI.
  Grit's job pods share it with grain's `kubernetes-granule` sandboxes
  until GS-5 moves those to the cloudtop.
  - The gate (`gate.go`) changes from "admit what fits in the ledger" to
    "admit up to a ceiling of *pending* pods" (`maxPendingJobs`). Its
    priority order and age boost are unchanged.
  - A namespace ResourceQuota on `grit-jobs` is the hard cap.
  - **Initial sizing, beside today's sandboxes.** At about 2,600 CI
    sandbox-hours over 6 days, CI averages about 18 concurrent job
    sandboxes today, and those keep running through grain's
    `kubernetes-granule` backend until the cutover. During GS-4's shadow
    week, grit runs the same jobs a second time at background priority.
    So the starting quota is a third of the pool: `requests.cpu: 96`,
    memory in proportion to the job shape's ratio, and `pods: 24`, with
    `maxPendingJobs: 4`. That keeps shadow runs from crowding out live
    CI and agents.
  - **At GS-5** grain's CI moves to grit and its agent sandboxes move to
    the cloudtop, so the cluster stops carrying grain's sandboxes. The
    room reserved for them shrinks as they leave, and `grit-jobs`'
    quota and `maxPendingJobs` are re-sized into it (to roughly
    `requests.cpu: 240` and `maxPendingJobs: 12`, leaving headroom for
    the `grit` namespace's own services and system pods). Both are grit
    settings, changed without a release.
  - **Autoscaling is a later step:** turning on the cluster autoscaler
    for the nested-virt pool, with a maximum node count as the spend cap,
    and letting pending pods scale it. Nothing in grit changes for that
    except `maxPendingJobs`.
- **PriorityClasses**, each with its own preemption policy:

  | Class | Value | Preemption | Used for |
  |---|---|---|---|
  | `grit-release` | 1000 | `PreemptLowerPriority` | trusted release runs and promote (`ci.PriorityReleaseGate`) |
  | `grit-base` | 800 | `PreemptLowerPriority` | base-branch runs, base confirmations, and the front of the merge queue |
  | `grit-normal` | 500 | `Never` | task branches and the rest of the queue |
  | `grit-background` | 100 | `Never` | schedules, nightlies and shadow runs (GS-4) |

  Within a class, the order is the gate's: grain's rank pushes, plus
  `DefaultAgeBoost`.
- **Infrastructure failures stay distinct from test failures.** Each of
  these is retried once, then concluded `errored` with an infrastructure
  step, never `failure`:
  - the pod was preempted (`DisruptionTarget` condition), evicted, or
    unschedulable past `UnschedulableTimeout`;
  - the node was lost;
  - the pod was OOM-killed outside the guest (`OOMKilled` reason);
  - setup failed (image pull, guest boot, `granule ready` timeout,
    checkout);
  - a dial to a cluster Service failed;
  - the ingest went silent past its lease.

  An OOM *inside* the guest is reported by `pkg/grit/steps` as a step
  exit 137 with the guest's OOM record. It is retried once, as the
  engine's OOM retry does today (`OOMRetryReasonPrefix`), and fed to the overcommit controller as an OOM
  signal. A step that exits non-zero is a test failure and is never
  retried for infrastructure. Grain's existing `infraverdict.go` reads
  these conclusions unchanged.
- **Grain's budget becomes the single node's**: the cloudtop's 96 vCPU
  and about 236 GB of RAM (decision 2). In remote mode `model.Limits`
  comes from the local host's capacity, as on any host backend. The ledger counts agent sandboxes plus embedded-mode jobs
  (`grain/config` checks and the routed workflows). The CI settings that
  bound grit (`CIJobsPerRun`, `CIAgeBoost`, `CIReserveAfter`, storage
  bounds) become grit's, shown read-only on Settings → CI from
  `/v1/info`, and set in grit's own configuration (per client where
  they are quotas).

## Secrets and trust

### Grain owns secret policy; grit only carries secrets

- **Grain is the only owner of secrets and of secret policy**: its
  encrypted store (release secrets stay at `ci.ReleaseSecretAddress`, as
  today), its capability registry and minting, its trust rules, its
  human-required paths and its release gates. **Grit has no secret store
  and no secret policy.** It never decides whether a job may have a
  secret; it delivers what grain addressed to the job, or nothing.
- **The release-secret binding stays on grain's side.** The rule that a
  secret is materialised in a job's guest only when the run is a trusted
  release or an operator's dispatch, the ref passes `releaseRefs`, the
  commit is vouched for at that ref, and the job's workflow (read from
  the base tree) names the secret, is evaluated in grain
  (`pkg/ci/release_secrets.go`, `checkReleaseRef`, `vouchForCommit`). Its
  result is the set of job-addressed secrets grain puts in the request.
  The first draft's Secret Manager copy, write-only routes and
  `migrate-release-secrets` command are gone.
- **Addressed to a job or a step, never to a run.** Each entry in the
  sealed payload names one job (`workflow/job`), and optionally one step
  in it. Grit hands a job only the entries addressed to it. A
  step-addressed secret is placed for that step's process alone and
  removed when the step ends.

### Short-lived credentials where possible

Grain mints the same temporary credentials it would give a sandbox, per
job, and passes them per request (decision 8). Grit holds no long-lived
copy of anything.

| Material | How grain provides it to a grit job | Lifetime |
|---|---|---|
| `github-sandbox` | a GitHub App installation token, narrowed to the repository and the permissions the capability grants | at most 1 hour |
| `package-publish` | an installation token from the git proxy's credential ladder (`PackageCredential`), when the repository's credential is App-backed | at most 1 hour |
| `gemini-key` | a Gemini API key minted for the job, revoked when the job ends | the job |
| `gcp-key` | a service-account key minted for the job from the minter credential (a lease), deleted when the job ends | the job, by revocation |
| `claude-proxy`, `openai-proxy`, `gemini-proxy` | a per-run, per-job token for grain's own model proxy, used through the relay (*The model-proxy relay*) | the job |

**These cannot be minted short-lived today**, and are passed as their
long-lived values, still sealed and job-addressed:

- `image-signing`: the cosign private key and its password, long-lived
  by design (`docs/image-signing.md`).
- **Release secrets** an operator pasted (for example `NPM_TOKEN`).
- `package-publish` when the repository's credential falls back to a
  personal access token rather than an App installation.
- `registry-push`, a pasted registry credential, if a workflow uses it.

Grain revokes minted material when the job's terminal event arrives,
and its existing lease reaper catches anything a lost event leaves
behind.

### Sealed to grit, readable only in grit's memory

- **Encryption.** Grain seals the request's secrets with envelope
  encryption: a fresh AES-256-GCM key per request encrypts the payload,
  and that key is wrapped with RSA-OAEP-3072-SHA256 to grit's published
  **encryption key** (`use: enc`), a Cloud KMS `ASYMMETRIC_DECRYPT` key
  that is separate from the signing key (*Key management*). The GCM
  associated data is the client id, the `requestId` and the tree's
  sha256, so a sealed payload cannot be replayed onto another request or
  tree. TLS terminates at Google's front end, which therefore sees only
  ciphertext.
- **Memory only.** Grit unwraps the key with one KMS call at admission
  and holds each job's material in process memory, never in Postgres,
  GCS, logs, events or a Kubernetes Secret. `grit pod` fetches its job's
  material over `grit-ingest` once the guest is ready, and places it in
  the guest's tmpfs exactly as the capability or release-secret
  placement does today. Grit drops it when the job ends. If the grit
  replica holding a run's secrets dies before a job starts, that job
  concludes `errored` (infrastructure, `secrets-lost`), and grain starts
  the run again with the same `requestId` and freshly minted secrets.
- **Redaction.** Grit and `grit pod` redact every value they were handed,
  and its base64 and URL-encoded forms, from step output, logs and error
  messages before anything leaves the pod (`pkg/redact`).
- **Job cache.** Today's job-cache key already includes the names of a
  job's capabilities and release secrets. Grit adds their **versions**:
  for stored secrets, the version of the value in grain's store; for
  minted credentials, the revision of the capability's configuration
  (not the token, which is new every time). So a rotated secret or a
  changed grant misses the cache, and a job never reuses a verdict
  reached with different material. Release-event runs never read the
  cache.
- **Residual risk.** A compromised grit sees every secret it is handed,
  for as long as it holds it. Minting per job, scoping per job and
  revoking at job end bound what that exposes. The non-mintable secrets
  above are the real exposure, and are worth moving to minted forms
  where their issuers allow it.

### Trust rules and verdicts

- **`.grain/workflows` trust rules.** Workflows are read from the
  tested tree, as today, so an agent's branch can change its own CI.
  Trust rules come only from the base branch's tree (*Trust rules come
  from the base tree*). The base gate (`GateOnBase` →
  `workflows.RemovedGateJobs`) stays: grit reads the base tree's
  workflows, and fails a head that would run fewer required jobs before
  anything is scheduled. Capabilities and secrets come only from grain's
  policy, never from the workflow file (`requirements.go`'s rule,
  unchanged), and arrive as job-addressed secrets.
- **Required checks and human-required paths** stay in grain's
  orchestrator unchanged. These are `pkg/trustboundary`, the security
  hold, and the `ci-suppressions` category guarding
  `.grain/quarantine.yml`. They gate *merging*, not running. Grit applies
  a quarantine file only from the base tree. Whether a change to that
  file may merge is still grain's decision.
- **Who decides green: grain.** Grit reports facts, signed: each job's
  conclusion, and whether it was cached, a flake, infrastructure, or
  quarantined. Grain counts a fact only after verifying its attestation
  (*Attestations*). Grain computes the verdicts, over the mirror, with the
  shared `pkg/ci` library:
  - `RequiredFor` and the merge rules;
  - `collectCIRun`;
  - the merge queue's verdict;
  - base-gate verdicts;
  - auto-merge decisions.

  A run's own `conclusion` is grit's summary, and grain reads it the same
  way it reads today's.

## Attestations

**Grit returns a signed proof of work for every run, and grain verifies
it before anything counts as green.** It is what lets grain trust a
verdict that crossed a network and a second service, and what lets
anyone else check it later.

### Format

An [in-toto Statement v1](https://github.com/in-toto/attestation) in a
[DSSE](https://github.com/secure-systems-lab/dsse) envelope
(`payloadType: application/vnd.in-toto+json`), signed with ECDSA P-256
and SHA-256. `pkg/attest` defines the types, signing and verification,
and both grain and the `grit verify` subcommand use it.

```json
{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [
    {"name": "tree", "digest": {"sha256": "<canonical archive>", "gitTree": "<tree id>"}},
    {"name": "commit", "digest": {"gitCommit": "<sha>"}}
  ],
  "predicateType": "https://grain.dev/grit/run/v1",
  "predicate": {
    "grit": {"version": "grit/v0.4.0", "build": "<digest>", "instance": "grit-<project>", "gritPod": "<step-semantics version>"},
    "client": {"id": "grain-049f1e09", "principal": "grain-client@…", "labels": {"taskId": "1338", "requestedBy": "merge-queue"}},
    "request": {"requestId": "grain:…", "run": "r-3f9c2a", "repo": "bwsalmon/grain", "ref": "…", "event": "pull_request",
                "baseTree": {"sha256": "…", "gitTree": "…"}, "baseCommit": "<sha>", "submittedAt": "…"},
    "workflows": [{"path": ".grain/workflows/tests.yml", "sha256": "…"}],
    "jobs": [{
      "id": "tests.yml/go", "definitionSha256": "…", "fingerprint": "…",
      "image": {"ref": "ghcr.io/…/sandbox", "digest": "sha256:…"},
      "sandbox": {"cpus": 8, "memoryMiB": 16384, "networkAccess": "open"},
      "env": {"GOFLAGS": "-mod=mod", "NPM_TOKEN": "secret:NPM_TOKEN@v7"},
      "secrets": [{"name": "gemini-key", "version": "cfg-12", "step": ""}],
      "modelCalls": [{"provider": "anthropic", "model": "claude-…", "requests": 41}],
      "steps": [{"name": "test", "commandSha256": "…", "exit": 0,
                 "startedAt": "…", "finishedAt": "…", "logSha256": "…"}],
      "result": {"conclusion": "success", "attempts": 1, "infrastructure": false, "flake": false, "quarantined": false},
      "tests": {"passed": 1840, "failed": 0, "skipped": 12, "failedNames": [], "namesSha256": "…", "namesArtifact": "…"},
      "artifacts": [{"name": "gotest.log", "sha256": "…"}],
      "timing": {"queuedAt": "…", "startedAt": "…", "finishedAt": "…"},
      "cached": null
    }],
    "timing": {"submittedAt": "…", "finishedAt": "…"},
    "issuedAt": "…"
  }
}
```

- **Subject:** the tree's identity, plus the commit when the run named
  one.
- **Predicate:** the workflows and jobs run, with their digests; each
  job's commands (as digests of the exact step scripts), image digests
  and environment (values for workflow-declared variables,
  `secret:<name>@<version>` for anything that came from a secret); per-job
  results; test counts, the names of failed tests, and a digest of the
  full test-name list (stored as an artifact when it is long), where the
  job produces a parsable report; timings; log and artifact digests; the
  grit version; the **requesting client**; the names and versions of the
  secrets given to each job, never their values; and the model calls the
  relay forwarded.
- **A cached verdict references the original.** A job whose verdict came
  from the job cache carries
  `"cached": {"attestation": "sha256:<envelope digest>", "run": "r-…", "fingerprint": "…"}`
  and no steps, logs or timings of its own. It never claims a fresh run.
  Grit stores the original envelope's digest in the cache entry, so it
  can always name it.
- **One envelope per run**, signed when the run concludes and delivered
  as a `run.attested` event and at `GET /v1/runs/{id}/attestation`. A
  cancelled or errored run is attested too, with what it did.

### Grain verifies before anything counts

Before a run counts towards **any** green verdict (the merge queue,
base-branch gates and confirmations, release gates such as `select-lkg`
and soak, auto-merge, required checks, `wait_for_checks`' verdict),
grain checks, in `pkg/ci`'s verifier:

1. the DSSE signature, with a key from grit's key set that grain trusts
   (*Key management*), not revoked at `issuedAt`;
2. that the subject's `sha256` and `gitTree` equal the tree grain sent,
   and the commit equals the one it named;
3. that `client.id` is grain's own, `request.requestId` and `run` match
   the run grain started and is reading, and `baseTree`/`baseCommit`
   equal the base grain meant;
4. that each job grain counts appears with the conclusion the mirror
   holds;
5. for each cached job, that the referenced original envelope (from
   grain's store, or fetched from grit) verifies by rules 1 and 4 and
   carries the same job fingerprint. Its subject may be a different tree,
   which is what a cache hit means.

A run whose attestation is missing or fails is **unknown, not red**: it
is held as an infrastructure failure, grain raises a critical
`grit-attestation-invalid` notification naming the check that failed,
and the UI shows the run as unverified. Embedded mode signs with a local
key in grain's state directory and runs the same verifier, so the path
is exercised everywhere.

### Stored and re-verifiable

Grain stores every envelope (`ci_attestation`, *Storage*), and grit keeps
its own copy in GCS. Anyone can re-verify:

- `grain ci attestation <run>` prints the envelope;
  `grain ci verify <run|file>` verifies it against grit's published keys
  and prints what it proves;
- `GET /api/ci/runs/{id}/attestation` and
  `GET /api/ci/attestations?subject=sha256:<digest>` on grain's API;
- `grit verify <file>` needs nothing but the envelope and the key
  endpoint, so a third party can check a release's tests without grain.

### What this does and does not prove

It proves that a holder of grit's signing key stated these results, for
this tree and base, for this client and request, at this time. So a
tampered mirror row, a man in the middle, a grain bug that mixes up runs,
or a verdict moved from one tree to another cannot pass as green.

It does **not** prove:

- **that grit told the truth.** A compromised grit, or anyone who can use
  its KMS signing permission, can sign lies. The signature moves trust
  from the network and the mirror onto grit's deployment and its key;
  it does not remove the need to trust grit.
- that a compromised job guest did not lie to `grit pod` about its own
  steps' results, or that the tests themselves are meaningful;
- that the tree is the one on GitHub. That is grain's claim, made when it
  built the tree.

**Replay.** An old, genuinely signed attestation cannot be reused for a
new verdict: grain accepts only an envelope whose `requestId` and `run`
are the ones it is evaluating, whose base is the current one, and whose
`issuedAt` is after the request was sent. An attestation for one client
is refused by every other, because the client is in the predicate. A
sealed secrets payload cannot be replayed onto another request or tree,
because its associated data binds it to them (*Secrets and trust*).

## Key management

- **Signing key:** a Cloud KMS key `grit-attest` in key ring `grit`
  (`ASYMMETRIC_SIGN`, `EC_SIGN_P256_SHA256`, HSM protection), used
  only by grit's Workload Identity service account, which holds
  `roles/cloudkms.signerVerifier` on that key alone.
- **Encryption key:** a separate KMS key `grit-seal`
  (`ASYMMETRIC_DECRYPT`, `RSA_DECRYPT_OAEP_3072_SHA256`), on which grit's
  service account holds `roles/cloudkms.cryptoKeyDecrypter` alone.
- **Job pods cannot reach either.** Pods in `grit-jobs` run under a
  Kubernetes service account with no Workload Identity binding, and the
  namespace's NetworkPolicy blocks the metadata server, so a pod has no
  ambient Google identity. The only Google credentials a job ever holds
  are the ones grain minted for it (`gcp-key`), and those grant no KMS
  role. A job that escapes its guest still cannot sign or unseal.
- **Published keys.** `GET /.well-known/grit-keys.json` is a JWK set
  holding every signing key version (`use: sig`, `alg: ES256`) and the
  current and previous encryption key versions (`use: enc`,
  `alg: RSA-OAEP-256`), each with `kid` = a hash of the KMS key-version
  name, `status` (`active`, `retired` or `revoked`), and `notBefore`,
  `notAfter` and `revokedAt`. The same document is written to a public
  GCS object so it outlives grit. `pkg/jwks` already parses JWK sets
  with EC P-256 and RSA keys and refuses https downgrades; `pkg/attest`
  wraps it and adds the status fields.
- **Grain's trust anchor.** Grain does not trust whatever the endpoint
  serves. Its configuration pins the KMS key names, and grain's
  `grain-client` service account holds `roles/cloudkms.publicKeyViewer`
  on them: grain accepts a new `kid` from the endpoint only after reading
  the same public key from KMS itself. Deployments without that IAM
  path pin key fingerprints in configuration instead.
- **Rotation.** A new signing key version every 90 days. Grit signs only
  with the newest, and older versions stay enabled in KMS and published
  as `retired`, so every attestation ever issued stays verifiable. The
  encryption key rotates on the same schedule, and an old version stays
  enabled for one day so in-flight requests can still be unsealed, then
  is disabled. A compromised key is published as `revoked` with
  `revokedAt`, and grain refuses envelopes issued after that time.

## Upgrades

- **Independent releases.** Grit's image is built from the same
  repository, but it has its own binary, image and version tags
  (`grit/vX.Y.Z`, *Grit as an independent service*), and it is deployed
  separately, by `kubectl apply -k deploy/grit/` or a GitOps
  controller (GS-3). Grain's self-upgrade (`pkg/upgrade`) does not touch
  it, and grit's rollout does not touch grain.
- **CI no longer dies when grain restarts.** This supersedes Stage 3,
  "CI job drivers", of `docs/non-disruptive-upgrade.md`: the CI half of
  #1245. In remote mode a grain restart costs a gap in the mirror's
  freshness and nothing else. Embedded mode keeps today's behaviour, and
  it is not worth building run hosts for: a single-box deployment's CI is
  small and cached. The agent half of #1245 (run hosts, adoption) is
  unaffected.
- **Grit upgrades** roll with two replicas and a lease. Jobs survive a
  grit restart (*Event stream*). A job pod keeps the image digest it was
  created with, so a job never changes `grit pod` partway through.
- **Skew** is handled as in *Versioning and skew*:
  - grit supports the current and the previous API minor;
  - grain degrades to the features grit reports and never fails a run
    over a missing feature;
  - the order of upgrade does not matter for additive changes;
  - for a `/v2`, grit is upgraded first.

  The package cache's `/pkg/v1` follows the same rules.

## Single-box mode

- **`ci.Runner` has two implementations, chosen by one flag:**
  - `grit.Local`, the default, runs the service in-process with
    `pkg/grit/engine`, grain's own `orchestrator.Sandboxes`, `DirCache`,
    `DirArtifacts` and `StoredRunStore`. This is today's behaviour,
    unchanged.
  - `-ci-remote=https://grit-….sslip.io` (plus `-ci-remote-audience`
    for IAP, the client token from grain's secret store, and the pinned
    KMS key names for verification) selects `gritapi.Client`, together with the embedded router
    (*Requests that stay in grain*).
- **The package cache works the same way:**
  - it runs in-process with a local disk store, as today, in every mode.
    There is no remote package-cache mode for grain (*Recorded for
    later: a grain pass-through to the cluster cache*).
- **`grain demo`** keeps `demo_fakeci.go` behind the same interface,
  and `demo_observability.go` keeps a real embedded engine. It runs in
  one process with no flags.
- **`grit serve`** can also run off-cluster with the local executor
  (`-executor=local`, today's engine on local granule) on a single big
  box. That is what GS-2's remote-client tests run against.

## Observability

- **Readers of the mirror are unchanged:** the CI pages, `grain ci`,
  `list_ci_runs`, `get_ci_log`, `wait_for_checks` and
  `pull_request_status`.
  - A run shows a small "grit" or "local" badge from `origin`.
  - Step output past the mirror's bound reads through
    `GET .../steps/{s}/log`.
- **Metrics:**
  - `ci_queueing` (`metrics.CIQueueing` over `model.CIJobTimings`) reads
    the mirror. Grit fills `QueuedCause` and `QueuedReason` with its own
    causes: `pending-pod` (with the scheduler's reason), `grit-ceiling`
    and `quota`.
  - `checks` measures the agent CI loop (waits, verdicts, pushes to
    green) from grain's own `wait_for_checks` records, so it is
    unaffected.
- **CI faults and unmatched failures** (`/api/ci/faults`,
  `ui/cifault.go`, `pkg/cifault`) run over the mirror in grain, as
  today.
- **Notifications** are raised by grain's orchestrator from mirror
  state, as today. To those it adds:
  - `grit-unreachable`;
  - `grit-event-lag`;
  - `grit-resync`, raised when a 410 happened;
  - `grit-version-skew`;
  - `grit-attestation-invalid` (critical);
  - `grit-session-down`, when no session offers the model-proxy relay
    for 5 minutes.
- **System → CI** gets a grit panel: the endpoint, versions, epoch,
  cursor lag, outbox depth, session state (and whether it offers the
  model-proxy relay), attestations verified and refused, the signing
  key in use, the cluster package cache's stats, and `/v1/capacity`
  (queue by class, pending pods, node-pool size, grain's quota use).
- **Grit itself** exports Prometheus metrics (API latency, ingest lag,
  pod phases, job-cache and package-cache hit rates, GCS bytes) and logs
  to Cloud Logging.

## Recorded alternative: Argo Workflows

Task 1324 scored Argo Workflows, Tekton, ARC, Buildkite on Kubernetes,
and our own engine in the cluster. It recommended Argo. The operator has
chosen our own engine for now (decision 1). The analysis is kept here so
that a later swap can start from it.

**What still holds:**

- **A grain job is one VM, not a sequence of containers.** In any of
  these systems a job is one privileged container whose entrypoint boots
  the guest and runs the steps: `grit pod` in this design. Per-step
  container models do not apply.
- **Verdict reuse has to happen before execution.** Failures are cached
  as well as passes, discarded when the base differs, and bypassed on
  recheck. No candidate has these semantics. Argo's `memoize` keeps only
  successes, in a ConfigMap. So grit keeps the job cache whatever runs
  the jobs.
- **Argo's strengths are on scheduling:**
  - workflow `priority` plus pod PriorityClass;
  - `retryStrategy.expression` over exit code and OOM reason;
  - Postgres archiving;
  - a controller HA story that is mature on GKE.
- **Argo's own log path** (`argo logs`, `archiveLogs`) reads container
  logs through the API server, which is the kubelet connection that
  fails today. Under Argo, `grit pod` would still push events and logs to
  grit over the pod network, as in this design.

**Rejected outright:** ARC, because it hands scheduling, secrets and
the verdict to GitHub, and Buildkite, because it is a SaaS control
plane. Tekton loses to Argo on priority and on the number of services
to run.

**How a swap would look.** It would replace `pkg/grit/podnet` (one pod
per job attempt) with an `argo` executor that submits one `Workflow`
per job attempt. The pod template would be the same (`grit pod`,
privileged, `/dev/kvm`). Infrastructure retries would map to
`retryStrategy.expression`, and priority to `spec.priority` plus
`podPriorityClassName`. The engine above the seam keeps the plan, the
DAG, the cache and the gate, and grain sees no difference.

**When it would be worth it:**

- if owning pod scheduling correctness and HA in grit proves costly;
- or if the cluster needs to run other teams' workflows beside grain's.

Before adopting it, a spike would have to confirm three things on the
remote cluster:

- the emissary executor leaves a privileged `/dev/kvm` container booting
  a guest unaffected;
- exit status and `OOMKilled` reach `retryStrategy.expression`;
- nothing in Argo's normal path reads pod logs through the kubelet with
  `archiveLogs: false`.

## Recorded alternative: Google Cloud Build

Cloud Build was considered as the executor. It is recorded here, beside
Argo, because parts of it match this design closely, and because it may
later be useful as a second backend for some jobs.

**Where it matches:**

- **Source by upload.** `gcloud builds submit` uploads a tarball of the
  source to GCS and builds that, which is the same shape as *Tree
  handoff*: the runner never needs forge credentials.
- **Outbound IAM auth.** The client authenticates to Google with its own
  identity, and nothing dials back, as grain's session does.
- **Provenance.** Cloud Build can emit SLSA build provenance, signed by
  Google, for the images it builds.
- **Managed execution**, with nothing to schedule or keep alive, and
  **private pools** for VPC access and larger machine types.

**Where it falls short:**

- **No nested virtualization and no `/dev/kvm`.** Every grain CI job is
  a cloud-hypervisor guest booted in a privileged pod (*Reaching job
  pods*). Cloud Build's workers, private pools included, cannot boot it.
  This alone rules it out as grit's main executor.
- **Its provenance covers build outputs, not tests run.** SLSA
  provenance says which steps produced an artifact. It says nothing
  about which tests ran, how many passed, or what verdict a job reached,
  which is what *Attestations* exist to prove.
- **No verdict reuse.** There is no job cache with grit's semantics
  (failures cached, discarded when the base moves, bypassed on recheck),
  so grit would keep its cache in front of it anyway.
- **`cloudbuild.yaml` is too small for `.grain/workflows`.** It has
  steps and `waitFor`, but no matrix, `needs` across jobs, `if`
  conditions or `uses`. Grit would still have to plan and expand every
  workflow and submit one build per job attempt.
- **Secrets are referenced, not passed.** Builds read Secret Manager
  versions by name, so per-job minted credentials would have to be
  written into Secret Manager first, which is the long-lived store this
  design avoids (*Secrets and trust*).
- **Cost and quotas.** It is billed per build-minute, with a
  concurrent-build quota per region. This deployment ran about 2,600 CI
  sandbox-hours in 6 days, about 156,000 build-minutes, or about 780,000
  a month. At list prices when this was written ($0.006 a minute for
  `e2-standard-2`, $0.016 for `e2-highcpu-8`, $0.064 for
  `e2-highcpu-32`), that is about $940 every 6 days (about $4,700 a
  month) on the smallest machine, and about $2,500 every 6 days (about
  $12,500 a month) at 8 vCPU, which is closer to what a job uses today.
  Private pools are priced per vCPU-minute and land in the same range.
  The existing node pool, by comparison, is already paid for. Check the
  current price list before relying on these figures.

**Possible future use:** a pluggable backend behind grit's execution
interface, for jobs that do not need KVM (for example a container-only
lint or a docs build), if the cluster's own capacity becomes the
constraint. Not planned.

### What a pluggable backend needs

For Argo, Cloud Build or anything else to slot in later behind the same
seam as `podnet`, the execution interface has to carry, per job attempt:

- **the job spec** (tree identity, steps, image, sandbox shape,
  `networkAccess`, timeout) and a way to fetch the tree and the job's
  secrets that does not depend on the backend (grit's git server and
  `grit-ingest`, or an equivalent the backend can reach);
- **a capability check**, so the engine routes a job only to a backend
  that can run it: KVM, privileged, network isolation, machine size;
- **events out of the job without the backend's log path**: `grit pod`
  (or the backend's equivalent step runner) pushing step events, output,
  logs, test reports and artifacts to grit, so attestations are built
  the same way whatever ran the job;
- **classified outcomes**: test failure, OOM inside the guest, and the
  infrastructure causes in *Budget and scheduling*, mapped from the
  backend's own statuses;
- **cancel, priority and reconcile**: cancel an attempt, express the
  gate's priority, and list what is running after a grit restart;
- **cost and capacity reporting**, for `/v1/capacity` and the gate.

Grit keeps the plan, the DAG, the job cache, the gate and signing above
that seam whatever runs the jobs.

## Order of the moves

The first draft moved the package cache to the cluster for grain's own
sandboxes first, to prove the outbound connection before the CI cutover.
Grain's sandboxes no longer use the cluster cache (decision 3), so that
reason is gone, and the cache simply moves with grit.

What now proves the outbound connection before anything depends on it
is **GS-4's shadow week**: every run grain starts locally is also
started on grit at background priority, through the real session, tree
upload, sealed secrets and attestation verification, and compared. A
failure there costs a shadow comparison, not a verdict. GS-5 cuts over
only once that has held, and only once attestation verification is
mandatory.

## Decisions recorded since the first draft

The first draft ended with open questions. The operator has answered all
of them, and they are written into the sections above. In brief:

1. **Run store:** in-cluster Postgres with CloudNativePG, in the
   existing cluster (*Storage*).
2. **Endpoint auth:** IAP is allowed; the DNS name follows the sslip.io
   convention until a zone exists; mTLS is a recorded alternative only
   (*Connectivity and auth*).
3. **GitHub App:** answered by a later decision. Moot, because grain
   sends the tree (*Tree handoff*).
4. **Release secrets in Secret Manager:** answered by a later decision.
   Moot, because secrets are passed per request and grit has no store
   (*Secrets and trust*).
5. **The single node:** the cloudtop, 96 vCPU and about 236 GB (decision
   2).
6. **Credentials for CI jobs and the model proxies:** grain mints the
   same temporary credentials it would give a sandbox and passes them
   per job; the model proxies reach jobs through the relay (*Secrets and
   trust*, *The model-proxy relay*).
7. **A local Go tier on the grain host** and 8. **egress for package
   bytes:** answered by a later decision. Moot, because grain's
   sandboxes do not use the cluster cache (*Package cache*).
9. **Docker Hub credentials:** there is no account; pull anonymously and
   mitigate (*Docker Hub without an account*).
10. **Go build-cache trust split:** yes, trusted and untrusted
    namespaces (*Cache keys*).
11. **Spend caps:** the existing node pool, no autoscaling for now, and a
    `grit-jobs` quota sized beside today's sandboxes (*Budget and
    scheduling*).
12. **History:** old CI history stays in grain with no backfill, and the
    job cache starts cold (*Storage*).
13. **HA:** two grit replicas with a lease.
14. **The `kubernetes-granule` agent backend:** demoted, not deleted, and
    off on this deployment after GS-5.
15. **Task 1324's held proposals:** answered by a later decision. None
    are open.

## The stages

GS-1 to GS-5 each depend on the one before. All are filed held for
approval. Implementing commits carry
`Design-Item: docs/design/grit-split.md#GS-<n>`.

### Put CI and the package cache behind interfaces {#GS-1}

Introduce `ci.Runner`, the whole of `ci.Service`'s exported method set,
and make every caller outside `pkg/ci` take it, apart from `pkg/daemon`'s
builder:

- `orchestrator`, `ui`, `mcp` and `demo`;
- `daemon.LiveCI` and `CiGate`.

Remove the type assertions on `*ci.Service` (`MarkFlake`,
`EvictBaseTransitionFailures`, and the artifact and cache halves).

Put the package cache behind a `pkgcache.Store` (the object store) and a
`ui.PackageCache`-shaped admin interface that the daemon wiring uses.

Add the import-boundary test. No behaviour change.

### Neutral packages, `grit serve`, tree intake and attestations, embedded {#GS-2}

Start with the independence work, before anything else in this stage:

- Move the shared code into the neutral packages (*Neutral packages*):
  `pkg/workflows` (the `.grain/workflows` parser and planner, fingerprints
  and result types), `pkg/treeid`, `pkg/attest` and `pkg/gritapi`, with
  `pkg/granule` checked as already neutral. `pkg/ci` keeps type aliases
  so its 65 importers need not move at once.
- Add the import lint (`pkg/grit/independence_test.go`, in the `lint`
  job) with its allowlist. It must fail on a grit or neutral package that
  imports a grain-only package.
- Give `cmd/grit` its own image and release tags (`grit/vX.Y.Z`),
  separate from grain's.

Then move the execution files to `pkg/grit/...` as the boundary tables
say, and `pkg/grit`'s act code to `pkg/grit/actions`, and build:

- `grit.Local`;
- the `/v1` server, with the event log, `requestId` dedupe and opaque
  `labels`, and grit's own store (`pkg/grit/store`);
- **tree intake:** tree identity and the canonical archive
  (`pkg/treeid`), `trees:negotiate`, `PUT /v1/trees` with pack
  verification against both digests, the per-client tree store and its
  size limits, and grit's read-only git server for job checkouts;
- base-tree trust rules: the base gate, declared workflows and quarantine
  read from `baseTree`;
- **the attestation format and signing:** the in-toto Statement and
  predicate in `pkg/attest`, DSSE signing behind a `Signer` interface
  (a local file key here; KMS in GS-3), cached jobs referencing their
  original envelope, `GET /v1/runs/{id}/attestation`, `run.attested`
  events, and `grit verify`;
- **token auth and scopes:** client tokens (hashed), per-client and
  per-repository scopes (`submit`, `read`, `cancel`, `cache-admin`,
  `release`), quotas and rate limits, and `grit client add|scope|revoke`;
- the session (`GET /v1/session`, `pkg/tunnel/wsupgrade` and
  `pkg/tunnel/mux`), with reverse-stream offers recorded but no stream
  types served yet;
- `gritapi.Client`, with the cursor handling, and grain's outbox;
- `grit serve -executor=local`;
- the `/pkg/v1` package-cache service, for job pods only.

Run one contract suite against both `grit.Local` and `gritapi.Client`
talking to an in-process `grit serve`. It must include kill-and-restart
of either side mid-run with no lost verdict, a 410 resync, a tree upload
that dedupes against held objects, a pack that does not match its
digests, a client without the needed scope, and an attestation that
verifies for its tree and fails for any other. No grain behaviour
changes.

### Deploy grit into the existing cluster {#GS-3}

Build `deploy/grit/` and bring grit up in the GKE cluster grain already
uses for `kubernetes-granule`, in `grain-sandbox-dev`, in namespaces
`grit` and `grit-jobs`, on the existing node pool:

- manifests for grit (two replicas with a lease), the package cache and
  the Actions cache (job pods only), grit's git server, the model-proxy
  relay Service (not yet wired), the PriorityClasses, and the `grit-jobs`
  ResourceQuota and `maxPendingJobs` sized beside today's sandboxes
  (*Budget and scheduling*), with no autoscaling;
- `pkg/grit/podnet` and `grit pod`, with the ingest endpoint and the
  per-job secret fetch;
- **CloudNativePG**: the pinned operator, a two-instance `Cluster` with
  the PVC sizes in *Storage*, barman-cloud WAL archiving and daily base
  backups to the bucket, and a restore drill;
- the GCS bucket with its lifecycle rules, in Terraform beside the
  existing `terraform` job's (no Cloud SQL, no GitHub App);
- **key management:** the KMS key ring `grit` with `grit-attest`
  (signing) and `grit-seal` (sealing) keys, IAM bindings for grit's
  Workload Identity service account only, the KMS `Signer` and unsealer
  in grit, `/.well-known/grit-keys.json` and its public GCS copy, and the
  90-day rotation that keeps old keys published;
- the Gateway with IAP, its DNS name (*Authentication, in layers*), the
  Workload Identity bindings, and `roles/cloudkms.publicKeyViewer` for
  `grain-client`;
- the `mirror.gcr.io` preference and the Docker Hub `429` handling
  (*Docker Hub without an account*).

Smoke test it with `curl` as the operator principal and with a test
client token, with no grain talking to it. Confirm from the API server's
audit log that a run makes no exec, attach, port-forward or log calls,
and that a job pod cannot reach KMS or the metadata server.

### Remote CI behind flags, with verification and per-request secrets, on this deployment {#GS-4}

Add to grain (there is no remote package-cache mode; grain's own
sandboxes keep their package access unchanged):

- `-ci-remote` and the router;
- tree handoff from grain: building `tree` and `baseTree`, negotiation
  and pack upload over the session;
- **attestation verification in grain**: the verifier with its five
  checks, the KMS-anchored key set, the `ci_attestation` table,
  `grain ci attestation` / `grain ci verify` and the API routes, and the
  `grit-attestation-invalid` notification. In this stage verification
  runs on every grit run and records its result, but a failure is
  reported rather than blocking, while the shadow week runs;
- **per-request secret passing**: grain mints each job's credentials
  (`CiGrantReservations`), applies the release-secret binding on grain's
  side, seals the payload to grit's encryption key, sends the manifest,
  and revokes minted material on the job's terminal event;
- **the model-proxy relay**: the `model-proxy` reverse stream on grain's
  session, the relay in grit, per-run per-job tokens, the attested model
  call counts, and `errored` on a missing tunnel. Until it lands,
  model-proxy workflows stay embedded;
- the mirror's `origin` column, the event follower and the outbox;
- the System → CI grit panel and the `grit-*` notifications.

Then shadow CI: every run grain starts locally is also started on grit
at `grit-background` priority, with `labels.requestedBy: shadow`. A
shadow run's records and attestations go to separate tables, not the
mirror, and are compared per job (conclusion, cache hit, duration,
attestation verified). A week of agreement, or of explained differences,
with every shadow attestation verifying, is the gate to GS-5.

### Cut over CI and grain's sandbox backend {#GS-5}

Only once attestation verification is **mandatory**: switch the
verifier from reporting to blocking, so that no grit run counts towards
the merge queue, base gates, release gates (`select-lkg`, soak),
auto-merge or required checks without a verified attestation. Then, on
this deployment:

1. Turn on `-ci-remote`.
2. Move grain's agent and chat sandboxes to local `granule` on the
   cloudtop (96 vCPU, about 236 GB), and size the budget to it.
3. Turn the `kubernetes-granule` agent backend off on this deployment.
   Keep its code and document it as not recommended across networks
   (demote, do not delete).
4. Re-size `grit-jobs`' quota and `maxPendingJobs` into the room grain's
   sandboxes leave on the node pool.

After a two-week soak, delete the cloudtop's job-cache and artifact
directories. Update `docs/non-disruptive-upgrade.md` to say that Stage 3
is superseded by this doc.
