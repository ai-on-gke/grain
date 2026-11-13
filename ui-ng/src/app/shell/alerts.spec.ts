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

import { describe, expect, it } from "vitest";
import type { GrainConfig, Task } from "../api/models";
import { collectAlerts, worstSeverity } from "./alerts";

const set = {
  deployment: {
    hasGitCredential: true,
    targetRepos: ["acme/widgets"],
  },
  agentBackends: [{ id: "c1" }],
};

describe("collectAlerts", () => {
  it("lists nothing for a healthy, set-up deployment", () => {
    expect(collectAlerts(set as GrainConfig)).toEqual([]);
    expect(collectAlerts(null)).toEqual([]);
  });

  it("lists a pipeline pause with its reason and a link to Fleet's controls", () => {
    const [a] = collectAlerts({
      ...set,
      pipelinePause: { paused: true, reason: "maintenance window" },
    } as GrainConfig);
    expect(a).toMatchObject({
      id: "pipeline-pause",
      severity: "warning",
      title: "Deployment paused",
      link: { route: "/fleet", query: { tab: "controls" } },
    });
    expect(a.sentence).toContain("No new tasks are being picked up");
    expect(a.sentence).toContain("maintenance window");
  });

  it("calls manual mode what it is", () => {
    const [a] = collectAlerts({
      ...set,
      pipelinePause: { paused: true, runNowPasses: true },
    } as GrainConfig);
    expect(a.title).toBe("Paused — only tasks you Run now will start");
    expect(a.sentence).toContain("Run now still starts the task");
  });

  it("puts critical rows first and orders the full set", () => {
    const alerts = collectAlerts(
      {
        deployment: { hasGitCredential: false },
        agentBackends: [],
        reconcilerDown: true,
        dispatchBlocked: [{ name: "store-down", error: "db gone" }],
        stateRepoSyncFailure: { repo: "acme/config", error: "push refused" },
        hostSandboxes: true,
        agentPause: {
          paused: true,
          pauses: [
            {
              connection: "c1",
              connectionName: "Alpha",
              model: "m1",
              until: new Date(Date.now() + 3600_000).toISOString(),
              secondsRemaining: 3600,
            },
          ],
        },
        pipelinePause: { paused: true },
      } as unknown as GrainConfig,
      [
        {
          id: "77",
          state: "awaiting_submit",
          redBaseBranch: "main",
          pullRequest: { number: 9 },
        } as unknown as Task,
      ],
    );
    expect(alerts.map((a) => a.id)).toEqual([
      "red-base-repair/77",
      "reconciler-down",
      "state-sync-failing",
      "host-sandboxes",
      "agent-pause//c1/m1",
      "pipeline-pause",
      "setup",
    ]);
    expect(worstSeverity(alerts)).toBe("critical");
    const byId = Object.fromEntries(alerts.map((a) => [a.id, a]));
    expect(byId["red-base-repair/77"].link?.route).toBe("/task/77");
    expect(byId["state-sync-failing"].link?.route).toBe(
      "/settings/config-repo",
    );
    expect(byId["reconciler-down"].sentence).toContain("store down: db gone");
    expect(byId["agent-pause//c1/m1"].scope).toBe("Connection Alpha");
    expect(byId["agent-pause//c1/m1"].sentence).toContain("m1 on Alpha");
    expect(byId["setup"].title).toBe("3 setup steps left");
    expect(byId["setup"].link?.route).toBe("/settings/git");
  });
});
