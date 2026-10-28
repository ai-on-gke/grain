# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-05
- **Task**: grain/task-1984
- **Status**: Passed, with two defects found and fixed in this change:
  - Escape in a New task picker closed the whole drawer.
  - Open memory documents' fields had ambiguous names.

  No follow-up task was needed. Observations are listed at the end.

## Environment

- **Commit walked through**: `98f2a031d`. This is the tip of both
  `candidate` and `main` at the start of the run. The footer stamp read
  `v1.8.0-rc.1 · 98f2a03 · 2026-10-05 00:11Z`.
- **Checks already in place**: yesterday's fixes (release deep link,
  memory Save/Revert names) and the duplicate-branch fix (`409a5fff4`,
  `d1e4f0ddc`) are all in this base.
- **Fixes**: checked on this branch against a second demo, rebuilt from
  the fixed tree, over a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64, 4 vCPU, 7 GB. Go 1.26.6,
  Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34 (Playwright
  `chromium-headless-shell` 1234).
  - It was driven by the Playwright library from ad-hoc scripts kept
    outside the repository (not jsdom).
  - The viewport was 1400×950, with spot checks at 390×844.
  - Every script logged:
    - console errors and warnings;
    - page errors;
    - failed requests;
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
  - The fix was verified on `127.0.0.1:8421`, under `TMPDIR=/tmp/run2`,
    built from this branch.
- **Account**: `operator`, created through the first-run **Create
  account** dialog (`POST /api/auth/register`) on each server.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so no Gemini-backed tooling was used.
- **Setup**: Chromium was installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium`.
  No screenshots or traces are committed.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo has no GitHub. Its real answer, 404 "GitHub branch listing is not available on this UI server", was seen on the task detail page. | `page.route` on this path only | `["main","candidate","release/3.0"]`; `["main","candidate"]` |
| GitHub compare behind `GET /api/repos/acme/widgets/compare?…` | The demo answers 404 "repository comparison is not available on this UI server". That real answer was exercised first, with no stub. | `page.route` on this path only, using `ui.BranchComparison`'s JSON shape | open diff with a merge base; 3 s delay, then merged with an empty diff; 502 `{"error":"GitHub: compare failed (502 Bad Gateway)"}` |
| Release-run dispatch, `POST /api/repos/acme/widgets/release-runs` | The demo daemon runs `-ci=false`, and its real answer is 404 "this deployment has no grain CI wired". That answer was exercised first, with no stub. | `page.route` on this path only | 502 `{"error":"GitHub: could not resolve refs/heads/main (502 Bad Gateway)"}`; 3 s delay, then 202 `{id:"run-20261005-2"}`; immediate 202 `{id:"run-20261005-1",status:"queued"}` |
| GitHub release reconciler (provisioned, candidate cut/promoted, merged, error, qualification sync) | No reconciler runs in the demo | The committed `ui/e2e/releasehelper`, built to `/tmp/releasehelper` and run as `-dir /tmp/run1/grain-demo-* [-release <name>] <cmd>`. It calls the `model.Store` transitions on the demo's own SQLite store. | `mark-release-provisioned`, `mark-candidate-cut` (×2), `mark-candidate-promoted`, `sync-qualifications`, `mark-release-merged`, `mark-release-error` |
| Network conditions | To see loading and offline behaviour without touching any API | The Chrome DevTools protocol's `Network.emulateNetworkConditions`: 1.5 s latency at 50 kB/s, then offline. This is not a stub: every request still reached the real server, or failed in the browser. | — |

Grain's own API, the React UI and every user gesture were real everywhere
else. Read-backs used plain `fetch` from the page against the same
server: `GET /api/tasks`, `GET /api/memory`, `GET /api/memory/documents`,
`GET /api/repos/acme/widgets/declarations` and
`GET /api/teams/default/repos/acme/widgets/config`.

## Steps and outcomes

### 1. Repos list

1. **Opening it**:
   - Focused the rail's **repos** entry (`div role=button tabindex=0`) and
     pressed **Enter**. The router went to `/repos`.
   - A cold `page.goto("/repos")` drew the same page.
2. **Counts**: the header badge read `1`. The `acme/widgets` row showed
   the ten state chips and `12 tasks`.
3. **Add repo** (the `owner/name` box, Enter to submit):

   | Typed | Request | Result |
   |---|---|---|
   | `acme/gadgets` | 200 | added |
   | `example-corporation/exceptionally-long-repository-name-for-responsive-layout-checks-1005` | 200 | added |
   | `zeta/alpha-tools` | 200 | added |
   | `acme/widgets`, `ACME/Widgets` | 200 | no second row |
   | `bad//name` | 400 `repo: repo must be owner/name, got "bad//name"` | shown |
   | ` acme/spaced ` | 200 | trimmed to `acme/spaced` |
   | `acme/widgets.git` | 200 | normalised onto `acme/widgets`, no new row |
   | `https://github.com/acme/urlform` | 200 | normalised to `acme/urlform` |

   The badge ended at `6`.
