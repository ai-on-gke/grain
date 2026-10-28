# 2026-10-26 Repository, Merge Queue, and Release UI Walkthrough

A walkthrough of the repository-centric operator workflow in a real headless Chromium browser against a real `grain demo` server over an embedded SQLite store.

## Environment

- **Commit tested**: `80f042b219c82c6a4777c9b506c5992d28d01d35` (`main` base commit for `grain/049f1e09/task-1146`), then re-verified with the fix on this branch.
- **Platform**: Linux 6.1.0-45-cloud-amd64 (`x86_64`), Go 1.26.2, Node.js v20.19.0, Playwright 1.58.2 driving Chromium (`chrome-headless-shell` 145.0.7632.6 from `/var/cache/playwright`).
- **Build**:
  ```bash
  find pkg/ui/static -mindepth 1 -not -name '.gitignore' -not -name '.gitkeep' -not -name 'placeholder.html' -delete
  cd ui && npm run build
  cd .. && go build -o /var/tmp/grain ./google/cmd/grain
  ```
- **Server**:
  ```bash
  /var/tmp/grain demo \
    -addr 127.0.0.1:8433 \
    -open=false \
    -access-code-file <temp-code-file> \
    -github-sim \
    -fake-ci \
    -scheduled-workflows \
    -base-branch-ci \
    -secrets-store \
    -merge-rules \
    -merge-queue-states \
    -qualification \
    -reconcile=500ms
  ```
- **Browser & Viewports**:
  - Desktop (`1280x800`) and mobile (`390x844`) viewports in Playwright Chromium.
  - Monitored `console` (`error` and `warning`), `pageerror`, `requestfailed`, `dialog`, and HTTP responses with status `>= 400`.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this sandbox; visual and layout checks were performed directly in Chromium together with bounding-box/scroll-width measurements and PNG inspection.

## Stubs and External Boundaries

Neither Grain's HTTP API nor its React UI was stubbed or intercepted in the browser. All external boundaries were served by `grain demo`'s built-in simulation flags:

1. **`-github-sim` (`pkg/github/githubsim`)**: Served GitHub branches, pull requests, diffs, and release branch cuts/promotions over a real bare git repository on disk (`/tmp/grain-demo-*/githubsim/acme/widgets.git`). To exercise branch comparisons with a real multi-file diff, `feature/compare-me` was pushed to that bare repository over `file://` (adding `src/compare_test.go` and modifying `README.md`).
2. **`-fake-ci` & `-base-branch-ci`**: Recorded Grain CI and base-branch runs in the store without requiring nested sandbox VMs.
3. **`-secrets-store`, `-merge-rules`, `-merge-queue-states`, `-qualification`, `-reconcile=500ms`**: Seeded an in-memory secret store (`NPM_TOKEN`), per-repo merge rules, a 2-PR in-flight merge batch on `acme/widgets` (`#5` and `#6`), a `1.0` (`1.0.rc.1`) release with qualification tasks (`#17` and `#18`), and a 500ms reconcile loop for branch creation and release-candidate transitions.

---

## Walkthrough Steps and Outcomes

### 1. Repos List (`/repos`)

- **Navigation**: Opened `/repos` by clicking `Repos` (`Repos 1`) in the left navigation rail and by loading `/repos` directly. Both rendered the `Repositories` page with `acme/widgets` (`17 tasks · 3 active`, `Remove` button), the allowlist notice (`An allowlist is in effect: 1 repo(s) set on Settings.`), and the GitHub App discovery caption (`Discovery is off: no GitHub App credentials are configured...`).
- **Adding repositories & custom git host**:
  - Ticked `On a custom git host` in the add-repo form; verified the `Clone URL` field (`https://git.example.com/owner/name.git`) appeared and hid again when unchecked.
  - Added `acme/gadgets` and an 82-character repository `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`. Verified the rail count updated to `Repos 3` and the long repository name wrapped cleanly inside `.repo-row` without horizontal overflow (`scrollWidth <= clientWidth` at both `1280x800` and `390x844`).
- **Search and sort**:
  - Filtered by `GADG` (case-insensitive match on `acme/gadgets`, `Clear` button visible) and `zzz-no-match` (`No repositories match "zzz-no-match".`).
  - Cycled `Sort repositories` through `Name (A–Z)`, `Active first`, `Most tasks`, and `Custom order`. Verified drag handles are disabled (`cursor: default`) when sorted by a non-custom order and active (`cursor: grab`) under `Custom order`.
