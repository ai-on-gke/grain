import { afterEach, describe, expect, it } from "vitest";
import {
  CONCLUSION_ORDER,
  DEFAULT_NARROWING,
  NARROWING_STORAGE_KEY,
  PENDING,
  RUNNING,
  RUN_ATTRIBUTES,
  conclusionOf,
  jobsNamed,
  loadNarrowing,
  narrowRuns,
  queuedByRun,
  queuedOf,
  repoNarrowedTo,
  runsQuery,
  saveNarrowing,
  startedSince,
  withRepo,
  RUN_POLL_LIMIT,
  WINDOWED_RUN_POLL_LIMIT,
} from "./ciFilters.js";

// The pane and list_ci_runs narrow the same runs by the same words
// (ciFilters.js's own note, pkg/ci/filter.go). These are this end of
// that vocabulary: that the conditions compose, that a run with no
// conclusion is still something a filter can ask for, and that an order
// does not reshuffle runs it has nothing to tell apart.

// Relative to the real clock, because a run that has not finished is
// measured against now -- which is the point of ordering by duration
// while something is still going.
const NOW = Date.now();
const ago = (minutes) => new Date(NOW - minutes * 60000).toISOString();

const runs = [
  {
    id: "red-main",
    repo: "acme/widgets",
    ref: "main",
    conclusion: "failure",
    startedAt: ago(30),
    finishedAt: ago(20),
    jobs: [
      {
        jobId: "go",
        name: "go tests",
        conclusion: "success",
        startedAt: ago(30),
        finishedAt: ago(28),
      },
      {
        jobId: "e2e",
        name: "ui e2e",
        conclusion: "failure",
        startedAt: ago(30),
        finishedAt: ago(20),
      },
    ],
  },
  {
    id: "green-branch",
    repo: "acme/widgets",
    ref: "grain/task-231",
    conclusion: "success",
    startedAt: ago(90),
    finishedAt: ago(85),
    jobs: [{ jobId: "e2e", name: "ui e2e", conclusion: "success" }],
  },
  {
    id: "running",
    repo: "acme/gadgets",
    ref: "main",
    startedAt: ago(8),
    jobs: [{ jobId: "e2e", name: "ui e2e", startedAt: ago(7) }],
  },
  {
    id: "queued",
    repo: "acme/gadgets",
    ref: "main",
    startedAt: ago(1),
    jobs: [{ jobId: "e2e", name: "ui e2e" }],
  },
];

const narrowing = (filters, sort = []) => ({ search: "", sort, filters });
const ids = (list) => list.map((run) => run.id);

