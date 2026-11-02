// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { Fragment, useEffect, useMemo, useState } from "react";
import { Button, Typography } from "@mui/material";
import api from "../api.js";
import { STATE_LABELS } from "../state.js";
// attributesFor is here for two callers: the throughput panel names the
// conditions it could not use (scopeNote), and naming one takes the
// attribute's own label; and a panel says which of its own conditions
// name no attribute at all (unknownNote).
import { attributesFor } from "../taskAttributes.js";
import {
  NO_NARROWING,
  defaultFilters,
  describeUnknownConditions,
  narrowTasks,
  unknownConditions,
} from "../taskFilters.js";
import { defaultColumns, groupIntoColumns, loadColumns } from "../board.js";
import { buildPath } from "../paths.js";
import {
  panelDestination,
  panelGanttView,
  panelMetricsWindow,
  panelNarrowing,
  panelPath,
  panelRangeNote,
  panelUsesNarrowing,
  panelTitle,
} from "../panels.js";
import { childRenderKeys } from "../childOrder.js";
import { panelPlaces } from "../panelOrder.js";
import { keptQuery } from "../boards.js";
import { reportPath, reportScope, scopeSummary } from "../report.js";
import { loadView } from "../gantt.js";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  usePickup,
} from "./ReorderPickup.jsx";
import GanttChart from "./GanttChart.jsx";
import Sparkline from "./Sparkline.jsx";
import StateDot from "./StateDot.jsx";
import { ListEmpty } from "./ListPrimitives.jsx";
import NarrowingBar from "./NarrowingBar.jsx";
import { TaskRow } from "./TaskList.jsx";
import { rowFields } from "./CustomFields.jsx";

