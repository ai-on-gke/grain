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
  try {
    await page.goto("tasks");
    await page.getByTestId("saved-boards-btn").click();
    await expectMenuLaidOut(page, b.id);
  } finally {
    await page.request.delete(`/api/boards/${b.id}`).catch(() => {});
  }
});

// The Timeline view on /next/tasks drew a task that was still open --
// waiting, asking, held as a proposal -- as a bar ending where it was
// filed, a 4% blip that read as finished, while the classic gantt draws
// the same task "still open" up to now. And a board saved from the
// Timeline view came back as the list, because the board's query never
// said which view it was (2026-11-14 saved boards walkthrough,
// docs/test-runs/2026-11-14-saved-boards-and-visualizations.md).
test("the Timeline view runs a still-open task's bar up to now", async ({
  page,
}) => {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const open = tasks.find(
    (t) => t.state === "proposed" && !t.endedAt && !t.closedAt,
  );
  expect(open, "a still-open proposal in the demo store").toBeTruthy();

  await page.goto("tasks?tab=proposed&view=timeline");
  const row = page.getByTestId(`row-${open.id}`);
  await expect(row).toBeVisible();
  const track = await row.locator(".timeline-track").boundingBox();
  const bar = await row.locator(".timeline-bar").boundingBox();
  // The bar reaches the right-hand ("now") edge of its track.
  expect(bar.x + bar.width).toBeGreaterThan(track.x + track.width - 2);

  // So does one filed a moment ago, whose minimum-width bar used to be
  // pushed 2% short of the edge.
  const res = await page.request.post("/api/tasks", {
    data: { title: "E2E fresh proposal", repo: open.repo || "acme/widgets" },
  });
  expect(res.ok(), `POST /api/tasks: ${res.status()}`).toBe(true);
  const fresh = await res.json();
  try {
    await page.reload();
    const freshRow = page.getByTestId(`row-${fresh.id}`);
    await expect(freshRow).toBeVisible();
    const ft = await freshRow.locator(".timeline-track").boundingBox();
    const fb = await freshRow.locator(".timeline-bar").boundingBox();
    expect(fb.x + fb.width).toBeGreaterThan(ft.x + ft.width - 2);
    expect(fb.x).toBeGreaterThanOrEqual(ft.x - 1);
  } finally {
    await page.request.delete(`/api/tasks/${fresh.id}`).catch(() => {});
  }
});

test("a board saved from the Timeline view opens on the Timeline again", async ({
  page,
}) => {
  const name = "E2E Timeline board";
  const plainName = "E2E Plain list board";
  try {
    await page.goto("tasks?tab=asked&view=timeline");
    await expect(page.getByTestId("tasks-timeline")).toBeVisible();
    const trigger = page.getByTestId("saved-boards-btn");
    await trigger.click();
    await page.getByTestId("board-save-open").click();
    await page.getByTestId("board-name-input").fill(name);
    const posted = page.waitForRequest(
      (r) => r.method() === "POST" && r.url().endsWith("/api/boards"),
    );
    await page.getByTestId("board-save-submit").click();
    const body = (await posted).postDataJSON();
    expect(new URLSearchParams(body.query).get("view")).toBe("timeline");
    expect(new URLSearchParams(body.query).get("tab")).toBe("asked");

    // Back on the plain list, applying the board brings the Timeline back.
    await page.getByTestId("tasks-view-list").click();
    await expect(page.getByTestId("tasks-timeline")).toBeHidden();
    const boards = await (await page.request.get("/api/boards")).json();
    const saved = boards.find((b) => b.name === name);
    await trigger.click();
    await page.getByTestId(`board-apply-${saved.id}`).click();
    await expect(page).toHaveURL(/[?&]view=timeline/);
    await expect(page.getByTestId("tasks-timeline")).toBeVisible();

    // And a board with no view of its own puts the list back.
    const res = await page.request.post("/api/boards", {
      data: { name: plainName, kind: "list", query: "tab=asked" },
    });
    expect(res.ok()).toBe(true);
    const plain = await res.json();
    await page.reload();
    await expect(page.getByTestId("tasks-timeline")).toBeVisible();
    await trigger.click();
    await page.getByTestId(`board-apply-${plain.id}`).click();
    await expect(page.getByTestId("tasks-timeline")).toBeHidden();
    await expect(page).not.toHaveURL(/[?&]view=/);
  } finally {
    const boards = await (await page.request.get("/api/boards")).json();
    for (const b of boards.filter((b) => [name, plainName].includes(b.name))) {
      await page.request.delete(`/api/boards/${b.id}`).catch(() => {});
    }
  }
});