describe("narrowRuns", () => {
  it("has every condition the tool has, and no more", () => {
    // An attribute here that list_ci_runs has no argument for is a
    // question only one of the two readers can ask, which is how two
    // vocabularies start (pkg/ci.RunFilter).
    expect(RUN_ATTRIBUTES.map((a) => a.id)).toEqual([
      "conclusion",
      "repo",
      "ref",
      "job",
      "started",
      "finished",
      "duration",
    ]);
  });

  it("narrows by one condition at a time, the way the tool does", () => {
    const one = (filters) => ids(narrowRuns(runs, narrowing(filters), {}));
    expect(
      one([{ attr: "conclusion", op: "any", values: ["failure", "errored"] }]),
    ).toEqual(["red-main"]);
    expect(
      one([{ attr: "repo", op: "any", values: ["acme/gadgets"] }]),
    ).toEqual(["running", "queued"]);
    // A branch by any part of its name, which is what the tool's `ref`
    // does -- "task-231" has to find "grain/task-231" on both sides.
    expect(one([{ attr: "ref", op: "has", values: ["task-231"] }])).toEqual([
      "green-branch",
    ]);
    expect(one([{ attr: "job", op: "any", values: ["go tests"] }])).toEqual([
      "red-main",
    ]);
  });

  it("composes several conditions, each of which alone would answer with more", () => {
    const filters = [
      { attr: "repo", op: "any", values: ["acme/widgets"] },
      { attr: "conclusion", op: "any", values: ["failure"] },
      { attr: "job", op: "any", values: ["ui e2e"] },
    ];
    expect(ids(narrowRuns(runs, narrowing(filters), {}))).toEqual(["red-main"]);
  });

  it("answers with nothing rather than everything when nothing matches", () => {
    const filters = [
      { attr: "repo", op: "any", values: ["acme/gadgets"] },
      { attr: "conclusion", op: "any", values: ["success"] },
    ];
    expect(narrowRuns(runs, narrowing(filters), {})).toEqual([]);
  });

  // A run that has not finished has no conclusion, and the two ways of
  // not having one are a question about this deployment rather than
  // about any commit: on a machine, or waiting for one. Neither may fall
  // out of a list for want of a conclusion to file it under.
  it("keeps an unfinished run under running or pending, with no conclusion on it", () => {
    const live = narrowRuns(
      runs,
      narrowing([{ attr: "conclusion", op: "any", values: [RUNNING] }]),
      {},
    );
    expect(ids(live)).toEqual(["running"]);
    expect(live[0].conclusion).toBeUndefined();
    expect(
      ids(
        narrowRuns(
          runs,
          narrowing([{ attr: "conclusion", op: "any", values: [PENDING] }]),
          {},
        ),
      ),
    ).toEqual(["queued"]);
    expect(conclusionOf(runs[2])).toBe(RUNNING);
    expect(conclusionOf(runs[3])).toBe(PENDING);
    expect(CONCLUSION_ORDER).toContain(RUNNING);
  });

  it("searches the things somebody types a fragment of", () => {
    const find = (search) =>
      ids(narrowRuns(runs, { search, sort: [], filters: [] }, {}));
    expect(find("task-231")).toEqual(["green-branch"]);
    expect(find("gadgets")).toEqual(["running", "queued"]);
    expect(find("go tests")).toEqual(["red-main"]);
    expect(find("nothing here")).toEqual([]);
  });

  it("orders by started, finished and duration", () => {
    const order = (attr, dir) =>
      ids(narrowRuns(runs, narrowing([], [{ attr, dir }]), {}));
    expect(order("started", "desc")).toEqual([
      "queued",
      "running",
      "red-main",
      "green-branch",
    ]);
    // Longest first is what tells a forty-minute timeout from a
    // two-minute assertion failure; a run still going is measured
    // against now, and one that never started has no duration to order
    // by at all and sorts last.
    expect(order("duration", "desc").slice(0, 2)).toEqual([
      "red-main",
      "running",
    ]);
    expect(order("finished", "desc").slice(0, 2)).toEqual([
      "red-main",
      "green-branch",
    ]);
  });

  // Runs that compare equal keep the order they arrived in, whichever
  // way the sort points -- a pane polling every five seconds must not
  // reshuffle an unchanged list under its reader.
  it("is stable across equal keys", () => {
    const level = [
      { id: "a", startedAt: ago(60), finishedAt: ago(50) },
      { id: "b", startedAt: ago(60), finishedAt: ago(50) },
      { id: "c", startedAt: ago(60), finishedAt: ago(50) },
    ];
    for (const dir of ["asc", "desc"]) {
      for (const attr of ["started", "finished", "duration"]) {
        expect(
          ids(narrowRuns(level, narrowing([], [{ attr, dir }]), {})),
        ).toEqual(["a", "b", "c"]);
      }
    }
  });
});

describe("jobsNamed", () => {
  it("is the jobs a Job condition names, and nothing for a list about no job", () => {
    const named = jobsNamed(runs[0], [
      { attr: "job", op: "any", values: ["ui e2e"] },
    ]);
    expect(named.map((job) => job.name)).toEqual(["ui e2e"]);
    expect(jobsNamed(runs[0], [])).toBeNull();
    // A run that does not have the job at all is a row with nothing
    // special to say, rather than a row saying "no such job".
    expect(
      jobsNamed(runs[2], [{ attr: "job", op: "any", values: ["go tests"] }]),
    ).toBeNull();
  });
});

describe("the narrowing this browser keeps", () => {
  afterEach(() => localStorage.clear());

  it("comes back as it was left", () => {
    const question = narrowing(
      [{ attr: "conclusion", op: "any", values: ["failure"] }],
      [{ attr: "duration", dir: "desc" }],
    );
    saveNarrowing(question);
    expect(loadNarrowing()).toEqual(question);
  });

  it("opens on newest first when there is nothing stored", () => {
    expect(loadNarrowing()).toEqual(DEFAULT_NARROWING);
  });

  // A half-written value, or a shape from some other build, is "nobody
  // has narrowed this yet" rather than an error to show somebody.
  it("treats an unreadable stored question as none at all", () => {
    localStorage.setItem(NARROWING_STORAGE_KEY, "{not json");
    expect(loadNarrowing()).toEqual(DEFAULT_NARROWING);
    localStorage.setItem(NARROWING_STORAGE_KEY, '"a string"');
    expect(loadNarrowing()).toEqual(DEFAULT_NARROWING);
  });
});

