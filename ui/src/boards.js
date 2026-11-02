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

// boards.js is everything the UI knows about a saved dashboard: which
// kinds there are, how a folder's worth of them is grouped for the nav
// rail, how a stored board becomes a narrowing the task views already
// understand -- and the task prompts that are how a board is made and
// changed (grain/task-48).
//
// # A board is a row in grain's database, not a browser setting
//
// The Kanban board's own columns live in localStorage (board.js) because
// they are how one person wants to look at the deployment. A board is
// not: it is asked for by name ("give me a dashboard of everything
// waiting on me in acme/widgets"), it is meant to be there tomorrow and
// on the other machine, and -- the part that settles it -- the thing
// asked to build one is usually an agent, which has a checkout and a git
// remote and no way at all into a browser's storage. So boards are
// pkg/model's own `board` table, served from /api/boards, and exported
// to the state repository like every other setting.
//
// # Which is why a board is edited by filing a task
//
// The buttons on the boards pages do not open a form that writes a
// board. They open the new-task form with a task already written in it:
// a task against the state repository, describing the board to add or
// the change to make, in the words and the row shape an agent needs.
// Merging that pull request is what makes the board appear, on the tick
// after it lands.
//
// That is a longer road than a form would be, and it is deliberately the
// road: it is the one an agent is already on. The alternative -- a form
// here and an agent-facing path over there -- is two ways to write a
// board that have to be kept saying the same thing. This way the UI's
// job is to make the request easy to state, which is what
// newBoardTask/editBoardTask below do: they carry the board's own
// values, and the narrowing the reader is looking at this moment, into
// the prompt so that "save what I am looking at as a board" is a
// sentence somebody clicks rather than one they compose.
//
// With one exception, which boardOrder.js argues for in full: where a
// board *sits* -- its folder and its order within it -- is dragged in
// the rail or on the boards page and written straight through
// PATCH /api/boards/{id} (grain/task-81). Everything above is about a
// board's content, which somebody has to say in words; a board's place
// is a preference with no words in it, and asking an agent to merge a
// pull request to move a row up one is how it came to be that nobody
// ever moved one.
import { narrowingFromQuery, queryFromNarrowing } from "./paths.js";
import { defaultColumns, normalizeColumns } from "./board.js";
import { NO_NARROWING, defaultNarrowing } from "./taskFilters.js";
import {
  METRICS_RANGES,
  PANEL_KINDS,
  PANEL_WIDTHS,
  normalizePanels,
  panelRanges,
  panelTitle,
} from "./panels.js";
import { RANGES } from "./gantt.js";
import {
  REPORT_SECTIONS,
  REPORT_WINDOWS,
  normalizeSections,
} from "./report.js";

// The kinds of dashboard there are, and what to call them. Each is a
// value here, a renderer in App.jsx's own board branch and a word in
// pkg/model's own BoardKind; nothing else about a board changes, which
// is exactly what "gantt" cost when the timeline became a kind
// (grain/task-55).
//
// "Report" is the fifth and the first that is not a view of the backlog
// at all (grain/task-83): it draws GET /api/metrics -- throughput,
// latency, how attempts ended -- rather than task rows, and it is the
// case that tested whether the claim above holds for a kind that shares
// none of the machinery. It does: the board row is the same row, and
// what the kind needed was a reading of the query (report.js turns the
// repo, the author and the dates into a scope the daemon computes under,
// and says out loud what it could not use) and rows of its own for the
// sections it prints, which is board_column's own answer for the third
// time running.
//
// "Panels" is the odd one and the reason for the fourth line
// (grain/task-88): every other kind is one view filling the pane, and a
// panels board is several of them at once, each narrowed to its own
// question (panels.js, PanelBoard.jsx). It is a kind rather than a
// destination of its own because everything a board already carries --
// a name, a folder, a place in the rail, a query, a link -- is what a
// dashboard needs, and a second thing that is nearly a board would have
// to grow all of it again.
//
// "Tree" is the backlog nested by who filed what (TaskTree.jsx,
// workTree.js): each task under the task whose run filed it, which is
// how a chat's or a design's whole tree of work is read at a glance --
// the task page's own tree (docs/work-trees.md), over whatever the
// board's query narrows to.
//
// "Timeline" rather than "Gantt" for the label, because that is what the
// nav rail already calls the same view and a board's chip should say the
// word the reader has seen.
export const BOARD_KINDS = [
  { id: "list", label: "List" },
  { id: "kanban", label: "Kanban" },
  { id: "gantt", label: "Timeline" },
  { id: "panels", label: "Panels" },
  { id: "report", label: "Report" },
  { id: "tree", label: "Tree" },
];

export const BOARD_KIND_LABELS = Object.fromEntries(
  BOARD_KINDS.map((k) => [k.id, k.label]),
);

