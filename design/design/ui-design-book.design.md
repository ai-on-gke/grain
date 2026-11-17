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
- **A note is not a question.** A task resting on a run's closing note
  (`comment_on_issue`, `Task.noteSummary`) shows that note's own line under
  a neutral "Note" pill with the neutral shape, and its page a hairline
  "Note:" card. The filled "Asked:" card, the reply tone and the reply shape
  belong to what a run actually asked (`questionSummary`).
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

- **Rail order:** Inbox, Workstreams, Tasks, Agents, Sessions, Processes, CI, Repos,
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
- **Build stamp:** the "grain v…" stamp at the left of the bottom bar is a
  link to Settings → Upgrade, as it is in the classic UI. It ellipsizes
  before the live count beside it gives way, and is hidden on phones.
- **Old addresses:** a classic address that has a page here (`/system/<tab>`,
  `/admin/logs`, `/sandboxes`, `/caches`, `/metrics`) redirects to it with
  its query kept (`app.routes.ts`), rather than falling through to the
  Inbox.
- **Dispatch & quota alerts:** when dispatch is paused or model quotas are
  exhausted (`shared/dispatch-status.ts`), a lazy `@defer`-loaded global banner
  (`shell/dispatch-banner.component.ts`) mounts below the top bar with a
  `Details (N)` breakdown, paired with status on the topbar live pill and
  contextual queue/settings notices formatted in the configured or local
  timezone with relative countdowns.

### Destinations fill the stage

Every routed page fills the area between the bars, and scrolls inside it,
never the window. The page host element must take part in the layout:

```css
g-my-page { display: contents; }   /* or: display: block; height: 100%; */
```

Forgetting this leaves the host as an inline box. The page then sizes to
its content, nothing scrolls, and the side drawer stops halfway down. Settings
shipped with exactly this bug.

One pane, one scroller. The scrolling element is a flex child with
`min-height: 0; overflow-y: auto`, and the pane around it clips
(`overflow: clip`) rather than scrolls. Give the scroller `position: relative`
too: `.sr-only` text is `position: absolute`, and with a positioned pane above
it, that text escapes the scroller's clip and gives the pane a second scroll
range. The wheel then chains into that range and slides the grid under the
footer (Agents, task 5977). When a pane can get too short for its fixed header
plus a whole item, scroll the header away with the content in that pane
instead. Agents does this with a size container query on the list pane (narrower
than 480px or shorter than 700px). `e2e/next-agents-grid-scroll.spec.js`
asserts the rule with bounding boxes.

Task Details follows the same rule in a full task page or the Tasks/Agents
inspector: below 600px of pane width or 700px of pane height, the header,
tabs and Details document scroll together. Roomier panes keep the header
and tabs fixed above the Details scroller. Both scroll surfaces are
keyboard focusable; the final attributes, grants and dependency controls
must be reachable without chaining between nested vertical scrollers
(`e2e/next-agent-details-scroll.spec.ts`). Tab navigation restores the outer
page and tab reader positions separately for the selected task. An unvisited
tab starts at the top, so leaving a scrolled Details view cannot hide its
actions. Selecting another task clears these positions; routine updates
leave the reader in place (`e2e/next-task-tab-scroll.spec.ts`).

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
- The detail pane is always there on desktop; it is not a modal. The
  exception is Agents, whose cards are a dashboard in their own right:
  its inspector opens only on the agent `?task=` names. With none (on
  arrival, or after the person closes it) the split drops to one
  full-width column of cards, nothing looks selected, and j/k or a click
  opens the inspector on an agent. It closes from "Close inspector" in
  the split bar, the pane's ×, Escape, or a second click on the selected
  card, and focus goes back to that card.
- **Never open the sheet unasked.** Below 1080px (and on a short landscape
  screen) the pane becomes a fixed sheet over the list and the top bar, so
  a surface may pre-select its first item only where the pane sits beside
  the list; on a tablet the list shows first and a tap opens the sheet
  (`next-responsive-sessions.spec.js` checks Agents at 820px and 1024px).
- **The selected item looks selected.** Whatever the list is drawn as —
  queue rows, table rows, cards — the item the pane shows carries
  `surface-container` with a 3px `primary` leading bar (`inset 3px 0 0`),
  and cards add a `primary` border unless a status border (quiet) says
  more. It is `aria-current="true"`, focusable (`tabindex="0"`, a 2px
  `primary` `:focus-visible` ring), `cursor: pointer`, and Enter or Space
  on the item itself selects it without also firing the page's Enter
  shortcut. A click on a link or button inside the item stays that
  control's.
- **Pane tabs live in `?pane=`, and the pane reads them back itself.**
  `g-task-detail` in pane mode follows `?pane=` whether or not the host
  binds `[tab]`, so a new host cannot ship tabs that do nothing.
- **Empty is a state of its own.** When a surface has nothing to list and
  nothing is selected (no agents running), drop the split and show one
  centred empty state: what this place is for, and one or two ways onward.
  An empty list beside an empty inspector looks broken.
