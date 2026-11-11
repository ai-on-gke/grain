# Task intake, Inbox and lifecycle walkthrough — 2026-11-08 (task 3611)

- **Date:** 2026-11-08, 11:50–13:40 UTC, over two runs (the first ran out of
  quota part-way through and its sandbox was lost).
- **Task:** task 3611, branch
  `grain/098073e4/task-3611-walk-through-task-intake-inbox`, base `main`.
- **Commits tested:**
  - Run 1 (Inbox and intake, §1–§2 below): `0fa1b11c`, binary
    `v0.0.0-20261108114918-0fa1b11c413c`.
  - Run 2 (everything else, plus a re-check of the Inbox and intake points):
    `33a44581` ("Merge pull request #946"), binary
    `v0.0.0-20261108122327-33a445814ac8`. Between the two commits, no Inbox,
    New task, list or task-page component changed. The only change in this
    area is the not-ready capability hint's wording in `state.js`
    (Settings > Legacy capabilities became Settings > Capabilities), and
    §2 re-checks it.
  - The fix below was checked on a third build of `33a44581` with the change
    applied.
- **Why a fourth report today:** `2026-11-08-task-intake-inbox-lifecycle.md`
  (task 3354), `…-task-3158.md` and `…-task-3458.md` were already filed
  today. This run concentrated on what those three list as not reachable in
  the demo: the capability-policy toast, a ticked capability that is not
  ready, an attempt's capability audit, a grain CI verdict with Re-run CI,
  the inline config-repo change card, cross-team approvals, and an external
  pull request's chip.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`; Node.js
    `v20.19.0`.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC. The
    viewport was 1400×900 unless a step says 390×844.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -legacy-capability -qualification -mechanical-port
-access-code-file <file>`, with `TMPDIR` set to a scratch directory so the
    store was a throwaway embedded SQLite file. Every script started a fresh
    demo.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `--look` was not available. `sqlite3` was installed with apt for the
    store stubs.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout and not committed. Each one signs in through the Access code page
  and then acts only through visible controls. Every page recorded uncaught
  page errors, console errors and warnings, `/api/` responses of 400 or more
  (with bodies), failed requests, every native dialog (its text and whether
  it was accepted), and every non-GET `/api/` request with its body. The
  REST API was used only to read back what a gesture had persisted, and for
  the stubs below.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Each stub stands in for something that only an agent run, a
reconciler or setup would write, or for a boundary the demo does not have.

1. **Held-for-approval proposal** (normally written by `propose_task`
   with `hold_for_approval`): sqlite `task_tag ('1','held-for-approval')`,
   with task 1's origin set to `agent` on behalf of `task 3`.
2. **Goals at their limit** (normally the goal reconciler): sqlite
   `task_goal` rows for #8 (`base-green`, `on_limit: hold`) and #11
   (`tasks-closed`, `on_limit: notify`), each with
   `resumes_since_progress = max_resumes = 6` and `limit_notified_at` set.
   #8's spec was `{}`, so its card reads "Base branch green: undefined on
   undefined green". That comes from the stub, not the UI.
3. **Pending secret** (normally a run's `request_secret`): sqlite
   `task_observation.pending_secret = 'stripe-api-key'` on #4.
4. **Suspended session** (normally a suspended run): sqlite
   `task_observation.suspended_session_*` on #8.
5. **Grain CI verdict** (normally grain CI): sqlite `task_observation.ci_*`
   on #24, with conclusion `failure` and detail "job test: go test ./...
   failed".
6. **Config-repo change proposals** (normally `propose_config_change`):
   sqlite `task_observation.pending_config_change` JSON on #7 and on a filed
   task, each with a one-line diff to `settings/engine.yml` and
   `validation.ok = true`.
7. **Host-script calls** (normally posted by a run's runhost):
   `POST /api/tasks/<id>/local-script-approvals` with the access code as a
   bearer token, on tasks filed with `local-scripts:ops`.
8. **A reason on a close:** the task page's Close takes no reason, so a
   300-character reason went through `POST /api/tasks/<id>/close
{"reason":…}`, as an agent or the API would send it.
9. **Setup through the API:** `PATCH /api/templates/template-2
{"priority":3}`, a template with a `{{env}}` field
   (`POST /api/templates`), `PUT /api/settings` (`defaultCapabilities`),
   `PUT /api/capability-policy` (a deny rule for `cloud#cloud-viewer` on
   `acme/widgets`), and sqlite `task.admission = 'dedicated'` on #2.
