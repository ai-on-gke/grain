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
  type SuiteRun,
  derivedRuns,
  movedOrder,
  parseProcessSort,
  sortProcessRows,
} from "./processes.models";

function sched(over: Partial<Schedule>): Schedule {
  return {
    id: "s",
    title: "",
    description: "",
    repo: "acme/widgets",
    autoMerge: false,
    capabilities: [],
    recurrence: { kind: "daily", timeOfDay: "09:00" },
    overlap: "skip",
    source: "api",
    type: "",
    enabled: true,
    nextRunAt: "",
    createdAt: "2026-11-01T00:00:00Z",
    ...over,
  } as Schedule;
}

describe("derivedRuns", () => {
  const schedules = [
    sched({
      id: "a",
      templateId: "t1",
      nextRunAt: "2026-11-20T09:00:00Z",
      lastRunAt: "2026-11-10T09:00:00Z",
    }),
    sched({
      id: "b",
      templateId: "t1",
      nextRunAt: "2026-11-16T09:00:00Z",
      lastRunAt: "2026-11-12T09:00:00Z",
    }),
    // Paused, suppressed and event schedules have no next run to offer.
    sched({
      id: "c",
      templateId: "t1",
      enabled: false,
      nextRunAt: "2026-11-15T15:00:00Z",
    }),
    sched({
      id: "d",
      templateId: "t1",
      nextRunAt: "2026-11-15T16:00:00Z",
      suppressed: { reason: "watchedRepoOffTarget" } as Schedule["suppressed"],
    }),
    sched({
      id: "e",
      suiteId: "st1",
      recurrence: { kind: "git_ref" },
      nextRunAt: "2027-11-15T00:00:00Z",
    }),
  ];

  it("takes the soonest next and newest last run of the schedules firing a template", () => {
    expect(derivedRuns(schedules, "templateId", "t1")).toEqual({
      nextRunAt: "2026-11-16T09:00:00Z",
      lastRunAt: "2026-11-12T09:00:00Z",
    });
  });

  it("is on demand with nothing scheduled, and counts a suite's own runs as its last", () => {
    const runs = [
      { suiteId: "st1", createdAt: "2026-11-14T00:00:00Z" },
      { suiteId: "other", createdAt: "2026-11-15T00:00:00Z" },
    ] as SuiteRun[];
    expect(derivedRuns(schedules, "suiteId", "st1", runs)).toEqual({
      nextRunAt: null,
      lastRunAt: "2026-11-14T00:00:00Z",
    });
    expect(derivedRuns(schedules, "templateId", "nobody")).toEqual({
      nextRunAt: null,
      lastRunAt: null,
    });
  });
});

describe("sortProcessRows", () => {
  const rows = [
    {
      key: "k:b",
      name: "Beta",
      kind: "trigger",
      nextRunAt: null,
      lastRunAt: "2026-11-12T00:00:00Z",
      item: "b",
    },
    {
      key: "k:a",
      name: "Alpha",
      kind: "schedule",
      nextRunAt: "2026-11-18T00:00:00Z",
      lastRunAt: null,
      item: "a",
    },
    {
      key: "k:c",
      name: "Gamma",
      kind: "schedule",
      nextRunAt: "2026-11-16T00:00:00Z",
      lastRunAt: "2026-11-14T00:00:00Z",
      item: "c",
    },
  ];
  const order = (sort: string, custom: string[] = []) =>
    sortProcessRows(rows, parseProcessSort(sort), custom).map((r) => r.item);

  it("orders by next run soonest first and last run newest first, timeless rows last", () => {
    expect(order("next")).toEqual(["c", "a", "b"]);
    expect(order("last")).toEqual(["c", "b", "a"]);
  });

  it("orders by name and by kind", () => {
    expect(order("name")).toEqual(["a", "b", "c"]);
    expect(order("kind")).toEqual(["a", "c", "b"]);
  });

  it("puts a custom order's rows first and the rest by name; an unknown sort is custom", () => {
    expect(order("custom", ["k:c", "gone"])).toEqual(["c", "a", "b"]);
    expect(order("bogus")).toEqual(["a", "b", "c"]);
  });
});

describe("movedOrder", () => {
  it("moves a row one place and keeps the stored keys that are off screen", () => {
    expect(movedOrder(["x", "k:a"], ["k:a", "k:b", "k:c"], "k:b", -1)).toEqual([
      "k:b",
      "k:a",
      "k:c",
      "x",
    ]);
    expect(movedOrder([], ["k:a", "k:b"], "k:a", -1)).toBeNull();
  });
});
