# Track 5 Execution Report: Capability Scoping, Credential Isolation & Git Commit Identity

**Date**: 2026-09-26  
**Test Plan Reference**: `docs/test-plans/2026-09-26-multi-team-testing.md` §3 Track 5 ("Capability Scoping, Credential Isolation & Git Commit Identity")  
**Target Branch**: `grain/task-808`  
**Environment**: Debian Linux x86_64, Go 1.26.6, SQLite 3.x  

---

## 1. Executive Summary

This test run executes and verifies Track 5 of the Multi-Team Capabilities & Testing matrix (`docs/test-plans/2026-09-26-multi-team-testing.md` §3 Track 5). It validates:

1. **Capability Authorization Boundaries**: Configuring `AuthorizedTeams` on capability providers (`package-publish` scoped to `release-team`, `gcp-key` / `internal-gcp` scoped to `infra-team`).
2. **Unauthorized Capability Denial**: Disagreeing requests from unauthorized teams (`frontend-team`) are refused during grant resolution with `TeamUnauthorizedRefusal`, recorded into task audit logs, parked without agent dispatch, and zero credential files are placed into the sandbox filesystem.
3. **Authorized Capability Provisioning**: Authorized tasks (`infra-team` requesting `gcp-key` and `release-team` CI requesting `package-publish`) mount the required credentials and placements accurately into the sandbox filesystem (`/home/debian/.gcp-service-account.json` and `/home/debian/.package-publish-*`).
4. **Per-Team Git Commit Identity**: Tasks executed under custom teams (`devops` configured with `agentGitName: "DevOps Automated Bot"` and `agentGitEmail: "devops-bot@corp.internal"`) generate sandbox `.gitconfig` with matching identity and produce commits stamped with exact Author and Committer metadata matching the team's identity. Tasks executed under the `default` team retain deployment default identity.

All unit tests and end-to-end walkthrough verification suites passed cleanly.

---

## 2. Test Architecture and Verification Scope

### A. Authorization Resolution Ordering Fix (`pkg/model/capability.go`)
- **Problem Identified**: When `ResolveGrants` resolved capabilities, the CI check (`!cc.CI && !p.Spec().SupportsTask()`) occurred *before* the team check (`!p.Spec().SupportsTeam(cc.Task.Team)`). As a result, when an unauthorized team requested a CI-only capability such as `package-publish`, it was refused with `CIOnlyRefusal` rather than `TeamUnauthorizedRefusal`, masking the team RBAC boundary violation.
- **Resolution**: Reordered grant resolution in `pkg/model/capability.go` so `SupportsTeam` is checked first across both grant loops. Unauthorized teams now receive clear, unambiguous `TeamUnauthorizedRefusal` audit logs indicating team restriction.

### B. Capability Unit Testing
- **`pkg/capability/packagepublish/packagepublish_test.go` (`TestPackagePublishAuthorizedTeams`)**:
  - Validates that `package-publish` configured with `AuthorizedTeams: ["release-team"]` refuses tasks from `frontend-team` with `TeamUnauthorizedRefusal` without calling the credential ladder.
  - Validates that CI jobs from `release-team` resolve and materialize all 3 placement files (`.package-publish-registry`, `.package-publish-username`, `.package-publish-token`).
- **`pkg/capability/gcpkey/gcpkey_test.go` (`TestGCPKeyAuthorizedTeams`)**:
  - Validates that `gcp-key` configured with `AuthorizedTeams: ["infra-team"]` refuses tasks from `frontend-team` with `TeamUnauthorizedRefusal`.
  - Validates that tasks from `infra-team` resolve and materialize `.gcp-service-account.json`.

### C. End-to-End Walkthrough Verification (`tests/e2e/track5_team_capabilities_git_identity_test.go`)
- **`TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough`**:
  - **Step 1: Team & Capability Setup**:
    - Created teams `frontend-team`, `release-team`, `infra-team`, and `devops` via UI REST Client.
    - Configured `devops` with `AgentGitName: "DevOps Automated Bot"` and `AgentGitEmail: "devops-bot@corp.internal"`.
    - Registered `package-publish` (`AuthorizedTeams: ["release-team"]`) and `gcp-key` (`AuthorizedTeams: ["infra-team"]`).
  - **Step 2: Unauthorized Capability Denial**:
    - Filed task `task-fe-unauth-001` assigned to `frontend-team` requesting `package-publish` and `gcp-key`.
    - Executed `orchestrator.RunCycle`.
    - Asserted that task failed dispatch with refusal error: `capability "package-publish" refused: capability "package-publish" is restricted to authorized teams [release-team]; team "frontend-team" is not authorized`.
    - Asserted audit comment was written to the task thread and task was parked with `PendingQuestionCommentID`.
    - Verified sandbox filesystem was not populated with `.package-publish-token` or `.gcp-service-account.json`.
  - **Step 3: Authorized Capability Provisioning**:
    - Filed task `task-infra-auth-001` assigned to `infra-team` requesting `gcp-key`.
    - Dispatched cycle; task became live.
    - Asserted `/home/debian/.gcp-service-account.json` was placed and valid.
    - Verified `release-team` CI capability resolution placed `.package-publish-registry`, `.package-publish-username`, and `.package-publish-token`.
  - **Step 4: Per-Team Git Commit Identity**:
    - Filed task `task-devops-001` assigned to `devops`.
    - Dispatched cycle; inspected `.gitconfig` and verified `user.name = DevOps Automated Bot` and `user.email = devops-bot@corp.internal`.
    - Created code change and executed `git commit`.
    - Asserted `git log -n 1 --format="%an <%ae> %cn <%ce>"` returned `DevOps Automated Bot <devops-bot@corp.internal> DevOps Automated Bot <devops-bot@corp.internal>`.
    - Asserted `git log -1 --format=fuller` verified exact `Author` and `Commit` headers.
  - **Step 5: Default Team Identity**:
    - Filed task `task-def-001` under the default team.
    - Dispatched cycle; created code commit.
    - Asserted commit author/committer matched deployment defaults (`Deployment Default Agent <deploy-agent@example.com>`).

