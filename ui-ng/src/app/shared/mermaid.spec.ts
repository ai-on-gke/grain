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

import { Component, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { renderMarkdown } from "../sessions/session-view";
import { ThemeService } from "../services/theme.service";
import { MarkdownDirective, sanitizeMarkdownHtml } from "./code-highlight";
import {
  mermaidConfigForMode,
  parseMermaidFenceMeta,
  sanitizeMermaidSvg,
  setMermaidLoaderForTest,
} from "./mermaid";

function render(src: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = sanitizeMarkdownHtml(renderMarkdown(src, { mermaid: true }));
  return host;
}

describe("mermaid markup", () => {
  it("leaves mermaid fences as code cards unless asked", () => {
    const html = renderMarkdown("```mermaid\ngraph TD; A-->B\n```");
    expect(html).toContain('class="code-card"');
    expect(html).not.toContain("data-mermaid");
  });

  it("turns a fence into a diagram block that survives the sanitizer", () => {
    const host = render(
      "Intro\n\n```mermaid Request flow\ngraph TD; A-->B<script>\n```\n\nafter",
    );
    const block = host.querySelector<HTMLElement>("[data-mermaid]");
    expect(block?.dataset["mermaid"]).toBe("diagram");
    expect(block?.getAttribute("aria-label")).toBe("Request flow");
    expect(block?.querySelector(".mermaid-title")?.textContent).toBe(
      "Request flow",
    );
    const views = Array.from(
      block!.querySelectorAll<HTMLButtonElement>("[data-mermaid-view]"),
    ).map((b) => [b.textContent, b.getAttribute("aria-pressed")]);
    expect(views).toEqual([
      ["Diagram", "true"],
      ["Source", "false"],
    ]);
    // The source is text, never markup.
    expect(block?.querySelector(".mermaid-source code")?.textContent).toBe(
      "graph TD; A-->B<script>",
    );
    expect(host.querySelector("script")).toBeNull();
    expect(host.textContent).toContain("Intro");
    expect(host.textContent).toContain("after");
  });

  it("keeps reference links working beside a diagram", () => {
    const host = render(
      "```mermaid\ngraph TD; A\n```\n\n[docs][d]\n\n[d]: https://grain.dev/x",
    );
    expect(host.querySelector("a")?.getAttribute("href")).toBe(
      "https://grain.dev/x",
    );
  });

  it("draws diagrams nested in lists and quotes", () => {
    const host = render(
      "- step\n\n  ```mermaid\n  graph TD; A\n  ```\n\n> ~~~mermaid\n> graph TD; B\n> ~~~",
    );
    expect(host.querySelectorAll('[data-mermaid="diagram"]').length).toBe(2);
    expect(host.querySelector("li [data-mermaid]")).not.toBeNull();
    expect(host.querySelector("blockquote [data-mermaid]")).not.toBeNull();
  });

  it("draws a fence that opens on a list marker", () => {
    for (const src of [
      "1. ```mermaid\n   graph TD; A\n   ```",
      "- ```mermaid\n  graph TD; A\n  ```",
      "> - ```mermaid\n>   graph TD; A\n>   ```",
    ]) {
      expect(
        render(src).querySelector("li [data-mermaid]"),
        src,
      ).not.toBeNull();
    }
  });

  it("pairs before / after fences named in the info string", () => {
    const host = render(
      "```mermaid before: monolith\ngraph TD; A\n```\n\n```mermaid after\ngraph TD; B\n```",
    );
    const blocks = host.querySelectorAll<HTMLElement>("[data-mermaid]");
    expect(blocks.length).toBe(1);
    expect(blocks[0].dataset["mermaid"]).toBe("pair");
    const tags = Array.from(
      blocks[0].querySelectorAll(".mermaid-state-tag"),
    ).map((t) => t.textContent);
    expect(tags).toEqual(["Before: monolith", "After"]);
    const sources = Array.from(
      blocks[0].querySelectorAll(".mermaid-source code"),
    ).map((c) => c.textContent);
    expect(sources).toEqual(["graph TD; A", "graph TD; B"]);
  });

  it("pairs diagrams under matching Before / After headings", () => {
    const host = render(
      "### Before\n\n```mermaid\ngraph TD; A\n```\n\n### After: split\n\n```mermaid\ngraph TD; B\n```",
    );
    const block = host.querySelector<HTMLElement>("[data-mermaid]");
    expect(block?.dataset["mermaid"]).toBe("pair");
    expect(host.querySelector("h3")).toBeNull();
    expect(
      Array.from(block!.querySelectorAll(".mermaid-state-tag")).map(
        (t) => t.textContent,
      ),
    ).toEqual(["Before", "After: split"]);
  });

  it("does not pair across mismatched headings or prose", () => {
    const mismatched = render(
      "## Before\n\n```mermaid\ngraph TD; A\n```\n\n### After\n\n```mermaid\ngraph TD; B\n```",
    );
    expect(mismatched.querySelectorAll('[data-mermaid="diagram"]').length).toBe(
      2,
    );
    const prose = render(
      "**Before** we deploy, look:\n\n```mermaid\ngraph TD; A\n```\n\n**After:**\n\n```mermaid\ngraph TD; B\n```",
    );
    expect(prose.querySelector('[data-mermaid="pair"]')).toBeNull();
    expect(prose.textContent).toContain("we deploy");
  });

  it("reads titles from fence metadata", () => {
    expect(parseMermaidFenceMeta('title="Request pipeline"')).toEqual({
      role: null,
      label: "",
      title: "Request pipeline",
    });
    expect(parseMermaidFenceMeta("beforehand").role).toBeNull();
    expect(parseMermaidFenceMeta("after (v2)").role).toBe("after");
  });
});

describe("sanitizeMermaidSvg", () => {
  it("strips scripts, handlers, fetches and unsafe links", () => {
    const out = sanitizeMermaidSvg(
      `<svg height="100" onload="alert(1)" style="width:10px">
        <script>alert(1)</script>
        <foreignObject><img src="https://t.example/p.gif"></foreignObject>
        <image href="https://t.example/p.png"></image>
        <animate attributeName="href" to="javascript:alert(1)"></animate>
        <style>@import url(https://t.example/x.css);</style>
        <a href="javascript:alert(1)"><text>bad</text></a>
        <a href="https://grain.dev/"><text>ok</text></a>
        <path marker-end="url(#arrow)" style="fill: url(https://t.example/f)"></path>
        <use href="#arrowhead"></use>
      </svg>`,
    );
    const host = document.createElement("div");
    host.innerHTML = out;
    const svg = host.querySelector("svg")!;
    expect(svg.getAttribute("onload")).toBeNull();
    expect(svg.getAttribute("height")).toBeNull();
    expect(svg.getAttribute("style")).toContain("max-width: 100%");
    for (const tag of [
      "script",
      "foreignObject",
      "img",
      "image",
      "animate",
      "style",
    ]) {
      expect(host.getElementsByTagName(tag).length, tag).toBe(0);
    }
    const links = Array.from(host.querySelectorAll("a"));
    expect(links[0].getAttribute("href")).toBeNull();
    expect(links[1].getAttribute("href")).toBe("https://grain.dev/");
    expect(links[1].getAttribute("rel")).toBe("noopener noreferrer");
    expect(host.querySelector("path")?.getAttribute("style")).toBeNull();
    expect(host.querySelector("path")?.getAttribute("marker-end")).toBe(
      "url(#arrow)",
    );
    expect(host.querySelector("use")?.getAttribute("href")).toBe("#arrowhead");
  });

  it("keeps a diagram legible rather than shrinking it to fit", () => {
    const host = document.createElement("div");
    host.innerHTML = sanitizeMermaidSvg(
      '<svg viewBox="0 0 800 100" style="max-width: 800px;"></svg>',
    );
    const style = host.querySelector("svg")!.getAttribute("style")!;
    expect(style).toContain("max-width: 800px");
    expect(style).toContain("min-width: 600px");
  });

  it("refuses output that is not an SVG", () => {
    expect(() => sanitizeMermaidSvg("<p>nope</p>")).toThrow(/SVG/);
  });
});

describe("mermaidConfigForMode", () => {
  // A %%{init}%% directive in a diagram may not loosen security nor pin
  // its own palette, fonts or CSS through a theme switch
  // (docs/test-runs/2026-11-16-next-markdown.md).
  it("locks security and theme keys against %%{init}%% directives", () => {
    for (const mode of ["light", "dark"] as const) {
      const cfg = mermaidConfigForMode(mode);
      expect(cfg.securityLevel).toBe("strict");
      expect(cfg.secure).toEqual(
        expect.arrayContaining([
          "secure",
          "securityLevel",
          "htmlLabels",
          "theme",
          "themeVariables",
          "themeCSS",
          "darkMode",
          "fontFamily",
        ]),
      );
    }
  });
});

@Component({
  imports: [MarkdownDirective],
  template: `<div [gMarkdown]="src()"></div>`,
})
class HostComponent {
  readonly src = signal("");
}

describe("[gMarkdown] diagrams", () => {
  const rendered: { source: string; theme: unknown }[] = [];
  let theme: unknown;

  beforeEach(() => {
    rendered.length = 0;
    setMermaidLoaderForTest(async () => ({
      initialize: (cfg: unknown) => {
        theme = (cfg as { themeVariables: { darkMode: boolean } })
          .themeVariables.darkMode;
      },
      render: async (_id: string, text: string) => {
        rendered.push({ source: text, theme });
        if (text.includes("oops")) throw new Error("Parse error on line 1:\nx");
        return {
          svg: `<svg onload="alert(1)"><text>${text.length}</text><script>x()</script></svg>`,
        };
      },
    }));
    localStorage.setItem("grain.themeMode", "light");
    TestBed.configureTestingModule({
      imports: [HostComponent],
      providers: [provideRouter([])],
    });
  });

  afterEach(() => {
    setMermaidLoaderForTest(null);
    localStorage.removeItem("grain.themeMode");
    TestBed.resetTestingModule();
  });

  async function settle(fixture: {
    detectChanges(): void;
    whenStable(): Promise<unknown>;
  }) {
    fixture.detectChanges();
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
      await fixture.whenStable();
    }
  }

  it("draws a sanitized SVG, toggles to source, and redraws on a theme change", async () => {
    const fixture = TestBed.createComponent(HostComponent);
    fixture.componentInstance.src.set("```mermaid\ngraph TD; A-->B\n```");
    await settle(fixture);
    const el: HTMLElement = fixture.nativeElement;
    const svg = el.querySelector(".mermaid-canvas svg");
    expect(svg).not.toBeNull();
    expect(svg?.getAttribute("onload")).toBeNull();
    expect(el.querySelector("script")).toBeNull();
    expect(rendered).toEqual([{ source: "graph TD; A-->B", theme: false }]);

    const block = el.querySelector<HTMLElement>("[data-mermaid]")!;
    expect(block.dataset["view"]).toBe("diagram");
    const sourceBtn = Array.from(
      block.querySelectorAll<HTMLButtonElement>("button"),
    ).find((b) => b.textContent === "Source")!;
    sourceBtn.click();
    expect(block.dataset["view"]).toBe("source");
    expect(sourceBtn.getAttribute("aria-pressed")).toBe("true");

    TestBed.inject(ThemeService).setMode("dark");
    await settle(fixture);
    // Same DOM (the Source choice is kept), redrawn in dark.
    expect(el.querySelector<HTMLElement>("[data-mermaid]")).toBe(block);
    expect(block.dataset["view"]).toBe("source");
    expect(rendered.at(-1)).toEqual({ source: "graph TD; A-->B", theme: true });
  });

  it("draws a diagram only once it nears the viewport", async () => {
    const observed: Element[] = [];
    let fire: (els: Element[]) => void = () => undefined;
    class FakeIO {
      constructor(
        cb: (
          e: { isIntersecting: boolean; target: Element }[],
          o: FakeIO,
        ) => void,
      ) {
        fire = (els) =>
          cb(
            els.map((target) => ({ isIntersecting: true, target })),
            this,
          );
      }
      observe(el: Element) {
        observed.push(el);
      }
      unobserve(el: Element) {
        observed.splice(observed.indexOf(el), 1);
      }
      disconnect() {}
    }
    const win = window as unknown as { IntersectionObserver?: unknown };
    const prev = win.IntersectionObserver;
    win.IntersectionObserver = FakeIO;
    try {
      const fixture = TestBed.createComponent(HostComponent);
      fixture.componentInstance.src.set(
        "```mermaid\ngraph LR; Far-->Away\n```",
      );
      await settle(fixture);
      const el: HTMLElement = fixture.nativeElement;
      expect(rendered).toEqual([]);
      expect(el.querySelector(".mermaid-canvas svg")).toBeNull();
      expect(observed).toHaveLength(1);

      fire([...observed]);
      await settle(fixture);
      expect(rendered.map((r) => r.source)).toEqual(["graph LR; Far-->Away"]);
      expect(el.querySelector(".mermaid-canvas svg")).not.toBeNull();
      expect(observed).toHaveLength(0);
    } finally {
      win.IntersectionObserver = prev;
    }
  });

  it("explains a diagram that does not parse and shows its source", async () => {
    const fixture = TestBed.createComponent(HostComponent);
    fixture.componentInstance.src.set("```mermaid\ngraph oops\n```");
    await settle(fixture);
    const el: HTMLElement = fixture.nativeElement;
    const note = el.querySelector('[data-mermaid] [role="status"]');
    expect(note?.textContent).toBe(
      "Diagram could not be rendered (Parse error on line 1:). Showing the Mermaid source.",
    );
    expect(el.querySelector(".mermaid-pane")?.classList).toContain("is-error");
  });

  it("never loads mermaid for a message without a diagram", async () => {
    const fixture = TestBed.createComponent(HostComponent);
    fixture.componentInstance.src.set("```ts\nconst x = 1;\n```");
    await settle(fixture);
    expect(rendered).toEqual([]);
    expect(fixture.nativeElement.querySelector(".code-card")).not.toBeNull();
  });
});
