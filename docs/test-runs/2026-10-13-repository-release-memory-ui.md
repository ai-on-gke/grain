# Repository, release and memory UI walkthrough — 2026-10-13

Task: grain/eu/task-3495. This was an operator's walk through the
repository-centric screens in a real browser against a real `grain demo`
server:

- the Repos list
- a repo's own page: branch rules, compare, CI and default capabilities,
  prompt extension, setup command, declarations, merge queue, CI mode,
  checks that cannot pass, work ownership, release workflows
- filing a task from a repo with read and write repositories
- deployment and repo memory, and memory documents
- releases and their release-candidate history

It built on the 2026-10-11 and 2026-10-12 walks of the same area and went
past both. It deliberately covered the places where two panes on one page
draw the same setting, and the release lifecycle driven through the
reconciler's states.

## What was tested

- **Commit tested**: `25cd6f2640ea` (`candidate`, "Merge pull request
  #3656 from bwsalmon/grain/eu/task-3532"). The UI reported
  `v1.10.0-rc.1.0.20261013090548-25cd6f2640ea`. Both fixes below were
  then re-verified on this branch against a rebuilt binary.
- **OS**: Debian GNU/Linux 12 (bookworm), amd64 sandbox, 4 vCPU, 7 GiB.
- **Toolchain**: Go 1.26.6, Node.js 20.19.0. `go build ./cmd/grain`
  embedded the Vite production build of `ui/` (`npm ci && npm run build`).
- **Browser**: Playwright 1.62.1, Chromium headless shell 151.0.7922.34
  (`chromium-1234`). Three viewports: 1400×950 desktop, 390×844 narrow,
  and a `hasTouch`/`isMobile` 390×844 context for tap reordering.
- **Servers**: three throwaway `grain demo` processes. Each ran over its
  own embedded SQLite store under a scratch `TMPDIR`/`HOME`:
  - `grain demo -addr 127.0.0.1:8787 -open=false`: the unfixed build.
    Used for the main walk, memory, repos list and stubs.
  - `grain demo -addr 127.0.0.1:8788 -open=false -fake-ci -github-sim`:
    the unfixed build with simulated GitHub (branches `main`,
    `release/1.0`). Used for branches, the release-switch reproduction and
    releases.
  - `grain demo -addr 127.0.0.1:8789 -open=false`: the fixed build. Used
    to re-run each reproduction.
- **Session**: loopback-trusted operator `alice` (`grain.username` in
  localStorage, as `e2e/pageerrors.js` sets it), with the
  `X-Grain-Admin-Mode: on` header the Playwright suite sends.
- **Model / Gemini**: the sandbox carried no
  `/home/debian/.gemini-api-key`, so no model was involved.
  - Every step was a scripted Playwright gesture on a visible control:
    click, fill, Tab, Enter, Space, Escape, drag, tap, browser
    Back/Forward and reload.
  - On every page the scripts collected page errors, console errors and
    warnings, failed requests, and 4xx/5xx responses.

## Stubs

Grain's own API, store and React UI were never replaced. Only these
boundaries, which the demo does not wire to anything real, were stubbed
or simulated:

| Boundary | How | Why | Responses exercised |
| --- | --- | --- | --- |
| GitHub compare (`GET /api/repos/acme/widgets/compare`) | Playwright `page.route` on that one URL | No demo wires a comparer: unstubbed, it answers 404 "repository comparison is not available on this UI server", and that text renders where the diff would be | one-file diff (merge base `abc1234d`), empty diff, merged, 502 with GitHub's error text, a held request (loading) |
| GitHub branch listing (`GET /api/repos/acme/widgets/github-branches`) on the plain demo | Playwright `page.route` | The plain demo has no GitHub client (404). `-github-sim` answered `["main","release/1.0"]` for real and was used unstubbed for the branch rules | `["main","candidate","release/4.0"]`, `[]`, 502, a held request |
| GitHub release reconciler | `ui/e2e/releasehelper` (`-release 2.1 mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-promoted`, `mark-release-merged`) against the `-github-sim` demo's store | The demo runs no reconciler, so nothing moves a release off Provisioning or a candidate off cutting | provisioning → active; rc.1 cutting → active → promoting → promoted; rc.2 cut; merge requested → merged with PR link |

## Steps and outcomes

### Repos list

1. Opened **Repos** from the rail (`Repos 2` → `/t/default/repos`) and
   from `/repos` directly. The list showed `acme/widgets` (state chips,
   "11 tasks") and `grain/config-default` ("0 tasks"). There were no
   console errors.
2. Added three repos: `acme/gadgets`, `zeta/alpha-tools` and
   `example-corporation/an-exceptionally-long-repository-service-name-for-layout-testing-purposes-xyz`.
   The count followed each add. `bad repo` was refused with "repo must be
   owner/name, got "bad repo" (one on another git host is
   host/owner/name)".
3. Search worked for `GADGETS` (case-insensitive), `alpha` (substring) and
   `zzz` ("No repos match your search.").
4. Reorder, in the custom order:
   - Keyboard: focus "Move zeta/alpha-tools" and press Enter. The slots are
     labelled "Move here, above …". Enter on "Move here, above
     acme/widgets" moved the row, and focus returned to "Move
     zeta/alpha-tools". Space also picks up a row, and Escape cancels with
     focus kept on the handle.
   - Pointer: dragged the long-named row to the top.
   - Touch: in the touch context, tapped "Move grain/config-default", then
     "Move here, above acme/gadgets".
   - Each order survived reload. The order is per browser (localStorage,
     `listOrder.js`), so a fresh browser context starts alphabetical. That
     is by design.
5. Sort offers custom order, name (a–z), active first and most tasks.
   - Move handles show only in custom order.
   - The sort choice does **not** survive a reload. It is plain
     `useState("custom")` in RepoList, as it is in the Templates, Schedules
     and Suites lists, so this is consistent and was left alone.
   - The 2026-10-12 report's "the choice survives reload" was wrong on
     this point. The *custom order* survives reload; the sort mode does
     not.
6. Layout:
   - At 1400 px the long name fits in full, with `text-overflow: ellipsis`
     and the full name in `title`.
   - At 390 px neither the list nor the releases pane scrolls sideways
     (`scrollWidth` 390).

### Repo page, task count, filing

7. Clicked the `acme/widgets` row, which opened
   `/t/default/repos/acme/widgets`. Back returned to the list and Forward
   returned to the page.
8. "11 tasks filed here" matched the row's "11 tasks".
   - The link opened `/t/default/tasks?f=repo:any:acme/widgets` with 11
     rows, and Back returned to the repo page.
   - The link's own `href` is `/?f=repo:any:acme/widgets`. Opened cold, as
     a new tab would, it lands on the same filtered list.
9. Filed a task with **New task** from the repo page.
   - The Repos field was preselected to `acme/widgets`, and the drawer
     read "In team admin".
   - Filled title, description, base `release/1.0` and Estimate 2.5. Added
     a write repo (`grain/config` + Enter took the highlighted
     `grain/config-default`) and a read-only repo `zeta/readonly`.
   - The task was created as 13 with `repo: acme/widgets`,
     `base: release/1.0`, `writes: [{repo: grain/config-default}]`,
     `reads: [zeta/readonly]`, `customFields.estimate: "2.5"`, and
     capabilities `gcp-key, gemini-key, self-debug`.
   - The drawer closed back onto the repo page. `/tasks/13` showed both
     repos, the base and the estimate, before and after reload.
10. Stubbed GitHub branch listing, New task base-branch field:
    - With `["main","candidate","release/4.0"]`, `release/4.0` was quiet
      and `nope/branch` warned "No branch "nope/branch" on acme/widgets:
      push it first, or have the task depend on the one that cuts it".
    - With `[]`, both warned.
    - With a 502, and while the request was held, the field said nothing.
    - Create stayed enabled in every case. This is the designed behaviour:
      the field warns but never refuses (`base-branch-unreadable.spec.js`).

### Branch rules, compare and repo configuration

11. Branches (`-github-sim`):
    - Added `walk/1013`, which was listed "-- pending".
    - Re-adding it added nothing.
    - `release/1.0` (already on the simulated GitHub) was adopted as
      pending.
    - `bad..name`, `-leading` and a whitespace-only name were each refused
      with "invalid branch name", and the typed text was kept.
12. Compare, with Head `walk/1013`, opened
    `/repos/acme/widgets/compare/main...walk/1013`. With compare stubbed:
    - a diff rendered (side by side, inline and raw; README.md +1 −0);
    - an empty diff showed "There are no changes between these branches.";
    - merged showed "Merged into main on acme/widgets";
    - a 502 showed GitHub's text in place;
    - a held request showed "Loading the comparison…" until it was
      answered.

    Back returned to the repo page. A cold `…/compare/main...` (no head)
    fell back to the repo page. Unstubbed, the demo's 404 text rendered in
    place.
13. Edited each of these on `/repos/acme/widgets` (team context
    "admin"):
    - CI capabilities: + GCP key
    - Default capabilities for admin: + GitHub sandbox. The "starts with"
      line updated to "Self debug, GCP key, Gemini key, GitHub sandbox".
    - The team's prompt extension and setup command, both saved on blur
    - "Require branches to be up to date": on
    - CI Mode: Grain CI
    - Check that cannot pass: `cla/google`
    - Jobs dispatched after every merge:
      `.grain/workflows/image.yml:guest-cache`, saved with its own button
    - Work ownership "release": off
    - Read acme/widgets's own declarations: on

    After reload every value read back. So did
    `GET /api/teams/default/repos/acme/widgets/config` (`effective`:
    `defaultCapabilities: [github-sandbox]`, `ciCapabilities: [gcp-key]`,
    `ciMode: grain`, `requireBranchUpToDate: true`) and
    `GET /api/repos/acme/widgets/release` (`postMergeDispatchJobs`).
14. Ticked "Edit deployment-wide defaults". The team-context alert went
    away, and the fields became the global "Prompt extension for
    acme/widgets" and "Setup command for acme/widgets".
    - Set a global prompt extension, which read back from
      `GET /api/repos/acme/widgets/prompt-extension`.
    - The checkbox itself survived reload.
    - "Add workflow" added an empty row with "Remove workflow 1".
15. Destructive confirmations:
    - **Remove** (repo) asked "Remove acme/widgets from target repos? …
      Tasks already filed there are not affected." Dismissing it kept the
      page and the repo.
16. **Bug 1, below**: Work ownership "release" and Release workflows'
    "Automatic releases" disagreed after a save until reload.

### Releases

17. **Releases** opened `/t/default/repos/acme/widgets/releases`, which
    said "acme/widgets has no releases yet". The Qualification plan
    controls were reachable.
18. Release names:
    - `bad name` and `../x` were refused with "invalid release name: use
      letters, digits, ".", "_" and "-", …", and the name was kept in the
      box.
    - An 80-character `aaaa…` and `2.1` were created. Each opened at its
      own URL (`…/releases/2.1`) and showed "2.1 -- Provisioning", "latest:
      2.1.latest, prod: 2.1", and rc.1 "-- cutting".
    - Cut, promote and merge stayed disabled while provisioning.
19. Lifecycle, driven by the release helper with a deep link at each
    step:
    - Provisioned and rc.1 cut showed "2.1 -- Active", with Promote
      enabled.
    - Cut new RC stayed disabled while rc.1 was unpromoted. That is the
      designed `canCut`, but the button gives no reason.
    - **Promote current RC** asked "Promote 2.1.rc.1 to 2.1? This moves
      acme/widgets's 2.1 branch to 2.1.rc.1." Dismissing it changed
      nothing. Accepting it disabled Promote.
    - Marked promoted, then reloaded: Cut new RC was enabled. It cut
      `2.1.rc.2` **without a confirmation**, and History listed rc.2 and
      rc.1.
    - **Merge 2.1 into the default branch** asked "… This opens a pull
      request on GitHub from 2.1." Dismissing it changed nothing.
      Accepting it moved the release to "Merge requested".
    - Marked merged: "2.1 -- Merged", with a "pull request" link to
      `https://github.com/acme/widgets/pull/4242`.
20. Release picker: it lists "2.1 -- merged" and "aaaa… -- active".
    - A deep link to the long-named release selected it.
    - A deep link to a release that does not exist (`…/releases/nope`)
      quietly rewrote the address to `…/releases` and showed the newest
      release, with no "no such release" note. This was noted and not
      changed.
21. **Delete release** on the long-named release asked "Remove release
    aaaa…? This drops the release and its candidates from grain."
    - Dismissing it kept the release.
    - Accepting it removed the release (confirmed through
      `GET …/releases`), and the pane moved to 2.1.
    - Chromium logged the DELETE as `net::ERR_ABORTED` even though it
      succeeded. The same was seen on 10-12, and the effect is correct.
22. Keyboard: Tab reaches ← acme/widgets, the new-release name, Create
    release, the release picker, the PR link, Delete release and every
    Qualification plan control. At 390 px nothing scrolls sideways.

### Memory

23. Team settings → Models (`/t/default/settings/models`) shows:
    - "This deployment" memory, written by the memory agent, with document
      `release-checklist`;
    - "acme/widgets" memory, edited here, with document `flaky-e2e`;
    - "Another repo".
24. **Open flaky-e2e (acme/widgets)** fetched and showed the summary and
    the full document (208 of 65536 characters). The button became
    "Close".
25. Made reversible corrections:
    - Appended ` CORRECTED-1013` to the deployment memory. Save became
      enabled.
    - Pressed Escape with the edit unsaved. The overlay asked "You have
      unsaved changes here. Leave and discard them?". Dismissing it kept
      the pane, and then Save was clicked.
    - Appended "Corrected 2026-10-13." to `release-checklist` and clicked
      Save document.
26. Reloaded. The deployment memory still ended with the marker, and both
    notes now read "written today, edited here". Reopening
    `release-checklist` read the correction back, and so did
    `GET /api/memory/documents/release-checklist` (`updatedBy: alice`,
    `updatedByKind: human`, length 355).
27. Restored both to their original text, then reloaded and reopened.
    Both were identical to before.
28. **Bug 2, below**: the panel's introduction pointed at the wrong page
    for standing instructions.

### Deep links and error states

29. These deep links were checked:
    - `/repos/nope/nothing` (a repo nothing knows) opens a repo page with
      "0 tasks filed here", without saying the repo is not configured.
      This is the existing behaviour and was noted and not changed.
    - `/t/default/repos/grain/config-default` opens that repo's page.
30. Tab order on the repo page reaches every control in `main`, from "←
    Repos" to "save dispatched jobs", including all twelve Work ownership
    buttons and the merge-batch task and PR links.

Console: the only errors were the intentional ones:

- the 400s for invalid repo and release names;
- the demo's 404s for compare and for GitHub branch listing;
- the stubbed 502s.

The only `ERR_ABORTED` entries were `/api/tasks` polls cancelled by a
navigation, plus the release DELETE in step 21.

## Bugs found

### 1. The repo page's two release switches disagreed until a reload (fixed)

- **Reproduction**: `grain demo -fake-ci -github-sim`.
  1. Open `/repos/acme/widgets`, scroll to Work ownership, and click
     "release: off". The Release workflows pane directly below still says
     "Automatic releases on", with "Turn off automatic releases".
  2. Click "release: say nothing", then "Turn off automatic releases".
     Work ownership still says "say nothing", though the server now has
     the release override `off`.
  3. Reload. Both now say off.

  The two are one setting: `GET /api/repos/acme/widgets/release` reports
  `enabled` as `model.RunsWorkItem(cfg, model.ReleaseItem)`, and its PUT
  writes the release item's override. Setting the repository default to
  off had the same stale effect, since a release that says nothing
  follows it.
- **Fix**: RepoPage counts the saves of each pane and passes them to the
  other as a `refreshKey`:
  - RepoWorkOwnership gains `refreshKey` and `onSaved`.
  - RepoReleaseWorkflowSettings gains `onToggled`, and its existing
    `refreshKey` now also moves on an ownership save.
- **Coverage**: `RepoPage.test.jsx`, "keeps the release's ownership row
  and its automatic-release switch in step". It fails without the fix.
  The e2e test
  `repository-release-memory-1013-walkthrough.spec.js`, "the repo page's
  two release switches follow each other without a reload", drives the
  steps above against the real demo, including the repository default.
  The fixed server (8789) passed each step without a reload.

### 2. The memory panel sent the reader to the tab they were already on (fixed)

- **Reproduction**: open `/t/default/settings/models`.
  - The note above the memory panel says "Max agent turns, standing
    instructions … a system admin sets them on System → Agents & Task
    Branches".
  - The panel's introduction, directly below it, then says memory "is
    kept apart from the standing instructions on the Models tab". That is
    this tab, and it has none.
- **Fix**: the sentence names System → Agents & Task Branches.
- **Coverage**: `MemoryPanel.test.jsx`, "points at where standing
  instructions are actually edited", and the second test in
  `repository-release-memory-1013-walkthrough.spec.js`.

## Noted, not changed

These are judgement calls rather than faults, so each is left for whoever
owns the area:

- **Cut new RC**:
  - It is disabled without saying why while the current candidate is
    unpromoted.
  - It is the one release action that runs without a confirmation. It is
    additive (it creates a branch), unlike promote, merge and delete.
- **Unknown release in the URL**: a deep link to a release that does not
  exist quietly becomes the bare releases URL.
- **Repos list sort**: the sort choice is not remembered across reloads
  on any of the four ordered lists.

## Tests run

- `npx vitest run src/components/RepoPage.test.jsx
  src/components/RepoWorkOwnership.test.jsx
  src/components/RepoReleaseWorkflowSettings.test.jsx`: 106 passed. The
  new RepoPage test fails with the three components reverted.
- `npx vitest run src/components/MemoryPanel.test.jsx`: 26 passed.
- `npx playwright test e2e/repository-release-memory-1013-walkthrough.spec.js
  e2e/repo-pane.spec.js e2e/memory.spec.js --project=chromium`: 7 passed.
- Whole suites: see the branch's grain CI.
- No screenshots or browser artifacts are committed. The exploration
  scripts lived in an uncommitted scratch directory (`/tmp/explore`).

## Sandbox notes

- `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.
- `ui/e2e/releasehelper` takes `-dir <store dir>` (the
  `grain-demo-*` directory under the demo's `TMPDIR`) and
  `-release <name>`. Without `-release` it acts on the *oldest* release,
  which is easy to miss when several exist.
