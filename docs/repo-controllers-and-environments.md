# Design: Networked Repo Controllers for Dynamic Environments and State Machines

> **Design Specification.**
> This document defines the architecture, network RPC contract, authentication model,
> environment/capability interfaces, and security boundaries for per-repository **Repo Controllers**.
> Repo Controllers run inside Grain's **Long-Running (Standing) Sandboxes** and communicate with the
> Grain daemon over the network via authenticated gRPC/HTTP/2, designed for production deployments
> in Kubernetes and Google Kubernetes Engine (GKE) clusters.
>
> *Note:* For autonomous agent-driven repository governance and two-sandbox provisioner models,
> see also [docs/repo-managers-and-environments.md](repo-managers-and-environments.md).

---

## 1. Executive Summary & Architectural Vision

In Grain, tasks run inside isolated sandboxes (lightweight Kontur microVMs or container guests) where
agents make edits, run unit tests, and interact with repository tooling. However, realistic software
engineering workflows frequently require interaction with **external cloud resources and deployed
runtime environments**—such as spinning up disposable Kubernetes preview namespaces, provisioning
temporary databases, running migrations against staging clusters, or driving custom repository
governance state machines.

Historically, capability and environment management in orchestrators has suffered from a painful trade-off:

1. **Monolithic In-Daemon Drivers**: Embedding provider logic (Kubernetes clients, AWS/GCP SDKs,
   Helm, database migration runners) directly into the orchestrator daemon (`graind`). This bloats the
   daemon binary, requires operator-level cloud credentials inside the central orchestrator, and
   tightly couples repository workflows to orchestrator release cycles.
2. **Uncoordinated Sandbox Scripting**: Granting raw cloud admin credentials directly to the task sandbox so
   the agent or CI scripts can provision cloud infrastructure on demand. This creates a severe security risk,
   expands the credential blast radius, and risks orphaned cloud resources if an agent crashes, times out,
   or fails to execute cleanup scripts.

### The Networked Repo Controller Model

Grain solves this by running per-repository **Repo Controllers** inside Grain's **Long-Running (Standing) Sandboxes**.
These controllers run as persistent network services in container guests or microVMs across the cluster network.
The Grain daemon and Repo Controllers communicate over actual network connections using secure gRPC / HTTP/2.

```
┌────────────────────────────────────────────────────────────────────────┐
│                              Grain Daemon                              │
│  - Schedules tasks & provisions empty sandboxes                        │
│  - Supervises Standing Sandboxes (lifecycle, health, restarts)         │
│  - Calls Repo Controller over network gRPC (mTLS / Token Auth)         │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    │ Network gRPC (mTLS / Workload Identity)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│       Repository Controller (Long-Running Standing Sandbox)            │
│  - Runs persistent service implementing RepoController gRPC server     │
│  - Holds repo provisioning credentials & state machine                 │
│  - Pluggable backend drivers (K8s client, cloud SDKs, custom APIs)     │
└───────────────┬────────────────────────────────────────┬───────────────┘
                │                                        │
                │ 1. PrepareEnvironment(TaskRequest)     │ 3. CleanupEnvironment(...)
                │    -> returns Placements & Env vars    │    -> reclaims resources
                ▼                                        ▼
┌────────────────────────────────────────────────────────────────────────┐
│                              Task Sandbox                              │
│  - Starts with files placed in guest fs & injected env vars            │
│  - Agent runs cleanly; completely decoupled from cloud provisioning    │
└────────────────────────────────────────────────────────────────────────┘
```

A Repo Controller fulfills two unified responsibilities:

1. **Dynamic Environment & Capability Provider**:
   The controller acts as the authority on the repository's infrastructure environments. It advertises
   supported environments via `GetEnvironments`, sets up scoped runtime dependencies and capability
   placements prior to task launch via `PrepareEnvironment`, and deterministically reclaims cloud
   infrastructure via `CleanupEnvironment`.
2. **Repository State Machine**:
   The controller subscribes to task and merge lifecycle events via `OnTaskEvent`. It coordinates
   speculative merge queue batches, drives release train qualification pipelines, validates branch
   protections, and enforces repository-specific governance policies.

---

## 2. System Architecture & Isolation Boundaries

### 2.1 The Three Planes

