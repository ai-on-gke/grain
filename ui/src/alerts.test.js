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

import { describe, expect, it, vi } from "vitest";
import {
  collectAlerts,
  deploymentSetupStepsLeft,
  formatRemaining,
  liftPath,
  pauseMessage,
  pausedUntil,
  pipelinePauseMessage,
  pipelinePauseTitle,
  heldForRunNow,
  resumeCancelsUpgrade,
  scopeLabel,
  setupAlertText,
  sortAlerts,
  stateSyncSentence,
  worstSeverity,
} from "./alerts.js";

vi.mock("./api.js", () => ({ default: vi.fn() }));

// The shapes GET /api/config carries (ui.Config and friends).

const alphaClaude = {
  connection: "alpha-claude",
  connectionName: "Alpha's Claude",
  team: "alpha",
  framework: "claude",
  until: "2026-09-03T17:00:00Z",
  reason: "claude: usage limit reached; resets at 2026-09-03T17:00:00Z",
  secondsRemaining: 7200,
  canLift: true,
};

const base = {
  agentBackends: [
    { id: "c1", team: "alpha" },
    { id: "c2", team: "beta" },
  ],
};

const ids = (alerts) => alerts.map((a) => a.id);
const byId = (alerts, id) => alerts.find((a) => a.id === id);

describe("collectAlerts (grain/task-4008)", () => {
  it("is nothing for a deployment with nothing wrong", () => {
    expect(collectAlerts(base)).toEqual([]);
    expect(collectAlerts(null)).toEqual([]);
  });

  // Each source the banners used to show, with the severity alerts.js
  // gives it and where it applies.
  it("gives every source its severity and scope", () => {
    const config = {
      ...base,
      reconcilerDown: true,
      stateRepoSyncFailure: {
        since: "2026-10-06T08:57:32Z",
        error: "withheld columns",
        repo: "grain/config",
      },
      hostSandboxes: true,
      agentPause: {
        paused: true,
        pauses: [
          alphaClaude,
          { team: "alpha", framework: "codex", until: alphaClaude.until },
        ],
      },
      pipelinePause: { paused: true, since: "2026-10-07T00:00:00Z" },
    };
    const alerts = collectAlerts(config);
    const want = {
      "reconciler-down": ["critical", "Deployment"],
      "state-sync-failing": ["critical", "Deployment"],
      "host-sandboxes": ["warning", "Deployment"],
      "agent-pause/alpha/alpha-claude": [
        "warning",
        "Connection Alpha's Claude",
      ],
      "agent-pause/alpha/codex": ["warning", "Deployment"],
      "pipeline-pause": ["warning", "Deployment"],
    };
    expect(ids(alerts).sort()).toEqual(Object.keys(want).sort());
    for (const [id, [severity, scope]] of Object.entries(want)) {
      const a = byId(alerts, id);
      expect([id, a.severity]).toEqual([id, severity]);
      expect([id, scopeLabel(a.scope)]).toEqual([id, scope]);
    }
    // Worst first, then the most recent of each severity first.
    expect(ids(alerts).slice(0, 2)).toEqual([
      "state-sync-failing",
      "reconciler-down",
    ]);
  });

  // One row per pause the server reports, so a per-connection pause
  // gets its own row, scope and Resume.
  it("makes one alert per paused connection, each with its own actions", () => {
    const config = {
      ...base,
      agentPause: {
        paused: true,
        pauses: [
          alphaClaude,
          {
            ...alphaClaude,
            connection: "beta-claude",
            connectionName: "",
            team: "beta",
            canLift: false,
          },
        ],
      },
    };
    const alerts = collectAlerts(config);
    const a = byId(alerts, "agent-pause/alpha/alpha-claude");
    const b = byId(alerts, "agent-pause/beta/beta-claude");
    expect(a.actions.map((x) => x.kind)).toEqual([
      "lift-agent-pause",
      "open-connection",
    ]);
    // Only the pause this viewer may lift offers Resume.
    expect(b.actions.map((x) => x.kind)).toEqual(["open-connection"]);
    expect(scopeLabel(b.scope)).toBe("Connection beta-claude");
    expect(a.until).toBe(alphaClaude.until);
  });

  it("still makes one alert of an older daemon's single pause", () => {
    const alerts = collectAlerts({
      ...base,
      agentPause: {
        paused: true,
        until: alphaClaude.until,
        secondsRemaining: 60,
      },
    });
    expect(ids(alerts)).toEqual(["agent-pause//0"]);
    expect(alerts[0].scope).toEqual({ kind: "deployment" });
  });

  it("counts the deployment's setup steps for whoever reads them", () => {
    const fresh = { id: "default" };
    const config = { ...base, agentBackends: [], deployment: fresh };
    const [a] = collectAlerts(config);
    expect(a.title).toBe("3 setup steps left");
    expect(a.severity).toBe("info");
    expect(scopeLabel(a.scope)).toBe("Deployment");
    expect(a.impact).toMatch(/wait until the deployment has a git host/);
    expect(a.actions).toEqual([
      { kind: "open-setup", label: "Finish setup", tab: "git", view: null },
    ]);
  });

  it("lists only the required steps still left", () => {
    const deployment = {
      id: "default",
      hasGitCredential: true,
      repos: [{ repo: "acme/widgets" }],
    };
    expect(deploymentSetupStepsLeft(deployment, {}).map((s) => s.id)).toEqual([
      "connections",
    ]);
    expect(
      deploymentSetupStepsLeft(deployment, {
        agentBackends: [{ id: "c1" }],
      }),
    ).toEqual([]);
    expect(
      collectAlerts({ deployment, agentBackends: [{ id: "c1" }] }),
    ).toEqual([]);
  });

  it("says step for one step", () => {
    expect(setupAlertText(1)).toBe("1 setup step left");
    expect(setupAlertText(2)).toBe("2 setup steps left");
  });

  // The access code is owner access (docs/design/settings-panel.md §6.1).
  it("offers Settings → Config repo for a failing sync to whoever reads it", () => {
    const config = {
      ...base,
      stateRepoSyncFailure: { error: "x", repo: "grain/config" },
    };
    expect(collectAlerts(config)[0].actions).toEqual([
      { kind: "open-config-repo", label: "Config repo" },
    ]);
  });
});

