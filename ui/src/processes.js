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

// processes.js is the Processes page's model (docs/design/settings-panel.md
// §5): the schedules, workflow schedules, templates, suites and triggers
// the four old lists showed, normalised into one row shape so one table
// can list, search, sort and filter them together. What the rows can be
// asked, and in what order they come, is processFilters.js.
//
// Nothing here fetches. ProcessesList is handed what App already holds
// -- GET /api/schedules, /api/templates, /api/suites, /api/suite-runs --
// and the triggers out of each declaring repository's declarations
// status, and this module only reads them.

// PROCESS_KINDS is the kind filter's chips, in the order the page shows
// them (§5.1). `param` is how the kind is spelled in `?kind=`, `chip` is
// what a row's kind chip says, and `glyph` is the figure the chip
// carries. A workflow schedule is a schedule, so it carries the
// schedules figure and opens at /processes/schedules/<id>.
export const PROCESS_KINDS = [
  {
    id: "schedule",
    label: "Schedules",
    chip: "Schedule",
    glyph: "schedules",
  },
  {
    id: "workflow",
    label: "Workflow schedules",
    chip: "Workflow",
    glyph: "schedules",
  },
  {
    id: "template",
    label: "Templates",
    chip: "Template",
    glyph: "templates",
  },
  { id: "suite", label: "Suites", chip: "Suite", glyph: "suites" },
  { id: "trigger", label: "Triggers", chip: "Trigger", glyph: "repos" },
];

const KIND_IDS = PROCESS_KINDS.map((k) => k.id);

// CREATABLE_KINDS is what the New menu offers (§5.3). Workflow
// schedules and triggers are declared in a repository's `.grain/`, so
// there is no form for them here.
export const CREATABLE_KINDS = ["schedule", "template", "suite"];

// PROCESS_SEGMENTS is the plural path segment each kind opens under
// (§5.4) -- the old top-level paths, so each redirect is a prefix swap.
export const PROCESS_SEGMENTS = {
  schedule: "schedules",
  workflow: "schedules",
  template: "templates",
  suite: "suites",
  trigger: "triggers",
};

export function processKind(id) {
  return PROCESS_KINDS.find((k) => k.id === id) || null;
}

// isWorkflowSchedule is the workflow-schedule classification of §5.1:
// a schedule whose workflowReview is set, which pkg/ui/schedules.go sets
// only on schedules that run a workflow.
export function isWorkflowSchedule(schedule) {
  return !!schedule?.workflowReview;
}

// parseKinds reads `?kind=` back into the kinds it names, in the chips'
// own order, dropping anything this build has no chip for. An empty
// list is All.
export function parseKinds(raw) {
  const asked = new Set(
    (raw || "")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean),
  );
  return KIND_IDS.filter((k) => asked.has(k));
}

// kindsParam is parseKinds' inverse: "" for All, which is also what
// every kind at once means.
export function kindsParam(kinds) {
  const list = KIND_IDS.filter((k) => (kinds || []).includes(k));
  if (list.length === 0 || list.length === KIND_IDS.length) return "";
  return list.join(",");
}

const time = (iso) => (iso ? new Date(iso).getTime() : NaN);

const newest = (times) =>
  times
    .filter(Boolean)
    .reduce((best, t) => (!best || time(t) > time(best) ? t : best), null);
const soonest = (times) =>
  times
    .filter(Boolean)
    .reduce((best, t) => (!best || time(t) < time(best) ? t : best), null);

