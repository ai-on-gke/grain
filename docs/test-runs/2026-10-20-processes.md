# 2026-10-20: Processes (settings-panel stage b)

Stage (b) of `design/design/settings-panel.design.md`: Schedules, Templates and
Suites became one Processes page. This is the run of
`ui/e2e/processes-walkthrough.spec.js` that goes with it, and the
screenshots in `2026-10-20-processes/`.

## How it was run

- `grain demo -sensitive-hold` (the e2e fixture's own server, built by
  `ui/e2e/global-setup.js`), against the built frontend
  (`cd ui && npm ci && npm run build`).
- `PROCESSES_SCREENSHOTS=/tmp/shots npx playwright test processes-walkthrough`
  in Chromium, at the default 1280×720 viewport.
- No stubs. Every request reached the demo's real API. `-sensitive-hold`
  is the demo's own seed for a declared trigger
  (`.grain/triggers/payments.yml` on acme/widgets).

## What it covers

1. **A pinned "Schedules" item from before the stage shows as Processes.**
   The stored list `[inbox, schedules (red), suites]` loads as
   `[inbox, processes (red)]` and is written back that way. The rail
   shows two Processes entries (the pin and the rail's own) and nothing
   by the old names.
2. **The old paths redirect.**
   - `/schedules/<id>` opens `ScheduleOverlay` at
     `/processes/schedules/<id>`.
   - `/templates` lands on `/processes?kind=template`.
   - `/suites/runs/404` lands on `/processes` with `no suite run "404"`.
3. **The kind filter.** Each chip (Schedules, Workflow schedules,
   Templates, Suites, Triggers) narrows the table to that kind and writes
   `?kind=<kind>`. All clears it. The demo declares no workflow
   schedule, so that chip shows an empty table here. Workflow schedules
   are walked in `workflow-schedules-candidate-walkthrough.spec.js`,
   which now reads them at `/processes?kind=workflow`.
4. **One item of each kind.**
   - A schedule, a template and a suite each open their own overlay at
     `/processes/<kind>s/<id>`, and Escape returns to `/processes`.
   - The trigger opens acme/widgets' repository page.
5. **Creating.**
   - **New** offers Schedule, Template and Suite, plus the hint that
     workflow schedules and triggers are declared in `.grain/`.
   - Schedule opens the existing form, and the saved schedule appears as
     a row.
   - Narrowed to Suites, **New** skips the menu and opens the suite form.

Result: 3 passed.

## Screenshots

| | |
|---|---|
| `processes-all.png` | the whole table, every kind |
| `processes-triggers.png` | narrowed to Triggers |
| `processes-open-schedule.png` | a schedule opened from its row |
| `processes-new-menu.png` | New asking for the kind |

## Found while running it

- **The declarations endpoint nests triggers.** It answers with the
  repository's settings, and the triggers sit under
  `declarationsStatus.triggers`. The first build read a top-level
  `triggers` and listed none.
- **Triggers come from target repos too.** The demo's acme/widgets
  declares only the trigger when `-sensitive-hold` is on, so a repository
  can declare a trigger and nothing else. Processes therefore reads the
  deployment's target repositories as well as every repository a loaded
  row was declared in.
- **Clipped names (found by reviewing the screenshots).** The first
  layout ellipsised names and repos and left list bullets on each row.
  Names, template titles and repos now wrap inside their columns.
