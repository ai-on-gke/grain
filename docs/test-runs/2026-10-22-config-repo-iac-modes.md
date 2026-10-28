# Walkthrough & Test Run: Config Repository (`grain/config`) Mutually Exclusive Internal vs. External Read-Only IaC Modes (`docs/test-plans/2026-10-22.md` §1)

Task 212 (`grain/049f1e09/task-212`): end-to-end walkthrough, security/mutator audit, bug fixes, and automated test coverage for **§1 of `docs/test-plans/2026-10-22.md`: Config Repository (`grain/config`) Mutually Exclusive Internal vs. External Read-Only IaC Modes** (commits `41762bd9b`, `b2cd20dbc` / `b608ea2fd`, `adad9a367` / `981f505b2`).

## Environment & Commit Tested

- **Base commit**: `6abd66d74` (`candidate`: `docs(test-runs): record candidate release qualification and fix nil-Task in forceMerge`)
- **Branch**: `grain/049f1e09/task-212`
- **Server & Frontend**: Live `grain daemon` running against scratch data directories (`-sandbox-port-base=0`) with embedded production frontend built via `make frontend`, paired with a local bare Git repository (`git init --bare --initial-branch=main`) acting as the external configuration repository remote.
- **Browser & Toolchain**: Playwright `1.58.2` driving Chromium on Debian Bookworm (`Linux 6.1.0-43-amd64`), Go `1.26.2`, Node `v20.19.0`, Vitest `4.1.11`.
- **Viewports exercised**: Desktop (`1280×800`) and Phone (`390×844`).

---

## Walkthrough Execution & Step-by-Step Results

### Step 1: Internal Mode Baseline (`Remote == ""`)

1. **Daemon startup & initial export**:
   - Started `grain daemon` on a fresh scratch data directory (`data/state-repo` initialized automatically on startup).
   - Seeded a repository (`PUT /api/deployment/repos/acme/widgets/config`) and a template (`POST /api/templates`, `"Internal Seed Template"`).
   - Opened `/settings/config-repo` in Playwright at desktop (`1280×800`) and phone (`390×844`) viewports.
2. **UI & API verification**:
   - Confirmed the `grain/config` chip (`.MuiChip-root`), `"Export now"` action button, and `"Use external config repository"` button are visible at both `1280×800` and `390×844`, and `data-testid="external-config-repo-banner"` is absent (`count === 0`).
   - Clicked `"Export now"` and verified `GET /api/state-repo` returned:
     ```json
     {
       "available": true,
       "mode": "internal",
       "repository": "grain/config",
       "branch": "main",
       "head": "<40-hex-sha>"
     }
     ```
   - Verified native `grain/config` branch comparison (`GET /api/repos/grain/config/compare?base=main&head=topic/internal-check`) returned `200 OK`.
3. **CLI verification**:
   - `grain state -data-dir <dir> status` reported:
     ```text
     repository: grain/config (controller local)
     ...
     mode:       internal -- grain/config is hosted on this host.
     branch:     main
     ```
   - `grain state -data-dir <dir> repo` reported:
     ```text
     mode:       internal -- grain/config is hosted on this host.
     ```
4. **Seeding external bare Git repository**:
   - Initialized a bare Git repository (`external-config.git`) with `--initial-branch=main`, cloned the internal `state-repo`, removed `.github/workflows/grain-state-check.yml`, and pushed `main` to `external-config.git`.

| Sub-check | Viewport / Surface | Outcome |
|---|---|---|
| `/settings/config-repo` shows `grain/config` chip, `"Export now"`, no external banner | `1280×800`, `390×844` | Pass |
| `GET /api/state-repo` reports `mode: "internal"`, `repository: "grain/config"`, `remote: ""` | HTTP API | Pass |
| `grain state status` & `grain state repo` report internal `grain/config` mode | CLI | Pass |
| `GET /api/repos/grain/config/compare` works on local `grain/config` branches | HTTP API | Pass |

---

### Step 2: Switch to External IaC Mode (`Remote != ""`)

1. **Connecting external repository via UI**:
   - On `/settings/config-repo`, entered the local bare repository path into `Repository URL` and clicked `"Use external config repository"`.
