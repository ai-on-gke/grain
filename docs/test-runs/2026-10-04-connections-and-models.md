# Connections and models: separate, per team, no deployment-key fallback

**Date**: 2026-10-04
**Plan**: `docs/test-plans/2026-10-04.md` §1
**Task**: grain/task-1827
**Tested commit**: `232c39294` (`candidate` when this task started). `git merge-base --is-ancestor 232c39294 origin/candidate` succeeds. The tip of `grain/task-1827`, with the fixes below, was also tested.
**§1 commits**: `4c59279d5`, `cf6e880fc`, `cc2032ab7`, `45d0240dc`, `ab02a110e`, `06960127c`, `5bfc54ac9`, `86c34f861`, `098eb70c0`, `8149c167c`, `4facffc38` and `57f76631c`. For each, `git branch -r --contains` lists `origin/candidate`, and each is an ancestor of the tested commit.

This report builds on `2026-10-03-connections-and-models-ui.md` and does not repeat its findings.

## Environment

| | |
|---|---|
| OS | Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, x86_64 |
| Go | go1.26.6 linux/amd64 |
| Node | v20.19.0 |
| Playwright | 1.62.1, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Browser | Chromium 1234 (Google Chrome for Testing 151.0.7922.34), headless |
| UI bundle | `cd ui && npm ci && npm run build`, into `pkg/ui/static` before `go build` |

## Servers used

- **`grain demo`**: `go build -o /tmp/grain ./cmd/grain` then `/tmp/grain demo -addr 127.0.0.1:0 -open=false` (port 42547), and one per Playwright worker via `ui/e2e/fixtures.js`. Demo has no secrets store, since `PUT /api/agent-backends/{id}/credential` answers 404 "secrets are not available". It also has no dispatch loop. So demo covered the connection setup and the refusals only.
- **`grain daemon`**: `/tmp/grain daemon -data-dir /tmp/dd -ui-addr 127.0.0.1:18420 -disable-native-sandboxes -default-target-repo acme/widgets [-agent-framework claude -claude-model legacy-claude-x] [-claude-path /tmp/fakeclaude]`, restarted on one data dir. The plan's `grain daemon -dev` does not exist in this build (`grain daemon -h` has no `-dev`). The daemon refuses to start with no sandbox backend, so `-disable-native-sandboxes` was used. It runs the real orchestrator and dispatch loop. Every dispatch then fails at sandbox acquisition, which comes *after* admission. So holds, admission and "dispatched on the next cycle" are all observable live, but the agent build (the credential check) is not. Loopback requests are trusted, so `X-Grain-User` selects the acting user.
- **Go tests**: the `pkg/orchestrator` tests run `RunCycle` against a real SQLite store and githubsim. The `cmd/grain` tests drive the real `agentPool` with a Claude stub binary. The `tests/e2e` harness was not used for steps 3, 4 and 7, because it does not wire the daemon's `agentPool` (`ModelAvailability` and `TeamHasAgentBackend` are only set in `cmd/grain/daemon.go`). The same paths were covered by the daemon and these tests instead.

## Steps and outcomes

### 1-2. A team admin sets up connections (demo and browser)

`ui/e2e/team-connections-models-walkthrough.spec.js` (new) **passes** (`npx playwright test e2e/team-connections-models-walkthrough.spec.js --project=chromium`).

