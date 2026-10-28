# Daily Exploratory UI Testing

**Date:** 2026-10-06
**Branch:** `grain/task-2002`
**Commit tested:** `bc6581f44` (`Merge pull request #3063 from bwsalmon/grain/task-2314`), the tip of `candidate`. The footer read `v1.8.0-rc.1.0.20261006125934-bc6581f44992`. That is 259 non-merge commits past the 2026-10-04 run (`c4944f2b8`).
**Environment:** Debian GNU/Linux 12, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 (from `ui/node_modules`), Chromium headless shell and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -merge-queue-states -sensitive-hold -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build`, over the throwaway embedded SQLite store.
**Model and tooling:** no model was called. Every step was a Playwright script.

## How it was driven

- **Scripts:** Node scripts using Playwright's `chromium`/`webkit` API, kept out of the tree.
- **Sign-in:** `operator` was registered once with `POST /api/auth/register`. Each browser context then signed in with `POST /api/auth/login` and set `grain.username` in an init script, as `e2e/authentication-settings.spec.js` does.
- **What every page recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`;
  - every mutating request and its body.
- **Layout checks:**
  - horizontal overflow of the document, with the widest offenders not inside a scroll container;
  - a near-empty body;
  - crash or error-boundary text;
  - whether the sign-in dialog was still covering the page.
- **Confirms:** native `confirm()` prompts were accepted with a `dialog` handler.

### Stubs

Grain's own APIs and React were not mocked. The resilience checks used Playwright's `page.route` to stand in for a failing server or network:

- `PUT /api/teams/payments` → 500 `{"error":"internal server error"}`, to simulate a server error during a bulk member add.
- `PUT /api/teams/payments` aborted with `internetdisconnected`, to simulate network loss during a bulk member add.

## Coverage

### Page-load crawl

- **Routes:** 112 in all:
  - every rail view;
  - every seeded task, schedule, template, suite, board and team (plus `/teams/<t>/schedules`);
  - all 19 Admin tab ids;
  - all 8 Team-settings tabs for every team;
  - the repo page;
  - unknown IDs and malformed queries (`/tasks/99999`, `/tasks/abc`, `/tasks/-1`, `/ci/999`, `/ci/runs/abc`, `/schedules/nope`, `/admin/bogus`, `/teams/nosuchteam`, `/teams/default/settings/bogus`, `/repos/no/such`, `/boards/nope`, `?state=%%`, `?sort=bogus&q=<script>…`, `?f=state:zz:qq`, and a truncated percent escape).
- **Engines:** Chromium at 1920x1080, 1440x900, 768x1024 and 375x812. WebKit at 375x812 and 768x1024.
- **Results:**
  - No page errors, document overflow, blank screens, error-boundary text or leftover sign-in dialogs in either engine.
  - The expected 404s for unknown task and CI-run ids appeared.
  - `GET /api/repos/acme/widgets/github-branches` returned 404 on task, schedule and template pages, which the demo cannot serve.
  - `/ci/runs/abc` requested `GET /api/ci/runs/runs`, which returned 404 and showed the not-found state. `/ci/<id>` is the canonical form, so this was not filed.
  - **New:** every visit to Admin › Access returns `GET /api/relay-settings` → 501, and the page shows an error toast. This is **Bug 1**.

### Re-checks of the 2026-10-04 bugs

- **A. Suite run into another team's config repo:** fixed (13cef3a26). `POST /api/suite-runs` with team `default` and repo `grain/config-payments` → 400 `only team "payments" can submit to grain/config-payments`.
- **B. Duplicate template names:** fixed in the UI (a620f1e51). Duplicates are still legal at the API (201), on purpose. The schedule editor's Template picker now lists `release-qualification — dup (template-3)` and `release-qualification — release qualification: … (template-1)`, while the unique `security-audit` keeps its bare name.

### Journeys

- **Bulk-add team members** (new, 5fb884a53). The paste was `Alice Smith <alice@example.com>, bob, Carol <carol@example.com>; bad@@x, ALICE@example.com`, a role line `Member`, and `user:Erin@Example.com`, entered in Team settings › Members › Add several at once.
  - The preview read "3 new, 0 already on the team, 1 invalid (1 duplicate merged)". `bad@@x` was flagged invalid. The names, `bob` and `Member` were listed as left out.
  - Add 3 people sent one `PUT /api/teams/payments {"addMembers":[…]}`.
  - Adding `erin@example.com` again through the API did not duplicate `Erin@Example.com`, because the server compares identities ignoring case (`model.SameIdentity`).
  - With a 500, and with the network down, the pasted text and the Add button stay, and the error is shown. After a later successful save, the earlier error snackbar stays for a few seconds beside "Added 2 people as Member." This is the same transient-snackbar pattern the 2026-10-04 run noted, and was not filed.
  - At 375 px, a 90-character address previews without overflow.
