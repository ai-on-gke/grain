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

// docs/test-runs/2026-11-03-ui-fixes.md §2.1: sidebar-pinned-cross-tab.spec.js
// pins and resets across two tabs; this walks the rest of what Customize
// sidebar saves -- the order of the pins and a pin's colour -- through
// the dialog's own controls, in two pages of one context that are never
// reloaded, then opens a third tab and checks it agrees before Reset to
// default puts all three back.

const SIZE = { width: 1440, height: 900 };

async function openCustomize(page) {
  await page
    .locator("aside")
    .getByRole("button", { name: "Customize sidebar" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Customize sidebar" }),
  ).toBeVisible();
}

async function save(page) {
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Customize sidebar" }),
  ).toHaveCount(0);
}

async function pinView(page, option) {
  await page.getByLabel("Type").click();
  await page.getByRole("option", { name: "View" }).click();
  await page.locator("#add-item-select").click();
  await page.getByRole("option", { name: option, exact: true }).click();
  await page.getByRole("button", { name: "Pin item" }).click();
}

function pin(page, name) {
  return page
    .locator("aside")
    .getByRole("button", { name: new RegExp(`^${name}`) });
}

// order is the top edge of each named pin, so a test can say which is
// drawn above which.
async function order(page, names) {
  const ys = [];
  for (const name of names) {
    ys.push((await pin(page, name).boundingBox()).y);
  }
  return ys;
}

async function iconColor(page, name) {
  return pin(page, name)
    .locator("svg")
    .first()
    .evaluate((el) => getComputedStyle(el).color);
}

test("pin order and colour follow into other open tabs and a later one", async ({
  page,
}) => {
  const context = page.context();
  const other = await context.newPage();
  let third;
  await page.setViewportSize(SIZE);
  await other.setViewportSize(SIZE);
  await page.goto("/");
  await page.evaluate(() => localStorage.removeItem("grain.pinned"));
  try {
    await page.reload();
    await other.goto("/");
    await expect(pin(other, "General")).toHaveCount(0);

    // Pin two views; the other tab shows them in the saved order.
    await openCustomize(page);
    await pinView(page, "Settings → General");
    await pinView(page, "Settings → Git");
    await save(page);
    await expect(pin(other, "General")).toBeVisible();
    await expect(pin(other, "Git")).toBeVisible();
    let [general, git] = await order(other, ["General", "Git"]);
    expect(general).toBeLessThan(git);

    // Move Git above General in the first tab; the second follows.
    await openCustomize(page);
    await page
      .getByRole("listitem")
      .filter({ hasText: /^Git/ })
      .getByRole("button", { name: "Move up" })
      .click();
    await save(page);
    await expect
      .poll(async () => {
        const [g, t] = await order(other, ["General", "Git"]);
        return t < g;
      })
      .toBe(true);

    // Recolour General red; the second tab's icon turns red too.
    const before = await iconColor(other, "General");
    await openCustomize(page);
    await page.getByRole("button", { name: "Customize General" }).click();
    await page.getByRole("button", { name: "Color Red" }).click();
    await save(page);
    await expect
      .poll(() => iconColor(page, "General"))
      .toBe("rgb(239, 68, 68)");
    await expect
      .poll(() => iconColor(other, "General"))
      .toBe("rgb(239, 68, 68)");
    expect(before).not.toBe("rgb(239, 68, 68)");

    // A tab opened now reads the same list from storage.
    third = await context.newPage();
    await third.setViewportSize(SIZE);
    await third.goto("/");
    await expect(pin(third, "Git")).toBeVisible();
    [general, git] = await order(third, ["General", "Git"]);
    expect(git).toBeLessThan(general);
    expect(await iconColor(third, "General")).toBe("rgb(239, 68, 68)");

    // Reset to default through the dialog puts every tab back.
    await openCustomize(page);
    await page.getByRole("button", { name: "Reset to default" }).click();
    await save(page);
    for (const tab of [page, other, third]) {
      await expect(pin(tab, "General")).toHaveCount(0);
      await expect(pin(tab, "Git")).toHaveCount(0);
      await expect(pin(tab, "Inbox").first()).toBeVisible();
    }
  } finally {
    await page.evaluate(() => {
      localStorage.removeItem("grain.pinned");
      localStorage.removeItem("grain.sidebar.hidden");
    });
    await other.close();
    if (third) await third.close();
  }
});
