# Walkthrough: per-connection usage-limit pauses and db-read enforcement (main)

Plan: `docs/test-plans/2026-10-11.md` §14 (Main-Only Changes). Task 3149, filed by task 2955.

## Changes under test

All four commits are on `origin/main` only (`git branch -r --contains` lists `origin/main` and
branches cut from it; `origin/candidate` does not have them):

- `acf69cd2f`: usage-limit pauses are keyed by the credential-owning model connection.
  `GET /api/pause` and `config.agentPause.pauses` list each pause with connection, team and `canLift`.
  `DELETE /api/pause?connection=` lifts one pause.
- `6d0f2cebd`: the banner, the Team settings chips and the queued-task pane name the paused connection
  and its team. A borrower is shown paused with its source.
- `9059de192`: db-read runs through `dbaccess.QueryReadOnly`, behind a tokenizing validator.
- `7283ae3f3`: clearing the CI cache needs a system admin. This commit also required a system admin to
  lift a pause, but `acf69cd2f` later deliberately relaxed that to "a team admin of the pause's team, or
  a system admin".

## Environment

- Branch `grain/eu/task-3149` cut from `origin/main` at `f71fd5363`.
- `go build ./cmd/grain` from that tree. The frontend was built with `cd ui && npm ci && npm run build`.
  For manual probing, `grain demo -addr 127.0.0.1:18080 -open=false -agent-paused` ran on its embedded
  SQLite store.
- Playwright 1.x with Chromium headless shell. The spec runs on its own `grain demo` worker through
  `ui/e2e/fixtures.js`.
- Go tests use real SQLite stores (`pkg/model/sqlite`).
- Sandbox note: `npx playwright install chromium` failed with `self-signed certificate in certificate
  chain` until it was run with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

## Stubs

| Boundary | Stub | Where |
| --- | --- | --- |
| Provider usage limit, UI half | demo's existing `-agent-paused`. It begins a real `orchestrator.Pause` on the default team's `gemini-pro` connection with a 6h `UsageLimitError`. | `pkg/demo/demo_scenarios.go` `demoAgentPause` |
| Provider usage limit, dispatch half | a real `codex` subprocess script. It answers key `sk-alpha-a` with Codex usage-limit wording ("usage limit … Try again in 2h 0m") and serves any other key with a minimal successful event stream. | `cmd/grain/connection_pause_walkthrough_test.go` |

Nothing else is stubbed: grain's own API, React UI, connection pool, `RunDispatch`, `ui.Server`, `ui.HTTPClient` and the
`query_grain_db` tool are all real.

## Steps and results

### Step 1: two teams, a borrower, and a usage limit on team A's credential-owning connection

Setup, all through grain's API:

- **Team A** is the default team. Its connections are:
  - `gemini-pro` (antigravity), which the demo pauses;
  - `a-borrow` ("A borrower", antigravity, `credentialBackendId: gemini-pro`);
  - `a-own` ("A own key", claude).
- **Team A users:** ann is the team admin and is not a system admin. amy is a plain member.
- **Team B** is `beta`, with connection `b-gem` ("B gemini", antigravity). bea is its admin and bob is a
  member.
- **System admin:** `sysadmin`, which is also a plain member of default.

Each queued task's pane (`ui/e2e/connection-pause-teams-walkthrough.spec.js`):

| Task | Expected | Result |
| --- | --- | --- |
| Team A, no pin, any framework | rotates onto `a-own`, not held | **pass**: no "waiting" line |
| Team A, `agentFramework: antigravity` | both antigravity connections paused, held | **pass**: "waiting: connection Gemini 2.5 Pro (team default) is paused until … Gemini API quota exhausted …" |
| Team A, pinned to `a-borrow` | held on its source's pause | **pass**: same line, naming the source |
| Team B, `agentFramework: antigravity` | not held | **pass** |

Team settings:

- **Connections tab, team A.** "Gemini 2.5 Pro" and "A borrower" both show **Paused** with the until-line
  and reason. "A own key" shows **Active**. Pass.
- **Models tab, team A.** Chips read "Gemini 2.5 Pro (Paused)" and "A borrower (Paused)". Pass.
- **Team B's settings.** "B gemini" is **Active** and no card is paused. Pass.

