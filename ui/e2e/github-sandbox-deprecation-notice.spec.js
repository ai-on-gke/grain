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

// Settings -> Capabilities says the in-process github-sandbox grant was
// retired onto the in-process github server's #sandbox-repo method, below the
// bootstrap-playbooks notice, and the notice stays inside the page at desktop
// and phone widths.
for (const [label, width, height] of [
  ["desktop", 1280, 900],
  ["phone", 390, 844],
]) {
  test(`shows the github-sandbox deprecation notice at ${label} width`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/settings/capabilities");
    const notice = page.getByTestId("github-sandbox-deprecation-notice");
    await expect(notice).toBeVisible();
    for (const text of [
      "github-sandbox",
      "https://grain.dev/capabilities/github#sandbox-repo",
      "github-app/app-id",
      "github-app/private-key",
    ]) {
      await expect(notice).toContainText(text);
    }
    const box = await notice.boundingBox();
    const above = await page
      .getByTestId("bootstrap-playbooks-deprecation-notice")
      .boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    // Stacked under the bootstrap-playbooks notice, the same width, not beside it.
    expect(box.y).toBeGreaterThanOrEqual(above.y + above.height);
    expect(Math.abs(box.width - above.width)).toBeLessThanOrEqual(1);
    expect(Math.abs(box.x - above.x)).toBeLessThanOrEqual(1);
    // The long reference wraps inside the notice rather than widening it
    // or the page.
    const clipped = await notice.evaluate(
      (el) => el.scrollWidth > el.clientWidth,
    );
    expect(clipped).toBe(false);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  });

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
