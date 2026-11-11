# 2026-11-11: Repository, merge queue, compare and release UI walkthrough (task 3933)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/098073e4/task-3933-walk-through-repository-merge`.
- **Commit tested**: `87a5e763` (`main`, "Merge pull request #1220"), binary
  `v0.0.0-20261111140231-87a5e7638d43`. The one fix below was then checked
  again in the browser on a build of this branch.
- **Build**:
  - Frontend: `cd ui && npm ci && npm run build`, the real Vite bundle
    embedded in the binary.
  - Server: `go build -tags grain_e2e ./cmd/grain`. `/var/tmp/grain-base` is
    the base build and `/var/tmp/grain-fixed` is the fixed one.
- **Toolchain**: Debian sandbox, `linux/amd64`, Go 1.26.10, Node.js v20.19.0.
- **Browser**: headless Playwright Chromium from `ui/node_modules`
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
  - It was driven by throwaway node scripts kept outside the checkout. They
    used only visible controls: the rail, tabs, buttons, switches, radios,
    text boxes, the branch suggestion list and the keyboard.
  - Viewports: 1280×900 (or 1280×844) desktop and 390×844 phone, in light
    and dark.
- **Sign-in**: every script signed in through the Access code page, typing
  an email and the demo's access code. The REST API, with the access code as
  a bearer token, was used only to read things back and to look up one
  task id.
- **What each script recorded**: uncaught page errors, console errors and
  warnings, failed requests, every `/api/` response of 400 or more (with
  its body), every non-GET `/api/` request (with its body), and every native
  dialog (its text, and whether it was accepted).
- **Gemini**: there was no `/home/debian/.gemini-api-key`, so no model was
  called. PNGs could not be read back in this sandbox either, so layout was
  checked with DOM geometry: bounding boxes, chip visibility,
  `scrollWidth <= clientWidth` and helper-text colour.
- **Servers**: each one is a real `grain demo` over a throwaway embedded
  store, with `TMPDIR` set to a fresh directory, and
  `-access-code-file <file>`.
  - **A** (`:4290`, base build): `-github-sim -reconcile=1s -fake-ci
    -scheduled-workflows -base-branch-ci -secrets-store -merge-rules
    -merge-queue-states -qualification`.
  - **B** (`:4291`, base build): `-github-sim -reconcile=1s`. With
    `-base-branch-ci` off, a watched default branch under GitHub or No CI is
    an unjudged base-branch gate.
  - **C** (`:4292`, fixed build): `-github-sim -reconcile=1s -fake-ci
    -base-branch-ci`, used to check the fix again.

## Stubs and external-boundary helpers
Grain's own API and React UI were never replaced. The only interceptions
and helpers were these:
- **GitHub**: the in-process GitHub simulation (`-github-sim`), over a real
  bare repository, stood in for GitHub everywhere: branches, compare, issue
  import, and release branches with their RC cut and promote.
- **Git pushes to the simulated repository**: `git push` into
  `tmpA/grain-demo-*/githubsim/acme/widgets.git` of the branches
  `feature/compare-3933` and `fix/x#3933`, each with one commit to
  `walk3933.txt`.
- **Issues**: two issues were seeded through the demo's loopback-only
  `POST /api/e2e/github-sim/repos/acme/widgets/issues`. Issue 8001 carries
  the label `walk-3933`; issue 8002 has no label.
- **One delayed request**: a Playwright `page.route` on `POST /api/repos`.
  It held Add repo's request until the script had measured the in-flight
  form, then let the request through to the real server unchanged
  (`route.continue()`). Nothing was answered on the server's behalf.
- **Loading, error and empty states** were reached through real negative
  cases rather than stubs:
  - an unknown compare ref, release name and tab;
  - an unlistable issue source;
  - invalid release, branch and upstream names;
  - a repository with no GitHub credential (`github-branches` 404s).

## Steps and outcomes

### 1. Repos list
- **Rail and direct URL**: the rail's Repos entry and a direct
  `/repos` both open the list. The rail count follows the allowlist (1,
  then 2 after the adds and the removal below). **PASS**
- **Counts**:
  - `acme/widgets` shows Blocked 1, Proposed 3, Waiting 3, Running 2,
    Asked 4, Merging 3, Deferred 1 and "17 tasks".
  - The closed chip is carried but hidden on the list, and shown on the
    repo page.
  - **PASS**
- **Add repo, in flight**: with the POST held, the "Repo to add" box and
  the Add button were both disabled. The button kept "Add repo"'s 71px
  width, at (997, 18) on a 1280px viewport and at (303, 108) on 390px,
  inside the viewport each time. **PASS**
- **Add repo, done**: on release it said "Added acme/x…-end". The 83- and
  84-character names did not overflow the page at 1280 or 390. **PASS**
