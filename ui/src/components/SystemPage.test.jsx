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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { collectAlerts } from "../alerts.js";
import api from "../api.js";
import SystemPage, { SYSTEM_TABS } from "./SystemPage.jsx";

vi.mock("../api.js", () => ({ default: vi.fn(() => Promise.resolve({})) }));

// docs/design/settings-panel.md §2.1: status and operations only.
describe("SystemPage", () => {
  afterEach(() => api.mockClear());

  it("has the design's tabs, in order, and opens on Health", () => {
    render(
      <SystemPage config={{}} tab="" onTab={() => {}} showError={vi.fn()} />,
    );
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Health",
      "Logs",
      "Sandboxes",
      "Caches",
      "Top",
      "Pipeline",
      "Metrics",
      "Power",
    ]);
    expect(SYSTEM_TABS.map((t) => t.id)[0]).toBe("health");
    expect(screen.getByRole("tab", { name: "Health" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByTestId("system-health")).toBeInTheDocument();
  });

  it("reports a picked tab, and Health as the bare path", async () => {
    const onTab = vi.fn();
    const user = userEvent.setup();
    render(
      <SystemPage config={{}} tab="logs" onTab={onTab} showError={vi.fn()} />,
    );
    await user.click(screen.getByRole("tab", { name: "Pipeline" }));
    expect(onTab).toHaveBeenCalledWith("pipeline");
    await user.click(screen.getByRole("tab", { name: "Health" }));
    expect(onTab).toHaveBeenCalledWith("");
  });

  it("lists what Health says is wrong, with a way to each", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/settings" ? { pendingRestart: ["githubHost"] } : {},
      ),
    );
    const onOpenSettings = vi.fn();
    const user = userEvent.setup();
    const failureConfig = {
      stateRepoResetNotice: true,
      stateRepoSyncFailure: {
        since: "2026-10-06T08:57:32Z",
        error: "boom",
        repo: "grain/config",
      },
    };
    render(
      <SystemPage
        config={failureConfig}
        alerts={collectAlerts(failureConfig)}
        tab=""
        onTab={() => {}}
        showError={vi.fn()}
        onOpenSettings={onOpenSettings}
      />,
    );
    expect(
      await screen.findByTestId("health-pending-restart"),
    ).toHaveTextContent("Saved, not applied until restart: GitHub host.");
    expect(screen.getByTestId("health-config-repo")).toHaveTextContent(
      "A merged change reset settings on this deployment.",
    );
    await user.click(
      within(screen.getByTestId("health-config-repo")).getByRole("button", {
        name: "Config repo",
      }),
    );
    expect(onOpenSettings).toHaveBeenCalledWith("config-repo");
    onOpenSettings.mockClear();
    await user.click(
      within(screen.getByTestId("alert-row")).getByRole("button", {
        name: "Config repo",
      }),
    );
    expect(onOpenSettings).toHaveBeenCalledWith("config-repo");
  });

  it("opens Config repo when clicking Config repo on a failing state-repo sync in Health", async () => {
    const onOpenSettings = vi.fn();
    const user = userEvent.setup();
    const config = {
      stateRepoSyncFailure: {
        since: "2026-10-22T09:15:00Z",
        error: "push rejected",
        repo: "acme/grain-config",
      },
    };
    render(
      <SystemPage
        config={config}
        alerts={collectAlerts(config)}
        tab=""
        onTab={() => {}}
        showError={vi.fn()}
        onOpenSettings={onOpenSettings}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Config repo" }));
    expect(onOpenSettings).toHaveBeenCalledWith("config-repo");
  });

  it("points ConnectedExecutors on Sandboxes to Settings → Executors and surfaces a load error instead of a false empty state", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/executors") {
        return Promise.reject(new Error("executor registry offline"));
      }
      return Promise.resolve({
        enabled: true,
        sandboxes: [],
        host: null,
      });
    });
    render(
      <SystemPage
        config={{}}
        tab="sandboxes"
        onTab={() => {}}
        showError={vi.fn()}
      />,
    );
    expect(
      await screen.findByText(
        /The tokens they connect with are on Settings → Executors\./,
      ),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        /Connected executors could not be read \(executor registry offline\)\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("No remote executors connected."),
    ).not.toBeInTheDocument();
  });

  // Upgrade moved into Settings (/settings/upgrade): System no longer
  // offers it, on the tab strip or a phone's list of tabs.
  it("leaves Upgrade to Settings", () => {
    render(
      <SystemPage config={{}} tab="" onTab={() => {}} showError={vi.fn()} />,
    );
    expect(screen.queryByRole("tab", { name: "Upgrade" })).toBeNull();
    expect(SYSTEM_TABS.some((t) => t.id === "upgrade")).toBe(false);
  });

  it("offers only the power actions the deployment enables", () => {
    render(
      <SystemPage
        config={{ restartEnabled: true }}
        tab="power"
        onTab={() => {}}
        showError={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Restart grain" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reboot host" })).toBeNull();
  });

  it("shows overcommit mode, effective factors, recent adjustments, and OOM kills on Health", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/settings") {
        return Promise.resolve({
          pendingRestart: [],
          unrunnable: [],
          sandboxCommitment: {
            budget: {
              mode: "auto",
              cpuOvercommit: 2.75,
              memoryOvercommit: 1.25,
              overcommit: {
                mode: "auto",
                cpuOvercommit: 2.75,
                memoryOvercommit: 1.25,
                oomKills: 1,
                adjustments: [
                  {
                    at: "2026-10-24T09:10:00Z",
                    dimension: "cpu",
                    from: 2.5,
                    to: 2.75,
                    reason: "CPU pressure calm and pool saturated",
                  },
                ],
              },
            },
          },
        });
      }
      return Promise.resolve({});
    });
    render(
      <SystemPage
        config={{}}
        tab="health"
        onTab={() => {}}
        showError={vi.fn()}
      />,
    );
    const status = await screen.findByTestId("sandbox-overcommit-status");
    expect(status).toHaveTextContent("Overcommit: auto");
    expect(status).toHaveTextContent("1 OOM kill");
    expect(status).toHaveTextContent(
      /Effective factors: CPU 2\.75× · memory 1\.25×/,
    );
    expect(screen.getByTestId("overcommit-adjustments")).toHaveTextContent(
      /CPU 2\.50× → 2\.75× — CPU pressure calm and pool saturated/,
    );
  });
});
