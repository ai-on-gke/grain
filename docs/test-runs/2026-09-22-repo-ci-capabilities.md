# RepoPage CI-Only Capability Display, Selection, and Persistence Walkthrough

## Test subject and environment

- Commit: `grain/task-320`
- Environment: Debian bookworm, Go 1.26.6, Node 20.19.0, npm 10.8.2.
- Browser: Playwright 1.57.0 with Chromium 151.0.7922.34 (Chrome for Testing) at 1440×1100.
- Built with `make frontend grain grit`, served by live `bin/grain demo -addr 127.0.0.1:<port> -open=false` backed by the embedded SQLite store (`sqlite.Open`/`model.New`) and `pkg/ui.Server`.
- Automated Playwright spec: `ui/e2e/repo-ci-capabilities.spec.js` executing all walkthrough steps against the real DOM, API handlers, and backend store.

## Walkthrough and outcome

All actions were executed through real UI controls against a live `grain demo` instance, verified through DOM states, network requests, state stores, and deep-link direct page reloads.

### 1. Direct URL navigation & route resolution
- Navigated directly to `/repo/bwsalmon/grain`.
- Verified that both `/repo/:owner/:name` and `/repos/:owner/:name` routes correctly resolve to the repository configuration view (`RepoPage.jsx`).
- Verified page header rendered repository heading `bwsalmon/grain` with the repository glyph and GitHub links.

### 2. CI capabilities dropdown inspection
- Located and expanded the "CI capabilities" selection dropdown (`#repo-ci-capabilities-label`).
- Verified that unselected CI-only capabilities (`package-publish` and `registry-push`) are visible in the dropdown alongside task capabilities (such as `gcp-key`).
- Verified human-readable titles ("Package publish", "Registry push", "GCP key") and readiness/descriptions are accurately rendered for each option.
- Verified that task-only capabilities that do not support CI shells (e.g. `self-debug`) are properly excluded from the CI capabilities dropdown.

### 3. CI capability selection & persistence
- Selected `Package publish` (`package-publish`) from the dropdown.
- Closed the dropdown and confirmed the `Package publish` chip was rendered inside the select value area.
- Clicked "Save CI capabilities", triggering `PUT /api/repos/bwsalmon/grain/ci-capabilities` with payload `{"ciCapabilities":["package-publish"]}`.
- Verified successful HTTP 200 response, no error banner displayed, and state refreshed.
- Verified that task default capabilities remained completely independent and untouched.

### 4. Direct URL reload & state persistence
- Performed a hard page reload / direct URL navigation to `/repo/bwsalmon/grain`.
- Confirmed that the repository page loaded with `Package publish` chip active in the CI capabilities section.
- Verified via backend API `GET /api/repos/bwsalmon/grain/ci-capabilities` that `ciCapabilities` contains `["package-publish"]`.

### 5. Multiple CI capability selection
- Reopened the "CI capabilities" dropdown, selected `Registry push` (`registry-push`), and saved.
- Verified that both `Package publish` and `Registry push` chips were rendered and persisted across hard reload.
- Verified `GET /api/repos/bwsalmon/grain/ci-capabilities` returned `["package-publish", "registry-push"]`.

### 6. CI workflow integration & requirements resolution
- Verified that `GET /api/repos/bwsalmon/grain/capabilities` returned `RepoDefaults` with `ciCapabilities: ["package-publish", "registry-push"]` and populated `availableCICapabilities`.
- Verified that CI job requirement resolution (`pkg/ci/requirements.go` and `cmd/grain/ci.go`) evaluates repository grants against declared workflow job capabilities.

## Stubs and diagnostics

- **Execution boundary**: `grain demo` serves real HTTP API endpoints backed by a full SQLite database store. Playwright navigated real browser instances and issued actual network requests.
- **Diagnostics**:
  - Monitored browser console, network requests, and server stdout/stderr.
  - Expected 404 responses on `/api/repos/.../github-branches` due to sandbox demo environment not having active GitHub App API connections (handled cleanly by fallback text input).
  - Zero unhandled JavaScript exceptions, zero React render errors, and zero server 500 errors.

## Verification

- **Playwright E2E walkthrough**: `ui/e2e/repo-ci-capabilities.spec.js` passed cleanly.
- **Vitest component & path tests**:
  - `ui/src/components/RepoPage.test.jsx`: 55 passed.
  - `ui/src/paths.test.js`: 58 passed.
- **Go unit & API tests**:
  - `pkg/ui/repo_ci_capabilities_test.go`: passed (`TestRepoCICapabilitiesAPIKeepsTaskDefaultsIndependent`, `TestConfigIncludesCICapabilitiesCatalogWithCIOnlyCapabilities`).
  - `pkg/ui/ci_only_capabilities_test.go`: passed.
