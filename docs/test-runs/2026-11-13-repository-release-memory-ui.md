# 2026-11-13 — Repository, merge queue, compare and release UI walkthrough (task 4728)

## Environment
- **Repository**: `ai-on-gke/grain`, branch
  `grain/098073e4/task-4728-walk-through-repository-merge`.
- **Commit tested**: `4ae6438fd` (`main` at the start of the run; build stamp
  `v0.0.0-20261113022244-4ae6438fdcd4`). The two fixes below were
  re-verified on this branch's builds (`8bba23d4e`, then `03cd6f890`).
- **Build**:
  - Frontends: `make build` (the real Vite `ui/` bundle and the Angular
    `ui-ng/` bundle, embedded in the binary), then
    `go build -tags grain_e2e ./cmd/grain` for the demo's loopback-only
    GitHub-simulation seeding route.
- **Browser**: Playwright 1.62.1 headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `timezoneId: UTC`,
  driven by throwaway node scripts outside the repository through visible
  controls (rail, rows, tabs, radios, checkboxes, switches, pill menus,
  text boxes, the keyboard). Viewports 1280×900 and 390×844, light and dark.
- **Gemini**: there is no `/home/debian/.gemini-api-key` in this sandbox, so
  nothing was Gemini-backed and `LOOK=1` was unavailable. The file reader
  here shows PNGs as bytes, so layout was checked with DOM geometry
  (bounding boxes, `scrollWidth`) as well as the `ui/e2e/screenshot.mjs`
  contact sheets attached to the task.
- **Servers** (each a real `grain demo -open=false -access-code-file …` over
  its own throwaway embedded SQLite store, signed in through
  `POST /api/auth/access-code` as `alice@example.com`):

| Demo | Port | Build | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 4728 | base | `-github-sim -reconcile=1s -fake-ci -scheduled-workflows -base-branch-ci -secrets-store -merge-rules -merge-queue-states -qualification` | the classic walkthrough and the bug reproductions |
| B | 4729 | fix 1 | `-github-sim -reconcile=1s` | re-verifying bug 1 |
| C | 4730 | fix 1+2 | `-github-sim` | re-verifying bug 2 |

## Stubs and external-boundary helpers
Grain's own API, the React UI and the Angular UI were never mocked or
intercepted; no `page.route` was used.
- **GitHub**: the in-process GitHub simulation (`-github-sim`) over a real
  bare repository stood in for GitHub (branches, compare, issue listing and
  import, release branches, RC cut and promote).
- **Seeding the simulated repository**:
  - Two issues labelled `walk-4728` through the demo's loopback-only
    `POST /api/e2e/github-sim/repos/acme/widgets/issues`.
  - `git push` into `grain-demo-*/githubsim/acme/widgets.git` of
    `feature/walk-4728` and `fix/#4728-hash` (one commit adding
    `WALK4728.md` and `pkg/walk.go`).
- **Direct API calls**, used only to read back persisted state and to set a
  repository's CI mode to `none` for the `/next/` reproduction (the
  `/next/` page itself could not set it, which is bug 1).

## Steps and outcomes

### Repo list (`/repos`)
- The rail's repos entry opens `/repos`. `acme/widgets` shows its counts,
  with the `blocked 1` chip first on the row.
- **Add repo**: `not a repo` is refused (`repo must be owner/name…`, 400).
  `acme/gadgets` and the 70-character
  `acme-observability-platform/an-extraordinarily-long-repository-name-4728`
  were added and survived a reload, with no horizontal overflow at 1280 px.
  At 390 px Add repo sits at x 303–374, on screen.
- **Search**: `gadg` narrows to `acme/gadgets`; `zzz` shows
  `No repos match your search.`
- **Reorder by keyboard/tap**: focusing `Move <repo>` and pressing Enter
  picks the row up and offers `Move here, above acme/widgets` and
  `Move here, to the end of the list`. Enter on the last moved the long-name
  repo to the end, and the order survived a reload.

### Opening a repo, its task count and filing
- A row opens `/repos/acme/widgets`; Back returns to `/repos`, Forward to
  the repo.
- `17 tasks filed here` opens `/?f=repo:any:acme/widgets`; Back returns to
  the repo.
- **Filing from the repo page**: `new task` on `/repos/acme/gadgets`
  preselected `acme/gadgets`. Task 25 was filed with `repo: acme/gadgets`
  and `/tasks/25` still names it after a reload.
  `/?f=repo:any:ACME/Gadgets` lists it (case-insensitive matching).