- **Reordering (tap, keyboard, and pointer drag)**:
  - Exercised tap-to-select + tap-destination reorder, keyboard `Space` -> `ArrowUp`/`ArrowDown` -> `Enter` reorder, `Escape` cancellation (`Reorder cancelled.` live announcement), and pointer drag (`mousedown` -> `mousemove` -> `mouseup`), each sending `PUT /api/settings` and persisting across full page reloads.

### 2. Repo Row Navigation and Filtered Task List

- Clicked `acme/widgets` on `/repos` -> navigated to `/repos/acme/widgets`.
- Clicked `← Repos` -> returned to `/repos` and verified focus was restored directly to the `acme/widgets` row link (`document.activeElement` href `/repos/acme/widgets`).
- Tested browser `Back` and `Forward` between `/repos` and `/repos/acme/widgets`.
- On `/repos/acme/widgets`, clicked `17 tasks filed here` -> navigated to `/?f=repo:any:acme/widgets` with `17` task rows rendered.
- Used browser `Back` -> returned cleanly to `/repos/acme/widgets`.

### 3. Filing a Task from a Repository Page

- Opened `/repos/acme/gadgets` (`0 tasks filed here`) and clicked `New task` -> opened `/new` with `Repository` (`#new-task-repo`) preselected to `acme/gadgets`.
- Filled `Title` (`Gadgets walkthrough task 1146`) and `Description`, then reloaded `/new` -> verified `sessionStorage` draft persistence kept the title, description, and preselected repository `acme/gadgets`.
- Expanded `Other repositories this task touches`, added `acme/widgets` under `May also push to (writes)` (default branch `main`) and the 82-character repo under `May also read (reads)`, and clicked `Create Task`.
- Verified navigation returned to `/repos/acme/gadgets` and the header count updated to `1 task filed here` (persisting across reload).

### 4. Repository Settings Tabs (`/repos/acme/widgets`)

- **Keyboard tab navigation**: Focused the `Overview` tab and used `ArrowRight`, `End`, `Home`, and `Enter` to switch across tabs (`Overview`, `CI`, `Agents`, `Merge queue`, `Branches`, `Compare`, `Releases`, `Advanced`).
- **Overview (`/repos/acme/widgets`)**:
  - Verified the 5 summary cards (`CI gate: Grain CI`, `Up to date before merge: Not required`, `Merge when green: Off`, `Base repair bypass: Off`, `Branches: 0`), clicking each card into its corresponding settings tab, the `MergeBatchPanel` (`2 changes on main`), and `RepoRuns` (`Grain CI runs`).
- **CI (`/repos/acme/widgets/settings/ci`)**:
  - Verified `CI Mode` radio group (`Use Grain CI`, `Use GitHub CI`, `No CI wait`), `Watch acme/widgets's default branch with grain CI`, and `Capabilities for grain CI runs` (toggled `Package publish`, verified `PUT /api/repos/acme/widgets/ci-capabilities` and reload persistence).
  - Clicked `Add workflow`: verified the unsaved-changes confirmation dialog (`You have unsaved changes here. Leave and discard them?`) blocked `← Repos` when dismissed.
  - Tested invalid workflow YAML (`name: [unclosed`) -> server returned `400 Bad Request` (`yaml: line 1: did not find expected ',' or ']'`) and displayed the error banner while keeping the editor open.
  - Saved valid `.grain/workflows/lint1146.yml` and reloaded `/repos/acme/widgets/settings/ci` -> persisted.
- **Agents (`/repos/acme/widgets/settings/agents`)**:
  - Toggled repo default capability `Memory` (`memory`) -> verified `PUT /api/repos/acme/widgets/capabilities` and the effective union summary (`A task filed against acme/widgets starts with: Self debug, GCP key, Gemini key, Memory`).
  - Edited `Prompt extension` (`Run make lint before pushing (task 1146).`) and `Setup command` (`make deps`), blurred each field to trigger auto-save (`PUT /api/repos/acme/widgets/prompt-extension` and `PUT /api/repos/acme/widgets/setup-command`), reloaded, and verified both values persisted.