The execution model is partitioned into three strictly separated tiers:

```
┌───────────────────────────────────────────────────────────────────────────────────────┐
│ CONTROL PLANE: Grain Daemon (graind)                                                  │
│ - Global task scheduling, queue arbitration, lease management                         │
│ - Standing sandbox supervision & task sandbox lifecycle management                    │
│ - Dispatches lifecycle RPCs over authenticated network gRPC (TCP / mTLS)              │
└──────────────────────────────────────────┬────────────────────────────────────────────┘
                                           │ Network gRPC (mTLS / Token Auth)
                                           ▼
┌───────────────────────────────────────────────────────────────────────────────────────┐
│ GOVERNANCE & PROVISIONING PLANE: Repository Controller (Standing Sandbox)             │
│ - Long-running standing sandbox running per target repository in the GKE cluster      │
│ - Holds privileged cloud credentials (GCP SA keys, AWS roles, K8s cluster admin)     │
│ - Implements the Environment & Capability interface with repository-specific drivers  │
│ - Maintains state machine state (merge queues, release trains, custom checks)         │
└──────────────────────────────────────────┬────────────────────────────────────────────┘
                                           │ Injected Files & Env (via Daemon)
                                           ▼
┌───────────────────────────────────────────────────────────────────────────────────────┐
│ EXECUTION PLANE: Task Sandbox (Kontur MicroVM / Ephemeral Container)                  │
│ - Ephemeral, zero-credential guest environment for untrusted LLM code execution       │
│ - Receives only scoped capability artifacts (e.g. ephemeral kubeconfig, DB DSN)       │
│ - Zero direct network or API access to the Governance Plane or Cloud Admin APIs       │
└───────────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Network Topology & GKE Cluster Environment

In modern cloud deployments (such as Google Kubernetes Engine):
- **Grain Daemon** runs as an orchestrator deployment within the cluster (or on a dedicated control plane host).
- **Repo Controllers** run inside Grain's **Standing Sandboxes** hosted as long-lived pods or microVMs
  within the GKE cluster. They are accessible over internal network addresses (e.g. Kubernetes Service
  ClusterIPs or pod network endpoints such as `repo-controller-bwsalmon-grain.grain-system.svc.cluster.local:8443`).
- **Network Isolation**: Kubernetes `NetworkPolicy` objects and firewall rules enforce that only the
  Grain Daemon control plane can open network connections to the Repo Controller's gRPC port. Task sandboxes
  are strictly prohibited from routing traffic to Repo Controller endpoints.

### 2.3 Security & Isolation Guarantees

1. **Credential Blast Radius Isolation**:
   High-privilege cloud admin credentials (e.g. GCP Project Owner/Editor service account keys,
   AWS administrative IAM roles, root Kubernetes cluster certificates) reside exclusively inside the
   Repo Controller standing sandbox. They are never mounted, written, or exposed to the task sandbox.
2. **Minimal Sandbox Footprint via Scoped Placements**:
   The task sandbox receives only the minimally scoped, ephemeral artifacts generated during environment
   preparation. For example, instead of a cluster-wide Kubernetes admin token, the sandbox receives a temporary
   `kubeconfig` restricted by RBAC to a single ephemeral namespace (`grain-preview-task-105`). Instead of a root
   database superuser password, the sandbox receives an ephemeral database user connection string with access
   only to a dynamically generated preview database.
3. **Guaranteed Zero-Leak Teardown**:
   Resource reclamation is decoupled from sandbox guest health. Even if an agent crashes, gets killed due
   to timeout, runs out of memory, or has its sandbox forcibly destroyed, the Grain daemon's level-triggered
   reconciler guarantees that `CleanupEnvironment` is executed on the Repo Controller with the opaque context
   data generated during preparation.

---

## 3. Hosting Repo Controllers on Long-Running Sandboxes

Grain uses its native **Long-Running (Standing) Sandbox** infrastructure (`model.StandingSandbox` and
`StandingSandboxSupervisor` in `pkg/orchestrator/standing_sandboxes.go`) to host and manage Repo Controllers.

### 3.1 Standing Sandbox Architecture & Supervisor Integration

A Repo Controller is declared as a `StandingSandbox` record in the `grain/config` state repository.
Not in `.grain/settings.yml`, as an earlier draft of this document had it: a controller resolves
secrets and is granted capabilities, and grain/task-340 settled that a repository never declares
either for itself (docs/declarations.md, "authority, not behaviour"). Grain's supervisor ensures
high availability:
1. **Boot & Lifecycle Supervision**: The Grain daemon starts the controller sandbox on orchestrator boot,
   monitors guest process liveness, and automatically restarts failed sandboxes with exponential backoff.
2. **Secret Resolution**: Master cloud credentials and authentication materials are resolved from Grain's
   encrypted secret store via `StandingSandboxSecret` and securely injected into the standing sandbox as
   environment variables or filesystem secrets before the controller entrypoint launches.
3. **Resource Sizing & Network Configuration**: Dedicated CPU, memory, and disk allocations (`CPUs`,
   `MemoryMB`, `DiskGB`) are assigned along with network access policies (`NetworkAccess`).

### 3.2 Additions for Networked Repo Controllers

To support networked Repo Controllers running within standing sandboxes, the standing sandbox model
incorporates the following extensions:

```go
// StandingSandbox extension for network service endpoints
type StandingSandboxService struct {
    // Port is the container/microVM listening port (e.g. 8443 for gRPC).
    Port int `json:"port"`

    // Protocol is the application protocol ("grpc", "http2", "https").
    Protocol string `json:"protocol"`

    // HealthCheck configures the readiness probe for the standing sandbox service.
    HealthCheck ServiceHealthCheck `json:"health_check"`

    // Repo binds this standing sandbox to a specific repository ("owner/name").
    Repo string `json:"repo,omitempty"`
}

