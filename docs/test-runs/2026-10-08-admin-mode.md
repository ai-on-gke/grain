# Test Run Report: Admin mode for system admins

**Date**: 2026-10-08
**Branch / base**: `grain/task-2537`, cut from `candidate`.
**Commit tested**: `5487e358e` on `candidate`, ported from `main` `18b55304b`.

| Commit | What | `git branch -r --contains` |
|---|---|---|
| `5487e358e` | Admin mode: a system admin is a normal user until they switch it on | `origin/candidate`, `origin/grain-merge-queue/candidate` |

**Environment**:
- Debian sandbox, Linux x86_64, Go 1.26.6, Node 20.19.0.
- Playwright 1.62.1 with Chromium headless shell 1234, from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- Real `grain demo` over its embedded SQLite store (`-sensitive-hold` verified). Real production frontend built with Vite.

## Stubs

None of grain's own APIs or React were mocked.
- **External boundaries stubbed**: None. No model sessions dispatched; demo uses its built-in simulator for external GitHub interaction.
- Component-level unit tests (`App.adminMode.test.jsx`, `AdminOverlay.test.jsx`, etc.) mock `api.js` in jsdom/Vitest per their standard suite pattern; all E2E browser tests and integration tests drive real Go HTTP servers and SQLite databases without mocks.

## Personas

| User | Configured Role | Admin Mode State |
|---|---|---|
| `alice` | System admin (`adminAccounts: ["alice", "admin", ...]`) | Starts OFF (acts as normal user); toggled to ON |
| `bob` / `carol` | Normal users / team members (not in `adminAccounts`) | No Admin mode switch; acts as normal user |

---

## Outcomes

### 1. Route Sweep (`tests/security/routes_test.go`: `TestAdminModeRouteSweep`)

We swept all 66 system-admin routes defined in `routeAuthorization`:
- **Admin with Admin mode OFF**: Server answers `403 Forbidden` with the explicit hint `"you are a system admin; turn on Admin mode to do this"`.
- **Admin with Admin mode ON** (`X-Grain-Admin-Mode: on`): Server accepts the request past the system-admin authorization check (answering non-401/403 status codes, proceeding to endpoint handling).
- **Non-admin with Admin mode header ON** (`X-Grain-Admin-Mode: on`): Server answers `403 Forbidden` (or `401 Unauthorized`) without offering the hint.
- **Baseline routes**: Routes exempt in `.grain/baselines/route-authorization.txt` (which validate payloads, secrets, or internal parameters prior to auth gating) were accounted for and verified against their respective baseline validation behaviors.

All 66 routes passed cleanly in both `go test ./tests/security -run TestAdminModeRouteSweep` and `make security`.

### 2. Browser Walkthrough (`ui/e2e/admin-mode-walkthrough.spec.js`)

Tested against a live `grain demo` instance using Playwright:
- **Switch visibility and initial state**:
  - `alice` logs in: the "Admin mode" switch in the rail is visible and unchecked by default.
  - The sticky red banner `admin-mode-banner` and chip `admin-mode-chip` are not rendered.