// Applying a Kanban, report, dashboard, tree or timeline board -- or a
// list narrowed in the classic grammar (f=state:any:queued) -- from the
// /next/tasks Boards menu used to leave the reader on the bare task list
// with nothing changed: the URL stayed /next/tasks and the board's kind
// and narrowing were dropped (2026-11-17 saved boards walkthrough,
// docs/test-runs/2026-11-17-saved-boards-and-visualizations.md). Such a
// board now says it opens on the classic Boards page, and does.
test("a board /next/tasks cannot draw opens on its classic page and says so @phone", async ({
  page,
}) => {
  const stamp = Date.now();
  const made = [];
  const mk = async (data) => {
    const res = await page.request.post("/api/boards", { data });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const b = await res.json();
    made.push(b.id);
    return b;
  };
  try {
    // Long enough to ellipsize in the menu, which must still show the hint.
    const kanbanName = `E2E Kanban ${stamp} named past the width of the menu`;
    const kanban = await mk({ name: kanbanName, kind: "kanban" });
    const classicList = await mk({
      name: `E2E Queued ${stamp}`,
      kind: "list",
      query: "f=state:any:queued",
    });
    const nextList = await mk({
      name: `E2E Asked ${stamp}`,
      kind: "list",
      query: "tab=asked",
    });

    await page.goto("tasks");
    const trigger = page.getByTestId("saved-boards-btn");
    await trigger.click();
    const menu = page.getByTestId("saved-boards-menu");
    await expect(
      menu.getByRole("button", {
        name: `${kanbanName}, kanban, opens on the classic Boards page`,
        exact: true,
      }),
    ).toBeVisible();
    const kanbanApply = page.getByTestId(`board-apply-${kanban.id}`);
    await expect(kanbanApply).toContainText("kanban ↗");
    const btn = await kanbanApply.boundingBox();
    const hint = await kanbanApply.getByText("kanban ↗").boundingBox();
    expect(hint.x + hint.width).toBeLessThanOrEqual(btn.x + btn.width);
    await expect(
      menu.getByRole("button", {
        name: `E2E Queued ${stamp}, filtered list, opens on the classic Boards page`,
        exact: true,
      }),
    ).toBeVisible();
    // A board this page applies keeps its plain name.
    await expect(
      menu.getByRole("button", { name: `E2E Asked ${stamp}`, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByTestId(`board-apply-${nextList.id}`),
    ).not.toContainText("↗");

    await page.getByTestId(`board-apply-${classicList.id}`).click();
    await expect(page).toHaveURL(
      new RegExp(`/boards/${classicList.id}(\\?|$)`),
    );
    await expect(
      page.getByRole("heading", { name: `E2E Queued ${stamp}` }),
    ).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/next\/tasks/);

    await trigger.click();
    await page.getByTestId(`board-apply-${kanban.id}`).click();
    await expect(page).toHaveURL(new RegExp(`/boards/${kanban.id}(\\?|$)`));
    await expect(page.getByRole("heading", { name: kanbanName })).toBeVisible();
    await page.goBack();

    await trigger.click();
    await page.getByTestId(`board-apply-${nextList.id}`).click();
    await expect(page).toHaveURL(/\/next\/tasks\?tab=asked$/);
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});

// A failed GET /api/boards on /next/tasks used to leave the Boards menu
// saying "No saved boards yet.", indistinguishable from an empty deployment
// (2026-11-15 saved boards walkthrough,
// docs/test-runs/2026-11-15-saved-boards-and-visualizations.md).
test("a failed /api/boards read explains why the boards could not be loaded rather than saying none exist", async ({
  page,
}) => {
  await page.route("**/api/boards", (route) => {
    if (route.request().method() === "GET") {
      return route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "store unavailable" }),
      });
    }
    return route.continue();
  });

  await page.goto("tasks");
  await page.getByTestId("saved-boards-btn").click();
  const menu = page.getByTestId("saved-boards-menu");
  await expect(menu.getByTestId("boards-load-error")).toContainText(
    "The boards could not be loaded (store unavailable).",
  );
  await expect(menu).not.toContainText("No saved boards yet.");
});

