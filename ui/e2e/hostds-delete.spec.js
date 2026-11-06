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

// Settings → Hosts: a revoked host has a Delete action in the row's
// right-hand actions, a live one has none, and "Delete all revoked" sits
// at the card's right edge -- on a wide screen and on a phone. Deleting
// asks first, and the deleted hostd leaves the table.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Hosts offers Delete on revoked hosts on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/hostds");
    const revoked = page.getByTestId("hostd-row-old-laptop");
    await expect(revoked).toContainText("Revoked");
    await expect(revoked.getByRole("button", { name: "Delete" })).toHaveCount(
      1,
    );
    for (const live of ["cloudtop-alice", "local"]) {
      await expect(
        page
          .getByTestId(`hostd-row-${live}`)
          .getByRole("button", { name: "Delete" }),
      ).toHaveCount(0);
    }

    const bulk = page.getByTestId("hostds-delete-revoked");
    await expect(bulk).toBeVisible();
    const card = await page.locator("#hostds").boundingBox();
    const bulkBox = await bulk.boundingBox();
    expect(bulkBox.x + bulkBox.width).toBeLessThanOrEqual(
      card.x + card.width + 1,
    );
    expect(bulkBox.x).toBeGreaterThan(card.x + card.width / 2);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    if (name === "desktop") {
      const rowBox = await revoked.boundingBox();
      const del = await revoked
        .getByRole("button", { name: "Delete" })
        .boundingBox();
      expect(del.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);
      expect(del.y).toBeGreaterThanOrEqual(rowBox.y - 1);
      expect(del.y + del.height).toBeLessThanOrEqual(
        rowBox.y + rowBox.height + 1,
      );
    }
  });
}

test("deleting a revoked host asks first and removes it", async ({
  page,
  request,
}) => {
  const created = await request.post("/api/hostds", {
    data: { name: "e2e-gone" },
  });
  expect(created.status()).toBe(201);
  try {
    expect((await request.post("/api/hostds/e2e-gone/revoke")).status()).toBe(
      200,
    );
    // A live hostd is refused through the API the page uses.
    expect((await request.delete("/api/hostds/cloudtop-alice")).status()).toBe(
      400,
    );

    await page.goto("/settings/hostds");
    const row = page.getByTestId("hostd-row-e2e-gone");
    await expect(row).toContainText("Revoked");
    let asked = "";
    page.once("dialog", (dialog) => {
      asked = dialog.message();
      dialog.accept();
    });
    await row.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByTestId("hostd-delete-notice")).toContainText(
      "Deleted host e2e-gone.",
    );
    expect(asked).toContain("Delete host e2e-gone");
    await expect(row).toHaveCount(0);
    await expect(page.getByTestId("hostd-row-old-laptop")).toBeVisible();
  } finally {
    // Leave the worker's demo server as it was, whatever happened above.
    await request.post("/api/hostds/e2e-gone/revoke");
    await request.delete("/api/hostds/e2e-gone");
  }
});
