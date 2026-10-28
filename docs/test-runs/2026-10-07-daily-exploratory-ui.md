# Daily Exploratory UI Testing

**Date:** 2026-10-07
**Branch:** `grain/task-2429`
**Commit tested:** `8f05562f4` (`Merge pull request #3132 from bwsalmon/grain/task-2417`), the tip of `candidate` when the run started. That is 29 non-merge commits past the 10-06 evening run's `fc7ba6f99` (`2026-10-06-daily-exploratory-ui-evening.md`). `main` was at `18bba89aa`.
**Environment:** Debian GNU/Linux 12, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 (from `ui/node_modules`), Chrome for Testing 151 and WebKit 26.5.
**Server under test:** this tree's own `grain demo -observability -merge-queue-states -sensitive-hold -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build`, over the throwaway embedded SQLite store.
**Model and tooling:** no model was called. Every step was a Playwright script or a curl call. The scripts were kept out of the tree.

## Focus

Five other walkthroughs had already run against `candidate` today: intake/inbox, saved boards, repository/release/memory, recurring/composed work, and responsive navigation. So this run spent its time on the areas the task names that those runs touch least:

- standing-work editors (schedules, templates, suites) and their validation;
- Team settings › Models, and the New team dialog;
- executors;
- the CI board, which runs live under `-observability`;
- filter and URL sync;
- bulk actions;
- client resilience.

### Personas

- `operator`: deployment admin.
- `zed`: registered, on no team.
- `carol`: admin of `payments` only, added through `PUT /api/teams/payments`. Not a deployment admin.

### How it was driven

