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

// Settings → Hosts (/settings/hosts and /settings/hostds): Invite hosts
// shared --join snippet, Pending join requests, the host registry
// table, and the legacy per-host token form -- on a wide screen and on a
// phone, with nothing pushing the page sideways.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Hosts fits a ${name} screen`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/hosts");

    await expect(
      page.getByRole("heading", { name: "Hosts", level: 1 }),
    ).toBeVisible();

    const snippet = page.getByTestId("invite-hosts-snippet");
    await expect(snippet).toBeVisible();
    await expect(snippet).toContainText(
      "/hostd/install | sh -s -- --join ghe_",
    );

    const pendingCard = page.getByTestId("pending-hosts-card");
    await expect(pendingCard).toBeVisible();
    await expect(pendingCard).toContainText("devbox-carol.example.com");
    await expect(pendingCard).toContainText("carol@example.com");
    await expect(pendingCard).not.toContainText("Pairing code");

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

    const inviteBox = await page.locator("#invite-hosts").boundingBox();
    const snippetBox = await snippet.boundingBox();
    const copyInvite = await page
      .getByRole("button", { name: "Copy invite command" })
      .boundingBox();
    expect(snippetBox.x).toBeGreaterThanOrEqual(inviteBox.x - 1);
    expect(copyInvite.x + copyInvite.width).toBeLessThanOrEqual(
      inviteBox.x + inviteBox.width + 1,
    );
    expect(Math.abs(copyInvite.y - snippetBox.y)).toBeLessThan(
      copyInvite.height,
    );

    const pendingBox = await page.locator("#pending-hosts").boundingBox();
    const acceptBtn = await pendingCard
      .getByRole("button", { name: "Accept" })
      .boundingBox();
    const rejectBtn = await pendingCard
      .getByRole("button", { name: "Reject" })
      .boundingBox();
    expect(acceptBtn.x).toBeGreaterThanOrEqual(pendingBox.x - 1);
    expect(rejectBtn.x + rejectBtn.width).toBeLessThanOrEqual(
      pendingBox.x + pendingBox.width + 1,
    );
    expect(Math.abs(acceptBtn.y - rejectBtn.y)).toBeLessThan(4);

    const card = await page.locator("#add-hostd").boundingBox();
    const add = await page
      .getByRole("button", { name: "Add host" })
      .boundingBox();
    expect(add.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(add.x + add.width).toBeLessThanOrEqual(card.x + card.width + 1);
    if (name === "desktop") {
      // The actions sit at the row's right edge, past the auth column.
      const rowBox = await ws.boundingBox();
      const rotate = await ws
        .getByRole("button", { name: "Rotate" })
        .boundingBox();
      expect(rotate.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);
      const nameField = await page
        .getByLabel("Name", { exact: true })
        .boundingBox();
      expect(Math.abs(add.y - nameField.y)).toBeLessThan(16);
    }
  });
}

test("Accepting a pending host moves it into the Hosts table as key-bound", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await page.goto("/settings/hostds");
  const pendingCard = page.getByTestId("pending-hosts-card");
  await expect(pendingCard).toContainText("devbox-carol.example.com");
  await pendingCard.getByRole("button", { name: "Accept" }).click();

  const row = page.getByTestId("hostd-row-devbox-carol");
  await expect(row).toBeVisible();
  await expect(row).toContainText("carol@example.com");
  await expect(row).toContainText("cap=4");
  await expect(row).toContainText("pool=workstation");
  await expect(row).toContainText("via workstations");
  await expect(pendingCard).toContainText("No hosts are waiting for approval.");
});
