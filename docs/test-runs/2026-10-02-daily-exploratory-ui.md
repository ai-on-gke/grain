# Daily Exploratory UI Testing

**Date:** 2026-10-02
**Branch:** `grain/task-1543`
**Commit tested:** `cf9d6156c` (`Merge pull request #2609 from bwsalmon/grain/task-1553`), the tip of `candidate`. `/api/config` reported version `v1.5.0`, commit `cf9d6156c9cd`.
**Environment:** Debian GNU/Linux 12, Go (repo toolchain), Node.js 20.19.0, Playwright 1.62.1, Chromium headless shell and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build` and ran over the throwaway embedded SQLite store. The server log recorded no errors or panics.
**Model and tooling:** no model was called. Every step was a Playwright script.

## How it was driven

- **Scripts:** Node scripts using Playwright's `chromium`/`webkit` API, kept out of the tree.
- **Sign-in:** the seeded `operator` system admin was registered through the "New user" tab of the sign-in dialog. The saved storage state was reused by every later script.
- **What every script recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`.
- **Layout checks:**
  - horizontal overflow;
  - elements reaching past the viewport with no clipping or scrolling ancestor;
  - a near-empty body or crash text.
- **Server-side checks:** same-origin `fetch` from the signed-in page, so each request carried the session cookie.

### Stubs

Grain's own APIs and React were not mocked. The resilience pass used `context.route` and `context.setOffline` to stand in for a broken proxy, network loss or a half-deployed server:

| Stub                                                                 | Purpose                                   |
| -------------------------------------------------------------------- | ----------------------------------------- |
| `context.setOffline(true)` for ~6 s                                  | Network loss while on the task list       |
| `GET /api/schedules` → 200 `{"not":"an array"}`                      | Malformed list shape                      |
| `GET /api/config` → 200 `text/html`                                  | A proxy or sign-in page in grain's place  |
| `GET /api/tasks?…` → 200 `null`; `GET /api/teams` → 200 `{"x":1}`    | Non-list answers on the shell's own polls |
| `GET /api/boards` → 500                                              | Server error                              |
| `GET /api/templates` → `[1,2,3]`; `GET /api/suites` → `[{"id":"z"}]` | Lists of malformed elements               |

## Coverage

### Page-load crawl

- **Chromium:** 72 routes at each of 1920x1080, 1440x900, 768x1024 and 375x812. Routes covered:
  - every rail view;
  - every seeded task, schedule, template, suite, board and team;
  - all 17 Admin tabs;
  - Settings, Team settings, Metrics, `/new`, `/caches` and `/actions-cache`;
  - unknown IDs (`/tasks/99999`, `/schedules/nope`, `/ci/999`, `/admin/bogus`, `/no/such/route`);
  - malformed queries (`?state=%%%`, `?sort=bogus&q=<script>…`, `?f=state:zz:qq`).
- **WebKit:** the same 72 routes at 375x812 and 768x1024.
- **Results:**
  - No page errors, horizontal overflow, past-viewport elements or blank screens in either engine.
  - The only 4xx responses were the expected `GET /api/tasks/99999` and `/api/ci/runs/999`, and `GET /api/repos/acme/widgets/github-branches` on every task page (404, which the demo cannot serve).

### Re-checks of the 2026-10-01 bugs

| 2026-10-01 bug                                                                | Today on `cf9d6156c`                                                                                                                                                                                                                                                                                |
| ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. Unknown schedule/template/suite/board ID pushes history, with no message   | **Fixed** (5c08a7ad4). A snackbar reads `no schedule "nope"` and the like, and Back goes to the previous page.                                                                                                                                                                                      |
| 2. Base branch never validated                                                | **Fixed** (f7b4a43ed). `POST /api/tasks {"base":"no such ..lock"}` → 400 "is not a usable branch name". A schedule with base `a..b` → 400. (`baseBranch` is not the field name and is ignored.)                                                                                                     |
| 3. Executor token accepts an unknown team                                     | **Fixed** (9d1ca7304). → 400 `team "no-such-team" does not exist`. The token list no longer returns `token_hash`.                                                                                                                                                                                   |
| 4. Malformed `/api/schedules` white-screens; HTML `/api/config` spins forever | **Fixed** (0398bef98, c36d15297). The render throw is contained under the shell with the rail intact ("render failed in page"). The HTML config shows "grain could not load … isn't grain -- possibly a proxy or a sign-in page". Malformed template and suite elements are contained the same way. |
| 5. Team settings tab not in the URL                                           | **Still open.** Every tab stays on `/team-settings`. It was filed yesterday against `main`, which has no fix yet, so it was not refiled.                                                                                                                                                            |

