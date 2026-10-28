# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-04
- **Task**: grain/task-1815
- **Status**: Passed, with two defects fixed in this change (the release
  deep link and the unnamed memory Save/Revert buttons) and one
  test-harness gap closed (the release helper). One follow-up was proposed.
  Observations are listed at the end.

## Environment

- **Commit walked through**: `f56fe0d45` (`candidate` tip at the start of
  the run). The footer stamp read
  `v1.7.0-rc.1 · f56fe0d · 2026-10-04 01:07Z`.
- **Fixes**: checked on this branch. The frontend and server were rebuilt,
  and the check ran against a second demo over a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64, 4 vCPU, 8 GB. Go 1.26.6,
  Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34, driven directly by
  the Playwright library from ad-hoc scripts outside the repository (not
  jsdom). The viewport was 1400×950, with spot checks at 390×844. Every
  script logged:
  - console errors and warnings;
  - page errors;
  - failed requests;
  - every response of 400 or above;
  - every mutating request, with its body;
  - every `confirm()` dialog, which the script either accepted or
    dismissed.
- **Server**: the real `grain demo`.
  - Built by running `npm ci && npm run build` (frontend into
    `pkg/ui/static`), then `go build -o /tmp/grain ./cmd/grain`.
  - Started as
    `TMPDIR=/tmp/run1 grain demo -addr 127.0.0.1:8420 -open=false` over a
    fresh embedded SQLite store.
  - The fix was verified on `127.0.0.1:8421`, under `TMPDIR=/tmp/run2`.
- **Account**: `operator`, created through the first-run **create account**
  dialog (`POST /api/auth/register`). On the second server the account
  was recreated and later used through **Log in**.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so no Gemini-backed tooling was used.
- **Setup**: Chromium was installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium`.
  No screenshots or traces are committed.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo has no GitHub (it answers 404) | `page.route` on this path only | success `["main","candidate","release/3.0"]`; empty `[]`; a 3 s delayed success; 502 `{"error":"GitHub: bad gateway"}` |
| GitHub compare behind `GET /api/repos/*/*/compare?base=&head=` | The demo answers 404 "repository comparison is not available on this UI server". That real answer was exercised first, with no stub. | `page.route` on this path only, using `ui.BranchComparison`'s own JSON shape | open diff with a merge base; merged (`merged:true`, empty diff); 3 s delayed; 502 `{"error":"GitHub: compare failed (502 Bad Gateway)"}` |
| GitHub release reconciler (release provisioned, candidate cut/promoted, release merged/error, qualification sync) | No reconciler runs in the demo | The committed `ui/e2e/releasehelper`, extended in this change, built to `/tmp/releasehelper` and run as `-port <port> [-release <name>] <cmd>`. It calls the `model.Store` transitions and `orchestrator.SyncQualifications` on the demo's own SQLite store. | provisioned; rc.1 cut; rc.1 promoted; rc.2 cut; qualification synced; merged; error |

Grain's own API, the React UI and every user gesture were real everywhere
else. Read-backs used plain `fetch` from the page against the same
server: `GET /api/tasks`, `GET /api/memory` and
`GET /api/teams/default/repos/acme/widgets/config`.

## Steps and outcomes

### 1. Repos list

1. **Opening it**:
   - Focused the rail's **repos** entry (`div role=button tabindex=0`) and
     pressed **Enter**. The router went to `/repos`.
   - A cold `page.goto("/repos")` drew the same page.
2. **Counts**: the header badge read `1`. The `acme/widgets` row showed
   the state chips and `12 tasks`.
3. **Add repo** (the inline `owner/name` box):
   - Added `acme/gadgets`,
     `example-corporation/exceptionally-long-repository-name-for-responsive-layout-checks-1004`
     and `zeta/alpha-tools`. The badge went 2 → 3 → 4.
   - Re-adding `acme/widgets`, and adding `Acme/Widgets`, was a silent
     no-op: no error and no second row.
   - `bad//name` was refused inline with
     `repo: repo must be owner/name, got "bad//name"` (HTTP 400).
4. **Long names**: no row overflowed at 1400 px (`scrollWidth ==
   clientWidth` on every row) or at 390 px. The document never scrolled
   sideways.
