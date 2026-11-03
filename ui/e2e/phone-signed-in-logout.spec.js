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

// A phone has no header rail, so the desktop email badge
// (iap-header-logout.spec.js) has nowhere to be. Its stand-in is a row at
// the foot of the navigation drawer (design/design/settings-panel.design.md
// §4.3): who IAP verified, opening Settings -> Access, and Log out.
const LONG_EMAIL =
  "grain-daemon-runner@very-long-gcp-project-id.iam.gserviceaccount.com";

async function signInAs(page, actor) {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        authenticated: true,
        authMethod: "iap",
        actor,
        actorKind: "human",
      },
    });
  });
}

test.describe("phone drawer signed-in row", () => {
  test("shows a long verified email ellipsized inside the drawer and logs out to /_gcp_iap/clear_login_cookie", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signInAs(page, LONG_EMAIL);
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
    await page.getByRole("button", { name: "Open navigation" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    await expect(drawer).toBeVisible();

    const row = drawer.getByTestId("phone-signed-in");
    await expect(row).toBeVisible();
    await expect(row).toContainText("Signed in as");
    const email = row.getByTestId("phone-signed-in-email");
    await expect(email).toHaveText(LONG_EMAIL);

    // Inside the drawer, below System, and not wider than it -- measured
    // once the drawer has finished sliding in, so every box below is
    // taken against the same position.
    await expect.poll(async () => (await drawer.boundingBox())?.x).toBe(0);
    const drawerBox = await drawer.boundingBox();
    const rowBox = await row.boundingBox();
    const systemBox = await drawer
      .getByRole("navigation", { name: "System" })
      .getByRole("button", { name: "System" })
      .boundingBox();
    expect(rowBox.x).toBeGreaterThanOrEqual(drawerBox.x - 1);
    expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(
      drawerBox.x + drawerBox.width + 1,
    );
    expect(rowBox.y + rowBox.height).toBeLessThanOrEqual(
      drawerBox.y + drawerBox.height + 1,
    );
    expect(rowBox.y).toBeGreaterThanOrEqual(systemBox.y + systemBox.height - 1);

    // The address is cut with an ellipsis, not wrapped or overflowing.
    const style = await email.evaluate((el) => {
      const cs = getComputedStyle(el);
      return {
        textOverflow: cs.textOverflow,
        whiteSpace: cs.whiteSpace,
        clipped: el.scrollWidth > el.clientWidth,
      };
    });
    expect(style.textOverflow).toBe("ellipsis");
    expect(style.whiteSpace).toBe("nowrap");
    expect(style.clipped).toBe(true);
    const emailBox = await email.boundingBox();
    expect(emailBox.x + emailBox.width).toBeLessThanOrEqual(
      rowBox.x + rowBox.width + 1,
    );
    const logout = row.getByRole("button", { name: "Log out" });
    const logoutBox = await logout.boundingBox();
    // Log out is under the address, so the address has the row's width.
    expect(logoutBox.y).toBeGreaterThanOrEqual(
      emailBox.y + emailBox.height - 1,
    );
    expect(logoutBox.y + logoutBox.height).toBeLessThanOrEqual(
      rowBox.y + rowBox.height + 1,
    );
    expect(logoutBox.x + logoutBox.width).toBeLessThanOrEqual(
      drawerBox.x + drawerBox.width + 1,
    );
    // Nothing on the page scrolls sideways.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    const drawerOverflow = await drawer.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(drawerOverflow).toBeLessThanOrEqual(0);

    await logout.click();
    await expect.poll(() => clearedIAPCookie).toBe(true);
    expect(page.url()).toContain("/_gcp_iap/clear_login_cookie");
  });

  // The 2026-11-03 IAP deployment walkthrough: with Log out beside it,
  // the address had ~110px of the 232px drawer, and owner@example.com
  // was drawn "owner@example...". An everyday address is shown whole, in
  // light and dark.
  for (const scheme of ["light", "dark"]) {
    test(`shows an everyday address whole at 390px (${scheme})`, async ({
      page,
    }) => {
      await page.emulateMedia({ colorScheme: scheme });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        scheme,
      );
      await page.setViewportSize({ width: 390, height: 844 });
      const everyday = "alice.smith@example.com";
      await signInAs(page, everyday);

      await page.goto("/");
      await page.getByRole("button", { name: "Open navigation" }).click();
      const drawer = page.getByRole("dialog", { name: "Navigation" });
      await expect.poll(async () => (await drawer.boundingBox())?.x).toBe(0);
      const row = drawer.getByTestId("phone-signed-in");
      const email = row.getByTestId("phone-signed-in-email");
      await expect(email).toHaveText(everyday);
      const fit = await email.evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(fit).toBeLessThanOrEqual(0);
      const logout = row.getByRole("button", { name: "Log out" });
      await expect(logout).toBeVisible();
      const drawerBox = await drawer.boundingBox();
      const logoutBox = await logout.boundingBox();
      expect(logoutBox.x + logoutBox.width).toBeLessThanOrEqual(
        drawerBox.x + drawerBox.width + 1,
      );
    });
  }

  test("opens Settings -> Access from the row", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signInAs(page, "alice@example.com");

    await page.goto("/");
    await page.getByRole("button", { name: "Open navigation" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    await drawer
      .getByRole("button", {
        name: "Signed in as alice@example.com. Access settings",
      })
      .click();
    await expect(drawer).toBeHidden();
    await expect(
      page.getByText("Signed in through IAP as alice@example.com."),
    ).toBeVisible();
  });

  test("is absent without a verified identity", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/");
    await page.getByRole("button", { name: "Open navigation" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    await expect(
      drawer.getByRole("button", { name: "System" }).first(),
    ).toBeVisible();
    await expect(drawer.getByTestId("phone-signed-in")).toHaveCount(0);
  });
});
