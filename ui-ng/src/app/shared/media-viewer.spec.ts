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
import { svgSize } from "./media-viewer.component";
import { MediaViewerService, withFreshSvgIds } from "./media-viewer.service";

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

describe("withFreshSvgIds", () => {
  it("renames the root id and every id scoped under it", () => {
    const svg =
      '<svg id="grain-mermaid-4" viewBox="0 0 1 1"><style>#grain-mermaid-4 .node{}</style>' +
      '<marker id="grain-mermaid-4_arrow"/><g id="grain-mermaid-4-flowchart-A-0"/>' +
      '<g id="grain-mermaid-40"/></svg>';
    const out = withFreshSvgIds(svg);
    const id = /id="([^"]+)"/.exec(out)![1];
    expect(id).not.toBe("grain-mermaid-4");
    expect(out).toContain(`#${id} .node`);
    expect(out).toContain(`${id}_arrow`);
    expect(out).toContain(`${id}-flowchart-A-0`);
    // grain-mermaid-40 is another diagram's id, not one under this one.
    expect(out).toContain('id="grain-mermaid-40"');
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
