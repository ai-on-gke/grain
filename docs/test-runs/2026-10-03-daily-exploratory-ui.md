# Daily Exploratory UI Testing

**Date:** 2026-10-03
**Branch:** `grain/task-1635`
**Commit tested:** `90490b285` (`Merge pull request #2746 from bwsalmon/grain/task-1632`), the tip of `candidate`. The footer read `v1.6.0-rc.1.0.20261003134344-90490b28592f`.
**Environment:** Debian GNU/Linux 12, Go 1.26.6, Node.js 20.19.0, Playwright (from `ui/node_modules`), Chromium headless shell and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build`, over the throwaway embedded SQLite store.
**Model and tooling:** no model was called. Every step was a Playwright script.

## How it was driven

- **Scripts:** Node scripts using Playwright's `chromium`/`webkit` API, kept out of the tree.
- **Sign-in:** the seeded `operator` system admin was registered through the "New user" tab, and the saved storage state was reused.
- **What every script recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`;
  - every mutating request and its body.
- **Layout checks:**
  - horizontal overflow;
  - a near-empty body;
  - crash or error-boundary text.
- **Server-side checks:** same-origin `fetch` from the signed-in page, so each request carried the session cookie and CSRF token.

### Stubs

Grain's own APIs and React were not mocked. The resilience and time-zone checks used Playwright's own controls to stand in for a failing server or a reader elsewhere:

| Stub                                                           | Purpose                                  |
| -------------------------------------------------------------- | ---------------------------------------- |
| `PATCH /api/tasks/1` aborted with `internetdisconnected`       | Network loss during a task-edit save     |
| `PATCH /api/tasks/2` → 500 `{"error":"internal server error"}` | Server error during a task-edit save     |
| `POST /api/tasks/1/comments` → 500 `{"error":"boom"}`          | Server error while posting a reply       |
| Browser context `timezoneId: "America/Los_Angeles"`            | A reader outside the deployment's zone   |

## Coverage

### Page-load crawl

- **Chromium:** 82 routes at each of 1920x1080, 1440x900, 768x1024 and 375x812. Routes covered:
  - every rail view;
  - every seeded task, schedule, template, suite, board and team (plus `/teams/<t>/schedules`);
  - all 16 Admin tabs;
  - all 8 Team-settings tabs;
  - Settings, Metrics, `/new`, `/cache`, `/actions-cache`, `/system`, and the repo and releases pages;
  - unknown IDs and malformed queries (`/tasks/99999`, `/schedules/nope`, `/ci/999`, `/admin/bogus`, `/teams/nosuchteam`, `/repos/no/such`, `/suites/runs/nope`, `/boards/nope`, `?state=%%%`, `?sort=bogus&q=<script>…`, `?f=state:zz:qq`).
- **WebKit:** the same 82 routes at 375x812 and 768x1024.
- **Results:**
  - No page errors, horizontal overflow, blank screens or error-boundary text in either engine.
  - The only 4xx responses were the expected `GET /api/tasks/99999` and `/api/ci/runs/999`, and `GET /api/repos/acme/widgets/github-branches` on task pages (404, which the demo cannot serve).
  - The server log recorded one 500, the team time-zone bug below.

### Re-checks of the 2026-10-02 bugs

| 2026-10-02 bug                                                          | Today on `90490b285`                                                                                                                                                                                                |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Team settings tab not in the URL                                        | **Fixed.** Every tab writes `/teams/<t>/settings/<tab>`.                                                                                                                                                            |
| A. Team settings / schedule editor discard an unsaved edit on close     | **Changed.** Editing the team name, switching tabs and pressing Escape now *saves* (`PUT /api/teams/qa-model {"name":"renamed-unsaved"}`). The draft survived the tab switch. Nothing is lost, so this was not refiled. |
| B. Percent-encoded schedule/template/suite IDs report "not found"       | **Fixed.** `/schedules/repo%3Aschedule%3A…` opens the editor and normalises the URL to raw colons. Templates and suites do the same.                                                                                 |

### Journeys

