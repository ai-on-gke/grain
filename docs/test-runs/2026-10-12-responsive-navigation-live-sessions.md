# Responsive Navigation and Live Sessions Walkthrough

Date: 2026-10-12 (13:10–14:10 UTC)

Commit tested: `6babee9a2`, the tip of `candidate` (the build stamp in the
rail read `v1.10.0-rc.1.0.20261012124358-6babee9a2d1d · 6babee9`). Since
the 10-10 walkthrough (`003d11565`) `candidate` gained, among others, the
surfaces this run concentrated on:

- `1f201114a` feat(ui): team home page with a setup checklist, and the team
  shown where things are created
- `b2a883e27` feat(ui): say grain is restarting while an upgrade's restart
  has the API down
- `4276f0e35` / `54aa85bf0` feat(ui): list grain's versions in Metrics ->
  Release notes and preview a target's notes in the upgrade pane
- `a0536b15d` feat(ui): rename the Admin section to System
- `4a2462f5c` fix(ui): name the chat transcript's scrollbar for keyboard
  and screen readers; `f244395e3` console resting line

One bug was found and fixed in this task (below).

## Environment

- Debian sandbox, Linux x86_64, 4 vCPU, 7 GiB. Go 1.26.6, Node.js 20.19.0.
- Playwright 1.62.1 (from `ui/package-lock.json`), Chromium and WebKit 26.5
  (playwright webkit 2336), installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`
  (the first attempt without the CA variable failed with "Download failure").
- Frontend: `cd ui && npm ci && npm run build` (into `pkg/ui/static`).
- Server: the real `grain demo` built with `go build -o /tmp/grain ./cmd/grain`,
  run as `TMPDIR=/tmp/demo /tmp/grain demo -addr 127.0.0.1:8765 -open=false`
  over its throwaway embedded SQLite store for the exploratory passes; the
  Playwright suites start one demo per worker through `e2e/fixtures.js`.
- No `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed
  tooling was used.
- Actor: the loopback-trusted `debian` (system admin) with
  `X-Grain-Admin-Mode: on`, `grain.username=alice` in localStorage to skip
  onboarding, as `e2e/pageerrors.js` does. The demo's `default` team is
  named "admin".
- Viewports: desktop 1440×900 and 1280×500; tablet 820×1180 and 1180×820;
  phone 390×844, 844×390 and 320×568. Coarse pointer (`hasTouch`) on phone
  contexts; light and dark colour schemes; `reducedMotion: reduce` at 320.

## Stubs

| Boundary | How | Why |
| :--- | :--- | :--- |
| Restart outage (exploratory, both engines, 320×568) | `page.route` on `/api/config` adds `restarting: true` to the real response, then every `/api/*` request is aborted with `connectionrefused` until released. | The demo cannot run an upgrade or restart itself; this is the network boundary the banner reads. |
| Live session transcript, offline/503, session-manager 503, deployment banners | Unchanged from the existing `responsive-navigation-walkthrough.spec.js` and `stacked-banners.spec.js` stubs (task GET intercepted to `running` with incremental markdown; `context.setOffline`; 503 answers; `/api/config` banner flags), documented in the 10-10 report. | The demo has no model provider behind its sessions and raises none of those banners. |

Nothing else was stubbed: navigation, React, layout, the real server and
its REST API (tasks, teams, console sessions, boards, release notes,
admin tabs) ran unmodified. Session create and delete went through the
real `POST`/`DELETE /api/console-sessions`.

## Exact steps and outcomes

### 1. Rail and drawer sweep through the real nav controls

A script clicked each rail entry by its accessible name — Team home,
Inbox, Tasks, Boards, Sessions, Schedules, Templates, Suites, CI, Repos,
System, Metrics, Team settings — at 1440×900 (dark), 820×1180 (dark),
390×844 (light, touch; through "Open navigation") and 320×568 (touch,
reduced motion). On every screen it checked document `scrollWidth` against
the viewport, and every visible control for a right/left edge outside the
viewport that no sideways scroller holds.

- Every destination opened at the expected URL (`/t/default`,
  `/t/default/inbox`, `/t/default/tasks?...`, `/t/default/boards`,
  `/t/default/sessions`, ..., `/admin`, `/metrics`, `/t/default/settings`)
  with zero horizontal overflow and no unreachable control.
- The only flag was MUI's invisible native switch input behind the rail's
  Admin mode switch (opacity 0, by design); its accessible name is
  "Admin mode" (`checkbox "Admin mode"`).
- No page errors. The only non-2xx responses were the known demo
  boundaries: `GET /api/relay-settings` 501 (no relay) and
  `GET /api/repos/acme/widgets/github-branches` 404 (no forge).

### 2. System tabs, task detail, Back/Forward and reload

At 390×844, 320×568 and 1440×900: opened System, walked all 16 tabs
(General & Deployment ... Sandboxes, Caches, Top, Pipeline, Power, Upgrade)
by clicking each tab; no overflow at any width. At 1440 the tab strip
scrolls, and a centre-point hit test found two tabs partly under MUI's
own scroll arrow, which is the strip scrolling, not a covered control.
Escape closed System back to `/t/default`. Opened task 1 from the list,
Back returned to the filtered list URL, Forward reopened
`/t/default/tasks/1`, and a hard reload of that deep link rendered with
no overflow. While the task pane is open the rail is inert, so
"+ New task" is not reachable behind it, as intended.

