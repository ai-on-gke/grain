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

// Regressions from the 2026-11-11 user and system settings walkthrough
// (docs/test-runs/2026-11-11-user-system-settings.md), driven through the
// real `grain demo` server and built frontend.

test.describe("settings walkthrough 2026-11-11 regressions", () => {
  // Picking a select from Filter settings focused the select's hidden
  // native <input> (aria-hidden, tabindex -1), so nothing on screen was
  // focused and Tab started from the top of the page. It focuses the
  // combobox a keyboard reaches; a setting hidden behind an off mode
  // (Automatic upgrade time of day) focuses its card's first one.
  test("a filtered select is focused on its visible combobox", async ({
    page,
  }) => {
    for (const query of ["check the upgrade", "upgrade time of day"]) {
      await page.goto("/settings/models");
      const filter = page.getByTestId("settings-filter").locator("input");
      await filter.fill(query);
      await page.getByTestId("settings-result").first().click();
      await expect(page).toHaveURL(/\/settings\/general#/);
      const focused = page.locator(":focus");
      await expect(focused).toHaveAttribute("role", "combobox");
      await expect(focused).not.toHaveAttribute("aria-hidden", "true");
      await expect(focused).toBeVisible();
    }
  });

  // Signing in from a deep link with a #card came back to the section
  // but not the card: the fragment never reaches the server, so the
  // form's return path could not carry it.
  test("signing in returns to the deep link's card, a wrong code included", async ({
    browser,
    baseURL,
    accessCode,
  }) => {
    const context = await browser.newContext({
      baseURL,
      storageState: { cookies: [], origins: [] },
      viewport: { width: 1280, height: 600 },
    });
    try {
      const page = await context.newPage();
      await page.goto("/settings/agents#external-agents");
      const signIn = async (code) => {
        await page.getByLabel("Your email").fill("alice@example.com");
        await page.getByLabel("Access code").fill(code);
        await page.getByRole("button", { name: "Continue" }).click();
      };
      await signIn("AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH");
      await expect(page.getByRole("alert")).toHaveText(
        "That access code was not accepted.",
      );
      await signIn(accessCode);
      await expect(page).toHaveURL(/\/settings\/agents#external-agents$/);
      await expect(page.locator("#external-agents")).toBeInViewport();
    } finally {
      await context.close();
    }
  });
});
