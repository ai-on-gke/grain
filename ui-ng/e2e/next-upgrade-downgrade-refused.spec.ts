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

const STALE_LATEST_REASON =
  'refusing schema downgrade on channel "latest" (docker.io/grainai/grain:latest): target build v3.6.0 (commit cefb15f) is at schema 143, which is older than the running store at schema 146 on v3.7.0-dev.31+fdd8b8d (commit fdd8b8d); schema downgrades are never allowed through upgrade. Channel "latest"\'s image is older than what is running, so the channel\'s image is probably stale or unpublished';

test("Settings -> Upgrade greys out schema-downgrade targets, shows refusal reason, fits viewport, and passes axe @phone @dark", async ({
  page,
  axe,
}) => {
  await page.route("**/api/upgrade", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          contentType: "application/json",
          body: JSON.stringify({
            enabled: true,
            mode: "image",
            channel: "latest",
            branch: "latest",
            phase: "failed",
            detail: STALE_LATEST_REASON,
            running: {
              release: "v3.7.0-dev.31+fdd8b8d",
              commit: "fdd8b8d000000000000000000000000000000000",
            },
            database: { driver: "postgres" },
          }),
        })
      : route.continue(),
  );
  await page.route("**/api/upgrade/targets*", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        default: "latest",
        allowedBranches: [
          "main",
          "candidate",
          "latest",
          "v[0-9]*.[0-9]*.[0-9]*",
        ],
        targets: [
          {
            name: "latest",
            kind: "channel",
            allowed: false,
            schema: 143,
            reason: STALE_LATEST_REASON,
          },
          {
            name: "main",
            kind: "channel",
            allowed: true,
            schema: 146,
          },
          {
            name: "v3.6.0",
            kind: "version",
            allowed: false,
            schema: 143,
            reason:
              'refusing schema downgrade on tag "v3.6.0": target build v3.6.0 is at schema 143, older than schema 146',
          },
        ],
      }),
    }),
  );
  await page.route("**/api/upgrade/release-notes*", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ branch: "main", notes: [] }),
    }),
  );

  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card).toBeVisible();

  const failedBanner = card.getByTestId("failed-upgrade-banner");
  await expect(failedBanner).toContainText("probably stale or unpublished");

  const refusalBanner = card.getByTestId("upgrade-target-refusal");
  await expect(refusalBanner).toContainText("schema 143");

  const startBtn = page.getByRole("button", { name: "Start upgrade" });
  await expect(startBtn).toBeDisabled();

  // Layout check: banners sit inside the upgrade card without overflowing the viewport.
  const viewport = page.viewportSize()!;
  const cardBox = (await card.boundingBox())!;
  const refusalBox = (await refusalBanner.boundingBox())!;
  expect(refusalBox.x).toBeGreaterThanOrEqual(cardBox.x - 1);
  expect(refusalBox.x + refusalBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 1,
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(viewport.width);

  // Open target selector: downgrade targets are disabled and show their reason.
  const picker = page.getByRole("button", { name: /Upgrade target branch/ });
  await picker.click();
  const listbox = page.getByRole("listbox", { name: /Upgrade target branch/ });
  await expect(listbox).toBeVisible();

  const latestOption = listbox.getByRole("option", { name: /latest/ });
  await expect(latestOption).toBeDisabled();
  await expect(latestOption).toContainText("probably stale or unpublished");

  const v360Option = listbox.getByRole("option", { name: /^v3\.6\.0/ });
  await expect(v360Option).toBeDisabled();

  // Menu fits inside the screen horizontally even on phone width (390px).
  const menuBox = (await listbox.boundingBox())!;
  expect(menuBox.x).toBeGreaterThanOrEqual(0);
  expect(menuBox.x + menuBox.width).toBeLessThanOrEqual(viewport.width + 1);

  expect(await axe()).toEqual([]);

  // Choosing an allowed target clears the refusal and enables Start upgrade.
  await listbox.getByRole("option", { name: /^main/ }).click();
  await expect(card.getByTestId("upgrade-target-refusal")).toHaveCount(0);
  await expect(startBtn).toBeEnabled();
});
