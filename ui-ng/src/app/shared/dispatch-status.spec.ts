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
import {
  formatLocalReset,
  formatNoticeText,
  pauseWindowsHidden,
  pruneHiddenPauses,
  summarizeDispatchStatus,
} from "./dispatch-status";

describe("dispatch-status", () => {
  const nowMs = Date.parse("2026-11-13T18:00:00Z");

  it("returns null when dispatch and model quotas are healthy", () => {
    expect(
      summarizeDispatchStatus({}, { warnings: [], scopes: [] }, nowMs),
    ).toBeNull();
  });

  it("formats local reset times with relative countdown", () => {
    const out = formatLocalReset(
      "2026-11-13T20:15:00Z",
      undefined,
      nowMs,
      "America/Los_Angeles",
    );
    expect(out).toContain("PST");
    expect(out).toContain("in 2h 15m");
  });

  it("reports whole-connection agentPause as error with local reset countdown", () => {
    const res = summarizeDispatchStatus(
      {
        agentPause: {
          paused: true,
          until: "2026-11-13T19:30:00Z",
          reason: "HTTP 429 quota exceeded",
          pauses: [
            {
              connection: "antigravity",
              connectionName: "Antigravity",
              team: "core",
              until: "2026-11-13T19:30:00Z",
              reason: "HTTP 429 quota exceeded",
              secondsRemaining: 5400,
              canLift: true,
            },
          ],
        },
      },
      null,
      nowMs,
      "America/Los_Angeles",
    );
    expect(res?.tone).toBe("error");
    expect(res?.headline).toContain("Antigravity development paused");
    expect(res?.resetText).toContain("in 1h 30m");
  });

  it("escalates usage-governor fallback exhaustion to error and converts UTC text", () => {
    const res = summarizeDispatchStatus(
      {},
      {
        warnings: [
          "default model opus-5.5 on antigravity is paused until 20:00 UTC; work falls back to nothing — it is the last model on the default list",
        ],
        scopes: [
          {
            connection: "antigravity",
            model: "opus-5.5",
            pausedUntil: "2026-11-13T20:00:00Z",
          },
        ],
      },
      nowMs,
      "America/Los_Angeles",
    );
    expect(res?.tone).toBe("error");
    expect(res?.headline).toContain("no fallback models left");
    expect(res?.resetText).toContain("in 2h");
    expect(
      formatNoticeText("paused until 20:00 UTC", nowMs, "America/Los_Angeles"),
    ).toContain("in 2h");
  });

  it("reads the daemon's 12-hour UTC reset clock in the viewer's zone", () => {
    const out = formatNoticeText(
      "gemini-x is paused until 8:00 PM UTC",
      nowMs,
      "America/Los_Angeles",
    );
    expect(out).toContain("PST");
    expect(out).toContain("in 2h");
    expect(out).toContain("[8:00 PM UTC]");
    // A clock already on the viewer's zone is left as it came.
    expect(
      formatNoticeText(
        "paused until 12:00 PM PST",
        nowMs,
        "America/Los_Angeles",
      ),
    ).toBe("paused until 12:00 PM PST");
  });

  it("filters expired agentPause and scope pauses instead of lingering stale banners", () => {
    const expired = summarizeDispatchStatus(
      {
        agentPause: {
          paused: true,
          until: "2026-11-13T17:55:00Z",
          pauses: [
            {
              connection: "antigravity",
              team: "core",
              until: "2026-11-13T17:55:00Z",
              secondsRemaining: 0,
              canLift: true,
            },
          ],
        },
      },
      {
        warnings: [],
        scopes: [
          {
            connection: "antigravity",
            model: "opus-5.5",
            pausedUntil: "2026-11-13T17:59:00Z",
          },
        ],
      },
      nowMs,
      "America/Los_Angeles",
    );
    expect(expired).toBeNull();
  });

  it("keeps fallback warning as warn when defaultModels still has an unpaused fallback and avoids duplicate UTC suffix", () => {
    const res = summarizeDispatchStatus(
      {
        defaultModel: "claude-opus-5,gemini-pro",
      },
      {
        warnings: [],
        scopes: [
          {
            connection: "c1",
            model: "claude-opus-5",
            pausedUntil: "2026-11-13T20:00:00Z",
          },
        ],
      },
      nowMs,
      "UTC",
    );
    expect(res?.tone).toBe("warn");
    expect(res?.headline).toContain("fallback active");

    expect(
      formatNoticeText("paused until 20:00 UTC", nowMs, "UTC"),
    ).not.toContain("[20:00 UTC]");
  });

  it("picks terminal exhaustion warning for subline and respects modelQuotaOnly", () => {
    const res = summarizeDispatchStatus(
      {
        reconcilerDown: true,
        pipelinePause: { paused: true, reason: "maintenance" },
      },
      {
        warnings: [
          "default model a on c1 is paused until 20:00 UTC; work falls back to b",
          "default model b on c1 is paused until 20:00 UTC; work falls back to nothing — last model",
        ],
        scopes: [],
      },
      nowMs,
      "UTC",
      { modelQuotaOnly: true },
    );
    expect(res?.tone).toBe("error");
    expect(res?.headline).toContain("no fallback models left");
    expect(res?.subline).toContain("work falls back to nothing");
  });

  it("ignores connectionless framework pauses when modelQuotaOnly is set", () => {
    const fwOnly = {
      agentPause: {
        paused: true,
        until: "2026-11-13T21:00:00Z",
        reason: "Paused by operator for known-bad CLI release",
        pauses: [
          {
            team: "core",
            framework: "codex",
            until: "2026-11-13T21:00:00Z",
            reason: "Paused by operator for known-bad CLI release",
            secondsRemaining: 10800,
            canLift: true,
          },
        ],
      },
    };
    expect(
      summarizeDispatchStatus(fwOnly, null, nowMs, "UTC", {
        modelQuotaOnly: true,
      }),
    ).toBeNull();
    expect(
      summarizeDispatchStatus(fwOnly, null, nowMs, "UTC")?.headline,
    ).toContain("codex development paused");

    const mixed = summarizeDispatchStatus(
      {
        agentPause: {
          paused: true,
          until: "2026-11-13T21:00:00Z",
          reason: "Paused by operator for known-bad CLI release",
          pauses: [
            {
              connection: "vertex-main",
              connectionName: "Vertex Main",
              model: "gemini-2.5-pro",
              team: "core",
              until: "2026-11-13T19:00:00Z",
              reason: "HTTP 429 quota exceeded",
              secondsRemaining: 3600,
              canLift: true,
            },
            {
              team: "core",
              framework: "codex",
              until: "2026-11-13T21:00:00Z",
              reason: "Paused by operator for known-bad CLI release",
              secondsRemaining: 10800,
              canLift: true,
            },
          ],
        },
      },
      null,
      nowMs,
      "UTC",
      { modelQuotaOnly: true },
    );
    expect(mixed?.tone).toBe("warn");
    expect(mixed?.headline).toContain("Vertex Main · gemini-2.5-pro");
    expect(mixed?.subline).toBe("HTTP 429 quota exceeded");
    expect(mixed?.resetText).toContain("in 1h");
  });

  it("names a spent weekly quota and reads a dated weekly reset", () => {
    const res = summarizeDispatchStatus(
      {},
      {
        warnings: [
          "default model opus on claude hit its weekly limit and is paused until Nov 16 03:00 UTC; work falls back to sonnet",
        ],
        scopes: [
          {
            connection: "claude",
            model: "opus",
            pausedUntil: "2026-11-16T03:00:00Z",
            pauseHorizon: "weekly",
            weeklyPausedUntil: "2026-11-16T03:00:00Z",
          },
        ],
      },
      nowMs,
      "UTC",
    );
    expect(res?.tone).toBe("warn");
    expect(res?.headline).toContain("Weekly model quota reached");
    expect(res?.details.some((d) => d.reason === "Weekly limit reached")).toBe(
      true,
    );
    // Nov 16 03:00 is 2d 9h after Nov 13 18:00, not the next 03:00.
    const text = formatNoticeText(
      "paused until Nov 16 03:00 UTC",
      nowMs,
      "UTC",
    );
    expect(text).toContain("Nov 16");
    expect(text).toContain("(in 57h)");
  });

  describe("hiding model pauses (GU-6)", () => {
    const until = (min: number) => new Date(nowMs + min * 60_000).toISOString();
    const modelPause = (model: string, min: number) => ({
      connection: "claude",
      connectionName: "Claude",
      model,
      until: until(min),
    });
    const cfgWith = (...pauses: object[]) => ({
      agentPause: { paused: true, pauses },
    });
    const hiddenFrom = (
      cfg: Parameters<typeof summarizeDispatchStatus>[0],
      at = nowMs,
    ) =>
      summarizeDispatchStatus(cfg, null, at)!.pauses.map((p) => ({
        ...p,
        hiddenAtMs: at,
      }));

    it("lets a model pause with fallback be hidden, and keeps it hidden across polls", () => {
      const b = summarizeDispatchStatus(
        cfgWith(modelPause("opus", 120)),
        null,
        nowMs,
      )!;
      expect(b.hideable).toBe(true);
      expect(b.pauses).toHaveLength(1);
      const hidden = hiddenFrom(cfgWith(modelPause("opus", 120)));
      // A later poll reports the same window a few seconds off.
      const jittered = summarizeDispatchStatus(
        cfgWith({ ...modelPause("opus", 120), until: until(120.5) }),
        null,
        nowMs + 30_000,
      );
      expect(pauseWindowsHidden(jittered, hidden)).toBe(true);
    });

    it("shows again for a new pause, a new window, or an escalation", () => {
      const hidden = hiddenFrom(cfgWith(modelPause("opus", 120)));
      const another = summarizeDispatchStatus(
        cfgWith(modelPause("opus", 120), modelPause("sonnet", 60)),
        null,
        nowMs,
      );
      expect(pauseWindowsHidden(another, hidden)).toBe(false);
      const renewed = summarizeDispatchStatus(
        cfgWith(modelPause("opus", 300)),
        null,
        nowMs,
      );
      expect(pauseWindowsHidden(renewed, hidden)).toBe(false);
      const escalated = summarizeDispatchStatus(
        cfgWith({ connection: "claude", until: until(120) }),
        null,
        nowMs,
      );
      expect(escalated?.hideable).toBe(true);
      expect(pauseWindowsHidden(escalated, hidden)).toBe(false);
    });

    it("never hides a deployment pause, dispatch block, or exhausted fallback", () => {
      expect(
        summarizeDispatchStatus(
          {
            ...cfgWith(modelPause("opus", 120)),
            pipelinePause: { paused: true, reason: "maintenance" },
          },
          null,
          nowMs,
        )?.hideable,
      ).toBe(false);
      expect(
        summarizeDispatchStatus({ reconcilerDown: true } as never, null, nowMs)
          ?.hideable,
      ).toBe(false);
      expect(
        summarizeDispatchStatus(
          {},
          { warnings: ["claude falls back to nothing until 9:00"] },
          nowMs,
        )?.hideable,
      ).toBe(false);
      expect(pauseWindowsHidden(null, [])).toBe(false);
    });

    it("prunes hidden pauses once their window has ended", () => {
      const hidden = hiddenFrom(cfgWith(modelPause("opus", 120)));
      expect(pruneHiddenPauses(hidden, nowMs + 60 * 60_000)).toHaveLength(1);
      expect(pruneHiddenPauses(hidden, nowMs + 130 * 60_000)).toHaveLength(0);
      const noEnd = [{ id: "warning|x", untilMs: null, hiddenAtMs: nowMs }];
      expect(pruneHiddenPauses(noEnd, nowMs + 3600_000)).toHaveLength(1);
      expect(pruneHiddenPauses(noEnd, nowMs + 13 * 3600_000)).toHaveLength(0);
    });
  });
});