// PanelBoard is a dashboard: one page showing several views of this
// deployment at once, each panel narrowed to its own question
// (grain/task-88). It is what a board of kind "panels" renders as, the
// way a "gantt" board renders as the timeline -- so a dashboard is a row
// in the store, an entry in the nav rail and a link like every other
// board (boards.js), and the panels it is assembled from are rows
// underneath it (panels.js, model.BoardPanel).
//
// # What a panel is made of
//
// Nothing new. Each panel takes the same tasks App already holds, asks
// them its own narrowing with the same code every task view uses
// (taskFilters.js), and draws them with the view that view is: the
// timeline is GanttChart with its chrome off, the state counts are the
// board's own grouping (board.js) with no cards under them, the list is
// the same TaskRow the flat list draws. The one panel that is not about
// the task list is the throughput trend, which asks /api/metrics -- the
// same report the Metrics pane prints (ReportBoard.jsx), at sparkline
// size.
//
// That is deliberate to the point of being the design: a panel that
// drew its own bars, or filtered tasks its own way, would be a second
// answer to a question this codebase has already answered once, and the
// two would drift within a release.
//
// # Narrowing the whole dashboard on the spot
//
// The toolbar over the grid is the same NarrowingBar every task view
// carries, over the same narrowing App holds for all of them -- so
// "this dashboard, but only acme/widgets, just for a minute" is a
// gesture here rather than an edit to the rows (grain/task-111).
//
// What it narrows is the dashboard's *question*: it stands where the
// board's stored query stood, as the fallback for a panel that carries
// none (panels.js's panelNarrowing). A panel asking its own question is
// left exactly as it was, because narrowing it further would combine
// two conditions on one attribute and silently empty it -- the reason
// the two were never composed in the first place.
//
// The throughput panel follows it under exactly those two rules
// (grain/task-317). It asks these rows nothing -- its report is
// computed by the daemon out of tables this page never holds -- but the
// narrowing it is *scoped* by is the one panelNarrowing works out for
// every panel, and report.js turns that into the repo, author and span
// GET /api/metrics takes. It used to follow nothing at all, because the
// endpoint took no scope to give it (grain/task-83 is the change that
// ended that), which made it the one panel where narrowing a dashboard
// silently did nothing.
//
// A dashboard opens on its own question, since App seeds the narrowing
// from the board row when the board is opened (its openBoard), so
// nothing about a dashboard nobody touches has changed -- and because
// the narrowing is App's, it is in the URL like every other view's, so
// a dashboard somebody has narrowed is a link they can send.
//
// # Reading a panel is one click from the full view
//
// Every panel's heading is a link to the view it is a small copy of,
// asking the same question (panels.js's panelDestination, paths.js's
// buildPath). A dashboard is for noticing something; the view it links
// to is for doing something about it, and a copyable URL is what makes
// the two the same place rather than two ways of looking that have to
// be re-narrowed by hand. Three land on the task list; the throughput
// panel lands on the Metrics pane, scoped and windowed to the report the
// sparkline is drawing, which it can be told about since grain/task-379.
//
// # Changing a dashboard
//
// "Add panel" opens a pre-written task, exactly as "New board" and
// "Edit board" do, for the reason boards.js gives at length: a
// dashboard lives in the state repository, an agent is what usually
// writes one, and a form here would be a second way of writing the same
// rows that has to be kept saying the same thing as the first.
//
// # Keeping a narrowing on the panel it was made on
//
// "Keep" is the same road, aimed at one panel (boards.js's
// keepPanelQueryTask). It appears on a panel only while the dashboard is
// narrowed to something its own board does not say -- which is exactly
// when that panel is showing the reader's question rather than the
// board's -- and the task it opens asks for that query to be written
// onto that panel's row.
//
// It is there because getting a panel's query right is iterative and
// the toolbar is where the trying happens: a reader narrows the
// dashboard until one panel says what they meant, and the gesture they
// want next is "that one, keep it", not a paragraph describing the
// narrowing they are looking at. Which panel is the reader's choice and
// not something a dashboard-wide control could make for them, so the
// control is on the panel.
//
// Two panels do not carry it. One with a query of its own is not showing
// this narrowing at all (it does not follow the toolbar, panels.js), so
// "keep" would mean silently replacing a question somebody stored with
// one they can see but are not being shown -- that is an edit to say in
// words, which is what "Edit board" is for. And a throughput panel the
// narrowing would not reach: it takes a query as a *scope* rather than
// as a filter (panels.js's panelUsesNarrowing), so a dashboard narrowed
// to a search term is one that panel is not narrowed by, and keeping it
// would write a query that changes nothing. Narrowed to a repo it is,
// and the panel offers the same word the other three do.
//
// # ...and dragging a panel into place, which is written straight through
//
// The one edit to a dashboard that is not a task at all (grain/task-368,
// panelOrder.js has the argument in full, and it is boardOrder.js's
// argument for boards word for word). A panel's place is a preference
// with no words in it; on a phone it is the whole layout, since every
// panel is full width and stacked in the board's own order. So the
// handle in a panel's header drags it, and what that writes is the
// board's `panels` array through the PATCH that already exists.
//
// # Why a picked-up panel stacks the dashboard
//
// The rail BoardList reorders is a vertical list, and a gap in it is a
// place you can point at. A panel grid is a wrapping row of thirds,
// halves and fulls, so the same "Move here" slots between panels would
// be asking a two-dimensional screen to show a one-dimensional answer --
// squeezed between two halves, or wrapped onto a line of their own,
// depending on what the row happened to hold.
//
// So picking a panel up stacks the dashboard: every panel full width,
// bodies closed, headings only (style.css's `.panel-grid-arranging`).
// That is not a different layout invented for the gesture -- it is the
// layout a phone already gets, which is the order itself with nothing
// else in the way. It also makes the slots reachable: six panels on real
// history are 2,500px of scrolling
// (the 2026-09-07 panel-dashboard hand run section 6), and choosing
// between drop targets three screens apart is not choosing.
//
// A mouse dragging a panel gets neither: the grid stays exactly as it
// is, the panels themselves are the drop targets, and the rule marking
// one is drawn down its leading edge -- the order runs left to right and
// then down, so "it lands here, before this one" is a vertical rule
// rather than the horizontal one a flat list draws. That is the same
// split every other list here makes (ReorderPickup.jsx): the HTML5 drag
// is the faster gesture on a mouse and needs no help, and the pick-up is
// the gesture that exists at all on a touchscreen and from a keyboard.
//
// The header is what is draggable, not the whole panel. A panel is a box
// with a body somebody reads, scrolls, selects text in and clicks rows
// inside; a row in a list is not. Dragging a window by its title bar is
// also what the shape already suggests.

