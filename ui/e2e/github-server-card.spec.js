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

// The in-process github server's card on Settings -> Capabilities
// renders without widening the page at desktop and phone widths.
for (const [label, width, height] of [
  ["desktop", 1280, 900],
  ["phone", 390, 844],
]) {
  test(`renders the in-process github server card cleanly at ${label} width`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto(
      "/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fgithub",
    );
    const card = page.locator('[data-in-process="github"]');
    await expect(card).toBeVisible();
    await expect(card).toContainText("github server");
    await expect(card).toContainText("https://grain.dev/capabilities/github");
    await expect(card).toContainText("sandbox-repo");
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  });
}