---

## 3. Detailed Execution Results

### 1. Unit Test Results
```
=== RUN   TestPackagePublishAuthorizedTeams
--- PASS: TestPackagePublishAuthorizedTeams (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/capability/packagepublish	0.004s

=== RUN   TestGCPKeyAuthorizedTeams
--- PASS: TestGCPKeyAuthorizedTeams (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/capability/gcpkey	0.014s
```

### 2. End-to-End Walkthrough Test Run
```
$ go test -v ./tests/e2e -run TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough
=== RUN   TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough
=== PAUSE TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough
=== CONT  TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough
    track5_team_capabilities_git_identity_test.go:133: Step 1: Setting up teams via UI API
    track5_team_capabilities_git_identity_test.go:179: Configuring scoped capabilities (package-publish -> release-team, gcp-key -> infra-team)
    track5_team_capabilities_git_identity_test.go:236: Step 2: Exercising Unauthorized Capability Denial for frontend-team
2026/09/27 00:12:02 orchestrator: filed task 1 for the director console session
2026/09/27 00:12:02 orchestrator: task task-fe-unauth-001 holds capability "package-publish", which its provider refuses (capability "package-publish" is restricted to authorized teams [release-team]; team "frontend-team" is not authorized); parking it on grain's own comment rather than retrying a dispatch that cannot work
    track5_team_capabilities_git_identity_test.go:276: Verified orchestrator returned refusal error during dispatch:
        dispatch: orchestrator: preparing task-fe-unauth-001-1: capability "package-publish" refused: capability "package-publish" is restricted to authorized teams [release-team]; team "frontend-team" is not authorized
    track5_team_capabilities_git_identity_test.go:291: Audit refusal comment verified:
        This task could not be dispatched: it holds the capability `package-publish`, and grain refuses it.
        
        > capability "package-publish" is restricted to authorized teams [release-team]; team "frontend-team" is not authorized
        
        No run can start while that is true, and every attempt fails in the same place -- before the agent exists, so nothing here can answer you. Grain has stopped rather than repeating it.
        
        Take `package-publish` off this task (its Capabilities, on the task's own pane), or close whatever gap the refusal names, and reply here to have it dispatched again.
    track5_team_capabilities_git_identity_test.go:324: Step 3: Exercising Authorized Capability Provisioning for infra-team (gcp-key)
    track5_team_capabilities_git_identity_test.go:370: Verified authorized GCP key placement:
        {"type":"service_account","project_id":"corp-infra","private_key_id":"key-infra-sa-001","client_email":"projects/corp-infra/serviceAccounts/infra-sa@corp-infra.iam.gserviceaccount.com"}
    track5_team_capabilities_git_identity_test.go:376: Verifying authorized CI capability resolution for release-team
    track5_team_capabilities_git_identity_test.go:397: Verified 3 placements for release-team: [{Side:sandbox Path:/home/debian/.package-publish-registry Content:ghcr.io Mode: Owner:} {Side:sandbox Path:/home/debian/.package-publish-username Content:release-bot Mode: Owner:} {Side:sandbox Path:/home/debian/.package-publish-token Content:sec-pkg-publish-token-999 Mode: Owner:}]
    track5_team_capabilities_git_identity_test.go:400: Step 4: Exercising Per-Team Git Commit Identity for devops team
    track5_team_capabilities_git_identity_test.go:442: Devops .gitconfig:
        [credential]
        	helper = store
        [user]
        	name = DevOps Automated Bot
        	email = devops-bot@corp.internal
    track5_team_capabilities_git_identity_test.go:457: Devops Commit format: "DevOps Automated Bot <devops-bot@corp.internal> DevOps Automated Bot <devops-bot@corp.internal>"
    track5_team_capabilities_git_identity_test.go:463: Devops git log fuller:
        commit e766665cdce616bff81602637bb941d0cb170b6f
        Author:     DevOps Automated Bot <devops-bot@corp.internal>
        AuthorDate: Sun Sep 27 00:12:06 2026 +0000
        Commit:     DevOps Automated Bot <devops-bot@corp.internal>
        CommitDate: Sun Sep 27 00:12:06 2026 +0000
        
            feat(devops): automated deployment health check
    track5_team_capabilities_git_identity_test.go:475: Step 5: Verifying default team task uses default repository Git identity
    track5_team_capabilities_git_identity_test.go:513: Default Commit format: "Deployment Default Agent <deploy-agent@example.com> Deployment Default Agent <deploy-agent@example.com>"
    track5_team_capabilities_git_identity_test.go:520: Track 5 walkthrough successfully completed!
--- PASS: TestTrack5CapabilityScopingCredentialIsolationAndGitIdentityWalkthrough (14.06s)
PASS
ok  	github.com/bwsalmon/grain/tests/e2e	14.082s
```

### 3. Full Repository Test Suite
All unit, package, and end-to-end test suites across the repository (`go test ./pkg/... ./tests/e2e/...`) pass with 0 failures.

---

## 4. Conclusion & Sign-Off

Track 5 of the Multi-Team testing plan is complete:
- Capability authorization boundaries are strictly enforced prior to task execution.
- Unauthorized capability requests fail dispatch safely without placing credentials in sandboxes and record clear audit logs.
- Authorized capability requests mount required credentials without leakage.
- Per-team Git identities are properly configured into sandboxes and accurately stamp commit metadata.
