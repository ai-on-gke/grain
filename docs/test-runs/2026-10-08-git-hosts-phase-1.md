# Test Run: Git Hosts Phase 1, Credential Hosts and Team Git Connections

This run executes §3 of `docs/test-plans/2026-10-07-evening.md` (task 2539, filed by task 2516).

## Summary

- **Real proxy in-place migration & wrong-host guard holds.** Through `pkg/gitproxy/hosts_walkthrough_test.go`, tested end-to-end against a real proxy with TLS upstreams:
  - An unconfigured host defaults to GitHub (`github.com`) and migrates in-place when set.
  - An attempt to use a credential scoped to host A against host B is refused cleanly with the exact required sentence:
    `credential "bot" is for git host ghe.internal, and this request goes to 127.0.0.1:<port> -- grain never sends one host's credential to another; add a git connection for 127.0.0.1:<port>`
  - No credential material leaked downstream; audit log records the refusal.
  - `make security` passed cleanly.
- **Agent and task notes observe the wrong-host refusal.** Through `tests/e2e/credential_refusal_test.go` (`TestARunWrongHostCredentialFailsAndTellsAgentAndTaskNotes`), verified that an agent attempting to push with a wrong-host credential receives the refusal sentence directly in tool output, the run fails, the task's failure streak records the exact sentence in `LastDetail`, and nothing lands upstream.
- **Store and state repo support verified across databases.**
  - **PostgreSQL (`pkg/model/postgres`):** Verified `git_connections` column CRUD operations (`TestTeamGitConnectionsCRUD`) on a real PostgreSQL instance under `GRAIN_REQUIRE_POSTGRES=1`.
  - **SQLite migration:** Tested with a pre-change database created before commit `6b08eaf95` (which lacked the `git_connections` column). Starting the `candidate` build cleanly ran `ensureColumn`, added `git_connections` with default `''`, and opened existing teams with empty connections (`[]`) without errors or data loss (`TestSQLitePreChangeMigrationOpensAndReadsEmptyConnections`).
  - **State repo export/import (`pkg/staterepo`):** Verified `staterepo.Export` dumps `git_connections` in `tables/team.json`. Tested manual edit and import round-trip. Verified that an import containing an extra unknown column from `main` (`anyone_may_propose`) is cleanly ignored by `candidate` without crashing or corrupting the record (`TestGitConnectionsStateRepoExportImport`).
- **Team settings save preserves connections & legacy vs connection resolution verified.**
  - Tested `PUT /api/teams/{id}` identity update in `pkg/ui/team_git_connections_ui_test.go`: connections survive intact in the store after team identity updates.
  - Verified that when both a legacy token (`team.gitCredential`) and default-host connection (`team.gitConnections`) exist:
    - Team settings (`GET /api/teams/{id}/github-credential`) continues to reflect the legacy token (as connections UI arrives in Phase 4/5).
    - Task clone/push resolution (`TeamGitCredentialOn`) picks the connection credential (rung 3 beats rung 4).
- **Caller agreement on GHE-default store holds.**
  - Verified `TeamGitCredential(ctx, team, owner, repo)` and `TeamGitCredentialOn(ctx, team, "ghe.example.com", owner, repo)` return identical credential names across:
    1. Explicit-host connection (`"ghe.example.com"`)
    2. Host-less connection (`Host: ""`)
    3. Legacy team with only `GitCredential` set.

## Environment

- Sandbox: Debian (kernel 6.1), Go 1.26.6, PostgreSQL 17.
- Working directory: `/home/debian/work`
- Branch: `grain/task-2539` (base `origin/candidate` at commit `9b0b38fc1`).
- Target repository: `bwsalmon/grain`.

## Stubs

- **No real secrets touched:** Neither this deployment's real `secrets/github/` nor any external network credentials were used or modified.
- **Upstream Git simulation:** Local `httptest.Server` instances with TLS and `githubsim` in-memory Git remotes were used to simulate GitHub and Enterprise Git hosts.

## Steps and Results

### 1. In-place migration & wrong-host guard through real proxy
- **Location:** `pkg/gitproxy/hosts_walkthrough_test.go` (`TestGitHostsPhase1ProxyMigrationAndGuard`)
- **Actions:**
  1. Spun up mock TLS Git upstream servers on loopback.
  2. Initialized `gitproxy.GitProxy` with credential store.
  3. Performed in-place host migration test (unconfigured -> explicit host).
  4. Executed wrong-host request using a credential tagged for `ghe.internal` against the test upstream server.
