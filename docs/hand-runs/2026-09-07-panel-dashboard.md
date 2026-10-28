# Hand run: a dashboard of six panels, on real history and on a phone

`docs/test-plans/2026-09-06.md` section 4, together with the phone leg of
its section 12. What the plan asked for: a dashboard of six panels on a
deployment with real history -- at least one timeline and one throughput
panel, and at least one panel overriding the dashboard's own question --
narrowed from the toolbar, clicked through, reloaded, timed, and then
opened at a phone's width; and a judgement on the round trip that adds a
panel.

Everything below was done in a real browser against a real
`pkg/ui.Server` over a real embedded SQLite store. Four of the plan's
seven steps came out exactly as it predicted. Three did not, and those
are sections 2, 5 and 6.

## What was run, and what was not

**Not run against this deployment's own database.** The sandbox this was
done in has no reach into the grain that dispatched the task: no copy of
its store, no daemon, and its boards live in `bwsalmon/grain-home` rather
than here. "This one will do" was not available, so the history was
rebuilt instead.

**What the history is.** This repository's own git log, read as the
record of what this deployment has actually done: 918 merged pull
requests between 2026-08-21 and 2026-09-07, one task each. Per task, all
of it real --

* the branch head's subject as the title and its commit message body as
  the description (so the text a panel searches is the text these tasks
  actually carry, at the length they actually carry it);
* the first commit on the branch as when work started, the last as when
  the run finished, the merge commit as when the task closed;
* one run per task, started and finished at those two moments, with the
  commit count in its `Detail`;
* a `fixes` link to the pull request, and `AutoMerge`, which is what
  makes the state come out as `closed` rather than as something else.

Plus the eleven branches `origin/main` has not merged, as the live work a
deployment always has some of. 929 tasks in total: 918 closed, 6 running,
3 queued, 2 proposed. Everything is written through `ui.Client` and
`model.Store`'s own exported writes, the way `grain demo`'s seed is, so
each task is as well-formed as a real one. Seeding took 2m30s--3m12s for
918 tasks (~200ms a task, most of it the store).

**What that history is not.** Three things git cannot say, and none of
them is guessed at quietly:

* **when a task was filed.** A task is filed before its first commit and
  waits in a queue; the first commit is the earliest moment git knows.
  So every seeded task's `created` is really "work started", the queue
  wait is zero everywhere, and any *latency* read off this store is
  wrong. Throughput and volume are right, because they are counted from
  moments that are real.
* **failed attempts.** Every task here has exactly one succeeded run.
  Real history has retries, repairs and failures; nothing in this store
  reads `failed`, so the "Needs you" column is 0 throughout.
* **the states of the eleven open branches**, which are assigned across
  running/queued/proposed rather than read off anything.

**The browser.** Headless Chromium (Playwright 1.63) against the
frontend built from this commit, at 1440x900 for the laptop legs. The
phone leg is Chromium at 390x844 and 360x740 with `isMobile`, `hasTouch`
and the CPU throttled 4x -- which is an emulator, not a phone. **No real
device was used**; that leg is still owed, and is the same debt section
12 of the plan already carries.

## 1. A dashboard of six panels

`POST /api/boards`, `kind: "panels"`, six panels: a full-width timeline
over 7 days, a throughput panel over 7 days, the state counts, two lists
carrying their own questions (`q=test` and `q=sandbox`), and a
full-width list carrying none. Four of them inherit the dashboard's
question; two override it. A second board of ten -- the same six plus two
more lists, a second timeline and a second throughput panel -- was made
for step 5's question about ten.

There is no form for this, which is the whole of section 7 below: a
dashboard is rows in the store, so making one is an API call here and a
pull request against the state repository on a real deployment.

## 2. Narrowing from the toolbar

Searching `sandbox` in the dashboard's own toolbar, on the six-panel
board:

| Panel | Own query | Count before | Count after |
| --- | --- | --- | --- |
| This week, as bars (timeline) | -- | 929 | 56 |
| Throughput, 7 days | -- | (none) | (none) |
| Where everything is (states) | -- | 929 | 56 |
| Tests and CI (list) | `q=test` | 88 | 88 |
| The sandbox and the guest (list) | `q=sandbox` | 56 | 56 |
| Live work (list) | -- | 929 | 56 |

