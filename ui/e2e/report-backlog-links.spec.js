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

test("a report board's backlog count lands on the matching filtered list", async ({
  page,
}) => {
  const board = await reportBoard(page);
  await page.goto(`/boards/${board.id}`);

  const waiting = page.getByRole("link", { name: /^Waiting \d+$/ });
  await expect(waiting).toBeVisible();
  const expected = "/?f=repo:any:acme/widgets&f=state:any:queued";
  await expect(waiting).toHaveAttribute("href", expected);
  const count = Number((await waiting.textContent()).match(/\d+/)[0]);

  await waiting.click();
  await expect(page).toHaveURL(
    new RegExp(`${expected.replace(/[?|]/g, "\\$&")}$`),
  );
  // The list it lands on is those tasks. Not exactly `count` of them:
  // the backlog counts a console session's task too, and no task list
  // shows one (taskFilters.js's isSessionTask).
  const rows = page.locator(".task-row");
  await expect(rows.first()).toBeVisible();
  expect(await rows.count()).toBeLessThanOrEqual(count);
});

test("the Metrics tab's backlog counts link to the list too", async ({
  page,
}) => {
  await page.goto("/system/metrics");
  const asked = page.getByRole("link", { name: /^Asked \d+$/ });
  await expect(asked).toBeVisible();
  await expect(asked).toHaveAttribute(
    "href",
    "/?f=state:any:awaiting_reply|awaiting_submit",
  );
});