- **Branches (`/repos/acme/widgets/settings/branches`)**:
  - Created `task-1146/operator-branch` from base `main` -> verified `pending` -> `created` reconciliation against the simulated GitHub bare repo.
  - Re-submitted `task-1146/operator-branch` -> verified idempotent status message (`task-1146/operator-branch is already on this repo's list (pending); nothing new was added.`).
  - Submitted invalid branch name `bad branch..name` -> verified `400 Bad Request` refusal (`invalid branch name "bad branch..name": use letters, digits, ".", "_", "-" and "/", starting with a letter or digit; a name cannot contain ".." or "//" or end in "/"`).
- **Releases Settings (`/repos/acme/widgets/settings/releases`)**:
  - Exercised `RepoWorkOwnership` overrides (`Reconcile releases`, `Run qualification`, `Triage failed releases`), setting `Triage failed releases` to `on` and back to `say nothing` (`DELETE /api/repos/acme/widgets/work-ownership`).
  - Clicked `Turn off automatic releases` -> verified alert (`Automatic releases are off for this repository...`).
  - Edited `Jobs that judge base branches` (`go, lint`), verified the unsaved-changes guard on tab navigation, clicked `Save jobs`, and edited/saved `Jobs dispatched after every merge` (`Save dispatched jobs`).
  - Clicked `Run the base-branch check now` -> verified the resulting run notice (`The merge queue still checks this repository's base branches: go, lint runs on push after every merge, and nothing is published.`).
  - Added custom release secret `NPM_TOKEN` with value `npm-secret-val-1146`, verified the `Set` chip, reloaded `/repos/acme/widgets/settings/releases`, and verified all settings persisted.
- **Advanced (`/repos/acme/widgets/settings/advanced`)**:
  - Toggled `Read acme/widgets's own declarations` off and back on, verifying `Read at 7a8baa4ed1b9: 0 template(s), 0 suite(s), 0 schedule(s), 0 trigger(s)`.
  - Verified the read-only `Paired branches (porting)` guidance and the `Remove` repository confirmation dialog (`Remove acme/widgets from target repos?...`), dismissing it to keep `acme/widgets`.

### 5. Merge Queue Settings, Base-Branch Gate Warnings, and Merge Batch Panel

- **Base-branch gate warnings across CI modes**:
  - On `/repos/acme/gadgets/settings/ci`, switched `CI Mode` to `Use GitHub CI` and checked `Watch acme/gadgets's default branch with grain CI`.
  - Without reloading, verified the red error alert appeared in `.repo-page-header`:
    > `Nothing is testing acme/gadgets's default branch, and the merge queue is merging onto it anyway...`
  - Clicked `Go to CI Mode` inside the alert and verified it navigated to `/repos/acme/gadgets/settings/ci`; verified the `Overview` tab's `CI gate` card read `GitHub CI — base branch not judged`.
  - Opened `/repos/acme/gadgets/releases` and verified the same base-branch gate warning appeared at the top of the Releases pane with an `Open repository settings` button that returned to `/repos/acme/gadgets`.
  - Switched `acme/gadgets` to `Use Grain CI` and verified the base-branch gate warning cleared immediately without a reload on both the repo header and `/repos/acme/gadgets/releases`.
- **Per-repository Merge Queue settings (`/repos/acme/widgets/settings/merge-queue`)**:
  - Toggled `Require branches to be up to date with their base branch before merging`, `Merge pull requests into acme/widgets when green and clean, even when they weren't opened by a grain task`, and `Base-branch repair PRs may merge without a human`. Verified all three persisted on reload and updated the `Overview` summary cards (`Required`, `On`, `On`).
  - Under `Checks no commit here can clear`, added `cla/google`, tested case-insensitive duplicate detection (`CLA/GOOGLE` -> warning `acme/widgets already declares CLA/GOOGLE unfixable — check names are matched without case.`), and tested the `grain CI` refusal (`grain CI judges one exact commit on the branch, so its verdict is always that branch's own answer and declaring it here would change nothing. Grain would go on repairing it.`).
