# Test run: dispatch-control, manual mode and Run now (2026-10-25 plan §1)

- **Plan:** `docs/test-plans/2026-10-25.md` §1
- **Run by:** task 927, 2026-10-26 UTC
- **Branch under test:** `candidate` at `ce051c1c9` (task branch `grain/049f1e09/task-927`). `candidate` was then rewritten. On the second attempt the three commits here were cherry-picked onto `candidate` at `253bee735`, and the Go tests, the unit tests and the Playwright spec were run again there, all passing.
- **Main-only items inspected on:** `origin/main` at `66c5a157a`, read with `git show`. `main` was not merged into this branch.
- **Environment:** grain sandbox VM (Debian bookworm), Go and Node from the image, Playwright Chromium from `/var/cache/playwright`. The frontend was built with `npm --prefix ui run build`. The hand-driven steps ran against `go build -o /var/tmp/grain ./cmd/grain` running as `grain demo -addr 127.0.0.1:0 -open=false -access-code-file …`, which uses a throwaway real SQLite store (`pkg/demo/demo.go`, `sqlite.Open`). The spec runs over `ui/e2e/fixtures.js`'s per-worker demo, using the `grain_e2e` build from `ui/e2e/global-setup.js`. API calls used `Authorization: Bearer <access code>`. `LOOK=1` was not available: the sandbox has no Gemini key.

## Summary

