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

// panels.js is everything the UI knows about a dashboard's panels: which
// kinds of panel there are, what each one is narrowed to, how wide it
// sits, and where clicking through from one lands (grain/task-88).
//
// A panel board is the one kind of board that is not a single view of
// tasks filling the pane (boards.js): it is several views at once, each
// asking its own question. The pieces a panel is drawn from are already
// here -- taskFilters.js for the question, gantt.js and board.js for the
// two panels that do their own arithmetic, paths.js for the link -- so
// what this file adds is the vocabulary and the falling back, not a
// second way of doing any of it.
//
// # Where a panel is kept, and why not in localStorage
//
// The Kanban columns and the timeline's range are browser settings
// (board.js's own "Where they are kept, and why not in the daemon").
// A dashboard is not, and model.BoardPanel's doc comment is where the
// argument is written down in full: it is asked for by name, it is
// meant to be there tomorrow and on somebody else's screen, it is the
// first thing here anybody wants to share as a link -- and the thing
// usually asked to build one is an agent, which cannot reach a
// browser's storage at all. So the panels are rows of `board_panel` in
// the store, arriving with the board over /api/boards, and this file
// only ever reads them.
//
// What stays in the browser is what was already there: a timeline
// panel's lanes and the columns the state counts are grouped into are
// the reader's own, exactly as they are on the full views.
import { NO_NARROWING } from "./taskFilters.js";
import { narrowingFromQuery, buildPath } from "./paths.js";
import { DAY, RANGES, normalizeView } from "./gantt.js";
import {
  DEFAULT_REPORT_WINDOW,
  hasAbsoluteSpan,
  reportScope,
  reportWindow,
} from "./report.js";

// PANEL_KINDS is every panel a dashboard can be made of: the word the
// store holds, what to call it, and which full view "open this panel"
// leads to.
//
// Three lead to the task list. The timeline and the state counts led to
// /gantt and /board while those were destinations of their own; both are
// gone (grain/task-162), and there is no fixed chart or fixed set of
// columns left to hand a narrowing to -- so "open this panel" gives what
// it can still honestly give, which is the same tasks as the list.
//
// The throughput panel leads to the report, which is what it is a small
// copy of. It went to the list for a while (grain/task-317): it had
// started carrying a scope, and /metrics was the whole deployment's
// report with no way to be told about one, so a link there answered a
// wider question than the panel it was clicked on. The Metrics pane
// takes a scope of its own now (grain/task-379, paths.js's
// `metricsNarrowing`), so the link is exact again and panelDestination
// below is what carries the panel's question and its span onto it.
//
// "metrics" is that pane rather than one of App's own views -- it is an
// overlay with a path, like /settings -- so it is the one value here
// panelDestination turns into a pane rather than a view.
//
// Four, and no Kanban one: a panel wanting its own columns would need a
// child table hanging off a child row, and a Kanban board is already a
// board of its own -- so every kind here is one that needs nothing but
// a query. `states` is the count per column of the same layout the
// board draws, which is the Kanban question minus the cards.
//
// Each is a value here, a renderer in PanelBoard.jsx and a word in
// model.PanelKinds; adding a fifth is those three lines, the same deal
// boards.js's own BOARD_KINDS makes.
export const PANEL_KINDS = [
  { id: "list", label: "Task list", view: "tasks" },
  { id: "gantt", label: "Timeline", view: "tasks" },
  { id: "states", label: "State counts", view: "tasks" },
  { id: "metrics", label: "Throughput", view: "metrics" },
];

export const PANEL_KINDS_BY_ID = Object.fromEntries(
  PANEL_KINDS.map((k) => [k.id, k]),
);

// PANEL_WIDTHS is how much of a row a panel takes. Three widths rather
// than a column count or a pixel size: a dashboard is read on a laptop
// and on a phone, the panels reflow, and "half the row" is something
// somebody can mean on both.
export const PANEL_WIDTHS = ["third", "half", "full"];

// DEFAULT_WIDTH is what a panel that does not say gets. Half: a
// dashboard of two-up panels is the shape most of them want, and a
// panel that wanted the whole row says so.
export const DEFAULT_WIDTH = "half";

// METRICS_RANGES is which of the timeline's ranges a throughput panel
// can be asked for -- every fixed window, but not "Everything": a
// report is computed over a window that ends now (pkg/metrics), and
// "all" is a span the chart sizes to its own bars rather than one the
// API takes. The four that remain are report.js's own REPORT_WINDOWS,
// deliberately the same vocabulary (gantt.js's RANGES says why).
export const METRICS_RANGES = RANGES.filter((r) => r.ms !== null).map(
  (r) => r.id,
);

