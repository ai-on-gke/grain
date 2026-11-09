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
import {
  chips,
  chooseOption,
  chosenValues,
  optionValues,
  removeOption,
} from "./searchSelect.js";

// Layout of SearchSelect's multiple mode (src/components/SearchSelect.jsx)
// where it replaced a <Select multiple>: the narrowing bar's value
// picker on the task list, and a suite's Templates. At
// desktop and phone widths the chips sit inside the box, every row's
// checkbox sits in front of its label, the open list stays on screen,
// and Escape closes the list without closing the pane around it.

const within = (inner, outer, slack = 0.5) => {
  expect(inner.x).toBeGreaterThanOrEqual(outer.x - slack);
  expect(inner.y).toBeGreaterThanOrEqual(outer.y - slack);
  expect(inner.x + inner.width).toBeLessThanOrEqual(
    outer.x + outer.width + slack,
  );
  expect(inner.y + inner.height).toBeLessThanOrEqual(
    outer.y + outer.height + slack,
  );
};

const inputBase = (box) =>
  box.locator("xpath=ancestor::div[contains(@class,'MuiInputBase-root')][1]");

// rowsLookRight checks every row of the box's open list: the checkbox
// is inside the row, in front of the label, and the row on screen.
async function rowsLookRight(box, width) {
  const id = await box.getAttribute("aria-controls");
  const list = box.page().locator(`[id="${id}"]`);
  const lb = await list.boundingBox();
  expect(lb.x).toBeGreaterThanOrEqual(0);
  expect(lb.x + lb.width).toBeLessThanOrEqual(width + 0.5);
  const rows = list.locator('[role="option"]');
  const n = Math.min(await rows.count(), 4);
  expect(n).toBeGreaterThan(0);
  for (let i = 0; i < n; i++) {
    const row = rows.nth(i);
    const r = await row.boundingBox();
    const check = await row.locator(".search-select-check").boundingBox();
    // A glyphed row (a repo's) labels itself with GlyphLabel instead.
    const text = await row
      .locator(".MuiListItemText-primary, .glyph-label")
      .first()
      .boundingBox();
    within(check, r);
    within(text, r);
    expect(check.x + check.width).toBeLessThanOrEqual(text.x + 0.5);
  }
}

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the narrowing bar's value picker is searched and chipped (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(size);
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
    const box = row.locator(".narrowing-values").getByRole("combobox");
    await box.scrollIntoViewIfNeeded();
    const before = await chosenValues(box);

    await box.click();
    await rowsLookRight(box, size.width);
    const offered = await optionValues(box);
    const wanted = offered.filter((v) => !before.includes(v)).slice(0, 2);
    expect(wanted.length).toBeGreaterThan(0);
    for (const v of wanted) await chooseOption(box, v);
    // The list stays open between ticks.
    await expect(box).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Escape");
    await expect(box).toHaveAttribute("aria-expanded", "false");
    await expect(box).toBeVisible();

    expect(await chosenValues(box)).toEqual([...before, ...wanted]);
    const field = await inputBase(box).boundingBox();
    for (const chip of await chips(box).all()) {
      within(await chip.boundingBox(), field);
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);

    await removeOption(box, wanted[0]);
    expect(await chosenValues(box)).toEqual([...before, ...wanted.slice(1)]);
    await row.getByRole("button", { name: /^Remove filter/ }).click();
  });

  test(`a suite's Templates picker shows its glyph and title line (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(size);
    await page.goto("/suites");
    await page.getByRole("button", { name: /New suite/ }).click();
    const box = page
      .getByRole("dialog")
      .getByRole("combobox", { name: "Templates" });
    await box.click();
    await rowsLookRight(box, size.width);
    const id = await box.getAttribute("aria-controls");
    const row = page.locator(`[id="${id}"] [role="option"]`).first();
    const glyph = await row.locator(".item-glyph, svg").nth(1).boundingBox();
    const primary = await row.locator(".MuiListItemText-primary").boundingBox();
    expect(glyph.x + glyph.width).toBeLessThanOrEqual(primary.x + 0.5);
    const secondary = row.locator(".MuiListItemText-secondary");
    if (await secondary.count()) {
      const s = await secondary.boundingBox();
      expect(s.y).toBeGreaterThanOrEqual(primary.y + primary.height - 0.5);
    }
    const value = await row.getAttribute("data-value");
    await row.click();
    await expect(box).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Escape");
    expect(await chosenValues(box)).toEqual([value]);
    await expect(page.getByRole("dialog")).toBeVisible();
  });
}
