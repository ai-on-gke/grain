// ciFilters.js is the CI pane's half of one filter vocabulary
// (grain/task-231). The other half is Go: pkg/ci/filter.go, which
// list_ci_runs and GET /api/ci/runs both narrow by, and which this file
// is deliberately a restatement of rather than a second design.
//
// # Why the two are spelled the same
//
// "Are the CI runs healthy?" is asked by an operator looking at this
// pane and by an agent calling list_ci_runs, about the same runs, and
// the two have to be able to check each other. If the pane called a
// failure "Red" and the tool called it "failure", or if one of them
// meant "the branch is main" and the other "the branch contains main",
// then "failed runs on main" would be two different sets with one name
// and a report from either side would be unverifiable from the other.
// So: the same five conditions, the same words for a conclusion, the
// same three orders, and the same reading of each -- repo whole, ref by
// any part of it, job by its exact name, conclusion as any of several.
//
// The same words, not the same runs. Since grain/task-172 an agent reads
// CI for the repository its own task works in (pkg/mcp's CIRepoScope),
// while this pane is an operator's view of their own deployment and is
// scoped to nothing, so the two can honestly count different totals for
// the same question. That is a difference in who is asking rather than
// in what was asked, which is exactly why the words have to stay one
// vocabulary: "failed runs on main" must still mean the same set of
// runs, or neither side can check the other's report of it.
//
// # Why it is the task list's machinery rather than a pane of its own
//
// The conditions here are taskFilters.js's conditions and the toolbar is
// NarrowingBar.jsx, over a set of attribute descriptors this file
// declares (RUN_ATTRIBUTES below) instead of a task's. That is the whole
// of the adaptation: the comparisons, the chips, the sort editor, the
// "is within the last N days" window and the way an unknown condition
// from a stale link narrows nothing rather than emptying the list all
// come from there and behave here exactly as they do on the task list.
// A second filter vocabulary in the same UI would be two things for a
// reader to learn and two places for a comparison to be written
// differently.
//
// # Where the choice is kept
//
// localStorage, like the board's columns and the list orders
// (board.js, listOrder.js): it is how one person wants to look at this
// deployment rather than a fact about it, and returning to the pane
// should not reset it. Not the URL -- unlike a task list, the CI pane is
// not a thing people send each other links into -- and not the daemon,
// which has nothing to do with one browser's preference.

import { useState } from "react";
import {
  NONE,
  TYPE_DATE,
  TYPE_ENUM,
  TYPE_NUMBER,
  TYPE_TAGS,
  TYPE_TEXT,
} from "./taskAttributes.js";
import {
  dayStart,
  matchesFilters,
  resolveConditions,
  sortTasks,
  NO_NARROWING,
} from "./taskFilters.js";

// GO_ZERO_TIME is Go's zero time.Time as it used to reach the browser.
// It reached it at all because `omitempty` does not omit a struct:
// ci.JobRun's StartedAt said omitempty and was serialized anyway, so a
// timestamp nobody had arrived as a real-looking instant in the year 1
// rather than as a missing field.
//
// grain does not send it any more -- those fields are *time.Time now,
// and a nil one is left out (ci.Run's own note on why) -- so a pane can
// go back to reading an absent field as an absent time. This stays
// because it costs one comparison to refuse an instant no CI run this
// UI will ever draw could honestly carry.
const GO_ZERO_TIME = Date.parse("0001-01-01T00:00:00Z");

// instantOf is a timestamp as a number of milliseconds, or null where
// there is not one there: absent, unparseable, or Go's zero time.
//
// Reading that zero as a real instant is what drew a CI job that errored
// waiting for a gcp-key grant -- finished, but never started -- as
// having taken 17756979h 39m, which is the span from the year 1 to now.
export function instantOf(value) {
  if (!value) return null;
  const ms = new Date(value).getTime();
  if (Number.isNaN(ms) || ms <= GO_ZERO_TIME) return null;
  return ms;
}

// hasStarted is whether a run, job or step has actually begun: a
// timestamp that is a time, rather than merely a field that is present.
export function hasStarted(thing) {
  return instantOf(thing?.startedAt) !== null;
}

