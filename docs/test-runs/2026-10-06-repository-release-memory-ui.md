# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-06
- **Task**: grain/task-2234
- **Status**: Passed, with three defects found and fixed in this change:
  - A team's own repo default capabilities never reached a task filed
    from the New task form.
  - The releases pane accepted release names git cannot use as a branch
    (`v3.0..x`).
  - The repo page's default-branch watch note read "It
    needsacme/widgets's CI mode".

  No follow-up task was filed. Observations are listed at the end.

## Environment

- **Commit walked through**: `eabd41c4a`, the tip of `candidate` at the
  start of the run. The footer stamp read
  `v1.8.0-rc.1.0.20261006192042-eabd41c4a10e · eabd41c · 2026-10-06 19:20Z`.
- **Fixes**: checked on this branch against a second demo, rebuilt from
  the fixed tree, over a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64. Go 1.26.6, Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34 (Playwright
  `chromium-headless-shell` 1234).
  - It was driven by the Playwright library from ad-hoc scripts kept
    outside the repository (not jsdom).
  - The viewport was 1400×950, with spot checks at 390×844.
  - Every script logged:
    - console errors and warnings;
    - page errors;
    - failed requests (other than `net::ERR_ABORTED` poll cancels);
    - every response of 400 or above;
    - every mutating `/api/` request, with its body;
    - every `confirm()` dialog, which the script accepted or dismissed as
      the step required.
- **Server**: the real `grain demo`.
  - Built by running `npm ci && npm run build` in `ui/` (into
    `pkg/ui/static`), then `go build -o /tmp/grain ./cmd/grain`.
  - Started as
    `TMPDIR=/tmp/run1 grain demo -addr 127.0.0.1:8420 -open=false` over a
    fresh embedded SQLite store.
  - The fixes were verified on `127.0.0.1:8421`, under `TMPDIR=/tmp/run2`,
    built from this branch.
- **Account**: `operator`, created through the first-run **New user**
  dialog on each server.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so no Gemini-backed tooling was used.
- **Setup**: Chromium was installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium`.
  No screenshots or traces are committed.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo has no GitHub. Its real answer is 404 "GitHub branch listing is not available on this UI server", seen unstubbed on the task detail page. | `page.route` on this path only, in the New task step | `["main","candidate","release/3.0"]` |
| Release-run dispatch, `POST /api/repos/acme/widgets/release-runs` | The demo daemon runs `-ci=false`. Its real answer, 404 "this deployment has no grain CI wired", was exercised first, with no stub. | `page.route` on this path only | 502 `{"error":"GitHub: could not resolve refs/heads/main (502 Bad Gateway)"}`; 2.5 s delay, then 202 `{id:"run-20261006-1",status:"queued"}`; immediate 202 `{id:"run-x"}` |
| GitHub release reconciler (provisioned, candidate cut/promoted, qualification sync, merged, error) | No reconciler runs in the demo | The committed `ui/e2e/releasehelper`, built to `/tmp/releasehelper` and run as `-dir /tmp/run1/grain-demo-* -release <name> <cmd>`. It calls the `model.Store` transitions on the demo's own SQLite store. | `mark-release-provisioned`, `mark-candidate-cut` (×2), `mark-candidate-promoted`, `sync-qualifications`, `mark-release-merged`, `mark-release-error` |

The repository comparison (`GET …/compare`) was exercised only
unstubbed. It returned the demo's real 404 "repository comparison is not
available on this UI server".

Grain's own API, the React UI and every user gesture were real everywhere
else. Read-backs used plain `fetch` from the page against the same
server (`GET /api/tasks`, `GET /api/memory/documents`).

## Steps and outcomes

### 1. Repos list

1. **Opening it**: focusing the rail's **repos** entry and pressing
   **Enter** went to `/repos`. A cold `page.goto("/repos")` drew the same
   six rows at the end of the run.
2. **Add repo**: the `owner/name` box, with Enter to submit.

   | Typed | Result |
   |---|---|
   | `acme/gadgets`, `zeta/alpha-tools`, the 87-char `example-corporation/…-1006` | added |
   | `ACME/Widgets`, `acme/widgets.git` | 200, but no second row |
   | `bad//name`, `a/b/c`, `acme/` | 400 `repo must be owner/name, got …`; the field keeps the text |
   | ` acme/spaced ` | trimmed to `acme/spaced` |
   | `https://github.com/acme/urlform` | normalised to `acme/urlform` |

   The badge ended at `6`.
