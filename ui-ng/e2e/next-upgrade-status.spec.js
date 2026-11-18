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

// What the 2026-11-18 scheduled-upgrade walkthrough found on
// /next/settings/upgrade (docs/test-runs/2026-11-18-upgrade-scheduled-cancel.md):
// a finished upgrade (phase "ok") read "Upgrade in progress: ok" (F9), and
// a schedule whose runs could not be counted read like any other (F8).

const countError =
  "the runs in flight could not be counted, so the queue stays held and the upgrade waits until they can be; the daemon log has the error";

async function stubUpgrade(page) {
  await page.route("**/api/upgrade", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        enabled: true,
        mode: "image",
        branch: "v2",
        phase: "ok",
        cancellable: false,
        scheduled: {
          branch: "v2",
          requestedAt: "2026-11-18T06:00:00Z",
          byKind: "human",
          byId: "alice@example.com",
          busyRuns: 0,
          busyCiRuns: 0,
          countError,
        },
      },
    });
  });
}

test("a finished upgrade is not in progress, and an uncountable schedule says why with its action on the right", async ({
  page,
}) => {
  await stubUpgrade(page);
  await page.goto("settings/upgrade");
  const banner = page.getByTestId("scheduled-upgrade-banner");
  await expect(banner).toBeVisible();
  await expect(page.getByTestId("scheduled-upgrade-count-error")).toHaveText(
    countError,
  );
  await expect(page.getByTestId("active-upgrade-banner")).toHaveCount(0);

  // The long message must not push the action off the right-hand slot.
  const b = await banner.boundingBox();
  const btn = await page
    .getByTestId("cancel-scheduled-upgrade-btn")
    .boundingBox();
  expect(b && btn).toBeTruthy();
  expect(Math.abs(b.x + b.width - (btn.x + btn.width))).toBeLessThanOrEqual(24);
  expect(btn.y).toBeLessThan(b.y + b.height / 2);
});

test("an uncountable schedule's action stays inside the banner on a phone @phone", async ({
  page,
}) => {
  await stubUpgrade(page);
  await page.goto("settings/upgrade");
  const banner = page.getByTestId("scheduled-upgrade-banner");
  await expect(page.getByTestId("scheduled-upgrade-count-error")).toBeVisible();
  const b = await banner.boundingBox();
  const btn = await page
    .getByTestId("cancel-scheduled-upgrade-btn")
    .boundingBox();
  const vw = page.viewportSize().width;
  expect(btn.x).toBeGreaterThanOrEqual(b.x);
  expect(btn.x + btn.width).toBeLessThanOrEqual(Math.min(vw, b.x + b.width));
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    ),
  ).toBeLessThanOrEqual(0);
});
