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

// The tree of work (docs/work-trees.md, grain/task-1926) against a real
// `grain demo`: the director's chat there filed the four tasks its
// conversation lists -- the toolchain bump, the pagination work (with
// its pull request open, acme/widgets#104), the dark mode toggle, and,
// one level further down, the pagination helper the pagination task
// filed itself. Nothing of grain's is stubbed: the tree is what
// GET /api/tasks/{id}/tree answers from the demo's own store.

async function taskId(page, title) {
  const res = await page.request.get("/api/tasks");
  expect(res.ok(), `GET /api/tasks: ${res.status()}`).toBe(true);
  const body = await res.json();
  const task = (Array.isArray(body) ? body : body.tasks).find(
    (t) => t.title === title,
  );
  expect(task, `no task called "${title}" in the demo store`).toBeTruthy();
  return task.id;
}

test("a chat's page draws the tree of work it filed", async ({ page }) => {
  const chat = await taskId(page, "Director Session");
  const pagination = await taskId(page, "Add pagination to the tasks API");
  const helper = await taskId(page, "Extract pagination query helper");

  await page.goto(`/tasks/${chat}`);
  const tree = page.getByRole("region", { name: "Work tree" });
  await expect(tree).toContainText(`Work from task ${chat}: 0 of 4 done`);
  await tree.locator(".provenance-toggle").click();

  // Nested: the helper sits under the pagination task, which sits under
  // the chat.
  const root = tree.locator(`li[data-task-id="${chat}"]`);
  const paginationItem = root.locator(`li[data-task-id="${pagination}"]`);
  await expect(paginationItem).toBeVisible();
  await expect(
    paginationItem.locator(`li[data-task-id="${helper}"]`),
  ).toBeVisible();
  const paginationRow = paginationItem.locator(".task-tree-row").first();
  await expect(paginationRow).toContainText("PR open");
  await expect(
    paginationRow.getByRole("link", { name: "acme/widgets#104" }),
  ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/104");

  // A task in the tree links to its own page, which draws the same tree
  // with that task marked as the one being read.
  await paginationItem
    .locator(`li[data-task-id="${helper}"]`)
    .getByRole("link", { name: helper, exact: true })
    .click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${helper}$`));
  const again = page.getByRole("region", { name: "Work tree" });
  await expect(again).toContainText("0 of 4 done");
  await again.locator(".provenance-toggle").click();
  await expect(
    again.locator(`li[data-task-id="${helper}"] > .task-tree-row`),
  ).toHaveClass(/task-tree-current/);
});

test("a Tree board nests the backlog by who filed what", async ({ page }) => {
  const chat = await taskId(page, "Director Session");
  const pagination = await taskId(page, "Add pagination to the tasks API");
  const helper = await taskId(page, "Extract pagination query helper");

  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === "Tree");
  expect(board, "no board called Tree in the demo store").toBeTruthy();
  await page.goto(`/boards/${board.id}`);

  await expect(page.locator(".repo-page-header")).toContainText("Tree");
  const root = page.locator(`.task-tree-board li[data-task-id="${chat}"]`);
  await expect(
    root.locator(
      `li[data-task-id="${pagination}"] li[data-task-id="${helper}"]`,
    ),
  ).toBeVisible();
  // A task nothing filed is a root of its own.
  await expect(
    page.locator(".task-tree-board > .task-tree > li", {
      hasText: "Fix flaky retry test",
    }),
  ).toBeVisible();
});

// docs/test-plans/2026-10-05.md §1 step 3 and 4
// (docs/test-runs/2026-10-05-work-origins-and-trees.md): the Tree board
// narrowed so a parent drops out, the tree's keyboard order and a 320px
// screen. (The tree as somebody outside the chat's team reads it went
// with teams: docs/single-tenant-reset.md, 3e-iii-c2.)

async function treeBoard(page) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === "Tree");
  expect(board, "no board called Tree in the demo store").toBeTruthy();
  return board;
}

test("a Tree board narrowed past a parent draws its child as a root", async ({
  page,
}) => {
  const pagination = await taskId(page, "Add pagination to the tasks API");
  const helper = await taskId(page, "Extract pagination query helper");
  const board = await treeBoard(page);
  await page.goto(`/boards/${board.id}`);
  await page.getByPlaceholder(/^Search/).fill("query helper");

  const roots = page.locator(".task-tree-board > .task-tree > li");
  await expect(roots).toHaveCount(1);
  await expect(roots.first()).toHaveAttribute("data-task-id", helper);
  await expect(
    page.locator(`.task-tree-board li[data-task-id="${pagination}"]`),
  ).toHaveCount(0);
});

// The sort the Tree board's toolbar offers has to order the tree: it
// wrote ?sort= into the URL and changed nothing, since the tree re-sorted
// its rows by id (docs/test-runs/2026-10-15-saved-boards-and-visualizations.md).
test("a Tree board's sort orders the tasks under each parent", async ({
  page,
}) => {
  const chat = await taskId(page, "Director Session");
  const children = await Promise.all(
    [
      "Add a dark mode toggle",
      "Add pagination to the tasks API",
      "Bump the Go toolchain to 1.24",
    ].map((title) => taskId(page, title)),
  );
  const board = await treeBoard(page);
  await page.goto(`/boards/${board.id}`);
  const kids = page.locator(
    `.task-tree-board li[data-task-id="${chat}"] > ul > li`,
  );
  await expect(kids).toHaveCount(3);

  await page.getByRole("button", { name: "Newest first" }).click();
  await page.getByRole("button", { name: /^Sort by/ }).click();
  await page.getByRole("menuitem", { name: /^Title/ }).click();
  await expect(page).toHaveURL(/sort=title:asc/);
  const order = () =>
    kids.evaluateAll((lis) => lis.map((li) => li.dataset.taskId));
  await expect.poll(order).toEqual(children);

  await page.reload();
  await expect(kids).toHaveCount(3);
  await expect.poll(order).toEqual(children);
});

test("the tree is read in filing order from the keyboard", async ({ page }) => {
  const chat = await taskId(page, "Director Session");
  const toggle = await taskId(page, "Add a dark mode toggle");
  const toolchain = await taskId(page, "Bump the Go toolchain to 1.24");
  const pagination = await taskId(page, "Add pagination to the tasks API");
  const helper = await taskId(page, "Extract pagination query helper");

  await page.goto(`/tasks/${chat}`);
  const tree = page.getByRole("region", { name: "Work tree" });
  await expect(tree).toContainText("0 of 4 done");
  await tree.locator(".provenance-toggle").click();
  await tree.getByRole("link", { name: chat, exact: true }).focus();
  const order = [];
  for (let i = 0; i < 6; i++) {
    order.push(
      await page.evaluate(() => document.activeElement.textContent.trim()),
    );
    await page.keyboard.press("Tab");
  }
  expect(order).toEqual([
    chat,
    toggle,
    toolchain,
    pagination,
    "acme/widgets#104",
    helper,
  ]);
  // The task being read is said to be, not only drawn bold.
  await expect(
    tree.locator(`li[data-task-id="${chat}"] > .task-tree-row`),
  ).toHaveAttribute("aria-current", "true");
});

test("the tree fits a 320px screen", async ({ page }) => {
  const chat = await taskId(page, "Director Session");
  const helper = await taskId(page, "Extract pagination query helper");
  await page.setViewportSize({ width: 320, height: 720 });
  for (const path of [
    `/tasks/${chat}`,
    `/boards/${(await treeBoard(page)).id}`,
  ]) {
    await page.goto(path);
    if (path.startsWith("/tasks/")) {
      await page
        .getByRole("region", { name: "Work tree" })
        .locator(".provenance-toggle")
        .click();
    }
    const row = page
      .locator(`li[data-task-id="${helper}"] > .task-tree-row`)
      .first();
    await expect(row).toBeVisible();
    const box = await row.boundingBox();
    expect(
      box.x + box.width,
      `${path}: the deepest row's right edge`,
    ).toBeLessThanOrEqual(320);
    const status = await row.locator(".task-tree-status").boundingBox();
    expect(status.x + status.width, `${path}: its status`).toBeLessThanOrEqual(
      320,
    );
  }
});