The dispatch half is in `cmd/grain/connection_pause_walkthrough_test.go`. `grain demo` dispatches nothing,
so it cannot show this. The test uses the real `daemon.AgentPool` and `orchestrator.RunDispatch` with the
codex stub:

- **Pause comes from the run.** A run pinned to `alpha-a` meets the limit. Its run is recorded `paused`,
  not failed. `Pause.Pauses` then lists `alpha-a` of team alpha and nothing else. The pause is begun by the
  run, through `CredentialConnection`, and not by hand. Pass.
- **Team A rotates.** Team alpha's next unpinned run is built on `alpha-b` and succeeds. Pass.
- **Borrower waits.** A task pinned to `alpha-borrow` gets an Availability reason containing "paused". Pass.
- **Team B keeps dispatching.** Team beta, on the same framework, is admitted and runs on `beta-a`
  successfully. Pass.
- **Keys spent.** The stub logs the keys spent as exactly `sk-alpha-a, sk-alpha-b, sk-beta-a`. Pass.
- **Lift.** Lifting `alpha-a` by connection admits the borrower again. Pass.

### Step 3: each viewer's banner, `GET /api/pause`, "Resume now" and `DELETE /api/pause`

`GET /api/pause` (and `config.agentPause`):

| Viewer | Sees | `canLift` |
| --- | --- | --- |
| amy (team A member) | one pause, `gemini-pro` / `Gemini 2.5 Pro` / `default` / `antigravity` | false |
| ann (team A admin) | same | **true** |
| bob (team B member) | `paused: false`, no pauses; `config.agentPause` absent | n/a |
| bea (team B admin) | `paused: false`, no pauses | n/a |
| sysadmin, Admin mode off | same as amy | false |
| sysadmin, Admin mode on | same | **true** |

The banner:

- **amy and sysadmin with Admin mode off** see "Agent usage limit reached: nothing is being dispatched onto
  connection Gemini 2.5 Pro (team default) until …" with **no** "Resume now".
- **bob and bea** see no banner.
- **ann** sees the banner with "Resume now". Clicking it sends exactly
  `DELETE /api/pause?connection=gemini-pro`. The banner goes, the held panes and the Paused chips clear, and
  `GET /api/pause` reads `paused: false` for amy, ann and sysadmin (with and without Admin mode).

`DELETE /api/pause` as each caller, before ann's lift:

| Caller | Query | Result |
| --- | --- | --- |
| amy | `connection=gemini-pro` / none | 403 "not an admin of any team" |
| bob | `connection=gemini-pro` | 403 |
| sysadmin, Admin mode off | `connection=gemini-pro` | 403, with the "turn on Admin mode" hint |
| bea (another team's admin) | `connection=gemini-pro`, `team=default&framework=antigravity`, none | 200, `lifted: false`; pause still in force |
| ann | `connection=a-borrow` (the borrower's own id) | 200, `lifted: false` (see O2) |
| ann | `connection=b-gem` (team B's) | 200, `lifted: false` |
| ann | `connection=gemini-pro` (via the banner) | 200, lifted |

Only viewers with `canLift` could lift. The system-admin lift with Admin mode on is covered by the existing
`model-source-pause-walkthrough.spec.js`, which lifts the same pause from the banner as an admin in Admin
mode. It still passes.

`DELETE /api/ci/cache` (`7283ae3f3`):

| Caller | Result |
| --- | --- |
| ann (team admin) | 403 |
| bea (team admin) | 403 |
| sysadmin, Admin mode off | 403 |
| sysadmin, Admin mode on | let through: 404 "this deployment has no grain CI wired", since the demo has no CI |

### Step 4: db-read bypasses through the real tool and HTTP path on SQLite

`grain demo` does not wire `ui.Config.SourceDB`, so `/api/tasks/{id}/db/query` answers 503 there. The
walkthrough therefore builds the path the daemon builds (`cmd/grain/db_read_walkthrough_test.go`):

- a real SQLite store;
- a `ui.Server` with `SourceDB` set the way `pkg/daemon/ui_server.go` sets it;
- a task holding `db-read` with a live run;
- `query_grain_db` from `daemon.GrantedDaemonTools`, whose `daemonDB` hop is a real `ui.HTTPClient`.

Each query was sent twice: through the tool, and straight at `POST /api/tasks/{id}/db/query` (what an agent
that skipped the tool's own validator would reach). The test reads every row of `task` and `task_grant`
after each query.

| Query | Tool | HTTP | Rows |
| --- | --- | --- | --- |
| `WITH x AS (SELECT 1) DELETE\tFROM tasks` | refused | 400 | unchanged |
| `SELECT 1; DELETE\nFROM tasks` | refused | 400 | unchanged |
| `PRAGMA writable_schema=ON` | refused | 400 | unchanged |
| the same against the real tables (`task`, `task_grant`), plus `UPDATE`, an `INSERT` of a `db-write` grant, and `PRAGMA query_only = OFF; COMMIT; DELETE` | refused | 400 | unchanged |

A plain `SELECT COUNT(*) FROM task` through the tool still answers (2 rows counted), so the refusals come
from the queries and not from a broken path. A db-read grant is not given `execute_grain_db`. Grain's own
writes still work afterwards.

**Mutation check.** With `ValidateReadOnlyQuery` forced to return nil (both the tool and the handler
validators off), every write against `task`/`task_grant` is still refused by the database ("attempt to
write a readonly database (8)", and "constraint failed (531)" from the commit hook for the
`query_only = OFF; COMMIT` form), and no row changes. That is `QueryReadOnly` doing its job.

With `QueryReadOnly` also swapped for the db-write path, the test fails at once: the `task` table is
emptied. So the test is not vacuous.

## Findings

### F1: the audit's bypass queries name a table grain does not have (fixed, test only)

The audit queries (task 2801), the commit message of `9059de192`, and `TestTaskDBQueryReadGrantCannotWrite`
in `pkg/ui/task_db_test.go` all write to `tasks`. Grain's table is `task`.

Reproduction: on any store `model.New(sqlite.Open(...))`, run `SELECT COUNT(*) FROM tasks`. It gives
`SQL logic error: no such table: tasks (1)`. With the validator disabled, the audit's queries fail with that
same error rather than the read-only one.

Consequence: the handler test's "title is still original" assertion could never fail. Even past every
safeguard, the statement had no table to change. The validator was the only thing the test proved.

The dbaccess package test (`readonly_test.go`) creates its own `tasks` table, so it is not affected.

Fix: `TestTaskDBQueryReadGrantCannotWrite` now aims its bypasses at `task`. The new walkthrough sends the
audit's literal queries and the same ones against `task` and `task_grant`. These are test-only changes,
committed as `test:`. No product bug sits behind this one: production refuses the real-table forms (shown
above).

### Observations (no change made)

- **O1.** With the validator bypassed, `PRAGMA writable_schema=ON` runs through `QueryReadOnly` without
  error (0 rows). It is harmless: the setting lives on the dedicated connection, `QueryReadOnly` closes that
  connection rather than returning it to the pool, `query_only` still blocks every write on it, and the
  validator refuses the statement at both edges. Noted as a defence-in-depth detail, not filed.
- **O2.** `DELETE /api/pause?connection=<borrower id>` answers 200 `lifted: false`. A borrower's pause is
  keyed by its source connection, so the source's id is the only one that lifts it. Every surface that
  offers a lift (the banner, `canLift` entries) names the source, so a user is never steered to the
  borrower's id. Left as is.
- **O3.** In `grain demo`, a request with no `X-Grain-User` acts as the demo's local actor, a system admin
  with no Admin-mode suppression, so it can lift any pause. That is the demo's trusted-loopback posture,
  the same on every route, and not specific to pauses.
- **O4.** `grain demo` cannot exercise the db-read route (no `SourceDB`) or dispatch, which is why steps 1
  (dispatch half) and 4 are Go tests on the real code path rather than browser steps.

No product bugs were found, so no follow-up tasks were filed.

## Durable tests added

- `ui/e2e/connection-pause-teams-walkthrough.spec.js`: step 1 UI and step 3. It has its own `demoArgs`
  (`-agent-paused -open=false`), so it never shares a demo with `model-source-pause-walkthrough.spec.js`,
  which lifts the same pause.
- `cmd/grain/connection_pause_walkthrough_test.go`: step 1, dispatch half.
- `cmd/grain/db_read_walkthrough_test.go`: step 4.
- `pkg/ui/task_db_test.go`: F1.

## CI verdict

See the pull request for this branch: grain CI full suite, run with `wait_for_checks`. The result is
recorded in the task's closing note.
