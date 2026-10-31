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

// Saved boards (grain/task-48) against a real server and a real
// browser -- see tasks.spec.js's own doc comment for what this suite is
// and what `grain demo` has already seeded by the time it runs.
//
// What is worth a real browser here rather than another jsdom test: a
// board is a row on the server, so this is the one place the whole thing
// is exercised end to end -- the API that stores it, the rail that lists
// it, the URL that opens it cold, and the narrowing it carries actually
// narrowing the list underneath.

// Every board this file makes, and it takes them away again after each
// test. The whole suite shares one `grain demo` store, and a board is a
// row in the nav rail every other spec is looking at: shell.spec.js
// measures whether the foot of that rail is still on a tablet's screen,
// and gantt.spec.js clicks a nav entry whose name a board's own can
// shadow. A spec that seeds three boards and leaves them there is
// changing the deployment the specs beside it are testing.
const seeded = [];

async function seedBoard(page, data) {
  const res = await page.request.post("/api/boards", { data });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seeded.push(board.id);
  return board;
}

test.afterEach(async ({ page }) => {
  while (seeded.length > 0) {
    await page.request.delete(`/api/boards/${seeded.pop()}`);
  }
});

// The "New board" button on the page, as opposed to the rail's compact
// "+", which carries the same accessible name and a different job: the
// page's button opens the pre-written new-task form (boards.js), while
// the rail's opens the Director with a prompt in the composer
// (App.jsx's openNewBoardWithDirector). Both the boards list and a
// board's own page put theirs in the page's header rail
// (PageHeader.jsx); the nav rail is the <aside> beside it.
const newBoardButton = (page) =>
  page.getByTestId("page-header").getByRole("button", { name: "New board" });

test("lists a board in the rail and opens it with its own narrowing", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E running ${stamp}`,
    folder: "E2E",
    kind: "list",
    description: "What is running right now",
    query: "f=state:any:running",
  });

  await page.goto("/");
  // The section's own heading, the folder the board is filed under, and
  // the board itself -- opened from the Boards panel.
  await expect(page.getByRole("button", { name: /^Boards/ })).toBeVisible();
  await page.getByRole("button", { name: /^Boards/ }).click();
  const entry = page.getByText(`E2E running ${stamp}`);
  await expect(entry).toBeVisible();

  await entry.click();
  await expect(page).toHaveURL(new RegExp(`/boards/${board.id}`));
  // The board's own question is in force: the seeded running task is
  // there and the seeded queued one is not.
  await expect(
    page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
  ).toBeVisible();
  await expect(
    page.locator(".task-row", { hasText: "Fix flaky retry test" }),
  ).toHaveCount(0);
});

test("loads a board cold from its own URL", async ({ page }) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E cold ${stamp}`,
    kind: "kanban",
    query: "",
    columns: [{ name: "In flight", states: ["running"] }],
  });

  await page.goto(`/boards/${board.id}`);
  // The page's own heading is the board's name; the pane under it is
  // the ordinary board, headed as it always is.
  await expect(
    page.getByRole("heading", { name: `E2E cold ${stamp}` }),
  ).toBeVisible();
  // Its own column, rather than the columns this browser keeps for the
  // ordinary board.
  const column = page.locator(".board-column", { hasText: "In flight" });
  await expect(column).toBeVisible();
  await expect(
    column.locator(".board-card", {
      hasText: "Bump the Go toolchain to 1.24",
    }),
  ).toBeVisible();
});

