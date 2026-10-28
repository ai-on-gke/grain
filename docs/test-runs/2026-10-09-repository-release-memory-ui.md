# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-09
- **Task**: grain/task-2708
- **Status**: Passed. Thoroughly exercised repository, release, qualification, memory, and team configuration workflows in a real browser against a real `grain demo` server. All existing functionality and prior bug fixes are intact. No new regressions or defects found.

## Environment

- **Commit walked through**: `5123d46d7`, the tip of `candidate` (`5123d46d7705e7cfd61df454a4bf1d60df620640` · Fri Oct 9 02:45:53 2026 +0200).
- **Host**: Debian (grain sandbox VM), Linux amd64. Go 1.26.6, Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34 (Playwright 1.64.0, `chromium-headless-shell` 1234).
  - Driven by Playwright browser automation against the built frontend.
  - Viewports tested: 1400×950 desktop and 390×844 mobile viewports.
  - Inspected: browser console errors, failed network requests, dialog confirmations, and HTTP error responses.
- **Server**: real `grain demo` server with embedded SQLite store (`/tmp/grain-demo-*`).
  - Frontend compiled with `npm run build` in `ui/` into `pkg/ui/static`.
  - Backend compiled with `go build ./cmd/grain`.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used.
- **Setup**: Playwright Chromium installed with `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium --with-deps`. No screenshots or traces committed.

## External Boundaries and Stubs

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo environment runs without live GitHub connectivity. | `page.route` on this path only | `["main","candidate","release/4.0"]` |
| Release-run dispatch, `POST /api/repos/acme/widgets/release-runs` | The demo daemon runs `-ci=false`. | `page.route` on this path only | Real unstubbed 404/refusal; 502 `{"error":"GitHub: could not resolve refs/heads/main (502 Bad Gateway)"}`; slow 202 `{id:"run-20261009-1",status:"queued"}` |
| GitHub release reconciler | No external reconciler runs in the demo | The `ui/e2e/releasehelper` utility compiled and run against the demo's SQLite store | `mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-promoted`, `mark-release-merged`, `mark-candidate-error`, `sync-qualifications` |

All other boundaries were real: Grain's own API, the React UI, and actual browser user gestures.

## Steps and Outcomes

### 1. Repositories Index (`/repos`)
- **Navigation**:
  - Navigated to `/repos` via the sidebar rail button and via direct URL navigation.
  - Header count badge verified against the number of repository rows.
- **Add Repos & Name Validation**:
  - Added `acme/gadgets`, `zeta/alpha-tools`, and an exceptionally long repository name (`example-corporation/ultra-long-repository-service-name-for-responsive-testing-1009`).
  - Tested duplicates (`Acme/Gadgets`), URL normalization (`git@github.com:acme/sshform.git` -> `acme/sshform`), and rejected invalid names (`acme/`, `acme/wid gets`, `acme/.git`).
- **Responsive Layout**:
  - Verified no horizontal overflow (`scrollWidth <= clientWidth`) at both 1400px and 390px mobile viewport widths with the long repository name rendered.
- **Search & Sort**:
  - Tested case-insensitive filtering (`WIDGETS` matches `acme/widgets`).
  - Tested non-matching query (`NONEXISTENT_REPO_ZZZ`) showing "No repos match your search."
  - Tested sort modes: Name (A–Z), Most tasks, Active first, and Custom order.
  - Reorder handles are only visible in Custom order.
- **Reordering Affordances**:
  - Tested drag/drop reorder via pointer and keyboard (Enter on handle, Enter on drop slot).
  - Focus correctly returns to the moved item's handle.
  - Reload verified that custom ordering persists in localStorage.

### 2. Repository Detail & Navigation (`/repos/acme/widgets`)
- **Navigation & Task Link**:
  - Navigated into `/repos/acme/widgets`.
  - Followed the task count link ("12 tasks filed here") to the filtered task list (`/?f=repo:any:acme/widgets`).
  - Returned to the repo detail page via browser Back button.
- **Task Filing from Repo Page**:
  - Opened New task drawer from repo page. Verified `select[name=repo]` is preselected to `acme/widgets`.
  - Configured task with base branch `release/4.0` (using stubbed branch list).
  - Added multi-repo relationships: write repo `acme/gadgets` (with base `candidate`) and read-only repo chip.
  - Verified Escape with suggestion menu open closes the menu without dropping the task form.
  - Created task and verified repo preselection and parameters persisted.