4. **Long names**:
   - Nothing overflowed at 1400 px or at 390 px (document `scrollWidth`
     equals the viewport).
   - At 390 px the long row's link is clipped with
     `text-overflow: ellipsis`, and its `title` carries the full name.
5. **Search**: `ALPHA` showed only `zeta/alpha-tools`, with a badge of
   `1`. `qqq` showed "No repos match your search." with a badge of `0`.
6. **Sort** (MUI combobox, opened with Enter): its options are Custom
   order, Name (A–Z), Active first and Most tasks.
   - Active first and Most tasks put `acme/widgets` first.
   - Only Custom order shows `Move …` handles (6). The others show 0.
7. **Keyboard reorder**:
   - Enter on `Move zeta/alpha-tools` showed "Moving 1 repo — choose where
     it goes", and focus moved to "Stop moving zeta/alpha-tools".
   - The slots were "Move here, above acme/gadgets" and so on. Enter on
     the first moved zeta to the top, and focus returned to its handle.
   - Escape on a pick-up of `acme/spaced` cancelled it. The order was
     unchanged and focus stayed on its handle.
8. **Pointer drag**: dragging the `acme/gadgets` handle above the first
   row put gadgets first.
9. **Click/tap pickup**: clicking `Move acme/widgets` and then "Move here,
   to the end of the list" moved it to the end, with focus back on its
   handle.
10. **Reload**: the order and "Custom order" survived.

### 2. Repo page and navigation

1. **Opening a row**: clicking `acme/widgets` opened
   `/repos/acme/widgets`. Back returned to `/repos` and Forward returned
   to the repo page. Enter on **← Repos** also returns to `/repos`.
2. **The task-count link**: **12 tasks filed here**
   (`/?f=repo:any:acme/widgets`) showed 12 rows with "Filters (1)". Back
   returned to the repo page, and Forward returned to the filtered list.
3. **Keyboard reach**: Tab from **← Repos** walks every control on the
   page in reading order before reaching the rail. That includes the
   repo link, the task count, New task, Releases, Remove, the branch and
   compare fields, both capability selects, both text areas, the
   checkboxes, CI mode, the merge-batch PR links, Add workflow, all 15
   work-ownership buttons and the release-workflow controls.

### 3. Filing a task from the repo page

1. **The form**: **New task** opened the drawer at `/new`, with
   `select[name=repo]` preselected to `acme/widgets`.
2. **Base branch** (stubbed listing): typing `rel` and pressing Enter took
   `release/3.0` without submitting the form.
3. **Draft persistence**: after a reload at `/new`, the title, repo
   (`acme/widgets`) and base (`release/3.0`) were all restored.
4. **Create task** → 201, task 13. `GET /api/tasks` showed repo
   `acme/widgets`, base `release/3.0`, state `queued` and capabilities
   `gcp-key, gemini-key, self-debug`.
   - Because the reload had happened at `/new`, the page behind the
     drawer was the task list, and Create returned there rather than to
     the repo page.
   - The repo page's counter then read "13 tasks filed here" (later 14,
     after the qualification task).
5. **Cross-repo task, from `/repos/zeta/alpha-tools`**:
   - The repo was preselected to `zeta/alpha-tools`.
   - Read-only repos suggested `acme/widgets` and `Add acme/wid` for
     `acme/wid`, and the click added a chip.
   - Its own repo is not offered as a read: `zeta/alpha` only offered
     "Add zeta/alpha".
   - In "Another repo this task may change", `acme/gad` highlighted
     `acme/gadgets`, and Enter added it ("Remove acme/gadgets").