// spanOf is how long something took, or how long it has been going --
// ci.Run.Duration's own rule, for a run, a job or a step alike. A thing
// that has not started yet has no span at all rather than a zero one.
export function spanOf(thing, now = Date.now()) {
  const started = instantOf(thing?.startedAt);
  if (started === null) return null;
  const finished = instantOf(thing.finishedAt);
  return (finished === null ? now : finished) - started;
}

// queuedOf is how long a job spent waiting for a machine before it got
// one, in milliseconds, or null where it waited for nothing worth
// reporting: ci.JobRun.Queued, which crosses the wire as Go writes a
// duration -- nanoseconds -- and is divided down here rather than at
// each of the places that draws it.
//
// A job's own duration starts when it gets a machine, so without this a
// job that queued for eighteen minutes and ran for four is drawn exactly
// like one that ran for twenty-two, and only one of those is a reason to
// go and look at this deployment's capacity.
export function queuedOf(thing) {
  const ns = thing?.queued;
  if (typeof ns !== "number" || !Number.isFinite(ns) || ns <= 0) return null;
  return ns / 1e6;
}

// queuedByRun is the same figure for a whole run: its jobs' waits added
// together (ci.Run.Queued). Job-time rather than wall clock, so it can
// be longer than the run itself -- eight jobs each held ten minutes is
// eighty minutes of queueing inside a run that took twenty -- and that
// is the point of it. No single job's wait says a deployment is short of
// sandboxes; the suite's total does.
export function queuedByRun(run) {
  let total = 0;
  for (const job of run?.jobs || []) total += queuedOf(job) ?? 0;
  return total > 0 ? total : null;
}

// RUNNING and PENDING are the two words for a run or a job that has no
// conclusion yet, and they are ci.StateRunning and ci.StatePending --
// the same two the tool takes.
//
// Conclusion's zero value cannot say this on its own: "on a machine and
// working" and "waiting for one" are one empty string to it and two
// different answers to "what is CI doing right now", which is a question
// about this deployment's capacity rather than about any commit.
export const RUNNING = "running";
export const PENDING = "pending";

// CONCLUSION_ORDER is the whole vocabulary, worst first -- ci.RunStates,
// in the same order, which is the order a menu offers it in and the
// order a reader scans it in.
export const CONCLUSION_ORDER = [
  "failure",
  "errored",
  "cancelled",
  "skipped",
  "success",
  RUNNING,
  PENDING,
];

// CONCLUSION_LABELS is how each of them is written on a chip.
//
// "Skipped" is its own thing rather than a shade of failure -- ci.
// Conclusion's own doc comment makes the point that "did not run because
// what it needed failed" and "failed" send a reader to different logs.
// "Errored" is that same point again and the sharpest case of it: a job
// grain could not run at all says nothing about the tree.
export const CONCLUSION_LABELS = {
  success: "Success",
  failure: "Failure",
  errored: "Errored",
  cancelled: "Cancelled",
  skipped: "Skipped",
  [RUNNING]: "Running",
  [PENDING]: "Pending",
};

// conclusionOf is which word describes a run -- ci.RunStateOf.
//
// A run enters the store when it is queued, so its own startedAt is not
// the question; what separates running from pending is whether any of
// its jobs has actually begun.
export function conclusionOf(run) {
  if (run?.conclusion) return run.conclusion;
  const started = (run?.jobs || []).some(
    (job) => job.conclusion || hasStarted(job),
  );
  return started ? RUNNING : PENDING;
}

// jobConclusionOf is the same for one job or one step, where startedAt
// *is* the question: a job acquires one when it gets a machine, so a job
// with no start is one queueing for a CI slot or one whose `needs` have
// not finished (ci.JobStateOf).
export function jobConclusionOf(job) {
  if (job?.conclusion) return job.conclusion;
  return hasStarted(job) ? RUNNING : PENDING;
}

// jobNames is every job a run has, by the name the pane shows and the
// tool matches on -- ci.MatchingJobs' own rule, which takes either the
// name or the id.
function jobNames(run) {
  const names = new Set();
  for (const job of run?.jobs || []) {
    const name = job.name || job.jobId;
    if (name) names.add(name);
  }
  return [...names];
}

