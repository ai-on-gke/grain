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
  formatElapsed,
  stepElapsedMs,
  upgradeProgress,
} from "./upgradeProgress.js";

describe("upgradeProgress", () => {
  const plan = (...states) =>
    states.map((state, i) => ({ name: `step-${i}`, state }));

  it("reports nothing to show for a status with no steps", () => {
    // What a status file written by an older grain looks like, and what
    // the panel falls back to its plain phase/detail display for.
    expect(upgradeProgress(undefined)).toMatchObject({
      total: 0,
      settled: 0,
      percent: 0,
      current: null,
      position: 0,
    });
  });

  it("counts settled steps and names the running one", () => {
    const got = upgradeProgress(plan("done", "running", "pending", "pending"));
    expect(got.total).toBe(4);
    expect(got.settled).toBe(1);
    expect(got.percent).toBe(25);
    expect(got.current.name).toBe("step-1");
    expect(got.position).toBe(2);
  });

  it("counts a skipped step as behind it", () => {
    // A health check this deployment has configured away is a step the
    // upgrade will never spend time on, so a bar that refused to count
    // it would sit short of full for the whole of a successful upgrade.
    const got = upgradeProgress(plan("done", "skipped", "done"));
    expect(got.percent).toBe(100);
    expect(got.current).toBe(null);
  });

  it("points at the failed step when nothing is running", () => {
    const got = upgradeProgress(plan("done", "failed", "pending"));
    expect(got.current.name).toBe("step-1");
    expect(got.position).toBe(2);
  });
});

describe("stepElapsedMs", () => {
  it("measures a finished step between its own timestamps", () => {
    const step = {
      startedAt: "2026-09-06T10:00:00Z",
      finishedAt: "2026-09-06T10:02:30Z",
    };
    expect(stepElapsedMs(step, Date.parse("2026-09-06T11:00:00Z"))).toBe(
      150000,
    );
  });

  it("measures a running step up to now", () => {
    const step = { startedAt: "2026-09-06T10:00:00Z" };
    expect(stepElapsedMs(step, Date.parse("2026-09-06T10:00:09Z"))).toBe(9000);
  });

  it("has nothing to say about a step that never started", () => {
    expect(stepElapsedMs({ state: "pending" })).toBe(null);
    expect(stepElapsedMs(null)).toBe(null);
    expect(stepElapsedMs({ startedAt: "not a time" })).toBe(null);
  });
});

describe("formatElapsed", () => {
  it("keeps to the units that matter at each scale", () => {
    expect(formatElapsed(0)).toBe("0s");
    expect(formatElapsed(9_400)).toBe("9s");
    expect(formatElapsed(60_000)).toBe("1m 0s");
    expect(formatElapsed(605_000)).toBe("10m 5s");
    expect(formatElapsed(3_600_000)).toBe("1h 0m");
    expect(formatElapsed(4_500_000)).toBe("1h 15m");
  });

  it("prints nothing for a duration there isn't one of", () => {
    expect(formatElapsed(null)).toBe("");
    expect(formatElapsed(undefined)).toBe("");
  });
});
