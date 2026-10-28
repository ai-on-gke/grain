# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-08
- **Task**: grain/task-2520
- **Status**: Passed cleanly. All repository, release, qualification, and memory UI operator flows verified end-to-end in real browser sessions against a real `grain demo` server.
- **Previous Defect Verifications**: All five fixes introduced on 2026-10-07 (workflow mid-save preservation, unsaved memory prompts, qualification dependency pruning, release focus, duplicate job list suppression) were confirmed intact and passing regression suites.
- **New Findings**: No regressions or new defects detected. All UI controls, validation constraints, persistence semantics, and layout boundaries behaved as designed.

## Environment

- **Commit walked through**: `fb6f746cd` (`fb6f746cdddb9387d0eaf24ec982b8e447ae01bf`), tip of `candidate` / `grain/task-2520`.
- **Host**: Debian GNU/Linux (Grain sandbox VM), Linux amd64. Go 1.26.6, Node.js 20.19.0.
- **Browser**: Chromium Headless Shell (Playwright 1.62.1 / Chromium 151.0.7922.34).
  - Executed via Playwright browser automations against real HTTP services (no DOM mockers or jsdom).
  - Primary viewport: 1400×950 desktop; responsive validation at 390×844 mobile viewport.
  - Telemetry monitored:
    - Console messages, warnings, and errors.
    - Unhandled page runtime exceptions.
    - Network request failures and API response codes.
    - Mutating HTTP requests (`POST`, `PUT`, `PATCH`, `DELETE`) with request/response payloads.
    - Modal confirmation dialogs (`window.confirm()`).
- **Server**: Real `grain demo` instance.
  - Built fresh from repository source: frontend assets compiled via `npm ci && npm run build` (Vite) into `pkg/ui/static`, daemon binary built via `go build ./cmd/grain`.
  - Executed with isolated temporary state directory over embedded SQLite.
- **Account**: Operator account (`operator`), provisioned via initial setup registration flow.
- **Model tooling**: No Gemini API credentials present in test sandbox; verified with standard daemon configuration.

### External Boundaries & Simulation

| Boundary | Why | Mechanism | Responses Exercised |
|---|---|---|---|
| GitHub branch listing (`GET /api/repos/*/*/github-branches`) | Demo server runs offline without GitHub connection. Real API returns 404 "GitHub branch listing is not available on this UI server" when unstubbed. | Intercepted via Playwright `page.route` exclusively on this endpoint for New Task & branch picker flows. | 200 `["main", "candidate", "release/4.0"]` |
| GitHub release reconciler transitions | Demo daemon runs without background GitHub reconciler. | Exercised via `ui/e2e/releasehelper` executing direct `model.Store` mutations on the demo database. | `mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-error`, `sync-qualifications` |
| Release comparison (`GET …/compare`) | Repository comparison diffing requires remote git daemon. | Unstubbed. Verified real server 404 response handling. | 404 "repository comparison is not available on this UI server" |

All Grain API endpoints, React frontends, routing behaviors, and user interactions were exercised against the live application without UI stubbing.

---

## Steps and Outcomes

### 1. Repositories List (`/repos`) & Navigation

1. **Rail & Direct URL Navigation**:
   - Navigating via the rail icon button (`role=button`, "repos") transitioned seamlessly to `/repos`.
   - Direct cold load of `/repos` rendered correctly at both 1400×950 and 390×844 viewports.
2. **Initial Seed & Badge**:
   - Pre-seeded `acme/widgets` repository rendered immediately in the list.
   - Header repository counter badge reflected accurate initial count (`1`).
3. **Repository Creation**:
   - Created valid repositories via `owner/name` input (`acme/gadgets`, `zeta/alpha-tools`, and long name `example-corporation/exceptionally-long-repository-name-for-responsive-layout`).
   - Counter badge incremented dynamically after each addition.
4. **Input Validation**:
   - Attempted invalid format `acme/` (trailing slash without repo name). Server returned expected 400 Bad Request (`repo: repo must be owner/name, got "acme/"`).
   - UI preserved entered input in the input field without clearing or crashing.
5. **Search & Filter**:
   - Filter query `ALPHA` narrowed results to matching `zeta/alpha-tools` row.
   - Non-matching filter `nonexistent-search-term` presented empty state: "No repos match your search."
6. **Sorting**:
   - Switched sort order to **Name (A–Z)**: repositories sorted alphabetically (`acme/gadgets`, `acme/widgets`, `example-corporation/...`, `zeta/alpha-tools`).
   - Switched to **Custom order**: drag handles (`button.task-drag-handle`) rendered for each repository row.
7. **Keyboard & Pointer Reordering**:
   - Focused first reorder handle and pressed `Enter`; Moving bar activated ("Moving 1 repo").
   - Navigated to target destination slot and pressed `Enter`; repository reordered and Moving bar dismissed.
   - Persistence confirmed across page reload (stored in client preferences).
8. **Responsive Layout Check**:
   - Resized viewport to 390×844 mobile resolution. Evaluated document `scrollWidth` vs `clientWidth`. No horizontal overflow detected.

### 2. Repository Detail & Back Navigation (`/repos/acme/widgets`)

1. **Row Navigation**:
   - Clicking `acme/widgets` row from `/repos` navigated to `/repos/acme/widgets`.
2. **Task Count Filter Link**:
   - Clicked "12 tasks filed here" (`/?f=repo:any:acme/widgets`). Filtered task list opened showing only tasks associated with `acme/widgets`.
   - Browser navigation (`page.goBack()`) returned smoothly to `/repos/acme/widgets` with full page state preserved.

### 3. Task Filing with Preselection & Relationships

