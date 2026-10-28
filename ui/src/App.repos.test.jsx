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

// A repo's own page (grain/task-111): the link to its filed tasks,
// filing against the repo whose page is open, and the release pane
// behind it.
//
// Part of the App suite -- see App.test.jsx for why it is six files.

import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import api from "./api.js";
import { resetAppTestState, setupApi } from "./appTestHarness.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll and showError
// call on whatever this mock rejects with -- stay the real, pure
// functions rather than becoming undefined (App.test.jsx's own mock,
// same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

describe("App", () => {
  afterEach(resetAppTestState);

  // A repo's task count is the way into the task list, carrying a fresh
  // repo filter rather than inheriting the task pane's current narrowing.
  it("reaches tasks filed against a repo from the top of its page", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/other"));

    const tasksLink = await screen.findByRole("link", {
      name: "1 task filed here",
    });
    expect(tasksLink).toHaveAttribute("href", "/?f=repo:any:acme/other");
    expect(screen.queryByText("Add feature")).not.toBeInTheDocument();
  });

  it("files a new task against the repo whose page is open", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/other"));
    await screen.findByRole("heading", { name: "acme/other" });

    await user.click(screen.getByRole("button", { name: "New task" }));
    expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/other");

    await user.type(screen.getByLabelText(/Title/), "Ship it");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(
      await screen.findByRole("link", { name: "2 tasks filed here" }),
    ).toBeInTheDocument();
  });

  it("opens a repo's release pane from its page and back out of it", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/other"));
    await user.click(await screen.findByRole("button", { name: "Releases" }));

    expect(
      await screen.findByRole("heading", { name: "acme/other releases" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Releases" }),
    ).not.toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/repos/acme/other/releases"),
    );

    await user.click(screen.getByRole("button", { name: /acme\/other$/ }));

    expect(
      await screen.findByRole("heading", { name: "acme/other" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/repos/acme/other"),
    );
  });

  // A pull request -- the change one branch brings to another -- is a
  // page under the repo it belongs to (grain/task-375), reached from the
  // repo's own compare form and from a task's Internal PR link, and back
  // out of it is the repo rather than the repo list.
  it("opens one of a repo's pull requests from its page and back out of it", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/other"));
    await user.click(await screen.findByRole("tab", { name: "Branches" }));
    await user.type(
      await screen.findByLabelText(/Head branch/),
      "grain/task-1",
    );
    await user.click(screen.getByRole("button", { name: "Compare" }));

    expect(await screen.findByLabelText("Branch changes")).toHaveTextContent(
      "the changed line",
    );
    expect(window.location.pathname).toBe(
      "/repos/acme/other/compare/main...grain/task-1",
    );

    // Back on the repo page, on the Branches tab the compare form is on:
    // the tab is in the path (docs/design/settings-panel.md §2.3).
    await user.click(screen.getByRole("button", { name: /acme\/other$/ }));
    expect(
      await screen.findByRole("heading", { name: "acme/other" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe(
        "/repos/acme/other/settings/branches",
      ),
    );
  });

  // Cold, from a link somebody sent: the page opens on the comparison
  // rather than on the repo it belongs to.
  it("opens a pull request link directly", async () => {
    setupApi();
    window.history.replaceState(
      null,
      "",
      "/repos/acme/other/compare/grain/task-1",
    );
    render(<App />);

    expect(await screen.findByLabelText("Branch changes")).toHaveTextContent(
      "the changed line",
    );
    expect(screen.getByText("Merged")).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe(
        "/repos/acme/other/compare/grain/task-1",
      ),
    );
  });

  // grain/task-351: a repo's page lists that repo's own CI runs, and a
  // run is read where run detail lives -- the CI page, which is a
  // section of the rail since grain/task-354, so this is a navigation
  // rather than a pane opening over the repo.
  it("opens a run over its repo page", async () => {
    setupApi();
    const run = {
      id: "9f2c1a",
      repo: "acme/other",
      ref: "grain/task-1",
      event: "push",
      startedAt: "2026-09-07T12:00:00Z",
      finishedAt: "2026-09-07T12:01:00Z",
      conclusion: "failure",
      jobs: [
        {
          workflow: "tests",
          workflowPath: "tests.yml",
          jobId: "test",
          name: "go test",
          conclusion: "failure",
          steps: [
            { name: "go test ./...", conclusion: "failure", exitCode: 1 },
          ],
        },
      ],
    };
    // The harness answers every other route; this test only has to say
    // that this repo has run something.
    const routed = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path.startsWith("/api/ci/runs?repo="))
        return Promise.resolve({ enabled: true, runs: [run] });
      if (path === "/api/ci/runs/9f2c1a") return Promise.resolve(run);
      return routed(path, opts);
    });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Repos/ }));
    await user.click(await screen.findByText("acme/other"));
    await user.click(
      await screen.findByRole("button", { name: "grain/task-1" }),
    );

    expect(await screen.findByText("go test")).toBeInTheDocument();
    // The repo remains the page being read underneath the transient run.
    await waitFor(() =>
      expect(window.location.pathname).toBe("/repos/acme/other"),
    );

    await user.click(screen.getByRole("button", { name: /Back/ }));
    expect(screen.getByText("CI workflows")).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/repos/acme/other"),
    );
  });

  // grain/task-1503: Back/Forward between two repo URLs swaps the repo
  // under a page that stays mounted. Nothing typed on the first repo's
  // page may survive onto the second, and an answer the first repo's
  // page asked for may not land on the second once it arrives late.
  it("starts a repo's page afresh when Back/Forward swaps the repo under it", async () => {
    setupApi();
    let answerStale;
    const routed = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/repos/acme/other/branches" && !opts)
        return new Promise((resolve) => {
          answerStale = resolve;
        });
      return routed(path, opts);
    });
    window.history.replaceState(null, "", "/repos/acme/other");
    const user = userEvent.setup();
    render(<App />);

    await screen.findByRole("heading", { name: "acme/other" });
    await user.type(
      await screen.findByLabelText(/Head branch/),
      "grain/task-1",
    );

    window.history.pushState(null, "", "/repos/acme/widgets");
    act(() => {
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await screen.findByRole("heading", { name: "acme/widgets" });
    await act(async () => {
      answerStale([
        {
          name: "stale-branch",
          status: "created",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ]);
    });

    expect(screen.getByLabelText(/Head branch/)).toHaveValue("");
    expect(screen.queryByText("stale-branch")).not.toBeInTheDocument();
  });
});
