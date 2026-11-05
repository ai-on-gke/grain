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

test.describe("IAP signed-in sidebar footer and logout", () => {
  test("omits any header user pill, shows the verified email in the sidebar footer, opens Access settings, and logs out to /_gcp_iap/clear_login_cookie", async ({
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
      await route.fulfill({
        status: 200,
        contentType: "text/plain",
        body: "logged out of IAP",
      });
      clearedIAPCookie = true;
    });

    await page.goto("/");
    const globals = page.getByTestId("page-header-globals");
    await expect(globals.getByTestId("top-bar-settings")).toBeVisible();
    await expect(page.getByTestId("top-bar-user")).toHaveCount(0);

    const aside = page.locator("aside");
    const tile = aside.getByTestId("user-tile");
    const sidebarLogout = aside.getByTestId("sidebar-logout");
    await expect(tile).toBeVisible();
    await expect(tile.getByTestId("user-tile-label")).toHaveText(
      "alice@example.com",
    );
    await expect(sidebarLogout).toBeVisible();

    // Clicking the sidebar user tile opens Settings → Access.
    await tile.click();
    await expect(
      page.getByText("Signed in through IAP as alice@example.com."),
    ).toBeVisible();

    // Clicking the sidebar's Log out button posts /api/auth/logout and
    // navigates to /_gcp_iap/clear_login_cookie.
    await sidebarLogout.click();
    await expect.poll(() => clearedIAPCookie).toBe(true);
    await expect(page).toHaveURL(/\/_gcp_iap\/clear_login_cookie/);
  });

  test("truncates long service-account emails in the sidebar footer without overflowing the rail", async ({
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
    await expect(page.getByTestId("top-bar-user")).toHaveCount(0);

    const aside = page.locator("aside");
    const tile = aside.getByTestId("user-tile");
    const logout = aside.getByTestId("sidebar-logout");
    await expect(tile).toBeVisible();
    await expect(logout).toBeVisible();

    const [railBox, tileBox, logoutBox] = await Promise.all([
      aside.boundingBox(),
      tile.boundingBox(),
      logout.boundingBox(),
    ]);
    expect(railBox).not.toBeNull();
    expect(tileBox).not.toBeNull();
    expect(logoutBox).not.toBeNull();
    expect(tileBox.x).toBeGreaterThanOrEqual(railBox.x - 1);
    expect(logoutBox.x + logoutBox.width).toBeLessThanOrEqual(
      railBox.x + railBox.width + 1,
    );

    const lines = await tile.getByTestId("user-tile-label").evaluate((el) => {
      const lh = parseFloat(getComputedStyle(el).lineHeight) || 16;
      return Math.round(el.getBoundingClientRect().height / lh);
    });
    expect(lines).toBe(1);
  });
});
