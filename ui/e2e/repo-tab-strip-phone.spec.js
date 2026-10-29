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

import { expect, test } from "./fixtures.js";

// docs/test-runs/2026-10-29-repository-release-memory-ui.md, finding 1:
// on a phone, a repo page opened at one of its last tabs -- the links
// Settings -> Repositories gives -- showed a strip whose selected tab
// sat under the right-hand scroll button. MUI scrolled the tab into a
// strip as wide as the row, then added its scroll buttons and left it
// there (tabStrip.js useRevealSelectedTab). The selected tab's box has
// to lie inside the strip's visible box, at a width where the strip
// scrolls at all.
test.use({ viewport: { width: 390, height: 844 } });

const repo = "acme/widgets";

for (const [id, label] of [
  ["advanced", "Advanced"],
  ["releases", "Releases"],
  ["ci", "CI"],
]) {
  test(`a phone opening /settings/${id} sees its ${label} tab whole`, async ({
    page,
  }) => {
    await page.goto(`/repos/${repo}/settings/${id}`);
    const tab = page.getByRole("tab", { name: label, exact: true });
    await expect(tab).toHaveAttribute("aria-selected", "true");
    const scroller = page.locator(".repo-page-header .MuiTabs-scroller");
    // The strip overflows here, or this would prove nothing.
    expect(
      await scroller.evaluate((el) => el.scrollWidth > el.clientWidth),
    ).toBe(true);
    await expect(async () => {
      const strip = await scroller.boundingBox();
      const box = await tab.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(strip.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        strip.x + strip.width + 1,
      );
    }).toPass({ timeout: 5000 });
  });
}
