# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-02
- **Task**: grain/task-1532
- **Status**: Passed. Three small accessibility/copy defects fixed in this
  change; one design question filed as a follow-up.

## Environment

- **Commit walked through**: `cf9d6156c9cd` (`candidate` tip at the start of
  the run; footer stamp `v1.5.0 · cf9d615 · 2026-10-02 08:02Z`). The fixes
  were verified in the browser on this branch's commit `c93003c73`, with the
  server and frontend rebuilt and a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64. Go 1.26.6, Node 20.19.0.
- **Browser**: Playwright 1.62.1 driving Chromium Headless Shell
  151.0.7922.34. The viewport was 1400×950 on desktop, plus 390×844 for the
  long-name check. The scripts used the Playwright library directly, not
  jsdom, so every step could be inspected as it ran. Each script logged
  console errors and warnings, page errors, failed requests and responses
  of 400 or above.
- **Server**: the real `grain demo` server, built with
  `go build ./cmd/grain` after `npm ci && npm run build` had written the
  production frontend to `pkg/ui/static`, and started as
  `TMPDIR=/tmp/run1 grain demo -addr 127.0.0.1:8420 -open=false`. Each start
  used a fresh embedded SQLite store under that `TMPDIR`. The fix
  verification used a second fresh server on port 8421.
- **Account**: `operator`, created through the first-run "create account"
  dialog. The browser session was reused across scripts through Playwright
  storage state.
- **Model tooling**: there was no `/home/debian/.gemini-api-key` in this
  sandbox, so no model-backed tooling was used.
