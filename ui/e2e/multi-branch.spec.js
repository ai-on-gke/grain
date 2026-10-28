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

test.describe("Multi-branch task configuration and cross-branch proposals", () => {
  let originalTargetRepos = [];

  test.beforeEach(async ({ request }) => {
    const teamResponse = await request.get("/api/deployment");
    expect(teamResponse.ok()).toBeTruthy();
    const team = await teamResponse.json();
    originalTargetRepos = team.targetRepos || [];
    const targetRepos = Array.from(
      new Set([...originalTargetRepos, "acme/tools"]),
    );
    const updateResponse = await request.put("/api/deployment", {
      data: { targetRepos },
    });
    expect(updateResponse.ok()).toBeTruthy();
  });

  test.afterEach(async ({ request }) => {
    await request.put("/api/deployment", {
      data: { targetRepos: originalTargetRepos },
    });
  });

  test("creates a task with multiple branch targets and verifies proposal auto-merge and UI rendering", async ({
    page,
    request,
  }) => {
    // 1. Load app
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // 2. Open New task overlay
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    const title = `Multi-branch parent task ${Date.now()}`;
    await page.getByLabel("Title").fill(title);

    // Select target repository
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    await page.getByLabel("Base branch").fill("main");

    // Open Advanced options to configure extra write targets
    await page.getByText(/Advanced options/).click();

    // Add extra write target 1: same repository on release/1.0
    await page.getByLabel(/Another repo/).fill("acme/widgets");
    await page.keyboard.press("Enter");
    const writeBaseInputs = page.getByLabel("Base branch in acme/widgets");
    await expect(writeBaseInputs).toHaveCount(1);
    await writeBaseInputs.first().fill("release/1.0");

    // Add extra write target 2: another repository acme/tools on main
    await page.getByLabel(/Another repo/).fill("acme/tools");
    await page.keyboard.press("Enter");
    const toolsBaseInput = page.getByLabel("Base branch in acme/tools");
    await expect(toolsBaseInput).toHaveCount(1);
    await toolsBaseInput.first().fill("main");

    // Submit task creation. Wait for the POST itself so a slow, loaded CI
    // worker does not turn task creation into an unexplained overlay timeout,
    // and report the server response if filing genuinely fails.
    const createdResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/tasks") &&
        response.request().method() === "POST",
      { timeout: 15_000 },
    );
    await page.getByRole("button", { name: "Create task" }).click();
    const response = await createdResponse;
    expect(
      response.ok(),
      `POST /api/tasks: ${response.status()} ${await response.text()}`,
    ).toBe(true);
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
      { timeout: 15_000 },
    );

    // 3. Verify task appears in task list with write targets rendered as chips with tooltips
    const taskRow = page.locator(".task-row", { hasText: title });
    await expect(taskRow).toBeVisible();
    const writeChip1 = taskRow.locator(
      'div[title="may also land work in acme/widgets on release/1.0"]',
    );
    await expect(writeChip1).toBeVisible();
    await expect(writeChip1).toHaveText("acme/widgets (write)");

    const writeChip2 = taskRow.locator(
      'div[title="may also land work in acme/tools on main"]',
    );
    await expect(writeChip2).toBeVisible();
    await expect(writeChip2).toHaveText("acme/tools (write)");

    // 4. Click into Task detail overlay
    await taskRow.click();
    const detailPaper = page.locator(".MuiDialog-paper");
    await expect(detailPaper).toBeVisible();
    await expect(
      detailPaper.getByRole("heading", { name: title }),
    ).toBeVisible();

    // Verify Write targets inputs in Detail overlay
    const base1Input = detailPaper.getByLabel("Base branch in acme/widgets");
    await expect(base1Input).toHaveValue("release/1.0");
    const base2Input = detailPaper.getByLabel("Base branch in acme/tools");
    await expect(base2Input).toHaveValue("main");

    // Test editing write target in detail overlay without cross-row mutation
    await base1Input.fill("release/1.1");
    // Trigger onCommit by blurring
    await base1Input.blur();
    await expect(base1Input).toHaveValue("release/1.1");
    await expect(base2Input).toHaveValue("main");

    // Test removing write target
    const removeWidgets = detailPaper.getByTitle("Remove acme/widgets");
    await removeWidgets.click();
    await expect(
      detailPaper.getByLabel("Base branch in acme/widgets"),
    ).toHaveCount(0);
    await expect(
      detailPaper.getByLabel("Base branch in acme/tools"),
    ).toHaveCount(1);

    // Get the task ID from the URL or list
    const taskIdMatch = page.url().match(/\/tasks\/([^/?#]+)/);
    expect(taskIdMatch).not.toBeNull();
    const taskId = taskIdMatch[1];

    // 5. Test cross-branch proposals via API
    // Proposal 1: Targeting declared branch `release/1.0` -> should inherit auto-merge / queued status
    const prop1Res = await request.post("/api/tasks", {
      data: {
        title: "Backport bugfix to release/1.0",
        description: "Backporting changes to release/1.0",
        repo: "acme/widgets",
        base: "release/1.0",
        autoMerge: true,
        queued: true,
      },
    });
    expect(prop1Res.ok()).toBeTruthy();
    const prop1Data = await prop1Res.json();
    expect(prop1Data.base).toBe("release/1.0");
    expect(prop1Data.repo).toBe("acme/widgets");

    // Proposal 2: Targeting undeclared branch `release/2.0` with explanatory review note
    const undeclaredNote =
      "grain: this task was filed by an agent running on branch main, but asked to land on branch release/2.0. That may be what you want, but auto-merge is a permission for one branch, so a human has to approve this task before it will run.";
    const prop2Res = await request.post("/api/tasks", {
      data: {
        title: "Experimental work on release/2.0",
        description: `Proposal on undeclared branch.\n\n${undeclaredNote}`,
        repo: "acme/widgets",
        base: "release/2.0",
        autoMerge: false,
        queued: false,
      },
    });
    expect(prop2Res.ok()).toBeTruthy();
    const prop2Data = await prop2Res.json();
    expect(prop2Data.base).toBe("release/2.0");
    expect(prop2Data.repo).toBe("acme/widgets");

    // 6. Navigate to undeclared proposal in UI and verify target base branch and review note rendering
    await page.goto(`/tasks/${prop2Data.id}`);
    await expect(page.locator(".MuiDialog-paper")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Experimental work on release/2.0" }),
    ).toBeVisible();
    await expect(page.getByLabel("Base branch")).toHaveValue("release/2.0");
    await expect(
      page.getByText(/auto-merge is a permission for one branch/),
    ).toBeVisible();
    await expect(page.locator(".detail-state .badge-proposed")).toBeVisible();
  });
});
