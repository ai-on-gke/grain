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

import { earliestWakeDay, zonedDay, zonedDayStart } from "./zoned-day";

describe("zoned-day", () => {
  it("starts a day at midnight in the named zone", () => {
    expect(new Date(zonedDayStart("2030-01-15", "UTC")!).toISOString()).toBe(
      "2030-01-15T00:00:00.000Z",
    );
    expect(
      new Date(zonedDayStart("2030-01-15", "Asia/Tokyo")!).toISOString(),
    ).toBe("2030-01-14T15:00:00.000Z");
    // Across a DST change: New York is UTC-4 in July and UTC-5 in March's
    // first days.
    expect(
      new Date(zonedDayStart("2030-07-04", "America/New_York")!).toISOString(),
    ).toBe("2030-07-04T04:00:00.000Z");
    expect(
      new Date(zonedDayStart("2030-03-10", "America/New_York")!).toISOString(),
    ).toBe("2030-03-10T05:00:00.000Z");
    expect(zonedDayStart("", "UTC")).toBeNull();
    expect(zonedDayStart("15/01/2030", "UTC")).toBeNull();
  });

  it("offers tomorrow in the zone as the earliest wake day", () => {
    // 20:00 UTC on Nov 14 is already Nov 15 in Tokyo.
    const now = Date.parse("2026-11-14T20:00:00Z");
    expect(zonedDay(now, "Asia/Tokyo")).toBe("2026-11-15");
    expect(earliestWakeDay("Asia/Tokyo", now)).toBe("2026-11-16");
    expect(earliestWakeDay("UTC", now)).toBe("2026-11-15");
    expect(earliestWakeDay("UTC", Date.parse("2026-12-31T12:00:00Z"))).toBe(
      "2027-01-01",
    );
  });
});