- The system admin creates `gamma-1827` (admin `gadmin-1827`) and `delta-1827` (admin `dadmin-1827`). Delta's admin creates `delta-conn-1827` through `PUT /api/teams/delta-1827/agent-backends/delta-conn-1827`, which returns 200 with `team: delta-1827`.
- In the browser as `gadmin-1827`, Team settings → Connections first shows `Team "gamma-1827" has no provider connection…`. The admin adds "Gamma One" (Claude, advertising model-a and model-b) and "Gamma Two" (Claude, advertising model-a). Models → Default model is set to model-a.
- After a reload, the cards read "Advertises: model-a, model-b" and "Advertises: model-a", Default model shows model-a, and `GET /api/teams/gamma-1827` returns `defaultModel: model-a`. Disabling Gamma One survives a reload.
- Refusals, each sent as `gadmin-1827`:
  - `PUT /api/teams/gamma-1827/agent-backends/delta-conn-1827` → **403** `connection "delta-conn-1827" belongs to team "delta-1827", not "gamma-1827"`.
  - `PUT /api/teams/delta-1827/agent-backends/x` → **403**.
  - `DELETE /api/teams/gamma-1827/agent-backends/delta-conn-1827` → **403**.
  - `credentialBackendId: delta-conn-1827` → **400** `credentialBackendId must name another of team "gamma-1827"'s own connections`.
  - `credentialBackendId` naming Gamma One → 200.
  - A body that says `"team":"delta"` is filed under gamma anyway.
  - Delta's connection is unchanged afterwards.
  - The same refusals were run with curl against the hand-run demo, which also refused claiming the default team's `gemini-pro` (403).
- **Key not echoed.** This was checked on the daemon, because demo has no secrets store. As `gadmin`, `PUT /api/agent-backends/g1/credential {"value":"sk-ant-oat01-GAMMAKEY1827"}` → `{"set":true}`. The value appears in none of `/api/teams/gamma`, `/api/teams`, `/api/config`, `/api/settings` or `/api/tasks/9`, nor in the Team settings → Connections DOM (Playwright `page.content()`). Setting a credential on a connection id gamma does not own → 403.

### 3. The three holds (daemon, browser, Go)

On the daemon, team gamma was given a GitHub credential (`PUT /api/teams/gamma/github-credential`), and tasks were filed and approved through the API.

| Hold | Before the fixes | After | Where |
|---|---|---|---|
| Team has no connection | `queued`, **no reason at all**; log says `skipped: paused or busy` (**F3**) | task pane: `waiting: team "eps" has no enabled provider connection -- a team admin adds one in Team settings -> Connections` | daemon + Chromium (task 7) |
| No connection serves the model | `waiting: no enabled connection of team "gamma" serves model model-a` | same; with g1 disabled, `… serves model model-a (1 disabled)` | daemon + Chromium (tasks 3, 9) |
| Every serving connection full | `all connections serving claude-x are busy (2 connections at their concurrency limits (3 of 3 runs))` | unchanged; with no model chosen it said the team **has no connection** (**F2**), and now says `all connections of team "default" are busy (1 connection at its concurrency limit (1 of 1 runs))` | `cmd/grain` pool tests |

- **Dispatched on the first cycle after the cause cleared.**
  - Task 3: `admission refused` at 09:28:38, g1 added, then run `3-1` at 09:28:48.
  - Task 9: g1 re-enabled at 09:35:58, then run `9-1` at 09:36:08 (`/tmp/run10.log`).
  - `TestDispatchHoldsATaskWhoseTeamHasNoConnection` covers the no-connection hold.
- **Busy hold, live.** This was not staged on the daemon. A busy connection needs a run that holds its lease, and every run there fails at the sandbox step and releases it at once. Concurrency 1 on two connections, with three runs spread over them and then queued, is covered against the real `agentPool` by `TestRotationHonoursConnectionConcurrency` and `TestRotationUsesOnlyEnabledTeamConnectionsServingTheModel`, which pass.
- **Wording vs the plan.** The plan expected "connections at their concurrency limit (1 of 1 runs)". With two concurrency-1 connections both full, the real text is "2 connections at their concurrency limits (2 of 2 runs)". The leading number counts connections and the bracket counts runs, as `57f76631c` intends.

### 4. A keyless connection gets no deployment key

