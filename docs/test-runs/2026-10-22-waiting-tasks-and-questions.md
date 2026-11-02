# 2026-10-22 Test Plan §4 Walkthrough: Waiting Tasks, `PendingQuestion` Callout, `/inbox` Pre-Fetch, `wait_for_task` Parked UI, and Quota/Budget Contention

- **Date:** 2026-10-23 (UTC)
- **Task:** `grain/049f1e09/task-215`, base `candidate`
- **Commit tested:** `374622764` (`origin/candidate` tip at the start of the run), covering commits `95073d9ec`, `fb6d2bad7`, `34166a920`, `9060e0a2f`, `44048b35f`, `b0a21c2bb`, `2fc026c20`, `f0ace8dd3` / `6ffed3f5a`, `e9fad2b64` / `438cb660e`, `daf546150` / `f14fad6fa`, and `d21320e92`.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.9`, Node.js `v20.19.0`, npm `10.8.2`
  - Playwright `1.62.1`, headless Chromium (`chromium-1234`), tested at both desktop (`1280x800`) and phone (`390x844`) viewports
  - Server: real a wrapper distribution's `cmd/grain` built with `-tags grain_e2e` (`orchestrator.NewHostSandboxes`) and started as `grain daemon -data-dir <dir> -sandbox-dir <dir> -ui-addr 127.0.0.1:0 -sandbox-port-base=0 -poll-interval 100ms -max-workers 2 -max-mergers 0` serving the production Vite bundle (`make frontend`, embedded from `pkg/ui/static`) over a real on-disk SQLite store.

## Summary of Results

| § | Area | Result | Automated Coverage |
|---|------|--------|--------------------|
| 4.1 | `wait_for_task` parked UI (`"Waiting on: #X"` vs. `"Blocked on: #X"`), validation refusals, and resume prompt injection | **PASS** | `ui/e2e/waiting-tasks-and-questions-walkthrough.spec.js`, `tests/e2e/wait_for_task_test.go`, `pkg/orchestrator/wait_for_task_test.go`, `pkg/ui/wait_task_test.go` |
| 4.2 | `/inbox` pre-fetched `.inbox-question` markdown (`task <N>` links, `pendingSecret` write-only notice) & `DetailOverlay` `PendingQuestion` callout (draft survival across poll refresh, inline `Send`) | **PASS** | `ui/e2e/waiting-tasks-and-questions-walkthrough.spec.js`, `ui/e2e/inbox.spec.js`, `ui/src/components/InboxPage.test.jsx`, `ui/src/components/DetailOverlay.test.jsx` |
| 4.3 | Queued task waiting status (`QuotaWaitCapability`) across `/` (`TaskList`), `/boards/<id>` (`TaskBoard`), `/tasks/<id>` (`DetailOverlay`), and `grain` CLI | **PASS after fix (B1)** | `ui/e2e/waiting-tasks-and-questions-walkthrough.spec.js`, `pkg/cli/defer_until_test.go` (`TestTaskBlockShowsActivityAndWaitReason`) |
| 4.4 | Shared sandbox budget contention holding CI job admissions (`"waiting behind a queued agent sandbox"`) & `wait_for_checks` `Stuck` deadlock escape hatch | **PASS** | `pkg/cli/ci_capacity_test.go` (`TestSharedSandboxBudgetContentionHoldsCIJobsAndAdmitsStuckWaitForChecksJob`), `pkg/ci/gate_test.go`, `pkg/dispatch/cycle_test.go` |

---

## Detailed Walkthrough & Verification

### 1. `wait_for_task` Parked UI (`"Waiting on: #X"` vs. `"Blocked on: #X"`) and Resume (`95073d9ec`, `fb6d2bad7`, `34166a920`, `9060e0a2f`, `44048b35f`)

1. **Setup:** Started `grain daemon` with `-max-workers 2` and filed two `noRepo: true, approved: true` tasks:
   - Task A (`#2`, *"Deploy downstream service after schema migration"*)
   - Task B (`#3`, *"Run production schema migration"*)
   Both tasks were dispatched into local e2e sandboxes (`2-1` and `3-1`).
