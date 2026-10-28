# Design: Long-Lived Repo Managers, Dynamic Environments, and Two-Sandbox Provisioning

> **Architectural Specification.**
> This document defines the architecture, substrate API, two-sandbox security model, dynamic
> environment provisioning, and autonomous scheduling for **Long-Lived Repo Managers**.
> It unifies repository governance, dynamic preview environments, and standing interactive
> agents into a single primitive, superseding static in-daemon reconcilers
> ([docs/staging-environments.md](staging-environments.md)) and static MicroVM controllers
> ([docs/repo-controllers-and-environments.md](repo-controllers-and-environments.md)), and
> generalizing the Director console session pattern ([docs/director.md](director.md)) across
> all managed repositories.

---

## 1. Executive Summary & Architectural Vision

In Grain, tasks have historically run inside isolated sandboxes (lightweight Kontur microVMs or container guests) where ephemeral worker agents make code edits, run unit tests, and post pull requests. However, as engineering workflows grow in complexity, orchestrating multi-service testing, managing merge queues, triaging incoming failures, and scheduling maintenance chores require more than isolated worker sandboxes.

Prior iterations explored two models:
1. **Centralized Daemon Reconcilers** (`docs/staging-environments.md`): Embedding environment management, lease tracking, and scheduling directly into `graind`. This bloated the central daemon with cloud provider logic and credential handling.
2. **Granule-based Repo Controllers** (`docs/repo-controllers-and-environments.md`): Offloading provisioning to per-repo Granule microVMs executing static RPC handlers (`PrepareSandbox`, `CleanupSandbox`, `OnTaskEvent`). While this isolated cloud credentials, it treated repository orchestration as a rigid, programmatic state machine lacking autonomous reasoning, intelligent triage, and conversational adaptability.

### The Unified Primitive: The Long-Lived Repo Manager

Grain unifies these disparate concepts into a single core primitive: the **Repo Manager Agent**.

```
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                 Grain Orchestration Plane                              │
│                                                                                        │
│  ┌──────────────────────────────┐              ┌────────────────────────────────────┐  │
│  │     The Director Agent       │              │    Application Repo Manager Agent  │  │
│  │  (Repo Manager: grain/config)│              │     (Repo Manager: app/backend)    │  │
│  └──────────────┬───────────────┘              └─────────────────┬──────────────────┘  │
│                 │                                                │                     │
│                 │ Substrate API / MCP Primitives                 │                     │
│                 ▼                                                ▼                     │
│  ┌──────────────────────────────────────────────────────────────────────────────────┐  │
│  │                                Grain Daemon (Kernel)                             │  │
│  │  - MicroVM isolation & sandbox lifecycle                                         │  │
│  │  - Minimal Substrate API (CreateTask, StartTaskAgent, StartCIJob, MergePR)       │  │
│  │  - Event routing & zero-polling wakeup engine                                    │  │
│  └──────────────────────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────────────────────┘
```

Under this architecture:
- **Every repository is governed by a standing, long-lived Repo Manager session**.
- **The Director is simply the Repo Manager for `grain/config`**, managing deployment-wide settings, global schedules, and operator chat.
- **Application Repo Managers** govern individual repositories, autonomously managing issue triage, qualification gates, speculative merge queues, and dynamic environment provisioning.
- **Dynamic environment provisioning uses a Two-Sandbox Security Model**, strictly separating high-privilege cloud admin credentials (held exclusively by hermetic provisioning sandboxes) from untrusted LLM-generated code (executed in low-privilege task worker sandboxes).
- **Scheduling and webhooks are decentralized**: Central cron tables (`tables/schedules.json`) are deprecated in favor of autonomous Repo Manager timers and intelligent webhook evaluation.

---

## 2. The Unified Primitive: The Long-Lived Repo Manager

### 2.1 Standing Session Model & Lifecycle

A Repo Manager is a standing agent session dedicated to a specific repository. Unlike ephemeral task workers that spin up, execute a specific job, and terminate, the Repo Manager persists throughout the lifetime of the repository's registration in Grain.