export function boardKindLabel(kind) {
  return BOARD_KIND_LABELS[kind] || "List";
}

// kindForView is which kind of board "New board" offers, given the view
// the reader was looking at when they clicked it. Saving what is on
// screen is the gesture this whole feature is for (newBoardTask below),
// and half of what is on screen is the layout: somebody who has narrowed
// a timeline board and clicked "+" means a timeline of those tasks, not
// a list of them.
//
// A board page is the only view with a layout to carry now. The fixed
// board and timeline on the rail used to be two more -- "+" from /board
// asked for a kanban board -- and they are gone (grain/task-162), which
// leaves this as "the board being read, or the list a board is by
// default". The other kinds are still one word away: the prompt names
// all four (kindsNote below), because the rail's "+" is now where a
// reader who wants a Kanban board asks for one, and having stood on a
// Kanban board first is no longer something they can do.
export function kindForView(view, board) {
  if (view === "boards") return board?.kind || "list";
  return "list";
}

// normalizeBoard is what stands between a row somebody hand-edited into
// the state repository and a render. A board can be written by an agent
// editing tables/board.json, so this build can be handed a kind it does
// not have, a missing name, columns that are not columns -- none of
// which is worth a blank screen. Everything unrecognized falls back the
// way board.js's own normalizeColumns falls back.
export function normalizeBoard(raw) {
  if (!raw || typeof raw !== "object") return null;
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!id) return null;
  const kind = BOARD_KIND_LABELS[raw.kind] ? raw.kind : "list";
  return {
    id,
    name: typeof raw.name === "string" && raw.name ? raw.name : id,
    folder: typeof raw.folder === "string" ? raw.folder : "",
    kind,
    description: typeof raw.description === "string" ? raw.description : "",
    query: typeof raw.query === "string" ? raw.query : "",
    columns: Array.isArray(raw.columns) ? raw.columns : [],
    // The panels come back normalized rather than raw, unlike the
    // columns above (boardColumns below is where those are made safe):
    // a panel is read by four different renderers, and each of them
    // falling back on its own would be four places to get it wrong.
    panels: normalizePanels(raw.panels),
    // The sections a report board prints, normalized here for the
    // panels' own reason: several renderers read them, and each falling
    // back on its own would be several places to get it wrong
    // (report.js's normalizeSections).
    sections: normalizeSections(raw.sections),
    orderKey: typeof raw.orderKey === "number" ? raw.orderKey : 0,
  };
}

export function normalizeBoards(raw) {
  if (!Array.isArray(raw)) return [];
  return raw.map(normalizeBoard).filter(Boolean);
}

// boardFolders groups boards for the nav rail: the loose ones first --
// a board nobody filed is a board at the top level -- then one group per
// folder, in the order the server lists them (by folder, then by each
// board's own place within it, pkg/model's ListBoards).
//
// A folder is nothing but a name a board carries. There is no folder to
// create, none to delete and none to leave empty: it exists exactly as
// long as a board names it, which is what makes "put this board in
// Reports" a one-field change an agent can make without inventing a
// second kind of row.
export function boardFolders(boards) {
  const loose = [];
  const folders = [];
  const byName = new Map();
  for (const b of boards) {
    if (!b.folder) {
      loose.push(b);
      continue;
    }
    let group = byName.get(b.folder);
    if (!group) {
      group = { folder: b.folder, boards: [] };
      byName.set(b.folder, group);
      folders.push(group);
    }
    group.boards.push(b);
  }
  return { loose, folders };
}

// LIST_KINDS are the kinds of board that are the task list: the saved
// list itself, and the dashboard whose panels are windows onto it and
// whose every "Open" link lands on it (panels.js's panelPath). They are
// the kinds boardNarrowing lets the deployment's own default answer for,
// and the note there says why the others answer for themselves.
const LIST_KINDS = new Set(["list", "panels"]);

