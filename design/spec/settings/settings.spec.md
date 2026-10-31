# Grain settings: field-by-field specification

**Status:** normative inventory. It is derived from the code at the commit it lands in, not from older prose.

This document lists every setting a Grain deployment has. That covers the deployment row (`model.Config` / `grain_config`), the per-repository row (`model.RepoConfig` / `repo_config`), repository-declared settings (`.grain/settings.yml` and the other `.grain/*` files), the composed configuration entities exported to the config repository, the `<data-dir>/state-repo.json` file, the daemon's startup flags and environment variables, and the preferences each browser keeps for itself.

- The one-page architecture this inventory sits under is [`design/design/settings.design.md`](../../design/settings.design.md). Read it first for the six state surfaces, the four configuration modes, and why secrets and sign-in never enter Git.
- The companion matrix, [`settings-mutability.spec.md`](settings-mutability.spec.md), says per mode who may write each surface (UI, REST, agent, config PR). This document does not repeat that matrix. It names each field's surface (bootstrap or config-managed), and the matrix decides what that means in each mode.
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
| Default model (`#model-defaults`) | `defaultModel` (`PUT /api/settings` or `PUT /api/deployment`); `defaultModels` (ordered fallback list, `PUT /api/deployment`) | `DefaultModel` / `default_model` | String. Empty means the first supported model of the configured connections, or else the framework default. | live (per dispatch) |
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

The card also shows the read-only `sandboxCommitment` (what the budget currently admits).

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
| PriorityClass (interactive / agent / CI) | `k8sPriorityClassInteractive`, `k8sPriorityClassAgent`, `k8sPriorityClassCi` | `k8s_priority_class_{interactive,agent,ci}` | `grain-interactive` / `grain-agent` / `grain-ci`. An empty string means **no** priority class, not the default. Must be a valid PriorityClass name. | restart (they are in `restartOnlySettings`) |

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
