# Daily Exploratory UI Testing & Defect Discovery Report (2026-10-15)

## Summary

- **Date:** 2026-10-15 (task 3778)
- **Build under test:** candidate `c9920e458`. It reports itself as `v1.12.0-rc.1.0.20261015061453-c9920e458b24`.
- **Server:** `grain demo -addr 127.0.0.1:8420 -open=false -observability -merge-queue-states -sensitive-hold -mechanical-port -merge-rules -secrets-store -qualification`, on its embedded SQLite store. The frontend came from `cd ui && npm ci && npm run build`, and was built before `go build`.
- **Browsers:** Playwright 1.62 Chromium and WebKit 26.5, both headless.
- **Viewports:** 1920×1080, 1440×900, 768×1024, 375×812.
- **Personas:** all sent as `X-Grain-User` from loopback.
  - `alice`: system admin and `default` admin. Admin mode is on unless noted.
  - `bob`: `default` member, Admin mode off
  - `carol`: `payments` admin only
  - `erin@example.com`: added during the run through System › Access & accounts. Erin is on the access list but on no team.
  - `dave`: on no team and not on the access list
- **Stubs:** none. Every request went to the real demo server.
- **Verdict:** no new defects; nothing filed. There were no crash screens, no uncaught page errors and no horizontal overflow in any engine, viewport or persona.
- **The 10-13 run's three defects are all fixed:**
  - `bob` now sees acme/widgets's repo settings read-only, with a notice saying who can change them.
  - In the task picker, `14` + Enter attaches task 14.
  - Team settings say "Showing settings for team: admin (default)".

## Coverage

### Passive sweep

- 46 routes:
  - team-scoped: home, tasks, inbox, boards, sessions, repos, the repo page and its releases, schedules, templates, suites, ci, memory, new, and all 8 Team settings tabs
  - 17 System tabs (`/admin/<tab>`)
  - `/metrics`, `/settings`, `/`, and task panes for tasks 4, 6, 18 and 21
- Run passes:
  - Chromium at all 4 viewports as `alice`
  - WebKit at 1440 and 375
  - `bob` at 1440 and 375
  - `carol` on `/t/payments/*`
- Listeners captured console errors and warnings, `pageerror`, failed requests and HTTP ≥ 400, blank and crash text, and `scrollWidth > clientWidth` (with the overflowing elements listed).
- The only signals were the expected ones, the same as on 10-13:
  - `GET /api/repos/acme/widgets/github-branches` → 404. The demo has no GitHub client.
  - `GET /api/relay-settings` → 501, handled as "no relay".
  - `GET /api/teams/default/audit-logs` → 403 for `bob`.
  - `GET /api/tasks/N` → 404 for `carol` on `default` tasks.

### Journeys

