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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import ReportBoard from "./ReportBoard.jsx";
import {
  backlogRows,
  formatBytes,
  formatSeconds,
  isThinPercentile,
  percent,
  sortedOutcomes,
} from "./metricsFormat.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// stage builds one entry of the report's latency list. The API sends
// every stage every time -- a stage nothing was measured for comes back
// as n: 0 rather than as an absent entry -- so the default here is a
// stage that measured nothing, and each test fills in only the numbers
// it is about.
function stage(name, over = {}) {
  return {
    stage: name,
    label: name,
    description: `what ${name} measures`,
    n: 0,
    minSeconds: 0,
    p50Seconds: 0,
    p90Seconds: 0,
    p99Seconds: 0,
    maxSeconds: 0,
    meanSeconds: 0,
    ...over,
  };
}

// report is roughly the worked example in README's "Measuring throughput
// and latency", so the numbers here are ones a reader can check against
// the `grain metrics` output printed there.
function report(over = {}) {
  return {
    since: "2026-08-27T00:00:00Z",
    until: "2026-09-03T00:00:00Z",
    windowSeconds: 604800,
    throughput: {
      tasksFiled: 42,
      tasksCompleted: 38,
      tasksClosed: 3,
      runsStarted: 61,
      runsFinished: 60,
      filedPerDay: 6,
      completedPerDay: 5.4,
      runsFinishedPerDay: 8.6,
      buckets: [
        {
          since: "2026-08-27T00:00:00Z",
          until: "2026-08-29T08:00:00Z",
          filed: 10,
          completed: 8,
          runsFinished: 15,
        },
        {
          since: "2026-08-29T08:00:00Z",
          until: "2026-08-31T16:00:00Z",
          filed: 14,
          completed: 12,
          runsFinished: 20,
        },
        {
          since: "2026-08-31T16:00:00Z",
          until: "2026-09-03T00:00:00Z",
          filed: 18,
          completed: 18,
          runsFinished: 25,
        },
      ],
    },
    latency: [
      stage("filed -> approved", {
        n: 12,
        p50Seconds: 252,
        p90Seconds: 3720,
        p99Seconds: 11400,
        maxSeconds: 11400,
      }),
      stage("approved -> attempt started", {
        n: 38,
        p50Seconds: 31,
        p90Seconds: 130,
        p99Seconds: 540,
        maxSeconds: 540,
      }),
      stage("one whole attempt", {
        n: 60,
        p50Seconds: 768,
        p90Seconds: 1570,
        p99Seconds: 3120,
        maxSeconds: 3120,
      }),
      stage("attempt finished -> next attempt started", {
        n: 4,
        p50Seconds: 120,
        p90Seconds: 240,
        p99Seconds: 360,
        maxSeconds: 360,
      }),
      stage("filed -> completed", {
        n: 38,
        p50Seconds: 720,
        p90Seconds: 3000,
        p99Seconds: 11400,
        maxSeconds: 11400,
      }),
    ],
    runs: {
      outcomes: { succeeded: 45, failed: 14, cancelled: 1 },
      attemptsPerCompletion: 1.58,
      meanConcurrent: 0.42,
      maxConcurrent: 3,
      utilization: 0.14,
      live: 2,
    },
    backlog: {
      byState: {
        queued: 4,
        running: 2,
        proposed: 1,
        awaiting_reply: 1,
        awaiting_submit: 2,
      },
      queued: 4,
      oldestQueuedSeconds: 8040,
      oldestQueuedTaskId: "51",
    },
    ...over,
  };
}

