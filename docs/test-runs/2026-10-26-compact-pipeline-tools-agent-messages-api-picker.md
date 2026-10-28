# Walkthrough: Compact Pipeline MCP Tools, Delta Feed, Cross-Task `/agent-messages` & Grain API Picker (`docs/test-plans/2026-10-26.md` §4)

- **Date:** 2026-10-26, 14:30–15:25 UTC
- **Task:** `grain/049f1e09/task-1202`, base `main`
- **Commit tested:** `add8ca764` (`main` at the start of the run, "Merge pull request #4916 from bwsalmon/grain/049f1e09/task-1140")
- **Test Plan Reference:** `docs/test-plans/2026-10-26.md` §4 (*Compact Pipeline MCP Tools & Delta Feed, Cross-Task `POST /api/tasks/{id}/agent-messages`, `"Grain API"` Capability Picker Group, and `self-debug` Alias*)
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.10`, Node.js `v20.19.0`, npm `10.8.2`
  - Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, headless Chromium), viewports `1440×900` (desktop) and `390×844` (mobile)
  - Live servers & subprocesses:
    - Built frontend (`npm --prefix ui ci && npm --prefix ui run build`) and `grain` binary (`go build -o /tmp/grain ./cmd/grain`)
    - `grain demo -open=false -github-sim -fake-ci -base-branch-ci -scheduled-workflows -merge-queue-states -merge-rules -addr 127.0.0.1:14290 -access-code-file /tmp/demo-access-code`
    - Live `grain mcpserver` JSON-RPC 2.0 stdio subprocesses (with `-server`, `-task`, `-addenda-after`, and `-tool-budget`)
    - `grain ci list -compact` and `grain ci list -compact -until ...` CLI commands

---

## Summary of Outcomes

| Area | Scope | Verdict |
|---|---|---|
| **1A. Pipeline Delta Feed** | `GET /api/pipeline/changes` & `read_pipeline_changes` MCP tool across task creation, run start/finish, merge-queue transitions, stall notes, base-branch CI red/green, and merge-batch completions; `<RFC3339>~<seq>` cursor pagination (`limit=2`) and HTTP 400 on malformed cursors | **PASS** |
| **1B. Repo Visibility (`6eb280bf9`)** | `GET /api/pipeline/changes` with repo-scoped `api:tasks:read@acme/widgets`: HTTP 403 when `?repo=` is omitted, HTTP 403 when `?repo=acme/gadgets` is out of scope, HTTP 200 when `?repo=acme/widgets` is in scope (excluding `acme/gadgets` tasks) | **PASS** |
| **1C. Compact Task & Queue Views** | `read_grain_task` (`view: "summary"` default ≤1 KB with 4-line handover head + last 5 events, `view: "conversation"` with `after` pagination, `view: "full"`, and error on unknown view); `list_grain_tasks` (`changed_since`, `stalled`, `kind` vocabulary); `query_merge_queue` (`view: "compact"` default + `changed_since`) | **PASS** |
| **1D. `expect` Preconditions (`6eb280bf9`)** | `expect: {state, title_prefix, kind}` on task-mutating `api_*` tools: matching preconditions succeed; mismatching `state`, `title_prefix`, or `kind` refuse with zero side effects; out-of-scope repo and missing `tasks:read` fail at the visibility gate without leaking target task state or title | **PASS** |
| **1E. Compact CI & `-until` (`ec744e2b4`)** | `GET /api/ci/runs?view=compact&until=...` (passing jobs omitted, shared-fault step output elided in favor of `fault: <digest>`, top-level `faults` summary, HTTP 400 on unknown `view=`), `grain ci list -compact -until ...`, and MCP `list_ci_runs` compact output | **PASS** |
| **1F. `mcpserver -tool-budget`** | Soft wrap-up notice at exactly call 40 (or ≥150 KB at call 10 with 16 KB reads) and hard handover notice at call 60 (or ≥300 KB at call 19), suppressed on subsequent calls | **PASS** |
| **2. Cross-Task `/agent-messages` (`05b46ea3f`, `7976efb4d`)** | `POST /api/tasks/{B}/messages` from task A refused with HTTP 403 naming `/api/tasks/{B}/agent-messages`; self `/messages` allowed; self `/agent-messages` refused with HTTP 403; `api_message_task_agent` records attributed comment, preserves `PendingQuestionCommentID`, and delivers addendum via `GET /api/tasks/{B}/messages?agents=true` on task B's next MCP tool call; `Store.ClearMisdirectedQuestions` lifts legacy misdirected questions | **PASS** |
| **3. `"Grain API"` Picker & `self-debug` Alias (`927e25c2f`, `7caf65604`, `07fde2e20`)** | `ApiGrantPicker.jsx` verified across [Tasks](/), task detail (`/tasks/<id>`), [Sessions](/sessions), and [Processes](/processes) (`ScheduleOverlay` & `TemplateOverlay`); presets before scopes, `"Danger"` badges + tooltips, repo-scoped grants, desktop (`1440×900`) & mobile (`390×844`) layout invariants, and `self-debug` alias resolution (`tasks:read` + `deployment:read`) | **PASS** |

---

## Detailed Execution & Findings

### Part 1: Compact Pipeline MCP Tools, Delta Feed, `expect` Preconditions, Compact CI, and `-tool-budget`

#### 1A. `GET /api/pipeline/changes` & `read_pipeline_changes` MCP Tool
1. **Initial 24h Window (`GET /api/pipeline/changes`)**:
   - Returned HTTP 200 with 30 seeded events across `kind` values `["base", "queue", "stall", "task"]`, `since` set to 24h prior, and `next` cursor `2026-10-26T15:09:17.287898197Z~0`.
2. **Live MCP `read_pipeline_changes` & Cursor Advancement**:
   - Filed a steward task (`id=18`) with `api:pipeline-steward` and `self-debug` capabilities and spawned `grain mcpserver -server http://127.0.0.1:14290 -task 18`.
   - Verified `tools/list` registered 99 tools including `read_pipeline_changes`, `read_grain_task`, `list_grain_tasks`, and the generated `api_*` tools.
   - Calling `read_pipeline_changes(after=cursor0)` returned:
     ```text
     task 18 task →filed 2026-10-26T15:09:17Z
     next: 2026-10-26T15:09:17.393830037Z~0
     ```
   - Calling `read_pipeline_changes` immediately again with `after=cursor1` returned:
     ```text
     Nothing has changed since that cursor.
     next: 2026-10-26T15:09:17.393830037Z~0
     ```
