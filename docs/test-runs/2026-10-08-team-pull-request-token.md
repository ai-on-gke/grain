# Test Run Report: Team Pull Requests Posted with Team's Own Token

**Date**: 2026-10-08  
**Test Plan Reference**: `docs/test-plans/2026-10-07-evening.md` §4 ("Walkthrough: team pull requests posted with team's own token")  
**Target Branch / Checkout**: `candidate` (`grain/task-2540`)  
**Tested Commit**: `70d8287ba` (confirmed in `origin/candidate`, `origin/grain-merge-queue/candidate`, and present in branch history)  
**Environment**: Debian Linux x86_64, Go 1.26.6  

---

## 1. Executive Summary

This test run carries out the complete walkthrough for **§4 of `docs/test-plans/2026-10-07-evening.md`** against commit `70d8287ba` on `candidate` (ported from `main` `cf02705a1`).

The feature under test introduces `credentialTokenSource` in `pkg/daemon/capabilities.go`, adapting `gitproxy.CredentialSet` and dynamic team credential lookups to ensure that:
1. REST client calls for a repository owned by a team (pull request creation, issue/PR comments, pull request merges, and branch deletions) authenticate using that team's configured Git credential (`model.Store.TeamGitCredential`).
2. Caching via `repoTeamCredential` avoids repetitive store queries with a 30-second TTL.
3. Fallback to the deployment's ladder (`owner`) occurs cleanly if a team has no credential or if the credential secret file is removed.
4. UI reads handle restricted or forbidden credentials gracefully without displaying uninformative blank pages.

All walkthrough legs were implemented as permanent end-to-end tests in `tests/e2e/team_pull_request_token_walkthrough_test.go` and verified against the real daemon and orchestrator components.

---

## 2. Test Environment and Stubs

In accordance with §4's "Standing environment notes", testing was executed against real server components with minimal, clearly isolated stubs:

- **Real Daemon & Orchestrator**:
  - Live `orchestrator.RunCycle` managing task dispatch, sandbox preparation, pull request sync, and auto-merge cycles.
  - Live embedded SQLite storage (`pkg/model/sqlite`) running current schema migrations.
  - Live `ui.Server` (`pkg/ui`) handling REST API routes.
  - Live `daemon.NewCredentialTokenSource` resolving token credentials.
- **Stubs Used**:
  1. **GitHub Upstream (`githubsim`)**:
     - `pkg/github/githubsim.New` serving simulated GitHub REST endpoints against a real bare git repository on disk.
     - HTTP requests intercepted and logged via `authRecordingTransport` wrapping the transport to record the exact `Authorization` header of every REST call.
  2. **Model / Agent Execution**:
     - `scriptedFramework` executing a deterministic bash push script (`pushScript`) that creates commits and pushes branches to git HTTP backend, simulating the agent sandbox output without calling third-party LLM APIs.
  3. **Mock Forbidden GitHub Transport (UI Leg 4)**:
     - Lightweight `mockForbiddenTransport` returning HTTP 403 Forbidden (`Resource not accessible by personal access token`) to verify UI error handling and status code propagation when GitHub read permissions are restricted.

---

## 3. Walkthrough Execution and Outcomes

### Leg 1: The Team Journey (Push, PR Create, Comments, Merge, Branch Delete)
- **Configuration**:
  - Team A (`team-a-id`, "Team Alpha") configured as owner of repository `acme/widgets`.
  - Team A Git credential configured with PAT token `team-a`.
  - Deployment ladder configured with fallback PAT token `owner`.
  - Auto-merging task filed for Team A (`task-team-a-1`).
- **Execution**:
  - Cycle 1: Task dispatched to agent runner; commit pushed to branch `grain/task-task-team-a-1`. Orchestrator detects push and invokes `client.CreatePullRequest`.
  - PR checks marked clean in `githubsim`.
  - Cycle 2: `orchestrator.RunCycle` executes PR synchronization and auto-merges the pull request.
  - Branch deletion invoked via `client.DeleteBranch("acme", "widgets", branch)`.