// METRICS_RANGE_DAYS is each of those spans as a whole number of days,
// which is the only way a narrowing can say one: a `created` condition
// counts days ("within the last 30"), and that condition is how a
// panel's own range travels in the link its heading opens
// (panelReportNarrowing). Derived from the ranges themselves so the two
// vocabularies cannot drift into disagreeing about how long "30d" is.
const METRICS_RANGE_DAYS = Object.fromEntries(
  RANGES.filter((r) => r.ms !== null).map((r) => [
    r.id,
    Math.round(r.ms / DAY),
  ]),
);

// DEFAULT_METRICS_RANGE is the default every report here shares, so a
// panel that does not say shows the same week `grain metrics` prints
// with no flags.
export const DEFAULT_METRICS_RANGE = DEFAULT_REPORT_WINDOW;

// panelRanges is the vocabulary one kind's `range` takes, and null for
// the kinds that draw no span at all -- the same split model.
// PanelRangesFor makes on the other side of the API, since `grain state
// check` has to know it too (pkg/staterepo/vocabulary.go).
export function panelRanges(kind) {
  if (kind === "gantt") return RANGES.map((r) => r.id);
  if (kind === "metrics") return METRICS_RANGES;
  return null;
}

export function panelKindLabel(kind) {
  return PANEL_KINDS_BY_ID[kind]?.label || PANEL_KINDS[0].label;
}

// normalizePanel is what stands between a row somebody hand-edited into
// the state repository and a render -- normalizeBoard's own job for the
// rows underneath a board. A panel can name a kind this build has never
// heard of, a width that is not a width, or nothing at all; none of
// that is worth a blank dashboard, so each field falls back on its own
// (gantt.js's normalizeView, same treatment).
//
// A panel whose kind is unknown falls back to the list, which is the
// panel that can be drawn from tasks alone -- a dashboard written for a
// newer build should show the tasks each panel is about rather than a
// hole where the panel was.
export function normalizePanel(raw, index = 0) {
  const p = raw && typeof raw === "object" ? raw : {};
  const kind = PANEL_KINDS_BY_ID[p.kind] ? p.kind : "list";
  return {
    // A key for the render, and nothing else: panels have no identity
    // of their own outside their board (the `id` on the row is the
    // store's own autoincrement, not something anybody names).
    key: `panel-${index}`,
    kind,
    title: typeof p.title === "string" ? p.title : "",
    query: typeof p.query === "string" ? p.query : "",
    range: typeof p.range === "string" ? p.range : "",
    width: PANEL_WIDTHS.includes(p.width) ? p.width : DEFAULT_WIDTH,
    // Where the panel sits among its board's own panels. Nothing here
    // draws with it -- the panels arrive in this order already
    // (model.ListBoards orders them) -- and it is kept because it is
    // the one field of a panel that names it: a panel has no id
    // anybody uses, so a prompt asking for *this* panel to be changed
    // says which row it means with the board's id and this key
    // (boards.js's panelLines), and dragging one into place writes it
    // (panelOrder.js).
    orderKey: typeof p.orderKey === "number" ? p.orderKey : 0,
    // The row as the store actually holds it, for the one caller that
    // writes the panels back: a reorder sends the board's whole
    // `panels` array (panelOrder.js says why), so every panel on the
    // dashboard is rewritten from what this build is holding, including
    // the ones nobody touched.
    //
    // The fallbacks above are for *drawing* a row this build cannot
    // name -- a panel whose kind is a word a newer build knows is drawn
    // as a list rather than as a hole. Writing that fallback back would
    // turn a temporary misreading into the row, so the rewrite sends
    // the words that were there. The server rejects a kind it does not
    // know (model.Board.Validate), so a dashboard holding one is a
    // dashboard whose reorder fails with a banner -- which is the right
    // answer: an older build should refuse to move a panel it cannot
    // understand rather than flatten it on the way past.
    stored: {
      kind: typeof p.kind === "string" ? p.kind : "",
      title: typeof p.title === "string" ? p.title : "",
      query: typeof p.query === "string" ? p.query : "",
      range: typeof p.range === "string" ? p.range : "",
      width: typeof p.width === "string" ? p.width : "",
    },
  };
}

// panelRow is a normalized panel back in the shape the API takes
// (ui.BoardPanel), at the order key it is being given. It is what a
// reorder sends, one per panel of the board, and it is here rather than
// in panelOrder.js because the shape of the row is this file's business
// -- normalizePanel is what took it apart.
export function panelRow(panel, orderKey) {
  return { ...(panel?.stored || {}), orderKey };
}

