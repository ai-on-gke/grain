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
});
