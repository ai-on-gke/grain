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
import { taskBranchSuggestion } from "./AgentsSection.jsx";

// grain/task-56: the Task branches card's one-line "Use it" offer of the
// current default template.
describe("taskBranchSuggestion", () => {
  const slugged = "grain/{deployment}/task-{id}-{slug}";

  it("offers a deployment still on grain/task-{id} its deployment ID", () => {
    expect(
      taskBranchSuggestion({
        taskBranchTemplate: "",
        taskBranchTemplateDefault: slugged,
      }),
    ).toMatch(/grain\/task-\{id\}.*existing tasks keep theirs/);
  });

  it("offers the unslugged deployment form a few words of each title", () => {
    expect(
      taskBranchSuggestion({
        taskBranchTemplate: "grain/{deployment}/task-{id}",
        taskBranchTemplateDefault: slugged,
      }),
    ).toMatch(/named by their number alone.*task-\{id\}-\{slug\}/);
  });

  it("offers nothing to a deployment already on the default or a custom template", () => {
    for (const taskBranchTemplate of [slugged, "alice/task-{id}"]) {
      expect(
        taskBranchSuggestion({
          taskBranchTemplate,
          taskBranchTemplateDefault: slugged,
        }),
      ).toBeNull();
    }
  });
});