6. **Bug, fixed here**: pressing **Escape** while the Read-only repos
   suggestion list was open closed the list *and* the whole New task
   drawer. The URL went back to `/repos/zeta/alpha-tools`.
   - The draft survived, and `/new` restored the title.
   - The base-branch and depends-on pickers do the same.
   - See *Fixes in this change*.

### 4. Repo configuration (`/repos/acme/widgets`)

Every change below was made through the visible control. The page was
then reloaded, and each value was read back from the page and, where
relevant, from the API.

| Setting | Request | After reload |
|---|---|---|
| Add branch `walk/topic-1005`, twice | `POST …/branches` → 201, then 200 with "walk/topic-1005 is already on this repo's list (pending); nothing new was added." | a single `walk/topic-1005 -- pending` row (the 10-04 follow-up is fixed) |
| Add branch `bad branch`, `../escape`, `-flag` | 400 "invalid branch name" each | not listed |
| CI capabilities: GCP key off, then GitHub sandbox on | `PUT …/ci-capabilities` | chip `GitHub sandbox` |
| Default capabilities: Memory | `PUT …/capabilities {"defaultCapabilities":["memory"]}` | "A task filed against acme/widgets starts with: Self debug, GCP key, Gemini key, Memory". The deployment defaults are marked "already a deployment default -- on here either way". |
| Prompt extension (two lines) | `PUT …/prompt-extension` on blur | kept |
| Setup command `make deps` | `PUT …/setup-command` on blur | kept |
| Declarations on | `PUT …/declarations {"declarations":true}` | checked, "Read at abc123commit: 1 template(s), 1 suite(s), 1 schedule(s), 0 trigger(s)" |
| Require branches up to date | `PUT …/require-branch-up-to-date` | checked |
| CI mode: grain CI | `PUT …/ci-mode {"ciMode":"grain"}` | selected |
| Unfixable check `cla/google`, then `CLA/Google` | one `PUT …/unfixable-checks`; the differently cased repeat sent nothing | listed once |
| Jobs dispatched after every merge: `cache-warm, docs` | `PUT …/release {"postMergeDispatchJobs":[…]}` | kept |
| Work ownership: release off | `PUT …/work-ownership {"item":"release","state":"off"}` | `aria-pressed=true` |
| Automatic releases off (no confirmation; reversible) | `PUT …/release {"enabled":false}` | "Automatic releases off". This revealed "Jobs that judge base branches". |
| Jobs that judge base branches: `go, lint` | `PUT …/release {"baseBranchJobs":["go","lint"]}` | the info alert names `go, lint` |
| Automatic releases back on | `PUT …/release {"enabled":true}` | on, and the base-branch jobs field is hidden again |

Notes from this section:

- **Toggle timing**: the declarations, up-to-date and CI-mode toggles
  follow the server's answer. Each flips when the PUT returns, not on the
  click itself.
- **Compare branches** (base `main`, head `walk/topic-1005`) opened
  `/repos/acme/widgets/compare/main...walk/topic-1005` under each answer:
  - **real demo answer**: "repository comparison is not available on this
    UI server";
  - **stubbed open diff**: "open · Into main on acme/widgets -- forked at
    0a1b2c3d", with side by side / inline / raw views and README.md +2 −1;
  - **delayed**: "Loading the comparison…", then after 3 s "merged ·
    Merged into main … There are no changes between these branches.";
  - **502**: the error, shown twice (see observations).
- **Remove**:
  - It first sent `POST /api/settings/plan`, the change preview, with
    `acme/widgets` dropped.
  - It then asked `confirm("Remove acme/widgets from target repos? New
    tasks won't be able to target it until it's added back, and it will
    drop off the repos list. Tasks already filed there are not
    affected.")`.
  - Dismissing it changed nothing.
- **Offline**:
  - With the browser offline, blurring an edited prompt extension
    showed "This deployment cannot be reached: the connection to it
    failed. (Failed to fetch)".
  - After reconnecting and reloading, the server value was unchanged.