3. **Long names**: the document `scrollWidth` equals the viewport at
   1400 px and at 390 px.
4. **Search**: `ALPHA` showed only `zeta/alpha-tools`, with a badge of
   `1`. `zzz` showed "No repos match your search."
5. **Sort**: opened with Enter on the combobox.
   - Name (A–Z) is alphabetical.
   - Active first and Most tasks put `acme/widgets` first.
   - Only Custom order shows `Move …` handles (6). The others show 0.
6. **Keyboard reorder**:
   - Enter on `Move zeta/alpha-tools` announced "Moving 1 repo — choose
     where it goes", and focus moved to "Stop moving zeta/alpha-tools".
   - There were five "Move here, above …" slots. Enter on the first moved
     zeta to the top, and focus returned to its handle.
   - Escape after picking up `acme/spaced` cancelled it. The order was
     unchanged and focus stayed on its handle.
7. **Pointer drag**: dragging the `acme/gadgets` handle onto the first
   row put gadgets first.
8. **Click/tap pickup**: clicking `Move acme/widgets` and then "Move here,
   to the end of the list" moved it to the end.
9. **Reload**: the order and "Custom order" survived.

### 2. Repo page and navigation

1. **Opening a row**: clicking `acme/widgets` opened
   `/repos/acme/widgets`. Back returned to `/repos` and Forward returned
   to the repo page.
2. **The task-count link**: **12 tasks filed here**
   (`/?f=repo:any:acme/widgets`) opened the task list with 12 tasks and
   "repo is any of acme/widgets". Back returned to the repo page.
3. **Team URL**: `/teams/default/repos/acme/widgets` draws the same page
   with a "Team context: default" banner. It says which settings are the
   team's own and which are global.

### 3. Filing a task from the repo page

1. **The form**: on `/repos/zeta/alpha-tools`, **New task** opened the
   drawer at `/new` with `select[name=repo]` = `zeta/alpha-tools`.
2. **Base branch** (stubbed listing): typing `rel`, ArrowDown and Enter
   gave `release/3.0`.
3. **Other repos**:
   - Another repo: typing `gadg` offered `acme/gadgets`, which was picked.
   - Read-only repos: typing `widg` offered `acme/widgets`.
4. **Escape**: Escape in the Read-only list closed only the list. The
   drawer and the typed title stayed.
5. **Persistence**: with "Queue immediately" unticked, Create returned to
   `/repos/zeta/alpha-tools`. `GET /api/tasks` showed task 13 with:
   - repo `zeta/alpha-tools`;
   - base `release/3.0`;
   - writes `acme/gadgets` and reads `acme/widgets`;
   - state `proposed`.

   `/tasks/13` showed all of these, and so did a reload.

### 4. Repo configuration (global), then reload

All of these were edited through the page's own controls on
`/repos/acme/widgets`, and each saved itself:

| Control | Request | After reload |
|---|---|---|
| Add branch `walkthrough/1006` | `POST …/branches` | listed, "pending" (no reconciler) |
| Compare `main…walkthrough/1006` | real 404, URL `…/compare/main...walkthrough/1006`; Back returns | — |
| CI capabilities → GCP key | `PUT …/ci-capabilities` | kept |
| Default capabilities → GCP key | `PUT …/capabilities` | kept; "starts with" line unchanged, since GCP key is already a deployment default |
| Prompt extension | `PUT …/prompt-extension` | kept |
| Setup command | `PUT …/setup-command` | kept |
| Read declarations | `PUT …/declarations {true}` | kept |
| Require up to date | `PUT …/require-branch-up-to-date` | kept |
| CI mode → grain CI | `PUT …/ci-mode {grain}` | kept |
| Watch default branch | `PUT …/watch-default-branch` | kept |
| Check that cannot pass: `cla/agreement` | `PUT …/unfixable-checks` | kept |
| Nightly Repo Maintenance Sweep: off, then say nothing | `PUT …/work-ownership` | kept |
| Jobs dispatched after every merge `build-cache, docs` | `PUT …/release` | kept as `build-cache, docs` |

Validation and confirmations on this page:

- **Unfixable check named "grain CI"**: refused in place, with "grain CI
  judges one exact commit … declaring it here would change nothing."