Exactly what the plan predicted, and what `PanelBoard.test.jsx` already
pins: every inheriting panel moves, the two overriding ones do not, the
throughput panel does not (its report takes no filter), and the URL
becomes `/boards/board-1?q=sandbox` -- a link somebody can send.

Each panel's own link carries the narrowing too. Opening them and
counting rows in the list they land on:

```
"Live work"                 count=56 link=/?q=sandbox -> 56 rows
"Tests and CI"              count=88 link=/?q=test    -> 88 rows
"The sandbox and the guest" count=56 link=/?q=sandbox -> 56 rows
```

**The disagreement: an inheriting panel on a dashboard nobody has
narrowed.** Before the search, those three panels read 929 and their
link is `/`. Following it:

```
"This week, as bars"  count=929 link=/ -> 11 rows; chips: State is none of Deferred, Closed
"Where everything is" count=929 link=/ -> 11 rows; chips: State is none of Deferred, Closed
"Live work"           count=929 link=/ -> 11 rows; chips: State is none of Deferred, Closed
```

929 on the panel, 11 in the list it opens. Both numbers are right for
their own view and the gap between them is the deployment's default
narrowing: the task list applies `defaultNarrowing(config)` -- "State is
none of Deferred, Closed", which the chip on the list says out loud --
and a dashboard opened from a board whose stored query is empty applies
nothing at all. So a panel over "every task" on a deployment with real
history is a panel over its *archive*: 918 of those 929 are closed, and
the eight rows a list panel shows are eight closed tasks from three weeks
ago.

That is not a small cosmetic gap. On demo data it is invisible -- there
are twelve tasks and one of them is closed -- which is why fourteen unit
cases and two e2e specs did not catch it. On real history it is the first
thing a reader sees, and the count it puts on the panel header is not the
count of anything they can then open.

It is also the same question section 3 of the plan left open about
`#929`: a URL with no `state` condition now means "show everything",
and a dashboard's panels are the largest population of such URLs anybody
has. Whichever way that is decided, a dashboard should decide it the same
way as the list it links to. Filed.

## 3. Clicking a state count

Works, exactly as the plan says, and the numbers underneath it are the
interesting part:

```
state counts: Proposed 2 | Queued 3 | Running 6 | Needs you 0 | Queued for merge 0
              918 in no column
clicked "Proposed" (2) -> /?f=state:any:proposed -> 2 rows
```

Two on the panel, two in the list. The note under the counts is the same
finding as section 2 wearing a different face: on real history, "918 in
no column" is what the state-counts panel mostly is, because the columns
this browser is set to are the live states and almost every task a
deployment has ever had is closed. The number is honest and the panel is
nearly empty above it.

## 4. Reload

Survives, as the plan expected -- panels, order and the dashboard's
question:

```
url before: /boards/board-1?q=sandbox      url after: /boards/board-1?q=sandbox
titles:  This week, as bars | Throughput, 7 days | Where everything is |
         Tests and CI | The sandbox and the guest | Live work    (identical)
counts:  56,-,56,88,56,56  ->  56,-,56,88,56,56
search box after reload: "sandbox"
```

## 5. What it costs

**The plan's premise is wrong, and correcting it is the finding.** The
plan says "six panels each run their own narrowed query" and asks what
ten would cost "the browser or the API". Three of the four panel kinds
run no query at all against the API: `PanelBoard.jsx` filters and sorts
the one task list `App` already holds, in the browser, once per panel.
Only the throughput panel fetches, once per window, and it does not poll.
So panel count costs the API nothing and the browser everything -- which
is the opposite of the shape the plan was worried about, and it changes
which fix is the right one.

**What the API costs, per open tab, whatever the view.** `App` polls
`/api/tasks` every 3 seconds and gets the whole list:

```
/api/tasks    n=151  1,717,527 bytes  min 197ms  median 1049ms  max 3003ms
/api/metrics  ?window=7d&buckets=24   7 KB       65-174ms
/api/config, /api/boards, /api/console  ~1 KB each, <1ms
```

1.7 MB every three seconds, and about a second of server time to build
it, for one reader with one tab open. That is the number the plan was
looking for, and it belongs to the deployment's history rather than to
the dashboard: the flat list pays it too. Two tabs open pushed the same
query to 3.0s.

