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

import { test, expect } from "./fixtures.js";

// ```mermaid fences and attached images on /next/task/<id>
// (docs/test-runs/2026-11-15-task-intake-inbox-lifecycle.md, step 4.11): a
// valid diagram draws as SVG, an invalid one keeps its source under a note,
// and both diagrams and images open in the full-screen viewer (X, backdrop
// or Escape closes; arrows page with an "i / N" counter; Download). Runs in
// the desktop, phone and dark projects.

// Two solid 64x40 PNGs, one colour each.
const PNGS = [
  "iVBORw0KGgoAAAANSUhEUgAAAEAAAAAoCAIAAADBrGu+AAAAPUlEQVR42u3PQQkAAAgEsAt2/VMYxgw+hcEKLNO+FgEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQGBiwW7BUjEu4zs7QAAAABJRU5ErkJggg==",
  "iVBORw0KGgoAAAANSUhEUgAAAEAAAAAoCAIAAADBrGu+AAAAPUlEQVR42u3PQQkAAAgEsAtmCCMa1Qw+hcEKLDX9WgQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQELhYRLLimCvq9UQAAAABJRU5ErkJggg==",
];

const DESCRIPTION = [
  "The request path, before the change:",
  "",
  "```mermaid",
  "flowchart LR",
  "  Client --> Proxy --> Daemon",
  "  Daemon --> Store[(Store)]",
  "```",
  "",
  "And a diagram that does not parse:",
  "",
  "```mermaid",
  "flowchart LR",
  "  A -->",
  "```",
].join("\n");

