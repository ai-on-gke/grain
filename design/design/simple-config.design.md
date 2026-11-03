# Simplified configuration mode

**Owner:** @bwsalmon

**Status:** Proposed (design only; no code yet)

A Grain deployment today exposes more than eighty settings across sixteen Settings sections, seven per-repository tabs, twenty-four New Task form controls, five system schedules, and twenty-eight config-repository tables ([`settings.spec.md`](../spec/settings/settings.spec.md)). That surface exists for large, shared deployments that run custom capability servers, release trains, qualification plans, and GitOps config-repository workflows. For a personal or small-team instance—especially one created in two clicks through the Grain registry ([`registry.design.md`](registry.design.md))—it is overwhelming on day one and creates dozens of ways to misconfigure a working appliance.

**Simplified configuration mode** (`config_mode: simple`) is a deployment-level mode in which a person sees and edits only what is needed to connect Grain to models, connect it to Git and pick repositories, admit teammates, and supply task secrets when asked. Every other setting is pinned to a single, versioned default profile (`pkg/model/profiles/simple.v1.yml`), hidden in the UI (behind one read-only inspection link), and refused at both the HTTP/MCP API and the config repository. An owner who outgrows simple mode can switch to `full` in place without losing any connection, task, or credential.

Read with:
- [`settings.design.md`](settings.design.md), [`settings.spec.md`](../spec/settings/settings.spec.md), and [`settings-mutability.spec.md`](../spec/settings/settings-mutability.spec.md) — the six state surfaces, `settings_locked`, `clearBootstrap`, `pkg/ui/config_refusal.go`, and `pkg/staterepo`.
- [`settings-panel.design.md`](settings-panel.design.md) and `settings/new-task-form.yml` (`pkg/model/new_task_form.go`) — the Settings navigation and declarative New Task form layers that simple mode reuses rather than inventing a parallel UI mechanism.
- [`registry.design.md`](registry.design.md) — how personal/small-team VM instances are created, authenticated, upgraded, and replaced.
- [`authentication.design.md`](authentication.design.md) and [`operator-attribution.design.md`](operator-attribution.design.md) — verified human principals (`IdentityVerifier`, `-ui-allowed-principals`, `ui.RequestActor`).

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
- **Seed-only startup flag.** `grain daemon` gains `-config-mode=simple|full` in the `seedOnly` flag class (`pkg/cli/daemon.go`, `settings.spec.md` §10.1): it seeds `grain_config.config_mode` only when creating the initial `grain_config` row (`GetConfig` returns `nil`, and no external state repo was loaded). Once the row exists, the database (and its state-repo projection) wins, so an owner can switch `simple → full` in the UI without editing `/etc/grain/daemon.env` or VM metadata, and restarts keep the chosen mode.
- **`grain init --simple`.** Workstation and self-hosted installs keep `full` as the default, with `--simple` opting into `config_mode: simple` at bootstrap.

### 1.2 Who may switch `config_mode` {#SC-2}

Only a **human `owner`** may change `config_mode` (`simple → full` or `full → simple`):

1. **Never an agent.** Any request carrying `PrincipalAgent` (scoped agent API, MCP, or in-sandbox CLI—even if holding `api:settings:write`) attempting to modify `config_mode` is refused unconditionally with `403 Forbidden` (`code: "person_required"`). Allowing an agent to flip `simple → full` would bypass the entire lock; allowing `full → simple` would let an agent reset operator configuration. This extends the existing `checkGrainConfigWritable` rule that already bars `PrincipalAgent` from unlocking `settings_locked` (`settings-mutability.spec.md` §2.9.2).
2. **Owner role required under RBAC (§4).** Among human callers, switching `config_mode` requires the `owner` role (`member` and `viewer` callers receive `403 Forbidden`, `code: "insufficient_role"`). Pre-RBAC, any verified human principal (`PrincipalHuman`) or local break-glass operator may switch it.
3. **Not via config-repo commits in simple mode.** While `config_mode == "simple"`, a `grain/config` commit or pull request cannot flip `config_mode` to `"full"` (`staterepo.Check` refuses it; §6.2); the switch must happen through the authenticated UI/API by a human `owner`.

### 1.3 Default for registry-created instances

**Recommendation: registry-created instances default to `simple`.**

When `grain-registry` provisions a VM (`registry.design.md`, *Instance lifecycle → Create*, step 4), it includes `config_mode=simple` in the VM's `grain-config` metadata alongside the registry verifier flags and owner email. The first-boot unit writes `-config-mode=simple` into `/etc/grain/daemon.env`.

Why `simple` should be the registry default:
- Registry users are individual engineers or small teams spinning up an appliance to run tasks on a handful of repos; forcing them through sixteen Settings sections, capability policies, and `grain/config` PRs defeats the purpose of one-click instance creation.
- Nothing is permanently lost or walled off: because `-config-mode` is seed-only, any instance owner who later wants custom schedules, capability servers, or merge-queue tuning clicks **Switch to full configuration** in Settings and immediately gets the full Grain surface on the same VM.
- Optionally, the registry's `/-/new` form may offer an advanced checkbox (*Start in full configuration mode*, default unchecked) for power users creating a second instance, without cluttering the primary flow.

---

## 2. What stays editable in `simple` mode

In `simple` mode, standing deployment configuration shrinks to **four editable areas** (all restricted to the `owner` role once RBAC lands; §4), plus write-only task secrets when tasks need credentials:

| Area | Route / UI | What the owner can edit | What is defaulted / hidden within the area |
| --- | --- | --- | --- |
| **1. Model connections** | `/settings/models` | Add, edit, enable/disable, reorder, test, and remove `claude`, `codex`, and `antigravity` connections (API key or OAuth/login); pick primary `defaultModel` and optional ordered fallback (`defaultModels`). | Per-connection `concurrency` (`4`), `cliPath` (empty), `custom` command backend, `modelTurnTimeout` / `modelRetryCutoff`, per-model reasoning `effort` (`modelSettings`), `agentBackendId` pin, `judgementModel`, and `cliRun` (`Where each agent CLI runs`). |
| **2. Git connections & commit identity** | `/settings/git` | Add, test, and remove GitHub Apps, PATs, or SSH keys for `github.com`, GitHub Enterprise, GitLab, Gitea, or other git hosts (`gitConnections`, optional `scope`, per-repo `git_credential`); optional commit author override (`agentGitName`, `agentGitEmail`). | `githubHost` / `githubInsecureHttp` startup-restart plumbing (each connection already carries its own `host`). Empty `agentGitName` / `agentGitEmail` defaults automatically to the credential's own identity (for example, the GitHub App bot or PAT owner's `~/.gitconfig`, preserving CLA compliance). |
| **3. Repositories** | `/settings/repositories` | Add or remove repositories in `targetRepos` via a searchable checklist populated from `GET /api/deployment/reachable-repos` (plus manual `owner/name` entry for hosts without listing). | All per-repository settings tabs (`/repos/<o>/<n>/settings/*`); every repository runs with zero-row `RepoConfig` defaults and derived CI mode (§3.12). |
| **4. Allowed users & roles** | `/settings/users` | Add or remove `user:<email>`, `group:<email>`, and `domain:<domain>` entries in `allowedPrincipals`, and set each entry's RBAC role (`owner`, `member`, `viewer`). | On a registry (or owner-seeded) instance, `user:<deploymentOwner>` is pinned to `owner` and cannot be removed or demoted (§4.2). `gcp-key` pool service accounts remain rejected (`RejectGCPKeyPoolAllowedPrincipals`). |

### Model rotation and per-connection limits (detailed decision)

- **Connection rotation and fallback models are IN (kept effortless).** Single-account personal credentials hit provider rate limits and session pauses regularly. Adding a second credential (two Claude logins, or Claude + Gemini) only helps if Grain rotates across enabled connections and falls back across models. Simple mode keeps `AgentBackends` list ordering (up/down reorder + Enable/Disable toggle) and the single *Default model* + *Add fallback model* picker (`defaultModel` / `defaultModels`).
- **Per-connection limits and harness plumbing are OUT (pinned to defaults).** `concurrency` stays at `DefaultAgentBackendConcurrency` (`4`), `modelTurnTimeout` and `modelRetryCutoff` use framework defaults, `modelSettings` (reasoning effort) stays empty (provider default), `custom` shell-command backends are hidden, and `cliRun` stays where the deployment image/host configured it (`hostd` on registry VMs, container/daemon elsewhere).

---

## 3. "What else might we need?" — Candidate-by-candidate evaluation

