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

// Comprehensive UI workflows for Task Detail: in-place edits, conversation comments,
// action buttons (Prompt, Close, Reopen), question answering, and navigation.

test.describe("Task Detail Interactive Button Workflows", () => {
  test("edits task title and description in place via UI buttons and persists across reload", async ({
    page,
  }) => {
    // 1. Create a task to edit
    const stamp = Date.now();
    const origTitle = `E2E Editable Task ${stamp}`;
    const origDesc = `Original description content ${stamp}`;
    const res = await page.request.post("/api/tasks", {
      data: {
        title: origTitle,
        description: origDesc,
      },
    });
    expect(res.ok()).toBe(true);
    const task = await res.json();

    // 2. Open task detail page
    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header h2")).toContainText(origTitle);
    await expect(page.locator(".description")).toContainText(origDesc);

    // 3. Click Edit button
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByLabel("Title")).toHaveValue(origTitle);

    // 4. Update title and description in the form
    const updatedTitle = `E2E Updated Title ${stamp}`;
    const updatedDesc = `Updated description body with markdown\n\n- Step 1\n- Step 2`;
    await page.getByLabel("Title").fill(updatedTitle);
    await page.getByLabel("Description").fill(updatedDesc);

    // 5. Click Save button
    await page.getByRole("button", { name: "Save", exact: true }).click();

    // 6. Verify changes rendered immediately in DOM
    await expect(page.locator(".detail-header h2")).toContainText(updatedTitle);
    await expect(page.locator(".description")).toContainText(
      "Updated description body with markdown",
    );
    await expect(page.locator(".description")).toContainText("Step 1");

    // 7. Verify persistence after hard reload
    await page.reload();
    await expect(page.locator(".detail-header h2")).toContainText(updatedTitle);
    await expect(page.locator(".description")).toContainText(
      "Updated description body with markdown",
    );
  });

  test("sends comments in the conversation chat and displays in transcript", async ({
    page,
  }) => {
    const stamp = Date.now();
    const res = await page.request.post("/api/tasks", {
      data: {
        title: `E2E Chat Task ${stamp}`,
        description: "Task for testing conversational comment posting.",
      },
    });
    expect(res.ok()).toBe(true);
    const task = await res.json();

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header h2")).toBeVisible();

    // Find the comment input textarea
    const commentInput = page.getByPlaceholder("Reply...");
    await expect(commentInput).toBeVisible();

    const commentText = `Testing operator comment at ${stamp}`;
    await commentInput.fill(commentText);

    // Click Comment button
    await page.getByRole("button", { name: "Comment", exact: true }).click();

    // Verify comment appears in the conversation thread
    const commentElement = page
      .locator(".timeline-item, .chat-message, .message-body")
      .filter({ hasText: commentText });
    await expect(commentElement).toBeVisible();

    // Verify persistence on reload
    await page.reload();
    await expect(
      page
        .locator(".timeline-item, .chat-message, .message-body")
        .filter({ hasText: commentText }),
    ).toBeVisible();
  });

  test("opens full prompt overlay via Prompt button and closes it", async ({
    page,
  }) => {
    await page.goto("/");
    // Open seeded running task which carries a prompt
    await page
      .locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" })
      .click();
    await expect(page.locator(".detail-header")).toBeVisible();

    // Click Prompt button
    const promptButton = page.getByRole("button", {
      name: "Prompt",
      exact: true,
    });
    await expect(promptButton).toBeVisible();
    await promptButton.click();

    // Verify prompt dialog opens
    const promptDialog = page.locator(".MuiDialog-paper", {
      has: page.getByRole("heading", { name: /^Prompt for/ }),
    });
    await expect(promptDialog).toBeVisible();
    await expect(
      promptDialog.getByText(/Push your change to a new branch named/),
    ).toBeVisible();

    // Close prompt dialog by clicking close button or pressing Escape
    await page.keyboard.press("Escape");
    await expect(promptDialog).toHaveCount(0);
  });

  test("closes and reopens a task via task detail action buttons", async ({
    page,
  }) => {
    const stamp = Date.now();
    const res = await page.request.post("/api/tasks", {
      data: {
        title: `E2E Close Reopen Task ${stamp}`,
        approved: true,
      },
    });
    expect(res.ok()).toBe(true);
    const task = await res.json();

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header h2")).toBeVisible();
    await expect(page.locator(".detail-state .badge")).toContainText("Waiting");

    // Click the Close button in the detail action buttons area
    const closeBtn = page
      .locator(".detail-side, .detail-actions, .actions-bar")
      .getByRole("button", { name: "Close", exact: true });
    await expect(closeBtn).toBeVisible();
    await closeBtn.click();

    // Verify task state changes to Closed
    await expect(page.locator(".detail-state .badge")).toContainText("Closed");

    // Click Reopen button
    const reopenBtn = page
      .locator(".detail-side, .detail-actions, .actions-bar")
      .getByRole("button", { name: "Reopen", exact: true });
    await expect(reopenBtn).toBeVisible();
    await reopenBtn.click();

    // Verify task state changes back to Waiting
    await expect(page.locator(".detail-state .badge")).toContainText("Waiting");
  });
  // docs/test-runs/2026-10-05-task-intake-inbox-lifecycle.md, bugs 1 and
  // 2: an edited description reached the timeline with a full stop after
  // it ("done.."), and the pane's links to related tasks had an href of
  // "#", so "open in new tab" reopened the pane already open.
  test("quotes an edited description as typed and links related tasks by address", async ({
    page,
    context,
  }) => {
    // Two page loads, an edit and a new tab: on a loaded CI runner the
    // tab alone can take most of the default 30s (task 3176).
    test.setTimeout(90_000);
    const res = await page.request.post("/api/tasks", {
      data: { title: `E2E edit note ${Date.now()}`, description: "before" },
    });
    expect(res.ok()).toBe(true);
    const task = await res.json();
    await page.goto(`/tasks/${task.id}`);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByLabel("Description").fill("All done.");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    const note = page.locator(".timeline-comment-body", {
      hasText: "This task was just edited",
    });
    await expect(note).toContainText("All done.");
    await expect(note).not.toContainText("All done..");

    // A pair this test files itself -- a parent, and a task proposed by
    // it, the way a run's propose_task reaches POST /api/tasks -- rather
    // than the first task in the worker's demo store that happens to
    // have a generatedFrom. Which seeded task that was, and what state
    // the other spec files sharing the worker had left its pane in,
    // changed with how files were spread across workers, and on CI the
    // modified click below on whatever it found timed out with nothing
    // to say why (grain/task-2400).
    const parentRes = await page.request.post("/api/tasks", {
      data: { title: `E2E link parent ${Date.now()}`, description: "parent" },
    });
    expect(parentRes.ok()).toBe(true);
    const parent = await parentRes.json();
    const childRes = await page.request.post("/api/tasks", {
      data: {
        title: `E2E link child ${Date.now()}`,
        description: "filed by the parent",
        proposedBy: parent.id,
      },
    });
    expect(childRes.ok()).toBe(true);
    const child = await childRes.json();
    const generated = await (
      await page.request.get(`/api/tasks/${child.id}`)
    ).json();
    expect(generated.generatedFrom).toBe(parent.id);
    await page.goto(`/tasks/${generated.id}`);
    await page.locator(".freshness .provenance-toggle").click();
    const link = page
      .locator(".freshness")
      .getByRole("link", { name: generated.generatedFrom, exact: true })
      .first();
    await expect(link).toHaveAttribute(
      "href",
      `/tasks/${generated.generatedFrom}`,
    );
    // Visible and clickable first, so a link something covers or keeps
    // moving fails here saying so, rather than as a wait for a tab.
    await expect(link).toBeVisible();
    await link.click({ trial: true, timeout: 10_000 });
    // Asked up to three times: on a loaded runner the modified click has
    // landed and opened nothing (the pane re-rendering under it), and one
    // 45s wait for a tab that was never going to come was the whole of
    // the failure. A tab is a tab whichever click opened it.
    let tab;
    for (let attempt = 0; !tab && attempt < 3; attempt++) {
      [tab] = await Promise.all([
        context
          .waitForEvent("page", { timeout: 15_000 })
          .catch((err) => (attempt === 2 ? Promise.reject(err) : undefined)),
        link.click({ modifiers: ["ControlOrMeta"] }),
      ]);
    }
    // A new tab reports "" as its URL until its first navigation
    // commits, which a busy runner can hold past expect's 5s default --
    // and on CI Playwright's own record of a popup's URL has stayed ""
    // even after its log said the tab had navigated to the team-scoped
    // address (task 3448). So ask the tab where it is rather than
    // trusting tab.url(); a context torn down mid-navigation reads as ""
    // and is asked again.
    await expect
      .poll(
        () => tab.evaluate(() => window.location.pathname).catch(() => ""),
        { timeout: 20_000 },
      )
      .toMatch(new RegExp(`/tasks/${generated.generatedFrom}$`));
    await expect(page).toHaveURL(new RegExp(`/tasks/${generated.id}$`));
    await tab.close();
    await link.click();
    await expect(page).toHaveURL(
      new RegExp(`/tasks/${generated.generatedFrom}$`),
    );
  });
});
