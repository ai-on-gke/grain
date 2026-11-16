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

// Regression coverage for task 5774 (from the 2026-11-14 responsive navigation
// walkthrough, docs/test-runs/2026-11-14-responsive-navigation-live-sessions.md):
//
//  1. When the daemon accepts connections on /api/** without answering, or
//     refuses connections outright, /next/ shows an announced disconnected
//     notice (role="status") on both desktop and phone, keeps existing rows on
//     screen without stacking a duplicate load-error banner, and clears
//     automatically without a reload once requests are answered again.
//  2. On phones (390x844, 320x568, and 844x390), where the bottom bar's build
//     stamp is hidden, the build stamp sits at the foot of the More sheet,
//     links to /next/settings/upgrade, and stays unclipped inside the sheet.

import type { Locator } from "@playwright/test";
import { expect, test } from "./fixtures";

async function hitsItself(locator: Locator): Promise<boolean> {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const hit = document.elementFromPoint(
      r.left + r.width / 2,
      r.top + r.height / 2,
    );
    return !!hit && el.contains(hit);
  });
}

test(
  "a hung daemon holding /api/** shows an announced disconnected notice and recovers after unroute",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    test.setTimeout(120_000);
    await page.goto("/next/tasks");
    await expect(
      page.getByRole("heading", { level: 1, name: "Tasks" }),
    ).toBeVisible();
    const banner = page.getByTestId("disconnected-banner");
    await expect(banner).toHaveCount(0);

    // Hold every new /api/** request open without answering, as a hung daemon
    // or half-open proxy socket does.
    await page.route("**/api/**", () => {
      // Intentionally left unanswered until unrouted/timed out.
    });

    await expect(banner).toBeVisible({ timeout: 24_000 });
    await expect(banner).toHaveAttribute("role", "status");
    await expect(banner).toContainText(/Server is not responding/i);
    await expect(banner).toContainText(/sent no answer/i);
    // Single failure notice: the task list's own banner (load-error, which
    // /next/tasks renders once its feed's failures are sustained -- three in
    // a row or 30 s, request-trouble.ts) stays off under it. Wait past that
    // threshold so this would fail if the suppression were removed.
    await page.waitForTimeout(32_000);
    await expect(banner).toBeVisible();
    await expect(page.getByTestId("load-error")).toHaveCount(0);
    expect(await axe()).toEqual([]);

    const bBox = await banner.boundingBox();
    const vp = page.viewportSize()!;
    expect(bBox).not.toBeNull();
    expect(bBox!.x).toBeGreaterThanOrEqual(0);
    expect(bBox!.x + bBox!.width).toBeLessThanOrEqual(vp.width + 0.5);
    expect(bBox!.y).toBeGreaterThanOrEqual(0);
    expect(bBox!.y + bBox!.height).toBeLessThanOrEqual(vp.height);

    // Once /api/** is answered again, the notice clears without a reload.
    await page.unroute("**/api/**");
    await expect(banner).toHaveCount(0, { timeout: 15_000 });
    await expect(
      page.getByRole("heading", { level: 1, name: "Tasks" }),
    ).toBeVisible();
  },
);

test(
  "a daemon refusing connections outright shows the disconnected notice and clears on recovery",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    test.setTimeout(90_000);
    await page.goto("/next/tasks");
    await expect(
      page.getByRole("heading", { level: 1, name: "Tasks" }),
    ).toBeVisible();
    const banner = page.getByTestId("disconnected-banner");
    await expect(banner).toHaveCount(0);

    await page.route("**/api/**", (route) => route.abort("connectionrefused"));

    await expect(banner).toBeVisible({ timeout: 15_000 });
    await expect(banner).toHaveAttribute("role", "status");
    await expect(banner).toContainText(/Server cannot be reached/i);
    // Refused reads fail at once, so the feed's backed-off polls (3, 6, 12 s)
    // reach three failures -- sustained -- inside this wait.
    await page.waitForTimeout(25_000);
    await expect(banner).toBeVisible();
    await expect(page.getByTestId("load-error")).toHaveCount(0);
    expect(await axe()).toEqual([]);

    await page.unroute("**/api/**");
    await expect(banner).toHaveCount(0, { timeout: 15_000 });
  },
);

for (const [width, height] of [
  [390, 844],
  [320, 568],
  [844, 390],
] as const) {
  test(
    `phone More sheet shows an unclipped build stamp at ${width}x${height} linking to Settings -> Upgrade`,
    { tag: ["@phone", "@dark"] },
    async ({ page, axe }) => {
      await page.setViewportSize({ width, height });
      await page.goto("/next/inbox");
      await expect(page.getByTestId("compact-more")).toBeVisible();
      // The bottom bar's stamp is hidden on phones before More opens.
      await expect(page.getByTestId("build-stamp")).not.toBeVisible();

      await page.getByTestId("compact-more").click();
      const sheet = page.getByTestId("more-sheet");
      await expect(sheet).toBeVisible();

      const stamp = page.getByTestId("build-stamp");
      await expect(stamp).toBeVisible();
      await expect(stamp).toContainText(/^grain \S+/);

      const sheetBox = (await sheet.boundingBox())!;
      const stampBox = (await stamp.boundingBox())!;
      expect(stampBox.x).toBeGreaterThanOrEqual(sheetBox.x);
      expect(stampBox.x + stampBox.width).toBeLessThanOrEqual(
        sheetBox.x + sheetBox.width + 0.5,
      );
      expect(stampBox.x + stampBox.width).toBeLessThanOrEqual(width + 0.5);
      expect(stampBox.y).toBeGreaterThanOrEqual(sheetBox.y);
      expect(stampBox.y + stampBox.height).toBeLessThanOrEqual(
        sheetBox.y + sheetBox.height + 0.5,
      );
      expect(stampBox.y + stampBox.height).toBeLessThanOrEqual(height + 0.5);
      expect(stampBox.height).toBeGreaterThanOrEqual(24);

      const clippedHorizontally = await stamp.evaluate(
        (el) => el.scrollWidth > el.clientWidth + 1,
      );
      expect(clippedHorizontally).toBe(false);
      expect(await hitsItself(stamp)).toBe(true);

      // Scrolling the list to its last item leaves Customize navigation above the stamp.
      const customize = sheet.getByRole("button", {
        name: "Customize navigation",
      });
      await customize.scrollIntoViewIfNeeded();
      const custBox = (await customize.boundingBox())!;
      expect(custBox.y + custBox.height).toBeLessThanOrEqual(stampBox.y + 0.5);

      expect(await axe()).toEqual([]);

      await stamp.click();
      await expect(page).toHaveURL(/\/next\/settings\/upgrade$/);
      await expect(sheet).toHaveCount(0);
    },
  );
}
