# Interactive-session message delivery and question parking — 2026-11-01

Walkthrough of [`docs/test-plans/2026-11-01-late.md`](../test-plans/2026-11-01-late.md) §1 (PR #289: `5fa17498`, `4d275302`, `22b9637f`, `a011eed4`, `8825f43f`, `d9b19ef1`). Task 2243, filed by task 2227.

## Environment

- **Code:** `main` at `48048dac` (it contains `20d79d0b`, PR #289). The fix below is on top of it, on `grain/049f1e09/task-2243`.
- **No sibling report.** `docs/test-runs/*interactive-sessions-warm*` (2026-11-01.md §5) does not exist yet, so there were no environment notes to reuse. `git log origin/main -- pkg/orchestrator pkg/ui/messages.go ui/src/components/TaskChat.jsx` shows no §2/§3 fixes after PR #289.
- **Agent backend: stub framework.** The sandbox has no agent credentials. The stub follows `tests/e2e/interactive_latency_test.go`, with one difference: every run forks the **real `grain mcpserver`** (built from the tree) against the daemon's HTTP API, using the same `-sandbox-root/-server/-task/-interactive/-addenda-after` arguments a framework passes. So every tool call, the addenda poller, `chat`'s long poll and `POST /api/tasks/{id}/delivered` go over the real sandbox-CLI path. Only the model is stubbed: the test scripts which tools are called and when.
- **Orchestrator:** the real store (embedded SQLite in a data dir), `ui.Server` and `daemon.ReconcileWith` loop. The run host is `orchestrator.NewHostSandboxes`, a host directory sandbox, **not** Docker or Granule.
  - Docker is installed in the sandbox, but a `GRAIN_RUNHOST_DOCKER=1` daemon needs a sandbox image this sandbox cannot pull or build in the time available.
  - §1 is about the conversation, not the sandbox; §2 covers the real run hosts.
  - Warm session retention is turned off in the harness (`SessionSandboxIdleTTL: -1`), because of finding 3.
- **Browser:** `consolerestserver` with the UI built into it (`cd ui && npm ci && npm run build`), Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), at 1440 and 390 px, light theme.
- **sqlite3** installed with apt. Reads were taken from a data dir kept with `GRAIN_WALKTHROUGH_DATA_DIR=/var/tmp/wt`.

The walkthrough is now committed as tests, so it can be re-run:
- `go test ./tests/e2e/ -run 'TestInteractive(MessageDelivery|QuestionParking|GoalFinalText|RestartBetween)' -v`. Each snapshot below is a `t.Logf` line from it.
- `npx playwright test e2e/console-session-wake-inbox-walkthrough.spec.js`.

In the tables, `delivered` is `task_observation.delivered_comment_id` and `pending` is `pending_question_comment_id`. Comment IDs are `task_comment.id`.

## "Not proved" 1 + 2 — the five timing windows (`TestInteractiveMessageDeliveryTimingWindowsWalkthrough`)

| Window | Comment | Before | After | Answered by |
|---|---|---|---|---|
| (a) before `chat` | 2 | running, delivered 0, pending 0 | `chat` returned it at once (pre-post poll); running, delivered 2, pending 3 (the chat message) | run 1's `chat` |
| (b) during `chat`'s wait | 5 | running, delivered 2, pending 3 | awaiting_reply, delivered 5, pending 0, 1 run | run 1's second `chat` |
| (c) during the last ordinary call (`run_command sleep 2`) | 8 | queued (kick 7), delivered 5 | run 2's `run_command` result **carried it**; run 2 ended with plain text; ProcessResult logged "a message was posted that the run was never shown" and left it queued; run 3 dispatched with `AddendaAfter=9`, comment in its prompt; then awaiting_reply, delivered 9 | run 2 (tool result) **and** run 3 (prompt): see finding 2 |
| (d) during the final `comment_on_issue` | 12 | queued (kick 11), delivered 9 | not consumed by the turn-ending call; running, delivered 9; then queued, not completed; run 5 got it in its prompt (`AddendaAfter=13`) | run 5 |
| (e) during ProcessResult (posted after the last tool call, as the result returned) | 14 | running, delivered 13 | queued; run 6 got it in its prompt (`AddendaAfter=15`); then awaiting_reply, delivered 15, 6 runs | run 6 |

The task never sat `awaiting_reply` with an undelivered human comment, and never went `completed`. The closing words `comment_on_issue` posted in (d) appear exactly once.

## "Not proved" 3 — question and secret parking (`TestInteractiveQuestionParkingWalkthrough`)

| Step | Comment IDs | State after |
|---|---|---|
| Run 2 calls `ask_question` | kick 3, Q1 4 | awaiting_reply, delivered 3, **pending 4** |
| `POST /delivered {"through":3}` (predates Q1) | — | 200; still pending 4 (guard holds) |
| Human reply R1 | 5 | queued, pending cleared |
| Run 3: human H posted, **then** run 3's final `ask_question` | H 6, Q2 7 | **before fix:** awaiting_reply, delivered 5, pending 7, with H never shown to any run (**finding 1**). **After fix:** queued, then run 4 dispatched with H in its prompt |
| Run 4 calls `request_secret demo-key` | request 8 | awaiting_reply, delivered 7, pending 8, pending_secret `demo-key` |
| `POST /delivered {"through":5}` (R1 predates it) | — | 200; secret request kept |
| Human reply R3 | 9 | queued, pending and secret cleared; run 5 answered; awaiting_reply, delivered 9 |

sqlite3 read of the kept data dir after the run (task 1):

```
task_comment: 2 hello (automation) | 3 kick run 2 (human) | 4 Q1 (automation) | 5 R1 (human)
              6 H (human) | 7 Q2 (automation) | 8 secret request (automation) | 9 R3 (human) | 10 thanks (automation)
task_observation: delivered_comment_id=9 pending_question_comment_id=NULL pending_secret=''
task_run: 1-1..1-5 succeeded
```

**Inbox.** `GET /api/notifications` was `[]` throughout: a parked question raises no notification row on its own. The Inbox *page* for a parked question was not rendered, because `consolerestserver`'s scripted provider can only answer in plain text and cannot call `ask_question`. In the browser, the Inbox was checked for the console session (below). This is the one part of item 3 left undone.

## "Not proved" 4 — a resting console session wakes on the first reply (`ui/e2e/console-session-wake-inbox-walkthrough.spec.js`)

On `consolerestserver`:
1. The Director session was rested with `POST /e2e/end-exchange`, so the real reconciler parked it again: awaiting_reply, 2 comments.
2. The Inbox at 1440 and 390 px showed **0** rows linking to the session's task. The two widths agree, and grain's own session note raises no Inbox row.
3. One reply was typed in the session composer at 1440 px. "Scripted answer 1." appeared **2.9 s** after Enter, with no second message.
4. `GET /e2e/prompts` grew by exactly one prompt, and that prompt contains the reply.
5. The task returned to awaiting_reply. The Inbox again showed 0 rows at both widths.

The run saves screenshots `rested-{1440,390}.png` and `answered-{1440,390}.png` to the test's output dir.

## "Not proved" 5 — goal tasks relay FinalText once (`TestInteractiveGoalFinalTextRelayedOnceWalkthrough`)

The task is interactive, with `goal: {kind: person-closes}`.
- Run 1 returned FinalText "goal answer one": relayed **1** time, task `deferred` (rests on its goal).
- Run 2 posted human comment 4 after its last tool call, then returned "goal answer two": relayed **1** time. ProcessResult left the task queued for the comment. Run 3 got comment 4 in its prompt and returned "goal answer three": relayed **1** time.
- End state: deferred, delivered 5, 3 runs, never completed.

## "Not proved" 6 — a restart between post and delivery (`TestInteractiveRestartBetweenPostAndDeliveryWalkthrough`)

1. The run was in a tool call when a human posted comment 2.
2. The daemon's HTTP API was then **closed**, so the addenda poller could not fetch the comment, and reopened 2 s later on the same address.
3. Snapshots: down: running, delivered 0. Back: running, delivered 0. After the run's next tool calls: delivered 2.
4. The comment showed up in **exactly one** tool result, and the run ended awaiting_reply with 1 run.

This restarts the API the run talks to, not the orchestrator process. Adopting the run across a full daemon restart is §2's subject.

## Findings

1. **Fixed: a comment posted during a run's final `ask_question` / `request_secret` left the task parked on an answer nobody would give.**
   - Turn-ending tools consume no addenda, so the run never sees the comment.
   - `ProcessResult` relayed the question and parked on it before any "message waiting" check. Only the chat path below it had that check.
   - The human comment predates the question, so `RecordDelivered`'s guard (correctly) can never un-park it on that comment. The task sat `awaiting_reply` until the person wrote again.
   - Reproduced deterministically: comment 6, question 7, `delivered_comment_id=5`, pending 7.
   - Fix in `pkg/orchestrator/finish.go`: after `relayParkingCalls` parks, re-check `interactiveMessageWaiting`. If a message is waiting, clear `PendingQuestionCommentID` and `PendingSecret` and leave the task queued. The question stays in the thread for the next run.
   - The change only applies to interactive tasks that keep a delivery mark, as `a011eed4`/`8825f43f` do.
   - Regression test: `TestInteractiveQuestionParkingWalkthrough`. It fails without the fix.
   - **`finish.go` is a trust-boundary path; the change needs a security review.**
2. **Observed, by design: a comment that rides on the last ordinary tool call's result is delivered twice** (window c).
   - `5fa17498` acknowledges addenda only at the *next* non-turn-ending tool call. A run that answers in plain text after the tool result never acknowledges it, so the task is re-dispatched and the next run is given the comment again.
   - That is at-least-once, never lost. The plan's "answered exactly once" holds only if the second run treats it as already answered.
   - Not changed here: `TestAddendaDeliveredOnFinalToolCallIsNotAckedUntilNextToolCall` pins this deliberately. Acknowledging when a non-turn-ending tool's result is returned would make it exactly-once, but that is a design call for the owners of `pkg/mcp/addenda.go`.
3. **Filed: the sandbox reaper deletes a warm or just-claimed session sandbox.**
   - Under `go test -race`, about 1 in 3 runs logged `reaped leaked agent sandbox 2-1 … run 2-1 already finished`.
   - Later runs of the same session were then handed sandbox `2-1`, and `grain mcpserver` exited 2 with `-sandbox-root … is not a directory`.
   - Cause: `reconcileSandboxes` in `pkg/orchestrator/reconcile_sandboxes.go` builds its keep set from separate, non-atomic snapshots (live runs, warm pool, InFlight). It also matches on the sandbox's first holder run ID rather than the run now using it.
   - Filed as a follow-up (depends on task 2243). The harness turns warm retention off until that lands.
4. **Seen, not a product bug:** `consolerestserver` serves a 404 for `/inbox` (and every page) unless the UI has been built into `pkg/ui/static` first. `global-setup.js` builds the server binary but not the frontend; CI's `ui e2e` job builds it beforehand.

## Outcome

- Items 1, 2, 4, 5 and 6 were walked and pass. Item 3 was walked through the API and store and passes after the fix. The Inbox view of a *parked question* at 1440/390 was not rendered, as explained above.
- One bug fixed with a regression test, one design observation, and one follow-up filed.
- Suites run:
  - `go test ./pkg/orchestrator/ ./pkg/ui/`: pass.
  - The four new `tests/e2e` walkthroughs: pass, 4× under `-race`.
  - The new Playwright spec: pass.