// Choosing New → New board from the shell while already on a narrowed
// /next/tasks view used to drop tab, q, repo and view=timeline before
// opening the save bar, and closing the save bar dropped focus to <body>
// (2026-11-15 saved boards walkthrough).
test("New board from the rail keeps the current Tasks narrowing and Timeline view, and returns focus", async ({
  page,
}) => {
  const name = "E2E Rail Narrowed";
  try {
    await page.goto(
      "tasks?tab=asked&q=merge&repo=acme%2Fwidgets&view=timeline",
    );
    await expect(page.getByTestId("tasks-timeline")).toBeVisible();

    await page.getByTestId("rail-create-menu").click();
    await page
      .getByTestId("create-menu")
      .getByRole("link", { name: /New board/ })
      .click();

    await expect(page.getByTestId("board-save-form")).toBeVisible();
    await expect(page.getByTestId("tasks-timeline")).toBeVisible();
    await expect(page.getByTestId("board-name-input")).toBeFocused();

    // Escape closes the save bar and hands focus to the Boards button.
    const trigger = page.getByTestId("saved-boards-btn");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("board-save-form")).toBeHidden();
    await expect(trigger).toBeFocused();

    // Saving from the rail's New board entry keeps every active parameter.
    await page.getByTestId("rail-create-menu").click();
    await page
      .getByTestId("create-menu")
      .getByRole("link", { name: /New board/ })
      .click();
    await page.getByTestId("board-name-input").fill(name);
    const posted = page.waitForRequest(
      (r) => r.method() === "POST" && r.url().endsWith("/api/boards"),
    );
    await page.getByTestId("board-save-submit").click();
    const params = new URLSearchParams((await posted).postDataJSON().query);
    expect(params.get("tab")).toBe("asked");
    expect(params.get("q")).toBe("merge");
    expect(params.get("repo")).toBe("acme/widgets");
    expect(params.get("view")).toBe("timeline");
    await expect(trigger).toBeFocused();
  } finally {
    const boards = await (await page.request.get("/api/boards")).json();
    for (const b of boards.filter((b) => b.name === name)) {
      await page.request.delete(`/api/boards/${b.id}`).catch(() => {});
    }
  }
});

