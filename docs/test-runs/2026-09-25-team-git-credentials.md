# Test Run Report: Per-Team Git Credentials and Commit Identities Walkthrough

**Date**: 2026-09-25  
**Test Plan Reference**: `docs/test-plans/2026-09-25.md` §1 ("Walkthrough: per-team git credentials and commit identities")  
**Target Branch**: `grain/task-664`  
**Environment**: Debian Linux x86_64, Go 1.26.6, Node 20.19.0, Playwright 1.57.0  

---

## 1. Executive Summary

This walkthrough validates that configuring per-team git commit identities (`AgentGitName`, `AgentGitEmail`, committer identity) and git credential secrets correctly propagates through:
1. The backend model and storage layers (`pkg/model/team_store.go`, `pkg/model/store.go`).
2. UI client & REST API handlers (`pkg/ui/teams.go`).
3. Orchestrator dispatch cycles and sandbox configuration (`pkg/orchestrator/cycle.go`, `pkg/mcp/git_credentials.go`).
4. The transparent git proxy authentication layer (`pkg/gitproxy/core.go`, `pkg/gitproxy/credentials.go`).
5. Running sandbox VM filesystems (`~/.gitconfig`, `~/.git-credentials`).
6. Subprocess execution of git commands (`git commit`, `git push`, `git log`).
7. MCP sandbox recreation lifecycles (`pkg/orchestrator/recreate.go`, `mcp_recreate_sandbox`).
8. Team isolation boundaries (preventing default/unauthorized teams from accessing restricted repositories or inheriting unintended commit signatures).

All 8 validation steps defined in `docs/test-plans/2026-09-25.md` §1 were implemented and verified with automated test suites in Go and Playwright browser walkthroughs.

---

## 2. Test Architecture and Verification Scope

### A. End-to-End Walkthrough Verification Suite (`tests/e2e/team_git_credentials_walkthrough_test.go`)

A dedicated, comprehensive Go end-to-end integration test was developed (`TestTeamGitCredentialsAndIdentityWalkthrough`), covering:
- **Upstream Git Simulation**: Configured simulated GitHub upstream server (`githubsim.Server`) where:
  - Default bot token (`e2eBotToken`) holds **read-only** permission to `acme/core-engine`.
  - Team secret token (`teamSecretToken` / `core-credential`) holds **read and write** permission to `acme/core-engine`.
- **Named Secret Store Integration**: Enhanced `worldConfig` in `tests/e2e/harness_test.go` with `withNamedCredential(teamCredName, teamSecretToken)` to place token secrets in `secrets/github/core-credential.token`.
- **Step 1: Team Configuration via UI REST Client**:
  - Invoked `uiClient.CreateTeam` creating `eng-core` with:
    - `AgentGitName: "Core Agent"`
    - `AgentGitEmail: "core-agent@example.com"`
    - `GitCredential: "core-credential"`
  - Verified stored team metadata in database store via `store.GetTeam`.
- **Step 2 & 3: Task Intake and Dispatch Cycle**:
  - Filed task `task-core-001` on repository `acme/core-engine` assigned to `eng-core`.
  - Executed orchestrator dispatch cycle (`orchestrator.RunCycle`).
  - Implemented `selectiveFramework` agent runner to capture live sandbox configuration.
- **Step 4: Sandbox VM Git Configuration Verification**:
  - Inspected generated `.gitconfig` in sandbox root.
  - Verified `[user] name = Core Agent` and `email = core-agent@example.com`.
  - Ran `git config --get user.name` and `git config --get user.email` inside sandbox checkout directory and confirmed exact match.
- **Step 5: Git Commit & Author/Committer Metadata Assertion**:
  - Created source file `engine.go` inside sandbox checkout directory.
  - Executed `git add` and `git commit -m 'feat(engine): add core version function'`.
  - Inspected commit format via `git log -n 1 --format="%an <%ae> %cn <%ce>"`.
  - Verified that author name/email and committer name/email both match `Core Agent <core-agent@example.com>`.
- **Step 6: Transparent Git Proxy Authentication & Push**:
  - Executed `git push origin grain/task-task-core-001` inside the sandbox through transparent git proxy.
  - Verified proxy matched `sandbox` -> live run -> task -> team `eng-core` -> credential `core-credential` (`teamSecretToken`).
  - Confirmed upstream simulated GitHub accepted push and created branch `grain/task-task-core-001`.
- **Step 7: Sandbox Recreation Persistence (`recreate_sandbox`)**:
  - Invoked MCP tool `recreate_sandbox` via `mcpserver.NewStdioClient`.
  - Verified sandbox VM destroyed and cleanly rebuilt.
  - Checked recreated `.gitconfig` to confirm team git identity and proxy helper persisted.
  - Made a second commit and executed `git push origin grain/task-task-core-001` from the recreated sandbox, verifying second commit format and successful push.
- **Step 8: Cross-Team Isolation Verification**:
  - Filed a task `task-def-001` assigned to default deployment team.
  - Dispatched `task-def-001` and inspected its sandbox.
  - Verified default task `.gitconfig` received deployment defaults (`Deployment Agent <deployment-agent@localhost>`).
  - Created commit and confirmed format matches deployment default identity.
  - Attempted `git push origin grain/task-task-def-001` to `acme/core-engine`.
  - Verified push was **rejected by upstream with HTTP 403 Forbidden** (`Write access to repository not granted`), proving that the default team cannot access or push to `acme/core-engine` without the team's credential grant.

