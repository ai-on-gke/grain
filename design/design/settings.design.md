# Grain settings model

Grain separates live runtime state, versioned declarative configuration, write-only secrets, operator sign-in, browser-local display preferences, and imperative operational actions so that standing configuration can be reviewed and audited as Git pull requests without locking operators out or exposing secret material.

This document is the 1-page architectural overview of that model. Exhaustive field-by-field and route-by-route inventories live in the companion specifications under `design/spec/settings/`: [`settings.spec.md`](../spec/settings/settings.spec.md) and [`settings-mutability.spec.md`](../spec/settings/settings-mutability.spec.md).

**The core rule:** the relational store (SQLite or PostgreSQL, `pkg/model`) is the live runtime record read on every reconcile tick; the config repository (`grain/config` on the controller or an external Git remote, `pkg/staterepo`) is its diffable declarative projection for standing configuration; and secret values, sign-in credentials, and per-browser UI state never enter Git.

## 1. Principles and state boundaries

Every knob or control on a deployment belongs to one of six surfaces:

| Surface | Backing store | Exported to config repo? | Live behavior |
| --- | --- | --- | --- |
| **Declarative settings** | SQLite / PostgreSQL (`staterepo.SettingsTables` + `memoryTables`) | **Yes** (`tables/*.json`, `settings/*.yml`, `memory/**`, `scripts/**`, `workflows/**`, `release-notes/**`) | Re-read every reconcile tick (`liveConfig` in `pkg/cli/daemon.go`), except the explicit `restartRequired` subset (`pkg/ui/settings.go`, e.g. `sandboxBackend`, `k8s*`), which reports `pendingRestart` until restart |
| **Secret material** | Encrypted sealed box `<data-dir>/secrets` (`pkg/secrets`, X25519 + HKDF-SHA256 + AES-256-GCM) | **Never** (`secrets.enc` is gitignored; config files hold only `$secret:<name>` / `<secret>/<key>` references or credential IDs) | Write-only via UI or `request_secret` (`PUT /api/tasks/{id}/secret`); `secrets:read` lists names only, and `statecheck` rejects raw credentials in config PRs |
| **Sign-in & access** | `local-api-token` (`GRAIN_ACCESS_CODE`, `pkg/ui/accesscode.go`) and Cloud IAP / `IdentityVerifier` (`design/design/authentication.design.md`) | **Never** | Enforced per HTTP/WebSocket request; kept out of Git so a bad config PR cannot lock operators out |
| **Browser preferences** | Browser `localStorage` (`PreferencesSection.jsx`, `NOT_CONFIG` in `SettingsPage.jsx`) | **Never** | Theme, time zone, timeline order, and sidebar pins/visibility apply locally in the current browser |
| **Operational actions** | In-memory / runtime endpoints (`Upgrade`, `Power`, cache flush, pipeline pause, `Sync`, connection checks) | **No** (imperative actions, not standing config) | Execute immediately via UI or scoped API without mutating declarative config files |
| **Startup CLI / env flags** | Process argv / env (`cmd/grain`, e.g. `-data-dir`, `-db`, listen addrs, `-sandbox-port-base`, `-ui-iap-*`) | **Never** | Bootstrap the controller process before the store, secret store, or HTTP listener opens |

### Live store versus config repository (`pkg/model` ↔ `pkg/staterepo`)

Reconcilers and HTTP handlers query SQLite or PostgreSQL directly; nothing above `pkg/staterepo` parses Git trees at request time. `pkg/staterepo` projects `SettingsTables` and `memoryTables` between the database and a Git working tree:

- **Files in `grain/config`:** table dumps in `tables/*.json`, human-readable YAML projections in `settings/*.yml` (`capabilities.yml`, `templates.yml`, `schedules.yml`, `suites.yml`, `config-policy.yml`, `new-task-form.yml`) that take precedence over the columns/tables they project (`pkg/staterepo/decl.go`, `capabilities_file.go`), and externalized text files (`memory/**`, `scripts/repos/<owner>/<name>/setup.sh`, `workflows/<owner>/<name>/...`, `release-notes/**`).
- **Live `Apply` versus cold `Load`:** Every database table is classified as either settings or runtime (`TestEveryTableIsEitherSettingsOrDeliberatelyNot`). On a running daemon, `staterepo.Apply` imports **only** `SettingsTables` (and `memoryTables`) when `HEAD` advances past the host's loaded-commit marker, replacing deleted rows cleanly without touching live runtime tables (`task`, `task_run`, `executor_token`, `sandbox_access`, notifications, audit logs). Whole-dump `staterepo.Load` runs only when restoring into an uninitialized database.

## 2. The four deployment configuration modes

Write routing is enforced centrally in `pkg/ui/config_refusal.go` (`configRepoFor`, `configRepoOnly`, `checkSettingsWritable`, `checkBootstrapWritable`, `checkGrainConfigWritable`), paired with `<data-dir>/state-repo.json` (`pkg/staterepo/settings.go`) and surfaced via `GET /api/config` (`configRepoOnly`, `settingsLocked`, `externalConfigRepo`):

| Mode | Condition | `configRepoOnly` | UI & REST API behavior | Config repo sync |
| --- | --- | --- | --- | --- |
| **1. Standalone / Demo** | No state repo (`StateRepo == nil`, e.g. `grain demo`) | `false` | All settings editable directly in UI and REST API; written straight to the store | None |
| **2. Internal (`grain/config`) — Unlocked** *(default)* | Controller-local `grain/config` (`remote == ""`), `grain_config.settings_locked: false` | `false` | UI and REST API edits enabled across settings; writes commit to the store and immediately export to `grain/config` | `StateManager.SyncAfterWrite` exports UI/API writes and advances the loaded-commit marker so reconcile ticks do not clobber them; merged `grain/config` PRs import via `staterepo.Apply` |
| **3. Internal (`grain/config`) — Locked** | Controller-local `grain/config` (`remote == ""`), `grain_config.settings_locked: true` (and `directConfigWrites` unset) | `true` | Non-bootstrap UI sections (`CONFIG_REPO_SECTION_PATHS`, `RepoPage`) render read-only with `/config?path=...` links; REST API refuses non-bootstrap writes with `409 config_managed_externally` (`"internal": true`) | Non-bootstrap changes land exclusively via reviewed `grain/config` PRs; bootstrap writes still export via `SyncAfterWrite` |
| **4. External Config Repo** | `state-repo.json` `remote` set to an external Git URL | `true` | Read-only mirror regardless of `settings_locked`; REST API refuses **both** non-bootstrap and bootstrap `grain_config` writes with `409 config_managed_externally` | Declarative changes land via PRs on the external Git repository and import on sync/pull |

- **Actionable `409 Conflict` refusals (`configWriteRehearsal`):** For routes without pre-store side effects, `pkg/ui/config_refusal.go` runs the write in a rolled-back transaction, diffs before/after `staterepo` exports, and returns `code: "config_managed_externally"` with target `path` entries, unified-diff `change` patches (for `POST /api/state-repo/pull-requests` or `git apply`), and—for agent callers—an `access` block reporting whether the task holds `writes:` on the config repo. Routes that touch secrets first use `Unrehearsed` guards and return `path` plus `description` without running.
- **Unlocking `settings_locked`:** `SettingsLocked` is included in `bootstrapOnlyChange` (`pkg/ui/config_bootstrap.go`), so a human operator can toggle `settings_locked: true → false` from `Settings → General` or `PUT /api/settings` while locked. `checkGrainConfigWritable` refuses `PrincipalAgent` callers attempting to unlock `settings_locked`.
- **`directConfigWrites` escape hatch:** Setting `"directConfigWrites": true` in `<data-dir>/state-repo.json` (re-read per request without restart) forces `configRepoOnly: false` in **internal** mode for migration and e2e harnesses (`pkg/staterepo/settings.go`). External mode (`remote != ""`) ignores `directConfigWrites` (`configRepoFor` checks `externalConfigRepo` first) so an external mirror never diverges silently from its upstream Git repository.

