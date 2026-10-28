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

import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { signInContext } from "./accesscode.js";
import { test, expect } from "./fixtures.js";

test.setTimeout(90_000);

/**
 * Invokes `grain mcpserver` in interactive mode against the live server
 * and calls the `propose_task` MCP tool over JSON-RPC stdio.
 */
async function callProposeTaskMCP({
  url,
  code,
  parentTaskId,
  sandboxRoot,
  args,
}) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  await fs.mkdir(sandboxRoot, { recursive: true });
  const rpcInput =
    [
      JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "initialize",
        params: {
          protocolVersion: "2024-11-05",
          capabilities: {},
          clientInfo: { name: "e2e-walkthrough", version: "1.0.0" },
        },
      }),
      JSON.stringify({
        jsonrpc: "2.0",
        id: 2,
        method: "tools/call",
        params: {
          name: "propose_task",
          arguments: args,
        },
      }),
    ].join("\n") + "\n";

  const stdout = execFileSync(
    bin,
    [
      "mcpserver",
      "-sandbox-root",
      sandboxRoot,
      "-server",
      url,
      "-task",
      String(parentTaskId),
      "-interactive",
    ],
    {
      input: rpcInput,
      env: { ...process.env, GRAIN_TOKEN: code },
      encoding: "utf8",
      timeout: 15_000,
    },
  );

  const lines = stdout
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => JSON.parse(l));
  const callResp = lines.find((msg) => msg.id === 2);
  expect(
    callResp,
    `Expected JSON-RPC response for id=2 in: ${stdout}`,
  ).toBeTruthy();
  expect(callResp.result?.isError).toBeFalsy();
  return callResp.result?.content?.[0]?.text || "";
}

