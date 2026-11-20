# 2026-11-20 — Repository, merge queue, compare and release UI walkthrough (task 7546)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-7546-walk-through-repository-merge`.
- **Commit tested**: `2c44ffa0b` (`main` at the start of the run). The fix
  below was re-verified on this branch's build (`37a785bb1`).
- **Build**: `make frontend` (the Vite `ui/` bundle and the Angular `ui-ng/`
  bundle embedded in the binary), then `go build -tags grain_e2e ./cmd/grain`
  so the demo's loopback-only GitHub-simulation routes are compiled in. The
  fix build re-ran `npm run build` in `ui-ng/` before the `go build`.
- **Browser**: Playwright (ui-ng's pinned `@playwright/test`) headless
  Chromium, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`, 1280×900 and 390×844. Throwaway node scripts outside
  the repository drove visible controls (rail links, tabs, pill menus,
  switches, text boxes, buttons, dialogs, Back/Forward, reload) and logged
  every console error, page error, failed request and HTTP ≥ 400.
- **Gemini**: this sandbox has no `/home/debian/.gemini-api-key`, so nothing
  was Gemini-backed and `LOOK=1` was unavailable; layout was checked with DOM
  geometry (bounding boxes, `scrollWidth`).
- **Server**: a real `grain demo -addr 127.0.0.1:7546 -open=false
  -access-code-file … -github-sim -reconcile=1s -fake-ci -base-branch-ci
  -merge-queue-states -qualification` over its own throwaway embedded
  (SQLite) store, signed in through `POST /api/auth/access-code` as
  `alice@example.com`. Restarted once on the fix build.

## Stubs and external-boundary helpers
Grain's own API, the React UI and the Angular UI were never mocked; no
`page.route` was used.
- **GitHub**: the in-process GitHub simulation (`-github-sim`) over a real
  bare repository stood in for GitHub (branches, compare, issue listing and
  import, release branches, RC cut and promote).
- **Seeding**: `git push` into `grain-demo-*/githubsim/acme/widgets.git` of
  `feature/walk-7546` and `fix/#7546-hash` (one commit adding `WALK7546.md`).
- **Direct API calls** only to read persisted state back after a UI mutation.

## Which UI each step ran against
`/next/` now has the Repos list (rail entry and `/next/repos`), every repo
tab including Advanced, work ownership, the qualification plan and CI
capabilities. Still classic-only: the compare page, a per-release view, the
Upstream repository box, Unfixable checks, the merge-queue overlap re-check
panel, and the read-only deployment / `.grain/settings.yml` prompt layers.

| Step | UI |
| --- | --- |
| Repos list: rail, direct URL, counts, Blocked first, search/sort, Add repo, phone width | `/next/repos` |
| Row → task count → filtered tasks, case-insensitive `?repo=` | `/next/` |
| New task from the repo page (repo preselected) | `/next/` |
| Repo tabs, URL, reload, Back/Forward, classic `/settings/<tab>` address, phone | `/next/repos/<o>/<n>/<tab>` |
| Settings → Repositories (base branch, declarations, CI mode, git connection, bypass) | `/next/settings/repositories` |
| CI mode, Watch default branch, base-gate warnings | `/next/` |
| Merge queue switches and bypass confirmation (repo page and Settings) | `/next/` |
| Agents: setup command and unsaved-edit guard | `/next/` |
| Issue import now | `/next/` |
| Branches: Create branch | `/next/` |
| Releases: list, Cut RC reason, refused name, work ownership | `/next/` |
| Per-release URL, unknown release URL | `/next/` (fixed here) and classic |
| Compare (`/repos/<o>/<n>/compare/<base>...<head>`) | classic (no `/next/` page; `/next/` now hands off) |
| Upstream repository (refusal banner clearing), Unfixable checks, Merge when green copy | classic (no `/next/` control) |
| Advanced: declarations, triggers, push refs, Ports, Remove repository | `/next/` |

## Steps and outcomes
### Repos list and task counts (`/next/`)
- Rail **Repos** and a direct `/next/repos` both open the list (heading
  `Repos 1`). The `acme/widgets` row reads `Blocked 1 Proposed 3 Waiting 4
  Running 2 Asked 2 Ready to submit 1 Merging 2 Deferred 2 · 16 open`, Blocked
  first. Filter and sort controls are present (`Filter repos`, `Sort repos:
  Custom order`).
- The open-task link goes to `/next/tasks?repo=acme%2Fwidgets`; Back returns
  to `/next/repos`. `/next/tasks?repo=ACME%2FWidgets` lists the same work
  (case-insensitive).
- At 390×844, **Add repo** sits at x 289–374 (on screen) and the page has
  no sideways scroll (`scrollWidth` 390).
- `/next/repos/acme/old-removed` still renders the repository page with its
  "View tasks in acme/old-removed" link.

### Repo tabs and addresses (`/next/`)
- Each tab rides in the URL: `/next/repos/acme/widgets`, `/ci`,
  `/merge-queue`, `/agents`, `/branches`, `/releases`, `/advanced`. Reload kept
  Advanced; Back went to Releases and Forward to Advanced, with the tab
  highlighted each time.
- The classic `/next/repos/acme/widgets/settings/merge-queue` redirects to
  `/next/repos/acme/widgets/merge-queue`.
- On a phone, a deep link to Advanced keeps the active tab in view (x 278,
  width 96 at 390 wide).
- **Bug 1 (fixed)**: `/next/repos/acme/widgets/releases/1.0` loaded a
  repository called `widgets/releases` on host `acme`, with 400s from
  `GET /api/repos/acme%2Fwidgets/releases/{settings,lifecycle}` and
  `/api/deployment/repos/acme%2Fwidgets/releases/config`. A compare address
  under `/next/` fell through to the Inbox with no explanation.

### CI and merge queue (`/next/`)
- CI mode pill offers `Grain CI (.grain/workflows)`, `GitHub Actions checks`,
  `No CI (merge without waiting)`; each choice persisted (`ci-mode` read back
  `none`, `grain`, `github`).
- Watch default branch on → stored `watchDefaultBranch: true`. With
  `-base-branch-ci` the demo already holds a base-branch row for `main`, so
  `GET /api/base-branch-gates` is `[]` in every mode and no warning shows —
  correct per `pkg/ui/basebranchgate.go` (a row is the opt-in). The warning
  path itself was re-run in `next-repo-base-gate-warning.spec.js` (demo
  without that flag), which passed.
- Merge Queue tab switches: `Merge on green`, `Require branch up to date`,
  `Base-branch repairs bypass human review gates`. The merge-on-green copy
  ("Land approved pull requests automatically once checks succeed.") makes
  no ordering claim; classic's "Merge when green and clean" says the queue no
  longer keeps an order.
- Bypass on asked "Let base-branch repairs merge without a human?" on both
  the repo page and Settings → Repositories; turning it off asked nothing.
  Stored value followed (`true`, then `false`).

### Overview, Agents, Branches (`/next/`)
- Import issues now, with import switched off, reports in place: "issue
  import is not switched on for acme/widgets: turn it on in the repository's
  settings first" (the request is a 400, logged as a console error by the
  browser; expected). The unreadable-source path is covered by
  `next-repo-import-releases.spec.js`, which passed.
