import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import NotifyRunningDialog from "./NotifyRunningDialog.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("NotifyRunningDialog", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("shows when no task agents are currently running and disables Send notice", () => {
    render(
      <NotifyRunningDialog
        open={true}
        onClose={() => {}}
        runningTasks={[]}
        onSent={() => {}}
        showError={() => {}}
      />,
    );

    expect(screen.getByTestId("notify-running-summary")).toHaveTextContent(
      "No task agents are currently running.",
    );
    expect(screen.getByRole("button", { name: "Send notice" })).toBeDisabled();
  });

  it("lists running task agents and posts a running_only broadcast", async () => {
    api.mockResolvedValueOnce({ delivered_count: 2, task_ids: ["2", "5"] });
    const onClose = vi.fn();
    const onSent = vi.fn();
    const user = userEvent.setup();

    render(
      <NotifyRunningDialog
        open={true}
        onClose={onClose}
        runningTasks={[
          { id: "2", title: "Ship the other thing", state: "running" },
          { id: "5", title: "Fix header layout", state: "running" },
        ]}
        onSent={onSent}
        showError={() => {}}
      />,
    );

    expect(screen.getByTestId("notify-running-summary")).toHaveTextContent(
      "2 running task agents will receive this notice:",
    );
    expect(screen.getByText("#2 Ship the other thing")).toBeInTheDocument();
    expect(screen.getByText("#5 Fix header layout")).toBeInTheDocument();

    const sendBtn = screen.getByRole("button", { name: "Send notice" });
    expect(sendBtn).toBeDisabled();

    await user.type(
      screen.getByLabelText("Notice"),
      "  Hold off on pushing while main is frozen  ",
    );
    expect(sendBtn).toBeEnabled();

    await user.click(sendBtn);

    expect(api).toHaveBeenCalledWith("/api/deployment/broadcast", {
      method: "POST",
      body: JSON.stringify({
        message: "Hold off on pushing while main is frozen",
        running_only: true,
      }),
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSent).toHaveBeenCalledWith({
      delivered_count: 2,
      task_ids: ["2", "5"],
    });
    expect(
      await screen.findByText("Notice sent to 2 running task agents."),
    ).toBeInTheDocument();
  });

  it("reports API errors via showError and keeps the dialog open", async () => {
    const err = new Error("broadcast failed");
    api.mockRejectedValueOnce(err);
    const onClose = vi.fn();
    const showError = vi.fn();
    const user = userEvent.setup();

    render(
      <NotifyRunningDialog
        open={true}
        onClose={onClose}
        runningTasks={[{ id: "2", title: "Ship it", state: "running" }]}
        onSent={() => {}}
        showError={showError}
      />,
    );

    await user.type(screen.getByLabelText("Notice"), "Check logs");
    await user.click(screen.getByRole("button", { name: "Send notice" }));

    expect(showError).toHaveBeenCalledWith(err);
    expect(onClose).not.toHaveBeenCalled();
  });
});