10. **Boundary stubs on `GET /api/config`** (Playwright route, response
    otherwise passed through):
    - `changeTargets: ["google3"]` (core registers no changelist target);
    - a `settings/new-task-form.yml` layer hiding custom fields, for the
      `/new?field.*` notice;
    - `example-legacy` marked `ready: false, needs: ["EXAMPLE_SECRET"]`.

## Steps and outcomes

### 1. Inbox (run 1 on `0fa1b11c`; re-checked on `33a44581`)

| #   | Step                                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Verdict |
| --- | ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Open from the rail and directly          | The rail's "inbox 28" leads to `/inbox`, and a deep link to `/inbox` loads the same page. The groups are notifications 4 (collapsed), local scripts waiting for approval 1, asked: reply 2, review needs your decision 1, asked: submit for merge 10, unblock a merge 1, approve on github 1, look at a stuck task 4, approve a proposal 3–4, plus the config-repo sync row "settings are not being exported". The rail count includes the waiting local-script call, and a config-repo sync failure took it from 28 to 29. | PASS    |
| 2   | Collapse and expand                      | Notifications starts with `aria-expanded="false"` and its count still shown. Clicking, `Enter` and `Space` on a focused header each toggle it. A reload keeps each section's state (re-checked on `33a44581`: Stalled collapsed with Enter stays collapsed after a reload, then Space expands it).                                                                                                                                                                                                                          | PASS    |
| 3   | Stalled sub-cases                        | #5 "on merge queue"; #21 ready → Run now; #22 live run → Cancel (native confirm); #23 wake time → Undefer, which took it to `queued` because it was already approved. Each row left the group after its control was pressed. Re-checked on `33a44581`: #21 Run now → `POST /api/tasks/21/run-now`, and the row left the group.                                                                                                                                                                                              | PASS    |
| 4   | Proposal, reply, review decision, submit | #1 Approve → `queued`. #4 inline Reply → Send → `queued` (re-checked). #28 Ship as is → completed with auto-merge on. Human-review #30 Submit → completed with auto-merge on. Each row left its group, and stayed gone after a reload.                                                                                                                                                                                                                                                                                      | PASS    |
| 5   | Unblock, GitHub approval, local script   | #25 Retry merge. #20 links to its pull request diff. The local-script card shows `deploy "--env" "staging"` with a 15-byte stdin preview `target=staging`, and Approve or Deny removes it.                                                                                                                                                                                                                                                                                                                                  | PASS    |
| 6   | Inline changelist actions                | Submit is offered inline only on submittable #15, not on #14 or #16–#19. Each control was pressed: Mail on #17 (→ `in_review`); Publish replies on #14, with its disabled-Submit reasons; Sync on #19 (clears "behind head" and takes a new snapshot whose presubmit has not run); and Submit on #15 (→ `closed`).                                                                                                                                                                                                          | PASS    |
| 7   | Row navigation, empty state, phone       | Row #1 opens `/tasks/1`, and Back returns to `/inbox`. Notification Acknowledge works. With every group answered, the empty state shows. At 390×844 there is no horizontal overflow.                                                                                                                                                                                                                                                                                                                                        | PASS    |

### 2. New task (run 1 on `0fa1b11c`; step 10 re-checked on `33a44581`)