- **Model pickers** (new, ef877ca0b / 2781a4e9a):
  - New task's Model picker offers Team default, `gemini-2.5-pro` and `gemini-2.5-flash`.
  - Typing `op` narrows the list to nothing, which is right for a fuzzy in-order match, and `zzzz` shows "No options". Escape followed by Tab leaves the value on Team default.
  - With `gemini-2.5-flash` picked, switching the target team to `operator` or `payments` hides the picker, since those teams have no models. Switching back to `admin (default)` shows it reset to Team default (92c72c26a verified).
- **Task pane and Tree board:**
  - Opened `/?state=queued`, then task 2 in the pane. Back returns to `/?state=queued`, Forward to `/tasks/2`, and a reload keeps the task, at both 1440 and 375.
  - The Tree board (`/boards/board-4`) lists every task under its filer. Clicking a row opens `/tasks/16` over the board, and Back returns to the board.
- **Merge queue and CI:**
  - The seeded tasks 13 and 14 show "Waiting for the base branch to go green" and "Waiting for an approval GitHub requires", each with Force merge.
  - Run now on task 13 asked for confirmation, then posted `run-now` and moved the task from completed to queued, sending it back over its pull request. That is `Client.RunNow`'s documented behaviour, so it was not filed.
  - `/ci` renders the job cache, active sandboxes and the empty runs table, with the grain-ci explanation.
- **Schedules:**
  - `timeOfDay` `-1:00` and `24:00` → 400, and a fractional `everyNHours` → 400.
  - Monthly on the 31st and in `Pacific/Apia`, and daily in `Etc/GMT+12`, all compute a sensible next run.
  - `everyNHours` 1,000,000,000 and 9,223,372,036,854,775,807 are accepted, and their next runs are 2118 and one hour away. This is **Bug 2**.
- **Suites:**
  - The editor's run modes are "run until no repo changes or new tasks" and "run a fixed number of times".
  - A count of 0 or -3 is held back by the field's native `min=1` validation, with no request. The API refuses both with 400 `count must be at least 1`.
  - A count of 1,000,000 is saved. Nothing caps it, but each pass waits on the previous one, so this was not filed.
  - The composition preview (`POST /api/suites/composition`) follows each edit.
- **Executors:**
  - Token creation validation works: an empty name → 400, and team `ghost` → 400 `team "ghost" does not exist`.
  - `GET /api/executors/bin/..%2f..%2fetc%2fpasswd` → 400 `invalid binary name`.
  - An HTML-looking name is stored and rendered as text.
  - A 5000-character name is accepted. In both executor lists, its unbroken title pushes the status and team chips about 42,000 px to the right. This is **Bug 3** (low priority). A realistic 66-character hostname fits.
  - DELETE on a token revokes it and leaves it listed as Revoked, which is how the routes are defined.

## Bugs filed

All three were filed with `propose_task` against `candidate`. None has a fix on `main`, and none appears in an earlier `docs/test-runs` report.

1. **Admin › Access Relay section raises an error toast on every visit to a daemon with no relay client.** `GET /api/relay-settings` → 501 `this UI does not manage a relay client`, which only daemons started with a relay URL or an existing `relay-settings.json` avoid. `RelayPanel` passes it to `showError` and renders nothing, so the toast is the whole section.
2. **`everyNHours` overflows `time.Duration`.** `Recurrence.Validate` only refuses values ≤ 0. `Recurrence.Next` multiplies by `time.Hour`, which wraps for values above about 2.56M hours. The `every <= 0 → time.Hour` fallback then turns some of those into an hourly schedule. The editor's field has no `max`.
3. **Executor token names are unbounded, and an unbroken one overflows the token card.** The fix is wrapping or truncating the title in `ExecutorsPanel.jsx` and adding a length cap at `POST /api/executors/tokens`.

## Setup notes

- `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium webkit` worked behind the package-cache proxy.
- Signing in through the API (`/api/auth/register` once, then `/api/auth/login` per context) is quicker than driving the sign-in dialog. It still needs `grain.username` in `localStorage`.
- Native form validation (`min`, `required`) blocks a submit without any `role=alert`. A check that looks only for alerts reads it as a silent no-op. Check `input.validity` or the absence of the request instead.
