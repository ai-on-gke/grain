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
import { settingLocation } from "../settings/settings-sections";
import {
  collectHealthAlerts,
  formatDurationSeconds,
  formatGitHubBudget,
  formatScheduleCadence,
  groupHealthAlerts,
  liftPauseQuery,
  staleScheduleRepo,
} from "./fleet-health.models";

const NOW = Date.parse("2026-10-26T04:10:00Z");

describe("fleet-health.models", () => {
  it("resolves pendingRestart and unrunnable setting keys to /settings/<section>#<field>", () => {
    expect(settingLocation("pollInterval")).toEqual({
      section: "engine",
      target: "pollInterval",
    });
    expect(settingLocation("sandboxBackend")).toEqual({
      section: "sandboxes",
      target: "sandboxBackend",
    });
    expect(settingLocation("githubHost")).toEqual({
      section: "capabilities",
      target: "githubHost",
    });
    expect(settingLocation("k8sNamespace")).toEqual({
      section: "sandboxes",
      target: "k8sNamespace",
    });
    expect(settingLocation("unknownFutureKey")).toBeNull();
  });

  it("formats GitHub API budget, exhaustion projection, idle-poll backoff, and callers", () => {
    expect(
      formatGitHubBudget({ enabled: true, budget: { limit: 0 } }, NOW),
    ).toBeNull();

    const view = formatGitHubBudget(
      {
        enabled: true,
        budget: {
          limit: 5000,
          remaining: 1650,
          resetsAt: "2026-10-26T04:48:00Z",
          spendPerHour: 3300,
          exhaustedAt: "2026-10-26T04:40:00Z",
          lowFraction: 0.15,
        },
        polls: {
          tracked: 4,
          backedOff: 3,
          baseSeconds: 30,
          capSeconds: 480,
          medianSeconds: 120,
          maxSeconds: 480,
        },
        callers: [
          {
            caller: "sync",
            spent: 900,
            revalidated: 1500,
            kinds: [{ kind: "PR read", spent: 380 }],
          },
        ],
      },
      NOW,
    )!;

    expect(view.tone).toBe("warn");
    expect(view.headline).toContain("1,650 of 5,000 calls left this hour");
    expect(view.headline).toContain("resets in 38m 0s");
    expect(view.headline).toContain("spending 3,300/h");
    expect(view.runsOutLine).toBe(
      "At that rate it runs out in 30m 0s, before it resets.",
    );
    expect(view.pollsLine).toBe(
      "Idle-poll backoff: 3 of 4 tracked PRs backed off · poll interval median 2m 0s, max 8m 0s (30s base, 8m 0s cap).",
    );
    expect(view.callers).toEqual([
      {
        caller: "sync",
        spent: "900",
        revalidated: "1,500",
        mostly: "PR read (380)",
      },
    ]);

    const low = formatGitHubBudget(
      {
        enabled: true,
        budget: { limit: 5000, remaining: 400, low: true, lowFraction: 0.15 },
      },
      NOW,
    )!;
    expect(low.tone).toBe("error");
    expect(low.lowLine).toContain("Under 15% left");
  });

  it("collects standing alerts across config, tasks, and open notifications sorted by severity", () => {
    const alerts = collectHealthAlerts(
      {
        reconcilerDown: true,
        dispatchBlocked: [
          {
            name: "git-proxy",
            error: "address in use",
            since: "2026-10-26T04:00:00Z",
          },
        ],
        agentPause: {
          paused: true,
          pauses: [
            {
              connection: "antigravity@hostd/devbox",
              model: "gemini-2.5-pro",
              until: "2026-10-26T11:15:00Z",
              secondsRemaining: 3600,
              canLift: true,
            },
          ],
        },
        deployment: {
          hasGitCredential: false,
          targetRepos: [],
        },
      },
      [
        {
          id: "14",
          title: "Fix broken main build",
          state: "awaiting_submit",
          redBaseBranch: "main",
          pullRequest: "https://example.com/pr/14",
        },
      ],
      [
        {
          id: 101,
          subject: "release.pending:v2.4.0",
          severity: "warning",
          actions: ["approve-release"],
          repo: "acme/widgets",
          body: "v2.4.0 passed qualification.",
        },
        {
          id: 102,
          subject: "merge.stalled:1",
          title: "A merge has been stalled for 2 hours",
          severity: "warning",
          taskId: "1",
          body: "PR #1 has not moved.",
        },
        {
          id: 103,
          subject: "already.acked",
          ackedAt: "2026-10-26T03:00:00Z",
        },
      ],
      "UTC",
      NOW,
    );

    expect(alerts.map((a) => a.id)).toEqual([
      "reconciler-down",
      "red-base-repair/14",
      "agent-pause//antigravity@hostd/devbox/gemini-2.5-pro",
      "notification-101",
      "notification-102",
      "setup",
    ]);

    // 6 alerts across 2 scopes ("Deployment" and "Connection antigravity on host devbox") -> grouped!
    const groups = groupHealthAlerts(alerts)!;
    expect(groups.map((g) => g.heading)).toEqual([
      "Deployment",
      "Connection antigravity on host devbox",
    ]);
  });

  it("formats helper strings cleanly", () => {
    expect(formatDurationSeconds(45)).toBe("45s");
    expect(formatDurationSeconds(125)).toBe("2m 5s");
    expect(formatDurationSeconds(7500)).toBe("2h 5m");
    expect(formatScheduleCadence(6 * 3600 * 1e9)).toBe("6h");
    expect(
      staleScheduleRepo({
        Host: "git.example.com",
        Owner: "acme",
        Name: "app",
      }),
    ).toBe("git.example.com/acme/app");
    expect(liftPauseQuery({ connection: "conn-1", model: "claude-opus" })).toBe(
      "connection=conn-1&model=claude-opus",
    );
  });
});
