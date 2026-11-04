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
import { chooseOption, optionValues } from "./searchSelect.js";

// grain/task-2912: NarrowingBar's attribute and choice selectors, and
// Customize sidebar's Item and Icon selectors, use SearchSelect so a
// repo, label, view, board or glyph can be found from the middle of its
// name or by fuzzy match, while keeping multi-select accumulation and
// Escape scoping intact.
test("searches filter attributes and choice values anywhere in the name, and narrows the task list", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page
    .getByRole("button", { name: /^Filter/ })
    .first()
    .click();
  await page.getByRole("button", { name: /^(Filter by…|Add filter)$/ }).click();
  await page.getByRole("menuitem", { name: "Origin" }).click();

  // Switch the new row's attribute to Repo via SearchSelect mid-word typing.
  const attrBox = page.getByLabel(/^Attribute for filter \d+$/).last();
  await attrBox.click();
  await attrBox.fill("epo");
  await attrBox.press("Enter");
  await expect(attrBox).toHaveValue("Repo");

  // Search the multi-select Repo values by mid-name ("dgets") and fuzzy ("acwgt").
  const valBox = page.getByLabel(/^Value for filter \d+$/).last();
  await expect(valBox).toHaveAttribute("role", "combobox");
  await valBox.click();
  await valBox.fill("dgets");
  const id = await valBox.getAttribute("aria-controls");
  const rows = page.locator(`[id="${id}"] [role="option"]`);
  await expect(rows.first()).toBeVisible();
  const values = await rows.evaluateAll((els) =>
    els.map((el) => el.dataset.value),
  );
  expect(values).toContain("acme/widgets");
  await expect(
    rows.filter({ has: page.locator(".search-select-hit") }).first(),
  ).toBeVisible();

  await valBox.fill("acwgt");
  await expect(rows.first()).toHaveAttribute("data-value", "acme/widgets");
  await valBox.press("Enter");

  // Escape closes the open value list without closing the filter editor.
  await expect(valBox).toHaveAttribute("aria-expanded", "true");
  await valBox.press("Escape");
  await expect(valBox).toHaveAttribute("aria-expanded", "false");
  await expect(attrBox).toBeVisible();

  await expect(page).toHaveURL(/f=repo:any:acme\/widgets/);
});

test("searches views and icons in Customize sidebar and saves the pin", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.getByRole("button", { name: "Customize sidebar" }).click();
  const dialog = page.getByRole("dialog", { name: "Customize sidebar" });
  await expect(dialog).toBeVisible();

  const itemBox = dialog.locator("#add-item-select");
  await itemBox.click();
  await itemBox.fill("caches");
  await itemBox.press("Enter");
  await expect(itemBox).toHaveValue("System → Caches");

  const iconBox = dialog.locator("#add-icon-select");
  expect(await optionValues(iconBox)).toContain("");
  await chooseOption(iconBox, "director");
  await expect(iconBox).toHaveValue("Director (Portico)");

  await dialog.getByRole("button", { name: "Pin item" }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toBeHidden();
});

for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  test(`lays out the narrowing bar filter row and open value list at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    await page
      .getByRole("button", { name: /^Filter/ })
      .first()
      .click();
    await page
      .getByRole("button", { name: /^(Filter by…|Add filter)$/ })
      .click();
    await page.getByRole("menuitem", { name: "Repo" }).click();

    const row = page.locator(".narrowing-row").last();
    await expect(row).toBeVisible();

    const attrRoot = row.locator(".narrowing-attr");
    const opRoot = row.locator(".MuiInputBase-root").nth(1);
    const valRoot = row.locator(".narrowing-values");
    const removeBtn = row.getByRole("button", { name: /^Remove filter/ });

    const rects = await Promise.all([
      attrRoot.boundingBox(),
      opRoot.boundingBox(),
      valRoot.boundingBox(),
      removeBtn.boundingBox(),
    ]);

    for (const r of rects) {
      expect(r).not.toBeNull();
      expect(r.width).toBeGreaterThan(20);
      expect(r.height).toBeGreaterThan(20);
      expect(r.x).toBeGreaterThanOrEqual(0);
      expect(r.x + r.width).toBeLessThanOrEqual(width + 1);
    }

    // No two controls in the row overlap, and controls on the same wrapped
    // line share a vertical centre within 4px.
    for (let i = 0; i < rects.length; i++) {
      for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i];
        const b = rects[j];
        const separated =
          a.x + a.width <= b.x + 1 ||
          b.x + b.width <= a.x + 1 ||
          a.y + a.height <= b.y + 1 ||
          b.y + b.height <= a.y + 1;
        expect(separated).toBe(true);
        const sameLine =
          Math.abs(a.y + a.height / 2 - (b.y + b.height / 2)) < 14;
        if (sameLine) {
          expect(
            Math.abs(a.y + a.height / 2 - (b.y + b.height / 2)),
          ).toBeLessThan(4);
        }
      }
    }

    // Open the multi-select value box, narrow by typing, and check its listbox.
    const valBox = valRoot.getByRole("combobox");
    await valBox.click();
    await valBox.fill("dgets");
    const id = await valBox.getAttribute("aria-controls");
    const list = page.locator(`[id="${id}"]`);
    await expect(list.locator('[role="option"]').first()).toBeVisible();

    const vb = await valRoot.boundingBox();
    const l = await list.boundingBox();
    expect(l.y >= vb.y + vb.height - 2 || l.y + l.height <= vb.y + 2).toBe(
      true,
    );
    expect(l.x).toBeGreaterThanOrEqual(0);
    expect(l.x + l.width).toBeLessThanOrEqual(width + 1);
    expect(l.y + l.height).toBeLessThanOrEqual(height + 1);

    const clipped = await list
      .locator('[role="option"]')
      .evaluateAll(
        (els) => els.filter((r) => r.scrollWidth > r.clientWidth + 1).length,
      );
    expect(clipped).toBe(0);
  });
}
