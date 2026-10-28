# Responsive navigation and live-session walkthrough — 2026-09-14

## Build and environment

- Tested code commit: `681becbea` (`grain/task-43`), based on `4bb9453c8d3e0b35a19361fed9d652b5e0b01287`.
- Debian 12 sandbox; Node 20.19.0; Playwright 1.62.1; Chromium 151.0.7922.34 and WebKit 26.5.
- Built the production frontend with `make frontend`, built `cmd/grain`, and ran the real `grain demo` server at loopback over its throwaway embedded SQLite store.
- Browser captures were temporary under `/tmp`. Gemini 2.5 Flash reviewed desktop, tablet, phone portrait/landscape, and WebKit captures; no capture or browser artifact is committed.

## Walkthrough and results

1. At 1440×900 desktop, 820×1180 iPad/coarse-pointer tablet, 390×844 phone, and 844×390 rotated phone, used the visible rail or drawer controls to open Inbox, All tasks, the Boards index, Kanban Board, Timeline, Report, Repos, Schedules, Templates, Suites, Sandboxes, and CI. Opened Settings, System, and Metrics from the same controls at desktop, tablet, and phone sizes. Every route had the expected selected state after the fixes, and counts matched the demo rows.
2. Repeated the route matrix in Linux WebKit at tablet and phone sizes. Live viewport changes phone portrait → phone landscape → tablet portrait → phone portrait switched the shell at the intended breakpoints without page overflow.
3. Verified the two setup banners stack in document flow above the phone bar/rail and page. Checked overlay edges, full-width phone drawers, Back/close controls, independently scrolling page/rail/dialog/chat regions, sticky form submission, the horizontally scrollable Settings tabs, and the rail footer after scrolling. No document-level horizontal overflow or accidental background scrolling was observed.
4. Used keyboard-only Enter/Escape on New task and confirmed focus restoration; used keyboard navigation through the Settings tabs; dismissed the phone drawer with Escape and confirmed focus returned to Open navigation. Used mouse, Playwright touch/tap with an iPad-style coarse pointer, and both browser engines.
5. Rendered light, dark, auto-dark, and reduced-motion paths. Gemini suggested apparent phone banner/sidebar overlap in static captures; browser geometry disproved it in both engines: the second banner ended at y=161.75, the phone bar began at y=161.75, both actions ended at x=382 inside a 390px viewport, no `aside` existed in phone mode, and document overflow was zero.
6. Created task 11 through the visible New task form with a deliberately long title, long Markdown, a table, and a wide code line; selected Interactive session and submitted it to the real store. Opened the resulting task/chat, sent a multiline Markdown message through the composer, and verified bold/table rendering plus persistence after hard reload. Rechecked the long detail and chat at desktop, tablet, and phone widths.
7. Exercised the overlay Back control and browser Back/Forward separately. Direct task links and standing-session links survived hard reload. A running task's native Cancel confirmation was dismissed once and accepted once; the Closed result survived reload and the rail count updated.
8. Exercised the standing Director empty state, two configured standing chats, session switching, incremental agent text, running/queued status rendering, one failed poll, recovery with final Markdown, and a session-manager error. The pane remained usable across the simulated disconnect.
9. Watched browser console errors, failed requests, and HTTP error responses throughout the unstubbed matrix. None occurred.

## Defects fixed

- A rail click opened the Director at `/session`, but path parsing did not recognize sessions; hard reload returned to `/`, and browser history could not restore a selected session. Standing chats now use `/session/:id` while the old `/session` path still opens the first session.
- The Boards index heading had neither visual selected treatment nor `aria-current`, leaving every rail destination apparently unselected on `/boards`.
- In a crowded/bannered rail, flexbox collapsed the build stamp to a computed height of 0px even after the rail was scrolled to its end. The stamp now has a non-shrinking flex basis and remains fully visible.
- The existing narrowed-list browser expectation predated the default `kind:none:interactive` filter; it now asserts the complete durable query.

Regression coverage was added in `ui/src/paths.test.js`, `ui/src/components/Sidebar.test.jsx`, `ui/e2e/paths.spec.js`, and `ui/e2e/shell.spec.js`.

## Stubs and limits

The demo has no running session manager or agent stream. Three browser interceptions covered only those unavailable live boundaries:

- `GET /api/console-sessions` passed through to the real server, then associated Director and a second Operations session with the real interactive task.
- `GET /api/tasks/11` passed through to the real store, then overlaid incremental/final agent comments and status; one poll was aborted to simulate disconnect and the next recovered.
- One `GET /api/console-sessions` returned HTTP 503 to exercise the visible manager-error state.

All navigation, React rendering, layout, user gestures, task creation, human chat sends, task transitions, reloads, and persistence used the real built frontend and Grain server. The current product no longer ships the former PTY console UI, so terminal resize and terminal-specific reconnect/exit/close controls were not present; the supported task-bound interactive surface is the chat exercised above. Linux WebKit is not a substitute for a physical iPad Safari check.
