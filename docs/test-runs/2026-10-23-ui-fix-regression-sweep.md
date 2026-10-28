# 2026-10-23: Regression sweep of the 2026-10-22 UI fixes on `candidate`

The walkthrough in `docs/test-plans/2026-10-23.md` §9, run against
`origin/candidate`'s tip in one session. Each fix's own reproduction was
replayed, the cross-checks were run together, and the six listed pages
were shot at desktop and phone widths, in light and dark.

## Environment

| | |
|---|---|
| Commit tested | `cb2ec0888` (`origin/candidate` tip, "Merge pull request #4458 from bwsalmon/grain/eu/task-4671") |
| Frontend | `cd ui && npm ci && npm run build` (Vite) |
| Server | real `grain demo -addr 127.0.0.1:0 -open=false`, one per Playwright worker (`ui/e2e/fixtures.js`), built from the same tree |
| Browser | Playwright 1.62.1, Chromium headless shell 151.0.7922.34 |
| Viewports | 1280×720 (Playwright default, specs); 1440×900 desktop and 390×844 phone (screenshots), light and dark |
| Toolchain | Go 1.26.6, Node 20.19.0 |

All 12 commits listed in §9 are ancestors of the tested tip. `dae7e4143`
(header-rail slot rules) is only on `main`
(`git branch -r --contains dae7e4143` lists `origin/main` and not
`origin/candidate`), so step 4 (`page-header-rail-walkthrough.spec.js` and
the rail on `/`, `/inbox`, `/boards`, `/repos`, `/sessions`,
`/processes`) was **not run**.

## Stubs

Nothing of grain's own was mocked: no React mocks, and no stubbed grain API
the demo can serve. Three boundaries were stubbed with Playwright
`page.route`:

| Route | Why | Where |
|---|---|---|
| `GET /api/executors` → 500 | The demo has no state in which the executor registry read fails, and §9 asks for that error. This is the HTTP boundary the plan allows. | `ui-fix-regression-sweep.spec.js`, "System → Sandboxes says a failed executors read failed" |
| `GET /api/upgrade` (scheduled, then `phase: "ok"` on `main`) | The demo wires no Upgrader, so the Upgrade pane has no status and no Branch box against the real server. This is the same boundary `upgrade.spec.js` stubs. | "a typed upgrade branch survives the status polls" |
| `POST`/other `/api/upgrade` → 409 | Never upgrade from a walkthrough. Nothing was posted (the test asserts zero). | same |

The fixes' own specs (below) bring their own documented stubs, which are
recorded in their 2026-10-22 reports.

## Step 2: each fix replayed

Every reproduction recorded in the `docs/test-runs/2026-10-22-*.md`
reports is encoded in the spec that its fix added. All of them were run on
the tip in Chromium:

```
npx playwright test --project=chromium repo-page-unsaved-edits.spec.js \
  settings-panel-walkthrough.spec.js recurring-composed-walkthrough.spec.js \
  saved-boards-walkthrough.spec.js task_intake_inbox_lifecycle_walkthrough.spec.js \
  repo-tabs-walkthrough.spec.js responsive-navigation-walkthrough.spec.js
103 passed (3.9m)
```

Gaps the specs left were replayed again in the new
`ui/e2e/ui-fix-regression-sweep.spec.js` (9 tests, all passing).

