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

import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import LogsPage from "./LogsPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// jsdom lays nothing out, so a <pre> it renders is 0px tall with 0px of
// content and "scroll to the bottom" is indistinguishable from "stay at
// the top". These give one element the measurements of a pane showing
// 300px of a 900px log, so scrollTop can say which end it is at -- the
// same device AttemptTranscriptOverlay.test.jsx uses for its own pane.
function measure(el, { scrollHeight = 900, clientHeight = 300 } = {}) {
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    value: scrollHeight,
  });
  Object.defineProperty(el, "clientHeight", {
    configurable: true,
    value: clientHeight,
  });
}

// deferred hands the test the resolve of the lines fetch, so the pane can
// be measured after it has rendered but before any log lines land in it.
function deferred() {
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

describe("LogsPage", () => {
  afterEach(() => {
    api.mockReset();
  });

  // 2026-10-13 deployment-operations walkthrough: a failed GET /api/logs
  // left the tab blank once its snackbar had gone.
  it("says the logs could not be listed when GET /api/logs fails", async () => {
    api.mockRejectedValue(new Error("journal unreadable"));
    render(<LogsPage showError={() => {}} />);

    expect(
      await screen.findByText(
        /The logs could not be listed \(journal unreadable\)/,
      ),
    ).toBeInTheDocument();
  });

  it("shows a note instead of a source picker when no log sources are configured", async () => {
    api.mockResolvedValueOnce({ enabled: false });
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Source/)).not.toBeInTheDocument();
  });

  it("fetches and shows the selected source's log lines", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        sources: ["daemon", "git-proxy-audit"],
      })
      .mockResolvedValueOnce({ lines: ["one", "two"] });
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/one/)).toBeInTheDocument();
    expect(screen.getByText(/two/)).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/logs/daemon?lines=500");
  });

  it("opens on the daemon's log rather than the first source listed", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        sources: ["config-sync", "daemon", "dmesg", "git-proxy-audit"],
      })
      .mockResolvedValueOnce({ lines: ["daemon line"] });
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/daemon line/)).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/logs/daemon?lines=500");
  });

  it("falls back to the first source when there is no daemon log", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        sources: ["config-sync", "dmesg"],
      })
      .mockResolvedValueOnce({ lines: ["sync line"] });
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/sync line/)).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/logs/config-sync?lines=500");
  });

  it("opens at the end of the log rather than the top", async () => {
    const lines = deferred();
    api
      .mockResolvedValueOnce({ enabled: true, sources: ["daemon"] })
      .mockReturnValueOnce(lines.promise);
    render(<LogsPage showError={() => {}} />);

    const view = await screen.findByText("(no log lines)");
    measure(view);
    await act(async () => {
      lines.resolve({ lines: ["first line", "last line"] });
    });

    expect(screen.getByText(/last line/)).toBeInTheDocument();
    expect(view.scrollTop).toBe(900);
  });

  it("stops following the tail once the reader scrolls up", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, sources: ["daemon"] })
      .mockResolvedValueOnce({ lines: ["first line", "last line"] })
      .mockResolvedValueOnce({
        lines: ["first line", "last line", "and more"],
      });
    const user = userEvent.setup();
    render(<LogsPage showError={() => {}} />);

    const view = await screen.findByText(/last line/);
    measure(view);
    // Back to the top to read something, then let the next read bring in
    // newer lines.
    view.scrollTop = 0;
    fireEvent.scroll(view);
    await user.click(screen.getByRole("button", { name: "Refresh" }));

    expect(await screen.findByText(/and more/)).toBeInTheDocument();
    expect(view.scrollTop).toBe(0);
  });

  it("re-fetches the selected source when Refresh is clicked", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, sources: ["daemon"] })
      .mockResolvedValueOnce({ lines: ["first"] })
      .mockResolvedValueOnce({ lines: ["first", "second"] });
    const user = userEvent.setup();
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/first/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh" }));

    expect(await screen.findByText(/second/)).toBeInTheDocument();
  });

  it("switches sources from the picker and re-fetches", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        sources: ["daemon", "git-proxy-audit"],
      })
      .mockResolvedValueOnce({ lines: ["daemon line"] })
      .mockResolvedValueOnce({ lines: ["audit line"] });
    const user = userEvent.setup();
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/daemon line/)).toBeInTheDocument();

    await user.click(screen.getByLabelText(/Source/));
    await user.click(
      await screen.findByRole("option", { name: "git-proxy-audit" }),
    );

    expect(await screen.findByText(/audit line/)).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/logs/git-proxy-audit?lines=500");
  });

  it("filters the visible tail without changing the selected source", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, sources: ["daemon"] })
      .mockResolvedValueOnce({
        lines: ["dispatch: started task 42", "github: synced pull requests"],
      });
    const user = userEvent.setup();
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText(/started task 42/)).toBeInTheDocument();
    await user.type(screen.getByLabelText("Search logs"), "GITHUB");

    expect(screen.queryByText(/started task 42/)).not.toBeInTheDocument();
    expect(screen.getByText(/synced pull requests/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Search logs"));
    await user.type(screen.getByLabelText("Search logs"), "nothing here");
    expect(screen.getByText("(no matching log lines)")).toBeInTheDocument();
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("shows a placeholder when the source has no lines", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, sources: ["daemon"] })
      .mockResolvedValueOnce({ lines: [] });
    render(<LogsPage showError={() => {}} />);

    expect(await screen.findByText("(no log lines)")).toBeInTheDocument();
  });

  it("drops a slow fetch for a source the reader has already left", async () => {
    const slow = deferred();
    api.mockImplementation((path) => {
      if (path === "/api/logs") {
        return Promise.resolve({
          enabled: true,
          sources: ["daemon", "dmesg"],
        });
      }
      if (path.startsWith("/api/logs/daemon")) return slow.promise;
      return Promise.resolve({ lines: ["kernel line"] });
    });
    const user = userEvent.setup();
    render(<LogsPage showError={() => {}} />);

    await screen.findByRole("combobox");
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "dmesg" }));
    expect(await screen.findByText("kernel line")).toBeInTheDocument();

    await act(async () => {
      slow.resolve({ lines: ["daemon line"] });
    });
    expect(screen.getByText("kernel line")).toBeInTheDocument();
    expect(screen.queryByText("daemon line")).not.toBeInTheDocument();
  });
});