- **Live.** The daemon had `claude-oauth-token` set deployment-wide (`PUT /api/agent-keys/claude`), and gamma's g1 had no key. A dispatch on g1 reached sandbox acquisition and failed there, before `buildClaudeFramework`. So the credential message could not be observed live without a sandbox backend.
- **Pool.** `TestAgentFrameworksIgnoreTheDeploymentsKey` passes: with every deployment key set in the UI and by flag file, a keyless Gemini, Claude or Codex connection fails with "… of its own". `TestAgentFrameworksIgnoresTheDeploymentsCodexLogin` passes too.
- **Wording.** The failure named "Settings -> Models", a system admin's page, although the team admin sets the key in Team settings (**F4**, fixed).

### 5. Migrations across restarts (daemon, one data dir)

1. **First start** with `-agent-framework claude -claude-model legacy-claude-x`. `secrets/` gains `.team-agent-backends-migrated` and `.agent-backend-keys-migrated`. The default team gets `defaultModel: legacy-claude-x` and `modelSettings: [{gemini-3.1-pro, high}]`, the latter from the `-gemini-model` / `-gemini-effort` defaults.
2. **Default model cleared** (`PUT /api/teams/default {"defaultModel":""}`), then restart: it came back as `legacy-claude-x` (**F1**, fixed). With the fix, a third marker `.model-selection-migrated` is written, and a cleared default stays cleared across restarts.
3. **Legacy seed.**
   - `PUT /api/agent-keys/claude {"value":"sk-deploy-LEGACY"}`.
   - `PUT /api/settings` with connection `leg1` `{model: claude-old, effort: high}` and a keyless connection `keyless` advertising `claude-old`.
   - Task 2's columns set to `agent_backend_id='claude-old', agent_model=''` with sqlite. The API no longer accepts that shape: `POST /api/tasks` moves a model-named `agentBackendId` into `model` itself.
   - `.agent-backend-keys-migrated` deleted, then restart.
4. **Results after that restart.**
   - The log shows `gave connection leg1 …` and `gave connection keyless its own copy of the deployment's claude key`, and the marker is rewritten.
   - `leg1` now reads `models: [claude-old]` with no `model`/`effort`, and the default team gained `{claude-old, high}`.
   - Task 2 reads `agentBackendId: ""`, `model: claude-old`.
5. **Copied key removed**: `DELETE /api/agent-backends/keyless/credential`, then restart. No "gave connection" line is logged, and `GET /api/secrets` lists only `agent-backend-leg1` and `claude-oauth-token`. The removed key stayed removed.

### 6. Template, schedule and repo-declaration `model:`

On the daemon, as `gadmin`:

- **Template.** `POST /api/templates {model: model-b}` → 201. `{model: g1}` (a connection id) → **400** `model "g1" names a connection, not a model it serves`.
- **Schedules.**
  - Schedule `sched-1` over the template, no model of its own: `POST /api/schedules/sched-1/run` filed task 5 with `model: model-b`.
  - Schedule `sched-2` with `model: model-a`: task 6 has `model: model-a`, so the schedule beats the template.
  - A schedule with `model: g1` → **400**, same message.
- **Suite schedule.** `POST /api/schedules {suiteId: suite-1, model: model-a}` → **400** `a schedule that runs a suite cannot choose a model: set it on the suite's templates instead`.
- **Model source in task details**: not shown (**F6**, filed).
- **Sync and `grain state check`.** Covered by `pkg/staterepo` (`TestASettingsFileWithoutModelsWarnsTheyGoBackToTheTeamDefault` and the agent-model tests) and `pkg/repodecl` model tests, which pass. A live `.grain/templates` sync over githubsim was not run.

### 7. `propose_task` with `model: <connection id>`

`TestProcessResultReadsAProposalModelNamingAConnectionAsItsPin` (ProcessResult over githubsim) passes, and the child is filed with `AgentBackendID` set and `Model` empty.