- **Admin overlay tab gating**:
  - With Admin mode OFF, clicking the **Admin** rail button displays only user-accessible tabs:
    - Visible: `Sandboxes`, `Caches`.
    - Hidden (0 occurrences): `General & Deployment`, `Access & Accounts`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Executors`, `Integrations & Auth`, `All Teams`, `All Users`, `Config repo`, `Logs`, `Top`, `Pipeline`, `Power`, `Upgrade`.
- **Switch toggled ON**:
  - Toggling switch ON immediately renders `admin-mode-banner` ("Admin mode is on") and the red indicator chip `admin-mode-chip`.
  - Re-opening Admin overlay restores the admin tabs: `All Users`, `Access & Accounts`, `Power`, etc. are fully visible.
- **Team settings and admin controls**:
  - In `TeamSettingsOverlay.jsx` / `TeamGitPanel.jsx`: deployment admin privileges are gated by `config.isAdmin`. With Admin mode OFF, `config.isAdmin` is false, and token management rights for teams Alice is not in (`payments`) are hidden/restricted; restored when Admin mode is ON.

### 3. Direct `/api/` Browser Requests Audit

Audited every `href`, `src`, and `window.open` targeting `/api/` across `ui/src`:
1. `ui/src/components/UsernameDialog.jsx`: `<a href="/api/auth/google/login">` initiates Google OAuth flow; public endpoint.
2. `ui/src/components/AttachmentLinks.jsx`: `<a href="/api/tasks/{taskId}/attachments/{id}">` downloads task attachments; gated by task-member read checks (served with `X-Content-Type-Options: nosniff` and CSP download sandbox).
3. `ui/src/components/CIPage.jsx`: `<a href="/api/ci/runs/{run}/artifacts/{job}/{name}">` downloads CI artifacts; gated by task-member read checks.

**Finding**: No system-admin-only endpoint is targeted by direct browser links or elements that cannot carry HTTP headers.

### 4. Sensitive-Path Approval Rule (`pkg/ui/admin_mode_test.go`: `TestSensitivePathApprovalAdminMode`)

Tested a task on hold for `builtin:deploy-keys` requiring `model.ApproverSystemAdmins` (`"system-admins"`):
- **Admin mode OFF**: `POST /api/tasks/{id}/submit` as `alice` returns `403 Forbidden`:
  `"1 touches sensitive paths, and alice is not one of the people who may release it. It waits for: - \`builtin:deploy-keys\`: a system admin"`.
- **Admin mode ON** (`X-Grain-Admin-Mode: on`): `POST /api/tasks/{id}/submit` as `alice` returns `200 OK`. The task's `ApprovedBy` is recorded as `"alice"` and `AutoMerge` is enabled (`true`).

### 5. Persistence Edges (`ui/e2e/admin-mode-walkthrough.spec.js`)

- **New tab isolation**: Opening a new tab (`context.newPage()`) on the same origin while Alice has Admin mode ON starts with Admin mode OFF (switch unchecked, banner absent) because state is kept in `sessionStorage` (`grain.adminMode`).
- **User switching to non-admin**: Switching user to `carol` in the same tab clears `grain.adminMode`, hides the switch completely, and requests to `/api/config` omit `X-Grain-Admin-Mode`.
- **Sign-out / sign-in**: Signing out and logging back in as a non-admin keeps the switch hidden.
- **Session expiration**: Expiring session cookies and reloading leaves the page in an anonymous state without active admin mode.
- **Switching back to admin**: Switching back to `alice` starts with Admin mode OFF (switch visible, unchecked, banner absent).

### 6. Candidate Port Verification (`/api/auth/user`)

Verified against `grain demo`:
- **Mode OFF**:
  `{"authenticated":true,"user":"alice","passwordAuthEnabled":true,"googleOAuthEnabled":false,"isSystemAdmin":true,"adminMode":false,"authMethod":"header","hasPassword":false}`
- **Mode ON**:
  `{"authenticated":true,"user":"alice","passwordAuthEnabled":true,"googleOAuthEnabled":false,"isSystemAdmin":true,"adminMode":true,"authMethod":"header","hasPassword":false}`
- **Unaccepted identity header**:
  `{"authenticated":false,"passwordAuthEnabled":true,"googleOAuthEnabled":false,"identityNotAccepted":"This request's identity was not accepted because its X-Goog-IAP-JWT-Assertion did not verify against this deployment's IAP audience, issuer and keys (-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url).","isSystemAdmin":false,"adminMode":false,"hasPassword":false}`

---

## Test Verification Summary

- `make security` passed (100% green, 209s).
- `go test -v ./pkg/ui -run TestSensitivePathApprovalAdminMode` passed (0.06s).
- `npx playwright test e2e/admin-mode-walkthrough.spec.js` passed (3.9s).
- `npx playwright test e2e/admin-mode.spec.js` passed (2.0s).
- `git branch -r --contains 5487e358e` confirmed on `origin/candidate`.
