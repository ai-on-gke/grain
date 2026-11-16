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
import type { Task } from "../api/models";
import { taskRowKey } from "./task-row-key";

const base: Task = { id: "7", title: "T", state: "awaiting_submit" };

describe("taskRowKey", () => {
  it("moves with fields GET /api/tasks rows actually carry", () => {
    const k = taskRowKey(base);
    expect(taskRowKey({ ...base })).toBe(k);
    for (const change of [
      { state: "running" },
      { activityAt: "2026-11-16T00:00:01Z" },
      { questionCommentId: 9 },
      { prMergedAt: "2026-11-16T00:00:02Z" },
      { mergeQueueState: "queued" },
      { mergeGates: [{ id: "ci", label: "CI", state: "passed" }] },
      { humanReviewHold: [{ reason: "x" }] },
    ] as Partial<Task>[]) {
      expect(taskRowKey({ ...base, ...change })).not.toBe(k);
    }
  });

  it("is empty for a missing row", () => {
    expect(taskRowKey(undefined)).toBe("");
  });
});
