# Repository, release and memory UI walkthrough — 2026-10-11

Task: grain/task-2958. An operator's walk through the repository-centric
screens (Repos list, repo page, branch comparison, repo settings, team
overrides, deployment and repo memory, releases and release runs) in a
real browser against a real `grain demo` server.

## What was tested

- **Commit tested**: `ab0ed8ca9e0c` (`candidate`, "Merge pull request #3442
  from bwsalmon/grain/task-3067"); the UI reported
  `v1.9.0-rc.1.0.20261011200744-ab0ed8ca9e0c`. Fixes below were then
  re-verified on this branch.
- **OS**: Debian GNU/Linux 12 (bookworm), amd64 sandbox.
- **Toolchain**: Go 1.26.6, Node.js 20.19.0, Vite production build of
  `ui/` embedded into `go build ./cmd/grain`.
- **Browser**: Playwright 1.62.1, Chromium headless shell 151.0.7922.34
  (`chromium-1234`), 1400×950 desktop, 390×844 narrow, and a
  `hasTouch`/`isMobile` 390×844 context for tap reordering.
- **Servers**: two throwaway `grain demo` processes, each over its own
  embedded SQLite store under a scratch `TMPDIR`:
  - `grain demo -addr 127.0.0.1:8787 -open=false` (main walkthrough; the
    store was recreated between passes);
  - `grain demo -addr 127.0.0.1:8788 -open=false -fake-ci -github-sim`
    (release runs and CI mode, so Grain's own release-run API answered for
    real instead of being stubbed).
- **Session**: loopback-trusted operator with `grain.username` set in
  localStorage (as `e2e/pageerrors.js` does) and the
  `X-Grain-Admin-Mode: on` header the Playwright suite sends.
- **Model / Gemini**: the sandbox carried no `/home/debian/.gemini-api-key`,
  so no model was involved; every step was driven by scripted Playwright
  gestures on visible controls (clicks, typing, Tab/Enter/Escape, drag,
  tap), with console errors, page errors, failed requests and 4xx/5xx API
  responses collected on every page.

## Stubs

Grain's own API, store and React UI were never replaced. Only these
boundaries, which the demo does not wire, were stubbed or simulated:

| Boundary | How | Why | Responses exercised |
| --- | --- | --- | --- |
| GitHub compare (`ui.Config.RepoComparer`, surfaced as `GET /api/repos/acme/widgets/compare`) | Playwright `page.route` on that one URL | The demo wires no comparer and answers 404 "repository comparison is not available on this UI server" | success with a one-file diff and merge base, empty diff, merged, 502 from GitHub, and a held (loading) response |
| GitHub branch listing (`GET /api/repos/*/*/github-branches`) | Playwright `page.route` | The plain demo has no GitHub client | `["main","candidate","release/4.0"]`, `[]`, 502 |
| GitHub release reconciler | `ui/e2e/releasehelper` (`mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-promoted`, `mark-release-merged`, `mark-release-error`) writing the store the reconciler would | The demo runs no reconciler against GitHub | provisioning → active, cutting → active, promoting → promoted, merge requested → merged, release error |

The unstubbed demo answers were also checked: compare's 404 message
renders in place, and the base-branch field with GitHub unreadable
neither warns nor refuses (the designed behaviour,
`base-branch-unreadable.spec.js`).

## Steps and outcomes

### Repos list

1. Opened **Repos** from the rail and from `/repos` directly: one row
   (`acme/widgets`), header count 1, the one-entry allowlist note.
2. Added `acme/gadgets`, `zeta/alpha-tools`, `beta/b-repo` and
   `example-corporation/an-exceptionally-long-repository-service-name-for-layout-testing-purposes`;
   rows and header count updated immediately.
3. Search: `WIDGETS` (case-insensitive), `alpha` (substring), a
   non-matching query ("No repos match your search."), `/` (all).
4. Sort: Name, Active first, Most tasks, Custom order — drag handles only
   in Custom order.
5. Reorder: HTML drag (`zeta/alpha-tools` above `acme/gadgets`); keyboard
   (focus handle, Enter, "Moving 1 repo" bar with labelled slots, Escape
   cancels and keeps focus on the handle, Tab ×7 to "Move here, to the
   end of the list", Enter — focus returns to the moved handle); click
   pick-up; a move while the list was filtered to `acme` (only the
   visible slot offered; hidden rows kept their places); tap pick-up in
   a touch context. Order persisted across reload
   (`grain.list-order.repos`).
6. At 390 px the page did not scroll sideways (`scrollWidth` 390); the
   long name is ellipsised with the full name in `title`.

### Repo page, task count, filing

7. Clicked the `acme/widgets` row → `/repos/acme/widgets`; Back returned
   to `/repos`, Forward to the repo page.
8. **Bug 1** — the page said "12 tasks filed here" and its link
   (`/?f=repo:any:acme/widgets`) opened a list of 11; see below.
9. Back from the filtered list returned to the repo page; Forward
   re-opened the filtered list.
10. **New task** from `/repos/acme/widgets` and `/repos/zeta/alpha-tools`:
    the Repos field was preselected with that repo; both tasks were
    created (ids 13, 14) with that repo, read back through
    `/api/tasks`, and the drawer closed back onto the repo page.
11. New task with base `candidate`, write repo `acme/gadgets` and
    read-only repo `zeta/alpha-tools`: chips render with a per-repo base
    field; Escape inside the suggestions kept the drawer open. Creating
    was refused with a clear inline reason because `acme/gadgets` was not
    on the allowlist in that (fresh) store — the form kept every value.
    With GitHub branches stubbed empty the form said `No branch
    "candidate" on acme/widgets…`; with it failing it said nothing, as
    designed.

### Repo settings

12. Branches: added `walkthrough/branch-1011` (listed "pending");
    re-adding it gave the "already on this repo's list (pending)" status
    note; `bad branch name..` was refused ("invalid branch name").
13. Compare: base defaulted to `main`, head offered the added branch via
    the datalist; Compare opened
    `/repos/acme/widgets/compare/main...walkthrough/branch-1011`, which
    survived reload and a head-only deep link ("Into the default branch").
    Stubbed success/empty/merged/failure/loading states all rendered
    (diff, "There are no changes between these branches.", "merged",
    the GitHub error text, "Loading the comparison…"); "← acme/widgets"
    returned to the repo page.
14. CI capabilities (`gcp key`), default capabilities (`memory` — "starts
    with: Self debug, GCP key, Gemini key, Memory"), prompt extension,
    setup command, declarations (off → on), "Require branches to be up to
    date" (off → on) and an unfixable check `cla/google` were all edited
    and all read back after reload.
15. Team scope (`?team=default` → `/teams/default/repos/acme/widgets`):
    the banner said the team inherits; a team prompt extension flipped it
    to "team-specific configuration overrides active", survived reload,
    and left the global value untouched. "Revert to global defaults"
    asked first; dismissing kept the override, accepting restored the
    global value.

### Memory

16. Team settings → Models: "Memory carried by every run", "Memory about
    acme/widgets", and documents `release-checklist (this deployment)`
    and `flaky-e2e (acme/widgets)` with age, author and size chips.
17. Opened `release-checklist`, appended a correction, saved, left and
    reopened: the correction was read back ("written today"). Restored
    the original text and confirmed it read back unchanged.
18. Appended a correction to the acme/widgets memory block, saved, read
    it back through `/api/memory` and on reopening ("edited here");
    Revert restored the saved text over a scratch edit; restored the
    original. Escape with an unsaved edit asked "You have unsaved
    changes here. Leave and discard them?" and stayed on dismiss.
19. **Bug 2** — emptying the open document and pressing Save document
    deleted it with no confirmation; see below.

### Releases and release runs

20. **Releases** from the repo page → `/repos/acme/widgets/releases`;
    empty-state alert. Release names `has space`, `../escape` and
    `rel.rc.1` were refused inline; a duplicate `r1011` was refused
    ("already has an unmerged release with this name"). A 120-character
    name was accepted and its page did not overflow at 390 px.
21. **Bug 3** — a release named `main` was accepted, with prod branch
    `main`; see below.
22. `r1011`: Provisioning → (helper) Active with `r1011.rc.1` active;
    Promote asked "Promote r1011.rc.1 to r1011? This moves acme/widgets's
    r1011 branch to r1011.rc.1." (dismiss → still active; accept →
    promoting → promoted); Cut new RC (enabled only once the current RC
    was promoted) → `r1011.rc.2` cutting → active; Merge asked "Merge
    r1011 into acme/widgets's default branch? This opens a pull request
    on GitHub from r1011." → Merge requested → Merged, with a pull
    request link.
23. Delete release on `r1011b` asked "Remove release r1011b? This drops
    the release and its candidates from grain." (dismiss kept it, accept
    removed it); its old deep link and `/releases/no-such-release` both
    landed on the list with `no release "…"`. `/repos/nope/nothing/releases`
    showed the off-allowlist refusal in place of the form. A release
    error recorded by the helper rendered inline beside its branches.
24. Keyboard: from "← acme/widgets", Tab reached the name field, Create
    release, the release selector, the pull request link, Delete release,
    the qualification options, Add item and Save qualification plan.
25. On the `-fake-ci -github-sim` demo: automatic releases off → on
    persisted across reload; dispatched jobs
    `.grain/workflows/image.yml:guest-cache` saved and read back; **Run
    release by hand** was refused while the repo's CI mode was GitHub
    ("grain CI is not enabled for acme/widgets…"); after choosing "Use
    Grain CI" (persisted across reload) a run started, its pane opened
    ("No jobs: this repository declares no workflows…"), Escape closed it
    with focus back on "Run release by hand", and the run was listed
    under CI runs.

Console and network: no page errors and no unexpected failures. The only
4xx/5xx responses were the intentional ones (invalid branch and release
names, refused writes, the demo's own 404 for compare, stubbed 502s, and
the 403s from run-by-hand before grain CI was on); `ERR_ABORTED` on
`/api/tasks` appeared only when a navigation cancelled an in-flight poll.

## Bugs found

### 1. A repo's task count included the standing Director session (fixed)

- **Reproduction**: `grain demo`; open `/repos/acme/widgets`; it reads
  "12 tasks filed here" and the Queued chip says 3 (the rail says 2).
  Follow the link: the list shows 11 rows. The difference is task 12,
  "Director Session" (`consoleSession: "director"`), which the task list
  hides (`hidesStandingSessions`) and the rail leaves out of its counts.
- **Fix**: `repoRows` (`ui/src/state.js`) now skips `consoleSession`
  tasks, the way `Sidebar` does.
- **Coverage**: `state.test.js` ("leaves standing sessions out of a
  repo's counts…"); e2e `repository-release-memory-regressions-walkthrough.spec.js`
  ("a repo's task count is the number of tasks its link opens"), which
  failed against the unfixed build (13 counted, 12 listed) and passes
  with the fix.

### 2. Saving a memory document empty deleted it without confirmation (fixed)

- **Reproduction**: Team settings → Models → Open `release-checklist
  (this deployment)`; clear the Document box; press Save document. No
  dialog appears and `GET /api/memory/documents` no longer lists it.
  `ui.Client.PutMemoryDocument` trims the text and an empty one is a
  delete, while the Delete button asks "Delete the memory document …?
  Nothing else keeps a copy of it."
- **Fix**: `MemoryPanel`'s Save routes a whitespace-only document through
  the same confirmed delete; a declined one leaves the edit on screen.
- **Coverage**: `MemoryPanel.test.jsx` ("asks before a save with no text
  deletes the document", fails without the fix); the e2e spec above
  ("saving a memory document empty asks before it deletes it").

### 3. A release named for the default branch would force-move it on promotion (fixed in the reconciler)

- **Reproduction**: on `/repos/acme/widgets/releases`, create release
  `main`. It is stored with `prodBranch: "main"`. Promotion calls
  `setBranch`, which force-updates an existing branch, so promoting
  `main.rc.N` would reset the repo's default branch to the candidate and
  drop every commit landed on it since the cut.
  `TestSyncReleasesNeverPromotesOntoTheDefaultBranch` reproduces this
  against the git-backed GitHub simulator: without the fix the default
  branch moves off the post-cut commit.
- **Fix**: `pkg/orchestrator/releases.go` refuses to provision a release
  whose prod branch is the default branch (records why once on the
  release, creates nothing), and refuses to promote one, which covers
  releases provisioned before this check.
- **Coverage**: `TestSyncReleasesRefusesToProvisionAReleaseNamedForTheDefaultBranch`,
  `TestSyncReleasesNeverPromotesOntoTheDefaultBranch`.
- **Follow-up**: the pane still accepts such a name, and a release named
  for any other existing branch that is not its own (`candidate`, a
  paired branch) is still force-moved on promotion. That needs a policy
  decision and a GitHub read at create time, so it went to a follow-up
  task rather than this change.

## Observations, not changed

- Choosing "Use Grain CI" applies at once with no confirmation, though
  its own text says a repo with no workflows then "merges nothing".
  It can be switched back just as quickly, and earlier runs left it this
  way on purpose, so it is noted here rather than changed.
- A system admin's run-by-hand accepts any ref, including one the
  simulated GitHub does not have (the fake CI never resolves it). The
  stricter repo-admin check is by design and covered by
  `release-run-ref-checks-walkthrough.spec.js`.
- The release pane has no "train" view separate from a release and its
  candidates; this walk treats a release and its RC history as the
  release train, as `App.jsx` does.

## Tests run

- `npx vitest run src/state.test.js src/components/RepoList.test.jsx src/components/RepoPage.test.jsx src/components/Sidebar.test.jsx src/components/MemoryPanel.test.jsx src/App.repos.test.jsx`: passed.
- `go test ./pkg/orchestrator/ -count=1`: passed.
- `npx playwright test e2e/repository-release-memory-regressions-walkthrough.spec.js e2e/memory.spec.js e2e/repo-pane.spec.js e2e/repository_release_memory_walkthrough.spec.js e2e/repos_workflows.spec.js --project=chromium`: 10 passed.
- No screenshots or browser artifacts are committed; the exploration
  scripts lived in an uncommitted scratch directory.

## Sandbox notes

- `npx playwright install` failed with `self-signed certificate in
  certificate chain` behind the package-cache proxy until it was run with
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- `go build ./cmd/grain` embeds whatever is in `pkg/ui/static` at that
  moment: building it before `npm run build` finishes serves a
  placeholder index, and building it after changing `ui/src` without
  rebuilding the frontend serves the old UI.
