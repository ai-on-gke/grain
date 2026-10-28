# Multi-branch task configuration and cross-branch proposals test run

## Build under test

- Repository/branch: `bwsalmon/grain`, `grain/task-318`
- Commit: `2dd12791796ac404e200c71e00a91c4d1011e173` (`Merge pull request #1821 from bwsalmon/grain/task-314`)
- Environment: Debian 12 amd64 sandbox; Go 1.26.6; Node 20.19.0; npm 10.8.2; Playwright 1.62.1 with Chromium 151 at 1440×1000.
- App: Production UI build (`npm run build`), served by `grain demo` over an embedded SQLite store with real orchestrator and API endpoints.
- Scope: Verification of Section 1 of `docs/test-plans/2026-09-22.md` ("Multi-branch task configuration and cross-branch proposals").

## Walkthrough & Lifecycle Execution

1. **Environment Setup**: Booted embedded `grain demo` instance with SQLite backend and initialized default demo state.
2. **Multi-Branch Parent Task Creation**:
   - Opened New Task overlay (`+ New task`).
   - Specified target repository `acme/widgets` with base branch `main`.
   - Opened Advanced Options -> Extra write repositories / branches.
   - Added secondary write target for same repository: `acme/widgets` on branch `release/1.0`.
   - Added secondary write target for second repository: `acme/tools` on branch `main`.
   - Submitted task creation; verified parent task row was stored and displayed in task list.
3. **UI Rendering & Key Stability Verification**:
   - Verified task list rendered write target chips:
     - `acme/widgets (write)` with tooltip `may also land work in acme/widgets on release/1.0`
     - `acme/tools (write)` with tooltip `may also land work in acme/tools on main`
   - Verified no duplicate React key console warnings or rendering collisions occurred.
   - Deep-linked into Task Detail overlay (`/tasks/<id>`) and verified write target input fields were populated with respective base branches (`release/1.0` and `main`).
4. **Detail View Edit & Mutation Isolation**:
   - Edited the base branch for `acme/widgets` from `release/1.0` to `release/1.1` in the detail view. Verified that the edit affected only the targeted row without mutating other write target rows.
   - Removed the `acme/widgets` write target using the remove control; verified that only the targeted row was removed while `acme/tools` remained intact.
5. **Cross-Branch Proposals Execution & Auto-Merge Verification**:
   - **Declared Branch Proposal**:
     - Submitted proposal targeting declared write target `acme/widgets` on branch `release/1.0` with `autoMerge: true`.
     - Confirmed task was queued (`queued: true`) and retained `autoMerge: true` via `model.LandsWhereGranted` matching `task.Writes`. No refusal notice was attached.
   - **Undeclared Branch Proposal**:
     - Submitted proposal targeting undeclared branch `acme/widgets` on branch `release/2.0`.
     - Confirmed auto-merge was withheld (`auto_merge: false`) and task remained in proposed state (`state: proposed`).
     - Verified explanatory note was appended: `grain: this task was filed by an agent running on branch main, but asked to land on branch release/2.0. That may be what you want, but auto-merge is a permission for one branch, so a human has to approve this task before it will run.`
6. **Proposal Detail View Verification**:
   - Navigated to `/tasks/<proposal-id>` for the undeclared branch proposal.
   - Verified base branch input rendered `release/2.0`.
   - Verified `.badge-proposed` badge displayed in detail sidebar.
   - Verified auto-merge permission warning note rendered clearly in task conversation body.

## Issues Identified & Resolved

1. **`WriteReposField.jsx` Mutation & Removal By Repo Name**:
   - *Problem*: `setBase`, `remove`, and `onCommit` matched targets by `w.repo === repo`, which caused mutating or removing one branch to mutate or delete all configured branches for that repository.
   - *Fix*: Switched operations to index-based mutation (`setBase(index, base)`, `remove(index)`, `onCommit(updatedWrites)`).
2. **`WriteReposField.jsx` Repository Offer Filtering**:
   - *Problem*: `offered` filtered out `target` repo and any repo already picked, preventing users from adding a second branch target on the task's primary repo or multiple branches for secondary repos.
   - *Fix*: Allowed adding multiple branch targets per repository and removed target repository filtering.
3. **`WriteReposField.jsx` Keystroke Focus Loss & React Key Collision**:
   - *Problem*: Keys constructed with mutable base branch values caused unmounting on typing, while repo-only keys caused React key collisions.
   - *Fix*: Used stable row key `${w.repo}:${index}`.
4. **`TaskList.jsx` Write Chip Key Collision**:
   - *Problem*: Write target chips in task list rows were keyed solely on `w.repo`, causing duplicate React key warnings when multiple targets used the same repository.
   - *Fix*: Updated chip key to `${w.repo}:${w.base || ""}:${i}`.

## Verification & Test Results

- **Vitest Frontend Unit Suite**:
  - `WriteReposField.test.jsx`: 7 passed (testing multiple branches per repo, target repo selection, per-row base branch mutations).
  - Full suite: 129 test files passed, 2,342 tests passed (0 failures).
- **Playwright E2E Suite**:
  - `ui/e2e/multi-branch.spec.js`: passed (covering full parent task multi-branch creation, detail overlay editing, declared vs undeclared proposal auto-merge handling, and UI rendering).
  - Full E2E suite: 102 tests passed across test suites.
- **Go Backend Test Suite**:
  - `go test ./pkg/...`: All packages passed, including `pkg/orchestrator/finish_test.go` and `pkg/model`.
