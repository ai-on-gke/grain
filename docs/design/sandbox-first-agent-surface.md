# Design: Sandbox-First Agent Surface

**Status:** Proposed (Task 845)  
**Builds on:** `docs/design/grain-namespace.md` (Tasks 843 / 846), `docs/design/unified-grants.md`, `docs/design/cli-run-location.md`, `docs/agent-api.md`, `docs/capabilities-spec.md`

---

## Context and Problem

Grain currently exposes almost all of its agent-facing functionality as MCP tools registered by `grain mcpserver` (`pkg/daemon/mcpserver.go`). An ordinary coding task with `grain-ci` receives **35 MCP tools** (`56,635 bytes` of JSON schema, `~14,158 tokens`); a Pipeline Steward or Director session holding `self-debug`, `grain-log`, `deployment-health`, `memory`, `api:task-admin`, and `api:dispatch-control` receives **118 MCP tools** (`~138 KB`, `~34,500 tokens`); a run granted the full `api:*` vocabulary receives **334 MCP tools** (`~318 KB`, `~79,550 tokens`).

This MCP-heavy surface causes four recurring operational problems:

1. **Per-framework drift and tool confusion.** Each agent CLI (Claude Code, Codex, Antigravity/Jetski) has its own native tools, internal control tools, and MCP naming conventions (`mcp__grain-sandbox__<tool>` vs. `mcp_grain-sandbox_<tool>`). On Antigravity runs, the model is simultaneously shown 57 built-in Jetski declarations (including `exit_plan_mode`, `enter_plan_mode`, and `schedule`) alongside 35+ `mcp_grain-sandbox_*` schemas, while Grain's `PreToolUse` hook (`pkg/agent/antigravity/antigravity.go`) blocks every tool not on `PublishedToolNames()`. Over the measurement window leading to this task, Antigravity runs called `exit_plan_mode` 25 times (failing 25/25 times, 100%) and `schedule` (failing 100% of the time).
2. **60+ tool schemas in every prompt.** Serialized MCP tool schemas consume 14,000–35,000+ tokens on turn 1 before the task description or conversation is even read (`pkg/model/promptsize.go` budgets the entire prompt warning threshold at 50,000 tokens).
3. **Tool-result caps on every read.** Every MCP tool response passes through `mcp.maxToolResultBytes` (16 KiB–64 KiB caps), forcing agents to paginate through JSON tool arguments instead of piping output through `grep`, `jq`, `head`, `diff`, or files in `/tmp`.
4. **Duplication.** Every new data source or action in Grain requires a custom `pkg/mcp` tool definition, a `pkg/daemon/mcpserver.go` HTTP adapter, a `pkg/ui` REST route, and often a CLI command in `pkg/cli`.

Task 846 (superseding Task 843) introduced the read-only virtual namespace (`pkg/namespace` and `pkg/gitproxy/namespace.go`), serving `ci://` and `src://` over the per-sandbox HTTP proxy at `http://127.0.0.1:9728/fs/` with zero size caps.

This design takes the next architectural step: **expose no Grain tools by default except the 4 sandbox tools and a small model-only control set, and expose everything else through a preinstalled `grain` CLI inside the sandbox talking to the per-sandbox proxy.**

---

## Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────────┐
│ Agent Harness (Host / Executor)                                            │
│                                                                            │
│  Model ──► MCP Control Server (`grain mcpserver`, ~10-11 tools)            │
│             ├─ Sandbox tools: run_command, read_file, write_file, edit_file│
│             ├─ Turn/run-ending: chat, ask_question, request_secret,        │
│             │                   comment_on_issue, nothing_needed,          │
│             │                   wait_for_task (park), resume_later         │
│             ├─ Work-filing / escalation: propose_task, recreate_sandbox    │
│             └─ Dangerous grants (model-only): api:<dangerous>,             │
│                dispatch-control, self-upgrade, self-repair, external caps  │
└───────────────────────────────┬────────────────────────────────────────────┘
                                │ `run_command` (SSH / container exec / host)
                                ▼
┌────────────────────────────────────────────────────────────────────────────┐
│ Sandbox VM / Container (`/home/debian/work`)                               │
│                                                                            │
│  `/usr/local/bin/grain` CLI                                                │
│   ├─ Reads credential from `/home/debian/.git-credentials` (or env)        │
│   ├─ Calls `http://127.0.0.1:9728/{fs,cli,api,cap}/...`                    │
│   └─ Uncapped stdout/stderr, pipeable to grep/jq/diff/files                │
└───────────────────────────────┬────────────────────────────────────────────┘
                                │ Loopback forward (9728)
                                ▼
┌────────────────────────────────────────────────────────────────────────────┐
│ Per-Sandbox Proxy (`pkg/gitproxy` in `grain daemon`)                       │
│                                                                            │
│  1. Authenticates per-attempt sandbox token (`p.Tokens.Authenticate`)      │
│  2. Resolves live `(sandbox, taskID, runID)` and per-attempt grant scopes  │
│  3. Enforces non-dangerous scope ceiling & self-mutation guards            │
│  4. Stamps live UI status line (`ui.Activity`), wait credit, & telemetry   │
│  5. Audits every call via `recordAgentCall` (`agent_api_call` table)       │
│  6. Routes to `/fs/` (namespace), `/cli/` (CI/PR/task), `/api/`, `/cap/`   │
└────────────────────────────────────────────────────────────────────────────┘
```

---

## 1. Privilege Separation

### Threat Model Inside the Sandbox

A credential placed inside the sandbox is readable and usable by **any process running as the sandbox user (`debian`)**:
- Repository test suites (`go test ./...`, `pytest`, `cargo test`) executing untrusted or third-party code.
- Package manager lifecycle hooks (`npm install` `postinstall`, `pip` `setup.py`, Cargo build scripts).
- Malicious test fixtures or prompt-injected scripts in external pull requests or dependencies.
- Background processes left running across commands within the same attempt.

Because any process in the sandbox can read `/home/debian/.git-credentials` and make HTTP requests to `http://127.0.0.1:9728`, **the sandbox credential must never hold authority to escalate privileges, approve or force-merge code, mutate other tasks unless explicitly granted a non-dangerous scope, alter deployment configuration/secrets, or end/park the agent's run.**

### Exact Scope Split: Sandbox CLI Credential vs. Model-Only Control Set

#### A. Carried by the In-Sandbox CLI Credential (`http://127.0.0.1:9728`)

Every sandbox credential is minted per attempt in `pkg/gitproxy/tokens.go` and carries:

