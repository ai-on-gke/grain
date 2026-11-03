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

// Settings → Hostds: the registry the demo seeds (local, a workstation
// with an owner and labels, a runner with no owner, a revoked laptop) in
// its table, the actions in the right-hand column, and the add form
// inside its card -- on a wide screen and on a phone, with nothing
// pushing the page sideways.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Hostds fits a ${name} screen`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/hostds");

    const table = page.getByTestId("hostds-table");
    await expect(table).toBeVisible();
    const ws = page.getByTestId("hostd-row-cloudtop-alice");
    await expect(ws).toContainText("alice@example.com");
    await expect(ws).toContainText("gpu=yes");
    await expect(ws).toContainText("Never connected");
    await expect(page.getByTestId("hostd-row-old-laptop")).toContainText(
      "Revoked",
    );
    await expect(
      page.getByTestId("hostd-owner-missing-build-runner"),
    ).toContainText("needed to dial in through IAP");
    const local = page.getByTestId("hostd-row-local");
    await expect(local).toContainText("Co-located (socket)");
    await expect(local.getByRole("button", { name: "Revoke" })).toHaveCount(0);

    // The table may scroll inside its card on a phone; the page may not.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    const card = await page.locator("#add-hostd").boundingBox();
    const add = await page
      .getByRole("button", { name: "Add hostd" })
      .boundingBox();
    expect(add.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(add.x + add.width).toBeLessThanOrEqual(card.x + card.width + 1);
    if (name === "desktop") {
      // The actions sit at the row's right edge, past the token column.
      const rowBox = await ws.boundingBox();
      const rotate = await ws
        .getByRole("button", { name: "Rotate" })
        .boundingBox();
      expect(rotate.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);
      const nameField = await page.getByLabel("Name").boundingBox();
      expect(Math.abs(add.y - nameField.y)).toBeLessThan(16);
    }
  });
}