1. **New Task Drawer**:
   - Clicking "New task" on the repository page opened the creation drawer.
   - Target repository was automatically preselected to `acme/widgets`.
2. **Task Creation & Persistence**:
   - Populated task title and description; submitted task creation request.
   - Task filed successfully via `POST /api/tasks`, transitioning back to the repository view.

### 4. Repository Configuration, Capabilities, and Workflow Synchronization

1. **Branch Management**:
   - Added branch `walkthrough/operator-branch` via "Add branch". Successfully registered.
   - Attempted duplicate branch submission; UI correctly displayed notice: "already on this repo's list".
2. **Branch Compare**:
   - Entered head branch `walkthrough/operator-branch` and clicked "Compare".
   - Navigated to `/repos/acme/widgets/compare/main...walkthrough/operator-branch`. Unstubbed server returned real 404 message ("repository comparison is not available on this UI server"). Back button returned to repo page.
3. **Capability Configurations**:
   - Modified CI capabilities to include GCP Key (`PUT /api/repos/acme/widgets/ci-capabilities`).
   - Modified default capabilities to include Self Debug (`PUT /api/repos/acme/widgets/capabilities`).
4. **Prompt Extension & Setup Command**:
   - Updated prompt extension text: "Standing prompt instructions for acme/widgets walkthrough." (`PUT /api/repos/acme/widgets/prompt-extension`).
   - Updated setup command: `make test && echo walkthrough-ready` (`PUT /api/repos/acme/widgets/setup-command`).
5. **CI Workflow Declarations**:
   - Added new workflow `ci.yml` via "Add workflow".
   - Entered workflow YAML payload; on blur, UI successfully sent `PUT /api/repos/acme/widgets/workflows`.
6. **Reload Persistence**:
   - Hard reloaded `/repos/acme/widgets`.
   - Verified that prompt extension, setup command, CI capabilities, and workflow definitions persisted intact.
7. **Workflow Removal & Confirmation**:
   - Clicked "Remove workflow .grain/workflows/ci.yml". Browser confirmation dialog intercepted and accepted (`Delete the workflow .grain/workflows/ci.yml grain declares for acme/widgets? Nothing else keeps a copy of it.`). Workflow removed cleanly.

### 5. Deployment and Repository Memory UI

1. **Memory Settings Panel**:
   - Navigated to `/teams/default/settings/models`.
   - Repository memory editor for `acme/widgets` displayed existing notes.
2. **Reversible Memory Edit**:
   - Appended test note (`- Walkthrough test note: verified on 2026-10-08.`) and clicked "Save acme/widgets memory" (`PUT /api/memory`).
3. **Full Document Inspection**:
   - Clicked "Open flaky-e2e (acme/widgets)"; document drawer opened showing full content. Closed drawer via "Close flaky-e2e (acme/widgets)".
4. **Readback & Reversion**:
   - Reloaded settings page; verified test note persisted in readback.
   - Reverted memory back to original content and saved, restoring pristine state.

### 6. Releases and Qualification

1. **Releases List Navigation**:
   - Navigated to `/repos/acme/widgets/releases`.
2. **Validation on Release Creation**:
   - Entered invalid release name with spaces (`invalid release name with spaces`).
   - Server returned 400 Bad Request; UI rendered error alert.
3. **Valid Release Creation**:
   - Created valid release `rel-walkthrough-1791501020493` via `POST /api/repos/acme/widgets/releases`.
   - Page URL redirected to `/repos/acme/widgets/releases/rel-walkthrough-1791501020493`.
4. **Qualification Plan Configuration**:
   - Toggled "Promote automatically" and "Require approval" switches. Saved qualification plan successfully.
5. **Reconciler State Transitions**:
   - Executed `releasehelper mark-candidate-error`: Reloaded page displayed simulated remote failure.
   - Executed `releasehelper mark-release-provisioned`, `mark-candidate-cut`, and `sync-qualifications`: Reloaded page displayed active release candidate (`rel-walkthrough-1791501020493.rc.1`).

### 7. Release Trains & Navigation Fallbacks

1. **Release Trains Routes**:
   - Loaded `/repos/acme/widgets/trains`; page rendered with appropriate repo context.
   - Loaded global `/releasetrains`; route redirected cleanly to `/` as expected.

---

## Automated Test Suites

1. **Vitest Unit & Component Suite**:
   - Executed `npm test` in `ui/`.
   - **Result**: 192 test files passed, 3,165 tests passed (0 failures).
2. **Playwright End-to-End Walkthrough Suites**:
   - Executed `npx playwright test e2e/repository_release_memory_walkthrough.spec.js e2e/repo_release_memory_edits_walkthrough.spec.js`.
   - **Result**: 5 passed (33.7s).
     - `e2e/repo_release_memory_edits_walkthrough.spec.js`:
       - the repo's workflow editor keeps a row added mid-save, and a removal (passed)
       - leaving Team settings asks before dropping an unsaved memory edit (passed)
       - removing a qualification item drops it from the others' dependencies (passed)
     - `e2e/repository_release_memory_walkthrough.spec.js`:
       - complete repository, release, qualification, and memory operator workflow (passed)
       - switching releases never shows the previous release's candidate (passed)

---

## Telemetry Summary

- **Console Errors**: 3 (deliberately provoked bad request for invalid repo name, expected unstubbed compare 404, and expected invalid release name 400).
- **Page Errors**: 0 unhandled exceptions.
- **Failed Requests**: 0 network-level failures.
- **Mutating Requests**: 17 mutating requests sent and verified.
- **Confirm Dialogs**: 1 dialog accepted (workflow removal confirmation).
- **DOM Stability**: No layout regressions, focus stranding, or scroll overflow detected at desktop or mobile viewports.
