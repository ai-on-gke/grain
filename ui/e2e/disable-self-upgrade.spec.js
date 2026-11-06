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

const MANAGEMENT_URL = "https://grain.example.com/entries/alice";

test.use({
  demoArgs: ["-disable-self-upgrade", `-management-url=${MANAGEMENT_URL}`],
});

test.describe("Settings with -disable-self-upgrade", () => {
  test("replaces in-VM upgrade controls and Updates settings with external management notice on desktop", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/settings/upgrade");

    const notice = page.getByTestId("upgrade-externally-managed");
    const link = page.getByTestId("upgrade-management-link");
    await expect(notice).toBeVisible();
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute("href", MANAGEMENT_URL);

    // No in-VM self-upgrade controls, automatic-upgrade intro link, or SQLite migration form.
    const section = page.getByTestId("upgrade-section");
    await expect(
      section.getByTestId("upgrade-check-settings-link"),
    ).toHaveCount(0);
    await expect(
      section.getByRole("button", { name: /^Upgrade$/ }),
    ).toHaveCount(0);
    await expect(
      section.getByRole("button", { name: /Migrate Database to PostgreSQL/ }),
    ).toHaveCount(0);

    const noticeBox = await notice.boundingBox();
    const linkBox = await link.boundingBox();
    expect(noticeBox.width).toBeGreaterThan(200);
    expect(linkBox.x).toBeGreaterThanOrEqual(noticeBox.x);
    expect(linkBox.x + linkBox.width).toBeLessThanOrEqual(
      noticeBox.x + noticeBox.width + 1,
    );

    // Settings -> General -> Updates card shows the same external notice and hides auto-upgrade fields.
    await page.goto("/settings/general#updates");
    const updatesNotice = page.getByTestId("updates-externally-managed");
    const updatesLink = page.getByTestId("updates-management-link");
    await expect(updatesNotice).toBeVisible();
    await expect(updatesLink).toHaveAttribute("href", MANAGEMENT_URL);
    await expect(page.locator('[data-setting-id="autoUpgradeOn"]')).toHaveCount(
      0,
    );

    // API enforces refusal too.
    const upgradeRes = await fetch(`${demoServer}/api/upgrade`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ branch: "main" }),
    });
    expect(upgradeRes.status).toBe(404);
  });

  test("fits cleanly without horizontal overflow on phone width", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/settings/upgrade");

    const notice = page.getByTestId("upgrade-externally-managed");
    const link = page.getByTestId("upgrade-management-link");
    await expect(notice).toBeVisible();
    await expect(link).toBeVisible();

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    const noticeBox = await notice.boundingBox();
    const linkBox = await link.boundingBox();
    expect(noticeBox.x).toBeGreaterThanOrEqual(0);
    expect(noticeBox.x + noticeBox.width).toBeLessThanOrEqual(390);
    expect(linkBox.x + linkBox.width).toBeLessThanOrEqual(
      noticeBox.x + noticeBox.width + 1,
    );
  });
});