```
                              Repo Manager Lifecycle
                              
             ┌──────────────────────────────────────────────────────┐
             │                     Parked State                     │
             │  - Zero CPU / token consumption                      │
             │  - Awaiting event wakeup (Webhook, CI, Task finish)  │
             └──────────────────────────┬───────────────────────────┘
                                        │
                         Event Wakeup / Operator Input
                                        ▼
             ┌──────────────────────────────────────────────────────┐
             │                     Active Turn                      │
             │  - Evaluates repository state & event payload        │
             │  - Reason over PRs, failed runs, or schedule timers  │
             │  - Calls Grain Substrate API (CreateTask, MergePR)   │
             │  - Provisions preview environments if needed         │
             └──────────────────────────┬───────────────────────────┘
                                        │
                             Yield / Turn Completion
                                        │
             ┌──────────────────────────▼───────────────────────────┐
             │                  Standing Handover                   │
             │  - Updates standing context & memory state           │
             │  - Parks back on event queue                         │
             └──────────────────────────────────────────────────────┘
```

#### Lifecycle Rules & Invariants
1. **Filed Approved**: The Repo Manager session is created automatically upon repository registration in Grain. It requires no manual approval to start.
2. **Never Done**: When a Repo Manager finishes processing an event turn, it does not mark its standing task as `completed`. Instead, Grain parks the session on its event queue.
3. **Never Retired for Failing**: Transient turn errors do not retire or deactivate the Repo Manager. The daemon logs the error, preserves state, and re-parks the session for subsequent events.
4. **Zero-Polling Parked State**: When no events or messages are queued, the Repo Manager consumes zero inference tokens and zero host compute. It is reactivated instantly when an event arrives.

### 2.2 Prompt Construction & Repository Specialization

The Repo Manager's system prompt is dynamically assembled from two layers:

```
┌────────────────────────────────────────────────────────────────────────┐
│ 1. Grain Substrate Base Prompt (pkg/repomanager/prompt.go)             │
│    - Explains Grain kernel primitives (Tasks, CI, Sandboxes, Merges)   │
│    - Defines available MCP tools and security invariants               │
│    - Instructs on standing handover maintenance                        │
├────────────────────────────────────────────────────────────────────────┤
│ 2. Repository Specialization (.grain/manager.md)                       │
│    - Domain-specific triage guidelines and labeling rules              │
│    - Dynamic preview environment requirements & qualification gates    │
│    - Speculative merge queue policies and branch protection rules      │
│    - Recurring chore schedules (nightly fuzzing, dependency updates)   │
└────────────────────────────────────────────────────────────────────────┘
```

### 2.3 The Director vs. Application Repo Managers

The architecture unifies the existing Director concept (`docs/director.md`) with application repository governance:

| Dimension | The Director (`grain/config`) | Application Repo Manager (`org/repo`) |
|---|---|---|
| **Repository Scope** | `grain/config` (state & control plane repo) | Application source repository (e.g. `bwsalmon/grain`) |
| **Primary Focus** | Deployment-wide settings, repo registration, operator chat | Tasks, PR merge queues, CI diagnostics, preview environments |
| **Capabilities** | `grain-log`, `bootstrap-playbooks`, `self-repair` | `create_task`, `start_ci`, `provision_env`, `merge_pr` |
| **Operator Interface** | Main Director chat console in Grain UI rail | Repository governance pane & task comment threads |
| **Coordination** | Dispatches global policies & cross-repo initiatives | Coordinates local repository state and worker agents |

---

## 3. Grain Daemon's Minimal Substrate API

The Grain daemon (`graind`) is streamlined into a high-performance, minimal execution kernel. It strips out complex repository business logic, cron parsing, and ad-hoc staging reconcilers, exposing clean primitives to Repo Managers via gRPC and MCP tool interfaces.

```
┌────────────────────────────────────────────────────────────────────────┐
│                              Repo Manager                              │
│         (Autonomous Decision Making, Triage, Merge Orchestration)      │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                         Substrate API / MCP Calls
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                        Grain Daemon Kernel (graind)                    │
│                                                                        │
│  ┌───────────────────────┐  ┌───────────────────────────────────────┐  │
│  │ Task & Agent Engine   │  │ CI & Workflow Engine                  │  │
│  │ - CreateTask()        │  │ - StartCIJob(sha, branch)             │  │
│  │ - StartTaskAgent()    │  │ - GetCIVerdict(run_id)                │  │
│  │ - UpdateTaskState()   │  └───────────────────────────────────────┘  │
│  └───────────────────────┘  ┌───────────────────────────────────────┐  │
│  ┌───────────────────────┐  │ Event Wakeup Engine                   │  │
│  │ Merge Engine          │  │ - Webhook Ingestion                   │  │
│  │ - MergePullRequest()  │  │ - CI / Task Lifecycle Notifications   │  │
│  │ - ValidateBranch()    │  │ - Level-triggered Session Unparking   │  │
│  └───────────────────────┘  └───────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
```

