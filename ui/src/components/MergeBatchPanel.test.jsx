import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import MergeBatchPanel, {
  batchAge,
  batchStanding,
} from "./MergeBatchPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const STARTED = "2026-09-19T12:00:00Z";
const NOW = new Date("2026-09-19T12:22:00Z").getTime();

// batch is one speculative merge in flight, as GET /api/merge-batches
// reports one: a throwaway ref, the tree being judged, the single run
// judging it, and the members in the queue's own order.
function batch(over = {}) {
  return {
    repo: "acme/widgets",
    branch: "main",
    ref: "grain-merge-queue/main",
    baseCommit: "base456789abcdef",
    commit: "abc123def4567890",
    runId: "run-42",
    startedAt: STARTED,
    running: true,
    members: [
      {
        task: "1",
        title: "Fix the parser",
        number: 11,
        headRef: "grain/task-1",
        head: "aaa111222333",
      },
      {
        task: "2",
        title: "Rename the thing",
        number: 12,
        headRef: "grain/task-2",
        head: "bbb111222333",
      },
      {
        task: "3",
        title: "Add the flag",
        number: 13,
        headRef: "grain/task-3",
        head: "ccc111222333",
      },
    ],
    ...over,
  };
}

function answer(over = {}) {
  return { batches: [batch()], maxAgeSeconds: 5400, ...over };
}

function renderPanel(over = {}) {
  const props = {
    repo: "acme/widgets",
    onOpenRun: vi.fn(),
    onOpenTask: vi.fn(),
    showError: vi.fn(),
    ...over,
  };
  render(<MergeBatchPanel {...props} />);
  return props;
}

describe("MergeBatchPanel", () => {
  afterEach(() => {
    api.mockReset();
    vi.useRealTimers();
  });

  // The whole point: three rows that each say "testing with the rest of
  // the batch" become one batch with three named members in it.
  it("names the members of one batch, in the queue's own order", async () => {
    api.mockResolvedValue(answer());
    renderPanel();

    expect(api).toHaveBeenCalledWith("/api/merge-batches?repo=acme%2Fwidgets");
    expect(await screen.findByText("3 changes on main")).toBeInTheDocument();
    const members = screen.getAllByRole("listitem");
    expect(members.map((li) => li.textContent)).toEqual([
      expect.stringContaining("Fix the parser"),
      expect.stringContaining("Rename the thing"),
      expect.stringContaining("Add the flag"),
    ]);
    // Which end a failure is halved towards, said on the member it is
    // about rather than left to be inferred from the order.
    expect(members[0].textContent).toMatch(/first/);
    expect(members[1].textContent).not.toMatch(/first/);
  });

  // The tree being judged is neither the base branch nor any member's
  // head, and nobody's pull request points at it.
  it("shows the throwaway ref and the commit the run is judging", async () => {
    api.mockResolvedValue(answer());
    renderPanel();

    expect(
      await screen.findByText("grain-merge-queue/main at abc123def456"),
    ).toBeInTheDocument();
  });

  it("links the whole batch to the one run deciding it", async () => {
    api.mockResolvedValue(answer());
    const { onOpenRun } = renderPanel();

    await userEvent.click(
      await screen.findByRole("button", { name: "Show the run" }),
    );
    expect(onOpenRun).toHaveBeenCalledWith("run-42");
  });

  it("opens a member's own task", async () => {
    api.mockResolvedValue(answer());
    const { onOpenTask } = renderPanel();

    const member = await screen.findByRole("link", {
      name: "Rename the thing",
    });
    // Its own address, for a middle click or "copy link"; a plain click
    // is taken over and opens the task in place.
    expect(member).toHaveAttribute("href", "/tasks/2");
    await userEvent.click(member);
    expect(onOpenTask).toHaveBeenCalledWith("2");
  });

  // Said rather than left blank: a section that appears only when
  // something is happening is one nobody knows to look for.
  it("says so when a repository has no batch in flight", async () => {
    api.mockResolvedValue({ batches: [], maxAgeSeconds: 5400 });
    renderPanel();

    expect(
      await screen.findByText(/No batch is in flight/),
    ).toBeInTheDocument();
  });

  // On a task's own page the panel is that task's batch or nothing at
  // all, and it marks which member the reader is looking at.
  it("on a member's page shows only that member's batch", async () => {
    api.mockResolvedValue({
      batches: [batch(), batch({ branch: "release", members: [] })],
      maxAgeSeconds: 5400,
    });
    renderPanel({ taskId: "2" });

    expect(await screen.findByText("Merge batch")).toBeInTheDocument();
    expect(screen.queryByText(/changes on release/)).toBeNull();
    expect(screen.getByText("Rename the thing")).toBeInTheDocument();
  });

  it("draws nothing on the page of a task in no batch", async () => {
    api.mockResolvedValue(answer());
    const { container } = render(
      <MergeBatchPanel repo="acme/widgets" taskId="99" showError={vi.fn()} />,
    );

    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});

describe("batchAge", () => {
  // "22m" says nothing on its own -- a full suite here is 20-45 minutes
  // -- and the bound comes from the API so that changing it changes this.
  it("counts a running batch against the bound it will be abandoned at", () => {
    expect(batchAge(batch(), 5400, NOW)).toBe("22m of 90m");
  });

  it("counts a finished batch to the moment it reported", () => {
    const done = batch({
      running: false,
      finishedAt: "2026-09-19T12:30:00Z",
      conclusion: "success",
    });
    // Not "30m of 90m": the bound has stopped meaning anything, and the
    // elapsed time is now how long the answer took rather than how much
    // rope is left.
    expect(batchAge(done, 5400, NOW)).toBe("ran for 30m");
  });
});

describe("batchStanding", () => {
  it("tells a broken combination from a run that said nothing", () => {
    expect(
      batchStanding(batch({ running: false, conclusion: "failure" })).label,
    ).toBe("Failed");
    // Errored and cancelled are not verdicts about the tree, so no
    // culprit is read out of them and nobody is blamed.
    expect(
      batchStanding(batch({ running: false, conclusion: "errored" })).label,
    ).toBe("Abandoned");
    expect(
      batchStanding(batch({ running: false, conclusion: "success" })).label,
    ).toBe("Passed");
    expect(batchStanding(batch()).label).toBe("Testing");
  });
});