// boardNarrowing is the question a board asks, in the shape every task
// view here already takes: the board's stored query, read by the very
// parser that opens a link somebody pasted (paths.js) -- and, for a
// board that names no question, null, which is that parser's own answer
// for a query asking nothing and which App reads as "nobody has asked"
// (taskFilters.js's defaultNarrowing).
//
// # A board that says nothing (grain/task-309)
//
// A board's query is stored in the address bar's own encoding and read
// by the address bar's own parser, so it means what the address bar
// means by it: a query naming nothing is not "every task", it is a view
// nobody has narrowed, and the deployment's default answers for it --
// the same answer "/" has always had, and the one the README argues for
// under "What a link written before that change now means".
//
// It used to mean every task, which put a dashboard and the list its
// panels link to on two different questions: on a deployment with real
// history a panel counted 929 tasks and its "Open" link opened a list of
// 11, because 918 of them were closed and only the list was leaving
// those out. The count on a panel header was a count of tasks nobody
// could then read. One reading now, for the query in a board row and the
// query in a link alike -- and a board that does mean "every task,
// closed ones included" says so the way a link does, by naming a
// question with no state condition in it (`sort=created:desc` is the
// shortest).
//
// # Why only these two kinds
//
// Because the default condition is the answer for a view that has no
// answer of its own about which tasks to leave out, and the other kinds
// do have one. A Kanban board says which states it is about
// in its columns, the way the nav rail's state entries do (App's
// selectStateFilter) -- narrowing one away from a column it draws would
// leave that column empty with nothing but a chip to say why. A timeline
// is a chart of when work happened, which is mostly work that has
// finished. And a report is not a view of tasks at all: it counts every
// task whatever its state (report.js's reportScope has no use for a
// state condition and says so under the heading), so handing it the
// default would put a chip on its toolbar and a "not in force" line
// under its heading about a condition that changes none of its numbers.
// A tree is the work a task filed, done and undone alike, and counts
// both the way a task's own page does ("n of m done").
//
// A panel of one of those kinds is a different matter and does inherit:
// what a dashboard's panels are about is the dashboard's question
// (panels.js's panelNarrowing), and every one of them links back to the
// list.
export function boardNarrowing(board) {
  const asked = board?.query ? narrowingFromQuery(board.query) : null;
  if (asked) return asked;
  return LIST_KINDS.has(board?.kind || "list") ? null : NO_NARROWING;
}

// boardColumns is which columns a Kanban board draws: its own, when it
// carries any, and otherwise whatever this browser is set to
// (board.js). A board asked for as "a Kanban board of my open work"
// should not have to invent a column layout to be a board, and the one
// in the browser is the layout its reader has already chosen.
export function boardColumns(board) {
  // Only rows that are shaped like a column at all: this list can come
  // from a hand-edited dump, and a string where a column should be must
  // not turn into an "Untitled" column collecting nothing.
  const own = (board?.columns || []).filter(
    (c) => c && typeof c === "object" && (c.name || Array.isArray(c.states)),
  );
  if (own.length === 0) return null;
  // A stored column is {name, states}; board.js's own column is
  // {id, title, states}, and its id is a key for the render and nothing
  // else -- so it is made here rather than stored.
  return (
    normalizeColumns(
      own.map((c, i) => ({
        id: `${board.id}-${i}`,
        title: c.name,
        states: c.states,
      })),
    ) || defaultColumns()
  );
}

// STATE_REPO_NOTE is the paragraph both prompts end with: where a board
// actually lives and what makes the change take effect. Written for an
// agent that has never seen a board before and has the state repository
// checked out in front of it.
const STATE_REPO_NOTE = `grain keeps boards in its own database and exports every table to this
repository, so a board is a row in \`tables/board.json\`. Add or edit the
row there, matching the shape of the rows already in that file. A kanban
board's columns are rows in \`tables/board_column.json\`, each naming the
board's id, a column name, a comma-separated \`states\` list and an
\`order_key\` deciding where it sits; a report board's sections are rows in
\`tables/board_section.json\` the same way.

Give a new board an id nothing else in the file uses, of the form
\`board-<something-short>\` (a word rather than a number: grain hands out
\`board-1\`, \`board-2\` and so on itself, and an id shaped like those can
collide with one it issues later).

\`board\` is a settings table, so once this pull request merges grain
pulls it down and the board appears within the minute -- no restart. The
repository's own check (\`grain state check .\`) runs on the pull request
and will say so if the rows do not load.`;

// QUERY_NOTE explains the one field of a board that is not just a word:
// its query. Kept in both prompts because it is the field an agent is
// most likely to try to invent a syntax for, and the answer -- "it is
// the query string off the address bar" -- is both simpler than any
// syntax it would invent and exactly what the reader can copy.
const QUERY_NOTE = `A board's \`query\` is the filtering and sorting, written exactly as grain's
own UI writes it into the address bar: \`q=<search>\`, \`sort=<attribute>:<asc|desc>\`
(comma-separated for tie-breakers) and one \`f=<attribute>:<comparison>:<values>\`
per condition, values separated by \`|\`. An empty query is a board nobody has
narrowed: a list board or a dashboard of panels shows what this deployment's
task list shows by default (today, everything that is not deferred or closed),
and a Kanban board, a timeline, a tree or a report shows every task, since each
of those answers the question itself. A board meaning "every task, closed ones
included" says so with a query naming no condition on state --
\`sort=created:desc\` is the shortest.`;

