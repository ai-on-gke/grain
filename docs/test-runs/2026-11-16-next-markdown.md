# Walkthrough: `/next/` Mermaid diagrams, prompt panes and capped Inbox questions

Plan: `docs/test-plans/2026-11-16.md` §6 (filed by [Task #6330](/tasks/6330)); run by [Task #6479](/tasks/6479).

## Environment

| | |
|---|---|
| Commit walked | `ad8ebd4fb` (`origin/main`, 2026-11-17), built with `make frontend && go build ./cmd/grain` |
| Rendering | `grain demo -github-sim -fake-ci -reconcile 2s` (throwaway SQLite store) on 127.0.0.1:4231 (base) and :4232 (with the fixes below) |
| Real daemon | `grain daemon -sandbox-port-base=0 -host-sandboxes` over **SQLite**, the fake agent CLI and GitHub sim from `docs/test-runs/2026-11-13-grain-health-exit-codes/` (`run_daemon_walk.sh` here); once on the base binary, once on the fixed one. PostgreSQL was not walked: nothing in §6 touches the store, and the run's time went to the fixes. |
| Browser | Playwright Chromium 151.0.7922.34 (headless shell), Node 20.19.0, Go 1.26.10 |
| Widths / themes | 1440×900 and 390×844, light and dark, plus a live theme switch (`t`) on every diagram page |

Helpers in `docs/test-runs/2026-11-16-next-markdown/`:
- `corpus.json`: the hostile and edge-case Mermaid corpus.
- `mermaid-walk.mjs`: item 1 on `/next/task/<id>` and the classic `/tasks/<id>`.
- `surfaces-walk.mjs`: item 1 on a session and a workstream.
- `run_daemon_walk.sh` with `daemon-actions.json`: parks the question, the access request, the review and the alert on a real daemon.
- `inbox-walk.mjs`: items 2–3 and the deep link.
- `prompt-walk.mjs`: item 4.
- `shots.mjs`: before/after screenshots and `contact-sheet.png`.

Every page listener recorded:
- console messages and page errors;
- dialogs;
- `securitypolicyviolation` events;
- a `window.__pwned` counter that the stubbed `alert`/`confirm`/`prompt` increment.

## Item 1: Mermaid against hostile input

The corpus was posted as seven comments, and the same text was used as a workstream's goal and in the Director session. It covers:
- `click B callback`, `click A call alert(document.domain)`;
- `click B "javascript:alert(1)"` and `click A href "https://example.com" _blank`;
- labels `"<img src=x onerror=alert(1)>"`, `"<script>alert(2)</script>"` and `"<a href='javascript:alert(3)'>x</a>"`;
- `%%{init: {securityLevel: 'loose', htmlLabels: true, flowchart: {htmlLabels: true}, themeCSS: 'body { display:none } @import url(…)', fontFamily: 'x;} body{display:none} {'}}%%`;
- a 401-node chain;
- a syntax error;
- fences nested in an ordered list and in a blockquote.

| Surface | 1440 light / dark | 390 light / dark |
|---|---|---|
| `/next/task/<id>` | 7 SVGs + 1 explained fallback ("Diagram could not be rendered (Parse error on line 2:)"), 0 scripts / `foreignObject` / `image` / `on*` nodes, 0 dialogs, `__pwned` 0, 0 CSP reports, 0 console errors, no horizontal overflow | same |
| Session (`/next/sessions/director`) | 8 blocks, 7 SVGs, 1 fallback, 0 unsafe nodes, 0 dialogs | same |
| Workstream (`/next/workstreams/task:<id>`, goal expanded + conversation) | 16 blocks, 14 SVGs, 2 fallbacks, 0 unsafe nodes | conversation only (the phone layout has no goal toggle): 8 / 7 / 1 |
| Classic `/tasks/<id>` (cross-check) | renders the same comment; 0 dialogs, `__pwned` 0 | same |

Details:
- **No script ran** from any diagram on any surface. All nodes were clicked.
  - Strict mode drops the `call` callbacks.
  - The `javascript:` link survives only as an `<a>` with no `href`.
  - `https://example.com` became `target="_blank" rel="noopener noreferrer"`.
  - Labels draw as text (`<img src="x">`), never as elements.
- `%%{init}%%` could not loosen `securityLevel` or `htmlLabels`.
  - Its `themeCSS` reached the SVG's `<style>`, but scoped under the SVG's id: `body{display:none}` did nothing.
  - Trying `&{position:fixed …}` left the SVG `position: static`, so no full-page overlay is possible.
  - **Bug 1**, below: it *could* pin its own palette and CSS.
- Live theme switch: every diagram redraws in the other palette (node fill `rgb(238,243,253)` ↔ `rgb(31,42,61)`) at both widths.
- Huge diagram:
  - all 7 diagrams were ready in 3.2–3.8 s, and the page stayed responsive;
  - the 401-node top-down chain draws legibly at 168 px × ~31,600 px, which makes the conversation very long. That is a design question (cap a diagram's height and scroll inside it?) rather than a defect. It is not changed here.
- Syntax error: the explained fallback with the source shown below it, and no stray Mermaid error graphic in `<body>`.
  - The classic page briefly held Mermaid's measuring node `dgrain-mermaid-1` mid-render. It was gone once rendering settled, so it is not a bug.
- Nested fences in a list and in a quote render as diagrams.

### Bug 1 (fixed): a diagram's `%%{init}%%` pinned its own palette, fonts and CSS

Reproduction (base `ad8ebd4fb`, `grain demo`):
1. Post this comment:

   ````
   ```mermaid Theme pin
   %%{init: {'theme': 'dark', 'themeVariables': {'primaryColor': '#ff0000', 'mainBkg': '#ff0000'}}}%%
   flowchart LR
     X --> Y
   ```
   ````

2. Open `/next/task/<id>`.

Result: the node's computed fill is `rgb(255, 0, 0)` in light and in dark, and it survives the theme switch. A `themeCSS: 'text{font-size:40px}'` likewise blew the diagram up from 98 px to 614 px tall.

Cause: `mermaidConfigForMode`'s `secure` list locked the security keys but not `theme`, `themeVariables`, `themeCSS`, `darkMode`, `fontFamily` or `suppressErrorRendering`. So untrusted agent output could restyle its diagram out of the palette `ThemeService` chose. The CSS stayed scoped to the diagram, so this is not script execution or a page-wide overlay.

Fix: lock those keys too (`ui-ng/src/app/shared/mermaid.ts`). After the fix, the pinned node's fill is `rgb(238, 243, 253)`, the same as an unpinned diagram, and it follows the theme switch.

Regression coverage:
- `mermaid.spec.ts` "locks security and theme keys against %%{init}%% directives";
- `next-mermaid-diagrams.spec.ts` "a diagram's %%{init}%% cannot pin its own palette or run its click handlers".

The classic `ui/src/mermaidRender.js` has the same `secure` list. It is not changed here: classic is being retired, and the gap is cosmetic.

## Item 2: long questions, access requests and alerts on a real daemon

`run_daemon_walk.sh` files three tasks whose fake runs call:
- `ask_question`: a bold lead, an ordered list with two-level nested bullets, a fenced `sh` block with a continuation line, a table, a Mermaid diagram and 25 more bullets;
- `request_access`: the same shape of reason, for write on `acme/gadgets`;
- two `add_review_comment` findings in GitHub HTML, then `comment_on_issue`.

It also raises a `warning` alert through `POST /api/notifications` with the same long Markdown body.

| Item | 1440 | 390 |
|---|---|---|
| Question | Markdown intact: 1 `ol`, 2 nested lists, 2 code cards with Copy, 1 table, the diagram drawn; capped box 191 px (body 1053 px); "Show full question" opens it to 272 px in place; Reply focuses the composer (`textarea[Your reply goes to the agent and it resumes]`); no page overflow | box 336 px, opens to 446 px; same Markdown, Reply focus |
| Question → Acknowledge | confirm dialog "Acknowledge #2? The task is closed, and reopening puts it back." → task `closed` | — |
| Access request (base) | **Bug 2**: the summary line drew 0 px tall, with a spurious "Show full request" | summary 17 of 40 px |
| Access request (fixed) | summary one full line (20 px), no toggle of its own; reason capped with "Show full reason" | summary two wrapped lines (40 px), nothing clipped |
| Alert (base) | **Bug 3**: only the body's first line, no Markdown | same |
| Alert (fixed) | whole body: list, nested list, code card, table; capped 195 px with "Show full alert" | 338 px, opens to 466 px |
| Alert → Acknowledge | `ackedBy: human:alice@example.com`; the row stays, marked "acknowledged", until cleared (as designed) | — |

No page errors or console errors on any of these, in light or dark.

### Bug 2 (fixed): a long access request squeezed its own summary line to nothing

Reproduction (base, real daemon or `grain demo`):
1. Park a task on `request_access` whose `reason` runs past the dock's cap. In the demo, use `POST /api/e2e/tasks/<id>/pending-access` with `LONG_REASON` from `next-inbox-long-question.spec.js`.
2. Select it in `/next/inbox` at 1440×900.

Result: `[data-testid=access-summary] .capped-md` is 0 px tall (its content is 20 px). A "Show full request" toggle appears for a one-line summary. The summary is invisible.

Cause: both `g-capped-markdown`s in the dock were `flex-shrink: 1`. Once the summary was squeezed, it overflowed and showed its toggle, and the toggle then took all of the summary's remaining room.

Fix: the access summary gets `.dock-summary { flex-shrink: 0 }`, so only the reason gives way (`inbox-page.component.ts`, `inbox-inspector.css`).

### Bug 3 (fixed): an alert showed only the first line of its body

Reproduction (base): raise an alert with a multi-line Markdown body (`POST /api/notifications`), then select it in `/next/inbox`. The dock's "The alert" box holds just the first line.

Cause: the Inbox polls `GET /api/notifications?open=1&fields=list`, which cuts each body to its first line (`listNotificationBody`). The dock rendered that cut body as the whole alert, so PR #1579's capped alert body never had more than one line to show.

Fix: on selecting an alert, the Inbox store reads that alert's family unabridged (`GET /api/notifications?open=1&family=<family>`, an existing route and scope) and renders the matching body in full (`GrainApi.openNotificationsInFamily`, `InboxStore.ensureNotificationBody` / `selectedNotificationBody`).

Regression coverage for bugs 2 and 3: `next-inbox-long-question.spec.js` "a long access request keeps its summary line, and an alert shows its whole body", at 1280, 2269 and 390 px.

## Item 3: review findings relayed as Markdown

The fake run's two findings were:

```
The helper <code>parseCacheKey</code> trims &quot;prefix&quot; twice:
* first in <strong>Load</strong>
* again in <code>Store</code>

Use <code>errors.Is(err, &quot;ErrMiss&quot;)</code> &amp; not <em>string compare</em>; see <code>if err.Error() == &quot;miss&quot;</code>
```

The task's relayed comment ("This run recorded review feedback, but it is not reviewing a pull request … The findings are here instead:") reads:

```
- The helper `parseCacheKey` trims "prefix" twice:
  - first in **Load**
  - again in `Store`
- Use `errors.Is(err, "ErrMiss")` & not *string compare*; see `if err.Error() == "miss"`
```

Every tag and entity is converted: `<code>` → backticks, `<strong>` → `**`, `<em>` → `*`, `&quot;`/`&amp;` → characters, `* ` → nested `- `. The task then parked as "Needs reply", and its Inbox row reads cleanly with no raw markup.

Note: the run's in-sandbox MCP server answered `unknown tool add_review_comment`, because a non-review task is not offered the tool. The orchestrator still relays findings from the run's transcript. That is how the fake agent exercised it; a real agent would not see the tool.

## Item 4: prompt panes

The task's prompt contains:
- an `h2`;
- 33 list items, including a 200-character URL;
- a code card with a 210-character line;
- a 3-column table;
- a diagram.

The processes views use the demo's seeded schedule and template. "Narrow" means the list share is stored at 0.95, which leaves the inspector at its 320 px floor.

| View | 1440 narrow | 1440 default | 390 |
|---|---|---|---|
| `/next/task/<id>` | Prompt above Attributes, no overlap, nothing escapes | same | same (366 px) |
| `/next/tasks?task=<id>&pane=details` | prompt 280 px; attributes stacked to one 250 px column | prompt 918 px, key column 117 px | list-over-pane layout; not measured |
| `/next/agents?task=<id>&pane=details` | **Bug 4 (base)**: inspector 68 px, prompt 30 px, attributes column 0 px; **fixed**: inspector 320 px, prompt 280 px | prompt 753 px | not measured (phone opens the agent full page) |
| `/next/processes/schedules` | prompt 272 px, config stacked to 242 px | 841 px | not measured |
| `/next/processes/templates` | prompt 272 px, config stacked to 242 px | 841 px | not measured |

### Bug 4 (fixed): the Agents inspector ignored its 320 px floor

Reproduction (base):
1. `localStorage.setItem("grain.agents.splitRatio", "0.95")`, which is what dragging the separator to the far right, or a wider window, leaves behind.
2. Open `/next/agents?task=<id>&pane=details` at 1440 px.

Result: the split's columns are `1299.59px 0px 68.41px`, and the Prompt pane is 30 px wide.

Cause: `AgentsPageComponent` never called `sp.attach()`, so its `SplitController` never measured the stage, and `ratioExact()` applied the stored share unclamped. Tasks and the Inbox attach in `afterNextRender`.

Fix: attach in `afterNextRender` and detach on destroy. After the fix the columns are `1048px 0px 320px`, and the prompt pane is 280 px.

Regression coverage: `next-prompt-panes.spec.js` "the Agents inspector keeps its floor under a stored wide list share".

## Inbox deep link (PR #1519)

`/next/task/<id>#comment=<n>` was sampled every 500 ms:
- no highlight at 40 ms, while the page was still loading;
- `rgb(211, 227, 253)` from about 540 ms through 4.07 s;
- none from 4.6 s.

So it shows at once on render and goes after `HIGHLIGHT_MS` (4 s), as specified.

## Verification of the fixes

- Unit: `npx vitest run src/app/agents src/app/inbox src/app/shared`, 201 passed. `npm run lint` is clean.
- e2e: these all passed (2 phone-only skips):
  - `next-mermaid-diagrams.spec.ts`
  - `next-inbox-long-question.spec.js`
  - `next-prompt-panes.spec.js`
  - `next-full-system.spec.js`
- Live: each bug above re-run on the fixed build, on the demo (bugs 1, 4) and on the real daemon (bugs 2, 3), with the results quoted in each section.
- Screenshots: `shots.mjs` took 14 shots per build (Inbox access and alert, task diagram, Agents inspector; 1440/390, light/dark) with a contact sheet for each. They were attached to the task's closing note. This run's file reader shows PNGs only as bytes and has no Gemini key for `LOOK=1`, so the layouts were checked by DOM and bounding-box measurement, not by eye.
