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

// taskFilters.js is the question "which tasks show up, in what order" --
// the comparisons a filter can make, the orders a list can be put in,
// and the code that turns a set of tasks into the menus offering them --
// kept apart from any one view that asks it.
//
// It lived inside TaskList.jsx until the board (TaskBoard.jsx) needed
// the same toolbar over the same tasks (grain/task-287). Two views
// asking that question two different ways would mean a repo filter that
// spells "no repo" differently on each of them, and a new attribute
// getting a menu on one view and not on the other; both views build
// their toolbar out of this file instead (NarrowingBar.jsx).
//
// What is deliberately *not* here: the sidebar's own state filter. The
// list takes that from the rail as a standing question about which tasks
// it is about, and the board answers the same question with its columns
// (board.js) -- so it is each view's own, not shared. (State is also an
// attribute here, which is not a contradiction: the rail picks what a
// list is *about*, and a filter narrows within it. Both are answerable,
// and a filter naming a state the rail excludes simply matches nothing,
// the same as any other filter over tasks that are not there -- which is
// why picking a state in the rail re-answers the state condition rather
// than intersecting with it, App.jsx's selectStateFilter.)
//
// The two states a list leaves out until it is asked for them are one of
// these conditions now rather than a mechanism of their own: see
// defaultFilters at the foot of this file.
//
// # The shape a narrowing takes
//
//   { search, sort, filters }
//
//   search   the search box, over a task's title and id
//   sort     an ordered list of {attr, dir} keys, the first one the
//            primary order and the rest tie-breakers; empty is the
//            backlog's own order
//   filters  a list of {attr, op, values} conditions, all of which a
//            task has to satisfy
//
// Filters were one value per attribute until grain/task-42 -- an object
// keyed by attribute id, one Select each, "is" the only comparison
// anything could make. A list is now narrowed by conditions instead:
// several on one attribute if that is the question ("created after
// Monday and before Friday"), several values in one condition ("either
// of these two repos"), and a comparison chosen per condition rather
// than implied. The attributes themselves, custom ones included, are
// taskAttributes.js.
import {
  NONE,
  NONE_LABEL_FALLBACK,
  TYPE_DATE,
  TYPE_ENUM,
  TYPE_NUMBER,
  TYPE_TAGS,
  TYPE_TEXT,
  attributeById,
  attributesFor,
  valueLabel,
} from "./taskAttributes.js";
// The zone reading behind "which day is this?" lives with the rest of
// the zone handling (time.js) rather than here: the timeline asks the
// same question of the same clock (gantt.js), and two copies of it would
// be two midnights.
import { zoneOffset } from "./time.js";
// The two states a list is not about until it is asked -- the whole of
// what "hidden by default" is now, since it is a condition rather than a
// mechanism (defaultFilters below).
import { SET_ASIDE_STATES } from "./state.js";

// NONE -- "this task has no value for the attribute at all" -- is
// declared with the attributes rather than here, since a descriptor has
// to recognise it, and re-exported so everything that compares values
// has one place to reach for it.
export { NONE };

// OPERATORS is every comparison a condition can make.
//
//   label   how it reads in the editor, between the attribute and the
//           value: "Repo | is any of | acme/widgets".
//   input   what the editor puts on the right of it, and what
//           `values` holds: a set of choices, a line of text, a number,
//           a date, or nothing at all.
//
// The ids are short because they are written into the query string
// (paths.js) and read back by a person deciding whether a link says what
// they meant.
export const OPERATORS = {
  any: { label: "is any of", input: "choices" },
  none: { label: "is none of", input: "choices" },
  all: { label: "has all of", input: "choices" },
  has: { label: "contains", input: "text" },
  hasnot: { label: "does not contain", input: "text" },
  eq: { label: "is", input: "number" },
  ne: { label: "is not", input: "number" },
  gt: { label: "is more than", input: "number" },
  lt: { label: "is less than", input: "number" },
  after: { label: "is after", input: "date" },
  before: { label: "is before", input: "date" },
  within: { label: "is within the last", input: "days" },
  // The one comparison that looks forward rather than back -- "what
  // fires in the next six hours" -- offered only by an attribute that is
  // about the future (`future`, a schedule's next run: processFilters.js).
  // A moment already gone by is inside the window: a next run that is
  // overdue happens as soon as it can, which is sooner than anything
  // else in it. pkg/schedulefilter reads it the same way.
  coming: { label: "is within the next", input: "hours" },
  empty: { label: "is empty", input: "none" },
  filled: { label: "is set", input: "none" },
  legacy: { label: "is a legacy value", input: "none" },
};

