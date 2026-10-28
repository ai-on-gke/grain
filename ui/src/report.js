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

// report.js is everything the UI knows about a report board: which
// sections the measurement report has, and -- the part that took a
// decision -- what a board's stored query means to a report at all
// (grain/task-83).
//
// A report board is the first kind of saved dashboard that is not a view
// of the backlog (boards.js's BOARD_KINDS). Every other kind draws the
// task rows App already holds, narrowed by taskFilters.js; this one draws
// GET /api/metrics, which is computed by the daemon out of every task's
// timeline and cannot be filtered client-side at all -- a p90 over a
// subset is not a subset of a p90.
//
// # What a query means to a report
//
// A board carries one query, in the encoding the address bar uses, and
// that field is kind-agnostic on purpose (model.Board). So the question
// this file answers is what a set of conditions on *tasks* can honestly
// say about a report over those tasks, and the answer is: three of them.
//
//   - `repo`, as "is any of" -- which repos the report is about
//   - `author`, as "is any of" -- whose work it is about
//   - `created`, as "within the last N days", "is after" or "is before"
//     -- the span the report measures over
//
// Those three become metrics.Scope and the report's window, sent as
// ?repo=, ?author=, ?since= and ?until=, and the daemon narrows the rows
// before it measures anything.
//
// Everything else in a query is dropped, and the page says so out loud
// (reportScope's `ignored`). A text search has nothing to match in a
// distribution; a state is a fact about where a task is *now* and a
// report is about what already happened; a sort has no rows to order.
// The alternative -- refusing to render a report board whose query says
// more than a report can use -- would make the obvious gesture ("I am
// looking at this list, save it as a report") fail for most lists, and
// silently ignoring it would be worse than either.
//
// # A date range is read as the report's window
//
// This is the one reading here that is not literal, and it is worth
// being plain about. `created:within:30` on a task list means "tasks
// filed in the last thirty days"; as a report's window it means "measure
// the last thirty days", and a window bounds measurements rather than
// rows (pkg/metrics) -- a task filed in March and completed today
// contributes its whole lead time to a report over today. The two are
// therefore not the same set of tasks.
//
// It is still the right reading, because it is the only one that answers
// what somebody asking for it meant. A report whose window was fixed and
// whose *tasks* were filtered by filing date would report a throughput
// over a month with most of that month's completions missing.
// ReportBoard.jsx prints the window it used, so the reading is on screen
// rather than in this comment alone.
import { NONE } from "./taskAttributes.js";
import { NO_NARROWING, needsValue } from "./taskFilters.js";

// REPORT_SECTIONS is every section of the report, in the order the
// report itself is written -- which is both the order a board with no
// sections of its own prints and the order the prompt offers them. Each
// is a value here, a word in model.ReportSections and a branch in
// ReportBoard.jsx; BOARD_KINDS' own deal.
//
// `summary` is what each one is for, in one line, and it is not
// decoration: it is what the "New board" prompt hands an agent choosing
// between them, so a section this build gains cannot leave that prompt
// describing six of seven.
export const REPORT_SECTIONS = [
  {
    id: "throughput",
    label: "Throughput",
    summary: "what crossed each line over the window, and the trend lines",
  },
  {
    id: "latency",
    label: "Latency",
    summary: "where a task's wall-clock time went, one distribution per stage",
  },
  {
    // Labelled after the heading its body draws (MetricsSections.jsx's
    // CapacitySection), not after its id: this label names the section's
    // move handle and its "nothing to report" line, and a handle reading
    // "Attempts" over a heading reading "Capacity" named a section the
    // page does not have.
    id: "runs",
    label: "Capacity",
    summary:
      "how the window's attempts ended, and what share of capacity they took",
  },
  {
    id: "backlog",
    label: "Backlog",
    summary: "what was still unfinished when the report was taken",
  },
  {
    id: "tools",
    label: "Tool use",
    summary:
      "what a run's tool calls cost it, and which tool its errors are in",
  },
  {
    id: "prompts",
    label: "Prompts",
    summary:
      "how big the prompts runs were handed are, and which tasks carry the heaviest",
  },
  {
    id: "checks",
    label: "CI waits",
    summary: "the push, wait, fix, push again loop, measured end to end",
  },
  {
    id: "ci_queueing",
    label: "CI queueing",
    summary:
      "how much of CI's job-time went on waiting for a machine, what held it, and which repositories wear it",
  },
  {
    id: "github_ci",
    label: "GitHub CI",
    summary:
      "the CI beside grain's own, labelled source github: pass, fail and cancel rates per job, time to green, flaky jobs and base-branch red time",
  },
  {
    id: "merges",
    label: "Merge queue",
    summary:
      "what landed, how long each change took, and what batching and branch repairs cost",
  },
  {
    id: "cycles",
    label: "Scheduling",
    summary: "the daemon's own ticks -- the one section a scope never narrows",
  },
  {
    id: "sandboxes",
    label: "Sandboxes",
    summary:
      "how many sandboxes this deployment has built and what they cost -- since it started counting, not over the window",
  },
  {
    id: "agents",
    label: "Agents",
    summary:
      "per-template calls and bytes per wake, elisions, re-reads, and steward report verification rates",
  },
];