- **Setup note**: `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the
  sandbox's package-cache proxy, as on 2026-10-01.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo answers 404 (no GitHub) | `page.route` for this path only | success (`main`, `candidate`, `walkthrough/topic-1`, `release/2.1`), empty `[]`, 3 s delay, 502 |
| GitHub comparison, `GET /api/repos/acme/widgets/compare?…` | The demo has no GitHub comparison | `page.route` for this path only | success (one-file unified diff), empty diff, 3 s delay, 502 |
| GitHub release reconciler (release provisioned, release error, candidate cut, cut error, promoted, merged, qualification sync) | No reconciler runs in the demo | A throwaway Go helper, not committed, that called the store methods the reconciler calls (`MarkReleaseProvisioned`, `MarkReleaseError`, `MarkCandidateCut`, `MarkCandidateError`, `MarkCandidatePromoted`, `MarkReleaseMerged`, `orchestrator.SyncQualifications`) against the demo's own SQLite store, keyed by release name. The committed `ui/e2e/releasehelper` does the same for the e2e spec, but only for the newest release. | each state named |
| Grain CI execution for a manual release, `POST /api/repos/acme/widgets/release-runs` | The demo daemon runs with `-ci=false` (observed unstubbed first: 404 "this deployment has no grain CI wired") | `page.route` for the POST only | 502 `grain CI: runner pool unavailable`, 2 s-delayed 201 `{id:"rr-0001"}` |
| Network latency and offline | To see loading and offline states without touching the API | Chrome DevTools Protocol `Network.emulateNetworkConditions` (2.5 s latency; offline) | n/a |

Grain's own API, the React UI and every user gesture were real everywhere
else. A few `GET`s were used only to read back what had been stored. Each is
named where it was used.

## Steps and outcomes

### 1. Repos list (`/repos`)

1. Focused the rail's **Repos** entry (`div[role=button]`, `tabindex=0`)
   and pressed **Enter**. The router went to `/repos`. A cold load of
   `/repos` rendered the same page.
2. The header count was `1`, for one row, `acme/widgets`, with state chips
   and `12 tasks`.
   - The chips add up to 13 because the **Blocked 1** chip overlays a task
     that is also counted under **queued 3**: task 11 is `queued` and
     `blocked`. This is the documented design: the blocked chip is
     error-coloured and drawn separately (`RepoList.jsx`).
3. **Add repo** (inline `owner/name` box, Enter to submit):
   - `acme/gadgets` and
     `example-corporation/exceptionally-long-repository-name-for-responsive-layout-checks`
     were both added with no confirmation, which is correct because the
     allowlist was already non-empty.
   - `not a repo` was refused inline with
     `repo: repo must be owner/name, got "not a repo"` (HTTP 400). The typed
     text was kept.
   - With the browser offline, adding `acme/offline` showed "Failed to fetch"
     and kept the typed text.
4. **Search**: `GADG` showed `acme/gadgets` only, and the count followed to
   `1`. `zzz-none` showed "No repos match your search."
5. **Sort**:
   - *Name*: alphabetical, with no drag handles.
   - *Most tasks*: `acme/widgets` first, with no handles.
   - *Custom order*: three `Move …` handles.
6. **Keyboard/tap reorder**:
   - Focused `Move <long repo>` and pressed Enter. The banner read "Moving 1
     repo — choose where it goes", with slots "Move here, above
     acme/gadgets" and "Move here, above acme/widgets". Enter on the first
     slot moved the long repo to the top.
   - Picking up `acme/gadgets` and pressing **Escape** closed every slot.
7. **Pointer drag**: dragged `acme/gadgets`'s handle onto the first row. The
   order became gadgets, long, widgets, and survived a reload
   (`localStorage["grain.list-order.repos"]`).
8. **Long name at 390 px**:
   - No horizontal overflow on the list or on the repo page
     (`scrollWidth == clientWidth == 390`).
   - The name is cut with an ellipsis (609 px of text in a 144 px box) and
     carries the full name in `title`, as fixed on 2026-10-01.

### 2. Repo page (`/repos/acme/widgets`)

1. **Navigation**:
   - Clicking the row name opened `/repos/acme/widgets`. **← repos**
     returned to `/repos`, and browser Back reopened the repo.
   - The **12 tasks filed here** link (`<a href="/?f=repo:any:acme/widgets">`)
     showed 12 rows and the chip "repo is any of acme/widgets". Back
     returned to the repo page and Forward to the filtered list.
2. **File a task from the repo page**: **New task** opened `/new` with
   `select[name=repo]` preselected to `acme/widgets`.
   - **Base branch** (`BranchField`) fetched the stubbed GitHub listing on
     focus, offered all four branches, filtered `cand` to `candidate`, and
     the pick was taken.
   - **Writes**: `acme/gad` offered `acme/gadgets` and `add acme/gad`; Enter
     took `acme/gadgets`.
   - **Reads**: `example` offered the long repo; Enter took it.
   - The drawer did not overflow (`560 == 560`).
   - **Create task** returned to the repo page, which read "13 tasks filed
     here", including after a reload. `GET /api/tasks` showed task 13 with
     repo `acme/widgets`, base `candidate`, writes `[{repo: acme/gadgets}]`,
     reads `[<long repo>]`, state `queued` and capabilities
     `gcp-key, gemini-key, self-debug`.
   - After a second create, the reopened form had an empty title and base
     `main`. That is `lastBaseForRepo` remembering the last base used in
     this repo, by design.
   - **Branch listing boundary**:
     - empty `[]`: no menu, no error;
     - 3 s delay: no spinner, then the suggestions;
     - 502: no menu and no banner. The box stays usable as plain text, and
       the request is logged as a console error.
3. **Draft vs page repo (follow-up filed)**:
   - Typed a title and base `feature/x` into a new task opened from
     `acme/gadgets`, then pressed Escape. The draft was kept in
     `localStorage["grain.task.draft"]`.
   - Opened **New task** from the `acme/widgets` page. The form opened
     targeting **acme/gadgets**, with the gadgets title and base.
   - The draft wins over the page the button was pressed on, with nothing
     saying so beyond the form's discard-draft button.
   - This is deliberate in `NewTaskOverlay.jsx` (a draft's repo always
     beats `defaultRepo`), so it was filed as a design question rather than
     changed here.
4. **Branch rules**:
   - `bad..branch` was refused with "invalid branch name" (400).
   - `walkthrough/topic-1`, `-2` and `-3` were added and listed newest
     first, and stayed listed across reloads.
   - The global error banner from the refused name stayed on screen for up
     to 5 s after a later successful add. That is the banner's auto-hide
     timer, not a stuck error.
5. **Compare** with base `main` and head `walkthrough/topic-1` went to
   `/repos/acme/widgets/compare/main...walkthrough/topic-1`. With the
   stubbed comparison:
   - success rendered the side-by-side diff of `README.md` (+1 −0);
   - empty said "There are no changes between these branches.";
   - the 3 s delay showed "Loading the comparison…" and then the diff;
   - 502 showed `GitHub compare failed: 502 Bad Gateway`. As noted on
     2026-10-01, the error text appears twice, once inline and once in the
     banner.
   - The compare URL worked as a cold deep link, and Back returned to the
     repo page.
6. **CI capabilities**: offered gcp key, gemini key, github sandbox,
   registry push, package publish and the three proxies.
   - Picking **gcp key**, and later **gemini key**, sent
     `PUT …/ci-capabilities {"ciCapabilities":["gcp-key","gemini-key"]}`.
     Both survived reload.
   - I also checked whether a later default-capability save, declarations
     toggle, unfixable-check add, dispatch-jobs save or ownership change
     disturbs the CI set. None did; `GET` after each step still returned
     `[gcp-key, gemini-key]`.
   - **Walkthrough note**: one script appeared to lose the CI pick. The
     cause was a crashed earlier attempt that had already saved `gcp-key`,
     so the re-run's click toggled it off. Not a defect.
7. **Default capabilities**: gcp key, gemini key and self debug are marked
   "already a deployment default -- on here either way". I added **memory**,
   **grain log** and **release notes**. The summary read "A task filed
   against acme/widgets starts with: Self debug, GCP key, Gemini key,
   Memory, Grain log, Release notes", and survived reload.
8. **Prompt extension / setup command**: set "Walkthrough prompt extension —
   2026-10-02." and `make deps && echo walkthrough-setup`. Both autosaved on
   blur and survived reload.
9. **Declarations**: toggled "Read acme/widgets's own declarations" with the
   keyboard (Space), on and then off. `PUT …/declarations` was sent, and the
   state survived reload.
   - As on 2026-10-01, the "Read at abc123commit: 1 template(s), 1 suite(s),
     1 schedule(s)" line still shows while reading is off.
10. **Checks no commit here can clear**: added `cla/assistant` and
    `cla/assistant2`. Both survived reload.
11. **Release workflows**:
    - **turn off automatic releases** → reload shows **turn on automatic
      releases** → turned back on.
    - **jobs dispatched after every merge** `build-cache, docs` saved and
      survived reload.
12. **Work ownership**: **release: on** sent
    `PUT …/work-ownership {"item":"release","state":"on"}`, and the row read
    "On, set here" after reload.
    - **Bug (fixed)**: the chosen button was distinguished only by its
      filled style. All three buttons had no `aria-pressed`, so a screen
      reader could not tell which state was chosen.
13. **Team overrides (repo-scoped custom configuration)**:
    - Switched the rail's team picker to **admin** (id `default`). The
      team-context banner appeared.
    - Changing the prompt extension sent
      `PUT /api/teams/default/repos/acme/widgets/config`. After reload the
      team value showed, while the global value from `GET …/capabilities`
      was unchanged.
    - **Revert to global defaults** asked "Revert team overrides for
      acme/widgets back to global repository defaults?":
      - dismissed: nothing changed;
      - accepted: `DELETE …/config`, and the box went back to the global
        text.

### 3. Memory (Team settings → Models tab)

1. `/team-settings` → **Models** tab. The tab does not change the URL, so
   it cannot be deep-linked; the memory panel sits at the foot of that tab.
   - **Copy bug (fixed)**: the prompt-extension help text above it said
     memory "lives on the Memory tab". There is no Memory tab.
2. The panel listed:
   - the deployment scope (271 characters, "written 2 days ago", "written
     by the memory agent");
   - the `acme/widgets` scope (130 characters, "edited here");
   - one document per scope: `release-checklist` (332 b) and `flaky-e2e`
     (208 b, "written 4 months ago");
   - an "another repo" picker for starting a new scope.
3. **Open full document**: **Open** on `flaky-e2e` fetched and showed the
   full 208-character text in a "Document: flaky-e2e" box, editable, with
   **Save document**. **Close** folded it again.
   - **Bug (fixed)**: every row's buttons were a bare "open" and "delete",
     so the pane exposed two identical "delete" buttons, and names can
     repeat across scopes.
4. **Delete confirmation**: **Delete** on `flaky-e2e` raised
   `confirm('Delete the memory document "flaky-e2e"? Nothing else keeps a copy of it.')`.
   Dismissing it left both documents in `GET /api/memory/documents`.
5. **Revert**: typed into the `acme/widgets` memory, then pressed
   **revert**. The text went back to what was stored and **save** was
   disabled again.
6. **Reversible correction to repo memory**:
   - Appended "Walkthrough 2026-10-02 correction: `make frontend` is now
     `make ui`." The character counter followed the text.
   - **save** sent `PUT /api/memory`.
   - Navigating back to `/team-settings` → Models re-read it verbatim. The
     scope read "written today / edited here", and `GET /api/memory` showed
     `updatedBy: operator`.
7. **Document correction**:
   - Appended a line to `flaky-e2e` and pressed **Save document**.
   - Reopening the pane and the document read the new text back, and the
     row showed "written today / edited here / 259 b".
8. **Restored**: both the memory and the document were put back to their
   original text through the same UI. `GET` confirmed the original memory
   text and document length 208.

### 4. Releases (`/repos/acme/widgets/releases`)

1. Focused **Releases** on the repo page and pressed Enter. This opened
   `/repos/acme/widgets/releases`, showing "acme/widgets has no releases
   yet…" and an empty qualification plan.
2. **Validation**:
   - An empty name was blocked by the browser ("Please fill out this
     field.").
   - `two words`, `../x` and `rel;rm` were refused by the server with
     "invalid release name" (400).
   - A duplicate `walk-a` was refused with "this repo already has an
     unmerged release with this name".
3. **Create**: `walk-a` and `walk-b` were created. The field cleared, and
   `walk-b` was selected as "Provisioning" with `walk-b.rc.1 -- cutting`,
   and with Cut, Promote and Merge all disabled.
4. **States through the helper**:
   - `walk-a`: provisioned, then rc.1 cut, so `walk-a.rc.1 -- active`.
   - `walk-b`: provisioned, then a cut error, shown as
     `walk-b.rc.1 -- cutting (Simulated: GitHub refused to create walk-b.rc.1 (422))`.
   - `walk-b` then also got a release-level error, shown beside the branch
     line as "latest: walk-b.latest, prod: walk-b (Simulated: GitHub could
     not create branch release/walk-b (403))".
5. **Switching**: in the picker (`walk-b -- active`, `walk-a -- active`),
   picking `walk-a` showed walk-a's own candidate within 200 ms. The
   2026-10-01 fix holds.
6. **Qualification plan**: **add item**, then the template picker
   (choose a template / nightly-maintenance / security-audit /
   release-qualification). Chose `nightly-maintenance` and pressed **save
   qualification plan**. It survived reload.
7. **Promote**: **promote current rc** asks
   `confirm("Promote walk-a.rc.1 to walk-a? This moves acme/widgets's walk-a branch to walk-a.rc.1.")`.
   - Dismissed: no request, still `active`.
   - Accepted: `POST …/candidates/promote`, and the candidate became
     `promoting`, then `promoted` after the helper.
   - The 2026-10-01 follow-up asking for this confirmation has landed.
8. **Cut**: **cut new rc** became enabled after promotion and needs no
   confirmation, since it creates a branch and moves nothing. It showed
   `walk-a.rc.2 -- cutting`, with history rc.2 and rc.1.
   - After the helper cut it and synced qualification: `walk-a.rc.2 --
     active`, a "qualification · running" block, and a task link
     `nightly-maintenance → /tasks/15`.
9. **Task link**: Enter on the focused link opened `/tasks/15`. Back
   returned to the releases pane with `walk-a -- Active` still selected.
10. **Merge back**: **merge walk-a into the default branch** asks
    `confirm("Merge walk-a into acme/widgets's default branch? This opens a pull request on GitHub from walk-a.")`.
    - Dismissed: no request. Accepted: `POST …/releases/walk-a/merge`.
    - After the helper marked it merged, the picker read "walk-a --
      Merged", and the pane read "Merge requested -- pull request" with a
      link to `https://github.com/acme/widgets/pull/4242`
      (`target=_blank`, `rel=noreferrer`).
    - On the merged release, Cut and Promote are disabled and the merge
      button is gone.
