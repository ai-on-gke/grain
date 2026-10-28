import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import PipelineControl from "./PipelineControl.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("PipelineControl", () => {
  afterEach(() => {
    api.mockReset();
    vi.unstubAllGlobals();
  });

  it("pauses new dispatch without offering to stop active runs", async () => {
    api.mockResolvedValueOnce({
      pause: {
        paused: true,
        reason: "host maintenance",
        since: "2026-09-11T20:00:00Z",
      },
      pausedNow: true,
    });
    const onChanged = vi.fn();
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{ paused: false }}
        onChanged={onChanged}
        showError={() => {}}
      />,
    );

    expect(
      screen.getByText(/already in flight to finish/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /stop|kill|cancel/i }),
    ).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/reason/i), "host maintenance");
    await user.click(screen.getByRole("button", { name: "Pause pipeline" }));

    expect(api).toHaveBeenCalledWith("/api/pipeline-pause", {
      method: "POST",
      body: JSON.stringify({ reason: "host maintenance" }),
    });
    expect(await screen.findByText(/pipeline is paused/i)).toBeInTheDocument();
    expect(screen.getByText(/runs already in flight/i)).toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("resumes dispatch", async () => {
    api.mockResolvedValueOnce({ pause: { paused: false }, resumed: true });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{ paused: true, reason: "maintenance" }}
        onChanged={onChanged}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Resume pipeline" }));

    expect(api).toHaveBeenCalledWith("/api/pipeline-pause", {
      method: "DELETE",
    });
    expect(await screen.findByText(/pipeline is running/i)).toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("warns that resuming during a scheduled upgrade hold cancels the upgrade", async () => {
    const confirm = vi
      .fn()
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    vi.stubGlobal("confirm", confirm);
    api.mockResolvedValueOnce({ pause: { paused: false }, resumed: true });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{
          paused: true,
          byKind: "automation",
          byId: "upgrade-schedule",
          until: "2026-09-12T04:00:00Z",
        }}
        onChanged={onChanged}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Resume pipeline" }));
    expect(confirm).toHaveBeenCalledWith(
      expect.stringMatching(/resuming cancels that upgrade/i),
    );
    expect(api).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Resume pipeline" }));
    expect(api).toHaveBeenCalledWith("/api/pipeline-pause", {
      method: "DELETE",
    });
    expect(await screen.findByText(/pipeline is running/i)).toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("says only Run now starts work under a pause it passes, and not under an upgrade's hold", () => {
    const { rerender } = render(
      <PipelineControl
        pause={{ paused: true, runNowPasses: true }}
        onChanged={() => {}}
        showError={() => {}}
      />,
    );
    expect(
      screen.getByText(/only tasks you Run now will start/),
    ).toBeInTheDocument();
    rerender(
      <PipelineControl
        pause={{ paused: true, runNowPasses: false }}
        onChanged={() => {}}
        showError={() => {}}
      />,
    );
    expect(screen.queryByText(/only tasks you Run now will start/)).toBeNull();
    expect(screen.getByText(/Run now waits too/)).toBeInTheDocument();
  });

  it("does nothing when a pause is declined", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{ paused: false }}
        onChanged={() => {}}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Pause pipeline" }));

    expect(api).not.toHaveBeenCalled();
    expect(screen.getByText(/pipeline is running/i)).toBeInTheDocument();
  });

  it("reports a failed pause and remains running", async () => {
    const error = new Error("pause failed");
    api.mockRejectedValueOnce(error);
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const showError = vi.fn();
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{ paused: false }}
        onChanged={() => {}}
        showError={showError}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Pause pipeline" }));

    expect(showError).toHaveBeenCalledWith(error);
    expect(screen.getByText(/pipeline is running/i)).toBeInTheDocument();
  });

  it("contacts running task agents from the pipeline control panel", async () => {
    api.mockResolvedValueOnce({ delivered_count: 1, task_ids: ["2"] });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <PipelineControl
        pause={{ paused: false }}
        tasks={[
          { id: "1", title: "Queued work", state: "queued" },
          { id: "2", title: "Running work", state: "running" },
          {
            id: "3",
            title: "Director",
            state: "running",
            consoleSession: "director",
          },
        ]}
        onChanged={onChanged}
        showError={() => {}}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Contact running agents (1)" }),
    );
    expect(screen.getByText("#2 Running work")).toBeInTheDocument();
    await user.type(
      screen.getByLabelText("Notice"),
      "Wrap up before maintenance",
    );
    await user.click(screen.getByRole("button", { name: "Send notice" }));

    expect(api).toHaveBeenCalledWith("/api/deployment/broadcast", {
      method: "POST",
      body: JSON.stringify({
        message: "Wrap up before maintenance",
        running_only: true,
      }),
    });
    expect(onChanged).toHaveBeenCalledTimes(1);
  });
});
