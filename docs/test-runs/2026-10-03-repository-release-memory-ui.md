# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-03
- **Task**: grain/task-1627
- **Status**: Passed with one defect fixed in this change: a keyboard
  reorder dropped focus to `<body>`. Several observations are recorded
  below. None of them needed a new task.

## Environment

- **Commit walked through**: `11606c9fcb64` (`candidate` tip at the start of
  the run; footer stamp `v1.6.0-rc.1.0.20261003060538-11606c9fcb64 · 11606c9 ·
  2026-10-03 06:05Z`). The fix was checked on this branch, with the frontend
  and server rebuilt and a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64, 4 vCPU. Go 1.26.6,
  Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34, driven directly by the
  Playwright library (not jsdom). The viewport was 1400×950. Every script
  logged:
  - console errors and warnings;
  - page errors;
  - failed requests;
  - every response of 400 or above;
  - every mutating request, with its body;
  - every `confirm()` dialog, which the script either accepted or dismissed.
- **Server**: the real `grain demo`.
  - Built by running `npm ci && npm run build` (frontend into
    `pkg/ui/static`), then `go build -o /tmp/grain ./cmd/grain`.
  - Started as
    `TMPDIR=/tmp/run1 grain demo -addr 127.0.0.1:8420 -open=false` over a
    fresh embedded SQLite store.
  - The fix was verified on a second build started on `127.0.0.1:8421`
    under `TMPDIR=/tmp/run2`.
- **Account**: `operator`, created through the first-run **create account**
  dialog. The session was carried between scripts as Playwright storage
  state.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so no Gemini-backed tooling was used.
- **Setup note**: `npx playwright install chromium` was run with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, as on earlier
  runs.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo has no GitHub (it answers 404) | `page.route` on this path only | success `["main","candidate","release/3.0"]`; empty `[]`; a 3 s delayed success; 502 `{"error":"GitHub: bad gateway"}` |
| GitHub release reconciler (release provisioned, candidate cut, qualification sync) | No reconciler runs in the demo | The committed `ui/e2e/releasehelper`, built to `/tmp/releasehelper` and run as `-port 8420 <cmd>`. It calls `MarkReleaseProvisioned`, `MarkCandidateCut` and `orchestrator.SyncQualifications` on the demo's own SQLite store, and it acts on the oldest unmerged release (`ok-1003` here). | provisioned, cut |

Grain's own API, the React UI and every user gesture were real everywhere
else. Read-backs used plain `fetch` from the page against the same server:
`GET /api/repos/acme/widgets/branches`, `GET /api/tasks` and
`GET /api/config`.

## Steps and outcomes

### 1. Repos list

1. **Opening it**:
   - Focused the rail's **repos** entry (`div`, `tabindex=0`) and pressed
     **Enter**. The router went to `/repos`.
   - A cold `page.goto("/repos")` drew the same page.
2. **Counts**: the header badge read `1` for one row, `acme/widgets`. The
   row showed state chips (proposed 1, queued 3, running 2, …, blocked 1) and
   `12 tasks`.
3. **Add repo** (the inline `owner/name` box):
   - Added `acme/gadgets`,
     `example-corporation/exceptionally-long-repository-name-for-responsive-layout-checks-2026`
     and `zeta/alpha-tools`. None of them asked for confirmation, which is
     right because the allowlist was not empty. The badge went to `4`.
   - Re-adding `acme/widgets` was a silent no-op: no error and no
     duplicate row.
   - `bad//name` was refused inline with
     `repo: repo must be owner/name, got "bad//name"` (HTTP 400).
4. **Search**: `ALPHA` showed only `zeta/alpha-tools`, and the badge
   followed to `1`. `qqq` showed "No repos match your search."
5. **Sort** (an MUI combobox named "Sort"; it opens with Enter from the
   keyboard). Its options are custom order, name (a–z), active first and
   most tasks.
   - *Name* sorted alphabetically and showed no handles.
   - *Most tasks* put `acme/widgets` first and showed no handles.
   - *Custom order* showed four `Move …` handles.
