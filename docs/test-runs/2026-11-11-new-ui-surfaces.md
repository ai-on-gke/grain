# §13 walkthrough: new UI surfaces (Angular `/next/`, hub-named grains, hub entry auto-refresh)

**Plan:** [`docs/test-plans/2026-11-11.md` §13](../test-plans/2026-11-11.md), "What is NOT proved" items 1–3.
**Task:** [Task #4526](/tasks/4526), filed by [Task #4297](/tasks/4297).
**Scope:** PRs #1164 (Angular 19 app in `ui-ng/`, served under `/next/`), #1150 (hub names a grain after its entry, schema 137) and #1148 (hub entry page auto-refresh).
**Helpers & transcripts:** [`2026-11-11-new-ui-surfaces/`](2026-11-11-new-ui-surfaces/).

## Environment

- Sandbox: Debian 12, Go 1.26.10, Node 20.19, PostgreSQL 15 (`postgresql`, `postgresql-client` from apt), Playwright Chromium (`/var/cache/playwright`). Base `main` @ `710b7fd5`.
- Built from `HEAD`: `make frontend` (builds `ui/` into `pkg/ui/static` and `ui-ng/` into `pkg/ui/static-next`), `ui-hub` (`npm ci && npm run build`), `go build ./cmd/grain` and `go build ./cmd/grain-hub`. The IAP stub is `docs/test-runs/2026-11-07-grain-registry/iapstub.go`, built unchanged.
- **Item 1:** `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:4290`. The demo seeds no access request, so one was put on task 7 directly in the demo's SQLite store (`task_observation.pending_access_request` plus `pending_question_comment_id`). No other fixture was used.
- **Items 2–3:** [`hub-naming.sh`](2026-11-11-new-ui-surfaces/hub-naming.sh) runs a real `grain-hub serve` with a PostgreSQL store, `tunnel.transport: ws-mux` and `catalog.heartbeat_interval: 2s`, behind the IAP stub. Next to it run a real connected `grain daemon -sandbox-port-base=0` (`-hub-url/-hub-entry/-hub-token-file`, token minted by `grain hub register -standalone -mint-token`) and a second, hosted-style daemon started with `GRAIN_DEFAULT_ENVIRONMENT_NAME`.
- **Stubbed:** only cloud VM creation (`compute.provider: none`, so the hub provisions no VM) and Google IAP (the stub re-signs a test identity as an IAP assertion). The hub, its PostgreSQL store, the daemons, the CLI and the browser are all real.
- Browser checks used Playwright at 1440×900 and 390×844 (`isMobile`), in light and dark. Screenshots: `node ui/e2e/screenshot.mjs --url … --code … --pages /next/,/inbox` (the `make ui-screenshots PAGES=/next/,/inbox` script against the running demo). The hub entry page was shot with [`hub-refresh-walk.mjs`](2026-11-11-new-ui-surfaces/hub-refresh-walk.mjs) `SHOTS=…`.
- The agent's file reader shows a PNG as bytes, and `LOOK=1` has no Gemini key in a sandbox. Layout was therefore checked with DOM assertions instead: no element past the viewport's right edge, `scrollWidth == clientWidth`, and the theme's computed background (`rgb(255,255,255)` light, `rgb(19,19,20)` `/next/` dark, `rgb(11,15,25)` hub dark). The contact sheets are attached to the PR for a human look.

## Summary of defects found

| # | Defect | Severity | Fix & regression |
|---|---|---|---|
| 1 | **A freshly provisioned hosted instance did not take its Grain Hub name until it restarted.** On the first start `run()` adopts `GRAIN_DEFAULT_ENVIRONMENT_NAME` into an empty store, which writes a `grain_config` row holding only `environment_name`/`hub_environment_name`. A few lines later `LoadConfig` treats that row as "connections only" (`model.Config.ConnectionsOnly`) and replaces it with the flag seed, which drops the name. The daemon still logs `environment name is now "…"`. Settings showed `""`. On the second start the name applied. Repro: `hub-naming.sh` step d on base, `FAIL (now: "")` ([before](2026-11-11-new-ui-surfaces/transcripts/hub-naming-before-fix.txt)). | **Medium**: hosted instances are the main case this feature is for, and they are created once and seldom restarted. | The seed now carries a hub-owned name over from the row it replaces (`model.Config.WithHubEnvironmentNameOf`, called in `pkg/daemon.LoadConfig`). An operator-set name is still never overridden, and later hub renames still apply. `TestDefaultEnvironmentNameSurvivesTheFirstStartsSeed` (`pkg/daemon`) fails on base and passes now. Step d now passes on the first start ([after](2026-11-11-new-ui-surfaces/transcripts/hub-naming-after-fix.txt)). |
| 2 | **Hub entry page: a poll for one entry that answered after the page had moved to another entry was merged into the new entry's page and form.** `EntryDetailView` is not remounted when a scope-conflict link (or Back) changes `entryName`. `refreshEntry`'s guard `requested === entryName` compared the name with itself through the callback's own closure, so it never fired. A Save could then write entry A's values to entry B. The hanging poll also held `refreshInFlight`, which stopped B's polling until A answered. `loadEntry` had the same race. | **Medium**: rare timing, but it can write wrong data. | The page now reads the shown entry from a ref (`shownEntryRef`), and the in-flight guard is per entry. Vitest "drops a poll that answers for the entry the page has navigated away from" (`ui-hub/src/App.test.jsx`) holds A's poll with a deferred promise, navigates and then releases it. It fails on base (`Received: Entry A`). |
| 3 | **Hub entry page: an edit the server had caught up with stayed dirty forever.** If somebody saved the same value the user had typed, the field kept showing "Updated on the server to "…" while you were editing; saving will overwrite it" and "Discard my edits", over a save that would change nothing. Later server changes to that field were never written into it. Seen live: `edit equal to the server's new value: hint 1, Discard 1` ([walk, before](#item-3--hub-entry-auto-refresh)). | Low (misleading copy, field stuck) | Such a field now syncs like an untouched one. Vitest "treats an edit the server has caught up with as synced, with no overwrite hint" fails on base. Live walk after the fix: `hint 0, Discard 0`. |

Observed but not changed (by design, or out of §13's scope):

- `/next/` serves `index.html` (200 `text/html`) for any missing path under it, including asset-like ones (`/next/missing.js`, `/next/favicon.ico`). This is the shared `spaHandler` the React UI also uses (`/missing.js` behaves the same), so it is not specific to `/next/`.
- `/next/` has only the Inbox route. `/next/tasks/1` and `/next/nowhere/deep` fall back to `/next/inbox`, by its route table's `**` redirect. Neither UI reads `?filter=` from the URL, so `/next/inbox?filter=proposals` opens on "All", the same as React.
- React calls the alerts group "Notifications" and `/next/` calls it "Alerts". This is a label difference only; the counts match.

---

## Item 1: `/next/` against `/inbox` over `grain demo`

Helper: [`next-walk.mjs`](2026-11-11-new-ui-surfaces/next-walk.mjs) (steps `counts gate deeplinks layout viewer act live`). Transcript of the read-only steps: [`transcripts/next-walk.txt`](2026-11-11-new-ui-surfaces/transcripts/next-walk.txt).

| Check | Result |
|---|---|
| Counts per class, same instant | ✅ Equal in every class. First pass: React `pending-releases 1, awaiting_reply 2, awaiting_submit 7, proposed 2, notifications 2` = 14; `/next/` `release 1, reply 2, submit 7, proposal 2, notification 2` = 14 (filters `all 14, asked 2, approvals 1, merges 7, proposals 2, alerts 2`). After the seeded access request: 13 = 13, with access in `/next/`'s "Asked: grant access" and React's asked group. After the actions below: 12 = 12. |
| Answer a question from `/next/` | ✅ Reply on #4: `POST /api/tasks/4/comments` 200. The reply group went 2 → 1 rows with no error banner. |
| Approve an access request from `/next/` | ✅ The dock lists each item with a checkbox ("Grant all" / "Grant n of m"). An item that cannot be granted (a repo off the target list) arrives ticked off and disabled, with the server's reason. A grantable `network: open` request: `POST /api/tasks/7/access-request/grant` 200, and the task became `queued` with `networkAccess: open`. A grant the server refuses (base branch `release/v1` does not exist) showed the server's 400 message in the action-error banner. |
| Approve a proposal from `/next/` | ✅ #1: `POST /api/tasks/1/approve` 200. Proposals went 3 → 2 rows. |
| Live updates over `/api/events` | ✅ One `/api/events` stream was opened. A proposal filed out of band (`POST /api/tasks`, unapproved) appeared in `/next/` without a reload, 519 ms later. |
| Deep links & SPA fallback | ✅ `/next` → 307 `/next/`. `/next/`, `/next/inbox`, `/next/inbox?filter=…`, `/next/nowhere/deep` and `/next/tasks/1` → 200, and the app lands on `/next/inbox`. `/next/index.html` → 301. |
| Limited role (viewer access laid over `/api/config`) | ✅ For each of 14 rows the dock has no enabled buttons and shows the RBAC note. No bulk buttons. |
| Access-code gate | ✅ Without a code, `/next/`, `/next/inbox` and `/next/main.js` → 401 with the "enter access code" page, the same as `/` and `/inbox`. The form carries `next=/next/inbox?x=1`, and signing in lands back on `/next/inbox?x=1` with the Inbox shown. |
| Security headers | ✅ Same as React: CSP `default-src 'self'; script-src 'self'; …`, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: same-origin`. |
| CSRF (`pkg/ui/csrf.go`: Sec-Fetch-Site/Origin; no token header exists) | ✅ A POST with the session cookie: `Sec-Fetch-Site: cross-site` → 403, foreign `Origin` → 403, same-origin → passes the check (404 from the route itself). |
| Layout, 1440×900 & 390×844, light & dark | ✅ No horizontal overflow (`scrollWidth == clientWidth`), no element past the right edge, and each theme applied. Screenshots of `/next/` and `/inbox` at both widths and themes are in the before contact sheet. |

## Item 2: Hub naming (real `grain-hub`, connected daemon)

[`hub-naming.sh`](2026-11-11-new-ui-surfaces/hub-naming.sh), transcripts [before](2026-11-11-new-ui-surfaces/transcripts/hub-naming-before-fix.txt) / [after](2026-11-11-new-ui-surfaces/transcripts/hub-naming-after-fix.txt) the fix.

| Step | Result |
|---|---|
| a. Register connected `name-walk` (display name "Name Walk") and start the daemon | ✅ The environment name is "Name Walk" on the first heartbeat, already set when the UI first answers. |
| b. `PATCH /api/entries/name-walk {"display_name":"Renamed Walk"}` | ✅ The grain is renamed on the next heartbeat (1–3 s). |
| c. Operator sets "Operator Name" (`PUT /api/settings`), then the hub renames the entry again | ✅ After 70 s the name is still "Operator Name", while the hub entry says "Hub Rename 3". |
| d. Hosted-style daemon, `GRAIN_DEFAULT_ENVIRONMENT_NAME="Hosted Default"`, first start | ❌ → ✅ Base: Settings `""` although the log says adopted (defect 1). After the fix: "Hosted Default". |
| d. Restart with a new default "Hosted Renamed" | ✅ Renamed. |
| d. Operator sets "Hosted Operator", restart with "Hosted Third" | ✅ "Hosted Operator" kept. |

## Item 3: Hub entry auto-refresh

[`hub-refresh-walk.mjs`](2026-11-11-new-ui-surfaces/hub-refresh-walk.mjs) runs against the real hub from item 2, through an IAP stub front asserting alice. It counts the page's own `GET /api/entries/name-walk` calls. Transcript after the fix: [`transcripts/hub-refresh-walk-after.txt`](2026-11-11-new-ui-surfaces/transcripts/hub-refresh-walk-after.txt).

| Check | Result |
|---|---|
| Settled (`running`, `health: ok`) | ✅ Bar: "refreshing every 20s". 2 polls in 45 s, 20 s apart. |
| Transitional | ✅ The connected entry cannot be driven into a transitional state without a VM, so `state: "replacing"` was laid over the real answer with `page.route`. Bar: "refreshing every 4s while replacing", with polls 4 s apart. Removing the fixture went back to 20 s. |
| Hidden tab | ✅ Headless Chromium cannot background a tab, so `document.hidden` was overridden and `visibilitychange` dispatched. 0 polls in 45 s hidden, and 1 poll within 1.5 s of being shown again. |
| Edit preservation | ✅ With "My local edit" typed, the hub was `PATCH`ed (display name and purpose). The input kept the edit, the hint read "Updated on the server to "Renamed on the server" while you were editing; saving will overwrite it.", and the untouched Purpose took the server's value. "Discard my edits" restored the server value and cleared the hint. ❌ → ✅ An edit equal to the server's new value: base `hint 1, Discard 1`, fixed `hint 0, Discard 0` (defect 3). |
| Failed poll | ✅ A 503 on one poll adds "· last refresh failed, showing earlier data" to the bar only. No alert banner, and the entry stays on screen. The next good poll clears the marker. |
| Layout, 1440×900 & 390×844, light & dark | ✅ No horizontal overflow, and the refresh bar spans the content column (60–1380 px desktop, 12–378 px phone). Before and after screenshots are in the hub contact sheet. |
| Stale poll after in-place navigation | Not reachable with one connected entry and no scope conflict in a live walk. Reproduced deterministically in Vitest (defect 2). |

## Outcomes

- Fixed in this branch: defects 1–3, each with a regression test that fails on base: `pkg/daemon` `TestDefaultEnvironmentNameSurvivesTheFirstStartsSeed`, plus two `ui-hub` Vitest cases.
- `ui/` and `ui-ng/` are unchanged. `ui-hub/src/App.jsx` changes behaviour only, with no markup or CSS changes. `make fmt-frontend` (prettier) is clean.
- Nothing was filed as a follow-up.
