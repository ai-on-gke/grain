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

import { expect } from "@playwright/test";
import { test } from "./fixtures.js";

// Comprehensive E2E Walkthrough: Interactive Task Intake, Board Views,
// Multi-Team Filtering, and Saved Board State Persistence across Session Reloads.

test.setTimeout(120_000);

const COLD_LOAD_MS = 20_000;

async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

test.describe("Interactive Task Intake, Board Views and Multi-Team Filter Workflows", () => {
  let createdTaskIds = [];
  let createdBoardIds = [];

  test.afterEach(async ({ request }) => {
    // Clean up created tasks
    for (const id of createdTaskIds) {
      try {
        await request.delete(`/api/tasks/${id}`);
      } catch (_) {}
    }
    createdTaskIds = [];

    // Clean up created boards
    for (const id of createdBoardIds) {
      try {
        await request.delete(`/api/boards/${id}`);
      } catch (_) {}
    }
    createdBoardIds = [];
  });

  test("1. Interactive task intake: form validation, rich custom fields, draft management, and detail overlay", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const stamp = Date.now();
    const taskTitle = `Interactive Intake Feature ${stamp}`;
    const taskDesc = `Automated validation of interactive custom fields and task intake for run ${stamp}`;

    // Open the "+ New task" modal
    await page.getByRole("button", { name: "+ New task" }).click();
    const newDialog = page.getByRole("heading", { name: "New task" });
    await expect(newDialog).toBeVisible();

    // Verify Create task button is disabled when title or repo is missing
    const createBtn = page.getByRole("button", { name: "Create task" });
    await expect(createBtn).toBeDisabled();

    // Select Repository: choose 'none' ("No repo")
    await page.getByLabel(/^Repos/).selectOption("none");

    // Test draft input and discard draft
    await page.getByLabel("Title").fill("Temporary Draft Title");
    await expect(createBtn).toBeEnabled();

    const discardBtn = page.getByRole("button", { name: "Discard draft" });
    await expect(discardBtn).toBeVisible();
    await discardBtn.click();
    await expect(page.getByLabel("Title")).toHaveValue("");
    await expect(createBtn).toBeDisabled();
    // Re-select repo after draft discard
    await page.getByLabel(/^Repos/).selectOption("none");

    // Fill Title and Description
    await page.getByLabel("Title").fill(taskTitle);
    await page.getByLabel("Description").fill(taskDesc);
    await expect(createBtn).toBeEnabled();

    // Verify Custom Fields block is rendered with declared demo fields
    await expect(page.getByText("Fields", { exact: true })).toBeVisible();

    // Set Priority enum to 'high'
    await page.getByLabel("Priority", { exact: true }).click();
    await page.getByRole("option", { name: "high" }).click();

    // Set Area multi-select enum: select 'frontend' and 'backend'
    for (const area of ["frontend", "backend"]) {
      await page.getByLabel("Area").click();
      await page.getByRole("option", { name: area }).click();
    }

    // Set Requested by text field
    await page.getByLabel("Requested by").fill("Platform Engineering");

    // Set Estimate (days) numeric field
    await page.getByLabel("Estimate (days)").fill("4");

    // Set Customer-facing boolean field to 'Yes'
    await page.getByLabel("Customer-facing").click();
    await page.getByRole("option", { name: "Yes", exact: true }).click();

    // Open Advanced options accordion
    const advSummary = page.getByText("Advanced options");
    await advSummary.click();

    // Fill Prompt extension override
    const promptExt = page.getByLabel("Prompt extension override");
    await expect(promptExt).toBeVisible();
    await promptExt.fill(
      "Ensure all unit tests pass and documentation is updated.",
    );

    // Submit the task creation
    await createBtn.click();
    await expect(newDialog).toHaveCount(0);

    // Verify task row renders in the table list view with the correct title
    const row = page.locator(".task-row", { hasText: taskTitle });
    await expect(row).toBeVisible();

    // Verify row state badge and custom field chip (Priority: high)
    await expect(row.locator(".badge-queued")).toBeVisible();
    await expect(row.locator(".chip-field")).toHaveText("Priorityhigh");

    // Extract task ID from the row for tracking and cleanup
    const taskIdText = await row.locator(".task-number").textContent();
    const taskIdMatch = taskIdText?.match(/(\d+)/);
    if (taskIdMatch) {
      createdTaskIds.push(taskIdMatch[1]);
    }

    // Open task detail overlay by clicking the row
    await row.click();
    await expect(
      page.locator(".detail-header h2", { hasText: taskTitle }),
    ).toBeVisible();

    // Verify all declared custom fields in the detail overlay
    const declaredRows = page.locator(".declared-row");
    await expect(declaredRows.filter({ hasText: "Priority" })).toContainText(
      "high",
    );
    await expect(declaredRows.filter({ hasText: "Area" })).toContainText(
      "backend, frontend",
    );
    await expect(
      declaredRows.filter({ hasText: "Requested by" }),
    ).toContainText("Platform Engineering");
    await expect(
      declaredRows.filter({ hasText: "Estimate (days)" }),
    ).toContainText("4");
    await expect(
      declaredRows.filter({ hasText: "Customer-facing" }),
    ).toContainText("Yes");

    // Close detail overlay
    await page.keyboard.press("Escape");
    await expect(page.locator(".detail-header")).toHaveCount(0);
  });

  test("2. Board views: Table/List view navigation, Kanban column card distribution, and column layout customization", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();

    // Seed tasks in distinct states to verify Kanban column distribution
    const queuedTaskRes = await request.post("/api/tasks", {
      data: {
        title: `Kanban Queued Task ${stamp}`,
        description: "Task queued for worker",
        approved: true,
        customFields: { priority: "high" },
      },
    });
    expect(queuedTaskRes.ok()).toBeTruthy();
    const queuedTask = await queuedTaskRes.json();
    createdTaskIds.push(queuedTask.id);

    const proposedTaskRes = await request.post("/api/tasks", {
      data: {
        title: `Kanban Proposed Task ${stamp}`,
        description: "Task proposed awaiting approval",
        proposed: true,
        customFields: { priority: "medium" },
      },
    });
    expect(proposedTaskRes.ok()).toBeTruthy();
    const proposedTask = await proposedTaskRes.json();
    createdTaskIds.push(proposedTask.id);

    // Navigate to default task list (Table view)
    await page.goto("/");
    await expect(
      page.locator(".task-row", { hasText: queuedTask.title }),
    ).toBeVisible();

    // Test Search filtering in Table view
    const searchInput = page.getByPlaceholder(/Search tasks/i);
    await expect(searchInput).toBeVisible();
    await searchInput.fill(queuedTask.title);
    await expect(
      page.locator(".task-row", { hasText: queuedTask.title }),
    ).toBeVisible();
    await expect(
      page.locator(".task-row", { hasText: proposedTask.title }),
    ).toHaveCount(0);
    await searchInput.fill("");

    // Navigate to the seeded Kanban Board
    const kanbanPath = await boardPath(page, "Board");
    await page.goto(kanbanPath);
    await expect(page.locator(".repo-page-header")).toContainText("Board");

    // Verify Kanban columns are rendered
    await expect(page.locator(".board-column").first()).toBeVisible();

    const queuedColumn = page.locator(".board-column").filter({
      has: page.locator(".board-column-title", { hasText: /^Waiting$/ }),
    });
    await expect(queuedColumn).toBeVisible();
    await expect(
      queuedColumn.locator(".board-card", { hasText: queuedTask.title }),
    ).toBeVisible();

    const proposedColumn = page.locator(".board-column").filter({
      has: page.locator(".board-column-title", { hasText: /^Proposed$/ }),
    });
    await expect(proposedColumn).toBeVisible();
    await expect(
      proposedColumn.locator(".board-card", { hasText: proposedTask.title }),
    ).toBeVisible();

    // Customize Kanban columns via "Columns" button
    const columnsBtn = page.getByRole("button", {
      name: "Columns",
      exact: true,
    });
    await columnsBtn.click();
    await expect(page.getByText("Board columns")).toBeVisible();

    // Add a new column "Completed Archive" mapped to "Closed" state
    await page.getByRole("button", { name: "+ Add column" }).click();
    const colTitleInput = page.getByLabel(/Column \d+ title/).last();
    await colTitleInput.fill("Completed Archive");
    await page.getByLabel("States").last().click();
    await page.getByRole("option", { name: "Closed" }).click();
    await page.keyboard.press("Escape");

    // Save column changes
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Board columns")).toHaveCount(0);

    // Verify the newly added column appears on the Kanban board
    const completedCol = page.locator(".board-column", {
      hasText: "Completed Archive",
    });
    await expect(completedCol).toBeVisible();

    // Reset columns to default
    await columnsBtn.click();
    await page.getByRole("button", { name: "Reset to default" }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.locator(".board-column", { hasText: "Completed Archive" }),
    ).toHaveCount(0);
  });

  test("4. Saved board state persistence: custom Kanban board queries, column layout, and session reload resilience", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const boardTitle = `High Priority Ops Board ${stamp}`;

    // Create high-priority and normal-priority tasks
    const highTaskTitle = `High Priority Outage Investigation ${stamp}`;
    const lowTaskTitle = `Low Priority Routine Maintenance ${stamp}`;

    const highTaskRes = await request.post("/api/tasks", {
      data: {
        title: highTaskTitle,
        description: "Urgent investigation task",
        approved: true,
        customFields: { priority: "high" },
      },
    });
    expect(highTaskRes.ok()).toBeTruthy();
    const highTask = await highTaskRes.json();
    createdTaskIds.push(highTask.id);

    const lowTaskRes = await request.post("/api/tasks", {
      data: {
        title: lowTaskTitle,
        description: "Low priority maintenance task",
        approved: true,
        customFields: { priority: "low" },
      },
    });
    expect(lowTaskRes.ok()).toBeTruthy();
    const lowTask = await lowTaskRes.json();
    createdTaskIds.push(lowTask.id);

    // Create a custom saved Kanban board via API
    const createBoardRes = await request.post("/api/boards", {
      data: {
        name: boardTitle,
        kind: "kanban",
        folder: "Operations",
        description: "Board dedicated to high priority items",
        query: "f=custom.priority:any:high",
        columns: [
          { name: "High Priority Queued", states: ["queued"] },
          {
            name: "High Priority Active",
            states: ["running", "awaiting_reply"],
          },
        ],
      },
    });
    expect(createBoardRes.ok(), await createBoardRes.text()).toBeTruthy();
    const board = await createBoardRes.json();
    createdBoardIds.push(board.id);

    // Deep link directly to the newly saved board
    await page.goto(`/boards/${board.id}`);
    await expect(page.locator(".repo-page-header")).toContainText(boardTitle);

    // Verify custom column titles
    const highQueuedCol = page.locator(".board-column", {
      hasText: "High Priority Queued",
    });
    const highActiveCol = page.locator(".board-column", {
      hasText: "High Priority Active",
    });

    await expect(highQueuedCol).toBeVisible({ timeout: COLD_LOAD_MS });
    await expect(highActiveCol).toBeVisible({ timeout: COLD_LOAD_MS });

    // Verify high priority task card is visible and low priority task card is excluded
    await expect(
      highQueuedCol.locator(".board-card", { hasText: highTaskTitle }),
    ).toBeVisible();
    await expect(
      page.locator(".board-card", { hasText: lowTaskTitle }),
    ).toHaveCount(0);

    // Execute full browser page reload (session reload test)
    await page.reload();

    // Verify state persistence across session reload: header, columns, and task cards remain intact
    await expect(page.locator(".repo-page-header")).toContainText(boardTitle);
    await expect(
      page
        .locator(".board-column", { hasText: "High Priority Queued" })
        .locator(".board-card", { hasText: highTaskTitle }),
    ).toBeVisible({ timeout: COLD_LOAD_MS });
    await expect(
      page.locator(".board-card", { hasText: lowTaskTitle }),
    ).toHaveCount(0);

    // Navigate to /boards index and back via UI
    await page.goto("/boards");
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
    const savedBoardRow = page.locator(".template-row", {
      hasText: boardTitle,
    });
    await expect(savedBoardRow).toBeVisible();
    await savedBoardRow.click();

    // Verify navigation back to the board successfully renders the persisted layout
    await expect(page.locator(".repo-page-header")).toContainText(boardTitle);
    await expect(
      page
        .locator(".board-column", { hasText: "High Priority Queued" })
        .locator(".board-card", { hasText: highTaskTitle }),
    ).toBeVisible({ timeout: COLD_LOAD_MS });
  });
});