Reading the code turned up a cross-team hole, **F5**, now fixed. `proposalConnection` and the dispatch-side reading in `agentPool.resolve` matched *any* team's connection id, so a gamma run proposing `model: delta-conn` filed a gamma task pinned to delta's connection. That task was admitted and failed setup, which wasted a sandbox. It never ran on delta's key, because dispatch's `BelongsTo` check refused it.

### 8. Browser spec

- `ui/e2e/team-connections-models-walkthrough.spec.js` covers step 2 in demo.
- Step 3's reasons were read in Chromium against the daemon with a one-off Playwright script (`page.goto('/tasks/<id>')`, `getByText(reason)`), not a spec, because `ui/e2e` runs only demo, which has no dispatch loop. Both reasons rendered as in the table above.

## Findings

**F1: a cleared default-team model returns on every restart.** Fixed.
- **Repro**:
  1. `grain daemon -data-dir D -disable-native-sandboxes -claude-model X …`.
  2. `PUT /api/teams/default {"defaultModel":""}`.
  3. Restart with the same data dir.
  4. `GET /api/teams/default` returns `defaultModel: X` again.
- **Cause**: `MigrateModelSelection` runs on every start and gives the default team the deployment's legacy model fields when it names none. The daemon reseeds those fields from its flags whenever they are empty, so they are never empty.

**F2: with no model chosen, a busy team reads as one with no connection.** Fixed.
- **Repro**: in a team with no default model, file a task with no model. The team's only enabled connection has concurrency 1 and one run in flight. `agentPool.Availability` returns `team "default" has no enabled connection`.
- **Cause**: `waitReason` returned early whenever no model was chosen.

**F3: a task held because its team has no connection shows no reason.** Fixed.
- **Repro**: team `eps` with a GitHub credential and no connection. File and approve a task. After two ticks, `GET /api/tasks/<id>` has no `activity`, and the log says `candidate N skipped: paused or busy`.
- **Cause**: `heldForTeamSetup` holds through `dispatch.Hold` and records nothing, unlike the model holds, which record a `GrantWait`.

**F4: a keyless connection's error names Settings -> Models.** Fixed.
- That page is a system admin's. Connections are per team, and a team admin sets their keys in Team settings -> Connections.
- **Repro**: `TestAgentFrameworksIgnoreTheDeploymentsKey`'s error text, or `cmd/grain/daemon.go`'s Gemini, Claude and Codex "of its own" messages.

**F5: a proposal's or task's `model` naming another team's connection id becomes a pin to it.** Fixed.
- **Repro**:
  - Proposal side: configure `{ID: elsewhere-conn, Team: elsewhere}`, then a default-team run calls `propose_task {"model":"elsewhere-conn"}`. The child has `AgentBackendID: elsewhere-conn`.
  - Dispatch side: `agentPool.Availability({Model: "theirs"})` returned `""`, so the task was admitted. It then failed setup with "belongs to team …".

**F6: task details don't say which layer chose the model.** Filed as a follow-up, not fixed.
- When a schedule or template chooses the model, it is stamped as plain `Task.Model`, so the pane shows "Model: model-b" with no source. Only the team default is labelled.
- **Repro**: step 6 above, then `GET /api/tasks/5`.
- **Why not here**: it needs a new stored field, an API change and UI work.

**F7: a task held for its team's GitHub credential also shows no reason.** Filed as a follow-up, not fixed.
- This is the same class as F3, on the other half of `heldForTeamSetup`.
- **Repro**: team with a connection and no GitHub credential, file and approve a task. `activity` is empty and the log says `paused or busy`.
- **Why not here**: it is outside §1, and it needs its own wording and rendering choice. The F3 fix does clear a stale "no connection" reason when this hold takes over.