2. **UI, API & CLI verification**:
   - Confirmed the primary sync button changed from `"Export now"` to `"Pull now"`, `"Switch to internal config repository"` appeared, and `"External repository (Infrastructure as Code):"` was rendered at both `1280×800` and `390×844`.
   - `GET /api/state-repo` returned:
     ```json
     {
       "available": true,
       "mode": "external",
       "repository": "<external-bare-repo-path>",
       "remote": "<external-bare-repo-path>",
       "branch": "main",
       "head": "<initial-external-sha>"
     }
     ```
   - `grain state -data-dir <dir> status` reported:
     ```text
     repository: <external-bare-repo-path> (external config repository, read-only IaC mode)
     ...
     mode:       external (read-only IaC) -- <external-bare-repo-path> (branch main)
     ```
   - `grain state -data-dir <dir> repo` reported:
     ```text
     mode:       external (read-only IaC) -- <external-bare-repo-path> (branch main)
     ```

| Sub-check | Viewport / Surface | Outcome |
|---|---|---|
| Switching via `"Use external config repository"` updates button to `"Pull now"` | `1280×800`, `390×844` | Pass |
| `GET /api/state-repo` reports `mode: "external"` and `remote` | HTTP API | Pass |
| `grain state status` & `grain state repo` report `external (read-only IaC)` | CLI | Pass |

---

### Step 3: Read-Only UI and Mutable Exceptions

1. **Repo-managed Settings sections (`1280×800` and `390×844`)**:
   - Visited `/settings/general`, `/settings/models`, `/settings/git`, `/settings/repositories`, `/settings/sandboxes`, `/settings/caches`, and `/settings/ci` at both desktop (`1280×800`) and phone (`390×844`) viewports.
   - Confirmed `data-testid="external-config-repo-banner"` was visible on every section with text `"Configuration is read-only and managed in <external-bare-repo-path> (main). Switch to the internal config repository in Config Repo settings to edit settings in the UI."`
   - Confirmed repo-managed `SettingsCard` sections wrapped their controls in `<fieldset disabled>` (`section[data-settings-card] fieldset[disabled]`).
2. **Mutable exceptions (`1280×800` and `390×844`)**:
   - `/settings/preferences`: confirmed `data-testid="external-config-repo-banner"` is absent (`count === 0`) and no `<fieldset disabled>` is rendered; browser-local appearance/timezone/pin controls remain interactive.
   - `/settings/config-repo`: confirmed `data-testid="external-config-repo-banner"` is absent (`count === 0`) and `"Pull now"` and `"Switch to internal config repository"` remain enabled.
   - `/settings/credentials` (`CredentialsSection`): confirmed repo-managed `GitHub host` input is disabled (`toBeDisabled()`), while `SecretsPanel` (`allowInExternalMode` on `SettingsCard`) keeps `"Set secret"` enabled. Stored a new encrypted secret (`iac-mode-secret` / `api-token`) through the UI while in External mode and verified it appeared in the secrets list.

| Route | Banner (`external-config-repo-banner`) | Controls State (`1280×800` & `390×844`) | Outcome |
|---|---|---|---|
| `/settings/general` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/models` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/git` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/repositories` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/sandboxes` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/caches` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/ci` | Visible | Disabled (`<fieldset disabled>`) | Pass |
| `/settings/preferences` | Absent | Interactive (mutable browser preferences) | Pass |
| `/settings/config-repo` | Absent | Interactive (`"Pull now"`, `"Switch to internal..."`) | Pass |
| `/settings/credentials` | Visible | Repo-managed host/GCP inputs disabled; `SecretsPanel` (`"Set secret"`) interactive | Pass |

---

### Step 4: API Mutator Refusals & Local `grain/config` Hosting Disabled

