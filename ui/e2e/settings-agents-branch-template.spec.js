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

// 2026-10-30 settings walkthrough (docs/test-runs/2026-10-30-user-system-settings.md).
test.describe("Settings → Agents branch name template", () => {
  // grain/task-{id}.lock was saved, though git refuses every branch it
  // makes; and a refusal's banner stayed up under "All changes saved"
  // once a good template was saved over it.
  test("refuses a template git cannot push, and a good save clears the refusal", async ({
    page,
  }) => {
    await page.goto("/settings/agents");
    const box = page.locator('input[name="taskBranchTemplate"]');
    const status = page.getByTestId("settings-status");
    const refusal = page
      .getByRole("alert")
      .filter({ hasText: "produces invalid branch name" });
    const original = await box.inputValue();
    try {
      await box.fill("grain/task-{id}.lock");
      await box.press("Enter");
      await expect(status).toHaveText("Couldn't save Branch name template");
      await expect(refusal).toContainText("grain/task-123.lock");
      await expect(box).toHaveValue(original);

      await box.fill("walk/{deployment}/t-{id}");
      await box.press("Enter");
      await expect(status).toHaveText("All changes saved");
      await expect(refusal).toHaveCount(0);
      await page.reload();
      await expect(box).toHaveValue("walk/{deployment}/t-{id}");
    } finally {
      await box.fill(original);
      await box.press("Enter");
      await expect(status).toHaveText("All changes saved");
    }
  });

  // docs/test-plans/2026-11-01.md §4: every ref git refuses, and a name
  // past MaxBranchNameLen, at desktop and phone width. The refusal names
  // the branch the sample task 123 would get, sits inside the viewport,
  // and a near-limit template -- the longest one the length check lets
  // through -- saves over it and clears it.
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    test(`refuses each unpushable or over-length template at ${viewport.width}px`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto("/settings/agents");
      const box = page.locator('input[name="taskBranchTemplate"]');
      const status = page.getByTestId("settings-status");
      const original = await box.inputValue();
      const nearLimit = `team/${"p".repeat(181)}/{deployment}/task-{id}-{slug}`;
      const refused = [
        ["grain/task-{id}.lock", "grain/task-123.lock"],
        ["grain/.hidden/task-{id}", "grain/.hidden/task-123"],
        ["grain/task-{id}.", "grain/task-123."],
        [
          `team/${"p".repeat(182)}/{deployment}/task-{id}-{slug}`,
          "245-character branch name, over the 244-character limit",
        ],
      ];
      try {
        for (const [template, says] of refused) {
          await box.fill(template);
          await box.press("Enter");
          await expect(status).toHaveText("Couldn't save Branch name template");
          const alert = page
            .getByRole("alert")
            .filter({ hasText: "branch template produces" });
          await expect(alert).toContainText(says);
          await expect(box).toHaveValue(original);
          const rect = await alert.boundingBox();
          expect(rect.x).toBeGreaterThanOrEqual(0);
          expect(rect.x + rect.width).toBeLessThanOrEqual(viewport.width);
        }

        await box.fill(nearLimit);
        await box.press("Enter");
        await expect(status).toHaveText("All changes saved");
        await expect(
          page
            .getByRole("alert")
            .filter({ hasText: "branch template produces" }),
        ).toHaveCount(0);
        await page.reload();
        await expect(box).toHaveValue(nearLimit);
      } finally {
        await box.fill(original);
        await box.press("Enter");
        await expect(status).toHaveText("All changes saved");
      }
    });
  }
});
