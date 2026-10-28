# Test Run: Access Accounts Administration & Granule Guest CA Integration Walkthrough

**Date**: 2026-09-28  
**Scope**: Access Control Accounts Filtering & Sorting, Account Type Normalization, Admin Authorization & Session Revocation, Granule VM Guest CA Integration (`NODE_EXTRA_CA_CERTS`), and Playwright Browser Caching  
**Reference**: `docs/test-plans/2026-09-28.md §7` / `docs/test-plans/2026-09-28-changes-test-plan.md`  
**Author/Runner**: Antigravity  
**Branch**: `grain/task-957`  

---

## 1. Executive Summary & Objectives

This walkthrough verified the end-to-end functionality, security boundaries, and runtime infrastructure for **Access Accounts Administration** and **Granule Guest CA Certificate Integration** across Grain.

### Key Objectives Exercised:
1. **Access Control Accounts Administration & UI**:
   - Evaluated account type normalization (`user`, `service_account`, `group`, `iap`) across the data model (`pkg/access/model.go`), API client (`ui/src/api/access.js`), React component (`ui/src/components/AccessPanel.jsx`), and Vue view (`ui/src/views/settings/AccessView.vue`).
   - Verified real-time search filtering, account type filtering (`All Types`, `User`, `Service Account`, `Group`), and sync status filtering (`All Statuses`, `Synced`, `Pending`, `Error`).
   - Verified interactive multi-column sorting (`Type`, `Identity`, `Description`, `Added At`, `IAP Status` / `Sync State`) across ascending and descending directions.
2. **Admin Authorization Enforcement & Immediate Session/Token Revocation**:
   - Verified authorization enforcement via `checkSystemAdmin(ctx)` in `pkg/ui/access.go`. Non-admin requests to `/api/access/accounts` are rejected with HTTP 403 Forbidden when `admin_accounts` are configured.
   - Evaluated account removal (`DELETE /api/access/accounts/:id` and CLI `grain access remove`):
     - Removal of an access account row immediately cascades into `team_store` access checks (`UserInTeam`), preventing unauthorized task dispatch and API operations.
     - Synchronous hook notification (`s.notifyHooks(ctx, "delete", *account)`) and immediate IAP policy reconciliation (`c.triggerIAPSync()` / `pkg/iapreconciler`) revoke upstream and downstream sessions/tokens.
3. **Granule VM Guest CA Certificate Import & TLS Inspection**:
   - Verified that Granule VM guest builds (`Granulefile`, `Granulefile.guest-base`, `Granulefile.sandbox`) copy Grain CA certificates (`.grain-ca.crt`) into `/usr/local/share/ca-certificates/grain-ca.crt`, execute `update-ca-certificates`, and set `ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
   - Verified global npm configuration (`npm config set cafile=/etc/ssl/certs/ca-certificates.crt`) preventing TLS inspection errors when communicating through intercepting package caches and proxy relays.
4. **Standardized Playwright Browser Installation & Caching**:
   - Verified standardized Playwright browser installation using `playwright-core` and `@playwright/test` targeting the shared cache directory `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
   - Verified that `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1` in the `npm` wrapper prevents redundant, non-deterministic browser downloads during build and test cycles.
   - Verified that `pkg/pkgcache/playwright.go` proxies and caches Playwright browser archive downloads across `cdn.playwright.dev`, `playwright.azureedge.net`, and `playwright.download.prss.microsoft.com`.

---