### 3.1 Go Interface Definition (`pkg/repomanager/substrate.go`)

```go
package repomanager

import (
    "context"
    "time"
)

// Substrate defines the minimal kernel API provided by Grain daemon to Repo Managers.
type Substrate interface {
    // Task Lifecycle Primitives
    CreateTask(ctx context.Context, req CreateTaskRequest) (*TaskResponse, error)
    ListTasks(ctx context.Context, filter TaskFilter) ([]TaskSummary, error)
    UpdateTaskState(ctx context.Context, req UpdateTaskStateRequest) error
    StartTaskAgent(ctx context.Context, req StartAgentRequest) (*AgentRunResponse, error)

    // Environment & Sandbox Provisioning Primitives
    ProvisionEnvironment(ctx context.Context, req ProvisionRequest) (*ProvisionResult, error)
    TeardownEnvironment(ctx context.Context, req TeardownRequest) error

    // Continuous Integration & Qualification
    StartCIJob(ctx context.Context, req StartCIRequest) (*CIJobResponse, error)
    GetCIRunStatus(ctx context.Context, runID string) (*CIRunStatus, error)

    // Repository & Branch Operations
    MergePullRequest(ctx context.Context, req MergeRequest) (*MergeResponse, error)
    GetBranchStatus(ctx context.Context, repo, branch string) (*BranchStatus, error)

    // Handover & State Management
    SaveHandover(ctx context.Context, repo string, handover string) error
    GetHandover(ctx context.Context, repo string) (string, error)
}

// Data Models

type CreateTaskRequest struct {
    Repo        string            `json:"repo"`
    Title       string            `json:"title"`
    Prompt      string            `json:"prompt"`
    TargetRef   string            `json:"target_ref,omitempty"`
    AutoMerge   bool              `json:"auto_merge"`
    Priority    int               `json:"priority"`
    CustomFields map[string]any   `json:"custom_fields,omitempty"`
    DependsOn   []string          `json:"depends_on,omitempty"`
}

type StartAgentRequest struct {
    TaskID      string            `json:"task_id"`
    Environment string            `json:"environment,omitempty"` // e.g. "preview", "staging"
    Files       map[string]string `json:"files,omitempty"`       // Injected guest files (e.g. kubeconfig)
    EnvVars     map[string]string `json:"env_vars,omitempty"`     // Injected guest environment variables
    Timeout     time.Duration     `json:"timeout,omitempty"`
}

type ProvisionRequest struct {
    TaskID      string            `json:"task_id"`
    Repo        string            `json:"repo"`
    Environment string            `json:"environment"` // Matches .grain/environments/<env>.tf
    Branch      string            `json:"branch"`
    CommitSHA   string            `json:"commit_sha"`
}

type ProvisionResult struct {
    Files       map[string]string `json:"files"`        // Scoped configs (e.g. /home/debian/.kube/config)
    EnvVars     map[string]string `json:"env_vars"`      // Scoped connection vars (e.g. DATABASE_URL)
    URLs        map[string]string `json:"urls"`          // UI preview URLs
    ContextData map[string]string `json:"context_data"`  // Opaque state returned for teardown
}

type TeardownRequest struct {
    TaskID      string            `json:"task_id"`
    Repo        string            `json:"repo"`
    Environment string            `json:"environment"`
    ContextData map[string]string `json:"context_data"`  // Preserved from ProvisionResult
}

type StartCIRequest struct {
    Repo      string   `json:"repo"`
    CommitSHA string   `json:"commit_sha"`
    Branch    string   `json:"branch"`
    Jobs      []string `json:"jobs,omitempty"`
}

type MergeRequest struct {
    Repo        string `json:"repo"`
    PullRequest int    `json:"pull_request"`
    Method      string `json:"method"` // "fast_forward", "squash", "rebase"
}
```

### 3.2 Event Wakeup Engine