// SHOWN_QUERY_NOTE answers the question both dashboard prompts now
// invite: why the query they carry has a condition on state in it that
// the reader never typed.
//
// Because it is on screen. A dashboard with no query of its own opens on
// the deployment's default (grain/task-309), so a reader who narrows one
// is narrowing that, and what the toolbar reads is the pair. A panel row
// is where the two stop being a pair: a panel carrying its own query is
// read exactly as written and given no default (panels.js's
// panelNarrowing), so a query with the state condition dropped out of it
// is a wider panel than the one the reader was looking at when they
// asked for this.
const SHOWN_QUERY_NOTE = `The query is the whole of what is on screen, this deployment's own default
condition on state included. Keep it: a panel that carries a query of its own
is read exactly as written and gets no default, so a query with that condition
left out is a panel showing closed and deferred tasks the reader who asked for
it was not being shown.`;

// kindLine describes a kind for a prompt: the word grain stores, and
// what it means, since an agent reading "kanban" should not have to
// guess whether that is a column layout or a list.
//
// The panels and report lines point at the note that prompt carries
// about that kind -- but only a prompt about that kind carries it, so
// `noted` says whether this one does. The menu of kinds every new-board
// prompt lists named both notes whatever the prompt was about, which on
// a list board's prompt sent an agent "below" to two sections that were
// not there.
function kindLine(kind, noted = true) {
  switch (kind) {
    case "kanban":
      return "kanban (columns of states; leave the columns out to use whatever columns the reader's browser is set to)";
    case "gantt":
      return "gantt (the timeline: one bar per task against a clock, from when it was filed to when it stopped being work in progress; its range and its grouping are the reader's own browser setting rather than the board's)";
    case "panels":
      return `panels (a dashboard of several panels at once, each with its own question${noted ? " -- see the panels section below" : ""})`;
    case "tree":
      return "tree (each task nested under the task whose run filed it, the way a task's own page draws its tree of work)";
    case "report":
      return `report (the measurement report -- throughput, latency, how attempts ended, what is still waiting -- over the tasks the query names, rather than a view of the tasks themselves${noted ? "; see the report section below" : ""})`;
    default:
      return "list (the flat task list)";
  }
}

// kindsNote is the menu the "New board" prompt carries: every layout a
// board can be, written from BOARD_KINDS so a kind added to this build
// cannot leave the prompt short of one.
//
// It is in the prompt at all because of what the rail's "+" now has to
// stand in for (grain/task-162). The kind line above it is a suggestion
// -- the layout the reader was looking at (kindForView) -- and while
// the Kanban board and the timeline were destinations of their own,
// standing on one and clicking "+" was how somebody asked for a board
// of that shape. Now the only way to ask for one is to say so, and
// somebody who has never read this file should not have to know that
// the word for "columns of states" is `kanban`.
function kindsNote(kind) {
  return `The kinds a board can be, if it should not be the one above:

${BOARD_KINDS.map((k) => `- ${kindLine(k.id, k.id === kind)}`).join("\n")}`;
}

// PANELS_NOTE is the paragraph a prompt about a dashboard ends with:
// what a panel is, which words its two closed vocabularies take, and
// the file the rows live in. Written from PANEL_KINDS and PANEL_WIDTHS
// rather than spelled out, so a kind added to this build cannot leave a
// prompt telling an agent it does not exist.
//
// It says what an `order_key` is because a panel added to a dashboard
// has to be written between the two it goes between, and the panels a
// prompt lists carry theirs for the same reason (panelLines). Moving one
// is no longer a task at all -- panels are dragged into their order the
// way boards are (panelOrder.js, grain/task-368) -- so this note is
// about the row an agent is adding rather than about a reorder anybody
// should be filing.

