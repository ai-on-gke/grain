# Release Testing Plan: First External User Onboarding & Platform Qualification

**Target Date**: 2026-09-25  
**Release Target**: First external consumer readiness (users and operators beyond the primary Grain developer)  
**Target Repository**: `bwsalmon/grain`  
**Scope Note**: Remote executor pooling and the Cloud Run Rendezvous Relay are deferred and out of scope for tomorrow's milestone; all tests focus on the single-node / clustered local daemon, Granule VM sandboxing, web UI, multi-agent execution, multi-team governance, declarative automation, and autonomous merge pipelines.

---

## 1. Executive Summary & Release Objectives

Tomorrow marks the first milestone where engineers and operators other than Grain's original developer will interact with, configure, and consume Grain. The primary objective of this testing plan is to validate that Grain is robust, intuitive, fault-tolerant, and secure across its complete user-facing and operational surface.

This plan details the testing procedures across seven core domains:
1. **Out-of-the-Box Experience (OOBE) & Host Bootstrap**: First-time installation, credentials initialization, daemon startup, and client connection.
2. **Comprehensive UI Walkthroughs**: Complete verification of task authoring, live log streaming, interactive questions, boards, repository administration, multi-team management, settings navigation, and responsive viewports.
3. **End-to-End Feature Lifecycle**: Full agentic lifecycle from backlog queueing through Granule VM sandbox provisioning, code modification, PR creation, CI check evaluation, proactive conflict detection, and autonomous merge queue landing.
4. **Multi-Agent Framework Compatibility**: Validating agent dispatch across Antigravity (`agy`), Claude Code, and OpenAI Codex backends.
5. **Declarative In-Repo Automation**: Syncing and executing repository standing declarations (schedules, templates, suites, workflows).
6. **Scalability, Concurrency & Resource Lifecycle**: High-throughput task dispatch, multi-team fair-share scheduling, VM lifecycle cleanup, and database/WebSocket event stress.
7. **Security, Governance & Boundary Isolation**: Verification of credential redaction, sandbox VM confinement, and team RBAC permission barriers.

---

## 2. Out-of-the-Box Experience (OOBE) & Host Bootstrap

First-time external users must be able to bootstrap Grain onto a clean Linux environment without hidden assumptions, manual file hacking, or unhandled errors.

### Existing coverage
- Shell script unit and integration tests in `tests/installer/installer_test.go` and `tests/deploy/deploy_test.go`.
- Hostmode initialization tests in `tests/installer/hostmode/`.
- Container startup checks in `tests/container/image_test.go`.

### Gaps and unproven behaviors
- Clean-machine bootstrap running `./scripts/setup.sh` on a fresh Debian 12 (Bookworm) instance without pre-existing environment variables.
- Interactive and non-interactive setup modes verifying helpful diagnostic output when required credentials (GitHub token/App, Gemini/Anthropic/OpenAI keys) are missing.
- Client connection script (`./scripts/client.sh <grain-host>`) behavior over SSH tunnels and direct network connections with browser auto-launch.
- Proper handling of initial empty states across all UI views when zero tasks, teams, or repositories have been registered.

### Walkthrough & test procedure
1. **Clean Host Provisioning**:
   - Provision a fresh Debian 12 VM with KVM virtualization enabled (`/dev/kvm`).
   - Clone `bwsalmon/grain` repository to `/home/debian/work`.
2. **Interactive Setup Script Execution**:
   - Run `sudo ./scripts/setup.sh --help` to verify CLI documentation completeness.
   - Execute setup without credentials; verify that the installer fails cleanly with clear, human-readable guidance on required tokens.
   - Execute setup with valid credentials:
     ```bash
     sudo \
       GRAIN_GITHUB_TOKEN="<test-github-token>" \
       GRAIN_GEMINI_API_KEY="<test-gemini-key>" \
       GRAIN_TARGET_REPO="bwsalmon/grain" \
       ./scripts/setup.sh
     ```
   - Verify that systemd services (`grain.service`) start cleanly and reach active/running state.
