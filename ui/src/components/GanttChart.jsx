import { useState } from "react";
import {
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  Typography,
} from "@mui/material";
import {
  STATE_LABELS,
  blockedDescription,
  stateTitle,
  taskBadgeModifier,
  taskBadgeState,
  isAgentRunning,
  isTaskRepairing,
} from "../state.js";
import { isNarrowed, narrowTasks } from "../taskFilters.js";
import {
  LANES,
  RANGES,
  describeSpan,
  duration,
  groupIntoLanes,
  inWindow,
  loadView,
  nowAt,
  placeOf,
  saveView,
  spanOf,
  ticksFor,
  windowOf,
} from "../gantt.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import {
  focusRowLink,
  ListEmpty,
  ListHeader,
  RowLink,
} from "./ListPrimitives.jsx";
import NarrowingBar from "./NarrowingBar.jsx";
import StateDot from "./StateDot.jsx";

// GanttChart is the same backlog as TaskList and TaskBoard, laid out
// against a clock: one bar per task, from when it was filed to when it
// stopped being work in progress, with the moment it was first
// dispatched marked on it (gantt.js, which holds all of the arithmetic
// and the reasoning about what this view is for).
//
// It is a third *view*, not a third source of truth. The tasks, the
// search box, the sort orders and every attribute filter are the list's
// own (taskFilters.js), so narrowing to one repo means the same thing
// here as it does there; what the chart adds is the two settings above
// it -- how far back to look, and what to group the rows into -- which
// are this browser's own (gantt.js's GANTT_STORAGE_KEY), exactly as the
// board's columns are.
//
// # Why it takes a `now` rather than reading the clock
//
// Because half of what it draws is measured from it: an unfinished bar
// runs to now, the "now" line is drawn at it, and every duration in a
// tooltip is counted against it. Taking it as a prop keeps one moment
// behind a whole render -- a chart whose bars each ended at their own
// Date.now() would disagree with its own axis -- and lets a test say
// what the current moment is instead of racing it.
//
// # What is not on a bar
//
// A checkbox. The list and the board both carry one per task and feed
// the batch-actions bar from it (App.jsx); a row here is a line about a
// task's shape in time, and 24 of them fit on a screen precisely because
// the row is one line high. A checkbox is 30px of touch target and would
// cost that, for a gesture both other views already offer over the same
// tasks. Clicking a row opens the task, which is what a card does.
// # Embedded, as one panel of a dashboard
//
// `embedded` draws the chart with neither of the two rows above it: the
// heading and the range/lanes controls, and the narrowing bar. A panel
// of a dashboard (PanelBoard.jsx, grain/task-88) carries its own
// heading, and its question is the board's rather than something the
// reader edits in place -- so what is left is exactly the chart. It is
// a prop rather than a second component because everything below those
// two rows is what a timeline *is*, and a panel drawing its own copy of
// it would be a second timeline to keep in step with this one.
//
// `fixedView` is that panel's own range, overriding what this browser
// last chose here. Passing it makes the two controls moot, which is why
// it only ever arrives with `embedded`.
//
// `maxRows` is how many bars that panel is allowed to draw, and 0 -- the
// full view's own value -- is "as many as there are" (grain/task-311).
// The range already decides which tasks have a bar at all, since a task
// whose span does not overlap the window is off the chart and counted as
// such; what it cannot decide is how many that leaves. A deployment
// whose last seven days hold nine hundred tasks has nine hundred bars to
// draw in a window seven days wide, and drew them: a 400px panel holding
// 14,000px of rows, scrolled inside a page that also scrolls, and the
// first thing on the dashboard at a phone's width. The pane the full
// view fills is a place to scroll through nine hundred rows; a panel is
// a window onto a view, so it says how many more there are instead --
// which is the deal LIST_PANEL_ROWS already makes for the list panel.
//
// `landmark` false keeps the full view -- heading, controls and all --
// but draws its root as a plain element, for a saved timeline board
// whose page is already the main landmark (BoardPage.jsx,
// grain/task-1253).
export default function GanttChart({
  tasks,
  config,
  narrowing,
  onNarrow,
  onOpenTask,
  now = Date.now(),
  embedded = false,
  fixedView = null,
  maxRows = 0,
  landmark = true,
}) {
  // The range and the lanes this browser last chose, or the default
  // (gantt.js). Held here rather than in App for the board's own reason:
  // nothing outside this view means anything by them.
  const [browserView, setBrowserView] = useState(() => loadView());
  const view = fixedView || browserView;
  const zone = useTimeZone();

  const choose = (patch) => {
    const next = { ...view, ...patch };
    setBrowserView(next);
    saveView(next);
  };

  // Narrow, then place: a bar's position depends only on the task's own
  // dates, but the row order is the narrowing's own, so the chart reads
  // top to bottom in whatever order the list would.
  //
  // The narrowing is the same three refinements every task view shares,
  // from the same place: App owns them and the query string is where
  // they live (paths.js), so a search typed on the list is still in
  // force here. Asked through taskFilters.js's narrowTasks, which is
  // also what makes a timeline *panel* cost what one chart costs -- the
  // dashboard has already asked this question of this list, and the
  // answer is the one it got (PanelBoard.jsx).
  const narrowed = narrowTasks(tasks, narrowing, config);
  const spans = new Map();
  for (const t of narrowed) {
    const span = spanOf(t, now);
    if (span) spans.set(t.id, span);
  }
  const chartWindow = windowOf([...spans.values()], view.range, now);
  const inRange = narrowed.filter(
    (t) => spans.has(t.id) && inWindow(spans.get(t.id), chartWindow),
  );
  // The budget, applied last: to what the range has already admitted,
  // and in the narrowing's own order, so the bars a panel draws are the
  // top of the same order the view behind it opens on -- ListPanel's own
  // slice, over the rows this view has.
  const shownTasks = maxRows > 0 ? inRange.slice(0, maxRows) : inRange;
  // The two ways a task the narrowing admits can have no bar on screen,
  // said out loud rather than silently dropped, the same way the board
  // says how many tasks its columns are not showing. A chart of the last
  // 24 hours on a deployment with a year of history is mostly *not*
  // showing things, and a reader who cannot see that has no reason to
  // reach for the range control -- or, on a panel, for the link out of
  // it. They are counted separately because they are different facts and
  // want different gestures: one is a range too narrow, the other is a
  // range that holds more than a panel can draw.
  const overBudget = inRange.length - shownTasks.length;
  const offChart = narrowed.length - inRange.length;
  const notes = [];
  if (overBudget > 0)
    notes.push(
      `Showing ${shownTasks.length} of ${inRange.length} in this range.`,
    );
  if (offChart > 0)
    notes.push(
      `${offChart} task${offChart === 1 ? "" : "s"} outside this range.`,
    );
  const ticks = ticksFor(chartWindow, zone);
  const marker = nowAt(chartWindow, now);
  const lanes = groupIntoLanes(shownTasks, view.lanes);
  // Every task rather than the narrowed ones, so a blocked row can say
  // what its blocker is doing even when the blocker is off the chart.
  const byId = new Map(tasks.map((t) => [t.id, t]));

  const Root = embedded || !landmark ? "div" : "main";
  return (
    <Root
      className={`gantt${embedded ? " gantt-embedded" : landmark ? "" : " task-view"}`}
    >
      {!embedded && (
        <ListHeader
          title="Timeline"
          count={shownTasks.length}
          action={
            // Wraps: side by side the two pickers are wider than a
            // phone's header has room for, and .gantt clips its
            // overflow, so the Lanes picker was cut off at the edge.
            <Stack
              direction="row"
              useFlexGap
              sx={{
                ml: "auto",
                flexWrap: "wrap",
                justifyContent: "flex-end",
                gap: 1,
              }}
            >
              <FormControl size="small" sx={{ minWidth: 150 }}>
                <InputLabel id="gantt-range-label">Range</InputLabel>
                <Select
                  labelId="gantt-range-label"
                  label="Range"
                  value={view.range}
                  onChange={(e) => choose({ range: e.target.value })}
                >
                  {RANGES.map((r) => (
                    <MenuItem key={r.id} value={r.id}>
                      {r.label}
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>
              <FormControl size="small" sx={{ minWidth: 130 }}>
                <InputLabel id="gantt-lanes-label">Lanes</InputLabel>
                <Select
                  labelId="gantt-lanes-label"
                  label="Lanes"
                  value={view.lanes}
                  onChange={(e) => choose({ lanes: e.target.value })}
                >
                  {LANES.map((l) => (
                    <MenuItem key={l.id} value={l.id}>
                      {l.label}
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>
            </Stack>
          }
        />
      )}
      {!embedded && tasks.length > 0 && (
        <NarrowingBar
          tasks={tasks}
          config={config}
          narrowing={narrowing}
          onNarrow={onNarrow}
        />
      )}

      {notes.length > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          className="gantt-off-note"
        >
          {notes.join(" ")}
        </Typography>
      )}

      <div className="gantt-body">
        {/* The axis is drawn once, above the lanes, and the same tick
            positions are re-used by the rules behind them -- so a label
            and the line it names cannot drift apart. */}
        <div className="gantt-scale">
          <div className="gantt-scale-label">
            {formatDateTime(chartWindow.from, zone)}
          </div>
          <div className="gantt-scale-track">
            {ticks.map((tick) => (
              <span
                key={tick.at}
                className={`gantt-tick${tick.day ? " gantt-tick-day" : ""}`}
                style={{ left: `${tick.left}%` }}
              >
                {tick.label}
              </span>
            ))}
          </div>
        </div>

        <div className="gantt-lanes">
          {/* One rule per tick and the "now" line, behind every row at
              once: a chart of 200 rows would otherwise draw 200 copies
              of the same grid, and they are decoration a screen reader
              has no use for. */}
          <div className="gantt-grid" aria-hidden="true">
            {ticks.map((tick) => (
              <span
                key={tick.at}
                className={`gantt-rule${tick.day ? " gantt-rule-day" : ""}`}
                style={{ left: `${tick.left}%` }}
              />
            ))}
            {marker !== null && (
              <span className="gantt-now" style={{ left: `${marker}%` }} />
            )}
          </div>

          {lanes.map((lane) => (
            <section key={lane.key || "all"} className="gantt-lane">
              {lane.label && (
                <header className="gantt-lane-header">
                  <span className="gantt-lane-title">{lane.label}</span>
                  <span className="count">{lane.tasks.length}</span>
                </header>
              )}
              <ul className="gantt-rows">
                {lane.tasks.map((t) => (
                  <GanttRow
                    key={t.id}
                    task={t}
                    span={spans.get(t.id)}
                    window={chartWindow}
                    now={now}
                    zone={zone}
                    byId={byId}
                    onOpenTask={onOpenTask}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>

      {tasks.length > 0 && shownTasks.length === 0 && (
        <ListEmpty>
          {isNarrowed(narrowing)
            ? "No tasks match this search and these filters in this range."
            : "No tasks in this range. Try a wider one."}
        </ListEmpty>
      )}
      {tasks.length === 0 && <ListEmpty>No tasks yet.</ListEmpty>}
    </Root>
  );
}

// GanttRow is one task: its name on the left, its bar on the right.
//
// The bar is one element with the wait drawn inside it rather than two
// side by side, so that a task whose wait is most of its life still
// reads as one bar of one length -- which is the thing being compared
// across rows -- with the dispatch marked inside it.
function GanttRow({ task, span, window, now, zone, byId, onOpenTask }) {
  const place = placeOf(span, window);
  const label = stateTitle(task, byId);
  // A blocked task waits on a dependency rather than a dispatcher -- the
  // fact its blocked state oval carries on a list row and a card
  // (state.js's taskBadgeState), and the one view of a task that would
  // otherwise read as a dispatch backlog. It is said on the row's badge,
  // in the bar's label and in the tooltip, which names the open
  // dependencies the way the badge's own title does.
  // Still no arrows (gantt.js, "What is deliberately not here").
  const blocked = task.blocked ? blockedDescription(task, byId) : null;
  // Everything the bar can say in one string: the two absolute times, in
  // the deployment's zone like every other timestamp in the UI, and the
  // waits and runs computed from them.
  const title = [
    `${task.id}: ${task.title}`,
    ...(blocked ? [blocked] : []),
    `filed ${formatDateTime(span.from, zone)}`,
    span.ongoing
      ? `still open after ${duration(now - span.from)}`
      : `ended ${formatDateTime(span.to, zone)}`,
    describeSpan(span, now),
  ].join(" · ");
  return (
    // A plain listitem that a pointer clicks anywhere on, with its title
    // as the link a keyboard opens it by -- the same shape as a task row
    // (TaskList.jsx). The timeline is a <ul>, and a child of one that
    // stops being a listitem takes the list's own count away from a
    // screen reader with it.
    <li
      className="gantt-row"
      onClick={(e) => {
        focusRowLink(e);
        onOpenTask(task.id);
      }}
      title={title}
    >
      <div className="gantt-row-label">
        {/* The row's state marker is a task row's own badge, so a state
            reads the same here as in the list and on the board -- the
            animated mark included, for a task whose run is live. */}
        {(() => {
          const modifier = taskBadgeModifier(task);
          const repairing = isTaskRepairing(task);
          const interactive = !repairing && Boolean(task.interactive);
          return (
            <span
              className={`badge badge-icon badge-${taskBadgeState(task)}${isAgentRunning(task) ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
              title={label}
            >
              <StateDot
                state={taskBadgeState(task)}
                title={label}
                repairing={repairing}
                interactive={interactive}
                agentRunning={isAgentRunning(task)}
              />
            </span>
          );
        })()}
        <span className="task-number">{task.id}</span>
        <RowLink
          className="gantt-row-title"
          href={`/tasks/${encodeURIComponent(task.id)}`}
          onOpen={() => onOpenTask(task.id)}
          aria-label={task.title ? undefined : `Task ${task.id}`}
        >
          {task.title}
        </RowLink>
      </div>
      <div className="gantt-track">
        <div
          className={[
            "gantt-bar",
            `gantt-bar-${task.state}`,
            span.ongoing ? "gantt-bar-ongoing" : "",
            place.clippedStart ? "gantt-bar-clipped-start" : "",
            place.clippedEnd ? "gantt-bar-clipped-end" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          style={{ left: `${place.left}%`, width: `${place.width}%` }}
          role="img"
          aria-label={`${STATE_LABELS[task.state] || task.state}${blocked ? ", blocked" : ""}: ${describeSpan(span, now)}`}
        >
          {/* The stretch before dispatch, drawn hollow inside the bar.
              Absent for a task that has never run at all, whose whole
              bar is a wait and says so through its own class. */}
          {span.started !== null && place.wait > 0 && (
            <span
              className="gantt-bar-wait"
              style={{ width: `${(place.wait / place.width) * 100}%` }}
            />
          )}
        </div>
      </div>
    </li>
  );
}