### 3. Metrics -> Release notes (new) — **bug found and fixed**

Reached through the rail's Metrics entry at 390×844, 320×568, 820×1180
and 1440×900 in both engines. The weekly section and the
"Versions" list (8 versions embedded in this build) render with no
overflow; reload and Back keep `/metrics`; Escape returns to the page
underneath.

**Bug:** clicking a version renders its notes below the whole list. The
notes started at y=931 on a 390×844 phone and y=1026 on a 1440×900
desktop, so a tap visibly changed nothing but the row highlight. The row
is a toggle, but it exposed no state: no `aria-expanded`, no
`aria-controls`; MUI's `selected` only adds a class.

Deterministic reproduction (before the fix), in either engine:
`/metrics` at 390×844 → click the first button in
`list "grain versions"` → `[data-testid=release-version-notes]` has
`getBoundingClientRect().top` 931 > `innerHeight` 844, and the button has
no `aria-expanded` attribute.

**Fix** (`ui/src/components/ReleaseVersions.jsx`): each version button
carries `aria-expanded` and, while open, `aria-controls` naming the notes.
The notes are a labelled `region` ("<version> release notes") and are
brought into view with `scrollIntoView({ block: "nearest" })` once they
load.

**Regression coverage:**
- `ReleaseNotesPanel.test.jsx`: the disclosure attributes, the named
  region, and closing again.
- `responsive-navigation-walkthrough.spec.js`: "A grain version's release
  notes open on screen and say they are open". At phone (through the
  drawer) and desktop, in Chromium and WebKit, it asserts that the notes
  region is in the viewport after the tap, `aria-expanded`/`aria-controls`
  are set, there is no overflow, and that Enter from the keyboard closes
  it. Run against the old component, it failed at the `aria-expanded`
  check (`Expected "false", Received ""`). With the fix it passes in both
  engines.

### 4. Team home page (new)

`/t/default` through "Team home", at all four widths: no overflow. A
keyboard-only Tab sweep (80 presses) at 390×844 and 1440×900 in Chromium
and WebKit, light scheme. Every stop was on screen, named, and outside any
`aria-hidden`/`inert` subtree. At the phone width, focus cycles banner →
Edit team profile → Edit members → Manage → Edit git connections → Edit
model connections → Models → Edit capacity → All tasks → the task links →
Director Session, then wraps.

### 5. Standing sessions: create, deep-link, delete (WebKit, 390×844, touch)

- Rail "Sessions" read 1. Opened Sessions through the drawer, then
  "+ New session". The dialog (326px wide, inside the screen) says
  "In team admin", and Create stays disabled until a name is typed.
- Created "Walkthrough night shift with an extraordinarily long title
  that keeps going". It navigated to `/t/default/session/<slug>`, and the
  rail count became 2. The long heading wraps (scrollWidth = clientWidth),
  with no page overflow. The pending session shows the "grain files this
  session's task on its next cycle" note and its Edit capabilities and
  Delete session buttons.
- Delete from the keyboard (focus + Enter) raised the native confirm
  `Delete session "<title>"? This removes the session from this
  deployment.`. Dismissing it kept the session, with focus back on
  "Delete session". Accepting it deleted the session and went to
  `/t/default/session`; the rail count was back to 1.
- A hard deep link to the deleted slug renders the Sessions list with
  the alert "There is no session “<slug>” here. It may have been deleted;
  pick another below." It does not open another session's chat.
- The live-session behaviours the demo cannot drive (incremental
  markdown, pinned scroll, offline composer, 503 recovery, task opening
  over the chat, title collisions) are covered by the existing
  walkthrough spec, which passed in both engines (below).
- No task-bound PTY console exists in the demo: `GET /api/console`
  answers `{"enabled":false}`, and interactive tasks use the chat
  surface, so there is no terminal to resize here.

### 6. "grain is restarting" banner (new), 320×568, both engines

With the stub above, the `status` banner read "grain is restarting -- an
upgrade or a restart is replacing the server. Unavailable since ... --
retrying automatically ..." 0.8 s after the API went away. It is 128px
tall, full width, and wraps (no overflow, `scrollWidth <= clientWidth`),
and it leaves "Open navigation" uncovered by a hit test. It cleared on its
own 3.6 s after the API was released, without a reload.

## Suites run

1. Before the fix, on `6babee9a2`:
   `npx playwright test responsive-navigation-walkthrough phone
   sidebar-navigation shell stacked-banners keyboard-open-rows
   candidate-keyboard-walkthrough narrow system-overlay-tabs director
   disconnection chat-theme chat-scrollbar-accessibility
   console-resting-notes console-sessions-team-scoping --workers=2`
   → **155 passed, 1 skipped** (6.0 m), Chromium and WebKit. The skip is
   `phone.spec.js`'s finger drag on WebKit, which needs CDP touch moves.
2. After the fix: `vitest run src/components/ReleaseNotesPanel.test.jsx`
   → 8/8. `responsive-navigation-walkthrough.spec.js` in both engines →
   **66/66 passed** (2.7 m, Chromium and WebKit, 33 each). The new case
   failed on the old component.
3. `prettier --check` on the changed files: clean.

## Follow-ups

None filed. Everything found was in scope and fixed here.