describe("worstSeverity and sortAlerts", () => {
  it("picks the worst and orders by severity, then recency", () => {
    const alerts = [
      { id: "a", severity: "info" },
      { id: "b", severity: "warning", since: "2026-01-01T00:00:00Z" },
      { id: "c", severity: "warning", since: "2026-02-01T00:00:00Z" },
      { id: "d", severity: "critical" },
    ];
    expect(worstSeverity(alerts)).toBe("critical");
    expect(worstSeverity([])).toBeNull();
    expect(ids(sortAlerts(alerts))).toEqual(["d", "c", "b", "a"]);
  });
});

describe("the sentences", () => {
  it("names a connection's usage-limit pause, until when, and what the provider said", () => {
    const text = pauseMessage(alphaClaude);
    expect(text).toMatch(
      /nothing is being dispatched onto connection Alpha's Claude/,
    );
    expect(text).toMatch(/about 2h 0m/);
    expect(text).toMatch(/claude: usage limit reached/);
    expect(liftPath(alphaClaude)).toBe("/api/pause?connection=alpha-claude");
  });

  it("names the model of a pause on one model, and lifts that model alone", () => {
    const pro = { ...alphaClaude, model: "claude-opus-4" };
    const text = pauseMessage(pro);
    expect(text).toMatch(
      /^claude-opus-4 on Alpha's Claude paused until .* — usage limit; the connection's other models keep dispatching\./,
    );
    expect(liftPath(pro)).toBe(
      "/api/pause?connection=alpha-claude&model=claude-opus-4",
    );
    const escalated = {
      ...alphaClaude,
      escalated: "the provider's refusal is account-wide",
    };
    expect(pauseMessage(escalated)).toMatch(
      /onto connection Alpha's Claude, on any model, .*\(the provider's refusal is account-wide\)/,
    );
  });

  it("names the deployment's runs on no connection by framework", () => {
    const legacy = {
      team: "default",
      framework: "codex",
      until: alphaClaude.until,
    };
    expect(pauseMessage(legacy)).toMatch(
      /nothing is being dispatched onto this deployment's codex runs/,
    );
    expect(liftPath(legacy)).toBe("/api/pause?team=default&framework=codex");
    expect(pauseMessage({ paused: true })).toMatch(
      /until the provider's window resets/,
    );
  });

  it("rounds a countdown to hours and minutes", () => {
    expect(formatRemaining(30)).toBe("less than a minute");
    expect(formatRemaining(600)).toBe("about 10m");
    expect(formatRemaining(18000)).toBe("about 5h 0m");
    expect(formatRemaining(-5)).toBe("less than a minute");
  });

  it("gives the pause as a clause for a place that names what is paused", () => {
    expect(pausedUntil(alphaClaude, "UTC")).toMatch(
      /^paused until .* \(about 2h 0m\) — claude: usage limit reached/,
    );
    expect(pausedUntil({ paused: true })).toBe(
      "paused until the provider's window resets",
    );
  });

  it("says the deployment is paused, that live runs finish, and when it lifts itself", () => {
    const byHand = { paused: true, reason: "patching the host" };
    expect(pipelinePauseMessage(byHand)).toMatch(
      /No new tasks are being picked up: .* left to finish\..*patching the host/,
    );
    expect(pipelinePauseMessage({ ...byHand, untilTask: "412" })).toMatch(
      /lifts itself when task 412 finishes/i,
    );
    const waiting = {
      paused: true,
      byKind: "automation",
      byId: "upgrade-schedule",
    };
    expect(pipelinePauseMessage(waiting)).toMatch(
      /the upgrade starts once they have, and resume now cancels it/i,
    );
    expect(pipelinePauseMessage(waiting)).not.toMatch(/lifts itself/i);
    expect(resumeCancelsUpgrade(waiting)).toMatch(
      /Schedule it again from Settings → Upgrade\./,
    );
  });

  it("says Run now still starts work under a pause it passes, and not under an upgrade's hold", () => {
    const manual = { paused: true, runNowPasses: true, reason: "hand-picking" };
    expect(pipelinePauseTitle(manual)).toBe(
      "Paused — only tasks you Run now will start",
    );
    expect(pipelinePauseMessage(manual)).toMatch(/Run now still starts/);
    const upgrading = { paused: true, byKind: "automation", byId: "upgrade" };
    expect(pipelinePauseTitle(upgrading)).toBe("Deployment paused");
    expect(pipelinePauseMessage(upgrading)).toMatch(
      /Run now waits for the upgrade/,
    );

    expect(heldForRunNow(manual, { id: "1", state: "queued" })).toBe(true);
    expect(
      heldForRunNow(manual, { id: "1", state: "queued", runNowAt: "x" }),
    ).toBe(false);
    expect(heldForRunNow(manual, { id: "1", state: "running" })).toBe(false);
    // A blocked task waits for its blockers, not only for the pause.
    expect(
      heldForRunNow(manual, { id: "1", state: "queued", blocked: true }),
    ).toBe(false);
    // An individually paused task waits on its own Resume, and a
    // priority-dispatch task or standing session dispatches through the pause.
    expect(
      heldForRunNow(manual, {
        id: "1",
        state: "queued",
        paused: { by: "alice", at: "2026-10-26T00:00:00Z" },
      }),
    ).toBe(false);
    expect(
      heldForRunNow(manual, {
        id: "1",
        state: "queued",
        capabilities: ["priority-dispatch"],
      }),
    ).toBe(false);
    expect(
      heldForRunNow(manual, {
        id: "1",
        state: "queued",
        consoleSession: "steward",
      }),
    ).toBe(false);
    expect(heldForRunNow(upgrading, { id: "1", state: "queued" })).toBe(false);
    expect(heldForRunNow(undefined, { id: "1", state: "queued" })).toBe(false);
  });

  it("names the repository a failing sync is not reaching", () => {
    expect(
      stateSyncSentence({ since: "", error: "boom", repo: "grain/config" }),
    ).toBe(
      "Settings have not been exported to grain/config since an unknown time: boom",
    );
  });

  // The 2026-10-23 settings walkthrough read this on the browser's own
  // clock while every other time followed the chosen time zone.
  it("prints when the sync stopped on the reader's time zone", () => {
    const failure = {
      since: "2026-10-23T09:00:00Z",
      error: "boom",
      repo: "grain/config",
    };
    const tokyo = stateSyncSentence(failure, "Asia/Tokyo");
    const utc = stateSyncSentence(failure, "UTC");
    expect(tokyo).toContain(
      new Date(failure.since).toLocaleString(undefined, {
        timeZone: "Asia/Tokyo",
      }),
    );
    expect(utc).toContain(
      new Date(failure.since).toLocaleString(undefined, { timeZone: "UTC" }),
    );
    expect(tokyo).not.toBe(utc);
    const [alert] = collectAlerts(
      { stateRepoSyncFailure: failure },
      "Asia/Tokyo",
    );
    expect(alert.sentence).toBe(`${tokyo}.`);
  });
});

