# Test Run Report: Console Session Lifecycle Controls, Backing-Task Redirects, and Session `WorkTree` (`docs/test-plans/2026-10-22.md` §3)

## 1. Environment & Commit Tested

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/049f1e09/task-214` (targeting `candidate`)
- **Base Commit Tested**: `374622764732c94fe7b535133d15cefe272a3bbc` (`Merge pull request #4500 from bwsalmon/grain/049f1e09/task-253`)
- **Feature Commits Exercised**:
  - `49cfebd4f`: Hides raw `model.Task` implementation details on `ConsoleSessionPage`, redirects direct `/tasks/{id}` navigation for session-backed tasks to `/session/{id}`, and adds console session lifecycle endpoints (`POST /api/console-sessions/{id}/cancel`, `POST /api/console-sessions/{id}/restart`, `DELETE /api/console-sessions/{id}`).
  - `882d00120`: Renders the `WorkTree` disclosure on `ConsoleSessionPage` when a session has filed follow-up tasks (`filedBy` / `generatedFrom`).
  - `3a81d0003`: Wires `ConsoleSessionPage`'s `onSaved` callback in `App.jsx` (`refreshSessionSaved`) to refresh `sessions`, the open session's `task`, and `tasks` immediately on `Stop`, `Restart`, and capability saves.
- **Platform**: Linux `6.1.0-54-amd64` (Debian 12 Bookworm sandbox VM, x86_64)
- **Toolchains**: Go `go1.26.9 linux/amd64`, Node.js `v20.19.0`, npm `10.8.2`
- **Browser**: Playwright `1.62.1` Chromium at desktop (`1280×800`) and phone (`390×844`) viewports
- **Server Under Test**: Real Go server (`ui/e2e/directorserver`, embedding `pkg/ui`, `pkg/orchestrator`, `pkg/model/sqlite`, `pkg/mcp`, and the built Vite frontend bundle in `pkg/ui/static`) with a controllable scripted agent provider (`scriptedDirector`) that runs real `grain mcpserver` subprocesses over HTTP against an on-disk SQLite store (`-root` / `-addr`).
- **Gemini Key / Visual Inspection Note**: `/home/debian/.gemini-api-key` and `GEMINI_API_KEY` are absent in this sandbox, and `read_file` returns raw PNG bytes rather than rendered images. Before and after screenshot sets (`ui/screenshots/before/contact-sheet.png` and `ui/screenshots/after/contact-sheet.png`) were generated across `/director`, `/session/director`, and `/sessions` at desktop (`1440×900`) and phone (`390×844`) widths in both light and dark themes via `ui/e2e/screenshot.mjs` and attached to the task closing note.

---

## 2. Bugs Uncovered & Fixed

While executing the walkthrough and tracing `App.jsx`, `WorkTree.jsx`, and `paths.js`, three frontend defects were uncovered, reproduced deterministically, and fixed with unit and end-to-end regression tests:

### 2.1 `Restart` (`POST /api/console-sessions/{id}/restart`) Stale Task Race in `refreshSessionSaved` (`ui/src/App.jsx`)

- **Symptom / Root Cause**:
  - Commit `3a81d0003` introduced `refreshSessionSaved`:
    ```js
    const refreshSessionSaved = useCallback(async () => {
      await Promise.all([refreshSessions(), loadSessionTask(), refreshList()]);
    }, [refreshSessions, loadSessionTask, refreshList]);
    ```
  - Because `loadSessionTask()` was invoked in parallel with `refreshSessions()`, `loadSessionTask()` read the _pre-restart_ `openSessionTaskId` closure value (the old, just-closed backing `taskId`) instead of the replacement `taskId` returned by `POST /api/console-sessions/{id}/restart` and `GET /api/console-sessions`.
  - Furthermore, if an earlier poll or `loadSessionTask(oldTaskId)` resolved after `openSessionTaskId` changed, `setSessionTask` overwrote `sessionTask` with the stale task, and `ConsoleSessionPage` briefly rendered the previous session's `sessionTask` when switching between sessions (`/session/director` ↔ `/session/triage`).