// RUN_ATTRIBUTES is what a CI run can be asked about, in the order the
// "add a filter" menu offers it: the two anybody reaches for first, then
// the rest.
//
// It is exactly pkg/ci's RunFilter and no more. An attribute here that
// the tool has no argument for would be a question only one of the two
// readers can ask, which is how two vocabularies start.
export const RUN_ATTRIBUTES = [
  {
    id: "conclusion",
    label: "Conclusion",
    type: TYPE_ENUM,
    // Several at once is the point: "what is red right now" is failure
    // *and* errored, and they are two words on purpose.
    values: (run) => [conclusionOf(run)],
    order: CONCLUSION_ORDER,
    labelOf: (v) => CONCLUSION_LABELS[v] || v,
    key: (run) => CONCLUSION_ORDER.indexOf(conclusionOf(run)),
  },
  {
    id: "repo",
    label: "Repo",
    type: TYPE_ENUM,
    values: (run) => (run.repo ? [run.repo] : []),
    noneLabel: "No repo",
    glyphOf: (v) => (v === NONE ? null : "repos"),
  },
  {
    id: "ref",
    label: "Branch",
    // Text rather than a menu, and so "contains" rather than "is any
    // of", because that is what the tool's `ref` does: a branch name
    // here is long, is remembered by its tail, and "task-231" has to
    // find "grain/task-231" on both sides or the two are not the same
    // filter.
    type: TYPE_TEXT,
    values: (run) => (run.ref ? [run.ref] : []),
  },
  {
    id: "job",
    label: "Job",
    // Tags rather than one value: the attribute is "the jobs this run
    // has", and a run matches when any of them is one the condition
    // names -- which is the tool's `job` exactly.
    type: TYPE_TAGS,
    values: jobNames,
    noneLabel: "No jobs",
  },
  {
    id: "started",
    label: "Started",
    type: TYPE_DATE,
    // "Is within the last N days" is the time window, in the same words
    // the task list uses for one; the tool spells the same question
    // `since`.
    values: (run) => (run.startedAt ? [run.startedAt] : []),
    key: (run) => instantOf(run.startedAt) ?? undefined,
  },
  {
    id: "finished",
    label: "Finished",
    type: TYPE_DATE,
    values: (run) => (run.finishedAt ? [run.finishedAt] : []),
    key: (run) => instantOf(run.finishedAt) ?? undefined,
  },
  {
    id: "duration",
    label: "Duration (minutes)",
    type: TYPE_NUMBER,
    // The order that tells a forty-minute timeout from a two-minute
    // assertion failure -- the same red chip, and very different
    // problems. Minutes because that is the unit somebody types into
    // "is more than"; the sort key keeps the seconds so runs a minute
    // apart do not all compare equal.
    values: (run) => {
      const span = spanOf(run);
      return span === null ? [] : [Math.round(span / 60000)];
    },
    key: (run) => spanOf(run) ?? undefined,
  },
];

// DEFAULT_NARROWING is what the pane opens on before anybody has
// narrowed it: nothing filtered, newest first -- which is the order the
// daemon already hands runs over in, and list_ci_runs' own default.
export const DEFAULT_NARROWING = Object.freeze({
  search: "",
  sort: Object.freeze([Object.freeze({ attr: "started", dir: "desc" })]),
  filters: Object.freeze([]),
});

// searchText is what the pane's search box looks through: the things
// somebody types a fragment of when they are looking for one run --
// where it ran, on what, at which commit, and which jobs were in it.
function searchText(run) {
  return [run.id, run.repo, run.ref, run.commit, run.event, ...jobNames(run)]
    .filter(Boolean)
    .join(" ");
}

// narrowRuns is the pane's whole question as one call: put the runs in
// the order asked for, then keep the ones every condition and the search
// box accept.
//
// Not memoised, unlike narrowTasks: that cache exists because a
// dashboard asks one list the same question once per panel per poll, and
// this pane asks one question of at most a few hundred runs every five
// seconds.
export function narrowRuns(runs, narrowing, config) {
  const { search = "", sort, filters } = narrowing || NO_NARROWING;
  const ordered = sortTasks(runs || [], sort, RUN_ATTRIBUTES, config);
  const conditions = resolveConditions(filters, RUN_ATTRIBUTES);
  const q = search.trim().toLowerCase();
  return ordered.filter((run) =>
    matchesFilters(run, conditions, q, config, searchText),
  );
}