- **A page of several kinds of thing is tabs, not one list with kind
  chips.** Processes shows Schedules & Triggers, Templates, Suites & Runs
  and Repo Workflows & Declarations as tabs (`?kind=` / the path), each
  with its count. The classic `/processes` page drew one mixed list with
  kind chips (All, Schedules, Workflow schedules, Templates, Suites,
  Triggers). `/next/` deliberately has no All view: each kind has its own
  row fields, filters, actions and detail pane, and a mixed list either
  hides those or shows rows whose actions differ by row. Workflow
  schedules and declared triggers are rows of Schedules & Triggers and
  count there. Its own chips (Active, Paused, Event triggers, Suppressed)
  narrow within the tab. Finding one item across kinds is the
  "Filter this view" box on each tab, and the header New menu creates any
  kind from any tab (task 6180).

### Workstream detail

Three columns: tree (≈21%), graph (flexible, minimum 280px), and task detail
(`clamp(440px, 40%, 580px)`). The detail column gets room first; the graph
takes what is left. Below 1280px it becomes two panes.

- **Heavy-data progressive disclosure:** Workstreams list rows summarize
  progress cleanly (`N/M done`, full counts in tooltip), cap repository lists
  at two plus `+N`, keep the detail header free of async description text (`0px`
  height shift when root task details load) by rendering the workstream goal
  inside the right-hand inspector's `In this stream` section as a 1-line
  plain-text preview with a toggle into a bounded (`max-height: 240px`)
  Markdown view, fold completed leaves above 40 nodes by default, and clip SVG
  node labels to node bounds. Telemetry groups quota notices into one collapsible banner, slices
  heavy sections initially (`8` scopes/templates/directions, `12` contributors
  with `All | People` filter and `2 +K` repos), and always keeps contributors
  alphabetical (`A–Z`).

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
  at the end while you are scrolled up; older history paged in above is not
  news and never raises it.
- **Sessions on a phone:** the chat pane scrolls as a whole, the log keeps a
  floor of `min(18rem, 45dvh)` and the composer is sticky at the pane's
  foot, so Send stays above the bottom bar however many rows the chat head
  wraps to (320×568 included). Only a person's own send scrolls the pane.
- **Sending is never blocked by sending.** In Sessions a sent message shows
  at once (dashed: `Sending…`, then `Queued` behind it) and the composer
  clears; a refused one stays as `Not sent` with its reason, Retry and Edit.
- **Comment links (`#comment=<id>`)** open Conversation on any tab. A cold
  load reads 500 comments and pages back 500 at a time to the comment
  before rendering, so the thread draws once (a 3,000-comment task is at most
  six reads); a link followed on an open page pages the same way. The comment
  is scrolled to the middle instantly (no long smooth travel), takes focus,
  and wears `primary-container` with a `primary` border for four seconds:
  on at once, fading out over 600 ms (no fade under reduced motion). A malformed or missing comment says
  so in a snackbar rather than silently landing at the end.

### Settings

Settings uses a 280px grouped section drawer beside flat `.s-card` forms capped
at 1180px, stacked with 24px vertical rhythm (`g-settings-*` flex hosts) and
24px internal padding. Inputs, descriptions, table cells and `<g-pill-select>`
controls use the proportional 14/20 body stack (`.s-num` aligns numeric figures
without switching prose or human names to monospace; `.s-mono` is reserved for
technical slugs, model IDs and paths). Tables keep row actions on one right-aligned
line (`.s-row-actions`), separate any below-table creation/edit form with a
`.s-subsection` divider, let long paths and ref patterns break inside their
cells (`overflow-wrap: anywhere`) rather than scroll the table on a phone, give a
`.s-table-wrap` that can still scroll sideways `role="region"`, `tabindex="0"`
and a label (axe's `scrollable-region-focusable`), and collapse per-connection password inputs behind an
on-demand `Credential` row. A table too wide for a phone never scrolls
sideways: mark it `.s-table.s-table-stack` inside `.s-table-wrap.s-table-stack-wrap`
and give each cell after the first a `data-label` naming its column. Below
880px of the wrap's own width (a container query, since the pane beside the
drawer and rail is narrower than the viewport) each row becomes a card of
labelled lines under its first cell (Settings → Repositories). A new table
outside Settings does the same with its own container query (Fleet's Waiting
on CI, `.fleet-ci-waits-table`). A sub-row editor
inside a table that still scrolls sideways (`.s-table-wrap.s-table-pinned`)
carries `.s-subrow-pin`, so it stays pinned to the visible width at phone sizes
rather than spanning the scrolled table; a Codex connection in the
`subscription` auth mode shows its own auth.json login there in place of the
key field. Search filters live inside `<form role="search"
autocomplete="off">` and secret inputs carry `autocomplete="new-password"` inside
`<form autocomplete="off">` so browser password managers never autofill emails
into search bars. Single-choice pickers use `<g-pill-select>`; a model is always chosen
with `<g-model-picker>` over what the connections advertise, never typed into a
text field. Ordered lists (the fallback models) are rows with Up / Down /
Remove buttons and an add picker that excludes what is already listed. Booleans use `.g-switch`
(`role="switch"`), and edits stage into `.settings-save-bar` (`Discard` /
`Save changes` with a `POST /api/settings/plan` diff preview). Opt-in workflow
controls (such as quality control PR rollout in `Settings -> Tasks`) live inside
their section's `.s-card` with dependent fields disabled while off — never as
persistent topbar or bottombar status badges. Root-persistent `SettingsStateService`
shows `.s-card` skeletons on cold load, opens warm Settings revisits immediately
while refreshing in the background, guards child `ngOnChanges` on non-first
`section` changes, and caches slow `reachable-repos` lookups for 60s.