1. **Direct API write refusals (`409 Conflict`)**:
   - Tested all settings/configuration API mutators while in External mode (`PUT /api/settings`, `POST/PATCH/DELETE /api/templates`, `POST/PATCH/DELETE /api/schedules`, `POST/PATCH/DELETE /api/suites`, `POST/PATCH/DELETE /api/boards`, `POST/PATCH/DELETE /api/console-sessions`, `PUT/DELETE /api/deployment/repos/{owner}/{name}/config`, `PUT /api/repos/{owner}/{name}/settings`, `PUT/DELETE /api/deployment/agent-backends/{id}`, `POST/DELETE /api/deployment/git-connections`, `PUT /api/deployment/repos/{owner}/{name}/git-connection`, `PUT/DELETE /api/repos/{owner}/{name}/release/secrets/{secret}`).
   - Confirmed every endpoint returned `409 Conflict` with error message `"settings are read-only because configuration is managed in external repository <url>; commit changes to that repository instead"` and zero side effects on disk or in SQLite.
2. **Local `grain/config` Git proxy & native PR endpoints disabled**:
   - Ran `git ls-remote http://x-access-token:<token>@127.0.0.1:<git-proxy-port>/grain/config.git` and confirmed it failed with `404 Not Found`: `"local config repository is disabled when an external config repository is configured"` (without forwarding `grain/config` to upstream GitHub).
   - Verified `GET /api/repos/grain/config/compare?base=main&head=topic/internal-check` was refused (`502 Bad Gateway` with daemon log `"native comparison is not available for grain/config"`).
   - Verified `StateRepoPullRequestClient` methods (`EnsurePullRequest`, `GetPullRequest`, `ClosePullRequest`, `MergePullRequest`, `ListBranches`, `DefaultBranch`, `BranchSHA`, `DeleteBranch`, etc.) return `404` (`errExternalConfigRepoNoNativePR`) when targeting `grain/config` in External mode.

---

### Step 5: Pulling External IaC Changes Without Daemon Export Commits

1. **Change 1 — UI `"Pull now"` on `/settings/config-repo`**:
   - Committed a new template (`id: template-iac-1`, `name: "External IaC Template v1"`) in `settings/templates.yml` and updated `environment_name` to `"iac-prod-v1"` in `tables/grain_config.json` in the external Git working tree, then pushed to `origin main`.
   - Clicked `"Pull now"` on `/settings/config-repo`.
   - Verified `GET /api/state-repo` advanced `head` to the new commit SHA, `GET /api/templates` included `"External IaC Template v1"`, `GET /api/settings` returned `environmentName: "iac-prod-v1"`, and `/processes` rendered `"External IaC Template v1"` in the UI.
2. **Change 2 — CLI `grain state sync`**:
   - Committed a second template (`id: template-iac-2`, `name: "CLI Synced IaC Template"`) in `settings/templates.yml` and updated `environment_name` to `"iac-prod-v2"` in `tables/grain_config.json`, then pushed to `origin main`.
   - Executed `grain state -data-dir <dir> sync` on the CLI and confirmed output: `"applied <sha> from <external-bare-repo-path>"`.
   - Verified `GET /api/templates` included `"CLI Synced IaC Template"` and `GET /api/settings` returned `environmentName: "iac-prod-v2"`.
3. **Zero daemon export commits on external bare repository**:
   - Inspected `git --git-dir <external-bare-repo> log --format=%s <initial-external-sha>..refs/heads/main` and verified it contained **only** the two operator commits:
     1. `"Add CLI Synced IaC Template and set environment_name=iac-prod-v2"`
     2. `"Add External IaC Template v1 and set environment_name=iac-prod-v1"`
   - Confirmed zero daemon-generated export commits (`"Update grain state"` / `"Export settings"`) were ever created or pushed to the external repository.

---

### Step 6: Switch Back to Internal Mode

1. **Switching back via UI & CLI**:
   - Clicked `"Switch to internal config repository"` on `/settings/config-repo` and confirmed the `"Export now"` button and `grain/config` chip returned.
   - Tested CLI round-trip: `grain state -data-dir <dir> repo <external-bare-repo>` followed by `grain state -data-dir <dir> repo -none`, confirming output: `"switched from external config repository <external-bare-repo> to the internal grain/config repository."`