Every other capability or knob in Grain falls into one of three buckets in simple mode: **In** (editable by `owner`), **Derived** (computed automatically without a knob), or **Out** (locked to the `simple.v1` profile default and hidden).

| # | Candidate | Decision | Summary |
| --- | --- | --- | --- |
| 3.1 | **Repositories** | **In** (candidates derived from git connection) | Explicit `targetRepos` opt-in list picked from `reachable-repos`; avoids exposing every repo a PAT/App can see. |
| 3.2 | **People and access (allowed users + RBAC)** | **In** | Editable `user:`, `group:`, `domain:` allowlist with `owner` / `member` / `viewer` roles; registry owner is non-removable (§4). |
| 3.3 | **Secrets vs. capabilities** | **Secrets: In** (write-only) · **Capabilities: Out** | `request_secret` in tasks and a minimal write-only Secrets list stay active; capability servers and policy stay locked. |
| 3.4 | **Spending and limits** | **Out + Derived** (from host shape) | `maxWorkers: 4`, auto-submit caps `2`/`5`, and `sandboxCommitment` dynamically scales concurrency to the VM's CPU/RAM/disk. |
| 3.5 | **Notifications** | **Out** (built-in Inbox + browser toggle) | Uses Grain's built-in `/inbox`, header alerts, and per-browser Web Notifications (`localStorage`); external chat bots stay out. |
| 3.6 | **Upgrades** | **Auto-upgrade ON** + manual **Upgrade now** action | `autoUpgradeChannel: "latest"` at `03:00` with `upgradeCheckMode: "auto"`; `POST /api/upgrade` action stays available to `owner`. |
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

Today `-ui-allowed-principals` is a startup CLI/env flag (`pkg/ui/allowlist.go`, `pkg/cli/daemon.go`), which means an owner on a registry VM cannot invite a teammate without SSHing into the VM and editing `/etc/grain/daemon.env`. In simple mode, **Users** (`/settings/users`) becomes a first-class declarative setting (`grain_config.allowed_principals`) paired with RBAC roles (`owner`, `member`, `viewer`). On a registry instance, the registry's verified owner (`-deployment-owner`) is permanently admitted as `owner` and cannot be removed or demoted. Section 4 specifies the exact RBAC contract and lockout-safety rules.

### 3.3 Secrets that tasks need vs. capabilities: **Secrets In (write-only), Capabilities Out**

Even on a one-person project, repository tasks and tests routinely need a secret that does not belong in Git—an npm token, a container registry credential, or a test API key. Grain already has a built-in workflow for this: an agent calls the `request_secret` MCP tool, which parks the task in the Inbox and renders a write-only value box backed by `PUT /api/tasks/{id}/secret` (`<data-dir>/secrets`, `pkg/secrets`).

- **What stays In:** `request_secret` (`PUT /api/tasks/{id}/secret`) and a compact **Secrets** page (`/settings/secrets`, backed by `GET /api/secrets` for names only and `PUT`/`DELETE /api/secrets/{secret}/{key}`) stay open to `owner`s so they can fulfill secret requests, pre-seed a secret, or rotate/delete an expired one. Secret values never enter `grain/config`.
- **What stays Out:** Custom capability servers (`mcp_servers`), capability policy rules (`capability_policy`), legacy GCP key-minting plumbing (`gcpProject`, `gcpServiceAccountEmail`, `gcp_key`), and task capability grants (`defaultCapabilities`) are locked and hidden.

### 3.4 Spending and limits: **Out + Derived from host hardware**

