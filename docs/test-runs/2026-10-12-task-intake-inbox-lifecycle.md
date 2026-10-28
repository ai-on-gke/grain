# Task intake, Inbox and lifecycle walkthrough — 2026-10-12

## What was tested

- **Commit tested:** `a40e60a0a295cb832449e5f0aaaa2bc92c14a28c`, the tip of
  `candidate` when this ran and the base of branch `grain/eu/task-3210`. The
  rail's version line read
  `v1.9.0-rc.1.0.20261012074526-a40e60a0a295 · a40e60a`.
- **Fix commit:** `fix(ui): keep an enum field value typed into its
  multi-select` on `grain/eu/task-3210` (Finding 1 below).
- **Environment:** Debian 12 (bookworm) amd64 on Linux 6.1.0-53, with Go
  1.26.6, Node v20.19.0, and Playwright 1.62.1 driving Chromium headless
  shell 151 (`chromium-1234`). Viewport 1400×900, browser timezone UTC.
  Downloading Chromium needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`,
  because the sandbox's package-cache proxy re-signs TLS.
- **Server:** the real `grain demo` server. I ran
  `cd ui && npm ci && npm run build`, then
  `go build -o /tmp/grain ./cmd/grain`, then started
  `grain demo -addr 127.0.0.1:7788 -open=false` with its own throwaway
  embedded SQLite store (`HOME` pointed at an empty temp directory).
- **Browser:** ad-hoc Playwright scripts, kept out of the tree, that used
  visible controls only: role/label locators, clicks, typing, and
  Tab/Enter/Escape. Every script logged console errors and warnings, page
  errors, failed requests, HTTP ≥ 400 responses, and the method and body of
  every mutating `/api` request. After each mutation I re-read the task with
  `GET /api/tasks/{id}` and then reloaded or deep-linked the page.
- **Sign-in:** a fresh browser gets demo's Create account dialog. I created
  `operator` through it and reused the session cookie for the rest of the run.
- **Gemini:** the sandbox had no `/home/debian/.gemini-api-key`. Nothing in
  this walkthrough calls a model. For the queued task I picked
  `gemini-2.5-pro` from the New task Model search.

### Stubs

None. Some external boundaries are simply absent in `grain demo`. I recorded
those rather than stubbing them:

- Every pane or form showing a Base branch box for `acme/widgets` requests
  `GET /api/repos/acme/widgets/github-branches`, which returns **404**
  because demo has no GitHub client. The only symptom is one console
  "Failed to load resource" line, and the box still works as free text.
- Pull-request links (`acme/widgets#101`, `#102`, `#104`) point at
  github.com, which this sandbox can't reach. I checked their `href`s but
  didn't follow them.
- Demo seeds no grain CI runs for these tasks, so the panes had no CI links
  to follow.

## Steps and outcomes

### 1. Inbox

| Step | Outcome |
| --- | --- |
| Cold load of `/inbox` | **Pass.** Count 4, with groups Answer a question (task 4), Submit for merge (6), Read an answer (7) and Approve a proposal (1). Each group has a count and a hint, and the rail badge reads 4. |
| Open from the rail (`/` → Inbox) | **Pass.** It lands on `/inbox`. |
| Row navigation | **Pass.** Clicking task 1's row opens `/tasks/1`, and Back returns to `/inbox`. |
| Decline, then dismiss the confirm | **Pass.** The confirm reads "Close 1 without running it? It can be reopened.". Dismissing it leaves task 1 `proposed`. |
| Approve the seeded proposal (task 1) | **Pass.** `POST /api/tasks/1/approve` succeeds, the group disappears, and task 1 is `queued` and listed under Queued. |
| Answer the seeded question (task 4) | **Pass.** Reply shows the question inline. Send stays disabled for an empty or whitespace-only reply. Sending posts a comment, after which task 4 is `queued` with no pending question. The badge goes to 2, and the reply shows on task 4's timeline. |
| Keyboard triage, then empty state | **Pass.** I focused Submit (task 6) and pressed Enter, then clicked Acknowledge (task 7). Task 6 → `completed` and task 7 → `closed`. The page reads "0 … Nothing is waiting on you…" and the rail badge has no number, both before and after a reload. |

### 2. New task (global form)