- **Throttled loading**: at 1.5 s latency and 50 kB/s, `/repos`, the
  repo page, the releases pane and the Models tab each drew their content
  once the data arrived. They showed no error and no layout jump. None of
  them showed a "Loading…" text, unlike the compare page.

### 5. Read and write relationships, and team-scoped configuration

1. **Read and write repos**: these are per-task. They were exercised on
   the New task form (§3.5).
2. **Team page**: `/teams/default/repos/acme/widgets` says that default
   capabilities, prompt extension and setup command are default's own,
   and everything else is global.
   - The global CI capabilities and the inherited prompt extension and
     setup command are shown with "inherits acme/widgets's global …".
3. **Inherited fields**: focusing and blurring the inherited prompt
   extension wrote nothing.
4. **Team overrides**:
   - Setting the team prompt extension and setup command sent
     `PUT /api/teams/default/repos/acme/widgets/config` with one field
     each.
   - Both survived a reload. The `config` object in the API carried both,
     and the global page still showed the global prompt extension.
5. **Revert to global defaults** asks `confirm("Revert team overrides for
   acme/widgets back to global repository defaults?")`.
   - Dismissed: no request.
   - Accepted: `DELETE …/config`. After a reload the field showed the
     global text again.

### 6. Memory (Team settings › Models)

1. **Opening it**: `/team-settings` resolved to `/teams/default/settings`,
   and the Models tab went to `/teams/default/settings/models`.
