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

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Access fits a ${name} screen`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/access");

    const table = page.getByTestId("allowed-principals-table");
    await expect(table).toBeVisible();
    await expect(
      page.getByTestId("principal-row-user:alice@example.com"),
    ).toContainText("user:alice@example.com");
    await expect(
      page.getByTestId("principal-row-group:eng-oncall@example.com"),
    ).toContainText("group:eng-oncall@example.com");

    // No horizontal page overflow at either desktop or phone width.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    const card = await page.locator("#allowed-principals").boundingBox();
    const addBtn = await page
      .getByRole("button", { name: "Add allowed principal" })
      .boundingBox();
    expect(addBtn.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(addBtn.x + addBtn.width).toBeLessThanOrEqual(
      card.x + card.width + 1,
    );

    if (name === "desktop") {
      const row = page.getByTestId("principal-row-user:alice@example.com");
      const rowBox = await row.boundingBox();
      const removeBtn = await row
        .getByRole("button", { name: "Remove user:alice@example.com" })
        .boundingBox();
      expect(removeBtn.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);

      const inputBox = await page.getByLabel("User or group").boundingBox();
      expect(Math.abs(addBtn.y - inputBox.y)).toBeLessThan(16);
    }
  });
}

test("Settings → Access validates, adds, warns on last-entry removal, and restores demo state", async ({
  page,
  request,
}) => {
  await page.setViewportSize(DESKTOP);
  const initialResp = await request.get("/api/access/principals");
  expect(initialResp.ok()).toBeTruthy();
  const initialData = await initialResp.json();
  const initialEntries = (initialData.entries || []).map((e) => e.entry);

  try {
    await page.goto("/settings/access");
    await expect(page.getByTestId("allowed-principals-table")).toBeVisible();

    const input = page.getByLabel("User or group");
    await input.fill("invalid-no-prefix");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByText(/must start with user: or group:/),
    ).toBeVisible();

    await input.fill("user:new-member@example.com");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByTestId("principal-row-user:new-member@example.com"),
    ).toBeVisible();

    // Remove until one entry remains, then verify removing the last entry warns first.
    await page
      .getByRole("button", { name: "Remove user:new-member@example.com" })
      .click();
    await expect(
      page.getByTestId("principal-row-user:new-member@example.com"),
    ).toHaveCount(0);

    await page
      .getByRole("button", { name: "Remove group:eng-oncall@example.com" })
      .click();
    await expect(
      page.getByTestId("principal-row-group:eng-oncall@example.com"),
    ).toHaveCount(0);

    // Last entry -> shows warning before removing.
    await page
      .getByRole("button", { name: "Remove user:alice@example.com" })
      .click();
    await expect(page.getByTestId("access-remove-warning")).toContainText(
      "removing the last allowed entry",
    );
    await page.getByRole("button", { name: "Remove anyway" }).click();
    await expect(page.getByTestId("allowed-principals-empty")).toBeVisible();
    await expect(page.getByTestId("access-removed-notice")).toContainText(
      "access code always still works",
    );
  } finally {
    // Restore initial demo state for any subsequent specs sharing demoServer.
    const current = await (await request.get("/api/access/principals")).json();
    for (const e of current.entries || []) {
      await request.delete(
        `/api/access/principals/${encodeURIComponent(e.entry)}`,
      );
    }
    for (const entry of initialEntries) {
      await request.post("/api/access/principals", { data: { entry } });
    }
  }
});
