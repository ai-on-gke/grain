# Task intake, Inbox and lifecycle walkthrough — 2026-10-11

## What was tested

- **Commit tested:** `ab0ed8ca9e0cdedb1088b33e40292db36013a2ee` (the tip of
  `candidate` when this ran, and the base of branch `grain/task-2956`). The
  version line in the rail read
  `v1.9.0-rc.1.0.20261011200744-ab0ed8ca9e0c · ab0ed8c`.
- **Fix commit:** `fix(ui): show a task's network access override on its
  detail pane` on `grain/task-2956` (Finding 1 below).
- **Environment:** Debian 12 (bookworm) amd64, Linux 6.1.0-53, Go 1.26.6,
  Node v20.19.0, Playwright 1.62.1 with Chromium headless shell 151
  (`chromium-1234`). Viewport 1400×900, browser timezone UTC except where
  noted.
- **Server:** the real `grain demo` server, built with
  `go build -o /tmp/grain ./cmd/grain` after `cd ui && npm ci && npm run build`,
  started as `grain demo -addr 127.0.0.1:7788 -open=false` over its own
  throwaway embedded SQLite store.
- **Browser:** ad-hoc Playwright scripts (kept out of the tree) driving
  visible controls: role/label locators, clicks, typing, Tab/Enter/Escape.
  Every step logged console errors and warnings, page errors, failed
  requests and HTTP ≥ 400 responses. Each mutation was checked again with a
  read-only `GET /api/tasks/{id}`, and by reloading or deep-linking the page.
- **Sign-in:** the browser has no stored username, so the demo shows its
  Create account dialog on first load. I created an account (`operator`)
  through that dialog and reused the session cookie for the rest of the run.
- **Gemini:** the sandbox had no `/home/debian/.gemini-api-key`. Nothing in
  this walkthrough calls a model. In the New task form I picked
  `gemini-2.5-pro` from the Model list for the queued task.

### Stubs

None. Two external boundaries simply aren't present in `grain demo`, so I
recorded them rather than stubbing them:

- `GET /api/repos/acme/widgets/github-branches` returns **404** every time a
  form or pane shows a Base branch box for `acme/widgets`, because demo has no
  GitHub client. The Base branch box stays a free-text field and works; the
  only symptom is a console "Failed to load resource" line.
- Pull-request links (`acme/widgets#101`, `#102`, `#104`) point at
  github.com, which the sandbox can't reach. I checked their `href`s but
  didn't follow them.

### Noise that is not a fault

- `GET /api/tasks … net::ERR_ABORTED` appears in the request log about every
  three seconds. These are the list poll's `If-None-Match` requests being
  answered `304 Not Modified` (confirmed with curl:
  `If-None-Match: <etag>` → `304`), which Chromium reports to Playwright as
  aborted. The list kept updating throughout.

## Steps and outcomes

### 1. Inbox

| Step | Outcome |
| --- | --- |
| Cold load of `/inbox` | **Pass.** Heading "Inbox" with count 4. Groups in this order: Answer a question (1, task 4), Submit for merge (1, task 6), Read an answer (1, task 7), Approve a proposal (1, task 1). Each group shows its count and hint. The rail's Inbox badge read 4. |
| Open from the rail (`/` → Inbox) | **Pass.** Lands on `/inbox`. |
| Row navigation | **Pass.** Clicking "Add a dark mode toggle" opens `/tasks/1` over the Inbox. Browser Back returns to `/inbox`. |
| Decline, then cancel the confirm | **Pass.** The confirm reads "Close 1 without running it? It can be reopened.". Cancelling leaves the task `proposed` and the row in place. |
| Approve the seeded proposal (task 1) from its row | **Pass.** The row and the whole "Approve a proposal" group disappear, and the Inbox badge goes 4 → 3. The server shows task 1 `queued`, and the Queued view lists it. |
| Answer the seeded question (task 4) | **Pass.** Reply opens the question inline ("Two PRs are both stuck failing the same lint check…") with a textbox labelled "Reply to 4". Send stays disabled while the textbox is empty. Sending removes the row, the badge goes 3 → 2, and the server shows task 4 `queued` with no pending question. |
| Keyboard triage | **Pass.** Tab from the heading reaches each row's link and then its action button, in order. Focusing Acknowledge (task 7) or Submit (task 6) and pressing Enter performs the action. |
| Empty state | **Pass.** With every group answered the page reads "0" and "Nothing is waiting on you. Every task is either running, queued or done.", and the rail badge has no number. Task 7 → `closed`; task 6 → `completed`, which the UI shows as queued for merge. |

