# Test run — Deployment, Tasks, Agents, Capabilities, CI, Memory, Boards and Processes settings across UI, grain/config and `settings_locked` (2026-11-01)

This run checks every setting in Settings → General, Tasks, Agents, Capabilities, CI and Memory, plus the composed entities on `/boards` and `/processes`, against `design/design/settings.design.md`, `design/spec/settings/settings.spec.md` and `design/spec/settings/settings-mutability.spec.md`, on three axes:

1. UI/API → store → grain/config, with the lock off.
2. grain/config → `grain state check` → store → UI.
3. `settings_locked: true`, plus the `directConfigWrites` opt-out.

Task 2156, from task 1933.

## Environment

- **Tree:** `main` at `1d324a89`, plus this branch's four fixes below.
- **Build:** `cd ui && npm ci && npm run build`, then `go build ./cmd/grain`, so the UI is embedded.
- **Daemon:** a real `grain daemon`, not `grain demo`:
  - set up with `grain init workstation -non-interactive -sandbox-backend host -model-backend claude -ui-addr 127.0.0.1:4300 -start-service=false` under a temp `HOME`;
  - started from its `daemon.sh` with `-sandbox-port-base=0`;
  - `GET /api/state-repo` reports `mode: internal`, `repository: grain/config`, schema 77.
- **Callers:**
  - HTTP: the daemon's `local-api-token` as a bearer token, as a person.
  - Browser: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, 1440×900, with the same bearer token sent as an extra header.
- **Sync:** `POST /api/state-repo/sync` after every write, then `git -C <data-dir>/state-repo diff` between the HEAD before and the HEAD after.

## Axis 1 — UI/API → store → grain/config (`settings_locked: false`)

### Settings fields

Each row is a `PUT /api/settings` of one field. In every row:

- the answer was 200 and echoed the new value;
- `GET /api/settings` reread it;
- the sync made a commit whose diff touched the named file.

| Section | Field | File |
| --- | --- | --- |
| General | `environmentName`, `showClosedByDefault`, `upgradeCheckMode`, `upgradeCheckPausesPipeline` | `tables/grain_config.json` |
| Tasks | `approvedByDefault`, `autoMergeByDefault` (both default `true`: a write of `true` is a no-op with no commit; `false` committed `approved_by_default: 0` / `auto_merge_by_default: 0`), `maxAutoSubmitDepth`, `maxAutoSubmitFanOut` | `tables/grain_config.json` |
| Agents | `maxAgentTurns`, `promptExtension` (standing instructions), `taskBranchTemplate` | `tables/grain_config.json` |
| CI | `ciJobsPerRun` (queue concurrency), `ciRunHistory` (history retention), `ciCacheMb` (job cache), `ciArtifactMb` (artifacts) | `tables/grain_config.json` |

`defaultCapabilities` is validated against the capability servers that are running. Naming `model-proxy` (not configured) or a stub server whose command is not on `PATH` answered 400 with the reason. That is correct, but it left no runnable capability for a positive case in this sandbox.

Through the browser:

1. Settings → General → Environment name, Agents → Maximum agent turns and CI → run history were edited and tabbed out of.
2. Each showed its saved indicator.
3. After a reload, the fields read `ui-walk-env`, `222` and `88`.
4. After a sync, `tables/grain_config.json` held `"environment_name": "ui-walk-env"`, `"max_agent_turns": 222` and `"ci_run_history": 88`.
5. Later syncs reverted none of them.

### Entities

| Route | Answer | Files the sync changed |
| --- | --- | --- |
| `PUT /api/capability-policy` | 200 | `tables/grain_config.json` |
| `PUT /api/api-scope-sets` | 200 | `tables/api_scope_set.json` |
| `PUT /api/capability-servers` | 200 | `tables/grain_config.json` |
| `POST /api/capability-servers/bind-secret` (with a value) | 200 | `tables/grain_config.json` (`"WALK_KEY": "$secret:walk-key"`) |
| `PUT /api/memory` | 200 | `memory/deployment.md`, `tables/memory.json` |
| `PUT /api/memory/documents` | 200 | `memory/documents/deployment/walk-doc.md`, `tables/memory_document.json` |
| `POST /api/boards` | 201 | `tables/board.json`, `tables/board_sequence.json` |
| `POST /api/templates` | 201 | `settings/templates.yml`, `tables/template_sequence.json` |
| `POST /api/schedules` | 201 | `settings/schedules.yml`, `tables/schedule_sequence.json` |
| `POST /api/console-sessions` | 201 | `tables/grain_config.json` |