// OPERATORS_BY_TYPE is which of them each kind of attribute offers, in
// menu order -- the most-used comparison first, since that is also the
// one a new condition starts on.
//
// Text takes no "is any of": its values are titles and ids, which nobody
// picks off a menu of every one in the list, so the editor gives it a
// box to type in and the comparison to match is "contains".
const OPERATORS_BY_TYPE = {
  [TYPE_ENUM]: ["any", "none"],
  [TYPE_TAGS]: ["any", "none", "all"],
  [TYPE_TEXT]: ["has", "hasnot"],
  [TYPE_DATE]: ["within", "after", "before"],
  [TYPE_NUMBER]: ["eq", "ne", "gt", "lt"],
};

// canBeEmpty is whether "has no value at all" is a real answer for this
// attribute, and so whether it is worth offering "is empty"/"is set".
// An attribute that says what it calls having none (noneLabel) is
// saying exactly that; a date or a number is absent often enough to be
// worth asking about either way. Everything else -- a title, a kind, an
// auto-merge flag -- is always there, and the two comparisons would be
// one that never matches and one that always does.
function canBeEmpty(attr) {
  return (
    !!attr.noneLabel || attr.type === TYPE_DATE || attr.type === TYPE_NUMBER
  );
}

// operatorsFor is the comparisons one attribute offers: its type's, the
// two about having no value at all where that is a real answer, and --
// for a field this deployment declares -- "is a legacy value", which is
// how the tasks a changed declaration stranded are found.
//
// That last one is offered by the attribute rather than by its type: an
// enum option retired from tables/custom_field.json leaves the tasks
// carrying it exactly as they are (model.NormalizeCustomFieldValuesFrom
// on why nothing rewrites them), and no built-in attribute has a
// vocabulary a deployment can change under it, so only a custom
// attribute can answer the question at all (taskAttributes.js's
// customAttribute is what carries `legacy`).
export function operatorsFor(attr) {
  let ops = OPERATORS_BY_TYPE[attr.type] || OPERATORS_BY_TYPE[TYPE_TEXT];
  // A name is picked off a menu when the person is on it, and typed when
  // all that is remembered is part of it ("anything filed by someone
  // called sam").
  if (attr.people) ops = [...ops, "has", "hasnot"];
  if (attr.future) ops = ["coming", ...ops];
  const withEmpty = canBeEmpty(attr) ? [...ops, "empty", "filled"] : ops;
  return attr.legacy ? [...withEmpty, "legacy"] : withEmpty;
}

// newCondition is the condition an attribute starts as when it is added
// to the editor: its first comparison and no value yet. It narrows
// nothing until the value is filled in (see matchesCondition), so
// picking an attribute never empties the list before the question has
// been asked.
export function newCondition(attr) {
  return { attr: attr.id, op: operatorsFor(attr)[0], values: [] };
}

// conditionFor keeps a condition answerable when its attribute changes
// under it: the comparison survives if the new attribute offers it, and
// is replaced by that attribute's first one if not. The values never
// survive -- a repo is not a capability -- which is also what stops a
// half-changed condition from matching on a value the new attribute
// could never hold.
export function conditionFor(attr, prev) {
  const ops = operatorsFor(attr);
  const op = prev && ops.includes(prev.op) ? prev.op : ops[0];
  return { attr: attr.id, op, values: [] };
}

// needsValue is whether a condition is still waiting to be told what to
// compare against. The two no-input comparisons never are; everything
// else is, until `values` has something in it.
export function needsValue(cond) {
  return OPERATORS[cond.op]?.input !== "none" && cond.values.length === 0;
}

// optionsFor is every value of one attribute the given tasks actually
// carry -- so a menu never offers a choice that would come back empty.
// A fixed-vocabulary attribute keeps its own order; the rest are
// alphabetical by what the menu shows rather than by the raw value,
// since a capability's id and its name are not the same string.
//
// `picked` is what the condition already names, which is folded in even
// when no task in view carries it: a value chosen before the sidebar
// moved, or one arriving from a link, stays visible in its own menu
// rather than silently dropping out of a condition that goes on
// filtering by it.
export function optionsFor(attr, tasks, config, picked = []) {
  const seen = new Set();
  let none = false;
  for (const t of tasks) {
    const values = attr.values(t);
    if (values.length === 0) none = true;
    for (const v of values) seen.add(attr.optionOf ? attr.optionOf(v) : v);
  }
  for (const v of picked) {
    if (v === NONE) none = true;
    else seen.add(v);
  }
  const option = (v) => ({
    value: v,
    label: valueLabel(attr, v, config),
    glyph: attr.glyphOf ? attr.glyphOf(v) : null,
  });
  const order = attr.menuOrder || attr.order;
  const options = order
    ? [
        ...order.filter((v) => seen.has(v)),
        // A value no fixed vocabulary knows about -- a state from a
        // newer daemon -- is still offered, after the ones it does, so
        // it is filterable rather than invisible.
        ...[...seen].filter((v) => !order.includes(v)).sort(),
      ].map(option)
    : [...seen]
        .map(option)
        .sort((a, b) => String(a.label).localeCompare(String(b.label)));
  if (none && (attr.noneLabel || attr.type !== TYPE_ENUM))
    options.push({
      ...option(NONE),
      label: attr.noneLabel || NONE_LABEL_FALLBACK,
    });
  return options;
}

