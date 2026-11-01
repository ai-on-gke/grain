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

// The (i) popovers Settings -> Capabilities puts server, method and
// config-field descriptions in (43a50379, PR #216): docs/test-plans/
// 2026-11-01.md §7 gap 5 asks that they stay inside a phone's viewport
// and that keyboard focus opens and closes them.

import { expect, test } from "./fixtures.js";

const DEMO = "https://capabilities.demo.grain.example/cloud";

for (const width of [1440, 390]) {
  test(`capability (i) popovers open on focus, close on Escape and fit at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/settings/capabilities#capability-servers");
    await page.locator(`[data-capability-server-row="${DEMO}"]`).click();

    const pops = page.getByTestId("info-popover");
    await expect(pops.first()).toBeVisible();
    const count = Math.min(await pops.count(), 6);
    expect(count).toBeGreaterThan(0);

    for (let i = 0; i < count; i++) {
      const pop = pops.nth(i);
      if (!(await pop.isVisible())) continue;
      await pop.scrollIntoViewIfNeeded();

      // Focus alone opens it, and the description is the button's own.
      await pop.focus();
      const tip = page.getByRole("tooltip");
      await expect(tip).toBeVisible();
      await expect(tip).not.toBeEmpty();
      const box = await tip.boundingBox();
      expect(box).not.toBeNull();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      if (i === 0) {
        await page.screenshot({
          path: testInfo.outputPath(`popover-${width}.png`),
          clip: {
            x: 0,
            y: Math.max(0, box.y - 60),
            width,
            height: box.height + 120,
          },
        });
      }

      // Escape closes it while focus stays put; Enter toggles it back.
      await pop.press("Escape");
      await expect(tip).toBeHidden();
      await expect(pop).toBeFocused();
      await pop.press("Enter");
      await expect(page.getByRole("tooltip")).toBeVisible();
      await pop.press("Enter");
      await expect(page.getByRole("tooltip")).toBeHidden();
    }
  });
}