5. **Search**: `ALPHA` showed only `zeta/alpha-tools` and the badge
   followed to `1`. `qqq` showed "No repos match your search." with a
   badge of `0`.
6. **Sort** (an MUI combobox that opens with Enter): its options are
   custom order, name (a–z), active first and most tasks.
   - Each sort except custom order showed 0 handles.
   - Custom order showed 4 `Move …` handles.
7. **Keyboard reorder**:
   - Enter on `Move zeta/alpha-tools` showed the "Moving 1 repo — choose
     where it goes" banner. Its slots were labelled "Move here, above
     acme/gadgets / acme/widgets / example-corporation/…".
   - Focus moved to "Stop moving zeta/alpha-tools".
   - Enter on the first slot moved the row to the top, and focus returned
     to `Move zeta/alpha-tools`, so the 10-03 fix holds.
   - Escape from a pick-up left focus on that row's handle.
8. **Pointer drag**: dragged the `acme/gadgets` handle onto the first row,
   which gave gadgets, zeta, widgets, long.
9. **Click/tap pickup**: clicked `Move acme/widgets`, then "Move here, to
   the end of the list". The row moved to the end, and focus came back to
   its handle.
10. **Reload**: the order and "Custom order" survived, held in
    `localStorage["grain.list-order.repos"]`.

### 2. Repo page and navigation

1. Clicked the `acme/widgets` name, which opened `/repos/acme/widgets`.
2. **The task-count link**:
   - **12 tasks filed here** (`href=/?f=repo:any:acme/widgets`) showed 12
     rows under "filters (1)".
   - Browser Back returned to the repo page, and Forward returned to the
     filtered list.

### 3. Filing a task from the repo page

1. **The form**: the repo page's **new task** opened the New task form
   (URL `/new`) with `select[name=repo]` preselected to `acme/widgets`.
2. **Base branch** (stubbed listing):
   - Focusing it offered `main, candidate, release/3.0`.
   - Typing `rel` narrowed the list to `release/3.0`, and Enter took it
     without submitting the form.
3. **Read-only repos**: `zeta` offered `zeta/alpha-tools`, and it was
   taken.
4. **Writes**: `acme/gad` offered `acme/gadgets` and `add acme/gad`.
   - ArrowDown then Enter takes the *second* entry, because the first is
     already highlighted. That added `acme/gad`.
   - **Create task** was refused with 400 "acme/gad is not on this
     deployment's target repo list, so it cannot be listed as a repo to
     write to … The list currently holds …", shown inline. The form and
     its draft were kept.
   - Removed it with **Remove acme/gad** and created again: 201, task 13.
5. **Read-back**:
   - The page returned to the repo page, which read "13 tasks filed
     here", including after a reload.
   - `GET /api/tasks` had task 13 with repo `acme/widgets`, base
     `release/3.0`, reads `["zeta/alpha-tools"]`, writes
     `[{repo:"acme/gadgets"}]`, state `queued`, capabilities `gcp-key,
     gemini-key, self-debug` and autoMerge `true`.
   - `/tasks/13` opened it, and the draft key was cleared.
6. **The branch listing boundary**, on new forms for three repos:
   - empty: no menu and no error;
   - delayed 3 s: no menu at 1 s, then `main` at 3.6 s;
   - 502: no menu and no banner, and the field stays a free-text box. The
     502 shows up only as a console resource error.

   This matches `BranchField`'s documented "no suggestions rather than an
   error" design. Escape closed the form without asking, back to the repo
   page.

### 4. Repo settings, edited and reloaded

| Setting | Request | After reload |
|---|---|---|
| Add branch `walk/topic-1004`, twice | `POST …/branches` → 201 ×2 | two `walk/topic-1004 -- pending` rows (see follow-up) |
| Add branch `bad branch name`, `../escape` | `POST …/branches` → 400 each | not listed |
| CI capabilities: added github sandbox, then registry push | `PUT …/ci-capabilities` | chips kept |
| Default capabilities: added memory | `PUT …/capabilities {"defaultCapabilities":["memory"]}` | "A task filed against acme/widgets starts with: Self debug, GCP key, Gemini key, Memory" |
| Prompt extension (two lines) | `PUT …/prompt-extension` on blur | value kept |
| Setup command | `PUT …/setup-command` on blur | value kept |
| Declarations, turned on | `PUT …/declarations {"declarations":true}` | checked |
| Merge queue "Require branches to be up to date", turned on | `PUT …/require-branch-up-to-date` | checked |
| CI mode: grain CI | `PUT …/ci-mode {"ciMode":"grain"}` | selected |
| Check that cannot pass: `cla/google` | `PUT …/unfixable-checks` | listed |
| Jobs dispatched after every merge: `cache-warm, docs` | `PUT …/release {"postMergeDispatchJobs":[…]}` | value kept |
| Work ownership: release off, nightly schedule off | `PUT …/work-ownership` ×2 | `aria-pressed` kept, and rows read "off, set here / not run". Both were then put back to "say nothing". |

