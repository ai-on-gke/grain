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
import { linkTaskRefs, taskRefFromHref } from "./taskRefs.js";

describe("linkTaskRefs", () => {
  it("links a task named in grain's own words", () => {
    expect(linkTaskRefs("Filed task 1514 to review it.")).toBe(
      "Filed [task 1514](#task-1514) to review it.",
    );
    expect(linkTaskRefs("Task 3 left findings")).toBe(
      "[Task 3](#task-3) left findings",
    );
  });

  it("leaves code, branch names and existing links alone", () => {
    for (const body of [
      "run `go test task 5`",
      "```\ntask 5\n```",
      "on grain/task-5",
      "see [task 5](#task-5)",
      "multitask 5",
    ]) {
      expect(linkTaskRefs(body)).toBe(body);
    }
  });

  it("passes an empty body through", () => {
    expect(linkTaskRefs("")).toBe("");
    expect(linkTaskRefs(undefined)).toBeUndefined();
  });
});

describe("taskRefFromHref", () => {
  it("reads the id back off a link linkTaskRefs wrote, and nothing else", () => {
    expect(taskRefFromHref("#task-1514")).toBe("1514");
    expect(taskRefFromHref("https://example.com/#task-1")).toBe("");
    expect(taskRefFromHref(null)).toBe("");
  });
});
