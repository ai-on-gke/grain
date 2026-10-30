# Operator notifications, message_task, question summaries and Asked/Waiting labels

- **Plan:** `docs/test-plans/2026-10-28.md` §1 (retired `bwsalmon/grain`), restated in [Task #1714](/tasks/1714)
- **Date:** 2026-10-29, 08:25–09:15 UTC
- **Task:** `grain/049f1e09/task-1714` on `ai-on-gke/grain`, base `main` at `425375a` (origin: [Task #1549](/tasks/1549))
- **Environment:**
  - Debian 12.15 sandbox (`kubernetes-granule`), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.10`, Node `v20.19.0`, Playwright chromium from `/var/cache/playwright`
  - `ui/` built with `npm ci && npm run build`, then `go build -o ~/grain ./cmd/grain` from this branch, so the binary embeds the UI
  - `grain demo -github-sim -fake-ci -git-proxy -notifications -open=false -addr 127.0.0.1:7811 -access-code-file /tmp/wt/code`. `-notifications` is new in this change (F2). Steps 1–3 first ran without it, against a binary built from `main`.
  - Only GitHub is stubbed (`githubsim`). Sandbox runs were started with `POST /api/e2e/git-proxy/runs`, which mints the task-scoped sandbox token that a real run's in-sandbox `grain` CLI and MCP hops present to the git proxy. Every agent call below goes through that proxy (`/cli/notify`, `/cli/tasks/{id}/notes`, `/api/...`), authenticated as `sandbox:<token>`.

## Outcome

| Step | Result |
|---|---|
| 1. Build `ui/` and `cmd/grain`; start `grain demo -github-sim -fake-ci` | **Pass.** |
| 2. `notify_operator`: subject bumping, `clears_when` ttl and task, critical refused | **Pass after fix (F2).** A second raise of `walk-a` bumps the same row (`id 4`, `count 2`, `new:false`). The demo never ran the clear sweep, so a 1m ttl stayed open past its `at`. With `-notifications`, the task-bound notification cleared about 1s after task 21 closed and the ttl one cleared within one 2s cycle (`clearedBy: condition`). Critical is refused with 403 without `api:task-admin` and accepted (un-namespaced subject) with it. |
| 3. `message_task` child→parent accepted and lands in the parent's conversation; to an unrelated task refused; a note wakes a resting durable task; `GET /api/tasks/<other>/messages` with another task's token → 403 | **Pass after fix (F1).** Child→parent and parent→child both return 200, and the note shows on task 19's conversation as `task 20 (agent)`. Unrelated→parent returns 403 naming `api:task-admin`. **F1:** `GET /api/tasks/19/messages?agents=true` with task 21's token returned **200 with task 20's note**. With the fix it returns 403. Waking a resting durable parent is proven by a new Go test (see the note under step 3). |
| 4. Playwright desktop 1440×900 and phone 390×844: bell and `/inbox`, Ack / Clear / Investigate one each, Inbox chip count after each, screenshots | **Pass.** The counts after each action match the API, at both widths (table below). The UI has no Clear button, by design (see the note under step 4), so Clear was `POST /api/notifications/{id}/clear` and the page's own poll was watched. |
| 5. `ask_question` summary: Inbox shows the summary only, with the full text behind the expander; a 101-char or multi-line summary is rejected; "Asked"/"Waiting" on Board and List; `grain list -filter` on `asked` | **Pass, plus a small fix (F3).** 101 chars → 400 (`summary is 101 characters…`), multi-line → 400 (`summary must be a single line…`). The Inbox row for task 20 shows only `Keep the v1 API alongside v2, or remove it?`, and the body is hidden until **Show question** at both widths. The List and Board say Asked and Waiting. `grain list -filter state:any:asked` (and `-state asked`) lists the same 11 tasks as `awaiting_reply,awaiting_submit`. **F3:** neither `-state`'s help nor the unknown-state error named `asked`. Both now do. |

## Steps and observations

### 1. Build and start

```sh
cd ui && npm ci && npm run build && cd ..
go build -o ~/grain ./cmd/grain
~/grain demo -github-sim -fake-ci -git-proxy -notifications -open=false \
  -addr 127.0.0.1:7811 -access-code-file /tmp/wt/code
```

Tasks seeded through the operator API: 19 "Walk parent", 20 "Walk child" (`proposedBy: "19"`, so `generatedFrom: 19`) and 21 "Walk unrelated". Each was approved and given a run and token with `POST /api/e2e/git-proxy/runs {"taskId": "<id>"}`.

### 2. notify_operator

Raised through `POST <proxy>/cli/notify` with task 20's token, which is the hop `grain notify` and the MCP tool's daemon call both make (`AgentNotify`):

| Call | Answer |
|---|---|
| `{"subject":"walk-a","title":"Walk A first"}` | 200 `{"id":4,"subject":"agent.20:walk-a","count":1,"new":true}` |
| `{"subject":"walk-a","title":"Walk A again","body":"second raise"}` | 200 `{"id":4,…,"count":2,"new":false}`: one row, bumped, title updated |
| `{"subject":"walk-ttl",…,"clears_when":{"kind":"ttl","ttl":"1m"}}` | 200 id 5; stored as `{"kind":"ttl","at":"08:52:29.6Z"}` |
| `{"subject":"walk-task",…,"clears_when":{"kind":"task","task":"21","until":"closed"}}` | 200 id 6 |
| `{"subject":"walk-bad",…,"clears_when":{"kind":"nope"}}` | 400 `clears_when kind "nope": want task, base-green or ttl` |
| `{"subject":"walk-crit",…,"severity":"critical"}` | 403 `only a task holding api:task-admin may raise a critical notification…` |
| the same, after granting task 20 `api:task-admin` | 200 `{"id":7,"subject":"walk-crit","severity":"critical"}` (not namespaced) |

Closing task 21 at 08:51:29 cleared id 6 at 08:51:30 (`clearedBy: condition`). Id 5 cleared at 08:52:30, the first 2s cycle after its `at`. **Before F2**, on the same demo without the sweep, a 1m ttl notification (`at` 08:40:00) was still open at 08:43:15. See F2.

Note: `/cli/notify` takes `clears_when` in snake case. A body written `clearsWhen`, the spelling `POST /api/notifications` uses, was accepted and silently dropped: the notification was raised with no condition. That is a wire-format inconsistency between two internal hops rather than something an agent types (the MCP tool and `grain notify --clears-when` both fill the field correctly), so it is recorded here and not changed.

### 3. message_task and the messages read

| Call (token) | Answer |
|---|---|
| `POST /cli/tasks/19/notes` (20, child) | 200 `Left a note on task #19 (comment 13).`; on task 19's conversation as author `20`, kind `agent` |
| `POST /cli/tasks/20/notes` (19, parent) | 200 |
| `POST /cli/tasks/19/notes` (21, unrelated) | 403 `task 21 may only message its parent or its children, and task 19 is neither: message_task to any other task needs api:task-admin` |
| `GET /api/tasks/19/messages?agents=true` (19, own) | 200, the child's note |
| `GET /api/tasks/19/messages` (21) **before F1** | **200** `[]` |
| `GET /api/tasks/19/messages?agents=true` (21) **before F1** | **200** `[{"author":"task 20",…,"body":"hello parent from child 20"}]` |
| `GET /api/tasks/19/messages?agents=true` (20) after F1 | 403 `task 20 cannot read task 19's messages: … Reading another task's needs api:task-admin` |

Waking a resting durable task: a demo task cannot be brought to rest cleanly, because the demo runs no agents. A run started with `/api/e2e/git-proxy/runs` only ends when it is stopped, and a stop parks the task on "Turn stopped by user", which `WakeRestingTask` correctly leaves alone because a person owes it an answer. So this was proven in the store instead: `TestMessageTaskWakesARestingDurableParent` (new, `pkg/ui/notifications_test.go`) rests a `durable` parent deferred on its met goal, posts a child's note, and checks three things: the parent is no longer resting, `RetryRequestedAt` is set, and its conversation holds the note plus grain's "a note from task N" wake line. It passed on the unchanged code, so this is coverage, not a fix.

### 4. Bell and Inbox (Playwright)

`/tmp/wt/walk.mjs` (in the closing note) signs in with the access code, raises three warnings per viewport with `POST /api/notifications`, and does one action per notification:

- **Desktop:** Acknowledge from the Inbox row, then Clear via the API, then Investigate from the Inbox row.
- **Phone:** Acknowledge from the bell's panel, then Clear via the API, then Investigate from the Inbox row.

After each action it reads four values:

- the bell's `aria-label`
- the rail's Inbox chip (on phone, through **Open navigation**)
- the Notifications section's count
- `GET /api/notifications?open=1`

| Viewport / after | Bell | Inbox chip | Section | API open / unacked |
|---|---|---|---|---|
| desktop start | 7 active, critical | 20 (12 tasks, 8 notifications) | 8 | 8 / 7 |
| desktop Acknowledge | 6 | 20 (8) | 8 | 8 / 6 |
| desktop Clear | 5 | 19 (7) | 7 | 7 / 5 |
| desktop Investigate | 5 | 19 (7) | 7 | 7 / 5; task 22 filed; the button now reads "Investigation"; the page opened `/tasks/22` |
| phone start | 8 active, critical | 23 (12 tasks, 11 notifications) | 11 | 11 / 8 |
| phone Acknowledge (bell) | 7 | 23 (11) | 11 | 11 / 7 |
| phone Clear | 6 | 22 (10) | 10 | 10 / 6 |
| phone Investigate | 6 | 22 (10) | 10 | 10 / 6; task 23 filed |

This is what the design says (`docs/design/durable-tasks.md`, "Acknowledge is not clear"):

- Acknowledge lowers only the bell.
- Clear lowers the bell, the chip and the section.
- Investigate files exactly one task and changes no count.

Notes:

- The Inbox and the bell offer Acknowledge, Investigate and Open task, but no Clear. The design lists exactly those Inbox actions, and Clear is an API action (`notifications:write`) or a met `clears_when`, so this is not a bug.
- The page re-reads notifications every 30s (`POLL_MS`), so a clear made elsewhere shows up there within 30s. A 30s wait in the first phone pass raced it once; with a 45s wait it passed.

Screenshots in `/tmp/wt/shots`:

- At 1440×900 and 390×844: `{desktop,phone}-inbox-{start,after-ack,after-clear,after-investigate}.png`, `{desktop,phone}-bell-open.png`, `phone-bell-after-ack.png`, `{desktop,phone}-list.png`
- At 1440×900 only: `desktop-board.png`

They are combined into the contact sheets attached to the closing note.

### 5. Question summaries and Asked/Waiting

The chat route `POST /api/tasks/20/messages` was called as task 20, with `X-Grain-Acting-Task: 20`. That is the route the chat and `ask_question` tools reach. Over the sandbox CLI channel it is model-only.

| Summary | Answer |
|---|---|
| `"x"×101` | 400 `summary is 101 characters; keep it to one line of at most 100…` |
| `"line one\nline two"` | 400 `summary must be a single line: put the detail in the question itself` |
| `Keep the v1 API alongside v2, or remove it?`, body with a `## Background` heading and `FULLTEXT-MARKER` | 200; task 20 `awaiting_reply`, `questionSummary` = the summary |

`ask_question`'s own refusal of the same summaries is `pkg/mcp`'s `TestQuestionToolsRefuseABadSummaryAndTolerateAMissingOne`, which is green.

In `/tmp/wt/step5.mjs`, at both widths:

- The Inbox row's `.question-disclosure-summary` is exactly the summary.
- `FULLTEXT-MARKER` is not visible until **Show question** is clicked, and is visible after.
- The task list carries 11 `Asked` and 3 `Waiting` state labels (aria-label/title) and no raw `awaiting_reply` text.
- The Kanban board's columns are `Proposed, Waiting, Running, Asked, Failed, Merging`.

```
$ GRAIN_TOKEN=… grain -server http://127.0.0.1:7811 list -filter state:any:asked
4      awaiting_reply  acme/widgets  Investigate the merge queue stall
6      awaiting_submit acme/widgets  Cache the capability readiness probe
…      (11 rows; 11 for state:any:awaiting_reply,awaiting_submit too)
20     awaiting_reply  acme/widgets  Walk child
```

## Findings and fixes

### F1: a run could read any other task's messages, including other agents' notes to it

- **Repro (before):** with task 21's sandbox token, `curl -u sandbox:$T21 "$PROXY/api/tasks/19/messages?agents=true"` returns 200 and the note task 20 left for task 19.
- **Cause:** `agentRails` in `pkg/ui/agent_api_gate.go` returned early for every GET. No rule tied `GET /api/tasks/{id}/messages` to the calling task, and it is not in bwsalmon/grain's `main` either.
- **Fix:** that route (`ownMessagesReadRoute`) now takes only the acting task's own id, unless the acting task holds `api:task-admin`. That is the same bar as `message_task` to an unrelated task. A run's own addenda poll is unaffected.
- **Regression test:** `TestAgentAPIMessagesReadIsOwnTaskOnly` in `pkg/ui/agent_messages_test.go`. It checks 403 and no leak for another task with and without `agents=true`, 200 for the task itself, and 200 for a task-admin. It fails on `main`.

### F2: no clears_when could clear on a running demo

- **Repro (before):** on `grain demo -github-sim -fake-ci -git-proxy`, raise with `clears_when {"kind":"ttl","ttl":"1m"}`. It stays open indefinitely, and the same happens for a `task` condition after the task closes.
- **Cause:** the demo's loops (`-reconcile`, `-schedules`, `-review-questions`) never run the daemon's `notifications` reconciler.
- **Fix:** the sweep in `reconcileNotifications` is now `orchestrator.ClearMetNotifications(ctx, store, now)`. It needs only the store; the rest of the reconciler wants CI and config that a store-only demo `Deps` lacks. A new opt-in `grain demo -notifications` runs that sweep every `-reconcile` interval (default 2s). It is opt-in, like `-schedules`, because other specs count the seeded notifications.
- **Regression test:** `TestDemoNotificationsClearsAMetConditionOnItsOwn` in `pkg/demo/demo_notifications_test.go`. An expired ttl is cleared (`condition`) and an unexpired one stays open. The orchestrator notification tests still pass on the extracted function.

### F3: `grain list` did not say `asked` exists

The `asked` alias worked, but `-state`'s help listed only the nine stored states, and `-filter state:any:waiting` answered `one of proposed, …, closed`. Both now name `asked (awaiting_reply or awaiting_submit)`. The regression test is a new case in `TestConditionsRejectWhatNobodyCanActOn`.

### Recorded, not changed

- `/cli/notify` silently ignores a `clearsWhen` key, because its wire type uses `clears_when`; see step 2.
- The Inbox and bell polls run every 30s, so a clear made elsewhere takes up to 30s to show. That is by design.

## Outcome

All five steps pass on this branch. One authorization bug (F1) and one gap in the demo harness (F2) were fixed, each with a regression test, and the CLI help was made more discoverable (F3). Nothing is left for a follow-up.
