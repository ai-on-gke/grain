# 2026-11-03 Repository, Merge Queue, and Release UI Walkthrough

Task 2700. This picks up where the partial 2026-11-02 run (task 2402)
stopped and walks the whole area again: the Repos list, the repo page's
seven tabs, Settings → Repositories, the merge queue and base-branch
gates, compare, and releases. It found one bug, fixed here, and five
observations that need no fix in this task.

## Environment

- **Commit tested**: `0fdeff3b` (`main`, merge of #540). The fix was
  checked on this branch's build of the same tree.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /home/debian/bin/grain ./cmd/grain` (the frontend is
  embedded).
- **Servers**, each signed in through the visible access-code form
  (**Access code** → **Continue**):
  - **A** on `:8433`:
    `grain demo -addr 127.0.0.1:8433 -open=false -access-code-file … -github-sim -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification -reconcile=500ms`
  - **B** on `:8434`, for the base-branch gates:
    `grain demo -addr 127.0.0.1:8434 -open=false -access-code-file … -github-sim -reconcile=500ms`
  - **C** on `:8435`, this branch's build, for checking the fix:
    `grain demo -addr 127.0.0.1:8435 -open=false -access-code-file … -github-sim -reconcile=500ms`
- **Driver**: ad hoc Node scripts on Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), kept outside the
  repository. Every script records `console` errors and warnings,
  `pageerror`, failed requests, responses `>= 400`, every non-GET request
  with its body, and every `dialog`. The access code is masked.
  Viewports were 1280×900 and 390×844/850, in light and dark.
- **Gemini**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so nothing used it.

## Stubs

None. GitHub is `grain demo`'s own `-github-sim`, reached through the
real GitHub client. The only GitHub-side setup went through the
simulator itself:

- Its loopback hook opened issue #8001, labelled `grain-2700`
  (`POST /api/e2e/github-sim/repos/acme/widgets/issues`). The simulator
  also seeds issue #1 on acme/widgets and issue #7 on
  `acme-upstream/widgets`.
- Three commits were pushed to the bare `acme/widgets.git`:
  - On `main`: `.grain/templates/release-cut.yml`, with a `push:` list
    that included a bad ref at first; `.grain/triggers/walk-docs.yml`;
    and `.grain/settings.yml` with `port-branches: [main, candidate]` and
    a `port-types` entry naming a pair that is not configured.
  - A follow-up on `main` that dropped the bad ref.
  - Branch `feature/compare-2700`, adding `WALK.md` and `walk.go`.

Nothing intercepted Grain's API or its React UI.

## Steps and outcomes

### 1. Repos list (A)

- **Opening it**: the rail's **Repos** button opens `/repos`, and so
  does the direct URL.
- **Adding repos**: `acme/gadgets` and an 84-character
  `acme/an-extremely-long-repository-name-…` were added with **Repo to
  add** / **Add repo** (`POST /api/repos`). After a reload the rail
  reads `repos 3`.
- **Search**: `GADG` leaves only gadgets, and `zzz-no-match` shows "No
  repos match your search."
- **Sort**: Custom order, Name (A–Z), Active first and Most tasks each
  order the rows as named.
- **Reorder by keyboard**: Enter on **Move acme/gadgets** offers two
  targets, "Move here, above …" and "Move here, to the end of the
  list". Escape clears them. Picking one moves the row, and focus
  returns to the handle.
- **Reorder by pointer**: `dragTo` moves the last row to the top. The
  order survives a reload.
- **Long name and busy row**: at 1280 and at 390 the document is as
  wide as the viewport.
  - The long name is ellipsised: 860 px wide at 1280, 144 px at 390.
  - On acme/widgets' row **Blocked 1** is the first chip and stays
    visible at 390. Proposed … Closed collapse.
  - **Add repo** stays on screen at 390 (x 303–374).

### 2. Repo row, task count, Back (A)

- The row opens `/repos/acme/widgets`. **← Repos** returns to the list,
  and browser Back returns to the repo page.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows. Back returns to the repo page.

### 3. Filing a task from the repo page (A)

