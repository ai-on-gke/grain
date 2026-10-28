# Test Run Report: Multi-Team Administration, RBAC Enforcement & Lifecycle Safeguards

**Date**: 2026-09-26  
**Track**: Track 1 — Multi-Team Administration, RBAC Enforcement & Lifecycle Safeguards  
**Test Plan Reference**: `docs/test-plans/2026-09-26-multi-team-testing.md` §3 Track 1  
**Author/Runner**: Antigravity  

---

## 1. Overview & Objectives

This test run executed the complete end-to-end operator and user journey for **Track 1: Multi-Team Administration, RBAC Enforcement & Lifecycle Safeguards** across the Grain system.

### Key Objectives Exercised:
1. **Default Team Inspection & Protections**:
   - Verify Default Team card rendering in `Admin Overlay -> All Teams` tab.
   - Verify resource limit chips (`Max Workers`, `Max Mergers`, `Max CI Jobs`).
   - Verify omission of `Delete Team` action on the default team card (built-in protection).
   - Test Remote Executor bootstrap command copy button on the default team (`--team default`).
2. **Team Creation & Field Validation**:
   - Form field validations: required team name, rejection of empty members/admins list.
   - Slug / ID validation: rejection of invalid slug characters (spaces `platform eng`, uppercase/special chars `Platform@Eng!`).
   - Successful creation of team `platform-eng` (`Platform Engineering`) with:
     - Name: `Platform Engineering`
     - Slug ID: `platform-eng`
     - Description: `Platform engineering infrastructure & automation team`
     - Resource limits: `Max Workers: 12`, `Max Mergers: 4`, `Max CI Jobs: 8`
     - IANA Timezone: `America/New_York`
     - Default Model: `claude-sonnet-5`
     - Git Identity: Name `Platform Bot`, Email `platform-bot@example.com`
     - Role-based membership: `alice:admin`, `bob:member`, `charlie:viewer`
   - Verification of rendered card metadata and chip badges in the All Teams list.
   - Verification of Remote Executor bootstrap command copy on the custom team (`--team platform-eng`).
3. **Member Management & Admin Invariant Enforcement**:
   - Editing team membership.
   - Enforcing "at least one admin" invariant when attempting to demote or remove all admins (`"a team must always have at least one admin"`).
   - Enforcing "at least one user" invariant when attempting to clear all members (`"a team must always have at least one user with permission to access it"`).
   - Promoting `dave:admin`, demoting `alice:member`, removing `alice`, and verifying live UI chip updates.
4. **RBAC Permission Enforcement Across Roles**:
   - **Viewer Role (`charlie`)**:
     - Attempting to submit tasks for `platform-eng` rejected (HTTP 400: `"user charlie does not have permission to submit tasks for team platform-eng"`).
     - Attempting to modify team settings rejected (HTTP 400: `"user charlie does not have permission to manipulate team platform-eng"`).
     - Attempting to delete team rejected (HTTP 400: `"user charlie does not have permission to delete team platform-eng"`).
   - **Member Role (`bob`)**:
     - Task submission succeeds (HTTP 200: task created with `team: platform-eng`).
     - Attempting to modify team settings rejected (HTTP 400).
     - Attempting to delete team rejected (HTTP 400).
   - **Admin Role (`dave`)**:
     - Team settings modification succeeds (HTTP 200).
5. **Lifecycle Safeguards & Clean Deletion**:
   - Attempting to delete `platform-eng` with active tasks attached is rejected with error feedback (`cannot delete a team that still has tasks`).
   - Task reassigned to `default`.
   - Attempting to delete `platform-eng` with an active schedule attached is rejected with error feedback (`cannot delete a team that still has tasks`).
   - Schedule deleted.
   - Clean deletion succeeds and `platform-eng` card is removed from DOM, leaving only `Default Team`.
6. **Console & Network Error Audit**:
   - Audited browser console and network telemetry; zero unexpected errors or unhandled rejections occurred.

---