| Step | Outcome |
| --- | --- |
| Open from "+ New task" | **Pass.** The URL becomes `/new`. |
| Validation | **Pass.** Create task stays disabled for an empty or whitespace-only title, and for a title with Repos still "—". With "Other…" and `not a repo name`, the server answers 400 `repo must be owner/name…`. The dialog stays open and the message shows in an alert above it (the element at that point is the alert). A vCPUs value of -2 is blocked by the browser's own constraint check, so no request is sent. A deferral date in the past is refused by the server with a clear alert. |
| Queued path, every option | **Pass, except Area (Finding 1).** I set title, a two-paragraph description, Priority high, Requested by, Due date, Estimate, Customer-facing Yes, repo `acme/widgets`, base `release-1.0`, auto-merge off, GCP key unticked, model `gemini-2.5-pro`, Network access None, nothing-to-do "Leave for acknowledgement", a prompt extension, and vCPUs/memory/disk 4/8192/40. Task 13 came back with every value as entered (`capabilities: [gemini-key, self-debug]`, `networkAccess: none`, `nothingNeededAction: ack`). The exception is Area: I typed "backend" and pressed Enter, and the request carried `area: []`. |
| Proposed path, No repo | **Pass.** No repo, Queue immediately unticked, Priority low, and Area "docs" picked with ArrowDown+Enter: the request carries `noRepo: true, approved: false`, and task 14 is `proposed` with `area: docs`. |
| Escape | **Pass.** Escape with an Autocomplete or Select popup open (Area, Model, Capabilities) closes only the popup. Escape with nothing open closes the form and keeps the draft, as designed. |

### 3. All tasks, state views, narrowing

| Step | Outcome |
| --- | --- |
| Search | **Pass.** "walkthrough" narrows to tasks 13 and 14 and writes `q=walkthrough`. A reload keeps both the box and the result. |
| Sort | **Pass.** Backlog order → Sort by… → Priority writes `sort=custom.priority:asc` (low, medium, high, then unset). "Last to first" switches to `:desc` and keeps unset last. Then by… Title writes `custom.priority:desc,title:asc`, and ties come out in title order. |
| Compound narrowing | **Pass.** Filter Priority is any of high, plus search "queue", leaves tasks 4 and 13, and survives a reload. A deep link to `/?f=custom.priority:any:high` gives 2, 4, 5, 13 and shows the condition as a chip. Clear goes back to the default conditions. |
| State views | **Pass.** From the rail, Queued, Proposed, Blocked (task 11), Deferred (task 9), Closed (task 10) and Running (3, 8) each list exactly their tasks. |
| Back and Forward | **Pass.** Back through Closed → Deferred → Blocked, and Forward again, restores each view. From a narrowed list, opening a row and pressing Back restores the narrowing. A second Back leaves the list, which is the documented replace-on-edit history design. |
| Keyboard reach | **Pass.** Tab goes through the rail, Search, Filters, sort, each filter chip, Select all, and then each row's Move / Select / title link in order. |

### 4. Task detail

| Step | Outcome |
| --- | --- |
| Entry points | **Pass.** Inbox row, All tasks row, rail state views, and cold `/tasks/{id}` deep links (1, 3, 4, 5, 6, 7, 8, 14). |
| Title and description | **Pass.** Cancel discards the edit with no request sent. Save sends one PATCH, and the header shows the new title after a reload. |
| Repo, base, auto-merge | **Pass.** Task 14 went from no repo to `acme/widgets` and base `release-2.0` (via Enter), and auto-merge was turned off. Each change is one PATCH and persists. |
| Custom fields | **Pass, except Area (Finding 1).** Priority high and Requested by persist. Area "backend" + Enter was dropped from the PATCH. |
| Proposal lifecycle (task 14) | **Pass.** Defer with no date → `deferred`. Undefer → `proposed`. A past date gives a 400 alert and the task stays `proposed`. Defer until 2026-12-31 → `deferredUntil: 2026-12-31T00:00:00Z`; the task is listed under Deferred, and the pane reads "Picks itself back up on 12/31/2026, 12:00:00 AM, back into the proposals…". Undefer → `proposed`. Approve → `queued`, which survives a reload. |
| Attempts, transcripts, chat, CI | **Pass, as far as the seed allows.** Running tasks 3 and 8 show their live attempt transcripts inline. A comment posted on task 3 survives a reload. Cancel on running task 3 asks "Cancel this job? Its run will be abandoned…", and dismissing it leaves the task `running`. Tasks 5, 6 and 8 link their PRs to github.com. There were no CI runs to link (see Stubs). |

