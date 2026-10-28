# Daily Exploratory UI Testing

**Date:** 2026-10-01
**Branch:** `grain/task-1368`
**Commit tested:** `f6f03c2b1` (`Merge pull request #2504 from bwsalmon/grain/task-1359`), the tip of `candidate`. The UI footer read `v1.5.0-rc.1.0.20261001115654-f6f03c2b14ac`.
**Environment:** Debian GNU/Linux 12 (bookworm), Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`) and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build` and ran over the throwaway embedded SQLite store. The server log recorded no errors or panics during the session.
**Model and tooling:** no model was called. Every step was a Playwright script.

## How it was driven

- **Scripts:** Node scripts using Playwright's `chromium`/`webkit` API (kept out of the tree), driving the real frontend through visible controls.
- **Sign-in:** the demo enables password auth. The seeded system admin `operator` was registered through the "New user" tab of the sign-in dialog, and the saved browser state was reused by every later script.
- **What every script recorded:**
  - console errors and warnings;
  - page errors (uncaught exceptions, including React render throws);
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`, which is the expected 304 path of the task poll, as noted in the 2026-10-01 deployment-operations report.
- **Layout checks:**
  - horizontal overflow (`documentElement.scrollWidth > innerWidth`);
  - elements reaching past the viewport with no clipping or scrolling ancestor;
  - text clipped by `overflow: hidden` without an ellipsis;
  - a near-empty body or crash text.
- **Mutations:** each was confirmed by reading the API afterwards.

### Stubs

Grain's own APIs and React were not mocked. The resilience pass stubbed network responses with `context.route` and toggled `context.setOffline`. These stood in for a broken proxy, network loss or a half-deployed server:

| Stub                                                    | Purpose                                            |
| ------------------------------------------------------- | -------------------------------------------------- |
| `context.setOffline(true)` for ~12 s                    | Network loss while navigating                      |
| `GET /api/tasks**` → 500 `{"error":"injected failure"}` | Server error on the main list and a task deep link |
| `GET /api/schedules` → 200 `{"not":"an array"}`         | Malformed response shape                           |
| `GET /api/config` → 200 `text/html` sign-in page        | A proxy or sign-in page answering in grain's place |

## Coverage

### Page-load crawl

- **Chromium:** 63 routes at 1920x1080, 1440x900, 768x1024 and 375x812, run both signed out and signed in. Routes covered:
  - every rail view;
  - every seeded task, board, schedule, template and suite;
  - all 17 Admin tabs;
  - Settings, Team settings and Metrics;
  - `/new`;
  - unknown IDs and malformed query strings (`?state=%%%`, `?sort=bogus&q=<script>`).
- **WebKit:** 40 of those routes at 375x812 and 768x1024.
- **Results:**
  - No page errors, horizontal overflow, clipped text or blank screens.
  - The only 4xx responses were expected: `/api/ci/runs/999`, `/api/tasks/99999`, and `GET /api/repos/acme/widgets/github-branches` (404 "not available on this UI server", which the demo cannot serve).
- **Dialog pass:** New task, task detail, schedule, template, suite, Admin (Teams, Executors, Engine), Team settings, Settings, Metrics, all three boards, a CI run and the inbox were opened at 375x812 and 768x1024. None overflowed.

### Journeys

- **Task filters and URL sync:**
  - Searching "pagination" wrote `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`.
  - The Queued, then Closed, rail picks each pushed an entry.
  - Back and Forward stepped between them.
  - Reload kept the state, the search box and the list.
  - The Filters panel listed the two default conditions with editable attribute, comparison and value.
- **Bulk operations:**
  - Defer and Undefer of tasks 1 and 2 restored their original states (proposed, queued).
  - Attaching the Memory capability reached both tasks.
  - Approve on a mixed selection (proposed + awaiting_submit) queued the proposed task and left the other unchanged.
  - The selection clears after each action.
- **New Task modal:**
  - A task was created with custom fields and repo `acme/widgets`.
  - A negative estimate is stored. That is allowed by design: the number-field regexp in `pkg/model/custom_field.go` permits a sign.
  - An invalid base branch was accepted. This is **Bug 2**.
- **Task detail:**
  - Editing the title (PATCH) worked.
  - Posting a comment worked.
  - Deferring until a past date was refused with a 400 and a visible message.
- **Inbox triage:**
  - Submit moved task 6 to completed.
  - Acknowledge closed task 7.
  - An inline Reply re-queued task 4.
  - The inbox then read "Nothing is waiting on you."
- **Schedules:**
  - `Every N hours = 0` or `-5` is blocked by native `min=1` validation, so no request was sent.
  - A monthly schedule was created (`sched-3`).
  - The Repeat options are every-N-hours, daily, weekly and monthly. The time-zone picker has 420 zones, and the time label follows the zone (e.g. "Time (Asia/Tokyo, GMT+9)").
  - Pause and Resume issued `PATCH {"enabled":false|true}`.
  - Run now confirmed, then filed task 14.
- **Templates:**
  - A template with a required input `{{component}}` was created.
  - "Use…" composed a preview whose 400 reads "field "component" is required".
  - Filing with the input filled produced "Audit billing".
  - An undeclared `{{concern}}` is left literally. That is by design (`templatePlaceholder` doc comment).
  - Duplicate template names are accepted. Templates are addressed by ID, so this only shows twice in pickers. Noted, not filed.
- **Suites:**
  - An empty suite is refused client-side.
  - A suite was created with a template and `until_clean` / 5 passes.
  - The "Run a suite" dialog previews each pass.
- **Teams:**
  - Create Team refused the invalid slug `Bad Slug!` (400 with a visible message) and an empty admin list (client-side).
  - It created `explore-qa` and `slug-two`.
  - Switching the rail to Explore QA scoped Tasks and Schedules to 0 under `/teams/explore-qa`, and the scope survived a reload.
  - The Team-card links (`/boards?team=default`, `/repos/acme/widgets`) resolve.
- **Executors:** Issue Token requires a name, but it accepted a nonexistent team. This is **Bug 3**.
- **CI:**
  - Start a run → Load workflows → `observability.yml` → Start run gave a 202 and opened `/ci/<id>`.
  - The run showed live in Active sandboxes and the runs table at 1440 and 375 widths with no overflow.
  - Cancel run (a prompt) concluded it `cancelled`.
- **Unknown IDs in URLs:**
  - Tasks and CI handle these correctly.
  - Schedules, templates, suites and boards do not. This is **Bug 1**.
- **Resilience:**
  - Offline: the last data stayed on screen and the app recovered silently.
  - The 500 stub showed a banner and an empty list. The `/tasks/2` deep link was dropped to `/`.
  - The malformed-shape stub and the HTML-config stub exposed **Bug 4**.
- **Team settings URL:**
  - Team settings opened from `/teams/explore-qa/settings` is rewritten to `/team-settings`. A reload then shows the `admin` team.
  - That half is already fixed on `main` by ce53270d0 and arrives with the next candidate cut, so it was not filed.
  - The selected tab is still not in the URL on `main`. This is **Bug 5**.

## Bugs filed

| #   | Bug                                                                                                                                                                                                                                                                          | Filed against                |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| 1   | Unknown schedule, template, suite or board ID in the URL **pushes** a corrected history entry (history +2, Back bounces back to the bad URL), and no "not found" message is shown. The cause is the async `refresh*` pruning in `App.jsx` running after `mountedRef` is set. | `candidate` (also on `main`) |
| 2   | Task, schedule and template base branch is never validated. `no such branch ..lock` → 201 on create, PATCH, schedule and template. The orchestrator refuses it only at dispatch (`checkout.go`: "not a usable base branch").                                                 | `candidate` (also on `main`) |
| 3   | Issue Executor Token accepts a nonexistent team scope (free-text field, no server check) → 201. Side note: the token list returns `token_hash`.                                                                                                                              | `candidate` (also on `main`) |
| 4   | No React error boundary: a malformed `/api/schedules` white-screens the app (`TypeError: e.find is not a function`). A 200 HTML `/api/config` leaves the loading canvas up forever (9 retries in 25 s, no message).                                                          | `candidate` (also on `main`) |
| 5   | The Team settings tab is not in the URL, so reload, Back and copied links reopen "Team". This is the same class as the Admin-tab fix in task 1359.                                                                                                                           | `main` (builds on ce53270d0) |

## Setup notes

- `npx playwright install chromium` (and `webkit`) needs `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy.
- Every page is behind the "Create account" dialog until signed in. A crawl that only seeds `localStorage["grain.username"]` renders the pages behind the modal, so register or log in first.
- The Create New Team snackbar ("A team must always have at least one user…") outlives a later successful save by up to the global banner's 5-second auto-hide. This is expected behaviour of `showError`, not a defect.

## CI note

The `go` job failed twice in three runs of this docs-only branch: `TestLiveBudgetLedgerConcurrentSaturationAndQueueing` reported "Expected 18 completed agent runs, got 19". One uncached rerun passed. The race had already been fixed on `main` by 06530389c (close each task before finishing its run). That fix is cherry-picked onto this branch, so it reaches `candidate` with this change.