| #   | Step                                            | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Verdict |
| --- | ----------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | `+ New task`, Cancel                            | Typed a title, then Close dialog and Discard draft: no request was sent and no native dialog appeared.                                                                                                                                                                                                                                                                                                                                                                                                   | PASS    |
| 2   | Rail create menu → proposed, no repo            | The menu offers new task / new session / new board. Filed #35 with Queue immediately unticked and No repo, a description, a `sample-note.txt` attachment and four custom fields: `state=proposed`, `repo` unset, `attachments=1`, and the custom fields stored.                                                                                                                                                                                                                                          | PASS    |
| 3   | Task priority                                   | `0`, `-3` and `2.5` each disable Create task, in the shared wording "a whole number, 1 or more; default 5, higher numbers run first". `8` is accepted.                                                                                                                                                                                                                                                                                                                                                   | PASS    |
| 4   | Advanced options                                | Model offers only Deployment default (gemini-2.5-pro), gemini-2.5-pro and gemini-2.5-flash, the models the one enabled connection advertises. A CPU of `-1`, submitted with Advanced options collapsed, reopens them.                                                                                                                                                                                                                                                                                    | PASS    |
| 5   | Grants                                          | Add grant… has the Grain API group. `Escape` closes only the picker, puts focus back on Add grant…, and leaves the form open.                                                                                                                                                                                                                                                                                                                                                                            | PASS    |
| 6   | Queued path, base branch, auto-merge off        | Filed #36 with `state=queued`, `priority=8`, `base=release/1.0` and `autoMerge=false`. `false` survived a later edit.                                                                                                                                                                                                                                                                                                                                                                                    | PASS    |
| 7   | Policy verdict and dropped-default toast        | The picker row reads "the capability policy denies …cloud#cloud-viewer to this task on acme/widgets (rule 1: …)". Filing gives the toast "Task #37 was filed without a default grant / …cloud#cloud-viewer: the capability policy denies …".                                                                                                                                                                                                                                                             | PASS    |
| 8   | Fork head label (Create a new branch unticked)  | `bad::head` disables Create task. `contrib:fix/readme-typo` is explained under the field and files #38 with `externalPullRequests: ["acme/widgets#41"]`.                                                                                                                                                                                                                                                                                                                                                 | PASS    |
| 9   | Templates                                       | Security-audit → New task from this template opens at priority 3. Picking No template goes back to 5, and picking security-audit again gives 3. A priority typed as 9 is kept across template switches. A template with a `{{env}}` field opens the Use… form ("File a task from a template") instead, and its row offers Use…. That filed "Verify production rollout" at priority 7. Its composition preview answers 400 "field \"Target environment\" is required" until the field is filled (see O3). | PASS    |
| 10  | `/new?field.*`, reviewers, not-ready capability | The notice reads "This link asked for values this form has left out: Priority: “high” is in a field this form hides; Area: “backend” is in a field this form hides." Picking the `google3` target shows Reviewers, which accepted `alice, bob`. Re-checked on `33a44581`: the default `example-legacy`, stubbed not ready, opens ticked, its option reads "Not ready -- needs EXAMPLE_SECRET. … (Settings > Capabilities).", and it stays ticked after typing a title.                                   | PASS    |

### 3. Tasks list (run 2)