// TIMELINE_ONLY_RANGES is the ranges a timeline takes and a throughput
// panel does not -- "all" today, which a report cannot be measured over
// (panels.js's METRICS_RANGES). Worked out from the two lists rather
// than written into the sentence, PANELS_NOTE's own rule: the one thing
// a prompt about a closed vocabulary must never do is name a word this
// build has stopped having, or leave out one it has gained.
const TIMELINE_ONLY_RANGES = (() => {
  const only = RANGES.map((r) => r.id).filter(
    (r) => !METRICS_RANGES.includes(r),
  );
  if (only.length === 0) return "";
  return ` (and ${only.map((r) => `\`${r}\``).join(", ")} on a timeline, which a report cannot be measured over)`;
})();

const PANELS_NOTE = `A \`panels\` board is a dashboard: several panels on one page, each with
its own question. The panels are rows in \`tables/board_panel.json\`, each
naming the board's id in \`board_id\` and carrying:

- \`kind\`: ${PANEL_KINDS.map((k) => `\`${k.id}\` (${k.label.toLowerCase()})`).join(", ")}
- \`title\`: the panel's heading, or empty for the name of its kind
- \`query\`: what this panel is narrowed to, in the same encoding as a
  board's own query; empty means whatever the dashboard as a whole is
  asking -- the board's own query, or what its reader has since narrowed
  it to with the toolbar over the panels
- \`range\`: for \`gantt\` and \`metrics\`, the span it covers -- ${METRICS_RANGES.map(
  (r) => `\`${r}\``,
).join(", ")}${TIMELINE_ONLY_RANGES}, or empty for the default.
  Those words are exact and nothing refuses a wrong one: a range this
  build does not have falls back to the default rather than failing, so
  a panel written \`1d\` draws the default span rather than a day.
  \`grain state check\` warns about one, and the panel itself says which
  word it could not use
- \`width\`: ${PANEL_WIDTHS.map((w) => `\`${w}\``).join(", ")} of the row; empty for \`${PANEL_WIDTHS[1]}\`
- \`order_key\`: where it sits among the board's own panels, ascending

A \`metrics\` panel is the one that is not a view of these tasks: it asks
\`GET /api/metrics\`, so its query is read as a *scope* rather than as a
filter. \`repo\`, \`author\` and a date range on \`created\` narrow the
report; anything else -- a search, a state, a sort -- has nothing to
narrow in a distribution and is dropped. A date range there is the span
to measure over rather than when a task was filed, and \`range\` above is
that span when the query names none.

The order keys need not be contiguous or evenly spaced, and the order is
the whole of a dashboard's layout on a phone, where every panel is full
width and stacked. So moving a panel is a change to that one field on
that one row -- give it a number between the keys of the two panels it
should sit between, or below the first one to put it at the top --
rather than a renumbering of the panels around it.`;

// REPORT_NOTE is what a prompt about a report board carries
// (grain/task-83). It has to say three things an agent cannot work out
// from the rows in front of it, and the order they are in is the order
// they will be wondered about.
//
// First, that this kind is not a view of the task list, because
// everything else in these prompts is and an agent that assumed so would
// write a query full of states.
//
// Second -- the paragraph that matters -- what the query *does* here.
// The field is the same field, and a report can only be computed under
// conditions the daemon can narrow rows by, so three of them count and
// the rest are dropped. Saying which three, in the encoding the query
// already uses, is the difference between a board that reports on
// acme/widgets and one that reports on the whole deployment under a
// heading that says otherwise. The date reading is spelled out for the
// same reason it is spelled out in report.js: it is not literal.
//
// Third, the sections, written from REPORT_SECTIONS so a section this
// build gains cannot leave the prompt describing one fewer -- and the
// fact that leaving them out is a complete answer rather than an
// unfinished one, since the likeliest request an agent gets is "a report
// board for acme/widgets" with no layout in it at all.
const REPORT_NOTE = `A \`report\` board is not a view of the task list: it draws grain's own
measurement report (\`GET /api/metrics\` -- throughput, latency, how
attempts ended, what is still waiting) over the tasks its query names.

Its \`query\` is the same field every other board carries, in the same
encoding, but a report can only be narrowed by the conditions the daemon
can measure under. Three count:

- \`f=repo:any:<owner/name>\` -- which repos the report is about (several
  values separated by \`|\` for "any of these")
- \`f=author:any:<who>\` -- whose work it is about
- \`f=created:within:<days>\`, \`f=created:after:<date>\` or
  \`f=created:before:<date>\` -- the span the report measures over

Everything else in the query (a search, a state, a sort) is ignored, and
the board's page says which conditions it could not use rather than
pretending. A report board with an empty query is the whole deployment.

The date conditions become the report's *window*, which bounds the
measurements rather than the rows: a task filed in March and completed
inside the window contributes its whole lead time to it. That is the
reading somebody asking for "the last thirty days" means, and it is not
the same set of tasks \`created:within:30\` picks on a task list.

Which sections the board prints are rows in \`tables/board_section.json\`,
each naming the board's id in \`board_id\` and carrying:

- \`kind\`: one of ${REPORT_SECTIONS.map((s) => `\`${s.id}\` (${s.summary})`).join(", ")}
- \`title\`: the heading over it, or empty for the name of the section
- \`order_key\`: where it sits among the board's own sections, ascending

Leave those rows out entirely and the board prints the whole report in
that order, which is a complete answer: a report board only needs
sections when somebody has asked for a shorter report than all of it.`;