export function normalizePanels(raw) {
  if (!Array.isArray(raw)) return [];
  return raw.map(normalizePanel);
}

// panelTitle is the heading a panel draws: its own, or the name of what
// it is -- so a panel that is exactly what it looks like needs no title
// invented for it.
export function panelTitle(panel) {
  return panel?.title || panelKindLabel(panel?.kind);
}

// panelNarrowing is the question one panel asks: its own query, or the
// dashboard's when it has none.
//
// The dashboard's, and not the dashboard's *and* its own, which would
// mean combining two narrowings -- and two conditions on one attribute
// intersect, so "the board is about acme/widgets" and "this panel is
// about failed tasks" would compose in one obvious case and silently
// contradict in another ("either repo" narrowed by "the other repo" is
// nothing at all). A panel that means both says both, which is one
// query somebody can read, and the dashboard's own question is what a
// panel that adds nothing inherits.
//
// # The dashboard's question is the live one, not the stored one
//
// `fallback` is a narrowing rather than the board row it started as:
// what a dashboard is asking at this moment is App's narrowing, which
// is seeded from the board's stored query when the board is opened and
// is then the reader's to change from the toolbar over the panel grid
// (PanelBoard.jsx, grain/task-111). So "this dashboard, but only
// acme/widgets, just for a minute" narrows every panel that inherits,
// and leaves alone every panel that carries a question of its own --
// which is the same rule as before, asked of a narrowing that can move.
//
// A panel with its own query is deliberately not narrowed further by
// the toolbar, for the reason two narrowings are not combined above: it
// would silently empty a panel whose own question mentions the same
// attribute, and a panel that says what it is about is the one thing on
// the dashboard that has already answered the question the toolbar is
// asking.
//
// A query this build can make nothing of -- a hand-edited row's `&&` --
// is a query that named nothing, and falls back the way an empty one
// does rather than becoming a panel over every task. That panel's
// "Open" link would carry nothing either, and a link carrying nothing
// opens the list on the deployment's default (paths.js, boards.js's
// boardNarrowing): a panel showing more than the list it opens is the
// gap grain/task-309 closed.
//
// # The throughput panel is no exception (grain/task-317)
//
// This is every panel's question, the throughput panel's included. That
// panel does not ask these tasks anything -- it asks GET /api/metrics,
// which computes over rows this page never holds -- but the narrowing
// it is scoped by is worked out right here, and report.js turns it into
// the repo, author and span the endpoint takes. So the same two rules
// hold for it as for the other three: a panel with no query of its own
// follows the toolbar, and a panel that carries one keeps asking that
// and is not narrowed further.
//
// The second half is worth saying out loud for this panel rather than
// inheriting by accident, because the reason is not the same one. For a
// list panel, intersecting would silently empty it. For a report there
// is no emptying to fear -- metrics.Scope would happily compute a
// report over no tasks at all -- but the answer would be a set of
// numbers nobody asked for: "the throughput of acme/widgets" narrowed
// by a toolbar saying acme/gadgets is a report over nothing, printed as
// zeroes rather than as an empty list somebody can see is empty. A
// panel that has said what it measures keeps measuring it.
export function panelNarrowing(panel, fallback) {
  const own = panel?.query ? narrowingFromQuery(panel.query) : null;
  return own || fallback || NO_NARROWING;
}

// panelGanttView is the range and lanes a timeline panel draws at: the
// panel's own range if it names one this build has, over whatever lanes
// this browser last chose on the full timeline. The split is the one
// the board already makes -- what the dashboard is *about* is the
// board's, how the reader likes to look at it is the browser's -- and
// normalizeView is what makes a dropped range fall back rather than
// draw nothing.
//
// The falling back is not silent any more: panelRangeNote below is the
// line the panel prints when the word it was given was not one of
// these, which on this kind especially matters -- the fallback is the
// *browser's* last range, so a mistyped panel draws a different window
// for every reader and looks healthy to all of them.
export function panelGanttView(panel, stored) {
  const base = normalizeView(stored);
  const range = RANGES.some((r) => r.id === panel?.range)
    ? panel.range
    : base.range;
  return { range, lanes: base.lanes };
}

