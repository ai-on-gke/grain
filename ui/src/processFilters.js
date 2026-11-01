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

// processFilters.js is the Processes page's vocabulary for narrowing
// itself (grain/task-429): the attributes a schedule, a workflow
// schedule, a template, a suite or a trigger can be asked about, over
// the task list's own machinery -- taskFilters.js's conditions and
// sorts, NarrowingBar.jsx's toolbar -- the way ciFilters.js is the CI
// pane's.
//
// # One vocabulary for the whole page
//
// The page lists five kinds of thing in one table, and the attributes
// here are one list over all of them rather than a list per kind. Each
// says which kinds it is about (`kinds`), and a row of any other kind
// simply has no value for it -- so "Enabled is any of No" is a question
// only schedules can answer yes to, and narrows the page to paused
// schedules without anybody having to pick the kind first. The kind is
// itself an attribute, and the chips under the toolbar are a shortcut
// for the one "Kind is any of" condition (kindsOf and withKinds below)
// rather than a filter of their own.
//
// # The same words as the API
//
// The schedule attributes are pkg/schedulefilter's, by the same ids and
// over the same values, and the page writes its narrowing into the
// address bar in the encoding GET /api/schedules reads -- so the query a
// filtered page shows can be pasted onto /api/schedules and answers with
// the same schedules. A schedule's source and type come off the wire
// (ui.Schedule.Source, .Type); sourceOf and scheduleTypeOf below work
// them out the same way for a daemon too old to send them.
//
// # Where the choice is kept
//
// In the URL, as the task list's is and unlike the CI pane's: a
// Processes view is a thing an operator links to ("everything
// bwsalmon/grain declares that is paused"), and it has to survive a
// reload and Back. paths.js reads and writes it with the functions at
// the bottom of this file.

import { NONE, TYPE_DATE, TYPE_ENUM, TYPE_TEXT } from "./taskAttributes.js";
import {
  conditionFromQuery,
  matchesFilters,
  resolveConditions,
  sortTasks,
} from "./taskFilters.js";
import { PROCESS_KINDS, describeRecurrence, parseKinds } from "./processes.js";

const KIND_IDS = PROCESS_KINDS.map((k) => k.id);
const SCHEDULES = ["schedule", "workflow"];
const isSchedule = (row) => SCHEDULES.includes(row.kind);
const one = (v) => (v ? [v] : []);
const yesNo = (b) => [b ? "yes" : "no"];
const YES_NO = ["yes", "no"];
const YES_NO_LABELS = { yes: "Yes", no: "No" };
const instant = (iso) => {
  const n = iso ? Date.parse(iso) : NaN;
  return Number.isFinite(n) ? n : undefined;
};

// SOURCE_SYSTEM and SOURCE_DEPLOYMENT are the two sources that are not a
// repository: one of grain's own standing schedules, and one somebody
// created on this deployment -- in the UI, over the API, or in the state
// repository's tables, which are the same rows (schedulefilter's
// SourceSystem and SourceDeployment).
export const SOURCE_SYSTEM = "system";
export const SOURCE_DEPLOYMENT = "deployment";

// sourceOf is where a row is declared: the repository for one a
// repository's `.grain/` declares (a trigger always is), and otherwise
// one of the two words above.
export function sourceOf(row) {
  const item = row.item || {};
  if (row.kind === "trigger") return row.repo;
  if (isSchedule(row) && item.source) return item.source;
  if (row.declaredIn?.repo) return row.declaredIn.repo;
  if (row.system) return SOURCE_SYSTEM;
  return SOURCE_DEPLOYMENT;
}

// SCHEDULE_TYPES is what a schedule's firing does, in menu order --
// schedulefilter.Types.
export const SCHEDULE_TYPES = [
  "template",
  "task",
  "suite",
  "workflow",
  "system",
];
const SCHEDULE_TYPE_LABELS = {
  template: "Files a task from a template",
  task: "Files its own task",
  suite: "Runs a suite",
  workflow: "Runs a workflow",
  system: "grain's own job",
};