| # | Step | Outcome |
|---|------|---------|
| 0 | The plan's names against the tree | **The plan describes an API that does not exist** (below). The walkthrough was run against what shipped. |
| 1 | Pause, then Run now from the task pane | **Pass** |
| 2 | Run now on a closed, running, proposed, parked, deferred or blocked task | **Pass, as designed**: closed and running are refused with 400. A person may Run now every other state on purpose. |
| 3 | The list's "paused — waits for Run now" chip | **Bug, fixed**: it stayed on a row after Run now was pressed, and showed on blocked tasks |
| 4 | "Run selected now" over several rows | **Pass**, with the fix in 3 for what the rows say afterwards |
| 5 | Creating a non-session task holding `dispatch-control` | **Accepted, as designed**: the grant is per task or per session. `ErrDispatchControlRequiresSession` does not exist. |
| 6 | Live `grain mcpserver` with `dispatch-control` over JSON-RPC stdio | **Pass**: all four tools, the refusals, attribution, and revoking the grant mid-session |
| 7 | MCP-made changes shown in the browser | **Pass**: `/system/pipeline`, the alerts panel, the list row and the task's thread |
| 8 | `system:pipeline-steward` and scoped OpenAPI schemas (PRs #4645, #4642) | **Ported by `253bee735`** (`2b2dc20b5`). Its Go tests pass. It is a standing schedule, not a session, and `grain demo` does not seed it. |

## 0. What the plan describes, and what shipped

Every name below is from the plan. `git grep` finds none of them under `pkg`, `ui/src` or `cmd` on either `origin/candidate` or `origin/main`:

| Plan says | What there is |
|---|---|
| `agentManualMode` setting, `"Agent manual mode"` toggle in `/settings/tasks` | No setting. Manual mode means pausing the pipeline (`POST /api/pipeline-pause`, `grain pause`, the banner, `/system/pipeline`) and then pressing Run now (`docs/manual-mode.md`). |
| `GET /api/tasks/dispatch-state` | `GET /api/pipeline-pause`, answering `{pause: {paused, since, reason, byKind, byId, runNowPasses}}` |
| `POST /api/tasks/run-now` with `{ids: [...]}` | `POST /api/tasks/{id}/run-now`, one task per call. "Run selected now" makes one call per row (`BatchActionsBar.jsx`). |
| MCP tools `get_dispatch_state`, `set_agent_manual_mode`, `dispatch_now`, `hold_merge_queue` | `pause_pipeline`, `resume_pipeline`, `run_task_now`, `defer_task` (`pkg/mcp/dispatch_control_tools.go`). Nothing in dispatch-control holds the merge queue. |
| `grain mcp-sandbox --capabilities dispatch-control --control-url …` | `grain mcpserver -sandbox-root … -server <url> -task <id> -grant dispatch-control`, with the access code in `GRAIN_TOKEN` |
| `pkg/capabilities/builtin/builtin.go`, `pkg/mcp/sandbox/tools_dispatch.go`, `pkg/ui/agent_manual_mode.go`, `pkg/ui/console_sessions.go` | `pkg/capability/dispatchcontrol/dispatchcontrol.go`, `pkg/mcp/dispatch_control_tools.go`, `pkg/daemon/mcpserver.go` (`daemonDispatchControl`), `pkg/ui/client.go` (`Client.RunNow`), `pkg/ui/pipelinepause.go` |
| `ErrDispatchControlRequiresSession`: ordinary tasks may not hold it | The grant is per task *or* per standing session, and admitted like any other capability (`docs/manual-mode.md`). Step 5 confirms an ordinary task can be filed with it. |
| "Run now" on board cards; `BoardView.jsx`, `ListView.jsx`, `TaskDetailPanel.jsx` | Run now is a button on the task pane only (`detail/Actions.jsx`). The list (`/`, `TaskList.jsx`) shows the "paused — waits for Run now" chip and has "Run selected now" in its batch bar. Board cards have neither. |
| Run now "does NOT appear on blocked, proposed" tasks | It is offered on every state except closed and running, on purpose. A person's Run now approves a proposal, undefers a deferred task, and starts a blocked one (the comment on the button in `Actions.jsx`; `Client.RunNow`). |
| `/merge-queue` page | No such route (also noted in `2026-10-25-merge-on-green-base-repair.md`). |

The commits the plan cites as manual mode (`17eb0d1b1`, `61060c99f`, `5d5ca0045`, `f57fe5a1f`) are other features: the scoped agent API, held-for-approval marks, `wait_for_task resume_at`, and `propose_task hold_for_approval`. The feature under test came in with `020388609` (PR #4690, "let Run now through an operator's pipeline pause"), and the earlier dispatch-control work with PRs #4630 and #4680.

## 1–4. API and browser walkthrough

### Hand-driven, against `grain demo` (`curl`)

Setup is the seeded store plus:

- task 13, filed with `capabilities: ["dispatch-control"]`, then approved;
- task 14, filed with `dependsOn: ["2"]`, then approved (`queued`, `blocked: true`);
- tasks 15 (asking for `gcp-key`) and 16 (no capabilities), both proposed.

```
POST /api/pipeline-pause {"reason":"walkthrough: hand-picking"}
→ 200 {"pause":{"paused":true,"reason":"walkthrough: hand-picking","byKind":"human","byId":"debian","runNowPasses":true},"pausedNow":true}

POST /api/tasks/2/run-now   (queued)          → 200, state queued
POST /api/tasks/1/run-now   (proposed)        → 200, approved and queued
POST /api/tasks/4/run-now   (awaiting_reply)  → 200, a person may un-park
POST /api/tasks/9/run-now   (deferred)        → 200, undeferred and queued
POST /api/tasks/3/run-now   (running)         → 400 {"error":"3 is already running: an agent is on it now"}
POST /api/tasks/10/run-now  (closed)          → 400 {"error":"10 is closed: reopen it before asking for a run"}
POST /api/tasks              {"capabilities":["dispatch-control"],…} → 201 (task 13)
```

`GET /api/tasks/2` then carried `runNowAt`, and its thread had the note "Asked for this task to be picked up now, ahead of the queue…". `grain demo` runs no orchestrator, so no task actually goes to `running`. The walkthrough checks the request (`Observation.RunNowAt`), which `dispatch.Cycle` reads. The dispatch side is covered by `pkg/dispatch/runnow_test.go`.

### Bug: the list's chip asked for a Run now already made

**Reproduction.** On `ce051c1c9`, with the setup above, the deployment paused, and Run now pressed on tasks 1, 2, 4, 9, 11 and 16, load `/`. Rows 1, 2, 4, 9, 11, 13, 14 and 16 all show "paused — waits for Run now". Only task 13 is really waiting:

- the six with Run now pressed still carry the chip;
- task 14 is blocked on task 2, so lifting the pause would not start it either.

**Cause.** `heldForRunNow` (`ui/src/alerts.js`) skips a task whose `runNowAt` is set. But `runNowAt` was only on `ui.TaskDetail`, the pane's shape. `GET /api/tasks` rows never had it, so on the list the check could never succeed. The comment on the field said it was left off the list because it "lasts one cycle at most". That is false in exactly the case manual mode creates: with no capacity free, a request waits as many cycles as it takes, and the row kept telling the operator to press a button they had already pressed. The POST answer from `/run-now` is the list shape too, so it never showed the request either. `heldForRunNow` also never looked at `blocked`.

**Fix** (`e9084a306`):

- `RunNowAt` moves from `ui.TaskDetail` onto `ui.Task`. The JSON name is unchanged, so the detail is the same on the wire.
- `Client.ListTasks` fills it from one new query, `Store.RunNowRequested`, which works like `MergeQueueBlocked`. `Client.Task`, the row a POST answers with, fills it from the observation it already reads.
- `heldForRunNow` returns false for a blocked task.

After the fix, the same setup shows the chip on task 13 only, and `POST /api/tasks/2/run-now` answers with `runNowAt` set.

**Regression coverage:**

- `pkg/ui/run_now_test.go` `TestRunNowIsOnTheListShape`: the list row and `Client.Task` carry the request, and a task nobody asked for carries none;
- `ui/src/alerts.test.js`: a blocked queued task is not held for Run now;
- the Playwright spec below. Its first test fails on the old code at the blocked-row check, and passes with the fix.

### Playwright: `ui/e2e/manual-mode-dispatch-control-walkthrough.spec.js`

The spec gets a demo of its own (`demoArgs: ["-open=false", "-as", "manual-mode-walkthrough"]`) and resumes the pause in `finally`.

1. **Run now and Run selected now under a pause, and the rows that say so.** It files and approves A, B and C, plus a fourth task blocked on A, then pauses the deployment.
   - The alerts panel shows "Paused — only tasks you Run now will start".
   - A, B and C show the chip. The blocked row and the seeded running row do not.
   - On `/tasks/<A>`: the held hint shows, and clicking **Run now** answers `POST /api/tasks/<A>/run-now` with 200 and `runNowAt` set. The pane then says "Asked for a run.", the button is disabled, and the hint is gone.
   - The running task's pane has no Run now button.
   - Back on `/`, A's chip is gone. Ticking B and C and clicking **Run selected now** sends two run-now POSTs, both 200, and both chips go. `GET /api/tasks` shows `runNowAt` on A, B and C and not on the blocked task.
   - Run now on the running and the closed seeded tasks answers 400 ("already running" and "is closed").
2. **dispatch-control over a live grain mcpserver** (section 6).

Result: 2/2 passed (`npx playwright test e2e/manual-mode-dispatch-control-walkthrough.spec.js --project=chromium --workers=1`, 27 s).

## 5. `dispatch-control` on an ordinary task

`POST /api/tasks {"title":"Steward stand-in","capabilities":["dispatch-control"],"repo":"acme/widgets"}` → 201. This is the shipped design, not a gap: `docs/manual-mode.md` says the capability is "granted per task or per standing session … It goes through the same capability admission as any other grant". The plan's `ErrDispatchControlRequiresSession` was never written. Whether only sessions should be allowed to hold it is a product decision, and this run did not change it.

## 6. Live `grain mcpserver` over JSON-RPC stdio

The command was `GRAIN_TOKEN=<code> grain mcpserver -sandbox-root /var/tmp/demo/sbx -server http://127.0.0.1:<port> -task 13 -grant dispatch-control`. A Python driver wrote `initialize`, `notifications/initialized`, `tools/list` and then `tools/call` lines to it, and read the answers back:

```
tools/list → defer_task, pause_pipeline, resume_pipeline, run_task_now (among the rest)
resume_pipeline {}                         → "The pause is lifted: the queue dispatches as usual again."
pause_pipeline {"reason":"steward: hand-picking the release fixes"}
                                           → "The deployment is paused: … Nothing new dispatches except tasks started with run_task_now (or a person's Run now)…"
run_task_now {"task_id":"11"}              → "Task 11 is asked to run now. The deployment is paused (…), and this request passes that pause…"
run_task_now {"task_id":"3"}  (running)    → isError: "3 is already running: an agent is on it now"
run_task_now {"task_id":"7"}  (awaiting_reply)
                                           → isError: "7 is waiting for a person's reply, and agent \"13\" may not start it in their place…"
run_task_now {"task_id":"15"} (proposal asking gcp-key)
                                           → isError: "15 is not approved and asks for capability gcp-key, which task 13 does not hold itself…"
run_task_now {"task_id":"16"} (plain proposal) → approved and asked to run
defer_task {"task_id":"12"}                → "Task 12 is deferred until somebody picks it back up."
```

Then:

- `GET /api/pipeline-pause` showed `"byKind":"agent","byId":"13","runNowPasses":true`.
- The Run now note on task 11 is authored by `13` (`authorKind: agent`).
- **Revocation mid-session.** One live server process called `run_task_now` successfully. Between two calls the grant was removed with `POST /api/tasks/13/capabilities {"id":"dispatch-control","attach":false}`. The next call in the same process answered "task 13 no longer holds dispatch-control". A server started after the revocation does not offer the four tools at all.

The plan's `hold_merge_queue` has no counterpart, so the merge-queue half of the plan's step 3 could not be run.

## 7. MCP changes in the browser

The spec's second test does this unattended:

- It files a steward task holding `dispatch-control`, a target task, and a proposal asking for `gcp-key`.
- It runs `grain mcpserver` through `pause_pipeline`, `run_task_now` on the target (accepted), on a running task (refused) and on the proposal (refused).
- It checks that `/api/pipeline-pause` names the agent, and that `/system/pipeline` shows the reason and "Paused — only tasks you Run now will start".
- On `/`, the target row carries no chip and the steward's row does. The target's last comment is the steward's.
- A second server process calls `resume_pipeline`, and `/system/pipeline` no longer shows the pause.

## 8. `system:pipeline-steward` and scoped schemas

On the first attempt (`ce051c1c9`) neither `90d2abfdd` (PR #4645) nor `b5591b288` (PR #4642) was on `candidate`. After the rewrite, `candidate` at `253bee735` has the steward (`2b2dc20b5`, `pkg/model/pipeline_steward_template.go`, `pkg/orchestrator/pipelinesteward.go`, `docs/pipeline-steward.md`). What it is differs from what the plan says:

- It is a seeded **standing schedule** (`system:pipeline-steward`, every `PipelineStewardEveryNMinutes` = 30 minutes, one task woken again and again), not a console session beside `system:director`.
- It gets `dispatch-control` as one of its agent API presets (`PipelineStewardAPIPresets = {"task-admin", "dispatch-control", "ci-read"}`), not through a console-session rule.

`grain demo` runs no orchestrator, and `reconcileSystemSchedules` is the orchestrator's, so the schedule is not seeded in the demo (`GET /api/schedules` and `GET /api/templates` list no steward). Its coverage is therefore the Go suite. `go test ./pkg/orchestrator/ ./pkg/model/ -run 'Steward|Standing'` passes, including:

- `TestSystemSchedulesSeedsThePipelineStewardEveryHalfHour`
- `TestThePipelineStewardHoldsTheDocumentedPresetsAndNothingDangerous`
- `TestAQuietQueueCostsThePipelineStewardNoRun`
- `TestAConfirmedRedBaseBranchWakesThePipelineSteward`
- `TestWakePipelineStewardWakesTheIdleStandingTask`
- `TestWakePipelineStewardLeavesADisabledScheduleAlone`

A live walkthrough of a steward run needs a real daemon with an orchestrator and is out of this demo's reach.

## Screenshots

`/` with the deployment paused and the setup above, before (`ce051c1c9`) and after the fix. Both are at desktop (1440×900) and phone (390×844), in light and dark. The after set adds `/tasks/13` (held hint), `/tasks/14` (blocked, no hint) and `/system/pipeline`. They are attached to the task's closing note as contact sheets. The chip check above was made from the DOM (`.chip-held-for-run-now` per `.task-row`), not by eye: this sandbox's file reader shows PNG bytes, and there is no Gemini key for `LOOK=1`.

## CI

Grain CI on the pushed branch: the full `tests.yml` suite was run with `wait_for_checks` on the rebased head. See pull request #4793.