// panelMetricsWindow is the span a throughput panel measures over: the
// panel's own range when it names one the report can be asked for,
// otherwise whatever span the narrowing it is scoped by names, and
// otherwise the same week `grain metrics` prints with no flags. A range
// it cannot ask about ("all") falls back the same way a dropped one
// does.
//
// `scope` is report.js's reading of that narrowing, where a `created`
// condition is the report's window rather than a filter on filing dates
// (report.js's own "A date range is read as the report's window").
//
// The panel's own range wins over the dashboard's for the same reason
// its own query does, and the pair is deliberately one rule: what a
// panel says about itself -- the question and the span -- is what it
// goes on saying while the toolbar moves, and a panel that says neither
// follows the dashboard in both.
//
// An absolute span is the one case the panel's range does not win, and
// it is not decided here: a narrowing naming actual dates is not a
// window at all, and reportPath sends it in place of one because it is
// the more specific thing to have said.
export function panelMetricsWindow(panel, scope) {
  if (METRICS_RANGES.includes(panel?.range)) return panel.range;
  return reportWindow(scope, DEFAULT_METRICS_RANGE);
}

// panelRangeNote is what a panel says when the `range` on its row was a
// word this build could not use: which word, and what it drew instead.
// "" for the ordinary panel, which is every panel whose range was
// understood and every panel that named none.
//
// # Why a panel says this at all
//
// Because the fallback above is invisible, and the common reason for
// one is a typo rather than a build. A dashboard panel is a row
// hand-written into the state repository by an agent or a person
// (model.BoardPanel), `panelGanttView` falls back to whatever range the
// browser last chose on the full timeline, and the result is a panel
// drawing a window nobody asked for while looking entirely healthy --
// no empty body, no error, a heading that still says what it meant.
//
// That is not hypothetical. A hand run seeded a dashboard with a second
// timeline panel "ranged to a single day", measured it as identical to
// the seven-day one, and concluded from that that a panel's range does
// not decide what it draws (the 2026-09-07 panel-dashboard hand run,
// section 5). The row said `"1d"`; the day-wide range is `"24h"`. A
// person measuring the thing, looking straight at it, was wrong about
// it for a whole section, and this line is what would have told them.
//
// The precedent for saying it rather than swallowing it is on this same
// page: a report scoped by a query it could only partly use names the
// conditions it dropped (ReportBoard.jsx, PanelBoard.jsx's scopeNote)
// instead of quietly answering a narrower question. This is the same
// bargain for a narrower field.
//
// `grain state check` says it earlier, on the pull request the row
// arrives in (pkg/staterepo/vocabulary.go), and that is the better
// place to catch it -- but only for a row that arrives that way, and
// only for somebody who reads the check's output. This one is for the
// reader in front of the panel, which is who the hand run's measurer
// was.
//
// `drawn` is the range the panel actually rendered at, which its caller
// knows and this does not: panelGanttView's answer for a timeline,
// panelMetricsWindow's for a throughput panel. Undefined for the kinds
// that draw no span, which need no "instead" because they drew nothing.
export function panelRangeNote(panel, drawn) {
  const asked = panel?.range;
  if (!asked) return "";
  const kind = PANEL_KINDS_BY_ID[panel.kind] ? panel.kind : "list";
  const allowed = panelRanges(kind);
  // A range on a list or a state count. Not a fallback at all -- there
  // is nothing for it to fall back to -- so the note says the value is
  // not read, which is the whole of what went wrong: somebody meant
  // something by writing it.
  if (allowed === null) {
    return `A ${panelKindLabel(kind).toLowerCase()} panel covers no span, so the range “${asked}” on this one is not read.`;
  }
  if (allowed.includes(asked)) return "";
  const instead = rangeLabel(drawn);
  // "all" on a throughput panel is the one case that is not a typo: a
  // range this build has, that a report cannot be measured over,
  // because a report is computed over a window that ends now
  // (panelMetricsWindow's own note). Told it was not a range, its
  // author would go looking for a misspelling that is not there.
  if (RANGES.some((r) => r.id === asked)) {
    return `“${asked}” is not a span a report can be measured over, so this panel is showing ${instead}.`;
  }
  return `“${asked}” is not a range this build has, so this panel is showing ${instead}.`;
}

// rangeLabel is a range as the picker on the full timeline names it,
// lowercased to sit inside a sentence -- "the last 7 days" rather than
// a bare "7d", since the reader of this note is being told what they
// are looking at rather than which word to write.
function rangeLabel(range) {
  const found = RANGES.find((r) => r.id === range);
  if (!found) return "the default span";
  if (found.ms === null) return "everything it holds";
  return `the ${found.label.toLowerCase()}`;
}

