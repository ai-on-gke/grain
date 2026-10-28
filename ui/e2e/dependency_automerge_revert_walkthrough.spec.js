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

import { expect, test } from "./fixtures.js";

test.describe("Multi-Task Dependency Chains, Auto-Merge Lifecycle and Revert Workflows Walkthrough", () => {
  test.setTimeout(120_000);

  test("exercises task dependency graph resolution, merge queue serialization and batching, auto-merge verification, and task revert operations", async ({
    page,
    request,
  }) => {
    // -----------------------------------------------------------------------
    // PHASE 1: Multi-Task Dependency Chains (depends_on / dependsOn)
    // -----------------------------------------------------------------------

    // 1.1 Create Root Prerequisite Task
    const rootTitle = `Root Prerequisite DB Migration ${Date.now()}`;
    const rootRes = await request.post("/api/tasks", {
      data: {
        title: rootTitle,
        description:
          "Foundational database schema migration required by downstream services.",
        repo: "acme/widgets",
        approved: true,
      },
    });
    expect(rootRes.ok()).toBeTruthy();
    const rootTask = await rootRes.json();
    expect(rootTask.id).toBeTruthy();

    // 1.2 Create Auxiliary Prerequisite Task (to test dynamic dependency manipulation)
    const auxTitle = `Auxiliary Auth Setup ${Date.now()}`;
    const auxRes = await request.post("/api/tasks", {
      data: {
        title: auxTitle,
        description: "Auxiliary auth provider configuration dependency.",
        repo: "acme/widgets",
        approved: true,
      },
    });
    expect(auxRes.ok()).toBeTruthy();
    const auxTask = await auxRes.json();

    // 1.3 Create Downstream Task with depends_on via New Task Form UI
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Open New Task Drawer
    await page.getByRole("button", { name: "+ New task", exact: true }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    const childTitle = `Dependent Feature Module ${Date.now()}`;
    await page.getByLabel("Title").fill(childTitle);
    await page
      .getByLabel("Description")
      .fill(
        "Feature module that requires foundational schema before execution.",
      );
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");

    // Pick Root Task in Depends On section
    const depSearchInput = page.getByPlaceholder("Search tasks to depend on…");
    await depSearchInput.fill(rootTask.id);
    const pickerOption = page
      .locator(".MuiMenuItem-root")
      .filter({ hasText: rootTitle })
      .first();
    await expect(pickerOption).toBeVisible();
    await pickerOption.click();

    // Verify dependency chip is rendered in New Task form
    await expect(
      page.locator(".MuiChip-root").filter({ hasText: rootTitle }),
    ).toBeVisible();

    // Submit the task
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // 1.4 Verify Child Task is Blocked in Task List
    const childRow = page.locator(".task-row").filter({ hasText: childTitle });
    await expect(childRow).toBeVisible();
    await expect(childRow.locator(".badge-blocked")).toBeVisible();

    // 1.5 Open Child Task in DetailOverlay and inspect Depends on Section
    await childRow.click();
    const overlay = page.locator(".MuiDialog-paper");
    await expect(
      overlay.getByRole("heading", { name: childTitle }),
    ).toBeVisible();

    // Verify "Depends on" fieldset shows Root Task as a blocking dependency
    const dependsOnFieldset = overlay
      .locator("fieldset")
      .filter({ hasText: "Depends on" });
    await expect(dependsOnFieldset).toBeVisible();
    const rootChip = dependsOnFieldset
      .locator(".MuiChip-root")
      .filter({ hasText: rootTitle });
    await expect(rootChip).toBeVisible();
    // Blocking chip should have warning / outlined styling indicating open dependency
    await expect(rootChip).toHaveClass(/MuiChip-colorWarning/);

    // 1.6 Click Dependency Chip to navigate to Root Task Detail
    await rootChip.click();
    await expect(
      overlay.getByRole("heading", { name: rootTitle }),
    ).toBeVisible();

    // Navigate back to Child Task
    await page.goto("/");
    await page.locator(".task-row").filter({ hasText: childTitle }).click();
    await expect(
      overlay.getByRole("heading", { name: childTitle }),
    ).toBeVisible();

    // 1.7 Add Auxiliary Dependency dynamically in DetailOverlay
    const detailDepInput = overlay.getByPlaceholder("Add a dependency…");
    await detailDepInput.fill(auxTask.id);
    const auxOption = page
      .locator(".MuiMenuItem-root")
      .filter({ hasText: auxTitle });
    await expect(auxOption).toBeVisible();
    await auxOption.click();

    // Verify both dependency chips are present
    await expect(
      dependsOnFieldset.locator(".MuiChip-root").filter({ hasText: rootTitle }),
    ).toBeVisible();
    const auxChip = dependsOnFieldset
      .locator(".MuiChip-root")
      .filter({ hasText: auxTitle });
    await expect(auxChip).toBeVisible();

    // 1.8 Remove Auxiliary Dependency via delete icon
    const auxDeleteBtn = auxChip.locator("[title*='Remove dependency']");
    await auxDeleteBtn.click();
    await expect(
      dependsOnFieldset.locator(".MuiChip-root").filter({ hasText: auxTitle }),
    ).not.toBeVisible();

    // 1.9 Complete Root Task and Verify Dependency Graph Resolution
    const closeRootRes = await request.post(`/api/tasks/${rootTask.id}/close`);
    expect(closeRootRes.ok()).toBeTruthy();

    // Reload or re-inspect Child Task in DetailOverlay
    await page.goto("/");
    await page.locator(".task-row").filter({ hasText: childTitle }).click();
    await expect(
      overlay.getByRole("heading", { name: childTitle }),
    ).toBeVisible();

    // Verify Root Task chip in Depends On section is now resolved (no longer warning/blocked)
    const resolvedChip = overlay
      .locator("fieldset")
      .filter({ hasText: "Depends on" })
      .locator(".MuiChip-root")
      .filter({ hasText: rootTitle });
    await expect(resolvedChip).toBeVisible();
    await expect(resolvedChip).not.toHaveClass(/MuiChip-colorWarning/);

    // Close detail overlay
    await page.keyboard.press("Escape");
    await expect(overlay).not.toBeVisible();

    // -----------------------------------------------------------------------
    // PHASE 2: Auto-Merge Lifecycle Verification & Inbox Management
    // -----------------------------------------------------------------------

    // 2.1 Create Proposal Task without Auto-Approval (approved: false)
    const proposalTitle = `Proposal Requiring Triage ${Date.now()}`;
    const proposalRes = await request.post("/api/tasks", {
      data: {
        title: proposalTitle,
        description:
          "Proposed task requiring human review before entering queue.",
        repo: "acme/widgets",
        approved: false,
        autoMerge: false,
      },
    });
    expect(proposalRes.ok()).toBeTruthy();
    const proposalTask = await proposalRes.json();
    expect(proposalTask.state).toBe("proposed");

    // 2.2 Navigate to /inbox and verify task is listed under Proposals
    await page.goto("/inbox");
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();

    const proposalItem = page
      .locator(".inbox-item")
      .filter({ hasText: proposalTitle });
    await expect(proposalItem).toBeVisible();

    // 2.3 Approve Proposal in Inbox
    const approveBtn = proposalItem.getByRole("button", {
      name: "Approve",
      exact: true,
    });
    await expect(approveBtn).toBeVisible();
    await approveBtn.click();

    // Verify task leaves inbox upon approval
    await expect(
      page.locator(".inbox-item").filter({ hasText: proposalTitle }),
    ).not.toBeVisible();

    // 2.4 Verify Task is now Queued on Task List and inspect Auto-Merge Toggle in DetailOverlay
    await page.goto("/");
    const approvedRow = page
      .locator(".task-row")
      .filter({ hasText: proposalTitle });
    await expect(approvedRow).toBeVisible();
    await expect(approvedRow.locator(".badge-queued")).toBeVisible();

    await approvedRow.click();
    await expect(
      overlay.getByRole("heading", { name: proposalTitle }),
    ).toBeVisible();

    // Toggle Auto-merge checkbox in Actions section
    const autoMergeCheckbox = overlay.getByLabel("Auto-merge once checks pass");
    await expect(autoMergeCheckbox).not.toBeChecked();
    await autoMergeCheckbox.click();

    // Verify task autoMerge flag is updated
    await expect(autoMergeCheckbox).toBeChecked();

    // Verify via API that autoMerge is true
    const checkTaskRes = await request.get(`/api/tasks/${proposalTask.id}`);
    const checkTask = await checkTaskRes.json();
    expect(checkTask.autoMerge).toBe(true);

    // Test Submit endpoint on a completed task
    const submitTaskRes = await request.post(
      `/api/tasks/${proposalTask.id}/submit`,
    );
    expect(submitTaskRes.ok()).toBeTruthy();

    await page.keyboard.press("Escape");
    await expect(overlay).not.toBeVisible();

    // -----------------------------------------------------------------------
    // PHASE 3: Merge Queue Serialization and Batching
    // -----------------------------------------------------------------------

    // 3.1 Navigate to Repository Page to verify MergeBatchPanel
    await page.goto("/repos/acme/widgets");
    await expect(
      page.getByRole("heading", { name: "acme/widgets", exact: true }),
    ).toBeVisible();

    // The batch `grain demo` seeds (pkg/demo/demo.go): the merge queue
    // testing acme/widgets#101 and #102 together on main, read through the
    // real GET /api/merge-batches -- member order and the throwaway ref.
    await expect(
      page.getByRole("heading", { name: "Merge batches" }),
    ).toBeVisible();
    const batchPanel = page
      .getByRole("heading", { name: "Merge batches" })
      .locator("xpath=..");
    await expect(batchPanel.getByText(/2 changes on main/i)).toBeVisible();
    await expect(
      batchPanel.getByText(/grain-merge-queue\/[a-z0-9-]+\/main/i),
    ).toBeVisible();
    await expect(
      batchPanel.getByText("first -- a failure is halved towards this end"),
    ).toBeVisible();
    // The queue halves from the front, so the order members are drawn in
    // is the order they were batched in.
    const panelText = await batchPanel.innerText();
    const first = panelText.indexOf("Rotate the Gemini signing key");
    const second = panelText.indexOf("Cache the capability readiness probe");
    expect(first).toBeGreaterThanOrEqual(0);
    expect(second).toBeGreaterThan(first);

    // -----------------------------------------------------------------------
    // PHASE 4: Task Revert Lifecycle Operations
    // -----------------------------------------------------------------------

    // 4.1 Create Origin Task that introduced changes
    const originTitle = `Original Production Feature ${Date.now()}`;
    const originRes = await request.post("/api/tasks", {
      data: {
        title: originTitle,
        description:
          "Original feature deployment requiring subsequent rollback.",
        repo: "acme/widgets",
        approved: true,
      },
    });
    expect(originRes.ok()).toBeTruthy();
    const originTask = await originRes.json();
    expect(originTask.id).toBeTruthy();

    // 4.2 Create Revert Task linking back to Origin Task
    const revertTitle = `Revert: ${originTitle}`;
    const revertRes = await request.post("/api/tasks", {
      data: {
        title: revertTitle,
        description: `Rollback PR changes for ${originTask.id} due to operational regression.`,
        repo: "acme/widgets",
        reverts: originTask.id,
        approved: true,
      },
    });
    expect(revertRes.ok()).toBeTruthy();
    const revertTask = await revertRes.json();
    expect(revertTask.id).toBeTruthy();
    expect(revertTask.reverts).toBe(originTask.id);

    // 4.3 Verify Origin Task has RevertedBy link mapped
    const updatedOriginRes = await request.get(`/api/tasks/${originTask.id}`);
    expect(updatedOriginRes.ok()).toBeTruthy();
    const updatedOriginTask = await updatedOriginRes.json();
    expect(updatedOriginTask.revertedBy).toBe(revertTask.id);

    // 4.4 Inspect Revert Task in UI: Verify "reverts <originId>" link is rendered
    await page.goto("/");
    const revertRow = page
      .locator(".task-row")
      .filter({ hasText: revertTitle });
    await expect(revertRow).toBeVisible();
    await revertRow.click();
    await expect(
      overlay.getByRole("heading", { name: revertTitle }),
    ).toBeVisible();
    await overlay.locator(".provenance-toggle").click();

    const revertsLink = overlay.getByRole("link", {
      name: originTask.id,
      exact: true,
    });
    await expect(revertsLink).toBeVisible();
    await expect(overlay.locator(".freshness")).toContainText(
      `reverts ${originTask.id}`,
    );

    // 4.5 Click Reverts Link to navigate to Origin Task
    await revertsLink.click();
    await expect(
      overlay.getByRole("heading", { name: originTitle }),
    ).toBeVisible();
    await overlay.locator(".provenance-toggle").click();

    // 4.6 Verify Origin Task renders "reverted by <revertId>" link
    const revertedByLink = overlay.getByRole("link", {
      name: revertTask.id,
      exact: true,
    });
    await expect(revertedByLink).toBeVisible();
    await expect(overlay.locator(".freshness")).toContainText(
      `reverted by ${revertTask.id}`,
    );

    // 4.7 Click Reverted By Link to return to Revert Task
    await revertedByLink.click();
    await expect(
      overlay.getByRole("heading", { name: revertTitle }),
    ).toBeVisible();

    // Close overlay
    await page.keyboard.press("Escape");
    await expect(overlay).not.toBeVisible();
  });
});
