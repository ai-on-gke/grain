# Daily Exploratory UI Testing & Defect Discovery Report (2026-10-12)

## Summary

- **Date:** 2026-10-12 (task 3235)
- **Build under test:** candidate `96d5f5e1c`. It reports itself as `v1.10.0-rc.1.0.20261012140113-96d5f5e1ccb5`.
- **Server:** `grain demo -addr 127.0.0.1:8420 -open=false -observability -merge-queue-states -sensitive-hold -mechanical-port -merge-rules`, on its embedded SQLite store. The frontend came from `cd ui && npm ci && npm run build`. `-github-sim` was dropped because it can't be combined with `-sensitive-hold` (defect 3 below). `-fake-ci` was dropped because it excludes `-observability`, by design.
- **Browsers:** Playwright 1.62 Chromium 151 and WebKit 26.5, both headless.
- **Viewports:** 1920×1080, 1440×900, 768×1024, 375×812.
- **Personas:** all sent as `X-Grain-User` from loopback.
  - anonymous
  - `alice`: system admin and `default` admin, with Admin mode on
  - `bob`: `default` member, Admin mode off
  - `carol`: `payments` admin only
- **Stubs:** none. Every request went to the real demo server. The CI stub is the one `-observability` itself wires.
- **Verdict:** 3 new defects, all filed as candidate tasks. No crash screens, no uncaught page errors and no horizontal overflow in any engine, viewport or persona.

## Coverage

### Passive sweep

- 35 routes:
  - team-scoped: `/t/<team>/…` for home, tasks, inbox, boards, sessions, repos, the repo page and its releases, schedules, templates, suites, ci, and all 9 Team settings tabs
  - `/system/*`, `/metrics`, `/settings`, `/new`, task detail panes for tasks 4, 6 and 18, `/sandboxes`, `/cache`
- Run passes:
  - Chromium at all 4 viewports
  - WebKit at desktop and mobile
  - Chromium as `bob` (non-admin)
- Listeners captured console errors and warnings, `pageerror`, failed requests and HTTP ≥ 400, crash/blank text, and `scrollWidth > clientWidth`.
- The only signal was `GET /api/repos/acme/widgets/github-branches` → 404 on task detail. That's expected: the demo has no GitHub client, and `branchRefs.js` falls back.

### Journeys

| Area | What was exercised | Result |
| --- | --- | --- |
| Team switcher (new, `8c62f4279`) | Mouse and keyboard switching (type-to-filter, ArrowDown, Enter). The URL moves `/t/default/tasks` ↔ `/t/payments/tasks`. Back, Forward and Reload keep the team. Also tested in the phone drawer at 375×812 in Chromium and WebKit. The listbox stays on screen and the drawer closes on select. | Pass |
| Legacy URLs (`35465dd70`) | `/tasks`, `/inbox`, `/schedules`, `/ci`, `/repos/…`, `/tasks/4`, `/admin`, `/admin/teams`, `/teams/payments/settings/models`. Each redirects to its `/t/<team>/…` form, and Back returns to the page before it. | Pass |
| Team home + setup checklist (`1f201114a`) | `default` shows no checklist (complete). `payments` shows "2 steps left": members and model connection open, git and repo done. The `data-done` flags match the team's data. | Pass |
| Team membership isolation | `carol` gets 404 on `default` tasks and sees only `payments` in the switcher. `bob` sees no Force merge on tasks 14–18 and 6. `alice` sees Force merge on all of them (`7a3920c1e`). | Pass, except defect 2 |
| Team settings | Walked all 9 tabs as `alice`, `carol` (on payments) and `bob`. Team name and capacity numbers autosave. A bogus tab falls back to Team. | Defect 1 (Audit / Security as a member) |
| System (renamed, `a0536b15d`) | All 16 tabs. URLs stay `/admin/<tab>` by design. `/admin/bogus` falls back to General. An invalid duration in Engine (`pollInterval: "10s edited"`) gets a 400 toast, and the field reverts. The 501 from `/api/relay-settings` is handled as "no relay". | Pass |
| Metrics release notes (`54e7…`/`4276f0e35`) | `/metrics` renders Weekly and Versions with the W41 summary. | Pass |
| Tasks: create | `/t/default/new`: Create is disabled until both repo and title are set. A title containing HTML/emoji, 150+ characters long, renders escaped. | Pass |
| Tasks: filters and URL sync | `f=state:any:queued` with and without `q=`, checked through Back, Forward, Reload and open-task-then-Back. Values containing `:` and `/` stay stable across reloads. Unknown attributes and operators show an "unknown condition" chip instead of dropping silently. | Pass |
| Tasks: bulk | Selected 2 rows, then Bulk edit priority → high ("Apply to 2 tasks"). Selection clears afterwards. Defer, then Undefer. | Pass |
| Schedules / templates / suites | Opened each new form. Cadence `everyNHours` of 0, −5, 1.5 and 100000 is blocked by native min/max/step validation, and no POST is sent. Composition previews (`/api/*/composition`) update live. | Pass |
| CI / merge queue | The CI "start a run" form, the repo page (merge-queue-held rows, branches, compare, CI capabilities), and the seeded merge-queue states in task rows ("Waiting: the base branch is broken", "Waiting for an approval GitHub requires", "Merge failed"). | Pass |
| Resilience | Malformed and unknown URLs: `/t/nonexistent/*`, `/t/default/tasks/99999`, `/t/default/tasks/abc`, `/t/default/ci/abc`, unknown schedule, template, suite or board, `/t/%E0%A4%A/tasks` (server 400). Offline for 6 seconds, then back online: the disconnection banner shows, then clears, and the list recovers. Tested in Chromium and WebKit. | Pass |

## Defects filed

1. **UI: Team Settings › Audit / Security, as a member.**
   - The cross-team policy switch is enabled for a member, and clicking it returns `PUT /api/teams/default` 403.
   - The panel shows the 403 error Alert and the "No credential audit events recorded yet" empty state together, plus a global toast.
   - Area: `TeamAuditPanel.jsx`.
2. **UI: `/t/default/*` for a user outside `default`.**
   - The `missingTeam` memo in `App.jsx` exempts `default`, so a non-member is left "working in default".
   - The switcher shows a team they aren't on, and lists and team home come up empty with no explanation.
   - The new-task form preselects `default`, and submitting it returns `POST /api/tasks` 403.
3. **demo: `grain demo -sensitive-hold -github-sim` exits at startup.**
   - The seeded "Security review" task is based on `grain/task-N` with no `DependsOn`.
   - With the simulated GitHub, the base-branch existence check rejects that branch.
   - Area: `pkg/demo/demo_sensitive.go`.

## Notes for the next run

- Browser download behind the package-cache proxy needs `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt npx playwright install chromium webkit`. Without it the install fails with `self-signed certificate in certificate chain`.
- Without `X-Grain-User`, every page opens the create-account / log-in dialog, which blocks clicks. That's expected: drive a named persona.
- Task filter URLs use `attr:op:value` with the enum operators `any`/`none`, and custom fields are `custom.<name>`. For example, `f=state:is:queued` is an unknown condition, not a filter.
