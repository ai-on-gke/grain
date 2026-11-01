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

// The two views that are not a view of the tasks
// (docs/design/settings-panel.md): Settings, every deployment setting,
// this browser's preferences and Memory, opened from the gear; and
// System, status and operations (Health, Logs, Sandboxes, Caches, Top,
// Pipeline, Metrics, Upgrade, Power), opened from the icon beside it.
//
// Part of the App suite -- see App.test.jsx for why it is six files.
// This file is the one that pays most for the split: opening these
// three brings in more of MUI than the rest of the app put together.

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import {
  config,
  initialTasks,
  resetAppTestState,
  setupApi,
} from "./appTestHarness.js";
import api from "./api.js";

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

  it("opens Settings from the gear and System from the icon beside it", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByTestId("top-bar-settings"));
    expect(
      await screen.findByRole("heading", { name: "Models", level: 1 }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/models"),
    );
    expect(screen.getByTestId("top-bar-settings")).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    await user.click(screen.getByTestId("top-bar-system"));
    expect(await screen.findByTestId("system-health")).toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: "Health", selected: true }),
    ).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/system"));
  });

  // Upgrade is a System tab, and the secrets sit with what uses them on
  // Settings → Credentials (grain/task-110).
  it("keeps Upgrade in Settings, out of System, and the secrets in Settings → Credentials", async () => {
    setupApi();
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/settings/credentials");
    render(<App />);

    expect(await screen.findByText("Other secrets")).toBeInTheDocument();
    expect(
      screen.getByText(
        /this UI was not started with a local secrets directory/i,
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByTestId("settings-nav-upgrade"));
    expect(
      await screen.findByText(/neither -upgrade-image nor -upgrade-src-dir/i),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings/upgrade");
    // The upgrade-check settings are in Settings → General.
    expect(
      screen.queryByRole("combobox", { name: /Check the upgrade/ }),
    ).toBeNull();

    await user.click(screen.getByTestId("top-bar-system"));
    expect(await screen.findByRole("tab", { name: "Health" })).toBeVisible();
    expect(screen.queryByRole("tab", { name: "Upgrade" })).toBeNull();
  });

  // Upgrade was a System tab: a bookmark or a notification's link to
  // /system/upgrade, anchor and all, lands on Settings → Upgrade, and
  // the address is corrected in place rather than left behind on Back.
  it("redirects the old /system/upgrade to Settings → Upgrade", async () => {
    setupApi();
    window.history.replaceState(null, "", "/system/upgrade#upgrade");
    render(<App />);

    expect(
      await screen.findByText(/neither -upgrade-image nor -upgrade-src-dir/i),
    ).toBeInTheDocument();
    expect(screen.getByTestId("settings-section")).toHaveAttribute(
      "data-section",
      "upgrade",
    );
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/upgrade"),
    );
    expect(window.location.hash).toBe("#upgrade");
  });

  // grain/task-201: Settings lists the declarations and says where to
  // change them, and one thing about a declaration is not in the
  // declaration -- how many tasks are still carrying a value it has
  // moved on from. Retiring an enum option leaves them exactly where
  // they are (model.NormalizeCustomFieldValuesFrom), so the pane that
  // shows the option is gone is where "and this is what it cost" goes.
  it("counts the tasks a declaration stranded, and opens them", async () => {
    setupApi([
      { ...initialTasks[0], customFields: { priority: "critical" } },
      { ...initialTasks[1], customFields: { priority: "high" } },
    ]);
    const inner = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            customFields: [
              {
                name: "priority",
                label: "Priority",
                type: "enum",
                // "critical" has been retired since task 1 chose it.
                options: ["low", "high"],
              },
            ],
          })
        : inner(path, opts),
    );
    window.history.replaceState(null, "", "/admin/workflow");
    const user = userEvent.setup();
    render(<App />);
    const note = await screen.findByText(
      "1 task still carries a value this field no longer offers",
    );

    // And the count is a way in: the list, narrowed to exactly those
    // tasks, with the pane out of the way.
    await user.click(note);

    expect(screen.queryByTestId("settings-page")).not.toBeInTheDocument();
    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText("Add feature")).not.toBeInTheDocument();
  });

  // Memory is a Settings section now (§3.2), and /memory goes there.
  it("opens Memory at /settings/memory, and redirects /memory there", async () => {
    setupApi();
    window.history.replaceState(null, "", "/memory");
    render(<App />);

    expect(await screen.findByTestId("memory-page")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Memory", level: 1 }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/memory"),
    );
    expect(api).toHaveBeenCalledWith("/api/memory");
  });

  it("shows Logs, Sandboxes and Caches as System tabs, and sends their old paths there", async () => {
    setupApi();
    window.history.replaceState(null, "", "/caches");
    render(<App />);

    expect(
      await screen.findByRole("tab", { name: "Caches", selected: true }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/system/caches"),
    );
    expect(screen.getByRole("tab", { name: "Logs" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Sandboxes" })).toBeInTheDocument();
    // No settings tab is left in System.
    expect(
      screen.queryByRole("tab", { name: "General & Deployment" }),
    ).not.toBeInTheDocument();
  });

  // grain/task-1359: the picked tab and section are in the address bar,
  // and Back steps to the one before.
  it("keeps the picked System tab in the URL, and Back returns to the previous tab", async () => {
    setupApi();
    window.history.replaceState(null, "", "/admin/logs");
    const user = userEvent.setup();
    render(<App />);

    expect(
      await screen.findByRole("tab", { name: "Logs", selected: true }),
    ).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/system/logs"));

    await user.click(screen.getByRole("tab", { name: "Pipeline" }));
    await waitFor(() =>
      expect(window.location.pathname).toBe("/system/pipeline"),
    );

    window.history.back();
    expect(
      await screen.findByRole("tab", { name: "Logs", selected: true }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/system/logs");
  });

  it("keeps the picked Settings section in the URL, and Back returns to it", async () => {
    setupApi();
    window.history.replaceState(null, "", "/admin/engine");
    const user = userEvent.setup();
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Engine", level: 1 }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/engine"),
    );

    await user.click(screen.getByTestId("settings-nav-ci"));
    expect(
      await screen.findByRole("heading", { name: "CI", level: 1 }),
    ).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/settings/ci"));

    window.history.back();
    expect(
      await screen.findByRole("heading", { name: "Engine", level: 1 }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings/engine");
  });

  // grain/task-379: the pane takes a scope from the URL, which is how a
  // throughput panel's heading opens the report that panel was drawing.
  it("opens the report the URL scopes it to", async () => {
    setupApi();
    window.history.replaceState(null, "", "/metrics?f=repo:any:acme/widgets");
    render(<App />);

    expect(
      await screen.findByRole("tab", { name: "Metrics", selected: true }),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(
      "/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24",
    );
    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/system/metrics?f=repo:any:acme/widgets",
      ),
    );
  });

  // ...and the tab itself is the deployment's whole report, whatever
  // the list behind it has been narrowed to.
  it("opens the deployment's own report from the tab, over a narrowed list", async () => {
    setupApi();
    window.history.replaceState(null, "", "/?f=repo:any:acme/widgets");
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByTestId("top-bar-system"));
    await user.click(await screen.findByRole("tab", { name: "Metrics" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/metrics?window=7d&buckets=24"),
    );
    expect(api).not.toHaveBeenCalledWith(
      "/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24",
    );
  });

  // The metrics report's backlog names the oldest queued task, and the
  // useful thing to do with that is go and look at it, over the report.
  it(
    "opens the oldest queued task from the Metrics tab",
    { timeout: 60000 },
    async () => {
      setupApi();
      const user = userEvent.setup();
      window.history.replaceState(null, "", "/system/metrics");
      render(<App />);
      await screen.findByText("task 1");

      await user.click(screen.getByRole("link", { name: "task 1" }));

      expect(
        await screen.findByRole("heading", { name: /Fix bug/ }),
      ).toHaveTextContent("1 Fix bug");
    },
  );

  // grain/task-1359: opening that task is one step in the history, so
  // Back returns to the report rather than to the bare list that used to
  // flash between them.
  it(
    "returns to Metrics on Back from a task opened in the Metrics tab",
    { timeout: 60000 },
    async () => {
      setupApi();
      const pushes = vi.spyOn(window.history, "pushState");
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");

      await user.click(screen.getByTestId("top-bar-system"));
      await user.click(await screen.findByRole("tab", { name: "Metrics" }));
      await waitFor(() =>
        expect(window.location.pathname).toBe("/system/metrics"),
      );
      pushes.mockClear();
      await screen.findByText("task 1");
      await user.click(screen.getByRole("link", { name: "task 1" }));
      await screen.findByRole("heading", { name: /Fix bug/ });

      await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));
      expect(pushes.mock.calls.map((call) => call[2])).toEqual(["/tasks/1"]);
      pushes.mockRestore();

      window.history.back();
      expect(
        await screen.findByRole("tab", { name: "Metrics", selected: true }),
      ).toBeInTheDocument();
      expect(window.location.pathname).toBe("/system/metrics");
    },
  );

  it(
    "asks before leaving an unsaved Settings edit via the top-bar System or Settings buttons",
    { timeout: 60000 },
    async () => {
      setupApi();
      window.history.replaceState(null, "", "/settings/agents");
      const user = userEvent.setup();
      render(<App />);

      const box = await screen.findByLabelText(
        "Standing instructions for every run",
      );
      await user.type(box, " Always run tests.");

      const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
      await user.click(screen.getByTestId("top-bar-system"));
      expect(confirm).toHaveBeenCalled();
      expect(window.location.pathname).toBe("/settings/agents");

      confirm.mockReturnValueOnce(false);
      await user.click(screen.getByTestId("top-bar-settings"));
      expect(confirm).toHaveBeenCalledTimes(2);
      expect(window.location.pathname).toBe("/settings/agents");

      confirm.mockReturnValueOnce(true);
      await user.click(screen.getByTestId("top-bar-system"));
      await waitFor(() => expect(window.location.pathname).toBe("/system"));
      confirm.mockRestore();
    },
  );

  it(
    "reloads open notifications immediately when acknowledging one on System → Health",
    { timeout: 60000 },
    async () => {
      setupApi();
      const inner = api.getMockImplementation();
      let acked = false;
      api.mockImplementation((path, opts) => {
        if (path === "/api/notifications?open=1") {
          return Promise.resolve(
            acked
              ? [
                  {
                    id: "n-1",
                    subject: "ci-quota",
                    severity: "warning",
                    title: "CI runner quota exhausted",
                    body: "No cloud runners available in us-central1.",
                    raisedAt: "2026-10-26T12:00:00Z",
                    ackedAt: "2026-10-26T12:05:00Z",
                  },
                ]
              : [
                  {
                    id: "n-1",
                    subject: "ci-quota",
                    severity: "warning",
                    title: "CI runner quota exhausted",
                    body: "No cloud runners available in us-central1.",
                    raisedAt: "2026-10-26T12:00:00Z",
                  },
                ],
          );
        }
        if (path === "/api/notifications/n-1/ack" && opts?.method === "POST") {
          acked = true;
          return Promise.resolve({ status: "ok" });
        }
        return inner(path, opts);
      });

      window.history.replaceState(null, "", "/system");
      const user = userEvent.setup();
      render(<App />);

      const health = await screen.findByTestId("system-health");
      const row = await within(health).findByTestId("alert-row");
      expect(row).toHaveTextContent("CI runner quota exhausted");
      expect(screen.getByTestId("alerts-button")).toHaveAccessibleName(
        "Alerts: 1 active",
      );

      await user.click(
        within(row).getByRole("button", { name: "Acknowledge" }),
      );

      await waitFor(() =>
        expect(within(health).queryByTestId("alert-row")).toBeNull(),
      );
      expect(screen.getByTestId("alerts-button")).toHaveAccessibleName(
        "Alerts: none",
      );
    },
  );
});
