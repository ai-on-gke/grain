# Walkthrough: `grain/config` import guards, grant plumbing and goal-met close (2026-11-16)

- **Plan:** `docs/test-plans/2026-11-16.md` §11 (filed by [Task #6330](/tasks/6330))
- **Task:** [Task #6484](/tasks/6484)
- **Commits walked:**
  - `main` HEAD: `a771a997344da6009bdaccb5e0e435e8f0895857` (`model.SchemaVersion = 151`)
  - `v3.5.0`: `c078507abc` (`model.SchemaVersion = 136`, built from detached worktree)
  - `origin/release/v3.5.1`: `4e29f7be9a` (`model.SchemaVersion = 136`, built from detached worktree)
- **Environment:**
  - Real `grain daemon -sandbox-port-base=0` (with `-host-sandboxes`, local bare `grain/config`, and simulated GitHub `ghsim` from `docs/test-runs/2026-11-15-config-grants-export/` and `docs/test-runs/2026-11-15-resume-later-release/`)
  - Go: `go1.26.10 linux/amd64`
  - SQLite: `3.40.1`
  - PostgreSQL: `15.20` (`GRAIN_REQUIRE_POSTGRES=1`)
  - Node: `v20.19.0`, Playwright `1.62.1`, Chromium `1234`
  - UI viewports checked: `2048×900`, `1440×900`, `390×844` (light and dark themes)

## Outcome summary

| # | Feature / check | SQLite | PostgreSQL | Notes |
|---|---|---|---|---|
| 1 | Newer-build refusal (`SyncExternal`, PR #1571 `473c2d6bb`) from sync tick, `/next/settings/config-repo` Sync now, and `grain state sync` | PASS (after **F1** fix) | PASS (after **F1** fix) | Backend refused atomically on all 3 entry points with zero half-applied rows. Found **F1**: `/next/settings/config-repo` dropped `status.error`, `schemaVersion`, and `buildSchemaVersion` on load — fixed in this PR. Also checked `release/v3.5.1` (`4e29f7be9a`, schema 136) reading a HEAD (schema 151) export: startup `LoadExternal` refuses cleanly; live `SyncExternal` applies because PR #1571 is on `main` only. |
| 2 | Hand-written `settings/templates.yml` omitting `target:`, `auto-merge:`, `created-at:` (PR #1648 `22b01abdb`), filed from `/next/` New task, next export stability | PASS | PASS | Imports with zero defaults (`target_owner=""`, `target_name=""`, `auto_merge=0/f`, `created_at=0001-01-01T00:00:00Z`), passes `grain state check`, files from `/next/` New task with unbound repository defaulting to `-default-target-repo` (`acme/widgets`), exports identically on SQLite and PostgreSQL, and subsequent `grain state sync` is a clean no-op (`already up to date, nothing to commit`). |
| 3 | Retired bare names (`self-debug`, `read-issues`, `github-credential:bot`) on a real `v3.5.0` state repo imported into HEAD (PR #1588 `270790ed1`) | PASS | PASS | Upgraded in place (`136 -> 151`), applied unapplied config commit, and rewrote dump at schema 151. `GET /api/config`, `GET /api/repos/acme/widgets/settings`, DB columns, `/next/repos/acme/widgets/agents` & `/ci`, and rewritten `tables/*.json` hold only canonical `https://grain.dev/capabilities/...` grants. |
| 4 | `propose_task` retired bare capability refused in-turn (PR #1588 `97046087b`) + CI job declaring retired bare capability names canonical grant (PR #1596 `d85bfd2af`) | PASS | PASS | All 4 retired `propose_task` calls returned `isError=true` in-turn with exact canonical grant refusals and 0 tasks filed. `.grain/workflows/retired.yml` jobs (`bare-gemini-key`, `bare-grain-ci`, `bare-self-debug`) concluded `errored` naming the canonical grant to declare instead. |
| 5 | `HeldGrantKey` (`https://grain.dev/capabilities/github#read-issues={}` vs bare `https://grain.dev/capabilities/github#read-issues`, PR #1594 `ad679cb3a`) | PASS | PASS | Acting task holding `https://grain.dev/capabilities/github#read-issues={}` succeeded filing `triage-bare` via `POST /api/tasks/from-template`, filing children with `reviewTemplateId: repo:template:acme:widgets:review-bare` via both `POST /api/tasks` and `propose_task`, and triggering `ReviewGrants` review task on pushed branch with `github#read-issues={}` preserved. Negative control (`triage-unheld` granting `https://grain.dev/capabilities/grain#source`) refused. |
| 6 | Review threads & decisions wake `resume_later(ci: my-branch)` tasks (PR #1590 `02fcaaef4`) | PASS | PASS | Re-ran `docs/test-runs/2026-11-15-resume-later-release/run_walkthrough.sh c c_person e e_person` on both SQLite and PostgreSQL: `c`, `c_person`, `e`, `e_person` all passed. |
| 7 | Goal-met close before dispatch (`goal-met-closes`, PR #1568 `57bec31d0`) | PASS | PASS | Task with `base-green` (`on_met: close`) met mid-run while attempt 1 stayed live across `PauseGracePeriod` (2m), was cancelled by `errGoalMet`, and closed immediately with `outcome: goal-met` and strictly 1 row in `task_run` (`2-1`, zero wasted `2-2`). |

---

## Findings & fixes

### F1 (Fixed): `/next/settings/config-repo` hides persisted background StateRepo sync errors and schema-version mismatches on load

- **Severity:** P1 (visible operational defect; directly breaks Item 1's operator-facing error check on `/next/settings/config-repo`).
- **Deterministic reproduction (before fix):**
  1. Start `grain daemon -sandbox-port-base=0` in read-only external IaC mode pointing at a bare `config.git` (at schema 151) on SQLite or PostgreSQL.
  2. Push a commit to `config.git` setting `schema-version` to `152` and mutating `settings/templates.yml` and `tables/grain_config.json`.
  3. Wait ~30s for the background sync tick. `GET /api/state-repo` now reports:
     ```json
     {
       "mode": "external",
       "head": "<unapplied-sha>",
       "schemaVersion": 152,
       "buildSchemaVersion": 151,
       "error": "state repository holds changes grain could not apply: state repository was written by a newer build of grain: repository is at schema 152, this build knows 151"
     }
     ```
  4. Open `/next/settings/config-repo` in Chromium.
  5. **Observed before fix:** `card-state-repo` renders a neutral pill `main @ <unapplied-sha>` and **zero** error banner or schema mismatch indicator — making it look as if the unapplied commit succeeded. Clicking **Sync now** (`POST /api/state-repo/sync` → HTTP 502) transiently shows the error in the top settings error banner, and reloading the page clears it again.
- **Root cause:** `loadStateRepo()` in `ui-ng/src/app/settings/settings-code.component.ts` only extracted `branch`, `head`/`commit`, and `remote` from `GET /api/state-repo`, discarding `error`, `schemaVersion`, and `buildSchemaVersion` (and swallowing fetch failures silently). `syncStateRepo()` also did not refresh `GET /api/state-repo` after a failed sync. Contrast classic `ui/src/components/StateRepoPanel.jsx`, which renders both `status.error` and the schema-version mismatch alert inside the panel on load.
- **Fix (in this PR):**
  - `ui-ng/src/app/settings/settings-code.component.ts`:
    - Store `stateRepoError`, `stateRepoLoadError`, `stateRepoSchemaVersion`, and `stateRepoBuildSchemaVersion` from `GET /api/state-repo` in `loadStateRepo()`.
    - Switch `state-repo-status` pill from `.neutral` to `.error` when a sync error, load error, or schema mismatch is present, and display a compact `schema N (build M)` error pill (`data-testid="state-repo-schema-mismatch"`) when `schemaVersion > 0 && buildSchemaVersion > 0 && schemaVersion !== buildSchemaVersion`.
    - Render `stateRepoError` (or schema-mismatch guidance when no sync error string is present, or `stateRepoLoadError` if the status read fails) inside `card-state-repo` as an `.s-banner.error` with `role="alert"`.
    - In `syncStateRepo()`, refresh `loadStateRepo()` on error as well as success; when `stateRepoError` already displays the server's sync refusal inline, clear `state.error` so the same error sentence is never duplicated in two banners (matching `design/design/ui-design-book.design.md`).
  - `ui-ng/src/app/settings/settings-forms.css`: add `overflow-wrap: anywhere` to `.s-banner` so long URLs/paths in error messages never cause sideways overflow at phone width (`390px`).
- **Regression tests added:**
  - Unit tests in `ui-ng/src/app/settings/settings-page.component.spec.ts` covering persisted sync error + schema mismatch on load, `Sync now` failure refresh without duplicate banners, recovery clearing back to neutral, and schema mismatch without an error string.
  - Playwright E2E spec `ui-ng/e2e/next-config-repo-sync-error.spec.ts` (`@phone @dark`), verified across `desktop` (`1440×900`), `phone` (`390×844`), and `dark` projects (axe clean, zero horizontal overflow, bounding boxes inside card).
- **UI verification & three review passes:**
  - Before/after screenshots captured via `ui/e2e/screenshot.mjs --pages /next/settings/config-repo --merge /api/state-repo=...` at `1440×900` and `390×844` in light and dark themes, plus programmatic layout/token check at `2048×900`, `1440×900`, and `390×844` (`cardW: 1180 / 1024 / 358`, `overflowX: 0`, `errors: 0`, M3 `error-container` / `on-error-container` tokens in both themes).
  - Re-walked Item 1 end-to-end against rebuilt `grain daemon` on both SQLite and PostgreSQL: `/next/settings/config-repo` now immediately shows `main @ <sha>`, `schema 152 (build 151)`, and the exact refusal banner before any button click, and clicking **Sync now** keeps a single inline alert.
  - **Design review:** M3 tokens only, contrast > 7:1 dark / > 11:1 light, no overflow at 2048/1440/390, single-banner rule observed.
  - **Functional review:** Live daemon + `grain demo` E2E verified load, failed sync refresh, recovery clearing, and schema-only mismatch; `next-full-system.spec.js` and `next-settings-walkthrough.spec.js` (14/14) and all 691 `ui-ng` unit tests pass.
  - **Adversarial review:** Verified distinct sync errors (e.g. 403 or unreachable daemon during refresh) still surface via `state.recordError`, absent/zero schema versions do not trigger false mismatch alerts, Angular interpolation avoids HTML injection, and double-clicking **Sync now** is guarded by `syncingStateRepo()`.

---

## Detailed walkthrough log

### Item 1: Newer-build refusal from all 3 entry points + `release/v3.5.1` check

1. **Setup (SQLite and PostgreSQL):**
   - Seeded Daemon A (HEAD `a771a9973`, schema 151) with a template (`template-1` / `walk-plain`) and cloned its `data/state-repo` into bare `config.git`.
   - Started Daemon B in read-only external IaC mode (`grain state repo -branch main file://.../config.git`) against a clean store. Verified `GET /api/state-repo` returned `schemaVersion: 151, buildSchemaVersion: 151, error: null`.
   - Pushed a commit to `config.git` changing `schema-version` to `152` and mutating `settings/templates.yml` (`walk-plain` → `MUTATED-BY-152`) and `tables/grain_config.json`.
2. **Entry point (a) — 30s background sync tick:**
   - Fired at ~27s on SQLite (`cb51b2f3`) and ~32s on PostgreSQL (`f8bb66ec`).
   - Daemon log recorded:
     `grain: state sync failed: state repository holds changes grain could not apply: state repository was written by a newer build of grain: repository is at schema 152, this build knows 151`
   - `GET /api/state-repo` populated `schemaVersion: 152, buildSchemaVersion: 151` and the exact `error` string.
3. **Entry point (b) — `/next/settings/config-repo` State pane Sync:**
   - Before F1 fix: page load hid the error completely; clicking **Sync now** received HTTP 502 and showed the error only transiently in the top banner.
   - After F1 fix (re-walked on both SQLite and PostgreSQL): page load immediately showed error pills `main @ cb51b2f3` / `main @ f8bb66ec`, `schema 152 (build 151)`, and the inline `role="alert"` banner with the exact refusal. Clicking **Sync now** sent `POST /api/state-repo/sync` → HTTP 502 and refreshed `GET /api/state-repo`, keeping a single alert banner.
4. **Entry point (c) — `grain state sync` CLI:**
   - Exited 1 on both SQLite and PostgreSQL with:
     `grain state: state repository holds changes grain could not apply: state repository was written by a newer build of grain: repository is at schema 152, this build knows 151`
5. **Atomicity check (SQLite and PostgreSQL):**
   - Diffed `template`, `template_read`, `template_write`, `schedule_read`, `schedule_write` tables before and after all 3 entry points: **unchanged** (`walk-plain` untouched; zero rows from the schema-152 commit applied).
6. **`release/v3.5.1` (`4e29f7be9a`, schema 136) reading a HEAD (`a771a9973`, schema 151) export:**
   - **Startup (`LoadExternal`):** Refused cleanly and exited non-zero (`state repository was written by a newer build of grain: repository is at schema 151, this build knows 136`).
   - **Live `POST /api/state-repo/sync`:** Returned HTTP 200 and applied the schema-151 dump (template changed from `v351-orig` to `walk-plain`; `acme/docs` read and `acme/gadgets` write survived because `v3.5.1` already parses `grants: repo#read/#write`). This matches expectations because PR #1571 (`473c2d6bb`) landed on `main` and was not backported to `release/v3.5.1`.

### Item 2: Hand-written template omitting `target:`, `auto-merge:`, `created-at:`

1. Pushed a minimal entry in `settings/templates.yml` (omitting `target:`, `auto-merge:`, `created-at:`):
   ```yaml
   templates:
     - id: template-handwritten
       name: hand-written-no-target
       title: Hand-written template without target
       body: Run a quick check on whichever repository is chosen.
   ```
2. Verified `grain state check` passed (`ok: grain can load this`) with zero "cannot be imported" warnings.
3. Imported through a live read-only IaC daemon on both SQLite and PostgreSQL:
   - DB row on both stores: `target_owner=""`, `target_name=""`, `auto_merge=0/f`, `created_at=0001-01-01T00:00:00Z`.
4. Filed from `/next/` **New task** sheet (`rail-new-task` → `nt-template` → `hand-written-no-target`):
   - Repository picker stayed unbound (`Select a repository…`), submitting `POST /api/tasks` with `"repo":""`; the server resolved target to the deployment's `-default-target-repo` (`acme/widgets`) and returned HTTP 201 (`autoMerge: false`, `templateId: "template-handwritten"`).
5. Exported back out via `grain state sync` on both SQLite and PostgreSQL:
   - First export normalized the minimal YAML to the canonical full-column shape (`target: ""`, `auto-merge: false`, `created-at: "0001-01-01T00:00:00Z"`), byte-for-byte identical between SQLite and PostgreSQL.
   - Second `grain state sync` printed `already up to date, nothing to commit` with a clean `git status`.

### Item 3: Retired-name upgrade from a real `v3.5.0` state repository

1. Ran a real `v3.5.0` (`c078507abc`, schema 136) daemon on both SQLite and PostgreSQL to generate genuine schema-136 stores and `state-repo` trees (`schema-version` = `136`).
2. Hand-edited and committed retired bare names into the `v3.5.0` state repo:
   - `tables/grain_config.json` `default_capabilities`: added `self-debug,read-issues`
   - `tables/repo_config.json` `default_capabilities`: set to `read-issues,github-credential:bot`
   - `tables/repo_config.json` `ci_capabilities`: set to `self-debug,github-credential:bot`
   *(Note: fresh IaC startup `LoadExternal` intentionally rejects older schemas with `ErrSchemaTooOld`; the supported path is upgrading the deployment in place with HEAD opening the `v3.5.0` data dir.)*
3. Started HEAD (`a771a9973`, schema 151) on the `v3.5.0` data dir on both SQLite and PostgreSQL:
   - Migrated schema `136 -> 151`, applied the 1 unapplied config commit, upgraded retired names on import, and rewrote `state-repo` at schema `151`.
4. Verified every surface held strictly canonical grants on both SQLite and PostgreSQL:
   - `GET /api/config` (`defaultCapabilities`): `["https://grain.dev/capabilities/api#self-debug","https://grain.dev/capabilities/grain#source={}","https://grain.dev/capabilities/github#read-issues={}"]`
   - `GET /api/repos/acme/widgets/settings`:
     - `defaultCapabilities`: `["https://grain.dev/capabilities/github#read-issues={}","https://grain.dev/capabilities/repo#credential={\"credential\":\"bot\"}"]`
     - `ciCapabilities`: `["https://grain.dev/capabilities/api#self-debug","https://grain.dev/capabilities/grain#source={}","https://grain.dev/capabilities/repo#credential={\"credential\":\"bot\"}"]`
   - `/next/repos/acme/widgets/agents` (`#rp-caps`) displayed `https://grain.dev/capabilities/github#read-issues={}, https://grain.dev/capabilities/repo#credential={"credential":"bot"}`, and `/next/repos/acme/widgets/ci` displayed the 3 canonical held grant chips (`self-debug`, `source`, `credential: bot`).
   - Rewritten `tables/grain_config.json` and `tables/repo_config.json` in `state-repo` contained zero bare retired names.

### Item 4: `propose_task` in-turn refusal & CI job retired-capability message

Executed with the scriptable fake agent (`docs/test-runs/2026-11-16-config-grant-plumbing/fake-agy.py`, which faithfully emits `state: "ERROR"` + `tool_info.error` when the sandbox MCP returns `isError: true`) against a live daemon on both SQLite and PostgreSQL:

1. **4a (`propose_task` live refusal, PR #1588 `97046087b`):**
   - Agent called `propose_task` with `capabilities: ["self-debug"]`, `["grain-ci"]`, `["read-issues"]`, and `grants: ["self-debug"]`.
   - Every call returned `isError=true` in the same turn with exact canonical refusals:
     - `propose_task: self-debug is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/api#self-debug and https://grain.dev/capabilities/grain#source={}; name that instead`
     - `propose_task: grain-ci is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/grain#ci={}, which no task names: grain grants it to every task on a repository whose CI Mode is "Use Grain CI", so leave it off`
     - `propose_task: read-issues is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/github#read-issues={}; name that instead`
   - Task count before = 1, after = 2 (only the acting task itself; **0** proposals filed) on both SQLite and PostgreSQL.
2. **4b (CI job declaring retired bare capabilities, PR #1596 `d85bfd2af`):**
   - Triggered `POST /api/ci/runs` on branch `walk-ci-retired` with `.grain/workflows/retired.yml` declaring `capabilities: [gemini-key]`, `[grain-ci]`, and `[self-debug]`.
   - All 3 jobs concluded `errored` naming their canonical grants on both SQLite and PostgreSQL:
     - `bare-gemini-key`: `this job requires the capability "gemini-key" and this deployment does not provide it for acme/widgets: gemini-key is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/gcp#gemini-key={}; declare that instead`
     - `bare-grain-ci`: `... it became https://grain.dev/capabilities/grain#ci={}; declare that instead`
     - `bare-self-debug`: `... it became https://grain.dev/capabilities/api#self-debug and https://grain.dev/capabilities/grain#source={}; declare that instead`

### Item 5: `HeldGrantKey` template-within-task and review template

Seeded `.grain/templates/` in `acme/widgets` with `triage-bare.yml` (bare `https://grain.dev/capabilities/github#read-issues`), `review-bare.yml` (bare `https://grain.dev/capabilities/github#read-issues`), and `triage-unheld.yml` (bare `https://grain.dev/capabilities/grain#source`). Enabled repository declarations via `PUT /api/repos/acme/widgets/declarations` (`{"declarations":true}`) and `ciMode: "grain"`.

Filed an acting task holding `https://grain.dev/capabilities/github#read-issues={}`, `https://grain.dev/capabilities/api#tasks:from-template`, `https://grain.dev/capabilities/api#tasks:write`, and `reviewTemplateId: "repo:template:acme:widgets:review-bare"`. On both SQLite and PostgreSQL:

- **5a (`POST /api/tasks/from-template` via `grain api`):** `triage-bare` succeeded (`{"id":"4","state":"proposed"}`), carrying `https://grain.dev/capabilities/github#read-issues={}`.
- **5b (`POST /api/tasks` with `reviewTemplateId` via `grain api`):** succeeded (Task 5, `reviewTemplateId: "repo:template:acme:widgets:review-bare"`).
- **5c (Negative control — unheld grant):** `POST /api/tasks/from-template` for `triage-unheld` refused (`RC=1`) with:
  `{"error":"template repo:template:acme:widgets:triage-unheld grants https://grain.dev/capabilities/grain#source, which the agent's own task does not hold"}`
- **5d (`propose_task` with `review_template_id`):** succeeded and relayed Task 6 with `reviewTemplateId: "repo:template:acme:widgets:review-bare"`.
- **5e (Live `ReviewGrants` on pushed branch):** after the acting task pushed its branch, the orchestrator automatically filed review Task 7 (`templateId: "repo:template:acme:widgets:review-bare"`) preserving `https://grain.dev/capabilities/github#read-issues={}`.

### Item 6: Review threads and decisions wake `resume_later(ci: my-branch)` tasks

Re-ran `docs/test-runs/2026-11-15-resume-later-release/run_walkthrough.sh c c_person e e_person` against HEAD (`a771a9973`) on both SQLite and PostgreSQL:

- **SQLite (`DAEMON_PORT=4280`):**
  - `c` (review thread on PR wakes `resume_later(ci: my-branch)` task): **PASS** (0.3s)
  - `c_person` (person-Defer control does not wake on review thread): **PASS**
  - `e` (review-question decision wakes `resume_later(ci: my-branch)` task): **PASS** (0.3s)
  - `e_person` (person-Defer control): **PASS**
- **PostgreSQL (`DAEMON_PORT=4290`):**
  - `c`: **PASS** (30.4s)
  - `c_person`: **PASS**
  - `e`: **PASS** (0.6s)
  - `e_person`: **PASS**

### Item 7: Goal-met close before dispatch (`goal-met-closes`)

1. Seeded `acme/widgets` `main` with a failing `.grain/workflows/ci.yml` (`exit 1`) and enabled `ciMode: "grain"` and `watchDefaultBranch: true`. Waited for base branch CI to conclude `failure`.
2. Filed Task 2 with `goal: {"kind":"base-green","spec":{"repo":"acme/widgets","branch":"main"},"on_met":"close"}` and configured `action-2.json` so attempt 1 slept inside `run_command` across `PauseGracePeriod` (`2m`), with a sentinel second action that would mark a bug if attempt 2 ever dispatched.
3. Once Task 2 was `running` (attempt 1 live), pushed a passing `.grain/workflows/ci.yml` (`exit 0`) to `main`.
4. **Observed on SQLite:**
   - `metAt` recorded at `01:22:15.499Z` while attempt 1 was running; warning comment posted (`this run will be stopped and the task closed in 2m`).
   - At `01:24:15` (exactly 2m later): `orchestrator: run of task 2 stopped: its goal is met`, immediately followed by `orchestrator: closing 2: its goal is met (...)`.
   - Final state (+10s stability check): `state: "closed"`, `goal.outcome: "goal-met"`, `goal.countedRunId: "2-1"`, `task_run` table has **1 row** (`2-1`, `attempt=1`, `outcome=cancelled`, `detail="orchestrator: run stopped because its task's goal was met"`). Zero wasted attempt `2-2`.
5. **Observed on PostgreSQL:**
   - `metAt` recorded at `01:25:45.263Z` while attempt 1 was running; warning comment posted.
   - At `01:27:45` (exactly 2m later): `orchestrator: run of task 2 stopped: its goal is met`, immediately followed by `orchestrator: closing 2: its goal is met (...)`.
   - Final state (+10s stability check): `state: "closed"`, `goal.outcome: "goal-met"`, `goal.countedRunId: "2-1"`, `task_run` table has **1 row** (`2-1`, `attempt=1`, `outcome=cancelled`, `detail="orchestrator: run stopped because its task's goal was met"`). Zero wasted attempt `2-2`.

---

## Verification commands run

- `cd ui-ng && npm test` — 70/70 test files, 691/691 tests passed
- `cd ui-ng && npm run lint && make fmt-frontend` — clean
- `make frontend` — built `ui/`, `ui-ng/`, and `ui-hub/` cleanly
- `cd ui-ng && npx playwright test e2e/next-config-repo-sync-error.spec.ts` — 3/3 passed (`desktop`, `phone`, `dark`)
- `cd ui-ng && npx playwright test e2e/next-full-system.spec.js e2e/next-settings-walkthrough.spec.js` — 14/14 passed
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/staterepo/... ./pkg/model/... ./pkg/mcp/... ./pkg/orchestrator/... ./pkg/ci/... ./pkg/workflows/... ./pkg/ports/... ./tests/hygiene/...` — all 14 packages passed
