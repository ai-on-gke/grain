# 2026-11-11 — Repository, merge queue, compare and release UI walkthrough (task 4300)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/098073e4/task-4300-walk-through-repository-merge`.
- **Commit tested**: `773b7bc62` (`main` at the start of the run; build stamp
  `v0.0.0-20261111152023-773b7bc62c7c · 773b7bc · 2026-11-11 15:20Z`). The two
  fixes below were re-verified on this branch's build (`grain-bin3`).
- **Build**:
  - Frontend: `cd ui && npm ci && npm run build`, the real Vite bundle
    embedded in the binary.
  - Server: `go build -tags grain_e2e ./cmd/grain`.
- **Browser**: Playwright headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `timezoneId: UTC`,
  driven by throwaway node scripts outside `ui/e2e` through the visible
  controls: rail, rows, tabs, buttons, menus, text boxes, switches and the
  keyboard. Viewports 1280×900 and 390×844, light and dark.
- **Gemini**: there is no `/home/debian/.gemini-api-key` in this sandbox, so
  nothing was Gemini-backed and `LOOK=1` was unavailable. The file reader
  here shows PNGs as bytes, so layout was checked with DOM geometry
  (bounding boxes, `scrollWidth`, element counts) rather than by eye.
- **Servers** (each a real `grain demo -open=false -access-code-file …` over
  its own throwaway embedded store, signed in through
  `POST /api/auth/access-code` as `alice@example.com`):

| Demo | Port | Build | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 4300 | base | `-github-sim -reconcile=1s -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification` | everything except the rows below |
| B | 4301 | base | `-github-sim -reconcile=1s` | CI mode switching and unjudged base-gate warnings |
| C | 4302 | base | `-github-sim -qualification -external-config-repo=https://github.com/grain/config.git` | config-repo read-only rendering (bug 1 found here) |
| D | 4303 | fixed | `-github-sim -fake-ci -scheduled-workflows -reconcile=1s` | re-verifying bug 2 |
| E | 4304 | fixed | as C | re-verifying bug 1 |

## Stubs and external-boundary helpers
Grain's own API and React UI were never mocked or intercepted; no
`page.route` was used.
- **GitHub**: the in-process GitHub simulation (`-github-sim`) over a real
  bare repository stood in for GitHub: branches, compare, issue listing and
  import, release branches, RC cut and promote.
- **Seeding the simulated repository**:
  - Issues through the demo's loopback-only
    `POST /api/e2e/github-sim/repos/acme/widgets/issues`: two labelled
    `walk-4300`, one unlabelled.
  - `git push` into `/tmp/grain-demo-*/githubsim/acme/widgets.git` of the
    branches `feature/walk-4300` and `fix/#4300-hash` (one commit:
    `WALK4300.md`, `pkg/walk.go`), and of `.grain/templates/docs-sync.yml`
    (with `push:` refs), `.grain/triggers/docs-gate.yml` (first without a
    `template:`, deliberately broken, then fixed) and `.grain/settings.yml`
    (`prompt-extension`, `setup-command`, `port-branches: [main,
    release/1.0]`, `port-types` including the unpaired `main->nonexistent`).
- **Network throttling** (CDP `Network.emulateNetworkConditions`, 1.5 s
  latency) to hold an Add repo in flight long enough to observe. Nothing
  was replaced.
- **The external config repo on demos C/E** is the demo's own stand-in: it
  names a repository and has no working tree, so
  `GET /api/state-repo/files` answers 500 by design
  (`pkg/demo/demo_external_config.go`). The UI swallows it and lists every
  config path.

## Steps and outcomes

### Repo list (`/repos`)
- The rail's `repos` button and the direct URL both open `/repos`. The rail
  count follows the number of repos.
- `acme/widgets` reads `blocked 1, proposed 3, waiting 3, running 2, asked
  4, merging 3, deferred 1, 17 tasks`.
- **Add repo**:
  - Under 1.5 s latency the picker is disabled and the button is
    `aria-busy` while the add is in flight, and both come back afterwards.
  - On a phone (390 px), light and dark, Add repo sits at x 303–374 both at
    rest and in flight, with no horizontal overflow.
  - `not a repo` is refused with `repo must be owner/name, got "not a
    repo"…`. `acme/gadgets` and a 70-character
    `acme-observability-platform/an-extraordinarily-long-repository-name-4300`
    were added and survived a reload.
- **Filter and sort**: `gadg` narrows to `acme/gadgets`, and `zzz` reads
  `No repos match your search.` The sort menu offers custom order, name
  (a–z), active first and most tasks. Most tasks puts `acme/widgets` first
  and removes the drag handles. Custom order brings back three `Move <repo>`
  handles.