**Observations (not bugs)**
- The plan's `grain daemon -dev` doesn't exist. `-disable-native-sandboxes` is needed for a sandbox-less daemon.
- Team admins can read `GET /api/settings` (200), which is scoped. The first draft of the spec asserted 403 and was corrected.
- `MigrateModelSelection` has no marker of its own by design (it is idempotent). The plan's "both marker files" are `.team-agent-backends-migrated` and `.agent-backend-keys-migrated`.

## Stubs

- **GitHub.** githubsim in the `pkg/orchestrator` tests. The daemon had no GitHub access, and its target `acme/widgets` is fictional. No daemon run got far enough to call GitHub, because sandbox acquisition fails first.
- **Live model sessions.**
  - The `cmd/grain` tests use their Claude stub binary (`modelDispatchClaudeStub`).
  - `pkg/orchestrator` tests use `stubFramework`.
  - `/tmp/fakeclaude` (a shell script that prints the token it was given) was passed as `-claude-path` to the daemon, but was never reached.
- **Sandboxes.** None. `-disable-native-sandboxes`, so every daemon dispatch ends `setup-failed` at sandbox acquisition.
- **Cloud APIs.** None were called.
- **Not stubbed.** grain's own APIs and React were never mocked. `page.route` was not used.

## Fixes (each with a regression test)

| Commit | Fix | Test |
|---|---|---|
| `fix(model): stop the default team regaining a cleared model on restart` | `MigrateModelSelectionOnce` with `.model-selection-migrated` beside the secrets key; daemon calls it | `pkg/model` `TestMigrateModelSelectionOnceLeavesAClearedDefaultModelCleared`; live restart re-checked |
| `fix(dispatch): say a team's connections are busy, not missing, with no model chosen` | `waitReason` handles busy and cooling off when no model is chosen | `cmd/grain` `TestRotationWithNoModelSaysTheTeamsConnectionsAreBusy` |
| `fix(orchestrator): show why a task waits when its team has no connection` | `heldForTeamSetup` records a `model` GrantWait naming Team settings -> Connections, and clears it when a git-credential hold takes over | `pkg/orchestrator` `TestDispatchHoldsATaskWhoseTeamHasNoConnection` (extended; fails without the fix), `TestDispatchClearsTheNoConnectionWaitOnceTheTeamHasOne` |
| `fix(agents): point a keyless connection's error at Team settings` | Gemini, Claude and Codex messages name Team settings -> Connections (Settings -> Models for a system admin) | `TestAgentFrameworksIgnoreTheDeploymentsKey` (extended) |
| `fix(dispatch): never read another team's connection id as a pin` | `proposalConnection` and `agentPool.resolve` match only the task's team | `TestProcessResultReadsAProposalModelNamingAConnectionAsItsPin` (extended), `TestAModelNamingAnotherTeamsConnectionIsNotItsPin` (fails without the fix) |
| `test(ui): walk a team admin through their team's connections and models` | new spec | `ui/e2e/team-connections-models-walkthrough.spec.js` |

`pkg/orchestrator` is a trust-boundary path, so a security-review hold before auto-merge is expected.

## Checks

- `gofmt -l cmd pkg tests`: clean. `npm run format` in `ui/`: clean.
- `make security` (vet + `go test -race ./tests/security/...`): **ok** (118.9s).
- Targeted Go:
  - `go test ./pkg/orchestrator` is ok.
  - These `cmd/grain` subsets are ok: `TestRotation`, `TestPinned`, `TestModelBackend`, `TestAgentFrameworks`, `TestConnections` and `TestAModelNaming`.
  - These model-related subsets are ok: `pkg/model`, `pkg/ui`, `pkg/staterepo` and `pkg/repodecl`.
- **Grain CI**: run `ea85a343c584520d` at `b20f5c5` (every fix and the spec, before this report) passed the full suite: frontend, go, installer, lint, package cache, postgres, terraform and ui e2e. No known flake recurred. After this report was added and the 17 newer `candidate` commits were merged in (`bc13fde0a`, a clean merge), run `00d59a9af2b58224` passed the same full suite.
