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

  // On a phone the button sits inside its row, which stays inside the
  // popover rather than pushing it wider than the screen.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/inbox");
  await page.getByTestId("alerts-button").first().click();
  const phoneRow = page
    .getByTestId("alerts-panel")
    .getByTestId("alert-row")
    .filter({ hasText: "Reconcile loop stopped" });
  const button = await phoneRow
    .getByRole("button", { name: "Power" })
    .boundingBox();
  const rowBox = await phoneRow.boundingBox();
  expect(button.x).toBeGreaterThanOrEqual(rowBox.x);
  expect(button.x + button.width).toBeLessThanOrEqual(
    rowBox.x + rowBox.width + 0.5,
  );
  expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(390);
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

test("pending-restart and not-runnable settings on System → Health link to their Settings section and field", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    return route.fulfill({
      response: res,
      json: {
        ...(await res.json()),
        pendingRestart: ["pollInterval", "githubHost"],
        unrunnable: [
          { key: "pollInterval", stored: "0s", running: "30s" },
          { key: "sandboxBackend", stored: "bogus", running: "docker" },
        ],
      },
    });
  });

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/system");

  const pendingRow = page.getByTestId("health-pending-restart");
  await expect(pendingRow).toContainText(
    "Saved, not applied until restart: Poll interval, GitHub host.",
  );
  const pollPending = pendingRow.getByRole("link", { name: "Poll interval" });
  const githubPending = pendingRow.getByRole("link", { name: "GitHub host" });
  const powerBtn = pendingRow.getByRole("button", { name: "Power" });
  await expect(pollPending).toHaveAttribute(
    "href",
    "/settings/engine#pollInterval",
  );
  await expect(githubPending).toHaveAttribute(
    "href",
    "/settings/credentials#githubHost",
  );

  // Power stays in the right-hand action slot of the pending-restart alert.
  const pendingBox = await pendingRow.boundingBox();
  const githubBox = await githubPending.boundingBox();
  const powerBox = await powerBtn.boundingBox();
  expect(powerBox.x).toBeGreaterThan(githubBox.x + githubBox.width);
  expect(powerBox.x + powerBox.width).toBeLessThanOrEqual(
    pendingBox.x + pendingBox.width + 0.5,
  );

  await pollPending.click();
  await expect(page).toHaveURL(/\/settings\/engine#pollInterval$/);
  await expect(page.getByTestId("settings-section")).toHaveAttribute(
    "data-section",
    "engine",
  );
  await expect(
    page.locator('[data-setting-id="pollInterval"]'),
  ).toBeInViewport();

  await page.goBack();
  await expect(page).toHaveURL(/\/system$/);

  const unrunnableRow = page.getByTestId("health-unrunnable");
  await expect(unrunnableRow).toContainText(
    "Stored, but not runnable: Poll interval (stored 0s, running 30s), Sandbox backend (stored bogus, running docker).",
  );
  const pollUnrunnable = unrunnableRow.getByRole("link", {
    name: "Poll interval",
  });
  const backendUnrunnable = unrunnableRow.getByRole("link", {
    name: "Sandbox backend",
  });
  await expect(pollUnrunnable).toHaveAttribute(
    "href",
    "/settings/engine#pollInterval",
  );
  await expect(backendUnrunnable).toHaveAttribute(
    "href",
    "/settings/sandboxes#sandboxBackend",
  );

  await backendUnrunnable.click();
  await expect(page).toHaveURL(/\/settings\/sandboxes#sandboxBackend$/);
  await expect(page.getByTestId("settings-section")).toHaveAttribute(
    "data-section",
    "sandboxes",
  );
  await expect(
    page.locator('[data-setting-id="sandboxBackend"]'),
  ).toBeInViewport();

  await page.goBack();
  await expect(page).toHaveURL(/\/system$/);
  await pendingRow.getByRole("button", { name: "Power" }).click();
  await expect(page).toHaveURL(/\/system\/power$/);

  // At phone width (390×844), both rows and their links/actions stay inside
  // the viewport without horizontal overflow, and each link opens its card.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/system/health");
  const phonePending = page.getByTestId("health-pending-restart");
  const phoneUnrunnable = page.getByTestId("health-unrunnable");
  await expect(phonePending).toBeVisible();
  await expect(phoneUnrunnable).toBeVisible();

  const phonePendingBox = await phonePending.boundingBox();
  const phonePowerBox = await phonePending
    .getByRole("button", { name: "Power" })
    .boundingBox();
  const phoneUnrunnableBox = await phoneUnrunnable.boundingBox();
  expect(phonePendingBox.x + phonePendingBox.width).toBeLessThanOrEqual(390);
  expect(phonePowerBox.x + phonePowerBox.width).toBeLessThanOrEqual(
    phonePendingBox.x + phonePendingBox.width + 0.5,
  );
  expect(phoneUnrunnableBox.x + phoneUnrunnableBox.width).toBeLessThanOrEqual(
    390,
  );

  await phonePending.getByRole("link", { name: "GitHub host" }).click();
  await expect(page).toHaveURL(/\/settings\/credentials#githubHost$/);
  await expect(page.locator('[data-setting-id="githubHost"]')).toBeInViewport();
});
