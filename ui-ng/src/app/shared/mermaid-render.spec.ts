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
import { renderMarkdown } from "../sessions/session-view";
import { sanitizeMarkdownHtml } from "./code-highlight";
import { svgSize } from "./media-viewer.component";
import { MediaViewerService } from "./media-viewer.service";
import {
  mermaidConfig,
  mermaidErrorText,
  sanitizeMermaidSvg,
  withFreshIds,
} from "./mermaid-render";

describe("mermaid fences", () => {
  it("wrap the code card in a .mermaid-block that survives the sanitizer", () => {
    const html = sanitizeMarkdownHtml(
      renderMarkdown("```mermaid\nflowchart LR\n  A --> B\n```"),
    );
    const div = document.createElement("div");
    div.innerHTML = html;
    const block = div.querySelector(".mermaid-block");
    expect(block).not.toBeNull();
    expect(block!.querySelector(".code-card pre code")!.textContent).toContain(
      "A --> B",
    );
  });

  it("leave other fences alone", () => {
    expect(renderMarkdown("```go\nfunc main() {}\n```")).not.toContain(
      "mermaid-block",
    );
  });
});

describe("sanitizeMermaidSvg", () => {
  it("drops scripts, handlers, foreign HTML and unsafe links", () => {
    const out = sanitizeMermaidSvg(
      '<svg id="g-mermaid-1" viewBox="0 0 10 10" onload="alert(1)">' +
        "<script>alert(2)</script>" +
        "<foreignObject><div>x</div></foreignObject>" +
        '<a href="javascript:alert(3)"><text>j</text></a>' +
        '<a href="https://example.com/x"><text>ok</text></a>' +
        '<path marker-end="url(#g-mermaid-1_arrow)" style="fill: url(https://evil.example/x)"/>' +
        '<use href="#g-mermaid-1_arrow"/>' +
        "</svg>",
    );
    expect(out).not.toMatch(/script|onload|foreignObject|javascript:|evil/i);
    expect(out).toContain('href="https://example.com/x"');
    expect(out).toContain('target="_blank"');
    expect(out).toContain('href="#g-mermaid-1_arrow"');
    expect(out).toContain('role="img"');
    expect(out).toContain('aria-label="Diagram"');
  });

  it("keeps a <style> from fetching anything remote", () => {
    const out = sanitizeMermaidSvg(
      '<svg id="g-mermaid-2" viewBox="0 0 1 1"><style>' +
        "@import url(https://evil.example/a.css);" +
        "#g-mermaid-2 .a{background:url( 'https://evil.example/p.png' )}" +
        "#g-mermaid-2 .b{fill:url(#g-mermaid-2-grad)}" +
        "</style></svg>",
    );
    expect(out).not.toContain("evil");
    expect(out).toContain("url(#g-mermaid-2-grad)");
  });

  it("refuses output that is not an SVG", () => {
    expect(() => sanitizeMermaidSvg("<p>nope</p>")).toThrow();
  });
});

describe("withFreshIds", () => {
  it("renames the root id and every id scoped under it", () => {
    const svg =
      '<svg id="g-mermaid-4" viewBox="0 0 1 1"><style>#g-mermaid-4 .node{}</style>' +
      '<marker id="g-mermaid-4_arrow"/><g id="g-mermaid-40"/></svg>';
    const out = withFreshIds(svg);
    const id = /id="(g-mermaid-\d+)"/.exec(out)![1];
    expect(id).not.toBe("g-mermaid-4");
    expect(out).toContain(`#${id} .node`);
    expect(out).toContain(`${id}_arrow`);
    // g-mermaid-40 is a different diagram's id, not one under g-mermaid-4.
    expect(out).toContain('id="g-mermaid-40"');
  });
});

describe("mermaidConfig", () => {
  it("locks strict security and plain SVG labels", () => {
    const c = mermaidConfig("dark");
    expect(c.securityLevel).toBe("strict");
    expect(c.htmlLabels).toBe(false);
    expect(c.secure).toContain("securityLevel");
    expect(c.themeVariables.darkMode).toBe(true);
  });
});

describe("mermaidErrorText", () => {
  it("keeps the first line of Mermaid's error", () => {
    expect(
      mermaidErrorText(new Error("Parse error on line 2:\n...A -->\n---^")),
    ).toBe("Parse error on line 2");
  });
});

describe("svgSize", () => {
  it("reads the viewBox", () => {
    expect(svgSize('<svg id="x" viewBox="4 4 736 95.5">')).toEqual({
      w: 736,
      h: 95.5,
    });
    expect(svgSize("<svg>")).toBeNull();
  });
});

describe("MediaViewerService", () => {
  it("opens at an index, wraps when stepping and closes", () => {
    const v = new MediaViewerService();
    const items = ["a", "b", "c"].map((n) => ({
      kind: "image" as const,
      name: n,
      src: `/${n}`,
    }));
    v.open(items, 2);
    expect(v.state()!.index).toBe(2);
    v.step(1);
    expect(v.state()!.index).toBe(0);
    v.step(-1);
    expect(v.state()!.index).toBe(2);
    v.close();
    expect(v.isOpen()).toBe(false);
  });
});