**What the browser costs.** Chromium's own `Performance.getMetrics`,
laptop viewport, three runs each; `script` is `ScriptDuration`:

| | cold, to first panel | script, cold | steady: script per 3s poll tick | main thread over 12s |
| --- | --- | --- | --- | --- |
| six panels | 2.7-3.2s | 1.3-1.6s | **1.02-1.19s** | 7.0-8.5s busy |
| ten panels | 3.7-4.5s | 1.8-2.6s | **2.12-2.32s** | 11.9-12.6s busy |
| the flat list | 2.3-2.7s | 1.1-1.3s | -- | -- |

Read the last column first: over a 12-second window with nothing
happening, the ten-panel dashboard's main thread was busy for 11.9-12.6
seconds of it. It is saturated. Every poll tick hands every panel a new
array of 929 tasks, each panel sorts and filters all 929 again
(`narrowedFor`), and `useMemo` cannot help because `tasks` is a fresh
array every poll. Six panels is about 40% of every tick; ten is about
75% of it and, with the rest of the render, the whole of it.

**Would ten be usable? No, and six is nearer the edge than it looks.**
Six is usable on a laptop -- scrolling and typing stayed responsive, and
a narrowing landed in well under a second. Ten was visibly laggy in
exactly the way the numbers say. And nothing here is a hard limit that
argues for a cap on panels: the cost is one shared list re-filtered N
times a tick, three seconds apart, so it is a memoisation and a polling
problem rather than a panel problem. Both are filed.

Two smaller costs found while measuring:

* **a timeline panel draws a row per task, whatever its range.** The
  panel is 398px tall and its body's `scrollHeight` is **14,097px** --
  929 rows in a 400px window. The board's second timeline panel, ranged
  to a single day rather than a week, measures the same 14,097px: the
  range decides the horizontal span and the "25 tasks outside this range"
  note, not how many rows are drawn. On demo data this is a nine-row
  chart and nobody notices.

  **Correction (grain/task-365): the second panel was never ranged to a
  day.** The seed wrote it `Range: "1d"`
  ([seed-store.go.txt:357](2026-09-07-panel-dashboard/seed-store.go.txt)),
  and `"1d"` is not one of `gantt.js`'s `RANGES` -- the day-wide range is
  `"24h"`. So `panelGanttView` fell back, as it is meant to, to whatever
  range this browser had last chosen on the full timeline, and the two
  panels drew the same seven days. The two `scrollHeight`s being equal
  measured nothing: they were the same panel twice.

  The claim above happens to be true anyway -- `GanttChart` does draw a
  row per task in the narrowing and lets the range decide only the
  horizontal span, which is why the row cap filed below was the right
  thing to file, and it has since been filed, fixed and pinned
  (`GANTT_PANEL_ROWS`, and "Showing 10 of 20 in this range." in
  `PanelBoard.test.jsx`). But it was not *shown* here: the "whatever the
  range" in the filed row's own summary was an inference, and the two
  equal `scrollHeight`s under it were not evidence for it.

  What the mistake demonstrates is worth more than what it cost: a panel
  ranged to a word this build does not have looked exactly like a panel
  ranged to a word it does, to a person measuring it on purpose. That is
  now said in three places -- `grain state check` warns about the row,
  the prompt that lists a dashboard's panels names the value it could
  not use, and the panel itself prints which word it dropped and what it
  drew instead (grain/task-365).
* the ten-panel board's heap reached 45-51 MB against 16 MB for six.

## 6. On a phone

Chromium at 390x844 and at 360x740, `isMobile`, `hasTouch`, CPU
throttled 4x. What is good:

* **no sideways scroll at either width** (`documentElement.scrollWidth`
  equals `innerWidth`: 390 and 360), which is what `#885` asked for and
  what a dashboard of a fixed grid could easily have broken;
* every panel reflows to the full column width (334px and 304px) and
  stacks, in the board's own order;
* the toolbar is there and works: the search box is visible and typing
  `sandbox` moved the same four panels to the same counts as on the
  laptop.

What is not:

* **it takes 13.7 seconds to draw**, against 2.7-3.2s unthrottled, and a
  narrowing gesture takes a further 2.2-2.3s to land. A phone fetches the
  same 1.7 MB and does the same six sorts of 929 tasks with about a
  quarter of the CPU. This is the same finding as section 5 with the
  multiplier a phone applies.
