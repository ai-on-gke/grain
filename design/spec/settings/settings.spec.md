# Grain settings: field-by-field specification

**Status:** normative inventory. It is derived from the code at the commit it lands in, not from older prose.

This document lists every setting a Grain deployment has. That covers the deployment row (`model.Config` / `grain_config`), the per-repository row (`model.RepoConfig` / `repo_config`), repository-declared settings (`.grain/settings.yml` and the other `.grain/*` files), the composed configuration entities exported to the config repository, the `<data-dir>/state-repo.json` file, the daemon's startup flags and environment variables, and the preferences each browser keeps for itself.

- The one-page architecture this inventory sits under is [`design/design/settings.design.md`](../../design/settings.design.md). Read it first for the six state surfaces, the four configuration modes, and why secrets and sign-in never enter Git.
- The companion matrix, [`settings-mutability.spec.md`](settings-mutability.spec.md), says per mode who may write each surface (UI, REST, agent, config PR): the summary is its §2.10, and §3 gives the per-setting detail. This document does not repeat that matrix. It names each field's surface (bootstrap or config-managed), and the matrix decides what that means in each mode.
- If this document and the code disagree, the code wins, and this document is the bug. §13 lists the sources it was enumerated from, so a reviewer can re-run the audit.

## Contents

1. Conventions
2. Where a setting lives: identifiers and surfaces
3. Deployment settings by Settings section
   - 3.1 Models & sandboxes → Models
   - 3.2 Models & sandboxes → Sandboxes
   - 3.3 Models & sandboxes → Executors
   - 3.4 Secrets & access → Legacy capabilities
   - 3.5 Secrets & access → Access
   - 3.6 System / Operations → Upgrade
   - 3.7 System / Operations → Caches
   - 3.8 System / Operations → Engine
   - 3.9 System / Operations → Config repo
   - 3.10 Managed in config repo → General
   - 3.11 Managed in config repo → Git
   - 3.12 Managed in config repo → Repositories
   - 3.13 Managed in config repo → Tasks
   - 3.14 Managed in config repo → Agents
   - 3.15 Managed in config repo → Capabilities
   - 3.16 Managed in config repo → CI
   - 3.17 Managed in config repo → Memory
4. Deployment settings with no Settings-page control
5. Retired and ignored `grain_config` columns
6. Per-repository settings (`repo_config`, `/repos/<owner>/<name>/settings/<tab>`)
7. Repository-declared settings (`.grain/*`)
8. Composed and auxiliary configuration entities
9. `<data-dir>/state-repo.json`
10. Daemon startup flags and environment variables
11. Browser-local preferences (This browser → Preferences)
12. Runtime application summary
13. Sources and coverage checklist

## 1. Conventions

1.1. **Route notation.** A Settings page location is written `/settings/<section>#<card>`. `<section>` is a section id from `ui/src/settingsSections.js` (`SETTINGS_GROUPS`), and `<card>` is the `anchor` of the `SettingsCard` that holds the control. A repository location is written `/repos/<owner>/<name>/settings/<tab>`, with `<tab>` one of `REPO_TABS` in `ui/src/components/RepoPage.jsx`: `overview`, `ci`, `merge-queue`, `agents`, `branches`, `releases`, `advanced`.

1.2. **API keys.** Unless a row names another route, a deployment setting is read from `GET /api/settings` and written with `PUT /api/settings` (also `POST /api/settings` and the legacy alias `PUT /api/config`). The body is `ui.UpdateSettingsRequest`. Every field in it is a pointer, so **only the fields present are changed**. `POST /api/settings/plan` runs the same validation and reports what would change without saving it (`ui.PlanSettings`). The JSON key is the camelCase name shown in backticks.

1.3. **Column names.** A row's `grain_config` column is also its key in `tables/grain_config.json` in the config repository (`staterepo` dumps tables column for column). Column names are listed as `store.go`'s `configColumns` writes them.

1.4. **Durations** are Go `time.ParseDuration` strings (`90s`, `5m`, `1h30m`). The API returns them via `durationSetting`, which renders `0` as the empty string. A blank string parses as `0` (`parseDurationSetting`). For every duration setting below, `0` means "grain's own default", unless the row says otherwise. Durations are stored in whole milliseconds (`*_ms` columns). Where a setting has a negative "off" reading, a non-zero value under 1 ms is refused, so that it is not silently stored as `0`, which would mean the default.

1.5. **Surface.** Each field is one of two kinds:
- **Bootstrap.** These are Models and Sandboxes fields that stay UI-writable in locked internal mode, so an operator can never lock themselves out of running work. The authoritative list is `clearBootstrap` in `pkg/ui/config_bootstrap.go`: `AgentBackends`, `DefaultModel`, `AgentBackendID`, `ModelSettings`, `JudgementModel`, `MaxWorkers`, `SandboxBackend`, every `Sandbox*` shape/budget/floor/overcommit field, `NetworkAccess`, `SandboxImage`, `SandboxImageMirror`, `CLIRun`, `SettingsLocked`, and every `K8s*` field.
- **Config-managed.** Everything else. In modes 3 and 4 these are read-only in the UI and refused with `409 config_managed_externally`.

The mutability matrix has the full per-mode rules.

1.6. **Runtime column.** The *Applies* column uses these terms:
- **live (tick):** re-read on the next reconcile cycle, or per request or dispatch, with no restart.
- **next sandbox:** reaches sandboxes and jobs created afterwards. A running sandbox keeps what it was built with.
- **next task:** seeds or freezes a value onto tasks created afterwards. Existing tasks keep theirs.
- **restart:** the value is in `ui.restartOnlySettings`. `GET /api/settings` lists it in `restartRequired` always, and in `pendingRestart` while the stored value differs from the one the daemon started with.

The live/restart split is enforced by `pkg/ui/settings_restart_drift_test.go`: every `UpdateSettingsRequest` key must be in either `settingsAppliedLive` or `restartOnlySettings`.

## 2. Where a setting lives: identifiers and surfaces

| Surface | Store | Config repo path | Who writes it |
| --- | --- | --- | --- |
| Deployment row | `grain_config` (one row, `id = 1`), `model.Config` | `tables/grain_config.json`; parts are projected into `settings/capabilities.yml` | `PUT /api/settings`, `PUT /api/deployment`, `PUT /api/settings/cli-run`, `/api/deployment/*`, `/api/capability-*`, `/api/console-sessions*`; config PR |
| Repository row | `repo_config`, `model.RepoConfig` | `tables/repo_config.json`; `scripts/repos/<owner>/<name>/setup.sh` | `PUT /api/repos/{owner}/{name}/settings` and the single-field `PUT /api/repos/{owner}/{name}/<field>` routes; `PUT /api/deployment/repos/{owner}/{name}/config`; config PR |
| Repository declarations | the repository's own tree, `.grain/**` | none (they live in the work repository) | a commit to the repository; read only when `repo_config.declarations` is on |
| Composed entities | `template*`, `suite*`, `schedule*`, `board*`, `custom_field`, `api_scope_set`, `standing_sandbox*`, `persistent_sandbox`, `gcp_key`, `repo_workflow`, `qualification_*`, `memory*` (`staterepo.SettingsTables` + `memoryTables`) | `tables/<table>.json`, `settings/{templates,suites,schedules,capabilities,config-policy,new-task-form}.yml`, `memory/**`, `workflows/<owner>/<name>/**` | their own REST routes (§8); config PR |
| State-repo binding | `<data-dir>/state-repo.json` | never | `POST /api/state-repo/remote`, `/secrets-repo`; hand edit |
| Secrets | `<data-dir>/secrets` (sealed box, `pkg/secrets`) | never (`secrets.enc` is gitignored; config holds `$secret:<name>` references) | `PUT /api/secrets/{secret}/{key}`, the credential routes, `request_secret` |
| Startup | argv / environment | never | the unit file, Deployment manifest or `grain init` |
| Browser | `localStorage` (`grain.*` keys) | never | the browser itself |

Every table is classified as settings or runtime (`TestEveryTableIsEitherSettingsOrDeliberatelyNot`). `staterepo.SettingsTables` is exactly: `grain_config`, `repo_config`, `repo_workflow`, `qualification_config`, `qualification_item`, `qualification_item_depends_on`, `custom_field`, `api_scope_set`, `template`, `template_read`, `template_write`, `template_grant`, `template_field`, `template_custom_field`, `template_sequence`, `suite`, `suite_item`, `suite_sequence`, `board`, `board_column`, `board_panel`, `board_section`, `board_sequence`, `schedule`, `schedule_read`, `schedule_write`, `schedule_grant`, `schedule_custom_field`, `schedule_sequence`, `standing_sandbox`, `standing_sandbox_secret`, `standing_sandbox_env`, `persistent_sandbox`, `gcp_key`. `memoryTables` is `memory` and `memory_document`.