// tools, checks and pullRequests are the three sections a deployment only
// has once its runs have recorded a census: report() above deliberately
// carries none of them, so that every test written before they existed
// still describes a report the API really sends -- one from a store whose
// runs all predate them.
function withCensus(over = {}) {
  return report({
    tools: {
      runs: 2,
      calls: 110,
      errored: 15,
      erroredShare: 15 / 110,
      cut: 4,
      cutShare: 4 / 110,
      callsPerRun: {
        n: 2,
        min: 50,
        p50: 50,
        p90: 60,
        p99: 60,
        max: 60,
        mean: 55,
        total: 110,
      },
      byTool: [
        {
          name: "run_command",
          runs: 2,
          calls: 100,
          errored: 10,
          errorRate: 0.1,
          timedOut: 2,
          timeoutRate: 0.02,
          resultBytes: {
            n: 100,
            meanBytes: 2048,
            maxBytes: 70000,
            p50AtMostBytes: 4095,
            p95AtMostBytes: 65535,
            p99AtMostBytes: 131071,
          },
          cut: {
            calls: 4,
            rate: 0.04,
            n: 4,
            droppedP50AtMostBytes: 32767,
            droppedP95AtMostBytes: 65535,
          },
        },
        {
          name: "edit_file",
          runs: 2,
          calls: 10,
          errored: 5,
          errorRate: 0.5,
          timedOut: 0,
          timeoutRate: 0,
          resultBytes: {
            n: 10,
            meanBytes: 40,
            maxBytes: 80,
            p50AtMostBytes: 63,
            p95AtMostBytes: 127,
            p99AtMostBytes: 127,
          },
          cut: {
            calls: 0,
            rate: 0,
            n: 0,
            droppedP50AtMostBytes: 0,
            droppedP95AtMostBytes: 0,
          },
        },
      ],
    },
    checks: {
      waits: 4,
      runs: 2,
      verdicts: { timed_out: 2, failed: 1, passed: 1 },
      blocked: {
        n: 4,
        minSeconds: 240,
        p50Seconds: 540,
        p90Seconds: 900,
        p99Seconds: 900,
        maxSeconds: 900,
        meanSeconds: 660,
      },
      pushesToGreen: {
        n: 1,
        min: 2,
        p50: 2,
        p90: 2,
        p99: 2,
        max: 2,
        mean: 2,
        total: 2,
      },
      greenRuns: 1,
      repairs: {
        runs: 2,
        passes: 3,
        settled: 2,
        settleRate: 2 / 3,
        timeToSettle: {
          n: 3,
          minSeconds: 1800,
          p50Seconds: 3600,
          p90Seconds: 7200,
          p99Seconds: 7200,
          maxSeconds: 7200,
          meanSeconds: 4200,
        },
        conflictPasses: 1,
        conflictTimeToSettle: {
          n: 1,
          minSeconds: 7200,
          p50Seconds: 7200,
          p90Seconds: 7200,
          p99Seconds: 7200,
          maxSeconds: 7200,
          meanSeconds: 7200,
        },
        testRepairPasses: 2,
        testRepairTimeToSettle: {
          n: 2,
          minSeconds: 1800,
          p50Seconds: 2700,
          p90Seconds: 3600,
          p99Seconds: 3600,
          maxSeconds: 3600,
          meanSeconds: 2700,
        },
        toolCalls: 12,
        toolCallsPerRun: { n: 2, mean: 6, max: 8 },
        erroredToolCalls: 2,
        erroredToolCallRate: 2 / 12,
        transcriptBytes: { n: 2, mean: 13500, max: 15000 },
        topTools: [
          { name: "run_command", calls: 7, errored: 1 },
          { name: "edit_file", calls: 5, errored: 1 },
        ],
      },
    },
    merges: {
      landed: 4,
      landedPerHour: 4 / 168,
      landedPerDay: 4 / 7,
      filedToLanded: {
        n: 4,
        minSeconds: 3600,
        p50Seconds: 7200,
        p90Seconds: 14400,
        p99Seconds: 14400,
        maxSeconds: 14400,
        meanSeconds: 8100,
      },
      completedToLanded: {
        n: 4,
        minSeconds: 1800,
        p50Seconds: 3600,
        p90Seconds: 7200,
        p99Seconds: 7200,
        maxSeconds: 7200,
        meanSeconds: 3900,
      },
      openedToLanded: {
        n: 4,
        minSeconds: 1800,
        p50Seconds: 3600,
        p90Seconds: 7200,
        p99Seconds: 7200,
        maxSeconds: 7200,
        meanSeconds: 3900,
      },
      batches: {
        batches: 3,
        changes: 4,
        fresh: 2,
        firstTry: 2,
        firstTryRate: 1.0,
        passed: 2,
        passRate: 2 / 3,
        split: 1,
        splitRate: 1 / 3,
        culprits: 1,
        abandoned: 0,
        meanSize: 2.0,
        biggest: 3,
        limit: 3,
        atLimit: 0,
        suites: 3,
        serialSuites: 4,
        saved: 1,
        sizes: [{ size: 2, batches: 2, passed: 2, share: 1.0 }],
      },
      repairs: {
        runs: 2,
        passes: 3,
        settled: 2,
        settleRate: 2 / 3,
        timeToSettle: {
          n: 3,
          minSeconds: 1800,
          p50Seconds: 3600,
          p90Seconds: 7200,
          p99Seconds: 7200,
          maxSeconds: 7200,
          meanSeconds: 4200,
        },
        conflictPasses: 1,
        conflictTimeToSettle: {
          n: 1,
          minSeconds: 7200,
          p50Seconds: 7200,
          p90Seconds: 7200,
          p99Seconds: 7200,
          maxSeconds: 7200,
          meanSeconds: 7200,
        },
        testRepairPasses: 2,
        testRepairTimeToSettle: {
          n: 2,
          minSeconds: 1800,
          p50Seconds: 2700,
          p90Seconds: 3600,
          p99Seconds: 3600,
          maxSeconds: 3600,
          meanSeconds: 2700,
        },
        toolCalls: 12,
        toolCallsPerRun: { n: 2, mean: 6, max: 8 },
        erroredToolCalls: 2,
        erroredToolCallRate: 2 / 12,
        transcriptBytes: { n: 2, mean: 13500, max: 15000 },
        topTools: [
          { name: "run_command", calls: 7, errored: 1 },
          { name: "edit_file", calls: 5, errored: 1 },
        ],
      },
      buckets: [],
    },
    pullRequests: {
      runs: 8,
      opened: 3,
      calls: 7,
      adoptionRate: 3 / 8,
      withTool: { runs: 3, fixTasks: 0, rate: 0 },
      withoutTool: { runs: 5, fixTasks: 2, rate: 0.4 },
    },
    ciQueueing: {
      jobs: 12,
      waited: 5,
      neverRan: 1,
      cached: 3,
      queuedSeconds: 3600,
      busySeconds: 8400,
      queuedShare: 0.3,
      wait: {
        n: 5,
        minSeconds: 60,
        p50Seconds: 420,
        p90Seconds: 2400,
        p99Seconds: 2400,
        maxSeconds: 2400,
        meanSeconds: 720,
      },
      unattributedSeconds: 300,
      byCause: [
        {
          cause: "hole",
          jobs: 2,
          queuedSeconds: 2400,
          share: 2400 / 3600,
          longestSeconds: 2400,
          reason: "capacity is being held for a larger job",
        },
        {
          cause: "slot",
          jobs: 2,
          queuedSeconds: 900,
          share: 900 / 3600,
          longestSeconds: 600,
          reason: "for one of the 3 sandboxes this deployment keeps for CI",
        },
        {
          cause: "unattributed",
          jobs: 1,
          queuedSeconds: 300,
          share: 300 / 3600,
          longestSeconds: 300,
          reason: "",
        },
      ],
      byRepo: [
        {
          repo: "acme/widgets",
          jobs: 8,
          queuedSeconds: 3300,
          busySeconds: 4200,
          queuedShare: 0.44,
          wait: {
            n: 4,
            minSeconds: 60,
            p50Seconds: 600,
            p90Seconds: 2400,
            p99Seconds: 2400,
            maxSeconds: 2400,
            meanSeconds: 825,
          },
          topCause: "hole",
        },
      ],
      byJob: [
        {
          repo: "acme/widgets",
          job: "image",
          jobs: 2,
          queuedSeconds: 2400,
          busySeconds: 1200,
          queuedShare: 2400 / 3600,
          wait: {
            n: 2,
            minSeconds: 1200,
            p50Seconds: 1200,
            p90Seconds: 2400,
            p99Seconds: 2400,
            maxSeconds: 2400,
            meanSeconds: 1200,
          },
          topCause: "hole",
        },
        {
          repo: "acme/widgets",
          job: "lint",
          jobs: 4,
          queuedSeconds: 0,
          busySeconds: 1200,
          queuedShare: 0,
          wait: {
            n: 0,
            minSeconds: 0,
            p50Seconds: 0,
            p90Seconds: 0,
            p99Seconds: 0,
            maxSeconds: 0,
            meanSeconds: 0,
          },
          topCause: "",
        },
      ],
      buckets: [
        {
          since: "2026-08-27T00:00:00Z",
          until: "2026-08-29T08:00:00Z",
          jobs: 4,
          queuedSeconds: 0,
          busySeconds: 1800,
          queuedShare: 0,
        },
        {
          since: "2026-08-29T08:00:00Z",
          until: "2026-08-31T16:00:00Z",
          jobs: 4,
          queuedSeconds: 1200,
          busySeconds: 1800,
          queuedShare: 0.4,
        },
        {
          since: "2026-08-31T16:00:00Z",
          until: "2026-09-03T00:00:00Z",
          jobs: 4,
          queuedSeconds: 2400,
          busySeconds: 1800,
          queuedShare: 0.57,
        },
      ],
    },
    prompts: {
      runs: 2,
      bytes: {
        n: 2,
        meanBytes: 130000,
        maxBytes: 250000,
        p50AtMostBytes: 131071,
        p95AtMostBytes: 262143,
        p99AtMostBytes: 262143,
      },
      large: 1,
      largeShare: 0.5,
      warnTokens: 50000,
      contextTokens: 200000,
      threadCut: 1,
      threadCutShare: 0.5,
      heaviest: [
        {
          taskId: "51",
          runs: 2,
          maxBytes: 250000,
          maxTokens: 62500,
          attempt: 2,
          threadCut: 1,
        },
      ],
    },
    ...over,
  });
}

