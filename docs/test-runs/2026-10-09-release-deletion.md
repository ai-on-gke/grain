# 2026-10-09 Release & Candidate Deletion Lifecycle Walkthrough

**Date**: 2026-10-09  
**Task**: grain/task-2762 (Origin: task 2757, Filed-by: task 2757)  
**Test Plan**: `docs/test-plans/2026-10-09.md` §2 ("Release & Candidate Deletion Lifecycle")  
**Target Commit**: `ed99d7879` (`feat(ui): allow removing a release and its candidates`, backported from `main` `0a6562180`) on branch `grain/task-2762` (tracking `candidate`)  
**Environment**: Debian GNU/Linux 12 (bookworm) x86_64 sandbox VM, Go 1.26.6, Node 20.19.0, Vite 6, Playwright 1.62.1 with Chromium / Chrome Headless Shell build 1234.  
**Server Under Test**: `grain demo` instance running with embedded SQLite on ephemeral isolated scratch directory, serving production frontend bundle compiled into `pkg/ui/static`. Unit tests also exercised PostgreSQL 15 (`localhost:5432`).  
**Browser Engine**: Headless Chromium at 1440×900 desktop viewport, with full console and network inspection.

---

## Executive Summary

- **Verdict**: **PASSED (Clean)**.
- The browser-based walkthrough and store lifecycle verification specified in `docs/test-plans/2026-10-09.md` §2 have been executed and verified end-to-end.
- Deleting an unprovisionable or abandoned release (`v2.5.0-abandoned`) removes the release record and its candidate (`rc.1`) cleanly from both SQLite and PostgreSQL stores, clearing pending reconciler rows from `PendingReleases` and `PendingCandidates`.
- The native UI confirm dialog prompt (`"Remove release v2.5.0-abandoned? This drops the release and its candidates from grain."`) safeguards deletion:
  - Dismissing/cancelling the dialog retains all release and candidate rows on screen without mutation.
  - Accepting the dialog sends `DELETE /api/repos/acme/widgets/releases/v2.5.0-abandoned`, receives HTTP 204 NoContent, and dynamically removes the release and candidate cards from the UI without requiring a full page refresh.
- Re-creation of a release with the identical name immediately succeeds (HTTP 201 Created), confirming release name reuse is unblocked.
- Authorization enforcement is strictly maintained: non-admin users cannot delete releases (the "Delete release" button is disabled in the UI, and direct DELETE requests return HTTP 403 Forbidden with prompt to enable Admin mode).

---

## Step-by-Step Walkthrough Execution & Results

### 1. Repository Release Deletion Controls (`acme/widgets`)
- **Action**: Navigated to Repositories ▸ `acme/widgets` ▸ Releases (`/repos/acme/widgets/releases`).
- **Setup**: Form submitted with `New release name` = `v2.5.0-abandoned`.
- **Result**:
  - Release created (`v2.5.0-abandoned.latest`, `v2.5.0-abandoned.rc.1`).
  - Active candidate card rendered with status `cutting` / `active`.
  - Delete control rendered: Button labeled `"Delete release"` (`color="error"`).

### 2. Confirm Dialog Cancellation
- **Action**: Clicked "Delete release" button.
- **Verification**: Native browser dialog event trapped. Dialog text asserted:
  `"Remove release v2.5.0-abandoned? This drops the release and its candidates from grain."`
- **Result**: Dialog dismissed (`dialog.dismiss()`).
- **Observation**:
  - No HTTP DELETE request sent.
  - Release dropdown option `v2.5.0-abandoned` remained selected.
  - Release branches hint (`v2.5.0-abandoned.latest`) and candidate row (`v2.5.0-abandoned.rc.1`) remained visible and interactable.

### 3. Confirmed Release & Candidate Deletion
- **Action**: Clicked "Delete release" button again and accepted the dialog (`dialog.accept()`).
- **Verification**:
  - Network request monitored: `DELETE /api/repos/acme/widgets/releases/v2.5.0-abandoned`.
  - Response status: HTTP 204 NoContent.
  - DOM updated dynamically via internal React refresh hook without full browser reload.
  - Release row and candidate row disappeared from page (`not.toBeVisible()`).
  - Direct API check: `GET /api/repos/acme/widgets/releases/v2.5.0-abandoned` returned HTTP 404 NotFound.

### 4. Release Name Reuse
- **Action**: Immediately filled `v2.5.0-abandoned` into the `New release name` text input and submitted `Create release`.
- **Verification**:
  - Network request: `POST /api/repos/acme/widgets/releases` with payload `{"name":"v2.5.0-abandoned"}`.
  - Response status: HTTP 201 Created.
  - UI updated: Release `v2.5.0-abandoned.latest` and newly cut candidate `v2.5.0-abandoned.rc.1` rendered on screen immediately.

### 5. Store Cascaded Cleanup (SQLite & PostgreSQL)
- **Tests Executed**:
  - `pkg/model/sqlite`: `TestDeleteReleaseRemovesReleaseAndCandidates` -> **PASS**
  - `pkg/model/postgres`: `TestDeleteReleaseRemovesReleaseAndCandidates` -> **PASS**
- **Invariants Checked**:
  1. `store.DeleteRelease(ctx, repo, name)` deletes rows from `release_candidate` where `release_id` matches.
  2. `store.DeleteRelease(ctx, repo, name)` deletes row from `release`.
  3. `store.PendingReleases(ctx)` returns no remaining entries for the deleted release.
  4. `store.PendingCandidates(ctx)` returns no remaining candidate entries for the deleted release.
  5. Subsequent `store.GetRelease(ctx, repo, name)` returns `nil, nil`.
  6. Subsequent `store.DeleteRelease(ctx, repo, name)` returns `ErrNoRelease`.

### 6. Authorization Enforcement
- **Tests Executed**:
  - `pkg/ui`: `TestReleaseWritesNeedARepoAdmin/delete/*` across all personas (`zed`, `viewer`, `proposer`, `member` -> 403 Forbidden; `lead`, `owner`, `root` -> 204 NoContent).
  - Playwright browser test with non-admin context (`X-Grain-User: alice` with Admin mode off):
    - "Delete release" button is rendered with `disabled` attribute (`manageRefused` condition).
    - Direct `DELETE /api/repos/acme/widgets/releases/v2.5.0-abandoned` returned HTTP 403 Forbidden with hint `"turn on Admin mode"`.

---

## Automated Test Coverage Added

Added permanent Playwright automated end-to-end spec in `ui/e2e/release_deletion_lifecycle.spec.js` covering:
- UI creation of release with initial candidate cut (`v2.5.0-abandoned.rc.1`).
- Confirmation prompt verification and dismissal safeguard.
- Confirmed deletion asserting HTTP 204 and dynamic element removal without page reload.
- Direct API 404 verification.
- Release name re-creation asserting HTTP 201 and UI re-population.
- Non-admin authorization enforcement (disabled button in UI, HTTP 403 on API).

Both e2e specs passed:
- `ui/e2e/release_delete_walkthrough.spec.js` (passed in 948ms)
- `ui/e2e/release_deletion_lifecycle.spec.js` (passed in 1.9s)
