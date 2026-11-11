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

import type { Task } from "../api/models";
import {
  ago,
  bands,
  inTab,
  levelOf,
  nextUp,
  placement,
  planMove,
  sorted,
} from "./task-view";

const t = (id: string, priority: number, extra: Partial<Task> = {}): Task =>
  ({ id, title: `t${id}`, state: "queued", priority, ...extra }) as Task;

// Dispatch order as the server lists it: jumps first, then priority.
const backlog = [
  t("1", 5, { admission: "bypass" }),
  t("2", 8),
  t("3", 8),
  t("4", 5, { blocked: true }),
  t("5", 5),
  t("6", 3),
];

describe("task view", () => {
  it("groups the backlog into bands in dispatch order", () => {
    expect(
      bands(backlog).map((b) => [b.label, b.tasks.map((x) => x.id)]),
    ).toEqual([
      ["Jumps the queue", ["1"]],
      ["High 8", ["2", "3"]],
      ["Normal 5", ["4", "5"]],
      ["Low 3", ["6"]],
    ]);
  });

  it("names priority levels with Grain's number", () => {
    expect([9, 8, 7, 6, 5, 4, 3, 2, 1].map((p) => levelOf(p).name)).toEqual([
      "Urgent",
      "High",
      "High",
      "Normal",
      "Normal",
      "Low",
      "Low",
      "Lowest",
      "Lowest",
    ]);
  });

  it("plans a move within a band as new neighbours only", () => {
    expect(planMove(backlog, 2, 1)).toEqual({
      id: "3",
      afterId: "1",
      beforeId: "2",
      priority: null,
      from: 2,
      to: 1,
    });
    expect(planMove(backlog, 2, 2)).toBeNull();
  });

  it("gives a task the band's priority when it moves into another band", () => {
    // #6 (Low 3) dropped between #2 and #3 joins High 8.
    expect(planMove(backlog, 5, 2)).toMatchObject({
      id: "6",
      afterId: "2",
      beforeId: "3",
      priority: 8,
    });
    // #2 moved to the bottom joins Low 3.
    expect(planMove(backlog, 1, 5)).toMatchObject({
      afterId: "6",
      beforeId: "",
      priority: 3,
    });
  });

  it("previews where a proposal would land", () => {
    expect(placement(backlog, 8)).toEqual({
      position: 4,
      of: 7,
      band: "High 8",
    });
    expect(placement(backlog, 1)).toEqual({
      position: 7,
      of: 7,
      band: "Lowest 1",
    });
  });

  it("skips a task waiting on a dependency for next up", () => {
    expect(nextUp(backlog)?.id).toBe("1");
    expect(nextUp([t("4", 5, { blocked: true }), t("5", 5)])?.id).toBe("5");
    expect(nextUp([])).toBeNull();
  });

  it("sorts by priority then age, or by age", () => {
    const list = [
      t("a", 3, { createdAt: "2026-01-01" }),
      t("b", 8, { createdAt: "2026-01-03" }),
      t("c", 8, { createdAt: "2026-01-02" }),
    ];
    expect(sorted(list, "priority").map((x) => x.id)).toEqual(["c", "b", "a"]);
    expect(sorted(list, "oldest").map((x) => x.id)).toEqual(["a", "c", "b"]);
    expect(sorted(list, "newest").map((x) => x.id)).toEqual(["b", "c", "a"]);
    expect(sorted(list, "run").map((x) => x.id)).toEqual(["a", "b", "c"]);
  });

  it("puts states in their tabs", () => {
    expect(inTab(t("1", 5, { state: "failed" }), "asked")).toBe(true);
    expect(inTab(t("1", 5, { state: "completed" }), "merging")).toBe(true);
    expect(inTab(t("1", 5, { state: "closed" }), "all")).toBe(false);
    expect(inTab(t("1", 5, { state: "proposed" }), "proposed")).toBe(true);
  });

  it("says how long ago", () => {
    const now = Date.parse("2026-11-11T12:00:00Z");
    expect(ago("2026-11-11T11:56:00Z", now)).toBe("4m");
    expect(ago("2026-11-11T09:00:00Z", now)).toBe("3h");
    expect(ago("2026-11-08T12:00:00Z", now)).toBe("3d");
    expect(ago(undefined, now)).toBe("");
  });
});
