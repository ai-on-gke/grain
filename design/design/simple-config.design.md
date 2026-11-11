# Simplified Configuration Mode

**Owner:** @bwsalmon

**Status:** Proposed (design only; no code yet)

A Grain deployment today exposes more than eighty settings across sixteen Settings sections, seven per-repository tabs, twenty-four New Task form controls, five system schedules, and twenty-eight config-repository tables ([`settings.spec.md`](../spec/settings/settings.spec.md)). That surface exists for large, shared deployments that run custom capability servers, release trains, qualification plans, and GitOps config-repository workflows. For a personal or small-team instance—especially one created in two clicks through the Grain registry ([`hub.design.md`](hub.design.md))—it is overwhelming on day one and creates dozens of ways to misconfigure a working appliance.

**Simplified configuration mode** (`config_mode: simple`) is a deployment-level mode in which an `admin` sees and edits only what is needed to connect Grain to models, connect it to Git and pick repositories, manage allowed users and their roles, and supply task secrets when asked. Every other setting is pinned to a single, versioned default profile (`pkg/model/profiles/simple.v1.yml`), hidden in the UI (behind one read-only inspection link), and refused at both the HTTP/MCP API and the config repository. An `admin` who outgrows simple mode can switch to `full` in place without losing any connection, task, user, or credential.

Read with:
- [`settings.design.md`](settings.design.md), [`settings.spec.md`](../spec/settings/settings.spec.md), and [`settings-mutability.spec.md`](../spec/settings/settings-mutability.spec.md) — the six state surfaces, `settings_locked`, `clearBootstrap`, `pkg/ui/config_refusal.go`, and `pkg/staterepo`.
- [`settings-panel.design.md`](settings-panel.design.md) and `settings/new-task-form.yml` (`pkg/model/new_task_form.go`) — the Settings navigation and declarative New Task form layers that simple mode reuses rather than inventing a parallel UI mechanism.
- [`hub.design.md`](hub.design.md) — how personal and small-team VM instances are created, authenticated, backed by externalised Cloud SQL / Secret Manager / config-repo state, upgraded, and replaced.
- [`authentication.design.md`](authentication.design.md), [`operator-attribution.design.md`](operator-attribution.design.md), and [Task #2764](/tasks/2764) (`design/design/rbac.design.md`, `design/spec/rbac/rbac.spec.md`) — verified human principals (`IdentityVerifier`, `-ui-allowed-principals`, `allowed_principal`, `ui.RequestActor`) and role-based access control (`admin`, `writer`, `reader`).

---

## 1. What the mode is {#SC-1}

### 1.1 One deployment setting: `config_mode`

Simple mode is controlled by a single column on `grain_config` (`model.Config.ConfigMode`, JSON key `configMode` on `GET`/`PUT /api/settings` and `GET /api/config`, exported to `tables/grain_config.json`):

```go
type ConfigMode string

const (
    ConfigModeFull   ConfigMode = "full"   // default when empty ("")
    ConfigModeSimple ConfigMode = "simple" // simplified configuration mode
)
```

- **Backward-compatible zero value.** An empty stored value (`""`) in existing SQLite/PostgreSQL rows or `staterepo` dumps resolves to `ConfigModeFull` (`Config.EffectiveConfigMode()`). Upgrading an existing deployment never hides or resets its configuration.
- **Seed-only startup flag.** `grain daemon` gains `-config-mode=simple|full` in the `seedOnly` flag class (`pkg/cli/daemon.go`, `settings.spec.md` §10.1): it seeds `grain_config.config_mode` only when creating the initial `grain_config` row (`GetConfig` returns `nil`, and no external state repo was loaded). Once the row exists in the database, the stored value wins, so an `admin` can switch `simple → full` in the UI without editing `/etc/grain/daemon.env` or VM metadata, and restarts or VM replacements (§9) keep the chosen mode.
- **`grain init --simple`.** Workstation and self-hosted installs keep `full` as the default, with `--simple` opting into `config_mode: simple` at bootstrap.

### 1.2 Who may switch `config_mode` {#SC-2}

Only a **human `admin`** may change `config_mode` (`simple → full` or `full → simple`):

1. **Never an agent.** Any request carrying `PrincipalAgent` (scoped agent API, MCP, or in-sandbox CLI—even if holding `api:settings:write`) attempting to modify `config_mode` is refused unconditionally with `403 Forbidden` (`code: "person_required"`). Allowing an agent to flip `simple → full` would bypass the entire lock; allowing `full → simple` would let an agent reset operator configuration. This extends the existing `checkGrainConfigWritable` rule that already bars `PrincipalAgent` from unlocking `settings_locked` (`settings-mutability.spec.md` §2.9.2).
2. **`admin` role required under RBAC (§4).** Among human callers, switching `config_mode` requires the `admin` role defined in `design/design/rbac.design.md` (`writer` and `reader` callers receive `403 Forbidden`, `code: "insufficient_role"`). Pre-RBAC, any verified human principal (`PrincipalHuman`) or local break-glass operator may switch it.
3. **Not via config-repo commits in simple mode.** While `config_mode == "simple"`, a `grain/config` commit or pull request cannot flip `config_mode` to `"full"` (`staterepo.Check` refuses it; §6.2); the switch must happen through the authenticated UI/API by a human `admin`.

### 1.3 Default for registry-created instances {#SC-14}

**Recommendation: registry-created instances default to `simple`.**

When `grain-registry` provisions a VM (`hub.design.md`, *Instance lifecycle → Create*, step 5), it includes `config_mode=simple` and `deployment_owner=<owner>` in the VM's `grain-config` metadata alongside the registry verifier flags, Cloud SQL connection name, Secret Manager prefix, and config-repo URL. The first-boot unit writes `-config-mode=simple` and `-deployment-owner=<owner>` into `/etc/grain/daemon.env`.

Why `simple` should be the registry default:
- Registry users are individual engineers or small teams spinning up an appliance to run tasks on a handful of repos; forcing them through sixteen Settings sections, capability policies, and `grain/config` PRs defeats the purpose of two-click instance creation.
- Nothing is permanently lost or walled off: because `-config-mode` is seed-only, any `admin` who later wants custom schedules, capability servers, or merge-queue tuning clicks **Switch to full mode…** in Settings and immediately gets the full Grain surface on the same instance.

---

## 2. What stays editable in `simple` mode

In `simple` mode, standing deployment configuration shrinks to **four editable areas** (all restricted to the `admin` role; §4), plus write-only task secrets when tasks need credentials:

| Area | Route / UI | What an `admin` can edit | What is defaulted / hidden within the area |
| --- | --- | --- | --- |
| **1. Model connections** | `/settings/models` | Add, edit, enable/disable, reorder, test, and remove `claude`, `codex`, and `antigravity` connections (API key or OAuth/login); pick primary `defaultModel` and optional ordered fallback (`defaultModels`). | Per-connection `concurrency` (`4`), `cliPath` (empty), `custom` command backend, `modelTurnTimeout` / `modelRetryCutoff`, per-model reasoning `effort` (`modelSettings`), `agentBackendId` pin, `judgementModel`, and `cliRun` (`Where each agent CLI runs`). |
| **2. Git connections & commit identity** | `/settings/git` | Add, test, and remove GitHub Apps, PATs, or SSH keys for `github.com`, GitHub Enterprise, GitLab, Gitea, or other git hosts (`gitConnections`, optional `scope`, per-repo `git_credential`); optional commit author override (`agentGitName`, `agentGitEmail`). | `githubHost` / `githubInsecureHttp` startup-restart plumbing (each connection already carries its own `host`). Empty `agentGitName` / `agentGitEmail` defaults automatically to the credential's own identity (for example, the GitHub App bot or PAT owner's `~/.gitconfig`, preserving CLA compliance). |
| **3. Repositories** | `/settings/repositories` | Add or remove repositories in `targetRepos` via a searchable checklist populated from `GET /api/deployment/reachable-repos` (plus manual `owner/name` entry for hosts without listing). | All per-repository settings tabs (`/repos/<o>/<n>/settings/*`); every repository runs with zero-row `RepoConfig` defaults and derived CI mode (§3.11). |
| **4. Allowed users & roles** | `/settings/users` | Add or remove `user:<email>`, `group:<email>`, and `domain:<domain>` entries in `allowed_principal`, and set each entry's RBAC role (`admin`, `writer`, `reader`). | On a registry (or owner-seeded) instance, the instance owner `user:<deploymentOwner>` is permanently pinned to `admin` and cannot be removed or demoted (§4.3). `gcp-key` pool service accounts remain rejected (`RejectGCPKeyPoolAllowedPrincipals`). |

### Model rotation vs. per-connection limits (explicit decision)

- **Connection rotation and fallback models are IN (kept effortless).** Single-account personal credentials hit provider rate limits and session pauses regularly. Adding a second credential (two Claude logins, or Claude + Gemini) only helps if Grain rotates across enabled connections and falls back across models. Simple mode keeps `AgentBackends` list ordering (up/down reorder + Enable/Disable toggle) and the single *Default model* + *Add fallback model* picker (`defaultModel` / `defaultModels`).
- **Per-connection limits and harness plumbing are OUT (pinned to defaults).** `concurrency` stays at `DefaultAgentBackendConcurrency` (`4`), `modelTurnTimeout` and `modelRetryCutoff` use framework defaults, `modelSettings` (reasoning effort) stays empty (provider default), `custom` shell-command backends are hidden, and `cliRun` stays where the deployment image/host configured it (`hostd` on registry VMs, container/daemon elsewhere).

---

## 3. "What else might we need?" — Candidate-by-candidate evaluation

Every other capability or knob in Grain falls into one of three buckets in simple mode: **In** (editable by `admin`), **Derived** (computed automatically without a knob), or **Out** (locked to the `simple.v1` profile default and hidden).

