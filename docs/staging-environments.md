# Staging environments in Grain

> **Superseded / Historical.** The workload plane environment reconciliation and provisioning
> architecture described here has been unified and superseded by
> [Long-Lived Repo Managers and Dynamic Environments](repo-managers-and-environments.md)
> (and previously [Granule-based Repo Controllers](repo-controllers-and-environments.md)).
>
> The Long-Lived Repo Manager architecture moves arbitrary cloud environment provisioning,
> dynamic preview environments, and repository governance out of the core daemon into autonomous
> Repo Manager Agents using a Two-Sandbox Security Model (Provisioner vs. Task Worker sandboxes)
> and a minimal Substrate API.
>
> This document is preserved for historical context and for the Control Plane guidelines (running
> staging deployments of the Grain orchestrator itself, Section 9). For current architecture
> specifications, see `docs/repo-managers-and-environments.md`.

Grain currently provides isolated sandboxes (kontur VMs or container sandboxes)
where agents run builds, tests, and tool commands. However, real-world software
validation requires testing against deployed runtime environments before merging
to `main` or promoting to production. Furthermore, organizations operating Grain
often run a staging Grain cluster alongside production.

This design addresses both planes:
1. **The Workload Plane**: First-class support for **Ephemeral Preview Environments**
   and **Shared Staging Environments** for repositories under test, integrated
   directly into task composition, CI workflows, merge queue batching, and release
   train qualification plans.
2. **The Control Plane**: Architecture, state isolation, webhook routing, and
   safety invariants for running staging deployments of the Grain orchestrator itself.

---

## 1. The problem and what exists today

### The Workload Plane: Sandboxes are not Staging

Today, an agent sandbox is a private VM running Docker or Kind on a single host.
While sandboxes excel at hermetic unit tests, linters, and fast test suites,
they cannot easily replicate:
- Multi-service distributed architectures running across cloud VPCs or Kubernetes clusters.
- Integration with live external dependencies (e.g. third-party APIs, OAuth providers, payment gateways).
- Staging databases with representative sanitized datasets or schema migration verification.
- End-to-end smoke testing, browser UI walkthroughs, and performance soaks.

Currently, if a task needs to deploy code or verify against staging, it must either:
- Rely on ad-hoc shell scripts embedded in task prompts, with no concurrency control
  against other agents targeting the same shared staging cluster.
- Blindly merge to `main` and let external CI/CD deploy to staging, discovering
  integration regressions only after code has already landed.
- Manually configure credentials with broad permissions, risking accidental mutation
  of production resources from a sandbox.

### The Control Plane: Telling Deployments Apart

For the Grain orchestrator itself, `model.Config.EnvironmentName` (grain/task-69)
provides a UI badge ("staging", "dev") to prevent operator misclicks between
pixel-identical deployments. `pkg/secretsrepo` partitions encrypted secret bundles
by state repository URL (`FileName(stateRepoURL)`). However, there is no formal
specification for webhook deconfliction, state branch management, or canary
validation between staging and production Grain instances.

---

## 2. Core architectural principles and invariants

1. **Declarations live with the code.**
   Like `.grain/workflows`, `.grain/templates`, and `.grain/suites`, repository-level
   environments are declared in `.grain/environments/<name>.yml`. They branch with
   the code, are reviewed in pull requests, and update dynamically when merged.
   Deployment-wide shared environments are declared in Grain's state repository.

2. **Strict concurrency control via Leases.**
   Shared staging environments are protected by **Environment Leases**. Two agent tasks,
   qualification runs, or merge queue batches never deploy to or mutate the same shared
   staging target concurrently. When an environment is occupied, dependent tasks wait
   in the queue until the lease is freed.

3. **Ephemeral instances have deterministic lifecycles and automatic reaping.**
   Ephemeral preview environments (e.g., disposable Kubernetes namespaces, temporary
   Cloud Run services) are spun up on demand, verified via health checks, linked to
   task/PR views, and automatically reaped when the task completes or its TTL expires.

4. **Credential isolation across environment boundaries.**
   A task running against `staging` is granted only staging capabilities and staging
   secrets. Production credentials and capabilities are withheld by construction.

5. **Level-triggered reconciliation.**
   All environment state transitions (provisioning, health checking, lease allocation,
   teardown, reaping) are driven by idempotent level-triggered reconcilers in
   `pkg/orchestrator`.

---

## 3. Environment declarations (`.grain/environments/`)

A repository declares its environments in `.grain/environments/<name>.yml`.
The file stem is the environment's unique identifier within the repository.

### Ephemeral Preview Environment Example

