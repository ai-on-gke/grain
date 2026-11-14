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

// Settings -> Upgrade on a deployment that cannot upgrade itself
// (2026-11-13 deployment-operations walkthrough). The worker's demo is
// started with neither -upgrade-image nor -upgrade-src-dir, so
// GET /api/upgrade answers `enabled: false`: the pane used to read "dev",
// draw an enabled Start upgrade and 404 on GET /api/upgrade/targets.

import { test, expect } from "./fixtures.js";

test("Settings -> Upgrade says Not available, names the running build and reads no targets without an upgrader", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const targetReads = [];
  page.on("request", (req) => {
    if (new URL(req.url()).pathname === "/api/upgrade/targets") {
      targetReads.push(req.url());
    }
  });
  const config = await (await page.request.get("/api/config")).json();
  const commit = String(config.version?.commit ?? "").slice(0, 7);

  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("upgrade-unavailable")).toContainText(
    "Not available",
  );
  const version = card.getByTestId("current-daemon-version");
  await expect(version).not.toHaveText("dev");
  if (config.version?.unversioned && commit) {
    await expect(version).toHaveText(commit);
  }
  await expect(card.getByTestId("start-upgrade-btn")).toHaveCount(0);
  await expect(card.getByTestId("select-upgrade-target")).toHaveCount(0);
  await expect(card.getByTestId("upgrade-database")).toContainText(
    "This deployment runs on",
  );
  expect(targetReads).toEqual([]);

  // The notice sits inside the card, under its heading.
  const cardBox = await card.boundingBox();
  const noticeBox = await card.getByTestId("upgrade-unavailable").boundingBox();
  expect(noticeBox.x).toBeGreaterThanOrEqual(cardBox.x);
  expect(noticeBox.x + noticeBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 1,
  );
});

test("Settings -> Upgrade on an externally managed deployment links to its management console", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  // The demo's -disable-self-upgrade needs a server of its own; the
  // answer it gives (pkg/ui's upgrade status) is stubbed here instead.
  await page.route("**/api/upgrade", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          contentType: "application/json",
          body: JSON.stringify({
            enabled: false,
            running: { commit: "4ae6438fdcd4a0fc373fb3da76888724685b75fd" },
            selfUpgradeDisabled: true,
            managementUrl: "https://console.example.com/deployments/grain-prod",
            database: { driver: "sqlite" },
          }),
        })
      : route.continue(),
  );
  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("upgrade-externally-managed")).toContainText(
    "managed externally",
  );
  await expect(card.getByTestId("upgrade-management-link")).toHaveAttribute(
    "href",
    "https://console.example.com/deployments/grain-prod",
  );
  await expect(card.getByTestId("current-daemon-version")).toHaveText(
    "4ae6438",
  );
  await expect(card.getByTestId("upgrade-unavailable")).toHaveCount(0);
  await expect(card.getByTestId("start-upgrade-btn")).toHaveCount(0);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});