// jobsNamed is the jobs of one run a "Job" condition names, so that a
// list narrowed to one job can put that job's own result and duration on
// the row instead of the run's -- the same thing list_ci_runs does with
// its `job` argument, and for the same reason: reading down a column of
// one job across twenty commits is the question, and the run's other
// eleven jobs are not part of it.
//
// It takes the names off the conditions rather than being told them, so
// there is one answer to "is this list about a job" and the row and the
// filter cannot disagree about it.
export function jobsNamed(run, filters) {
  const wanted = new Set();
  for (const cond of filters || []) {
    if (cond.attr !== "job" || cond.op !== "any") continue;
    for (const v of cond.values || []) if (v !== NONE) wanted.add(v);
  }
  if (wanted.size === 0) return null;
  const found = (run.jobs || []).filter((job) =>
    wanted.has(job.name || job.jobId),
  );
  return found.length > 0 ? found : null;
}

// RUN_POLL_LIMIT is how many runs the pane asks for every five seconds,
// and WINDOWED_RUN_POLL_LIMIT how many it asks for when the reader has
// named a window to look back over.
//
// Two numbers because the poll and the question are not the same thing
// (grain/task-289). The pane's ordinary state is an unnarrowed table
// refreshing itself, and what it needs is the newest screenful and a
// little either side -- a deployment keeping five thousand runs has no
// business sending all of them to draw forty rows. A reader who asks
// "started within the last 14 days" has asked a different question, and
// the daemon can answer that one over the whole history it keeps, so the
// poll may cost more: it is a gesture rather than a heartbeat.
//
// Both are bounded again at the far end (ci.MaxFoundRuns), so neither is
// load-bearing for the daemon's safety -- they are what this pane thinks
// is worth fetching.
export const RUN_POLL_LIMIT = 200;
export const WINDOWED_RUN_POLL_LIMIT = 2000;

// startedSince is the oldest run a narrowing could possibly want, as
// milliseconds, or null where it has not said.
//
// Only the "Started" conditions count, and only the two that are a lower
// bound on it -- "is within the last N days" and "is after a date". A
// window on *finish* is deliberately not one of them: a run that started
// before the window and finished inside it matches it, so asking the
// daemon for runs started since that date would drop exactly the long
// runs somebody looking at finish times is after.
//
// Several such conditions are ANDed by the filter, so the latest of
// their lower bounds is the one that binds -- which is the same answer
// the browser's own comparison would give, and never a smaller one: a
// poll that fetched less than the reader can see would be a table that
// lies about what matched.
export function startedSince(filters, zone, now = Date.now()) {
  let since = null;
  const bind = (at) => {
    if (at === null || !Number.isFinite(at)) return;
    since = since === null ? at : Math.max(since, at);
  };
  for (const cond of filters || []) {
    if (cond?.attr !== "started") continue;
    const value = (cond.values || [])[0];
    if (cond.op === "within") {
      const days = Number(value);
      if (Number.isFinite(days) && days > 0)
        bind(now - days * 24 * 60 * 60 * 1000);
    } else if (cond.op === "after") {
      bind(dayStart(value, zone));
    }
  }
  return since;
}

// runsQuery is what the pane's poll asks GET /api/ci/runs for: a limit
// always, and the window the narrowing names when it names one.
//
// The rest of the narrowing stays in the browser (repoNarrowedTo below
// says why), so this is not "the filter, sent to the daemon" -- it is
// how much of the deployment's history the pane needs in front of it to
// answer its own question honestly.
export function runsQuery(narrowing, config, now = Date.now()) {
  const since = startedSince(narrowing?.filters, config?.timeZone, now);
  const query = new URLSearchParams();
  query.set(
    "limit",
    String(since === null ? RUN_POLL_LIMIT : WINDOWED_RUN_POLL_LIMIT),
  );
  if (since !== null) query.set("since", new Date(since).toISOString());
  return `?${query.toString()}`;
}

