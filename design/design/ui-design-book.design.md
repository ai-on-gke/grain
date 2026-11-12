# The grain UI design book

> **Status.** Living document. It describes how the Angular UI (`ui-ng/`,
> served under `/next/`) looks, behaves, is tested and is reviewed. Read it
> before you touch anything under `ui-ng/`, and update it in the same pull
> request when a change sets a new rule. Where it and the code disagree, the
> code is wrong or this book is stale; fix one of them, never leave both.
>
> Companions: [`angular-ui.design.md`](angular-ui.design.md) (why a second
> UI, how it is served and migrated), [`docs/brand.md`](../../docs/brand.md)
> (the mark and its geometry), [`docs/ui-screenshots.md`](../../docs/ui-screenshots.md)
> (rendering pages for review).

## 1. What grain's UI is for

grain runs autonomous coding agents. Most of what it does should never need
a person: tasks are filed, run, verified and landed on their own. The UI
exists for the exceptions and for trust.

1. **Exception-first.** The first screen answers "what is waiting on me?"
   (the Inbox), not "what is happening?". Routine progress is visible but
   never demands attention.
2. **One work surface.** Inbox, Tasks, Agents and Workstreams show the same
   tasks through different lenses. They share one list|detail layout, one
   task detail, one conversation and one set of actions, so a person learns
   the surface once.
3. **Honest.** Every number, state and label comes from the server. A value
   that could not be read is shown as absent ("not measured"), never
   estimated into a number. Estimates say so (`est.`). Sample or demo data is
   labelled at the point of display.
4. **Safe by construction.** A click acts on what the person was looking at
   when they clicked, not on whatever a poll put there since. Destructive or
   wide actions confirm with their scope. Anything that can be undone offers
   Undo.
5. **Calm, then delightful.** The base is quiet: white and near-white
   surfaces, hairlines, one accent. Delight comes from motion and finish
   (sections 6 and 7), and it never costs legibility or speed.

## 2. Brand

### The mark

The grain mark is the Chladni rosette described in `docs/brand.md`. In
`ui-ng` it is drawn from the same path (`shared/brand-mark.ts`) and tinted
with the theme's primary colour, so it reads as part of the interface rather
than a sticker:

- Top of the rail: a 44×44 button linking to the Inbox, above the New task
  button.
- Favicons: `public/grain-favicon-light.svg` (`#0b57d0`) and
  `public/grain-favicon-dark.svg` (`#a8c7fa`), swapped with the theme.
- When agents are running and the person has not asked for reduced motion,
  the mark may animate; otherwise it is still.

The classic UI (`ui/`) uses the bronze and wheat palette in `docs/brand.md`.
`ui-ng` uses the Material 3 palette below, with the mark in primary blue.
Do not mix the two palettes in one surface.

### Colour

All colour comes from tokens in `ui-ng/src/styles.css`. Never write a hex
value in a component stylesheet; if a token is missing, add it to
`styles.css` for both themes.

| Role | Light | Dark | Use |
|---|---|---|---|
| `primary` | `#0b57d0` | `#a8c7fa` | the one accent: primary buttons, the New task button, links, selection, the mark |
| `secondary-container` | `#c2e7ff` | `#004a77` | selected chips, active nav item, pressed segments |
| `tertiary` | `#146c2e` | `#6dd58c` | healthy, done, live dot |
| `warning` / `-container` | `#8a5300` / `#fef0c3` | `#ffdf99` / `#4a3400` | needs a person, approval intake, quiet agents |
| `error` / `-container` | `#b3261e` / `#f9dedc` | `#f2b8b5` / `#8c1d18` | failed, denied, destructive |
| `surface` | `#ffffff` | `#181a20` | the page |
| `surface-container-lowest` | `#ffffff` | `#13151a` | recessed wells: code, diffs, transcripts |
| `surface-container-low` | `#f7f9fc` | `#1e2128` | rail, top bar, cards |
| `surface-container` | `#eff3f8` | `#242830` | a person's message, menus, popovers |
| `surface-container-high` | `#e6ecf5` | `#2c303a` | dialogs, hover |
| `outline` / `outline-variant` | `#727885` / `#d8e0eb` | `#8b91a0` / `#383d4a` | borders, hairlines |

