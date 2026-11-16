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
import { branchTemplateError, sampleBranchName } from "./branch-template";

describe("branchTemplateError", () => {
  it("accepts the default and the documented templates", () => {
    for (const t of [
      "",
      "grain/task-{id}",
      "grain/{deployment}/task-{id}",
      "grain/{deployment}/task-{id}-{slug}",
    ]) {
      expect(branchTemplateError(t, "abcd1234"), t).toBeNull();
    }
  });

  it("refuses what the server refuses, naming the field", () => {
    const cases: [string, RegExp][] = [
      ["grain/task", /must contain \{id\}/],
      ["grain/{slug}/task-{id}", /only end in \{slug\}/],
      ["task-{id}-{slug}-{slug}", /only once/],
      ["grain/{deployment}-task-{id}-{slug}", /out of the part/],
      ["grain/{nope}/task-{id}", /unknown placeholder/],
      ["bad..name {id}", /not a valid git branch name/],
      ["grain/task-{id}.lock", /not a valid git branch name/],
      ["x".repeat(250) + "{id}", /over the 244-character limit/],
    ];
    for (const [t, want] of cases) {
      const err = branchTemplateError(t, "abcd1234");
      expect(err, t).toMatch(want);
      expect(err, t).toMatch(/^Branch name template /);
    }
  });

  it("samples a branch with the deployment ID in it", () => {
    expect(
      sampleBranchName("grain/{deployment}/task-{id}-{slug}", "abcd1234"),
    ).toBe("grain/abcd1234/task-42");
  });
});