| #   | Step                               | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Verdict |
| --- | ---------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 11  | Rail views                         | tasks 35 → `/?f=state:none:deferred\|closed` (31 rows); proposed 3, waiting 8, running 3, asked 13, merging 4, deferred 2, closed 1 → `/?state=<s>&f=none` (matching row counts); merged 1 → `&sort=merged:desc`; blocked 1 → `/?state=blocked`, showing #11.                                                                                                                                                                                                                                                          | PASS    |
| 12  | Row state and chips                | #11 has `badge-blocked`, aria-label "Blocked on: #8 (running)", and the sub-case "blocked on task" (hover "queued/blocked_on_task / Driven by reconcileDispatch / Waiting on the task it depends on closing"). #33, #5, #20 and #25 read "Merging". A priority-8 task carries `p8` (hover "Task priority 8 (default 5; higher numbers run first)"), and default rows carry none. Live #8 has "live run" (hover "running/live_run / Driven by ProcessResult / Waiting on the run in flight / "Cancel" ends this wait"). | PASS    |
| 13  | Imported issue                     | #12 carries `imported` (hover "imported from GitHub issue acme/widgets#42"). While unapproved, its badge reads "Imported". Approve on its page → `queued`; the badge reads "Waiting" and the `imported` chip stays.                                                                                                                                                                                                                                                                                                    | PASS    |
| 14  | External pull request              | On a task filed against `contrib:fix/readme-typo`, the task header has the outlined `acme/widgets#41` chip linking to the pull request. **The list row named no pull request at all** (chips: `acme/widgets`, owner, grants). This is Gap 1 below, fixed here.                                                                                                                                                                                                                                                         | FIXED   |
| 15  | Held for approval                  | #1 carries "held for approval by 3" (hover "task 3 could have queued this on its own permission and asked for a person to approve it first"). After Approve on its page it is `queued` and the chip is gone.                                                                                                                                                                                                                                                                                                           | PASS    |
| 16  | Origin = Imported issue            | `?f=state:none:deferred\|closed&f=origin:any:imported` shows `["12"]`, and still does after a reload.                                                                                                                                                                                                                                                                                                                                                                                                                  | PASS    |
| 17  | Compound narrowing, Clear          | Adding State is any of Proposed → `…&f=state:any:proposed` gives 0 rows, "No tasks match these filters.". Clear → 31 rows, URL `?f=state:none:deferred\|closed`.                                                                                                                                                                                                                                                                                                                                                       | PASS    |
| 18  | Search, sort, reload, Back/Forward | Asked has 13 rows. Searching `retry` → `?state=asked&q=retry` (14–19). Sort Title → `&sort=title:asc` (16, 17, 15, 14, 19, 18), which survives a reload. Back → the All tasks view, because search and sort replace the history entry; Forward → the sorted search. A search with no matches shows "No tasks match your search.".                                                                                                                                                                                      | PASS    |
| 19  | Keyboard                           | Focusing row #2's title link and pressing Enter opens `/tasks/2`.                                                                                                                                                                                                                                                                                                                                                                                                                                                      | PASS    |

### 4. Task page (run 2)