3. **Live Pipeline Mutations Across All 5 Event Kinds (`task`, `queue`, `stall`, `base`, `batch`)**:
   - Drove `POST /api/e2e/base-branch/red` on `acme/widgets` `main`, which ran `fake-ci-1` and `fake-ci-2`, confirmed `main` red, and filed repair task `19`.
   - Recorded a task run (`started_at`, `finished_at`), a merge-queue transition (`merge_queue_state='batching'`), a merge-queue stall (`merge_queue_stalled_at`), and a completed merge batch (`merge_batch_history`) after `cursor1`.
   - Polled `GET /api/pipeline/changes?after=<cursor1>` and verified all 8 transitions appeared in chronological `(at, seq)` order:
     - `{'id': '19', 'kind': 'task', 'to': 'filed'}`
     - `{'id': 'acme/widgets@main', 'kind': 'base', 'to': 'running', 'detail': 'run fake-ci-1'}`
     - `{'id': 'acme/widgets@main', 'kind': 'base', 'from': 'running', 'to': 'failure', 'detail': 'run fake-ci-1'}`
     - `{'id': '19', 'kind': 'task', 'from': 'filed', 'to': 'running'}`
     - `{'id': '19', 'kind': 'task', 'from': 'running', 'to': 'awaiting_reply'}`
     - `{'id': '19', 'kind': 'queue', 'to': 'batching'}`
     - `{'id': '19', 'kind': 'stall', 'to': 'queue stalled', 'detail': 'batching'}`
     - `{'id': 'batch 1', 'kind': 'batch', 'from': 'running', 'to': 'merged', 'detail': 'acme/widgets@main merged 1 of 1, run fake-ci-1'}`
   - Paged through all 8 events via MCP `read_pipeline_changes` with `limit: 2` across 4 consecutive pages without duplicates or gaps.
   - Verified `GET /api/pipeline/changes?after=not-a-timestamp` returned HTTP 400:
     `"not-a-timestamp" is not a pipeline cursor: want <RFC 3339 time>~<seq>, as the last read's next`.

