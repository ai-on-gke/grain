# Repository, release and memory UI walkthrough — 2026-10-12

Task: grain/eu/task-3212. An operator's walk through the repository-centric
screens (Repos list, repo page, branch comparison, repo settings and team
overrides, read/write repository relationships on filing, deployment and
repo memory, releases and their RC history) in a real browser against a
real `grain demo` server. It deliberately went past the scripted cases in
`ui/e2e` and past the 2026-10-11 walk of the same area
(`2026-10-11-repository-release-memory-ui.md`), whose fixes it rechecked.

## What was tested

- **Commit tested**: `13821bad67cb` (`candidate`, "Merge pull request
  #3513 from bwsalmon/grain/eu/task-3147"); the UI reported
  `v1.10.0-rc.1.0.20261012112416-13821bad67cb`. The fixes below were then
  re-verified on this branch against a rebuilt binary.
- **OS**: Debian GNU/Linux 12.15 (bookworm), amd64 sandbox, 4 vCPU, 7 GiB.
- **Toolchain**: Go 1.26.6, Node.js 20.19.0, Vite production build of
  `ui/` (`npm ci && npm run build`) embedded by `go build ./cmd/grain`.
- **Browser**: Playwright 1.62.1, Chromium headless shell 151.0.7922.34
  (`chromium-1234`); 1400×950 desktop, 390×844 narrow, and a
  `hasTouch`/`isMobile` 390×844 context for tap reordering.
- **Servers**: three throwaway `grain demo` processes, each over its own
  embedded SQLite store under a scratch `TMPDIR`:
  - `grain demo -addr 127.0.0.1:8787 -open=false` — the unfixed build, main
    walk and every reproduction;
  - `grain demo -addr 127.0.0.1:8788 -open=false -fake-ci -github-sim` —
    the unfixed build with simulated GitHub (branches `main`, `release/1.0`)
    for branches and releases;
  - `grain demo -addr 127.0.0.1:8789 -open=false` — the fixed build, to
    re-run each reproduction.
- **Session**: loopback-trusted operator `alice` (`grain.username` in
  localStorage, as `e2e/pageerrors.js` sets it) with the
  `X-Grain-Admin-Mode: on` header the Playwright suite sends.
- **Model / Gemini**: the sandbox carried no `/home/debian/.gemini-api-key`,
  so no model was involved. Every step was a scripted Playwright gesture on
  a visible control (click, fill, Tab/Shift+Tab/Enter/Space/Escape, drag,
  tap, browser Back/Forward, reload). On every page the scripts collected
  page errors, console errors and warnings, failed requests and 4xx/5xx
  responses.

## Stubs

Grain's own API, store and React UI were never replaced. Only these
boundaries, which the demo does not wire to anything real, were stubbed
or simulated:

| Boundary | How | Why | Responses exercised |
| --- | --- | --- | --- |
| GitHub compare (`ui.Config.RepoComparer`, surfaced as `GET /api/repos/acme/widgets/compare`) | Playwright `page.route` on that one URL | Neither demo wires a comparer, `-github-sim` included: both answer 404 "repository comparison is not available on this UI server" | success (one-file diff, merge base `abc1234d`), empty diff, merged, 502 with GitHub's error text, a held request (loading) |
| GitHub branch listing (`GET /api/repos/*/*/github-branches`) on the plain demo | Playwright `page.route` | The plain demo has no GitHub client (404 "GitHub branch listing is not available"); `-github-sim` answered `["main","release/1.0"]` for real and was used unstubbed | `["main","candidate","release/4.0"]`, `[]`, 502 |
| GitHub release reconciler | `ui/e2e/releasehelper` (`mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-promoted`, `mark-release-merged`, `mark-release-error`) against the `-github-sim` demo's store | The demo runs no reconciler, so nothing moves a release off Provisioning or a candidate off cutting | provisioning → active; rc.1 cutting → active → promoting → promoted; rc.2 cutting → active; merge requested → merged with PR link; release error |

The unstubbed answers were checked as well: compare's 404 message renders
in place of the diff, and with GitHub unreadable the New task base-branch
field neither warns nor refuses (the designed behaviour,
`base-branch-unreadable.spec.js`).

## Steps and outcomes

### Repos list

1. Opened **Repos** from the rail (`Repos 2` → `/t/default/repos`) and from
   `/repos` directly: `acme/widgets` and `grain/config-default`, header
   count 2, "This team allows only acme/widgets."; no console errors.
2. Added `acme/gadgets`, `zeta/alpha-tools`, `good/one` and
   `example-corporation/an-exceptionally-long-repository-service-name-for-layout-testing-purposes-xyz`;
   the header count followed each add. `Acme/Widgets` and `ACME/WIDGETS`
   (an existing repo in another case) added nothing and said nothing. `bad repo` was
   refused ("repo must be owner/name, got "bad repo"…") with the text
   kept in the box. The page-level error banner stays up for its 5 s
   timeout even after a later add succeeds. That is by design
   (`App.showError`), not stale state.
3. Search: `WIDGETS` (case-insensitive), `alpha` (substring), `zzz` ("No
   repos match your search."), `/` and empty (all rows).
4. Sort: custom order, name (a–z), active first, most tasks. Move handles
   appear only in custom order, and the choice survives reload.
5. Reorder:
   - Keyboard: focus a handle and press Enter. The "Moving 1 repo — choose
     where it goes" bar appears with labelled slots ("Move here, above
     acme/gadgets", …). The last row's slots are all above it, so they are
     reached with Shift+Tab, not Tab. Enter moves the row, focus returns
     to "Move zeta/alpha-tools", and the order survives reload. Space
     picks up too, and Escape cancels with focus kept on the handle.
   - Pointer: an HTML drag of the long-named row to the top persisted
     across reload.
   - Touch: in the touch context, tap the handle, then tap a slot. Moving
     `acme/widgets` to the end worked.
6. Layout: at 1400 px the long name is ellipsised in its row
   (`text-overflow: ellipsis`, full name in `title`). At 390 px the
   document does not scroll sideways (`scrollWidth` 390) on the list, the
   long-named repo's page, its releases pane or its compare page. The only
   boxes past the right edge were MUI's invisible notch `<legend>` spans for
   the two long field labels.

### Repo page, task count, filing

7. Clicked the `acme/widgets` row → `/t/default/repos/acme/widgets`. Back
   returned to the list and Forward to the page; "← Repos" also returned
   to the list.
8. "11 tasks filed here" matched the list row's "11 tasks". The 10-11 fix
   (standing Director session no longer counted) holds. The link opened
   `/t/default/tasks?f=repo:any:acme/widgets` with 11 rows, still 11 after
   reload, and Back returned to the repo page.
9. **New task** from `/repos/acme/widgets`: Repos was preselected to
   `acme/widgets`. Created task 13 (`repo: acme/widgets`, team `default`,
   capabilities `gcp-key, gemini-key, self-debug`). The drawer closed back
   onto the repo page, and `/tasks/13` showed the repo.
10. **New task** from `/repos/zeta/alpha-tools`: Repos preselected
    `zeta/alpha-tools`. Added write repo `acme/gadgets` (suggestion list,
    Enter) and a read-only repo through the keyboard.
    - Escape with the suggestion list already closed closed the drawer, as
      `RepoPicker` documents. Reopening restored the whole draft.
    - The task was created as 14 with `writes: [{repo: acme/gadgets}]`, and
      its detail page shows both repos.
    - The read-only entry was stored as `acme/wid`: ArrowDown had moved the
      highlight from `acme/widgets` to the "add acme/wid" row before Enter.
      That is the expected highlight behaviour, so this is not a bug.

### Branch rules and repo configuration

11. Branches (`-github-sim`): added `walk/1012` (listed "-- pending").
    Re-adding it said "walk/1012 is already on this repo's list (pending);
    nothing new was added." `release/1.0`, which already exists on the
    simulated GitHub, was adopted as pending. `bad..name` was refused
    ("invalid branch name"), and a blank name sent nothing.
12. Compare: Base defaulted to `main`, and Head took `release/1.0`. Compare
    opened `/repos/acme/widgets/compare/main...release/1.0`. A head-only
    deep link read "Into the default branch". With compare stubbed, all
    five states rendered: the README diff with "forked at abc1234d",
    "There are no changes between these branches.", "merged", GitHub's
    error text, and "Loading the comparison…".
13. New task base branch against the stubbed branch list: no note for a
    listed branch. An empty list said `No branch "candidate" on
    acme/widgets: push it first, or have the task depend on the one that
    cuts it`. A 502 said nothing, as designed.
14. On the repo page in team context (`default`), each of these was edited
    and read back after a reload:
    - CI capabilities: `gcp key`.
    - Default capabilities: `memory`. The effective line updated to "…starts
      with: Self debug, GCP key, Gemini key, Memory".
    - Team prompt extension. The banner flipped to "team-specific
      configuration overrides active".
    - Setup command: `make deps`.
    - Declarations: off → on.
    - "Require branches to be up to date": off → on.
    - Unfixable check: `cla/google`.
15. Deployment-wide layer: ticked "Edit deployment-wide defaults" and set a
    global prompt extension. It survived reload, and the team value stayed
    separate. "Revert to global defaults" asked "Revert team overrides for
    acme/widgets back to global repository defaults?". Declining kept the
    team text, and accepting showed the global text.
16. **Bug 1**: the team was named "default" here and "admin" everywhere
    else (see below).
17. **Bug 2**: the page had no main landmark (see below).
18. Keyboard: from "← Repos", Tab reaches the repo link, the task-count
    link, New task, Releases, Remove, the branch and compare fields,
    capability selects, the prompt and setup boxes, declarations, merge
    queue, CI mode, the check field, the merge-batch task links, Add
    workflow and the work-ownership toggles, in reading order.

### Leaving edits

19. **Bug 3**: browser Back dropped an edit still focused in a save-as-you-leave field
    (repo prompt extension, Team settings boxes); see below.
20. Observation, not fixed here: "Jobs dispatched after every merge" has an
    explicit Save, and an unsaved value is dropped without a word by Back,
    "← Repos" or a rail click. Proposed as a follow-up (see below).

### Memory

21. Team settings → Models shows:
    - "This deployment", with "Memory carried by every run" (271 of 8192
      characters).
    - Document `release-checklist` (5 days ago, memory agent, 332 B).
    - "acme/widgets" memory ("edited here").
    - Document `flaky-e2e`.
    - "Another repo" picker.
22. Opened `release-checklist (this deployment)` and appended "Correction
    10-12: confirm the tag exists before announcing." After Save document,
    left Team settings and reopened: the correction read back, and the
    chip said "written today".
23. Emptied "Memory about acme/widgets" and pressed Save. It asked "Forget
    everything grain has learned about acme/widgets? Saving an empty
    memory deletes it rather than storing a blank one." Declining left
    `/api/memory` unchanged. The 10-11 fix for saving an empty *document*
    is covered by `repository-release-memory-regressions-walkthrough.spec.js`,
    which passed.

### Releases and release history

24. **Releases** from the repo page (`-github-sim`) →
    `/repos/acme/widgets/releases`, which shows the empty-state alert.
    - Refused inline: `has space`, `../x`, `rel.rc.1`, `release/1.0` and
      `-dash`, each with the release-name rule. An empty name sent nothing.
    - Accepted: `candidate` and `main`. This is the known gap filed as a
      follow-up by the 10-11 walk; the reconciler refuses to provision or
      promote onto the default branch. Not re-filed.
    - Accepted: `1012`.
25. `1012`, through the helper and the pane:
    - Provisioning → Active.
    - Promote asked "Promote 1012.rc.1 to 1012? This moves acme/widgets's
      1012 branch to 1012.rc.1." Declining left it active; accepting set
      promoting, then promoted.
    - Cut new RC (no confirmation) gave `1012.rc.2`, cutting then active.
      History lists rc.2 above rc.1.
    - Merge was requested, then shown as Merged with a "pull request" link
      to `https://github.com/acme/widgets/pull/4242`. "Merge requested" is
      the right wording at Merged, which means the merge-back PR is open
      (`model.Release`).
    - The helper's release error rendered beside the branch names.
26. Delete release `main` asked "Remove release main? This drops the release
    and its candidates from grain." Declining kept it; accepting removed it
    and landed on the list.
    - The browser logs the DELETE as `net::ERR_ABORTED`, but the server
      answered 204 before the navigation. Timestamps: DELETE request at
      `…12425`, 204 at `…12431`, navigation at `…12446`. Harmless.
    - The old deep link and `/releases/main` land on the list with `no
      release "main"`.
    - `/repos/nope/nothing/releases` shows the off-allowlist refusal in
      place of the form.
27. Keyboard on a release: from "← acme/widgets", Tab reaches the name
    field, Create release, the release selector, the pull request link,
    Delete release, both qualification checkboxes, Add item and Save
    qualification plan.

Console and network: no page errors and no render failures. Every 4xx/5xx
was intentional: invalid repo, branch and release names, the demo's 404s
for compare and for branch listing without GitHub, and stubbed 502s. The
only `ERR_ABORTED` entries were `/api/tasks` polls cancelled by a
navigation, plus the DELETE in step 26.

## Bugs found

### 1. The repo page named the team by its id, not its name (fixed)

- **Reproduction**: `grain demo`. The top bar's Team switcher and the New
  task form ("In team admin") call the demo's team **admin**. Open
  `/repos/acme/widgets`: the page says "Team context: default",
  "Default capabilities for default", "Only for tasks default files…",
  "Prompt extension for acme/widgets (team default)" and "Setup command
  for acme/widgets (team default)". `GET /api/teams` shows `{"id":
  "default", "name": "admin"}`, so one team reads as two.
- **Fix**: `RepoPage` derives `teamLabel` (the team's `name` from
  `config.teams`, falling back to the id) and uses it in every sentence and
  label. API paths and capability lookups keep the id.
- **Coverage**: `RepoPage.test.jsx`, "names the team as the team switcher
  does, not by its id" (fails without the fix). e2e:
  `repository-release-memory-1012-walkthrough.spec.js`, "a repo page is the
  main landmark and names the team as the switcher does".
  `repo-team-overrides.spec.js`,
  `team_repo_default_capabilities_walkthrough.spec.js` and
  `team_repo_defaults_release_names.spec.js` now read the name.

### 2. The repo page had no main landmark (fixed)

- **Reproduction**: open `/repos/acme/widgets` and list landmarks. The page
  is a `<div class="main-column">`, so there is no `main`. The Repos list,
  the releases pane and the compare page beside it are each a `<main>`, so
  "skip to main content" lands nowhere on the one page with the most
  controls.
- **Fix**: the page's root is `<main className="main-column">`, which the
  task tree board already uses.
- **Coverage**: `RepoPage.test.jsx`, "is the page's main landmark" (fails
  without the fix), and the e2e test above.

### 3. Browser Back dropped an edit still in a field that saves as it is left (fixed)

- **Reproduction**: `grain demo`. Open Repos, click `acme/widgets`, type in
  "Prompt extension for acme/widgets (team …)", and press the browser's
  Back without clicking anywhere. Back to the repo: the box shows the
  inherited text again, and `GET /api/teams/default/repos/acme/widgets/config`
  has no `promptExtension`. The same happens in Team settings → Models →
  "Branch name template" when Back is pressed with the box focused.
  - Clicking anything else first saves the edit, because the click blurs
    the box.
  - Back, Alt+Left, a mouse's back button and a phone's back gesture
    unmount the field with focus still inside, so its `onBlur` never runs.
- **Fix**: App's `popstate` handler blurs the focused element first, while
  the page it belongs to is still mounted. That commits a save-as-you-leave
  field the way any other exit does. Panes guarded by
  `unsavedEdits.js` still ask as before (`unsaved-edits-guard.spec.js`
  passes).
- **Coverage**: `repository-release-memory-1012-walkthrough.spec.js`, "Back
  with an edit still in a repo's prompt extension saves it". It failed
  against a build without the fix (`promptExtension` undefined) and passes
  with it. The Team settings case was re-run by hand against the fixed
  server: the template read back after Back.

## Follow-up proposed

- **An unsaved "Jobs dispatched after every merge" value is dropped on
  leaving the repo page.** It has its own Save button. The repo page is not
  an overlay, so nothing asks before Back, "← Repos" or a rail item drops
  a typed value. A guard would need App's non-modal navigation (rail
  buttons, `closeRepoPage`) to consult `leavingLosesEdits()`, which is
  wider than this change. Proposed as a separate task.

## Tests run

- `npx vitest run src/components/RepoPage.test.jsx`: 77 passed. The two new
  tests fail against the unfixed component.
- `npx vitest run` (whole UI suite): see the commit's CI.
- `npx playwright test e2e/repository-release-memory-1012-walkthrough.spec.js
  e2e/repo-team-overrides.spec.js e2e/team_repo_default_capabilities_walkthrough.spec.js
  e2e/team_repo_defaults_release_names.spec.js
  e2e/repository-release-memory-regressions-walkthrough.spec.js
  e2e/repo-pane.spec.js e2e/repos_workflows.spec.js
  e2e/unsaved-edits-guard.spec.js --project=chromium`: 19 passed.
- No screenshots or browser artifacts are committed. The exploration
  scripts lived in an uncommitted scratch directory (`/tmp/explore`).

## Sandbox notes

- `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.
- `go build ./cmd/grain` embeds whatever `pkg/ui/static` holds at that
  moment, so rebuild the frontend first after changing `ui/src`.
- A Playwright `page.goto` between two app URLs is a full document load,
  so a following `goBack()` unloads the document rather than firing
  `popstate`. Reproducing Bug 3 needs an in-app navigation (clicking the
  repo row) before Back.