| #   | Step                                 | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Verdict |
| --- | ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 20  | Entry points                         | Tasks row, Inbox Proposals row, #11's dependency chip "8 add pagination to the tasks api (open)" (→ `/tasks/8`; Back → `/tasks/11`), the work tree's links, and a deep link.                                                                                                                                                                                                                                                                                                                                                                                         | PASS    |
| 21  | Edit                                 | Edit → priority `0` disables Save, in the shared wording. Saved title "Walk proposal edited", description "Edited description" and priority 7, all still there after a reload.                                                                                                                                                                                                                                                                                                                                                                                       | PASS    |
| 22  | Auto-merge                           | Unticking "Auto-merge once checks pass" gives `autoMerge=false`, still unticked after a reload.                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | PASS    |
| 23  | Defer, Undefer, Approve, Withdraw    | Defer with no date → `deferred`, no wake date. Undefer → `proposed`. Defer until 2026-11-11 → `deferredUntil=2026-11-11T00:00:00Z`, with the hint "Picks itself back up on 11/11/2026, 12:00:00 AM, back into the proposals for someone to approve.". Undefer, then Approve → `queued`. Withdraw approval → `proposed`.                                                                                                                                                                                                                                              | PASS    |
| 24  | Close and Reopen                     | Close → `closed`, with no confirmation (see O1). Reopen → `queued`. The timeline records "Closed by operator (access code)" and "Reopened by operator (access code)". The 300-character API close reason is shown at 390×844 with 0 px of horizontal overflow.                                                                                                                                                                                                                                                                                                       | PASS    |
| 25  | Pause now, Resume (page)             | On running #3, Pause now asks "Pause 3 now? Its run is stopped at once, without the grace period…". After accepting, the page shows Resume without a reload, and Resume clears the pause. The state stayed `running` throughout.                                                                                                                                                                                                                                                                                                                                     | PASS    |
| 26  | Chat pane                            | On a filed interactive task, the chat pane's Pause → paused and Resume → unpaused, staying `queued` throughout. Stop → `awaiting_reply`. Retry is shown, and pressing it → `queued`.                                                                                                                                                                                                                                                                                                                                                                                 | PASS    |
| 27  | CI, Force merge, Retry merge         | #24's card: "grain CI: failure against abc1234. job test: go test ./... failed. …". Re-run CI → `POST /api/ci/runs {"taskId":"24"}`. Force merge asks "Force this merge? grain merges the pull request itself, whatever its checks say, …", then sends `POST /api/tasks/24/force-merge`, and the page reflects it without a reload. #25 Retry merge → `POST /api/tasks/25/retry-merge`. The card's way into CI is a "show the run" control rather than an `<a href="/ci/…">` link (see O4).                                                                          | PASS    |
| 28  | Goal card                            | #8 (hold): "6 resume(s) without progress … then hold. Held at its limit: a person has to act." #11 (notify): "… then notify. Limit reached: operator notified, backing off." Remove goal on #11 removes the card at once with no confirmation, the card is still gone after a reload, and the API reports `goal: null`.                                                                                                                                                                                                                                              | PASS    |
| 29  | Dependencies                         | Added #35 to #36 using only the keyboard. The chip reads "35 walk proposal edited (open)", with tooltip "35 Walk proposal edited — Waiting (blocking this task)". The rail went from blocked 1 to blocked 2, and the Blocked view shows `["11","36"]`. Delete on the focused chip removes it, and the rail goes back to blocked 1.                                                                                                                                                                                                                                   | PASS    |
| 30  | Work tree                            | #11: "Provenance · work from task 13: 0 of 4 done". Expanded, it nests 13 → 1, 3, 8 → 11, with states and `acme/widgets#104`, in 7 links. The root opens `/session/director`.                                                                                                                                                                                                                                                                                                                                                                                        | PASS    |
| 31  | Merge policy                         | #24: "Merge rules: .grain/merge-rules.yml at 5e1d2c3b4a59 matched rule(s) deploy … It needs: ci.mode: grain -- from repository setting / ci.mode: both -- from rule deploy / auto_merge: never -- from rule deploy / stall_timeout: 30m -- from rule deploy".                                                                                                                                                                                                                                                                                                        | PASS    |
| 32  | Pending secret                       | #4's box says nothing is stored as `stripe-api-key/value` yet. A value typed and set → `PUT /api/tasks/4/secret`; the box goes away. After a reload the value appears nowhere in the DOM or in `GET /api/tasks/4`.                                                                                                                                                                                                                                                                                                                                                   | PASS    |
| 33  | Suspended session, `resume_later`    | #8 shows the parked session with token `sess-walk-8f2c`. `POST /api/tasks/<id>/resume-later` with a person's access code returned 200 `{"when":{"message":true}}`. Whether the park reason survives was not checked on the page, so that remains unverified.                                                                                                                                                                                                                                                                                                         | PARTIAL |
| 34  | Cross-team approvals                 | #26's sensitive hold "Held by this repository's gating triggers (human: required) … review task 27" and rule row "trigger:payments (.grain/triggers/payments.yml): waiting for a person holding the access code". Submit → `completed` with `approvedBy: "debian"` on the rule. The only withdraw control is Withdraw approval (step 23); a trigger rule has none of its own.                                                                                                                                                                                        | PASS    |
| 35  | Host-script approvals (page)         | Two cards show stdin previews `target=staging` and `target=prod`. Approve sends `{"approve":true}` and Deny sends `{"approve":false}`; no cards are left.                                                                                                                                                                                                                                                                                                                                                                                                            | PASS    |
| 36  | Config-repo change card              | #7's card shows the summary, description, branch at `2222222`, `settings/engine.yml`, "Schema valid · Capability catalogue consistent · Dispatch liveness OK · Dry-run apply succeeded", and a side-by-side or inline diff (`poll_interval: 30s`). Reject asks "Why are you rejecting it?" and sends `{"headSha":"2222222","reason":"Keep 10s for now"}`. Approve & Apply sends `{"headSha":"2222222"}`. Both get **501** "this deployment's UI does not manage a config repository to propose changes to", because the demo has no config repository (Follow-up 1). | PARTIAL |
| 37  | Parked question, prompt, transcripts | #4's callout: "Fix both lint-stuck PRs, or only unblock the merge queue?". Prompt opens "No prompt recorded yet -- nothing has been dispatched…". #8 shows "Live Attempt #2 Transcript — Still running -- nothing written yet." #8's "3 capability audit records" opens Attempt #1's transcript with its "capability audit · 3 records" section.                                                                                                                                                                                                                     | PASS    |