type ServiceHealthCheck struct {
    // Type specifies the probe mechanism: "grpc" (gRPC health check v1) or "http" (HTTP GET).
    Type string `json:"type"`

    // Path is the HTTP endpoint path (e.g. "/healthz"), used when Type is "http".
    Path string `json:"path,omitempty"`

    // Interval is the polling frequency between health checks.
    Interval time.Duration `json:"interval"`
}
```

- **Service Endpoint Discovery**: Grain resolves the IP and port of the standing sandbox's running container/microVM
  and creates a persistent, pooled gRPC client connection.
- **Readiness Probing**: Before dispatching `PrepareEnvironment` or `OnTaskEvent` calls, the daemon asserts
  that the controller's gRPC health service (`grpc.health.v1.Health/Check`) returns `SERVING`.
- **Dynamic Reloads & Image Updates**: When a new controller OCI image is pushed or repository settings update,
  the supervisor orchestrates a graceful drain and replacement of the controller instance.

---

## 4. Authentication & Authorization

Because Repo Controllers and the Grain daemon communicate over the network across pod, node, and cluster
boundaries, all communication must be authenticated, encrypted, and authorized.

```
                      Mutual Authentication & Authorization
  ┌──────────────────┐                                     ┌──────────────────┐
  │   Grain Daemon   │                                     │ Repo Controller  │
  │                  │  1. mTLS Handshake (TLS 1.3)        │ (Standing Box)   │
  │                  │◄───────────────────────────────────►│                  │
  │                  │                                     │                  │
  │                  │  2. gRPC Call + Bearer Token / JWT  │                  │
  │                  ├────────────────────────────────────►│                  │
  │                  │     (Validate Repo Scope & Claims)  │                  │
  │                  │                                     │                  │
  │                  │  3. Authenticated RPC Response      │                  │
  │                  │◄────────────────────────────────────┤                  │
  └──────────────────┘                                     └──────────────────┘
