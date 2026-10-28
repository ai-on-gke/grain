# Test Run: Repository Ownership Models and CI Capability Security Boundaries

The pass `docs/test-plans/2026-09-23.md` §5 specifies: an end-to-end walkthrough and verification procedure for repository ownership models and CI capability security boundaries across `RepoPage`, work ownership state transitions (`unclaimed`, `managed`, `read-only`), CI capability event gating (`SupportsCIEvent`), and team-scoped config repository submission permissions.

## Result

**Pass**. All walkthrough scenarios, UI ownership state toggles, API endpoints, CI capability event restrictions, and team-scoped config repo boundaries executed cleanly without error.

1. **Repository Ownership Model State Transitions**:
   - Verified that `RepoPage.jsx` and `RepoWorkOwnership.jsx` correctly render and persist the three distinct repository work ownership states:
     - **Unclaimed / Unset** (`default = ""`): Displays `Nobody has said` and resolves to `Runs this repository's work` (default runs).
     - **Read-Only** (`default = "off"`): Displays `Off, set here` and resolves to `Does not run this repository's work` (`defaultRuns = false`).
     - **Managed** (`default = "on"`): Displays `On, set here` and resolves to `Runs this repository's work` (`defaultRuns = true`).
   - Verified that individual work item overrides (e.g., `release` and declared schedules) allow granular control (`On`, `Off`, `Say nothing`) while inheriting the repository default when unconfigured.
2. **Read-Only Repository Enforcement**:
   - Verified that setting repository default ownership to `off` properly reflects across backend APIs (`/api/repos/:owner/:name/work-ownership`).
   - Verified that task creation rules (`refuseTargetAmongReads`) strictly enforce read-only repository boundaries and prevent improper mixing of target and read-only specifications.
3. **CI Capability Security Boundaries (`SupportsCIEvent`)**:
   - Verified that sensitive write-scoped CI capabilities (`package-publish`, `repo-write:<repo>`) implement `model.CIEventCapability` and restrict execution to trusted events (`release`, `workflow_dispatch`).
   - Proved that untrusted external events (`pull_request`, `push`) attempting to request sensitive write capabilities have those capabilities strictly stripped by `cmd/grain/ci.go` (`resolveCICapabilities`), executing jobs in an isolated read-only sandbox.
4. **Team-Scoped Config Repository Permissions**:
   - Verified that `grain/config-<team>` repositories enforce strict task submission boundaries:
     - Agent proposals from another team's tasks are rejected with `only agents from team "<team>" can submit to its config repo`.
     - Human task submissions from users not belonging to the target team (and not system administrators) are rejected with `user "<user>" does not have permission to submit tasks for team "<team>"`.
     - Submissions by authorized team members succeed and inherit the appropriate team assignment.

## Test Environment and Setup

- Platform: Debian GNU/Linux 12 (bookworm), Linux x86_64 sandbox.
- Go runtime: Go 1.26.6 linux/amd64.
- Node runtime: Node 20.19.0, npm 10.8.2.
- Browser: Playwright 1.62.1 with Chromium 151.0.7922.34 (Chrome for Testing) at 1440×1100.
- Grain build: `make frontend grain grit` producing static assets in `pkg/ui/static` and binary `bin/grain`.
- Server environment: Live `bin/grain demo` instance backed by embedded SQLite database store (`model.Store`) and HTTP API server (`pkg/ui.Server`).
- Automated specs:
  - Playwright E2E walkthrough: `ui/e2e/repo-ownership-ci-security.spec.js`
  - Vitest UI component suite: `ui/src/components/RepoWorkOwnership.test.jsx`
  - Go CI capability security suite: `cmd/grain/ci_capabilities_test.go`
  - Go repository ownership & team permissions suite: `pkg/ui/repo_work_ownership_test.go`, `pkg/ui/client_test.go`

## Walkthrough Steps and Observations

### 1. Repository Ownership State Transitions (`RepoPage.jsx`)

Navigated to `/repo/bwsalmon/grain` in the live UI and inspected the "Work ownership" management section:
- **Initial State (Unclaimed / Unset)**:
  - Repository Default chip displayed `Nobody has said`.
  - Resolved status displayed `Runs this repository's work`.
  - Stored state confirmed via `GET /api/repos/bwsalmon/grain/work-ownership` returning `{"repo":"bwsalmon/grain","default":"","defaultRuns":true}`.
- **Transition to Read-Only (`Off`)**:
  - Clicked button `the repository default: off`.
  - UI updated dynamically: chip transitioned to `Off, set here` with error styling and resolved chip updated to `Does not run this repository's work`.
  - API call `PUT /api/repos/bwsalmon/grain/work-ownership` with payload `{"default":"off"}` succeeded with HTTP 200.
  - Verified backend returned `default: "off"`, `defaultRuns: false`.