| Area | What was exercised | Result |
| --- | --- | --- |
| Team switcher in the top bar (bc629a157) | As `alice` from `/t/default/schedules`, the switcher lists admin and payments. Picking payments lands on `/t/payments` (team home, by design: `switchTeam` resets the view). It does the same from the phone drawer at 375×812. | Pass |
| Pinned items per team (5311ed15f) | Customize → pin "tasks" for payments → save. The pin survives a reload, and `default`'s pins are unchanged. The dialog says "for team payments. Each team keeps its own pinned items." | Pass |
| Team-scoped creation (3b15d9f00, badd810ec) | New task from `/t/default` while `localStorage` still held `payments` opens "In team admin". The Title box has focus (e4d3f0c47). The POST files into default. `bob`, `carol` and `erin` are offered only their own team's config repo and target repos, in both New task and CI start-a-run. | Pass |
| Reader on no team (b66f6b8a1) | `erin@example.com` sees "you are not on any team", with the system admins named and "start a new team", at 1440 and 375. `/new` and `/t/default/*` close onto that page. The top bar says "No team", with no switcher and no + New task. Start a new team → "Erin Lab" → `/t/erin-lab/tasks` with a "3 setup steps left" banner and its own Director session. `dave`, who is not on the access list, gets "access denied" instead. | Pass |
| Stale save (9d55be291, f98d67b43) | Opened the same schedule in two tabs and saved in tab 1, then in tab 2 → `409 {"code":"stale"}`. Tab 2 keeps its pane, explains what happened, and offers "reload". Reload brings back the saved copy. | Pass |
| List search by row chips (5e711ebbc) | On Schedules, "mondays", "daily" and "06:00" each find their row. On Templates, "main" and "release" match the `repo @ base` chip. Suites search by mode as the rows show it ("run 2×"). The search box is not kept in the URL. | Pass |
| CI | Start a run (acme/widgets main → load workflows → observability) deep-links to `/t/default/ci/<id>`, with an active sandbox row, a live job and "(waiting for output...)". A cold load of that link shows the same run. An unknown run id shows "no CI run". | Pass |
| Task pane | Edit title → saved and shown at once. Edit fields opens the custom-field editor. Defer until 2026-10-20 → deferred. The date box has `min` = tomorrow. Typing a past date anyway → the 400 "wake time … is not in the future" shows inline. | Pass |
| Bulk operations | Selecting 2 tasks shows a bar with "2 selected", approve, submit, defer, undefer, close, reopen and bulk edit…. Bulk edit marks the mixed fields "(mixed across the selection)" and offers "apply to 2 tasks". | Pass |
| Task filters | Tested `f=custom.priority:any:high`, `f=state:any:queued\|running`, custom area plus repo, `none`, `team`, `created:after`, `custom.estimate:gt:2`, and `q` with state. Open a task, then Back/Forward/Reload: the URL and chips are stable. `f=nonsense:any:x` and `f=state:frob:queued` show "unknown condition" chips. A bare `state=` is the legacy single-value alias (`paths.js`), so `state=a&state=b` keeps only the first value. | Pass |
| Team settings › Capacity | Saves on blur. `-1` gives "Not saved: use a whole number, 0 or more." and the stored value stays 0. | Pass |
| Resilience (WebKit, 375) | Unknown team, task, schedule, suite, template, board, session, CI run, release and settings/admin tab; malformed `f=`; a task of another team; `/t//tasks`; a `..` path. Each falls back or shows a "no X" notice, with no crash. Going offline for 9 seconds shows the "cannot be reached … retrying automatically" banner, which clears once back online. | Pass |

## Observations not filed

- **Demo seed: the "Security review: Charge in cents" task is filed as a human.**
  - `pkg/demo/demo_sensitive.go` files it through `CreateTask` as `debian`, with base `grain/task-18`.
  - `suggestsBase` therefore counts it as a human choice. New task prefills **base `grain/task-18`** for acme/widgets, and this run's own probe task 23 was filed onto that branch.
  - Production review tasks carry `Origin.Attribution.Actor` = automation `review` (`pkg/orchestrator/review.go`), so this is demo-only. The 10-06 evening run noted it too.
  - Worth knowing before trusting New task's base default in a walkthrough.
- **`grain.selectedTeam` is not updated by a cold load of a `/t/<team>/…` URL.**
  - It is written only when the switcher is used or a task or item is opened.
  - So after following a link into `default`, a bare `/` still lands on the last team picked.
  - This matches `TeamContext.jsx`'s "remembers the last team picked" contract.
- **A malformed percent escape in the path** (`/t/%E0%A4%A/tasks`) gets Go's plain "400 Bad Request" page rather than the SPA. Nothing reaches the app.

## Notes for the next run

- `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt npx playwright install chromium webkit`, then `sudo npx playwright install-deps chromium webkit`.
- Visible labels are lower-cased by CSS (`theme.js`), but accessible names keep their case. Use case-insensitive regexes for button names (`/^edit$/i`, not `"edit"`).
- Team settings tab buttons are not inside a `role=dialog`. Scope them to the page.
- System › Access & accounts' Add Account needs an email-shaped identifier (`#account-identifier`). Add one to get a reader who is on no team.
- CI start-a-run's submit is "start run". A loose `/^start/` also matches the "started newest first" sort button.
