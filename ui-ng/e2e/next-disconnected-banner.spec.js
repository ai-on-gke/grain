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

// A daemon that accepts every request and answers none (gap G1 of the
// 2026-11-15 responsive navigation walkthrough): /next/ kept showing the
// stale task list with nothing but the footer's "Updated 1m ago". The
// shell's disconnected banner (app.component.ts, fed by
// services/connection.service.ts) must stand above it at desktop and phone
// widths and take itself down once the daemon answers again;
// next-deployment-operations.spec.js covers the same banner on Fleet at
// desktop only.

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

for (const size of SIZES) {
  test(`a silent daemon raises the disconnected banner, which clears when it answers (${size.name})`, async ({
    page,
    axe,
  }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: size.width, height: size.height });
    await page.goto("tasks");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTestId("disconnected-banner")).toHaveCount(0);

    const held = [];
    let holding = true;
    await page.route("**/api/**", (route) => {
      if (holding) held.push(route);
      else void route.fallback();
    });
    try {
      const banner = page.getByTestId("disconnected-banner");
      await expect(banner).toBeVisible({ timeout: 30_000 });
      await expect(banner).toContainText(
        "it accepted the connection but sent no answer within 10 seconds",
      );
      await expect(banner).toHaveAttribute("role", "status");
      await expect(banner).toContainText("retrying automatically");
      // The page's own data stays where it was underneath.
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();

      // Above the shell and the page, and nothing wider than the window.
      const box = await banner.boundingBox();
      const stage = await page.locator("main#main").boundingBox();
      expect(box).not.toBeNull();
      expect(stage).not.toBeNull();
      expect(box.y + box.height).toBeLessThanOrEqual(stage.y + 1);
      const top = await page.locator(".top-bar").boundingBox();
      expect(top.y).toBeGreaterThanOrEqual(box.y + box.height - 0.5);
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(size.width);
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth,
      );
      expect(overflow).toBe(false);
      // Its copy wraps rather than being clipped.
      const clipped = await banner.evaluate(
        (el) =>
          el.scrollHeight > el.clientHeight + 1 ||
          el.scrollWidth > el.clientWidth + 1,
      );
      expect(clipped).toBe(false);
      for (const scheme of ["light", "dark"]) {
        await page.emulateMedia({ colorScheme: scheme });
        await expect(page.locator("html")).toHaveAttribute(
          "data-theme",
          scheme,
        );
        expect(await axe(), `axe with the banner up (${scheme})`).toEqual([]);
        await page.screenshot({
          path: test
            .info()
            .outputPath(`disconnected-${size.name}-${scheme}.png`),
        });
      }
      await page.emulateMedia({ colorScheme: "light" });
      await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

      // The daemon comes back: release what was held and let the rest
      // through. No reload.
      holding = false;
      for (const r of held.splice(0)) await r.fallback().catch(() => {});
      await expect(banner).toHaveCount(0, { timeout: 15_000 });
    } finally {
      holding = false;
      await page.unrouteAll({ behavior: "ignoreErrors" });
      for (const r of held) await r.abort().catch(() => {});
    }
  });
}

test("a refused connection raises the banner too", async ({ page }) => {
  test.setTimeout(60_000);
  await page.goto("tasks");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await page.route("**/api/**", (route) => route.abort("connectionrefused"));
  try {
    const banner = page.getByTestId("disconnected-banner");
    await expect(banner).toBeVisible({ timeout: 30_000 });
    await expect(banner).toContainText("the connection to it failed");
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await expect(banner).toHaveCount(0, { timeout: 15_000 });
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
  }
});
