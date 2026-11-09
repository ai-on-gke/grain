# 2026-11-09 — Repository, merge queue, compare and release UI walkthrough (task 3716)

## Environment
- **Repository**: `ai-on-gke/grain`.
- **Commit tested**: `8b0d6bdf` (`main`, `v0.0.0-20261109042256-8b0d6bdfa020`).
  The fixes below were then re-verified on this branch's build.
- **Build**:
  - Frontend: `cd ui && npm ci && npm run build`, the real Vite bundle
    embedded in the binary.
  - Server: `go build -tags grain_e2e ./cmd/grain`. `grain-bin` is the base
    build and `grain-bin2` is the fixed one.
- **Browser**: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
  It was driven by node scripts outside `ui/e2e`, through the visible
  controls (rail, tabs, buttons, menus, text boxes, keyboard).
  - Viewports: 1280×900 desktop and 390×844 phone.
  - Colour schemes: light and dark.
- **Gemini**: there was no key at `/home/debian/.gemini-api-key`, so
  nothing was Gemini-backed, and screenshots could not be described by
  `LOOK=1`. Layout was checked with DOM geometry: bounding boxes,
  `scrollWidth <= clientWidth`, and chip and status positions.
- **Servers**: each one is a real `grain demo` over a throwaway embedded
  store. Every caller signs in with the demo's access code.
  - **A** (`:4290`, base build): `-github-sim -reconcile=1s -fake-ci
    -scheduled-workflows -base-branch-ci -secrets-store -merge-rules
    -merge-queue-states -qualification`.
  - **B** (`:4291`, base build): `-github-sim -reconcile=1s`. This is the
    server where a GitHub or No CI mode with a watched default branch
    gives an unjudged base-branch gate. On A, `-base-branch-ci` already
    judges the branch.
  - **C** (`:4292`, fixed build): the same flags as A, used to re-verify
    the fixes.

## Stubs and external-boundary helpers
Grain's own API and React UI were never mocked or intercepted.
- **GitHub**: the in-process GitHub simulation (`-github-sim`), over a
  real bare repository, stood in for GitHub everywhere: branches,
  compare, issue import, release branches and the RC cut/promote.
- **Git pushes to the simulated repository**: `git push` into
  `/tmp/grain-demo-*/githubsim/acme/widgets.git`.
  - `.grain/templates/docs-check.yml`, with `push:` refs.
  - `.grain/triggers/docs-gate.yml`.
  - A deliberately broken `.grain/triggers/broken.yml`, removed again
    afterwards.
  - `.grain/settings.yml`, with `prompt-extension`, `setup-command`,
    `port-branches: [main, release/1.0]` and `port-types`, including the
    unpaired `main->nonexistent`.
  - The branches `feature/compare-3716` and `feature/x#hash`.
- **Seeding through the demo's loopback-only `/api/e2e/github-sim/...`
  routes**:
  - Two issues, one carrying the label `walk-3716`.
  - A branch `fix/#3716-…`, created through the sim's git-database
    `POST .../git/refs`. GitHub allows `#` in a branch name; grain's own
    Add branch refuses it, correctly.
- **No route interception**: no `page.route` stubs were needed this run.
  Loading, error and empty states were reached through real negative
  cases:
  - an unknown compare ref, an unknown release name and an unknown tab;
  - a missing issue source, invalid release names and invalid YAML;
  - an invalid branch name and invalid issue-import intervals.

## Steps and outcomes

### Repo list (`/repos`)
- **Reaching the list**: the rail's `repos` button and the direct URL
  both open `/repos`. The rail count follows the number of repos.
- **Counts**: `acme/widgets` shows `blocked 1, proposed 3, waiting 2,
  running 2, asked 4, merging 3, deferred 1, 16 tasks`. These agree with
  the repo page's `16 tasks filed here`.
- **Adding repos**: added `acme/gadgets` and a 70-character name. Both
  persisted across a reload.
- **Filtering and sorting**:
  - `gadg` filters the list down to `acme/gadgets`.
  - The sort options are custom order, name (a–z), active first and most
    tasks.
  - `most tasks` puts `acme/widgets` first and removes the drag handles,
    as designed.
- **Reordering**:
  - Pointer: dragging `acme/widgets`' row to the top persisted across a
    reload.
  - Keyboard: focus the handle (`Move acme/…`), press Enter, and two
    `Move here, above …` slots appear under the `Moving 1 repo` bar. Enter
    on a slot moves the row.
  - Escape cancels a pick-up and leaves no slots.
- **Phone (390 px), light and dark**:
  - No horizontal overflow.
  - The long name is truncated (`scrollWidth > clientWidth`).
  - `Blocked 1` is the only chip shown on the busy row, and it sits right
    after the name (241–312 px).
  - The Add repo form spans 16–374 px.