Rules that came from real defects:

- **Light mode is white, not grey.** Sheets, dialogs, cards and code wells
  stay `#ffffff` with a hairline; grey slabs made the UI look muddy.
- **Dark mode is not black.** The page is `#181a20`. Pure black (`#0e0e0e`)
  killed elevation: cards looked like holes.
- **Status is never colour alone.** Every status pill carries a 12×12 shape
  (`shared/status-icon.component.ts`) so it reads under colour-vision
  deficiency. Actors are shape-coded too: circle for a person, square for an
  agent, rounded square with the mark for grain itself.
- **Dim with a colour, not opacity.** Fading text with `opacity: .35` drops
  contrast to about 2:1. To de-emphasise, switch to the `outline` or
  `on-surface-variant` token, which keep at least 4.5:1. Never dim anything
  that needs a person.

### Type

```
--grain-font-display: "Google Sans", "Google Sans Text", Roboto, system-ui, ...
--grain-font-body:    "Google Sans Text", "Google Sans", Roboto, system-ui, ...
--grain-font-mono:    "Google Sans Mono", "Roboto Mono", ui-monospace, ...
```

The daemon's content security policy does not allow external font hosts, so
these stacks fall back to locally installed fonts. Never add a `<link>` to a
font CDN.

- Page titles 22/28 display, weight 600. Section titles 14/20, weight 600.
  Body 14/20. Meta and captions 11–12/16.
- Sentence case everywhere. No forced lowercase, no all-caps except the
  small group labels in the Settings drawer.
- Ids, branches, paths, models and counts use the mono stack or
  `.tabular` (tabular figures) so columns line up.

### Shape and elevation

Corners: 4 (xs), 8 (small), 12 (medium), 16 (large), 28 (extra-large, the
full-screen conversation), full (pills and chips). Elevation is used sparingly:
the New task button (blue-tinted shadow), menus, dialogs and the full-screen
conversation. Cards are flat with a hairline.

### Iconography

24px outline icons drawn for this app (the `ICONS` map in
`app.component.ts`), filled with `currentColor`. Each icon depicts the
concept, not a generic person: Agents is a robot head, Workstreams a small
tree, Sessions a speech bubble. Add new icons to the map in the same style;
do not pull in an icon font.

## 3. Layout

### The shell

```
┌──────┬──────────────────────────────────────────────────────────────┐
│ mark │ Grain        [Next up #54 … ▾] [○ 0 agents running] [/] ? ◐ AE│  56px top bar
│ [ + ]│──────────────────────────────────────────────────────────────│
│ New ▾│                                                              │
│ Inbox│                     destination                              │
│ Work…│                                                              │
│ Tasks│                                                              │
│ Agen…│                                                              │
│  …   │                                                              │
│ ⚙    │ grain v… · 0 running · Updated 3s ago             Shortcuts  │  40px bottom bar
└──────┴──────────────────────────────────────────────────────────────┘
  72px rail
```

- **Rail order:** Inbox, Workstreams, Tasks, Agents, Sessions, Processes, CI,
  Telemetry, Context, Fleet; Settings pinned at the bottom. People can hide
  destinations (Customize navigation); Inbox cannot be hidden.
- **New task button:** filled primary, 60×60 (52 on short screens), with a
  "New ▾" menu below it for the other things you can start. It is the one
  loud control on the page.
- **Top bar:** "Next up" is always present, even when the queue is empty, in
  which case it says so. "N agents running" is always present and links to
  Agents; it pulses only when the number is above zero.
- **Phone (< 600px):** the rail becomes a bottom bar with Inbox, Tasks,
  Sessions and More, plus a floating New task button.

### Destinations fill the stage

Every routed page fills the area between the bars, and scrolls inside it,
never the window. The page host element must take part in the layout:

```css
g-my-page { display: contents; }   /* or: display: block; height: 100%; */
```

