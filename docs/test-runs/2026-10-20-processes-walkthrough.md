# 2026-10-20: Processes walkthrough, SET-2 test pass

This is the test task for stage (b) of `design/design/settings-panel.design.md`
(SET-2, at e4761b5). It checks `ui/e2e/processes-walkthrough.spec.js`
and the stage's unit tests against the bullets the doc lists, fills the
gaps, and records the run. The first run of the spec, made with the
stage itself, is `2026-10-20-processes.md`.

## How it was run

- Base: `main` at e22a37b.
- `grain demo -sensitive-hold`, the e2e fixture's own server, built by
  `ui/e2e/global-setup.js`. It ran against the built frontend
  (`cd ui && npm ci && npm run build`).
- `PROCESSES_SCREENSHOTS=/tmp/shots npx playwright test
  processes-walkthrough --project=chromium --repeat-each=3`, using
  Playwright 1.62.1 and Chromium at the default 1280×720 viewport.
- There are no stubs and no mocked grain APIs. Every request reached the
  demo's real API, including the suite run the spec starts with `POST
  /api/suite-runs`.

Result: 3 tests, each run 3 times. 9 passed and none were flaky.

## The doc's Playwright bullets

| Bullet | Covered by | Before this pass |
|---|---|---|
| `/schedules/<id>` redirecting | "redirects the old paths": the demo's first schedule opens `ScheduleOverlay` at `/processes/schedules/<id>` | covered |
| `/templates` redirecting | the same test: lands on `/processes?kind=template` | covered |
| `/suites/runs/<id>` redirecting | the same test: a real run, started on the demo's first suite, lands on `/processes/suites/runs/<id>` with `SuiteRunPane` (suite heading, "Pass 1"). A missing run (`404`) lands on `/processes` and names the missing run | **gap:** only the missing-run case was walked, so the redirect to a real run's pane was never seen. Added |
| filtering to Suites | "filters by each kind…": every chip, including Suites, writes `?kind=<kind>` and narrows the rows to that kind. Narrowed to Suites, New skips its menu | covered |
| creating a template through New → Template | the same test: New, then Template, opens "New template". The form is filled in (name, task title), the template is saved, and the row appears in the table with its title. `GET /api/templates` lists it | **gap:** only New → Schedule was walked. Added |
| a pinned "Schedules" item from before the stage shows as Processes | "a Schedules pin from before the stage…": `[inbox, schedules (red), suites]` under the old per-user key loads as `[inbox, processes (red)]` and is written back that way | covered |

## The unit tests

- **`ProcessesList.test.jsx`** covers every bullet the doc lists:
  - the rows per kind, chips and counts ("lists every kind…");
  - the `?kind=` round trip ("narrows to the kinds picked…" writes it,
    and "opens with the kinds a ?kind= link named" reads it);
  - a template fired by two schedules ("shows a template's last and
    next runs…");
  - the workflow-schedule classification (one `workflowReview` schedule
    counted as a Workflow row, the other three as Schedules);
  - the system lock;
  - the create menu and the skip when one kind is filtered;
  - the declared chip.

  The declared-chip check was weak. It accepted any text matching
  `/nightly\.yml|declared/i` in the workflow row. It now asserts that
  exactly one `DeclaredChip` is drawn, on the declared row, labelled
  "Declared by acme/widgets", with the file in its tooltip. The rows
  that nothing declares get none.
- **`processes.test.js`**: has 11 tests covering normalisation, the
  derived runs, the kind query, sort, search and trigger reading. No
  change.
- **`paths.test.js`**: covers the §5.4 table. That means every
  `/processes…` path, the trigger path, every old-to-new redirect row,
  `?kind=`/`?q=`/`?sort=` and `processPath()`. No change.
- **`pinnedNav.test.js`**: covers the §5.6 rows. Each of `schedules`,
  `templates` and `suites` becomes one Processes pin at the first one's
  place, keeping its colour and icon. A second load changes nothing and
  writes nothing. No change.

`npx vitest run` over those four files gives 166 passed.

## Screenshots

These are in `2026-10-20-processes-walkthrough/`. The rest of the
spec's screenshots are unchanged from `2026-10-20-processes/`.

| | |
|---|---|
| `processes-suite-run.png` | `/suites/runs/<id>` redirected to the run's pane |
| `processes-new-template.png` | the template created through New → Template, as a row |

## Found while running it

Nothing new. Neither added step turned up a fault in the page.
