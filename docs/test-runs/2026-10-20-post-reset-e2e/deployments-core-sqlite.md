| id | flow | build/db | result | detail |
|---|---|---|---|---|
| I1 | Fresh install boots; access code 0600; no tenancy file | core/sqlite | **PASS** | data dir: cache hub-audit.log local-api-token secrets state state-repo store  |
| I2 | Access-code sign-in (no code 401, wrong code 401, code 200; grain access-code prints it) | core/sqlite | **PASS** | /: 401, /api/tasks: 401, wrong: 401, right: 200 |
| I2b | Access-code login form (lower-case, spaces) sets a session cookie | core/sqlite | **PASS** | POST /api/auth/access-code: 303; cookie → /api/tasks 200 |
| U1 | No team/account/admin/hub API routes | core/sqlite | **PASS** | all 404 |
| C3 | Command-backed forge advertised only in the Google build | core/sqlite | **PASS** | sources: [] |
| C3b | Core refuses the forgecli source cleanly | core/sqlite | **PASS** | exit 1: grain: unknown ambient source "forgecli": gh, git, auto |
| C5 | Fresh install has no default model | core/sqlite | **PASS** | defaultModel unset |
| I4 | grain connections import (git + model + default model), idempotent | core/sqlite | **PASS** | imported 1 git and 1 model connections; token 0600; default model read back |
| L1 | Task from the CLI: create, approve, close, reopen | core/sqlite | **PASS** | task 2: approved→queued, closed→closed, reopened→queued |
| L2 | Task from the API with depends_on and not_before | core/sqlite | **PASS** | task 3: dependsOn ['2'], deferredUntil 2099-01-01T00:00:00Z |
| S1 | Standing work: template, cron schedule, suite, board created and listed | core/sqlite | **PASS** | templates 201 (template-1), schedules 201 (sched-1; 4 listed), suites 201, boards 201 |
| S2 | Schedule run-now files a task | core/sqlite | **PASS** | POST /api/schedules/sched-1/run: 201 {"taskId":"4","schedule":{"id":"sched-1","title":"qualification from template","description":"t","repo":"acme/widgets"," |
| L3 | Pipeline pause/resume; per-model pause lift | core/sqlite | **PASS** | set/resume exit 0; DELETE /api/pause?connection&model: 200 |
| X1 | Secrets (git token, model key, access code) never in the daemon log | core/sqlite | **PASS** | none found |
| I3 | Restart on a dir this build wrote (guard accepts it) | core/sqlite | **PASS** | task 2 still there |
| R1 | Export/import round trip (tasks, default model, templates) | core/sqlite | **PASS** | task 2, default model and 1 template(s) survive |
| I5 | Guard refuses a v1.13.0 (pre-reset) data dir, untouched, with a fresh-dir instruction | core/sqlite | **PASS** | 2026/10/20 16:48:22 grain daemon: this build of grain is single-tenant only, and this data dir was written by a build from before the single-tenant reset: schema 17, from before the single-tenant baseline (schema 19), wh |
