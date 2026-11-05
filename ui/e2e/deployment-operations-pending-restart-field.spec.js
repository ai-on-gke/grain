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

// Regression found by the 2026-11-01 deployment-operations walkthrough
// (docs/test-runs/2026-11-01-deployment-operations.md), driven through
// the real demo server and built frontend: System → Health's "Saved, not
// applied until restart" row linked each setting to the top of its
// Settings card (settingLocation's card anchor). GitHub host sits near
// the foot of Legacy capabilities, a screen and more below that card's
// top, so following its link opened Settings with the field it named out
// of sight and unfocused.
test("Health's pending-restart link lands on the setting's own field", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  // The demo has no running daemon config to differ from what is
  // stored, so pendingRestart is always empty there; it is added to the
  // demo's own GET /api/settings answer.
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    return route.fulfill({
      response: res,
      json: { ...(await res.json()), pendingRestart: ["githubHost"] },
    });
  });
  await page.goto("/system");
  const row = page.getByTestId("health-pending-restart");
  await expect(row).toContainText(
    "Saved, not applied until restart: GitHub host.",
  );
  const link = row.getByRole("link", { name: "GitHub host" });
  await expect(link).toHaveAttribute(
    "href",
    "/settings/credentials#githubHost",
  );
  await link.click();

  await expect(page).toHaveURL(/\/settings\/credentials#githubHost$/);
  const field = page.locator('[data-setting-id="githubHost"]');
  await expect(field).toBeInViewport();
  await expect(
    field.getByRole("textbox", { name: "GitHub host" }),
  ).toBeFocused();

  // The same address, opened cold, lands in the same place.
  await page.goto("/settings/credentials#githubHost");
  await expect(page.locator('[data-setting-id="githubHost"]')).toBeInViewport();
});