2. **Validation refusals on `POST /api/tasks/2/wait-on`:**
   - **Self-wait (`ids: ["2"]`):** Refused with HTTP `400 Bad Request` (`"a task cannot depend on itself"`).
   - **Non-existent target (`ids: ["99999"]`):** Refused with HTTP `400 Bad Request` (`"depends on unknown task 99999"`).
   - **Standing console session (`ids: ["1"]`, the daemon's standing director session):** Refused with HTTP `400 Bad Request` (`"task 1 never completes -- it is a standing chat session, and a task waiting on it would never be dispatched"`).
   - **Dependency cycle (`ids: ["4"]`, where `#4` already depends on `#2`):** Refused with HTTP `400 Bad Request` (`"task 4 already waits on task 2 (4 → 2), so task 2 depending on it would be a loop neither could leave"`).
3. **Parking Task A on Task B (`park: true`):**
   - Called `POST /api/tasks/2/wait-on` with `{"ids": ["3"], "park": true}` and emitted the `mcp__grain-sandbox__wait_for_task` tool call in run `2-1`'s stream.
   - `GET /api/tasks/2` transitioned to `state: "queued"`, `blocked: true`, `parked: true`, `dependsOn: ["3"]`, `blockedBy: ["3"]`, with `attempts[0].outcome === "parked"` and `attempts[0].detail === "parked: waiting on task(s) 3"`.
4. **UI verification at desktop (`1280x800`) and phone (`390x844`) viewports:**
   - On `/` (`TaskList`), Task A's row rendered the `"Blocked"` chip (`color="error"`) with `title="Blocked on: #3 (running)"` (because `Task` on `GET /api/tasks` omits `parked`, keeping list rows uniform).
   - On `/tasks/2` (`DetailOverlay`), the `"Blocked"` chip inside `.detail-state` rendered with `title="Waiting on: #3 (running)"` (`parked: true` on `TaskDetail`), and the timeline recorded the automation comment:
     `"grain: this task's run parked it, waiting on #3 (Run production schema migration). It goes back in the queue once every one of them has closed; closing one, or removing it from this task's dependencies, releases the wait the same way."`
5. **Target closure & resume prompt injection:**
   - Finished Task B (`3-1`) via `nothing_needed` (`reason: "Schema migration v42 applied cleanly."`).
   - Task B transitioned to `closed`, automatically releasing Task A's dependency and dispatching Attempt 2 (`2-2`).
   - Inspected the prompt delivered to `agy` on `stdin` for run `2-2` (`agent-stdin`) and verified `orchestrator.BuildPrompt` injected the `ResumedFrom` section:
     - `"This run resumes from a park: the previous attempt called wait_for_task, parked this task on the task(s) below, and ended."`
     - `"Task 3 \"Run production schema migration\": closed, outcome completed (closed, nothing of it merged)."`
     - `"Its closing note:\n> Schema migration v42 applied cleanly."`

---

### 2. `/inbox` Pre-Fetched Question & `PendingQuestion` Callout in `DetailOverlay` (`b0a21c2bb`, `2fc026c20`)

1. **Setup:** Dispatched two tasks:
   - Task C (`#5`, *"Clarify staging rollout target"*), whose run called `ask_question` with markdown containing bold text (`**config.yaml**`), inline code (`` `acme/widgets` ``), a two-item bullet list, and a task reference (`task 3`).
   - Task D (`#6`, *"Configure webhook signing secret"*), whose run called `request_secret` (`secret: "stripe-api-key"`, `reason: "Needed to verify webhook signatures in staging."`).
   - Both tasks transitioned to `awaiting_reply`, with `GET /api/tasks/6` reporting `pendingSecret.name === "stripe-api-key"`.
2. **`/inbox` verification at `1280x800` and `390x844`:**
   - Under the `"Answer a question"` group on `/inbox`, Task C's `.inbox-item` row pre-fetched `GET /api/tasks/5` on mount via `Responder` and immediately rendered `.inbox-question` markdown (`<strong>config.yaml</strong>`, `<code>acme/widgets</code>`, two `<li>` items, and `<a href="#task-3">task 3</a>` from `linkTaskRefs`) **before** clicking `Reply`.
   - Clicking `<a href="#task-3">task 3</a>` with `Control` held (`ctrlKey: true`) left `/inbox` untouched with no overlay opened; a plain click intercepted the `#task-3` fragment and opened Task B in `DetailOverlay` (`/tasks/3`).
   - On `/inbox`, Task D (`pendingSecret`) rendered no `.inbox-question` while collapsed. Clicking `Reply` opened `.inbox-responder` displaying the `Alert` naming `stripe-api-key` and the `"Open the task"` button, with **no** plain-text `<textarea>` reply box.
3. **`DetailOverlay` (`PendingQuestion`) verification at `1280x800` and `390x844`:**
   - Opening `/tasks/5` rendered `[data-testid="pending-question"]` (`"Waiting for your reply"`) at the top of `.detail-main`, above both `.description` and `.timeline` (verified via `boundingBox().y` at both `1280x800` and `390x844`).
   - Typed `"Use Option A (us-central1 staging) and deploy now."` into `PendingQuestion`'s uncontrolled reply input (`aria-label="Reply to 5"`), waited for a live background poll of `GET /api/tasks/5` (`200 OK`), and verified the draft remained intact.
   - Clicked `Send`; `[data-testid="pending-question"]` unmounted as Task C transitioned back to `queued` and dispatched Attempt 2 (`5-2`).

---

### 3. Queued Task Waiting Status on `TaskList`, `TaskBoard`, `DetailOverlay`, and CLI (`e9fad2b64`, `f0ace8dd3`, `daf546150`)

1. **Setup:** With both worker slots (`-max-workers 2`) occupied by live runs (`5-2` and Task F `7-1`), filed Task E (`#8`, *"Queued task waiting on worker capacity"*).
2. **Store & API verification:**
   - `dispatch.Cycle` recorded a `model.QuotaWaitCapability` (`"quota"`) wait on Task E (`"2 of 2 workers busy"`).
   - `GET /api/tasks/8` returned `state: "queued"`, `activity: "waiting: 2 of 2 workers busy"`, `activityBySetup: true`.
3. **UI verification at `1280x800` and `390x844`:**
   - On `/` (`TaskList`), Task E's row rendered `.badge-icon.badge-queued` with `aria-label="Queued"` and `title="Queued — waiting: 2 of 2 workers busy"`, and omitted `.task-activity` from the row so the list remained uncluttered.
   - On `/boards/<id>` (`TaskBoard`), Task E's `.board-card` in the `Queued` column rendered `.badge-icon.badge-queued` with `title="Queued — waiting: 2 of 2 workers busy"`, and omitted `.board-card-activity` from the card.
   - On `/tasks/8` (`DetailOverlay`), `.detail-state .badge-queued` rendered `"Queued"` with `title="Queued — waiting: 2 of 2 workers busy"`, and `.detail-activity` displayed `"grain"` alongside `"waiting: 2 of 2 workers busy"`.
4. **CLI verification:**
   - `grain -server <url> -token <token> get 8` printed:
     ```
     state:      queued
     activity:   waiting: 2 of 2 workers busy
     ```
   - `grain -server <url> -token <token> -json list` included `"state": "queued"` and `"activity": "waiting: 2 of 2 workers busy"`.

---

### 4. Shared Sandbox Budget Contention Holding CI Job Admissions & `wait_for_checks` Deadlock Escape Hatch (`d21320e92`)

Verified via `TestSharedSandboxBudgetContentionHoldsCIJobsAndAdmitsStuckWaitForChecksJob` in `pkg/cli/ci_capacity_test.go` using the daemon's real `sharedSandboxGate(ledger, gate)` (`pkg/cli/ci.go`), `model.Ledger`, and `ci.NewSharedGate`:

1. **Contention hold:** Configured a shared host budget of `4 vCPUs / 8192 MiB` with `1` active agent sandbox holding `2 vCPUs / 4096 MiB` (`2 vCPUs / 4096 MiB` remaining). Set pending queued agent sandbox demand on the ledger to `AgentShape{CPUs: 4, MemoryMB: 4096}` (so the queued agent task cannot fit until the active run finishes).
2. **CI job held back:** Attempted to admit a `1 vCPU / 2048 MiB` CI job through `sharedSandboxGate`. Even though `1 vCPU / 2048 MiB` physically fits in the `2 vCPUs / 4096 MiB` free room, `sharedSandboxGate` held the CI job back, reporting `gate.Waiting() === "waiting behind a queued agent sandbox"` on the first wait tick so CI jobs cannot starve the queued agent task.
3. **`Stuck` deadlock escape hatch (`wait_for_checks`):** Marked the live agent run blocked inside `wait_for_checks` via `ledger.EnterCheckWait()` (`ledger.Stuck() == true`), since that live agent will never release its `2 vCPUs` until its CI run finishes. Verified that `sharedSandboxGate` bypassed the queued-agent hold and immediately admitted and completed the CI job (`Waiting() === ""`).
4. **Release after agent sandbox exits:** Exited check-wait, re-verified a second CI job was held back with `"waiting behind a queued agent sandbox"`, then released the active `2 vCPU` agent sandbox (`Release(agentShape)`) and cleared `SetAgentDemand(model.Shape{})` as the queued agent claimed its slot; verified the held CI job immediately acquired its slot and completed.

---

## Bug Found and Fixed

### B1: Human-readable `grain get <id>` omitted `t.Activity` for queued tasks waiting on quota or capability grants

- **Symptom:** §4 step 3 specifies that both `DetailOverlay` and the `grain` CLI display `"waiting: <reason>"` when a queued task is held back by a `QuotaWaitCapability` or scarce capability wait. While `GET /api/tasks` and `GET /api/tasks/{id}` populated `Task.Activity` (`"waiting: 2 of 2 workers busy"`) and `grain -json get <id>` / `grain -json list` included `"activity"`, the human-readable `taskBlock(t ui.Task)` formatter in `pkg/cli/taskprint.go` printed `state:      queued` and never printed `t.Activity`.
- **Root cause:** `taskBlock` in `pkg/cli/taskprint.go` formatted `state`, `scheduled`, `blocked`, `deferred`, `branch`, `pr`, and `merged`, but had no line for `t.Activity`.
- **Fix:** Updated `taskBlock` in `pkg/cli/taskprint.go` to print `activity:   %s\n` immediately beneath `state:` whenever `t.Activity != ""`.
- **Regression tests:**
  - Unit test `TestTaskBlockShowsActivityAndWaitReason` in `pkg/cli/defer_until_test.go`.
  - End-to-end CLI assertion (`grain get <id>`) in `ui/e2e/waiting-tasks-and-questions-walkthrough.spec.js`.
