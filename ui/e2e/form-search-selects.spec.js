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

// The task, schedule and template forms' data-driven pickers are
// SearchSelects (grain/task-2910): typed into, they narrow to what
// matches anywhere in an option's text, and they sit in their form as
// the plain fields around them do -- same left edge, same width -- with
// the open list under the box and on screen, at desktop and phone widths.

import { expect, test } from "./fixtures.js";
import { chooseOption } from "./searchSelect.js";

for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  test(`the review picker searches templates and lines up with the form at ${width}px`, async ({
    page,
  }) => {
    const stamp = Date.now().toString(36);
    const res = await page.request.post("/api/templates", {
      data: {
        name: `Zeta review ${stamp}`,
        title: "Review the change",
        description: "Read what the task proposed.",
      },
    });
    expect(res.ok(), await res.text()).toBe(true);
    const template = await res.json();
    try {
      await page.setViewportSize({ width, height });
      await page.goto("/new");
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("button", { name: "Advanced options" }).click();
      const review = dialog.getByRole("combobox", { name: "Review" });
      await expect(review).toHaveValue("No review");
      await review.scrollIntoViewIfNeeded();

      // The box is as wide as the plain field beside it and starts where
      // it does.
      const prompt = dialog.getByLabel("Prompt extension override");
      const boxOf = (l) =>
        l.locator(
          "xpath=ancestor::div[contains(@class,'MuiFormControl-root')][1]",
        );
      const a = await boxOf(review).boundingBox();
      const b = await boxOf(prompt).boundingBox();
      expect(Math.abs(a.x - b.x)).toBeLessThan(1);
      expect(Math.abs(a.width - b.width)).toBeLessThan(1);

      // A word from the middle of the name finds it, and the list hangs
      // off the box -- under it, or over it where the viewport has no
      // room below -- and inside the viewport.
      await review.click();
      await review.fill(`review ${stamp}`);
      const listbox = page.getByRole("listbox");
      await expect(listbox.getByRole("option")).toHaveCount(1);
      // Polled: the list is placed again as the search shrinks it.
      await expect(async () => {
        const list = await listbox.boundingBox();
        const input = await review.boundingBox();
        const below = list.y >= input.y + input.height - 1;
        const above = list.y + list.height <= input.y + 1;
        expect(below || above).toBe(true);
        expect(list.y).toBeGreaterThanOrEqual(0);
        expect(list.y + list.height).toBeLessThanOrEqual(height + 0.5);
        expect(list.x).toBeGreaterThanOrEqual(0);
        expect(list.x + list.width).toBeLessThanOrEqual(width + 0.5);
      }).toPass();
      await page.keyboard.press("Enter");
      await expect(review).toHaveValue(`Zeta review ${stamp}`);
      // Escape on a closed list still reaches the drawer; on an open one
      // it only closes the list.
      await review.click();
      await expect(review).toHaveAttribute("aria-expanded", "true");
      await page.keyboard.press("Escape");
      await expect(review).toHaveAttribute("aria-expanded", "false");
      await expect(dialog).toBeVisible();
    } finally {
      await page.request.delete(`/api/templates/${template.id}`);
    }
  });
}

test("the schedule form's time zone is found by any part of its name", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/schedules");
  await page.getByRole("button", { name: "+ New schedule" }).click();
  await expect(
    page.getByRole("heading", { name: "New schedule" }),
  ).toBeVisible();
  const zone = page.getByRole("combobox", { name: /^Time zone/ });
  await expect(zone).toHaveValue("UTC (default)");
  await zone.click();
  await zone.fill("tokyo");
  await expect(page.getByRole("listbox").getByRole("option")).toHaveText([
    /^Asia\/Tokyo/,
  ]);
  await page.keyboard.press("Enter");
  await expect(zone).toHaveValue(/^Asia\/Tokyo/);
  await expect(
    page.locator('input[type="hidden"][name="timeZone"]'),
  ).toHaveValue("Asia/Tokyo");
  await chooseOption(zone, "");
  await expect(zone).toHaveValue("UTC (default)");
});
