# Task intake, Inbox, and lifecycle walkthrough — 2026-09-14

## Build under test

- Repository/branch: `bwsalmon/grain`, `grain/task-41`
- Commit: `da20fbe72b9492ac6c3193511115173cbdcb174e` (`Merge pull request #1330 from bwsalmon/grain/task-25`)
- Environment: Debian 12 amd64 sandbox; Go 1.26.6; Node 20.19.0; npm 10.8.2; Playwright 1.62.1 with headless Chromium 151 at 1440×1000.
- App: production UI from `npm --prefix ui run build`, served by the freshly built `grain demo -addr 127.0.0.1:0 -open=false` over one throwaway embedded SQLite store.
- Model tooling: Gemini 2.5 Flash reviewed the ordered coverage checklist through the live Gemini API. The key was read into process environment only and was not printed or written.
- Stubs: none. The prescribed demo seed is synthetic data, but the store, server, built frontend, HTTP requests, Chromium layout, and all gestures were real.

## Walkthrough

1. Loaded `/inbox` cold. The rail and heading showed 4 items. The groups “Answer a question”, “Submit for merge”, “Read an answer”, and “Approve a proposal” each showed count 1 and the correct seeded row. Opened the proposal row into `/tasks/1`, then returned to `/inbox`. Re-entered Inbox from All tasks by focusing its rail button and pressing Enter.
2. Approved “Add a dark mode toggle” from its Inbox row. It disappeared, Inbox fell to 3, and Queued showed the task with a queued badge. Replied “Just unblock the queue for now.” to the actual seeded question on “Investigate the merge queue stall”; it disappeared, Inbox fell to 2, and Queued showed it as queued.
3. Submitted “Cache the capability readiness probe” and acknowledged “Why did last week’s nightly suite slow down?”. Both left their groups. Inbox reached the real empty state: “Nothing is waiting on you. Every task is either running, queued or done.” The zero count was omitted from the rail label as designed.
4. Opened global New task. Verified Create remained disabled with no title/target and with a whitespace-only title, enabled only after an explicit No repo choice, and cancellation via the visible close control created nothing. Expanded Advanced options and exercised framework, network, and sandbox shape inputs.
5. Filed a no-repo proposal with description, priority, requester, due date, estimate, customer-facing, auto-merge/default queue choices, Antigravity framework, and prompt override. Opened its task URL and confirmed proposed state and advanced/custom values.
6. Filed a queued `acme/widgets` task with description, `release/ui-walkthrough` base, high/frontend custom fields, requester/date/estimate/customer-facing values, auto-merge off, Self debug capability, dependency on the proposal, Codex framework, explicit egress, 2 vCPU/1024 MiB/8 GiB resource overrides, and prompt override. It appeared queued and in the new Blocked 1 rail view because its dependency remained open.
7. Opened Blocked, Proposed, Queued, and All tasks views and checked admitted/excluded rows. In All tasks searched “Walkthrough”, sorted Title A–Z, and added Repo = `acme/widgets` on top of the two default conditions. The compound result narrowed from two rows to one and wrote `q`, `sort`, and three `f` clauses to the URL.
8. Hard-reloaded the compound URL and confirmed the search, sort, filters, row count, and result persisted. Opened the row into its task URL, used Browser Back to recover the exact compound URL/result, Browser Forward to reopen detail, and the pane Back control to return. A nonsense search rendered the no-match state; Clear restored rows.
9. Deep-linked to the proposal detail and edited title, description, repo, base, custom priority/requester, and auto-merge. Verified each settled from the real PATCH response. Deferred it indefinitely, reloaded and undeferred it; deferred it to a date, reloaded and observed the wake-date sentence, then undeferred it. Approved it from detail, reloaded the deep link, and rechecked queued state and every edited field.
10. Opened the seeded running task’s Timeline. Focused an attempt and pressed Enter to open the transcript, confirmed seeded transcript content, and closed it. Clicked running-task Cancel, inspected the destructive confirmation, dismissed it, and confirmed the task remained running.
11. Inspected seeded GitHub pull-request links on completed tasks without leaving the app. The demo seeds no interactive task, so no chat surface was available to exercise; the inspected completed task correctly had no chat. The CI page correctly said grain CI is unavailable/no runs in this demo.
12. Selected both walkthrough rows and opened Bulk edit. Enabled and changed Base branch, then cancelled: both rows stayed selected, and opening detail proved the value had not changed. Returned to the list with the 2-row selection intact, bulk-set repo `acme/widgets` and base `release/bulk-41`, and observed the selection clear after both PATCHes succeeded. Reloaded, opened both task URLs, and verified both persisted values.
13. Loaded `/tasks/does-not-exist`. The task request returned 404 and the SPA cleanly normalized back to the populated task list.

## Robustness observations

- Browser capture covered console messages, page errors, failed requests, and HTTP responses throughout the full mutation sequence.
- No application exception or unexpected failed mutation occurred.
- Expected boundary/request noise only:
  - three `GET /api/repos/acme/widgets/github-branches` responses were 404 because the demo’s repository is fake and has no GitHub reader;
  - `GET /api/tasks/does-not-exist` was the deliberate error-state probe;
  - three `GET /api/tasks` requests were aborted by Chromium while Back/Forward or reload replaced the document.
- The local embedded server completed initial data fetches too quickly for a natural loading indicator to become observable; no artificial delay or internal API interception was introduced.
- The demo starts with no configured model backend or Git credential, and its Models settings page exposes that empty state. New task offers a per-task agent framework rather than a per-task model selector in this build. Gemini-backed coverage review was performed externally as noted above.
- GitHub, live agent execution, interactive chat, and grain CI were unavailable demo boundaries. They were not stubbed; only their available UI affordances and unavailable/empty states were inspected.
- No deterministic product bug was found. The targeted Playwright run did expose one stale regression expectation: `paths.spec.js` still expected the older single default filter after the UI added `Kind is none of Interactive`. The test now asserts the complete canonical URL.

## Verification

- Purpose-built walkthrough driver: 20/20 scenarios passed against one fresh demo store (driver and browser artifacts removed afterward).
- Targeted Chromium suite before correction: 20 passed, 1 failed solely on the stale URL expectation.
- `npm --prefix ui run test:e2e -- e2e/paths.spec.js --project=chromium --workers=1`: 9 passed after correction.
