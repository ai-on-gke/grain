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

import { beforeEach, describe, expect, it } from "vitest";
import {
  clearMermaidCache,
  getCachedMermaidSvg,
  mermaidConfigForMode,
  renderMermaidSvg,
  sanitizeMermaidSvg,
} from "./mermaidRender.js";

describe("mermaidConfigForMode", () => {
  it("locks strict security settings and switches palettes by theme mode", () => {
    const light = mermaidConfigForMode("light");
    const dark = mermaidConfigForMode("dark");

    expect(light.securityLevel).toBe("strict");
    expect(dark.securityLevel).toBe("strict");
    expect(light.secure).toContain("securityLevel");
    expect(light.themeVariables.darkMode).toBe(false);
    expect(dark.themeVariables.darkMode).toBe(true);
    expect(light.themeVariables.primaryBorderColor).toBe("#8a6a2e");
    expect(dark.themeVariables.primaryBorderColor).toBe("#d9a441");
  });
});

describe("sanitizeMermaidSvg", () => {
  it("strips dangerous elements, inline event handlers, and unsafe URL schemes", () => {
    const dirty = `
      <svg viewBox="0 0 100 50" height="500" onload="alert(1)">
        <script>alert(2)</script>
        <foreignObject><iframe src="javascript:alert(3)"></iframe><div onclick="alert(4)">Node</div></foreignObject>
        <a href="javascript:alert(5)"><text>bad link</text></a>
        <a xlink:href="data:text/html,<script>alert(6)</script>"><text>bad data</text></a>
        <a href="https://example.com/docs"><text>good link</text></a>
        <a href="#task-42"><text>task link</text></a>
        <path marker-end="url(#arrow)" style="fill: url(javascript:alert(7))" />
        <use href="#arrow" />
      </svg>
    `;

    const clean = sanitizeMermaidSvg(dirty);
    const doc = new DOMParser().parseFromString(clean, "text/html");
    const svg = doc.body.querySelector("svg");

    expect(svg).not.toBeNull();
    expect(svg.querySelector("script")).toBeNull();
    expect(svg.querySelector("iframe")).toBeNull();
    expect(svg.hasAttribute("onload")).toBe(false);
    expect(svg.hasAttribute("height")).toBe(false);

    for (const el of [svg, ...svg.querySelectorAll("*")]) {
      for (const attr of el.attributes) {
        expect(attr.name).not.toMatch(/^on/i);
        expect(attr.value).not.toMatch(/javascript:|data:|vbscript:/i);
      }
    }

    const links = [...svg.querySelectorAll("a")];
    expect(links[0].hasAttribute("href")).toBe(false);
    expect(links[1].hasAttribute("xlink:href")).toBe(false);
    expect(links[2].getAttribute("href")).toBe("https://example.com/docs");
    expect(links[2].getAttribute("target")).toBe("_blank");
    expect(links[2].getAttribute("rel")).toBe("noopener noreferrer");
    // Fragment links stay in-page so #task-42 opens in place.
    expect(links[3].getAttribute("href")).toBe("#task-42");
    expect(links[3].hasAttribute("target")).toBe(false);
    expect(svg.querySelector("use").getAttribute("href")).toBe("#arrow");
  });
});

describe("renderMermaidSvg", () => {
  beforeEach(() => {
    clearMermaidCache();
  });

  it("renders valid diagrams, populates the cache, and keeps working after a syntax error", async () => {
    const valid = "flowchart LR\n  API[API Gateway] --> DB[(Postgres)]";

    await expect(
      renderMermaidSvg("not a valid diagram %%% ???", "light"),
    ).rejects.toThrow();

    const svg = await renderMermaidSvg(valid, "light");
    expect(svg).toContain("<svg");
    const parsed = new DOMParser().parseFromString(svg, "text/html");
    expect(parsed.body.textContent).toContain("API Gateway");
    expect(parsed.body.textContent).toContain("Postgres");
    expect(getCachedMermaidSvg("light", valid)).toBe(svg);
    expect(getCachedMermaidSvg("dark", valid)).toBeNull();
  });
});
