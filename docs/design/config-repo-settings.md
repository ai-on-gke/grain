# Config-Repo-Only Settings, Capability Catalogue, and Director Approval

**Status:** Proposed (2026-10-28)
**Task:** [Task #1515](/tasks/1515) (Origin: [Task #1458](/tasks/1458))

---

## 1. Context and Operator Decision

On 2026-10-27 the operator decided to move Grain to a configuration model with eight invariants:

1. **The Settings UI holds only what is needed to bootstrap a deployment:**
   - **Models:** connecting model backends and agent frameworks (`ModelsSection`, `ModelDefaults`, and their credentials).
   - **Sandboxes:** getting sandboxes up—backend (`host` vs `granule`), Kubernetes cluster connection, sandbox images, resource capacity, remote executor tokens, and local CLI run locations.
2. **Everything else is configured through the config repository** (`grain/config` on the controller host, or an external Git mirror configured in `state-repo.json`). Agents and operators reconfigure the deployment by changing files in `grain/config` (`settings/*.yml`, `tables/*.json`, `memory/**`, `scripts/**`, `workflows/**`).
3. **Director sessions can commit to the config repo directly, with inline user approval inside the session.** Today a director session gets a read-only checkout at `/home/debian/grain-state` with push disabled (`remote set-url --push origin grain-read-only` in `pkg/orchestrator/checkout.go`) and must file a separate task to change configuration. Under this design, the director gets a writable checkout, pushes a branch per change, and requests approval inline in the session chat via a new `propose_config_change` tool modeled on `request_secret` and `ask_question`.
4. **The config repo defines every capability that may be used in tasks, together with the restrictions on using it.** A single declarative catalogue (`settings/capabilities.yml`) covers capability servers (`mcp_servers`) and their methods, in-process capabilities, `api:` scopes/presets, and CI capabilities—including which repos, templates, and task/run kinds may hold each capability, whether it is CI-only, whether granting it requires a person, and the default grants per deployment and per repo.
5. **Config repo changes always go through a person by default.** Today `pkg/trustboundary/trustboundary.go` (`ClassifyState`) only holds pull requests touching `StatePrivilegedTables` (`grain_config`, `repo_config`, `api_scope_set`, etc.) and lets `StateBenignTables` (`board*`, `custom_field`, `memory*`, etc.) auto-merge. Under this design, **every** config repo pull request is held for a human **Submit** (`RequiresHuman: true`) by default, with an opt-in exception list declared in `settings/config-policy.yml`—a path that only a person may change.
6. **Secrets never go in the repo or through an agent's context.** Sessions ask the person for a secret through the existing write-only pop-up (`request_secret` → `ui/src/components/detail/PendingSecret.jsx` → `PUT /api/tasks/{id}/secret`), or the operator sets credentials in the Models / Sandboxes bootstrap UI. Secret values go straight into `<data-dir>/secrets` (`pkg/secrets`), and config repo files refer to secrets by name (`$secret:<name>` or `<secret>/<key>`) or credential ID only.
7. **Access stays as it is:** the access code (`GRAIN_ACCESS_CODE` / `AccessSection.jsx`) and Cloud IAP / proxy authentication stay outside the config repo so a bad config PR can never lock the operator out.
8. **The whole New Task form is configurable from the config repo (`settings/new-task-form.yml`)**, and the **first UI step is a config repo browser** (tracked in parallel). Settings sections that leave the UI point to the config repo browser at the relevant file path (`?path=...`) during the transition.

---

## 2. Complete Inventory of Settings UI Sections and API Routes

### 2.1 UI Settings Sections (`ui/src/components/settings/` and Per-Repo / Top-Level Pages)

Every section in `ui/src/settingsSections.js`, component in `ui/src/components/settings/`, per-repository setting in `ui/src/components/RepoPage.jsx`, and configuration surface across the UI falls into one of three dispositions:

- **Stays in UI (Bootstrap / Secret / Access / Per-Browser):** Needed to bring up model connections or sandboxes, store write-only secret material in `<data-dir>/secrets`, sign in, or toggle browser-local display preferences.
- **Config-repo only:** Removed from direct DB mutation in the Settings UI. Until retired, the UI section renders a read-only summary with a deep link to the config repo browser (`/config?path=<file>`) and a hint to ask the Director or open a config PR.
- **Operational action:** Imperative runtime controls (restart, upgrade, pause/resume dispatch, flush cache, sync now, trigger a run) that do not mutate standing configuration. These remain in the UI and API.

| UI Component / Surface | Settings Covered | Disposition | Storage Location (Config Repo File, Table, or Host Store) |
| :--- | :--- | :--- | :--- |
| `ModelsSection.jsx` | Agent backend connections (`id`, `name`, `framework`, `authMode`, `models`, `vertexProject`, `vertexLocation`, `apiEndpoint`, `enabled`, `maxConcurrency`), framework API keys, backend credentials, Codex OAuth (`codex-auth`), GCP service account keys (`gcp-keys`) | **Stays in UI** (Bootstrap + Secrets) | `tables/agent_backend.json` (synced with DB) + `<data-dir>/secrets` (`agent-keys/*`, `agent-backends/*`, `codex-auth`, `gcp-keys/*`) |
| `ModelDefaults.jsx` | Deployment default model rotation (`defaultModel`), director model (`directorModel`), per-model concurrency caps (`modelConcurrency`), per-framework concurrency caps (`frameworkConcurrency`) | **Stays in UI** (Bootstrap under Models) | `tables/grain_config.json` (`default_model`, `director_model`, `model_concurrency`, `framework_concurrency`) |
| `SandboxesSection.jsx` | Sandbox backend (`sandboxBackend`: `host` vs `granule`), Granule cluster (`granuleKubeconfig`, `granuleContext`, `granuleNamespace`), base image (`sandboxBaseImage`), capacity (`maxParallel`), default VM shape (`defaultSandboxCpus`, `defaultSandboxMemoryMb`, `defaultSandboxDiskGb`), default network access (`networkAccess`), idle TTL (`idleSandboxTtlMinutes`), persistent sandboxes list | **Stays in UI** (Bootstrap) + **Operational action** (start/stop/recreate persistent sandboxes) | `tables/grain_config.json` (`sandbox_backend`, `granule_*`, `sandbox_base_image`, `max_parallel`, `default_sandbox_*`, `network_access`, `idle_sandbox_ttl_minutes`) + `<data-dir>/secrets` (`granule-kubeconfig`) |
| `CustomSandboxImageCard.jsx` | Custom sandbox Dockerfile (`sandboxImageDockerfile`), target image ref (`sandboxImageRef`), registry push secret (`sandboxImagePushSecret`), build trigger & status | **Stays in UI** (Bootstrap under Sandboxes) + **Operational action** (trigger image build) | `tables/grain_config.json` (`sandbox_image_dockerfile`, `sandbox_image_ref`, `sandbox_image_push_secret`) + `<data-dir>/secrets` |
| `ExecutorsSection.jsx` | Remote sandbox executor registration tokens (`executor_token` create/revoke/delete) and connected executor status | **Stays in UI** (Bootstrap under Sandboxes) | `<data-dir>/secrets` / `executor_token` (runtime auth table, excluded from config dump) |
| `CLIRunLocations.jsx` | Local checkout directories for `grain run` (`cli-run.json`) | **Stays in UI** (Bootstrap under Sandboxes on local deployments) | Host file `<data-dir>/cli-run.json` |
| `AccessSection.jsx` | Sign-in status (IAP / access code) and **Log out** button | **Stays in UI** (Access) | Host env (`GRAIN_ACCESS_CODE`, IAP headers) + browser session cookie |
| `PreferencesSection.jsx` | Theme (`light`/`dark`/`system`), UI density, notification sound, keyboard shortcuts; plus default backlog insertion order (`newestFirst`) | **Stays in UI** (per-browser `localStorage` in `PreferencesContext.jsx`); `newestFirst` moves to **Config-repo only** (`settings/new-task-form.yml` / `tables/grain_config.json`) | Browser `localStorage` (`grain.preferences.*`); `tables/grain_config.json` (`newest_first`) |
| `ConfigRepoSection.jsx` + `StateRepoPanel.jsx` | External Git mirror URL (`remote`), branch (`branch`), token (`state-repo/token`), check image (`checkImage`), **Sync now**, and embedded `StateRepoBrowser.jsx` | **Stays in UI** (Bootstrap for attaching an external mirror + **Operational action** for Sync now); file browsing moves to dedicated Config Repo Browser | Host file `<data-dir>/state-repo.json` + `<data-dir>/secrets` (`state-repo/token`) |
| `UpgradeSection.jsx` | Controller binary/image upgrade (`POST /api/upgrade`), release notes viewer, database engine migration to Postgres, and host power controls (**Restart daemon**, **Reboot host**, **Shut down host**) | **Operational action** (Stays in UI header / system menu or operational tab) | Runtime systemd / container lifecycle; `<data-dir>/db-config.json` |
| `GeneralSection.jsx` | Deployment title (`title`), accent color (`color`), favicon (`favicon`), intake/triage settings | **Config-repo only** | `tables/grain_config.json` (`title`, `color`, `favicon`) |
| `GitSection.jsx` (`GitConnections`) | GitHub credentials (`git_credential`: GitHub App or PAT), default Git credential (`gitCredentialId`), git author identity (`gitAuthorName`, `gitAuthorEmail`). **Note:** Secret token/PEM values remain write-only via secret store. | **Config-repo only** for credential metadata & bindings; **Stays in UI / `request_secret`** for write-only secret material (`github-tokens/*`, `git-credentials/*`) | `tables/git_credential.json`, `tables/grain_config.json` (`git_credential_id`) + `<data-dir>/secrets` |
| `CredentialsSection.jsx` | Overview of stored secrets (`GET /api/secrets`), setting/replacing secret keys (`PUT /api/secrets/{secret}/{key}`), secret audit log (`GET /api/deployment/secret-audit`) | **Stays in UI** (Write-only secret entry + audit viewer under Models/Sandboxes or Secrets modal) | `<data-dir>/secrets` (`pkg/secrets` encrypted store) |
| `RepositoriesSection.jsx` | Attached target repositories (`targetRepos`), per-repo Git connection binding (`repo_git_credential`), default branch, work ownership | **Config-repo only** | `tables/grain_config.json` (`target_repos`), `tables/repo_config.json`, `tables/repo_git_credential.json` |
| `TasksSection.jsx` | Auto-merge by default (`autoMergeByDefault`), approved by default (`approvedByDefault`), default capabilities (`defaultCapabilities` via `GrantsPicker`), custom task fields (`custom_field`), branch naming, port rules | **Config-repo only** | `settings/new-task-form.yml`, `settings/capabilities.yml`, `tables/grain_config.json`, `tables/custom_field.json` |
| `AgentsSection.jsx` | Standing console/director sessions (`console_sessions`), deployment prompt extension (`promptExtension`), swarm/subagent settings | **Config-repo only** | `tables/grain_config.json` (`console_sessions`, `prompt_extension`), `tables/console_session.json` |
| `CapabilitiesSection.jsx` | Capability servers (`mcp_servers`), capability policy (`capability_policy`), API scope presets (`api_scope_set`), in-process capability toggles. (Secret bindings still store values in `<data-dir>/secrets`.) | **Config-repo only** (secret values set via `request_secret` or secret prompt) | `settings/capabilities.yml` (and synced `tables/grain_config.json`, `tables/api_scope_set.json`) |
| `CISection.jsx` | CI concurrency, CI mode defaults, flake thresholds, merge queue batch settings | **Config-repo only** | `tables/grain_config.json`, `tables/repo_config.json` |
| `CachesSection.jsx` | CI job cache, Go/npm package cache, and GitHub Actions cache inspection + **Flush cache** buttons | **Operational action** (Inspection + Flush stay as operational actions; cache retention/size config is config-repo only) | Runtime cache directories; config in `tables/grain_config.json` |
| `EngineSection.jsx` | Dispatch pause/resume (`DELETE /api/pause`, `POST /api/pipeline-pause`), merge queue hold, orchestrator timeouts and retry limits | **Operational action** for Pause/Resume; **Config-repo only** for timeouts/retry limits | Runtime pause state in `tables/grain_config.json` (operational); limits in `tables/grain_config.json` |
| `MemorySection.jsx` + `MemoryPage.jsx` | Deployment, team, and repository standing memory (`memory/*.md`) and memory documents (`memory/documents/**/*.md`) | **Config-repo only** | `memory/deployment.md`, `memory/repos/<owner>/<name>.md`, `memory/documents/**/*.md` (`tables/memory.json`, `tables/memory_document.json`) |
| `RepoPage.jsx` (Per-Repo Settings tabs) | Per-repo default capabilities (`capabilities`), CI capabilities (`ci-capabilities`), prompt extension, setup script (`setup-command`), declarations, `merge-on-green`, `require-branch-up-to-date`, `base-repair-bypasses-human-gates`, `ignore-github-ci`, `ci-mode`, `watch-default-branch`, `unfixable-checks`, `.grain/workflows` overrides, release & qualification config | **Config-repo only** (Operational triggers like "Run base branch CI", "Refresh lifecycle", "Promote candidate" stay as **Operational actions**) | `settings/capabilities.yml`, `tables/repo_config.json`, `scripts/repos/<owner>/<name>/setup.sh`, `workflows/<owner>/<name>/*.yml`, `tables/repo_workflow.json`, `tables/repo_declaration.json`, `tables/repo_qualification_plan.json` |
| `TemplatesPage.jsx`, `SchedulesPage.jsx`, `SuitesPage.jsx`, `BoardsSettingsDialog.jsx` | Task templates, cron schedules, benchmark/test suites, and custom kanban boards | **Config-repo only** ("Run schedule now" and "Start suite run" stay as **Operational actions**) | `settings/templates.yml`, `settings/schedules.yml`, `settings/suites.yml`, `tables/board.json`, `tables/board_column.json` |

### 2.2 Settings-Writing and Operational API Routes (`pkg/ui/route_scopes.go`)

Every mutating HTTP route in `pkg/ui/` that touches deployment configuration, secrets, or runtime state is classified below:

| HTTP Route(s) | Scope | Disposition | Behavior & Config Repo Target |
| :--- | :--- | :--- | :--- |
| `PUT /api/deployment/agent-backends/{backendId}`<br>`DELETE /api/deployment/agent-backends/{backendId}` | `connections:write` | **Stays in UI** (Bootstrap: Models) | Writes `agent_backend` in DB and triggers state-repo export to `tables/agent_backend.json`. |
| `PUT /api/agent-keys/{framework}`<br>`DELETE /api/agent-keys/{framework}`<br>`PUT /api/agent-backends/{id}/credential`<br>`DELETE /api/agent-backends/{id}/credential`<br>`PUT /api/agent-backends/{id}/codex-auth`<br>`DELETE /api/agent-backends/{id}/codex-auth`<br>`PUT /api/codex-auth`<br>`DELETE /api/codex-auth`<br>`POST /api/gcp-keys`<br>`PUT /api/gcp-keys/{name}`<br>`DELETE /api/gcp-keys/{name}` | `secrets:write` | **Stays in UI** (Bootstrap: Models & Secrets) | Writes directly to `<data-dir>/secrets` (`pkg/secrets`). Never touches the config repo. |
| `PUT /api/github-tokens/{name}`<br>`DELETE /api/github-tokens/{name}`<br>`PUT /api/secrets/{secret}/{key}`<br>`DELETE /api/secrets/{secret}/{key}`<br>`DELETE /api/secrets/{secret}`<br>`PUT /api/repos/{owner}/{name}/release-secrets/{secret}`<br>`DELETE /api/repos/{owner}/{name}/release-secrets/{secret}`<br>`POST /api/capability-servers/bind-secret`<br>`POST /api/capability-servers/rotate-managed` | `secrets:write` / `capabilities:admin` | **Stays in UI** (Write-only secret store) | Stores or deletes secret material in `<data-dir>/secrets`. Never writes plaintext secrets to `grain/config`. |
| `PUT /api/tasks/{id}/secret` | `secrets:write` | **Stays in UI** (`PendingSecret.jsx`) | Stores the secret requested by `request_secret` in `<data-dir>/secrets` and unparks the task (`pkg/ui/task_secret.go`). |
| `POST /api/executors/tokens`<br>`POST /api/executors/tokens/{id}/revoke`<br>`DELETE /api/executors/tokens/{id}`<br>`DELETE /api/executors/tokens/{id}/revoke` | `secrets:write` | **Stays in UI** (Bootstrap: Sandboxes) | Manages executor bearer tokens in `executor_token`. |
| `PUT /api/settings/cli-run` | `settings:write` | **Stays in UI** (Bootstrap: Sandboxes) | Writes host-local `<data-dir>/cli-run.json`. |
| `POST /api/auth/access-code`<br>`POST /api/auth/logout`<br>`GET /api/auth/logout` | `internal` | **Stays in UI** (Access) | Manages browser session authentication cookies. |
| `POST /api/state-repo/remote`<br>`POST /api/state-repo/secrets-key`<br>`POST /api/state-repo/import-key`<br>`POST /api/state-repo/secrets-repo` | `settings:write` / `secrets:write` | **Stays in UI** (Bootstrap: Config Repo & Secrets key) | Writes host-local `<data-dir>/state-repo.json` and `<data-dir>/secrets` encryption key. |
| `PUT /api/settings`<br>`POST /api/settings`<br>`PUT /api/deployment` | `settings:write` | **Split: Bootstrap fields stay in UI; all other fields Config-repo only** | Requests mutating **only** bootstrap columns (`default_model`, `director_model`, `model_concurrency`, `framework_concurrency`, `sandbox_backend`, `granule_*`, `sandbox_base_image`, `sandbox_image_*`, `max_parallel`, `default_sandbox_*`, `network_access`, `idle_sandbox_ttl_minutes`) write to DB and export to `tables/grain_config.json`. Requests mutating any other `grain_config` field (`title`, `color`, `target_repos`, `default_capabilities`, `prompt_extension`, `auto_merge_by_default`, `approved_by_default`, `console_sessions`, etc.) are refused via `configWriteRehearsal` with `409 Conflict` and a patch against `tables/grain_config.json` (or `settings/*.yml`). |
| `POST /api/deployment/git-connections`<br>`DELETE /api/deployment/git-connections/{cred}`<br>`PUT /api/deployment/repos/{owner}/{name}/git-connection` | `connections:write` | **Config-repo only** (except secret material in `<data-dir>/secrets`) | Refused with `409 Conflict` returning patch to `tables/git_credential.json` / `tables/repo_git_credential.json`. (Any accompanying token/PEM is still written only via `/api/secrets`.) |
| `POST /api/repos`<br>`DELETE /api/repos/{owner}/{name}`<br>`PUT /api/deployment/repos/{owner}/{name}/config`<br>`PUT /api/repos/{owner}/{name}/settings`<br>`PUT /api/repos/{owner}/{name}/work-ownership`<br>`PUT /api/repos/{owner}/{name}/prompt-extension`<br>`PUT /api/repos/{owner}/{name}/setup-command`<br>`PUT /api/repos/{owner}/{name}/declarations`<br>`PUT /api/repos/{owner}/{name}/require-branch-up-to-date`<br>`PUT /api/repos/{owner}/{name}/merge-on-green`<br>`PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates`<br>`PUT /api/repos/{owner}/{name}/ignore-github-ci`<br>`PUT /api/repos/{owner}/{name}/ci-mode`<br>`PUT /api/repos/{owner}/{name}/watch-default-branch`<br>`PUT /api/repos/{owner}/{name}/unfixable-checks`<br>`PUT /api/repos/{owner}/{name}/workflows`<br>`PUT /api/repos/{owner}/{name}/release`<br>`PUT /api/repos/{owner}/{name}/qualification-plan` | `repos:write` / `releases:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `tables/repo_config.json`, `tables/grain_config.json`, `scripts/repos/<owner>/<name>/setup.sh`, `workflows/<owner>/<name>/*.yml`, `tables/repo_declaration.json`, or `tables/repo_qualification_plan.json`. |
| `PUT /api/repos/{owner}/{name}/capabilities`<br>`PUT /api/repos/{owner}/{name}/ci-capabilities`<br>`PUT /api/capability-servers`<br>`POST /api/capability-servers/remove`<br>`POST /api/capability-servers/enable`<br>`POST /api/capability-servers/disable`<br>`PUT /api/capability-policy`<br>`PUT /api/api-scope-sets`<br>`POST /api/api-scope-sets/delete` | `capabilities:admin` | **Config-repo only** | Refused with `409 Conflict` returning patch to `settings/capabilities.yml` (and synced `tables/grain_config.json`, `tables/repo_config.json`, `tables/api_scope_set.json`). |
| `POST /api/templates`<br>`PATCH /api/templates/{id}`<br>`DELETE /api/templates/{id}` | `templates:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `settings/templates.yml`. |
| `POST /api/schedules`<br>`PATCH /api/schedules/{id}`<br>`DELETE /api/schedules/{id}` | `schedules:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `settings/schedules.yml`. |
| `POST /api/suites`<br>`PATCH /api/suites/{id}`<br>`DELETE /api/suites/{id}` | `suites:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `settings/suites.yml`. |
| `POST /api/boards`<br>`PATCH /api/boards/{id}`<br>`DELETE /api/boards/{id}` | `boards:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `tables/board.json` and `tables/board_column.json`. |
| `PUT /api/memory`<br>`PUT /api/memory/documents`<br>`PUT /api/release-notes` | `memory:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `memory/**` or `release-notes/**`. |
| `POST /api/console-sessions`<br>`PUT /api/console-sessions/{id}`<br>`DELETE /api/console-sessions/{id}` | `sessions:write` | **Config-repo only** | Refused with `409 Conflict` returning patch to `tables/grain_config.json` (`console_sessions`) / `tables/console_session.json`. |
| `POST /api/state-repo/sync`<br>`POST /api/state-repo/reset-notice`<br>`POST /api/host/reboot`<br>`POST /api/host/shutdown`<br>`POST /api/host/restart`<br>`POST /api/upgrade`<br>`DELETE /api/upgrade/scheduled`<br>`POST /api/database/test`<br>`POST /api/database/upgrade`<br>`POST /api/upgrade/postgres`<br>`POST /api/settings/db/migrate-to-postgres`<br>`POST /api/persistent-sandboxes/{name}/{action}`<br>`DELETE /api/pause`<br>`POST /api/pipeline-pause`<br>`DELETE /api/pipeline-pause`<br>`DELETE /api/ci/cache`<br>`POST /api/package-cache/flush`<br>`POST /api/actions-cache/flush`<br>`POST /api/schedules/{id}/run`<br>`POST /api/suite-runs`<br>`POST /api/console-sessions/{id}/restart`<br>`POST /api/console-sessions/{id}/cancel`<br>`POST /api/repos/{owner}/{name}/release-runs`<br>`POST /api/repos/{owner}/{name}/base-branch-runs`<br>`POST /api/repos/{owner}/{name}/lifecycle/refresh`<br>`POST /api/capability-servers/test`<br>`POST /api/capability-servers/test-script`<br>`POST /api/capability-servers/gchat-test`<br>`POST /api/deployment/git-connections/check`<br>`POST /api/agent-keys/{framework}/check`<br>`POST /api/capabilities/{id}/check` | Various operational scopes | **Operational action** (Stays in UI / API) | Executes the runtime operation directly without mutating declarative config files. |

### 2.3 How the Config Repo Browser Fits In

A separate, parallel task is building a first-class Config Repo Browser (evolving `ui/src/components/StateRepoBrowser.jsx` into a dedicated view addressable at `/config?path=<filepath>`).

1. **Navigation shrink in `ui/src/settingsSections.js`:**
   - The Settings sidebar shrinks to **Models**, **Sandboxes**, **Credentials / Secrets**, **Access**, **Preferences**, and **System / Operations** (Upgrade, Power, Caches, Engine pause, Config repo mirror status).
   - Departing sections (**General**, **Git**, **Repositories**, **Tasks**, **Agents**, **Capabilities**, **CI**, **Memory**) are replaced during transition by compact read-only cards that display the current active values and a **"View in config repo → `settings/capabilities.yml`"** link opening `/config?path=settings/capabilities.yml` (or the relevant `tables/*.json` / `settings/*.yml` file), plus a **"Ask Director to change…"** shortcut that pre-fills a message in the Director chat.
2. **In-place refusal banner (`ui/src/components/ConfigRefusal.jsx`):**
   - Any legacy UI control or API caller that hits a `409 Conflict` (`config_managed_externally`) already renders `ConfigRefusal.jsx` with the exact file path and patch. We add a **"Open in Config Browser"** link and a **"Create Config PR"** button to `ConfigRefusal.jsx`.

---

## 3. The Capability Catalogue in the Config Repo

### 3.1 One Declarative File: `settings/capabilities.yml`

Today, capability definitions and grant rules are split across four places:
- `grain_config.mcp_servers` (JSON array in `tables/grain_config.json`) for capability servers and their methods;
- `grain_config.capability_policy` (JSON object in `tables/grain_config.json`, `pkg/model/capability_ref.go`) for allow/deny rules on `<server>#<method>`;
- `tables/api_scope_set.json` (`pkg/model/api_scope_set.go`) for named `api:<preset>` bundles;
- `grain_config.default_capabilities`, `repo_config.default_capabilities`, and `repo_config.ci_capabilities` for default task and CI grants;
- Hardcoded Go registries (`pkg/daemon/capabilities.go` and `pkg/daemon/ci_policy.go`) for in-process capabilities (`gcp-key`, `github-token`, `k8s-kubeconfig`, `self-debug`, `corp-research`, `grain-state-check`, etc.).

We unify these into a single human-readable YAML projection in the config repo, **`settings/capabilities.yml`**, managed by `pkg/staterepo` alongside `settings/templates.yml`, `settings/schedules.yml`, and `settings/suites.yml` (`pkg/staterepo/settings_files.go`).

### 3.2 Schema of `settings/capabilities.yml`

```yaml
# settings/capabilities.yml
# Declarative catalogue of every capability usable in tasks, sessions, and CI.

# 1. Default grants per deployment and per repository.
# Replaces grain_config.default_capabilities and repo_config.{default_capabilities,ci_capabilities}.
defaults:
  # Granted to every task when the caller does not specify an explicit capability list
  # (and pre-selected in the New Task form unless overridden by settings/new-task-form.yml).
  task:
    - corp-research
    - api:task-reader
  # Granted to grain CI workflow runs (.grain/workflows/*.yml) by default.
  ci: []
  # Per-repository additive defaults (merged with deployment defaults).
  repos:
    ai-on-gke/grain:
      task:
        - self-debug
        - https://capabilities.internal/gcp#gcp-key:
            with:
              project: grain-dev
      ci:
        - gcp-key

# 2. Capability servers (MCP / HTTP / script servers) and per-method rules.
servers:
  - id: gcp
    name: GCP Credentials
    url: https://capabilities.internal/gcp
    enabled: true
    # Secret bindings refer to <data-dir>/secrets by name only -- never secret values.
    secrets:
      service_account: $secret:gcp-keys/grain-deployer
    methods:
      - name: gcp-key
        description: Mint a short-lived GCP access token for a project.
        # Parameter schema / allowed argument values
        args:
          project:
            allowed: ["grain-dev", "grain-staging", "grain-prod"]
        restrictions:
          repos: ["ai-on-gke/*", "bwsalmon/*"]
          templates: ["*"]
          kinds: ["task", "ci", "session"]   # task | session | director | ci | process | review
          ci_only: false
          requires_person: false
          # Per-argument elevation: requesting grain-prod requires human approval.
          requires_person_when:
            project: ["grain-prod"]

# 3. In-process capabilities (built into the controller).
in_process:
  corp-research:
    enabled: true
    restrictions:
      repos: ["*"]
      templates: ["*"]
      kinds: ["task", "session", "director", "review"]
      ci_only: false
      requires_person: false
  self-debug:
    enabled: true
    restrictions:
      repos: ["ai-on-gke/grain", "bwsalmon/grain"]
      templates: ["*"]
      kinds: ["task", "session", "director"]
      ci_only: false
      requires_person: false
  gcp-key:
    enabled: true
    secret: $secret:gcp-keys/default
    restrictions:
      repos: ["ai-on-gke/grain"]
      templates: ["release-*"]
      kinds: ["ci"]
      ci_only: true
      requires_person: true
  k8s-kubeconfig:
    enabled: false
    restrictions:
      repos: []
      kinds: ["ci"]
      ci_only: true
      requires_person: true

# 4. Agent API scope presets (api:<name>) and raw scope restrictions.
api_scopes:
  presets:
    task-reader:
      description: Read-only access to tasks, CI runs, and logs.
      scopes: ["tasks:read", "ci:read", "deployment:read"]
      restrictions:
        repos: ["*"]
        templates: ["*"]
        kinds: ["task", "session", "director", "review"]
        ci_only: false
        requires_person: false
    release-coord:
      description: Coordinate releases and merge batches.
      scopes: ["tasks:read", "tasks:write", "releases:read", "releases:write", "ci:read"]
      restrictions:
        repos: ["ai-on-gke/grain"]
        templates: ["release-coordinator"]
        kinds: ["task", "session"]
        ci_only: false
        requires_person: true
  # Restrictions on raw api:<area>:<verb> grants not bundled in a preset.
  raw_scopes:
    "*:read":
      repos: ["*"]
      kinds: ["task", "session", "director", "review"]
      requires_person: false
    "*:write":
      repos: ["*"]
      kinds: ["task", "session", "director"]
      requires_person: true
    "*:admin":
      repos: ["ai-on-gke/grain"]
      kinds: ["director"]
      requires_person: true
    "secrets:*":
      # Never grantable to tasks or CI; only human or director with person approval.
      repos: []
      kinds: []
      requires_person: true
```

### 3.3 Restriction Semantics

Every capability entry in `settings/capabilities.yml` carries a uniform `restrictions` block (`model.CapabilityRestrictions`):

1. **`repos` (`[]string`, glob patterns):** Which target repositories (`owner/name`, `owner/*`, `*`, or `"none"` for no-repo tasks) may hold the capability. Empty list `[]` means no repository may hold it.
2. **`templates` (`[]string`, glob patterns):** Which template IDs or slugs may grant the capability. `["*"]` (the default when omitted) allows ad-hoc tasks as well as any template; a specific list like `["release-*"]` restricts the capability to tasks instantiated from matching templates.
3. **`kinds` (`[]string`):** Which execution kinds may hold the capability:
   - `task`: standard code/investigation task runs;
   - `session`: standing interactive chat sessions;
   - `director`: the deployment director session;
   - `review`: pre-merge code review runs spawned via `reviewTemplateId`;
   - `process`: capability server process sandboxes;
   - `ci`: Grain CI workflow jobs (`.grain/workflows/*.yml`).
4. **`ci_only` (`bool`):** When `true`, `kinds` must be `["ci"]`. The capability cannot be attached to any task, template, schedule, or session (`pkg/daemon/ci_policy.go`), and is hidden from task capability pickers.
5. **`requires_person` (`bool`, "dangerous"):** Whether granting this capability requires an explicit human decision:
   - A task filed by a human in the UI may include a `requires_person: true` capability only when the human files or approves the task.
   - A task proposed by an agent via `propose_task` that requests a `requires_person: true` capability (or a `requires_person_when` argument match) is **always** filed unapproved (`proposed`), even if the parent task has `auto_merge: true` or standing session queue permissions.
   - An agent holding `api:capabilities:admin` cannot attach a `requires_person: true` capability to an existing task without parking the task for human approval.
6. **`defaults` (`defaults.task`, `defaults.ci`, `defaults.repos[repo]`):**
   - Replaces `grain_config.default_capabilities`, `repo_config.default_capabilities`, and `repo_config.ci_capabilities`.
   - During `staterepo.ApplySettings` / `ExportConfig`, `settings/capabilities.yml` is the single source of truth; the legacy columns in `grain_config` and `repo_config` are populated from `settings/capabilities.yml` on import so existing Go readers continue to work seamlessly during migration, and omitted from `tables/grain_config.json` and `tables/repo_config.json` on export (matching how `settings/templates.yml` suppresses `tables/task_template.json`).

### 3.4 Validation Lifecycle: Filing, Edit, and Dispatch

A single evaluator, `catalogue.ValidateGrants(ctx, GrantContext{Repo, TemplateID, Kind, Actor, Grants})` in `pkg/model/capability_catalogue.go`, enforces the catalogue at three points:

1. **At Task Filing (`POST /api/tasks`, `propose_task`, schedule firing, template instantiation):**
   - Every grant in `req.Capabilities` is canonicalized (`mcp_server#method`, in-process ID, or `api:...`).
   - Unknown capabilities, disabled servers/capabilities, `ci_only: true` capabilities on non-CI runs, `kinds` mismatches, `repos` mismatches, `templates` mismatches, and disallowed `with:` arguments are **refused immediately with `400 Bad Request`** (or dropped with a note on `propose_task` if inherited from defaults that don't apply to the target repo).
   - If any requested grant has `requires_person: true` (or matches `requires_person_when`) and the caller is an agent (`propose_task` or agent bearer token), the task's `Approved` flag is forced to `false` (`State: proposed`) so a human must approve it in the UI before dispatch.
2. **At Task Edit (`POST /api/tasks/{id}/capabilities`, `PATCH /api/tasks/{id}`):**
   - Runs `catalogue.ValidateGrants` against the task's current `Repo`, `TemplateID`, and `Kind`.
   - If an agent bearer token attempts to add a capability with `requires_person: true`, the request is refused with `403 Forbidden` (`capability <name> requires human approval`).
3. **At Dispatch (`pkg/orchestrator` and `pkg/daemon/capabilities.go` / `pkg/daemon/ci_policy.go`):**
   - Before minting credentials or starting a sandbox (and before a CI workflow job receives `defaults.ci` / repo `ci` grants), the orchestrator re-runs `catalogue.ValidateGrants`.
   - If a config change merged after the task was filed has revoked a capability or tightened its `repos`/`kinds` rule, dispatch refuses to start the run and parks the task with a clear message naming the disallowed capability, rather than handing the sandbox a capability the catalogue now forbids.

### 3.4.1 As Implemented (Slice 2)

- **Storage.** The restrictions have no column of their own: they are kept inside `grain_config.capability_policy` as `CapabilityPolicy.Catalogue` (`pkg/model/capability_catalogue.go`), so adopting the file needs no migration. A deployment whose policy carries no catalogue exports no `settings/capabilities.yml` and is judged exactly as before.
- **Projection.** `pkg/staterepo/capabilities_file.go` overlays the rows `dumpRowsOf` returns for `grain_config`, `repo_config` and `api_scope_set` with what the file says, so every reader of a dump (Import, Check, resets, rehearsal) sees the file's values. A top-level section the file leaves out leaves its columns as `tables/` has them. The legacy columns are still written to `tables/` on export (they agree with the file, both being rendered from the database); where they disagree in a pull request, the file wins.
- **Scope of judgement.** The catalogue restricts what it names. A capability it does not list is left to the existing checks (registry, policy rules, local policy); a server that lists methods refuses a method it does not list. In-process entries match a grant by its full name or by the family before `:` (`gcp-key` covers `gcp-key:prod`). Presets match `api:<name>[@repo]`; raw scope rules match `api:<area>:<verb>` by glob, and every matching rule applies.
- **Filing.** `POST /api/tasks` and `propose_task` (`ui.Client.CreateTask`): an explicitly requested grant the catalogue refuses is a `400`; a refused *default* grant is dropped. A `requires_person` grant on a proposal or on an agent's own filing files the task `proposed` with a note naming the grants, whatever permission it would otherwise inherit.
- **Edit.** `POST /api/tasks/{id}/capabilities` is judged against the task's repo, template and kind (`400` on refusal, `403` for an agent attaching a `requires_person` grant); `PATCH /api/tasks/{id}` moving a task to another repository re-judges every grant it holds.
- **Dispatch.** `orchestrator.catalogueHold` re-judges every queued task each cycle before admission and holds a refused one under the `capability-catalogue` grant wait, naming the grant; grain CI jobs are judged as kind `ci` in `CiGrantReservations.Acquire` before anything is minted (`daemon.CheckCIJobCatalogue`), and `defaults.ci` joins every repository's CI capabilities.
- **Check.** `staterepo.Check` refuses a file that does not parse (unknown keys included), is inconsistent with itself (bad kinds, `ci_only` with other kinds, secrets written as values, a default its own restrictions refuse), or refuses a grant some template or schedule in the dump carries.

### 3.5 Relationship to Existing Tables and Trust Boundary

- `settings/capabilities.yml` projects onto `grain_config.mcp_servers`, `grain_config.capability_policy`, `grain_config.default_capabilities`, `api_scope_set`, and `repo_config.{default_capabilities,ci_capabilities}`.
- In `pkg/trustboundary/trustboundary.go`, `settings/capabilities.yml` is in `NeverAutoMergePaths` (see §5): any pull request touching `settings/capabilities.yml` is unconditionally `RequiresHuman: true`.

---

## 4. The Write Path and Director In-Session Approval

### 4.1 UI and API Write Path: `409 Conflict` Rehearsal and Direct Config PRs

Today, `pkg/ui/config_refusal.go` (`checkSettingsWritable` and `checkSettingsWritableUnrehearsed`) only intercepts settings writes when `externalConfigRepo(ctx)` returns a non-empty external Git remote (`state-repo.json`).

Under this design:
1. **Always-on rehearsal for non-bootstrap settings:**
   - `checkSettingsWritable(ctx, tables...)` is armed on **every** deployment (both internal `grain/config` and external config repo) for all config-repo-only routes listed in §2.2.
   - When called, `configWriteRehearsal` runs the store mutation inside a transaction, calls `staterepo.Rehearse(ctx, tx, write)` (`pkg/staterepo/rehearse.go`), rolls back `tx`, and returns `409 Conflict`:
     ```json
     {
       "error": "this setting is managed in grain/config: commit the change below to settings/capabilities.yml",
       "code": "config_managed_externally",
       "remote": "grain/config",
       "branch": "main",
       "path": "settings/capabilities.yml",
       "changes": [
         {
           "path": "settings/capabilities.yml",
           "op": "modify",
           "patch": "diff --git a/settings/capabilities.yml b/settings/capabilities.yml\n...",
           "fragment": "..."
         }
       ]
     }
     ```
2. **Optional one-click Config PR creation (`POST /api/state-repo/pull-requests`):**
   - For any `FileChange` list returned by a `409` rehearsal (or edited in the Config Repo Browser), a human operator can POST `{ title, summary, changes }` to `POST /api/state-repo/pull-requests`, which applies `changes` onto a new branch in `grain/config`, runs the validation pipeline (§4.4), and opens a config PR ready for review and Submit.

### 4.2 Writable Config Repo Checkout for the Director

Today in `pkg/orchestrator/checkout.go`:
- `prepareDirectorState` runs only when `id == model.DirectorSessionID && task.Target == nil`, checks `task.Reads` for `grain/config`, clones `http://127.0.0.1:9728/grain/config.git` into `/home/debian/grain-state`, and disables push via `git -C 'grain-state' remote set-url --push origin grain-read-only`.
- Meanwhile, `grain_config.console_sessions` already declares `{"Owner": "grain", "Name": "config"}` in `writes` for `director`, and `pkg/gitproxy/local.go` already serves `grain/config.git` over `git http-backend` with `receiveHardening` (`receive.denyCurrentBranch=refuse`, `receive.fsckObjects=true`).

We update the checkout and gitproxy layers as follows:

1. **`pkg/orchestrator/checkout.go` (`prepareDirectorState` and `directorStateSection`):**
   - If a session or task has `grain/config` in `task.Writes` (as `director` does by default), clone `grain/config` into `/home/debian/grain-state` **with push enabled** (do not set `pushurl` to `grain-read-only`), and configure the sandbox's git author identity on the clone.
   - If `grain/config` is only in `task.Reads`, keep `remote set-url --push origin grain-read-only`.
   - Update `directorStateSection` in `pkg/orchestrator/checkout.go` so the director's system prompt explains the workflow:
     - Edit files in `/home/debian/grain-state` (`settings/*.yml`, `tables/*.json`, `memory/**`, etc.);
     - Validate locally with `grain state check /home/debian/grain-state`;
     - Call `propose_config_change` to commit, push a branch, validate, and show the inline approval card to the user in the session chat.
2. **`pkg/gitproxy/authorize.go` and `pkg/gitproxy/refupdates.go`:**
   - A task or standing session with `grain/config` in `Writes` is authorized to push branches under `refs/heads/grain/config/<session-id>/*` (for standing sessions, which may propose multiple config changes over their lifetime) as well as its own task branch `refs/heads/grain/<run>/<task>`.
   - Direct pushes to `refs/heads/main` (or the configured default branch) and force-pushes/deletions of other tasks' branches remain hard-refused by `gitproxy/refupdates.go`.

### 4.3 In-Session Inline Approval: `propose_config_change` and `PendingConfigChange.jsx`

To make director config changes fast, safe, and reviewable without leaving the conversation, we model config change approval directly on `request_secret` (`ui/src/components/detail/PendingSecret.jsx`, `pkg/ui/task_secret.go`) and `ask_question` (`ui/src/components/detail/PendingQuestion.jsx`).

#### Step 1: The `propose_config_change` MCP Tool (`pkg/mcpserver`)

```json
{
  "name": "propose_config_change",
  "description": "Commit and push your changes in /home/debian/grain-state to a branch in grain/config, run config validation, and show an inline diff with Approve / Reject buttons in this session's chat. This ends your turn and waits for the person's decision.",
  "parameters": {
    "summary": "One-line summary (max 100 chars) of what this configuration change does and why.",
    "description": "Optional markdown explanation of the change and any trade-offs.",
    "branch": "Optional branch suffix (defaults to an auto-generated slug from summary)."
  }
}
```

When the director calls `propose_config_change`:
1. The sandbox helper (or controller handler) checks `/home/debian/grain-state`:
   - If there are uncommitted working-tree changes on `main` or the current branch, it creates/switches to `grain/config/<session-id>/<slug>`, stages and commits them with `summary` + `description`, and pushes `HEAD` to `origin refs/heads/grain/config/<session-id>/<slug>`.
   - Records the exact `headSHA` and the base commit `baseSHA` (`origin/main`).
2. The controller opens (or updates) a held pull request in `StateRepoPullRequestClient` (`pkg/daemon/statepullrequests.go`) for the branch and runs the pre-review validation suite (§4.4).
3. The controller stores `PendingConfigChange` on the task/session observation (`model.Observation.PendingConfigChange`, serialized as JSON in `task_observation.pending_config_change`):
   ```go
   type PendingConfigChange struct {
       Branch      string               `json:"branch"`
       BaseSHA     string               `json:"baseSha"`
       HeadSHA     string               `json:"headSha"`
       PRNumber    int                  `json:"prNumber,omitempty"`
       Summary     string               `json:"summary"`
       Description string               `json:"description,omitempty"`
       Files       []string             `json:"files"`
       Diff        string               `json:"diff"`
       Validation  ConfigCheckSummary   `json:"validation"`
       CreatedAt   time.Time            `json:"createdAt"`
   }
   ```
4. The session turn ends and parks on the pending config change (just as `request_secret` sets `obs.PendingSecret` and parks).

#### Step 2: Inline Approval Card (`ui/src/components/detail/PendingConfigChange.jsx`)

In both `TaskChat.jsx` (used by the Director and interactive sessions) and `DetailOverlay.jsx` (used by standard tasks), when `t.pendingConfigChange` is non-null, `PendingConfigChange.jsx` renders inline at the bottom of the conversation timeline:

- **Header & Summary:** Displays `"Config change waiting for your approval"`, the director's one-line `summary`, optional `description`, branch name, and short `headSha`.
- **Files Touched & Links:** Chips for each file in `pending.files` (linking to `/config?path=<file>`).
- **Validation Report Banner:**
  - Green badge when `validation.ok` is true (`"Schema valid · Capability catalogue consistent · Dispatch liveness OK · Dry-run apply succeeded"`), plus any non-blocking warnings (e.g. `"Secret $secret:gcp-keys/new-sa is not set yet"`).
  - Red error alert when `validation.ok` is false, showing the exact failure and disabling the **Approve** button.
- **Inline Diff Viewer:** Renders `pending.diff` using `<DiffView diff={pending.diff} />` (`ui/src/components/DiffView.jsx`, which uses `react-diff-view` with side-by-side / unified / raw toggle and word-level highlighting).
- **Action Controls:**
  - **Approve & Apply** button (`variant="contained" color="success"`): calls `POST /api/tasks/{id}/config-change/approve` with `{ headSha: pending.headSha }`.
  - **Reject** button + text field for the rejection reason: calls `POST /api/tasks/{id}/config-change/reject` with `{ headSha: pending.headSha, reason }`.

#### Step 3: Approve and Reject Endpoints (`pkg/ui/task_config_change.go`)

- **`POST /api/tasks/{id}/config-change/approve`:**
  1. **Human-only check:** Refuses any request carrying an agent bearer token (`agentTokenFromContext(ctx) != nil` → `403 Forbidden: only a human operator may approve a configuration change`). Route scope in `route_scopes.go` is `"internal"` so no `api:*` scope ever exposes it to agents.
  2. **SHA pin verification:** Compares `req.HeadSHA` against `obs.PendingConfigChange.HeadSHA` and the live branch tip in `grain/config`. If either differs, returns `409 Conflict` (`"branch tip changed since diff was rendered"`).
  3. **Re-validates against current `main`:** Runs the validation suite (§4.4) against the merge of `req.HeadSHA` onto current `main`.
  4. **Merges & Syncs:** Calls `StateRepoPullRequestClient.MergePullRequest(ctx, "grain", "config", prNumber, MergePullRequestOptions{SHA: req.HeadSHA})`, which merges the branch into `grain/config` `main` and deletes the branch, then triggers an immediate `stateManager.Sync(ctx)` so the new configuration is imported into SQLite and pushed to any external mirror before the HTTP call returns.
  5. **Wakes the session:** Clears `obs.PendingConfigChange` and posts an automated system/operator message into the session (`"Approved and applied config change (<short-sha>): <summary>."`), resuming the director session.

- **`POST /api/tasks/{id}/config-change/reject`:**
  1. Verifies human caller and `req.HeadSHA`.
  2. Deletes the proposed branch `refs/heads/grain/config/...` from `grain/config` (closing the held state-repo PR).
  3. Clears `obs.PendingConfigChange` and posts `"Rejected config change (<short-sha>): <reason>"` into the session, waking the director so it can revise its changes in `/home/debian/grain-state` and call `propose_config_change` again if needed.

### 4.4 Validation Pipeline Before a Person Sees or Merges a Config PR

Every config PR—whether opened by `propose_config_change`, opened by a task targeting `grain/config`, or synced from an external repo PR—runs a four-stage validator (`pkg/staterepo/validate_pr.go`) before a human approves it:

1. **Schema & Structural Check (`staterepo.Check` in `pkg/staterepo/check.go`):**
   - Loads the candidate config tree (merged cleanly onto `grain/config` `main`) into a fresh, throwaway in-memory SQLite database at `model.SchemaVersion` with `PRAGMA foreign_keys = ON`.
   - Parses `settings/*.yml` (`templates.yml`, `schedules.yml`, `suites.yml`, `capabilities.yml`, `new-task-form.yml`, `config-policy.yml`) with `KnownFields(true)` so unknown keys or YAML syntax errors fail fast.
   - Collects `CheckReport.Warnings` (`describeDump` / `vocabulary.go` unknown columns or enum values).
2. **Capability Catalogue Consistency (`catalogue.CheckConsistency`):**
   - Verifies that every capability referenced in `defaults.task`, `defaults.ci`, `defaults.repos`, `settings/templates.yml`, `settings/schedules.yml`, `settings/new-task-form.yml`, and `console_sessions` is defined and enabled in `settings/capabilities.yml` and allowed by its own `repos`, `templates`, `kinds`, and `ci_only` rules.
   - Verifies that no plaintext secret material was committed (checks `staterepo.HasSecrets` and scans for raw key patterns where `$secret:<name>` references are expected).
3. **"Would This Lock Dispatch" Check (`CheckDispatchLiveness`):**
   - Compares the candidate config against the deployment's live bootstrap state:
     - **Model liveness:** If `default_model` or `director_model` is set, verifies that at least one enabled `agent_backend` serves it.
     - **Director liveness:** Verifies that the `director` console session definition in `console_sessions` remains valid and its model and capabilities are satisfiable, so a config change cannot brick the director that fixes config changes.
     - **Queue liveness:** Warns or fails if `max_parallel` is set to `<= 0` or if removing a repo from `target_repos` leaves currently running/queued tasks orphaned without a targetable repo.
4. **Dry-Run Apply (`staterepo.RehearseApply`):**
   - Opens a read/write transaction against a snapshot of the live controller database, runs `staterepo.ApplySettings(ctx, tx, candidateDir)`, computes the exact row-level delta across all `SettingsTables`, verifies `CheckSingleTenantIn`, and rolls back the transaction.

---

## 5. Default Human Gate and the Auto-Merge Exception List

### 5.1 Default Rule: Every Config PR Is Held for a Human

Today, `pkg/trustboundary/trustboundary.go` (`ClassifyState`) divides `staterepo.SettingsTables` into `StatePrivilegedTables` (which set `RequiresHuman: true`) and `StateBenignTables` (`custom_field`, `board*`, `memory*`, `release_note`, `README.md`, etc., which return nil and can auto-merge when a task has `auto_merge: true`).

Under this design, **every commit to `grain/config` is held for a human Submit by default:**
- `ClassifyState(paths, exceptions)` returns `StatePrivilege{Path: p, Reason: "config repository change", RequiresHuman: true}` for every changed path `p`, unless `p` matches an active opt-in exception in `settings/config-policy.yml` **and** `p` is not in the hardcoded `NeverAutoMergePaths` list.
- In `pkg/orchestrator/securityhold.go` and `pkg/daemon/statepullrequests.go`, any PR against `grain/config` with at least one `RequiresHuman: true` classification is parked on a security hold and cannot be merged by the merge queue or by an agent until a human clicks **Submit** (or **Approve** in `PendingConfigChange.jsx`).

### 5.2 Opt-In Exception List (`settings/config-policy.yml`)

Deployments that want specific low-risk config paths (such as weekly release notes or agent-curated memory files) to auto-merge without a human gate declare them in **`settings/config-policy.yml`**:

```yaml
# settings/config-policy.yml
# Only a human operator may modify this file.
auto_merge_paths:
  - "memory/**"
  - "release-notes/**"
  - "tables/memory.json"
  - "tables/memory_document.json"
  - "tables/release_note.json"
```

If `settings/config-policy.yml` does not exist or `auto_merge_paths` is empty, **no** path in `grain/config` auto-merges.

### 5.3 Hardcoded Ceiling (`NeverAutoMergePaths`)

To ensure an auto-merged change or a misconfigured glob in `settings/config-policy.yml` can never escalate privileges or widen the exception list itself, `pkg/trustboundary/trustboundary.go` enforces a hardcoded ceiling evaluated **before** `auto_merge_paths`:

```go
var NeverAutoMergePaths = []string{
    "settings/config-policy.yml",
    "settings/capabilities.yml",
    "settings/new-task-form.yml",
    "settings/templates.yml",
    "settings/schedules.yml",
    "settings/suites.yml",
    "tables/grain_config.json",
    "tables/repo_config.json",
    "tables/api_scope_set.json",
    "tables/git_credential.json",
    "tables/repo_git_credential.json",
    "tables/agent_backend.json",
    "tables/console_session.json",
    "tables/repo_workflow.json",
    "scripts/**",
    "workflows/**",
    ".github/**",
    ".grain/**",
    "schema-version",
}
```

- **Who may change `settings/config-policy.yml`:** Because `settings/config-policy.yml` is the first entry in `NeverAutoMergePaths`, any PR that creates, modifies, or deletes `settings/config-policy.yml` is unconditionally classified with `RequiresHuman: true`. Only a human operator clicking **Approve** / **Submit** in the UI can land a change to the exception list.
- **Evaluator uses base branch's exception list:** When evaluating whether a candidate PR on `grain/config` may auto-merge, `orchestrator/securityhold.go` reads `settings/config-policy.yml` from `grain/config`'s **base branch (`main`)**, never from the unmerged PR head.

---

## 6. Configurable New Task Form (`settings/new-task-form.yml`)

### 6.1 Inventory of Every Field in `NewTaskOverlay.jsx` and `GrantsPicker.jsx`

From `ui/src/components/NewTaskOverlay.jsx` and `ui/src/components/GrantsPicker.jsx`, the New Task form contains the following fields:

| Field ID | Current UI Control & Label | Default Placement | Current Default Value |
| :--- | :--- | :--- | :--- |
| `title` | `TextField` ("Title", required) | `shown` (top) | `""` (or `prefill.title`) |
| `description` | `TextField` multiline ("Description") | `shown` | `""` (or `prefill.description`) |
| `attachments` | `AttachmentPicker` (paperclip icon inside `description`) | `shown` | `[]` |
| `customFields.<id>` | `CustomFieldInputs` (one control per row in `tables/custom_field.json`) | `shown` (when declared) | `fileableFieldValues(config, fields)` |
| `repo` | `RepoField` ("Repos — the first is the one this task's own work lands in", includes `NO_REPO` and custom `changeTarget` options) | `shown` | `defaultRepo` (current page repo) or `""` |
| `newBranch` | `Checkbox` ("Create a new branch") + `TextField` (`newBranchName`, "New branch name") | `shown` (when `!noRepo`) | `false` / `""` |
| `base` | `BranchField` ("Base branch" or "Create from") | `shown` (when git repo target) | `lastBaseForRepo(tasks, repo)` or `""` |
| `writes` | `WriteReposField` (additional writable `{repo, base}` rows) | `shown` (when git repo target) | `[]` |
| `reads` | `ReadOnlyReposField` ("Read-only repos") | `shown` | `[]` |
| `autoMerge` | `Checkbox` ("Auto-merge once checks pass") | `shown` (when git repo target) | `!!config.autoMergeByDefault` |
| `noPort` | `Checkbox` ("Don't port to the repo's paired branches") | `shown` (when git repo target) | `false` |
| `fixesBaseBranch` | `Checkbox` ("This fixes a red `<branch>`") | `shown` (when `!noRepo`) | `false` |
| `legacyCapabilities` | `Select` multiple ("Legacy capabilities" — in-process capability IDs) | `shown` | `defaultCapabilitiesFor(config, repo)` filtered to legacy IDs |
| `grants` | `GrantsPicker` ("Grants" — `api:` presets/scopes and `<server>#<method>` capability grants with `with:` args) | `shown` | `defaultCapabilitiesFor(config, repo)` filtered to `api:` + capability refs |
| `model` | `ModelSelect` ("Model") | `shown` (when `models.length > 0`) | `""` (deployment default rotation) |
| `dependsOn` | `TaskPicker` ("Depends on") | `shown` | `[]` |
| `agentBackendId` | `TextField` select ("Specific connection") | `more` (Advanced options) | `""` (any enabled connection) |
| `networkAccess` | `TextField` select ("Network access": deployment default, `open`, `none`) | `more` (Advanced options) | `""` (deployment default) |
| `nothingNeededAction` | `TextField` select ("When the agent finds nothing to do": `close`, `ack`) | `more` (Advanced options) | `"close"` |
| `reviewTemplateId` | `TextField` select ("Review" — pre-merge review template) | `more` (Advanced options) | `""` (no review) |
| `promptExtension` | `TextField` multiline ("Prompt extension override") | `more` (Advanced options) | `""` |
| `sandboxShape` | Fieldset ("Sandbox shape override": `sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, or preset selector) | `more` (Advanced options) | `""` / `0` (deployment default) |
| `approved` | `Checkbox` ("Queue immediately (unchecked files it as a proposal, needing approval)") | `shown` (bottom) | `!!config.approvedByDefault` |
| `atFront` | `TextField` select ("Add to backlog": `end` vs `front`) | `shown` (bottom) | `!!config.newestFirst` |

> **Consolidation note on `legacyCapabilities` vs `grants`:** With the unified capability catalogue (§3), `GrantsPicker` handles all three grant kinds (`<server>#<method>`, `api:<preset>`, and in-process capabilities) in one picker (`grants`), retiring the separate `legacyCapabilities` dropdown when `settings/new-task-form.yml` is active (while still accepting `legacyCapabilities` as an alias in config).

### 6.2 Declarative Configuration: `settings/new-task-form.yml`

The entire New Task form is configured through **`settings/new-task-form.yml`** in `grain/config`:

```yaml
# settings/new-task-form.yml
# Controls which fields appear on the New Task form, their order, labels,
# offered options, and defaults -- deployment-wide and overridden per repo,
# per template, or per board.

default:
  # Ordered list of field configurations. Any field omitted inherits its
  # built-in placement, label, and default.
  fields:
    title:
      visibility: shown       # shown | more | hidden
      order: 10
      label: "Title"
    description:
      visibility: shown
      order: 20
      label: "Description"
      help_text: "Describe the goal, constraints, and acceptance criteria."
    attachments:
      visibility: shown
      order: 25
    customFields:
      visibility: shown
      order: 30
    repo:
      visibility: shown
      order: 40
      label: "Repository"
      help_text: "The repository this task's branch and pull request land in."
      default: "ai-on-gke/grain"
      allow_no_repo: true
      # Optional subset of target_repos to offer in the dropdown:
      offered: ["ai-on-gke/grain", "bwsalmon/grain", "gke-labs/granule"]
    newBranch:
      visibility: more
      order: 45
    base:
      visibility: shown
      order: 50
      default: ""
    writes:
      visibility: more
      order: 55
    reads:
      visibility: shown
      order: 60
    autoMerge:
      visibility: shown
      order: 70
      label: "Auto-merge once checks pass"
      default: true
    noPort:
      visibility: more
      order: 75
      default: false
    fixesBaseBranch:
      visibility: more
      order: 80
      default: false
    grants:
      visibility: shown
      order: 90
      label: "Capabilities"
      help_text: "Capabilities granted to this task's sandbox."
    model:
      visibility: shown
      order: 100
      default: ""             # "" uses deployment default model rotation
      offered: ["claude-opus-4-7", "gemini-3.1-pro", "gpt-5.4"]
    dependsOn:
      visibility: shown
      order: 110
    approved:
      visibility: shown
      order: 120
      default: true
    atFront:
      visibility: more
      order: 130
      default: false          # false = "end", true = "front"
    agentBackendId:
      visibility: hidden      # hidden from form; uses default ("")
      default: ""
    networkAccess:
      visibility: more
      order: 210
      default: ""
      offered: ["", "open", "none"]
    nothingNeededAction:
      visibility: more
      order: 220
      default: "close"
    reviewTemplateId:
      visibility: more
      order: 230
      default: ""
    promptExtension:
      visibility: hidden
      default: ""
    sandboxShape:
      visibility: more
      order: 240
      allow_custom_numbers: true
      default_preset: ""
      presets:
        - id: standard
          label: "Standard (4 vCPU, 8 GiB, 30 GiB disk)"
          cpus: 4
          memory_mb: 8192
          disk_gb: 30
        - id: large
          label: "Large (8 vCPU, 16 GiB, 60 GiB disk)"
          cpus: 8
          memory_mb: 16384
          disk_gb: 60

  # Capability picker presentation and defaults (within `grants`).
  capabilities:
    # Pre-added when the form opens (if omitted, inherits defaults.task from settings/capabilities.yml).
    defaults:
      - corp-research
      - api:task-reader
    # Optional allowlist of capabilities to offer in the picker (further intersected with
    # settings/capabilities.yml restrictions for the selected repo and template).
    offered: ["*"]
    # Capabilities to hide from the picker UI without disabling their server at runtime.
    hidden:
      - internal-migration-helper
    # Presentation overrides in GrantsPicker and GrantChip:
    display:
      corp-research:
        label: "Internal Code & Doc Search"
        description: "Read-only access to internal code_search and document search."
        order: 10
      self-debug:
        label: "Self-Debug Grain"
        description: "Inspect this controller's own logs and traces."
        order: 20

# Layer 2: Per-repository overrides (applied when that repo is selected in `repo`).
repos:
  ai-on-gke/grain:
    fields:
      reviewTemplateId:
        visibility: shown
        order: 105
        default: "go-ui-review"
      fixesBaseBranch:
        visibility: shown
    capabilities:
      defaults:
        - corp-research
        - self-debug
        - api:task-reader
  bwsalmon/kubernetes:
    fields:
      sandboxShape:
        visibility: shown
        default_preset: "large"

# Layer 3: Per-template overrides (applied when filing from a template or picking one).
templates:
  release-cut:
    fields:
      autoMerge:
        visibility: shown
        default: false
      networkAccess:
        visibility: hidden
        default: "none"

# Layer 4: Per-board / intake overrides (applied when clicking "+ New task" from that board).
boards:
  triage:
    fields:
      approved:
        visibility: hidden
        default: false        # Intake tasks always file as unapproved proposals
```

### 6.3 Layering and Resolution Order

When `NewTaskOverlay.jsx` renders (and when `POST /api/tasks` applies server-side defaults for hidden fields), configuration is merged deterministically in five layers from lowest to highest precedence:

$$\text{Built-in Defaults} \longrightarrow \text{Deployment (`default`)} \longrightarrow \text{Board (`boards[boardId]`)} \longrightarrow \text{Repo (`repos[repo]`)} \longrightarrow \text{Template (`templates[templateId]`)}$$

- **Field state (`fields.<id>`):** Higher layers override only the properties they specify (`visibility`, `order`, `label`, `help_text`, `default`, `offered`, etc.).
- **User edit preservation (`*Edited` refs):** Just as `baseEdited` and `capabilitiesEdited` work in `NewTaskOverlay.jsx` today, switching `repo` or `template` in the open form re-evaluates the merged layer and updates `default` values **only** for fields the user has not manually touched during the current form session.
- **Hidden fields:** When a field has `visibility: hidden`, `NewTaskOverlay.jsx` does not render it (neither in the main body nor inside "Advanced options"), and submits the resolved `default` value. If all fields assigned to `visibility: more` are moved to `shown` or `hidden`, the "Advanced options" `<Accordion>` does not render at all.
- **Fallback invariant:** When `settings/new-task-form.yml` is absent or empty, `resolveNewTaskForm` returns the exact built-in field map matching `NewTaskOverlay.jsx` today (`autoMergeByDefault`, `approvedByDefault`, `newestFirst`, `defaultCapabilitiesFor`).

### 6.4 Consistency with Enforcement (`CapabilityPolicy` and PR Validation)

1. **Runtime policy filtering in the picker:**
   - The capabilities offered in `GrantsPicker` and pre-populated by `capabilities.defaults` are intersected with `catalogue.AllowedFor(GrantContext{Repo: selectedRepo, TemplateID: selectedTemplate, Kind: "task"})` (§3) and `CapabilityPolicy` (`pkg/model/capability_ref.go`).
   - If a capability is `ci_only: true`, disabled, or restricted to `repos: ["ai-on-gke/grain"]`, it is automatically excluded from the picker and from defaults when `bwsalmon/silo` is selected.
   - Capabilities listed in `capabilities.hidden` are omitted from the `GrantsPicker` dropdown menu (unless already held on the draft/task, in which case `useHeldIds` keeps the chip removable), while remaining enabled at runtime for templates or schedules that declare them.
2. **Config PR validation (`staterepo.Check`):**
   - Validates `settings/new-task-form.yml` on every config PR:
     - Rejects unknown field IDs or invalid `visibility` values (`shown`, `more`, `hidden`).
     - Rejects `visibility: hidden` on `title`, and rejects `visibility: hidden` on `repo` unless `default` names a valid target repo or `allow_no_repo: true` is set with `default: "none"`.
     - Rejects any capability in `capabilities.defaults` or `capabilities.offered` that does not exist in `settings/capabilities.yml`, is `ci_only: true`, or is denied by `settings/capabilities.yml` for that layer's repo/template and kind `task`.
     - Rejects any model in `fields.model.offered` or `fields.model.default` that is malformed, and warns if no currently configured `agent_backend` serves it.

---

## 7. Migration and Compatibility

### 7.1 Existing Deployments (Internal `grain/config` and External Config Repo)

1. **Automatic initial export before arming refusal:**
   - Every deployment already maintains the controller-local Git repository `<data-dir>/state-repo` (`grain/config`) and syncs `staterepo.SettingsTables` on each tick (`cmd/grain/staterepo.go`).
   - On first startup after upgrading to this release, before `checkSettingsWritable` begins refusing non-bootstrap writes in internal mode, the daemon runs a one-time `ExportConfig` pass that materializes `settings/capabilities.yml` and `settings/new-task-form.yml` (seeded from existing `grain_config`, `repo_config`, and `api_scope_set` rows if those files do not yet exist in `grain/config`) and commits the migration to `grain/config` (and pushes to the external remote if configured, or stages a migration commit).
2. **External config repositories (`state-repo.json`):**
   - Deployments already using an external Git mirror (`docs/external-config-repo.md`) continue to work without change: `grain/config` mirrors to and from the external remote, and `StateRepoPullRequestClient` or the upstream GitHub PR workflow handles PRs.
   - When an external remote is configured, `propose_config_change` pushes the branch both to the local `grain/config` repo and to the external remote, and `Approve` in `PendingConfigChange.jsx` merges and pushes to the external remote so the two never diverge.

### 7.2 What Happens to DB-Only Settings

- **Settings tables (`staterepo.SettingsTables`):** Every configuration table (`grain_config`, `repo_config`, `git_credential`, `repo_git_credential`, `api_scope_set`, `custom_field`, `task_template`, `schedule`, `suite`, `board`, `memory`, `console_session`, etc.) is already in `staterepo.SettingsTables`. Any setting previously written only via `PUT /api/settings` into SQLite is exported into `grain/config` during the startup sync so zero DB-only configuration is lost.
- **Bootstrap tables (`agent_backend`, `executor_token`, `<data-dir>/cli-run.json`, `<data-dir>/state-repo.json`, `<data-dir>/secrets`):**
  - `agent_backend` and the bootstrap columns of `grain_config` (Models and Sandboxes) remain writable from the Settings UI; when mutated in the UI, the controller commits the updated `tables/agent_backend.json` / `tables/grain_config.json` straight to `grain/config` (just as internal state-repo export does today) so `grain/config` still has a complete snapshot of non-secret bootstrap config.
  - `<data-dir>/secrets` and `executor_token` never enter `grain/config`.

### 7.3 Per-User / Browser Preferences

- Browser-local display preferences in `ui/src/components/settings/PreferencesSection.jsx` and `ui/src/PreferencesContext.jsx` (color theme `light`/`dark`/`system`, compact density, sound alerts) live in the user's browser `localStorage` and are per-user/per-browser. They do not touch the server DB or `grain/config` and **stay in the UI** (accessible from the user/theme menu or Settings → Preferences).
- The deployment-wide `newest_first` ("Add to backlog: Front vs End") setting that previously sat alongside preferences moves to `settings/new-task-form.yml` (`default.fields.atFront.default`) and `tables/grain_config.json`.

### 7.4 In-Flight `GrantsPicker` Work ([Task #1470](/tasks/1470) and [Task #1471](/tasks/1471))

- **[Task #1470](/tasks/1470) (`GrantsPicker` for per-task capability picking in the New Task form, task detail pane, templates, and schedules):**
  - **Keep in full.** In `NewTaskOverlay.jsx` and `DetailOverlay.jsx`, `GrantsPicker` is the interactive control for selecting capabilities on a task, driven by the capability catalogue (`settings/capabilities.yml`) and the New Task form config (`settings/new-task-form.yml`).
- **[Task #1471](/tasks/1471) (`GrantsPicker` in Settings for deployment default capabilities, per-repo default capabilities, and CI capabilities):**
  - Task #1471 has already landed in `ui/src/components/settings/TasksSection.jsx` and `ui/src/components/RepoPage.jsx`.
  - **Disposition:** Do not add any further Settings-writing features to it. In Slice 4 (when `TasksSection` and `RepoPage` capability settings become config-repo-only), replace the mutable `GrantsPicker` in `TasksSection.jsx` and `RepoPage.jsx` with a read-only list of `<GrantChip>` components displaying the active defaults from `settings/capabilities.yml` alongside a **"Edit in `settings/capabilities.yml`"** link to the Config Repo Browser.

---

## 8. Implementation Slices

Each slice below is a self-contained, reviewable pull request in dependency order, filed as a proposed task with `depends_on` chaining them. **No implementation is performed in [Task #1515](/tasks/1515).**

### Slice 1: Default Human Gate on Config PRs and `settings/config-policy.yml`
- **Scope:**
  - Update `pkg/trustboundary/trustboundary.go` (`ClassifyState`) so every path in `grain/config` returns `StatePrivilege{RequiresHuman: true}` by default.
  - Add `settings/config-policy.yml` parser (`auto_merge_paths` globs) and hardcode `NeverAutoMergePaths` (`settings/config-policy.yml`, `settings/capabilities.yml`, `settings/new-task-form.yml`, `settings/templates.yml`, `settings/schedules.yml`, `settings/suites.yml`, `tables/grain_config.json`, `tables/repo_config.json`, `tables/api_scope_set.json`, `scripts/**`, `workflows/**`, `.github/**`, `.grain/**`).
  - Update `pkg/orchestrator/securityhold.go` and `pkg/daemon/statepullrequests.go` to load `settings/config-policy.yml` from `grain/config`'s base branch when evaluating whether a config PR requires a human Submit.
  - Add unit tests in `pkg/trustboundary/state_test.go` and `pkg/orchestrator/securityhold_test.go`.

### Slice 2: Declarative Capability Catalogue (`settings/capabilities.yml`) and Unified Enforcement
- **Scope:**
  - Add `settings/capabilities.yml` projection and parser in `pkg/staterepo/capabilities_file.go` and evaluator in `pkg/model/capability_catalogue.go`, unifying `mcp_servers`, in-process capabilities, `api_scope_set`, `capability_policy`, `default_capabilities`, and `ci_capabilities`.
  - Support per-capability restrictions: `repos`, `templates`, `kinds` (`task`, `session`, `director`, `ci`, `process`, `review`), `ci_only`, `requires_person` (and `requires_person_when`), and `defaults` (`task`, `ci`, `repos`).
  - Wire `catalogue.ValidateGrants` into task filing (`POST /api/tasks`, `propose_task`), task capability edits (`POST /api/tasks/{id}/capabilities`), and runtime dispatch (`pkg/orchestrator`, `pkg/daemon/capabilities.go`, `pkg/daemon/ci_policy.go`), forcing `proposed` (unapproved) state whenever an agent proposes a task with a `requires_person: true` capability.
  - Add `staterepo.Check` validation for `settings/capabilities.yml`.

### Slice 3: Config-Repo-Only Write Path (`409` Rehearsal in Internal Mode) and Pre-Review Config PR Validation
- **Scope:**
  - Update `pkg/ui/config_refusal.go` (`checkSettingsWritable` and `checkSettingsWritableUnrehearsed`) so all non-bootstrap configuration routes refuse direct DB mutations even when `externalConfigRepo(ctx)` is empty, returning the rehearsed `409 Conflict` patch against `grain/config`.
  - Keep bootstrap routes writable in the UI: Models (`agent_backend`, `default_model`, `director_model`, `model_concurrency`, `framework_concurrency`, model credentials), Sandboxes (`sandbox_backend`, `granule_*`, `sandbox_base_image`, `sandbox_image_*`, `max_parallel`, `default_sandbox_*`, `network_access`, `idle_sandbox_ttl_minutes`, `executor_token`, `cli-run`), Secrets (`PUT /api/secrets/...`, `PUT /api/tasks/{id}/secret`), Access, and operational actions.
  - Implement the pre-review config PR validation suite in `pkg/staterepo/validate_pr.go`: `staterepo.Check`, capability catalogue consistency, `CheckDispatchLiveness` ("would this lock dispatch"), and `RehearseApply` dry-run apply.
  - Add `POST /api/state-repo/pull-requests` to open a held config PR directly from rehearsed `FileChange`s.

### Slice 4: Shrink Settings UI to Models & Sandboxes and Point Departing Sections at Config Repo Browser
- **Scope:**
  - Update `ui/src/settingsSections.js` and `ui/src/components/SettingsPage.jsx` so the primary Settings UI holds **Models**, **Sandboxes**, **Credentials / Secrets**, **Access**, **Preferences** (browser-local), and **System / Operations** (Upgrade, Power, Caches, Engine pause, Config repo mirror).
  - Convert departing Settings sections (`GeneralSection`, `GitSection`, `RepositoriesSection`, `TasksSection`, `AgentsSection`, `CapabilitiesSection`, `CISection`, `MemorySection`) and per-repo config cards in `RepoPage.jsx` (including the Task #1471 `GrantsPicker` instances in `TasksSection.jsx` and `RepoPage.jsx`) into compact read-only summaries with deep links to the Config Repo Browser (`/config?path=...`).

### Slice 5: Writable Director Checkout of `grain/config` and In-Session Inline Approval (`propose_config_change`)
- **Scope:**
  - Update `pkg/orchestrator/checkout.go` (`prepareDirectorState`, `directorStateSection`) and `pkg/gitproxy/{authorize,refupdates}.go` so sessions with `grain/config` in `Writes` receive a writable clone at `/home/debian/grain-state` and may push branches under `refs/heads/grain/config/<session-id>/*` while `refs/heads/main` remains protected.
  - Add the `propose_config_change` MCP tool in `pkg/mcpserver`, storing `PendingConfigChange` (pinned to `headSha`, with unified diff and validation report from Slice 3) on the task/session observation and parking the turn.
  - Add human-only endpoints `POST /api/tasks/{id}/config-change/approve` and `POST /api/tasks/{id}/config-change/reject` in `pkg/ui/task_config_change.go` (rejecting agent bearer tokens, verifying `headSha`, merging via `StateRepoPullRequestClient`, triggering immediate `stateManager.Sync`, and waking the session).
  - Build `ui/src/components/detail/PendingConfigChange.jsx` using `DiffView.jsx` (`react-diff-view`) and wire it into `TaskChat.jsx` and `DetailOverlay.jsx`.

### Slice 6: Configurable New Task Form (`settings/new-task-form.yml`)
- **Scope:**
  - Add `settings/new-task-form.yml` schema, parser, and validator in `pkg/staterepo/new_task_form_file.go` and expose the resolved configuration on `GET /api/config` (`newTaskForm`).
  - Support per-field `visibility` (`shown`, `more`, `hidden`), `order`, `label`, `help_text`, `default`, `offered` options (repos, models, network access, sandbox size presets), and capability picker rules (`defaults`, `offered`, `hidden`, `display`), layered across `default` → `boards` → `repos` → `templates`.
  - Update `ui/src/components/NewTaskOverlay.jsx` and `ui/src/components/GrantsPicker.jsx` to render fields, ordering, presets, and filtered capabilities from `resolveNewTaskForm(config, { repo, templateId, boardId })`, falling back to current behavior when unconfigured.
  - Enforce server-side defaults for hidden/locked fields in `POST /api/tasks` and validate `settings/new-task-form.yml` against `settings/capabilities.yml` in `staterepo.Check`.
