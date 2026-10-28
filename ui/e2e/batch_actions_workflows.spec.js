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

// Comprehensive UI workflows for multi-task selection and BatchActionsBar:
// selecting multiple tasks with checkboxes, deferring, undeferring,
// bulk editing fields (Priority, Auto-merge), and clearing selection.

test.describe("Batch Actions and Bulk Operations Workflows", () => {
  test("selects tasks, performs batch defer and undefer, bulk edits fields, and clears selection", async ({
    page,
  }) => {
    const stamp = Date.now();
    const taskTitles = [
      `E2E Batch Task A ${stamp}`,
      `E2E Batch Task B ${stamp}`,
      `E2E Batch Task C ${stamp}`,
    ];

    // 1. Seed 3 proposed tasks
    const seeded = [];
    for (const title of taskTitles) {
      const res = await page.request.post("/api/tasks", {
        data: {
          title,
          customFields: { priority: "low" },
        },
      });
      expect(res.ok()).toBe(true);
      seeded.push(await res.json());
    }

    // 2. Navigate to /
    await page.goto("/");
    for (const title of taskTitles) {
      await expect(page.locator(".task-row", { hasText: title })).toBeVisible();
    }

    // 3. Select 2 tasks via checkboxes
    const rowA = page.locator(".task-row", { hasText: taskTitles[0] });
    const rowB = page.locator(".task-row", { hasText: taskTitles[1] });

    await rowA.getByRole("checkbox").check();
    await rowB.getByRole("checkbox").check();

    // 4. Verify BatchActionsBar appears showing "2 selected"
    await expect(page.getByText("2 selected")).toBeVisible();

    // 5. Click Defer button in BatchActionsBar
    await page.getByRole("button", { name: "Defer", exact: true }).click();

    // 6. Verify selection is cleared after action
    await expect(page.getByText("2 selected")).toHaveCount(0);

    // 7. Deferred tasks are hidden from default view, but appear when filtering by deferred
    await expect(
      page.locator(".task-row", { hasText: taskTitles[0] }),
    ).toHaveCount(0);
    await expect(
      page.locator(".task-row", { hasText: taskTitles[1] }),
    ).toHaveCount(0);

    await page.goto("/?f=state:any:deferred");
    const defRowA = page.locator(".task-row", { hasText: taskTitles[0] });
    const defRowB = page.locator(".task-row", { hasText: taskTitles[1] });
    await expect(defRowA).toBeVisible();
    await expect(defRowB).toBeVisible();

    // 8. Select them in deferred list and click Undefer
    await defRowA.getByRole("checkbox").check();
    await defRowB.getByRole("checkbox").check();
    await expect(page.getByText("2 selected")).toBeVisible();

    await page.getByRole("button", { name: "Undefer", exact: true }).click();
    await expect(page.getByText("2 selected")).toHaveCount(0);

    // 9. Back on main list, select 2 tasks and open Bulk edit…
    await page.goto("/");
    const mainRowA = page.locator(".task-row", { hasText: taskTitles[0] });
    const mainRowB = page.locator(".task-row", { hasText: taskTitles[1] });
    await expect(mainRowA).toBeVisible();
    await expect(mainRowB).toBeVisible();

    await mainRowA.getByRole("checkbox").check();
    await mainRowB.getByRole("checkbox").check();
    await expect(page.getByText("2 selected")).toBeVisible();

    await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();

    // 10. In BulkEditOverlay, check Priority and set to "high"
    await dialog.getByRole("checkbox", { name: /^Priority/ }).check();
    await dialog.getByRole("combobox", { name: "Priority" }).click();
    await page.getByRole("option", { name: "high" }).click();

    // 11. Check Auto-merge and set to "On"
    await dialog.getByRole("checkbox", { name: /^Auto-merge/ }).check();
    await dialog.getByRole("combobox", { name: "Auto-merge" }).click();
    await page.getByRole("option", { name: "On" }).click();

    // 12. Click Apply to 2 tasks
    await dialog.getByRole("button", { name: "Apply to 2 tasks" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText("2 selected")).toHaveCount(0);

    // 13. Verify updated fields on the rows
    await expect(mainRowA.locator(".chip-field")).toHaveText("Priorityhigh");
    await expect(mainRowB.locator(".chip-field")).toHaveText("Priorityhigh");

    // 14. Test Shift-click range selection, range deselection, and Clear selection button
    const mainRowC = page.locator(".task-row", { hasText: taskTitles[2] });
    await mainRowA.getByRole("checkbox").click();
    await mainRowC.getByRole("checkbox").click({ modifiers: ["Shift"] });
    await expect(page.getByText("3 selected")).toBeVisible();
    await expect(mainRowA.getByRole("checkbox")).toBeChecked();
    await expect(mainRowB.getByRole("checkbox")).toBeChecked();
    await expect(mainRowC.getByRole("checkbox")).toBeChecked();

    // Shift-click range deselection from C back to B
    await mainRowC.getByRole("checkbox").click();
    await expect(page.getByText("2 selected")).toBeVisible();
    await mainRowB.getByRole("checkbox").click({ modifiers: ["Shift"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await expect(mainRowA.getByRole("checkbox")).toBeChecked();
    await expect(mainRowB.getByRole("checkbox")).not.toBeChecked();
    await expect(mainRowC.getByRole("checkbox")).not.toBeChecked();

    await page
      .getByRole("button", { name: "Clear selection", exact: true })
      .click();
    await expect(page.getByText("1 selected")).toHaveCount(0);
    await expect(mainRowA.getByRole("checkbox")).not.toBeChecked();
  });
});
