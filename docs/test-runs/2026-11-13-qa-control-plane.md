# Walkthrough: QA control plane, off by default (2026-11-13 §2)

Plan: [`docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md) §2, "QA control plane (M1), off by default". Covers PR #1404 (`24283c109`, schema 146).

- **Commit walked:** `23005075b` (`origin/main` at 2026-11-13 12:05 UTC), compared against baseline `24d0eba10` (schema 139).
- **Helpers:** [`2026-11-13-qa-control-plane/`](2026-11-13-qa-control-plane/).
- **Raw output:** [`out/`](2026-11-13-qa-control-plane/out/), [`out/before/`](2026-11-13-qa-control-plane/out/before/) and [`out/after/`](2026-11-13-qa-control-plane/out/after/).

## Results

| # | Item | SQLite | PostgreSQL | Outcome |
|---|---|---|---|---|
| 1 | Off really is off (`task_qa_*` empty, no `perf/qa-checks.jsonl`, dispatch writes vs `24d0eba1`) | pass | pass | Zero `task_qa_*` rows, zero perf JSONL files, and identical per-dispatch task-table tuple writes (`+20 ins / +93 upd / 0 del` across 5 dispatches) vs `24d0eba10`. |
| 2 | `grain qa status\|enable\|disable` round trip, flag preservation, epoch monotonicity, range refusals, audit + state-repo + restart | pass after fix | pass after fix | **F1 fixed:** bare `grain qa enable` previously clobbered an explicitly configured `0%` shadow-only rollout to `30%` after `grain qa disable`. |
| 3 | Sticky arm assignment across 50 tasks in 20 work-tree groups, retry stickiness, epoch re-randomization | pass after fix | pass after fix | **F2 fixed:** `resolveTaskQAAssignment` previously split originless child/review tasks (`WorkOrigin == ""`, `LinkProposedBy != ""`) off from their work tree's root arm. |
| 4 | `/next/settings/tasks` card agrees with CLI, inline boundary refusals, viewer read-only + 403 forced POST, two-tab non-stomp | pass after fix | pass after fix | **F3 fixed:** `GET /api/config` previously returned `access: null` for scoped `gma_…` `grain/viewer` bearer tokens, leaving `/next/` Settings controls enabled until save failed 403. Also restored worker-scoped `demoServer` state in `ui-ng/e2e/next-qa-settings.spec.js`. |
| 5 | Schema 139 → 146 upgrade with QA unset, `grain state check`, dump/import round trip | pass | pass | Clean in-place upgrade 139 → 146 on both stores; `grain state check` passes on unset and configured stores and refuses out-of-bounds `grain_config.qa`; state-repo sync exports `grain_config.qa` and omits local-only `task_qa_*` tables. |

## Environment

- **Binaries:**
  - `HEAD`: `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (schema 146).
  - Baseline (`24d0eba10`): built from a detached worktree at `24d0eba10` to `/home/debian/bin/grain-24d0eba1` (schema 139) for Item 1's write-count comparison and Item 5's real 139 → 146 store upgrades.
- **Daemon:** real `grain daemon -sandbox-port-base=0 -host-sandboxes -as alice@example.com -repo acme/widgets -default-target-repo acme/widgets` listening on `127.0.0.1:4281` (`4282` for Item 5's import target).
- **Stores:**
  - **SQLite:** `<work>/data/store/grain.db` (WAL mode).
  - **PostgreSQL 15:** Debian `postgresql` (`15.19`), `-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_qa_walk?sslmode=disable`, freshly dropped and recreated before every script (`GRAIN_REQUIRE_POSTGRES=1` set for all Go test runs).
- **Browser & viewports:** Headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `@playwright/test` 1.62.1), verified at `1440×900` desktop and `390×844` phone in both light and dark themes, plus `make ui-screenshots PAGES=/next/settings/tasks`.

### Stubbed boundaries

Only boundaries unavailable inside the isolated sandbox VM were stubbed:

1. **GitHub API & git remote (`ghsim.go`):** wraps `pkg/github/githubsim` + `git http-backend` on `127.0.0.1:4285` over a local bare `acme/widgets.git` repository.
2. **Model CLI (`fake-agy.py`):** scripted Antigravity (`-agy-path`) executable that connects to the **real** per-run `grain mcpserver` over stdio JSON-RPC to invoke `nothing_needed`, `comment_on_issue`, or `run_command` (`git commit && git push` to open pull requests).
3. **CI workflow (`.grain/workflows/ci.yml`):** lightweight passing `unit` job seeded on `acme/widgets`'s `main` branch and executed by the daemon's real in-process grain CI runner.
4. **Model API key file:** placeholder credential file (`walkthrough-gemini-key`); no external LLM endpoint is contacted.

---

## Steps and verdicts

### Item 1: Off really is off

Script: [`item1_off_by_default.sh`](2026-11-13-qa-control-plane/item1_off_by_default.sh) ([`out/item1-sqlite.out`](2026-11-13-qa-control-plane/out/item1-sqlite.out), [`out/item1-postgres.out`](2026-11-13-qa-control-plane/out/item1-postgres.out)).

1. Started a fresh daemon with QA untouched (`GET /api/settings` reported `qaEnabled: false, qaFlowId: "golden-v1-minimal", qaRolloutPct: 0, qaShadowOnControl: true, qaEpoch: 1, qaRegimeId: 1`).
2. Filed, approved, and ran **5 tasks** to completion (3 closing via `nothing_needed`, 2 committing and pushing branches to open pull requests).
3. Verified on both **SQLite** and **PostgreSQL**:
   - `grain_config.qa` remained `""`.
   - `task_qa_assignment`, `task_qa_check_run`, and `task_qa_finding` all held **`0` rows**.
   - Neither `<dataDir>/perf/qa-checks.jsonl` nor `~/.perf/grain-qa-checks.jsonl` was created.
4. Ran the identical 5-task workload against baseline `grain-24d0eba1` (schema 139):
   - Core task-lifecycle table row deltas were identical on both binaries (`task`: `1 → 6`, `task_run`: `0 → 5`, `task_observation`: `1 → 6`).
   - On PostgreSQL (`pg_stat_user_tables` summed over `task`, `task_run`, `task_comment`, `task_link`, `task_observation`), tuple writes went from `3 ins / 0 upd / 0 del` before the 5 dispatches to **`23 ins / 93 upd / 0 del`** after on `24d0eba10` and **`23 ins / 93 upd / 0 del`** on `HEAD` — **zero extra dispatch writes** (`+4.0` inserts and `+18.6` updates per dispatch on both). Whole-store `cycles.commits` grew slightly between `24d0eba10` and `HEAD` (`69 → 87` on Postgres, `80 → 94` on SQLite) solely from `integration_event` rows added in PR #1233 (schema 140), while all `task_qa_*` tables recorded `0` writes.

### Item 2: CLI round trip (`grain qa status|enable|disable`)

Script: [`item2_cli_roundtrip.sh`](2026-11-13-qa-control-plane/item2_cli_roundtrip.sh) ([`out/item2-sqlite.out`](2026-11-13-qa-control-plane/out/item2-sqlite.out), [`out/item2-postgres.out`](2026-11-13-qa-control-plane/out/item2-postgres.out)).

1. **Fresh status:** `grain qa status` and `grain --json qa` reported `off`, `flowId: "golden-v1-minimal"`, `rolloutPct: 0`, `shadowOnControl: true`, `epoch: 1`, `regimeId: 1`.
2. **Enable & flag preservation:**
   - `grain qa enable -flow golden-v1-minimal -rollout 30` → `enabled: true, rolloutPct: 30, epoch: 1, regimeId: 1`.
   - `grain qa enable -rollout 50` → preserved `flowId: "golden-v1-minimal"`, `shadowOnControl: true`, `epoch: 1`, and bumped `regimeId: 2`.
   - `grain qa enable -flow repo-custom -shadow-control=false -epoch 3` followed by `grain qa enable -rollout 65` → preserved `repo-custom`, `shadowOnControl: false`, and `epoch: 3` (`regimeId: 4`).
   - Also tested `0%` shadow-only rollout preservation across `disable` → bare `enable`, which exposed **F1** (fixed below).
3. **Epoch monotonicity & range refusals:**
   - `-epoch 0` and `-epoch -2`: refused (`rc=2`, `qaEpoch must be at least 1`).
   - `-epoch 2` when stored epoch was `3`: refused (`rc=2`, `qa: epoch cannot decrease from 3 to 2`).
   - `-rollout 101` and `-rollout -1`: refused (`rc=2`, `qa: rolloutPct must be between 0 and 100`).
   - `-flow nonexistent-flow`: refused (`rc=2`, `qa: flowId must be one of golden-v1-minimal, deterministic-s3-only, repo-custom`).
   - Confirmed `config_audit` row count was unchanged across all 6 refused invocations.
4. **Disable stops assignment while preserving settings:**
   - Dispatched 1 task while enabled → `task_qa_assignment` = `1` row, `perf/qa-checks.jsonl` = `1` sample.
   - Ran `grain qa disable` → `enabled: false`, keeping `flowId: "repo-custom"`, `rolloutPct: 65`, `shadowOnControl: false`, `epoch: 3` (`regimeId: 5`).
   - Dispatched another task with a fresh origin → `task_qa_assignment` stayed at `1` row and `perf/qa-checks.jsonl` stayed at `1` line.
5. **Audit log, state-repo history & restart survival:**
   - All 5 accepted CLI mutations recorded `surface = 'settings'` entries in `config_audit` (`GET /api/deployment/config-audit`) and created 5 git commits touching `tables/grain_config.json` in `<dataDir>/state-repo`.
   - Restarted `grain daemon` over the same SQLite and PostgreSQL stores; `grain --json qa status` matched byte-for-byte after restart.

### Item 3: Sticky work-tree arm assignment

Script: [`item3_sticky_assignment.sh`](2026-11-13-qa-control-plane/item3_sticky_assignment.sh) ([`out/item3-sqlite.out`](2026-11-13-qa-control-plane/out/item3-sqlite.out), [`out/item3-postgres.out`](2026-11-13-qa-control-plane/out/item3-postgres.out)).

1. Enabled QA at `rolloutPct: 30, epoch: 1` and filed + dispatched **50 tasks** grouped into **20 distinct work-tree root origins**:
   - 10 GitHub origins (`acme/widgets#201` .. `acme/widgets#210`), 3 tasks each (30 tasks).
   - 10 grain task-tree origins (`task:<root>` with root + child naming `origin: "task <root>"`), 2 tasks each (20 tasks).
   - Also tested pre-origin child tasks (`origin_ref = ''` with `LinkProposedBy`), which exposed **F2** (fixed below).
2. **Stickiness & treatment calibration (both stores):**
   - `task_qa_assignment` recorded **20** rows for `epoch = 1` (**7 `treatment` / 13 `control` = 35%**, well within binomial tolerance of the 30% target).
   - `perf/qa-checks.jsonl` recorded 50 `S0` `arm-assign` samples with **0 mismatches** between any member task's resolved arm and its root origin's stored arm.
3. **Retry stickiness across rollout changes:**
   - Dispatched a task (`origin: "acme/widgets#999"`), which received `control` on attempt 1.
   - Flipped rollout to the opposite extreme (`100%`) in `epoch = 1`, reopened the task, and dispatched attempt 2: both runs recorded `control` (`runs=2, distinct_arms=[control]`).
4. **Epoch re-randomization:**
   - Bumped to `-epoch 2` at `30%` rollout and dispatched 10 tasks across `acme/widgets#201..210`: `task_qa_assignment` recorded 10 new `epoch = 2` rows while preserving `epoch = 1` rows, and **6 of 10** origins flipped arm between epochs 1 and 2.

### Item 4: `/next/` Settings → Tasks card and API agreement

Scripts: [`item4_ui_and_api.sh`](2026-11-13-qa-control-plane/item4_ui_and_api.sh) & [`item4_ui.mjs`](2026-11-13-qa-control-plane/item4_ui.mjs) ([`out/item4-sqlite.out`](2026-11-13-qa-control-plane/out/item4-sqlite.out), [`out/item4-postgres.out`](2026-11-13-qa-control-plane/out/item4-postgres.out)); Playwright spec [`ui-ng/e2e/next-qa-settings.spec.js`](../../ui-ng/e2e/next-qa-settings.spec.js).

1. **Bidirectional CLI ↔ UI agreement:**
   - Fresh daemon: `/next/settings/tasks` rendered `Enable quality control engine` unchecked (`aria-checked="false"`), `golden-v1-minimal`, `0%`, and all dependent inputs disabled, matching `grain qa status`.
   - After `grain qa enable -flow repo-custom -rollout 42 -shadow-control=false`, reloading `/next/settings/tasks` displayed `repo-custom`, `42`, and `shadow=false`.
   - Selecting `deterministic-s3-only`, typing `68`, enabling shadow scoring, and clicking `Save changes` updated `grain --json qa status` to `enabled: true, flowId: "deterministic-s3-only", rolloutPct: 68, shadowOnControl: true`.
2. **Inline boundary validation:**
   - Entering `""`, `"101"`, `"-5"`, and `"12.5"` into `PR rollout (%)` preserved the typed text without DOM stomping, displayed `error-qa-rollout-pct`, marked `aria-invalid="true"`, and disabled `Save changes`. Clicking `Discard` restored `"42"` and hid the save bar. A forced `POST /api/settings` with `{"qaRolloutPct": 101}` returned `400 Bad Request`.
3. **Viewer read-only banner & forced POST 403:**
   - Minted a real `gma_…` bearer token with `scopeSet: "grain/viewer"` on the running daemon (exposing **F3** before the fix).
   - After fixing **F3**, opening `/next/settings/tasks` with that real `grain/viewer` token (no browser route stubs) rendered the `Read-only` pill (`section-role-readonly-pill`), the `settings-role-readonly` banner naming `settings:write` / `grain/viewer`, and disabled all QA switches and inputs. Forced `POST /api/settings` and `PUT /api/settings` calls returned **`403 Forbidden`** naming `settings:write` and left stored settings untouched.
4. **Two-tab concurrent non-stomp check:**
   - Opened Tab A and Tab B simultaneously on `/next/settings/tasks`. Tab A changed `qaFlowId` → `golden-v1-minimal` and saved; Tab B (without reloading) changed `qaRolloutPct` → `25` and saved. Because `SettingsStateService` sends partial per-field drafts, both edits survived (`flowId: "golden-v1-minimal", rolloutPct: 25`), and Tab B updated its displayed flow pill from the save response. Verified the same across cards (`maxAutoSubmitDepth: 7` in Tab A + `qaShadowOnControl: false` in Tab B).
5. **Visual & accessibility pass:**
   - Rendered `/next/settings/tasks` at `1440×900` and `390×844` in light and dark themes (`make ui-screenshots PAGES=/next/settings/tasks OUT=screenshots/qa-control-plane` and `next-qa-settings.spec.js` + `next-full-system.spec.js`): zero serious/critical axe violations and zero horizontal overflow (`scrollWidth <= clientWidth`).

### Item 5: Schema 139 → 146 upgrade and state-repo round trip

Script: [`item5_upgrade_139_to_146.sh`](2026-11-13-qa-control-plane/item5_upgrade_139_to_146.sh) ([`out/item5-sqlite.out`](2026-11-13-qa-control-plane/out/item5-sqlite.out), [`out/item5-postgres.out`](2026-11-13-qa-control-plane/out/item5-postgres.out)).

1. Initialized real schema-139 SQLite and PostgreSQL stores using `grain-24d0eba1`, seeded template + schedule + qualification + release rows (exercising Go migrations 142, 144, and 145 alongside SQL migrations 140, 141, 143, and 146) with QA unset, and stopped the daemon.
2. Started `HEAD`'s `grain daemon` against the schema-139 stores:
   - Upgraded cleanly to `grain_schema = 146` with `grain_config.qa = ''` and QA off.
   - `grain state sync` + `grain state check` passed (`schema: 146, which is what this build of grain knows`).
3. Enabled QA (`deterministic-s3-only`, `35%`, `shadow=false`, `epoch=2`), dispatched a task to populate `task_qa_assignment`, and inserted rows into `task_qa_check_run` and `task_qa_finding`.
4. Ran `grain state sync` + `grain state check`:
   - `grain state check` passed on the valid export and **refused** (`rc=1`, `rolloutPct must be between 0 and 100`) a mutated copy with `"rolloutPct": 999` in `tables/grain_config.json`.
   - Confirmed `tables/grain_config.json` carried `qa` (`{"enabled":true,"flowId":"deterministic-s3-only","rolloutPct":35,"shadowOnControl":false,"epoch":2,"regimeId":1}`), while `task_qa_assignment.json`, `task_qa_check_run.json`, and `task_qa_finding.json` were **absent** from `state-repo/tables/`.
5. Imported `state-repo` via `grain state restore-snapshot` into a fresh SQLite store and a fresh PostgreSQL database (`grain_qa_walk_imp`): `grain --json qa status` restored the exact QA configuration while all three `task_qa_*` tables had `0` rows.

---

## Findings and fixes

Deterministic reproduction script covering F1–F3 on both SQLite and PostgreSQL: [`repro_findings.sh`](2026-11-13-qa-control-plane/repro_findings.sh) (pre-fix output in [`out/before/`](2026-11-13-qa-control-plane/out/before/), post-fix output in [`out/after/`](2026-11-13-qa-control-plane/out/after/)).

### F1: Bare `grain qa enable` clobbered an explicitly configured `0%` shadow-only rollout after `grain qa disable`

- **Reproduction:**
  ```sh
  grain qa enable -flow deterministic-s3-only -rollout 0
  grain qa disable
  grain --json qa enable
  ```
  Before the fix, `rolloutPct` silently jumped from `0` to `30` on both SQLite and PostgreSQL, routing 30% of PRs into active treatment when the operator had configured `0%` passive shadow evaluation.
- **Root cause:** `cmdQA` (`pkg/cli/qa.go`) inferred "QA has never been configured" from `current.QARolloutPct == 0 && !current.QAEnabled`, which is indistinguishable from an operator who explicitly configured `0%` rollout and later ran `grain qa disable`.
- **Fix:** Exposed `QAConfigured bool` (`!cfg.QA.IsZero()`) on `ui.Settings` (`pkg/ui/settings.go`) and gated `grain qa enable`'s default `30%` rollout on `!current.QAConfigured` (`pkg/cli/qa.go`).
- **Regression tests:** `TestQAEnablePreservesExplicitZeroPercentShadowOnlyRolloutAcrossDisable` (`pkg/cli/qa_test.go`) and `TestQASettingsShipOffPlanCleanlyAndBumpRegimeOnRolloutChange` (`pkg/ui/qa_settings_test.go`).

### F2: `resolveTaskQAAssignment` split originless child/review tasks off from their work tree's sticky arm

- **Reproduction:** Seed a pre-origin / updated-link child task (`task.WorkOrigin == ""`, `LinkProposedBy -> <root>`) whose standalone `"task:<child>"` bucket falls on the opposite side of `qaRolloutPct` from `"task:<root>"`. Before the fix, `GET /api/tasks/<child>/tree` reported `origin: "task:5"`, yet root task `5` received `treatment` while child task `6` received `control`.
- **Root cause:** `resolveTaskQAAssignment` (`pkg/orchestrator/qa.go`) passed `string(task.WorkOrigin)` straight to `qa.ResolveRootOrigin` without consulting `store.TreeOrigin(ctx, task.ID)` when `task.WorkOrigin == ""` and `task.Parent() != ""` (unlike `ui.Client.WorkTree`).
- **Fix:** When `qaCfg.Enabled && store != nil && rawOrigin == "" && task.Parent() != ""`, `resolveTaskQAAssignment` resolves `store.TreeOrigin(ctx, task.ID)`. Disabled deployments, standalone root tasks (`Parent() == ""`), and tasks with `WorkOrigin != ""` perform zero extra queries.
- **Regression test:** `TestResolveTaskQAAssignment_OriginlessChildInheritsRootTreeArm` (`pkg/orchestrator/qa_test.go`).

### F3: `GET /api/config` omitted `access` for scoped `gma_…` external tokens, leaving `/next/` Settings controls enabled for `grain/viewer` tokens

- **Reproduction:** Enable `externalAgentApi: true`, mint a `grain/viewer` token via `POST /api/external-agents/tokens`, and open `/next/settings/tasks` with `Authorization: Bearer gma_…`. Before the fix, `GET /api/config` returned `"access": null`; `AccessService` (`isFullAccess = !a || !!a.full`) treated `null` as an unlimited operator, hiding the `Read-only` pill and `settings-role-readonly` banner and leaving `switch-qa-enabled` enabled until `POST /api/settings` failed 403.
- **Root cause:** `handleConfig` populated `resp.Access` solely via `accessRoleInfo(r.Context())` (verified human IAP/SSO sessions), leaving `resp.Access = nil` inside `narrowConfigForAgentCall` for `gma_…` tokens and task agent calls.
- **Fix:** Added `agentCallAccessRoleInfo(call)` (`pkg/ui/access_roles.go`) and populated `resp.Access` in `narrowConfigForAgentCall` (`pkg/ui/tasks.go`) from `call.grants` and `call.external.ScopeSet` (`full: false`). Also updated `ui-ng/e2e/next-qa-settings.spec.js` to test a real `gma_…` `grain/viewer` token + forced 403 `POST /api/settings`, two-tab non-stomp saves, and `finally` restoration on the worker-scoped `demoServer`.
- **Regression tests:** `TestExternalViewerTokenGetsNonFullAccessOnGetConfigAnd403OnSettingsWrite` (`pkg/ui/qa_settings_test.go`) and `ui-ng/e2e/next-qa-settings.spec.js`.