```

### 4.1 Transport Security & Mutual TLS (mTLS)

All network communication uses **TLS 1.3**:
- **Server Authentication**: The Repo Controller presents an X.509 certificate signed by a trusted
  internal Certificate Authority (Grain Internal CA or SPIRE/SPIFFE). The Grain daemon validates that the
  SAN (Subject Alternative Name) matches the expected controller identity (`repo-controller.<repo>.grain.internal`).
- **Client Authentication (mTLS)**: The Grain daemon presents a client certificate asserting its
  orchestrator identity (`grain-daemon.grain-system.grain.internal`). The Repo Controller rejects any
  connection from unauthorized network clients.

### 4.2 GKE Workload Identity & Kubernetes Service Account Tokens

When running inside Kubernetes / GKE:
1. **Projected Service Account Tokens**: The Grain daemon obtains an RFC 7519 JWT token projected via
   the Kubernetes `TokenRequest` API with the specific audience (`grain-repo-controller`).
2. **Token Verification**: The Repo Controller verifies the JWT token against the Kubernetes API Server OIDC
   discovery keys (`/.well-known/openid-configuration`), asserting:
   - `iss`: Valid Kubernetes cluster issuer.
   - `aud`: `grain-repo-controller` or specific controller target.
   - `sub`: `system:serviceaccount:grain-system:grain-daemon`.
3. **GKE Workload Identity Binding**: Repo Controller pods bind to specific Google Service Accounts (GSAs)
   via GKE Workload Identity to interact with Google Cloud APIs (e.g. GKE cluster APIs, Cloud SQL Admin,
   Secret Manager) without managing static service account keys on disk.

### 4.3 Scoped Bearer Tokens & Secret Store Integration

For deployments using token-based authentication:
- **Token Injection via StandingSandboxSecret**: Grain provisions a cryptographically random, per-repository
  secret token stored in Grain's encrypted secret store and mounts it into the controller sandbox via
  `StandingSandboxSecret`.
- **gRPC Metadata**: Every RPC request carries the credential in the standard authorization header:
  `authorization: Bearer <token>`.
- **Repository Tenancy Enforcement**: The Repo Controller strictly verifies that incoming requests match
  the repository it governs (`repo: "owner/name"`). A controller refuses any request attempting to act on
  or inspect another repository's tasks or resources.

---

## 5. Environment & Capability Interface Definition

Rather than prescribing specific infrastructure tools (such as Terraform, Helm, or raw shell scripts),
Grain defines a clean, declarative **Environment and Capability Interface**. The Repo Controller is free
to implement the interface using any underlying provisioning engine (Kubernetes API client, cloud SDKs,
custom Go controllers, Terraform/OpenTofu, Pulumi, or container APIs).

### 5.1 Concepts: Environments vs. Capabilities

- **Environment**: A named target runtime context (e.g. `preview`, `staging`, `load-test`, `isolated-db`)
  specifying concurrency rules, TTLs, and required parameters.
- **Capability**: A discrete resource grant, injected configuration, or permission delivered to a sandbox
  or CI job (e.g. database credentials, namespace access, API proxy URLs, secret tokens).
- **Placement**: A concrete material artifact (file or environment variable) written to the sandbox guest
  before task execution, matching Grain's core capability model (`pkg/model/capability.go`).
- **ContextData**: An opaque state map preserved by the Grain daemon across the task lifecycle and passed
  back during cleanup to enable deterministic resource destruction.

### 5.2 Go Interface & Data Model (`pkg/repocontroller`)

```go
package repocontroller

import (
    "context"
    "time"
)

// EnvironmentDesc describes an environment supported by the repository controller.
type EnvironmentDesc struct {
    Name         string            `json:"name"`          // e.g. "preview", "staging", "isolated-db"
    Description  string            `json:"description"`   // Human-readable description for Grain UI
    Concurrency  int               `json:"concurrency"`   // 0 = unlimited ephemeral, 1 = exclusive mutex
    DefaultTTL   time.Duration     `json:"default_ttl"`   // Default lifetime before forced cleanup
    MaxTTL       time.Duration     `json:"max_ttl"`       // Maximum permissible lifetime
    Capabilities []string          `json:"capabilities"`  // Capability names provided by this environment
    Parameters   []ParameterSpec   `json:"parameters"`    // Expected/optional parameter declarations
    Metadata     map[string]string `json:"metadata"`      // Custom annotations and labels
}

// ParameterSpec defines a parameter accepted by the environment.
type ParameterSpec struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Required    bool   `json:"required"`
    Default     string `json:"default,omitempty"`
}

