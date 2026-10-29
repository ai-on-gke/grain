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

import { expect, test } from "./fixtures.js";

// The 2026-10-28 nightly walkthrough's Mermaid half (docs/test-runs/
// 2026-10-28-navigation-sessions-ui.md): task comments carrying valid,
// malformed, hostile, very large and before/after Mermaid, posted over
// the real API and read on the real task page at a desktop width and on
// a phone. Two of these were bugs the walkthrough found:
//
//   - a 400-node chain rendered ~31,700px tall inline, so one comment
//     was the whole thread. The inline canvas is now held to a box that
//     scrolls (style.css, .mermaid-block .mermaid-canvas).
//   - a diagram over Mermaid's maxTextSize rendered Mermaid's stock
//     "Maximum text size in diagram exceeded" graph in place of the
//     source. It now takes the fallback a syntax error does
//     (mermaidRender.js's MAX_DIAGRAM_CHARS).
//
// The task is filed by the test and closed afterwards, so nothing here is
// left on the worker's shared demo for another file to find.

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };
const F = "```";

const chain = (n) =>
  "flowchart TD\n" +
  Array.from(
    { length: n },
    (_, i) => `  N${i}[Node ${i}] --> N${i + 1}[Node ${i + 1}]`,
  ).join("\n");

const COMMENTS = [
  `Valid diagram:\n\n${F}mermaid\nflowchart LR\n  A[Client] --> B{Gate}\n  B -->|ok| C[Server]\n${F}`,
  `Malformed diagram:\n\n${F}mermaid\nflowchart LR\n  A[Client --> ((B\n${F}`,
  `Hostile diagram:\n\n${F}mermaid\n%%{init: {"securityLevel": "loose", "htmlLabels": true}}%%\nflowchart LR\n  A["<script>window.__mermaidXss=1</script>label"] --> B["<img src=x onerror='window.__mermaidXss=2'>"]\n  click A "javascript:window.__mermaidXss=3" "tip"\n  click B call alert("x")\n${F}`,
  `Long diagram:\n\n${F}mermaid\n${chain(400)}\n${F}`,
  `Oversized diagram:\n\n${F}mermaid\n${chain(1500)}\n${F}`,
  `Comparison:\n\n### Before\n\n${F}mermaid\nflowchart LR\n  A[Monolith] --> DB[(DB)]\n${F}\n\n### After\n\n${F}mermaid\nflowchart LR\n  A[API] --> S[Service] --> DB[(DB)]\n${F}`,
];

// block is the Mermaid block (or comparison) in the comment whose first
// paragraph starts with label.
function block(page, label) {
  return page.locator(
    `xpath=//p[starts-with(normalize-space(.), "${label}")]` +
      `/following::div[contains(concat(" ", @class, " "), " mermaid-block ")][1]`,
  );
}

test("Mermaid in task comments: renders, falls back, stays contained, runs no script", async ({
  page,
}) => {
  const consoleErrors = [];
  page.on("console", (msg) => {
    // The demo has no GitHub, so a task page's branch picker asks for
    // GitHub's branch list and is told 404: that is the demo, not Mermaid.
    if (msg.type() === "error" && !/404 \(Not Found\)/.test(msg.text())) {
      consoleErrors.push(msg.text());
    }
  });
  const dialogs = [];
  page.on("dialog", (d) => {
    dialogs.push(d.message());
    d.dismiss();
  });

  const created = await page.request.post("/api/tasks", {
    data: { title: "Mermaid walkthrough", description: "e2e" },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    for (const body of COMMENTS) {
      const res = await page.request.post(`/api/tasks/${id}/comments`, {
        data: { body },
      });
      expect(res.ok(), `posting ${body.split(":")[0]}`).toBeTruthy();
    }

    for (const size of [DESKTOP, PHONE]) {
      await page.setViewportSize(size);
      await page.goto(`/tasks/${id}`);
      await expect(page.getByText("Comparison:")).toBeVisible();
      await expect(page.locator(".mermaid-loading")).toHaveCount(0, {
        timeout: 30000,
      });

      await expect(
        block(page, "Valid diagram:").locator(".mermaid-svg-wrap svg"),
      ).toBeVisible();

      const malformed = block(page, "Malformed diagram:");
      await expect(malformed.locator(".mermaid-fallback-note")).toContainText(
        "could not be rendered",
      );
      await expect(malformed.locator("pre.mermaid-source")).toContainText(
        "A[Client --> ((B",
      );

      // Hostile labels render as text or not at all: no script, image or
      // handler reaches the page, and nothing set the flag they try to.
      const hostile = block(page, "Hostile diagram:");
      await expect(hostile.locator(".mermaid-svg-wrap svg")).toBeVisible();
      expect(
        await hostile
          .locator(
            "script, img, foreignObject, [onerror], [onclick], a[href^='javascript']",
          )
          .count(),
      ).toBe(0);
      await hostile
        .locator("svg text", { hasText: "label" })
        .first()
        .click({ force: true });

      // A long chain scrolls inside its own box rather than making the
      // comment tens of thousands of pixels tall.
      const long = block(page, "Long diagram:");
      const canvas = long.locator(".mermaid-canvas");
      await expect(canvas.locator("svg")).toBeVisible();
      const box = await canvas.boundingBox();
      expect(
        Math.abs(box.height - Math.min(size.height * 0.7, 640)),
        `long diagram's canvas height at ${size.width}px`,
      ).toBeLessThan(1);
      expect(
        await canvas.evaluate((el) => el.scrollHeight > el.clientHeight + 100),
      ).toBe(true);

      // Over Mermaid's text limit: the source and the reason, not
      // Mermaid's stock error graph.
      const oversized = block(page, "Oversized diagram:");
      await expect(oversized.locator(".mermaid-fallback-note")).toContainText(
        /characters, over the 50,000 Mermaid renders/,
      );
      await expect(oversized.locator("svg text")).toHaveCount(0);
      const source = await oversized
        .locator("pre.mermaid-source")
        .boundingBox();
      expect(source.height).toBeLessThanOrEqual(
        Math.min(size.height * 0.7, 640) + 1,
      );

      const comparison = block(page, "Comparison:");
      await expect(comparison).toHaveClass(/mermaid-comparison/);
      // Side by side on a desktop; a phone opens on Before alone and
      // flips to After in place.
      const panes = comparison.locator(".mermaid-svg-wrap svg");
      if (size === DESKTOP) {
        await expect(panes).toHaveCount(2);
      } else {
        await expect(panes).toHaveCount(1);
        await expect(panes.first()).toContainText("Monolith");
        await comparison.getByRole("button", { name: "After" }).click();
        await expect(panes).toHaveCount(1);
        await expect(panes.first()).toContainText("Service");
      }

      const { scrollWidth, clientWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(
        scrollWidth,
        `no sideways scroll at ${size.width}px`,
      ).toBeLessThanOrEqual(clientWidth + 1);
    }

    expect(await page.evaluate(() => window.__mermaidXss)).toBeUndefined();
    expect(dialogs).toEqual([]);
    expect(consoleErrors).toEqual([]);
  } finally {
    await page.request.post(`/api/tasks/${id}/close`, { data: {} });
  }
});
