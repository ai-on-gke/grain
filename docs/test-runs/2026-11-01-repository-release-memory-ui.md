# 2026-11-01 Repository, Merge Queue, and Release UI Walkthrough

A walkthrough of the repository-centric operator workflow. It was driven
through visible controls in a real headless Chromium, against real
`grain demo` servers over throwaway embedded stores. It follows
[2026-10-30](2026-10-30-repository-release-memory-ui.md) and records
what has changed since, and the one fault found and fixed this time.

## Environment

- **Commit tested**: `df38c2b5` (`main`, merge of #333) for the
  walkthrough. The fix is on `grain/049f1e09/task-2087`.
- **Platform**: Linux sandbox (`kubernetes-granule`), Go and Node from
  the image (Node v20.19.0), and Playwright's Chromium and WebKit from
  `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH`).
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain` (the frontend is embedded).
- **Servers**: all three sign in through the access-code form, with the
  code written to `/var/tmp/code`.
  - **A**, the main walkthrough, on `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file /var/tmp/code -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B**, base-branch gate warnings, on `:8434`:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file /var/tmp/code -github-sim -reconcile=500ms`.
    A's `-base-branch-ci` enrols acme/widgets's base branch, so A can
    never raise the gate.
  - **C**, the fixed build, on `:8435`: as B, from the rebuilt binary.
- **Driver**: ad hoc Node scripts on `playwright`'s `chromium`, kept
  outside the repository. Every page was watched for `console`
  errors and warnings, `pageerror`, `requestfailed`, HTTP responses
  `>= 400` and `dialog`s, and every non-GET request was logged.
- **Viewports**: 1280x800 for desktop; 360, 390 and 599 for phone; and
  600, 700, 800, 900, 1000, 1280 and 1600 for the header sweep in
  Finding 1.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so `LOOK=1` was not available, and the sandbox's file reader
  shows a PNG or JPEG as bytes. Layout was checked with bounding boxes,
  `elementFromPoint` hit tests and scroll widths measured in the
  browser.

## Stubs

None. Grain's API and React UI were never intercepted. The only
external boundary, GitHub, is `grain demo`'s own `-github-sim`
(`pkg/github/githubsim`). It serves branches, comparisons, pull
requests and release branch moves over a real bare repository on disk.
One branch, `feature/walk-2087` (a new `WALK.md` and `walk.go`), was
pushed with `git push` to the simulator's
`/tmp/grain-demo-*/githubsim/acme/widgets.git`, so the compare page had
a real diff to show. The demo's own seeds (`-merge-queue-states`,
`-qualification`, `-base-branch-ci`) supplied the merge batch, release
`1.0` and its qualification task.

## Walkthrough

### 1. Repos list

- **Rail and URL**: the rail's **Repos** entry (a `role="button"` list
  item, `Repos 1`) and a direct load of `/repos` both open the page. It
  shows the add-repo picker, the "No repositories discovered" caption
  and the one-repo allowlist notice. `acme/widgets` is listed with its
  state chips and `16 tasks`.
- **Adding**: added `acme/gadgets` and the 82-character
  `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`
  through **Repo to add** and **Add repo** (`POST /api/repos`). After a
  reload the rail read `Repos 3`.
- **Search**: `GADG` leaves only `acme/gadgets`. `zzz-no-match` shows
  "No repos match your search." Clearing restores all three. The box
  still has no `aria-label`. Its accessible name is the placeholder, as
  on 2026-10-30.
- **Sort**: the options are `Custom order`, `Name (A–Z)`, `Active first`
  and `Most tasks`. Each reorders as named. The three **Move …**
  handles exist only under `Custom order`.
- **Reorder**:
  - Keyboard: Enter on **Move acme/gadgets** offers two "Move here"
    targets. Choosing the end target moved the row last, and focus
    returned to the handle.
  - Escape after a pick-up leaves no targets.
  - Pointer: an HTML5 `dragTo` moved the last row to the top.
  - The order survived a reload.
- **Long name**: at 1280 and at 390 the document is as wide as the
  viewport. The long name is cut with `text-overflow: ellipsis`, with
  the full name in `title`.

### 2. Repo row, task count, Back

- The `acme/widgets` row opens `/repos/acme/widgets`. **← Repos**
  returns to `/repos`, and Back steps back to the repo page.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows. Back returns to `/repos/acme/widgets`.

### 3. Filing a task from the repo page

- On `/repos/acme/gadgets` (`0 tasks filed here`), **New task** opened
  `/new` with **Repo** preselected as `acme/gadgets`.
- A title and description were typed and `/new` reloaded. Title,
  description and repo all survived. **Create Task** filed task 24
  (`POST /api/tasks` with `"repo":"acme/gadgets"`) and went to `/`. The
  repo page then read `1 task filed here`.
- A second task filed without a reload returned to
  `/repos/acme/gadgets` (`2 tasks filed here`). `GET /api/tasks` lists
  tasks 24 and 25 on `acme/gadgets`.

### 4. Repo tabs and Settings → Repositories

- Each of Overview, CI, Merge queue, Agents, Branches, Releases and
  Advanced opens at `/repos/acme/widgets` or
  `/repos/acme/widgets/settings/<tab>`, and stays selected after a
  reload.
- Tabs replace the history entry. After `/repos`, the repo page,
  **Branches** and then **Releases**, one Back returns to `/repos`, and
  Forward returns to `settings/releases`.
- Keyboard: ArrowLeft on Overview wraps to Advanced, Home goes back to
  Overview, and End then Enter opens `settings/advanced`.
- `/settings/bogus` falls back to Overview. `/repos/no/such-repo`
  renders an empty page for it with `0 tasks filed here`, as before.
- **Settings → Repositories** is listed under "Managed in config repo"
  (#3d26e211). In `grain demo` it is still editable. Its rows:
  - `acme/gadgets`: `Default branch`, `Not read`, `GitHub CI`,
    `Not listed`.
  - `acme/widgets`: `main`, `Read`, `Grain CI`, `Not listed`.
  - The long-named repo: as acme/gadgets.
  - The cells link to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` and `/settings/git`.
