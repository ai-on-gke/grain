# 2026-10-23 §5: Pinned sidebar sub-targets, console session restart, live `WorkTree`, `/director`, and `/processes` over the combined demo

The walkthrough in `docs/test-plans/2026-10-23-afternoon.md` §5, run on
`candidate` (tip `bc6bf2775`) with `95f0d3825` backported onto it. Run on
2026-10-24 in one sandbox session.

## Environment

| | |
|---|---|
| Base | `origin/candidate` at `bc6bf2775` ("Merge pull request #4610 from bwsalmon/grain/049f1e09/task-577") |
| Frontend | `cd ui && npm ci && npm run build` (Vite) |
| Server | real `grain demo -addr 127.0.0.1:0 -open=false -merge-rules -sensitive-hold -mechanical-port`, one per Playwright worker (`ui/e2e/fixtures.js`, `test.use({ demoArgs })`), built from the same tree |
| Browser | Playwright 1.62.1, Chromium headless shell (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) |
| Viewports | 1440×900 desktop, 390×844 phone (touch, mobile), light and dark |
| Toolchain | Go 1.26.10, Node 20.19.0 |

Nothing of grain's was stubbed: every request in the new spec goes to the
demo's own API and store.

## Step 1: commits

`61707e025`, `f82f5ee81` and `cd2b49e44` are on `origin/candidate`.
`95f0d3825` (PR #4424) was only on `main` and its branches, so it was
cherry-picked onto this branch with
`Backported-from: 95f0d382548b88375ff67db41860890e03a87bfd`.

The one conflict was the `<ConsoleSessionPage task={...}>` prop in
`ui/src/App.jsx`: `61707e025` guarded it with `sessionTask && String(sessionTask.id) === String(openSessionTaskId)`,
`95f0d3825` with `openSessionTaskId && String(sessionTask?.id) === String(openSessionTaskId)`.
The resolution keeps both conditions:

```jsx
task={
  openSessionTaskId &&
  sessionTask &&
  String(sessionTask.id) === String(openSessionTaskId)
    ? sessionTask
    : null
}
```

`App.test.jsx`, `CustomizePinnedOverlay.test.jsx`, `Sidebar.test.jsx`,
`WorkTree.test.jsx` and `paths.test.js` pass after the merge (240 tests),
including both branches' own regression tests: "refreshes the open
session's task immediately when ... Start a fresh chat restarts the
session" (`61707e025`) and "does not show the previous standing session's
conversation while the newly opened session's task is still loading"
(`95f0d3825`).

## Step 3: browser walkthrough

All of it is `ui/e2e/sessions-worktree-pinned-nav-walkthrough.spec.js`
(10 tests, all passing on Chromium).

### Pinned System and Settings sub-targets

- Starting from an empty pinned list, `System`, `System → Health`,
  `System → Sandboxes`, `Settings`, `Settings → General` and
  `Settings → Credentials` were pinned one after another through
  `CustomizePinnedOverlay`. After each pin, that target's "Pin item" was
  disabled, every target still to come stayed enabled, and so did the
  siblings `System → Logs` and `Settings → Memory`. **Pass.**
- `localStorage["grain.pinned"]` held all six after Save.
- Which pin is lit (desktop, before and after a reload):

  | Address | Canonical URL | Lit pin |
  |---|---|---|
  | `/system` | `/system` | Health |
  | `/system/health` | `/system` | Health |
  | `/system/sandboxes` | `/system/sandboxes` | Sandboxes |
  | `/system/logs` | `/system/logs` | System (bare) |
  | `/settings` | `/settings/general` | General |
  | `/settings/general` | `/settings/general` | General |
  | `/settings/credentials` | `/settings/credentials` | Credentials |

  Only the most specific pin lights; the bare pin lights only on a tab or
  section nobody pinned. Clicking the Health pin goes to the canonical
  `/system`. **Pass.**
- **Plan deviation:** the plan names `/system/executors`, but there is no
  Executors System tab (`SYSTEM_TABS` in `ui/src/settingsSections.js`).
  `/system/executors` is an old System-overlay settings tab and is
  redirected to `/settings/executors`, so the "unpinned System tab" row
  used `/system/logs`.
- A stored list with a duplicate `System → Sandboxes`, a `section: 7`
  entry, an empty `tab`, an unknown `kind`, `null` and a bare string loads
  with no page error at 1440 and 390px. The malformed sub-targets are
  read as bare pins, the invalid entries are dropped, and both duplicate
  Sandboxes pins are drawn and lit together. **Pass.**

### `/director`, switching and restart

- From `/processes`, `goto("/director")` lands on `/session/director`.
  `history.length` grows by one for the visit and not again for the
  rewrite. Back returns to `/processes`, Forward to `/session/director`,
  and Back again to `/processes`, so `replaceState` traps nothing. **Pass.**
- A second standing session (`triageNNNNN`) was created through
  `POST /api/console-sessions` and given a marker comment. Both sessions
  were pinned, and the test clicked back and forth between the two pins
  six times. A `MutationObserver` recorded any DOM state where the
  director's page showed the triage chat's marker, or the triage page
  showed the director's conversation ("plan the Q3 delivery milestone").
  Then came "Start a fresh chat" → "Close this chat and start a fresh one"
  (`POST /api/console-sessions/{id}/restart`), four more rapid switches,
  and one background poll tick. After that the closed chat's marker was
  also forbidden on the triage page. The recorder stayed empty. **Pass.**
- **Mutation check:** with the guard reverted to `task={sessionTask}`,
  this same test fails, and the recorder holds 28 stale draws, both
  sessions' conversations on each other's pages. So the test does catch
  the regression both commits fix.
- **Plan deviation:** in a plain `grain demo` the director's chat (task 12)
  is `queued`, not `awaiting_reply`, because no agent answers it.
  `ui/e2e/director.spec.js` already covers the `awaiting_reply` behaviour
  over its scripted director server. This walkthrough covers the guard and
  restart over the real demo store.

### Live `WorkTree`

On `/session/director` with the tree expanded and a draft typed into the
composer, these ran through the API without touching the page:

1. The grandchild "Extract pagination query helper" (filed by the
   pagination task, two levels down) was closed.
2. The child "Add a dark mode toggle" was approved.
3. The child "Bump the Go toolchain to 1.24" was stopped.

The summary went from "0 of 4 done" to "1 of 4 done", and both children's
statuses changed in place. A `MutationObserver` saw no moment without the
tree, and the draft was untouched. Clicking the dark-mode child opened
`/tasks/1` with its heading, and Back returned to the session page with
the tree still drawn. **Pass.**

### Combined demo declarations (`cd2b49e44`, `f82f5ee81`)

- `GET /api/repos/acme/widgets/declarations` carries the union of the
  three scenarios: template `.grain/templates/nightly.yml`, suite
  `.grain/suites/smoke.yml`, schedule `.grain/schedules/nightly.yml`,
  trigger `.grain/triggers/payments.yml`, merge rules
  `.grain/merge-rules.yml`, the `.grain/triggers/broken.yml` read error,
  three merge-rules warnings and two port-types warnings. **Pass.**
- `/processes` lists `nightly-maintenance`, `release-qualification`,
  `security-audit`, both suites, three schedules and the triggers. **Pass.**
- `/repos/acme/widgets#advanced` showed the read error and every warning,
  but **nothing about what was still in force**. With a read error, the
  "Read at …: N template(s), … merge rules in …" line is replaced by
  "Last read failed: … — still running what 0f1e2d3c4b5a declared." That
  sentence never says what this commit declared. Under `-merge-rules`
  alone (no read error) the line is there, which is why
  `merge-rules-walkthrough.spec.js` never noticed. **Fixed** (see below).
- The repository's own port-types warnings appear twice on the page,
  among the declaration warnings and in the Ports box. `pkg/ui/repos.go`
  does this on purpose, and the spec asserts both places.

### `/processes` at 390×844

The spec ran in light and dark. For every template, suite, schedule and
trigger row, every visible part stayed inside the row and the viewport:
kind chip, name link, chips (including the recurrence description),
hints, repo, next run and `Use…`/`Run…` buttons. No button overlapped any
text, and the document did not scroll sideways. The list rows have no
`Run now` or `Pause`/`Resume`; those are on a schedule's own page. There,
both buttons are on screen and do not overlap. **Pass**, both themes.

## The fix

`ui/src/components/RepoPage.jsx`: a failed read with a last good commit
now keeps a second line, "Still running from 0f1e2d3c4b5a: 1 template(s),
1 suite(s), 1 schedule(s), 1 trigger(s), merge rules in
.grain/merge-rules.yml". It is counted by the same `declaredSummary`
helper the clean read now uses. A failed read with no good commit behind
it says nothing more. Two Vitest cases in `RepoPage.test.jsx` cover this,
and so does the walkthrough spec. The existing "Last read failed" tests
(`RepoPage.test.jsx`, `path-triggers-sensitive-hold-walkthrough.spec.js`)
still pass unchanged.

## Step 4: screenshots

`make ui-screenshots PAGES=/director,/sessions,/processes,/system,/settings/credentials,/repos/acme/widgets#advanced OUT=screenshots/sessions-and-pinned-nav`
with `DEMO_ARGS="-merge-rules -sensitive-hold -mechanical-port"` shot each
page at desktop and phone widths, light and dark. `OUT=screenshots/before`
shot the repository page with candidate's `RepoPage.jsx`.

This sandbox has no Gemini key, so `LOOK=1` could not describe the shots,
and the file reader shows a PNG as bytes. The pictures themselves were
not looked at here. Their content was checked by OCR (tesseract): the
"before" repository page has the read error and no "Still running" line,
and the "after" one has both, at desktop and at phone width, where the
line wraps. Layout was checked by the spec's bounding-box assertions
rather than by eye. The contact sheet is attached to the task's closing
note for a reviewer to look at.
