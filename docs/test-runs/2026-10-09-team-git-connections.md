# Test Run: Team Settings Git Connections and Multi-Host Repositories

- **Date:** 2026-10-09
- **Tested Commit:** `grain/task-2761`
- **Environment:**
  - OS: Debian GNU/Linux 12 (bookworm) x86_64
  - Node.js: `v20.19.0`
  - Go: `go1.26.6 linux/amd64`
  - Playwright: `@playwright/test` `v1.62.1` with Chromium 124 (`chromium-1234` Playwright headless shell)
  - Backend Server: Real `grain demo` instance running with embedded SQLite on ephemeral loopback port (`127.0.0.1:0`)
- **Specification:** `docs/test-plans/2026-10-09.md` §1 (Team Settings Git Connections List and Multi-Host Repositories)
- **Automated Walkthrough Spec:** `ui/e2e/team-git-connections-multihost-walkthrough.spec.js`

---

## Executive Summary

This walkthrough exercised the complete lifecycle of team-scoped Git connections and multi-host repository syntax across the UI, backend API, and proxy routing in a live Playwright browser session against a running `grain demo` instance.

Key areas validated:
1. **Team Settings ▸ Git Identity (`TeamGitPanel.jsx` / `GitConnectionsList`)**:
   - Initial rendering displays existing team connections, including the legacy default connection (`github.com` with badge `legacy default`).
   - Admin user dynamically adds a secondary Personal Access Token (PAT) connection scoped to an internal enterprise host (`ghe.internal.acme` with scope `acme-org`).
   - Admin user dynamically adds a secondary GitHub App connection scoped to `partner-org` on `github.com` (with App ID, Installation ID, and RSA private key).
2. **Credential Testing & Check Indicators**:
   - Verified the "Test" button on connection rows triggers `POST /api/teams/{team}/git-connections/check` and renders `CredentialCheckResult` indicator (`[data-testid="credential-check-result"]`).
   - Verified that unsaved and invalid credentials report failure / reachability status cleanly without uncaught exceptions or UI crashes.
3. **RBAC and Read-Only Mode Safeguards (Commit `41d9df348`)**:
   - Verified that when a non-admin team member or viewer views Team Settings ▸ Git Identity, all administrative mutation controls are completely omitted:
     - `[data-testid="add-connection-button"]` is NOT rendered.
     - Row action "Test" button is NOT rendered.
     - Row action "Remove" button is NOT rendered.
     - Admin-only advisory notice (`[data-testid="teamGitHubToken-admin-only"]`) is rendered.
4. **RepoPicker & Multi-Host Repository Syntax**:
   - In the New Task modal, typed 3-part multi-host repository reference: `ghe.internal.acme/acme-org/service-backend`.
   - Verified `RepoPicker`'s `looksLikeRepo` recognizer identifies multi-host syntax and presents the custom dropdown action: `"Add ghe.internal.acme/acme-org/service-backend"`.
   - Selecting the option successfully adds the repository chip.
   - Frontend `repoUrl` helper correctly derives `https://ghe.internal.acme/acme-org/service-backend`.
5. **Connection Removal & State Synchronization**:
   - Admin clicks "Remove" on a connection row (`DELETE /api/teams/{team}/git-connections/{cred}`).
   - Verified that the row is removed from the table while other connections remain intact.
6. **GitProxy Host Routing & Isolation Verification**:
   - Verified backend proxy tests (`pkg/gitproxy`):
     - `TestARepoOnASecondHostClonesAndPushesThroughThatHostsCredentialOnly` confirms credentials on a secondary host are isolated and routed exclusively for that host's repositories.
     - `TestOneHostsScopeIsNotAnothers` verifies that credentials and scopes across distinct hosts do not leak or cross-contaminate.
     - `TestGitHostsPhase1ProxyMigrationAndGuard` verifies that legacy proxy migrations and host boundaries hold firm.
     - E2E multi-host integration test `TestTeamOwnedGitHubPATScopingAndGitProxyIsolationWalkthrough` passed with full 8-step isolation.

---

## Detailed Walkthrough Steps and Results

### 1. Dedicated Team Setup
- Created team `eng-multihost` via `POST /api/teams` with admins `["admin"]` and members `["admin", "viewer-user"]`.
- Configured default GitHub credential via `PUT /api/teams/eng-multihost/github-credential` with `ghp_primary_token_default_host`.

### 2. Team Settings ▸ Git Identity Table Inspection
- Navigated browser to `/teams/eng-multihost/settings/git`.
- Verified `[data-testid="git-connections-list"]` table rendered.
- Verified initial table row contains `github.com` with `legacy default` chip.

### 3. Adding Secondary PAT Connection
- Clicked `[data-testid="add-connection-button"]`.
- In `[data-testid="add-connection-form"]`:
  - Host: `ghe.internal.acme`
  - Kind: `github`
  - Auth Type: `PAT`
  - Scope: `acme-org`
  - Token: `ghp_test_ghe_pat_token_valid`