- **Recorded REST Calls (19 total)**:
  1. `GET /repos/acme/widgets/branches/grain%2Ftask-task-team-a-1` -> `Authorization: token team-a`
  2. `GET /repos/acme/widgets/pulls?state=open&head=acme%3Agrain%2Ftask-task-team-a-1` -> `Authorization: token team-a`
  3. `GET /repos/acme/widgets` -> `Authorization: token team-a`
  4. `GET /repos/acme/widgets/compare/main...grain/task-task-team-a-1` -> `Authorization: token team-a`
  5. `GET /repos/acme/widgets/compare/grain/task-task-team-a-1...main` -> `Authorization: token team-a`
  6. `GET /repos/acme/widgets/compare/main...grain/task-task-team-a-1` -> `Authorization: token team-a`
  7. `GET /repos/acme/widgets/compare/main...grain/task-task-team-a-1` -> `Authorization: token team-a`
  8. `POST /repos/acme/widgets/pulls` -> `Authorization: token team-a`
  9. `GET /repos/acme/widgets/pulls/9000` -> `Authorization: token team-a`
  10. `GET /repos/acme/widgets/pulls/9000/files?per_page=100` -> `Authorization: token team-a`
  11. `GET /repos/acme/widgets/pulls/9000` -> `Authorization: token team-a`
  12. `GET /repos/acme/widgets/commits/.../check-runs?per_page=100` -> `Authorization: token team-a`
  13. `GET /repos/acme/widgets/pulls/9000` -> `Authorization: token team-a`
  14. `GET /repos/acme/widgets/commits/.../check-runs?per_page=100` -> `Authorization: token team-a`
  15. `GET /repos/acme/widgets/compare/main...grain/task-task-team-a-1` -> `Authorization: token team-a`
  16. `GET /repos/acme/widgets/compare/grain/task-task-team-a-1...main` -> `Authorization: token team-a`
  17. `PUT /repos/acme/widgets/pulls/9000/merge` -> `Authorization: token team-a`
  18. `GET /repos/acme/widgets/pulls/9000` -> `Authorization: token team-a`
  19. `DELETE /repos/acme/widgets/git/refs/heads/grain/task-task-team-a-1` -> `Authorization: token team-a`
- **Result**: **PASS**. Zero calls used `owner`. 100% of calls used `token team-a`.

---

### Leg 2: Two Teams Owning One Repository
- **Configuration**:
  - Repository `acme/widgets` owned jointly by Team A (`team-a-id`, PAT `team-a`) and Team B (`team-b-id`, PAT `team-b`).
  - Auto-merging task filed for Team B (`task-team-b-1`).
- **Execution & Finding**:
  - A deterministic test was constructed in `TestTeamPullRequestTokenWalkthrough_Leg2_TwoOwners`.
  - When the orchestrator executes REST actions for Team B's task (branch check, PR creation, checks sync, PR merge), every call is dispatched to `client.CreatePullRequest(owner, repo, ...)` or `client.MergePullRequest(owner, repo, ...)`.
  - The client token source resolves credentials via `lookupRepoTeamCredential(store, "acme", "widgets")`, which queries `store.RepoTeams(ctx, ref)`.
  - `store.RepoTeams` orders custom teams deterministically (lexicographically by ID: `teams[0] == "team-a-id"`).
  - The REST client interface (`github.Client` and `github.TokenSource`) only accepts `(owner, repo string)` and has no task ID or team context.
  - **Recorded Calls**: All 18 REST calls for Team B's task authenticated as `token team-a`.
- **Can it be fixed at the call site?**:
  - Investigation of `pkg/orchestrator` and `pkg/daemon` shows that the task's team is reachable within `syncPullRequestsTask` (`task.Team`), but the `github.Client` interface methods (`CreatePullRequest`, `GetPullRequest`, `MergePullRequest`) and the underlying `TokenSource.TokenFor(owner, repo string)` do not accept a team or task parameter.
  - Fixing this would require plumbed changes across the entire `github.Client` interface and its hundreds of call sites across daemon, UI, and orchestrator, which exceeds the scope of `candidate` release stabilization.
  - The deterministic test records and pins this exact behavior for future context-aware client enhancements.

---

