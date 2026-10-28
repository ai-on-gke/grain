# Walkthrough & Test Run: Task Pause/Resume, Durable Suspensions & `priority-dispatch` (`docs/test-plans/2026-10-26.md` §1)

**Date**: 2026-10-26  
**Plan Section**: `docs/test-plans/2026-10-26.md` §1 (*Task Pause & Resume, Durable Run Suspensions, and `priority-dispatch` Capability*)  
**Environment**:
- Binary: `go build ./cmd/grain` + `cd ui && npm ci && npm run build`
- Live Server: `grain demo -github-sim -fake-ci -base-branch-ci -git-proxy -merge-queue-states` backed by embedded SQLite (`modernc.org/sqlite` in WAL mode) and inspected with `sqlite3` (`3.40.1-2+deb12u2`)
- Browser: Headless Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at desktop (`1440x900`) and mobile (`390x844`) viewports in both light and dark modes

---

## Executive Summary

All three areas of `docs/test-plans/2026-10-26.md` §1 were executed end-to-end against a live `grain demo` server, embedded SQLite database, `grain` CLI, and Playwright browser.

During the walkthrough, **three scoped bugs** were discovered, reproduced deterministically, fixed in this branch, and covered by regression tests:

1. **Bug 1 — Agent `POST /api/tasks/{id}/run-now` (and `/pause`, `/resume`) allowed overriding an operator-paused task (`pkg/ui/client.go`, `pkg/ui/taskpause.go`, `pkg/ui/agent_api_gate.go`)**:
   - **Symptom**: While commit `a9738a836` blocked an agent from overwriting or lifting an operator's deployment-wide `/api/pipeline-pause`, neither `Client.RunNow`, `Client.Pause`, `Client.Resume`, nor `agentRails` checked `Store.GetPause(ctx, id)` for a per-task operator pause (`model.TaskPause`, `20ead6fe5`). Calling `POST /api/tasks/{id}/run-now` as an agent (`X-Grain-Acting-Task` or `requestedBy`) on an operator-paused task returned `200 OK` and mutated the paused task's underlying state in SQLite (approving unapproved tasks, clearing `DeferredAt`, setting `RunNowAt`, and posting a comment claiming the next cycle would start it), and calling `POST /api/tasks/{id}/resume` or `POST /api/tasks/{id}/pause` over the agent API allowed an agent to lift or overwrite an operator's per-task pause.
   - **Fix**: Updated `Client.RunNow` (`pkg/ui/client.go`), `Client.Pause` & `Client.Resume` (`pkg/ui/taskpause.go`), and `agentRails` (`pkg/ui/agent_api_gate.go`) to refuse agent callers (`403 Forbidden`) when targeting a task paused by an operator or another principal, leaving the paused task untouched. Added `TestRunNowByAnAgentRefusesAnOperatorPausedTask` in `pkg/ui/run_now_test.go`.

2. **Bug 2 — `heldForRunNow` misclassified individually paused, `priority-dispatch`, and standing-session tasks under a pipeline pause (`ui/src/alerts.js`), and paused `runNowAt` hint promised immediate next-cycle dispatch (`ui/src/components/detail/Actions.jsx`)**:
   - **Symptom**:
     - `heldForRunNow(pause, t)` in `ui/src/alerts.js` returned `true` for any queued task without `t.runNowAt` or `t.blocked`, including tasks that were individually paused (`t.paused`), held `priority-dispatch` (`t.capabilities`), or belonged to a standing session (`t.consoleSession`). Under a pipeline pause, individually paused tasks showed the misleading `"paused — waits for Run now"` chip and hint even though `Run now` cannot start a paused task, and `priority-dispatch` / `consoleSession` tasks showed `"paused — waits for Run now"` even though `PriorityPassesPause` dispatches them without `Run now`.
     - In `ui/src/components/detail/Actions.jsx`, a paused task with `t.runNowAt` set (e.g. restored by `settleRunNow` / `Store.RestoreRunNow`) rendered `"Asked for a run. It starts on the next dispatch cycle, or as soon as there is capacity for it."` instead of explaining that it waits until the task is resumed.
   - **Fix**: Excluded `t.paused`, `t.consoleSession`, and `priority-dispatch` from `heldForRunNow` in `ui/src/alerts.js`, updated the `t.runNowAt` hint in `ui/src/components/detail/Actions.jsx` when `isTaskPaused(t)` to `"Asked for a run. It starts ahead of the queue once this task is resumed."`, and added unit tests in `ui/src/alerts.test.js` and E2E assertions in `ui/e2e/task-pause.spec.js`.