// --- matching --------------------------------------------------------

const lower = (v) => String(v).toLowerCase();

// matchesCondition is whether one task satisfies one condition.
//
// A condition still waiting for its value matches everything: the
// editor adds a row before there is anything to compare against, and a
// list that emptied itself the moment somebody picked an attribute
// would be answering a question nobody has asked yet.
function matchesCondition(attr, cond, t, config) {
  if (needsValue(cond)) return true;
  const values = attr.values(t);
  // A value that stands for several (the state attribute's "asked") is
  // matched as all of them.
  const wanted = attr.expand ? cond.values.flatMap(attr.expand) : cond.values;
  switch (cond.op) {
    case "any":
      return values.length === 0
        ? wanted.includes(NONE)
        : values.some((v) => wanted.includes(v));
    case "none":
      return !(values.length === 0
        ? wanted.includes(NONE)
        : values.some((v) => wanted.includes(v)));
    case "all":
      return wanted.every((w) =>
        w === NONE ? values.length === 0 : values.includes(w),
      );
    case "has":
      return values.some((v) =>
        lower(valueLabel(attr, v, config)).includes(lower(wanted[0])),
      );
    case "hasnot":
      return !values.some((v) =>
        lower(valueLabel(attr, v, config)).includes(lower(wanted[0])),
      );
    case "empty":
      return values.length === 0;
    case "filled":
      return values.length > 0;
    case "legacy":
      // Only a declared field carries this, and only it offers the
      // comparison -- so a condition naming it on anything else can
      // only have come out of a link, and narrows nothing rather than
      // emptying the list, exactly as an unknown comparison does below.
      return attr.legacy ? attr.legacy(t) : true;
    case "eq":
    case "ne":
    case "gt":
    case "lt": {
      const want = Number(wanted[0]);
      if (!Number.isFinite(want)) return true;
      const nums = values.map(Number).filter((n) => Number.isFinite(n));
      if (nums.length === 0) return false;
      if (cond.op === "ne") return nums.every((n) => n !== want);
      return nums.some((n) =>
        cond.op === "eq" ? n === want : cond.op === "gt" ? n > want : n < want,
      );
    }
    case "after":
    case "before": {
      // A date the editor writes is a plain day (2026-03-04), read as
      // that day in the zone this deployment keeps its clock in
      // (time.js, grain/task-368) rather than as UTC or as the
      // browser's: every timestamp on the rows being filtered is
      // printed in that zone, so a day named here has to start when the
      // rows say it does. "After" is inclusive of the whole day named
      // and "before" exclusive of it, so the two together read as a
      // range with no gap and no overlap.
      const day = dayStart(wanted[0], config?.timeZone);
      if (day === null) return true;
      const times = momentsOf(values, config);
      if (times.length === 0) return false;
      return times.some((n) => (cond.op === "after" ? n >= day : n < day));
    }
    case "within": {
      const days = Number(wanted[0]);
      if (!Number.isFinite(days)) return true;
      const cutoff = Date.now() - days * 24 * 60 * 60 * 1000;
      return momentsOf(values, config).some((n) => n >= cutoff);
    }
    case "coming": {
      const hours = Number(wanted[0]);
      if (!Number.isFinite(hours)) return true;
      const cutoff = Date.now() + hours * 60 * 60 * 1000;
      return momentsOf(values, config).some((n) => n <= cutoff);
    }
    default:
      // A comparison this build has never heard of -- a link from a
      // newer one -- narrows nothing rather than emptying the list.
      return true;
  }
}

// momentOf is when one value of a date attribute happened, in
// milliseconds, or undefined for a value that is not a moment at all.
//
// A timestamp (a task's createdAt) is read as itself. A bare calendar
// day -- 2026-03-04, which is what a custom date field holds
// (model.CustomFieldDateLayout) -- is read as that day *in the
// deployment's own zone*, the same reading dayStart gives the day named
// in the editor. The two have to agree: read as UTC instead, a field
// whose value is the 4th would answer no to "is after the 4th" on every
// deployment west of UTC, since UTC midnight falls on the 3rd there.
function momentOf(value, zone) {
  const day = dayStart(value, zone);
  if (day !== null) return day;
  const n = Date.parse(value);
  return Number.isFinite(n) ? n : undefined;
}

// momentsOf is that over one task's values, dropping the ones that are
// not moments -- what every date comparison compares against.
function momentsOf(values, config) {
  return values
    .map((v) => momentOf(v, config?.timeZone))
    .filter((n) => n !== undefined);
}