#### 1B. Repository Visibility Filtering on `GET /api/pipeline/changes` (`6eb280bf9`)
- Created task `20` in `acme/gadgets` (`"Secret gadgets task"`) and task `21` in `acme/widgets` holding only repo-scoped `api:tasks:read@acme/widgets` and `api:tasks:write@acme/widgets`.
- With `X-Grain-Acting-Task: 21`:
  - `GET /api/pipeline/changes` (no `?repo=`) returned **HTTP 403**:
    `task 21 holds tasks:read only for some repositories, and GET /api/pipeline/changes does not name one it reaches`.
  - `GET /api/pipeline/changes?repo=acme/gadgets` returned **HTTP 403**:
    `task 21 holds tasks:read, but not on acme/gadgets`.
  - `GET /api/pipeline/changes?repo=acme/widgets` returned **HTTP 200** with 39 entries scoped to `acme/widgets`, excluding task `20` (`acme/gadgets`).

#### 1C. `read_grain_task` Views, `list_grain_tasks` Filters, and `query_merge_queue`
1. **`read_grain_task` (`view: "summary"` default, `view: "conversation"`, `view: "full"`)**:
   - Added progress comments and a 5-line handover to task `19`.
   - Default `read_grain_task({"task_id": "19"})` returned a **695-byte** summary (< 1 KB target) showing `Kind: repair`, `Merge queue: Testing Batch (batching), ... stalled`, the last events, and the first 4 lines of the handover clipped with `...`, ending with `(view "full" for the whole record, "conversation" to page the thread)`.
   - `read_grain_task({"task_id": "19", "view": "conversation"})` returned the 6-comment thread chronologically; passing `"after": 10` returned only comments after `#10`.
   - `read_grain_task({"task_id": "19", "view": "full"})` returned the full 1,685-byte task record including description, runs, and comments.
   - `read_grain_task({"task_id": "19", "view": "bogus"})` returned `isError: true` with `view "bogus" is not one of summary, full or conversation`.
2. **`list_grain_tasks` (`kind`, `stalled`, `changed_since`)**:
   - `list_grain_tasks({"kind": "repair"})` returned only repair task `19` (`kind repair`).
   - `list_grain_tasks({"kind": "proposal"})` returned only unapproved proposal tasks (`kind proposal`).
   - `list_grain_tasks({"stalled": true})` returned only stalled/blocked tasks (`17` and `19`).
   - `list_grain_tasks({"changed_since": "10m"})` filtered to recently changed tasks.
   - `list_grain_tasks({"kind": "notakind"})` returned `isError: true` with `kind "notakind" is not one of port, repair, session, proposal, durable`.
3. **`query_merge_queue` (`view: "compact"` default & `changed_since`)**:
   - Default `query_merge_queue({})` rendered compact one-line entries for active batches and queued tasks with `(view "full" adds notes and merge rules)`.
   - `query_merge_queue({"changed_since": "5m"})` filtered to only task `19` (stalled within the last 5m) and summarized `(7 batch(es) and queued change(s) unchanged since ... not shown)`.

#### 1D. `expect` Preconditions & Repo-Visibility Gate (`6eb280bf9`)
- Created unapproved proposal task `22` (`state: "proposed"`, `kind: "proposal"`, `title: "Expect check target task"`).
- Verified mismatching `expect` preconditions on `api_add_comment` were refused without writing a comment:
  - `expect: {"state": "queued"}` → refused: `task 22 does not match what this call expected: state is proposed, not queued. Nothing was changed -- re-read it with read_grain_task before deciding what to do`.
  - `expect: {"state": "proposed", "title_prefix": "Wrong prefix"}` → refused: `title is "Expect check target task", which does not start "Wrong prefix"`.
  - `expect: {"state": "proposed", "kind": "none"}` → refused: `kind is proposal, not none`.
  - Confirmed via `GET /api/tasks/22` that 0 comments were written across all 3 refusals.
