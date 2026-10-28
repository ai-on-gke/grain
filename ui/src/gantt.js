// gantt.js is the timeline view's own arithmetic and its own settings:
// where a task's bar starts and stops, which stretch of the calendar the
// chart is showing, where the ticks under it fall, and how the rows are
// grouped into lanes (GanttChart.jsx).
//
// # The question this view answers
//
// The flat list answers "what is the backlog, in order". The board
// answers "where is everything right now". The inbox answers "what is
// waiting on me". None of them answers the fourth question a deployment
// gets asked -- *when* -- and it is the one somebody asks with a
// calendar already open: what ran last night, what has been sitting in
// the queue since Tuesday, whether those four tasks on one repo actually
// overlapped or merely look like they did in a list sorted by id.
//
// A Gantt chart is the answer because every one of those is a length
// rather than a badge. A task on this view is a bar from when it was
// filed to when it stopped being work in progress, drawn against a time
// axis every other bar shares, with the moment it was first dispatched
// marked on it (model.TaskSpan, and ui.Task's startedAt/endedAt). The
// stretch before that mark is the wait; the stretch after it is the
// work; a bar with no end is still going, and its length is a number
// that grows while you look at it.
//
// # What is deliberately not here
//
// Dependency arrows, and dragging a bar to move it. Both are what a
// planning tool's Gantt is *for*, and neither means anything here: a
// task's dates are not a plan somebody set, they are a record of what
// dispatch did, and there is no write behind "start this on Thursday"
// for a dragged bar to make -- the same reason a card cannot be dragged
// between the board's columns (board.js). Tasks do have depends-on links
// and they are on screen, as the "Blocked" chip a list row, a card and a
// timeline row all carry; what is not drawn is an arrow claiming one bar's end scheduled
// the next one's start, which is not how dispatch chooses.
//
// # Where the settings live
//
// localStorage, under GANTT_STORAGE_KEY, exactly as the board's columns
// do (board.js's own "Where they are kept, and why not in the daemon"):
// the range somebody wants to look at and the lanes they want it grouped
// into are how one person is reading the deployment today, not a fact
// about the deployment. Nothing behind /api/config changes when the
// chart is switched from 7 days to 30, so the whole feature stays a
// frontend change with no settings key and no migration behind it -- and
// normalizeView below is what makes a stored value from an older build
// safe to read back.
import { STATE_LABELS, STATE_ORDER, displayState } from "./state.js";
import { formatDate, formatHour, zoneOffset } from "./time.js";

export const GANTT_STORAGE_KEY = "grain.gantt.view";

const MINUTE = 60 * 1000;
const HOUR = 60 * MINUTE;
export const DAY = 24 * HOUR;

// RANGES is how far back the chart looks. `ms` is the width of the
// window that ends at the right-hand edge; null is "all", which sizes
// itself to whatever the tasks on screen actually cover.
//
// The four fixed windows are report.js's own REPORT_WINDOWS,
// deliberately: an operator who reads a 7-day report and then opens the
// timeline to see
// what those numbers were made of should be looking at the same seven
// days, and two vocabularies for one span would make that a conversion.
export const RANGES = [
  { id: "24h", label: "Last 24 hours", ms: DAY },
  { id: "7d", label: "Last 7 days", ms: 7 * DAY },
  { id: "30d", label: "Last 30 days", ms: 30 * DAY },
  { id: "90d", label: "Last 90 days", ms: 90 * DAY },
  { id: "all", label: "Everything", ms: null },
];

// LANES is how the bars are grouped. "none" is one flat run of rows in
// the narrowing's own sort order, which is the chart at its simplest;
// the other three each answer a question the flat version cannot -- "did
// this repo have a quiet week", "how much of yesterday was one task
// failing over and over", "what has the schedule been filing at 3am".
//
// Grouping is a lane per value, not a filter: a task with no value for
// the attribute gets the "—" lane rather than falling off the chart, the
// same way the narrowing's own conditions treat a missing value as a
// value (taskFilters.js's NONE).
export const LANES = [
  { id: "none", label: "No lanes" },
  { id: "repo", label: "By repo" },
  { id: "state", label: "By state" },
  { id: "author", label: "By author" },
];

export const DEFAULT_VIEW = { range: "7d", lanes: "none" };

// defaultView hands back a fresh object every time, board.js's
// defaultColumns' own reason: the caller holds it as state and edits it.
export function defaultView() {
  return { ...DEFAULT_VIEW };
}