- **Busy row**: on `acme/widgets`, Blocked 1 is the first chip and visible.
  At 390px it is the only chip still visible: every later chip is hidden,
  and the strip does not overflow. **PASS**
- **Filter**: `WIDG` matches `acme/widgets` (case-insensitive). `zzz` says
  "No repos match your search.", and Clear the search box restores all
  three rows. **PASS**
- **Sort**: Custom order, Name (A–Z), Active first and Most tasks. Active
  first puts `acme/widgets` first. **PASS**
- **Reorder by keyboard and tap**:
  - Focusing the "Move acme/very-long…" handle and pressing Enter shows
    "Moving 1 repo — choose where it goes", with "Move here, above …" and
    "Move here, to the end of the list" targets.
  - Enter on the last target moved the row to the end.
  - The order survived a reload, with the sort still on Custom order.
  - **PASS**
- **Reorder by pointer**: dragging the third row's handle above the first
  row reordered the list, and the order survived a reload. **PASS**

### 2. Repo page, task counts and filing a task
- **Row and back**: clicking the `acme/widgets` row opens
  `/repos/acme/widgets`. Browser Back and "← repos" both return to
  `/repos`. **PASS**
- **"17 tasks filed here"**:
  - It links to `/?f=repo:any:acme/widgets`, the filtered task list.
  - Back and Forward step between the list and the page.
  - **PASS**
- **Case-insensitive repo match**: `/?f=repo:any:ACME/Widgets` reads
  "repo is any of acme/widgets" and lists the same tasks. **PASS**
- **New task from the repo page**:
  - On `acme/xvery-…-end`, New task opened with the repo preselected in
    both the picker and the hidden `repo` field.
  - Create task sent `"repo":"acme/xvery-…-end"`.
  - The page then read "waiting 1 / 1 task filed here", and task 25
    carries the repo.
  - **PASS**
- **Removing a repo with work left**:
  - Remove (Advanced) asked a native confirm naming the repo, and
    `DELETE /api/repos/…` followed. The list no longer shows the repo.
  - Its page then read "waiting 1 / 1 task still targets this repo, which
    is no longer on the target list".
  - After closing task 25 from its task page, the removed repo's page
    read "closed 1" with the same sentence. That sentence links to
    `/?f=repo:any:acme/xvery-…-end`, which lists task 25, closed work
    included.
  - **PASS**

### 3. Repo tabs and Settings → Repositories
- **Tab URLs**: Overview (`/repos/acme/widgets`), then `/settings/ci`,
  `/settings/merge-queue`, `/settings/agents`, `/settings/branches`,
  `/settings/releases` and `/settings/advanced`. A direct URL and a reload
  keep the tab. **PASS**
- **Other entry routes**:
  - `/settings/nonsense` falls back to Overview.
  - The old `#ci` link opens CI and moves it into the path.
  - From a focused tab, ArrowRight moves focus to the next tab, and Enter
    selects it.
  - **PASS**
- **Back across tabs**: switching tabs replaces the address rather than
  pushing one, so Back leaves the repo page rather than stepping through
  tabs. `App.jsx` documents this as intended, as it was when the tab rode
  in the hash. Back and Forward between pages restore the tab. **PASS (by
  design)**
- **Phone tab strip**: at 390px in dark, the selected tab stayed within
  the viewport for every tab (x 56→244, width 90–115), with no page
  overflow. **PASS**
- **Settings → Repositories**:
  - The table lists base branch, declarations, skills, CI mode and git
    connection for each repository.
  - `acme/widgets` reads "main · Read · Default folders · Grain CI · Not
    listed".
  - It also lists the removed `acme/xvery-…-end`, because a task still
    targets it (`knownRepos`, `ui/src/state.js`). See Observations.
  - **PASS**

### 4. CI, merge queue and base-gate warnings
- **CI mode on B**:
  - On B, the radios moved through GitHub → Grain → GitHub → No CI →
    Grain, with each `PUT …/ci-mode` sent once.
  - "Watch acme/widgets's default branch…" was ticked under Grain CI
    (`PUT …/watch-default-branch`).
  - **PASS**
- **Base-gate warning appears**:
  - After switching to GitHub CI, the error alert "Nothing is testing
    acme/widgets's default branch, and the merge queue is merging onto it
    anyway…" appeared on the CI tab and on Overview, without a reload.
  - It stayed for No CI, and showed on the Releases pane as well.
  - **PASS**
- **"Go to CI Mode"**: on Overview it switches to `/settings/ci` and
  scrolls CI Mode into view. On the Releases pane it opens
  `/repos/acme/widgets/settings/ci`. **PASS**