11. **Deep links**:
    - `/repos/acme/widgets/releases` opens the pane cold.
    - `/repos/acme/widgets/releases/walk-a` redirects to the pane, which
      does not select `walk-a` (known; the router has no per-release
      path).
    - `/repos/nobody/missing/releases` now says the repo "is not on this
      deployment's target repo list, so a release cannot be created in it"
      and offers no create form. The 2026-10-01 follow-up landed.
12. **Offline**: with the network offline for 7 s, the pane kept its last
    data. The app poll logged `grain: poll failed TypeError: Failed to
    fetch` as a console warning and showed no banner, and the pane recovered
    when the network came back.

### 5. Release trains and manual releases

The demo still has no release-train page: `/repos/acme/widgets/trains`
falls back to the repo page and `/releasetrains` to the task list, with no
errors. The train is the base-branch release, in the repo page's **release
workflows** section.

1. **run release by hand** shows a "Release branch" form.
   - An empty branch was blocked by the browser.
   - Unstubbed, `main` returned 404 "this deployment has no grain CI wired".
   - Stubbed 502: the error `grain CI: runner pool unavailable` showed, and
     the form stayed open.
   - Stubbed delayed 201: **Run release** was disabled while the POST was
     in flight (the 2026-10-01 fix holds). It then handed off to the run
     view, whose real `GET /api/ci/runs/rr-0001` 404s, as expected for a
     stubbed id.