3. **Client Connection & Empty-State Walkthrough**:
   - From an operator workstation, run `./scripts/client.sh <grain-host>`.
   - Verify that SSH port forwarding is established and the browser opens to `http://localhost:8080` (or configured port).
   - Verify that the Inbox displays an intuitive empty state welcoming the user with quick-start actions ("Create Task", "Browse Templates", "Explore Repositories").

Filed: **Walkthrough: Out-of-the-box installation, bootstrap, and client onboarding (docs/test-plans/2026-09-25-release-testing.md §2)**

---

## 3. Comprehensive UI & User Experience Walkthroughs

External users interact primarily through the web interface. Every view, control, modal, and responsive layout must operate smoothly and communicate system state unambiguously.

### 3.1 Task Intake, Authoring & Detail Supervision
- **New Task Modal**: Test opening `/tasks/new`, entering titles and markdown task descriptions, selecting target repositories, base branches, models (`claude-sonnet-5`, `gemini-3.1-pro`, `gpt-5-codex`), agent frameworks (`antigravity`, `claude`, `codex`), attaching capabilities (`grain-ci`, `gcp-key`), specifying custom VM parameters (`sandbox_cpus`, `sandbox_memory_mb`), and queueing options (`at_front`).
- **Live Task Detail View**:
  - Live WebSocket log streaming: verify sub-second update latency, ANSI color rendering, auto-scrolling, manual scroll lock toggle, and byte offset resumption.
  - Diff Viewer: verify unified and side-by-side git diff rendering for files modified by the agent during the run.
  - Status Indicators: verify distinct visual states for `Queued`, `Provisioning Sandbox`, `Running`, `Waiting on Question`, `Running CI`, `In Merge Queue`, `Completed`, `Failed`, and `Cancelled`.
- **Human-in-the-Loop Interaction**:
  - Dispatch a task that invokes `ask_question`.
  - Assert that the task status chips update to "Waiting on User", an audible or visual badge appears in the UI, and the interactive Question Modal opens.
  - Submit write-in responses as well as option-based responses; assert the agent immediately resumes execution.
- **Task Cancellation & Retries**:
  - Cancel an active running task via the UI; verify the sandbox VM is immediately terminated, resources are reclaimed, and status transitions to `Cancelled`.
  - Retry a failed or cancelled task; assert new attempt is created while preserving historical attempt logs.

### 3.2 Saved Boards, Visualizations & Metrics
- **Kanban Board View**: Verify drag-and-drop column transitions, swimlane grouping by team or repository, column collapsing, and real-time task card updates.
- **List View**: Verify column sorting, filtering (by team, status, author, repository, date range), free-text search, and bulk actions (bulk cancel, bulk reassign).
- **Timeline / Gantt View**: Verify temporal rendering of task run durations, dependency lines between parent and child proposed tasks, and schedule milestones.
- **Metrics Dashboard**: Verify real-time cards for active workers, merge queue depth, CI pass rates, average task runtime, and model token usage.

### 3.3 Multi-Team Management & RBAC Roles
- **Self-Service Team Creation**: Authenticated user creates a new team ("Frontend Engineering"); verify creator is assigned the `admin` role.
- **Role Assignment**: Add User B as `member` and User C as `viewer`.
- **Permission Verification**:
  - `admin`: Can modify team settings, default models, team secrets, and invite/remove members.
  - `member`: Can create, edit, run, and cancel tasks; cannot modify team settings or invite users.
  - `viewer`: Can inspect tasks, logs, boards, and metrics; cannot submit, mutate, cancel, or approve tasks (mutating buttons hidden or disabled).
- **Team-Scoped Secrets & Default Configs**: Set team default model to `claude-sonnet-5` and configure team secret `FRONTEND_DEPLOY_TOKEN`. Assert that tasks dispatched under this team inherit these defaults, and that other teams cannot access or view this secret.
- **Team Archival Lifecycle**: Archive a team; assert it is hidden from default selector dropdowns while preserving history; unarchive and verify full restoration.

### 3.4 Repository Administration & Settings Navigation Overhaul
- **Repository Hub (`RepoPage`)**:
  - CI Mode Selection: Toggle CI mode between `grain` (internal Grain CI), `github` (GitHub Checks), and `none`. Verify immediate backend persistence and merge queue rule adjustment.
  - Admin Role Management: Add repository administrators via `GET/PUT /api/repos/:owner/:repo/admins`; verify non-admin users cannot alter repo configuration.
  - Capability Security: Verify repository-level capabilities and permissions gating.