2. **Restored UI & API mutability, internal export, and local `grain/config` hosting**:
   - Visited `/settings/general` and confirmed `data-testid="external-config-repo-banner"` and `section[data-settings-card] fieldset[disabled]` were gone (`count === 0`).
   - Verified `PUT /api/settings` (`{"environmentName": "internal-restored"}`) succeeded with `200 OK`.
   - Clicked `"Export now"` on `/settings/config-repo` and verified a new commit was created in the internal `state-repo` with `tables/grain_config.json` containing `"environment_name": "internal-restored"`.
   - Verified local `grain/config` Git proxy hosting and `GET /api/repos/grain/config/compare?base=main&head=topic/internal-check` (`200 OK`) worked again.

---

## Bugs Uncovered & Fixed in This Branch

During the code audit and walkthrough of External Read-Only IaC mode, we uncovered and fixed 8 gaps where mutators or local `grain/config` handlers either bypassed `checkSettingsWritable`, performed side effects before checking `checkSettingsWritable`, fell through to upstream GitHub when local `grain/config` hosting was disabled, or re-acquired `StateManager.mu` while already holding it:

1. **`DELETE /api/schedules/{id}` missing `checkSettingsWritable` (`pkg/ui/schedules.go`)**:
   - *Symptom*: `CreateSchedule` and `UpdateSchedule` checked `c.checkSettingsWritable(ctx)`, but `DeleteSchedule` did not, allowing schedules to be deleted via the API while in External read-only IaC mode.
   - *Fix*: Added `c.checkSettingsWritable(ctx)` at the start of `DeleteSchedule` and `authorizeScheduleEdit`.
2. **`PUT/DELETE /api/deployment/agent-backends/{id}` and `POST/PUT/DELETE /api/gcp-keys*` missing `checkSettingsWritable` (`pkg/ui/team_agent_backends.go`, `pkg/ui/gcp_keys.go`)**:
   - *Symptom*: `PutTeamAgentBackend` and `DeleteTeamAgentBackend` mutated `AgentBackends` in `grain_config` and wrote/deleted secret files on disk without checking `c.checkSettingsWritable(ctx)`; `SetGCPKeyWithUser` (`POST /api/gcp-keys`, `PUT /api/gcp-keys/{name}`) and `DeleteGCPKeyWithUser` (`DELETE /api/gcp-keys/{name}`) mutated `gcp_key` (a `staterepo.SettingsTables` table overwritten by `SyncExternal`) and wrote/deleted key files on disk without checking `c.checkSettingsWritable(ctx)`.
   - *Fix*: Added `c.checkSettingsWritable(ctx)` at the entry of `PutTeamAgentBackend`, `DeleteTeamAgentBackend`, `SetGCPKeyWithUser`, and `DeleteGCPKeyWithUser`.
3. **Git connection & release secret mutators writing/removing credentials on disk before checking `checkSettingsWritable` (`pkg/ui/team_git_connections.go`, `pkg/ui/repo_release_workflows.go`)**:
   - *Symptom*: `AddTeamGitConnection`, `DeleteTeamGitConnection`, `SetTeamRepoGitConnection`, `SetRepoReleaseSecret`, and `DeleteRepoReleaseSecret` wrote or deleted credential/secret files on disk (or mutated `repo_config` via `Store.SetRepoGitConnection`) before calling `SaveConfig` (or without calling `checkSettingsWritable` at all in `SetTeamRepoGitConnection` and `DeleteRepoReleaseSecret`).
   - *Fix*: Added `c.checkSettingsWritable(ctx)` at the start of `AddTeamGitConnection`, `DeleteTeamGitConnection`, `SetTeamRepoGitConnection`, `SetRepoReleaseSecret` (when adding a new secret name to `repo_config.ReleaseSecrets`), and `DeleteRepoReleaseSecret`.
4. **Template, suite, board, console-session, qualification, workflow, and repo-config mutators performing work before `checkSettingsWritable` (`pkg/ui/templates.go`, `pkg/ui/suites.go`, `pkg/ui/boards.go`, `pkg/ui/console_sessions.go`, `pkg/ui/standing_work_preauth.go`, `pkg/ui/qualifications.go`, `pkg/ui/repo_workflows.go`, `pkg/ui/teams.go`)**:
   - *Symptom*: `DeleteConsoleSession` stopped and closed the running console session task *before* calling `checkSettingsWritable(ctx)`; `CreateTemplate`, `CreateSuite`, and `CreateBoard` allocated IDs from SQLite `*_sequence` tables before checking `checkSettingsWritable(ctx)`.
   - *Fix*: Moved `c.checkSettingsWritable(ctx)` to the entry of `CreateTemplate`, `UpdateTemplate`, `CreateSuite`, `UpdateSuite`, `DeleteSuite`, `CreateBoard`, `UpdateBoard`, `DeleteBoard`, `UpdateConsoleSession`, `DeleteConsoleSession`, `authorizeTemplateEdit`, `authorizeSuiteEdit`, `PutQualificationPlan`, `SetRepoWorkflows`, and `SetDeploymentRepoConfigWithUser`.