- **Opening a repo**: a row opens `/repos/acme/widgets`, and Back returns
  to `/repos`.
- **Task count link**: `16 tasks filed here` opens
  `/?f=repo:any:acme/widgets`, with the filter chip and 16 task links.
  Back and Forward step between the two pages.

### Filing a task from the repo page
- `new task` on `/repos/acme/gadgets` preselects `acme/gadgets` as the
  target repo.
- **Persistence**: filed `Walkthrough 3716: check gadgets preselection`
  as task 24. `GET /api/tasks` reports `repo: acme/gadgets`, and so does
  a reload of `/tasks/24`.
- **Expected 404s**: `GET /api/repos/acme/gadgets/github-branches` 404s,
  because the sim only serves `acme/widgets`. The branch field stays
  usable.

### Tabs, URL, reload and Back/Forward
- **Tab URLs**: each of `ci`, `merge-queue`, `agents`, `branches`,
  `releases` and `advanced` sets `/repos/acme/widgets/settings/<tab>`.
  Overview is the bare `/repos/acme/widgets`. A reload keeps the selected
  tab. `/settings/nosuchtab` opens Overview.
- **Back and Forward**: switching tabs replaces the history entry, so
  Back leaves the page rather than stepping through its tabs. This is
  documented in `App.jsx` and was checked against it.
- **Phone**: the selected tab is scrolled into view. `advanced` sits at
  x = 241–334 and `ci` at 146–236, with no overflow.
- **Settings → Repositories**: the table shows repository, base branch,
  declarations, CI mode and git connection, for example
  `acme/widgets  main  Read  Grain CI  Not listed`. A row opens its repo
  page.

### Overview, CI and Merge queue
- **Overview**: the summary cards were checked, and so were the merge
  re-checks panel and the CI runs table.
  - The panel shows the `testing` chip, an elapsed time against max age
    (`14m of 90m`, which reached `50m of 90m` later), the ref and SHA, the
    task link `/tasks/5` and the PR link `#101`.
  - `Show the run` opens the run in the app's CI-run overlay, with Cancel
    run. Task 5's own page shows the same panel.
  - Phone overflow: none.
- **CI Mode and base-branch gate warnings (server B)**:
  - Switching to GitHub CI, with Watch default branch on, raises
    `Nothing is testing acme/widgets's default branch…`. It appears in the
    header on the CI, Overview and Releases tabs, and Overview reads
    `GitHub CI — base branch not judged`.
  - `Go to CI Mode` switches to the CI tab and scrolls CI Mode into view.
  - Choosing grain CI clears the warning without a reload. No CI raises it
    again (`No CI — base branch not judged`), and grain CI clears it
    again.
- **CI workflows**:
  - Invalid YAML is refused inline:
    `…walk-3716.yml: yaml: mapping values are not allowed`.
  - A valid workflow saved on blur and was still there after a reload.
- **CI capabilities**: the Add grant… menu carries per-method policy
  verdicts for acme/widgets. It disabled every release-server method:
  **bug 3**.
- **Merge queue**:
  - **Up to date** and **Merge when green**: both persisted across a
    reload.
  - **Base-branch repair bypass**: turning it on asks for confirmation on
    both Repo → Merge queue and Settings → Repositories, and it persisted
    across a reload.
    - Dismissing the dialog left the box off; accepting turned it on.
    - Turning it off asked nothing.
  - **Unfixable checks**:
    - Add is disabled while the box is blank.
    - `grain CI` is refused with its helper text.
    - `CLA/ACME-3716` is refused as a case-insensitive duplicate.
    - `cla/acme-3716` persisted across a reload.
    - The chip removes with the keyboard: focus it, then press Delete.

### Agents
- **Layers**: the deployment layer reads `nothing`, and the
  `.grain/settings.yml` layers show read-only after the declaration sync.
  - Prompt extension: `Repo says: keep commits small (3716).`
  - Setup command: `make init-3716`.
- **Saving**: the editable prompt extension and setup command save on
  blur and persisted across a reload. Leaving through the rail with a
  typed, unblurred setup command saved it rather than losing it.