- **Reordering**:
  - Keyboard: focus a handle, press Enter, and `Moving 1 repo — choose
    where it goes` appears with two `move here` slots. Enter on a slot
    moves the row, and the order survived a reload.
  - Pointer: a drag of the `acme/widgets` handle kept it on top.
  - Escape cancels a pick-up and leaves no slots.
  - The custom order is stored per browser, as `RepoList.jsx` documents; a
    fresh browser context starts alphabetical.
- **Phone layout**: the long name is truncated (`scrollWidth >
  clientWidth`) within x 83–227, and on the busy row `blocked 1` is the
  only chip shown, right after the name (x 241–312).

### Opening a repo, its task count and filing
- A row opens `/repos/acme/gadgets`. Back returns to `/repos`, and Forward
  returns to the repo.
- **Filing from the repo page**: `New task` on `/repos/acme/gadgets`
  preselected `acme/gadgets` twice; tasks 25 and 26 were filed with
  `repo: acme/gadgets`. Task 27 was filed from the global `+ New task`
  with the repo typed as `ACME/Gadgets`. All three appear under
  `/?f=repo:any:acme/gadgets`, so repo matching is case-insensitive.
  `/tasks/25` still names `acme/gadgets` after a reload.
- The count link `3 tasks filed here` opens `/?f=repo:any:acme/gadgets`.
  Back and Forward step between the two pages.
- **Removed repository**:
  - Task 26 was closed through its task page.
  - Remove repository on `acme/gadgets` asked: `Remove acme/gadgets from
    target repos? … acme/gadgets comes off this deployment's target repo
    list, so 2 tasks still in play there are parked instead of
    dispatched.` Dismissing kept the repo; accepting returned to `/repos`
    without the row.
  - `/repos/acme/gadgets`, and `/repos/ACME/GADGETS` too, still reads
    `waiting 2, closed 1` and `3 tasks still target this repo, which is no
    longer on the target list`, closed work included. That link lists all
    three tasks.

### Tabs, URL, reload, Back/Forward, Settings → Repositories
- Each of `ci`, `merge-queue`, `agents`, `branches`, `releases` and
  `advanced` sets `/repos/acme/widgets/settings/<tab>`. Overview is the
  bare path. A reload keeps the selected tab, and
  `/settings/nosuchtab` opens Overview. No tab overflows.
- Tab switches replace the history entry, so Back from Agents leaves the
  repo page (as `App.jsx` documents) and Forward returns to `agents`.
- **Keyboard**: ArrowRight moves focus along the tab strip, and Enter opens
  the focused tab.
- **Phone**, light and dark: the selected tab is scrolled into view
  (`advanced` x 242–334, `merge queue` 219–334, `ci` 146–236), with no
  overflow.
- **Settings → Repositories**: one row per repository showing its base
  branch, declarations, skills, CI mode and git connection, each a link to
  the tab that changes it. For example `acme/widgets  main  Read  Default
  folders  Grain CI  Not listed`.

### Overview, CI and capabilities
- **Overview**: the summary cards read CI gate, Up to date, Merge when
  green, Issue import, Base repair bypass and Branches.
- **Merge re-checks panel**:
  - It shows `1 change on main`, the `testing` chip and `12m of 90m`
    (`25m of 90m` later), with the ref and SHA, the task link `/tasks/5`
    and PR `#101`.
  - `show the run` opens the CI-run overlay with Cancel run, and Escape
    closes it.
  - Task 5's page shows the same panel.
- **CI capabilities**: the Add grant… menu lists capability-server methods,
  including `release server › image-signing / package-publish /
  registry-push`, all enabled for acme/widgets.
  - Picking `registry-push` puts it in the CI capabilities select above, as
    a `registry push` chip (the release server's methods are that select's
    by design). `grain › health` lands in CI grants.
  - Both survived a reload and were removed again.
  - Unready capabilities (`image-signing`, `sandbox-repo`) are offered only
    once ticked, as `configuredCapabilities` documents.

### CI mode and base-branch gate warnings (demo B)
- **GitHub CI with Watch default branch ticked**:
  - The error alert `Nothing is testing acme/widgets's default branch, and
    the merge queue is merging onto it anyway…` appears on CI, Overview
    (`GitHub CI — base branch not judged`) and Releases.
  - `go to ci mode` switches to the CI tab with CI Mode in view (y = 84).
- **Switching modes**: grain CI clears the warning everywhere without a
  reload (`Grain CI`). No CI raises it again on CI and Releases, and grain
  CI clears it again, still cleared after a reload.