3. **Bug 3 — `grain pause <id>`, `grain pause -now <id>`, and `grain get <id>` omitted `t.Paused` from CLI output (`pkg/cli/taskprint.go`)**:
   - **Symptom**: `taskBlock(t ui.Task)` in `pkg/cli/taskprint.go` printed `state: <state>` without formatting `t.Paused`, causing `grain pause <id>` and `grain resume <id>` to print identical output and hiding the pause actor, timestamp, prior state, grace deadline, and immediate flag unless `-json` was passed.
   - **Fix**: Updated `taskBlock` in `pkg/cli/taskprint.go` to print `paused:     by <by> at <RFC3339> (was <fromState>)[, grace until <RFC3339> | , immediate]` whenever `t.Paused != nil`. Added `TestTaskBlockShowsPausedStatus` in `pkg/cli/defer_until_test.go`.

---

## §1.1 Task Pause & Resume (`20ead6fe5`, `6032bf747`, `40316c7a6`, `db22a553c`, PR #4814; `fca5002a9`, PR #4875)

### HTTP & CLI Exercise Across All 5 Open States

We exercised `POST /api/tasks/{id}/pause` (graceful `{}` and `{"immediate": true}`), `POST /api/tasks/{id}/resume`, `grain pause <id>`, `grain pause -now <id>`, and `grain resume <id>` across `queued`, `running`, `awaiting_reply`, `deferred`, and `merging` (`completed` on merge queue) states:

| Task | Underlying State | Action Exercised | SQLite `task_pause` Row & API `reason` | Post-Resume Verification |
| :--- | :--- | :--- | :--- | :--- |
| [Task #2](/tasks/2) (*Fix flaky retry test*) | `queued` | `POST /api/tasks/2/pause` `{}` -> `POST /api/tasks/2/resume` & `grain pause 2` -> `grain resume 2` | `from_state=queued`, `immediate=0`, `grace_until=NULL`; `reason="queued/operator_paused"` (`control="Resume"`) | Restored to `state="queued"`, `paused=null`, `reason="queued/ready"` |
| [Task #3](/tasks/3) (*Bump the Go toolchain to 1.24*) | `running` | Graceful `grain pause 3` -> escalate `grain pause -now 3` -> `grain resume 3` | Graceful: `from_state=running`, `run_id=demo-run-3`, `immediate=0`, `grace_until=+2m`; after `-now`: `immediate=1` | CLI output shows `paused: by debian at ... (was running), grace until ...` then `, immediate`; resume clears `task_pause` |
| [Task #8](/tasks/8) (*Add pagination to the tasks API*) | `running` | Immediate `POST /api/tasks/8/pause` `{"immediate":true}` -> `POST /api/tasks/8/resume` | `from_state=running`, `immediate=1`; posts automation comment: `"Paused by debian. The run in progress is being stopped now; whatever it had not pushed is gone."` | Restored with `Resumed by debian.` comment |
| [Task #4](/tasks/4) (*Investigate the merge queue stall*) | `awaiting_reply` | `grain pause 4` -> `grain resume 4` | `from_state=awaiting_reply`, `reason="awaiting_reply/operator_paused"`; pending question preserved | Restored to `awaiting_reply` (`reason="awaiting_reply/question"`, `control="Reply"`) |
| [Task #9](/tasks/9) (*Replace the settings pane with a form library*) | `deferred` | `POST /api/tasks/9/pause` -> `grain resume 9` | `from_state=deferred`, `reason="deferred/operator_paused"`; `deferredUntil` (`2026-11-25T15:33:39Z`) untouched | Restored to `deferred` with `deferredUntil` intact |
| [Task #5](/tasks/5) (*Rotate the Gemini signing key*) | `merging` (`completed`, `mergeQueueState="batching"`) | `grain pause 5` -> `grain resume 5` | `from_state=completed`, `reason="completed/operator_paused"`, `mergeQueueReason=null`; active batch slot released while `order_key=4194304.0` and `merge_queue_entered_at=2026-10-26T14:00:00Z` stay untouched in SQLite | Restored to `mergeQueueState="batching"` with `order_key=4194304.0` and `merge_queue_entered_at=2026-10-26T14:00:00Z` preserved |

#### CLI Output Trace (`grain pause 3`, `grain pause -now 3`, `grain resume 3`)

```text
--- grain pause 3 ---
3 Bump the Go toolchain to 1.24
state:      running
paused:     by debian at 2026-10-26T15:39:15Z (was running), grace until 2026-10-26T15:41:15Z
repo:       acme/widgets
auto-merge: false
capabilities: self-debug
fields:     area=backend,infra priority=medium

--- grain pause -now 3 ---
3 Bump the Go toolchain to 1.24
state:      running
paused:     by debian at 2026-10-26T15:39:15Z (was running), immediate
repo:       acme/widgets
auto-merge: false
capabilities: self-debug
fields:     area=backend,infra priority=medium

--- grain resume 3 ---
3 Bump the Go toolchain to 1.24
state:      running
repo:       acme/widgets
auto-merge: false
capabilities: self-debug
fields:     area=backend,infra priority=medium
```

### Browser UI Verification (`/`, `/tasks/<id>`, `/repos/acme/widgets/merge-queue`, `/session/new`)

- **Task List (`/`)**:
  - Paused tasks render `.badge[aria-label="Paused"]` with hover attribute `title="Paused by debian at 10/26/2026, 3:46:49 PM (was Merging)"`.
  - Under a deployment pipeline pause (`POST /api/pipeline-pause`), paused tasks and `priority-dispatch` tasks ([Task #104](/tasks/104)) have `0` `.chip-held-for-run-now` chips.
- **Task Detail ([Task #11](/tasks/11))**:
  - Displays the `"Paused"` state badge, `data-testid="paused-note"` (`"Paused by debian at 10/26/2026, 3:54:53 PM (was Queued)."`), `data-testid="resume-task"` (`Resume` button directly below the note: desktop `noteBox={"x":1152,"y":351.6,"width":260,"height":38.4}`, `resumeBox={"x":1152,"y":407.6,"width":260,"height":36.5}`; mobile `noteBox={"x":17,"y":3328.7,"width":357,"height":19.2}`, `resumeBox={"x":17,"y":3365.5,"width":357,"height":36.5}`), and the paused-aware `Run now` hint: `"Asked for a run. It starts ahead of the queue once this task is resumed."`.
- **Merge Queue ([Merge Queue](/repos/acme/widgets/merge-queue))**:
  - Displays [Task #5](/tasks/5) (*Rotate the Gemini signing key*) with its paused status and returns it to its original `MergeQueueEnteredAt` slot on resume.
- **Unstarted Session ([New Session](/session/new)) vs Interactive Task Chat (`6032bf747`)**:
  - On [New Session](/session/new), `.task-chat-compose` renders `0` `Pause` buttons (`!onSend && t?.id` guard in `ui/src/components/TaskChat.jsx`).

---

## §1.2 Durable-Tasks Suspension Records (`DT-2`) & Dispatch-Standing Preservation (`DT-3`) (`19a361cba`, `284456eb3`, PR #4890)

We pressed `Run now` (`POST /api/tasks/11/run-now`) on [Task #11](/tasks/11) (`runNowAt = 2026-10-26T15:42:43.838493356Z`), granted it `priority-dispatch` and `base-repair:acme/widgets#main`, and drove 4 consecutive runs through the SQLite store across `paused` (`usage-limit`), `oom` (`oom`), `setup` (`setup`), and `operator-pause` (`operator-pause`), recording a CLI session token (`SetSuspendedSession`) on runs 1, 2, and 4.

### SQLite Inspection (`task_run`, `task_suspension`, `task_observation`)

1. **`task_run` table**:
   ```text
   id                  attempt  outcome    suspend_kind    run_now_at                               standing
   ------------------  -------  ---------  --------------  ---------------------------------------  ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
   run-11-usage-limit  1        paused     usage-limit     2026-10-26 15:42:43.838493356 +0000 UTC  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-oom          2        oom        oom             2026-10-26 15:42:43.838493356 +0000 UTC  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-setup        3        suspended  setup           2026-10-26 15:42:43.838493356 +0000 UTC  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-op-pause     4        suspended  operator-pause  2026-10-26 15:42:43.838493356 +0000 UTC  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   ```
2. **`task_suspension` table**:
   ```text
   run_id              kind            detail                            standing
   ------------------  --------------  --------------------------------  ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
   run-11-usage-limit  usage-limit     429 usage limit reached           {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-oom          oom             sandbox OOM killed (exit 137)     {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-setup        setup           sandbox container pull timed out  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   run-11-op-pause     operator-pause  paused by debian                  {"run_now_at":"2026-10-26T15:42:43.838493356Z","priority_dispatch":true,"session":false,"order_key":10485760,"fixes_base_branch":true,"grants":["gcp-key","gemini-key","priority-dispatch","self-debug"]}
   ```
3. **`task_observation` table & `GET /api/tasks/11`**:
   ```text
   task_id  streak_count  suspended_session_framework  suspended_session_token       suspended_session_run_id  run_now_at
   -------  ------------  ---------------------------  ----------------------------  ------------------------  ---------------------------------------
   11       0             claude                       claude-session-dt2-token-004  run-11-op-pause           2026-10-26 15:42:43.838493356 +0000 UTC
   ```
   - `streak_count` remains `0` (`failedAttempts: null` in `GET /api/tasks/11`) across all four suspensions.
   - `suspended_session_token` (`claude-session-dt2-token-004`) is preserved across suspensions and exposed in `GET /api/tasks/11` and `grain get 11`.
   - `run_now_at` (`2026-10-26T15:42:43.838493356Z`) is consumed by `StartRun` into `task_run.run_now_at`, automatically restored by `settleRunNow` when each suspended run finishes, and preserved across `grain resume 11` (`fca5002a9`).

---

## §1.3 `priority-dispatch` Capability & Agent `Run now` Guardrails (`b4f0c04a2`, `af975fe50`, PR #4815; `a9738a836`, PR #4804; `ed5081191`, PR #4785; `3696ca298`, PR #4812)

### Capability Picker, `system:pipeline-steward`, Pipeline Pause, and Red-Base Exemption

1. **Capability Offering & `system:pipeline-steward`**:
   - `GET /api/config` includes `{"id": "priority-dispatch", "name": "Priority dispatch", "ready": true}` in `capabilities` (`ui.OfferedCapabilities()`), and `model.IsPersonOnlyGrant("priority-dispatch")` is `true`.
   - `model. pipelineStewardTemplate` attaches `{Capability: "priority-dispatch", Via: "grain"}` alongside `self-debug` and its API presets (`3696ca298`).
2. **Red Base Branch Exemption (`3696ca298`) & `PriorityPassesPause`**:
   - Triggering `POST /api/e2e/base-branch/red` (`{"branch": "main"}`) confirmed `main` red and filed repair task [Task #16](/tasks/16).
   - Inspecting `GET /api/tasks` while `main` was red:
     - Ordinary queued [Task #2](/tasks/2): `blocked=true`, `reason="queued/blocked_on_task"` (held behind the red-base repair task).
     - Standing console session [Task #12](/tasks/12) (*Director Session*): `blocked=false`, `reason="queued/ready"`.
     - Approved `priority-dispatch` [Task #104](/tasks/104): `blocked=false`, `reason="queued/ready"` (never linked/held behind a red base branch's repair).

### Agent-Authenticated `POST /api/tasks/{id}/run-now` Guardrails over HTTP

With acting agent [Task #99](/tasks/99) seeded holding `api:dispatch-control` and `priority-dispatch`:

| Guardrail Scenario | HTTP Request | HTTP Status & Response Body |
| :--- | :--- | :--- |
| **(a1) Target task is paused by an operator** (`X-Grain-Acting-Task: 99`) | `POST /api/tasks/2/run-now` | `403 Forbidden` — `{"error":"task 2 is paused by human debian; only they may lift or override that pause"}` |
| **(a1) Target task is paused by an operator** (`requestedBy` body) | `POST /api/tasks/2/run-now` `{"requestedBy":"99","requestedByKind":"agent"}` | `403 Forbidden` — `{"error":"2 is paused by debian, and agent \"99\" may not start it while it is paused"}` |
| **(a2) Pipeline is paused by an operator** (`a9738a836`) | `POST /api/pipeline-pause` (`X-Grain-Acting-Task: 99`) | `403 Forbidden` — `{"error":"dispatch was paused by human debian (operator maintenance); only they may lift it"}` |
| **(b1) Target unapproved task declares `writes` push target** (`a9738a836`) | `POST /api/tasks/101/run-now` (`X-Grain-Acting-Task: 99`) | `403 Forbidden` — `{"error":"101 is not approved and asks for write target acme/other, which task 99 does not hold itself: a person has to approve it before an agent may run it"}` |
| **(b2) Target unapproved task carries declared template push set** (`a9738a836`) | `POST /api/tasks/102/run-now` (`X-Grain-Acting-Task: 99`) | `403 Forbidden` — `{"error":"102 is not approved and asks for declared push set from .grain/templates/release.yml, which task 99 does not hold itself: a person has to approve it before an agent may run it"}` |
| **(c) Target unapproved task requests `person`-only grant `priority-dispatch`** (`af975fe50`) | `POST /api/tasks/103/run-now` (`X-Grain-Acting-Task: 99`, holding `priority-dispatch`) | `403 Forbidden` — `{"error":"103 is not approved and asks for capability priority-dispatch, which task 99 does not hold itself: a person has to approve it before an agent may run it"}` |