- **Settings Overlay Reorganization**:
  - Navigate between User Profile, Team Settings, and System Administrator panels.
  - Administrator Access & Accounts: Inspect user accounts table, sync statuses (`Pending` -> `Synced`), and role badges.
  - Left Sidebar Customization: Pin and unpin navigation items (Boards, Director, Repos, Schedules); reload browser to confirm user preference persistence.

### 3.5 Responsive Layout & Viewport Testing
- **Desktop (1920x1080 & 1440x900)**: Full multi-pane layout, side navigation, unconstrained diff scrolling.
- **Tablet (768x1024 / iPad)**: Collapsible sidebar navigation, adaptive board grid layouts, touch scroll support.
- **Mobile / Phone (375x667 & 414x896)**: Bottom drawer navigation, stacked task detail views, responsive log viewers with word-wrap toggle.

Filed: **Walkthrough: Comprehensive UI, task supervision, boards, multi-team RBAC, and responsive layouts (docs/test-plans/2026-09-25-release-testing.md §3)**

---

## 4. End-to-End Operational Lifecycle Testing

End-to-end tests validate that the entire operational loop operates autonomously, reliably, and without deadlocks.

```text
Task Intake → Backlog Prioritization → Dispatcher → Granule VM Sandbox →
Agent Execution → Git Commit & Push → Pre-PR Rebase Check → PR Creation →
Grain CI Validation → Proactive Conflict Detection → Merge Queue → Main Branch
```

### Existing coverage
- End-to-end harness tests in `tests/e2e/e2e_test.go` and `tests/e2e/harness_test.go`.
- CLI verb integration tests in `tests/e2e/cli_verbs_test.go`.
- Proposal and approval flow tests in `tests/e2e/propose_task_approval_test.go`.

### Gaps and unproven behaviors
- Complete uninterrupted lifecycle from task creation to automatic PR merge on a live repository using real agent binaries (`agy`, `claude`, `codex`).
- Agent creating multi-file code changes, formatting via Conventional Commits, and pushing branch `grain/task-<id>`.
- Pre-PR rebase verification ensuring that when the base branch advances during agent execution, the branch is rebased cleanly before the pull request is opened.
- Pre-merge review template attachment (`review_template_id`) validating that draft reviews are generated and reviewed before merging.

### Walkthrough & test procedure
1. **Task Dispatch**:
   - Enqueue an automated feature task: "Add health check endpoint `/api/healthz` with JSON response `{\"status\": \"ok\", \"time\": <timestamp>}`".
   - Target branch: `main`, agent: `agy`, capability: `grain-ci`.
2. **Sandbox Provisioning & Execution**:
   - Verify Granule initializes a clean KVM VM guest in `< 3s`.
   - Verify `/home/debian/work` contains the repository clone on branch `grain/task-<id>`.
   - Verify `.gemini-api-key` is mounted with `0600` permissions.
   - Observe agent creating code in `pkg/ui/health.go` and test in `pkg/ui/health_test.go`.
3. **Commit, Push & Pre-PR Rebase**:
   - Agent executes `git commit -m "feat(api): add /api/healthz endpoint"` and pushes branch.
   - Orchestrator verifies mergeability against base branch; if base advanced, performs automated rebase.
   - Orchestrator opens Pull Request with title and body synthesized from commit messages.
4. **CI Execution & Automatic Merge**:
   - Grain CI executes `.grain/workflows/tests.yml` against the commit.
   - Live CI logs stream to task view.
   - Upon green CI checks, merge queue evaluates auto-merge disposition (`auto_merge: true`) and merges PR into `main`.
   - Sandbox VM is cleanly destroyed; task status reaches `Completed`.

Filed: **Walkthrough: Complete autonomous backlog-to-merge lifecycle (docs/test-plans/2026-09-25-release-testing.md §4)**

---

## 5. Multi-Agent Framework Compatibility Matrix

Grain supports three major coding agent backends. First-time users will rely on different backends based on their organizational API keys.

