# Repository, merge queue, compare and release UI walkthrough (2026-10-23)

Task 4673. A walk through the repository-centric operator workflow in a
real browser against a real `grain demo` server, following up
[2026-10-22](2026-10-22-repository-release-memory-ui.md).

## Environment

- **Commit tested**: `5ca6b0c89` (`origin/candidate` tip, the merge of
  #4457). Fixes from this run were then verified on the same tree plus this
  branch's commits.
- **Platform**: Linux 6.1.0 x86_64 sandbox, Go 1.26.6, Node.js 20.19.0,
  Playwright 1.62.1 driving headless Chromium 151.0.7922.34.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -o /tmp/grain ./google/cmd/grain` (the binary
  `ui/e2e/global-setup.js` builds), so the frontend served is the one
  built from this tree.
- **Server**, over a throwaway embedded store in `$TMPDIR/grain-demo-*`:

  ```bash
  /tmp/grain demo -addr 127.0.0.1:8431 -open=false \
    -access-code-file <temp file> \
    -github-sim -fake-ci -scheduled-workflows -base-branch-ci \
    -secrets-store -merge-rules -merge-queue-states -qualification \
    -reconcile=500ms
  ```

- **Browser driving**: ad-hoc Playwright scripts outside the tree (not
  committed), signing in through the access-code page and then using only
  visible controls: clicks, typing, Tab/Enter/Escape, browser Back/Forward,
  reloads and deep links. Every script logged console errors and warnings,
  page errors, failed requests and every HTTP response of 400 or above.
  The server's state was read back with `curl` only to confirm a result
  already seen in the UI.
- **Viewports**: 1280x800 desktop and 390x844 phone, light and dark.
- **Gemini**: the sandbox carries no `/home/debian/.gemini-api-key`, so
  nothing here used Gemini. Screenshots were checked by bounding box and
  overflow measurements and by looking at contact sheets.

## Stubs

grain's API and React frontend were not stubbed or intercepted. The only
external boundary is GitHub, and the demo's own simulators covered it:

1. **`-github-sim`**: a simulated GitHub (`pkg/github/githubsim`) over a real
   bare repo, read through grain's real GitHub client. To have something to
   compare, I cloned that bare repo over `file://` and pushed
   `feature/compare-me` (adds `RELEASE.md` and `src/answer.go`).
   Release provisioning, RC cuts and promotion ran for real against it
   through the `-reconcile` loop, so this run did not need the release
   helper (`ui/e2e/releasehelper`) or any intercepted
   success/empty/loading/failure response. GitHub's failure answers came
   from the simulator itself: an unknown ref, and a repo it does not host
   (`acme/gadgets`).
2. **`-fake-ci` / `-base-branch-ci`**: grain CI accepts runs and never
   executes them.
3. **`-secrets-store`, `-merge-rules`, `-merge-queue-states`,
   `-qualification`**: demo seeds, as their flag help describes.

## Notes on the task's wording

- Repository pages live at `/repos/<owner>/<name>/…`. The
  `/t/<team>/repos/…` form the task names was removed by the single-tenant
  reset, and `/t/…` paths now normalise to `/` (`paths.test.js`).
- The repo page has no read/write repository relationship editor and no
  repo-scoped custom configuration on this branch. Reads and writes are per
  task (the New task form's *Read-only repos* / *Another repo*). Port
  directions and paired branches are shown read-only on **Advanced** and are
  set in grain/config or `.grain/settings.yml`. "Branch rules (base/head)"
  were walked as the Branches tab's add-branch form and the compare form.
- "Release trains" are the releases on `/repos/<o>/<n>/releases/<name>`,
  each with its latest/rc/prod branches and its RC history. No separate
  train page exists.

## Steps and outcomes

### 1. Repos list

1. From `/` I clicked **Repos** in the rail and landed on `/repos`, then
   opened `/repos` directly with a reload. The header count read `3` after
   two repos were added. With one target repo, the one-repo allowlist
   notice showed.
2. I typed `acme/gadgets` and then an 82-character
   `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`
   into **Repo to add** and clicked **Add repo**. Both rows appeared. The
   discovery line said no repositories were discovered and why, which is
   correct for a demo with no Git connection.
3. Search: `gadg` left 1 row and the count followed it to `1`. `zzz-none`
   showed "No repos match your search."
4. **Sort**: *Name (A–Z)*, *Active first* and *Most tasks* each ordered as
   expected and hid the drag handles. *Custom order* brought back three
   **Move …** handles.
5. Reordering:
   - Tap: I clicked **Move acme/gadgets**. The "Moving 1 repo — choose where
     it goes" banner and two drop slots appeared. Clicking the last slot
     moved the row to the end.
   - Keyboard: I focused a handle and pressed Enter, then focused the last
     slot and pressed Enter. The row moved and focus returned to its own
     **Move …** handle. Escape cancelled a pick and kept focus on the
     handle.
   - Pointer: a mouse drag from the first handle to the third moved the row.
   - The order survived a reload.
6. Layout: at 1280 the long name renders in an ellipsised 860 px cell with
   its full name in `title`. At 390 the rows are 390 px wide and nothing
   overflows horizontally.

### 2. Repo page, task count and filing a task

1. I clicked the `acme/widgets` row and got `/repos/acme/widgets`, with
   state chips and **17 tasks filed here**. **← Repos** went back, and
   browser Back and Forward stepped between the two pages.
2. Clicking **17 tasks filed here** opened `/?f=repo:any:acme/widgets`
   with 17 rows and a "repo is any of acme/widgets" chip. Back returned to
   the repo page. Ctrl+click opened the list in a new tab and left this one
   in place. The plain click does a full page load rather than an in-app
   navigation (observation 1).
3. From `/repos/acme/gadgets` I clicked **New task**. The form opened at
   `/new` with **Repos** preselected to `acme/gadgets`; from
   `/repos/acme/widgets` it preselected `acme/widgets`.
   - Reloading mid-form kept the draft title and the preselected repo.
   - Escape closed the form back to the repo page.
   - **Create task** returned to `/repos/acme/gadgets`, now showing
     `queued 1 · 1 task filed here`. That survived a reload.
   - The task-count link's list held the new task, and opening it showed
     `/tasks/19` with its description.

### 3. Repo settings

| Tab | Edit made through the UI | Request | After reload |
|---|---|---|---|
| CI | **CI capabilities** → Package publish, then Gemini proxy | `PUT …/ci-capabilities` | both chips present |
| CI | **Add workflow** `lint.yml` with `runs-on: ubuntu-latest` | `PUT …/workflows` 200 | stored |
| CI | the same with `runs-on: grain` | `PUT …/workflows` 400 | refusal shown in an alert ("runs-on must be "ubuntu-latest" …"). Leaving the page raised the browser's unsaved-changes prompt. **Remove workflow bad.yml** cleared it |
| Agents | **Default capabilities** → Memory | `PUT …/capabilities` | chip present; "starts with: Self debug, GCP key, Gemini key, Memory" |
| Agents | **Prompt extension** `Run \`make lint\` before pushing.` | `PUT …/prompt-extension` | value kept |
| Agents | **Setup command** `make deps` | `PUT …/setup-command` | value kept |
| Merge queue | **Check that cannot pass** `cla/google`, then `CLA/GOOGLE` | `PUT …/unfixable-checks` | one entry; the second was refused inline, case-insensitively |
| Branches | **Add branch** `task-4673/operator-branch`, twice | `POST …/branches` ×2 | one row (`pending`), plus "already on this repo's list (pending); nothing new was added." |
| Advanced | Declarations | — | "Read at 2b4dc4dab2f9: 0 template(s), …"; Ports table and Remove repository render |

I confirmed through the API that saving one setting never cleared
another: each PUT left the CI capabilities, prompt, setup command and
unfixable checks as they were.

### 4. CI mode, base-gate warnings and the merge queue

1. On `acme/widgets` I ticked **Watch acme/widgets's default branch…**
   while in grain CI mode, then switched to **Use GitHub CI**. No warning
   appeared, because the reconciler had already enrolled the branch, so
   it is judged. That matches `baseBranchGateOf`: grain-ci-off is reported
   only for a branch never enrolled.
2. On `acme/gadgets`, never enrolled:
   - I chose **Use GitHub CI** and then ticked the watch box. Without a
     reload, the header showed the error alert "Nothing is testing
     acme/gadgets's default branch, …" with **Go to CI Mode**. It read as
     written (not lowercased) and is the same on Overview.
   - **Releases** showed the shorter form of the same alert with **Open
     repository settings**.
   - Back, then **Go to CI Mode**, opened `/repos/acme/gadgets/settings/ci`
     with the CI Mode radios in view.
   - **Use No CI** kept the warning. **Make grain CI the gate…** cleared it
     on the header and on Releases without a reload.
3. Settings → Engine, Merge queue:
   - `soon` in **Merge gather window** was refused ("Couldn't save Merge
     gather window", `time: invalid duration "soon"`).
   - `-3` in **Merge batch size** was refused ("must be at least 1 … or 0").
   - **Max merges per repo branch** `2`, **Merge batch size** `3` and
     **Merge gather window** `45s` each saved ("All changes saved") and
     survived a reload.
4. Merge batch panel:
   - On `/repos/acme/widgets` it showed `2 changes on main · testing ·
     5m of 90m`, the temp ref, and #101 and #102 linked to their tasks and
     PRs.
   - Keyboard: I focused *Rotate the Gemini signing key* and pressed Enter.
     `/tasks/5` showed its own Merge batch panel, and Back returned to the
     repo page.
   - `/tasks/6` (#102, the batch's second member) showed **no** Merge batch
     panel (bug 1).

### 5. Compare branches

1. In the Branches tab I set Base `main` and Head `feature/compare-me` and
   clicked **Compare**. That opened
   `/repos/acme/widgets/compare/main...feature/compare-me` with "open ·
   Into main on acme/widgets -- forked at …", side-by-side/inline/raw
   toggles, and `RELEASE.md` (+3) and `src/answer.go` (+3).
2. Reload, Back and Forward all kept the page. A direct link without the
   dots (`/compare/feature/compare-me`) compared against the default
   branch. At 390 nothing overflowed.
3. `main...release/1.0` and `main...main` each read "merged … There are no
   changes between these branches."
4. `main...no-such-branch` and `acme/gadgets/compare/main...dev` (a repo
   the simulator does not host) answered 502 and read "an upstream service
   returned an error (ref …)" (bug 2).
5. The Base/Head boxes offered no suggestions from the repository (bug 3).

### 6. Releases

1. `/repos/acme/widgets/releases` showed release `1.0 -- Active`, its
   branches, current candidate `1.0.rc.1 -- active`, and a qualification
   awaiting approval with **Approve all** and a link to the **Qualify** task
   (`/tasks/18`; Back returned).
2. Create-release validation:
   - `bad release name` and `x.latest` were refused with the naming rules.
   - `1.0` was refused with "already has an unmerged release with this
     name".
   - An empty name was stopped by the browser's required-field check.
3. Creating `wt2610` navigated to `/repos/acme/widgets/releases/wt2610`.
   The reconciler provisioned it against the simulator: `Active`, with
   `wt2610.rc.1 -- active`.
4. Confirmations:
   - **Promote current RC**: "Promote wt2610.rc.1 to wt2610? This moves
     acme/widgets's wt2610 branch to wt2610.rc.1." Dismissing it changed
     nothing. Accepting it gave `rc.1 -- promoted`, which survived a
     reload.
   - **Merge wt2610 into the default branch**: "… This opens a pull request
     on GitHub from wt2610." I dismissed it.
   - **Delete release**: "Remove release wt2610? This drops the release and
     its candidates from grain." Dismissing kept it. Accepting removed it,
     returned to `/releases`, and the removal survived a reload.
   - **Cut new RC** asks nothing. Once rc.1 was promoted it gave
     `wt2610.rc.2 -- active` with history `rc.2 active · rc.1 promoted`.
5. Qualification plan: **Repeat** `2` and **Promote automatically once
   qualification succeeds**, then **Save qualification plan**. Both
   survived a reload.
6. The release picker switched to `1.0` at `/releases/1.0`, and Back
   returned to `wt2610`.
7. An unknown release (`/releases/nope-404`) silently fell back to
   `/releases` (observation 3).
8. At 390 nothing overflowed.

### Console and network

- Expected: a 401 on the first `GET /` before signing in, and the 400s and
  404s/502s of the refusals above, each shown in the UI.
- `GET /api/tasks` is often logged `net::ERR_ABORTED` at navigation or
  reload: the in-flight list poll is cancelled.
- `DELETE …/releases/<name>` is logged `ERR_ABORTED` even though it
  succeeds: `api.js` never reads a 204's empty body, and Chromium reports
  it that way. The release was gone after a reload.
- No page errors and no React warnings.

## Bugs found and fixed

### Bug 1: a merge batch's second member showed no batch on its own page

- **Reproduction**:
  1. Run `grain demo` and open `/repos/acme/widgets`.
  2. In **Merge batches**, click *Cache the capability readiness probe*
     (#102, task 6).
  3. `/tasks/6` has no Merge batch panel. `GET /api/tasks/6` has no
     `mergeQueueState`, while task 5 has `batching`.
- **Cause**: `seedDemo` (`pkg/demo/demo.go`) wrote the batching state only
  on task 5. The real queue writes it on every member, and
  `DetailOverlay` draws the panel only for a task in that state. The
  2026-10-22 fix (ff622301b) added the state to #5 on the belief that #6
  already had it. #6 never did.
- **Fix**: seed every member, with the first-in-batch note on the first
  only, as the orchestrator words it.
- **Regression**: `TestSeedDemoMarksEveryMergeBatchMemberBatching` in
  `pkg/demo/demo_test.go` fails on the old seed ("batch member 1 (task 6)
  is not batching").

### Bug 2: an unknown compare branch read as an upstream outage

- **Reproduction**: open
  `/repos/acme/widgets/compare/main...no-such-branch`. The page reads "an
  upstream service returned an error (ref …)", the API answers 502
  `upstream_unavailable`, and the daemon log shows `GitHub API error 404`.
- **Fix**: `handleCompareBranches` (`pkg/ui/branches.go`) answers GitHub's
  404 with a 404 in grain's own words, naming only what the caller sent:
  "GitHub has nothing to compare for main...no-such-branch in acme/widgets:
  one of the two branches does not exist, the two share no history, or
  this deployment's credential cannot see acme/widgets". I added "share no
  history" after hitting that case myself: a branch pushed from an earlier
  demo's repo got the same 404. Other failures stay 502 with GitHub's text
  only in the log (#2424).
- **Regression**: `TestCompareBranchesAnswersAnUnknownBranchAsNotFound`
  in `pkg/ui/compare_test.go`, and the e2e spec below.

### Bug 3: Compare branches suggested only grain-created branches

- **Reproduction**: on `/repos/acme/widgets/settings/branches`, focus
  **Base branch** or **Head branch**. The `<datalist>` holds only the
  branches grain was asked to create (`task-4673/operator-branch`, still
  `pending` and not on GitHub). It never offers `main`, `candidate`,
  `release/1.0` or a pushed `feature/compare-me`.
- **Fix**: both boxes are now `BranchField` (`RepoPage.jsx`). It lists
  GitHub's branches (`/github-branches`) with grain's own, keyboard
  navigable, and the head box warns `No branch "x" on acme/widgets: there
  is nothing to compare`. The base box does not warn: it starts as `main`,
  every tab stays mounted, and a warning there would ask GitHub on every
  repo page view. Each picker sits in a flex Box so the pair fills the
  row, as the text boxes did; `BranchField`'s root is a plain block, and
  without the Box each stopped at 228 px.
- **Regression**:
  - Unit: "offers the repository's own branches to compare, not only the
    ones grain created" in `RepoPage.test.jsx`.
  - E2E: `ui/e2e/repo-compare-picker.spec.js` against `grain demo
    -github-sim` covers the suggestions, the row layout by bounding box,
    the missing-branch warning, the 404 sentence (and no "upstream
    service"), and a compare picked from the list.

### Bug 4 (copy): an internal function name in a sentence an operator reads

The Releases pane's qualification plan hint ended "… so the API refuses
those (ui.Client.PutQualificationPlan) and this picker leaves them out."
It now ends "so grain refuses those and this picker leaves them out", and
the function name moved to a code comment.

### Screenshots

Taken before (base build) and after (this branch), at 1280x800 and 390x844
in light and dark. Pages: the branches tab with the head suggestions open,
`compare/main...no-such-branch`, `releases/1.0` and `/tasks/6`.

- No horizontal overflow on any page.
- The suggestion menu opens below the head box on desktop and above it on
  phone, inside the viewport both times.
- The screenshots are not committed.

## Observations not fixed

1. **N tasks filed here** on the repo page is a plain `href`, so a left
   click reloads the whole app rather than navigating in place. The URL,
   Back and modified clicks are all correct. Doing it in place needs a new
   App-level navigation prop for a narrowed list. That is cosmetic, so I
   left it.
2. **Cut new RC** and **Promote current RC** are disabled with no reason
   given: Cut while a candidate is active, Promote once it is promoted.
3. An unknown release in the URL (`/releases/nope-404`) silently falls back
   to the list's first release without saying the named one was not found.
4. A refused compare or workflow save shows its message twice: once in
   the page and once in the app's error alert.
5. The **Repos** field on the New task form is a native `<select>` inside a
   `<label>`. Its accessible name runs on into every option's text.
6. Ticking **Watch … default branch** while the repo is on grain CI and
   then switching to GitHub CI leaves an enrolled branch with no warning,
   by design (`baseBranchGateOf` reports grain-ci-off only for a branch
   never enrolled). Whether grain should still say something there is a
   product question, not a fault in what is shown.

## Tests run

- `go test ./pkg/demo -run 'TestSeedDemoMarksEveryMergeBatchMemberBatching|TestSeedDemoCoversEveryState'`
- `go test ./pkg/ui -run TestCompareBranches`
- `npx vitest run src/components/RepoPage.test.jsx` (75 passed) and
  `src/components/RepoReleases.test.jsx` (31 passed)
- `npx playwright test e2e/repo-compare-picker.spec.js
  e2e/repo-release-memory-inputs-walkthrough.spec.js
  e2e/repo-tabs-walkthrough.spec.js
  e2e/repository_release_memory_walkthrough.spec.js` (10 passed)
