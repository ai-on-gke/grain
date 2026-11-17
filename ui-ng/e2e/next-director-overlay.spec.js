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

test("M16 Director overlay: top-bar toggle, d/Alt+D/Escape, coplanar selection context, and Shape with Director in New/Edit Task", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });

  await page.goto("/next/tasks");
  await page.waitForSelector(".main-stage > *:not(router-outlet)");

  // Top-bar trigger opens coplanar Director drawer without blocking .main-stage.
  await page.getByTestId("director-trigger").click();
  const drawer = page.getByTestId("director-drawer");
  await expect(drawer).toBeVisible();
  await expect(page.locator("#main")).not.toHaveAttribute("inert", "");

  // Selecting rows in Tasks updates the drawer context toggle live.
  const rows = page.locator('[data-testid^="open-"]');
  await expect(rows.first()).toBeVisible();
  const rowCount = await rows.count();
  if (rowCount >= 2) {
    await rows.nth(1).click();
    await expect(page.getByTestId("director-context-toggle")).toContainText(
      "Task #",
    );
  }

  // Send a message with context attached and assert POST /api/tasks/{id}/comments carries provenance quote.
  const commentReq = page.waitForRequest(
    (req) =>
      /\/api\/tasks\/[^/]+\/comments$/.test(req.url()) &&
      req.method() === "POST",
  );
  const box = drawer.getByTestId("task-composer");
  await box.fill("Check why this task is queued.");
  await box.press("Control+Enter");
  const req = await commentReq;
  const payload = JSON.parse(req.postData() || "{}");
  expect(payload.body).toContain("> [Context:");
  expect(payload.body).toContain("Check why this task is queued.");

  // Escape closes, d opens, Alt+D toggles even while typing in the search box.
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);

  await page.getByTestId("view-filter").blur();
  await page.keyboard.press("d");
  await expect(drawer).toBeVisible();

  await page.getByTestId("view-filter").focus();
  await page.keyboard.press("Alt+d");
  await expect(drawer).toHaveCount(0);

  // Coplanar Shape with Director inside New Task sheet; wider sheet + single-row footer buttons.
  await page.getByTestId("rail-new-task").click();
  const sheet = page.getByTestId("new-task-sheet");
  await expect(sheet).toBeVisible();
  const sheetLayout = await sheet.evaluate((el) => {
    const btns = [...el.querySelectorAll(".sheet-actions > button")].filter(
      (b) => b.getBoundingClientRect().height > 0,
    );
    const tops = btns.map((b) => Math.round(b.getBoundingClientRect().top));
    return {
      width: Math.round(el.getBoundingClientRect().width),
      rowSpread: tops.length ? Math.max(...tops) - Math.min(...tops) : 0,
      count: btns.length,
    };
  });
  expect(sheetLayout.width).toBeGreaterThanOrEqual(700);
  expect(sheetLayout.count).toBeGreaterThanOrEqual(3);
  expect(sheetLayout.rowSpread).toBeLessThanOrEqual(4);
  await page.getByTestId("shape-director-btn").click();
  await expect(sheet.getByTestId("director-shaper-pane")).toBeVisible();
  const shaperRowSpread = await sheet.evaluate((el) => {
    const tops = [...el.querySelectorAll(".sheet-actions > button")]
      .filter((b) => b.getBoundingClientRect().height > 0)
      .map((b) => Math.round(b.getBoundingClientRect().top));
    return tops.length ? Math.max(...tops) - Math.min(...tops) : 0;
  });
  expect(shaperRowSpread).toBeLessThanOrEqual(4);
  await page.getByTestId("new-task-close").click();
  await expect(sheet).toHaveCount(0);

  // Coplanar Shape with Director inside Task Edit panel.
  await page.getByTestId("act-edit").click();
  const editPanel = page.getByTestId("task-edit-panel");
  await expect(editPanel).toBeVisible();
  await page.getByTestId("edit-shape-director").click();
  await expect(editPanel.getByTestId("director-shaper-pane")).toBeVisible();
});
