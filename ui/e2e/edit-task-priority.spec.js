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

// A task's Edit form is not a <form>, so its Task priority box's min=1
// and step=1 were never enforced on Save: a 0 saved as the default 5 and
// a 2.5 as 2, without a word (docs/test-runs/2026-11-01-task-intake-
// inbox-lifecycle.md). It now refuses them where they are typed. Nothing
// here is saved, so the shared demo is left as it was.

test("Edit refuses a Task priority that is not a whole number from 1", async ({
  page,
}) => {
  await page.goto("/tasks/2");
  const pane = page.getByRole("dialog");
  await pane.getByRole("button", { name: "Edit", exact: true }).click();
  const priority = pane.getByLabel("Task priority");
  const save = pane.getByRole("button", { name: "Save", exact: true });
  await expect(priority).toHaveValue("5");
  await expect(save).toBeEnabled();

  for (const typed of ["0", "2.5"]) {
    await priority.fill(typed);
    await expect(priority).toHaveAttribute("aria-invalid", "true");
    await expect(save).toBeDisabled();
    const hint = pane.getByText(/a whole number, 1 or more/);
    await expect(hint).toBeVisible();
    // The reason sits under the box it is about.
    const field = await priority.boundingBox();
    const box = await hint.boundingBox();
    expect(box.y).toBeGreaterThanOrEqual(field.y + field.height - 0.5);
    expect(Math.abs(box.x - field.x)).toBeLessThan(32);
  }

  await priority.fill("8");
  await expect(priority).toHaveAttribute("aria-invalid", "false");
  await expect(save).toBeEnabled();
  await pane.getByRole("button", { name: "Cancel", exact: true }).click();
});
