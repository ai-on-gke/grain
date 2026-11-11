# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-08

- **Date:** 2026-11-08, 04:17–05:00 UTC
- **Task:** task 3354, branch `grain/098073e4/task-3354-walk-through-task-intake-inbox`, base `main`
- **Commit tested:** `d7dde0c2` (`main` at the start of the run, "Merge pull
  request #877"). The binary reports `v0.0.0-20261108030308-d7dde0c23839`.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    1400×900
  - Frontend built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`, so the embedded
    `pkg/ui/static` was current
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -fork-pull-request -human-review -base-branch-ci
-access-code-file <file>` with `TMPDIR=/var/tmp/walk`, so the store
    was a throwaway embedded SQLite file
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
    `sqlite3` was installed with apt for stub 1.
- **How it was driven:** throwaway Playwright scripts (not committed). Each
  one signs in through the "Access code" page and then acts only through
  visible controls. Every page logged `pageerror`, console errors and
  warnings, `/api/` responses of 400 or more (with their bodies), failed
  requests, every dialog (its text and whether it was accepted), and every
  non-GET `/api/` request with its body. The REST API was used only to read
  back what a gesture had persisted, and for the setup stubs listed below.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Each stub stands in for something that only an agent run, a
reconciler, a config repository or setup would write:

1. **Host-script approvals** (normally posted by a run's runhost): sqlite
   `task_grant ('2','local-scripts:deploy-tools','operator')`, then two
   `POST /api/tasks/2/local-script-approvals` (`walk-1`, `walk-2`) with
   the access code as a bearer token. Each carried a 42-byte stdin preview
   and sha256 `abc123`. The endpoint refuses a signed-in person, so a
   browser cannot make the request itself.
2. **Template priority** (setup): `PATCH /api/templates/template-2
{"priority":8}`.
3. **Config-repo sync failure** (`grain demo` has no state repository):
   `page.route` added `stateRepoSyncFailure {since, error:"push rejected:
non-fast-forward", repo:"acme/grain-config"}` to the real `GET
/api/config`.
4. **`settings/new-task-form.yml`** (no config repository):
   `page.route` added `newTaskForm: {default: {fields: {customFields:
{visibility: hidden}}}}` to `GET /api/config`.
5. **Inbox empty and error states:** `page.route` emptied the arrays in
   `GET /api/tasks`, `/api/notifications` and
   `/api/local-script-approvals`. A second pass answered `GET /api/tasks`
   with a 500.

## Steps and outcomes

### Inbox

| #   | Step                                                          | Outcome                                                                                                                                                                                                                                                                                                                                                                                       | Verdict |
| --- | ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail's Inbox from `/`, then `/inbox` directly                 | Both land on `/inbox`. The rail read `Inbox 25` ("21 tasks waiting on you, 4 notifications"), and `27` once stub 1's two waiting host-script calls were added. Groups: Notifications 4, Local scripts waiting for approval 2, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 10, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 2. | PASS    |
| 2   | Notifications by keyboard                                     | Starts `aria-expanded=false` with its count 4 still shown. Enter → true, Space → false, Enter → true.                                                                                                                                                                                                                                                                                         | PASS    |
| 3   | Collapse Approve a proposal by click, then reload             | After the reload Notifications stays expanded and Approve a proposal stays collapsed. Enter on its header opens it again.                                                                                                                                                                                                                                                                     | PASS    |
| 4   | Local scripts: Approve walk-1, Deny walk-2                    | Each row names task 2 and shows `deploy.sh "--env" "staging-N" (and 42 bytes on stdin)`, "On stdin: 42 bytes, sha256 abc123" and the preview. Approve sends `…/lsa-1/answer {"approve":true}` and the group goes 2 → 1. Deny sends `{"approve":false}` and the group disappears. The rail goes 27 → 26 → 25 (the second change shows on the next poll).                                       | PASS    |
| 5   | Stuck group sub-cases                                         | #5 (`syncEntry`, on the merge queue) offers only Open task. #21 (`reconcileDispatch`) offers Run now, #22 (`ProcessResult`, live run) offers Cancel, and #23 (`reconcileWake`) offers Undefer. Each row explains why it counts as stuck.                                                                                                                                                      | PASS    |
| 6   | Run now #21; Cancel #22 dismissed, then accepted; Undefer #23 | Run now: `…/21/run-now`, the group goes 4 → 3, #21 is `queued`. Cancel asks "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." Dismissing sends nothing (#22 stays `running`). Accepting sends `…/22/close`, #22 is `closed`, the group goes → 2. Undefer: `…/23/undefer`, #23 is `queued`, the group goes → 1.                                      | PASS    |
| 7   | Retry merge on #25 (Unblock a merge)                          | `POST …/25/retry-merge`; the group disappears.                                                                                                                                                                                                                                                                                                                                                | PASS    |
| 8   | Approve proposal #1                                           | `…/1/approve`; #1 is `queued`; Approve a proposal goes 2 → 1. #12 is still listed with its `imported` chip and Approve / Defer / Decline.                                                                                                                                                                                                                                                     | PASS    |
| 9   | Parked question #4: Show question, Reply                      | Show question expands the full question and its context. Reply → Send posts `…/4/comments {"body":…}`; #4 goes `awaiting_reply` → `queued` and Asked: reply goes 2 → 1.                                                                                                                                                                                                                       | PASS    |
| 10  | Review decision #28: Ship as is                               | `POST …/28/submit`; #28 is `completed` (merge queue); the Review needs your decision group disappears.                                                                                                                                                                                                                                                                                        | PASS    |
| 11  | Submit offered only when submittable                          | In Asked: submit for merge, Submit appears on #6, #15, #24, #26 and #30. CL tasks #14, #16, #17, #18 and #19, which are not submittable, have no Submit; each row instead says why it cannot be submitted yet (for example "review is not approved (changes requested); required presubmit has not passed on snapshot 3 (failed)").                                                           | PASS    |
| 12  | CL actions (Mail, Publish replies, Sync)                      | These are on the task page, not the Inbox row (see O1). #14: Mail disabled, Publish replies (2), Submit disabled, Sync. #17: Mail enabled. Pressing it sends `…/17/change-mail {}` and the review becomes `in_review`, "already mailed". #19: Sync sends `…/19/change-sync`; Submit stays disabled for "required presubmit has not passed on snapshot 3 (not run)".                           | PASS    |
| 13  | Config-repo sync failure (stub 3)                             | Rail 18 → 19. A "Settings are not being exported 1" group appears with "Settings have not been exported to acme/grain-config since 11/8/2026, 3:00:00 AM: push rejected: non-fast-forward".                                                                                                                                                                                                   | PASS    |
| 14  | Empty state, then load error (stub 5)                         | Empty: "Nothing is waiting on you. Every task is either running, waiting or done." and the rail shows no Inbox count. Error: "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own." Notifications stay listed.                                                                                                                    | PASS    |

### Intake

| #   | Step                                                               | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 15  | Rail's create menu                                                 | "Create options" lists New task, New session and New board. New task opens the form on `/new`, with focus in Title.                                                                                                                                                                                                                                                                                                                                                          | PASS    |
| 16  | Queued path, every field (#32)                                     | Filled title, two-paragraph description, an attachment, repo acme/widgets (options: —, acme/widgets, No repo, Other…), base `release/1.0`, reviewers `alice, bob`, Area backend, auto-merge unticked, priority 7. Read back as `queued` with `base release/1.0`, `priority 7`, `reviewers [alice,bob]`, `autoMerge false`, `customFields {area:backend, priority:medium}`, `attachments [attach.txt]` and the repo's default grants.                                         | PASS    |
| 17  | Task priority validation                                           | `0`, `-2` and `1.5` are each refused with "a whole number, 1 or more; default 5, higher numbers run first", and Create task is disabled. `7` is accepted.                                                                                                                                                                                                                                                                                                                    | PASS    |
| 18  | Model picker                                                       | Offers "Deployment default (gemini-2.5-pro)", gemini-2.5-pro and gemini-2.5-flash, which are the models the demo's enabled connections advertise. Escape closes the list and leaves the form open.                                                                                                                                                                                                                                                                           | PASS    |
| 19  | Refused field inside Advanced options; proposed no-repo path (#33) | With vCPUs `-1` set and Advanced options collapsed again, Create task opens Advanced (`aria-expanded` false → true) and focuses `sandboxCpus`; nothing is filed. With vCPUs 2, Repos "No repo" and Queue immediately unticked, the task is filed as `noRepo:true, sandboxCpus:2` and `proposed`.                                                                                                                                                                             | PASS    |
| 20  | Template's Use…                                                    | On Processes, Use… opens "File a task from a template" both for the field-carrying GKE template and for security-audit. The latter shows "Files against acme/widgets @ main -- this template is bound to it."                                                                                                                                                                                                                                                                | PASS    |
| 21  | Template picker priority (stub 2, #34)                             | From security-audit's page, "New task from this template" opens the form at priority 8. The picker lists No template, security-audit and release-qualification. Switching to release-qualification gives 5, and back gives 8. After editing it to 4, switching templates keeps 4. Helper text: "always files against acme/widgets on main, whatever Repos and Base branch say". #34 is filed at priority 4 on `main`, even though the box held the remembered `release/1.0`. | PASS    |
| 22  | `/new?field.…` links                                               | Notice: "This link asked for values this form has left out: “nosuch” is not a field on this deployment; Estimate (days): “x” is not one of its values." With stub 4: "Priority: “high” is in a field this form hides; Area: “backend” is in a field this form hides."                                                                                                                                                                                                        | PASS    |
| 23  | Fork head, Create a new branch unticked (#35)                      | `alice:feature-x` → "GitHub fork head: branch feature-x on alice's fork. grain files this task against the open pull request on acme/widgets it heads." `alice:` and `:x` → "needs both an owner and a branch". `a:b:c` → "has more than one ':'". Submitting `a:b:c` is refused with a 400 in the same words, and the form stays open with the message. `contrib:fix/readme-typo` files #35.                                                                                | PASS    |
| 24  | Grants (#36)                                                       | Add grant… opens the "Grants to add" menu: Grain API presets, scopes grouped by area, then Capability server methods with their verdicts ("needs arguments", "ci only: not a grant for agent tasks…"). Ticking `ci-read` sends a grant check, Escape closes only the menu, and #36 is filed with `api:ci-read` beside the repo's defaults.                                                                                                                                   | PASS    |

### Tasks list

| #   | Step                            | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| 25  | Every rail view                 | Proposed 2, Waiting 13, Running 2, Asked 11, Merging 4, Deferred 1, Closed 2 and Blocked 1 each open their `?state=` view with as many rows as their count. Tasks 35 opens the default narrowing (`f=state:none:deferred\|closed`) with 32 rows; the count includes the 3 deferred and closed tasks, as it is meant to.                                                                                                                                      | PASS    |
| 26  | Row chips                       | #11 shows the state oval "Blocked on: #8 (running)" and a `blocked on task` chip. #5, #20, #25 and #28 read "Merging". #32 has `P7` and #34 `P4` ("Task priority N (default 5; higher numbers run first)"). Live #3 and #8 carry `live run`, whose hover reads "running/live_run · Driven by ProcessResult · Waiting on the run in flight". #12 carries `imported` ("imported from GitHub issue acme/widgets#42") with the "Imported" oval and `unapproved`. | PASS    |
| 27  | Search                          | "retry" gives 7 rows and `?q=retry`; "12" finds #12 by id.                                                                                                                                                                                                                                                                                                                                                                                                   | PASS    |
| 28  | Origin filter, reload           | Origin values: Filed by hand, Proposed by an agent, Imported issue, Review. Imported issue → `&f=origin:any:imported`, showing only #12, and the filter survives a reload.                                                                                                                                                                                                                                                                                   | PASS    |
| 29  | Sort, compound narrowing, Clear | Sort by… → created gives `?state=queued&sort=created:desc` (36, 35, 34, 32, …), and the order survives a reload. `?state=asked&q=retry` gives 6 rows; Clear returns `?state=asked&f=none` with 11.                                                                                                                                                                                                                                                           | PASS    |
| 30  | Back / Forward                  | `/` → `?state=queued&f=none`. Back returns to `/` (32 rows) and Forward to the Waiting view (13 rows).                                                                                                                                                                                                                                                                                                                                                       | PASS    |

### Task detail

| #   | Step                                  | Outcome                                                                                                                                                                                                                                                                                                                                                                                                          | Verdict |
| --- | ------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 31  | Edit #32                              | Priority `0` disables Save with the shared wording. Title, reviewers `carol`, priority 9 and base `main` are saved with two `PATCH` calls, and all of them persist. `autoMerge` stays `false`.                                                                                                                                                                                                                   | PASS    |
| 32  | Defer, Undefer, dated Defer           | Defer `{"deferredUntil":""}` → `deferred`; Undefer → `queued`. Until `2030-12-31` → "Picks itself back up on 12/31/2030, 12:00:00 AM, onto the queue.", and it is still there after a reload; Undefer → `queued`.                                                                                                                                                                                                | PASS    |
| 33  | Close, Reopen #32                     | `…/close {"close_pull_request":false}` → `closed`, and the timeline shows "Closed · by operator (access code)". `…/reopen` → `queued`. After the defers, edits, close and reopen, `autoMerge` is still `false` and priority 9.                                                                                                                                                                                   | PASS    |
| 34  | Dependency #34 → #33, then removed    | The picker ("Add a dependency…") sends `depends-on {"id":"33","attach":true}`. The chip reads "33 walk b: proposed no repo (open)", and its hover "33 Walk B: proposed no repo — Proposed (blocking this task)". #34 becomes `queued/blocked_on_task`, joins the Blocked view (11, 34), and the rail's Blocked goes 1 → 2. Removing the chip sends `attach:false`; #34 is unblocked and the rail goes back to 1. | PASS    |
| 35  | Pause / Resume: queued #2, running #8 | #2 Pause → `…/pause`; #8 Pause now → `{"immediate":true}`. Resume appears without a reload, and each task keeps its state (`queued`, `running`) and its list position (2 → 2, 8 → 8). Resume → `…/resume`.                                                                                                                                                                                                       | PASS    |
| 36  | Force merge #5 (stuck waiting on CI)  | It prompts "Force this merge? … Say why. It is required…". Dismissing sends nothing. With a reason it sends `…/5/force-merge {"reason":…}`, and the page shows "Asked grain to force the merge of acme/widgets#101 at fedcba987654, overriding the merge queue's own decision." without a reload. No seeded task offers Re-run CI or Retry merge on its page (Retry merge was covered from the Inbox, step 7).   | PASS    |

### Selection, bulk edit, manual mode

| #   | Step                                               | Outcome                                                                                                                                                                                                                                                                                                    | Verdict |
| --- | -------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 37  | Waiting view, tick #34, #35, #36                   | The bar shows "3 selected" with Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, the capability picker, Attach, Detach and Clear selection. Close asks "Close 3 task(s)?"; dismissing sends nothing and keeps the selection.                                     | PASS    |
| 38  | Bulk edit… → Cancel; then Task priority -1, 6      | The dialog lists the fields with "task priority (mixed across the selection)". Cancel sends nothing and keeps the selection. Once Task priority is ticked, `-1` disables Apply. `6` sends three `PATCH {"priority":6}` calls and nothing else: base and auto-merge are unchanged and the selection clears. | PASS    |
| 39  | Defer and Undefer from the bar                     | One call per ticked task each time; the tasks go `deferred` and then, from the Deferred view, back to `queued`.                                                                                                                                                                                            | PASS    |
| 40  | System → Pipeline → Pause pipeline                 | Confirm "Pause the pipeline? Grain will stop starting new agent and CI runs…" → `POST /api/pipeline-pause`. Every queued row carries "waits for Run now" except #11 (blocked) and #21 (already started in step 6).                                                                                         | PASS    |
| 41  | Select #34, #35 → Run selected now; reload; Resume | `…/run-now` for each. After a reload their markers are gone and the rest remain. Resume pipeline asks first and sends `DELETE /api/pipeline-pause`.                                                                                                                                                        | PASS    |

### Console and network

There were no uncaught page errors. The only console errors and 4xx/5xx
responses were expected ones:

- the 401 on the sign-in page, before the access code is entered;
- the stubbed 500 in step 14;
- the server refusing the malformed fork head in step 23.

No requests failed apart from polls cut off by navigation. The demo
server's log had no panics or errors.

## Bugs

None found in this run.

## Observations

- **O1:** Mail, Publish replies and Sync are drawn on the task page
  (`detail/Actions.jsx`: `ReviewActions`, `ClSubmitActions`), not on Inbox
  rows. Inbox rows offer only Submit, and only when the change is
  submittable. Otherwise the row says why it is not. They were exercised
  on the task page instead (step 12).
- **O2:** Use… on a field-less template (security-audit) also opens "File
  a task from a template", not the New task form. The template picker with
  its priority is reached through the template page's "New task from this
  template" (step 21).
- **O3:** On a queued or proposed task with no pull request, Close on the
  task page closes at once with no confirmation (step 33). The batch bar's
  Close and the Inbox's Cancel of a running task do ask first. A closed
  task can be reopened, so this was not treated as a defect.
- **O4:** `bad owner:x` has no `<owner>:<branch>` label shape, so the form
  shows no fork-head explanation and treats it as a plain branch. The client
  (`forkHead.js`) and server (`model.ParseForkHead`) agree on this.
- **Not exercised this run:** the goal card and `on_limit`, the work tree,
  the merge policy lines, a pending-secret box, a suspended session, the
  host-script approval card on the task page, the chat pane's Pause/Stop/Retry,
  long close reasons at 390 px, and the held-for-approval chip. The time
  went to the intake, Inbox and batch paths. The 2026-11-04 walkthrough
  covered all of these over the same seeds and stubs; this run did not
  re-check them.
- **Not reachable in the demo:** the capability-policy toast for dropped
  default grants, a ticked capability that is not ready, cross-team
  approvals and their withdraw control, an inline config-repo change card,
  an attempt's capability audit, and the Internal PR link. No seed carries
  any of these.