* **the panel's "Open" link is a 31x19px tap target.** It is the only way
  out of a panel into the view it summarises, it is the gesture a phone
  reader wants most, and it is well under the 44x44 both platforms' own
  guidance asks for. The state counts are fine (146x65 each).
* the dashboard is 2,500px of scrolling on a 844px screen -- about three
  screens for six panels, and the first of them is the 14,097px timeline
  panel from section 5, which on a phone is a 334px-wide window onto a
  chart 42 times its own height. A timeline panel is the one kind that
  does not survive the reflow.

**A real device is still owed.** Nothing above is iOS Safari, and the
three things the emulator cannot answer are the three section 12 already
names: momentum scrolling inside a panel that is itself inside a
scrolling page, what the on-screen keyboard does to the toolbar when the
search box takes focus, and whether a 31x19px link is hittable at all
with a thumb.

## 7. Adding a panel is a pull request

"Add panel" opens the new-task form pre-written, and the task it writes
is genuinely good: it names the board and its id, lists the six panels
already on it in order with each one's kind, width, query and range,
gives the vocabulary for all four fields, explains that the rows live in
`tables/board_panel.json` in the state repository, and says what happens
after the merge ("grain pulls it down and the board appears within the
minute -- no restart"). Somebody who has never seen a panel row could
file it without reading any of this documentation, and an agent has
everything it needs in the prompt.

**Is the round trip bearable?** For what it mostly is, yes. Adding a
panel is a change to a dashboard several people read -- it is closer to
editing a saved report than to dragging a widget -- and it lands as a
reviewed pull request in the state repository with the repository's own
`grain state check` run against it, which is the right weight for that.
The wait is a task dispatch plus a review plus a minute, and there is
nothing to type.

**But it is the wrong weight for two things it is also used for**, and
both showed up in this run:

1. **Getting a panel's query right is iterative.** `q=test` finding 88
   tasks and `q=sandbox` finding 56 is something you learn by trying,
   and here each try is a pull request. Everything needed to try one
   already exists: the toolbar narrows the dashboard live, and "Edit
   board" hands an agent the query the reader is looking at. What is
   missing is the step between -- "narrow this panel like *that*, and
   keep it".
2. **Panel order.** `#839` made boards draggable into folders and order;
   panels are not draggable, and moving one is a pull request that edits
   an `order_key`. That is the one edit with no judgement in it at all,
   and on a phone -- where the order is the whole layout, since every
   panel is full width and stacked -- it is the edit most likely to be
   wanted.

So: not the thing to fix next. Section 5's cost is the thing to fix
next, because a dashboard nobody can leave open on a second screen is not
worth editing. But the "narrow it, then keep it" gesture is worth more
than a panel form would be, and is filed as such.

## What was filed

| Task | Kind | Why |
| --- | --- | --- |
| A dashboard and the list it links to should hide the same tasks | bug | section 2: 929 on the panel, 11 in the list it opens |
| Stop re-filtering every task for every panel on every poll | performance | section 5: 1.0s of script per tick at six panels, 2.3s at ten |
| Cap what a timeline panel draws | bug | section 5: 14,097px of rows inside a 398px panel, whatever the range |
| A panel's "Open" link is a 31x19px tap target | bug | section 6 |
| Keep the narrowing a reader made, as a panel's own query | ergonomics | section 7 |

## Still owed

1. **A real phone.** Everything in section 6 is an emulator with a
   throttled CPU. The device leg was already owed by section 12 and this
   run adds three specific things to look for, listed there.
2. **The deployment's own database.** The history here is reconstructed,
   and it is honest about volume, text and spread but silent about queue
   waits, retries and failures -- so nothing above says what a *latency*
   panel or a "Needs you" column reads like when they are not empty. The
   run to do on the real store is section 3's, with the state counts and
   the throughput panel read against what the daemon actually recorded.
3. **`/api/tasks` at this size.** 1.7 MB a poll is measured here and
   filed nowhere: the dashboard did not cause it and cannot fix it, and
   pagination or a delta poll is a change to the list API that wants its
   own design. Worth saying out loud that a deployment three months older
   than this one pays five times as much for it, per tab, every three
   seconds.
