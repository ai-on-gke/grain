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
  type Schedule,
  formatRunAt,
  scheduleNextRunLabel,
} from "./processes.models";

function sched(over: Partial<Schedule>): Schedule {
  return {
    id: "s1",
    title: "t",
    enabled: true,
    nextRunAt: "2026-11-16T06:00:00.123456789Z",
    ...over,
  } as Schedule;
}

describe("scheduleNextRunLabel", () => {
  it("says paused for a paused schedule rather than its stale nextRunAt", () => {
    expect(scheduleNextRunLabel(sched({ enabled: false }))).toBe("paused");
  });

  it("says suppressed rather than the placeholder far-future time", () => {
    const s = sched({
      nextRunAt: "2027-11-15T11:52:03.650805487Z",
      suppressed: { reason: "watchedRepoOffTarget", detail: "x" },
    } as Partial<Schedule>);
    expect(scheduleNextRunLabel(s)).toBe("suppressed");
  });

  it("formats an enabled schedule's next run instead of raw RFC 3339", () => {
    const label = scheduleNextRunLabel(sched({}));
    expect(label).not.toContain("T06:00");
    expect(label).not.toContain("Z");
    expect(label).toMatch(/2026/);
  });
});

describe("formatRunAt", () => {
  it("shows a dash for nothing and passes an unparsable value through", () => {
    expect(formatRunAt(undefined)).toBe("—");
    expect(formatRunAt("soon")).toBe("soon");
  });
});
