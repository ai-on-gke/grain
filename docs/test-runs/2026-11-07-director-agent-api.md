# Walkthrough: Director rails & Agent API (2026-11-07 plan §4)

- **Plan:** [`docs/test-plans/2026-11-07.md`](../test-plans/2026-11-07.md) §4. It covers the shipped Director posture (PRs #788, #806, #817), the Tier 3 floor (#786), seeded-default admission and console-session sync (#784), per-turn bounds and `grain/config` holds (#783), the G8 approval rail (#785, #797), `capabilities:detach` and the setup `Stop` races (#787, #795), compact `grain_explore_scope` (#710) and `GET /api/events` (#731).
- **Run by:** task 3488, 2026-11-08 02:04–02:45 UTC.
- **Base:** `origin/main` at `46ab9012` (PR #876).
- **Scripts & raw output:** [`2026-11-07-director-agent-api/`](2026-11-07-director-agent-api/).

## Outcome

| § item | Result |
|---|---|
| 1. Fresh vs upgraded Director posture, Adopt shipped defaults (REST + UI at 1440/390) | **Fixed one bug (finding 1).** Fresh posture, upgrade banner, adopt over REST and UI, and the concurrent-edit sync all pass. One lost update remains in the whole-list PUT: a stale tab's adopt drops a grant another tab added (finding 4). Filed as a follow-up. |
| 2. Adversarial proposal & approval rail matrix | **Fixed one rail bypass (finding 2).** The per-turn cap, `grain/config` holds, Tier 3/person-only holds, elevated-`admission` holds, unchanged-seeded-default admission and lineage self-approval refusals all hold. External credentials are not covered by the G8 rail (finding 5). Filed as a follow-up. |
| 3. `capabilities:detach` & setup `Stop` race | **Fixed one hint bug (finding 3).** Detach-only credentials detach cleanly and cannot attach, replace, widen repo lists or touch standing sessions. The live `Stop` race could not be driven here (no model backend or agent CLI in the sandbox). Its regression tests pass under `-race -count=3`. |
| 4. `grain_explore_scope` size/hints & `GET /api/events` SSE | **Pass.** Every view fits one page (≤ 9.6 KB). Typos get "did you mean". SSE ordering, `Last-Event-ID` replay (no gaps or duplicates) and repo isolation all hold. Live `task.ci` events could not be produced (see item 4). |

## Environment

- **Sandbox:** Debian VM, 7 vCPU, 30 GB RAM.
- **Toolchain:** Go 1.26.10, Node 20.19.0, Playwright Chromium from `/var/cache/playwright`. `bc` was installed with apt.
- **Build:** `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. This was redone after each fix. A `grain.before` binary built from the base UI and API was used for the before screenshots.
- **Daemons:** real `grain daemon` processes started by [`env.sh`](2026-11-07-director-agent-api/env.sh):
  - SQLite store, `-sandbox-port-base=0`, `-poll-interval 2s`, `-ci=false`.
  - Local bare repositories `acme/app` and `acme/other`, a stub PAT git connection, and `externalAgentApi` turned on.
  - Control plane only (`-disable-native-sandboxes`) on 4271. The non-isolating backend (`-host-sandboxes`) ran on 4272 (fixed build) and 4273 (base build).
- **Demo:** `grain demo -github-sim -fake-ci -reconcile 2s` ran on 4274, for the CI-event check.
- **The Director:** task 1 on every fresh store. Its proposals were replayed the way the control plane relays a live `propose_task` call: `POST /api/tasks` with `proposedBy: "1"`, which only the daemon-trusted access code may name (an acting agent naming it gets "an agent cannot name somebody else as the proposedBy"). Its own agent-API calls carried `X-Grain-Acting-Task: 1` and `X-Grain-Acting-Reason`.
- **External agents:** `gma_` credentials minted with explicit scopes and repos, used over REST and `POST /api/mcp` (`grain_invoke`, `grain_explore_scope`).

## Findings

### Finding 1 — a session's `none` network was reported, and drawn, as honoured on a backend that cannot withhold it (fixed)

**Repro** (base build, [`item1-ui-layout-before.json`](2026-11-07-director-agent-api/item1-ui-layout-before.json)):

1. Run `SANDBOX_FLAGS="-host-sandboxes -sandbox-dir …" env.sh start`.
2. `GET /api/console-sessions` answers `{"networkAccess":"none"}` for the Director.
3. The Director's task 1 has `"networkAccess":"open"`.
4. The pane's select reads "none (this deployment only)", with no warning at either width.

`ConsoleSessionPage.jsx`'s own comment promised "the warning under it" (grain/task-399), but nothing rendered it, and the API gave the client nothing to draw it from.

**Fix:**
- `ConsoleSessionSetting.effectiveNetworkAccess` is `"open"` when the configured `none` cannot be honoured (`sandboxesIsolateNetwork() == false`).
- `ConsoleSessionPage` shows a warning `Alert` under the select (`data-testid="session-network-not-isolated"`).

**Regressions:**
- `TestConsoleSessionsOnShapedUnisolatedBackendResolveNetworkNoneToOpen` and `TestConsoleSessionsOnIsolatedUnshapedBackendKeepNetworkNone` assert the field on both backend kinds.
- The Vitest case "warns when the backend cannot withhold the session's network".

**After** (fixed build, [`item1-ui-layout-after.json`](2026-11-07-director-agent-api/item1-ui-layout-after.json)):
- The API answers `{"networkAccess":"none","effectiveNetworkAccess":"open"}` on the host backend, and no `effectiveNetworkAccess` on the isolating one.
- [`ui_director_layout.mjs`](2026-11-07-director-agent-api/ui_director_layout.mjs) asserts at 1440×900 and 390×844 that the warning is shown, sits below the select, stays inside the viewport, causes no horizontal overflow and raises no page errors.
- Screenshots: [before](2026-11-07-director-agent-api/screenshots/before-contact-sheet.png), [after](2026-11-07-director-agent-api/screenshots/after-contact-sheet.png).

### Finding 2 — G8 rail bypass: an agent could approve a proposal its proposer left for a person (fixed)

The approval rail lists `hold_for_approval` as a hold. It read that hold off `HeldForApprovalTag`, which by design is recorded only when the proposer's own permission would have queued the proposal ("a proposal held by a run whose permission covered nothing carries no tag"). So a run without auto-approve that asked `hold_for_approval` left no trace of the request.

**Live repro** ([`item2-rails.before-fix.out`](2026-11-07-director-agent-api/item2-rails.before-fix.out)):

1. A person gives the Director `api:tasks:admin` + `api:dispatch:write`.
2. Task 28 (approved, person-filed) proposes task 29 with `holdForApproval: true`.
3. `POST /api/tasks/29/approve` and `/run-now` as task 1 → `HTTP 200`, and task 29 is queued.

**Fix:**
- New `model.HoldRequestedTag` (`hold-requested`), recorded on every proposal that asks `hold_for_approval`. Both `ui.Client.CreateTask` and the orchestrator's end-of-run relay write it.
- `agentApprovalRail` reads `model.HoldRequested` (either tag).
- `HeldForApprovalTag` keeps its narrower meaning ("a choice was made"), so the UI's held badge is unchanged.
- `docs/agent-api.md` is updated to match.

**After** ([`item2-rails.out`](2026-11-07-director-agent-api/item2-rails.out)): approve and run-now on task 29 → `403 … held for a person (it was filed with hold_for_approval)`.

**Regressions:**
- `TestAgentApprovalRailHonoursHoldForApprovalFromAProposerWithoutPermission`: REST filing, approve and run-now refused, plain sibling still approvable.
- `TestProcessResultHoldWithoutPermissionIsAnOrdinaryProposal`: the relay records the tag.

### Finding 3 — `grain_explore_scope` told a detach-only credential it held dangerous `capabilities:admin` (fixed)

**Before:** an external credential holding only `capabilities:detach` + `tasks:read` got this overview:

```
{"group":"capabilities","scopes":[{"scope":"capabilities:admin","description":"Change capability servers, policy and any task's or repository's grants","dangerous":true,"routeCount":3}]}
```

**Fix:** `mcpCallerAccess.authorizedRoutes` labels a route reached only through a carve-out with the scope that reaches it: `capabilities:detach`, or `tasks:admin` for the priority patch. Authorization is unchanged; the gate still checks `routeScopes` plus the carve-out's body rules.

**After** ([`item34-detach-explore.out`](2026-11-07-director-agent-api/item34-detach-explore.out)): `{"scope":"capabilities:detach","routeCount":3}`, not dangerous. Each route's `scope` field also says `capabilities:detach`.

**Regression:** `TestCapabilitiesDetachIsWhatGrainExploreScopeSays`.

### Finding 4 — Adopt shipped defaults from a stale tab drops a grant another tab added (follow-up filed)

[`item1_posture_adopt.sh`](2026-11-07-director-agent-api/item1_posture_adopt.sh), last block:

1. Tab A reads the pre-#806 row.
2. Tab B adds `api:tasks:read`.
3. Tab A clicks Adopt; the pane sends `held ∪ missing` from its own read.
4. Result: `{"tabBGrantKept":false}`.

This is the whole-list `PUT /api/console-sessions/{id}` `capabilities` semantics. Every tick box in the pane shares it; it is not specific to #806. Concurrent server-side edits do not race: 10 parallel title edits plus an adopt → session and task grants identical, and no sync-race lines in the daemon log. A server-side `adoptShippedDefaults` (or an `expect` precondition on `capabilities`) would close it. That is filed as a follow-up rather than patched here.

### Finding 5 — the G8 rail does not cover external agent credentials (follow-up filed)

With `tasks:admin` + `dispatch:write` on `acme/app`, an owned `gma_` credential (owner `alice@example.com`) can approve, over REST and over `POST /api/mcp` (`grain_invoke api_approve`):
- its own filing (task 37), and
- a person-filed task with `admission: durable` (task 35).

`run-now` on the same durable task is refused ("serves no task whose grants could cover approving it"). `externalAgentRails` never calls `agentApprovalRail`, and `docs/agent-api.md` documents that no task id matches `external:<name>` for lineage. Whether a person's delegated credential may approve a person-only hold is a policy question, and the approve/run-now asymmetry should be settled deliberately. Filed as a follow-up.

## Item-by-item record

### 1. Posture & Adopt shipped defaults ([`item1-posture-adopt.out`](2026-11-07-director-agent-api/item1-posture-adopt.out), [`item1-ui-adopt.json`](2026-11-07-director-agent-api/item1-ui-adopt.json))

**Fresh store:**
- The Director has `networkAccess: none` and `tasksApproved: true`.
- It holds all ten Tier 1 grants (`api:task-admin`, `api:capabilities:read`, `api:capabilities:detach`, `api:settings:read`, `api:repos:read`, `api:releases:read`, `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:dispatch:read`) beside `api:ci-read`, `api:deployment:read`, `api:self-debug` and the `grain#health`/`journal`/`source` and `config-map` grants.
- `missingShippedDefaults` is empty, and task 1 is filed with `networkAccess: none`.

**Non-isolating backend:** see finding 1.

**Upgraded row** (pre-#806/#817: no Tier 1 grants, `tasksApproved: false`, a custom `github#read-issues`):
- `missingShippedDefaults` lists exactly the ten Tier 1 grants.
- Adopt over REST keeps the custom grant, keeps `tasksApproved: false` (sticky, #817), and leaves session and task grants identical (`sessionOnly: [] taskOnly: []`).

**UI adopt** ([`ui_adopt.mjs`](2026-11-07-director-agent-api/ui_adopt.mjs)), at both 1440×900 and 390×844:
- The banner and its button sit inside the viewport.
- Clicking **Adopt shipped defaults** removes the banner, takes 10 missing grants to 0 and keeps the custom grant.
- No horizontal overflow and no page errors. Banner screenshots: `screenshots/adopt-banner-{desktop,phone}.png`.

### 2. Proposal & approval rail matrix ([`item2_rails.sh`](2026-11-07-director-agent-api/item2_rails.sh), [`item2-rails.out`](2026-11-07-director-agent-api/item2-rails.out))

**Director proposals in one turn** (the turn starts at a person's comment on task 1):

| Proposal | Result |
|---|---|
| unchanged seeded repo default (`acme/other` seeds `github#read-issues`, which the Director lacks) | queued ✅ |
| `api:settings:write` (Tier 3 floor) | proposed (held) ✅ |
| `grain#host-exec` (person-only) | held ✅ |
| `admission: durable` / `bypass` | held ✅ |
| `repo: grain/config` / `writes: grain/config` | held ✅ |
| seeded template `system:gke-sandbox-cluster` (carries `host-exec`) | held ✅ |
| `holdForApproval: true` | held ✅ |
| fan-out: after the 1 queued above, 9 more queue, then the 11th and 12th are held | `TurnFanOut` 10 ✅ |

**Director approvals at shipped posture** (`api:task-admin`, no `tasks:admin`/`dispatch:write`): every approve and run-now is refused by scope ✅.
- `PATCH {approved:true}` on its own proposal → 403 ✅.
- `POST /api/tasks {approved:true}` → 403 ✅.

**After a person grants `api:tasks:admin` + `api:dispatch:write`:**
- approve or run-now of its own held proposal → 403, "it proposed it" ✅.
- third-party `hold_for_approval` proposal → **was 200 (finding 2)**, now 403 ✅.
- third-party proposal asking `api:settings:write` → 403 ✅.
- person-filed `durable` → 403 ✅.
- person-filed `grain/config` → 403 ✅.
- unrelated plain proposal → 200 ✅ (documented: tasks:admin approves work held for nobody).

**External credential:** see finding 5. Over MCP the approve tool is `api_approve`; `api_approve_task` gets "did you mean api_approve, …" ✅.

### 3. `capabilities:detach` & `Stop` races ([`item34_detach_explore.sh`](2026-11-07-director-agent-api/item34_detach_explore.sh), [`item34-detach-explore.out`](2026-11-07-director-agent-api/item34-detach-explore.out))

**Detach-only credential** (`capabilities:detach@acme/app`, `tasks:read@acme/app`; no dangerous scope):
- `tools/list` shows the six controller tools, and `grain_explore_scope` lists `capabilities:detach` (3 routes; finding 3 fixed).
- Over `grain_invoke api_set_capability`, these are refused:
  - attach → 403.
  - replace → 403.
  - no reason → refused ("reason is required").
  - the Director's standing session → 403.
- Detach with a reason removes `github#read-issues` and leaves `grain#journal`.
- REST `PATCH /api/tasks/{id}` capabilities → 403.
- A repo default list naming a grant not already stored → 403 ("adding one needs capabilities:admin").

**Setup / pre-registration `Stop`:**
- The live attempt used a host-sandbox daemon, `-max-concurrent 1`, and a GitHub host that accepts but never answers.
- It never reached setup: dispatch stops at "no configured agent backend is free: of 0 configured". The sandbox has no model credential and no agent CLI, so a blocked checkout cannot be staged here.
- The scenario's regressions were run instead, under `go test -race -count=3` ([`item3-stop-race-tests.out`](2026-11-07-director-agent-api/item3-stop-race-tests.out)), all passing:
  - `TestStopDuringSandboxSetupCancelsImmediatelyAndRetrySkipsBackoff`
  - `TestStopDuringCloneCancelsImmediatelyWithoutSetupFailedStrike`
  - `TestStopBeforeSetupStartsPreservesCancelledRowWithoutAcquiringSandbox`

### 4. `grain_explore_scope` & `GET /api/events` ([`item34-detach-explore.out`](2026-11-07-director-agent-api/item34-detach-explore.out), [`item4_events.sh`](2026-11-07-director-agent-api/item4_events.sh), [`item4-events.out`](2026-11-07-director-agent-api/item4-events.out))

**Explore sizes**, for a 7-scope credential:

| Call | Size |
|---|---|
| `{}` | 2.2 KB |
| `view=index` / `scope=all` | 2.3 KB |
| `scope=tasks` | 9.6 KB (schemas folded into compact lines) |
| `tool=approve` | 1.4 KB |

**Explore hints:**
- `aprove` → "Did you mean api_approve?"; `tsks` → "Did you mean tasks?".
- `priority-dispatch` → "Scope groups near that word: dispatch".
- `secrets` → names the scopes it needs.
- `grain_invoke api_aprove` → "did you mean api_approve?".
- `self-debug` and `frobnicate` get only the generic pointer. The retired grant names (`self-debug`, `priority-dispatch`, `read-issues`) are capabilities rather than API routes, so explore does not mention their `api:self-debug` / `admission` / `github#read-issues` successors. This is noted, not changed.

**SSE:** an operator stream and an `acme/app`-only credential stream ran across creation, approval, comments, the Director's own turn, defer and close in `acme/app`, `acme/other` and the no-repo Director.
- Ids are strictly increasing on every stream.
- The restricted stream received 0 events outside `acme/app`.
- `?repo=acme/other` → 403.
- Its first `: ready cursor=0` does not leak the global sequence.
- Reconnecting with `Last-Event-ID: 17` replayed exactly the 3 events missed while away. The two connections together equal the operator's `acme/app` ids (`4 6 8 13 17 18 20 21`): nothing missed, nothing duplicated.
- JSON paging from the same cursor: `{"ids":["18","20"],"cursor":"20","hasMore":true}`.

**`task.ci` events:** these are written when a task observation gains a CI verdict. Neither the control-plane daemon (no sandboxes, no GitHub) nor `grain demo -github-sim -fake-ci -reconcile 2s` produces one: its event log after start had 35 `task.state`, 6 `task.comment`, 1 `task.question` and no `task.ci`. The CI half rests on `pkg/model/integration_event_test.go`.

## Checks run

- `go test ./pkg/ui/ ./pkg/model/` and the targeted `./pkg/orchestrator/ -run 'Hold|Propos|Relay|TestProcessResultHold'`.
- `go test ./tests/hygiene/...` (pass).
- `npx vitest run src/components/ConsoleSessionPage.test.jsx` (30/30).
