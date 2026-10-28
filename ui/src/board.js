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

// board.js is the Kanban board's own layout: which columns there are,
// which of model.State's states each one collects, and how a set of
// tasks falls into them (grain/task-287).
//
// # Why a board's columns are a *setting* at all
//
// A board is the flat list's answer to a different question. The list
// answers "what is the backlog, in order"; the board answers "where is
// everything right now", and that only reads as an answer if the
// columns match how this deployment actually works. grain has ten
// states and no two operators want the same ten columns: one wants
// proposed and queued side by side because approving is the daily job,
// another wants a single "waiting on me" column holding awaiting_reply,
// failed, awaiting_submit and awaiting_ack, because those are the four
// that never move on their own. Neither is a default worth hard-coding over the
// other, so the columns are edited (BoardColumnsOverlay.jsx) and kept.
//
// # Where they are kept, and why not in the daemon
//
// localStorage, under BOARD_STORAGE_KEY -- the same treatment the theme
// mode gets (ThemeModeContext.jsx), and for the same reason: this is
// how one person wants to look at the deployment, not a fact about the
// deployment. Nothing behind /api/config changes when a column is
// renamed, no run behaves differently, and an operator with a phone and
// a laptop is allowed to want different columns on each. It also means
// the whole feature is a UI change with no schema, no settings key and
// no migration behind it.
//
// The cost is that a browser's stored layout can be older than the
// build reading it, which is what normalizeColumns below is for: a
// stored column naming a state this build has never heard of, or a
// stored value that isn't a board at all, falls back rather than
// throwing at render time.
//
// # What a column can and cannot do
//
// A column is a set of states, so "which tasks show up" is answered by
// the columns themselves: a state no column names is off the board
// entirely, which is how "closed" is hidden by default without a
// toggle of its own. What a column cannot do is move a task: a task's
// state is derived by the daemon from what has actually happened to it
// (model.StateOf), not stored as a field the UI could set, so there is
// no drop that could put a queued task into "Running". Dragging a card
// reorders the backlog within its own column and nothing else -- see
// TaskBoard.jsx.
import { STATE_ORDER, stateLabels } from "./state.js";

export const BOARD_STORAGE_KEY = "grain.board.columns";

// DEFAULT_COLUMNS is the board somebody who has never opened the editor
// sees. It is grain's own life-cycle read left to right -- what an agent
// suggested, what is waiting to run, what is running, what is stuck on a
// human, what is waiting to land. The two states shown as "Asked"
// (state.js's ASKED_STATES) are one column, because to a person they are
// one state: the task has stopped and it is their move. "Failed" is the
// other state that never moves on its own, and has the column beside it.
//
// "Closed" and "Deferred" get no column. It is the same judgement every
// task view makes with the condition it opens narrowed by (state.js's
// SET_ASIDE_STATES): neither a finished task nor
// one somebody deliberately put aside is part of "where is everything
// right now", and a board whose widest column is the archive says
// nothing. Adding a column for either is two clicks in the editor for
// whoever disagrees.
export const DEFAULT_COLUMNS = [
  { id: "proposed", title: "Proposed", states: ["proposed"] },
  { id: "queued", title: "Waiting", states: ["queued"] },
  { id: "running", title: "Running", states: ["running"] },
  {
    id: "asked",
    title: "Asked",
    states: ["awaiting_reply", "awaiting_submit"],
  },
  { id: "failed", title: "Failed", states: ["failed"] },
  { id: "landing", title: "Merging", states: ["completed"] },
];

// defaultColumns hands back a fresh deep copy every time: the editor
// mutates the array it is given (adding, renaming, reordering), and a
// "Reset to default" that handed out the module's own object would let
// the next reset start from the last edit.
export function defaultColumns() {
  return DEFAULT_COLUMNS.map((c) => ({ ...c, states: [...c.states] }));
}

let nextColumnSeq = 0;

// newColumn is the editor's "Add column": one empty column with an id
// nothing else can collide with. The id is never shown and never
// stored anywhere but this browser's own layout, so a counter is
// enough -- it only has to be unique among the columns on screen.
export function newColumn() {
  nextColumnSeq += 1;
  return {
    id: `col-${Date.now().toString(36)}-${nextColumnSeq}`,
    title: "New column",
    states: [],
  };
}

