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

// A repo row's chip strip is clipped to one line, and ChipStrip hides
// the chips that wrapped -- the last ones first. The demo's acme/widgets
// has tasks in seven states and one blocked, and the Blocked chip used
// to come after all seven counts: the counts filled a 1280px row by
// themselves, so the one chip RepoList says is there to make a stuck repo
// stand out was hidden at every width (grain/task-2235). It leads the
// strip now, and this asserts it is laid out inside the row, where
// jsdom has no layout to say so.
for (const width of [1280, 390]) {
  test(`a repo's Blocked chip stays visible on its row at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 844 });
    await page.goto("/repos");
    const row = page
      .locator(".repo-list-row")
      .filter({ has: page.getByRole("link", { name: "acme/widgets" }) });
    const blocked = row
      .locator(".chips > *")
      .filter({ hasText: /^Blocked \d+$/ });
    await expect(blocked).toBeVisible();

    const chip = await blocked.boundingBox();
    const strip = await row.boundingBox();
    expect(chip.width).toBeGreaterThan(0);
    expect(chip.x).toBeGreaterThanOrEqual(strip.x);
    expect(chip.x + chip.width).toBeLessThanOrEqual(strip.x + strip.width);
    expect(chip.y).toBeGreaterThanOrEqual(strip.y);
    expect(chip.y + chip.height).toBeLessThanOrEqual(strip.y + strip.height);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  });
}
