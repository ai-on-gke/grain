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

// Pinned items live in this browser's localStorage (grain.pinned,
// ui/src/pinnedNav.js), so every open tab of the deployment shows the
// same list: saving Customize sidebar in one tab, or resetting the list,
// redraws the pins in another without a reload.
test("pins saved in one tab show in another without a reload", async ({
  page,
}) => {
  const other = await page.context().newPage();
  await page.setViewportSize({ width: 1440, height: 900 });
  await other.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.evaluate(() => localStorage.removeItem("grain.pinned"));
  try {
    await page.reload();
    await other.goto("/");
    const asideA = page.locator("aside");
    const asideB = other.locator("aside");
    await expect(asideB.getByRole("button", { name: /^Inbox/ })).toBeVisible();
    await expect(asideB.getByRole("button", { name: /^General/ })).toHaveCount(
      0,
    );

    await asideA.getByRole("button", { name: "Customize sidebar" }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();
    await page.getByLabel("Type").click();
    await page.getByRole("option", { name: "View" }).click();
    await page.locator("#add-item-select").click();
    await page
      .getByRole("option", { name: "Settings → General", exact: true })
      .click();
    await page.getByRole("button", { name: "Pin item" }).click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toHaveCount(0);
    await expect(
      asideA.getByRole("button", { name: /^General/ }),
    ).toBeVisible();

    // The other tab, never reloaded, follows.
    await expect(
      asideB.getByRole("button", { name: /^General/ }),
    ).toBeVisible();

    // Removing the key (resetPinnedItems) puts the other tab back on the
    // defaults.
    await page.evaluate(() => localStorage.removeItem("grain.pinned"));
    await expect(asideB.getByRole("button", { name: /^General/ })).toHaveCount(
      0,
    );
    await expect(asideB.getByRole("button", { name: /^Inbox/ })).toBeVisible();
  } finally {
    await page.evaluate(() => localStorage.removeItem("grain.pinned"));
    await other.close();
  }
});