// dayStart is the moment the editor's own date value (yyyy-mm-dd)
// begins, in `zone`, or null for anything that is not a date -- a
// half-typed one, or a value from a link that was never a date at all.
// An empty zone is "whatever this browser is in", the same reading
// time.js gives it.
//
// Cached because it is asked once per task and answers once per
// condition: the Intl work below is not much, but it is not nothing
// across a few hundred rows re-filtered on every keystroke.
const dayStarts = new Map();

export function dayStart(text, zone) {
  const key = `${text}|${zone || ""}`;
  if (dayStarts.has(key)) return dayStarts.get(key);
  const at = computeDayStart(text, zone);
  dayStarts.set(key, at);
  return at;
}

function computeDayStart(text, zone) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(text || ""));
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (!zone) {
    const local = new Date(y, mo - 1, d);
    return Number.isNaN(local.getTime()) ? null : local.getTime();
  }
  // Midnight UTC on that date, moved back by however far the zone is
  // from UTC at that moment. Twice, because the offset is read *at* an
  // instant and the first guess can land on the wrong side of a
  // daylight-saving change -- the second reading is taken at the answer
  // the first one gave, which is within an hour of the truth.
  const utc = Date.UTC(y, mo - 1, d);
  if (Number.isNaN(utc)) return null;
  const first = utc - zoneOffset(utc, zone);
  return utc - zoneOffset(first, zone);
}

// resolveConditions pairs each stored condition with the attribute it
// names, dropping the ones whose attribute this build does not have --
// a custom attribute the deployment has since removed, or a link from a
// newer build. Dropped rather than treated as an error, and dropped
// rather than left to match nothing: a stale link should open the list
// it names, not an empty one.
// `index` is where the condition sits in the stored list, which is what
// the editor needs in order to change or remove the row it drew from it
// -- the two lists are not the same length whenever one has been
// dropped.
export function resolveConditions(filters, attributes) {
  const out = [];
  (filters || []).forEach((cond, index) => {
    const attr = attributeById(attributes, cond.attr);
    if (attr && knownOperator(cond.op)) out.push({ attr, cond, index });
  });
  return out;
}

// knownOperator is whether `op` is a comparison this build knows at
// all. One it does not -- "state is bogus", a link from a newer build or
// a hand-edited board -- is the same kind of stale link as an attribute
// nobody declares, and is dropped and named the same way rather than
// parsed away in silence. It is the vocabulary that is checked, not the
// attribute's own menu: matchesCondition answers every known comparison
// on every attribute ("is any of Not set" on a text field is real).
function knownOperator(op) {
  return Boolean(OPERATORS[op]);
}

// unknownConditions is the other half of resolveConditions: the stored
// conditions it dropped, each with its index in the stored list so a
// view can offer to remove it. Dropping one is right -- a stale link
// should open the list it names -- but dropping it *silently* is not:
// a board whose saved query misspells an attribute shows every task in
// reach and looks exactly like a board whose question matched them all
// (grain/task-1421). So a view that drops a condition says which one,
// the way a report says what of its question is not in force.
//
// A condition whose attribute is known but whose comparison is not one
// this build has comes back with `badOp` set, so the line names the pair
// ("state:bogus") rather than an attribute that plainly exists.
export function unknownConditions(filters, attributes) {
  const out = [];
  (filters || []).forEach((cond, index) => {
    const attr = attributeById(attributes, cond?.attr);
    if (!attr) out.push({ cond, index });
    else if (!knownOperator(cond.op)) out.push({ cond, index, badOp: true });
  });
  return out;
}

// unknownConditionName is how one of those conditions is named: its
// attribute, or its attribute and comparison when the comparison is
// what could not be read.
export function unknownConditionName({ cond, badOp }) {
  return badOp ? `${cond?.attr}:${cond?.op}` : cond?.attr;
}

// describeUnknownConditions is those conditions as the one line a view
// prints about them: "Unknown condition: nosuchattr", or "Unknown
// conditions: a, b" for several. "" when there are none.
export function describeUnknownConditions(unknown) {
  const names = [...new Set((unknown || []).map(unknownConditionName))];
  if (names.length === 0) return "";
  return `Unknown condition${names.length === 1 ? "" : "s"}: ${names.join(", ")}`;
}

