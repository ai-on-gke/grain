# 2026-11-04 Repository, Merge Queue, and Release UI Walkthrough

Task 2961. A full pass over the Repos list, the repo page's seven tabs,
Settings → Repositories, the merge queue and base-branch gates, compare,
and releases, against yesterday's report
([2026-11-03](2026-11-03-repository-release-memory-ui.md)). It found one
bug, fixed here. Two of yesterday's observations no longer hold.

## Environment

- **Commit tested**: `249f92a0` (`main`, merge of #696). The fix was
  checked on this branch's build of the same tree.
- **`main` does not compile at this commit.** `pkg/cli/main.go` has
  `case "git-login", "login":` (from #512) beside the existing sign-in
  `case "context", "whoami", "login", "logout":`, and Go refuses the
  duplicate case. To get a binary, both builds used a local-only edit
  dropping `"login"` from the `git-login` case. The edit was reverted
  before every commit. It is not part of this branch. This is base-branch
  breakage for grain's base-green repair, not this task.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o … ./cmd/grain` (the frontend is embedded).
- **Servers**, each signed in through the visible access-code form:
  - **A** on `:8433`: `grain demo -addr 127.0.0.1:8433 -open=false
    -access-code-file … -github-sim -reconcile=500ms -fake-ci
    -scheduled-workflows -base-branch-ci -secrets-store -merge-rules
    -merge-queue-states -qualification`
  - **B** on `:8434`, for the base-branch gates, and as the unfixed
    build: `grain demo … -github-sim -reconcile=500ms`
  - **C** on `:8435`, this branch's build, for checking the fix: the same
    flags as B.
- **Driver**: ad hoc Node scripts on Playwright's Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), kept outside the
  repository. Every script records:
  - `console` errors and warnings, and `pageerror`;
  - failed requests and responses `>= 400`;
  - every non-GET API request with its body;
  - every `dialog`, which is dismissed or accepted on purpose.
- **Viewports**: 1280×900 and 390×844/850, in light and dark.
- **Gemini**: `/home/debian/.gemini-api-key` is not present, so nothing
  used it. The sandbox's file reader shows PNGs as bytes, so layout was
  checked with DOM geometry (bounding boxes, `scrollWidth` against
  `clientWidth`) rather than by eye.

## Stubs

**GitHub** is `grain demo`'s own `-github-sim`, reached through the real
GitHub client. All GitHub-side setup went through the simulator:

- **Issue**: its loopback hook opened issue #8001 on acme/widgets,
  labelled `grain-2961` (`POST /api/e2e/github-sim/repos/acme/widgets/issues`).
- **Commits pushed to the bare `acme/widgets.git` on `main`**:
  - `.grain/templates/release-cut.yml`, whose `push:` list first carried
    an unknown `description:` key and the bad ref `refs/heads/../escape`;
    two follow-up commits fixed each.
  - `.grain/triggers/walk-docs.yml`.
  - `.grain/settings.yml` with `port-branches: [main, candidate]` and a
    `port-types` entry `bogus->nowhere`.
- **Branch** `feature/compare-2961`, adding `WALK.md` and `walk.go`.

**One `page.route`** delayed `POST …/import-issues` by 2 s, to see the
button's in-flight state. The request still reached the real server.

Nothing replaced Grain's API or its React UI.

## Steps and outcomes

### 1. Repos list (A)

- **Opening it**: the rail's **repos** item opens `/repos`, and so does
  the direct URL.
- **Adding repos**: **Repo to add** / **Add repo** added `acme/gadgets`
  and an 84-character `acme/an-extremely-long-repository-name-…`
  (`POST /api/repos`). The rail then reads `repos 3`.
- **Counts**: acme/widgets reads Blocked 1, Proposed 3, Waiting 2,
  Running 2, Asked 4, Merging 3, Deferred 1, 16 tasks.
- **Search**: `GADG` leaves only gadgets, and `zzz-no-match` reads "No
  repos match your search."
- **Sort**: Name, Active first, Most tasks and Custom each order the rows
  as named.
- **Keyboard reorder**: Enter on **Move acme/widgets** offers "Move here,
  above …" targets, and Escape clears them. Picking one moves the row,
  and focus returns to the handle.
- **Pointer reorder**: `dragTo` moves the last row to the top. Both
  orders survive a reload.
- **Long name and busy row**: at 1280 and at 390 the document is exactly
  as wide as the viewport.
  - The long name is ellipsised: 860 px at 1280, 144 px at 390.
  - **Blocked 1** is the first chip on acme/widgets and stays visible at
    390, where every count chip after it collapses.
  - **Add repo** stays on screen at 390 (x 303–374).

### 2. Repo row, task count, Back (A)

- A click on the row opens `/repos/acme/widgets`. **← Repos** returns to
  the list, and browser Back returns to the repo page.
- **16 tasks filed here** opens `/?f=repo:any:acme/widgets` with 16
  rows, and Back returns to the repo page.

### 3. Filing a task from the repo page (A)

- **New task** on `/repos/acme/gadgets` opens `/new` with Repo set to
  `acme/gadgets`. The grant check is judged for that repo
  (`subject: {repo: "acme/gadgets"}`).
- The title, description and repo survive a reload of `/new`.
- **Create Task** sends `"repo":"acme/gadgets"`, and the repo page goes
  from "0 tasks filed here" to "1 task filed here".

### 4. Tabs, Settings → Repositories, edits (A)

- **URLs**: every tab rides in `/repos/acme/widgets/settings/<tab>`
  (Overview is the bare path), and each survives a reload still
  selected.
- **History**: tabs replace the history entry. Back from Releases (after
  Branches) goes to `/repos`, and Forward returns to `settings/releases`.
- **Odd URLs**: `settings/bogus` falls back to Overview and rewrites the
  URL.
- **Keyboard**: ArrowLeft on Overview wraps to Advanced, Home goes to
  Overview, and End then Enter opens Advanced.
- **Phone (390)**: the selected tab is scrolled into view (for example
  advanced at x 242–334, ci at 146–236), with no horizontal overflow.
- **Settings → Repositories**:
  - The rows read: acme/widgets `main`, `Read`, `Grain CI`, `Not listed`;
    the other two `Default branch`, `Not read`, `GitHub CI`, `Not listed`.
  - The cells link to the repo page, `settings/releases`,
    `settings/advanced`, `settings/ci` and `/settings/git`.
- **Overview**:
  - The summary reads CI gate, Up to date before merge, Merge when green,
    Issue import, Base repair bypass and Branches.
  - The merge re-check card is covered in §5.
  - Recent CI runs lists the re-check's running run.
- **Agents**, each value read back after a reload:
  - **Default capabilities**:
    - The picker marks Self debug and Read issues "already a deployment
      default".
    - Priority dispatch was added (`PUT …/capabilities`), and the summary
      then reads "starts with: Self debug, Read issues, Priority
      dispatch".
  - **Default hostd** `no-such-hostd-2961` is refused ("is not in the
    registry (Settings -> Hostds)") and put back to empty.
  - **Upstream repository** (new since 11-03):
    - `not a repo` is refused with the owner/name rule and put back.
    - `acme/widgets`, the repo itself, is stored as empty, so pull
      requests stay on acme/widgets.
    - `acme-upstream/widgets` saves, and the summary reads "open against
      acme-upstream/widgets".
  - **Prompt extension**: "Run make lint before pushing (task 2961)."
    saves on blur. The read-only deployment layer reads "Deployment-wide,
    set in Settings → Agents: nothing".
  - **Setup command**: `make deps-2961`.
  - **GitHub issue import**:
    - **Import issues now** is disabled while import is off.
    - Enable and **Queue imported tasks at once** save on click, with the
      trust warning beside the switch ("Turn it on only if you trust
      everybody who can open an issue on acme/widgets …").
    - Label `grain-2961` and every 15 minutes saved
      (`issueImportIntervalSeconds: 900`).
    - `-3` minutes is stopped by the browser ("Value must be greater than
      or equal to 1.").
  - **Import issues now**:
    - With the label: "Read 1 open issue on acme/widgets: imported 0, 1
      already imported." The importer's own cycle, which enabling the
      switch let run, had already taken #8001.
    - With source `acme-upstream/widgets`: "Read 1 open issue …: imported
      1".
    - With source `acme/nonexistent-2961`: "Read 0 open issues … Some
      could not be imported: … GitHub answered 404: Not Found".
    - While the request is in flight the button reads "Importing…" and is
      disabled.
    - The Overview summary then reads "Issue import: On, approved". The
      repo's task list shows `[#8001] …` and `[acme-upstream/widgets#7] …`.
- **CI**:
  - **Workflows**: `lint2961.yml` with `name: [unclosed` was refused (400,
    "yaml: line 1: did not find expected ',' or ']'"), and the editor kept
    the text.
  - **Unsaved edit**: with that draft unsaved, **← Repos** and the rail's
    **Tasks** each asked "You have unsaved changes here. Leave and discard
    them?". Dismissing kept the page. Switching tabs keeps the draft.
  - A valid workflow then saved and survives a reload as
    `.grain/workflows/lint2961.yml`. The refusal stayed on screen after
    the good save, which is **Bug 1**.
  - **CI capabilities** offers `package publish`. The grants dialog lists
    the grain API presets.
- **Branches**:
  - **Add branch** `feature/walk-2961` shows "-- pending". Adding it again
    reads "already on this repo's list (pending); nothing new was added."
  - `bad name..lock` is refused with the branch-name rule.
- **Advanced**:
  - With the unknown template key, and then the bad push ref, it reads
    "Last read failed: … push: "refs/heads/../escape" is not a ref name
    grain accepts …", plus the trigger's missing template, "— still
    running what 6a2a91c0319b declared".
  - On the next two-minute cycle after the fix it reads "Read at
    0b4e2d58abe7: 1 template(s) … 1 trigger(s)", with:
    - "Path triggers: .grain/triggers/walk-docs.yml".
    - "may also push: refs/heads/candidate (force), refs/heads/main,
      refs/heads/scratch-* (delete), refs/tags/v*".
    - The `bogus->nowhere` warning.
    - The read-only **Ports** table: main->candidate fix, revert;
      candidate->main every merge; bogus->nowhere nothing.
  - **Remove** asks "Remove acme/widgets from target repos? …" (after a
    `POST /api/settings/plan` check). Dismissed.

### 5. Merge queue and base-branch gates (A and B)

- **CI mode (B)**: Grain CI with **Watch default branch** ticked, then
  **Use GitHub CI** and then **Use No CI**:
  - Overview's CI gate reads "GitHub CI — base branch not judged" and
    "No CI — base branch not judged".
  - The alert reads "Nothing is testing acme/widgets's default branch,
    and the merge queue is merging onto it anyway …", on Overview and on
    the Releases tab.
  - **Go to CI Mode** lands on `settings/ci` with CI Mode in view (y 353
    of 850).
- **Clearing (B)**: back to Grain CI, both warnings cleared with no
  reload.
- **Merge queue settings (A)**, each read back after a reload:
  - **Require branches up to date** and **Merge when green and clean**
    toggle with no prompt.
  - **Base-branch repair PRs may merge without a human** asks "Let
    base-branch repair PRs in acme/widgets merge without a human when
    their CI is green? …" when turned on. Dismissing changes nothing,
    accepting saves it, and turning it off does not ask.
  - Settings → Repositories' switch for acme/widgets behaves the same
    way.
- **Unfixable checks**:
  - `cla/walk-2961` saved.
  - `grain CI` is refused inline ("judges one exact commit …").
  - `CLA/WALK-2961` is refused as a duplicate ("check names are matched
    without case").
- **Overlap re-check panel**, on Overview and on `/tasks/5`:
  - "1 change on main" and the **testing** chip, titled "One grain CI run
    is judging the base branch with this change merged into it …".
  - Elapsed time against max age ("12m of 90m", ticking).
  - The ref and commit, a task link to `/tasks/5` and PR #101.
  - **Show the run** now appears and opens the run's overlay
    (`grain-merge-queue/…/recheck/5/main`, running, **Cancel run**), so
    yesterday's observation 2 no longer holds.

### 6. Compare (A)

- **Picker**: the Head branch box suggests `feature/walk-2961, 1.0.latest,
  1.0.rc.1, candidate, feature/compare-2961, main, release/1.0`. Typing
  `feature/comp` narrows it to `feature/compare-2961`.
- **Compare** opens `/repos/acme/widgets/compare/main...feature/compare-2961`:
  "Into main on acme/widgets -- forked at 98d06b2a", Side by side /
  inline / raw, `WALK.md` +1 and `walk.go` +3.
- **Reload and Back**: a reload redraws it, and Back returns to
  `settings/branches`.
- **Other refs**:
  - `main...no-such-ref-2961` answers 404 and reads "GitHub has nothing
    to compare … one of the two branches does not exist …".
  - `release/1.0...main` parses the slashed base.
  - `main...main` reads "There are no changes between these branches."

### 7. Releases and work ownership (A)

- **Work ownership**:
  - **Standing work on this deployment** off (`{"default":"off"}`, no
    confirmation), then per-item **Release: on** and **Triage …: off**.
  - After a reload the switch is still off. The `aria-pressed` buttons
    match, and the statuses read "Release on, set here … runs" and every
    other item "not run".
  - Restored to say nothing and on.
- **Release names** (`/repos/acme/widgets/releases`):
  - `bad name`, `-lead` and `1.0` are refused on the name box itself
    (`aria-invalid="true"`) with no toast, as yesterday's fix intended.
  - `walk2961` created `/repos/acme/widgets/releases/walk2961` and cleared
    the refusal. It survives a reload.
- **Release picker**: picking 1.0 moves to `/releases/1.0`. Back and
  Forward move between the two releases, with the picker following.
- **Unknown release**: `/releases/nosuch-2961` now reads "acme/widgets
  has no release named “nosuch-2961” -- showing walk2961 instead …", so
  yesterday's observation 4 no longer holds.
- **Seeded 1.0 (`-qualification`)**:
  - **Cut new RC** is greyed with "1.0.rc.1 is active: a new RC is cut
    once it is promoted." The **Qualify** task link goes to `/tasks/23`.
  - **Approve all** approved the qualification.
  - **Promote current RC** asks "Promote 1.0.rc.1 to 1.0? …". Dismissing
    does nothing. Accepting shows "promoting", with both buttons greyed
    and their reasons given, and then "promoted".
  - The simulator now has the `1.0.rc.1` branch, so yesterday's
    observation 3 no longer holds.
- **walk2961**:
  - Promote → "walk2961.rc.1 -- promoted", and Promote is greyed with
    "is already promoted: a candidate is promoted once".
  - **Cut new RC** → "walk2961.rc.2 -- active", with rc.1 in history. This
    survives a reload.
- **Merge … into the default branch** asks "… This opens a pull request
  on GitHub from walk2961." Dismissed.
- **Delete release** asks "Remove release walk2961? …". Dismissing does
  nothing. Accepting deletes it, and the picker then offers only 1.0.

### Console and network

- **Expected**: the unauthenticated `GET /` 401 before sign-in, and the
  400s and the 404 the steps above provoke on purpose.
- **Daemon log**: two 500s on A, "context canceled", logged as a page
  navigated away mid-request (as on 11-03).
- No page errors and no failed requests.

## Bugs

1. **A repo setting's refusal outlived the save that fixed it** (fixed).
   - **Reproduction**: on `/repos/acme/widgets/settings/ci`, **Add
     workflow** with file `x.yml` and text `name: [unclosed`, then leave
     the box. Replace the text with a valid workflow and leave the box
     again. The same happens with **Upstream repository** `not a repo`
     followed by `acme-upstream/widgets`.
   - **What happened**: the good save is stored, but the refusal
     ("workflows: ci: .grain/workflows/x.yml: yaml: line 1: …") stays on
     the shell's banner for the rest of its five seconds. It was still
     there 317 ms after the successful `PUT`, under a file that had just
     saved. On B (unfixed) the upstream refusal outlived its fix the
     same way. This is the same shape as 11-03's release-name bug, on
     every box RepoPage saves.
   - **Fix**: App already has `dismissError(message)` (used by Settings),
     which takes the banner down only if it still says that message.
     - App now passes it to RepoPage.
     - RepoPage records, per box, the refusal it put up (`refuseSave`),
       and that box's next good save takes exactly that message down
       (`settleSave`).
     - This covers workflows, default hostd, upstream repository, prompt
       extension, setup command, default and CI capabilities, issue
       import settings, unfixable checks and Add branch.
     - A refusal from another box is left alone.
   - **Regression coverage**:
     - A unit test in `RepoPage.test.jsx`, which fails on `main`.
     - `ui/e2e/repo-setting-refusal-clears.spec.js`, which checks that
       the refusal goes within 1.5 s of the good save and that an
       unrelated hostd refusal stays.
     - `npx vitest run src/components/RepoPage.test.jsx`: 90 passed. The
       new spec and `repo-page-unsaved-edits.spec.js`: 3 passed. Both
       e2e runs used the local compile workaround above.
   - **Check in the browser (C)**: at 1280 and 390, light and dark, the
     refusal shows, clears as soon as its own box saves, and an unrelated
     hostd refusal stays. There is no horizontal overflow.

## Observations (not fixed here)

1. **Compare picker** still offers `feature/walk-2961`, which is only
   "pending" creation, first among the head branches. Its comment says
   grain's own list is deliberate. Picking it gives the 404 message, not
   an error.
2. **Repeated warning**: the Advanced tab still draws the port-types
   warning twice, once under Declarations and once under Ports (as on
   11-03).
3. **Grain CI mode on a `-ci=false` daemon** (B) clears the "Nothing is
   testing …" gate warning, although nothing runs grain CI there. A
   separate alert on Overview ("This deployment does not run grain CI:
   its daemon was started with -ci=false …") does say so. The Releases
   tab says nothing.