- **Fix (`ui/src/App.jsx`)**:
  - Updated `refreshSessionSaved` to await `Promise.all([refreshSessions(), refreshList()])` first, resolve the active session's updated `nextTaskId` from the fresh `sessions` list (`openSessionIdRef.current`), update `openSessionTaskIdRef.current = nextTaskId`, and then `await loadSessionTask(nextTaskId)`.
  - Added a stale-response guard in `loadSessionTask(taskId)` (`if (String(openSessionTaskIdRef.current) !== String(taskId)) return;`) and ensured `UNCHANGED` (`304`) responses re-fetch unconditionally if `sessionTaskRef.current?.id` does not match `taskId`.
  - Guarded `ConsoleSessionPage`'s `task` prop (`sessionTask && String(sessionTask.id) === String(openSessionTaskId) ? sessionTask : null`) so switching sessions never flashes another session's conversation.
- **Regression Coverage**:
  - Unit test in `ui/src/App.test.jsx`: `"loads the replacement session task immediately when Start a fresh chat restarts the session"`.
  - E2E assertions in `ui/e2e/director.spec.js` verifying `< 1500ms` immediate transition to the replacement task on both `carryHandover: true` and `carryHandover: false` restarts (well under the `3000ms` poll interval).

### 2.2 `WorkTree` Not Refreshing When Follow-Up Tasks Are Filed While Session Remains in `awaiting_reply` (`ui/src/components/detail/WorkTree.jsx`)

- **Symptom / Root Cause**:
  - `WorkTree`'s `useEffect` depended only on `[t?.id, t?.state, inTree]`.
  - On `ConsoleSessionPage`, a standing session task starts in `awaiting_reply` and returns to `awaiting_reply` after a turn completes. If `WorkTree` had already fetched `/api/tasks/{id}/tree` while `t.state === "awaiting_reply"` (or when a second child task was filed or a child task changed state while the session stayed in `awaiting_reply`), `t.id` and `t.state` did not change once settled back to `awaiting_reply`, leaving `tree` stale until remount.
  - Additionally, calling `setTree(null)` unconditionally at the top of the effect caused the `WorkTree` disclosure to flash/collapse whenever the effect re-ran for the same root task.
- **Fix (`ui/src/components/detail/WorkTree.jsx`)**:
  - Computed `treeTasksKey` from all tasks in the tree (`id`, `state`, `generatedFrom`, `pullRequest?.number`, `pullRequest?.state`, `pullRequest?.merged`) and included `treeTasksKey` in the `useEffect` dependency array so `/api/tasks/${t.id}/tree` re-fetches whenever child tasks are added or change state.
  - Preserved the existing `tree` state across refreshes of the same `t.id` while clearing it only when `t.id` changes or `inTree` becomes `false`.
- **Regression Coverage**:
  - Unit test in `ui/src/components/detail/WorkTree.test.jsx`: `"re-fetches /api/tasks/:id/tree when follow-up tasks are added or change state while the root task stays awaiting_reply"`.
  - E2E assertions in `ui/e2e/director.spec.js` Step 2.

### 2.3 Direct `/director` Path Alias Canonicalization (`ui/src/paths.js`, `ui/src/App.jsx`)

- **Symptom / Root Cause**:
  - The test plan and operator workflows reference `/director` as the shorthand entry route for the Director session (alongside `/session/director`), but `parsePath("/director")` fell through to `{ view: "tasks" }` (`/`).
- **Fix (`ui/src/paths.js`, `ui/src/App.jsx`)**:
  - Mapped `/director` in `parsePath` to `{ view: "session", sessionId: "director" }` and added `/director` to the legacy-redirect `replaceState` check in `App.jsx` so navigating to `/director` opens the Director session and canonicalizes the address bar to `/session/director`.
- **Regression Coverage**:
  - Unit test in `ui/src/paths.test.js` (`parsePath("/director")`).
  - E2E assertion in `ui/e2e/director.spec.js` Step 1 (`page.goto("/director")` -> `/session/director`).

---

