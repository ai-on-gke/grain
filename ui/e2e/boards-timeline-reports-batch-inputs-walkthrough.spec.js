import { test, expect } from "./fixtures.js";

// Every input on boards, the timeline, report and panel dashboards, and
// the batch-actions bar, operated in a real browser against `grain demo`
// -- docs/test-runs/2026-10-11-boards-timeline-reports-batch-inputs.md
// is the walkthrough this spec keeps honest.
//
// What the other specs already cover (board.spec.js's column layout
// across a reload, gantt.spec.js's range and lanes across a reload,
// boards.spec.js's panel dashboards, batch_actions_workflows.spec.js's
// defer and Priority bulk edit) is not repeated. What is here is the part
// they leave: driving these inputs from the keyboard alone, the edits a
// form must refuse or fall back from, a malformed narrowing on each view
// that takes one, the board's own card checkboxes feeding the batch bar,
// and each surface at a phone's width.

// MUI leaves a closed Select's menu list in the DOM labelled by the same
// element as the box, so the pickers are found by role rather than by
// label -- gantt.spec.js's own `select`, and its reason.
const combobox = (page, name) => page.getByRole("combobox", { name });

// Where a demo board lives, asked of the server rather than assumed --
// gantt.spec.js's boardPath.
async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

// Everything a test here seeds, taken away again after it: the suite
// shares one demo store per worker, and a board or task left behind is a
// row in some other spec's list (boards.spec.js's own reasoning).
const seededBoards = [];
const seededTasks = [];

async function seedBoard(page, data) {
  const res = await page.request.post("/api/boards", { data });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seededBoards.push(board.id);
  return board;
}

async function seedTask(page, data) {
  const res = await page.request.post("/api/tasks", { data });
  expect(res.ok(), `POST /api/tasks: ${res.status()}`).toBe(true);
  const task = await res.json();
  seededTasks.push(task.id);
  return task;
}

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    await page.request.delete(`/api/boards/${seededBoards.pop()}`);
  }
  while (seededTasks.length > 0) {
    await page.request.delete(`/api/tasks/${seededTasks.pop()}`);
  }
});

// Whether the page is wider than the window it is in -- the one thing a
// phone-width layout must never be, since the overflow is off-screen
// and, on .gantt, clipped rather than scrollable.
async function overflowsHorizontally(page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth + 1,
  );
}

const columnsButton = (page) =>
  page.getByRole("button", { name: "Columns", exact: true });