- **Verification:**
  - Status 403 / git-receive-pack refusal returned.
  - Refusal body contained: `credential "bot" is for git host ghe.internal, and this request goes to 127.0.0.1:... -- grain never sends one host's credential to another; add a git connection for 127.0.0.1:...`
  - Fixed a typed-nil issue in `pkg/gitproxy/server.go` (`cfg.Store` assigning typed nil to `CredentialOverrides` interface).
  - Executed `make security`: passed cleanly.

### 2. Refusal observed from agent push and task notes
- **Location:** `tests/e2e/credential_refusal_test.go` (`TestARunWrongHostCredentialFailsAndTellsAgentAndTaskNotes`)
- **Actions:**
  1. Configured task and test world with credential `bot` marked for `ghe-internal.example.com`.
  2. Dispatched agent run attempting to push to target repository on default host.
- **Verification:**
  - Push failed.
  - Agent tool output received exact refusal sentence:
    `credential "bot" is for git host ghe-internal.example.com, and this request goes to 127.0.0.1:41775 -- grain never sends one host's credential to another; add a git connection for 127.0.0.1:41775`
  - `store.FinishRun` recorded the output, and `store.FailureStreak(ctx, taskID)` confirmed `LastDetail` contained the exact refusal message.
  - Upstream branch was never created or modified.

### 3. Store and state repo
- **3.1 PostgreSQL:**
  - Created `pkg/model/postgres/team_git_connections_test.go` (`TestTeamGitConnectionsCRUD`).
  - Executed under `GRAIN_REQUIRE_POSTGRES=1`. Tested team creation with `GitConnections`, query, update, and empty serialization round-trips. Passed.
- **3.2 SQLite migration from pre-change database:**
  - Created pre-change database lacking `git_connections` column (using binary built prior to `6b08eaf95`).
  - Added test in `pkg/model/sqlite/team_git_connections_migration_test.go` (`TestSQLitePreChangeMigrationOpensAndReadsEmptyConnections`).
  - Candidate build ran `ensureColumn` cleanly on start, migrated table, and read existing teams with `GitConnections: []`.
- **3.3 & 3.4 State repo export/import and candidate/main compatibility:**
  - Created `pkg/staterepo/team_git_connections_staterepo_test.go` (`TestGitConnectionsStateRepoExportImport`).
  - Exported database to state repo directory; verified `tables/team.json` contains `git_connections` with expected JSON array.
  - Edited `git_connections` in `tables/team.json` and ran `staterepo.Import`; verified store reflected updated connections.
  - Added row containing unknown column `anyone_may_propose` (from `main` schema). Verified `staterepo.Import` on candidate cleanly ignores unknown columns without failing or corrupting store.

### 4. Team settings save & legacy vs connection
- **Location:** `pkg/ui/team_git_connections_ui_test.go` (`TestTeamSettingsSavePreservesGitConnections`)
- **Actions:**
  1. Stored a team having both `GitCredential: "legacy-token"` and `GitConnections: [{Credential: "conn-default-token", Host: "github.com"}, {Credential: "conn-ghe-token", Host: "ghe.example.com"}]`.
  2. Submitted `PUT /api/teams/{id}` with new `description`, `agentGitName`, and `agentGitEmail`.
  3. Re-read team: verified `GitConnections` remained untouched and intact.
  4. Queried `GET /api/teams/{id}/github-credential`: verified it reports on the legacy token (what UI displays).
  5. Called `store.TeamGitCredentialOn(ctx, id, "github.com", owner, repo)`: verified it resolves to `conn-default-token` (rung 3 connection precedence over rung 4 legacy credential).

### 5. Caller agreement on GHE-default store
- **Location:** `pkg/model/sqlite/team_git_connections_test.go` (`TestTeamGitCredentialOnFollowsTheDeploymentsDefaultHost`)
- **Actions & Verification:**
  - Verified that on a deployment with `GitDefaultHost = "ghe.example.com"`:
    - Scoped explicit-host connection returns `"infra"` from both `TeamGitCredential` and `TeamGitCredentialOn`.
    - Host-less connection (`Host: ""`) matches default host and returns `"hostless-conn"` from both methods.
    - Legacy-only team (`GitCredential: "legacy-cred"`) falls back and returns `"legacy-cred"` from both methods.
    - Request for `"github.com"` on GHE deployment correctly returns `model.ErrNoTeamGitCredential`.