describe("formatSeconds", () => {
  it("renders a duration rather than a bare second count", () => {
    expect(formatSeconds(31)).toBe("31s");
    expect(formatSeconds(252)).toBe("4m 12s");
    expect(formatSeconds(11400)).toBe("3h 10m");
  });

  it("gets coarser the longer the duration is", () => {
    expect(formatSeconds(90000)).toBe("1d 1h");
  });

  it("renders a missing number as a dash, but a real zero as a zero", () => {
    expect(formatSeconds(null)).toBe("—");
    expect(formatSeconds(undefined)).toBe("—");
    expect(formatSeconds(0)).toBe("0s");
  });
});

// The whole reason `n` is a column: a percentile only says something the
// maximum did not once at least one sample can fall in the tail it
// names, which takes 100/(100-p) samples.
describe("isThinPercentile", () => {
  it("calls a p99 over a handful of samples what it is", () => {
    expect(isThinPercentile(4, 99)).toBe(true);
    expect(isThinPercentile(99, 99)).toBe(true);
    expect(isThinPercentile(100, 99)).toBe(false);
  });

  it("holds a p90 to a tenth of the bar a p99 gets", () => {
    expect(isThinPercentile(9, 90)).toBe(true);
    expect(isThinPercentile(10, 90)).toBe(false);
  });

  it("asks almost nothing of a median", () => {
    expect(isThinPercentile(1, 50)).toBe(true);
    expect(isThinPercentile(2, 50)).toBe(false);
  });

  it("says nothing about a stage with no samples at all", () => {
    expect(isThinPercentile(0, 99)).toBe(false);
  });
});

describe("sortedOutcomes", () => {
  it("puts the ending that dominates the window first, breaking ties by name", () => {
    expect(sortedOutcomes({ failed: 14, succeeded: 45, cancelled: 1 })).toEqual(
      [
        ["succeeded", 45],
        ["failed", 14],
        ["cancelled", 1],
      ],
    );
    expect(sortedOutcomes({ zebra: 2, alpha: 2 })).toEqual([
      ["alpha", 2],
      ["zebra", 2],
    ]);
  });

  it("survives a report with no outcomes at all", () => {
    expect(sortedOutcomes(undefined)).toEqual([]);
  });
});

