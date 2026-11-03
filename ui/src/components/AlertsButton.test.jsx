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

import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import AlertsButton, { groupAlerts } from "./AlertsButton.jsx";
import DisconnectedBanner from "./DisconnectedBanner.jsx";
import { collectAlerts } from "../alerts.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const pausedConnection = {
  connection: "alpha-claude",
  connectionName: "Alpha's Claude",
  team: "alpha",
  framework: "claude",
  until: "2026-09-03T17:00:00Z",
  reason: "claude: usage limit reached",
  secondsRemaining: 7200,
  canLift: true,
};

const config = (extra = {}) => ({
  agentBackends: [{ id: "c1", team: "alpha" }],
  ...extra,
});

function renderBell(cfg, props = {}) {
  const alerts = collectAlerts(cfg);
  return render(<AlertsButton alerts={alerts} {...props} />);
}

function openPanel() {
  fireEvent.click(screen.getByTestId("alerts-button"));
  return screen.getByTestId("alerts-panel");
}

describe("AlertsButton (grain/task-4008)", () => {
  it("is quiet with no alerts, and says so when opened", () => {
    renderBell(config());
    const bell = screen.getByTestId("alerts-button");
    expect(bell.querySelector('svg[data-glyph="alerts"]')).toBeInTheDocument();
    expect(bell).toHaveAttribute("data-severity", "none");
    expect(bell).toHaveAccessibleName("Alerts: none");
    expect(within(bell).queryByText("0")).not.toBeInTheDocument();
    openPanel();
    expect(screen.getByTestId("alerts-empty")).toBeInTheDocument();
  });

  // grain/task-1760: the popover's "see all" (settings-panel.md §4.1)
  // was missing, so the rest of System → Health -- config repo state,
  // settings waiting on a restart -- was a page away with no way there.
  it("opens System → Health from See all, with or without alerts, and closes", async () => {
    const onOpenHealth = vi.fn();
    renderBell(config(), { onOpenHealth });
    openPanel();
    fireEvent.click(screen.getByTestId("alerts-see-all"));
    expect(onOpenHealth).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByTestId("alerts-panel")).not.toBeInTheDocument(),
    );
  });

  it("counts the alerts and takes the worst one's colour", () => {
    const { rerender } = renderBell(
      config({ agentPause: { paused: true, pauses: [pausedConnection] } }),
    );
    const bell = screen.getByTestId("alerts-button");
    expect(bell).toHaveAttribute("data-severity", "warning");
    expect(bell).toHaveAccessibleName("Alerts: 1 active");
    expect(within(bell).getByText("1")).toBeInTheDocument();

    rerender(
      <AlertsButton
        alerts={collectAlerts(
          config({
            reconcilerDown: true,
            agentPause: { paused: true, pauses: [pausedConnection] },
          }),
        )}
      />,
    );
    expect(bell).toHaveAttribute("data-severity", "critical");
    expect(bell).toHaveAccessibleName("Alerts: 2 active, critical");
    expect(within(bell).getByText("2")).toBeInTheDocument();
  });

  it("lists each alert with its severity, scope, sentence and until when", () => {
    renderBell(
      config({ agentPause: { paused: true, pauses: [pausedConnection] } }),
    );
    const panel = openPanel();
    const [row] = within(panel).getAllByTestId("alert-row");
    expect(row).toHaveAttribute("data-severity", "warning");
    expect(within(row).getByTestId("alert-scope")).toHaveTextContent(
      "Connection Alpha's Claude",
    );
    expect(row).toHaveTextContent(
      /nothing is being dispatched onto connection/,
    );
    expect(row).toHaveTextContent(/Until /);
    expect(within(row).getByTestId("alert-impact")).toBeInTheDocument();
  });

  it("lifts a usage-limit pause from the panel and re-reads config", async () => {
    api.mockResolvedValueOnce({ lifted: true });
    const onChanged = vi.fn().mockResolvedValue(undefined);
    renderBell(
      config({ agentPause: { paused: true, pauses: [pausedConnection] } }),
      {
        onChanged,
      },
    );
    const panel = openPanel();
    fireEvent.click(within(panel).getByRole("button", { name: "Resume now" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/pause?connection=alpha-claude", {
        method: "DELETE",
      }),
    );
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it("reports a failed lift rather than pretending dispatch resumed", async () => {
    api.mockRejectedValueOnce(new Error("nope"));
    const onChanged = vi.fn();
    const showError = vi.fn();
    renderBell(
      config({ agentPause: { paused: true, pauses: [pausedConnection] } }),
      {
        onChanged,
        showError,
      },
    );
    const panel = openPanel();
    fireEvent.click(within(panel).getByRole("button", { name: "Resume now" }));
    await waitFor(() => expect(showError).toHaveBeenCalledWith("nope"));
    expect(onChanged).not.toHaveBeenCalled();
    expect(
      within(panel).getByRole("button", { name: "Resume now" }),
    ).toBeEnabled();
  });

  // Resuming the deployment while it waits to upgrade throws the
  // upgrade away, so that one pause asks first.
  it("asks before a resume that would cancel a scheduled upgrade", async () => {
    api.mockResolvedValue({ resumed: true });
    api.mockClear();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onChanged = vi.fn();
    renderBell(
      config({
        pipelinePause: {
          paused: true,
          byKind: "automation",
          byId: "upgrade-schedule",
        },
      }),
      { onChanged },
    );
    const panel = openPanel();
    fireEvent.click(within(panel).getByRole("button", { name: "Resume now" }));
    expect(confirm).toHaveBeenCalledWith(
      expect.stringMatching(/resuming cancels that upgrade/i),
    );
    await waitFor(() =>
      expect(
        within(panel).getByRole("button", { name: "Resume now" }),
      ).toBeEnabled(),
    );
    expect(api).not.toHaveBeenCalled();
    expect(onChanged).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    fireEvent.click(within(panel).getByRole("button", { name: "Resume now" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/pipeline-pause", {
        method: "DELETE",
      }),
    );
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    confirm.mockRestore();
  });

  it("opens the Settings tab that fixes the deployment's first setup step, and closes the panel", async () => {
    const onOpenSetup = vi.fn();
    const cfg = config({
      deployment: {
        id: "default",
        targetRepos: ["acme/widgets"],
      },
      agentBackends: [],
    });
    renderBell(cfg, { onOpenSetup });
    const panel = openPanel();
    expect(within(panel).getByTestId("alert-row")).toHaveAttribute(
      "data-severity",
      "info",
    );
    fireEvent.click(
      within(panel).getByRole("button", { name: "Finish setup" }),
    );
    expect(onOpenSetup).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "open-setup", tab: "git" }),
    );
    await waitFor(() =>
      expect(screen.queryByTestId("alerts-panel")).not.toBeInTheDocument(),
    );
  });

  it("opens a paused connection's settings", () => {
    const onOpenConnection = vi.fn();
    renderBell(
      config({ agentPause: { paused: true, pauses: [pausedConnection] } }),
      {
        onOpenConnection,
      },
    );
    const panel = openPanel();
    fireEvent.click(
      within(panel).getByRole("button", { name: "Open connection" }),
    );
    expect(onOpenConnection).toHaveBeenCalledWith("alpha-claude");
  });

  it("opens Settings → Config repo for a failing state-repo sync and closes the panel", async () => {
    const onOpenConfigRepo = vi.fn();
    renderBell(
      config({
        stateRepoSyncFailure: {
          since: "2026-10-06T08:57:32Z",
          error: "push rejected",
          repo: "grain/config",
        },
      }),
      { onOpenConfigRepo },
    );
    const panel = openPanel();
    fireEvent.click(within(panel).getByRole("button", { name: "Config repo" }));
    expect(onOpenConfigRepo).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByTestId("alerts-panel")).not.toBeInTheDocument(),
    );
  });

  it("opens System → Power, where Restart grain is, for a stopped reconcile loop", async () => {
    const onOpenPower = vi.fn();
    renderBell(config({ reconcilerDown: true }), { onOpenPower });
    const panel = openPanel();
    const [row] = within(panel).getAllByTestId("alert-row");
    expect(row).toHaveTextContent("Restart the daemon to recover");
    fireEvent.click(within(row).getByRole("button", { name: "Power" }));
    expect(onOpenPower).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByTestId("alerts-panel")).not.toBeInTheDocument(),
    );
  });

  it("announces a new critical alert politely, but not the ones already up", () => {
    const { rerender } = render(<AlertsButton alerts={[]} />);
    const region = screen.getByTestId("alerts-announcer");
    expect(region).toHaveAttribute("aria-live", "polite");
    expect(region).toBeEmptyDOMElement();
    act(() => {
      rerender(
        <AlertsButton alerts={collectAlerts({ reconcilerDown: true })} />,
      );
    });
    expect(region).toHaveTextContent("Critical alert: Reconcile loop stopped");
  });

  it("groups by connection once there are many", () => {
    const alerts = [
      { id: "d", severity: "critical", scope: { kind: "deployment" } },
      ...["a", "b", "c", "e"].map((c) => ({
        id: c,
        severity: "warning",
        scope: {
          kind: "connection",
          connection: c,
          connectionName: c.toUpperCase(),
        },
      })),
    ];
    expect(groupAlerts(alerts.slice(0, 4))).toBeNull();
    expect(groupAlerts(alerts).map(([h]) => h)).toEqual([
      "Deployment",
      "Connection A",
      "Connection B",
      "Connection C",
      "Connection E",
    ]);
  });
});