// TaskRequest contains task metadata and parameters provided to PrepareEnvironment.
type TaskRequest struct {
    TaskID      int64             `json:"task_id"`
    SandboxID   string            `json:"sandbox_id"`
    Environment string            `json:"environment"` // Target environment name
    Repo        string            `json:"repo"`        // "owner/name"
    Branch      string            `json:"branch"`      // Branch name (e.g. "grain/task-105")
    CommitSHA   string            `json:"commit_sha"`  // Git commit SHA being tested
    TargetRef   string            `json:"target_ref"`  // Base branch or release candidate ref
    Author      string            `json:"author"`      // Task trigger author / actor
    Parameters  map[string]string `json:"parameters"`  // Custom parameters from template/schedule
    Metadata    map[string]string `json:"metadata"`    // Custom metadata tags
}

// Side is where a Placement lands (sandbox guest or controller).
type Side string

const (
    SideSandbox    Side = "sandbox"
    SideController Side = "controller"
)

// Placement represents a concrete file or secret material placed in the sandbox guest.
type Placement struct {
    Side    Side   `json:"side"`     // Target location (usually SideSandbox)
    Path    string `json:"path"`     // Guest filesystem path (e.g. "/home/debian/.kube/config")
    Content string `json:"content"`  // File content
    Mode    string `json:"mode"`     // File permissions (e.g. "0600")
    Owner   string `json:"owner"`    // File ownership (e.g. "debian:debian")
}

// EnvironmentPreparation contains the scoped files, env vars, and URLs injected into the sandbox.
type EnvironmentPreparation struct {
    // Placements defines files written to the sandbox guest filesystem.
    Placements  []Placement       `json:"placements"`

    // Env defines environment variables injected into the sandbox guest process.
    Env         map[string]string `json:"env"`

    // URLs contains interactive URLs (e.g. preview endpoint, Grafana dashboard) for the Grain UI.
    URLs        map[string]string `json:"urls"`

    // ContextData is opaque state preserved by Grain daemon and returned to CleanupEnvironment.
    ContextData map[string]string `json:"context_data"`

    // Lease optionally specifies lease expiration and renewal parameters.
    Lease       *EnvironmentLease `json:"lease,omitempty"`
}

// EnvironmentLease tracks time-bounded lease reservations.
type EnvironmentLease struct {
    LeaseID   string    `json:"lease_id"`
    ExpiresAt time.Time `json:"expires_at"`
}

// CleanupRequest supplies the context needed to tear down allocated resources.
type CleanupRequest struct {
    TaskID      int64             `json:"task_id"`
    SandboxID   string            `json:"sandbox_id"`
    Environment string            `json:"environment"`
    Repo        string            `json:"repo"`
    Status      string            `json:"status"`       // "succeeded", "failed", "cancelled", "timed_out"
    ContextData map[string]string `json:"context_data"` // Opaque state from PrepareEnvironment
}

// CleanupResponse indicates the result of the resource reclamation.
type CleanupResponse struct {
    ReclaimedResources []string `json:"reclaimed_resources"`
}

// TaskEvent represents a lifecycle event dispatched to the repository state machine.
type TaskEvent struct {
    EventType   TaskEventType     `json:"event_type"`
    TaskID      int64             `json:"task_id"`
    RunID       int64             `json:"run_id,omitempty"`
    SandboxID   string            `json:"sandbox_id,omitempty"`
    Environment string            `json:"environment,omitempty"`
    Repo        string            `json:"repo"`
    Branch      string            `json:"branch"`
    CommitSHA   string            `json:"commit_sha"`
    Status      string            `json:"status,omitempty"` // "succeeded", "failed", "cancelled", etc.
    Error       string            `json:"error,omitempty"`
    Timestamp   time.Time         `json:"timestamp"`
    ContextData map[string]string `json:"context_data,omitempty"`
}

type TaskEventType string

const (
    EventTaskDispatched     TaskEventType = "task.dispatched"
    EventTaskRunning        TaskEventType = "task.running"
    EventTaskSucceeded      TaskEventType = "task.succeeded"
    EventTaskFailed         TaskEventType = "task.failed"
    EventTaskCancelled      TaskEventType = "task.cancelled"
    EventTaskTimedOut       TaskEventType = "task.timed_out"
    EventPRCreated          TaskEventType = "pr.created"
    EventPRUpdated          TaskEventType = "pr.updated"
    EventMergeBatchStarted  TaskEventType = "merge_batch.started"
    EventMergeBatchPassed   TaskEventType = "merge_batch.passed"
    EventMergeBatchFailed   TaskEventType = "merge_batch.failed"
    EventReleaseCandidate   TaskEventType = "release.candidate_cut"
    EventReleaseQualified   TaskEventType = "release.qualified"
)

