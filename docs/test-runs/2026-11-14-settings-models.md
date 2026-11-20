# Walkthrough: Settings → Models picker and on-demand discovery (2026-11-14 plan §4)

Task: [Task #5729](/tasks/5729), filed by [Task #5629](/tasks/5629) from
[`docs/test-plans/2026-11-14.md`](../test-plans/2026-11-14.md) §4 (PR #1489,
`6b560ee97`). No earlier report in `docs/test-runs/` covered this route or the
picker. `2026-11-14-user-system-settings.md` mentions Model connections with
discovery only in passing.

## Environment

| | |
|---|---|
| Commit walked | `39e33c00d` (`main` at the start of the run, which includes `6b560ee97`); fixes on top of it are listed under "Fixes" |
| Daemon | real `grain daemon -sandbox-port-base=0 -host-sandboxes`, `-max-workers 4`, poll 1 s, on 127.0.0.1:4280 behind `tests/iap/iapsim.go` (IAP proxy 4296) for the per-person roles; ghsim (from `2026-11-13-grain-health-exit-codes/`) as the forge |
| Stores | SQLite 3.40.1 (`data/store/grain.db`); PostgreSQL 15.20 (Debian 15.20-0+deb12u1, `postgres://grain@127.0.0.1/grain_walk`) |
| Stubbed provider (the only stub) | [`fakeprovider.go`](2026-11-14-settings-models/fakeprovider.go): Anthropic-compatible `GET /v1/models` served over TLS as `api.anthropic.com` (`/etc/hosts` → 127.0.0.1:443, and only the daemon trusts its CA through `SSL_CERT_FILE`). The daemon's own `claude.Catalog` code, unchanged, makes the call. A control port switches the answer: a model list, `500`, `hang`, or `slow` (a stale answer 15 s late) |
| Agent | the 2026-11-13 fake `agy` CLI (`-agy-path`) with an `mcp_sequence` action that holds its run until `$WORK_DIR/release` exists |
| Connections | `anthro` (claude, no models listed, so it discovers), `conn-a` [`walk-a`], `conn-b` [`walk-b`], concurrency 1, `conn-c` [`walk-c`]; all with their own stored key |
| People | `alice@example.com` (deployment owner), `admin2@` (`grain/operator`), `ops@` (declared set `walk/ops-no-conn` = `grain/operator` + `deny: [connections:write]`), `viewer@` (`grain/viewer`); external agent tokens `walk-agent` (scopes include `connections:write`) and `walk-agent-ro` (no `connections:write`) |
| Browser | Playwright 1.62.1, Chrome for Testing 151.0.7922.34 (headless), 1440×900 and 390×844 |
| Toolchain | Go 1.26.10, Node 20.19.0, Debian 12 sandbox, kernel 6.1.0-54 |

Harness: [`env.sh`](2026-11-14-settings-models/env.sh) (`STORE=sqlite|postgres`;
`init_env`, `start_sims`, `start_daemon`, `api`, `code_as`, `discover`),
[`route_checks.sh`](2026-11-14-settings-models/route_checks.sh) (items 1–2),
[`tick_race.sh`](2026-11-14-settings-models/tick_race.sh) (tick vs refresh),
[`walk_chain.mjs`](2026-11-14-settings-models/walk_chain.mjs) and
[`dispatch.sh`](2026-11-14-settings-models/dispatch.sh) (item 3),
[`ui_cred.mjs`](2026-11-14-settings-models/ui_cred.mjs) (item 4) and
[`ui_probe.mjs`](2026-11-14-settings-models/ui_probe.mjs) (Refresh per role).
Captured output is under [`captures/sqlite/`](2026-11-14-settings-models/captures/sqlite)
and [`captures/postgres/`](2026-11-14-settings-models/captures/postgres).

To reproduce: build (`make frontend && go build -o /home/debian/bin/grain ./cmd/grain`),
install `postgresql sqlite3 jq`, then:

```sh
go build -o ~/fakeprovider docs/test-runs/2026-11-14-settings-models/fakeprovider.go
echo "127.0.0.1 api.anthropic.com" | sudo tee -a /etc/hosts
(cd ~ && sudo ./fakeprovider -ca ~/fake-ca.pem &)
cd docs/test-runs/2026-11-14-settings-models
export STORE=postgres   # or sqlite
source env.sh && init_env && start_sims && start_daemon
bash tick_race.sh > $WORK_DIR/captures/tick_race.txt &   # waits for the first 10-minute tick
bash route_checks.sh
node walk_chain.mjs save && bash dispatch.sh && touch $WORK_DIR/release
node ui_cred.mjs; node ui_probe.mjs /tmp/ui
```

## Results

### 1. Real provider listing: PASS on SQLite and PostgreSQL

`POST /api/deployment/agent-backends/anthro/discover` as the owner. The output is
identical on both stores ([`route-postgres.txt`](2026-11-14-settings-models/captures/postgres/route-postgres.txt)):

| Step | Provider | Answer |
|---|---|---|
| first refresh | `[claude-walk-a]` | `asked:true`, discovered `[claude-walk-a]`, `refreshedAt` now |
| provider adds a model | `[claude-walk-a, claude-walk-new]` | the new model is listed at once (no lister cache sits on the Claude path), `refreshedAt` moves |
| provider 500 | `500` | `error: "Anthropic refused to list models (HTTP 500: fake provider is down) …"`. `discovered.models` **keeps** `[claude-walk-a, claude-walk-new]` and its `refreshedAt` |
| provider hangs | no answer | returns after 20 s (`catalogTimeout`) with `error: "could not reach Anthropic … context deadline exceeded"`; the list is kept again |
| recovered | `ok` | the error clears and `refreshedAt` moves |
| unknown connection `nope` | none | **404** `{"error":"no connection nope"}` |
| framework that cannot be asked | none | **400** `connection "legacy-cmd" runs framework "custom", whose provider cannot be asked …`. PUT refuses to store a `custom` connection ("removed in v3.2.0"), so the row was written into the store directly, as an old export would hold it, and removed afterwards |
| `asked=false` | none | Not reachable on a real daemon: `startUIServer` always wires the Claude, Codex and Antigravity listers. It is the `grain demo` path without `-model-catalog` (`demo.go` sets `ClaudeModels` only with that flag). `TestRefreshDiscoveredModelsRefusals` covers it, and I did not drive it live |

The fake provider's hit log shows exactly one request per refresh, each with the
stored key (`auth=true`). No other connection's provider was asked.

**The 10-minute `RunModelDiscovery` tick does not clobber a newer refresh** (PASS on both stores,
[`tick_race.txt` SQLite](2026-11-14-settings-models/captures/sqlite/tick_race.txt),
[PostgreSQL](2026-11-14-settings-models/captures/postgres/tick_race.txt)). Ten seconds before the
first tick, the provider was switched to answer `[a, stale]` 15 s late. The tick
asked at 14:21:27. Two seconds later the provider was switched to `[a, fresh]` and Refresh was
POSTed. The refresh blocked on `modelDiscoveryMu` until the tick had written its stale
answer (14:21:42.440, log line `refreshed the models 1 connection(s)`), then asked again
and wrote `[a, fresh]` at 14:21:42.440227. The stored list after both was
`[claude-walk-a, claude-walk-fresh]` with the refresh's `refreshedAt`. PostgreSQL showed the same sequence
(tick at 14:34:44, refresh written 14:34:59.477). The cost is that a click arriving
during a slow tick waits for that tick, up to its 30 s list timeout.

Observation (not a bug, recorded so it is not rediscovered): connections imported with
`grain connections import` while the daemon runs are not discovered until the next tick,
up to 10 minutes. The daemon's start-up pass had already run, and the CLI writes the store directly
without the save-time `kickModelDiscovery`. Refresh, or any save through the API, discovers
them at once. It was seen on both stores.

### 2. Authorization: route PASS; UI FAIL (fixed)

Through the IAP proxy (`X-Iapsim-Email`) and with bearer tokens, on both stores:

| Caller | `POST …/anthro/discover` |
|---|---|
| `viewer@` (`grain/viewer`) | **403** `… holds no scope connections:write, which POST … needs` |
| `ops@` (operator minus `connections:write`) | **403**, same message naming `walk/ops-no-conn` |
| `admin2@` (`grain/operator`) | 200 |
| agent token without `connections:write` | **403** `external:walk-agent-ro holds no api grant for scope connections:write` |
| agent token with `connections:write`, no reason | **403** `… is destructive (scope connections:write): give a reason` |
| same token with `X-Grain-Acting-Reason` | **200** |

With the external agent API off, both tokens get 403 `external agent API is disabled`.
The plan's "an agent API token cannot call it" holds for any token a person has not
deliberately minted with `connections:write`. That scope is dangerous and person-grantable,
so a token that holds it can call the route once it gives a reason, as with every other
`connections:write` route. That is the designed contract, not a bug.

**Bug 1 (UI, fixed):** the viewer and `ops@` were shown each discovering connection's
**Refresh** button, disabled, on `/next/settings/models`. The model picker already hid its
own "Refresh models" from them. Before:
[`refresh-visibility-before.txt`](2026-11-14-settings-models/captures/sqlite/refresh-visibility-before.txt)
(`ops@`/`viewer@`: `Refresh models of anthro (disabled)`). After:
[`refresh-visibility-after.txt`](2026-11-14-settings-models/captures/postgres/refresh-visibility-after.txt)
(`ops@`/`viewer@`: none; owner and `admin2@`: Refresh shown and enabled). Repro: sign in as a
`grain/viewer` and open `/next/settings/models` on a deployment with a connection that lists
no models.

### 3. Default/fallback order drives dispatch: PASS on SQLite and PostgreSQL

- In `/next/settings/models` as the owner, the default was `walk-a`. I added fallbacks `walk-b`
  then `walk-c` with `picker-add-fallback` and pressed Save.
  `GET /api/deployment` then showed `defaultModels: ["walk-a","walk-b","walk-c"]`
  ([`chain-sqlite.txt`](2026-11-14-settings-models/captures/sqlite/chain-sqlite.txt),
  [`chain-postgres.txt`](2026-11-14-settings-models/captures/postgres/chain-postgres.txt)).
- After a reload, `/next/settings/models` shows `walk-a`, `walk-b`, `walk-c`. Classic
  `/settings/models` shows the same chain, `walk-a`, `walk-b`, `walk-c` (selects in page order;
  Remove on `walk-b` and `walk-c`), and after `conn-a` was disabled it labels the default
  `walk-a (disabled connections only)`.
- I disabled `conn-a` (`PUT /api/deployment/agent-backends/conn-a` with `disabled: true`), filed
  task 2 pinned to `walk-b` and waited until it was running, which holds `conn-b`'s one slot. Then I
  filed task 3 (unpinned) and task 4 (pinned `walk-b`):
  - task 3 dispatched at once on **`walk-c`** (`attempts[0].model = "walk-c"`);
  - task 4 stayed `queued`, with `activity: "waiting: all connections serving walk-b are busy
    (1 connection at its concurrency limit (1 of 1 runs)) -- the task is pinned to walk-b; it
    does not fall back"`. The daemon log repeats `candidate 4 skipped: admission refused
    by model availability …`;
  - after `touch release`, all three closed and task 4 ran on `walk-b`
  ([`dispatch-sqlite.txt`](2026-11-14-settings-models/captures/sqlite/dispatch-sqlite.txt),
  [`dispatch-postgres.txt`](2026-11-14-settings-models/captures/postgres/dispatch-postgres.txt)).

Observation (P3, not fixed): task 4's structured `reason` still reads `queued/ready`, with
resource "a worker slot (max_workers) and a sandbox" and the Run now control, while only 2 of 4
workers are busy. The precise wait is only in `activity`. A surface that shows `reason` would
blame the wrong resource. I filed it as a follow-up (see below).

### 4. Credential clear and autofill isolation: FAIL (fixed) / PASS

**Bug 2 (fixed, both UIs):** "Clear stored secret" on a model connection
(`/next/settings/models`, Credential → Clear stored secret) deleted the connection's
stored secret on **one click, with no confirmation**. Removing the connection does ask first.
Classic's connection editor "Clear credential" had the same defect. Live repro on the base
build ([`cred-before.txt`](2026-11-14-settings-models/captures/sqlite/cred-before.txt)):

```
before: confirm dialog shown=false; secret agent-backend-conn-c stored=true
after clicking Clear stored secret: confirm dialog shown=false; secret agent-backend-conn-c stored=false
```

After the fix ([`cred-after.txt`](2026-11-14-settings-models/captures/postgres/cred-after.txt)):
a click opens "Clear the credential for conn-c?", Cancel leaves the secret stored, and only
Accept deletes it.

What the plan calls the `/api/settings` key-set flags (`geminiApiKeySet`,
`claudeOAuthTokenSet`, `openaiApiKeySet`, `codexAuthJSONSet`) describe the deployment-wide
framework keys only. They stayed `false` throughout, because the daemon was given the Gemini key as a
file and the per-connection keys are not deployment-wide. A connection's own secret is visible only
as `agent-backend-<id>` in `GET /api/secrets`, so that is what the check reads. ui-ng
has no UI for the deployment-wide keys any more (the framework keys card is retired), so
there is nothing on `/next/` that clears them.

**Autofill isolation: PASS (structural).** On `/next/settings/models`, `/connections` and
`/secrets`, both search fields (`view-filter`, `settings-filter-input`) have
`type=search`, `autocomplete=off` and a distinct `name` (`grain-view-filter`,
`grain-settings-filter`). Each sits in its own `<form role="search" autocomplete="off">`
with no password input in it. The credential box is `type=password autocomplete=new-password`
in a separate form. After typing a secret into the credential box, both search fields are still
empty. **Not proved:** a real Chromium password-manager fill. Headless Chrome for Testing
has no password store or saved logins to offer, so a saved-credential autofill cannot be
staged in this sandbox. A headed Chrome with a saved login for the origin is still
needed to close this item.

**Bug 3 (fixed):** `/next/settings/connections`, the path the task, the plan and classic's
old connections tab use, is no ui-ng Settings section. On a desktop it fell back to the
default section (Models) by accident. At 390 px the page treated it as "no section chosen" and
showed only the section list, with the stage `display: none`. It is now an alias of Models,
as `docs/connections-and-models.md` says old connections links behave. Repro: open
`/next/settings/connections` at 390×844.

**Bug 4 (fixed):** a connection whose provider has never answered, such as all three `agy`
connections here (`agy models printed no model names`) or a Claude connection without a token, is
stored with `discovered: {refreshedAt: "0001-01-01T00:00:00Z", error: …}`. ui-ng rendered
"0 models discovered · **Jan 1, 00:00 UTC**", a refresh that never happened. The
classic `ConnectionModelsField` already skipped a `0001` time. Seen live on
`/next/settings/models`; the time is now left out, and the error is still shown.

### 5. Screenshots

`make ui-screenshots`-equivalent (`ui/e2e/screenshot.mjs`) over `grain demo`, at 1440 and
390 px, light and dark, of `/next/settings/models` and `/next/settings/connections`, before (base
build) and after. Also `--access viewer` before and after, and the clear-credential confirm
dialog after (desktop and phone). The contact sheets are attached to the task's closing note.
**I could not look at the PNGs myself.** This sandbox's file reader shows a PNG as bytes, and
`LOOK=1` needs a Gemini key the sandbox does not have. The layout claims rest on DOM and axe
assertions instead: `next-settings-models.spec.js` renders the Models card at all four
sizes, WCAG 2.1 AA clean, and the new checks assert the controls by role and test id. Classic
pages were not shot: the classic change is a native `window.confirm`, which screenshots do not
capture.

## Fixes

| Commit | Change | Regressions |
|---|---|---|
| `0c6d4e625` | ui-ng: confirm dialog before clearing a connection's secret; Refresh hidden without `POST …/discover` | `settings-models.component.spec.ts` "offers no Refresh …", `settings-page.component.spec.ts` (Cancel deletes nothing, Accept does); e2e "offers Refresh models only to a person who may ask providers" |
| `b457b78a8` | ui-ng: `connections` → `models` section alias | unit "opens Models, not the phone section list …"; e2e "opens Models for an old settings/connections link on a phone"; e2e "clears a connection's stored secret only after confirming" |
| `e7ab5411e` | ui-ng: no refresh time for a zero `refreshedAt` | unit "gives no refresh time …"; e2e "shows no refresh time for a provider that never answered" |
| `27b9c984c` | classic: `window.confirm` before "Clear credential" | `SettingsPage.test.jsx` decline/accept |

Each new unit test was run against the unfixed component and failed there. No Go code changed:
every defect found was in the UIs, and the route and dispatch behaved as designed on both
stores. Locally: ui-ng `src/app/settings` vitest 31/31, classic `SettingsPage.test.jsx`
44/44, `next-settings-models.spec.js` 10/10, `next-settings-repos.spec.js` 2/2,
`next-full-system.spec.js` 5/5, eslint clean, and `make fmt-frontend` clean.

The design book's three-reviewer mandate (Design, Functional, Adversarial) was not carried
out in this run. The author may not review their own change, and this run had no independent
reviewer to hand the diff to. It is left to the PR's review.

## Follow-ups

- The structured `reason` of a pinned task waiting at a connection's concurrency limit says
  `queued/ready … a worker slot (max_workers)`, while `activity` gives the real wait (filed
  from this task).
- A real-Chrome password-manager check of the Settings search fields, which needs a headed browser with a
  saved login. It is not filed separately: it is the one unproved part of item 4 above.