### 2. New task (global form)

| Step | Outcome |
| --- | --- |
| Open from "+ New task" | **Pass.** The URL becomes `/new` and the dialog is named "New task". |
| Validation | **Pass.** Create task stays disabled with an empty or whitespace-only title, and with a title while Repos is still "—". With "Other…" and a malformed repo (`not a repo name`) the button enables, the server answers 400 `repo must be owner/name, got "not a repo name" …`, the dialog stays open and the error is shown. |
| Queued path, every option | **Pass.** I set title, description, Priority high, Area frontend, Requested by, Due date, Estimate 3, Customer-facing Yes, repo `acme/widgets`, base `release-1.0`, auto-merge off, GCP key unticked, model `gemini-2.5-pro`, Advanced → Network access None, nothing-to-do "Leave for acknowledgement", vCPUs 4 / memory 8192 / disk 40, and a prompt extension. Task 13 came back from the server with every value as entered: state `queued`, `capabilities: [gemini-key, self-debug]`. |
| Proposed path, No repo | **Pass.** With "No repo" chosen, Queue immediately unticked and Priority low, the request carries `noRepo: true, approved: false`, and task 14 is `proposed`. |
| Escape and Close with a dirty draft | **Pass.** Escape after a dropdown has closed, and the Close dialog button, both close the form and keep the draft (title and Area are still there on reopen). Discard draft clears it and leaves the form open. |
| Prefill link `/new?field.priority=low&field.area=docs` | **Pass.** Priority shows low and Area shows docs. |

### 3. All tasks, state views, narrowing

| Step | Outcome |
| --- | --- |
| Search | **Pass.** "walkthrough" narrows the list to tasks 13 and 14 and writes `?q=walkthrough` into the URL. Reload keeps both the box and the result. |
| Sort | **Pass.** Sort by… → Priority writes `sort=custom.priority:asc`. "First to last" orders low, medium, high, then unset, matching the field's declared option order (`low, medium, high`). |
| Compound narrowing | **Pass.** Adding filter Priority is any of high (`f=custom.priority:any:high`) on top of the default State and Kind conditions, then the search "queue", leaves exactly tasks 4 and 13. All of it survives a reload. |
| State views | **Pass.** Queued (`?state=queued`) and Blocked (`?state=blocked`: task 11, which waits on task 10) list the right tasks. A search typed under Queued carries over to Blocked, which is how the narrowing model is meant to work. |
| Back and Forward | **Pass.** All tasks → Queued (+ search) → Blocked → task 11's pane: Back steps through each in reverse with its search restored, and Forward replays them, ending on the pane. Editing a search or filter replaces the current history entry rather than pushing a new one. That's deliberate (see the comment above the history effect in `App.jsx`): Back from a narrowed list leaves the list. |
| Unknown deep links | **Pass.** `/tasks/99999` shows "no task 99999" and falls back to `/`. An unknown `?state=` value is normalized to `/`. |

### 4. Task detail

| Step | Outcome |
| --- | --- |
| Entry points | **Pass.** I opened tasks from an Inbox row, an All tasks row, a Proposed view row, a Blocked view row, and a cold `/tasks/{id}`. |
| Title and description | **Pass.** Cancel discards the edit. Save sends a PATCH, and the new title is in the header straight away and after reload. |
| Repo and base | **Pass.** Task 14's repo changed from "—" to `acme/widgets` (`PATCH {"repo":"acme/widgets"}`). Task 13's base changed from `release-1.0` to `main` via Enter. Both persisted. |
| Custom fields | **Pass.** Edit fields → Priority medium, Requested by cleared, Estimate 5 → Save fields. After reload the row shows the new values and Requested by is gone, and the fields I didn't touch (due date, area, customer-facing) are unchanged. |
| Auto-merge | **Pass.** The checkbox sends `PATCH {"autoMerge":…}` and persists. It is driven by the server's answer rather than flipped optimistically, so it changes a moment after the click. |
| Proposal lifecycle (task 14) | **Pass.** Defer with no date → `deferred`. Undefer → `proposed`. Defer until 2026-12-31 → `deferredUntil: 2026-12-31T00:00:00Z`, and the pane reads "Picks itself back up on 12/31/2026, 12:00:00 AM, back into the proposals…", which survives reload. Undefer → `proposed`. Approve → `queued`. Withdraw approval → `proposed`. Close → `closed`. Reopen → `proposed`. |
| Timezone | **Pass.** With the browser in America/Los_Angeles, deferring task 2 to 2026-12-31 still stored midnight UTC, as the hint under the date box says, and the pane showed that instant. Undeferring returned task 2 to `queued`. |
| Attempts, transcripts, chat, CI | **Pass, as far as the seed allows.** Task 3 (running) has a live Attempt #1. Clicking the attempt opens the "Attempt #1 transcript" dialog with preformatted logs, and Escape closes just that dialog. The attempt row itself has `tabIndex=-1` by design (grain/task-1582); keyboard users open the transcript from the button in its title. A comment posted from the Reply box shows at once and survives reload. Tasks 3, 5 and 6 link their pull requests to github.com. Demo seeds no grain CI runs for these tasks, so there were no CI links to follow. |
| Run options shown on the pane | **Fail → fixed (Finding 1).** |