```yaml
# .grain/environments/preview.yml
name: Ephemeral PR Preview
type: ephemeral

# Provisioning workflow executed to spin up the environment
provision:
  workflow: .grain/workflows/preview-deploy.yml
  timeout: 10m
  healthcheck:
    http:
      path: /healthz
      port: 8080
      expected_status: 200
      interval: 5s
      timeout: 2m

# Automatic teardown policy
teardown:
  workflow: .grain/workflows/preview-destroy.yml
  ttl: 4h
  reap_on_task_finish: true

# Capabilities granted to the provisioning job and the running task
capabilities:
  - k8s-sandbox/staging
  - gcp-key/staging

# Dynamic URL template exposed to the agent, UI, and PR comments
urls:
  primary: "https://pr-{{ .Task.ID }}.preview.example.com"
  metrics: "https://grafana.staging.example.com/d/preview?var-env=pr-{{ .Task.ID }}"
```

### Shared Staging Environment Example

```yaml
# .grain/environments/staging.yml
name: Shared Staging Cluster
type: shared

# Concurrency limit: only 1 task or batch may mutate staging at a time
concurrency: 1

# Optional reset/cleanup hook run before or after each lease
reset:
  workflow: .grain/workflows/staging-clean.yml
  timeout: 5m

healthcheck:
  http:
    url: https://staging.example.com/healthz
    expected_status: 200
    timeout: 30s

capabilities:
  - k8s-staging-access
  - gcp-key/staging

secrets:
  DATABASE_URL: staging/db-url
  API_KEY: staging/service-key

urls:
  primary: "https://staging.example.com"
  dashboard: "https://dashboard.staging.example.com"
```

---

## 4. Data model & schema

Three new entities represent environments in `pkg/model`:

```
┌─────────────────────────────────────────────────────────────────┐
│                       model.Environment                         │
│  Declares an environment: name, type, provision/teardown hooks, │
│  concurrency limit, capabilities, and URL patterns.             │
└────────────────────────────────┬────────────────────────────────┘
                                 │ 1:N
                                 ▼
┌─────────────────────────────────────────────────────────────────┐
│                   model.EnvironmentInstance                     │
│  A runtime instance: ephemeral (per-task) or shared singleton.  │
│  Holds status: Pending, Provisioning, Ready, TearingDown,       │
│  Reaped, Failed. Holds resolved URLs and allocated endpoints.   │
└────────────────────────────────┬────────────────────────────────┘
                                 │ 1:N
                                 ▼
┌─────────────────────────────────────────────────────────────────┐
│                     model.EnvironmentLease                      │
│  An active lock holding an instance for a Task, Qualification   │
│  Run, or Merge Batch. Released on completion or failure.        │
└─────────────────────────────────────────────────────────────────┘
```

### Go Model Definitions (`pkg/model/environment.go`)

```go
package model

import "time"

type EnvironmentType string

const (
	EnvEphemeral EnvironmentType = "ephemeral"
	EnvShared    EnvironmentType = "shared"
	EnvExternal  EnvironmentType = "external"
)

type EnvironmentStatus string

const (
	EnvStatusPending      EnvironmentStatus = "pending"
	EnvStatusProvisioning EnvironmentStatus = "provisioning"
	EnvStatusReady        EnvironmentStatus = "ready"
	EnvStatusInUse        EnvironmentStatus = "in_use"
	EnvStatusTearingDown  EnvironmentStatus = "tearing_down"
	EnvStatusReaped       EnvironmentStatus = "reaped"
	EnvStatusFailed       EnvironmentStatus = "failed"
)

// Environment is the declared template/configuration for an environment.
type Environment struct {
	ID           string            `json:"id"`
	Repo         RepoRef           `json:"repo"`
	Name         string            `json:"name"`
	Type         EnvironmentType   `json:"type"`
	Concurrency  int               `json:"concurrency"` // default 1 for shared
	TTL          time.Duration     `json:"ttl"`
	Capabilities []string          `json:"capabilities"`
	Secrets      map[string]string `json:"secrets"`
	URLPatterns  map[string]string `json:"url_patterns"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// EnvironmentInstance is a live runtime instance of an environment.