- **`CLA/Agreement` after `cla/agreement`**: refused with "acme/widgets
  already declares CLA/Agreement unfixable — check names are matched
  without case."
- **Remove**:
  - It first posts a dry-run `POST /api/settings/plan` to price the
    removal.
  - It then asks: "Remove acme/widgets from target repos? … Tasks already
    filed there are not affected."
  - Dismissed, nothing was removed.

### 5. Team-scoped repo configuration

On `/teams/default/repos/acme/widgets`:

1. **Prompt extension**: the team prompt extension showed the global text,
   as inherited. Editing it saved a team copy, and the global page still
   showed the global text.
2. **Clearing the override**: emptying it (`PUT …/config
   {"promptExtension":""}`) and reloading showed the inherited global text
   again, with "default has no text of its own".
3. **Default capabilities for default → Memory**:
   - `PUT /api/teams/default/repos/acme/widgets/config
     {"defaultCapabilities":["memory"]}`.
   - The page then said "A task default files against acme/widgets starts
     with: Self debug, GCP key, Gemini key, Memory".
   - It survived a reload.
   - **Defect 1**: **New task** from this page ticked only Self debug, GCP
     key and Gemini key, and task 14 was filed with no `memory` (see
     Fixes).

### 6. Memory

On `/teams/default/settings/models` (Team settings → Models):

1. **The panel**: it lists "This deployment" (271 of 8192 characters) and
   `acme/widgets` (130 characters), with the documents
   `release-checklist` and `flaky-e2e`.
2. **Repo memory**: appending "- 2026-10-06 walkthrough: corrected note."
   enabled Save (`PUT /api/memory`). After a reload the text was there,
   with "written today · edited here".
3. **Opening a document**: **Open flaky-e2e (acme/widgets)** shows
   "Summary of flaky-e2e (acme/widgets)" and "Document: flaky-e2e
   (acme/widgets)" (208 b). Appending a correction and pressing **Save
   document flaky-e2e (acme/widgets)** sent `PUT /api/memory/documents`.
4. **Reading it back**:
   - Leaving the tab, coming back and opening the document again showed
     the correction.
   - So did a reload, now sized 260 b and "edited here".
   - `GET /api/memory/documents` reported length 260 with a new
     `updatedAt`.
5. **Revert**: on an unsaved edit, **Revert** restored the saved text.
6. **Delete**: **Delete release-checklist (this deployment)** asked
   "Delete the memory document "release-checklist"? Nothing else keeps a
   copy of it." Dismissed, the document stayed.
7. **Restoring**: both the document and the repo memory were put back to
   their original text and saved. A reload showed the originals.
8. **Keyboard**: Tab from the repo memory box reaches Open flaky-e2e,
   Delete flaky-e2e, then "Add a repo to write a memory for". Save and
   Revert are skipped while disabled.

### 7. Releases

1. **Opening the pane**: **Releases** on the repo page opened
   `/repos/acme/widgets/releases`. It showed "acme/widgets has no
   releases yet".
2. **Create release**:

   | Name | Result |
   |---|---|
   | `bad name`, `-lead` | 400 "invalid release name" |
   | `v3.0..x` | **200, created** (defect 2) |
   | `v3.0-1006`, `hotfix-1006` | created; URL moves to `…/releases/<name>` |
   | `V3.0-1006` (beside `v3.0-1006`) | created (see Observations) |

3. **Provisioning and cutting** (`mark-release-provisioned`,
   `mark-candidate-cut`):
   - v3.0-1006 read "Active", with rc.1 active.
   - Cut new RC was disabled while a candidate was active. Promote and
     Merge were enabled.
4. **Promote**: **Promote current RC** asks a confirmation. Dismissed,
   nothing changed. Accepted, rc.1 went to "promoting", then
   (`mark-candidate-promoted`) "promoted".
5. **Cut new RC**: this asks no confirmation. rc.2 showed "cutting", then
   (`mark-candidate-cut`) "active". The history lists rc.2 active and
   rc.1 promoted.
6. **Qualification plan**:
   - **Add item** gives a row with "Template", "Repeat" and "Depends on",
     all with accessible names.
   - The templates offered are nightly-maintenance, security-audit and
     release-qualification.
   - Saving release-qualification with "Require approval" ticked sent
     `PUT …/qualification-plan`, and it survived a reload.