// ...and the one thing about a board the UI does write itself: where it
// sits (grain/task-81). Worth a real browser for the same reason the
// rest of this file is: the move is a PATCH against the row an agent
// would otherwise be asked to edit, so this is the only place the
// gesture, the request and what ListBoards hands back afterwards are
// exercised together.
//
// Driven through the handle's tap gesture rather than a drag: it is the
// same drop, and two clicks are deterministic where a synthesized HTML5
// drag is a bet on the browser's own event sequence.
test("reorders a board within its folder, and files one into it", async ({
  page,
}) => {
  const stamp = Date.now();
  const folder = `E2E order ${stamp}`;
  // Explicit order keys rather than the default 0 for both: two boards
  // sharing a key come back in id order, which is a lexical sort of
  // "board-9" against "board-10" and not the order they were made in.
  const first = await seedBoard(page, {
    name: `E2E first ${stamp}`,
    folder,
    kind: "list",
    query: "",
    orderKey: 1,
  });
  const second = await seedBoard(page, {
    name: `E2E second ${stamp}`,
    folder,
    kind: "list",
    query: "",
    orderKey: 2,
  });
  const loose = await seedBoard(page, {
    name: `E2E loose ${stamp}`,
    kind: "list",
    query: "",
  });

  await page.goto("/boards");
  await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
  // This test's own boards, in the order the page is showing them --
  // every other board this deployment has is somebody else's. Polled
  // with a long ceiling rather than the default five seconds: every
  // check here is waiting on a request and a re-render, and a CI runner
  // with the rest of this suite on it is slower than it looks.
  const ours = async () =>
    (await page.locator(".template-name").allTextContents()).filter((n) =>
      n.endsWith(`${stamp}`),
    );
  const expectOurs = (names) =>
    expect.poll(ours, { timeout: 20_000 }).toEqual(names);
  await expectOurs([
    `E2E loose ${stamp}`,
    `E2E first ${stamp}`,
    `E2E second ${stamp}`,
  ]);

  // Pick the second one up and put it above the first.
  await page.getByRole("button", { name: `Move E2E second ${stamp}` }).click();
  await page
    .getByRole("button", { name: `Move here, above E2E first ${stamp}` })
    .click();
  await expectOurs([
    `E2E loose ${stamp}`,
    `E2E second ${stamp}`,
    `E2E first ${stamp}`,
  ]);

  // ...and file the loose one under the folder, which is the other half
  // of the gesture: a board's folder is a field on its own row.
  await page.getByRole("button", { name: `Move E2E loose ${stamp}` }).click();
  await page
    .getByRole("button", { name: `Move here, at the end of ${folder}` })
    .click();
  await expectOurs([
    `E2E second ${stamp}`,
    `E2E first ${stamp}`,
    `E2E loose ${stamp}`,
  ]);

  // The rows themselves say so, which is what an agent reading
  // tables/board.json would see.
  const res = await page.request.get("/api/boards");
  const boards = await res.json();
  const of = (id) => boards.find((b) => b.id === id);
  expect(of(second.id).orderKey).toBeLessThan(of(first.id).orderKey);
  expect(of(loose.id).folder).toBe(folder);
  expect(of(loose.id).orderKey).toBeGreaterThan(of(first.id).orderKey);
});

