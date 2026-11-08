# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-08

- **Date:** 2026-11-08, 03:30–04:15 UTC
- **Task:** grain/098073e4/task-3158 (attempt 2), base `main`
- **Commit tested:** `d7dde0c2` (`main` at the start of the run, "Merge pull
  request #877"), which is the build the rail's version link names
  (`v0.0.0-20261108030308-d7dde0c23839`). The two fixes below were checked
  against a rebuild of this branch on a second server.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`, Node.js
    `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
    unless a step says 390×844
  - `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`, so the
    embedded `pkg/ui/static` was current.
  - `:8420` (base build): `grain demo -addr 127.0.0.1:8420 -open=false
-github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -fork-pull-request -access-code-file <file>`, with
    `TMPDIR=/var/tmp/walk`, so the store was a throwaway embedded SQLite file.
  - `:8421` (this branch's build): `grain demo -github-sim -fork-pull-request`
    over its own throwaway store, for the fixes only.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. `sqlite3` was installed with apt
    for stub 1.
- **How it was driven:** throwaway Playwright scripts (not committed). They sign
  in with `POST /api/auth/access-code`, then act only through visible controls.
  Every page logged `pageerror`, console errors and warnings, responses of 400
  or more (with their bodies), failed requests, every dialog (its text and
  whether it was accepted or dismissed), and every non-GET `/api/` request with
  its body. The REST API was used only to read back what a gesture had
  persisted, except where a stub below says otherwise.

## Stubs

The server, store, built frontend, browser and gestures were real throughout.
Each stub stands in for something that only an agent run, a reconciler or an
external system writes:

1. **Host-script approvals** (a run's runhost): sqlite `task_grant`
   `('2', 'https://grain.dev/capabilities/grain#host-script={"scripts":["deploy.sh"]}', 'operator')`,
   then three `POST /api/tasks/2/local-script-approvals` (`walk-1`, `walk-2`,
   and `walk-3` with `stdin_bytes: 42`, a two-line `stdin_preview` and
   `stdin_sha256: abc123`).
2. **Template priority:** `PATCH /api/templates/template-2 {"priority":8}`
   (setup only).
3. **Config-repo sync failure** (`grain demo` has no state repository):
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config`.
4. **`settings/new-task-form.yml`** (no config repository): `page.route` added
   `newTaskForm: {default: {fields: {customFields: {visibility: hidden}}}}`
   to `GET /api/config`.
5. **Inbox empty and error states:** `page.route` emptied `GET /api/tasks`,
   `/api/notifications` and `/api/local-script-approvals`, then in a second
   pass answered `GET /api/tasks` with a 500 on a cold load.
6. **A close and reopen with a long reason** (what an agent or the CLI sends;
   the page's Close sends none): `POST /api/tasks/32/close {"reason":…}` and
   `POST …/32/reopen {"reason":…}`, with a 400-character reason ending in an
   unbroken 115-character token.

## Steps and outcomes

### Inbox

| #   | Step                                                          | Outcome                                                                                                                                                                                                                                                                                                         | Verdict |
| --- | ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail's Inbox from `/`, then `/inbox` directly                 | Both open `/inbox`. Rail `Inbox 23`: Notifications 3 (collapsed, count shown), Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 2. These sum to 23.                                                  | PASS    |
| 2   | Notifications by keyboard                                     | Starts `aria-expanded=false`. Enter → true, Space → false, Enter → true. It lists the flaky-e2e notice, the v2.4.0 promotion (Approve / Reject / Acknowledge / Investigate) and the stalled-merge notice.                                                                                                       | PASS    |
| 3   | Collapse Approve a proposal by click; reload                  | After the reload Notifications is still expanded and Approve a proposal still collapsed.                                                                                                                                                                                                                        | PASS    |
| 4   | Stuck group sub-cases                                         | #5 (`syncEntry`, merge queue) offers Open task only; #21 (`reconcileDispatch`) offers Run now; #22 (`ProcessResult`) offers Cancel; #23 (`reconcileWake`) offers Undefer. Each note names the part of grain that should have moved the task on.                                                                 | PASS    |
| 5   | Run now #21; Cancel #22 dismissed, then accepted; Undefer #23 | Run now → #21 leaves the group (4→3), `queued`. Cancel asks "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later."; dismissing sends nothing (#22 still `running`); accepting sends `…/22/close` and #22 is `closed`. `…/23/undefer` → #23 `queued`. The rail falls 22→20. | PASS    |
| 6   | Retry merge on #25 (Unblock a merge)                          | `POST …/25/retry-merge`; the group is gone.                                                                                                                                                                                                                                                                     | PASS    |
| 7   | Approve proposal #1                                           | `…/1/approve`; #1 `queued`. #12 still shows `imported` and `unapproved` with Approve / Defer / Decline.                                                                                                                                                                                                         | PASS    |
| 8   | Parked question #4: Reply, Send                               | Reply opens an inline box; Send posts `…/4/comments {"body":"Only unblock the merge queue."}`; #4 `queued`; Asked: reply 2→1.                                                                                                                                                                                   | PASS    |
| 9   | Review decision #28: Ship as is                               | Offers Reply and Ship as is; Ship as is sends `…/28/submit`; #28 `completed` (merge queue); the group is gone.                                                                                                                                                                                                  | PASS    |
| 10  | CL rows in Asked: submit for merge                            | Submit is inline only on submittable rows: #15 (CL), and PRs #6, #24, #26. #14, #16, #17, #18 and #19 carry their "Not submittable yet: …" reason and no Submit. Submit on #15 sends `…/15/submit`; #15 closes as merged (rail Merged 1, `/?state=merged` lists it). The rail reads 15.                         | PASS    |
| 11  | Local scripts (stub 1)                                        | The "Local scripts waiting for approval 3" group appears and the rail goes 15→18. Each row says `deploy.sh "--env" "staging-1"`; walk-3 adds "(and 42 bytes on stdin)", "On stdin: 42 bytes, sha256 abc123" and the preview.                                                                                    | PASS    |
| 12  | Deny a call on task #2's page; reload                         | `…/lsa-1/answer {"approve":false}`; after the reload the timeline has "Denied local script call walk-1 (deploy-tools)"; the Inbox group reads 2.                                                                                                                                                                | PASS    |
| 13  | Config-repo sync failure (stub 3)                             | A "Settings are not being exported 1" group explains the backup gap. The rail's 17 equals the groups (3+2+1+7+1+1+1 = 16) plus the failure.                                                                                                                                                                     | PASS    |
| 14  | Empty and error states (stub 5)                               | Empty: "Nothing is waiting on you. Every task is either running, waiting or done." 500: "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own."                                                                                                      | PASS    |

### New task

| #   | Step                                                 | Outcome                                                                                                                                                                                                                                                                                                                                                                                                         | Verdict                      |
| --- | ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| 15  | `+ New task`, every field (filed #30)                | Focus lands on Title. Set: title, description, `notes.txt` attachment, repo acme/widgets, base `release/1.0`, reviewer `alice`, Task priority 3, auto-merge unticked, Advanced → vCPUs 2 and Network access "none". The model picker offers only "Deployment default (gemini-2.5-pro)", `gemini-2.5-pro` and `gemini-2.5-flash` (the demo's one connection). #30 read back with every value and its attachment. | PASS                         |
| 16  | Task priority `0`, `-2`, `2.5`                       | Each: "a whole number, 1 or more; default 5, higher numbers run first", `aria-invalid=true`, Create task disabled.                                                                                                                                                                                                                                                                                              | PASS                         |
| 17  | Proposed path (#31)                                  | Queue immediately unticked → #31 `proposed`.                                                                                                                                                                                                                                                                                                                                                                    | PASS                         |
| 18  | Fork head with Create a new branch unticked          | `alice:feature-x` → "GitHub fork head: branch feature-x on alice's fork. grain files this task against the open pull request on acme/widgets it heads." `alice:` and `:x` → "needs both an owner and a branch"; `a:b:c` → "has more than one ':'". **Create task stayed enabled for the malformed labels, and pressing it returned the same words as a 400 (bug 1, fixed).**                                    | FAIL on base, PASS after fix |
| 19  | Cancel                                               | Escape closes the form and keeps the draft; reopening restores it; Discard draft empties it; nothing filed.                                                                                                                                                                                                                                                                                                     | PASS                         |
| 20  | No repo (#32)                                        | Repos → "No repo (standalone task -- nothing to check out)" files `noRepo:true`, `queued`.                                                                                                                                                                                                                                                                                                                      | PASS                         |
| 21  | Rail's create menu                                   | "Create options" lists New task, New session, New board; New task opens the form on `/new`.                                                                                                                                                                                                                                                                                                                     | PASS                         |
| 22  | Template priority (stub 2), from the template's page | "New task from this template" opens `/new` with the picker: No template / security-audit / release-qualification (not the field-carrying GKE template). security-audit → 8, release-qualification → 5, back → 8; edited to 4, switching keeps 4. #33 filed at priority 4 against the template's `main`.                                                                                                         | PASS                         |
| 23  | Template with input fields                           | Its Use… on Processes → Templates opens "File a task from a template" with its inputs, not the New task form.                                                                                                                                                                                                                                                                                                   | PASS                         |
| 24  | `/new?field.priority=high&field.area=backend&…`      | Notice: "This link asked for values this form has left out: “nosuch” is not a field on this deployment; Estimate (days): “x” is not one of its values."; Priority and Area prefilled. With stub 4: "Priority: “high” is in a field this form hides; Area: “backend” is in a field this form hides."                                                                                                             | PASS                         |

### Tasks list

| #   | Step                          | Outcome                                                                                                                                                                                                                                                                                                                | Verdict |
| --- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 25  | Every rail state view         | Tasks, Proposed, Waiting, Running, Asked, Merging (`?state=completed`), Deferred, Closed, Merged (`?state=merged&sort=merged:desc`, #15) and Blocked (#11) each list exactly the tasks in that state.                                                                                                                  | PASS    |
| 26  | Search, reload, Back/Forward  | "retry budget" → `?q=retry+budget…`, rows 14,16–19; survives reload; Back returns to the Merging view and Forward to the search.                                                                                                                                                                                       | PASS    |
| 27  | Filters → Add filter → Origin | Offers Filed by hand / Proposed by an agent / Imported issue. Imported issue → `&f=origin:any:imported`, row #12 only, "filters (2)", survives reload; Clear drops it back to the default view.                                                                                                                        | PASS    |
| 28  | Row chips                     | Blocked shows its own "blocked on task" oval; merge-queue rows read "on merge queue"; non-default priority shows its badge; the live-run chip's title gives "Driven by ProcessResult · Waiting on the run in flight · "Cancel" ends this wait"; #12's `imported` chip is "imported from GitHub issue acme/widgets#42". | PASS    |

### Task detail

| #   | Step                                     | Outcome                                                                                                                                                                                                                                                                                                                              | Verdict                      |
| --- | ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------- |
| 29  | Edit #30                                 | Task priority 0 disables Save with the shared wording; title, description, reviewers `alice, bob` and priority 7 save in one `PATCH`, read back, and appear in the timeline as an edit.                                                                                                                                              | PASS                         |
| 30  | Auto-merge and base on the pane          | Auto-merge toggles with `PATCH {"autoMerge":…}` each way. Base `release/2.0` is refused: "does not exist on acme/widgets: push it first, or have the task depend on the one that creates it".                                                                                                                                        | PASS                         |
| 31  | Defer without and with a date; Undefer   | `…/defer {"deferredUntil":""}` → `deferred`, page offers Undefer; Undefer → `queued` with auto-merge kept; Until 2026-12-01 → `deferredUntil: 2026-12-01T00:00:00Z`.                                                                                                                                                                 | PASS                         |
| 32  | Pause / Resume on a queued task          | `…/pause` keeps the state `queued` with `paused.fromState=queued`; the page offers Resume; Resume clears it and keeps auto-merge.                                                                                                                                                                                                    | PASS                         |
| 33  | Close / Reopen; timeline at 390 px       | Close and Reopen land; the timeline says "Closed · by operator (access code)" and "Reopened · by operator (access code)". With stub 6 at 390×844, `scrollWidth` stays 390 and no reason element runs past the viewport.                                                                                                              | PASS                         |
| 34  | Add and remove a dependency (#33 on #32) | "Add a dependency…" with `32` + Enter → `…/depends-on {"id":"32","attach":true}`; the chip reads "#32 Walk: no repo" with the hover "32 Walk: no repo — Waiting (blocking this task)"; rail Blocked 1→2, Blocked view 11,33. Delete on the focused chip detaches it; Blocked back to 1.                                              | PASS                         |
| 35  | CL actions                               | #14 Submit disabled with "Not submittable yet: review is not approved (changes requested); …"; Publish replies (2) sent `…/change-publish` and is gone after reload; Mail is disabled once mailed. #17 Mail → `…/change-mail`, "Mailed cl/612400103…", "already mailed". #19 Sync → `…/change-sync`, "Synced cl/612400105".          | PASS                         |
| 36  | Force merge on #24 (waiting on CI)       | A prompt asks "Force this merge? … Say why. It is required". Dismissing sends nothing; an empty reason is refused by the server ("forcing a merge needs a reason…"); with a reason it sends `…/force-merge {"reason":…}`, and the page explains it lands on the next merge-queue cycle.                                              | PASS                         |
| 37  | Pause now / Resume on running #3         | **Pause now sent `…/pause {"immediate":true}` at once, with no confirmation, although its own tooltip says whatever the run has not pushed is lost (bug 2, fixed).** After the fix it asks first; dismissing sends nothing. Paused, the task keeps `running` with the note "Paused by operator … (was Running)"; Resume restores it. | FAIL on base, PASS after fix |

### Selection bar and manual mode

| #   | Step                                               | Outcome                                                                                                                                                                                                                           | Verdict |
| --- | -------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 38  | Waiting view, tick #30, #32, #33; Close dismissed  | "Close 3 task(s)?"; dismissing sends nothing and keeps the selection.                                                                                                                                                             | PASS    |
| 39  | Bulk edit… → Cancel; Task priority `-1`, then `9`  | Mixed fields say "(mixed across the selection)". Cancel sends nothing and keeps the selection. `-1` disables Apply. `9` sends three `PATCH {"priority":9}` and nothing else; base and auto-merge unchanged; the selection clears. | PASS    |
| 40  | Defer, Undefer, Attach/Detach, Close, Reopen       | One call per ticked task each; states land (`deferred`/`queued`/`closed`/`queued`). Attach and Detach of "API preset: task-admin" add and then remove `api:task-admin` on both tasks.                                             | PASS    |
| 41  | Approve #31, Submit #6 from the bar                | #31 `queued`; #6 `completed` (merge queue).                                                                                                                                                                                       | PASS    |
| 42  | System → Pipeline → Pause pipeline                 | Confirms "Pause the pipeline? …", `POST /api/pipeline-pause`. Every queued row is marked "waits for Run now" except #11 (blocked) and #21 (already started by Run now in step 5).                                                 | PASS    |
| 43  | Select #30, #32 → Run selected now; reload; Resume | `…/run-now` each; their markers are gone and stay gone after the reload. Resume pipeline confirms and sends `DELETE /api/pipeline-pause`.                                                                                         | PASS    |

### Console and network

No uncaught page errors. The only 4xx/5xx responses and console errors were the
deliberate ones: the refused base (step 30), the malformed fork label on base
(step 18) and the empty Force merge reason (step 36), plus the stubbed 500 in
step 14. The only failed requests (79) were `GET /api/tasks` and
`/api/tasks/<id>` polls aborted by `page.goto`, `page.reload` or the browser
closing (the UI aborts none itself).

## Bugs

### 1 — Create task stayed enabled for a malformed fork head label (fixed)

**Repro (base `d7dde0c2`):** `/new`, Title "x", Repos acme/widgets, leave
Create a new branch unticked, Base branch `a:b:c` (or `alice:`). The box says
`"a:b:c" has more than one ':': a fork head is <owner>:<branch>` and is marked
invalid, but Create task stays enabled; pressing it sends `POST /api/tasks`
and the server answers 400 with the same words. Noted as O4 on 11-04.

**Fix:** `NewTaskOverlay.jsx` disables Create task while Base branch parses as
a malformed fork head (`parseForkHead(base).error`, where BranchField takes
labels), just as it does for an invalid Task priority. On `:8421`: `a:b:c` and
`alice:` disable it, `alice:feature-x` and `release/1.0` leave it enabled.
Covered in `NewTaskOverlay.repos.test.jsx` ("accepts a GitHub fork head label
as the base branch").

### 2 — Pause now stopped a running task with no confirmation (fixed)

**Repro (base `d7dde0c2`):** open a running task (#3 in the demo) and press
Pause now. `POST /api/tasks/3/pause {"immediate":true}` is sent at once. The
button's own tooltip says "Whatever the run has not pushed is lost", and Cancel
on the same page asks first.

**Fix:** `detail/Actions.jsx` asks "Pause 3 now? Its run is stopped at once,
without the grace period to push its work and write its handover; whatever it
has not pushed is lost. Resume picks the task back up where it was left." and
pauses only once confirmed. On `:8421` dismissing sent nothing and accepting
paused the task. Covered in `detail/Actions.test.jsx` ("asks before Pause now
stops a running task, and pauses only once confirmed").

`npx vitest run` over the two changed test files and `NewTaskOverlay.test.jsx` passed (96 tests), and so did
`npx playwright test` over `external-pr-chip*.spec.js`, `task-pause.spec.js`,
`new-task-form-config.spec.js` and `new-task-number-boxes.spec.js` (18
tests). Screenshots of `/new` and `/tasks/8` at desktop and phone width, light
and dark, were taken before (`:8420`) and after (`:8421`) with
`ui/e2e/screenshot.mjs --url`. Without a Gemini key they could not be
described, so the guarded form was measured instead at 1440 and 390 px, light
and dark. There was no horizontal overflow, Create task sat inside the
viewport with MUI's standard disabled colours, and the Base branch helper
stayed within its field.

## Observations

- **O1:** with no repo picked, Create task is disabled with nothing on the form
  saying why (Repos carries no required mark). Title does carry one.
- **O2:** the New task form offers no Admission or Hosting choice. A task's
  page and the template and schedule editors do.
- **O3:** the Inbox's local-script rows name the task and the command but not
  the call id (`walk-1`); the task page's timeline does.
- **O4:** the dependency picker's results are a `MenuList` under a plain text
  box (no `role=combobox`/`aria-expanded`). Keyboard use works (type, arrows,
  Enter), but a screen reader hears no popup.
- **O5:** the rail's entries are `role=button` divs, not links, so they cannot
  be opened in a new tab.
- **O6:** the selection bar's capability picker offers presets marked
  "(dangerous)" (deployment-ops, self-upgrade, tasks:admin, merge:force) and
  attaches them without a confirmation. Only the non-dangerous task-admin was
  attached here.
- Not exercised for want of a seed, as on 11-04: stop and Retry on an
  interactive turn, the capability-policy toast for dropped default grants, a
  capability that is not ready, cross-team approvals, an inline config-repo
  change card, an attempt's capability audit, a pending-secret box, a
  suspended session and the Internal PR link.