describe("collectAlerts: a red base branch's waiting repair", () => {
  const awaitingSubmit = {
    id: "323",
    state: "awaiting_submit",
    autoMerge: false,
    pullRequest: "bwsalmon/grain#4495",
    repo: "bwsalmon/grain",
    redBaseBranch: "main",
    approvals: [
      {
        rule: "trigger:grain-declarations",
        approvers: ["any human"],
      },
    ],
  };
  const autoMerging = {
    id: "324",
    state: "completed",
    autoMerge: true,
    pullRequest: "bwsalmon/grain#4496",
    repo: "bwsalmon/grain",
    redBaseBranch: "candidate",
    mergeQueueState: "waiting_ci",
    mergeQueueNote: "Waiting on check go",
  };

  it("is a critical row per waiting repair, with the banner's actions", () => {
    const alerts = collectAlerts(base, "", [awaitingSubmit, autoMerging]);
    expect(ids(alerts)).toEqual(["red-base-repair/323", "red-base-repair/324"]);
    const a = byId(alerts, "red-base-repair/323");
    expect(a.severity).toBe("critical");
    expect(a.title).toBe("`main` is red — repair #323");
    expect(a.sentence).toBe(
      "`main` is red — repair #323 is waiting for human approval on trigger:grain-declarations (any human)",
    );
    expect(a.scope).toEqual({ kind: "deployment" });
    expect(a.actions).toEqual([
      { kind: "open-task", label: "Open #323", taskId: "323" },
      { kind: "submit-task", label: "Submit", taskId: "323" },
      { kind: "force-merge-task", label: "Force merge", task: awaitingSubmit },
    ]);
    // Auto-merging: nothing to submit, only the override.
    expect(
      byId(alerts, "red-base-repair/324").actions.map((x) => x.kind),
    ).toEqual(["open-task", "force-merge-task"]);
    expect(byId(alerts, "red-base-repair/324").sentence).toBe(
      "`candidate` is red — repair #324 is waiting in the merge queue (Waiting on check go)",
    );
  });

  it("offers Force merge only to an admin of the repair's repository", () => {
    const [a] = collectAlerts(
      { ...base, adminScoped: true, adminRepos: ["acme/*"] },
      "",
      [awaitingSubmit],
    );
    expect(a.actions.map((x) => x.kind)).toEqual(["open-task", "submit-task"]);
  });

  it("leaves out repairs that are not waiting, and callers that pass no tasks", () => {
    expect(collectAlerts(base)).toEqual([]);
    expect(
      collectAlerts(base, "", [
        { ...awaitingSubmit, state: "running" },
        { ...awaitingSubmit, id: "2", state: "closed" },
        { ...awaitingSubmit, id: "3", redBaseBranch: "" },
        { ...autoMerging, mergeQueueState: "merging" },
      ]),
    ).toEqual([]);
  });

  it("sorts with the other critical alerts, ahead of warnings", () => {
    const alerts = collectAlerts(
      { ...base, agentPause: { paused: true, pauses: [alphaClaude] } },
      "",
      [awaitingSubmit],
    );
    expect(alerts[0].id).toBe("red-base-repair/323");
    expect(alerts[1].severity).toBe("warning");
  });
});
