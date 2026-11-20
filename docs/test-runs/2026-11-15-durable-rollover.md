# Walkthrough: durable task rollover banners, pills and chain (2026-11-15)

Plan: [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §1 (PR #1532,
[Task #4222](/tasks/4222)). Walked by [Task #5913](/tasks/5913), filed by
[Task #5885](/tasks/5885).

## Outcome

| Plan item | Result |
|---|---|
| 1. Real rollover end to end (`reconcileRollover`, live daemon) | **Pass** on SQLite and PostgreSQL after one model fix (bug 2). |
| 2. Navigation both ways on `/next/` | **Pass**, except `#comment=` deep links, which `/next/` does not implement at all (finding 3, filed as a follow-up). |
| 3. Row pill under filters and views; A→B→C chain | **Pass** after one UI fix: the Timeline view had no pill (bug 1). |
| 4. Layout at 1440/390, light/dark, long titles, a 5-digit id | **Pass** by bounding-box assertions; one phone squeeze fixed as part of bug 1. |
| 5. Console session near the record limit | **Pass**: the note reaches the live session's prompt on both stores. What the agent then says is model behaviour, which a fake agent can't show. |
| 6. Classic Boards card chip at 1280/390 | **Pass** once a Closed column is added (the default board has none, by design). |

Fix commits on this branch:

- `fix(ui-ng): show the rolled-over pill on /next/tasks Timeline rows` (bug 1)
- `fix(model): carry one brief and handover through a chain of rollovers` (bug 2)

## Environment

- Commit walked: `c7987fe4b` (`origin/main` at task start), then this branch's fixes.
- Built with `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`, go1.26.10.
- Demo: `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:4290`
  ([`demo-chain.sh`](2026-11-15-durable-rollover/demo-chain.sh)). That script also
  rolls the seeded successor (22) over again through `POST /api/tasks/22/rollover`,
  which gives the chain 21 → 22 → 23, and gives 22 and 23 a 180-character title.
- Real daemon: `grain daemon -sandbox-port-base=0 -host-sandboxes` with the fake agy CLI
  and the GitHub sim from
  [`2026-11-13-grain-health-exit-codes/`](2026-11-13-grain-health-exit-codes/), run by
  [`run_daemon.sh`](2026-11-15-durable-rollover/run_daemon.sh). The run uses a thin
  [`fake-agy.py`](2026-11-15-durable-rollover/fake-agy.py) wrapper that writes every
  prompt to `runs/prompt-NNN.txt`.
  - SQLite 3.40.1.
  - PostgreSQL 15.20 (Debian 15.20-0+deb12u1).
- Browser: Playwright 1.62.1, Chromium build 1234 (`/var/cache/playwright`), Node v20.19.0.
  - Widths: 1440 and 390 for `/next/`, 1280 and 390 for classic Boards.
  - Themes: light and dark (`ui/e2e/screenshot.mjs`).

## Steps and results

### Demo: `/next/` walk (plan items 2–4)

[`walk-next.mjs`](2026-11-15-durable-rollover/walk-next.mjs) runs 45 checks at 1440
and 390 against a server holding a chain A→B→C:

```
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright URL=http://127.0.0.1:4290 \
  CODE=$(cat /tmp/demo/tok) A=21 B=22 C=23 node ../docs/test-runs/2026-11-15-durable-rollover/walk-next.mjs
```

Before the fix, 39 of 43 checks passed
([before](2026-11-15-durable-rollover/captures/walk-next-demo-before.txt)). After it,
43 of 45 passed (the saved-board step was added after the first run) ([after](2026-11-15-durable-rollover/captures/walk-next-demo-after.txt)).
Both failures left are the `#comment=` deep link, at each width.

What passed:

- **Closed tab:**
  - A shows "Rolled over to #22" and B shows "Rolled over to #23". A names B, not C, which is correct.
  - The pill is still there under `?q=nightly`, `?group=workstream` and with the row's
    selection checkbox ticked (selection bar open).
  - No sideways scroll.
- **B's page (`/next/task/22`):**
  - Both banners show, "Rolled over to #23" above "Carries on the work of #21".
  - Each banner fits on screen, at x 92–1420 on desktop and x 20–370 on phone. The page does not scroll sideways.
  - The successor link takes keyboard focus. Enter follows it to C, browser Back returns to B,
    and Forward returns to C, which shows only the "Carries on" banner.
- **Inspector pane (`/next/tasks?tab=closed&task=21&pane=runs`, desktop):**
  - The banner link moves the pane to `?task=22` and keeps `tab=closed` and `pane=runs`.
  - The back-link returns to `task=21`, and history Back works.
  - On a phone, rows open full-page rather than in a pane, so the walk checks the pane on desktop only.
- **Inbox:** the closed predecessor is not listed.

**Saved boards:** the walk saves `?tab=closed&q=nightly` as a board, opens the plain
`/next/tasks`, and applies the board from the saved-boards menu. The pill is back on A at
both widths.

**Screen readers:** both banners are `<p role="status">` with the link inside. Their
accessible text is the full sentence the walk captured ("Rolled over to #23. This durable
task passed its record limit…").

### Bug 1 (fixed): the Timeline view has no rollover pill, and the pill squeezes the title on a phone

Reproduction: on `grain demo`, open `/next/tasks?tab=closed&view=timeline`. Row 21 shows
its Repair-style badges but no "Rolled over to #22". The Timeline view draws its own row
template (`tasks-page.component.ts`, `.timeline-row-top`), and PR #1532 gave that
template the Repair badge but not the rollover pill.

First fix attempt: add the pill to the timeline row. The new phone assertion then caught a
second problem. At 390 px the pill, the state pill, the priority chip and the age are all
`flex-shrink: 0`, so the title was squeezed to **49 px**, about five letters.

Fix:

- `.timeline-row-top` now wraps (`flex-wrap: wrap`).
- `.timeline-row-title` has a `160px` flex basis, so badges drop under the title before they squeeze it.
- The design book (§ the "feel great" list) records the rule.
- On desktop the row is unchanged (one line).

Regression test: `ui-ng/e2e/next-task-rollover.spec.js`, "the Timeline view's row carries
the rolled-over pill" (`@phone`). It asserts the pill's text, that the pill stays inside
the row, off the title and on screen, that it is centred on the title line at desktop
width, that the title keeps ≥120 px, and that the page has no sideways scroll. It fails
on `main` (no pill) and passes on this branch.

### Real daemon: `reconcileRollover` end to end (plan item 1)

```
STORE=sqlite   docs/test-runs/2026-11-15-durable-rollover/run_daemon.sh
STORE=postgres docs/test-runs/2026-11-15-durable-rollover/run_daemon.sh
```

What each run does, with the same results on both stores:

1. **File the tasks.** Durable task A (#2) is filed with a `BRIEF-MARKER` description.
   Task W (#3) has `dependsOn: [A]`.
2. **A's first run.** The fake agent pushes a branch, and grain opens `acme/widgets#9000`
   in the sim. The agent calls `write_handover` (`HANDOVER-MARKER`) and `comment_on_issue`,
   and A rests in `awaiting_submit`.
3. **A rolls over.** `UPDATE task SET created_at = now() - 400 days` on A. Within one cycle,
   `reconcileRollover` logs
   `rolled durable task 2 over to task 4: it has been open since 2025-10-11 (the limit is a year)`.
   This is the orchestrator's own pass, not `Store.Rollover` called directly.
4. **What B (#4) carries:**
   - `state: queued`, `admission: durable`.
   - `pullRequest: acme/widgets#9000` and A's branch. A's `pullRequest` is now empty.
   - A description of brief, `## Handover`, the handover text, and "Rolled over from [task 2](/tasks/2)."
   - W now `dependsOn: ["4"]` and says so in a comment ("Now waits on task 4, which task 2
     was rolled over to.").
   - A is `closed`, with `rolledOverTo: 4` and grain's rollover comment.
5. **B's next run sees the handover.** B dispatched at once. Its prompt
   (`runs/prompt-001.txt`, "You are running as task 4") holds the brief, `## Handover`,
   `HANDOVER-MARKER` and "Rolled over from", once each.
6. **The chain.**
   - B's run writes its own handover (`HANDOVER-B-MARKER`).
   - The script inserts `task_sequence` 12344 on SQLite, or `setval` on PostgreSQL, so the
     next id has five digits, then back-dates B.
   - `reconcileRollover` rolls B over to C (#12345). W follows to `dependsOn: ["12345"]`
     and C keeps PR #9000.
   - C's run prompt (`prompt-002.txt`) carries `HANDOVER-B-MARKER` once and A's stale
     handover not at all. This holds only after bug 2's fix.
7. **`/next/` over the daemon.** `walk-next.mjs` with `A=2 B=4 C=12345` gives 41 of 43 (run before the saved-board step was added)
   on both stores
   ([PostgreSQL](2026-11-15-durable-rollover/captures/walk-next-daemon-postgres.txt)).
   The only failures are the `#comment=` checks. The 5-digit id and the long title on C
   cause no overflow at either width.

### Bug 2 (fixed): a second rollover nests the successor's whole body

Reproduction: on `main`, step 6 above gave C (#12345) this description:

```
BRIEF-MARKER: …

## Handover

HANDOVER-MARKER: night 1 was green; …          <- A's handover, carried inside B's body

Rolled over from [task 2](/tasks/2).

Rolled over from [task 4](/tasks/4).
```

With B's own handover written as well, C got **two** `## Handover` sections, A's stale
one first.

Cause: `rolloverCarry` took a successor's `Body`, which is the `RolloverBody` it was filed
with, as the brief whenever the successor had no `current_brief`.

Fix: `rolloverCarry` now unwraps an unedited `RolloverBody` back into brief and handover.
It recognises one by its exact "Rolled over from [task N](/tasks/N)." footer, where N is
the task's `rolled-over-from` link.

- The successor's own latest handover wins.
- When the successor wrote none, the handover it was filed with is carried once.
- A description edited since the rollover (no footer) is left whole.

After the fix, C's description is
[`captures/C-description-after-fix.md`](2026-11-15-durable-rollover/captures/C-description-after-fix.md):
brief, B's handover, and one footer naming task 4. The earlier lineage is still reachable
through the "Carries on the work of" banner chain.

Regression test: `pkg/model/rollover_test.go`, `TestRolloverOfASuccessorDoesNotNestItsBody`
(B with and without a handover of its own). Both subtests fail on `main` and pass here.

### Console session near the record limit (plan item 5)

[`console_session.sh`](2026-11-15-durable-rollover/console_session.sh) runs over the same
live daemon:

1. `POST /api/console-sessions {"id":"walk-rollover"}` creates the session's task (#12346).
2. The script back-dates its `created_at` by 400 days and posts a message to the session.

Result on SQLite and PostgreSQL:

- The session's next prompt contains the record-limit note once: "[grain] This console
  session is past a durable task's record limit: it has been open since 2025-10-11 (the
  limit is a year)…".
- `reconcileRollover` leaves the session alone (`rolledOverTo: null`, `awaiting_reply`),
  as `RolloverRefusal` says it should.

A fake agent can't show whether the session then says it is due to be closed and refiled
when the conversation rests. That is what a real model does with the note, so it is not
proved here.

### Classic Boards card chip (plan item 6)

[`walk-classic-board.mjs`](2026-11-15-durable-rollover/walk-classic-board.mjs), over the demo chain:

- **Why it needs a Closed column:** classic boards have no Closed column by default
  (`ui/src/board.js` `DEFAULT_COLUMNS`; the board shows "4 tasks in no column (Deferred,
  Closed)"). So the chip, which only a closed predecessor carries, is never visible on a
  default board. That is the documented design ("Adding a column for either is two clicks
  in the editor"), not a bug. The script adds the column through `grain.board.columns`,
  as the editor saves it.
- **Result:** at 1280 and 390 px the chips "rolled over to 22" and "rolled over to 23" are
  one line, not truncated, and inside their card (chip x 68–177 in a card at x 68–354 on a
  phone, after scrolling the sideways-scrolling board to the card). Card crops are
  `board-chip-1280.png` and `board-chip-390.png`.

### Finding 3 (not fixed here): `/next/` has no `#comment=` deep links

`/next/task/<id>#comment=<n>` loads the page, banners included, and stays at the top. No
element carries a comment id, and nothing in `ui-ng/src` reads the fragment. Classic
handles it (`ui/src/App.jsx`). This affects every task, not only rolled-over ones, and
predates PR #1532, so it is filed as a follow-up rather than fixed in this walkthrough.

## Screenshots

`make ui-screenshots`-equivalent runs (`node e2e/screenshot.mjs` with `URL`/`CODE`
pointing at the running demo or daemon) at 1440/390 px, light and dark:

- `/next/tasks?tab=closed`
- `/next/tasks?tab=closed&view=timeline`
- `/next/task/21`, `/next/task/22` and `/next/task/23`
- `/next/tasks?tab=closed&task=22` (the pane)

Each set was shot before the fix (`screenshots/before`) and after it (`screenshots/after`),
plus the same pages over the PostgreSQL daemon with ids 2/4/12345 (`screenshots/after-daemon`).
Contact sheets are attached to the task's closing note. This sandbox's file reader returns
PNGs as bytes and `LOOK=1` has no Gemini key here, so the author could not inspect the
images by eye. The layout claims above rest on the bounding-box assertions, and a human
reviewer should look at the contact sheets.

## Tests run

- `go test ./pkg/model ./pkg/demo -count=1`: pass.
- `go test ./pkg/orchestrator -run 'Rollover|Compaction|Durable'`: pass.
- `cd ui-ng && npx vitest run src/app/tasks src/app/task`: 92 passed.
- `cd ui-ng && npx playwright test e2e/next-task-rollover.spec.js e2e/next-full-system.spec.js e2e/next-saved-boards.spec.js`:
  14 passed (desktop and phone projects).
- `make fmt-frontend` (prettier): clean for `ui-ng/`.

## Notes for whoever runs this next

- `make frontend` leaves `ui/` and `ui-ng/` without their dev dependencies, so run
  `npm ci` again before Playwright.
- `pkill -f "grain demo"` and `pgrep -f` match the sandbox shell's own command line and
  kill it. Use the pid file `demo-chain.sh` writes.
- The task JSON uses `dependsOn` and `attempts` (an array). `depends_on` is silently ignored.
