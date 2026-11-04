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
import { optionValues } from "./searchSelect.js";

// grain/task-2879: the New task form's repo box is searched anywhere in
// owner/name, not only from its first letters the way a native <select>'s
// type-ahead was -- "dgets", from the middle of the name, finds
// acme/widgets, and "acwgt", which is in no name as written, still finds
// it by its letters in order. Enter takes the highlighted row without submitting
// the form, and the task is filed against the repo picked.
test("searches the New task repo box anywhere in the name, and files against the pick", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  const box = page.getByLabel(/^Repos/);
  await expect(box).toHaveAttribute("role", "combobox");

  await box.click();
  await box.fill("dgets");
  const id = await box.getAttribute("aria-controls");
  const rows = page.locator(`[id="${id}"] [role="option"]`);
  await expect(rows.first()).toBeVisible();
  const values = await rows.evaluateAll((els) =>
    els.map((el) => el.dataset.value),
  );
  expect(values).toContain("acme/widgets");
  // Every repo row matched; Other… is the one row kept on offer anyway.
  for (const v of values.filter((v) => v.includes("/"))) {
    expect(v).toContain("dgets");
  }
  expect(values[values.length - 1]).toBe("custom");
  // The hit is marked in the row.
  await expect(
    rows.filter({ has: page.locator(".search-select-hit") }).first(),
  ).toBeVisible();

  // The list hangs under the box, at least as wide as it.
  const boxRect = await box.boundingBox();
  const listRect = await page.locator(`[id="${id}"]`).boundingBox();
  expect(listRect.y).toBeGreaterThanOrEqual(boxRect.y + boxRect.height - 1);
  expect(listRect.width).toBeGreaterThanOrEqual(boxRect.width - 1);

  // Fuzzy: no name holds "acwgt", but acme/widgets has its letters in
  // order.
  await box.fill("acwgt");
  await expect(rows.first()).toHaveAttribute("data-value", "acme/widgets");
  await box.press("Enter");
  await expect(box).toHaveValue("acme/widgets");
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  expect(await optionValues(box)).toContain("none");

  const title = `Repo search ${Date.now()}`;
  await page.getByLabel("Title").fill(title);
  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  expect(response.request().postDataJSON().repo).toBe("acme/widgets");
});

// The box is a plain input drawn to look like the <select> it replaced:
// the repos ring in front of it, a caret inside its right edge that the
// typed text never runs under, and the list hung off it inside the
// viewport -- at a phone's width as much as a desktop's.
for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  test(`lays the repo box and its list out at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page
      .getByRole("button", { name: /New task/i })
      .first()
      .click();
    const box = page.getByLabel(/^Repos/);
    await expect(box).toBeVisible();

    const field = page.locator(".repo-field-control").first();
    const glyph = field.locator(".repo-field-icon");
    const caret = field.locator(".repo-field-caret");
    const b = await box.boundingBox();
    const g = await glyph.boundingBox();
    const c = await caret.boundingBox();
    // The ring sits before the box, centred on it.
    expect(g.x + g.width).toBeLessThanOrEqual(b.x);
    expect(Math.abs(g.y + g.height / 2 - (b.y + b.height / 2))).toBeLessThan(3);
    // The caret is inside the box's right edge, centred on it, and the
    // box's own padding keeps text out from under it.
    expect(c.x).toBeGreaterThan(b.x + b.width / 2);
    expect(c.x + c.width).toBeLessThanOrEqual(b.x + b.width);
    expect(Math.abs(c.y + c.height / 2 - (b.y + b.height / 2))).toBeLessThan(3);
    const padRight = await box.evaluate((el) =>
      parseFloat(getComputedStyle(el).paddingRight),
    );
    expect(b.x + b.width - padRight).toBeLessThanOrEqual(c.x);
    // The box fills the row, and the row fits the viewport.
    expect(b.x + b.width).toBeLessThanOrEqual(width);

    await box.click();
    await box.fill("dgets");
    const id = await box.getAttribute("aria-controls");
    const list = page.locator(`[id="${id}"]`);
    await expect(list.locator('[role="option"]').first()).toBeVisible();
    // Clicking scrolled the box into view; measure it where it is now.
    // The list hangs under it, or -- when the drawer leaves no room
    // below -- flips above it, but never covers it.
    const bb = await box.boundingBox();
    const l = await list.boundingBox();
    expect(l.y >= bb.y + bb.height - 1 || l.y + l.height <= bb.y + 1).toBe(
      true,
    );
    expect(l.x).toBeGreaterThanOrEqual(0);
    expect(l.x + l.width).toBeLessThanOrEqual(width);
    expect(l.y + l.height).toBeLessThanOrEqual(height);
    // No row's text is cut off sideways.
    const clipped = await list
      .locator('[role="option"]')
      .evaluateAll(
        (rows) => rows.filter((r) => r.scrollWidth > r.clientWidth + 1).length,
      );
    expect(clipped).toBe(0);
  });
}