// repoNarrowedTo is the one repository this narrowing is about, or ""
// for a narrowing that is about several or about none.
//
// The pane answers its whole question in the browser, over the runs it
// already polls for. It used to send the repo to the daemon as ?repo=
// instead -- a smaller answer over the wire every five seconds -- and
// that is now what GET /api/ci/runs does for anybody who asks it that
// way (the route understands the entire vocabulary, ui.ciRunFilterFrom),
// but not what this pane does. Two reasons, and the second is the real
// one: five conditions over one table, four of them answered here and
// one of them by the daemon, is two behaviours for one toolbar; and a
// menu of repositories built from an answer the repository filter has
// already emptied offers exactly one repository -- the one that is
// chosen -- so there is no way to pick a different one. What the
// narrowing costs instead is the page the pane already polls for, which
// runsQuery above bounds: RUN_POLL_LIMIT runs, or the window the reader
// asked to look back over.
//
// It is still worth knowing which repository a pane is about, for the
// lines that say so; only the unambiguous case counts -- one condition,
// "is any of", one repository.
export function repoNarrowedTo(filters) {
  const repos = (filters || []).filter((c) => c.attr === "repo");
  if (repos.length !== 1) return "";
  const [only] = repos;
  if (only.op !== "any" || (only.values || []).length !== 1) return "";
  return only.values[0] === NONE ? "" : only.values[0];
}

// withRepo is `narrowing` asking about one repository and nothing else
// about repositories -- what a repo's own page hands the pane when it
// sends its reader here for the rest of the runs (RepoRuns.jsx).
export function withRepo(narrowing, repo) {
  const filters = (narrowing.filters || []).filter((c) => c.attr !== "repo");
  return {
    ...narrowing,
    filters: [...filters, { attr: "repo", op: "any", values: [repo] }],
  };
}

export const NARROWING_STORAGE_KEY = "grain.ci.narrowing";

// loadNarrowing is what this browser last asked of the CI pane.
//
// Anything unreadable -- a half-written value, a shape from some other
// build, localStorage denied outright -- is "nobody has narrowed this
// yet" rather than an error to show somebody, exactly as listOrder.js
// treats a bad stored order. The pane still renders; the next gesture
// overwrites the bad value. A condition naming an attribute this build
// does not have is dropped when it is resolved rather than here
// (taskFilters.js's resolveConditions), so a stored question from a
// newer build opens the pane instead of emptying it.
export function loadNarrowing() {
  let raw = null;
  try {
    raw = localStorage.getItem(NARROWING_STORAGE_KEY);
  } catch {
    return DEFAULT_NARROWING;
  }
  if (!raw) return DEFAULT_NARROWING;
  try {
    const stored = JSON.parse(raw);
    if (!stored || typeof stored !== "object") return DEFAULT_NARROWING;
    return {
      search: typeof stored.search === "string" ? stored.search : "",
      sort: Array.isArray(stored.sort) ? stored.sort : [],
      filters: Array.isArray(stored.filters) ? stored.filters : [],
    };
  } catch {
    return DEFAULT_NARROWING;
  }
}

export function saveNarrowing(narrowing) {
  try {
    localStorage.setItem(NARROWING_STORAGE_KEY, JSON.stringify(narrowing));
  } catch {
    // A browser that won't store this still narrows the pane; it just
    // forgets on reload. Not worth an error banner over a preference.
  }
}

// useCINarrowing is the pane's narrowing as it holds it: read once when
// the pane mounts, kept in state from then on, written back on every
// change. `initialRepo` is the repository the pane was opened *about*
// (a repo page sending its reader here), which replaces whatever repo
// condition was stored -- seeded rather than enforced, so the reader can
// unpick it like any other condition.
export function useCINarrowing(initialRepo) {
  const [narrowing, setNarrowing] = useState(() => {
    const stored = loadNarrowing();
    return initialRepo ? withRepo(stored, initialRepo) : stored;
  });
  const narrow = (patch) => {
    const next = { ...narrowing, ...patch };
    setNarrowing(next);
    saveNarrowing(next);
  };
  return [narrowing, narrow];
}