// RepoControllerServer defines the network gRPC service exposed by the Repo Controller.
type RepoControllerServer interface {
    // GetEnvironments lists all environments and capabilities managed by this controller.
    GetEnvironments(ctx context.Context) ([]EnvironmentDesc, error)

    // PrepareEnvironment provisions or allocates required cloud/cluster resources for the task
    // and returns the scoped placements, environment variables, and URLs to place in the sandbox.
    PrepareEnvironment(ctx context.Context, req TaskRequest) (*EnvironmentPreparation, error)

    // CleanupEnvironment tears down any ephemeral resources allocated during PrepareEnvironment.
    CleanupEnvironment(ctx context.Context, req CleanupRequest) (*CleanupResponse, error)

    // OnTaskEvent notifies the repository state machine of lifecycle milestones.
    OnTaskEvent(ctx context.Context, event TaskEvent) error
}
```

### 5.3 Network Protocol & Lifecycle Sequence

```
Grain Daemon                Repo Controller (Standing Sandbox)      Task Sandbox
     │                                 │                                 │
     ├─────── GetEnvironments() ──────►│                                 │
     │◄────── [EnvironmentDesc] ───────┤                                 │
     │                                 │                                 │
     │ (Task Scheduled on Environment) │                                 │
     ├─────── PrepareEnvironment(req) ─►│ (allocates namespace / db)     │
     │◄────── EnvironmentPreparation ──┤                                 │
     │                                 │                                 │
     ├────────────────────── Boot & Inject Placements / Env ────────────►│
     │                                 │                                 │
     ├─────── OnTaskEvent(running) ───►│ (updates repo state machine)    │
     │                                 │                                 │
     │                                 │            [Agent Works]        │
     │                                 │                                 │
     │◄───────────────────── Task Completes / Fails ─────────────────────┤
     │                                 │                                 │
     │                                 │                                 │
     ├─────── OnTaskEvent(succeeded) ─►│ (triggers merge queue / triage) │
     │                                 │                                 │
     ├─────── CleanupEnvironment(req) ─►│ (deletes ephemeral resources)   │
     │◄────── CleanupResponse ─────────┤                                 │
     ▼                                 ▼                                 ▼
```

### 5.4 Invariants of the Interface

1. **Idempotency**: `PrepareEnvironment`, `CleanupEnvironment`, and `OnTaskEvent` must be idempotent.
   If a network timeout or reconnection occurs, the Grain daemon will retry with the identical `TaskID`
   and `ContextData`.
2. **Deterministic Context Passing**: Any state needed for teardown (e.g. dynamic namespace names,
   cloud resource IDs, lease IDs) is stored in `EnvironmentPreparation.ContextData` and persisted
   by Grain in its database. Grain guarantees that the identical map is passed to `CleanupEnvironment`.
3. **Dead-man Cleanup Guarantee**: If the Repo Controller is temporarily unreachable when a task
   completes, Grain daemon marks the cleanup as `pending_cleanup` in its state store and retries
   with exponential backoff until the controller acknowledges the reclamation.

---

## 6. Repository State Machine & Governance Workflows

In addition to environment provisioning, the Repo Controller serves as the repository's programmable
governance and lifecycle engine through `OnTaskEvent`.

```
                        Repository State Machine
    ┌──────────────────────────────────────────────────────────────┐
    │                                                              │
    │  Task Event Stream                                           │
    │  - task.dispatched ──► Enforce branch policies & check caps  │
    │  - task.succeeded  ──► Advance merge queue or qualification  │
    │  - task.failed     ──► Trigger triage / halving logic        │
    │  - pr.created      ──► Add automated review / preview URLs   │
    │                                                              │
    └──────────────────────────────────────────────────────────────┘