// LIST_PANEL_ROWS is how many tasks a list panel shows before it says
// how many more there are. Eight: enough that a panel is a list rather
// than a teaser, few enough that four panels fit on a laptop screen
// without the page becoming one long scroll -- which is the whole
// difference between a dashboard and the list view.
const LIST_PANEL_ROWS = 8;

// GANTT_PANEL_ROWS is the same budget for a timeline panel: how many
// bars it draws before it says how many more its range holds
// (grain/task-311, GanttChart's own `maxRows`).
//
// A timeline panel had no budget at all, on the assumption that its
// range was one -- and a range is not one. It decides which tasks have a
// bar in the window, which on a deployment whose last week holds nine
// hundred tasks leaves nine hundred bars: a panel 400px tall with
// 14,000px of rows inside it, and 42 times its own height at a phone's
// width. Whatever the range, the answer to "how many rows fit here" is a
// property of the box, not of the calendar.
//
// Ten, because that is what the box holds: `.panel-body` is 22rem tall,
// a `.gantt-row` is 1.65rem, and the axis, the body's padding and the
// note under it come to about 4rem between them -- so ten bars is a
// panel that does not scroll at all, and eleven is one that does. Two
// more than the list panel's eight for the same reason: a bar is a
// shorter row than a task is.
const GANTT_PANEL_ROWS = 10;

// METRICS_BUCKETS is the number of points a throughput sparkline
// carries -- ReportBoard's own count, so the panel and the full report
// draw the same shape of the same series.
const METRICS_BUCKETS = 24;

// ListPanel is the flat list, cut to the top few rows. The rows are the
// list view's own (TaskRow), without the checkbox and the drag handle:
// selecting and reordering are gestures the list view offers over the
// same tasks, and a panel is a window onto them rather than a second
// place to act on them.
function ListPanel({ tasks, config, narrowing, onOpenTask }) {
  if (tasks.length === 0) return <ListEmpty>Nothing here right now.</ListEmpty>;
  // A panel carries its own sort, saved with the panel rather than
  // picked in a toolbar, so a panel that says "the backlog by priority"
  // shows the priority on its rows exactly as the list view does
  // (CustomFields.jsx's rowFields).
  const fields = rowFields(config, narrowing.sort);
  const shown = tasks.slice(0, LIST_PANEL_ROWS);
  return (
    <>
      {/* Above the rows, where the timeline panel says the same thing
          (GanttChart's "Showing 10 of 12"): a panel that stops at eight
          rows without a word reads as a list that has eight. */}
      {shown.length < tasks.length && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="p"
          className="panel-list-more"
        >
          Showing {shown.length} of {tasks.length}.
        </Typography>
      )}
      <ul className="task-list">
        {shown.map((t) => (
          <li key={t.id}>
            <TaskRow
              t={t}
              config={config}
              fields={fields}
              onOpenTask={onOpenTask}
            />
          </li>
        ))}
      </ul>
    </>
  );
}

