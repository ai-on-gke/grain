# Walkthrough: task template provenance and the `TemplateID` authority boundary

Plan: [`docs/test-plans/2026-11-13-afternoon.md`](../test-plans/2026-11-13-afternoon.md) §1, which audits PR #1438 (`bcd2b5a83`, `task_source_template` / `templateId`).

- **Commit walked:** `ab445d443` (`origin/main` when [Task #5483](/tasks/5483) started). The two fixes below sit on top of it on this branch.
- **Helpers:** [`2026-11-13-task-source-template/`](2026-11-13-task-source-template/).
- **Raw output:** [`out/`](2026-11-13-task-source-template/out/) (after the fixes) and [`out/before/`](2026-11-13-task-source-template/out/before/) (item 2 on a build without the F1 fix).

## Results

| # | Item | SQLite | PostgreSQL | Outcome |
|---|---|---|---|---|
| 1 | Pre-`bcd2b5a83` schema-146 store: `Init`, the API, `grain state check`, state-repo round-trip | pass | pass | No change needed. |
| 2 | Authority boundary across (a)–(e), plus edit, reopen and redispatch | pass after fix | pass after fix | **F1 fixed.** Before the fix the port (d) was held at dispatch for good. |
| 3 | `/next/` New-task sheet: template and scratch filings, `/next/tasks`, `/next/task/<id>` | pass after fix | pass after fix | **F2 fixed.** Before the fix, filing a deployment template from the sheet failed with a 404. |

**F3** (open, follow-up proposed): `model.TemplateWithinTask` reads `…#read-issues={}` (how a task stores a method grant) and `…#read-issues` (how a template stores it) as two different grants. An agent holding the grant is therefore refused filing a template that grants it.

## Environment

- **Binary:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` from this branch. Item 2's "before" used `grain-base`, the same tree with `pkg/orchestrator/port.go` reverted.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes -max-workers 1 -as alice@example.com -repo acme/widgets` on `127.0.0.1:4281` ([`env.sh`](2026-11-13-task-source-template/env.sh)).
- **Stores:**
  - SQLite (`data/store/grain.db`).
  - PostgreSQL 15.20 (Debian `postgresql`, `-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_walk_st`), on a freshly created database for every run.
- **Browser:** Chromium from `ui/node_modules/@playwright/test`, with `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. Viewports 1440×900 and 390×844.

### Stubbed boundaries

These are the only stubs.

- **GitHub:** [`ghsim.go`](2026-11-13-task-source-template/ghsim.go) is `pkg/github/githubsim` over a local bare `acme/widgets`. On top of the sim it adds:
  - `git http-backend` for git traffic;
  - recorded merge commits;
  - a reachable bot token;
  - `GET /user`;
  - one seeded issue, which `grain issue list` reads.
- **Repository content:**
  - `main` and `release`, paired through `.grain/settings.yml` `port-branches`. `release` diverges on `README.md`, so a port cannot be cherry-picked mechanically and is filed as an agent task.
  - `.grain/templates/release-note.yml`, which declares `capabilities: [github#read-issues]` and `push: [refs/tags/walk-*]`.
  - `plain-note.yml`, with no capabilities and no push rule.
  - `.grain/triggers/walk-merge.yml` (`on: merge`, `paths: [CHANGELOG.md]`, `template: release-note`, `auto-approve: true`).
- **Agent CLI:** [`fake-agy.py`](2026-11-13-task-source-template/fake-agy.py) drives the **real** `grain mcpserver` that each run is handed (`run_command`, `propose_task`, `comment_on_issue`, `nothing_needed`). Every git push goes through the **real** sandbox git proxy.
- **Model credential:** a placeholder key file. Nothing calls a model.

## Item 1: an old schema-146 store

Script: [`item1_store_staterepo.sh`](2026-11-13-task-source-template/item1_store_staterepo.sh) ([SQLite](2026-11-13-task-source-template/out/item1-sqlite.out), [PostgreSQL](2026-11-13-task-source-template/out/item1-postgres.out)).

1. **Seed the store.** Seeded a template, a scratch task and a task filed from the template, then stopped the daemon.
2. **Make it a pre-`bcd2b5a83` store.** Ran `DROP TABLE task_source_template` (`grain_schema.version` stays 146) and removed `data/state-repo`, so the next start opens the existing store rather than importing.
3. **Start this build on it.** `Init` recreated `task_source_template` on both drivers, still at schema 146, with no errors in the log.
4. **Read the old tasks back through the API.**
   - The scratch task omits `templateId` on `GET /api/tasks?fields=full` and on `GET /api/tasks/{id}`: no `"templateId": ""` and no scan error.
   - The pre-existing template task still shows `templateId: template-1`, read from its surviving `task_filed_template` row through `hydrate`'s fallback.
5. **File a proposal naming the template** (`proposedBy`). It records `task_source_template` only, with no `task_filed_template` row.
6. **Export and check.** `grain state sync` exported the store and `grain state check` loaded it (`ok: grain can load this`). Neither `settings/` nor `tables/` carries any `task_source_template` file or reference.
7. **Round-trip through an external settings repository.**
   - Pointed the deployment at an external settings repository (`grain state repo <bare>`) and added a template there.
   - `grain state check` passed on the edited repository, and `grain state sync` applied it.
   - After a restart the new template is in the store, and the proposal still reads `templateId: template-1` on both routes, with `task_filed_template` still empty.

## Item 2: the authority boundary

Script: [`item2_authority_boundary.sh`](2026-11-13-task-source-template/item2_authority_boundary.sh) ([SQLite](2026-11-13-task-source-template/out/item2-sqlite.out), [PostgreSQL](2026-11-13-task-source-template/out/item2-postgres.out)).

**Setup:**
- The capability catalogue restricts `in_process.read-issues` to `templates: [release-note]`.
- The repository's declarations were read, and their holds were accepted.

**The probe:** every dispatched run tries `git push origin origin/main:refs/tags/walk-<label>-<task>` (the template's `push:`) and `grain issue list` (the template-restricted capability).

| Filing | `templateId` (list / detail) | `task_filed_template` | Push to `refs/tags/walk-*` | `grain issue list` |
|---|---|---|---|---|
| (a1) person `POST /api/tasks {templateId: release-note}` | release-note / release-note | 1 | allowed | allowed |
| (a2) schedule firing release-note (`POST /api/schedules/{id}/run`) | release-note / release-note | 1 | allowed | allowed |
| (a3) `walk-merge` path trigger, fired by merging (a1)'s PR | release-note / release-note | 1 | allowed | allowed |
| (b) `propose_task(template_id: release-note)`, approved by the operator | release-note / release-note | 0 | refused | refused |
| (c) agent `POST /api/tasks {templateId: plain-note}` (holding `api#tasks:write`) | plain-note / plain-note | 0 | refused | refused |
| (d) port to `release` of (a1)'s merge (`newPortTask`) | release-note / release-note | 0 | refused | refused (grant dropped, F1) |
| (e) scratch task | omitted / omitted | 0 | refused | refused |

The results are identical on both stores.

- **How the proxy refuses:** "refusing to update refs/tags/walk-… : a sandbox may push only its own task's branch … A task filed from a template declared in this repository's .grain/templates pushes other refs of it only where that template lists them under `push:`".
- **Agent API on a push template:** the same agent's `POST /api/tasks {templateId: release-note}` is refused before filing: "carries a declared push set … an agent cannot file, schedule, or modify a template that carries a declared push set".
- **(c) against a push rule:** (c) therefore used `plain-note`, and `push: [refs/tags/walk-*]` was added to `plain-note.yml` on `main` *after* the filing and before dispatch. (c) is still refused, because its `templateId` is provenance only.
- **Edit, reopen and redispatch:**
  - (a2) was edited (title).
  - (b), (c) and (d) were each edited (title and description), reopened and run again with `POST /api/tasks/{id}/run-now`.
  - All four keep `templateId` and keep `task_filed_template` as it was: 1 for (a2), 0 for the others.
  - The redispatched runs of (b), (c) and (d) are refused the push and the issue read again.

### F1: a port of a template-filed task never ran (fixed)

**Reproduction:** [`out/before/item2-sqlite.out`](2026-11-13-task-source-template/out/before/item2-sqlite.out), using the pre-fix binary.

**What happened:**
- `newPortTask` copies the source's grants. Here that includes `github#read-issues={}` from release-note.yml, but the port carries no `TemplateID`.
- `catalogueHold` then refused the grant on every dispatch: "capability https://grain.dev/capabilities/github#read-issues={} is not granted to an ad-hoc task by settings/capabilities.yml (templates: release-note) (in_process.read-issues, the capability it replaced)".
- The port stayed `queued`, and so did its run-now.
- The authority boundary held, since the port never got the grant. But every port of such a task was stuck.

**Fix:** `newPortTask` now judges each copied grant twice (`portCatalogueGrants`): once as the ad-hoc task a port is, and once as if filed from the source's template. It drops a grant only when the catalogue refuses the first and admits the second, which means the template's restriction is the only reason, and names it in the port's body. A grant refused for any other reason (disabled, a repository or kind restriction) stays on the port, so dispatch still holds the port on it, as it would hold the source. A deployment with no catalogue, or a source filed from no template, is unaffected. This narrowing answers security review [task 5747](/tasks/5747).

**Regression tests:** `TestAPortKeepsAGrantTheCatalogueRefusesForAnotherReason`, and `TestAPortDropsAGrantRestrictedToItsSourcesTemplate` (`pkg/orchestrator/port_template_grant_test.go`). It fails without the fix with the same refusal.

## Item 3: `/next/`

Script: [`item3_next_ui.sh`](2026-11-13-task-source-template/item3_next_ui.sh) → [`item3_next_ui.mjs`](2026-11-13-task-source-template/item3_next_ui.mjs) ([SQLite](2026-11-13-task-source-template/out/item3-sqlite.out), [PostgreSQL](2026-11-13-task-source-template/out/item3-postgres.out)).

Three tasks were filed through the New-task sheet, each opened with `c`:

| Task | Filed through | `templateId` (list / detail) |
|---|---|---|
| repository template `repo:template:acme:widgets:plain-note` | `POST /api/tasks/from-template` | plain-note / plain-note |
| deployment template `template-1` | `POST /api/tasks` (after F2) | template-1 / template-1 |
| scratch | `POST /api/tasks` | omitted / omitted |

- **Rendering:** `/next/tasks` and each `/next/task/<id>` rendered at 1440×900 and 390×844 with no sideways overflow and no page errors (8/8 checks per store).
- **Screenshots:** `make ui-screenshots PAGES=/next/tasks,/next/inbox` produced the contact sheet attached to the PR. My file reader shows PNGs only as bytes, so the visual check rests on these automated checks, not on viewing the images. The fix changes no markup or CSS, only which route the sheet posts to.

### F2: filing a deployment template from the sheet answered 404 (fixed)

**What happened:**
- `fromTemplateStem` (`ui-ng/src/app/create/new-task-form.ts`) treated any template id without a colon as a stem of the target repository's `.grain/templates`.
- Picking `template-1` therefore posted `POST /api/tasks/from-template {template: "template-1"}`.
- The server looked up `repo:template:acme:widgets:template-1` and answered `404 {"error":"no template repo:template:acme:widgets:template-1"}`. Captured in run 1 of this task on both stores.

**Fix:** a stem now comes only from a repository-declared id. That is `model.RepoDeclaredID`'s `repo:template:<owner>:<name>:<stem>` (the function only knew the older `declared:` spelling) or a template whose `declaredIn` names the repository. Everything else goes through `POST /api/tasks {templateId}`.

**Regression test:** a unit test in `new-task-form.spec.ts`.

## Store tests

`GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model/... ./pkg/staterepo ./pkg/ports/... ./tests/hygiene/...` passed. That includes `pkg/model/postgres` against the local PostgreSQL 15. `go test ./pkg/orchestrator` and the `pkg/ui` `templateId` tests also passed.

## F3: method grants with and without `={}` (follow-up)

**Observation:** in run 1 a driver task held `https://grain.dev/capabilities/github#read-issues={}`. Its agent `POST /api/tasks {templateId}` for a template granting `https://grain.dev/capabilities/github#read-issues` was refused: "template … grants https://grain.dev/capabilities/github#read-issues, which the agent's own task does not hold".

**Cause:**
- `model.CanonicalGrantKey` keeps the two spellings distinct, and `TemplateWithinTask` compares them with that key.
- Tasks store a method grant as `…={}` (`task_grant`), while declared templates store it bare (`template_grant`).
- So an agent can never name a template granting a method it does hold.

**What it touches:** this refuses more than intended, never less, and the boundary walked here is not at risk. Proposed as a follow-up rather than fixed here, because the comparison is shared by review-template and filing checks.

## Fix commits

- `fix(orchestrator): file a port without grants the catalogue restricts to its source's template` (F1, plus the helpers).
- `fix(ui-ng): file a deployment template from the New-task sheet through POST /api/tasks` (F2).
- `docs(test-runs): …` (this report and the raw output).
