import { test, expect } from "./fixtures.js";

// Comprehensive UI workflows for Boards, Views, Column customizations, and Pinned Navigation.

test.describe("Boards and View Customization Workflows", () => {
  test("navigates boards index, opens new board drawer, configures board columns, filter toolbar, and customizes pinned navigation", async ({
    page,
  }) => {
    // 1. Navigate to /boards
    await page.goto("/boards");
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

    // 2. Click "New board" button
    await page.getByRole("button", { name: "New board" }).first().click();

    // Verify New task overlay opens prefilled for the board task
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    // Close the drawer
    await page.getByRole("button", { name: "Close dialog" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // 3. Open the demo's seeded Kanban board. "/" is the flat task list,
    // which has no Columns button: only a Kanban board that names no
    // columns of its own (TaskBoard.jsx's fixedColumns) offers one, and
    // the demo's "Board" is that board.
    await page.goto("/boards");
    await page.getByText("Board", { exact: true }).click();
    await expect(page.locator(".board-column").first()).toBeVisible();

    // 4. Test "Columns" button in Board view. Exact, because the board's
    // "Edit columns" link also matches a substring "Columns" (see
    // board.spec.js's columnsButton).
    const columnsBtn = page.getByRole("button", {
      name: "Columns",
      exact: true,
    });
    await expect(columnsBtn).toBeVisible();
    await columnsBtn.click();
    await expect(
      page.getByRole("heading", { name: "Board columns" }),
    ).toBeVisible();

    // Click "+ Add column"
    await page.getByRole("button", { name: "+ Add column" }).click();

    // Click "Reset to default"
    await page.getByRole("button", { name: "Reset to default" }).click();

    // Click "Save"
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Board columns" }),
    ).toHaveCount(0);

    // 5. Test Narrowing toolbar (Filters and Sort buttons)
    const filterBtn = page.locator(
      "button[title='Add and edit the conditions this list is narrowed by']",
    );
    await expect(filterBtn).toBeVisible();
    await filterBtn.click();

    // Verify filter panel opened
    await expect(page.getByRole("group", { name: "Filters" })).toBeVisible();

    // Toggle filter panel closed
    await filterBtn.click();
    await expect(page.getByRole("group", { name: "Filters" })).toHaveCount(0);

    // Test Sort button
    const sortBtn = page.locator(
      "button[title='Choose the order this list is shown in']",
    );
    await expect(sortBtn).toBeVisible();
    await sortBtn.click();

    // Verify sort panel opened
    await expect(page.getByRole("group", { name: "Sort" })).toBeVisible();

    // Toggle sort panel closed
    await sortBtn.click();
    await expect(page.getByRole("group", { name: "Sort" })).toHaveCount(0);

    // 6. Test "Customize" pinned items button in Sidebar
    const customizeBtn = page.getByRole("button", {
      name: "Customize sidebar",
    });
    await expect(customizeBtn).toBeVisible();
    await customizeBtn.click();

    // Verify CustomizeSidebarOverlay opens
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();

    // Click "Reset to default"
    await page.getByRole("button", { name: "Reset to default" }).click();

    // Click "Save"
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toHaveCount(0);
  });
});
