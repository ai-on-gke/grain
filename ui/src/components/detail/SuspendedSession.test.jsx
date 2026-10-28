import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay suspended session", () => {
  afterEach(resetDetailMocks);

  // grain/task-125: the conversation this task's last run left
  // suspended, which its next run resumes. The token is the point of
  // the section -- it is what an operator pastes into `claude --resume`
  // to read what the run was actually thinking -- so it is shown rather
  // than alluded to, and a task with nothing suspended gets no section
  // at all.
  it("shows the token that reopens a suspended session", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          suspendedSession: {
            token: "sess-abc123",
            framework: "claude",
            runId: "r7",
            at: "2026-02-01T10:00:00Z",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Suspended session")).toBeInTheDocument();
    expect(screen.getByText("sess-abc123")).toBeInTheDocument();
    expect(screen.getByText(/resumes this conversation/)).toBeInTheDocument();
    expect(screen.getByText(/r7/)).toBeInTheDocument();
  });

  it("says nothing about a suspended session on a task with none", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.queryByText("Suspended session")).not.toBeInTheDocument();
  });
});
