import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import TopPage from "./TopPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// grain/task-120: `top` on the daemon's own machine, as a tab of the
// System overlay -- the per-process answer to the "what is loading this
// machine" that Sandbox health's own host section can only ask.
describe("TopPage", () => {
  const snapshot = {
    enabled: true,
    lines: [
      "top - 12:00:00 up 3 days,  load average: 1.10, 0.90, 0.75",
      "    PID USER      PR  NI    VIRT    RES  S  %CPU  %MEM     TIME+ COMMAND",
      "    412 grain     20   0  1.2g   180m  S  87.5   9.1   3:20.11 claude",
    ],
  };

  afterEach(() => {
    api.mockReset();
  });

  it("shows the snapshot it fetched", async () => {
    api.mockResolvedValueOnce(snapshot);
    render(<TopPage showError={() => {}} />);

    expect(await screen.findByText(/load average: 1.10/)).toBeInTheDocument();
    expect(screen.getByText(/3:20.11 claude/)).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/host/top?lines=500");
  });

  it("shows a note instead of a pane when no snapshot is configured", async () => {
    api.mockResolvedValueOnce({ enabled: false });
    render(<TopPage showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Refresh" }),
    ).not.toBeInTheDocument();
  });

  it("re-fetches when Refresh is clicked", async () => {
    api.mockResolvedValueOnce(snapshot).mockResolvedValueOnce({
      enabled: true,
      lines: ["top - 12:00:05 up 3 days,  load average: 0.10, 0.90, 0.75"],
    });
    const user = userEvent.setup();
    render(<TopPage showError={() => {}} />);

    expect(await screen.findByText(/load average: 1.10/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh" }));

    expect(await screen.findByText(/load average: 0.10/)).toBeInTheDocument();
  });

  // The point of the switch: every poll re-sorts the table under whoever
  // is reading a row of it, so it has to be possible to hold one still.
  it("stops polling when auto-refresh is turned off", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    api.mockResolvedValue(snapshot);
    const user = userEvent.setup();
    render(<TopPage showError={() => {}} />);

    expect(await screen.findByText(/load average: 1.10/)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /auto-refresh/i }));
    const afterOff = api.mock.calls.length;
    await vi.advanceTimersByTimeAsync(20000);

    expect(api.mock.calls.length).toBe(afterOff);
    vi.useRealTimers();
  });

  it("reports a failed fetch through showError", async () => {
    const err = new Error("top: executable file not found in $PATH");
    api.mockRejectedValueOnce(err);
    const showError = vi.fn();
    render(<TopPage showError={showError} />);

    await vi.waitFor(() => expect(showError).toHaveBeenCalledWith(err));
  });
});

// A first read that failed used to leave the tab empty once the snackbar
// faded, and a run of failed polls raised a snackbar every five seconds.
describe("TopPage when its read fails", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("says so in place, and Refresh brings the table back", async () => {
    api.mockRejectedValueOnce(new Error("procps missing"));
    api.mockResolvedValueOnce({ enabled: true, lines: ["1 root systemd"] });
    const showError = vi.fn();
    render(<TopPage showError={showError} />);

    expect(
      await screen.findByText(/could not be read \(procps missing\)/),
    ).toBeInTheDocument();
    expect(showError).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(/1 root systemd/)).toBeInTheDocument();
    expect(screen.queryByText(/could not be read/)).not.toBeInTheDocument();
  });
});
