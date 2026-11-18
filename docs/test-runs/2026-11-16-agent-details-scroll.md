# Agent Details scrolling verification

## Reproduction and affected views

In `grain demo`, open `/next/agents`, select a running agent card, then
choose **Details**. Desktop selection opens
`/next/agents?task=<id>&pane=details`. On a phone (Agents uses a 720px
cutoff), selecting the card instead opens
`/next/task/<id>?from=agents`; choosing Details adds `tab=details`.
The card title and inspector's expand control also lead to the shared
full task view.

Give the task a description longer than the viewport (the regression
supplies 45 Markdown paragraphs), and use a 1440×500 or 390×500 window.
Scroll over the Details content to the bottom with a wheel or touch
gesture. Before this fix, the inner tab reaches its bottom while the
last controls are still clipped. Another gesture has to chain into the
outer task page before those controls become visible.

Measured before the fix:

| View | Viewport | Visible bottom | Inner tab bottom |
| --- | --- | --- | --- |
| Agents inspector | 1440×500 | 460px | 545px |
| Task opened from Agents | 390×500 | 408px | 657px |

At 1440×900 and 390×844, ordinary wheel scrolling already worked.
This reproduces a nested-scrolling/clipping defect in **Details**, not a
universal inability to scroll every agent view.

The inspector's enclosing flex/grid height chain was bounded correctly.
The problem was the minimum-height task layout below its fixed header,
combined with both the task page and tab body owning vertical overflow.
No agent-view wheel/touch lock caused the problem; modal inertness remains
limited to the existing dialogs. Task #27's Director drawer fix concerns
its separate conversation layout and is unchanged here. Sessions is also
unchanged.

## Result and regression coverage

When a task page or Tasks/Agents inspector is narrower than 600px or shorter
than 700px, Details now scrolls with its header and tabs in one task-page
viewport. At roomier sizes, the header and tabs stay fixed while the Details
body scrolls. Named size containers measure the actual pane, including
width reduced by a docked Director, rather than only the browser viewport.
The scroll surfaces take keyboard focus and show a visible focus ring.

`next-agent-details-scroll.spec.ts` uses the existing Chromium/Playwright
demo harness, real task metadata, and deterministic long response content.
It checks:

- The actual Agents card selection routes on desktop and touch/mobile.
- Tab access, Page Down, Ctrl+Home/End, wheel scrolling, and native touch input.
- Final dependency control bounding boxes, one vertical scroll owner,
  fixed roomy headers, and no window/horizontal scrolling.
- Expanding the grant builder and a longer prompt arriving on a poll.
- Returning to the top/actions, closing or returning to Agents, and an
  independently scrollable neighbouring Tasks list.

The 1440×500 regression fails against the original CSS because the final
dependency input has zero visible intersection. It passes with the fix.
The existing phone Docs/Details test now checks the task-page scroll owner
and final control visibility, retaining its document reader/modal coverage.

## Local verification

Chromium, in light and dark themes:

| Viewport | Surface selected from Agents | Result |
| --- | --- | --- |
| 1440×900 | Inspector, fixed header/tabs | Pass |
| 1440×500 | Inspector, whole Details page scrolls | Pass |
| 1024×860 | Narrow inspector sheet | Pass |
| 2048×1200 | Inspector, fixed header/tabs | Pass |
| 390×844 | Full task page, native touch enabled | Pass |
| 390×500 | Full task page, native touch enabled | Pass |

ESLint and all 689 frontend unit tests pass. The focused agent/details/docs
run passes all 21 cases. The broader run also passes the five full-system
page/accessibility/contrast checks and the existing Director desktop/touch,
Agents grid, responsive Agents, Sessions, Workstream, and navigation checks.
Light/dark before/after screenshots were rendered and inspected.

Production build succeeds. Its initial bundle is 468.11kB, with the same
450kB budget warning reproduced before the change; this work does not alter
the initial bundle. No dependency, deployment, or test-framework changes
are included.

Local setup required the existing Playwright Chromium browser and its OS
dependencies (`npx playwright install --with-deps chromium`); browser
download used the sandbox's system CA bundle via
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