### 5. Batch bar and manual pipeline (run 2)

| #   | Step                                   | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                            | Verdict |
| --- | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 38  | Select and Shift-select                | Clicking #35 and Shift-clicking #38 ticks 4 rows. The bar offers approve, run selected now, submit, retry merge, defer, undefer, close, reopen, bulk edit…, attach, detach and clear selection. Close asks "Close 4 task(s)?"; dismissing it sends nothing and keeps the selection.                                                                                                                                                | PASS    |
| 39  | Bulk edit                              | Fields: repo, base branch, auto-merge, model, agent framework, review, prompt extension, task priority and the declared fields. Cancel sends nothing and keeps the selection. Ticking Task priority and entering `0` disables "Apply to 2 tasks", in the shared wording. `6` sends `PATCH {"priority":6}` to each task, leaves base and auto-merge alone, and clears the selection; rows read `p6` after a reload.                 | PASS    |
| 40  | Approve, Defer, Undefer, Close, Reopen | Approve → `queued`, Defer → `deferred`, Undefer from the Deferred view → `queued`, Close (asks "Close 2 task(s)?") → `closed`, Reopen from the Closed view → `queued`.                                                                                                                                                                                                                                                             | PASS    |
| 41  | Capabilities, Submit, Retry merge      | "API preset: ci-read" Attach → `{"id":"api:ci-read","attach":true}` per task, and the grant is present. Detach → `attach:false`, and it is gone. #6 Submit → `completed`. #25 Retry merge → `POST /api/tasks/25/retry-merge`.                                                                                                                                                                                                      | PASS    |
| 42  | Manual mode                            | System → Pipeline → Pause pipeline asks "Pause the pipeline? Grain will stop starting new agent and CI runs. …" → `POST /api/pipeline-pause`. "waits for Run now" appears on #2, 21, 23, 27, 29, 31, 37 and 38, but not on blocked #11 or running #3 and #8. Run selected now on #37 and #38 → `run-now` for each. The markers are gone after a reload and still gone 6 s later. Resume asks first → `DELETE /api/pipeline-pause`. | PASS    |

### Console, network, server

There were no uncaught page errors in any run. The console errors and
4xx/5xx responses were all expected:

- the 401 on the sign-in page, before the access code is entered;
- the 400s from the Use… composition preview while its required field was
  empty (O3);
- the 501s from the config-change card (step 36).

No requests failed apart from polls cut off by navigation. No demo log had a
panic or an error line.

## Gaps and bugs

### Gap 1 — A task row did not name the external pull request it is about (fixed)

**Repro (base `33a44581`):** `grain demo -github-sim -fork-pull-request`.
File a task on `acme/widgets` with base `contrib:fix/readme-typo`; the API
returns `externalPullRequests: ["acme/widgets#41"]`. On the task page, the
header carries the outlined `acme/widgets#41` chip. In Tasks (`/?q=contributor`),
the same row's chips are `acme/widgets`, the owner and the grants, so nothing
on the row says it is about a contributor's pull request. The walkthrough's
row checklist expects an outlined PR chip there. Earlier reports today had
listed this as "not reachable in the demo".

**Cause:** `038446cb` added `externalPullRequests` to the task view and drew
it only in `DetailOverlay.jsx`'s header. `TaskRow` (`TaskList.jsx`) never
read it.