// matchesFilters is whether one task survives every condition currently
// set plus the search box -- the second half of every view's own
// `matches`, whose first half is that view's own question about state.
// The search is over the title and the id, which is what somebody
// typing "287" or "kanban" is reaching for.
//
// `searchIn` is what a list of something other than tasks searches
// instead: the CI pane narrows runs with these same conditions
// (ciFilters.js), and a run has no title. It is only the search box --
// the conditions themselves are already about whatever the attribute
// descriptors say they are about, and never touched a task's own fields.
export function matchesFilters(t, conditions, q, config, searchIn = null) {
  for (const { attr, cond } of conditions) {
    if (!matchesCondition(attr, cond, t, config)) return false;
  }
  if (q === "") return true;
  if (searchIn) return String(searchIn(t)).toLowerCase().includes(q);
  return (
    t.title.toLowerCase().includes(q) || String(t.id).toLowerCase().includes(q)
  );
}

// describeCondition is one condition as a sentence, for the chip a
// closed editor leaves behind: "Repo is any of acme/widgets, acme/
// gadgets". A condition with no value yet says what it is waiting for
// rather than reading as a filter that is doing something.
export function describeCondition(attr, cond, config) {
  const op = OPERATORS[cond.op];
  const label = `${attr.label} ${op ? op.label : cond.op}`;
  if (!op || op.input === "none") return label;
  if (cond.values.length === 0) return `${label}…`;
  if (op.input === "choices")
    return `${label} ${cond.values
      .map((v) =>
        v === NONE
          ? attr.noneLabel || NONE_LABEL_FALLBACK
          : valueLabel(attr, v, config),
      )
      .join(", ")}`;
  if (op.input === "days")
    return `${label} ${cond.values[0]} day${cond.values[0] === "1" ? "" : "s"}`;
  if (op.input === "hours")
    return `${label} ${cond.values[0]} hour${cond.values[0] === "1" ? "" : "s"}`;
  return `${label} ${cond.values[0]}`;
}

// --- sorting ---------------------------------------------------------

// DIRECTIONS is how "ascending" and "descending" are worded for each
// kind of attribute: nobody reads a list of dates as A–Z, and a state
// order that runs proposed through closed is neither.
const DIRECTIONS = {
  [TYPE_DATE]: { asc: "Oldest first", desc: "Newest first" },
  [TYPE_NUMBER]: { asc: "Lowest first", desc: "Highest first" },
  ordered: { asc: "First to last", desc: "Last to first" },
  text: { asc: "A–Z", desc: "Z–A" },
};

// A date that is still to come -- a schedule's next run -- is read as
// soon or late rather than old or new: "Next run, oldest first" named
// the run furthest off as the old one.
const FUTURE_DATE = { asc: "Soonest first", desc: "Latest first" };

export function directionLabels(attr) {
  if (attr.type === TYPE_DATE && attr.future) return FUTURE_DATE;
  if (DIRECTIONS[attr.type]) return DIRECTIONS[attr.type];
  return attr.order ? DIRECTIONS.ordered : DIRECTIONS.text;
}

// Every attribute can be ordered on, because every attribute reduces to
// one comparable key per task -- which is sortKey.
//
// sortKey is the one value a task is ordered by for one attribute:
// whatever the attribute nominates (a state's rank, an id as a number),
// and otherwise its first value read as its type. undefined means the
// task has no value for it, which sorts last in *both* directions --
// "Repo (A–Z)" opening on a run of tasks with no target repo would bury
// the thing the order was picked to find, and so would reversing it.
function sortKey(attr, t, config) {
  if (attr.key) return attr.key(t);
  const v = attr.values(t)[0];
  if (v === undefined) return undefined;
  if (attr.type === TYPE_DATE) return momentOf(v, config?.timeZone);
  if (attr.type === TYPE_NUMBER) {
    const n = Number(v);
    return Number.isFinite(n) ? n : undefined;
  }
  if (attr.order) {
    const i = attr.order.indexOf(v);
    return i === -1 ? attr.order.length : i;
  }
  return String(valueLabel(attr, v, config));
}

function compareKeys(x, y) {
  if (typeof x === "number" && typeof y === "number") return x - y;
  return String(x).localeCompare(String(y));
}

// comparatorFor is one sort key as a comparator, missing values last
// whichever way it points.
function comparatorFor(attr, dir, config) {
  const sign = dir === "desc" ? -1 : 1;
  return (a, b) => {
    const x = sortKey(attr, a, config);
    const y = sortKey(attr, b, config);
    const xEmpty = x === undefined || x === "";
    const yEmpty = y === undefined || y === "";
    if (xEmpty || yEmpty) return xEmpty && yEmpty ? 0 : xEmpty ? 1 : -1;
    return sign * compareKeys(x, y);
  };
}