describe("repoNarrowedTo", () => {
  it("is the one repository a narrowing is unambiguously about", () => {
    expect(repoNarrowedTo([{ attr: "repo", op: "any", values: ["a/b"] }])).toBe(
      "a/b",
    );
    expect(repoNarrowedTo([])).toBe("");
    expect(
      repoNarrowedTo([{ attr: "repo", op: "any", values: ["a/b", "c/d"] }]),
    ).toBe("");
    expect(
      repoNarrowedTo([{ attr: "repo", op: "none", values: ["a/b"] }]),
    ).toBe("");
  });

  it("replaces whatever repo condition was there when a pane is sent about one", () => {
    const sent = withRepo(
      narrowing([{ attr: "repo", op: "any", values: ["old/one"] }]),
      "new/one",
    );
    expect(repoNarrowedTo(sent.filters)).toBe("new/one");
    expect(sent.filters).toHaveLength(1);
  });
});

// The pane's poll is a page of the deployment's history rather than the
// whole of it, and the reader's own window is what widens it
// (grain/task-289).
describe("runsQuery", () => {
  const at = (iso) => Date.parse(iso);

  it("asks for a bounded page when nobody has named a window", () => {
    expect(runsQuery({ filters: [] })).toBe(`?limit=${RUN_POLL_LIMIT}`);
    expect(runsQuery(null)).toBe(`?limit=${RUN_POLL_LIMIT}`);
    // A condition that is not about when a run started leaves the poll
    // alone: the pane answers those in the browser.
    expect(
      runsQuery({ filters: [{ attr: "repo", op: "any", values: ["a/b"] }] }),
    ).toBe(`?limit=${RUN_POLL_LIMIT}`);
  });

  it("reaches back as far as the window the reader asked for", () => {
    const now = at("2026-09-18T12:00:00Z");
    const query = runsQuery(
      { filters: [{ attr: "started", op: "within", values: ["14"] }] },
      { timeZone: "UTC" },
      now,
    );
    expect(query).toContain(`limit=${WINDOWED_RUN_POLL_LIMIT}`);
    expect(query).toContain(
      `since=${encodeURIComponent("2026-09-04T12:00:00.000Z")}`,
    );
  });

  it("reads a date the way the filter beside it does", () => {
    expect(
      startedSince(
        [{ attr: "started", op: "after", values: ["2026-09-01"] }],
        "UTC",
      ),
    ).toBe(at("2026-09-01T00:00:00Z"));
  });

  // Two windows are one window: the filter ANDs them, so the poll must
  // not fetch less than the reader can see -- the latest lower bound is
  // the one that binds.
  it("takes the narrowest of several windows", () => {
    const now = at("2026-09-18T12:00:00Z");
    expect(
      startedSince(
        [
          { attr: "started", op: "within", values: ["14"] },
          { attr: "started", op: "within", values: ["2"] },
        ],
        "UTC",
        now,
      ),
    ).toBe(at("2026-09-16T12:00:00Z"));
  });

  // A window on when a run *finished* is not a window on when it
  // started: a long run can start before it and finish inside it, and
  // dropping those at the daemon would hide exactly the runs somebody
  // reading finish times is looking for.
  it("leaves a finish window to the browser", () => {
    expect(
      startedSince([{ attr: "finished", op: "within", values: ["14"] }], "UTC"),
    ).toBe(null);
    // So are the conditions that are not a lower bound at all, and the
    // half-typed ones.
    expect(
      startedSince(
        [{ attr: "started", op: "before", values: ["2026-09-01"] }],
        "UTC",
      ),
    ).toBe(null);
    expect(
      startedSince(
        [{ attr: "started", op: "after", values: ["2026-09"] }],
        "UTC",
      ),
    ).toBe(null);
    expect(
      startedSince([{ attr: "started", op: "within", values: [""] }], "UTC"),
    ).toBe(null);
  });
});

// grain/task-326: a job's queueing crosses the wire as Go writes a
// duration -- nanoseconds -- and is read in milliseconds, which is what
// everything that draws a span here takes.
describe("queueing", () => {
  it("reads a job's wait in the units a span is drawn in", () => {
    expect(queuedOf({ queued: 18 * 60 * 1e9 })).toBe(18 * 60 * 1000);
    // A job that waited for nothing has no wait to draw, rather than a
    // zero somebody has to read past.
    expect(queuedOf({ queued: 0 })).toBe(null);
    expect(queuedOf({})).toBe(null);
    expect(queuedOf(undefined)).toBe(null);
    expect(queuedOf({ queued: "18m" })).toBe(null);
  });

  // Job-time rather than wall clock, which is the whole point of the
  // figure: it can be longer than the run it is on, and a run whose
  // queueing dwarfs its duration is a deployment short of sandboxes.
  it("adds a run's jobs' waits together", () => {
    expect(
      queuedByRun({
        jobs: [
          { queued: 18 * 60 * 1e9 },
          { queued: 36 * 60 * 1e9 },
          { cached: true },
          {},
        ],
      }),
    ).toBe(54 * 60 * 1000);
    expect(queuedByRun({ jobs: [] })).toBe(null);
    expect(queuedByRun(undefined)).toBe(null);
  });
});