```

### 6.1 Merge Queue Batching Coordination

When merge queue batching is active (`docs/merge-queue-batching.md`):

1. **Speculative Batch Setup**: When Grain daemon forms a speculative merge batch on
   `grain-merge-queue/<branch>`, it notifies the Repo Controller via `EventMergeBatchStarted`.
2. **Staging Validation Gate**:
   - The controller claims an exclusive lease (`concurrency: 1`) on the shared staging environment.
   - It deploys the speculative batch commit to staging.
   - It signals the Grain daemon to execute the staging integration suite.
3. **Batch Qualification & Splitting**:
   - If the batch passes, the controller emits approval, and Grain fast-forwards all pull requests in
     the batch to `main`.
   - If the batch fails, the controller executes the batch-halving algorithm to isolate the failing PR
     without ever polluting the `main` branch or production staging.

### 6.2 Release Train Candidate Qualification

For scheduled or manual release trains:

1. `EventReleaseCandidate` is emitted when a release candidate (`rel.2026.09.19`) is cut.
2. The Repo Controller provisions a high-fidelity qualification environment (e.g. cloning production
   schema, populating synthetic test data).
3. The controller orchestrates multi-stage qualification tasks:
   - Migration idempotency checks.
   - Load and soak testing.
   - End-to-end browser walkthroughs.
4. Upon all qualification tasks passing, the controller signals `EventReleaseQualified`, enabling
   automatic promotion to production.

### 6.3 Custom Repository Policies & Governance

Repositories often require custom policies that do not belong in generic orchestrator core logic:
- **Pre-Dispatch Checks**: Rejecting tasks targeting protected branches without appropriate approvals.
- **Custom Notifications**: Emitting formatted Slack or Webhook alerts when specific capabilities
  (e.g., production database migrations) are exercised.
- **Dynamic Linters**: Enforcing custom compliance, licensing, or security checks against PR diffs
  before allowing merge queue admission.

---

## 7. Comparison with Earlier Architecture

| Architectural Dimension | Earlier Draft (`docs/staging-environments.md`) | Networked Repo Controllers (`docs/repo-controllers-and-environments.md`) |
|---|---|---|
| **Hosting Substrate** | In-process daemon goroutines | Grain Long-Running (Standing) Sandboxes (`StandingSandbox`) |
| **Communication** | Central daemon internal method calls | Network gRPC over TCP / HTTP/2 with mTLS & Workload Identity |
| **Runtime Target** | Monolithic host process (`graind`) | Distributed Kubernetes/GKE Pods & Services or microVMs |
| **Authentication** | In-process (none) | mTLS + GKE Workload Identity / Kubernetes ServiceAccount JWTs |
| **Driver Abstraction** | Hardcoded monolithic drivers | Declarative Environment & Capability Interface (engine agnostic) |
| **Credential Scope** | Central daemon held all cloud credentials | Cloud credentials isolated inside per-repo Standing Sandboxes |
| **State Machine** | Rigid, built-in daemon lifecycle | Customizable repository state machine (`OnTaskEvent`) |
| **Sandbox Isolation** | Potential sandbox cloud API exposure | Zero cloud API access; sandboxes receive strictly scoped Placements |
| **Resource Reclamation**| Daemon-driven ad-hoc cleanup | Standardized `CleanupEnvironment` with guaranteed ContextData passing |

---

## 8. Summary of Core Invariants

1. **Network-First Communication**: Controllers communicate with the Grain daemon over authenticated
   network gRPC (TCP / HTTP/2), enabling seamless operation within and across GKE/Kubernetes clusters.
2. **Hosted on Standing Sandboxes**: Controllers run inside Grain's long-running standing sandboxes,
   benefiting from automatic health monitoring, supervisor restarts, and secret store injection.
3. **Mutual Authentication**: All network RPCs require cryptographic identity verification (mTLS,
   GKE Workload Identity / Kubernetes ServiceAccount OIDC tokens, or scoped Bearer tokens) with strict
   per-repository authorization.
4. **Pluggable Environment / Capability Interface**: The controller implements declarative RPCs
   (`GetEnvironments`, `PrepareEnvironment`, `CleanupEnvironment`, `OnTaskEvent`) without binding Grain
   to specific infrastructure tooling or vendor implementations.
5. **Strict Privilege Separation**: Admin cloud credentials remain in the Repo Controller standing sandbox.
   Sandboxes receive only minimally scoped `Placement` files and environment variables.
6. **Guaranteed Zero-Leak Teardown**: Ephemeral resources are deterministically destroyed via
   `CleanupEnvironment`, ensuring zero leaked cloud infrastructure regardless of task exit conditions.