| Fix | Report | Replay | Outcome |
|---|---|---|---|
| `4101f47bb` Memory dirty state, top-bar leave guards, Config repo reload, section pointers | user-system-settings, bugs 1–4 | `settings-panel-walkthrough.spec.js`. The sweep spec also covers a dirty Memory note against the top bar, the rail and Back. | Holds |
| `292c1c403` / `ff622301b` repo, header and settings guards; Back history on the repo page | repository-release-memory, bugs 1–2 | `repo-tabs-walkthrough.spec.js` test 2 and `repo-page-unsaved-edits.spec.js`. The sweep spec adds the repo page against all three ways out, plus Back from New task. | Holds. The repo report's step 2 says New task opens `/new?repo=acme%2Fwidgets`; on this tip the address is `/new` with the repo prefilled in the form. The Back behaviour it describes still holds. |
| `10495fe4e` settings navigation and dirty-state tracking | user-system-settings | `settings-panel-walkthrough.spec.js`, plus the sweep's dirty Agents card | Holds |
| `97fcdeab6` Processes trigger routing, overlay reset, unsaved guards | (commit message; no 10-22 report of its own) | `recurring-composed-walkthrough.spec.js`. Its Vitests run in CI. | Holds |
| `432401911` dependent rows and suite runs refresh | recurring-composed, bug 1 | `recurring-composed-walkthrough.spec.js` tests 13 and 15, plus the sweep's in-app replay (no `goto` between edit and check, with a no-reload marker) | Holds. See the observation below about the schedule's title. |
| `cbf7a13b0` board return focus and dashboard modified clicks | saved-boards, bugs 1–2 | `saved-boards-walkthrough.spec.js` 5c and 6 | Holds |
| `355505e13` session title in the rail and session task sync | responsive-navigation, bugs 1–2 | `responsive-navigation-walkthrough.spec.js` | Holds |
| `9ece8ba1b` Health actions and executor load errors | deployment-operations, bugs 1–2 | The sweep spec (`/api/executors` 500): the error alert shows, the empty-state info does not, and the subtitle points at Settings → Executors | Holds. The `state-sync-failing` "Open System" action can't be reached in the demo (no failing sync), so it rests on its Vitest (`SystemPage.test.jsx`). |
| `c0ace1323` CI job cache unfreeze, Health sync alert, typed upgrade branch | (commit message) | The sweep spec for the upgrade branch. With the fix reverted locally the check fails (`Received: "main"`), and it passes with the fix. The CI job cache part rests on its Vitest (`SandboxHealthPage.test.jsx`). | Holds |
| `890dbc457` run-go-tests extraction, quarantine diagnostics, System copy | deployment-operations, bug 3 | Go and Vitest tests in CI. The `ConnectedExecutors` copy is checked in the sweep spec. | Holds |
| `de15725b9` state-view Clear and local-script approval cache | task-intake-inbox-lifecycle, bugs 1–2 | `task_intake_inbox_lifecycle_walkthrough.spec.js` ("keeps a rail state view across reload, Back and Forward", "surfaces a local-script approval on an already-cached task pane") | Holds |

Every reproduction could still be followed. The only exception is the
`/new?repo=` address noted above, which changed shape without changing
the behaviour under test.

## Step 3: cross-checks (`ui-fix-regression-sweep.spec.js`)

- **Dirty forms.** I reached the Settings → Agents standing instructions,
  a Settings → Memory note and an unsaved repo CI workflow by in-app
  navigation, so Back has somewhere to go. Each was then left via the
  top-bar System button, the rail's Inbox and browser Back. Every way out
  asked **exactly one** question (counted, with a 300 ms window for a
  second guard). On cancel the page kept its address and the edit, and
  nothing was saved (`/api/settings` unchanged).
- **Settings → General** has no draft cards. Its one text box
  (Environment name) commits on blur, so a top-bar click saves it rather
  than asking. Checked: no dialog, the value is saved, and it is still
  there after Back.
- **Processes.** I seeded a template, a suite, a template schedule and a
  suite schedule, then on `/processes`, without reloading:
  - a template title edit renamed its schedule row;
  - a suite rename updated the schedule's `Suite:` chip;
  - Run now on the suite schedule put its run in Suite runs.
- **System.** With `/api/executors` failing, System → Sandboxes says
  "Connected executors could not be read" and not "No remote executors
  connected."
- **Upgrade pane.** A branch typed while an upgrade is scheduled survives
  the next poll, which reports `phase: "ok"` on `main`.

## Step 5: screenshots

I couldn't run `make ui-screenshots` as written on `candidate`. The target,
`ui/e2e/screenshot.mjs` and `docs/ui-screenshots.md` came in with
`cc7658c13` on `main` and were never ported. I ran `main`'s
`ui/e2e/screenshot.mjs` (`git show origin/main:ui/e2e/screenshot.mjs`)
from an uncommitted copy, with
`PAGES=/,/inbox,/processes,/settings/general,/system/health,/system/sandboxes OUT=screenshots/sweep`,
at desktop (1440×900) and phone (390×844), light and dark: 24 shots. I
read them through `LOOK=1` (Gemini) and checked each remark in the DOM.

