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

// docs/test-plans/2026-11-01.md §1 step 2, at desktop and phone widths in
// both themes: tasks filed at priorities 1, 5 and 9 from New task, the
// list's P-badge on the two off the default (and none on 5), the pane's
// priority chip and Declared row, and an edit to 7 from the pane carried
// back to all three. Every task filed here is closed again.

const sizes = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

for (const size of sizes) {
  for (const scheme of ["light", "dark"]) {
    test(`task priority files, shows and edits at ${size.width}px ${scheme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: scheme });
      const stamp = `${Date.now()}-${size.name}-${scheme}`;
      const ids = {};
      const declaredPriority = () =>
        page
          .locator(".declared-row")
          .filter({ has: page.locator("text=/^Task priority$/") });
      try {
        for (const priority of [1, 5, 9]) {
          await page.goto("/new");
          const dialog = page.getByRole("dialog");
          await dialog
            .getByLabel(/^Title/)
            .fill(`Priority walk ${stamp} P${priority}`);
          await dialog.getByLabel(/^Repos/).selectOption("none");
          await dialog.getByLabel("Task priority").fill(String(priority));
          const created = page.waitForResponse(
            (r) =>
              /\/api\/tasks$/.test(r.url()) && r.request().method() === "POST",
            { timeout: 15_000 },
          );
          await dialog.getByRole("button", { name: "Create task" }).click();
          const response = await created;
          expect(response.status()).toBe(201);
          // The form leaves the default out of what it sends.
          const sent = JSON.parse(response.request().postData());
          expect(sent.priority).toBe(priority === 5 ? undefined : priority);
          const task = await response.json();
          expect(task.priority).toBe(priority);
          ids[priority] = task.id;
        }

        const row = (priority) =>
          page.locator(".task-row", {
            hasText: `Priority walk ${stamp} P${priority}`,
          });
        const badgeIn = async (priority, label) => {
          const badge = row(priority).getByTestId("task-priority-badge");
          await expect(badge).toHaveText(label);
          // On the row, and on the screen at a phone's width.
          const box = await badge.boundingBox();
          const rowBox = await row(priority).boundingBox();
          expect(box.x).toBeGreaterThanOrEqual(rowBox.x - 0.5);
          expect(box.x + box.width).toBeLessThanOrEqual(
            Math.min(rowBox.x + rowBox.width, size.width) + 0.5,
          );
          expect(box.y).toBeGreaterThanOrEqual(rowBox.y - 0.5);
          expect(box.y + box.height).toBeLessThanOrEqual(
            rowBox.y + rowBox.height + 0.5,
          );
        };
        await page.goto("/");
        await expect(row(9)).toBeVisible();
        await badgeIn(9, "P9");
        await badgeIn(1, "P1");
        await expect(row(5)).toBeVisible();
        await expect(row(5).getByTestId("task-priority-badge")).toHaveCount(0);

        await page.goto(`/tasks/${ids[9]}`);
        const pane = page.getByRole("dialog");
        await expect(pane.getByTestId("detail-priority-badge")).toHaveText(
          "priority: 9",
        );
        await expect(declaredPriority()).toContainText("9 (default 5)");

        await pane.getByRole("button", { name: "Edit", exact: true }).click();
        const field = pane.getByLabel("Task priority");
        await expect(field).toHaveValue("9");
        await field.fill("7");
        const saved = page.waitForResponse(
          (r) =>
            r.url().endsWith(`/api/tasks/${ids[9]}`) &&
            r.request().method() === "PATCH",
          { timeout: 15_000 },
        );
        await pane.getByRole("button", { name: "Save", exact: true }).click();
        const patch = await saved;
        expect(patch.status()).toBe(200);
        expect(JSON.parse(patch.request().postData()).priority).toBe(7);
        await expect(pane.getByTestId("detail-priority-badge")).toHaveText(
          "priority: 7",
        );
        await expect(declaredPriority()).toContainText("7 (default 5)");

        // The pane on the default says so and Declared leaves it out.
        await page.goto(`/tasks/${ids[5]}`);
        await expect(
          page.getByRole("dialog").getByTestId("detail-priority-badge"),
        ).toHaveText("priority: 5");
        await expect(declaredPriority()).toHaveCount(0);

        await page.goto("/");
        await badgeIn(9, "P7");
      } finally {
        for (const id of Object.values(ids)) {
          await page.request.post(`/api/tasks/${id}/close`, { data: {} });
        }
      }
    });
  }
}