2. **The two scopes**:
   - "Memory carried by every run" holds the `make fmt` note, written 2
     days ago by the memory agent.
   - "Memory about acme/widgets" holds the `make frontend` note.
   - The buttons are named "Save/Revert this deployment's memory" and
     "Save/Revert acme/widgets memory" (yesterday's fix holds).
3. **Keyboard**: Tab from the acme/widgets text box reaches "Revert
   acme/widgets memory" next.
4. **Correcting the repo memory**: appended "- 10-05 walkthrough: `make
   frontend` needs Node 20." and pressed Save (`PUT /api/memory`).
   - After a reload the box held the line, and the scope read "written
     today / edited here".
   - `GET /api/memory` showed `updatedBy:"operator"`,
     `updatedByKind:"human"`.
5. **Correcting a document**: opened `flaky-e2e (acme/widgets)`, appended
   "- 10-05: retry budget is 0; a red is real." and saved (`PUT
   /api/memory/documents`).
   - After a reload the document was closed again. Reopening it read the
     correction back, with the summary unchanged.
6. **Validation**: saving with an empty summary was refused with 400 "a
   memory document needs a summary: …", shown in the banner.
7. **Restored**: both corrections were put back through the UI, and
   `GET /api/memory` matched the original text exactly.
8. **Delete confirmation**: **Delete release-checklist (this
   deployment)** asks `confirm('Delete the memory document
   "release-checklist"? Nothing else keeps a copy of it.')`. Dismissing it
   left both documents in place (`GET /api/memory/documents` still listed
   2).
9. **Bug, fixed here**: with both documents open, the pane had two text
   boxes named "Summary". A document name repeated across scopes would
   also give two named "Document: <name>". See *Fixes in this change*.

### 7. Releases and release trains

1. **Opening it**: Enter on the repo page's **Releases** button opened
   `/repos/acme/widgets/releases`, which read "acme/widgets has no
   releases yet …".
2. **Validation**:
   - An empty name is blocked by the browser ("Please fill out this
     field.").
   - `has space` and `../x` came back 400 "invalid release name".
   - A duplicate `v2.0-1005` came back 400 "this repo already has an
     unmerged release with this name".
   - A refused name stays in the box. A created one clears it.
3. **Create**:
   - `v2.0-1005` and `hotfix-1005` were both created (201).
   - The URL followed each to `…/releases/<name>`.
4. **Helper: provisioned and cut** (v2.0-1005):
   - The picker then offered "hotfix-1005 -- Provisioning" and "v2.0-1005
     -- Active".
   - Picking v2.0 set `…/releases/v2.0-1005` and showed
     `v2.0-1005.rc.1 -- active`.
5. **Promote**: **Promote current RC** asks `confirm("Promote
   v2.0-1005.rc.1 to v2.0-1005? This moves acme/widgets's v2.0-1005 branch
   to v2.0-1005.rc.1.")`.
   - Dismissed: no request.
   - Accepted: `POST …/candidates/promote` → 200, and the candidate showed
     `promoting`.
6. **Cut rc.2**:
   - After the helper's `mark-candidate-promoted`, **Cut new RC** sent
     `POST …/candidates` → 201 with no confirmation.
   - The history then read `v2.0-1005.rc.2 -- cutting`, `v2.0-1005.rc.1 --
     promoted`.
7. **Qualification plan**:
   - **Add item** opened a template select (nightly-maintenance,
     security-audit, release-qualification).
   - Saving release-qualification sent `PUT …/qualification-plan` → 200,
     and it survived a reload.
8. **Qualification run**:
   - After `sync-qualifications`, the pane showed "qualification · running
     · release-qualification".
   - That is `<a href=/tasks/14>`. It opened task 14, and Back returned to
     `…/releases/v2.0-1005`.
9. **Merge back**: **Merge v2.0-1005 into the default branch** asks
   `confirm("Merge v2.0-1005 into acme/widgets's default branch? This
   opens a pull request on GitHub from v2.0-1005.")`.
   - Dismissed: no request.
   - Accepted: `POST …/merge` → 200, and the label read "Merge requested".
   - The button is then disabled. `RequestReleaseMerge` also refuses a
     second request.
10. **Merged and error states**:
    - After `mark-release-merged`, the release reads "Merged", with Cut
      and Promote disabled and "Merge requested -- pull request" in place
      of the button.
    - After `mark-release-error` on hotfix-1005, its line reads "(Simulated
      GitHub failure creating the release branches)".
11. **Deep links**:

    | URL | Result |
    |---|---|
    | `/repos/acme/widgets/releases/nope` | rewritten to `…/releases`, newest shown, alert `no release "nope"` |
    | `/repos/acme/widgets/releases/a%20b` | the same, `no release "a b"` |
    | `/repos/zeta/alpha-tools/releases` | empty pane cold, plus "No templates this repo can qualify yet" |
    | `/repos/nobody/missing/releases` | "nobody/missing is not on this deployment's target repo list …", with no create |
    | `/teams/default/repos/acme/widgets/releases/v2.0-1005` | opens v2.0-1005 |
    | `/repos/acme/widgets/releases/v2.0-1005/extra` | normalised to `…/releases/v2.0-1005` |
    | `/repos/acme/widgets/trains` | falls back to the repo page; there is no train page |

12. **Release trains**: as on 10-04, the "train" is the repo page's
    **Release workflows** section. It holds:
    - automatic releases on/off;
    - **Run release by hand**, which reveals a required "Release branch"
      field;
    - jobs dispatched after every merge, and jobs that judge base
      branches.
13. **Release-run dispatch**: with no stub, the run is refused with 404
    "this deployment has no grain CI wired", shown in the banner. With the
    stub:
    - **502**: the error is shown, and the form stays open for another
      try.
    - **3 s delay**: **Run release** is disabled while the request is in
      flight, which stops double dispatch.
    - **202**: the form closes and the UI asks grain for
      `GET /api/ci/runs/<id>`. In the demo that is grain's own real
      404, because no CI is wired.
14. **Keyboard picker**: Enter, ArrowDown, Enter on the release picker
    moved to `…/releases/v2.0-1005`, and focus stayed on the picker.
15. **Phone**: at 390 px the releases pane did not scroll sideways.

### Console and network

No page errors anywhere. Every console error was one of these:

- the resource errors for deliberate 400s;
- the stubbed 502s;
- the demo's real 404s for `github-branches` (task detail), `compare`
  and `release-runs` before stubbing;
- `GET /api/ci/runs/<id>` after a stubbed release run;
- `ERR_INTERNET_DISCONNECTED` during the offline step.

The recurring `GET /api/tasks net::ERR_ABORTED` lines are the poll's
conditional 304s, as explained on 10-04.

## Fixes in this change

### 1. Escape closes the open suggestion list, not the New task drawer

- **Reproduction** (fresh demo):
  1. Open `/repos/zeta/alpha-tools` and press **New task**.
  2. Type a title, click **Read-only repos** so its list opens, and press
     **Escape**.
  3. The drawer closes and the URL returns to the repo page.

  The base-branch field and "Search tasks to depend on…" behave the same.
  The draft survives, but the form is gone mid-entry.
- **Cause**: the `onKeyDown` handlers in `RepoPicker`, `BranchField` and
  `TaskPicker` closed their popper on Escape and let the event bubble to
  the MUI Drawer, which closes on Escape.
- **Fix**: each handler now calls `stopPropagation()` on Escape, but only
  while its list is actually showing (`open`, `showing`, `showResults`).
  With no list open, Escape still reaches the drawer.
- **Verified in the browser** on the rebuilt server: for Read-only repos,
  Another repo, Base branch and depends-on, Escape took the menu items
  from N to 0 and left the drawer at `/new`. A second Escape closed the
  drawer back to `/repos/acme/widgets`.
- **Regression coverage**:
  - new cases in `TaskPicker.test.jsx`, `ReadOnlyReposField.test.jsx`
    (RepoPicker) and `BranchField.test.jsx`. Each puts the picker inside
    a parent `onKeyDown` spy, checks that the first Escape closes the list
    without reaching the parent, and that the second does reach it. All
    three fail without the change.
  - an Escape step in `e2e/repository_release_memory_walkthrough.spec.js`,
    which checks the drawer and the typed title are still there.

### 2. Open memory documents' fields are named for their document and scope

- **Reproduction**: on `/teams/default/settings/models`, open
  `flaky-e2e (acme/widgets)` and `release-checklist (this deployment)`,
  then list the text boxes' accessible names. There are two "Summary".
- **Fix** (`MemoryPanel.jsx`):
  - The inputs are named "Summary of flaky-e2e (acme/widgets)" and
    "Document: flaky-e2e (acme/widgets)".
  - The visible labels are unchanged and form the start of each name, so
    `getByLabel("Document: release-checklist")` in the existing specs
    still matches.
- **Verified in the browser**: the names read `Summary of
  release-checklist (this deployment)`, `Document: release-checklist
  (this deployment)`, `Summary of flaky-e2e (acme/widgets)` and
  `Document: flaky-e2e (acme/widgets)`.
- **Regression coverage**:
  - a `MemoryPanel.test.jsx` case with same-named documents open in both
    scopes, which fails without the change;
  - a two-documents-open step in the walkthrough e2e spec.

### Tests run

- `vitest`: `MemoryPanel`, `TaskPicker`, `ReadOnlyReposField`,
  `BranchField` and `WriteReposField` passed (75 tests). The new cases
  fail with the component changes stashed.
- Playwright Chromium (`npx playwright test`) for
  `repository_release_memory_walkthrough`, `memory`,
  `new_task_draft_repo_page` and `repos_workflows`: 7 passed.

## Observations (not filed)

- **Errors outlive a success**: the global error banner still keeps a
  stale error after a later success. `bad//name`'s refusal stayed under
  three successful adds. This is the app-wide behaviour noted on 10-03
  and 10-04.
- **Duplicated compare error**: a failing compare still shows its message
  twice, in the page and in the banner.
- **Missing landmark**: the repo page, boards, board and sessions pages
  render in `div.main-column` with no `<main>` landmark, while the Repos
  list, task list and releases pane use `<main>`. This is a cross-page
  accessibility gap, left alone here because `main {}` carries layout
  rules of its own (`style.css`).
- **Suggestion list roles**: the repo, branch and task pickers' lists are
  MUI `menu`/`menuitem`, and the text box carries no `combobox`/
  `aria-expanded`. A screen reader is not told that a list opened. This
  is a design choice shared by all three pickers.
- **Release-branch format**: the "Release branch" field accepts
  `bad branch` client-side. The demo refuses it only because no CI is
  wired, so server-side branch validation for release runs could not be
  observed.
- **Unconfirmed actions**: Cut new RC and Turn off automatic releases ask
  no confirmation. Both are reversible or additive, unlike Promote,
  Merge, Remove and the deletes, which all confirm.
- **Unsaved memory edits**: an unsaved memory edit is dropped without a
  word when switching tabs. Revert exists, but nothing warns.