7. **Qualification task**:
   - After `sync-qualifications` the pane showed "qualification · pending
     approval" with **Approve all** and `release-qualification ·
     unapproved`.
   - The task link `/tasks/15` opened the task, and Back returned to
     `…/releases/v3.0-1006`.
   - **Approve all** moved it to "running".
8. **Merge back**: **Merge v3.0-1006 into the default branch** confirms
   with "… This opens a pull request on GitHub from v3.0-1006."
   - Dismissed: no request.
   - Accepted: `POST …/merge`, the status read "Merge requested" and the
     button was disabled.
   - After `mark-release-merged`, the status read "Merged", with "Merge
     requested -- pull request".
9. **Error state**: after `mark-release-error`, hotfix-1006's line read
   "(Simulated GitHub failure creating the release branches)".
10. **Picker**: the release picker opens with Enter and lists every
    release with its status.
11. **Deep links**:

    | URL | Result |
    |---|---|
    | `/repos/acme/widgets/releases/nope` | rewritten to `…/releases`, alert `no release "nope"` |
    | `/repos/zeta/alpha-tools/releases` | "no releases yet" and "No templates this repo can qualify yet" |
    | `/repos/nobody/missing/releases` | "nobody/missing is not on this deployment's target repo list …", with no create |
    | `/teams/default/repos/acme/widgets/releases/v3.0-1006` | opens v3.0-1006 |
    | `/repos/acme/widgets/releases/v3.0-1006/extra` | normalised to `…/releases/v3.0-1006` |
    | `/repos/acme/widgets/trains` | falls back to the repo page; there is no train page |

### 8. Release trains (Release workflows on the repo page)

As on earlier runs, the "train" is the repo page's **Release workflows**
section.

1. **Automatic releases**: **Turn off automatic releases** (`PUT …/release
   {"enabled":false}`) survived a reload as "Turn on automatic releases".
   It was then turned back on.
2. **Run release by hand**: this reveals a required "Release branch" field
   and becomes **Cancel manual release**.
   - **Unstubbed**: refused with the real "this deployment has no grain
     CI wired".
   - **Stubbed 502**: the error is shown, and the form stays open.
   - **Stubbed slow 202**: **Run release** is disabled while the request
     is in flight. On 202 the form closes and the CI run overlay opens.
     The overlay fetches `GET /api/ci/runs/run-20261006-1`, gets the
     demo's real 404 and shows "this deployment has no grain CI wired".
     Escape closes it.
   - **`bad branch`**: posted as typed, with no client-side check. The
     server's `checkReleaseRef` refuses any ref that is not a watched base
     or open release branch, so this is not a gap.

### 9. Phone width (390×844)

- `/repos`, `/repos/acme/widgets`, the long-name repo page,
  `…/releases/v3.0-1006`, the Models tab and the team repo page all have a
  `scrollWidth` equal to the viewport.
- On the long-name repo page, only the MUI outlined-field notch legends
  ("Prompt extension for example-corporation/…") extend past the edge.
  They are invisible and clipped.

### Console and network

There were no page errors anywhere. Every console error was one of these:

- the resource errors for deliberate 400s (`bad//name`, `a/b/c`, `acme/`,
  `bad name`, `-lead`);
- the demo's real 404s for `github-branches` (task detail), `compare`,
  `release-runs` and `GET /api/ci/runs/<id>`;
- the stubbed 502.

## Fixes in this change

### 1. A team's own repo default capabilities reach the New task form

- **Reproduction** (fresh demo):
  1. Open `/teams/default/repos/acme/widgets`.
  2. Add **Memory** under "Default capabilities for default". The page
     says the task "starts with: Self debug, GCP key, Gemini key,
     Memory".
  3. Press **New task**.

  The capability picker ticks only Self debug, GCP key and Gemini key.
  Filing gives task 14 with `["gcp-key","gemini-key","grain-ci","self-debug"]`.
- **Cause**:
  - `Client.defaultCapabilities` unions the team layer
    (`TeamRepoConfig.DefaultCapabilities`) only when a request names no
    capabilities.
  - The form always names its list (`defaultCapabilitiesFor` in
    `state.js`).
  - `GET /api/config` carried only the deployment and global repo layers,
    so the form could not add the team's.