- **Edits**, each confirmed by reloading and reading the field back:
  - **Agents**:
    - Default grant `api:task-admin`, picked from **Add grant…**
      (`PUT .../capabilities`, `["api:task-admin"]`). The summary read
      "Self debug, GCP key, Gemini key, API preset: task-admin".
    - Prompt extension "Run make lint before pushing (task 2087)."
    - Setup command `make deps-2087`.
    - GitHub issue import (repo-scoped configuration) with label
      `grain-2087` every 15 minutes
      (`PUT .../settings`, `issueImportIntervalSeconds: 900`).
  - **CI**:
    - CI grant `cloud-viewer`
      (`capabilities.demo.grain.example › cloud-viewer`).
    - Workflow `lint2087.yml`:
      - `name: [unclosed` is refused with 400. The alert reads
        "workflows: ci: .grain/workflows/lint2087.yml: yaml: line 1:
        did not find expected ',' or ']'", and the editor keeps the
        text.
      - `runs-on: grain` is refused with 400.
      - The corrected workflow saved as `.grain/workflows/lint2087.yml`
        and survived a reload.
      - Each refused edit now sends one `PUT`. On 2026-10-30 an edit
        followed by a blur sent two.
      - The refusal is a timed alert that clears within six seconds.
  - **Releases tab**:
    - **Turn off automatic releases** saved (`PUT .../release`,
      `enabled:false`) with no confirmation, survived a reload, and was
      turned back on.
    - **Jobs dispatched after every merge** `cache-2087` survived a
      reload and was cleared again.
  - **Advanced**:
    - Unticking **Read acme/widgets's own declarations** saved
      (`PUT .../declarations`). Settings → Repositories then read
      `Not read`. It was ticked again.
    - With the demo's repository declaring nothing, the status reads
      "Read at 774661396028: 0 template(s), 0 suite(s), 0 schedule(s),
      0 trigger(s)", so there were no declared triggers to list.
  - **Read/write relationships**: as on 2026-10-29 and 2026-10-30, the
    repo page has no editor for a repository's read/write
    relationships. They live on the new-task form, templates, schedules
    and sessions. That part of the brief does not match this build.
  - **Branch rules (base/head)**: the repo page has no editor for these
    either. What it has is the Branches tab's **Add branch** and its
    base/head compare pickers (§6), and the Releases tab's watched base
    branches.
- **Unsaved edits**: with the refused workflow on screen, **← Repos**
  asked "You have unsaved changes here. Leave and discard them?".
  Dismissing it kept the page.
- **Remove**: Advanced's **Remove** first asked "Remove acme/widgets
  from target repos? …". Dismissed, nothing was removed.

### 5. Merge queue and base-branch gates

- **Switches on A**: **Require branches to be up to date**, **Merge
  when green and clean** and **Base-branch repair PRs … may merge
  without a human** each saved with one click (`PUT
  .../require-branch-up-to-date`, `.../merge-on-green` and
  `.../base-repair-bypasses-human-gates`). `license/cla` was added as a
  check no commit can clear. After a reload all three were on and
  `license/cla` was listed. The Overview cards read `Required`, `On`
  and `On`. The base-repair bypass still takes effect with no
  confirmation, as recorded on 2026-10-29 and 2026-10-30.