test("edits board columns from the keyboard, keeps each state in one column, and asks before dropping an edit", async ({
  page,
}) => {
  await page.goto(await boardPath(page, "Board"));
  await expect(page.locator(".board-column").first()).toBeVisible();

  await columnsButton(page).click();
  const heading = page.getByRole("heading", { name: "Board columns" });
  await expect(heading).toBeVisible();

  // The first column's title, typed into and tabbed out of: the next
  // stop is that column's own States picker.
  const firstTitle = page.getByLabel("Column 1 title");
  await expect(firstTitle).toHaveValue("Proposed");
  await firstTitle.focus();
  await page.keyboard.press("End");
  await page.keyboard.type(" ideas");
  await expect(firstTitle).toHaveValue("Proposed ideas");
  await page.keyboard.press("Tab");
  const firstStates = combobox(page, "States").first();
  await expect(firstStates).toBeFocused();
  await expect(firstStates).toHaveText("Proposed");

  // A new column, its states picked with the keyboard. Queued belongs to
  // the second column until it is picked here; a state is in one column
  // only, so picking it takes it away from there.
  await page.getByRole("button", { name: "+ Add column" }).click();
  const lastTitle = page.getByLabel(/Column \d+ title/).last();
  await expect(lastTitle).toHaveValue("New column");
  await lastTitle.fill("Waiting lane");
  const lastStates = combobox(page, "States").last();
  await expect(lastStates).toHaveText("No states");
  await lastStates.focus();
  await page.keyboard.press("Enter");
  const queued = page.getByRole("option", { name: "Waiting", exact: true });
  await expect(queued).toBeVisible();
  await queued.focus();
  await page.keyboard.press("Enter");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(lastStates).toHaveText("Waiting");
  await expect(combobox(page, "States").nth(1)).toHaveText("No states");

  // The move buttons are named after the column they move and disabled
  // at the ends.
  await expect(
    page.getByRole("button", { name: "Move Proposed ideas left" }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Move Waiting lane right" }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Move Waiting lane left" }).click();
  await expect(page.getByLabel(/Column \d+ title/).last()).not.toHaveValue(
    "Waiting lane",
  );

  // Escape over an edited form asks first; declining keeps the edit.
  let asked = "";
  page.once("dialog", (d) => {
    asked = d.message();
    d.dismiss();
  });
  await page.keyboard.press("Escape");
  await expect.poll(() => asked).not.toBe("");
  await expect(heading).toBeVisible();
  await expect(page.getByLabel("Column 1 title")).toHaveValue("Proposed ideas");

  // Cancel throws the whole draft away: the board is as it was.
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(heading).toHaveCount(0);
  await expect(
    page.locator(".board-column", { hasText: "Waiting lane" }),
  ).toHaveCount(0);
  await expect(
    page.locator(".board-column-title", { hasText: "Proposed ideas" }),
  ).toHaveCount(0);

  // An emptied title is not a column with no name: Save falls back to
  // the label of the states it collects.
  await columnsButton(page).click();
  await page.getByLabel("Column 2 title").fill("");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(heading).toHaveCount(0);
  await expect(
    page.locator(".board-column-title", { hasText: /^Waiting$/ }),
  ).toBeVisible();

  // Every column removed falls back to the default board rather than
  // saving a board with nothing on it.
  await columnsButton(page).click();
  const removes = page.getByRole("button", { name: /^Remove / });
  while ((await removes.count()) > 0) await removes.first().click();
  await expect(
    page.getByText("No columns. Saving now puts the default board back."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(
    page.locator(".board-column-title", { hasText: /^Proposed$/ }),
  ).toBeVisible();
  await expect(
    page.locator(".board-column-title", { hasText: /^Running$/ }),
  ).toBeVisible();
});

test("selects board cards by checkbox and column, and bulk edits a text field from the batch bar", async ({
  page,
}) => {
  const stamp = Date.now();
  const a = await seedTask(page, { title: `E2E board pick A ${stamp}` });
  const b = await seedTask(page, { title: `E2E board pick B ${stamp}` });

  await page.goto(await boardPath(page, "Board"));
  const proposed = page.locator(".board-column", {
    has: page.locator(".board-column-title", { hasText: /^Proposed$/ }),
  });
  await expect(
    proposed.locator(".board-card", { hasText: a.title }),
  ).toBeVisible({ timeout: 20_000 });

  // One card by the mouse, the other by the keyboard: Space on a focused
  // checkbox ticks it like a click does.
  await page.getByRole("checkbox", { name: `Select ${a.id}` }).check();
  await expect(page.getByText("1 selected")).toBeVisible();
  const pickB = page.getByRole("checkbox", { name: `Select ${b.id}` });
  await pickB.focus();
  await page.keyboard.press("Space");
  await expect(pickB).toBeChecked();
  await expect(page.getByText("2 selected")).toBeVisible();

  // The column's own checkbox selects every card in it, and clears them
  // all again.
  const all = page.getByRole("checkbox", {
    name: "Select every task in Proposed",
  });
  await all.check();
  const inColumn = await proposed.locator(".board-card").count();
  await expect(page.getByText(`${inColumn} selected`)).toBeVisible();
  await all.uncheck();
  await expect(page.getByText(/\d+ selected/)).toHaveCount(0);

  await page.getByRole("checkbox", { name: `Select ${a.id}` }).check();
  await page.getByRole("checkbox", { name: `Select ${b.id}` }).check();
  await expect(page.getByText("2 selected")).toBeVisible();

  // The capability picker is named even once a capability is picked, and
  // Attach and Detach wait for one.
  await expect(
    page.getByRole("combobox", { name: "Capability to attach or detach" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Attach" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Detach" })).toBeDisabled();

  await page.getByRole("button", { name: "Bulk edit…" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("2 selected tasks.")).toBeVisible();
  const apply = dialog.getByRole("button", { name: "Apply to 2 tasks" });
  // Nothing ticked is nothing to apply.
  await expect(apply).toBeDisabled();

  // The deployment's own text field, ticked and typed into.
  await dialog.getByRole("checkbox", { name: /^Requested by/ }).check();
  const requestedBy = dialog.getByRole("textbox", { name: "Requested by" });
  await requestedBy.fill(`Board walkthrough ${stamp}`);
  // A task names no team, so there is no Team row to move it with.
  await expect(dialog.getByRole("checkbox", { name: /^Team/ })).toHaveCount(0);

  await expect(apply).toBeEnabled();
  await apply.click();
  await expect(dialog).toHaveCount(0);

  for (const t of [a, b]) {
    await expect
      .poll(async () => {
        const res = await page.request.get(`/api/tasks/${t.id}`);
        const body = await res.json();
        return body.customFields?.["requested-by"];
      })
      .toBe(`Board walkthrough ${stamp}`);
  }

  // A batch that went through clears the selection behind it.
  await expect(page.getByText(/\d+ selected/)).toHaveCount(0);
});

test("operates the timeline's range, lanes and search from the keyboard, and names a malformed narrowing", async ({
  page,
}) => {
  const path = await boardPath(page, "Timeline");
  await page.goto(path);
  await expect(page.locator(".gantt-row").first()).toBeVisible();

  // Range: focused, opened with the keyboard, an option picked by arrow.
  const range = combobox(page, "Range");
  await range.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("listbox")).toBeVisible();
  await page.getByRole("option", { name: "Everything" }).focus();
  await page.keyboard.press("Enter");
  await expect(range).toHaveText("Everything");
  await expect(range).toBeFocused();

  // Lanes, the same way: each lane gets a title row.
  const lanes = combobox(page, "Lanes");
  await lanes.focus();
  await page.keyboard.press("Enter");
  await page.getByRole("option", { name: "By state" }).focus();
  await page.keyboard.press("Enter");
  await expect(lanes).toHaveText("By state");
  await expect(page.locator(".gantt-lane-title").first()).toBeVisible();

  // The search box narrows the rows as it is typed into, and clearing
  // it brings them back.
  const before = await page.locator(".gantt-row").count();
  const search = page.getByPlaceholder("Search tasks…");
  await search.fill("Bump the Go toolchain");
  await expect(
    page.locator(".gantt-row", { hasText: "Bump the Go toolchain to 1.24" }),
  ).toBeVisible();
  await expect(page.locator(".gantt-row")).toHaveCount(1);
  await search.fill("");
  await expect(page.locator(".gantt-row")).toHaveCount(before);

  // A narrowing naming an attribute that does not exist is said, not
  // silently dropped -- and removing it takes it out of the address.
  await page.goto(`${path}?f=nosuchattr:any:x`);
  const unknown = page.getByText("Unknown condition: nosuchattr");
  await expect(unknown).toBeVisible();
  await page
    .locator(".MuiChip-root", { hasText: "Unknown condition: nosuchattr" })
    .locator(".MuiChip-deleteIcon")
    .click();
  await expect(unknown).toHaveCount(0);
  await expect(page).not.toHaveURL(/nosuchattr/);

  // Put the defaults back for whichever spec opens the chart next.
  await combobox(page, "Range").click();
  await page.getByRole("option", { name: "Last 7 days" }).click();
  await combobox(page, "Lanes").click();
  await page.getByRole("option", { name: "No lanes" }).click();
});

test("operates a report board's window and narrowing, and a malformed condition on it", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E report inputs ${stamp}`,
    kind: "report",
    sections: [
      { kind: "throughput", orderKey: 0 },
      { kind: "backlog", orderKey: 1 },
    ],
  });
  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `E2E report inputs ${stamp}` }),
  ).toBeVisible();

  const window = combobox(page, "Window");
  await expect(window).toBeVisible();
  await window.focus();
  await page.keyboard.press("Enter");
  await page.getByRole("option", { name: "Last 30 days" }).focus();
  await page.keyboard.press("Enter");
  await expect(window).toHaveText("Last 30 days");

  const refresh = page.getByRole("button", { name: "Refresh" });
  await expect(refresh).toBeEnabled({ timeout: 20_000 });
  await refresh.click();
  await expect(refresh).toBeEnabled({ timeout: 20_000 });
  await expect(page.getByLabel("Loading the report")).toHaveCount(0, {
    timeout: 20_000,
  });

  // The narrowing's search box on a report takes free text without
  // breaking the report under it.
  const search = page.getByPlaceholder("Search tasks…");
  await search.fill("zz-no-task-has-this-title");
  await expect(search).toHaveValue("zz-no-task-has-this-title");
  await expect(page.getByText(/could not be loaded/)).toHaveCount(0);
  await search.fill("");

  await page.goto(`/boards/${board.id}?f=nosuchattr:any:x`);
  await expect(page.getByText("Unknown condition: nosuchattr")).toBeVisible();
  await expect(page.getByText(/could not be loaded/)).toHaveCount(0);
});

test("lays out the columns editor, board, timeline, report, dashboard and batch bar at a phone's width", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const stamp = Date.now();
  const task = await seedTask(page, { title: `E2E phone pick ${stamp}` });
  const report = await seedBoard(page, {
    name: `E2E phone report ${stamp}`,
    kind: "report",
    sections: [{ kind: "throughput", orderKey: 0 }],
  });
  const dashboard = await seedBoard(page, {
    name: `E2E phone dashboard ${stamp}`,
    kind: "panels",
    panels: [
      { kind: "states", title: "Where everything is", width: "half" },
      { kind: "list", title: "Bad query", query: "f=nosuchattr:any:x" },
    ],
  });

  // The columns editor wraps each row rather than squeezing the title
  // box: on a phone it is still wide enough to read what was typed.
  await page.goto(await boardPath(page, "Board"));
  await columnsButton(page).click();
  const title = page.getByLabel("Column 1 title");
  const box = await title.boundingBox();
  expect(box.width).toBeGreaterThan(100);
  await page.getByRole("button", { name: "Cancel" }).click();

  // The batch bar wraps its buttons rather than running off the edge.
  await page.getByRole("checkbox", { name: `Select ${task.id}` }).check();
  await expect(page.getByText("1 selected")).toBeVisible();
  const clear = page.getByRole("button", { name: "Clear selection" });
  await expect(clear).toBeInViewport();
  await clear.click();

  // Both timeline pickers stay on screen.
  await page.goto(await boardPath(page, "Timeline"));
  await expect(combobox(page, "Range")).toBeInViewport();
  await expect(combobox(page, "Lanes")).toBeInViewport();
  expect(await overflowsHorizontally(page)).toBe(false);

  await page.goto(`/boards/${report.id}`);
  await expect(combobox(page, "Window")).toBeInViewport();
  expect(await overflowsHorizontally(page)).toBe(false);

  await page.goto(`/boards/${dashboard.id}`);
  await expect(page.locator(".panel")).toHaveCount(2);
  await expect(
    page
      .locator(".panel", { hasText: "Bad query" })
      .locator(".panel-unknown-note"),
  ).toContainText("Unknown condition: nosuchattr");
  expect(await overflowsHorizontally(page)).toBe(false);
});
