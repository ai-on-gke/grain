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
import remarkMermaid, {
  classifyBeforeAfterHeader,
  parseMermaidFenceMeta,
} from "./mermaidPair.js";

const code = (lang, value, meta = null) => ({
  type: "code",
  lang,
  meta,
  value,
});

const heading = (depth, text) => ({
  type: "heading",
  depth,
  children: [{ type: "text", value: text }],
});

const paragraph = (...children) => ({
  type: "paragraph",
  children,
});

const strong = (text) => ({
  type: "strong",
  children: [{ type: "text", value: text }],
});

const text = (value) => ({ type: "text", value });

function runPlugin(children) {
  const tree = { type: "root", children: structuredClone(children) };
  remarkMermaid()(tree);
  return tree.children;
}

describe("parseMermaidFenceMeta", () => {
  it("classifies before/after roles and standalone titles", () => {
    expect(parseMermaidFenceMeta("")).toEqual({
      role: null,
      label: "",
      title: "",
    });
    expect(parseMermaidFenceMeta("before")).toEqual({
      role: "before",
      label: "Before",
      title: "before",
    });
    expect(parseMermaidFenceMeta("before: monolith")).toEqual({
      role: "before",
      label: "Before: monolith",
      title: "before: monolith",
    });
    expect(parseMermaidFenceMeta('title="After: Split worker"')).toEqual({
      role: "after",
      label: "After: Split worker",
      title: "After: Split worker",
    });
    expect(parseMermaidFenceMeta("Dispatch lifecycle")).toEqual({
      role: null,
      label: "",
      title: "Dispatch lifecycle",
    });
  });
});

describe("classifyBeforeAfterHeader", () => {
  it("matches Before and After headings and bold labels, but rejects prose sentences", () => {
    expect(classifyBeforeAfterHeader(heading(3, "Before"))).toEqual({
      kind: "heading:3",
      role: "before",
      label: "Before",
    });
    expect(
      classifyBeforeAfterHeader(heading(3, "After: Event-driven outbox")),
    ).toEqual({
      kind: "heading:3",
      role: "after",
      label: "After: Event-driven outbox",
    });
    expect(classifyBeforeAfterHeader(paragraph(strong("Before:")))).toEqual({
      kind: "label",
      role: "before",
      label: "Before",
    });
    expect(
      classifyBeforeAfterHeader(
        paragraph(strong("Before"), text(" we deploy, verify migrations.")),
      ),
    ).toBeNull();
  });
});

describe("remarkMermaid", () => {
  it("pairs ### Before and ### After Mermaid sections into one comparison node", () => {
    const got = runPlugin([
      heading(3, "Before: Direct DB write"),
      code("mermaid", "flowchart LR\n  API --> DB"),
      heading(3, "After: Outbox worker"),
      code("mermaid", "flowchart LR\n  API --> Outbox --> DB"),
    ]);

    expect(got).toHaveLength(1);
    expect(got[0]).toMatchObject({
      type: "mermaidComparison",
      data: {
        hName: "mermaid-comparison",
        hProperties: {
          "data-before-code": "flowchart LR\n  API --> DB",
          "data-after-code": "flowchart LR\n  API --> Outbox --> DB",
          "data-before-label": "Before: Direct DB write",
          "data-after-label": "After: Outbox worker",
        },
      },
    });
  });

  it("pairs consecutive ```mermaid before and ```mermaid after fences", () => {
    const got = runPlugin([
      code("mermaid", "flowchart TD\n  A --> B", "before"),
      code("mermaid", "flowchart TD\n  A --> C --> B", "after"),
    ]);

    expect(got).toHaveLength(1);
    expect(got[0].data.hName).toBe("mermaid-comparison");
    expect(got[0].data.hProperties["data-before-label"]).toBe("Before");
    expect(got[0].data.hProperties["data-after-label"]).toBe("After");
  });

  it("leaves prose between Before and After untouched rather than reordering it", () => {
    const got = runPlugin([
      heading(3, "Before"),
      code("mermaid", "flowchart LR\n  A --> B"),
      paragraph(text("Today the handler writes synchronously.")),
      heading(3, "After"),
      code("mermaid", "flowchart LR\n  A --> Q --> B"),
    ]);

    expect(got).toHaveLength(5);
    expect(got[1].data.hName).toBe("mermaid-diagram");
    expect(got[2].type).toBe("paragraph");
    expect(got[4].data.hName).toBe("mermaid-diagram");
  });

  it("does not pair headings of different depths", () => {
    const got = runPlugin([
      heading(2, "Before"),
      code("mermaid", "flowchart LR\n  A --> B"),
      heading(4, "After"),
      code("mermaid", "flowchart LR\n  A --> C"),
    ]);

    expect(got).toHaveLength(4);
    expect(got[1].data.hName).toBe("mermaid-diagram");
    expect(got[3].data.hName).toBe("mermaid-diagram");
  });

  it("converts standalone mermaid fences and leaves other languages untouched", () => {
    const got = runPlugin([
      code("mermaid", "sequenceDiagram\n  A->>B: ping", "Handshake"),
      code("go", "func main() {}"),
    ]);

    expect(got[0]).toMatchObject({
      type: "mermaidDiagram",
      data: {
        hName: "mermaid-diagram",
        hProperties: {
          "data-code": "sequenceDiagram\n  A->>B: ping",
          "data-title": "Handshake",
        },
      },
    });
    expect(got[1]).toEqual(code("go", "func main() {}"));
  });
});
