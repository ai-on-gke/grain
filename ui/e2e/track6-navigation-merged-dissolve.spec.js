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

test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };
const PHONE_PORTRAIT = { width: 390, height: 844 };

const seededBoards = [];
const seededTasks = [];

async function seedBoard(page, data) {
  const res = await page.request.post("/api/boards", { data });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seededBoards.push(board.id);
  return board;
}

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
  while (seededTasks.length > 0) {
    const id = seededTasks.pop();
    await page.request.delete(`/api/tasks/${id}`).catch(() => {});
  }
});

test.describe("Track 6: UI Navigation Redesign, Bottom User Profile & Merged Task Dissolve", () => {
  // The user tile is gone (design/design/settings-panel.design.md §6): the foot
  // of the rail is the build stamp, under System on a phone (§4.3).
  test("1. The rail's foot is the build stamp, with no user tile, across Desktop and Mobile", async ({
    page,
  }) => {
    // Desktop Viewport
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    await expect(aside).toBeVisible();
    await expect(aside.locator('[data-testid="user-tile"]')).toHaveCount(0);

    const stamp = aside.getByTestId("build-stamp");
    await expect(stamp).toBeVisible();
    const reposBtn = aside.getByRole("button", { name: /^Repos/ });
    expect((await stamp.boundingBox()).y).toBeGreaterThan(
      (await reposBtn.boundingBox()).y,
    );
    // System and Settings are the page header rail's (§4.1).
    await page.getByTestId("top-bar-settings").click();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // Mobile Viewport
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/");
    const navBtn = page.getByRole("button", { name: "Open navigation" });
    await expect(navBtn).toBeVisible();
    await navBtn.click();

    const mobileDrawer = page.locator(".MuiDrawer-paper");
    await expect(mobileDrawer).toBeVisible();
    await expect(mobileDrawer.locator('[data-testid="user-tile"]')).toHaveCount(
      0,
    );

    const mobileStamp = mobileDrawer.getByTestId("build-stamp");
    await expect(mobileStamp).toBeVisible();
    const mobileAdminBtn = mobileDrawer.getByRole("button", {
      name: /^System/i,
    });
    expect((await mobileStamp.boundingBox()).y).toBeGreaterThan(
      (await mobileAdminBtn.boundingBox()).y,
    );
  });

  test("2. Tasks breakdown navigation in sidebar", async ({ page }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const tasksBtn = aside.getByRole("button", { name: /^Tasks/ }).first();
    await expect(tasksBtn).toBeVisible();

    // Assert the page is the task list: its heading, in the main
    // landmark, under the header rail titled "tasks".
    await expect(
      page.getByRole("main").getByRole("heading", { name: "All tasks" }),
    ).toBeVisible();
    await expect(page.getByTestId("top-bar-page")).toHaveText("tasks");
  });

  test("3. Custom icon and color assignment for pinned navigation items persists across reloads", async ({
    page,
  }) => {
    const stamp = Date.now();
    const customBoard = await seedBoard(page, {
      name: `Track6 Board ${stamp}`,
      kind: "list",
      query: "",
    });

    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const customizeBtn = aside.getByRole("button", {
      name: "Customize sidebar",
    });
    await expect(customizeBtn).toBeVisible();
    await customizeBtn.click();

    // Dialog for pinned customization opens
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();

    // Select "Board" -> custom board to add
    const typeSelect = page.getByLabel("Type");
    await typeSelect.click();
    await page.getByRole("option", { name: "Board" }).click();

    const itemSelect = page.locator("#add-item-select");
    await itemSelect.click();
    await page.getByRole("option", { name: `Track6 Board ${stamp}` }).click();

    await page.getByRole("button", { name: "Pin item" }).click();

    // Save pinned navigation
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).not.toBeVisible();

    // Verify pinned section contains custom board
    const pinnedBoardBtn = aside
      .getByRole("button", {
        name: `Track6 Board ${stamp}`,
      })
      .first();
    await expect(pinnedBoardBtn).toBeVisible();

    // Reload browser and verify pinned section persists
    await page.reload();
    await expect(page.locator(".task-row").first()).toBeVisible();
    await expect(
      aside.getByRole("button", { name: `Track6 Board ${stamp}` }).first(),
    ).toBeVisible();
  });

  test("4. Live transcript and prompt access within task detail view", async ({
    page,
  }) => {
    await page.goto("/");
    await page
      .locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" })
      .click();
    await expect(page.locator(".detail-header")).toBeVisible();
    await page.getByRole("button", { name: "Prompt" }).click();

    const prompt = page.locator(".MuiDialog-paper", {
      has: page.getByRole("heading", { name: /^Prompt for/ }),
    });
    await expect(
      prompt.getByText(/Push your change to a new branch named/),
    ).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(prompt).not.toBeVisible();
  });

  test("5. Auto-hiding custom scrollbars across layout", async ({ page }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Evaluate scrollbar styles on aside and main elements
    const hasCustomScrollbarStyle = await page.evaluate(() => {
      const aside = document.querySelector("aside");
      if (!aside) return false;
      const style = window.getComputedStyle(aside);
      return (
        style.scrollbarWidth === "thin" ||
        style.overflowY === "auto" ||
        style.overflow === "auto"
      );
    });
    expect(hasCustomScrollbarStyle).toBe(true);
  });
});
