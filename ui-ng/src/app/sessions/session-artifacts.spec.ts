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

import type { Comment } from "../api/models";
import {
  buildAgentModelOptions,
  extractSessionArtifacts,
  topologicalPlanOrder,
  wrapSandboxedHtml,
} from "./session-artifacts";

describe("session-artifacts helpers", () => {
  it("extracts HTML mocks, markdown specs, and grain-plan DAGs from non-human comments", () => {
    const comments: Comment[] = [
      {
        id: 1,
        author: "alice",
        authorKind: "human",
        body: "```html\n<h1>Ignored human html</h1>\n```",
      },
      {
        id: 2,
        author: "grain",
        authorKind: "automation",
        body: [
          "## Architecture Proposal",
          "",
          "Detailed breakdown of the API and UI state model so engineers can execute autonomously across phases.",
          "```html",
          "<title>Ops Preview</title><h1>Live Status</h1>",
          "```",
          "```grain-plan",
          JSON.stringify({
            title: "Ops Dashboard",
            repo: "acme/web",
            tasks: [
              { key: "ui", title: "Render dashboard", dependsOn: ["api"] },
              { key: "api", title: "Add metrics endpoint" },
            ],
          }),
          "```",
        ].join("\n"),
      },
    ];

    const res = extractSessionArtifacts(comments);
    expect(res.htmlMocks).toHaveLength(1);
    expect(res.htmlMocks[0].title).toBe("Ops Preview");
    expect(res.docs).toHaveLength(1);
    expect(res.docs[0].title).toBe("Architecture Proposal");
    expect(res.plans).toHaveLength(1);

    const ordered = topologicalPlanOrder(res.plans[0].plan.tasks);
    expect(ordered.map((t) => t.key)).toEqual(["api", "ui"]);
  });

  it("wraps HTML with a strict CSP and builds deduplicated agent model options", () => {
    const wrapped = wrapSandboxedHtml("<div>Hi</div>", "dark");
    expect(wrapped).toContain("Content-Security-Policy");
    expect(wrapped).toContain("connect-src 'none'");
    expect(wrapped).toContain("base-uri 'none'");
    expect(wrapped).toContain("color-scheme:dark");

    const hostile = wrapSandboxedHtml(
      '<!doctype html><html><head><base href="https://evil.test"><meta http-equiv="refresh" content="0;url=https://evil.test"></head><body><h1>Safe</h1></body></html>',
      "light",
    );
    expect(hostile).not.toContain("<base");
    expect(hostile).not.toContain('http-equiv="refresh"');
    expect(hostile).toContain("Content-Security-Policy");

    expect(() =>
      topologicalPlanOrder([
        { key: "a", title: "Step A", dependsOn: ["b"] },
        { key: "b", title: "Step B", dependsOn: ["a"] },
      ]),
    ).toThrow(/Dependency cycle/);

    const opts = buildAgentModelOptions(
      [
        {
          id: "gemini-3.1-pro-high",
          label: "Gemini 3.1 Pro",
          effort: "high",
          family: "gemini-3.1-pro",
        },
        {
          id: "unconfigured-cli-catalog-model",
          label: "Unconfigured CLI Model",
        },
      ],
      ["gemini-3.1-pro-high", "claude-sonnet-4"],
      "gemini-3.1-pro-high",
      "custom-pinned-model",
    );
    expect(opts.map((o) => o.value)).toEqual([
      "",
      "gemini-3.1-pro-high",
      "claude-sonnet-4",
      "custom-pinned-model",
    ]);
    expect(opts[1].label).toBe("Gemini 3.1 Pro · high");
  });
});