## 2. Environment & Test Matrix

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-804`
- **Host System**: Debian GNU/Linux 12 (bookworm), Linux x86_64
- **Runtime**: Go `1.26.6`, Node.js `20.19.0`, npm `10.8.2`
- **Test Frameworks**: Playwright `1.57.0` (Chromium headless), Vitest `4.1.11`, Go `testing`
- **E2E Test Specifications**:
  - `ui/e2e/team-admin-rbac-walkthrough.spec.js` (Track 1 dedicated walkthrough)
  - `ui/e2e/teams-walkthrough.spec.js` (Comprehensive multi-team operator walkthrough)
- **Unit Test Suites**:
  - Go: `pkg/model`, `pkg/ui`
  - Vitest: 147 test files (2,593 unit tests)

---

## 3. Findings & Bugs Resolved

### 1. Backend Deletion Safeguard Missing Schedule Check (`pkg/model/team_store.go`)
- **Problem**: `DeleteTeam` in `pkg/model/team_store.go` checked for attached `task` rows (`SELECT COUNT(*) FROM task WHERE team = ?`) to return `ErrTeamNotEmpty`, but did not check for attached `schedule` rows. Deleting a team with active schedules would succeed and orphan the schedules instead of rejecting the deletion.
- **Fix**: Added `SELECT COUNT(*) FROM schedule WHERE team = ?` check in `DeleteTeam`. If active schedules exist, `DeleteTeam` returns `ErrTeamNotEmpty`.
- **Test Coverage**: Added test case in `pkg/model/team_test.go` (`TestTeamDeletionInvariants`) verifying `DeleteTeam` returns `ErrTeamNotEmpty` when schedules are attached.

### 2. Admin Invariant Enforcement During Update (`pkg/ui/teams.go` and `ui/src/components/TeamsPanel.jsx`)
- **Problem**: In `UpdateTeam` (`pkg/ui/teams.go`), if an update payload resulted in 0 admins, it was auto-promoting the first member instead of returning a validation error unless `req.Admins != nil && len(*req.Admins) == 0`. In `TeamsPanel.jsx`, an empty admin field passed `admins: undefined`, skipping backend admin checks.
- **Fix**:
  - In `pkg/ui/teams.go`, updated `UpdateTeam` to return `validationErrorf("a team must always have at least one admin")` if `len(t.Admins) == 0`.
  - In `ui/src/components/TeamsPanel.jsx`, added client-side validation in `handleSave` to immediately reject saving when no admin role is present, and added `defaultModel` input field and card chip display.
- **Test Coverage**: Exercised and verified in `ui/e2e/team-admin-rbac-walkthrough.spec.js`.

---

## 4. Test Execution Results

### 1. Go Unit & Integration Tests
```text
$ go test -v ./pkg/model -run "Test.*Team"
=== RUN   TestResolveGrantsTeamScoping
--- PASS: TestResolveGrantsTeamScoping (0.00s)
=== RUN   TestTeamConfigRepoResolution
--- PASS: TestTeamConfigRepoResolution (0.00s)
=== RUN   TestComposeScheduleTaskBindsTeamFromSchedule
--- PASS: TestComposeScheduleTaskBindsTeamFromSchedule (0.00s)
=== RUN   TestComposeTemplateTaskPreservesTeam
--- PASS: TestComposeTemplateTaskPreservesTeam (0.00s)
=== RUN   TestTeamLimits
--- PASS: TestTeamLimits (0.00s)
=== RUN   TestTeamUserMethods
--- PASS: TestTeamUserMethods (0.00s)
=== RUN   TestTeamAdminMethods
--- PASS: TestTeamAdminMethods (0.00s)
=== RUN   TestTeamConfigRepoMethod
--- PASS: TestTeamConfigRepoMethod (0.00s)
=== RUN   TestTeamDeletionInvariants
--- PASS: TestTeamDeletionInvariants (0.06s)
=== RUN   TestEnsureDefaultTeam
--- PASS: TestEnsureDefaultTeam (0.12s)
=== RUN   TestTeamRolesAndRBAC
--- PASS: TestTeamRolesAndRBAC (0.08s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/model	0.578s

$ go test -v ./pkg/ui -run "Test.*Team"
=== RUN   TestTeamsEndpoints
--- PASS: TestTeamsEndpoints (0.06s)
=== RUN   TestTeamPermissionsAndUserAccess
--- PASS: TestTeamPermissionsAndUserAccess (0.05s)
=== RUN   TestTeamArchivalAPIAndInvariants
--- PASS: TestTeamArchivalAPIAndInvariants (0.06s)
=== RUN   TestTeamModelConfiguration
--- PASS: TestTeamModelConfiguration (0.04s)
=== RUN   TestSecretsEndpointsTeamScoping
--- PASS: TestSecretsEndpointsTeamScoping (0.04s)
=== RUN   TestAnyUserCanCreateTeamAndBecomesAdmin
--- PASS: TestAnyUserCanCreateTeamAndBecomesAdmin (0.10s)
=== RUN   TestTeamRepositoryAssociations
--- PASS: TestTeamRepositoryAssociations (0.05s)
=== RUN   TestTeamEditViaHTTPEndpoint
--- PASS: TestTeamEditViaHTTPEndpoint (0.05s)
=== RUN   TestTeamBroadcastEndpoint
--- PASS: TestTeamBroadcastEndpoint (0.05s)
=== RUN   TestTeamGitCredentialAndIdentity
--- PASS: TestTeamGitCredentialAndIdentity (0.04s)
=== RUN   TestTeamTargetRepos
--- PASS: TestTeamTargetRepos (0.05s)
=== RUN   TestTeamCopyCredentials
--- PASS: TestTeamCopyCredentials (0.04s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/ui	2.627s
```

### 2. UI Vitest Suite
```text
$ cd ui && npm test
 Test Files  147 passed (147)
      Tests  2593 passed (2593)
   Duration  166.90s
```

### 3. Playwright E2E Walkthroughs
```text
$ PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/team-admin-rbac-walkthrough.spec.js e2e/teams-walkthrough.spec.js

Running 2 tests using 2 workers

  ✓  1 [chromium] › e2e/team-admin-rbac-walkthrough.spec.js:6:3 › Track 1: Multi-Team Administration, RBAC Enforcement & Lifecycle Safeguards › executes complete Track 1 walkthrough: Admin Overlay, Default Team inspection, validation, RBAC, safeguards, and deletion (4.4s)
  ✓  2 [chromium] › e2e/teams-walkthrough.spec.js:6:3 › Teams UI and Multi-Team Lifecycle Operator Walkthrough › thoroughly exercises teams management, validation, global team selector, task intake, detail overlays, bulk edit, schedules, console sessions, and board integration in a real browser (19.9s)

  2 passed (25.3s)
```

---

## 5. Conclusion & Verification Summary

Track 1 multi-team administration, RBAC enforcement, and lifecycle safeguards are fully verified, robust, and protected by comprehensive automated test suites at the unit, integration, and browser E2E levels.