// StatesPanel is how many tasks are in each column of the board's own
// layout -- board.js's grouping with the cards left off.
//
// Per column rather than per state, and per *this browser's* columns,
// because that is the layout its reader has already chosen for the same
// question (the same fallback a Kanban board with no columns of its own
// takes, boards.js's boardColumns). A deployment that groups "proposed"
// and "queued" into one "Waiting" column means one number by it here
// too.
//
// Each count is a link into the list narrowed to that column's states,
// on top of whatever the panel is already narrowed to: "eleven waiting"
// is a number worth clicking, and what it opens should be those eleven.
function StatesPanel({ tasks, narrowing, onOpen }) {
  const columns = useMemo(() => loadColumns() || defaultColumns(), []);
  const { cards, hidden } = groupIntoColumns(tasks, columns);
  return (
    <div className="panel-states">
      {columns.map((c) => {
        const held = cards.get(c.id) || [];
        const asked = {
          ...narrowing,
          filters: [
            ...narrowing.filters,
            { attr: "state", op: "any", values: c.states },
          ],
        };
        return (
          <a
            className="panel-state"
            key={c.id}
            href={buildPath({ view: "tasks", narrowing: asked })}
            // A real href so a count can be copied and sent, followed
            // in-app when it is simply clicked -- DetailOverlay's own
            // treatment of the link it offers.
            onClick={(e) => {
              if (
                !onOpen ||
                e.button !== 0 ||
                e.metaKey ||
                e.ctrlKey ||
                e.shiftKey ||
                e.altKey
              )
                return;
              e.preventDefault();
              onOpen({ view: "tasks", narrowing: asked });
            }}
          >
            <span className="panel-state-count">{held.length}</span>
            <span className="panel-state-title">
              {/* The dot the rest of the UI draws for the first state
                  this column collects: a column is recognised by its
                  colour on the board, and a count that dropped it would
                  be the same layout wearing a different face. */}
              <StateDot
                state={c.states[0]}
                title={STATE_LABELS[c.states[0]] || c.states[0]}
              />
              {c.title}
            </span>
          </a>
        );
      })}
      {hidden > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          className="panel-state-hidden"
        >
          {hidden} in no column
        </Typography>
      )}
    </div>
  );
}