export const REPORT_SECTIONS_BY_ID = Object.fromEntries(
  REPORT_SECTIONS.map((s) => [s.id, s]),
);

// REPORT_WINDOWS are the ?window= values a report's picker offers. They
// are the strings ui.ParseMetricsWindow already accepts (a Go duration,
// or a count of days or weeks), sent through verbatim rather than
// converted to seconds, so the picker and `grain metrics -window` speak
// the same vocabulary and a window read off one can be typed into the
// other.
//
// They live here rather than beside the picker because there is one
// picker for two callers now (ReportBoard.jsx) and because panels.js
// asks the same question of a throughput panel's `range`: the four
// spans a report can be asked for are a fact about the report, not
// about the control.
export const REPORT_WINDOWS = [
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
  { value: "90d", label: "Last 90 days" },
];

// DEFAULT_REPORT_WINDOW matches ui.DefaultMetricsWindow, so the first
// report anybody sees -- on the Metrics pane, on a report board whose
// query names no span, or in a throughput panel -- is the same one
// `grain metrics` prints with no flags.
export const DEFAULT_REPORT_WINDOW = "7d";

// WINDOW_SYNONYMS are the spans a narrowing and the picker say
// differently. A narrowing counts days ("created is within the last 1"),
// and one day is the window this vocabulary calls "24h" -- the same span
// under the name gantt.js's ranges already gave it. Without this a link
// carrying a one-day window would arrive at a picker with no option
// matching it, which is a control showing blank beside a report it is
// supposed to describe.
const WINDOW_SYNONYMS = { "1d": "24h" };

export function reportSectionLabel(id) {
  return REPORT_SECTIONS_BY_ID[id]?.label || id;
}

// normalizeSections is what stands between rows somebody hand-edited into
// tables/board_section.json and a render (normalizePanels' own job). A
// section naming a kind this build does not have is dropped rather than
// falling back to another one: unlike a panel, where "show the tasks
// instead" is a sensible degradation, there is no section that stands in
// for another, and drawing the throughput because a row said "burndown"
// would be a lie about what is on screen.
export function normalizeSections(raw) {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((s, i) => {
      const row = s && typeof s === "object" ? s : {};
      if (!REPORT_SECTIONS_BY_ID[row.kind]) return null;
      return {
        key: `section-${i}`,
        kind: row.kind,
        title: typeof row.title === "string" ? row.title : "",
        orderKey: typeof row.orderKey === "number" ? row.orderKey : 0,
        // Reordering replaces every row; keep the stored vocabulary rather
        // than writing a rendering fallback back through the board.
        stored: {
          kind: typeof row.kind === "string" ? row.kind : "",
          title: typeof row.title === "string" ? row.title : "",
        },
      };
    })
    .filter(Boolean);
}

export function sectionRow(section, orderKey) {
  return { ...(section?.stored || {}), orderKey };
}

// boardSections is which sections a report board prints: its own, when it
// names any, and otherwise the whole report. The same fallback a Kanban
// board with no columns gets (boards.js's boardColumns) and for the same
// reason -- a board asked for as "a report on acme/widgets" should not
// have to choose a layout to be a board.
//
// A board whose every section named something this build has dropped
// falls back too, rather than rendering as a page with no sections at
// all: an empty list here is indistinguishable from "the board says
// nothing", which is exactly the case the fallback is for.
export function boardSections(board) {
  const own = normalizeSections(board?.sections);
  if (own.length > 0) return own;
  return REPORT_SECTIONS.map((s) => ({
    key: `section-${s.id}`,
    kind: s.id,
    title: "",
  }));
}

// sectionTitle is the heading a section draws: its own, or the name of
// what it is (panelTitle's own shape).
export function sectionTitle(section) {
  return section?.title || reportSectionLabel(section?.kind);
}