| Agent Backend | Binary / Invocation | Tool Surface | Isolation / Capabilities |
| :--- | :--- | :--- | :--- |
| **Antigravity** | `agy` (v1.2.2) | Native MCP + CLI Tools | Strict sandbox, Gemini API key injection |
| **Claude Code** | `claude` | Anthropic Tool Suite | Anthropic API key, headless daemon mode |
| **OpenAI Codex**| `codex` / Custom | OpenAI Tools & MCP | OpenAI API key, isolated sandbox loop |

### Test Procedure for Each Framework:
1. **Antigravity (`agy`)**:
   - Dispatch task targeting bug fix in Go package.
   - Verify agent executes `mcp_grain-sandbox_read_file`, `mcp_grain-sandbox_edit_file`, and `mcp_grain-sandbox_run_command`.
   - Assert retry backoff under simulated model 503 capacity refusal works smoothly.
2. **Claude Code**:
   - Dispatch task targeting TypeScript / React component update in `ui/`.
   - Verify CLI token handshake, headless tool execution, and clean process termination.
3. **OpenAI Codex**:
   - Dispatch documentation and unit test authoring task.
   - Verify JSON tool call parsing, execution in sandbox, and commit delivery.

Filed: **Walkthrough: Multi-agent framework verification (docs/test-plans/2026-09-25-release-testing.md §5)**

---

## 6. Merge Queue, Proactive Conflict Detection & Release Watchdog

The merge queue is Grain's critical safety valve. It guarantees that code landing on `main` is always tested, verified, and free of merge conflicts.

### Existing coverage
- `pkg/orchestrator/mergequeue_behind_test.go`
- `tests/e2e/mergequeue_conflict_test.go`
- `tests/e2e/merge_stress_test.go`

### Test Procedure:
1. **Multi-PR Queueing**:
   - Submit three tasks that generate three concurrent PRs (PR 1, PR 2, PR 3) touching adjacent code in `pkg/model/`.
2. **Proactive Conflict Detection on Base Advance**:
   - Merge PR 1 into `main`.
   - Assert that orchestrator immediately triggers parallel mergeability checks for PR 2 and PR 3 on the base-advance tick.
3. **Automated Conflict Repair Dispatch**:
   - If PR 3 conflicts with PR 1's landed change, assert that orchestrator:
     a) Flags PR 3 as `Conflicted`.
     b) Automatically dispatches a conflict resolution repair task to rebase and fix merge markers.
     c) Keeps PR 2 moving forward in the queue without blocking behind PR 3.
4. **Release Watchdog**:
   - Trigger the release watchdog (`.grain/templates/release-watchdog.yml`).
   - Assert detection of stalled candidate branches and automatic creation of triage review tasks.

Filed: **Walkthrough: Merge queue proactive conflict detection, rebase, and release watchdog (docs/test-plans/2026-09-25-release-testing.md §6)**

---

## 7. Scalability, Concurrency & Resource Lifecycle Testing

For multi-user environments, Grain must handle concurrent workloads without degrading, running out of file descriptors, leaking VMs, or deadlocking database transactions.

### 7.1 Concurrency & Fair-Share Scheduling Stress
- **Configuration**: Set `max_workers = 6`, `max_mergers = 2`, `max_ci_jobs = 4`.
- **Multi-Team Workload**:
  - Team A enqueues 10 tasks in rapid burst.
  - Team B enqueues 4 tasks immediately after.
- **Assertion**:
  - Verify dispatcher allocates workers fairly across Team A and Team B based on per-team concurrency limits, rather than allowing Team A to starve Team B.
  - Backlog prioritization (`at_front: true`) correctly jumps tasks to the front of their respective team queue.

### 7.2 Granule VM Lifecycle & Disk/Memory Recycling
- **Rapid VM Churn**:
  - Run 20 sequential short tasks.
  - Monitor `/var/lib/grain/sandboxes`, `/dev/kvm`, and host network bridge (`br0` / tap interfaces).
- **Assertion**:
  - Every completed/failed VM is cleanly shutdown via `cloud-hypervisor` API.
  - Ephemeral disk overlays and loopback mounts are deleted (`rm -rf` / unmounted cleanly).
  - No leaked tap network devices or dangling file handles remain.