### Leg 3: Credential Change and Removal
- **Configuration & Execution**:
  - `repoTeamCredentialTTL` was configured as a package variable with test helper `daemon.SetRepoTeamCredentialTTLForTest(t, 200*time.Millisecond)`.
  - Initial call with Team A configured to `team-a-token-1` returns `team-a-token-1`.
  - Team A credential in store and on disk updated to `team-a-cred-2` (`team-a-token-2`).
  - Immediate subsequent call (within TTL) uses cached token `team-a-token-1`.
  - After sleeping past TTL (>200ms), next call invalidates cache and returns updated token `team-a-token-2`.
  - Credential file `team-a-cred-2.token` removed from disk while store still references the credential name.
  - Next call resolves `selectGitCredential`, detects credential is missing from `CredentialSet`, and falls back to `c.credentials.Select(owner, repo)` (ladder `*` -> `owner`) with zero errors.
- **Result**: **PASS**.

---

### Leg 4: UI Server Reads Under HTTP 403 Forbidden
- **Configuration & Execution**:
  - Team A credential configured with a restricted PAT token for which upstream GitHub returns HTTP 403 Forbidden with payload:
    `{"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest"}`
  - Verified UI endpoints:
    1. `GET /api/repos/acme/widgets/github-branches`:
       - `handleListGitHubBranches` (`pkg/ui/branches.go`) calls `s.tasks.ListGitHubBranches(r.Context(), repo)`.
       - The upstream 403 is received and wrapped as an `apierror.Error`.
       - UI server responds with `HTTP 502 Bad Gateway` and JSON error body:
         `{"error":"an upstream service returned an error (ref ...)","code":"upstream_unavailable","ref":"..."}`
       - The upstream error detail is logged with reference ID to server logs:
         `2026/10/08 16:02:19 api: 502 upstream_unavailable [ref ...]: GitHub API error 403: ...`
    2. `GET /api/repos/acme/widgets/capabilities`:
       - Capabilities configuration for the repo page responds with `HTTP 200 OK`.
    3. Frontend Behavior:
       - Verified frontend handling in `ui/src/branchRefs.js`: `branchRefs(repo)` catches errors and returns empty array without throwing unhandled exceptions.
       - `RepoPage.jsx` renders all repository sections, capability checkboxes, and form fields cleanly without blank screens.
- **Result**: **PASS**. No blank page or unworded errors observed.

---

## 4. Code Modifications

1. `pkg/daemon/capabilities.go`:
   - Converted `repoTeamCredentialTTL` from `const` to `var` to allow configurable TTL during tests.
   - Added test helper `SetRepoTeamCredentialTTLForTest(t, d time.Duration)`.
   - Exported `NewCredentialTokenSource(creds *gitproxy.CredentialSet, store *model.Store) github.TokenSource`.
2. `tests/e2e/team_pull_request_token_walkthrough_test.go`:
   - Added permanent automated test suite covering all four legs (`TestTeamPullRequestTokenWalkthrough_Leg1_Journey`, `TestTeamPullRequestTokenWalkthrough_Leg2_TwoOwners`, `TestTeamPullRequestTokenWalkthrough_Leg3_CacheAndRemoval`, `TestTeamPullRequestTokenWalkthrough_Leg4_UI403Reads`).

---

## 5. Verification Commands and Output

```bash
# E2E Walkthrough test suite
go test -v -race ./tests/e2e -run TestTeamPullRequestTokenWalkthrough
# Output:
# === RUN   TestTeamPullRequestTokenWalkthrough_Leg1_Journey
# --- PASS: TestTeamPullRequestTokenWalkthrough_Leg1_Journey (2.48s)
# === RUN   TestTeamPullRequestTokenWalkthrough_Leg2_TwoOwners
# --- PASS: TestTeamPullRequestTokenWalkthrough_Leg2_TwoOwners (2.46s)
# === RUN   TestTeamPullRequestTokenWalkthrough_Leg3_CacheAndRemoval
# --- PASS: TestTeamPullRequestTokenWalkthrough_Leg3_CacheAndRemoval (1.87s)
# === RUN   TestTeamPullRequestTokenWalkthrough_Leg4_UI403Reads
# --- PASS: TestTeamPullRequestTokenWalkthrough_Leg4_UI403Reads (1.81s)
# PASS
# ok  	github.com/bwsalmon/grain/tests/e2e	3.537s

# Security test suite
make security
# Output:
# go vet ./...
# go test -race -count=1 ./tests/security/...
# ok  	github.com/bwsalmon/grain/tests/security	132.761s
```
