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

// The CI section in the rail, the page it opens, and the two ways into
// one run: the row, and a task's own grain CI verdict (grain/task-354).
//
// Part of the App suite -- see App.test.jsx for why it is several files.

import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import api, { unreachableReason } from "./api.js";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll and showError
// call on whatever this mock rejects with -- stay the real, pure
// functions rather than becoming undefined (App.test.jsx's own mock,
// same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

const STARTED = "2026-09-07T12:00:00Z";

// One run still going, with one of its two jobs on a machine: the state
// this section exists to show.
const liveRun = {
  id: "9f2c1a",
  repo: "acme/widgets",
  ref: "grain/task-1",
  event: "push",
  startedAt: STARTED,
  jobs: [
    {
      workflow: "tests",
      workflowPath: "tests.yml",
      jobId: "test",
      name: "go test",
      sandbox: "c9f2c1a-0",
      startedAt: STARTED,
      steps: [],
    },
    {
      workflow: "tests",
      workflowPath: "tests.yml",
      jobId: "lint",
      name: "lint",
      steps: [],
    },
  ],
};

const withCI = (runs, tasks = initialTasks) =>
  setupApi(tasks, [], [], [], [], [], runs);

describe("App", () => {
  afterEach(resetAppTestState);

  it("opens grain CI from the rail, sandboxes first, at a path of its own", async () => {
    withCI([liveRun]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");
    expect(screen.getByRole("button", { name: "CI 1" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^CI/ }));

    // What is running now, and on what.
    expect(await screen.findByText("Active sandboxes (1)")).toBeInTheDocument();
    // Collapsed to its summary until somebody opens it.
    await user.click(screen.getByRole("button", { name: /Active sandboxes/ }));
    expect(screen.getByText("c9f2c1a-0")).toBeInTheDocument();
    // ...above the runs themselves.
    expect(screen.getByText("Runs")).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/ci"));
  });

  it("opens one run at its own path, and back out to the list", async () => {
    withCI([liveRun]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");
    await user.click(screen.getByRole("button", { name: /^CI/ }));

    await user.click(
      (await screen.findAllByRole("button", { name: "acme/widgets" }))[0],
    );

    expect(
      await screen.findByRole("button", { name: /Back/ }),
    ).toBeInTheDocument();
    // A run is a place, so it can be reloaded onto and sent on.
    await waitFor(() => expect(window.location.pathname).toBe("/ci/9f2c1a"));

    await user.click(screen.getByRole("button", { name: /Back/ }));
    await waitFor(() => expect(window.location.pathname).toBe("/ci"));
  });

  it("opens the run a /ci/<run> link names, cold", async () => {
    window.history.replaceState(null, "", "/ci/9f2c1a");
    withCI([liveRun]);
    render(<App />);

    expect(
      await screen.findByRole("button", { name: /Back/ }),
    ).toBeInTheDocument();
    // The open run's own job. The active-sandboxes table behind it is
    // collapsed to its summary, so it does not name the job a second time.
    expect(screen.getAllByText("go test")).toHaveLength(1);
  });

  // grain/task-351, landing on the CI page rather than in the System
  // pane since grain/task-354: a repo's own runs list sends its reader
  // to the rest of them, narrowed to that repo.
  it("opens the CI page narrowed to a repo, from that repo's own page", async () => {
    withCI([liveRun]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/widgets"));
    await user.click(
      await screen.findByRole("button", { name: /All runs for this repo/ }),
    );

    expect(await screen.findByText("Active sandboxes (1)")).toBeInTheDocument();
    // The condition the page opened narrowed by, so "all runs" means
    // this repo's rather than the deployment's hundred -- and it says so
    // as a chip the reader can take off again (ciFilters.js).
    expect(screen.getByText("Repo is any of acme/widgets")).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/ci"));
  });

  // From a task's own CI verdict to the step output that says why its
  // branch is red. The shared run pane opens over the task, so closing it
  // returns to the context in which the verdict was read.
  it("follows a task's grain CI verdict to the run behind it", async () => {
    const tasks = [
      {
        ...initialTasks[0],
        ci: { runId: "9f2c1a", conclusion: "failure", ref: "grain/task-1" },
      },
      initialTasks[1],
    ];
    withCI([liveRun], tasks);
    const user = userEvent.setup();
    render(<App />);

    await user.click(await screen.findByText("Fix bug"));
    await user.click(
      await screen.findByRole("button", { name: "Show the run" }),
    );

    expect(
      await screen.findByRole("button", { name: /Back/ }),
    ).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));

    await user.click(screen.getByRole("button", { name: /Back/ }));
    expect(screen.getAllByText("Fix bug").length).toBeGreaterThan(0);
    await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));
  });

  it("skips overlapping background /api/ci/runs polls while a previous poll is still in flight", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    window.history.replaceState(null, "", "/ci");
    withCI([liveRun]);
    const baseImpl = api.getMockImplementation();

    const isCIPageListPoll = (path) =>
      (path === "/api/ci/runs" || path.startsWith("/api/ci/runs?")) &&
      !path.includes("conclusion=running");

    let slowResolve = null;
    let listCalls = 0;
    api.mockImplementation((path, options) => {
      if (isCIPageListPoll(path)) {
        listCalls += 1;
        if (listCalls === 2) {
          return new Promise((resolve) => {
            slowResolve = resolve;
          });
        }
        return Promise.resolve({ enabled: true, runs: [liveRun], faults: [] });
      }
      return baseImpl(path, options);
    });

    render(<App />);
    expect(await screen.findByText("Active sandboxes (1)")).toBeInTheDocument();
    expect(listCalls).toBe(1);

    // First 5s interval tick starts poll #2, which stays in flight.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(listCalls).toBe(2);

    // Subsequent 5s interval ticks while poll #2 is still in flight must not stack overlapping polls.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(listCalls).toBe(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(listCalls).toBe(2);

    // Once the in-flight poll settles, the next 5s tick starts a new poll normally.
    await act(async () => {
      slowResolve({ enabled: true, runs: [liveRun], faults: [] });
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(listCalls).toBe(3);
  });

  it("tolerates a single background poll blip on /ci and formats sustained unreachable failures with unreachableReason", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    window.history.replaceState(null, "", "/ci");
    withCI([liveRun]);
    const baseImpl = api.getMockImplementation();

    const isCIPageListPoll = (path) =>
      (path === "/api/ci/runs" || path.startsWith("/api/ci/runs?")) &&
      !path.includes("conclusion=running");

    let shouldFail = false;
    api.mockImplementation((path, options) => {
      if (isCIPageListPoll(path)) {
        if (shouldFail) {
          return Promise.reject(new TypeError("Failed to fetch"));
        }
        return Promise.resolve({ enabled: true, runs: [liveRun], faults: [] });
      }
      return baseImpl(path, options);
    });

    render(<App />);
    expect(await screen.findByText("Active sandboxes (1)")).toBeInTheDocument();

    // 1. A single transient "Failed to fetch" background poll blip does not raise the warning alert or toast.
    shouldFail = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(
      screen.queryByText(/The latest read of CI runs failed/),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    // Recovering on the next tick resets the streak so a later single blip is also tolerated.
    shouldFail = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    shouldFail = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(
      screen.queryByText(/The latest read of CI runs failed/),
    ).not.toBeInTheDocument();

    // 2. A second consecutive unreachable poll failure surfaces the warning alert and toast using unreachableReason wording rather than raw "Failed to fetch".
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    const warning = await screen.findByText(
      /The latest read of CI runs failed/,
    );
    expect(warning).toHaveTextContent(
      unreachableReason(new TypeError("Failed to fetch")),
    );
    expect(warning).not.toHaveTextContent("Failed to fetch");
  });
});
