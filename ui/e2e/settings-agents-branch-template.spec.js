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
});