Repo Managers operate reactively without burning polling cycles. The Grain daemon routes events directly into the Repo Manager's mailbox:

```go
type RepoEvent struct {
    Type      EventType      `json:"type"`
    Repo      string         `json:"repo"`
    Timestamp time.Time      `json:"timestamp"`
    Payload   map[string]any `json:"payload"`
}

type EventType string

const (
    EventTaskCompleted   EventType = "task.completed"
    EventTaskFailed      EventType = "task.failed"
    EventCICompleted     EventType = "ci.completed"
    EventPRSubmitted     EventType = "pr.submitted"
    EventPRUpdated       EventType = "pr.updated"
    EventWebhookReceived EventType = "webhook.received"
    EventScheduleTrigger EventType = "schedule.trigger"
)
```

---

## 4. Two-Sandbox Security Model for Dynamic Provisioning

A fundamental challenge in AI-assisted engineering is **dynamic infrastructure provisioning vs. untrusted code execution**.
- To test pull requests or reproduce issues, agents need realistic preview environments (ephemeral Kubernetes namespaces, temporary Cloud SQL databases, test IAM service accounts).
- However, provisioning such environments requires **high-privilege cloud credentials** (GCP SA key, AWS IAM admin role, Terraform remote state backend credentials).
- Allowing an LLM worker agent or untrusted PR code to hold high-privilege cloud credentials creates an unacceptable security exposure: prompt injection, supply chain tampering, or accidental resource destruction could compromise cloud infrastructure.

Grain resolves this through the **Two-Sandbox Security Model**.

### 4.1 Architecture & Flow Diagram

```
┌────────────────────────────────────────────────────────────────────────┐
│                        Repo Manager Agent                              │
│  Evaluates task requirements: "Provision preview env for Task #109"   │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    ▼ Calls Substrate: ProvisionEnvironment()
┌────────────────────────────────────────────────────────────────────────┐
│                    Sandbox 1: Provisioner Sandbox                      │
│  - HIGH PRIVILEGE: Holds GCP SA Key / AWS Admin / Terraform State Auth │
│  - HERMETIC & DETERMINISTIC: Runs ONLY vetted repo setup modules       │
│    (e.g., `terraform apply` in `.grain/environments/preview/`)         │
│  - ZERO LLM INVOLVEMENT: Never runs LLM prompts or worker code         │
│  - Executes in ~1.5 seconds with pre-cached plugins                    │
│  - EMITS SCOPED ARTIFACTS ONLY: Scoped K8s token, ephemeral DB creds  │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
               Scoped Artifacts Only│ (Injected Files & Env Vars)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                    Sandbox 2: Task Worker Sandbox                      │
│  - LOW PRIVILEGE: ZERO access to master cloud credentials              │
│  - Isolated Kontur MicroVM for agent coding, builds, and test runs     │
│  - Receives ONLY ephemeral credentials (e.g. namespace-scoped config)  │
│  - Agent writes code, runs test suites, and iterates on PR safely      │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                       Task Finished│ (or crashed / timed out / cancelled)
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                    Sandbox 3: Teardown Sandbox                         │
│  - HIGH PRIVILEGE: Holds cloud credentials for resource reclamation    │
│  - Runs `terraform destroy` with opaque ContextData from Provisioner   │
│  - GUARANTEED EXECUTION: Daemon reconciler guarantees teardown even if  │
│    the Task Worker sandbox crashed, panicked, or timed out             │
│  - Complete zero-leak resource destruction & token revocation          │
└────────────────────────────────────────────────────────────────────────┘
```

### 4.2 Security Invariants & Isolation Boundaries

1. **Strict Credential Segregation**:
   Master cloud administrative credentials (e.g. `GOOGLE_APPLICATION_CREDENTIALS`, AWS IAM tokens, root cluster certificates) reside exclusively inside Provisioner and Teardown sandboxes. They are never mounted, written, or transmitted into the Task Worker sandbox.
2. **Deterministic, Vetted Code Execution in Provisioner**:
   The Provisioner sandbox executes static, version-controlled Infrastructure-as-Code definitions (e.g., `.grain/environments/preview/main.tf`). It never parses or executes LLM-generated bash commands or untrusted PR code.
