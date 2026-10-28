# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-01

## What was tested

- **Commit:** `aeba486fa` (tip of `candidate`; `grain/task-1355` branched from here).
  Every finding below was reproduced on this commit. The fixes on this
  branch were then re-checked in the same browser, against a rebuilt
  server and frontend.
- **Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64
  x86_64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with headless
  Chromium (chromium-headless-shell 1234), viewport 1400×900, browser
  time zone UTC.
- **Server:** the real `grain demo -addr 127.0.0.1:18080 -open=false`,
  built from this tree (`go build ./cmd/grain`). It ran over a throwaway
  embedded SQLite store under a scratch `TMPDIR`, which was deleted and
  re-seeded each time the binary was rebuilt. The frontend was the
  production build (`npm run build` into `pkg/ui/static`, embedded in
  the binary).
- **Driver:** ad-hoc Playwright scripts, kept outside the repository and
  not committed. They drove visible controls: clicks, typing, keyboard,
  Back/Forward, reload and deep links. They read results from the page.
  The REST API was used only to *confirm* persisted state after a UI
  action, never to perform one. Every script logged browser console
  errors and warnings, page errors, failed requests and HTTP ≥ 400
  responses.
- **Account:** created through the first-run "Create account" dialog
  (`operator`). The session was reused through Playwright storage state.
- **Gemini key:** `/home/debian/.gemini-api-key` was not present in this
  sandbox, so nothing model-backed was exercised. The demo needs no
  model.

## Stubs

None of grain's own components were stubbed or mocked. The only things
missing were outside it:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/acme/widgets/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions"
  (`branchRefs.js`). It is the only console error seen on these screens.
- **CI** has no runs in the demo (rail "CI 0"). So the CI links on task
  detail could only be checked for absence, not followed.

## Steps and outcomes

### 1. Inbox

