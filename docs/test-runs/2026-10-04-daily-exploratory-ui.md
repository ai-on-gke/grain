# Daily Exploratory UI Testing

**Date:** 2026-10-04
**Branch:** `grain/task-1838`
**Commit tested:** `c4944f2b8` (`Merge pull request #2900 from bwsalmon/grain/task-1835`), the tip of `candidate`. The footer read `v1.7.0-rc.1.0.20261004205854-c4944f2b8a9f`. That is 321 non-merge commits past yesterday's run (`90490b285`).
**Environment:** Debian GNU/Linux 12, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 (from `ui/node_modules`), Chromium headless shell (`chromium-1234`) and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -merge-queue-states -sensitive-hold -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build`, over the throwaway embedded SQLite store. (`-fake-ci` can't be combined with `-observability`, and the demo refuses the pair.)
**Model and tooling:** no model was called. Every step was a Playwright script.

## How it was driven

- **Scripts:** Node scripts using Playwright's `chromium`/`webkit` API, kept out of the tree.
- **Sign-in:** the seeded `operator` system admin was registered through the sign-in dialog's "New user" tab, and the saved storage state was reused. A first, signed-out crawl was thrown away: the sign-in dialog covers every page until then.
- **What every script recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`;
  - every mutating request and its body.
- **Layout checks:**
  - horizontal overflow of the document;
  - elements inside an open dialog that extend past the viewport;
  - a near-empty body;
  - crash or error-boundary text.
- **Server-side checks:** same-origin `fetch` from the signed-in page (session cookie plus `X-Grain-User`).
- **Confirms:** native `confirm()` prompts (schedule Run now, Revoke) were accepted with a Playwright `dialog` handler. Playwright dismisses them by default, so without the handler, Run now looks like it does nothing.

### Stubs

Grain's own APIs and React were not mocked. The resilience and time-zone checks used Playwright's own controls to stand in for a failing network or a reader elsewhere:

| Stub                                                                   | Purpose                                        |
| ---------------------------------------------------------------------- | ---------------------------------------------- |
| Every `/api/**` request aborted with `internetdisconnected` for 15 s   | Network loss under the polls                   |
| `/api/tasks**` → 503 `text/html`                                       | A proxy in front of grain failing              |
| `GET /api/tasks` → 500 `{"error":"db locked"}`                         | Grain itself failing                           |
| `PATCH /api/tasks/1` → 500 `{"error":"internal server error"}`         | Server error during a task-edit save           |
| `POST /api/tasks/1/comments` → 500 `{"error":"boom"}`                  | Server error while posting a reply             |
| Browser context `timezoneId` `America/Los_Angeles` and `Asia/Tokyo`    | A reader outside the deployment's zone         |

## Coverage

### Page-load crawl

- **Chromium:** 112 routes at each of 1920x1080, 1440x900, 768x1024 and 375x812, signed in. Routes covered:
  - every rail view;
  - every seeded task, schedule, template, suite, board and team (plus `/teams/<t>/schedules`);
  - all 19 Admin tab ids;
  - all 8 Team-settings tabs for every team;
  - Settings, Metrics, `/new`, `/cache`, `/actions-cache`, `/system`, and the repo and releases pages;
  - unknown IDs and malformed queries (`/tasks/99999`, `/schedules/nope`, `/templates/nope`, `/suites/nope`, `/sessions/nope`, `/ci/999`, `/admin/bogus`, `/teams/nosuchteam`, `/teams/default/settings/bogus`, `/repos/no/such`, `/suites/runs/nope`, `/boards/nope`, `?state=%%`, `?sort=bogus&q=<script>…`, `?f=state:zz:qq`).
- **WebKit:** the same 112 routes at 375x812 and 768x1024.
- **Results:**
  - No page errors, document overflow, blank screens or error-boundary text in either engine.
  - The only 4xx responses were the expected `GET /api/tasks/99999` and `/api/ci/runs/999`, and `GET /api/repos/acme/widgets/github-branches` on task, schedule and template pages (404, which the demo cannot serve).
  - The server log recorded no 5xx.
  - Every unknown ID shows a "no schedule/template/suite/session … "nope"" alert over its list, rather than a blank pane.
  - Overlays at 375 px: the only elements past the right edge were tabs inside MUI's scrollable tab strips (Admin, Team settings), which is how those strips scroll.

### Re-checks of the 2026-10-03 bugs

All five were fixed on `candidate` and were confirmed fixed in the browser:

| 2026-10-03 bug                                                           | Today on `c4944f2b8`                                                                                                                                                       |
| ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. Tasks/schedules/boards accept a team that does not exist             | **Fixed** (908f63b75). `POST /api/tasks`, `PATCH /api/tasks/1`, `POST /api/schedules` and `POST /api/boards` with `team: "ghost-team"` → 400 `team "ghost-team" does not exist`. |
| 2. `POST /api/teams` with an invalid `timeZone` → 500                    | **Fixed** (d28f3fe21). Returns 400 `invalid timeZone "Mars/Olympus"`.                                                                                                       |
| 3. A failed task-edit save or reply loses what was typed                 | **Fixed** (7a531b8fb, bf116f79c). After a 500, the editor stays open with the new title, and the reply box keeps its text. Both show the server's message.                    |
| 4. `/teams/<unknown>` presents as an empty real team                     | **Fixed** (a2d46bb21). It falls back to `/` on "all teams", with an alert reading `no team "nosuchteam"`.                                                                     |
| 5. Executor token times printed in the browser's zone                    | **Fixed** (e758b1046). With an LA browser, a token issued at 22:26 UTC reads `Created: 10/4/2026, 10:26:25 PM`, the deployment's zone.                                      |

### Journeys

- **Teams management:**
  - Admin › All teams lists each team's admins, members, limits, credential, config repo and executor bootstrap command.
  - In the Create team dialog:
    - `Bad Id!` → 400 shown in an alert, and the dialog stays open.
    - No members → "A team must always have at least one user with permission to access it".
    - `maxWorkers -2` → 400 "maxWorkers cannot be negative".
    - A valid team (`ui-team`, `maxWorkers 2`, `maxCIJobs 1`, `operator:admin, bob`) is created, and the dialog closes. The previous error's snackbar stays on screen for about 3 s after the success, then hides itself.
  - API: a duplicate id, a bad id charset, negative limits and an empty name on PUT → 400. PUT/DELETE of an unknown team → 404.
  - **Team switcher:** offers every team by name. Picking `payments` writes `/teams/payments?…` and keeps the search. Back/Forward/Reload all restore the team, state and query (`/` → `?state=queued` → `?q=pagination` → `/teams/payments?q=pagination`). The deep link `/teams/payments?state=queued` survives a reload.
- **Deployment ID** (new, 3e4823e71). It sits in Team settings › Models, under Task branches.
  - `Bad_ID!` and `-lead` → 400 "may contain only lowercase letters, digits and hyphens…".
  - 40 characters → 400 "must be at most 32 characters".
  - `staging-1` saves.
  - With the template `grain/{deployment}/task-{id}`, the next tasks were filed on `grain/staging-1/task-19` and `-20`.
  - A rejected value empties the box back to the saved one rather than keeping the typed text for correction. This is the save-on-blur pattern every Settings text field shares, and was not filed.
  - The placement of this deployment-wide section inside a team's settings was already reported in `2026-10-04-release-engineering.md` (finding 4), so it was not refiled.
- **"Can't reach grain" banner** (new, 9460f11eb):
  - With every API request aborted, it appears after the second failed poll (at about 8 s; not yet at 4 s).
  - Clicking a task while offline shows "Couldn't reach grain -- check the connection and try again (Failed to fetch)".
  - The banner disappears on the first successful poll.
  - A proxy's 503 HTML raises the same banner and clears the same way.
  - A grain 500 on the list poll correctly does not raise it.
  - No page errors throughout.
- **Executors:**
  - Admin › Executors empty state renders.
  - Issue token (name `builder-1`, team scope `default`) → 201. The token is shown once with a copy button and a `grain executor --token … --server <server-addr:port>` line. The list shows only the prefix.
- **Schedules:**
  - Changing the time zone to `America/New_York` relabels the time field `time (america/new_york, edt)`. It saves, and the list reads "mondays at 06:00 america/new_york · Next run 10/5/2026, 10:00:00 AM" (10:00 UTC).
  - Pause → the list reads "paused · Held -- fires … if resumed by then" (024a3ee93 verified), and the button becomes Resume.
  - Run now → confirm naming the task → `POST /api/schedules/sched-2/run` 201, and task 24 is queued.
  - Resume restores the next run.
  - API: `timeOfDay` `25:00`, weekday `funday` and an unknown recurrence kind → 400. `9:7` is accepted and normalised to `09:07`.