describe("backlogRows", () => {
  it("orders the backlog the way the sidebar orders states", () => {
    expect(
      backlogRows({ running: 2, queued: 4, proposed: 1 }).map((r) => r.state),
    ).toEqual(["proposed", "queued", "running"]);
  });

  it("keeps a state this UI does not know about rather than dropping it", () => {
    expect(
      backlogRows({ queued: 1, hibernating: 3 }).map((r) => r.state),
    ).toEqual(["queued", "hibernating"]);
  });

  it("leaves out states nothing is sitting in", () => {
    expect(backlogRows({ queued: 2, running: 0 }).map((r) => r.state)).toEqual([
      "queued",
    ]);
  });
});

describe("the whole report, as the Metrics pane draws it", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("asks for the default window, the one `grain metrics` prints with no flags", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);

    expect(await screen.findByText("Throughput")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/metrics?window=7d&buckets=24");
  });

  // GitHub CI sits beside grain's own CI, labelled with its source, and a
  // report with no GitHub results draws no section for it at all.
  it("draws GitHub CI labelled with its source, after grain's CI queueing", async () => {
    api.mockResolvedValue(
      withCensus({
        githubCI: {
          source: "github",
          jobs: 10,
          passed: 6,
          failed: 2,
          cancelled: 1,
          skipped: 1,
          neutral: 0,
          other: 0,
          passRate: 6 / 9,
          failRate: 2 / 9,
          cancelRate: 1 / 9,
          byJob: [
            {
              source: "github",
              repo: "acme/widgets",
              job: "CI / test",
              jobs: 5,
              passed: 3,
              failed: 2,
              cancelled: 0,
              passRate: 0.6,
              failRate: 0.4,
              cancelRate: 0,
              flaky: 1,
            },
          ],
          moreJobs: 0,
          timeToGreen: {
            recovered: 1,
            stillRed: 1,
            duration: { n: 1, p50Seconds: 1800, maxSeconds: 1800 },
          },
          flaky: [
            {
              source: "github",
              repo: "acme/widgets",
              job: "CI / test",
              commits: 1,
              last: "abc",
            },
          ],
          flakyCommits: 1,
          baseBranches: [
            {
              source: "github",
              repo: "acme/widgets",
              branch: "main",
              redShare: 0.25,
              redSeconds: 3600,
              greenSeconds: 10800,
              redStretches: 2,
              redNow: true,
            },
          ],
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    const heading = await screen.findByRole("heading", { name: /GitHub CI/ });
    expect(screen.getByTestId("github-ci-source")).toHaveTextContent(
      "source: github",
    );
    const queueing = screen.getByRole("heading", { name: "CI queueing" });
    expect(
      queueing.compareDocumentPosition(heading) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    const jobs = screen.getByTestId("github-ci-jobs");
    expect(within(jobs).getByText("CI / test")).toBeInTheDocument();
    expect(within(jobs).getByText("40%")).toBeInTheDocument();
    expect(screen.getByTestId("github-ci-flaky")).toHaveTextContent(
      "CI / test (acme/widgets) on 1 commit",
    );
    const bases = screen.getByTestId("github-ci-bases");
    expect(within(bases).getByText("main")).toBeInTheDocument();
    expect(within(bases).getByText("red")).toBeInTheDocument();
  });

  it("draws no GitHub CI section for a window with no GitHub results", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("CI queueing");
    expect(screen.queryByTestId("github-ci-source")).toBeNull();
  });

  it("shows the throughput counts and their daily rates", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");

    expect(screen.getByText("Tasks filed")).toBeInTheDocument();
    expect(screen.getByText("42")).toBeInTheDocument();
    expect(screen.getByText("6.0/day")).toBeInTheDocument();
    expect(screen.getByText("5.4/day")).toBeInTheDocument();
    expect(screen.getByText("1.58")).toBeInTheDocument();
  });

  it("draws the bucketed series as trend lines", async () => {
    api.mockResolvedValue(report());
    const { container } = render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");

    // One line per series -- filed, completed, attempts finished -- each
    // a polyline through every bucket rather than an empty placeholder.
    const polylines = container.querySelectorAll("polyline");
    expect(polylines).toHaveLength(3);
    expect(polylines[0].getAttribute("points").trim().split(" ")).toHaveLength(
      3,
    );
  });

  it("shows occupancy against the configured concurrency limit", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Capacity");

    expect(screen.getByText("0.42 of 3")).toBeInTheDocument();
    expect(
      screen.getByText(
        "14% of the worker and merger limit; console sessions run outside it",
      ),
    ).toBeInTheDocument();
  });

  it("says so rather than dividing by nothing when no concurrency limit is set", async () => {
    api.mockResolvedValue(
      report({
        runs: {
          outcomes: {},
          attemptsPerCompletion: 0,
          meanConcurrent: 0.42,
          maxConcurrent: 0,
          utilization: 0,
          live: 0,
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Capacity");

    expect(screen.getByText("0.42")).toBeInTheDocument();
    expect(
      screen.getByText(/no concurrency limit configured/i),
    ).toBeInTheDocument();
  });

  it("shows the cluster's exec-tunnel failures beside capacity", async () => {
    api.mockResolvedValue(
      report({
        runs: {
          ...report().runs,
          execTunnelFailures: 7,
          execTunnelToolCalls: 12,
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Capacity");

    const label = screen.getByText("Setups the exec tunnel failed");
    const stat = label.parentElement;
    expect(within(stat).getByText("7")).toBeInTheDocument();
    expect(
      within(stat).getByText("and 12 tool calls mid-run"),
    ).toBeInTheDocument();
  });

  it("lists the attempt outcomes, commonest first", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Capacity");

    expect(screen.getByText("succeeded 45")).toBeInTheDocument();
    expect(screen.getByText("failed 14")).toBeInTheDocument();
    expect(screen.getByText("cancelled 1")).toBeInTheDocument();
  });

  it("keeps the latency stages in the order the API sent them", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    const rows = screen.getAllByRole("row").slice(1); // drop the header row
    expect(
      rows.map((r) => within(r).getAllByRole("cell")[0].textContent),
    ).toEqual([
      "filed -> approved",
      "approved -> attempt started",
      "one whole attempt",
      "attempt finished -> next attempt started",
      "filed -> completed",
    ]);
  });

  it("shows each stage's own n beside its percentiles", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    const row = screen.getByText("filed -> approved").closest("tr");
    expect(
      within(row)
        .getAllByRole("cell")
        .map((c) => c.textContent),
    ).toEqual([
      "filed -> approved",
      "12",
      "4m 12s",
      "1h 2m",
      "3h 10m*",
      "3h 10m",
    ]);
  });

  // README, "Measuring throughput and latency": a p99 over four samples
  // is the maximum wearing a percentile's name, so it is not shown as if
  // it were a p99.
  it("marks the percentiles a stage has too few samples to support", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    // Four samples support neither a p90 nor a p99; both are the max.
    const retries = screen
      .getByText("attempt finished -> next attempt started")
      .closest("tr");
    expect(
      within(retries)
        .getAllByRole("cell")
        .map((c) => c.textContent),
    ).toEqual([
      "attempt finished -> next attempt started",
      "4",
      "2m 0s",
      "4m 0s*",
      "6m 0s*",
      "6m 0s",
    ]);

    // Sixty clears the p90 bar (10 samples) but not the p99 one (100).
    const attempt = screen.getByText("one whole attempt").closest("tr");
    expect(within(attempt).getByText("26m 10s")).toBeInTheDocument();
    expect(within(attempt).getByText("52m 0s*")).toBeInTheDocument();

    expect(screen.getByText(/maximum under another name/i)).toBeInTheDocument();
  });

  // The stages are independent measurements: they are drawn as a table
  // of distributions, never a stacked bar, and the pane says why right
  // where the numbers are.
  it("says the stages do not add up to the lead time", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    expect(
      screen.getByText(/do not add up to the lead time/i),
    ).toBeInTheDocument();
  });

  it("shows nothing to report rather than a table of dashes when no stage has samples", async () => {
    api.mockResolvedValue(report({ latency: [stage("filed -> completed")] }));
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    expect(screen.getByText(/no latency to report yet/i)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  // The backlog is a gauge read at the report's own moment, not
  // something measured over the window, and the pane has to say which.
  it("labels the backlog as a reading taken now rather than over the window", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Backlog");

    expect(
      screen.getByText(/right now, not over the window/i),
    ).toBeInTheDocument();
    expect(screen.getByText("Waiting 4")).toBeInTheDocument();
    // Both asked states are one "Asked" chip, with their sum.
    expect(screen.getByText("Asked 3")).toBeInTheDocument();
  });

  it("links the oldest queued task through to its own page", async () => {
    api.mockResolvedValue(report());
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(<ReportBoard showError={() => {}} onOpenTask={onOpenTask} />);
    await screen.findByText("Backlog");

    expect(screen.getByText(/waiting 2h 14m/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "task 51" }));

    expect(onOpenTask).toHaveBeenCalledWith("51");
  });

  it("leaves the oldest-queued line out when nothing is queued", async () => {
    api.mockResolvedValue(
      report({
        backlog: {
          byState: { running: 1 },
          queued: 0,
          oldestQueuedSeconds: 0,
          oldestQueuedTaskId: "",
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Backlog");

    expect(screen.queryByText(/oldest queued/i)).not.toBeInTheDocument();
  });

  it("re-fetches over the window the picker asks for", async () => {
    api.mockResolvedValue(report());
    const user = userEvent.setup();
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");

    await user.click(screen.getByLabelText(/Window/));
    await user.click(
      await screen.findByRole("option", { name: "Last 30 days" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith(
        "/api/metrics?window=30d&buckets=24",
      ),
    );
  });

  it("re-fetches the same window on Refresh", async () => {
    api.mockResolvedValue(report());
    const user = userEvent.setup();
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");
    expect(api).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
    expect(api).toHaveBeenLastCalledWith("/api/metrics?window=7d&buckets=24");
  });

  // A report costs a full scan of `task` and `task_run` every time it is
  // asked for, so unlike the Logs and Sandbox health panels beside it,
  // this one loads once and then waits to be asked again.
  it("does not poll on a timer", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");

    vi.useFakeTimers();
    try {
      await vi.advanceTimersByTimeAsync(60000);
      expect(api).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("hands a failed report to showError rather than blanking the pane", async () => {
    api.mockRejectedValue(new Error("reading task timings: no such table"));
    const showError = vi.fn();
    render(<ReportBoard showError={showError} />);

    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        new Error("reading task timings: no such table"),
      ),
    );
  });

  it("points at a longer window when the one asked for is empty", async () => {
    api.mockResolvedValue(
      report({
        throughput: {
          tasksFiled: 0,
          tasksCompleted: 0,
          tasksClosed: 0,
          runsStarted: 0,
          runsFinished: 0,
          filedPerDay: 0,
          completedPerDay: 0,
          runsFinishedPerDay: 0,
          buckets: [],
        },
        latency: [stage("filed -> completed")],
        backlog: {
          byState: {},
          queued: 0,
          oldestQueuedSeconds: 0,
          oldestQueuedTaskId: "",
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");

    expect(
      screen.getByText(/nothing was filed or attempted inside this window/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/the backlog is empty/i)).toBeInTheDocument();
  });

  it("reports the tool census, busiest tool first", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Tool use");

    expect(screen.getByText("110")).toBeInTheDocument();
    expect(screen.getByText("14% of all calls")).toBeInTheDocument();

    const rows = screen
      .getAllByRole("row")
      .filter((r) => within(r).queryByText(/run_command|edit_file/));
    expect(
      rows.map((r) => within(r).getAllByRole("cell")[0].textContent),
    ).toEqual(["run_command", "edit_file"]);
    // edit_file's error rate is the "String not found" loop measured, and
    // the reason the table is per tool rather than one number.
    expect(within(rows[1]).getByText("5 (50%)")).toBeInTheDocument();
    // Only a tool that bounds its own work reports timeouts; the rest
    // show a dash rather than a zero they cannot vouch for. edit_file was
    // never cut either, so its row is dashes for those two columns too.
    expect(within(rows[0]).getByText("2 (2%)")).toBeInTheDocument();
    expect(within(rows[1]).getAllByText("—")).toHaveLength(3);
    // The p95 is a bucket bound, and is shown as one.
    expect(within(rows[0]).getByText("≤ 64 KB")).toBeInTheDocument();
    // Cut answers are the half of the census that survives the cap: how
    // often it fired, and what the median cut dropped.
    expect(within(rows[0]).getByText("4 (4%)")).toBeInTheDocument();
    expect(within(rows[0]).getByText("≤ 32 KB")).toBeInTheDocument();
    expect(screen.getByText("4% of all calls")).toBeInTheDocument();
  });

  it("reports prompt sizes with the bound that censors them, and names the heaviest task", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Prompts");

    // The distribution, as bounds, in the units a prompt is judged in.
    expect(screen.getByText("≤ 128 KB")).toBeInTheDocument();
    expect(screen.getByText("p99 ≤ 256 KB")).toBeInTheDocument();
    expect(screen.getByText(/mean 127 KB . max 244 KB/)).toBeInTheDocument();
    // The two counts the cap does not censor, which is what the sizes
    // beside them have to be read against.
    expect(screen.getByText("Over the warning line")).toBeInTheDocument();
    expect(screen.getByText("Conversation cut to fit")).toBeInTheDocument();
    expect(screen.getAllByText("50% of them")).toHaveLength(2);
    // And where to look: a distribution cannot say whose prompts grew.
    const row = screen
      .getAllByRole("row")
      .find((r) => within(r).queryByText("task 51"));
    expect(within(row).getByText("244 KB")).toBeInTheDocument();
    expect(within(row).getByText("62500")).toBeInTheDocument();
  });

  // A deployment whose runs recorded no prompt gets no section, for the
  // tool census's reason: zeroes would read as "grain hands its runs
  // nothing".
  it("says nothing about prompts when no run recorded one", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    expect(screen.queryByText("Prompts")).not.toBeInTheDocument();
  });

  // The pane names no sections, so one with nothing to say is still
  // simply not there -- the note is a board's, which named it.
  it("leaves a section with nothing to say out silently", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");
    expect(screen.queryByText(/nothing to report over this window/)).toBeNull();
  });

  it("heads every rendered section at h3", async () => {
    api.mockResolvedValue(
      withCensus({
        sandboxes: {
          total: {
            attempted: 2,
            created: 2,
            failed: 0,
            finished: 2,
            unaccounted: 0,
            hours: 1,
            meanSeconds: 1800,
          },
          kinds: [],
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Throughput");
    for (const name of [
      "Throughput",
      "Capacity",
      "Latency",
      "Tool use",
      "Prompts",
      "CI waits",
      "Mid-run pull requests",
      "Landings & Lead Time",
      "CI queueing",
      "Sandboxes",
      "Backlog",
    ]) {
      expect(
        screen.getByRole("heading", { level: 3, name }),
      ).toBeInTheDocument();
    }
  });

  it("reports the CI loop: verdicts, how long it blocked, and pushes before green", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("CI waits");

    expect(screen.getByText("timed_out 2")).toBeInTheDocument();
    expect(screen.getByText("passed 1")).toBeInTheDocument();
    // The median blocked time, read through its own sub-line: "9m 0s"
    // alone is a number several sections of this report can produce.
    expect(screen.getByText(/p90 15m 0s . max 15m 0s/)).toBeInTheDocument();
    expect(screen.getByText("2.0")).toBeInTheDocument();
    expect(
      screen.getByText(/over 1 attempt that went green/i),
    ).toBeInTheDocument();
  });

  it("reports mid-run pull requests as an adoption rate and a comparison", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Mid-run pull requests");

    expect(screen.getByText("38%")).toBeInTheDocument();
    expect(screen.getByText(/3 attempts . 7 calls in all/)).toBeInTheDocument();
    // The two fix-task rates are only readable against each other, so
    // both are on screen with their own denominators under them.
    expect(screen.getByText("0%")).toBeInTheDocument();
    expect(screen.getByText("0 of 3")).toBeInTheDocument();
    expect(screen.getByText("40%")).toBeInTheDocument();
    expect(screen.getByText("2 of 5")).toBeInTheDocument();
  });

  // A window in which no run could have opened its own pull request gets
  // no section at all: zeroes there would read as "no run ever calls it".
  it("says nothing about mid-run pull requests when nobody was offered one", async () => {
    api.mockResolvedValue(
      withCensus({
        pullRequests: {
          runs: 0,
          opened: 0,
          calls: 0,
          adoptionRate: 0,
          withTool: { runs: 0, fixTasks: 0, rate: 0 },
          withoutTool: { runs: 0, fixTasks: 0, rate: 0 },
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Tool use");

    expect(screen.queryByText("Mid-run pull requests")).not.toBeInTheDocument();
  });

  it("splits the endings out of the outcome words", async () => {
    api.mockResolvedValue(
      report({
        runs: {
          outcomes: { succeeded: 45, cancelled: 2 },
          endings: { succeeded: 45, runtime_cap: 1, task_closed: 1 },
          attemptsPerCompletion: 1.5,
          meanConcurrent: 0.4,
          maxConcurrent: 3,
          utilization: 0.13,
          live: 0,
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Capacity");

    expect(screen.getByText("cancelled 2")).toBeInTheDocument();
    expect(screen.getByText("runtime_cap 1")).toBeInTheDocument();
    expect(screen.getByText("task_closed 1")).toBeInTheDocument();
  });

  // What this deployment's sandboxes have cost it (grain/task-315).
  // The one section of the report that is not over its window, which is
  // why it says so in its own caption: the counters are a lifetime
  // meter, not a derivation of the window's rows.
  it("reports what sandboxes have cost, and says the figures are not over the window", async () => {
    api.mockResolvedValue(
      report({
        sandboxes: {
          total: {
            attempted: 2416,
            created: 2413,
            failed: 3,
            finished: 2410,
            unaccounted: 3,
            hours: 1204,
            meanSeconds: 1798.5,
            since: "2026-09-05T04:08:00Z",
          },
          kinds: [
            {
              kind: "agent",
              created: 46,
              failed: 0,
              finished: 46,
              unaccounted: 0,
              hours: 3,
              meanSeconds: 234.8,
            },
            {
              kind: "ci",
              created: 2367,
              failed: 3,
              finished: 2364,
              unaccounted: 3,
              hours: 1201,
              meanSeconds: 1828.9,
            },
          ],
        },
      }),
    );
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Sandboxes");

    expect(screen.getByText("2,413")).toBeInTheDocument();
    expect(screen.getByText("1,204 h")).toBeInTheDocument();
    // Both kinds, because the question behind the total is what CI costs
    // against agent runs.
    expect(screen.getByText("2,367")).toBeInTheDocument();
    expect(screen.getByText("46")).toBeInTheDocument();
    // The two honesty lines: sandboxes that never started, and the ones
    // whose time is missing because nobody saw them stop.
    expect(screen.getByText("3 never started")).toBeInTheDocument();
    expect(screen.getByText("Not seen to stop")).toBeInTheDocument();
    expect(screen.getByText(/not over the window above/i)).toBeInTheDocument();
  });

  // A deployment that has counted no sandbox yet gets no section: a row
  // of noughts reads as one that builds none.
  it("says nothing about sandboxes before any has been counted", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    expect(screen.queryByText("Sandboxes")).not.toBeInTheDocument();
  });

  // CI queueing (grain/task-345): the share of CI's job-time that went on
  // waiting for a machine, what held it, and which repository's jobs wear
  // it. The section the CI page cannot answer, because the question is
  // about a window rather than about one run.
  it("reports the share of CI job-time spent waiting, and what held it", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("CI queueing");

    expect(screen.getByText("30%")).toBeInTheDocument();
    expect(
      screen.getByText(/1h 0m waiting . 2h 20m working/),
    ).toBeInTheDocument();
    // The cache hits are named in the caption rather than folded into the
    // share: a job answered from the cache asked for no machine.
    expect(
      screen.getByText(/3 answered from the job cache/),
    ).toBeInTheDocument();
    // A job cancelled while still queueing is the case the measurement
    // was built for -- its row has no duration of its own.
    expect(screen.getByText("Never got a machine")).toBeInTheDocument();

    // Each cause is a different setting to reach for, worst first, with
    // the gate's own sentence beside the worst wait of each.
    // The causes table, told from the jobs table under it by its own
    // width: the jobs table names a cause too, in its "held by" column.
    const causes = screen
      .getAllByRole("row")
      .filter(
        (r) =>
          within(r).queryByText(/^(hole|slot|unattributed)$/) &&
          within(r).getAllByRole("cell").length === 5,
      );
    expect(
      causes.map((r) => within(r).getAllByRole("cell")[0].textContent),
    ).toEqual([
      "holecapacity held back for a larger job (the gate's reservation)",
      "slotthe sandbox count (max_ci_jobs)",
      "unattributednothing recorded a cause",
    ]);
    expect(
      screen.getByText(/capacity is being held for a larger job/),
    ).toBeInTheDocument();
  });

  it("names the repository and the jobs that wear the waiting", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("CI queueing");

    expect(screen.getByText("acme/widgets 55m 0s")).toBeInTheDocument();
    // The jobs table is the bias the whole measurement was built to look
    // for: the expensive job waits and the cheap one, which waited for
    // nothing, is not in the table at all.
    const jobs = screen
      .getAllByRole("row")
      .filter((r) => within(r).queryByText("image"));
    expect(jobs).toHaveLength(1);
    expect(
      within(jobs[0])
        .getAllByRole("cell")
        .map((cell) => cell.textContent),
    ).toEqual([
      "acme/widgets",
      "image",
      "2",
      "40m 0s",
      "67%",
      "40m 0s",
      "hole",
    ]);
    expect(
      screen.getAllByRole("row").filter((r) => within(r).queryByText("lint")),
    ).toHaveLength(0);
  });

  // Waiting nobody attributed is counted and blamed on nothing: every job
  // stored before grain kept the cause is in it, and a breakdown that
  // quietly dropped them would read as a deployment with less queueing
  // than it has.
  it("says how much of the waiting has no cause recorded", async () => {
    api.mockResolvedValue(withCensus());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("CI queueing");

    expect(
      screen.getByText(/5m 0s of that waiting has no cause recorded/),
    ).toBeInTheDocument();
  });

  // A store whose runs all predate the census reports nothing rather than
  // a table of zeroes: "nobody measured this" is not "the tools never
  // failed".
  it("shows no tool or CI section at all when nothing recorded one", async () => {
    api.mockResolvedValue(report());
    render(<ReportBoard showError={() => {}} />);
    await screen.findByText("Latency");

    expect(screen.queryByText("Tool use")).not.toBeInTheDocument();
    expect(screen.queryByText("CI waits")).not.toBeInTheDocument();
    expect(screen.queryByText("CI queueing")).not.toBeInTheDocument();
  });

  it("reports branch repairs telemetry under CI waits", async () => {
    api.mockResolvedValue(withCensus());
    render(
      <ReportBoard
        showError={() => {}}
        board={{
          id: "b-checks",
          title: "Checks",
          kind: "report",
          sections: [{ kind: "checks" }],
        }}
      />,
    );
    await screen.findByText("CI waits");

    expect(screen.getByText("Branch repairs")).toBeInTheDocument();
    expect(
      screen.getByText(/3 passes \(67% settled\) across 2 runs/i),
    ).toBeInTheDocument();
    expect(screen.getByText("1h 0m")).toBeInTheDocument();
    expect(screen.getByText("2h 0m")).toBeInTheDocument();
    expect(screen.getByText("6.0")).toBeInTheDocument();
    expect(screen.getByText("13 KB")).toBeInTheDocument();
  });

  it("reports merge queue landings, batches, and branch repairs", async () => {
    api.mockResolvedValue(withCensus());
    render(
      <ReportBoard
        showError={() => {}}
        board={{
          id: "b1",
          title: "Merges",
          kind: "report",
          sections: [{ kind: "merges" }],
        }}
      />,
    );
    await screen.findByText("Landings & Lead Time");

    expect(screen.getByText("Landed changes")).toBeInTheDocument();
    expect(screen.getByText("4")).toBeInTheDocument();
    expect(screen.getByText("Merge Batches")).toBeInTheDocument();
    expect(screen.getByText("Branch repairs")).toBeInTheDocument();
  });

  it("renders per-template agent telemetry and steward report checks", async () => {
    api.mockResolvedValue(
      report({
        agents: {
          templates: [
            {
              templateId: "system:pipeline-steward",
              wakes: 3,
              calls: 24,
              bytes: 32768,
              callsP50: 8,
              callsP95: 12,
              bytesP50: 8192,
              bytesP95: 16384,
              elisions: 2,
              reReads: 1,
            },
          ],
          reportChecks: {
            wakes: 3,
            claims: 10,
            uncited: 1,
            unread: 0,
            mismatched: 2,
            uncitedRate: 0.1,
            unreadRate: 0,
            mismatchRate: 0.2,
          },
        },
      }),
    );
    render(
      <ReportBoard
        showError={() => {}}
        board={{
          id: "b-agents",
          title: "Agent Telemetry",
          kind: "report",
          sections: [{ kind: "agents" }],
        }}
      />,
    );
    await screen.findByText("system:pipeline-steward");
    expect(
      screen.getByText(
        /Report checks: 3 wake\(s\), 10 claim\(s\) — uncited 1 \(10\.0%\), unread 0 \(0\.0%\), mismatched 2 \(20\.0%\)/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("8 KB")).toBeInTheDocument();
    expect(screen.getByText("16 KB")).toBeInTheDocument();
  });
});

describe("formatBytes", () => {
  it("renders a size in the units a cap would be written in", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(65535)).toBe("64 KB");
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MB");
    expect(formatBytes(undefined)).toBe("—");
  });
});

describe("percent", () => {
  it("renders a rate as whole percent", () => {
    expect(percent(0.5)).toBe("50%");
    expect(percent(0.024)).toBe("2%");
    expect(percent(0)).toBe("0%");
  });
});