- **Issue import**:
  - Enabling it saves at once. The queue-at-once switch carries its trust
    sentence inline.
  - **Validation**:
    - The source `not a repo` is refused with
      `issueImportRepo: repo must be owner/name…`.
    - `-5`, `0` and `2.5` are blocked by the box's native `min`/step
      validation.
    - Through the API, `-60` and `30` are refused with `must be 0 for the
      default or at least 60`.
  - A label and an interval of 15 persisted across a reload.
  - **Leaving with an unsaved label**: going through the rail asks
    `You have unsaved changes here. Leave and discard them?`, and
    dismissing it stays on the page.
  - **Import issues now**: reports
    `Read 1 open issue on acme/widgets: imported 0, 1 already imported.`
    The reconciler had already imported both seeded issues (tasks 26 and
    27) before the label was saved.
  - **A missing source** (`acme/does-not-exist-3716`) reported `Read 0
    open issues … Some could not be imported: …404`: **bug 1**.

### Branches and Compare
- The head picker offers the repository's own branches as menu items:
  `1.0.latest`, `1.0.rc.1`, `candidate`, `feature/compare-3716`,
  `feature/x#hash`, `main` and `release/1.0`.
- **`main...feature/compare-3716`**: shows `WALK3716.md +1` and
  `walk.go +3`. A direct link works, and Back returns.
- **An unknown ref**: `GitHub has nothing to compare for
  main...nosuch-3716…`, with no phone overflow.
- **`feature/x#hash`**: the comparison opened in-app, but a reload
  compared `feature/x` and 404'd: **bug 2**.
- **Add branch**: `fix/#3716-hash` is refused with the branch-name rule
  in an Alert. `fix/3716-plain` was accepted (`pending`).

### Advanced
- **A broken trigger**: the declaration read failed with `Last read
  failed: … no template "no-such-template-3716" — still running what
  9d5773112aea declared.` The tab needs a reload to show it.
- **After removing the broken trigger**:
  - `Read at db96653437e8: 1 template(s), 0 suite(s), 0 schedule(s), 1
    trigger(s)`.
  - `Path triggers: .grain/triggers/docs-gate.yml`.
  - `Tasks from .grain/templates/docs-check.yml may also push:
    refs/heads/docs-sync/*, refs/tags/docs-* (force)`.
  - A read-only Ports table: `main->nonexistent` → `nothing (not a
    paired branch)`, `main->release/1.0` → `fix, docs`, and
    `release/1.0->main` → `every merge`. The port-types warning shows.
- **Declaration syncing**: the switch saves without a confirmation. Off
  reads `Not read yet — grain picks this up on its next cycle.` after
  being switched back on. It was restored to on.
- **Remove repository**:
  - For `acme/gadgets`, which holds task 24, the confirm was dismissed and
    the repo kept.
  - The long-named repo was removed, and the page returned to `/repos`.
  - The config-repo refusal is covered on an external-config demo by
    `repo-release-plan-config-repo.spec.js`. It was not re-walked here.

### Releases
- **Work ownership** (Releases tab):
  - Switching Standing work off reads `standing work on this deployment:
    off`.
  - Per-item overrides persisted across a reload: `Release: on` reads
    `on, set here`, and the schedule override `Release Qualification…:
    off` held too.
  - Everything was reset to `say nothing` and on, and that persisted.
  - The release workflow controls (`automatic releases on`, `turn off
    automatic releases`, `run release by hand`) render beside them.
- **Release 1.0** (`/repos/acme/widgets/releases/1.0`):
  - `Cut new RC` is disabled with `Cut new RC: 1.0.rc.1 is active: a new
    RC is cut once it is promoted.`
  - The qualification task links to `/tasks/23`. `Approve all` moves
    qualification from pending approval to running.
  - **Promote**: the confirm reads `Promote 1.0.rc.1 to 1.0? This moves
    acme/widgets's 1.0 branch to 1.0.rc.1.` Dismissing it changes
    nothing. Accepting gives `1.0.rc.1 -- promoted`, and `Promote current
    RC` is then disabled with `…is already promoted: a candidate is
    promoted once.`
  - `Cut new RC` then gives `1.0.rc.2 -- active`. The history lists both
    candidates, and it survives a reload.
- **Creating releases**:
  - `bad name` and `-lead` are refused with the release-name rule.
  - `1.0` is refused with `this repo already has an unmerged release with
    this name`.
  - A 120-character name and `2.0-3716` were created, and their direct
    URLs survive a reload.
  - The 120-character name fits a phone with no overflow and was deleted
    afterwards.
- **An unknown release**: `/releases/nosuch-3716` reads `acme/widgets has
  no release named “nosuch-3716” -- showing 2.0-3716 instead…`.
- **Delete release**: dismissing the confirm keeps the release, and
  accepting it returns to `/repos/acme/widgets/releases`.
- **Phone, light and dark**: no overflow on `/releases/1.0`.

### Console and network audit
- Uncaught page errors: **0**.
- Every `Failed to load resource` matches a deliberate negative case:
  - 400s: the invalid issue source, invalid YAML, the invalid branch name
    and invalid release names.
  - 404s: the unknown compare ref, `github-branches` for the off-sim
    repo, and the bug-2 reload.
