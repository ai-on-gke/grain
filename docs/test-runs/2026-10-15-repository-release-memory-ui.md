# Repository, release and memory UI walkthrough — 2026-10-15

Task: grain/eu/task-3752. This was an operator's walk through the
repository-centric screens, in a real browser against a real `grain demo`
server:

- the Repos list
- a repo's page and each of its seven tabs
- filing a task from a repo
- the team Memory page and its documents
- releases and their release-candidate trains

It built on the 2026-10-13 walk of the same area
(`2026-10-13-repository-release-memory-ui.md`). It re-checked what that
walk covered and went further on four things:

- keyboard focus around the memory documents
- the repo page's hash-addressed tabs
- long release names at phone width
- team repo defaults reaching the new-task form

## What was tested

- **Commit tested**: `9788ecb1e3aa` (`candidate`, "Merge pull request
  #3832 from bwsalmon/grain/eu/task-3744"). The UI reported
  `v1.12.0-rc.1.0.20261014232547-9788ecb1e3aa`. The three fixes below
  were then re-verified against a frontend rebuilt from this branch.
- **OS**: Debian GNU/Linux 12 (bookworm), amd64 sandbox, 4 vCPU, 7 GiB.
- **Toolchain**: Go 1.26.6 and Node.js 20.19.0.
  - `npm ci && npm run build` in `ui/` produced the Vite build.
  - `go build ./cmd/grain` embedded it.
- **Browser**: Playwright 1.62.1, Chromium headless shell 151.0.7922.34
  (`chromium-1234`).
  - Desktop runs used a 1400×950 viewport.
  - The layout checks used 390×844.
- **Servers**: two throwaway `grain demo` processes. Each had its own
  embedded SQLite store under a scratch `TMPDIR`/`HOME`:
  - `grain demo -addr 127.0.0.1:8787 -open=false -as alice`: used for
    the Repos list, the repo page, filing a task, memory, and the
    stubbed GitHub boundaries.
  - `grain demo -addr 127.0.0.1:8788 -open=false -as alice -fake-ci
    -github-sim -secrets-store`: used for branch rules, the agent and CI
    settings, and releases. Its simulated GitHub has `main` and
    `release/1.0`.
- **Session**: loopback-trusted operator `alice` (`grain.username` in
  localStorage), with the `X-Grain-Admin-Mode: on` header the Playwright
  suite sends.
- **Model / Gemini**: the sandbox had no `/home/debian/.gemini-api-key`,
  so no model was involved.
  - Every step was a scripted Playwright gesture on a visible control:
    click, fill, Tab, Enter, Escape, drag, browser Back/Forward and
    reload.
  - Every script collected page errors, console errors and warnings,
    failed requests, and 4xx/5xx responses.
  - The scripts lived in an uncommitted scratch directory
    (`/tmp/explore`). No screenshots or browser artifacts are committed.

## Stubs

Grain's own API, store and React UI were never replaced. Only these
boundaries, which the demo does not wire to anything real, were stubbed
or simulated:

| Boundary | How | Why | Responses exercised |
| --- | --- | --- | --- |
| GitHub compare (`GET /api/repos/acme/widgets/compare`) | Playwright `page.route` on that one URL, plain demo | No demo wires a comparer. Unstubbed, it answers 404 "repository comparison is not available on this UI server", even with `-github-sim` (seen on 8788). | one-file diff (merge base `abc1234def`); empty diff; merged; 502 with GitHub's error text; a held request (loading) |
| GitHub branch listing (`GET /api/repos/acme/widgets/github-branches`) | Playwright `page.route`, plain demo | The plain demo has no GitHub client | `["main","candidate","release/4.0"]`; `[]`; 502 |
| GitHub release reconciler | `ui/e2e/releasehelper -dir <store> -release 2.1` (`mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-promoted`, `mark-release-merged`) against the `-github-sim` demo's store | The demo runs no reconciler, so nothing moves a release off Provisioning or a candidate off cutting | provisioning → active; rc.1 cutting → active → promoting → promoted; rc.2 cutting → active; merged with PR link |

## Steps and outcomes

### Repos list

1. **Opening the list.**
   - The rail's **Repos** button opened `/t/default/repos`.
   - A cold `/repos` landed on the same URL.
   - The list showed `acme/widgets` (ten state chips, "11 tasks") and
     `grain/config-default` ("0 tasks").
2. **Adding repos.**
   - Added `acme/gadgets` and
     `example-corporation/an-exceptionally-long-repository-service-name-for-layout-testing-purposes-xyz`.
     The row count went 2 → 3 → 4.
   - `bad repo` was refused in an alert: "repo must be owner/name, got
     "bad repo" (one on another git host is host/owner/name)".
3. **Search.**
   - `GADG` matched only `acme/gadgets`, so search ignores case.
   - `zzz` showed "No repos match your search."
4. **Keyboard reorder.**
   - Focused "Move grain/config-default" and pressed Enter.
   - The slots were labelled "Move here, above …" for each other row.
   - Enter on "Move here, above acme/widgets" moved the row, and focus
     came back to "Move grain/config-default".
5. **Pointer reorder.** Dragged the long-named row's handle to the top.
   The order survived a reload.
6. **Sort.**
   - The choices are custom order, name (a–z), active first and most
     tasks.
   - "Most tasks" put `acme/widgets` first, and the move handles went
     away (0 rendered).
7. **Narrow layout.** At 390 px, neither the document nor the main
   column scrolls sideways (`scrollWidth` 390).
8. **Removing a repo.**
   - On `acme/gadgets` → Advanced → **Remove**, a native confirm
     appeared: "Remove acme/gadgets from target repos? …".
   - Cancel left the page in place.
   - OK returned to the list, which then had 3 rows.

### Repo page, task count and filing

9. **Opening a repo.**
   - Clicking the `acme/widgets` row opened
     `/t/default/repos/acme/widgets`.
   - The tabs are overview, ci, merge queue, agents, branches, releases
     and advanced. Each was walked, and the URL hash followed it.
10. **Task count.**
    - "11 tasks filed here" opened
      `/t/default/tasks?f=repo:any:acme/widgets`, with 11 rows.
    - Back returned to the repo page, and Forward returned to the list.
11. **Filing from the repo page.**
    - **New task** opened the drawer with "In team admin", and the
      Repos field was preselected to `acme/widgets`.
    - Created "Walkthrough 10-15 repo task" as task 13: `repo:
      acme/widgets`, team `default`, capabilities `gcp-key, gemini-key,
      self-debug`. The drawer closed back onto the repo page.
    - On the 8788 demo, after `memory` was added as a team default
      capability (step 15), the drawer pre-ticked self debug, gcp key,
      gemini key **and memory**. The filed task carried
      `[gcp-key, gemini-key, memory, self-debug]`, so team repo defaults
      reach the form.
12. **Stubbed GitHub branch listing** (New task → Base branch
    `nope/branch`):
    - With `["main","candidate","release/4.0"]` and with `[]`, the field
      warned "No branch "nope/branch" on acme/widgets: push it first, or
      have the task depend on the one that cuts it".
    - With a 502 it said nothing. That is the designed behaviour
      (`base-branch-unreadable.spec.js`).

### Branch rules, compare and repo configuration (8788)

13. **Branches.**
    - Adding `walk/1015` listed it "-- pending".
    - `bad branch..` raised the alert "invalid branch name" and kept the
      text in the box.
14. **Compare.**
    - Base `main`, head `release/1.0` → **Compare** opened
      `/t/default/repos/acme/widgets/compare/main...release/1.0`.
    - Unstubbed, it shows the server's 404 text.
    - Stubbed:
      - The diff showed the README hunk with "forked at abc1234d" and
        side-by-side/inline/raw views.
      - The empty diff said "There are no changes between these
        branches."
      - Merged showed a "Merged" chip and "Merged into main".
      - The 502 showed GitHub's text.
      - The held request showed "Loading the comparison…".
15. **Agents tab (team admin's own overrides).**
    - Typed a prompt extension, typed a setup command `make deps`, and
      added **Memory** and later **Release notes** to the team's default
      capabilities.
    - Each saved on its own, as a partial PUT to
      `/api/teams/default/repos/acme/widgets/config`
      (`{"promptExtension":…}`, `{"setupCommand":…}`,
      `{"defaultCapabilities":[…]}`). None of the three clobbered the
      others.
    - After a reload:
      - All three values were back.
      - The page said "This repository has team-specific configuration
        overrides active for this team", and offered **revert to global
        defaults**.
      - The summary read "starts with: Self debug, GCP key, Gemini key,
        Memory, Release notes".
16. **CI tab.**
    - The CI capabilities picker offers only CI-grantable capabilities
      (package publish and the three model proxies).
    - A pick saved through `PUT /api/repos/acme/widgets/ci-capabilities`
      and survived a reload.
    - The CI mode radios and the workflow editor rendered. The demo says
      it runs no grain CI ("-ci=false").
17. **Advanced tab.** Declarations read "Read at abc123commit: 1
    template(s), 1 suite(s), 1 schedule(s), 0 trigger(s)". The text says
    the files are edited in the repo, not here.
18. **Releases tab (work ownership and release workflows).** All eight
    ownership rows render with on/off/say-nothing. The 2026-10-13
    regression spec covers the two release switches staying in step,
    and it passed on this branch.

### Memory (8787)

19. **Opening the page.** The rail's **Memory** opened
    `/t/default/memory`. It shows the deployment's own memory, team
    default (empty), and acme/widgets (team default), each with its
    documents.
20. **Opening a document.** **Open** on `release-checklist (this
    deployment)` showed its summary and full text (332 characters).
21. **Correcting the document.**
    - Appended "5. Walkthrough 10-15 correction." and pressed **Save
      document**.
    - The chips became "written today", "edited here" and "365 b".
    - Closing and reopening read the correction back, and so did a
      reload.
22. **Unsaved edit.** Edited the open document, then clicked **Repos**
    in the rail. A native confirm "You have unsaved changes here. Leave
    and discard them?" appeared, and Cancel stayed on Memory.
23. **Keyboard: see bug 1.**

### Releases and release trains (8788)

24. **Opening the pane.** The header's **releases** button (distinct
    from the *releases* tab) opened
    `/t/default/repos/acme/widgets/releases`, which reads "acme/widgets
    has no releases yet".
25. **Name validation.**
    - With an empty name, Create sends nothing.
    - `bad name`, `release/x` and `-x` were refused, and the server's
      rule rendered in an alert ("invalid release name: use letters,
      digits, …").
    - A 70-character name was accepted, since no length cap exists. See
      bug 3.
26. **Creating 2.1.** It opened `/releases/2.1`: "2.1 -- Provisioning",
    "latest: 2.1.latest, prod: 2.1", "2.1.rc.1 -- cutting".
27. **The train**, with the helper standing in for the reconciler:
    - **Active.** rc.1 active. Cut was disabled and Promote enabled.
    - **Promote.**
      - The native confirm read "Promote 2.1.rc.1 to 2.1? This moves
        acme/widgets's 2.1 branch to 2.1.rc.1."
      - Cancel changed nothing.
      - OK made rc.1 "promoting", with both buttons disabled.
    - **Promoted.** Cut was enabled.
    - **Cut new RC.** rc.2 was "cutting", and the history read
      "2.1.rc.2 -- cutting ; 2.1.rc.1 -- promoted".
    - **rc.2 active.** Promote was enabled again.
    - **Merge back.** The native confirm read "Merge 2.1 into
      acme/widgets's default branch? This opens a pull request on GitHub
      from 2.1." When the helper marked the release merged, the pane read
      "Merge requested -- pull request". The link went to
      `https://github.com/acme/widgets/pull/4242` with `target=_blank`.
    - **Delete release.** The native confirm read "Remove release 2.1? …"
      and Cancel kept it.
28. **Deep links.**
    - `/releases/2.1` reopened the release cold.
    - `/releases/nope` quietly became the bare `/releases` URL. This is
      unchanged from 10-13 and noted there.

Console: the only errors were the intentional ones:

- the 400s for invalid repo, branch and release names;
- the demo's 404 for compare;
- the stubbed 502s.

The only `ERR_ABORTED` entries were `/api/tasks` polls cancelled by a
navigation.

## Bugs found

### 1. Opening or saving a memory document dropped keyboard focus (fixed)

- **Reproduction**: plain demo, `/t/default/memory`.
  1. Tab to "Open release-checklist (this deployment)" and press Enter.
     The document opens, but `document.activeElement` is `<body>`.
  2. The same happens after **Save document**.
  3. The next Tab starts again from the top of the page.
- **Cause**: `DocumentRow` disables both buttons while their request is
  in flight, and a browser drops focus from a button it disables.
- **Fix**: `MemoryPanel.jsx`'s `DocumentRow`:
  - It remembers the button a request started from.
  - Once the request settles, it focuses that button again (now "Close",
    or Save document), unless something else took focus meanwhile.
- **Coverage**: `e2e/repository-release-memory-1015-walkthrough.spec.js`,
  "opening and saving a memory document keeps keyboard focus". It also
  corrects the document, reloads, and reads the correction back. It
  fails on the unfixed build ("Received: inactive"). jsdom does not blur
  a disabled element, so a unit test could not see this.

### 2. Changing only the hash didn't switch the repo page's tab (fixed)

- **Reproduction**:
  1. Open `/t/default/repos/acme/widgets#agents`.
  2. Change the address to `…#ci` (or `location.hash = "#ci"`).
  3. The URL says `#ci`, but the agents tab stays selected and the CI
     panel stays hidden.
  4. A reload shows CI.
- **Cause**: `RepoPage` read the hash once, in `useState`. A hash-only
  change is a same-document navigation and remounts nothing.
- **Fix**: `RepoPage` follows `hashchange`. Its own tab clicks still use
  `replaceState`, which fires no `hashchange`, so nothing doubles up.
- **Coverage**:
  - The `RepoPage.test.jsx` test "follows a change to the address's hash
    to the tab it names" fails with the fix reverted.
  - The e2e test "changing only the hash switches the repo page's tab"
    fails on the unfixed build.

### 3. A long release name pushed the releases pane sideways at phone width (fixed)

- **Reproduction**:
  1. Create a release with a 70-character name (valid: the name rule has
     no length cap).
  2. Open it at 390×844. The main column's `scrollWidth` is 737 against
     a `clientWidth` of 390.
- **Cause**: the release picker (`minWidth: 240`, no max) and the
  release's `<strong>` branch names grew to the name's width.
- **Fix**: `RepoReleases.jsx` caps the picker at `maxWidth: "100%"`,
  which ellipsizes it, and wraps the release's detail block
  (`overflowWrap: "anywhere"`).
- **Coverage**: the e2e test "a long release name does not push the
  releases pane sideways" fails on the unfixed build ("Received: 726").

## Noted, not changed

These are judgement calls rather than faults:

- **Two "releases" controls.** The repo header's **releases** button
  opens the release trains. The **releases** tab holds work ownership and
  release workflows. They share a name and lead to different places.
- **Deep link to a removed repo.**
  `/t/default/repos/acme/gadgets`, opened after removing that repo,
  still draws a full repo page with **new task**. It does not say the
  repo is no longer a target.
- **Repos list hint.** The list says "This team allows only
  acme/widgets." while it lists, and in admin mode accepts, other repos.
- **Merged release wording.** A release whose status is *Merged*, with a
  pull request link, reads "Merge requested -- pull request". The code
  comment says this is deliberate.
- **Memory "Save document".** It stays enabled with nothing changed,
  while the scope memory's Save is disabled until an edit.
- **Carried over from 10-13.** Cut new RC is disabled without saying
  why, an unknown release in the URL quietly becomes the bare releases
  URL, and the Repos sort is not remembered.

## Tests run

- `npx vitest run src/components/RepoPage.test.jsx
  src/components/MemoryPanel.test.jsx src/components/RepoReleases.test.jsx
  src/components/MemoryPage.test.jsx`: 146 passed.
- `npx playwright test
  e2e/repository-release-memory-1015-walkthrough.spec.js
  e2e/memory.spec.js e2e/repo-pane.spec.js
  e2e/repository-release-memory-1013-walkthrough.spec.js
  --project=chromium`: 10 passed.
  - Before the fix, against the unfixed frontend, each of the three new
    tests failed on its own.
- Whole suites: see the branch's grain CI.

## Sandbox notes

- `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.
- Playwright's `page.goto` to a URL that differs only in its hash is a
  same-document navigation. That is how bug 2 showed up.
- Capability options are lower-cased by CSS. Their accessible names are
  capitalized ("Memory", "Grain log"), and clicking a selected option in
  the multi-select toggles it off.