// MetricsPanel is the throughput trend from /api/metrics: what was
// filed and finished over the panel's own window, as two sparklines and
// the totals they are read against (Sparkline auto-scales, so the line
// says which way and the number says how many -- the full report makes
// the same split).
//
// What it is a trend *of* is the panel's narrowing, turned into the
// scope the daemon computes under by the same report.js every report
// board uses (grain/task-317): a dashboard narrowed to acme/widgets
// narrows this sparkline too, because the endpoint takes ?repo=,
// ?author=, ?since= and ?until= and applies them before a number is
// taken. Everything else a narrowing can say -- a search, a state -- has
// nothing to narrow in a distribution and is dropped, which is the
// reading report.js argues at length and ReportBoard.jsx prints a line
// about.
//
// It fetches once per question and does not poll: a report costs a full
// scan of `task` and `task_run` (README, "Measuring throughput and
// latency"), and a dashboard left open on a wall would otherwise pay
// for one every few seconds.
//
// A failed fetch is said inside the panel rather than raised to the
// page's error banner: the other panels on this dashboard are fine, and
// a banner over all of them would be reporting more than went wrong.
//
// `scope` and `window` are worked out by the caller rather than here,
// because Panel needs the window itself: a panel whose row named a span
// this build could not use says so above the numbers
// (panels.js's panelRangeNote), and saying what it drew instead means
// knowing what it drew. The scope goes with it so that the two are one
// computation rather than the same parse run twice a render.
function MetricsPanel({ scope, window }) {
  const [report, setReport] = useState(null);
  const [failed, setFailed] = useState(false);
  const path = reportPath({ scope, window, buckets: METRICS_BUCKETS });

  useEffect(() => {
    let live = true;
    setFailed(false);
    api(path)
      .then((result) => {
        if (live) setReport(result);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
    // The path rather than the narrowing it was built from: a narrowing
    // is a fresh object every render (panelNarrowing parses the panel's
    // query each time), and an effect keyed on one would re-fetch the
    // report on every keystroke in the toolbar. The string is the whole
    // of what this asks for, so keying on it re-fetches exactly when
    // the question changes.
  }, [path]);

  if (failed) {
    return (
      <Typography variant="body2" color="text.secondary">
        The metrics report could not be loaded.
      </Typography>
    );
  }
  if (!report) {
    return (
      <Typography variant="body2" color="text.secondary">
        Loading…
      </Typography>
    );
  }
  const buckets = report.throughput?.buckets || [];
  return (
    // What the numbers are over, as the panel's own tooltip. Two counts
    // and two sparklines have no room for the lines ReportBoard prints
    // under a scoped report, and the alternative is a panel that
    // quietly answers a narrower question than the heading names -- or,
    // where the dashboard asked about a state or a search, a wider one
    // than every other panel beside it.
    <div className="panel-metrics" title={scopeNote(scope) || undefined}>
      <div className="panel-metric">
        <span className="panel-metric-count">
          {report.throughput?.tasksFiled ?? 0}
        </span>
        <span className="panel-metric-label">filed</span>
        <Sparkline data={buckets.map((b) => b.filed)} />
      </div>
      <div className="panel-metric">
        <span className="panel-metric-count">
          {report.throughput?.tasksCompleted ?? 0}
        </span>
        <span className="panel-metric-label">completed</span>
        <Sparkline data={buckets.map((b) => b.completed)} color="#2e7d32" />
      </div>
    </div>
  );
}

// scopeNote is that tooltip: what the report was measured over, and
// what of the question it could not use, in the wording ReportBoard
// prints on a page with room for it. "" for a report over everything,
// which needs no note because it is what a throughput panel has always
// been.
function scopeNote(scope) {
  const parts = [];
  const summary = scopeSummary(scope);
  if (summary) parts.push(`Measured over the tasks ${summary}.`);
  if (scope.ignored.length > 0) {
    parts.push(
      `A report is measured over whole distributions, so the rest of this question is not in force: ${scope.ignored.join(", ")}.`,
    );
  }
  return parts.join(" ");
}

// Panel is one panel: its heading, the link out of it, and the body its
// kind decides.
//
// `asked` is what the dashboard as a whole is asking right now -- the
// toolbar's narrowing -- which is what a panel with no query of its own
// inherits. `kept` is that same narrowing written back out as a query
// when the reader is the one who made it, and "" when the dashboard is
// still on its own question: it is what the "Keep" control is for and
// whether there is anything to keep.
function Panel({
  panel,
  asked,
  kept,
  handle,
  // The drag, in two halves: `grip` is what makes the header a drag
  // source and `target` is what makes the whole panel a place to drop
  // one, both undefined on a dashboard nobody can reorder.
  grip,
  target,
  dragging,
  over,
  picked,
  tasks,
  config,
  onOpenTask,
  onOpen,
  onKeep,
  now,
}) {
  const narrowing = panelNarrowing(panel, asked);
  // The tasks a panel is about: the same resolve/sort/match every task
  // view does, from the one place that does it (taskFilters.js's
  // narrowTasks). Worked out here even though only three of the four
  // kinds want them -- the throughput panel sends its narrowing to the
  // server as a scope rather than matching these rows against it --
  // because computing a narrowing it does not use is cheaper than four
  // branches deciding whether to.
  //
  // No useMemo around it, and not for want of trying: the one that used
  // to be here was keyed on `tasks`, which App handed over as a fresh
  // array on every three-second poll, so it recomputed every tick and
  // every panel recomputed its own copy of its neighbour's answer.
  // narrowTasks memoises on the identity of the list and the question
  // asked of it instead, which is a cache the whole dashboard shares:
  // the panels inheriting the dashboard's narrowing compute one answer
  // between them, and a poll that changed nothing computes none.
  const narrowed = narrowTasks(tasks, narrowing, config);
  const ganttView = useMemo(() => panelGanttView(panel, loadView()), [panel]);

  let body = null;
  let count = null;
  // The span this panel actually ended up drawing, for the note below:
  // undefined on the two kinds that draw no span, which need none.
  let drawn;
  switch (panel.kind) {
    case "gantt":
      body = (
        // The tasks the page holds rather than the ones narrowed above,
        // even though the count beside the heading is the narrowed
        // length: GanttChart narrows what it is given by the narrowing
        // it is given, so handing it the answer as well would be the
        // same filter run twice over 929 tasks -- and asked the same
        // question of the same list, its own call lands on the answer
        // this one just computed.
        <GanttChart
          tasks={tasks}
          config={config}
          narrowing={narrowing}
          onOpenTask={onOpenTask}
          now={now}
          embedded
          fixedView={ganttView}
          maxRows={GANTT_PANEL_ROWS}
        />
      );
      drawn = ganttView.range;
      count = narrowed.length;
      break;
    case "states":
      body = (
        <StatesPanel tasks={narrowed} narrowing={narrowing} onOpen={onOpen} />
      );
      count = narrowed.length;
      break;
    case "metrics": {
      const scope = reportScope(narrowing, attributesFor(config));
      drawn = panelMetricsWindow(panel, scope);
      body = <MetricsPanel scope={scope} window={drawn} />;
      break;
    }
    default:
      body = (
        <ListPanel
          tasks={narrowed}
          config={config}
          narrowing={narrowing}
          onOpenTask={onOpenTask}
        />
      );
      count = narrowed.length;
  }

  // Only on a panel that is showing the reader's own narrowing and
  // could store it: the comment at the top of this file says which two
  // panels those are not. `narrowing` rather than `asked` for the last
  // of them, though with no query of its own the panel they are the same
  // thing -- the question being asked here is what this panel is showing.
  const keepable =
    Boolean(onKeep) &&
    kept &&
    !panel.query &&
    panelUsesNarrowing(panel, narrowing);

  // What the row asked for and did not get. "" on every panel whose
  // range was understood, and on every panel that named none, so this
  // is chrome nobody sees until something is wrong -- which is the
  // point: the panel it appears on is the one that would otherwise look
  // exactly like a panel showing what it was asked for.
  const rangeNote = panelRangeNote(panel, drawn);
  // The same for the panel's own question: a condition in it naming an
  // attribute this deployment does not have is dropped (taskFilters.js's
  // unknownConditions), and a panel narrowed by nothing else would show
  // every task without a word. Only for a panel's own query -- one that
  // inherits the dashboard's narrowing has the toolbar above it saying
  // the same thing -- and only once config has arrived, before which a
  // declared field is not an attribute yet.
  const unknownNote =
    panel.query && config
      ? describeUnknownConditions(
          unknownConditions(narrowing.filters, attributesFor(config)),
        )
      : "";

  return (
    <section
      className={`panel panel-${panel.width}${dragging ? " panel-dragging" : ""}${over ? " panel-drop-target" : ""}${picked ? " panel-picked" : ""}`}
      {...target}
    >
      <header
        className={`panel-header${grip ? " panel-header-grip" : ""}`}
        {...grip}
      >
        {/* The handle is the thing inside the header a mouse grabs:
            pressing it starts the header's own drag, and a finger's
            press is answered by the pick-up gesture instead
            (ReorderPickup.jsx). */}
        {handle}
        <h3 className="panel-title">{panelTitle(panel)}</h3>
        {count !== null && <span className="count">{count}</span>}
        {keepable && (
          <button
            type="button"
            className="panel-keep"
            // Named for the reader who cannot see which panel this
            // button sits in: a dashboard can have several of them at
            // once, and "Keep" on its own would be several buttons with
            // one name. The title says what will happen, since the
            // gesture writes nothing itself -- it opens the task that
            // does.
            aria-label={`Keep this narrowing on “${panelTitle(panel)}”`}
            title={`File the task that makes \`${kept}\` this panel's own question`}
            onClick={() => onKeep(panel)}
          >
            Keep
          </button>
        )}
        <a
          className="panel-open"
          href={panelPath(panel, asked)}
          // An anchor is a drag source of its own, and this one sits
          // inside a header that is the panel's: without this, dragging
          // from "Open" hands the browser a URL to drop somewhere
          // instead of moving the panel. Refusing it lets the drag fall
          // through to the header, which is the gesture that was meant.
          draggable={false}
          onClick={(e) => {
            if (
              !onOpen ||
              e.button !== 0 ||
              e.metaKey ||
              e.ctrlKey ||
              e.shiftKey ||
              e.altKey
            )
              return;
            e.preventDefault();
            // The destination the href was built from, so the in-app
            // click and the copyable link open the same thing -- the
            // list for three kinds, the report for the throughput one.
            onOpen(panelDestination(panel, asked));
          }}
        >
          Open
        </a>
      </header>
      {rangeNote && (
        <Typography
          variant="caption"
          color="text.secondary"
          className="panel-range-note"
        >
          {rangeNote}
        </Typography>
      )}
      {unknownNote && (
        <Typography
          variant="caption"
          color="warning.main"
          className="panel-unknown-note"
          title="This condition is not applied, so the panel is not narrowed by it"
        >
          {unknownNote} (not applied).
        </Typography>
      )}
      <div className="panel-body">{body}</div>
    </section>
  );
}

export default function PanelBoard({
  board,
  tasks,
  config,
  narrowing = NO_NARROWING,
  // A dashboard rendered without one is a dashboard whose toolbar
  // changes nothing -- the props default so that a caller showing a
  // board can leave the narrowing to App, as every task view does.
  onNarrow = () => {},
  onOpenTask,
  onOpen,
  onAddPanel,
  onKeepPanelQuery,
  // onMovePanel(board, key, beforeKey) is the drop: put this panel
  // immediately above that one, or at the end of the dashboard when
  // beforeKey is null (panelOrder.js). Left out, the dashboard draws no
  // handles and nothing is draggable -- a dashboard rendered by a caller
  // that cannot write is a dashboard to read.
  onMovePanel,
  now = Date.now(),
}) {
  const panels = board.panels || [];
  // Worked out once for the whole grid rather than per panel: it is a
  // question about the dashboard ("has its reader narrowed it away from
  // what the board says?"), and every panel gets the same answer.
  const kept = keptQuery(board, narrowing, config);

  // The two halves of the gesture, held exactly as BoardList holds them.
  // dragKey is the panel a mouse is dragging; overKey is only the
  // highlight -- which target the pointer is over -- and never decides
  // the move, which happens in the drop alone.
  const [dragKey, setDragKey] = useState(null);
  const [overKey, setOverKey] = useState(null);
  const enabled = !!onMovePanel;
  const { picked, toggle, cancel } = usePickup();
  const pickedPresent =
    enabled &&
    Boolean(picked?.ids?.length) &&
    panels.some((p) => p.key === picked.ids[0]);
  useEffect(() => {
    if (!enabled || (picked && !pickedPresent)) cancel();
  }, [enabled, picked, pickedPresent, cancel]);
  // A panel is named by its position (panels.js), so a pick-up made on
  // one dashboard means a different panel on the next one. Opening
  // another board puts it back down.
  useEffect(() => cancel, [board.id, cancel]);
  const pickedKey = pickedPresent ? picked.ids[0] : null;
  const renderKeys = childRenderKeys(panels);

  const stop = () => {
    setDragKey(null);
    setOverKey(null);
  };
  const drop = (beforeKey) => {
    if (dragKey !== null) onMovePanel(board, dragKey, beforeKey);
    stop();
  };
  const move = (beforeKey) => {
    if (pickedKey !== null) onMovePanel(board, pickedKey, beforeKey);
    cancel();
  };

  // Where the picked-up panel is allowed to land. Its own place is not
  // among them: a slot that promises a move and writes nothing reads as
  // a broken button.
  const places = pickedKey !== null ? panelPlaces(panels, pickedKey) : [];
  const offered = new Set(places);
  const slot = (beforeKey, label) =>
    pickedKey !== null && offered.has(beforeKey) ? (
      <MoveSlot
        as="div"
        className="reorder-slot-panel"
        label={label}
        onMove={() => move(beforeKey)}
      />
    ) : null;

  // What a slot, a handle and the bar call a panel. The number is its
  // place in the dashboard, one-based, which is how a prompt about a
  // panel numbers them too (boards.js's panelLines) -- and it is what
  // tells two panels apart when they carry the same heading, or none,
  // which nothing else about a panel row can do.
  const panelName = (panel, i) => `panel ${i + 1}, “${panelTitle(panel)}”`;

  const dragOver = (key) => (e) => {
    if (dragKey === null) return;
    e.preventDefault();
    setOverKey(key);
  };
  const onDrop = (beforeKey) => (e) => {
    e.preventDefault();
    drop(beforeKey);
  };

  return (
    <div className="panel-board">
      {/* The toolbar, with "Add panel" at the end of its row -- where
          the list's "show closed tasks" and the board's "Columns" sit,
          since each view puts its own control there (NarrowingBar's
          children). Drawn whether or not this deployment has any tasks
          yet: the button that fills an empty dashboard is on it. */}
      <NarrowingBar
        tasks={tasks}
        config={config}
        narrowing={narrowing}
        onNarrow={onNarrow}
        // What this dashboard is narrowed by before anybody asks it
        // anything -- the deployment's own condition, the same one the
        // task list opens on (grain/task-309). Clear puts the toolbar
        // back to that rather than to nothing at all, for the reason it
        // does on the list: clearing to nothing fills every panel with
        // closed tasks, and it is the one narrowing no link can carry,
        // so a panel's "Open" would stop landing on what the panel
        // shows.
        defaults={defaultFilters(config)}
        searchPlaceholder="Search this dashboard…"
      >
        <Button size="small" onClick={() => onAddPanel?.(board)}>
          Add panel
        </Button>
      </NarrowingBar>
      {pickedKey !== null && (
        <MovingBar
          count={1}
          noun="panel"
          nowhere={places.length === 0}
          onCancel={cancel}
        />
      )}
      {panels.length === 0 ? (
        <ListEmpty>
          This dashboard has no panels yet. “Add panel” files the task that adds
          one.
        </ListEmpty>
      ) : (
        // Stacked while a panel is up, so the slots between the panels
        // are a column of drop targets rather than gaps wedged into a
        // wrapping row -- the section at the top of this file has the
        // argument.
        <div
          className={`panel-grid${pickedKey !== null ? " panel-grid-arranging" : ""}`}
        >
          {panels.map((panel, i) => (
            <Fragment key={renderKeys[i]}>
              {slot(panel.key, `Move here, above ${panelName(panel, i)}`)}
              <Panel
                panel={panel}
                asked={narrowing}
                kept={kept}
                handle={
                  enabled ? (
                    <MoveHandle
                      label={
                        pickedKey === panel.key
                          ? `Stop moving ${panelName(panel, i)}`
                          : `Move ${panelName(panel, i)}`
                      }
                      picked={pickedKey === panel.key}
                      disabled={panelPlaces(panels, panel.key).length === 0}
                      onToggle={() => toggle([panel.key])}
                    />
                  ) : null
                }
                grip={
                  enabled
                    ? {
                        draggable: true,
                        onDragStart: () => setDragKey(panel.key),
                        onDragEnd: stop,
                      }
                    : undefined
                }
                target={
                  enabled
                    ? {
                        onDragOver: dragOver(panel.key),
                        onDrop: onDrop(panel.key),
                      }
                    : undefined
                }
                dragging={dragKey === panel.key}
                over={dragKey !== null && overKey === panel.key}
                picked={pickedKey === panel.key}
                tasks={tasks}
                config={config}
                onOpenTask={onOpenTask}
                onOpen={onOpen}
                onKeep={
                  onKeepPanelQuery
                    ? (p) => onKeepPanelQuery(board, p)
                    : undefined
                }
                now={now}
              />
            </Fragment>
          ))}
          {slot(null, "Move here, at the end of the dashboard")}
          {/* "Last on the dashboard", for a mouse. Only while a drag is
              in flight, the way BoardList's own trailing zone is: the
              last panel's own drop target means "above it", so without
              this there is no way to drag anything past it. */}
          {enabled && dragKey !== null && (
            <div
              className={`panel-drop-end${
                overKey === "__end__" ? " panel-drop-target" : ""
              }`}
              onDragOver={dragOver("__end__")}
              onDrop={onDrop(null)}
            />
          )}
        </div>
      )}
    </div>
  );
}