- **Work ownership**: switching the repository default off and back on left
  the gate warning unchanged. Standing work does not cover the merge queue
  for hand-filed work, which the pane's own copy says.

### Merge queue
- `Require branches to be up to date` and `Merge … when green and clean`
  both persisted across a reload. The Merge when green copy says the queue
  no longer keeps an order and that the box changes nothing; it makes no
  ordering claim.
- **Base-branch repair bypass**:
  - Turning it on asks `Let base-branch repair PRs in acme/widgets merge
    without a human when their CI is green? …`. Dismissing leaves it off;
    accepting turns it on, which persisted.
  - Turning it off asks nothing.
  - Settings → Repositories behaves the same: confirm on, none off, and the
    state persisted.
- **Unfixable checks**:
  - Add is disabled while the box is blank or whitespace.
  - `grain CI` is refused (`grain CI judges one exact commit…`), and
    `CLA/ACME-4300` is refused as a case-insensitive duplicate.
  - `cla/acme-4300` persisted, and was removed by focusing its chip and
    pressing Delete.

### Agents
- **Upstream repository**:
  - `not a repo` is refused (`upstreamRepo: repo must be owner/name…`) and
    the box reverts.
  - A different valid value saved and the refusal banner was gone within
    0.7 s.
  - `acme/upstream-4300` reads `Pull requests for tasks targeting
    acme/widgets open against acme/upstream-4300.` after a reload; it was
    cleared afterwards.
- **Prompt extension and setup command** save on blur and persisted.
- **Read-only layers**: once `.grain/settings.yml` was read, the
  declared prompt (`Repo says: keep commits small (4300).`) and setup
  command (`make init-4300`) render as read-only layers beside the
  deployment layer (`nothing`).
- **Leaving with unsaved edits**:
  - An unsaved issue-import label or source asks `You have unsaved changes
    here. Leave and discard them?`. Dismissing stays on the page.
  - Upstream, prompt and setup save on blur when a rail click leaves, so
    nothing is asked and nothing is lost (the `blurSaving` design). A
    reload or closed tab still gets the `beforeunload` prompt.
- **Issue import**:
  - Enabling it saves at once. Queue at once carries its trust sentence
    inline (`Turn it on only if you trust everybody who can open an issue
    on acme/widgets…`) and asks no confirmation.
  - The source `acme/does-not-exist-4300` reports `Could not read the open
    issues on acme/does-not-exist-4300: … GitHub answered 404`, so it says
    it read nothing.
  - Intervals `-5` and `0` are blocked by the box's native `min`; 30 and 15
    saved.
  - The label `walk-4300` gave `Read 2 open issues on acme/widgets:
    imported 2, 0 already imported.`, and again `imported 0, 2 already
    imported.`
  - Label, interval and the enabled switch persisted across a reload.

### Branches and Compare
- The head picker lists the repository's own branches. The list is capped
  at 8 suggestions, so `main` and `release/1.0` appear only once typed.
- **`main...fix/#4300-hash`**: picked from the menu, it opens
  `/repos/acme/widgets/compare/main...fix/%234300-hash` and shows
  `WALK4300.md +1` and `pkg/walk.go +3`. It survives a reload, and Back
  returns to Branches.
- **Other links**:
  - The direct link `main...feature/walk-4300` works.
  - An unknown ref reads `GitHub has nothing to compare for
    main...nosuch-4300…`, with no overflow.
  - `/compare/main` compares main with itself (`There are no changes
    between these branches`), as `paths.js` documents for a head-only
    link.
- **Add branch**: `bad name 4300` is refused with the branch-name rule.
  `walk/added-4300` is added as pending, and again gives `…is already on
  this repo's list (pending); nothing new was added.`

### Advanced
- **The broken trigger** showed `Last read failed: … docs-gate.yml: a
  trigger needs a template: to file — still running what ad07a70282e1
  declared.`
- **After the fix**:
  - `Read at 22fa62d3341b: 1 template(s), 0 suite(s), 0 schedule(s), 1
    trigger(s)`.
  - `Path triggers: .grain/triggers/docs-gate.yml`.
  - `Tasks from .grain/templates/docs-sync.yml may also push:
    refs/heads/docs-sync/*, refs/tags/docs-* (force)`.
  - A read-only Ports table: `main->nonexistent` → `nothing (not a paired
    branch)`, `main->release/1.0` → `fix, docs`, `release/1.0->main` →
    `every merge`.
  - The port-types warning was drawn twice: **bug 2**.