- Verified matching `expect: {"state": "proposed", "title_prefix": "expect check", "kind": "proposal"}` (case-insensitive prefix) succeeded and posted the comment.
- **Repo-visibility check before `expect` evaluation (`6eb280bf9`)**:
  - Task `21` (`api:tasks:read@acme/widgets`, `api:tasks:write@acme/widgets`) calling `api_add_comment` with `expect` on task `20` (`acme/gadgets`) was refused by the visibility gate (`reading task 20 to check expect: task 21 holds tasks:read, but not on acme/gadgets`) without leaking task `20`'s state or title.
  - Task `23` (holding only `api:tasks:write`, no `api:tasks:read`) calling `api_add_comment` with `expect` on task `22` was refused (`task 23 holds no api grant for scope tasks:read, which GET /api/tasks/{id} needs`) without leaking task `22`'s state or title.

#### 1E. Compact CI Runs (`GET /api/ci/runs?view=compact&until=...` & `grain ci list -compact -until`)
- *Demo harness enhancement*: Extended `demoFakeCI.FinishWithOutput` (`POST /api/e2e/fake-ci/runs/{id}/finish` in `pkg/demo/demo_scheduled_workflows.go`) to accept optional `step`, `output`, and `error` fields (covered by unit test `TestDemoFakeCIFinishWithOutputPopulatesSharedFaultSignature`) so `-fake-ci` runs can carry step output for shared-fault signature calculation.
- Started runs `fake-ci-3` (`grain/task-501`) and `fake-ci-4` (`grain/task-502`) and finished both with identical `go test` failure output, plus `fake-ci-5` (`grain/task-503`) finished with `success`.
- `GET /api/ci/runs?view=compact` returned HTTP 200 with:
  - Passing run `fake-ci-5` omitting `jobs` (`jobs: undefined`).
  - Failing runs `fake-ci-3` and `fake-ci-4` omitting `steps` and carrying `"fault": "6e89922d857bb3f9d0bf2530706d4acb95b2ee0d4a84b8a0c556335c41d003b6"`.
  - Top-level `faults` array with 1 entry (`refs: 2`, `runs: 2`, `ownerTaskId: "19"`).
- `GET /api/ci/runs?view=unknown` returned **HTTP 400**: `"unknown" is not a view: use full (the default) or compact`.
- `GET /api/ci/runs?view=compact&until=2h` returned HTTP 200 with `matched: 0, stored: 5`.
- CLI `grain ci list -compact` printed the compact table with `test: failure, step go test, shared fault 6e89922d...` and omitted passing jobs on `fake-ci-5`.
- CLI `grain ci list -compact -until 2h` printed: `no CI run matches started before ... -- this deployment remembers 5 runs in all`.

#### 1F. `mcpserver -tool-budget`
- Spawned `grain mcpserver -tool-budget` and ran small commands:
  - Calls 1–39 had no budget notice.
  - **Call 40** appended the soft notice:
    `[grain] Tool budget: this wake has made 40 tool calls and 16 KB of tool output, past its target of 40 calls or 150 KB. Read nothing a change does not name, prefer summary views, and start wrapping up; at 60 calls or 300 KB you will be asked to write your handover and stop.`
  - Calls 41–59 did not repeat the soft notice.
  - **Call 60** appended the hard handover notice:
    `[grain] Tool budget exhausted: this wake has made 60 tool calls and 24 KB of tool output, past its limit of 60 calls or 300 KB. Write your handover now (write_handover) -- what you settled, what is left, and the read_pipeline_changes cursor to start from -- then end this wake. The next wake carries on from the handover with a fresh context.`
  - Call 61 did not repeat the hard notice.
- Spawned a second `grain mcpserver -tool-budget` reading a 16 KB file per call:
  - **Call 10** (160 KB ≥ 150 KB) triggered the byte-threshold soft notice.
  - **Call 19** (305 KB ≥ 300 KB) triggered the byte-threshold hard handover notice.

