# Task intake, Inbox and lifecycle walkthrough — 2026-11-09 (task 3714)

- **Date:** 2026-11-09, 03:28–04:00 UTC, one run.
- **Task:** task 3714, branch
  `grain/098073e4/task-3714-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `e764c09d` ("Merge pull request #997"), binary
  `v0.0.0-20261109032659-e764c09d193e`. The fix below was checked on a
  second build of the same commit with the change applied.
- **Focus:** the earlier reports in this series (most recently
  `2026-11-08-task-intake-inbox-lifecycle-task-3611.md`) cover this area in
  depth. This run re-walked the whole operator path on the current `main`
  and concentrated on what changed since: the new `grain demo -config-change`
  seed (a real throwaway grain/config behind the config-change card, which
  answered 501 on 2026-11-08), and the rail/Inbox counts after the
  Legacy-capabilities removal.
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
-access-code-file <file>`, with `TMPDIR` set to a fresh scratch directory
    on every start, so the store was a throwaway embedded SQLite file.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    screenshots could not be described with `LOOK=1`. No screenshots were
    committed.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through the Access code page and then acts only
  through visible controls. Each page recorded uncaught page errors, console
  errors and warnings, failed requests, `/api/` responses of 400 or more
  (with bodies), every native dialog (its text, and whether it was accepted),
  and every non-GET `/api/` request with its body. The REST API (bearer access
  code) was used to read back what a gesture had persisted, and for the setup
  listed under Stubs.
- **Console baseline:** each script's sign-in logs one `401` for the
  unauthenticated first load. That is expected and is left out below.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **Two templates with a priority** (`urgent-fix` at 8, `sweep` at 9):
   `POST /api/templates`. The demo seeds no template with a priority.
2. **`settings/new-task-form.yml` layer:** a Playwright route on
   `GET /api/config` added
   `newTaskForm: {default: {fields: {customFields: {visibility: hidden}}}}`.
   The rest of the response passed through. The demo has no config file for
   this.