**Secret values stay out of grain/config.** The bound value (`TOPSECRET-walk-value`) appears nowhere in the working tree (`grep -r`) or in its history (`git log -p -S`). Only the `$secret:walk-key` reference is exported.

**`settings/capabilities.yml` is not written on this deployment.** It is exported only once the stored capability policy carries a catalogue (`pkg/staterepo/capabilities_file.go`). Until then, capability servers, policy and default grants are `grain_config` columns, and scope sets are `api_scope_set` rows. See fix 3.

## Axis 2 — grain/config → `grain state check` → store → UI

These edits were committed straight to the controller-local grain/config (`<data-dir>/state-repo`, `main`):

- `tables/custom_field.json`: a `customer` text field shown on rows;
- `tables/grain_config.json`: `environment_name` set to `from-config-repo`, `max_agent_turns` set to `654`;
- an appended line in `memory/deployment.md`.

Results:

- **`grain state check <copy>` passed** (`ok: grain can load this`).
- **After `POST /api/state-repo/sync`:**
  - `GET /api/settings` read `from-config-repo` and `654`;
  - `GET /api/config` listed the `customer` custom field;
  - `GET /api/memory` carried the new line.
- **The next export commit did not revert them.** It changed only `README.md`.
- **Uncommitted edits in the daemon's own working copy are overwritten** by its next export. That is expected, but anyone editing by hand should know it: commit, or open a config pull request.

`grain state check` given invalid values:

| Edit | Result |
| --- | --- |
| `max_agent_turns: "lots"` | exit 1, a Scan error naming the column |
| `custom_field` `type: "colour"` | exit 1, "declares 1 field(s) grain cannot offer" |
| `upgrade_check_mode: "sometimes"` | **exit 0, nothing said.** See fix 4. |

## Axis 3 — `settings_locked: true`

`PUT /api/settings {"settingsLocked":true}` answered 200 with `settingsLocked: true`, `configRepoOnly: true`. `GET /api/config` agreed.

### REST

Each write below changed a value. Every one answered **409** with:

- `code: "config_managed_externally"`;
- `internal: true`;
- the files it would change;
- one rehearsed patch per file.

Settings fields (`PUT /api/settings`), all with `path: ["tables/grain_config.json"]`:

- General: `environmentName`, `showClosedByDefault`, `upgradeCheckMode`, `upgradeCheckPausesPipeline`;
- Tasks: `approvedByDefault`, `autoMergeByDefault`, `maxAutoSubmitDepth`, `maxAutoSubmitFanOut`;
- Agents: `maxAgentTurns`, `promptExtension`, `taskBranchTemplate`;
- CI: `ciJobsPerRun`, `ciRunHistory`, `ciCacheMb`, `ciArtifactMb`.

Entity routes:

| Route | `path` |
| --- | --- |
| capability policy, capability servers, bind-secret, console sessions | `tables/grain_config.json` |
| API scope sets | `tables/api_scope_set.json` |
| boards | `tables/board_sequence.json`, `tables/board.json` |
| templates | `tables/template_sequence.json`, `settings/templates.yml` |
| schedules | `tables/schedule_sequence.json`, `settings/schedules.yml` |

A write that changes nothing (`PUT` of the value already stored) answers 200. That is correct: there is nothing to refuse.

**Still open, D-3:** `PUT /api/memory` and `PUT /api/memory/documents` answered **200** while locked, and the next sync exported the note. This is unchanged and recorded in the spec, which leaves open which side to change. The Memory section is drawn read-only, so it is reachable only from the API.

**Bind-secret while locked (fixed here, D-7).** The refused binding is 409 with the `$secret:walk-key2` reference in its patch. Before this fix it stored `LOCKEDSECRET` in the secret store anyway. After it, `GET /api/capability-servers` lists only `walk-key` as present.

### UI

Measured in Chromium at 1440×900, on each of `/settings/{general,tasks,agents,capabilities,ci,memory}`:

- **The section is read-only.** The only enabled visible inputs are "Filter settings" and, on General, the `settingsLocked` toggle itself (bootstrap, a person may unlock).
- **Each section has its config-managed notice:**

  | Section | Links |
  | --- | --- |
  | General, Agents | `tables/grain_config.json` |
  | Tasks | `tables/grain_config.json`, `tables/custom_field.json`, `settings/capabilities.yml` |
  | Capabilities | `tables/grain_config.json`, `tables/api_scope_set.json`, `settings/capabilities.yml` |
  | CI | `tables/grain_config.json`, `tables/repo_config.json` |
  | Memory | `memory/deployment.md` |

- **Following General's first link** opens `/settings/config-repo/tables/grain_config.json`, which shows the file.
- **`/boards` ("New board") and `/processes` ("+ New") stay enabled** while locked, as spec D-4 records. Their writes reach the 409 refusal dialog, which offers Create Config PR (`e2e/config-refusal-pull-request.spec.js`, rerun green).

### `directConfigWrites` (D-10)

With `{"directConfigWrites": true}` in `<data-dir>/state-repo.json` and `settings_locked` still on:

- `GET /api/settings` answers `settingsLocked: true`, `configRepoOnly: false`;
- General, Agents and CI render editable, with no notice. General: `environmentName`, `showClosedByDefault`, `upgradeCheckMode`, …; CI: all fourteen `ci*` fields.

Before fix 2, the page keyed on `settingsLocked` and drew them all read-only, though the server would accept every write.

### External mode

Not driven against a live external remote in this run. The refusal paths are covered by `TestExternalModeRefusesEachConfigWriteWithTheEditToCommit` and `TestExternalConfigRepoRefusesSettingsMutationsAndExposesMode`, which pass on this branch (`go test ./pkg/ui`).

## Fixes on this branch

1. **D-7: a locked secret binding stored its value** (`pkg/ui/capability_servers.go`).
   - **Before:** `BindCapabilityServerSecret` passed its bootstrap guard and wrote the value to the secret store. Only then was its `mcp_servers` write refused, leaving a secret nothing referenced.
   - **After:** the value is stored only when the binding itself will be written.
   - **Test:** `TestBindingASecretWhileLockedStoresNothing`. It fails on `main`: `a refused binding stored its value "super-secret-key-material"`.
2. **D-10: SettingsPage ignored `directConfigWrites`.**
   - `GET`/`PUT /api/settings` now carry `configRepoOnly` for the row they answer with (`Client.rowConfigRepoOnly`).
   - `SettingsPage` keys its lock on that, falling back to `GET /api/config`'s, instead of on `settingsLocked`.
   - **Tests:** `TestSettingsSayWhenTheyAreConfigRepoOnly`, and the SettingsPage unit test "keys the lock on configRepoOnly, not settingsLocked", which fails on `main`.
3. **The Capabilities notice linked a file that did not exist.**
   - **Before:** Capabilities linked only `settings/capabilities.yml`, and Tasks linked it first. That file is absent until a deployment adopts the catalogue.
   - **After:** both sections list the files that always exist first, `tables/grain_config.json` (and, for Capabilities, `tables/api_scope_set.json`). `settings/capabilities.yml` stays last, for deployments that use it.
   - **Tests:** the SettingsPage unit test and `e2e/config-repo-only-settings.spec.js` assert the new order. The e2e stub now also stamps `configRepoOnly` on `GET /api/settings`.
4. **`grain state check` was silent about an unknown `upgrade_check_mode`.**
   - **Before:** a typo loads as `auto` (`NormalizeUpgradeCheckMode`), so it would turn unattended upgrades on, while `PUT /api/settings` refuses the same value.
   - **After:** the check's closed-vocabulary warnings (`pkg/staterepo/vocabulary.go`) cover `grain_config.upgrade_check_mode` and `network_access`. The warning names the word, the vocabulary and the fallback. It is a warning, not a failure, for the forward-compatibility reason the file gives.
   - **Test:** `TestCheckWarnsAboutAGrainConfigModeThisBuildDoesNotHave`.

## Checks run

- `go test ./pkg/ui ./pkg/staterepo -count=1`: ok.
- `npx vitest run`: 228 files, 3578 tests passed.
- `npx playwright test e2e/config-repo-only-settings.spec.js e2e/config-refusal-pull-request.spec.js e2e/session-pending-grants.spec.js`: 9 passed.

## Left open

- **D-3:** memory is written in every mode while the UI draws it read-only.
- **D-4:** Boards and Processes are not lock-aware in the UI.
- **D-1, D-2, D-5, D-6, D-8, D-9, D-11 (residual), D-12:** as the spec records them. This run touched none of them.