5. **`GitProxy.HandleRequest` forwarding disabled `grain/config` requests to upstream GitHub (`pkg/gitproxy/local.go`, `pkg/gitproxy/core.go`)**:
   - *Symptom*: When `LocalRepository.Enabled()` returned `false` in External mode, `LocalRepository.Matches("grain", "config")` returned `false`. As a result, `GitProxy.HandleRequest` skipped `LocalRepository.ForwardRepoStream` (making its `404 local config repository is disabled` response unreachable dead code) and forwarded `grain/config` to upstream `github.com/grain/config`.
   - *Fix*: Added `LocalRepository.DisabledFor(owner, name)` and checked it at the start of `GitProxy.HandleRequest` so requests for `grain/config` in External mode immediately return `404 Not Found` (`"local config repository is disabled when an external config repository is configured"`).
6. **`StateRepoPullRequestClient` falling through to `c.Client` / `queuer` / `MergeRulesReader` for `grain/config` (`pkg/daemon/statepullrequests.go`)**:
   - *Symptom*: When `isInternalConfigRepo()` was `false`, `StateRepoPullRequestClient.isLocal("grain", "config")` returned `false`, causing methods on `StateRepoPullRequestClient` to forward `grain/config` to `c.Client` instead of returning `errExternalConfigRepoNoNativePR` (`404`). Furthermore, because `StateRepoPullRequestClient` embedded `github.Client` and omitted `github.MergeRulesReader`, 11 methods (`DeleteBranch`, `SetDefaultBranch`, `ListPullRequestCommits`, `ListIssues`, `GetIssue`, `AddLabel`, `RemoveLabel`, `CloseIssue`, `ReopenIssue`, `UpdateIssue`, `CreateIssue`) fell through to `c.Client` in both Internal and External modes, and `MergeRequirements` (`github.MergeRulesReader`) was hidden from the orchestrator's type assertion.
   - *Fix*: Replaced the embedded `github.Client` with an explicit `Client github.Client` field with compile-time assertions (`_ github.Client`, `_ github.MergeQueuer`, `_ github.MergeRulesReader`), implemented all remaining `github.Client` and `github.MergeRulesReader` methods locally for `grain/config`, and guarded every method with `isDisabledConfigRepo(owner, name)`.
7. **`StateManager.Compare` and `StateManager.noteOutcome` lock ordering (`pkg/daemon/statemanager.go`)**:
   - *Symptom*: `StateManager.Compare` acquired `m.mu.Lock()` before calling `m.isInternalConfigRepo()`, which itself acquires `m.mu.Lock()` when `m.dataDir == ""`. Similarly, `StateManager.noteOutcome` (called inside `m.cycle` while `m.mu` is held) called `m.settingsRepo()` -> `m.ExternalConfigRepo()`, which acquired `m.mu.Lock()` whenever `m.dataDir == ""` or `staterepo.LoadSettings(m.dataDir)` returned an error (such as when `state-repo.json` is malformed).
   - *Fix*: Checked `!m.isInternalConfigRepo() || !target.Same(model.ConfigRepo())` before acquiring `m.mu.Lock()` in `Compare`, and added `externalConfigRepo(false)` / `settingsRepoLocked()` for `noteOutcome` so `m.mu` is never re-locked while held.