### Journeys

- **Task filters and URL sync:**
  - Searching "dark" wrote `?q=dark&f=state:none:deferred|closed&f=kind:none:interactive`.
  - The Queued and then Closed rail picks each pushed an entry and kept `q`.
  - Back, Back, Forward, Forward and Reload each restored the URL, the search box and the condition chips.
  - `?f=nosuchattr:eq:x` shows a chip "unknown condition: nosuchattr" (d9d030504).
  - A combined `repo` + `team` + `state:none` + `q` URL loads cleanly.
- **Keyboard:** Tab reaches the first task row (a link to `/tasks/1`). Enter opens it, and Escape returns to `/` (c1d899b68, 59d86ccdc).
- **Bulk selection:** with two rows checked, the bulk bar offers Approve and Submit, and nothing overflows.
- **New Task:**
  - Create is disabled until a title _and_ a repo are set. On `/` the repo picker starts empty and is labelled "Repos the first is the one this task's own work lands in".
  - The base-branch 400 path is covered by f7b4a43ed's own spec. Here it was re-checked at the API.
- **Teams:**
  - Created `qa-1002`. `/teams/qa-1002` scopes Tasks to 0 and Schedules to "No schedules yet", and both survive a reload.
  - Team settings has 8 tabs (team, members, capacity, git identity, connections, models, executors, workflow & tasks). All render without overflow.
  - "Default model" is a free-text field (`gemini-2.5-pro`).
  - The default team carries no model default in the demo, so the task detail reads "Rotation", as 280e9c2fb intends.
  - The repo page has no team prefix (`/teams/<t>/repos/…` → `/repos/…`). The team stays selected in the sidebar, which is by design (`buildPath`).
- **Dirty state:**
  - Admin tabs keep a draft across tab switches.
  - Team settings and the schedule editor drop an unsaved edit on Escape with no prompt. This is **Bug A**.
- **Schedules:**
  - `monthly` day 31 23:59 → next run `2026-10-31T23:59Z`.
  - `daily` `25:00` → 400 "want HH:MM".
  - Unknown recurrence kinds and a numeric `weekday` → 400.
  - The editor offers Pause, Run now, Delete, Cancel and Save.
  - Deep links with raw colons round-trip through click and reload. Percent-encoded ones do not. This is **Bug B**.
- **Suites:** an empty or unknown-template suite → 400 "a suite needs at least one template".
- **Executors:** the Admin › Executors empty state renders. A token scoped to an existing team → 201, and the list shows only the prefix.
- **CI / merge queue:**
  - `/ci` renders the job cache, active sandboxes and the empty runs table.
  - The "Queued for merge" rail item maps to `?state=completed` and lists the seeded task.
- **Resilience:**
  - Offline: the last data stayed up and the app recovered once back online.
  - A 500 on boards leaves the shell up, with the Boards count at 0.
  - `null` tasks and a non-list teams answer leave the shell intact.

## Bugs filed

| #   | Bug                                                                                                                                                                                                                            | Filed against |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------- |
| A   | Team settings and the schedule editor silently discard an unsaved edit when the overlay is closed (Escape or "← back"): no confirm, no draft kept. New task keeps a draft and Admin keeps tab drafts, so this is inconsistent. | `candidate`   |
| B   | `/schedules/`, `/templates/` and `/suites/` with a percent-encoded ID (`repo%3Aschedule%3A…`) report "not found" and fall back to the list. `parseSegments` decodes the team segment but not the ID segments.                  | `candidate`   |

## Setup notes

- `npx playwright install chromium webkit` needed `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy, as noted yesterday.
- `go build ./cmd/grain` embeds `pkg/ui/static`, so rebuild the binary _after_ `npm run build`.
- The task-list rail entries are `div[role=button]` with text such as "Queued3". Select them with a case-insensitive `hasText` filter.