// normalizeView turns whatever was stored into a view this build can
// render, falling back one field at a time rather than all at once: a
// browser holding a range this build has dropped and a lane it still has
// keeps the lane. Anything that is not an object at all is not a view,
// and normalizes to the default entire.
export function normalizeView(raw) {
  const stored = raw && typeof raw === "object" ? raw : {};
  const range = RANGES.some((r) => r.id === stored.range)
    ? stored.range
    : DEFAULT_VIEW.range;
  const lanes = LANES.some((l) => l.id === stored.lanes)
    ? stored.lanes
    : DEFAULT_VIEW.lanes;
  return { range, lanes };
}

// loadView is what this browser last chose, or the default. Anything
// unreadable -- a half-written value, localStorage denied outright --
// is "nothing stored yet" rather than an error somebody has to see, the
// treatment board.js's loadColumns gives the same failure.
export function loadView() {
  let stored = null;
  try {
    stored = localStorage.getItem(GANTT_STORAGE_KEY);
  } catch {
    return defaultView();
  }
  if (!stored) return defaultView();
  try {
    return normalizeView(JSON.parse(stored));
  } catch {
    return defaultView();
  }
}

// saveView writes the choice back, and clears the key when it is the
// default -- so a chart nobody has touched follows whatever this file
// calls the default rather than being pinned to today's copy of it
// (board.js's saveColumns, same reasoning).
export function saveView(view) {
  try {
    const v = normalizeView(view);
    if (v.range === DEFAULT_VIEW.range && v.lanes === DEFAULT_VIEW.lanes)
      localStorage.removeItem(GANTT_STORAGE_KEY);
    else localStorage.setItem(GANTT_STORAGE_KEY, JSON.stringify(v));
  } catch {
    // The chart still shows what was asked for; this browser just
    // forgets it on reload. Not worth an error banner over a preference.
  }
}

// # A task's own bar
//
// spanOf is one task as three instants:
//
//   from     when it was filed (createdAt), which is when it started
//            costing somebody something
//   started  when its first attempt began, or null for a task that has
//            never been dispatched
//   to       when it stopped being work in progress (endedAt), or `now`
//            for a task that has not
//
// `ongoing` is that last distinction, because it is the one a reader
// has to see: a bar that ends at the right-hand edge because the task
// finished then and a bar that ends there because it is still running
// are opposite facts, and the chart draws them differently.
//
// A task with no createdAt at all -- nothing in the store lacks one, but
// the field is optional on the wire -- falls back to its first attempt,
// and then to nothing: null, meaning "not drawable", rather than a bar
// pinned to the epoch or to now.
export function spanOf(task, now) {
  const from = ms(task.createdAt) ?? ms(task.startedAt);
  if (from === null) return null;
  const started = ms(task.startedAt);
  const ended = ms(task.endedAt);
  // An ending before the filing, or a start before it, is a clock
  // disagreeing with itself somewhere upstream. Clamped rather than
  // dropped: the bar is still worth drawing, and a negative width is not
  // a thing CSS can render.
  const to = Math.max(ended ?? now, from);
  return {
    id: task.id,
    from,
    started: started === null ? null : Math.min(Math.max(started, from), to),
    to,
    ongoing: ended === null,
  };
}

function ms(value) {
  if (!value) return null;
  const at = new Date(value).getTime();
  return Number.isNaN(at) ? null : at;
}

// windowOf is the stretch of calendar the chart is showing: {from, to}
// in milliseconds.
//
// For a fixed range it ends at `now` and runs back by the range's own
// width -- a window that ends at the last task's ending would slide
// about as tasks finish, and "the last 7 days" has to mean the same
// thing every time it is asked for.
//
// "Everything" is the other reading: the earliest thing on screen to the
// latest, so a deployment being looked at for the first time gets a
// chart that fits rather than an empty one and a scrollbar. It still
// ends no earlier than `now` when anything is still running, since an
// ongoing bar runs to the current moment by definition. With no tasks at
// all it falls back to a day ending now, which is an axis to draw rather
// than a division by zero.
export function windowOf(spans, range, now) {
  const chosen = RANGES.find((r) => r.id === range) || RANGES[0];
  if (chosen.ms !== null) return { from: now - chosen.ms, to: now };
  if (spans.length === 0) return { from: now - DAY, to: now };
  let from = Infinity;
  let to = -Infinity;
  for (const s of spans) {
    from = Math.min(from, s.from);
    to = Math.max(to, s.to);
  }
  // A single instant is not a window: a store holding one task filed and
  // finished in the same second would otherwise divide by a zero width.
  if (to - from < HOUR) to = from + HOUR;
  return { from, to };
}