- **Task detail editing:**
  - Save is disabled for a blank (whitespace) title.
  - A 300-character unbroken title saves, and neither the detail nor the list overflows at 1440 or 375.
  - API validation: blank title → 400; an unconfigured `model` → 400; custom fields keyed by their label → 400 "no custom field … is declared" (keys are ids such as `area`, so this is correct).
  - `PATCH {"team":"no-such-team"}` → 200. This is **Bug 1**.
- **Bulk operations:**
  - With three proposed rows selected, the bar offers approve, submit, defer, undefer, close, reopen, bulk edit and capability attach/detach.
  - Bulk edit marks mixed fields "(mixed across the selection)".
  - An invalid base branch applied to three tasks reports "3 of 3 task(s) failed: base … is not a usable branch name", and nothing is changed.
  - Approving a running and a proposed task together approves only the proposed one.
- **Teams:**
  - Creation validation: ID charset → 400; negative limits → 400; unknown role → 400; recreating `default` → 400.
  - An invalid `timeZone` on create → **500**. This is **Bug 2** (update correctly returns 400).
  - An unadvertised `defaultModel` is accepted. The Models tab then says "definitely-not-a-model (no connection advertises it) … Tasks on the default model wait in the queue until one does". That is a designed state rather than a bug.
  - `/teams/nosuchteam` presents as a real, empty team with a "No model connections are configured" banner. This is **Bug 4**.
- **Executors:**
  - Admin › Executors and Team › Executors empty states render.
  - Issue token: Generate is disabled until a name is entered. The token is shown once, and the list shows only the prefix.
  - Revoke asks first, naming the token.
  - The token's Created time is printed in the browser's zone, not the deployment's. This is **Bug 5**.
- **Schedules / templates / suites (API):**
  - Rejected with 400: `everyNHours` 0 or −3; `dayOfMonth` 0 or 32; an unknown overlap policy; an unknown template; an invalid time zone; a schedule `title` alongside a `templateId`.
  - Suites reject count 0 or −1, `maxPasses` 0 and unknown modes.
  - A schedule, template, suite or board with `team: "ghost-team"` → 201. This is part of **Bug 1**.
- **Merge queue / CI:**
  - The repo page draws the in-flight batch ("grain-merge-queue/main at abcdef012345").
  - `/?state=completed` lists the queued-for-merge task, whose detail offers force merge.
  - `/ci` renders the job cache, active sandboxes and an empty runs table.
  - (Demo seed only: task 5 is listed as a batch member by `/api/merge-batches` but has no `mergeQueueState`, so its own page draws no batch card. Not filed.)
- **Resilience:**
  - A failed task-edit save (network loss or 500) shows the error, then closes the editor and drops the edit.
  - A failed reply clears the reply box. This is **Bug 3**.

## Bugs filed

All were filed with `propose_task` against `candidate`. None had a fix on `main` (`git log origin/main`), and none appeared in today's other walkthrough reports.

| #   | Bug                                                                                                                                                                                                                                               |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Tasks (create and PATCH), schedules, templates, suites and boards accept a team that does not exist, for a system admin. Executor tokens already refuse this (9d1ca7304). In `UpdateTask`, `tm != nil && tm.Archived` lets a missing team through. |
| 2   | `POST /api/teams` with an invalid `timeZone` returns 500 (the error only surfaces in `PutTeam` → `Team.Validate`). `PUT` returns 400.                                                                                                            |
| 3   | The task editor closes and loses the edit, and the reply box loses the comment, when the save fails. `App.jsx` `act` swallows the error, so `EditTaskForm` calls `onDone()` regardless and `Timeline.send` has already cleared the textarea.     |
| 4   | `/teams/<unknown>` selects the missing team, shows zero counts and a "No model connections" banner, and team settings says the deployment "has not answered" after a 404.                                                                       |
| 5   | `ExecutorsPanel.jsx` prints Created / Last Connected / Revoked with `toLocaleString()` in the browser's zone. Every other timestamp uses the deployment zone (grain/task-368).                                                                   |

## Setup notes

- `npx playwright install chromium webkit` needed `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy.
- MUI renders button labels lower-case with CSS, but the DOM text is capitalised. Use case-insensitive matches (`hasText: /^edit$/i`).
- The request body for suites is `templateIds` (not `items`), and for schedule times it is `recurrence.timeOfDay`. Unknown JSON fields are ignored silently, so a probe using the response's field names can appear to pass.