- **Merge batch**: the Overview reads "1 change on main", testing, "14m
  of 90m", `grain-merge-queue/0a0c62c2/main at abcdef012345`, and links
  task 5 ("Rotate the Gemini signing key") and PR `#101`. `/tasks/5`
  carries the same "merge batch" panel, and Back returns to the repo
  page.
- **Gates on B**:
  - **Use GitHub CI** plus **Watch acme/widgets's default branch**
    raised "Nothing is testing acme/widgets's default branch, and the
    merge queue is merging onto it anyway. grain CI is not enabled…".
    It appears on the Overview, with a red dot on the CI tab, and on
    `/repos/acme/widgets/releases`.
  - The Releases pane's **Go to CI Mode** (the 2026-10-30 fix) opens
    `settings/ci` with the CI Mode radio in view. The Overview has the
    same control.
  - **Make grain CI the gate** cleared the warning and the CI tab dot
    without a reload.
  - **No CI** with the watch on raised the warning again, with the
    Overview card reading "No CI — base branch not judged".
  - The CI Mode radios are controlled: one shows as checked only once
    `PUT .../ci-mode` answers.
  - A fix made from a second browser tab reaches an already-open
    Releases pane only on the shell's next one-minute gate poll. It had
    not after 12 seconds. A fix made on the page itself clears it at
    once (`onRefreshGates`).

### 6. Branches and compare

- Base starts at `main`. Typing `rel`, ArrowDown and Enter picked
  `release/1.0`, and **Compare** opened
  `/repos/acme/widgets/compare/main...release/1.0`: "Merged into main …
  There are no changes between these branches." The page survived a
  reload, and Back returned to the Branches tab.
- `main...feature/walk-2087`, opened by direct link, is `open`, "Into
  main on acme/widgets -- forked at 77466139". It has side-by-side,
  inline and raw views, and lists `WALK.md` (+1) and `walk.go` (+3). The
  document is 1280 wide at desktop and 390 at phone.
- Direct links:
  - `main...candidate` and `main...main` say there are no changes.
  - `main...no-such-branch` and `nope...main` return 404. The page says
    "GitHub has nothing to compare for … one of the two branches does
    not exist…".
  - `main..feature/walk-2087`, with two dots, is read as a head named
    `main..feature/walk-2087` and gets the same 404 message, prefixed
    "for the default branch...".
- The head picker warns `No branch "missing-branch-xyz" on
  acme/widgets: there is nothing to compare` for an unknown head, and
  **Compare** stays enabled. `feature/walk-2087`, pushed before the
  picker's first read, drew no warning.
- **Add branch** `walk-2087-added` showed "-- pending" and is still
  listed after a reload.

### 7. Releases

- `/repos/acme/widgets/releases` shows:
  - `1.0 -- Active`, `latest: 1.0.latest, prod: 1.0`, and candidate
    `1.0.rc.1 -- active`.
  - The pending-approval banner with **Approve all**.
  - The qualification task **Qualify**, a link to `/tasks/23`. Back
    from it returns to `/releases/1.0`.
  - History, merge-back, **Delete release** and the qualification plan.
- **Cut new RC** is disabled with "Cut new RC: 1.0.rc.1 is active: a
  new RC is cut once it is promoted."
- **Approve all** approved the qualification
  (`POST .../candidates/1/qualification/approve`). After a reload the
  section read "running".
- **Validation**:
  - `bad release name` and `x.latest` are refused (400) with the
    naming rules.
  - `1.0` is refused with "this repo already has an unmerged release
    with this name".
  - An empty name is stopped by the browser's required-field check, and
    nothing is sent.
- **Lifecycle of `wt2087`**:
  - Created at `/releases/wt2087`. While provisioning, both buttons were
    greyed out with "wt2087 is still provisioning: grain cuts and
    promotes its candidates once its wt2087.latest branch exists." It
    then went active as `wt2087.rc.1`.
  - **Promote current RC**: the confirm "Promote wt2087.rc.1 to wt2087?
    This moves acme/widgets's wt2087 branch to wt2087.rc.1." was
    dismissed (no request), then accepted.
  - While the candidate was promoting, both buttons gave their reasons:
    "…is promoting: a new RC is cut once it is promoted" and "…only an
    active candidate can be promoted".
  - Once it was promoted: "Promote current RC: wt2087.rc.1 is already
    promoted: a candidate is promoted once."
  - **Cut new RC** gave `wt2087.rc.2 -- cutting` ("Cut new RC:
    wt2087.rc.2 is still being cut."), then active, with
    `wt2087.rc.1 -- promoted` in history.
  - A reload kept all of it. `/releases/1.0` opens directly, and Back
    returns to `wt2087`. `/releases/nope-404` is rewritten to
    `/releases` with `no release "nope-404"`.
  - **Merge wt2087 into the default branch**: the confirm "…This opens a
    pull request on GitHub from wt2087." was dismissed, and no request
    was sent.
  - **Delete release**: the confirm "Remove release wt2087? This drops
    the release and its candidates from grain." was dismissed (no
    request), then accepted (`DELETE`). The pane fell back to
    `/releases`, and the release was gone after a reload.