1. **Baseline Own-Task & Read Scopes (every run with a sandbox):**
   - **Namespace reads (`/fs/`):**
     - `ci://<task-repo>/...` (when the task holds `grain-ci`)
     - `task://<own-task-id>/...` (own task metadata, comments, handover, diff)
     - `config://repos/<task-repo>`, `config://workflows/`
     - `memory://global/...`, `memory://repos/<task-repo>/...`
     - `deploy://merge-queue` (read-only merge queue state, replacing `query_merge_queue`)
   - **Own-task CI & PR actions (`/cli/`):**
     - `grain ci wait`, `grain ci run-job`, `grain ci retrigger`, `grain ci list`, `grain ci log`, `grain ci test-history`, `grain ci artifact` (scoped strictly to the task's own repository and branch)
     - `grain pr status`, `grain pr base-status`, `grain pr open`, `grain pr reply`, `grain pr resolve`, `grain pr review-comment` (scoped strictly to the task's own branch / pinned PR number, exactly as `pullRequestToolsWithNumber` scopes them today)
   - **Own-task non-ending collaboration helpers (`/cli/`):**
     - `grain status <note>` (`POST /cli/status` — updates the live UI status row for the current run, replacing `update_status`)
     - `grain handover write` (`POST /cli/handover` — writes the standing handover note for the current task, replacing `write_handover`)
     - `grain notify` (`POST /cli/notify` — raises an operator notification scoped to the current task, capped at 10 per run / 20 per task, replacing `notify_operator`)
     - `grain task message <id>` (`POST /cli/tasks/{id}/notes` — leaves an agent note on the parent or child task's conversation, replacing `message_task`)
     - `grain task wait <id>` (`POST /cli/tasks/wait` — **blocking wait only**, never `park: true`)
     - `grain git revert <task-or-sha>` (runs `git revert` locally inside `/home/debian/work` using metadata fetched from `/cli/tasks/{id}/revert-info`, replacing `revert_task` and `revert_merge`—which previously failed on Granule VMs and containers because `pkg/daemon/mcpserver.go` required a host `-sandbox-root`!)

2. **Non-Dangerous Granted Scopes (when attached to the task's `capabilities:`):**
   - `self-debug` / `api:self-debug` → widens `/fs/ci://`, `/fs/task://`, `/fs/src://`, `/fs/config://`, `/fs/deploy://metrics`, `/fs/deploy://pipeline-changes`, and `/cli/metrics`, `/cli/tasks` across the deployment.
   - `grain-log` → enables `/fs/deploy://grain.log` and `grain log` (passing through `grainlog.Redactor` on the daemon before bytes ever enter the proxy response).
   - `deployment-health` → enables `/fs/deploy://health` and `/fs/deploy://state` (redacted).
   - `bootstrap-playbooks` → enables `/fs/src://playbooks/` and `grain playbooks`.
   - `memory` / `api:memory:write` → enables `grain memory write` / `grain memory doc write` (attributed to `Principal{Kind: PrincipalAgent, ID: taskID}`).
   - `release-notes` → enables `grain release-notes stats` and `grain release-notes publish`.
   - `read-issues` → enables `grain issue list` and `grain issue read` (scoped to readable GitHub repos).
   - `quarantine-test` → enables `grain quarantine`.
   - `db-read` → enables `grain db query` (read-only SQL queries).
   - **Non-dangerous `api:<scope>` grants** (`pkg/model/agent_api.go` scopes where `Dangerous == false`, except escalation scopes noted below):
     - Read scopes: `tasks:read`, `ci:read`, `dispatch:read`, `schedules:read`, `templates:read`, `suites:read`, `releases:read`, `repos:read`, `boards:read`, `memory:read`, `sessions:read`, `caches:read`, `settings:read`, `connections:read`, `capabilities:read`, `secrets:read` (names only, never values), `deployment:read`.
     - Safe write scopes: `ci:write`, `notifications:write`, `schedules:write`, `templates:write`, `suites:write`, `releases:write`, `boards:write`, `memory:write`, `caches:write`, plus `tasks:write` and `merge:write` subject to the escalation guards in `pkg/ui/agent_api_gate.go` (see below).
   - **Non-dangerous capability `mcp_endpoint` tools:** capability server tools explicitly marked `model_only: false` (see §7), invoked via `grain cap <server> <tool>`.

#### B. Kept Strictly in the Model-Only Control Set (Never on the Sandbox Credential)

The proxy **refuses** these operations on the sandbox credential (`403 Forbidden: this operation requires a model control tool`), and `grain mcpserver` serves them directly to the model over the host-side MCP stdio/socket channel:

| Category | Model-Only Tools / Scopes | Why It Cannot Live on the Sandbox Credential |
|---|---|---|
| **Turn & Run Lifecycle** | `chat`, `ask_question`, `request_secret`, `comment_on_issue`, `nothing_needed`, `wait_for_task(park: true)`, `resume_later` | The harness (`orchestrator.ProcessResult` and `agent.DecideStop`) inspects MCP tool calls to know whether the run completed, parked on a human question, requested a secret box, or suspended its VM. A subprocess inside `run_command` must not be able to terminate or park the agent's run. |
| **Sandbox Lifecycle** | `recreate_sandbox` | Destroys the running sandbox VM/container and reprovisions it while the MCP server holds the outer session open. Calling this from inside `run_command` kills the caller mid-flight and would let a rogue test wipe forensic state. |
| **Work Filing & Approval Escalation** | `propose_task` (full contract with inherited `auto_merge` / auto-approval / `supersedes`) | For an `auto_merge: true` task or a session with `auto_approve_proposals`, `propose_task` files an **already-approved, auto-merging task** that dispatches with zero human review (`pkg/orchestrator/finish.go`). If reachable from the sandbox credential, a malicious test in a PR could spawn an autonomous auto-merging task with arbitrary instructions, or close queued tasks via `supersedes`. *(Note: `grain task propose` is also offered in the CLI for convenience, but when called over the sandbox credential the proxy hard-forces `hold_for_approval: true`, `auto_merge: false`, and rejects `supersedes`, so a sandbox process can at most file an unapproved draft subject to a per-attempt cap of 10 proposals. Only the model-only `propose_task` tool can inherit auto-approval, auto-merge, or `supersedes`.)* |
| **Dangerous API Scopes (`Dangerous: true` in `pkg/model/agent_api.go`)** | `api:tasks:admin` (`approve`, `recreate`), `api:merge:force`, `api:settings:write`, `api:connections:write`, `api:secrets:write`, `api:capabilities:admin`, `api:deployment:upgrade`, `api:deployment:restart`, `api:deployment:reboot`, and any `RequiresPerson` scope set | These scopes approve unreviewed code, bypass merge gates, rewrite deployment settings/secrets, alter capability policies, or reboot the controller host. |
| **Queue & Host Control Capabilities** | `dispatch-control` / `pipeline-control` (`api:dispatch:write`: `pause`, `resume`, `run-now`), `self-upgrade` (`upgrade_deployment`, `cancel_deployment_upgrade`), `self-repair` (`run_host_command`), `db-write` (`exec_db`), `team-broadcast` (`broadcast_team_message`), `repos:write`, `sessions:write` | Typing into other live sessions (`sessions:write`), adding/modifying repositories (`repos:write`), pausing/unpausing the deployment pipeline (`dispatch:write`), running commands on the controller host (`self-repair`), or mutating the database (`db-write`) are high-impact control-plane actions that must only be invoked by the model's explicit tool call. |
| **Model-Only External Capability Tools** | External capability server tools with `model_only: true` (the default for external servers; see §7) | External capability servers (e.g., production cloud admin, chat posting, incident paging) may carry production side effects; keeping them model-only by default prevents sandbox processes from invoking them unless the capability author explicitly marks a tool `model_only: false`. |

#### Self-Mutation and Escalation Guards on `tasks:write` and `merge:write`

When a task holds `api:task-admin` (such as the Pipeline Steward), its sandbox CLI credential receives the non-dangerous subset (`tasks:read`, `tasks:write`, `merge:write`, `ci:read`, `ci:write`, `notifications:write`). On the proxy, every `/api/` request passes through `agentAPIGate` (`pkg/ui/agent_api_gate.go`), which already enforces:
1. **`selfMutableTaskRoutes` (`pkg/ui/agent_api_gate.go`):** A task can **never** approve, withdraw approval, close, reopen, stop, retry, defer, undefer, run-now, force-merge, retry-merge, or edit `approved`, `autoMerge`, `capabilities`, or `dependsOn` on **itself**.
2. **`agentFilingEscalation` (`pkg/ui/agent_api_gate.go`):** A caller with `tasks:write` (without `tasks:admin`) can **never** file a task with `approved: true` or `autoMerge: true`, and can **never** attach a capability the acting task does not itself hold or any capability containing a dangerous scope.
3. **Additional Sandbox-Credential Guard:** When `tasks:write` is exercised via the sandbox proxy credential (rather than a model-only tool), `PATCH /api/tasks/{id}` and `POST /api/tasks/{id}/capabilities` also forbid attaching any new capabilities to existing tasks.

### Evaluation of Alternative: Per-Turn Token Handed Only to the Model

The prompt asks us to evaluate an alternative design: **what if the harness mints a fresh per-turn token on each model turn and hands it only to the model (e.g. via a hidden turn addendum or tool wrapper), allowing dangerous or escalation operations to move to the `grain` CLI inside `run_command`?**

We evaluated three variants of a per-turn token and reject them as a security boundary for dangerous operations:

1. **Variant A: Model passes `--turn-token <tok>` on the `grain` command line inside `run_command`.**
   - *Failure mode 1 (`/proc` exposure):* In Linux sandboxes, `/proc/<pid>/cmdline` and `/proc/<pid>/environ` are readable by every process running under the same UID (`debian`). Any background process spawned by an earlier `go test` or `npm install` in the same attempt can poll `/proc` (or `ps aux`) during subsequent `run_command` invocations, steal the turn token within milliseconds, and race the CLI call against `127.0.0.1:9728`.
   - *Failure mode 2 (Shell/workspace interception):* `run_command` executes `bash -c <command>` inside `/home/debian/work`. A malicious repository test or build step that modifies `/home/debian/.bashrc`, `$BASH_ENV`, git hooks, or workspace wrapper scripts can intercept the token the next time the model runs a command.
   - *Failure mode 3 (Ergonomics and token waste):* Requiring the LLM to copy a random 32-byte token into every CLI command wastes tokens and causes frequent copy-paste hallucinations.
2. **Variant B: Harness injects `GRAIN_TURN_TOKEN` into `run_command`'s environment for that single command.**
   - While this hides the token from the LLM, **every child process of `run_command` inherits the environment**. If the model runs `grain ... && go test ./...` or if a background process reads `/proc/<pid>/environ` of the `run_command` shell, the token is immediately compromised. Even if the token is single-use, a background watcher can consume it before `grain` starts, or a command that runs tests first (`go test && grain ...`) hands the single-use token directly to the test binary.
3. **Variant C: Dedicated out-of-sandbox execution channel (`grain_control` MCP tool).**
   - If the model invokes a separate tool that does *not* run inside the sandbox shell so that sandbox processes cannot see the token, that tool *is* an MCP control tool.

**Conclusion:** A credential that enters the sandbox guest OS (`debian` UID) cannot be isolated from other processes running as `debian` in that same guest OS. Therefore, the architectural boundary between **in-sandbox CLI (per-attempt token on `127.0.0.1:9728`, scoped to reads + safe own-task/granted actions)** and **out-of-sandbox MCP control tools (for run-ending, work-filing escalation, and dangerous scopes)** is the only boundary that holds under adversarial sandbox processes.

### Existing Per-Sandbox Git-Proxy Credential Model & Auditing

Today (`pkg/gitproxy/tokens.go`, `pkg/gitproxy/server.go`, `pkg/gitproxy/namespace.go`):
- When an attempt starts, the orchestrator calls `gitproxy.TokenStore.Issue(sandbox, taskRepo, ...)` which mints a random 32-byte `gpx_...` token with `DefaultSandboxTokenTTL = 24h` (heartbeat-extended while the run is live, revoked immediately when the attempt finishes or the sandbox is torn down, and persisted encrypted under `sandbox-tokens.key`).
- Inside the sandbox, `/home/debian/.git-credentials` holds `http://x-access-token:<token>@127.0.0.1:9728`.
- `gitproxy.NewHandler` (`pkg/gitproxy/server.go`) already muxes:
  - `/fs/` → `ServeNamespaceHTTP` (`pkg/gitproxy/namespace.go`), which extracts the token from `Authorization: Bearer` or `Basic`, validates it via `p.Tokens.Authenticate(token)`, and looks up the live run via `p.Store.SandboxNamespaceScope(ctx, sandbox)`.
  - `/` → Git smart-HTTP proxy (`authorize.go`).

**How this design extends `pkg/gitproxy`:**
1. **Per-Attempt Binding:** `gitproxy.Grant` (`pkg/gitproxy/tokens.go`) gains `TaskID string` and `Attempt int` (populated at `Issue` time alongside `Sandbox`). Even if a sandbox name is reused across attempts of a durable task or console session, rotating or re-binding the token per attempt ensures credentials and audit records are pinned to the exact `(TaskID, Attempt)`.
2. **New Proxy Prefixes on `127.0.0.1:9728`:**
   - `/fs/<scheme>/<path>` — Virtual namespace reads (existing from Task 846, widened with Stage 2 roots `task://`, `config://`, `memory://`, `deploy://`).
   - `/cli/...` — Structured JSON endpoints for the `grain` CLI's own-task CI, PR, status, handover, notification, and blocking-wait commands.
   - `/api/...` — Direct pass-through to `ui.Server`'s agent API mux with `X-Grain-Acting-Task: <taskID>` and a new internal marker `X-Grain-Channel: sandbox-cli` set by the proxy after authenticating the sandbox token (never trusted from client headers). `agentAPIGate` (`pkg/ui/agent_api_gate.go`) rejects any route whose scope is `Dangerous: true` or on the model-only list when `X-Grain-Channel == "sandbox-cli"`.
   - `/cap/<server>/mcp` — Reverse proxy to `/runs/<attempt_id>/mcp` (`pkg/orchestrator/capability_endpoints.go`) for capability servers granted to the attempt, filtering `tools/call` against the `model_only` policy (§7).
3. **Unified Auditing (`recordAgentCall`):**
   - Every `/cli/`, `/api/`, and `/cap/` call through the proxy invokes `recordAgentCall` (`pkg/ui/agent_api_gate.go`), writing a row to `agent_api_call` (`TaskID`, `Method`, `Path`, `Scope`, `Status`, `Reason`, `CreatedAt`) and logging `agent api: task=%s attempt=%d channel=sandbox-cli ...` to `systemlog`.
   - Read-heavy `/fs/` calls are recorded in the per-run tool telemetry (`task_run_tool`, see §3) so they do not flood the `agent_api_call` mutation audit log while remaining 100% visible in run transcripts and metrics.

---

## 2. Blocking and Turn-Ending Semantics

### The Timeout Mismatch

- `run_command` enforces a maximum per-command timeout of **600 seconds (10 minutes)** (`maxCommandTimeout = 600_000` ms in `pkg/mcp/sandbox_tools.go`).
- `wait_for_checks` (`pkg/mcp/wait_for_checks_tool.go`, `pkg/mcp/grain_ci_wait_tool.go`) and `wait_for_task` (`pkg/mcp/wait_for_task_tool.go`) default to **900 seconds (15 minutes)** and allow up to **3,600 seconds (1 hour)** (`MaxWaitForChecksTimeout = time.Hour`).
- While blocked inside `wait_for_checks` or `wait_for_task`, the MCP server credits the blocked duration back to the run's wall-clock deadline via `RunWaitCredits.Credit(taskID, blocked)` (`pkg/orchestrator/waitcredit.go`, capped at `RunWaitCreditCap = 45 * time.Minute` per attempt) and marks the run's activity status with `IsDeclaredWait: true` so the durable-task idle watchdog (`DurableIdleTimeout = 60m`) does not kill the run as stalled.

### Per-Operation Decision Table

| Operation | Surface | Blocking / Turn-Ending Behavior & Wait-Credit Mechanism |
|---|---|---|
| `chat` | **Model-only control tool** | Blocks in `grain mcpserver` until a human replies in the console session, crediting wait time and advancing `deliveredCursor`. Cannot be a CLI command because it mediates turn-by-turn human chat. |
| `ask_question` | **Model-only control tool** | Ends the run immediately (`orchestrator.ProcessResult` parks the task on the question). |
| `request_secret` | **Model-only control tool** | Ends the run immediately and renders the encrypted secret input box in the UI. |
| `comment_on_issue` | **Model-only control tool** | Ends the run (when no branch is pushed) or records the closing report relayed into the task conversation. |
| `nothing_needed` | **Model-only control tool** | Ends the run and closes the task (or leaves for ack) with the stated reason. |
| `resume_later` | **Model-only control tool** | Ends the run after verifying no unpushed work exists in the sandbox (`SandboxUnpushedWork`), suspending the task until `when` (`ci`, `task`, `at`, `message`) is satisfied. |
| `wait_for_task` (`park: true` or `resume_at`) | **Model-only control tool** | Ends the run after checking `SandboxUnpushedWork`, adds `depends_on` (or defers until `resume_at`), and tears down the sandbox VM. |
| `wait_for_task` (blocking wait, `park: false`) | **Sandbox CLI:** `grain task wait <id>... [--until closed\|merged] [--timeout 9m]` | Moves to the CLI over `POST /cli/tasks/wait`. Bounded to `--timeout` (default & max `9m` / `540s` per CLI invocation so it always returns cleanly before `run_command`'s `600s` ceiling). Server-side wait credit + resumable polling (see below). |
| `wait_for_checks` | **Sandbox CLI:** `grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]` | Moves to the CLI over `POST /cli/ci/wait`. Starts Grain CI (or attaches to the in-flight run / `--resume <run-id>`), long-polls up to `540s` (`9m`), applies server-side wait credit, and returns a resumable token if still running at `9m`. For waits where the agent prefers to release the VM, the agent calls `resume_later(when: {ci: "my-branch"})`. |
| `run_ci_job` | **Sandbox CLI:** `grain ci run-job <job> [--timeout 9m]` | Moves to the CLI over `POST /cli/ci/run-job`, sharing the same `540s` long-poll + `--resume <run-id>` and server-side wait-credit path. |

### Resumable Long-Poll Pattern with Server-Side Wait Credit (`grain ci wait` & `grain task wait`)

Instead of keeping `wait_for_checks` as a model-only MCP tool, moving it to `grain ci wait` inside the sandbox has a major advantage: the agent can chain `git push origin <branch> && grain ci wait` in a single turn, or pipe the output of a failed check directly into `grain ci log`. To make this work seamlessly within `run_command`'s 600-second ceiling:

1. **9-Minute Poll Slice (`DefaultCLIWaitSlice = 540 * time.Second`):**
   - `grain ci wait` sends `POST http://127.0.0.1:9728/cli/ci/wait` with `{"jobs": [...], "resume_run_id": "...", "timeout_seconds": 540}`.
   - If the caller invokes `run_command` with its default 300s timeout, `grain` also reads `GRAIN_COMMAND_TIMEOUT_MS` (or the proxy caps the HTTP hold to `min(req.Timeout, ctx.Deadline - 15s, 540s)`) so the HTTP response **always returns before `run_command` kills the subprocess**. (In the prompt section, agents are instructed to pass `timeout: 600000` when running `grain ci wait`, or let `grain ci wait` auto-slice at 270s when `run_command` uses the 300s default.)
2. **Server-Side Wait Credit (`pkg/orchestrator/waitcredit.go`):**
   - Because `/cli/ci/wait` and `/cli/tasks/wait` execute inside `grain daemon` (via the proxy handler), the daemon holds `RunWaitCredits` directly in memory!
   - When `/cli/ci/wait` begins waiting, the daemon marks the run's live activity status with `IsDeclaredWait: true` (`"waiting for CI on the branch"`).
   - Every 15 seconds while the HTTP request is open—and once more when the request returns—the handler calls `RunWaitCredits.Credit(taskID, elapsedSlice)`, extending the attempt's `runDeadline` up to `RunWaitCreditCap` (45 minutes) and returning the updated credit in the JSON response.
   - Because the credit is applied **server-side in `grain daemon`** and pushed to the executor's deadline tracker, `run_command`'s own execution does not eat into the attempt's 60-minute active work budget while `grain ci wait` is blocked.
3. **Resumable Token on Slice Expiry:**
   - If the CI run finishes within the poll slice, `grain ci wait` prints the verdict (plus shared-fault annotations from `GrainCISharedFaults` when a job fails) and exits `0` (green) or `1` (red).
   - If the CI run is still running when the poll slice expires, `/cli/ci/wait` returns HTTP 200 with `status: "running"` and `resume_run_id: "<run-id>"`. `grain ci wait` prints:
     ```
     CI run ci-1942 is still running after 9m0s (jobs running: ui-e2e; passed: go, lint; wait credit used: 9m0s / 45m0s).
     To keep waiting in this run:  grain ci wait --resume ci-1942
     To suspend and free the VM:   call resume_later with when: {"ci": "ci-1942"}
     ```
     and exits `0` (so chained shell commands know the poll slice completed cleanly without a test failure). Calling `grain ci wait --resume ci-1942` (or bare `grain ci wait`, which automatically reuses the identical in-flight run on the branch tip) continues waiting from where it left off.

---

## 3. Status Line and Telemetry

Today, two observability pipelines depend on `grain mcpserver` intercepting MCP tool calls:
1. **Per-run UI activity line (`pkg/mcp/tool_activity.go` & `pkg/ui/activity.go`):** `Registry.reportActivity` maps each MCP tool name to a human-readable phrase (e.g. `wait_for_checks` → `"waiting for CI on the branch"`, `get_ci_log` → `"reading CI logs"`) and POSTs it to `/api/tasks/{id}/status` with `IsDeclaredWait: true/false`.
2. **Tool & Check-Wait Census (`pkg/mcp/telemetry.go` & `pkg/metrics/tools.go`):** `orchestrator` extracts `agent.Result.ToolCalls` from the framework transcript and writes `task_run_tool` rows (powering `read_grain_metrics` `tools` section: call counts, runs, error rates, result sizes, and zero-call warnings) and `task_run_check_wait` rows (powering `read_grain_metrics` `checks` section: check-wait duration percentiles).

If 25+ MCP tools move into `grain <subcommand>` invoked inside `run_command` without server-side instrumentation, every CLI call would collapse into `run_command` in `task_run_tool`, `task_run_check_wait` would record zero rows, and the UI status row would only ever show `run_command`'s description.

### Server-Side Activity Reporting from the Proxy

Because every `grain` CLI call goes through `http://127.0.0.1:9728/{fs,cli,api,cap}/...` on the daemon's proxy server, and the proxy authenticates `(sandbox, taskID, attempt)` on every request:

1. **Route-to-Activity Table (`pkg/gitproxy/activity.go`):**
   The proxy maps each incoming request to the exact same human-readable status phrases `pkg/mcp/tool_activity.go` uses today:

   | Proxy Request | Logical Telemetry Name | UI Activity Phrase (`ui.Activity`) | `IsDeclaredWait` |
   |---|---|---|---|
   | `GET /fs/ci://...` (list) | `grain_ls` (`ci://`) | `"listing CI namespace"` | `false` |
   | `GET /fs/ci://...` (read) | `grain_read` (`ci://`) | `"reading CI logs"` | `false` |
   | `GET /fs/...?grep=` | `grain_grep` | `"searching namespace"` | `false` |
   | `GET /fs/src://...` | `grain_read` (`src://`) | `"reading grain source"` | `false` |
   | `GET /fs/task://...` | `read_grain_task` | `"reading a task"` | `false` |
   | `GET /fs/deploy://grain.log` | `read_grain_log` | `"reading the daemon log"` | `false` |
   | `GET /fs/deploy://health` | `read_deployment_health` | `"reading deployment health"` | `false` |
   | `GET /fs/deploy://merge-queue` | `query_merge_queue` | `"checking the merge queue"` | `false` |
   | `POST /cli/ci/wait` | `wait_for_checks` | `"waiting for CI on the branch"` | **`true`** |
   | `POST /cli/ci/run-job` | `run_ci_job` | `"running a single CI job"` | **`true`** |
   | `POST /cli/ci/retrigger` | `retrigger_ci` | `"retriggering failed CI jobs"` | `false` |
   | `GET /cli/ci/runs` | `list_ci_runs` | `"listing recent CI runs"` | `false` |
   | `GET /cli/ci/test-history` | `query_test_history` | `"querying test history"` | `false` |
   | `GET /cli/pr/status` | `pull_request_status` | `"checking pull request status"` | `false` |
   | `GET /cli/pr/base-status` | `base_branch_status` | `"checking base branch CI"` | `false` |
   | `POST /cli/pr/open` | `open_pull_request` | `"opening the pull request"` | `false` |
   | `POST /cli/pr/reply` | `reply_to_pull_request_comment` | `"replying to a review comment"` | `false` |
   | `POST /cli/pr/resolve` | `resolve_review_thread` | `"resolving a review thread"` | `false` |
   | `POST /cli/pr/review-comment` | `add_review_comment` | `"recording a review comment"` | `false` |
   | `POST /cli/tasks/wait` | `wait_for_task` | `"waiting on another task"` | **`true`** |
   | `POST /cli/status` | `update_status` | *(sets caller's status phrase directly)* | `false` |
   | `POST /cli/handover` | `write_handover` | `"writing the task handover"` | `false` |
   | `POST /cli/notify` | `notify_operator` | `"notifying the operator"` | `false` |
   | `POST /cli/tasks/{id}/notes` | `message_task` | `"messaging another task"` | `false` |
   | `* /api/...` | `api_<handler_name>` | `"calling agent API <method> <path>"` | `false` |
   | `POST /cap/<srv>/mcp` | `cap__<srv>__<tool>` | `"calling capability <srv>/<tool>"` | `false` |

2. **Stacking with `run_command`'s Activity Note:**
   - When `run_command` starts (e.g. `description: "running grain ci wait"`), `mcpserver` sets the outer activity note on `ui.Activity`.
   - When the proxy receives a `/cli/ci/wait` request inside that `run_command`, the proxy pushes `"waiting for CI on the branch"` with `IsDeclaredWait: true` onto the run's activity state. When the `/cli/ci/wait` HTTP call finishes, if the outer `run_command` is still running, the activity state reverts to the outer `run_command` description (or gets replaced by the next CLI call).
   - Crucially, while `/cli/ci/wait`, `/cli/ci/run-job`, or `/cli/tasks/wait` is in flight, `IsDeclaredWait` is `true` in `ui.Activity`, so the durable-task idle watchdog (`pkg/orchestrator/run.go`) knows a declared wait is in progress.

### Server-Side Tool & Check-Wait Telemetry Attribution

1. **Recording `task_run_tool` Rows Directly from the Proxy:**
   - The proxy wraps its `http.ResponseWriter` with a byte-counting and status-capturing writer.
   - On completion of every `/fs/`, `/cli/`, `/api/`, and `/cap/` request, the proxy appends a `model.ToolCallRecord` (`Name`: logical tool name from the table above or `grain/<subcommand>`, `DurationMS`, `ResultBytes`: response body bytes, `IsError`: `status >= 400`, `Source`: `"cli"`) to the live attempt's tool-call buffer in `model.Store` (flushed into `task_run_tool` alongside the MCP control-tool calls when the attempt finishes).
   - **Canonical Name Continuity:** By mapping CLI routes to their canonical tool names (e.g. `wait_for_checks`, `list_ci_runs`, `read_grain_task`, `api_list_tasks`) or recording both the canonical name and `channel = 'cli'`, `pkg/metrics/tools.go` (`read_grain_metrics` `tools` section) continues to report accurate invocation counts, error rates, and response byte percentiles across the migration with zero discontinuity.
   - **Updating `publishedTools` in `pkg/metrics/tools.go`:** Once legacy MCP tools are removed from `grain mcpserver` in Stage 5, `PublishedToolNames()` in `pkg/agent/toolroster.go` is split into `PublishedControlToolNames()` (the MCP control set) and `PublishedCLICommandNames()` (the `grain` CLI operations), so the "published with 0 calls" detector in `read_grain_metrics` tracks both surfaces accurately.
2. **Recording `task_run_check_wait` Rows Directly from `/cli/ci/wait`:**
   - Today, `orchestrator` records a `task_run_check_wait` row whenever a `wait_for_checks` MCP tool call completes.
   - Under this design, the `/cli/ci/wait` handler in the daemon directly records the `task_run_check_wait` measurement (aggregating consecutive `--resume` slices for the same CI run ID into a single logical check wait, or recording each wait slice with `run_id`) so `read_grain_metrics` `checks` section remains 100% populated.

---

## 4. Discoverability and Prompts

### `grain` CLI Command Hierarchy & Help Surface

The `grain` binary is already built and baked into the sandbox image (`/usr/local/bin/grain`). When invoked inside a sandbox (detected automatically via `GRAIN_SANDBOX_PROXY=http://127.0.0.1:9728` or the presence of `127.0.0.1:9728` in `/home/debian/.git-credentials`), `grain` operates in sandbox mode and offers a cohesive, self-documenting command tree:

```text
$ grain help
grain — sandbox CLI for interacting with this grain deployment (proxy: http://127.0.0.1:9728)

NAMESPACE & FILES (no size cap; pipe to grep, jq, head, or redirect to files):
  grain ls <uri> [--glob <pat>]          List directory or glob inci://, src://, task://, config://, memory://, deploy://
  grain read <uri> [--tail N] [--offset N --limit N]
                                         Read a file from the namespace to stdout
  grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>]
                                         Search a file or subtree in the namespace with an RE2 regex

CI & PULL REQUEST (scoped to this task's branch):
  grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]
                                         Start or attach to grain CI for this branch and wait for verdict (with wait credit)
  grain ci run-job <job> [--timeout 9m]  Run a single named CI job against the current branch commit
  grain ci retrigger [--jobs j1,j2|--all] [--commit <sha>]
                                         Rerun failed (or named) CI jobs uncached on the same commit
  grain ci list [--ref <b|task>] [--conclusion <c>] [--job <j>] [--since <d>] [--full]
                                         List recent grain CI runs
  grain ci log [<run-id>] [--job <j>] [--step <s>] [--tail N] [--attempt K]
                                         Stream or tail a CI job/step log to stdout (no 16 KB cap)
  grain ci test-history <test-name> [--limit N]
                                         Show pass/fail/flake history across recent CI runs
  grain ci artifact <job> <name> [-o <dest>] [--run <run-id>]
                                         Download a CI artifact into the workspace
  grain pr status                        Show branch tip, open pull request, review threads, and CI summary
  grain pr base-status                   Show CI status on the target base branch
  grain pr open                          Open or update the pull request for this branch from its commit messages
  grain pr reply <body|--file f> [--comment-id <id>]
                                         Reply to an inline review comment or post a top-level PR comment
  grain pr resolve (--thread-id <id> | --comment-id <id>)
                                         Mark a GitHub inline review conversation resolved
  grain pr review-comment <body|--file f> [--path <file> --line <N>]
                                         Record a code-review feedback comment

TASK, COLLABORATION & GIT:
  grain status <one-line-note>           Update this run's live activity status in the UI (<120 chars)
  grain handover write (<text> | --file <f> | -)
                                         Write or replace the standing handover summary for this task
  grain task wait <id>... [--until closed|merged] [--timeout 9m]
                                         Block until other task(s) close or merge (for long waits that free the VM, use the wait_for_task or resume_later tool)
  grain task message <id> (<body|--file f>)
                                         Leave a note on a parent or child task's conversation
  grain task propose --title <t> (--body <b> | --file <f>) [flags]
                                         File an unapproved follow-up task proposal (use the propose_task tool to inherit auto-merge/auto-approval or supersede)
  grain notify --subject <s> --title <t> [--body <b>] [--severity info|warning] [--clears-when <json>]
                                         Raise an operator notification without parking this task
  grain git revert (--task <id> | --commit <sha>) --reason <why>
                                         Create a clean git revert commit on the current branch
  grain merge-queue [--repo <r>] [--branch <b>] [--since <d>] [--full]
                                         Inspect merge queue batches, queued PRs, and held gates

GRANTED CAPABILITIES & AGENT API (shown based on this task's grants):
  grain cap list                         List capability servers and tools granted to this attempt
  grain cap <server> <tool> [flags | --json '<args>']
                                         Invoke a sandbox-accessible capability tool
  grain api routes [--scope <s>]         List agent API routes granted to this attempt
  grain api <METHOD> <path> [--body '<json>' | --field k=v] [--reason <why>]
                                         Call a granted non-dangerous agent API route

Run 'grain <command> --help' for flags and examples.
```

Every subcommand supports `--help` (and `-h`) with copy-pasteable examples, exact flag descriptions, and JSON output mode (`--json`) where applicable. If a run calls a subcommand whose capability or scope it does not hold (for example, `grain log` without `grain-log` or `grain read src://...` without `self-debug`), the proxy returns the same actionable refusal message `unknownToolMessage` in `pkg/mcp/toolnames.go` produces today:
> `grain read src://pkg/ci: this run does not hold the "self-debug" capability. Nothing you can do from inside this run will add it — a human attaches it to the task.`

### Short Prompt Section Replacing Tool Schemas

In `pkg/orchestrator/run.go`, `BuildPrompt` adds a compact `### Grain CLI (`grain`)` section (~1,420 bytes / ~355 tokens) right after the sandbox workspace paragraph:

```markdown
### Grain CLI (`grain`)

Inside your sandbox, the `grain` CLI (`run_command`) reaches this deployment's namespace, CI, pull request, merge queue, and any non-dangerous capabilities granted to this task — with no MCP tool-result size cap:

- **Namespace (`ci://`, `src://`, `task://`, `config://`, `memory://`, `deploy://`):**
  `grain ls <uri>`, `grain read <uri> [--tail N]`, `grain grep <re> <uri>`.
  Examples: `grain ls ci://bwsalmon/grain`, `grain read ci://bwsalmon/grain/<run>/go/gotest.log --tail 16384`.
- **CI & Pull Requests (for this task's branch):**
  `grain ci wait [--jobs j1,j2]` (starts/waits for grain CI with wait credit; pass `timeout: 600000` to `run_command`, and if still running after 9m re-run `grain ci wait --resume <run-id>`), `grain ci run-job <job>`, `grain ci retrigger [--jobs ...]`, `grain ci list`, `grain ci log [<run-id>] [--job <j>] [--tail N]`, `grain ci test-history <test>`, `grain pr status`, `grain pr base-status`, `grain pr open`, `grain pr reply`, `grain pr resolve`, `grain pr review-comment`.
- **Task & Collaboration:**
  `grain status "<note>"`, `grain handover write --file <path>`, `grain task wait <id>`, `grain task message <id> "<body>"`, `grain notify --subject <s> --title <t>`, `grain git revert --task <id> --reason "<why>"`, `grain merge-queue`.
- **Turn-ending, parking, and proposals stay as model tools:**
  Use `comment_on_issue`, `nothing_needed`, `ask_question`, `request_secret`, `wait_for_task` (`park: true`), `resume_later`, and `propose_task` directly as tool calls, not shell commands. Run `grain help` or `grain <subcommand> --help` for full flag details.
```

When a task holds additional grants (`self-debug`, `grain-log`, `deployment-health`, `memory`, `api:<scope>`, or external capability servers), `capabilityPassPromptSection` (`pkg/orchestrator/capability_materialize.go`) appends one concise bullet per grant naming the unlocked `grain` commands (e.g., `- **self-debug:** \`grain ls/read/grep src://...\`, \`grain read task://<id>/...\`, \`grain metrics [--section <name>]\`, \`grain api GET /api/pipeline/changes\`.`).

### Measured Prompt and Schema Size Reduction

We measured the exact wire JSON size (`[]struct{Name, Description, InputSchema}`) of the MCP tool rosters in `pkg/daemon/mcpserver.go` and `pkg/ui/agent_api_tools.go`, converted to tokens using `pkg/model/promptsize.go`'s `PromptBytesPerToken = 4` (`PromptContextTokens = 200,000`, `PromptWarnTokens = 50,000`):

| Roster Configuration | Today: MCP Tools | Today: Schema Bytes (~Tokens) | Sandbox-First: MCP Control Tools | Sandbox-First: Schema + CLI Prompt Bytes (~Tokens) | Net Reduction |
|---|---:|---:|---:|---:|---:|
| **Ordinary coding task** (`grain-ci`) | 35 tools | 56,635 B (~14,158 tok, **7.1%** of 200k window) | 11 tools (or 10 if `propose_task` schema is compacted) | 15,820 B schema + 1,420 B prompt = **17,240 B (~4,310 tok, 2.2%)** *(28,803 B / ~7,555 tok before `propose_task` schema compaction)* | **-39,395 B (-9,848 tok, -69.6%)** |
| **Interactive chat session** (`grain-ci` + `chat`) | 36 tools | 59,306 B (~14,826 tok, **7.4%**) | 12 tools | 18,491 B schema + 1,420 B prompt = **19,911 B (~4,978 tok, 2.5%)** | **-39,395 B (-9,848 tok, -66.4%)** |
| **Pipeline Steward / Triage** (`self-debug`, `grain-log`, `deployment-health`, `memory`, `api:task-admin`, `api:dispatch-control`) | 118 tools | 138,140 B (~34,535 tok, **17.3%**) | 15 tools (11 base + 4 `dispatch-control` model-only tools) | 19,210 B schema + 1,950 B prompt = **21,160 B (~5,290 tok, 2.6%)** | **-116,980 B (-29,245 tok, -84.7%)** |
| **Full Operator / All API Scopes** (35 default + 22 capability + 277 `api_*` routes) | 334 tools | 318,196 B (~79,549 tok, **39.8%**) | 11 base + 66 dangerous `api_*` routes = 77 tools *(or 12 tools with single `grain_admin_api` control tool)* | 21,400 B–82,200 B (**~5,350–20,550 tok**) | **-236,000 B to -296,800 B (-74% to -93%)** |

#### Additional Compaction Opportunity in the Control Set

In the 11-tool control set (`run_command`, `read_file`, `edit_file`, `write_file`, `ask_question`, `request_secret`, `comment_on_issue`, `nothing_needed`, `wait_for_task`, `resume_later`, `propose_task`), **`propose_task` alone accounts for 14,767 bytes (~3,691 tokens, 51% of the control set)** because its description embeds multi-paragraph essays on `auto_merge`, `supersedes`, `hold_for_approval`, and `custom_fields`. By trimming `propose_task`'s schema description to concise parameter rules (~1,800 bytes) and placing the detailed reference in `grain help propose`, the entire 11-tool control set drops from 28,803 bytes to **15,836 bytes (~3,959 tokens)**.

Similarly, for tasks holding dangerous `api:*` scopes (66 routes across `tasks:admin`, `merge:force`, `settings:write`, `connections:write`, `secrets:write`, `capabilities:admin`, `deployment:*`), instead of registering up to 66 individual `api_*` MCP tools (`64,982 bytes`), `grain mcpserver` can expose a single model-only **`api_admin`** MCP tool (`method`, `path`, `body`, `reason`) backed by `grain api routes --dangerous` for discovery, keeping the MCP schema under **5,000 tokens** even for full-admin sessions.

---

## 5. Frameworks

### How Each Framework Sees the Surface

| Framework | Native Tools Disabled / Blocked? | MCP Control Tools Exposed | How Sandbox Shell & File Operations Work |
|---|---|---|---|
| **Claude Code** (`pkg/agent/claude/claude.go`) | Native tools (`Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`, `WebFetch`, `Task`) are disabled via `--tools ""` (`claude.go:391`). `--allowedTools` lists only `mcp__grain-sandbox__<tool>` for the control set + `HostToolGrant` + model-only capability tools. | `mcp__grain-sandbox__{run_command,read_file,write_file,edit_file,ask_question,request_secret,comment_on_issue,nothing_needed,wait_for_task,resume_later,propose_task,recreate_sandbox}` (+ `chat` if interactive). | Uses `mcp__grain-sandbox__{run_command,read_file,write_file,edit_file}` which execute inside the sandbox (Granule VM, K8s pod, Docker container, or host root). Inside `run_command`, the agent runs `grain <subcommand>`. |
| **Codex** (`pkg/agent/codex/codex.go`) | Codex runs with its native shell disabled (`--disable-shell-tool` or sandbox policy) and `mcp_servers.grain-sandbox` configured in `config.toml` with `enabled_tools` restricted to the control set (`codex.go`). | Same 11–12 control tools under `grain-sandbox`. | Uses `grain-sandbox` MCP sandbox tools (`run_command`, `read_file`, `write_file`, `edit_file`) and invokes `grain <subcommand>` inside `run_command`. |
| **Antigravity / Jetski** (`pkg/agent/antigravity/antigravity.go`) | `agy` always injects its own 57 built-in tool declarations into the model prompt; Grain cannot strip them via a CLI flag today, so Grain's `PreToolUse` hook (`HookDecision` in `antigravity.go:635`) blocks every non-allowed tool call and tells the model to use `mcp_grain-sandbox_*`. | Same 11–12 control tools registered eagerly as `mcp_grain-sandbox_<tool>`. | Uses `mcp_grain-sandbox_{run_command,read_file,write_file,edit_file}` (plus the host's search tools when an edition's research capability is granted) and invokes `grain <subcommand>` inside `mcp_grain-sandbox_run_command`. |

*(Note on native shell/file tools: As documented in `docs/design/cli-run-location.md`, all three agent CLIs run on the **host/executor** while the workspace lives inside a **Granule VM, Kubernetes pod, or Docker container**. Therefore, the 4 MCP sandbox tools (`run_command`, `read_file`, `write_file`, `edit_file`) remain the bridge that crosses from the host-side agent CLI into the sandbox guest; if a future deployment runs the agent CLI inside the sandbox guest itself, the framework's native `Bash`/`Read`/`Write`/`Edit` map 1:1 to those 4 tools with zero change to the `grain` CLI or control set.)*

### How the Antigravity Failures (`exit_plan_mode`, `schedule`) Go Away

We traced the exact mechanism behind the Antigravity 100% failure rates on `exit_plan_mode` (25/25 failed calls) and `schedule`:

1. **Why `exit_plan_mode` and `schedule` are called:**
   - Jetski (`agy`) injects a `<model_router_plan_protocol>` block into its system prompt instructing the model: *"Every user message starts a planning turn... Then call `exit_plan_mode` with the complete plan."* It also injects a `<messaging>` block advertising `schedule` for timers.
   - When `agy` starts a run with 35–100+ `mcp_grain-sandbox_*` tools alongside its 57 built-in tools (~95–150 total tools), the model frequently falls back to its built-in `<model_router_plan_protocol>` training and calls `exit_plan_mode` at the end of its read-only investigation, or calls `schedule` when waiting on CI.
   - When the model calls `exit_plan_mode` or `schedule`, Grain's `PreToolUse` hook (`HookDecision` in `pkg/agent/antigravity/antigravity.go:665`) intercepts the call, sees that `"exit_plan_mode"` is not in `allowedTools`, and returns:
     ```json
     {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "exit_plan_mode is a built-in tool that runs on the host machine, not in your sandbox..."}}
     ```
   - `agy` records the denied call as an error in the transcript (`is_error: true`). Worse, saying *"exit_plan_mode is a built-in tool that runs on the host machine, not in your sandbox"* confuses the model because `exit_plan_mode` is a *router state-transition pseudo-tool*, not a host filesystem tool!
2. **Three-part fix in `pkg/agent/antigravity/antigravity.go` + Sandbox-First Surface:**
   - **Fix 1 — Pass plan-mode disablement in `agy` settings (`antigravity.go:mcpConfigJSON`):** Set `"modelRouterPlanProtocol": false` / `"planMode": false` in the per-session `.gemini/settings.json` written by `antigravity.go` so `agy` omits `<model_router_plan_protocol>` when supported by the installed `agy` version, and add one explicit line to `sandboxToolDirective` (`antigravity.go:246`):
     `"Never call exit_plan_mode, enter_plan_mode, or schedule: plan-mode routing and timers are disabled in headless grain runs — execute your plan directly in this turn with mcp_grain-sandbox_* tools."`
   - **Fix 2 — Handle `exit_plan_mode` / `enter_plan_mode` in `HookDecision` (`antigravity.go:635`):** If a model still calls `exit_plan_mode` despite the directive, `exit_plan_mode` is a harmless no-op state handoff inside `agy` (or if denied, the denial message should specifically say `"Plan mode is already off in headless runs; execute the steps directly using mcp_grain-sandbox_run_command, mcp_grain-sandbox_edit_file, and mcp_grain-sandbox_write_file without calling exit_plan_mode"`). Even better: allowing `exit_plan_mode` in `HookDecision` if `agy` is actually in plan-mode (where `agy` refuses `edit_file` until `exit_plan_mode` is called!) or disabling plan mode in `settings.json` prevents `agy` from trapping the model in read-only plan mode. Specifically, inspect the `<model_router_plan_protocol>` prompt injected by `agy`: *"Editing tools are refused while plan mode is on... Call `exit_plan_mode` once the plan is complete."* Because `agy` itself starts in plan mode and **Wait**: why did `exit_plan_mode` fail in `PreToolUse`? Because `HookDecision` denied `exit_plan_mode`! And for `schedule`, the hook denial message explicitly directs the model to `grain ci wait` / `resume_later` instead of `schedule`.
   - **Fix 3 — Shrink the MCP tool list from 35–118 tools to 11 tools:** Removing 25–100+ MCP schemas cuts the total tool declarations in `agy`'s prompt by more than half and moves CI waiting to `grain ci wait` inside `mcp_grain-sandbox_run_command`, eliminating the trigger that caused models to reach for `schedule`.

---

## 6. Sessions and No-Sandbox Runs

### Current Invariant: Every Run Has a Sandbox

In Grain today (`pkg/orchestrator/run.go`, `pkg/orchestrator/console.go`), **every dispatched task attempt and every standing console session (Director, Pipeline Steward, repo chat) provisions a sandbox** before the agent CLI starts:
- `granule` (local Firecracker VM via `granule`)
- `kubernetes-granule` (Granule VM inside a Kubernetes pod)
- `docker` (local Docker container)
- `host` (confined directory on the controller host)

In all four backends, `127.0.0.1:9728` (or the dynamic proxy port in `/home/debian/.git-credentials` / `GRAIN_SANDBOX_PROXY`) is reachable from `run_command` and `/usr/local/bin/grain` (or `$PATH` on `host` backend) is available.

### What Happens If a Run or Session Has No Sandbox

A run can execute without a sandbox in three situations:
1. A future lightweight standing chat or triage session configured with `no_sandbox: true` (or `sandbox_backend: none`) to avoid VM startup latency.
2. Unit/integration tests in `pkg/mcp` or `pkg/daemon` that invoke `grain mcpserver` without `-sandbox-root`, `-granule-vm`, `-container`, or `-k8s-pod`.
3. A framework or external MCP client that cannot shell out (`run_command` unavailable).

**Contract for No-Sandbox Runs:**
1. **Detection in `grain mcpserver` (`pkg/daemon/mcpserver.go`):**
   `hasSandbox := *root != "" || *granuleVM != "" || *container != "" || *k8sPod != ""`
2. **Automatic Fallback to MCP Wrappers (or `-mcp-full-roster` flag):**
   - When `hasSandbox` is `true` (the default for all production runs), `grain mcpserver` registers **only** the 4 sandbox tools + the model-only control set (plus any model-only dangerous capability tools).
   - When `hasSandbox` is `false` (or when `-mcp-surface=full` is explicitly passed for a framework that cannot shell out), `grain mcpserver` registers the **thin MCP wrapper tools over the same namespace and daemon handlers** (`grain_ls`, `grain_read`, `grain_grep`, CI tools, granted capability tools, and `api_*` tools), and `BuildPrompt` omits the `run_command`-based `grain` CLI instructions in favor of the MCP tool names.
   - Because the thin MCP wrappers call the exact same `namespace.Server` and `ui.HTTPClient` methods as the proxy's `/fs/`, `/cli/`, and `/api/` routes, keeping this fallback path requires **zero duplicate business logic**—only the registration switch in `ServeMCPServerPinned`.

---

## 7. Capabilities

### What Stays Unchanged

Per `docs/capabilities-spec.md`, the **grant and pass lifecycle is completely unchanged**:
1. **Declaration & Policy:** Capability servers and built-in capabilities are declared in the deployment config; tasks request them in `capabilities:`; `capability_policy` evaluates whether a task or proposer may hold them (`pkg/capability`).
2. **Minting at Admission (`pkg/orchestrator/capability_pass.go`):** Before an attempt starts, `mintCapabilityPass` calls each capability server's `mint_<method>` endpoint, producing a `capabilityPass` linked to `attemptID`.
3. **Revocation at Attempt End (`pkg/orchestrator/capability_pass.go`):** When the attempt finishes (for any reason: completion, failure, cancellation, park, or timeout), `releaseCapabilityPass` calls `release_<method>` for every minted method and revokes the per-attempt sandbox token.

### How Each of the Five Materialized Primitives Is Delivered

Per `docs/capabilities-spec.md` §3, a minted pass can return five primitives. Under the sandbox-first design, their delivery is:

| Primitive | Current Delivery | Sandbox-First Delivery |
|---|---|---|
| **1. `placements`** (files written into sandbox) | Written into the sandbox filesystem before the agent starts (`capability_materialize.go`). | **Unchanged** (already sandbox-first). |
| **2. `ports`** (forwarded TCP ports) | Forwarded into the sandbox guest (`capability_materialize.go`). | **Unchanged** (already sandbox-first). |
| **3. `setup` / `setup_root`** (init scripts in sandbox) | Executed inside the sandbox before the agent starts (`capability_materialize.go`). | **Unchanged** (already sandbox-first). |
| **4. `prompt_section`** (markdown added to prompt) | Appended to the run's prompt (`capabilityPassPromptSection`). | **Primary discovery mechanism:** tells the agent which `grain` CLI subcommands or `grain cap <server> <tool>` commands its grant unlocked, replacing MCP tool schemas. |
| **5. `mcp_endpoint`** (`/runs/<attempt_id>/mcp`) | Wired into the host-side agent CLI's MCP config (`RunConfig.CapabilityMCP`), exposing every tool as a model MCP tool. (Meanwhile, CI jobs in `capability_ci.go` write `/home/debian/.grain/mcp.json` inside the sandbox.) | **Routed through `grain cap <server> <tool>` over the sandbox proxy (`http://127.0.0.1:9728/cap/<server>/mcp`)** for sandbox-allowed tools, and kept on the host-side MCP config **only** for tools marked `model_only: true`. |

### Grain's Own Tool-Serving Capabilities Become Sandbox Credential Scopes

Grain's built-in capabilities that previously existed only to add MCP tools in `pkg/daemon/mcpserver.go` (`GrantedDaemonTools`) now map directly to scopes on the per-attempt sandbox credential (or stay model-only when dangerous):

| Built-in Capability | Classification | Sandbox CLI Surface Unlocked (`http://127.0.0.1:9728`) | Model-Only Control Tools |
|---|---|---|---|
| `grain-ci` | Safe (own-repo) | `grain ci {wait,run-job,retrigger,list,log,test-history,artifact}`, `grain ls/read/grep ci://<task-repo>/...` | None (`resume_later` is already in base control set) |
| `self-debug` | Safe (read-only) | Widens `/fs/ci://`, `/fs/src://`, `/fs/task://`, `/fs/config://`, `/fs/deploy://{metrics,pipeline-changes}`; unlocks `grain metrics`, `grain task {list,read,prompt,transcript}`, `grain pipeline-changes` | None |
| `grain-log` | Safe (redacted read) | `grain read deploy://grain.log`, `grain log [--since ...] [--grep ...]` (redacted by `grainlog.Redactor` on controller before proxy sends bytes) | None |
| `deployment-health` | Safe (redacted read) | `grain read deploy://health`, `grain read deploy://state`, `grain health` | None |
| `bootstrap-playbooks` | Safe (read-only) | `grain ls/read src://playbooks/...`, `grain playbooks` | None |
| `read-issues` | Safe (read-only) | `grain issue list`, `grain issue read <number>` | None |
| `memory` | Safe (attributed write) | `grain memory {list,read,write,doc-write}` (attributed to `agent:<taskID>`) | None |
| `release-notes` | Safe (attributed write) | `grain release-notes {stats,publish}` (attributed to `agent:<taskID>`) | None |
| `quarantine-test` | Safe (workspace + proposal) | `grain quarantine <pkg> <test> --reason <why>` | None |
| `db-read` | Safe (read-only SQL) | `grain db query "<sql>"` | None |
| `api:<non-dangerous>` | Safe (scoped API) | `grain api <METHOD> <path>` for routes in granted non-dangerous scopes (`tasks:read/write`, `merge:write`, `ci:read/write`, `notifications:write`, `schedules:*`, `templates:*`, `suites:*`, `releases:*`, `boards:*`, `memory:*`, `caches:*`, `repos:read`, `sessions:read`, `settings:read`, `connections:read`, `capabilities:read`, `secrets:read`, `deployment:read`) | None |
| `api:<dangerous>` (`tasks:admin`, `merge:force`, `settings:write`, `connections:write`, `secrets:write`, `capabilities:admin`, `deployment:upgrade/restart/reboot`) | **Dangerous** | **None** (never on sandbox credential) | `api_<route>` (or `api_admin`) on host MCP server |
| `dispatch-control` / `pipeline-control` (`api:dispatch:write`) | **Dangerous (queue control)** | Read-only pause status via `grain merge-queue` / `grain api GET /api/pipeline/pause` | `pause_pipeline`, `resume_pipeline`, `run_task_now`, `defer_task` on host MCP server |
| `self-upgrade` | **Dangerous** | `grain read deploy://upgrade` (status read only) | `upgrade_deployment`, `cancel_deployment_upgrade` on host MCP server |
| `self-repair` | **Dangerous (host exec)** | **None** | `run_host_command` on host MCP server |
| `db-write` | **Dangerous (DB mutation)** | `grain db query` (read-only) | `exec_db` on host MCP server |
| `team-broadcast` | **Escalation (cross-task write)** | **None** | `broadcast_team_message` on host MCP server |

### External Capability Servers: `mcp_endpoint`, `grain cap`, and `model_only`

#### 1. How `grain cap` Reaches `mcp_endpoint`
When a minted capability pass includes an `mcp_endpoint` (`/runs/<attempt_id>/mcp` on a capability server), the daemon registers that endpoint under the sandbox proxy at:
```
POST http://127.0.0.1:9728/cap/<server>/mcp
```
Inside the sandbox:
- `grain cap list` queries `GET http://127.0.0.1:9728/cap/` to list all capability servers granted to this attempt and their sandbox-callable tools (with descriptions and flags generated from each tool's JSON `inputSchema`).
- `grain cap <server> <tool> [--flag value ... | --json '{"k":"v"}']` sends a JSON-RPC `tools/call` request to `http://127.0.0.1:9728/cap/<server>/mcp` authenticated by the sandbox token, prints the tool result text to stdout (uncapped!), and exits `0` (or `1` if `isError: true`).
- **Unification with CI Jobs (`pkg/orchestrator/capability_ci.go`):** Today, CI jobs with `capabilities:` already expose `mcp_endpoint` inside the sandbox via `/home/debian/.grain/mcp.json` forwarded through the host listener (`capability_ci.go:108`). Under this design, both task attempts and CI jobs point `/home/debian/.grain/mcp.json` at `http://127.0.0.1:9728/cap/<server>/mcp` (authenticated by the sandbox proxy token) and can invoke tools via `grain cap <server> <tool>`.
- **Privilege Constraint on CI Jobs:** Because CI jobs have no LLM model channel at all (they are shell steps in `.grain/workflows/*.yml`), a CI job can **only** invoke capability tools where `model_only == false`. Any tool marked `model_only: true` is refused on `/cap/<server>/mcp` for both task sandboxes and CI sandboxes.

#### 2. Declaring Tools `model_only` vs. Sandbox-Callable (Default: `model_only: true` for External Servers)

External capability servers need an explicit, backwards-compatible way to declare whether a tool on their `mcp_endpoint` may be called from the sandbox CLI (`model_only: false`) or must remain wired exclusively to the model's host-side MCP channel (`model_only: true`).

We specify **two levels** where `model_only` can be declared, with **external capability servers defaulting to `model_only: true` (fail-closed) until they opt in**:

1. **Per-Tool in `tools/list` (`annotations.modelOnly` / `x-grain-model-only`):**
   In the standard MCP `tools/list` response from `/runs/<attempt_id>/mcp`, each tool object may carry:
   ```json
   {
     "name": "query_logs",
     "description": "Query Cloud Logging for this project",
     "inputSchema": { "type": "object", "properties": { "filter": { "type": "string" } } },
     "annotations": {
       "readOnlyHint": true,
       "modelOnly": false
     }
   }
   ```
   (Grain also accepts top-level `"x-grain-model-only": false` on the tool object for servers that do not populate MCP `annotations`.)
2. **Per-Method in the Capability Server Manifest (`GET /capabilities`):**
   In the capability server's declaration (`docs/capabilities-spec.md` §2), a method definition may declare a default for all tools served by its `mcp_endpoint`:
   ```json
   {
     "name": "cloud-logs",
     "description": "Read-only Cloud Logging access",
     "mcp_delivery": "sandbox"
   }
   ```
   where `mcp_delivery` is `"model_only"` (the default for all external capability servers) or `"sandbox"` (all tools on this method's `mcp_endpoint` are exposed via `grain cap <server> <tool>` unless an individual tool sets `annotations.modelOnly: true`).
3. **Precedence and Default Rule:**
   - **Rule 1:** If a tool's `tools/list` entry explicitly sets `annotations.modelOnly` (or `x-grain-model-only`), that boolean wins.
   - **Rule 2:** Otherwise, if the capability method sets `mcp_delivery: "sandbox"`, the tool is sandbox-callable (`model_only = false`).
   - **Rule 3 (Default for External Servers):** Otherwise, **`model_only` defaults to `true`**. Existing external capability servers (such as `gchat` or custom production servers) continue to have their tools wired as model-only MCP tools on the host (`RunConfig.CapabilityMCP`) and **cannot** be called from `/cap/<server>/mcp` inside the sandbox until the server or method explicitly opts into `"mcp_delivery": "sandbox"` or `"annotations": {"modelOnly": false}`.
   - **Enforcement at Two Layers:**
     - *Host MCP wiring (`pkg/orchestrator/capability_endpoints.go`):* When `probeCapabilityMCPTools` calls `tools/list` on `/runs/<attempt_id>/mcp` at attempt start, it partitions the returned tools into `ModelOnlyTools` (added to the host agent CLI's MCP config and `--allowedTools`) and `SandboxCLITools` (registered on the proxy's `/cap/<server>/mcp` allowlist and summarized in `prompt_section`).
     - *Proxy enforcement (`pkg/gitproxy`):* When `POST /cap/<server>/mcp` receives a `tools/call` request from inside the sandbox, it checks that the target tool name is in `SandboxCLITools` (`model_only == false`). Any attempt by a sandbox process to call a `model_only: true` tool over `/cap/<server>/mcp` is rejected with HTTP 403 before reaching the capability server.

#### 3. Per-Attempt Scoping and Revocation
- The sandbox proxy authenticates every `/cap/<server>/mcp` and `/api/` call using the per-attempt sandbox token (`gpx_...`), which maps to the attempt's `capabilityPass` (`attemptID`).
- When the attempt ends, `releaseCapabilityPass` revokes the pass on the capability server (`POST /release_<method>`) **and** `p.Tokens.Revoke(sandbox)` revokes the sandbox token on the proxy. A leaked sandbox token or `/home/debian/.grain/mcp.json` URL is immediately dead when the attempt ends.

#### 4. How Capability Authors Test It
Capability authors can test both delivery paths using:
1. **`POST /api/capability-servers/test` (and the Settings → Capability Servers UI "Test" button / `grain api POST /api/capability-servers/test`):** Extended to report each discovered tool's delivery mode (`delivery: "sandbox (grain cap)"` vs. `delivery: "model_only (MCP)"`) alongside its `tools/list` schema validation.
2. **Inside any sandbox (or local test harness):**
   - `grain cap list` — verifies which tools of the server are visible to the sandbox CLI vs. held back as `model_only`.
   - `grain cap <server> <tool> --help` — verifies flag generation from `inputSchema`.
   - `grain cap <server> <tool> --json '{...}'` — exercises the full `grain` → `127.0.0.1:9728/cap/<server>/mcp` → `/runs/<attempt_id>/mcp` round-trip.

---

## 8. Migration Plan

We stage the migration across **five sequential phases** so that every phase is independently testable, observability never regresses, and prompts/templates are updated in lockstep with the roster cutover.

### Stage 1: Namespace Stage 2 Roots & `grain` CLI Read Commands
- **Scope:**
  - Complete the Stage 2 virtual namespace roots from `docs/design/grain-namespace.md` (`task://`, `config://`, `memory://`, `deploy://`) on `pkg/namespace` and `pkg/gitproxy/namespace.go`.
  - Add in-sandbox `grain` CLI read commands talking to `http://127.0.0.1:9728/fs/` and `/cli/`:
    `grain ls`, `grain read`, `grain grep`, `grain ci list`, `grain ci log`, `grain ci test-history`, `grain ci artifact`, `grain pr status`, `grain pr base-status`, `grain merge-queue`, `grain task {list,read,prompt,transcript}`, `grain metrics`, `grain health`, `grain log`, `grain playbooks`, `grain issue {list,read}`, `grain db query`, and `grain help`.
  - Add server-side activity reporting (`pkg/gitproxy/activity.go`) and `task_run_tool` telemetry recording on the proxy for all read routes.
- **Daemon wiring (`pkg/daemon/sandbox_namespace.go`):** each root and `/cli/` read is answered by the backend of the MCP tool it replaces, asked through grain's own API handler in process (`LiveAPI`, as the CI API listener does): `task://` by `read_grain_task*`'s renderers (`mcp.NamespaceTasks`, attempts read lazily), `deploy://merge-queue`/`metrics`/`pipeline-changes` by `query_merge_queue`/`read_grain_metrics`/`read_pipeline_changes`, `deploy://health`/`state` by `pkg/capability/deploymenthealth`, `deploy://grain.log` by `pkg/capability/grainlog` with its redactor, `memory://` by the store's memory rows and documents, `/cli/issues` by `list_issues`/`read_issue` with the run's own git credential, and `/cli/db/query` by `query_grain_db`, read-only even for a task holding `db-write`.
- **`config://` trust boundary:** the state repository is served only where `staterepo.BrowsablePath` allows -- `README.md`, `schema-version`, and `memory/`, `release-notes/`, `scripts/`, `settings/`, `tables/`, `workflows/` -- the same allowlist the UI's configuration browser uses. `.git` (whose objects keep everything ever committed), `secrets.enc`, `secrets/` and anything else a deployment keeps beside them are refused, symlinks are neither listed nor followed outside that allowlist, and every byte still passes the namespace's redactor. `config://repos/<owner>/<repo>` is the repository's configuration row, with any credential in its clone URL removed.

### Stage 2: `grain` CLI CI & PR Action Commands with Server-Side Wait Credit
- **Scope:**
  - Add `/cli/ci/{wait,run-job,retrigger}` and `/cli/pr/{open,reply,resolve,review-comment}` on the sandbox proxy (`pkg/gitproxy`).
  - Implement resumable 9-minute long-polling (`grain ci wait [--jobs ...] [--resume <run-id>]` and `grain ci run-job <job>`) with server-side `RunWaitCredits.Credit` (`pkg/orchestrator/waitcredit.go`), `IsDeclaredWait: true` activity stamping, and direct `task_run_check_wait` telemetry recording.
  - Implement `grain git revert` inside the sandbox workspace backed by `/cli/tasks/{id}/revert-info` (making task/merge reverts work on Granule VMs and containers as well as host sandboxes).
- **Implemented (Task 1437):**
  - Routes in `pkg/gitproxy/cli_actions.go`, acting through `ui.SandboxCLIActions` (`pkg/ui/sandbox_cli_actions.go`). The daemon wires them in `pkg/daemon/sandbox_cli.go`. `pr reply` and `pr resolve` run pkg/mcp's own review-thread tools, scoped to the task's branch and pull request.
  - Wait credit is kept per source (`RunWaitCredits.CreditSource`, `WaitSourceCLI`). CLI-blocked time therefore adds to MCP-blocked time under the one 45m cap, rather than the larger of two cumulative totals winning.
  - Every poll slice records its own `task_run_check_wait` row. A slice that ends with CI still running is recorded as `timed_out`. The rows are written as they happen and merged into the end-of-run telemetry write.
  - `grain pr review-comment` findings are kept per run (`model.Store.RecordProxyToolCallArgs`) and relayed with `add_review_comment` calls when the run finishes.

### Stage 3: Non-Dangerous Task & Agent-API Actions on the Sandbox CLI
- **Scope:**
  - Add `/cli/status`, `/cli/handover`, `/cli/notify`, `/cli/tasks/{id}/notes`, `/cli/tasks/wait` (blocking mode with server-side wait credit), and `/cli/proposals` (enforcing `hold_for_approval: true`, `auto_merge: false`, no `supersedes` on the sandbox credential).
  - Expose `/api/` on `http://127.0.0.1:9728/api/` for non-dangerous `api:<scope>` grants, memory writes (`grain memory`), release-notes (`grain release-notes`), and test quarantine (`grain quarantine`), enforced by `agentAPIGate` (`X-Grain-Channel: sandbox-cli` blocking all `Dangerous: true` and model-only scopes) and audited via `recordAgentCall`.
  - Compact `propose_task`'s MCP schema description (`pkg/mcp/mock_tools.go`) from 14.7 KB to ~1.8 KB, moving detailed prose to `grain help propose`.

### Stage 4: Capability `mcp_endpoint` CLI Delivery (`grain cap`) & `model_only` Metadata
- **Scope:**
  - Add `/cap/` and `/cap/<server>/mcp` to `pkg/gitproxy`, backed by the attempt's `capabilityPass`.
  - Implement `grain cap list` and `grain cap <server> <tool> [flags|--json]`.
  - Update `probeCapabilityMCPTools` (`pkg/orchestrator/capability_endpoints.go`) to parse `annotations.modelOnly` / `x-grain-model-only` and method-level `mcp_delivery`, defaulting external capability servers to `model_only: true` and routing `model_only: false` tools through `grain cap` + `prompt_section`.
  - Unify CI capability endpoints (`pkg/orchestrator/capability_ci.go`) onto the same `/cap/<server>/mcp` proxy path and `model_only` filter, and surface tool delivery modes in `POST /api/capability-servers/test`.
- **Implemented (Task 1439):**
  - `pkg/capability/protocol` reads `annotations.modelOnly` / `x-grain-model-only` on attempt tools and `annotations.mcpDelivery` / `x-grain-mcp-delivery` on `mint_<method>` (any other value is a discovery error), and carries the method's delivery on each `Grant`. `protocol.ResolveModelOnly` is the precedence rule.
  - `probeCapabilityMCPTools` (`pkg/orchestrator/capability_endpoints.go`) splits each endpoint into `ModelOnlyTools`, which are the only tools wired to the agent CLI, and `SandboxCLITools`. The sandbox tools are registered in a `gitproxy.CapRegistry` under the run ID for as long as the pass is materialized. A `<capability-cli>` block after the attributed prompt sections lists them as `grain cap <server> <tool>` commands. It gives names only: a tool's description is the server's untrusted text.
  - `GET /cap/`, `GET /cap/<server>` and `/cap/<server>/mcp` (`pkg/gitproxy/capserver.go`) answer from that registry. Every `tools/call` in a request body, batches included, is checked first: a model-only tool, or one the server never listed, gets 403 and never reaches the server. Calls are counted like the `/cli/` routes, through `recordRouteCall` into `task_run_tool` and `task_run_api_call` as `cap__<server>__<tool>`; they do not go through pkg/ui's `recordAgentCall`.
  - CI jobs are registered under their sandbox name, because they have no task run. Their `mcp.json` points at `<proxy>/cap/<server>/mcp` with the job sandbox's own proxy token in an `Authorization` header, and lists only sandbox tools. The capability port relay is kept only as a fallback for a deployment that wires no registry.
  - `POST /api/capability-servers/test` reports each method's resolved `delivery` (and `deliveryDeclared`). The control-plane probe never mints, so it cannot see per-attempt tools; their per-tool resolution shows in `grain cap list` inside a sandbox.

### Stage 5: Shrink Default MCP Roster to Control Set & Update Prompts/Templates
- **Scope:**
  - Switch `grain mcpserver` (`pkg/daemon/mcpserver.go`) so runs with a sandbox register **only** the 4 sandbox tools + the model-only control set (`ask_question`, `request_secret`, `comment_on_issue`, `nothing_needed`, `wait_for_task`, `resume_later`, `propose_task`, `recreate_sandbox`, plus `chat` when interactive, and any granted model-only dangerous tools). Keep `-mcp-surface=full` as the automatic fallback when `hasSandbox == false`.
  - Add the Antigravity plan-mode/timer fixes in `pkg/agent/antigravity/antigravity.go` and split `PublishedToolNames()` in `pkg/agent/toolroster.go` / `pkg/metrics/tools.go`.
  - Add the compact `### Grain CLI (grain)` section to `BuildPrompt` (`pkg/orchestrator/run.go`) and update all built-in prompts and system templates that name migrated MCP tools:
    1. **Base coding & red-CI prompts (`pkg/orchestrator/run.go`, `pkg/orchestrator/sync.go`):** Update `redCIProcedureSection` and PR/CI instructions (`wait_for_checks` → `grain ci wait`, `get_ci_log` → `grain ci log`, `list_ci_runs` → `grain ci list`, `query_test_history` → `grain ci test-history`, `run_ci_job` → `grain ci run-job`, `pull_request_status` → `grain pr status`, `base_branch_status` → `grain pr base-status`).
    2. **Pipeline Steward (`pkg/model/pipeline_steward_template.go`):** Update wake procedure to use `grain pipeline-changes` / `grain api`, `grain task read`, `grain merge-queue`, `grain ci list/log`, `grain health`, and `grain log` while keeping `resume_later`, `propose_task`, and `dispatch-control` tools as model tool calls.
    3. **Memory Agent (`pkg/model/system_template.go` — `memoryAgentTemplate`):** Update to use `grain task list/read/transcript`, `grain metrics`, and `grain memory`.
    4. **Infra / Workflow Review (`pkg/model/system_template.go` — `infraReviewTemplate` & `pkg/model/workflow_review_template.go`):** Update to use `grain ci list`, `grain ci log`, `grain metrics`, and `grain ls/read ci://...`.
    5. **Release Notes & Release Triage (`pkg/model/system_template.go` — `releaseNotesTemplate` & `pkg/orchestrator/releases.go`):** Update to use `grain release-notes`, `grain ci list/log`, and `grain api`.
    6. **Base Branch Repair (`pkg/orchestrator/basebranchci.go` & `pkg/model/basebranchrepair.go`):** Update to use `grain ci list/log/wait`, `grain pr base-status`, and `grain git revert`.
    7. **Console Sessions / Director (`pkg/model/console_session.go`):** Update standing session prompt to reference `grain` CLI commands alongside `chat` and `propose_task`.
