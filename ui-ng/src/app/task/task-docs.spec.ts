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
import type { TaskDetail } from "../api/models";
import { extractTaskDocuments } from "./task-docs";

function baseTask(patch: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: "42",
    title: "Add task Docs tab",
    state: "awaiting_submit",
    repo: "acme/web",
    createdAt: "2026-11-10T10:00:00Z",
    updatedAt: "2026-11-12T12:00:00Z",
    description: "",
    comments: [],
    ...patch,
  };
}

describe("extractTaskDocuments", () => {
  it("extracts prompt, PR markdown/doc diffs, and structured report comments while skipping short chat", () => {
    const diffText = [
      "diff --git a/pkg/server.go b/pkg/server.go",
      "--- a/pkg/server.go",
      "+++ b/pkg/server.go",
      "@@ -1,2 +1,2 @@",
      "-old",
      "+new",
      "diff --git a/docs/architecture.md b/docs/architecture.md",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/docs/architecture.md",
      "@@ -0,0 +1,4 @@",
      "+# Architecture",
      "+",
      "+- Flex scroll chain",
      "+- Container layout",
      "diff --git a/docs/deleted.md b/docs/deleted.md",
      "deleted file mode 100644",
      "--- a/docs/deleted.md",
      "+++ /dev/null",
      "@@ -1,2 +0,0 @@",
      "-# Old",
      "-Gone",
    ].join("\n");

    const task = baseTask({
      description: "## Goal\n\nBuild the task Docs tab.",
      comments: [
        {
          id: 1,
          author: "ana",
          authorKind: "human",
          authorLabel: "Ana",
          body: "LGTM, ship it.",
        },
        {
          id: 2,
          author: "agent",
          authorKind: "agent",
          summary: "Verification report",
          body: [
            "## Verification",
            "",
            "- Vitest unit suite passed",
            "- Production build passed",
          ].join("\n"),
          createdAt: "2026-11-12T11:00:00Z",
        },
        {
          id: 3,
          author: "agent",
          authorKind: "agent",
          handover: true,
          body: "Continuing after compact: remaining work is E2E verification.",
        },
      ],
    });

    const docs = extractTaskDocuments(task, { diffText });
    expect(docs.map((d) => [d.id, d.source, d.title])).toEqual([
      ["prompt", "prompt", "Task prompt"],
      ["pr:docs/architecture.md", "pr", "architecture.md"],
      ["comment-2", "comment", "Verification report"],
      ["comment-3", "comment", "Handover note"],
    ]);

    expect(docs[0].lineCount).toBe(3);
    expect(docs[1].path).toBe("docs/architecture.md");
    expect(docs[1].markdown).toContain("# Architecture");
    expect(docs[1].markdown).not.toContain("old");
    expect(docs[2].subtitle).toBe("agent");
    expect(docs[3].subtitle).toBe("agent · Handover");
  });

  it("reconstructs non-deleted lines across multiple diff hunks and skips deleted lines", () => {
    const diffText = [
      "diff --git a/design/design/ui-design-book.design.md b/design/design/ui-design-book.design.md",
      "--- a/design/design/ui-design-book.design.md",
      "+++ b/design/design/ui-design-book.design.md",
      "@@ -10,3 +10,3 @@",
      " ## Layout",
      "-Old viewport query",
      "+Container query at 1060px",
      "@@ -80,2 +80,2 @@",
      "-Legacy modal",
      "+Native doc-reader-modal",
    ].join("\n");

    const docs = extractTaskDocuments(baseTask(), { diffText });
    expect(docs).toHaveLength(1);
    expect(docs[0].markdown).toContain("Container query at 1060px");
    expect(docs[0].markdown).toContain("Native doc-reader-modal");
    expect(docs[0].markdown).not.toContain("Old viewport query");
    expect(docs[0].markdown).not.toContain("Legacy modal");
  });

  it("deduplicates identical documents across prompt, PR diff, and comments", () => {
    const shared =
      "# Release Notes\n\n- Fixed scroll containment\n- Added Docs tab";
    const diffText = [
      "diff --git a/README.md b/README.md",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/README.md",
      "@@ -0,0 +1,4 @@",
      "+# Release Notes",
      "+",
      "+- Fixed scroll containment",
      "+- Added Docs tab",
    ].join("\n");

    const task = baseTask({
      description: shared,
      comments: [
        { id: 10, author: "agent", authorKind: "agent", body: shared },
        {
          id: 11,
          author: "agent",
          authorKind: "agent",
          body: "## Extra Findings\n\n- Verified light theme\n- Verified dark theme",
        },
        {
          id: 12,
          author: "agent",
          authorKind: "agent",
          body: "## Extra Findings\n\n- Verified light theme\n- Verified dark theme",
        },
      ],
    });

    const docs = extractTaskDocuments(task, { diffText });
    expect(docs.map((d) => d.id)).toEqual(["prompt", "comment-11"]);
  });

  it("returns empty array when task has no description, doc diffs, or structured comments", () => {
    const docs = extractTaskDocuments(
      baseTask({
        description: "   ",
        comments: [{ id: 1, author: "ana", body: "Thanks!" }],
      }),
    );
    expect(docs).toEqual([]);
  });

  it("extracts quoted git diff paths with spaces and closes unclosed code fences across hunks", () => {
    const diffText = [
      'diff --git "a/docs/my plan.md" "b/docs/my plan.md"',
      "new file mode 100644",
      "--- /dev/null",
      '+++ "b/docs/my plan.md"',
      "@@ -0,0 +1,4 @@",
      "+# My Plan",
      "+",
      "+```ts",
      "+const x = 1;",
      "@@ -10,0 +15,2 @@",
      "+## Phase 2",
      "+Done.",
    ].join("\n");

    const docs = extractTaskDocuments(baseTask({}), { diffText });
    expect(docs.map((d) => d.id)).toEqual(["pr:docs/my plan.md"]);
    expect(docs[0].path).toBe("docs/my plan.md");
    expect(docs[0].markdown).toContain("```ts\nconst x = 1;\n```");
    expect(docs[0].markdown).toContain("---\n\n## Phase 2");
  });
});