// scheduleTypeOf is ui.Schedule.Type, worked out from the fields it is
// derived from when the daemon did not send it.
export function scheduleTypeOf(s) {
  if (s.type) return s.type;
  if (s.suiteId) return "suite";
  if (s.workflowReview) return "workflow";
  if (s.system) return "system";
  if (s.templateId) return "template";
  return "task";
}

const CADENCE_ORDER = [
  "everyNMinutes",
  "everyNHours",
  "daily",
  "weekly",
  "monthly",
];
const CADENCE_LABELS = {
  everyNMinutes: "Every N minutes",
  everyNHours: "Every N hours",
  daily: "Daily",
  weekly: "Weekly",
  monthly: "Monthly",
};

const OUTCOME_LABELS = {
  fired: "Fired",
  "not-firing": "Not firing",
  success: "Success",
  failure: "Failure",
  errored: "Errored",
  cancelled: "Cancelled",
  skipped: "Skipped",
};

// lastOutcomeOf is how a schedule's last firing went -- schedulefilter's
// LastOutcome: a workflow's CI conclusion, "not-firing" for one held by
// its previous firing, "fired" for any other that has, and nothing for
// one that never has.
function lastOutcomeOf(s) {
  if (s.lastWorkflowRun?.conclusion) return [s.lastWorkflowRun.conclusion];
  if (s.suppressed) return ["not-firing"];
  if (s.lastRunAt) return ["fired"];
  return [];
}

// templateNamesOf is the templates a row fires or runs: a schedule's
// one, or a suite's items. The page's names for them rather than their
// ids, since that is what the row shows.
function templateNamesOf(row) {
  const item = row.item || {};
  if (isSchedule(row))
    return item.templateId ? [item.templateName || item.templateId] : [];
  if (row.kind === "suite")
    return (item.items || [])
      .map((it) => it.templateName || it.templateId)
      .filter(Boolean);
  return [];
}

// targetReposOf is the repositories a row targets -- not its "Repo"
// column, which says "any" and "several" where this has to say which.
const targetReposOf = (row) => row.targetRepos || [];

// only is an attribute's values for the kinds it is about, and nothing
// for every other kind.
const only = (kinds, values) => (row) =>
  kinds.includes(row.kind) ? values(row) : [];