6. **Keyboard reorder**:
   - Focused `Move zeta/alpha-tools` and pressed Enter. A "Moving 1 repo —
     choose where it goes" banner appeared, with slots labelled
     "Move here, above acme/gadgets", "…above acme/widgets" and
     "…above example-corporation/…".
   - Enter on the first slot moved the repo to the top.
   - **Bug**: `document.activeElement` was then `<body>`. See *Fix in this
     change* below.
7. **Pointer drag**: dragged the `acme/gadgets` handle onto the first row.
   - The order became gadgets, zeta, widgets, long.
   - It survived a reload, held in `localStorage["grain.list-order.repos"]`,
     and the sort still read "Custom order".
8. **Click/tap pickup**: clicked `Move acme/widgets` and then
   "Move here, to the end of the list". The row moved to the end.

### 2. Repo page and navigation

1. Clicked the `acme/widgets` row name, which opened `/repos/acme/widgets`.
2. **The task-count link**:
   - **12 tasks filed here** (`href=/?f=repo:any:acme/widgets`) showed 12
     rows.
   - Browser Back returned to the repo page, and Forward went to the
     filtered list again.
3. **← repos** went to `/repos`, and Back reopened the repo page.

### 3. Filing a task from the repo page

1. The repo page's **New task** opened `/new` with `select[name=repo]`
   preselected to `acme/widgets`.
2. **Base branch**:
   - Focusing the field offered `main`, `candidate` and `release/3.0`
     (stubbed GitHub listing).
   - Typing `rel` narrowed the list to `release/3.0`, which was then picked.
3. **Read-only repos**: `zeta` offered `zeta/alpha-tools`, and Enter took it.
4. **Writes** ("Another repo this task may change"): `acme/gad` offered
   `acme/gadgets` and `add acme/gad`, and Enter took `acme/gadgets`.
5. **Create task** returned to the repo page, which then read "13 tasks
   filed here", including after a reload.
6. `GET /api/tasks` showed task 13 stored with:
   - repo `acme/widgets` and base `release/3.0`;
   - reads `["zeta/alpha-tools"]` and writes `[{repo:"acme/gadgets"}]`;
   - state `queued`;
   - capabilities `gcp-key, gemini-key, self-debug`.

   Its detail page opened at `/tasks/13`.
7. **The branch listing boundary**, on new forms targeting `acme/gadgets`:
   - empty `[]`: no menu and no error;
   - delayed 3 s: no menu at 1 s, then `main` once the response arrived;
   - 502: no menu and no banner, and the field stays a free-text box. The
     502 shows up only as a console resource error.

   This matches `BranchField`'s documented "no suggestions rather than an
   error" design.
8. The repo `<select>` also lists `grain/config-default`. That is the
   default team's own state repository, which the demo registers, and it
   is not on the Repos page.

### 4. Repo settings, edited and reloaded

The table lists each setting and the request it sent. Every value read back
the same after a full reload.

| Setting | Request | After reload |
|---|---|---|
| Add branch `walk/topic-1003` | `POST …/branches` → 201 | listed as `walk/topic-1003 -- pending` |
| Prompt extension | `PUT …/prompt-extension` on blur | value kept |
| Setup command | `PUT …/setup-command` on blur | value kept |
| CI capabilities: added github sandbox, then gcp key, then registry push | `PUT …/ci-capabilities {"ciCapabilities":[…]}` | chips kept |
| Default capabilities: added memory and gemini proxy | `PUT …/capabilities {"defaultCapabilities":[…]}` | chips kept; "A task filed against acme/widgets starts with: Self debug, GCP key, Gemini key, Memory, Gemini proxy" |
| Declarations ("Read acme/widgets's own declarations"), turned on, then off | `PUT …/declarations {"declarations":true/false}` | checkbox kept |
| Merge queue "Require branches to be up to date", turned on, then off | `PUT …/require-branch-up-to-date` | checkbox kept |

- A text-field save followed by a capability pick in the same visit lost
  nothing: three PUTs, each answered 200 with the whole updated config, and
  every value was still there after reload.
- Each CI capability option renders with `aria-selected`. Each default
  capability option says when it is "already a deployment default -- on
  here either way".