Forgetting this leaves the host as an inline box. The page then sizes to
its content, nothing scrolls, and the side drawer stops halfway down. Settings
shipped with exactly this bug.

Content uses the width it is given. Do not cap a page at a fixed max-width
(`max-width: 1560px` left half of a large screen empty on Workstreams). Cap
only forms and reading measures that need it (Settings sections at 1180px).

### List | detail

Inbox, Tasks and Agents share one split (`shared/split-controller.ts`,
`inbox/inbox-queue.css`, `inbox/inbox-inspector.css`):

- Default 30/70, the list clamped to `336px–480px`; presets 30/70 and 42/58;
  a draggable separator (`role="separator"`, 24px hit zone, keyboard
  arrows, Enter collapses, double-click resets) and a "Hide list" toggle.
  The choice is remembered per surface.
- The detail pane is always there on desktop; it is not a modal.
- **Empty is a state of its own.** When a surface has nothing to list and
  nothing is selected (no agents running), drop the split and show one
  centred empty state: what this place is for, and one or two ways onward.
  An empty list beside an empty inspector looks broken.

### Workstream detail

Three columns: tree (≈21%), graph (flexible, minimum 280px), and task detail
(`clamp(440px, 40%, 580px)`). The detail column gets room first; the graph
takes what is left. Below 1280px it becomes two panes.

### The conversation

One component, `shared/conversation/` (`g-conversation`), is the thread and
composer everywhere a person talks to an agent about a task: the Inbox
inspector and Task Detail. Sessions shares its message styles.

- **Inline:** the thread fills the pane below the cards above it, between a
  floor of half the viewport and a ceiling of the viewport minus the
  chrome. An empty thread is one quiet line, not a blank box.
- **Expand:** the sibling cards step aside and the thread takes the pane.
- **Full screen:** a native modal `<dialog>` (top layer, page inert), 860px
  wide, composer pinned, Escape closes, the draft survives. Shift+F toggles
  it unless you are typing.
- **Messages read like a document.** Full width. Agent messages are flat; a
  person's message sits on `surface-container` with a hairline. No saturated
  chat bubbles.
- The composer is `box-sizing: border-box; width: 100%`; attach, hint and
  Send share one row. A "New messages ↓" pill appears when something arrives
  while you are scrolled up.

### Breakpoints

`< 600px` phone, `600–839px` compact, `840–1279px` medium, `≥ 1280px`
expanded. Test at 390×844, 1024×860, 1440×900 and 2048×1200 at least.

## 4. Components

Use these before writing new markup. If one is close but not right, extend
it rather than copy it.

| Component | File | Use |
|---|---|---|
| Pill select | `shared/pill-select.component.ts` | every single-choice dropdown (no native `<select>`) |
| Priority chip | `shared/priority-chip.component.ts` | named priority levels with grain's number |
| Actor chip | `shared/actor-chip.component.ts` | who did something, shape-coded, "on behalf of" |
| Status icon | `shared/status-icon.component.ts` | the shape inside every status pill |
| Conversation | `shared/conversation/` | thread + composer (section 3) |
| Task detail | `task/task-detail.component.ts` | the one task view, full page and in panes |
| Split controller | `shared/split-controller.ts` | list|detail sizing and persistence |
| Confirm dialog | `shared/confirm-dialog.component.ts` | wide or destructive actions, with scope |
| Markdown | `shared/code-highlight.ts` (`[gMarkdown]`) | agent output: sanitised, code copy, links |
| Diff viewer | `shared/diff-viewer.component.ts` | line-numbered, word-level, syntax coloured |
| Tooltip | `shared/tip.directive.ts` (`gTip`) | every icon button and every non-obvious control |

Rules:

- **Class names are global.** Most pages use `ViewEncapsulation.None`, so a
  class defined in one page's stylesheet applies wherever that stylesheet is
  loaded. Prefix page-specific classes, and search for a name before you use
  it. Example: Task Detail's `.gate-card` restyled the Backlog's approval
  card until it was renamed `.intake-card`.