- **Removed repository**: Remove repository on `acme/gadgets` asked
  `Remove acme/gadgets from target repos? …`; accepting returned to
  `/repos`. `/repos/ACME/GADGETS` then reads `1 task still targets this
  repo, which is no longer on the target list`.

### Tabs, URL, reload, Back/Forward, Settings → Repositories
- Each of `ci`, `merge-queue`, `agents`, `branches`, `releases` and
  `advanced` sets `/repos/acme/widgets/settings/<tab>`; a reload keeps the
  tab (`aria-selected=true`). Tab switches replace the history entry, so
  Back leaves the repo page and Forward returns to `advanced`.
- **Phone**: the selected tab is scrolled into view (`advanced` x 242–334,
  `merge queue` 219–334, `ci` 146–236), `scrollWidth` 390.
- **Settings → Repositories** row: `acme/widgets  main  Read  Default
  folders  Grain CI  Not listed`. Its bypass checkbox asks before turning
  on and not before turning off.

### Overview and CI
- **Overview** cards: CI gate, Up to date before merge, Merge when green,
  Issue import, Base repair bypass, Branches. The merge re-checks panel
  shows `1 change on main`, `testing`, `6m of 90m` (later `21m of 90m`),
  the ref and SHA, `/tasks/5` and `#101`; task 5's page shows the same
  panel.
- **CI mode**: GitHub → Overview reads `GitHub CI`; No CI → Overview
  reads `No CI`; grain CI persisted across a reload.

### Merge queue
- The Merge when green copy reads `The merge queue no longer keeps an
  order, so this no longer changes anything…`, with no ordering claim.
- **Base-branch repair bypass**: turning it on asks `Let base-branch repair
  PRs in acme/widgets merge without a human when their CI is green? …`;
  dismissing leaves it off, accepting turns it on, turning it off asks
  nothing.
- **Unfixable checks**: Add is disabled for a blank/whitespace box;
  `lint/walk-4728` was added and survived a reload.

### Agents
- **Issue import**: ticking `Queue imported issues…` shows the trust
  warning (`Turn it on only if you trust everybody who can open an issue
  on acme/widgets…`). The label `walk-4728` was saved and survived a
  reload. `Import issues now` read `Read 2 open issues on acme/widgets:
  imported 0, 2 already imported.` after an earlier pass imported both.
  With source `acme/nosuch-4728` it reads `Could not read the open issues
  on acme/nosuch-4728: … GitHub answered 404: Not Found`.
- **Unsaved edits**: an unsaved issue-import label asks `You have unsaved
  changes here. Leave and discard them?` on `← Repos`. Setup command,
  upstream repository and prompt extension save when the box is left
  (`onBlur`), so `← Repos` saved them rather than asking. Nothing was lost,
  as the read-back showed.

### Branches and Compare
- Compare `main...feature/walk-4728` from the Branches tab opens
  `/repos/acme/widgets/compare/main...feature/walk-4728` with
  `WALK4728.md` in the diff, and survives a reload.
- The direct link `/compare/main...fix/%234728-hash` shows the same diff.
- `main...nosuch-4728` reads `GitHub has nothing to compare for
  main...nosuch-4728 in acme/widgets: one of the two branches does not
  exist…` (404 from the compare route).

### Releases and work ownership
- `/repos/acme/widgets/releases/nosuch-4728` reads `acme/widgets has no
  release named “nosuch-4728” -- showing 1.0 instead…`.
- `Cut new RC` was disabled with `Cut new RC: 1.0.rc.1 is active: a new
  RC is cut once it is promoted.`
- **Promote** asks `Promote 1.0.rc.1 to 1.0? This moves acme/widgets's 1.0
  branch to 1.0.rc.1.`; dismissing changes nothing, accepting gives
  `1.0.rc.1 -- promoted`. Cut new RC then produced `1.0.rc.2 -- active`
  without a reload.
- **Create release**: `bad name` is refused inline (`aria-invalid=true`
  on the box, no toast) and the error clears as soon as the box is edited.
  `2.0-walk4728` was created at `/repos/acme/widgets/releases/2.0-walk4728`
  and survived a reload.
- **Qualification plan**: an unsaved repeat asks before leaving; saving
  persisted the new value across a reload.
- **Work ownership**: `Release: off` persisted across a reload
  (`aria-pressed=true`) and was reset to `say nothing`.

### Advanced
- Declarations and trigger files are shown; no extra push refs or Ports
  table on this store (no paired branches). Remove repository on
  `acme/widgets` asks first and dismissing keeps the repo.