- The settings above are what this workflow covers, but the repo page holds
  more, and a scripted catalogue of its controls found these as well:
  - compare base/head (`BranchField`s);
  - CI mode radios, each named for the repo;
  - the default-branch watch checkbox;
  - the unfixable-check box, whose **add** stays disabled until something
    is typed;
  - work-ownership buttons, each carrying `aria-pressed`;
  - release workflows, with **run release by hand**;
  - post-merge dispatched jobs.
- The team-scoped repo configuration
  (`/api/teams/{team}/repos/{owner}/{name}/config`) is only drawn while a
  single team is selected. This walkthrough ran with "all teams" selected,
  so that layer was not edited here.

### 5. Memory

1. Opened **Team settings** from the rail (Enter on the gear), which went to
   `/teams/default/settings`, then the **Models** tab.
2. **Deployment memory** ("Memory carried by every run") held the seeded
   `npm test` / `make fmt` note. **Repo memory** ("Memory about
   acme/widgets") held the `make frontend` note.
3. **Documents**:
   - The row buttons are named "Open release-checklist (this deployment)"
     and "Open flaky-e2e (acme/widgets)".
   - Opening each turned its button into "Close …" with
     `aria-expanded=true`, and showed its Summary and Document fields.
4. **Correcting the repo memory**: appended "Walkthrough 10-03: run
   `make e2e` with -j1 on 2-cpu sandboxes." and pressed that scope's
   **save** (`PUT /api/memory`).
5. **Correcting a document**: appended "- 10-03: retry budget is 0; a red
   is real." to `flaky-e2e` and pressed **Save document**
   (`PUT /api/memory/documents`).
6. **Read back**: reloaded and reopened the Models tab. The repo memory
   ended with the note, and reopening `flaky-e2e` showed the correction.
7. **Restored**: both were put back through the same UI. Reopening showed
   the original text exactly, and the document was 208 b again.
8. **Delete confirmation**: **Delete release-checklist (this deployment)**
   asks `confirm('Delete the memory document "release-checklist"? Nothing
   else keeps a copy of it.')`. Dismissing it sent no request and kept the
   row.

### 6. Releases

1. **Opening it**: Enter on the repo page's **releases** button opened
   `/repos/acme/widgets/releases`. The pane showed the "no releases yet"
   notice and an empty qualification plan.
2. **Validation**:
   - An empty name is blocked by the browser ("Please fill out this
     field.").
   - `has space` and `../x` came back 400 "invalid release name".
   - A duplicate `ok-1003` came back 400 "this repo already has an
     unmerged release with this name".
3. **Create**:
   - `ok-1003` and `r2-1003` were both created (201). The field cleared,
     and `r2-1003 -- Provisioning` was selected, showing
     `r2-1003.rc.1 -- cutting`.
   - Cut, promote and merge are not offered until the release is active.
4. **States from the helper**:
   - After it marked `ok-1003` provisioned and its rc.1 cut, the picker
     read `r2-1003 -- provisioning` / `ok-1003 -- active`.
   - Picking `ok-1003` showed `ok-1003.rc.1 -- active` with its history.
5. **Qualification plan**: **add item** opens a Template picker (choose a
   template / nightly-maintenance / security-audit / release-qualification)
   and a Depends on picker ("no other items yet"). Picked
   `nightly-maintenance` and saved the plan.
6. **Promote**: **promote current rc** asks `confirm("Promote ok-1003.rc.1
   to ok-1003? This moves acme/widgets's ok-1003 branch to
   ok-1003.rc.1.")`.
   - Dismissed: no request.
   - Accepted: the candidate became `promoting`, and **cut new rc** stayed
     disabled while it was.
7. **Helper limit**: the helper has no "mark promoted" command, and its
   `mark-candidate-cut` set rc.1 back to `active`. So the rc.2 cut and the
   qualification task links were not reached on this run. The 2026-10-02
   run covered them with an ad-hoc helper.
8. **Merge back**: **merge ok-1003 into the default branch** asks
   `confirm("Merge ok-1003 into acme/widgets's default branch? This opens a
   pull request on GitHub from ok-1003.")`.
   - Dismissed: no request.
   - Accepted: `POST /api/repos/acme/widgets/releases/ok-1003/merge`. With
     no reconciler, the release stays `active`.
9. **Deep links**:
   - `/repos/zeta/alpha-tools/releases` opens that repo's empty pane cold.
   - `/repos/nobody/missing/releases` explains that the repo is not on the
     target list, names the repos that are, and offers no create form.
   - `/repos/acme/widgets/releases/ok-1003` is rewritten to
     `/repos/acme/widgets/releases`, and selects the newest release
     (`r2-1003`) rather than `ok-1003`. This is still as noted on
     2026-10-02; see the observations.
10. **Release trains**: there is still no release-train page.
    `/repos/acme/widgets/trains` falls back to the repo page, with no error.
    The train is the repo page's **release workflows** section (**turn off
    automatic releases**, **run release by hand**). The demo daemon runs
    with `-ci=false`, and the page says so.

### Console and network

No page errors anywhere. The console errors were all one of these:

- the resource errors for deliberate 400s (bad repo name, invalid or
  duplicate release names);
- the stubbed 502 branch listing;
- `GET /api/tasks net::ERR_ABORTED`, which is the three-second poll cut off
  by navigation or reload.

## Fix in this change

**After a keyboard (or click) reorder, focus fell to `<body>`.**

- **Reproduction**:
  1. On `/repos` in Custom order, focus `Move zeta/alpha-tools` and press
     Enter.
  2. Focus "Move here, above acme/gadgets" and press Enter.
  3. Evaluate `document.activeElement`: it is `BODY`. The next Tab starts
     from the top of the page, and a keyboard user loses the row they just
     moved.
- **Cause**: the "Move here" slot disappears in the same render that moves
  the row. A focused element leaving the document leaves focus on `<body>`.
  The same thing happened after Escape cancelled from a slot.
- **Scope**: every list built on `ReorderPickup` has this: repos,
  schedules, templates, suites, boards, the backlog and board columns.
- **Fix** (`ui/src/components/ReorderPickup.jsx`, `MoveHandle`):
  - When a row is put down and focus has been lost to `<body>`, the moved
    row's handle takes focus, with `preventScroll`.
  - Focus that went somewhere real (a field, another handle) is left alone.
- **Regression coverage**:
  - Two `ReorderPickup.test.jsx` cases. The focus-return case fails without
    the fix.
  - A keyboard pick-up/put-down step in
    `e2e/repository_release_memory_walkthrough.spec.js`, asserting that the
    handle is focused afterwards.
- **Verified in the browser** on a rebuilt server with a fresh store:
  - after the keyboard move, focus was on `Move zeta/alpha-tools`, and Tab
    went on to the `zeta/alpha-tools` row link;
  - after Escape from a slot, focus was on `Move acme/gadgets`.
- **Tests run**:
  - `vitest` for ReorderPickup, RepoList, TaskList, TaskBoard, BoardList,
    PanelBoard and ListPrimitives: 311 passed;
  - Playwright Chromium for
    `e2e/repository_release_memory_walkthrough.spec.js`: 2 passed.

## Observations (not filed)

- **Duplicate branch rows**: adding the same branch name repeatedly records
  one `pending` row per press, and the repo page lists all of them. Three
  identical `walk/topic-1003 -- pending` rows were seen. This is deliberate
  (`model.Store.CreateBranch`: "asking twice is how a row whose reconcile
  failed gets retried"), but refusing a name that already has a *pending*
  row would lose nothing.
- **Errors outlive a success**: the global error banner lingers for its 5 s
  timeout after a later success. A failed create followed by a successful
  one within 5 s still shows the old "invalid release name" under the newly
  created release. This is app-wide toast behaviour, not specific to
  releases.
- **Release deep link** (unchanged from 2026-10-02):
  `/repos/acme/widgets/releases/<name>` still doesn't select that release.
- **Unlabelled buttons**: the scope memory **save** / **revert** buttons, and
  each open document's **Save document**, still carry bare names. With two
  documents open there are two identical "Save document" buttons.
- **Helper gaps**: `ui/e2e/releasehelper` has no `mark-candidate-promoted`
  or `mark-release-merged`, and it acts only on the oldest unmerged release.
  So a walkthrough using it cannot reach rc.2, qualification task links or
  the merged state without an ad-hoc helper.