test("Task provenance disclosure, deferred approval/supersedes, and board/list/tree interactions (§6)", async ({
  page,
  browser,
  demoServer,
  accessCode,
}) => {
  const root = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-provenance-e2e-"),
  );

  try {
    await page.setViewportSize({ width: 1280, height: 800 });

    const apiPost = async (pathname, data) => {
      const res = await page.request.post(pathname, { data });
      expect(
        res.ok(),
        `POST ${pathname} failed (${res.status()}): ${await res.text()}`,
      ).toBe(true);
      return res.json();
    };

    const apiGet = async (pathname) => {
      const res = await page.request.get(pathname);
      expect(res.ok(), `GET ${pathname} failed: ${res.status()}`).toBe(true);
      return res.json();
    };

    // Create a standing interactive chat session to serve as the parent for live `propose_task`.
    const parentChat = await apiPost("/api/tasks", {
      title: "Walkthrough planning chat",
      body: "Standing interactive session for filing proposals.",
      repo: "acme/widgets",
      interactive: true,
    });
    const parentChatId = String(parentChat.id);

    // -------------------------------------------------------------------------
    // Step 1: Deferred task in-place approval & propose_task supersedes
    // -------------------------------------------------------------------------
    const deferredIso = "2099-05-01T09:00:00Z";
    const t1Created = await apiPost("/api/tasks", {
      title: "Deferred schema rollout T1",
      body: "Initial deferred proposal with reads and writes declarations.",
      noRepo: true,
      approved: false,
      deferredUntil: deferredIso,
      reads: ["acme/docs"],
      writes: [{ repo: "acme/widgets", base: "main" }],
    });
    const t1Id = String(t1Created.id);

    // Open /tasks/<T1> and verify unapproved deferred state.
    await page.goto(`/tasks/${t1Id}`);
    const overlay = page.getByRole("dialog");
    await expect(overlay).toBeVisible();

    const actionsBar = overlay.locator(".actions");
    await expect(overlay.locator(".detail-state .badge-deferred")).toHaveText(
      "Deferred",
    );
    const approveBtn = actionsBar.getByRole("button", {
      name: "Approve",
      exact: true,
    });
    await expect(approveBtn).toBeVisible();
    await expect(actionsBar).toContainText(
      "back into the proposals for someone to approve.",
    );

    // Click Approve in Actions: assert approved becomes true while state: "deferred" and deferredUntil stay intact.
    await approveBtn.click();
    const withdrawBtn = actionsBar.getByRole("button", {
      name: "Withdraw approval",
      exact: true,
    });
    await expect(withdrawBtn).toBeVisible();
    await expect(actionsBar).toContainText("onto the queue.");
    await expect(overlay.locator(".detail-state .badge-deferred")).toHaveText(
      "Deferred",
    );

    let t1Detail = await apiGet(`/api/tasks/${t1Id}`);
    expect(t1Detail.state).toBe("deferred");
    expect(t1Detail.approved).toBe(true);
    expect(t1Detail.deferredUntil).toBe(deferredIso);

    // Click Withdraw approval: assert approved returns to false without clearing deferredUntil.
    await withdrawBtn.click();
    await expect(approveBtn).toBeVisible();
    await expect(actionsBar).toContainText(
      "back into the proposals for someone to approve.",
    );

    t1Detail = await apiGet(`/api/tasks/${t1Id}`);
    expect(t1Detail.state).toBe("deferred");
    expect(Boolean(t1Detail.approved)).toBe(false);
    expect(t1Detail.deferredUntil).toBe(deferredIso);

    // Call live `propose_task` with `supersedes: "<T1>"` via `grain mcpserver`.
    const proposalText = await callProposeTaskMCP({
      url: demoServer,
      code: accessCode,
      parentTaskId: parentChatId,
      sandboxRoot: path.join(root, "mcp-sandbox"),
      args: {
        title: "Refined schema rollout T2 (canary-batch)",
        body: "Replacement proposal superseding T1.",
        no_repo: true,
        supersedes: `#${t1Id}`,
        reads: ["acme/docs"],
        writes: [{ repo: "acme/widgets", base: "main" }],
      },
    });

    // Extract T2 ID from the relative `/tasks/<T2>` link in the tool output.
    const match = proposalText.match(/\/tasks\/(\d+)/);
    expect(
      match,
      `Expected relative /tasks/<id> in: ${proposalText}`,
    ).toBeTruthy();
    const t2Id = match[1];
    expect(proposalText).toContain(
      `Proposed task #${t2Id} is visible now: /tasks/${t2Id}.`,
    );
    expect(proposalText).not.toContain("http://127.0.0.1");
    expect(proposalText).not.toContain("http://localhost");
    expect(proposalText).toContain(
      `Task ${t1Id} is closed as superseded by it.`,
    );

    t1Detail = await apiGet(`/api/tasks/${t1Id}`);
    const t2Detail = await apiGet(`/api/tasks/${t2Id}`);
    expect(t1Detail.state).toBe("closed");
    expect(t1Detail.supersededBy).toBe(t2Id);
    expect(t2Detail.supersedes).toBe(t1Id);
    expect(t2Detail.generatedFrom).toBe(parentChatId);
    expect(t2Detail.origin).toBe(`task:${parentChatId}`);

    // Verify SupersedeRefusal refuses to close an already-closed task and notes why in the body.
    const refusalText = await callProposeTaskMCP({
      url: demoServer,
      code: accessCode,
      parentTaskId: parentChatId,
      sandboxRoot: path.join(root, "mcp-sandbox-refusal"),
      args: {
        title: "Follow-up task T3 (canary-batch)",
        body: "Attempts to supersede T1 after T1 is already closed.",
        no_repo: true,
        supersedes: t1Id,
        depends_on: [t2Id],
        reads: ["acme/docs"],
      },
    });
    expect(refusalText).toContain(
      `Task ${t1Id} is still open: grain would not close it`,
    );
    const t3Match = refusalText.match(/\/tasks\/(\d+)/);
    expect(t3Match).toBeTruthy();
    const t3Detail = await apiGet(`/api/tasks/${t3Match[1]}`);
    expect(t3Detail.description).toContain(`is already closed`);

    // Seed two more backlog tasks (T4, T5) with `reads: ["acme/docs"]` so we have 4 contiguous
    // tasks on the scoped board.
    await apiPost("/api/tasks", {
      title: "Backlog migration task T4",
      body: "Fourth task for range selection.",
      noRepo: true,
      approved: false,
      dependsOn: [t2Id],
      reads: ["acme/docs"],
    });

    await apiPost("/api/tasks", {
      title: "Backlog cleanup task T5",
      body: "Fifth task for range selection.",
      noRepo: true,
      approved: false,
      reads: ["acme/docs"],
    });

    // -------------------------------------------------------------------------
    // Step 2: .provenance-toggle collapse, expand, reset, and coarse-pointer touch target
    // -------------------------------------------------------------------------
    // First check the seeded task with a PR ("Add pagination to the tasks API") to verify
    // that PR link and age are directly visible while .provenance-toggle is collapsed.
    const allTasksBody = await apiGet("/api/tasks");
    const allTasks = Array.isArray(allTasksBody)
      ? allTasksBody
      : allTasksBody.tasks;
    const paginationTask = allTasks.find(
      (t) => t.title === "Add pagination to the tasks API",
    );
    expect(paginationTask).toBeTruthy();

    await page.goto(`/tasks/${paginationTask.id}`);
    await expect(overlay).toBeVisible();
    const freshness = overlay.locator(".freshness");
    const provToggle = freshness.locator(".provenance-toggle");
    await expect(provToggle).toBeVisible();
    await expect(provToggle).toHaveAttribute("aria-expanded", "false");
    await expect(freshness).toContainText("as of");
    const prLink = freshness.locator(
      'a[href="https://github.com/acme/widgets/pull/104"]',
    );
    await expect(prLink).toBeVisible();
    await expect(prLink).toHaveText("acme/widgets#104");
    await expect(freshness.locator(".provenance-panel")).toHaveCount(0);

    // Now open /tasks/<T2> and verify .provenance-toggle starts collapsed by default.
    await page.goto(`/tasks/${t2Id}`);
    await expect(overlay).toBeVisible();
    await expect(provToggle).toBeVisible();
    await expect(provToggle).toHaveAttribute("aria-expanded", "false");
    await expect(freshness).toContainText("as of");
    await expect(freshness.locator(".provenance-panel")).toHaveCount(0);

    // Verify coarse-pointer touch target (>= 40px height) on 390x844 phone viewport.
    const phoneContext = await browser.newContext({
      baseURL: demoServer,
      viewport: { width: 390, height: 844 },
      hasTouch: true,
      isMobile: true,
    });
    await signInContext(phoneContext, demoServer, accessCode);
    const phonePage = await phoneContext.newPage();
    await phonePage.goto(`/tasks/${t2Id}`);
    const phoneToggle = phonePage.locator(".freshness .provenance-toggle");
    await expect(phoneToggle).toBeVisible();
    const isCoarse = await phonePage.evaluate(
      () => window.matchMedia("(pointer: coarse)").matches,
    );
    expect(isCoarse).toBe(true);
    const toggleBox = await phoneToggle.boundingBox();
    expect(toggleBox).toBeTruthy();
    expect(toggleBox.height).toBeGreaterThanOrEqual(40);
    await phoneToggle.click();
    await expect(phoneToggle).toHaveAttribute("aria-expanded", "true");
    await expect(
      phonePage.locator(".freshness .provenance-panel"),
    ).toBeVisible();
    await phoneContext.close();

    // Back on desktop: click .provenance-toggle to expand provenance and WorkTree.
    await expect(provToggle).toContainText(`Work from task ${parentChatId}`);
    await provToggle.click();
    await expect(provToggle).toHaveAttribute("aria-expanded", "true");
    const provPanel = freshness.locator(".provenance-panel");
    await expect(provPanel).toBeVisible();
    const provLinks = provPanel.locator(".provenance-links");
    await expect(provLinks).toContainText("supersedes");
    await expect(provLinks).toContainText("generated from");
    await expect(provLinks).toContainText("origin");

    const supersedesLink = provLinks.locator(`a[href="/tasks/${t1Id}"]`);
    await expect(supersedesLink).toBeVisible();
    await expect(supersedesLink).toHaveText(t1Id);

    const workTreeList = provPanel.locator(".task-tree").first();
    await expect(workTreeList).toBeVisible();

    // Click `#<T1>` in provenance links to switch tasks; verify .provenance-toggle resets to collapsed.
    await supersedesLink.click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${t1Id}$`));
    await expect(provToggle).toHaveAttribute("aria-expanded", "false");
    await expect(freshness.locator(".provenance-panel")).toHaveCount(0);

    // Expand T1's .provenance-toggle and verify `superseded by #T2`.
    await provToggle.click();
    await expect(provToggle).toHaveAttribute("aria-expanded", "true");
    await expect(provLinks).toContainText("superseded by");
    await expect(provLinks.locator(`a[href="/tasks/${t2Id}"]`)).toHaveText(
      t2Id,
    );

    // Verify in-pane DetailOverlay dirty-close guard when clicking an in-pane TaskLink while editing.
    // T3 depends on T2, so its sidebar renders a TaskLink to T2 while EditTaskForm is open.
    const t3Id = t3Match[1];
    await page.goto(`/tasks/${t3Id}`);
    await expect(overlay).toBeVisible();
    await overlay.getByRole("button", { name: "Edit", exact: true }).click();
    const titleInput = overlay.getByLabel("Title");
    await titleInput.fill("Unsaved title edit on T3");

    const depTaskLink = overlay.getByRole("button", {
      name: new RegExp(`^${t2Id} `),
    });
    await expect(depTaskLink).toBeVisible();

    let dialogMessage = null;
    page.once("dialog", async (dialog) => {
      dialogMessage = dialog.message();
      await dialog.dismiss();
    });
    await depTaskLink.click();
    expect(dialogMessage).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(page).toHaveURL(new RegExp(`/tasks/${t3Id}$`));
    await overlay.getByRole("button", { name: "Cancel", exact: true }).click();

    // -------------------------------------------------------------------------
    // Step 3: BoardCard chip parity, single-card disabled move handle,
    // Shift-click range selection, hidden Kanban selection warning, and guards
    // -------------------------------------------------------------------------
    // Verify T1's row on TaskList (`/?state=closed`) renders `superseded by`, `reads`, and `writes` chips.
    await page.goto(`/?state=closed`);
    const t1Row = page
      .locator(".task-row")
      .filter({ has: page.locator(".task-number", { hasText: t1Id }) });
    await expect(t1Row).toBeVisible();
    await expect(t1Row.locator(".chips")).toContainText(
      `superseded by ${t2Id}`,
    );
    await expect(t1Row.locator(".chips")).toContainText("acme/docs (read)");
    await expect(t1Row.locator(".chips")).toContainText("acme/widgets (write)");

    // Create a Kanban board scoped to `reads:any:acme/docs` with a "Closed" column
    // (holding only T1, with two states so showState renders the role="img" badge)
    // and a "Backlog" column (holding T2..T5).
    const kanbanBoard = await apiPost("/api/boards", {
      name: "Walkthrough Kanban",
      kind: "kanban",
      query: "f=reads:any:acme/docs",
      columns: [
        { name: "Closed", states: ["closed", "failed"] },
        { name: "Backlog", states: ["proposed", "queued", "deferred"] },
      ],
    });

    await page.goto(`/boards/${kanbanBoard.id}`);
    const pageHeader = page.getByTestId("page-header");
    await expect(pageHeader).toBeVisible();
    await expect(pageHeader.locator(".header-icon svg")).toBeVisible();
    const crumbStyles = await pageHeader
      .getByTestId("top-bar-page")
      .evaluate((el) => {
        const cs = window.getComputedStyle(el);
        return {
          fontSize: cs.fontSize,
          fontWeight: cs.fontWeight,
        };
      });
    expect(crumbStyles).toEqual({
      fontSize: "16px",
      fontWeight: "600",
    });

    const closedCol = page
      .locator(".board-column")
      .filter({ hasText: "Closed" });
    const t1Card = closedCol.locator(".board-card").filter({ hasText: t1Id });
    await expect(t1Card).toBeVisible();

    // Verify BoardCard chip parity (`superseded by`, `reads`, `writes`) and `role="img"` state badge.
    await expect(t1Card.locator(".board-card-chips")).toContainText(
      `superseded by ${t2Id}`,
    );
    await expect(t1Card.locator(".board-card-chips")).toContainText(
      "acme/docs (read)",
    );
    await expect(t1Card.locator(".board-card-chips")).toContainText(
      "acme/widgets (write)",
    );
    const t1Badge = t1Card.locator(".badge-closed");
    await expect(t1Badge).toHaveAttribute("role", "img");
    await expect(t1Badge).toHaveAttribute("aria-label", "Closed");

    // Verify single-card column disables its move handle with tooltip "Nowhere to move this: it is the only one here".
    const t1MoveBtn = t1Card.getByRole("button", {
      name: `Move task ${t1Id}`,
    });
    await expect(t1MoveBtn).toBeDisabled();
    await expect(t1MoveBtn).toHaveAttribute(
      "title",
      "Nowhere to move this: it is the only one here",
    );

    // Shift-click range selection on TaskList (`/`):
    await page.goto(`/`);
    const listRows = page.locator(".task-row");
    await expect(listRows.nth(3)).toBeVisible();
    const firstListCheckbox = listRows.nth(0).getByRole("checkbox");
    const fourthListCheckbox = listRows.nth(3).getByRole("checkbox");

    await firstListCheckbox.click();
    await fourthListCheckbox.click({ modifiers: ["Shift"] });

    for (let i = 0; i < 4; i++) {
      await expect(listRows.nth(i).getByRole("checkbox")).toBeChecked();
    }
    await expect(page.getByText("4 selected", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "Clear selection", exact: true })
      .click();
    await expect(page.getByText("4 selected", { exact: true })).toHaveCount(0);

    // Shift-click range selection on TaskBoard (`/boards/<id>`):
    await page.goto(`/boards/${kanbanBoard.id}`);
    const backlogCol = page
      .locator(".board-column")
      .filter({ hasText: "Backlog" });
    const backlogCards = backlogCol.locator(".board-card");
    await expect(backlogCards).toHaveCount(4);

    const firstCardCheckbox = backlogCards.nth(0).getByRole("checkbox");
    const fourthCardCheckbox = backlogCards.nth(3).getByRole("checkbox");

    await firstCardCheckbox.click();
    await fourthCardCheckbox.click({ modifiers: ["Shift"] });

    for (let i = 0; i < 4; i++) {
      await expect(backlogCards.nth(i).getByRole("checkbox")).toBeChecked();
    }
    await expect(page.getByText("4 selected", { exact: true })).toBeVisible();
    await expect(page.getByText(/not shown here/)).toHaveCount(0);

    // Narrow the search filter so only the 2 "canary-batch" cards remain visible on the board:
    const searchInput = page.getByPlaceholder("Search tasks…");
    await searchInput.fill("canary-batch");
    await expect(backlogCards).toHaveCount(2);
    await expect(page.getByText("4 selected", { exact: true })).toBeVisible();
    await expect(
      page.getByText("2 not shown here", { exact: true }),
    ).toBeVisible();

    // Narrow further so 0 of the 4 selected cards remain visible:
    await searchInput.fill("no-matching-tasks-xyz");
    await expect(backlogCards).toHaveCount(0);
    await expect(
      page.getByText("none of them shown here", { exact: true }),
    ).toBeVisible();

    await searchInput.fill("");
    await expect(page.getByText(/not shown here/)).toHaveCount(0);

    // Open BulkEditOverlay, add a field edit, press Escape, and confirm the unsaved-changes guard fires.
    await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
    const bulkDialog = page.getByRole("dialog");
    await expect(bulkDialog).toBeVisible();

    await bulkDialog.getByRole("checkbox", { name: /^Auto-merge/ }).check();

    let bulkGuardMessage = null;
    page.once("dialog", async (dialog) => {
      bulkGuardMessage = dialog.message();
      await dialog.dismiss();
    });
    await page.keyboard.press("Escape");
    expect(bulkGuardMessage).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(bulkDialog).toBeVisible();

    page.once("dialog", async (dialog) => {
      await dialog.accept();
    });
    await page.keyboard.press("Escape");
    await expect(bulkDialog).toHaveCount(0);

    // Verify CustomizeSidebarOverlay dirty-close guard on Escape.
    await page.getByRole("button", { name: "Customize sidebar" }).click();
    const pinDialog = page.getByRole("dialog", {
      name: "Customize sidebar",
    });
    await expect(pinDialog).toBeVisible();
    await pinDialog.locator('button[aria-label^="Unpin "]').first().click();

    let pinGuardMessage = null;
    page.once("dialog", async (dialog) => {
      pinGuardMessage = dialog.message();
      await dialog.dismiss();
    });
    await page.keyboard.press("Escape");
    expect(pinGuardMessage).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(pinDialog).toBeVisible();

    page.once("dialog", async (dialog) => {
      await dialog.accept();
    });
    await page.keyboard.press("Escape");
    await expect(pinDialog).toHaveCount(0);

    // Verify un-sectioned ReportBoard renders no section move handles.
    const reportBoard = await apiPost("/api/boards", {
      name: "Unsectioned Report",
      kind: "report",
    });
    await page.goto(`/boards/${reportBoard.id}`);
    await expect(page.locator(".report-sections")).toBeVisible();
    await expect(
      page.locator('button[aria-label^="Move section"]'),
    ).toHaveCount(0);

    // -------------------------------------------------------------------------
    // Step 4: TaskTree row click & Escape focus return
    // -------------------------------------------------------------------------
    const treeBoard = await apiPost("/api/boards", {
      name: "Walkthrough Tree",
      kind: "tree",
      query: "f=reads:any:acme/docs",
    });
    await page.goto(`/boards/${treeBoard.id}`);
    const treeRow = page
      .locator(".task-tree-row")
      .filter({ has: page.locator(".task-tree-id", { hasText: t2Id }) });
    await expect(treeRow).toBeVisible();

    // Click the row's title/badge area (outside .task-tree-id) to open DetailOverlay.
    await treeRow.locator(".task-tree-title").click();
    await expect(overlay).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/tasks/${t2Id}$`));

    // Press Escape to close DetailOverlay and assert focus returns to .task-tree-id.
    await page.keyboard.press("Escape");
    await expect(overlay).toHaveCount(0);
    const treeIdLink = treeRow.locator("a.task-tree-id");
    await expect(treeIdLink).toBeFocused();

    // -------------------------------------------------------------------------
    // Step 5: propose_task hold_for_approval chip on TaskList and TaskBoard
    // -------------------------------------------------------------------------
    const autoParent = await apiPost("/api/tasks", {
      title: "Auto-merge parent for hold_for_approval",
      body: "Parent whose permission covers same-branch proposals.",
      repo: "acme/widgets",
      approved: true,
      autoMerge: true,
      reads: ["acme/docs"],
    });
    const autoParentId = String(autoParent.id);
    const heldText = await callProposeTaskMCP({
      url: demoServer,
      code: accessCode,
      parentTaskId: autoParentId,
      sandboxRoot: path.join(root, "mcp-sandbox-held"),
      args: {
        title: "Held rollout proposal",
        body: "Held for operator review despite auto-merge parent.",
        reads: ["acme/docs"],
        hold_for_approval: true,
      },
    });
    expect(heldText).toContain(
      "filed unapproved because you asked to hold it (hold_for_approval); your permission would otherwise have queued it",
    );
    const heldId = heldText.match(/\/tasks\/(\d+)/)[1];

    await page.goto(`/boards/${kanbanBoard.id}`);
    const heldCard = backlogCol
      .locator(".board-card")
      .filter({ hasText: heldId });
    await expect(heldCard.locator(".chip-held")).toHaveText(
      `held for approval by ${autoParentId}`,
    );

    await page.goto("/");
    const heldRow = page
      .locator(".task-row")
      .filter({ has: page.locator(".task-number", { hasText: heldId }) });
    const heldChip = heldRow.locator(".chip-held");
    await expect(heldChip).toHaveText(`held for approval by ${autoParentId}`);
    for (const vp of [
      { width: 1280, height: 800 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(vp);
      await expect(heldChip).toBeVisible();
      const rowBox = await heldRow.boundingBox();
      const chipBox = await heldChip.boundingBox();
      expect(chipBox.x).toBeGreaterThanOrEqual(rowBox.x - 1);
      expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(
        rowBox.x + rowBox.width + 1,
      );
      expect(chipBox.y).toBeGreaterThanOrEqual(rowBox.y - 1);
      expect(chipBox.y + chipBox.height).toBeLessThanOrEqual(
        rowBox.y + rowBox.height + 1,
      );
    }
    await page.request.delete(`/api/tasks/${heldId}`).catch(() => {});
    await page.request.delete(`/api/tasks/${autoParentId}`).catch(() => {});
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});