### 7.3 High-Throughput WebSocket & REST API Stress
- Simulate 10 simultaneous browser clients connected via WebSocket viewing live streaming tasks while 6 concurrent tasks stream stdout logs.
- Assert sub-50ms UI response latency and zero dropped WebSocket frames or server panics.

Filed: **Walkthrough: Scalability, multi-team fair sharing, VM recycling, and load stress (docs/test-plans/2026-09-25-release-testing.md §7)**

---

## 8. Security, Credential Isolation & RBAC Verification

Because external engineers and teams will share the Grain deployment, strict isolation between sandboxes, credentials, and team tenants is mandatory.

### Security Assertions & Test Matrix:

| Security Boundary | Test Scenario | Expected Outcome |
| :--- | :--- | :--- |
| **Sandbox Confinement** | Sandbox script attempts `curl http://169.254.169.254` (cloud metadata) or access `/dev/sda` host block device. | Connection refused / permission denied; guest kernel cannot reach host memory or metadata. |
| **Credential Redaction** | Agent logs or outputs environment variables containing tokens; user inspects live UI logs and REST response. | Deployment master tokens and provider secrets are masked (`[REDACTED]`) and never sent to browser. |
| **Cross-Team Secrets** | Team B task attempts to read secret declared exclusively for Team A. | Credential mounting engine refuses secret lookup; task logs clear authorization failure without leaking secret value. |
| **Viewer Role Mutation** | User with `viewer` role executes `POST /api/tasks` or `POST /api/repos/:owner/:repo/ci-mode`. | `403 Forbidden` returned; UI hides submit buttons. |
| **CI Capability Gating** | Untrusted `pull_request` task requests `repo-write` capability. | `SupportsCIEvent` refuses capability injection; task runs with restricted read-only permissions. |

Filed: **Walkthrough: Security isolation, credential protection, and RBAC enforcement (docs/test-plans/2026-09-25-release-testing.md §8)**

---

## 9. Pre-Flight Verification Checklist & Tester Runbook

This section serves as the immediate actionable runbook for tomorrow's release testing session.

### Phase 1: Environment Readiness (09:00 - 09:30)
- [ ] Host machine booted with clean kernel, `/dev/kvm` accessible, Docker & Granule operational.
- [ ] Grain daemon running (`systemctl status grain`).
- [ ] Target repositories registered (`bwsalmon/grain`).
- [ ] Declaration syncing active (`RepoConfig.Declarations = true`).
- [ ] Provider credentials verified (Gemini, Claude, OpenAI).
- [ ] Client connection script tested (`./scripts/client.sh`).

### Phase 2: First-Time User Journey (09:30 - 11:00)
- [ ] Tester A (Admin): Log in, explore Inbox empty state, customize sidebar navigation.
- [ ] Tester A: Create Team "Core-Eng", configure team defaults, invite Tester B (Member) and Tester C (Viewer).
- [ ] Tester B (Member): Author first task via New Task form; choose template `nightly-features-test-plan`.
- [ ] Tester B: Watch live execution in task detail view; verify log streaming and diff preview.
- [ ] Tester B: Respond to interactive question modal when prompted; confirm resumption.
- [ ] Tester C (Viewer): Verify read-only access across all views and inability to mutate tasks.

### Phase 3: Automation, Merge Queue & Concurrency (11:00 - 12:30)
- [ ] Run declarative suite `.grain/suites/release-ui-walkthrough.yml` in UI.
- [ ] Enqueue 5 concurrent tasks across multiple teams; monitor worker pool saturation and fair dispatching.
- [ ] Enqueue 2 overlapping PRs; verify proactive mergeability check and automated merge on green CI.
- [ ] Check `/var/lib/grain/sandboxes` to verify zero residual disk or process leaks.

### Phase 4: Sign-Off & Bug Triage (12:30 - 13:00)
- [ ] Review Metrics dashboard for error rates and throughput.
- [ ] Triage any reported issues:
  - **P0 (Blocker)**: Sandbox escaping, credential leakage, daemon crash, merge queue deadlock -> Immediate fix before external rollout.
  - **P1 (High)**: UI rendering glitch, missing empty state cue, confusing error message -> File follow-up task with reproduction steps.
- [ ] Release coordinator signs off on release candidate promotion (`candidate` -> `latest`).