### 5. Bulk selection and edit

| Step | Outcome |
| --- | --- |
| Select rows | **Pass.** Ticking 13 and 14 shows "2 selected" with Approve, Submit, Defer, Undefer, Close, Reopen, Bulk edit…, the capability picker, and Clear selection. |
| Cancel a bulk edit | **Pass.** Cancel sends no PATCH and leaves both rows ticked. A reopened form starts with nothing ticked, and mixed fields are labelled "(mixed across the selection)". |
| Apply | **Pass.** Due date 2026-12-15 and Auto-merge Off send one PATCH per task. Each task keeps its own priority, requested-by, estimate, base and model, and the selection clears. |
| Off-screen selection | **Pass.** With task 13 searched out of view, the bar says "1 not shown here", and Close confirms "Close 2 task(s)? 1 of them is not shown in this view.". Dismissing the confirm leaves both tasks `queued`. Clear selection empties the bar, and the selection does not come back after a reload. |

## Findings

### Finding 1 — an enum multi-select dropped a typed option (fixed)

**Reproduction:** deterministic, in Chromium against `grain demo`:

1. Open + New task, fill a title, and pick `acme/widgets`.
2. Click into **Area** (a multi-valued enum: frontend, backend, infra, docs).
   Type `backend`. The list filters to the single option "backend".
3. Press Enter. No chip appears and the text stays in the box.
4. Click Create task. The request carries `"area": []` and the task has no
   Area. There is no warning; the text simply disappears.

The same thing happens in the task pane's **Edit fields** and in **Bulk
edit**, because all three render `MultiValueInput` from
`ui/src/components/CustomFields.jsx`. Only ArrowDown before Enter, or a
mouse click on the option, ever added a value.

**Cause:** the multi-select Autocomplete enabled `autoSelect` only for
free-text fields, and never `autoHighlight`. For an enum, Enter had
nothing highlighted to pick, and on blur the input was cleared.

**Fix:**

- For an enum, `autoHighlight` makes Enter pick the first option still
  listed.
- Leaving the box with text that names an option exactly (ignoring case)
  adds that option. This mirrors how free text is already committed on
  blur.
- Text that names no option is still discarded, because it is not a
  value.
- `autoSelect` stays off for enums. With an option always highlighted, it
  would add the first one when someone merely tabbed through the box.

**Regression coverage:**

- `ui/src/components/CustomFields.test.jsx` has three new tests: typed +
  Enter picks the option; typed in full + Tab commits it, case-insensitively;
  passing through, or leaving a partial match, adds nothing. Without the fix
  the first two fail and the third passes.
- `ui/e2e/task_intake_inbox_lifecycle_walkthrough.spec.js`: the proposed
  task now types `backe` + Enter and then `docs` left in the box, clicks
  Create, and asserts `customFields.area` is `["backend", "docs"]` on the
  server. Against a build without the fix, this step fails. With the fix,
  the spec and `inbox.spec.js` pass (9 tests, Chromium).
- I re-checked by hand on a rebuilt demo. Edit fields on task 1 with
  `back` + Enter and `INFRA` left in the box saved
  `area: [frontend, backend, infra]`. Bulk edit with `doc` + Enter set
  `docs` on both tasks.

### Observations (no change made)

- Every Base-branch box for `acme/widgets` logs a console 404 for
  `/github-branches` in demo (see Stubs). This is harmless, and it only
  happens because demo has no GitHub client.
- Task 5's "File a task with these fields" link carries
  `field.sprint=2026-Q2`, but `sprint` is not a declared field. It is seed
  data from a retired declaration. The form ignores unknown fields.
- As the 10-11 run noted, with "No repo" chosen the New task form still
  shows Base branch and auto-merge, and a no-repo task's pane still shows
  the auto-merge checkbox. Neither does any harm.
- In Bulk edit, "Agent framework" reads "(mixed across the selection)"
  for two tasks that set none explicitly. One of them names a model,
  which is presumably where the framework is derived from. Cosmetic.