### 3. Repository Configuration & Persistence
- **Branch Management**:
  - Added branch `task-396/operator-branch`. Verified presence in branch list.
  - Attempting to add an existing branch gave the expected notice without creating a duplicate.
  - Compared branches (`main` vs `task-396/operator-branch`) navigating to `/repos/acme/widgets/compare` and back.
- **Capabilities**:
  - Configured CI capabilities (`GCP key`, `Gemini key`) and Default capabilities (`Self debug`, `Memory`).
  - Reloaded page and verified chips persisted.
- **Prompt Extension & Setup Command**:
  - Edited Prompt extension and Setup command.
  - Reloaded and verified daemon readback of persisted values.
- **CI Workflows (`.grain/workflows`)**:
  - Tested YAML validation error on malformed YAML.
  - Added valid workflow (`e2e-test.yml`), verified auto-saving and correct path prefixing (`.grain/workflows/e2e-test.yml`).
  - Removed workflow with confirmed deletion prompt, verified clean removal.
- **Release Job Lists**:
  - Verified deduplication and whitespace trimming of post-merge dispatch jobs and base branch jobs.

### 4. Team-Scoped Repository Configuration
- Navigated to `/teams/default/repos/acme/widgets`.
- Verified context banner indicating inheritance of global defaults.
- Configured team-scoped setup command override. Reload verified override persisted without altering global setting.
- Tested "Revert to global defaults" dialog: dismiss preserves override; confirm deletes override and restores global value.

### 5. Memory UI (`/team-settings` -> Models tab)
- Verified deployment-wide memory and repo-specific memory (`acme/widgets`).
- Inspected author attribution chips ("written by the memory agent", "edited here").
- Opened memory documents (`release-checklist`, `flaky-e2e`) in place; verified each document's fields are uniquely labeled.
- Made reversible correction in `acme/widgets` memory, saved, reloaded to confirm persistence ("written today"), and restored original content.
- Verified unsaved changes guard: leaving tab or pressing Escape prompts user before discarding unsaved edits.

### 6. Releases and Qualification (`/repos/acme/widgets/releases`)
- Tested release creation validation: spaces or invalid characters are rejected with descriptive alerts.
- Created release `v4.0-1009`. Verified status transitions to "Provisioning" and RC cutting.
- Configured qualification plan (auto-promote and require-approval switches), saved plan.
- Simulated external boundaries using `releasehelper`:
  - `mark-candidate-error`: verified simulated git branch failure alert.
  - `mark-release-provisioned` & `mark-candidate-cut`: verified release transitions to active.
  - `mark-candidate-promoted`: verified RC promotion.
  - `mark-release-merged`: verified merge request and merged state.
- Verified switching between releases does not leak previous candidate state.
- Exercised fallback and deep link URLs: `/repos/acme/widgets/releases/nope` (normalizes with alert), `/repos/zeta/alpha-tools/releases`, and `/releasetrains`.

### 7. Release Trains & Manual Dispatch
- Toggled automatic releases on/off and verified state persisted across reload.
- Tested manual release dispatch ("Run release by hand"):
  - Unstubbed dispatch correctly handled demo limitation ("no grain CI wired").
  - Intercepted 502 Bad Gateway response; verified UI presents error banner without clearing form.
  - Intercepted slow 202 response; verified button disabling during flight and subsequent status view.
  - Verified focus restoration on close.

## Verification & Test Results
- Full UI E2E test suite for repository, release, and memory workflows run via Playwright:
  - `e2e/repository_release_memory_walkthrough.spec.js`: 2 passed (complete workflow + release switching isolation).
  - `e2e/repo_release_memory_edits_walkthrough.spec.js`: 3 passed (workflow mid-save/removal, memory unsaved guard, qualification dependencies).
  - `e2e/repo-pane.spec.js`: 2 passed.
  - `e2e/repo-ci-capabilities.spec.js`: 1 passed.
  - `e2e/repo-ownership-ci-security.spec.js`: 1 passed.
  - `e2e/repo-team-overrides.spec.js`: 1 passed.
  - `e2e/repos_workflows.spec.js`: 1 passed.
  - `e2e/memory.spec.js`: 3 passed.
  - `e2e/release-run-ref-checks-walkthrough.spec.js`: 2 passed.
  - Total: 16 passed, 0 failed.
- All deep walkthrough edge case scenarios passed cleanly with zero unhandled errors.
