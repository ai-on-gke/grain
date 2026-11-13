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

// Fleet → Health and the Host Power card: the classic System → Health
// and System → Power tabs, under /next/ (task 5525).
//
// Nothing here ever reaches the host: every test routes POST
// /api/host/reboot and /api/host/shutdown to a stub before the page
// loads, and the demo itself starts with reboot and shutdown off. Where a
// test needs them on, it rewrites /api/config's answer in the browser.

import { test, expect } from "./fixtures.js";

// stubPower answers the reboot and shutdown POSTs in the browser, never
// letting one through to the daemon, and records each it saw.
async function stubPower(page, answer) {
  const posts = [];
  for (const path of ["reboot", "shutdown"]) {
    await page.route(`**/api/host/${path}`, (route) => {
      posts.push(`${route.request().method()} ${path}`);
      return answer(path, route);
    });
  }
  return posts;
}

async function enablePower(page, flags) {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({ response: res, json: { ...body, ...flags } });
  });
}

test("classic System → Health and → Power land on their /next/ view", async ({
  page,
}) => {
  await stubPower(page, (_p, route) => route.abort());
  await page.goto("system/health");
  await expect(page).toHaveURL(/\/next\/fleet\?tab=health$/);
  await expect(page.locator("#fleet-panel-health")).toBeVisible();
  await expect(page.getByTestId("fleet-tab-health")).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(
    page.getByRole("heading", { name: "Alerts", exact: true }),
  ).toBeVisible();

  await page.goto("system/power");
  await expect(page).toHaveURL(/\/next\/fleet\?tab=controls#fleet-power$/);
  const card = page.getByTestId("fleet-power-card");
  await expect(card).toBeVisible();
  await expect(card).toBeInViewport();
  await expect(card.getByRole("heading", { name: "Host Power" })).toBeVisible();
});

test("Health shows the settings, budget and alerts the classic tab shows @phone @dark", async ({
  page,
  axe,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        pendingRestart: ["maxWorkers"],
        unrunnable: [{ key: "pollInterval", stored: "1ms", running: "30s" }],
      },
    });
  });
  await page.route("**/api/metrics?section=github", (route) =>
    route.fulfill({
      json: {
        github: {
          enabled: true,
          budget: { limit: 5000, remaining: 4200 },
          polls: {
            tracked: 4,
            backedOff: 1,
            medianSeconds: 120,
            maxSeconds: 900,
          },
          callers: [
            {
              caller: "merge-queue",
              spent: 40,
              revalidated: 12,
              kinds: [{ kind: "pulls", spent: 30 }],
            },
          ],
        },
      },
    }),
  );
  await enablePower(page, {
    pipelinePause: { paused: true, reason: "maintenance window" },
  });
  await page.goto("fleet?tab=health");

  const pending = page.getByTestId("health-pending-restart");
  await expect(pending).toContainText("Saved, not applied until restart");
  await expect(pending.getByRole("link", { name: "Power" })).toHaveAttribute(
    "href",
    /\/next\/fleet\?tab=controls#fleet-power$/,
  );
  const field = pending.getByRole("link", { name: "Max worker agents" });
  await expect(field).toHaveAttribute(
    "href",
    /\/next\/settings\/engine#maxWorkers$/,
  );
  await expect(page.getByTestId("health-unrunnable")).toContainText(
    "(stored 1ms, running 30s)",
  );

  await expect(page.getByTestId("health-github-budget")).toContainText(
    "4,200 of 5,000 calls left this hour",
  );
  await expect(page.getByTestId("health-github-polls")).toContainText(
    "1 of 4 tracked PRs backed off",
  );

  const alert = page
    .getByTestId("health-alert")
    .filter({ hasText: "Deployment paused" });
  await expect(alert).toContainText("maintenance window");
  await alert.getByRole("link", { name: "Pauses & Controls" }).click();
  await expect(page).toHaveURL(/\/next\/fleet\?tab=controls$/);
  await page.goBack();
  await expect(page.locator("#fleet-panel-health")).toBeVisible();

  await field.click();
  await expect(page).toHaveURL(/\/next\/settings\/engine#maxWorkers$/);
  await page.goBack();
  await expect(page.locator("#fleet-panel-health")).toBeVisible();
  const sideways = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(sideways).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);
});

test("Reboot host and Shut down host are off where /api/config says so", async ({
  page,
}) => {
  const posts = await stubPower(page, (_p, route) => route.abort());
  // The demo starts with rebootEnabled and shutdownEnabled false.
  await page.goto("fleet?tab=controls");
  await expect(page.getByTestId("power-reboot-btn")).toBeDisabled();
  await expect(page.getByTestId("power-shutdown-btn")).toBeDisabled();
  await expect(page.getByTestId("power-reboot-off")).toContainText(
    "Rebooting the host is not enabled for this deployment.",
  );
  await expect(page.getByTestId("power-shutdown-off")).toContainText(
    "Shutting down the host is not enabled for this deployment.",
  );
  expect(posts).toEqual([]);
});

test("each power action asks first, and a refused POST is a failure", async ({
  page,
}) => {
  const posts = await stubPower(page, (path, route) =>
    path === "reboot"
      ? route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "sudo: a password is required" }),
        })
      : route.fulfill({
          status: 403,
          contentType: "application/json",
          body: JSON.stringify({ error: "shutdown is not allowed here" }),
        }),
  );
  await enablePower(page, { rebootEnabled: true, shutdownEnabled: true });
  await page.goto("fleet?tab=controls");
  const dialog = page.getByTestId("confirm-dialog");

  const reboot = page.getByTestId("power-reboot-btn");
  await expect(reboot).toBeEnabled();
  await reboot.click();
  await expect(dialog).toContainText("Reboot the host machine?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  expect(posts).toEqual([]);

  await reboot.click();
  await dialog.getByRole("button", { name: "Reboot host" }).click();
  await expect(
    page.getByText("Reboot failed: sudo: a password is required"),
  ).toBeVisible();
  await expect(page.getByTestId("fleet-power-requested")).toHaveCount(0);

  const shutdown = page.getByTestId("power-shutdown-btn");
  await shutdown.click();
  await expect(dialog).toContainText("Shut down the host machine?");
  await dialog.getByRole("button", { name: "Shut down host" }).click();
  await expect(
    page.getByText("Shutdown failed: shutdown is not allowed here"),
  ).toBeVisible();
  await expect(page.getByTestId("fleet-power-requested")).toHaveCount(0);
  expect(posts).toEqual(["POST reboot", "POST shutdown"]);
});

test("a dropped connection is the host going down", async ({ page }) => {
  const posts = await stubPower(page, (_p, route) =>
    route.abort("connectionreset"),
  );
  await enablePower(page, { rebootEnabled: true, shutdownEnabled: true });
  await page.goto("fleet?tab=controls");
  await page.getByTestId("power-reboot-btn").click();
  await page
    .getByTestId("confirm-dialog")
    .getByRole("button", { name: "Reboot host" })
    .click();
  await expect(page.getByText("Reboot requested.")).toBeVisible();
  await expect(page.getByTestId("fleet-power-requested")).toContainText(
    "Rebooting",
  );
  expect(posts).toEqual(["POST reboot"]);
});
