# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-04

- **Date:** 2026-11-04, 09:05–09:55 UTC
- **Task:** grain/049f1e09/task-2959, base `main`
- **Commit tested:** `cc66a6bc` (`main` at the start of the run, "Merge pull
  request #689"), the build the rail's version link names
  (`v0.0.0-20261104085229-cc66a6bcc706`). The fix below was checked against
  a rebuild of this branch on a second server.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
    unless a step says 390×844
  - Frontend `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`,
    so the embedded `pkg/ui/static` was current.
  - `:8420` (base build): `grain demo -addr 127.0.0.1:8420 -open=false
-github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
-secrets-store -stalled-tasks -sensitive-hold -review-questions
-notifications -fork-pull-request -access-code-file <file>`, with
    `TMPDIR=/var/tmp/walk`, so the store was a throwaway embedded SQLite file.
  - `:8421` (this branch's build): `grain demo -github-sim` over its own
    throwaway store, for the fix only.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. `sqlite3` was installed with apt
    for the stubs.
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
   (setup only).
6. **Config-repo sync failure** (no state repository in `grain demo`):
   `page.route` added `stateRepoSyncFailure` to the real `GET /api/config`.
7. **`settings/new-task-form.yml`** (no config repository): `page.route` added
   `newTaskForm: {default: {fields: {customFields: {visibility: hidden}}}}`
   to `GET /api/config`.
8. **Inbox empty and error states:** `page.route` emptied the arrays in
   `GET /api/tasks`, `/api/notifications` and `/api/local-script-approvals`,
   and in a second pass answered the live `GET /api/tasks` with a 500 on a
   cold load.
9. **A close and reopen with a long reason** (what an agent or the CLI sends;
   the page's Close sends none): `POST /api/tasks/33/close {"reason":…}` and
   `POST …/33/reopen {"reason":…}`.
10. **Closed-task history** (a deployment's past work): 203 tasks on `:8420`
    and 205 on `:8421`, each filed with `POST /api/tasks {"noRepo":true}` and
    closed with `POST …/close`, so there were more closed tasks than one
    closed page (200) holds.
11. **A run in flight on an interactive task** (the dispatcher starting a
    run): #242 filed with `POST /api/tasks {"interactive":true}`, then a
    `task_run` row (`walk-run-242`, started, not finished), which makes it
    `running` and draws the chat pane with Stop.
12. **A goal at its limit** (the goal reconciler): after the page's own Add
    goal, sqlite `task_goal` on #31 got `max_resumes=3,
resumes_since_progress=3, limit_notified_at=…`, first with `on_limit` as
    saved (`notify`) and then `hold`.

## Steps and outcomes

### Inbox

| #   | Step                                                          | Outcome                                                                                                                                                                                                                                                                       | Verdict |
| --- | ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail's Inbox from `/`, then `/inbox` directly                 | Both on `/inbox`. Rail `Inbox 25` with stub 1: Notifications 3 (collapsed), Local scripts 2, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 2 — they sum to 25.  | PASS    |
| 2   | Notifications by keyboard                                     | Starts `aria-expanded=false` with its 3 shown. Enter → true, Space → false, Enter → true.                                                                                                                                                                                     | PASS    |
| 3   | Collapse Approve a proposal by click; reload                  | After the reload Notifications is still expanded and the proposal group still collapsed.                                                                                                                                                                                      | PASS    |
| 4   | Local scripts group (stub 1); Approve walk-1                  | Each row names task 2, `deploy.sh "--env" "staging-1" (and 42 bytes on stdin)`, "On stdin: 42 bytes, sha256 abc123" and the preview. Approve sends `…/lsa-1/answer {"approve":true}`; group 2→1, rail 25→24.                                                                  | PASS    |
| 5   | Stuck group sub-cases                                         | #5 (`syncEntry`, merge queue) offers Open task only; #21 (`reconcileDispatch`) Run now; #22 (`ProcessResult`) Cancel; #23 (`reconcileWake`) Undefer.                                                                                                                          | PASS    |
| 6   | Run now #21; Cancel #22 dismissed, then accepted; Undefer #23 | `…/21/run-now`, group 4→3, #21 `queued`. Cancel confirms "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later."; dismissing sends nothing; accepting sends `…/22/close`, #22 `closed`, group →2. `…/23/undefer`, #23 `queued`, group →1. | PASS    |
| 7   | Retry merge on #25 (Unblock a merge)                          | `POST …/25/retry-merge`; the group is gone.                                                                                                                                                                                                                                   | PASS    |
| 8   | Approve proposal #1                                           | `…/1/approve`; #1 `queued`; Approve a proposal 2→1. #12 shows `imported`, `held for approval by 3` (stub 2) and `unapproved` with Approve / Defer / Decline.                                                                                                                  | PASS    |
| 9   | Parked question #4: Show question, Reply, Send                | The question and its context expand inline; Reply opens an inline box; Send posts `…/4/comments`; #4 `queued`; Asked: reply 2→1.                                                                                                                                              | PASS    |
| 10  | Review decision #28: Ship as is                               | Offers Reply and Ship as is; Ship as is sends `…/28/submit`; the group is gone.                                                                                                                                                                                               | PASS    |
| 11  | CL rows in Asked: submit for merge                            | Submit is inline only on the submittable rows (#15, and PR #6); #14, #16, #17, #18, #19 carry their not-submittable reason and no Submit. Mail, Publish replies and Sync are on the task page (step 47), not inline.                                                          | PASS    |
| 12  | Config-repo sync failure (stub 6)                             | "Settings are not being exported 1" group; the rail's 18 equals the sum of the groups, the failure included.                                                                                                                                                                  | PASS    |
| 13  | Empty and error states (stub 8)                               | Empty: "Nothing is waiting on you. Every task is either running, waiting or done." 500: "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own." (the 11-03 fix holds).                                             | PASS    |
| 14  | Row navigation by keyboard                                    | Focus the first Inbox row's link, Enter → `/tasks/7`; Back → `/inbox`.                                                                                                                                                                                                        | PASS    |

### New task

| #   | Step                                            | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                       | Verdict |
| --- | ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 15  | `+ New task`, every field (filed #31)           | Title, description, `notes.txt` attachment, repo acme/widgets, base `release/1.0`, reviewer `alice`, Task priority 3, Requested by, auto-merge unticked, Network access none, 2 vCPUs. The model picker offers only the deployment's `gemini-2.5-pro` / `gemini-2.5-flash` (the demo's one connection); Specific connection offers `gemini-pro`. #31 is `queued` with every value as entered and `autoMerge:false`.           | PASS    |
| 16  | Task priority `0`, `-2`, `2.5`                  | Each: "a whole number, 1 or more; default 5, higher numbers run first" and Create task disabled.                                                                                                                                                                                                                                                                                                                              | PASS    |
| 17  | Proposed path (#32)                             | Queue immediately unticked → #32 `proposed`.                                                                                                                                                                                                                                                                                                                                                                                  | PASS    |
| 18  | Fork head with Create a new branch unticked     | `alice:feature-x` → "GitHub fork head: branch feature-x on alice's fork. grain files this task against the open pull request on acme/widgets it heads." `alice:`, `:x` → "needs both an owner and a branch"; `a:b:c` → "has more than one ':'". Submitting `a:b:c`, `bad owner:x` or `alice:feature-x` (no such PR) is refused by the server in the same words, nothing filed. `contrib:fix/readme-typo` files #34 on `main`. | PASS    |
| 19  | No repo (#33)                                   | Repos "none" files `noRepo:true`, `queued`.                                                                                                                                                                                                                                                                                                                                                                                   | PASS    |
| 20  | Cancel                                          | Escape closes the form and keeps the draft; reopening shows it; Discard draft empties it; nothing filed.                                                                                                                                                                                                                                                                                                                      | PASS    |
| 21  | Rail's create menu                              | "Create options" lists New task, New session, New board; New task opens the form on `/new`; Escape returns to `/inbox`. Focus lands on Title and Tab walks the fields in order.                                                                                                                                                                                                                                               | PASS    |
| 22  | Template priority (stub 5), from its page       | Picker offers No template, security-audit, release-qualification (not the field-carrying gke template). security-audit → 8, release-qualification → 5, back → 8; edited to 4, switching keeps 4. Helper: "always files against acme/widgets on main, whatever Repos and Base branch say". #35 filed at priority 4.                                                                                                            | PASS    |
| 23  | Template with input fields                      | Its Use… opens "File a task from a template", not the New task form.                                                                                                                                                                                                                                                                                                                                                          | PASS    |
| 24  | `/new?field.priority=high&field.area=backend&…` | Notice: "This link asked for values this form has left out: “nosuch” is not a field on this deployment; Estimate (days): “x” is not one of its values." #36 filed with `{area:backend, priority:high, requested-by:Linkco}`.                                                                                                                                                                                                  | PASS    |
| 25  | Same link under a hiding form config (stub 7)   | "Priority: “high” is in a field this form hides; Area: “backend” is in a field this form hides."                                                                                                                                                                                                                                                                                                                              | PASS    |
| 26  | Grants (#37)                                    | Add grant… lists Grain API presets, scopes by area and Capability server methods with their verdicts ("needs arguments", "CI only: not a grant for agent tasks"). `api:ci:read` and `api:external-dev` ticked stay ticked and are filed beside the repo's defaults.                                                                                                                                                           | PASS    |

### Tasks list

| #   | Step                                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Verdict    |
| --- | ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| 27  | Every rail view                          | Tasks, Proposed, Waiting, Running, Asked, Merging, Deferred, Closed, Blocked each open their `?state=` view with as many rows as their count — except Closed and Tasks once closed tasks pass one page (bug 1).                                                                                                                                                                                                                                                | FAIL→fixed |
| 28  | Row chips                                | Blocked #11 "blocked on task", hover "queued/blocked_on_task · Driven by reconcileDispatch · Waiting on the task it depends on closing". #5 on the merge queue reads "Merging". #31 carries `P9` ("Task priority 9 (default 5; higher numbers run first)"). Live #3 "live run", hover "running/live_run · Driven by ProcessResult…". #12 "imported" titled "imported from GitHub issue acme/widgets#42" and "held for approval by 3" until approved (step 44). | PASS       |
| 29  | Search                                   | "retry" → 7 rows, `?q=retry`; "12" finds #12 by id. Search is over title and id by design (`taskFilters.js`), so a phrase only in a description finds nothing.                                                                                                                                                                                                                                                                                                 | PASS       |
| 30  | Origin filter, compound narrowing, Clear | Origin values: Filed by hand, Proposed by an agent, Imported issue. Imported issue → `f=origin:any:imported`, only #12; survives a reload. Adding Repo acme/widgets → `&f=repo:any:acme/widgets`. Clear → the default narrowing, 34 rows.                                                                                                                                                                                                                      | PASS       |
| 31  | Back / Forward                           | Narrowing changes replace the history entry rather than push one (deliberate, App.jsx's samePage); Forward restores the compound URL. The rail's view picks do push.                                                                                                                                                                                                                                                                                           | PASS       |

### Task detail

| #   | Step                                            | Outcome                                                                                                                                                                                                                                                                                                                   | Verdict |
| --- | ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 32  | Open from a row, from Inbox, by deep link       | #2's description shows from all three (the live list no longer carries descriptions; the detail fetch does).                                                                                                                                                                                                              | PASS    |
| 33  | Edit #31                                        | Priority `0` refused in the shared wording, Save disabled. Title, description, reviewers `alice, bob`, priority 7 → one `PATCH`; base `main` from Target; Requested by from Edit fields. All persisted; `autoMerge` stayed `false`.                                                                                       | PASS    |
| 34  | Defer, Undefer, dated Defer                     | Defer (no date) `{"deferredUntil":""}` → `deferred`; Undefer → `queued`. Until `2030-12-31` → "Picks itself back up on 12/31/2030, 12:00:00 AM, onto the queue.", kept after a reload; Undefer.                                                                                                                           | PASS    |
| 35  | Close, Reopen #31                               | `…/close {"close_pull_request":false}` → `closed` (Reopen offered); `…/reopen` → `queued`; the timeline records Closed and Reopened. `autoMerge` still `false` after defer, edits, close and reopen.                                                                                                                      | PASS    |
| 36  | Long close/reopen reason at 390 px (stub 9)     | "Reopened by operator (access code) · Reopened: Closing because…" wraps inside the 357 px pane; the document is 390 px wide (no sideways scroll).                                                                                                                                                                         | PASS    |
| 37  | Chat pane on running interactive #242 (stub 11) | Compose row: Stop, Pause, Send. Pause → `…/pause`, Resume shown at once, state `running`; Resume → `…/resume`. Stop → `…/stop`: `awaiting_reply` with `turnStopped`, and the page offers Retry; Retry → `…/retry`, `queued`.                                                                                              | PASS    |
| 38  | Pause/Resume on the page: queued #2, running #8 | #2 Pause, #8 Pause now (`{"immediate":true}`); Resume appears without a reload; both keep their state and their place in the list (3 → 3, 9 → 9).                                                                                                                                                                         | PASS    |
| 39  | Force merge #5                                  | Prompt "Force this merge? … Say why. It is required…". Dismissed: nothing sent. Empty answer: the server refuses "forcing a merge needs a reason…". With a reason: `…/force-merge`, and the page shows "Asked grain to force the merge of acme/widgets#101…" and the reason without a reload.                             | PASS    |
| 40  | Re-run CI / Retry merge on the page             | Not offered on any seeded task looked at (#5, #6, #20, #24, #26): no run here is failed. Retry merge was covered from the Inbox (step 7).                                                                                                                                                                                 | N/A     |
| 41  | Dependency on #31 → #33, then removed           | Chip "33 Walk D: no repo (open)", hover "33 Walk D: no repo — Waiting (blocking this task)". #31 `blocked`, joins the Blocked view; rail Blocked 1 → 2. Removing it: unblocked, rail back to 1.                                                                                                                           | PASS    |
| 42  | Goal on #31 (stub 12)                           | Add goal → Pull request merged, task 31, Notify and back off → `PUT …/goal`; "Not met yet … (limit 6 resumes or 24h, then notify)". At its limit: "Limit reached: operator notified, backing off."; with `hold`: "Held at its limit: a person has to act." Remove goal → `DELETE`; no card after a reload, Add goal back. | PASS    |
| 43  | Work tree on #3                                 | "Provenance · Work from task 13: 0 of 4 done", nested 13 ▸ 1, 3 (running), 8 (PR open, acme/widgets#104) ▸ 11, each linking to its task.                                                                                                                                                                                  | PASS    |
| 44  | Approve #12 from its page                       | "Imported from GitHub issue acme/widgets#42" and Approve; `…/12/approve` → `queued`; its row drops "held for approval by 3" and keeps "imported".                                                                                                                                                                         | PASS    |
| 45  | Merge policy on #26                             | "Held by this repository's gating triggers (human: required).", `.grain/triggers/payments.yml: review task 27`, and the trigger's waiting line.                                                                                                                                                                           | PASS    |
| 46  | Full prompt and live transcript on #3           | "prompt for 3 · attempt #1 · 10 kb · ~2,692 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`. "live attempt #1 transcript" is drawn.                                                                                                                                                                    | PASS    |
| 47  | CL actions on #14, #17, #19                     | #14: Mail disabled, Publish replies (2) → `…/change-publish`, Submit disabled. #17: Mail → `…/change-mail`. #19: Sync → `…/change-sync`; Submit disabled behind head.                                                                                                                                                     | PASS    |
| 48  | Pending secret on #8 (stub 3)                   | The box names `stripe-api-key/value`, write-only. Set secret → `PUT …/8/secret`; after a reload the value appears nowhere in the page.                                                                                                                                                                                    | PASS    |
| 49  | Suspended session on #2 (stub 4)                | "Left by demo-run-2-1 on 11/4/2026, 8:00:00 AM (Antigravity). Any further run of this task resumes this conversation…".                                                                                                                                                                                                   | PASS    |
| 50  | Host-script approvals on #2 (stub 1)            | Deny on walk-2 (`{"approve":false}`); after a reload the timeline has "Denied local script call walk-2 (deploy-tools)" and "Approved local script call walk-1 (deploy-tools)".                                                                                                                                            | PASS    |
| 51  | External PR chip on #34                         | The page carries the outlined `acme/widgets#41` chip linking to the pull request. List rows carry none (see O5).                                                                                                                                                                                                          | PASS    |

### Selection, bulk edit, manual mode

| #   | Step                                                   | Outcome                                                                                                                                                                                                         | Verdict |
| --- | ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 52  | Waiting view, tick #31, #33, #35                       | Bar: Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, Attach, Detach, Clear selection. Close confirms "Close 3 task(s)?"; dismissing sends nothing, selection kept.   | PASS    |
| 53  | Bulk edit… → Cancel; then Task priority `-1`, then `9` | Mixed fields say "(mixed across the selection)". Cancel sends nothing, selection kept. `-1` refused, Apply disabled. `9` → three `PATCH {"priority":9}` only; base and auto-merge unchanged; selection cleared. | PASS    |
| 54  | Defer, Undefer, Attach/Detach Durable, Close, Reopen   | Each sends one call per ticked task and lands (`deferred`/`queued`/`closed`/`queued`; `durable` attached then detached). Reopen worked from the Closed view.                                                    | PASS    |
| 55  | Approve #32, Submit #6 and #15 from the bar            | #32 `queued`; #6 `completed` (merge queue), #15 (CL) submitted and closed.                                                                                                                                      | PASS    |
| 56  | System → Pipeline → Pause pipeline                     | Confirm "Pause the pipeline? …", `POST /api/pipeline-pause`. Every queued row says "paused — waits for Run now" except #11 (blocked) and #21 (already started in step 6).                                       | PASS    |
| 57  | Select #33, #35 → Run selected now; reload; Resume     | `…/run-now` each; their markers are gone and stay gone after a reload. Resume confirms and sends `DELETE /api/pipeline-pause`.                                                                                  | PASS    |

### Console and network

No uncaught page errors. The only console errors and 4xx/5xx responses were
the deliberate ones: the stubbed 500s (step 13, which also log "grain: poll
failed"), the server's refusals in steps 18, 39 and 42's first try (a goal
saved with no task, see O2). The only failed requests were polls aborted by
navigation.

## Bugs

### 1 — The rail stopped counting closed work at the pages it had read (fixed)

**Repro (base `cc66a6bc`):** give the deployment more than 200 closed tasks
(stub 10: 205), open `/`. The rail reads Closed 200 and a Tasks total five
short; open Closed and press Load older closed tasks and they become 205 and
the full total. On `:8420`, with 207 closed tasks, the rail read Closed 199 and
Merged 1. Since #681 the polled list carries no closed tasks and the UI pages
them in 200 at a time (`closedTasks.js`), but `Sidebar.jsx` still counted
the tasks it held, so on any deployment with more than 200 closed tasks the
Closed, Merged and Tasks counts were the size of the pages read, not totals.

**Fix:** a closed page (`GET /api/tasks?state=closed&cursor=`) now also says
how many tasks match across every page: `total`, plus `merged` and `sessions`,
the two the rail counts under other entries (`pkg/ui/task_list_filter.go`).
`useClosedTasks` keeps them, works out what no page read so far holds
(`unseenClosed`), and the rail adds that to Closed, Merged and Tasks. A server
that sends no totals leaves the counts as they were. On `:8421` with 206
closed tasks the rail reads Closed 206 and Tasks 223 (224 tasks, one of them
the Director session's), the same before and after Load older closed tasks.
Covered by `TestTaskListPagesCountEveryClosedTask`,
`closedTasks.test.js` ("counts the closed work no page read so far holds")
and `Sidebar.test.jsx` ("counts closed and merged work not yet paged in").
Screenshots of `/` and `/?state=closed&f=none` at 1440 and 390, light and
dark, were taken before (`:8420`) and after (`:8421`) with
`ui/e2e/screenshot.mjs --url`. They could not be described without a Gemini
key, so the rail entries' geometry was measured in both themes on both
servers: each count sits inside its entry, vertically centred (dy 0) and
clear of its label.

## Observations

- **O1:** the chat pane of a running interactive task offers Pause (with a
  handover) and Stop, but not Pause now. The page offers both pauses; in the
  pane, Stop is the immediate way out.
- **O2:** a "Pull request merged" goal starts with an empty Task field (repo
  and branch default to the task's own), and saving it as it is is refused:
  "goal pr-merged needs a task, or a repo and a number". Defaulting Task to
  the task itself would save a step.
- **O3 (from 11-03, still true):** a fork-head task filed with an empty
  description gets one that starts with a blank line and `---`, rendered as a
  leading rule (#34). The note's lead is also what `forkhead.StripNote`
  matches on when a task leaves the repository, so the fix touches the
  orchestrator as well. Proposed as a follow-up.
- **O4:** malformed fork labels are explained under Base branch while you
  type, but Create task stays enabled; the server then refuses them in the
  same words.
- **O5:** the outlined external-PR chip is drawn on the task page
  (`DetailOverlay.jsx`, `ui/e2e/external-pr-chip.spec.js`), not on list
  rows. List rows show no pull-request chip of any kind, so this walkthrough
  checked the page.
- **O6:** the form remembers the last base branch filed on a repo
  (`release/1.0` came back for #32 after #31), as it did on 11-03.
- **O7:** a first script pressed Enter in Reviewers (a comma-separated text
  box) and filed #30 half-filled. That is the browser's ordinary submit on
  Enter, not a bug.
- **O8:** the Force merge refusal toast from the empty-reason attempt was
  still on screen right after the successful one.
- Not reachable in the demo: the capability-policy toast for dropped default
  grants, a capability that is not ready, cross-team approvals and their
  withdraw control, an inline config-repo change card, an attempt's
  capability audit and the Internal PR link. No seed carries them, and the
  GitHub simulator lists branches from its own memory, so a branch pushed to
  its bare repository (tried for #31) does not appear and the link stays
  hidden.
