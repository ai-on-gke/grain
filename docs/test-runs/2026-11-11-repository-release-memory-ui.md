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
