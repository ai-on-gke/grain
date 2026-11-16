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
  batchAge,
  batchStanding,
  type MergeBatch,
} from "./merge-recheck-panel.component";

const base: MergeBatch = {
  repo: "acme/widgets",
  branch: "main",
  ref: "grain-merge-queue/x/recheck/5/main",
  startedAt: "2026-11-16T10:00:00Z",
  members: [{ task: "5" }],
};

describe("merge re-check panel", () => {
  it("reads elapsed time against the queue's maximum while running", () => {
    const now = new Date("2026-11-16T10:20:00Z").getTime();
    expect(batchAge({ ...base, running: true }, 5400, now)).toBe("20m of 90m");
    expect(batchAge({ ...base, running: true }, undefined, now)).toBe("20m");
    expect(
      batchAge(
        { ...base, running: false, finishedAt: "2026-11-16T10:07:00Z" },
        5400,
        now,
      ),
    ).toBe("ran for 7m");
    expect(batchAge({ ...base, startedAt: undefined }, 5400, now)).toBe("");
  });

  it("names each standing as the classic panel does", () => {
    expect(batchStanding({ ...base, running: true }).label).toBe("Testing");
    expect(batchStanding({ ...base, conclusion: "success" }).label).toBe(
      "Passed",
    );
    expect(batchStanding({ ...base, conclusion: "failure" }).tone).toBe(
      "error",
    );
    expect(batchStanding({ ...base, conclusion: "errored" }).label).toBe(
      "Abandoned",
    );
  });
});
