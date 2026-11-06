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
// where it replaced a <Select multiple>: the Legacy capabilities pickers
// on the new-task form and the task pane, and a suite's Templates. At
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
    const check = await row.locator(".MuiCheckbox-root").boundingBox();
    const text = await row
      .locator(".MuiListItemText-primary")
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
  test(`the new-task Legacy capabilities picker is searched and chipped (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(size);
    await page.goto("/new");
    const box = page.getByRole("combobox", { name: "Legacy capabilities" });
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
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    expect(await chosenValues(box)).toEqual([...before, ...wanted]);
    const field = await inputBase(box).boundingBox();
    for (const chip of await chips(box).all()) {
      within(await chip.boundingBox(), field);
    }
    const form = page.locator("form", {
      has: page.getByRole("button", { name: "Create task" }),
    });
    expect(
      await form.evaluate((el) => el.scrollWidth - el.clientWidth),
    ).toBeLessThanOrEqual(1);

    await removeOption(box, wanted[0]);
    expect(await chosenValues(box)).toEqual([...before, ...wanted.slice(1)]);
    await page.getByRole("button", { name: "Discard draft" }).click();
  });

  test(`the task pane's Legacy capabilities picker attaches from its list and detaches from its chip (${label})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E multi select ${label} ${Date.now()}`,
        repo: "acme/widgets",
        capabilities: ["bootstrap-playbooks"],
      },
    });
    expect(created.ok()).toBeTruthy();
    const task = await created.json();
    await page.goto(`/tasks/${task.id}`);
    const box = page.getByRole("combobox", { name: "Legacy capabilities" });
    await box.scrollIntoViewIfNeeded();
    await expect.poll(() => chosenValues(box)).toEqual(["bootstrap-playbooks"]);
    within(
      await chips(box).first().boundingBox(),
      await inputBase(box).boundingBox(),
    );
    await box.click();
    await rowsLookRight(box, size.width);
    await page.keyboard.press("Escape");
    await expect(box).toHaveAttribute("aria-expanded", "false");
    await expect(box).toBeVisible();

    const dir = process.env.SHOTS_DIR;
    if (dir) {
      await box.click();
      for (const theme of ["light", "dark"]) {
        await page.evaluate(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.emulateMedia({ colorScheme: theme });
        await page.waitForTimeout(200);
        await page.screenshot({
          path: `${dir}/task-pane-capabilities-${label}-${theme}.png`,
        });
      }
      await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
      await page.emulateMedia({ colorScheme: "light" });
      await page.keyboard.press("Escape");
    }

    const detached = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/api/tasks/${task.id}/capabilities`) &&
        r.request().method() === "POST",
    );
    await removeOption(box, "bootstrap-playbooks");
    expect(JSON.parse((await detached).request().postData())).toEqual({
      id: "bootstrap-playbooks",
      attach: false,
    });
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