---

### Part 2: Cross-Task Agent Messaging (`POST /api/tasks/{id}/agent-messages`, `05b46ea3f`, `7976efb4d`)

1. **Setup**:
   - Created task `24` (`"Pipeline Steward Sender"`) and task `25` (`"Worker Target Task"`).
   - Task `25` called `POST /api/tasks/25/messages` (`X-Grain-Acting-Task: 25`) with `"Should I use approach X or Y?"`, parking task `25` in `awaiting_reply` with `PendingQuestionCommentID = 18`.
2. **Cross-Task `/messages` Refused (`HTTP 403`)**:
   - Task `24` calling `POST /api/tasks/25/messages` (`X-Grain-Acting-Task: 24`) returned **HTTP 403**:
     `task 24 cannot call POST /api/tasks/{id}/messages on task 25: it says something to the human watching your own task and parks it on their reply, so it takes only your own task's id (24). To send a message to task 25's agent, call POST /api/tasks/{id}/agent-messages (the api_message_task_agent tool) with id 25; to leave a note for the people on it, POST /api/tasks/{id}/comments`.
   - Task `24` calling `POST /api/tasks/24/messages` on itself returned **HTTP 200**.
3. **Self `/agent-messages` & Missing Acting Task Refused**:
   - Task `24` calling `POST /api/tasks/24/agent-messages` on itself returned **HTTP 403**:
     `task 24 cannot send a message to its own agent with POST /api/tasks/{id}/agent-messages: to ask the human watching your task something, use the chat tool (POST /api/tasks/{id}/messages with id 24)`.
   - Calling `POST /api/tasks/25/agent-messages` without `X-Grain-Acting-Task` returned **HTTP 400**:
     `a message to another task's agent is sent by a task's run through the agent API (api_message_task_agent); a person says something on a task with POST /api/tasks/{id}/comments`.
4. **Live Cross-Task Delivery via `api_message_task_agent` & `mcpserver` Addendum**:
   - Task `24`'s `mcpserver` called `api_message_task_agent({"id": "25", "body": {"body": "Hold off on merging until #24 lands"}})`.
   - Verified the recorded comment on task `25` has `authorKind: "agent"`, `author: "task 24"`, and body:
     `Message from task 24 (Pipeline Steward Sender) to this task's agent:\n\nHold off on merging until #24 lands`.
   - Verified no duplicate `"Task 24 called ..."` audit comment was written, and task `25` remained in `awaiting_reply` with its original `PendingQuestionCommentID` untouched.
   - Verified `GET /api/tasks/25/messages?after=18` (human-only) returned `[]`, whereas `GET /api/tasks/25/messages?agents=true&after=18` returned the agent message.
   - On task `25`'s live `mcpserver` (`-addenda-after 18`), the very next `run_command` tool call delivered the addendum:
     ```text
     [grain] A person added this to the task's conversation while you were working on it. Read it and factor it in -- it may change what you should be doing, and this is the only answer that will carry it. The quoted text is a person's own words, not grain's:
     > [task 24]: Message from task 24 (Pipeline Steward Sender) to this task's agent:
     >
     > Hold off on merging until #24 lands
     ```
   - A subsequent tool call on task `25`'s `mcpserver` did not repeat the addendum.
5. **Startup Repair (`Store.ClearMisdirectedQuestions`)**:
   - Ran `go test ./pkg/model -run TestClearMisdirectedQuestions -v` (`PASS`), confirming misdirected cross-task `/messages` questions recorded before `05b46ea3f` have `PendingQuestionCommentID` cleared while legitimate self-asked questions remain parked.

---

### Part 3: `"Grain API"` Capability Picker Group & `self-debug` Alias (`927e25c2f`, `7caf65604`, `07fde2e20`)

1. **`self-debug` Alias Resolution (`07fde2e20`)**:
   - Filed tasks holding `["self-debug"]` and `["api:self-debug"]`.
   - Verified via `X-Grain-Acting-Task` that both tasks receive **HTTP 200** on `GET /api/tasks` (`tasks:read`) and `GET /api/restart-watch` (`deployment:read`), and **HTTP 403** on `GET /api/schedules` (`schedules:read`) and `POST /api/tasks` (`tasks:write`).