- The only failed requests were `GET /api/tasks` calls aborted by
  navigation (`net::ERR_ABORTED`).

## Bugs found and fixed

### 1. A failed issue-source listing read as "Read 0 open issues" (`ui/src/components/RepoPage.jsx`)
- **Repro**:
  1. On `grain demo -github-sim`, enable issue import for acme/widgets
     with the source `acme/missing-3716`.
  2. Click **Import issues now**.
- **Observed**: `Read 0 open issues on acme/missing-3716: imported 0, 0
  already imported. Some could not be imported: orchestrator: … listing
  open issues on acme/missing-3716: GitHub answered 404: Not Found`. That
  reads as a source with no issues and some per-issue failures.
  - The 11-03, 11-04, 11-07 and 11-08 runs all recorded this line without
    fixing it.
- **Fix**: `orchestrator.importIssues` returns an error with nothing open
  only when the listing itself failed. The UI now says `Could not read the
  open issues on <source>: <error>` in that case, and keeps the "Some
  could not be imported" wording for partial failures.
- **Coverage**:
  - Vitest: `says an import whose source could not be listed read
    nothing`. It fails on the base commit.
  - Playwright: `issue-import-walkthrough.spec.js` now imports from a
    missing source.

### 2. A compare URL with `#` in a branch lost the rest on reload (`ui/src/paths.js`, `ui/src/state.js`)
- **Repro**:
  1. Push a branch `fix/#3716-hash` to acme/widgets.
  2. On Branches, pick it as the head and click Compare. The page shows
     the comparison.
  3. Reload.
- **Observed**: the address was `/compare/main...fix/#3716-hash`. A
  reload, or a shared link, read `#3716-hash` as a fragment, compared
  `fix/` and showed `GitHub has nothing to compare for main...fix/`.
- **Fix**: `comparisonRefSegment` percent-encodes each ref with its `/`
  left literal. It is used by `paths.js` `comparisonRefs` and by
  `internalPullRequestUrl`. Parsing already decoded refs, so existing
  links are unchanged.
- **Coverage**:
  - Unit tests in `paths.test.js` (a round trip with `#` and `?`) and in
    `state.test.js`.
  - Playwright: `repo-compare-hash-ref.spec.js`, at desktop and phone. It
    creates the branch through the sim's git-refs API, picks it, reloads
    and re-opens the link. It fails on the base commit.

### 3. The CI capabilities picker refused every release-server method (`pkg/model`, `pkg/ui/capability_grants.go`)
- **Repro**: on `grain demo -github-sim`, open
  `/repos/acme/widgets/settings/ci` → CI capabilities → **Add grant…**.
- **Observed**: `release server › registry-push`, `package-publish` and
  `image-signing` are `aria-disabled`, each saying `…is handed only to a
  CI job of release, and this is a unnamed run`.
  - `GET /api/capability-methods?kind=ci&repo=acme/widgets` judged a CI
    run with no trigger event against each method's event-gated
    `CIGrantRule`.
  - `PUT .../ci-capabilities` accepts these grants, so the repository list
    that is meant to grant them could not be edited from the UI.
- **Fix**:
  - `model.CapabilitySubject.AnyEvent`, set only by that listing for
    `kind=ci`, skips the event-gated rule. Capability policy deny rules
    still apply.
  - Tasks are still refused these CI-only methods. Real CI jobs never set
    the flag, so enforcement is unchanged.
  - The event-less refusal now reads `this is a run with no trigger
    event`.
- **Coverage**:
  - Go: `TestCIGrantRuleNotJudgedForAnyEvent` in `pkg/model`, and
    `TestCapabilityMethodsForCIDoNotRefuseAnEventGatedMethod` in
    `pkg/ui`. The `pkg/ui` test fails on the base commit.
  - Playwright: `repo-ci-release-capability-grant.spec.js`, at desktop
    and phone. It fails on the base commit.

All three were re-verified in the browser on server C, built from this
branch. Their states fit both widths in both themes:
- The import outcome stays inside its section, in the error colour.
- No page overflows.

## Observations (not changed)
- The Advanced tab does not refresh a declaration read that finishes
  while it is open; it needs a reload to show the result.
- Declaration syncing switches off without a confirmation. This is
  reversible, and the copy says what it does.
- `Go to CI Mode` scrolls CI Mode into view but leaves focus on the
  button.
- The issue-import label box accepts a newline over the API, and the
  label is stored as typed.
- Promote current RC is enabled while qualification is still running.
  This matches the 11-08 run; it was not judged a bug here.
