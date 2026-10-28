# grain UI — visual design review and redesign options

A designer's pass over grain's web UI (`ui/`), done against a throwaway
`grain demo` store on 2026-10-26 at build `16cace5ee`. It captures the
nine main screens as they are, critiques them, and proposes three
redesign directions — from a conservative refresh to a bolder rework —
each with a colour system, type scale, icon set, refreshed mark and
mockups rendered over the real UI. The shipping UI is untouched: every
mock is CSS and a little DOM surgery injected by Playwright
([`tools/`](tools/)), nothing under `ui/src` changed.

**Recommendation in one line:** ship **Option 2 (“Ledger”)**, taking
Option 1's contrast and chip fixes as its first increment and keeping
Option 3's state stripe and ink rail in reserve for a later brand push.
The ranking and reasons are at the [end](#ranking-and-recommendation).

> [!NOTE]
> **How this was looked at.** The sandbox had no way to view a PNG, so
> besides the captures themselves every claim below is grounded in
> measurement: a computed-style census of each rendered screen (font
> sizes, weights, text/background pairs with WCAG ratios, element
> geometry — [`tools/census.mjs`](tools/census.mjs)), DOM structure
> dumps, and ImageMagick histograms of screenshot regions. Numbers in
> the critique come from those; the images are for the reader.

Contents

1. [Current state](#1-current-state)
2. [Critique](#2-critique)
3. [Option 1 — Settle (conservative refresh)](#3-option-1--settle)
4. [Option 2 — Ledger (moderate redesign)](#4-option-2--ledger)
5. [Option 3 — Plate (bold redesign)](#5-option-3--plate)
6. [Ranking and recommendation](#ranking-and-recommendation)
7. [Appendix: method, tools, full contrast tables](#appendix)

---

## 1. Current state

Nine screens, desktop 1440×900 and phone 390×844, light and dark.
The theme is switched through the app's own `grain.themeMode` setting as
well as `prefers-color-scheme`, as `ui/e2e/screenshot.mjs` does.
Routes: `/`, `/boards/board-1`, `/tasks/4`, `/new`, `/ci`,
`/repos/acme/widgets/settings/merge-queue`, `/inbox`,
`/settings/capabilities`, `/metrics`. The demo ran with
`-observability -merge-queue-states -merge-rules -review-questions` and
three CI runs were started through the CI page's own form so the CI
table has rows ([`tools/seed-ci.mjs`](tools/seed-ci.mjs)).

| screen | desktop · light | desktop · dark | phone · light | phone · dark |
|---|---|---|---|---|
| Task list / backlog | ![](current/tasks-desktop-light.png) | ![](current/tasks-desktop-dark.png) | ![](current/tasks-phone-light.png) | ![](current/tasks-phone-dark.png) |
| Board | ![](current/board-desktop-light.png) | ![](current/board-desktop-dark.png) | ![](current/board-phone-light.png) | ![](current/board-phone-dark.png) |
| Task detail (chat + timeline) | ![](current/task-desktop-light.png) | ![](current/task-desktop-dark.png) | ![](current/task-phone-light.png) | ![](current/task-phone-dark.png) |
| New-task overlay | ![](current/new-task-desktop-light.png) | ![](current/new-task-desktop-dark.png) | ![](current/new-task-phone-light.png) | ![](current/new-task-phone-dark.png) |
| CI runs | ![](current/ci-desktop-light.png) | ![](current/ci-desktop-dark.png) | ![](current/ci-phone-light.png) | ![](current/ci-phone-dark.png) |
| Merge queue / repo page | ![](current/merge-queue-desktop-light.png) | ![](current/merge-queue-desktop-dark.png) | ![](current/merge-queue-phone-light.png) | ![](current/merge-queue-phone-dark.png) |
| Inbox / alerts | ![](current/inbox-desktop-light.png) | ![](current/inbox-desktop-dark.png) | ![](current/inbox-phone-light.png) | ![](current/inbox-phone-dark.png) |
| Settings → Capabilities | ![](current/capabilities-desktop-light.png) | ![](current/capabilities-desktop-dark.png) | ![](current/capabilities-phone-light.png) | ![](current/capabilities-phone-dark.png) |
| Metrics | ![](current/metrics-desktop-light.png) | ![](current/metrics-desktop-dark.png) | ![](current/metrics-phone-light.png) | ![](current/metrics-phone-dark.png) |

---

## 2. Critique

The UI is in better shape than most internal tools: it has a real token
layer (`ui/src/style.css` `:root` and `ui/src/theme.js`, deliberately
twinned), a brand with an argument behind it (`docs/brand.md`), and a
shell — rail, page header, pane — that every page respects. What follows
is what a designer would change, not a list of bugs.

### 2.1 Information hierarchy and density

- **The list row carries six things of equal weight.** On `/` every row is
  drag handle · checkbox · state glyph · number · title · five or six MUI
  chips · reason chip ([`tasks-desktop-light`](current/tasks-desktop-light.png);
  `ui/src/style.css` `.task-row`, `TaskList` in `ui/src/App.jsx`). The
  census counts **106 chips on the first screen of the backlog** — the
  capability grants (`gcp key`, `gemini key`, `grain-ci`, `self debug`)
  repeat on almost every row, so they carry no information per row and
  take ~500px of each one. The title, the only thing a reader scans, is
  `14.4px/400` while the chips are `13px` filled pills: the noise is
  nearly as loud as the signal.
- **Rows are tall but not roomy.** 58px per row with 0.6rem padding, yet
  the row's content is a single line; the height comes from the 38px
  checkbox hit area and the 24px chip strip. Twelve rows fit in 900px.
- **The page header is 77px for a 16px title.** `.page-header` fixes its
  height at 77px (`.page-header { height: 77px }` in `style.css`) and holds a 20px icon, the title and
  a toolbar slot; on `/ci` the title is the two letters “ci” with ~990px
  of empty slot beside it ([`ci-desktop-light`](current/ci-desktop-light.png)).
- **The task page is well arranged but under-stated.** `/tasks/4` puts a
  pending question, the description, then the timeline (newest first)
  in an 863px column with a sticky 260px side column
  (`.detail-layout`, `.detail-side`). That is the right structure. But
  the state — the one fact that explains why this task is in front of
  you — is a `12.48px` badge at the top of the side column, while the
  “Waiting for your reply” card and the “Send” button are the same
  colour and weight as everything else.
- **Settings uses a second rail inside the pane.** `/settings/capabilities`
  shows the app rail, then a settings rail of 17 entries in five groups,
  then the page ([`capabilities-desktop-light`](current/capabilities-desktop-light.png)).
  Two rails of the same width and typography compete; the settings one
  should be visibly subordinate (narrower, smaller type, or tabs).

### 2.2 Navigation: rail, tabs, overlays

- The **rail** (232px, `theme.js` `SIDEBAR_WIDTH`, `components/Sidebar.jsx`)
  does three jobs in one column: a pinned section, the Tasks tree with
  its nine state children, and the destinations (Boards, Sessions,
  Processes, CI, Repos). The state children are the useful part and the
  cheapest to scan, but they are indented 4px and set in the same
  `13.6px/500` as the destinations, so the tree reads as a flat list of
  fifteen items. Counts are right-aligned numerals in two weights (600
  when non-zero and “yours”, 400 otherwise) — a good idea nobody will
  notice at 12px.
- **Overlays grey out the rail.** Opening a task opens an MUI `Dialog`
  whose backdrop covers the rail: the histogram of the rail region of
  [`task-desktop-light`](current/task-desktop-light.png) is 50% grey
  `#7d7d7c` rather than the rail's white. The pane starts at
  `SIDEBAR_WIDTH` precisely so the rail stays in place
  (`components/Overlay.jsx`), yet it is dimmed and inert. Settings,
  System and the new-task form behave the same way. Either the rail
  stays live (it is navigation, not a modal's context) or the pane
  should be a route with its own back affordance — it already has one
  (`.overlay-pane-back`).
- **Tabs inside the repo page** (`overview · ci · merge queue · agents ·
  branches · releases · advanced`, `components/RepoPage.jsx`) are MUI
  tabs lowercased; the active tab is the only use of the accent on that
  screen, which works. On the phone they overflow with no scroll hint.
- **The phone shell is an AppBar with a hamburger**; the rail becomes a
  drawer. Fine, but the bar carries the mark, the word “grain”, the
  alerts bell, the theme toggle and the menu — five controls in 53px —
  and the page title lives a further 50px down. Phone users land on a
  list with no visible name.

### 2.3 Consistency of spacing, typography and components

- **Type sizes are not a scale.** The census finds **13 distinct font
  sizes on `/tasks/4`** (10.88, 11.2, 11.52, 12, 12.48, 12.8, 13, 13.12,
  13.6, 14, 14.4, 16, 18.4px). They come from rem fractions in
  `style.css` (`0.78rem`, `0.8rem`, `0.82rem`, `0.85rem`, `0.9rem`) and
  MUI's own defaults side by side. None of them lands on a 4px grid, and
  the differences (12.8 vs 13 vs 13.12) are below perception — so the
  page has texture without hierarchy.
- **Weights 400/500/600/700 are all in use**, with 500 and 600 doing the
  same job on different pages (`h3` 600 in the timeline, `Runs` 500 on CI).
- **Two chip systems.** The app's own `.badge` (dot + text, no fill) and
  MUI `Chip` (filled, 16px radius, 24px tall) sit on the same rows.
  Field chips use MUI's default outline `#bdbdbd` and filled chips MUI's
  default `rgba(0,0,0,0.08)` — neither is a token colour, so they are
  the two greys on the page that do not belong to the warm palette.
  Likewise the breadcrumb separator “/” is MUI's `#9e9e9e`.
- **Forced lowercase everywhere.** `theme.js` lowercases buttons, tabs,
  table heads, labels, dialog titles, chips and every heading, and
  `style.css`'s lowercase block (`.badge, .chip, .tab, label, .MuiChip-label …`) adds badges, chips and labels. 139 of the 182 text nodes on
  the backlog are transformed. It is a strong brand voice, but it
  mangles proper nouns and acronyms (`ci`, `gcp key`, `acme/widgets` is
  fine but `github` is not) and it removes the one cue — capitalisation
  — that tells a heading from a label at a glance.
- **Radius is mostly 6px**, except chips (16px), the task-id bubble
  (999px), the pending-question card (8px) and the rail's active row
  (9px). Pick two.

### 2.4 Colour, status colours, the red-base banner, dark mode

- **The warm palette is distinctive and should stay.** Bronze `#8a6a2e`
  on warm white, wheat `#d9a441` on near-black, greys warmed to match:
  it is the mark's own colour and it is not what every other tracker
  looks like. Keep it.
- **But the accent is doing two jobs.** It is the primary button and it
  is the *running* state (`--state-running: var(--accent)`, with the
  argument in the comment beside it in `style.css`). On a list where the “+ New task” button,
  the active rail row and the running spinner share one colour, the
  running state does not stand out as a state.
- **Ten state colours is too many to learn.** proposed amber, queued
  grey, running bronze, interactive teal, awaiting purple, awaiting-submit
  coral, completed/merging blue, merged green, failed red, deferred slate,
  closed grey. Each has a reason in the CSS comments, and the
  reasons are good — but a reader sees eleven dots of unequal lightness.
  Purple and coral at `#9333ea`/`#d95f02` are far more saturated than
  the bronze and blue beside them, so “needs you” shouts and “running”
  whispers, which is the opposite of the sidebar mark's claim that
  running is *the* signal.
- **The red-base banner** (`components/RedBaseRepairBanner.jsx`, an MUI
  `Alert severity="error"`) and the disconnected banner take a full-width
  row above the shell. The pale MUI error fill reads as a form validation
  message rather than “your base branch is broken and the queue is
  held”. It wants the state colour language of the rest of the app (a
  red stripe, the failed glyph) not MUI's default.
- **Dark mode is competent but flat.** Canvas `#0f1013`, surface
  `#14161a`, border `#292b31`: a 2% lightness step between canvas and
  surface, so the rail, header and pane only separate by their 1px
  border. The MUI `Paper` elevation gradient is still on in dark, so
  dialogs are a slightly different grey from the pane behind them for
  no reason. Chip fills in dark are MUI's `rgba(255,255,255,0.16)`,
  again off-token.

### 2.5 Contrast and accessibility (measured)

WCAG 2.x ratios on the actual palette (`tools/contrast.py current`,
full table in the [appendix](#appendix)):

| pair | light | dark | verdict |
|---|---|---|---|
| text `#24221d` / canvas `#f7f6f3` | 14.7 | 15.4 (`#e9e7e1`/`#0f1013`) | pass |
| secondary `#6e6a60` / canvas | 5.0 | 6.6 | pass |
| **tertiary `#9a9588` / canvas** | **2.76** | **3.63** (`#6f6c65`) | **fail AA** |
| **tertiary / white surface** | **2.99** | 3.46 | **fail AA** |
| accent as text `#8a6a2e` / white | 5.0 | 8.1 | pass |
| white on accent (primary button) | 5.0 | 8.2 (`#17130a`) | pass |
| **awaiting-submit `#d95f02` as text / white** | **3.76** | 8.0 | **fail AA** |
| **closed `#9a9588` as a dot / canvas** | **2.76** | 3.63 | **fail 3:1 (non-text)** |
| white on success chip `#1a9c53` (CI “Success”) | **3.54** | — | **fail AA** |
| disabled “Create task” / “Save policy” | 1.84 | 2.56 | fail (MUI default; exempt but illegible) |

`--text-tertiary` is used for task numbers, counts, freshness lines,
timeline timestamps and the “as of just now” stamp — 35 text nodes on
the first screen of the backlog fail AA in light mode, 9 on the phone.
The dark palette is better but not clean. Everything else that matters
passes; fixing `--text-tertiary`, `--state-closed` and
`--state-awaiting-submit` would make the palette AA-clean.

Other accessibility notes: every row click target is the whole row
(good); the state dots are 8px with no text alternative in the rail
(the label is beside them, fine); hover-only scrollbars
(the scrollbar rules at the top of `style.css`) hide the scroll affordance from keyboard users until
focus lands inside the pane.

### 2.6 Iconography

Three icon vocabularies share the rail and the header:

1. **The Chladni item glyphs** (`ui/src/brand/item-glyphs.js`,
   `components/ItemGlyph.jsx`): filled figures at 16px for Repos,
   Schedules, CI, Inbox and so on. Beautiful at 32px, but at 16px the
   filled rosettes are blobs of similar weight — the census shows all
   of them rendering as a 14×14 filled path. They do not read as
   *different* things at rail size.
2. **MUI Material icons** (`@mui/icons-material`, imported in 25
   components, 28 distinct icons): 24px-grid filled glyphs used for the
   alerts bell, theme toggle, drag handle, checkboxes, close buttons.
3. **CSS dots and spinners** (`.dot`, `.badge::before`) for state.

The Material set is heavy and generic next to the brand glyphs; the
brand glyphs are too alike to navigate by; and state has no icon at
all beyond a dot. The mark itself is right and should not change — but
it wants an app-icon tile (the iOS icon is a bare fill on dark) and a
line-weight sibling that can sit beside line icons.

The way out is not to abandon the plate but to draw its **nodal lines**
instead of its filled regions. A Chladni figure is legible by the thin
curves the sand settles on; filled at 16px those curves become one
blob, but drawn as 1.5px strokes on a faint round plate they keep their
topology — parallel lines, columns, a fork, a wave, spokes, nested
rings — and topology is what the eye tells apart at that size. Every
option below carries a **nodal-line navigation set** on that principle
(the same figures, at each option's stroke weight), shown in its rail
in the mockups, with a generic line set beside it for comparison.

### 2.7 How state is communicated

State is told five different ways depending on where you look:

| place | how | file |
|---|---|---|
| rail | 8px dot, filled or outlined, spinning for running | `.dot-*` in `style.css` |
| list row | 11px dot + nothing (the `.badge-icon` has no label) and a *separate* outlined “reason” chip (`unapproved`, `live run`, `question`, `on merge queue`, `blocked on task`) | `.badge`, `.task-reason` |
| task page | a filled pill with the task number in the state colour, plus a dot+label badge in the side column | `.task-id-bubble`, `.badge` |
| board | column heading + count; cards carry the reason chip | `TaskBoard.jsx` |
| CI table | an MUI chip filled green/red with “Success”/“running” text | `CIPage.jsx` |

The *reason* chip is the most informative element on the row and the
least emphasised. Running is a spinner in the rail and a static dot in
the row. Blocked is a chip (`blocked on task`) with no colour. Merging
and completed share blue. Failed is red but so is the “Close” button
next to it on the task page. A reader cannot tell, from colour alone,
a task that needs them from one that is working from one that is done
— which is the one question this UI exists to answer.

---

## 3. Option 1 — Settle {#D-1}

*Conservative refresh. Same brand, same shell, same lowercase voice.*

**Concept.** Keep everything that is distinctive — the warm palette, the
bronze/wheat mark, the rail-and-pane shell, the lowercase voice — and
settle the details: one type scale, one chip system on the warm tokens,
AA contrast everywhere, a shorter header, a tighter rail, and a
consistent dot-plus-label voice for state. Nothing moves; everything
lines up. A reader who knows grain today would not notice a redesign,
only that it feels finished.

### Layout

- Page header 77px → **56px**; title 15/600; the toolbar slot keeps its
  height but loses the empty band above and below.
- Rail stays 232px; rows 38px → **34px**, counts in tabular numerals
  in `--text-tertiary`, active row filled with `--accent-subtle`. Section
  eyebrows (“pinned”, “tasks”) become 11.5px uppercase-tracked — the
  one place lowercase is given up, because an eyebrow is a label not a
  word.
- List rows keep their structure but chips drop to **22px, 6px radius,
  outlined on the warm tokens**, so the title is the darkest thing on
  the row. Field chips go borderless-on-canvas; the reason chip is
  outlined in `--border-strong` and set in the text colour.
- Task page: the number bubble becomes an **outlined** pill in the
  state colour (readable on every state), the state badge in the side
  column grows to 12.5/500. No structural change.
- Dark mode: panels one step above canvas; MUI's elevation gradient off.

### Colour system

Tokens as today, with four corrections (bold) and the states re-tuned to
equal visual weight. All pairs below pass AA; the full table is in the
appendix.

| token | light | dark | notes |
|---|---|---|---|
| canvas | `#f7f6f3` | `#0f1013` | unchanged |
| surface | `#ffffff` | **`#15171b`** | dark raised by one step |
| border / strong | **`#e2ded5` / `#cfc9bc`** | **`#2a2d33` / `#3c4048`** | slightly firmer |
| text | `#24221d` | `#ebe8e1` | 15.9 / 14.7 |
| text-secondary | **`#5c574d`** | **`#aaa69a`** | 7.2 / 7.4 |
| text-tertiary | **`#6b655a`** | **`#8d897e`** | **5.8 / 5.1** (was 2.8 / 3.6) |
| accent / hover | `#8a6a2e` / `#6d5324` | `#d9a441` / `#e8b962` | unchanged; 5.0 / 8.0 as text on surface |
| danger | **`#c03a2f`** | **`#f0786d`** | 5.4 / 6.5 |
| state · proposed | **`#a35406`** | `#f5a524` | 5.5 / 8.8 |
| state · queued | `#6e6a60` | `#a39f94` | 5.4 / 6.8 |
| state · running | = accent | = accent | kept, see Option 2 for the alternative |
| state · interactive | `#0b6f78` | `#45c3cf` | 5.9 / 8.5 |
| state · awaiting reply | **`#7e22ce`** | `#c89bfc` | 7.0 / 8.2 |
| state · awaiting submit | **`#c2410c`** | `#fb9a4a` | **5.2 / 8.4** (was 3.8) |
| state · completed / merging | **`#0b5fc0`** | `#6fb1ff` | 6.2 / 8.0 |
| state · merged | `#15803d` | `#4cc466` | 5.0 / 8.0 |
| state · deferred | `#55657a` | `#98a8bd` | 6.0 / 7.4 |
| state · closed | **`#7b766a`** | `#8d897e` | **4.5 / 5.1** (was 3.0) |

### Typography

System stack unchanged (`-apple-system, Segoe UI, Roboto, …`; no font
to license or load). The scale collapses to **11.5 · 12 · 12.5 · 13.5 ·
14 · 15 · 16 · 20**, each on a 4px line grid:

| role | size / weight | replaces |
|---|---|---|
| eyebrow (rail sections, fieldset legends) | 11.5 / 600, +4% tracking, uppercase | 11.52, 12 |
| meta (numbers, counts, timestamps, chips) | 12 – 12.5 / 400–500 | 10.88, 11.2, 12.48, 12.8, 13 |
| body, rail items, table cells | 13.5 / 400 | 13, 13.12, 13.6 |
| row title | 14 / 500 | 14.4 / 400 |
| page title, pane h2 | 15 / 600 | 16 / 600 |
| settings h1 | 20 / 600 | 24 |

### Iconography

- **Mark**: unchanged in the app. New **app-icon tile** — the rosette at
  78% on a rounded square (sand `#f3ecdb` for light, ink `#17130a` for
  dark) — for the iOS icon, PWA manifest and the README. (The mockups
  below show the tile in the rail only to preview it at size.)
- **Status set** (12 glyphs, 16px grid, 1.5px round stroke, drawn in
  `currentColor` so the state token colours them): running, queued,
  proposed, awaiting, submit, merging, merged, failed, blocked,
  deferred, closed, interactive.
- **Navigation set — nodal lines** (15 glyphs, 1.5px, each on a faint
  7px-radius plate): inbox, tasks, boards, sessions, director,
  processes, CI, repos, metrics, settings, system, alerts, staging,
  new. These replace both the filled Chladni item glyphs in the rail
  and the Material icons, so the rail has one vocabulary. The filled
  figures stay for the hero and the 32px mark.
- A **generic line set** of the same 12 destinations is included for
  comparison (`nav-*.svg`); it is the fallback if the nodal set is
  judged too subtle.

![marks, nodal-line and generic navigation icons, status icons, at 16–128px on the option's grounds](option-1/icons-preview.png)

| mark (light) | mark (dark) | status icons (light) | status icons (dark) |
|---|---|---|---|
| ![](option-1/mark-light.svg) | ![](option-1/mark-dark.svg) | ![](option-1/status-icons-light.svg) | ![](option-1/status-icons-dark.svg) |

| nodal-line navigation icons (light) | nodal-line navigation icons (dark) |
|---|---|
| ![](option-1/nodal-icons-light.svg) | ![](option-1/nodal-icons-dark.svg) |

Individual files: [`option-1/nodal-*.svg`](option-1/), [`option-1/status-*.svg`](option-1/), [`option-1/nav-*.svg`](option-1/).

### Mockups

Rendered over the real UI with [`tools/option-1.css`](tools/option-1.css)
(+ [`mui-remap.css`](tools/mui-remap.css), [`mock-common.js`](tools/mock-common.js)).

| screen | desktop · light | desktop · dark | phone · light | phone · dark |
|---|---|---|---|---|
| Task list | ![](option-1/tasks-desktop-light.png) | ![](option-1/tasks-desktop-dark.png) | ![](option-1/tasks-phone-light.png) | ![](option-1/tasks-phone-dark.png) |
| Board | ![](option-1/board-desktop-light.png) | ![](option-1/board-desktop-dark.png) | ![](option-1/board-phone-light.png) | ![](option-1/board-phone-dark.png) |
| Task detail | ![](option-1/task-desktop-light.png) | ![](option-1/task-desktop-dark.png) | ![](option-1/task-phone-light.png) | ![](option-1/task-phone-dark.png) |
| New task | ![](option-1/new-task-desktop-light.png) | ![](option-1/new-task-desktop-dark.png) | ![](option-1/new-task-phone-light.png) | ![](option-1/new-task-phone-dark.png) |
| CI runs | ![](option-1/ci-desktop-light.png) | ![](option-1/ci-desktop-dark.png) | ![](option-1/ci-phone-light.png) | ![](option-1/ci-phone-dark.png) |
| Merge queue | ![](option-1/merge-queue-desktop-light.png) | ![](option-1/merge-queue-desktop-dark.png) | ![](option-1/merge-queue-phone-light.png) | ![](option-1/merge-queue-phone-dark.png) |
| Inbox | ![](option-1/inbox-desktop-light.png) | ![](option-1/inbox-desktop-dark.png) | ![](option-1/inbox-phone-light.png) | ![](option-1/inbox-phone-dark.png) |
| Capabilities | ![](option-1/capabilities-desktop-light.png) | ![](option-1/capabilities-desktop-dark.png) | ![](option-1/capabilities-phone-light.png) | ![](option-1/capabilities-phone-dark.png) |
| Metrics | ![](option-1/metrics-desktop-light.png) | ![](option-1/metrics-desktop-dark.png) | ![](option-1/metrics-phone-light.png) | ![](option-1/metrics-phone-dark.png) |

### Effort and risk

- **Effort: small — 2 to 4 days.** Token values in `style.css` `:root`
  and `theme.js` (edited together, as their comments ask); a `MuiChip`
  style override; `.page-header` height; rail row padding; `.task-id-bubble`;
  a handful of rem sizes collapsed to the scale. The icon set is a new
  `ui/src/brand/line-icons.js` plus swapping ~28 `@mui/icons-material`
  imports.
- **Risk: low.** No structure moves, so e2e specs that assert layout
  (`alerts-panel-layout.spec.js`, `phone.spec.js`) survive; snapshot
  specs on exact pixel heights (header 77px) need updating. The brand
  argument in `docs/brand.md` is untouched.
- **Touches:** `ui/src/style.css`, `ui/src/theme.js`, `components/Sidebar.jsx`
  (row padding), `components/TaskList*`/`App.jsx` chip variants,
  `index.html` (icon tile), new icon module.

---

## 4. Option 2 — Ledger {#D-2}

*Moderate redesign. Neutral paper, cool ink, one bronze accent; Inter +
JetBrains Mono; sentence case; denser; state as a pill.*

**Concept.** grain is an instrument you read many times a day: a ledger
of what agents are doing and what needs you. Option 2 makes the page
quieter and the state louder. The warm tint leaves the neutrals — paper
becomes a true light grey, ink a near-black with a little blue — so the
one warm thing on the page is the bronze accent, which is now reserved
for *grain acting* (primary action, running state, active item). Type
moves to Inter with JetBrains Mono for identifiers, rows get denser,
the header shrinks, and the rail becomes a grouped index with pill
counts. Forced lowercase goes; sentence case returns the capital that
separates a heading from a label.

### Layout

- **Rail 232 → 208px**, on the canvas colour rather than white so the
  page is the raised surface. Rows 30px, 13/500; counts in 11.5px
  pills; section eyebrows 11px uppercase. The active item is a white
  card with a 1px border and a 1px shadow — the one lifted thing in the
  rail.
- **Header 77 → 48px.** Title 15/600 in sentence case; the slot keeps
  the filters.
- **List rows 58 → 44px.** Title first at 13.5/500; number in mono with
  a `#` prefix; **chips collapse**: the first field chip and the first
  grant stay, the rest fold into a `+N` chip with the full list on
  hover (in the mock [`mock-common.js`](tools/mock-common.js) does this;
  in the implementation `TaskRow` would group grants). The reason chip
  stays outlined and becomes the row's state label.
- **Task page**: side column becomes a bordered card on the surface;
  question and description cards get a 1px border and a 6px radius so
  they read as cards, not tinted paragraphs; `h3` section labels become
  uppercase eyebrows so “Timeline” stops competing with the content.
- **Tables** (CI, merge queue, capabilities): 13px cells, 8px vertical
  padding, uppercase 11.5px heads — roughly 30% more rows per screen.
- **Phone**: unchanged structure; the denser rows and smaller header
  recover ~100px of list.

### Colour system

| token | light | dark | contrast (light / dark) |
|---|---|---|---|
| canvas | `#f3f3f1` | `#121417` | — |
| surface | `#ffffff` | `#191c20` | — |
| surface-2 (cards, inputs) | `#f8f8f7` | `#1f2328` | — |
| border / strong | `#e3e3df` / `#c9c9c3` | `#2c3036` / `#3d424a` | — |
| text | `#1b1c1e` | `#e7e8ea` | 17.1 / 13.9 |
| text-secondary | `#55575c` | `#a3a7ae` | 7.2 / 7.1 |
| text-tertiary | `#6b6e75` | `#878c94` | 5.1 / 5.1 |
| accent / hover | `#7a5a22` / `#5e4517` | `#e0b45c` / `#ebc477` | 6.3 / 8.8 as text; white / ink on it 6.3 / 9.5 |
| accent-subtle | `#f3ecdc` | `#2b2515` | — |
| danger | `#c81e1e` | `#f87171` | 5.7 / 6.2 |
| state · proposed | `#b4530a` | `#f5a524` | 5.0 / 8.4 |
| state · queued | `#6b6e75` | `#9aa0a8` | 5.1 / 6.5 |
| state · running | = accent | = accent | the only warm state |
| state · interactive | `#0b7285` | `#22c1d6` | 5.6 / 7.9 |
| state · awaiting reply | `#7c3aed` | `#c084fc` | 5.7 / 6.5 |
| state · awaiting submit | `#c2410c` | `#fb923c` | 5.2 / 7.5 |
| state · completed / merging | `#1d4ed8` | `#60a5fa` | 6.7 / 6.7 |
| state · merged | `#15803d` | `#4ade80` | 5.0 / 9.8 |
| state · deferred | `#475569` | `#94a3b8` | 7.6 / 6.7 |
| state · closed | `#6f727a` | `#878c94` | 4.8 / 5.1 |

The state family is chosen so each hue sits at roughly the same
lightness (L* ≈ 42 light, ≈ 72 dark) — purple and coral stop shouting,
blue and green come up to meet them — and so that every state passes as
*text*, which is what lets the reason chip be set in its state colour.

### Typography

**Inter** (SIL OFL) for UI, **JetBrains Mono** (SIL OFL) for identifiers
— task numbers, shas, branch names, timestamps, `declared-key` rows.
Both self-hosted (`woff2`, ~100KB each subsetted); the mock loads them
from Google Fonts for convenience only.

| role | face | size / weight | line |
|---|---|---|---|
| eyebrow | Inter | 11 / 600, +6% tracking, uppercase | 16 |
| meta / identifiers | JetBrains Mono | 12 / 400 | 16 |
| rail item, chip, table cell | Inter | 13 / 500 (chips 11.5) | 20 |
| body, row title | Inter | 13.5 / 400 (title 500) | 20 |
| page title, pane h2 | Inter | 15 / 600, −1% tracking | 24 |
| settings h1 | Inter | 18 / 600, −1% tracking | 28 |

Sentence case throughout; `text-transform` only on eyebrows.

### Iconography

- **Mark**: the rosette as a **monoline** on the circle clip — the same
  figure at the same optical size, in the ledger's own line weight, so
  it sits beside the line icons in the rail instead of being the one
  filled thing. The stroke is 7 units of the 128 box, which is 1.75px
  at the rail's 32px (a first cut at 3.5 units was sub-pixel there).
  The filled mark stays the favicon and the hero.
- **Nodal-line navigation set and status set**: as Option 1, at 1.5px,
  for a 5px radius system.

![marks, nodal-line and generic navigation icons, status icons, at 16–128px on the option's grounds](option-2/icons-preview.png)

| mark (light) | mark (dark) | status icons (light) | status icons (dark) |
|---|---|---|---|
| ![](option-2/mark-light.svg) | ![](option-2/mark-dark.svg) | ![](option-2/status-icons-light.svg) | ![](option-2/status-icons-dark.svg) |

| nodal-line navigation icons (light) | nodal-line navigation icons (dark) |
|---|---|
| ![](option-2/nodal-icons-light.svg) | ![](option-2/nodal-icons-dark.svg) |

### Mockups

Rendered with [`tools/option-2.css`](tools/option-2.css).

| screen | desktop · light | desktop · dark | phone · light | phone · dark |
|---|---|---|---|---|
| Task list | ![](option-2/tasks-desktop-light.png) | ![](option-2/tasks-desktop-dark.png) | ![](option-2/tasks-phone-light.png) | ![](option-2/tasks-phone-dark.png) |
| Board | ![](option-2/board-desktop-light.png) | ![](option-2/board-desktop-dark.png) | ![](option-2/board-phone-light.png) | ![](option-2/board-phone-dark.png) |
| Task detail | ![](option-2/task-desktop-light.png) | ![](option-2/task-desktop-dark.png) | ![](option-2/task-phone-light.png) | ![](option-2/task-phone-dark.png) |
| New task | ![](option-2/new-task-desktop-light.png) | ![](option-2/new-task-desktop-dark.png) | ![](option-2/new-task-phone-light.png) | ![](option-2/new-task-phone-dark.png) |
| CI runs | ![](option-2/ci-desktop-light.png) | ![](option-2/ci-desktop-dark.png) | ![](option-2/ci-phone-light.png) | ![](option-2/ci-phone-dark.png) |
| Merge queue | ![](option-2/merge-queue-desktop-light.png) | ![](option-2/merge-queue-desktop-dark.png) | ![](option-2/merge-queue-phone-light.png) | ![](option-2/merge-queue-phone-dark.png) |
| Inbox | ![](option-2/inbox-desktop-light.png) | ![](option-2/inbox-desktop-dark.png) | ![](option-2/inbox-phone-light.png) | ![](option-2/inbox-phone-dark.png) |
| Capabilities | ![](option-2/capabilities-desktop-light.png) | ![](option-2/capabilities-desktop-dark.png) | ![](option-2/capabilities-phone-light.png) | ![](option-2/capabilities-phone-dark.png) |
| Metrics | ![](option-2/metrics-desktop-light.png) | ![](option-2/metrics-desktop-dark.png) | ![](option-2/metrics-phone-light.png) | ![](option-2/metrics-phone-dark.png) |

### Effort and risk

- **Effort: medium — 1.5 to 3 weeks.** Everything in Option 1, plus:
  fonts (self-host, `@font-face`, `font-display: swap`, update
  `theme.typography`), the `text-transform` removal (a dozen overrides
  in `theme.js` and the lowercase block in `style.css`, and a pass over strings that were
  written lowercase *because* the CSS would lowercase them anyway),
  chip grouping in `TaskRow`/board cards, rail width (`SIDEBAR_WIDTH`,
  which `Overlay.jsx`'s pane and the 720px/952px breakpoints in
  `style.css` depend on), table density in `CIPage.jsx`, `RepoPage.jsx`,
  `CapabilitiesPanel.jsx`.
- **Risk: medium.** The lowercase voice is a brand decision with a
  history (`theme.js` lowercases on purpose); dropping it needs the
  owner's yes. Font loading adds a FOUT path to test. Rail width changes
  touch every spec that measures the pane. The chip grouping changes
  what the list shows, so `App.tasks.test.jsx` and the e2e walkthroughs
  that look for grant chips by text need updating.
- **Touches:** `ui/src/style.css`, `ui/src/theme.js`, `index.html`
  (font preload), `ui/public/fonts/`, `components/Sidebar.jsx`,
  `components/Overlay.jsx` (pane offset), `TaskRow`/`TaskBoard.jsx`,
  `CIPage.jsx`, `RepoPage.jsx`, `CapabilitiesPanel.jsx`, settings rail
  styling, `docs/brand.md` (the monoline mark).

---

## 5. Option 3 — Plate

*Bold redesign. The Chladni plate made literal: an ink rail in both
themes with wheat glyphs, sand-coloured paper, IBM Plex, loud state.*

**Concept.** The brand's story is sand on a vibrating plate. Option 3
puts the plate on screen: the rail is a dark warm-ink panel in *both*
themes, the mark and the active item glow wheat on it, and the page
beside it is sand-coloured paper (deep ink in dark mode). State stops
being a dot: every row carries a 4px stripe in its state colour on the
left edge, the reason chip becomes a filled state pill, the pending
question card takes a purple edge. IBM Plex Sans and Mono give it an
engineered, slightly typewritten voice to match. It is the version
that would make a screenshot recognisably grain from across the room.

### Layout

- **Rail 224px, ink `#1b1916` in light, `#0c0d10` in dark**, with a
  1px warm border. Rows 36px, 14/500, 18px glyphs in `--rail-muted`;
  the active item has a 3px wheat bar on its left and wheat text. The
  primary button is wheat on ink with ink text. Section eyebrows 11px
  tracked uppercase in the muted tone. The phone AppBar is the same
  plate.
- **Header 60px** on the canvas (not white), title 18/600, page glyph
  in the accent at 22px.
- **List rows 52px on surface over a canvas gutter**, a 4px state
  stripe at the left edge (`.task-row::before`, keyed off the row's
  state class), title 15/500, number in Plex Mono. Chips collapse as in
  Option 2; the reason chip is **filled** in its state colour with
  white (light) or ink (dark) text — `question` is purple, `live run`
  bronze, `on merge queue` blue, `unapproved` amber, the rest ink.
- **Task page**: id pill filled in the state colour at 13/600; the
  pending-question card gets a 4px purple left edge; the description
  and the side column become bordered 10–12px-radius cards on the
  surface; eyebrow `h3`s.
- **Cards and tables** take a 12px radius and a 1px border, no shadow:
  paper on paper.

### Colour system

| token | light | dark | contrast (light / dark) |
|---|---|---|---|
| rail | `#1b1916` | `#0c0d10` | — |
| rail-2 (hover, active) | `#262320` | `#15161a` | — |
| rail text / muted | `#ece7dc` / `#a69e8e` | `#ece7dc` / `#a09a8c` | 14.2 / 15.8 · muted 6.6 / 6.9 |
| rail accent (wheat) | `#e3b65a` | `#e3b65a` | 9.3 / 10.3 on rail |
| canvas (sand / ink) | `#f4f0e8` | `#0c0d10` | — |
| surface | `#fffdf8` | `#15161a` | — |
| border / strong | `#e4ddcf` / `#cdc3ad` | `#262930` / `#3a3e47` | — |
| text | `#1b1916` | `#ece7dc` | 17.2 / 14.7 |
| text-secondary | `#5a5447` | `#aaa393` | 7.4 / 7.2 |
| text-tertiary | `#6e675a` | `#8c8577` | 5.5 / 4.9 |
| accent / hover | `#7d5a1c` / `#5f4413` | `#e3b65a` / `#efc877` | 6.2 / 9.6 as text; white / ink on it 6.3 / 9.7 |
| danger | `#b3261e` | `#f4776c` | 6.4 / 6.6 |
| state · proposed | `#a8530a` | `#f6a623` | 5.3 / 8.9 |
| state · queued | `#6e675a` | `#a09a8c` | 5.5 / 6.5 |
| state · running | = accent | = accent | |
| state · interactive | `#0b6f78` | `#3fc3cf` | 5.8 / 8.5 |
| state · awaiting reply | `#6d28d9` | `#c99cff` | 7.0 / 8.3 |
| state · awaiting submit | `#b83a0b` | `#fb9a4a` | 5.7 / 8.5 |
| state · completed / merging | `#1a56c4` | `#6fb1ff` | 6.5 / 8.1 |
| state · merged | `#167a3c` | `#4fcf72` | 5.3 / 9.0 |
| state · deferred | `#4b5a6e` | `#9aa9bd` | 6.9 / 7.6 |
| state · closed | `#736c5d` | `#8c8577` | 5.1 / 4.9 |

Filled pills use white text on the light state colours (lowest pair:
white on proposed `#a8530a`, 5.4 : 1) and ink `#0c0d10` on the dark
ones (lowest: ink on awaiting `#c99cff`, 9.0 : 1).

### Typography

**IBM Plex Sans** and **IBM Plex Mono** (SIL OFL), self-hosted.

| role | face | size / weight |
|---|---|---|
| eyebrow | Plex Sans | 11–12 / 600, +8% tracking, uppercase |
| identifiers, timestamps, version | Plex Mono | 12 / 400 |
| rail item, chip | Plex Sans | 14 / 500 (chips 12 / 500) |
| body | Plex Sans | 14 / 400 |
| row title | Plex Sans | 15 / 500 |
| page title | Plex Sans | 18 / 600, −1% tracking |
| settings h1 | Plex Sans | 22 / 600 |

### Iconography

- **Mark — “the plate”**: a 24px-radius square a step lighter than the
  rail (`#2b2723`) with a faint wheat edge, carrying the rosette in
  wheat at 80% and four faint grains in the corners (the plate's
  mounting points). One file serves both themes because the plate is
  ink in both; it is also the app icon as-is. (The first cut was the
  rail's own ink, so on the ink rail the tile had no edge.)
- **Nodal-line navigation set and status set**: the same figures at a
  **2px** stroke for an 8px-radius system, shown on the ink rail where
  they live.

![marks, nodal-line and generic navigation icons, status icons, at 16–128px on the ink rail](option-3/icons-preview.png)

| mark (both themes) | status icons (light) | status icons (dark) |
|---|---|---|
| ![](option-3/mark-light.svg) | ![](option-3/status-icons-light.svg) | ![](option-3/status-icons-dark.svg) |

| nodal-line navigation icons on the rail | generic line icons on the rail |
|---|---|
| ![](option-3/nodal-icons-light.svg) | ![](option-3/nav-icons-light.svg) |

### Mockups

Rendered with [`tools/option-3.css`](tools/option-3.css).

| screen | desktop · light | desktop · dark | phone · light | phone · dark |
|---|---|---|---|---|
| Task list | ![](option-3/tasks-desktop-light.png) | ![](option-3/tasks-desktop-dark.png) | ![](option-3/tasks-phone-light.png) | ![](option-3/tasks-phone-dark.png) |
| Board | ![](option-3/board-desktop-light.png) | ![](option-3/board-desktop-dark.png) | ![](option-3/board-phone-light.png) | ![](option-3/board-phone-dark.png) |
| Task detail | ![](option-3/task-desktop-light.png) | ![](option-3/task-desktop-dark.png) | ![](option-3/task-phone-light.png) | ![](option-3/task-phone-dark.png) |
| New task | ![](option-3/new-task-desktop-light.png) | ![](option-3/new-task-desktop-dark.png) | ![](option-3/new-task-phone-light.png) | ![](option-3/new-task-phone-dark.png) |
| CI runs | ![](option-3/ci-desktop-light.png) | ![](option-3/ci-desktop-dark.png) | ![](option-3/ci-phone-light.png) | ![](option-3/ci-phone-dark.png) |
| Merge queue | ![](option-3/merge-queue-desktop-light.png) | ![](option-3/merge-queue-desktop-dark.png) | ![](option-3/merge-queue-phone-light.png) | ![](option-3/merge-queue-phone-dark.png) |
| Inbox | ![](option-3/inbox-desktop-light.png) | ![](option-3/inbox-desktop-dark.png) | ![](option-3/inbox-phone-light.png) | ![](option-3/inbox-phone-dark.png) |
| Capabilities | ![](option-3/capabilities-desktop-light.png) | ![](option-3/capabilities-desktop-dark.png) | ![](option-3/capabilities-phone-light.png) | ![](option-3/capabilities-phone-dark.png) |
| Metrics | ![](option-3/metrics-desktop-light.png) | ![](option-3/metrics-desktop-dark.png) | ![](option-3/metrics-phone-light.png) | ![](option-3/metrics-phone-dark.png) |

### Effort and risk

- **Effort: large — 4 to 6 weeks.** Everything in Option 2, plus: a
  rail that is dark in the light theme (every rail component needs its
  own token set: `Sidebar.jsx`, `PhoneNav`, `AlertsButton.jsx`, the
  pinned section, the staging pin, the drawer), the state stripe on
  rows and cards (a state class on `.task-row`, `TaskBoard` cards,
  inbox rows), filled reason pills (a colour-per-reason map that does
  not exist today), the task page cards, and the mark/app-icon
  pipeline (`npm run brand` exports a new tile).
- **Risk: high.** A dark rail in light mode is a strong opinion some
  readers dislike; it also doubles the surfaces every overlay and
  banner has to be right on (the red-base banner now sits across an ink
  rail and a sand page). The filled pills raise the colour volume of
  the list considerably — the thing the critique says to *reduce* —
  and only work if the grant chips are collapsed first. Playwright
  specs that assert rail colours or row geometry change wholesale.
- **Touches:** all of Option 2 plus `components/Sidebar.jsx`,
  `components/PhoneNav*`, `components/AlertsButton.jsx`,
  `components/TaskBoard.jsx`, `components/InboxPage.jsx`,
  `components/DetailOverlay.jsx`, `ui/scripts/export-brand-assets.mjs`,
  `docs/brand.md`.

---

## Ranking and recommendation {#D-3}

| rank | option | why |
|---|---|---|
| **1** | **Option 2 — Ledger** | It fixes the actual problems the critique found — density, a non-scale of type sizes, two chip systems, contrast, state that does not stand out — and it does so by *subtracting*: the warm tint leaves the neutrals so the bronze means something again; grants fold into a count so the title is what you read; the header and rows give back ~25% of the screen to content. The dark mode gains a real lightness ramp. It is a recognisable step up without betting the brand on a dark rail. The cost that matters is the lowercase voice, and that is a decision the owner should make with the mockups side by side. |
| **2** | **Option 1 — Settle** | Every change in it is correct and cheap, and it is the right first increment whatever is chosen after: AA contrast, token-coloured chips, a shorter header, a consistent status voice, an app-icon tile. On its own it leaves the density and the twelve-font-size texture mostly as they are, so the UI will feel tidier rather than better. |
| **3** | **Option 3 — Plate** | The most *grain* of the three and the best story — the plate, the sand, the wheat glow — but it spends its boldness on the rail, which is the part of the screen a reader looks at least, and it raises the colour volume of the list when the list needs to get quieter. The state stripe and the filled state pill are genuinely good and should be borrowed by whichever option ships; the ink rail should wait until the typography and density are settled, because a dark rail beside today's chip-heavy rows makes the page busier, not clearer. |

**Recommendation.** Do Option 1 now as a one-PR token-and-chip fix
(it is a strict subset of Option 2 in effect, and it clears every AA
failure measured above). Then do Option 2 in two steps — type and
density first, the lowercase decision second — and lift Option 3's
4px state stripe into it on the list, board and inbox rows, because it
is the single clearest way the review found to answer “what state is
this in” without reading. Leave the ink rail and the plate mark for a
brand refresh if the appetite is there afterwards.

Things that any option should do, in case the ranking is overruled:

1. `--text-tertiary` to ≥ 4.5 : 1 in both themes; `--state-closed` to
   ≥ 3 : 1 as a dot; `--state-awaiting-submit` to ≥ 4.5 : 1 as text.
2. Chips on the warm tokens, not MUI's `#bdbdbd` / `rgba(0,0,0,.08)` /
   `rgba(255,255,255,.16)`; and fold repeated grant chips.
3. A type scale with at most seven sizes, each on a 4px line grid.
4. One status voice (icon + label) used in the rail, the row, the
   board card, the inbox row and the task header alike, with
   *running* visually distinct from the primary action.
5. Keep the rail live under overlays, or make the pane a route.

---

## Appendix

### Method and tools

Everything under [`tools/`](tools/) is throwaway and is not wired into
any build, test or make target.

| file | what |
|---|---|
| [`shoot.mjs`](tools/shoot.mjs) | Captures the nine screens from a running `grain demo` at both widths and themes; `--css`/`--js` inject an option over the live page; `--mark-*` swaps the mark; `--census` writes the style census beside the PNGs. Resolves Playwright from `ui/node_modules`. |
| [`seed-ci.mjs`](tools/seed-ci.mjs) | Starts CI runs through the CI page's own form on an `-observability` demo and finishes some with the demo's `/api/e2e/ci/finish` control. |
| [`census.mjs`](tools/census.mjs), [`censusFn.mjs`](tools/censusFn.mjs) | The computed-style census: every visible text node's size, weight, colour, effective background and WCAG ratio; counts of chips, SVGs, buttons, lowercase-transformed nodes. |
| [`contrast.py`](tools/contrast.py) | The palettes of the current UI and the three options with WCAG ratios; prints the tables below. |
| [`icons.py`](tools/icons.py) | Writes the marks, the nodal-line and generic navigation icon sets, the status set and their contact sheets for each option; `nodal-icons.json` is what the mock injects into the rail. |
| [`icon-preview.mjs`](tools/icon-preview.mjs) | Renders each option's marks and icon sets at 16–128px on its grounds to `option-N/icons-preview.png`. |
| [`mui-remap.css`](tools/mui-remap.css) | Points MUI's emotion-generated colours at the CSS custom properties so an option only restates tokens. |
| [`option-{1,2,3}.css`](tools/), [`mock-common.js`](tools/mock-common.js) | The three options as overrides, and the shared page script (mark swap, chip folding, sentence-casing of lowercased headings). |
| [`run-options.sh`](tools/run-options.sh) | Renders all three options. |

To regenerate: build the frontend and `grain`, start
`grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -observability -merge-queue-states -merge-rules -review-questions`,
write the access code to `/home/debian/demo/code.txt` (or pass
`--code-file`), then `node tools/seed-ci.mjs`, `node tools/shoot.mjs --out current`,
`tools/run-options.sh`, and `pngquant --quality=60-85 --ext .png --force */*.png`.

The mockups' web fonts (Inter, JetBrains Mono, IBM Plex) were loaded
from Google Fonts at render time; an implementation would self-host
them. Where a mock could not reach something through CSS alone — the
grant chips folding into `+N`, the sentence-cased page titles, the
swapped mark, the nodal-line icons in the rail and page header —
[`mock-common.js`](tools/mock-common.js) edits the DOM after load; those
are the places an implementation changes a component rather than a
token. The mark swap is limited to the rail's and the phone bar's
mark; the 20px marks the task list uses for the repair indicator keep
the brand's own figure. (The first published render of these mockups
showed empty tiles and, for Option 2, no mark at all: `icons.py` had
read the brand SVG's `clipPath id="f"` as the rosette's path, so every
generated mark carried an empty `d`. The extraction is fixed and
`tools/icon-preview.mjs` now renders the marks at 16–128px so a blank
one is visible before it reaches a mockup.)

### Measured result on the mockups

The same census that found the current UI's failures was run over every
mockup (`option-*/census.json`): **0 text/background pairs below AA on
all 36 captures of each option**, against 35 failing nodes on the
current backlog's first screen in light mode.

### Full contrast tables

Generated by `python3 tools/contrast.py all`. “as text” is the 4.5 : 1
AA threshold for normal text; “as dot” is the 3 : 1 threshold for
non-text indicators.

##### current — light

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#24221d` on `#f7f6f3` | 14.70 | pass (4.5) |
| text / surface | `#24221d` on `#ffffff` | 15.89 | pass (4.5) |
| secondary / canvas | `#6e6a60` on `#f7f6f3` | 4.99 | pass (4.5) |
| secondary / surface | `#6e6a60` on `#ffffff` | 5.39 | pass (4.5) |
| tertiary / canvas | `#9a9588` on `#f7f6f3` | 2.76 | FAIL (4.5) |
| tertiary / surface | `#9a9588` on `#ffffff` | 2.99 | FAIL (4.5) |
| accent text / surface | `#8a6a2e` on `#ffffff` | 5.02 | pass (4.5) |
| on-accent / accent (button) | `#ffffff` on `#8a6a2e` | 5.02 | pass (4.5) |
| state proposed as text / surface | `#b45309` on `#ffffff` | 5.02 | pass (4.5) |
| state queued as text / surface | `#6e6a60` on `#ffffff` | 5.39 | pass (4.5) |
| state interactive as text / surface | `#0e7c86` on `#ffffff` | 4.95 | pass (4.5) |
| state awaiting as text / surface | `#9333ea` on `#ffffff` | 5.38 | pass (4.5) |
| state awaiting_submit as text / surface | `#d95f02` on `#ffffff` | 3.76 | FAIL (4.5) |
| state completed as text / surface | `#0969da` on `#ffffff` | 5.19 | pass (4.5) |
| state merging as text / surface | `#0969da` on `#ffffff` | 5.19 | pass (4.5) |
| state merged as text / surface | `#1a7f37` on `#ffffff` | 5.08 | pass (4.5) |
| state deferred as text / surface | `#64748b` on `#ffffff` | 4.76 | pass (4.5) |
| state closed as text / surface | `#9a9588` on `#ffffff` | 2.99 | FAIL (4.5) |
| state danger as text / surface | `#d0453a` on `#ffffff` | 4.57 | pass (4.5) |
| state proposed as dot / canvas | `#b45309` on `#f7f6f3` | 4.65 | pass (3.0) |
| state queued as dot / canvas | `#6e6a60` on `#f7f6f3` | 4.99 | pass (3.0) |
| state interactive as dot / canvas | `#0e7c86` on `#f7f6f3` | 4.58 | pass (3.0) |
| state awaiting as dot / canvas | `#9333ea` on `#f7f6f3` | 4.98 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#d95f02` on `#f7f6f3` | 3.48 | pass (3.0) |
| state completed as dot / canvas | `#0969da` on `#f7f6f3` | 4.80 | pass (3.0) |
| state merging as dot / canvas | `#0969da` on `#f7f6f3` | 4.80 | pass (3.0) |
| state merged as dot / canvas | `#1a7f37` on `#f7f6f3` | 4.70 | pass (3.0) |
| state deferred as dot / canvas | `#64748b` on `#f7f6f3` | 4.40 | pass (3.0) |
| state closed as dot / canvas | `#9a9588` on `#f7f6f3` | 2.76 | FAIL (3.0) |
| state danger as dot / canvas | `#d0453a` on `#f7f6f3` | 4.23 | pass (3.0) |

##### current — dark

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#e9e7e1` on `#0f1013` | 15.38 | pass (4.5) |
| text / surface | `#e9e7e1` on `#14161a` | 14.65 | pass (4.5) |
| secondary / canvas | `#9b988f` on `#0f1013` | 6.60 | pass (4.5) |
| secondary / surface | `#9b988f` on `#14161a` | 6.28 | pass (4.5) |
| tertiary / canvas | `#6f6c65` on `#0f1013` | 3.63 | FAIL (4.5) |
| tertiary / surface | `#6f6c65` on `#14161a` | 3.46 | FAIL (4.5) |
| accent text / surface | `#d9a441` on `#14161a` | 8.05 | pass (4.5) |
| on-accent / accent (button) | `#17130a` on `#d9a441` | 8.23 | pass (4.5) |
| state proposed as text / surface | `#f59e0b` on `#14161a` | 8.43 | pass (4.5) |
| state queued as text / surface | `#9b988f` on `#14161a` | 6.28 | pass (4.5) |
| state interactive as text / surface | `#3fb8c4` on `#14161a` | 7.65 | pass (4.5) |
| state awaiting as text / surface | `#c084fc` on `#14161a` | 6.85 | pass (4.5) |
| state awaiting_submit as text / surface | `#fb923c` on `#14161a` | 8.00 | pass (4.5) |
| state completed as text / surface | `#58a6ff` on `#14161a` | 7.17 | pass (4.5) |
| state merging as text / surface | `#58a6ff` on `#14161a` | 7.17 | pass (4.5) |
| state merged as text / surface | `#3fb950` on `#14161a` | 7.13 | pass (4.5) |
| state deferred as text / surface | `#8fa1b8` on `#14161a` | 6.86 | pass (4.5) |
| state closed as text / surface | `#6f6c65` on `#14161a` | 3.46 | FAIL (4.5) |
| state danger as text / surface | `#ec7469` on `#14161a` | 6.27 | pass (4.5) |
| state proposed as dot / canvas | `#f59e0b` on `#0f1013` | 8.86 | pass (3.0) |
| state queued as dot / canvas | `#9b988f` on `#0f1013` | 6.60 | pass (3.0) |
| state interactive as dot / canvas | `#3fb8c4` on `#0f1013` | 8.03 | pass (3.0) |
| state awaiting as dot / canvas | `#c084fc` on `#0f1013` | 7.20 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#fb923c` on `#0f1013` | 8.41 | pass (3.0) |
| state completed as dot / canvas | `#58a6ff` on `#0f1013` | 7.53 | pass (3.0) |
| state merging as dot / canvas | `#58a6ff` on `#0f1013` | 7.53 | pass (3.0) |
| state merged as dot / canvas | `#3fb950` on `#0f1013` | 7.49 | pass (3.0) |
| state deferred as dot / canvas | `#8fa1b8` on `#0f1013` | 7.21 | pass (3.0) |
| state closed as dot / canvas | `#6f6c65` on `#0f1013` | 3.63 | pass (3.0) |
| state danger as dot / canvas | `#ec7469` on `#0f1013` | 6.58 | pass (3.0) |

##### option 1 — light

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#24221d` on `#f7f6f3` | 14.70 | pass (4.5) |
| text / surface | `#24221d` on `#ffffff` | 15.89 | pass (4.5) |
| secondary / canvas | `#5c574d` on `#f7f6f3` | 6.64 | pass (4.5) |
| secondary / surface | `#5c574d` on `#ffffff` | 7.18 | pass (4.5) |
| tertiary / canvas | `#6b655a` on `#f7f6f3` | 5.35 | pass (4.5) |
| tertiary / surface | `#6b655a` on `#ffffff` | 5.78 | pass (4.5) |
| accent text / surface | `#8a6a2e` on `#ffffff` | 5.02 | pass (4.5) |
| on-accent / accent (button) | `#ffffff` on `#8a6a2e` | 5.02 | pass (4.5) |
| state proposed as text / surface | `#a35406` on `#ffffff` | 5.49 | pass (4.5) |
| state queued as text / surface | `#6e6a60` on `#ffffff` | 5.39 | pass (4.5) |
| state interactive as text / surface | `#0b6f78` on `#ffffff` | 5.90 | pass (4.5) |
| state awaiting as text / surface | `#7e22ce` on `#ffffff` | 6.98 | pass (4.5) |
| state awaiting_submit as text / surface | `#c2410c` on `#ffffff` | 5.18 | pass (4.5) |
| state completed as text / surface | `#0b5fc0` on `#ffffff` | 6.15 | pass (4.5) |
| state merging as text / surface | `#0b5fc0` on `#ffffff` | 6.15 | pass (4.5) |
| state merged as text / surface | `#15803d` on `#ffffff` | 5.02 | pass (4.5) |
| state deferred as text / surface | `#55657a` on `#ffffff` | 5.95 | pass (4.5) |
| state closed as text / surface | `#7b766a` on `#ffffff` | 4.52 | pass (4.5) |
| state danger as text / surface | `#c03a2f` on `#ffffff` | 5.40 | pass (4.5) |
| state proposed as dot / canvas | `#a35406` on `#f7f6f3` | 5.08 | pass (3.0) |
| state queued as dot / canvas | `#6e6a60` on `#f7f6f3` | 4.99 | pass (3.0) |
| state interactive as dot / canvas | `#0b6f78` on `#f7f6f3` | 5.46 | pass (3.0) |
| state awaiting as dot / canvas | `#7e22ce` on `#f7f6f3` | 6.46 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#c2410c` on `#f7f6f3` | 4.79 | pass (3.0) |
| state completed as dot / canvas | `#0b5fc0` on `#f7f6f3` | 5.69 | pass (3.0) |
| state merging as dot / canvas | `#0b5fc0` on `#f7f6f3` | 5.69 | pass (3.0) |
| state merged as dot / canvas | `#15803d` on `#f7f6f3` | 4.64 | pass (3.0) |
| state deferred as dot / canvas | `#55657a` on `#f7f6f3` | 5.51 | pass (3.0) |
| state closed as dot / canvas | `#7b766a` on `#f7f6f3` | 4.19 | pass (3.0) |
| state danger as dot / canvas | `#c03a2f` on `#f7f6f3` | 5.00 | pass (3.0) |

##### option 1 — dark

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#ebe8e1` on `#0f1013` | 15.55 | pass (4.5) |
| text / surface | `#ebe8e1` on `#15171b` | 14.67 | pass (4.5) |
| secondary / canvas | `#aaa69a` on `#0f1013` | 7.82 | pass (4.5) |
| secondary / surface | `#aaa69a` on `#15171b` | 7.37 | pass (4.5) |
| tertiary / canvas | `#8d897e` on `#0f1013` | 5.45 | pass (4.5) |
| tertiary / surface | `#8d897e` on `#15171b` | 5.14 | pass (4.5) |
| accent text / surface | `#d9a441` on `#15171b` | 7.98 | pass (4.5) |
| on-accent / accent (button) | `#17130a` on `#d9a441` | 8.23 | pass (4.5) |
| state proposed as text / surface | `#f5a524` on `#15171b` | 8.79 | pass (4.5) |
| state queued as text / surface | `#a39f94` on `#15171b` | 6.79 | pass (4.5) |
| state interactive as text / surface | `#45c3cf` on `#15171b` | 8.51 | pass (4.5) |
| state awaiting as text / surface | `#c89bfc` on `#15171b` | 8.16 | pass (4.5) |
| state awaiting_submit as text / surface | `#fb9a4a` on `#15171b` | 8.39 | pass (4.5) |
| state completed as text / surface | `#6fb1ff` on `#15171b` | 8.04 | pass (4.5) |
| state merging as text / surface | `#6fb1ff` on `#15171b` | 8.04 | pass (4.5) |
| state merged as text / surface | `#4cc466` on `#15171b` | 8.03 | pass (4.5) |
| state deferred as text / surface | `#98a8bd` on `#15171b` | 7.41 | pass (4.5) |
| state closed as text / surface | `#8d897e` on `#15171b` | 5.14 | pass (4.5) |
| state danger as text / surface | `#f0786d` on `#15171b` | 6.50 | pass (4.5) |
| state proposed as dot / canvas | `#f5a524` on `#0f1013` | 9.32 | pass (3.0) |
| state queued as dot / canvas | `#a39f94` on `#0f1013` | 7.20 | pass (3.0) |
| state interactive as dot / canvas | `#45c3cf` on `#0f1013` | 9.02 | pass (3.0) |
| state awaiting as dot / canvas | `#c89bfc` on `#0f1013` | 8.65 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#fb9a4a` on `#0f1013` | 8.90 | pass (3.0) |
| state completed as dot / canvas | `#6fb1ff` on `#0f1013` | 8.52 | pass (3.0) |
| state merging as dot / canvas | `#6fb1ff` on `#0f1013` | 8.52 | pass (3.0) |
| state merged as dot / canvas | `#4cc466` on `#0f1013` | 8.51 | pass (3.0) |
| state deferred as dot / canvas | `#98a8bd` on `#0f1013` | 7.86 | pass (3.0) |
| state closed as dot / canvas | `#8d897e` on `#0f1013` | 5.45 | pass (3.0) |
| state danger as dot / canvas | `#f0786d` on `#0f1013` | 6.90 | pass (3.0) |

##### option 2 — light

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#1b1c1e` on `#f3f3f1` | 15.35 | pass (4.5) |
| text / surface | `#1b1c1e` on `#ffffff` | 17.05 | pass (4.5) |
| secondary / canvas | `#55575c` on `#f3f3f1` | 6.51 | pass (4.5) |
| secondary / surface | `#55575c` on `#ffffff` | 7.23 | pass (4.5) |
| tertiary / canvas | `#6b6e75` on `#f3f3f1` | 4.60 | pass (4.5) |
| tertiary / surface | `#6b6e75` on `#ffffff` | 5.11 | pass (4.5) |
| accent text / surface | `#7a5a22` on `#ffffff` | 6.34 | pass (4.5) |
| on-accent / accent (button) | `#ffffff` on `#7a5a22` | 6.34 | pass (4.5) |
| state proposed as text / surface | `#b4530a` on `#ffffff` | 5.02 | pass (4.5) |
| state queued as text / surface | `#6b6e75` on `#ffffff` | 5.11 | pass (4.5) |
| state interactive as text / surface | `#0b7285` on `#ffffff` | 5.59 | pass (4.5) |
| state awaiting as text / surface | `#7c3aed` on `#ffffff` | 5.70 | pass (4.5) |
| state awaiting_submit as text / surface | `#c2410c` on `#ffffff` | 5.18 | pass (4.5) |
| state completed as text / surface | `#1d4ed8` on `#ffffff` | 6.70 | pass (4.5) |
| state merging as text / surface | `#1d4ed8` on `#ffffff` | 6.70 | pass (4.5) |
| state merged as text / surface | `#15803d` on `#ffffff` | 5.02 | pass (4.5) |
| state deferred as text / surface | `#475569` on `#ffffff` | 7.58 | pass (4.5) |
| state closed as text / surface | `#6f727a` on `#ffffff` | 4.81 | pass (4.5) |
| state danger as text / surface | `#c81e1e` on `#ffffff` | 5.74 | pass (4.5) |
| state proposed as dot / canvas | `#b4530a` on `#f3f3f1` | 4.52 | pass (3.0) |
| state queued as dot / canvas | `#6b6e75` on `#f3f3f1` | 4.60 | pass (3.0) |
| state interactive as dot / canvas | `#0b7285` on `#f3f3f1` | 5.03 | pass (3.0) |
| state awaiting as dot / canvas | `#7c3aed` on `#f3f3f1` | 5.13 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#c2410c` on `#f3f3f1` | 4.66 | pass (3.0) |
| state completed as dot / canvas | `#1d4ed8` on `#f3f3f1` | 6.03 | pass (3.0) |
| state merging as dot / canvas | `#1d4ed8` on `#f3f3f1` | 6.03 | pass (3.0) |
| state merged as dot / canvas | `#15803d` on `#f3f3f1` | 4.51 | pass (3.0) |
| state deferred as dot / canvas | `#475569` on `#f3f3f1` | 6.82 | pass (3.0) |
| state closed as dot / canvas | `#6f727a` on `#f3f3f1` | 4.33 | pass (3.0) |
| state danger as dot / canvas | `#c81e1e` on `#f3f3f1` | 5.16 | pass (3.0) |

##### option 2 — dark

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#e7e8ea` on `#121417` | 15.05 | pass (4.5) |
| text / surface | `#e7e8ea` on `#191c20` | 13.94 | pass (4.5) |
| secondary / canvas | `#a3a7ae` on `#121417` | 7.64 | pass (4.5) |
| secondary / surface | `#a3a7ae` on `#191c20` | 7.08 | pass (4.5) |
| tertiary / canvas | `#878c94` on `#121417` | 5.46 | pass (4.5) |
| tertiary / surface | `#878c94` on `#191c20` | 5.06 | pass (4.5) |
| accent text / surface | `#e0b45c` on `#191c20` | 8.84 | pass (4.5) |
| on-accent / accent (button) | `#1a1405` on `#e0b45c` | 9.47 | pass (4.5) |
| state proposed as text / surface | `#f5a524` on `#191c20` | 8.38 | pass (4.5) |
| state queued as text / surface | `#9aa0a8` on `#191c20` | 6.49 | pass (4.5) |
| state interactive as text / surface | `#22c1d6` on `#191c20` | 7.87 | pass (4.5) |
| state awaiting as text / surface | `#c084fc` on `#191c20` | 6.47 | pass (4.5) |
| state awaiting_submit as text / surface | `#fb923c` on `#191c20` | 7.55 | pass (4.5) |
| state completed as text / surface | `#60a5fa` on `#191c20` | 6.72 | pass (4.5) |
| state merging as text / surface | `#60a5fa` on `#191c20` | 6.72 | pass (4.5) |
| state merged as text / surface | `#4ade80` on `#191c20` | 9.81 | pass (4.5) |
| state deferred as text / surface | `#94a3b8` on `#191c20` | 6.67 | pass (4.5) |
| state closed as text / surface | `#878c94` on `#191c20` | 5.06 | pass (4.5) |
| state danger as text / surface | `#f87171` on `#191c20` | 6.18 | pass (4.5) |
| state proposed as dot / canvas | `#f5a524` on `#121417` | 9.04 | pass (3.0) |
| state queued as dot / canvas | `#9aa0a8` on `#121417` | 7.00 | pass (3.0) |
| state interactive as dot / canvas | `#22c1d6` on `#121417` | 8.49 | pass (3.0) |
| state awaiting as dot / canvas | `#c084fc` on `#121417` | 6.98 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#fb923c` on `#121417` | 8.15 | pass (3.0) |
| state completed as dot / canvas | `#60a5fa` on `#121417` | 7.26 | pass (3.0) |
| state merging as dot / canvas | `#60a5fa` on `#121417` | 7.26 | pass (3.0) |
| state merged as dot / canvas | `#4ade80` on `#121417` | 10.59 | pass (3.0) |
| state deferred as dot / canvas | `#94a3b8` on `#121417` | 7.20 | pass (3.0) |
| state closed as dot / canvas | `#878c94` on `#121417` | 5.46 | pass (3.0) |
| state danger as dot / canvas | `#f87171` on `#121417` | 6.67 | pass (3.0) |

##### option 3 — light

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#1b1916` on `#f4f0e8` | 15.43 | pass (4.5) |
| text / surface | `#1b1916` on `#fffdf8` | 17.25 | pass (4.5) |
| secondary / canvas | `#5a5447` on `#f4f0e8` | 6.61 | pass (4.5) |
| secondary / surface | `#5a5447` on `#fffdf8` | 7.39 | pass (4.5) |
| tertiary / canvas | `#6e675a` on `#f4f0e8` | 4.93 | pass (4.5) |
| tertiary / surface | `#6e675a` on `#fffdf8` | 5.51 | pass (4.5) |
| accent text / surface | `#7d5a1c` on `#fffdf8` | 6.16 | pass (4.5) |
| on-accent / accent (button) | `#ffffff` on `#7d5a1c` | 6.27 | pass (4.5) |
| rail text / rail | `#ece7dc` on `#1b1916` | 14.22 | pass (4.5) |
| rail muted / rail | `#a69e8e` on `#1b1916` | 6.60 | pass (4.5) |
| rail accent / rail | `#e3b65a` on `#1b1916` | 9.28 | pass (4.5) |
| state proposed as text / surface | `#a8530a` on `#fffdf8` | 5.29 | pass (4.5) |
| state queued as text / surface | `#6e675a` on `#fffdf8` | 5.51 | pass (4.5) |
| state interactive as text / surface | `#0b6f78` on `#fffdf8` | 5.80 | pass (4.5) |
| state awaiting as text / surface | `#6d28d9` on `#fffdf8` | 6.99 | pass (4.5) |
| state awaiting_submit as text / surface | `#b83a0b` on `#fffdf8` | 5.66 | pass (4.5) |
| state completed as text / surface | `#1a56c4` on `#fffdf8` | 6.51 | pass (4.5) |
| state merging as text / surface | `#1a56c4` on `#fffdf8` | 6.51 | pass (4.5) |
| state merged as text / surface | `#167a3c` on `#fffdf8` | 5.32 | pass (4.5) |
| state deferred as text / surface | `#4b5a6e` on `#fffdf8` | 6.92 | pass (4.5) |
| state closed as text / surface | `#736c5d` on `#fffdf8` | 5.12 | pass (4.5) |
| state danger as text / surface | `#b3261e` on `#fffdf8` | 6.43 | pass (4.5) |
| state proposed as dot / canvas | `#a8530a` on `#f4f0e8` | 4.73 | pass (3.0) |
| state queued as dot / canvas | `#6e675a` on `#f4f0e8` | 4.93 | pass (3.0) |
| state interactive as dot / canvas | `#0b6f78` on `#f4f0e8` | 5.19 | pass (3.0) |
| state awaiting as dot / canvas | `#6d28d9` on `#f4f0e8` | 6.25 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#b83a0b` on `#f4f0e8` | 5.06 | pass (3.0) |
| state completed as dot / canvas | `#1a56c4` on `#f4f0e8` | 5.82 | pass (3.0) |
| state merging as dot / canvas | `#1a56c4` on `#f4f0e8` | 5.82 | pass (3.0) |
| state merged as dot / canvas | `#167a3c` on `#f4f0e8` | 4.76 | pass (3.0) |
| state deferred as dot / canvas | `#4b5a6e` on `#f4f0e8` | 6.19 | pass (3.0) |
| state closed as dot / canvas | `#736c5d` on `#f4f0e8` | 4.58 | pass (3.0) |
| state danger as dot / canvas | `#b3261e` on `#f4f0e8` | 5.75 | pass (3.0) |

##### option 3 — dark

| sample | colours | ratio | AA |
|---|---|---|---|
| text / canvas | `#ece7dc` on `#0c0d10` | 15.76 | pass (4.5) |
| text / surface | `#ece7dc` on `#15161a` | 14.66 | pass (4.5) |
| secondary / canvas | `#aaa393` on `#0c0d10` | 7.74 | pass (4.5) |
| secondary / surface | `#aaa393` on `#15161a` | 7.21 | pass (4.5) |
| tertiary / canvas | `#8c8577` on `#0c0d10` | 5.31 | pass (4.5) |
| tertiary / surface | `#8c8577` on `#15161a` | 4.94 | pass (4.5) |
| accent text / surface | `#e3b65a` on `#15161a` | 9.56 | pass (4.5) |
| on-accent / accent (button) | `#1a1405` on `#e3b65a` | 9.69 | pass (4.5) |
| rail text / rail | `#ece7dc` on `#0c0d10` | 15.76 | pass (4.5) |
| rail muted / rail | `#a09a8c` on `#0c0d10` | 6.94 | pass (4.5) |
| rail accent / rail | `#e3b65a` on `#0c0d10` | 10.28 | pass (4.5) |
| state proposed as text / surface | `#f6a623` on `#15161a` | 8.95 | pass (4.5) |
| state queued as text / surface | `#a09a8c` on `#15161a` | 6.45 | pass (4.5) |
| state interactive as text / surface | `#3fc3cf` on `#15161a` | 8.54 | pass (4.5) |
| state awaiting as text / surface | `#c99cff` on `#15161a` | 8.34 | pass (4.5) |
| state awaiting_submit as text / surface | `#fb9a4a` on `#15161a` | 8.46 | pass (4.5) |
| state completed as text / surface | `#6fb1ff` on `#15161a` | 8.10 | pass (4.5) |
| state merging as text / surface | `#6fb1ff` on `#15161a` | 8.10 | pass (4.5) |
| state merged as text / surface | `#4fcf72` on `#15161a` | 9.04 | pass (4.5) |
| state deferred as text / surface | `#9aa9bd` on `#15161a` | 7.56 | pass (4.5) |
| state closed as text / surface | `#8c8577` on `#15161a` | 4.94 | pass (4.5) |
| state danger as text / surface | `#f4776c` on `#15161a` | 6.63 | pass (4.5) |
| state proposed as dot / canvas | `#f6a623` on `#0c0d10` | 9.62 | pass (3.0) |
| state queued as dot / canvas | `#a09a8c` on `#0c0d10` | 6.94 | pass (3.0) |
| state interactive as dot / canvas | `#3fc3cf` on `#0c0d10` | 9.18 | pass (3.0) |
| state awaiting as dot / canvas | `#c99cff` on `#0c0d10` | 8.96 | pass (3.0) |
| state awaiting_submit as dot / canvas | `#fb9a4a` on `#0c0d10` | 9.09 | pass (3.0) |
| state completed as dot / canvas | `#6fb1ff` on `#0c0d10` | 8.71 | pass (3.0) |
| state merging as dot / canvas | `#6fb1ff` on `#0c0d10` | 8.71 | pass (3.0) |
| state merged as dot / canvas | `#4fcf72` on `#0c0d10` | 9.72 | pass (3.0) |
| state deferred as dot / canvas | `#9aa9bd` on `#0c0d10` | 8.13 | pass (3.0) |
| state closed as dot / canvas | `#8c8577` on `#0c0d10` | 5.31 | pass (3.0) |
| state danger as dot / canvas | `#f4776c` on `#0c0d10` | 7.13 | pass (3.0) |