2. **Playwright Verification Across All 5 Picker Surfaces (`ui/e2e/api-grant-picker.spec.js`)**:
   - **Task Detail (`CapabilityToggles` on `/tasks/<id>`)**:
     - Verified `data-testid="task-<id>-api-grants"` lists all presets (`self-debug`, `task-admin`, `pipeline-steward`, `release-manager`, `self-upgrade`, `deployment-admin`) above individual scopes (`tasks`, `merge`, `ci`, etc.), shows scope expansion chips on each preset, flags dangerous scopes (`api:merge:force`, etc.) with `.api-dangerous` badge and tooltip `"only a person may grant this"`, excludes `api:*` from the flat `Capabilities` combobox, and supports repo-scoped ticking (`api:ci:write@acme/widgets`).
   - **New Task Overlay (`NewTaskOverlay` on [Tasks](/))**:
     - Clicked `+ New task`, verified `data-testid="new-task-api-grants"` starts collapsed (`aria-expanded="false"`, summary `"none granted"`), expands on click (`aria-expanded="true"`), displays `self-debug grants` (`tasks:read`, `deployment:read`), and satisfies layout invariants (`document.documentElement.scrollWidth <= window.innerWidth`, positive non-overlapping bounding boxes with presets above scopes) at both desktop (`1440×900`) and mobile (`390×844`) viewports.
   - **Console Session (`ConsoleSessionPage` on [Sessions](/sessions))**:
     - Opened the Director session, clicked `Edit Director capabilities`, expanded `data-testid="session-director-api-grants"`, ticked `self-debug`, and confirmed `"1 granted"` persisted.
   - **Schedule Overlay (`ScheduleOverlay` on [Processes](/processes))**:
     - Clicked `+ New` → `Schedule`, expanded `data-testid="schedule-api-grants"`, ticked `self-debug` and repo-scoped `ci:write` (`acme/widgets`), confirmed `"2 granted"`, saved the schedule, reloaded [Processes](/processes), and verified `GET /api/schedules` persisted `["api:ci:write@acme/widgets", "api:self-debug"]` and reopening the schedule displayed `"2 granted"`.
   - **Template Overlay (`TemplateOverlay` on [Processes](/processes))**:
     - Clicked `+ New` → `Template`, expanded `data-testid="template-api-grants"`, ticked `self-debug` and repo-scoped `ci:write` (`acme/widgets`), confirmed `"2 granted"`, saved the template, reloaded [Processes](/processes), and verified `GET /api/templates` persisted `["api:ci:write@acme/widgets", "api:self-debug"]` and reopening the template displayed `"2 granted"`.

---

## Code Changes Made in This Branch

1. **`pkg/demo/demo_scheduled_workflows.go` & `pkg/demo/demo_scheduled_workflows_test.go`**:
   - Added `FinishWithOutput` and extended `POST /api/e2e/fake-ci/runs/{id}/finish` to accept optional `step`, `output`, and `error` fields so walkthroughs and e2e tests running against `grain demo -fake-ci` can exercise `ci.SharedFaults` and `GET /api/ci/runs?view=compact` / `grain ci list -compact` shared-fault elision end-to-end.
   - Added unit test `TestDemoFakeCIFinishWithOutputPopulatesSharedFaultSignature`.
2. **`ui/e2e/api-grant-picker.spec.js`**:
   - Added end-to-end Playwright test covering `self-debug` alias resolution (`GET /api/tasks`, `GET /api/restart-watch`, `GET /api/schedules`, `POST /api/tasks`), `NewTaskOverlay` (with desktop `1440×900` and mobile `390×844` layout checks), `ConsoleSessionPage`, `ScheduleOverlay`, and `TemplateOverlay`.
3. **`docs/test-runs/2026-10-26-compact-pipeline-tools-agent-messages-api-picker.md`**:
   - Recorded this walkthrough report.
