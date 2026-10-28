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

// Settings -> Sandboxes' Disk overcommit field (grain/task-785), against
// the real demo server: it sits directly under Memory overcommit at the
// same left edge and width, at desktop and phone widths, and a value
// typed into it is what the stored row holds afterwards.

async function stored(page, key) {
  const settings = await page.evaluate(async () =>
    (await fetch("/api/settings")).json(),
  );
  return settings[key];
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`lays the disk overcommit field out under memory overcommit (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/sandboxes");
    const memory = page.getByLabel(/^memory overcommit$/i);
    const disk = page.getByLabel(/^disk overcommit$/i);
    await disk.scrollIntoViewIfNeeded();
    await expect(disk).toBeVisible();
    const m = await memory.boundingBox();
    const d = await disk.boundingBox();
    expect(Math.abs(d.x - m.x)).toBeLessThan(1);
    expect(Math.abs(d.width - m.width)).toBeLessThan(1);
    expect(d.y).toBeGreaterThan(m.y + m.height);
    // Nothing spills past the viewport sideways.
    expect(d.x + d.width).toBeLessThanOrEqual(viewport.width);
  });
}

test("saves the disk overcommit and restores it", async ({ page }) => {
  await page.goto("/settings/sandboxes");
  const before = await stored(page, "sandboxDiskOvercommit");
  const disk = page.getByLabel(/^disk overcommit$/i);
  try {
    await disk.fill("10");
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await disk.press("Tab");
    expect((await saved).status()).toBe(200);
    await expect.poll(() => stored(page, "sandboxDiskOvercommit")).toBe(10);
  } finally {
    await page.evaluate(async (v) => {
      await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sandboxDiskOvercommit: v }),
      });
    }, before);
  }
});