3. **Least Privilege Injected Artifacts**:
   The Task Worker sandbox receives only tightly bounded, ephemeral resources:
   - A `kubeconfig` bound via Kubernetes RBAC strictly to namespace `preview-task-109`.
   - An ephemeral PostgreSQL user with grants limited to `db_task_109`.
   - A temporary preview hostname (`task-109.preview.grain.zone`).
4. **Guaranteed Zero-Leak Teardown**:
   Resource reclamation does not depend on the worker agent's exit hooks. The Grain daemon tracks allocated environments in its persistent state store (`pkg/model`). When a task concludes (success, failure, timeout, or cancellation), the daemon automatically spins up the Teardown sandbox with the preserved `ContextData` to execute reclamation.

---

## 5. Autonomous Scheduling & Event Ingestion

### 5.1 Deprecation of Centralized Cron Tables

Prior orchestrator designs relied on centralized configuration files like `tables/schedules.json` or daemon-level cron loops (`docs/schedules.md`). This created several friction points:
- Adding or modifying a recurring chore required modifying deployment-wide state repositories.
- Cron parsers lacked semantic understanding of repository activity (e.g., running expensive soak tests even when no code changed, or failing to run triage when backlogs grew).
- Scheduling logic was completely disconnected from repository guidelines and PR activity.

In this architecture:
- **Centralized `tables/schedules.json` and static daemon cron parsers are deprecated**.
- **Repo Managers maintain their own autonomous timers and schedule policies** defined in `.grain/manager.md`.
- Grain daemon provides simple timer ticks to the Repo Manager, which uses LLM reasoning to evaluate whether to trigger a task, skip execution, or adjust batch parameters.

### 5.2 Event Ingestion & Webhook Routing

External webhooks (GitHub events, Slack messages, Datadog alerts, security notifications) route through `graind`'s webhook ingestion endpoint and are immediately dispatched to the appropriate Repo Manager:

```
┌─────────────────┐       ┌─────────────────┐       ┌─────────────────┐
│  GitHub Webhook │       │  Slack Mention  │       │  Alertmanager   │
└────────┬────────┘       └────────┬────────┘       └────────┬────────┘
         │                         │                         │
         └────────────────┐        │        ┌────────────────┘
                          ▼        ▼        ▼
             ┌───────────────────────────────────────────┐
             │       Grain Daemon Webhook Gateway        │
             │  - Validates HMAC signatures              │
             │  - Resolves target repository             │
             └─────────────────────┬─────────────────────┘
                                   │
                                   ▼ Unparks Session
             ┌───────────────────────────────────────────┐
             │            Repo Manager Agent             │
             │  1. Ingests event payload                 │
             │  2. Evaluates repository state            │
             │  3. Dispatches task via CreateTask()      │
             └───────────────────────────────────────────┘
```

#### Example Workflow Scenarios
- **Nightly Qualification**: At the scheduled timer wakeup, the Repo Manager checks if new commits landed on `main`. If so, it triggers an ephemeral staging qualification run. If `main` is unchanged, it skips execution to conserve compute.
- **Flaky Test Triage**: On receiving consecutive CI failure events on `main`, the Repo Manager autonomously creates an investigative task to bisect commits and isolate the flaky test.
- **Interactive Operator Commands**: An engineer tags `@grain triage` in a PR comment; the webhook wakes the Repo Manager, which evaluates the PR diff, tests the preview build, and replies with diagnostic feedback.

---

## 6. Terraform / OpenTofu as the Standard Driver

While Provisioner sandboxes can execute any vetted script, **Terraform / OpenTofu** is the standard infrastructure driver for Grain dynamic environments.

### 6.1 Sub-Second Lifecycle & Performance Optimizations

Dynamic environments must be fast enough to support interactive agent development without multi-minute provisioning delays:

1. **Pre-cached Provider Plugins (`TF_PLUGIN_CACHE_DIR`)**:
   Provisioner sandbox images bundle all common provider binaries (`terraform-provider-kubernetes`, `terraform-provider-google`, `terraform-provider-postgresql`, `terraform-provider-random`) in `/opt/tf-plugins`. `tofu init` runs fully offline in milliseconds.
2. **Pre-warmed Module Schemas**:
   On daemon startup, environment definitions in `.grain/environments/` are pre-validated and initialized.
3. **Workspace-Isolated State**:
   Each task creates an isolated state workspace (`task-${TASK_ID}`) backed by local fast NVMe scratch storage or cloud object storage.

