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

// expectMenuLaidOut checks the Boards menu's geometry: the whole menu on
// the screen, each board's name and folder drawn in full, and the ×
// a small square beside it. A generic "button { width: 100% }" rule
// used to stretch the × across the row and squeeze every name to a
// 28px ellipsis, and at phone width the menu opened off the left edge.
async function expectMenuLaidOut(page, boardId) {
  const vw = page.viewportSize().width;
  const menu = await page.getByTestId("saved-boards-menu").boundingBox();
  expect(menu.x).toBeGreaterThanOrEqual(0);
  expect(menu.x + menu.width).toBeLessThanOrEqual(vw);
  const apply = page.getByTestId(`board-apply-${boardId}`);
  const del = await page.getByTestId(`board-delete-${boardId}`).boundingBox();
  expect(del.width).toBeLessThanOrEqual(32);
  const a = await apply.boundingBox();
  expect(a.x + a.width).toBeLessThanOrEqual(del.x + 1);
  expect(await apply.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(
    true,
  );
  // A board that opens on the classic page: its name drawn in full too,
  // and its "Classic ↗" tag inside the row.
  const name = apply.locator(".board-classic-name");
  if (await name.count()) {
    expect(await name.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(
      true,
    );
    const tag = await apply.locator(".board-classic-tag").boundingBox();
    expect(tag.x + tag.width).toBeLessThanOrEqual(a.x + a.width);
  }
}

// The Boards menu on /next/tasks deleted a saved board -- a setting
// shared by everyone on the deployment -- on one click of its ×, and
// named three same-named boards in different folders "Triage" and
// "Delete board Triage" alike (2026-11-13 saved boards walkthrough,
// docs/test-runs/2026-11-13-saved-boards-and-visualizations-task-5385.md).
test("the Boards menu tells same-named boards apart and asks before deleting one", async ({
  page,
}) => {
  const made = [];
  const mk = async (data) => {
    const res = await page.request.post("/api/boards", { data });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const b = await res.json();
    made.push(b.id);
    return b;
  };
  try {
    const loose = await mk({ name: "E2E Twin", kind: "list" });
    const ops = await mk({ name: "E2E Twin", folder: "E2E Ops", kind: "list" });
    await mk({ name: "E2E Twin", folder: "E2E Zeta", kind: "list" });

    await page.goto("tasks");
    const trigger = page.getByTestId("saved-boards-btn");
    await trigger.click();
    const menu = page.getByTestId("saved-boards-menu");
    await expect(
      menu.getByRole("button", {
        name: "E2E Twin, outside every folder",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      menu.getByRole("button", { name: "E2E Twin, in E2E Ops", exact: true }),
    ).toBeVisible();
    await expect(
      menu.getByRole("button", { name: "Delete board E2E Twin, in E2E Zeta" }),
    ).toBeVisible();
    // The folder is drawn after the name, so sighted readers can tell
    // them apart too.
    await expect(page.getByTestId(`board-apply-${ops.id}`)).toContainText(
      "E2E Ops",
    );
    await expectMenuLaidOut(page, ops.id);

    // × asks first. Cancel deletes nothing and hands focus back to the
    // Boards button.
    const deletes = [];
    page.on("request", (r) => {
      if (r.method() === "DELETE" && r.url().includes("/api/boards/")) {
        deletes.push(new URL(r.url()).pathname);
      }
    });
    await page.getByTestId(`board-delete-${loose.id}`).click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText(
      "Delete board E2E Twin, outside every folder?",
    );
    await expect(dialog).toContainText("everyone on this deployment");
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(deletes).toEqual([]);

    // Accepting deletes exactly that board, and focus lands on the
    // Boards button rather than on <body>.
    await trigger.click();
    await page.getByTestId(`board-delete-${loose.id}`).click();
    await dialog.getByRole("button", { name: "Delete board" }).click();
    await expect(dialog).toBeHidden();
    await expect.poll(() => deletes).toEqual([`/api/boards/${loose.id}`]);
    await expect(trigger).toBeFocused();
    const left = await (await page.request.get("/api/boards")).json();
    expect(left.map((b) => b.id)).not.toContain(loose.id);
    expect(left.map((b) => b.id)).toContain(ops.id);
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});

test("the Boards menu opens on the screen at phone width @phone", async ({
  page,
}) => {
  const res = await page.request.post("/api/boards", {
    data: { name: "E2E Phone board", folder: "E2E Folder", kind: "list" },
  });
  expect(res.ok()).toBe(true);
  const b = await res.json();
  const kb = await (
    await page.request.post("/api/boards", {
      data: { name: "E2E Phone kanban", folder: "E2E Folder", kind: "kanban" },
    })
  ).json();
  try {
    await page.goto("tasks");
    await page.getByTestId("saved-boards-btn").click();
    await expectMenuLaidOut(page, b.id);
    // A board that opens on the classic page fits as well, tag and all.
    await expectMenuLaidOut(page, kb.id);
  } finally {
    await page.request.delete(`/api/boards/${b.id}`).catch(() => {});
    await page.request.delete(`/api/boards/${kb.id}`).catch(() => {});
  }
});

// The /next/ Boards menu and the classic boards read one query grammar
// (ui-ng/src/app/tasks/board-query.ts). /next/ used to read only its own
// tab=&q=&repo= keys, so a classic board -- f=state:any:queued -- or a
// Kanban, Report, Tree or Panels board opened as an unnarrowed Backlog,
// and a view saved here lost its tab on /boards/<id> (2026-11-13 saved
// boards walkthrough, task 5385, §2.8 and §4).
test("the Boards menu opens a classic board on its tab and sends a Kanban board to the classic page @dark", async ({
  page,
  axe,
}) => {
  const made = [];
  const mk = async (data) => {
    const res = await page.request.post("/api/boards", { data });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const b = await res.json();
    made.push(b.id);
    return b;
  };
  try {
    const asked = await mk({
      name: "E2E Asked retry",
      kind: "list",
      query: "q=retry&f=state:any:awaiting_reply|failed",
    });
    const kanban = await mk({ name: "E2E Flow", kind: "kanban", query: "" });
    const report = await mk({ name: "E2E Report", kind: "report" });

    await page.goto("tasks");
    const trigger = page.getByTestId("saved-boards-btn");
    await trigger.click();
    await page.getByTestId(`board-apply-${asked.id}`).click();
    await expect(page).toHaveURL(/[?&]tab=asked\b/);
    await expect(page).toHaveURL(/[?&]q=retry\b/);
    await expect(page.getByTestId("tab-asked")).toHaveAttribute(
      "aria-current",
      "page",
    );
    // The Boards button names the board on screen.
    await expect(trigger).toContainText("E2E Asked retry");

    // A Kanban or Report board is a link to its classic page, and says so.
    await trigger.click();
    const menu = page.getByTestId("saved-boards-menu");
    // Opening the menu focuses its first entry, link or button.
    await expect(menu.locator("a[href], button").first()).toBeFocused();
    for (const [b, kind] of [
      [kanban, "Kanban"],
      [report, "Report"],
    ]) {
      const link = menu.getByRole("link", {
        name: `${b.name}, opens on the classic page: it is a ${kind} board`,
      });
      await expect(link).toHaveAttribute("href", `/boards/${b.id}`);
      await expect(page.getByTestId(`board-classic-${b.id}`)).toHaveText(
        "Classic ↗",
      );
    }
    await expectMenuLaidOut(page, kanban.id);
    // The tag sits inside the row, after the name, on one line.
    const tag = await page
      .getByTestId(`board-classic-${kanban.id}`)
      .boundingBox();
    const row = await page
      .getByTestId(`board-apply-${kanban.id}`)
      .boundingBox();
    expect(tag.x + tag.width).toBeLessThanOrEqual(row.x + row.width);
    expect(tag.height).toBeLessThan(row.height);
    expect(await axe()).toEqual([]);
    await menu.getByRole("link", { name: /^E2E Flow,/ }).click();
    await expect(page).toHaveURL(new RegExp(`/boards/${kanban.id}$`));
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});

test("a view saved on /next/ is stored in the board grammar and a board-grammar link opens its tab", async ({
  page,
}) => {
  let id = "";
  try {
    await page.goto("tasks?tab=asked&q=retry");
    await page.getByTestId("saved-boards-btn").click();
    await page.getByTestId("board-save-open").click();
    await page.getByTestId("board-name-input").fill("E2E Saved asked");
    const posted = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname === "/api/boards",
    );
    await page.getByTestId("board-save-submit").click();
    const b = await (await posted).json();
    id = b.id;
    // The tab is a condition on state, which the classic list honours.
    expect(b.kind).toBe("list");
    expect(b.query).toBe("q=retry&f=state:any:awaiting_reply|failed");

    // A link in that grammar opens the tab it names here too.
    await page.goto("tasks?f=state:any:awaiting_reply|failed");
    await expect(page).toHaveURL(/[?&]tab=asked\b/);
    await expect(page).not.toHaveURL(/[?&]f=/);
    await expect(page.getByTestId("tab-asked")).toHaveAttribute(
      "aria-current",
      "page",
    );
  } finally {
    if (id) await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
});