- **Release workflows** (Releases tab):
  - **Run release by hand** opens a **Release branch** field. Empty, it
    is held by the browser's "Please fill out this field.". With `main`
    it posted `release-runs` and reported "No jobs: this repository
    declares no workflows, so grain CI had nothing to run."
  - **Run the base-branch check now** posted `base-branch-runs` and
    said a run starts on the next cycle.

### 8. Phone and tablet layout

- At 390 the repo page and its CI, Merge queue and Releases panes, and
  Settings → Repositories, all have no element past the viewport.
- `/repos` did not: see Finding 1.
- From 600 to about 1100 the Repos header is portalled into the rail's
  slot. There **Add repo** wraps behind **More page actions**, as the
  slot is built to do. Opening that menu showed it, and a repo added
  through it was listed.

### 9. Console and network

- No `pageerror`s. `console.error` appeared only for the deliberate 400
  and 404 responses above, and for the 401 of the first, unsigned
  load.
- `GET /api/tasks` shows up as `requestfailed: net::ERR_ABORTED` on
  navigation, as recorded on 2026-10-29 and 2026-10-30. These are polls
  the browser drops on unload, and nothing shows to the user.

## Findings

### 1. Fixed: on a phone, the Repos page's Add repo button was off screen

- **Reproduction** (any `grain demo`):
  1. Open `/repos` in a 390x844 viewport.
  2. **Add repo**'s box is x=423..487, past the viewport's right edge.
     `document.elementFromPoint` at its centre finds nothing. The
     document is still 390 wide, so nothing scrolls it into reach.
  3. At 360 it is the same: the button ends at 487.
- **Cause**: below 600px the header is drawn in the page, as a
  `.content-header` flex row, not in the rail's slot. The add-repo
  form sat beside the title and could not shrink: the picker has
  `minWidth: 300`, and the form and its row had the default
  `min-width: auto`.
- **Fix** (`RepoList.jsx`):
  - The header wraps.
  - On a phone (`xs`) the form takes a line of its own
    (`flexBasis: 100%`), and the form and its row may shrink
    (`minWidth: 0`).
  - The picker's 300px minimum applies from `sm` up, and the button
    does not shrink.
  - At 390 the field is now 208px wide at x=22, and **Add repo** ends at
    x=374 and receives a tap at its centre. From 600 up the layout is
    unchanged: the header is in the slot there, where this style does
    not apply.
- **Regression coverage**: `e2e/repos-add-form-phone.spec.js` checks,
  at 360 and 390, that:
  - the field and button lie inside the viewport;
  - the field is at least 150px wide;
  - the button is what a tap on it lands on;
  - the document is no wider than the viewport.

  It fails on the old build ("Expected: <= 390, Received: 487.046875")
  and passes on Chromium and WebKit with the fix.

### Observations not changed here

- The repo filter box is labelled only by its placeholder (§1). The box
  is shared by every list.
- **Base-branch repair PRs may merge without a human** and **Turn off
  automatic releases** take effect with no confirmation (§4, §5).
- Right after **Read … declarations** is unticked, Advanced still shows
  the last "Read at …" line until a reload. `saveDeclarations`
  documents that the next cycle does the removal.
- A gate fixed from another browser tab reaches an open Releases pane
  only on the shell's minute-long poll (§5).
- A compare URL with two dots (`main..x`) is read as a head name rather
  than as GitHub's two-dot syntax (§6).
- The brief's repo-scoped read/write relationships and base/head branch
  rules are not on the repo page in this build (§4).

## Tests run

- `npx vitest run src/components/RepoList.test.jsx`: 46 passed. The full
  `npx vitest run` is in the closing note.
- `npx playwright test e2e/repos-add-form-phone.spec.js e2e/repo-tab-strip-phone.spec.js e2e/repos-autosave-walkthrough.spec.js`:
  12 passed (Chromium and WebKit).
- The new spec against a build of the unfixed component: 2 failed, as
  expected.
- `npx prettier --check` on the changed files.
- Rendered `/repos` with `node e2e/screenshot.mjs --url …` (the
  `make ui-screenshots` tool):
  - Widths and themes: desktop and phone, light and dark.
  - Builds: before on A, after on C.
  - Bounding boxes measured at 360, 390, 599, 600, 700, 800, 900, 1000,
    1280 and 1600.
  - Screenshots are not committed.
