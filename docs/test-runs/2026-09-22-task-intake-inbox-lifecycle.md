# Task Intake, Inbox, and Lifecycle Walkthrough (2026-09-22)

## Build under test

- Repository/branch: `bwsalmon/grain`, `grain/task-306`
- Commit: `641fb224f40a10e9b67addaa0e34aedc75e626e8` (`Merge pull request #1812 from bwsalmon/grain/task-305`)
- Environment: Debian 12 amd64 sandbox; Go 1.26.6; Node 20.19.0; npm 10.8.2; Playwright 1.62.1 with headless Chromium 151 at 1440×1000.
- App: production UI from `npm --prefix ui run build`, served by the freshly built `grain demo -addr 127.0.0.1:0 -open=false` over one throwaway embedded SQLite store.
- Stubs: none. The prescribed demo seed is synthetic data, but the store, server, built frontend, HTTP requests, Chromium layout, and all operator interactions were real.

## Walkthrough

1. Loaded `/inbox` cold. The rail and heading showed the seeded items waiting for operator action. The groups “Answer a question”, “Submit for merge”, “Read an answer”, and “Approve a proposal” each showed count 1 and their respective seeded task rows: “Investigate the merge queue stall”, “Cache the capability readiness probe”, “Why did last week's nightly suite slow down?”, and “Add a dark mode toggle”.
2. Tested row navigation by clicking the proposal row into `/tasks/1`, confirming the detail overlay header and URL update, then returned to `/inbox` via the overlay Back button. Verified rail navigation between All tasks and Inbox.
3. Approved proposal “Add a dark mode toggle” directly from its Inbox row. The proposal immediately cleared from the group.
4. Answered parked question on “Investigate the merge queue stall” with reply text (“Just unblock the queue for now.”). The task cleared from the Inbox group and returned to the queued state.
5. Submitted “Cache the capability readiness probe” for merge and acknowledged the answer for “Why did last week's nightly suite slow down?”. Both rows cleared, reaching the empty inbox state: “Nothing is waiting on you. Every task is either running, queued or done.”
6. Opened global New task overlay from rail. Verified validation: the “Create task” button remained disabled while Title was blank. Verified the close/cancel control cleanly dismissed the dialog without creating a task.
7. Filed a no-repo proposal task: entered title and description, selected “No repo”, unchecked “Queue immediately” to mark as proposed, opened Advanced options, added a custom prompt extension, and submitted. Verified the proposed task rendered in the list with a `.badge-proposed` badge.
8. Filed a queued task: entered title and description, selected repo `acme/widgets`, set base branch `main`, verified “Queue immediately” was checked, and submitted. Verified the queued task rendered in the list with a `.badge-queued` badge.
9. Verified Search, Sort, Compound Filtering, and URL persistence:
   - Searched for “Walkthrough” in the search bar; confirmed live query filtering and `?q=Walkthrough` URL parameter update.
   - Performed a hard reload on the search URL and verified the query input value, filtered rows, and count persisted.
   - Cleared search to restore all tasks.
   - Tested browser history: deep-linked into task detail, navigated back using Browser Back (overlay dismissed), and navigated forward with Browser Forward (detail restored).
10. Exercised Task Detail View editing and lifecycle:
    - Clicked “Edit” in the detail header and updated Title and Description. Saved changes and verified live persistence.
    - Deferred task indefinitely via “Defer” button; verified `.badge-deferred` state.
    - Undeferred task via “Undefer” button; verified return to `.badge-proposed`.
    - Approved proposal from detail view via “Approve” button; verified transition to `.badge-queued`.
    - Inspected running task detail (“Bump the Go toolchain to 1.24”) and opened the Prompt dialog, confirming prompt template text.
11. Exercised Bulk Operations:
    - Selected multiple task checkboxes in the task list.
    - Verified the floating batch action bar appeared with the exact count (“2 selected”).
    - Opened “Bulk edit…” modal and cancelled: verified selection remained intact.
    - Opened “Bulk edit…” modal, checked “Base branch”, entered `release/bulk-walkthrough`, and applied the bulk update. Verified successful batch PATCH execution and selection reset.
12. Loaded non-existent task URL `/tasks/non-existent-task-99999` (404 handling). Confirmed SPA cleanly rendered the task list without unhandled exceptions or crashing.

## Robustness observations

- Monitored console messages, page errors, and network request failures during full browser execution.
- No application crashes, runtime exceptions, or unexpected mutation failures occurred.
- Full E2E suite (`npm run test:e2e`) executed with 125 passed across Chromium and WebKit projects.
- Full Vitest frontend suite (`npm run test`) executed with 131 test files passed and 2,360 individual unit/component tests passing.
- Backend UI package test suite (`go test -v ./pkg/ui/...`) executed with 100% test passes.

## Verification

- Playwright walkthrough execution against live `grain demo` server: all scenarios and assertions passed cleanly.
- Full Playwright suite: 125 passed, 1 skipped.
- Frontend unit tests: 131 files passed, 2,360 tests passed.
- Go backend package tests (`./pkg/ui/...`): passed.