- **Compare branches** (base `main`, head `walk/topic`) opened
  `/repos/acme/widgets/compare/main...walk/topic` under each answer:
  - **real demo answer**: "repository comparison is not available on this
    UI server";
  - **stubbed success**: a side-by-side diff of README.md (+2 −1),
    "forked at 0a1b2c3d", which survived a reload;
  - **merged**: "Merged into main … There are no changes between these
    branches.";
  - **delayed**: "Loading the comparison…";
  - **502**: the error text, shown both in the page and in the global
    banner.
- **Remove**: **remove** asked `confirm("Remove acme/widgets from target
  repos? New tasks won't be able to target it until it's added back, and
  it will drop off the repos list. Tasks already filed there are not
  affected.")`.
  - Before asking, it sent `POST /api/settings/plan`, the change preview.
  - Dismissing it changed nothing.
- **Team-scoped configuration** (`/teams/default/repos/acme/widgets`):
  1. The page says the prompt extension and setup command are the team's
     own, and everything else is global.
  2. Set the team prompt extension and added **release notes** to "default
     capabilities for default". Each sent
     `PUT /api/teams/default/repos/acme/widgets/config` with only that
     field.
  3. Both survived a reload, and the global repo page still showed the
     global prompt extension.
  4. Focusing and blurring an *inherited* field wrote nothing.
  5. **Revert to global defaults** asks `confirm("Revert team overrides
     for acme/widgets back to global repository defaults?")`.
     - Dismissed: no request.
     - Accepted: `DELETE …/config`, and the field showed the global value
       again, including after a reload.

### 5. Memory (Team settings › Models)

1. **Opening it**: `/team-settings` resolved to
   `/teams/default/settings`, and the **models** tab went to
   `/teams/default/settings/models`.
2. **The two scopes**:
   - "Memory carried by every run" holds the seeded `make fmt` note,
     written by the memory agent.
   - "Memory about acme/widgets" holds the `make frontend` note.
   - The document rows `release-checklist (this deployment)` and
     `flaky-e2e (acme/widgets)` have named Open/Delete buttons.
3. **Correcting the repo memory**: appended a line and pressed that
   scope's Save (`PUT /api/memory`). The scope then read "written today /
   edited here", and `GET /api/memory` showed `updatedBy:"operator"`.
4. **Correcting a document**: opened `flaky-e2e`, appended "- 10-04: retry
   budget is 0; a red is real.", and saved (`PUT /api/memory/documents`).
5. **Read back**: after a reload, the memory ended with the note and the
   reopened document held the correction.
6. **Validation**: saving the document with an empty summary was refused
   with 400 "a memory document needs a summary: …", shown in the banner.
7. **Restored**: both were put back through the UI. The text and summary
   then matched the originals exactly.
8. **Delete confirmation**: **Delete release-checklist (this deployment)**
   asks `confirm('Delete the memory document "release-checklist"? Nothing
   else keeps a copy of it.')`. Dismissing it sent no request.
9. **Bug, fixed here**: every scope's **Save** and **Revert**, and every
   open document's **Save document**, had a bare name. The pane listed
   `["Revert","Save",…,"Revert","Save",…]`. See *Fixes in this change*.

### 6. Releases

1. **Opening it**: Enter on the repo page's **releases** button opened
   `/repos/acme/widgets/releases`, showing "acme/widgets has no releases
   yet".