// The same move with a mouse, from a folder below the first section.
// Marking a drag draws a drop zone closing every section, and drawn
// inside dragstart it pushed every board under the first section down
// and out from under the pointer -- which Chromium answers by cancelling
// the drag on the spot (dragstart, then dragend, and no drop). jsdom has
// no such check, so this is the test that can see it.
test("files a board into another folder by dragging it", async ({ page }) => {
  const stamp = Date.now();
  const from = `E2E drag from ${stamp}`;
  const to = `E2E drag to ${stamp}`;
  const moved = await seedBoard(page, {
    name: `E2E dragged ${stamp}`,
    folder: from,
    kind: "list",
    query: "",
  });
  await seedBoard(page, {
    name: `E2E resident ${stamp}`,
    folder: to,
    kind: "list",
    query: "",
  });

  await page.goto("/boards");
  const handle = page.getByRole("button", {
    name: `Move E2E dragged ${stamp}`,
  });
  await expect(handle).toBeVisible();
  const heading = page.locator(".board-folder-heading", { hasText: to });

  const patched = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/boards/${moved.id}`) &&
      r.request().method() === "PATCH",
  );
  // By hand rather than dragTo: the drop zones a drag draws push the
  // folders below down by a row or two once it has started, so the
  // heading is aimed at where it is then -- as a person would -- rather
  // than where it was before the drag began.
  const from_ = await handle.boundingBox();
  await page.mouse.move(from_.x + from_.width / 2, from_.y + from_.height / 2);
  await page.mouse.down();
  await page.mouse.move(
    from_.x + from_.width / 2,
    from_.y + from_.height / 2 + 8,
    { steps: 3 },
  );
  // The drag took: had Chromium cancelled it, the zones would never be
  // drawn and the row would never dim.
  await expect(page.locator(".board-drop-end").first()).toBeAttached();
  await expect(page.locator(".task-row-dragging")).toHaveCount(1);
  const to_ = await heading.boundingBox();
  await page.mouse.move(to_.x + 40, to_.y + to_.height / 2, { steps: 8 });
  await page.mouse.up();
  expect((await patched).ok()).toBe(true);

  await expect
    .poll(
      async () => {
        const res = await page.request.get("/api/boards");
        return (await res.json()).find((b) => b.id === moved.id)?.folder;
      },
      { timeout: 20_000 },
    )
    .toBe(to);
});

// The whole of how a board is made: the button opens the new-task form
// with the task already written (boards.js), rather than a form that
// writes a board.
//
// The page's own button, scoped to .main-column rather than taken as the
// first "New board" on screen: the rail carries a second control with
// that accessible name -- its compact "+", which opens the Director with
// a board-building prompt instead (App.jsx's openNewBoardWithDirector) --
// and it comes first in the document, so `.first()` here was asserting
// the rail's behaviour under the page's name.
test("offers a new board as a task somebody files", async ({ page }) => {
  await page.goto("/boards");
  await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

  await newBoardButton(page).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel("Title")).toHaveValue("Add a board");
  await expect(page.getByLabel("Description")).toContainText(
    "tables/board.json",
  );
});

// A board whose kind is the timeline (grain/task-55). This is the third
// renderer a board can have, and the one that proves the extension point
// is real: the row carries a word, and what opens is the chart rather
// than a list of the same tasks.
test("opens a timeline board as the chart, narrowed to its own query", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E timeline ${stamp}`,
    kind: "gantt",
    description: "How long the running work has been running",
    query: "f=state:any:running",
  });

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `E2E timeline ${stamp}` }),
  ).toBeVisible();
  // The chip in the page's own header says which sort of dashboard this
  // is, in the word the nav rail uses for the same view.
  await expect(
    page.locator(".repo-page-header").getByText("Timeline", { exact: true }),
  ).toBeVisible();

  // The chart itself, asking the board's own question: the seeded
  // running task has a bar with no end on it, and the queued one the
  // query excludes is not drawn at all.
  const running = page.locator(".gantt-row", {
    hasText: "Bump the Go toolchain to 1.24",
  });
  await expect(running).toBeVisible();
  await expect(running.locator(".gantt-bar-ongoing")).toBeVisible();
  await expect(
    page.locator(".gantt-row", { hasText: "Fix flaky retry test" }),
  ).toHaveCount(0);
  await expect(page.locator(".gantt-tick").first()).toBeVisible();
});

// "New board" clicked while reading a timeline board asks for another
// timeline: the narrowing is only half of what is on screen, and the
// layout is the other half (boards.js's kindForView). A board page is
// the only view left that carries a layout, since the fixed board and
// timeline went off the rail (grain/task-162) -- which is also why the
// prompt goes on to name every kind there is, for the reader who wants
// one of the others.
test("offers a timeline board when the button is clicked from one", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E chart ${stamp}`,
    kind: "gantt",
    query: "",
  });

  await page.goto(`/boards/${board.id}`);
  await expect(page.locator(".gantt-tick").first()).toBeVisible();

  await newBoardButton(page).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  const description = page.getByLabel("Description");
  await expect(description).toContainText("kind: gantt");
  await expect(description).toContainText("kanban (columns of states");
});

// A dashboard of panels (grain/task-88): the one kind of board that is
// not a single view, and the one whose rows go all the way through --
// the panels are stored beside the board, come back with it, and are
// what the page draws.
test("opens a dashboard of panels, each narrowed to its own question", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E dashboard ${stamp}`,
    kind: "panels",
    description: "What is happening right now",
    panels: [
      { kind: "states", title: "Where everything is", width: "half" },
      {
        kind: "list",
        title: "Running now",
        query: "f=state:any:running",
        width: "half",
      },
      { kind: "gantt", title: "This week", range: "7d", width: "full" },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `E2E dashboard ${stamp}` }),
  ).toBeVisible();
  await expect(page.locator(".panel")).toHaveCount(3);

  // The list panel asks its own question: the seeded running task is in
  // it and the seeded queued one is not.
  const running = page.locator(".panel", { hasText: "Running now" });
  await expect(
    running.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
  ).toBeVisible();
  await expect(
    running.locator(".task-row", { hasText: "Fix flaky retry test" }),
  ).toHaveCount(0);

  // The counts, and the chart drawn as a panel rather than as a pane:
  // its axis is there, and the toolbar the full view carries is not.
  await expect(
    page
      .locator(".panel", { hasText: "Where everything is" })
      .locator(".panel-state")
      .first(),
  ).toBeVisible();
  const timeline = page.locator(".panel", { hasText: "This week" });
  await expect(timeline.locator(".gantt-tick").first()).toBeVisible();
  await expect(timeline.getByPlaceholder("Search tasks…")).toHaveCount(0);

  // A panel is one click from the view it is a small copy of, asking the
  // same question -- as a real URL, followed without a page load.
  await running.getByRole("link", { name: "Open", exact: true }).click();
  await expect(page).toHaveURL(/\/\?f=state:any:running/);
  await expect(
    page.locator(".task-row", { hasText: "Fix flaky retry test" }),
  ).toHaveCount(0);
});

