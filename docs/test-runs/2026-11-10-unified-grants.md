# Unified Grants Walkthrough: Canonical References, Repository Grant Lines & Proposal Rails — 2026-11-10

Walkthrough execution of `docs/test-plans/2026-11-10.md` §3 ("Unified grants: canonical references, repository grants in GrantsPicker, held bare ids, proposal rails"), filed from [Task #3930](/tasks/3930).

## Scope & Environment

- **Commits tested:**
  - Pre-canonical baseline: `c08832a2` (`SchemaVersion = 129`, built in `/tmp/c08832a2` to `/tmp/old/grain-c08832a2`)
  - Target `HEAD`: `c7651098f` (`SchemaVersion = 133`) plus the three scoped fixes landed on this branch (`grain/098073e4/task-3950-walkthrough-unified-grants`). Note that between the test plan's cutoff (`61668e58`) and `c7651098f`, Slice 4 (`6f0f15cde`, migration 132 canonical grant store migration) and skills migration 133 (`3fa456fe2`) also landed on `main`.
  - PRs in scope: #944 (`4a0d3155`, `ce4a7094`, `c4f3d595`), #1052 (`b5517814`, `aaf05ffc`), #1024 (`f4b4b649`), #1022 (`d76d2791`, `2570f150`, `bac5ad1a`), #1020 (`663e99ee`), #1018 (`d45aa842`).
- **Databases:** Embedded SQLite (`3.40.1`) and real PostgreSQL (`15.19` cluster on `127.0.0.1:5432`, role `debian`).
- **GitHub & Git simulator:** [`ghsim.go`](2026-11-10-unified-grants/ghsim.go) serving real bare repositories (`acme/widgets`, `acme/lib`, `acme/other`, and long-name repo fixtures) over `git http-backend` alongside `pkg/github/githubsim`.
- **Real dispatch & MCP harness:** `grain daemon -host-sandboxes` driving [`fake-agy.sh`](2026-11-10-unified-grants/fake-agy.sh) and [`hook_3_4.py`](2026-11-10-unified-grants/hook_3_4.py) over real stdio `grain mcpserver` (`-interactive` for live proposals and non-interactive `-out` JSONL relay for run-end proposals).
- **Browser & viewports:** Real Playwright Chromium (`1234`) at `1440×900` (desktop) and `390×844` (phone) across light and dark themes (`go1.26.10 linux/amd64`, Node `v20.19.0`).

### Reproduction Commands

```bash
# Build baseline (c08832a2, schema 129) in /tmp and fixed HEAD in ./work
git clone . /tmp/c08832a2 && git -C /tmp/c08832a2 checkout c08832a2
(cd /tmp/c08832a2/ui && npm ci && npm run build)
mkdir -p /tmp/old && (cd /tmp/c08832a2 && go build -o /tmp/old/grain-c08832a2 ./cmd/grain)

(cd ui && npm ci && npm run build)
mkdir -p "$HOME/bin" && go build -o "$HOME/bin/grain" ./cmd/grain

# Item 1: Schema 129 -> 133 upgrade, UI rendering, partial edits, admission policy & state sync/check
bash docs/test-runs/2026-11-10-unified-grants/item1_upgrade.sh sqlite
bash docs/test-runs/2026-11-10-unified-grants/item1_upgrade.sh postgres

# Items 2 & 5: Repository grant lines on all 5 surfaces + GitHub-simulator bad declarations
bash docs/test-runs/2026-11-10-unified-grants/item2_5_walkthrough.sh

# Items 3 & 4: Real -host-sandboxes dispatches (live vs relayed proposals + undeclared-arg refusals)
bash docs/test-runs/2026-11-10-unified-grants/items3_4_walkthrough.sh

# Item 6: Long-name & multi-repo visual/bounding-box layout checks (1440x900 & 390x844, light/dark)
bash docs/test-runs/2026-11-10-unified-grants/item6_visual.sh after
```

---

## Defects Found & Fixed

**Security review boundary check:** None of `pkg/capability/*`, `pkg/model/canonical_grant.go`, or `pkg/orchestrator/finish.go` were modified. All three fixes sit in `pkg/ui/{tasks,repos,teams}.go` and `ui/src/{state.js,components/ConsoleSessionPage.jsx}`, so the capability/canonical-grant/finish security review is **not** triggered.

| ID | Surface | Symptom Reproduced | Root Cause | Fix & Regression Test |
|---|---|---|---|---|
| **F1** | `GET /api/config` & New task overlay (`pkg/ui/tasks.go`, `ui/src/state.js`) | 1. When deployment or repo `defaultCapabilities` held bare `self-debug` (migrated intact from `c08832a2`), opening **New task** silently omitted `self-debug` from the pre-filled Grants list even though filing a task still expanded and attached `api#self-debug` + `grain#source={}` server-side.<br>2. When deployment defaults held unmigrated `api:tasks:read` and repo defaults held canonical `https://grain.dev/capabilities/api#tasks:read`, the New task overlay rendered **two** identical `api:tasks:read` rows (`rowCount: 3` instead of `4` in `out/item6-layout-before.json`). | `GET /api/config` filtered `cfg.DefaultCapabilities` and `rc.DefaultCapabilities` through `s.tasks.capabilityByID(id) != nil`, which returns `nil` for retired bare alias `self-debug` and passed legacy `api:` strings through uncanonicalized; `unionCapabilities` in `ui/src/state.js` keyed deduplication by raw string rather than `storedApiGrant(id)`. | `pkg/ui/tasks.go` now resolves both slices through `s.tasks.resolveDefaultCapabilities(...)` (expanding `self-debug` -> `api#self-debug` + `grain#source={}` and canonicalizing `api:` -> `api#`). `ui/src/state.js` `unionCapabilities` deduplicates API grants via `storedApiGrant(id)`.<br>**Tests:** `TestConfigRouteExpandsRetiredAliasesAndCanonicalizesDefaultCapabilities` (`pkg/ui/default_grants_test.go`); `"deduplicates canonical api# references against legacy api: strings"` (`ui/src/state.test.js`). |
| **F2** | Repo CI capabilities (`PUT /api/repos/{owner}/{name}/capabilities` in `pkg/ui/repos.go` & `pkg/ui/teams.go`) | 1. If `repo_config.ci_capabilities` held a retired/unsupported bare id (`self-debug` or `release-gate-read`) from an older store, adding an unrelated CI capability on `/repos/acme/lib#ci` failed with **HTTP 400** (`capability "self-debug" is not supported for CI`), blocking partial edits.<br>2. Conversely, `cleanRepoCICapabilities` rejected legacy `api:*` (`strings.HasPrefix(id, "api:")`) as unsupported for CI, but failed to check canonical `https://grain.dev/capabilities/api#*` (`capability.ParseAPIReference`), allowing task-only Grain API grants to be written into `ciCapabilities`. | `cleanRepoCICapabilities` lacked `teams.go`'s `slices.Contains(existingHeld, id)` exemption for already-held bare ids when `s.tasks.capabilityByID(id)` was `nil`/non-CI, and checked only `strings.HasPrefix(id, "api:")` rather than `capability.ParseAPIReference(id)` before calling `s.tasks.validateCapabilityRef`. | Preserved already-held bare ids (`existingHeld`) in `cleanRepoCICapabilities` across both `pkg/ui/repos.go` and `pkg/ui/teams.go` while refusing newly added bare ids and refusing canonical `https://grain.dev/capabilities/api#*` references with HTTP 400 `not supported for CI`.<br>**Tests:** Extended `TestRepoCICapabilitiesAPIKeepsTaskDefaultsIndependent` (`pkg/ui/repo_ci_capabilities_test.go`). |
| **F3** | Console session page (`ui/src/components/ConsoleSessionPage.jsx`) | On `/session/director` (where `readsAll: true` populates `session.autoReads` with every managed repo except `grain/config`), clicking **Add grant… → Read a repository** still offered `acme/widgets`, `acme/lib`, and `acme/other` even though they were already listed in `"Reads every repository this deployment works in"` and `PUT /api/console-sessions/director` immediately strips `autoReads` entries on save. | `repos.readOptions` filtered only `session.reads` (`readSet`) and omitted `session.autoReads`. | Filtered `session.autoReads` (case-insensitively alongside `session.reads`) out of `repos.readOptions` in `ConsoleSessionPage.jsx`, leaving only non-auto-read repos (`grain/config`) selectable when `readsAll: true`.<br>**Tests:** `"excludes autoReads repositories from Read a repository options"` (`ui/src/components/ConsoleSessionPage.test.jsx`). |

---

## Detailed Results by Walkthrough Item

### Item 1: Canonical `api#` & `repo#` Storage, Upgrade (`c08832a2` → `HEAD`), UI Deduplication & Partial Edits

Harnesses: [`item1_upgrade.sh`](2026-11-10-unified-grants/item1_upgrade.sh) and [`item1_ui_check.mjs`](2026-11-10-unified-grants/item1_ui_check.mjs). Captured outputs (byte-identical structural results across SQLite and PostgreSQL):
- Rehearsal: [`out/item1-prepare-upgrade-sqlite.txt`](2026-11-10-unified-grants/out/item1-prepare-upgrade-sqlite.txt), [`out/item1-prepare-upgrade-postgres.txt`](2026-11-10-unified-grants/out/item1-prepare-upgrade-postgres.txt)
- UI & partial-edit `GET` snapshots: [`out/item1-ui-sqlite.json`](2026-11-10-unified-grants/out/item1-ui-sqlite.json), [`out/item1-ui-postgres.json`](2026-11-10-unified-grants/out/item1-ui-postgres.json)
- Admission policy denial: [`out/item1-admission-sqlite.json`](2026-11-10-unified-grants/out/item1-admission-sqlite.json), [`out/item1-admission-postgres.json`](2026-11-10-unified-grants/out/item1-admission-postgres.json)
- `grain state sync` / `grain state check`: [`out/item1-state-sync-sqlite.txt`](2026-11-10-unified-grants/out/item1-state-sync-sqlite.txt), [`out/item1-state-check-sqlite.txt`](2026-11-10-unified-grants/out/item1-state-check-sqlite.txt), [`out/item1-state-check-postgres.txt`](2026-11-10-unified-grants/out/item1-state-check-postgres.txt)

1. **Upgrade rehearsal & migration (`129 -> 133`):**
   - Seeded on `c08832a2` (schema 129) with `api:tasks:read@acme/widgets`, `api:test-quarantine`, `repo-read:acme/lib`, and bare `self-debug` across a task (`#2`), template (`tmpl-1`), schedule (`sched-1`), deployment defaults, repo defaults (`acme/widgets`), console session `ops` (with `api:ci-read` unticked into `declinedDefaults`), and `acme/lib` `ciCapabilities` (`self-debug`, `https://grain.dev/capabilities/github#sandbox-repo={}`).
   - `grain state prepare-upgrade` rehearsed cleanly on both engines: `rehearsed: 123 rows across 28 tables migrate from schema 129 into 133`.
   - Starting `HEAD` migrated all `api:` and `repo-read:`/`repo-write:` strings (including `ops.declinedDefaults`) to canonical `https://grain.dev/capabilities/api#...` and `.../repo#read|#write` references via migration 132, while keeping bare `self-debug` intact on every object.
2. **Single-line rendering & partial edits (no duplicates, no silent drops, no cross-object side effects):**

   | Surface | Initial Render After Upgrade | Partial Edit Performed | Stored Result After Edit |
   |---|---|---|---|
   | **New task overlay** | 4 lines (`#target`, `api#ci-read`, `api#self-debug`, `grain#source={}`), `hasDuplicates: false` (after F1 fix) | Inspected pre-filled defaults for `acme/widgets` | No write; retired `self-debug` expanded cleanly once |
   | **Task pane** (`/tasks/2`) | 5 lines (`#target`, `repo#read` `acme/lib`, bare `self-debug` retired-alias row, `api#tasks:read={"repo":"acme/widgets"}`, `api#test-quarantine`) | Added `api:tasks:write` via `GrantsPicker` (`POST /api/tasks/2/capabilities`) | `["https://grain.dev/capabilities/api#tasks:read={\"repo\":\"acme/widgets\"}", "https://grain.dev/capabilities/api#tasks:write", "https://grain.dev/capabilities/api#test-quarantine", "self-debug"]` — bare `self-debug` untouched, new grant canonical |
   | **Template editor** | 5 lines, `hasDuplicates: false` | Added `api:tasks:write` and saved (`PUT /api/templates/...`) | Normalized once (`self-debug` -> `api#self-debug` + `grain#source={}`, plus `api#tasks:write`), `reads: ["acme/lib"]` preserved |
   | **Schedule editor** | 5 lines, `hasDuplicates: false` | Added `api:tasks:write` and saved (`PUT /api/schedules/...`) | Normalized once, `reads: ["acme/lib"]` preserved |
   | **Console session** (`/session/ops`) | `api#ci-read` stayed **off** (declined default respected across migration); `self-debug` + `api#tasks:read` + `repo#read` shown once | Added `api:tasks:write` and saved (`PUT /api/console-sessions/ops`) | `declinedDefaults: ["https://grain.dev/capabilities/api#ci-read"]` kept; `api#ci-read` did not re-enable |
   | **Settings → Tasks** (`/settings/tasks`) | Deployment defaults shown once, `hasDuplicates: false` | Added `api:tasks:write` and saved (`PUT /api/settings`) | Normalized once (`api#ci-read`, `api#self-debug`, `api#tasks:write`, `grain#source={}`) |
   | **Repo defaults** (`/repos/acme/widgets#agents`) | Repo defaults shown once, `hasDuplicates: false` | Added `api:tasks:write` and saved | Normalized once (`api#self-debug`, `api#tasks:read={"repo":"acme/widgets"}`, `api#tasks:write`, `grain#source={}`) |
   | **Repo CI capabilities** (`/repos/acme/lib#ci`) | Held `self-debug` + `github#sandbox-repo={}` | Added `release#registry-push={}` (`PUT /api/repos/acme/lib/capabilities`) | **HTTP 200** (after F2 fix): kept held `self-debug` + `github#sandbox-repo={}` + `release#registry-push={}`; canonical `api#tasks:read` on `ciCapabilities` rejected with **HTTP 400** |

3. **Admission `CapabilityPolicy` denial (`HasCapabilityRef == false`):**
   - Configured rule `deny https://grain.dev/capabilities/api#tasks:read` on `PUT /api/capability-policy`.
   - Dispatched two tasks on `acme/widgets`: Task `#3` submitted with legacy `"api:tasks:read@acme/widgets"` and Task `#4` submitted with canonical `"https://grain.dev/capabilities/api#tasks:read={\"repo\":\"acme/widgets\"}"`.
   - Both normalized to canonical storage and both were blocked before sandbox creation (`state: "awaiting_reply"`) with identical refusal comments: `grain refused the capability https://grain.dev/capabilities/api#tasks:read, so no sandbox was built for it: the capability policy denies https://grain.dev/capabilities/api#tasks:read to this task on acme/widgets (rule 1: deny https://grain.dev/capabilities/api#tasks:read)`.
4. **`grain state sync` & `grain state check`:**
   - `grain state sync` exported cleanly to `grain/config`, and `grain state check` loaded all 47 rows across 10 tables at schema 133 (`ok: grain can load this`) on both SQLite and PostgreSQL.

---

### Item 2: Repository Read & Write Lines in `GrantsPicker`

Harnesses: [`item2_5_walkthrough.sh`](2026-11-10-unified-grants/item2_5_walkthrough.sh) and [`item2_ui.mjs`](2026-11-10-unified-grants/item2_ui.mjs); output in [`out/item2-ui.json`](2026-11-10-unified-grants/out/item2-ui.json).

- **All five surfaces** (**New task** overlay, **Task pane**, **Template editor**, **Schedule editor**, **Console session** `/session/ops`):
  - Added `Read a repository` -> `acme/lib` and `Write to a repository` -> `acme/other` on non-default base branch `staging`, saved, reloaded, verified `GET` stored `reads: ["acme/lib"]` and `writes: [{"repo": "acme/other", "base": "staging"}]`, then removed both lines, saved, and reloaded (`reads: []`, `writes: []`; on the task pane, `grantEvents` was also verified empty).
  - **Focus return (`aaf05ffc`):** Immediately after clicking **Add** on both `"Read a repository"` and `"Write to a repository"` across all 5 surfaces, `document.activeElement` returned to `BUTTON` `"Add grant…"` (`focusedAddGrant: true` on 10/10 checks).
- **Unremovable `#target` line:** Present with `buttonCount: 0` (`removable: false`) and surface-specific explanatory copy on the 4 repo-bound surfaces (New task, Task pane, Template editor, Schedule editor), and absent (`present: false`) on the unbound Console session page.
- **Read-only `push:` lines:** On a task filed from declared template `acme/widgets:release` (carrying `push: [refs/heads/candidate, +refs/tags/latest]`), the task pane rendered two read-only push lines (`acme/widgets refs/heads/candidate · push` and `acme/widgets refs/tags/latest (force) · push`, `"declared by the template"`) with `buttons: 0`.
- **CI `repo#read` separation:** In every task/template/schedule/session `Add grant…` menu where `reads` are edited, raw `https://grain.dev/capabilities/repo#read` method entries had `ciMethodCount: 0` while `"Read a repository"` had `repoReadRowCount: 1`. On `/repos/acme/widgets#ci` (where `reads` is not a separate list), `https://grain.dev/capabilities/repo#read` appeared in `Add grant…` (`ciMethodCount: 1`, `repoReadRowCount: 0`).
- **Console session `readsAll: true` (`Director`):** After F3 fix, `/session/director` offered only `["grain/config"]` under `"Read a repository"`, omitting auto-read repositories already covered by `readsAll`.

---

### Item 3: Person-Only Capabilities & Uncovered Bare IDs on Live vs Relayed Proposals

Harnesses: [`items3_4_walkthrough.sh`](2026-11-10-unified-grants/items3_4_walkthrough.sh), [`fake-agy.sh`](2026-11-10-unified-grants/fake-agy.sh), and [`hook_3_4.py`](2026-11-10-unified-grants/hook_3_4.py); output in [`out/item3-results.json`](2026-11-10-unified-grants/out/item3-results.json).

Dispatched 4 real parent tasks with `autoMerge: true` on `grain daemon -host-sandboxes`:
- **Pair 1** (Task `#2` live `interactive: true` vs Task `#3` relayed `interactive: false`): parent held `["https://grain.dev/capabilities/api#self-upgrade"]`.
- **Pair 2** (Task `#4` live `interactive: true` vs Task `#5` relayed `interactive: false`): parent held `["https://grain.dev/capabilities/api#self-upgrade", "https://grain.dev/capabilities/api#tasks:read"]`.

Each parent proposed four children (`self-debug`, `https://grain.dev/capabilities/api#self-upgrade`, `https://grain.dev/capabilities/api#secrets:write`, and `https://grain.dev/capabilities/api#tasks:read`):

| Parent Grants (`autoMerge: true`) | Child Capability Requested | Live (`propose_task` interactive) | Run-End Relayed (`relayProposedTasks`) | Stored Child `capabilities` |
|---|---|---|---|---|
| `api#self-upgrade` | `"self-debug"` | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `[".../api#self-debug", ".../grain#source={}"]` |
| `api#self-upgrade` | `api#self-upgrade` *(held by parent, person-only)* | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `[".../api#self-upgrade"]` |
| `api#self-upgrade` | `api#secrets:write` *(person-only)* | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `[".../api#secrets:write"]` |
| `api#self-upgrade` | `api#tasks:read` *(not held by parent)* | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `proposed` (`heldForPerson: true`, `autoMerge: false`) | `[".../api#tasks:read"]` |
| `api#self-upgrade` + `api#tasks:read` | `api#tasks:read` *(held & non-dangerous)* | Auto-submitted (`heldForPerson: false`, `autoMerge: true`) | Auto-submitted (`heldForPerson: false`, `autoMerge: true`) | `[".../api#tasks:read"]` |

`parityDiffs` between live and relayed children was `[]` on both pairs.

---

### Item 4: `propose_task` Undeclared-Argument Refusals Over Real MCP

Output in [`out/item4-results.json`](2026-11-10-unified-grants/out/item4-results.json). Called `propose_task` over a real stdio `grain mcpserver` session for all 7 mistyped/undeclared shapes on both `-interactive` (Task `#6`) and relayed JSONL (`relayProposedTasks`, Task `#7`):

| Case | MCP `isError` | Refusal Returned to Caller |
|---|---|---|
| `base: "release"` | `true` | `propose_task: unknown argument "base" (did you mean "branch"?)` |
| `description: "..."` | `true` | `propose_task: unknown argument "description" (did you mean "body"?)` |
| `fields: {"env": "staging"}` | `true` | `propose_task: unknown argument "fields" (did you mean "template_fields"?)` |
| `approve: true` | `true` | `propose_task: unknown argument "approve" (did you mean "hold_for_approval"?)` |
| `capabilites: [".../api#ci-read"]` | `true` | `propose_task: unknown argument "capabilites" (did you mean "capabilities"?)` |
| `writes: [{"repo": "acme/lib", "branch": "main"}]` | `true` | `propose_task: writes[0]: unknown field "branch" (did you mean "base"?)` |
| `dependsOn: ["1"]` | `true` | `propose_task: unknown argument "dependsOn" (did you mean "depends_on"?)` |

Zero tasks were filed from any refused call. When replayed into `relayProposedTasks` at run end, `daemon.log` logged `orchestrator: skipping proposed task with invalid arguments: ...` for each entry without posting error noise onto the parent task's conversation (`relayParentComments` contained only the agent's normal closing comment).