// buildProcessRows is every row the table lists, of every kind. `key`
// is unique across kinds ("schedule:sched-1"), since nothing promises a
// template's id and a suite's never coincide; it is what the custom
// order stores.
export function buildProcessRows({
  schedules = [],
  templates = [],
  suites = [],
  suiteRuns = [],
  triggers = [],
}) {
  const list = (v) => (Array.isArray(v) ? v : []);
  schedules = list(schedules);
  templates = list(templates);
  suites = list(suites);
  suiteRuns = list(suiteRuns);
  const rows = [];

  for (const s of schedules) {
    rows.push({
      key: `schedule:${s.id}`,
      kind: isWorkflowSchedule(s) ? "workflow" : "schedule",
      id: s.id,
      name: s.title || s.id,
      repo: s.repo || "",
      targetRepos: s.repo ? [s.repo] : [],
      lastRunAt: s.lastRunAt || null,
      lastWorkflowRun: s.lastWorkflowRun || null,
      nextRunAt: s.nextRunAt || null,
      nextLabel: null,
      enabled: !!s.enabled,
      system: !!s.system,
      declaredIn: s.declaredIn || null,
      item: s,
    });
  }

  // A template's or suite's last and next runs are derived (§5.1): from
  // the schedules that fire it, and for a suite from its own runs too.
  const firedBy = (field, id) => schedules.filter((s) => s[field] === id);
  const nextThrough = (field, id) =>
    soonest(
      firedBy(field, id)
        .filter((s) => s.enabled)
        .map((s) => s.nextRunAt),
    );

  for (const t of templates) {
    const next = nextThrough("templateId", t.id);
    rows.push({
      key: `template:${t.id}`,
      kind: "template",
      id: t.id,
      name: t.name || t.id,
      repo: t.repo ? (t.base ? `${t.repo} @ ${t.base}` : t.repo) : "any",
      targetRepos: t.repo ? [t.repo] : [],
      lastRunAt: newest(firedBy("templateId", t.id).map((s) => s.lastRunAt)),
      lastWorkflowRun: null,
      nextRunAt: next,
      nextLabel: next ? null : "on demand",
      enabled: null,
      system: !!t.system,
      declaredIn: t.declaredIn || null,
      item: t,
    });
  }

  const templateRepo = new Map(templates.map((t) => [t.id, t.repo || ""]));
  for (const s of suites) {
    const repos = [
      ...new Set(
        (s.items || [])
          .map((it) => templateRepo.get(it.templateId))
          .filter(Boolean),
      ),
    ];
    const next = nextThrough("suiteId", s.id);
    const runs = suiteRuns.filter((r) => r.suiteId === s.id);
    rows.push({
      key: `suite:${s.id}`,
      kind: "suite",
      id: s.id,
      name: s.name || s.id,
      repo: repos.length > 1 ? "several" : repos[0] || "any",
      targetRepos: repos,
      lastRunAt: newest(runs.map((r) => r.createdAt)),
      lastWorkflowRun: null,
      nextRunAt: next,
      nextLabel: next ? null : "on demand",
      enabled: null,
      system: false,
      declaredIn: s.declaredIn || null,
      item: s,
    });
  }

  for (const tr of list(triggers)) {
    const name = tr.path.replace(/^\.grain\/triggers\//, "");
    rows.push({
      key: `trigger:${tr.repo}:${tr.path}`,
      kind: "trigger",
      id: `${tr.repo}/${tr.path}`,
      name,
      repo: tr.repo,
      targetRepos: [tr.repo],
      lastRunAt: null,
      lastWorkflowRun: null,
      nextRunAt: null,
      nextLabel: "on change",
      enabled: null,
      system: false,
      declaredIn: { repo: tr.repo, path: tr.path },
      item: tr,
    });
  }
  return rows;
}

// declaringRepos is every repository whose declarations status is read
// for triggers: each one the loaded schedules, templates and suites say
// they were declared in, and each of the deployment's target
// repositories (`repos`, config.targetRepos), since a repository can
// declare nothing but a trigger. One that does not read its `.grain/`
// reports no status, and so no triggers.
export function declaringRepos({
  schedules = [],
  templates = [],
  suites = [],
  repos = [],
}) {
  const found = new Set(
    (Array.isArray(repos) ? repos : []).filter(
      (r) => typeof r === "string" && r,
    ),
  );
  for (const list of [schedules, templates, suites]) {
    for (const it of Array.isArray(list) ? list : []) {
      if (it?.declaredIn?.repo) found.add(it.declaredIn.repo);
    }
  }
  return [...found].sort();
}

// triggersOf reads one repository's triggers out of what GET
// /api/repos/{owner}/{name}/declarations answers: the repository's
// settings, with what grain made of its declarations under
// declarationsStatus (pkg/ui.RepoDeclarationsStatus.Triggers, each a
// file path).
export function triggersOf(repo, answer) {
  const paths = answer?.declarationsStatus?.triggers;
  return Array.isArray(paths) ? paths.map((path) => ({ repo, path })) : [];
}

// countByKind is each chip's count, over the rows the search leaves.
export function countByKind(rows) {
  const counts = Object.fromEntries(KIND_IDS.map((k) => [k, 0]));
  for (const r of rows) counts[r.kind] = (counts[r.kind] || 0) + 1;
  return counts;
}

function capitalize(s) {
  return s[0].toUpperCase() + s.slice(1);
}

// describeRecurrence renders a schedule's cadence the way a human reads
// it -- the row's own short form of what RecurrenceFields lets a human
// set. It moved here from the Schedules page with the rest of that page.
//
// A wall-clock cadence names the zone its clock reads in when that is
// not UTC (model.Recurrence.TimeZone). The row's "Next run" beside it is
// drawn in the viewer's own zone, so "Fridays at 08:30" next to "Next
// run 12:30 PM" read as a contradiction until the chip said "08:30
// America/New_York" (grain/task-1533). Every N hours (or minutes) has no clock, and
// an unset zone or UTC is the default the editor itself names.
export function describeRecurrence(r) {
  if (!r) return "";
  const zone =
    r.timeZone && r.timeZone !== "UTC" && r.timeZone !== "Etc/UTC"
      ? ` ${r.timeZone}`
      : "";
  switch (r.kind) {
    case "everyNHours":
      return `every ${r.everyNHours}h`;
    case "everyNMinutes":
      return `every ${r.everyNMinutes}m`;
    case "daily":
      return `daily at ${r.timeOfDay}${zone}`;
    case "weekly":
      return `${capitalize(r.weekday)}s at ${r.timeOfDay}${zone}`;
    case "monthly":
      return `monthly on day ${r.dayOfMonth} at ${r.timeOfDay}${zone}`;
    default:
      return r.kind;
  }
}