// A panel whose saved query names an attribute this deployment does not
// have -- a typo in tables/board*.json, say -- is accepted by the API
// (model.Board.Validate does not read inside a query) and used to show
// every task with no word that its one condition had been dropped. The
// panel, and the list its "Open" link leads to, now both name it
// (grain/task-1421; docs/test-runs/2026-10-01-saved-boards-and-
// visualizations.md has the walkthrough that found it).
test("names a panel condition whose attribute does not exist", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E bad query ${stamp}`,
    kind: "panels",
    panels: [{ kind: "list", title: "bad", query: "f=nosuchattr:any:x" }],
  });

  await page.goto(`/boards/${board.id}`);
  const bad = page.locator(".panel", { hasText: "bad" });
  await expect(bad.locator(".panel-unknown-note")).toContainText(
    "Unknown condition: nosuchattr",
  );

  await bad.getByRole("link", { name: "Open" }).click();
  await expect(page).toHaveURL(/\/\?f=nosuchattr:any:x/);
  const chip = page.getByText("Unknown condition: nosuchattr");
  await expect(chip).toBeVisible();

  // The same list opened cold from its own URL says the same thing, and
  // deleting the chip takes the condition out of the address too.
  await page.goto("/?f=nosuchattr:any:x");
  await expect(chip).toBeVisible();
  await page
    .locator(".MuiChip-root", { hasText: "Unknown condition: nosuchattr" })
    .locator(".MuiChip-deleteIcon")
    .click();
  await expect(chip).toHaveCount(0);
  await expect(page).not.toHaveURL(/nosuchattr/);
});

// ...and a panel's place on that dashboard, which is the one edit to a
// dashboard the UI writes itself (grain/task-368). Worth a real browser
// for the reason the board reorder above is: the move is a PATCH against
// the rows an agent would otherwise be asked to edit, so this is the only
// place the gesture, the request, and what PutBoard and ListBoards do
// with the array afterwards are exercised together -- and PutBoard's own
// reading of an order key of 0 ("this row was not numbered") is exactly
// what panelOrder.js writes only positive keys to stay clear of.
//
// Driven through the handle's tap gesture rather than a drag, like the
// board reorder above: it is the same drop, and two clicks are
// deterministic where a synthesized HTML5 drag is a bet on the browser's
// own event sequence.
test("drags a panel into place on a dashboard", async ({ page }) => {
  const stamp = Date.now();
  // Seeded with no order keys at all, which is what a dashboard created
  // through POST /api/boards actually is: the store numbers them 0, 1, 2
  // on the way in, and the first panel's key is therefore a genuine
  // zero.
  const board = await seedBoard(page, {
    name: `E2E panel order ${stamp}`,
    kind: "panels",
    panels: [
      { kind: "states", title: `Where ${stamp}`, width: "half" },
      { kind: "list", title: `Running ${stamp}`, width: "half" },
      { kind: "gantt", title: `Week ${stamp}`, range: "7d", width: "full" },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `E2E panel order ${stamp}` }),
  ).toBeVisible();
  const headings = async () =>
    await page.locator(".panel-title").allTextContents();
  const expectHeadings = (names) =>
    expect.poll(headings, { timeout: 20_000 }).toEqual(names);
  await expectHeadings([`Where ${stamp}`, `Running ${stamp}`, `Week ${stamp}`]);

  // The last panel to the front. Zero is a genuine key once any sibling
  // is numbered, so the moved row steps below it and the store preserves
  // that ordering rather than interpreting zero as "unnumbered".
  await page
    .getByRole("button", { name: `Move panel 3, “Week ${stamp}”` })
    .click();
  await page
    .getByRole("button", {
      name: `Move here, above panel 1, “Where ${stamp}”`,
    })
    .click();
  await expectHeadings([`Week ${stamp}`, `Where ${stamp}`, `Running ${stamp}`]);

  // ...and then one step down, into a gap that has room to halve.
  await page
    .getByRole("button", { name: `Move panel 1, “Week ${stamp}”` })
    .click();
  await page
    .getByRole("button", {
      name: `Move here, above panel 3, “Running ${stamp}”`,
    })
    .click();
  await expectHeadings([`Where ${stamp}`, `Week ${stamp}`, `Running ${stamp}`]);

  // The rows themselves say so, which is what an agent reading
  // tables/board_panel.json would see: three keys, strictly ascending,
  // with the first row's genuine zero preserved by PutBoard.
  const res = await page.request.get("/api/boards");
  const stored = (await res.json()).find((b) => b.id === board.id);
  expect(stored.panels.map((p) => p.title)).toEqual([
    `Where ${stamp}`,
    `Week ${stamp}`,
    `Running ${stamp}`,
  ]);
  const keys = stored.panels.map((p) => p.orderKey);
  expect(keys[0]).toBe(0);
  expect(keys[1]).toBeGreaterThan(keys[0]);
  expect(keys[2]).toBeGreaterThan(keys[1]);
});

// Narrowing a dashboard on the spot (grain/task-111): the toolbar over
// the panels narrows what a panel with no question of its own inherits,
// and the address bar says so -- so the narrowed dashboard is a link,
// which is the half of this that only a real browser can show.
test("narrows a whole dashboard from the toolbar over its panels", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E narrowed dashboard ${stamp}`,
    kind: "panels",
    panels: [
      { kind: "list", title: "Everything", width: "half" },
      {
        kind: "list",
        title: "Running now",
        query: "f=state:any:running",
        width: "half",
      },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  const everything = page.locator(".panel", { hasText: "Everything" });
  await expect(everything.locator(".task-row").first()).toBeVisible();
  expect(await everything.locator(".task-row").count()).toBeGreaterThan(1);

  await page.getByPlaceholder("Search this dashboard…").fill("toolchain");

  // The panel that inherits follows the toolbar, down to the one task
  // whose title says it...
  await expect(
    everything.locator(".task-row", {
      hasText: "Bump the Go toolchain to 1.24",
    }),
  ).toBeVisible();
  await expect(everything.locator(".task-row")).toHaveCount(1);
  // ...while the panel that asks its own question goes on asking it,
  // seeded running task and all: narrowing that one by the toolbar too
  // would leave it showing nothing, since no running task's title
  // mentions the toolchain but the one above.
  await expect(
    page
      .locator(".panel", { hasText: "Running now" })
      .locator(".task-row", { hasText: "Add pagination to the tasks API" }),
  ).toBeVisible();
  // And the dashboard as narrowed is a URL somebody can send.
  await expect(page).toHaveURL(new RegExp(`/boards/${board.id}\\?q=toolchain`));
});

