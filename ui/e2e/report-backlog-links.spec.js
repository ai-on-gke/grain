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

// A report's backlog names the oldest waiting task and counts the
// unfinished tasks by state. Both are links (grain/task-2231): the task
// to /tasks/<id>, opened over the report on a plain click, and each
// count to the task list narrowed to the report's scope and that state,
// the way the dashboard's state counts are. They were buttons with no
// href, which could not be opened in a new tab or copied.

const seededBoards = [];

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
});

async function reportBoard(page) {
  const res = await page.request.post("/api/boards", {
    data: {
      name: `Backlog links ${Date.now()}`,
      kind: "report",
      query: "f=repo:any:acme/widgets",
    },
  });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seededBoards.push(board.id);
  return board;
}

test("a report board's oldest waiting task is a link that opens the overlay", async ({
  page,
}) => {
  const board = await reportBoard(page);
  await page.goto(`/boards/${board.id}`);

  const line = page.getByText(/Oldest waiting task:/);
  await expect(line).toBeVisible();
  const link = line.getByRole("link", { name: /^task \d+$/ });
  const href = await link.getAttribute("href");
  expect(href).toMatch(/^\/tasks\/\d+$/);
  const id = href.split("/").pop();

  await link.click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${id}$`));
  await expect(
    page.locator(".detail-overlay, [role=dialog]").first(),
  ).toBeVisible();
});

test("a report board's backlog counts match the filtered lists they open", async ({
  page,
}) => {
  const board = await reportBoard(page);
  await page.goto(`/boards/${board.id}`);

  const waiting = page.getByRole("link", { name: /^Waiting \d+$/ });
  const asked = page.getByRole("link", { name: /^Asked \d+$/ });
  await expect(waiting).toBeVisible();
  await expect(asked).toBeVisible();

  // Bounding-box check: chips sit aligned on the row without overlapping,
  // and each state dot is vertically centred inside its chip.
  const waitingBox = await waiting.boundingBox();
  const askedBox = await asked.boundingBox();
  expect(waitingBox).not.toBeNull();
  expect(askedBox).not.toBeNull();
  expect(Math.abs(waitingBox.y - askedBox.y)).toBeLessThanOrEqual(2);
  expect(waitingBox.x + waitingBox.width).toBeLessThan(askedBox.x);
  for (const [chip, box] of [
    [waiting, waitingBox],
    [asked, askedBox],
  ]) {
    const dotBox = await chip.locator(".dot").boundingBox();
    expect(dotBox).not.toBeNull();
    const chipMidY = box.y + box.height / 2;
    const dotMidY = dotBox.y + dotBox.height / 2;
    expect(Math.abs(chipMidY - dotMidY)).toBeLessThanOrEqual(2);
  }

  const expectedWaiting = "/?f=repo:any:acme/widgets&f=state:any:queued";
  await expect(waiting).toHaveAttribute("href", expectedWaiting);
  const waitingCount = Number((await waiting.textContent()).match(/\d+/)[0]);
  expect(waitingCount).toBe(2);

  await waiting.click();
  await expect(page).toHaveURL(
    new RegExp(`${expectedWaiting.replace(/[?|]/g, "\\$&")}$`),
  );
  // Session tasks are excluded from both backlogOf and the task list, so
  // the rendered row count matches the chip count exactly.
  const rows = page.locator(".task-row");
  await expect(rows).toHaveCount(waitingCount);

  await page.goto(`/boards/${board.id}`);
  const askedAgain = page.getByRole("link", { name: /^Asked \d+$/ });
  await expect(askedAgain).toBeVisible();
  const expectedAsked =
    "/?f=repo:any:acme/widgets&f=state:any:awaiting_reply&f=completed:empty:";
  await expect(askedAgain).toHaveAttribute("href", expectedAsked);
  const askedCount = Number((await askedAgain.textContent()).match(/\d+/)[0]);
  expect(askedCount).toBe(1);

  await askedAgain.click();
  await expect(page).toHaveURL(
    new RegExp(`${expectedAsked.replace(/[?|]/g, "\\$&")}$`),
  );
  await expect(rows).toHaveCount(askedCount);
});

test("the Metrics tab's backlog counts link to the matching list too", async ({
  page,
}) => {
  await page.goto("/system/metrics");
  const asked = page.getByRole("link", { name: /^Asked \d+$/ });
  await expect(asked).toBeVisible();
  await expect(asked).toHaveAttribute(
    "href",
    "/?f=state:any:awaiting_reply&f=completed:empty:",
  );
  const askedCount = Number((await asked.textContent()).match(/\d+/)[0]);

  await asked.click();
  await expect(page.locator(".task-row")).toHaveCount(askedCount);
});

test("phone width: backlog chips and narrowed filter chips wrap cleanly without horizontal overflow", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const board = await reportBoard(page);
  await page.goto(`/boards/${board.id}`);

  const waiting = page.getByRole("link", { name: /^Waiting \d+$/ });
  const asked = page.getByRole("link", { name: /^Asked \d+$/ });
  await expect(waiting).toBeVisible();
  await expect(asked).toBeVisible();

  for (const chip of [waiting, asked]) {
    const box = await chip.boundingBox();
    expect(box).not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  }

  await asked.click();
  await expect(page.locator(".task-row")).toHaveCount(1);
  const filterChips = page.locator(".narrowing-chips .MuiChip-root");
  await expect(filterChips).toHaveCount(3);
  const chipCount = await filterChips.count();
  for (let i = 0; i < chipCount; i++) {
    const box = await filterChips.nth(i).boundingBox();
    expect(box).not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  }
});
