# Walkthrough — 2026-11-13 §8: schedule publishing holds, template sandbox size, a schedule's own settings

Plan: [`docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md) §8 (PRs #1209, #1269, #1320, #1273, #1263, #1260; schemas 141, 143, 144).
Task 5360. Helpers: [`2026-11-13-schedules-templates/`](2026-11-13-schedules-templates/).

## Summary

| Item | SQLite | PostgreSQL | Verdict |
|---|---|---|---|
| 1. `skip-when: publishing-held` | held, recorded once, shown on `/api/schedules`, released on triage close | same | **Pass** after F1 (`grain health` said nothing) |
| 2. Template `sandbox:` | 60 GiB reaches the fired task; refused at load for 5000, 0 and an unknown key | same | **Pass** after F2 (every firing failed setup on host sandboxes) and F3 (doubled path) |
| 3. Schedule's own settings | template edit reaches the next firing; schedule's own grant survives and goes when removed | same | **Pass** |
| 4. Upgrade 139 → 146, dump/import | copies cleared, differences kept; `skip_when` and `sandbox` round-trip | same | **Pass** after F4 (own grants lost until the next commit) |

Four defects were fixed in this PR, each with a regression test. One gap (F5) is filed as follow-up work.

## Environment

- Commit walked: `51844bb2c` (`origin/main` at 2026-11-13 03:57 UTC). Fixes are on top of it in this branch.
- `go build -o /home/debian/bin/grain ./cmd/grain` from `HEAD`. The schema-139 build is `24d0eba1` in a worktree (`/home/debian/bin/grain-139`).
- A real `grain daemon -sandbox-port-base=0 -host-sandboxes -reconcile -poll-interval 1s` over:
  - SQLite (`data/store/grain.db`);
  - PostgreSQL 15 (Debian `postgresql` package, local cluster, `-postgres-url`).
- GitHub: `githubsim_server.go` (copied from the 2026-11-11 schedule-groups walkthrough). It serves githubsim's REST API plus `git http-backend` for `acme/widgets`, `acme/upstream` and `acme/downstream`. The daemon reads it via `-github-host 127.0.0.1:4270 -github-insecure-http`.
- Agent: a fake `agy` script that exits at once, configured as agent backend `ag-walk`. Its runs end `failed` ("no result event"), which is enough to see dispatch get past sandbox acquisition.
- `acme/widgets` carries `.grain/templates/{plain,big,nightly}.yml` and `.grain/schedules/{held,free,big,own}.yml` (see [`env.sh`](2026-11-13-schedules-templates/env.sh)).
- The pipeline is paused so filed tasks stay queued for inspection. It is only resumed for item 2's dispatch step.
- Browser: Playwright Chromium (`/var/cache/playwright`), 1440 px and 390 px, light and dark.
- Raw output: `out/<db>-item<N>.log` after the fixes, and `out/before-fix/` from the same scripts on the unfixed build.

**Stubbed boundaries** (the sandbox has nothing real behind these):

- **GitHub**: githubsim plus local bare repos.
- **The model**: a fake agent CLI.
- **VM sandboxes**: `-host-sandboxes` (host directories). Granule VMs need nested KVM and a guest image, so a 60 GiB VM disk could not be checked. Item 2 instead checks the task's recorded shape and what dispatch asks of the backend.
- **Release pipeline**: the base-branch publishing finding is seeded. That means three failed releases on `base_branch_release` plus a release triage task tagged `base-release-triage:acme/widgets:main`, instead of a red release run.
- **Clock**: the reconciler's 1 s tick is real. Each step pulls `next_run_at` forward (and `repo_declaration_sync.checked_at` back) in SQL, because `faketime` does not move a Go binary's clock.

Re-run everything with `DB=sqlite ./run_all.sh`, then `DB=postgres ./run_all.sh`, and each store's `walk_item_4b_dump.sh`.

## Item 1 — `skip-when: publishing-held`

[`walk_item_1_hold.sh`](2026-11-13-schedules-templates/walk_item_1_hold.sh)

1. **No hold.** `held` (skip-when) and `free` (none) both come due and each files a task.
2. **Raise the hold.** Seeding the release row and opening triage task 4 makes `GET /api/sandboxes` report `baseBranchPublishing[0].gated: true, triageTaskID: "4"`.
3. **Both come due under the hold.**
   - `free` files a task.
   - `held` files none. `scheduled_firing_skip` gets one row, `reason=publishingHeld`, `task_id=4`, detail "publishing held by task 4 (acme/widgets main has published nothing since …)".
   - `GET /api/schedules` shows `held` with `suppressed: {reason: publishingHeld, taskId: "4", detail: "skipped: publishing held by task 4 …"}`, `nextRunAt` left where it was, and the skip in its history. `free` shows `suppressed: null`.
   - Both UIs render `suppressed`. The `/next/` processes page shows "Suppressed (publishingHeld): …" and has a "Suppressed" filter. The classic `ProcessesList` shows a "Not firing" chip and "not firing: …" with a link to the triage task.
   - Rendered in Chromium over a daemon built with `make frontend` assets (`page_text.mjs`, `out/page-text.log`). `/next/processes/schedules` shows a "Suppressed" badge on `held`, and its expanded row shows the banner "Suppressed (publishingHeld): skipped: publishing held by task 2 (…) — Open task #2" above the skip history. Classic `/processes` shows "not firing: skipped: publishing held by task 2 (…), task 2". Screenshots: `make ui-screenshots`'s `ui/e2e/screenshot.mjs --url … --click 'text="held" >> nth=0'` at 1440 px desktop and 390 px phone, light and dark, for `/next/`, plus classic in light. The contact sheets are attached to the task's closing note, not committed.
4. **Further ticks** under the hold re-skip without a second record (still one row).
5. **Close the triage task.** On the next tick `held` fires once. The skip row becomes `outcome=fired`, `resolved_at` is set, and `suppressed` clears.

Identical on SQLite and PostgreSQL.

### F1 — `grain health` said nothing while publishing was held (fixed)

With the hold raised, `grain -server … health` printed only the target-repo host warning. The response's `baseBranchPublishing` (gated, triage task 4) appeared only under `-json`. So the text a person or monitoring script reads gave no sign that releases and `skip-when` schedules were held.

- **Reproduction:** `out/before-fix/postgres-item1.log`, "--- grain health".
- **Fix:** `pkg/cli/health.go` prints a `WARNING: base branches publishing nothing:` section with one line per branch. A gated branch's line names the triage task and says that further releases and `skip-when: publishing-held` schedules into the branch wait for it. The exit code stays 0, because this is a warning, not a failure.
- **After the fix:** `out/sqlite-item1.log`:
  `acme/widgets main: 3 release(s) published nothing since 2026-11-13T00:18:17Z; held by release triage task 4 -- further releases, and schedules with skip-when: publishing-held into this branch, wait until it closes`.
- **Regression test:** `TestGrainHealthNamesHeldBaseBranchPublishing` (`pkg/cli/health_test.go`).

## Item 2 — template `sandbox:` size

[`walk_item_2_sandbox.sh`](2026-11-13-schedules-templates/walk_item_2_sandbox.sh)

1. **Loaded.** `big.yml` with `sandbox: {disk-gb: 60}` loads as `template.sandbox = {"diskGb":60}`, and `GET /api/templates` shows it.
2. **Fired by its schedule.** The task has `sandbox_disk_gb = 60`.
3. **Filed by hand** (`POST /api/tasks` with `templateId`): the task has `0`. This is by design on this backend, because `applyTemplate` skips the template's size when `Config.SandboxShapeIgnored` (host-directory sandboxes). On a VM backend it is filled in.
4. **Dispatch.** Before the fix, the fired task's run ended `setup-failed` and was retried (see F2). After the fix, the run gets past sandbox acquisition (it ends `failed` on the fake agent's empty output), and the daemon logs `task 7: not asking for template repo:template:acme:widgets:big's sandbox size (0 vCPU/0 MiB/60 GiB): this deployment's host-directory sandboxes have no size of their own`.
5. **Refused at load** (each push, then a forced re-read). Each refusal shows in `repo_declaration_sync.last_error` and under `FAILING: repository declarations not read in full` in `grain health`. The template keeps `{"diskGb":60}` from the last good commit.
   - `disk-gb: 5000`: "sandbox: disk-gb is 5000, and may be at most 1024".
   - `disk-gb: 0`: "sandbox: disk-gb must be a positive integer, got 0".
   - `gpus: 1`: held back as a file written for a newer grain, by design (`SchemaError`). The other files stay in force: "sandbox: field gpus not found -- sandbox accepts cpus, memory-mb, disk-gb".
6. **Restored.** `last_error` clears.

The plan says "config-repo notice". Repository declarations report through `repo_declaration_sync` and `grain health`'s refused-declarations section, and that is where these appear.

### F2 — a template's `sandbox:` broke every firing on host-directory sandboxes (fixed)

- **Before the fix:** `out/before-fix/postgres-item2.log` and `sqlite-item2.log`:
  `run 7-1 (task 7): acquiring a sandbox: sandbox "7-1" asks for 0 vCPU/0 MiB/60 GiB but a host-directory sandbox has no shape of its own`. The task went back to the queue and failed the same way on every dispatch.
- **Cause:** the New Task form leaves a template's size off on such a backend. Every other filing point copies it on regardless: a schedule firing (`ComposeScheduleTask`), a suite, a path trigger, a run's proposal.
- **Fix:** `pkg/orchestrator/template_shape.go`. At dispatch, on a backend that is `Shapeless()` (`HostSandboxes`), the size is dropped only when every dimension the task states equals its template's (`TemplateID`, else `SourceTemplateID`). In that case the size came from the template, not from a person. A size a person stated on the task is still refused, as before.
- **Regression test:** `TestATemplatesSandboxSizeIsNotAskedOfAHostDirectoryBackend` (`pkg/orchestrator/template_shape_internal_test.go`).

### F3 — a held-back declaration repeated its path (fixed)

`.grain/templates/big.yml: .grain/templates/big.yml: line 5: sandbox: field gpus not found …` appeared on `last_error` and in `grain health`.

- **Cause:** `decodeInto` already prefixes the path, and `keepHeldBack` (and `grain health`) put `Path` in front of `Message` again.
- **Fix:** `pkg/repodecl/repodecl.go` drops the leading `<path>: ` from `RefusedDeclaration.Message`.
- **Regression test:** `TestAHeldBackFilesMessageDoesNotRepeatItsPath` (`pkg/repodecl/forward_test.go`).

## Item 3 — a template-backed schedule's own settings

[`walk_item_3_own.sh`](2026-11-13-schedules-templates/walk_item_3_own.sh)

`nightly.yml` sets `admission: durable`, `repo#read acme/upstream` and `api#memory`. `own.yml` fires it and adds `api#ci-read`.

A schedule may not declare repository access (`repo#read`/`repo#write`); that is refused at load. So the schedule's own grant is a non-repository one.

| Firing | Task admission/hosting | Task grants | Task reads | Schedule row |
|---|---|---|---|---|
| 3a first | durable / unattended | `api#ci-read` (own.yml), `api#memory` (nightly.yml) | acme/upstream | no admission; grant `api#ci-read` only |
| 3b second | same | same: the schedule's own grant survives a firing | acme/upstream | unchanged |
| 3c template edited (`api#memory`→`api#tasks:read`, upstream→downstream) | durable | `api#ci-read` (own.yml), `api#tasks:read` (nightly.yml); `api#memory` gone | acme/downstream | unchanged |
| 3d own grant removed | durable | `api#tasks:read` plus the ci_mode github default `api#ci-read={"repo":"acme/widgets"}` (via `default`) | acme/downstream | no grants |
| 3e `hosting: console` on own.yml | durable / **console** | as 3d | acme/downstream | hosting `console` |

The schedule row stores only its own difference throughout. Identical on PostgreSQL.

## Item 4 — upgrade 139 → 146, and dump/import

### In-place upgrade

[`walk_item_4_upgrade.sh`](2026-11-13-schedules-templates/walk_item_4_upgrade.sh)

1. **The schema-139 build fires `own`.** As before PR #1273, the firing wrote the template's `durable` and its `api#memory` grant (via `.grain/templates/nightly.yml`) onto the schedule row. It also replaced `own.yml`'s declared `api#ci-read`: the 139 row holds only `api#memory`.
2. **Seeded in SQL:** `free` (template `plain`) is raised to `bypass`/`console` with an operator grant; `big` is raised to `durable`; `own` gets an operator-written `api#memory`, the same as the template's.
3. **HEAD starts on the same store.** It logs `migrated the store in place from schema 139 to 146` on both stores. After migration 144:
   - `own` has no admission or hosting and no grants: the copy and the operator-written duplicate are both gone.
   - `free` keeps `bypass`/`console` and its grant, and `big` keeps `durable`.
   - `skip_when` and `template.sandbox` exist, both empty.
4. **The next firing of `own`** carries the template's `durable` and `api#memory`.

### F4 — a schedule's declared grants stayed lost after the upgrade (fixed)

Step 1 shows the old build's firing had already overwritten `own.yml`'s `api#ci-read`. Migration 144 deleted only the copy. The declaration sync writes a file's grants back only when the default branch moves (`syncDeclarations` skips when `head == last.Commit`).

So after the upgrade, every firing of `own` went out without its declared grant until somebody committed to `acme/widgets`.

- **Reproduction:** `out/before-fix/*-item4.log`. The firing on HEAD lacks `api#ci-read` (via own.yml). It returned only after an unrelated commit was pushed (appended to `before-fix/sqlite-item4.log`).
- **Fix:** migration 144 (`pkg/model/schedule_own_settings_migration.go`) also clears `repo_declaration_sync.commit`, so the next reconcile re-reads every repository and writes the declared grants back. It is guarded for stores older than that table. Stores that already ran 144 are not affected; they have very likely had a commit since.
- **After the fix:** `out/sqlite-item4.log` and `out/postgres-item4.log`. The `commit` is empty after the upgrade, and `own`'s declared `api#ci-read` is back within the 2-minute declaration poll (109 s on SQLite, 101 s on PostgreSQL) with no commit. The next firing carries `api#ci-read` (own.yml) plus `api#memory` (nightly.yml).
- **Regression test:** `TestMigration144ClearsTemplateCopiesOffSchedules` now also asserts the sync commit is forgotten (`pkg/model/schedule_own_settings_test.go`).

The operator grant seeded on `free` is dropped by that re-read. That is expected: a repository schedule's grants are its file's, and any later commit does the same.

### Dump/import

[`walk_item_4b_dump.sh`](2026-11-13-schedules-templates/walk_item_4b_dump.sh)

1. **Daemon A exports.** Its own export writes `skip-when: publishing-held` for `held` in `settings/schedules.yml`. It writes `sandbox: {disk-gb: 60}` (repository `big`) and `sandbox: {cpus: 4, disk-gb: 80}` (an operator template made with `POST /api/templates`) in `settings/templates.yml`.
2. **`grain state check`** on that dump says "ok: grain can load this".
3. **Daemon B** starts on a fresh store whose state repository is a clone of A's. It logs `loading … as the deployment's state repository; importing its settings`.
4. **Result:** B's `template.sandbox` and `schedule.skip_when` match A's, and B's `GET /api/templates` shows both sandboxes.

Done on SQLite (`out/sqlite-item4b.log`) and PostgreSQL (`out/postgres-item4b.log`).

### F5 — `skip_when` is not in the schedules API or either UI (filed)

`POST`/`PATCH /api/schedules` takes no `skipWhen`, and `GET /api/schedules` returns none. Neither UI shows that a schedule declares `skip-when: publishing-held` until a firing is actually held. An operator can set it only through `settings/schedules.yml` in the config repository, or through a repository declaration.

This is not a regression; PR #1209 added the column and the YAML key only. It is filed as follow-up work: the API field, plus showing it on the `/next/` schedule page.

## Other observations (no change)

- A refused template also refuses the schedules that name it ("no template \"big\" …") in the same `last_error`. That is correct, but it doubles the notice.
- The publishing finding's detail says "Nothing is held up by that" and, in the same text, "Further releases on this branch are held pending resolution of triage task N", which also holds `skip-when` schedules. The first sentence is about the merge queue, but it reads as a contradiction. Worth a wording pass.
- The declaration poll is 2 minutes (`declarationPollInterval`). The scripts force a re-read by moving `checked_at` back.