- **Remove repository**: covered under "Removed repository" above.
- **Config-repo refusal**: on demo C the config-repo row offers no Remove;
  the refusal and its copy are covered by
  `repo-release-plan-config-repo.spec.js`, rerun green.

### Config-repo-locked and role-restricted settings (demo C)
- Every control on CI, Merge queue, Agents and Advanced is disabled, under
  `acme/widgets's settings live in the config repo and are read-only here.
  Change them with a config pull request, or ask the Director to.`, with
  links to `tables/repo_config.json` and `settings/capabilities.yml`.
- Branches (Add branch, Compare) and Releases' `run release by hand` stay
  live. They are actions, not settings.
- The qualification plan is read-only under its own notice, which read
  `acme/widgets's qualification plan live in the config repo and are
  read-only here. Change them …`: **bug 1**.
- Role restriction was covered by rerunning `role-gated-actions.spec.js`
  (green) rather than re-walked.

### Releases and work ownership
- **Work ownership**:
  - Repository default off reads `standing work on this deployment: off`.
  - `Release: on` reads `on, set here` / `runs` while the default is off,
    and `Release Qualification…: off` reads `off, set here`.
  - These survived a reload, and everything was reset to `say nothing` and
    on.
- **Creating releases**:
  - `bad name` and `-lead` are refused inline with the release-name rule
    (`aria-invalid`, helper text), and `1.0` with `this repo already has an
    unmerged release with this name`. None of these became a toast.
  - Typing in the box clears the error.
  - `2.0-walk4300` was created and opened at
    `/repos/acme/widgets/releases/2.0-walk4300`, which survived a reload.
- **An unknown release**: `/releases/nosuch-4300` reads `acme/widgets has no
  release named “nosuch-4300” -- showing 2.0-walk4300 instead…`.
- **Release 1.0**:
  - `Cut new RC` was disabled with `Cut new RC: 1.0.rc.1 is active: a new
    RC is cut once it is promoted.`
  - `approve all` moved qualification from pending approval to running.
  - **Promote**: the confirm reads `Promote 1.0.rc.1 to 1.0? This moves
    acme/widgets's 1.0 branch to 1.0.rc.1.` Dismissing changes nothing.
    Accepting gives `1.0.rc.1 -- promoted`, with `Promote current RC:
    1.0.rc.1 is already promoted: a candidate is promoted once.`
  - **Cut**: the next RC (`1.0.rc.2`, later `1.0.rc.3`) shows `cutting`,
    then `active` within 2 s, without a reload. The history lists every
    candidate.
- **Qualification plan**:
  - With an unsaved repeat, both the page's `← acme/widgets` and the rail
    ask before leaving.
  - Saving persisted `3` across a reload.
- **Delete release**: the confirm reads `Remove release 2.0-walk4300? This
  drops the release and its candidates from grain.` Dismissing keeps the
  release; accepting returns to `/releases`. The deleted link then falls
  back to `1.0` with the explanation.

### Console and network audit
- **Uncaught page errors**: 0.
- **Every 4xx/5xx** is a deliberate negative case:
  - 400s: invalid repo, upstream, release and branch names.
  - 404s: the unknown compare ref, `github-branches` for repos the sim does
    not serve, and the missing issue source.
  - 500s: `/api/state-repo/files` on the external-config demo, described
    above.
- **Failed requests**: only `GET /api/tasks` reported as `net::ERR_ABORTED`
  every 3 s. These are the poll's `If-None-Match` revalidations, which the
  server answers `304 Not Modified` (checked with curl). Chromium reports
  them to Playwright as aborted, but the poll itself completes.

## Bugs found and fixed

### 1. The config-repo notice on a qualification plan was ungrammatical (`ui/src/components/ConfigRepoLink.jsx`)
- **Repro**:
  1. Run `grain demo -github-sim -qualification
     -external-config-repo=https://github.com/grain/config.git`.
  2. Open `/repos/acme/widgets/releases`.
- **Observed**: `acme/widgets's qualification plan live in the config repo
  and are read-only here. Change them with a config pull request…`.
  `ConfigManagedNotice` always conjugated for a plural subject.
- **Fix**: a `singular` prop switches the sentence to `lives … is …
  Change it`, and `RepoReleases` passes it. The other callers name plural
  subjects and read as before.