- An unsaved setup command, then the rail's Tasks link: the page asked
  "Discard unsaved changes? You have unsaved agent settings changes on
  acme/widgets." and stayed put.
- Create branch `walk/7546-x` from `main`: the row appeared (`pending`) and
  `GET /branches` returned it.
- New task from the repo page opened with `Repository: acme/widgets`
  preselected.

### Releases (`/next/`)
- Release 1.0 listed with rc#1; Cut RC greyed with "Cut RC: 1.0.rc.1 is
  active: a new RC is cut once it is promoted."
- Create release `bad name!`: the refusal sat under the box
  (`role=alert`, no separate toast) and cleared on the next keystroke.
- Work ownership: Standing work switch on; per-item rows for release,
  base-ci, post-merge, ports, triage and each schedule.

### Classic-only steps
- Classic `/repos/acme/widgets/releases/9.9` says "acme/widgets has no
  release named “9.9” -- showing 1.0 instead." 
- Classic compare `main...feature/walk-7546` and `main...fix/%237546-hash`
  show `WALK7546.md +1 -0`, survive a reload; `main...nope/missing` says
  "GitHub has nothing to compare for main...nope/missing" (a 404 from
  `GET /api/repos/acme/widgets/compare`, expected).
- Classic Upstream repository: `not a repo` was refused ("upstreamRepo: repo
  must be owner/name …"); saving `upstream-org/widgets` cleared that banner,
  reload showed it persisted, clearing it stored `""`.
- Classic Unfixable checks box is present (`placeholder cla/google`).
- Advanced (`/next/`): declarations off with its reason, empty triggers and
  push refs, Ports "No branches are paired", Remove asks with the allowlist
  consequence; Cancel keeps the repo.

### Not exercised
- Train execution beyond the demo's seeded RC, and the overlap re-check panel
  (no `merge_recheck` state in this demo; classic-only).

### Console and network audit
Beyond the expected 400s/404s noted above (refused import, refused release
name, refused upstream repo, unknown compare ref), the only errors were bug
1's 400s. One classic page also shows "This UI is not connected to the
deployment's encrypted secrets store." — expected without `-secrets-store`.

## Bugs found and fixed
### 1. Classic per-release and compare addresses broke under `/next/`
- **Reproduction**: open `/next/repos/acme/widgets/releases/1.0` → heading
  `acme/widgets` on host `acme` / repo `releases`, three 400s; open
  `/next/repos/acme/widgets/compare/main...feature/x` → lands on the Inbox.
- **Cause**: `repos/:host/:owner/:name/:tab` accepted any first segment as a
  host; nothing matched a compare path.
- **Fix** (`ui-ng/src/app/app.routes.ts`): the five-segment host route now
  requires a host-looking segment (`.` or `:`), as the four-segment one
  already did; `/repos/<o>/<n>/releases/<r>` opens the Releases tab with
  `?release=<r>`, preselecting that release, or falling back to the newest
  with an in-place note when it is missing
  (`repo-detail-page.component.ts`); `/repos/<o>/<n>/compare/…` is handed to
  the classic compare page with its path intact (slashes and `%23`).
  Classic was correct; no classic change.

## Regression coverage
- `ui-ng/src/app/app.routes.spec.ts` (new): the matchers.
- `ui-ng/e2e/next-repo-classic-addresses.spec.js` (new, real demo with
  `-github-sim -qualification`): per-release address, stale release note
  across a reload with no 4xx, compare hand-off with `/` and `%23`, and a
  named-host tab still loading.
- Ran: that spec plus `next-repo-base-gate-warning`, `next-repo-import-releases`,
  `next-repos-list`, `next-settings-repos`, `next-repo-ci-mode-bypass`,
  `next-repo-advanced`, `next-repo-ownership-plan-caps` (29 passed), and
  vitest for `src/app/repos` and the routes (41 passed).

## Parity gaps left for follow-up (not bugs in what `/next/` has)
- No `/next/` compare page or branch picker, Upstream repository box,
  Unfixable checks, merge-queue overlap re-check panel, or read-only prompt
  layers on Agents. Filed as a follow-up task.