// The throughput panel is scoped by the question it is asking
// (grain/task-317), and that road only exists end to end: the panel
// builds the URL, the daemon is what narrows the rows before it
// measures anything, and a panel whose request the server would not
// answer says so instead of drawing numbers.
test("scopes a throughput panel to the repo its query names", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E scoped dashboard ${stamp}`,
    kind: "panels",
    panels: [
      {
        kind: "metrics",
        title: "Widgets throughput",
        query: "f=repo:any:acme/widgets",
        width: "half",
      },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  const panel = page.locator(".panel", { hasText: "Widgets throughput" });
  await expect(panel.locator(".panel-metric-count").first()).toBeVisible();
  // What the numbers are over, where a panel of two counts and two
  // sparklines has room to say it -- the line a report board prints
  // under its own header.
  await expect(panel.locator(".panel-metrics")).toHaveAttribute(
    "title",
    /Measured over the tasks in acme\/widgets\./,
  );

  // And its heading leads to that same report rather than to the whole
  // deployment's, which would answer a wider question than the panel it
  // was clicked on (panels.js's panelDestination). The pane takes a
  // scope of its own now (grain/task-379), so the link carries the
  // panel's question onto it and says so on the page.
  await panel.getByRole("link", { name: "Open", exact: true }).click();
  await expect(page).toHaveURL(/\/metrics\?f=repo:any:acme\/widgets/);
  await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();
  await expect(
    page.getByText(/Measured over the tasks in acme\/widgets/),
  ).toBeVisible();
});

// grain/task-309, and the reason it wants a real browser: a panel and
// the list its "Open" link opens have to be about the same tasks, and
// the two had drifted apart in the one case nobody was looking at. A
// dashboard whose board carries no query opened over every task, the
// archive included, while the list it linked to applied the
// deployment's own default -- so the number on a panel header was a
// count of tasks the link did not show, and on a deployment with real
// history most of them were tasks closed weeks ago. `grain demo` seeds
// one closed task and one deferred one, which is exactly the
// difference, and the panel row cap is why the count rather than the
// rows is what the two views are compared by.
test("hides the same tasks on a dashboard as in the list it links to", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E agreeing dashboard ${stamp}`,
    kind: "panels",
    panels: [{ kind: "list", title: "Everything", width: "full" }],
  });

  await page.goto(`/boards/${board.id}`);
  const panel = page.locator(".panel", { hasText: "Everything" });
  await expect(panel.locator(".task-row").first()).toBeVisible();
  // The set-aside states are out of the panel, exactly as they are out
  // of the list: this is the seeded closed task.
  await expect(
    panel.locator(".task-row", { hasText: "Spike: websocket transport" }),
  ).toHaveCount(0);
  const counted = Number(await panel.locator(".count").textContent());
  expect(counted).toBeGreaterThan(0);

  await panel.getByRole("link", { name: "Open", exact: true }).click();

  // The link says the whole of what the panel is showing, the default
  // condition included, so it reopens this list rather than one
  // narrowed by whatever the deployment's default has become since.
  await expect(page).toHaveURL(/\/\?f=state:none:deferred\|closed/);
  await expect(page.locator(".task-row")).toHaveCount(counted);
  await expect(
    page.locator(".task-row", { hasText: "Spike: websocket transport" }),
  ).toHaveCount(0);
});