// PROCESS_ATTRIBUTES is everything a Processes row can be asked about,
// in the order the "add a filter" menu offers it: the ones an operator
// reaches for first -- what kind of thing, where it is declared, what it
// fires, its name -- then the rest.
//
// The schedule attributes are pkg/schedulefilter's, id for id; an
// attribute here that the API had no reading of would be a question
// only one of the two could ask. The template and suite ones are this
// page's alone, since only schedules have a filtered API.
export const PROCESS_ATTRIBUTES = [
  {
    id: "kind",
    label: "Kind",
    type: TYPE_ENUM,
    values: (row) => [row.kind],
    order: KIND_IDS,
    labelOf: (v) => PROCESS_KINDS.find((k) => k.id === v)?.label || v,
    glyphOf: (v) => PROCESS_KINDS.find((k) => k.id === v)?.glyph || null,
  },
  {
    id: "source",
    label: "Source",
    type: TYPE_ENUM,
    values: (row) => [sourceOf(row)],
    labelOf: (v) =>
      v === SOURCE_SYSTEM
        ? "grain (system)"
        : v === SOURCE_DEPLOYMENT
          ? "This deployment (UI, API or config repo)"
          : v,
    glyphOf: (v) =>
      v === SOURCE_SYSTEM || v === SOURCE_DEPLOYMENT ? null : "repos",
  },
  {
    id: "type",
    label: "Type",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => [scheduleTypeOf(row.item)]),
    order: SCHEDULE_TYPES,
    labelOf: (v) => SCHEDULE_TYPE_LABELS[v] || v,
    noneLabel: "Not a schedule",
  },
  {
    id: "name",
    label: "Name",
    type: TYPE_TEXT,
    values: (row) => one(row.name),
  },
  {
    id: "repo",
    label: "Target repo",
    type: TYPE_ENUM,
    values: targetReposOf,
    noneLabel: "Any repo",
    glyphOf: (v) => (v === NONE ? null : "repos"),
  },
  {
    id: "base",
    label: "Base branch",
    kinds: [...SCHEDULES, "template"],
    type: TYPE_ENUM,
    values: only([...SCHEDULES, "template"], (row) => one(row.item?.base)),
    noneLabel: "Default branch",
  },
  {
    id: "template",
    label: "Template",
    kinds: [...SCHEDULES, "suite"],
    type: TYPE_ENUM,
    values: templateNamesOf,
    noneLabel: "No template",
    glyphOf: (v) => (v === NONE ? null : "templates"),
  },
  {
    id: "suite",
    label: "Suite",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) =>
      row.item?.suiteId ? [row.item.suiteName || row.item.suiteId] : [],
    ),
    noneLabel: "No suite",
    glyphOf: (v) => (v === NONE ? null : "suites"),
  },
  {
    id: "enabled",
    label: "Enabled",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => yesNo(row.enabled)),
    order: YES_NO,
    labelOf: (v) => YES_NO_LABELS[v] || v,
  },
  {
    id: "cadence",
    label: "Cadence",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => one(row.item?.recurrence?.kind)),
    order: CADENCE_ORDER,
    labelOf: (v) => CADENCE_LABELS[v] || v,
  },
  {
    id: "timeZone",
    label: "Time zone",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => [
      row.item?.recurrence?.timeZone || row.item?.timeZone || "UTC",
    ]),
  },
  {
    id: "autoApprove",
    label: "Auto-approve proposals",
    kinds: [...SCHEDULES, "suite"],
    type: TYPE_ENUM,
    values: only([...SCHEDULES, "suite"], (row) =>
      yesNo(row.item?.autoApproveProposals),
    ),
    order: YES_NO,
    labelOf: (v) => YES_NO_LABELS[v] || v,
  },
  {
    id: "autoMerge",
    label: "Auto-merge",
    kinds: [...SCHEDULES, "template", "suite"],
    type: TYPE_ENUM,
    values: only([...SCHEDULES, "template", "suite"], (row) =>
      yesNo(row.item?.autoMerge),
    ),
    order: YES_NO,
    labelOf: (v) => YES_NO_LABELS[v] || v,
  },
  {
    id: "overlap",
    label: "Overlap",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => one(row.item?.overlap || "skip")),
    order: ["skip", "allow"],
    labelOf: (v) =>
      v === "skip" ? "Skips while running" : v === "allow" ? "Can overlap" : v,
  },
  {
    id: "requireApproval",
    label: "Requires approval",
    kinds: ["suite"],
    type: TYPE_ENUM,
    values: only(["suite"], (row) => yesNo(row.item?.requireApproval)),
    order: YES_NO,
    labelOf: (v) => YES_NO_LABELS[v] || v,
  },
  {
    id: "next",
    label: "Next run",
    type: TYPE_DATE,
    // "Is within the next N hours" -- what fires before the end of the
    // shift (taskFilters.js's `coming`).
    future: true,
    values: (row) => one(row.nextRunAt),
    key: (row) => instant(row.nextRunAt),
  },
  {
    id: "last",
    label: "Last run",
    type: TYPE_DATE,
    values: (row) => one(row.lastRunAt),
    key: (row) => instant(row.lastRunAt),
  },
  {
    id: "lastOutcome",
    label: "Last outcome",
    kinds: SCHEDULES,
    type: TYPE_ENUM,
    values: only(SCHEDULES, (row) => lastOutcomeOf(row.item || {})),
    labelOf: (v) => OUTCOME_LABELS[v] || v,
    noneLabel: "Never fired",
  },
  {
    id: "created",
    label: "Created",
    type: TYPE_DATE,
    values: (row) => one(row.item?.createdAt),
    key: (row) => instant(row.item?.createdAt),
  },
];

