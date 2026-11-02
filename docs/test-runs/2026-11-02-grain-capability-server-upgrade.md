# Test run — 2026-11-02 §3: the in-process `grain` capability server and the schema 78 → 86 upgrade

Plan: [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §3 (PRs #311, #385, #406, #375, #389, #374, #387, #365). Task 2445, filed by task 2399.

## Environment

| | |
|---|---|
| Old build | `ea9bceb0` (schema 78), built from `git worktree add /var/tmp/old ea9bceb0` |
| New build | `main` at `8726f7f7` (schema **86**: `main` had moved past the 84 the brief names, so migrations 85 `git-push-any-ref` and 86 `corp-research` ran too) |
| Daemon | a real `grain daemon -data-dir … -sandbox-port-base=0 -container-sandboxes -container-git-proxy-host <docker0>`, no GitHub (`-github-host 127.0.0.1:1`) |
| DB engines | SQLite (embedded), then Postgres 15 (`apt-get install postgresql`, `-postgres-url`) |
| Run host | Docker. The image was `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` plus `main`'s `cmd/grain-sandbox` copied to `/usr/local/bin/grain`; the published image carries no sandbox `grain` CLI (see "Not run / environment notes") |
| Agent | a stub `claude` CLI (`-model-backend claude -claude-path <stub> -claude-run host`, with a dummy OAuth token and an agent backend added through `PUT /api/settings`). It speaks stdio MCP to the `grain mcpserver` grain stages, lists the tools, calls them, and records every result. No real model credentials were used |
| UI | `cd ui && npm ci && npm run build`, `main` rebuilt with the UI embedded, `make ui-screenshots URL=… CODE=…` against the **upgraded** daemon |

## Seeding the schema-78 store

These went in through the old daemon's API:
- tasks 2–7, one per retired name;
- task 8 holding all six;
- task 9 holding `deployment-health` + `api:deployment:read`;
- `template-1` and `sched-1` with all six;
- `acme/widgets` `default_capabilities` = `grain-log,db-read`;
- a catalogue (`PUT /api/capability-policy`) restricting `in_process.{self-repair,db-write,db-read,grain-log,deployment-health}` to `repos: [acme/other]`.

The old API refuses in-process names in `ci_capabilities` ("capability "grain-log" is not supported for CI"). So `repo_config.ci_capabilities` (all six) and `grain_config.default_capabilities` (`deployment-health,self-repair`) were written with `sqlite3`/`psql` while the daemon was stopped, as a store from before that check would hold them. The old daemon was then restarted once so its state-repo dump (`tables/repo_config.json`, `settings/capabilities.yml`) carried them, and stopped. Postgres was seeded the same way.

## Before / after (SQLite; Postgres identical)

Read with `sqlite3`/`psql` before and after `main`'s first start. The daemon logged `migrated the store in place from schema 78 to 86` and `the config repository's dump was rewritten at schema 86`. The Postgres before/after snapshots `diff` empty against SQLite's.

| Column | Before (78) | After (86) |
|---|---|---|
| `task_grant` 2 | `grain-log` | `grain#journal={}` |
| `task_grant` 3 | `deployment-health` | `api:deployment:read`, `grain#health={}` |
| `task_grant` 4 | `db-read` | `grain#sql={"write":false}` |
| `task_grant` 5 | `db-write` | `grain#sql={"write":true}` |
| `task_grant` 6 | `self-repair` | `grain#host-exec={}` |
| `task_grant` 7 | `git-push-any-ref` | `repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}` |
| `task_grant` 8 | all six | all seven grants above, once each |
| `task_grant` 9 | `deployment-health`, `api:deployment:read` | `api:deployment:read`, `grain#health={}` (no duplicate) |
| `template_grant` template-1 | all six | all seven |
| `schedule_grant` sched-1 | all six | all seven |
| `grain_config.default_capabilities` | `deployment-health,self-repair` | `api:deployment:read,grain#health={},grain#host-exec={}` |
| `repo_config.default_capabilities` | `grain-log,db-read` | `grain#journal={},grain#sql={"write":false}` |
| `repo_config.ci_capabilities` | `grain-log,…,git-push-any-ref` (CSV) | JSON array of the seven (JSON because `repo#write`'s grant holds commas; it reads back through `GET …/ci-capabilities` correctly) |
| `persistent_sandbox.capabilities` | `["db-read","self-repair"]` | unchanged: nothing in `main` reads this column (`pkg/daemon/persistent_sandboxes.go` never uses it), so it carries no grant |
| `grain_config.capability_policy` | `in_process` keyed by the retired names | unchanged, by design: the catalogue binds through `judgeRetiredInProcess` (below) |

(`grain#…` is short for `https://grain.dev/capabilities/grain#…`, and `repo#write` for `https://grain.dev/capabilities/repo#write`.)

## Each method from a real sandbox run

Each task was person-filed with `noRepo`, approved, and dispatched by the orchestrator into a Docker sandbox. The stub ran `grain …` through `run_command` inside that sandbox and called `execute_grain_db` over MCP.

| Task holds | `grain read deploy://grain.log` | `grain read deploy://health` | `grain db query SELECT…` | `grain db query UPDATE…` | `execute_grain_db` | extra MCP tools |
|---|---|---|---|---|---|---|
| `grain#journal={}` | read (`deploy://` lists `grain.log`; "no lines in that window": the daemon was not under systemd) | forbidden | forbidden | forbidden | unknown tool | none |
| `grain#health={}` + `api:deployment:read` | forbidden | **live host/sandbox probes** (`loadAverage1`, `cpus: 7`, `memoryTotalMB: 15992`, disks …) | forbidden | forbidden | unknown tool | none |
| `grain#sql={"write":false}` | forbidden | forbidden | **`{"n": 36}`** | **rejected**: read-only | unknown tool | none |
| `grain#sql={"write":true}` | forbidden | forbidden | `{"n": 36}` | rejected (the CLI path is read-only by design) | **`Statement executed successfully: rowsAffected 0`** | `execute_grain_db` |
| nothing | forbidden | forbidden | forbidden | forbidden | unknown tool | none |
| `grain#host-exec` (interactive) | — | — | — | — | — | `chat` only: **no `run_host_command`** (finding 4) |

The "forbidden" refusals named the retired capabilities: `that tool belongs to the "grain-log" capability`, `"deployment-health"`, `"db-read"`, and `… permitted with db-read capability`. That is finding 3, fixed here.

## Person-only grants on the filing surfaces

On the upgraded daemon:
- **Agent API**: an external agent token (`tasks:write`, then `templates:write`) calling `/mcp` `api_create_task` and `api_create_template`. Refused with 403 `… is a dangerous grant: only a person may grant it` for:
  - `grain#sql={"write":true}` and `grain#sql={ "write" : true }`;
  - `grain#sql={"write":false,"write":true}`;
  - `grain#host-exec={}`, bare `grain#host-exec`, and `grain#host-exec={"x":1}`;
  - bare `db-write`, ` db-write ` and `self-repair` (each canonicalised first).

  `{"write":"true"}`, `{"write":1}`, case variants and a trailing `/` were refused as grants the agent's own task does not hold. A person filing `{"write":"true"}` gets `args.write: must be of type boolean`, so no string spelling is read as write.
- **A person**, through the browser API: `POST /api/tasks` with `grain#sql={"write":true}` / `grain#host-exec` was refused by the catalogue while it was in force. With the catalogue lifted, it filed tasks 10 and 11, both holding the grant.
- **Inherited defaults**: a task an external agent files with no capabilities inherits the deployment's migrated defaults, `grain#host-exec={}` included (`via = default`). The default was set by a person, it was `self-repair` (equally person-only) before the upgrade, and the task is `proposed` until a person approves it. That is the same behaviour as before, recorded here because the default is now visibly a dangerous method.
- **`propose_task` from a run**: a run holding `#sql` write and `#host-exec` proposed `#sql` write, `#host-exec`, `db-write`, `self-repair` and `#sql` read. Every call answered "Recorded … unapproved until a human approves it". The resulting proposals were **not observed**: the stub's run parked `awaiting_reply` instead of finishing, because the claude framework learns run-ending calls from stream-json `tool_use` events the stub did not yet emit. The run that would have fixed that was cut off by a daemon restart (see "Not run").
- **Reviews**: not run.

## Catalogue binding across the upgrade

`CapabilityCatalogue.Refusals` on the store before (old build, old names) and after (new build) the upgrade, for `acme/widgets` and `acme/other`. The live daemon gave the same answers on `POST /api/tasks`.

- Before: each retired name is refused on `acme/widgets` and admitted on `acme/other`.
- After: every spelling is refused on `acme/widgets` with `… (in_process.<old name>, the capability it replaced)` and admitted on `acme/other`. That covers `grain#host-exec={}`, bare `grain#host-exec`, `grain#host-exec={ }`, `grain#sql={"write":true}`, `{ "write" : true }`, `{"write":false}`, `grain#journal` and `grain#journal={}`, and `grain#health` and `grain#health={}`.
- **Difference (finding 5):** after the upgrade `in_process.deployment-health` also refuses a stand-alone `api:deployment:read` (task 9 held it on its own before the upgrade), which the old build admitted. It refuses more, never less.

## Run-host local policy files (`pkg/policy`, PR #406)

`policy.Parse` + `Check` against the rewritten grants:
- `deny: [git-push-any-ref]` and `deny: ['git-*']` refuse `repo#write={…}` and nothing else.
- `deny: [self-repair]` refuses both `grain#host-exec` spellings.
- `deny: [db-write]` refuses every `grain#sql` (the whole method, as documented).
- `deny: [deployment-health]` refuses `grain#health={}` and `api:deployment:read`.
- `allow: [db-read]` admits exactly `grain#sql={"write":false}` and refuses `{"write":true}`, `{ "write" : true }` and everything else.

Each refusal names the capability (`local policy forbids capability "…"`). This was not run through a live run-host dispatch.

## State-repo dump across the upgrade

- **In place (PR #387):** `main` started on the old data dir migrated the store and rewrote the dump at schema 86 (prior dump kept as the preceding commit). `settings/capabilities.yml` then carries the rewritten defaults and repo lists, while `in_process` keeps the retired keys, which still bind as above. Same on Postgres.
- **Into a fresh store:** copying the schema-78 dump into an empty data dir and starting `main` is refused: `the state repository holds a dump this build cannot read … repository is at schema 78, this build knows 86 … grain will not write over that dump`, pointing at `grain state reformat` / `prepare-upgrade`. That is the documented behaviour, not a bug.

## Settings → Capabilities and the task chips

Shot with `make ui-screenshots PAGES=/settings/capabilities,/tasks/8,/tasks/9 URL=<upgraded daemon> CODE=…` at 1440×900 and 390×844, light and dark (12 shots and a contact sheet). The rendered text was checked with Playwright at each width and theme, since this sandbox cannot render the PNGs:
- The task chips read `grain › journal`, `api: deployment:read`, `grain › health`, `grain › sql`, `grain › sql · write: true`, `grain › host-exec` and `repo › write · force: true, refs: …`. No retired id appears except in my own seed titles ("seed grain-log").
- `scrollWidth - clientWidth` is 0 on every page at both widths.
- **Settings → Capabilities names no retired id, but none of the new methods either** (finding 6).

## Findings

1. **Postgres: a re-run `ADD COLUMN` aborts the migration transaction.** *Fixed (c0d13bc7).*
   - Cause: `migrateSchema` passes over an `ADD COLUMN` whose column exists, but on Postgres the failed statement poisons the transaction, so the next statement fails instead.
   - Reproduction: `pkg/model/postgres/schema78_upgrade_test.go` stamps a fresh store `version = 78` and calls `Init`. It failed with `migrating schema to 80 (…): task_grant: dropping grain-log …: current transaction is aborted (SQLSTATE 25P02)`, where 79's `pending_config_change` was the column that already existed. SQLite is not affected.
   - Fix: the tolerated `ADD COLUMN` runs under `SAVEPOINT grain_add_column` and is rolled back to it.
   - The live 78→86 Postgres upgrade was not affected (no column pre-existed). Any re-run migration over an existing column would have been.
2. **`execute_grain_db`'s "missing grant" message never fired.** *Fixed (41dfe0be).*
   - Cause: `pkg/mcp/toolnames.go` keyed it as `exec_grain_db`.
   - Reproduction: a run holding `grain#sql={"write":false}` calls `execute_grain_db` and gets `unknown tool execute_grain_db`, as tasks 29/34 showed.
   - Regression test: `pkg/capability/dbaccess/toolnames_test.go` checks each `dbaccess.Tools` name against `mcp.CapabilityForTool`, and the refusal text.
3. **The sandbox CLI's refusals named retired capabilities.** *Fixed (9198a927).*
   - `deploy://grain.log`, `deploy://health`, `deploy://state` and `grain db query` now name `https://grain.dev/capabilities/grain#journal` / `#health` / `#sql`.
   - The read-only rejection now says `… permitted without https://grain.dev/capabilities/grain#sql={"write":true}`.
   - Reproduction: any run without the grant running `grain read deploy://health`. The tests that pinned the old names (`pkg/namespace`, `pkg/gitproxy`, `pkg/daemon`) now pin the new ones.
4. **`grain#host-exec` gives no agent `run_host_command`.** *Follow-up filed.* `run_host_command` is built only through `orchestrator.Config.GrantTools`, i.e. `agent.RunConfig.Tools`, which no remaining framework reads. The orchestrator's own comment says "These tools reach no agent at present", and `ea9bceb0` carries the same comment, so this predates the upgrade.
   - Reproduction: an interactive task holding `grain#host-exec` dispatched on claude lists `chat` but no `run_host_command` (task 26).
   - So the per-command chat confirmation could not be exercised live, and the plan's "still offered to an interactive task" does not hold for any CLI framework.
5. **An `in_process.deployment-health` restriction now also refuses a stand-alone `api:deployment:read`.** *Follow-up filed (design question).* `judgeRetiredInProcess` applies the retired entry to every grant the name became, so a grant that predates and is independent of `deployment-health` is newly refused. The direction is safe: it refuses more.
6. **Settings → Capabilities shows neither `grain` nor `repo`.** *Follow-up filed.* `/api/capability-servers` lists only the six configured subprocess servers. `/api/capability-methods` lists `grain#health`, `#host-exec`, `#journal`, `#sql` and `repo#write`. So nothing on the page shows who holds a `#host-exec` or `#sql` write grant (here: tasks, a template, a schedule and the deployment defaults).

Minor, not filed: `PUT /api/capability-policy {"policy":{}}` returns and keeps the existing catalogue. A catalogue is replaced by one naming it, not cleared by an empty body.

## Fixes on this branch

| Commit | Change | Tests |
|---|---|---|
| c0d13bc7 | `pkg/model/migrations.go`: savepoint around tolerated `ADD COLUMN` | `TestSchema78StoreMigratesEveryRetiredGrantAtOnce` (new, `pkg/model/{sqlite,postgres}/schema78_upgrade_test.go`): all six retired names on one task/template/schedule/session/list column, 78 → 86 in one `Init`. Passes on SQLite and on a real Postgres (`GRAIN_POSTGRES_TEST_URL`); the Postgres run failed before the fix |
| 41dfe0be | `pkg/mcp/toolnames.go`: `execute_grain_db` key | `TestEveryDBToolNamesTheGrantThatServesIt` (fails before, passes after); `pkg/mcp` |
| 9198a927 | grant refs in the namespace / CLI / read-only refusals | `pkg/namespace`, `pkg/gitproxy`, `pkg/capability/dbaccess`, `pkg/daemon` |

`pkg/staterepo` was not changed.

## Not run / environment notes

- **`run_host_command`'s per-command chat confirmation**: not reachable from any agent (finding 4).
- **`propose_task` and reviews as person-only surfaces**: the proposer run's calls were accepted and recorded, but the stub's run parked before grain filed the proposals, and a daemon restart ended that run. Reviews were not run. The agent API and templates were covered (above).
- **Journal redaction**: not checked live. The daemon was not under systemd, so `deploy://grain.log` returned no lines. `grainserver_test` covers it.
- **Granule**: not used; Docker only.
- **Sandbox image**: `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` (the default `sandboxImageDefault`) has no `/usr/local/bin/grain`, so every `grain …` in a sandbox was `command not found` until `cmd/grain-sandbox` was copied in. The default still points at the retired `bwsalmon` registry.
- The UI screenshots were checked by DOM text and scroll width, not by eye: this sandbox has no PNG viewer and no Gemini key for `LOOK=1`.
