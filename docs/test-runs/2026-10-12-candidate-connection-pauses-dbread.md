# Walkthrough: per-connection pauses, db-read and `POST /api/repos` on `candidate`

Plan: `docs/test-plans/2026-10-12.md` §2. Run by task 3287 (filed by task 3209) on 2026-10-12
against `candidate` at `c8a3f5e0c`.

## The plan's premise no longer holds

The plan was written for a `candidate` that lacked `7283ae3f3` and still had "All teams", and it asked
for main's specs to be adapted to that tree. When this run started, `candidate` had been re-cut
(v1.9.0, `b0fd41c2a`) and already held:

- `7283ae3f3` (system admin on pause lifting) and main's walkthrough report `adfe427a6`;
- all three of main's specs, unchanged: `ui/e2e/connection-pause-teams-walkthrough.spec.js`
  (`e4805b252`), `cmd/grain/connection_pause_walkthrough_test.go` (`e822cf470`) and
  `cmd/grain/db_read_walkthrough_test.go` (`cdac8d8e5`);
- `/t/<team>` URLs, with "All teams" removed (grain/task-3088).

`git diff origin/candidate origin/main` is empty for `pkg/ui/pause.go`, `pkg/ui/repos*.go`,
`pkg/dbaccess`, `tests/security/routes_test.go` and the three specs. So nothing was left to adapt,
and "the adapted pause handler" no longer exists: `candidate` runs main's handler. Neither
`.grain/baselines/route-authorization.txt` nor `baselineRoutes` lists `DELETE /api/pause` or
`POST /api/repos`. Steps 1 and 6 were therefore "run main's specs as they are on `candidate`". The only
step no spec covered was step 5 in the browser, and this run adds a spec for it. The banner under
"All teams" (step 2) can't be checked because "All teams" no longer exists on `candidate`.

## Environment

- `candidate` `c8a3f5e0c`, Go 1.26.6, Node 20.19.0, Playwright 1.62.1 (Chrome for Testing 151).
- `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false` on its own embedded SQLite store. The e2e
  fixture starts one demo per worker (`ui/e2e/fixtures.js`). The curl matrix below used a binary from
  `go build ./cmd/grain`, readiness polled on `/api/sandboxes`.
- The UI was built with `cd ui && npm ci && npm run build`. Playwright drove the built UI that the
  demo serves.
- Non-admin roles were exercised with `extraHTTPHeaders: {}`, naming each user per request or
  signing them in. Admin mode was sent only where a step asks for it.
