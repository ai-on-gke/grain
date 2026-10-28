import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import RepoRuns from "./RepoRuns.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const STARTED = "2026-09-07T12:00:00Z";

// run is one finished run of this repo's, as GET /api/ci/runs?repo=
// summarizes one: jobs without their steps' output, which is all a
// listing shows.
function run(over = {}) {
  return {
    id: "9f2c1a",
    repo: "acme/widgets",
    ref: "grain/task-1",
    commit: "0123456789abcdef0123",
    event: "push",
    startedAt: STARTED,
    finishedAt: "2026-09-07T12:01:23Z",
    conclusion: "failure",
    jobs: [
      { workflow: "tests", jobId: "lint", conclusion: "success" },
      { workflow: "tests", jobId: "test", conclusion: "failure" },
    ],
    ...over,
  };
}

function renderRuns(over = {}) {
  const props = {
    repo: "acme/widgets",
    onOpenRun: vi.fn(),
    showError: vi.fn(),
    ...over,
  };
  render(<RepoRuns {...props} />);
  return props;
}

describe("RepoRuns", () => {
  afterEach(() => {
    api.mockReset();
    vi.useRealTimers();
  });

  it("lists this repo's own runs, asked for as this repo's", async () => {
    api.mockResolvedValue({ enabled: true, runs: [run()] });
    const { onOpenRun } = renderRuns();

    expect(api).toHaveBeenCalledWith("/api/ci/runs?repo=acme%2Fwidgets");
    // Conclusion, branch, commit and what the jobs did -- the row's
    // whole answer to "did that workflow work".
    expect(await screen.findByText("Failure")).toBeInTheDocument();
    expect(screen.getByText("0123456789ab")).toHaveAttribute(
      "title",
      "0123456789abcdef0123",
    );
    expect(screen.getByText("1m 23s")).toBeInTheDocument();
    expect(screen.getByText(/1 failure/)).toBeInTheDocument();
    // No column repeating the page's own subject in every row -- so the
    // branch is what opens the run.
    expect(screen.queryByRole("columnheader", { name: "Repo" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "grain/task-1" }));
    expect(onOpenRun).toHaveBeenCalledWith("9f2c1a", "acme/widgets");
  });

  it("shows the most recent few and sends the reader to the rest", async () => {
    const runs = Array.from({ length: 7 }, (_, i) =>
      run({ id: `r${i}`, ref: `grain/task-${i}` }),
    );
    api.mockResolvedValue({ enabled: true, runs });
    const { onOpenRun } = renderRuns();

    // The daemon's own order, kept: newest first, oldest dropped.
    expect(await screen.findByText("grain/task-0")).toBeInTheDocument();
    expect(screen.queryByText("grain/task-5")).toBeNull();

    await userEvent.click(
      screen.getByRole("button", { name: "All 7 runs for this repo" }),
    );
    // No run named: the pane opens on its listing, narrowed to here.
    expect(onOpenRun).toHaveBeenCalledWith(null, "acme/widgets");
  });

  it("says the deployment runs no CI, beside the workflows it would run", async () => {
    api.mockResolvedValue({ enabled: false });
    renderRuns();

    expect(await screen.findByText(/-ci=false/)).toBeInTheDocument();
    // Nothing to start, and no empty table to read as "nothing has run".
    expect(screen.queryByRole("button", { name: "Start a run" })).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says nothing has run here yet, and what turning it on takes", async () => {
    api.mockResolvedValue({ enabled: true, runs: [] });
    renderRuns();

    expect(
      await screen.findByText(/No CI runs for acme\/widgets yet/),
    ).toBeInTheDocument();
    expect(screen.getByText(/grain-ci capability/)).toBeInTheDocument();
  });

  it("starts a run against this repo without asking which repo", async () => {
    const started = { id: "new1", repo: "acme/widgets", ref: "main" };
    api.mockImplementation((path, opts) => {
      if (opts?.method === "POST") return Promise.resolve(started);
      if (path.startsWith("/api/ci/workflows?"))
        return Promise.resolve({
          workflows: [{ path: ".github/workflows/ci.yml", name: "CI" }],
        });
      return Promise.resolve({ enabled: true, runs: [] });
    });
    const { onOpenRun } = renderRuns();

    await userEvent.click(
      await screen.findByRole("button", { name: "Start a run" }),
    );
    // The repo is the page's, so the form is a branch and a button.
    expect(screen.getByText("Repo: acme/widgets")).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Repo$/)).toBeNull();

    await userEvent.type(screen.getByLabelText("Branch"), "main");
    await userEvent.click(
      screen.getByRole("button", { name: "Load workflows" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify({
          repo: "acme/widgets",
          branch: "main",
          workflow: ".github/workflows/ci.yml",
        }),
      }),
    );
    // Straight onto the run that was just asked for: it is the reason
    // the button was pressed.
    expect(onOpenRun).toHaveBeenCalledWith("new1", "acme/widgets");
  });

  it("reports a listing that could not be read rather than showing an empty one", async () => {
    api.mockRejectedValue(new Error("no such repo"));
    const { showError } = renderRuns();

    await waitFor(() => expect(showError).toHaveBeenCalled());
    expect(screen.queryByRole("table")).toBeNull();
  });
});