- **Sign-in:** the same as the 10-06 runs. `POST /api/auth/register` once, then `POST /api/auth/login` per context, and `grain.username` in `localStorage`.
- **What every page recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`;
  - every mutating request with its body;
  - confirm dialogs, with their text.
- **Layout checks:** document overflow, with the offending leaf elements that are not inside a scroll container; dialog scrollers; a near-empty body; and error-boundary text.

### Stubs

- Grain's own APIs and React were not mocked in any functional step.
- The resilience step alone used `page.route` to answer `GET /api/tasks` with a 500 and `GET /api/config` with an HTML page. It also used `context.setOffline(true)`. Each was removed before the next step.
- The CI run was paced with the observability demo's own `POST /api/e2e/ci/finish`.

## Coverage

### Route crawl

- **Routes:** 64 in all:
  - every rail view;
  - tasks 1, 3, 4, 6, 9, 10 and 16;
  - a schedule, a template, a suite and suite runs;
  - all four boards;
  - the repo page and its releases;
  - `/teams/default`, `/teams/payments` and all 8 Team-settings tabs;
  - `/settings`;
  - 15 Admin tabs (not Power);
  - 16 bad URLs: `/tasks/99999`, `/ci/999`, `sched-999`, `template-999`, `suite-999`, `board-999`, `/repos/no/such`, `/teams/nosuch`, `/teams/nosuch/settings/models`, `?state=%%`, `?f=state:zz:qq`, `?f=%E0%A4%A`, `/admin/nosuch`, an unknown team-settings tab, release 999 and `/sessions/999`.
- **Engine, viewport and user:**
  - Chromium as `operator` at 1920x1080 and 375x812;
  - WebKit as `operator` at 768x1024;
  - Chromium as `zed` at 1440x900.
- **Results:**
  - No page errors, document overflow, blank screens or error-boundary text.
  - The bad URLs fall back to their list, or to `/`.
  - Known and expected answers only:
    - the demo's `github-branches` 404;
    - `/api/relay-settings` 501, logged to the console with no banner, which is 10-06's fix;
    - 404s for missing ids;
    - `GET /api/state-repo` → 403 on every load by `zed`. That is the 10-06 evening run's **Bug 5**, already filed.

### Schedules

- **New schedule, every N hours:**
  - `0`, `-3` and `1.5` are stopped by the browser's own number validation.
  - `100000` was stored. The row reads "every 100000h · Next run 3/4/2038".
- **The API refuses bad recurrences:**
  - `everyNHours` 0 or −1 → 400 "must be positive";
  - `1.5` → 400;
  - `timeOfDay` `25:00` → 400;
  - weekday `funday` → 400;
  - `dayOfMonth` 0 → 400;
  - kind `bogus` → 400.
- **Pause, resume and run now:**
  - Each one works: `PATCH {"enabled":false}` / `{"enabled":true}`, and `POST …/run` after a confirm that names what will run.
  - Each one then **closes the editor**. With an unsaved edit in the form, the edit is thrown away without the "You have unsaved changes" prompt that Escape, "← schedules" and the browser's Back all show. A reopen shows the old title. This is **Bug 2**.
  - Browser Back after an in-app open of an edited schedule or template does ask. Cancel discards without asking, which is an explicit choice and was not filed.
  - There is no `beforeunload` guard, so a reload discards an edit too. The 2026-10-02 deployment-operations run recorded the same for settings. It is folded into Bug 2.

### Templates and suites

- **API validation:**
  - Template `name` blank → 400.
  - A duplicate name is allowed. Pickers tell same-named templates apart, per `c7fd13c43`.
  - Suite with no templates → 400.
  - `count` < 1 → 400, and `maxPasses` < 1 → 400.
  - Unknown mode → 400.
  - Unknown template → 400.
- **Template delete:** deleting a template used by a suite is refused with `template is used by 1 suite(s) ("Explore suite")`. That is today's `cc1caaa6c`.
- **Suite editor:**
  - "+ new template" opens the template form inside the suite flow.
  - Saving it returns to the suite with the new template selected. Name and max passes are kept.
- **Observation, not filed:** switching a new suite's target team from default to payments keeps the default-team template already picked. Saving creates a payments suite that chains a default-team template. `CreateSuite` only checks that the caller can read the template, so this looks allowed on purpose. Whether a suite's templates should follow its team is a product question.

### Task list: filters, URL and history

- **Narrowing URLs:**
  - `?state=…` works for queued, closed, deferred, blocked and awaiting_submit.
  - `?q=flaky`, `?sort=created:asc`, `?f=title:has:merge` and `?f=repo:any:…&f=capability:any:gcp-key` all narrowed correctly.
  - The filter button counts the conditions.
  - `?sort=bogus:sideways` is normalised to `bogus:asc` and left unsorted.
- **Rail → search → open → history:**
  - The walk: rail queued for merge → blocked → awaiting submit → queued → search "flaky" → open task 2.
  - Back ×3 and Forward ×2 each restored the right URL, rows and search box. A reload kept `?state=queued&q=flaky`.
  - Typing in search replaces the history entry rather than adding one.
- **Minor, not filed:**
  - An unknown attribute (`?f=priority:any:high`, where a custom field's id is namespaced) is kept in the URL and ignored.
  - `?f=created:within:abc` counts as one filter but filters nothing.

### Bulk actions

- Selecting tasks 12 and 16 (queued) offers approve, submit, defer, undefer, close, reopen, bulk edit…, a capability attach/detach, and clear selection. Attach and detach stay disabled until a capability is chosen.
- Approve on two already-approved tasks sent `POST /api/tasks/{12,16}/approve`. Both returned 200 as no-ops, and the selection cleared.
- `approve` on closed task 10 and completed task 5, and `reopen` and `undefer` on queued task 12, were also 200 no-ops through the API.

### Task pane and custom fields

- **Custom-field validation on `POST /api/tasks`:**
  - a non-number estimate, `2026-13-45` as a date, an enum value off its list, `maybe` as a boolean, and an undeclared field are each refused with a specific 400;
  - an estimate of `-3` days is stored.
- **Task 17:** Defer → `POST /api/tasks/17/defer {"deferredUntil":""}`, and the pane shows deferred with undefer.
- **WebKit at 375x812:**
  - The inbox lays out without overflow.
  - `/new`: title, description, then repo `acme/widgets` (required, starts at "—") → `POST /api/tasks` 201.

### Teams and per-team models

- **Team settings › Models, as `operator`:**
  - Default model → `PUT /api/teams/default {"defaultModel":"gemini-2.5-pro"}`. It survives a tab switch.
  - The same tab, headed "Showing settings for team: default", also holds max agent turns, prompt extension, task branches (template, deployment ID) and commit identity. Editing max agent turns sent **`PUT /api/settings {"maxAgentTurns":7}`**, which is deployment-wide. The 10-04 release-engineering and 10-05 teams runs recorded this placement, but nothing was filed.
- **The same tab as `carol`, a payments-only admin:**
  - All five deployment fields are shown enabled.
  - Every blur sends `PUT /api/settings` → **403** "user "carol" does not have administrator privileges", and a red toast. The box quietly reverts.
  - This is **Bug 1**.
- **New team** (Create options › new team):
  - The dialog's Admins helper says "you become one when you create it".
  - With Members and Admins both left blank, Save is refused **client-side** with "A team must always have at least one user with permission to access it". No request is sent.
  - The same body through the API (`POST /api/teams {"id":"qa-team","name":"QA"}`) → 201 with `operator` as admin.
  - Admin › All Teams' create path (`TeamsPanel`) has the identical check.
  - This is **Bug 3**. The 10-05 teams run typed a member, so it never hit this.

### Executors

- **Admin › Executors:**
  - Issue token: blank or whitespace-only names keep "generate token" disabled.
  - `ci-box-1` twice → two tokens. Duplicate names are allowed.
  - The one-time token dialog shows the `grain executor --token … --server <server-addr:port>` command.
  - Revoke works.
- **Long names:** a 200-character name with no break characters does not wrap. At 375 px the token list's card becomes a 1743-px sideways scroller, and the name runs past its Active, Team and Revoke row. A realistic hyphenated 50-character name (`gke_prod-project_us-central1_payments-pool_worker01`) wraps correctly at 375 and 768. This is **Bug 4**, minor.
- **Team settings › Executors** for `operator` lists the team's tokens. The demo's two connected executors belong to teams `infra` and `frontend`, which do not exist in this store, so no team view shows them. That is demo data, not filed.

### CI board

- **Start a run:** repo `acme/widgets`, branch `main`, then load workflows → `POST /api/ci/runs {"workflow":".grain/workflows/observability.yml"}` 202, which navigates to `/ci/<id>`.
- **The run page:**
  - It shows the running job, its sandbox, and the step "Wait for the browser".
  - The elapsed time ticks every poll.
  - A reload of the deep link restores it.
  - Active sandboxes shows (1).
- **Start again:** an identical start while the first run is in flight returns the same run id. That is the deduplication `wait_for_checks` describes.
- **Cancel run:** the confirm explains the effect and offers a reason. `DELETE /api/ci/runs/<id> {"reason":""}` → cancelled with completed time.
- **A rechecked run** finished with `/api/e2e/ci/finish`. Within one poll the page showed success, with the step output "observed through the sandbox", and the list read "1 jobs, all green".
- **Search runs** does not go into the URL, unlike the task list's `q`. Noted, not filed.

### Resilience

- **Offline for 12 s on `/tasks/2`:**
  - A banner reads "This deployment cannot be reached … retrying automatically". The pane stays drawn.
  - Back online, the banner clears on its own within 12 s.
- **`GET /api/tasks` → 500:**
  - The error text shows as a banner.
  - The list reads "No tasks in this state" and the rail "tasks 0", which is a misleading empty state while the read is failing. Noted, not filed.
  - After the route was removed, the next poll brought back all 14 rows without a reload.
- **`GET /api/config` → 200 HTML:** a full "grain could not load" screen names the proxy or sign-in page cause and offers Reload. No crash.

## Bugs filed

All four were proposed with `propose_task` against `candidate`. None appears in an earlier `docs/test-runs` report as filed. Each still reproduces in `main`'s source (`18bba89aa`).

1. **UI: Team settings › Models offers deployment-wide fields to team admins, who get a 403 for each one.** The same fields, edited by a deployment admin under a team heading, change every team.
2. **UI: the schedule editor's Pause, Resume and Run now drop unsaved edits without asking.** So does a reload.
3. **UI: New team refuses a team with no members typed in, although its helper says the creator becomes admin.**
4. **UI: an unbroken executor token name overflows the token list.** Minor.

## Setup notes

- **Browser download:** the Playwright browser download failed with `SELF_SIGNED_CERT_IN_CHAIN` through the package-cache proxy. `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium webkit` worked.
- **Case-insensitive selectors:** many buttons are lower-cased by CSS but labelled in title case, so `getByRole({name, exact})` misses them. Use `locator("button", { hasText: /^pause$/i })`.
- **New task repo:** the repo picker starts at "—", and Create task stays disabled until a repo is picked.
- **Schedule editor URL:** the New schedule form has no URL of its own and stays at `/schedules`.
- **Team-settings fields:** they autosave on blur. Moving focus to another field is enough to trigger the save.
