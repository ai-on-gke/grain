import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay live attempt transcript", () => {
  afterEach(resetDetailMocks);

  it("renders embedded live transcript immediately for running task with active attempt", async () => {
    api.mockResolvedValue({ transcript: "agent is thinking...\nran command" });
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Live Attempt #1 Transcript")).toBeInTheDocument();
    expect(
      await screen.findByText(/agent is thinking\.\.\./),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/attempts/1/transcript");
  });

  it("displays placeholder when running task active attempt has no output yet", async () => {
    api.mockResolvedValue({ transcript: "" });
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          attempts: [
            {
              number: 2,
              startedAt: "2026-08-28T12:00:00Z",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Live Attempt #2 Transcript")).toBeInTheDocument();
    expect(
      await screen.findByText("Still running -- nothing written yet."),
    ).toBeInTheDocument();
  });

  it("does not render embedded live transcript for queued or completed task", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "queued",
          attempts: [],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.queryByText(/Live Attempt #\d+ Transcript/),
    ).not.toBeInTheDocument();
  });
});