- **Warning clears**: switching back to Grain CI cleared the warning on
  Overview and on the Releases pane, without a reload. **PASS**
- **Server A**: with `-base-branch-ci` the branch is judged, so no warning
  appeared in any mode, which is correct. **PASS**
- **Merge queue copy**:
  - "Merge when green and clean" says the merge queue no longer keeps an
    order and that the switch changes nothing. It makes no ordering claim.
  - Require up to date, Unfixable checks ("Checks no commit here can
    clear") and the bypass risk copy are all present.
  - **PASS**
- **Base-repair bypass on Repo → Merge queue**:
  - Turning it on asks a native confirm. Accepting saved `true`, which
    survived a reload. Turning it off sent `false` with no prompt.
  - Dismissing the confirm left it off (no PUT), also after a reload.
  - **PASS**
- **Base-repair bypass on Settings → Repositories**: the same behaviour
  on the per-repo switches there. **PASS**
- **Overlap re-check panel, repo page**: "merge re-checks · 1 change on
  main · testing · Nm of 90m · show the run ·
  grain-merge-queue/db238af6/recheck/5/main at abcdef012345 · Rotate the
  Gemini signing key · #101". The elapsed time advanced across visits
  (8m → 31m). The task link goes to `/tasks/5`, and "#101" is the
  pull-request link. **PASS**
- **Overlap re-check panel, task page**: task 5 shows the same "merge
  re-check" panel with "Waiting for CI" and the re-check explanation.
  **PASS**
- **CI capabilities**: Add grant… → typing `release` lists
  `release server › image-signing`, `› package-publish` and
  `› registry-push` (`grain.dev/capabilities/release`). **PASS**

### 5. Agents tab
- **Upstream repository, prompt extension and setup command**: each saves
  when focus leaves (`PUT …/settings`, `…/prompt-extension`,
  `…/setup-command`), and the values survived a reload. So leaving the
  page with one typed saves it rather than asking. **PASS**
- **Refused upstream**:
  - `not a repo!!` was refused with a 400. The reason showed in the app's
    toast, and the box went back to the saved value.
  - Nothing unsaved is left, so leaving does not ask.
  - **PASS**
- **Issue-import draft guard**: an unsaved source edit asks "You have
  unsaved changes here. Leave and discard them?" from the rail. A reload
  raises `beforeunload`. Switching tabs keeps the draft. **PASS**
- **Issue import switches**:
  - Enable saved `issueImportEnabled:true`.
  - Queue at once saved `issueImportApproved:true`. It sits beside the
    trust warning ("Turn it on only if you trust everybody who can open an
    issue…").
  - **PASS**
- **Issue import settings**:
  - An interval of `0` or `-5` is held back by the box's own `min=1`
    ("Value must be greater than or equal to 1.", focus on the box).
  - Source `acme/does-not-exist`, label `walk-3933` and 15 minutes saved
    together, and the description updated to "Every 15 minutes … labelled
    “walk-3933”".
  - **PASS**
- **Import issues now**:
  - With the missing source it said "Could not read the open issues on
    acme/does-not-exist: … GitHub answered 404: Not Found" (it read
    nothing).
  - With the source cleared it said "Read 1 open issue on acme/widgets:
    imported 0, 1 already imported." The reconciler had already imported
    issue 8001 within a second of enabling.
  - The saved values survived a reload.
  - **PASS**

### 6. Branches and compare
- **Add branch**:
  - `walk/3933-branch` was added and survived a reload.
  - `bad name` was refused with a 400, but only through a five-second
    toast. The refused name stayed in the box, unmarked: **bug 1**,
    fixed.
- **Compare from the picker**: typing `fix/` in Head branch suggests the
  repository's own `fix/x#3933`. Compare opens
  `/repos/acme/widgets/compare/main...fix/x%233933` with the
  `walk3933.txt` diff (+2 −0), and a reload keeps it. **PASS**
- **Compare from a direct link**:
  `/repos/acme/widgets/compare/main...feature/compare-3933` shows "hello
  3933". **PASS**
- **Unknown ref**: `main...no-such-branch` says "GitHub has nothing to
  compare … one of the two branches does not exist…". Back returns to the
  previous comparison. **PASS**

### 7. Releases
- **Pane**: release `1.0` shows its current candidate, Cut new RC (greyed
  out, with "Cut new RC: 1.0.rc.1 is active: a new RC is cut once it is
  promoted."), Promote, the qualification plan, history, merge back and
  delete release. **PASS**
- **Refused names**:
  - `has space`, `../x` and `1.0` (already unmerged) were each refused
    inline on the "new release name" box, with `aria-invalid=true` and no
    toast.
  - The next edit cleared it, and so did a successful create of
    `walk3933`, which opened `/repos/acme/widgets/releases/walk3933`.
  - `rc` is a valid name and was created (branches `rc.latest` and
    `rc.rc.1`).
  - **PASS**
- **Unknown release URL**: `/releases/no-such-release` lands on
  `/repos/acme/widgets/releases` with "acme/widgets has no release named
  “no-such-release” -- showing walk3933 instead. The link may be stale, or
  the release deleted; pick another below." **PASS**
- **Promote current RC**: it asked "Promote walk3933.rc.1 to walk3933?
  This moves acme/widgets's walk3933 branch to walk3933.rc.1." Accepting
  promoted it. Promote then greyed out ("already promoted: a candidate is
  promoted once") and Cut new RC enabled. **PASS**
- **Cut new RC**: it made `walk3933.rc.2` (active). History "rc.2 —
  active / rc.1 — promoted" survived a reload. **PASS**
- **Qualification plan guard**: editing "repeat" and pressing
  "← acme/widgets" asks "You have unsaved changes here. Leave and discard
  them?". A reload raises `beforeunload`. **PASS**
- **Work ownership**: the Releases tab shows "Standing work on this
  deployment: on" with its Covers and Does-not-cover copy. Toggling it
  re-reads the gates (unit-covered, "re-reads the base-branch gates when
  standing work is switched off and on"). **PASS**

### 8. Console and network
- Every `401` is the first unauthenticated load before sign-in.
- Every other console error is the browser's line for a deliberate 400 or
  404 listed above:
  - release names, branch name and upstream;
  - unknown compare ref;
  - `github-branches` on the removed repo, which has no credential.
- No uncaught page error and no 5xx.
- The only failed requests were `GET /api/tasks` and
  `GET /api/tasks/25?…` polls aborted by navigation.

## Bugs found and fixed

### 1. A refused branch name was a toast that went while the name stayed (`ui/src/components/RepoPage.jsx`)
- **Reproduction**:
  1. On `/repos/acme/widgets/settings/branches`, type `bad name` into
     Branch name and press Add branch.
  2. `POST /api/repos/acme/widgets/branches` answers 400 `invalid branch
     name "bad name": …`.
  3. The reason appears only in the app's toast. Eight seconds later it
     has gone, while `bad name` is still in the box with
     `aria-invalid=false` and the default helper text.
  - The release name box on the Releases pane had already been moved to
    show its refusal inline for this reason
    (`2026-11-03-repository-release-memory-ui.md`).
- **Fix**:
  - A 400 from Add branch now sets the Branch name box's `error` and puts
    the server's reason in its helper text, so it is read as the box's
    accessible description.
  - The next edit or a successful add clears it.
  - Any other failure still goes to the app's toast (`refuseSave`).
- **Regression coverage**:
  - `RepoPage.test.jsx` › "shows a refused branch name on its box until it
    is edited". It fails on the base commit.
  - `e2e/repo-release-memory-inputs-walkthrough.spec.js` now asserts the
    box's accessible description and `aria-invalid`, and that the refusal
    is gone after a valid add. All 3 tests there pass over `grain demo`.
- **Re-verified on server C** at 1280px and 390px, in light and dark:
  - The helper text has the error class and colour (`rgb(185,58,48)` in
    light, `rgb(236,116,105)` in dark).
  - It sits under the box, left of Add branch (desktop helper x 274–809,
    button 831–908; phone helper 42–270, button 292–362). It wraps to two
    lines at 390px.
  - No horizontal overflow and no toast. Editing clears `aria-invalid`.
  - `e2e/screenshot.mjs` was run for
    `/repos/acme/widgets/settings/branches` at both widths and themes.

## Observations (not changed)
- **Settings → Repositories after a removal**: the table keeps listing a
  removed repository while any task targets it, because the rows come
  from `knownRepos` (allowlist plus every task's repo). The rail and the
  page header count only the allowlist (2, against 3 rows). Its page
  explains why it is still there; the table does not. This is left as is.
- **"N task(s) still targets this repo"** is also said of closed tasks.
  The sentence is accurate enough for history, but reads oddly when the
  only task is closed.
- **Toasts for autosave refusals**: the upstream-repo and prompt boxes
  report a refusal in the app's five-second toast and restore the saved
  value. Nothing is left unsaved, so the toast going is not a loss.
- **"Go to CI Mode"** still leaves focus on the button after scrolling, as
  the 11-09 run noted.
- **Not covered this run**:
  - config-repo-locked (`-external-config-repo`) and role-restricted
    read-only rendering;
  - the Ports table, which needs paired branches;
  - trigger files and extra push refs pushed under `.grain/`.
  The 11-08 and 11-09 runs covered these. Nothing in this run's diff
  touches them.
