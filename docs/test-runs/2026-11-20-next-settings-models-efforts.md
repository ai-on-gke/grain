# Walkthrough: `/next/settings` model efforts, guards and schema 153 migration

**Test plan:** [`docs/test-plans/2026-11-20.md`](../test-plans/2026-11-20.md) §6 (PRs #1554, #1551, #1565, #1899, #1820, #1843, #1586)  
**Task:** [Task #7689](/tasks/7689) (filed by [Task #7543](/tasks/7543))  
**Date:** 2026-11-20

## Summary

- Walked all six "What is NOT proved" items of §6 plus every required `/next/settings/*` page (`/next/settings/models`, `/next/settings/compute`, `/next/settings/capabilities`, `/next/settings/sandboxes`, `/next/settings/hosts`, `/next/settings/access`) and classic `/settings/models` (item 6 only).
- **Items 1, 3, and 5** ran against real `grain daemon -sandbox-port-base=0` instances over **both SQLite and PostgreSQL**, with a scripted fake `agy` CLI (`-agy-path`) recording exact runner argv/env and emitting real quota refusals, and a local bare-repo GitHub simulator (`docs/test-runs/2026-11-19-next-processes-and-reconcile/sim.go`). Item 5 also ran a real schema-152 binary built from `9dad3601a` (parent of PR #1586) to verify the full 153→152 downgrade note path end to end on both SQLite and PostgreSQL.
- **Items 2, 4, and 6** ran against real `grain demo -github-sim -fake-ci -secrets-store`.
- **Two scoped UI bugs were found and fixed** in this branch with Vitest unit tests, Playwright e2e regression tests, and before/after screenshots across desktop (1440×900) and phone (390×844) in light and dark themes (0 horizontal overflow, 0 WCAG 2.1 AA axe violations):
  1. **Bug 1 (Item 4):** When `PUT /api/deployment` fails while saving `modelSettings` or `defaultModels` on `/next/settings/models` (or `agentGitName` / `agentGitEmail` on `/next/settings/git`), the section header misreported **`Couldn't save Deployment identity`** instead of naming the edited setting (`Model reasoning settings`, `Default model`, `Commit author name`, `Commit author email`), and `readableSaveError` left deployment JSON field prefixes untranslated in the error banner.
  2. **Bug 2 (Item 3):** When `defaultModels` has multiple entries (e.g. `gemini-3.1-pro,gemini-3.8-flash`) and only the last fallback model (`gemini-3.8-flash`) is paused while the primary default (`gemini-3.1-pro`) is healthy and dispatching, `summarizeDispatchStatus` matched `pkg/usage`'s `"work falls back to nothing -- it is the last model on the default list"` warning and falsely escalated the banner to a red, un-hideable error (`Model quotas exceeded (no fallback models left) — development paused`).
- **One larger UX parity gap filed as a follow-up:** on `/next/settings/models`, when two connections are paused simultaneously, the top `models-quota-banner` combines them into one `Resume` button (confirming and lifting both via scoped `DELETE /api/pause?...` calls) and the `Model connections` table shows neither a `Paused` badge nor a per-connection `Resume` action (unlike `/next/fleet?tab=controls` and classic `/settings/models`). Filed as a follow-up task rather than adding new table controls in this walkthrough PR.

## Environment

| Component | Version / configuration |
| --- | --- |
| Repository & base commit | `ai-on-gke/grain`, `HEAD` `36e7abf3a` (branch `grain/53549e42/task-7689-walkthrough-next-settings`) |
| Schema-152 downgrade binary | Built from `9dad3601a` (`const SchemaVersion = 152`, parent of PR #1586) |
| Build | `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (rebuilt `ui-ng` + `grain` after fixes) |
| Go / Node | Go `1.26.10` (`linux/amd64`), Node `v20.19.0` |
| SQLite / PostgreSQL | SQLite `3.40.1`, PostgreSQL `15.20` (`Debian 15.20-0+deb12u1`, local cluster on `127.0.0.1:5432`) |
| Browser & Playwright | Playwright `1.62.1`, headless Chromium (`Google Chrome for Testing 151.0.7922.34`), `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC` |
| Viewports & themes | Desktop `1440×900`, phone `390×844`; light and dark themes |
| Real daemon (items 1, 3, 5) | `grain daemon -sandbox-port-base=0 -host-sandboxes -as alice@example.com -repo acme/widgets -default-target-repo acme/widgets -poll-interval 1s -max-workers 1 -max-mergers 0 -agent-framework antigravity -agy-path <fake-agy.py> -github-host <sim> -github-insecure-http` (SQLite on `127.0.0.1:4281`/`4288`, PostgreSQL on `127.0.0.1:4283`/`4289`) |
| Demo server (items 2, 4, 6) | `grain demo -addr 127.0.0.1:4285 -open=false -access-code-file <tmp> -github-sim -fake-ci -secrets-store` |

Throwaway harness scripts and logs lived under `/var/tmp/walk-s6/` (not committed). No Gemini API key was present in the sandbox, so visual checks used Playwright DOM geometry, `scrollWidth`/`clientWidth`, axe-core WCAG 2.1 AA audits, and rendered before/after PNG contact sheets.

## Stubs

| Boundary | Stub used | Why the real boundary was unavailable |
| --- | --- | --- |
| Model provider / `agy` CLI (items 1 & 3) | Python fake `agy` CLI passed via `-agy-path` speaking stream-json + real `grain-sandbox` MCP (`nothing_needed`) and emitting real `Individual quota reached... Resets in Nh.` errors on demand | No live LLM provider credentials in sandbox; also required to capture exact `--model` / `--effort` argv and `GEMINI_API_KEY` env per run |
| GitHub remote (items 1, 3, 5) | Local bare git repo + `docs/test-runs/2026-11-19-next-processes-and-reconcile/sim.go` | Sandboxes have no outbound GitHub API access |
| Failed save write (item 4) | Single-request Playwright `page.route` returning HTTP `503` on the first `PUT /api/deployment`, `POST /api/settings`, or `PUT /api/memory`, then falling through to the real server | `grain demo` does not fail valid writes on its own; needed to prove drafts survive a failed save and Retry succeeds |
| GitHub / CI in `grain demo` (items 2, 4, 6) | Built-in `-github-sim -fake-ci` flags | Standard demo mode without external GitHub access |

No API responses were stubbed during the real-daemon SQLite/PostgreSQL runs (items 1, 3, 5).

## Item-by-item results

### Item 1 — Effort reaches the runner (default, fallback after pause, effort-less model) — PASS (SQLite & PostgreSQL)

**Setup:** Three real Antigravity connections imported into the daemon:
- `conn-primary` (`Primary Gemini`, models: `["gemini-3.1-pro"]`, key tag `key-primary`)
- `conn-fallback` (`Fallback Flash`, models: `["gemini-3.8-flash"]`, key tag `key-fallback`)
- `conn-effortless` (`Effortless Agy`, models: `["gemini-3.1-pro-high", "auto"]`, key tag `key-effortless`)

**Steps & observations (identical on both SQLite and PostgreSQL):**
1. Opened `/next/settings/models` in Playwright, set default model `gemini-3.1-pro` effort to `high`, added fallback `gemini-3.8-flash` and set its effort to `low`, clicked **Save changes**, reloaded, and verified persistence via `GET /api/deployment` (`defaultModels: ["gemini-3.1-pro","gemini-3.8-flash"]`, `modelSettings: [{model:"gemini-3.1-pro",effort:"high"},{model:"gemini-3.8-flash",effort:"low"}]`).
2. Filed and approved Task A on `acme/widgets` with no pinned model. `fake-agy.py` recorded:
   - `--model gemini-3.1-pro --effort high`, `GEMINI_API_KEY=key-primary` → completed via `nothing_needed`.
3. Scripted `fake-agy.py` to fail the next `gemini-3.1-pro` run with `Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h.` and filed/approved Task B with no pinned model:
   - Attempt 1 received `--model gemini-3.1-pro --effort high` (`key-primary`), failed with quota error, and the daemon persisted a real `usage_limit_pause` row (`connection|conn-primary|gemini-3.1-pro`).
   - Attempt 2 immediately fell back to `conn-fallback` and received **`--model gemini-3.8-flash --effort low`** (`GEMINI_API_KEY=key-fallback`), completing cleanly.
4. Stored pre-existing efforts on effort-less models (`{model:"gemini-3.1-pro-high", effort:"low"}` and `{model:"auto", effort:"low"}`) via `PUT /api/deployment` (HTTP 200).
   - Verified `/next/settings/models` surfaced both in the "other models" table with clear annotations: `(not applied: Effort is part of the model name: high.)` and `(not applied: The provider chooses the effort.)`.
   - Filed and approved Task C pinned to `gemini-3.1-pro-high` and Task D pinned to `auto`. `fake-agy.py` recorded `--model gemini-3.1-pro-high` and `--model auto` with **no `--effort` flag** (`has_effort_flag: false`, `effort: null`) on both SQLite and PostgreSQL.

### Item 2 — Pre-existing effort table & preservation on save — PASS (`grain demo`)

**Steps & observations:**
1. Seeded connections for Antigravity (`gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-3.1-pro-high`, `opus-5.5-xhigh`, `auto`), Claude (`claude-sonnet-4-5`), and Codex (`gpt-5.5`, `gpt-5.1-codex-max`), plus pre-existing `defaultModels: ["gemini-2.5-pro", "gemini-2.5-flash"]` and `modelSettings` entries for models both inside and outside the default chain (`gemini-2.5-pro=high`, `gemini-2.5-flash=low`, `gpt-5.5=medium`, `gpt-5.1-codex-max=high`, `gemini-3.1-pro-high=low`, `claude-opus-5-5=high`).
2. Loaded `/next/settings/models`:
   - Default model cell showed `high`; fallback row 0 showed `Effort / low`.
   - Non-chain entries remained visible in the "Other model reasoning efforts" table: `gpt-5.5` and `gpt-5.1-codex-max` (Codex `-max` correctly unmarked as unused), while `gemini-3.1-pro-high` and unadvertised `claude-opus-5-5` showed `(not applied: Effort is part of the model name: high.)` and `(not applied: Claude takes no reasoning effort.)`.
3. Changed default model effort `high → medium`, clicked **Save changes**, and checked `GET /api/deployment`: `gemini-2.5-pro` updated to `medium` while **all four non-chain entries (`gpt-5.5`, `gpt-5.1-codex-max`, `gemini-3.1-pro-high`, `claude-opus-5-5`) were preserved intact**.
4. Tested the adder (`picker-model-setting-model`): selecting `opus-5.5-xhigh`, `claude-sonnet-4-5`, or `auto` disabled the effort picker and showed the exact framework reason (`Effort is part of the model name: xhigh.`, `Claude takes no reasoning effort.`, `The provider chooses the effort.`).
5. Checked `390×844` (phone, `<600px`): fallback row placed the Effort control on its own line (`effortOnOwnLine: true`), `scrollWidth == clientWidth` (0 overflow), and 0 WCAG 2.1 AA axe violations.

### Item 3 — Quota Resume scope — PASS after Bug 2 fix (with one UX parity gap filed) (SQLite & PostgreSQL)

**Steps & observations (identical on both SQLite and PostgreSQL):**
1. Triggered real quota pauses on both `conn-primary` (`gemini-3.1-pro`) and `conn-fallback` (`gemini-3.8-flash`), plus seeded a connectionless operator framework pause (`team|default|codex`) in `usage_limit_pause` and restarted the daemon so all three loaded cleanly (`GET /api/pause` returned 3 active pauses).
2. Opened `/next/fleet` → **Pauses & Controls** (`fleet-tab-controls`): all 3 pauses rendered as individual cards. Clicking **Lift pause** on `Primary Gemini` sent only `DELETE /api/pause?connection=conn-primary&model=gemini-3.1-pro`. `GET /api/pause` confirmed `conn-primary` cleared while `conn-fallback` (`gemini-3.8-flash`) and `codex` remained paused.
3. Opened `/next/settings/models` with `conn-fallback` (`gemini-3.8-flash`) and `codex` still paused:
   - **Before Bug 2 fix:** `models-quota-banner` showed a red `.s-banner.error` claiming `Model quotas exceeded (no fallback models left) — development paused`, even though `gemini-3.1-pro` (primary default) was unpaused and dispatching.
   - **After Bug 2 fix:** `models-quota-banner` rendered as amber `.s-banner.warn`: `Model quota exceeded on Fallback Flash · gemini-3.8-flash (fallback active) · resets ... Resume` (ignoring the connectionless `codex` pause).
   - Clicking **Resume** sent only `DELETE /api/pause?connection=conn-fallback&model=gemini-3.8-flash`, clearing `conn-fallback` and dismissing the banner while `GET /api/pause` confirmed the connectionless `codex` framework pause remained untouched.
4. Also tested `/next/settings/models` with **both** `conn-primary` and `conn-fallback` paused: `models-quota-banner` displayed one combined `Resume` button which opened `<g-confirm-dialog>` (`Resume 2 paused model quotas? Clears the usage-limit pauses on Fallback Flash · gemini-3.8-flash, Primary Gemini · gemini-3.1-pro. Other connection, team, or framework pauses stay in place.`), sending zero `DELETE /api/pause` requests before confirmation and two scoped `DELETE /api/pause?connection=...&model=...` requests on confirmation.
   - **Parity gap noted & filed:** unlike classic `/settings/models` and `/next/fleet?tab=controls`, `/next/settings/models` shows no `Paused` badge or per-connection `Resume` button in the `Model connections` table, so an operator viewing `/next/settings/models` with two paused connections cannot resume only one of them without switching to `/next/fleet?tab=controls`.

### Item 4 — Unsaved guard vs real saves — PASS after Bug 1 fix (`grain demo`)

Walked all 12 stateful settings sections (`models`, `sandboxes`, `git`, `ci`, `tasks`, `agents`, `capabilities`, `memory`, `access`, `general`, `caches`, `engine`, `registry` — 13 total including `capabilities`) across `POST /api/settings`, `PUT /api/deployment`, and `PUT /api/memory`, plus leaving `/next/settings` for `/next/inbox` and triggering a real `page.reload()` `beforeunload` event:

| Section | Write route blocked | Nav guard & Cancel | `beforeunload` guarded | Draft kept after 503 | Header status after 503 (before → after Bug 1 fix) | Clean save clears guard & `beforeunload` |
| --- | --- | --- | --- | --- | --- | --- |
| `models` | `PUT /api/deployment` (`modelSettings`) | PASS | PASS | PASS (`high` kept) | `Couldn't save Deployment identity` → **`Couldn't save Model reasoning settings`** | PASS |
| `sandboxes` | `POST /api/settings` (`sandboxCpus`) | PASS | PASS | PASS (`6` kept) | `Couldn't save Sandbox vCPUs` | PASS |
| `git` | `PUT /api/deployment` (`agentGitName`) | PASS | PASS | PASS (`Walkthrough Bot` kept) | `Couldn't save Deployment identity` → **`Couldn't save Commit author name`** | PASS |
| `ci` | `POST /api/settings` (`ciJobsPerRun`) | PASS | PASS | PASS | `Couldn't save CI jobs per run` | PASS |
| `tasks` | `POST /api/settings` (`taskBranchTemplate`) | PASS | PASS | PASS | `Couldn't save Branch name template` | PASS |
| `agents` | `POST /api/settings` (`promptExtension`) | PASS | PASS | PASS | `Couldn't save Standing instructions for every run` | PASS |
| `capabilities` | `POST /api/settings` (`githubHost`) | PASS | PASS | PASS | `Couldn't save GitHub host` | PASS |
| `memory` | `PUT /api/memory` | PASS | PASS | PASS (markdown kept) | `Couldn't save Deployment memory` | PASS |
| `access` | `POST /api/settings` (`gcpProject`) | PASS | PASS | PASS (`example-walk-proj` kept) | `Couldn't save GCP project` | PASS |
| `general` | `POST /api/settings` (`environmentName`) | PASS | PASS | PASS (`Walkthrough Staging` kept) | `Couldn't save Environment name` | PASS |
| `caches` | `POST /api/settings` (`packageCacheSizeGb`) | PASS | PASS | PASS | `Couldn't save Proxy cache max size (GiB)` | PASS |
| `engine` | `POST /api/settings` (`maxWorkers`) | PASS | PASS | PASS | `Couldn't save Max worker agents` | PASS |
| `registry` | `POST /api/settings` (`hub`) | PASS | PASS | PASS | `Couldn't save Hub` | PASS |

- Leaving `/next/settings/models` via the rail link to `/next/inbox` while dirty opened `Discard unsaved changes?`; Cancel kept the user on `/next/settings/models`.
- Real `page.reload()` while dirty fired a `beforeunload` dialog; after saving cleanly, neither section navigation nor `beforeunload` prompted.
- **Note on card-level forms:** `Token budget & usage guardrails` (`PUT /api/usage/settings`) on `/next/settings/models` keeps component-local signals with its own `Save usage settings` button and does not participate in `SettingsStateService.isDirty()`.

### Item 5 — Schema 153 migration on real data & 153→152 downgrade note — PASS (SQLite & PostgreSQL)

**Steps & observations (identical on both SQLite and PostgreSQL):**
1. Initialised a real store, stopped the daemon, rewrote `grain_schema.version = 152`, and seeded bare retired names alongside canonical grants in all three columns:
   - `grain_config.default_capabilities`: `self-debug,read-issues,bootstrap-playbooks,https://grain.dev/capabilities/api#tasks:read`
   - `repo_config.default_capabilities` (`acme/widgets`): `self-debug,read-issues,bootstrap-playbooks,https://grain.dev/capabilities/api#tasks:read`
   - `repo_config.ci_capabilities` (`acme/widgets`): `self-debug,read-issues,bootstrap-playbooks,https://grain.dev/capabilities/grain#ci={}`
2. Started real `grain daemon -sandbox-port-base=0` at `HEAD` (`SchemaVersion = 153`):
   - Daemon logged all 9 removals with their replacement canonical grant names and printed `grain: migrated the store in place from schema 152 to 153`.
   - Direct SQL queries confirmed `grain_schema.version = 153`, `grain_config.default_capabilities = https://grain.dev/capabilities/api#tasks:read`, `repo_config.default_capabilities = https://grain.dev/capabilities/api#tasks:read`, and `repo_config.ci_capabilities = https://grain.dev/capabilities/grain#ci={}`.
   - API checks (`GET /api/config`, `GET /api/settings`, `GET /api/repos/acme/widgets/capabilities`, `GET /api/repos/acme/widgets/ci-capabilities`) and Playwright checks on `/next/settings/capabilities`, `/next/settings/agents`, `/next/repos/acme/widgets/agents`, and `/next/repos/acme/widgets/ci` matched the migrated database with zero bare retired capability names (the only substring match on `/next/settings/capabilities` is the built-in `grain/self-debug` scope-set documentation).
3. Verified the **153→152 downgrade note path** end to end on **both SQLite and PostgreSQL** using a real schema-152 binary (`9dad3601a`):
   - Starting `grain-s152` without a downgrade note was refused (`database schema is newer than this build: found 153, this build knows 152`).
   - Running `grain db downgrade -to 152 -data-dir <dir>` while the schema-153 daemon was running was refused (`a grain daemon is attached to this store`). *(Note: inside an agent sandbox where `/home/debian/.git-credentials` points at the sandbox proxy, `GRAIN_SANDBOX_CLI=0` is needed so the host `db downgrade` command runs instead of `sandboxcli`'s `db query`.)*
   - Stopping the daemon and running `grain db downgrade -to 152 -data-dir <dir>` exported the migrated tables cleanly and wrote `schema-upgrade/upgrade.json` (`"from": 153, "to": 152, "operatorDowngrade": true`).
   - Starting `grain-s152` consumed the note, archived the schema-153 store (`store.schema153-20261120-193403` on SQLite; Postgres schema `public_schema153_20261120_193407`), rebuilt a fresh schema-152 store from the export, removed `schema-upgrade/`, and served `GET /api/config` cleanly with `defaultCapabilities: ["https://grain.dev/capabilities/api#tasks:read"]`.

### Item 6 — Classic Settings → Models effort picker hiding — PASS (`grain demo`)

Opened classic `/settings/models` in Playwright against the same multi-framework connection set as Item 2:

| Model | Framework | Stored effort | Classic UI behaviour | Matches `/next/`? |
| --- | --- | --- | --- | --- |
| `gemini-2.5-pro` | `antigravity` | `medium` | Reasoning effort picker shown | Yes |
| `gemini-2.5-flash` | `antigravity` | `low` | Reasoning effort picker shown | Yes |
| `gpt-5.5` | `codex` | none | Reasoning effort picker shown | Yes |
| `gpt-5.1-codex-max` | `codex` | `high` | Reasoning effort picker shown (`-max` not mistaken for named effort) | Yes |
| `gemini-3.1-pro-high` | `antigravity` | none | Picker hidden (`Effort is part of the model name: high.`) | Yes |
| `opus-5.5-xhigh` | `antigravity` | none | Picker hidden (`Effort is part of the model name: xhigh.`) | Yes |
| `auto` | `antigravity` | none | Picker hidden (`auto picks the model, and its effort, itself.`) | Yes |
| `claude-sonnet-4-5` | `claude` | none | Picker hidden (`Claude takes no reasoning effort.`) | Yes |
| `claude-opus-5-5` (unadvertised) | inferred `claude` | `high` | Clearable picker shown with `Not applied: Claude takes no reasoning effort. Choose "Model default" to clear it.` | Yes |

### Required `/next/settings/*` page crawl & companion §6 features — PASS

- Loaded `/next/settings/models`, `/next/settings/compute` (resolves cleanly to Models heading), `/next/settings/capabilities`, `/next/settings/sandboxes`, `/next/settings/hosts` (alias for `hostds`, heading `Hosts`), and `/next/settings/access` at `1440×900` and `390×844`: 0 page errors, 0 horizontal overflow (`scrollWidth == clientWidth`).
- Verified PR #1899 (Sandboxes overcommit draft reset, Hosts token vs key-bound auth actions, GCP server links, API scope-set editing), PR #1820 (capability-policy verdicts in grants pickers), and PR #1843 (unmigrated-install notice) via both live demo inspection and the full Playwright e2e sweep below.

## Bugs found, fixes & follow-ups

| # | Summary | Deterministic reproduction | Disposition | Regression coverage |
| --- | --- | --- | --- | --- |
| 1 | Failed `PUT /api/deployment` saves on Models/Git mislabelled as `"Couldn't save Deployment identity"` and left deployment JSON field prefixes untranslated | Open `/next/settings/models`, change default model reasoning effort, return HTTP 503 on `PUT /api/deployment` (e.g. `{"error":"modelSettings temporarily unavailable"}`), click **Save changes** → header reads `Couldn't save Deployment identity` and banner keeps raw `modelSettings` prefix. Same on `/next/settings/git` for `agentGitName`/`agentGitEmail`. | **Fixed here** (`ui-ng/src/app/settings/settings-sections.ts`, `settings-state.service.ts`): added `defaultModels`, `agentGitName`, `agentGitEmail` to `SETTING_LABELS`, mapped `deploymentKeys` through `settingLabel` in `failedSaveLabels`, and passed `deploymentKeys` to `readableSaveError`. Classic UI unaffected. | `ui-ng/src/app/settings/settings-state.service.spec.ts`, `ui-ng/e2e/next-settings-save-failure.spec.ts` |
| 2 | Fallback-only quota pause falsely escalates banner to red `"Model quotas exceeded (no fallback models left) — development paused"` | Configure `defaultModels = ["gemini-3.1-pro", "gemini-3.8-flash"]`, pause only `gemini-3.8-flash` (or pause both and lift `gemini-3.1-pro`). Because `pkg/usage` emits `work falls back to nothing -- it is the last model on the default list` for `gemini-3.8-flash`, `summarizeDispatchStatus` matched `falls back to nothing` and set `noFallbackLeft = true`, flipping the banner to red error and disabling GU-6 Hide while `gemini-3.1-pro` was still healthy and dispatching. | **Fixed here** (`ui-ng/src/app/shared/dispatch-status.ts`): when `defaultModels` and active pause scopes are present, only escalate to `noFallbackLeft` when every configured default model is paused (or a warning explicitly says all models are paused). Classic UI unaffected. | `ui-ng/src/app/shared/dispatch-status.spec.ts`, `ui-ng/e2e/next-user-system-settings.spec.ts` |
| 3 | Parity gap: `/next/settings/models` has no per-connection pause indicator or per-connection `Resume` action in the `Model connections` table | Pause two connections simultaneously (`conn-primary` and `conn-fallback`) and open `/next/settings/models`. The combined banner's `Resume` lifts both after confirmation; lifting only one requires switching to `/next/fleet?tab=controls` (unlike classic `/settings/models`). | **Filed as follow-up** via `propose_task` (depends on [Task #7689](/tasks/7689)). | — |

## Verification run

- `make fmt-frontend`: passed (Prettier clean across `ui/`, `ui-ng/`, `ui-hub/`, `ui-shared/`).
- `cd ui-ng && npx vitest run src/app/settings src/app/shared/dispatch-status.spec.ts src/app/app.component.spec.ts`: **8 files, 124 tests passed**.
- `cd ui-ng && GRAIN_BIN=/home/debian/bin/grain PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/next-settings-save-failure.spec.ts e2e/next-user-system-settings.spec.ts e2e/next-settings-model-efforts.spec.ts e2e/next-settings-models.spec.js e2e/next-settings-walkthrough.spec.js e2e/next-connections-walk-fixes.spec.ts e2e/next-capability-policy-pickers.spec.js e2e/next-deployment-operations.spec.js e2e/next-full-system.spec.js`: **75 passed** (desktop, phone, dark).
- `GRAIN_REQUIRE_POSTGRES=1 GRAIN_TEST_POSTGRES_URL="postgres://grain:grain@127.0.0.1:5432/postgres?sslmode=disable" go test -v ./pkg/model/postgres -run TestMigration153`: **3 passed** (Postgres, SQLite, runtime expansion).
- `go test ./pkg/daemon -run "TestBuildOn|Downgrade"`, `go test ./pkg/agent/antigravity -run "Effort"`, `go test ./pkg/ui -run TestAngularRouteTableInSync`, `go test ./tests/hygiene/... ./pkg/ports/...`: **all passed**.
- Re-ran Item 3 live against the rebuilt `/home/debian/bin/grain` on both SQLite and PostgreSQL daemons: confirmed the fallback-only banner now renders as `.s-banner.warn` (`Model quota exceeded on Fallback Flash · gemini-3.8-flash (fallback active)`), scoped Resume lifts only `conn-fallback`, and the `codex` framework pause stays intact.
- Before/after contact sheet rendered across desktop (1440×900) and phone (390×844) in light and dark themes (0 overflow, 0 axe violations) and attached to [Task #7689](/tasks/7689).