// DEFAULT_PROCESS_NARROWING is what the page opens on: nothing filtered,
// soonest next run first -- "when does this next run" is the question
// the page is opened to answer. An empty sort is the page's custom
// order, the one a drag changes.
export const DEFAULT_PROCESS_SORT = Object.freeze([
  Object.freeze({ attr: "next", dir: "asc" }),
]);
export const DEFAULT_PROCESS_NARROWING = Object.freeze({
  search: "",
  sort: DEFAULT_PROCESS_SORT,
  filters: Object.freeze([]),
});

// processSearchText is what the search box looks through: the name and
// the id, the repository, what it fires, where it is declared, and the
// cadence the row's chip says -- schedulefilter.SearchText's list, plus
// the chips only the page draws. `extra` is what this module cannot word
// itself (a suite's mode, SuiteRunPane.jsx's describeMode).
export function processSearchText(row, extra = []) {
  const item = row.item || {};
  return [
    row.name,
    row.id,
    row.repo,
    item.title,
    item.templateName,
    item.suiteName,
    row.declaredIn?.repo,
    row.declaredIn?.path,
    item.recurrence ? describeRecurrence(item.recurrence) : "",
    ...extra,
  ]
    .filter(Boolean)
    .join(" ");
}

// narrowProcesses is the page's whole question as one call: the rows in
// the order asked for, keeping the ones every condition and the search
// accept. `ordered` is the rows in the page's custom order, which is
// what an empty sort hands back; ties in any other sort fall back to
// the name, as the old Select's orders did.
export function narrowProcesses(rows, narrowing, config, extraOf = () => []) {
  const { search = "", sort = [], filters = [] } = narrowing || {};
  const conditions = resolveConditions(filters, PROCESS_ATTRIBUTES);
  const q = search.trim().toLowerCase();
  const byName = [...rows].sort((a, b) => a.name.localeCompare(b.name));
  const ordered =
    sort.length === 0
      ? rows
      : sortTasks(byName, sort, PROCESS_ATTRIBUTES, config);
  return ordered.filter((row) =>
    matchesFilters(row, conditions, q, config, (r) =>
      processSearchText(r, extraOf(r)),
    ),
  );
}

// kindsOf is which kinds the chips show picked: the values of the one
// "Kind is any of" condition, or none (All) when there is not exactly
// one. A narrowing that says something else about kind -- "is none of",
// or two conditions -- leaves the chips on All, and the toolbar says
// what it is asking.
export function kindsOf(filters) {
  const kinds = (filters || []).filter((c) => c.attr === "kind");
  if (kinds.length !== 1 || kinds[0].op !== "any") return [];
  return KIND_IDS.filter((k) => (kinds[0].values || []).includes(k));
}

// withKinds is `filters` with the chips' kinds as its kind condition:
// replacing the one there is, or added at the front, or taken away
// altogether for All (which every kind at once also is).
export function withKinds(filters, kinds) {
  const list = KIND_IDS.filter((k) => (kinds || []).includes(k));
  const all = list.length === 0 || list.length === KIND_IDS.length;
  const cond = { attr: "kind", op: "any", values: list };
  const rest = filters || [];
  const at = rest.findIndex((c) => c.attr === "kind");
  if (at === -1) return all ? [...rest] : [cond, ...rest];
  return all
    ? rest.filter((_, i) => i !== at)
    : rest.map((c, i) => (i === at ? cond : c));
}

// kindNarrowing is the page narrowed to some kinds and nothing else --
// what the old /schedules, /templates and /suites paths open as.
export function kindNarrowing(kinds) {
  return { ...DEFAULT_PROCESS_NARROWING, filters: withKinds([], kinds) };
}

// processNarrowingOf is the narrowing a query object stands for: a
// narrowing as it is, or the shape the page held before it had one --
// {kinds, q, sort}, with sort one of the old Select's words -- read as
// the narrowing that means the same. Null or empty is the default.
export function processNarrowingOf(query) {
  if (!query) return DEFAULT_PROCESS_NARROWING;
  if (Array.isArray(query.filters) || Array.isArray(query.sort))
    return {
      search: query.search || "",
      sort: Array.isArray(query.sort) ? query.sort : DEFAULT_PROCESS_SORT,
      filters: Array.isArray(query.filters) ? query.filters : [],
    };
  return {
    search: query.q || "",
    sort:
      typeof query.sort === "string" && LEGACY_SORTS[query.sort]
        ? LEGACY_SORTS[query.sort]
        : DEFAULT_PROCESS_SORT,
    filters: withKinds([], query.kinds || []),
  };
}