8. **Branch name validation in `staterepo.Open`, `ConfigureMirror`, `SetRemote`, `SetSecretsRepo`, `secretsrepo.Open`, and `grain state/secrets repo` (`pkg/staterepo/staterepo.go`, `pkg/daemon/statemanager.go`, `pkg/daemon/statepullrequests.go`, `pkg/secretsrepo/secretsrepo.go`, `pkg/cli/staterepo.go`, `pkg/cli/secretsrepo.go`)**:
   - *Symptom*: `Repo.ConfigureMirror` ran `git checkout -q -b <branch>` on empty repositories without validating `branch` via `git check-ref-format --branch` or rejecting leading `-`, `SetRemote` / `SetSecretsRepo` / `grain state repo` wrote `state-repo.json` / `secrets-repo.json` before validating `branch`, and `git check-ref-format --branch "@{-1}"` could exit `0` when the process working directory was a git checkout with checkout reflog history (printing the resolved branch name while the caller stored `@{-1}`).
   - *Fix*: Added `staterepo.ValidateBranch(branch)` and `secretsrepo.ValidateBranch(branch)` (rejecting leading `-` and verifying `git check-ref-format --branch` echoes the exact branch name rather than a resolved `@{-N}` reflog shorthand) and enforced them across `staterepo.Open`, `Repo.ConfigureMirror`, `StateManager.SetRemote`, `StateManager.SetSecretsRepo`, `StateManager.Compare`, `localRef`, `secretsrepo.Open`, `grain state repo`, and `grain secrets repo`.
9. **Local `grain/config` checks in `CiWorkflowSource.clone`, `StateCheckSandboxes.localRepo`, and `handleSetGitHubToken` (`pkg/daemon/ci.go`, `pkg/daemon/ci_statecheck.go`, `pkg/ui/github_tokens.go`)**:
   - *Symptom*: `CiWorkflowSource.clone` and `StateCheckSandboxes.localRepo` did not check whether local `grain/config` hosting was disabled in External mode (and `CiWorkflowSource.clone` also queried forge credentials / `ServerGitRemoteFor` for `grain/config`), and `PUT /api/github-tokens/{name}` auto-bootstrapped a default `GitConnections` row in `grain_config` even when in External read-only mode.
   - *Fix*: Guarded `CiWorkflowSource.clone` and `StateCheckSandboxes.localRepo` with `localConfigRepoEnabled(ConfigRepoDir)` (and bypassed forge credentials for `grain/config`), and guarded the auto-bootstrap `UpdateConfig` in `handleSetGitHubToken` with `checkSettingsWritable(ctx) == nil`.

---

## Automated Tests Added / Updated

- **Playwright E2E (`ui/e2e/config-repo-iac-modes.spec.js`)**:
  - Full 6-step end-to-end test against a real `grain daemon` and local bare Git repository exercising Internal mode, switching to External IaC mode, read-only banner and disabled fieldsets across all 7 repo-managed Settings routes at `1280×800` and `390×844`, mutable exceptions (`/settings/preferences`, `/settings/config-repo`, `/settings/credentials`), `409 Conflict` across API mutators, disabled local `grain/config` Git proxy and native compare, pulling external commits via UI `"Pull now"` and CLI `grain state sync` with zero daemon export commits, and switching back to Internal mode.
- **Go Unit & Integration Tests**:
  - `pkg/ui/staterepo_test.go` (`TestExternalConfigRepoRefusesSettingsMutationsAndExposesMode`): expanded to cover 26 mutator endpoints (`POST/PUT/PATCH/DELETE` across settings, templates, schedules, suites, boards, console-sessions, repos, workflows, qualification, agent-backends, git-connections, and gcp-keys) and verified side-effect-free `409 Conflict` refusals.
  - `pkg/gitproxy/local_test.go` (`TestDisabledLocalRepositoryRefusesRequestsInsteadOfForwarding`): verifies that a disabled `LocalRepository` returns `404` for `grain/config` without invoking credential selection or upstream forwarding.
  - `pkg/daemon/state_settings_repo_test.go` (`TestConfigRepoInternalAndExternalModes`, `TestStateManagerSyncDoesNotDeadlockOnCorruptSettingsFile`): verifies `StateRepoPullRequestClient` (including `EnqueuePullRequest` and `MergeQueueEntryOf`) and `StateManager.Compare` refuse `grain/config` operations in External mode (`404`) and succeed again after switching back to Internal mode, and that `StateManager.Sync` does not deadlock when `state-repo.json` is malformed.