- **Deployment-wide Merge Queue settings (`/settings/engine`)**:
  - Tested validation on `Merge gather window` (`"soon"` -> `400 Bad Request`: `Merge gather window: time: invalid duration "soon"`) and `Merge batch size` (`-3` -> `400 Bad Request`: `Merge batch size must be at least 1; -3 was not saved.`).
  - Saved `Merge gather window = 45s`, `Merge batch size = 3`, and `Max merges per hour = 2`, reloaded `/settings/engine`, and verified persistence.
- **Merge Batch Panel**:
  - On `/repos/acme/widgets`, inspected `MergeBatchPanel` (`2 changes on main`, ref `grain-merge-queue/<id>/main at abcdef012345`, linking to PRs `#41` and `#42` and tasks `#5 Fix flaky widget test` and `#6 Tighten retry backoff`).
  - Clicked `#5 Fix flaky widget test` -> opened `TaskDetailsOverlay` at `/tasks/5`, verified `MergeBatchPanel` rendered inside the task detail dialog, and clicked `#6 Tighten retry backoff` inside the task's batch panel to switch directly to `/tasks/6`.

### 6. Compare Branches and PullRequestPage

- On `/repos/acme/widgets/compare`, verified `Base branch` defaulted to `main` and focusing `Head branch` opened the autocomplete menu listing `task-1146/operator-branch`, `1.0.latest`, `candidate`, `feature/compare-me`, `main`, and `release/1.0`.
- Typed an unknown branch `missing-branch-xyz` into `Head branch` -> verified `Open comparison` was disabled and the inline warning read `No branch "missing-branch-xyz" on acme/widgets: there is nothing to compare.`
- Selected `feature/compare-me` and clicked `Open comparison` -> navigated to `/repos/acme/widgets/compare/main...feature/compare-me` and rendered the diff (`Add compare test fixture`, `2 files changed`, `+9`, `-0`, `README.md`, `src/compare_test.go`).
- Verified deep links and edge cases:
  - `/t/default/repos/acme/widgets/compare/main...feature/compare-me` normalized to `/repos/acme/widgets/compare/main...feature/compare-me` and rendered the diff.
  - `/repos/acme/widgets/compare/feature/compare-me` inferred the default base branch `main`.
  - `/repos/acme/widgets/compare/main...main` showed `No changes between main and main.`
  - `/repos/acme/widgets/compare/main...no-such-branch` returned `404` and rendered `Could not compare main...no-such-branch on acme/widgets: acme/widgets: unknown ref in main...no-such-branch.`
  - `/repos/acme/gadgets/compare/main...dev` returned `502` and rendered `Could not compare main...dev on acme/gadgets: reading comparison from GitHub: githubsim: unknown repo acme/gadgets.`

### 7. Releases and Release Trains (`/repos/acme/widgets/releases`)

- **Seeded release `1.0` & qualification approval**:
  - Opened `/repos/acme/widgets/releases`; verified `1.0 -- Active` (`latest: 1.0.latest, prod: release/1.0`), current candidate `1.0.rc.1 -- active`, and disabled `Cut new RC` explanation (`Cut new RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.`).
  - Verified the qualification summary `Pending approval` (`2 pending approval`) and task links (`#17 Run smoke suite on RC`, `#18 Verify migration rollback`).
  - Clicked `#17 Run smoke suite on RC` to open the task overlay and returned; clicked `Approve all` on the pending-approval banner and verified the qualification status transitioned to `Running`.
- **Direct URLs and validation**:
  - Opened `/repos/acme/widgets/releases/nope-404` -> verified URL normalized to `/repos/acme/widgets/releases`, fell back to `1.0`, and showed the error alert `no release "nope-404"`.
  - Submitted invalid release names `bad release name` and `x.latest` (`400 Bad Request` with git ref naming rules) and duplicate release name `1.0` (`400 Bad Request`: `this repo already has an unmerged release with this name`).
