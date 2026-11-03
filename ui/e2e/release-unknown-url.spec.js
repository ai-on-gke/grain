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

// docs/test-runs/2026-11-03-repository-release-memory-ui.md, observation
// 4: a link naming a release the repo does not have drew the newest
// release as if it had been asked for, with only a toast gone in five
// seconds to say otherwise. The pane now says so in place, above the
// picker, until the reader dismisses it or picks a release.
test.use({ demoArgs: ["-github-sim", "-qualification"] });

test.describe("A release URL naming no release of the repo", () => {
  for (const width of [1280, 390]) {
    test(`says so in place at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/repos/acme/widgets/releases/nosuch-2700");

      const notice = page.getByRole("alert").filter({
        hasText: "acme/widgets has no release named “nosuch-2700”",
      });
      await expect(notice).toBeVisible();
      await expect(notice).toContainText("showing 1.0 instead");
      // The address stops naming it, and the page is the release list.
      await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases$/);
      const picker = page.getByRole("combobox", { name: "Release" });
      await expect(picker).toContainText("1.0");

      // Above the picker, and inside the page rather than overflowing a
      // phone's width.
      const box = await notice.boundingBox();
      const pickerBox = await picker.boundingBox();
      expect(box.y + box.height).toBeLessThanOrEqual(pickerBox.y);
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);

      // It is still there after the toast it replaces would have gone.
      await page.waitForTimeout(6000);
      await expect(notice).toBeVisible();

      await notice.getByRole("button", { name: "Close" }).click();
      await expect(notice).toHaveCount(0);
    });
  }
});
