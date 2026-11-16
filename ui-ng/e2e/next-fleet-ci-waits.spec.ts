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

// /next/system lists the tasks resting on resume_later for a CI verdict
// more than an hour old (GET /api/sandboxes ciWaits, what `grain health`
// warns of), which no page showed before
// (docs/test-runs/2026-11-15-resume-later-release.md, Finding 2). `grain
// demo` seeds one such task (pkg/demo/demo_ci_wait.go), so everything
// here is the real store's except the empty state, which the demo's
// answer is narrowed for.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

const TITLE = "Bump the widgets build to Go 1.27";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

async function seededWait(page: Page): Promise<{ id: string }> {
  const res = await page.request.get(
    `/api/tasks?q=${encodeURIComponent(TITLE)}`,
  );
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const list = Array.isArray(body) ? body : (body.tasks ?? []);
  const task = list.find((t: { title: string }) => t.title === TITLE);
  expect(task, `the demo seeds "${TITLE}"`).toBeTruthy();
  return { id: String(task.id) };
}

for (const size of SIZES) {
  test(`the CI waits card lists the demo's wait and fits the page — ${size.name}`, async ({
    page,
    axe,
  }) => {
    await page.setViewportSize({ width: size.width, height: size.height });
    const { id } = await seededWait(page);
    await page.goto("/next/system");
    await expect(page).toHaveURL(/\/next\/fleet/);

    const card = page.getByRole("article", { name: "Waiting on CI" });
    await expect(card).toBeVisible();
    await expect(card.getByTestId("ci-waits-count")).toHaveText(/1 waiting/);
    const table = card.getByRole("region", { name: "Tasks waiting on CI" });
    for (const col of [
      "Task",
      "Waited",
      "Waiting for",
      "Repository",
      "Branch",
      "CI mode",
    ]) {
      await expect(
        table.getByRole("columnheader", { name: col, exact: true }),
      ).toBeAttached();
    }
    const row = card.getByTestId(`ci-wait-row-${id}`);
    const link = row.getByRole("link", { name: `#${id}`, exact: true });
    await expect(link).toHaveAttribute("href", `/next/task/${id}`);
    const cells = row.getByRole("cell");
    // Seeded two hours ago, so it reads as hours, never seconds.
    await expect(cells.nth(1)).toHaveText(/^\d+h$/);
    await expect(cells.nth(2)).toHaveText("my-branch");
    await expect(cells.nth(3)).toHaveText("acme/widgets");
    // The task's own branch, which names it.
    await expect(cells.nth(4)).toHaveText(new RegExp(`grain/\\S*${id}`));
    await expect(cells.nth(5)).not.toHaveText("");

    // The card sits inside the viewport and the page does not scroll
    // sideways; on a phone the table scrolls inside its own region.
    const vw = size.width;
    const cardBox = (await card.boundingBox())!;
    expect(cardBox.x).toBeGreaterThanOrEqual(0);
    expect(cardBox.x + cardBox.width).toBeLessThanOrEqual(vw + 0.5);
    const tableBox = (await table.boundingBox())!;
    expect(tableBox.x).toBeGreaterThanOrEqual(cardBox.x - 0.5);
    expect(tableBox.x + tableBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 0.5,
    );
    // Every cell is on screen: the table never scrolls sideways, and on a
    // phone each row stacks into labelled lines under its task.
    for (let n = 0; n < 6; n++) {
      const c = (await cells.nth(n).boundingBox())!;
      expect(c.x).toBeGreaterThanOrEqual(cardBox.x - 0.5);
      expect(c.x + c.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 0.5,
      );
    }
    const overflow = await table.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    const waited = (await cells.nth(1).boundingBox())!;
    const task = (await cells.nth(0).boundingBox())!;
    if (size.name === "phone") {
      expect(waited.y).toBeGreaterThanOrEqual(task.y + task.height - 0.5);
      await expect(cells.nth(1)).toHaveAttribute("data-label", "Waited");
    } else {
      expect(Math.abs(waited.y - task.y)).toBeLessThan(1);
    }
    // One line per row: the branch is not broken over two.
    const branchLines = await cells.nth(4).evaluate((td) => {
      const range = document.createRange();
      range.selectNodeContents(td);
      return new Set(
        Array.from(range.getClientRects()).map((r) => Math.round(r.top)),
      ).size;
    });
    expect(branchLines).toBe(1);
    const scrollW = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollW).toBeLessThanOrEqual(vw);

    // The heading leads the card and the count pill sits beside it at
    // desktop width, in the head's right-hand slot.
    const title = card.getByRole("heading", { name: "Waiting on CI" });
    const titleBox = (await title.boundingBox())!;
    const pillBox = (await card.getByTestId("ci-waits-count").boundingBox())!;
    expect(titleBox.y).toBeLessThan(tableBox.y);
    expect(pillBox.y + pillBox.height).toBeLessThanOrEqual(tableBox.y);
    expect(pillBox.x + pillBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 0.5,
    );
    if (size.name === "desktop") {
      expect(pillBox.x).toBeGreaterThan(titleBox.x + titleBox.width);
      expect(pillBox.y).toBeLessThan(titleBox.y + titleBox.height);
    }

    expect(await axe()).toEqual([]);
  });
}

test("a closed task leaves the CI waits and a reopened one comes back, without a reload", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const { id } = await seededWait(page);
  const call = (action: string) =>
    fetch(`${demoServer}/api/tasks/${id}/${action}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    });
  await page.goto("/next/system");
  const card = page.getByRole("article", { name: "Waiting on CI" });
  const row = card.getByTestId(`ci-wait-row-${id}`);
  await expect(row).toBeVisible();
  // A reload would lose this.
  await page.evaluate(() => {
    (window as unknown as { ciWaitsMark: number }).ciWaitsMark = 1;
  });

  let closed = false;
  try {
    const res = await call("close");
    expect(res.ok).toBe(true);
    closed = true;
    await expect(row).toHaveCount(0, { timeout: 15_000 });
    await expect(card.getByTestId("ci-waits-empty")).toHaveText(
      "No task has waited on CI for more than an hour.",
    );

    const reopened = await call("reopen");
    expect(reopened.ok).toBe(true);
    closed = false;
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row.getByRole("cell").nth(2)).toHaveText("my-branch");
  } finally {
    if (closed) await call("reopen").catch(() => {});
  }
  expect(
    await page.evaluate(
      () => (window as unknown as { ciWaitsMark?: number }).ciWaitsMark,
    ),
  ).toBe(1);
});

test("the CI waits card says so when nothing has waited an hour @phone", async ({
  page,
  axe,
}) => {
  await page.route(/\/api\/sandboxes(\?.*)?$/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    delete body.ciWaits;
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/system");
  const card = page.getByRole("article", { name: "Waiting on CI" });
  await expect(card.getByTestId("ci-waits-empty")).toHaveText(
    "No task has waited on CI for more than an hour.",
  );
  await expect(card.getByRole("table")).toHaveCount(0);
  await expect(card.getByTestId("ci-waits-count")).toHaveCount(0);
  const vw = page.viewportSize()!.width;
  const box = (await card.boundingBox())!;
  expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
  expect(await axe()).toEqual([]);
});
