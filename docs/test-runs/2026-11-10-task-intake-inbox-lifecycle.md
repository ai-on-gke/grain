# Task intake, Inbox and lifecycle walkthrough — 2026-11-10 (task 3931)

- **Date:** 2026-11-10, 04:02–04:45 UTC, one run.
- **Task:** task 3931, branch
  `grain/098073e4/task-3931-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `61668e58` ("Merge pull request #1058"), binary
  `v0.0.0-20261110030527-61668e582600`. The fixes below were checked again on
  a rebuild of the same commit with them applied.
- **Focus:** the earlier reports in this series (most recently
  `2026-11-09-task-intake-inbox-lifecycle.md`) cover this area in depth. This
  run walked the whole operator path again on the current `main` and spent
  most of its time on what changed since `e764c09d`: pending releases moved
  out of Notifications into their own "Asked: approve a release" group with
  Allow breaking / Allow short soak boxes (`651664f3`, `b51a7100`), repository
  grants in the GrantsPicker (`b5517814`), token usage on the timeline
  (`e07c18fa`), and the config-change card's refusal handling (`986f606c`).
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -qualification -mechanical-port -config-change
-access-code-file <file>`. `TMPDIR` was set to a fresh scratch directory
    on every start, so the store was a throwaway embedded SQLite file. The
    server was restarted on a fresh store twice: once after step 1.5's
    accidental Acknowledge all, and once to load the rebuilt frontend.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each one signs in through the Access code page and then acts
  only through visible controls. Each page recorded uncaught page errors,
  console errors and warnings, failed requests, `/api/` responses of 400 or
  more (with bodies), every native dialog (its text, and whether it was
  accepted), and every non-GET `/api/` request with its body. The REST API
  (bearer access code) was used to read back what a gesture had persisted,
  and for the setup listed under Stubs.
- **Console baseline:** every sign-in logs one `401` for the
  unauthenticated first load. That is expected and is left out below. No
  uncaught page error was recorded in any script.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **A template with a priority** (`Urgent fix` at 8):
   `POST /api/templates`. The demo seeds no template with a priority.
2. **Task-list read failures:** a Playwright route on `GET /api/tasks`
   answering `500 {"error":"store unavailable"}`, switched on for a cold
   load and, separately, mid-session. Everything else passed through.

Nothing else was stubbed. Host-script approvals, pending secrets, suspended
sessions, cross-team approvals, `request_access` cards and a grain CI verdict
with Re-run CI need rows that only a run or a reconciler writes. The
2026-11-08 reports covered them with sqlite stubs, and this run did not
repeat them (see Not covered).

## Steps and outcomes

### 1. Inbox

| #   | Step                           | Outcome                                                                                                                                                                                                                                                                                                                                                                                                         | Verdict      |
| --- | ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | Groups and counts              | The page shows Notifications 3, Asked: approve a release 1, Asked: reply 4, Review needs your decision 1, Asked: submit for merge 10, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4 and Approve a proposal 3. These add up to the rail's Inbox 28. The demo seeds no base-branch repair, local-script or config-repo sync rows.                                                                | PASS         |
| 2   | Collapse by keyboard, reload   | Notifications starts collapsed (`aria-expanded=false`) with its 3 still shown. `Enter` on its toggle expands it, and `Space` on Approve on GitHub collapses that one. After a reload both stay as they were left.                                                                                                                                                                                               | PASS         |
| 3   | Notifications content          | Rows are the human-review hold on #30 (Acknowledge / Investigate / Open task), the flaky webkit e2e (already acknowledged: Investigate / Open task) and the stalled merge (Acknowledge / Investigate / Open task). The pending release is no longer here: it has its own group (row 1.9).                                                                                                                       | PASS         |
| 4   | Stalled sub-cases and controls | #5 "on merge queue" (no button: the note says which part of grain should move it), #21 "ready" → Run now, #22 "live run" → Cancel, #23 "wake time" → Undefer. Run now sent `POST …/run-now`, and #21 left the group (4 → 3). Undefer moved #23 to `queued` (3 → 2). Cancel asks first: dismissing it left #22 `running`, and accepting closed it and took it out of the group.                                  | PASS         |
| 5   | Bulk confirmations             | Defer all (3) and Acknowledge all (4) each ask first, and dismissing either one changes nothing. On the first store my dialog answers were queued in the wrong order, so the accept meant for Cancel went to Acknowledge all. That closed #4, #7, #35 and #36 and removed the group, which is the confirmed path working as designed. The server was restarted on a fresh store for the remaining steps.        | PASS         |
| 6   | Approve a proposal             | Approve on #1's row → `queued`, `approved: true`. The group goes 3 → 2. #12 and #34 are untouched.                                                                                                                                                                                                                                                                                                              | PASS         |
| 7   | Answer the parked question     | On #4: Show question, then Reply. The text "Only unblock the merge queue; leave the lint PRs." and Send → `POST /api/tasks/4/comments`. #4 goes `awaiting_reply` → `queued` and leaves Asked: reply (4 → 3). The reply is on #4's timeline. (`Ctrl+Enter` in the box does not send; Send does.)                                                                                                                 | PASS         |
| 8   | Ship as is, Submit, Retry      | Ship as is on #28 → `POST …/28/submit`, and the review-decision group empties. Submit on CL #15 (presubmit passed, LGTM) → `POST …/15/submit`, and the submit group goes 10 → 9. Submit is offered only on #6, #15 and the other submittable rows; #14, #16 and #17 instead say why they are not submittable. Retry merge on #25 → `POST …/25/retry-merge`, and Unblock a merge empties. The rail goes 26 → 23. | PASS         |
| 9   | Pending release                | The row reads "soaked 25h10m of 24h" with approve / reject and the two boxes. Ticking Allow short soak relabels the button "approve short soak". Approving asks first and sends `{"short_soak_ok":true}`, and the group goes away. The question it asked was **"…It soaked 25h10m of 24h, under the minimum."**, which is false. That is Bug 1.                                                                 | FAIL → fixed |
| 10  | Cold `/api/tasks` failure      | With stub 2 on from the first load, the Inbox says "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own." It never claims nothing is waiting, and it still lists the release and Notifications groups that do not depend on the task list.                                                                                                          | PASS         |