A settings picker offers the server's own vocabulary (for example
`sandboxBackendOptions` and `networkAccesses` from `GET /api/settings`), never a
hand-written list: a choice the daemon cannot use stays listed as a `disabled`
`PillOption` with the server's reason as its `hint`. A number whose stored `0`
means grain's default shows blank with the server's `<key>Default` as its
placeholder, and a cleared box saves that default again. A card a link points
at carries an `id` (`#capability-policy`, `#credentials`, `#other-secrets`), and the page
scrolls to the URL's fragment once the section has rendered.

A card follows the scope of the routes it writes through, not its section's:
Settings → Capabilities' Deployment credentials holds its fields to
`settings:write` and its stored secrets to `secrets:write`, whatever
`capabilities:admin` says. A config-repo lock disables and dims (`.s-dimmed`,
with the `on-surface-variant` colour rather than opacity) the settings it
covers but never the secrets beside them, which the API keeps writable. A
secret is listed by name and key only, never its value. Its value
box is `type="password"`, with "From file…" for multi-line values such as
PEMs, never a textarea masked by CSS. Every removal of a secret, key, scope
set or server asks first through `<g-confirm-dialog>`, saying what stops
working. A table cell holding a list (scopes, repositories) stays on one line,
truncates with the full list in its `title`, and drops secondary columns
(`.s-hide-phone`, the full list kept in the row's `title`) below 600px so that row actions stay in view.

A setting kept in the secret store rather than in grain/config (the Hub's
`grc_…` registration token) stays editable while a config-repo lock
disables the fields around it, says when the daemon has no secret store
instead of offering a box that cannot save, and is cleared only after a
confirm. A value checked by its form (an `owner/name` repository, a
`key=value` node selector, an auto-overcommit bound) is refused by name in
an `.s-field-error` under its box and holds the Save bar; what only the
server can judge (a minimum above its maximum) is left to its refusal in
the error banner. A settings table that must fit a phone folds secondary
columns into the first (`.s-cell-stack`: repository over path) rather
than scrolling sideways. A blank string setting shows `<key>Default` as
its placeholder like a number does; a blank that means "none" (a pod
priority class) says `none` instead.
Where a stored `0` is a value of its own rather than "grain's default"
(CPU or memory overcommit, where 0 is unbounded; the disk floor share,
where 0 holds nothing back), a typed `0` saves 0 and says what it means
beside the box, and a cleared box saves the server's `<key>Default` --
so the greyed placeholder is always what Save stores.

### Pre-PR quality checks

Quality control stays inside existing destinations rather than adding new rail
items or topbar chrome:

- **Settings → Tasks:** opt-in rollout switch, flow package `<g-pill-select>`,
  rollout percentage, and control shadow-measurement toggle inside `.s-card`.
- **Processes → Quality checks (`/processes/quality`):** standard 30/70 split
  listing built-in and `.grain/qa-flow.yml` flow packages on the left and stage
  checks, path scopes, and isolation settings in `.proc-card` panels on the
  right, with a `Configure rollout` link to `/settings/tasks`.
- **Task Detail → Changes & CI:** a `.ci-runs-card` (`Pre-PR quality checks`)
  shown above `CI checks & runs` only when quality control is active or
  recorded on that task. Each check run uses `<g-status-icon>` inside
  `.status-pill`, finding evidence expands on demand in `.ci-step-log`, and
  `Helpful` / `Not helpful` `.filter-chip` toggles (`aria-pressed`) are shown
  only when the viewer holds `tasks:write`.
- **Telemetry (`/telemetry`):** Two tabs (`Overview` | `Quality checks`) using the standard WAI-ARIA `.tab-strip` pattern. The `Overview` tab shows operational throughput cards and a compact Quality checks digest card. The `Quality checks` tab shows rollout results and check health (gated by `deployment:read`), with the header window control replaced by a read-only epoch chip to explain origins are grouped by enrollment time.

### Breakpoints

`< 600px` phone, `600–839px` compact, `840–1279px` medium, `≥ 1280px`
expanded. Test at 390×844, 1024×860, 1440×900 and 2048×1200 at least.

## 4. Components

Use these before writing new markup. If one is close but not right, extend
it rather than copy it.

| Component | File | Use |
|---|---|---|
| Pill select | `shared/pill-select.component.ts` | every single-choice dropdown (no native `<select>`) |
| Toggle pills | `shared/grants-picker.component.ts` (`.grant-pill`), `repos/repo-detail.css` (`.repo-toggle-pill`) | multi-choice sets (capabilities, a qualification item's *Depends on*) and three-state settings (work ownership's *On / Off / Say nothing*): a labelled `role="group"` of buttons carrying `aria-pressed`, never a checkbox list or a multiple `<select>` |
| Model picker | `shared/model-picker.component.ts` (+ `shared/model-catalog.ts`) | every model choice: searchable, grouped by connection, keeps and flags a value no connection advertises, optional *Refresh models* |
| Priority chip | `shared/priority-chip.component.ts` | named priority levels with grain's number |
| Actor chip | `shared/actor-chip.component.ts` | who did something, shape-coded, "on behalf of" |
| Status icon | `shared/status-icon.component.ts` | the shape inside every status pill |
| Conversation | `shared/conversation/` | thread + composer (section 3) |
| Task detail | `task/task-detail.component.ts` | the one task view, full page and in panes |
| Split controller | `shared/split-controller.ts` | list|detail sizing and persistence |
| Confirm dialog | `shared/confirm-dialog.component.ts` | wide or destructive actions, with scope |
| Markdown | `shared/code-highlight.ts` (`[gMarkdown]`) | agent output: sanitised, code copy, links; raw HTML is text except bare `code`/`strong`/`em`/`b`/`i`/`kbd`/`sub`/`sup`/`br`, and `&quot;`-style references read as characters; Mermaid fences draw as diagrams (`shared/mermaid.ts`) |
| Prompt and attributes panes | `shared/detail-panes.css` (`.g-panes`, `.g-pane`, `.g-attrs`) | what an agent is told beside the facts about it: task detail, Processes schedule and template inspectors |
| Capped Markdown | `shared/capped-markdown.component.ts` | a long message beside its controls (an Inbox question, an access reason, an alert): full Markdown, at most 20 lines or 40% of its pane (`cqh`), own scroll, "Show full …" in place |
| Diff viewer | `shared/diff-viewer.component.ts` | line-numbered, word-level, syntax coloured |
| Tooltip | `shared/tip.directive.ts` (`gTip`) | every icon button and every non-obvious control |
| Director drawer | `director/director-drawer.component.ts` | global coplanar (`>= 1080px`) or dialog overlay (`< 1080px`) Director panel |
| Director shaper | `director/director-shaper.component.ts` | in-sheet task shaping with `grain-draft` diff & Apply |
| Session canvas | `sessions/session-canvas.component.ts` | coplanar right-hand Docs, sandboxed HTML Mocks, `grain-plan` DAG, Memory editor |

Rules:

- **Every message is Markdown.** A question, reason or alert body a person
  reads is rendered with `[gMarkdown]` (or `g-capped-markdown` where it sits
  above actions), never `{{ }}` into a `<p>`: interpolation runs lists and
  code blocks into one paragraph. A one-line `summary` stays the row title.
- **A decision's actions never leave the screen.** In the Inbox inspector
  only the message in the resolution dock gives way (it shrinks and
  scrolls); the dock's actions keep their height and the inspector body
  keeps a floor of `min(240px, 25cqh)`, so the task's details are never a
  sliver. `next-inbox-long-question.spec.js` holds this at 1280×800 and
  2269×1079.
- **Class names are global.** Most pages use `ViewEncapsulation.None`, so a
  class defined in one page's stylesheet applies wherever that stylesheet is
  loaded. Prefix page-specific classes, and search for a name before you use
  it. Example: Task Detail's `.gate-card` restyled the Backlog's approval
  card until it was renamed `.intake-card`.
- **A prompt has its own pane.** Wherever a page shows the prompt an agent
  is (or will be) given -- task detail on the full page and in every
  inspector pane, and the Processes schedule and template inspectors -- it
  sits in a `.g-pane` headed "Prompt", rendered with `[gMarkdown]`, never
  as a raw monospace text well. The facts about it (priority, repository,
  cadence, overlap, ...) go in a second `.g-pane` below, headed
  "Attributes" for tasks and "Configuration" for processes, as a `.g-attrs`
  key-value list. The key column is capped at 40% of the pane and the list
  stacks key over value when the pane is under 260px, so no value clips or
  overlaps its key at any split width (`ui-ng/e2e/next-prompt-panes.spec.js`).
- **A time is shown as a date, never as the wire's string.** An instant
  the server sends (`2026-11-17T10:24:14.543896659Z`) reaches the page
  through `formatStamp` (`shared/dispatch-status.ts`), so a row and the
  pane beside it say the same time the same way
  (`ui-ng/e2e/next-processes-run-times.spec.ts`).
- **Task detail container layout, Docs tab, and full-screen document reader.**
  `.task-page` is an inline-size container (`container: task-page / inline-size`);
  at `@container task-page (max-width: 1060px)` `.task-layout` collapses its
  right-hand `.task-aside` so opening the coplanar Director drawer automatically
  gives `.task-main` full width. The `Details` tab stays visible at every width
  without redirecting back to `Conversation`. A dedicated `Docs` tab
  (`g-task-docs-tab`, `task/task-docs.ts`) pairs a document picker with a clean
  `surface` Markdown reader (`76ch` measure, independent vertical scrolling)
  across the task prompt, PR Markdown/doc diffs, and structured report comments.
  Prompt headers and the Docs reader offer `Open Docs` and `Full screen` (`⤢`)
  triggers that open a native top-layer `<dialog class="doc-reader-modal">`
  (`aria-modal="true"`, backdrop click & Escape dismissal, `Prev` / `Next`
  document navigation, `76ch` measure, independent scroll)
  (`ui-ng/e2e/next-task-docs-scroll.spec.ts`).
- **One control height per toolbar.** Chips, pill selects and segmented
  buttons in one row share a height (36px in toolbars). Reset inherited
  margins (`.filter-strip` carries a top margin for page headers).
- **Every control has a name.** Icon buttons get `aria-label` and a `gTip`.
- **A deployment finding that blocks nothing is a warning, not an error.**
  Something an operator should fix while the deployment keeps working (a
  base branch publishing nothing, `baseBranchPublishing`) is a
  `warning-container` banner with a warning status icon on its first line
  and `role="status"`. It sits beside the page's error banners (Dispatch
  blocked, `role="alert"`), above the content. Use the server's `detail`
  sentence rather than rewording it, and link the task that holds the
  finding where there is one (`ui-ng/e2e/next-fleet-publishing-held.spec.js`).
