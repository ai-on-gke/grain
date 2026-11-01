# 2026-11-01 Repository, Merge Queue, and Release UI Walkthrough

A walkthrough of the repository-centric operator workflow. It was driven
through visible controls in a real headless Chromium, against real
`grain demo` servers over throwaway embedded stores. It follows
[2026-10-30](2026-10-30-repository-release-memory-ui.md) and records
what has changed since, and the one fault found and fixed this time.

## Environment

- **Commit tested**: `96f09c68` (`main`, merge of #337) for the
  walkthrough. The fix is on `grain/049f1e09/task-2235`.
- **Platform**: Linux sandbox, Go and Node from the image (Node
  v20.19.0), and Playwright's Chromium from `/var/cache/playwright`
  (`PLAYWRIGHT_BROWSERS_PATH`).
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain` (the frontend is embedded).
- **Servers**: all three sign in with an access code written to
  `/var/tmp/code` and posted to `/api/auth/access-code` for the session
  cookie.
  - **A**, the main walkthrough, on `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file /var/tmp/code -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B**, base-branch gate warnings and the "before" screenshots, on
    `:8434`:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file /var/tmp/code -github-sim -reconcile=500ms`.
    A's `-base-branch-ci` enrols acme/widgets's base branch, so A can
    never raise the gate.
  - **C**, the fixed build, on `:8435`: as B, from the rebuilt binary.
  - `grain demo` does not take `-sandbox-port-base`. That flag belongs
    to `grain daemon`, and passing it to the demo makes it exit at
    once.
- **Driver**: ad hoc Node scripts on `playwright`'s `chromium`, kept
  outside the repository. Every page was watched for `console`
  errors and warnings, `pageerror`, `requestfailed`, HTTP responses
  `>= 400` and `dialog`s, and every non-GET request was logged.
- **Viewports**: 1280x800 or 1280x844 for desktop, 390x844 for phone.
  The screenshot tool used 1440x900 and 390x844.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so `LOOK=1` was not available and nothing Gemini-backed ran.
  The file reader here cannot render PNGs either. Layout was checked
  with bounding boxes and scroll widths measured in the browser.

## Stubs

None. Grain's API and React UI were never intercepted. The only
external boundary, GitHub, is `grain demo`'s own `-github-sim`. It
serves branches, comparisons, pull requests and release branch moves
over a real bare repository on disk. One branch, `feature/walk-2235` (a
new `WALK.md` and `main.go`), was pushed with `git push` to the
simulator's `/tmp/grain-demo-*/githubsim/acme/widgets.git`, so the
compare page had a real diff to show. The demo's own seeds
(`-merge-queue-states`, `-qualification`, `-base-branch-ci`) supplied
the merge batch, release `1.0` and its qualification task. Release state
and train execution are grain's own and ran for real against the
simulator, so there was no external boundary left to stub for loading,
empty or failure responses. The empty and failure states were reached
through real inputs instead: unknown refs, unknown releases, invalid
names and invalid workflows.

## Walkthrough

### 1. Repos list

- **Rail and URL**: the rail's `Repos` entry (a button) and a direct
  load of `/repos` both open the page. It shows the add-repo picker, the
  "No repositories discovered" caption and the one-repo allowlist
  notice. `acme/widgets` is listed with state chips and `16 tasks`.
- **Adding**: added `acme/gadgets` and an 82-character
  `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`
  through **Repo to add** and **Add repo** (`POST /api/repos` each).
  The rail read `Repos 3` after a reload, and the allowlist notice went
  away.
- **Search**: `GADG` leaves only `acme/gadgets`. `zzz-no-match` shows
  "No repos match your search." Clearing restores all three. The filter
  box still has no `aria-label`. Its accessible name comes from the
  placeholder "Filter added repos…".
- **Sort** (`repo-sort-label` select): `Custom order`, `Name (A–Z)`,
  `Active first` and `Most tasks` each reorder as named. The three
  **Move …** handles exist only under `Custom order`.
- **Reorder**:
  - Keyboard: on **Move acme/gadgets**, Enter offers "Move here, above
    organization-…" and "Move here, to the end of the list". Escape
    leaves no targets behind. Choosing the end target moved the row
    there, and focus returned to the handle.
  - Tap: **Move organization-…**, then "Move here, above acme/widgets".
  - Pointer: an HTML5 `dragTo` from the third row onto the first moved
    the row.
  - The order survived a reload. A fresh browser context shows the
    default order: the custom order belongs to this browser.
- **Long name**: at 1280 the name is 860px wide with
  `text-overflow: ellipsis`, and no row is wider than its box. At 390 it
  is cut to 144px (602px of text), with the full name in `title`, and
  the document is 390 wide.
- **Finding 1** (fixed below): acme/widgets's red `Blocked 1` chip
  measured 0px wide at both 1280 and 390. At 390 only `Proposed 3`
  showed.
- **Keyboard order**: Tab goes from the rail to the add form, the
  filter and the sort select, then to the alerts, System and Settings
  header controls, then to the discovery caption's link, then into the
  rows (handle, then name, for each).

### 2. Repo row, task count, Back

- The `acme/widgets` row opens `/repos/acme/widgets`. **← Repos**
  returns to `/repos`. Back and Forward step between the two.
- **16 tasks filed here** is a link to `/?f=repo:any:acme/widgets`,
  which shows 16 rows. Back returns to the repo page.

### 3. Filing a task from the repo page

- On `/repos/acme/gadgets` (`0 tasks filed here`), **New task** opened
  the `/new` dialog over the repo page, with **Repo** set to
  `acme/gadgets`.
- Typed a title and a description, then reloaded `/new`. The title,
  description and repo survived. **Create Task** filed task 24 with
  `repo: "acme/gadgets"`, and the page then read `1 task filed here`.
  After that reload, Create went to `/`, as on 2026-10-30.
- A second task, filed without a reload, returned to
  `/repos/acme/gadgets` (`2 tasks filed here`). `GET /api/tasks` lists
  tasks 24 and 25 on `acme/gadgets`, both queued.

### 4. Repo tabs and Settings → Repositories

- The strip is a `role="tablist"` (`acme/widgets sections`). Each of
  Overview, CI, Merge queue, Agents, Branches, Releases and Advanced
  opens at `/repos/acme/widgets` or
  `/repos/acme/widgets/settings/<tab>`, and stays selected after a
  reload.
- Keyboard: ArrowLeft wraps from Overview to Advanced, Home and End go
  to the ends, ArrowRight wraps back, and Enter opens the focused tab
  (`settings/ci`).
- Tabs replace the history entry. After `/repos`, then each of the seven
  tabs, one Back leaves the page for `/repos`, and Forward returns to
  Advanced.
- `/settings/bogus` falls back to Overview.
- **Settings → Repositories** has the columns repository, base branch,
  declarations, CI mode and git connection:
  - `acme/gadgets`: `Default branch`, `Not read`, `GitHub CI`,
    `Not listed`.
  - `acme/widgets`: `main`, `Read`, `Grain CI`, `Not listed`.
  - The long-named repo: as acme/gadgets.
  - Each cell links to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` or `/settings/git`. The CI link
    opened `settings/ci`, and Back returned to Settings.
  - The per-repo **Merge queue** links have moved since 2026-10-30.
    They are no longer on the table rows. They now sit in the labels of
    the per-repo "base-branch repair human-gate bypass" switches below
    the table.
- **Edits**, each confirmed by reloading and reading the field back:
  - **Agents**:
    - Default capability **Read issues**. The summary read "A task
      filed against acme/widgets starts with: Self debug, GCP key,
      Gemini key, Read issues" (`PUT .../capabilities`). **Add grant…**
      lists `cloud-key` (needs arguments), `cloud-viewer` and `gcp-key`.
    - Prompt extension "Run make lint before pushing (task 2235)."
    - Setup command `make deps-2235`.
  - **CI**:
    - CI capability **package publish** (`PUT .../ci-capabilities`)
      survived a reload.
    - **CI workflow** `lint2235.yml`:
      - `name: [unclosed` is refused with 400. The YAML error ("did not
        find expected ',' or ']'") is shown as an alert, and the editor
        keeps the text.
      - `runs-on: grain` is refused with "runs-on must be
        "ubuntu-latest" in the first GitHub Actions compatibility
        release".
      - The corrected workflow saved as `.grain/workflows/lint2235.yml`
        and survived a reload.
      - The refused `PUT` went out once per blur this time, not twice
        as on 2026-10-30.
      - Observation: after a refused save, a valid one succeeds but the
        refusal's alert stays up for another 1.5–4s, until it times out.
        For that time the page says the valid workflow was refused
        (`showError` is never cleared by a later success).
  - **Advanced**: "Read at 4567e69efbb0: 0 template(s), 0 suite(s), 0
    schedule(s), 0 trigger(s)", with **Remove repository**.
  - As on 2026-10-30, this build has no branch-rule (base/head) editor,
    no repo-scoped read/write repository editor and no custom
    configuration on the repo page. Branches has **Add branch** and
    **Compare branches**. Base branches are read from the release
    configuration. That part of the brief does not match this build.
- **Unsaved edits**: with the refused workflow on screen, **← Repos**
  asked "You have unsaved changes here. Leave and discard them?".
  Dismissing it kept the page on `settings/ci`.

### 5. Merge queue and base-branch gates

- **Switches on A**: **Require branches to be up to date**, **Merge pull
  requests … when green and clean** and **Base-branch repair PRs … may
  merge without a human** each saved with one click (`PUT
  .../require-branch-up-to-date`, `.../merge-on-green`,
  `.../base-repair-bypasses-human-gates`). `license/cla` was added under
  "Checks no commit here can clear". After a reload all three were on
  and `license/cla` was listed. The Overview cards read `Required`, `On`
  and `On`. The base-repair bypass still takes effect with no
  confirmation, as on 2026-10-29 and 2026-10-30.
- **Merge batch**: the Overview reads "1 change on main", testing, "Nm
  of 90m", `grain-merge-queue/ece7fedb/main at abcdef012345`, and links
  task 5 ("Rotate the Gemini signing key") and PR `#101`. `/tasks/5`'s
  dialog has the same "merge batch" panel, with Pause, Force merge and
  Run now. Back from `/tasks/5` returns to the repo page.
- **Gates on B**:
  - CI mode **GitHub** plus **Watch acme/widgets's default branch with
    grain CI…** raised "Nothing is testing acme/widgets's default
    branch, and the merge queue is merging onto it anyway. grain CI is
    not enabled for this repository…". It appears on the CI tab, on the
    Overview (card: "GitHub CI — base branch not judged"), with a red dot
    on the CI tab, and on `/repos/acme/widgets/releases`.
  - The Releases pane's **Go to CI Mode** opens `settings/ci` with the
    grain CI radio in the viewport. This is the fix from 2026-10-30,
    holding.
  - Choosing **Use grain CI** cleared the warning and the dot. Back to
    the Releases pane showed no warning, without a reload.
  - **No CI** with the watch on raised the warning again ("No CI — base
    branch not judged"). Grain cleared it again.

### 6. Branches and compare

- Base starts at `main`. Typing `feat`, ArrowDown and Enter picked
  `feature/walk-2235` from the simulator's branches. **Compare** opened
  `/repos/acme/widgets/compare/main...feature/walk-2235`, which is
  `open`, "Into main on acme/widgets -- forked at 4567e69e". It has
  side-by-side, inline and raw views, and lists `WALK.md` (+1) and
  `main.go` (+3).
- It survived a reload. The document is 1280 wide at desktop and 390 at
  phone. Back returned to the Branches tab.
- Direct links:
  - `main...main` and `release/1.0...main` say "There are no changes
    between these branches."
  - `main...no-such-branch` and `nope...main` return 404, and the page
    says "GitHub has nothing to compare for … one of the two branches
    does not exist, the two share no history, or this deployment's
    credential cannot see acme/widgets".
- An unknown head (`missing-branch-xyz`) warns `No branch
  "missing-branch-xyz" on acme/widgets: there is nothing to compare`.
  **Compare** stays enabled.

### 7. Releases

- `/repos/acme/widgets/releases` shows `1.0 -- Active`, `latest:
  1.0.latest, prod: 1.0` and candidate `1.0.rc.1 -- active`. It also
  shows the pending-approval banner with **Approve all**, the
  qualification task **Qualify** (`/tasks/23`), history, merge-back,
  **Delete release** and the qualification plan editor.
- **Cut new RC** is disabled, with "Cut new RC: 1.0.rc.1 is active: a
  new RC is cut once it is promoted." **Promote current RC** is enabled.
- **Validation**:
  - `bad release name` and `x.latest` are refused (400) with the
    naming rules.
  - `1.0` is refused with "this repo already has an unmerged release
    with this name".
- **Lifecycle of `wt2235`**:
  - Created at `/releases/wt2235`. It went active as `wt2235.rc.1`.
  - **Promote current RC**: the confirm was dismissed (no request),
    then accepted.
  - While the candidate was promoting, both buttons were greyed out
    with reasons: "Cut new RC: wt2235.rc.1 is promoting: a new RC is cut
    once it is promoted." and "Promote current RC: … only an active
    candidate can be promoted."
  - Once promoted: "Promote current RC: wt2235.rc.1 is already promoted:
    a candidate is promoted once." Cut was enabled.
  - **Cut new RC** gave `wt2235.rc.2 -- cutting` ("Cut new RC:
    wt2235.rc.2 is still being cut."), then active.
  - After a reload the history read `wt2235.rc.2 -- active`,
    `wt2235.rc.1 -- promoted`.
  - `/releases/1.0` opens directly, and Back returns to `wt2235`.
  - `/releases/nope-404` is rewritten to `/releases` with `no release
    "nope-404"`.
  - **Delete release**: the confirm was dismissed (no request), then
    accepted. The pane fell back to `/releases` with no false error.

### 8. Console and network

- No `pageerror`s. `console.error` appeared only for the deliberate
  400 and 404 responses above.
- `GET /api/tasks` shows up as `requestfailed: net::ERR_ABORTED` on
  navigation, as on earlier runs. These are polls the browser drops on
  unload, and nothing shows to the user.

## Findings

### 1. Fixed: a busy repo's Blocked chip was hidden at every width

- **Reproduction** (any `grain demo`):
  1. Open `/repos` at 1280x800.
  2. On the `acme/widgets` row, the strip shows Proposed, Waiting,
     Running, Asked, Merging, Deferred and Closed. In the DOM the eighth
     chip, `Blocked 1` (error colour), has `chip-overflow` and a 0px
     box.
  3. At 390x844 only `Proposed N` is shown, and Blocked is hidden too.
     (At 1440 it just fits.)
- **Cause**: the row's `.chips` strip is clipped to one line, and
  `ChipStrip` hides whichever chips wrapped, starting from the last.
  `RepoList` rendered Blocked after every per-state count, so it was
  always the first to go. Its own comment says the counts are there so
  "a repo with something stuck (… a pile of blocked work) stands out
  before anyone opens it".
- **Fix**: Blocked leads the strip. Measured on server C: at 1440 every
  chip shows with Blocked first. At 390 Blocked (72px) is the one chip
  shown.
- **Regression coverage**:
  - `RepoList.test.jsx`, "puts the Blocked chip ahead of the state
    counts".
  - `e2e/repo-list-blocked-chip.spec.js`, at 1280 and 390, against the
    real demo. It asserts with bounding boxes that the chip is visible
    and inside the acme/widgets row, and that the document is no wider
    than the viewport.
  - Both tests fail with the old order and pass with the fix.

### Observations not changed here

- The repo filter box is labelled only by its placeholder (§1). The box
  is shared by every list, so a fix belongs app-wide.
- **Base-branch repair PRs may merge without a human** still has no
  confirmation (§5). The risk is spelled out next to the switch.
- A refused workflow's alert stays up for seconds after the corrected
  workflow saves (§4).
- **Compare** stays enabled under the "No branch" warning (§6). The
  compare page then explains the 404, so nothing is lost.
- A reload of `/new` forgets the repo page it was opened from (§3).
- The brief's branch rules, repo-scoped read/write relationships and
  custom configuration are not on the repo page in this build (§4).

## Tests run

- `npx vitest run` (all of `ui/`): 229 files, 3586 tests passed.
- `npx playwright test e2e/repo-list-blocked-chip.spec.js e2e/repo-pane.spec.js e2e/repo-tabs-walkthrough.spec.js e2e/base-branch-gate-warnings-walkthrough.spec.js`:
  9 passed.
- The new tests were also run against the unfixed `RepoList.jsx`: the
  unit test failed, and both e2e cases failed at `toBeVisible`.
- `npx prettier --write` on the changed files.
- Rendered with `node e2e/screenshot.mjs --url …` (the
  `make ui-screenshots` tool):
  - Page: `/repos`.
  - Widths and themes: desktop (1440) and phone (390), light and dark.
  - Builds: before on B, after on C.
  - Chip widths for the acme/widgets row, measured in the browser at
    1440 and 390 on both servers, are under Finding 1.
  - Screenshots are not committed.