- Sandbox note: `npx playwright install chromium` failed with `SELF_SIGNED_CERT_IN_CHAIN` behind the
  package-cache proxy until `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  was set.

## Stubs

1. **`grain demo -agent-paused`** begins a real `orchestrator.Pause` on the default team's
   `gemini-pro` connection. It stands in for a model provider's usage-limit reply. This was the only
   stub in the browser spec and the curl matrix.
2. **A fake `codex` executable** in `TestWalkthroughAUsageLimitPausesOnlyTheConnectionWhoseKeyMetIt`
   answers one API key with a usage-limit error. It stands in for the provider CLI (from main's
   test, unchanged).

grain's APIs, React, teams, members, connections, tasks and the store are all real.

## Steps and results

### Step 1: main's specs on `candidate`, unchanged

| Spec | Result |
|---|---|
| `go test ./cmd/grain -run TestWalkthroughAUsageLimitPausesOnlyTheConnectionWhoseKeyMetIt` | PASS |
| `go test ./cmd/grain -run TestWalkthroughDBReadBypassesAreRefusedThroughTheToolAndHTTP` | PASS |
| `ui/e2e/connection-pause-teams-walkthrough.spec.js` | PASS (3 runs) |
| `ui/e2e/model-source-pause-walkthrough.spec.js` (shares the pause, own demo) | PASS |
| `ui/e2e/rbac-enforcement-sweep.spec.js` (includes `POST /api/repos`) | PASS |
| `ui/e2e/new_task_draft_repo_page.spec.js` | PASS |

### Step 2: two teams, team A's connection paused

The setup was the same as the spec's. Team A is `default`: ann is admin, amy is a member, and it has
a borrower `a-borrow` of `gemini-pro`'s key plus `a-own` with its own key. Team B is `beta`: bea is
admin, bob is a member, and it has its own `b-gem`. Live `GET /api/pause`:

| Viewer | `paused` | `pauses` | `canLift` |
|---|---|---|---|
| amy (A member) | true | `gemini-pro` (team `default`) | false |
| ann (A admin) | true | `gemini-pro` (team `default`) | true |
| bob (B member) | false | none | — |
| bea (B admin) | false | none | — |
| sysadmin, Admin mode off | true | `gemini-pro` (team `default`) | false |
| sysadmin, Admin mode on | true | `gemini-pro` (team `default`) | true |

The browser spec checks the same results in the UI. amy's banner names the connection and its team
with no "Resume now", and so does the system admin's with Admin mode off. bob and bea see no banner.
B's Gemini task isn't held and `b-gem` is Active. On A's Connections tab, `Gemini 2.5 Pro` and
`A borrower` show Paused and `A own key` shows Active. The Go walkthrough begins the pause from a real
run's usage-limit error and shows the other team's connection still dispatching.

"Under All teams" was not run because that selection no longer exists on `candidate`.

### Step 3: every lift, as every role

A fresh demo was used for each probe, so each one starts with `gemini-pro` paused.

| Caller | `DELETE /api/pause` (bare) | `DELETE /api/pause?connection=gemini-pro` |
|---|---|---|
| amy (A member) | 403 `user "amy" is not an admin of any team, so may not lift a usage-limit pause` | 403, same |
| bob (B member) | 403, same wording | 403 |
| bea (B admin) | 200 `lifted:false`, pause kept | 200 `lifted:false`, pause kept |
| sysadmin, Admin mode off | 403 `… : you are a system admin; turn on Admin mode to do this` | 403, same |
| ann (A admin) | 200 `lifted:true`, nothing left | 200 `lifted:true`, nothing left |
| sysadmin, Admin mode on | 200 `lifted:true`, nothing left | 200 `lifted:true`, nothing left |

The demo can pause only one connection, so it can't show live that a system admin's bare DELETE lifts
*every* pause while a team admin's bare DELETE lifts only their team's.
`pkg/ui/pause_test.go`'s `TestDeletePauseLiftsOnlyWhatTheCallerAdministers` covers that with two
teams' pauses: lead's bare DELETE leaves beta's pause, and root's bare DELETE clears both. It passes
on `candidate`.

Reproduction: `/tmp/lifts.sh` as run here registers the same teams over loopback, then for each
probe starts `grain demo -agent-paused` and issues one `curl -X DELETE` with `X-Grain-User` and,
where marked, `X-Grain-Admin-Mode: on`. The browser spec makes the same refusals and the banner lift
deterministic in CI.

### Step 4: db-read bypasses on SQLite

`TestWalkthroughDBReadBypassesAreRefusedThroughTheToolAndHTTP` sends each query through the real
`query_grain_db` MCP tool and `POST /api/tasks/{id}/db/query`. The task's queries are
`WITH x AS (SELECT 1) DELETE\tFROM tasks`, `SELECT 1; DELETE\nFROM tasks` and
`PRAGMA writable_schema=ON`, plus the same against grain's real table `task` and the
`UPDATE` / grant-`INSERT` / `query_only = OFF; COMMIT` variants. Every one was refused on both
paths, for example `mutating keyword "DELETE" is not permitted in read-only queries`,
`only one statement is permitted in a read-only query` and
`PRAGMA writable_schema is not permitted in read-only queries`. The `task` and `task_grant` rows
were unchanged afterwards.

### Step 5: `POST /api/repos` as a member and as the team admin

The new spec `ui/e2e/repos-add-team-admin-walkthrough.spec.js` signs rlead (admin) and rmember
(member) in to team `alpha`.

- API, member: `{repo, team:"alpha"}` returned 403 with
  `user "rmember" does not have permission to add repos to team "alpha"`. `{repo: 42, team:"alpha"}`
  also returned 403, so authorization comes before the body is decoded.
- Repos page (`/t/alpha/repos`), member: "Add repo" is offered. Submitting it sends the POST, which
  gets a 403. The page shows the server's sentence, no row appears, and the team's `targetRepos` is
  unchanged.
- Repos page, rlead: the same submit returns 200, the row appears, and `targetRepos` holds
  `acme/gadgets`.

## Findings

No product bugs were found. Every expectation in the plan held, with the route expectations as
written.

### Observations (no change made)

- **"Add repo" is offered to members who will be refused.** The sweep already records it as an
  ungated control (`rbac-enforcement-sweep.spec.js`, "Add repo: offered" for every role). The refusal
  is readable, so this is UX, not a hole. It belongs with the sweep's existing follow-up on gating
  those controls, so no new task was filed.
- **A malformed body that names no team gets a 400, not a 403, from a "member".** Registering
  creates a personal team the user administers, so a body naming no team passes the "admin of some
  team" pre-check (`requireAdminOfSomeTeam`), and it would be allowed on that personal team anyway.
  This is correct, but a test that sends a bare malformed body as a "member" will see 400. The new
  spec names the team in its malformed body for that reason.
- **The plan was out of date by the time it ran.** `candidate` had been re-cut from `main`. Plans
  that name commits "missing from `candidate`" should be re-checked with `git branch -r --contains`
  before an adaptation is attempted.

## Fixes

None needed.

## Durable tests added

- `ui/e2e/repos-add-team-admin-walkthrough.spec.js`: a member is refused `POST /api/repos` with a
  readable 403 through both the API and the Repos page, and the team admin adds it from the same
  page.

The pause and db-read regression specs the plan asked to port were already on `candidate`, unchanged
from `main`.