- **A Mermaid fence is a diagram, not code.** `[gMarkdown]` draws
  ```` ```mermaid ```` fences in the active theme with a Diagram / Source
  toggle, and pairs Before / After fences (named in the info string or by
  matching headings) into one block with Side by side / Before / After /
  Stacked / Source (Before alone on a phone). `mermaid` is imported only
  when a message holds a diagram, and its SVG passes `sanitizeMermaidSvg`
  before it reaches the DOM. A diagram never shrinks below 75% of its
  natural size; on a phone a wide one scrolls inside its canvas, which
  takes keyboard focus. A source that does not parse says why and shows
  the source (`ui-ng/e2e/next-mermaid-diagrams.spec.ts`). Render
  user-written Markdown through `[gMarkdown]`, never `renderMarkdown()`
  into `[innerHTML]`, so previews draw diagrams too (the Context page's
  previews, `ui-ng/e2e/next-context-doc-mermaid.spec.ts`); a page that
  hosts it loads `shared/code-highlight.css` and `shared/mermaid.css`.
- **Say a wire code in words.** A reason or state code from the API
  (`publishingHeld`, `standingWork`) is mapped to words before it is shown.
  A code nobody has mapped yet shows as itself, which beats showing nothing.
- **A form that files tasks offers every setting a task carries.** The
  schedule and template forms carry Admission and Hosting (pill selects
  whose one-line help says what the choice means and that it "applies to
  every task this … files"), Keep running (a switch), Grants
  (`g-grants-picker` with `[policyVerdicts]="true"`, so a grant the
  capability policy denies, may refuse or reserves for CI is badged and
  explained, and a refused method is listed disabled with the policy's
  reason) and Model (`g-model-picker` plus *Use default*). Where
  something else decides a setting (a template behind a schedule, a suite),
  the form says so instead of offering a box the server would ignore.
- **An edit sends only what changed.** A PATCH from a form leaves out each
  setting still equal to the saved row: absent means "leave alone", and
  resending an unchanged admission would ask a limited role's grant gate
  for a raise nobody made. A draft composition preview is the other way
  round: it always sends what is in force, because the preview endpoint
  has no saved row to fall back on (`ui-ng/e2e/next-processes-forms.spec.ts`).
- **A dialog fits the screen.** `.g-dialog` (and its `.is-drawer` width) is
  `box-sizing: border-box`, so its `min(520px, 100vw - 32px)` width
  includes its padding and border. That leaves a 16px margin either side
  on a phone. A fixed-position box sized against `100vw` must be
  border-box, or its padding pushes it off both edges
  (`ui-ng/e2e/next-dialog-fits-phone.spec.js`).

## 5. Interaction

- **Keyboard first.** `c` new task, `d` / `Alt+D` Director drawer, `/` filter, `?` help, `1`–`9`
  destinations, `j`/`k` step, `x` select, `Shift+F` full-screen conversation.
  Every shortcut is listed in Help. Shortcuts never fire while typing
  (`isTypingTarget`).
- **URLs.** Every view, tab and selection that a person might share has a
  URL (`?tab=`, `?task=`, `?pane=`). Back works.
  A list's narrowing and order are URL state too: the "Filter this view"
  box is `?q=` (written back as it is typed, with `replaceUrl`) and a
  sort is `?sort=`, so a reload and Back/Forward keep them. Tabs carry
  both (`queryParamsHandling="merge"`). See
  `ui-ng/e2e/next-processes-narrowing.spec.ts`.
- **A custom order moves by pointer and by keyboard.** Where a list keeps
  a person's own order (a "Custom order" sort), a row is dragged, or moved
  one place with `Alt+ArrowUp` / `Alt+ArrowDown` (named in
  `aria-keyshortcuts`). Each move is announced in a polite live region,
  and focus stays on the row that moved. That order is browser state, kept
  under the classic UI's own `grain.list-order.<list>` key and row keys,
  so both UIs show it.
- **Role-aware.** An action the person cannot take is hidden, or shown
  disabled with the reason. It is never shown enabled only to fail.
- **State-aware.** The same goes for an action the item's state refuses.
  Example: Release hold cannot clear review findings, so a hold of findings
  alone shows no Release hold. The gate card says what does clear it and
  offers that action (*Override review gate…*) to people who may take it
  (`ui-ng/e2e/next-human-review-findings-hold.spec.js`).
- **Refusals say why.** A failed action shows the server's own `error`
  text (`errorText`). A refusal the request caused is a 4xx with a message,
  never a 500 with only a reference id.
- **Safety.**
  - Selection is pinned: if the selected item leaves the list, say so; do
    not jump to another item.
  - The composer is locked to its target task.
  - Batch actions name their scope (repos, owners) in the confirmation.
  - A batch action acts only on the selected rows its state allows (Run
    now skips running and closed work, Submit wants `awaiting_submit`).
    When that is fewer than the selection, the label counts them
    (`Submit (2)`). It is offered only when the role reaches every one of
    those rows.
  - A bulk action shows its progress in the selection bar and offers
    Stop: calls already sent finish, and the rest are never sent.
    Afterwards only the rows that failed or were never sent stay ticked
    (`ui-ng/e2e/next-tasks-batch-actions.spec.js`).
  - Undo for reversible actions.
  - An action that moves a branch, opens a pull request on someone's
    behalf or drops a record (promote, merge back, delete) asks first in
    the confirm dialog, naming what it touches.
  - A form that waits for a Save button asks before its edits are
    dropped, whether by a tab switch, a link, the rail or browser Back
    (a `canDeactivate` guard plus the page's own tab bar). Discard puts
    back the saved values.
  - A refused value is reported beside its field (`aria-invalid`) and
    clears on the next edit, not in a page-wide banner.
- **Live, quietly.** Server-sent events update lists in place. Never steal
  focus or scroll position on an update; hold an update while the person is
  interacting with the row.
- **A failed poll is not news.** A background read that fails once keeps
  the last good answer on screen and says nothing; only failure that lasts
  (three reads in a row, or 30 s, `services/request-trouble.ts`) puts up
  one banner, over the stale list rather than in place of it. A read that
  has never succeeded says so at once. The same sentence is never shown
  twice: two surfaces reporting one failure show it once. A person's own
  action that fails still says so immediately.

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
  at desktop or phone size (`ui-ng/e2e/next-full-system.spec.js`).
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
- Request efficiency: coalesce concurrent identical GET/conditional reads,
  serve `GET /api/config` and Inbox summary hydrations with ETag `If-None-Match`
  and bounded worker pools (`<= 4`), throttle hidden-tab or unmounted-page
  polling, load inactive Fleet tabs lazily, and never block primary pickers on
  slow `reachable-repos` discovery. A poll that is failing backs off
  (doubling, capped at 60 s, never sooner than `Retry-After`) and ignores
  event wake-ups until a read succeeds again.
- No new runtime dependency without a reason written in the pull request.
- A click never waits silently on the network. Every lazy page chunk is
  preloaded shortly after start-up (`shell/route-preload.ts`, skipped under
  Save-Data), so navigation does not stall behind a slow origin. A
  navigation still pending after 120 ms shows the shell's top progress bar
  (`shell/nav-progress.component.ts`), which takes no pointer events. Routes
  have no resolvers or guards that wait on polled reads; a page loads its own
  data and shows its loading state. `e2e/next-task-page-stall.spec.js`
  covers this.

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
3. **Real browser over `grain demo` (Playwright, `ui-ng/e2e/`, `make
   test-e2e-ng`, CI job `ui-ng e2e`):**
   - `next-full-system.spec.js` visits every `/next/` destination in light and
     dark, at 1440×900 and 390×844. It asserts no sideways scroll, no page
     error and no serious or critical axe violation, plus contrast on the
     primary destinations. Set `GRAIN_UI_SHOTS=<dir>` to write one PNG per
     page, theme and size.
   - Scenario specs cover the interactions people rely on: selecting and
     resizing the split, changing priority with Undo, reordering the backlog,
     replying to and waking an Inbox item, expanding and full-screening a
     conversation, editing a task in place, filing a new task. A new
     scenario spec goes in `ui-ng/e2e/` as a `*.spec.ts`, never in the
     classic `ui/e2e/`.
   - Specs import `test` and `expect` from `ui-ng/e2e/fixtures.ts`: one
     signed-in `grain demo` per worker (shared with the classic suite
     through `ui-shared/e2e/`), `baseURL` at its `/next/`, an `axe()`
     fixture returning serious and critical violations, and a
     `pageErrors` fixture that fails a test on any uncaught error.
   - Projects: `desktop` (1440×900) runs every test; `phone` (390×844,
     touch) and `dark` run the tests tagged `@phone` or `@dark`.
   - Select by role and accessible name (`getByRole`, `getByLabel`), as a
     person or screen reader finds things. Never select by a CSS class,
     and never by a classic UI class name. A `data-testid` is a last
     resort for something with no role or name of its own.
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

## 12. V3 Studio, Telemetry, & Inbox Badges

The **V3 Studio** (`/next/processes`) separates pipeline stages (S0 Intent, S2 Plan Critic, S5 Merge Triage) and custom checks (pattern, diff-budget, companion, jsonschema) into a dedicated component (`g-qa-v3-studio`), maintaining strict component constraints (`< 6,500` bytes CSS). It also introduces the "Adopt from repo commit" modal and a "What-if probe" drawer to simulate pipeline execution.

The **Telemetry Cards** (`/next/telemetry?tab=quality`) adds three new compact elements in `g-quality-v3-cards`:
- A **Diff Strip** to highlight pipeline modifications against the reference arm (`+S0`, `+custom checks`, `Verifier changed`).
- A **Stage Funnel & Cost Attribution** ops-card, measuring `p95 latency`, `runs`, and `tokensMedian` per stage and arm.
- A **Human-Review Triage (S5)** ops-card highlighting escape rates, human-routed precision, and reviews saved.

The **Inbox & Tasks views** now integrate a compact `[T2/T3 · Review]` triage chip natively in queue rows (via `qaTriageChip()`), reducing UI noise while ensuring safety-critical `qa-triage` tasks are immediately identifiable in `<span class="row-line3">`. The Task Changes tab also explicitly flags `Complexity` and `Blast` tiers.

## 13. V4 Software Workflow & QA Studio, Tab-Strip Underline, & Full-Width Composition Body Well

1. **Flush `3px` Active Tab Underline (`button.tab-link` & `.stage-header:has(> .tab-strip:last-child)`):**
   - Both `<a class="tab-link">` and `<button class="tab-link">` reset top/left/right borders (`border-top: 0; border-right: 0; border-left: 0`) and reserve a `3px` bottom border (`border-bottom: 3px solid transparent`), switching to `border-bottom-color: var(--md-sys-color-primary)` and `color: var(--md-sys-color-primary)` when `[aria-current="page"]` or `[aria-selected="true"]`.
   - When `.tab-strip` is the last child of `.stage-header` (such as on `/next/telemetry` and `/next/processes`), `.stage-header:has(> .tab-strip:last-child) { padding-bottom: 0 }` removes the bottom header padding so the active tab's `3px` underline sits flush against `.stage-header`'s `1px` bottom border with zero horizontal overflow at both `1440×900` and `390×844`.

2. **`CompositionViewComponent` Full-Width `.comp-body-well` Rule (`processes-detail.css`):**
   - In `g-composition-view` (`[data-testid="composition-preview"]`), scalar metadata fields flow in `.comp-meta-grid` (`grid-template-columns: repeat(auto-fill, minmax(170px, 1fr))`), while list fields (`reads`, `writes`, `capabilities`, `customFields`) and the `body` prompt row use `.comp-row-full` (`grid-column: 1 / -1; width: 100%`).
   - `.comp-body-well` (`[data-testid="composition-body-well"]`) spans `grid-column: 1 / -1; width: 100%; box-sizing: border-box` (`>= 85%` of `.comp-task` `clientWidth`, never crushed into a narrow `~220px` single grid column), with a `320px` max-height scroll clamp (`.is-clamped`) and an `Expand full prompt` / `Collapse prompt` toggle (`[data-testid="composition-body-toggle"]`) for long task prompts.

3. **`30/70` 8-Stage `Software Workflow & QA` Studio Layout & Operator `qaFlowRef` Pickers:**
   - Under `/next/processes/quality`, the tab is titled **`Software Workflow & QA`** (`[data-testid="proc-tab-quality"]`) and renders a full-bleed workbench (`g-qa-flows-panel` + `g-qa-v3-studio`) without the narrow master-detail list pane.
   - A top sub-header toolbar (`[data-testid="qa-studio-toolbar"]`) houses the flow package switcher (`<g-pill-select testid="qa-flow-switcher">`), active/schema badges (`v2`, `v3`, `v4`), live prompt-budget meter (`[data-testid="qa-prompt-budget"]`, `<= 8,192 B`), flow version quick-select strip (`[data-testid="quality-flows-list"]`), and studio actions (`What-if probe`, `Dry run`, `Adopt from commit`, `Fork flow`, `New version`).
   - Below the toolbar, `.qa-workbench-split` uses a `30/70` split (`grid-template-columns: clamp(300px, 30%, 440px) minmax(0, 1fr)`) on desktop (`>= 900px`) and collapses cleanly to a single-column vertical stack (`grid-template-columns: 1fr`, with `.qa-stage-tree` switching its right border to a bottom border and `.qa-ordered-steps` scrolling horizontally inside its card) on viewports `< 900px` (`390×844`) with zero horizontal page overflow:
     - **Left `30%` Stage & Step Tree (`[data-testid="qa-stage-tree"]`):** search input (`[data-testid="qa-step-search"]`), step-kind filter chips (`All`, `Skills`, `Checks`, `Agents`), and the collapsible 8-stage software lifecycle tree (`1. Decompose & Contract` through `8. Merge & Post-Merge`, plus custom stages).
     - **Right `70%` Full-Width Stage / Step Inspector & Live Editor (`[data-testid="qa-step-inspector"]`):** stage summary bar (`[data-testid="qa-flow-detail-card"]`) and three inspector tabs (`Definition & fixtures`, `Empirical stats & dry-run`, `Lineage & YAML`).
   - Operator-owned Schedule, Template, and Suite create/edit drawers include a **`Software workflow`** `<g-pill-select>` picker (`[data-testid="process-qa-flow-select"]`) populated from `GET /api/qa/flow-versions` (defaulting to `Deployment default` plus active/built-in flows such as `engineering-workflow-v4@1`, `golden-v3-enforced@1`, `golden-v3@1`, `golden-v2@1`), persisting `qaFlowRef` and displaying a `Workflow: <qaFlowRef>` badge (`[data-testid="process-qa-flow-badge"]`) in the detail header when set.

## 14. Autonomous Goal Mode

Autonomous Goal Mode lets an operator hand Grain a multi-pass outcome ("Prototype a goal") and step back until the work is verified and ready to submit or reaches a hard safety/budget boundary.

### 14.1 Intake (`+ Create` menu & `NewTaskSheetComponent`)

- The `+ Create` menu in the shell (`AppComponent`) offers a third rich 2-line entry after **New task** and **Start a workstream**:
  - **Prototype a goal** (`data-testid="create-prototype-goal"`): *"Hammer through autonomously — logs assumptions, verifies deliverables, opens a PR"*.
  - Selecting it opens `NewTaskSheetComponent` pre-filled with `goalMode: { enabled: true, maxPasses: 8, wallClockMinutes: 120, mockMissingSecrets: true }` and `autoApproveProposals: true`.
- Inside `NewTaskSheetComponent`, **Execution posture** (`<g-pill-select testid="nt-posture">`) switches between `Supervised` and `Autonomous goal`.
- When `Autonomous goal` is active, a compact `.nt-section-card` (`data-testid="nt-goal-config"`) reveals:
  - A calm one-sentence explanation of Inbox silence and bounded verification passes.
  - **Max verification passes** (`data-testid="nt-max-passes"`, `1..20`, default `8`).
  - **Wall-clock budget (min)** (`data-testid="nt-wall-clock"`, `5..720`, default `120`).
  - **Mock missing external secrets locally** (`data-testid="nt-mock-secrets"`, `role="switch"`).
  - Primary submit action label updates to **Start autonomous goal** (`data-testid="new-task-submit"`).
- `.sheet-body` carries `32px` bottom padding and `scroll-padding-bottom: 32px` (with `flex-shrink: 0` on direct children) so bottom controls scroll comfortably clear of the sticky `.sheet-foot`. Active snackbars anchor above `--grain-bottombar-height` (`+ 16px` desktop, `+ 84px` phone, `+ 80px` landscape short) and dismiss/suppress overlapping bottom-bar tooltips on open.

### 14.2 Inbox Silence & Terminal Surfacing (`inbox-classifier.ts`)

- While `t.goalMode?.enabled` is true, mid-flight `awaiting_reply` questions (`!t.awaitingAccess && !isStoppedTurn(t)`) and auto-approved child proposals (`t.state === "proposed" && !!t.proposedBy && !!t.autoApproveProposals`) are suppressed from `waitsOnUser` and `classifyTask`.
- Terminal and safety states always surface in the Inbox:
  - `awaiting_submit`: renders pill **Goal ready** (`healthy`) when `t.goalScorecard?.verified` is true, or **Goal capped** (`warn`) when `t.goalScorecard?.stoppedAtCap` is true.
  - `awaitingAccess`, `isStoppedTurn`, `failed`, `waitingBaseRepair`, `mergeBlocked`, `mergeQueueStalled`, and `stall` surface in their standard priority groups.

### 14.3 Goal Scorecard, Assumptions Ledger & Revise Loop (`<g-task-goal-card>`)

- `<g-task-goal-card>` (`data-testid="task-goal-card"`) renders above `<g-conversation>` in `TaskDetailComponent` (`/next/task/:id` and split-pane inspectors) and inside `InboxPageComponent`'s inspector whenever `goalMode?.enabled`, `goalScorecard`, or `goalAssumptions` is present.
- Header (`data-testid="goal-status-pill"`):
  - `Goal verified · N passes` (`healthy`), `Stopped at cap · N / M passes` (`warn`), or `Autonomous goal · Pass N / M` (`setup`).
- Operator controls:
  - **Switch to supervised** (`data-testid="act-switch-supervised"`): sends `PATCH /api/tasks/{id}` with `{ goalMode: { enabled: false } }` so the operator can take over mid-flight.
  - **Revise…** (`data-testid="act-revise-goal"`): opens `NewTaskSheetComponent` pre-filled as a follow-up autonomous goal targeting the task's branch (`base: t.branch || t.base`), linked via `generatedFrom: t.id`, with prior summary and assumptions pre-populated in the description.
- Body:
  - **Deliverables checklist** (`data-testid="goal-deliverables"` / `data-testid="goal-deliverable"`): each requirement, verification mark (`✓` / `!`), and evidence citation.
  - **Assumptions ledger** (`data-testid="goal-assumptions"` / `data-testid="goal-assumption"`): each autonomous decision (`topic`, `decision`, `rationale`, `alternatives`).

### 14.4 Cross-Surface Badges

- **Tasks (`/next/tasks`)** and **Agents (`/next/agents`)**: render compact status pills (`data-testid="goal-badge-<id>"`) showing `Goal ✓` (`healthy`), `Goal capped` (`warn`), or `Goal · N/M` (`setup`).
- **Workstreams (`/next/workstreams`)**: streams whose root or member tasks run in Autonomous Goal Mode display an `Autonomous goal` chip (`data-testid="ws-goal-chip-<key>"`).
