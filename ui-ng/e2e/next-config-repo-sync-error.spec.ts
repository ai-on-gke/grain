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

// Regression test for §11 Finding 1: /next/settings/config-repo must display
// persisted background StateRepo sync errors (e.g. a newer-schema refusal from
// SyncExternal) and schema-version mismatches on load, refresh cleanly on
// "Sync now" without duplicating the error in two banners, stay within card
// bounds on desktop and phone, and pass axe in light and dark themes.
const REFUSAL =
  "state repository holds changes grain could not apply: state repository was written by a newer build of grain: repository is at schema 152, this build knows 151";

test("@phone @dark config-repo shows newer-schema sync error on load and clears on recovery", async ({
  page,
  axe,
}) => {
  let failedState = true;

  await page.route("**/api/state-repo", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    if (failedState) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          available: true,
          mode: "external",
          remote: "https://github.com/acme/grain-config.git",
          branch: "main",
          head: "e1042474abcdef1234567890",
          schemaVersion: 152,
          buildSchemaVersion: 151,
          error: REFUSAL,
        }),
      });
    } else {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          available: true,
          mode: "external",
          remote: "https://github.com/acme/grain-config.git",
          branch: "main",
          head: "a1b2c3d4abcdef1234567890",
          schemaVersion: 151,
          buildSchemaVersion: 151,
          error: "",
        }),
      });
    }
  });

  await page.route("**/api/state-repo/sync", async (route) => {
    if (failedState) {
      await route.fulfill({
        status: 502,
        contentType: "application/json",
        body: JSON.stringify({ error: REFUSAL }),
      });
    } else {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          available: true,
          mode: "external",
          remote: "https://github.com/acme/grain-config.git",
          branch: "main",
          head: "a1b2c3d4abcdef1234567890",
          schemaVersion: 151,
          buildSchemaVersion: 151,
          error: "",
        }),
      });
    }
  });

  await page.goto("/next/settings/config-repo");

  const card = page.locator('[data-testid="card-state-repo"]');
  const errBanner = card.getByRole("alert");
  await expect(errBanner).toBeVisible();
  await expect(errBanner).toContainText(REFUSAL);
  await expect(
    card.locator('[data-testid="state-repo-schema-mismatch"]'),
  ).toContainText("schema 152 (build 151)");

  // Error banner stays inside its card and causes no horizontal overflow.
  const cardBox = await card.boundingBox();
  const bannerBox = await errBanner.boundingBox();
  expect(cardBox).not.toBeNull();
  expect(bannerBox).not.toBeNull();
  expect(bannerBox!.x).toBeGreaterThanOrEqual(cardBox!.x - 1);
  expect(bannerBox!.x + bannerBox!.width).toBeLessThanOrEqual(
    cardBox!.x + cardBox!.width + 1,
  );
  const overflowX = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflowX).toBeLessThanOrEqual(1);

  expect(await axe()).toEqual([]);

  // Clicking Sync now while the refusal persists keeps a single alert (no
  // duplicate page-level error banner).
  await card.getByRole("button", { name: "Sync now" }).click();
  await expect(errBanner).toBeVisible();
  await expect(errBanner).toContainText(REFUSAL);
  await expect(page.locator('[data-testid="settings-error"]')).toHaveCount(0);

  // Once the remote is compatible again, Sync now clears the error banner and
  // schema mismatch pill.
  failedState = false;
  await card.getByRole("button", { name: "Sync now" }).click();
  await expect(card.locator('[data-testid="state-repo-error"]')).toHaveCount(0);
  await expect(
    card.locator('[data-testid="state-repo-schema-mismatch"]'),
  ).toHaveCount(0);
  await expect(card.locator('[data-testid="state-repo-status"]')).toContainText(
    "main @ a1b2c3d4",
  );
});
