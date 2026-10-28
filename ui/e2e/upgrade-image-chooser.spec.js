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

// Settings → Upgrade on an image deployment says which image the button
// will pull and lets the operator choose another repository the daemon
// allows (grain/task-543). `grain demo -upgrade-image` wires a real
// image-mode upgrader; nothing of grain's own API is mocked.

// Core grain's own repository, the one repository core knows to offer
// (pkg/installtrack's KnownSuccessorRepositories).
const GHCR = "ghcr.io/bwsalmon/grain/grain";

test.use({ demoArgs: [`-upgrade-image=${GHCR}`] });

test.describe("the upgrade image chooser", () => {
  for (const [name, viewport] of [
    ["desktop", { width: 1440, height: 900 }],
    ["phone", { width: 390, height: 844 }],
  ]) {
    test(`lines up under the branch field at ${name} width`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto("/settings/upgrade");
      const branch = page.getByLabel("Branch");
      const image = page.getByLabel("Image (optional)");
      await expect(image).toBeVisible();
      await branch.fill("main");
      const willPull = page.locator(".upgrade-image-ref code");
      await expect(willPull).toHaveText(`${GHCR}:main`);

      // Same column as the branch field, below it.
      // The outlined boxes, not the inputs: an Autocomplete pads its own
      // input differently from a plain field's.
      const outline =
        "xpath=ancestor::div[contains(@class,'MuiInputBase-root')][1]";
      const branchBox = await branch.locator(outline).boundingBox();
      const imageBox = await image.locator(outline).boundingBox();
      expect(Math.abs(imageBox.x - branchBox.x)).toBeLessThanOrEqual(1);
      expect(Math.abs(imageBox.width - branchBox.width)).toBeLessThanOrEqual(1);
      expect(imageBox.y).toBeGreaterThan(branchBox.y + branchBox.height);

      // The reference inside the page. Core offers one repository, so
      // there are no repository chips to choose between (UpgradePanel
      // shows them only for more than one).
      const box = await willPull.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }
});
