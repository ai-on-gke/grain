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

// A config pull request disabling every connection is refused as
// locking dispatch (staterepo.CheckDispatchLiveness); the same change
// made from Settings → Models is a bootstrap write and is written
// (design/design/config-repo-settings.design.md, section 4.1.1), so the page
// warns while it lasts. Found by the 2026-10-31 config-repo write-path
// test run.
for (const viewport of [
  { name: "desktop", width: 1280, height: 800 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`warns when every connection is disabled (${viewport.name})`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/models");
    const section = page.getByTestId("settings-section");
    const warning = section.getByTestId("all-connections-disabled");
    await expect(section.getByRole("button", { name: "Disable" })).toHaveCount(
      1,
    );
    await expect(warning).toHaveCount(0);

    await section.getByRole("button", { name: "Disable" }).click();
    try {
      await expect(warning).toBeVisible();
      await expect(warning).toContainText(
        "Every provider connection is disabled",
      );
      // The warning sits inside the connections card, within the page.
      const card = await section.boundingBox();
      const box = await warning.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(card.x);
      expect(box.x + box.width).toBeLessThanOrEqual(card.x + card.width + 1);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
      await warning.scrollIntoViewIfNeeded();
      await page.screenshot({
        path: testInfo.outputPath(`all-disabled-${viewport.name}.png`),
      });
      await page.screenshot({
        path: testInfo.outputPath(`all-disabled-${viewport.name}-crop.png`),
        clip: {
          x: card.x,
          y: Math.max(box.y - 120, 0),
          width: card.width,
          height: box.height + 140,
        },
      });

      await section.getByRole("button", { name: "Enable" }).click();
      await expect(warning).toHaveCount(0);
    } finally {
      // The demo server is the worker's: leave its one connection enabled.
      const settings = await (await page.request.get("/api/settings")).json();
      for (const backend of settings.agentBackends || []) {
        if (backend.disabled) {
          await page.request.put(
            `/api/deployment/agent-backends/${backend.id}`,
            { data: { ...backend, disabled: false } },
          );
        }
      }
    }
  });
}