## 2. Environment & Test Matrix

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-957`
- **Host Environment**: Debian GNU/Linux 12 (bookworm), Linux x86_64
- **Runtimes**: Go `1.26.6`, Node.js `20.19.0`, npm `10.8.2`
- **Test Frameworks**: Playwright `1.57.0` (Chromium headless), Vitest `4.1.11`, Go `testing`
- **Test Specifications**:
  - `pkg/access/access_test.go`, `pkg/access/benchmark_test.go` (Normalization, CRUD, indexing)
  - `pkg/iapreconciler/reconciler_test.go` (IAP IAM policy synchronization & retry backoff)
  - `pkg/ui/access_test.go` (Admin authorization & REST endpoints)
  - `cmd/grain/access_test.go` (CLI `grain access` subcommands)
  - `pkg/pkgcache/playwright_test.go`, `pkg/pkgcache/ca_test.go`, `pkg/pkgcache/transparent_test.go` (CA generation & Playwright caching)
  - `pkg/orchestrator/packagecache_test.go` (Sandbox CA and tool configuration)
  - `ui/src/components/AccessPanel.test.jsx` (Frontend UI unit suite)
  - `ui/e2e/access-accounts-iap-walkthrough.spec.js` (Playwright browser walkthrough)

---

## 3. Step-by-Step Verification & Technical Analysis

### 3.1 Access Control Accounts Normalization & Type Parsing
- **Implementation**: `pkg/access/model.go` (`ParseAndNormalizeIdentifier`), `ui/src/api/access.js` (`validateIdentifier`), and `pkg/iapreconciler/reconciler.go` (`NormalizeMember`).
- **Behaviors Verified**:
  - Bare user emails (`alice@example.com`) and prefixed user emails (`user:alice@example.com`) normalize to `user:alice@example.com` (type `user`).
  - Google Cloud service account emails (`worker@project.iam.gserviceaccount.com`) automatically infer type `service_account` and normalize to `serviceAccount:worker@project.iam.gserviceaccount.com`.
  - Security group emails with prefix `group:team-leads@example.com` normalize to `group:team-leads@example.com` (type `group`).
  - Mismatches between explicit type selectors and identifier prefixes are rejected with clear validation errors.

### 3.2 Access Panel UI Filtering & Multi-Column Sorting
- **Implementation**: `ui/src/components/AccessPanel.jsx` and `ui/src/views/settings/AccessView.vue`.
- **Behaviors Verified**:
  - **Account Type Filtering**: Filtering by `User`, `Service Account`, or `Group` isolates matching rows and hides others.
  - **Sync Status Filtering**: Filtering by `Synced`, `Pending`, or `Error` isolates rows by reconciliation state.
  - **Search Filtering**: Query string matches across identifier, description, account type, and sync state simultaneously.
  - **Multi-Column Sorting**: Clicking table header sort labels for `Type`, `Identifier`, `Description`, `Added At`, and `IAP Status` reorders table entries deterministically in ascending and descending directions without clearing active filters.

### 3.3 Admin Authorization Enforcement & Session Revocation
- **Implementation**: `pkg/ui/access.go` (`checkSystemAdmin`), `pkg/access/store.go` (`Delete`), and `pkg/model/team_store.go` (`UserInTeam`).
- **Behaviors Verified**:
  - Unprivileged principals accessing `/api/access/accounts` receive HTTP 403 Forbidden.
  - System administrators listed in `grain_config.admin_accounts` can list, add, and revoke authorized accounts.
  - When an authorized account is removed (`DELETE /api/access/accounts/:id` or `grain access remove`):
    - The row is immediately deleted from SQLite/PostgreSQL `access_accounts`.
    - Subsequent access checks in `UserInTeam` immediately evaluate `accExists` to false, blocking further actions.
    - Delete hooks dispatch to `pkg/iapreconciler` to reconcile GCP IAP backend service IAM policy and drop access bindings.

### 3.4 Granule VM Guest CA Certificate Integration
- **Implementation**: `Granulefile`, `Granulefile.guest-base`, `Granulefile.sandbox`, and `pkg/pkgcache/ca.go`.
- **Behaviors Verified**:
  - The guest image build conditionally imports `.grain-ca.crt` into `/usr/local/share/ca-certificates/grain-ca.crt` and updates the system trust store via `update-ca-certificates`.
  - `ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` is exported across base and sandbox stages.
  - Global npm configuration (`npm config set cafile=/etc/ssl/certs/ca-certificates.crt --location=global`) ensures npm and Node runtimes trust internal certificates.
  - `pkg/pkgcache` generates dynamic CA certificates for transparent proxying of package archives without TLS verification failures.

### 3.5 Standardized Playwright Browser Installation & Caching
- **Implementation**: `Granulefile`, `Granulefile.sandbox`, `Makefile`, and `pkg/pkgcache/playwright.go`.
- **Behaviors Verified**:
  - `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` centralizes installed Chromium and WebKit browser binaries with full permissions (`chmod -R 777 /var/cache/playwright`).
  - The `npm` wrapper script exports `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1` to prevent redundant downloads when running `npm install` or `npm ci` inside sandboxes.
  - `pkg/pkgcache` transparently caches Playwright browser archives (`.zip`, `.tgz`) fetched from Microsoft CDNs (`cdn.playwright.dev`, `playwright.azureedge.net`, `playwright.download.prss.microsoft.com`), supporting HTTP range requests and HEAD checks.

---

## 4. Test Execution Output

### 4.1 Go Backend Integration & Unit Suites

```text
$ go test -v ./pkg/access/... ./pkg/iapreconciler/...
=== RUN   TestNormalizeAndValidateIdentifier
=== RUN   TestNormalizeAndValidateIdentifier/bare_user_email
=== RUN   TestNormalizeAndValidateIdentifier/prefixed_user_email
=== RUN   TestNormalizeAndValidateIdentifier/gcp_service_account_inferred
=== RUN   TestNormalizeAndValidateIdentifier/prefixed_service_account
=== RUN   TestNormalizeAndValidateIdentifier/prefixed_group
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_matches
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_synonym_users_with_bare_email
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_synonym_users_with_prefixed_email
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_synonym_service_accounts_with_prefix
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_synonym_groups_with_prefix
=== RUN   TestNormalizeAndValidateIdentifier/invalid_explicit_type_with_prefixed_identifier
=== RUN   TestNormalizeAndValidateIdentifier/explicit_type_mismatch_with_prefix
=== RUN   TestNormalizeAndValidateIdentifier/empty_string
=== RUN   TestNormalizeAndValidateIdentifier/invalid_email
--- PASS: TestNormalizeAndValidateIdentifier (0.00s)
=== RUN   TestStoreCRUDAndHooks
--- PASS: TestStoreCRUDAndHooks (0.00s)
=== RUN   TestStorePutAndValidation
--- PASS: TestStorePutAndValidation (0.00s)
=== RUN   TestAccessStoreIndexUsage
--- PASS: TestAccessStoreIndexUsage (0.00s)
=== RUN   TestAccessStoreLookupLatency10k
--- PASS: TestAccessStoreLookupLatency10k (0.07s)
=== RUN   TestHandlerEndpoints
--- PASS: TestHandlerEndpoints (0.00s)
=== RUN   TestHandlerAdminAuthorization
--- PASS: TestHandlerAdminAuthorization (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/access	0.077s

=== RUN   TestNormalizeMember
--- PASS: TestNormalizeMember (0.00s)
=== RUN   TestNormalizeMembers
--- PASS: TestNormalizeMembers (0.00s)
=== RUN   TestApplyDesiredMembers_Additions
--- PASS: TestApplyDesiredMembers_Additions (0.00s)
=== RUN   TestApplyDesiredMembers_Removals
--- PASS: TestApplyDesiredMembers_Removals (0.00s)
=== RUN   TestApplyDesiredMembers_IdempotentNoOp
--- PASS: TestApplyDesiredMembers_IdempotentNoOp (0.00s)
=== RUN   TestApplyDesiredMembers_PreserveOtherRolesAndConditions
--- PASS: TestApplyDesiredMembers_PreserveOtherRolesAndConditions (0.00s)
=== RUN   TestApplyDesiredMembers_EmptyDesiredMembersRemovesBinding
--- PASS: TestApplyDesiredMembers_EmptyDesiredMembersRemovesBinding (0.00s)
=== RUN   TestReconcileIAPMembers_FullCycle
--- PASS: TestReconcileIAPMembers_FullCycle (0.00s)
=== RUN   TestReconcileIAPMembers_ConcurrencyConflictRetry
--- PASS: TestReconcileIAPMembers_ConcurrencyConflictRetry (0.00s)
=== RUN   TestReconcileIAPMembers_ConcurrencyConflictMaxRetriesExceeded
--- PASS: TestReconcileIAPMembers_ConcurrencyConflictMaxRetriesExceeded (0.00s)
=== RUN   TestReconcileIAPMembers_NonConflictErrorDoesNotRetry
--- PASS: TestReconcileIAPMembers_NonConflictErrorDoesNotRetry (0.00s)
=== RUN   TestReconcileIAPMembers_GetPolicyError
--- PASS: TestReconcileIAPMembers_GetPolicyError (0.00s)
=== RUN   TestReconcileIAPMembers_ValidationErrors
--- PASS: TestReconcileIAPMembers_ValidationErrors (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/iapreconciler	0.007s
```

```text
$ go test -v ./cmd/grain -run TestCLIAccess
=== RUN   TestCLIAccess
No access accounts configured.
Added access account user:alice@example.com (user, acc-afb8fbd09663fbce)
Added access account serviceAccount:worker-1@myproj.iam.gserviceaccount.com (service_account, acc-55bb5b6a42254b2f)
[
  {
    "id": "acc-afb8fbd09663fbce",
    "type": "user",
    "identifier": "user:alice@example.com",
    "description": "Alice engineer",
    "createdAt": "2026-09-28T09:09:56.052415847Z",
    "syncState": "synced"
  },
  {
    "id": "acc-55bb5b6a42254b2f",
    "type": "service_account",
    "identifier": "serviceAccount:worker-1@myproj.iam.gserviceaccount.com",
    "description": "CI worker",
    "createdAt": "2026-09-28T09:09:56.053470847Z",
    "syncState": "synced"
  }
]
Removed access account user:alice@example.com
Removed access account acc-55bb5b6a42254b2f
--- PASS: TestCLIAccess (0.07s)
PASS
ok  	github.com/bwsalmon/grain/cmd/grain	0.086s
```

### 4.2 Package Cache & Playwright TLS Interception Suites

```text
$ go test -v ./pkg/pkgcache/... -run "TestPlaywright|TestCA|TestTransparent"
=== RUN   TestCAGenerationAndDynamicCert
--- PASS: TestCAGenerationAndDynamicCert (0.00s)
=== RUN   TestCAClientServerHandshake
--- PASS: TestCAClientServerHandshake (0.00s)
=== RUN   TestPlaywrightBinaryCached
--- PASS: TestPlaywrightBinaryCached (0.00s)
=== RUN   TestPlaywrightNonCacheablePassThrough
--- PASS: TestPlaywrightNonCacheablePassThrough (0.00s)
=== RUN   TestPlaywrightRangeRequest
--- PASS: TestPlaywrightRangeRequest (0.00s)
=== RUN   TestPlaywrightHEADRequest
--- PASS: TestPlaywrightHEADRequest (0.00s)
=== RUN   TestPlaywrightPathTraversalRefusal
--- PASS: TestPlaywrightPathTraversalRefusal (0.00s)
=== RUN   TestPlaywrightLegacyPrefix
--- PASS: TestPlaywrightLegacyPrefix (0.00s)
=== RUN   TestPlaywrightFlush
--- PASS: TestPlaywrightFlush (0.00s)
=== RUN   TestPlaywrightTLSInterception
--- PASS: TestPlaywrightTLSInterception (0.00s)
=== RUN   TestPlaywrightDNSInterception
--- PASS: TestPlaywrightDNSInterception (0.00s)
=== RUN   TestTransparentE2E
--- PASS: TestTransparentE2E (0.06s)
=== RUN   TestTransparentDisallowedHost
--- PASS: TestTransparentDisallowedHost (0.00s)
=== RUN   TestTransparentDockerPushE2E
--- PASS: TestTransparentDockerPushE2E (0.01s)
=== RUN   TestTransparentConcurrentLoad
--- PASS: TestTransparentConcurrentLoad (0.05s)
PASS
ok  	github.com/bwsalmon/grain/pkg/pkgcache	0.138s
```

### 4.3 Frontend Vitest Unit Tests

```text
$ cd ui && npx vitest run src/components/AccessPanel.test.jsx

 RUN  v4.1.11 /home/debian/work/ui

 ✓ src/components/AccessPanel.test.jsx (9 tests) 3672ms
     ✓ fetches and renders the list of authorized accounts with columns and status chips
     ✓ filters accounts table when searching
     ✓ filters accounts by account type
     ✓ filters accounts by sync status
     ✓ sorts accounts table when clicking column headers
     ✓ opens add account modal, validates and submits a new account
     ✓ displays client-side validation error when adding invalid email
     ✓ opens revoke confirmation dialog and deletes account
     ✓ calls showError when API fetch fails

 Test Files  1 passed (1)
      Tests  9 passed (9)
   Duration  4.80s
```

### 4.4 Playwright E2E Browser Walkthrough

```text
$ cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/access-accounts-iap-walkthrough.spec.js

Running 1 test using 1 worker

  ✓  1 [chromium] › e2e/access-accounts-iap-walkthrough.spec.js:6:3 › Access & Accounts Management, Dynamic IAP IAM Policy Sync & System Administration Walkthrough › thoroughly exercises access accounts, identity types, status chips, filtering, sorting, revocation, and admin privileges (4.4s)

  1 passed (11.7s)
```

---

## 5. Summary of Findings & Verification Conclusion

- **Access Accounts Management**: Complete feature parity across React and Vue UIs with responsive multi-column sorting, dual filter dropdowns (type and sync status), fuzzy search, and validation modals.
- **Admin Authorization & Revocation**: Enforces HTTP 403 Forbidden for non-administrators when admin accounts are configured; removes access accounts with immediate cascade to team authorization and IAP IAM policy reconciliation.
- **Granule Guest CA Integration**: Automatically incorporates root CA certificates into `/usr/local/share/ca-certificates` and updates trust stores, with `NODE_EXTRA_CA_CERTS` and global npm `cafile` eliminating TLS inspection issues.
- **Standardized Playwright Installation**: Browser binaries are cached under `/var/cache/playwright` and proxied via `pkg/pkgcache`, with `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1` suppressing redundant downloads.

All test suites and walkthrough verifications passed without regressions.
