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

// A stale task list on a phone (2026-11-07 walkthrough, finding F7,
// docs/test-runs/2026-11-07-ui-search-scroll-resilience.md): on a
// mid-session /api/tasks outage the rail's Tasks entries say "not
// refreshing", but at 390 px the rail is in the drawer, so nothing on
// screen said the list had stopped being refreshed. PhoneNav's top bar
// now carries a slim "Task list not refreshing" row under its controls
// while the poll fails and the last list is held; this checks where it
// sits, in light and dark, and that the next poll that answers takes it
// away.
//
// SHOTS_DIR, when set, is where the bar is shot during the outage, for a
// reviewer to look at.

import fs from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const LIVE_POLL = /\/api\/tasks(\?(?!.*state=closed).*)?$/;

function inside(inner, outer, slack = 0.5) {
  return (
    inner.x >= outer.x - slack &&
    inner.y >= outer.y - slack &&
    inner.x + inner.width <= outer.x + outer.width + slack &&
    inner.y + inner.height <= outer.y + outer.height + slack
  );
}

function overlaps(a, b) {
  return (
    a.x < b.x + b.width &&
    b.x < a.x + a.width &&
    a.y < b.y + b.height &&
    b.y < a.y + a.height
  );
}

// barBoxes is where the phone bar and its controls are.
async function barBoxes(page) {
  const bar = page.locator("header.MuiAppBar-root");
  return {
    bar: await bar.boundingBox(),
    toolbar: await bar.locator(".MuiToolbar-root").boundingBox(),
    menu: await page
      .getByRole("button", { name: "Open navigation" })
      .boundingBox(),
    wordmark: await bar.locator("h1").boundingBox(),
    gear: await page.getByTestId("phone-settings").boundingBox(),
    plus: await page.getByRole("button", { name: "New task" }).boundingBox(),
  };
}

// The placement checks measure the bar without an environment chip, which
// takes up to 96 px of the wordmark's room. This worker's demo server is
// shared with the files run before this one (e2e/fixtures.js), and
// several of them save an environment name, so the name is cleared here
// and put back afterwards rather than assumed empty.
let environmentName = "";
test.beforeEach(async ({ page }) => {
  environmentName =
    (await (await page.request.get("/api/settings")).json()).environmentName ??
    "";
  await page.request.put("/api/settings", { data: { environmentName: "" } });
});
test.afterEach(async ({ page }) => {
  await page.request.put("/api/settings", { data: { environmentName } });
});

for (const colorScheme of ["light", "dark"]) {
  test(`a phone's top bar says the task list is stale while /api/tasks fails (390px, ${colorScheme})`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/");
    const rows = page.locator(".task-row");
    await expect(rows.first()).toBeVisible();
    const held = await rows.count();
    const chip = page.getByTestId("phone-tasks-stale");
    await expect(chip).toHaveCount(0);
    const before = await barBoxes(page);

    await page.route(LIVE_POLL, (route) =>
      route.fulfill({
        status: 502,
        contentType: "application/json",
        body: JSON.stringify({ error: "synthetic tasks 502" }),
      }),
    );
    try {
      await expect(chip).toBeVisible({ timeout: 10_000 });
      await expect(chip).toContainText("Task list not refreshing");
      await expect(chip).toHaveAccessibleName(/not refreshing/);
      await expect(chip).toHaveAttribute("title", /synthetic tasks 502/);
      // The list is still the one held: stale, not gone.
      await expect(rows).toHaveCount(held);

      // Placement: a row of its own across the bar, under the controls,
      // which keep exactly the places they had; the page starts below it.
      const after = await barBoxes(page);
      const box = await chip.boundingBox();
      expect(inside(box, after.bar), "the row is on the bar").toBe(true);
      expect(box.y).toBeGreaterThanOrEqual(
        after.toolbar.y + after.toolbar.height - 0.5,
      );
      expect(Math.abs(box.x - after.bar.x)).toBeLessThanOrEqual(0.5);
      expect(Math.abs(box.width - after.bar.width)).toBeLessThanOrEqual(0.5);
      expect(box.width).toBeLessThanOrEqual(390);
      expect(box.height).toBeLessThanOrEqual(36);
      for (const key of ["toolbar", "menu", "gear", "plus"]) {
        expect(after[key], `${key} moved`).toEqual(before[key]);
      }
      for (const key of ["menu", "wordmark", "gear", "plus"]) {
        expect(overlaps(box, after[key]), `${key} overlaps the row`).toBe(
          false,
        );
      }
      // The wordmark keeps its room: the row takes none from the bar.
      // Its room is what the bar's other items leave it, so it is
      // compared with the bar before the outage rather than with a
      // fixed width: an environment name an earlier spec left on the
      // worker's shared demo server puts a chip of up to 96 px beside it.
      expect(after.wordmark).toEqual(before.wordmark);
      expect(after.wordmark.width).toBeGreaterThan(0);
      const main = await page.locator("main").first().boundingBox();
      expect(main.y).toBeGreaterThanOrEqual(box.y + box.height - 0.5);
      // One line, nothing cut off at this width.
      const fits = await chip.evaluate((el) =>
        [...el.querySelectorAll("span")].every(
          (s) => s.scrollWidth <= s.clientWidth + 1,
        ),
      );
      expect(fits, "the row's text is cut off at 390px").toBe(true);
      // Legible on the bar in this scheme.
      const axe = await new AxeBuilder({ page })
        .include('[data-testid="phone-tasks-stale"]')
        .withRules(["color-contrast"])
        .analyze();
      expect(axe.violations).toEqual([]);

      if (process.env.SHOTS_DIR) {
        fs.mkdirSync(process.env.SHOTS_DIR, { recursive: true });
        await page.screenshot({
          path: path.join(
            process.env.SHOTS_DIR,
            `phone-tasks-stale-${colorScheme}.png`,
          ),
        });
      }

      // Tapping it asks again.
      const asked = page.waitForRequest(LIVE_POLL);
      await chip.click();
      await asked;
    } finally {
      await page.unroute(LIVE_POLL);
    }
    // The next poll that answers takes it away, with no tap.
    await expect(chip).toHaveCount(0, { timeout: 10_000 });
  });
}

test("the desktop shell has no phone stale chip: the rail says it there (1440px)", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.route(LIVE_POLL, (route) =>
    route.fulfill({
      status: 502,
      contentType: "application/json",
      body: JSON.stringify({ error: "synthetic tasks 502" }),
    }),
  );
  try {
    await expect(page.getByText("not refreshing").first()).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByTestId("phone-tasks-stale")).toHaveCount(0);
  } finally {
    await page.unroute(LIVE_POLL);
  }
});
