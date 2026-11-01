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

// docs/test-runs/2026-11-01-repository-release-memory-ui.md, finding 1:
// on a phone the Repos header is drawn in the page, not in the rail's
// slot (which reaches what wraps through More page actions), and the
// add-repo picker's 300px minimum beside the title put Add repo at
// x=423..487 of a 390px viewport, where nothing could reach it. The
// field and its button both have to lie inside the viewport, the button
// has to be what a tap on it lands on, and the field has to be left wide
// enough to type an owner/name into.
for (const width of [360, 390]) {
  test.describe(`at ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });

    test("the Repos page's add-repo form fits the screen", async ({ page }) => {
      await page.goto("/repos");
      const field = page.getByLabel("Repo to add");
      const add = page.getByRole("button", { name: "Add repo", exact: true });
      await expect(add).toBeVisible();
      const viewport = page.viewportSize();
      const fieldBox = await field.boundingBox();
      const addBox = await add.boundingBox();
      expect(fieldBox.x).toBeGreaterThanOrEqual(0);
      expect(fieldBox.width).toBeGreaterThanOrEqual(150);
      expect(addBox.x).toBeGreaterThan(fieldBox.x + fieldBox.width - 1);
      expect(addBox.x + addBox.width).toBeLessThanOrEqual(viewport.width);
      const hit = await page.evaluate(
        ([x, y]) =>
          document.elementFromPoint(x, y)?.closest("button")?.textContent,
        [addBox.x + addBox.width / 2, addBox.y + addBox.height / 2],
      );
      expect(hit).toBe("Add repo");
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(viewport.width);
    });
  });
}