- **One control height per toolbar.** Chips, pill selects and segmented
  buttons in one row share a height (36px in toolbars). Reset inherited
  margins (`.filter-strip` carries a top margin for page headers).
- **Every control has a name.** Icon buttons get `aria-label` and a `gTip`.

## 5. Interaction

- **Keyboard first.** `c` new task, `/` filter, `?` help, `1`–`9`
  destinations, `j`/`k` step, `x` select, `Shift+F` full-screen conversation.
  Every shortcut is listed in Help. Shortcuts never fire while typing
  (`isTypingTarget`).
- **URLs.** Every view, tab and selection that a person might share has a
  URL (`?tab=`, `?task=`, `?pane=`). Back works.
- **Role-aware.** An action the person cannot take is hidden, or shown
  disabled with the reason. It is never shown enabled only to fail.
- **Safety.**
  - Selection is pinned: if the selected item leaves the list, say so; do
    not jump to another item.
  - The composer is locked to its target task.
  - Batch actions name their scope (repos, owners) in the confirmation.
  - Undo for reversible actions.
- **Live, quietly.** Server-sent events update lists in place. Never steal
  focus or scroll position on an update; hold an update while the person is
  interacting with the row.

## 6. Motion

- Durations 120–200ms; standard easing `cubic-bezier(0.2, 0, 0, 1)`.
- Motion explains a change: things appear from where they came from, a pill
  pops when it arrives, the live dot pulses only while something runs.
- `prefers-reduced-motion: reduce` turns all of it off. This is required,
  not optional.
- No motion on page load. No looping animation on anything idle.

## 7. Making it feel great

A screen is finished when it is correct, then readable, then delightful. In
that order.

**The craft list** (each of these was missed once):

- Alignment: controls in a row share a baseline and a height. Text in a
  column lines up.
- Space: content uses the space it has. No half-empty pages, no peephole
  panes, no fixed caps on large screens. On the other hand, nothing touches
  an edge: 16–24px page padding.
- Hierarchy: one loud thing per screen (usually the primary action).
  Everything else steps down.
- Highlights mean something: a warning-container band means a person is
  needed. Do not use the colour for decoration, and do not leave a
  needs-a-person signal plain.
- Empty, loading and error states are designed, not left as a blank or a
  single grey line.
- Code, diffs and transcripts sit in recessed wells with syntax colour and
  line numbers.
- Long text truncates with an ellipsis and a tooltip, never wraps a table
  row to six lines.

**Sparks** (sparingly, never at the cost of speed):

- The live dot and the running mark.
- A restrained Aurora gradient behind the Inbox resolution dock and the
  Agents summary, reacting to state (idle, processing, responding). Its
  stops are `primary`→`secondary`, never pale tints, which measured 1.4:1
  against the page and vanished.
- Pills that pop in, tooltips that fade in, sparklines on agent cards, a
  three-pulse ring on running workstream nodes.

**Anti-patterns:** gradients on buttons; shadows on everything; grey on grey;
opacity for de-emphasis; more than one accent colour; icons that are all the
same person; chat bubbles for long agent reports.

## 8. Accessibility

WCAG 2.1 AA is the floor.

- axe reports no serious or critical violation on any page, in either theme,
  at desktop or phone size (`ui/e2e/next-full-system.spec.js`).
- Text contrast is at least 4.5:1 (3:1 for large text and UI shapes).
- Focus is always visible (2px primary outline).
- Hit targets are at least 24×24.
- Landmarks and headings are in order (one `h1` per page).
- Live regions announce counts that change.
- Dialogs trap focus and restore it on close.

## 9. Performance budgets

Enforced by `ui-ng/angular.json`; a build with warnings is a failed build.

- Initial bundle under 450 kB. Pages are lazy routes. Shell-only code stays
  small; use `@defer` for overlays.
- Each component stylesheet under 8 kB. Do not re-import shared CSS in a
  parent that a child component already loads; it duplicates the bytes.
- No new runtime dependency without a reason written in the pull request.

## 10. How to test a UI change

A UI change is not done until it has been rendered, measured and looked at.
Passing unit tests is not the same as the page looking right.

