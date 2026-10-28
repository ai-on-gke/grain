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

// Upgrade moved from a System tab (/system/upgrade) into Settings, as
// its own section (/settings/upgrade). These follow the ways an
// operator gets there: the old link, the section nav, the filter, the
// build stamp, and a phone's section list. Nothing is upgraded: the
// demo wires no Upgrader, and nothing here clicks Upgrade.

test.describe("Settings → Upgrade", () => {
  test("the old /system/upgrade link, anchor and all, lands on it", async ({
    page,
  }) => {
    await page.goto("/system/upgrade#upgrade");
    await expect(page).toHaveURL(/\/settings\/upgrade#upgrade$/);
    await expect(page.getByTestId("settings-section")).toHaveAttribute(
      "data-section",
      "upgrade",
    );
    await expect(page.getByTestId("upgrade-section")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Upgrade", level: 1 }),
    ).toBeVisible();
    // An action, not a setting: there is no save state to report.
    await expect(page.getByTestId("settings-status")).toHaveCount(0);
  });

  test("sits in the Deployment group, beside the section nav, and not in System", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/settings/general");
    await page.getByTestId("settings-nav-upgrade").click();
    await expect(page).toHaveURL(/\/settings\/upgrade$/);
    const nav = await page.getByTestId("settings-nav").boundingBox();
    const section = await page.getByTestId("upgrade-section").boundingBox();
    // The section's content is to the right of the nav, not under it.
    expect(section.x).toBeGreaterThanOrEqual(nav.x + nav.width);

    await page.getByTestId("top-bar-system").click();
    await expect(page.getByRole("tab", { name: "Health" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Upgrade" })).toHaveCount(0);
  });

  test("the settings filter finds it for 'upgrade'", async ({ page }) => {
    await page.goto("/settings/general");
    const filter = page.getByTestId("settings-filter").locator("input");
    await filter.fill("upgrade");
    // Results are listed by section, so General's upgrade-check fields
    // come first on screen; the section itself is the best match, which
    // is the one Enter takes.
    await expect(
      page
        .getByTestId("settings-result")
        .filter({ hasText: "Upgrade grain, Upgrade" }),
    ).toBeVisible();
    await filter.press("Enter");
    await expect(page).toHaveURL(/\/settings\/upgrade#upgrade$/);
    await expect(page.getByTestId("upgrade-section")).toBeVisible();
  });

  test("the build stamp opens it", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    await page.getByTestId("build-stamp").click();
    await expect(page).toHaveURL(/\/settings\/upgrade$/);
    await expect(page.getByTestId("upgrade-section")).toBeVisible();
  });

  test.describe("on a phone", () => {
    test.use({ viewport: { width: 390, height: 844 } });

    test("the section list reaches it, and back returns to the list", async ({
      page,
    }) => {
      await page.goto("/");
      await page.getByTestId("phone-settings").click();
      await page.getByTestId("settings-nav-upgrade").click();
      await expect(page).toHaveURL(/\/settings\/upgrade$/);
      await expect(page.getByTestId("upgrade-section")).toBeVisible();
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
      await page.getByTestId("settings-back").click();
      await expect(page.getByTestId("settings-nav")).toBeVisible();
    });
  });
});