- **Fix**:
  - `GET /api/config` now reports `teamRepoDefaultCapabilities` (team →
    repo → ids). It covers only the teams in the response's own `teams`,
    and only ids this build offers.
  - `defaultCapabilitiesFor` unions it after the other two layers when a
    team is given. The form already re-seeds through it on a repo or team
    change.
- **Verified in the browser** on the rebuilt server:
  - New task from the team page ticked Memory with no reload, and the
    filed task carried `memory`.
  - Switching the repo dropped it.
- **Regression coverage**:
  - `TestConfigEndpointReportsTeamRepoDefaultCapabilities`
    (`pkg/ui/server_test.go`).
  - A `defaultCapabilitiesFor` case in `state.test.js`.
  - The e2e test "New task ticks the team's own repo default
    capabilities" in `e2e/team_repo_defaults_release_names.spec.js`.

### 2. Release names git cannot use as a branch are refused

- **Reproduction**: on `/repos/acme/widgets/releases`, create `v3.0..x`.
  - It is accepted (200), listed as "Provisioning", and opens at
    `…/releases/v3.0..x`.
  - Its prod branch is `v3.0..x` and its latest branch `v3.0..x.latest`.
    `git check-ref-format` refuses both, so no reconciler could ever
    create them.
- **Cause**: `model.ValidReleaseName` checked only the charset.
  `ValidBranchName` already refuses `..`.
- **Fix**: `ValidReleaseName` also refuses a `..` anywhere, a trailing
  `.`, and a `.lock` suffix. The bare name is itself the prod branch.
- **Verified**: the e2e test below passed against this branch's build.
- **Regression coverage**:
  - `TestCreateReleaseRejectsAnInvalidName` in both the SQLite and
    Postgres store suites, which fails without the change.
  - The e2e test "refuses a release name git cannot use as a branch".

### 3. "It needs acme/widgets's CI mode" keeps its space

- **Reproduction**: on `/repos/acme/widgets`, the Merge queue note under
  the default-branch watch reads "It needsacme/widgets’s CI mode set to
  …".
- **Cause**: JSX drops the line break between `It needs` and `{repo}`.
- **Fix**: `{" "}` is written out. A scan of every non-test `.jsx` for
  the same text-line-then-`{expr}` shape found no other case that loses
  a space.
- **Verified in the browser**: the note reads "It needs acme/widgets’s CI
  mode".
- **Regression coverage**: a `RepoPage.test.jsx` case, which fails
  without the change.

### Tests run

- `go test ./pkg/ui/ -run TestConfigEndpointReports` and
  `go test ./pkg/model/sqlite/ -run TestCreateRelease`: pass. The release
  test fails with the model change stashed.
- `vitest`: `state.test.js`, `RepoPage.test.jsx` and the `NewTaskOverlay`
  suites passed (353 tests).
- Playwright Chromium:
  - The new `team_repo_defaults_release_names.spec.js`: 2 passed.
  - `repository_release_memory_walkthrough`, `memory`,
    `new_task_draft_repo_page`, `repos_workflows`, `repo-team-overrides`,
    `team-scoped-repos-autosave-walkthrough`,
    `release-run-ref-checks-walkthrough` and `repo-pane`: 16 passed.

## Observations (not filed)

- **Case-only release names**: `V3.0-1006` can be created beside
  `v3.0-1006`. The UI lowercases text, so the release picker shows two
  identical "v3.0-1006 -- …" rows. Git refs are case-sensitive, so this is
  not refused here. It is worth deciding whether release names should be
  unique without case, as unfixable checks already are.
- **The rejected `v3.0..x` row**: the release created before the fix
  stays in the demo store. Rows already stored are untouched by the
  stricter check, and nothing deletes a release from the UI.
- **Focus after the CI run overlay**: once a manual release run has
  started, closing the overlay leaves focus on the document. **Run
  release**, which opened it, has unmounted.
- **Picker reopening**: after Escape closes a repo picker's list, ArrowDown
  does not reopen it, though typing does. The pickers are still MUI
  menus, not comboboxes, as noted on 10-05.
- **Repeated errors**: errors still outlive later successes in the global
  banner (as on 10-03 to 10-05). The compare 404 still shows twice.
- **Unnamed fields**: the Repos list's "owner/name", "Clone URL" and
  "Search repos…" boxes are named only by their placeholders.
- **Unconfirmed actions**: Cut new RC, Turn off automatic releases and
  Approve all ask no confirmation. Each is additive or reversible.
