# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-03

- **Date:** 2026-11-03, 03:01–04:50 UTC
- **Task:** grain/049f1e09/task-2698, base `main`
- **Commit tested:** `0fdeff3b` (`main` at the start of the run, "Merge pull
  request #540"), the build the rail's version link names
  (`v0.0.0-20261103022227-0fdeff3b8161`). The two fixes below were checked
  against a rebuild of this branch.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
    unless a step says 390×844
  - Frontend `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`,
    so the embedded `pkg/ui/static` was current.
  - `:8420`: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -fork-pull-request -access-code-file <file>`, with
    `TMPDIR=/var/tmp/walk`, so the store was a throwaway embedded SQLite file.
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
    `sqlite3` was installed with apt for the stubs.
- **How it was driven:** throwaway Playwright scripts (not committed). They sign
  in with `POST /api/auth/access-code`, then act only through visible controls.
  Every page logged `pageerror`, console errors and warnings, responses of 400
  or more (with their bodies), failed requests, every dialog (its text and
  whether it was accepted), and every non-GET `/api/` request with its body.
  The REST API was used only to read back what a gesture had persisted,
  except where a stub below says otherwise.

## Stubs

The server, store, built frontend, browser and gestures were real throughout.
Each stub stands in for something that only an agent run, a reconciler or an
external system writes:

1. **Host-script approvals** (a run's runhost): sqlite `task_grant`
   `('2','local-scripts:deploy-tools','operator')`, then two
   `POST /api/tasks/2/local-script-approvals` (`walk-1`, `walk-2`) with a
   42-byte stdin preview and sha256 `abc123`.
2. **Held-for-approval proposal** (an agent's `propose_task
hold_for_approval`): sqlite `origin_behalf_kind='task', origin_behalf_id='3'`
   on #12 and a `task_tag` row `held-for-approval`.
3. **Pending secret** (an agent's `request_secret`):
   `task_observation.pending_secret='stripe-api-key'` on running #8.
4. **Suspended session** (a run that suspends): a `task_observation` row for
   #2 with `suspended_session_*` set (`demo-run-2-1`, Antigravity).
5. **Template priority:** `PATCH /api/templates/template-2 {"priority":8}`
   (setup only; the Templates page's own editor was covered on 11-02).
6. **Config-repo sync failure** (no state repository in `grain demo`):
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config`.
7. **`settings/new-task-form.yml`** (no config repository): `page.route` added
   `newTaskForm: {default: {fields: {customFields: {visibility: hidden}}}}`
   to `GET /api/config`.
8. **Inbox empty and error states:** `page.route` emptied the arrays in
   `GET /api/tasks`, `/api/notifications` and `/api/local-script-approvals`,
   and in a second pass answered `GET /api/tasks` with a 500 on a cold load
   (notifications and local scripts still empty).
9. **A close and reopen with a long reason** (what an agent or the CLI sends;
   the page's Close sends none): `POST /api/tasks/36/close {"reason":…}` and
   `POST …/36/reopen {"reason":…}`, after the page's own Close and Reopen.

## Steps and outcomes

### Inbox

| #   | Step                                                         | Outcome                                                                                                                                                                                                                                                                                                                            | Verdict |
| --- | ------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail's Inbox from `/`, then `/inbox` directly                | Both on `/inbox`. Rail `Inbox 25`. Groups: Notifications 3 (collapsed), Local scripts waiting for approval 2, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 2 — they sum to 25, the two local-script calls included. | PASS    |
| 2   | Notifications toggle by keyboard                             | Starts `aria-expanded=false` with its count shown. Enter → true, Space → false, Enter → true.                                                                                                                                                                                                                                      | PASS    |
| 3   | Collapse Approve a proposal by click; reload                 | After the reload Notifications is still expanded and the proposal group still collapsed.                                                                                                                                                                                                                                           | PASS    |
| 4   | Row navigation                                               | Proposal #1's title opens `/tasks/1`; Back returns to `/inbox`.                                                                                                                                                                                                                                                                    | PASS    |
| 5   | Local scripts group (stub 1)                                 | Each row: "Task 2", `deploy.sh "--env" "staging-1" (and 42 bytes on stdin)`, "On stdin: 42 bytes, sha256 abc123", the preview, Approve / Deny. Approve on walk-1 sends `…/lsa-1/answer {"approve":true}`; the group goes 2→1.                                                                                                      | PASS    |
| 6   | Stuck group sub-cases                                        | #5 (merge queue, `syncEntry`) Open task only; #21 (`reconcileDispatch`) Run now; #22 (`ProcessResult`) Cancel; #23 (`reconcileWake`) Undefer.                                                                                                                                                                                      | PASS    |
| 7   | Run now #21; Cancel #22 dismissed then accepted; Undefer #23 | Run now: `POST …/21/run-now`, group 4→3, #21 `queued`. Cancel confirms "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later."; dismissing sends nothing (#22 `running`); accepting sends `…/22/close`, group →2, #22 `closed`. Undefer: `…/23/undefer`, group →1, #23 `queued`.               | PASS    |
| 8   | Retry merge #25 (Unblock a merge)                            | `POST …/25/retry-merge`, no confirm; the group is gone; #25 `completed`.                                                                                                                                                                                                                                                           | PASS    |
| 9   | Approve proposal #1                                          | `POST …/1/approve`; group 2→1; #1 `queued`, approved.                                                                                                                                                                                                                                                                              | PASS    |
| 10  | Reply on #4                                                  | Send disabled while empty, enabled once typed; `POST …/4/comments`; Asked: reply 2→1; #4 `queued`.                                                                                                                                                                                                                                 | PASS    |
| 11  | Ship as is on #28 (review decision)                          | `POST …/28/submit`; the review group is gone; #28 `completed`.                                                                                                                                                                                                                                                                     | PASS    |
| 12  | Submit inline only when submittable                          | CL rows #14, #16, #17, #18, #19 offer no Submit and say why ("Not submittable yet: …", "behind head: sync it"); #15 (presubmit passed, LGTM) and the PR rows do. Submit #15 → `…/15/submit`, group 9→8, #15 closed and listed under Merged; Submit #6 → 8→7, #6 `completed`.                                                       | PASS    |
| 13  | Reload                                                       | Notifications 3, local-scripts 1, Asked: reply 1, submit 7, Approve on GitHub 1, stuck 1, proposals 1 = rail `Inbox 15`.                                                                                                                                                                                                           | PASS    |
| 14  | Config-repo sync failure (stub 6)                            | "Settings are not being exported 1" is drawn second, after Notifications; rail 15→16; its Config repo button opens `/settings/config-repo`.                                                                                                                                                                                        | PASS    |
| 15  | Empty state (stub 8)                                         | "Nothing is waiting on you. Every task is either running, waiting or done."                                                                                                                                                                                                                                                        | PASS    |
| 16  | Error state: cold load with `GET /api/tasks` → 500 (stub 8)  | **Before the fix** the toast "500 Internal Server Error" showed and the page said "Nothing is waiting on you. Every task is either running, waiting or done." — about tasks it never read (bug 1). With the fix it says "The task list could not be read, so this page cannot show what is waiting on you yet."                    | FIXED   |

### New task

| #   | Step                                                                                                                                                              | Outcome                                                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 17  | Rail's Create options                                                                                                                                             | New task / New session / New board; New task opens the form at `/new`.                                                                                                                                                                                                                                                                                                                       | PASS    |
| 18  | Task priority `0`, `-2`, `1.5`, then `1e3`, `7`                                                                                                                   | The first three: "a whole number, 1 or more; default 5, higher numbers run first", field invalid, Create task disabled. `1e3` and `7` are accepted. Task priority is drawn below Advanced options, not inside it, so the refusal stays in view while Create is disabled.                                                                                                                     | PASS    |
| 19  | Refused field inside Advanced options                                                                                                                             | vCPUs `-3`, Advanced collapsed, Create task: no POST; Advanced opens and vCPUs has focus.                                                                                                                                                                                                                                                                                                    | PASS    |
| 20  | Fork head labels, Create a new branch unticked                                                                                                                    | `contrib:fix/readme-typo` → "GitHub fork head: branch fix/readme-typo on contrib's fork. grain files this task against the open pull request on acme/widgets it heads." `octo:cat:x`, `:nobranch` and `octocat:bad..branch` are refused with the server's own wording. `octocat:fix-typo` (no such PR) is refused by the server: "…no open pull request on acme/widgets has it as its head". | PASS    |
| 21  | File the fork head task with Task priority `1e3`                                                                                                                  | #31 `queued`, priority **1000** (the 11-02 `1e3` fix holds), base `main`, linked to acme/widgets#41 with the fetch instructions in its description.                                                                                                                                                                                                                                          | PASS    |
| 22  | Model picker                                                                                                                                                      | "Deployment default (gemini-2.5-pro)", `gemini-2.5-pro`, `gemini-2.5-flash` only.                                                                                                                                                                                                                                                                                                            | PASS    |
| 23  | Queued path: title, two-paragraph description, `notes.txt`, acme/widgets, base `release/1.0`, Requested by, auto-merge off, vCPUs 2, Memory 4096, Task priority 7 | One POST → #32 `queued`, approved, `release/1.0`, `autoMerge:false`, priority 7, `requested-by: Walker`, 2 vCPU / 4096 MiB, description intact, one attachment.                                                                                                                                                                                                                              | PASS    |
| 24  | Proposed path, No repo                                                                                                                                            | Queue immediately unticked; `"noRepo":true` → #33 `proposed`, with `"base":""`. Base branch is still drawn with No repo (O5 of 10-29), but not sent.                                                                                                                                                                                                                                         | PASS    |
| 25  | Cancel                                                                                                                                                            | Escape closes without a confirm; reopening restores the draft title; Discard draft clears it.                                                                                                                                                                                                                                                                                                | PASS    |
| 26  | From a template (security-audit, priority 8, stub 5)                                                                                                              | Opened from the template's page: Template picker offers No template / security-audit / release-qualification (not the GKE one, which has input fields). Task priority shows 8, "default 8, from the template". Typed 3 survives switching to No template and back; filed #34 at priority 3 with the template's title.                                                                        | PASS    |
| 27  | Base branch while a repo-bound template is picked                                                                                                                 | The Base branch box held `release/1.0` (prefilled from acme/widgets' last task) and the POST sent it, but the task was filed on the template's `main` (`ui.Client.fromTemplate`: a bound template's repo and branch win). The helper said only "always files against acme/widgets" (bug 2). It now says "…always files against acme/widgets on main, whatever Repos and Base branch say".    | FIXED   |
| 28  | Template with input fields                                                                                                                                        | The GKE template's row offers "Use…", which opens "File a task from a template" with its four inputs.                                                                                                                                                                                                                                                                                        | PASS    |
| 29  | `/new?field.requested-by=Linked%20Person&field.priority=low`                                                                                                      | Requested by "Linked Person", Priority low. `/new?field.nonexistent=x&field.estimate-days=abc` names both as "not a field on this deployment" (the field is `estimate`). With stub 7, `/new?field.priority=high` says "Priority: “high” is in a field this form hides."                                                                                                                      | PASS    |
| 30  | Capabilities                                                                                                                                                      | acme/widgets preselects Self debug, GCP key, Gemini key (`POST /api/capability-grants/check`); filed #35 carries those plus `grain-ci`. No policy drops a default grant in the demo, so no toast; not reachable here.                                                                                                                                                                        | PASS    |

### Tasks list

| #   | Step                                  | Outcome                                                                                                                                                                                                                                                                                                                   | Verdict |
| --- | ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 31  | Rail Tasks, Blocked, Merged, Proposed | Tasks → `/?f=state:none:deferred\|closed`; Blocked → #11; Merged → #15; Proposed → #37, #12, #33, #36.                                                                                                                                                                                                                    | PASS    |
| 32  | Row ovals and chips                   | #11 "Blocked on: #8 (running)"; #5 "Merging"; P1000/P7/P8 badges titled "Task priority N (default 5; …)" on non-default tasks only; #3 and #8 "live run" titled "running/live_run · Driven by ProcessResult · …"; #12 `imported` ("imported from GitHub issue acme/widgets#42"), "held for approval by 3" and unapproved. | PASS    |
| 33  | Search, reload, Back/Forward          | "pagination" → `q=pagination` (#8, #11); reload keeps URL, box and rows; Back from `/tasks/8` returns to the search, Forward to `/tasks/8`.                                                                                                                                                                               | PASS    |
| 34  | Origin filter deep link               | `/?f=origin:any:imported` lists #12 only.                                                                                                                                                                                                                                                                                 | PASS    |

### Task detail

| #   | Step                                                                              | Outcome                                                                                                                                                                                                                                                                                                                                                                                                              | Verdict |
| --- | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 35  | #36: Defer with "Until" 2026-11-20, Undefer, Defer with no date, Undefer, Approve | `{"deferredUntil":"2026-11-20T00:00:00.000Z"}` → `deferred` until 11-20; Undefer → `proposed`; `{"deferredUntil":""}` → `deferred` with no date; Undefer → `proposed`; Approve → `queued`.                                                                                                                                                                                                                           | PASS    |
| 36  | Add and remove a dependency on #8                                                 | "Add a dependency…" → `…/depends-on {"id":"8","attach":true}`; the chip reads "8 Add pagination… (running)"; #36 shows "Blocked on: #8 (running)" and the rail goes Blocked 1→2. The chip's × sends `attach:false`; the rail goes back to Blocked 1.                                                                                                                                                                 | PASS    |
| 37  | Close and Reopen from #36's page, then stub 9                                     | `…/close {"close_pull_request":false}` (no confirm) → `closed`; Reopen → `queued`. The timeline reads "Closed by human debian" / "Reopened … by human debian · Reopened: Closing this because …". At 390×844 the long-reason entries are 334 px wide inside a 390 px page (`scrollWidth` 390): they wrap.                                                                                                            | PASS    |
| 38  | Pause / Resume queued #2; Pause now / Resume running #3                           | `…/2/pause`; Resume is offered after a reload; `…/2/resume`; #2 stays `queued`. `…/3/pause {"immediate":true}`; Resume appears without a reload; #3 stays `running`.                                                                                                                                                                                                                                                 | PASS    |
| 39  | Work tree on #3                                                                   | "Provenance · Work from task 13: 0 of 4 done", nested 13 ▸ 1, 3 (running), 8 (PR open, acme/widgets#104) ▸ 11. #3 itself is drawn as text, not a self-link (the 11-02 change holds).                                                                                                                                                                                                                                 | PASS    |
| 40  | Merge policy on #26                                                               | "Held by this repository's gating triggers (human: required)." with `.grain/triggers/payments.yml: review task 27` and the trigger's waiting line.                                                                                                                                                                                                                                                                   | PASS    |
| 41  | CL actions on #14, #17, #19                                                       | #14: Submit disabled, titled "Not submittable yet: review is not approved (changes requested); required presubmit has not passed on snapshot 3 (failed)"; Mail disabled; Publish replies (2) → `…/change-publish`, "Published 2 replies on cl/612400001." #17: Mail → `…/change-mail`, "Mailed cl/612400103…", and Mail is then disabled. #19: Sync → `…/change-sync`, "Synced cl/612400105: synced to head (demo)." | PASS    |
| 42  | Pending secret on #8 (stub 3)                                                     | The request box names `stripe-api-key/value`, write-only. Set secret → "Set stripe-api-key. Its value is in grain's secret store, not in this conversation…"; the value appears nowhere in the page after a reload.                                                                                                                                                                                                  | PASS    |
| 43  | Suspended session on #2 (stub 4)                                                  | "Left by demo-run-2-1 on 11/3/2026, 2:00:00 AM (Antigravity). Any further run of this task resumes this conversation…".                                                                                                                                                                                                                                                                                              | PASS    |
| 44  | Host-script approvals on #2 (stub 1)                                              | Deny on walk-2 from the pane (`{"approve":false}`); timeline "Approved local script call walk-1 (deploy-tools)" and "Denied local script call walk-2 (deploy-tools)", each with its argv.                                                                                                                                                                                                                            | PASS    |
| 45  | Parked question on #7                                                             | "Waiting for your reply" callout; a reply from it posts `…/7/comments` and #7 goes `queued`.                                                                                                                                                                                                                                                                                                                         | PASS    |
| 46  | Full prompt on #3                                                                 | "prompt for 3 · attempt #1 · 10 kb · ~2,683 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`.                                                                                                                                                                                                                                                                                                      | PASS    |
| 47  | Force merge on #5                                                                 | Offered on #5 (waiting on CI on the merge queue). Its prompt was exercised on 11-02 and not repeated.                                                                                                                                                                                                                                                                                                                | N/A     |

### Selection, bulk edit, manual mode

| #   | Step                                            | Outcome                                                                                                                                                                                                                            | Verdict |
| --- | ----------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 48  | Queued view, tick #31, #32, #34                 | "3 selected" with Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, Attach, Detach, Clear selection. Close confirms "Close 3 task(s)?"; dismissing sends nothing and keeps the selection. | PASS    |
| 49  | Bulk edit… → Cancel                             | Base branch, auto-merge, Task priority and Requested by say "(mixed across the selection)". Cancel sends nothing; the selection stays.                                                                                             | PASS    |
| 50  | Bulk edit Task priority: `0`, then `1e3`, Apply | `0` → the shared refusal and Apply disabled. `1e3` → three `PATCH {"priority":1000}` and nothing else; base and auto-merge unchanged on each; the selection is cleared.                                                            | PASS    |
| 51  | System → Pipeline → Pause pipeline              | Confirm "Pause the pipeline? …", `POST /api/pipeline-pause`. Queued rows carry "waits for Run now" except #11 (blocked) and #21 (already started in step 7).                                                                       | PASS    |
| 52  | Select #32, #34 → Run selected now; reload      | `…/run-now` for each; their markers are gone and stay gone after the reload. Resume pipeline confirms and sends `DELETE /api/pipeline-pause`.                                                                                      | PASS    |

### Console and network

No uncaught page errors. The only console errors and 4xx/5xx responses were
the deliberate ones: the stubbed 500s (step 16, which also log "grain: poll
failed" warnings) and the server's refusal of a fork head with no pull request
(step 20). The only failed requests were polls aborted by navigation.

## Bugs

### 1 — The Inbox said nothing was waiting when it had not read the tasks (fixed)

**Repro (base `0fdeff3b`):** answer `GET /api/tasks` with a 500 and
`/api/notifications` and `/api/local-script-approvals` with `[]`, then open
`/inbox` cold. The toast shows the 500, and the page says "Nothing is waiting
on you. Every task is either running, waiting or done." App passes the Inbox
the empty array it starts with, and the Inbox cannot tell "not read" from
"none".

**Fix:** App records whether `/api/tasks` has answered with a list once
(`tasksListed`) and passes it to the Inbox as `tasksLoaded`. Until then the
Inbox says "The task list could not be read, so this page cannot show what is
waiting on you yet. It tries again on its own." and not the empty-state claim.
Covered by `InboxPage.test.jsx` ("does not say nothing is waiting before the
task list has been read") and `ui/e2e/inbox-unread-task-list.spec.js`.

### 2 — A repo-bound template's branch was not named beside its repo (fixed)

**Repro:** file a task on acme/widgets with base `release/1.0`, then open New
task from security-audit's page (bound to acme/widgets, base `main`). Base
branch is prefilled `release/1.0`; the Template helper says "fills what this
form leaves blank; always files against acme/widgets". Create task sends
`"base":"release/1.0"` and the task is filed on `main`.

**Fix:** the helper now names the branch and says the form's own boxes do not
apply: "fills what this form leaves blank; always files against acme/widgets
on main, whatever Repos and Base branch say". The server's precedence is
deliberate (`pkg/ui/templates.go`) and is unchanged. Covered by
`NewTaskOverlay.formConfig.test.jsx` ("names a bound template's branch beside
its repo").

## Observations

- **O1:** the 11-02 O1 (the error toast coming and going with the polls)
  still holds. Bug 1's notice is what the page shows under it now.
- **O2:** Close on a task page asks nothing and sends no reason. A reason
  reaches the timeline only from an agent or the CLI (stub 9).
- **O3:** a fork-head task with an empty description is filed with one that
  starts with a blank line and `---`, ahead of grain's note. It renders as a
  rule.
- Not reachable in the demo: the capability-policy toast for dropped default
  grants, cross-team approvals, an inline config-repo change card, and
  external pull request chips. No row or seed carries them.
