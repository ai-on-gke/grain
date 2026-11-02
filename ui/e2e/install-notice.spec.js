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

// The notice an unmigrated host-mode install gets (pkg/daemon's
// installnotice.go, GET /api/config's installNotice): a standing banner
// above the top bar on every view, the same notice in full in System →
// Health and before an upgrade in Settings → Upgrade. `grain demo
// -host-mode-install` reports it; nothing of grain's own API is mocked.

test.use({ demoArgs: ["-host-mode-install"] });

test.describe("a host-mode install", () => {
  for (const [name, viewport] of [
    ["desktop", { width: 1440, height: 900 }],
    ["phone", { width: 390, height: 844 }],
  ]) {
    test(`shows the banner across the top at ${name} width`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto("/");
      const banner = page.getByTestId("install-notice-banner");
      await expect(banner).toBeVisible();
      await expect(banner).toContainText("This install is still host mode");
      await expect(banner).toContainText("re-run scripts/setup.sh");
      await expect(page.getByTestId("install-notice-banner-clis")).toHaveText(
        "claude",
      );
      await expect(banner.getByTestId("install-notice-docs")).toHaveAttribute(
        "href",
        /cli-run-location\.design\.md/,
      );

      // The full width of the page, at its top, and nothing of it
      // pushed past the right edge.
      const box = await banner.boundingBox();
      expect(box.x).toBe(0);
      expect(box.y).toBe(0);
      expect(Math.round(box.width)).toBe(viewport.width);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }

  test("System → Health and Settings → Upgrade say it in full", async ({
    page,
  }) => {
    await page.goto("/system/health");
    const health = page.getByTestId("health-install-notice");
    await expect(health).toBeVisible();
    await expect(health.getByTestId("install-notice-command")).toContainText(
      "sudo ./scripts/setup.sh",
    );
    await expect(health.getByTestId("install-notice-clis")).toContainText(
      "claude: no host binary, and no container location on a bare-machine daemon",
    );

    await page.goto("/settings/upgrade");
    await expect(page.getByTestId("upgrade-install-notice")).toBeVisible();
  });
});
