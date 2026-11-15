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

    // Service accounts sit in their own group, outside the people table,
    // labelled as non-human callers with full operator access and with
    // the reason they were added.
    const sa =
      "serviceAccount:release-bot@demo-project.iam.gserviceaccount.com";
    const saGroup = page.getByTestId("allowed-service-accounts");
    await expect(saGroup).toContainText("held to its role");
    await expect(page.getByTestId(`principal-row-${sa}`)).toContainText(
      "release pipeline files tasks through IAP",
    );
    await expect(table).not.toContainText(sa);

    // Every entry shows its role.
    await expect(
      page.getByTestId("principal-row-group:eng-oncall@example.com"),
    ).toContainText("grain/contributor");
    await expect(page.getByTestId(`principal-row-${sa}`)).toContainText(
      "grain/release-manager",
    );

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
    const saBox = await saGroup.boundingBox();
    expect(saBox.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(saBox.x + saBox.width).toBeLessThanOrEqual(card.x + card.width + 1);
    expect(saBox.y).toBeGreaterThan((await table.boundingBox()).y);

    if (name === "desktop") {
      const row = page.getByTestId("principal-row-user:alice@example.com");
      const rowBox = await row.boundingBox();
      const removeBtn = await row
        .getByRole("button", { name: "Remove user:alice@example.com" })
        .boundingBox();
      expect(removeBtn.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);

      const inputBox = await page
        .getByLabel("User, group or service account")
        .boundingBox();
      expect(Math.abs(addBtn.y - inputBox.y)).toBeLessThan(16);
      const reasonBox = await page
        .getByTestId("access-reason-input")
        .boundingBox();
      expect(Math.abs(reasonBox.y - inputBox.y)).toBeLessThan(4);
      expect(reasonBox.x).toBeGreaterThan(inputBox.x + inputBox.width - 1);
      expect(addBtn.x).toBeGreaterThan(reasonBox.x + reasonBox.width - 1);
      // The role picker sits on the same row, between the entry and the
      // reason.
      const roleBox = await page
        .locator("#add-allowed-principal")
        .getByRole("combobox")
        .boundingBox();
      expect(Math.abs(roleBox.y - inputBox.y)).toBeLessThan(4);
      expect(roleBox.x).toBeGreaterThan(inputBox.x + inputBox.width - 1);
      expect(reasonBox.x).toBeGreaterThan(roleBox.x + roleBox.width - 1);
    }

    // The access history sits in its own card below the list.
    const history = await page.locator("#access-history").boundingBox();
    expect(history.y).toBeGreaterThan(card.y + card.height - 1);
    expect(history.x + history.width).toBeLessThanOrEqual(viewport.width + 1);

    // Removing an operator entry opens the reason panel inside the card,
    // its reason field and buttons within it, without sideways scroll;
    // Cancel closes it and removes nothing.
    await page
      .getByRole("button", { name: "Remove user:alice@example.com" })
      .click();
    const panel = page.getByTestId("access-change-panel");
    await expect(panel).toBeVisible();
    const panelBox = await panel.boundingBox();
    expect(panelBox.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(panelBox.x + panelBox.width).toBeLessThanOrEqual(
      card.x + card.width + 1,
    );
    for (const el of [
      page.getByTestId("access-change-reason"),
      page.getByTestId("access-change-apply"),
    ]) {
      const box = await el.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(panelBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        panelBox.x + panelBox.width + 1,
      );
    }
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth + 1,
      ),
      "the page scrolls sideways with the reason panel open",
    ).toBe(false);
    await panel.getByRole("button", { name: "Cancel" }).click();
    await expect(panel).toHaveCount(0);
    await expect(
      page.getByTestId("principal-row-user:alice@example.com"),
    ).toBeVisible();
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
  const initialEntries = initialData.entries || [];

  try {
    await page.goto("/settings/access");
    await expect(page.getByTestId("allowed-principals-table")).toBeVisible();

    const input = page.getByLabel("User, group or service account");
    await input.fill("invalid-no-prefix");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByText(/must start with user:, group: or serviceAccount:/),
    ).toBeVisible();

    // A service account needs a reason before it is sent.
    await input.fill("serviceAccount:ci@demo-project.iam.gserviceaccount.com");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(page.getByText(/Give a reason/)).toBeVisible();
    await page.getByTestId("access-reason-input").fill("ci pipeline");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByTestId(
        "principal-row-serviceAccount:ci@demo-project.iam.gserviceaccount.com",
      ),
    ).toContainText("ci pipeline");

    // The default role, grain/operator, holds dangerous scopes, so it is
    // assigned only with a reason.
    await input.fill("user:new-member@example.com");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByText(/Give a reason: grain\/operator/),
    ).toBeVisible();
    await page.getByTestId("access-reason-input").fill("new teammate");
    await page.getByRole("button", { name: "Add allowed principal" }).click();
    await expect(
      page.getByTestId("principal-row-user:new-member@example.com"),
    ).toContainText("grain/operator");
    // ... and the reason is kept in the access history.
    await expect(page.getByTestId("access-history")).toContainText(
      "allowed user:new-member@example.com as grain/operator (new teammate)",
    );

    // Removing an entry whose role holds dangerous scopes asks for a
    // reason first, and refuses to go on without one.
    await page
      .getByRole("button", { name: "Remove user:new-member@example.com" })
      .click();
    await expect(page.getByTestId("access-change-panel")).toContainText(
      "give a reason",
    );
    await page.getByTestId("access-change-apply").click();
    await expect(page.getByTestId("access-change-error")).toContainText(
      "Give a reason",
    );
    await page.getByTestId("access-change-reason").fill("left the team");
    await page.getByTestId("access-change-apply").click();
    await expect(
      page.getByTestId("principal-row-user:new-member@example.com"),
    ).toHaveCount(0);
    await expect(page.getByTestId("access-history")).toContainText(
      "removed user:new-member@example.com (was grain/operator) (left the team)",
    );

    // Remove until one entry remains, then verify removing the last entry
    // warns first. Each removal gives a reason when the panel asks.
    const remove = async (entry) => {
      const row = page.getByTestId(`principal-row-${entry}`);
      const panel = page.getByTestId("access-change-panel");
      await page.getByRole("button", { name: `Remove ${entry}` }).click();
      await Promise.race([
        row.waitFor({ state: "detached" }),
        panel.waitFor({ state: "visible" }),
      ]);
      if (await panel.isVisible()) {
        await page.getByTestId("access-change-reason").fill("cleanup");
        await page.getByTestId("access-change-apply").click();
      }
      await expect(row).toHaveCount(0);
    };
    for (const entry of [
      "serviceAccount:ci@demo-project.iam.gserviceaccount.com",
      "serviceAccount:release-bot@demo-project.iam.gserviceaccount.com",
      "group:eng-oncall@example.com",
    ]) {
      await remove(entry);
    }

    // Last entry -> shows warning before removing.
    await page
      .getByRole("button", { name: "Remove user:alice@example.com" })
      .click();
    await expect(page.getByTestId("access-remove-warning")).toContainText(
      "removing the last allowed entry",
    );
    await page.getByTestId("access-change-reason").fill("last one");
    await page.getByRole("button", { name: "Remove anyway" }).click();
    await expect(page.getByTestId("allowed-principals-empty")).toBeVisible();
    await expect(page.getByTestId("access-removed-notice")).toContainText(
      "access code still works on a direct connection",
    );
  } finally {
    // Restore initial demo state for any subsequent specs sharing demoServer.
    const current = await (await request.get("/api/access/principals")).json();
    for (const e of current.entries || []) {
      await request.delete(
        `/api/access/principals/${encodeURIComponent(e.entry)}`,
      );
    }
    for (const { entry, reason, role } of initialEntries) {
      await request.post("/api/access/principals", {
        data: { entry, role, reason: reason || "restoring the demo" },
      });
    }
  }
});

