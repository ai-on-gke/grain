# 2026-11-07 Repository, Merge Queue, and Release UI Walkthrough

Task 3356. A full pass over the Repos list, the repo page's seven tabs,
Settings → Repositories, the merge queue and base-branch gates, compare,
and releases, against the last report
([2026-11-04](2026-11-04-repository-release-memory-ui.md)). It found two
bugs in the same corner of the Agents tab, both fixed here. Everything
else behaved as the 11-04 report describes.

## Environment

- **Commit tested**: `b4b773fe` (`main`, merge of #832). The fix was
  checked on this branch's build of the same tree. Unlike 11-04, `main`
  compiles as it stands.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o … ./cmd/grain` (the frontend is embedded).
- **Servers**, each signed in through the visible access-code form, each
  over its own throwaway store:
  - **A** on `:8433`: `grain demo -addr 127.0.0.1:8433 -open=false
    -access-code-file … -github-sim -reconcile=500ms -fake-ci
    -scheduled-workflows -base-branch-ci -secrets-store -merge-rules
    -merge-queue-states -qualification`
  - **B** on `:8434`, for the base-branch gates: `grain demo …
    -github-sim -reconcile=500ms`
  - **C** on `:8435`, this branch's build, for checking the fix: the same
    flags as B.
- **Driver**: ad hoc Node scripts on Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), kept outside the
  repository. Every script records:
  - `console` errors and warnings, and `pageerror`;
  - failed requests and responses `>= 400`;
  - every non-GET API request with its body;
  - every `dialog`, which is dismissed or accepted on purpose.
- **Viewports**: 1280×900/850 and 390×844, in light and dark.
- **Gemini**: `/home/debian/.gemini-api-key` is not present, so nothing
  used it. The sandbox's file reader shows PNGs as bytes, so layout was
  checked with DOM geometry (bounding boxes, `scrollWidth` against
  `clientWidth`) rather than by eye.

## Stubs

**GitHub** is `grain demo`'s own `-github-sim`, reached through the real
GitHub client. All GitHub-side setup went through the simulator:

- **Issue**: its loopback hook opened issue #8001 on acme/widgets,
  labelled `walk-3356` (`POST /api/e2e/github-sim/repos/acme/widgets/issues`).
- **Commits pushed to the bare `acme/widgets.git` on `main`**:
  `.grain/templates/release-cut.yml` (first with an unknown
  `description:` key), `.grain/triggers/walk-docs.yml` (first without
  `on:`), and `.grain/settings.yml` with `port-branches: [main,
  candidate]` (first with `port-types` written as a list).
- **Branch** `feature/compare-3356`, adding `WALK.md` and `walk.go`.

**One `page.route`** delayed `POST …/import-issues` by 1.5 s, to see the
button's in-flight state. The request still reached the real server.

Nothing replaced Grain's API or its React UI.

## Steps and outcomes

### 1. Repos list (A)

- **Opening it**: the rail's **Repos** item opens `/repos`, and so does
  the direct URL.
- **Adding repos**: **Repo to add** / **Add repo** added `acme/gadgets`
  and an 89-character `acme/an-extremely-long-repository-name-…`
  (`POST /api/repos`). The rail then reads `Repos 3`.
- **Counts**: acme/widgets reads Blocked 1, Proposed 3, Waiting 2,
  Running 2, Asked 4, Merging 3, Deferred 1, 16 tasks.
- **Search** (the box is named by its placeholder, "Filter added
  repos…"): `GADG` leaves only gadgets, and `zzz-no-match` reads "No
  repos match your search."
- **Sort**: Custom order, Name (A–Z), Active first and Most tasks each
  order the rows as named; Custom order brings the hand order back.
- **Keyboard reorder**: Enter on **Move acme/widgets** offers two "Move
  here" targets ("above …", "to the end of the list"), and Escape clears
  them. Picking one moves the row, and focus returns to the handle.
- **Pointer reorder**: `dragTo` from **Move acme/gadgets** onto the top
  row moves it there. The order survives a reload.
- **Tap reorder at 390**: a tap on a handle offers the same two targets,
  and a tap on one moves the row.
- **Long name and busy row**: at 1280 and at 390, light and dark, the
  document is exactly as wide as the viewport.
  - The long name is ellipsised: 860 px at 1280, 144 px at 390.
  - **Blocked 1** is the first chip on acme/widgets and stays visible at
    390 (x 241, 72 px), where every count chip after it is
    `display: none`.
  - **Add repo** stays on screen at 390 (x 303–374).

### 2. Repo row, task count, Back (A)

- A click on the row opens `/repos/acme/widgets`. **← Repos** returns to
  the list, and browser Back returns to the repo page.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows, and Back returns to the repo page.

### 3. Filing a task from the repo page (A)

- **New task** on `/repos/acme/gadgets` opens the form with **repos**
  preselected to `acme/gadgets`.
- **Create task** posts `{"repo":"acme/gadgets",…}` and returns to the
  repo page, which now reads "1 task filed here". Task 24's page names
  acme/gadgets after a reload.
- The repo page asks `GET /api/repos/acme/gadgets/github-branches`, which
  the simulator answers 404 (it only simulates acme/widgets). Nothing on
  the page breaks.

### 4. The repo page's tabs (A)

- **Tabs and URLs**: Overview is `/repos/acme/widgets`; CI, Merge queue,
  Agents, Branches, Releases and Advanced are
  `/repos/acme/widgets/settings/<tab>`. A reload keeps the tab.
- **Back and Forward** leave the page rather than stepping through its
  tabs: picking a tab replaces the entry (`App.jsx`, "a change of tab
  rather than of page"). After four tab clicks `history.length` grew by
  none, and Back from Advanced went to the page before the repo page.
  This is the documented design, not a fault.
- `/settings/bogus` is corrected to `/repos/acme/widgets` on Overview.
- **Phone (390)**: opening Advanced, Releases or Branches by URL puts the
  selected tab in view (x 242–334), with no horizontal overflow.
- **Settings → Repositories** lists every repo with base branch,
  declarations, CI mode and git connection (acme/widgets: `main`, Read,
  Grain CI, Not listed). Its links go to Releases (`main`), Advanced
  (`Read`), CI (`Grain CI`) and Merge queue.
- **Overview**: the summary (CI gate, Up to date before merge, Merge when
  green, Issue import, Base repair bypass, Branches), the merge re-check
  card and recent CI runs.
- **Agents**:
  - **Default capabilities**: "A task filed against acme/widgets starts
    with: API preset: self-debug, grain › source, github › read-issues".
  - **Upstream repository**: `not a repo` is refused ("upstreamRepo:
    repo must be owner/name …") and the box goes back to the stored
    value. The refusal leaves the banner within its five seconds. A good
    value saves (`PUT …/settings`) and survives a reload.
  - **Prompt extension**: "Run make lint before pushing (task 3356)."
    saves on blur, beside the read-only "Deployment-wide, set in
    Settings → Agents: nothing".
  - **Setup command**: `make deps-3356` saves on blur.
  - **GitHub issue import**:
    - **Import issues now** is disabled while import is off.
    - The enable switch and **Queue imported tasks at once** save on
      click. The trust warning reads "Turn it on only if you trust
      everybody who can open an issue on acme/widgets: they write what
      the agent is told to do."
    - `-3` minutes is stopped by the browser ("Value must be greater than
      or equal to 1."). Label `walk-3356` and every 20 minutes saved
      (`issueImportIntervalSeconds: 1200`), and the description then
      reads "Every 20 minutes … labelled “walk-3356”".
    - **Import issues now** reads "Importing…" and is disabled while the
      request is out, then "Read 1 open issue on acme/widgets: imported
      0, 1 already imported." The importer's own cycle had already taken
      #8001.
    - With source `acme/nonexistent-3356`: "Read 0 open issues … Some
      could not be imported: … GitHub answered 404: Not Found".
    - Overview then reads "Issue import: On, held for approval".
    - Typing a label and then flipping the queue switch lost the label,
      and leaving with it typed asked nothing: **Bugs 1 and 2**.
- **CI**:
  - **Watch default branch** saves on click
    (`PUT …/watch-default-branch`).
  - **Workflows**: `lint3356.yml` with `name: [unclosed` is refused
    (400, "yaml: line 1: did not find expected ',' or ']'"), and the
    editor keeps the text.
  - **Unsaved edit**: with that draft unsaved, **← Repos** and the rail's
    **Tasks** each asked "You have unsaved changes here. Leave and
    discard them?". Dismissing kept the page, and switching tabs kept
    the draft. A reload with the draft got the browser's own
    `beforeunload` question.
  - A valid workflow then saved, cleared the refusal, and survives a
    reload as `.grain/workflows/lint3356.yml`.
- **Branches**:
  - **Add branch** `feature/walk-3356` shows "-- pending". Adding it again
    reads "… is already on this repo's list (pending); nothing new was
    added."
  - `bad name..lock` is refused with the branch-name rule.
- **Advanced**:
  - With the three broken files it reads "Last read failed: …
    `.grain/settings.yml`: … cannot unmarshal !!seq into
    map[string][]string; `.grain/templates/release-cut.yml`: … field
    description not found …; `.grain/triggers/walk-docs.yml`: a trigger
    needs `on: merge` or `on: pull_request`; … no template
    "release-cut" … — still running what 1a7c9ba5eda5 declared." Every
    fault is named with its file.
  - With `port-types` written as a map but `bogus->nowhere: []`, it
    reads "port-types: bogus->nowhere names no types: leave the direction
    out to port every merge, or set port-branches to port none".
  - With every file fixed (`bogus->nowhere: [fix]`), the next cycle reads
    "Read at 472b8447df32: 1 template(s) … 1 trigger(s)", with:
    - "Path triggers: .grain/triggers/walk-docs.yml".
    - "Tasks from .grain/templates/release-cut.yml may also push:
      refs/heads/candidate (force), refs/heads/scratch-* (delete),
      refs/tags/v*".
    - The warning "port-types in .grain/settings.yml: bogus->nowhere names
      bogus and nowhere, which is not a paired branch …".
    - The read-only **Ports** table: bogus->nowhere declared fix, ports
      nothing (not a paired branch); candidate->main every merge;
      main->candidate fix, revert.
  - **Remove** on acme/widgets runs `POST /api/settings/plan`, then asks
    "Remove acme/widgets from target repos? …". Dismissed.
  - **Remove** on acme/gadgets, accepted: `DELETE /api/repos/acme/gadgets`,
    back on `/repos` without it (observation 2).

### 5. Merge queue and base-branch gates (A and B)

- **CI mode (B)**: Grain CI with **Watch default branch** ticked, then
  **Use GitHub CI** and then **Use No CI**:
  - Overview's CI gate reads "GitHub CI — base branch not judged" and
    "No CI — base branch not judged".
  - The alert reads "Nothing is testing acme/widgets's default branch,
    and the merge queue is merging onto it anyway …", on Overview and on
    the Releases tab, and at 390 with no overflow.
  - **Go to CI Mode** lands on `settings/ci` with CI Mode in view (y 328
    of 850).
- **Clearing (B)**: back to Grain CI, both warnings cleared with no
  reload. B's separate "-ci=false" notice stays (observation 3).
- **Merge queue settings (A)**, each read back after a reload:
  - **Require branches up to date** and **Merge when green and clean**
    toggle with no prompt.
  - **Base-branch repair PRs may merge without a human** asks "Let
    base-branch repair PRs in acme/widgets merge without a human when
    their CI is green? …" when turned on. Dismissing changes nothing,
    accepting saves it, and turning it off does not ask.
  - Settings → Repositories' switch for acme/widgets behaves the same
    way, and survives a reload.
- **Unfixable checks**:
  - `cla/walk-3356` saved.
  - `grain CI` is refused inline ("grain CI judges one exact commit on
    the branch …").
  - `CLA/WALK-3356` is refused as a duplicate ("… check names are matched
    without case"). Blank input leaves **Add** disabled.
- **Overlap re-check panel**, on Overview and on `/tasks/5`: "1 change on
  main", the **testing** chip, elapsed against max age ("9m of 90m" then
  "27m of 90m"), the ref and commit, a link to task 5 and PR #101.
  **Show the run** opens the run's overlay (running, **Cancel run**).

### 6. Compare (A)

- **Picker**: the Head branch box offers `feature/walk-3356, 1.0.latest,
  1.0.rc.1, candidate, feature/compare-3356, main, release/1.0`. Typing
  `feature/comp` narrows it to `feature/compare-3356`. `no-such-branch`
  reads 'No branch "no-such-branch" on acme/widgets: there is nothing to
  compare'.
- **Compare** opens `/repos/acme/widgets/compare/main...feature/compare-3356`:
  "Into main on acme/widgets -- forked at 156e789e", Side by side /
  inline / raw, `WALK.md` +1 and `walk.go` +3.
- **Reload and Back**: a reload redraws it, and Back returns to
  `settings/branches`.
- **Other refs by URL**:
  - `main...no-such-ref-3356` and `nosuch-base...feature/compare-3356`
    answer 404 and read "GitHub has nothing to compare … one of the two
    branches does not exist …".
  - `release/1.0...main` parses the slashed base and shows the
    `.grain/settings.yml` commit.
  - `main...main` reads "There are no changes between these branches."

### 7. Releases and work ownership (A)

- **Work ownership**:
  - **Standing work on this deployment** off (`{"default":"off"}`, no
    confirmation), then per-item **Release: on** and **Triage …: off**.
  - After a reload the switch is still off, the `aria-pressed` buttons
    match, and the statuses read "off, set here", "on, set here … runs"
    and every other item "not run".
  - Restored to say nothing and on.
- **Release names** (`/repos/acme/widgets/releases`): `bad name`,
  `-lead` and `1.0` are refused on the name box (`aria-invalid="true"`).
  `walk3356` created `/repos/acme/widgets/releases/walk3356`, which
  survives a reload.
- **Picker**: picking 1.0 moves to `/releases/1.0`. Back and Forward move
  between the two releases.
- **Unknown release**: `/releases/nosuch-3356` reads "acme/widgets has no
  release named “nosuch-3356” -- showing walk3356 instead …".
- **Seeded 1.0 (`-qualification`)**:
  - **Cut new RC** is greyed with "1.0.rc.1 is active: a new RC is cut
    once it is promoted." The **Qualify** link goes to `/tasks/23`.
  - **Approve all** approved the qualification.
  - **Promote current RC** asks "Promote 1.0.rc.1 to 1.0? …". Dismissing
    does nothing. Accepting shows "promoting", with both buttons greyed
    and their reasons given, then "promoted".
- **walk3356**: Promote → "walk3356.rc.1 -- promoted" and Promote greyed
  ("is already promoted: a candidate is promoted once"); **Cut new RC** →
  "walk3356.rc.2 -- active", with rc.1 in history, surviving a reload.
- **Merge walk3356 into the default branch** asks "… This opens a pull
  request on GitHub from walk3356." Dismissed.
- **Delete release** asks "Remove release walk3356? …". Dismissing does
  nothing; accepting deletes it, and the picker offers only 1.0.

### Console and network

- **Expected**: the unauthenticated `GET /` 401 before sign-in, the 400s
  and 404s the steps above provoke on purpose, and the simulator's 404
  for acme/gadgets' branches.
- No page errors and no failed requests.

## Bugs

1. **Flipping an issue-import switch threw away a typed label** (fixed).
   - **Reproduction**: on `/repos/acme/widgets/settings/agents`, type
     `typed-not-saved` in GitHub issue import's **Label** (do not press
     Save), then tick **Queue imported tasks at once**.
   - **What happened**: `PUT …/settings {"issueImportApproved":true}`
     is sent, and the Label box goes back to the stored label. Switching
     import off did the same. `saveIssueImport` cleared the whole draft
     after every save, including the switches' saves that do not carry
     it.
   - **Fix**: only a save that carries the source, label and interval
     clears the draft (Save, or switching import on, which already saves
     the draft with it).
2. **Leaving the repo page dropped some unsaved edits without asking**
   (fixed).
   - **Reproduction**: type a label as above and click **← Repos**: the
     page leaves and the label is gone, with no question. Or type into
     **Prompt extension** and reload with the cursor still in it: the
     page reloads and the text is gone.
   - **What happened**: the page's leave guard (`guardLeaving`) asked
     only about workflow rows. The issue-import boxes wait for their own
     Save, and the upstream, prompt-extension and setup-command boxes save
     on blur, which a reload or a closed tab never fires.
   - **Fix**: the guard also asks about an unsaved issue-import draft
     (`issueImportDraftUnsaved`, now shared with the form's own `dirty`),
     and about a blur-saved box whose text differs from what is stored.
     A box whose save is already in flight is skipped, so a click away
     (which blurs, and so saves) does not ask.
   - **Regression coverage**:
     - Two unit tests in `RepoPage.test.jsx`, which fail on `main`:
       "flipping Queue imported tasks at once keeps a label typed beside
       it" and "asks before leaving with an unsaved issue import box or a
       box not yet left". `npx vitest run src/components/RepoPage.test.jsx`:
       91 passed.
     - `ui/e2e/repo-issue-import-draft-kept.spec.js`: the label survives
       the queue switch, and **← Repos** asks and stays when told no.
     - The 32 repo, repository-walkthrough, issue-import, base-branch and
       merge-on-green specs (74 tests, including
       `repo-page-unsaved-edits.spec.js`): all passed.
   - **Check in the browser (C)**: at 1280 light and 390 dark, the label
     survives the queue switch, **← Repos** asks, Save then **← Repos**
     leaves without asking and the label persists, a reload with a focused
     prompt draft gets the browser's question, and clicking away saves the
     draft on blur without asking. No horizontal overflow.

## Observations (not fixed here)

1. **Compare picker**: it still offers `feature/walk-3356`, which is only
   "pending" creation, first among the head branches (as on 11-04). The
   list opens on focus with its first suggestion already highlighted, so
   Enter takes that one and ArrowDown then Enter takes the second. That
   is BranchField's deliberate highlight-first behaviour, noted here only
   because it surprised the driver script.
2. **Removed repo with a queued task**: after removing acme/gadgets, task
   24 is still queued against it, but the repo leaves the list and its
   page reads "0 tasks filed here". This follows `repoRows`' documented
   rule that a restricted deployment's tasks never raise a row of their
   own, and the removal question says it "will drop off the repos list".
   But the count on the repo page reads as if nothing were filed there.
3. **Grain CI mode on a `-ci=false` daemon** (B) still clears the
   "Nothing is testing …" warning, as on 11-04. Overview's separate
   "-ci=false" notice says so. The Releases tab says nothing.
4. **Repeated warning**: the Advanced tab still draws the port-types
   warning twice, once under Declarations and once under Ports (as on
   11-03 and 11-04).
5. **Upstream refusal, then the stored value typed back**: no save is
   sent, because the value is unchanged, so the refusal stays on the
   banner until its five seconds run out. Short-lived, and the box itself
   is correct.

---

# Second pass: task 3160

Task 3160 walked the same area again on the same day, against a newer
`main`. It found three bugs that the first pass did not reach, in the
Releases tab's qualification plan and in Settings → Repositories. All
three are fixed here. Everything else matched the first pass above.

## Environment

- **Commit tested**: `5dd24214` (`main`, merge of #858). The fixes were
  checked on this branch's build of the same tree.
- **Build**: `cd ui && npm ci && npm run build`, then `go build` of
  `./cmd/grain`. `e2e/global-setup.js` builds the binary for the suite.
  Note: `make frontend` runs `npm ci --omit=dev`, which removes
  Playwright and Vitest. Run `npm ci` again before running tests.
- **Servers**: real `grain demo` processes, each over its own throwaway
  embedded store, signed in with the access code:
  - **A**: `-github-sim -reconcile=500ms -fake-ci -scheduled-workflows
    -base-branch-ci -secrets-store -merge-rules -merge-queue-states
    -qualification`.
  - **B**: `-github-sim -reconcile=500ms`, for the base-branch gates.
  - **C**: `-github-sim -qualification
    -external-config-repo=https://github.com/grain/config.git`. Here
    `/api/config` itself answers `configRepoOnly: true` and every
    settings write is refused with 409.
- **Driver**: Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). The ad hoc walks
  ran through the suite's own `e2e/fixtures.js`, kept outside the
  repository. Each walk recorded:
  - console errors and warnings, and `pageerror`;
  - failed requests and responses `>= 400`;
  - every `dialog`, dismissed or accepted on purpose.
- **Viewports**: 1280×900 and 390×844, in light and dark.
- **Gemini**: `/home/debian/.gemini-api-key` is not present, so nothing
  used it.
- **Two sandbox runs**: the first run (attempt 1) did the exploratory
  walk and found the bugs. A usage limit stopped it before anything was
  pushed. The second run, in a fresh sandbox, rebuilt the fixes and
  re-ran the walk where noted. It also ran the regression specs and the
  30 repository, merge queue and release e2e specs listed below.

## Stubs

- **GitHub** is `grain demo`'s own `-github-sim`, reached through the
  real GitHub client. The simulator's bare `acme/widgets.git` got:
  - `.grain/settings.yml` with `port-branches: [main, candidate]` and
    `port-types` (`candidate->main: [fix]`, `main->release: [fix]`);
  - `.grain/triggers/walk-docs.yml`;
  - `.grain/templates/release-cut.yml`, with extra push refs;
  - the branch `feature/compare-3160`.
  These were pushed through `/api/e2e/scheduled-workflows`, the demo's
  e2e hook. An issue labelled for import was opened through the
  simulator's issue hook.
- **CI** is `-fake-ci`. The merge re-check batch carries the seeded run
  `demo-recheck-1`.
- **Delays**: two `page.route` delays of 1.2 s on `POST /api/repos` and
  1.0 s on `POST …/import-issues`. They were used only to see the
  **Adding…** and **Importing…** states. Both requests still reached the
  real server.
- **Config repo** (server C) is the demo's own `-external-config-repo`
  mode, not a rewritten answer.

Nothing replaced Grain's API or its React UI.

## Steps and outcomes

### 1. Repos list, repo page and task filing (A; attempt 1, 34 checks, no page errors)

- **Opening it**: the rail's **Repos** item and the direct `/repos` URL
  both open the list.
- **Counts, search and sort**: they behave as in the first pass.
- **Adding a repo**: the add form locks while the add is in flight
  (**Adding…**).
- **Long name and busy row at 390, light and dark**:
  - no horizontal overflow;
  - **Blocked** is the first chip on acme/widgets and stays visible;
  - **Add repo** stays on screen.
- **Reorder**: pointer drag, keyboard (Enter on the handle, then a "Move
  here" target) and a tap at 390 each move the row. The order survives
  a reload.
- **Repo row**: opening it and returning through **← Repos** works.
- **Task count**: "N tasks filed here" opens `/?f=repo:any:acme/widgets`,
  and browser Back returns to the repo page.
- **New task** from the repo page preselects acme/widgets. The filed
  task keeps it after a reload.

### 2. Tabs and Settings → Repositories (A)

- **Tabs**: each of the seven tabs rides in the URL as
  `/repos/acme/widgets/settings/<tab>`. Each survives a reload and
  Back/Forward. At 390 the selected tab is scrolled into view in the
  tab strip.
- **Settings → Repositories**: each row shows the base branches, the
  declarations, the CI mode and the git connection. Each fact links to
  the tab that changes it.
- **Overview**: shows the summary, the issue-import status, the overlap
  re-check card and recent CI runs.
- **CI**:
  - CI mode and **Watch default branch**;
  - CI workflows;
  - CI capabilities, with each policy verdict judged for acme/widgets.
- **Agents**:
  - **Prompt extension** shows beside the read-only deployment and
    `.grain/settings.yml` layers.
  - **Setup command** shows beside its `.grain/settings.yml` layer.
  - **Default capabilities** are shown.
  - **GitHub issue import**: the enable switch, and the queue-at-once
    switch with its trust warning. Source, label and interval are saved,
    and each survives a reload.
  - **Import issues now** shows **Importing…**, then "Read 1 open issue"
    and "1 already imported".
  - A source repository the simulator lacks reads as a 404 refusal.
  - An unsaved label asks before **← Repos**.
- **Branches**: **Add branch** behaves as in the first pass.
- **Advanced**:
  - The declarations read "Read at <sha>".
  - Path triggers lists `.grain/triggers/walk-docs.yml`.
  - Extra push refs for `release-cut.yml` are listed.
  - The read-only **Ports** table has three rows, with two port
    warnings.
  - On server C, a broken trigger reads "Last read failed: …
    `.grain/triggers/broken.yml` …". The tab still reports what the
    last good read (`0f1e2d3c4b5a`) declared.
  - On server C, `/repos/grain/config/settings/advanced` refuses
    removal: "This is the deployment's own configuration repository and
    cannot be removed here."
  - **Remove** on acme/widgets runs the stranding plan and asks first.
    The question was dismissed.

### 3. Merge queue and base-branch gates (A, B; re-run in attempt 2 on A)

- **Require branches to be up to date** and **Merge when green and
  clean** toggle with no prompt. Each change survives a reload and was
  restored afterwards.
- **Base-branch repair PRs may merge without a human** on Repo → Merge
  queue:
  - Turning it on asks "Let base-branch repair PRs in acme/widgets merge
    without a human when their CI is green? …".
  - Dismissing leaves it off. Accepting saves it, and it survives a
    reload.
  - Turning it off does not ask.
- **The same switch on Settings → Repositories**: turning it on asks the
  same question, the change survives a reload, and turning it off does
  not ask.
- **Unfixable checks**:
  - `cla/walk-3160` is added, and it survives a reload.
  - `grain CI` is refused inline: "grain CI judges one exact commit on
    the branch …".
  - A blank box leaves **Add** disabled.
  - The case-insensitive duplicate refusal is covered by
    `merge-rules-walkthrough.spec.js`, which passed.
- **Overlap re-check**, on Overview: "1 change on main", the **testing**
  chip, "5m of 90m", the recheck ref at `abcdef012345`, and PR #101.
  **Show the run** opens the run overlay, with **Cancel run**.
- **Overlap re-check on `/tasks/5`**: shows the same "5m of 90m" and the
  **testing** chip.
- **CI mode (B)**: switching between **Use GitHub CI**, **Use No CI** and
  **Use Grain CI**:
  - The "Nothing is testing acme/widgets's default branch …" alert
    appears on Overview and on the Releases tab, and at 390 with no
    overflow.
  - **Go to CI Mode** lands on `settings/ci` with CI Mode in view.
  - Back on Grain CI, both alerts clear without a reload.
  - `base-branch-gate-warnings-walkthrough.spec.js` passed on this
    build.

### 4. Compare (A; attempt 1)

- **Picker**: the Head branch box lists the repository's own branches.
- **Compare** opens
  `/repos/acme/widgets/compare/main...feature/compare-3160` and shows
  the diff. The direct link and a reload both redraw it.
- **Other refs by URL**:
  - `release/1.0...main` parses the slashed base.
  - `main...main` reads "There are no changes between these branches."
  - An unknown ref answers 404 and reads "GitHub has nothing to compare
    …".

### 5. Releases and work ownership (A)

- **Work ownership** (attempt 2):
  - Turning **Standing work on this deployment** off asks nothing, and
    the change survives a reload. It was restored afterwards.
  - The per-item buttons (26 `aria-pressed` buttons across the schedules
    and the release) were tested on the first item: "the repository
    default: on" stays pressed after a reload. It was cleared afterwards.
- **Releases** (attempt 1, and the passing specs
  `release_name_validation_walkthrough`, `release_delete_walkthrough`,
  `release-unknown-url` and `qualification-promote-and-recheck-run`):
  - `bad name`, `-lead` and `1.0` are refused on the name box.
  - Creating a release opens `/releases/<name>`, which survives a
    reload.
  - **Cut new RC** is greyed while an RC is active, with "… is active: a
    new RC is cut once it is promoted."
  - **Promote current RC** asks first. Dismissing does nothing.
    Accepting reaches "promoted", and Promote is then greyed with "…
    already promoted".
  - Cut then makes `rc.2`, which survives a reload.
  - **Merge … into the default branch** and **Delete release** each ask
    first.
  - An unknown release URL names the missing release and shows another
    instead.
- **Qualification plan**: see bugs 1 and 2.

### Console and network

- No page errors, and no console errors from the app.
- **Expected 4xx**: the 404 for a missing issue-import source, the 404
  for an unknown compare ref, the 400s for refused release names and
  unfixable checks, and the 409s on server C.
- **Aborted requests**: `GET /api/tasks` showed `net::ERR_ABORTED` when a
  navigation cancelled an in-flight poll. That is harmless.

## Bugs

1. **The qualification plan dropped unsaved edits on leaving, and
   showed rows as typed rather than as saved** (fixed).
   - **Reproduction 1**: on `/repos/acme/widgets/releases`, clear the
     plan row's **Repeat** and click **Add item**. Click
     **← acme/widgets**: the page leaves with no question, and the edit
     is gone.
   - **Reproduction 2**: make the same edit and click **Save
     qualification plan**. The PUT saves one row with `repeat: 1`, but
     the blank row and the empty Repeat stay on screen until a reload.
   - **What happened**:
     - `QualificationPlanEditor` copied the plan into state once, on
       mount, and never took on the saved plan again.
     - Nothing registered the form with `guardLeaving`.
     - The pane's **← repo** and **Open repository settings** buttons
       called `onBack` directly. App's own openers already ask through
       `leavingLosesEdits`, but this path did not.
   - **Fix** (`RepoReleases.jsx`):
     - The editor tracks a baseline and is dirty while what it shows
       differs from it.
     - A successful save makes the payload the new baseline and shows
       it.
     - A poll's new plan replaces the form only while it is clean, so
       the 3 s poll never wipes an edit in progress.
     - The form registers with `guardLeaving`, so a reload and App's own
       navigation ask about it.
     - **← repo** and **Open repository settings** ask
       (`DISCARD_PROMPT`) when there is an unsaved edit.
     - `savePlan` now answers whether the save succeeded, so a refused
       save leaves the edit on screen.
2. **The qualification plan stayed editable when the API refuses its
   save** (fixed).
   - **Reproduction**: on server C, open `/repos/acme/widgets/releases`.
     Every plan control is live, and **Save** answers 409. The same
     happens for a caller who is not one of the repo's admins
     (`adminScoped`), where the save answers 403.
   - **Fix**: the editor is read-only when `manageRefused`,
     `configRepoOnly` or `!repoAdministered(config, repo)`. Under
     `configRepoOnly` a `ConfigManagedNotice` links
     `tables/qualification_config.json` and
     `tables/qualification_item.json`, which are the tables
     `PutQualificationPlan`'s `checkSettingsWritable` names.
3. **Settings → Repositories' base-branch repair switch stayed live
   where the repo's own Merge queue tab is read-only, and swallowed the
   refusal** (fixed).
   - **Reproduction**: on server C, open `/settings/repositories`. The
     acme/widgets switch is enabled, while the repo page's copy is
     disabled. Ticking it asks the confirmation, then the 409 is
     swallowed and the box springs back with no message.
   - **Fix** (`RepositoriesSection.jsx`, `SettingsPage.jsx`):
     - The switch is disabled under `configRepoOnly` or
       `!repoAdministered(config, repo)`, the same rule as RepoPage's
       `readOnly`.
     - The section now gets `showError` from the Settings page, and a
       failed PUT is reported through it.
- **Regression coverage**:
  - **Unit tests**, all failing without the fix:
    - `RepoReleases.test.jsx`, four tests: "shows the saved plan after
      Save, not the rows as typed", "asks before leaving with an unsaved
      qualification plan edit", and read-only under config-repo-only
      and for a non-admin.
    - `RepositoriesSection.test.jsx`, two tests: disabled where the
      repo page is, and the refusal is reported.
    - `npx vitest run` on these two files plus `SettingsPage.test.jsx`
      and `RepoPage.test.jsx`: 182 passed.
  - **Playwright, against the real demo, at 1280 and 390**. All four
    tests fail on `main`'s frontend and pass with the fix:
    - `ui/e2e/repo-release-plan-edits.spec.js`: leaving asks; Save shows
      the saved plan; reload; clean leave does not ask.
    - `ui/e2e/repo-release-plan-config-repo.spec.js`: on server C the
      plan is read-only under the notice, and the bypass switch is
      disabled on both surfaces.
  - **Suite**: 30 repository, merge queue and release specs, including
    the two new ones (71 tests), all passed on this build:
    `base-branch-gate-warnings-walkthrough`, `config-repo-only-settings`,
    `issue-import-walkthrough`, `merge-on-green-base-repair-walkthrough`,
    `merge-on-green`, `merge-rules-walkthrough`,
    `new_task_draft_repo_page`, `qualification-promote-and-recheck-run`,
    `release-unknown-url`, `release_delete_walkthrough`,
    `release_name_validation_walkthrough`, `repo-ci-capabilities`,
    `repo-compare-picker`, `repo-issue-import-draft-kept`,
    `repo-list-blocked-chip`, `repo-ownership-ci-security`,
    `repo-page-unsaved-edits`, `repo-pane`,
    `repo-release-memory-inputs-walkthrough`,
    `repo-setting-refusal-clears`, `repo-tab-strip-phone`,
    `repo-tabs-walkthrough`, `repo_release_memory_edits_walkthrough`,
    `repos-add-form-phone`, `repos-autosave-walkthrough`,
    `repos_workflows`, `repository-release-memory-regressions-walkthrough`
    and `repository_release_memory_walkthrough`.
  - **Screenshots**: `e2e/screenshot.mjs` was run on server C's flags
    for `/repos/acme/widgets/releases` and `/settings/repositories`, at
    desktop and phone, light and dark, before and after the fix. They
    were not committed.

## Observations (not fixed here)

The first pass's observations 2 to 5 still hold. The second pass found
nothing new outside the bugs above.