### 6.2 Reference Environment Definition (`.grain/environments/preview/main.tf`)

```hcl
variable "task_id" {
  type        = string
  description = "Grain task ID requiring the preview environment"
}

variable "branch" {
  type        = string
  description = "Target Git branch under test"
}

# 1. Ephemeral Kubernetes Namespace
resource "kubernetes_namespace" "preview" {
  metadata {
    name = "preview-task-${var.task_id}"
    labels = {
      "grain.zone/managed-by" = "grain-provisioner"
      "grain.zone/task-id"    = var.task_id
      "grain.zone/branch"     = var.branch
    }
  }
}

# 2. Ephemeral Database Role & Database
resource "random_password" "db_password" {
  length  = 24
  special = false
}

resource "postgresql_role" "task_user" {
  name     = "user_task_${var.task_id}"
  password = random_password.db_password.result
  login    = true
}

resource "postgresql_database" "task_db" {
  name  = "db_task_${var.task_id}"
  owner = postgresql_role.task_user.name
}

# 3. Scoped Service Account & RoleBinding
resource "kubernetes_service_account" "agent_sa" {
  metadata {
    name      = "sandbox-worker"
    namespace = kubernetes_namespace.preview.metadata[0].name
  }
}

resource "kubernetes_role_binding" "agent_binding" {
  metadata {
    name      = "sandbox-worker-binding"
    namespace = kubernetes_namespace.preview.metadata[0].name
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "edit"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.agent_sa.metadata[0].name
    namespace = kubernetes_namespace.preview.metadata[0].name
  }
}

# Structured Outputs Consumed by Substrate & Injected into Task Worker Sandbox
output "files" {
  sensitive = true
  value = {
    "/home/debian/.kube/config" = templatefile("${path.module}/templates/kubeconfig.tpl", {
      endpoint  = "https://k8s-preview.internal:6443"
      namespace = kubernetes_namespace.preview.metadata[0].name
      token     = kubernetes_secret.agent_token.data["token"]
    })
  }
}

output "env_vars" {
  value = {
    DATABASE_URL       = "postgres://user_task_${var.task_id}:${random_password.db_password.result}@postgres.internal:5432/db_task_${var.task_id}?sslmode=disable"
    K8S_NAMESPACE      = kubernetes_namespace.preview.metadata[0].name
    PREVIEW_HOST       = "task-${var.task_id}.preview.grain.zone"
  }
}

output "urls" {
  value = {
    "Preview Application" = "https://task-${var.task_id}.preview.grain.zone"
    "Live Logs Dashboard" = "https://logs.internal/dashboards/preview?task=${var.task_id}"
  }
}

output "context_data" {
  value = {
    "workspace_name" = "task-${var.task_id}"
    "namespace"      = kubernetes_namespace.preview.metadata[0].name
    "db_user"        = postgresql_role.task_user.name
    "db_name"        = postgresql_database.task_db.name
  }
}
```

---

## 7. Repo Manager MCP Toolset

Repo Managers interact with the Grain kernel via standard Model Context Protocol (MCP) tools:

```json
[
  {
    "name": "mcp_grain_create_task",
    "description": "Create and queue a new task worker job for this repository",
    "parameters": {
      "type": "object",
      "properties": {
        "title": { "type": "string" },
        "prompt": { "type": "string" },
        "target_ref": { "type": "string" },
        "auto_merge": { "type": "boolean" },
        "priority": { "type": "integer" },
        "depends_on": { "type": "array", "items": { "type": "string" } }
      },
      "required": ["title", "prompt"]
    }
  },
  {
    "name": "mcp_grain_provision_environment",
    "description": "Trigger the high-privilege Provisioner sandbox to prepare an ephemeral environment for a task",
    "parameters": {
      "type": "object",
      "properties": {
        "task_id": { "type": "string" },
        "environment": { "type": "string" }
      },
      "required": ["task_id", "environment"]
    }
  },
  {
    "name": "mcp_grain_teardown_environment",
    "description": "Trigger the high-privilege Teardown sandbox to destroy an ephemeral environment",
    "parameters": {
      "type": "object",
      "properties": {
        "task_id": { "type": "string" },
        "environment": { "type": "string" }
      },
      "required": ["task_id", "environment"]
    }
  },
  {
    "name": "mcp_grain_start_ci",
    "description": "Execute Grain CI workflow checks for a commit or branch",
    "parameters": {
      "type": "object",
      "properties": {
        "commit_sha": { "type": "string" },
        "branch": { "type": "string" },
        "jobs": { "type": "array", "items": { "type": "string" } }
      },
      "required": ["commit_sha", "branch"]
    }
  },
  {
    "name": "mcp_grain_merge_pr",
    "description": "Fast-forward or merge an approved pull request to the target branch",
    "parameters": {
      "type": "object",
      "properties": {
        "pull_request": { "type": "integer" },
        "method": { "type": "string", "enum": ["fast_forward", "squash", "rebase"] }
      },
      "required": ["pull_request"]
    }
  },
  {
    "name": "mcp_grain_list_tasks",
    "description": "Query tasks and active runs for this repository",
    "parameters": {
      "type": "object",
      "properties": {
        "status": { "type": "string" },
        "limit": { "type": "integer" }
      }
    }
  }
]
```

