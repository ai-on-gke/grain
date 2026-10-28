# Test Run: System Settings Restructuring & Scoped Non-Admin Access

**Date**: 2026-09-28  
**Scope**: System Settings Restructuring, Scoped Non-Admin Access, Tab Scoping, Flush Button Disabling & Backend REST API RBAC 403 Enforcement (`docs/test-plans/2026-09-28.md` §3)  
**Author/Runner**: Antigravity  

---

## 1. Overview & Objectives

This test run verified the restructuring of System Settings accessibility, UI role-based scoping, and backend RBAC mutation security across the Grain web application and REST API.

### Key Objectives Exercised:
1. **System Settings Navigation Accessibility**:
   - Ensured the footer navigation item ("Admin" / System Settings) in the left navigation rail is visible to all authenticated users (not hidden behind `isAdmin`).
   - Removed the blocking "Access Denied" screen from the overlay when accessed by non-administrators.
2. **Role-Based Tab Scoping**:
   - **Non-Administrators**: Scoped to view only `Sandboxes` and `Caches` tabs. Sensitive administrative tabs (`General & Deployment`, `Access & Accounts`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Executors`, `Integrations & Auth`, `All Teams`, `All Users`, `Config repo`, `Logs`, `Top`, `Pipeline`, `Power`, `Upgrade`) are excluded from the navigation bar.
   - Non-administrators opening System Settings default directly to the first authorized tab (`Sandboxes`).
   - **Administrators**: Unrestricted access to all 16 System Settings tabs.
3. **Cache Action Permissions**:
   - **Non-Administrators**: Destructive actions on the `Caches` tab (per-ecosystem and whole proxy cache flush buttons, Actions cache flush buttons) are disabled.
   - **Administrators**: Full cache flush management actions remain enabled and interactive.
4. **Backend REST API RBAC Enforcement**:
   - Enforced strict authorization (`checkSystemAdmin`) returning `HTTP 403 Forbidden` across all mutation and administrative REST endpoints for unprivileged users:
     - `PUT /api/settings` (Cluster settings update)
     - `POST /api/settings/plan` (Cluster settings dry-run plan)
     - `POST /api/package-cache/flush` (Proxy package cache eviction)
     - `POST /api/actions-cache/flush` (Actions cache eviction)
     - `POST /api/pipeline-pause` (Merge pipeline pause)
     - `DELETE /api/pipeline-pause` (Merge pipeline resume)
     - `POST /api/host/restart` (Host daemon restart)
     - `POST /api/host/reboot` (Host system reboot)
     - `POST /api/host/shutdown` (Host system shutdown)
     - `POST /api/upgrade` (Immediate or scheduled upgrade execution)
     - `DELETE /api/upgrade/scheduled` (Scheduled upgrade cancellation)
   - Verified that authorization checks take precedence before feature availability checks so non-admins consistently receive HTTP 403 rather than HTTP 501.
5. **Automated End-to-End Walkthrough**:
   - Executed Playwright browser walkthrough against a multi-user environment validating navigation, tab rendering, button states, and API responses.

---

## 2. Environment & Test Matrix

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-953`
- **Host Environment**: Debian GNU/Linux 12 (bookworm), Linux x86_64
- **Runtimes**: Go `1.26.6`, Node.js `20.19.0`, npm `10.8.2`
- **Test Frameworks**: Playwright `1.57.0` (Chromium headless), Vitest `4.1.11`, Go `testing`
- **Test Specifications**:
  - `ui/e2e/system-settings-scoped-access-walkthrough.spec.js` (Playwright E2E browser walkthrough)
  - `ui/src/components/Sidebar.test.jsx` (Navigation rail visibility unit suite)
  - `ui/src/components/AdminOverlay.test.jsx` (Tab scoping & non-admin rendering unit suite)
  - `ui/src/components/CachesPage.test.jsx` (Flush button disabled state unit suite)
  - `pkg/ui/settings_test.go` (`TestUpdateAndPlanSettingsNonAdminForbidden`)
  - `pkg/ui/packagecache_test.go` (`TestFlushPackageCacheNonAdminForbidden`)
  - `pkg/ui/actionscache_test.go` (`TestFlushActionsCacheNonAdminForbidden`)
  - `pkg/ui/access_test.go` (System admin authorization & token verification suite)

---

## 3. Step-by-Step Verification Results

### 1. Navigation Rail Visibility & Initial Access
- **Action**: Rendered the navigation sidebar for both admin user (`alice`) and non-admin member (`bob`).
- **Assertions**:
  - The "Admin" footer button is rendered and clickable in the rail for all authenticated users.
  - Clicking "Admin" opens the overlay without showing an "Access Denied" error message.
- **Verdict**: PASS

### 2. Administrator Full Tab Access
- **Action**: Logged in as system administrator `alice` and opened System Settings.
- **Assertions**:
  - All 16 administrative tabs are present in the tab bar: `General & Deployment`, `Access & Accounts`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Executors`, `Integrations & Auth`, `All Teams`, `All Users`, `Config repo`, `Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Power`, `Upgrade`.
  - Navigating to the `Caches` tab reveals active, interactive flush controls for Proxy and Actions caches.
- **Verdict**: PASS

### 3. Non-Administrator Scoped Tab Access & Button State
- **Action**: Logged in as standard member `bob` and opened System Settings.
- **Assertions**:
  - The overlay displays only `Sandboxes` and `Caches` tabs.
  - All 14 sensitive administrative tabs are hidden.
  - Navigating to the `Caches` tab shows that the "Flush the whole cache" and ecosystem-specific flush buttons are disabled (`disabled` attribute and styling).
- **Verdict**: PASS

### 4. Backend REST API RBAC Mutation Enforcement
- **Action**: Executed mutation requests against backend endpoints using non-admin credentials (`bob`).
- **Assertions**:
  - `PUT /api/settings` returned `HTTP 403 Forbidden`.
  - `POST /api/package-cache/flush` returned `HTTP 403 Forbidden`.
  - `POST /api/actions-cache/flush` returned `HTTP 403 Forbidden`.
  - `POST /api/pipeline-pause` returned `HTTP 403 Forbidden`.
  - `POST /api/host/restart` returned `HTTP 403 Forbidden`.
  - `POST /api/upgrade` returned `HTTP 403 Forbidden`.
- **Verdict**: PASS

---

## 4. Test Suite Execution Summary

| Suite / Test Target | Type | Total Tests | Passed | Failed | Duration |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `ui/e2e/system-settings-scoped-access-walkthrough.spec.js` | Playwright E2E | 1 | 1 | 0 | 1.8s |
| `ui/src/components/Sidebar.test.jsx` | Vitest Unit | 18 | 18 | 0 | 0.4s |
| `ui/src/components/AdminOverlay.test.jsx` | Vitest Unit | 88 | 88 | 0 | 2.5s |
| `ui/src/components/CachesPage.test.jsx` | Vitest Unit | 7 | 7 | 0 | 0.4s |
| `pkg/ui/...` | Go Unit / Integration | 148 | 148 | 0 | 51.1s |
| Full UI Suite (`npm test`) | Vitest | 2,624 | 2,624 | 0 | 175.8s |
| Full Go Suite (`go test ./...`) | Go | All packages | All | 0 | 254.2s |

---

## 5. Conclusion

All requirements of `docs/test-plans/2026-09-28.md` §3 have been verified and validated. System Settings is now accessible to all authenticated team members with appropriately scoped read access (`Sandboxes` and `Caches`), while preserving strict operational security through disabled UI actions and backend REST API HTTP 403 RBAC enforcement.
