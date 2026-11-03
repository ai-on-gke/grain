# grain/config and state — repo-less config writers, system schedules, `github_workflow_run` imports, suite bases, push refusals, Postgres, retired batching — 2026-11-03

Walkthrough of [`docs/test-plans/2026-11-03.md`](../test-plans/2026-11-03.md) §9 "What is NOT proved" (PRs #506, #500, #545, #494, #479, #460, #438, #465, #443). The scripts and outputs are in [`2026-11-03-config-state/`](2026-11-03-config-state/).

**Result:** all seven items behave as their PRs describe. The walk turned up five faults on the paths around them. All five are fixed here, each with a regression test:

| # | Finding | Severity | Fix |
|---|---|---|---|
| 1 | `grain init workstation`'s starter policy names only `-repo`, so local policy holds every task that reads or writes the deployment's own `grain/config` at dispatch forever. That covers the Director and every chat given `writes: [grain/config]` (#506's whole feature). | High | `renderWorkstationPolicy` always lists `grain/config` (`pkg/cli/init_workstation.go`) |
| 2 | `propose_config_change` told the agent "It passed config validation." when validation carried warnings, such as the #500 `system:*` schedule warning. The person's card showed the warning and the agent never heard it. | Medium | The result relays the warnings (`pkg/mcp/config_change_tool.go`) |
| 3 | A repo-less config writer's prompt contradicts itself. The write-targets paragraph says "You cannot push to those [grain/config] … call `propose_task`", right beside the grain-state note that says to edit the checkout and call `propose_config_change`. | Medium | That paragraph leaves `grain/config` out for a task given the writable state checkout (`pkg/orchestrator/run.go`) |
| 4 | A proposal the state manager refuses (nothing changed against main, not pushed, moved, no shared history) came back to the agent as `grain server: internal server error (ref …)`, a 500. The reason was only in the daemon log. | Low | The refusals wrap `ui.ErrConfigChangeRefused`, answered 400 with the text (`pkg/daemon/config_change.go`, `pkg/ui/task_config_change.go`) |
| 5 | The infrastructure review's prompt still lists `merge_batch_size` as an in-scope dial after #443 retired batching. `ValidateTuningParameter` refuses it as a recommendation target. | Low | Dropped from the scope paragraph (`pkg/model/system_template.go`) |

Not fixed, recorded for follow-up (see "Left open"): the Director on a workstation is still held by the starter policy, for a capability; a policy hold reads as `queued/ready` in the task's reason; and `PUT /api/settings` drops the retired merge-batching fields with a 200 and no notice.

## Environment

- **Build:** `go build -o /home/debian/bin/grain ./cmd/grain` at this branch, after `cd ui && npm ci && npm run build` so the binary embeds the real UI. Two older builds came from worktrees:
  - `grain-s85`: `75331e14`, the audited range's start, schema 85.
  - `grain-v310`: `v3.1.0`. It turned out to be at schema **87**, not 85 as the plan's header implies, so `grain-s85` was used for the upgrade items.
- **Real daemon (items 1, 2, 3, 5, 7):**
  - Set up by `grain init workstation -non-interactive -repo acme/widgets -sandbox-backend host -model-backend claude -ui-addr 127.0.0.1:18702 -start-service=false` under `HOME=/home/debian/live`. That gives an internal `grain/config`, served by the daemon's own git proxy.
  - Started by [`run-daemon.sh`](2026-11-03-config-state/run-daemon.sh) with `-sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http`.
- **Stub agent framework:** a scripted custom agent backend, [`stub-backend.py`](2026-11-03-config-state/stub-backend.py) (protocol 2, `docs/custom-agent-backends.md`), registered with `PUT /api/deployment/agent-backends/stub` (`framework: custom`).
  - For each run, the daemon starts it in the host sandbox with the real `grain mcpserver` command line.
  - The stub starts that server, records `tools/list`, and replays the `STUB-RUN:` / `STUB-CALL:` lines of the newest chat message through the real tools. Each `STUB-RUN:` goes through `run_command`, so git pushes go through the git proxy with the run's own credential.
  - Every call and result is in [`stub-calls.jsonl`](2026-11-03-config-state/stub-calls.jsonl).
  - No real model backend was used (the sandbox has no credentials for one).
- **`grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:18742` (items 4, 7):** Chromium via Playwright from `ui/` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), at 1440×1000.
- **Postgres (item 6):** Debian's `postgresql` (15) from apt, which installed in about 30 s.

## Item 1 — a non-Director chat with `writes: [grain/config]`

1. Filed two repo-less interactive tasks with `POST /api/tasks`: task 3 with `"writes":[{"repo":"grain/config"}]` and task 2 without. Neither is a console session or the Director.
2. **Finding 1.** Task 3 never dispatched: `dispatch: candidate 3 skipped: held by local policy: local policy does not allow repository grain/config on this machine` ([`policy-hold.log`](2026-11-03-config-state/policy-hold.log)).
   - The Director (task 1, `reads: [grain/config]`) was held the same way once messaged.
   - The policy `grain init` wrote was `repos: ["acme/widgets"]`. Adding `"grain/config"` and restarting let task 3 run.
   - The Director was then held for its next claim instead: `local policy does not allow capability "api:deployment:read"` (left open, below).
3. Offered tools, recorded by the stub from the real `mcpserver` on real dispatch:
   - task 3: 17 tools, **including `propose_config_change`**;
   - task 2: 16 tools, **without it**.
   - The same split shows when `grain mcpserver -sandbox-root … -server … -task N` is run by hand.
4. Task 3's prompt carried the grain-state note (`Grain checked out this deployment's state repository, grain/config, at …/runs/3-1/grain-state`). The checkout was there, with its push URL on the git proxy.
5. **Finding 3.** The same prompt also said: *"This task may land work in more than one repository … it covers grain/config (on its default branch). You cannot push to those from this sandbox … call `propose_task` with `repo` set to that owner/name"*. That is the opposite instruction, for the same repository.
6. Sent the chat [`propose-system-title.sh`](2026-11-03-config-state/propose-system-title.sh)'s turn: a `sed` on `settings/schedules.yml` in grain-state, then `propose_config_change {"summary":"Rename the infrastructure review schedule","branch":"rename-infra-review"}`.
   - The tool committed and pushed `grain/config/3/rename-infra-review` (`d2c98f0`).
   - The task's `pendingConfigChange` carried the diff, `files: [settings/schedules.yml]` and validation `ok: true` with the warning (item 2).
7. `POST /api/tasks/3/config-change/approve {"headSha":"d2c98f0…"}` merged it into grain/config `main` (`d289e69 Rename the infrastructure review schedule`), and the daemon applied it (`staterepo: wrote settings snapshot … before applying d289e690`).

## Item 2 — a `system:*` schedule edit is warned, and reverted as warned

- **The card.** `pendingConfigChange.validation.warnings` = `settings/schedules.yml (id = 'system:infra-review'): title edits content grain owns on its own schedule; those columns are a cache of the template this build ships, so the next reconcile cycle writes the template back and the change is undone rather than kept -- change the template in grain's source instead`.
- **Finding 2.** The tool result the agent got read: *"Proposed grain/config/3/rename-infra-review (d2c98f0, 1 file(s)). **It passed config validation.** The person sees the diff…"*. With the fix, the same turn on the rebuilt daemon reads *"It passed config validation with warnings, shown to the person beside the diff: settings/schedules.yml (id = 'system:infra-review'): title edits …"*.
- **Approved anyway, it was reverted on the same cycle** ([`system-schedule-revert.log`](2026-11-03-config-state/system-schedule-revert.log)). At 05:04:44 the import logged that the change touched content grain owns and said the next reconcile would write it back. In the same tick the orchestrator logged `schedule system:infra-review (Infrastructure review) said something this build does not fire (title); resynced from the template grain ships`. `GET /api/schedules` never showed the new title (polled every 5 s from approval). At 05:05:06 grain re-exported (`1183367 Update grain state`: `-title: Nightly infrastructure review (renamed)` / `+title: Suggest what to tune for throughput and reliability`).
- The import also logged `the notice above … did not reach the pull request it came in on (the config repository is controller-local …)`. On an internal config repo, the proposal card is the only place the person sees the warning, which is why finding 2 matters.

## Item 3 — `nightly-ci-review.yml` (a `github_workflow_run` trigger) round trip

[`round-trip.sh`](2026-11-03-config-state/round-trip.sh) → [`round-trip.out`](2026-11-03-config-state/round-trip.out) and [`migrate-85-dump.sh`](2026-11-03-config-state/migrate-85-dump.sh) → [`migrate-85-dump.out`](2026-11-03-config-state/migrate-85-dump.out).

- **Same schema (93):**
  1. `.grain/schedules/nightly-ci-review.yml`'s schedule was created on the real daemon: same title, base `main`, `overlap: skip`, `trigger: {github_workflow_run: {workflow: nightly.yml, conclusions: [failure, timed_out, startup_failure], branches: [main]}}`.
  2. The export wrote it under `trigger:`.
  3. `grain state check` and `grain state check -migrate` on a clone both answered `ok: grain can load this` (6 schedules).
  4. A second daemon whose fresh data dir started from a clone of that export (`staterepo: loading … importing its settings`) served `sched-1` with the same trigger.
- **From schema 85:**
  - `grain-s85` exported the same schedule, at `schema-version` 85.
  - This build's `grain state check` refused it (`repository is at schema 85, this build knows 93`, exit 1), as it should without `-migrate`.
  - `grain state check -migrate` answered `schema: 85, migrated into the 93 this build of grain knows … ok: grain can load this`. That is the refusal #545 fixed, now gone.
  - A fresh data dir seeded with the schema-85 dump is refused at start (`grain will not write over that dump: … run the build that wrote it`). That is by design; the upgrade path is in place.
  - So `grain state -data-dir <old> prepare-upgrade` (`rehearsed: 46 rows across 18 tables migrate from schema 85 into 93`, `prepared 85 93 …`) was run, then this build was started on the same data dir: `migrated the store in place from schema 85 to 93`. The schedule kept its trigger.

## Item 4 — a declared suite schedule without `base:`

[`suite-without-base.sh`](2026-11-03-config-state/suite-without-base.sh) → [`suite-without-base.out`](2026-11-03-config-state/suite-without-base.out), [`pages.mjs`](2026-11-03-config-state/pages.mjs) → [`demo/pages.json`](2026-11-03-config-state/demo/pages.json).

1. On the demo, committed `.grain/templates/nightly.yml`, `.grain/suites/sweep.yml` and `.grain/schedules/sweep.yml` (`suite: sweep`, `daily: {at: "03:00"}`, no `base:`) to `acme/widgets`'s simulated bare repository. Then turned on reading its declarations: `PUT /api/repos/acme/widgets/declarations {"declarations":true}`. The demo starts with them off, so nothing is read until then.
2. The next sync refused it. `GET /api/repos/acme/widgets/declarations` → `declarationsStatus.error` = `reading acme/widgets's declarations: .grain/schedules/sweep.yml: a schedule that runs a suite needs `base:` -- a suite run has no default branch to fall back to`.
3. **It reaches the repo page, not only the log.** `/repos/acme/widgets/settings/advanced` → Declarations shows `Last read failed: reading acme/widgets's declarations: .grain/schedules/sweep.yml: a schedule that runs a suite needs `base:` …`, naming the file ([`demo/repo-acme-widgets-advanced.png`](2026-11-03-config-state/demo/repo-acme-widgets-advanced.png)).

## Item 5 — a sandbox push to grain/config

[`push-refusal.sh`](2026-11-03-config-state/push-refusal.sh). Every push ran in task 3's own run, through `run_command` in its grain-state checkout, with its own proxy credential (outputs in [`stub-calls.jsonl`](2026-11-03-config-state/stub-calls.jsonl)).

| push | result |
|---|---|
| `git push origin HEAD:main` (on top of `origin/main`) | **refused**: `refusing to update refs/heads/main in grain/config: a sandbox may push only its own task's branch, refs/heads/grain/7e4e7587/task-3-ops-chat (push with …), and its own config proposal branches, refs/heads/grain/config/3/<name>. A change reaches grain/config's main only through propose_config_change and a person's approval` |
| `HEAD:refs/heads/grain/config/3/direct-probe` | **accepted** (`* [new branch]`) |
| `HEAD:refs/heads/grain/config/999/not-mine` | **refused**, same message |
| `HEAD:refs/heads/grain/7e4e7587/task-3-ops-chat` (the task branch the message offers) | accepted |

The first attempt at `main` was refused client-side as non-fast-forward, because the item 1 approval had moved `main`. The run above fetched and reset first, so the proxy is what refused.

## Item 6 — Postgres 85 → 93 with a pre-existing column

[`postgres-85-93.sh`](2026-11-03-config-state/postgres-85-93.sh) → [`postgres-85-93.out`](2026-11-03-config-state/postgres-85-93.out).

1. `grain-s85` ran against `postgres://…/grain85` and wrote a `github_workflow_run` schedule, a task and a comment.
2. With it stopped, two columns that 86..93 add were pre-created to simulate a partial earlier run:
   - `task.origin_actor_source`, the first of migration 91's ALTERs;
   - `task_run_capability_audit.hostd`, migration 93's last statement.
3. This build started on the same database and logged `grain: migrated the store in place from schema 85 to 93`.
4. Afterwards:
   - the schedule (with its trigger) and the comment written at 85 read back;
   - 91's other `*_source` columns, 92's `hostd` table (one local row) and 93's `task_run_hostd` exist;
   - `merge_batch` has 89's per-task shape.

## Item 7 — Settings after #443

[`pages.mjs`](2026-11-03-config-state/pages.mjs), [`recheck-labels.mjs`](2026-11-03-config-state/recheck-labels.mjs) → [`demo/pages.json`](2026-11-03-config-state/demo/pages.json), [`demo/recheck-labels.json`](2026-11-03-config-state/demo/recheck-labels.json), [`old-settings-keys.sh`](2026-11-03-config-state/old-settings-keys.sh) → [`old-settings-keys.out`](2026-11-03-config-state/old-settings-keys.out).

- **No merge-batching controls.** Settings → Engine, General, Repositories, Config repo, Models and Sandboxes have no line matching `batch|gather` ([`demo/settings-engine.png`](2026-11-03-config-state/demo/settings-engine.png)). Nor do the repo's merge-queue, advanced and CI tabs.
- **Re-check labels.** The repo overview's panel reads "merge re-checks", and its ref `grain-merge-queue/…/recheck/5/main`.
- **Old keys in a config repo:**
  - A `tables/grain_config.json` row carrying a key no build has (`merge_batching_enabled`) passes `grain state check` with `warning: tables/grain_config.json row 0 has a column "merge_batching_enabled" that grain_config does not; it would be ignored`: a notice, not an error.
  - `merge_batch_size` and `merge_gather_window_ms` are still columns of `grain_config`, are still exported, and are accepted silently, with nothing reading them.
  - `PUT /api/settings {"mergeBatchSize":5,"mergeGatherWindow":"2m"}` answers 200 and drops both with no notice.
- **Finding 5.** The infrastructure review's prompt still lists `merge_batch_size` as an in-scope dial.

## Finding 4 — a refused proposal is a 500

In the item 5 turn, an earlier version of the stub (whose newest-message marker did not match) replayed the item 1 steps first. Their `propose_config_change` under the already-approved name `rename-infra-review` came from a checkout whose `origin/main` was stale. It pushed a branch that changes nothing against `main`, and the agent was told `Pushed grain/config/3/rename-infra-review, but grain could not record it for approval: grain server: internal server error (ref 35648769deda) (500 Internal Server Error)`. The reason was in the daemon log alone: `api: 500 internal [ref 35648769deda]: branch grain/config/3/rename-infra-review changes nothing against main: edit and commit something first`.

Today's propose script fetches before it checks for changes, so this could not be forced again from a sandbox. The fix is covered by `TestProposedConfigChangeIsDiffedValidatedAndMergedAtItsHead` (each refusal is `ui.ErrConfigChangeRefused`) and `TestProposeConfigChangeAnswersARefusalAsInvalid` (a refusal is a `ValidationError` carrying its text; any other manager error is not).

## Regression tests

- `pkg/cli`: `TestTheStarterPolicyFailsClosed` now also checks that a task reading `grain/config` and a repo-less `grain/config` writer pass the starter policy, and that `grain/config` is not listed twice.
- `pkg/mcp`: `TestProposeConfigChangeRelaysValidationWarnings`.
- `pkg/orchestrator`: `TestWriteTargetsSectionLeavesTheStateCheckoutsRepoOut`.
- `pkg/daemon`: `TestProposedConfigChangeIsDiffedValidatedAndMergedAtItsHead`, extended.
- `pkg/ui`: `TestProposeConfigChangeAnswersARefusalAsInvalid`.
- `pkg/model`: `TestTheInfraReviewsScopeNamesOnlyTunableDials` checks every backticked dial in the scope paragraph against `ValidateTuningParameter`. It fails on `merge_batch_size` without the fix.

No `pkg/staterepo`, `pkg/repodecl` or `pkg/gitproxy` change was needed: everything those packages do here behaved as designed.

## Left open

- **The Director on a `grain init workstation` deployment is still held**, by `local policy does not allow capability "api:deployment:read"`. The starter policy allows only `grain-ci`. Whether init should allow the Director's own grants is a policy decision.
- **An existing workstation keeps its old policy.** "A re-run keeps it", so a deployment set up before this change needs `"grain/config"` added to `repos:` by hand.
- **A policy hold reads as `queued/ready`.** While task 3 was held by local policy, `GET /api/tasks/3` gave `reason: {"reason":"queued/ready", …, "control":"Run now"}`, so nothing told the person why it never ran.
- **Retired merge-batching settings are accepted silently.** `PUT /api/settings` drops the removed fields without a notice, and `grain_config`'s `merge_batch_size` / `merge_gather_window_ms` columns are still exported and imported with nothing reading them. That is harmless, but it is not "ignored with a notice".
- **The plan's header says `latest` (`v3.1.0`) is at schema 85.** It is at 87; 85 is `75331e14`, the range's start.
