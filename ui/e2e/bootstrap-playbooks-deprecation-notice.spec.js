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

// Settings -> Capabilities says the in-process bootstrap-playbooks grant was
// retired onto the config-map server's built-in bootstrap map, below the
// gemini-key notice, and the notice stays inside the page at desktop and phone
// widths.
for (const [label, width, height] of [
  ["desktop", 1280, 900],
  ["phone", 390, 844],
]) {
  test(`shows the bootstrap-playbooks deprecation notice at ${label} width`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/settings/capabilities");
    const notice = page.getByTestId("bootstrap-playbooks-deprecation-notice");
    await expect(notice).toBeVisible();
    for (const text of [
      "bootstrap-playbooks",
      "https://grain.dev/capabilities/config-map#config-map",
      '{"config_maps":["bootstrap"]}',
      "/home/debian/.config-map/bootstrap",
    ]) {
      await expect(notice).toContainText(text);
    }
    const box = await notice.boundingBox();
    const above = await page
      .getByTestId("gemini-key-deprecation-notice")
      .boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    // Stacked under the gemini-key notice, the same width, not beside it.
    expect(box.y).toBeGreaterThanOrEqual(above.y + above.height);
    expect(Math.abs(box.width - above.width)).toBeLessThanOrEqual(1);
    expect(Math.abs(box.x - above.x)).toBeLessThanOrEqual(1);
  });
}
