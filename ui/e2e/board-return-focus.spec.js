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

test("Back from a standing session row on a tree board restores focus to that row's link", async ({
  page,
}) => {
  const board = await seedBoard(page, {
    name: `Return focus tree ${Date.now()}`,
    kind: "tree",
  });
  const path = `/boards/${board.id}`;
  await page.goto(path);

  const sessionLink = page.locator('.task-tree-id[href="/tasks/13"]');
  await expect(sessionLink).toBeVisible();
  await followAndComeBack(page, sessionLink, path);
  await expect(sessionLink).toBeFocused();
});

test("Moving one of two identical untitled panels from the keyboard keeps focus on the moved panel", async ({
  page,
}) => {
  const board = await seedBoard(page, {
    name: `Twin panels focus ${Date.now()}`,
    kind: "panels",
    panels: [
      { kind: "list", orderKey: 1 },
      { kind: "list", orderKey: 2 },
    ],
  });
  await page.goto(`/boards/${board.id}`);
  const secondHandle = page.getByRole("button", {
    name: "Move panel 2, “Task list”",
  });
  await expect(secondHandle).toBeVisible();
  await secondHandle.focus();
  await page.keyboard.press("Enter");

  const patchDone = page.waitForResponse(
    (r) =>
      r.url().includes(`/api/boards/${board.id}`) &&
      r.request().method() === "PATCH",
  );
  const refreshDone = page.waitForResponse(
    (r) => r.url().includes("/api/boards") && r.request().method() === "GET",
  );
  const slotAboveFirst = page.getByRole("button", {
    name: "Move here, above panel 1, “Task list”",
  });
  await slotAboveFirst.focus();
  await page.keyboard.press("Enter");
  await patchDone;
  await refreshDone;

  await expect(
    page.getByRole("button", { name: "Move panel 1, “Task list”" }),
  ).toBeFocused();
});

test("Same-named boards across folders keep distinct accessible names and do not overlap Customize sidebar actions on phone", async ({
  page,
}) => {
  const stamp = Date.now();
  const folderA = `Ops Incident Triage ${stamp}`;
  const folderB = `Zeta ${stamp}`;
  const boardA = await seedBoard(page, {
    name: `Triage ${stamp}`,
    folder: folderA,
    kind: "list",
  });
  const boardB = await seedBoard(page, {
    name: `Triage ${stamp}`,
    folder: folderB,
    kind: "kanban",
  });
  const nameA = `Triage ${stamp}, in ${folderA}`;
  const nameB = `Triage ${stamp}, in ${folderB}`;

  try {
    await page.goto("/boards");
    await expect(page.getByRole("link", { name: nameA })).toHaveAttribute(
      "href",
      `/boards/${boardA.id}`,
    );
    await expect(page.getByRole("link", { name: nameB })).toHaveAttribute(
      "href",
      `/boards/${boardB.id}`,
    );

    await page.getByRole("button", { name: "Customize sidebar" }).click();
    const dialog = page.getByRole("dialog", { name: "Customize sidebar" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("Type").click();
    await page.getByRole("option", { name: "Board" }).click();
    await dialog.getByLabel("Item").click();
    await page.getByRole("option", { name: nameA }).click();
    await dialog.getByRole("button", { name: "Pin item" }).click();
    await dialog.getByLabel("Item").click();
    await page.getByRole("option", { name: nameB }).click();
    await dialog.getByRole("button", { name: "Pin item" }).click();
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog).toHaveCount(0);

    await expect(
      page.locator("aside").getByRole("button", { name: nameA }),
    ).toBeVisible();
    await expect(
      page.locator("aside").getByRole("button", { name: nameB }),
    ).toBeVisible();

    // Check phone width (390x844): long disambiguated label + Board chip stay
    // strictly to the left of the pinned row's action buttons.
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("button", { name: /Open navigation|Navigation|Menu/i })
      .first()
      .click();
    await page.getByRole("button", { name: "Customize sidebar" }).click();
    await expect(dialog).toBeVisible();

    const longRow = dialog.locator("li.MuiListItem-root", { hasText: nameA });
    const chipBox = await longRow.locator(".MuiChip-root").boundingBox();
    const actionsBox = await longRow
      .locator(".customize-pinned-actions")
      .boundingBox();
    expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(actionsBox.x);
    await page.keyboard.press("Escape");
  } finally {
    await page.evaluate(() => localStorage.removeItem("grain.pinned"));
  }
});