// reportScope is what a narrowing says to a report: the scope the daemon
// can compute under, the span to measure, and -- as important -- what it
// could not use.
//
// It takes a narrowing rather than a query string because the toolbar
// over a report board narrows the same object every other task view's
// does (App's own), so "this report, but only acme/widgets, just for a
// minute" goes down the same path as the board's stored question. The
// board's query is turned into one by boardNarrowing.
//
// The three conditions it can use are listed at the top of this file.
// `ignored` is one line per condition it could not, phrased for a reader
// rather than for a parser, because the honest thing a page can do about
// a query it half understands is say which half.
export function reportScope(narrowing, attributes) {
  const scope = { repos: [], authors: [], within: "", since: "", until: "" };
  const ignored = [];
  const n = narrowing || NO_NARROWING;
  if (n.search) {
    ignored.push(`the search for "${n.search}"`);
  }
  for (const cond of n.filters || []) {
    if (cond && needsValue({ ...cond, values: cond.values || [] })) continue;
    const values = (cond?.values || []).filter((v) => v !== NONE);
    const label = conditionLabel(cond, attributes);
    if (cond?.attr === "repo" && cond.op === "any" && values.length > 0) {
      scope.repos.push(...values);
      continue;
    }
    if (cond?.attr === "author" && cond.op === "any" && values.length > 0) {
      scope.authors.push(...values);
      continue;
    }
    if (cond?.attr === "created" && values.length > 0) {
      // The last one of each wins rather than the first, so a reader who
      // has just typed a date over the board's own gets the one they
      // typed -- App appends to the condition list.
      const days = Number(values[0]);
      if (
        cond.op === "within" &&
        Number.isInteger(days) &&
        days >= 1 &&
        days <= 365
      ) {
        scope.within = `${days}d`;
        continue;
      }
      if (cond.op === "after") {
        scope.since = values[0];
        continue;
      }
      if (cond.op === "before") {
        scope.until = values[0];
        continue;
      }
    }
    ignored.push(label);
  }
  return { ...scope, ignored };
}

// conditionLabel is one condition as a phrase for the "not used" note --
// "State is any of running", falling back to the raw attribute id for
// one this build has no declaration for (a link from a newer build, the
// case every other reader of a narrowing already tolerates).
function conditionLabel(cond, attributes) {
  const attr = (attributes || []).find((a) => a.id === cond?.attr);
  const name = attr?.label || cond?.attr || "a condition";
  const values = (cond?.values || []).join(", ");
  return values ? `${name} (${values})` : name;
}

// reportPath is the query string GET /api/metrics is asked with: the
// scope, the span, and how many points the trend lines carry.
//
// The span is said one of the two ways the endpoint takes. An absolute
// one (the query named dates) goes as ?since=/?until=; anything else is
// a length ending now, which is what the reader's own window picker
// offers. A query naming both a date and a window is not a shape the
// picker can produce -- absolute wins, because it is the more specific
// thing to have said.
export function reportPath({ scope, window, buckets }) {
  const params = new URLSearchParams();
  for (const repo of scope?.repos || []) params.append("repo", repo);
  for (const author of scope?.authors || []) params.append("author", author);
  if (scope?.since || scope?.until) {
    if (scope.since) params.set("since", scope.since);
    if (scope.until) params.set("until", scope.until);
  } else if (window) {
    params.set("window", window);
  }
  if (buckets) params.set("buckets", String(buckets));
  return `/api/metrics?${params.toString()}`;
}

// reportWindow is the span a report opens on: the one its query names,
// or the default every report here falls back to. Only the relative
// half -- an absolute span is not one of the picker's options, and
// reportPath sends it instead of a window.
//
// A span the picker spells differently is given back in the picker's
// own spelling (WINDOW_SYNONYMS), so the window a link asked for and
// the option showing beside the report are the same string.
export function reportWindow(scope, fallback) {
  const within = scope?.within;
  if (!within) return fallback;
  return WINDOW_SYNONYMS[within] || within;
}

// hasAbsoluteSpan says whether the scope names actual dates rather than a
// length ending now, which is what the window picker has to stand aside
// for: offering "Last 7 days" beside a report that is deliberately about
// last March would be a control that lies about what is on screen.
export function hasAbsoluteSpan(scope) {
  return Boolean(scope?.since || scope?.until);
}

// scopeSummary is the scope as a phrase under the board's name -- "in
// acme/widgets, by ada" -- or "" for a report over everything, which
// needs no note because it is what a report has always been.
export function scopeSummary(scope) {
  const parts = [];
  if (scope?.repos?.length) parts.push(`in ${scope.repos.join(" or ")}`);
  if (scope?.authors?.length) parts.push(`by ${scope.authors.join(" or ")}`);
  return parts.join(", ");
}
