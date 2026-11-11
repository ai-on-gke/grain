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

// Queue order as the server lists it (taskQueueOrderSQL): priority, then
// manual order. Admission is not part of it, so a bypass task sits inside
// its priority run.
const backlog = [
  t("2", 8),
  t("3", 8),
  t("4", 5, { blocked: true }),
  t("1", 5, { admission: "bypass" }),
  t("5", 5),
  t("6", 3),
];

describe("task view", () => {
  it("groups the backlog into one band per priority, in queue order", () => {
    expect(
      bands(backlog).map((b) => [b.key, b.label, b.tasks.map((x) => x.id)]),
    ).toEqual([
      ["8", "High 8", ["2", "3"]],
      ["5", "Normal 5", ["4", "1", "5"]],
      ["3", "Low 3", ["6"]],
    ]);
    // A priority seen twice still gets distinct keys (and header ids).
    expect(bands([t("a", 5), t("b", 8), t("c", 5)]).map((b) => b.key)).toEqual([
      "5",
      "8",
      "5-2",
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
    expect(planMove(backlog, 1, 0)).toEqual({
      id: "3",
      afterId: "",
      beforeId: "2",
      priority: null,
      from: 1,
      to: 0,
    });
    expect(planMove(backlog, 2, 2)).toBeNull();
  });

  it("gives a task the band's priority when it moves into another band", () => {
    // #6 (Low 3) dropped between #2 and #3 joins High 8.
    expect(planMove(backlog, 5, 1)).toMatchObject({
      id: "6",
      afterId: "2",
      beforeId: "3",
      priority: 8,
    });
    // #2 moved to the bottom joins Low 3.
    expect(planMove(backlog, 0, 5)).toMatchObject({
      afterId: "6",
      beforeId: "",
      priority: 3,
    });
  });

  it("previews a proposal landing where the queue order already puts it", () => {
    const work = [
      t("2", 8),
      t("p", 8, { state: "proposed" }),
      t("3", 8),
      t("5", 5),
    ];
    expect(placement(work, work[1])).toEqual({
      position: 2,
      of: 4,
      band: "High 8",
    });
  });

  it("skips a task waiting on a dependency for next up", () => {
    // Admitted past worker limits: dispatched in an earlier pass.
    expect(nextUp(backlog)?.id).toBe("1");
    expect(nextUp([t("2", 8), t("3", 5)])?.id).toBe("2");
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
