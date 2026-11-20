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

// Two shell/list bugs from the 2026-11-20 responsive-navigation
// walkthrough (docs/test-runs/2026-11-20-responsive-navigation-live-sessions.md).

// 1. On a tablet the Tasks list pane beside the open task is ~260-290px
//    wide, and the Backlog's four-segment "Order" control (about 290px)
//    shrank under overflow:hidden, clipping Oldest/Newest out of reach.
//    A narrow pane now offers the order as one "Order" menu.
for (const viewport of [
  { width: 820, height: 1180 },
  { width: 1024, height: 768 },
]) {
  test(`the Backlog order is reachable at ${viewport.width}×${viewport.height}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("tasks");
    await expect(page.getByTestId("ready-count")).toBeVisible();

    // Nothing in the toolbar's order control is clipped.
    const clipped = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>(".list-toolbar .segmented")]
        .filter((e) => e.getBoundingClientRect().width > 0)
        .filter((e) => e.scrollWidth > e.clientWidth + 1)
        .map((e) => e.getAttribute("aria-label")),
    );
    expect(clipped).toEqual([]);
    await expect(page.getByTestId("sort-newest")).toBeHidden();

    const menu = page.getByTestId("sort-select");
    await expect(menu).toBeVisible();
    await menu.click();
    await page.getByTestId("sort-select-opt-newest").click();
    await expect(menu).toContainText("Newest");

    // Widening the window brings the segments back with the same order.
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(page.getByTestId("sort-select")).toBeHidden();
    await expect(page.getByTestId("sort-newest")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });
}

// 2. ArrowDown on the rail's open create menu scrolled the rail, and a
//    rail scroll closes its floating menus, so the menu shut under the
//    keyboard. Arrow keys now move through the menu's entries.
test("arrow keys move through the rail's create menu", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("inbox");
  const newBtn = page.getByTestId("rail-create-menu");
  await newBtn.focus();
  await page.keyboard.press("ArrowDown");
  const menu = page.getByTestId("create-menu");
  await expect(menu).toBeVisible();
  await expect(page.getByTestId("create-new-task")).toBeFocused();

  await page.keyboard.press("ArrowDown");
  await expect(page.getByTestId("create-start-workstream")).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("ArrowUp");
  // Wraps to the last entry.
  await expect(menu.getByRole("link", { name: /New board/ })).toBeFocused();
  await page.keyboard.press("Home");
  await expect(page.getByTestId("create-new-task")).toBeFocused();

  // Walk down to New session and open it from the keyboard.
  for (let i = 0; i < 8; i++) {
    const name = await page.evaluate(
      () => document.activeElement?.textContent ?? "",
    );
    if (name.includes("New session")) break;
    await page.keyboard.press("ArrowDown");
  }
  await expect(menu).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("new-session-dialog")).toBeVisible();
  await expect(page.getByTestId("new-session-name")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("new-session-dialog")).toHaveCount(0);

  // Escape from inside the menu still hands focus back to New.
  await newBtn.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByTestId("create-new-task")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(newBtn).toBeFocused();
});