// sortTasks puts tasks in the order the sort keys ask for: the first key
// decides, the next breaks its ties, and so on. An empty sort is the
// backlog order the store itself keeps (open tasks in backlog order,
// closed tasks sorted newest first) -- the array is handed straight
// back rather than copied, since that is already the order it is in.
//
// Nothing breaks the last tie: Array#sort is stable, so tasks that
// compare equal on every key keep the order they arrived in,
// which makes every sort "the backlog, grouped by X" rather than a
// reshuffle inside each group.
export function sortTasks(tasks, sort, attributes, config) {
  const cmps = (sort || [])
    .map(({ attr, dir }) => {
      const a = attributeById(attributes, attr);
      return a ? comparatorFor(a, dir, config) : null;
    })
    .filter(Boolean);
  if (cmps.length === 0) return tasks;
  return [...tasks].sort((a, b) => {
    for (const cmp of cmps) {
      const n = cmp(a, b);
      if (n !== 0) return n;
    }
    return 0;
  });
}

// --- asking a whole list one question --------------------------------

// narrowTasks is what a task view actually does with a narrowing, as one
// call: resolve its conditions against the attributes this deployment
// has, put the tasks in the order it asks for, and keep the ones that
// match. Nothing new -- it is resolveConditions, sortTasks and
// matchesFilters in the only order they can go in, which every view
// above spelled out for itself.
//
// # It is memoised, and the memo is what this is for
//
// A dashboard is several views of one list at once (PanelBoard.jsx), and
// every panel on it asked this question of every task on every
// three-second poll. Measured on a store of 929 tasks: 1.0-1.2s of
// script per poll tick at six panels and 2.1-2.3s at ten, which is a
// main thread saturated by a deployment where nothing is happening
// (the 2026-09-07 panel-dashboard hand run, section 5). The tasks
// were the same tasks and the question was usually the same question --
// a panel with no query of its own inherits the dashboard's -- so the
// cost was N copies of one answer, N times a minute, for as long as the
// tab was open.
//
// The cache is keyed on the identity of the `tasks` array, in a WeakMap
// so that a list nothing holds any more takes its answers with it, and
// then on the question written out as a string. Identity is what makes
// it sound rather than merely fast: a different array is a different
// entry, so an answer can never outlive the list it was computed from.
// That is also why App keeps the array it already has when a poll brings
// back the same tasks (App.jsx's refreshList) instead of handing every
// view a fresh array of identical rows every three seconds -- without
// that, every tick is a fresh identity and this cache never hits twice.
//
// The sort is cached again under the sort keys alone, because it is the
// expensive half and it does not depend on the rest of the question: two
// panels asking different things in the same order share one sorted
// array.
//
// Answers are shared, so a caller must not mutate what it is handed --
// the same deal `sortTasks` already makes by returning its input for an
// empty sort. Every caller here maps or filters it.
const answerCache = new WeakMap();

// questionKey is a narrowing as a string: two narrowings that read the
// same are the same question, however they were built. Only ever a cache
// key, so a narrowing written in a different key order is a miss and a
// recompute rather than a wrong answer.
function questionKey(search, sort, filters) {
  return JSON.stringify([search, sort || [], filters || []]);
}

// dependsOnClock is whether an answer would stop being true just by time
// passing. `within` and `coming` are the comparisons measured against
// Date.now() (matchesCondition), so a narrowing carrying one is answered
// afresh every time rather than kept.
function dependsOnClock(filters) {
  return (filters || []).some((c) => c.op === "within" || c.op === "coming");
}

// KEPT_ANSWERS is how many questions one list is remembered having been
// asked. A dashboard asks one per panel and holds still; a search box
// asks a new one per keystroke, and now that a list keeps its identity
// across polls those would otherwise pile up for as long as nothing
// changed. Oldest out first, which for a keystroke is the prefix nobody
// is going back to and for a dashboard is nothing at all -- sixteen is
// comfortably more panels than fit on a screen.
const KEPT_ANSWERS = 16;

function remember(map, key, value) {
  map.set(key, value);
  if (map.size > KEPT_ANSWERS) map.delete(map.keys().next().value);
}

export function narrowTasks(
  tasks,
  narrowing,
  config,
  { keepSessions = false } = {},
) {
  const { search = "", sort, filters } = narrowing || NO_NARROWING;
  let entry = answerCache.get(tasks);
  // config is part of the answer -- it decides the custom attributes
  // that exist, the zone a date condition is read in, and how a value
  // reads -- and it arrives as a fresh object from its own poll, so a
  // new one drops the answers rather than being a second key.
  if (!entry || entry.config !== config) {
    entry = {
      config,
      attributes: attributesFor(config),
      sorted: new Map(),
      answers: new Map(),
    };
    answerCache.set(tasks, entry);
  }
  const q = search.trim().toLowerCase();
  const key = `${questionKey(q, sort, filters)}:keepSessions=${keepSessions}`;
  const kept = dependsOnClock(filters) ? undefined : entry.answers.get(key);
  if (kept) return kept;

  const sortKey = JSON.stringify(sort || []);
  let ordered = entry.sorted.get(sortKey);
  if (!ordered) {
    ordered = sortTasks(tasks, sort, entry.attributes, config);
    remember(entry.sorted, sortKey, ordered);
  }
  const conditions = resolveConditions(filters, entry.attributes);
  // A session's task is out of a panel or a timeline for the reason it
  // is out of the list it links to (isSessionTask): the two must count
  // the same tasks. Tree boards leave them in so work a session filed
  // still nests under it (docs/work-trees.md).
  const answer = ordered.filter(
    (t) =>
      !(!keepSessions && isSessionTask(t)) &&
      matchesFilters(t, conditions, q, config),
  );
  if (!dependsOnClock(filters)) remember(entry.answers, key, answer);
  return answer;
}