### `/next/` repository page (Angular)
- `/next/repos/acme/widgets/<tab>` renders Overview, CI & Workflows, Merge
  Queue, Agents & Grants, Branches and Releases; an unknown tab opens
  Overview. `← Repositories` goes to `/next/settings/repositories`
  (there is no `/next/repos` list; that URL falls through to the inbox).
- **CI mode pill**: offered `Both Grain & GitHub CI` and `Off`, which the
  server refuses, and showed a repository on `none` as Grain CI: **bug 1**.
- **Base-branch repair bypass switch** (Merge Queue tab and Settings →
  Repositories): turned the human gate off with no confirmation: **bug 2**.

### Console and network audit
- **Uncaught page errors**: 0.
- **Every 4xx** was a deliberate negative case: 400 for `not a repo`
  and `bad name`; 404 for the unknown compare ref and for
  `github-branches` on `acme/gadgets`, which the simulation does not serve;
  400 `PUT /api/repos/acme/widgets/ci-mode` from `/next/` (bug 1).

## Bugs found and fixed

### 1. The `/next/` CI mode pill offered modes the server refuses and misread "none" (`ui-ng/src/app/repos/repo-detail-page.component.ts`)
- **Repro** (demo A):
  1. Open `/next/repos/acme/widgets/ci` and open the `CI mode` pill.
  2. Pick `Off` (or `Both Grain & GitHub CI`).
  3. Then set the repo to No CI on the classic CI tab (or
     `PUT /api/repos/acme/widgets/ci-mode {"ciMode":"none"}`) and reload
     the `/next/` page.
- **Observed**:
  - Step 2: `400 PUT /api/repos/acme/widgets/ci-mode` with `ciMode: must be
    "grain", "github", or "none"`, while the pill went on reading `Off`. The
    server still had `grain`.
  - Step 3: the pill read `Grain CI (.grain/workflows)`. `none` had no
    option, and the pill falls back to its first label.
- **Fix**: the options are now exactly `grain`, `github` and `none` (`No CI
  (merge without waiting)`). A refused save puts the pill back on the
  previous mode, and the card no longer promises "both".
- **Coverage**:
  - Vitest (`repo-detail-page.component.spec.ts`): the option list and a
    repo on `none`; the revert on refusal. Both fail without the fix.
  - Playwright: `ui-ng/e2e/next-repo-ci-mode-bypass.spec.js`, at 1280 and
    390 px, over a real demo.
- **Verified** on demo B at 1280 and 390 px, light and dark: three options,
  menu inside the viewport (x 121+938 at 1280, x 37+316 at 390), `none`
  saved and read back as `No CI` after a reload, no 4xx.

### 2. The `/next/` base-repair bypass switches took the human gate off with no confirmation (`repo-detail-page.component.ts`, `settings-code.component.ts`)
- **Repro** (demo A): on `/next/repos/acme/widgets/merge-queue`, click
  `Base-branch repairs bypass human review gates` (or the row switch on
  `/next/settings/repositories`).
- **Observed**: one click stored `baseRepairBypassesHumanGates: true`. The
  classic Repo → Merge queue and Settings → Repositories both ask first.
- **Fix**: both switches open the shared confirm dialog
  (`shared/confirm-dialog.component.ts`, new `bypassConfirm` helper) with
  the classic wording when turning on. Cancel is focused and Escape
  cancels. Turning off still saves at once.
- **Coverage**:
  - Vitest: the repo page's ask → cancel → ask → accept → off (no ask)
    sequence.
  - Playwright: `next-repo-ci-mode-bypass.spec.js` drives both pages.
- **Verified** on demo C at 1280 and 390 px, light and dark: the dialog
  sits at x 355 w 570 (desktop) and x 16 w 358 (phone), focus starts on
  Cancel, Escape closes it with nothing stored, no 4xx.

## Observations (not changed)
- **Setup command, upstream repository and prompt extension save on
  blur**, so `← Repos` after typing saves rather than asking. Only the
  issue-import boxes (which wait for Save) and the qualification plan ask.
  Nothing is lost.
- **Classic Settings → Repositories still lists a removed repository**
  that has tasks (`acme/gadgets`), as reported on 11-11.
- **There is no `/next/repos` list**; `/next/repos` falls through to the
  inbox. The `/next/` repository page's back link goes to
  `/next/settings/repositories`.
- **The `/next/` repository page covers much less than the classic one**:
  no CI capabilities, issue-import counts, work ownership, compare or
  Advanced tab. This is a parity gap for the Angular migration, not a bug
  in what is there.
