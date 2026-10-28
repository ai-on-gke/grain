# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-10
- **Commit tested**: `3dc993bee3f7f2a7cdb2d1a8384c4e4791a042b1` (Merge pull request #3344 from bwsalmon/grain/task-2821)
- **Environment**:
  - OS: Debian GNU/Linux (amd64)
  - Browser: Chromium 151.0.7922.34 (Playwright 1.62.1 headless shell)
  - Runtime: Node.js v20.19.0, Go 1.26.6
  - Server: Real `grain demo` server with embedded SQLite throwaway store (`/tmp/grain-e2e-run-*`)
  - Frontend: Built production React distribution (`pkg/ui/static`, built via Vite v6.4.3)
- **Tooling / Model Context**: Sandbox does not carry `/home/debian/.gemini-api-key`. All validations conducted via real browser interactions and deterministic fixtures.

---

## External Boundaries and Stubs

In accordance with the test protocol, Grain's own API, React state, and SQLite stores remained real and untouched. Only unavailable external boundaries (GitHub remote API and external CI systems) were intercepted:

| Boundary / Endpoint | Mode | Purpose | Observed Outcome |
| --- | --- | --- | --- |
| `GET /api/repos/*/*/github-branches` | Stubbed / Fixture | Simulated remote repository branch listing (`["main", "candidate", "release/4.0"]`). | Correctly populates head/base branch dropdowns and comparisons. |
| `POST /api/repos/acme/widgets/release-runs` | Unstubbed & Stubbed | Verified real demo refusal ("disabled: no grain CI wired") without CI, simulated 502 Bad Gateway, and simulated slow 202 in-flight dispatch. | Real demo gracefully rejects with inline alert; 502 displays error banner while keeping input; 202 disables submit button during flight and resets form on complete. |
| GitHub Release Reconciler | Simulated via `/tmp/releasehelper` CLI (`mark-release-provisioned`, `mark-candidate-cut`, `mark-candidate-error`, `mark-candidate-promoted`, `mark-release-merged`, `sync-qualifications`) | Simulates background GitHub release state transitions and branch cuts over loopback HTTP. | UI accurately transitions through Provisioning, Cutting RC, Error alerting, Active promotion, and Merged states. |

---

## Detailed Step-by-Step Operator Walkthrough & Outcomes

### 1. Repositories Index (`/repos`)
- **Direct URL & Navigation Rail**:
  - Navigated directly to `/repos` and via the navigation rail's "Repos" button.
  - Verified header displays exact repository count matching total row items (`.content-header .count`).
- **Seeded & Added Repositories**:
  - Inspected default seeded repository `acme/widgets` with active task counts.
  - Added multiple repositories (`acme/gadgets`, `zeta/alpha-tools`, and an exceptionally long name: `example-corporation/exceptionally-long-repository-service-name-for-responsive-layout-testing`).
  - Confirmed repository list updated immediately and counter badge updated accurately.
- **Search Filtering**:
  - Case-insensitive search: Searching `WIDGETS` properly isolated `acme/widgets`.
  - Substring search: Searching `alpha` isolated `zeta/alpha-tools`.
  - Non-matching query: Searching `nonexistent-repo-query-xyz` yielded zero rows and rendered "No repos match your search." empty-state message.
  - Clearing search restored all rows.
- **Sorting & Reordering**:
  - Sorted by **Name (A–Z)**: Verified alphabetical ordering; verified reorder drag handles were correctly hidden.
  - Sorted by **Most tasks**: Verified repositories with highest task counts (`acme/widgets`) were ordered first.
  - Sorted by **Custom order**: Drag handles (`button.task-drag-handle`) re-appeared on each row.
  - Reordered via pointer click and drop slots (`button.reorder-slot-button`).
  - Reordered via keyboard affordances: focused drag handle, pressed `Enter` to initiate move, navigated to target drop slot, pressed `Enter` to drop. Focus was accurately restored to the moved drag handle.
  - Reloaded page and verified custom order persisted in `localStorage`.
- **Responsive Layout**:
  - Tested mobile viewport (390x844). Long repository name wrapped cleanly without causing horizontal scrolling or layout clipping (`scrollWidth <= clientWidth`).
  - Restored desktop viewport (1400x950).

### 2. Repo Detail, Task Count Filtering, and Task Creation
- **Repo Detail Navigation**:
  - Clicked `acme/widgets` to navigate to `/repos/acme/widgets`. Verified header, task badges, and GitHub link.
- **Task Count Filter & Back Navigation**:
  - Clicked the link `tasks filed here`. Verified browser navigated to task list filtered by `/?f=repo:any:acme/widgets`.
  - Used browser `goBack()` navigation to return to `/repos/acme/widgets`; confirmed page state restored without errors.
- **Task Filing from Repo Page**:
  - Clicked `New task` on the repository page.
  - Verified the target repository dropdown was automatically preselected with `acme/widgets`.
  - Specified extra write repository (`acme/gadgets` on branch `candidate`) and extra read-only repository (`zeta/alpha-tools`).
  - Tested suggestion menu keyboard dismissal: pressed `Escape` with autocomplete options open; verified suggestion popover closed while retaining drawer state and input values.
  - Created task and verified clean return to repo page.

### 3. Branch Rules, Capabilities, Prompt Instructions & Workflows
- **Branch Management**:
  - Added new branch `task-2828/walkthrough-branch`. Confirmed appearance in branches list and persistence across reload.
  - Added duplicate branch name; verified inline notice (`task-2828/walkthrough-branch is already on this repo's list`) and confirmed no duplicate entries were created.
  - Compared branch against base via `Compare` button; opened `/repos/acme/widgets/compare/...` diff view and returned.
- **CI and Default Capabilities**:
  - Toggled CI-only capability (`GCP key`) and default capability (`Self debug`).
  - Reloaded page; verified selected capability chips persisted accurately.
- **Prompt Extension & Setup Command**:
  - Edited "Prompt extension for acme/widgets" and "Setup command for acme/widgets".
  - Verified auto-save on blur; confirmed values persisted across page reload.
- **Workflow Declaration (`.grain/workflows/*.yml`)**:
  - Clicked `Add workflow`. Added `ci-build.yml`.
  - Tested YAML syntax validator by typing invalid/unclosed YAML (`broken: [unclosed: yaml`); verified error alert rendered.
  - Replaced with valid YAML; verified error cleared automatically on blur.
  - Reloaded and confirmed persisted workflow path (`.grain/workflows/ci-build.yml`).
  - Clicked `Remove workflow .grain/workflows/ci-build.yml`; verified native browser confirmation dialog ("Delete the workflow .grain/workflows/ci-build.yml grain declares for acme/widgets? Nothing else keeps a copy of it."). Accepted dialog; verified workflow was completely removed and persisted as empty on reload.

### 4. Team-Scoped Overrides and Inheritance
- Navigated to `/repos/acme/widgets?team=default`.
- Confirmed alert banner rendered: "Viewing repository settings inheriting global defaults for this team."
- Set a team-scoped override for "Setup command for acme/widgets (team default)".
- Confirmed banner updated to: "This repository has team-specific configuration overrides active for this team."
- Verified global repository settings at `/repos/acme/widgets` remained completely untouched.
- Clicked "Revert to global defaults" button in team context; accepted confirmation dialog.
- Verified team setup command reverted to inherit global default value.

### 5. Deployment and Repo Memory (`/team-settings` -> Models)
- Navigated to `/team-settings` and selected the "Models" tab.
- Inspected "This deployment" memory ("Memory carried by every run") and repository-scoped memory ("Memory about acme/widgets").
- Verified attribution indicators ("written by the memory agent", "edited here").
- Opened document `release-checklist` in place; verified text content displayed.
- Opened second document `flaky-e2e (acme/widgets)`; verified both documents display disambiguated summary input labels (`Summary of release-checklist (this deployment)` and `Summary of flaky-e2e (acme/widgets)`).
- Closed document view and confirmed focus returned properly.
- **Reversible Correction**:
  - Edited repository memory with additional verification note; clicked `Save acme/widgets memory`.
  - Confirmed badge updated to "written today".
  - Reloaded page and verified updated memory persisted in throwaway SQLite store.
  - Reverted memory back to original state and saved.
- **Unsaved Changes Guard**:
  - Edited deployment memory without saving and pressed `Escape`.
  - Verified native confirmation dialog prevented accidental discard. Accepted dialog to cleanly dismiss.

### 6. Releases and Release Trains UI (`/repos/acme/widgets/releases`)
- Navigated to `/repos/acme/widgets/releases`.
- **Validation**:
  - Tested invalid release name with spaces; verified rejection with inline error alert.
- **Release Creation & State Transitions**:
  - Created valid release `rel-walkthrough-20261010`.
  - Verified initial state: "Provisioning" and candidate cutting (`rel-walkthrough-20261010.rc.1 ... cutting`). Verified "Promote current RC" button was disabled.
- **Qualification Plan Configuration**:
  - Configured qualification plan options: toggled "Promote automatically once qualification succeeds" and "Require approval before running qualification tasks".
  - Saved plan successfully.
- **Release Reconciler Simulation (`releasehelper`)**:
  - Stimulated candidate error via `mark-candidate-error`; verified simulated remote git branch failure alert was displayed.
  - Simulated provisioned state via `mark-release-provisioned` and `mark-candidate-cut`; ran `sync-qualifications`.
  - Reloaded page; verified candidate transitioned to `active` and "Promote current RC" became enabled.
  - Promoted candidate via `mark-candidate-promoted`.
  - Marked release merged via `mark-release-merged`; verified release merged state.
- **Deep Links & Route Normalization**:
  - Navigated to `/repos/acme/widgets/releases/non-existent-release-xyz`; verified page normalized to `/repos/acme/widgets/releases` with alert banner `no release "non-existent-release-xyz"`.
  - Visited `/repos/acme/widgets/trains` and `/releasetrains`; verified standard fallback routes render valid application frames without crashing.

### 7. Manual Release Dispatch & External Boundaries
- Toggled automatic releases off/on in `/repos/acme/widgets`; confirmed state persisted across page reload.
- Opened "Run release by hand" form.
- Verified real demo refusal without CI: returned expected inline error alert without crashing.
- Intercepted 502 Bad Gateway response from external GitHub API: verified error banner displayed and form remained editable.
- Intercepted slow 202 response: verified "Run release" button disabled during flight, preventing double dispatch.
- Verified form unmounted upon successful dispatch and called `onOpenRun`.

---

## Verification & Test Results

The full UI E2E test suite was executed against the real `grain demo` server and Chromium headless shell:

```
Running 17 tests using 2 workers

  ✓  e2e/memory.spec.js › shows what this deployment remembers, and each repo's own (1.0s)
  ✓  e2e/memory.spec.js › opens a document in place, which the listing did not carry (891ms)
  ✓  e2e/memory.spec.js › corrects a memory, and the daemon has it on the next open (1.4s)
  ✓  e2e/repo_release_memory_edits_walkthrough.spec.js › the repo's workflow editor keeps a row added mid-save, and a removal (1.7s)
  ✓  e2e/repo_release_memory_edits_walkthrough.spec.js › leaving Team settings asks before dropping an unsaved memory edit (1.6s)
  ✓  e2e/repo_release_memory_edits_walkthrough.spec.js › removing a qualification item drops it from the others' dependencies (2.8s)
  ✓  e2e/repo-ci-capabilities.spec.js › walkthrough: RepoPage CI-only capability selection, persistence, and workflow generation (2.4s)
  ✓  e2e/repo-ownership-ci-security.spec.js › walkthrough: repository ownership models and CI capability security boundaries (1.1s)
  ✓  e2e/repo-pane.spec.js › opens a repo's own page from a plain list row and files a task scoped to it (1.7s)
  ✓  e2e/repo-pane.spec.js › reaches a repo's branches, capabilities and releases from its page (918ms)
  ✓  e2e/repo-team-overrides.spec.js › a team's override is what the field shows, and an edit in one team leaves the other team and the global value alone (1.4s)
  ✓  e2e/repos_workflows.spec.js › inspects repo details, auto-saves prompt extension & setup command, adds branch, and manages workflows (2.5s)
  ✓  e2e/comprehensive_operator_walkthrough.spec.js › thorough end-to-end operator walkthrough with deep verification (26.3s)
  ✓  e2e/release-run-ref-checks-walkthrough.spec.js › release runs: unreviewed refs and pinned commits are refused, a reviewed branch starts (1.6s)
  ✓  e2e/release-run-ref-checks-walkthrough.spec.js › fake CI runs show pending on the CI page until cancelled there (1.1s)
  ✓  e2e/repository_release_memory_walkthrough.spec.js › complete repository, release, qualification, and memory operator workflow (24.7s)
  ✓  e2e/repository_release_memory_walkthrough.spec.js › switching releases never shows the previous release's candidate (1.5s)

17 passed (52.1s)
```

- **Console & Network Health**:
  - No unexpected unhandled JavaScript exceptions or console errors.
  - No unintended 4xx/5xx network failures outside of intentional boundary probes.
  - All keyboard shortcuts, focus restorations, and confirmation dialogs performed as specified.
- **Defects Found**: None. All repository, release, and memory UI behaviors were fully verified, robust, and cleanly persisted.