2. Custom secrets: "This UI is not connected to the deployment's encrypted
   secrets store." "No custom release secrets configured."

### Console and network

No page errors on any screen. The only console errors were:

- the expected 4xx/5xx from deliberate invalid input and stubbed failures;
- the demo's real 404 for `github-branches` on the task detail page
  (unstubbed there);
- the stubbed run id's 404;
- `GET /api/tasks net::ERR_ABORTED`, which is the app poll cut off by a
  navigation or reload.

## Fixes in this change

1. **Memory document buttons are indistinguishable**
   (`ui/src/components/MemoryPanel.jsx`).
   - Each row's buttons are now named for the document and its scope:
     "Open flaky-e2e (acme/widgets)", "Delete release-checklist (this
     deployment)".
   - Open/Close carries `aria-expanded`.
   - Covered by a new `MemoryPanel.test.jsx` case. The existing cases
     failed without the fix once updated to the new names. Also covered by
     an assertion in `e2e/memory.spec.js`.
2. **Work-ownership state not exposed to assistive technology**
   (`ui/src/components/RepoWorkOwnership.jsx`).
   - `SetButtons` sets `aria-pressed` on the chosen state.
   - Covered by a new `RepoWorkOwnership.test.jsx` case that fails without
     the fix, and by assertions in `e2e/repo-ownership-ci-security.spec.js`.