// LEGACY_SORTS are the orders the page's one Select wrote as `?sort=`
// before it had sort keys: a kept link sorts the way it said. "custom"
// is also how the empty sort -- the custom order -- is written now,
// since the empty string is the default.
const LEGACY_SORTS = {
  next: [{ attr: "next", dir: "asc" }],
  last: [{ attr: "last", dir: "desc" }],
  name: [{ attr: "name", dir: "asc" }],
  kind: [{ attr: "kind", dir: "asc" }],
  custom: [],
};

const sortWord = (sort) => sort.map((k) => `${k.attr}:${k.dir}`).join(",");

// processNarrowingFromSearch reads the page's query string: `q`, `sort`
// and one `f` per condition, in the task list's encoding (paths.js), and
// the old `?kind=schedule,suite` as the kind condition it now is. Null
// when it asks nothing, which is the default.
export function processNarrowingFromSearch(params) {
  const filters = params.getAll("f").map(conditionFromQuery).filter(Boolean);
  const kinds = parseKinds(params.get("kind"));
  const q = params.get("q") || "";
  const rawSort = params.get("sort") || "";
  if (filters.length === 0 && kinds.length === 0 && !q && !rawSort) return null;
  let sort = DEFAULT_PROCESS_SORT;
  if (rawSort in LEGACY_SORTS) sort = LEGACY_SORTS[rawSort];
  else if (rawSort) {
    // Only the keys this page has an attribute for, the way parseKinds
    // drops a kind it does not know: a key naming none orders nothing,
    // and kept it left a link's `?sort=bogus` showing the default rows
    // under a sort nobody could see or take back. Nothing left is the
    // default order rather than the custom one, which only "custom" asks for.
    const keys = rawSort
      .split(",")
      .map((part) => part.split(":"))
      .filter(([attr]) => PROCESS_ATTRIBUTES.some((a) => a.id === attr))
      .map(([attr, dir]) => ({ attr, dir: dir === "desc" ? "desc" : "asc" }));
    if (keys.length > 0) sort = keys;
  }
  return {
    search: q,
    sort,
    filters: kinds.length ? withKinds(filters, kinds) : filters,
  };
}

const SEPARATORS = /[%:|,]/g;
const escapeValue = (v) =>
  String(v).replace(
    SEPARATORS,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );

// processNarrowingQuery is its inverse: "" for the default, so an
// un-narrowed page still lives at /processes, and otherwise the search,
// the sort where it is not the default, and the conditions in the order
// the editor holds them -- with the separators written as themselves,
// since this is a link somebody reads before sending it.
//
// The chips' kind condition is written the way the page has always
// written it, `?kind=schedule,suite`, first -- so the links the rail,
// the old /schedules paths and every bookmark already use are still the
// address of the view they open. Any other condition on kind ("is none
// of") is an ordinary `f`.
export function processNarrowingQuery(query) {
  const narrowing = processNarrowingOf(query);
  const params = new URLSearchParams();
  const kinds = kindsOf(narrowing.filters);
  if (kinds.length) params.set("kind", kinds.join(","));
  if (narrowing.search) params.set("q", narrowing.search);
  const sort = narrowing.sort || [];
  if (sort.length === 0) params.set("sort", "custom");
  else if (sortWord(sort) !== sortWord(DEFAULT_PROCESS_SORT))
    params.set("sort", sortWord(sort));
  for (const c of narrowing.filters || []) {
    if (kinds.length && c.attr === "kind") continue;
    params.append(
      "f",
      `${c.attr}:${c.op}:${(c.values || []).map(escapeValue).join("|")}`,
    );
  }
  const text = params
    .toString()
    .replace(/%2F/g, "/")
    .replace(/%3A/g, ":")
    .replace(/%7C/g, "|")
    .replace(/%2C/g, ",");
  return text ? `?${text}` : "";
}