// inWindow is whether a bar has anything to draw inside the window at
// all -- any overlap, not containment, since the tasks worth seeing on a
// 24-hour chart include the one that has been running since Monday.
export function inWindow(span, window) {
  return span.to >= window.from && span.from <= window.to;
}

// placeOf turns a span into the percentages the bar is drawn at, already
// clipped to the window: `left` and `width` for the whole bar, `wait`
// for how much of that width is the stretch before the first attempt
// began, and `clippedStart`/`clippedEnd` for whether the bar runs off
// the edge -- which the chart marks, so a bar that fills the width reads
// as "longer than this window" rather than "exactly this long".
//
// Percentages rather than pixels because the chart has no idea how wide
// it is: it is laid out by CSS, in a pane whose width changes with the
// window, the rail and whether a phone is holding it.
export function placeOf(span, window) {
  const width = window.to - window.from;
  const from = Math.max(span.from, window.from);
  const to = Math.min(span.to, window.to);
  const left = ((from - window.from) / width) * 100;
  const right = ((to - window.from) / width) * 100;
  const started = span.started === null ? from : Math.max(span.started, from);
  return {
    left,
    // A bar for a task that lived a few seconds would otherwise be
    // invisible: a floor of a fifth of a percent keeps every task on
    // screen findable, which is the point of drawing it at all.
    width: Math.max(right - left, 0.2),
    wait: Math.max(((Math.min(started, to) - from) / width) * 100, 0),
    clippedStart: span.from < window.from,
    clippedEnd: span.to > window.to,
  };
}

// nowAt is where the current moment falls in the window, as a
// percentage, or null when it falls outside it -- a chart of last month
// has no "now" line to draw, and drawing it at the edge would claim the
// window ends now when it does not.
export function nowAt(window, now) {
  if (now < window.from || now > window.to) return null;
  return ((now - window.from) / (window.to - window.from)) * 100;
}

// # The axis
//
// STEPS is the ladder of tick spacings, coarsest last. The chosen one is
// the finest that leaves at most MAX_TICKS labels on the axis: more than
// that and the labels collide at any width a browser has.
const STEPS = [
  HOUR,
  2 * HOUR,
  3 * HOUR,
  6 * HOUR,
  12 * HOUR,
  DAY,
  2 * DAY,
  7 * DAY,
  14 * DAY,
  28 * DAY,
  91 * DAY,
];
const MAX_TICKS = 8;

// ticksFor is the axis under the chart: one {at, left, label, day} per
// tick, positioned as a percentage the same way the bars are.
//
// Ticks are anchored to midnight in the *deployment's* zone rather than
// to the window's own left edge, so they land on times somebody can
// name -- "Sep 3", "06:00" -- instead of on whatever instant the page
// happened to load at. That is the same clock every other timestamp in
// the UI is printed against (time.js), and the reason zoneOffset lives
// there rather than here.
//
// `day` marks a tick that falls on a day boundary, which is what lets
// the chart label it with the date even when the spacing is hourly: an
// axis of eight times of day with no date on it says nothing about which
// day any of them is.
export function ticksFor(window, zone, step = stepFor(window)) {
  const out = [];
  const width = window.to - window.from;
  if (!(width > 0)) return out;
  let at = startOfDay(window.from, zone);
  // Walk forward from the midnight at or before the window rather than
  // from the window's own edge, so a tick's position is a property of
  // the calendar and not of when this was rendered.
  while (at < window.from) at += step;
  for (; at <= window.to && out.length <= MAX_TICKS + 2; at += step) {
    const day = at === startOfDay(at, zone);
    out.push({
      at,
      left: ((at - window.from) / width) * 100,
      label: day || step >= DAY ? formatDate(at, zone) : formatHour(at, zone),
      day,
    });
  }
  return out;
}

