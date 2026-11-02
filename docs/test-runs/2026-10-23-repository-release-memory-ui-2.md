# Repository, merge queue, compare and release UI walkthrough (2026-10-23, task 273)

A walk through the repository-centric operator workflow in a real browser
against a real `grain demo` server. It follows
[the morning run](2026-10-23-repository-release-memory-ui.md) (task 4673).
That run already holds today's `YYYY-MM-DD-repository-release-memory-ui.md`
name, so this second run of the day takes a `-2` suffix rather than
overwriting it, the same convention the `interim-refactor-check-N` reports
use. Task 4673's six observations are rechecked below.

## Environment

- **Commit tested**: `66b743a5d` (`candidate` tip, the merge of #4528). The
  fixes below were then checked on the same tree plus this branch's commits.
- **Platform**: Linux x86_64 sandbox, Go 1.26, Node.js 20.19.0, Playwright
  driving headless Chromium from `/var/cache/playwright`.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -o /var/tmp/grain a wrapper distribution's `cmd/grain`. That
  binary embeds the frontend built from this tree.
- **Server**, over a throwaway embedded store in `/tmp/grain-demo-*`:

  ```bash
  /var/tmp/grain demo -addr 127.0.0.1:8431 -open=false \
    -access-code-file <temp file> \
    -github-sim -fake-ci -scheduled-workflows -base-branch-ci \
    -secrets-store -merge-rules -merge-queue-states -qualification \
    -reconcile=500ms
  ```

  The server was restarted three times, each time on a fresh store, to
  serve each rebuilt frontend. Steps 1–5 ran on the base build and step 6
  on the build with the fix.
- **Driving**: ad-hoc Playwright scripts outside the tree (not committed).
  Each one signed in through the access-code page and then used only
  visible controls: clicks, typing, Tab/Enter/Escape/arrow keys, browser
  Back/Forward, reloads and deep links. Every script logged console errors
  and warnings, page errors, failed requests, dialogs, and every response
  of 400 or above.
- **Viewports**: 1280x800 for the walk. 1440x900 and 390x844 in light and
  dark for screenshots and layout checks.
- **Gemini**: there is no `/home/debian/.gemini-api-key` in this sandbox,
  so nothing here used Gemini. The screenshots were checked by
  bounding-box and overflow measurements in the browser.

## Stubs

grain's API and React frontend were not stubbed or intercepted. GitHub is
the only external boundary, and the demo's own simulators covered it:

1. **`-github-sim`** simulates GitHub (`pkg/github/githubsim`) over a real
   bare repo, read through grain's real GitHub client. To have something to
   compare, I cloned that bare repo over `file://` and pushed
   `feature/compare-me`, which adds `RELEASE.md` and `src/answer.go`.
   Release provisioning, RC cuts and promotion ran for real against the
   simulator through the `-reconcile` loop. That meant no release helper
   and no intercepted success, empty, loading or failure responses were
   needed. The failure answers came from the simulator itself: an unknown
   ref, and a repo it does not host (`acme/gadgets`).
2. **`-fake-ci` / `-base-branch-ci`**: grain CI accepts runs but never
   executes them.
3. **`-secrets-store`, `-merge-rules`, `-merge-queue-states`,
   `-qualification`**: demo seeds, as described in their flag help.

## Notes on the task's wording

- Repository pages live at `/repos/<owner>/<name>/…`. A `/t/<team>/…` link
  from before the single-tenant reset opens the team-less page.
  `/t/default/repos/acme/widgets/compare/main...feature/compare-me` landed
  on `/repos/acme/widgets/compare/main...feature/compare-me` with the diff.
- The repo page has no editor for read/write repository relationships and
  no repo-scoped custom configuration. Reads and writes are set per task on
  the New task form. Ports are shown read-only and set in grain/config or
  `.grain/settings.yml`. I treated "branch rules (base/head)" as the
  Branches tab's add-branch form plus the compare form.
- "Release trains" are the releases at `/repos/<o>/<n>/releases/<name>`,
  each with its latest/rc/prod branches and its RC history.

## Steps and outcomes

### 1. Repos list

1. I clicked **Repos** in the rail from `/` and landed on `/repos`. A
   direct load and a reload of `/repos` also worked. I added two repos
   through **Repo to add** / **Add repo**: `acme/gadgets` and the
   82-character
   `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name`.
   After a reload the list had 3 rows and the rail count read 3.
2. **Filter added repos…**: `gadg` left 1 row. `zzz-none` showed "No repos
   match your search."
3. **Sort**: *Name (A–Z)*, *Active first* and *Most tasks* each ordered the
   rows correctly and disabled every drag handle. *Custom order* brought
   back 3 enabled **Move …** handles.
4. Reordering:
   - Tap: **Move acme/widgets** showed the "Moving 1 repo — choose where it
     goes" banner with 2 slots. Clicking the last slot moved the row to the
     end.
   - Keyboard: I pressed Enter on the handle, then Enter on the first slot.
     The row moved to the top and focus came back to **Move acme/widgets**.
     Escape cancelled a pick, cleared the slots and left focus on the
     handle.
   - Pointer: dragging the first handle onto the third moved the row.
   - The order survived a reload.
5. Layout: at 1280 the long name sits in an ellipsised 860 px cell with
   its full name in `title`. At 390 nothing overflows horizontally.
6. Clicking the `acme/widgets` row opened `/repos/acme/widgets`. **← Repos**
   went back, and browser Back and Forward stepped between the two pages.
7. **17 tasks filed here** is `<a href="/?f=repo:any:acme/widgets">`. It
   opened the list narrowed to "repo is any of acme/widgets", and Back
   returned to the repo page.

### 2. Filing a task from the repo page

1. **New task** on `/repos/acme/gadgets` opened `/new` with **Repos** set
   to `acme/gadgets`.
2. A reload mid-form kept both the draft title and the repo.
3. **Create task** without a reload returned to `/repos/acme/gadgets`,
   which went from `1 task filed here` to `2 tasks filed here`. The count
   held after a reload, and the task appeared in the narrowed list.
4. After a reload mid-form, closing or creating returned to `/`, not to
   the repo page. `/new` parses as an overlay over the task list
   (`paths.js`), so the page behind it is not part of its address. That is
   by design, and I left it as observation A.

### 3. Repo settings (`acme/widgets`)

| Tab | Edit made through the UI | Request | After reload |
|---|---|---|---|
| CI | **CI capabilities** → Package publish | `PUT …/ci-capabilities` | chip present |
| CI | **Add workflow** `.grain/workflows/lint273.yml`, `runs-on: ubuntu-latest` | `PUT …/workflows` | file and YAML kept |
| Agents | **Default capabilities** → Memory | `PUT …/capabilities` | "starts with: Self debug, GCP key, Gemini key, Memory" |
| Agents | **Prompt extension** ``Run `make lint` before pushing (task 273).`` | `PUT …/prompt-extension` | kept |
| Agents | **Setup command** `make deps` | `PUT …/setup-command` | kept |
| Merge queue | **Check that cannot pass** `cla/google`, then `CLA/GOOGLE` | `PUT …/unfixable-checks` | one entry; the second was refused inline, matched without case |
| Branches | **Add branch** `task-273/operator-branch`, twice | `POST …/branches` ×2 | one `pending` row, plus "already on this repo's list (pending); nothing new was added." |
| Branches | **Add branch** `bad branch..name` | `POST …/branches` 400 | **bug 2**: the alert said only "invalid branch name" |
| Advanced | Declarations | — | "Read at aa88a07e3750: 0 template(s), …" |
| Advanced | **Remove** (dismissed) | none | confirm: "Remove acme/widgets from target repos? …". Dismissing it left the repo in place |

### 4. CI mode, base-gate warnings and the merge queue

1. On `acme/gadgets`, which has never been enrolled, I chose **Use GitHub
   CI** and ticked **Watch acme/gadgets's default branch…**. Without a
   reload, the header showed the error alert "Nothing is testing
   acme/gadgets's default branch, and the merge queue is merging onto it
   anyway. …" with **Go to CI Mode**. The **Overview** and **Releases**
   tabs showed the same alert. Unticking the box cleared it.
2. **Go to CI Mode**, from the header on Overview and from Releases, opened
   `/repos/acme/gadgets/settings/ci` with the CI Mode radios visible.
3. **Use No CI** kept the warning. **Make grain CI the gate…** cleared it
   on the settings tab and on Releases without a reload. Grain CI was
   still selected after a reload.
4. Settings → Engine, Merge queue:
   - `soon` in **Merge gather window** was refused with
     `mergeGatherWindow: time: invalid duration "soon"`.
   - `-3` in **Merge batch size** was refused with "mergeBatchSize must be
     at least 1 …".
   - `3`, `45s` and **Max merges per repo branch** `2` each saved and
     survived a reload.
   - The refusals name the JSON fields rather than the labels; see
     observation B.
5. Merge batch panel:
   - `/repos/acme/widgets` showed "2 changes on main · testing · 14m of
     90m", the temp ref, and #101 and #102.
   - Pressing Enter on *Rotate the Gemini signing key* opened `/tasks/5`
     with its own **Merge batch** panel, and Back returned to the repo.
   - *Cache the capability readiness probe* opened `/tasks/6`, which also
     has the panel. Task 4673's fix holds.

### 5. Compare branches

1. On the Branches tab, Base already read `main`. In Head I typed
   `feature/comp`, then ArrowDown and Enter picked `feature/compare-me`
   from the suggestions. **Compare** opened
   `/repos/acme/widgets/compare/main...feature/compare-me`, which showed
   "open · Into main on acme/widgets -- forked at aa88a07e",
   side-by-side/inline/raw, `RELEASE.md` (+3) and `src/answer.go` (+3).
2. Reload, Back and Forward all kept the page. A direct link without the
   dots (`/compare/feature/compare-me`) compared against the default
   branch. The `/t/default/…` form opened the same page. At 390 nothing
   overflowed.
3. `main...main` read "Merged … There are no changes between these
   branches."
4. `main...no-such-branch` answered 404 and showed "GitHub has nothing to
   compare for main...no-such-branch in acme/widgets: one of the two
   branches does not exist, …". `acme/gadgets/compare/main...dev`, a repo
   the simulator does not host, answered 502 with "an upstream service
   returned an error (ref …)". Both show once in the page and once in the
   app alert, and `PullRequestPage.jsx` says that is intended.

### 6. Releases (rebuilt with the fix)

1. `/repos/acme/widgets/releases` showed `1.0 -- Active`, its branches,
   current candidate `1.0.rc.1 -- active`, the qualification awaiting
   approval with **Approve all**, and **Cut new RC** disabled. On the base
   build there was nothing to say why (**bug 1**). With the fix, a line
   under the buttons reads "Cut new RC: 1.0.rc.1 is active: a new RC is cut
   once it is promoted."
2. `/releases/nope-404` showed the newest release, rewrote the address to
   `/releases`, and put up the app alert `no release "nope-404"`.
   Task 4673's observation 3 ("silently falls back") was wrong: the alert
   is there, at the top of the page.
3. Create-release validation:
   - `bad release name` and `x.latest` were refused with the naming rules.
   - `1.0` was refused with "already has an unmerged release with this
     name".
   - An empty name was stopped by the browser's "Please fill out this
     field."
4. `wt273` opened `/releases/wt273` while provisioning, with the reason
   "wt273 is still provisioning: grain cuts and promotes its candidates
   once its wt273.latest branch exists." The reconciler then provisioned
   it against the simulator: `Active`, `wt273.rc.1 -- active`.
5. **Promote current RC**:
   - Its confirm reads "Promote wt273.rc.1 to wt273? This moves
     acme/widgets's wt273 branch to wt273.rc.1." Dismissing it changed
     nothing.
   - Accepting it gave `rc.1 -- promoted` and the reason "Promote current
     RC: wt273.rc.1 is already promoted: a candidate is promoted once."
     Both survived a reload.
6. **Cut new RC** asks for no confirmation. It gave the history
   `wt273.rc.2 -- active · wt273.rc.1 -- promoted`.
7. **Merge wt273 into the default branch** asked "… This opens a pull
   request on GitHub from wt273." I dismissed it.
8. Qualification plan: **Repeat** `2` and **Promote automatically once
   qualification succeeds**, then **Save qualification plan**. Both
   survived a reload.
9. The picker switched to `1.0` at `/releases/1.0`, and Back returned to
   `wt273`.
10. **Delete release**: its confirm reads "Remove release wt273? This drops
    the release and its candidates from grain." Dismissing it kept the
    release. Accepting it removed the release, returned to `/releases` on
    `1.0`, and the removal survived a reload.
11. At 390 nothing overflowed. The reason wraps to two lines, 8 px under
    the buttons.

### Console and network

- Expected: a 401 on the first `GET /` before signing in, and the 400s,
  404s and 502s of the refusals above, each one shown in the UI.
- `GET /api/tasks` is logged `net::ERR_ABORTED` on navigation, when the
  in-flight list poll is cancelled.
- `DELETE …/releases/wt273` is logged `ERR_ABORTED` because the 204 body
  is never read. The delete itself succeeded, which a reload confirmed.
- There were no page errors and no React warnings.

## Bugs found and fixed

### Bug 1: Cut new RC and Promote current RC greyed out with no reason

- **Reproduction**: run `grain demo -github-sim -qualification` and open
  `/repos/acme/widgets/releases`. **Cut new RC** is disabled (`1.0.rc.1`
  is active) with no title or text saying why. After a promote, **Promote
  current RC** is disabled the same way. Task 4673 recorded this as
  observation 2.
- **Fix**: `releaseActionReasons` (`RepoReleases.jsx`) returns a sentence
  for each button that is off, using the same conditions as `canCut` and
  `canPromote`: not a repo admin, a release still provisioning or merged,
  a candidate still active, cutting or promoting, none cut yet, or one
  already promoted. The pane shows that sentence under the buttons
  (`data-testid="release-action-reasons"`).
- **Regression**:
  - Unit tests in `RepoReleases.test.jsx`: the helper's cases, and both
    reasons rendered.
  - E2E: `repository_release_memory_walkthrough.spec.js` asserts the text
    and that the reason sits directly under **Cut new RC**, by bounding
    box.

### Bug 2: a refused branch name did not say what was wrong

- **Reproduction**: on `/repos/acme/widgets/settings/branches`, add
  `bad branch..name`. The API answers 400 `{"error":"invalid branch
  name"}` and the alert repeats only that.
- **Fix**: `CreateBranch` (`pkg/ui/branches.go`) names the refused value,
  whether that is the branch or its `from`, and spells out
  `ValidBranchName`'s rule, as release names already do. The message still
  begins "invalid branch name", so existing matches keep working.
- **Regression**: `TestCreateBranchRefusalSpellsOutTheRule` in
  `pkg/ui/branches_test.go`.

### A change made and then withdrawn

I first added an in-pane "no release named nope-404 -- showing 1.0
instead" note, going by task 4673's observation 3. Then I found that App
already raises `no release "nope-404"` (`selectRelease` → `showError`), and
`repository_release_memory_walkthrough.spec.js` already asserts it. The
note only said the same thing twice, so I removed it before pushing the
final branch.

### Screenshots

I took before (base build) and after (this branch) shots of
`/repos/acme/widgets/releases` and `/repos/acme/widgets/releases/nope-404`
at 1440x900 and 390x844, in light and dark, with `ui/e2e/screenshot.mjs
--url`. They are not committed. The contact sheets are attached to the
task's closing note.

## Observations not fixed

- **A.** After a reload of `/new` opened from a repo page, the form closes
  to the task list, not to the repo. The repo stays preselected.
- **B.** The Engine tab's refusals name JSON fields
  (`mergeGatherWindow: time: invalid duration "soon"`, `mergeBatchSize
  must be …`) where an operator reads "Merge gather window".
- **C.** The base-gate alert's text was correct throughout, but a single
  alert runs to about 90 words.
- Task 4673's observations 1 (the task count link reloads the whole app),
  4 (refusals shown twice; intended on the compare page) and 5 (the
  `<select>` inside its `<label>`) still stand. Observation 2 is fixed
  above, and observation 3 was mistaken.

## Tests run

- `go test ./pkg/ui -run TestCreateBranch`
- `npx vitest run src/components/RepoReleases.test.jsx` (38 passed)
- `npx playwright test e2e/repository_release_memory_walkthrough.spec.js
  --project=chromium` (2 passed)
