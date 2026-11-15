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

// The /next/ Processes page narrows its lists the way the classic
// /processes page does (task 5706): a sort kept in ?sort=, a custom order
// moved from the keyboard or by dragging, the filter kept in ?q=, one
// header New menu, and template/suite rows that say when they next run.

import { expect, test } from "./fixtures";

test.use({
  demoArgs: [
    "-schedules",
    "-github-sim",
    "-fake-ci",
    "-scheduled-workflows",
    "-sensitive-hold",
  ],
});
test.describe.configure({ mode: "serial" });

// rowTitles is the title of each row of the list on screen -- schedules
// and declared triggers alike -- once it has loaded.
async function rowTitles(
  page: import("@playwright/test").Page,
): Promise<string[]> {
  const titles = page.locator(".proc-list .proc-row .proc-row-title");
  await expect(titles.first()).toBeVisible();
  return titles.allTextContents();
}

test("template and suite rows show derived runs, on demand otherwise", async ({
  page,
}) => {
  await page.goto("processes/templates");
  const qual = page.locator('[data-testid^="tmpl-row-"]', {
    hasText: "release-qualification",
  });
  // The demo's daily schedule fires release-qualification.
  await expect(qual.locator('[data-testid^="tmpl-next-"]')).toContainText(
    "next:",
  );
  const audit = page.locator('[data-testid^="tmpl-row-"]', {
    hasText: "security-audit",
  });
  await expect(audit.locator('[data-testid^="tmpl-next-"]')).toHaveText(
    "· on demand",
  );

  await page.goto("processes/suites");
  await expect(
    page
      .locator('[data-testid^="suite-row-"]', {
        hasText: "Release Qualification Suite",
      })
      .locator('[data-testid^="suite-next-"]'),
  ).toHaveText("· on demand");
});

test("the sort is kept in ?sort= through reload and Back/Forward", async ({
  page,
}) => {
  await page.goto("processes/schedules");
  await expect(page.getByTestId("proc-sort")).toContainText("Custom order");
  const byDefault = await rowTitles(page);
  expect(byDefault.length).toBeGreaterThan(1);
  expect(byDefault).toEqual([...byDefault].sort((a, b) => a.localeCompare(b)));

  await page.getByTestId("proc-sort").click();
  await page
    .getByTestId("proc-sort-menu")
    .getByRole("option", { name: "Next run" })
    .click();
  await expect(page).toHaveURL(/[?&]sort=next/);
  await expect(page.getByTestId("proc-sort")).toContainText("Next run");

  // Kind puts schedules before triggers and workflow schedules.
  await page.getByTestId("proc-sort").click();
  await page
    .getByTestId("proc-sort-menu")
    .getByRole("option", { name: "Kind" })
    .click();
  await expect(page).toHaveURL(/[?&]sort=kind/);
  await page.reload();
  await expect(page.getByTestId("proc-sort")).toContainText("Kind");

  await page.goBack();
  await expect(page).toHaveURL(/[?&]sort=next/);
  await expect(page.getByTestId("proc-sort")).toContainText("Next run");
  await page.goForward();
  await expect(page.getByTestId("proc-sort")).toContainText("Kind");

  // A tab keeps the sort; suites have no Kind, so they read as custom.
  await page.getByTestId("proc-tab-templates").click();
  await expect(page).toHaveURL(/\/processes\/templates\?.*sort=kind/);
  await page.getByTestId("proc-tab-suites").click();
  await expect(page.getByTestId("proc-sort")).toContainText("Custom order");
});

test("the filter box is kept in ?q=", async ({ page }) => {
  await page.goto("processes/schedules");
  await page.getByTestId("view-filter").fill("Weekly");
  await expect(page).toHaveURL(/[?&]q=Weekly/);
  await expect(page.locator('[data-testid^="sched-row-"]')).toHaveCount(1);
  await page.reload();
  await expect(page.getByTestId("view-filter")).toHaveValue("Weekly");
  await expect(page.locator('[data-testid^="sched-row-"]')).toHaveCount(1);
  await page.getByTestId("view-filter").fill("");
  await expect(page).not.toHaveURL(/[?&]q=/);
});

test("a row moves in the custom order from the keyboard and by dragging", async ({
  page,
}) => {
  await page.goto("processes/schedules");
  await page.evaluate(() =>
    localStorage.removeItem("grain.list-order.processes"),
  );
  await page.reload();
  // The declared trigger arrives after the schedules; wait for it so the
  // list stops growing before it is read.
  await expect(page.locator('[data-testid^="trigger-row-"]')).toHaveCount(1);
  const before = await rowTitles(page);
  expect(before.length).toBeGreaterThan(1);

  const first = page.locator(".proc-list .proc-row").first();
  await first.focus();
  await page.keyboard.press("Alt+ArrowDown");
  await expect(page.getByTestId("proc-move-note")).toHaveText(
    `Moved to position 2 of ${before.length}.`,
  );
  const moved = [before[1], before[0], ...before.slice(2)];
  await expect.poll(() => rowTitles(page)).toEqual(moved);
  // Focus stays on the row that moved.
  await expect(page.locator(".proc-list .proc-row").nth(1)).toBeFocused();

  // The order is the classic page's own, kept across a reload.
  await page.reload();
  await expect.poll(() => rowTitles(page)).toEqual(moved);
  const stored = await page.evaluate(() =>
    JSON.parse(localStorage.getItem("grain.list-order.processes") || "[]"),
  );
  expect(stored).toHaveLength(before.length);
  for (const key of stored) expect(key).toMatch(/^(schedule|trigger):/);

  // Dragging the last row onto the first puts it first.
  const rows = page.locator(".proc-li");
  await rows.last().dragTo(rows.first());
  await expect
    .poll(() => rowTitles(page))
    .toEqual([moved[moved.length - 1], ...moved.slice(0, -1)]);

  // Under any other sort the rows do not move.
  await page.goto("processes/schedules?sort=name");
  await page.locator(".proc-list .proc-row").first().focus();
  await page.keyboard.press("Alt+ArrowDown");
  await expect(page.getByTestId("proc-move-note")).toHaveText("");
  await expect(rows.first()).not.toHaveAttribute("draggable", "true");

  await page.evaluate(() =>
    localStorage.removeItem("grain.list-order.processes"),
  );
});