// Inside the 30/70 split pane (410px on a 1440px desktop) and on a 390px
// phone, a Backlog row's ⋮ button used to wrap onto a third line at the
// far-left edge and open its right-aligned popup at x = -54 underneath the
// rail, making Move down / Move up unclickable (2026-11-15 walkthrough).
test("the Backlog row More actions menu sits in the right-hand slot and opens on screen @phone @dark", async ({
  page,
}) => {
  await page.goto("tasks");
  const firstRow = page.locator(".backlog-row").first();
  await expect(firstRow).toBeVisible();

  const orderBefore = await page
    .locator(".backlog-row")
    .evaluateAll((els) => els.map((el) => el.getAttribute("data-task-id")));
  expect(orderBefore.length).toBeGreaterThanOrEqual(2);
  const firstId = orderBefore[0];
  const secondId = orderBefore[1];
  // Move down may cross a priority band, which re-prioritises the moved
  // task as well as reordering it; remember both so cleanup restores both.
  const priorityOf = async (id) =>
    (await (await page.request.get(`/api/tasks/${id}`)).json()).priority;
  const firstPriority = await priorityOf(firstId);

  try {
    const rowBox = await firstRow.boundingBox();
    const menuBtn = page.getByTestId(`menu-${firstId}`);
    const btnBox = await menuBtn.boundingBox();
    // Action sits in the right-hand half of its row, near the right edge.
    expect(btnBox.x).toBeGreaterThan(rowBox.x + rowBox.width / 2);
    expect(
      rowBox.x + rowBox.width - (btnBox.x + btnBox.width),
    ).toBeLessThanOrEqual(24);

    await menuBtn.click();
    const popup = page.locator(`#menu-list-${firstId}`);
    await expect(popup).toBeVisible();
    const popBox = await popup.boundingBox();
    const vw = page.viewportSize().width;
    const railBox = await page
      .locator("nav.nav-rail")
      .boundingBox()
      .catch(() => null);
    const minX =
      railBox && railBox.width > 0 && railBox.height > 200
        ? railBox.x + railBox.width
        : 0;
    expect(popBox.x).toBeGreaterThanOrEqual(minX);
    expect(popBox.x + popBox.width).toBeLessThanOrEqual(vw);

    // Move down is genuinely clickable (not intercepted by the rail) and reorders.
    const reordered = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/api/tasks/reorder") &&
        r.ok(),
    );
    await popup.getByRole("button", { name: "Move down" }).click();
    await reordered;
    await expect
      .poll(() =>
        page
          .locator(".backlog-row")
          .evaluateAll((els) =>
            els.map((el) => el.getAttribute("data-task-id")),
          ),
      )
      .toEqual([secondId, firstId, ...orderBefore.slice(2)]);
  } finally {
    // Restore the worker-scoped demoServer's backlog order (and the moved
    // task's priority), and fail loudly if the restore did not take, so a
    // later test in this worker never inherits a reordered backlog.
    if ((await priorityOf(firstId)) !== firstPriority) {
      const patched = await page.request.patch(`/api/tasks/${firstId}`, {
        data: { priority: firstPriority },
      });
      expect(patched.ok()).toBe(true);
    }
    const restored = await page.request.post("/api/tasks/reorder", {
      data: { ids: [firstId], beforeId: secondId, afterId: "" },
    });
    expect(restored.ok()).toBe(true);
    await page.reload();
    await expect
      .poll(() =>
        page
          .locator(".backlog-row")
          .evaluateAll((els) =>
            els.map((el) => el.getAttribute("data-task-id")),
          ),
      )
      .toEqual(orderBefore);
  }
});

