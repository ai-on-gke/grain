# 2026-11-08 §7 walkthrough — UI narrowing, closed-work server queries, `GrantsPicker` Escape, removed-repo tasks & editor/role-gating polish

Executes **§7** of [`docs/test-plans/2026-11-08.md`](../test-plans/2026-11-08.md) end to end. Every check ran in Playwright Chromium against real servers, at desktop (1440×900) and phone (390×844), in light and dark themes. The plan landed with [Task #3495](/tasks/3495). Feature PRs covered: #859, #867, #870, #880, #882, #893, #898, #903, #904, #928, #930, #931, #932, #933, #934, #935, #936, #938.

## Environment

| Item | Detail |
|---|---|
| Base | `main` at `b3149bb1` (PR #937) |
| Build | `cd ui && npm ci && npm run build`, then `go build -o ~/bin/grain ./cmd/grain` and `go build ./ui/e2e/consolerestserver`. The base binaries went to `~/bin`. The binaries with the fixes went to `~/bin2`, rebuilt after each fix. |
| Servers | (a) `ui/e2e/consolerestserver` (`GRAIN_E2E_ACCESS_CODE` set), seeded for item 1. (b) A fresh `consolerestserver` for item 3a. Its `PUT /api/deployment` `targetRepos` makes it a restricted deployment, and its orchestrator files a real standing session. (c) `grain demo -github-sim -fake-ci -reconcile 2s -open=false -access-code-file …` for items 2 and 3b. The base build served on `:4392` and the fixed build on `:4393`. |
| Seed | [`seed.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/seed.mjs) files **3,540 tasks** over HTTP: **3,500 closed** and **40 live**. Owners rotate over the viewer (`console-rest-e2e-operator`, what `owner:any:me` resolves to), `other@example.com`, `alice@example.com` and `bob@example.com`. That gives 875 closed tasks each. Admission rotates once per owner cycle over `standard` / `bypass` (the value the UI labels Priority) / `durable` / unset, so every owner holds every tier. Seeds 7, 9 and 11 carry `externalRef: INC-99`. They are 17–18 pages deep in newest-closed-first order at 200 a page. Every 50th seed carries `BUG-42`, and every 97th has the title word `kestrelmint`. [`legacy-admission.sh`](2026-11-08-ui-narrowing-grantspicker-polish/legacy-admission.sh) then writes `admission = ''` straight into the store for the unset quarter (884 rows), because `POST /api/tasks` defaults it to `standard`. |
| Harness | Scripts in [`2026-11-08-ui-narrowing-grantspicker-polish/`](2026-11-08-ui-narrowing-grantspicker-polish/): [`narrowing-walk.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/narrowing-walk.mjs) (item 1), [`grantspicker-walk.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/grantspicker-walk.mjs) (item 2), [`repo-walk.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/repo-walk.mjs) (item 3a), [`cross-walk.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/cross-walk.mjs) (item 3b) and [`preview-shots.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/preview-shots.mjs) (before/after element shots). Shared setup is in [`lib.mjs`](2026-11-08-ui-narrowing-grantspicker-polish/lib.mjs). Each script writes `<area>-<label>.json` (every check, with its data) and screenshots beside itself. axe runs with `wcag2a` + `wcag2aa` (`color-contrast` off, as in `ui/e2e`). |

## Outcome

| Run | Checks | Failed |
|---|---|---|
| `narrowing-after.json` (item 1, fixed build, 4 viewport×theme) | 138 | 0 |
| `grantspicker-base.json` (item 2, base) | 145 | 5 (axe on the Add grant… menu: F3) |
| `grantspicker-after.json` (item 2, fixed) | 145 | 0 |
| `repo-base.json` (item 3a, base) | 17 | 3 (count link lists 0 tasks: F2) |
| `repo-after.json` (item 3a, fixed) | 17 | 0 |
| `cross-base.json` / `cross-after.json` (item 3b) | 47 | base: axe `definition-list` on the schedule pane (F4). `cross-after.json` was taken with the F4 change, which was later reverted (see F4), so 0 |

I only probed item 1 on the base build at 1440 light. That probe is where F1 showed up. `narrowing-after.json` is the full run on the fixed build.

## Findings

| # | Finding | Severity | Outcome |
|---|---|---|---|
| F1 | **`owner:any:me` asked the server for somebody else's closed work first.** On a cold load of `/?f=state:any:closed&f=owner:any:me&f=admission:any:durable`, the first targeted request was `GET /api/tasks?state=closed&owner=operator`. A second request for `owner=console-rest-e2e-operator` followed once `GET /api/config` answered. `closedServerFilters` expanded "me" with no config, and `expandOwnerFilterValue` falls back to `"operator"`. On a deployment where a real `operator` owns closed work, that answer is someone else's rows. Elsewhere it is a wasted request. It is a race: the single-chip run happened to get the config first. | Low–Medium | **Fixed.** `closedServerFilters` (`ui/src/taskFilters.js`) leaves "me" unpinned until the config has answered. The first targeted request now names the viewer in every run (`firstTargeted.owner = console-rest-e2e-operator`). Vitest covers it in `taskFilters.test.js`. The Playwright regression `narrowing-grantspicker-polish.spec.js` holds `/api/config` 1.2 s. On base it reads `["", "operator", "debian"]` and fails. |
| F2 | **A removed repo's count link listed none of the tasks it counted.** `/repos/ACME/legacy-service` (mixed case) correctly said "3 tasks still target this repo, which is no longer on the target list". Its link `/?f=repo:any:ACME/legacy-service` drew **0 rows**, at both widths. The `repo` attribute compared case-sensitively against the stored `acme/legacy-service`. `/?f=repo:any:acme/legacy-service` drew the 3. | Medium (the #893 link was dead for any mixed-case URL) | **Fixed.** The `repo` attribute (`ui/src/taskAttributes.js`) compares without case, the way `state.js`'s `repoKey` finds the page's tasks. After the fix the link draws exactly the 3 tasks and not the session (`repo-after.json`). Covered by Vitest (`the repo attribute`) and the Playwright regression. |
| F3 | **axe: the Add grant… menu's group titles broke `role="menu"`.** `listitem` (26 nodes) and `aria-required-children` fired in all five GrantsPicker hosts. `ListSubheader` is a bare `<li>` inside MUI's `<ul role="menu">`. | Low (a11y) | **Fixed.** The titles are named separators (`role="separator"`, `aria-label` = title), which a menu may own. MUI `MenuList` still skips them on arrow keys, since they have no `tabindex`. Desktop and phone shots of the open menu match the base pixel for pixel (`addgrant-{before,after}-*.png`). The only difference is one caret pixel and the nav rail's demo counts. |
| F4 | **axe: the "What this files" preview broke its `<dl>`.** `definition-list` fired on the schedule pane: each row put its source `Chip` beside the `<dt>`/`<dd>` inside the `<dl>`'s row `<div>`. | Low (a11y) | **Not fixed here; filed as a follow-up.** Moving the chip inside the `<dd>` cleared axe and left the preview byte-identical at 1440 and 390, light and dark (`preview-{before,after}-*.png`). But `template-priority.spec.js`, `processes-keep-running.spec.js` and `processes-phone-layout.spec.js` read the `<dd>` as the bare value and the chip as the row's direct child, so `ui e2e` went red, and I reverted the change. The follow-up has to move the chip and update those specs together. |
| N1 | **Plan wording: there is no Kanban "group by" attribute.** A Kanban board's columns are states (`model.BoardColumn`), so a saved board cannot be grouped by `admission`. I saved a Kanban board narrowed by `admission:any:durable` with `sort=admission:desc` instead. It loads with the chip "Admission is any of Durable" and no unknown-condition warning. | Note | Plan overreach, not a bug. |
| N2 | **Plan wording: the tier is `bypass`, not `priority`.** `POST /api/tasks` refuses `admission: "priority"` with `want standard, bypass or durable`. The UI labels `bypass` as Priority. A stored `""` is served as `standard` (model scan), so only an older server sends `""` over the wire. The UI's `|| "standard"` fallback still covers that. | Note | — |
| N3 | **New Task keeps a draft rather than asking `DISCARD_PROMPT`.** Escape #3 in the dirty New Task panel closes it without a prompt and keeps the draft (#933). Reopening `/new` restores the typed title. That is by design, unlike the Template and Schedule overlays, which prompt. | Note | — |

## Item 1 — closed-work owner / externalRef queries & admission at scale

Run on the fixed build: `narrowing-after.json`, 138 checks, 0 failed, at 1440 and 390, light and dark. Every case loaded `/?f=state:any:closed&f=<conditions>[&q=…]` and recorded every `GET /api/tasks?state=closed`. It then wheeled the list's end into view four times, read the rows, and compared them with the server's own paged answer for the same question:

| Case | First targeted request | Rows drawn / expected | Plain closed pages read past page 1 |
|---|---|---|---|
| `owner:any:me` | `owner=console-rest-e2e-operator` | 875 / 875 (5 targeted pages, scroll-driven) | 0 |
| `owner:any:other@example.com` | `owner=other@example.com` | 875 / 875 | 0 |
| `externalRef:any:INC-99` | `externalRef=INC-99` (1 request) | 3 / 3 (seeds 11, 9, 7, ~3,490 tasks deep) | 0 |
| `owner:any:me` + search `kestrelmint` | `owner=…&search=kestrelmint` (1 request) | 10 / 10 | 0 |
| `owner:any:me` + `admission:any:durable` | `owner=console-rest-e2e-operator` | 219 / 219 (admission applied client-side) | 0 |

- No case drew a duplicate row or a stray. Rows stayed in closed order, compared at the millisecond the browser's `Date` holds. seed.mjs closes 8 tasks at a time, so sub-millisecond ties can be drawn either way.
- No page overflowed horizontally.
- Removing the chips (and emptying the box) returned to the plain newest-closed first page, with no out-of-place hits (#870) and no further targeted request.
- **One request, not 18×200 pages:** without the server pin, INC-99 would need ~18 pages.
- **Race:** `owner=alice` answers were held 2.5 s while the URL moved on to `owner:any:bob` 150 ms later (`history.pushState` + `popstate`, in-app). The list drew 200 of bob's rows and 0 of alice's in all four runs.
- **Admission sort:** `sort=admission:asc` ranks `standard` and legacy `""` (48 legacy rows in the first 200) first, then `bypass`, then `durable`. `desc` reverses it. `admission:any:standard` keeps the legacy rows. `admission:any:bogus` draws one warning chip, "Admission is any of “bogus” (unknown)", without overflow. axe found nothing on the narrowed list.
- **CLI parity** ([`cli-parity.txt`](2026-11-08-ui-narrowing-grantspicker-polish/cli-parity.txt), `grain list` against the same server):
  - `-closed -filter admission:any:durable -owner me` gives 221 rows, which is the UI's 219 closed plus 2 live.
  - `-filter admission:any:standard -search 'seed 001'` gives seeds 12–19, including the legacy 12–15.
  - `-sort admission:asc` puts standard first and durable (10, 11) last; `desc` reverses it.
  - `-filter admission:any:bogus` fails with exit 1 and `unknown admission "bogus": one of standard, bypass, durable`.
  - `-sort admission:sideways` fails with `asc or desc`.
  - `-external-ref INC-99` gives the same 3 tasks.

## Item 2 — multi-layer Escape across every GrantsPicker host

`grantspicker-walk.mjs` covers five hosts at four viewport/theme combinations: the New Task panel (`/new`, Title dirtied), the Template overlay (`/processes/templates/template-2`, Name dirtied), the Schedule overlay (`/processes/schedules/sched-2`, Title dirtied), the Task side panel (`/tasks/2`, clean) and the Console Session panel (`/sessions` → Director → Edit capabilities, clean). With the fix it ran 145 checks with 0 failures. In every one of the 20 runs:

1. **Escape #1**, with the nested "Grain API grants apply to" repository `SearchSelect` listbox open, closes only the listbox. The menu stays and no dialog appears.
2. **Escape #2** closes only GrantsPicker. `document.activeElement` is **Add grant…**, the URL is unchanged, and there is no `confirm`.
3. **Escape on a focused disabled policy/CI-only link** inside the menu closes only the menu and restores focus to Add grant….
4. **Escape in the very next task after `click()`** closes only the menu, with no dialog. In 16 of 20 runs no animation frame had run before the keydown. The gap was 37–57 ms of React render, and the menu had not painted. In 4 runs on a loaded machine one frame ran. Real-input Escape immediately after the click also closes only the menu.
5. **Escape #3:**
   - The Template and Schedule overlays ask `DISCARD_PROMPT` ("You have unsaved changes here. Leave and discard them?").
   - The clean Task and Session panels close.
   - New Task closes and keeps its draft (N3).

The base build passes the same behaviour checks. Its only failures are F3's axe violations.

## Item 3a — removed repo on a restricted deployment

`repo-walk.mjs`, on a fresh `consolerestserver`:

1. Set `targetRepos: ["acme/widgets", "acme/legacy-service"]`.
2. File 3 tasks with `repo: "Acme/Legacy-Service"` (stored `acme/legacy-service`, `proposed`).
3. Start a standing session with `writes: [{repo: "Acme/Legacy-Service"}]`.
4. Set `targetRepos: ["acme/widgets"]`.

At every viewport and theme:

- `/repos` omits the repo.
- `/repos/ACME/legacy-service` reads exactly "3 tasks still target this repo, which is no longer on the target list". The session is left out.
- At 390 px the sentence wraps inside the viewport: `scrollWidth - clientWidth = 0` on the document and on every ancestor, with the right edge at 349.5 px of 390.
- axe found nothing.
- The link lists the 3 tasks, but only after F2's fix. On base it listed 0.

## Item 3b — cross-surface sweep (#903, #928, #931, #933, #934, #935, #936, #938)

`cross-walk.mjs` over `grain demo`, 47 checks, 0 failed on the fixed build:

- **#933 + #903 + hosting:**
  - A New Task draft ("Draft walk title") was saved, then "New task from this template" was opened on a template with `priority: 8` and `hosting: console`.
  - Task priority reads **8** ("default 8, from the template"), and the draft's title is kept. The console hosting is shown.
  - Base branch `bad::head` shows `"bad::head" has more than one ':': a fork head is <owner>:<branch>` and holds **Create task**. `forkuser:feat` releases it.
  - No overflow. axe found nothing.
- **#936:** a schedule filed from that template shows the "What this files" preview with a Hosting row reading interactive console. The pane fails axe `definition-list` (F4, still open).
- **#928 / #935 role gating:** `/api/config` `access` was rewritten per role.
  - A viewer sees no `Move task …` handles in the list or on a saved Kanban board.
  - A developer and an operator see 16 list handles and 16 Kanban handles (15 enabled).
  - Every handle's accessible name carries the task id ("Move task 1").
  - I did not re-walk the pending-access **Grant** buttons; `ui/e2e/role-gated-actions.spec.js` covers them in CI, and the demo has no live access request.
- **#931:** with `GET /api/boards` answering 503, `/boards` shows "The boards could not be loaded (boards store unavailable)." and **Retry** instead of "No boards yet", without overflow. Retry, with the route released, draws the boards. axe found nothing.

## Regressions added

- `ui/src/taskFilters.test.js`:
  - "me" is not pinned before the config answers; a loaded config with no viewer still reads "operator".
  - A repo condition matches in any case, and "No repo" still means none.
- `ui/e2e/narrowing-grantspicker-polish.spec.js` (demo fixture; the task it files is deleted in `finally`). All three tests fail on the base build and pass with the fixes:
  - F1: `/api/config` held 1.2 s, and no `owner=operator` request is sent.
  - F2: `repo:any:ACME/Widgets` draws the row.
  - F3: axe `listitem` / `aria-required-children` on the Add grant… menu.

## Screenshots

`make ui-screenshots PAGES=/,/boards,/processes/schedules/sched-2,/processes/templates/template-2,/repos` at desktop and phone, light and dark, against the base demo ([`contact-sheet-before.png`](2026-11-08-ui-narrowing-grantspicker-polish/contact-sheet-before.png)) and the fixed demo ([`contact-sheet.png`](2026-11-08-ui-narrowing-grantspicker-polish/contact-sheet.png)). I compared each pair pixel by pixel:

- The schedule and template panes are identical at phone width. At desktop the only difference is the rail's demo counters.
- `/`, `/boards` and `/repos` differ only in demo counts and badges. The two demos had served different walks. `/repos` text is identical at 390 px.
- Element shots of the changed parts (`preview-*`, `addgrant-*`) are identical, apart from the rail and one caret pixel. The `preview-after` shots were taken with the reverted F4 change.

I could not view the PNGs as images in the sandbox, and `LOOK=1` needs a Gemini key. Layout was therefore checked by these pixel diffs and by DOM assertions (bounding boxes, `scrollWidth <= clientWidth`), not by eye.