| # | Candidate | Decision | Summary |
| --- | --- | --- | --- |
| 3.1 | **Repositories** | **In** (candidates derived from git connection) | Explicit `targetRepos` opt-in list picked from `reachable-repos`; avoids exposing every repo a PAT/App can see. |
| 3.2 | **People and access (allowed users + RBAC)** | **In** | Editable `user:`, `group:`, `domain:` allowlist with `admin` / `writer` / `reader` roles; registry owner is non-removable (§4). |
| 3.3 | **Secrets vs. capabilities** | **Secrets: In** (write-only) · **Capabilities: Out** | `request_secret` in tasks and a minimal write-only Secrets list stay active; capability servers and policy stay locked. |
| 3.4 | **Spending and limits** | **Out + Derived** (from host shape) | `maxWorkers: 4`, auto-submit caps `2`/`5`, and `sandboxCommitment` dynamically scales concurrency to the VM's CPU/RAM/disk. |
| 3.5 | **Notifications** | **Out** (built-in Inbox + browser toggle) | Uses Grain's built-in `/inbox`, header alerts, and per-browser Web Notifications (`localStorage`); external chat bots stay out. |
| 3.6 | **Upgrades** | **Auto-upgrade ON** + manual **Upgrade** action | Standalone: `autoUpgradeChannel: "latest"` at `03:00` + `POST /api/upgrade`. Registry VMs (`-disable-self-upgrade`): VM replacement upgrade (`POST /api/entries/<name>/upgrade`). |
| 3.7 | **Agent framework & model per task** | **Framework: Out** · **Model: `more` override** | Tasks never pick an `agentBackendId` or CLI framework; `model` defaults to `defaultModel` with an optional picker under *More options*. |
| 3.8 | **Templates, schedules, suites, boards, custom fields** | **Out** (2 essential system schedules ON) | UI surfaces hidden and user rows locked; **only** `system:pipeline-steward` and `system:memory-agent` stay enabled. |
| 3.9 | **Capabilities & capability servers** | **Out** (fixed built-in catalogue) | `EnsureBuiltins` only; no custom MCP servers, no `defaultCapabilities`, no `api_scope_set` edits, `externalAgentApi: false`. |
| 3.10 | **Sandbox backend and size** | **Out** (fixed by deployment / VM shape) | Backend (`""` → startup flags) and per-sandbox shape (`0` → backend defaults) are locked so users cannot brick sandboxes. |
| 3.11 | **CI mode per repo** | **Derived** (`github` if Actions/checks exist, else `none`) | Inspects `.github/workflows/` and commit check suites: gates on GitHub CI when present, skips waiting when absent. |
| 3.12 | **Browser preferences & Director chat** | **Kept** (non-config surfaces) | Per-browser theme/timezone (`localStorage`) and the default `director` console session work out of the box with zero settings. |

### 3.1 Repositories: **In** (with reachable candidates derived from git connections)

A git connection authenticates to a forge, not to a work scope. A personal GitHub PAT or org-wide GitHub App installation frequently sees fifty to five hundred repositories, while Grain's empty `target_repos` (`[]`) means *every* repository is allowed (`RepoAllowed`), leaving the New Task form without a curated picker and allowing tasks to target unintended repositories. Conversely, auto-adding every visible repository would flood the UI.

Simple mode therefore keeps **Repositories** (`/settings/repositories`, and Step 3 of first-run setup) editable:
- Grain queries `GET /api/deployment/reachable-repos` across the configured `gitConnections` and presents a searchable multi-select checklist (plus a manual `owner/name` text box for SSH or custom git hosts that cannot enumerate repositories).
- In simple mode, `targetRepos` SHOULD be non-empty before tasks are filed; when `len(targetRepos) == 1`, the New Task form pre-selects that repository automatically.
- Per-repository configuration tabs (`/repos/<owner>/<name>/settings/*`) are hidden and locked; all added repositories run with default `RepoConfig` (only `repo_config.git_credential` is settable when multiple git connections exist).

### 3.2 People and access: **In** (editable allowlist + RBAC roles)

