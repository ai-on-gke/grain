# Inbox sections collapse, notifications out of director chat, rail Inbox count — 2026-10-31

Walkthrough of `docs/test-plans/2026-10-30.md` §6 (PRs #199 `c9d674f9`,
#203 `609c1b24`, #167 `8905f2cb`, #157 `4a175de6`).

- **Date:** 2026-10-31, 21:52–22:40 UTC
- **Task:** 2015 (filed by task 1899), branch `grain/049f1e09/task-2015`, base `main`
- **Commit tested:** `7c652762` (`main` at the start of the run, "Merge pull
  request #297"). No product code changed in this run.
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), `linux/amd64`
  - Go 1.26.10, Node.js v20.19.0, Playwright from `ui/package-lock.json`,
    headless Chromium `chromium-1234` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`)
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`,
    so the demo embeds the built UI.
  - `TMPDIR=/tmp/demo grain demo -github-sim -fake-ci -git-proxy -reconcile 2s
    -notifications -addr 127.0.0.1:18731 -open=false -access-code-file /tmp/demo/code`
    (the git proxy at `127.0.0.1:9728` is the sandbox's own; `-git-proxy` was
    added to the plan's flags so a child run could leave a note the way
    `grain task message` does, through the proxy's `/cli/` hop).
  - `sqlite3` installed with apt for the stubs below.
  - No Gemini key in the sandbox, so `LOOK=1` was not available; layout was
    checked with bounding boxes instead (below).

## Stubs

The server, store, UI, browser, notification raises and the note hop were real.
What the demo has no producer for was staged:

1. **The director's run having finished** (the demo runs no agent): sqlite
   `task_observation.completed_at` set on the director's task (#13), so it is
   *resting* and a wake would show (`retry_requested_at` set, `completed_at`
   cleared — `Store.WakeRestingTask`).
2. **A subscribed standing task** (the demo runs no pipeline steward): sqlite
   `task_notification_subscription (7, 'ops.*')` on resting task #7.
3. **Legacy lines from a pre-#203 build** in the director's conversation:
   sqlite `task_comment` rows `automation/console-session`
   `**Notification (warning):** …` and `automation/schedule` `_Woken early:_ …`.
4. **Config-repo sync failure** (the demo has no grain/config repository): the
   committed spec adds `stateRepoSyncFailure` to `/api/config`'s real answer
   with `page.route`. Local-script approvals are real (`POST
   /api/tasks/{id}/local-script-approvals`, the run's own hop).

## Results

### Gap 1 — persistence and keyboard (pass)

Pinned in `ui/e2e/inbox-collapse-walkthrough.spec.js`, run over the
fixture's own demo at 1440 and 390 px:

- Defaults: Notifications `aria-expanded="false"`, every other section `true`;
  `aria-controls` names `inbox-group-<sectionId>`.
- Mouse click folds (body hidden, heading count unchanged) and unfolds; the
  `grain.inbox.collapsed` map holds only choices that differ from the default,
  and the key is removed when none do.
- Enter folds, Space unfolds, focus stays on the heading button; Space on
  Notifications unfolds it and its rows show.
- Reload keeps `{notifications: false, <group>: true}`; `/tasks` and Back
  (the Inbox unmounts and mounts again) keep it too.
- Empty and refill: a local-script section folded, its only call answered (the
  section goes), a new call asked → the section comes back folded with count 1.
- A critical notification raised while Notifications is folded: the heading's
  count goes up by one without a reload, the section stays folded, the page
  total goes up by one, and the bell lists it with `data-severity="critical"`
  (1440 and 390).

Layout, over the live demo with a throwaway script, at 1440/390 × light/dark in
the default, all-folded and all-unfolded states: chevron, title and count on
one centre line (±2 px) in every heading, count inside the viewport, no
horizontal page overflow — 12/12 ok. Screenshots: `make ui-screenshots
PAGES=/inbox,/session/director OUT=screenshots/after`, plus full-page
default/all-folded/all-unfolded shots of `/inbox` at each width and theme.

### Gap 2 — director chat over the live demo (pass)

Director session `director` → task #13, resting (stub 1); task #7 resting and
subscribed to `ops.*` (stub 2). `POST /api/notifications` as the operator with
`ops.walkthrough-warning` (warning) and `ops.walkthrough-critical` (critical),
both `new: true`; then 6 s for the `-notifications` sweep:

| | before | after |
|---|---|---|
| #13 comments | 6 | 6 |
| #13 `completed_at` / `retry_requested_at` | set / – | set / – (not woken) |
| #7 comments | 1 | 2: `_Woken early:_ notification ops.walkthrough-warning: …` |
| #7 `completed_at` / `retry_requested_at` | set / – | – / set (woken) |

The critical one added no second wake line to #7 because #7 was already queued
by the warning (`WakeRestingTask` leaves a non-resting task alone), as intended.

In Chromium, `/session/director` at 1440/390 × light/dark: neither raised
notification's title is in the page, and the legacy `**Notification (warning):**`
and `_Woken early:_` lines (stub 3) are hidden.

### Gap 3 — child note wakes the console session (pass, one finding)

#13 put back to rest. Task #1 (`proposed-by` #13) approved, a run started and its
sandbox token minted with `POST /api/e2e/git-proxy/runs`, then `POST
/cli/tasks/13/notes` through the git proxy with that token (what `grain task
message 13` sends): `Left a note on task #13 (comment 11).` #13's
`retry_requested_at` was set and `completed_at` cleared (woken); the only new
comment is the note itself (`agent/1`), with no `_Woken early:_` line; the note
is shown on `/session/director` at every width and theme.

**Finding (not fixed here — filed as a follow-up):** the same note sent through the
agent API instead (`POST /api/tasks/13/notes` with `X-Grain-Acting-Task: 1`,
task #1 holding `api:tasks:write`) is refused with `task 13 is the standing
session "director", which task 1 does not own` (`agentRails` in
`pkg/ui/agent_api_gate.go`). The sandbox CLI hop (`SandboxCLIActions.MessageTask`)
and `grain mcpserver`'s `message_task` hop have no such check, so a director's
child can report to the director by one route and not by the other. Whether a
note (which never parks or acts on a run) should be exempt from the
standing-session rail is a trust-boundary call, so it is left for review.

### Gap 4 — counts agree (pass)

`ui/e2e/inbox-collapse-walkthrough.spec.js`, 1440 px, with one real local-script call
pending and the stubbed sync failure: the rail's Inbox chip (`.rail-count`,
titled "12 tasks waiting on you, 3 notifications, 1 local script waiting for
approval, settings are not being exported") equals the Inbox heading total (17),
with local-scripts, state-sync and notifications all expanded and all folded;
folding changes no count; still equal after the call is answered (section gone)
and after a new one refills it.

## Bugs

None in the §6 changes. One cross-route inconsistency (gap 3) filed as a follow-up.

## Outcome

- New spec `ui/e2e/inbox-collapse-walkthrough.spec.js` (3 tests, passing
  locally in 1.4 min) pins persistence, keyboard behaviour, empty/refill, the
  folded-critical case and the chip/heading agreement.
- No product code changed, so there are no before/after pairs; the after shots
  above are the record.