Raw scheduler and budget knobs (`pollInterval`, `maxMergers`, `maxCIJobs`, `sandboxCpuOvercommit`, `sandboxMemoryOvercommit`, `sandboxDiskOvercommit`, `sandboxMemoryReserveMb`, `sandboxDiskFloor*`, `sandboxOvercommit`) require understanding Grain's admission arithmetic and are locked in simple mode:
- **`maxWorkers` is pinned to `4`** (rather than full mode's `10`). Four concurrent workers match `DefaultAgentBackendConcurrency` (`4`) and prevent a burst of follow-up tasks from exhausting a personal Claude or Gemini rate-limit window in a single minute.
- **Hardware scaling is already automatic.** Grain's `NewBudget` (`pkg/model/budget.go`) computes `sandboxCommitment` dynamically from the host's actual vCPUs, RAM (`ReserveMemoryDefault = -1`, reserving 1/8 of host RAM, minimum 2 GiB), and disk (`DefaultDiskFloorShare = 0.10`, `MinDiskFloorMB = 5120`). On a `small` registry VM (4 vCPU, 16 GiB RAM), the budget naturally caps concurrent sandboxes to what fits in ~14 GiB; on a `large` VM (16 vCPU, 64 GiB RAM), it scales up to the `maxWorkers: 4` (+ `maxMergers: 1`) ceiling without changing a single setting.
- **Runaway proposal caps stay tight:** `maxAutoSubmitDepth: 2`, `maxAutoSubmitFanOut: 5`, `maxAgentTurns: 0` (framework default, bounded by the 2-hour per-run wall-clock timeout), and provider quota pauses (`#2214`) automatically park runs until quota resets.

### 3.5 Notifications: **Out (defaulted to Inbox and browser notifications)**

Grain's built-in **Inbox** (`/inbox`), header alert badge (`AlertsButton.jsx`), and per-browser Web Notifications toggle (`grain.inboxNotifications` in `localStorage` under `/settings/preferences`) work with zero external credentials. External notification integrations (`googleChatSpace` on console sessions and the `gchat` capability server) require bot credentials and space IDs and are **Out** in simple mode.

### 3.6 Upgrades: **Auto-upgrade ON (`latest`), plus manual Upgrade action**

In full mode, `autoUpgradeChannel` defaults to `""` (off), because production multi-engineer controllers often coordinate upgrades manually. For personal and registry VMs (`registry.design.md`, *Instance lifecycle → Upgrade*), leaving auto-upgrade off means instances stagnate on the release image they booted with.

In simple mode:
- **Standing policy (locked):** `autoUpgradeChannel: "latest"`, `autoUpgradeTime: "03:00"` (in the deployment's `timeZone`, default `UTC`), `upgradeCheckMode: "auto"`, `upgradeCheckPausesPipeline: false`. Grain drains and upgrades non-disruptively (`non-disruptive-upgrade.design.md`) overnight as new releases are promoted.
- **Manual action (kept for `owner`):** `/settings/upgrade` (and the registry's owner page button `POST /api/entries/<name>/upgrade`) remains available as an imperative operational action (`POST /api/upgrade`) so an owner can pull a newly promoted release immediately without switching modes. Arbitrary branch/image-override inputs on the Upgrade panel are hidden in simple mode; the button upgrades to `latest`.

### 3.7 Agent framework and model choice per task: **Framework Out, Model optional in `more`**

In full mode, `settings/new-task-form.yml` (`model.NewTaskForm`) exposes twenty-four task controls. Pinning a task to a specific `agentBackendId` or CLI framework (`claude` vs `codex` vs `antigravity`) ties the task to one credential quota pool and requires knowing how CLI harnesses differ.

Simple mode applies a built-in **`SimpleNewTaskForm`** layer (`pkg/model/new_task_form.go`; §5.3) instead of inventing a separate task form:
- **Primary (`shown`):** `title`, `description`, `repo` (pre-filled when only one target repo exists), and `attachments`.
- **Collapsed (`more`):** `base` (target branch), `autoMerge` (default `true`), `approved` (*Queue immediately*, default `true`), `dependsOn`, and **`model`** (optional override restricted to models served by the enabled connections; empty by default so tasks use `defaultModel` and fallback rotation, and hidden automatically when only one model is available).
- **Hidden (`hidden`, using profile defaults):** `agentBackendId`, `grants`, `legacyCapabilities`, `networkAccess`, `nothingNeededAction`, `reviewTemplateId`, `promptExtension`, `hostd`, `sandboxShape`, `customFields`, `noPort`, `fixesBaseBranch`, `writes`, `reads`, `newBranch`, `atFront`.

### 3.8 Templates, schedules, suites, boards, and custom fields: **Out (exact system-schedule split)**

User-defined templates, schedules, suites, custom task boards, and custom fields are workflow-orchestration tools for larger teams. In simple mode:
- Top-level sidebar links to **Boards**, **Processes** (Templates, Schedules, Suites), and **Config** (`/config`) are hidden, custom fields are hidden, and API mutations to `template*`, `schedule*`, `suite*`, `board*`, and `custom_field` are refused (`409 locked_in_simple_mode`).
- **Standing Director chat stays ON:** `grain_config.console_sessions` is pinned to `nil` (which resolves to the single built-in **Director** session), so `owner`s and `member`s can chat with the Director to plan and file tasks, while creating or editing custom standing console sessions is locked.
- **System schedules (`pkg/model/system_template.go`):** Grain ships five seeded schedules in `model.SystemTemplates` plus the repo-declared `system:workflow-review:<stem>` family. Simple mode enables **only the two essential operational schedules** and suppresses the other four in `reconcileSystemSchedules`:

| System schedule ID | Cadence | Simple mode | Justification |
| --- | --- | --- | --- |
| `system:pipeline-steward` | Event-driven (`pipeline.*`) + 6h safety check (`SkippedQuiet` when healthy) | **ON** | Essential self-healing: diagnoses confirmed red base branches and coordinates a single `base-green` repair task so the user's repo does not wedge. Costs **zero tokens** when healthy (`pipelineStewardQuiet`). |
| `system:memory-agent` | Nightly (`03:00`) | **ON** *(with quiet skip)* | Essential in simple mode *because* `promptExtension` and `setupCommand` are locked: memory is how Grain records a repository's build/test conventions from completed tasks. Extended with `SkippedQuiet` when zero tasks completed since its last run, so idle instances spend zero tokens overnight. |
| `system:infra-review` | Nightly (`03:00`) | **OFF** | Audits overcommit ratios, cache sizes, worker counts, and merge-queue knobs to propose `grain/config` PRs—every one of which is locked in simple mode. Running it would spend tokens on unmergeable config advice. |
| `system:release-notes` | Weekly (Monday) | **OFF** | Writes weekly deployment-throughput narratives (`release-notes/`), unnecessary on personal/small-team appliances and wasteful of model quota. |
| `system:steward-audit` | Weekly (Monday) | **OFF** | Meta-audits 1-in-10 `pipeline-steward` wakes for Grain's own quality tracking; unnecessary overhead on user appliances. |
| `system:workflow-review:<stem>` | On scheduled workflow run | **OFF** | Driven by `.grain/workflows/*` repository declarations, which are off (`declarations: false`) in simple mode. |

### 3.9 Capabilities and capability servers: **Out (fixed default catalogue)**

Simple mode runs with `intree.Builtins()` (`EnsureBuiltins`) and **no custom `mcp_servers`**, **empty `defaultCapabilities`** on both deployment and repos, **empty `capabilityPolicy` rules**, **empty `api_scope_set` additions**, and **`externalAgentApi: false`**. Tasks run in isolated sandboxes with Grain's standard built-in task surface (target-repo git push via proxy, `ci-read` / `grain ci`, `ask_question`, `request_secret`, `comment_on_issue`, `nothing_needed`, `propose_task`). Internal grants baked into `system:pipeline-steward` and `system:memory-agent` continue to resolve normally from their code-defined templates.

### 3.10 Sandbox backend and size: **Out (fixed by deployment or registry shape)**

`sandboxBackend`, `sandboxImage`, `sandboxImageMirror`, `sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, `networkAccess`, `sessionSandboxIdleTtl`, and all sixteen `k8s*` fields are locked in simple mode. Most of these are `restartRequired` (`pkg/ui/settings.go`) and misconfiguring them leaves the daemon unable to boot sandboxes (`unrunnable`). On registry VMs, `scripts/setup.sh` sets the local sandbox flags at VM boot; on Kubernetes installs, the Helm/Terraform manifest sets them; in both cases `0` / `""` in `grain_config` cleanly inherits the deployment's provisioned backend and default guest shape (2 vCPU, 8192 MiB RAM, default root disk).

### 3.11 The CI mode per repo: **Derived automatically (`github` if present, else `none`)** {#SC-3}

Today `RepoConfig.EffectiveCIMode()` (`pkg/model/repo_config.go`) unconditionally defaults an unset `CIMode` (`""`) to `CIModeGitHub` (`"github"`). That works on repositories with `.github/workflows/*.yml` or external status checks, where GitHub reports check runs that gate merging. On a brand-new or personal repository with **no** `.github/workflows/` directory and **no** external CI apps reporting check runs or commit statuses, configuring CI manually per repo should not be required.

In simple mode, `repo_config.ci_mode` is locked (`""`) and **derived per pull request commit** (`orchestrator.effectiveRepoCIMode` when `config_mode == "simple"` and `repo_config.ci_mode == ""`):
1. If the repository is hosted on GitHub and **either** (a) the PR's head tree (or base branch) contains at least one `.github/workflows/*.yml` / `.yaml` file, **or** (b) GitHub reports one or more check runs or commit statuses on the head SHA (or base branch's required status checks), effective CI mode is **`CIModeGitHub`** (`"github"`): Grain waits for all reported checks to pass and auto-repairs failures as usual.
2. Otherwise (a repository with no `.github/workflows/` and zero check runs/statuses, or a plain SSH/non-GitHub git host with no check API), effective CI mode is **`CIModeNone`** (`"none"`): a clean, non-conflicting PR merges without hanging indefinitely waiting for checks that do not exist, while the agent still runs tests directly inside its sandbox before pushing.
3. Adding `.github/workflows/ci.yml` in a task PR immediately activates `CIModeGitHub` on that very PR (condition 1a holds on the PR's head tree) with zero settings changes.

---

## 4. Allowed users and RBAC integration {#SC-4}

### 4.1 Dependency: the Grain RBAC design (`design/design/rbac.design.md`)

Grain has no roles today: every principal admitted by `-ui-allowed-principals` or the access code has full operator authority (`authentication.design.md`, `operator-attribution.design.md`). No RBAC design document exists in `design/design/` yet.

**Filing and approving `design/design/rbac.design.md` is a hard dependency of simple mode's Users role selector and route-level role gates** (Task 1 in §12). To keep boundaries clean, **this document does not design the general RBAC engine** (such as custom roles, per-repository ACLs, audit schemas, or token delegation). Instead, §4.2–§4.3 specify **exact, minimal requirements and code seams** that simple mode requires from RBAC, and carry forward the open question deferred from `authentication.design.md` (§Open questions):
- **Deferred RBAC question (belongs in `rbac.design.md`):** *Who may mint `gma_` external-agent tokens (`/api/external-agents/tokens`) and `hostd` registration tokens (`/api/hostds`)?* Simple mode locks both surfaces (`externalAgentApi: false`, Hostds hidden), and recommends that `rbac.design.md` restrict both to `owner` in full mode because both grant execution or API authority across the deployment.

### 4.2 What simple mode requires from RBAC

Simple mode needs three built-in deployment roles assignable per allowed-principal entry:

| Role | Tasks, Inbox, & Director chat | Connections, Repos, Users, Secrets, Upgrade, & `config_mode` |
| --- | --- | --- |
| **`owner`** | Full access (file, edit, approve, cancel, comment, answer `ask_question`, merge/retrigger, chat with Director). | **Editable** (within the active `config_mode`): edit model & git connections, `targetRepos`, `allowedPrincipals` & roles, write-only task secrets (`PUT /api/secrets/…` and `PUT /api/tasks/{id}/secret`), trigger `Upgrade`, and switch `config_mode` (`simple ↔ full`). |
| **`member`** | **Full day-to-day work:** file, edit, approve, cancel, reopen, and comment on tasks; answer `ask_question`; chat with Director; acknowledge Inbox notifications; trigger PR merge/retrigger. | **Read-only / hidden:** cannot edit connections, `targetRepos`, `allowedPrincipals`, deployment secrets (`PUT /api/tasks/{id}/secret` writes deployment-wide `<data-dir>/secrets`, so fulfilling or overwriting secrets is `owner`-only unless `rbac.design.md` introduces task-scoped secrets), `Upgrade`, or `config_mode`. Settings shows only browser-local **Preferences** and signed-in **Access** info. |
| **`viewer`** | **Read-only:** view tasks, diffs, transcripts, CI logs, repositories, and Inbox. All task/chat/inbox mutations return `403`. | **Read-only / hidden:** same as `member` (browser **Preferences** and **Access** only; all mutating API calls return `403`). |

Resolution rules simple mode relies on from RBAC:
1. **Highest matching role wins.** If `domain:example.com` is granted `viewer`, `group:eng@example.com` is granted `member`, and `user:alice@example.com` is granted `owner`, `alice@example.com` resolves to `owner` (never downgraded by a broader group or domain rule).
2. **Local break-glass access code resolves to `owner`.** Loopback / break-glass `access-code` callers (`operator (access code)`) retain `owner` authority so SSH break-glass recovery (`registry.design.md`, *The access code on registry-created instances*) can always repair a broken allowlist.

### 4.3 Hooks simple mode defines

1. **Declarative allowlist schema (`grain_config.allowed_principals`).** Stored in `grain_config.allowed_principals` (JSON array, exported to `tables/grain_config.json` and listed in `trustboundary.NeverAutoMergePaths`):
   ```go
   type Role string
   const (
       RoleOwner  Role = "owner"
       RoleMember Role = "member"
       RoleViewer Role = "viewer"
   )

   type AllowedPrincipal struct {
       Principal string `json:"principal" yaml:"principal"` // "user:<email>", "group:<email>", "domain:<domain>", "serviceAccount:<email>"
       Role      Role   `json:"role" yaml:"role"`           // "owner", "member", or "viewer"
   }
   ```
   - Validation reuses `ui.ParseAllowedPrincipals` and `ui.RejectGCPKeyPoolAllowedPrincipals` (`pkg/ui/allowlist.go`). In simple mode's UI, prefixes `user:`, `group:`, and `domain:` are offered (`serviceAccount:` entries provisioned by Grain itself remain implicit).
   - Migration from `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS`: bare startup entries without a stored `allowed_principals` row seed `grain_config.allowed_principals` with `role: "owner"`, preserving existing deployments' exact behavior.
2. **Immutable deployment-owner floor (no-lockout invariant).**
   - A new startup flag `-deployment-owner=<email>` (`$GRAIN_DEPLOYMENT_OWNER`, set by `grain-registry` from `entries.owner` in VM metadata and by `grain init`) defines the deployment's pinned owner.
   - `Server.effectiveAllowedPrincipals(ctx)` **always** unions `{Principal: "user:" + deploymentOwner, Role: RoleOwner}` in memory before evaluating any request, regardless of what `grain_config.allowed_principals` or `grain/config` holds. Even a corrupted database or bad config import can never lock out the registry owner (preserving `settings.design.md` §1's lockout-safety rule while allowing `allowed_principals` to persist across VM replacements).
   - `PUT /api/deployment/allowed-principals` (and `PUT /api/settings`) refuses with `422 Unprocessable Entity` any update that: (a) attempts to omit or demote `user:<deploymentOwner>`, (b) leaves zero `user:` entries with role `owner`, or (c) removes or demotes the requesting human caller's own `owner` access unless `-deployment-owner` still covers them.
   - On registry ownership transfer (`registry.design.md` §Authorization rules), the registry updates the instance's owner floor and adds the new owner as `owner`.
3. **Route permission hook & `whoami` extension.**
   - Mutating handlers call `s.requireRole(r, model.RoleOwner)` (or `model.RoleMember` for task/chat writes) ahead of `config_refusal.go` guards, returning `403 Forbidden` with `{"code": "insufficient_role", "error": "this action requires the owner role", "requiredRole": "owner", "role": "<caller-role>"}`.
   - `GET /api/whoami` (`operator-attribution.design.md`) adds `"role": "owner" | "member" | "viewer"` and `"deploymentOwner": "<email>"`, which drives Settings visibility in React.

---

## 5. The locked defaults: `pkg/model/profiles/simple.v1.yml` {#SC-5}

### 5.1 Declared, versioned profile (not scattered constants)

Every setting's simple-mode disposition lives in **one embedded, versioned YAML file**: `pkg/model/profiles/simple.v1.yml`, loaded via `//go:embed` into `model.SimpleProfile()` (`ProfileVersion: 1`).

Why an embedded, versioned profile in `pkg/model/profiles/` (exported onto `grain_config` / `grain/config` when simple mode is active) beats scattered `if cfg.ConfigMode == Simple` literals or a user-editable file in `grain/config`:
1. **Single auditable source of truth.** Reviewers can read one ~180-line YAML file to see every pinned value, every derived rule, every enabled system schedule, and the entire New Task form layout.
2. **Compile/test-time exhaustiveness (`TestSimpleProfileCoversEverySetting`).** Using reflection over `model.Config`, `model.RepoConfig`, `ui.UpdateSettingsRequest`, `model.NewTaskFormFieldIDs`, `model.SystemTemplates`, and `staterepo.SettingsTables`—the exact pattern `pkg/ui/settings_restart_drift_test.go` and `TestEveryTableIsEitherSettingsOrDeliberatelyNot` already use—adding a new setting anywhere in Grain **fails `go test ./pkg/model/...`** until `simple.v1.yml` classifies it as `editable`, `derived`, or `pinned`.
3. **Clean upgrades.** When a future Grain release tunes a simple-mode default in `simple.v2.yml`, upgrading a simple-mode instance applies the updated profile cleanly on startup without disturbing editable fields (`agentBackends`, `defaultModel`, `gitConnections`, `targetRepos`, `allowedPrincipals`).

Shape of `pkg/model/profiles/simple.v1.yml`:

```yaml
profile: simple
version: 1

grain_config:
  editable:
    - config_mode           # human owner only
    - agent_backends        # simple subset: id, name, framework (claude|codex|antigravity), models, excludedModels, authMode, credentialBackendId, disabled
    - default_model
    - git_connections
    - git_credential
    - agent_git_name
    - agent_git_email
    - target_repos
    - allowed_principals    # human owner only; deploymentOwner floor enforced
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
    auto_upgrade_channel: "latest"
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
| **§3.1 Models** | `agentBackends` (`claude`, `codex`, `antigravity`; `id`, `name`, `framework`, `models`, `excludedModels`, `authMode`, `credentialBackendId`, `disabled`), credentials, `defaultModel` / `defaultModels` | **Editable (`owner`)** |
| **§3.1 Models (advanced)** | Backend `concurrency` (`4`), `cliPath` (`""`), `custom` `command`, `modelTurnTimeout` / `modelRetryCutoff` (`0`), `modelSettings` (`[]`), `agentBackendId` (`""`), `judgementModel` (`""`), `cliRun` (`{}`) | **Pinned** |
| **§3.2 Sandboxes** | `sandboxBackend` (`""`), `sandboxCpus` (`0`), `sandboxMemoryMb` (`0`), `sandboxDiskGb` (`0`), `networkAccess` (`""`), `sessionSandboxIdleTtl` (`30m`), `sandboxImage` (`""`), `sandboxImageMirror` (`""`), `sandboxOvercommit` (`fixed`), `sandbox{Cpu,Memory,Disk}Overcommit` (`2.0`/`1.0`/`2.0`), `sandboxMemoryReserveMb` (`-1`), `sandboxDiskFloorShare` (`0.10`), `sandboxDiskFloorMinMb` (`5120`), all `k8s*` | **Pinned** (resolved from VM/cluster startup flags + dynamic host budget) |
| **§3.4 Secrets / Legacy capabilities** | Secret values (`<data-dir>/secrets` via `PUT`/`DELETE /api/secrets/…` and `PUT /api/tasks/{id}/secret`) | **Editable (`owner`, write-only)** |
| **§3.4 Legacy capabilities (config)** | `githubHost` (`""` → `github.com`), `githubInsecureHttp` (`false`), `gcpProject` (`""`), `gcpServiceAccountEmail` (`""`), `gcpServiceAccountCount` (`0`), `settings.capabilities` catalogue | **Pinned** |
| **§3.5 Access & Hostds** | `allowedPrincipals` (`user:`, `group:`, `domain:` + RBAC role) | **Editable (`owner`)**; `deploymentOwner` floor non-removable. `externalAgentApi: false`; `/api/hostds` token management locked (VM's own local `hostd` uses `-hostd-token-file`). |
| **§3.6 & §3.10 Upgrades** | `autoUpgradeChannel` (`"latest"`), `autoUpgradeTime` (`"03:00"`), `upgradeCheckMode` (`"auto"`), `upgradeCheckPausesPipeline` (`false`); `POST /api/upgrade` (to `latest`) | Policy **Pinned**; manual **Upgrade to latest** action **Open (`owner`)** |
| **§3.7 Caches** | `packageCacheEnabled` (`true`), `packageCacheSizeGb` (`0` → 20 GiB), `actionsCacheEnabled` (`true`), `actionsCacheSizeGb` (`0` → 20 GiB) | **Pinned** (flush actions remain available on `/system` if needed, hidden from Settings) |
| **§3.8 Engine** | `pollInterval` (`10s`), `maxWorkers` (`4`), `maxMergers` (`1`), `maxMergesPerRepo` (`1`), `maxCIJobs` (`1`), retired `mergeBatch*` | **Pinned** |
| **§3.9 Config repo** | `state-repo.json` UI (`POST /api/state-repo/remote`, `/pull-requests`) | **Locked in UI/API** (except registry/bootstrap write-through mirror; §8) |
| **§3.10 General** | `configMode` (`"simple"`), `environmentName` (`""`), `showClosedByDefault` (`false`), `settingsLocked` (`false`), `timeZone` (`"UTC"`), `deploymentId` (preserved auto-generated ID, never wiped) | `configMode` **Editable (`owner`)**; `deploymentId` preserved; rest **Pinned** |
| **§3.11 Git** | `gitConnections`, `gitCredential`, `repo_config.git_credential`, `agentGitName`, `agentGitEmail` | **Editable (`owner`)** |
| **§3.12 Repositories** | `targetRepos` | **Editable (`owner`)** |
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
   Just as `clearBootstrap` zeroes bootstrap fields so `bootstrapOnlyChange(before, after)` detects non-bootstrap edits on `PUT /api/settings` and `PUT /api/deployment`, `clearSimpleEditable(c *model.Config)` zeroes only the simple-editable fields (`ConfigMode`, the simple subset of `AgentBackends`, `DefaultModel`, `GitConnections`, `GitCredential`, `AgentGitName`, `AgentGitEmail`, `TargetRepos`, `AllowedPrincipals`).
   - For `AgentBackends`, `normalizeSimpleBackends` also checks that no backend sets `Framework == "custom"`, `Command != ""`, `CLIPath != ""`, non-default `Concurrency`, or custom `ModelTurnTimeout` / `ModelRetryCutoff`.
   - `simpleEditableChange(before, after)` runs the same `comparableConfig` + `pruneEmpty` comparison as `bootstrapOnlyChange`. Mixed requests (such as `{"defaultModel": "claude-opus-5-5", "maxWorkers": 16}`) fail as a whole without writing the allowed half (`settings-mutability.spec.md` §2.3.5).
2. **Route guard extension in `pkg/ui/config_refusal.go`.**
   Every mutating settings/entity route is classified against simple mode:
   - **Allowed for `owner` in simple mode:**
     - `PUT`/`POST /api/settings`, `PUT /api/config`, `PUT /api/deployment` — when `simpleEditableChange(before, after)` holds (and if `ConfigMode` or `AllowedPrincipals` changed, caller is `PrincipalHuman` with `RoleOwner`).
     - `PUT`/`DELETE /api/deployment/agent-backends/{id}` (validated for simple backend fields) and credential routes (`PUT`/`DELETE /api/agent-backends/{id}/credential`, `/codex-auth`, `/api/agent-keys/{framework}`, `POST /api/agent-keys/{framework}/check`).
     - `POST`/`DELETE /api/deployment/git-connections[/{cred}]`, `POST /api/deployment/git-connections/check`, `PUT /api/deployment/repos/{o}/{n}/git-connection`.
     - `POST /api/repos`, `DELETE /api/repos/{o}/{n}`.
     - `PUT /api/deployment/allowed-principals`.
     - `PUT`/`DELETE /api/secrets/{secret}[/{key}]` and `PUT /api/tasks/{id}/secret`.
     - `POST /api/upgrade` (with no custom branch/image override; upgrades to `latest`).
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
   `state-repo.json`'s `directConfigWrites: true` only relaxes `settings_locked` (`settings-mutability.spec.md` §2.5); it never bypasses `config_mode: simple`. While `config_mode == "simple"`, UI writes to the four allowed areas commit directly to the store and call `StateManager.SyncAfterWrite` to keep `grain/config` current.
4. **Agents can never unlock or write locked settings.** {#SC-7}
   - `PrincipalAgent` hits the identical `409 locked_in_simple_mode` guard on every locked setting.
   - `PrincipalAgent` is refused with `403 person_required` on `configMode` and `allowedPrincipals`, and in simple mode no task can hold `Dangerous` `api:*:write` scopes (`SimpleNewTaskForm` hides `grants`, `api_scope_set` is empty, and `POST /api/tasks` rejects privileged capability grants on simple-mode task creation except on internal system templates).
   - Grain's own internal reconciler and `system:memory-agent` store writes carry no HTTP `Principal` / `WriteRehearsal` and proceed normally, matching `settings-mutability.spec.md` §1.3.

### 6.2 Config repository handling in simple mode {#SC-8}

We have three options for commits/PRs against `grain/config` (or an external backup mirror) while `config_mode == "simple"`:
1. *Allowed with a warning* — breaks the lock: a config PR or stray commit could change `max_workers` or `prompt_extension` behind a UI that hides those controls.
2. *Ignored silently on import* — breaks `staterepo` fidelity (`settings.design.md` core rule): `grain/config` and SQLite diverge silently.
3. **Refused at Grain's config gates + defensively clamped on external `Apply` with a warning notification (Recommended).**

**Recommendation: Option 3.** Specifically:
- **Refused at every Grain-controlled Git/PR gate.** `staterepo.Check` (called by `ProposeConfigChange` in `pkg/daemon/config_change.go`, `POST /api/state-repo/pull-requests`, `grain state check`, and the git proxy's push validation on `grain/config`) runs `checkSimpleModeProfile(baseDir, candidateDir)`: if `base` or `candidate` has `config_mode == "simple"`, any diff touching a locked `grain_config` field, a locked `repo_config` field, a locked `settings/*.yml` file, or `config_mode` itself fails validation with `"locked in simple mode; switch to full mode in Settings to change <field>"`. Meanwhile, `StateManager.SyncAfterWrite` continues to export the allowed simple-mode edits (`agent_backends`, `git_connections`, `target_repos`, `allowed_principals`, and `system:memory-agent`'s `memory/**`) to `grain/config` so the repository stays an exact backup.
- **Clamped on out-of-band external `Apply` so sync never wedges.** If an owner attaches a Git backup mirror (§8.2) and pushes a hand-edited locked field directly on GitHub bypassing Grain's proxy, refusing the entire `staterepo.Apply` tick would wedge all future imports (`pkg/staterepo/replaced.go` lines 49–57). Instead, when `config_mode == "simple"`, `staterepo.Apply` imports the allowed fields, clamps locked fields back to `simple.v1.yml`, logs a `staterepo: simple mode clamped ...` journal entry, raises/updates a warning notification (`config.simple-mode-clamped`) in the Inbox, and re-exports on the next sync.

---

## 7. Switching modes (`simple ↔ full`) {#SC-9}

### 7.1 `simple → full`

When a human `owner` switches `config_mode` from `"simple"` to `"full"` (`PUT /api/settings {"configMode": "full"}`):
1. **All current values are preserved.** Model connections, `defaultModel`, git connections, commit identity, `targetRepos`, `allowedPrincipals` (and roles), task secrets, `deploymentId`, and `memory/**` remain untouched.
2. **Previously pinned values become ordinary editable values.** For instance, `max_workers` (`4`) and `auto_upgrade_channel` (`"latest"`) stay at those values in `grain_config` until the owner edits them under `/settings/engine` or `/settings/general`.
3. **Full UI and system schedules unlock live.** All sixteen Settings sections, per-repo settings tabs, Boards, Processes, Config Repo Browser, and the full New Task form appear immediately with no daemon restart. On the next reconcile tick, `reconcileSystemSchedules` re-enables `system:infra-review`, `system:release-notes`, and `system:steward-audit`.

### 7.2 `full → simple`

There are two ways `full → simple` could treat existing customisations:
- *Alternative A (keep customised values and lock them):* leaves `max_workers: 25`, a custom `setupCommand`, `declarations: true`, `autoMergeByDefault: false`, or an hourly user schedule running in the background while **hiding every UI page that shows they exist**. The instance becomes "haunted": its actual behaviour no longer matches simple mode, and neither the owner nor support can tell why tasks fail or why tokens burn overnight without switching back to `full`.
- **Alternative B — Recommended (reset locked settings to `simple.v1` defaults, with an explicit preview confirmation and a pre-switch Git snapshot):**

How **Alternative B** works:
1. **Dry-run preview (`POST /api/settings/plan {"configMode": "simple"}`).** Returns `simpleModeReset`: an itemised list of every `grain_config` field, `repo_config` override, and active user `schedule` / `standing_sandbox` / custom entity that differs from `simple.v1.yml`. If nothing differs, the confirmation is a single paragraph.
2. **Confirmation dialog in UI.** Lists every item that will change (for example: *"Max workers: 12 → 4; `acme/api` custom setup script cleared; 2 custom schedules paused"*) and requires clicking **Reset to defaults & switch to simple mode**.
3. **Pre-switch snapshot in `grain/config`.** Before mutating the store, `StateManager` commits the current `full` export to `grain/config` (`"Snapshot full configuration before switching to simple mode"`), so every customisation, prompt extension, template, and script remains recoverable in Git history even after the reset.
4. **Atomic reset.** In one database transaction:
   - `grain_config` locked columns are set to `simple.v1.yml` values (preserving `agentBackends` [clamping any advanced per-backend fields to simple defaults], `defaultModel`, `gitConnections`, `gitCredential`, `agentGitName`, `agentGitEmail`, `targetRepos`, `allowedPrincipals`, and `deploymentId`).
   - `repo_config` rows clear all columns except `Repo` and `GitCredential` (`Empty()` rows with no `GitCredential` are deleted per `PutRepoConfig`'s normal rule).
   - User-created `schedule` and `standing_sandbox` rows have `enabled` set to `false` (paused rather than deleted, so nothing fires invisibly while simple mode is on, and switching back to `full` lets the owner re-enable them without recreating them; templates, suites, and boards stay inert in their tables while hidden).
   - `StateManager.SyncAfterWrite` commits the resulting simple state to `grain/config`.

---

## 8. First-run flow {#SC-10}

Today (`ui/src/alerts.js`, `deploymentSetupStepsLeft`), a fresh deployment drops the owner onto an empty task list with a corner alert reading *"3 setup steps left"*, requiring visits to three separate pages (`/settings/git`, `/settings/models`, `/repos`).

In `simple` mode, when the signed-in user is an `owner`, zero tasks exist yet, and `deploymentSetupStepsLeft` is non-empty (unless the owner clicks *Skip to task list*), `/` renders a **guided 3-step Setup flow** directly in the main pane:

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

- **Step 1 — Model connection:** Pick Claude, Gemini (`antigravity`), or Codex; authenticate via OAuth or paste a key (`POST /api/agent-keys/{framework}/check` verifies before saving); auto-selects the provider's recommended default model with an inline dropdown to change it.
- **Step 2 — Git connection:** Install/connect a GitHub App, paste a PAT, or add an SSH key (for GitHub, GitHub Enterprise, GitLab, or another host), verified live via `POST /api/deployment/git-connections/check`. Displays *"Commits will be authored as `<detected credential identity>`"* with a disclosure link *Override commit name/email* (`agentGitName`, `agentGitEmail`).
- **Step 3 — Repositories:** Automatically calls `GET /api/deployment/reachable-repos` using Step 2's connection and shows a filterable checklist of accessible `owner/name` repositories (plus a manual `owner/name` input when listing is unavailable).
- **Completion:** Clicking **Finish & open task list** saves `targetRepos` and lands on `/` (the task list) with the streamlined New Task composer focused (pre-selecting `repo` when one repository was chosen).
- **Resumable & role-aware:** Each step persists immediately to the database on **Save & continue**, so refreshing the browser resumes at the first incomplete step. If a `member` or `viewer` visits before setup finishes, they see a calm waiting card (*"This Grain instance is still being set up by its owner"*) rather than unsubmittable forms.

---

## 9. Replaceable registry instances: where config and secrets live {#SC-11}

A central goal of `registry.design.md` is that personal/team VMs are cattle, not pets: if a VM is resized (`small → standard`), migrated across zones, or replaced after a host failure, the replacement VM must come back **in the same `config_mode` (`simple` or `full`) with the same model connections, git connections, target repositories, allowed users, and working credentials**.

### 9.1 Why `grain/config` alone or today's Mode 4 is not enough

1. **Metadata vs. secret material (`settings.design.md` §1):** `grain_config` (and its export `tables/grain_config.json`) stores `config_mode`, `agent_backends`, `default_model`, `git_connections`, `target_repos`, and `allowed_principals`—but **never** API keys, OAuth tokens, GitHub App private keys, or task secrets, which live encrypted in `<data-dir>/secrets` (`pkg/secrets`). Restoring `grain_config` without `<data-dir>/secrets` brings back connection rows whose credentials are missing.
2. **Why today's External Config Repo mode (Mode 4) clashes with simple mode:** When `<data-dir>/state-repo.json` sets `remote` today, `configRepoOnly` becomes `true` and **every UI write—including Models and Git—is refused with `409 config_managed_externally`** (`settings-mutability.spec.md` §2.7). A simple-mode owner edits connections in the UI, not via GitOps PRs.
3. **Network isolation (`registry.design.md` §Network isolation):** Registry VMs have **no** network route to the registry's Cloud SQL database and **no** GCP IAM permissions (`grain-instance@` has zero roles; RFC 1918 and private Google API ranges are blocked). Therefore, an instance VM cannot store its database in the registry's Cloud SQL or call Cloud KMS/GCS directly.

### 9.2 How state survives VM replacement

Inside a running Grain VM, **SQLite (`grain_config`) + `<data-dir>/secrets` + controller-local `grain/config`** remain the live runtime stores. Two mechanisms carry both declarative config and encrypted secrets across a VM replacement while preserving `registry.design.md`'s zero-privilege VM boundary:

1. **Registry-managed replacement (zero user setup — recommended default for `grain-registry`):**
   - **Data-disk detach/reattach on in-zone replace or resize (`Replace VM` vs. `Delete entry`):** When the registry replaces a VM for machine-type resize or base-image refresh, it detaches (or clones from a snapshot) the `/var/lib/grain` persistent disk rather than deleting it; only `DELETE /api/entries/<name>` destroys the data disk. Because `-config-mode=simple` in VM metadata is **seed-only**, the new VM reads the existing `/var/lib/grain` SQLite `grain_config` row (`config_mode`, connections, repos, users) and `<data-dir>/secrets` intact.
   - **KMS-enveloped recovery bundle pushed over `:8080` (survives total disk/zone loss):** On each settings/secret write (debounced) or periodic reconciler poll, the registry reconciler fetches an exported snapshot bundle (`staterepo` settings archive + `secrets.enc` + wrapped master key) from the instance over the existing internal `registry → VM:8080` channel (authenticated by a short-lived admin JWT with `iss=<registry>`, `aud=<instance>`, `sub=registry`), encrypts it with Cloud KMS using AAD `(registry_id, entry_name, owner)` so no other entry can ever decrypt it, and stores the ciphertext in object storage / `instance_backups`. When creating a replacement VM onto a blank disk, the reconciler pushes that bundle back to `/api/internal/bootstrap-restore` on `:8080` **before** flipping the entry from `creating` to `running` and before seeding defaults. The VM itself still holds zero GCP credentials and zero access to other VMs.
2. **Optional Git-backed write-through mirror (`state-repo.json` `mode: "mirror"` + `secrets-repo`) for standalone replaceable VMs:**
   - Add `"mode": "mirror"` alongside `"external"` (the default when `remote != ""`) in `<data-dir>/state-repo.json` (`pkg/staterepo/settings.go`).
   - In `"mirror"` mode, `configRepoOnly` stays `false`: UI writes commit to the store, run `SyncAfterWrite`, and **push** straight to the remote Git repo; simultaneously `pkg/daemon/secretsrepo.go` (`<data-dir>/secrets-repo.json` + X25519 master key) pushes encrypted `secrets.enc` to its separate private repo.
   - On a replaced standalone VM booted with `state-repo.json` (`mode: "mirror"`), `secrets-repo.json`, and the `secrets-key`, `StartupSecretsRepoSync` + `staterepo.Load` restore secrets and `SettingsTables` (including `config_mode`, connections, repos, and users) before `-config-mode` seed flags are consulted.

In both paths, if the owner was in `simple` mode, the replaced VM boots in `simple` mode with every connection and secret ready; if the owner had switched to `full` mode, `grain_config.config_mode == "full"` in the restored store overrides the `-config-mode=simple` seed flag and the replaced VM comes back in `full` mode.

---

## 10. UI design {#SC-12}

### 10.1 Simple Settings layout (`/settings`)

In `simple` mode, an `owner` opening `/settings` sees a flat, uncluttered left rail with **five configuration items**, a divider for **browser preferences** and **Upgrade**, and a footer with **one read-only inspection link** plus the mode switch:

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

The new **Users** section edits `grain_config.allowed_principals` and their RBAC roles (`owner`, `member`, `viewer`), alongside the current browser's sign-in card:

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
│  Upgrade        v2.4  │  │ user:alice@example.com          Owner  🔒       │ │
│                       │  │   Instance owner (managed by registry)          │ │
│                       │  │ user:bob@example.com          [ Member ▾ ] [✕]  │ │
│                       │  │ group:frontend@example.com    [ Member ▾ ] [✕]  │ │
│                       │  │ domain:example.com            [ Viewer ▾ ] [✕]  │ │
│                       │  │                                                 │ │
│                       │  │ [ user: ▾ ] [ carol@example.com ] [ Member ▾ ]  │ │
│                       │  │                                       [ + Add ] │ │
│  ───────────────────  │  └─────────────────────────────────────────────────┘ │
│  Show all settings    │  Roles: Owners manage connections & users · Members  │
│  (read-only)          │  file and approve tasks · Viewers have read access.  │
│  Switch to full mode… │                                                      │
│                       │  ┌─ This browser ──────────────────────────────────┐ │
│                       │  │ Signed in as alice@example.com (via registry)   │ │
│                       │  └─────────────────────────────────────────────────┘ │
└───────────────────────┴──────────────────────────────────────────────────────┘
```

### 10.3 Read-only inspection mode, deep links, and non-owner views

- **`Show all settings (read-only)` (`/settings?all=1`):** Clicking this link expands the locked sections (`Sandboxes`, `Caches`, `Engine`, `General`, `Tasks`, `Agents`, `Capabilities`, `CI`, `Memory`) below a divider in the left nav and renders them inside `SettingsReadOnlyContext` with a top banner:
  > **Simple configuration mode is on.** These settings are locked to Grain's defaults. [Hide locked settings](/settings/models) · [Switch to full mode…](#switch-mode)
- **Deep links to locked sections:** Navigating directly to `/settings/ci` or `/settings/sandboxes` in simple mode opens that section in the same read-only inspection state with the banner above, rather than returning a broken page or silently discarding the URL.
- **Settings search (`settingsIndex.js`):** Search hits in locked sections stay discoverable when `all=1` (or under a *"Locked in simple mode"* group at the bottom of search results) and open read-only.
- **Sidebar & repo pages outside Settings:** Top-level sidebar items **Boards**, **Processes**, and **Config** are hidden in simple mode (Tasks, Inbox, Director chat, Repositories, CI status, and System health remain). On `/repos/<owner>/<name>`, the overview and active PRs/runs render normally while the per-repo settings tabs are hidden (with a *"Repository settings use Grain's defaults in simple mode"* note if visited by URL).
- **`member` and `viewer` views:** When `whoami.role !== "owner"`, `/settings` shows only **Preferences** and the *This browser* sign-in card; deployment connection/user tabs and the **Switch to full mode…** link are omitted.

---

## 11. Verification and test requirements

Every normative guarantee in this design has a stable requirement ID (`SC-1`..`SC-14`) backed by automated unit, integration, and Playwright tests:

| Req ID | Requirement | Test layer & target | What proves it holds |
| --- | --- | --- | --- |
| **SC-1** | `config_mode` defaults to `"full"` when empty, seeds from `-config-mode=simple` only on an uninitialised store, and persists across restarts once stored. | `pkg/model`, `pkg/daemon` (`TestConfigModeSeedOnlyAndUpgradeDefault`) | Upgrading a schema dump with `config_mode = ""` reads `ConfigModeFull`; booting with `-config-mode=simple` seeds `"simple"`, switching to `"full"` via API and restarting with `-config-mode=simple` stays `"full"`. |
| **SC-2** | Only a human `owner` may change `config_mode`; `PrincipalAgent`, `member`, and `viewer` callers are refused in both directions. | `pkg/ui` (`TestConfigModeSwitchRequiresHumanOwner`) | `PUT /api/settings {"configMode":"full"}` and `{"configMode":"simple"}` return `403 person_required` for `PrincipalAgent` (even with `api:settings:write` + `directConfigWrites: true`) and `403 insufficient_role` for `member`/`viewer`. |
| **SC-3** | Per-repo CI mode in simple mode derives `github` when `.github/workflows/*.yml` or check runs exist on the PR/branch, and `none` otherwise. | `pkg/orchestrator` (`TestSimpleModeDerivesCIModeFromWorkflowsAndChecks`) | Repo without `.github/workflows/` and 0 checks merges clean green PRs under `CIModeNone`; PR adding `.github/workflows/test.yml` or reporting a failing check gates under `CIModeGitHub`. |
| **SC-4** | `allowedPrincipals` enforces RBAC roles (`owner`, `member`, `viewer`), highest-role-wins precedence, and the non-removable `deploymentOwner` floor. | `pkg/ui` (`TestAllowedPrincipalsRolesAndDeploymentOwnerFloor`) | Removing or demoting `user:<deploymentOwner>` via API fails with `422`; corrupting DB `allowed_principals` to `[]` still admits `deploymentOwner` as `owner`; `member` can `POST /api/tasks` and approve, gets `403` on `/api/settings` and `/api/deployment/*`; `viewer` gets `403` on `POST /api/tasks` too. |
| **SC-5** | `pkg/model/profiles/simple.v1.yml` classifies every `Config` column, `RepoConfig` field, `UpdateSettingsRequest` key, `NewTaskFormFieldIDs` entry, `SystemTemplates` ID, and `SettingsTables` table. | `pkg/model`, `pkg/ui` (`TestSimpleProfileCoversEverySetting`) | Reflection drift test fails if any struct field, API key, form field, system schedule, or settings table is missing from `simple.v1.yml`. |
| **SC-6** | Every mutating HTTP/MCP route outside the simple-mode allowlist refuses with `409 locked_in_simple_mode` (including mixed `PUT /api/settings` requests and advanced `AgentBackend` fields). | `pkg/ui` (`TestSimpleModeRouteExhaustiveRefusals`) | Iterates every registered mutating route in `pkg/ui/server.go`; verifies allowed routes succeed for `owner`, mixed payloads (`defaultModel` + `maxWorkers`) fail atomically with `409 locked_in_simple_mode`, and `custom` framework / custom `concurrency` on `agent-backends` is refused. |
| **SC-7** | Agents cannot mutate locked settings, cannot self-grant `Dangerous` API scopes in simple mode, and cannot alter `allowedPrincipals`. | `pkg/ui`, `pkg/mcp` (`TestSimpleModeBlocksAgentMutationsAndScopeEscalation`) | Task runs over MCP, agent API, and `sandbox-cli` receive `409 locked_in_simple_mode` on locked settings and `403` on `configMode` / `allowedPrincipals`; `POST /api/tasks` with `grants` in simple mode strips/refuses non-default grants. |
| **SC-8** | Config-repo proposals/pushes (`ProposeConfigChange`, `POST /api/state-repo/pull-requests`, git proxy push, `staterepo.Check`) touching simple-locked fields or `config_mode` are refused; out-of-band external `Apply` clamps to `simple.v1` and raises `config.simple-mode-clamped`. | `pkg/staterepo`, `pkg/daemon` (`TestSimpleModeRefusesConfigRepoEditsAndClampsExternalApply`) | Pushing a branch editing `max_workers` or `settings/capabilities.yml` in `grain/config` fails validation; allowed UI writes still export via `SyncAfterWrite`; simulated external remote commit altering `max_workers` is clamped back to `4` on `Apply` with an Inbox warning. |
| **SC-9** | Mode transitions: `simple → full` keeps all connections, repos, users, and values; `full → simple` previews diffs via `POST /api/settings/plan`, snapshots `grain/config`, resets locked columns to `simple.v1`, and pauses user schedules/standing sandboxes. | `pkg/ui`, `pkg/daemon` (`TestConfigModeRoundTripAndFullToSimpleReset`) | Customising `maxWorkers`, `promptExtension`, a `repo_config`, and an active user schedule in `full`, calling `/api/settings/plan`, then switching to `simple` writes a pre-switch Git commit, resets locked fields to `simple.v1`, pauses the user schedule, and keeps connections/users/secrets intact. |
| **SC-10** | System schedules in simple mode run only `system:pipeline-steward` and `system:memory-agent` (with `SkippedQuiet` on idle nights), and suppress `infra-review`, `release-notes`, `steward-audit`, and `workflow-review`. | `pkg/orchestrator` (`TestSimpleModeSystemSchedulesAndIdleMemorySkip`) | `reconcileSystemSchedules` leaves only the two essential schedules enabled; `memory-agent` records `SkippedQuiet` when zero tasks completed since its last run. |
| **SC-11** | First-run guided setup renders on an unconfigured simple-mode instance for `owner`, walks Model → Git → Repos, and lands on the task list with `SimpleNewTaskForm`. | `ui/src` Vitest + `ui/e2e` Playwright (`simple-mode-onboarding.spec.js`) | Fresh simple-mode server opens the 3-step wizard at `/`, completes each step, lands on `/`, and renders New Task with only `title`, `description`, `repo`, `attachments`, and `+ More options`. |
| **SC-12** | Simple Settings UI hides locked sections, exposes `Models`, `Git`, `Repositories`, `Users`, `Secrets`, `Preferences`, and `Upgrade`, toggles read-only inspection via `Show all settings (read-only)`, and hides owner tabs from `member`/`viewer`. | `ui/src` Vitest (`SettingsPage.simple.test.jsx`) + Playwright (`simple-mode-settings.spec.js`) | Asserts exact nav items at desktop and phone widths in light and dark themes, verifies `Show all settings (read-only)` renders locked cards disabled with the simple-mode banner, verifies Users add/role/remove and locked registry owner row, and verifies `member` sees only `Preferences` + `Access`. |
| **SC-13** | Replaceable VM recovery restores `config_mode` (`simple` or `full`), connections, repos, allowed users, and encrypted credentials on a fresh VM before seed flags run. | `pkg/daemon`, `tests/e2e` (`TestReplacedVMbootsInSavedModeWithConnections`) | Instance configured in `simple` (and a second switched to `full`) restores onto a new data directory from both a registry KMS-bundle / retained disk and a `mode: "mirror"` state+secrets repo pair; verifies `config_mode`, `agentBackends`, `gitConnections`, `allowedPrincipals`, and decrypted secrets match. |
| **SC-14** | Registry-created instances pass `-config-mode=simple` and `-deployment-owner=<owner>` in first-boot metadata. | `pkg/registry` (`TestRegistryCreateSeedsSimpleModeAndOwner`) | `Compute.Create` `VMSpec` metadata sets `config_mode=simple` and `deployment_owner=<owner>`. |

---

## 12. Open questions

1. **Should `member`s be allowed to fulfill a `request_secret` prompt (`PUT /api/tasks/{id}/secret`) on a task they filed?**
   Today `PUT /api/tasks/{id}/secret` writes into the shared deployment secret store (`<data-dir>/secrets`), so a `member` could overwrite a secret key used by other tasks on the deployment.
   - **Recommendation:** Require `owner` in v1 (a parked `request_secret` notifies `owner`s in the Inbox), or allow `member` **create-if-absent only** (never overwrite an existing secret name) if `rbac.design.md` adopts write-once task secrets.
2. **Should `.grain/*` repository declarations (`repo_config.declarations`) ever be derived/enabled in simple mode?**
   A repository's `.grain/settings.yml` can supply `setup-command` and `prompt-extension` from source control without UI settings, which is convenient, *but* `.grain/workflows/*`, `.grain/schedules/*`, and `.grain/merge-rules.yml` also introduce Grain CI jobs, scheduled workflows, and merge rules whose UI surfaces are hidden in simple mode.
   - **Recommendation:** Keep `declarations: false` in `simple.v1` so simple mode has zero hidden `.grain/*` automation; `system:memory-agent` covers repo conventions automatically, and teams using `.grain/*` workflows can switch to `full` mode in one click.
3. **Should the `grain` CLI work for `member`s and `owner`s against a simple-mode registry instance without `gma_` tokens?**
   Yes: per `authentication.design.md` (*grain CLI*) and `registry.design.md` (*HTTP API*), the human `grain` CLI authenticates with an IAP ID token (not a `gma_` external-agent token), so `externalAgentApi: false` does not block CLI users. Whether `owner`s in simple mode also want `gma_` tokens for external automation is deferred until a user asks.
   - **Recommendation:** Keep `externalAgentApi: false` in `simple.v1`; human CLI sign-in via IAP works automatically under the caller's RBAC role.
4. **Should `max_workers: 4` scale up on `large` registry VM shapes (16 vCPU, 64 GiB)?**
   `4` protects personal model quotas, while a 6-person team picking a `large` VM shape may have multiple provider connections and want 8 concurrent workers without leaving simple mode.
   - **Recommendation:** Start `simple.v1` at `max_workers: 4` (or `min(8, max(4, hostCPUs / 2))` as a `derived` expression in `simple.v1.yml`); either way `sandboxCommitment` remains the hard RAM/disk safety floor. Prefer fixed `4` in v1 for predictability unless early registry teams hit queuing delays.
5. **Should registry instances offer an opt-out checkbox (*Start in full configuration mode*) on the registry's `/-/new` create page?**
   - **Recommendation:** No checkbox in the registry MVP—every new registry instance starts in `simple` mode, and power users click **Switch to full mode…** inside Settings once created. Fewer choices on `/-/new` keeps registry creation fast.

---

## 13. Implementation plan

These are the follow-up tasks implied by this design, in dependency order. **They are not filed yet: the operator reviews this design first.**

1. **Design: role-based access control (`design/design/rbac.design.md`).**
   Define Grain's human principal roles (`owner`, `member`, `viewer`), highest-role-wins precedence across `user:` / `group:` / `domain:` entries, route permission checks, `GET /api/whoami` role reporting, and who may mint `gma_` external-agent tokens and `hostd` tokens (`authentication.design.md` open question). *No dependencies.*
2. **Backend: declarative `allowed_principals`, `-deployment-owner` floor, and RBAC route enforcement.**
   Add `grain_config.allowed_principals` migration and `staterepo` export (`NeverAutoMergePaths`), `-deployment-owner` immutable in-memory floor, `PUT /api/deployment/allowed-principals` validation (no owner lockout), `GET /api/whoami` `role`, and `requireRole` checks across UI/API routes (`SC-4`). *Depends on 1.*
3. **Backend: `config_mode` column, `-config-mode` seed flag, and `pkg/model/profiles/simple.v1.yml`.**
   Add `grain_config.config_mode` (`full` | `simple`), `-config-mode` seed-only flag and `grain init --simple`, the embedded `simple.v1.yml` loader (`SimpleProfile`, `SimpleNewTaskForm`), and the reflection completeness test `TestSimpleProfileCoversEverySetting` (`SC-1`, `SC-5`). *No dependencies (can run in parallel with 1–2).*
4. **Backend: simple-mode API & config-repo enforcement + mode-switch reset.**
   Implement `clearSimpleEditable` and `locked_in_simple_mode` refusals in `pkg/ui/config_refusal.go`, `PrincipalAgent` and `owner`-role gates on `configMode` (`SC-2`, `SC-6`, `SC-7`), `staterepo.Check` refusal + external `Apply` clamping (`SC-8`), and `POST /api/settings/plan` + pre-switch `grain/config` snapshot + atomic reset for `full → simple` (`SC-9`). *Depends on 2 and 3.*
5. **Orchestrator: derived per-repo `ci_mode` and simple-mode system schedule filter.**
   Derive `github` vs. `none` from `.github/workflows/` presence and reported checks when `config_mode == "simple"` (`SC-3`); restrict `reconcileSystemSchedules` in simple mode to `system:pipeline-steward` and `system:memory-agent`, and add `SkippedQuiet` when zero tasks completed since `memory-agent`'s last firing (`SC-10`). *Depends on 3.*
6. **UI: simple Settings view, Users section, `Show all settings (read-only)`, and role-gated navigation.**
   Build `UsersSection.jsx` (`/settings/users`), filter `SETTINGS_GROUPS` and sidebar navigation in simple mode, add `?all=1` read-only inspection banner and the `full ↔ simple` switch modal (backed by `/api/settings/plan`), hide owner Settings tabs from `member`/`viewer`, and add Vitest + Playwright screenshot and bounding-box coverage (`SC-12`). *Depends on 2 and 4.*
7. **UI: guided 3-step first-run setup flow.**
   Render the resumable Model → Git → Repositories setup wizard on `/` for unconfigured simple-mode instances when `whoami.role === "owner"`, landing on the task list with `SimpleNewTaskForm`, with Vitest and Playwright e2e tests (`SC-11`). *Depends on 4 and 6.*
8. **Persistence & registry integration: write-through `mode: "mirror"` state repo and registry VM seeding/restore.**
   Add `mode: "mirror"` to `pkg/staterepo/settings.go` for standalone replaceable VMs, wire `-config-mode=simple` and `-deployment-owner` into `grain-registry` VM metadata (`SC-14`), and implement data-disk retention / KMS-wrapped state+secrets bundle restore across VM replacement (`SC-13`). *Depends on 3, 4, and `registry.design.md` tasks 6 & 9.*