- **New task** on `/repos/acme/gadgets` opens `/new` with Repo set to
  `acme/gadgets`. The grant check is judged for that repo
  (`subject: {repo: "acme/gadgets"}`).
- The title, description and repo survive a reload of `/new`.
- **Create Task** sends `"repo":"acme/gadgets"`, and the repo's count
  goes from "0 tasks" to "1 task filed here".

### 4. Tabs, Settings → Repositories, edits (A)

- **URLs**: every tab rides in `/repos/acme/widgets/settings/<tab>`
  (Overview is the bare path), and each survives a reload with the same
  tab selected.
- **History**: tabs replace the history entry. Back from Releases,
  after Branches, goes to `/repos`, and Forward returns to
  `settings/releases`.
- **Odd URLs**: `settings/bogus` falls back to Overview and rewrites the
  URL. `/repos/no/such-repo` draws a repo page with defaults.
- **Keyboard**: ArrowLeft on Overview wraps to Advanced, Home goes to
  Overview, and End then Enter opens Advanced.
- **Phone (390)**: the selected tab is scrolled into view for advanced,
  releases and agents (x≈243, width≈92), with no horizontal overflow.
- **Settings → Repositories**: the rows show base branch, declarations,
  CI mode and git connection:
  - acme/widgets: `main`, `Read`, `Grain CI`, `Not listed`.
  - The other two: `Default branch`, `Not read`, `GitHub CI`,
    `Not listed`.
  - The cells link to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` and `/settings/git`.
- **Overview** shows:
  - The summary chips: CI gate, Up to date before merge, Merge when
    green, Issue import, Base repair bypass, Branches.
  - The merge re-check card (§5).
  - Recent CI runs: "No CI runs for acme/widgets yet …".
- **Agents** (each read back after a reload):
  - **Default capabilities**: granted `grain server › health`
    (`PUT …/capabilities`). The summary reads "A task filed against
    acme/widgets starts with: Self debug, GCP key, Gemini key, grain ›
    health".
  - **Prompt extension**: "Run make lint before pushing (task 2700)."
    It saves on blur. The read-only deployment layer reads
    "Deployment-wide, set in Settings → Agents: nothing".
  - **Setup command**: `make deps-2700b`.
  - **GitHub issue import**:
    - Enable and **Queue imported tasks at once** each save on click.
      The trust warning ("Turn it on only if you trust everybody who can
      open an issue …") sits beside the switch. It is a warning, not a
      confirmation.
    - Label `grain-2700` and every 15 minutes saved
      (`issueImportIntervalSeconds: 900`).
    - `-3` minutes is stopped by the browser (`min=1`: "Value must be
      greater than or equal to 1.").
  - **Import issues now**:
    - With label `grain-2700`: "Read 1 open issue on acme/widgets:
      imported 1, 0 already imported."
    - Run again: "imported 0, 1 already imported."
    - With source `acme-upstream/widgets`: "Read 1 open issue on
      acme-upstream/widgets: imported 1".
    - With a missing source: "Read 0 open issues on
      acme/nonexistent-2700 … Some could not be imported: … GitHub
      answered 404: Not Found".
    - The imported tasks appear in the repo's task list as `[#8001] …`
      and `[acme-upstream/widgets#7] …`.
- **CI**:
  - **Workflows**: `lint2700.yml` with `name: [unclosed` was refused
    with 400. The alert reads "yaml: line 1: did not find expected ','
    or ']'", and the editor keeps the text.
  - **Leaving with that unsaved edit**: **← Repos** and the rail's
    **Tasks** each asked "You have unsaved changes here. Leave and
    discard them?". Dismissing it kept the page. Switching to another
    tab does not ask, because tabs stay mounted and the draft was still
    there on returning to CI.
  - A valid workflow then saved. Unlike 11-02, the refusal alert
    cleared at once. After a reload the file reads
    `.grain/workflows/lint2700.yml`.
  - **CI grants**: `capabilities.demo.grain.example › cloud-viewer`
    saved (`PUT …/ci-capabilities`) and survived a reload. The picker
    lists methods with the policy's verdicts judged for this repo
    (`runKind="ci"`, `policyRepo`).
- **Branches**:
  - **Add branch** `feature/walk-2700` shows "-- pending".
  - `bad name..lock` is refused with the full branch-name rule.
- **Advanced**:
  - The bad push ref showed "Last read failed: … push:
    "refs/heads/../escape" is not a ref name grain accepts …", along
    with the trigger's missing template, "— still running what
    5931e9500217 declared".
  - After the fix it reads "1 template(s) … 1 trigger(s)", with:
    - "Path triggers: .grain/triggers/walk-docs.yml".
    - "Tasks from .grain/templates/release-cut.yml may also push:
      refs/heads/candidate (force), refs/heads/main, refs/heads/scratch-*
      (delete), refs/tags/v*".
    - "Warning: port-types … bogus->nowhere … has no effect".
    - The read-only **Ports** table: main->candidate fix, revert;
      candidate->main every merge; bogus->nowhere nothing.
  - **Remove** asks "Remove acme/widgets from target repos? …".
    Dismissed.

### 5. Merge queue and base-branch gates (A and B)

- **CI mode on B**:
  - Grain CI with **Watch default branch** ticked, then **Use GitHub
    CI**: Overview's CI gate reads "GitHub CI — base branch not judged"
    with **Go to CI Mode**.
  - **Use No CI** reads "No CI — base branch not judged".
  - The Releases page shows "Nothing is testing acme/widgets's default
    branch, and the merge queue is merging onto it anyway …" with **Go
    to CI Mode**.
  - Both links land on `settings/ci` with CI Mode in view (y 353 and
    503 of 900).
- **Clearing**: switching back to Grain CI cleared the Overview warning
  and the Releases page warning, with no reload (in-app navigation
  only).
- **Merge queue settings (A)**, each read back after a reload:
  - **Require branches up to date** and **Merge when green and clean**
    turn on without a prompt.
  - **Base-branch repair PRs may merge without a human** asks "Let
    base-branch repair PRs in acme/widgets merge without a human …?"
    when turned on. Dismissing changes nothing. Accepting saves it.
    Turning it off does not ask. Settings → Repositories' switch for
    acme/widgets behaves the same way.
  - The Overview summary then reads Required / On.
- **Unfixable checks**:
  - `cla/walk-2700` saved.
  - `grain CI` is refused ("grain CI judges one exact commit …").
  - `CLA/WALK-2700` is refused as a duplicate ("check names are matched
    without case").
- **Overlap re-check panel**: on Overview and on `/tasks/5` it shows "1
  change on main", the **Testing** chip (titled "One grain CI run is
  judging the base branch …"), elapsed time against max age ("20m of
  90m", ticking), the ref and commit, a task link to `/tasks/5` and a
  PR link to #101.
  - **Show the run** never appears, because the demo's seeded batch
    has no run id (`pkg/demo/demo.go`). See observation 2.

### 6. Compare (A)

- **Picker**: the Head branch box lists `feature/walk-2700, 1.0.latest,
  candidate, feature/compare-2700, main, release/1.0`. Typing
  `feature/comp` narrows it to `feature/compare-2700`.
- **Compare** opens `/repos/acme/widgets/compare/main...feature/compare-2700`:
  - "Into main on acme/widgets -- forked at 3642fd55".
  - Side by side / inline / raw views.
  - `WALK.md` +1 and `walk.go` +3.
- **Reload and Back**: a reload redraws the same page, and Back returns
  to `settings/branches`.
- **Other refs**:
  - An unknown ref answers 404 and reads "GitHub has nothing to compare
    for main...no-such-ref-2700 in acme/widgets: one of the two branches
    does not exist …".
  - `release/1.0...main` parses the slashed base.
  - `main...main` reads "There are no changes between these branches."

### 7. Releases and work ownership (A)

- **Work ownership**:
  - **Standing work on this deployment** off: `PUT …/work-ownership
    {"default":"off"}`, with no confirmation. Every item then reads
    "not run".
  - Per-item **Release: on** and **Triage …: off** saved. After a
    reload the switch was still off and the `aria-pressed` buttons
    still matched.
  - Restored to say nothing and on.
- **Release names** (`/repos/acme/widgets/releases`):
  - `bad name` and `-lead` are refused with the release-name rule, and
    `1.0` with "this repo already has an unmerged release with this
    name".
  - `walk2700` created `/repos/acme/widgets/releases/walk2700`, which
    survives a reload.
  - **Bug 1** below.
- **Release picker**: picking 1.0 moves to `/releases/1.0`. Back and
  Forward move between the two releases.
  - `/releases/nosuch-2700` shows the first release without saying the
    name was unknown. See observation 4.
- **Seeded 1.0 (`-qualification`)**:
  - **Cut new RC** is greyed with "Cut new RC: 1.0.rc.1 is active: a new
    RC is cut once it is promoted."
  - **Approve all** approved the qualification (`POST
    …/qualification/approve`).
  - **Promote current RC** asks "Promote 1.0.rc.1 to 1.0? …".
    Dismissing does nothing. Accepting left it "promoting (candidate
    branch "1.0.rc.1" does not exist)" with both buttons greyed and
    their reasons shown. See observation 3.
- **walk2700**: promote → "walk2700.rc.1 -- promoted", and Promote is
  greyed with "is already promoted: a candidate is promoted once". **Cut
  new RC** → "walk2700.rc.2 -- active", with rc.1 in history. This
  survives a reload. The task link **Qualify** goes to `/tasks/23`.
- **Delete release** asks "Remove release …? This drops the release
  and its candidates from grain." Dismissing does nothing. Accepting
  deletes it, and the picker drops it.
- **Merge … into the default branch** asks "… This opens a pull request
  on GitHub from walk2700." Dismissed.

### Console and network

Across all runs:

- **Expected**: the unauthenticated `GET /` 401 before sign-in, and the
  400s and 404 that the steps above provoke on purpose.
- **Daemon log**: one 500 on A, "listing declarations: context
  canceled", logged when a page navigated away mid-request.
- No page errors and no failed requests.

## Bugs

1. **A refused release name outlived the release that fixed it**
   (fixed).
   - **Reproduction**: on `/repos/acme/widgets/releases`, Create release
     `bad name`. Then Create release `walk2700`.
   - **What happened**: the release is created and selected, but "invalid
     release name: …" stays on screen as the app-wide toast for up to 5
     more seconds. The same happened when a 70-character name was
     created right after a refused one.
   - **Fix**: `RepoReleases.jsx` shows a 400 from Create release on the
     name box itself (`error`, helper text, `aria-invalid`). The next
     edit or a create that succeeds clears it. Other failures still go
     to the toast.
   - **Regression coverage**: a unit test in `RepoReleases.test.jsx`.
     It fails on `main`.
   - **Check in the browser (C)**: at 1280 and 390, light and dark, the
     refusal is inline with no toast, the helper sits below the box and
     inside the viewport, there is no horizontal overflow, and editing
     or creating clears it.

## Observations (not fixed here)

1. **Compare picker**: the Head branch list offers `feature/walk-2700`,
   which is still "pending" creation, and not the `walk2700.latest` /
   `walk2700.rc.N` branches that the release created in the simulated
   repo (`git ls-remote` shows them, `GET …/github-branches` does not).
   This looks like a simulator listing gap rather than UI.
2. **Re-check panel**: the demo seeds the re-check with no `RunID`, so
   **Show the run** cannot be exercised in `grain demo`. It is covered
   by `MergeBatchPanel` unit tests.
3. **`-qualification` seed**: candidate `1.0.rc.1` has no branch in the
   simulator, so promoting it sticks at "promoting (candidate branch …
   does not exist)". Releases created in the walkthrough promote and
   cut cleanly.
4. **Unknown release URL**: `/repos/acme/widgets/releases/<unknown>`
   silently shows the first release.
5. **Repeated warning**: the Advanced tab shows the port-types warning
   twice, once under Declarations and once under Ports.
