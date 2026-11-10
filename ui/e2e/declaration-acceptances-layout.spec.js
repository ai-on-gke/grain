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

// Settings → Capabilities → Repository declarations over the demo's seeded
// rows (pkg/demo seedDemoDeclarations): a CI job that widened past what was
// accepted is listed first, awaiting acceptance, with its status beside its
// id and Accept inside its card; the accepted one offers no Accept. Nothing
// pushes the page sideways on a phone, however long a grant's spelling is.
// Nothing here presses Accept: the demo server is shared by the suite's
// workers, and an acceptance cannot be taken back.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Repository declarations fit a ${name} screen`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities#declarations");

    const widened = page.locator(
      '[data-declaration="ci:acme/widgets:deploy-preview"]',
    );
    const accepted = page.locator(
      '[data-declaration="ci:acme/widgets:publish-images"]',
    );
    await expect(widened).toBeVisible();
    await expect(widened.getByTestId("declaration-status")).toHaveText(
      "Awaiting acceptance",
    );
    await expect(widened).toContainText("(new since it was accepted)");
    await expect(accepted.getByTestId("declaration-status")).toHaveText(
      "Accepted",
    );
    await expect(accepted.getByRole("button", { name: "Accept" })).toHaveCount(
      0,
    );

    // Awaiting first.
    const ids = await page
      .locator(".declaration-acceptance")
      .evaluateAll((els) => els.map((el) => el.dataset.declaration));
    expect(ids.indexOf("ci:acme/widgets:deploy-preview")).toBeLessThan(
      ids.indexOf("ci:acme/widgets:publish-images"),
    );

    // The status chip sits on the id's line, and Accept inside the card.
    const card = await widened.boundingBox();
    const id = await widened.locator(".declaration-id").boundingBox();
    const chip = await widened.getByTestId("declaration-status").boundingBox();
    const centre = (b) => b.y + b.height / 2;
    if (name === "desktop") {
      expect(Math.abs(centre(chip) - centre(id))).toBeLessThan(4);
    }
    const accept = await widened
      .getByRole("button", { name: "Accept" })
      .boundingBox();
    expect(accept.x).toBeGreaterThanOrEqual(card.x);
    expect(accept.y + accept.height).toBeLessThanOrEqual(card.y + card.height);
    expect(chip.x + chip.width).toBeLessThanOrEqual(card.x + card.width + 1);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}