// Changing a dashboard is a task, like every other change to a board.
test("offers a new panel as a task somebody files", async ({ page }) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E empty dashboard ${stamp}`,
    kind: "panels",
  });

  await page.goto(`/boards/${board.id}`);
  await expect(page.getByText(/no panels yet/)).toBeVisible();

  await page.getByRole("button", { name: "Add panel" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel("Title")).toHaveValue(
    `Add a panel to the dashboard "E2E empty dashboard ${stamp}"`,
  );
  await expect(page.getByLabel("Description")).toContainText(
    "tables/board_panel.json",
  );
});

// ...and the narrowing a reader arrives at by trying is kept the same
// way, on the panel they arrived at it on. Worth a real browser because
// the whole gesture is a round trip through the address bar and back:
// the toolbar narrows the dashboard, the panel that inherits shows the
// answer, and "Keep" has to carry that same query into the prompt --
// which is the step that used to be a paragraph typed from memory.
test("keeps a narrowing a reader made as one panel's own query", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E kept dashboard ${stamp}`,
    kind: "panels",
    panels: [
      { kind: "list", title: "Everything", width: "half", orderKey: 1024 },
      {
        kind: "list",
        title: "Running now",
        query: "f=state:any:running",
        width: "half",
        orderKey: 2048,
      },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  const everything = page.locator(".panel", { hasText: "Everything" });
  // Nothing to keep on a dashboard nobody has narrowed.
  await expect(everything.locator(".panel-keep")).toHaveCount(0);

  await page.getByPlaceholder("Search this dashboard…").fill("toolchain");
  // The panel showing the reader's narrowing offers to keep it; the one
  // asking its own question does not follow the toolbar, so it has
  // nothing of the reader's to keep.
  await expect(everything.locator(".panel-keep")).toBeVisible();
  await expect(
    page.locator(".panel", { hasText: "Running now" }).locator(".panel-keep"),
  ).toHaveCount(0);

  await everything.locator(".panel-keep").click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel("Title")).toHaveValue(
    `Narrow the panel "Everything" on the dashboard "E2E kept dashboard ${stamp}"`,
  );
  // The query the reader arrived at, and the row it is to be written
  // onto -- neither of them retyped by anybody.
  //
  // The whole of what is on screen, which on a dashboard that names no
  // query of its own is the deployment's default condition and the word
  // the reader typed over it (grain/task-309). Both, because a panel
  // carrying a query is read as written and gets no default: keeping
  // `q=toolchain` alone would keep a wider panel than the one being
  // pointed at.
  await expect(page.getByLabel("Description")).toContainText(
    "`q=toolchain&f=state:none:deferred|closed`",
  );
  await expect(page.getByLabel("Description")).toContainText(
    "`order_key` 1024",
  );
});