## 3. Walkthrough Execution & Step-by-Step Verification

All five walkthrough procedures from `docs/test-plans/2026-10-22.md` §3 were executed end-to-end against the real Go server and SQLite store in Playwright Chromium across desktop (`1280×800`) and phone (`390×844`) viewports:

| Step  | Walkthrough Area                                                                                                                              | Viewports Tested      | Outcome  |
| :---- | :-------------------------------------------------------------------------------------------------------------------------------------------- | :-------------------- | :------- |
| **1** | Backing-task route redirect (`/tasks/<id>` -> `/session/<id>`) & hidden task pill                                                             | `1280×800`, `390×844` | **PASS** |
| **2** | Session `WorkTree` disclosure rendering, expansion, and child task navigation                                                                 | `1280×800`, `390×844` | **PASS** |
| **3** | `Stop` (`POST /api/console-sessions/triage/cancel`) immediate UI refresh (`3a81d0003`)                                                        | `1280×800`, `390×844` | **PASS** |
| **4** | `Restart` (`POST /api/console-sessions/triage/restart`) with `carryHandover: true` vs. `carryHandover: false`                                 | `1280×800`            | **PASS** |
| **5** | `Delete` (`DELETE /api/console-sessions/director`) without `ConsoleSessionsOr` resurrection across reload & server restart + fresh recreation | `1280×800`            | **PASS** |

### Step 1: Backing-Task Route Redirect & Hidden Task Pill (`49cfebd4f`)

1. Opened `/director` in Playwright at `1280×800`; verified the browser canonicalized the URL to `/session/director`, rendered the `Director` session in `awaiting_reply` (`"waiting for your reply"`), and rendered neither `.console-session-header .count` nor any `"Open the task behind this session"` link.
2. Created a second standing console session `Triage` (`id: "triage"`) via the UI (`Create options` -> `New session` -> `"Triage"` -> `Create`); confirmed navigation to `/session/triage` and read both sessions' backing `taskId`s from `GET /api/console-sessions`.
3. At both desktop (`1280×800`) and phone (`390×844`) viewports, navigated directly to `/tasks/<directorTaskId>` and `/tasks/<triageTaskId>`:
   - Verified `/tasks/<directorTaskId>` immediately redirected to `/session/director` without opening the raw task detail overlay (`.detail-state` count `0`).
   - Verified `/tasks/<triageTaskId>` immediately redirected to `/session/triage` without opening the raw task detail overlay (`.detail-state` count `0`).
   - Verified no raw backing task ID pill (`.console-session-header .count`) or task link was rendered in the session header at either viewport.

### Step 2: Session `WorkTree` Disclosure (`882d00120`)

1. Sent `"File two follow-up tasks and record the standing handover summary."` in `/session/triage`.
2. The `triage` run invoked `propose_task` twice via `grain mcpserver`, filing two child tasks (`"Audit session redirect handling"` and `"Verify session work tree links"`) with `FiledBy` / `generatedFrom` set to `triage`'s backing `taskId`, recorded a standing handover via `write_handover`, and completed the turn.
3. Verified on `/session/triage` at both `1280×800` and `390×844` that `ConsoleSessionPage` rendered the `WorkTree` region (`role="region" aria-label="Work tree"`) showing `Provenance · Work from task <triageTaskId>: 0 of 2 done`.
4. Expanded the `WorkTree` disclosure (`.provenance-toggle`), verified the root node (`li[data-task-id="<triageTaskId>"]`) and both child task rows (`li[data-task-id="<child1Id>"]`, `li[data-task-id="<child2Id>"]`) were visible at both `390×844` and `1280×800`, and clicked the first child task link to confirm navigation to `/tasks/<child1Id>` with the child task's detail heading visible.

### Step 3: `Stop` (`cancel`) Immediate UI Refresh (`49cfebd4f`, `3a81d0003`)

