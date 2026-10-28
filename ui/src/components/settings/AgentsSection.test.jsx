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