// panelLines describes the panels a board has today, for a prompt about
// changing one: an agent asked to add a fourth panel needs to know what
// the three are and what order keys they took.
//
// The order keys because they are what the *fourth* row has to be
// written between, and because they are the only thing that names an
// existing panel: a panel row has no id anybody chose (panels.js), so
// "the third panel, `order_key` 3145728, on board-4" is as precisely as
// a prompt can point at one. `mark` is the panel a prompt is about, if
// it is about one, and the arrow saying so is worth more than another
// sentence of prose describing which row is meant.
//
// A panel with no query of its own is said out loud to follow the
// dashboard rather than left with a blank where the others carry a
// query: an empty field an agent is reading is exactly where it might
// otherwise decide something is missing and fill it in.
//
// The throughput panel is listed like the other three, which it was not
// while it ignored a query outright. Since grain/task-317 it carries one
// like the rest and follows the dashboard without one; what is different
// about it is only which parts of a query it can use, and PANELS_NOTE
// says that once, in full, rather than each row saying it again.
function panelLines(board, mark = -1) {
  const panels = board?.panels || [];
  if (panels.length === 0) return "(none yet -- this dashboard is empty)";
  return panels
    .map((p, i) => {
      const question = p.query
        ? `narrowed to \`${p.query}\``
        : "following the dashboard's own question";
      return `${i + 1}. ${p.kind}${p.title ? ` "${p.title}"` : ""}, ${p.width} width${panelRangeText(p)}, ${question}, \`order_key\` ${p.orderKey ?? 0}${i === mark ? "   <-- this one" : ""}`;
    })
    .join("\n");
}

// panelRangeText is one panel's range as a prompt lists it -- and, when
// it is a word this build could not use, the fact that it could not.
//
// A prompt listing a dashboard's panels is the one place an agent finds
// out what is already there, and until now it copied `range` out
// verbatim: a row saying `1d` was listed as a one-day panel beside a
// seven-day one, which is precisely the reading that has already misled
// a person measuring the real thing
// (the 2026-09-07 panel-dashboard hand run, section 5). An agent
// asked to add a panel "like the others" would go on to write the same
// word again, and an agent asked to fix the range would have no reason
// to think this one was broken.
//
// The vocabulary is asked for rather than assumed (panels.js's
// panelRanges), so a range on a kind that covers no span is called what
// it is too: not a typo, a field nothing reads.
function panelRangeText(p) {
  if (!p.range) return "";
  const allowed = panelRanges(p.kind);
  if (allowed === null) {
    return `, \`range\` \`${p.range}\` (a ${p.kind} panel covers no span, so nothing reads this)`;
  }
  if (!allowed.includes(p.range)) {
    return `, \`range\` \`${p.range}\` (not one this build has, so it draws over the default span)`;
  }
  return `, ${p.range}`;
}

// sectionLines describes the sections a report board prints today, for a
// prompt about changing one -- panelLines' own job for the other kind
// with child rows. "The whole report" rather than a list when it names
// none, because that is what such a board actually draws.
function sectionLines(board) {
  const sections = board?.sections || [];
  if (sections.length === 0) {
    return "(none named, so it prints the whole report in its own order)";
  }
  return sections
    .map((s, i) => `${i + 1}. ${s.kind}${s.title ? ` "${s.title}"` : ""}`)
    .join("\n");
}

// columnLines describes the columns a Kanban board draws today, for a
// prompt about changing one -- sectionLines' job for the third kind with
// child rows. A board carrying columns of its own draws those and offers
// no column editor (TaskBoard.jsx's fixedColumns), which leaves "Edit
// board" as the only way to change them; a prompt that did not say what
// they are asked an agent to add a column to a layout it could not see.
function columnLines(board) {
  const columns = (board?.columns || []).filter(
    (c) => c && typeof c === "object",
  );
  if (columns.length === 0) {
    return "(none of its own, so it draws whatever columns the reader's browser is set to)";
  }
  return columns
    .map((c, i) => {
      const states = Array.isArray(c.states) ? c.states : [];
      return `${i + 1}. "${c.name || ""}": ${states.length > 0 ? states.map((st) => `\`${st}\``).join(", ") : "(no states)"}`;
    })
    .join("\n");
}

// newBoardTask is the "New board" button's task: a title and a body,
// pre-written, for the new-task form to open on. narrowing is what the
// reader is looking at right now, so clicking New board on a list they
// have just narrowed offers to save that narrowing -- which is the
// gesture this whole feature is for.
// windowLines is what a report's Window picker is showing, said for a
// board that cannot keep it. The picker is the address bar's `window=`,
// not the board's query, so a report somebody has set to the last 90
// days and then clicked "Edit board" (or "New board") on filed a task
// that never mentioned the 90 days -- the one setting on screen these
// prompts left out (2026-10-13 boards walkthrough). A board keeps a
// window as a date condition in its query, which REPORT_NOTE explains;
// this names the one to write. Empty for every other kind, and for a
// report showing the window its query already opens it on.
function windowLines(kind, window) {
  if (kind !== "report" || !window) return "";
  const picked = REPORT_WINDOWS.find((w) => w.value === window);
  const days = window === "24h" ? 1 : Number(/^(\d+)d$/.exec(window)?.[1]);
  if (!picked || !days) return "";
  return `
The report is being read right now over the ${picked.label.toLowerCase()}
(its Window picker), which a board does not keep on its own. To save it,
the board's query carries \`f=created:within:${days}\` in place of any
other \`created\` condition.
`;
}