- **Transition to Managed (`On`)**:
  - Clicked button `the repository default: on`.
  - UI updated: chip transitioned to `On, set here` (success style) and resolved chip updated to `Runs this repository's work`.
  - API call `PUT /api/repos/bwsalmon/grain/work-ownership` with payload `{"default":"on"}` returned HTTP 200 with `default: "on"`, `defaultRuns: true`.
- **Reversion to Unset (`Say nothing`)**:
  - Clicked button `the repository default: say nothing`.
  - Chip returned to `Nobody has said` with neutral outline; resolution returned to `Runs this repository's work`.
- **Granular Item Overrides**:
  - Tested item-level override buttons on the `release` item:
    - Setting `release: off` produced `Off, set here` and `Not run`.
    - Setting `release: on` produced `On, set here` and `Runs`.
    - Setting `release: say nothing` reverted the override to follow the repository default.

### 2. Read-Only Repository & Conflicting Target Validation

- Evaluated task creation restrictions when specifying read-only repositories:
  - Attempting to submit a task with conflicting primary target and read-only specification (`POST /api/tasks` with `repo: "bwsalmon/grain", reads: ["bwsalmon/grain"]`) was rejected with HTTP 400 Bad Request and validation error:
    `"bwsalmon/grain is this task's own repo, which its run already clones and pushes to -- it cannot also be a repo the run may only read"`.
  - Verified that read-only repositories prevent unwanted write access while allowing bounded read-only clone operations for auxiliary repositories.

### 3. CI Capability Security Boundaries (`SupportsCIEvent`)

- Evaluated capability resolution across different CI trigger events:
  - **Untrusted PR (`pull_request`) & Push (`push`) Events**:
    - Simulated CI jobs requesting `package-publish` and `repo-write:acme/widgets`.
    - `cmd/grain/ci.go` evaluated `SupportsCIEvent("pull_request")` and `SupportsCIEvent("push")` against the capability providers.
    - Both capabilities returned `false` for untrusted events and were stripped from `cc.Task.Grants`.
    - The resulting job ran in a fully isolated, sandboxed read-only execution context with zero write grants.
  - **Trusted Release (`release`) & Manual (`workflow_dispatch` / `EventManual`) Events**:
    - Evaluated identical capability requests under `ci.EventRelease` and `ci.EventManual`.
    - Providers returned `true` for `SupportsCIEvent`, granting `package-publish` and `repo-write:acme/widgets`.
    - Credentials and write access tokens materialized safely for authorized deployment and release jobs.

### 4. Team-Scoped Config Repository Permissions

- Evaluated task submission restrictions for team configuration repositories (`grain/config-<team>`):
  - **Agent Cross-Team Submission Prevention**:
    - Agent task on team `alpha` attempted to propose a config update task targeting `grain/config-beta`.
    - Submission failed validation with: `only agents from team "beta" can submit to its config repo grain/config-beta`.
    - Agent task on team `alpha` proposing to `grain/config-alpha` succeeded.
  - **Human Cross-Team Submission Prevention**:
    - User `alice` (member of team `alpha`) attempted to submit a task targeting `grain/config-beta`.
    - Submission was rejected with: `user "alice" does not have permission to submit tasks for team "beta"`.
    - User `bob` (member of team `beta`) submitting to `grain/config-beta` succeeded.

## Verification Matrix

| Test Suite / Spec | Component / Subsystem | Result |
| :--- | :--- | :--- |
| `ui/e2e/repo-ownership-ci-security.spec.js` | Full browser E2E walkthrough of ownership states, item overrides, API validation, and team permissions | **PASS** (1/1) |
| `ui/e2e/repo-ci-capabilities.spec.js` | CI capability dropdown selection, persistence, and repo settings | **PASS** (1/1) |
| `ui/src/components/RepoWorkOwnership.test.jsx` | React component rendering and button interaction for `RepoWorkOwnership` | **PASS** (6/6) |
| `cmd/grain/ci_capabilities_test.go` | CI capability event gating (`TestPackagePublishAndRepoWriteStripWritePermissionsOnUntrustedEvents`) | **PASS** |
| `pkg/ui/repo_work_ownership_test.go` | REST API work ownership handlers and multi-state persistence | **PASS** |
| `pkg/ui/client_test.go` | Team config repo agent & human submission restrictions (`TestTeamConfigRepoAgentSubmissionRestrictions`, `TestTeamConfigRepoHumanSubmissionRestrictions`) | **PASS** |
| `go test ./pkg/capability/... ./pkg/ui/...` | Core capabilities, UI server handlers, models, and stores | **PASS** |
| `ui` npm test suite | All UI component unit and integration tests | **PASS** (2,434/2,434) |

## Stubs and Diagnostics

- **Execution Mode**: Tested with real binaries (`bin/grain demo`) and automated Playwright browser instances interacting with the live DOM and network.
- **Diagnostics**: Clean execution with zero unhandled JavaScript errors, zero React component render warnings, and zero server panics or 500 responses.