type EnvironmentInstance struct {
	ID            string            `json:"id"`
	EnvironmentID string            `json:"environment_id"`
	Repo          RepoRef           `json:"repo"`
	Status        EnvironmentStatus `json:"status"`
	StatusMessage string            `json:"status_message,omitempty"`
	OwnerTaskID   *int64            `json:"owner_task_id,omitempty"`
	OwnerBatchID  *string           `json:"owner_batch_id,omitempty"`
	OwnerCandID   *int64            `json:"owner_candidate_id,omitempty"`
	ResolvedURLs  map[string]string `json:"resolved_urls,omitempty"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// EnvironmentLease represents exclusive or shared access granted to a task or run.
type EnvironmentLease struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	TaskID     *int64    `json:"task_id,omitempty"`
	RunID      *int64    `json:"run_id,omitempty"`
	BatchRef   *string   `json:"batch_ref,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}
```

### Task Composition Layer (`pkg/model/compose.go`)

Environment bindings are incorporated into Grain's unified task composition rules:

- **Layer 1 (Template / Schedule / Plan)**: A template or qualification plan specifies
  `Environment: "staging"` or `Environment: "preview"`.
- **Layer 2 (Placement)**: Composing a task resolves the environment configuration from
  the target repository's declarations (`.grain/environments/`).
- **Layer 3 (Dispatch)**: The task is tagged with required environment leases. The dispatcher
  will not advance the task to `running` until an environment instance is `ready` and
  its lease is granted.

---

## 5. Orchestrator lifecycle reconcilers

Grain's level-triggered daemon cycle (`pkg/orchestrator/cycle.go`) introduces two
new reconcilers:

### 1. `SyncEnvironments` (`pkg/orchestrator/environments.go`)

`SyncEnvironments` manages the state machine of environment instances:

1. **Provisioning**: For each `pending` ephemeral instance, launches the provision
   workflow via `pkg/ci`. Once the workflow succeeds and health checks pass, marks the
   instance `ready` and populates `ResolvedURLs`.
2. **Health Monitoring**: Periodically evaluates health checks against `ready` and `in_use`
   instances. If an instance fails consecutive health checks, flags it as `failed`.
3. **TTL Expiration & Teardown**: Identifies ephemeral instances whose `ExpiresAt` has
   passed or whose owning task has concluded (`completed`, `failed`, `cancelled`).
   Transitions them to `tearing_down`, runs the cleanup workflow, and marks them `reaped`.
4. **Orphan Garbage Collection**: Reaps any dangling preview instances created by runs
   that terminated abruptly or sandboxes destroyed during unexpected host restarts.

### 2. `SyncEnvironmentLeases` (`pkg/orchestrator/lease.go`)

`SyncEnvironmentLeases` arbitrates access to shared and ephemeral environments:

- **FIFO Queueing**: When multiple tasks or batches request a shared environment with
  concurrency = 1, leases are granted in order of task submission or merge queue priority.
- **Task Gating**: If a task requires an environment lease that is currently unavailable,
  `dispatch.Cycle` leaves the task in `pending_lease` state and does not allocate a sandbox
  until the lease is acquired.
- **Automatic Release**: When a task's run completes (or fails), `orchestrator.finishRun`
  automatically releases all held leases, making the instance available for the next
  queued candidate or triggering the reset hook.

```
       Task Dispatched (needs staging)
                     │
                     ▼
          ┌────────────────────┐
          │   Request Lease    │
          └──────────┬─────────┘
                     │
       ┌─────────────┴─────────────┐
       ▼                           ▼
[Ephemeral Environment]    [Shared Environment]
       │                           │
       ▼                           ▼
Provision new instance      Is lease available?
       │                           │
       ▼                      Yes ┌┴┐ No
Healthcheck passes                │ │
       │                          │ └─► Park task (pending_lease)
       ▼                          │
Mark Ready ───────────────────────┤
                                  ▼
                          Acquire Lease
                                  │
                                  ▼
                          Run Agent / CI
                                  │
                                  ▼
                          Task Completes
                                  │
                                  ▼
                          Release Lease
                                  │
       ┌──────────────────────────┴──────────────────────────┐
       ▼                                                     ▼
[Ephemeral]                                             [Shared]
Trigger Teardown & Reap                             Run optional reset hook
                                                    Wake next queued task
```

---

## 6. Integration with Merge Queue & Release Trains

### Staging-Gated Merge Queue

When batching pull requests (`docs/merge-queue-batching.md`), speculative merges on
`grain-merge-queue/<branch>` must be validated on staging before fast-forwarding to `main`:

```yaml
# .grain/merge-queue.yml
batch_size: 4
timeout: 45m
staging_gate:
  environment: staging
  deploy_workflow: .grain/workflows/staging-deploy.yml
  test_suite: .grain/suites/staging-smoke.yml
  require_green: true
```

**Workflow**:
1. `startMergeBatch` creates speculative ref `grain-merge-queue/<branch>` containing
   candidates 1..N.
2. Fast unit CI runs against the ref.
3. Upon unit pass, `acquireMergeQueueLease` claims the `staging` environment lease.
4. `deploy_workflow` deploys the speculative batch commit to staging.
5. `test_suite` executes end-to-end smoke tests against staging `ResolvedURLs`.
6. **On Pass**: All N pull requests merge to `main`. Staging lease is released.
7. **On Failure**: The batch is halved (`merge-queue-batching.md` failure policy) to
   isolate the culprit without ever polluting the `main` branch or production.

### Release Train Candidate Qualification

When a release train cuts a candidate (`pkg/orchestrator/qualifications.go`):

1. `SyncQualifications` identifies an active release candidate (`rel.1.0.1`).
2. If the repository's `QualificationPlan` specifies an environment (`staging`),
   Grain provisions a preview instance or claims the shared staging lease.
3. Candidate qualification tasks (e.g. database migration verification, load tests,
   integration suites) execute against the deployed staging instance.
4. Upon successful completion of all qualification tasks, `AutoPromote` promotes
   the candidate to `rel.latest` and triggers production deployment.

---

## 7. Agent ergonomics & MCP tool surface

Agents working inside sandboxes receive first-class MCP tools to inspect and interact
with their assigned staging environment:

### MCP Tools Added to Sandbox Guest:

1. **`mcp_grain-sandbox_environment_info`**
   Returns the current task's assigned environment details:
   ```json
   {
     "name": "preview",
     "type": "ephemeral",
     "status": "ready",
     "urls": {
       "primary": "https://pr-75.preview.example.com",
       "api": "https://pr-75.preview.example.com/api/v1"
     },
     "variables": {
       "CLUSTER_NAMESPACE": "grain-pr-75"
     }
   }
   ```

2. **`mcp_grain-sandbox_environment_deploy`**
   Triggers an incremental rebuild and redeployment of the sandbox's current uncommitted
   or committed branch state to the preview environment. Enables rapid iterative testing.

3. **`mcp_grain-sandbox_environment_logs`**
   Streams runtime application logs from the staging/preview environment directly into
   the agent's context for debugging crashes and 500 errors.

4. **`mcp_grain-sandbox_environment_health`**
   Runs on-demand health check endpoints and reports diagnostic output.

---

## 8. Security, isolation & credential scoping

1. **Credential Namespacing in `pkg/secretsrepo`**:
   Secrets are strictly partitioned by environment path prefix:
   - `staging/db_password`
   - `production/db_password`
   A task targeting `staging` can only resolve `staging/*` secrets. Attempts to access
   `production/*` keys fail closed before sandbox execution.

2. **Network Isolation**:
   Sandbox guests access staging environments via standard outbound HTTPS or scoped
   Kubernetes cluster proxies. Production VPCs and networks are unroutable from
   sandboxes unless explicitly granted via high-trust capability overrides.

3. **Placement Sanitization**:
   Environment-specific credentials placed in `/home/debian/.env` or placed file
   locations (`mcp.PlaceFileOverSSH`) are wiped on sandbox recreation or task release.

---

## 9. Operating Staging Deployments of Grain (The Control Plane)

When running a dedicated **Staging Grain Deployment** (e.g. `staging-grain.internal`)
alongside **Production Grain** (`grain.internal`):

```
┌──────────────────────────────┐        ┌──────────────────────────────┐
│   Staging Grain Controller   │        │  Production Grain Controller │
│   -env-name=staging          │        │   -env-name=production       │
│   state branch: state-staging│        │   state branch: main         │
└──────────────┬───────────────┘        └──────────────┬───────────────┘
               │                                       │
               ▼                                       ▼
 ┌───────────────────────────┐           ┌───────────────────────────┐
 │ secretsrepo (remote repo) │           │ secretsrepo (remote repo) │
 │ grain-staging.secrets.enc │           │ grain-prod.secrets.enc    │
 └───────────────────────────┘           └───────────────────────────┘
               │                                       │
               ▼                                       ▼
 ┌───────────────────────────┐           ┌───────────────────────────┐
 │ Target Repos:             │           │ Target Repos:             │
 │ - staging-only test repos │           │ - all production repos    │
 │ - PR label: `grain-stage` │           │ - PR label: `grain`       │
 └───────────────────────────┘           └───────────────────────────┘
```

### Operational Invariants for Multi-Deployment Setups:

1. **State Repository Branching**:
   - Production Grain binds to `main` of `grain/config`.
   - Staging Grain binds to `staging` (or `state-staging`), testing configuration and
     template changes prior to promotion.

2. **Secrets Isolation**:
   - `pkg/secretsrepo` derives separate files based on state repo URL or explicit
     `-secrets-file`: `grain-staging-state.secrets.enc` vs `grain-prod-state.secrets.enc`.
   - Distinct master keys / passphrase envelopes for staging and production.

3. **Webhook & Pull Request Deconfliction**:
   - Staging Grain configures a distinct GitHub App installation or issue label
     (`grain-stage` vs `grain`), ensuring staging and production agents never double-process
     the same issue or race on pull request review comments.
   - `-target-repos` flag is used on Staging Grain to restrict operation to staging
     or test repositories.

4. **Visual Disambiguation (`model.Config.EnvironmentName`)**:
   - Staging Grain displays an ambient amber warning badge in the sidebar and tab title
     (`[staging] Grain — Tasks`).
   - Destructive actions (e.g. cluster reboot, secret rotation, bulk task cancellation)
     require typing the environment name to confirm on staging.

---

## 10. UI & Frontend representations

1. **Task & Pull Request View**:
   - Dedicated **Environment Panel** showing active preview/staging deployment.
   - Live URL pill with "Open Preview" button and health indicator (Green = Healthy,
     Amber = Provisioning, Red = Failed).
   - "Redeploy to Preview" button and "Teardown" action.

2. **Releases & Qualification View**:
   - Release candidate card displays qualification staging deployment status.
   - Links to staging logs and metrics dashboards for the candidate build.

3. **Merge Queue View**:
   - Active batch card shows the current staging validation step (Deploying -> Smoke Tests -> Clean).
   - Real-time lease lock status and waiting queue depth.

4. **Environments Dashboard (`/environments`)**:
   - Tab listing all declared shared and ephemeral environments across repositories.
   - Live lease monitor showing which task currently holds each shared staging environment.

---

## 11. Implementation plan & phased delivery

### Phase 1: Data Model, Storage, and Declarations (1 week)
- Add `model.Environment`, `model.EnvironmentInstance`, and `model.EnvironmentLease`
  structs and SQLite migrations in `pkg/model/sqlite/`.
- Implement YAML parser for `.grain/environments/*.yml` in `pkg/declarations/`.
- Extend `pkg/model/compose.go` to support environment bindings on templates and suites.

### Phase 2: Lease Management & Reconcilers (2 weeks)
- Implement `pkg/orchestrator/environments.go` (`SyncEnvironments`) for provisioning,
  health checking, and TTL reaping.
- Implement `pkg/orchestrator/lease.go` (`SyncEnvironmentLeases`) for FIFO concurrency
  control and task gating.
- Add unit and integration tests simulating concurrent tasks contending for shared leases.

### Phase 3: Merge Queue & Qualification Integration (1.5 weeks)
- Extend `pkg/orchestrator/mergebatch.go` to deploy speculative batches to staging
  and execute staging verification suites before merging to `main`.
- Update `pkg/orchestrator/qualifications.go` to deploy release candidates to staging
  environments during qualification plans.

### Phase 4: Agent MCP Tools & UI Dashboard (1.5 weeks)
- Implement `mcp_grain-sandbox_environment_*` tools in `pkg/granule` and `pkg/mcpserver`.
- Build UI Environment Panel in `ui/src/components/EnvironmentPanel.jsx` and
  Environments dashboard view in `ui/src/views/EnvironmentsView.jsx`.

---

## 12. Alternatives considered and trade-offs

| Alternative | Considered | Reason Rejected / Why Chosen Instead |
|---|---|---|
| **Rely solely on sandbox Docker/Kind** | Agents create containers inside VM | Insufficient for multi-service cloud topologies, live database migrations, or realistic OAuth/third-party integrations. |
| **Uncoordinated shared staging** | Tasks deploy to staging without locks | Leads to race conditions where two agents overwrite each other's databases and generate false-positive test failures. |
| **External CI-driven preview environments** | Grain waits on external GitHub Actions | High latency (10-30 min per roundtrip), no agent feedback loop, and vulnerable to external Actions quota exhaustion. |
| **Level-triggered Leases in Grain (Chosen)** | Native `EnvironmentLease` and `SyncEnvironments` | Fully integrated into Grain's task queue, merge queue, and qualification runs with zero external dependencies. |

---

## 13. Summary of settled decisions

1. **Declarative**: Environments are declared alongside code in `.grain/environments/`.
2. **Safe**: Concurrency on shared environments is strictly governed by `EnvironmentLeases`.
3. **Clean**: Ephemeral environments are guaranteed to be reaped on task completion or TTL expiry.
4. **Scoped**: Staging tasks receive only staging secrets and staging capabilities.
5. **Integrated**: Merge queue batches and release candidates qualify on staging before promoting.