2. **Validation**:
   - An empty name is blocked by the browser ("Please fill out this
     field.").
   - `has space` and `../x` came back 400 "invalid release name".
   - A duplicate `ok-1004` came back 400 "this repo already has an
     unmerged release with this name".
3. **Create**:
   - `ok-1004` and `r2-1004` were both created (201).
   - `r2-1004 -- Provisioning` was selected, showing `r2-1004.rc.1 --
     cutting`.
   - Cut and promote were disabled.
4. **Helper: provisioned and cut**. After `-release ok-1004
   mark-release-provisioned` and `mark-candidate-cut`, the picker read
   `r2-1004 -- provisioning / ok-1004 -- active`. Picking `ok-1004` showed
   `ok-1004.rc.1 -- active`.
5. **Qualification plan**:
   - **add item** opened a template picker (nightly-maintenance,
     security-audit, release-qualification).
   - Saving `release-qualification` sent `PUT …/qualification-plan` → 200.
6. **Promote**: **promote current rc** asks `confirm("Promote ok-1004.rc.1
   to ok-1004? This moves acme/widgets's ok-1004 branch to
   ok-1004.rc.1.")`.
   - Dismissed: no request.
   - Accepted: `POST …/candidates/promote` → 200, and the candidate showed
     `promoting`.
7. **rc.2 and qualification**:
   - The helper marked rc.1 promoted. **cut new rc** then sent
     `POST …/candidates` → 201 and showed `ok-1004.rc.2 -- cutting`, with
     rc.1 in the history as promoted.
   - The helper cut rc.2 and synced qualifications. The pane then showed
     "qualification · running · release-qualification".
   - That link is `<a href=/tasks/14>`. It opened task 14, and Back
     returned to the pane.
8. **Merge back**: **merge ok-1004 into the default branch** asks
   `confirm("Merge ok-1004 into acme/widgets's default branch? This opens a
   pull request on GitHub from ok-1004.")`.
   - Dismissed: no request.
   - Accepted: `POST …/releases/ok-1004/merge`.
9. **Helper: merged and error**:
   - After `mark-release-merged` on `ok-1004`, it read `ok-1004 -- Merged`,
     with cut and promote disabled and "Merge requested -- pull request"
     linking the PR.
   - After `mark-release-error` on `r2-1004`, its line read "(Simulated
     GitHub failure creating the release branches)".
10. **Deep links**:
    - `/repos/zeta/alpha-tools/releases` opens that repo's empty pane cold.
    - `/repos/nobody/missing/releases` explains that the repo is not on the
      target list and offers no create form.
    - **Bug, fixed here**: picking a release never reached the address.
      - Reload, Back and a link all reopened the newest release.
      - `/repos/acme/widgets/releases/ok-1004` was rewritten to
        `/releases` and showed `r2-1004`.
      - This had been recorded on 10-02, 10-03 and today.
11. **Release trains**: there is still no release-train page.
    `/repos/acme/widgets/trains` falls back to the repo page, with no
    error.
    - The train is the repo page's **release workflows** section
      (automatic releases on/off, **run release by hand**, dispatched
      jobs).
    - The demo daemon runs with `-ci=false`, and the page says so.
12. **Keyboard**:
    - Tab order through the pane: name box → Create release → Release
      picker → Promote → Merge → plan fields → Add item → Save plan.
      Disabled Cut is skipped.
    - Picking with Enter/ArrowUp/Enter on the picker kept focus on the
      picker.
13. **Phone**: at 390 px neither the release pane nor the long-named repo
    page scrolled sideways.

### Console and network

No page errors anywhere. The console errors were all one of these:

- the resource errors for deliberate 400s;
- the stubbed 502s;
- the demo's real 404 for `github-branches` on the task detail page.

**The recurring `GET /api/tasks net::ERR_ABORTED`** lines are not
navigation aborts, as the 10-03 report put it. The DevTools protocol shows
each one is the three-second poll's conditional request (`If-None-Match`)
answered **304**. Chromium reports a `cache: "no-store"` 304 as
`ERR_ABORTED`, and `api.js` reads it as `UNCHANGED`. This is harmless.

## Fixes in this change

### 1. The selected release is in the address

- **Reproduction** (fresh demo):
  1. Create releases `a` then `b` on `/repos/acme/widgets/releases`.
  2. Pick `a` in the Release picker. The URL stays `…/releases`.
  3. Reload. The pane shows `b`.

  Also: open `/repos/acme/widgets/releases/a` cold. The address is
  rewritten to `…/releases` and `b` is shown.