---

## 8. Unification & Comparison with Earlier Subsystems

This specification synthesizes and completes the evolution across earlier Grain architecture documents:

| Dimension | 1. In-Daemon Staging (`docs/staging-environments.md`) | 2. Granule MicroVM Controllers (`docs/repo-controllers-and-environments.md`) | 3. Long-Lived Repo Managers (`docs/repo-managers-and-environments.md`) |
|---|---|---|---|
| **Core Primitive** | Static reconciler in daemon | Granule microVM executing RPC endpoints | **Autonomous Long-Lived Repo Manager Agent** |
| **Reasoning Model** | None (rigid Go code) | None (static RPC hooks) | **Full LLM Reasoning** with domain `.grain/manager.md` |
| **Security Architecture** | Central daemon held all cloud credentials | Controller VM held cloud credentials | **Two-Sandbox Isolation**: Provisioner (High Priv) vs. Worker (Zero Priv) |
| **Operator Interface** | Separate settings tabs | None (automated background process) | **Unified standing chat & governance console** (generalizing Director) |
| **Scheduling Engine** | Central `tables/schedules.json` & cron loop | Polling / external triggers | **Autonomous Repo Manager timers & webhook triage** |
| **Substrate API** | Monolithic daemon internals | Local UDS gRPC (`pkg/repocontroller`) | **Lean Substrate API** (`CreateTask`, `StartAgent`, `StartCI`, `MergePR`) |
| **Resource Reclamation** | Best-effort daemon scripts | Controller RPC cleanup | **Guaranteed Dead-Man Teardown via Daemon Context Store** |

### Cross-Subsystem Synergy

1. **Unification with `docs/director.md`**:
   The Director was introduced as an interactive console session for deployment management. This design elevates that insight into the standard model for all repositories: the Director is simply the Repo Manager for `grain/config`, while every application repository runs an equivalent Repo Manager specialized by `.grain/manager.md`.
2. **Unification with `docs/staging-environments.md`**:
   Instead of hardcoded daemon staging reconcilers, preview and staging environments are provisioned on demand through the Two-Sandbox pattern and coordinated by the Repo Manager.
3. **Unification with `docs/repo-controllers-and-environments.md`**:
   The static Granule microVM RPC contract is replaced by the substrate API, empowering an autonomous agent to make intelligent decisions regarding batch qualification, error diagnosis, and environment orchestration.

---

## 9. Summary of Key Invariants

1. **Unified Standing Primitive**: Every repository has one standing Repo Manager session that is filed approved, never marked done, and reactivated on event wakeups without polling.
2. **Zero Master Credentials in Worker Sandboxes**: Task workers execute with strictly scoped, temporary artifacts. Cloud admin credentials are isolated to hermetic Provisioner and Teardown sandboxes.
3. **Vetted Provisioner Execution**: Provisioner sandboxes execute only static, reviewed Terraform/OpenTofu definitions in the repository, never untrusted PR code or LLM-generated commands.
4. **Guaranteed Zero-Leak Teardown**: The Grain daemon guarantees teardown execution with persisted context data regardless of worker agent outcomes.
5. **Decentralized Scheduling**: Autonomous Repo Managers replace rigid cron tables with intelligent, context-aware scheduling and event triage.
