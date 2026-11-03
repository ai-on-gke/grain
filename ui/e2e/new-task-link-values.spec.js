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

// A /new?field.<name>= link whose value the field does not offer, or
// whose name no field declares, files the task without it -- which the
// form now says, beside the fields, instead of leaving the select blank
// without a word (docs/test-runs/2026-10-30-task-intake-inbox-
// lifecycle.md; O3 of the 10-29 run). `grain demo` declares Priority as
// an enum of low, medium and high.

test("New task names the link values it left out", async ({ page }) => {
  await page.goto("/new?field.priority=bogus&field.nosuch=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel(/^Title/)).toBeVisible();
  const notice = dialog.getByTestId("unfiled-link-values");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText(
    "Priority: “bogus” is not one of its values",
  );
  await expect(notice).toContainText(
    "“nosuch” is not a field on this deployment",
  );

  // It sits inside the form, above the fields it is about, at a desktop
  // width and a phone's.
  for (const size of [
    { width: 1400, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await notice.scrollIntoViewIfNeeded();
    const box = await notice.boundingBox();
    const form = await dialog.boundingBox();
    const priority = await dialog.getByLabel(/^Priority/).boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(form.x);
    expect(box.x + box.width).toBeLessThanOrEqual(form.x + form.width + 0.5);
    expect(box.y + box.height).toBeLessThanOrEqual(priority.y);
  }

  // A link whose every value is kept raises nothing, and the value is on
  // the form.
  await page.goto("/new?field.priority=high");
  await expect(dialog.getByLabel(/^Title/)).toBeVisible();
  await expect(dialog.getByLabel(/^Priority/)).toHaveText("high");
  await expect(dialog.getByTestId("unfiled-link-values")).toHaveCount(0);
});
