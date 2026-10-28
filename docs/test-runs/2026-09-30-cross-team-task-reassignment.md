# Test Run Report: Cross-Team Task Reassignment & Dynamic Team Slug Generation

**Date**: 2026-09-30  
**Reference Test Plan**: `docs/test-plans/2026-09-30.md §2`  
**Environment**: Debian bookworm, Linux x86_64, Go `1.24+`  
**Execution Type**: Live `grain demo` CLI & REST API Walkthrough, Comprehensive End-to-End Integration Suite (`pkg/ui`), and Store/Model Unit Tests (`pkg/model`, `cmd/grain`)  

---

## Executive Summary

An end-to-end operator walkthrough and automated validation of **Cross-Team Task Reassignment** and **Dynamic Team Slug Generation** was executed against a live `grain demo` server and SQLite store in multi-team mode.

All 8 specific validation scenarios mandated by `docs/test-plans/2026-09-30.md §2` were thoroughly verified:
1. Multi-team daemon startup and initial state inspection via `grain demo`.
2. Dynamic slug generation upon team creation without explicit ID ("Frontend Devs" $\rightarrow$ `frontend-devs`).
3. Creation of teams with explicit slugs and target repository allowlists (e.g. `backend-core` with `targetRepos: ["backend/*"]` and `mobile` with `targetRepos: ["mobile/*"]`).
4. Task creation under `frontend-devs` targeting `backend/api` (`Task A`).
5. Task movement to `backend-core` using both CLI (`grain team move <task-id> backend-core`) and REST API (`POST /api/tasks/{id}/move` / `PATCH /api/tasks/{id}`), verifying immediate backlog and repo association updates.
6. Rejection of off-allowlist task movement (attempting to move Task A to `mobile` whose allowlist excludes `backend/api` returns HTTP 400 Bad Request with a clear, descriptive validation error).
7. Task reassignment across lifecycle states (`queued` and `awaiting_reply`), verifying state preservation, immediate ready queue reflection (`ReadyForTeam`), team concurrency scoping, and team event stream routing.
8. REST API team slug resolution across all endpoints (`/api/teams/frontend-devs`, `/api/teams/backend-core`, `/api/teams/mobile`).

---

## Detailed Walkthrough & Verification Steps

### 1. Launch `grain demo` with Multi-Team Mode Enabled
- Launched `grain demo -addr 127.0.0.1:8434 -open=false`.
- The embedded daemon initialized the SQLite schema and seeded demo tasks across various states.
- Multi-team functionality was verified active with the built-in `default` team registered.

### 2. Dynamic Team Slug Generation ("Frontend Devs")
- **Request**: Sent `POST /api/teams` with JSON body:
  ```json
  {
    "name": "Frontend Devs",
    "description": "Frontend development team"
  }
  ```
- **Result**: Team created with status `201 Created`. The server automatically derived slug `frontend-devs` via `NewTeamSlug` / `SlugifyTeamID`.
- **Response**:
  ```json
  {
    "id": "frontend-devs",
    "name": "Frontend Devs",
    "description": "Frontend team",
    "configRepo": "grain/config-frontend-devs",
    "configCloneUrl": "/grain/config-frontend-devs.git",
    "cloneCommand": "git clone /grain/config-frontend-devs.git",
    "maxWorkers": 0,
    "maxMergers": 0,
    "maxCIJobs": 0,
    "users": ["debian"],
    "admins": ["debian"],
    "members": [{"user": "debian", "role": "admin"}]
  }
  ```

### 3. Create Teams with Custom Slugs & Target Repo Allowlists
- **Backend Core Team**:
  - `POST /api/teams` with `{"id": "backend-core", "name": "Backend Core", "description": "Backend team", "targetRepos": ["backend/*"]}`.
  - Successfully created with `id: "backend-core"` and `targetRepos: ["backend/*"]`.
- **Mobile Team**:
  - `POST /api/teams` with `{"id": "mobile", "name": "Mobile", "description": "Mobile team", "targetRepos": ["mobile/*"]}`.
  - Successfully created with `id: "mobile"` and `targetRepos: ["mobile/*"]`.

### 4. Dispatch Task A under `frontend-devs` Targeting `backend/api`
- **Command**:
  ```bash
  grain -json create -title "Task A: GraphQL API Gateway" -team frontend-devs -repo backend/api -approve
  ```
- **Result**: Task `13` created in `queued` state assigned to team `frontend-devs` with repo `backend/api`.
- Verified `ReadyForTeam("frontend-devs")` contains Task `13`, while `ReadyForTeam("backend-core")` is empty.