// Active board matching on /next/tasks used to compare raw query strings
// without checking whether the board opens here: a classic Kanban board with
// query "q=merge" claimed to be active on /next/tasks?q=merge, while a /next/
// list saved as "q=merge&tab=asked" did not match after being applied
// (2026-11-19 saved boards walkthrough).
test("the Boards button never claims a classic board is active and matches reordered /next/ queries", async ({
  page,
}) => {
  const stamp = Date.now();
  const made = [];
  const mk = async (data) => {
    const res = await page.request.post("/api/boards", { data });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const b = await res.json();
    made.push(b.id);
    return b;
  };
  try {
    await mk({
      name: `E2E Merge Kanban ${stamp}`,
      kind: "kanban",
      query: "q=merge",
    });
    const rev = await mk({
      name: `E2E Reversed ${stamp}`,
      kind: "list",
      query: "q=merge&tab=asked",
    });

    await page.goto("tasks?q=merge");
    const trigger = page.getByTestId("saved-boards-btn");
    await expect(trigger).toBeVisible();
    await expect(trigger).not.toContainText(`E2E Merge Kanban ${stamp}`);

    await trigger.click();
    await page.getByTestId(`board-apply-${rev.id}`).click();
    await expect(page).toHaveURL(/\/next\/tasks\?tab=asked&q=merge$/);
    await expect(trigger).toContainText(`E2E Reversed ${stamp}`);
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});

// Without max-height / overflow-y: auto on .boards-menu.pop-menu, a deployment
// with ~20 saved boards pushed the menu past the bottom edge of the viewport
// and slid "+ Save current view…" behind the phone bottom bar (2026-11-19
// saved boards walkthrough).
test("a long Boards menu stays inside the viewport and scrolls @phone @dark", async ({
  page,
}) => {
  const stamp = Date.now();
  const made = [];
  try {
    for (let i = 1; i <= 20; i++) {
      const res = await page.request.post("/api/boards", {
        data: { name: `E2E Scroll ${stamp}-${i}`, kind: "list" },
      });
      expect(res.ok()).toBe(true);
      made.push((await res.json()).id);
    }

    await page.goto("tasks");
    await page.getByTestId("saved-boards-btn").click();
    const menu = page.getByTestId("saved-boards-menu");
    await expect(menu).toBeVisible();

    const vh = page.viewportSize().height;
    const vw = page.viewportSize().width;
    const box = await menu.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(vw);
    expect(box.y + box.height).toBeLessThanOrEqual(vh);

    // On a phone, the menu must stop above the bottom navigation bar too.
    const bottomNav = await page
      .locator("nav.compact-nav")
      .boundingBox()
      .catch(() => null);
    if (bottomNav && bottomNav.height > 0 && bottomNav.y > vh / 2) {
      expect(box.y + box.height).toBeLessThanOrEqual(bottomNav.y);
    }

    const scrollMeta = await menu.evaluate((el) => ({
      overflowY: getComputedStyle(el).overflowY,
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
    }));
    expect(["auto", "scroll"]).toContain(scrollMeta.overflowY);
    expect(scrollMeta.scrollHeight).toBeGreaterThan(scrollMeta.clientHeight);

    const saveBtn = page.getByTestId("board-save-open");
    await saveBtn.scrollIntoViewIfNeeded();
    await expect(saveBtn).toBeVisible();
  } finally {
    for (const id of made) {
      await page.request.delete(`/api/boards/${id}`).catch(() => {});
    }
  }
});

// Cold-loading /next/tasks?newBoard=1 as a read-only viewer used to open the
// Save Board bar before GET /api/config returned, and Backlog rows still
// showed an enabled ⋮ -> Defer button to viewers lacking tasks:write
// (2026-11-19 saved boards walkthrough).
test("a read-only viewer is never shown the Save Board bar on ?newBoard=1 or Backlog row action menus", async ({
  page,
}) => {
  await page.route("**/api/config", async (route) => {
    const resp = await route.fetch();
    const json = await resp.json();
    await new Promise((r) => setTimeout(r, 150));
    json.access = {
      role: "grain/viewer",
      roles: ["grain/viewer"],
      full: false,
      scopes: [
        "tasks:read",
        "boards:read",
        "metrics:read",
        "repos:read",
        "ci:read",
      ],
    };
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(json),
    });
  });

  await page.goto("tasks?newBoard=1");
  await expect(page).toHaveURL(/\/next\/tasks$/);
  await expect(page.getByTestId("board-save-form")).toBeHidden();
  await expect(page.locator(".backlog-row").first()).toBeVisible();
  await expect(page.locator('[data-testid^="menu-"]')).toHaveCount(0);
  await expect(page.locator(".drag-handle")).toHaveCount(0);
});

// A failed initial GET /api/tasks in Timeline view used to draw "Timeline · 0
// tasks" under the error banner, and Backlog showed "Ready to run 0"
// (2026-11-19 saved boards walkthrough).
test("a failed initial /api/tasks read does not draw a 0-task Timeline or Ready to run 0", async ({
  page,
}) => {
  await page.route("**/api/tasks**", (route) => {
    if (route.request().method() === "GET") {
      return route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "tasks db offline" }),
      });
    }
    return route.continue();
  });

  await page.goto("tasks?view=timeline");
  await expect(
    page.locator('[role="alert"]').filter({ hasText: "tasks db offline" }),
  ).toBeVisible();
  await expect(page.getByTestId("tasks-timeline")).toHaveCount(0);
  await expect(page.getByTestId("timeline-empty")).toHaveCount(0);

  await page.getByTestId("tasks-view-list").click();
  await expect(page.getByTestId("ready-count")).toHaveText("Ready to run");
});
