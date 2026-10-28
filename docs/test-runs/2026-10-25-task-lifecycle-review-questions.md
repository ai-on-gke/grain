# Test Run — Task State & Orchestrator Lifecycle, Settings Bounds, `grain demo -review-questions` (2026-10-25 plan §7)

**Plan:** `docs/test-plans/2026-10-25.md` §7
**Run by:** task 933, 2026-10-26 00:10–01:00 UTC
**Branch:** `grain/049f1e09/task-933`, cut from `candidate` at `6d8f57485` (PR #4744)

## Environment

- Debian GNU/Linux 12 (bookworm) sandbox VM, kernel 6.1.0-54-amd64, 7 vCPUs, 15 GiB RAM.
- Go 1.26.10, Node v20.19.0, Playwright 1.62.1 (Chromium build 1234, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Frontend built with `npm --prefix ui run build`, binary with `go build ./cmd/grain`.
- `grain demo -addr 127.0.0.1:0 -open=false -review-questions -access-code-file …` on an ephemeral port (37203 at first, 43301 after the demo fix). Every request carried the access code as a bearer token.
- Exploratory drivers were one-off Playwright scripts (not committed). The regression coverage that came out of them is `ui/e2e/review-questions-blocked-walkthrough.spec.js`.

## Where each §7 item actually is

The plan names some commits and files that do not match the tree. The real state:

| Item | Plan says | Actually |
|---|---|---|
| Blocked oval | `a3bedb1b0` PR #4738; files `StatusPill.jsx`, `BoardView.jsx`, `ListView.jsx` | `a3bedb1b0` is right and is on `candidate`. Those files don't exist: the change is in `ui/src/state.js` (`isBlockedWaiting`, `taskBadgeState`), `TaskBoard.jsx`, `TaskList.jsx`, `DetailOverlay.jsx`, `GanttChart.jsx` and `style.css`. |
| `"Merging"` rename | `4dea55457` PR #4734 on `candidate`; `64b86a886` PR #4712 on `main` | **Not on `candidate`.** The rename is `aa753eb76` (PR #4709, `0970145ee`), and it exists only on `main`. `4dea55457`/PR #4734 is the `nothing_needed` fix, and `64b86a886`/PR #4712 is the `errDaemonStopped` fix. On `candidate`, `STATE_LABELS.completed` and the default board's `landing` column still read "Queued for merge" (`ui/src/state.js:70`, `ui/src/board.js:73`). |
| `nothing_needed` over an earlier branch | `da789c9fb` PR #4717 | It is `4dea55457` (PR #4734; `5b3eb480f` PR #4677 on `main`). `da789c9fb`/PR #4717 is the `errDaemonStopped` salvage fix. |
| `errDaemonStopped` preservation | `b5d7734c5` PR #4687 | It is `da789c9fb` (PR #4717; `64b86a886` PR #4712 on `main`). `b5d7734c5`/PR #4687 is the agy quota-reset model pause. |
| Settings bounds | `f4e5b3c3a` PR #4676 | Correct. `7cb1a29a9` on `main` is the blocked-oval merge, not the settings one. |
| `-review-questions` | `8b000f604` PR #4671 | `8b000f604` is the `-github-sim` push fix to the seed. The flag itself was already on `candidate`. |

## 1. `grain demo -review-questions`

The demo seeds task 13 ("Publish both daemon images", `awaiting_submit`, auto-merge off, a `review-questions` link) and its review, task 14 ("Review: Publish both daemon images", based on `grain/task-13`).

| Step | Result |
|---|---|
| `/inbox` lists the hold | **Pass.** A "review needs your decision (1)" group has task 13 with **reply** and **ship as is**. The card shows the file and line (`Makefile:41`), the question ("Should the core build also publish the google image?"), the recommended answer ("I recommend not: keep the core image core-only…") and the PR (`acme/widgets#106`). |
| Click through to `/tasks/13` | **Pass.** The pane opens over the inbox. The timeline has the review's comment; the state pane reads "awaiting submit — Review needs your decision…" and links Review: task 14. |
| Answer in the UI (Reply… box, **Comment**) | `POST /api/tasks/13/comments` was sent and the reply landed on the conversation. |
| Question resolves; task re-enters the queue | **FAIL → fixed.** Task 13 stayed `awaiting_submit` with its `reviewQuestions` hold indefinitely. See bug 1. |

After the fix, the same click path gives: task 13 is `queued` within one 2s tick, `autoMerge` is `true` again, the hold is gone, and the timeline gains "The owner answered the open questions from review task 14, so this task is back in the queue…". The inbox's "review needs your decision" group is empty. Nothing dispatches the task, since agents never run in the demo.

A cosmetic quirk I did not fix: the seed files task 13 at "now" and its completion 10 minutes earlier, so its timeline shows "Awaiting submit" (12:03) before "Proposed/Queued" (12:13).

## 2. Blocked oval and `"Merging"`

The seeded blocked task is #11 "Extract pagination query helper", `queued`, `blockedBy: ["8"]`.

| View | Before #8 closes | After `POST /api/tasks/8/close` |
|---|---|---|
| List (`/`) | `badge badge-icon badge-blocked`, title and aria-label "Blocked on: #8 (running)", no extra "Blocked" chip | `badge-queued`, "Queued" |
| Board (`/boards/<Board>`) | Card in the **queued** column wearing `badge-blocked`, title "Blocked on: #8 (running)" | No badge (the one-state Queued column needs none) |
| Detail (`/tasks/11`) | `.detail-state > .badge.badge-blocked`, text "blocked", title "Blocked on: #8 (running)" | `badge-queued`, "queued". The new spec shows this updates on the open page without a reload. |

**`"Merging"`: not testable on `candidate`.** The board's columns are proposed / queued / running / needs you / **queued for merge**. The sidebar's state row and the badge for `completed` say "Queued for merge" too. That is expected: the rename (`aa753eb76`, PR #4709) is on `main` and has not been ported. Per the plan's branch guidance I did not merge `main`. I checked the design on `main` (`git show aa753eb76`): it renames `STATE_LABELS.completed` and the default `landing` column, and migrates a stored "Queued for merge" title in `normalizeColumns`. "Merging" does appear on `candidate`, but only as the merge-queue phrase for `mergeQueueState: "merging"` (`MERGE_QUEUE_PHRASES`, `ui/src/state.js:500`).

## 3. Settings numeric bounds sweep

I swept every `input[type=number]` on the six pages: 34 boxes. Each got `-1`, `0`, `""`, a half-typed `1e` (which the browser reports as `badInput`) and a valid value, then a blur. The page was reloaded between tries. For each try I recorded the box's value after the blur, any `PUT /api/settings` sent, and any new alert text.

| Page | Boxes | Result |
|---|---|---|
| `/settings/agents` | Max agent turns (min 0) | -1 and `1e` are refused by label ("Max agent turns must be at least 0; -1 was not saved.", "…: what was typed is not a number, so it was not saved.") with no PUT. 0 and 2 save. |
| `/settings/tasks` | Max unreviewed follow-up depth, …per task (min 1) | -1, 0 and `1e` are refused by label with no PUT. `""` puts the stored value back. A valid value saves. |
| `/settings/engine` | Max worker agents, Max merges per repo branch, Merge batch size (min 1) | -1, 0 and `1e` are refused by label with no PUT. `""` saves the default (Max worker agents went 4 → 10; Merge batch size goes blank, meaning grain's default). Valid values save. |
| `/settings/ci` | CI jobs per run, CI output per run, 6 cache/artifact boxes (min 0) | -1 and `1e` are refused by label with no PUT. 0 and `""` mean grain's default. Valid values save. |
| `/settings/ci` | CI runs kept, CI step output kept (MiB), CI step output age (days) (no min) | -1 **saves**, which is by design: each box's helper text says "-1 keeps every run" (or every byte, or "bounds output by size alone"), and the server stores it raw. `1e` is refused by label. |
| `/settings/sandboxes` | Sandbox vCPUs/memory/disk, CPU/Memory overcommit, Host memory reserve, Disk floor share (max 1), Disk floor minimum | -1, `1e` and 1.01 on the share are refused by label with no PUT. Memory 2 MiB passes the client (min 0) and the server refuses it by label ("Sandbox memory (MiB) must be 0 (unset) or at least 128"), which leaves the box blank. That is acceptable, since the server error names the box. |
| `/settings/sandboxes` | **Auto CPU/memory minimum and maximum, CPU/memory pressure target** | **FAIL → fixed.** -1 and 0 were dropped with **no message**. `""` and `1e` left the box blank. See bug 2. |
| `/settings/caches` | Proxy / Actions cache max size (GiB) | -1 and `1e` are refused by label with no PUT. 0 and `""` mean the default. Valid values save. |

No out-of-range edit sent a `PUT /api/settings` anywhere, except the documented `-1`s on the three CI history boxes.

## Bugs found and fixed

### Bug 1: `grain demo -review-questions` never resolves an answered hold

**Repro (before):** start `grain demo -review-questions`, open `/inbox`, click "Publish both daemon images", type a reply, press **Comment**. `GET /api/tasks/13` stays `state: awaiting_submit, autoMerge: false, reviewQuestions: "14"` forever.

**Cause:** `orchestrator.SyncReviewQuestions`, the "review-questions" reconciler in `orchestrator.Reconcilers()`, is what takes an answered change off its hold. A daemon's cycle runs it; the demo never did.

**Fix (`7c70b8b0c`):** `-review-questions` now runs that one reconciler every `-reconcile` interval (default 2s), as `-schedules` does for its own (`runDemoReviewQuestions`, `pkg/demo/demo_review_questions.go`). Regression test: `TestDemoReviewQuestionsRequeuesOnTheOwnersReply` (`pkg/demo/demo_review_questions_test.go`). It seeds the hold, checks that an unanswered hold stays put, replies as a human, and expects `queued`, the hold dropped and auto-merge restored.

### Bug 2: the auto overcommit bounds skipped the shared number-box reading

**Repro (before):** on `/settings/sandboxes`, type `-1` into **Auto CPU minimum** and Tab away. The box goes back to 1 and nothing says why. `0.1` (under the box's 0.25 `min`) or `150` in **CPU pressure target (%)** (over its 100 `max`) went to the daemon. A half-typed `1e` (reported as `""`) counted as emptied, which saves grain's default whenever that differs from the stored value. Otherwise it leaves the box blank.

**Cause:** `onOvercommitFloatBlur` in `SandboxesSection.jsx` called `parseFloat` itself and checked only `n <= 0`. It was the one Settings number box that skipped `readNumberBox` (PR #4676).

**Re-sweep after the fix:** for all six boxes, -1, 0 and `1e` are refused by label with no PUT, and the box keeps its stored value. `""` keeps the stored value too, since each one equals its default in the demo. In-range values (1.25, 1.05, 1.5) save.

**Fix (`abac73447`):** the six boxes now go through `readNumberBox` with the setting's own minimum, maximum (from the input) and label. A refusal goes through the section hook's `refuseBox`, which `useSettingsSection` now returns, together with `unchanged`. Each refused value shows a labelled banner ("Auto CPU minimum must be at least 0.25; -1 was not saved."), sends no PUT, and puts the box back. A cleared box shows the default it stands for. I also renamed `settingsPlan.js`'s labels for the two pressure targets from "CPU/Memory PSI target (%)" to the on-page "CPU/Memory pressure target (%)".

Regression tests:
- `SandboxesSection.test.jsx`, "auto overcommit bounds": -1, 0, 0.1 and 150 are refused by name with no save; a `badInput` box is refused rather than saved as the default; a cleared box goes back to its default; an in-range value saves the whole setting. Six of the seven fail on the old handler.
- `ui/e2e/review-questions-blocked-walkthrough.spec.js`, its own `-review-questions` demo:
  - answering the inbox's review question re-queues the change;
  - the task pane's blocked oval clears to Queued live when its dependency closes;
  - the overcommit refusals show their labelled messages, keep the boxes as they were, and send no PUT.

  All three pass locally in Chromium.

**Screenshots** (`make ui-screenshots PAGES=/settings/sandboxes,/inbox`, desktop 1440×900 and phone 390×844, light and dark, base `6d8f57485` vs branch). The refusal was also shot after typing `-1` into Auto CPU minimum. Before: no banner at any width. After: the banner "Auto CPU minimum must be at least 0.25; -1 was not saved." at all four combinations. A bounding-box check puts it at the bottom centre (desktop 440×48 at x=500; phone 351×68 at x=20), inside the viewport, with no horizontal overflow at either width. The sandbox has no Gemini key, so `LOOK=1` was unavailable; the shots were checked with these geometry assertions rather than by eye.

## The two orchestrator items (CI-proved; no live walkthrough needed)

`go test ./pkg/orchestrator -run 'NothingNeeded|Interrupted|DaemonStopped'`: all 13 pass. They include `TestProcessResultNothingNeededWinsOverABranchAnEarlierAttemptPushed`, `TestProcessResultNothingNeededDoesNotDiscardANewPushOnAnEarlierBranch`, `TestRunCycleStoppedMidRunWithAPushedBranchDoesNotSalvageOrOverwriteInterruptedOutcome` and `TestADaemonStoppedMidRunKeepsItsConversationAndTakesNoStrike`. The demo cannot drive either path, because it never dispatches a run.

## Left open

- The `"Merging"` rename (`aa753eb76`, PR #4709) is still `main`-only. Its port to `candidate` will need this walkthrough's Merging checks run again once it lands.