**Fix:** `TaskRow` draws one outlined chip per external pull request
(`chip-external-pr`, `data-testid="task-external-pr-chip"`), next to the
other origin chips, with the hover text "about pull request <ref>, opened
from a fork rather than by this task". It is not a link: the row is the link
to the task, and the task page links the pull request. A click on the chip
opens the task, as a click anywhere else on the row does.

**Regression coverage:**

- `TaskList.test.jsx` "names the external pull request a task is about on
  its row" checks one chip, outlined, labelled `acme/widgets#41`, with that
  ref in its title, not inside a link, and none on a task without one. It
  fails on the base and passes with the fix; all 123 `TaskList` tests pass.
- `e2e/external-pr-chip.spec.js` "the task list's row names the external
  pull request too" runs a real demo with `-github-sim -fork-pull-request`,
  checks the row's chip text and outlined class, and checks that clicking
  the chip opens `/tasks/<id>`. All four tests in that spec pass on the fixed
  tree. The spec's global setup builds `grain` from the working tree, so the
  e2e test could not be run against the base binary; the Vitest test is
  the one shown failing on the base.
- **Screenshots:** `/?q=contributor`, desktop and phone, light and dark,
  before and after (`e2e/screenshot.mjs --url` against a demo with the
  fork-head task filed). Measured on the fixed build:
  - the chip sits inside the row, 24 px high like the row's other chips;
  - 0 px horizontal overflow at 1440 and 390 px wide;
  - the desktop row stays 58 px high;
  - the phone row grows from 152 to 181 px, because the extra chip wraps
    onto another line;
  - the text and border take the theme's colours in light and dark.

### Retracted from run 1 — the rail's Inbox count looked stale after answering a local-script call

Run 1 (on `0fa1b11c`) recorded that answering a waiting local-script call in
the Inbox updated the page but left the rail's Inbox count unchanged until
the next background `/api/config` poll. Run 2 measured it on `33a44581` by
sampling the rail every 100 ms after Approve, twice. Each time a
`/api/config` request went out about 100–150 ms after the click, and the
count went from 28 to 27 within 260–335 ms. The task page has no rail, so the
count cannot be seen from there. A code change was drafted (refreshing the
config in `App.jsx`'s `act` and `inboxAct`) and then dropped, because the
behaviour it was meant to fix could not be reproduced.

## Observations

- **O1:** On a queued or proposed task with no pull request, Close on the
  task page acts at once, and so does Remove goal on a goal card. The batch
  bar's Close and the Inbox's Cancel of a running task both ask first. A
  closed task can be reopened and a goal declared again, so neither was
  treated as a defect.
- **O2:** A stalled row's control (for example Undefer on #23) uses the same
  label as a batch-bar button. With rows selected, the list therefore shows
  two "Undefer" buttons that act on different tasks. A script that took the
  first matching button undeferred #23 rather than the selection. The bar's
  own buttons do act only on the selection.
- **O3:** The Use… form asks for its composition preview as soon as it
  opens, so a template with a required field logs one 400 ("field \"Target
  environment\" is required") and a console error before anything is typed.
  The preview recovers once the field is filled. This is noise rather than a
  failure; a follow-up could skip the request until required fields have
  values.
- **O4:** On #24 the CI card's way into CI is a "show the run" control, not
  an `<a href="/ci/…">` link, and it was not followed: the run id was a stub
  (`run-walk-24`) with no run behind it. The verdict and detail stand alone
  without it, as `TaskCIStatus` intends.
- **O5:** Bulk edit's Task priority is a checkbox ("change this field")
  with a number box beside it, both labelled "Task priority". Assistive
  technology and `getByLabel` therefore meet the checkbox first. It is
  operable, but the two controls cannot be told apart by name.
- **Not exercised:** the Internal PR link into the pull request page's diff
  (it needs a branch pushed into the simulator's bare repository;
  `…-task-3458.md` covers it), and the `resume_later` park reason on the
  page (step 33).
