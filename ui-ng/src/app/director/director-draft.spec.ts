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

import {
  buildShapingPrompt,
  diffDraftFields,
  extractGrainDrafts,
  extractGrainPlans,
  type TaskShapeFields,
} from "./director-draft";

describe("director-draft helpers", () => {
  it("extracts grain-draft and tagged json blocks while ignoring unrelated json blocks", () => {
    const md = [
      "Here is a sample config:",
      "```json",
      '{"unrelated": true}',
      "```",
      "And here is your refined task:",
      "```grain-draft",
      JSON.stringify({
        title: "Add backoff jitter",
        repo: "acme/web",
        priority: 8,
        dependsOn: ["12"],
        description: "Add exponential jitter.",
      }),
      "```",
      "Alternate json tag:",
      "```json",
      JSON.stringify({
        grainDraft: true,
        title: "Updated title",
      }),
      "```",
    ].join("\n");

    const drafts = extractGrainDrafts(md);
    expect(drafts).toEqual([
      {
        title: "Add backoff jitter",
        repo: "acme/web",
        priority: 8,
        dependsOn: ["12"],
        description: "Add exponential jitter.",
      },
      {
        title: "Updated title",
      },
    ]);
  });

  it("extracts grain-plan DAG blocks cleanly", () => {
    const md = [
      "```grain-plan",
      JSON.stringify({
        title: "Telemetry overhaul",
        repo: "acme/web",
        tasks: [
          { key: "api", title: "Expose latency histogram" },
          { key: "ui", title: "Render chart", dependsOn: ["api"] },
        ],
      }),
      "```",
    ].join("\n");
    const plans = extractGrainPlans(md);
    expect(plans).toHaveLength(1);
    expect(plans[0].title).toBe("Telemetry overhaul");
    expect(plans[0].tasks.map((t) => t.title)).toEqual([
      "Expose latency histogram",
      "Render chart",
    ]);
    expect(plans[0].tasks[1].dependsOn).toEqual(["api"]);
  });

  it("computes clear field diffs and formats shaping prompts", () => {
    const cur: TaskShapeFields = {
      title: "Fix retry",
      description: "Old body",
      repo: "acme/web",
      base: "main",
      priority: 5,
      model: "",
      dependsOn: [],
    };
    const diffs = diffDraftFields(cur, {
      title: "Fix retry backoff",
      priority: 8,
      dependsOn: ["44"],
    });
    expect(diffs.map((d) => d.field)).toEqual([
      "title",
      "priority",
      "dependsOn",
    ]);

    const prompt = buildShapingPrompt("edit", cur, "Narrow to HTTP 429.", {
      taskId: "71",
      state: "failed",
      failureSummary: "timed out",
    });
    expect(prompt).toContain("[Shape Task #71 · failed]");
    expect(prompt).toContain("Recent issue/context: timed out");
    expect(prompt).toContain("```grain-draft");
  });
});