1. Returned to `/session/triage` and sent `"Hold this turn in running until stopped."`, holding the turn in `running`.
2. Verified the typing indicator (`.cs-typing-indicator`) and the composer `Stop` button (`role="button" name="Stop"`) were visible at both `1280×800` and `390×844`.
3. Clicked `Stop` (`POST /api/console-sessions/triage/cancel`) and measured the elapsed time from the HTTP response resolving to the DOM settling:
   - Verified the `Stop` button, `.cs-typing-indicator`, and `.grain-mark-interactive` disappeared immediately (`< 1500ms`, without waiting for the `3000ms` background poll tick).
   - Verified `"waiting for your reply"` and the `"Cancelled"` separator (`.cs-message-separator`) appeared immediately and `GET /api/tasks/<triageTaskId>` reported `state: "awaiting_reply"`.

### Step 4: `Restart` with `carryHandover: true` vs. `carryHandover: false` (`49cfebd4f`, `3a81d0003`)

1. Confirmed `triage`'s initial backing task (`triageTaskId`) held the standing handover comment (`handover: true`, body `"Triage handover summary: audited redirect handling and queued two follow-up tasks."`).
2. **Restart keeping handover (`carryHandover: true`)**:
   - Clicked `Start a fresh chat` -> `Start fresh with summary` (`POST /api/console-sessions/triage/restart` with `{"carryHandover":true}`).
   - Verified `/session/triage` switched to the replacement backing task (`triageTask2Id !== triageTaskId`) immediately (`< 1500ms`), rendering the carried handover summary while clearing the old turn comments.
   - Verified `triageTaskId` was `closed` and **not** tagged `console-session-reset`, while `triageTask2Id` was in `awaiting_reply` with `continues === triageTaskId` and a carried `handover: true` comment.
3. **Restart starting fresh (`carryHandover: false`)**:
   - Clicked `Start a fresh chat` -> `Close this chat and start a fresh one` (`POST /api/console-sessions/triage/restart` with `{"carryHandover":false}`).
   - Verified `/session/triage` switched to a brand-new backing task (`triageTask3Id !== triageTask2Id`) immediately (`< 1500ms`) with the carried handover summary absent from the DOM.
   - Verified `triageTask2Id` was `closed` and tagged `console-session-reset`, and `triageTask3Id` started in `awaiting_reply` with `continues === undefined` and zero handover comments.

### Step 5: Delete `director` Without Resurrection & Fresh Recreation (`49cfebd4f`)

1. Deleted `triage` via `Delete session` (`DELETE /api/console-sessions/triage`), then navigated to `/session/director`, recorded a pre-delete standing handover summary (`"Pre-delete director handover summary that must not survive deletion."`), and deleted `director` via `Delete session` (`DELETE /api/console-sessions/director`).
2. Verified `GET /api/console-sessions` returned `sessions: []` (0 sessions) and `directorTaskId` was `closed` and tagged `console-session-reset`.
3. Reloaded the browser, restarted the `directorserver` process over the same SQLite database directory (`restartDirectorServer`), and reloaded the browser again:
   - Verified `GET /api/console-sessions` still returned `sessions: []` (confirming `[]model.ConsoleSession{}` was preserved in SQLite and `ConsoleSessionsOr` did **not** resurrect `director`).
   - Verified the UI rendered the `Sessions` empty state (`"No interactive sessions yet."`).
4. Recreated `Director` via `+ New session` -> `"Director"` -> `Create`:
   - Verified navigation to `/session/director` with a brand-new backing task (`recreatedDirectorTaskId !== directorTaskId`).
   - Verified the recreated `director` task started in `awaiting_reply` with `continues === undefined`, zero handover comments, and none of the pre-delete conversation or handover summary.

---

## 4. Automated Test Execution Summary

- **Frontend Unit Tests (`vitest`)**:
  - `npm --prefix ui test -- --run src/paths.test.js src/components/detail/WorkTree.test.jsx src/App.test.jsx` -> **3 passed (231 tests)**
- **Playwright End-to-End Tests (`chromium`)**:
  - `npx playwright test e2e/director.spec.js --project=chromium` -> **2 passed (54.8s)**
  - `npx playwright test e2e/paths.spec.js --project=chromium` -> **16 passed (52.8s)**