3. **A long close/reopen reason** (what an agent or the CLI sends; the task
   page's Close takes no reason): `POST /api/tasks/21/close` and `…/reopen`,
   each with a ~300-character reason. The timeline was then read in the
   browser at 390 px.
4. **An interactive task for the chat pane:**
   `POST /api/tasks {"interactive": true, "approved": true}` (#42). The New
   task form does not offer interactive filing.

Nothing else was stubbed. Host-script approvals, pending secrets, suspended
sessions, cross-team approvals and a grain CI verdict with Re-run CI need
rows that only a run or a reconciler writes. The 2026-11-08 reports covered
them with sqlite stubs, and this run did not repeat them (see Not covered).

## Steps and outcomes

### 1. Inbox

| #   | Step                            | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Verdict |
| --- | ------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Open from the rail and directly | The rail's Inbox button (count chip 28, tooltip "24 tasks waiting on you, 4 notifications") opens `/inbox`. The deep link loads the same page. The groups are notifications 4, asked: reply 4 (#4, #7, #35, #36), review needs your decision 1, asked: submit for merge 10, unblock a merge 1, approve on github 1, look at a stuck task 4, and approve a proposal 3. 4+1+10+1+1+4+3 = 24, plus 4 notifications, is 28.                                                                                                                                              | PASS    |
| 2   | Collapse/expand, persistence    | Notifications starts `aria-expanded="false"` with "notifications 4" still shown. A click collapses Approve a proposal, `Enter` on focused Unblock a merge collapses it, and `Space` collapses Approve on GitHub. After a reload all three stay collapsed, and Look at a stuck task stays expanded. Expanding Notifications shows its four rows (human-review hold, flaky e2e, v2.4.0 promotion…).                                                                                                                                                                    | PASS    |
| 3   | Row navigation                  | Clicking the "Add a dark mode toggle" row opens `/tasks/1`. Back returns to the Inbox.                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | PASS    |
| 4   | Proposal, stalled sub-cases     | #1 Approve → `queued`. Stalled rows show their sub-case chip and the matching control: #23 "wake time" Undefer → `queued`; #21 "ready" Run now → `POST …/run-now`; #22 "live run" Cancel asks "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." → `closed`. #5 ("on merge queue") offers only Open task, and its note explains why.                                                                                                                                                                                        | PASS    |
| 5   | Unblock a merge, submit, review | #25 Retry merge → `POST …/retry-merge`. #15 (submittable CL) inline Submit → `closed`. #28 Ship as is → `completed`, auto-merge on. Inline Submit is drawn only on #15 among CL rows #14–#19, and each of the others shows its "Not submittable yet: …" reason.                                                                                                                                                                                                                                                                                                      | PASS    |
| 6   | Groups and counts follow        | After steps 4–5 none of the seven acted-on rows remain, and the rail went 28 → 21, both before and after a reload. Unblock a merge disappeared once empty, and Look at a stuck task fell to 1.                                                                                                                                                                                                                                                                                                                                                                       | PASS    |
| 7   | Parked question reply           | #4's inline Reply → Send posts `…/4/comments {"body":"Only unblock the merge queue for now."}` → `queued`.                                                                                                                                                                                                                                                                                                                                                                                                                                                           | PASS    |
| 8   | Config-repo change card (real)  | #35's card: "Raise the default poll interval to 30s", branch `grain/config/35/poll-interval` at a real SHA, `tables/grain_config.json`, "Schema valid · Capability catalogue consistent · Dispatch liveness OK · Dry-run apply succeeded", and a side-by-side/inline/raw diff (`poll_interval_ms: 10000` → `30000`). **Approve & Apply** posts `{"headSha":…}`. The timeline then reads "Approved and applied config change (d668d1b52155)…", the daemon log records the import (`poll_interval_ms was 10000 and is now 30000`), and the task goes back to `queued`. | PASS    |
| 9   | Config-change Reject (real)     | On #36, Reject opens "Why are you rejecting it?". Reject change posts the typed reason, the timeline reads "Rejected config change (5deb3d5a212c): Not every run needs the unit tests…", and the task goes to `queued`. Both #35 and #36 left the Inbox. (On a first pass the script filled the comment box instead, and Reject change was sent with `"reason":""`. The UI allows an empty reason.)                                                                                                                                                                  | PASS    |
| 10  | Console on config tasks         | **Every page about #35/#36 logged `404 GET /api/repos/grain/config/github-branches`** ("branch listing is not available for the internal config repository"), and the base-branch picker had nothing to offer. Bug 1 below, fixed here.                                                                                                                                                                                                                                                                                                                              | FIXED   |

### 2. Intake

| #   | Step                                 | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                         | Verdict |
| --- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Global New task, queued path         | Title, two-paragraph description, `sample-note.txt` attachment, Repos → acme/widgets, base `release/1.0`, reviewers `alice, bob`, auto-merge unticked (it defaults on), custom fields Requested by and Estimate, vCPUs 2. Filed #37: `queued`, priority 7, `autoMerge:false`, reviewers `["alice","bob"]`, 1 attachment, `customFields {estimate:3, priority:medium, requested-by:Carol at Acme}`, `sandboxCpus:2`.             | PASS    |
| 2   | Task priority                        | 0, −3 and 1.5 each disable Create task, with "a whole number, 1 or more; default 5, higher numbers run first". (A number input cannot take non-numeric text, so "abc" could not be typed at all.)                                                                                                                                                                                                                               | PASS    |
| 3   | Model picker                         | Offers "Deployment default (gemini-2.5-pro) \| gemini-2.5-pro \| gemini-2.5-flash", which is what the demo's connection advertises.                                                                                                                                                                                                                                                                                             | PASS    |
| 4   | Refused field inside Advanced        | vCPUs −2 with Advanced options collapsed: pressing Create task re-opened Advanced options (`aria-expanded` false → true) and did not file. 2 then filed.                                                                                                                                                                                                                                                                        | PASS    |
| 5   | Template picker carries priority     | From `/processes/templates/template-3`, New task from this template opens with Template = urgent-fix and priority 8. Picking `sweep` → 9. Typing 3, then picking urgent-fix again, keeps 3. Filed #38 "Urgent fix" at priority 3. The picker lists only templates without input fields.                                                                                                                                         | PASS    |
| 6   | Template with input fields           | New task from this template on `system:gke-sandbox-cluster` opens "File a task from a template" (Use…), with its four fields filled from their defaults, not the New task form.                                                                                                                                                                                                                                                 | PASS    |
| 7   | Template base vs repo history        | `template-2` (base `main`) opens with an empty Base branch even though acme/widgets' last task used `release/1.0`, and files #39 on `main`. urgent-fix (no base) prefills `release/1.0` from history.                                                                                                                                                                                                                           | PASS    |
| 8   | `/new?field.` links + form config    | `/new?field.title=…&field.requested-by=Dana&field.estimate=2&field.area=api&field.bogus=1`, with custom fields hidden (stub 2), shows: "This link asked for values this form has left out: “title” is not a field on this deployment; Area: “api” is not one of its values; “bogus” is not a field on this deployment; Requested by: “Dana” is in a field this form hides; Estimate (days): “2” is in a field this form hides." | PASS    |
| 9   | Rail create menu → proposed, no repo | Create options offers new task / new session / new board. With No repo ("No repo (standalone task -- nothing to check out)") picked and Queue immediately unticked, the form files #40 as `proposed`, with no repo and `noRepo:true` in the request.                                                                                                                                                                            | PASS    |
| 10  | Fork head label                      | With Create a new branch unticked: `bad::head` → "has more than one ':'…" (Create disabled); `contrib:` → "is not a fork head: it needs both an owner and a branch…" (disabled); `contrib:fix/readme-typo` → "GitHub fork head: branch fix/readme-typo on contrib's fork…", which files #41 on base `main` with `externalPullRequests:["acme/widgets#41"]`.                                                                     | PASS    |

### 3. Tasks list

| #   | Step                         | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                           | Verdict |
| --- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail views                   | tasks 40 → `/?f=state:none:deferred\|closed` (35 rows); proposed 3, waiting 15, running 2, asked 10, merging 5, deferred 1 and closed 2 → `/?state=<s>&f=none`, each with matching row counts; merged 2 → `&sort=merged:desc`; blocked 1 → `/?state=blocked…`, showing #11.                                                                                                                                                                       | PASS    |
| 2   | Row chips                    | #11 has the oval "Blocked on: #8 (running)". #25 has "Merging". #12 has "Imported" plus an `imported` chip titled "imported from GitHub issue acme/widgets#42", and the unapproved sub-case "\"Approve\" ends this wait". #37 has the `p7` badge. #41 has the outlined `acme/widgets#41` chip. #3's `live run` chip carries "running/live_run — Driven by ProcessResult — Waiting on the run in flight — \"Cancel\" ends this wait" as its title. | PASS    |
| 3   | Search, URL, reload, history | "retry budget" → `?q=retry+budget…`, rows 14,16,17,18,19. These survive a reload with the box still filled. Back → `/?f=none` (40 rows), Forward → the search again (5 rows).                                                                                                                                                                                                                                                                     | PASS    |

### 4. Task page

| #   | Step                                  | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Verdict |
| --- | ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Edit                                  | On #40, priority 0 disables Save, with the shared wording. Saving title "Walk proposal edited", description "Edited description" and priority 6 sends `PATCH /api/tasks/40`, and all three are still there after a reload. The timeline records the edit.                                                                                                                                                                                                                             | PASS    |
| 2   | Defer/Undefer/Approve                 | Defer with "until" empty → `deferred`, no wake date. Undefer → `proposed`. With until set to 2026-11-11, Defer sends `{"deferredUntil":"2026-11-11T00:00:00.000Z"}`, and the hint reads "Picks itself back up on 11/11/2026, 12:00:00 AM, back into the proposals for someone to approve.". Undefer clears the date. Approve → `queued`.                                                                                                                                              | PASS    |
| 3   | Close/Reopen at 390 px                | Close → `closed` ("Closed — by operator (access code)"). Reopen → `queued` ("Reopened — by operator (access code)"). With stub 3, #21's timeline shows both the Closed and the Reopened entry with the full ~300-character reason, including an unbroken 140-character word and a long URL. Nothing in the pane is wider than the 390 px viewport (`scrollWidth` 390).                                                                                                                | PASS    |
| 4   | Pause/Resume from the page            | #2 (queued): Pause → `paused.fromState: queued`, state unchanged, Resume shown. After a reload Resume clears it, still `queued`. #8 (running): Pause now asks "Pause 8 now? Its run is stopped at once, without the grace period…" and sends `{"immediate":true}`; the task stays `running` with the pause recorded. Resume clears it.                                                                                                                                                | PASS    |
| 5   | Chat pane (stub 4)                    | #42's compose row: Stop, Pause, Send. Pause → `queued`, `paused.fromState: queued`, Resume shown at once. Resume clears it. Stop → `awaiting_reply` with `turnStopped`, and Retry appears. Retry → `queued`, `turnStopped` cleared, Stop back.                                                                                                                                                                                                                                        | PASS    |
| 6   | Force merge                           | #24 (stuck on a required check): the prompt asks for a reason. Accepting it empty sends `{"reason":""}`, and the 400 "forcing a merge needs a reason…" is shown on the page. With a reason, the timeline records "Asked grain to force the merge of acme/widgets#131 at a1b2c3d4e5f6… Reason given: … What the queue was saying instead: … cla/google" without a reload.                                                                                                              | PASS    |
| 7   | Dependencies                          | On #41, Add a dependency… → 37 → `POST …/depends-on {"id":"37","attach":true}`. The chip reads "37 walk queued task (open)". The rail goes blocked 1 → 2, and the Blocked view lists 11 and 41. Delete on the focused chip detaches it and the rail goes back to 1.                                                                                                                                                                                                                   | PASS    |
| 8   | Goal card                             | Add goal: Base branch green, repo acme/widgets, branch main, at its limit "notify and back off" → `PUT …/goal {"kind":"base-green",…,"on_limit":"notify"}`. After a reload the card reads "Base branch green: main on acme/widgets green. Not met yet. … (limit 6 resumes or 12h, then notify)." Remove goal → `DELETE`, and after a reload the card says "No goal…" with nothing stale left.                                                                                         | PASS    |
| 9   | Work tree, merge policy, CL, attempts | #3's Provenance shows "work from task 13: 0 of 4 done", with links to /tasks/13, 1, 8 and 11. #24 lists the merge-rule lines, each with its source ("ci.github: cla/google -- from rule deploy"…). #14 shows the cl/612400001 chip, "presubmit failed: 2 checks", "changes requested", Mail / Publish replies (2) / Sync, and a disabled Submit with its reasons. #8: "attempt #2 transcript — Still running…", plus "capability audit · 3 records" with argv and the refused record. | PASS    |

### 5. Batch bar and manual mode

| #   | Step                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                             | Verdict |
| --- | ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Bar actions              | With #37–#39 selected, the bar offers Approve, Run selected now, Submit, Retry merge, Defer, Undefer, Close, Reopen, Bulk edit…, a capability picker with Attach/Detach, and Clear selection. Defer → all three `deferred`, and the selection clears afterwards. Undefer → `queued`. Close asks "Close 3 task(s)?"; Cancel leaves them `queued` and selected, and OK → `closed`. Reopen → `queued`. | PASS    |
| 2   | Bulk edit priority       | Ticking Task priority (mixed across the selection) and typing 0, −1 or 2.5 disables "apply to 3 tasks", with the shared wording. Cancel leaves priorities 7, 3 and 5 and keeps the selection. Apply 4 sends three `PATCH {"priority":4}`, after which the rows show `p4` and the custom fields are untouched.                                                                                       | PASS    |
| 3   | Capability attach/detach | API preset: memory → Attach → `POST …/capabilities {"id":"api:memory","attach":true}` on both rows, and Detach removes it again.                                                                                                                                                                                                                                                                    | PASS    |
| 4   | Approve, Submit          | Approve on #12 and #34 → both `queued`. Submit on #6 and #26 → both `completed`.                                                                                                                                                                                                                                                                                                                    | PASS    |
| 5   | Manual mode              | System → Pipeline, reason "walkthrough manual mode", then Pause pipeline (confirmed). The queued view marks 16 rows "paused — waits for Run now". Run selected now on #40 and #1 posts `run-now` for each and sets `runNowAt`. After a reload the marker is gone from both and still on the other 14. Resume pipeline (confirmed) → `DELETE /api/pipeline-pause`.                                   | PASS    |

## Bugs

### Bug 1 — `grain demo -config-change` could not list grain/config's branches (fixed)

**Reproduction:** start `grain demo -config-change`, sign in, and open
`/tasks/35`. The console logs
`404 GET /api/repos/grain/config/github-branches` with
`{"error":"branch listing is not available for the internal config repository on this UI server"}`.
The base-branch picker offers no branches. The same happens on #36.

**Cause:** `configureConfigChangeDemo` gives the UI the throwaway
grain/config's `StateManager` (`ui.Config.StateRepo`) but leaves
`ui.Config.RepoBranches` unset. The daemon sets it to `repoBranchGate`,
which lists grain/config's branches from that same checkout. Without it,
`Client.ListGitHubBranches` answers `errConfigRepoBranchesUnavailable`
(404) for grain/config. This only affects the demo, but `-config-change`
exists to put the config-change card in front of a real repository.

**Fix:** `pkg/demo/demo_config_change.go` now sets `cfg.RepoBranches` to
`configRepoBranches`. For grain/config this lists the manager's branches;
every other repository goes to whatever source was set before.

**Regression coverage:**
`TestConfigChangeDemoApprovesAndRejectsAgainstARealConfigRepo` now lists
grain/config's branches through `ui.Client.ListGitHubBranches` and requires
`main` and both proposal branches. It fails on the base with the message
above and passes with the fix. `go test ./pkg/demo` passes. In the browser
on the fixed build, `/tasks/35` loads with no 404, and the endpoint returns
`["grain/config/35/poll-interval","grain/config/36/prompt-extension","main"]`.

## Observations (not treated as defects)

- **O1:** Close on a queued or proposed task with no pull request acts at
  once (as reported on 2026-11-08). The batch bar's Close asks first.
- **O2:** Force merge's prompt says the reason "is required", yet accepting
  it empty still sends the request. The server refuses it and the page shows
  why, so nothing is lost, but the refusal could be made in the browser
  instead of after a round trip.
- **O3:** Reject on the config-change card sends an empty reason if the box
  is left empty. The session then gets no explanation. The UI could require
  one, as Force merge's server does.
- **O4:** The New task form's grants list (api:self-debug, grain › source,
  github › read-issues) does not show grain › ci, yet the server attaches it
  to acme/widgets tasks as the repository's CI default
  (`applyGrainCIGrant`). This is by design, but the form's note ("Grants
  already listed are the defaults for this repo") reads as if the list were
  complete.
- **O5:** A batch action (Defer, Undefer, Close, Reopen) clears the
  selection afterwards, while Cancel on a confirm or Bulk edit keeps it.

## Not covered this run

The following need rows that only an agent run or a reconciler writes, and
the 2026-11-08 reports covered them with sqlite stubs: host-script approval
(stdin preview, approve and deny), pending-secret box, suspended session,
cross-team approvals, a grain CI verdict with Re-run CI, the
held-for-approval chip, and the config-repo sync-failure group. Inbox empty
and error states were not re-run. Screenshots were taken but could not be
looked at (no Gemini key, and the file reader does not render PNGs), and
none were committed. This change touches no UI code.