- **Templates and suites:**
  - Suites reject an empty template list and an unknown mode (the mode is spelled `until_clean`).
  - Templates reject an empty name, but accept a duplicate name. This is **Bug B**.
  - Running a suite from the Suites page:
    - The repo picker lists every team's config repo.
    - The server accepts a default-team run against `grain/config-payments`, and files a queued task there. This is **Bug A**.
  - A template bound to its own repo keeps that repo whatever the run says (`ComposeSuiteTask`, by design).
- **Merge queue and CI:**
  - The `-merge-queue-states` seeds render with their reasons:
    - Task 13 shows "Waiting for the base branch to go green", with the red `checks/go` at `0123456789ab`.
    - Task 14 shows "Waiting for an approval GitHub requires", naming the code owners, the ruleset and the review link.
    - Both offer force merge.
  - `/ci` renders the job cache, active sandboxes and an empty runs table.
  - The Start-a-run form keeps Start run disabled until workflows are loaded from GitHub, which the demo cannot do, so no run was started. The same form's repo picker also lists every team's config repo (noted under Bug A).
- **Task lifecycle and bulk operations:**
  - Defer with an until date of `2026-10-10` from a Tokyo browser → `POST /api/tasks/18/defer {"deferredUntil":"2026-10-10T00:00:00.000Z"}`. That is the deployment's midnight (1b4c067ed verified).
  - Bulk:
    - With three proposed rows selected, the bar offers approve, submit, defer, undefer, close, reopen, bulk edit and capability attach/detach.
    - Bulk defer sends three `POST /defer`. Bulk undefer of four sends four `POST /undefer`. The rail counts update (proposed 3→6, deferred 4→gone) without a reload.
    - Bulk edit over six mixed-team tasks marks repo, team and every custom field "(mixed across the selection)".
  - API: blank title → 400; a `repo` that is not owner/name → 400; a base with `..` → 400; a PATCH of an unknown task → 404.
  - Filing for `other/thing` (not on the target list) leaves the task awaiting a reply with the gate's explanation.
- **New task form:**
  - The target team select offers every team.
  - The repo select follows the team's config repo (`grain/config-qa-x`, `grain/config-operator`).
  - The demo answers `reachable-repos` with `available: false`, so the per-team push list (8579cdd2f) can't be exercised here.
- **Settings resilience:**
  - Admin › Engine: `maxMergesPerRepo -5` and `mergeBatchSize -5` → 400 with the message, and the field reverts.
  - `ciRunHistory -5` and `ciStepOutputMb -5` are saved. That is deliberate: negative means "keep forever" (`pkg/ui/settings.go`, grain/task-269). The values were reset to 0 afterwards.
  - Overwriting every non-identity `localStorage` key with junk (`{not json`, `42`, `null`, `[]`, `{"a":1}`) and reloading `/`, the three boards, `/inbox` and a task broke nothing. Only `grain.username` is kept there.

## Bugs filed

Both were filed with `propose_task` against `candidate`. Neither has a fix on `main` (`9715b22e1`), and neither appears in today's other walkthrough reports.

| #   | Bug                                                                                                                                                                                                                                                                                                                                                                                                   |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A   | `POST /api/suite-runs` doesn't run the team config-repo check that `CreateTask`/`CreateSchedule` got in 9878e11b8. A default-team suite run against `grain/config-payments` is accepted, and an item whose template has no repo is filed approved and queued there (task 28). `POST /api/tasks` refuses the same pair, and the task can't even be retitled afterwards (`PATCH` → 400 `only team "payments" can submit to grain/config-payments`). The suite-run and CI start-run repo pickers list every team's config repo. |
| B   | Template names are not unique within a team. Creating `release-qualification` twice returns 201. The schedule editor's Template select then lists `release-qualification` three times with nothing to tell them apart, although `model.Template.Name` is documented as how a human picks a template out of a list.                                                                                     |

## Setup notes

- `npx playwright install chromium webkit` needed `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy.
- The demo serves the API without a session, but the UI covers every page with the sign-in dialog until someone signs in. A crawl that doesn't register a user first is crawling behind a modal.
- `pkill -f "<pattern>"` inside `run_command` also matches the wrapping shell and kills the command. Kill by PID instead.
- Several forms use native `confirm()`. Give Playwright a `page.on('dialog', d => d.accept())` handler, or the action is silently cancelled.
- The Team settings Models tab holds the deployment-wide Task branches section (template and Deployment ID), below Remote Executors.