// stepFor is that ladder applied to one window. A window wider than the
// coarsest rung gets an even division of its own rather than hundreds of
// quarterly ticks -- "Everything" on a deployment years old is a real
// window and it still needs an axis.
export function stepFor(window) {
  const width = window.to - window.from;
  for (const step of STEPS) if (width / step <= MAX_TICKS) return step;
  return Math.ceil(width / MAX_TICKS);
}

// startOfDay is midnight in `zone` at or before `at` -- the anchor every
// tick is measured from.
//
// The offset is read twice for taskFilters.js's own reason: an offset is
// read *at* an instant, so a first guess taken on one side of a
// daylight-saving change lands an hour out, and the second reading taken
// at the first answer is within an hour of the truth.
export function startOfDay(at, zone) {
  const local = at + zoneOffset(at, zone);
  const midnight = Math.floor(local / DAY) * DAY;
  const first = midnight - zoneOffset(midnight, zone);
  return midnight - zoneOffset(first, zone);
}

// # Lanes
//
// laneOf is which lane a task belongs to under the current grouping: an
// {key, label} pair, since a repo groups by its own name and a state
// groups by an id whose label is somewhere else.
export function laneOf(task, lanes) {
  switch (lanes) {
    case "repo":
      return { key: task.repo || "", label: task.repo || "No repo" };
    case "state":
      return {
        // Both asked states are one lane, "Asked" (state.js's displayState).
        key: displayState(task.state || ""),
        label: STATE_LABELS[task.state] || task.state || "No state",
      };
    case "author":
      return { key: task.author || "", label: task.author || "No author" };
    default:
      return { key: "", label: "" };
  }
}

// groupIntoLanes drops the given tasks -- already sorted, already
// narrowed, already known to have something to draw -- into their lanes,
// keeping the order they arrived in within each. board.js's
// groupIntoColumns for a chart, with one difference: the lanes are not
// declared anywhere, so they are discovered from the tasks themselves.
//
// Which means the lane order has to come from somewhere. States take
// STATE_ORDER, the same order they appear in everywhere else in the UI;
// everything else is alphabetical, with the "no value" lane last, since
// a lane of tasks that are missing the thing being grouped by is the
// footnote and not the headline.
//
// "No lanes" comes back as a single unnamed lane rather than as a
// special case for the caller: one code path draws every grouping.
export function groupIntoLanes(tasks, lanes) {
  if (lanes === "none" || !LANES.some((l) => l.id === lanes))
    return [{ key: "", label: "", tasks }];
  const byKey = new Map();
  for (const t of tasks) {
    const lane = laneOf(t, lanes);
    if (!byKey.has(lane.key)) byKey.set(lane.key, { ...lane, tasks: [] });
    byKey.get(lane.key).tasks.push(t);
  }
  const out = [...byKey.values()];
  const rank =
    lanes === "state"
      ? (l) => {
          const i = STATE_ORDER.indexOf(l.key);
          return i < 0 ? STATE_ORDER.length : i;
        }
      : null;
  out.sort((a, b) => {
    if (a.key === "") return 1;
    if (b.key === "") return -1;
    if (rank) return rank(a) - rank(b) || a.label.localeCompare(b.label);
    return a.label.localeCompare(b.label);
  });
  return out;
}

// # What a bar says when you point at it
//
// describeSpan is the bar's own tooltip: how long the task waited, how
// long it has been running or ran, and whether it is still going. It is
// text rather than a rendered popover because it is a `title` attribute
// -- available to a keyboard, a screen reader and a hover alike, with no
// second thing to keep on screen.
export function describeSpan(span, now) {
  const parts = [];
  if (span.started !== null) {
    parts.push(`waited ${duration(span.started - span.from)}`);
    parts.push(
      span.ongoing
        ? `running for ${duration(now - span.started)}`
        : `ran for ${duration(span.to - span.started)}`,
    );
  } else {
    parts.push(
      span.ongoing
        ? `waiting for ${duration(now - span.from)}, never dispatched`
        : `never dispatched, ${duration(span.to - span.from)} old when it ended`,
    );
  }
  return parts.join(", ");
}

// duration is a length of time as somebody says one out loud. Coarser
// the longer it gets, for metricsFormat.js's formatSeconds' own reason:
// nobody reading a three-day wait needs the minutes on the end of it.
export function duration(msSpan) {
  const s = Math.max(Math.round(msSpan / 1000), 0);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    return m ? `${h}h ${m}m` : `${h}h`;
  }
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  return h ? `${d}d ${h}h` : `${d}d`;
}
