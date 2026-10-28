import { test, expect } from "./fixtures.js";

test.setTimeout(120_000);

const seededBoards = [];
const seededTasks = [];

async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const boards = await res.json();
  const board = boards.find((b) => b.name === name);
  expect(board, `no board called "${name}" in demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
  while (seededTasks.length > 0) {
    const id = seededTasks.pop();
    await page.request.delete(`/api/tasks/${id}`).catch(() => {});
  }
});

test.describe("Saved Boards and Visualizations Walkthrough", () => {
  test("1. Boards Index & Navigation: rail, direct URLs, folders, counts, legacy routes & error recovery", async ({
    page,
  }) => {
    // Navigate to root
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Verify Boards section in sidebar rail
    await expect(page.getByRole("button", { name: /^Boards/ })).toBeVisible();

    // Open Boards Index from rail
    await page.getByRole("button", { name: /^Boards/ }).click();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

    // Check count and seeded boards (Board, Timeline, Report)
    const boardCountBadge = page.locator(".content-header .count");
    await expect(boardCountBadge).toBeVisible();
    const count = Number(await boardCountBadge.textContent());
    expect(count).toBeGreaterThanOrEqual(3);

    await expect(
      page.locator(".template-name", { hasText: "Board" }),
    ).toBeVisible();
    await expect(
      page.locator(".template-name", { hasText: "Timeline" }),
    ).toBeVisible();
    await expect(
      page.locator(".template-name", { hasText: "Report" }),
    ).toBeVisible();

    // Verify lack of inappropriate search/sort input on boards index
    await expect(page.getByPlaceholder("Search tasks…")).toHaveCount(0);

    // Deep link cold into seeded Kanban board
    const kanbanUrl = await boardPath(page, "Board");
    await page.goto(kanbanUrl);
    await expect(page.locator(".repo-page-header")).toContainText("Board");
    await expect(page.locator(".board-column").first()).toBeVisible();

    // Deep link cold into seeded Timeline board
    const timelineUrl = await boardPath(page, "Timeline");
    await page.goto(timelineUrl);
    await expect(page.locator(".repo-page-header")).toContainText("Timeline");
    await expect(page.locator(".gantt-tick").first()).toBeVisible();

    // Deep link cold into seeded Report board
    const reportUrl = await boardPath(page, "Report");
    await page.goto(reportUrl);
    await expect(page.locator(".repo-page-header")).toContainText("Report");
    await expect(page.getByText("Tasks filed")).toBeVisible();

    // Test Back, Forward, and Reload navigation
    await page.goBack();
    await expect(page).toHaveURL(
      new RegExp(timelineUrl.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
    await page.goForward();
    await expect(page).toHaveURL(
      new RegExp(reportUrl.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
    await page.reload();
    await expect(page.locator(".repo-page-header")).toContainText("Report");

    // Test nonexistent board ID recovers gracefully to /boards index
    await page.goto("/boards/nonexistent-board-id-9999");
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

    // Test legacy routes redirect to /
    await page.goto("/board");
    await expect(page).toHaveURL(/\/$/);
    await expect(page.locator(".task-row").first()).toBeVisible();

    await page.goto("/gantt");
    await expect(page).toHaveURL(/\/$/);
    await expect(page.locator(".task-row").first()).toBeVisible();
  });

  test("2. Asynchronous Board Creation Workflow: file task for new board and inspect draft/contract", async ({
    page,
  }) => {
    await page.goto("/boards");
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();

    // Initial count
    const initialCount = Number(
      await page.locator(".content-header .count").textContent(),
    );

    // Click "New board" from the main column
    await page
      .getByTestId("page-header")
      .getByRole("button", { name: "New board" })
      .click();
    await expect(page).toHaveURL(/\/new/);
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    // Verify task title and tables/board.json description contract
    await expect(page.getByLabel("Title")).toHaveValue("Add a board");
    const desc = page.getByLabel("Description");
    await expect(desc).toContainText("tables/board.json");
    await expect(desc).toContainText("- kind: list");

    // Enter custom title
    const stamp = Date.now();
    const taskTitle = `Add Task 395 Exploratory Board ${stamp}`;
    await page.getByLabel("Title").fill(taskTitle);

    // Submit task
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // Verify count on /boards is STILL initialCount (asynchronous creation contract)
    const newCount = Number(
      await page.locator(".content-header .count").textContent(),
    );
    expect(newCount).toBe(initialCount);

    // Navigate to all tasks backlog and verify the newly filed task
    await page.goto("/");
    const createdTask = page.locator(".task-row", { hasText: taskTitle });
    await expect(createdTask).toBeVisible();

    // Open task detail and check description contract
    await createdTask.click();
    await expect(page.locator(".detail-header")).toContainText(taskTitle);
    await expect(page.getByText("tables/board.json")).toBeVisible();
    // Filed against the deployment's config repository, which the server
    // never parks (model.IsConfigRepo), so the task's page must not say it
    // is off the target repo list (2026-10-06 walkthrough). The pane's
    // picker offers it (targetableRepos), so it is the select's value
    // rather than typed into "Other…".
    // Asked of the pane's own Repos fieldset: the task list behind it can
    // carry another spec's off-list task (new-task-off-list-repo.spec.js),
    // whose row says the same words about its own repo.
    await expect(page.locator('[name="repo"]')).toHaveValue(/^grain\/config/);
    const repos = page.locator("fieldset", {
      has: page.locator('[name="repo"]'),
    });
    await expect(repos).toHaveCount(1);
    await expect(
      repos.getByText(/is not on this deployment's target repo list/),
    ).toHaveCount(0);
    await page.keyboard.press("Escape");
  });

  test("3. Folder organization, ordering, keyboard handles and reordering", async ({
    page,
  }) => {
    const stamp = Date.now();
    const folder = `Walkthrough Folder ${stamp}`;

    const b1 = await seedOne(page, "/api/boards", {
      name: `WT Alpha ${stamp}`,
      folder,
      kind: "list",
      query: "",
      orderKey: 1,
    });
    seededBoards.push(b1.id);

    const b2 = await seedOne(page, "/api/boards", {
      name: `WT Beta ${stamp}`,
      folder,
      kind: "kanban",
      query: "",
      orderKey: 2,
    });
    seededBoards.push(b2.id);

    const bLoose = await seedOne(page, "/api/boards", {
      name: `WT Loose ${stamp}`,
      kind: "list",
      query: "",
    });
    seededBoards.push(bLoose.id);

    await page.goto("/boards");
    await expect(
      page.locator(".board-folder-heading", { hasText: folder }),
    ).toBeVisible();

    const ours = async () =>
      (await page.locator(".template-name").allTextContents()).filter((n) =>
        n.endsWith(`${stamp}`),
      );
    const expectOurs = (names) =>
      expect.poll(ours, { timeout: 20_000 }).toEqual(names);

    await expectOurs([
      `WT Loose ${stamp}`,
      `WT Alpha ${stamp}`,
      `WT Beta ${stamp}`,
    ]);

    // Move WT Beta above WT Alpha using keyboard pickup/drop handle
    await page.getByRole("button", { name: `Move WT Beta ${stamp}` }).click();
    await page
      .getByRole("button", { name: `Move here, above WT Alpha ${stamp}` })
      .click();

    await expectOurs([
      `WT Loose ${stamp}`,
      `WT Beta ${stamp}`,
      `WT Alpha ${stamp}`,
    ]);

    // File WT Loose into folder
    await page.getByRole("button", { name: `Move WT Loose ${stamp}` }).click();
    await page
      .getByRole("button", { name: `Move here, at the end of ${folder}` })
      .click();

    await expectOurs([
      `WT Beta ${stamp}`,
      `WT Alpha ${stamp}`,
      `WT Loose ${stamp}`,
    ]);

    // Reload and verify persisted order
    await page.reload();
    await expectOurs([
      `WT Beta ${stamp}`,
      `WT Alpha ${stamp}`,
      `WT Loose ${stamp}`,
    ]);

    // Reorder persists on page
    await expectOurs([
      `WT Beta ${stamp}`,
      `WT Alpha ${stamp}`,
      `WT Loose ${stamp}`,
    ]);
  });

  test("3b. A board moved into or out of a folder from the keyboard keeps focus, and unordered boards keep filing order", async ({
    page,
  }) => {
    // 2026-10-12 walkthrough: a board put down in another folder is a
    // new row under another heading, so focus fell to <body> once the
    // boards came back; and boards nobody had ordered came back sorted
    // by their ids as text, board-10 above board-9.
    const stamp = Date.now();
    const folder = `WT Focus Folder ${stamp}`;
    const names = [];
    for (let i = 1; i <= 3; i++) {
      const b = await seedOne(page, "/api/boards", {
        name: `WT Filed ${i} ${stamp}`,
        folder,
        kind: "list",
      });
      seededBoards.push(b.id);
      names.push(b.name);
    }
    const loose = await seedOne(page, "/api/boards", {
      name: `WT Mover ${stamp}`,
      kind: "list",
    });
    seededBoards.push(loose.id);

    await page.goto("/boards");
    const ours = async () =>
      (await page.locator(".template-name").allTextContents()).filter(
        (n) => n.startsWith("WT Filed ") && n.endsWith(`${stamp}`),
      );
    await expect.poll(ours).toEqual(names);

    const handle = page.getByRole("button", { name: `Move WT Mover ${stamp}` });
    await handle.press("Enter");
    await page
      .getByRole("button", { name: `Move here, at the end of ${folder}` })
      .press("Enter");
    await expect
      .poll(async () => {
        const boards = await (await page.request.get("/api/boards")).json();
        return boards.find((b) => b.id === loose.id)?.folder ?? "";
      })
      .toBe(folder);
    await expect(handle).toBeFocused();

    await handle.press("Enter");
    await page
      .getByRole("button", { name: "Move here, outside every folder" })
      .press("Enter");
    await expect
      .poll(async () => {
        const boards = await (await page.request.get("/api/boards")).json();
        return boards.find((b) => b.id === loose.id)?.folder ?? "";
      })
      .toBe("");
    await expect(handle).toBeFocused();
  });

  test("4. List and Kanban presentations: task details, live query narrowing, column editor, card reordering", async ({
    page,
  }) => {
    const stamp = Date.now();

    // 1. Scoped list board
    const listBoard = await seedOne(page, "/api/boards", {
      name: `WT List ${stamp}`,
      kind: "list",
      query: "f=state:any:running",
    });
    seededBoards.push(listBoard.id);

    await page.goto(`/boards/${listBoard.id}`);
    await expect(
      page.getByRole("heading", { name: `WT List ${stamp}` }),
    ).toBeVisible();
    await expect(
      page.locator(".repo-page-header").getByText("List", { exact: true }),
    ).toBeVisible();

    // Verify task row displayed and open task detail overlay
    const runningRow = page.locator(".task-row", {
      hasText: "Bump the Go toolchain to 1.24",
    });
    await expect(runningRow).toBeVisible();
    await runningRow.click();
    await expect(page.locator(".detail-header")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator(".detail-header")).toHaveCount(0);

    // Update query via live search bar
    await page.getByPlaceholder("Search tasks…").fill("pagination");
    await expect(page).toHaveURL(
      new RegExp(`/boards/${listBoard.id}\\?.*q=pagination`),
    );
    await expect(
      page.locator(".task-row", { hasText: "Add pagination to the tasks API" }),
    ).toBeVisible();
    await expect(
      page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
    ).toHaveCount(0);

    // Reload and confirm query persistence
    await page.reload();
    await expect(
      page.locator(".task-row", { hasText: "Add pagination to the tasks API" }),
    ).toBeVisible();

    // 2. Kanban board with fixed column definition from DB
    const fixedKanbanBoard = await seedOne(page, "/api/boards", {
      name: `WT Fixed Kanban ${stamp}`,
      kind: "kanban",
      query: "",
      columns: [{ name: "In Flight Only", states: ["running"] }],
    });
    seededBoards.push(fixedKanbanBoard.id);

    await page.goto(`/boards/${fixedKanbanBoard.id}`);
    await expect(
      page.getByRole("heading", { name: `WT Fixed Kanban ${stamp}` }),
    ).toBeVisible();
    const fixedCol = page.locator(".board-column", {
      hasText: "In Flight Only",
    });
    await expect(fixedCol).toBeVisible();
    await expect(
      fixedCol.locator(".board-card", {
        hasText: "Bump the Go toolchain to 1.24",
      }),
    ).toBeVisible();
    // Fixed column kanban does not expose the Columns edit button
    await expect(
      page.getByRole("button", { name: "Columns", exact: true }),
    ).toHaveCount(0);

    // 3. Unfixed Kanban board (seeded "Board") with user-editable columns
    await page.goto(await boardPath(page, "Board"));
    await page.getByRole("button", { name: "Columns", exact: true }).click();
    await expect(page.getByText("Board columns")).toBeVisible();
    await page.getByRole("button", { name: "+ Add column" }).click();
    const newTitleInput = page.getByLabel(/Column \d+ title/).last();
    await newTitleInput.fill("Archive");
    await page.getByLabel("States").last().click();
    await page.getByRole("option", { name: "Closed" }).click();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Save" }).click();

    // Verify Archive column appears with closed task card
    const archiveCol = page.locator(".board-column", { hasText: "Archive" });
    await expect(
      archiveCol.locator(".board-card", {
        hasText: "Spike: websocket transport",
      }),
    ).toBeVisible({ timeout: 20_000 });

    // Reload and verify layout persistence
    await page.reload();
    await expect(
      page
        .locator(".board-column", { hasText: "Archive" })
        .locator(".board-card", {
          hasText: "Spike: websocket transport",
        }),
    ).toBeVisible({ timeout: 20_000 });

    // Clean up columns back to default
    await page.getByRole("button", { name: "Columns", exact: true }).click();
    await page.getByRole("button", { name: "Reset to default" }).click();
    await page.getByRole("button", { name: "Save" }).click();
  });

  test("5. Timeline and Gantt presentations: range, lanes, tick markers, task bars, detail link, persistence", async ({
    page,
  }) => {
    const stamp = Date.now();
    const ganttBoard = await seedOne(page, "/api/boards", {
      name: `WT Timeline ${stamp}`,
      kind: "gantt",
      query: "",
    });
    seededBoards.push(ganttBoard.id);

    await page.goto(`/boards/${ganttBoard.id}`);
    await expect(
      page.getByRole("heading", { name: `WT Timeline ${stamp}` }),
    ).toBeVisible();
    await expect(
      page.locator(".repo-page-header").getByText("Timeline", { exact: true }),
    ).toBeVisible();

    // Ongoing task bar with pulsing indicator
    const runningRow = page.locator(".gantt-row", {
      hasText: "Bump the Go toolchain to 1.24",
    });
    await expect(runningRow).toBeVisible();
    await expect(runningRow.locator(".gantt-bar-ongoing")).toBeVisible();

    // Axis ticks
    await expect(page.locator(".gantt-tick").first()).toBeVisible();

    // Out of range note on default 7d
    await expect(page.getByText(/outside this range/)).toBeVisible();

    // Switch range to "Everything"
    await page.getByRole("combobox", { name: "Range" }).click();
    await page.getByRole("option", { name: "Everything" }).click();

    // Closed task bar visible
    const closedRow = page.locator(".gantt-row", {
      hasText: "Spike: websocket transport",
    });
    await expect(closedRow.locator(".gantt-bar")).toBeVisible();
    await expect(closedRow.locator(".gantt-bar-ongoing")).toHaveCount(0);

    // Switch Lanes to "By state"
    await page.getByRole("combobox", { name: "Lanes" }).click();
    await page.getByRole("option", { name: "By state" }).click();
    await expect(page.locator(".gantt-lane-title").first()).toBeVisible();

    // Open task detail overlay from Gantt bar click
    await runningRow.click();
    await expect(page.locator(".detail-header")).toContainText(
      "Bump the Go toolchain to 1.24",
    );
    await page.keyboard.press("Escape");
    await expect(page.locator(".detail-header")).toHaveCount(0);

    // Reload and verify persisted range and lanes
    await page.reload();
    await expect(page.getByRole("combobox", { name: "Range" })).toHaveText(
      "Everything",
    );
    await expect(page.getByRole("combobox", { name: "Lanes" })).toHaveText(
      "By state",
    );

    // Restore defaults
    await page.getByRole("combobox", { name: "Range" }).click();
    await page.getByRole("option", { name: "Last 7 days" }).click();
    await page.getByRole("combobox", { name: "Lanes" }).click();
    await page.getByRole("option", { name: "No lanes" }).click();
  });

  test("6. Dashboard (Panels) presentation: multi-panel, scoping, whole-board narrowing, keep, panel reordering, empty state", async ({
    page,
  }) => {
    const stamp = Date.now();
    const dashBoard = await seedOne(page, "/api/boards", {
      name: `WT Dashboard ${stamp}`,
      kind: "panels",
      description: "Full visualization panel test",
      panels: [
        { kind: "states", title: `States ${stamp}`, width: "half" },
        {
          kind: "list",
          title: `Inheriting List ${stamp}`,
          width: "half",
          orderKey: 100,
        },
        {
          kind: "list",
          title: `Scoped Running ${stamp}`,
          query: "f=state:any:running",
          width: "half",
          orderKey: 200,
        },
        {
          kind: "metrics",
          title: `Metrics ${stamp}`,
          query: "f=repo:any:acme/widgets",
          width: "half",
          orderKey: 300,
        },
        {
          kind: "gantt",
          title: `Timeline ${stamp}`,
          range: "7d",
          width: "full",
          orderKey: 400,
        },
      ],
    });
    seededBoards.push(dashBoard.id);

    await page.goto(`/boards/${dashBoard.id}`);
    await expect(
      page.getByRole("heading", { name: `WT Dashboard ${stamp}` }),
    ).toBeVisible();
    await expect(page.locator(".panel")).toHaveCount(5);

    // Verify States panel
    const statesPanel = page.locator(".panel", { hasText: `States ${stamp}` });
    await expect(statesPanel.locator(".panel-state").first()).toBeVisible();

    // Verify Metrics panel sparklines and scoping title
    const metricsPanel = page.locator(".panel", {
      hasText: `Metrics ${stamp}`,
    });
    await expect(
      metricsPanel.locator(".panel-metric-count").first(),
    ).toBeVisible();
    await expect(metricsPanel.locator(".panel-metrics")).toHaveAttribute(
      "title",
      /Measured over the tasks in acme\/widgets\./,
    );

    // Test whole-board narrowing
    const inheritingPanel = page.locator(".panel", {
      hasText: `Inheriting List ${stamp}`,
    });
    const scopedPanel = page.locator(".panel", {
      hasText: `Scoped Running ${stamp}`,
    });

    await page.getByPlaceholder("Search this dashboard…").fill("toolchain");

    // Inheriting panel filters down to toolchain task
    await expect(
      inheritingPanel.locator(".task-row", {
        hasText: "Bump the Go toolchain to 1.24",
      }),
    ).toBeVisible();
    await expect(inheritingPanel.locator(".task-row")).toHaveCount(1);

    // Scoped panel retains its own running tasks
    await expect(
      scopedPanel.locator(".task-row", {
        hasText: "Add pagination to the tasks API",
      }),
    ).toBeVisible();

    // Verify "Keep" button appears on inheriting panel and opens task creation with query & order_key
    await expect(inheritingPanel.locator(".panel-keep")).toBeVisible();
    await expect(scopedPanel.locator(".panel-keep")).toHaveCount(0);

    await inheritingPanel.locator(".panel-keep").click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await expect(page.getByLabel("Title")).toHaveValue(
      `Narrow the panel "Inheriting List ${stamp}" on the dashboard "WT Dashboard ${stamp}"`,
    );
    await expect(page.getByLabel("Description")).toContainText(
      "tables/board_panel.json",
    );
    await expect(page.getByLabel("Description")).toContainText(
      "`order_key` 100",
    );
    await page.keyboard.press("Escape");

    // Test Panel "Open" and state tile modified clicks + normal navigation
    await page.goto(`/boards/${dashBoard.id}`);
    const openMetrics = page.locator(".panel", { hasText: `Metrics ${stamp}` });
    await page.evaluate(() => {
      window.__panelLinkPrevented = null;
      document.addEventListener(
        "click",
        (e) => {
          const link = e.target.closest("a.panel-open, a.panel-state");
          if (link && (e.ctrlKey || e.metaKey || e.shiftKey)) {
            window.__panelLinkPrevented = e.defaultPrevented;
            e.preventDefault();
          }
        },
        false,
      );
    });
    await openMetrics
      .getByRole("link", { name: "Open", exact: true })
      .click({ modifiers: ["Shift"] });
    await expect(page).toHaveURL(new RegExp(`/boards/${dashBoard.id}`));
    expect(await page.evaluate(() => window.__panelLinkPrevented)).toBe(false);

    await page
      .locator(".panel-state", { hasText: "Running" })
      .click({ modifiers: ["Shift"] });
    await expect(page).toHaveURL(new RegExp(`/boards/${dashBoard.id}`));
    expect(await page.evaluate(() => window.__panelLinkPrevented)).toBe(false);

    await openMetrics.getByRole("link", { name: "Open", exact: true }).click();
    await expect(page).toHaveURL(/\/metrics\?f=repo:any:acme\/widgets/);
    await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();

    // Test Panel Reordering using pickup/drop handle
    await page.goto(`/boards/${dashBoard.id}`);
    const panelTitles = async () =>
      await page.locator(".panel-title").allTextContents();
    const expectTitles = (titles) =>
      expect.poll(panelTitles, { timeout: 20_000 }).toEqual(titles);

    await expectTitles([
      `States ${stamp}`,
      `Inheriting List ${stamp}`,
      `Scoped Running ${stamp}`,
      `Metrics ${stamp}`,
      `Timeline ${stamp}`,
    ]);

    // Move Timeline panel to top
    await page
      .getByRole("button", { name: `Move panel 5, “Timeline ${stamp}”` })
      .click();
    await page
      .getByRole("button", {
        name: `Move here, above panel 1, “States ${stamp}”`,
      })
      .click();

    await expectTitles([
      `Timeline ${stamp}`,
      `States ${stamp}`,
      `Inheriting List ${stamp}`,
      `Scoped Running ${stamp}`,
      `Metrics ${stamp}`,
    ]);

    // Reload and verify layout persistence
    await page.reload();
    await expectTitles([
      `Timeline ${stamp}`,
      `States ${stamp}`,
      `Inheriting List ${stamp}`,
      `Scoped Running ${stamp}`,
      `Metrics ${stamp}`,
    ]);

    // Empty Dashboard State
    const emptyBoard = await seedOne(page, "/api/boards", {
      name: `WT Empty Dash ${stamp}`,
      kind: "panels",
    });
    seededBoards.push(emptyBoard.id);

    await page.goto(`/boards/${emptyBoard.id}`);
    await expect(page.getByText(/no panels yet/)).toBeVisible();
    await page.getByRole("button", { name: "Add panel" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await expect(page.getByLabel("Title")).toHaveValue(
      `Add a panel to the dashboard "WT Empty Dash ${stamp}"`,
    );
    await expect(page.getByLabel("Description")).toContainText(
      "tables/board_panel.json",
    );
    await page.keyboard.press("Escape");
  });

  test("7. Report board presentation: sections, scoping notices, reordering, data links", async ({
    page,
  }) => {
    const stamp = Date.now();

    // Report board with specific sections and scope
    const reportBoard = await seedOne(page, "/api/boards", {
      name: `WT Report ${stamp}`,
      kind: "report",
      description: "Scoped metric report",
      query: "f=repo:any:acme/widgets&f=state:any:running",
      sections: [
        { kind: "throughput", title: `Throughput ${stamp}`, orderKey: 10 },
        { kind: "latency", title: `Latency ${stamp}`, orderKey: 20 },
      ],
    });
    seededBoards.push(reportBoard.id);

    await page.goto(`/boards/${reportBoard.id}`);
    await expect(
      page.getByRole("heading", { name: `WT Report ${stamp}` }),
    ).toBeVisible();
    await expect(
      page.locator(".repo-page-header").getByText("Report", { exact: true }),
    ).toBeVisible();

    // Verify scope notice: repo condition in force, state condition noted as not in force
    await expect(
      page.getByText("Measured over the tasks in acme/widgets."),
    ).toBeVisible();
    await expect(page.getByText(/not in force/)).toContainText("State");

    // Verify specified sections are displayed
    await expect(page.getByText(`Throughput ${stamp}`)).toBeVisible();
    await expect(page.getByText(`Latency ${stamp}`)).toBeVisible();
    // Backlog section was not included
    await expect(page.getByText("Backlog", { exact: true })).toHaveCount(0);

    // Section reordering using keyboard pickup handles
    await page
      .getByRole("button", { name: `Move section 2, “Latency ${stamp}”` })
      .click();
    await page
      .getByRole("button", {
        name: `Move here, above section 1, “Throughput ${stamp}”`,
      })
      .click();

    // Check API persistence of reordered sections
    const res = await page.request.get("/api/boards");
    const stored = (await res.json()).find((b) => b.id === reportBoard.id);
    expect(stored.sections.map((s) => s.kind)).toEqual([
      "latency",
      "throughput",
    ]);

    // Reload and verify order
    await page.reload();
    const firstSection = page.locator(".report-section").first();
    await expect(firstSection).toContainText(`Latency ${stamp}`);
  });

  test("6b. Dashboard throughput panel prints the totals /api/metrics returns", async ({
    page,
  }) => {
    // grain/task-1356: the panel read throughput.filed/.completed, which
    // only a bucket carries; the totals are tasksFiled/tasksCompleted, so
    // every real dashboard printed "0 filed" beside a non-empty sparkline.
    const stamp = Date.now();
    const dash = await seedOne(page, "/api/boards", {
      name: `WT Metrics ${stamp}`,
      kind: "panels",
      panels: [
        {
          kind: "metrics",
          title: `Widgets ${stamp}`,
          query: "f=repo:any:acme/widgets",
          range: "7d",
        },
      ],
    });
    seededBoards.push(dash.id);

    const metrics = page.waitForResponse(
      (r) => r.url().includes("/api/metrics?") && r.ok(),
    );
    await page.goto(`/boards/${dash.id}`);
    const { throughput } = await (await metrics).json();
    expect(throughput.tasksFiled).toBeGreaterThan(0);

    const counts = page.locator(".panel-metric-count");
    await expect(counts.nth(0)).toHaveText(String(throughput.tasksFiled));
    await expect(counts.nth(1)).toHaveText(String(throughput.tasksCompleted));
  });

  test("6d. A panel or section moved from the keyboard keeps focus, and a cut list panel says so", async ({
    page,
  }) => {
    // 2026-10-04 walkthrough: panels and sections were rendered by their
    // position, so after a keyboard move the focused handle stayed where
    // the panel had been, relabelled as the panel that moved into it.
    const stamp = Date.now();
    const dash = await seedOne(page, "/api/boards", {
      name: `WT Focus ${stamp}`,
      kind: "panels",
      panels: [
        { kind: "states", title: `Counts ${stamp}`, orderKey: 1 },
        {
          kind: "list",
          title: `Everything ${stamp}`,
          query: "sort=created:desc",
          orderKey: 2,
        },
        { kind: "gantt", title: `Week ${stamp}`, range: "7d", orderKey: 3 },
      ],
    });
    seededBoards.push(dash.id);
    await page.goto(`/boards/${dash.id}`);

    // Every task there is, closed ones included, is more than eight.
    const everything = page
      .locator(".panel")
      .filter({ hasText: `Everything ${stamp}` });
    await expect(everything.locator(".panel-list-more")).toHaveText(
      /^Showing 8 of \d+\.$/,
    );

    await page
      .getByRole("button", { name: `Move panel 3, “Week ${stamp}”` })
      .press("Enter");
    await page
      .getByRole("button", {
        name: `Move here, above panel 1, “Counts ${stamp}”`,
      })
      .press("Enter");
    const moved = page.getByRole("button", {
      name: `Move panel 1, “Week ${stamp}”`,
    });
    await expect(moved).toBeVisible();
    await expect(moved).toBeFocused();
    await page.reload();
    await expect(page.locator(".panel-title").first()).toHaveText(
      new RegExp(`week ${stamp}`, "i"),
    );

    const report = await seedOne(page, "/api/boards", {
      name: `WT Focus Report ${stamp}`,
      kind: "report",
      sections: [
        { kind: "throughput", orderKey: 1 },
        { kind: "latency", orderKey: 2 },
        { kind: "backlog", orderKey: 3 },
      ],
    });
    seededBoards.push(report.id);
    await page.goto(`/boards/${report.id}`);
    await page
      .getByRole("button", { name: "Move section 3, “Backlog”" })
      .press("Enter");
    await page
      .getByRole("button", { name: "Move here, above section 1, “Throughput”" })
      .press("Enter");
    const section = page.getByRole("button", {
      name: "Move section 1, “Backlog”",
    });
    await expect(section).toBeVisible();
    await expect(section).toBeFocused();
  });

  test("6b2. A report section's handle names the heading drawn under it", async ({
    page,
  }) => {
    // 2026-10-07 walkthrough: the "runs" section's handle read "Attempts"
    // over a body headed "Capacity", so the handle named a section the
    // page's heading outline does not have.
    const stamp = Date.now();
    const report = await seedOne(page, "/api/boards", {
      name: `WT Capacity Report ${stamp}`,
      kind: "report",
      sections: [
        { kind: "throughput", orderKey: 1 },
        { kind: "runs", orderKey: 2 },
      ],
    });
    seededBoards.push(report.id);
    await page.goto(`/boards/${report.id}`);
    await expect(
      page.getByRole("heading", { level: 3, name: "Capacity" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Move section 2, “Capacity”" }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: /Attempts/ })).toHaveCount(0);
  });

  test("6c. A panel or link with a comparison grain does not know says so", async ({
    page,
  }) => {
    // grain/task-1531: `f=state:bogus:running` was parsed away before
    // anything could name it, so the panel showed every task without a
    // word and the task list rewrote the link to `/`.
    const stamp = Date.now();
    const dash = await seedOne(page, "/api/boards", {
      name: `WT Bad Op ${stamp}`,
      kind: "panels",
      panels: [
        {
          kind: "list",
          title: `Bad op ${stamp}`,
          query: "f=state:bogus:running",
        },
      ],
    });
    seededBoards.push(dash.id);

    await page.goto(`/boards/${dash.id}`);
    const panel = page.locator(".panel", { hasText: `Bad op ${stamp}` });
    await expect(panel.locator(".panel-unknown-note")).toContainText(
      "Unknown condition: state:bogus",
    );

    await page.goto("/?f=state:bogus:running");
    await expect(
      page
        .locator(".narrowing-chips")
        .getByText("Unknown condition: state:bogus"),
    ).toBeVisible();
    await expect(page).toHaveURL(/\?f=state:bogus:running$/);
  });

  test("7b. Report board accounts for every section it names, scheduling included", async ({
    page,
  }) => {
    // grain/task-1356: "cycles" had no renderer, so it vanished, and a
    // named section with no data (the demo has no merges and no daemon
    // ticks) left its title over an empty space.
    const stamp = Date.now();
    const reportBoard = await seedOne(page, "/api/boards", {
      name: `WT Sections ${stamp}`,
      kind: "report",
      sections: [
        { kind: "throughput" },
        { kind: "merges", title: `Landed ${stamp}` },
        { kind: "cycles" },
      ],
    });
    seededBoards.push(reportBoard.id);

    await page.goto(`/boards/${reportBoard.id}`);
    await expect(page.locator(".report-section")).toHaveCount(3);
    await expect(page.getByText("Tasks filed")).toBeVisible();
    await expect(
      page.getByText("Merge queue: nothing to report over this window."),
    ).toBeVisible();
    await expect(
      page.getByText("Scheduling: nothing to report over this window."),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Move section 3, “Scheduling”" }),
    ).toBeVisible();
  });

  test("7c. A report board's picked window is in its URL and survives a reload", async ({
    page,
  }) => {
    // grain/task-2393 (2026-10-06 walkthrough, finding 3): a board held
    // its pick in component state only, so a reload, Back from a task or
    // a shared link put the report back on the last 7 days.
    const stamp = Date.now();
    const reportBoard = await seedOne(page, "/api/boards", {
      name: `WT Window ${stamp}`,
      kind: "report",
      sections: [{ kind: "throughput" }],
    });
    seededBoards.push(reportBoard.id);
    const windowPicker = page.getByRole("combobox", { name: "Window" });
    const boardUrl = `/boards/${reportBoard.id}?f=repo:any:acme/widgets`;

    await page.goto(boardUrl);
    await expect(windowPicker).toHaveText("Last 7 days");

    const picked = page.waitForRequest((r) =>
      r.url().includes("/api/metrics?"),
    );
    await windowPicker.click();
    await page.getByRole("option", { name: "Last 30 days" }).click();
    expect((await picked).url()).toContain("window=30d");
    await expect(page).toHaveURL(
      new RegExp(
        `/boards/${reportBoard.id}\\?f=repo:any:acme/widgets&window=30d$`,
      ),
    );

    const reloaded = page.waitForRequest((r) =>
      r.url().includes("/api/metrics?"),
    );
    await page.reload();
    expect((await reloaded).url()).toContain("window=30d");
    await expect(windowPicker).toHaveText("Last 30 days");

    // The board's own default clears the pick from the address.
    await windowPicker.click();
    await page.getByRole("option", { name: "Last 7 days" }).click();
    await expect(page).toHaveURL(
      new RegExp(`/boards/${reportBoard.id}\\?f=repo:any:acme/widgets$`),
    );
  });

  test("7c2. Edit board and New board on a report say which window it is being read over", async ({
    page,
  }) => {
    // 2026-10-13 walkthrough: the Window picker is the address bar's
    // `window=`, not the board's query, and both prompts dropped it.
    await page.goto(await boardPath(page, "Report"));
    const windowPicker = page.getByRole("combobox", { name: "Window" });
    await expect(windowPicker).toHaveText("Last 7 days");
    await windowPicker.click();
    await page.getByRole("option", { name: "Last 90 days" }).click();
    await expect(page).toHaveURL(/window=90d$/);

    const prompt = page.getByRole("dialog").locator("textarea").first();
    for (const button of ["Edit board", "New board"]) {
      await page.getByRole("button", { name: button }).click();
      await expect(prompt).toHaveValue(/over the last 90 days/);
      await expect(prompt).toHaveValue(/`f=created:within:90`/);
      await page.goBack();
      await expect(page).toHaveURL(/window=90d$/);
      await expect(windowPicker).toHaveText("Last 90 days");
    }
  });

  test("7d. A report board is scoped to a repo from its own filter editor", async ({
    page,
  }) => {
    // 2026-10-11 walkthrough: the report's toolbar was handed no tasks,
    // so "Repo" offered "No values to choose from" and the scope the
    // report is most often asked under could only be typed into the URL.
    const stamp = Date.now();
    const reportBoard = await seedOne(page, "/api/boards", {
      name: `WT Scope ${stamp}`,
      kind: "report",
      sections: [{ kind: "throughput" }],
    });
    seededBoards.push(reportBoard.id);

    await page.goto(`/boards/${reportBoard.id}`);
    await expect(page.getByRole("combobox", { name: "Window" })).toBeVisible();
    await page
      .getByTitle("Add and edit the conditions this list is narrowed by")
      .click();
    await page
      .getByRole("button", { name: /^(Filter by…|Add filter)$/ })
      .click();
    await page.getByRole("menuitem", { name: "Repo" }).click();
    await page
      .getByLabel(/^Value for filter \d+$/)
      .last()
      .click();

    const scoped = page.waitForRequest((r) =>
      r.url().includes("/api/metrics?repo=acme%2Fwidgets"),
    );
    await page.getByRole("option", { name: "acme/widgets" }).click();
    await scoped;
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/f=repo:any:acme\/widgets/);
    await expect(
      page.getByText("Measured over the tasks in acme/widgets."),
    ).toBeVisible();
  });

  test("5b. A Tree board is narrowed by its query and its own search, and a modified click opens a new tab", async ({
    page,
  }) => {
    // grain/task-1983: the tree pane drew the whole backlog whatever the
    // board's query or the search said, and had no narrowing bar.
    const treeUrl = await boardPath(page, "Tree");
    const shown = () =>
      page
        .locator(".task-tree-board li")
        .evaluateAll((lis) => lis.map((li) => li.dataset.taskId));

    await page.goto(`${treeUrl}?q=pagination`);
    await expect(page.getByRole("main")).toBeVisible();
    await expect.poll(shown).toEqual(["8", "11"]);
    // 11 was filed by 8, so it is drawn under it.
    await expect(
      page.locator(
        '.task-tree-board li[data-task-id="8"] li[data-task-id="11"]',
      ),
    ).toBeVisible();
    await page.reload();
    await expect.poll(shown).toEqual(["8", "11"]);

    const search = page.getByPlaceholder("Search tasks…");
    await expect(search).toHaveValue("pagination");
    await search.fill("zzzznothing");
    await expect(page).toHaveURL(/q=zzzznothing/);
    await expect(
      page.getByText("No tasks match this search and these filters."),
    ).toBeVisible();
    await page.getByRole("button", { name: "Clear", exact: true }).click();
    await expect.poll(async () => (await shown()).length).toBeGreaterThan(2);

    const link = page.locator(
      '.task-tree-board li[data-task-id="8"] > .task-tree-row a.task-tree-id',
    );
    // Whether the app took a modified click, read after React has seen it.
    // A new tab is the browser's business and headless builds differ on
    // opening one, so the click is then cancelled here and what is
    // asserted is only that the app left it alone.
    await page.evaluate(() => {
      window.__treeClicks = [];
      document.addEventListener("click", (e) => {
        if (!e.ctrlKey && !e.metaKey) return;
        window.__treeClicks.push(e.defaultPrevented);
        e.preventDefault();
      });
    });
    await link.click({ modifiers: ["ControlOrMeta"] });
    await expect
      .poll(() => page.evaluate(() => window.__treeClicks))
      .toEqual([false]);
    await expect(page).toHaveURL(new RegExp(`${treeUrl}$`));
    await link.click();
    await expect(page).toHaveURL(/\/tasks\/8$/);
  });

  test("4b. Back from a reloaded task returns to the board it was opened from", async ({
    page,
  }) => {
    // grain/task-1356: a reload of /tasks/N rewrote the entry to "/" and
    // pushed /tasks/N again, so Back skipped the board; Forward onto a
    // task pushed an extra entry too.
    const boardUrl = await boardPath(page, "Board");
    await page.goto(boardUrl);
    await page
      .locator(".board-card", { hasText: "Fix flaky retry test" })
      .click();
    await expect(page).toHaveURL(/\/tasks\/2$/);
    const length = await page.evaluate(() => history.length);

    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`${boardUrl}$`));
    await page.goForward();
    await expect(page.locator(".detail-header")).toBeVisible();
    expect(await page.evaluate(() => history.length)).toBe(length);

    await page.reload();
    await expect(page.locator(".detail-header")).toBeVisible();
    await expect(page).toHaveURL(/\/tasks\/2$/);
    expect(await page.evaluate(() => history.length)).toBe(length);
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`${boardUrl}$`));
    await expect(page.locator(".board-column").first()).toBeVisible();
  });

  test("4c. The Kanban column editor keeps keyboard focus through moves to either end, removals and additions", async ({
    page,
  }) => {
    // 2026-10-13 walkthrough: moving a column to the first or last slot
    // disabled the arrow under focus, and Remove unmounted it, so focus
    // fell to the dialog and a keyboard reader lost their column.
    await page.goto(await boardPath(page, "Board"));
    await expect(page.locator(".board-column").first()).toBeVisible();
    await page.getByRole("button", { name: "Columns", exact: true }).click();
    const editor = page.getByRole("dialog");
    await expect(
      editor.getByRole("heading", { name: "Board columns" }),
    ).toBeVisible();

    await editor
      .getByRole("button", { name: "Move Waiting left" })
      .press("Enter");
    await expect(
      editor.getByRole("button", { name: "Move Waiting left" }),
    ).toBeDisabled();
    await expect(
      editor.getByRole("button", { name: "Move Waiting right" }),
    ).toBeFocused();

    const removes = editor.getByRole("button", { name: /^Remove / });
    const last = await removes.last().getAttribute("aria-label");
    const beforeLast = await removes.nth(-2).getAttribute("aria-label");
    await removes.last().press("Enter");
    await expect(editor.getByRole("button", { name: last })).toHaveCount(0);
    await expect(
      editor.getByRole("button", { name: beforeLast }),
    ).toBeFocused();

    await editor.getByRole("button", { name: "+ Add column" }).press("Enter");
    const titles = editor.getByRole("textbox", { name: /^Column \d+ title$/ });
    await expect(titles.last()).toHaveValue("New column");
    await expect(titles.last()).toBeFocused();
  });

  test("5c. A task opened by clicking a timeline bar or a card off its title hands focus back to that row's link", async ({
    page,
  }) => {
    // 2026-10-15 walkthrough: a bar or a card's body is no control, so
    // the task pane opened from it gave focus back to <body> on Escape.
    await page.goto(await boardPath(page, "Timeline"));
    const bar = page.locator(".gantt-bar").first();
    await expect(bar).toBeVisible();
    const row = page.locator(".gantt-row").first();
    await bar.click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/boards\/board-\d+$/);
    await expect(row.locator(".row-link")).toBeFocused();

    await page.goto(await boardPath(page, "Board"));
    const card = page.locator(".board-card").first();
    await expect(card).toBeVisible();
    const box = await card.boundingBox();
    await page.mouse.click(box.x + box.width - 6, box.y + box.height - 6);
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await page.keyboard.press("Escape");
    await expect(card.locator(".row-link")).toBeFocused();

    // Selecting a card on a Kanban board and narrowing it out warns in the
    // batch actions bar that the selection is not shown on screen.
    await card.getByRole("checkbox").check();
    await expect(page.getByText("1 selected")).toBeVisible();
    await page.getByPlaceholder("Search tasks…").fill("zzzznothing");
    await expect(page.getByText("none of them shown here")).toBeVisible();
    await page.getByRole("button", { name: "Clear", exact: true }).click();
    await page.getByRole("button", { name: "Clear selection" }).click();

    await page.goto(await boardPath(page, "Tree"));
    const treeRow = page
      .locator('.task-tree-board li[data-task-id="8"] > .task-tree-row')
      .first();
    await expect(treeRow).toBeVisible();
    await treeRow.locator(".task-tree-title").click();
    await expect(page).toHaveURL(/\/tasks\/8$/);
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/boards\/board-\d+$/);
    await expect(treeRow.locator("a.task-tree-id")).toBeFocused();

    // 2026-10-22 walkthrough: opening a board on /boards by clicking its
    // row off the title link, or opening a task inside the board before
    // returning via "← Boards", still returns focus to that board's link.
    await page.goto("/boards");
    const boardRow = page
      .locator(".template-row", { hasText: "Board" })
      .first();
    await boardRow.locator(".chips").click();
    await expect(page).toHaveURL(/\/boards\/board-\d+$/);
    await page.getByRole("button", { name: "← Boards" }).click();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(
      page.getByRole("link", { name: "Board", exact: true }),
    ).toBeFocused();

    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/boards\/board-\d+$/);
    await page.locator(".board-card a.row-link").first().click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/boards\/board-\d+$/);
    await page.getByRole("button", { name: "← Boards" }).click();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(
      page.getByRole("link", { name: "Board", exact: true }),
    ).toBeFocused();
  });

  test("2b. Edit board says what a Kanban board's own columns are, and New board points only at notes it carries", async ({
    page,
  }) => {
    // 2026-10-03 walkthrough: a board carrying its own columns offers no
    // column editor, so "Edit board" is the only way to change them --
    // and its prompt left them out. New board's menu of kinds sent a
    // list board's agent "below" to panels and report notes it lacked.
    const stamp = Date.now();
    const kanban = await seedOne(page, "/api/boards", {
      name: `WT Own Columns ${stamp}`,
      kind: "kanban",
      columns: [
        { name: "Waiting", states: ["queued", "proposed"] },
        { name: "Going", states: ["running"] },
      ],
    });
    seededBoards.push(kanban.id);

    await page.goto(`/boards/${kanban.id}`);
    await expect(
      page.locator(".board-column", { hasText: "Going" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Columns", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "Edit board" }).click();
    const desc = page.getByLabel("Description");
    await expect(desc).toContainText("Its columns, left to right:");
    await expect(desc).toContainText('1. "Waiting": `queued`, `proposed`');
    await expect(desc).toContainText('2. "Going": `running`');

    await page.goto("/?q=pagination");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page.getByRole("button", { name: /create options/i }).click();
    await page.getByRole("menuitem", { name: /board/i }).click();
    await expect(desc).toContainText("- kind: list");
    await expect(desc).not.toContainText("see the panels section below");
    await expect(desc).not.toContainText("see the report section below");
  });

  test("8. Deletion safety and throwaway cleanup", async ({ page }) => {
    // Verify that the UI does NOT render arbitrary board deletion buttons,
    // adhering to the state repository configuration architecture.
    await page.goto("/boards");
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
    await expect(
      page.getByRole("button", { name: /Delete board/i }),
    ).toHaveCount(0);
  });

  test("9. Wave 2b edge cases: un-sectioned report board, single-card column handles, and deep 10-level task trees on phone", async ({
    page,
  }) => {
    // 1. Seeded Report board (board-3) names no sections: it prints the whole
    // report without dead Move section handles or empty-section placeholders,
    // and supports custom created:within:N day windows in its Window control.
    await page.goto("/boards/board-3");
    await expect(
      page.getByRole("heading", { level: 3, name: "Throughput" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^Move section / }),
    ).toHaveCount(0);
    await expect(
      page.getByText(/nothing to report over this window/),
    ).toHaveCount(0);

    await page.goto("/boards/board-3?f=created:within:14");
    await expect(page.getByLabel("Window")).toContainText("Last 14 days");
    await expect(page.getByText(/not in force/)).toHaveCount(0);

    // Control /api/tasks for steps 2 and 3 so earlier specs in the same
    // worker that created proposed/queued tasks on the shared demoServer
    // cannot perturb column card counts.
    await page.route("**/api/tasks**", async (route) => {
      const now = new Date().toISOString();
      const tasks = [
        {
          id: "900",
          title: "Proposed single card",
          state: "proposed",
          priority: 0,
          repo: "acme/widgets",
          createdAt: now,
        },
        {
          id: "900-q1",
          title: "Queued first card",
          state: "queued",
          priority: 0,
          repo: "acme/widgets",
          createdAt: now,
        },
        {
          id: "900-q2",
          title: "Queued second card",
          state: "queued",
          priority: 1,
          repo: "acme/widgets",
          createdAt: now,
        },
        {
          id: "900-m1",
          title: "Rotate the Gemini signing key",
          state: "completed",
          priority: 0,
          repo: "acme/widgets",
          createdAt: now,
        },
      ];
      let prev = "900";
      for (let d = 1; d <= 10; d += 1) {
        const id = String(900 + d);
        tasks.push({
          id,
          title: `Generation ${d} verification follow-up task`,
          state: "awaiting_reply",
          repo: "acme/widgets",
          generatedFrom: prev,
          createdAt: now,
        });
        prev = id;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(tasks),
      });
    });

    // 2. Seeded Kanban board (board-1): a column with a single card disables
    // its move handle, a column with 2+ cards enables its handles, and
    // picking up a card then narrowing it out of view cancels the pickup.
    await page.goto("/boards/board-1");
    const proposedCol = page.locator(".board-column").filter({
      has: page.locator(".board-column-title", { hasText: /^Proposed$/ }),
    });
    await expect(proposedCol.locator(".board-card")).toHaveCount(1);
    await expect(proposedCol.locator(".task-drag-handle")).toBeDisabled();

    const queuedCol = page.locator(".board-column").filter({
      has: page.locator(".board-column-title", { hasText: /^Waiting$/ }),
    });
    await expect(queuedCol.locator(".board-card")).toHaveCount(2);
    await expect(
      queuedCol.locator(".task-drag-handle").first(),
    ).not.toBeDisabled();
    await queuedCol.locator(".task-drag-handle").first().click();
    await expect(page.getByText(/Moving 1 task/)).toBeVisible();

    await page
      .getByPlaceholder("Search tasks…")
      .fill("Rotate the Gemini signing key");
    await expect(page.getByText(/Moving 1 task/)).toHaveCount(0);
    const mergeCol = page.locator(".board-column", {
      hasText: "Merging",
    });
    await expect(mergeCol.locator(".board-card")).toHaveCount(1);
    await expect(mergeCol.locator(".task-drag-handle")).toBeDisabled();

    // 3. Deep 10-generation task tree on a 375x812 phone viewport keeps task
    // titles readable without page-level horizontal overflow.
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto("/boards/board-4");
    const deepestRow = page.locator('li[data-task-id="910"] > .task-tree-row');
    await expect(deepestRow).toBeVisible();
    const layout = await page.evaluate(() => {
      const titleEl = document.querySelector(
        'li[data-task-id="910"] > .task-tree-row .task-tree-title',
      );
      return {
        docScrollWidth: document.documentElement.scrollWidth,
        docClientWidth: document.documentElement.clientWidth,
        titleWidth: titleEl?.getBoundingClientRect().width ?? 0,
      };
    });
    expect(layout.docScrollWidth).toBeLessThanOrEqual(layout.docClientWidth);
    expect(layout.titleWidth).toBeGreaterThanOrEqual(90);
    await page.unroute("**/api/tasks**");
  });
});