- **Release lifecycle (`wt1146`)**:
  - Created release `wt1146` -> URL updated to `/repos/acme/widgets/releases/wt1146`, status transitioned from `Provisioning` to `Active`, and candidate `wt1146.rc.1` was automatically cut (`active`).
  - Clicked `Promote current RC`, tested dismissing the confirmation dialog (`Promote wt1146.rc.1 to wt1146? This moves acme/widgets's wt1146 branch to wt1146.rc.1.`), then accepted it -> `wt1146.rc.1` transitioned to `promoted` and `Cut new RC` became enabled.
  - Clicked `Cut new RC` -> `wt1146.rc.2` became the current `active` candidate and `wt1146.rc.1 -- promoted` moved into `Candidate history`.
  - Updated the qualification plan (`Repeat = 2`, `Promote automatically once qualification succeeds`), clicked `Save qualification plan`, reloaded, and verified persistence.
  - Switched between `1.0` and `wt1146` in the `Release` combobox and verified browser `Back` stepped between `/repos/acme/widgets/releases/1.0` and `/repos/acme/widgets/releases/wt1146`.
  - Clicked `Delete release` on `wt1146` (dismissed first, then accepted) -> discovered and fixed the bug below.

---

## Bug Found and Fixed

### Deleting a selected release displays a false `no release "<name>"` error banner

- **Symptom**: On `/repos/acme/widgets/releases/<name>`, clicking `Delete release` and accepting the confirmation dialog deletes the release (`DELETE /api/repos/acme/widgets/releases/<name>` -> `204 No Content`), switches the picker to the remaining release (`1.0`), and normalizes the URL to `/repos/acme/widgets/releases`, **but simultaneously raises a red error banner (`Alert severity="error"`) at the bottom of the screen saying `no release "<name>"`**.
- **Deterministic Reproduction**:
  1. Open `/repos/acme/widgets/releases`.
  2. Create a release named `wt1146` (which selects `wt1146` and updates the URL to `/repos/acme/widgets/releases/wt1146`) or open `/repos/acme/widgets/releases/1.0` directly.
  3. Wait for the release to become `Active`, click `Delete release`, and accept the confirmation prompt.
  4. Observe the red error alert `no release "wt1146"` (or `no release "1.0"`).
- **Root Cause**:
  1. In [`ui/src/components/RepoReleases.jsx`](../../ui/src/components/RepoReleases.jsx), `removeRelease` deleted the release on the server and called `await refresh()` while `selectedRef.current` still held the deleted release's name. Inside `refresh()`, `asked` (`selectedRef.current`) was no longer in `list`, so `refresh()` unconditionally called `onSelectReleaseRef.current(null, asked)` — reporting the just-deleted release to `App.jsx` as a missing URL target (`missingMessage("release", asked)` -> `no release "<name>"`).
  2. In addition, `refresh()` did not track whether `asked` was an unconfirmed release name from the URL (`urlReleaseRef`, analogous to `App.jsx`'s `urlItemsRef` / `keepIfListed`), so any previously loaded release that disappeared after already being confirmed was reported as a broken deep link.
  3. In [`ui/src/App.jsx`](../../ui/src/App.jsx), `selectRelease(null)` without a `missing` argument did not mark `droppedRef.current`, so normalizing the URL from `/repos/<owner>/<name>/releases/<deleted>` to `/repos/<owner>/<name>/releases` pushed a new history entry onto the browser stack instead of replacing the deleted release's URL in place.
- **Fix**:
  - In [`ui/src/components/RepoReleases.jsx`](../../ui/src/components/RepoReleases.jsx):
    - Track unconfirmed URL-supplied release names in `urlReleaseRef` and only pass the second `missing` argument to `onSelectRelease(null, asked)` when `asked` came from the URL and failed its first list check.
    - In `removeRelease`, clear `selectedRef.current`, `shownRelease.current`, `urlReleaseRef.current`, `selected`, and call `onSelectRelease(null)` before calling `await refresh()`.
  - In [`ui/src/App.jsx`](../../ui/src/App.jsx):
    - In `selectRelease`, when `release === null` and `prev !== null` without `missing`, set `droppedRef.current = droppedRef.current || { missing: [] }` so the URL effect replaces `/repos/<owner>/<name>/releases/<deleted>` with `/repos/<owner>/<name>/releases` via `history.replaceState` without showing an error banner.
- **Regression Coverage**:
  - Added unit test `"clears the URL release on Delete release without reporting it as missing"` in [`ui/src/components/RepoReleases.test.jsx`](../../ui/src/components/RepoReleases.test.jsx) verifying that `onSelectRelease` is called with `(null)` and never with `(null, "1.0")` when deleting a release.
  - Verified end-to-end in headless Chromium against the rebuilt `grain demo` binary (`0` error alerts on `Delete release`).