> **Note on `CONFIG_REPO_SECTION_PATHS`.** `ui/src/settingsSections.js` links some sections to config files that are no longer separate tables:
> - The Git section's `tables/git_credential.json` and `tables/repo_git_credential.json`. Git connections are now `grain_config.git_connections` and `grain_config.git_credential` (schema 22 moved them off the team row, `staterepo.movedTeamSettingColumns`), and a repository's choice is `repo_config.git_credential`.
> - The Agents section's `tables/console_session.json`. Console sessions are the `grain_config.console_sessions` JSON column.
>
> The links still open the config browser, but these fields are found in the files named above.

## 3. Deployment settings by Settings section

### 3.1 Models & sandboxes → Models (`/settings/models`)

Component: `ModelsSection.jsx`, `ModelDefaults.jsx`, `CLIRunLocations.jsx`. Surface: **bootstrap**.

| Setting (UI label, card) | API | `model.Config` / column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Provider connections (`#models`) | `agentBackends` on `PUT /api/settings`; one connection at a time via `PUT`/`DELETE /api/deployment/agent-backends/{backendId}` | `AgentBackends` / `agent_backends` (JSON) | Ordered list of `model.AgentBackend`. Each entry has the fields below. The list order is the dispatch rotation. An empty pool falls back to the legacy `AgentFramework` + model + credential. | live (tick): `dispatchConfig` and the backend pool refresh before each dispatch |
| … `id` | | | Non-empty identifier, unique in the list | |
| … `name` | | | Display name, optional | |
| … `framework` | | | One of `model.AgentFrameworkNames()`: `claude`, `codex`, `antigravity`, `custom` | |
| … `models`, `excludedModels`, `discovered` | | | Models offered or excluded by this connection. `discovered` is what the provider last reported. | |
| … `authMode` | | | Must be valid for the framework | |
| … `credentialBackendId` | | | Shares the credential of another, directly authenticated, connection of the same framework | |
| … `disabled` | | | `true` takes the connection out of rotation | |
| … `command` | | | Required when `framework` is `custom` | |
| … `cliPath` | | | Only for `claude`, `codex` and `antigravity`. Must be a valid path. | |
| … `team` | | | Legacy team scoping | |
| … `concurrency` | | | Runs at once on this connection; default `model.DefaultAgentBackendConcurrency` = 4 | |
| … `modelTurnTimeout`, `modelRetryCutoff` | | | `antigravity` only. Durations, with `modelRetryCutoff` ≤ `modelTurnTimeout`. | |
| Connection credential | `PUT`/`DELETE /api/agent-backends/{id}/credential`, `PUT`/`DELETE /api/agent-backends/{id}/codex-auth`, `PUT`/`DELETE /api/agent-keys/{framework}`, `PUT`/`DELETE /api/codex-auth` | secret store only | Write-only. `POST /api/agent-keys/{framework}/check` tests a credential without storing it. | live |
| Default model (`#model-defaults`) | `defaultModel` (`PUT /api/settings` or `PUT /api/deployment`); `defaultModels` (ordered fallback list, `PUT /api/deployment`; the UI's *Add fallback model* control, `addDefaultModel`) | `DefaultModel` / `default_model` | String. Empty means the first supported model of the configured connections, or else the framework default. | live (per dispatch) |
| Reasoning effort, per model (`#model-defaults`) | `modelSettings` on `PUT /api/deployment` | `ModelSettings` / `model_settings` (JSON `[{model, effort}]`) | Effort per model. The values offered come from `codexEfforts` and `geminiEfforts` in `GET /api/settings`. | live (next dispatch) |
| Pinned connection | `agentBackendId` (team/deployment update) | `AgentBackendID` / `agent_backend_id` | The connection a task that names no model, connection or framework is pinned to. Empty means the rotation. | live |
| Where each agent CLI runs (`#cli-run`) | `GET`/`PUT /api/settings/cli-run` | `CLIRun` / `cli_run` (JSON map, framework → `{location}`) | `location` is `host`, `container` or `detached` (`model.CLIRun*`), limited to what this host allows. A framework with no entry runs where the daemon flags (`-claude-run`, `-codex-run`, `-agy-run`), and then the built-in default, put it. | next run |
| Judgement model | not on `PUT /api/settings` (config repo) | `JudgementModel` / `judgement_model` | What the symbolic model `judgement` resolves to (pipeline steward). Empty means the strongest model the connections offer, by `JudgementModelRanking`. | live |

### 3.2 Models & sandboxes → Sandboxes (`/settings/sandboxes`)

Component: `SandboxesSection.jsx`, `CustomSandboxImageCard.jsx`. Surface: **bootstrap**. The response carries `sandboxShapeIgnored` when the active backend has no shape, such as `host`. Shape and network values are then stored but not in effect.

#### Backend and size (`#backend-and-size`)

| UI label | API key | Field / column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Sandbox backend | `sandboxBackend` | `SandboxBackend` / `sandbox_backend` | One of `container`, `granule`, `kubernetes`, `kubernetes-granule`, `host`, `none` (`model.SandboxBackendNames`), and it must be among the `sandboxBackendOptions` this daemon can run (its plumbing must have been given at startup). Empty means "resolved from startup flags". | **restart** |
| Sandbox vCPUs | `sandboxCpus` | `SandboxCPUs` / `sandbox_cpus` | `0` means grain's default (2 vCPU); otherwise ≥ 1. The default is reported as `sandboxCpusDefault`. | next sandbox |
| Sandbox memory (MiB) | `sandboxMemoryMb` | `SandboxMemoryMB` / `sandbox_memory_mb` | `0` means the default (8192 MiB); otherwise ≥ 128 | next sandbox |
| Sandbox disk (GiB) | `sandboxDiskGb` | `SandboxDiskGB` / `sandbox_disk_gb` | `0` means the default (`kontur.DefaultDiskGB`, reported as `sandboxDiskGbDefault`); otherwise ≥ 1 | next sandbox |
| Sandbox network | `networkAccess` | `NetworkAccess` / `network_access` | `open` or `none`. Empty reads as `open`. This is the default for tasks that name no network of their own. | next sandbox |
| Session sandbox idle TTL | `sessionSandboxIdleTtl` | `SessionSandboxIdleTTL` / `session_sandbox_idle_ttl_ms` | Duration. `0`/blank means 30 min (`DefaultSessionSandboxIdleTTL`). Negative turns warm retention off. Under 1 ms is refused. | live (tick) |

#### Custom sandbox image (`#custom-sandbox-image`)

| UI label | API key | Field / column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Custom sandbox image (OCI reference) | `sandboxImage` | `SandboxImage` / `sandbox_image` | One OCI image reference. Empty means the build-stamped image for the active backend (`sandboxImageDefault`). An explicit image flag (`-sandbox-image`, `-container-sandbox-image`, a granule `-kontur-image` pin) still wins. The response reports `sandboxContract`, `sandboxMinimumContractVersion`, `sandboxImageCheck` and `sandboxImageWarning`. | next sandbox |
| (no UI control) | `sandboxImageMirror` | `SandboxImageMirror` / `sandbox_image_mirror` | An OCI *repository* reference with no tag. Off-cluster `kubernetes-granule` deployments mirror the stamped guest image here and pin pods to its digest. | live (tick), via `applySandboxImage` |

#### Sandbox budget (`#sandbox-budget`)

| UI label | API key | Field / column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Overcommit mode | `sandboxOvercommit.mode` | `SandboxOvercommit` / `sandbox_overcommit` (JSON) | `fixed` (default) or `auto`. In `auto`, the controller tunes the two ratios inside the bounds below, starting from the stored ratios. | live |
| Auto CPU minimum / maximum | `sandboxOvercommit.cpuMin` / `cpuMax` | ″ | Defaults 1.0 / 6.0 (`DefaultOvercommitCPU*`) | live |
| Auto memory minimum / maximum | `sandboxOvercommit.memoryMin` / `memoryMax` | ″ | Defaults 1.0 / 2.0 | live |
| CPU pressure target (%) | `sandboxOvercommit.cpuPressureTarget` | ″ | Default 25 | live |
| Memory pressure target (%) | `sandboxOvercommit.memoryPressureTarget` | ″ | Default 5 | live |
| CPU overcommit | `sandboxCpuOvercommit` | `SandboxCPUOvercommit` / `sandbox_cpu_overcommit` | Float ≥ 0. Default 2.0. `0` means unbounded. | live (next admission) |
| Memory overcommit | `sandboxMemoryOvercommit` | `SandboxMemoryOvercommit` / `sandbox_memory_overcommit` | Float ≥ 0. Default 1.0. `0` means unbounded. | live |
| Disk overcommit | `sandboxDiskOvercommit` | `SandboxDiskOvercommit` / `sandbox_disk_overcommit` | Float ≥ 0. Default 2.0. `0` means unbounded by the sum (the floor still holds). On Kubernetes it also scales each pod's ephemeral-storage *request*. | live |
| Host memory reserve (MiB) | `sandboxMemoryReserveMb` | `SandboxMemoryReserveMB` / `sandbox_memory_reserve_mb` | Negative (default −1) means grain's reserve: ⅛ of the host, never less than 2 GiB. `0` reserves nothing. Must be less than the host's memory. | live |
| Disk floor (share of each volume) | `sandboxDiskFloorShare` | `SandboxDiskFloorShare` / `sandbox_disk_floor_share` | 0–1. Default 0.10. Effectively capped at 0.25 (`MaxDiskFloorShare`). `0` together with min `0` means no floor. | live (per disk reading) |
| Disk floor minimum (MiB) | `sandboxDiskFloorMinMb` | `SandboxDiskFloorMinMB` / `sandbox_disk_floor_min_mb` | ≥ 0. Default 5120 (`MinDiskFloorMB`). | live |

The auto-mode inputs are named `sandboxOvercommitCpuMin`, `sandboxOvercommitCpuMax`, `sandboxOvercommitMemoryMin`, `sandboxOvercommitMemoryMax`, `sandboxOvercommitCpuPressureTarget` and `sandboxOvercommitMemoryPressureTarget` in the form, and are sent as the nested `sandboxOvercommit` object. The card also shows the read-only `sandboxCommitment` (what the budget currently admits).

#### Kubernetes (`#kubernetes`)

These apply to the `kubernetes` and `kubernetes-granule` backends. Every field except the three priority classes is **restart**-only.

| UI label | API key | Column | Default, validation | Applies |
| --- | --- | --- | --- | --- |
| Kubernetes namespace | `k8sNamespace` | `k8s_namespace` | `grain`. A DNS-1123 label (≤ 63 characters: lowercase letters, digits, `-`). | restart |
| Kubectl binary | `k8sKubectl` | `k8s_kubectl` | `kubectl` | restart |
| Kubernetes sandbox pod image | `k8sSandboxImage` | `k8s_sandbox_image` | `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` (or the build stamp) | restart |
| Kubernetes Granule VM pod image | `k8sGranuleImage` | `k8s_granule_image` | `ghcr.io/bwsalmon/grain/guest:latest` (or the build stamp) | restart |
| Kubernetes workspace directory | `k8sWorkspace` | `k8s_workspace` | `/home/agent` (`kubernetes`) or `/home/debian` (`kubernetes-granule`) | restart |
| Kubernetes git & model proxy host | `k8sGitProxyHost` | `k8s_git_proxy_host` | Empty, or a host name or IP | restart |
| Kubernetes sandbox Service | `k8sSandboxService` | `k8s_sandbox_service` | A Kubernetes object name | restart |
| Kubernetes sandbox ServiceAccount | `k8sServiceAccount` | `k8s_service_account` | A Kubernetes object name; normally empty | restart |
| Kubernetes node selector | `k8sNodeSelector` | `k8s_node_selector` | A list of `key=value` | restart |
| (checkbox) privileged | `k8sPrivileged` | `k8s_privileged` | `false` | restart |
| (checkbox) NetworkPolicy enforced | `k8sNetworkPolicy` | `k8s_network_policy` | `false`; asserts the CNI enforces `deploy/kubernetes/networkpolicy.yaml` | restart |
| Kubernetes Deployment | `k8sDeployment` | `k8s_deployment` | `grain`; the Deployment the Upgrade button repoints | restart |
| Kubernetes container | `k8sContainer` | `k8s_container` | `grain` | restart |
| PriorityClass (interactive / agent / CI) | `k8sPriorityClassInteractive`, `k8sPriorityClassAgent`, `k8sPriorityClassCi` | `k8s_priority_class_interactive`, `k8s_priority_class_agent`, `k8s_priority_class_ci` | `grain-interactive` / `grain-agent` / `grain-ci`. An empty string means **no** priority class, not the default. Must be a valid PriorityClass name. | restart (they are in `restartOnlySettings`) |

### 3.3 Models & sandboxes → Executors (`/settings/executors#executors`)

Component: `ExecutorsSection.jsx` → `ExecutorsPanel`. Card: *Executor join tokens*.

| Setting | API | Store | Notes | Applies |
| --- | --- | --- | --- | --- |
| Executor join tokens | `GET`/`POST /api/executors/tokens`, `POST /api/executors/tokens/{id}/revoke`, `DELETE /api/executors/tokens/{id}` | `executor_token` (a **runtime** table, not exported) | A token a remote executor host joins with. The secret is shown once at creation. The listener itself is the startup flag `-executor-addr` (§10). | immediate |

### 3.4 Secrets & access → Legacy capabilities (`/settings/credentials#credentials`)

Component: `CredentialsSection.jsx`. The aliases `/settings/secrets` and `/settings/legacy-capabilities` resolve here. Surface: **config-managed** (the secret values themselves are never in the config repo).

| UI label | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| GitHub host | `githubHost` | `github_host` | Host (normalised by `gitHubHostSetting`). Required the first time settings are saved. Default `github.com`. | **restart** |
| (checkbox) plain HTTP | `githubInsecureHttp` | `github_insecure_http` | `false`; for mock servers only | **restart** |
| GCP project | `gcpProject` | `gcp_project` | Empty disables `gcp-key` and `gemini-key` | live (tick): the capability registry is rebuilt |
| GCP service account email | `gcpServiceAccountEmail` | `gcp_service_account_email` | The narrow agent SA `gcp-key` mints keys for | live (tick) |
| GCP service account count | `gcpServiceAccountCount` | `gcp_service_account_count` | `0` or `1` means a single account; `N` means a numbered pool | live (tick) |
| Secret values | `GET /api/secrets` (names only), `PUT /api/secrets/{secret}/{key}`, `DELETE /api/secrets/{secret}[/{key}]`, `PUT`/`DELETE /api/github-tokens/{name}` | `<data-dir>/secrets` | Write-only. A GitHub token cannot be deleted while a git connection names it. | immediate |

### 3.5 Secrets & access → Access (`/settings/access#this-browser`)

Component: `AccessSection.jsx`. Card: *This browser*. It shows how this browser is signed in. None of it is stored in the config repo or the database.

| Mechanism | Where it is set | Notes |
| --- | --- | --- |
| Access code / session sign-in | `grain access-code` (and `grain access-code rotate`); the token file under `<data-dir>` (`pkg/ui/accesscode.go`); `GET /api/auth/access-code` | 160-bit code. Each use renews the session. Rotating ends every session at once. The daemon re-reads the file periodically (`accessCodeRecheck`). Session lifetime: `-ui-access-session-lifetime`. |
| Cloud IAP identity | `-ui-iap-audience` / `$GRAIN_IAP_AUDIENCE`, `-ui-iap-issuer` / `$GRAIN_IAP_ISSUER`, `-ui-iap-keys-url` / `$GRAIN_IAP_KEYS_URL` | The signed `X-Goog-IAP-JWT-Assertion` is verified per request ([authentication design](../../design/authentication.design.md)) |
| Allowed hosts | `-ui-allowed-hosts` | Host names the UI answers to, besides IP literals, localhost and the hostname |
| External agent API | `externalAgentApi` on `PUT /api/settings`; column `external_agent_api` | Default `false`. Enables `/mcp` bearer-token auth for external agents. **No Settings-page control.** Applies live (per request). Tokens: §8.9. |

### 3.6 System / Operations → Upgrade (`/settings/upgrade`)

Component: `UpgradeSection.jsx` → `UpgradePanel`. The panel holds the imperative actions: `POST /api/upgrade` (branch or image, optional `afterTasksFinish`), `DELETE /api/upgrade/scheduled`, and the Postgres migration `POST /api/database/upgrade`. These are actions, not standing settings. The standing upgrade *policy* is in General → Updates (§3.10). Startup-only knobs: `-upgrade-src-dir`, `-upgrade-install-path`, `-upgrade-image`, `-upgrade-allowed-branches`, `-upgrade-drain-window`, `-upgrade-image-ref-file`, `-upgrade-restart-cmd`, `-upgrade-verify-cmd`, `-restart-cmd`, `-reboot-cmd`, `-shutdown-cmd` (§10).

### 3.7 System / Operations → Caches (`/settings/caches`)

Component: `CachesSection.jsx`. Surface: config-managed (`grain_config`).

| UI label (card) | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Proxy cache enabled (`#proxy-cache`) | `packageCacheEnabled` | `package_cache_enabled` | Default `true`. Points apt, pip, npm, Go and the Docker registry at the controller's cache. | next sandbox |
| Proxy cache max size (GiB) | `packageCacheSizeGb` | `package_cache_size_gb` | `0` means 20 GiB (`packageCacheSizeGbDefault`); otherwise ≥ 1 | live (`SetMaxBytes`) |
| Actions cache enabled (`#actions-cache`) | `actionsCacheEnabled` | `actions_cache_enabled` | Default `true`. Hands CI jobs `ACTIONS_CACHE_URL`. Every branch of a repository shares the cache. | next CI job |
| Actions cache max size (GiB) | `actionsCacheSizeGb` | `actions_cache_size_gb` | `0` means 20 GiB; otherwise ≥ 1 | live |

Actions, not settings: `POST /api/package-cache/flush[?kind=]`, `POST /api/actions-cache/flush[?repo=]`, `DELETE /api/ci/cache`. Cache directories and upstreams are startup flags: `-package-cache-dir`, `-package-cache-*-registry`, `-package-cache-pypi-index`, `-package-cache-go-proxy`, `-package-cache-apt-upstream`, `-actions-cache-dir`.

### 3.8 System / Operations → Engine (`/settings/engine`)

Component: `EngineSection.jsx`.

| UI label (card) | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Poll interval (`#polling`) | `pollInterval` | `poll_interval_ms` | Duration > 0. Default 10 s. Required on first save. | live: retimes the running loop |
| Max worker agents (`#polling`) | `maxWorkers` | `max_workers` | ≥ 1. Default 10. **Bootstrap.** | live (tick) |
| Max merges per repo branch (`#merge-queue`) | `maxMergesPerRepo` | `max_merges_per_repo` | ≥ 1. Default 1. Ready PRs landed per cycle per lane when the overlap re-check is not enforced. | live (cycle) |
| Merge batch size (`#merge-queue`) | `mergeBatchSize` | `merge_batch_size` | 0 (default 3) or 1–20 (`MaxMergeBatchSize`); `1` turns batching off. **Retired (grain/task-1023):** validated and stored, but ignored. The response's `mergeBatching` / `mergeBatchSizeChosen` say what is in force. | n/a (ignored) |
| Merge gather window (`#merge-queue`) | `mergeGatherWindow` | `merge_gather_window_ms` | Duration ≤ 30 min. Blank means 5 min. Negative means no wait. Under 1 ms is refused. **Retired**, like batch size. | n/a (ignored) |

### 3.9 System / Operations → Config repo (`/settings/config-repo#config-repo`)

Component: `ConfigRepoSection.jsx` → `StateRepoPanel`. Shows and changes the binding in `<data-dir>/state-repo.json` (§9): `POST /api/state-repo/remote` (an external remote, or `none: true` for internal), `POST /api/state-repo/secrets-repo`, `POST /api/state-repo/secrets-key` / `import-key`, `POST /api/state-repo/sync`, `POST /api/state-repo/reset-notice`, and `POST /api/state-repo/pull-requests` (open a held config PR carrying a refused write's `change`). `GET /api/config` reports `configRepoOnly`, `settingsLocked` and `externalConfigRepo`. The lock itself, `settings_locked`, is in General (§3.10).

### 3.10 Managed in config repo → General (`/settings/general`)

Component: `GeneralSection.jsx`. Config file: `tables/grain_config.json`.

| UI label (card) | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| About (`#about`) | — | — | Read-only build and version (`settings-version-display`) | — |
| Environment name (`#deployment`) | `environmentName` | `environment_name` | Free text, trimmed, ≤ 32 characters, no line breaks or tabs. Empty means unnamed (no badge). | live (per request) |
| Show closed tasks by default (`#deployment`) | `showClosedByDefault` | `show_closed_by_default` | Default `false`. Only the starting state of a task list. | live |
| Settings lock (`#settings-lock`) | `settingsLocked` | `settings_locked` | Default `false`. When `true` with an internal config repo, non-bootstrap settings become read-only (mode 3). A person may unlock it while locked (`bootstrapOnlyChange`); an agent may not (`checkGrainConfigWritable`). Not offered in external mode (`externalConfigRepo`). **Bootstrap.** | live (per request) |
| Upgrade check mode (`#updates`) | `upgradeCheckMode` | `upgrade_check_mode` | `off`, `approve` or `auto` (default; empty reads as `auto`). Whether a post-upgrade health-check task is filed and whether it is queued at once. Each upgrade is checked at most once. | live (cycle) |
| Pause pipeline during check (`#updates`) | `upgradeCheckPausesPipeline` | `upgrade_check_pauses` | Default `false`. Honoured only when a check is filed. The pause is lifted at the check's deadline. | live (cycle) |
| Auto-upgrade channel (`#updates`) | `autoUpgradeChannel` | `auto_upgrade_channel` | Empty (default) means no automatic upgrades. Otherwise `latest`, `candidate`, `main` or any branch `-upgrade-allowed-branches` admits; checked when saved. | live (cycle) |
| Auto-upgrade time (`#updates`) | `autoUpgradeTime` | `auto_upgrade_time` | `HH:MM`, 00:00–23:59, on the deployment's `timeZone`. Default `03:00`. This is when the queue drains, not necessarily when the restart happens. | live (cycle) |

### 3.11 Managed in config repo → Git (`/settings/git#git`)

Component: `GitSection.jsx`, `GitConnections.jsx`.

| Setting | API | Field / column | Type, validation | Applies |
| --- | --- | --- | --- | --- |
| Git connections table | `GET`/`POST /api/deployment/git-connections`, `DELETE /api/deployment/git-connections/{cred}`, `POST /api/deployment/git-connections/check` (test without saving); `GET /api/git-ambient-sources` | `GitConnections` / `git_connections` (JSON `[{credential, host, scope}]`) | `credential` names a secret-store credential. `host` defaults to the deployment's git host. `scope` is an optional owner or `owner/name`. The form fields are Kind, Auth type (PAT, SSH private key with known hosts, or GitHub App ID + installation ID + private key), Host, Scope, Proxy and Preset. Secret parts go to the secret store, never to the row. | live (next clone/push) |
| Legacy default connection | (team update) | `GitCredential` / `git_credential` | The single pre-connections credential. It is still read as the default connection for the default host. | live |
| Per-repository connection | `PUT /api/deployment/repos/{owner}/{name}/git-connection` | `repo_config.git_credential` | Names one connection | live |

### 3.12 Managed in config repo → Repositories (`/settings/repositories#repositories`)

Component: `RepositoriesSection.jsx`. Config files: `tables/repo_config.json`, `tables/grain_config.json`.

| Setting | API | Field / column | Validation | Applies |
| --- | --- | --- | --- | --- |
| Target repositories | `POST /api/repos`, `DELETE /api/repos/{owner}/{name}`, `targetRepos` on `PUT /api/deployment`; `GET /api/deployment/reachable-repos` lists the candidates | `TargetRepos` / `target_repos` (JSON list of `owner/name`) | Empty means every repository. Work is only dispatched into repositories in the list (`RepoAllowed`). | live |
| Per-repository rows | §6 | `repo_config` | — | — |

### 3.13 Managed in config repo → Tasks (`/settings/tasks`)

Component: `TasksSection.jsx`. Config files: `tables/grain_config.json`, `settings/capabilities.yml`, `tables/custom_field.json`.

| UI label (card) | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| New tasks join the top (newest first) (`#task-defaults`) | `newestFirst` | `newest_first` | Default `false` (new tasks join the bottom of the backlog) | next task |
| Queue immediately (`#task-defaults`) | `approvedByDefault` | `approved_by_default` | Default `true`. Starting state of the new-task form only. | next task (form) |
| Auto-merge once checks pass (`#task-defaults`) | `autoMergeByDefault` | `auto_merge_by_default` | Default `true`. Starting state only. | next task (form) |
| Default legacy capabilities (`#task-defaults`, `#default-grants`) | `defaultCapabilities` | `default_capabilities` (JSON list); also `settings/capabilities.yml` `defaults.task` | Each id must be one this build offers (`capabilityRefusal`). Seeded onto new tasks as removable grants. Unioned with the repository's own defaults first. | next task |
| Max unreviewed follow-up depth (`#backlog`) | `maxAutoSubmitDepth` | `max_auto_submit_depth` | ≥ 1. Default 2. `0` stored reads as the default. | live (as each proposal is filed) |
| Max unreviewed follow-up per task (`#backlog`) | `maxAutoSubmitFanOut` | `max_auto_submit_fan_out` | ≥ 1. Default 5. | live |
| Task fields (`#task-fields`) | `TaskFieldsCard` (custom field routes) | `custom_field` table → `tables/custom_field.json` | Fields: `name`, `label`, `type`, `options`, `multiple`, `description`, `default`, `position`, `showOnRow` | live |
| Default task filters | not on `PUT /api/settings` (config repo; read in `GET /api/config`) | `default_filters` | A query string of repeated `f=` conditions. Default `f=state:none:deferred|closed&f=kind:none:interactive`. Empty explicitly means no filters. | live |

The section also links the per-repository CI gate (`#ci-gate-per-repo`, §6).

### 3.14 Managed in config repo → Agents (`/settings/agents`)

Component: `AgentsSection.jsx`. Config files: `tables/grain_config.json` (including `console_sessions`).

| UI label (card) | API key | Column | Type, default, validation | Applies |
| --- | --- | --- | --- | --- |
| Max agent turns (`#agent-runs`) | `maxAgentTurns` | `max_agent_turns` | ≥ 0. `0` (default) means the framework default, which is uncapped. | next run |
| Standing instructions for every run (`#standing-instructions`) | `promptExtension` | `prompt_extension` | Free text, trimmed. Read at dispatch, not seeded. Composed with the repository's text (§6) and the repository's declared text (§7). | live (next run) |
| Branch name template (`#task-branches`) | `taskBranchTemplate` | `task_branch_template` | Must contain `{id}` when set. Also expands `{deployment}`, `{instance}` and `{slug}`. Empty means the legacy `grain/task-{id}`. New deployments are seeded with `grain/{deployment}/task-{id}-{slug}` (`taskBranchTemplateDefault`). | next task (frozen at creation) |
| Deployment ID (`#task-branches`) | `deploymentId` | `deployment_id` | A short name for `{deployment}`. Empty means the random id derived from the installation (`deploymentIdDefault`). | next task |
| Commit identity (`#commit-identity`) | `agentGitName`, `agentGitEmail` on `PUT /api/deployment` | `agent_git_name`, `agent_git_email` | Used where the git credential names nobody. Empty means the sandbox default. | next run |
| Standing console sessions | `GET`/`POST /api/console-sessions`, `PUT`/`DELETE /api/console-sessions/{id}`, `POST /api/console-sessions/{id}/restart` (`carryHandover`) and `/cancel` | `ConsoleSessions` / `console_sessions` (JSON) | Each `model.ConsoleSession` has `ID`, `Title`, `titleSource`, `team`, `Capabilities`, `NetworkAccess`, `TasksApproved`, `keepRunning`, `reads`, `writes`, `googleChatSpace` and `model`. `nil` means the default (the director); an empty list means none. The director session may not be removed by a config PR (`validate_pr.go`). | live |
| Repair template | `repairTemplateID` | `repair_template_id` | A template id used when filing base-branch repairs and release triage. A repository's own (§6) is asked first. **No Settings-page control.** | live (per filing) |
| Default framework | — (config repo) | `agent_framework` | The framework a task with no model, connection or framework runs on. Empty means the default. | live |

### 3.15 Managed in config repo → Capabilities (`/settings/capabilities`)

Component: `CapabilitiesSection.jsx`. Config file: `settings/capabilities.yml`, which projects `grain_config.mcp_servers`, `grain_config.capability_policy`, `grain_config.default_capabilities`, `repo_config.default_capabilities` / `ci_capabilities` and the `api_scope_set` table, and takes precedence over them on import.

| Card | API | Store | Shape and validation | Applies |
| --- | --- | --- | --- | --- |
| Capability servers (`#capability-servers`) | `GET`/`PUT /api/capability-servers` (`renameFrom` renames), `POST …/remove`, `…/enable`, `…/disable`, `…/test`, `…/test-script`, `…/bind-secret`, `…/rotate-managed`, `…/dismiss-notice`, `…/gchat-test` | `MCPServers` / `mcp_servers` (JSON map `capability_id → protocol.ServerConfig`) | In YAML, `servers:` is a list of `{id, name, url \| command+args \| run, env, headers, enabled, builtin, aliases, notice, secrets, config, methods, restrictions, presets}`. Sensitive values are `$secret:<name>` references. UI and CLI writes are validated with `protocol.ParseServers`. A bad entry from a config PR is refused on its own when it is attached. Built-in servers are disable-only. | live (re-attached) |
| Capability policy (`#capability-policy`) | `GET`/`PUT /api/capability-policy`, `POST /api/capability-policy/preview` | `CapabilityPolicy` / `capability_policy` (JSON) | `rules: [{capability, repos, runKinds, events, allowUntrusted, deny}]` plus an optional `catalogue`. Checked at admission before anything is minted. | live |
| API scope sets (`#api-scope-sets`) | `GET`/`PUT /api/api-scope-sets`, `POST /api/api-scope-sets/delete`, `GET /api/api-scope-sets/usage` | `api_scope_set` table (`api_scopes` in YAML) | `name`, `description`, `scopes`, `includes`, `repos`, `deny`, `requiresPerson`, `version`, `deprecated`. Declaring a set grants it to nobody. | live |
| In-process capabilities | — | `in_process:` in `capabilities.yml` | Per-capability configuration of in-tree capabilities | live |

### 3.16 Managed in config repo → CI (`/settings/ci`)

Component: `CISection.jsx`. Config files: `tables/grain_config.json`, `tables/repo_config.json`. Each field's grain default is reported beside it as `<key>Default`.

| UI label (card) | API key | Column | Readings and default | Applies |
| --- | --- | --- | --- | --- |
| CI jobs per run (`#ci-queue`) | `ciJobsPerRun` | `ci_jobs_per_run` | ≥ 0. `0` (default) means no per-run limit. | live (per run start) |
| CI queue age boost (`#ci-queue`) | `ciAgeBoost` | `ci_age_boost_ms` | Duration ≥ 0. Blank or `0` means 2 m. Negative is refused (it would starve the queue). | live (per admission) |
| CI capacity reserved after (`#ci-queue`) | `ciReserveAfter` | `ci_reserve_after_ms` | Duration. Blank or `0` means 1 m. | live |
| CI runs kept (`#ci-history`) | `ciRunHistory` | `ci_run_history` | Positive is the bound. `0` means 5000. Negative means unbounded. | live (per sweep) |
| CI step output kept (MiB) | `ciStepOutputMb` | `ci_step_output_mb` | Positive, `0` (64 MiB) or negative (unbounded) | live |
| CI step output age (days) | `ciStepOutputDays` | `ci_step_output_days` | Positive, `0` (14 days) or negative (unbounded) | live |
| CI output per run (KiB) | `ciRunOutputKb` | `ci_run_output_kb` | Positive, `0` (default) or negative | live |
| CI job cache size (MiB) (`#ci-job-cache`) | `ciCacheMb` | `ci_cache_mb` | ≥ 0. `0` means 256. Negative is refused: CI disk has no unbounded setting. | live |
| CI job cache entries | `ciCacheEntries` | `ci_cache_entries` | ≥ 0. `0` means 4096. | live |
| CI job cache age (days) | `ciCacheDays` | `ci_cache_days` | ≥ 0. `0` means 30. | live |
| CI artifacts kept (MiB) (`#ci-artifacts`) | `ciArtifactMb` | `ci_artifact_mb` | ≥ 0. `0` means 2048. | live |
| CI artifacts per run (MiB) | `ciArtifactRunMb` | `ci_artifact_run_mb` | ≥ 0. `0` means 128. | live |
| Largest CI artifact (MiB) | `ciArtifactFileMb` | `ci_artifact_file_mb` | ≥ 0. `0` means 32. | next job |
| CI artifacts age (days) | `ciArtifactDays` | `ci_artifact_days` | ≥ 0. `0` means 14. | live |

Per-repository CI (mode, capabilities, workflows) is in §6. The CI-job ceiling `max_ci_jobs` is in §4.

### 3.17 Managed in config repo → Memory (`/settings/memory#memory`)

Component: `MemorySection.jsx` → `MemoryPanel`. Config path: `memory/deployment.md` (and `memory/**` for repositories and named documents).

| Setting | API | Store | Notes | Applies |
| --- | --- | --- | --- | --- |
| Deployment memory | `GET`/`PUT /api/memory` (with `repo` for one repository) | `memory` table → `memory/**` | Free text given to runs | next run |
| Memory documents | `GET /api/memory/documents[/{name}]`, `PUT /api/memory/documents` | `memory_document` table | Named documents, per deployment or per repository | next run |

## 4. Deployment settings with no Settings-page control

These `model.Config` fields are real and exported to `tables/grain_config.json`, but no Settings page card edits them. Some are written by another route, and the rest only by a config PR (or `grain settings` / `PUT /api/settings` where a key exists).

| Field / column | Write path | Type, default, semantics | Applies |
| --- | --- | --- | --- |
| `MaxMergers` / `max_mergers` | config repo | Capacity on top of `max_workers` that only merge-queue fix tasks may use. `0` is meaningful: mergers then contend with workers. Default 1. (The `-max-mergers` flag is deprecated and ignored.) | live |
| `MaxCIJobs` / `max_ci_jobs` | config repo (the `sandbox_commitment` planner may zero it) | CI job sandboxes at once, independent of workers. Default 1. (The `-max-ci-jobs` flag is deprecated and ignored.) | live |
| `BaseCIRedStreak` / `base_ci_red_streak` | config repo | Consecutive judged base-branch tips that fail the same job before the branch is treated as confirmed red without a re-run. `0` means 2. | live |
| `TimeZone` / `time_zone` | `timeZone` on `PUT /api/settings` | An IANA zone. Empty or `UTC` means UTC. The default zone every timestamp is printed in, carried in `GET /api/config`. A browser may override it locally (§11). | live (per request) |
| `DefaultFilters` / `default_filters` | config repo | §3.13 | live |
| `JudgementModel` / `judgement_model` | config repo | §3.1 | live |
| `AgentFramework` / `agent_framework` | config repo | §3.14 | live |
| `RepairTemplateID` / `repair_template_id` | `repairTemplateID` | §3.14 | live |
| `ExternalAgentAPI` / `external_agent_api` | `externalAgentApi` | §3.5 | live |
| `SandboxImageMirror` / `sandbox_image_mirror` | `sandboxImageMirror` | §3.2 | live |
| `IAPBackendService` / `iap_backend_service` | `iapBackendService` | The GCP backend service whose IAP IAM policy is reconciled. Empty disables reconciliation. | live (tick, plus `TriggerIAPSync`) |
| `IAPResource` / `iap_resource` | `iapResource` | The IAP web resource to reconcile as well, for example `projects/<N>/iap_web/cloud_run-<region>/services/<svc>`. Empty derives it from a Cloud Run relay's audience. `none` turns that off. Validated. | live |
| `AgentBackendID`, `GitCredential`, `ModelSettings`, `AgentGitName`, `AgentGitEmail`, `TargetRepos` | `PUT /api/deployment`, legacy team update | §3.1, §3.11, §3.12, §3.14 | live |

## 5. Retired and ignored `grain_config` columns

These columns are still read and written for dump fidelity, and nothing acts on them:

- `merge_batch_size`, `merge_gather_window_ms`: retired by grain/task-1023 (§3.8).
- `password_auth_enabled`, `open_registration`, `google_oauth_enabled`, `google_oauth_client_id`, `google_oauth_client_secret`, `google_oauth_allowed_domains`: in-app sign-in from before IAP and access-code sign-in ([authentication design](../../design/authentication.design.md)). There is no API key for any of them.

A new field that holds a credential belongs in `pkg/secrets`, never here (the `model.Config` doc comment).

## 6. Per-repository settings (`repo_config`, `/repos/<owner>/<name>/settings/<tab>`)

Store: `model.RepoConfig`, one row per repository → `tables/repo_config.json`. The setup command is externalised to `scripts/repos/<owner>/<name>/setup.sh`. Surface: **config-managed**. Write routes:
- `PUT /api/repos/{owner}/{name}/settings` (`ui.SetRepoSettingsRequest`, only the fields given are changed).
- One single-field route per setting, listed below.
- `PUT /api/deployment/repos/{owner}/{name}/config` (operator-level fields, `json` keys as given).

`GET /api/repos/{owner}/{name}/settings` reads them. Every repository field applies **live** (it is read at dispatch, at merge, or per cycle) unless the row says otherwise.

| Tab | UI control | API key (route) | Field / column | Type, default, semantics |
| --- | --- | --- | --- | --- |
| overview | Merge batches panel, runs (read-only) | — | — | — |
| ci | CI mode | `ciMode` (`PUT …/ci-mode`) | `CIMode` / `ci_mode` | `grain` (`.grain/workflows` gates, GitHub checks ignored), `github` (GitHub checks gate) or `none` (no CI gate). Empty derives from the legacy `ignore_github_ci`. |
| ci | Ignore GitHub CI (legacy) | `ignoreGitHubCI` (`PUT …/ignore-github-ci`) | `IgnoreGitHubCI` / `ignore_github_ci` | Bool. A repository layer can only add to a deployment's own refusal, never subtract. |
| ci | Watch default branch | `watchDefaultBranch` (`PUT …/watch-default-branch`) | `WatchDefaultBranch` / `watch_default_branch` | Bool. Seed base-branch CI and repair it when red. Only has effect when grain CI is enabled for the repository. |
| ci | CI capabilities | `ciCapabilities` (`PUT …/ci-capabilities`) | `CICapabilities` / `ci_capabilities` | The explicit capability set for CI jobs (also `capabilities.yml` `repos.<repo>.ci`) |
| ci | Workflows editor | `PUT …/workflows` (`{workflows:[{path,text}]}`) | `repo_workflow` table (`Repo`, `Path`, `Text`) → `workflows/<owner>/<name>/…` | The deployment's own grain CI workflows for the repository, alongside `.grain/workflows` |
| merge-queue | Require branch up to date | `requireBranchUpToDate` (`PUT …/require-branch-up-to-date`) | `RequireBranchUpToDate` / `require_branch_up_to_date` | Bool |
| merge-queue | Merge on green | `mergeOnGreen` (`PUT …/merge-on-green`) | `MergeOnGreen` / `merge_on_green` | Bool. Merge a green, clean PR without waiting behind earlier entries. |
| merge-queue | Base repairs bypass human gates | `baseRepairBypassesHumanGates` (`PUT …/base-repair-bypasses-human-gates`) | `BaseRepairBypassesHumanGates` / `base_repair_bypasses_human_gates` | Bool |
| merge-queue | Checks that cannot pass | `unfixableChecks` (`PUT …/unfixable-checks`) | `UnfixableChecks` / `unfixable_checks` | Exact check names, case-insensitive, never substrings |
| merge-queue | Read declarations (also on advanced) | `declarations` (`PUT …/declarations`) | `Declarations` / `declarations` | Bool, default off. Whether grain reads this repository's `.grain/*` (§7). Never declarable by the repository itself. |
| (no control) | — | `mergeRecheck` (`PUT …/settings`) | `MergeRecheck` / `merge_recheck` | `off`, `observe` or `enforce`. Under `enforce`, each entry is checked against the live tip before merging, and `max_merges_per_repo` is not spent. |
| agents | Default capabilities | `defaultCapabilities` (`PUT …/capabilities`) | `DefaultCapabilities` / `default_capabilities` | Ids added (never subtracted) to the deployment seed for new tasks on this repository. Applies to the next task. |
| agents | Standing instructions | `promptExtension` (`PUT …/prompt-extension`) | `PromptExtension` / `prompt_extension` | Text appended after the deployment's. Read at dispatch. |
| agents | Setup command | `setupCommand` (`PUT …/setup-command`) | `SetupCommand` / `setup_command` → `scripts/repos/<owner>/<name>/setup.sh` | Shell run in each new checkout, before the repository's declared one. Applies to the next sandbox. |
| agents | Issue import | `issueImportEnabled`, `issueImportRepo`, `issueImportLabel`, `issueImportApproved`, `issueImportIntervalSeconds` (`PUT …/settings`); `POST …/import-issues` runs it now | `IssueImport` / `issue_import` (JSON) | `Repo` empty means this repository. `Label` filters by label. `Approved` files the tasks queued. Interval `0` means 10 min, floored at 1 min. |
| (no control) | — | `repairTemplateID` (`PUT …/settings`) / `repairTemplateId` (deployment route) | `RepairTemplateID` / `repair_template_id` | Asked before the deployment's repair template |
| branches | Create branch (action) | `POST …/branches` | — | — |
| releases | Work ownership | `PUT …/work-ownership` | `Work` / `work` (JSON `{Default, Overrides}`) | `on`, `off` or unset, per item: `release`, `base-ci`, `post-merge`, `ports`, `triage`. Never declarable by the repository. |
| releases | Release workflow settings | `PUT …/release` | `BaseBranchCIJobs` / `base_branch_ci_jobs`; `PostMergeDispatchJobs` / `post_merge_dispatch_jobs` | The jobs that judge base branches, and the jobs dispatched after a merge |
| releases | Release secrets | `PUT`/`DELETE …/release-secrets/{secret}`; `releaseSecrets` (deployment route) | `ReleaseSecrets` / `release_secrets` (names only; values in the secret store) | Write-only values for release jobs |
| releases | Qualification plan | `PUT …/qualification-plan` | `qualification_config`, `qualification_item`, `qualification_item_depends_on` | The items a release candidate is qualified by, and whether a run needs approval |
| advanced | Port directions | config repo | `PortBranches` / `port_branches`, `PortTypes` / `port_types` | Operator layer, unioned with the declared `port-branches` / `port-types` (§7) |
| (operator) | — | `cloneUrl`, `gitCredential` (`PUT /api/deployment/repos/{owner}/{name}/config`, `…/git-connection`) | `CloneURL` / `clone_url`, `GitCredential` / `git_credential` | Overrides the GitHub proxy clone URL and the git connection |
| (read-only) | — | — | `DeclaredPromptExtension`, `DeclaredSetupCommand`, `DeclaredPortBranches`, `DeclaredPortTypes` | Copied from `.grain/settings.yml` when declarations are on. These are not operator-writable. |

## 7. Repository-declared settings (`.grain/*`)

A repository's own tree is read only when `repo_config.declarations` is on. The rule is **authority, not behaviour** ([`docs/declarations.md`](../../../docs/declarations.md), `pkg/declschema.RepoSettingsSchema`): a repository may declare what its own work does, never what that work may do or who judges it.

| File | Keys | Composition |
| --- | --- | --- |
| `.grain/settings.yml` (or `.grain/config.yml`) | `prompt-extension`, `setup-command`, `port-branches`, `port-types` (`<from>-><to>: [types]`) | Appended or unioned after the operator's layer, never replacing it. `port-types` only narrows pairs the repository declared itself. Everything else is refused by name: authority (`default-capabilities`, `capabilities`, `ci-capabilities`, `release-secrets`, `clone-url`, `repo`), verdict (`ignore-github-ci`, `unfixable-checks`, `base-branch-ci-jobs`, `require-branch-up-to-date`, `watch-default-branch`), bootstrap (`declarations`) and ownership (`work-ownership`, `release-workflows-disabled`). |
| `.grain/review.yml` (or `.yaml`, or `.grain/templates/review.yml`) | `name`, `title`, `prompt` / `body`, `network`, `custom-fields` | The default review template for tasks on the repository |
| `.grain/templates/<stem>.yml` | Template fields (§8.2) | Row id is `model.RepoDeclaredID` |
| `.grain/suites/<stem>.yml` | Suite fields (§8.3) | — |
| `.grain/schedules/<stem>.yml` | Schedule fields (§8.4) | Runs only where `work-ownership` says this deployment owns the repository's standing work |
| `.grain/triggers/<stem>.yml` | Event triggers (`pkg/repodecl/triggers.go`) | — |
| `.grain/merge-rules.yml` | `ci: {mode, grain, github}`, `auto_merge`, `stall_timeout`, path rules `{id, description, paths, review}`; `batching`, `repairs`, `flake_recheck` and `port` are reserved | `pkg/repodecl/merge_rules.go` |
| `.grain/workflows/<name>.yml` | grain CI workflow, including its own cron | Read when the repository's CI mode is `grain` |
| `.grain/lifecycle.yml` | `name`, `image`, `serve: {run, working-directory, listen, env, ready-timeout-seconds, request-timeout-ms, sandbox: {cpus, memory-mb}}`; `build`, `capabilities`, `secrets`, `repo`, `target`, `reads`, `writes` and `grants` are reserved | `pkg/repodecl/lifecycle.go`. Defaults: ready timeout 30 s, request timeout 5000 ms. |

A declaration is checked before a push with `grain declarations check`, and its status is shown by `GET /api/repos/{owner}/{name}/declarations`.

## 8. Composed and auxiliary configuration entities

These are all config-managed and exported through `staterepo.SettingsTables`, except where noted. Each applies live from the next read.

| § | Entity | Routes | Config repo path | Fields |
| --- | --- | --- | --- | --- |
| 8.1 | Boards | `GET`/`POST /api/boards`, `PATCH`/`DELETE /api/boards/{id}` | `tables/board*.json` | `ID`, `Name`, `Folder`, `Kind` (kanban, panels, report), `Description`, `Query`, `Columns`, `Panels`, `Sections`, `OrderKey` |
| 8.2 | Templates | `POST /api/templates`, `PATCH`/`DELETE /api/templates/{id}` (`revision` guards concurrent edits); `POST /api/tasks/from-template` | `settings/templates.yml` + `tables/template*.json` | `ID`, `Name`, `Title`, `Body`, `Fields`, `Target`, `ChangeTarget`, `Reviewers`, `Base`, `CustomFields`, `NetworkAccess`, `NothingNeededAction`, `Goal`, `Model`, `Reads`, `Writes`, `Grants`, `AutoMerge`, `Interactive`, `KeepRunning`, `Priority` (default 5) |
| 8.3 | Suites | `POST /api/suites`, `PATCH`/`DELETE /api/suites/{id}`, `POST /api/suites/composition` | `settings/suites.yml` + `tables/suite*.json` | `ID`, `Name`, `Items`, `Mode`, `Count`, `MaxPasses`, `RequireApproval`, `AutoMerge`, `AutoApproveProposals` |
| 8.4 | Schedules | `GET`/`POST /api/schedules`, `PATCH`/`DELETE /api/schedules/{id}`, `POST /api/schedules/{id}/run`, `POST /api/schedules/composition` | `settings/schedules.yml` + `tables/schedule*.json` | `ID`, `Title`, `Body`, `Target`, `Base`, `AutoMerge`, `AutoApproveProposals`, `Reads`, `Writes`, `Grants`, `NetworkAccess`, `NothingNeededAction`, `KeepRunning`, `Model`, `WorkflowReview`, `CustomFields`, `TemplateID` / `SuiteID`, `Recurrence`, `TimeZone`, `Overlap`, `Enabled`. `NextRunAt` and `LastRunAt` are runtime. |
| 8.5 | Custom fields | the task-fields card (§3.13) | `tables/custom_field.json` | §3.13 |
| 8.6 | API scope sets | §3.15 | `settings/capabilities.yml` `api_scopes` / `tables/api_scope_set.json` | §3.15 |
| 8.7 | GCP keys | `GET`/`POST /api/gcp-keys`, `GET`/`PUT`/`DELETE /api/gcp-keys/{name}` | `tables/gcp_key.json` | `Name`, `ProjectID`, `ServiceAccountEmail`, `ServiceAccountCount`, `MinterCredential` (a credential reference, not material), `MaxKeyAge`, `Description` |
| 8.8 | Standing and persistent sandboxes | System → Sandboxes | `tables/standing_sandbox*.json`, `tables/persistent_sandbox.json` | Standing: `Name`, `Image`, `Command`, `Dir`, `Env`, `Secrets` (references), `CPUs`, `MemoryMB`, `DiskGB`, `NetworkAccess`, `Enabled`. Persistent: also `Repo`, `Ref`, `AutoUpdate`, `Capabilities`, `SandboxPort`, `HostPort`, `UISubpath`, `StripPrefix`, `Refresh`, `RollbackOnCrash`. |
| 8.9 | External agent tokens | `GET`/`POST /api/external-agents/tokens`, `POST …/{id}/rotate`, `POST …/{id}/revoke`, `DELETE …/{id}` | **not exported** (runtime, hash only) | `id`, `name`, `prefix`, `scopeSet`, `scopes`, `repos`, `rateLimit` (default 60), `maxConcurrent` (default 8), `expiresAt`, `revokedAt`. The secret is shown once. Needs `externalAgentApi`. |
| 8.10 | Executor tokens | §3.3 | **not exported** | — |
| 8.11 | Console sessions | §3.14 | `grain_config.console_sessions` | §3.14 |
| 8.12 | Git connections | §3.11 | `grain_config.git_connections` | §3.11 |
| 8.13 | Config policy and the new-task form | — | `settings/config-policy.yml` (checked by `statecheck`), `settings/new-task-form.yml` | Review policy for config PRs, and the new-task form layout |

## 9. `<data-dir>/state-repo.json`

This file is re-read per request and never exported (`pkg/staterepo/settings.go`).

| Key | Type | Meaning |
| --- | --- | --- |
| `remote` | string | Empty means internal `grain/config` on the controller. A URL means an external config repository (mode 4). |
| `branch` | string | The branch read on the remote |
| `tokenFile` | string | Credential file for the remote |
| `checkImage` | string | The image `grain state check` runs in |
| `directConfigWrites` | bool | Internal mode only. Forces `configRepoOnly: false`, for migration and e2e harnesses. External mode ignores it. |
| `noWorkflow`, `postCheck`, `localOnlyAcknowledged` | bool | Turn off the config repo's own CI workflow, run post-merge checks, and acknowledge a local-only repository with no backup remote |

## 10. Daemon startup flags and environment variables

`grain daemon -flag-names` prints every flag this build accepts. Flags fall into three classes.

`grain settings` (`pkg/cli/settingscmd.go`) edits the stored settings from the command line through the same validation.

**10.1 Seed-only flags.** The help text ends "seeds the stored configuration on first use; ignored once one exists" (`seedOnly` in `pkg/cli/daemon.go`, `loadConfig`). Once a `grain_config` row exists, the stored value wins, and the daemon warns at startup when a flag and the row disagree (`pkg/daemon/config.go`). The flags, by the column each seeds:
- `-poll-interval` → `poll_interval_ms`
- `-max-workers` (former name `-max-concurrent`) → `max_workers`
- `-max-agent-turns`
- `-github-host`, `-github-insecure-http`
- `-gcp-project`, `-gcp-agent-service-account`, `-gcp-agent-account-count`
- `-iap-backend-service`, `-iap-resource`
- `-sandbox-cpus`, `-sandbox-memory-mb`, `-sandbox-disk-gb`
- `-sandbox-cpu-overcommit`, `-sandbox-memory-overcommit`, `-sandbox-disk-overcommit`, `-sandbox-memory-reserve-mb`
- `-sandbox-disk-floor-share`, `-sandbox-disk-floor-min-mb`
- `-sandbox-image-mirror`
- `-package-cache-size-gb`, `-actions-cache-size-gb`
- `-ci-run-history`, `-ci-step-output-mb`, `-ci-step-output-days`, `-ci-run-output-kb`
- `-ci-cache-max-bytes`, `-ci-cache-max-entries`, `-ci-cache-max-age`, `-ci-artifact-max-bytes`, `-ci-artifact-max-run-bytes`, `-ci-artifact-max-entry-bytes`, `-ci-artifact-max-age` (converted to the MiB and day columns)
- `-k8s-namespace`, `-k8s-kubectl`, `-k8s-sandbox-image`, `-k8s-granule-image`, `-k8s-workspace`, `-k8s-git-proxy-host`, `-k8s-sandbox-service-account`, `-k8s-node-selector`, `-k8s-sandbox-privileged`, `-k8s-network-policy`, `-k8s-deployment`, `-k8s-container`, `-k8s-priority-class-{interactive,agent,ci}`
- the backend-selecting booleans `-container-sandboxes`, `-granule-sandboxes`, `-k8s-sandboxes`, `-k8s-granule-sandboxes` and `-host-sandboxes`, which resolve an empty `sandbox_backend`. `-sandbox-backend` itself is a flag of `grain local`, `grain init` and `grain settings`, not of `grain daemon`.

**10.2 Override flags.** These win over a stored setting while given:
- `-sandbox-image`, `-container-sandbox-image` and a granule `-kontur-image` / `-granule-create-arg` pin override `sandbox_image`.
- `-claude-run`, `-codex-run` and `-agy-run` supply the CLI location for a framework with no `cli_run` entry.
- `-default-target-repo` is the repository for a UI-created task that names none.

**10.3 Startup-only flags.** These have no stored equivalent, because the process needs them before the store, secret store or listener opens, or because they name secret material or host plumbing:
- Store and paths: `-data-dir` (required; `$GRAIN_DATA_DIR`), `-db-driver` (`sqlite` or `postgres`; `$GRAIN_DB_DRIVER`), `-db-dsn` (`$GRAIN_DB_DSN`), `-postgres-url` (`$GRAIN_POSTGRES_URL`), `-sandbox-dir`, `-docker-root-dir`, `-grain-state-dir`, `-grain-secrets-dir`, `-grain-src-dir`
- Listeners and sign-in: `-ui-addr` (default `0.0.0.0:4242`), `-ui-allowed-hosts`, `-ui-iap-audience` / `-ui-iap-issuer` / `-ui-iap-keys-url` (with the `$GRAIN_IAP_*` fallbacks), `-ui-access-session-lifetime`, `-ui-open`, `-as`, `-sandbox-port-base` (the git proxy, model proxies and caches bind from here; use `0` inside a sandbox), `-executor-addr` / `-executor-token-file` / `-executor-cert-file` / `-executor-key-file` / `-executor-wait` (each also under a legacy `-model-exec-*` name), `-hostd-socket`, `-hostd-token-file`
- Credentials by file: `-gemini-api-key-file`, `-claude-oauth-token-file`, `-openai-api-key-file`, `-codex-auth-file`. Each is optional, because the same credential can be set from Settings → Models into the secret store.
- Agent CLIs: `-agy-path`, `-claude-path`, `-codex-path`, `-model-backend`, `-mcp-surface`, `-model-proxy-unrestricted`, `-model-proxy-max-concurrent`, `-model-proxy-slot-wait`, `-recorded-tools`, `-tool-budget`
- Sandbox plumbing: the `-granule-*` flags (each also under a legacy `-kontur-*` name), `-container-git-proxy-host`, `-container-sandbox-network`, `-container-sandbox-cap-add`, `-k8s-kubeconfig`, `-k8s-granule-access`, `-k8s-granule-reattach-timeout`, `-sandbox-access-addr` / `-dial` / `-ca-secret`, `-run-host-image`, `-run-host-credential-dir`, `-run-host-dir`, `-run-host-daemon-url`, `-k8s-run-host-rwx`, `-k8s-run-host-service-account`, `-k8s-run-host-volume`, `-sandbox-env`, `-sandbox-env-file`, `-host-sandbox-env`, `-allow-unisolated-host-sandboxes`, `-native-sandboxes` / `-disable-native-sandboxes`, `-repair-reserve-sandboxes`
- CI: `-ci` (default `true`), `-grit-url`, `-grit-client-token`, `-checkpost-url`, `-checkpost-expect`
- Caches: `-package-cache-dir` and its upstream mirror flags, `-actions-cache-dir`
- Upgrade and power: §3.6
- Backups: `-backup-interval`, `-backup-gcs-bucket`, `-backup-google-drive-folder`, `-backup-google-credentials-file`
- Policy: `-policy` (`grain_policy.yml`; see [`docs/local-policy.md`](../../../docs/local-policy.md)), `-local-scripts`, `-local-scripts-env-file`
- Deprecated or removed: `-max-mergers` and `-max-ci-jobs` are ignored. `-cli-image`, `-cli-image-file` and `-cli-container-engine` are ignored. `-tunnel-*` and `-relay-*` refuse to start.

`docs/grain-options.md` records the design options considered for the executor control channel. It is not a flag reference: `-flag-names` and each flag's help text are authoritative.

## 11. Browser-local preferences (This browser → Preferences, `/settings/preferences`)

Component: `PreferencesSection.jsx`. These are stored in `localStorage` and never sent to the daemon. `SettingsPage.jsx` marks the section `NOT_CONFIG`. All of them apply immediately, in this browser only.

| UI label (card) | `localStorage` key | Values |
| --- | --- | --- |
| Appearance (`#appearance`) | `grain.themeMode` | `light`, `dark` or `auto` (follow the OS) |
| Time zone (`#time-zone`) | `grain.timeZone` (migrated from `grain.userTimeZone`) | An IANA zone, or unset to use the deployment's `timeZone` (§4) |
| Task timeline (`#timeline`) | `grain.timelineOrder` | Newest first or oldest first |
| Sidebar (`#pinned`) | `grain.pinned`, `grain.sidebar.hidden` | Pinned items, and hidden sidebar sections |

Other per-browser UI state, not shown in Preferences: `grain.sidebar.collapsed`, `grain.inbox.collapsed`, `grain.inboxNotifications`, `grain.board.columns`, `grain.gantt.view`, `grain.ci.narrowing`, `grain.ci.activeSandboxes.expanded`, `grain.ci.sharedFaults.collapsed`, `grain.system.ciSandboxes.expanded`, `grain.task.draft` (the unsent new-task form), and the per-list filter keys (`storageKey(list)`). `grain.storageVersion` drives `storageMigration.js`.

## 12. Runtime application summary

- **Restart needed** (`ui.restartOnlySettings`, reported in `restartRequired` and in `pendingRestart` while they differ): `sandboxBackend`, `k8sNamespace`, `k8sKubectl`, `k8sSandboxImage`, `k8sGranuleImage`, `k8sWorkspace`, `k8sGitProxyHost`, `k8sSandboxService`, `k8sServiceAccount`, `k8sNodeSelector`, `k8sPrivileged`, `k8sNetworkPolicy`, `k8sDeployment`, `k8sContainer`, `k8sPriorityClassInteractive`, `k8sPriorityClassAgent`, `k8sPriorityClassCi`, `githubHost`, `githubInsecureHttp`.
- **Unrunnable:** `GET /api/settings` also reports `unrunnable`, for stored values this daemon cannot honour (`runnableSettings`), with what it runs instead.
- **Next sandbox or job only:** sandbox shape, `networkAccess`, `sandboxImage`, `packageCacheEnabled`, `actionsCacheEnabled`, `ciArtifactFileMb`, `cliRun`, and the repository setup command.
- **Next task only** (seeded or frozen at creation): `defaultCapabilities` (deployment and repository), `taskBranchTemplate`, `deploymentId`, and the new-task form defaults (`approvedByDefault`, `autoMergeByDefault`, `newestFirst`).
- **Live:** every other `PUT /api/settings` key, as listed in `settingsAppliedLive`. That includes the whole sandbox budget, the CI retention, storage and queue knobs, cache sizes, the GCP and IAP fields, models and connections, prompt extensions, the upgrade policy, `externalAgentApi` and `settingsLocked`.
- **Config repo imports:** a merged `grain/config` PR is applied by `staterepo.Apply` when `HEAD` passes the loaded-commit marker. Each field then takes effect under the same rule as a UI write.

## 13. Sources and coverage checklist

This inventory was enumerated from:
- `pkg/model/config.go`: `Config`, `DefaultConfig`, `NewDeploymentConfig` and the `Default*` constants.
- `pkg/model/store.go` `configColumns`.
- `pkg/model/repo_config.go`.
- `pkg/ui/settings.go`: `UpdateSettingsRequest`, `plannedConfig` and `restartOnlySettings`.
- `pkg/ui/settings_restart_drift_test.go` (`settingsAppliedLive`).
- `pkg/ui/config_bootstrap.go` (`clearBootstrap`).
- `pkg/ui/deployment_routes.go`, `pkg/ui/repos.go` (`SetRepoSettingsRequest`), `pkg/ui/agent_api_tools.go` (the route catalogue).
- `pkg/staterepo/bind.go` (`SettingsTables`), `pkg/staterepo/capabilities_file.go`, `pkg/staterepo/settings.go`.
- `pkg/repodecl/*.go`.
- `pkg/cli/daemon.go` (the flag set).
- `ui/src/settingsSections.js`, `ui/src/components/settings/*.jsx`, `ui/src/components/RepoPage.jsx`, and the `localStorage` keys under `ui/src`.

Every `UpdateSettingsRequest` key, every `configColumns` column, every `RepoConfig` field and every `SettingsTables` table appears above. A field added to any of them must be added here in the same change. The drift test already forces it to be classified as live or restart-only.