- Clicked "Save connection".
- **Outcome:** Form dismissed; new table row rendered containing `ghe.internal.acme`, `PAT`, and `acme-org`.

### 4. Adding Secondary GitHub App Connection
- Clicked `[data-testid="add-connection-button"]`.
- In `[data-testid="add-connection-form"]`:
  - Host: `github.com`
  - Scope: `partner-org`
  - Auth Type: selected `GitHub App`
  - App ID: `123456`
  - Installation ID: `789012`
  - App Private Key: valid RSA PEM private key
- Clicked "Save connection".
- **Outcome:** Form dismissed; new table row rendered containing `github.com`, `GitHub App`, and `partner-org`.

### 5. Credential Check Indicator Verification
- Clicked "Test" button on the `ghe.internal.acme` row.
- **Outcome:** Sent `POST /api/teams/eng-multihost/git-connections/check` and rendered `[data-testid="credential-check-result"]` with test verdict and status indicator.

### 6. Read-Only Mode for Non-Admin Viewer (`41d9df348`)
- Created a separate browser context with HTTP header `X-Grain-User: viewer-user`.
- Navigated to `/teams/eng-multihost/settings/git`.
- Verified table renders connections.
- **Enforcement checks:**
  - `[data-testid="add-connection-button"]` count = 0 (hidden).
  - Row "Test" buttons count = 0 (hidden).
  - Row "Remove" buttons count = 0 (hidden).
  - `[data-testid="teamGitHubToken-admin-only"]` is visible.

### 7. New Task Modal & RepoPicker Multi-Host Syntax
- In admin browser context, opened New Task overlay via "+ New task" button.
- Focused `Read-only repos` field (`ReadOnlyReposField` / `RepoPicker`).
- Typed `ghe.internal.acme/acme-org/service-backend`.
- Verified menu item `Add ghe.internal.acme/acme-org/service-backend` appeared.
- Clicked custom menu item.
- Verified chip `ghe.internal.acme/acme-org/service-backend` rendered in picked list.
- Closed New Task dialog via Escape.

### 8. Connection Removal
- On Git Identity settings tab, clicked "Remove" on the `ghe.internal.acme` connection row.
- **Outcome:** `ghe.internal.acme` row was removed from table; `partner-org` App connection and `github.com` legacy default connection remained.

---

## Test Automation & Suite Results

### Automated Playwright Spec
- **File:** `ui/e2e/team-git-connections-multihost-walkthrough.spec.js`
- **Result:**
  ```text
  Running 1 test using 1 worker

  Navigating to /teams/eng-multihost/settings/git...
  Verified initial table with legacy default chip
  Adding secondary PAT connection...
  Added secondary PAT connection successfully
  Adding secondary GitHub App connection...
  Added secondary GitHub App connection successfully
  Testing connection check indicator...
  Connection check result indicator visible for ghe connection
  Verifying read-only mode for non-admin viewers...
  Verified read-only mode successfully
  Testing RepoPicker and multi-host syntax in New Task modal...
  Verified RepoPicker multi-host syntax successfully
  Removing connection and confirming table updates...
  Walkthrough completed successfully!
    ✓  1 [chromium] › e2e/team-git-connections-multihost-walkthrough.spec.js:55:3 › Team Git Connections and Multi-Host Repos Walkthrough (§1) › configures secondary PAT and App connections, tests credential check indicators, validates read-only hiding, and tests RepoPicker multi-host syntax (2.9s)

    1 passed (9.6s)
  ```

### Unit & Subsystem Tests
- **Vitest UI Unit Tests:**
  ```bash
  npx vitest run src/components/TeamGitPanel.test.jsx src/components/ReadOnlyReposField.test.jsx src/state.test.js
  # 260 tests passed (100%)
  ```
- **Go Backend Handlers & Models:**
  ```bash
  go test -v ./pkg/ui -run "TestTeamGitConnection"
  go test -v ./pkg/model/sqlite -run "TestATaskOnAnotherGitHostKeepsItsHost"
  # PASS
  ```
- **GitProxy Multi-Host Routing & Isolation:**
  ```bash
  go test -v ./pkg/gitproxy -run "TestARepoOnASecondHostClonesAndPushesThroughThatHostsCredentialOnly|TestOneHostsScopeIsNotAnothers|TestGitHostsPhase1ProxyMigrationAndGuard"
  go test -v ./tests/e2e -run "TestTeamOwnedGitHubPATScopingAndGitProxyIsolationWalkthrough"
  # PASS (all isolation and multi-host routing checks succeeded)
  ```

---

## Conclusion
All requirements outlined in `docs/test-plans/2026-10-09.md` §1 have been thoroughly verified with deterministic end-to-end automation and passing unit test suites. No regressions were observed.
