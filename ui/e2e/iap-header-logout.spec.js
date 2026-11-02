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

test.describe("IAP signed-in header badge and logout", () => {
  test("aligns the email badge in the header rail, opens Access settings, and logs out to /_gcp_iap/clear_login_cookie", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });

    await page.route("**/api/config", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          authenticated: true,
          authMethod: "iap",
          actor: "alice@example.com",
          actorKind: "human",
        },
      });
    });

    await page.route("**/api/auth/logout", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          loggedOut: true,
          redirect: "/_gcp_iap/clear_login_cookie",
        }),
      });
    });

    let clearedIAPCookie = false;
    await page.route("**/_gcp_iap/clear_login_cookie", async (route) => {
      clearedIAPCookie = true;
      await route.fulfill({
        status: 200,
        contentType: "text/plain",
        body: "logged out of IAP",
      });
    });

    await page.goto("/");
    const globals = page.getByTestId("page-header-globals");
    const gear = page.getByTestId("top-bar-settings");
    const badge = page.getByTestId("top-bar-user");

    await expect(badge).toBeVisible();
    await expect(badge).toHaveText("alice@example.com");

    const globalsBox = await globals.boundingBox();
    const gearBox = await gear.boundingBox();
    const badgeBox = await badge.boundingBox();
    expect(globalsBox).not.toBeNull();
    expect(gearBox).not.toBeNull();
    expect(badgeBox).not.toBeNull();

    // Badge sits horizontally after Settings gear without overlapping,
    // vertically centred with the gear icon within 2px, and inside globals.
    expect(badgeBox.x).toBeGreaterThanOrEqual(gearBox.x + gearBox.width - 1);
    const gearCenterY = gearBox.y + gearBox.height / 2;
    const badgeCenterY = badgeBox.y + badgeBox.height / 2;
    expect(Math.abs(gearCenterY - badgeCenterY)).toBeLessThanOrEqual(2);
    expect(badgeBox.x + badgeBox.width).toBeLessThanOrEqual(
      globalsBox.x + globalsBox.width + 1,
    );

    // Clicking the badge opens the menu; "Access settings" opens Settings → Access.
    await badge.click();
    await page.getByRole("menuitem", { name: "Access settings" }).click();

    await expect(
      page.getByText("Signed in through IAP as alice@example.com."),
    ).toBeVisible();
    const logoutBtn = page.getByRole("button", { name: "Log out" });
    await expect(logoutBtn).toBeVisible();

    // Clicking "Log out" posts /api/auth/logout and navigates to /_gcp_iap/clear_login_cookie.
    await logoutBtn.click();
    await expect.poll(() => clearedIAPCookie).toBe(true);
    expect(page.url()).toContain("/_gcp_iap/clear_login_cookie");
  });

  test("truncates long service-account emails without wrapping the header rail", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });

    await page.route("**/api/config", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          authenticated: true,
          authMethod: "iap",
          actor:
            "grain-daemon-runner@very-long-gcp-project-id.iam.gserviceaccount.com",
          actorKind: "service",
        },
      });
    });

    await page.goto("/");
    const badge = page.getByTestId("top-bar-user");
    await expect(badge).toBeVisible();

    const badgeBox = await badge.boundingBox();
    expect(badgeBox).not.toBeNull();
    expect(badgeBox.width).toBeLessThanOrEqual(201);
    expect(badgeBox.height).toBeLessThanOrEqual(30);
  });
});