// The one banner that stays a banner: while the daemon cannot be
// reached, nothing in the alerts panel can be trusted either.
describe("DisconnectedBanner", () => {
  it("still shows as a banner", () => {
    render(
      <DisconnectedBanner
        disconnection={{ reason: "grain is not answering.", since: "" }}
      />,
    );
    expect(screen.getByTestId("disconnected-banner")).toHaveTextContent(
      /Unreachable/,
    );
  });
});

// A red base branch's waiting repair pull request was a filled strip
// across the top of every page (RedBaseRepairBanner); it is a critical
// row in this panel now, with the strip's buttons.
describe("AlertsButton: a red base branch's waiting repair", () => {
  const repair = {
    id: "323",
    state: "awaiting_submit",
    autoMerge: false,
    pullRequest: "bwsalmon/grain#4495",
    redBaseBranch: "main",
    approvals: [
      {
        rule: "trigger:grain-declarations",
        approvers: ["any human"],
      },
    ],
  };

  function renderRepair(tasks, props = {}) {
    return render(
      <AlertsButton alerts={collectAlerts(config(), "", tasks)} {...props} />,
    );
  }

  it("turns the bell critical and lists the repair with its wait reason", () => {
    renderRepair([repair]);
    const bell = screen.getByTestId("alerts-button");
    expect(bell).toHaveAttribute("data-severity", "critical");
    expect(bell).toHaveAccessibleName("Alerts: 1 active, critical");
    openPanel();
    const row = screen.getByTestId("alert-row");
    expect(row).toHaveAttribute("data-alert-id", "red-base-repair/323");
    expect(row).toHaveAttribute("data-severity", "critical");
    expect(row).toHaveTextContent(
      "main is red — repair #323 is waiting for human approval on trigger:grain-declarations (any human)",
    );
  });

  it("opens the repair task and closes the panel", async () => {
    const onOpenTask = vi.fn();
    renderRepair([repair], { onOpenTask });
    openPanel();
    fireEvent.click(screen.getByRole("button", { name: "Open #323" }));
    expect(onOpenTask).toHaveBeenCalledWith("323");
    await waitFor(() =>
      expect(screen.queryByTestId("alerts-panel")).not.toBeInTheDocument(),
    );
  });

  it("submits the repair through onAct", async () => {
    api.mockReset();
    api.mockResolvedValue({});
    const onAct = vi.fn(async (fn) => {
      await fn();
      return true;
    });
    const onChanged = vi.fn();
    renderRepair([repair], { onAct, onChanged });
    openPanel();
    fireEvent.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(onAct).toHaveBeenCalledWith(expect.any(Function), "323");
    expect(api).toHaveBeenCalledWith("/api/tasks/323/submit", {
      method: "POST",
    });
  });

  it("force-merges the repair with the reason the operator gives", async () => {
    api.mockReset();
    api.mockResolvedValue({});
    const prompt = vi.spyOn(window, "prompt").mockReturnValue("unblock main");
    const onAct = vi.fn(async (fn) => {
      await fn();
      return true;
    });
    try {
      renderRepair([repair], { onAct });
      openPanel();
      fireEvent.click(screen.getByRole("button", { name: "Force merge" }));
      await waitFor(() =>
        expect(api).toHaveBeenCalledWith("/api/tasks/323/force-merge", {
          method: "POST",
          body: JSON.stringify({ reason: "unblock main" }),
        }),
      );
      expect(onAct).toHaveBeenCalledWith(expect.any(Function), "323");
    } finally {
      prompt.mockRestore();
    }
  });

  it("offers no Submit while the repair is auto-merging", () => {
    renderRepair([
      {
        ...repair,
        autoMerge: true,
        state: "completed",
        approvals: [],
        mergeQueueState: "waiting_ci",
        mergeQueueNote: "Waiting on check go",
      },
    ]);
    openPanel();
    expect(screen.getByTestId("alert-row")).toHaveTextContent(
      "main is red — repair #323 is waiting in the merge queue (Waiting on check go)",
    );
    expect(
      screen.queryByRole("button", { name: "Submit" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Force merge" }),
    ).toBeInTheDocument();
  });
});