- **Coverage**:
  - Vitest: `ConfigRepoLink.test.jsx` ("agrees with a plural subject by
    default and a singular one when asked") and an assertion in
    `RepoReleases.test.jsx`'s config-repo test.
  - Playwright: `repo-release-plan-config-repo.spec.js` now checks the
    sentence, at desktop and phone.
- **Verified** on demo E at 1280 and 390 px, light and dark, with no
  overflow.

### 2. A declared port-types warning was drawn twice on the Advanced tab (`ui/src/components/RepoPage.jsx`)
- **Repro**:
  1. On `grain demo -github-sim -fake-ci -scheduled-workflows`, push a
     `.grain/settings.yml` with `port-branches: [main, release/1.0]` and
     `port-types: {main->nonexistent: [fix]}`.
  2. Open `/repos/acme/widgets/settings/advanced` once it has been read.
- **Observed**: `Warning: port-types in .grain/settings.yml:
  main->nonexistent names nonexistent, which is not a paired branch…`
  appeared under Declarations and again under the Ports table.
  - `ui.Client.RepoDefaults` appends the repository's own port-types
    findings to the declarations status. The Ports table drew every
    `portWarnings` entry, and its code comment wrongly said the status did
    not show them.
- **Fix**: the Ports table draws only the `portWarnings` the declarations
  status has not already said. The operator's findings still appear there,
  and the repository's own appear once, beside the read they come from.
- **Coverage**:
  - New `repo-port-warning-once.spec.js` (desktop and phone). It fails on
    the base bundle with `Expected: 1, Received: 2` and passes with the
    fix.
  - `port-settings-walkthrough.spec.js` asserted the duplicated state; it
    now checks that each finding is said exactly once.
  - `sessions-worktree-pinned-nav-walkthrough.spec.js` ("Every scenario's
    declarations together") also asserted the duplicated state (2 in the
    Ports box, 4 on the page). That broke `ui e2e` on this branch's pull
    request after the first merge of main. It now expects 0 in the Ports
    box and 2 on the page. The test came in with the tree import and was
    not a flake.
- **Verified** on demo D at 1280 and 390 px, light and dark: 1 warning
  (base demo A: 2), with no overflow.

Rerun on the fixed build, all green (86 tests): every `repo-*.spec.js` and
`repository*.spec.js`, `repo_release_memory_edits_walkthrough`,
`role-gated-actions`, `base-branch-gate-warnings-walkthrough`,
`issue-import-walkthrough`, `release-unknown-url`,
`release_name_validation_walkthrough` and `port-settings-walkthrough`.
Vitest `RepoPage`, `RepoReleases` and `ConfigRepoLink` also pass.

## Observations (not changed)
- **Settings → Repositories still lists a removed repository** that has
  tasks (`acme/gadgets  Default branch  Not read  …  GitHub CI`). It uses
  the wide `knownRepos` list, which includes task repos, while `/repos`
  drops it. This is consistent with the code's comments, but the two pages
  disagree.
- **Picking a release-server method from CI grants' Add grant… lands it in
  the CI capabilities select above** rather than in the grants list it was
  picked from. This is by design (`inCIPicker`), but it surprised this
  walkthrough.
- **The branch menu is capped at 8 suggestions**, so on a repo with RC
  branches `main` and `release/1.0` are reachable only by typing.
- **A refusal banner stays up to its 5-second timeout** when the box is
  then set back to its already-stored value, because no save is made to
  settle it. A different valid value clears it at once.
- **Deleting a release leaves its branches** (`2.0-walk4300.latest`,
  `.rc.1`) on the repository. The confirm says "from grain", which is
  accurate.
- **`Go to CI Mode` leaves focus on the button**, as noted on 11-09.

---

Two tasks walked this area on the same day: task 4300 (above) and task 3933
(below). Their reports share this file because both were asked for this
name. Each one records its own commit, servers and findings.

## 2026-11-11: Repository, merge queue, compare and release UI walkthrough (task 3933)

### Environment
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

### Stubs and external-boundary helpers
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

### Steps and outcomes

#### 1. Repos list
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

#### 2. Repo page, task counts and filing a task
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

#### 3. Repo tabs and Settings → Repositories
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

#### 4. CI, merge queue and base-gate warnings
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

#### 5. Agents tab
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

#### 6. Branches and compare
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

#### 7. Releases
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

#### 8. Console and network
- Every `401` is the first unauthenticated load before sign-in.
- Every other console error is the browser's line for a deliberate 400 or
  404 listed above:
  - release names, branch name and upstream;
  - unknown compare ref;
  - `github-branches` on the removed repo, which has no credential.
- No uncaught page error and no 5xx.
- The only failed requests were `GET /api/tasks` and
  `GET /api/tasks/25?…` polls aborted by navigation.

### Bugs found and fixed

#### 1. A refused branch name was a toast that went while the name stayed (`ui/src/components/RepoPage.jsx`)
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

### Observations (not changed)
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
