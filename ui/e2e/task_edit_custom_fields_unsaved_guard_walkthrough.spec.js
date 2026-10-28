import { test, expect } from "./fixtures.js";

// Walkthrough: Task edit and custom fields unsaved changes guard
// (docs/test-plans/2026-10-09.md §3)
// Commits: 131c8f003, 8dd9070c8

test.describe("Unsaved changes guard in Task Edit and Custom Fields", () => {
  test("Task Edit dirty guard: backdrop click, Escape key, Back button, and browser Back", async ({
    page,
  }) => {
    const prompts = [];
    let accept = false;
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      return accept ? dialog.accept() : dialog.dismiss();
    });

    // Start from home so history has an entry to goBack() to
    await page.goto("/");
    await page
      .locator(".task-row")
      .filter({ hasText: "Add a dark mode toggle" })
      .first()
      .click();
    await expect(page).toHaveURL(/\/tasks\/1$/);

    const overlay = page.getByRole("dialog", { name: /^Task 1:/ });
    await expect(overlay).toBeVisible();

    // Enter Edit mode
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const titleInput = page.getByLabel("Title", { exact: true });
    const descInput = page.getByLabel("Description", { exact: true });
    await expect(titleInput).toBeVisible();

    const origTitle = await titleInput.inputValue();
    const origDesc = await descInput.inputValue();

    // Modify title and description
    await titleInput.fill("Add a dark mode toggle (draft modified)");
    await descInput.fill("Settings page theme switch draft notes.");

    // 1. Backdrop click dismiss trigger
    const backdrop = page.locator(".MuiBackdrop-root").first();
    await backdrop.click({ position: { x: 50, y: 300 }, force: true });
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(titleInput).toHaveValue(
      "Add a dark mode toggle (draft modified)",
    );
    await expect(descInput).toHaveValue(
      "Settings page theme switch draft notes.",
    );

    // 2. Escape key dismiss trigger
    await page.keyboard.press("Escape");
    await expect.poll(() => prompts.length).toBe(2);
    expect(prompts[1]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(titleInput).toHaveValue(
      "Add a dark mode toggle (draft modified)",
    );

    // 3. ← Back button dismiss trigger
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(3);
    expect(prompts[2]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(titleInput).toHaveValue(
      "Add a dark mode toggle (draft modified)",
    );

    // 4. Browser history Back dismiss trigger
    await page.goBack();
    await expect.poll(() => prompts.length).toBe(4);
    expect(prompts[3]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(titleInput).toHaveValue(
      "Add a dark mode toggle (draft modified)",
    );

    // Now test dialog acceptance: discards changes, closes overlay
    accept = true;
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(5);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(page.url()).not.toContain("/tasks/1");

    // Reopen /tasks/1 and verify original values are retained (not modified)
    await page
      .locator(".task-row")
      .filter({ hasText: "Add a dark mode toggle" })
      .first()
      .click();
    await expect(
      page.getByRole("heading", { name: /Add a dark mode toggle/ }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByLabel("Title", { exact: true })).toHaveValue(
      origTitle,
    );
    await expect(page.getByLabel("Description", { exact: true })).toHaveValue(
      origDesc,
    );
  });

  test("CustomFieldsSection dirty guard: dropdown and text custom fields across dismiss triggers", async ({
    page,
  }) => {
    const prompts = [];
    let accept = false;
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      return accept ? dialog.accept() : dialog.dismiss();
    });

    // Start from home so history has an entry to goBack() to
    await page.goto("/");
    await page
      .locator(".task-row")
      .filter({ hasText: "Add a dark mode toggle" })
      .first()
      .click();
    await expect(page).toHaveURL(/\/tasks\/1$/);

    const overlay = page.getByRole("dialog", { name: /^Task 1:/ });
    await expect(overlay).toBeVisible();

    // Open "Edit fields"
    await page
      .getByRole("button", { name: "Edit fields", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Save fields" }),
    ).toBeVisible();

    // Alter text field (Requested by) and dropdown (Area / Priority)
    const requestedByInput = page.getByLabel("Requested by");
    const origRequestedBy = await requestedByInput.inputValue();
    await requestedByInput.fill("Acme Global Systems");

    // Alter dropdown (Area)
    await page.getByLabel("Area").click();
    await page.getByRole("option", { name: "infra" }).click();

    // 1. Backdrop click dismiss trigger
    const backdrop = page.locator(".MuiBackdrop-root").first();
    await backdrop.click({ position: { x: 50, y: 300 }, force: true });
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(requestedByInput).toHaveValue("Acme Global Systems");

    // 2. Escape dismiss trigger
    await page.keyboard.press("Escape");
    await expect.poll(() => prompts.length).toBe(2);
    expect(prompts[1]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(requestedByInput).toHaveValue("Acme Global Systems");

    // 3. ← Back button dismiss trigger
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(3);
    expect(prompts[2]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(requestedByInput).toHaveValue("Acme Global Systems");

    // 4. Browser history Back dismiss trigger
    await page.goBack();
    await expect.poll(() => prompts.length).toBe(4);
    expect(prompts[3]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(requestedByInput).toHaveValue("Acme Global Systems");

    // Test dialog acceptance
    accept = true;
    await page.keyboard.press("Escape");
    await expect.poll(() => prompts.length).toBe(5);
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // Reopen and verify original custom field values are retained
    await page
      .locator(".task-row")
      .filter({ hasText: "Add a dark mode toggle" })
      .first()
      .click();
    await page
      .getByRole("button", { name: "Edit fields", exact: true })
      .click();
    await expect(page.getByLabel("Requested by")).toHaveValue(origRequestedBy);
  });

  test("Concurrent editing: simultaneous edits in Task Edit and Custom Fields evaluate combined dirty ref", async ({
    page,
  }) => {
    const prompts = [];
    let accept = false;
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      return accept ? dialog.accept() : dialog.dismiss();
    });

    await page.goto("/tasks/1");
    const overlay = page.getByRole("dialog", { name: /^Task 1:/ });
    await expect(overlay).toBeVisible();

    // 1. Enter Task Edit mode and modify title
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const titleInput = page.getByLabel("Title", { exact: true });
    await titleInput.fill("Concurrent edit title modification");

    // 2. Also open "Edit fields" in CustomFieldsSection and modify Requested by
    await page
      .getByRole("button", { name: "Edit fields", exact: true })
      .click();
    const requestedByInput = page.getByLabel("Requested by");
    await requestedByInput.fill("Concurrent customer modification");

    // Both editors are actively dirty. Verify combined dirty ref triggers confirmation
    await page.keyboard.press("Escape");
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(titleInput).toHaveValue("Concurrent edit title modification");
    await expect(requestedByInput).toHaveValue(
      "Concurrent customer modification",
    );

    // Cancel Custom Fields editor using its specific Cancel button inside .declared
    await page
      .locator(".declared")
      .getByRole("button", { name: "Cancel" })
      .click();
    // Custom fields editor is closed, but Task Edit form is still dirty!
    // Try dismiss via ← Back button
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(2);
    expect(prompts[1]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(titleInput).toHaveValue("Concurrent edit title modification");

    // Re-open Custom Fields, make it dirty, cancel Task Edit
    await page
      .getByRole("button", { name: "Edit fields", exact: true })
      .click();
    await page.getByLabel("Requested by").fill("Only custom fields dirty now");
    // Cancel Task Edit form using its Cancel button inside .detail-main
    await page
      .locator(".detail-main")
      .getByRole("button", { name: "Cancel" })
      .click();
    // Task edit is not dirty, but Custom Fields is dirty!
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(3);
    expect(prompts[2]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );

    // Accept discard
    accept = true;
    await page.getByRole("button", { name: /Back/ }).click();
    await expect.poll(() => prompts.length).toBe(4);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("In-pane navigation guard and failed custom-fields save retention", async ({
    page,
  }) => {
    const prompts = [];
    let accept = false;
    page.on("dialog", (dialog) => {
      prompts.push(dialog.message());
      return accept ? dialog.accept() : dialog.dismiss();
    });

    // Attach task 2 as a dependency on task 1 so there is an in-pane task chip to click
    await page.goto("/tasks/1");
    const overlay = page.getByRole("dialog", { name: /^Task 1:/ });
    await expect(overlay).toBeVisible();

    const depRes = await page.request.post("/api/tasks/1/depends-on", {
      data: { id: "2", attach: true },
    });
    expect(depRes.ok()).toBe(true);
    await page.reload();
    await expect(overlay).toBeVisible();

    // 1. Dirty Task Edit and click "File a task with these fields"
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const titleInput = page.getByLabel("Title", { exact: true });
    await titleInput.fill("Unsaved task edit before in-pane link");

    await page
      .getByRole("link", { name: "File a task with these fields" })
      .click();
    await expect.poll(() => prompts.length).toBe(1);
    expect(prompts[0]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();
    await expect(titleInput).toHaveValue(
      "Unsaved task edit before in-pane link",
    );

    // 2. Dirty Custom Fields and click dependency chip for task 2
    await page
      .getByRole("button", { name: "Edit fields", exact: true })
      .click();
    const requestedByInput = page.getByLabel("Requested by");
    await requestedByInput.fill("Unsaved customer before dep click");

    // Simulate failed PATCH on Save fields and verify editor stays open with typed value
    await page.route("**/api/tasks/1", async (route) => {
      if (route.request().method() === "PATCH") {
        await route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "simulated save failure" }),
        });
      } else {
        await route.continue();
      }
    });
    await page.getByRole("button", { name: "Save fields" }).click();
    await expect(page.getByText(/simulated save failure/)).toBeVisible();
    await expect(requestedByInput).toBeVisible();
    await expect(requestedByInput).toHaveValue(
      "Unsaved customer before dep click",
    );
    await page.unroute("**/api/tasks/1");

    // Clicking the dependency chip for task 2 prompts before discarding unsaved edits
    const depChip = page
      .locator("fieldset", { hasText: "Depends on" })
      .getByRole("button")
      .filter({ hasText: /^2 / })
      .first();
    await depChip.click();
    await expect.poll(() => prompts.length).toBe(2);
    expect(prompts[1]).toBe(
      "You have unsaved changes here. Leave and discard them?",
    );
    await expect(overlay).toBeVisible();

    // Accepting the prompt navigates to task 2 cleanly
    accept = true;
    await depChip.click();
    await expect.poll(() => prompts.length).toBe(3);
    await expect(page).toHaveURL(/\/tasks\/2$/);

    // Clean up attached dependency
    await page.request.post("/api/tasks/1/depends-on", {
      data: { id: "2", attach: false },
    });
  });

  test("React Hook order invariant: tasks with no declared or stored custom fields across repeated renders (invariant 310)", async ({
    page,
  }) => {
    // Collect all console errors and warnings to check for React invariant 310
    const consoleErrors = [];
    page.on("console", (msg) => {
      if (msg.type() === "error") {
        consoleErrors.push(msg.text());
      }
    });

    // Create a task with no custom fields via API
    const res = await page.request.post("/api/tasks", {
      data: {
        title: "Task with zero custom fields",
        description:
          "Checking React hook order stability across repeated renders",
        team: "default",
      },
    });
    expect(res.ok()).toBe(true);
    const newTask = await res.json();
    const taskId = newTask.id;

    // Navigate to the newly created task
    await page.goto(`/tasks/${taskId}`);
    const overlay = page.getByRole("dialog", {
      name: new RegExp(`^Task ${taskId}:`),
    });
    await expect(overlay).toBeVisible();

    // Repeated renders: toggle Edit mode on and off repeatedly, switch tabs/panes, reload
    for (let i = 0; i < 5; i++) {
      await page.getByRole("button", { name: "Edit", exact: true }).click();
      await expect(page.getByLabel("Title")).toBeVisible();
      await page.getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(
        page.getByRole("heading", { name: /Task with zero custom fields/ }),
      ).toBeVisible();
    }

    // Navigate away and back across repeated transitions
    await page.getByRole("button", { name: /Back/ }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await page.goto(`/tasks/${taskId}`);
    await expect(overlay).toBeVisible();

    // Verify absence of React invariant 310 or render error exceptions
    const invariant310Errors = consoleErrors.filter(
      (err) =>
        err.includes("310") ||
        err.toLowerCase().includes("rendered fewer hooks") ||
        err.toLowerCase().includes("rendered more hooks"),
    );
    expect(invariant310Errors).toEqual([]);
    expect(
      consoleErrors.filter((err) => err.startsWith("grain: render failed")),
    ).toEqual([]);
  });
});
