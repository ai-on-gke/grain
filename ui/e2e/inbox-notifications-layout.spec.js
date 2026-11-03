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
import { alertRow, closeAlerts, openAlerts } from "./alerts.js";

// Operator notifications (design/design/durable-tasks.design.md DT-5) on the
// Inbox and in the alerts bell, over the demo's three seeded ones
// (pkg/demo's seedDemoNotifications): an open stalled-merge warning, a
// pending release asking for Approve or Reject, and an agent's
// acknowledged info note. Nothing here acknowledges or
// investigates -- the demo server is shared by the worker -- so the
// assertions are about what is shown and where.

test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function noHorizontalOverflow(page) {
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(scrollWidth, "the page scrolls sideways").toBeLessThanOrEqual(
    clientWidth + 1,
  );
}

test("the Inbox lists open notifications above the tasks, with their actions in the row", async ({
  page,
}) => {
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    const section = page.getByTestId("inbox-notifications");
    await expect(section).toBeVisible();
    const toggle = section.locator(".inbox-group-toggle");
    await expect(toggle).toBeVisible();

    // Chevron, heading and count sit on one horizontal centre line inside
    // the toggle button.
    const chevronBox = await toggle
      .locator(".inbox-group-chevron")
      .boundingBox();
    const headingBox = await toggle.locator("h3").boundingBox();
    const countBox = await toggle.locator(".count").boundingBox();
    const headingMid = headingBox.y + headingBox.height / 2;
    expect(
      Math.abs(chevronBox.y + chevronBox.height / 2 - headingMid),
    ).toBeLessThanOrEqual(2);
    expect(
      Math.abs(countBox.y + countBox.height / 2 - headingMid),
    ).toBeLessThanOrEqual(2);
    expect(chevronBox.x).toBeLessThan(headingBox.x);
    expect(headingBox.x).toBeLessThan(countBox.x);

    const rows = section.getByTestId("inbox-notification");
    if (viewport === DESKTOP) {
      // Notifications starts collapsed by default; expanding it persists
      // across the reload into the phone viewport below.
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(rows).toHaveCount(0);
      await toggle.click();
    }
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(rows).toHaveCount(3);

    const warning = rows.filter({ hasText: "A merge has been stalled" });
    await expect(
      warning.getByRole("button", { name: "Acknowledge" }),
    ).toBeVisible();
    await expect(
      warning.getByRole("button", { name: "Investigate" }),
    ).toBeVisible();
    await expect(
      warning.getByRole("button", { name: "Open task" }),
    ).toBeVisible();

    // A pending release leads with what it asks for, Approve then Reject,
    // on one line with the rest of its actions and inside its row.
    const release = rows.filter({ hasText: "v2.4.0 is ready to promote" });
    const approve = release.getByRole("button", { name: "Approve" });
    const reject = release.getByRole("button", { name: "Reject" });
    await expect(approve).toBeVisible();
    await expect(reject).toBeVisible();
    const releaseBox = await release.boundingBox();
    const approveBox = await approve.boundingBox();
    const rejectBox = await reject.boundingBox();
    const ackBox = await release
      .getByRole("button", { name: "Acknowledge" })
      .boundingBox();
    expect(approveBox.x).toBeLessThan(rejectBox.x);
    expect(rejectBox.x).toBeLessThan(ackBox.x);
    expect(Math.abs(approveBox.y - rejectBox.y)).toBeLessThanOrEqual(1);
    for (const box of [approveBox, rejectBox, ackBox]) {
      expect(box.x).toBeGreaterThanOrEqual(releaseBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        releaseBox.x + releaseBox.width + 1,
      );
    }

    // Acknowledged: still listed, marked, and with nothing to acknowledge.
    const acked = rows.filter({ hasText: "webkit e2e job looks flaky" });
    await expect(acked).toContainText("acknowledged");
    await expect(
      acked.getByRole("button", { name: "Acknowledge" }),
    ).toHaveCount(0);

    // Every button sits inside its row, and the row inside the window.
    const rowBox = await warning.boundingBox();
    expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(viewport.width + 1);
    for (const button of await warning.getByRole("button").all()) {
      const box = await button.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(rowBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        rowBox.x + rowBox.width + 1,
      );
    }

    // Above the task groups, not among them.
    const firstGroup = page.locator(".inbox-group").first();
    await expect(firstGroup).toHaveAttribute(
      "data-testid",
      "inbox-notifications",
    );
    await noHorizontalOverflow(page);
  }
});

test("the alerts bell lists the open notification and not the acknowledged one", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await page.goto("/");
  await openAlerts(page);
  const row = alertRow(page, "A merge has been stalled");
  await expect(row).toHaveCount(1);
  await expect(row).toHaveAttribute("data-severity", "warning");
  await expect(row.getByRole("button", { name: "Acknowledge" })).toBeVisible();
  const release = alertRow(page, "v2.4.0 is ready to promote");
  await expect(release.getByRole("button", { name: "Approve" })).toBeVisible();
  await expect(release.getByRole("button", { name: "Reject" })).toBeVisible();
  await expect(alertRow(page, "webkit e2e job looks flaky")).toHaveCount(0);
  await closeAlerts(page);
});
