# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-08 (task 3458)

- **Date:** 2026-11-08, 08:11–08:50 UTC
- **Task:** task 3458, branch `grain/098073e4/task-3458-walk-through-task-intake-inbox`, base `main`
- **Commit tested:** `6d15dc3a` (`main` at the start of the run, "Merge pull
  request #912"). The binary reports `v0.0.0-20261108080820-6d15dc3a27aa`.
  The fix below was re-checked on a second build of the same tree with the
  fix applied.
- **Why a third report today:** `2026-11-08-task-intake-inbox-lifecycle.md`
  (task 3354) and `…-task-3158.md` were already filed today. Both list the
  goal card, work tree, merge policy lines, pending-secret box, suspended
  session, host-script card on the task page, chat-pane Pause/Stop/Retry,
  long close reasons at phone width and the held-for-approval chip as not
  exercised. This run aimed at those first, then went over the Inbox,
  intake, list and batch paths again more briefly.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    1400×900 unless a step says 390×844
  - Frontend built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`, so the embedded
    `pkg/ui/static` was current
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -fork-pull-request -human-review -base-branch-ci
-access-code-file <file>` with `TMPDIR=/var/tmp/walk`, so the store
    was a throwaway embedded SQLite file. The fix was re-checked on a second
    demo (`-github-sim -fake-ci`) on `:8421`, built from the fixed tree.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` was not available. `sqlite3` was installed with apt for the
    store stubs.
- **How it was driven:** throwaway Playwright scripts (not committed). Each
  one signs in through the "Access code" page and then acts only through
  visible controls. Every page logged `pageerror`, console errors and
  warnings, `/api/` responses of 400 or more (with bodies), failed requests,
  every dialog (its text and whether it was accepted), and every non-GET
  `/api/` request with its body. The REST API was used only to read back
  what a gesture had persisted, and for the setup stubs below.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Each stub stands in for something only an agent run, a
reconciler or setup would write:

1. **Held-for-approval proposal** (normally written by `propose_task
hold_for_approval`): sqlite `task_tag ('1','held-for-approval')` and
   task 1's origin set to `agent` on behalf of `task 3`.
2. **Goals at their progress limit** (normally the goal reconciler):
   sqlite `task_goal` rows for #8 (`base-green`, `on_limit: hold`) and #11
   (`on_limit: notify`), each with `resumes_since_progress = max_resumes =
6` and `limit_notified_at` set.
3. **Interactive task for the chat pane:** `POST /api/tasks
{"interactive":true,…}` filed #32, because the demo's only interactive
   task (#13) opens as a console session, whose chat pane has its own
   turn control and no Pause.
4. **Pending secret** (normally a run's `request_secret`): sqlite
   `task_observation.pending_secret = 'stripe-api-key'` on #4.
5. **Suspended session** (normally a suspended run): sqlite
   `task_observation.suspended_session_*` on #8.
6. **Host-script approvals** (normally posted by a run's runhost): sqlite
   `task_grant ('33','local-scripts:deploy-tools','operator')`, then two
   `POST /api/tasks/33/local-script-approvals` (`walk-1`, `walk-2`) with
   the access code as a bearer token.
7. **Pushed branch for the Internal PR link** (normally a run's push): a
   commit adding `toolchain.txt` pushed as `grain/task-3` into the GitHub
   simulator's bare repo.
8. **A reason on a close:** the task page's Close takes no reason, so the
   long reason went through `POST /api/tasks/2/close {"reason":…}`, as an
   agent or the API would close it.
9. **Template priority** (setup): `PATCH /api/templates/template-2
{"priority":3}` (8 in an earlier step).

## Steps and outcomes

### Task-page sections

| #   | Step                                          | Outcome                                                                                                                                                                                                                                                                                                                                                                                              | Verdict |
| --- | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Goal on #2: Add goal → Tasks closed `3`       | "No goal: this task completes when its agent says it is done." → Add goal, kind Tasks closed, Tasks `3`, At its limit "Notify and back off". `PUT …/2/goal {"kind":"tasks-closed","spec":{"tasks":["3"],"until":"closed"},"on_met":"close","on_limit":"notify"}`. Card: "Tasks closed: 3 closed. Not met yet. On met: close. 0 resume(s)… (limit 6 resumes or 24h, then notify)." Survives a reload. | PASS    |
| 2   | Remove goal on #2                             | `DELETE …/2/goal`; the card goes back to "No goal…" at once and after a reload (no stale card). The timeline records both "Set this task's goal…" and "Removed this task's goal…".                                                                                                                                                                                                                   | PASS    |
| 3   | `on_limit` hold vs notify (stub 2)            | #8: "…then hold). Held at its limit: a person has to act." #11: "…then notify). Limit reached: operator notified, backing off."                                                                                                                                                                                                                                                                      | PASS    |
| 4   | Work tree on #11                              | Collapsed: "Provenance · Work from task 13: 0 of 4 done". Expanded: 13 at depth 1; 1, 3 and 8 at depth 2; 11 at depth 3 under 8, each with its state ("planned", "running", "PR open", "queued"). Links go to `/tasks/13`, `/tasks/1`, `/tasks/3`, `/tasks/8` and #8's `acme/widgets#104`.                                                                                                           | PASS    |
| 5   | Merge policy on #24                           | "Merge rules (matched deploy) for a1b2c3d4e5f6:" then one line per requirement with its rule: `ci.mode: grain` — from repository setting, `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build` and `ci.github: cla/google`, each from rule deploy, then "GitHub CI is narrowed to build, cla/google". No card on tasks with no rules (#6, #26).    | PASS    |
| 6   | Held chip (stub 1), then Approve on #1's page | Proposed view: "held for approval by 3", hover "task 3 could have queued this on its own permission and asked for a person to approve it first". Approve → `…/1/approve`, `queued`; the chip is gone from the Proposed view and from all tasks.                                                                                                                                                      | PASS    |
| 7   | Chat pane on #32 (stub 3): Pause, Resume      | The compose row offers Stop, Pause and Send. Pause → `…/32/pause`; the button becomes Resume, the page's own Resume appears without a reload, and the task stays `queued` with `paused.fromState: queued`. Resume → `…/32/resume`; the pause is cleared and the state is unchanged.                                                                                                                  | PASS    |
| 8   | Chat pane Stop, then Retry                    | Stop → `…/32/stop`; "Turn stopped by user" lands in the timeline and the task becomes `awaiting_reply`; Stop leaves the compose row; Retry is offered. Retry → `…/32/retry`; `queued`.                                                                                                                                                                                                               | PASS    |
| 9   | Close / Reopen at 390 px (stub 8)             | Close → `…/2/close {"close_pull_request":false}`, Reopen → `…/2/reopen`. Timeline: "Closed", "Reopened", and "by operator (access code) · Superseded by https://github.com/…/files#diff-0123…cdef and branch grain/walk/a-very-long-branch-name-…". The reason element is `overflow-wrap: anywhere` and ends at x=374 inside the 390 px viewport; `scrollWidth` 390, so nothing overflows.           | PASS    |
| 10  | Host-script card on #33 (stub 6)              | "A local script is waiting for approval. The run asks to run deploy-tools on the node host: deploy.sh "--env" "staging-1" (and 42 bytes on stdin) On stdin: 42 bytes, sha256 abc123 {"release":"v1.2.1"}". Approve → `…/lsa-1/answer {"approve":true}`; Deny on staging-2 → `{"approve":false}`; the timeline records "Denied local script call walk-2…", and nothing is left waiting.               | PASS    |
| 11  | Pending secret on #4 (stub 4)                 | "This task's run asked for a secret. Nothing is stored as stripe-api-key/value yet…". The box is `type=password`. Set secret → `PUT …/4/secret`; #4 goes `queued` and `pendingSecret` is cleared. The timeline says "Set `stripe-api-key`. Its value is in grain's secret store…". The typed value is not on the page, before or after a reload, and not anywhere in the task's API read.            | PASS    |
| 12  | Suspended session on #8 (stub 5)              | "Suspended session: Left by run-walk-8 on 11/8/2026, 7:30:00 AM (Antigravity). Any further run of this task resumes this conversation…" plus the token.                                                                                                                                                                                                                                              | PASS    |
| 13  | CL task #14                                   | Mail (disabled), Publish replies (2), Submit (disabled), Sync. Submit's explanation: "Not submittable yet: review is not approved (changes requested); required presubmit has not passed on snapshot 3 (failed)".                                                                                                                                                                                    | PASS    |
| 14  | Force merge #5                                | It prompts "Force this merge? … Say why. It is required…". Dismissing sends nothing. With a reason it sends `…/5/force-merge {"reason":…}`, and the page shows "Asked grain to force the merge of acme/widgets#101 at fedcba987654…" without a reload.                                                                                                                                               | PASS    |
| 15  | Prompt and live transcript on #3              | Prompt: "prompt for 3 · attempt #1 · 10 kb · ~2,633 tokens · 1% of a 200,000-token window", then the full prompt. Escape closes it. "live attempt #1 transcript" opens the agent's running narration.                                                                                                                                                                                                | PASS    |
| 16  | Internal PR on #3 (stub 7)                    | Once `GET /api/repos/acme/widgets/github-branches` lists `grain/task-3`, the header shows "Internal PR" (`target=_blank`) → `/repos/acme/widgets/compare/grain/task-3`: "pull request 3 ← acme/widgets grain/task-3 open Into main…", with side-by-side/inline/raw and the diff `toolchain.txt +1 -0 … go 1.24`.                                                                                     | PASS    |

### Inbox

| #   | Step                          | Outcome                                                                                                                                                                                                                                     | Verdict |
| --- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 17  | Rail's Inbox                  | Rail Inbox 25 → `/inbox`. Groups: Notifications 4 (collapsed, count shown), Asked: reply 2, Review needs your decision 1, Asked: submit for merge 10, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 2. | PASS    |
| 18  | Keyboard and reload           | Notifications header is a focusable `BUTTON`: Enter → expanded, Space → collapsed, Enter → expanded. Approve a proposal collapsed by click. After a reload Notifications is still expanded and Approve a proposal still collapsed.          | PASS    |
| 19  | Approve imported proposal #12 | Row: "imported … unapproved approve defer decline". `…/12/approve` → `queued`; the group disappears (#1 had been approved in step 6).                                                                                                       | PASS    |
| 20  | Stuck #21: Run now            | Row: "stalled ready … run now open task — this has stood here for more than 72h, which is longer than `reconcileDispatch` should take…". `…/21/run-now`; group 4 → 3 and the row leaves it.                                                 | PASS    |
| 21  | Parked question #7: Reply     | Reply → Send posts `…/7/comments {"body":…}`; #7 `queued`. Rail Inbox now 21.                                                                                                                                                               | PASS    |
| 22  | Row navigation, Back          | A row link opens `/tasks/4`; Back returns to `/inbox`.                                                                                                                                                                                      | PASS    |

### Intake

| #   | Step                                                      | Outcome                                                                                                                                                                                                                                                                                                                     | Verdict |
| --- | --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 23  | Create options → New task, queued path (#33)              | Menu: New task, New session, New board; focus lands in Title. Title, two-paragraph description, attachment, repo acme/widgets, base `release/1.0`, reviewers `alice, bob`, auto-merge unticked, priority 3. Read back `queued`, `autoMerge:false`, `priority 3`, `base release/1.0`, `reviewers [alice,bob]`, `attach.txt`. | PASS    |
| 24  | Task priority validation                                  | `0`, `-3` and `2.5` each show "a whole number, 1 or more; default 5, higher numbers run first" and disable Create task.                                                                                                                                                                                                     | PASS    |
| 25  | Proposed, no-repo path (#34); cancel                      | Repos "No repo (standalone task -- nothing to check out)", Queue immediately unticked → `noRepo:true`, `proposed`. A third draft closed with Close dialog files nothing.                                                                                                                                                    | PASS    |
| 26  | Auto-merge off survives holds and edits                   | #33 Pause → Resume, Defer → Undefer, then Edit title → Save (`PATCH` without `autoMerge`): `autoMerge` stays `false` and priority 3 throughout.                                                                                                                                                                             | PASS    |
| 27  | `/new?field.area=backend&field.nosuch=1&field.estimate=x` | Notice: "This link asked for values this form has left out: “nosuch” is not a field on this deployment; Estimate (days): “x” is not one of its values." Area shows the `backend` chip.                                                                                                                                      | PASS    |
| 28  | Fork head, Create a new branch unticked                   | `alice:feature-x` → "GitHub fork head: branch feature-x on alice's fork…", Create enabled. `alice:` and `:x` → "needs both an owner and a branch"; `a:b:c` → "has more than one ':'"; Create disabled for each.                                                                                                             | PASS    |
| 29  | Model picker; refused field in Advanced                   | Models: "Deployment default (gemini-2.5-pro)", gemini-2.5-pro, gemini-2.5-flash; Escape leaves the form open. With vCPUs `-1` and Advanced collapsed, Create task opens Advanced (`aria-expanded` false → true) and focuses `sandboxCpus`.                                                                                  | PASS    |
| 30  | Template picker priority (stub 9)                         | From security-audit's page, "New task from this template" opens at 8 (then 3 after re-patching); switching to release-qualification gives 5 and back gives the template's value; a typed 4 survives a template switch.                                                                                                      | PASS    |
| 31  | Template priority with a draft left behind                | **Bug 1.** With any draft left in the form (only a title typed), "New task from this template" restored the draft, picked the template and kept Task priority at 5, and filing sent `priority: 5` (#35 filed at 5 against a template at 3). Fixed; see below.                                                               | FIXED   |

### Tasks list, selection and manual mode

| #   | Step                                            | Outcome                                                                                                                                                                                                                                                                                                                                               | Verdict |
| --- | ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 32  | Row chips                                       | #12 `imported`, hover "imported from GitHub issue acme/widgets#42"; #11 "blocked on task", hover "Blocked on: #8 (running)" and "queued/blocked_on_task / Driven by reconcileDispatch / Waiting on the task it depends on closing"; live tasks carry "live run" and stalled ones "stalled…"; Task priority badge `p6` after step 38.                  | PASS    |
| 33  | Origin filter = Imported issue                  | Add filter → Origin → Imported issue: `?f=state:none:deferred\|closed&f=origin:any:imported`, rows `12`; the same after a reload.                                                                                                                                                                                                                     | PASS    |
| 34  | Search, sort, Back/Forward                      | `?state=asked` (12 rows) → search `retry` → `&q=retry` (14–19) → sort Title → `&sort=title:asc` (16,17,15,14,19,18). Back and Forward walk between entries and rebuild the rows each time; clearing the search drops `q`. See O1 on which steps make history entries.                                                                                 | PASS    |
| 35  | Dependency #33 → #34, then removed              | "Add a dependency…" → `34` → Enter sends `depends-on {"id":"34","attach":true}`. Chip "34 walk b: proposed no repo (open)", tooltip "34 Walk B: proposed no repo — Proposed (blocking this task)". #33 joins the Blocked view (11, 33) and the rail's Blocked goes 1 → 2. Focusing the chip and pressing Delete sends `attach:false`; rail back to 1. | PASS    |
| 36  | Bar: 2 selected, Close dismissed                | Bar: Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, capability picker, Attach, Detach, Clear selection. Close asks "Close 2 task(s)?"; dismissing sends nothing and keeps the selection.                                                                                                                  | PASS    |
| 37  | Bulk edit Cancel                                | The dialog lists repo, base branch (mixed), auto-merge, model, agent framework, review, prompt extension, task priority (mixed) and custom fields. Cancel sends nothing; the selection stays.                                                                                                                                                         | PASS    |
| 38  | Bulk edit Task priority                         | `0` disables Apply. `6` sends exactly `PATCH {"priority":6}` to #33 and #21; base and auto-merge unchanged; the selection clears.                                                                                                                                                                                                                     | PASS    |
| 39  | Bar Defer, Undefer, Attach, Detach              | Defer/Undefer: one call per task, `deferred` then `queued`. Attach `API preset: ci-read` → `capabilities {"id":"api:ci-read","attach":true}` → present; Detach → gone.                                                                                                                                                                                | PASS    |
| 40  | Bar Approve, Close, Reopen, Submit, Retry merge | #34 Approve → `queued`; Close (confirmed) → `closed`; Reopen from Closed → `queued`; #6 Submit → `completed`; #25 Retry merge → `…/25/retry-merge`.                                                                                                                                                                                                   | PASS    |
| 41  | System → Pipeline → Pause; Run selected now     | Confirm "Pause the pipeline? …" → `POST /api/pipeline-pause`. Queued rows carry "waits for Run now" except #21 (already started) and #11 (blocked). Run selected now on #33 and #2 → `run-now` each; the markers go and stay gone after a reload. Resume asks first and sends `DELETE /api/pipeline-pause`.                                           | PASS    |

### Console, network, server

There were no uncaught page errors. The only console errors and 4xx/5xx
responses were the 401s on the sign-in page before the access code is
entered. No requests failed apart from polls cut off by navigation. Neither
demo server's log had a panic or an error.

## Bugs

### 1 — A left-behind draft overrode a template's Task priority (fixed)

**Repro (base `6d15dc3a`):** `PATCH /api/templates/template-2
{"priority":3}`. Open New task, type a title, Close dialog (the draft is
kept). Open Processes → security-audit → "New task from this template".
The form restores the draft and picks security-audit, but Task priority
says 5, and Create task sends `"priority":5` with `"templateId":"template-2"`,
so the task files at 5. Without the left-behind draft the same steps open at
3 and file at 3. Typing only a base branch or only an Advanced field instead
of the title gives the same result.

**Cause:** `NewTaskOverlay.jsx` seeds `formEdited` (the fields a template or
repo change must leave alone) from `Object.keys(draft)`. A draft saves
every field, typed or not, so an untouched priority counted as edited, and
the effect that follows the Template picker never ran.

**Fix:** the draft now records `edited: [...formEdited.current]`, and a
restored draft seeds `formEdited` from that list. A draft saved before this
change has no `edited` and falls back to all its keys, as before. A priority
somebody typed is still in `edited`, so it still wins over the template.

**Regression coverage:** `NewTaskOverlay.test.jsx` "lets a template's
priority win over a draft's untouched one, but not a typed one". It leaves a
title-only draft, opens the form from a template at 8, expects 8 shown and
`priority: 8` filed, then repeats with a typed 3 and expects 3 kept. It fails
on the base and passes with the fix. All six `NewTaskOverlay*` suites pass
(93 tests). In the browser on the fixed build (`:8421`), the three draft
variants open at 3 and #20 files at 3.

## Observations

- **O1:** Picking a rail view or adding a filter makes a history entry, but
  typing a search and changing the sort replace the current one
  (`paths.js`'s replaceState pass). So Back from a sorted, searched Asked
  view goes to the view before Asked, not to the unsorted list. This looks
  intentional, since it avoids one history entry per keystroke.
- **O2:** The rail's state views keep the current narrowing ("the rail's
  state first, then the narrowing", `paths.js`). With Origin = Imported
  issue active, Blocked (rail count 1) and Merging opened empty. Clearing
  the filter shows the rows. The counts are not narrowed, which can look
  inconsistent, but this is by design.
- **O3:** The task page is a full-pane overlay without the rail, so the
  rail's Blocked count was read from the list (step 35).
- **O4:** The chat pane offers only Pause for a running task. "Pause now"
  is on the task page's own actions. The interactive director session (#13)
  opens as a console session, whose chat pane has Stop and no Pause.
- **O5:** The Stop → Retry pair leaves the "Turn stopped by user" comment,
  but after Retry the task's `transitions` no longer list the
  `awaiting_reply` it passed through. `model.Transitions` rebuilds history
  from timestamps on the record, and the ask's timestamp is cleared when the
  task is requeued. The comment is the lasting record.
- **O6:** A suspended session left by the old `gemini` framework value is
  labelled "Antigravity". `frameworkLabel` maps the former wire value
  deliberately.
- **O7:** The "Add a dependency…" box has a placeholder but no label. The
  browser takes its accessible name from the placeholder, and the results
  are menu items reachable with the arrow keys and Enter (step 35 used the
  keyboard).
- **Not reachable in this demo:** a grain CI verdict on any task (so no
  verdict card, CI-run link or Re-run CI), the `resume_later` park reason (no
  suspended run record carries one), cross-team approvals and their
  withdraw control, an inline config-repo change card, an attempt's
  capability audit, the capability-policy toast for dropped default grants,
  a ticked capability that is not ready, and an external fork pull
  request's outlined PR chip on the list. The 2026-11-08 task 3354 report
  covered the config-repo sync group, Inbox empty and error states, Ship as
  is, and CL Mail/Publish replies/Sync over the same seeds; they were not
  repeated here.