// describeSort is the sort as a phrase for the toolbar's own button:
// "Newest first", or "State, then Newest first" for a sort with a
// tie-breaker.
export function describeSort(sort, attributes) {
  const parts = [];
  for (const { attr, dir } of sort || []) {
    const a = attributeById(attributes, attr);
    if (!a) continue;
    parts.push(`${a.label} ${directionLabels(a)[dir] || dir}`);
  }
  return parts.join(", then ");
}

// NO_NARROWING is a task view's toolbar with nothing asked of it at all:
// the search box empty, the backlog's own order, no condition set. It is
// the shape every narrowing takes, and what the three kinds of saved
// board that answer for themselves mean by an empty query (boards.js's
// boardNarrowing) -- a Kanban board's columns, a timeline's whole
// history, a report's every task. A list board and a dashboard mean
// something else by it: nobody has narrowed this view, which is the
// default below.
//
// It is *not* what a task view starts on: that is defaultNarrowing
// below, which is this plus the one condition every list has always had.
//
// Frozen because it is shared: App hands the same object to every task
// view, and a view that mutated it would narrow every other one.
export const NO_NARROWING = Object.freeze({
  search: "",
  sort: Object.freeze([]),
  filters: Object.freeze([]),
});

// SHOWN_BY_SETTING is the deployment-wide setting behind a set-aside
// state, for the one that has one: showClosedByDefault (ui.Settings,
// bwsalmon/agents#537) is a deployment saying its lists are about closed
// tasks too, so the default condition below stops leaving that state
// out. Deferred has none and never had one -- deferring is a per-task
// "not now", and the condition is right there in the toolbar for
// whoever wants to look. The day somebody wants a second knob, this is
// the one place that has to learn its key.
const SHOWN_BY_SETTING = { closed: "showClosedByDefault" };

// conditionFromQuery reads one `f` parameter in the shared task-list URL
// encoding. Paths and deployment defaults use this same parser so a value
// copied from the address bar cannot mean something different in config.
export function conditionFromQuery(raw) {
  const first = raw.indexOf(":");
  const second = raw.indexOf(":", first + 1);
  if (first < 1 || second < 0) return null;
  // A comparison this build does not know is kept rather than parsed
  // away: resolveConditions leaves it out of the narrowing and
  // unknownConditions names it, so a link carrying one keeps its
  // address and says what it is not applying (grain/task-1531).
  const op = raw.slice(first + 1, second);
  if (op === "") return null;
  const rest = raw.slice(second + 1);
  const unescape = (v) =>
    v.replace(/%([0-9A-Fa-f]{2})/g, (_, hex) =>
      String.fromCharCode(parseInt(hex, 16)),
    );
  return {
    attr: raw.slice(0, first),
    op,
    values: rest === "" ? [] : rest.split("|").map(unescape),
  };
}

// isLegacyInteractiveClause is the `kind:none:interactive` condition
// task views used to open narrowed by, back when interactive tasks were a
// Kind a reader could filter on. They are never in a task view now
// (isSessionTask), so the clause has nothing left to say: a deployment
// whose stored defaultFilters still carries it, or a link written while
// it was the default, drops it rather than showing a chip for an
// attribute that no longer exists.
export function isLegacyInteractiveClause(c) {
  return (
    !!c &&
    c.attr === "kind" &&
    c.op === "none" &&
    (c.values || []).length === 1 &&
    c.values[0] === "interactive"
  );
}

// defaultFilters is what every task view is narrowed by before anybody
// asks it anything. The deployment stores these ordinary conditions in
// its config repository; the historical state exclusions remain the
// fallback while config is loading and for older daemons.
export function defaultFilters(config) {
  // defaultFilters is stored in grain_config in the same readable form
  // task-list URLs use: repeated f=attribute:operator:value clauses.
  // An empty configured value deliberately means no starting filters.
  if (config && Object.hasOwn(config, "defaultFilters")) {
    const params = new URLSearchParams(config.defaultFilters || "");
    const filters = params
      .getAll("f")
      .map(conditionFromQuery)
      .filter((c) => c && !isLegacyInteractiveClause(c));
    // Keep the existing Settings checkbox effective while it remains:
    // its historical "on" value removes closed from the built-in default.
    if (
      config.showClosedByDefault &&
      filters.length === 1 &&
      isBuiltInStateClause(filters[0])
    )
      return [{ attr: "state", op: "none", values: ["deferred"] }];
    return filters;
  }

  // Compatibility for the first paint and older daemons that do not yet
  // report defaultFilters.
  const hidden = SET_ASIDE_STATES.filter((s) => !config?.[SHOWN_BY_SETTING[s]]);
  return hidden.length === 0
    ? []
    : [{ attr: "state", op: "none", values: hidden }];
}