1. **Unit tests (vitest, `ui-ng/`):** `npm test`. Every test drives the
   component (clicks, keys, inputs) and asserts on real effects: the request
   sent, the DOM shown, the signal changed. Never push into a fake's call
   log, inject DOM text, or set `disabled` by hand to make a test pass. jsdom
   applies no component CSS, so layout is not tested here.
2. **Lint and build:** `npm run lint`, `npm run build` (0 warnings, budgets
   above).
3. **Real browser over `grain demo` (Playwright, `ui/e2e/`):**
   - `next-full-system.spec.js` visits every `/next/` destination in light and
     dark, at 1440×900 and 390×844. It asserts no sideways scroll, no page
     error and no serious or critical axe violation, plus contrast on the
     primary destinations. Set `GRAIN_UI_SHOTS=<dir>` to write one PNG per
     page, theme and size.
   - Scenario specs cover the interactions people rely on: selecting and
     resizing the split, changing priority with Undo, reordering the backlog,
     replying to and waking an Inbox item, expanding and full-screening a
     conversation, editing a task in place, filing a new task.
   - Where a layout rule matters (a badge centred on its label, a composer
     inside its card), assert it with bounding boxes.
4. **Screenshots:** `make ui-screenshots PAGES=... OUT=screenshots/after`
   (`docs/ui-screenshots.md`). For a fix, take `screenshots/before` first.
   **Open and look at every image.** Check:
   - the light and dark images actually differ;
   - nothing overflows its card or the viewport;
   - controls align;
   - empty states are designed;
   - the page uses the space at 2048 wide.
5. **Live drive:** for larger changes, drive the real flows for three minutes
   against a running daemon with screenshots every 30 seconds, and check for
   zero console errors.

## 11. The review mandate

Every pull request that changes `ui-ng/` is reviewed by three independent
reviewers before it merges. Each may be an agent. Each works from the diff,
this book, the screenshots and a running instance, and never from the
author's reasoning or summary. The author does not review their own work.

| Reviewer | Looks for | Evidence it must produce |
|---|---|---|
| **Design** | brand and token use, hierarchy, alignment, spacing, use of space at all four sizes, empty/loading/error states, motion and reduced motion, consistency with this book, the "feel great" list | annotated screenshots (light and dark, phone and desktop), each finding tied to a rule in this book |
| **Functional** | every changed flow works end to end against `grain demo`: the right API call with the right arguments, role gating, Undo, URLs and Back, keyboard paths, live updates, no regressions in neighbouring pages | the steps it ran, the requests it saw, and a failing/passing Playwright scenario for each finding |
| **Adversarial** | ways the change is wrong: race conditions, stale selection, wrong target, unsafe HTML, unsanitised input, leaks of internal data, fake or weakened tests, budget creep, dead code, global class collisions | a concrete reproduction for each finding |

Rules:

- Findings are graded P0 (broken, unsafe, or wrong data), P1 (a visible
  defect or a rule in this book broken), P2 (polish) and P3 (suggestion). P0
  and P1 are fixed before merge. P2 is fixed or filed. Nothing is waived
  silently.
- A reviewer that cannot render the page or reach the instance says so and
  stops. It does not pass the change.
- The author answers each finding with the fix (commit and evidence) or a
  reason. "Done" means re-verified by the reviewer, not asserted by the
  author.
- A change touching more than one destination, the shell, or shared
  components gets a final live drive (section 10, step 5) after the fixes.

### Definition of done

- [ ] Unit tests added or updated; all pass; no test deleted, skipped or
      weakened.
- [ ] Lint clean; build with 0 warnings and within budgets.
- [ ] `next-full-system.spec.js` and the relevant scenario specs pass.
- [ ] Screenshots before and after, light and dark, phone and desktop,
      looked at.
- [ ] Design, Functional and Adversarial reviews done; P0/P1 fixed and
      re-verified.
- [ ] This book updated if the change sets or changes a rule.
- [ ] No internal hostnames, people's names or credentials in code, fixtures,
      screenshots or commit messages.