// panelDestination is where "open this panel" goes, in the shape
// buildPath takes and App can act on directly: the full view of the
// tasks it is about, asking the same question -- or, for the throughput
// panel, the Metrics pane over the report it is drawing.
//
// That is the whole point of a panel being a narrowing rather than a
// screenshot: the four-row list on the dashboard and the list it came
// from are one URL apart, and that URL can be copied and sent. Which
// destination that is, per kind, is PANEL_KINDS above.
//
// # The throughput panel opens its own report again
//
// It landed on /metrics carrying nothing while the report was the
// deployment's whole output and took no filter. Then the endpoint grew
// a scope (grain/task-83) and the panel started sending one
// (grain/task-317), and the link had to move to the task list: /metrics
// was still the deployment-wide report, so a link there from a panel
// about acme/widgets answered a wider question than the panel it was
// clicked on. The list was honest but adjacent -- it is the panel's
// *question* rather than the report's window, and a `created` range
// means filing dates to a list and the span to measure to a report.
//
// The Metrics pane takes a scope of its own now (grain/task-379), so
// the link is the report the panel is drawing: the panel's narrowing as
// the pane's scope, and the panel's own range with it. Both halves are
// needed for that to be exact -- panelMetricsWindow answers "over what
// span" and panelNarrowing "over which tasks", and a link carrying one
// without the other would open a report the panel is not showing.
export function panelDestination(panel, fallback) {
  if (panelView(panel) === "metrics") {
    return {
      view: "system",
      systemTab: "metrics",
      metricsNarrowing: panelReportNarrowing(panel, fallback),
    };
  }
  return {
    view: panelView(panel),
    narrowing: panelNarrowing(panel, fallback),
  };
}

// panelPath is that destination as an ordinary link (paths.js), which
// is what the heading is: a URL somebody can copy, send and open cold.
export function panelPath(panel, fallback) {
  return buildPath(panelDestination(panel, fallback));
}

// panelReportNarrowing is the whole of what a throughput panel's link
// says: the narrowing the panel is scoped by, carrying the panel's own
// range as the span to measure.
//
// The range goes in as a `created` condition because that is the one
// thing a narrowing has to say a span with, and report.js reads it as
// the report's window rather than as a filter on filing dates -- which
// is the reading that makes the pane draw what the panel drew. It
// replaces any relative span already in the narrowing for the reason
// panelMetricsWindow prefers it: what a panel says about itself is what
// it goes on saying while the toolbar moves.
//
// A narrowing naming actual dates is left exactly as it is, the one
// case panelMetricsWindow does not decide either: an absolute span is
// the more specific thing to have said, and reportPath sends it in
// place of a window at both ends of the link.
export function panelReportNarrowing(panel, fallback) {
  const narrowing = panelNarrowing(panel, fallback);
  const days = METRICS_RANGE_DAYS[panel?.range];
  if (!days || hasAbsoluteSpan(reportScope(narrowing))) return narrowing;
  return {
    ...narrowing,
    filters: [
      ...(narrowing.filters || []).filter(
        (c) => !(c?.attr === "created" && c.op === "within"),
      ),
      { attr: "created", op: "within", values: [String(days)] },
    ],
  };
}

// panelUsesNarrowing is whether writing *this* narrowing onto this
// panel's row would mean anything. It is here for the one gesture that
// offers to write a panel's query -- "keep this narrowing",
// PanelBoard.jsx -- which should not be offered where keeping would
// change nothing on screen.
//
// Three of the four kinds narrow the tasks they draw, so any narrowing
// at all is a narrowing they show and the panel is the whole question.
//
// The throughput panel is the one that has to be asked about the
// narrowing as well, and it did not used to take one at all: since
// grain/task-317 it carries a query like the rest, but read as a *scope*
// rather than as a filter, and report.js keeps three things out of one
// -- the repo, the author and the span -- and drops everything else. So
// a dashboard narrowed to a search term, or to a state, is a dashboard
// this panel is not narrowed by, and offering to keep that here would
// file a task to write a query that changes nothing the reader can see.
// Narrowed to a repo, it is, and the offer is real.
export function panelUsesNarrowing(panel, narrowing) {
  if (panel?.kind !== "metrics") return true;
  const scope = reportScope(narrowing);
  return Boolean(
    scope.repos.length ||
    scope.authors.length ||
    scope.within ||
    scope.since ||
    scope.until,
  );
}

// panelView is what a panel is a small copy of -- one of App's own
// views, or the Metrics pane for the panel that draws a report. It is
// what panelDestination turns into a path, and what a click on the
// heading opens without a page load.
export function panelView(panel) {
  return (PANEL_KINDS_BY_ID[panel?.kind] || PANEL_KINDS[0]).view;
}
