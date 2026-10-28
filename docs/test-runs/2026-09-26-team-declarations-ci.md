# Multi-Team Test Run: Track 6 — Team-Scoped Automations, Declarations, Boards & CI Observability

**Date**: 2026-09-26  
**Target Branch**: `grain/task-809`  
**Test Plan Reference**: `docs/test-plans/2026-09-26-multi-team-testing.md §3 Track 6`  
**Execution Environment**: Headless Playwright Chromium (`ui/e2e/team_declarations_ci_walkthrough.spec.js`, `ui/e2e/teams-walkthrough.spec.js`) against `grain demo` + Go backend test suite (`pkg/ci`, `pkg/repodecl`, `pkg/mcp`, `pkg/ui`, `pkg/orchestrator`).

---

## Executive Summary

This test run documents the end-to-end execution and validation of **Track 6 (Team-Scoped Automations, Declarations, Boards & CI Observability)** from the Multi-Team Testing Plan.

All 4 capability areas across Track 6 were verified via automated end-to-end browser walkthroughs and backend integration test suites:
1. **Declarative In-Repo Automation**: Verified that declarative team assignments configured in schedules (`.grain/schedules/*.yml` / `POST /api/schedules` with `team: "sec-ops"`) synthesize tasks assigned to the declared team upon firing, with proper attribution inheritance and UI filtering isolation.
2. **Team-Scoped Saved Boards**: Verified creation of team-scoped boards (`qa-infra`, `default`), global boards (`all`), Kanban column configurations, and real-time sidebar/index filtering when toggling the Global Team Selector.
3. **Interactive Console Sessions**: Verified session creation with team attribution (`qa-infra`), display of team chips in Sessions index, and reactive filtering across global and team views.
4. **CI Observability & Event Streaming**: Verified multi-team CI run filtering (`/ci?team=...`), MCP tool filtering (`list_ci_runs(team="...")`), and verified SSE/long-poll event stream isolation (`GET /api/teams/{id}/events`) ensuring zero cross-team task leakage.

---

## Test Verification Matrix

| Area / Subsystem | Verification Scope | Automated Suite / Harness | Result |
| :--- | :--- | :--- | :--- |
| **1. In-Repo Automation** | Declarative schedule `team` parsing, firing, task attribution to declared team, filter isolation. | `ui/e2e/team_declarations_ci_walkthrough.spec.js`, `pkg/repodecl`, `pkg/model` | **PASS** |
| **2. Saved Boards** | Team-scoped board creation (Kanban & Panels), sidebar count reactivity, Global Team Selector filtering. | `ui/e2e/team_declarations_ci_walkthrough.spec.js`, `ui/e2e/teams-walkthrough.spec.js` | **PASS** |
| **3. Console Sessions** | Console session creation under team, team chips, sidebar reactivity, settings page inspection. | `ui/e2e/team_declarations_ci_walkthrough.spec.js`, `pkg/orchestrator/consolesessions.go` | **PASS** |
| **4. CI Observability** | `/ci` page team filtering, Global Team Selector integration, `list_ci_runs(team="...")` MCP queries. | `pkg/ci/filter_test.go`, `pkg/mcp/grain_ci_wait_tool_test.go`, Playwright Chromium | **PASS** |
| **5. Team Event Stream** | SSE stream (`GET /api/teams/{id}/events?stream=true`) and long-poll isolation without cross-team leakage. | `ui/e2e/team_declarations_ci_walkthrough.spec.js`, `pkg/ui/team_events.go` | **PASS** |

---

## Detailed Findings

### 1. Declarative In-Repo Team Automation
- Evaluated schedule creation with explicit `team` fields (`team: "sec-ops"`).
- Verified `model.Store.FireScheduleNow` and `model.ComposeScheduleTask` correctly propagate `comp.Team = sched.Team` to generated tasks.
- In Playwright Chromium tests, fired the schedule and confirmed the resulting task (`Security Vulnerability Triage`) is assigned to `sec-ops`, visible under the `Security Operations` team view, and hidden under `QA & Infrastructure`.

### 2. Team-Scoped Saved Boards & Visualizations
- Created team-scoped board `Infra Sprint Board` (`team: "qa-infra"`, Kanban with columns `To Do`, `In Progress`, `Done`) and default board `Default Triage Board` (`team: "default"`).
- Navigated to `/boards` and verified:
  - When Global Team Selector is `All teams`, both boards are rendered in the index.
  - When Global Team Selector is switched to `QA & Infrastructure`, only `Infra Sprint Board` is visible, and default boards are hidden.
  - When switched to `Security Operations`, all non-SecOps boards are cleanly hidden.
  - Sidebar boards count reactively updates to reflect visible boards for the selected team.

### 3. Interactive Console Sessions
- Opened New Session dialog (`+ New session`) and created session `QA Auto-Test Session` targeting `qa-infra`.
- Verified session creation persists `session.team = "qa-infra"`.
- Verified Sessions index displays team badge `qa-infra`.
- Validated that switching the Global Team Selector filters out sessions belonging to other teams and preserves the active session when matching.

### 4. CI Observability & Multi-Team Filtering
- **UI Route & Filtering**: Verified `/ci` route and `CIPage.jsx` integration with `TeamContext` (`useTeam()`), correctly narrowing CI runs to `selectedTeam`.
- **MCP Tool Support**: Tested `list_ci_runs(team="qa-infra")` in `pkg/mcp/grain_ci_wait_tool_test.go` confirming `ci.RunFilter` correctly narrows runs to the specified team.
- **Backend Filter Tests**: Verified `TestRunFilterTeamScoping` in `pkg/ci/filter_test.go` covering exact matches, empty filter (all teams), default team fallback, and non-existent team queries.

### 5. Real-Time Event Streaming Isolation
- Tested `GET /api/teams/qa-infra/events` and `GET /api/teams/sec-ops/events`.
- Verified that team event payloads only contain tasks belonging to the requested team, preventing cross-tenant information disclosure.

---

## Test Execution Summary

- **Go Test Suite**: `go test ./pkg/...` — 100% PASS across 50+ packages.
- **Playwright Chromium E2E Suite**:
  - `ui/e2e/team_declarations_ci_walkthrough.spec.js` — **PASS** (7.1s)
  - `ui/e2e/teams-walkthrough.spec.js` — **PASS** (18.1s)
- **Asset Compilation**: `npm run build` cleanly produced updated production assets in `pkg/ui/static/`.

---

## Conclusion

Track 6 execution is complete. All declarative schedule automations, team-scoped saved boards, console sessions, CI observability filtering, and real-time event streams operate as specified with full isolation across teams.
