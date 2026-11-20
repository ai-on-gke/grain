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

// The /next/ Repos list (2026-11-14 repository walkthrough,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md): /next/repos
// fell through to the Inbox and the rail had no Repos entry, so the
// count chips with Blocked first, search and sort, Add repo, and the
// pointer and keyboard reorder were classic-only. Over a real
// `grain demo` with its GitHub simulation and qualification seeding.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const ADDED = "acme/gadgets-e2e";

const rowNames = (page) =>
  page
    .getByRole("list", { name: "Repos" })
    .getByRole("listitem")
    .evaluateAll((lis) => lis.map((li) => li.getAttribute("data-repo")));

async function withExtraRepo(page, fn) {
  const res = await page.request.post("/api/repos", {
    data: { repo: ADDED },
  });
  expect(res.ok()).toBe(true);
  try {
    await fn();
  } finally {
    await page.request.delete(`/api/repos/${ADDED}`);
  }
}

test("the rail opens Repos, whose row counts open work with Blocked first and links on", async ({
  page,
  pageErrors,
}) => {
  await page.goto("/next/inbox");
  await page.getByRole("link", { name: "Repos", exact: true }).first().click();
  await expect(page).toHaveURL(/\/next\/repos$/);
  await expect(page.getByRole("heading", { name: /^Repos/ })).toBeVisible();

  const row = page.locator('[data-repo="acme/widgets"]');
  await expect(row).toBeVisible();
  // The demo has one blocked task in acme/widgets: its chip leads.
  const chips = row.locator("[data-chip]");
  await expect(chips.first()).toHaveText(/^Blocked 1$/);
  await expect(row.locator('[data-chip="running"]')).toContainText("Running");
  await expect(
    row.getByRole("img", { name: /^Active: \d+ tasks? running now$/ }),
  ).toBeVisible();

  await row.getByRole("link", { name: /open tasks? in acme\/widgets/ }).click();
  await expect(page).toHaveURL(/\/next\/tasks\?repo=acme%2Fwidgets/);
  await page.goBack();
  await row.getByRole("link", { name: "acme/widgets", exact: true }).click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);
  await page.getByRole("link", { name: "← Repos" }).click();
  await expect(page).toHaveURL(/\/next\/repos$/);
  expect(pageErrors).toEqual([]);
});

test("search and sort live in the URL and survive a reload", async ({
  page,
}) => {
  await withExtraRepo(page, async () => {
    await page.goto("/next/repos");
    await expect.poll(() => rowNames(page)).toEqual([ADDED, "acme/widgets"]);

    await page.getByRole("searchbox", { name: "Filter repos" }).fill("widg");
    await expect.poll(() => rowNames(page)).toEqual(["acme/widgets"]);
    await expect(page).toHaveURL(/[?&]q=widg/);
    // No reorder handles while a filter hides rows.
    await expect(page.getByRole("button", { name: /^Move / })).toHaveCount(0);

    await page.getByRole("searchbox", { name: "Filter repos" }).fill("zzz");
    await expect(page.getByTestId("repos-no-match")).toContainText("zzz");
    await page.getByRole("button", { name: "Clear the filter" }).click();
    await expect.poll(() => rowNames(page)).toHaveLength(2);

    await page.getByTestId("repos-sort").click();
    await page.getByRole("option", { name: "Most tasks" }).click();
    await expect(page).toHaveURL(/[?&]sort=tasks/);
    await page.reload();
    await expect.poll(() => rowNames(page)).toEqual(["acme/widgets", ADDED]);
    await expect(page.getByTestId("repos-sort")).toContainText("Most tasks");
  });
});

test("Add repo stores the repo, locks while adding, and says it landed", async ({
  page,
}) => {
  try {
    await page.goto("/next/repos");
    await expect(page.getByTestId("repos-allowlist-note")).toContainText(
      "allows only acme/widgets",
    );
    const field = page.getByRole("combobox", { name: "Repository to add" });
    await field.fill(ADDED);
    let release;
    const held = new Promise((r) => (release = r));
    await page.route("**/api/repos", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      await held;
      await route.continue();
    });
    await page.getByRole("button", { name: "Add repo" }).click();
    await expect(field).toBeDisabled();
    await expect(page.getByTestId("repo-discovery-status")).toHaveText(
      `Adding ${ADDED}…`,
    );
    release();
    await expect(page.getByTestId("repo-discovery-status")).toHaveText(
      `Added ${ADDED}`,
    );
    await expect(page.locator(`[data-repo="${ADDED}"]`)).toBeVisible();
    await expect(field).toHaveValue("");
    await expect(page.getByTestId("repos-allowlist-note")).toHaveCount(0);
    await page.unroute("**/api/repos");
  } finally {
    await page.request.delete(`/api/repos/${ADDED}`);
  }
});

test("rows reorder by keyboard and by pointer, and the order is kept", async ({
  page,
}) => {
  await withExtraRepo(page, async () => {
    await page.goto("/next/repos");
    await page.evaluate(() =>
      localStorage.removeItem("grain.list-order.repos"),
    );
    await page.reload();
    await expect.poll(() => rowNames(page)).toEqual([ADDED, "acme/widgets"]);

    const handle = page.getByRole("button", {
      name: "Move acme/widgets, position 2",
    });
    await handle.focus();
    await page.keyboard.press("Space");
    await expect(page.getByTestId("repos-announcer")).toContainText(
      "Picked up acme/widgets",
    );
    await page.keyboard.press("ArrowUp");
    await expect.poll(() => rowNames(page)).toEqual(["acme/widgets", ADDED]);
    await page.keyboard.press("Space");
    await expect(page.getByTestId("repos-announcer")).toContainText(
      "Dropped acme/widgets at position 1",
    );
    await page.reload();
    await expect.poll(() => rowNames(page)).toEqual(["acme/widgets", ADDED]);

    // Pointer: drag acme/widgets below the last row.
    const src = page.getByRole("button", {
      name: "Move acme/widgets, position 1",
    });
    const target = page.locator(`[data-repo="${ADDED}"]`);
    const box = await target.boundingBox();
    await src.dragTo(target, {
      targetPosition: { x: 40, y: box.height - 6 },
    });
    await expect.poll(() => rowNames(page)).toEqual([ADDED, "acme/widgets"]);
    await page.reload();
    await expect.poll(() => rowNames(page)).toEqual([ADDED, "acme/widgets"]);
  });
});

test("on a phone Add repo stays on screen and no row scrolls sideways @phone", async ({
  page,
  axe,
}) => {
  await page.goto("/next/repos");
  const add = page.getByRole("button", { name: "Add repo" });
  await expect(add).toBeVisible();
  const vw = page.viewportSize().width;
  const box = await add.boundingBox();
  expect(box.x + box.width).toBeLessThanOrEqual(vw);
  const row = page.locator('[data-repo="acme/widgets"]');
  const rb = await row.boundingBox();
  expect(rb.x + rb.width).toBeLessThanOrEqual(vw);
  const scrollW = await page.evaluate(
    () => document.documentElement.scrollWidth,
  );
  expect(scrollW).toBeLessThanOrEqual(vw);
  expect(await axe()).toEqual([]);
});