export function newBoardTask({ narrowing, kind = "list", window } = {}) {
  const query = narrowing ? queryFromNarrowing(narrowing) : "";
  return {
    title: "Add a board",
    description: `Add a saved board to grain.

The board:

- name: <what it should be called in the nav rail>
- folder: <the folder to file it under, or leave empty for none>
- kind: ${kindLine(kind)}
- description: <one line saying what the board is for>
- query: ${query ? `\`${query}\`` : "<see below>"}

${query ? "That query is the view the board was created from, and is what the person asking for this board was looking at." : ""}
${windowLines(kind, window)}${kindsNote(kind)}

${QUERY_NOTE}
${kind === "panels" ? `\n${PANELS_NOTE}\n` : ""}${kind === "report" ? `\n${REPORT_NOTE}\n` : ""}
${STATE_REPO_NOTE}`,
  };
}

// editBoardTask is the "Edit board" button's task: the same thing for a
// board that already exists, opening on what that board says today and
// on the narrowing the reader has since made of it. The two together
// are the edit: "here is the row, and here is what I want it to say".
export function editBoardTask(board, { narrowing, window } = {}) {
  const current = board?.query || "";
  const shown = narrowing ? queryFromNarrowing(narrowing) : "";
  const changed = shown !== "" && shown !== current;
  return {
    title: `Change the board "${board?.name || board?.id || ""}"`,
    description: `Change grain's saved board \`${board?.id || ""}\` ("${board?.name || ""}").

What it says today:

- name: ${board?.name || ""}
- folder: ${board?.folder || "(none)"}
- kind: ${kindLine(board?.kind)}
- description: ${board?.description || "(none)"}
- query: ${current ? `\`${current}\`` : "(empty -- nobody has narrowed it; see below)"}
${
  board?.kind === "panels"
    ? `
Its panels, in the order they are drawn:

${panelLines(board)}
`
    : ""
}${
      board?.kind === "kanban"
        ? `
Its columns, left to right:

${columnLines(board)}
`
        : ""
    }${
      board?.kind === "report"
        ? `
Its sections, in the order they are printed:

${sectionLines(board)}
`
        : ""
    }
What to change: <say what should be different>
${
  changed
    ? `
The board is being read right now narrowed to \`${shown}\`, which is
probably the query it should be saved with.
`
    : ""
}${windowLines(board?.kind, window)}
${QUERY_NOTE}
${board?.kind === "panels" ? `\n${PANELS_NOTE}\n` : ""}${board?.kind === "report" ? `\n${REPORT_NOTE}\n` : ""}
${STATE_REPO_NOTE}`,
  };
}

// keptQuery is the narrowing a reader has *made* of a dashboard: the
// query the panel grid is answering this moment, or "" when that is
// still the dashboard's own question.
//
// The distinction is the whole of what the two dashboard prompts carry.
// A dashboard opens on its board's query (App's openBoard seeds the
// narrowing from the row), so a toolbar still showing exactly that is
// nobody's narrowing: it is the board saying what it has always said,
// and a panel asked for on that basis is a panel that inherits -- an
// empty query rather than the board's own frozen onto a row that would
// then stop following the toolbar for good. Anything else is a question
// this reader composed, and is the one worth keeping.
//
// The board's own question is what the dashboard *opens* on, which for
// a board naming no query is not "nothing": boardNarrowing answers null
// for one of those and App resolves that to the deployment's own default
// (grain/task-309), so the toolbar of an untouched dashboard is already
// showing a state condition nobody typed. Comparing against the bare
// board row instead would read that condition as this reader's own
// narrowing -- "Keep" on every panel of a dashboard nobody has narrowed,
// and "Add panel" pre-filled with a condition the deployment is entitled
// to change. Which is also why Clear keeps nothing: it puts the toolbar
// back to exactly this, and back to the board's question is back to
// having composed no question.
//
// Both sides go through queryFromNarrowing before being compared, so a
// stored query and the narrowing parsed out of it match even when they
// were spelled differently.
export function keptQuery(board, narrowing, config) {
  const asked = narrowing ? queryFromNarrowing(narrowing) : "";
  const own = queryFromNarrowing(
    boardNarrowing(board) || defaultNarrowing(config),
  );
  return asked === own ? "" : asked;
}

// addPanelTask is the "Add panel" button on a dashboard: the same road
// every other change to a board takes (see the top of this file), asked
// about one panel instead of the whole board.
//
// It carries the narrowing the reader has made on the dashboard this
// moment, for the reason "Edit board" does: the gesture worth having is
// "put what I am looking at on the dashboard", and a narrowing typed
// into the toolbar and then described again in prose is a chance to get
// it wrong. It also carries the panels already there, so the agent
// writing the row knows what it is being added to and where in the
// order it goes.
//
// The narrowing it carries is only the reader's own: a dashboard opens
// on its board's question, so a toolbar still showing exactly that is
// somebody asking for a panel of the dashboard rather than for a panel
// of something narrower, and the row for that has an empty query
// (panels.js's panelNarrowing). Writing the board's query onto the
// panel instead would freeze it -- the panel would go on asking that
// one question while every other panel followed the toolbar.
// "Edit board" makes the same comparison for the same reason.
export function addPanelTask(board, { narrowing, config } = {}) {
  const shown = keptQuery(board, narrowing, config);
  return {
    title: `Add a panel to the dashboard "${board?.name || board?.id || ""}"`,
    description: `Add a panel to grain's saved board \`${board?.id || ""}\` ("${board?.name || ""}"), which is a dashboard of panels.

Its panels today, in the order they are drawn:

${panelLines(board)}

The panel to add:

- kind: <one of the kinds below>
- title: <the heading, or leave empty for the name of the kind>
- query: ${shown ? `\`${shown}\`` : "<see below; empty means the dashboard's own question>"}
- range: <for a timeline or a throughput panel; empty for the default>
- width: <third, half or full; empty for half>
- order_key: <where it goes among the panels above>
${
  shown
    ? `
That query is what the dashboard is narrowed to on screen right now, and
is probably the question this panel should ask. ${SHOWN_QUERY_NOTE}
`
    : ""
}
${QUERY_NOTE}

${PANELS_NOTE}

${STATE_REPO_NOTE}`,
  };
}

// keepPanelQueryTask is the "Keep" control on a panel a reader has
// narrowed from the toolbar (PanelBoard.jsx): the task that makes that
// narrowing the panel's own question, so it is still being asked
// tomorrow and on somebody else's screen.
//
// # Why this is the gesture that was missing
//
// Both halves of it already existed. The toolbar narrows a dashboard
// live, and "Add panel" carries whatever the reader has narrowed it to
// into the row it asks for. What neither did is the step between them:
// getting a panel's *own* query right is iterative -- learning that one
// word finds ninety tasks and another finds fifty is something a reader
// finds out by trying, in the toolbar, in seconds -- and until now the
// only way to keep the answer was to describe it again in prose in a
// task about a panel that already exists. This is the same road every
// other change to a board takes, aimed at the one panel the reader is
// looking at and filled in with the query they arrived at.
//
// # Which panel, and what it will do afterwards
//
// The prompt points at one row (panelLines' `mark`, and the order key
// beside it, since a panel row has no id anybody named) and asks for one
// field on it. It also says what the change does to the panel beyond
// storing a string, because that is the part a reader cannot see from
// the gesture: a panel with a query of its own stops following the
// toolbar (panels.js's panelNarrowing says why two narrowings are never
// combined), so keeping this narrowing costs the panel the ability to be
// narrowed to the next one. That is what "keep" means here, and the
// person filing it should be told rather than surprised.
export function keepPanelQueryTask(board, panel, { narrowing, config } = {}) {
  const kept = keptQuery(board, narrowing, config);
  const panels = board?.panels || [];
  // The panel object as PanelBoard drew it, which is the same object
  // this board's own list holds -- by key as well, so a caller that
  // rebuilt the board between the render and the click still points at
  // the right row.
  const index = panels.findIndex(
    (p) => p === panel || (panel?.key && p.key === panel.key),
  );
  const position = index >= 0 ? `${index + 1}` : "";
  return {
    title: `Narrow the panel "${panelTitle(panel)}" on the dashboard "${board?.name || board?.id || ""}"`,
    description: `Give one panel of grain's saved board \`${board?.id || ""}\` ("${board?.name || ""}") a
question of its own, instead of the dashboard's.

Its panels today, in the order they are drawn:

${panelLines(board, index)}

What to change: set the \`query\` of ${position ? `panel ${position}` : `the panel "${panelTitle(panel)}"`} above to ${
      kept
        ? `\`${kept}\``
        : "<the query it should ask -- the dashboard was not narrowed to anything when this was filed>"
    }.
Leave every other panel, and the board's own row, exactly as they are.
${
  kept
    ? `
That is the query the dashboard is narrowed to on screen right now. The
panel is showing those tasks because it carries no query of its own and
follows the dashboard's toolbar; writing the query onto its own row is
what keeps it there once the toolbar is cleared, and is the whole of this
request. ${SHOWN_QUERY_NOTE}
`
    : ""
}
One consequence worth knowing before this is merged: a panel that carries
its own query is no longer narrowed by the dashboard's toolbar at all --
the two are never combined, because two conditions on one attribute
intersect and would silently empty the panel. So this panel will go on
asking this one question while the panels around it follow whatever the
dashboard is narrowed to.

${QUERY_NOTE}

${PANELS_NOTE}

${STATE_REPO_NOTE}`,
  };
}