1. Cold-loaded `/inbox`, and also opened it from the rail. Four groups,
   one task each, with counts that agree with the rail's Inbox badge (4):
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. Clicked a row title (#1). Its pane opened at `/tasks/1` over the
   inbox, and Back returned to `/inbox`.
3. Clicked **Reply** on #4. The parked question rendered and the reply box
   took focus. Send with an empty box did nothing. Answered and sent:
   #4 left the group and became `queued`, with the comment authored by
   `operator`.
4. Clicked **Decline** on #1. A confirm appeared ("Close 1 without
   running it? It can be reopened."); dismissing it left #1 `proposed`.
   **Approve** then moved #1 to `queued` and it left the group.
5. After a reload the rail badge read 2. **Submit** (#6 → `completed`) and
   **Acknowledge** (#7 → `closed`) emptied the inbox: "Nothing is waiting
   on you…", and the rail badge disappeared.

**Result: pass.**

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:**
   - Create is disabled with an empty or whitespace-only title, and
     until a repo (or "No repo") is picked.
   - A vCPU of `-2` is blocked by native constraint validation ("Value
     must be greater than or equal to 0."), which focuses the field.
     Number fields refuse non-numeric text.
2. **Queued path:** filled in
   - title and Markdown description;
   - priority high, requested-by, due date, estimate and
     customer-facing Yes;
   - repo `acme/widgets`, base `release-1.2`, auto-merge;
   - the default capabilities;
   - **Advanced options**: listed the model, agent framework, network
     and review options, then set network `none`, a prompt extension and
     2 vCPU / 4096 MiB / 30 GiB.

   The submitted task #13 was `queued` and every one of those fields
   persisted.
3. **Draft, cancel and discard:**
   - Escape closed the form and kept the draft. Reopening `/new`, and
     reloading it, restored the draft.
   - **Discard draft** cleared it, including after a reload, and the
     button then disappeared.
4. **Proposed path:** "No repo" with "Queue immediately" unticked. Task
   #14 was `proposed`, had no repo, and appeared in the Inbox's
   proposal group.

**Result: pass, with two observations (listed below).**

### 3. All tasks, state views, Blocked, search, sort, filters

1. Clicked Queued, Proposed, Deferred, Closed and Blocked in the rail.
   Each list was right on screen.
   **Bug 1:** the URL never changed between state views. Back skipped
   them all. Reloading Closed opened All tasks with closed and deferred
   tasks mixed in, because the rail drops the State condition from the
   narrowing (see Bugs).
2. After the fix, each view has its own URL (`/?state=closed&f=…`).
   Back, Forward and reload all restore it.
3. **Search:** "pagination" inside Queued narrowed to #11 and was written
   to `q=`. Clearing it removed `q=`.
4. **Sort:** Priority, last to first, was written as
   `sort=custom.priority:desc`.
5. **Compound narrowing:** Queued + Priority any of high + the
   interactive exclusion narrowed to #2. A trip to Closed, Back, and a
   reload each restored exactly that URL and list.
6. **Opening a row:** clicking a row opened `/tasks/2`. Escape returned to
   the same narrowed URL.
7. **Keyboard:** Tab from page start reaches "+ New task", the team
   picker, every rail state and every other rail destination in order.

**Result: fail on the commit tested (bug 1); pass after the fix.**

### 4. Task detail

1. **Entry points:** an Inbox row, a list row, and deep links
   `/tasks/1`, `/tasks/3`, `/tasks/5`, `/tasks/6`, `/tasks/8`.
2. **Edits** on #1, each checked through the API:
   - **Edit → Save** of title and Markdown description persisted, and
     survived a reload.
   - Escape while editing closed the pane and discarded the edit (see
     observations).
   - Base branch persisted both on Enter (`release-9`) and on blur
     (`release-10`). The repo picker offers `acme/widgets` and Other….
   - **Edit fields → Save fields:** customer-facing No, requested-by
     Fabrikam.
   - **Bug 3:** the read-only rows printed customer-facing as raw
     "true"/"false", where the editor offers Yes/No. Fixed; they now
     read "No".
   - The auto-merge checkbox toggled `false → true`.
3. **Lifecycle:**
   - **Defer** with no date → `deferred`; **Undefer** → back to
     `proposed`.
   - Defer until 2026-12-31 → "Picks itself back up on 12/31/2026,
     12:00:00 AM." with `deferredUntil` 2026-12-31T00:00:00Z.
   - Picking it back up worked.
   - A past date (typed past the input's `min`) was refused with a
     visible error ("wake time … is not in the future"). The task stayed
     `proposed`.
   - **Approve** → `queued`.
4. **Running and merged tasks:**
   - #3 (running) shows the live attempt #1 transcript inline.
   - #8 (repairing) shows its merge-queue repair note and attempt #2.
   - A comment posted with the **Comment** button appeared with author
     `operator`, and the box cleared. Ctrl+Enter does not send.
5. **Bug 2:** the pull request link on #5, #6 and #8 pointed at
   `https://github.com/https://github.com/acme/widgets/pull/101/pull/undefined`.
   After the fix it points at `https://github.com/acme/widgets/pull/101`.

**Result: fail on the commit tested (bugs 2 and 3); pass after the fixes.**

### 5. Selection and bulk edit

1. In Queued, ticked #2 and #11 → "2 selected" bar.
2. The selection survives switching to Closed and Back, which is
   deliberate (`App.jsx`, "A selection made on the list survives…").
3. **Bulk edit…:** Apply is disabled until a field is ticked. Fields that
   differ are labelled "(mixed across the selection)". **Cancel** closed
   the dialog, kept both rows ticked, and changed nothing (checked through
   the API).
4. Re-opened it and ticked:
   - Priority = high
   - Base branch = `release-2.0`
   - Requested by = `Bulk Co`
   - Customer-facing left at Not set

   **Apply to 2 tasks** closed the dialog and cleared the selection.
   Both tasks carry the new values. Unticked fields kept their values
   (area; #11's estimate 3). The rows show the new chips after a reload.

**Result: pass.**

### Console and network

- **On every screen:** no page errors and no console errors, apart from
  the GitHub branch-list 404 above.
- **Polls:** Chromium reports each 3-second `/api/tasks` poll that is
  answered `304 Not Modified` as `net::ERR_ABORTED`. This is expected:
  `api.js` polls with `cache: "no-store"` and `If-None-Match`, and
  nothing on screen is affected.

## Bugs found and fixed on this branch

### 1. Rail state views were not in the URL

**Reproduction:** open `/`, click **Closed** in the rail, then reload.

- **Before:** the URL is `/?f=kind:none:interactive`. All tasks opens,
  with closed and deferred tasks included. Clicking Deferred and then
  Closed and pressing Back leaves the app instead of returning to
  Deferred.
- **Fix:**
  - `paths.js` reads and writes `state=<id>` on the flat list.
  - `App.jsx` seeds the rail state from it, restores it on popstate, and
    pushes a history entry when only the state changes.
  - A link naming only the state (`/?state=closed`) takes the default
    narrowing without its State condition, as a rail click would.
- **Coverage:**
  - `paths.test.js`: parse, build and round-trip.
  - `App.narrowing.test.jsx`: cold load, Back, and a state-only link.
  - e2e `task_intake_inbox_lifecycle_walkthrough.spec.js`: "keeps a rail
    state view across reload, Back and Forward".

### 2. The demo's pull request links were broken

**Reproduction:** open `/tasks/5` and inspect the "acme/widgets…" link
beside "as of just now".

- **Cause:** `cmd/grain/demo.go` seeded `LinkFixes` targets as full
  URLs. Real runs record `owner/name#N` (`model.PullRequestRef.String`),
  and the UI's `pullRequestUrl` reads that form.
- **Fix:** the seed now uses `acme/widgets#101` / `#102` / `#104`.
- **Coverage:**
  - `TestSeedDemoCoversEveryState` now parses every seeded pull request.
    It fails on the old seed.
  - e2e "links a seeded task's pull request to GitHub".

### 3. Boolean custom fields read "true"/"false" in the task pane

**Reproduction:** open `/tasks/1`. Customer-facing reads "true".

- **Fix:** `CustomFields.jsx`'s `fieldRows` shows declared booleans as
  Yes/No, matching the editor and the filter menu.
- **Coverage:** `CustomFields.test.jsx`.

## Observations, not changed

- **New task → Area:** typing a partial value such as "back" and pressing
  Enter selects nothing (MUI Autocomplete without `autoHighlight`). The
  keyboard path is ArrowDown then Enter.
- **New task → No repo:** Base branch and Auto-merge stay editable when
  "No repo" is chosen, and the deployment's auto-merge default is filed
  on the repo-less task. This is harmless, since there is nothing to
  merge.
- **Task pane title/description edit:** Escape closes the whole pane
  (discarding the edit) rather than cancelling just the edit.
- **Batch bar:** it stays visible in state views where none of the
  selected tasks are shown. This is by design.

## Tests run

- `npx vitest run`: 156 files, 2677 tests, all passed.
- `npx playwright test --project=chromium`:
  - `task_intake_inbox_lifecycle_walkthrough`, `paths`, `tasks`,
    `inbox`, `narrow`, `sidebar-navigation`,
    `track6-navigation-merged-dissolve`, `batch_actions_workflows` and
    `task_detail_workflows`: 44 tests, all passed.
- `go test ./cmd/grain`: passed.
- **Grain CI:** the first full run failed `ui e2e`. Two older specs
  (`fields.spec.js` and
  `interactive-task-intake-board-views-multiteam-walkthrough.spec.js`)
  asserted the raw "true"/"false" that bug 3 replaced with Yes/No. Both
  now expect Yes/No and pass locally. The full suite then passed on
  `22d8151` (run `b9fa4e770c60c9f0`): frontend, go, installer, lint,
  package cache, terraform and ui e2e.