// isBuiltInStateClause is the built-in default's own state condition,
// `state:none:deferred|closed`, which showClosedByDefault rewrites.
function isBuiltInStateClause(c) {
  return (
    c.attr === "state" &&
    c.op === "none" &&
    (c.values || []).length === 2 &&
    c.values[0] === "deferred" &&
    c.values[1] === "closed"
  );
}

// isSessionTask is whether a task is a live chat rather than backlog
// work: a standing session's own task (model.ConsoleSessionIDOf,
// ui.Task.ConsoleSession) or any other interactive task. Such a task is
// parked on its own conversation between exchanges, so a view that
// admitted it would carry one permanent "Awaiting reply" entry per
// session; sessions live in the Sessions pane (SessionsPage.jsx)
// instead, and task lists and boards always leave them out.
export function isSessionTask(t) {
  return !!(t.consoleSession || t.interactive);
}

// defaultNarrowing is that condition as a whole narrowing -- the value a
// task view falls back to when the URL carries none, and what Clear puts
// a view back to.
//
// It is worked out from `config` at the moment it is needed rather than
// seeded once, because config arrives a fetch after the first render:
// anything that captured a default before it would be answering with the
// deployment's setting missing.
//
// A fallback for "nobody asked", and deliberately not a floor under
// what a link may ask: a narrowing that parsed and names no state --
// which is every link written before this condition existed -- is left
// as written, so it opens showing closed and deferred tasks. Folding
// the condition back into such a narrowing would make a list *with*
// them unsayable in a link, since that is exactly the narrowing with no
// state clause in it. Pinned in taskFilters.test.js ("a link written
// before the default was a condition") and argued in the README.
export function defaultNarrowing(config) {
  return { search: "", sort: [], filters: defaultFilters(config) };
}

// defaultSortFor is the order a state in the rail opens its list in
// when nobody has asked for one. Every state opens in backlog order
// except Merged, whose list is a history of what landed and reads
// newest merged first (taskAttributes.js's "merged" attribute). App's
// listDefault and selectStateFilter ask it; isNarrowed leaves the sort
// out entirely, so this default never offers Clear on its own.
export const MERGED_SORT = [{ attr: "merged", dir: "desc" }];
export function defaultSortFor(stateFilter) {
  return stateFilter === "merged" ? MERGED_SORT : [];
}

// isDefaultSort is whether a sort is exactly one defaultSortFor hands
// out -- the one a list may swap for another state's default as the
// reader moves along the rail, since nobody chose it.
export function isDefaultSort(sort) {
  const keys = (s) => (s || []).map((k) => `${k.attr}:${k.dir}`).join(",");
  return keys(sort) === "" || keys(sort) === keys(MERGED_SORT);
}

// sameFilters is whether two lists of conditions ask the same thing, in
// the same order -- which is all "is this list still on its default?"
// needs, since the default is one condition nobody has touched.
function sameFilters(a, b) {
  const key = (c) => `${c.attr}:${c.op}:${(c.values || []).join("|")}`;
  const x = (a || []).map(key);
  const y = (b || []).map(key);
  return x.length === y.length && x.every((k, i) => k === y[i]);
}

// isNarrowed is whether a narrowing asks anything of a view beyond what
// that view opens asking, the sort aside -- which is what the Clear
// button is offered on, and what an empty list checks before blaming the
// filters for it. The sort is not part of it: an order is not something
// a list has to be rescued from.
//
// `defaults` is what the view opens narrowed by, which is not the same
// answer everywhere: the task list opens on defaultFilters above, and a
// saved board opens on its own stored question (boards.js), which is
// already the whole of what that board is about. Left out, it means a
// view that opens narrowed by nothing at all -- which is what a board
// with no query is.
//
// Measured against that rather than against emptiness because the states
// a list leaves out are a condition now: a list showing exactly what it
// shows on arrival is not one anybody needs rescuing from, and Clear is
// how you get back there -- including from a list whose conditions have
// been deleted down to none, which asks for *more* than the default
// rather than less.
export function isNarrowed(narrowing, defaults = []) {
  return (
    (narrowing.search || "").trim() !== "" ||
    !sameFilters(narrowing.filters, defaults)
  );
}
