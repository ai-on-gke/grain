# Walkthrough: token usage and budgets

- **Plan:** [`docs/test-plans/2026-11-10.md`](../test-plans/2026-11-10.md) §1, "What is NOT proved" items 1–6.
- **Date:** 2026-11-10, 07:12–08:50 UTC.
- **Task:** 3948, branch `grain/098073e4/task-3948-walkthrough-token-usage-and` on `ai-on-gke/grain`, base `main` at `c7651098` (PR #1079).
- **Scripts, SQL and captured output:** [`2026-11-10-token-usage/`](2026-11-10-token-usage/). `out/` holds every captured file named below.

## Environment

- Debian 12 sandbox, `linux/amd64`, 7 vCPU, Go `go1.26.10`, Node from the image.
- `apt-get install postgresql postgresql-client sqlite3 jq faketime`: PostgreSQL 15 on `127.0.0.1:5432` (role `debian`), SQLite 3.40.
- **Binaries:**

  | Binary | Commit | `SchemaVersion` |
  |---|---|---|
  | `grain` (HEAD) | `c7651098`, then this branch, rebuilt after each fix | 133 (`main` has moved past the plan's 131) |
  | `grain-s129` | `c08832a2`, built in a `/tmp/old129` worktree | 129 |

- **Daemons.** Real `grain daemon -sandbox-backend host -sandbox-port-base=0 -poll-interval 2s` ([`start.sh`](2026-11-10-token-usage/start.sh)). One ran over SQLite (`:4301`) and one over PostgreSQL (`-postgres-url …/tu_pg`, `:4302`). Real dispatch, real host sandboxes, and the real orchestrator path from run to `task_run_usage`.
- **Frameworks: all fake.** The sandbox has no claude, codex or Gemini credential, so every run went through [`fake-agy.sh`](2026-11-10-token-usage/fake-agy.sh): an antigravity connection whose `cliPath` is the script ([`setup.sh`](2026-11-10-token-usage/setup.sh)). It prints agy's real stream-json, with a `result.usage` block shaped like the one `pkg/agent/antigravity`'s parser reads. **claude and codex were not driven end to end.** Their parsers are covered only by `pkg/agent/{claude,codex}/tokenusage_test.go` over captured event lines (see O4).
- **No GitHub.** Every task is `noRepo: true` ([`task.sh`](2026-11-10-token-usage/task.sh)). Token accounting does not touch the forge, so no GitHub simulator or fake CI was needed for items 1–5.

## Outcome

| Item | Result |
|---|---|
| 1. Real runs record real usage (SQLite + PostgreSQL) | **Pass.** Every run's `task_run_usage` row equals the fake CLI's own report, with agy's arithmetic applied: input − cache read, output + thinking. Every row on both stores was checked by hand against `emitted.log`. Each run had **two execs**, because the stop rule resumed every background run with `--conversation`, and the row sums both (`turns = 2`). Setup-failed runs and CLI-failed runs (`fail` mode, exit 1 before any model call) record **no row**, and the API gives `usage: null`, not zero. `GET /api/tasks/{id}` per-attempt and total figures equal the rows ([`out/item1.txt`](2026-11-10-token-usage/out/item1.txt)). |
| 2. Upgrade 129 → HEAD (133) on both backends | **Pass.** `migrated the store in place from schema 129 to 133` on both. `prepare-upgrade` printed `rehearsed: 86 rows across 26 tables … prepared 129 133`, and `state check: ok` on both the in-place and the dump routes. The pre-upgrade run reads `usage: null` (task and attempt), and a post-upgrade run records `19000/570/8000, turns 2`, matching the CLI ([`out/upgrade-*`](2026-11-10-token-usage/out/)). The usage tables are runtime state and are **not** in the state dump by design; see O1. |
| 3. Learned capacity from a stub refusal | **Pass.** `refuse` mode answers agy's "Individual quota reached … Resets in 1h0m0s". It opened a pause on `agy-walk/fake-model` until 08:28:26, and `usage_capacity` recorded `70830` tokens: the sum of the window's three runs, 15530 + 19570 + 35730. `reset_at` is exactly the pause's `until_at`. `grain usage` shows `71k~` and `paused` ([`out/item1.txt`](2026-11-10-token-usage/out/item1.txt), last block). The learned window is the 5 h default rather than the 1 h the provider named (O2). |
| 4. `auto` caps dispatch | **Fail → fixed (F1).** Before the fix, `-mode auto -min-workers 2` plus a budget sustaining ≈1 dispatched **four** runs at once (`6-1…9-1`, all started 07:29:06), because the cap cached under the previous settings was still in force. After the fix, 5 × 20 s tasks ran **at most 2 at once**, and the daemon logged `usage: automatic worker cap 2 of max_workers 4 (sustainable 1)`. `-mode advisory` ran 4 at once and logged `the automatic cap is lifted`. `-mode off` reports no scopes and no estimate. `-mode auto` with no budget and no learned capacity left the cap at 4. |
| 5. Settings round trip and refusals | **Fail → fixed (F2, F3).** Every change is in `GET /api/deployment/config-audit` (`surface: usage`). Malformed tokens, negative or zero windows, negative tokens and unknown modes answer 400 with a clear message. A **budget on an unknown connection was stored** (200) (F2). The CLI **exited 1, not 2**, for malformed flags (F3). A reader (external-agent token with `deployment:read`) reads `GET /api/usage` (200), and gets **403** on PUT and exit 3 from `grain usage -mode auto`. Retention pruned the row seeded 8 days back and kept the one seeded 6 days back under `-retention-days 7` (`usage: pruned 1 run usage row(s) past retention`). |
| 6. UI | **Rendered, not inspected.** All 8 shots rendered (Settings → Models and a demo task, 1440 px and 390 px, light and dark), and the contact sheet is attached to the closing note. This sandbox's file reader returns PNGs as raw bytes, and `LOOK=1` has no Gemini key here, so nobody looked at them in this run. The empty, many-models and exhausted-budget states were not set up. See [Item 6](#item-6-ui). |

## Findings

| # | Finding | Status |
|---|---|---|
| F1 | **`auto` did not bind until the cached estimate expired.** One `usage.Cap` lives as long as the daemon (`usageCapFor`, `pkg/daemon/run.go`), and it cached `EffectiveWorkers` for a minute keyed only on `max_workers`. After `grain usage -mode auto -min-workers 2 -budget agy-walk=60000000/5h` (CLI: `effective worker cap: 2`), five approved tasks dispatched four at once ([`out/item4-before-fix-runs.txt`](2026-11-10-token-usage/out/item4-before-fix-runs.txt): `6-1…9-1` all started 07:29:06.48). The daemon logged `automatic worker cap 4 … (sustainable unbounded)` at 07:28:51, then nothing until 07:29:52. | **Fixed** (`pkg/usage/store.go`). `Cap.Workers` reads the usage settings every cycle (one row) and recomputes whenever they differ from those the cached figure was computed under. Leaving `auto` logs that the cap is lifted. Regression: `TestCapRecomputesWhenSettingsChange` keeps one `Cap` across the change. On the old code it fails with `cap a second after auto and a budget were set = 6, want the floor 2`. |
| F2 | **A budget on an unknown connection was accepted.** `grain usage -budget nosuch=1000/5h` and `PUT /api/usage/settings {"budgets":[{"connection":"nosuch",…}]}` both answered 200 and stored it. Usage is recorded under connection ids, so the budget would never count anything. | **Fixed** (`pkg/ui/tokenusage.go`): 400 `budget 1: no connection "nosuch" on this deployment (Settings -> Models lists their ids)`. A budget already stored on a connection since removed stays acceptable, so the next mode change, which resends every budget, is not refused. Regression: `TestSetUsageSettingsRefusesUnknownConnection`. |
| F3 | **`grain usage` exited 1 for malformed flags.** `-budget claude-1=abc/5h`, a negative or zero window, negative tokens and `-mode sometimes` all exited 1. The daemon's 400 for the same settings exits 2 (`pkg/cli/errors.go`: "2 the command was wrong"). Under `-mode off` the header also said `sustainable: unbounded (a connection in use has no known budget)` on a deployment that had a budget. | **Fixed** (`pkg/cli/usage_cmd.go`): the local errors are usage errors (exit 2), and off mode says `sustainable: not estimated (mode off)`. Regressions: `TestGrainUsageMalformedSettingsExitTwo` and `TestGrainUsageOffSaysNotEstimated`. |
| O1 | `usage_setting` holds the operator's budgets, mode, floor and retention. It is classified as runtime state alongside `task_run_usage` and `usage_capacity` (`pkg/staterepo/tables_internal_test.go`), so it is not in the state dump. A deployment rebuilt from its config repository loses its budgets and goes back to `advisory`. Both upgrade routes keep the rows, since the database is migrated in place. | Follow-up proposed: decide whether `usage_setting` is a settings table. This is a `pkg/staterepo/*` trust-boundary change, so it was not made here. |
| O2 | A learned capacity always takes the configured window, else the previous learned window, else 5 h (`LearnFromPause`). agy said `Resets in 1h0m0s`. `reset_at` honours that, but `window_ms` stays 18000000 (5 h). | Recorded. The window cannot be inferred from one refusal. |
| O3 | `PUT /api/usage/settings` replaces the whole settings object. A body carrying only `budgets` resets mode, floor and retention to their defaults (the audit shows `min workers 0, retention 0 days`). `grain usage` sends the merged object, so only raw API callers meet this. The plan's `GET /api/config-audit` is `GET /api/deployment/config-audit`. | Recorded. It is a PUT, and `docs/agent-api.md` calls it "Replace". |
| O4 | claude and codex were not driven by a real daemon. No credential exists in the sandbox, and only agy has a stub CLI in this walk. codex's `turn.completed` usage is read as one turn's usage. If codex reports the thread's cumulative total there after `exec resume`, a stop-rule continuation would double-count. Only a real codex can say which. | Recorded for a credentialed run. |

## How each item was run

### Item 1

1. `start.sh` (both stores), `setup.sh` (one antigravity connection `agy-walk` → `fake-agy`, `maxWorkers: 4`, `defaultModel: fake-model`, a stub key).
2. `task.sh <dir> <port> <walk> <mode> <title>` files a `noRepo` task, writes `mode-<id>`, approves it, calls `run-now` and waits for its first attempt to end. The modes were `ok`, `fail` and `slow:15`.
3. The rows were compared with the CLI's report in `emitted.log` (`seq task mode input output thinking cache_read`). For example, SQLite `3-1` is execs 3 + 4: (13000 − 4000) + (14000 − 4000) = 19000 in, and (230 + 50) + (240 + 50) = 570 out, with 8000 cache read and 2 turns. PostgreSQL `2-1` is execs 5 + 6: 23000/610.
4. The first run on SQLite (`2-1`, `2-2`) was `setup-failed`: the connection had no key yet. Neither attempt has a row.

### Item 2

[`upgrade.sh sqlite|postgres`](2026-11-10-token-usage/upgrade.sh) does the following:

1. Seeds with `grain-s129`, including one real run.
2. Copies the store and opens the copy with HEAD in place (log, the old task through the API, a new task, `state sync` + `state check`).
3. Takes a second copy through `grain state prepare-upgrade`, starts HEAD on it, reads the old task again, and runs `state check`.

### Item 3

`task.sh … refuse` (SQLite, task 6), then `usage_capacity`, `usage_limit_pause` and `grain usage`.

### Item 4 (PostgreSQL)

[`item4.sh <dir> <port> <walk> <cli> <n> <secs>`](2026-11-10-token-usage/item4.sh) files *n* slow tasks, approves them all without `run-now`, and samples the live fake CLI processes once a second. The budget was calibrated with `grain usage -budget agy-walk=<n>/5h` until the connection scope sustained ≈1, which happened at 18M. The steps:

1. `grain usage -budget agy-walk=18000000/5h -mode auto -min-workers 2`, then `item4.sh … 5 20`: `max concurrent: 2` ([`out/item4-auto.txt`](2026-11-10-token-usage/out/item4-auto.txt)).
2. `grain usage -mode advisory`, then `item4.sh … 5 15`: `max concurrent: 4` ([`out/item4-advisory.txt`](2026-11-10-token-usage/out/item4-advisory.txt)).
3. `grain usage -mode off` ([`out/item4-off.txt`](2026-11-10-token-usage/out/item4-off.txt)).

The pre-fix run is kept as [`out/item4-before-fix-runs.txt`](2026-11-10-token-usage/out/item4-before-fix-runs.txt). Its sampler output was dropped as unusable, because `pgrep -f` matched the sampler itself; `item4.sh` now anchors the pattern.

### Item 5 (SQLite)

- [`out/item5.txt`](2026-11-10-token-usage/out/item5.txt): before the fixes.
- [`out/item5-after-fix.txt`](2026-11-10-token-usage/out/item5-after-fix.txt): the fixed build, plus the audit rows.
- [`out/item5-reader.txt`](2026-11-10-token-usage/out/item5-reader.txt): `POST /api/external-agents/tokens {"scopes":["deployment:read"]}`, with `externalAgentApi: true`.
- [`out/item5-retention.txt`](2026-11-10-token-usage/out/item5-retention.txt): [`retention-seed.sql`](2026-11-10-token-usage/retention-seed.sql) was written with the daemon stopped, then the daemon restarted. `faketime` was installed but not needed: seeding `finished_at`, which the plan allows, was enough, and `Cap` prunes on its first cycle after a start.

### Item 6: UI

`make ui-screenshots PAGES=/settings/models,/tasks/2 OUT=screenshots/walk` was run over `grain demo`, which seeds usage, a configured budget and a learned capacity (`pkg/demo/demo_token_usage.go`), at 1440 px and 390 px in light and dark. This branch makes no `ui/` change, so there is no before/after pair. The shots were rendered but not inspected (see the Outcome table), so item 6 still needs a person, or a run that can view images, to look at the attached contact sheet. The layout itself stays pinned by `ui/e2e/usage-settings-layout.spec.js`.

## Reproduce

```sh
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client sqlite3 jq faketime
cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain
git worktree add /tmp/old129 c08832a2 && (cd /tmp/old129 && go build -o /home/debian/bin/grain-s129 ./cmd/grain)
mkdir -p /home/debian/walk/runs && cp docs/test-runs/2026-11-10-token-usage/fake-agy.sh /home/debian/walk/fake-agy
T=docs/test-runs/2026-11-10-token-usage
$T/start.sh /home/debian/bin/grain /home/debian/walk/sq 4301 && $T/setup.sh /home/debian/walk/sq 4301
$T/task.sh /home/debian/walk/sq 4301 /home/debian/walk ok "item1 ok"      # item 1
$T/task.sh /home/debian/walk/sq 4301 /home/debian/walk refuse "item3"     # item 3
$T/upgrade.sh sqlite; $T/upgrade.sh postgres                              # item 2
$T/item4.sh /home/debian/walk/pg 4302 /home/debian/walk/pgw fake-agy-pg 5 20   # item 4, after `grain usage -mode auto …`
go test ./pkg/usage ./pkg/cli ./pkg/ui -run 'Cap|GrainUsage|SetUsageSettings'
```
