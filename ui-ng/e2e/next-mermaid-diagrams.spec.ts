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

import { test, expect } from "./fixtures";

// ```mermaid fences in a task's conversation render as diagrams through
// [gMarkdown] (shared/mermaid.ts): a sanitized SVG drawn by the lazily
// loaded mermaid chunk, a Diagram / Source toggle, an explained fallback
// for a source that does not parse, and Before / After fences paired
// side by side.

const COMMENT = [
  "Here is the request path:",
  "",
  "```mermaid Request flow",
  "flowchart LR",
  "  Browser --> Proxy --> Daemon",
  "```",
  "",
  "```mermaid before",
  "flowchart TD",
  "  A[Monolith] --> DB[(Store)]",
  "```",
  "",
  "```mermaid after: split",
  "flowchart TD",
  "  A[API] --> Q[Queue] --> W[Worker] --> DB[(Store)]",
  "```",
  "",
  "```mermaid Broken",
  "flowchart TD",
  "  A --> --> ((",
  "```",
].join("\n");

async function api(
  baseURL: string,
  code: string,
  method: string,
  path: string,
  body?: unknown,
) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

test("a task comment's Mermaid fences render as diagrams @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}, testInfo) => {
  test.setTimeout(90_000);
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid diagrams ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body: COMMENT },
    );
    await page.goto(`task/${task.id}`);

    const flow = page.getByRole("region", { name: "Request flow" });
    await expect(flow.locator("svg")).toBeVisible({ timeout: 20_000 });
    await expect(flow.locator("svg")).toContainText("Daemon");
    await expect(flow.getByLabel("Request flow source")).toBeHidden();

    // Source shows the fence text in place of the drawing.
    await flow.getByRole("button", { name: "Source" }).click();
    await expect(flow.getByRole("button", { name: "Source" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(flow.getByLabel("Request flow source")).toContainText(
      "Browser --> Proxy --> Daemon",
    );
    await expect(flow.locator("svg")).toBeHidden();
    await flow.getByRole("button", { name: "Diagram" }).click();
    await expect(flow.locator("svg")).toBeVisible();

    // Before / After pair: side by side on a desktop, Before alone on a
    // phone, and each layout button shows what it says.
    const pair = page.getByRole("region", {
      name: "Before and after diagrams",
    });
    const before = pair
      .getByRole("group", { name: "Before", exact: true })
      .locator("svg");
    const after = pair
      .getByRole("group", { name: "After: split", exact: true })
      .locator("svg");
    await expect(before).toBeVisible({ timeout: 20_000 });
    if (testInfo.project.name === "phone") {
      await expect(after).toBeHidden();
      await pair.getByRole("button", { name: "After" }).click();
      await expect(after).toBeVisible();
      await expect(before).toBeHidden();
    } else {
      await expect(after).toBeVisible();
      const b = (await before.boundingBox())!;
      const a = (await after.boundingBox())!;
      expect(a.x).toBeGreaterThan(b.x + b.width - 1);
      expect(Math.abs(a.y - b.y)).toBeLessThan(40);
      await pair.getByRole("button", { name: "Stacked" }).click();
      const b2 = (await before.boundingBox())!;
      const a2 = (await after.boundingBox())!;
      expect(a2.y).toBeGreaterThan(b2.y + b2.height - 1);
      await pair.getByRole("button", { name: "Side by side" }).click();
    }

    // A source that does not parse says so and shows the source.
    const broken = page.getByRole("region", { name: "Broken" });
    await expect(broken.getByRole("status")).toContainText(
      "Diagram could not be rendered",
      { timeout: 20_000 },
    );
    await expect(broken.getByLabel("Broken source")).toContainText("A -->");

    // Nothing the diagrams put in the page can run script or load
    // anything from elsewhere.
    expect(
      await page
        .locator(
          ".mermaid-canvas script, .mermaid-canvas foreignObject, .mermaid-canvas image",
        )
        .count(),
    ).toBe(0);

    // Nothing overflows sideways.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    expect(await axe()).toEqual([]);
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("a theme switch redraws a diagram in the other palette in place", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  await page.addInitScript(() =>
    localStorage.setItem("grain.themeMode", "light"),
  );
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid theme ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body: "```mermaid Themed\nflowchart LR\n  A --> B\n```" },
    );
    await page.goto(`task/${task.id}`);
    const svg = page.getByRole("region", { name: "Themed" }).locator("svg");
    await expect(svg).toBeVisible({ timeout: 20_000 });
    const nodeFill = () =>
      svg
        .locator(".node rect, .node path")
        .first()
        .evaluate((el) => getComputedStyle(el).fill);
    const light = await nodeFill();
    // "t" switches the theme in place; the diagram is redrawn, not
    // re-rendered from scratch, so its Source / Diagram choice survives.
    await page.locator("body").press("t");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect.poll(nodeFill).not.toBe(light);
    await expect(svg).toBeVisible();
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
