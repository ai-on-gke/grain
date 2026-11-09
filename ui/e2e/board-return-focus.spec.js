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

// Following a link out of a dashboard or a report and coming Back puts
// focus on the link that was followed, as it already did for a row's
// name (RowLink, grain/task-1580). A panel's "Open", its state counts,
// a report's backlog counts and its task references left focus on
// <body> instead: Back remounts the board -- a report fetches itself
// again, so its links do not exist until that lands -- and a keyboard
// user started again from the top of the document (2026-11-09 boards
// walkthrough, useReturnFocus in ListPrimitives.jsx).

const seededBoards = [];

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
});

async function seedBoard(page, data) {
  const res = await page.request.post("/api/boards", { data });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seededBoards.push(board.id);
  return board;
}

// follow presses Enter on link, waits to leave the board, goes Back
// and waits for the board to be drawn again.
async function followAndComeBack(page, link, boardPath) {
  await link.focus();
  await page.keyboard.press("Enter");
  await expect(page).not.toHaveURL(new RegExp(`${boardPath}(\\?|$)`));
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`${boardPath}(\\?|$)`));
}

test("Back to a dashboard focuses the panel link that was followed", async ({
  page,
}) => {
  const stamp = Date.now();
  // Two panels following the dashboard's own question: their "Open"
  // links share one href, so Back has to tell them apart.
  const board = await seedBoard(page, {
    name: `Return focus dash ${stamp}`,
    kind: "panels",
    query: "f=repo:any:acme/widgets",
    panels: [
      { kind: "list", title: `List ${stamp}`, orderKey: 1 },
      { kind: "states", title: `States ${stamp}`, orderKey: 2 },
    ],
  });
  const path = `/boards/${board.id}`;
  await page.goto(path);
  const listPanel = page.locator(".panel", { hasText: `List ${stamp}` });
  const statesPanel = page.locator(".panel", { hasText: `States ${stamp}` });
  const listOpen = listPanel.getByRole("link", { name: "Open", exact: true });
  const statesOpen = statesPanel.getByRole("link", {
    name: "Open",
    exact: true,
  });
  await expect(statesOpen).toBeVisible();
  expect(await statesOpen.getAttribute("href")).toBe(
    await listOpen.getAttribute("href"),
  );

  // The second panel's Open, not the first one sharing its href.
  await followAndComeBack(page, statesOpen, path);
  await expect(statesOpen).toBeFocused();

  // A state count inside the panel.
  const count = statesPanel.locator("a.panel-state").first();
  await expect(count).toBeVisible();
  await followAndComeBack(page, count, path);
  await expect(count).toBeFocused();

  // A row's name kept its own behaviour.
  const row = listPanel.locator(".task-row a.row-link").first();
  await followAndComeBack(page, row, path);
  await expect(row).toBeFocused();
});

test("Back to a report focuses the backlog count or task that was followed", async ({
  page,
}) => {
  const board = await seedBoard(page, {
    name: `Return focus report ${Date.now()}`,
    kind: "report",
    sections: [{ kind: "backlog", orderKey: 1 }],
  });
  const path = `/boards/${board.id}`;
  await page.goto(path);

  const counts = page.locator(".metrics-panel a.MuiChip-root");
  await expect(counts.first()).toBeVisible();
  // The last count rather than the first, so landing on the first by
  // accident would not pass.
  const n = await counts.count();
  const count = counts.nth(n - 1);
  const name = await count.innerText();
  await followAndComeBack(page, count, path);
  await expect(
    page.locator(".metrics-panel a.MuiChip-root", { hasText: name }),
  ).toBeFocused();

  const task = page
    .getByText(/Oldest waiting task:/)
    .getByRole("link", { name: /^task \d+$/ });
  await expect(task).toBeVisible();
  await followAndComeBack(page, task, path);
  await expect(task).toBeFocused();
});