3. **Stale "Memory tab" copy** (`ui/src/components/TeamSettingsOverlay.jsx`)
   now points to the memory section at the foot of the Models tab.

Verified in the browser on a rebuilt server with a fresh store:

- `release: off` was pressed and survived reload as `aria-pressed=true`,
  with the other two `false`;
- the document buttons carried the new names, and after opening,
  `Close flaky-e2e (acme/widgets)` had `aria-expanded=true`;
- the corrected copy rendered.

Tests run:

- `vitest` for `MemoryPanel`, `RepoWorkOwnership` and `TeamSettingsOverlay`
  (45 passed).
- Playwright Chromium for `e2e/memory.spec.js`,
  `e2e/repo-ownership-ci-security.spec.js` and
  `e2e/repository_release_memory_walkthrough.spec.js` (6 passed).

## Follow-up filed (branch `candidate`)

- **A stored new-task draft overrides the repo whose page "New task" was
  pressed on.** It includes the reproduction from §2.3.

## Minor observations (not filed)

- The compare page shows a GitHub error twice (inline and in the banner).
- The declarations "Read at …" line still shows while reading is off.
- `/repos/acme/widgets/releases/<name>` does not select that release.
- Team settings tabs are not in the URL, so the memory panel cannot be
  deep-linked.
- `BranchField` shows no loading or error hint while the GitHub listing is
  slow or failing; the box stays a plain text field, which is a safe
  fallback.
- The scope memory **save**/**revert** buttons are still bare "save" and
  "revert". They sit directly under their labelled textarea, and renaming
  them would touch several existing specs. Left for a wider naming pass.
- The repo-page **remove** button and a workflow's **remove** button still
  share the bare name "remove" (noted on 2026-10-01).