---

## 3. Detailed Execution Results

### 1. E2E Test Suite Run (`TestTeamGitCredentialsAndIdentityWalkthrough`)
```bash
$ go test -v -run TestTeamGitCredentialsAndIdentityWalkthrough ./tests/e2e
=== RUN   TestTeamGitCredentialsAndIdentityWalkthrough
=== PAUSE TestTeamGitCredentialsAndIdentityWalkthrough
=== CONT  TestTeamGitCredentialsAndIdentityWalkthrough
    team_git_credentials_walkthrough_test.go:112: Step 1: Creating team eng-core via UI Client API with custom git identity and credential
    team_git_credentials_walkthrough_test.go:156: Step 2: Filing task for eng-core on repo acme/core-engine
    team_git_credentials_walkthrough_test.go:187: Step 3: Dispatching cycle for task-core-001
2026/09/25 21:13:59 orchestrator: filed task 1 for the director console session
    team_git_credentials_walkthrough_test.go:222: Step 4: Inspecting sandbox .gitconfig and testing git config values
    team_git_credentials_walkthrough_test.go:227: Sandbox .gitconfig:
        [credential]
        	helper = store
        [user]
        	name = Core Agent
        	email = core-agent@example.com
    team_git_credentials_walkthrough_test.go:257: Step 5: Executing git commit and verifying author and committer metadata
    team_git_credentials_walkthrough_test.go:267: Commit metadata format: "Core Agent <core-agent@example.com> Core Agent <core-agent@example.com>"
    team_git_credentials_walkthrough_test.go:273: Step 6: Executing git push through proxy and verifying team credential authentication
    team_git_credentials_walkthrough_test.go:276: git push output:
        To http://127.0.0.1:44633/acme/core-engine.git
         * [new branch]      grain/task-task-core-001 -> grain/task-task-core-001
    team_git_credentials_walkthrough_test.go:283: Step 7: Testing sandbox recreation via MCP recreate_sandbox tool
    team_git_credentials_walkthrough_test.go:289: recreate_sandbox report:
        Sandbox task-core-001-1 has been destroyed and rebuilt. Everything that was in it is gone, including anything you had not pushed.
        
        grain has put back:
        - its git credentials for grain's git proxy
        - a fresh clone of acme/core-engine at ./work, with grain/task-task-core-001 checked out (including anything you had already pushed to it)
        
        Start again from ./work. Anything you installed or built in the old sandbox you will have to do again.
    team_git_credentials_walkthrough_test.go:302: Making second commit from recreated sandbox and pushing
    team_git_credentials_walkthrough_test.go:327: Step 8: Proving isolation - verifying default team task receives deployment default identity and fails on unauthorized core repo push
    team_git_credentials_walkthrough_test.go:387: Default push correctly rejected upstream: remote: acme/core-engine: GitHub accepted the "bot" credential and refused it write access (403). The credential authenticated, so this is a permission it was never granted rather than a missing repository: a fine-grained token without Contents: write, an App installation granted read only, or a ruleset on this repository that refuses the push. Check what the token or App installation behind that credentials.json entry was granted on this repository. GitHub said: "Write access to repository not granted."
        fatal: unable to access 'http://127.0.0.1:44633/acme/core-engine.git/': The requested URL returned error: 403
    team_git_credentials_walkthrough_test.go:392: Per-Team Git Credentials & Identity Walkthrough completed successfully!
--- PASS: TestTeamGitCredentialsAndIdentityWalkthrough (6.46s)
PASS
ok  	github.com/ai-on-gke/grain/tests/e2e	6.488s
```

### 2. Full End-to-End Suite (`tests/e2e`)
```bash
$ go test -v ./tests/e2e
...
PASS
ok  	github.com/ai-on-gke/grain/tests/e2e	62.424s
```

### 3. Playwright Teams UI Walkthrough (`ui/e2e/teams-walkthrough.spec.js`)
```bash
$ cd ui && GRAIN_E2E_DEMO_BIN=/home/debian/work/bin/grain PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/teams-walkthrough.spec.js
Running 1 test using 1 worker

  ✓  1 [chromium] › e2e/teams-walkthrough.spec.js:6:3 › Teams UI and Multi-Team Lifecycle Operator Walkthrough › thoroughly exercises teams management, validation, global team selector, task intake, detail overlays, bulk edit, schedules, console sessions, and board integration in a real browser (26.2s)

  1 passed (31.7s)
```

---

## 4. Code & Test Artifacts Changed

- `tests/e2e/team_git_credentials_walkthrough_test.go` (NEW): Full 8-step end-to-end integration test validating team identity propagation, proxy authentication, sandbox recreation survival, and security isolation.
- `tests/e2e/harness_test.go`: Added `withNamedCredential` helper to write named token files under `secrets/github/<name>.token`.
- `tests/e2e/mcp_propose_task_walkthrough_test.go`: Attached `grain-ci` capability to test UI client harness.
- `ui/e2e/teams-walkthrough.spec.js`: Updated heading and select option regexes for `admin (default)` team matching.

---

## 5. Conclusion & Verification Verdict

The per-team git credentials and commit identities subsystem is robust, secure, and fully operational across all architectural layers. All criteria outlined in `docs/test-plans/2026-09-25.md` §1 are verified.
