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

// Regressions found by the 2026-10-30 deployment-operations walkthrough
// (docs/test-runs/2026-10-30-deployment-operations.md), driven through
// the real demo server and built frontend:
//
//   - "Reconcile loop stopped" told the reader to restart the daemon and
//     offered nothing to do it with, though System → Power's Restart
//     grain is exactly that;
//   - System → Caches had two buttons both named "Flush the whole
//     cache", one emptying the proxy cache and one the Actions cache.
test.use({ demoArgs: ["-observability"] });

test("a stopped reconcile loop's alert opens System → Power, from the bell and from Health", async ({
  page,
}) => {
  // Only a daemon whose reconcile loop has died sets reconcilerDown;
  // the demo has no loop to stop, so the flag is added to its own
  // /api/config answer.
  await page.route("**/api/config*", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    return route.fulfill({
      response: res,
      json: { ...(await res.json()), reconcilerDown: true },
    });
  });
  await page.goto("/inbox");
  await page.getByTestId("alerts-button").first().click();
  const panel = page.getByTestId("alerts-panel");
  const row = panel
    .getByTestId("alert-row")
    .filter({ hasText: "Reconcile loop stopped" });
  await expect(row).toContainText("Restart the daemon to recover");
  await row.getByRole("button", { name: "Power" }).click();
  await expect(panel).toBeHidden();
  await expect(page).toHaveURL(/\/system\/power$/);
  await expect(
    page.getByRole("button", { name: "Restart grain" }),
  ).toBeVisible();

  // System → Health lists the same alert with the same way out.
  await page.goto("/system");
  const healthRow = page
    .getByTestId("system-health")
    .getByTestId("alert-row")
    .filter({ hasText: "Reconcile loop stopped" });
  await healthRow.getByRole("button", { name: "Power" }).click();
  await expect(page).toHaveURL(/\/system\/power$/);
});

test("the two whole-cache flush buttons on System → Caches are named apart", async ({
  page,
}) => {
  await page.goto("/system/caches");
  const proxy = page.getByRole("button", {
    name: "Flush the whole proxy cache",
  });
  const actions = page.getByRole("button", {
    name: "Flush the whole Actions cache",
  });
  await expect(proxy).toHaveText(/Flush the whole cache/i);
  await expect(actions).toHaveText(/Flush the whole cache/i);
  // Nothing left answering to the old shared name.
  await expect(
    page.getByRole("button", { name: "Flush the whole cache", exact: true }),
  ).toHaveCount(0);
  // Each sits in its own section: the proxy cache's above the Actions
  // cache's.
  const proxyBox = await proxy.boundingBox();
  const actionsBox = await actions.boundingBox();
  expect(proxyBox.y).toBeLessThan(actionsBox.y);
});