test("Settings → Access wraps a long history, a long reason, and a refusal on the panel without overflow", async ({
  page,
  request,
}) => {
  const initialData = await (
    await request.get("/api/access/principals")
  ).json();
  const initialEntries = initialData.entries || [];
  const longReason =
    "Quarterly access audit rotation: narrowing secondary operator sessions after incident review #4821 while preserving primary break-glass coverage and documenting the ticket trail in full detail for compliance.";

  try {
    // Seed 12 history rows including a long reason so the Access history card is populated.
    for (let i = 0; i < 6; i++) {
      await request.patch(
        `/api/access/principals/${encodeURIComponent("group:eng-oncall@example.com")}`,
        {
          data: {
            role: i % 2 === 0 ? "grain/viewer" : "grain/contributor",
            reason: i === 5 ? longReason : `scheduled audit check #${i + 1}`,
          },
        },
      );
    }

    for (const viewport of [DESKTOP, PHONE]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/access");
      await expect(page.getByTestId("allowed-principals-table")).toBeVisible();
      await expect(page.getByTestId("access-history")).toContainText(
        longReason,
      );

      // Open the reason panel on an operator entry, trigger the missing-reason error, then fill the long reason.
      await page
        .getByRole("button", { name: "Remove user:alice@example.com" })
        .click();
      const panel = page.getByTestId("access-change-panel");
      await expect(panel).toBeVisible();
      await page.getByTestId("access-change-apply").click();
      const errBox = page.getByTestId("access-change-error");
      await expect(errBox).toBeVisible();
      await page.getByTestId("access-change-reason").fill(longReason);

      const cardBox = await page.locator("#allowed-principals").boundingBox();
      const panelBox = await panel.boundingBox();
      const errRect = await errBox.boundingBox();
      const applyRect = await page
        .getByTestId("access-change-apply")
        .boundingBox();
      const histBox = await page.locator("#access-history").boundingBox();

      expect(panelBox.x).toBeGreaterThanOrEqual(cardBox.x - 1);
      expect(panelBox.x + panelBox.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
      expect(errRect.x).toBeGreaterThanOrEqual(panelBox.x - 1);
      expect(errRect.x + errRect.width).toBeLessThanOrEqual(
        panelBox.x + panelBox.width + 1,
      );
      expect(applyRect.y).toBeGreaterThanOrEqual(
        errRect.y + errRect.height - 1,
      );
      expect(histBox.y).toBeGreaterThan(cardBox.y + cardBox.height - 1);
      expect(histBox.x + histBox.width).toBeLessThanOrEqual(viewport.width + 1);
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth + 1,
        ),
      ).toBe(false);

      await panel.getByRole("button", { name: "Cancel" }).click();
    }
  } finally {
    const current = await (await request.get("/api/access/principals")).json();
    for (const e of current.entries || []) {
      await request.delete(
        `/api/access/principals/${encodeURIComponent(e.entry)}`,
      );
    }
    for (const { entry, reason, role } of initialEntries) {
      await request.post("/api/access/principals", {
        data: { entry, role, reason: reason || "restoring the demo" },
      });
    }
  }
});