// A report board (grain/task-83): the one kind that is not a view of the
// backlog, and so the one whose whole road only exists end to end -- the
// sections stored beside the board, the query read as a scope, and the
// report the daemon computes under it. None of that can be checked
// without a real server, which is what this suite is for.
test("opens a report board over the sections it names", async ({ page }) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E report ${stamp}`,
    kind: "report",
    description: "How the demo deployment is going",
    query: "f=created:within:30",
    sections: [
      { kind: "throughput", title: "Last thirty days", orderKey: 0 },
      { kind: "latency", orderKey: 1 },
    ],
  });

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `E2E report ${stamp}` }),
  ).toBeVisible();
  await expect(
    page.locator(".repo-page-header").getByText("Report", { exact: true }),
  ).toBeVisible();

  // The two sections it names, with the heading the board gave one of
  // them -- and not the sections it did not name.
  await expect(page.getByText("Last thirty days")).toBeVisible();
  await expect(page.getByText("Tasks filed")).toBeVisible();
  await expect(page.getByText("Latency", { exact: true })).toBeVisible();
  await expect(page.getByText("Backlog", { exact: true })).toHaveCount(0);
});

// A report board with no sections of its own is the whole report, and
// the query is read as a scope: what a repo condition means here is
// which tasks the numbers are measured over, said on the page.
test("scopes a report board to the repo its query names", async ({ page }) => {
  const stamp = Date.now();
  const board = await seedBoard(page, {
    name: `E2E scoped report ${stamp}`,
    kind: "report",
    query: "f=repo:any:acme/widgets&f=state:any:running",
  });

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByText("Measured over the tasks in acme/widgets."),
  ).toBeVisible();
  // ...and the condition a report cannot use is named rather than
  // silently dropped, which is the honest half of reading a task list's
  // query as a scope.
  await expect(page.getByText(/not in force/)).toContainText("State");
  // No sections named, so the whole report is printed.
  await expect(page.getByText("Tasks filed")).toBeVisible();
  await expect(page.getByText("Backlog", { exact: true })).toBeVisible();
  // The demo has nothing to say in several of the sections, and each of
  // those is left out of the list a screen reader announces rather than
  // counted as an empty item in it.
  const sections = page.locator("ul.report-sections");
  const items = sections.getByRole("listitem");
  await expect(items.first()).toBeVisible();
  for (const text of await items.allInnerTexts()) {
    expect(text.trim()).not.toBe("");
  }
  expect(await sections.locator("> li").count()).toBeGreaterThan(
    await items.count(),
  );
});