---

### Item 5: Declared `repo#target` & Base-Carrying Repo Grants Refused Without Breaking Previous Declarations

Outputs in [`out/item5-sync.json`](2026-11-10-unified-grants/out/item5-sync.json), [`out/item5a-ui.json`](2026-11-10-unified-grants/out/item5a-ui.json), and [`out/item5b-ui.json`](2026-11-10-unified-grants/out/item5b-ui.json).

1. Seeded `acme/widgets` on `ghsim` with valid `.grain/templates/release.yml` (commit `f3c6f4b1`) and synced (`declarationsStatus.commit == "f3c6f4b1..."`, live template `repo:template:acme:widgets:release`).
2. **Pushed `.grain/templates/x.yml` declaring `https://grain.dev/capabilities/repo#target`:**
   - `grain declarations check -repo acme/widgets` exited `1`: `.grain/templates/x.yml: capabilities: "https://grain.dev/capabilities/repo#target": https://grain.dev/capabilities/repo#target is the task's own repository -- set `base:` on the template or schedule, not in `capabilities``.
   - After daemon sync, `liveDeclaredTemplates` kept `repo:template:acme:widgets:release` from `f3c6f4b1`, `declarationsStatus.error` recorded the refusal, and `/repos/acme/widgets#advanced` rendered `Last read failed: reading acme/widgets's declarations: ...`.
