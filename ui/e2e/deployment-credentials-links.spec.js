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

// The retired Settings → Legacy capabilities tab is the Deployment
// credentials card at the foot of Settings → Capabilities (task 2876), and
// the links that named it still have to reach it. In the 2026-11-08
// connections walkthrough (docs/test-runs/2026-11-08-connections-
// infrastructure-settings-task-3616.md) /settings/credentials, its
// aliases and Settings → Git's read-only "Settings → Capabilities" note
// all opened Capabilities at its top, with the card some 5,900px -- six
// and a half screens -- below the fold.

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];

for (const [label, viewport] of VIEWPORTS) {
  test(`the retired Legacy capabilities addresses open their card (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    for (const [alias, anchor] of [
      ["/settings/credentials", "credentials"],
      ["/settings/legacy-capabilities", "credentials"],
      ["/settings/secrets", "other-secrets"],
    ]) {
      await page.goto(alias);
      await expect(
        page.getByRole("heading", { name: "Capabilities", level: 1 }),
      ).toBeVisible();
      // The server list draws a moment after the card: the card has to
      // stay in view once it has.
      await expect(
        page.locator("[data-capability-server-row]").first(),
      ).toBeAttached();
      await expect(page.locator(`#${anchor}`)).toBeInViewport();
      // The corrected address names the card, so a reload or a shared
      // link opens it too.
      await expect(page).toHaveURL(`/settings/capabilities#${anchor}`);
    }

    // A #hash of its own still wins over the alias's card.
    await page.goto("/settings/credentials#capability-policy");
    await expect(
      page.locator("[data-capability-server-row]").first(),
    ).toBeAttached();
    await expect(page.locator("#capability-policy")).toBeInViewport();
    await expect(page).toHaveURL("/settings/capabilities#capability-policy");
  });
}

// GET /api/settings and /api/config are stubbed to say the settings are
// held in the config repo (configRepoOnly), which a standalone demo never
// is: that is what draws Settings → Git read-only, with its note.
test("Settings → Git's read-only note opens the Deployment credentials card", async ({
  page,
}) => {
  for (const glob of ["**/api/settings", "**/api/config"]) {
    await page.route(glob, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const body = await response.json();
      await route.fulfill({
        response,
        json: { ...body, configRepoOnly: true },
      });
    });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/settings/git");
  const note = page.getByTestId("git-secret-note");
  const link = note.getByRole("link", { name: "Settings → Capabilities" });
  await expect(link).toHaveAttribute(
    "href",
    "/settings/capabilities#credentials",
  );
  await link.click();
  await expect(
    page.getByRole("heading", { name: "Capabilities", level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL("/settings/capabilities#credentials");
  await expect(
    page.locator("[data-capability-server-row]").first(),
  ).toBeAttached();
  await expect(page.locator("#credentials")).toBeInViewport();

  // Back returns to Git, as any section change does.
  await page.goBack();
  await expect(page).toHaveURL("/settings/git");
  await expect(page.getByTestId("git-secret-note")).toBeVisible();
});