### 5. Bulk selection and edit

| Step | Outcome |
| --- | --- |
| Select rows | **Pass.** Ticking 13 and 14 shows the bar: "2 selected", Approve, Submit, Defer, Undefer, Close, Reopen, Bulk edit…, a capability picker, and Clear selection. |
| Cancel a bulk edit | **Pass.** Bulk edit… → tick Priority → Cancel, and separately Escape: no request is sent, both rows stay ticked, the bar still says "2 selected", and a reopened form starts with nothing ticked. |
| Apply | **Pass.** Priority high and Area backend → "Apply to 2 tasks" sends one PATCH per task. Task 13 keeps its due date, estimate and customer-facing; task 14 keeps its fields. The selection clears afterwards. A ticked field left empty clears that field on every selected task; that is deliberate and documented in `BulkEditOverlay.jsx`. |
| Off-screen selection | **Pass.** After narrowing so that task 14 is hidden, the bar says "1 not shown here", and Close's confirm reads "Close 2 task(s)? 1 of them is not shown in this view.". Cancelling it changes nothing. Bulk Defer and Undefer act on the whole selection, hidden rows included, without a confirm, which matches the bar's design comment. |

## Findings

### Finding 1 — a task's network-access override never appeared on its own page (fixed)

**Reproduction:**

1. In `grain demo`, open + New task.
2. Pick a repo, open Advanced options, set Network access to "None (this
   deployment only)", and create the task.
3. Open the task. The settings column lists Sandbox vCPUs, Model, Prompt
   extension, "When the agent finds nothing to do", and so on, but has no row
   for network access. `GET /api/tasks/{id}` does return
   `"networkAccess": "none"`. Nothing on the page shows the override.

**Cause:** `ui/src/components/detail/Declared.jsx` builds a row for each
per-task override but had no case for `networkAccess`.

**Fix:** a "Network access" row, shown only when the task carries an
override, named as the New task picker names it ("Egress (outbound internet
only)" / "None (this deployment only)").

**Regression coverage:**

- `ui/src/components/detail/Declared.test.jsx`: both values are named, and a
  task on the deployment default gets no row.
- `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`: the spec now
  picks an actual override. It used to pick the first option matching
  `/open|default/`, which is "Deployment default", so it never set one. It
  then asserts the row on the task's detail pane. Without the fix this
  assertion fails ("element(s) not found"); with it, the spec and
  `inbox.spec.js` pass (9 tests, Chromium).

### Observations (no change made)

- **Close on the task pane has no confirm**, unlike Inbox Decline and bulk
  Close. Cancel on a running task does confirm. A closed task can be
  reopened, and closing its pull request too takes a separate opt-in
  checkbox, so I left this alone. It is worth a product decision if the
  inconsistency matters.
- **The rail's "Tasks" count** is every task (14 at the time), including
  deferred, closed and interactive ones. The All tasks heading counts the
  default-narrowed list (11). The per-state rail entries account for the
  difference.
- **A no-repo task's detail pane** shows its repo as "—", and that picker has
  no "No repo" entry. Nothing else marks the task as standalone, but it can't
  be confused with a broken repo either, so I filed nothing.
- **With "No repo" chosen in New task**, the Base branch box and the
  auto-merge checkbox stay on screen, and the request still sends
  `autoMerge: true`. This is harmless (there is nothing to merge), just noise
  in the form.