3. **Pushed `.grain/templates/x.yml` declaring `https://grain.dev/capabilities/repo#write` with `with: {repo: acme/lib, base: staging}`:**
   - `grain declarations check -repo acme/widgets` exited `1`: `a declared write to another repository always lands on its default branch -- pass only repo: <owner>/<repo>, and set this repository's own branch with base:`.
   - Previous declarations (`release.yml` at `f3c6f4b1`) remained live while `/repos/acme/widgets#advanced` surfaced the new error.
4. **`repo#read` with `base` (`grain declarations check`):** Exited `1` with `https://grain.dev/capabilities/repo#read takes only repo: <owner>/<repo>, not base`.

---

### Item 6: Visual & Layout Inspection (`1440×900` & `390×844`, Light & Dark)

Harnesses: [`item6_visual.sh`](2026-11-10-unified-grants/item6_visual.sh) and [`item6_layout_check.mjs`](2026-11-10-unified-grants/item6_layout_check.mjs).
- Bounding-box geometry checks: [`out/item6-layout-before.json`](2026-11-10-unified-grants/out/item6-layout-before.json) and [`out/item6-layout-after.json`](2026-11-10-unified-grants/out/item6-layout-after.json) — **16/16 checks passed** (`overflowX: false`, all `[data-grant-line]` rows and remove buttons inside their container rects with zero vertical overlap, `pageErrors: []`) across `taskPane` (12 grant lines including `organization-with-a-very-long-name/platform-monorepo-services` on `release/2026-11-10-extremely-long-integration-branch-name`, `repo-read-01`..`06`, retired `self-debug`, and `api#tasks:read`), `templateOverlay`, `newTaskOverlay` (`rowCount` improved from `3` before F1 fix to `4` after F1 fix), and `directorSession`.
- Contact sheets in [`2026-11-10-unified-grants/`](2026-11-10-unified-grants/):
  - [`contact-sheet.png`](2026-11-10-unified-grants/contact-sheet.png) (Task pane & Template overlay with 10+ repo & long-branch grant lines, 1440×900 & 390×844, light & dark)
  - [`contact-sheet-new-task-before.png`](2026-11-10-unified-grants/contact-sheet-new-task-before.png) vs [`contact-sheet-new-task-after.png`](2026-11-10-unified-grants/contact-sheet-new-task-after.png) (F1 fix on New task overlay)
  - [`contact-sheet-director-before.png`](2026-11-10-unified-grants/contact-sheet-director-before.png) vs [`contact-sheet-director-after.png`](2026-11-10-unified-grants/contact-sheet-director-after.png) (F3 fix on `/session/director` with `Add grant… → Read a repository` open)