### 5. Cross-Team Task Movement to `backend-core`
- **CLI Execution**:
  ```bash
  grain team move 13 backend-core
  ```
  *Output*: `Task 13 moved to team "backend-core".`
- **REST API Verification**:
  - Verified `GET /api/tasks/13` returned `{"id": "13", "team": "backend-core", "state": "queued", ...}`.
  - Verified `GET /api/teams/backend-core` returns `repos` and `associatedRepos` containing `backend/api`.
  - Verified `ReadyForTeam("frontend-devs")` is now empty `[]`, and `ReadyForTeam("backend-core")` returns `["13"]`.

### 6. Off-Allowlist Validation & Error Rejection
- **Attempt**: Move Task `13` (targeting `backend/api`) to team `mobile` (which only allows `mobile/*`).
- **REST API Execution**: `POST /api/tasks/13/move` with `{"team": "mobile"}`.
- **Result**: Rejected with HTTP `400 Bad Request`.
- **Error Response**:
  ```json
  {
    "error": "backend/api is not on this deployment's target repo list, so this task cannot be moved onto it: nothing here can clone, push to, or open a pull request against a repo the list does not name. The list currently holds `mobile/*` -- an operator widens targetRepos under Settings."
  }
  ```
- **Verification**: Confirmed Task `13` team remains intact on `backend-core`.

### 7. Task Movement Across Lifecycle States (`queued` & `awaiting_reply`)
- **Queued Task**:
  - Created approved task in `frontend-devs`.
  - Moved to `backend-core`.
  - Verified state remained `queued` and ready queue immediately updated from `frontend-devs` to `backend-core`. Concurrency limits for `backend-core` now evaluate this task upon next dispatch cycle.
- **Awaiting Reply Task**:
  - Created task parked on an agent question with `PendingQuestionCommentID` (state: `awaiting_reply`).
  - Moved task to `backend-core`.
  - Verified task state remained `awaiting_reply` and team updated to `backend-core`.
  - Reconnected event listeners on `/api/teams/backend-core/events` received the updated task notification.

### 8. REST API Team Slug Resolution
- `GET /api/teams/frontend-devs` $\rightarrow$ `200 OK` (`Frontend Devs`)
- `GET /api/teams/backend-core` $\rightarrow$ `200 OK` (`Backend Core`)
- `GET /api/teams/mobile` $\rightarrow$ `200 OK` (`Mobile`)
- `GET /api/teams/non-existent-slug` $\rightarrow$ `404 Not Found` (`{"error": "no team non-existent-slug"}`)

---

## Code Changes & Bug Fixes

1. **Target Repository Wildcard Matching (`pkg/model/repo_scope.go`)**:
   - Enhanced `model.RepoAllowed` to evaluate glob wildcard patterns (`backend/*`, `*/*`, `*`) against repository targets, ensuring wildcard target repo allowlists configured on teams are correctly honored during dispatch and cross-team movement.
2. **Task Movement REST API Endpoint (`pkg/ui/server.go`, `pkg/ui/tasks.go`, `pkg/ui/httpclient.go`)**:
   - Added dedicated `POST /api/tasks/{id}/move` endpoint supporting `{ "team": "<team-id>" }` (and `team_id`) alongside `PATCH /api/tasks/{id}`.
   - Added `MoveTask(ctx, id, team)` method to `ui.HTTPClient`.
3. **Automated Integration & Unit Test Coverage**:
   - Created `pkg/ui/cross_team_walkthrough_test.go` covering the complete 8-step walkthrough test plan.
   - Added `TestTaskMovePostEndpoint` in `pkg/ui/task_team_assignment_test.go`.
   - Added `TestRepoAllowedWildcards` in `pkg/model/repo_scope_test.go`.

---

## Verification Logs

### Automated Go Integration Test
```
=== RUN   TestCrossTeamTaskReassignmentAndDynamicSlugWalkthrough
=== PAUSE TestCrossTeamTaskReassignmentAndDynamicSlugWalkthrough
=== CONT  TestCrossTeamTaskReassignmentAndDynamicSlugWalkthrough
--- PASS: TestCrossTeamTaskReassignmentAndDynamicSlugWalkthrough (0.08s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/ui	0.094s
```

### Full Repository Test Suite
```
ok  	github.com/ai-on-gke/grain/cmd/grain	22.326s
ok  	github.com/ai-on-gke/grain/pkg/model	4.027s
ok  	github.com/ai-on-gke/grain/pkg/orchestrator	112.182s
ok  	github.com/ai-on-gke/grain/pkg/ui	52.071s
ok  	github.com/ai-on-gke/grain/tests/e2e	109.836s
All packages PASS (0 failures across the entire repository).
```