async function apiJson(baseURL, code, method, p, body) {
  const res = await fetch(`${baseURL}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

function inside(box, vw, vh) {
  expect(box.x).toBeGreaterThanOrEqual(-0.5);
  expect(box.y).toBeGreaterThanOrEqual(-0.5);
  expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
  expect(box.y + box.height).toBeLessThanOrEqual(vh + 0.5);
}

test("Mermaid diagrams and attached images open full screen on the task page @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  test.setTimeout(120_000);
  const { width: vw, height: vh } = page.viewportSize();
  const stamp = Date.now();
  const names = [`before-${stamp}.png`, `after-${stamp}.png`];
  const task = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Media viewer ${stamp}`,
    repo: "acme/widgets",
    description: DESCRIPTION,
    attachments: names.map((filename, i) => ({
      filename,
      contentType: "image/png",
      content: PNGS[i],
    })),
  });
  try {
    // On a phone the description is on the Details tab.
    await page.goto(`/next/task/${task.id}?tab=details`);
    // A phone keeps a hidden second copy of the details; use what shows.
    const blocks = page.locator(".mermaid-block").filter({ visible: true });
    const desc = blocks.first().locator("xpath=..");

    // The valid fence is a diagram, inside the description and the window.
    const good = blocks.nth(0);
    const svg = good.locator(".mermaid-canvas svg");
    await expect(svg).toBeVisible({ timeout: 20_000 });
    await expect(good.locator(":scope > .code-card")).toBeHidden();
    // The diagram's frame stays inside the description and the window;
    // a diagram wider than the pane scrolls inside it rather than
    // shrinking its labels past 60%.
    const descBox = await desc.boundingBox();
    const canvasBox = await good.locator(".mermaid-canvas").boundingBox();
    expect(canvasBox.x).toBeGreaterThanOrEqual(descBox.x - 0.5);
    expect(canvasBox.x + canvasBox.width).toBeLessThanOrEqual(
      descBox.x + descBox.width + 0.5,
    );
    expect(canvasBox.x + canvasBox.width).toBeLessThanOrEqual(vw + 0.5);
    const svgBox = await svg.boundingBox();
    const natural = await svg.evaluate((el) => el.viewBox.baseVal.width);
    expect(svgBox.width).toBeGreaterThanOrEqual(natural * 0.6 - 1);
    expect(svgBox.width).toBeLessThanOrEqual(natural + 1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      ),
    ).toBeLessThanOrEqual(0);
    await expect(svg.locator("text", { hasText: "Proxy" })).toHaveCount(1);

    // Source shows the fence under the diagram, and hides it again.
    const source = good.getByRole("button", { name: "Source" });
    await source.click();
    await expect(source).toHaveAttribute("aria-pressed", "true");
    await expect(good.locator(":scope > .code-card")).toBeVisible();
    await source.click();
    await expect(good.locator(":scope > .code-card")).toBeHidden();

    // The invalid fence keeps its source under a note naming the problem.
    const bad = blocks.nth(1);
    await expect(bad.locator(".mermaid-note")).toContainText(
      "Diagram could not be rendered",
    );
    await expect(bad.locator(".code-card")).toBeVisible();
    await expect(bad.locator(".code-card pre")).toContainText("A -->");
    await expect(bad.locator(".mermaid-canvas")).toHaveCount(0);

    // Expand opens the diagram full screen, fitted to the window; zoom
    // makes it bigger; Escape closes and gives focus back.
    const viewer = page.getByTestId("media-viewer");
    const expand = good.locator(".code-card-head").getByRole("button", {
      name: "Open diagram full screen",
    });
    await expand.focus();
    await page.keyboard.press("Enter");
    await expect(viewer).toBeVisible();
    const big = viewer.locator(".mv-svg svg");
    await expect(big).toBeVisible();
    await expect(page.getByTestId("media-viewer-count")).toHaveCount(0);
    const fit = await big.boundingBox();
    inside(fit, vw, vh);
    // Fitted: as wide as the stage allows, or its own width if smaller.
    expect(fit.width).toBeGreaterThanOrEqual(Math.min(natural, vw - 128) - 1);
    inside(await page.getByTestId("media-viewer-close").boundingBox(), vw, vh);
    await page.getByTestId("media-viewer-zoom-in").click();
    await expect(page.getByTestId("media-viewer-zoom")).toHaveText("125%");
    const zoomed = await big.boundingBox();
    expect(zoomed.width).toBeGreaterThan(fit.width * 1.1);
    await page.keyboard.press("0");
    await expect(page.getByTestId("media-viewer-zoom")).toHaveText("100%");
    expect(await axe()).toEqual([]);
    await page.keyboard.press("Escape");
    await expect(viewer).toBeHidden();
    await expect(expand).toBeFocused();

    // The images open in the viewer too, paged with an "i / N" counter.
    const detail = await apiJson(
      demoServer,
      accessCode,
      "GET",
      `/api/tasks/${task.id}`,
    );
    const ids = names.map(
      (n) => detail.attachments.find((a) => a.filename === n).id,
    );
    const second = page
      .getByTestId(`attachment-${ids[1]}`)
      .filter({ visible: true });
    await second.click();
    await expect(viewer).toBeVisible();
    const count = page.getByTestId("media-viewer-count");
    const title = page.getByTestId("media-viewer-title");
    const image = page.getByTestId("media-viewer-image");
    await expect(count).toHaveText("2 / 2");
    await expect(title).toHaveText(names[1]);
    await expect(image).toHaveAttribute("alt", names[1]);
    await page.keyboard.press("ArrowRight");
    await expect(count).toHaveText("1 / 2");
    await expect(title).toHaveText(names[0]);
    await page.keyboard.press("ArrowLeft");
    await expect(count).toHaveText("2 / 2");
    await page.getByRole("button", { name: "Previous image" }).click();
    await expect(count).toHaveText("1 / 2");

    // Arrows sit level with the middle of the stage, one at each side;
    // the image, the bar and Close stay on screen.
    const prev = await page
      .getByRole("button", { name: "Previous image" })
      .boundingBox();
    const next = await page
      .getByRole("button", { name: "Next image" })
      .boundingBox();
    inside(prev, vw, vh);
    inside(next, vw, vh);
    expect(prev.x + prev.width).toBeLessThan(vw / 2);
    expect(next.x).toBeGreaterThan(vw / 2);
    expect(Math.abs(prev.y - next.y)).toBeLessThan(1);
    const stage = await page.getByTestId("media-viewer-stage").boundingBox();
    expect(
      Math.abs(prev.y + prev.height / 2 - (stage.y + stage.height / 2)),
    ).toBeLessThan(2);
    await expect(image).toBeVisible();
    inside(await image.boundingBox(), vw, vh);
    const close = await page.getByTestId("media-viewer-close").boundingBox();
    inside(close, vw, vh);
    expect(close.y + close.height).toBeLessThanOrEqual(stage.y + 0.5);

    // Download saves the file under its own name.
    const download = page.getByTestId("media-viewer-download");
    await expect(download).toHaveAttribute("download", names[0]);
    await expect(download).toHaveAttribute(
      "href",
      new RegExp(`/api/tasks/${task.id}/attachments/${ids[0]}$`),
    );

    // A click on the backdrop closes; so does X.
    await page.mouse.click(stage.x + 4, stage.y + stage.height - 4);
    await expect(viewer).toBeHidden();
    await second.click();
    await expect(count).toHaveText("2 / 2");
    await page.getByTestId("media-viewer-close").click();
    await expect(viewer).toBeHidden();
    await expect(second).toBeFocused();
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