## 3. Setting categories and authority boundaries

| Category | Mode 2: Internal Unlocked | Mode 3: Internal Locked | Mode 4: External Repo | Stored in `grain/config`? |
| --- | --- | --- | --- | --- |
| **Bootstrap (`Models`, `Sandboxes`)** | UI/API + config PR (`SyncAfterWrite`) | **UI/API** (lock-exempt; `SyncAfterWrite`) + config PR | External config PR (`409` on API) | **Yes** (`tables/grain_config.json`, without secrets) |
| **Bootstrap host-local (`Executors`, model credentials)** | UI/API | **UI/API** (lock-exempt) | **UI/API** (host-local) | **Never** (`executor_token`, `<data-dir>/secrets`) |
| **Config-managed (`General`, `Git`, `Repositories`, `Tasks`, `Agents`, `Capabilities`, `CI`, `Memory`, `Caches`, `Engine`, `Boards`, `Processes`)** | UI/API + config PR (`SyncAfterWrite`) | `grain/config` PR only (`409` on API) | External config PR only (`409` on API) | **Yes** (`settings/*.yml`, `tables/*.json`, `memory/**`, `scripts/**`, `workflows/**`) |
| **Write-only secrets (`Legacy capabilities`, git tokens/keys, capability secrets)** | UI / `request_secret` | **UI / `request_secret`** | **UI / `request_secret`** | **Never** (`<data-dir>/secrets`; `$secret:<name>` refs only) |
| **Access & Browser preferences (`Access`, `Preferences`)** | Local UI/API or `localStorage` | **Local** UI/API or `localStorage` | **Local** UI/API or `localStorage` | **Never** |
| **Repository-declared (`.grain/` in target repo)** | Target-repo PR overrides | Target-repo PR overrides | Target-repo PR overrides | Derived cache only; target repo's default branch wins |

### Bootstrap settings (`Models`, `Sandboxes`, `Executors` — `SETTINGS_GROUPS` `"bootstrap"`)

A deployment must be able to bring up model connections/defaults (`agent_backends`, `default_model`, `model_settings`, `judgement_model`), sandbox infrastructure/capacity/images/network/CLI run locations (`sandbox_*`, `k8s_*`, `cli_run`, `network_access`, `max_workers`), and remote executors (`Executors`) before it can run tasks or fix `grain/config`. Non-secret `Models` and `Sandboxes` fields (`clearBootstrap` in `pkg/ui/config_bootstrap.go`, guarded by `checkBootstrapWritable`) stay UI/API-writable when `settings_locked: true` and export to `tables/grain_config.json` (moving to external-repo PRs only in Mode 4). Model credentials (`PUT /api/agent-backends/{id}/credential` → `<data-dir>/secrets`) and dynamic `Executors` join tokens (`executor_token`, excluded from `SettingsTables`) never export to Git and stay UI-mutable in all modes, even when an external config repo is unreachable.

### Config-managed deployment, repo, and composed settings

Covers the `"config-managed"` nav group (`General`, `Git` credential metadata/bindings, `Repositories`, `Tasks`, `Agents`, `Capabilities`, `CI`, `Memory` in `CONFIG_REPO_SECTION_PATHS` of `ui/src/settingsSections.js`), non-bootstrap `Caches` and `Engine` fields on `grain_config` (grouped under `System / Operations` in the nav, still guarded by `checkGrainConfigWritable`), per-repo settings (`RepoPage`), **Boards** (`tables/board*.json`), and **Processes** (`settings/templates.yml`, `settings/schedules.yml`, `settings/suites.yml`, `qualification_*`, composed via `pkg/model/compose.go`). Editable in both UI/API and `grain/config` in Mode 2; config-repo-only in Modes 3 and 4.

### Secrets, access, and browser preferences