- `/`, `/system/health`, `/system/sandboxes`: nothing found at either
  width or in either theme.
- `/settings/general`: **defect, fixed (finding 1).**
- `/processes` at phone width: **defect, proposed (finding 2).**
- `/inbox` at phone width: Gemini read Reply/Acknowledge sitting above the
  message body as a mistake. It is deliberate: `style.css` gives an inbox
  row's answers a row of their own under the task line on a phone. Not a
  defect.
- Several remarks on the after-shots were not defects:
  - "content clipped at the bottom" is the viewport edge (the shots are
    not full-page);
  - "a horizontal line through the label notch" is MUI's outlined field;
  - "helper text floating outside the card" was measured: on
    `/settings/caches` and `/settings/general` at 390 px, the text box
    lies inside its `section[data-settings-card]`.

## Findings

### 1. Settings checkbox hints ran into their labels (fixed)

- **Where:** every `FormControlLabel` in Settings whose label carries a
  `<span className="hint">`: General (Show closed tasks by default, Hold
  the queue until the check is done, Wait until this deployment is idle),
  Tasks, Sandboxes, Caches and Credentials.
- **Deterministic reproduction:** open `/settings/general` at any width.
  The label reads "Show closed tasks by default**off (default)**: a task
  list opens…" as one run of text in the label's own type, and the
  checkbox sits against the middle of a four-line paragraph. Measured at
  1440×900: the hint's computed `display` was `inline`, and its top
  (308 px) was the title's own line.
- **Cause:** `.hint` is styled only by `form .hint` (and
  `.detail-side .hint`). The Settings view isn't a `<form>`, so the hint
  got neither the smaller tertiary type nor any separation. This is not
  one of the 10-22 fixes; it dates from the settings split (`e56295569`).
  `main` has the same markup and CSS.
- **Fix:** `ui/src/style.css` now makes
  `.settings-content .MuiFormControlLabel-label > .hint` a block in the
  hint type, with the box beside the label's first line rather than the
  paragraph's middle.
- **Regression test:** a bounding-box check in
  `ui-fix-regression-sweep.spec.js`. The hint must start at or below the
  title line's bottom, and the checkbox centre must be within 4 px of the
  title line's centre.
- **Screenshots:** before and after of `/settings/general`,
  `/settings/tasks`, `/settings/sandboxes` and `/settings/caches` at
  desktop and phone, light and dark. The contact sheets are summarised in
  `2026-10-23-settings-checkbox-hint-before-after.png` beside this report
  (General's Deployment and Updates cards, before above after: desktop
  light and dark, phone light).

### 2. `/processes` rows are squeezed into a 110 px column on a phone (proposed)

At 390×844, the ≤900 px grid (`1.25rem 6.5rem minmax(0, 1fr) auto`, with
1.75 rem side padding) leaves a template row's name, title and repo in a
column 110 px wide. That puts the Use… button in the `auto` column, and
the type chip in a 104 px column of its own. `release-qualification`
breaks mid-word, and its task title wraps to 120 px tall at one to three
words a line. It needs a phone layout for `.process-row`, which is a
design change beyond this sweep, so it was filed as a follow-up task.

Fixed by task 5094: on a phone the kind chip and Use… share a first line
and the name column spans the row (about 326 px at 390 px), pinned by
`ui/e2e/processes-phone-layout.spec.js`.
`2026-10-23-processes-phone-before-after.jpg` beside this report shows
`/processes` at 390 px, before (light, dark) then after (light, dark).

### Observation: a suite schedule's title keeps the suite's old name

`/api/schedules` derives a suite schedule's `suiteName` but stores its
`title` (it defaults to the suite name when the schedule is made). After a
suite rename, the row's `Suite:` chip updates but its link text doesn't.
The 2026-10-22 recurring report describes both as derived. On this tip
only the chip is, and this report treats that as the schedule's own title
working as designed, not as a regression.

## Follow-ups

- Proposed: a phone layout for `/processes` rows (finding 2).
- Proposed: port `make ui-screenshots` (`cc7658c13`) to `candidate`, so
  the standing UI instructions can be followed on candidate work.
- Port finding 1's CSS to `main`, which has the same defect. grain's port
  mechanism carries a `fix:` across when it merges.