// normalizeColumns turns whatever was stored -- or whatever the editor
// currently holds -- into a board that renders, or null when it cannot
// be read as one at all (so the caller falls back to the default).
//
// The rules, in order:
//
//   - it has to be a non-empty array of objects; anything else is not a
//     board and there is nothing to salvage
//   - a state this build doesn't know is dropped, and a state named by
//     two columns stays only in the first. Otherwise a layout stored by
//     a newer build would render a column of tasks that can't exist, or
//     one task would appear twice on the same board
//   - a column with no title left gets its states' own labels, or
//     "Untitled", rather than rendering as a nameless column
//   - ids are made unique, since they key the rendered columns
//
// A column left holding no states survives on purpose: it is a real
// thing to have on a board you are still building, it renders as an
// empty column saying so, and dropping it would silently undo an edit
// somebody made.
export function normalizeColumns(raw) {
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const known = new Set(STATE_ORDER);
  const taken = new Set();
  const ids = new Set();
  const columns = [];
  for (const c of raw) {
    if (!c || typeof c !== "object") continue;
    const states = Array.isArray(c.states)
      ? c.states.filter((s) => known.has(s) && !taken.has(s))
      : [];
    for (const s of states) taken.add(s);
    let id =
      typeof c.id === "string" && c.id !== "" && !ids.has(c.id)
        ? c.id
        : newColumn().id;
    ids.add(id);
    let title =
      typeof c.title === "string" && c.title.trim() !== ""
        ? c.title.trim()
        : stateLabels(states).join(" · ") || "Untitled";
    if (
      id === "landing" &&
      states.length === 1 &&
      states[0] === "completed" &&
      title === "Queued for merge"
    ) {
      title = "Merging";
    }
    // The default board's own queued column, kept from before the state
    // was relabelled "Waiting": a title nobody chose follows the label.
    if (
      id === "queued" &&
      states.length === 1 &&
      states[0] === "queued" &&
      title === "Queued"
    ) {
      title = "Waiting";
    }
    columns.push({ id, title, states });
  }
  return columns.length > 0 ? columns : null;
}

// loadColumns is the layout this browser last saved, or the default.
// Anything unreadable -- a half-written value, a shape from a build that
// spelled columns differently, localStorage denied outright by the
// browser's own settings -- is treated as "nothing stored yet" rather
// than as an error to show somebody: the board still opens, on the
// default columns, and the next save overwrites the bad value.
export function loadColumns() {
  let stored = null;
  try {
    stored = localStorage.getItem(BOARD_STORAGE_KEY);
  } catch {
    return defaultColumns();
  }
  if (!stored) return defaultColumns();
  try {
    return normalizeColumns(JSON.parse(stored)) || defaultColumns();
  } catch {
    return defaultColumns();
  }
}

// saveColumns writes the layout back, or clears it when it is the
// default -- so a board left alone keeps following whatever this file
// calls the default rather than being pinned to the copy of it that
// happened to be current the day somebody first opened the editor.
export function saveColumns(columns) {
  try {
    if (isDefault(columns)) localStorage.removeItem(BOARD_STORAGE_KEY);
    else localStorage.setItem(BOARD_STORAGE_KEY, JSON.stringify(columns));
  } catch {
    // A browser that won't store this still shows the board the edit
    // asked for; it just forgets it on reload. Not worth an error
    // banner over a preference.
  }
}

function isDefault(columns) {
  const a = columns.map((c) => ({ title: c.title, states: c.states }));
  const b = DEFAULT_COLUMNS.map((c) => ({ title: c.title, states: c.states }));
  return JSON.stringify(a) === JSON.stringify(b);
}

// columnOf is the column a task belongs to, or null when no column
// names its state -- which is how a board deliberately leaves states off
// screen.
export function columnOf(columns, task) {
  return columns.find((c) => c.states.includes(task.state)) || null;
}

// boardStates is every state the current columns cover: what the board
// is about, in the sense the list's own state filter is.
export function boardStates(columns) {
  return columns.flatMap((c) => c.states);
}

// hiddenStates is the other half of that -- the states no column names,
// in STATE_ORDER, so the board can say out loud which tasks it is not
// showing rather than leaving somebody to count.
export function hiddenStates(columns) {
  const shown = new Set(boardStates(columns));
  return STATE_ORDER.filter((s) => !shown.has(s));
}

// groupIntoColumns drops the given tasks -- already sorted, already
// filtered -- into their columns, keeping the order they arrived in
// within each. Tasks whose state no column names are left out, and
// reported as `hidden` so the board can say how many it is not showing.
export function groupIntoColumns(tasks, columns, matches = () => true) {
  const byState = new Map();
  for (const c of columns) for (const s of c.states) byState.set(s, c.id);
  const cards = new Map(columns.map((c) => [c.id, []]));
  let hidden = 0;
  for (const t of tasks) {
    if (!matches(t)) continue;
    const id = byState.get(t.state);
    if (id === undefined) {
      hidden += 1;
      continue;
    }
    cards.get(id).push(t);
  }
  return { cards, hidden };
}