Secret values (`Legacy capabilities` / `CredentialsSection`, model API keys, git tokens/SSH keys, capability server secrets, `gcp_key` material) enter via `allowInExternalMode` UI cards or `request_secret`, live encrypted in `<data-dir>/secrets`, and are referenced in `grain/config` solely as `$secret:<name>`, `<secret>/<key>`, or credential IDs. `Access` (`GRAIN_ACCESS_CODE`, IAP) and `Preferences` (`localStorage`) never touch `grain/config`.

### Repository-declared authority (`.grain/` — `pkg/repodecl`)

When declarations are enabled on a repository, files on its default branch (`.grain/settings.yml`, `.grain/workflows/*.yml`, `.grain/templates/*.yml`, `.grain/suites/*.yml`, `.grain/schedules/*.yml`, `.grain/triggers/*.yml`, `.grain/merge-rules.yml`, `.grain/lifecycle.yml`, `.grain/review.yml`, and `**/*.grain.md`) override both the UI and `grain/config` for the repo-scoped fields they claim, edited via PRs to that repository. Declarations cannot self-grant privilege: `capabilities`, cross-repo `reads`/`writes`, and `target` overrides are rejected at parse time (`pkg/repodecl`, `pkg/declschema`).

### Trust boundary and agent gates (`docs/agent-api.md`, `pkg/trustboundary`)

- **HTTP / MCP scopes:** Every route maps to an `api:<area>:<verb>` scope (`pkg/ui/route_scopes.go`, `pkg/model/agent_api.go`). Mutating settings, connections, capability policy, or secrets requires **Dangerous** scopes (`settings:write`, `connections:write`, `capabilities:admin`, `secrets:write`)—person-grantable only, blocked from agent self-escalation (`agentFilingEscalation`), refused on the in-sandbox CLI channel (`X-Grain-Channel: sandbox-cli`), audited with a mandatory reason, and barred from unlocking `settings_locked`.
- **Git PR holds:** `trustboundary.ClassifyState` classifies **every** path in `grain/config` under `StatePrivilege` (`RequiresHuman: true`) by default, holding every config repo PR for a human **Submit**. Optional `auto_merge_paths` in `settings/config-policy.yml` can exempt benign paths (such as `memory/**` or `tables/board*.json`), bounded by `NeverAutoMergePaths` so `settings/*.yml`, `tables/grain_config.json`, `tables/repo_config.json`, `tables/api_scope_set.json`, `scripts/**`, and `workflows/**` never auto-merge without a human. Target-repo `.grain/` paths (`grain-config`, `merge-rules`, `path-triggers`, `ci-suppressions`) likewise trigger `trustboundary.Classify` security and human/owner holds.

## 4. Companion specifications and related designs

- **[`design/spec/settings/settings.spec.md`](../spec/settings/settings.spec.md):** Exhaustive normative specification of every setting in Grain—storage table/column, config-repo file projection, REST route and `api:` scope, live-reload versus `restartRequired` semantics, and UI surface.
- **[`design/spec/settings/settings-mutability.spec.md`](../spec/settings/settings-mutability.spec.md):** Normative per-setting mutability matrix across UI, REST API, `grain/config`, external config repo, target-repo `.grain/`, `settings_locked`, and caller principal (human operator versus agent).
- **Component and historical designs in `docs/design/`:**
  - [`docs/design/config-repo-settings.md`](../../docs/design/config-repo-settings.md) established the bootstrap versus config-repo boundary, `settings/capabilities.yml`, transactional `configWriteRehearsal` (`config_managed_externally`), and default `StatePrivilege` human holds; its unconditional internal-mode lock was subsequently refined on `main` into the opt-in `grain_config.settings_locked` toggle (`false` by default with immediate `SyncAfterWrite`).
  - [`docs/design/settings-panel.md`](../../docs/design/settings-panel.md) is the frontend component design defining the `/settings/<section>` and `/system/<tab>` views, `SETTINGS_GROUPS` navigation (`ui/src/settingsSections.js`), and legacy `/admin/*` redirects.
  - [`docs/external-config-repo.md`](../../docs/external-config-repo.md) and [`docs/agent-api.md`](../../docs/agent-api.md) document the wire contract for `409 config_managed_externally` patches and `api:` scope grants.
