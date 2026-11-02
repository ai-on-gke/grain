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

test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };
const TABLET = { width: 820, height: 1180 };

const seededBoards = [];

async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
});

test.describe("Sidebar Navigation Restructuring & Foldable Controls", () => {
  test("1. Visual DOM Layout Hierarchy: Top Pinned -> Global Navigation -> User tile", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    await expect(aside).toBeVisible();

    // 1. Brand mark at top
    const wordmark = aside.getByRole("heading", { name: "grain" });
    await expect(wordmark).toBeVisible();

    // 2. Primary "+ New task" button
    const newTaskBtn = aside.getByRole("button", { name: "+ New task" });
    await expect(newTaskBtn).toBeVisible();

    // The primary action and its dropdown share one full-width split button.
    // Keep the arrow as a compact affordance instead of letting either child
    // claim the whole row and wrap the task label.
    const createOptionsBtn = aside.getByRole("button", {
      name: "Create options",
    });
    const [newTaskBox, createOptionsBox] = await Promise.all([
      newTaskBtn.boundingBox(),
      createOptionsBtn.boundingBox(),
    ]);
    expect(newTaskBox).not.toBeNull();
    expect(createOptionsBox).not.toBeNull();
    expect(createOptionsBox.width).toBeLessThanOrEqual(40);
    expect(newTaskBox.width).toBeGreaterThan(createOptionsBox.width * 3);
    const newTaskLabelLines = await newTaskBtn.evaluate((button) => {
      const range = document.createRange();
      range.selectNodeContents(button);
      return new Set(
        [...range.getClientRects()].map((rect) => Math.round(rect.top)),
      ).size;
    });
    expect(newTaskLabelLines).toBe(1);

    // 3. Pinned items section
    const pinnedHeading = aside.getByText("Pinned");
    await expect(pinnedHeading).toBeVisible();
    const customizePinnedBtn = aside.getByRole("button", {
      name: "Customize sidebar",
    });
    await expect(customizePinnedBtn).toBeVisible();

    // 4. The destinations, flat: the bordered team-scoped box went with
    // the teams (design/design/settings-panel.design.md §4.2).
    await expect(aside.getByTestId("team-section")).toHaveCount(0);
    await expect(page.getByTestId("team-selector")).toHaveCount(0);

    const order = [];
    for (const name of [
      /^Tasks/,
      /^Boards/,
      /^Sessions/,
      /^Processes/,
      /^CI/,
      /^Repos/,
    ]) {
      const btn = aside.getByRole("button", { name }).first();
      await expect(btn).toBeVisible();
      order.push((await btn.boundingBox()).y);
    }
    expect([...order].sort((x, y) => x - y)).toEqual(order);

    // Memory, Sandboxes and Caches are not rail entries: Memory is a
    // Settings section and the other two are System tabs.
    for (const item of ["Memory", "Sandboxes", "Caches"]) {
      expect(
        await aside
          .getByRole("button", { name: new RegExp(`^${item}`) })
          .count(),
      ).toBe(0);
    }

    // 5. No footer list: System and Settings are the page header rail's buttons,
    // and Metrics is a System tab.
    for (const item of ["System", "Metrics", "Settings"]) {
      expect(
        await aside
          .getByRole("button", { name: new RegExp(`^${item}`) })
          .count(),
      ).toBe(0);
    }
    await expect(page.getByTestId("top-bar-system")).toBeVisible();
    await expect(page.getByTestId("top-bar-settings")).toBeVisible();

    // 6. No user tile: the build stamp alone is the foot, a link to
    // Settings -> Upgrade (design/design/settings-panel.design.md §4.3).
    await expect(aside.getByTestId("user-tile")).toHaveCount(0);
    const stamp = aside.getByTestId("build-stamp");
    await expect(stamp).toBeVisible();
    await expect(stamp).toHaveAttribute("href", "/settings/upgrade");
    const reposBox = await aside
      .getByRole("button", { name: /^Repos/ })
      .boundingBox();
    expect((await stamp.boundingBox()).y).toBeGreaterThan(reposBox.y);
  });

  test("2. Pinned Items: customization modal, item rendering, navigation, and localStorage persistence", async ({
    page,
  }) => {
    // Seed a board to test board pinning
    const stamp = Date.now();
    const customBoard = await seedOne(page, "/api/boards", {
      name: `Pinned Board ${stamp}`,
      kind: "list",
      query: "",
    });
    seededBoards.push(customBoard.id);

    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");

    // Open Customize Pinned modal
    const customizeBtn = aside.getByRole("button", {
      name: "Customize sidebar",
    });
    await customizeBtn.click();

    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();

    // Select "View" -> "CI" to add
    const typeSelect = page.getByLabel("Type");
    await typeSelect.click();
    await page.getByRole("option", { name: "View" }).click();

    const itemSelect = page.locator("#add-item-select");
    await itemSelect.click();
    await page.getByRole("option", { name: "CI", exact: true }).click();

    await page.getByRole("button", { name: "Pin item" }).click();

    // A System tab can be pinned on its own
    // (design/design/settings-panel.design.md §5.6).
    await itemSelect.click();
    await page.getByRole("option", { name: "System → Caches" }).click();
    await page.getByRole("button", { name: "Pin item" }).click();

    // Re-selecting an already-pinned System tab disables "Pin item",
    // while another System tab ("System → Health") remains pinnable.
    await itemSelect.click();
    await page.getByRole("option", { name: "System → Caches" }).click();
    await expect(page.getByRole("button", { name: "Pin item" })).toBeDisabled();
    await itemSelect.click();
    await page.getByRole("option", { name: "System → Health" }).click();
    await expect(page.getByRole("button", { name: "Pin item" })).toBeEnabled();
    await page.getByRole("button", { name: "Pin item" }).click();

    // Select "Board" -> custom board to add
    await typeSelect.click();
    await page.getByRole("option", { name: "Board" }).click();

    await itemSelect.click();
    await page.getByRole("option", { name: `Pinned Board ${stamp}` }).click();

    await page.getByRole("button", { name: "Pin item" }).click();

    // Save pinned navigation
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).not.toBeVisible();

    // Verify pinned section contains CI and custom board
    const pinnedCIBtn = aside.getByRole("button", { name: /^CI/ }).first();
    await expect(pinnedCIBtn).toBeVisible();

    const pinnedBoardBtn = aside
      .getByRole("button", {
        name: `Pinned Board ${stamp}`,
      })
      .first();
    await expect(pinnedBoardBtn).toBeVisible();

    // Click pinned CI button and verify navigation to CI view
    await pinnedCIBtn.click();
    await expect(page).toHaveURL(/\/ci$/);
    await expect(page.getByRole("heading", { name: "CI" })).toBeVisible();

    // Click the pinned System tab and verify it opens that tab
    await aside
      .getByRole("button", { name: /^Caches/ })
      .first()
      .click();
    await expect(page).toHaveURL(/\/system\/caches$/);

    // Click the pinned Health tab and verify it opens /system and stays selected
    const pinnedHealthBtn = aside
      .getByRole("button", { name: /^Health/ })
      .first();
    await pinnedHealthBtn.click();
    await expect(page).toHaveURL(/\/system$/);
    await expect(pinnedHealthBtn).toHaveClass(/Mui-selected/);

    // Click pinned board button and verify navigation to Board view
    await pinnedBoardBtn.click();
    await expect(page).toHaveURL(new RegExp(`/boards/${customBoard.id}$`));

    // Reload page and verify pinned items persist
    await page.reload();
    await expect(
      aside.getByRole("button", { name: `Pinned Board ${stamp}` }).first(),
    ).toBeVisible();
  });

  test("3. Left Panel Cleanup: Tasks breakdown and navigation to Boards and Sessions", async ({
    page,
  }) => {
    // Seed a board
    const stamp = Date.now();
    const b = await seedOne(page, "/api/boards", {
      name: `Clean Board ${stamp}`,
      kind: "list",
      query: "",
    });
    seededBoards.push(b.id);

    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const tasksBtn = aside.getByRole("button", { name: /^Tasks/ });
    const boardsBtn = aside.getByRole("button", { name: /^Boards/ });
    const sessionsBtn = aside.getByRole("button", { name: /^Sessions/ });

    // Tasks breakdown is directly visible without carets
    await expect(tasksBtn).toBeVisible();
    await expect(aside.getByTestId("toggle-tasks-button")).not.toBeVisible();
    await expect(
      aside.getByRole("button", { name: /^Waiting \d+/ }),
    ).toBeVisible();

    // Boards button navigates to /boards
    await boardsBtn.click();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

    // Sessions button navigates to /sessions
    await sessionsBtn.click();
    await expect(page).toHaveURL(/\/sessions$/);
    await expect(page.getByRole("heading", { name: "Sessions" })).toBeVisible();
  });

  test("4. Keyboard Accessibility: Tab focus and navigation", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const boardsBtn = aside.getByRole("button", { name: /^Boards/ });

    // Focus boards button and activate via keyboard Enter
    await boardsBtn.focus();
    await expect(boardsBtn).toBeFocused();

    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
  });
});

test.describe("Sidebar on Tablet & Coarse Pointer", () => {
  test.use({ viewport: TABLET, hasTouch: true, isMobile: true });

  test("5. Coarse Pointer / Touch Viewport Target Sizing & Tap Disambiguation", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const boardsBtn = aside.getByRole("button", { name: /^Boards/ });

    // Verify touch target sizing for section headings
    const headings = aside.locator(".sidebar-section-heading");
    const headingCount = await headings.count();
    expect(headingCount).toBeGreaterThanOrEqual(1);

    for (let i = 0; i < headingCount; i++) {
      const heading = headings.nth(i);
      const box = await heading.boundingBox();
      expect(
        Math.round(box.height),
        `Heading "${await heading.textContent()}" height is ${box.height}px`,
      ).toBeGreaterThanOrEqual(40);
    }

    // Tapping the Boards button should navigate to /boards
    await boardsBtn.click();
    await expect(page).toHaveURL(/\/boards$/);
  });
});