test(
  "the header New menu opens the kind asked for",
  { tag: ["@phone", "@dark"] },
  async ({ page }) => {
    await page.goto("processes/schedules");
    await page.getByTestId("proc-new-menu-btn").click();
    const menu = page.getByTestId("proc-new-menu");
    await expect(menu.getByRole("menuitem")).toHaveText([
      "Schedule",
      "Template",
      "Suite",
    ]);
    await expect(page.getByTestId("proc-new-menu-hint")).toContainText(
      "declared in a repository",
    );
    // The menu sits inside the viewport, under its button.
    const box = (await menu.boundingBox())!;
    const vw = page.viewportSize()!.width;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);

    await page.getByTestId("proc-new-suite").click();
    await expect(page).toHaveURL(/\/processes\/suites/);
    await expect(page.getByTestId("suite-dialog")).toBeVisible();
    await page
      .getByTestId("suite-dialog")
      .getByRole("button", { name: "Cancel" })
      .click();

    await page.getByTestId("proc-new-menu-btn").click();
    await page.getByTestId("proc-new-template").click();
    await expect(page).toHaveURL(/\/processes\/templates/);
    await expect(
      page.locator('[aria-labelledby="tmpl-dialog-title"]'),
    ).toBeVisible();
    await page.keyboard.press("Escape");

    // ?new=suite deep-links the same form.
    await page.goto("processes/schedules?new=suite");
    await expect(page.getByTestId("suite-dialog")).toBeVisible();
  },
);

// -sensitive-hold declares acme/widgets' .grain/triggers/payments.yml.
test("a repository's declared trigger is a row that opens its Advanced tab", async ({
  page,
}) => {
  await page.goto("processes/schedules");
  const row = page.locator('[data-testid^="trigger-row-"]', {
    hasText: "payments.yml",
  });
  await expect(row).toContainText("acme/widgets");
  await expect(row).toContainText("on change");
  await page.getByTestId("sched-filter-paused").click();
  await expect(row).toHaveCount(0);
  await page.getByTestId("sched-filter-triggers").click();
  await row.click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/advanced/);
});

// The sort sits in each list's subbar beside its chips: one control
// height, inside the list pane, wrapping rather than overflowing at phone
// width. A trigger row and a template's "on demand" stay inside their row.
test(
  "the sort, trigger rows and derived runs fit their pane",
  { tag: ["@phone", "@dark"] },
  async ({ page }) => {
    await page.goto("processes/schedules");
    const pane = page.getByTestId("proc-list-pane");
    const bar = page.getByRole("toolbar", { name: "Schedule filter" });
    const sort = page.getByTestId("proc-sort");
    await expect(sort).toBeVisible();
    const paneBox = (await pane.boundingBox())!;
    const sortBox = (await sort.boundingBox())!;
    const chipBox = (await page.getByTestId("sched-filter-all").boundingBox())!;
    expect(Math.abs(sortBox.height - chipBox.height)).toBeLessThanOrEqual(1);
    expect(sortBox.x).toBeGreaterThanOrEqual(paneBox.x);
    expect(sortBox.x + sortBox.width).toBeLessThanOrEqual(
      paneBox.x + paneBox.width + 0.5,
    );
    expect(
      await bar.evaluate((el) => el.scrollWidth - el.clientWidth),
    ).toBeLessThanOrEqual(0);

    const trigger = page.locator('[data-testid^="trigger-row-"]').first();
    await expect(trigger).toBeVisible();
    const tBox = (await trigger.boundingBox())!;
    expect(tBox.x + tBox.width).toBeLessThanOrEqual(
      paneBox.x + paneBox.width + 0.5,
    );
    expect(
      await trigger.evaluate((el) => el.scrollWidth - el.clientWidth),
    ).toBeLessThanOrEqual(0);

    await page.goto("processes/templates");
    const onDemand = page
      .locator('[data-testid^="tmpl-next-"]', { hasText: "on demand" })
      .first();
    await expect(onDemand).toBeVisible();
    const row = page
      .locator('[data-testid^="tmpl-row-"]', { has: onDemand })
      .first();
    const rBox = (await row.boundingBox())!;
    const dBox = (await onDemand.boundingBox())!;
    expect(dBox.x + dBox.width).toBeLessThanOrEqual(rBox.x + rBox.width + 0.5);
  },
);