### 2. New task

| #   | Step                        | Outcome                                                                                                                                                                                                                                                                                                                                                                               | Verdict      |
| --- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | Create menu                 | The rail's Create options offers new task / new session / new board.                                                                                                                                                                                                                                                                                                                  | PASS         |
| 2   | Task priority               | `0`, `-2` and `2.5` disable Create task and show "a whole number, 1 or more; default 5, higher numbers run first". `1e3` is accepted and files as `priority: 1000` (#38).                                                                                                                                                                                                             | PASS         |
| 3   | Fork head label             | With Create a new branch unticked, `octo:feature-x` is explained ("GitHub fork head: branch feature-x on octo's fork. grain files this task against the open pull request on acme/widgets it heads."). `a:b:c` is refused ("has more than one ':'") and holds Create disabled. `bad owner:x` is not caught in the browser; the server refuses it with a 400 that the form shows (O2). | PASS         |
| 4   | Add grant… / Escape         | The menu lists Repository access (`repo#read`, `repo#write`) and the Grain API presets with their scopes, with deployment-ops marked dangerous. `Escape` closes only the menu and leaves the dialog open.                                                                                                                                                                             | PASS         |
| 5   | Queued path                 | Title, description, repo acme/widgets, owner alice@example.com, auto-merge unticked, Don't port ticked, priority 7 → #37 `queued`, `priority 7`, `autoMerge false`, `noPort true`, `approved true`.                                                                                                                                                                                   | PASS         |
| 6   | Proposed path               | Queue immediately unticked → #38 `proposed`, priority 1000.                                                                                                                                                                                                                                                                                                                           | PASS         |
| 7   | `/new?field.` link          | `?field.area=billing&field.requested_by=ops-team&field.nonexistent=zzz` shows "This link asked for values this form has left out: Area: “billing” is not one of its values; “requested_by” is not a field on this deployment; “nonexistent” is not a field on this deployment." (The field is `requested-by`, so the notice is right.)                                                | PASS         |
| 8   | Off-allowlist repo          | Repos → Other… → `acme/gizmos` shows "acme/gizmos is not on this deployment's target repo list. the task will be filed but parked, …".                                                                                                                                                                                                                                                | PASS         |
| 9   | Cancel                      | Close dialog on a filled form files nothing.                                                                                                                                                                                                                                                                                                                                          | PASS         |
| 10  | From a template, with draft | Template page → New task from this template opens `/new` at priority 8. Then: type a title and reload. The draft gave the title back but showed priority **5**, and Create task filed a plain task at 5 with none of the template's settings. That is Bug 2. On the fixed build the reload keeps priority 8 and the task files at 8 with `templateId`.                                | FAIL → fixed |
| 11  | Base branch prefill         | On a fresh store, the template's task in step 10 was filed against **`grain/task-30`**, the branch of seeded review task #31. Nobody typed it. That is Bug 3 (hardening).                                                                                                                                                                                                             | FAIL → fixed |

### 3. Tasks list

| #   | Step                    | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                    | Verdict |
| --- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| 1   | Filters                 | `admission:any:durable` and `…:bypass` give 0 rows with "No tasks match these filters." (the demo seeds none). `standard` gives all 36. `origin:any:imported` gives #12. `state:any:nosuchstate` and `origin:any:external-agent` each mark themselves "(unknown)" and match nothing (the Origin value is `external`). `repo:any:ACME/WIDGETS` gives 28, so matching is case-insensitive. `owner:any:me` gives 34.                          | PASS    |
| 2   | Search, reload, history | "retry budget" → `/?q=retry+budget`, 6 rows. These survive a reload with the box still filled. Back returns to the previous filter, and Forward to the search again.                                                                                                                                                                                                                                                                       | PASS    |
| 3   | Row chips               | #11 "blocked on task", #12 `imported` + "unapproved", #25 "Merge failed / merge blocked", #37 the `p8` badge.                                                                                                                                                                                                                                                                                                                              | PASS    |
| 4   | Load failures           | Cold (stub 2): "The tasks could not be loaded (store unavailable)." with Retry, and Retry with the stub off brings back 36 rows. Mid-session: the 36 rows stay, a "not refreshing" chip appears, and it clears by itself once the poll succeeds again. The console logs `grain: poll failed` warnings, as designed.                                                                                                                        | PASS    |
| 5   | Batch bar               | With #12 and #34 selected, the bar offers approve, run selected now, submit, retry merge, defer, undefer, close, reopen and bulk edit…. In Bulk edit, ticking Task priority and typing 0 disables Apply with the shared wording; 9 applies to both, and the selection then clears (O3). Close asks first, and dismissing it keeps both `proposed` and selected. Defer → both `deferred`. Undefer from the Deferred view → both `proposed`. | PASS    |
| 6   | Manual mode             | System → Pipeline → Pause pipeline asks ("Pause the pipeline? …") and pauses (`runNowPasses: true`). Rows 37, 2, 21, 23, 27, 29 and 31 carry "paused — waits for Run now". Run selected now on #37 → `POST …/37/run-now`, and after a reload #37 no longer carries the marker. Resume asks and sends `DELETE /api/pipeline-pause`.                                                                                                         | PASS    |

### 4. Task page

| #   | Step                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                            | Verdict |
| --- | ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Pause / Resume           | On #2, Pause → `paused.fromState` recorded with the state unchanged. After a reload, Resume clears it.                                                                                                                                                                                                                                                                                             | PASS    |
| 2   | Defer with date, Undefer | Until 2026-11-12 + Defer → `{"deferredUntil":"2026-11-12T00:00:00.000Z"}`, and the hint reads "Picks itself back up on 11/12/2026, 12:00:00 AM, onto the queue." Undefer → `queued` with the date cleared.                                                                                                                                                                                         | PASS    |
| 3   | Close / Reopen at 390 px | Close → `closed` (`{"close_pull_request":false}`), Reopen → `queued`. At 390 px the timeline shows both, and the page is 390 px wide (`scrollWidth`).                                                                                                                                                                                                                                              | PASS    |
| 4   | Token usage              | #4's timeline reads "107k tokens (93k in, 14k out, 580k cached) across 1 run".                                                                                                                                                                                                                                                                                                                     | PASS    |
| 5   | Dependencies             | On #37, Add a dependency… → `2` → "Fix flaky retry test" → `{"id":"2","attach":true}`. The chip reads "2 fix flaky retry test (open)". The rail's Blocked count goes 1 → 2, and the Blocked view lists 37 and 11. `Delete` on the focused chip detaches it, and Blocked goes back to 1.                                                                                                            | PASS    |
| 6   | Config-change card       | #35 shows its branch at `19921abc0351`, the file, "Schema valid · Capability catalogue consistent · Dispatch liveness OK · Dry-run apply succeeded" and a side-by-side diff (`poll_interval_ms` 10000 → 30000). Approve & apply sends its `headSha`, and #35 goes to `queued`. On #36, Reject → "Why are you rejecting it?" → Reject change sends the reason, and after a reload the card is gone. | PASS    |
| 7   | Phone width              | `/inbox`, `/new` and `/tasks/37` at 390×844 have no horizontal overflow (`scrollWidth` 390, dialog `scrollWidth - clientWidth` 0).                                                                                                                                                                                                                                                                 | PASS    |

## Bugs found and fixed

### Bug 1 — the short-soak question called a full soak short

**Reproduction:** on the demo, open `/inbox` and go to Asked: approve a
release. v2.4.0 reads "soaked 25h10m of 24h", and the train raised no SHORT
SOAK warning. Tick Allow short soak and press Approve short soak. The
confirmation asks "Approve v2.4.0 on a SHORT SOAK? It soaked 25h10m of 24h,
under the minimum. …".

**Cause:** `decideRelease` (`ui/src/notifications.js`) said "under the
minimum" whenever `short_soak_ok` was being sent. The person's own box can
set that on a release that soaked its full time.

**Fix:** "under the minimum" now appears only when the notification carries
the train's SHORT SOAK warning. Otherwise the question states the soak as
recorded ("…, and the release train did not flag it as short."), or says
that there is no recorded soak.

**Regression coverage:** `notifications.test.js` "do not call a full soak
short when a person ticks Allow short soak". It fails on the base and passes
with the fix.

### Bug 2 — a reloaded New task draft lost its template

**Reproduction:** on a template with priority 8, press New task from this
template, type a title and reload. The form comes back with the title but no
template, and Task priority shows 5. Create task files a plain task at 5,
without the template's description or settings.

**Cause:** the draft (`saveTaskDraft`) never recorded `fromTemplate`. The
page that opened the form on the template does not survive a reload. With no
template, the effect that makes Task priority follow the Template picker
until edited reset it to 5.

**Fix:** `NewTaskOverlay.jsx` saves `templateId` in the draft and restores
it when the form is opened without a template of its own. It also shows the
Template picker whenever a template is picked, so the restored one can still
be changed or cleared.

**Regression coverage:** `NewTaskOverlay.test.jsx` "keeps a draft's
template, and its priority, when the form reopens without one". It fails on
the base and passes with the fix. In the browser on the fixed build, the
reload keeps priority 8 and the task files at 8 with `templateId`.

### Bug 3 — Base branch prefilled from a stacked review's branch

**Reproduction:** on a fresh demo store, open New task for acme/widgets.
Base branch is prefilled with `grain/task-30`, the branch of review task #31
("Review: Default new workspaces to the paid tier", `stacked: true`).

**Cause:** `suggestsBase` (`ui/src/state.js`) judged a task only by its
state and `authorKind`. Its own comment says stacked fixes and reviews are no
evidence of where the next hand-filed task should start. grain files them as
`automation` (`pkg/orchestrator/review.go`), so production rows were already
skipped, but the demo seeds #31 as a person's.

**Fix:** a `stacked` task never feeds the prefill.

**Regression coverage:** `state.test.js` "looks past a stacked review or
repair whatever its author". On the fixed build, picking acme/widgets in New task leaves
Base branch empty.

## Observations (not treated as defects)

- **O1:** Reject on the config-change card still allows an empty reason
  (2026-11-09 O3).
- **O2:** Base branch catches a malformed fork label in the browser, but not
  a malformed plain branch name such as `bad owner:x`. The server refuses it
  with a clear 400, which the form shows.
- **O3:** Bulk edit and the batch actions clear the selection after applying,
  while Cancel keeps it (2026-11-09 O5).
- **O4:** `Ctrl+Enter` in the Inbox's reply box does not send. Send does.
- **O5:** Cancel on a stalled "live run" row closes the task (`closed`), as
  its confirmation says it will.

## Not covered this run

The following need rows that only an agent run or a reconciler writes:
host-script approval (stdin preview, approve and deny), the pending-secret
box, a suspended session, cross-team approvals, `request_access` cards, a
grain CI verdict with Re-run CI, the held-for-approval chip, the config-repo
sync-failure group and role-gated read-only rendering. The 2026-11-08
reports covered them with sqlite stubs. Also not re-walked this run: closed-task
paging, Force merge, the goal card, Mermaid and the image viewer, and the
chat pane. The 2026-11-09 report covers those on a build only a day older.

Screenshots of `/inbox` and `/new` at desktop and phone widths, in light and
dark, were taken with `make ui-screenshots` on the fixed build. I could not
look at them: there was no Gemini key, and the file reader does not render
PNGs. None were committed. The three fixes change a confirmation's text and
the form's draft and prefill logic, not layout. The 390 px overflow check in
step 4.7 is the layout evidence.