- **Fix**:
  - `paths.js` parses and builds `/repos/<owner>/<repo>/releases/<name>`.
    The team prefix works, and the name is percent-encoded.
  - `App.jsx` holds `openRelease` beside the other open items. It is
    restored on Back/Forward and cleared on leaving the pane.
  - `RepoReleases.jsx` takes `release` and `onSelectRelease`:
    - it opens on the named release;
    - it reports picks and newly created releases;
    - it follows Back/Forward;
    - with no release named, it shows the newest and leaves the address
      alone.
  - A link naming a missing release shows the newest, and the address is
    corrected with `replaceState` and the note `no release "<name>"`. This
    is the same path a missing task takes.
- **Verified in the browser** on the rebuilt server:
  - creating `ok-1004` and `r2-1004` moved the URL to each;
  - picking `ok-1004` gave `…/releases/ok-1004`, and a reload kept it;
  - Back went to `r2-1004` and Forward to `ok-1004`;
  - a cold deep link opened `ok-1004`;
  - `/releases/nope` gave `…/releases` + `r2-1004` + "no release
    "nope"", and Back skipped the corrected URL;
  - `/teams/default/repos/acme/widgets/releases/ok-1004` worked;
  - a keyboard pick updated the URL.
- **Regression coverage**:
  - `paths.test.js` (parse, build, round trip);
  - four `RepoReleases.test.jsx` cases: named release shown, pick
    reported, missing release reported, prop change followed;
  - reload, Back and missing-release steps added to
    `e2e/repository_release_memory_walkthrough.spec.js`.

  The unit cases fail without the change.

### 2. Memory Save/Revert buttons are named for their scope

- **Reproduction**: on `/teams/default/settings/models`, list the
  buttons' accessible names. There are two "Save", two "Revert", and one
  "Save document" per open document.
- **Fix** (`MemoryPanel.jsx`): the buttons are now named "Save acme/widgets
  memory", "Revert this deployment's memory" and "Save document flaky-e2e
  (acme/widgets)".
  - The visible text is unchanged and is contained in each name.
  - A name never contains its text box's label, so
    `getByLabel("Memory about …")` still finds exactly one element. A
    first version that did contain it broke `e2e/memory.spec.js` in strict
    mode, and was changed.
- **Regression coverage**:
  - a new `MemoryPanel.test.jsx` case asserting the names and that no bare
    "Save" remains;
  - the existing cases, and both e2e specs, now find buttons by name
    instead of by xpath ancestor.

### 3. Release helper (test infrastructure)

`ui/e2e/releasehelper` gains:

- `-release <name>`;
- candidate commands that act on the newest candidate (previously the
  oldest, which is why 10-03's cut reset rc.1);
- `mark-candidate-promoted`, `mark-release-merged` and
  `mark-release-error`.

With these, the rc.2, qualification-link and merged steps above ran
without ad-hoc code.

### Tests run

- `vitest`: `paths`, `RepoReleases`, `MemoryPanel`, `App.repos` and
  `App` passed. The full suite then passed: 170 files, 2903 tests.
- Playwright Chromium for `repository_release_memory_walkthrough`,
  `memory`, `repos_workflows` and `paths`: 22 passed.

## Follow-up proposed

- **Duplicate pending branch rows**: pressing **add branch** twice with
  the same name records two `pending` rows, and the repo page lists both.
  It was seen on 10-03 and again today. Proposed as a separate task
  against `candidate`.

## Observations (not filed)

- **Errors outlive a success**: the global error banner still lingers
  after a later success. Creating `r2-1004` right after the duplicate
  `ok-1004` refusal left "this repo already has an unmerged release with
  this name" under the new release. This is app-wide toast behaviour,
  already noted on 10-03.
- **Duplicated compare error**: a failing compare shows its message twice,
  once in the page and once in the global banner.
- **Status label case**: the picker's labels are lowercase
  (`r2-1004 -- provisioning`), while the selected value reads
  `r2-1004 -- Provisioning`.
- **Write-repo suggestions**: in the "Another repo this task may change"
  menu the first suggestion is pre-highlighted, so ArrowDown+Enter picks
  the *second* one ("add acme/gad"), unlike the branch field. The server
  refuses the result clearly.
- **Error release label**: a release in error keeps its
  `Provisioning`/`Active` label, with the error in parentheses.