When `-ui-allowed-principals` is only a startup CLI/env flag (`pkg/ui/allowlist.go`, `pkg/cli/daemon.go`), an owner on a registry VM cannot invite a teammate without SSHing into the VM and editing `/etc/grain/daemon.env`. In simple mode, **Users** (`/settings/users`, backed by the database `allowed_principal` table) is editable by any `admin` and pairs each `user:`, `group:`, or `domain:` entry with an RBAC role (`admin`, `writer`, `reader`) from [Task #2764](/tasks/2764) (`design/design/rbac.design.md`). On a registry instance, the registry's verified instance owner (`-deployment-owner`) is permanently admitted with role `admin` and cannot be removed or demoted. Section 4 specifies the exact RBAC contract and lockout-safety rules.

### 3.3 Secrets that tasks need vs. capabilities: **Secrets In (write-only), Capabilities Out**

Even on a one-person project, repository tasks and tests routinely need a secret that does not belong in Git—an npm token, a container registry credential, or a test API key. Grain already has a built-in workflow for this: an agent calls the `request_secret` MCP tool, which parks the task in the Inbox and renders a write-only value box backed by `PUT /api/tasks/{id}/secret` (`pkg/secrets`, stored in Secret Manager on registry VMs or `<data-dir>/secrets` on standalone installs).

- **What stays In:** `request_secret` (`PUT /api/tasks/{id}/secret`) and a compact **Secrets** page (`/settings/secrets`, backed by `GET /api/secrets` for names/metadata only and `PUT`/`DELETE /api/secrets/{secret}/{key}`) stay open to `admin`s so they can fulfill secret requests, pre-seed a secret, or rotate/delete an expired one. Secret values never enter `grain/config` and can never be read back through the API.
- **What stays Out:** Custom capability servers (`mcp_servers`), capability policy rules (`capability_policy`), legacy GCP key-minting plumbing (`gcpProject`, `gcpServiceAccountEmail`, `gcp_key`), and task capability grants (`defaultCapabilities`) are locked and hidden.

### 3.4 Spending and limits: **Out + Derived from host hardware**

Raw scheduler and budget knobs (`pollInterval`, `maxMergers`, `maxCIJobs`, `sandboxCpuOvercommit`, `sandboxMemoryOvercommit`, `sandboxDiskOvercommit`, `sandboxMemoryReserveMb`, `sandboxDiskFloor*`, `sandboxOvercommit`) require understanding Grain's admission arithmetic and are locked in simple mode:
- **`maxWorkers` is pinned to `4`** (rather than full mode's `10`). Four concurrent workers match `DefaultAgentBackendConcurrency` (`4`) and prevent a burst of follow-up tasks from exhausting a personal Claude or Gemini rate-limit window in a single minute.
- **Hardware scaling is already automatic.** Grain's `NewBudget` (`pkg/model/budget.go`) computes `sandboxCommitment` dynamically from the host's actual vCPUs, RAM (`ReserveMemoryDefault = -1`, reserving 1/8 of host RAM, minimum 2 GiB), and disk (`DefaultDiskFloorShare = 0.10`, `MinDiskFloorMB = 5120`). On a `small` registry VM (4 vCPU, 16 GiB RAM), the budget naturally caps concurrent sandboxes to what fits in ~14 GiB; on a `large` VM (16 vCPU, 64 GiB RAM), it scales up to the `maxWorkers: 4` (+ `maxMergers: 1`) ceiling without changing a single setting.
- **Runaway proposal caps stay tight:** `maxAutoSubmitDepth: 2`, `maxAutoSubmitFanOut: 5`, `maxAgentTurns: 0` (framework default, bounded by the 2-hour per-run wall-clock timeout), and provider quota pauses automatically park runs until quota resets.

### 3.5 Notifications: **Out (defaulted to Inbox and browser notifications)**

Grain's built-in **Inbox** (`/inbox`), header alert badge (`AlertsButton.jsx`), and per-browser Web Notifications toggle (`grain.inboxNotifications` in `localStorage` under `/settings/preferences`) work with zero external credentials. External notification integrations (`googleChatSpace` on console sessions and the `gchat` capability server) require bot credentials and space IDs and are **Out** in simple mode.

### 3.6 Upgrades: **Auto-upgrade ON (`latest`) + manual Upgrade action**

In full mode, `autoUpgradeChannel` defaults to `""` (off), because production multi-engineer controllers often coordinate upgrades manually. On personal and small-team appliances, leaving upgrades off means instances stagnate on the release image they booted with.

Simple mode keeps instances current while respecting how each deployment hosts its daemon:
- **Standalone / self-hosted instances:**
  - **Standing policy (locked):** `autoUpgradeChannel: "latest"`, `autoUpgradeTime: "03:00"` (in the deployment's `timeZone`, default `UTC`), `upgradeCheckMode: "auto"`, `upgradeCheckPausesPipeline: false`. Grain drains and upgrades non-disruptively (`non-disruptive-upgrade.design.md`) overnight as new releases are promoted.
  - **Manual action (kept for `admin`):** `/settings/upgrade` keeps the one-click **Upgrade to latest** button (`POST /api/upgrade`) so an `admin` can pull a newly promoted release immediately without switching modes. Arbitrary branch/image-override inputs on the Upgrade panel are hidden in simple mode.
- **Registry-created VM instances (`-disable-self-upgrade`):**
  - Per [`hub.design.md`](hub.design.md) (*Instance lifecycle → Replace (upgrades and shape changes)*), registry VMs run with `-disable-self-upgrade` because the VM itself is disposable: upgrading happens by replacing the VM (`status = replacing`) with a fresh boot image while Cloud SQL, Secret Manager, and the per-instance config repository preserve all state (§9).
  - On a registry VM, `/settings/upgrade` shows the running release version, whether a newer registry image is available, and links directly to the registry's one-click upgrade action (`POST /api/entries/<name>/upgrade` on the registry owner page), while any scheduled fleet upgrade window is driven by `grain-registry`.

### 3.7 Agent framework and model choice per task: **Framework Out, Model optional in `more`**

In full mode, `settings/new-task-form.yml` (`model.NewTaskForm`) exposes twenty-four task controls. Pinning a task to a specific `agentBackendId` or CLI framework (`claude` vs `codex` vs `antigravity`) ties the task to one credential quota pool and requires knowing how CLI harnesses differ.

Simple mode applies a built-in **`SimpleNewTaskForm`** layer (`pkg/model/new_task_form.go`; §5.3) instead of inventing a separate task form:
- **Primary (`shown`):** `title`, `description`, `repo` (pre-filled when only one target repo exists), and `attachments`.
- **Collapsed (`more`):** `base` (target branch), `autoMerge` (default `true`), `approved` (*Queue immediately*, default `true`), `dependsOn`, and **`model`** (optional override restricted to models served by the enabled connections; empty by default so tasks use `defaultModel` and fallback rotation, and hidden automatically when only one model is available).
- **Hidden (`hidden`, using profile defaults):** `agentBackendId`, `grants`, `legacyCapabilities`, `networkAccess`, `nothingNeededAction`, `reviewTemplateId`, `promptExtension`, `hostd`, `sandboxShape`, `customFields`, `noPort`, `fixesBaseBranch`, `writes`, `reads`, `newBranch`, `atFront`.

### 3.8 Templates, schedules, suites, boards, and custom fields: **Out (exact system-schedule split)** {#SC-10}

User-defined templates, schedules, suites, custom task boards, and custom fields are workflow-orchestration tools for larger teams. In simple mode:
- Top-level sidebar links to **Boards**, **Processes** (Templates, Schedules, Suites), and **Config** (`/config`) are hidden, custom fields are hidden, and API mutations to `template*`, `schedule*`, `suite*`, `board*`, and `custom_field` are refused (`409 locked_in_simple_mode`).
- **Standing Director chat stays ON:** `grain_config.console_sessions` is pinned to `nil` (which resolves to the single built-in **Director** session), so `admin`s and `writer`s can chat with the Director to plan and file tasks, while creating or editing custom standing console sessions is locked.
- **System schedules (`pkg/model/system_template.go`):** Grain ships five seeded schedules in `model.SystemTemplates` plus the repo-declared `system:workflow-review:<stem>` family. Simple mode enables **only the two essential operational schedules** and suppresses the other four in `reconcileSystemSchedules`:

| System schedule ID | Cadence | Simple mode | Justification |
| --- | --- | --- | --- |
| `system:pipeline-steward` | Event-driven (`pipeline.*`) + 6h safety check (`SkippedQuiet` when healthy) | **ON** | Essential self-healing: diagnoses confirmed red base branches and coordinates a single `base-green` repair task so the user's repo does not wedge. Costs **zero tokens** when healthy (`pipelineStewardQuiet`). |
| `system:memory-agent` | Nightly (`03:00`) | **ON** *(with quiet skip)* | Essential in simple mode *because* `promptExtension` and `setupCommand` are locked: memory is how Grain records a repository's build/test conventions from completed tasks. Extended with `SkippedQuiet` when zero tasks completed since its last run, so idle instances spend zero tokens overnight. |
| `system:infra-review` | Nightly (`03:00`) | **OFF** | Audits overcommit ratios, cache sizes, worker counts, and merge-queue knobs to propose `grain/config` PRs—every one of which is locked in simple mode. Running it would spend tokens on unmergeable config advice. |
| `system:release-notes` | Weekly (Monday) | **OFF** | Writes weekly deployment-throughput narratives (`release-notes/`), unnecessary on personal/small-team appliances and wasteful of model quota. |
| `system:steward-audit` | Weekly (Monday) | **OFF** | Meta-audits 1-in-10 `pipeline-steward` wakes for Grain's own steward prompt tuning; unnecessary overhead on simple instances. |
| `system:workflow-review:<stem>` | Monthly | **OFF** | `repo_config.declarations` is `false` in simple mode (§3.11), so `.grain/workflows/*` jobs are not imported. |

### 3.9 Capabilities and capability servers: **Out (fixed built-in catalogue)**

`EnsureBuiltins` (`pkg/capability/intree/builtins.go`) continues to register the standard in-tree capability servers (`grain-sandbox`, `browser`, `google-workspace`, `gcp-key`), which stay inert unless granted. In simple mode:
- Adding, editing, or deleting custom `mcp_servers`, `capability_policy`, `api_scope_set` rows, or `defaultCapabilities` is locked.
- Every task gets `grain-sandbox` (workspace file/command tools, PR/CI status, `ask_question`, `request_secret`, `comment_on_issue`, `propose_task`). No task in simple mode can be granted `Dangerous` `api:*:write` scopes.
- `externalAgentApi` is pinned to `false` (no `gma_` external-agent bearer tokens). Human CLI access (`grain task list`, `grain task create`) still works via IAP ID tokens (`authentication.design.md`, *grain CLI*).

### 3.10 Sandbox backend and size: **Out (fixed by deployment / registry shape)**

Changing `sandboxBackend` (`bwrap`, `vm`, `container`, `k8s`, `k8s-granule`) or `k8s_*` fields on a live appliance can immediately break every task run, and registry VM sizing (`small` / `medium` / `large`) is already chosen in `grain-registry` (`hub.design.md`, *Configuration* & *Replace*). Simple mode pins `sandbox_backend: ""` (resolved from daemon startup flags: `vm` on registry instances, `k8s-granule` on GKE, `bwrap`/`container` locally) and pins per-sandbox shape overrides (`sandboxCpus: 0`, `sandboxMemoryMb: 0`, `sandboxDiskGb: 0`) to backend defaults (`2` vCPU, `8192` MiB RAM).

### 3.11 CI mode per repository: **Derived automatically (`github` if present, otherwise `none`)** {#SC-3}

In full mode, `RepoConfig.EffectiveCIMode()` defaults an unset `ci_mode` (`""`) unconditionally to `CIModeGitHub` (`pkg/model/repo_config.go`). On a newly created personal repo or prototype with no `.github/workflows/*.yml`, `CIModeGitHub` leaves green PRs waiting for checks that never arrive unless the user knows to visit `/repos/<o>/<n>/settings/ci` and set `ci_mode: none`.

In `simple` mode, `ci_mode` is **derived per branch/PR** without user configuration (`deriveSimpleCIMode`):
1. If the repository is hosted on GitHub **and** either (a) the target branch or PR head contains at least one `.github/workflows/*.{yml,yaml}` file **or** (b) GitHub reports one or more check runs or commit statuses on the head SHA (or base branch's required status checks), effective CI mode is **`CIModeGitHub`** (`"github"`): Grain waits for all reported checks to pass and auto-repairs failures as usual.
2. Otherwise (a repository with no `.github/workflows/` and zero check runs/statuses, or a plain SSH/non-GitHub git host with no check API), effective CI mode is **`CIModeNone`** (`"none"`): a clean, non-conflicting PR merges without hanging indefinitely waiting for checks that do not exist, while the agent still runs tests directly inside its sandbox before pushing.
3. Adding `.github/workflows/ci.yml` in a task PR immediately activates `CIModeGitHub` on that very PR (condition 1a holds on the PR's head tree) with zero settings changes.

### 3.12 Browser preferences and standing Director chat: **Kept**

Browser-local display preferences (`grain.themeMode`, `grain.timeZone`, `grain.timelineOrder`, `grain.inboxNotifications` in `localStorage`; `settings.spec.md` §11 `NOT_CONFIG`) affect only the current browser and stay available to every signed-in user (`admin`, `writer`, `reader`) under `/settings/preferences`.

---

## 4. Allowed users and RBAC integration {#SC-4}

### 4.1 Dependency: Grain RBAC ([Task #2764](/tasks/2764))

Grain has no roles in code today: every principal admitted by `-ui-allowed-principals` or the access code has full operator authority (`authentication.design.md`, `operator-attribution.design.md`). Role-based access control is being specified in [Task #2764](/tasks/2764) (`design/design/rbac.design.md` and `design/spec/rbac/rbac.spec.md`), which defines three human roles: **`admin`**, **`writer`**, and **`reader`**.

**`design/design/rbac.design.md` ([Task #2764](/tasks/2764)) is a direct dependency of simple mode's Users role selector and route-level role gates** (see §13). To keep architectural boundaries clean, **this document does not redefine the RBAC model itself** (role resolution internals, audit trails, or token delegation belong in `design/design/rbac.design.md` and `design/spec/rbac/rbac.spec.md`). Instead, §4.2–§4.3 define **what simple mode needs from `rbac.design.md` and the exact hooks simple mode wires to its `admin`, `writer`, and `reader` roles**, and note the token-minting authorization question from [`authentication.design.md`](authentication.design.md) (§3; [`authentication.spec.md`](../spec/authentication/authentication.spec.md) `{#AU-11}`) that belongs to `rbac.design.md`:
- **Open question owned by `design/design/rbac.design.md`:** *Who may mint `gma_` external-agent tokens (`/api/external-agents/tokens`) and `hostd` registration tokens (`/api/hostds`)?* Simple mode locks both surfaces (`externalAgentApi: false`, Hostds hidden), and expects `rbac.design.md` to restrict both to `admin` in full mode because both grant execution or API authority across the deployment.

### 4.2 How simple mode maps onto `admin`, `writer`, and `reader`

Simple mode uses `rbac.design.md`'s three roles directly on each allowed-user entry, and distinguishes between the **RBAC role `admin`** (a permission level one or more trusted people can hold) and the **deployment / registry instance owner** (`-deployment-owner`, the single verified owner from `grain-registry` who is permanently pinned to `admin`):

| Role (`rbac.design.md`) | Tasks, Inbox, & Director chat | Simple-mode Settings, Connections, Users, Secrets, Upgrade, & `config_mode` |
| --- | --- | --- |
| **`admin`** | Full access (file, edit, approve, cancel, comment, answer `ask_question`, merge/retrigger, chat with Director). | **Editable** (within the active `config_mode`): edit model & git connections, `targetRepos`, allowed users & roles (`/api/access/principals`), write-only task secrets (`PUT /api/secrets/…` and `PUT /api/tasks/{id}/secret`), trigger `Upgrade`, and switch `config_mode` (`simple ↔ full`). |
| **`writer`** | **Full day-to-day work:** file, edit, approve, cancel, reopen, and comment on tasks; answer `ask_question`; chat with Director; acknowledge Inbox notifications; trigger PR merge/retrigger. | **Hidden / refused (`403`):** cannot edit model or git connections, `targetRepos`, allowed users, deployment secrets (`PUT /api/tasks/{id}/secret` writes deployment-wide secrets, so setting/overwriting secrets requires `admin` unless `rbac.design.md` defines write-once task-scoped secrets), `Upgrade`, or `config_mode`. Settings shows only browser-local **Preferences** and signed-in **Access** info. |
| **`reader`** | **Read-only:** view tasks, diffs, transcripts, CI logs, repositories, and Inbox. All task/chat/inbox mutations return `403`. | **Hidden / refused (`403`):** same as `writer` (browser **Preferences** and **Access** only; all mutating API calls return `403`). |

Role-resolution invariants from `rbac.design.md` that simple mode relies on:
1. **Most-permissive matching role wins (`admin > writer > reader`).** If `domain:example.com` has `reader`, `group:eng@example.com` has `writer`, and `user:alice@example.com` has `admin`, `alice@example.com` resolves to `admin` (never downgraded by a broader group or domain rule).
2. **Local break-glass access code resolves to `admin`.** Loopback / break-glass `access-code` sessions (`operator (access code)`) carry `admin` authority so SSH break-glass recovery (`hub.design.md`, *The access code on registry-created instances*) can always repair a broken allowlist.

### 4.3 Hooks simple mode defines

1. **Database-backed `allowed_principal` entries with `role`.**
   Allowed principals live in the database `allowed_principal` table (exposed at `GET`/`POST`/`DELETE`/`PATCH /api/access/principals` and rendered at `/settings/users`):
   ```go
   // Role values defined by design/design/rbac.design.md (Task #2764):
   type Role string
   const (
       RoleAdmin  Role = "admin"
       RoleWriter Role = "writer"
       RoleReader Role = "reader"
   )

   type AllowedPrincipal struct {
       Principal string `json:"principal"` // "user:<email>", "group:<email>", "domain:<domain>"
       Role      Role   `json:"role"`      // "admin", "writer", or "reader"
   }
   ```
   - **Out of `staterepo.SettingsTables`, persisted in SQL.** Keeping `allowed_principal` in the database (Shared Cloud SQL on registry instances; SQLite/PostgreSQL elsewhere, included in `staterepo.BackupOnlyTables` cold dumps) rather than `grain/config` ensures a config-repository pull request can never lock `admin`s out or silently admit an attacker (`settings.design.md` §1), while still surviving VM replacement on registry instances (§9.1).
   - **Validation** reuses `ui.ParseAllowedPrincipals` and `ui.RejectGCPKeyPoolAllowedPrincipals` (`pkg/ui/allowlist.go`), supporting `user:<email>`, `group:<email>`, and `domain:<domain>` entries (`authentication.design.md`).
   - **Startup flag migration:** `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS` seeds initial rows with `role: "admin"` when `allowed_principal` is empty, preserving existing deployments' behaviour.
2. **Immutable instance-owner floor (`-deployment-owner`, no-lockout invariant).**
   - A startup flag `-deployment-owner=<email>` (`$GRAIN_DEPLOYMENT_OWNER`, populated by `grain-registry` from `entries.owner` in VM metadata and optionally by `grain init`) names the deployment's pinned owner.
   - When evaluating admission and RBAC roles, the server **always** merges `{Principal: "user:" + deploymentOwner, Role: RoleAdmin}` in memory as an immutable floor—regardless of what `allowed_principal` rows exist in the database. Even an empty table cannot lock out the registry owner.
   - Mutations on `/api/access/principals` refuse with `422 Unprocessable Entity` any change that: (a) attempts to delete or demote `user:<deploymentOwner>` below `admin`, (b) leaves zero `user:` entries with role `admin`, or (c) removes or demotes the calling human `admin`'s own `admin` access unless `-deployment-owner` still covers them.
   - When ownership of a registry entry is transferred (`hub.design.md`, *Authorization rules*), updating `entries.owner` updates `-deployment-owner` on the instance and grants the new owner the pinned `admin` floor.
3. **Route permission hook & `GET /api/whoami`.**
   - Simple-mode settings, connection, repository, user, secret, upgrade, and `configMode` routes enforce RBAC's `admin` permission check (`403 Forbidden`, `code: "insufficient_role"`) ahead of `config_refusal.go`'s mode/lock checks; task creation, approval, comments, and Director chat enforce `writer` (`admin` or `writer`).
   - `GET /api/whoami` (`operator-attribution.design.md`) reports `"role": "admin" | "writer" | "reader"` and `"deploymentOwner": "<email>"` so the UI hides `admin`-only Settings sections from `writer`s and `reader`s and renders the lock badge on the registry owner's row.

---

## 5. The locked defaults: `pkg/model/profiles/simple.v1.yml` {#SC-5}

### 5.1 Declared, versioned profile (not scattered constants)

Every setting's simple-mode disposition lives in **one embedded, versioned YAML file**: `pkg/model/profiles/simple.v1.yml`, loaded via `//go:embed` into `model.SimpleProfile()` (`ProfileVersion: 1`).

Why an embedded, versioned profile in `pkg/model/profiles/` (materialised onto `grain_config` / `grain/config` when simple mode is active) beats scattered `if cfg.ConfigMode == Simple` literals or a user-editable file in `grain/config`:
1. **Single auditable source of truth.** Reviewers can read one ~180-line YAML file to see every pinned value, every derived rule, every enabled system schedule, and the entire New Task form layout.
2. **Compile/test-time exhaustiveness (`TestSimpleProfileCoversEverySetting`).** Using reflection over `model.Config`, `model.RepoConfig`, `ui.UpdateSettingsRequest`, `model.NewTaskFormFieldIDs`, `model.SystemTemplates`, and `staterepo.SettingsTables`—the exact pattern `pkg/ui/settings_restart_drift_test.go` and `TestEveryTableIsEitherSettingsOrDeliberatelyNot` already use—adding a new setting anywhere in Grain **fails `go test ./pkg/model/...`** until `simple.v1.yml` classifies it as `editable`, `derived`, or `pinned`.
3. **Clean upgrades.** When a future Grain release tunes a simple-mode default in `simple.v2.yml`, upgrading a simple-mode instance applies the updated profile cleanly on startup without disturbing editable state (`agentBackends`, `defaultModel`, `gitConnections`, `targetRepos`, `allowed_principal`, and secrets).

Shape of `pkg/model/profiles/simple.v1.yml`:

```yaml
profile: simple
version: 1

grain_config:
  editable:
    - config_mode           # human admin only (never PrincipalAgent)
    - agent_backends        # simple subset: id, name, framework (claude|codex|antigravity), models, excludedModels, authMode, credentialBackendId, disabled
    - default_model
    - git_connections
    - git_credential
    - agent_git_name
    - agent_git_email
    - target_repos
  pinned:
    agent_backend_id: ""
    model_settings: []
    judgement_model: ""
    agent_framework: ""
    cli_run: {}
    poll_interval_ms: 10000          # 10s (DefaultPollInterval)
    max_workers: 4                   # conservative personal/small-team concurrency
    max_mergers: 1                   # DefaultMaxMergers
    max_merges_per_repo: 1           # DefaultMaxMergesPerRepo
    max_ci_jobs: 1                   # DefaultMaxCIJobs
    max_agent_turns: 0               # framework default (bounded by 2h run deadline)
    sandbox_backend: ""              # resolved from daemon startup flags
    sandbox_cpus: 0                  # backend default (2 vCPU)
    sandbox_memory_mb: 0             # backend default (8192 MiB)
    sandbox_disk_gb: 0               # backend default
    sandbox_cpu_overcommit: 2.0      # DefaultCPUOvercommit
    sandbox_memory_overcommit: 1.0   # DefaultMemoryOvercommit
    sandbox_disk_overcommit: 2.0     # DefaultDiskOvercommit
    sandbox_memory_reserve_mb: -1    # 1/8 of host RAM, min 2 GiB
    sandbox_disk_floor_share: 0.10   # DefaultDiskFloorShare
    sandbox_disk_floor_min_mb: 5120  # MinDiskFloorMB (5 GiB)
    sandbox_overcommit: { mode: "fixed" }
    network_access: ""               # deployment default ("open" unless restricted by flag)
    sandbox_image: ""                # build-stamped guest image
    sandbox_image_mirror: ""
    session_sandbox_idle_ttl_ms: 1800000 # 30m (DefaultSessionSandboxIdleTTL)
    k8s_namespace: ""                # startup/default ("grain")
    k8s_kubectl: ""
    k8s_sandbox_image: ""
    k8s_granule_image: ""
    k8s_workspace: ""
    k8s_git_proxy_host: ""
    k8s_sandbox_service: ""
    k8s_service_account: ""
    k8s_node_selector: []
    k8s_privileged: false
    k8s_network_policy: false
    k8s_deployment: ""
    k8s_container: ""
    k8s_priority_class_interactive: "grain-interactive"
    k8s_priority_class_agent: "grain-agent"
    k8s_priority_class_ci: "grain-ci"
    github_host: ""                  # defaults to github.com; per-connection host used
    github_insecure_http: false
    gcp_project: ""
    gcp_service_account_email: ""
    gcp_service_account_count: 0
    iap_backend_service: ""
    iap_resource: ""
    external_agent_api: false
    package_cache_enabled: true
    package_cache_size_gb: 0         # default (20 GiB)
    actions_cache_enabled: true
    actions_cache_size_gb: 0         # default (20 GiB)
    environment_name: ""
    show_closed_by_default: false
    settings_locked: false           # superseded while config_mode == "simple"
    time_zone: "UTC"                 # browsers override locally via Preferences
    upgrade_check_mode: "auto"
    upgrade_check_pauses: false
    auto_upgrade_channel: "latest"   # active on standalone installs; no-op under -disable-self-upgrade on registry VMs
    auto_upgrade_time: "03:00"
    newest_first: false
    approved_by_default: true
    auto_merge_by_default: true
    default_capabilities: []
    max_auto_submit_depth: 2
    max_auto_submit_fan_out: 5
    default_filters: "f=state:none:deferred|closed&f=kind:none:interactive"
    prompt_extension: ""
    task_branch_template: "grain/{deployment}/task-{id}-{slug}"
    deployment_id: ""                # keeps auto-generated installation deployment ID
    console_sessions: null           # default single Director session
    repair_template_id: ""
    mcp_servers: {}                  # EnsureBuiltins populates in-tree builtins only
    capability_policy: {}
    base_ci_red_streak: 0            # default (2)
    ci_jobs_per_run: 0               # unbounded per run
    ci_age_boost_ms: 0               # default (2m)
    ci_reserve_after_ms: 0           # default (1m)
    ci_run_history: 0                # default (5000)
    ci_step_output_mb: 0             # default (64 MiB)
    ci_step_output_days: 0           # default (14d)
    ci_run_output_kb: 0              # default
    ci_cache_mb: 0                   # default (256 MiB)
    ci_cache_entries: 0              # default (4096)
    ci_cache_days: 0                 # default (30d)
    ci_artifact_mb: 0                # default (2048 MiB)
    ci_artifact_run_mb: 0            # default (128 MiB)
    ci_artifact_file_mb: 0           # default (32 MiB)
    ci_artifact_days: 0              # default (14d)

access:
  editable_tables:
    - allowed_principal              # human admin only; deploymentOwner floor enforced

repo_config:
  editable:
    - git_credential                 # per-repo git connection binding when multiple connections exist
  derived:
    ci_mode: github_if_present_else_none
  pinned:
    clone_url: ""
    default_capabilities: []
    ci_capabilities: []
    ignore_github_ci: false
    watch_default_branch: false
    require_branch_up_to_date: false
    merge_on_green: true             # land clean green PRs without head-of-line blocking
    merge_recheck: "off"
    base_repair_bypasses_human_gates: false
    unfixable_checks: []
    declarations: false              # .grain/* ignored in simple mode
    prompt_extension: ""
    setup_command: ""
    issue_import: { enabled: false }
    repair_template_id: ""
    work: {}                         # default ownership (base-ci/triage on; release off unless configured)
    base_branch_ci_jobs: []
    post_merge_dispatch_jobs: []
    release_secrets: []
    port_branches: []
    port_types: {}

system_schedules:
  enabled:
    - system:pipeline-steward
    - system:memory-agent
  disabled:
    - system:infra-review
    - system:release-notes
    - system:steward-audit
```

### 5.2 Complete setting-by-setting inventory (`settings.spec.md` cross-reference)

Every surface in [`settings.spec.md`](../spec/settings/settings.spec.md) §3–§8 is accounted for below:

| `settings.spec.md` section | Surface / fields | Simple-mode status & value |
| --- | --- | --- |
| **§3.1 Models** | `agentBackends` (`claude`, `codex`, `antigravity`; `id`, `name`, `framework`, `models`, `excludedModels`, `authMode`, `credentialBackendId`, `disabled`), credentials, `defaultModel` / `defaultModels` | **Editable (`admin`)** |
| **§3.1 Models (advanced)** | Backend `concurrency` (`4`), `cliPath` (`""`), `custom` `command`, `modelTurnTimeout` / `modelRetryCutoff` (`0`), `modelSettings` (`[]`), `agentBackendId` (`""`), `judgementModel` (`""`), `cliRun` (`{}`) | **Pinned** |
| **§3.2 Sandboxes** | `sandboxBackend` (`""`), `sandboxCpus` (`0`), `sandboxMemoryMb` (`0`), `sandboxDiskGb` (`0`), `networkAccess` (`""`), `sessionSandboxIdleTtl` (`30m`), `sandboxImage` (`""`), `sandboxImageMirror` (`""`), `sandboxOvercommit` (`fixed`), `sandbox{Cpu,Memory,Disk}Overcommit` (`2.0`/`1.0`/`2.0`), `sandboxMemoryReserveMb` (`-1`), `sandboxDiskFloorShare` (`0.10`), `sandboxDiskFloorMinMb` (`5120`), all `k8s*` | **Pinned** (resolved from VM/cluster startup flags + dynamic host budget) |
| **§3.4 Secrets / Legacy capabilities** | Secret values (`pkg/secrets` via `PUT`/`DELETE /api/secrets/…` and `PUT /api/tasks/{id}/secret`) | **Editable (`admin`, write-only)** |
| **§3.4 Legacy capabilities (config)** | `githubHost` (`""` → `github.com`), `githubInsecureHttp` (`false`), `gcpProject` (`""`), `gcpServiceAccountEmail` (`""`), `gcpServiceAccountCount` (`0`), `settings.capabilities` catalogue | **Pinned** |
| **§3.5 Access & Hostds** | `allowed_principal` (`user:`, `group:`, `domain:` + RBAC role `admin` / `writer` / `reader`) | **Editable (`admin`)**; `deploymentOwner` (`admin`) floor non-removable. `externalAgentApi: false`; `/api/hostds` token management locked (VM's own local `hostd` uses `-hostd-token-file`). |
| **§3.6 & §3.10 Upgrades** | `autoUpgradeChannel` (`"latest"`), `autoUpgradeTime` (`"03:00"`), `upgradeCheckMode` (`"auto"`), `upgradeCheckPausesPipeline` (`false`); manual `POST /api/upgrade` (standalone) or registry `POST /api/entries/<name>/upgrade` (`-disable-self-upgrade`) | Policy **Pinned**; manual **Upgrade to latest** action **Open (`admin`)** |
| **§3.7 Caches** | `packageCacheEnabled` (`true`), `packageCacheSizeGb` (`0` → 20 GiB), `actionsCacheEnabled` (`true`), `actionsCacheSizeGb` (`0` → 20 GiB) | **Pinned** (flush actions remain available on `/system` if needed, hidden from Settings) |
| **§3.8 Engine** | `pollInterval` (`10s`), `maxWorkers` (`4`), `maxMergers` (`1`), `maxMergesPerRepo` (`1`), `maxCIJobs` (`1`), retired `mergeBatch*` | **Pinned** |
| **§3.9 Config repo** | `state-repo.json` UI (`POST /api/state-repo/remote`, `/pull-requests`) | **Locked in UI/API** (registry/bootstrap write-through mirror runs automatically; §9) |
| **§3.10 General** | `configMode` (`"simple"`), `environmentName` (`""`), `showClosedByDefault` (`false`), `settingsLocked` (`false`), `timeZone` (`"UTC"`), `deploymentId` (preserved auto-generated ID, never wiped) | `configMode` **Editable (`admin`)**; `deploymentId` preserved; rest **Pinned** |
| **§3.11 Git** | `gitConnections`, `gitCredential`, `repo_config.git_credential`, `agentGitName`, `agentGitEmail` | **Editable (`admin`)** |
| **§3.12 Repositories** | `targetRepos` | **Editable (`admin`)** |
| **§3.13 Tasks** | `newestFirst` (`false`), `approvedByDefault` (`true`), `autoMergeByDefault` (`true`), `defaultCapabilities` (`[]`), `maxAutoSubmitDepth` (`2`), `maxAutoSubmitFanOut` (`5`), `defaultFilters` (default), `custom_field` table (`[]`) | **Pinned** |
| **§3.14 Agents** | `maxAgentTurns` (`0`), `promptExtension` (`""`), `taskBranchTemplate` (`"grain/{deployment}/task-{id}-{slug}"`), `consoleSessions` (`nil` → Director), `repairTemplateId` (`""`), `agentFramework` (`""`) | **Pinned** |
| **§3.15 Capabilities** | `mcpServers` (builtins only), `capabilityPolicy` (`{}`), `api_scope_set` (`[]`) | **Pinned** |
| **§3.16 CI** | All 14 `ci*` retention/queue/cache/artifact columns (`0` → Grain defaults), `baseCIRedStreak` (`0` → 2) | **Pinned** |
| **§3.17 Memory** | `memory` and `memory_document` tables | **Daemon-managed** (`system:memory-agent` writes; manual `PUT /api/memory*` locked) |
| **§6–§7 Per-repo & `.grain/*`** | `repo_config` (all columns except `git_credential`), `.grain/*` (`declarations: false`) | `ci_mode` **Derived**; `merge_on_green: true`; rest **Pinned** |
| **§8 Composed entities** | `template*`, `suite*`, `board*`, `custom_field`, `qualification_*`, `repo_workflow`, `gcp_key`, `standing_sandbox*`, `persistent_sandbox` | **Pinned empty / paused** (`schedule*` holds only the seeded system rows, with only `pipeline-steward` and `memory-agent` enabled) |
| **§11 Browser preferences** | `grain.themeMode`, `grain.timeZone`, `grain.timelineOrder`, `grain.inboxNotifications` | **Editable per browser** (`localStorage`, `NOT_CONFIG`) |

### 5.3 Built-in `SimpleNewTaskForm` (`settings/new-task-form.yml` layer)

When `config_mode == "simple"`, `Config.EffectiveNewTaskForm()` returns the `simple.v1` `NewTaskForm` below (ignoring any `grain_config.new_task_form` column):

```yaml
default:
  fields:
    title:               { visibility: shown, order: 1 }
    description:         { visibility: shown, order: 2 }
    repo:                { visibility: shown, order: 3, allow_no_repo: false, allow_change_targets: false }
    attachments:         { visibility: shown, order: 4 }
    base:                { visibility: more,  order: 5 }
    model:               { visibility: more,  order: 6 }
    autoMerge:           { visibility: more,  order: 7, default: true }
    approved:            { visibility: more,  order: 8, default: true }
    dependsOn:           { visibility: more,  order: 9 }
    # Every advanced field is hidden and takes its safe default on POST /api/tasks:
    newBranch:           { visibility: hidden }
    writes:              { visibility: hidden, default: [] }
    reads:               { visibility: hidden, default: [] }
    noPort:              { visibility: hidden, default: false }
    fixesBaseBranch:     { visibility: hidden, default: false }
    grants:              { visibility: hidden, default: [] }
    legacyCapabilities:  { visibility: hidden, default: [] }
    agentBackendId:      { visibility: hidden, default: "" }
    networkAccess:       { visibility: hidden, default: "" }
    nothingNeededAction: { visibility: hidden, default: "close" }
    reviewTemplateId:    { visibility: hidden, default: "" }
    promptExtension:     { visibility: hidden, default: "" }
    hostd:               { visibility: hidden, default: "" }
    sandboxShape:        { visibility: hidden }
    customFields:        { visibility: hidden }
    atFront:             { visibility: hidden, default: false }
  capabilities:
    defaults: []
    offered:  []
```

---

## 6. Enforcement: building on `pkg/ui/config_refusal.go` and `pkg/staterepo`

Simple mode **does not invent a parallel middleware**. It extends the central guard layer in `pkg/ui/config_refusal.go`, `pkg/ui/config_bootstrap.go`, and `pkg/staterepo/check.go` (`settings.design.md` §2, `settings-mutability.spec.md` §1.2).

### 6.1 HTTP & MCP API enforcement (`409 locked_in_simple_mode`) {#SC-6}

1. **`clearSimpleEditable` beside `clearBootstrap` (`pkg/ui/config_simple.go`).**
   Just as `clearBootstrap` zeroes bootstrap fields so `bootstrapOnlyChange(before, after)` detects non-bootstrap edits on `PUT /api/settings` and `PUT /api/deployment`, `clearSimpleEditable(c *model.Config)` zeroes only the simple-editable fields (`ConfigMode`, the simple subset of `AgentBackends`, `DefaultModel`, `GitConnections`, `GitCredential`, `AgentGitName`, `AgentGitEmail`, `TargetRepos`).
   - For `AgentBackends`, `normalizeSimpleBackends` also checks that no backend sets `Framework == "custom"`, `Command != ""`, `CLIPath != ""`, non-default `Concurrency`, or custom `ModelTurnTimeout` / `ModelRetryCutoff`.
   - `simpleEditableChange(before, after)` runs the same `comparableConfig` + `pruneEmpty` comparison as `bootstrapOnlyChange`. Mixed requests (such as `{"defaultModel": "claude-opus-5-5", "maxWorkers": 16}`) fail as a whole without writing the allowed half (`settings-mutability.spec.md` §2.3.5).
2. **Route guard extension in `pkg/ui/config_refusal.go`.**
   Every mutating settings/entity route is classified against simple mode:
   - **Allowed for `admin` in simple mode:**
     - `PUT`/`POST /api/settings`, `PUT /api/config`, `PUT /api/deployment` — when `simpleEditableChange(before, after)` holds (and if `ConfigMode` changed, caller is `PrincipalHuman` with `RoleAdmin`).
     - `PUT`/`DELETE /api/deployment/agent-backends/{id}` (validated for simple backend fields) and credential routes (`PUT`/`DELETE /api/agent-backends/{id}/credential`, `/codex-auth`, `/api/agent-keys/{framework}`, `POST /api/agent-keys/{framework}/check`).
     - `POST`/`DELETE /api/deployment/git-connections[/{cred}]`, `POST /api/deployment/git-connections/check`, `PUT /api/deployment/repos/{o}/{n}/git-connection`.
     - `POST /api/repos`, `DELETE /api/repos/{o}/{n}`.
     - `POST`/`PATCH`/`DELETE /api/access/principals` (human `RoleAdmin` only; enforces `-deployment-owner` floor).
     - `PUT`/`DELETE /api/secrets/{secret}[/{key}]` and `PUT /api/tasks/{id}/secret`.
     - `POST /api/upgrade` (with no custom branch/image override on standalone installs; upgrades to `latest`).
   - **Refused in simple mode (`409 Conflict`):** Every other **S**, **S·u**, **B**, **B·u**, and **G** route (`PUT /api/repos/{o}/{n}/settings` and single-field repo routes, `PUT /api/settings/cli-run`, `/api/capability-*`, `/api/api-scope-sets*`, `/api/console-sessions*` create/edit/delete, `/api/templates*`, `/api/schedules*`, `/api/suites*`, `/api/boards*`, `/api/custom-fields*`, `/api/gcp-keys*`, `/api/standing-sandboxes*`, `/api/persistent-sandboxes*`), plus the previously unguarded routes that mutate non-simple config: `PUT /api/memory*`, `POST /api/external-agents/tokens*`, `POST`/`DELETE /api/hostds*`, and `POST /api/state-repo/{remote,pull-requests}`.
   - **Wire response (`SimpleModeLockedCode = "locked_in_simple_mode"`):**
     ```json
     {
       "code": "locked_in_simple_mode",
       "setting": "maxWorkers",
       "error": "maxWorkers is locked in simple mode; switch to full mode in Settings to change it."
     }
     ```
     Unlike `config_managed_externally`, this refusal carries **no** rehearsed config-repo patch, because the config repository is not a backdoor for changing locked settings in simple mode.
3. **No bypass via `directConfigWrites` or `settings_locked`.**
   `state-repo.json`'s `directConfigWrites: true` only relaxes `settings_locked` (`settings-mutability.spec.md` §2.5); it never bypasses `config_mode: simple`. While `config_mode == "simple"`, UI writes to the allowed areas commit directly to the database and call `StateManager.SyncAfterWrite` to keep `grain/config` current.
4. **Agents can never unlock or write locked settings.** {#SC-7}
   - `PrincipalAgent` hits the identical `409 locked_in_simple_mode` guard on every locked setting.
   - `PrincipalAgent` is refused with `403 person_required` on `configMode` and `/api/access/principals`, and in simple mode no task can hold `Dangerous` `api:*:write` scopes (`SimpleNewTaskForm` hides `grants`, `api_scope_set` is empty, and `POST /api/tasks` rejects privileged capability grants on simple-mode task creation except on internal system templates).
   - Grain's own internal reconciler and `system:memory-agent` store writes carry no HTTP `Principal` / `WriteRehearsal` and proceed normally, matching `settings-mutability.spec.md` §1.3.

### 6.2 Config repository handling in simple mode {#SC-8}

We have three options for commits/PRs against `grain/config` (whether local or pushed to an instance's private config repository; §9.1) while `config_mode == "simple"`:
1. *Allowed with a warning* — breaks the lock: a config PR or stray commit could change `max_workers` or `prompt_extension` behind a UI that hides those controls.
2. *Ignored silently on import* — breaks `staterepo` fidelity (`settings.design.md` core rule): `grain/config` and the database diverge silently.
3. **Refused at Grain's config gates + defensively clamped on external `Apply` with a warning notification (Recommended).**

**Recommendation: Option 3.** Specifically:
- **Refused at every Grain-controlled Git/PR gate.** `staterepo.Check` (called by `ProposeConfigChange` in `pkg/daemon/config_change.go`, `POST /api/state-repo/pull-requests`, `grain state check`, and the git proxy's push validation on `grain/config`) runs `checkSimpleModeProfile(baseDir, candidateDir)`: if `base` or `candidate` has `config_mode == "simple"`, any diff touching a locked `grain_config` field, a locked `repo_config` field, a locked `settings/*.yml` file, or `config_mode` itself fails validation with `"locked in simple mode; switch to full mode in Settings to change <field>"`. Meanwhile, `StateManager.SyncAfterWrite` continues to export the allowed simple-mode edits (`agent_backends`, `default_model`, `git_connections`, `target_repos`, and `system:memory-agent`'s `memory/**`) to `grain/config` so the config repository stays an exact, auditable mirror.
- **Clamped on out-of-band external `Apply` so sync never wedges.** If an operator has direct GitHub access to the private config repository and pushes a hand-edited locked field bypassing Grain's proxy, refusing the entire `staterepo.Apply` tick would wedge all future imports (`pkg/staterepo/replaced.go` lines 49–57). Instead, when `config_mode == "simple"`, `staterepo.Apply` imports the allowed fields, clamps locked fields back to `simple.v1.yml`, logs a `staterepo: simple mode clamped ...` journal entry, raises/updates a warning notification (`config.simple-mode-clamped`) in the Inbox, and re-exports on the next sync.

---

## 7. Switching modes (`simple ↔ full`) {#SC-9}

### 7.1 `simple → full`

When a human `admin` switches `config_mode` from `"simple"` to `"full"` (`PUT /api/settings {"configMode": "full"}`):
1. **All current values are preserved.** Model connections, `defaultModel`, git connections, commit identity, `targetRepos`, `allowed_principal` entries and roles, task secrets, `deploymentId`, and `memory/**` remain untouched.
2. **Previously pinned values become ordinary editable values.** For instance, `max_workers` (`4`) and `auto_upgrade_channel` (`"latest"`) stay at those values in `grain_config` until an `admin` edits them under `/settings/engine` or `/settings/general`.
3. **Full UI and system schedules unlock live.** All sixteen Settings sections, per-repo settings tabs, Boards, Processes, Config Repo Browser, and the full New Task form appear immediately with no daemon restart. On the next reconcile tick, `reconcileSystemSchedules` re-enables `system:infra-review`, `system:release-notes`, and `system:steward-audit`.

### 7.2 `full → simple`

There are two ways `full → simple` could treat existing customisations:
- *Alternative A (keep customised values and lock them):* leaves `max_workers: 25`, a custom `setupCommand`, `declarations: true`, `autoMergeByDefault: false`, or an hourly user schedule running in the background while **hiding every UI page that shows they exist**. The instance becomes "haunted": its actual behaviour no longer matches simple mode, and neither the `admin` nor support can tell why tasks fail or why tokens burn overnight without switching back to `full`.
- **Alternative B — Recommended (reset locked settings to `simple.v1` defaults, with an explicit preview confirmation and a pre-switch Git snapshot):**

How **Alternative B** works:
1. **Dry-run preview (`POST /api/settings/plan {"configMode": "simple"}`).** Returns `simpleModeReset`: the structured list of every non-default `grain_config` field, `repo_config` row, and active user schedule / standing sandbox that differs from `simple.v1.yml`.
2. **Confirmation dialog in UI.** Lists every item that will change (for example: *"Max workers: 12 → 4; `acme/api` custom setup script cleared; 2 custom schedules paused"*) and requires clicking **Reset to defaults & switch to simple mode**.
3. **Pre-switch snapshot in `grain/config`.** Before mutating the store, `StateManager` commits (and pushes, if a remote config repo is configured) the current `full` export to `grain/config` (`"Snapshot full configuration before switching to simple mode"`), so every customisation, prompt extension, template, and script remains recoverable in Git history even after the reset.
4. **Atomic reset.** In one database transaction:
   - `grain_config` locked columns are set to `simple.v1.yml` values (preserving `agentBackends` [clamping any advanced per-backend fields to simple defaults], `defaultModel`, `gitConnections`, `gitCredential`, `agentGitName`, `agentGitEmail`, `targetRepos`, and `deploymentId`; `allowed_principal` and secrets are untouched).
   - `repo_config` rows clear all columns except `Repo` and `GitCredential` (`Empty()` rows with no `GitCredential` are deleted per `PutRepoConfig`'s normal rule).
   - User-created `schedule` and `standing_sandbox` rows have `enabled` set to `false` (paused rather than deleted, so nothing fires invisibly while simple mode is on, and switching back to `full` lets an `admin` re-enable them without recreating them; templates, suites, and boards stay inert in their tables while hidden).
   - `StateManager.SyncAfterWrite` commits the resulting simple state to `grain/config`.

---

## 8. First-run flow {#SC-11}

Today (`ui/src/alerts.js`, `deploymentSetupStepsLeft`), a fresh deployment drops the user onto an empty task list with a corner alert reading *"3 setup steps left"*, requiring visits to three separate pages (`/settings/git`, `/settings/models`, `/repos`).

In `simple` mode, when the signed-in user has role `admin`, zero tasks exist yet, and `deploymentSetupStepsLeft` is non-empty (unless the `admin` clicks *Skip to task list*), `/` renders a **guided 3-step Setup flow** directly in the main pane:

```
┌──────────────────────────────────────────────────────────────────────┐
│  grain                                          alice@example.com ▾  │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│   Set up your Grain instance                              Step 1 / 3 │
│   ─────────────────────────────────────────────────────────────────  │
│                                                                      │
│   ● 1. Connect a model        ○ 2. Connect Git      ○ 3. Pick repos  │
│                                                                      │
│   Choose an AI provider to run tasks:                                │
│                                                                      │
│   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐      │
│   │ Claude          │  │ Gemini          │  │ Codex (OpenAI)  │      │
│   │ OAuth or key    │  │ API key / OAuth │  │ Sign in or key  │      │
│   └─────────────────┘  └─────────────────┘  └─────────────────┘      │
│                                                                      │
│   API key or OAuth token:  [ sk-ant-•••••••••••••••••••• ] [ Verify ]│
│   Default model:           [ claude-sonnet-4-6                ▾ ]    │
│                                                                      │
│   [ Skip setup for now ]                        [ Save & continue → ]│
└──────────────────────────────────────────────────────────────────────┘
```

- **Step 1 — Model connection:** Pick Claude, Gemini (`antigravity`), or Codex; authenticate via OAuth or paste a key (`POST /api/agent-keys/{framework}/check` verifies live); auto-selects a sensible `defaultModel` from the verified connection.
- **Step 2 — Git connection:** Connect a GitHub App or PAT (or other git host), verify via `POST /api/deployment/git-connections/check`, with an optional collapsed *Custom commit name / email* disclosure.
- **Step 3 — Repositories:** Shows the searchable checklist from `GET /api/deployment/reachable-repos` (plus manual `owner/name` entry). Selecting one or more repos writes `targetRepos`.
- **Finish → Task list:** Clicking **Finish setup** lands on `/` (the task list) with the streamlined **New Task** button ready (`SimpleNewTaskForm` pre-selecting the repository). Setup is stateless and resumable: refreshing after Step 1 opens directly on Step 2 because step completion is derived from `/api/config` (`deploymentSetupStepsLeft`). If a `writer` or `reader` signs in before setup finishes, they see a calm *"Waiting for an admin to finish connecting models and Git"* empty state rather than broken setup controls.

---

## 9. Interaction with replaceable registry instances {#SC-13}

### 9.1 Where simple-mode config and credentials live on registry VMs

Per [`hub.design.md`](hub.design.md) (*Replaceable instances and externalised state*), every registry VM has a disposable local scratch disk (`auto_delete: true`) and stores all durable state across four externalised backing services provisioned per instance (`<name>`):

| State in simple mode | Where it lives on a registry instance (`hub.design.md`) | Why |
| --- | --- | --- |
| **`config_mode`**, **`agentBackends`** (non-secret metadata), **`defaultModel`**, **`gitConnections`** (non-secret metadata), **`agentGitName` / `agentGitEmail`**, **`targetRepos`**, **`repo_config.git_credential`** | **1. Shared Cloud SQL for PostgreSQL** (`entries.db_name`, reached via `cloud-sql-proxy` with IAM database auth) **and** **2. Per-instance private GitHub config repo** (`<config-org>/<registry-id>-<name>`, exported via `StateManager.SyncAfterWrite`; or Cloud SQL alone when `config_repo.provider: none`). | Cloud SQL is the live transactional store; `StateManager.SyncAfterWrite` commits and pushes every allowed simple-mode UI edit (`mode: "mirror"`, `directConfigWrites: true`) to the instance's private config repo so `grain/config` stays an auditable Git backup without forcing the user through config PRs. |
| **Allowed users & RBAC roles (`allowed_principal`)** + instance owner floor (`-deployment-owner`) | **Shared Cloud SQL for PostgreSQL** (`allowed_principal` table in `db_name`) + `grain-config` VM metadata (`deployment_owner=<entries.owner>`). | Persists across VM replacements in Cloud SQL without exposing sign-in ACLs to `grain/config` Git edits; `-deployment-owner` guarantees the registry owner retains `admin` access even if the table were emptied. |
| **Model API keys / OAuth tokens, GitHub App private keys / PATs, and `request_secret` task secrets** | **Google Cloud Secret Manager** (`-secrets-backend=secretmanager`, `-secrets-sm-prefix=projects/<num>/secrets/grain-inst-<name>-`, IAM-scoped to the instance's dedicated service account), with non-secret index rows in Cloud SQL `secret_entries`. | Credentials never touch `grain/config` or local disk backups; IAM condition `resource.name.startsWith(".../grain-inst-<name>-")` isolates one instance's secrets from every other VM (`hub.design.md`, *Secrets in Secret Manager*). |
| **Tasks, conversations, memory (`system:memory-agent`), logs, and attachments** | **Shared Cloud SQL** (tasks, runs, memory) + **Cloud Storage Managed Folder** (`gs://<bucket>/instances/<name>/` for logs, transcripts, and attachments). | Survives VM replacement intact alongside configuration. |

### 9.2 What happens when a registry VM is replaced (`status = replacing`)

When `grain-registry` replaces an instance's VM—for a Grain version upgrade, a machine-shape change (`small` ↔ `medium` ↔ `large`), a zone move, or recovery of a broken guest (`hub.design.md`, *Instance lifecycle → Replace*)—the lifecycle works out of the box with simple mode:

1. The registry creates the replacement VM with the same `grain-config` metadata (`config_mode=simple`, `deployment_owner=<owner>`, `db_dsn` pointing at the existing `db_name` in Cloud SQL, `secrets_sm_prefix=projects/<num>/secrets/grain-inst-<name>-`, and `config_repo=git@github.com:<config-org>/<registry-id>-<name>.git`).
2. On boot, the new VM starts `cloud-sql-proxy`, connects to the existing Cloud SQL `db_name`, attaches to the existing `grain-inst-<name>-*` secrets in Secret Manager (including `config-repo-key`), and clones `<config-org>/<registry-id>-<name>` into `/var/lib/grain/state-repo`.
3. **Mode and connections are preserved automatically:** Because `grain_config` already exists in Cloud SQL (and in the cloned config repo), `-config-mode=simple` in `grain-config` metadata is skipped by the `seedOnly` rule (§1.1):
   - If the instance was in `simple` mode, the replacement VM comes back in `simple` mode with the exact same model connections, git connections, credentials, `targetRepos`, task secrets, and `allowed_principal` users/roles.
   - If an `admin` had previously switched the instance from `simple` to `full`, `grain_config.config_mode == "full"` in Cloud SQL and `grain/config` wins over `-config-mode=simple`, so the replacement VM comes back in `full` mode with all customisations intact.

### 9.3 Standalone replaceable VMs outside `grain-registry`

Self-hosted deployments running outside GCP/registry get the same replacement guarantee either by pointing `-db-url` at external PostgreSQL + `-secrets-backend` / `secrets-repo.json`, or by configuring `<data-dir>/state-repo.json` with `"mode": "mirror"` (write-through push on every `SyncAfterWrite` without enabling `configRepoOnly` PR refusals) paired with `<data-dir>/secrets-repo.json` (`pkg/daemon/secretsrepo.go` X25519 encrypted `secrets.enc`) and a SQLite backup of `BackupOnlyTables` (`allowed_principal`). On a fresh host given those bootstrap credentials, `StartupSecretsRepoSync` and `staterepo.Load` restore connections, `config_mode`, and secrets before `-config-mode` seed flags run.

---

## 10. UI design {#SC-12}

### 10.1 Simple Settings layout (`/settings`)

In `simple` mode, an `admin` opening `/settings` sees a flat, uncluttered left rail with **five configuration items**, a divider for **browser preferences** and **Upgrade**, and a footer with **one read-only inspection link** plus the mode switch:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  ← Back to tasks    Settings                                                 │
├───────────────────────┬──────────────────────────────────────────────────────┤
│  Models            ●  │  Models                                              │
│  Git                  │  Connect AI providers and choose the default model.  │
│  Repositories         │                                                      │
│  Users                │  ┌─ Provider connections ────────── [ + Connect ] ─┐ │
│  Secrets              │  │ ≡ 1. Claude (OAuth · alice)        ● Enabled  ⋯ │ │
│  ───────────────────  │  │ ≡ 2. Gemini API key                ● Enabled  ⋯ │ │
│  Preferences          │  └─────────────────────────────────────────────────┘ │
│  Upgrade        v2.4  │  Connections are tried in order when one hits a      │
│                       │  rate limit.                                         │
│                       │                                                      │
│                       │  ┌─ Default model ─────────────────────────────────┐ │
│                       │  │ Primary:  [ claude-sonnet-4-6               ▾ ] │ │
│                       │  │ Fallback: [ gemini-2.5-pro                  ▾ ] │ │
│                       │  │           + Add fallback model                  │ │
│                       │  └─────────────────────────────────────────────────┘ │
│  ───────────────────  │                                                      │
│  Show all settings    │                                           [ Save ]   │
│  (read-only)          │                                                      │
│  Switch to full mode… │                                                      │
└───────────────────────┴──────────────────────────────────────────────────────┘
```

### 10.2 Users page (`/settings/users`, alias `/settings/access`)

The **Users** section edits `allowed_principal` entries and their RBAC roles (`admin`, `writer`, `reader` from [Task #2764](/tasks/2764)), alongside the current browser's sign-in card:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  ← Back to tasks    Settings                                                 │
├───────────────────────┬──────────────────────────────────────────────────────┤
│  Models               │  Users                                               │
│  Git                  │  Control who can sign in to this Grain instance and  │
│  Repositories         │  what they can change.                               │
│  Users             ●  │                                                      │
│  Secrets              │  ┌─ Allowed users ─────────────────────────────────┐ │
│  ───────────────────  │  │ Principal                       Role            │ │
│  Preferences          │  │ ─────────────────────────────────────────────── │ │
│  Upgrade        v2.4  │  │ user:alice@example.com          Admin  🔒       │ │
│                       │  │   Instance owner (managed by registry)          │ │
│                       │  │ user:bob@example.com          [ Writer ▾ ] [✕]  │ │
│                       │  │ group:frontend@example.com    [ Writer ▾ ] [✕]  │ │
│                       │  │ domain:example.com            [ Reader ▾ ] [✕]  │ │
│                       │  │                                                 │ │
│                       │  │ [ user: ▾ ] [ carol@example.com ] [ Writer ▾ ]  │ │
│                       │  │                                       [ + Add ] │ │
│  ───────────────────  │  └─────────────────────────────────────────────────┘ │
│  Show all settings    │  Roles: Admins manage connections & users · Writers  │
│  (read-only)          │  file and approve tasks · Readers have read access.  │
│  Switch to full mode… │                                                      │
│                       │  ┌─ This browser ──────────────────────────────────┐ │
│                       │  │ Signed in as alice@example.com (Admin)          │ │
│                       │  └─────────────────────────────────────────────────┘ │
└───────────────────────┴──────────────────────────────────────────────────────┘
```

### 10.3 `Show all settings (read-only)`, deep links, and `writer` / `reader` views

- **`Show all settings (read-only)` (`/settings/<section>?all=1`):** Clicking this link expands the hidden groups (* Sandboxes, Capabilities, Engine, Caches, CI, Tasks, Agents, General, Hostds, Config repo, Memory*) under a muted **Locked defaults** heading in the left rail. Every control on those pages renders disabled (`fieldset disabled`) with a top banner:
  > **Simple configuration mode is on.** These settings are locked to Grain's defaults (`simple.v1`). [Hide locked settings](/settings/models) · [Switch to full mode…](#switch-mode)
- **Deep links to locked sections:** Navigating directly to `/settings/ci` or `/settings/sandboxes` in simple mode opens that section in the same read-only inspection state with the banner above, rather than returning a broken page or silently discarding the URL.
- **Settings search (`settingsIndex.js`):** Search hits in locked sections stay discoverable when `all=1` (or under a *"Locked in simple mode"* group at the bottom of search results) and open read-only.
- **Sidebar & repo pages outside Settings:** Top-level sidebar items **Boards**, **Processes**, and **Config** are hidden in simple mode (Tasks, Inbox, Director chat, Repositories, CI status, and System health remain). On `/repos/<owner>/<name>`, the overview and active PRs/runs render normally while the per-repo settings tabs are hidden (with a *"Repository settings use Grain's defaults in simple mode"* note if visited by URL).
- **`writer` and `reader` views:** When `whoami.role !== "admin"`, `/settings` shows only **Preferences** and the *This browser* sign-in card; deployment connection/user/secret/upgrade tabs and the **Switch to full mode…** link are omitted.

---

## 11. Verification and test requirements

Every normative guarantee in this design has a stable requirement ID (`SC-1`..`SC-14`) backed by automated unit, integration, and Playwright tests:

| Req ID | Requirement | Test layer & target | What proves it holds |
| --- | --- | --- | --- |
| **SC-1** | `config_mode` defaults to `"full"` when empty, seeds from `-config-mode=simple` only on an uninitialised store, and persists across restarts once stored. | `pkg/model`, `pkg/daemon` (`TestConfigModeSeedOnlyAndUpgradeDefault`) | Upgrading a schema dump with `config_mode = ""` reads `ConfigModeFull`; booting with `-config-mode=simple` seeds `"simple"`, switching to `"full"` via API and restarting with `-config-mode=simple` stays `"full"`. |
| **SC-2** | Only a human `admin` may change `config_mode`; `PrincipalAgent`, `writer`, and `reader` callers are refused in both directions. | `pkg/ui` (`TestConfigModeSwitchRequiresHumanAdmin`) | `PUT /api/settings {"configMode":"full"}` and `{"configMode":"simple"}` return `403 person_required` for `PrincipalAgent` (even with `api:settings:write` + `directConfigWrites: true`) and `403 insufficient_role` for `writer`/`reader`. |
| **SC-3** | Per-repo CI mode in simple mode derives `github` when `.github/workflows/*.yml` or check runs exist on the PR/branch, and `none` otherwise. | `pkg/orchestrator` (`TestSimpleModeDerivesCIModeFromWorkflowsAndChecks`) | Repo without `.github/workflows/` and 0 checks merges clean green PRs under `CIModeNone`; PR adding `.github/workflows/test.yml` or reporting a failing check gates under `CIModeGitHub`. |
| **SC-4** | `allowed_principal` enforces RBAC roles (`admin`, `writer`, `reader`), `admin > writer > reader` precedence, and the non-removable `deploymentOwner` (`admin`) floor. | `pkg/ui` (`TestAllowedPrincipalsRolesAndDeploymentOwnerFloor`) | Removing or demoting `user:<deploymentOwner>` via `/api/access/principals` fails with `422`; emptying DB `allowed_principal` still admits `deploymentOwner` as `admin`; `writer` can `POST /api/tasks` and approve, gets `403` on `/api/settings`, `/api/deployment/*`, and `/api/access/principals`; `reader` gets `403` on `POST /api/tasks` too. |
| **SC-5** | `pkg/model/profiles/simple.v1.yml` classifies every `Config` column, `RepoConfig` field, `UpdateSettingsRequest` key, `NewTaskFormFieldIDs` entry, `SystemTemplates` ID, and `SettingsTables` table. | `pkg/model`, `pkg/ui` (`TestSimpleProfileCoversEverySetting`) | Reflection drift test fails if any struct field, API key, form field, system schedule, or settings table is missing from `simple.v1.yml`. |
| **SC-6** | Every mutating HTTP/MCP route outside the simple-mode allowlist refuses with `409 locked_in_simple_mode` (including mixed `PUT /api/settings` requests and advanced `AgentBackend` fields). | `pkg/ui` (`TestSimpleModeRouteExhaustiveRefusals`) | Iterates every registered mutating route in `pkg/ui/server.go`; verifies allowed routes succeed for `admin`, mixed payloads (`defaultModel` + `maxWorkers`) fail atomically with `409 locked_in_simple_mode`, and `custom` framework / custom `concurrency` on `agent-backends` is refused. |
| **SC-7** | Agents cannot mutate locked settings, cannot self-grant `Dangerous` API scopes in simple mode, and cannot alter `allowed_principal`. | `pkg/ui`, `pkg/mcp` (`TestSimpleModeBlocksAgentMutationsAndScopeEscalation`) | Task runs over MCP, agent API, and `sandbox-cli` receive `409 locked_in_simple_mode` on locked settings and `403` on `configMode` / `/api/access/principals`; `POST /api/tasks` with `grants` in simple mode strips/refuses non-default grants. |
| **SC-8** | Config-repo proposals/pushes (`ProposeConfigChange`, `POST /api/state-repo/pull-requests`, git proxy push, `staterepo.Check`) touching simple-locked fields or `config_mode` are refused; out-of-band external `Apply` clamps to `simple.v1` and raises `config.simple-mode-clamped`. | `pkg/staterepo`, `pkg/daemon` (`TestSimpleModeRefusesConfigRepoEditsAndClampsExternalApply`) | Pushing a branch editing `max_workers` or `settings/capabilities.yml` in `grain/config` fails validation; allowed UI writes still export via `SyncAfterWrite`; simulated external remote commit altering `max_workers` is clamped back to `4` on `Apply` with an Inbox warning. |
| **SC-9** | Mode transitions: `simple → full` keeps all connections, repos, users, and values; `full → simple` previews diffs via `POST /api/settings/plan`, snapshots `grain/config`, resets locked columns to `simple.v1`, and pauses user schedules/standing sandboxes. | `pkg/ui`, `pkg/daemon` (`TestConfigModeRoundTripAndFullToSimpleReset`) | Customising `maxWorkers`, `promptExtension`, a `repo_config`, and an active user schedule in `full`, calling `/api/settings/plan`, then switching to `simple` writes a pre-switch Git commit, resets locked fields to `simple.v1`, pauses the user schedule, and keeps connections/users/secrets intact. |
| **SC-10** | System schedules in simple mode run only `system:pipeline-steward` and `system:memory-agent` (with `SkippedQuiet` on idle nights), and suppress `infra-review`, `release-notes`, `steward-audit`, and `workflow-review`. | `pkg/orchestrator` (`TestSimpleModeSystemSchedulesAndIdleMemorySkip`) | `reconcileSystemSchedules` leaves only the two essential schedules enabled; `memory-agent` records `SkippedQuiet` when zero tasks completed since its last run. |
| **SC-11** | First-run guided setup renders on an unconfigured simple-mode instance for `admin`, walks Model → Git → Repos, and lands on the task list with `SimpleNewTaskForm`. | `ui/src` Vitest + `ui/e2e` Playwright (`simple-mode-onboarding.spec.js`) | Fresh simple-mode server opens the 3-step wizard at `/`, completes each step, lands on `/`, and renders New Task with only `title`, `description`, `repo`, `attachments`, and `+ More options`. |
| **SC-12** | Simple Settings UI hides locked sections, exposes `Models`, `Git`, `Repositories`, `Users`, `Secrets`, `Preferences`, and `Upgrade`, toggles read-only inspection via `Show all settings (read-only)`, and hides `admin` tabs from `writer`/`reader`. | `ui/src` Vitest (`SettingsPage.simple.test.jsx`) + Playwright (`simple-mode-settings.spec.js`) | Asserts exact nav items at desktop and phone widths in light and dark themes, verifies `Show all settings (read-only)` renders locked cards disabled with the simple-mode banner, verifies Users add/role/remove and locked registry owner row, and verifies `writer` sees only `Preferences` + `Access`. |
| **SC-13** | Replaceable VM recovery restores `config_mode` (`simple` or `full`), connections, repos, allowed users, and secrets on a fresh VM before seed flags run. | `pkg/daemon`, `tests/e2e` (`TestReplacedVMBootsInSavedModeWithConnections`) | Simulated registry replacement (fresh VM directory reconnecting to existing PostgreSQL DB + Secret Manager backend + config repo) and standalone `mode: "mirror"` recovery both boot in saved `config_mode` (`simple` and switched `full`) with `agentBackends`, `gitConnections`, `allowed_principal`, and secrets intact. |
| **SC-14** | Registry-created instances pass `-config-mode=simple` and `-deployment-owner=<owner>` in first-boot metadata. | `pkg/hub` (`TestRegistryCreateSeedsSimpleModeAndOwner`) | `Compute.Create` `VMSpec` metadata sets `config_mode=simple` and `deployment_owner=<owner>`. |

---

## 12. Open questions

1. **Should `writer`s be allowed to fulfill a `request_secret` prompt (`PUT /api/tasks/{id}/secret`) on a task they filed?**
   Today `PUT /api/tasks/{id}/secret` writes into the shared deployment secret store (`pkg/secrets`), so a `writer` could overwrite a secret key used by other tasks on the deployment.
   - **Recommendation:** Require `admin` in v1 (a parked `request_secret` notifies `admin`s in the Inbox), or allow `writer` **create-if-absent only** (never overwrite an existing secret name) if `design/design/rbac.design.md` ([Task #2764](/tasks/2764)) adopts write-once task secrets.
2. **Should `.grain/*` repository declarations (`repo_config.declarations`) ever be derived/enabled in simple mode?**
   A repository's `.grain/settings.yml` can supply `setup-command` and `prompt-extension` from source control without UI settings, which is convenient, *but* `.grain/workflows/*`, `.grain/schedules/*`, and `.grain/merge-rules.yml` also introduce Grain CI jobs, scheduled workflows, and merge rules whose UI surfaces are hidden in simple mode.
   - **Recommendation:** Keep `declarations: false` in `simple.v1` so simple mode has zero hidden `.grain/*` automation; `system:memory-agent` covers repo conventions automatically, and teams using `.grain/*` workflows can switch to `full` mode in one click.
3. **Should the `grain` CLI work for `writer`s and `admin`s against a simple-mode registry instance without `gma_` tokens?**
   Yes: per `authentication.design.md` (*grain CLI*) and `hub.design.md` (*APIs and UI → HTTP API*), the human `grain` CLI authenticates with an IAP ID token (not a `gma_` external-agent token), so `externalAgentApi: false` does not block CLI users. Whether `admin`s in simple mode also want `gma_` tokens for external automation is deferred until a user asks.
   - **Recommendation:** Keep `externalAgentApi: false` in `simple.v1`; human CLI sign-in via IAP works automatically under the caller's RBAC role (`admin`, `writer`, or `reader`).
4. **Should `max_workers: 4` scale up on `large` registry VM shapes (16 vCPU, 64 GiB)?**
   `4` protects personal model quotas, while a 6-person team picking a `large` VM shape may have multiple provider connections and want 8 concurrent workers without leaving simple mode.
   - **Recommendation:** Start `simple.v1` at `max_workers: 4` (or `min(8, max(4, hostCPUs / 2))` as a `derived` expression in `simple.v1.yml`); either way `sandboxCommitment` remains the hard RAM/disk safety floor. Prefer fixed `4` in v1 for predictability unless early registry teams hit queuing delays.
5. **Should registry instances offer an opt-out checkbox (*Start in full configuration mode*) on the registry's `/-/new` create page?**
   - **Recommendation:** No checkbox in the registry MVP—every new registry instance starts in `simple` mode, and power users click **Switch to full mode…** inside Settings once created. Fewer choices on `/-/new` keeps registry creation fast.

---

## 13. Implementation plan

These are the follow-up tasks implied by this design, in dependency order. **They are not filed yet: the operator reviews this design first.**

1. **Dependency — RBAC design & implementation ([Task #2764](/tasks/2764): `design/design/rbac.design.md`, `design/spec/rbac/rbac.spec.md`).**
   Land Grain's `admin`, `writer`, and `reader` role model on `allowed_principal` (including `domain:` alongside `user:` and `group:`), `admin > writer > reader` resolution, `GET /api/whoami` `role`, route-level permission checks, and the `-deployment-owner` non-removable `admin` floor (`SC-4`).
2. **Backend: `config_mode` column, `-config-mode` seed flag, and `pkg/model/profiles/simple.v1.yml`.**
   Add `grain_config.config_mode` (`full` | `simple`), `-config-mode` seed-only flag and `grain init --simple`, the embedded `simple.v1.yml` loader (`SimpleProfile`, `SimpleNewTaskForm`), and the reflection completeness test `TestSimpleProfileCoversEverySetting` (`SC-1`, `SC-5`). *No dependencies (can run in parallel with 1).*
3. **Backend: simple-mode API & config-repo enforcement + mode-switch reset.**
   Implement `clearSimpleEditable` and `locked_in_simple_mode` refusals in `pkg/ui/config_refusal.go`, `PrincipalAgent` and `admin`-role gates on `configMode` (`SC-2`, `SC-6`, `SC-7`), `staterepo.Check` refusal + external `Apply` clamping (`SC-8`), and `POST /api/settings/plan` + pre-switch `grain/config` snapshot + atomic reset for `full → simple` (`SC-9`). *Depends on 1 and 2.*
4. **Orchestrator: derived per-repo `ci_mode` and simple-mode system schedule filter.**
   Derive `github` vs. `none` from `.github/workflows/` presence and reported checks when `config_mode == "simple"` (`SC-3`); restrict `reconcileSystemSchedules` in simple mode to `system:pipeline-steward` and `system:memory-agent`, and add `SkippedQuiet` when zero tasks completed since `memory-agent`'s last firing (`SC-10`). *Depends on 2.*
5. **UI: simple Settings view, Users role editor, `Show all settings (read-only)`, and role-gated navigation.**
   Update `/settings/users` (`/settings/access`) with `admin` / `writer` / `reader` role selectors and the locked registry owner badge, filter `SETTINGS_GROUPS` and sidebar navigation in simple mode, add `?all=1` read-only inspection banner and the `full ↔ simple` switch modal (backed by `/api/settings/plan`), hide `admin` Settings tabs from `writer`/`reader`, and add Vitest + Playwright screenshot and bounding-box coverage (`SC-12`). *Depends on 1 and 3.*
6. **UI: guided 3-step first-run setup flow.**
   Render the resumable Model → Git → Repositories setup wizard on `/` for unconfigured simple-mode instances when `whoami.role === "admin"`, landing on the task list with `SimpleNewTaskForm`, with Vitest and Playwright e2e tests (`SC-11`). *Depends on 3 and 5.*
7. **Persistence & registry integration: write-through `mode: "mirror"` state repo and registry VM seeding/replace verification.**
   Add `mode: "mirror"` to `pkg/staterepo/settings.go` so simple-mode UI edits push straight to the per-instance private config repo (and standalone mirror repos), wire `-config-mode=simple` and `-deployment-owner=<owner>` into `grain-registry` `grain-config` VM metadata (`SC-14`), and add end-to-end VM replacement tests verifying `simple` and switched `full` modes survive replacement with Cloud SQL + Secret Manager + config repo intact (`SC-13`). *Depends on 2, 3, and `hub.design.md`.*
