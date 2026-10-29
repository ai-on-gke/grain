# Walkthrough: sidebar rail and Customize sections, session delete and `rename_session`, Mermaid Markdown, per-attempt model, disk overcommit

- **Plan:** `docs/test-plans/2026-10-28.md` §9 (retired `bwsalmon/grain`), restated in task 1722
- **Date:** 2026-10-29, 00:12–01:00 UTC
- **Task:** `grain/049f1e09/task-1722` on `ai-on-gke/grain`, base `main` at `0dc554f`
- **Outcome:** two Mermaid bugs found and fixed, each with a regression test. Everything else in §9 behaved as specified. Sandbox start times and the Merged panel's order could not be checked live, because `grain demo` seeds neither (see §4).

## Environment

- Debian 12 sandbox (`kubernetes-granule` backend), `linux/amd64`, Go 1.26, Node 20.19.0
- `grain` built from this branch (`go build ./cmd/grain`), UI built with `npm run build`
- `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file …`: a throwaway SQLite store with the seeded demo tasks
- Playwright 1.62 with Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, at **1440×900** and **390×844**, light and dark. Driven by ad-hoc Node scripts plus the committed spec `ui/e2e/mermaid-comments-walkthrough.spec.js`
- `rename_session` was called through the real MCP server, `grain mcpserver -sandbox-root … -server http://127.0.0.1:18080 -task <id>` (with `GRAIN_LOCAL_API_TOKEN_FILE` set to the demo's access code), using JSON-RPC `initialize` and then `tools/call` over stdio

## 1. Sidebar rail and Customize sidebar

Steps at 1440×900:

1. Clicked **Collapse sidebar**.
2. Opened **Customize sidebar**. All eight switches were on: Pinned, Tasks, Boards, Sessions, Processes, CI, Repos, Staging.
3. Turned off **Show Boards** and **Show Repos**, then clicked Save.
4. Reloaded the page.
5. Switched the viewport to 390×844 and opened **Open navigation**, then switched back to 1440×900.

Observations:

- **Persistence:** after the reload, `localStorage["grain.sidebar.collapsed"] == "true"` and `localStorage["grain.sidebar.hidden"] == ["boards","repos"]`. The rail shows **Expand sidebar** and has no Boards or Repos item.
- **Phone width:** the phone drawer leaves out Boards and Repos too, and still shows Sessions. Going back to 1440 brings the folded rail back. The page never scrolled sideways.
- **Tab order:** starting from `<body>`, Tab walks the rail top to bottom: Expand sidebar (y=54), Create (96), Customize sidebar (140), then Inbox, Tasks and its states (Proposed … Blocked), Sessions, Processes and CI. Focus then leaves the rail for the task list's search box and toolbar. Focus does not land on anything hidden, and the rail keeps no focus trap.
- **Tasks total:** the rail's Tasks total was **16**. `/api/tasks` returned 19 tasks, 3 of them standing sessions (Director plus two made in §2), so sessions are left out of the total as 98fdaefde intends. The per-state counts add up to 17 because Blocked overlaps the other states.
- The page threw no errors and logged nothing to the console.

## 2. Sessions: `rename_session`, renaming by hand, delete

Steps and results, on two sessions made with `POST /api/console-sessions`:

| # | Action | Result |
|---|---|---|
| 1 | New session from "can you check the release pipeline for me" | Heuristic title, task 19 |
| 2 | Agent: `rename_session {"title":"Release pipeline check"}` over the real MCP server | `isError:false`, "This session is now titled "Release pipeline check" … That name is final" |
| 3 | Agent: `rename_session {"title":"Something else"}` | `isError:true`, "already named "Release pipeline check", by an earlier run of this session … Do not call it again" |
| 4 | Second session; agent rename to "Nightly CI failure" (`PUT /api/tasks/18/session-title`, the route the tool calls) | 200 |
| 5 | Person renames it to "My own name" (`PUT /api/console-sessions/{id}`) | 200, title updated |
| 6 | Agent renames again ("Agent override") | 400, "already named "My own name", **by a person**" |
| 7 | Agent rename on a task that is not a session (task 1) | 400, "task 1 is not a standing chat session" |
| 8 | Agent rename with a 300-character title | 400, "at most 60 characters" |

- A person's title is never overwritten (a525082ad holds), and an agent gets one rename per session.
- The task row's own title keeps the old name until the reconciler's next cycle, as the tool's reply warns ("on this task's row on the next cycle").
- Each fresh `mcpserver` process relays the session's opening message as "A person added this … while you were working". That is expected when the server is started by hand once per call, as here. It is not a product behaviour.
- Deleting from `/sessions` is already covered by CI (`ui/e2e/sessions-list-delete.spec.js`: the confirm dialog, staying on the list, and the bin's position at both widths). It was not repeated by hand.

## 3. Mermaid in task comments

The following comments were posted to a task with `POST /api/tasks/{id}/comments` and read on `/tasks/{id}` at both widths:

- valid flowchart
- malformed flowchart
- hostile flowchart: `<script>` and `<img onerror>` in labels, a `click A "javascript:…"`, `click B call alert()`, and an `%%{init: {"securityLevel":"loose","htmlLabels":true}}%%` directive trying to loosen the config
- sequence diagram with HTML in the message and the note
- 400-edge chain
- 1500-edge chain (55,578 characters)
- before/after pair written as `### Before` / `### After` headings
- before/after pair written as `mermaid before:` / `mermaid after:` fences

Results:

- **Valid diagrams** rendered, and so did both pairs. The comparison shows the two panes side by side at 1440. At 390 it opens on Before and flips to After in place (`useIsPhone`, by design).
- **Malformed diagrams** show the fallback note ("Diagram could not be rendered (Parse error on line 2 …)") with the source under it. Nothing crashed.
- **Hostile input:** no `window.__xss*` flag was set, no dialog opened, and no rendered diagram contains `script`, `img`, `foreignObject`, `on*` handlers or `javascript:` links. This held after hovering over and clicking the labelled nodes. The `init` directive could not loosen anything (`secure` covers `securityLevel` and `htmlLabels`). Labels render as text (`label`, `<img src="x">`) or not at all.
- **Console:** no page errors, and no console errors other than the demo's own `404 /api/repos/acme/widgets/github-branches`. The demo has no GitHub, so the task page's branch picker gets that 404 on every task page whether or not Mermaid is present.
- **Bug 1, a long diagram takes over the thread:** the 400-edge chain rendered **31,655px** tall inline. The canvas had no height limit, so that one comment was 31,753px of the thread at both widths.
- **Bug 2, an oversized diagram hides its source:** the 1500-edge chain went over Mermaid's `maxTextSize` (50,000). Mermaid does not throw in that case. It swaps the source for a stock "Maximum text size in diagram exceeded" graph and resolves, so the reader got that graph instead of the source fallback.

## 4. Per-attempt model, sandbox start times, Merged panel, disk overcommit

- **Per-attempt model:** task 8 has two attempts. The timeline shows `· gemini-3.1-flash` and `· gemini-3.1-pro`, one on each attempt's meta line, at 1440 and 390. The framework is in each one's tooltip ("Ran on … (antigravity)").
- **Sandbox start times:** not checked live. `grain demo` answers `/api/sandboxes` with `{"enabled":false}`, so there are no live sandboxes to show. Covered by `SandboxHealthPage.test.jsx` ("says how long each live sandbox has been running"), which passes.
- **Merged panel order:** not checked live. The demo seeds no merged task, so the rail has no Merged item. Covered by `TaskList.test.jsx` (the merged list, "shows when each task merged, on the active clock"), which passes.
- **Disk overcommit:** on **Settings → Sandboxes**, the "Disk overcommit" field reads 2, the default. Setting it to 1 and then 2 (saved on blur) gave `sandboxDiskOvercommit` 1 and then 2 in `/api/settings`, and the value survived a reload. A real admission needs a disk-bounded host or cluster, which the demo does not have. The admission rule itself is covered by `go test -run 'Disk|Overcommit' ./pkg/model ./pkg/k8s ./pkg/daemon`, which passes. In particular, `TestBudgetHoldsPromisedDiskAgainstTheClusterTimesTheRatio` admits a fourth 40 GiB sandbox on a 100 GiB node at ratio 2 (160 GiB of 180) and refuses a fifth: a task that fits only because of overcommit is admitted.

## Fixes

1. **`ui/src/style.css`:** inline, `.mermaid-block .mermaid-canvas` and `.mermaid-block pre.mermaid-source` are now held to `max-height: min(70vh, 640px)` and scroll inside that box. The full-screen viewer (`DiagramDialog`) is portalled outside `.mermaid-block`, so it is not capped. The 400-edge comment went from 31,721px to 702px at 1440 and 700px at 390.
2. **`ui/src/mermaidRender.js`:** `renderMermaidSvg` now refuses a source over `MAX_DIAGRAM_CHARS` (50,000, Mermaid's own default) before calling Mermaid, with the error "diagram is 55,578 characters, over the 50,000 Mermaid renders". `DiagramCanvas` shows that error in its usual fallback, with the source under it, as it does for a syntax error.

## Regression tests

- `ui/src/mermaidRender.test.js`: "refuses a diagram over Mermaid's text limit rather than rendering its stock error graph".
- `ui/e2e/mermaid-comments-walkthrough.spec.js`: posts the malformed, hostile, long, oversized and comparison comments to a task it files (and closes in `finally`), then at 1440×900 and 390×844 asserts:
  - the malformed fallback and its source are shown
  - no script, image or handler appears in the rendered diagrams, and no flag, dialog or console error appears
  - the long diagram's canvas is exactly `min(70vh, 640px)` tall and scrolls
  - the oversized diagram shows the fallback with the reason, and no SVG text
  - the comparison has two panes on desktop and flips Before/After on a phone
  - the page has no sideways overflow

  **This spec fails on `main`** ("long diagram's canvas height at 1440px") and passes with the fix.

## Screenshots

Taken with Chromium over `grain demo` (files under `/tmp/shots` in the run's sandbox):

- `/` with the rail folded and Customize open (1440), and the phone drawer (390)
- `/tasks/<id>` with all the Mermaid cases (1440 and 390, before the fix)
- `/tasks/<id>` with the long and oversized comments after the fix (1440 and 390, light and dark)
- `/tasks/8` attempts (1440 and 390)
- `/settings/sandboxes` (1440)
