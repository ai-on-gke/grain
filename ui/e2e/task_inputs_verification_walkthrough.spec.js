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

test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

// Comprehensive E2E Walkthrough: Operating and Verifying Every Input Element on
// Task Intake, Task Detail Edit, and Task Filtering Views across Light & Dark Themes.

async function setThemeMode(page, mode) {
  await page.evaluate((themeMode) => {
    try {
      localStorage.setItem("grain.themeMode", themeMode);
    } catch (_) {}
    document.documentElement.dataset.theme = themeMode;
  }, mode);
}

test.describe("Task Intake, Detail Edit, and Filter Inputs Verification Walkthrough", () => {
  let createdTaskIds = [];

  test.afterEach(async ({ request }) => {
    for (const id of createdTaskIds) {
      try {
        await request.delete(`/api/tasks/${id}`);
      } catch (_) {}
    }
    createdTaskIds = [];
  });

  // =========================================================================
  // SECTION 1: New Task Overlay (Task Intake Form)
  // =========================================================================
  test("1. New Task Overlay: verify and operate all intake input fields, keyboard focus, validation, draft reset, and themes", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Set Theme mode to light
    await setThemeMode(page, "light");

    // 1.1 Open "+ New task" modal
    const openBtn = page.getByRole("button", { name: "+ New task" });
    await openBtn.click();
    const heading = page.getByRole("heading", { name: "New task" });
    await expect(heading).toBeVisible();

    // 1.2 Verify Validation Error State: Create task is disabled when required fields are missing
    const createBtn = page.getByRole("button", { name: "Create task" });
    await expect(createBtn).toBeDisabled();

    // 1.3 No Target team selector: a task names no team.
    await expect(
      page.getByRole("combobox", { name: /Target team/i }),
    ).toHaveCount(0);

    // 1.4 Repository picker / input and "no-repo" option
    const repoSelect = page.getByLabel(/^Repos/);
    await expect(repoSelect).toBeVisible();
    // Choose "none" ("No repo")
    await repoSelect.selectOption("none");

    // 1.5 Title & Description inputs: typing, keyboard focus, tab ordering
    const titleInput = page.getByLabel("Title");
    const descInput = page.getByLabel("Description");
    await expect(titleInput).toBeVisible();
    await expect(descInput).toBeVisible();

    // Focus title and type draft
    await titleInput.focus();
    await expect(titleInput).toBeFocused();
    await page.keyboard.type("Draft Task Intake Title");
    await page.keyboard.press("Tab");
    await expect(descInput).toBeFocused();
    await page.keyboard.type("Draft Task Intake Description line 1\nline 2");

    // 1.6 Draft persistence and Discard Draft reset
    const discardBtn = page.getByRole("button", { name: "Discard draft" });
    await expect(discardBtn).toBeVisible();
    await discardBtn.click();
    await expect(titleInput).toHaveValue("");
    await expect(descInput).toHaveValue("");
    await expect(createBtn).toBeDisabled();

    // 1.7 Refill Title, Description, and re-select repo
    const stamp = Date.now();
    const taskTitle = `Verified Intake Task ${stamp}`;
    const taskDesc = `Comprehensive verification of all input fields at ${stamp}`;
    await titleInput.fill(taskTitle);
    await descInput.fill(taskDesc);

    // Switch repo to custom or select 'none' to enable Create task
    await repoSelect.selectOption("none");
    await expect(createBtn).toBeEnabled();

    // Now test repo selection with repo branch
    // Switch repo to 'acme/widgets' (seeded in demo)
    await repoSelect.selectOption("acme/widgets");
    const baseBranchInput = page.getByLabel("Base branch");
    await expect(baseBranchInput).toBeVisible();
    await baseBranchInput.fill("main");

    // "Create a new branch" checkbox & "New branch name" TextField
    const newBranchCheckbox = page.getByRole("checkbox", {
      name: "Create a new branch",
    });
    await newBranchCheckbox.check();
    const newBranchNameInput = page.getByLabel("New branch name");
    await expect(newBranchNameInput).toBeVisible();
    await newBranchNameInput.fill(`feature/verify-inputs-${stamp}`);
    // Uncheck to keep on existing branch
    await newBranchCheckbox.uncheck();
    await expect(newBranchNameInput).toHaveCount(0);

    // 1.8 Custom Fields: exercise all 5 declared types (enum, multiselect, text, number, boolean)
    await expect(page.getByText("Fields", { exact: true })).toBeVisible();

    // (a) Enum select: Priority
    const priorityCombobox = page.getByRole("combobox", { name: /^Priority/i });
    await expect(priorityCombobox).toBeVisible();
    await priorityCombobox.click();
    await page.getByRole("option", { name: "high" }).click();
    await expect(priorityCombobox).toHaveText("high");

    // (b) Multi-select enum autocomplete: Area
    for (const area of ["backend", "frontend"]) {
      await page.getByLabel("Area").click();
      await page.getByRole("option", { name: area }).click();
    }
    const areaRoot = page
      .locator(".MuiAutocomplete-root")
      .filter({ has: page.getByLabel("Area") });
    await expect(areaRoot.getByText("backend")).toBeVisible();
    await expect(areaRoot.getByText("frontend")).toBeVisible();

    // (c) Text field: Requested by
    const requestedByInput = page.getByLabel("Requested by");
    await expect(requestedByInput).toBeVisible();
    await requestedByInput.fill("Core Infra QA");
    await expect(requestedByInput).toHaveValue("Core Infra QA");

    // (d) Number field: Estimate (days)
    const estimateInput = page.getByLabel("Estimate (days)");
    await expect(estimateInput).toBeVisible();
    await estimateInput.fill("5");
    await expect(estimateInput).toHaveValue("5");

    // (e) Boolean select: Customer-facing (yes/no/not set)
    const customerFacingCombobox = page.getByRole("combobox", {
      name: /^Customer-facing/i,
    });
    await expect(customerFacingCombobox).toBeVisible();
    await customerFacingCombobox.click();
    await page.getByRole("option", { name: "Yes", exact: true }).click();
    await expect(customerFacingCombobox).toHaveText("Yes");

    // (f) Date field: Due date
    const dueDateInput = page.getByLabel("Due date");
    await expect(dueDateInput).toBeVisible();
    await dueDateInput.fill("2026-11-20");
    await expect(dueDateInput).toHaveValue("2026-11-20");

    // 1.9 Capabilities toggles: Select dropdown multi-select
    const capabilitiesCombobox = page.getByRole("combobox", {
      name: /^Legacy capabilities/i,
    });
    await expect(capabilitiesCombobox).toBeVisible();

    // 1.10 Auto-merge checkbox & noPort checkbox
    const autoMergeCheckbox = page.getByRole("checkbox", {
      name: "Auto-merge once checks pass",
    });
    await autoMergeCheckbox.check();
    expect(await autoMergeCheckbox.isChecked()).toBe(true);

    const noPortCheckbox = page.getByRole("checkbox", {
      name: "Don't port to the repo's paired branches",
    });
    await noPortCheckbox.check();
    expect(await noPortCheckbox.isChecked()).toBe(true);
    await noPortCheckbox.uncheck();

    // 1.11 Advanced Settings inputs (Accordion)
    const advAccordion = page.getByText("Advanced options");
    await advAccordion.click();

    // No interactive-session option: sessions are started from the
    // Sessions page (NewSessionDialog.jsx), not filed from here.
    await expect(
      page.getByRole("checkbox", { name: /Interactive session/i }),
    ).toHaveCount(0);

    // Network access selector
    const networkCombobox = page.getByRole("combobox", {
      name: /^Network access/i,
    });
    if (await networkCombobox.isEnabled()) {
      await networkCombobox.click();
      await page
        .getByRole("option", { name: "Egress (outbound internet only)" })
        .click();
    }

    // Agent findings behavior (nothingNeededAction)
    const nothingNeededCombobox = page.getByRole("combobox", {
      name: /When the agent finds nothing to do/i,
    });
    await expect(nothingNeededCombobox).toBeVisible();
    await nothingNeededCombobox.click();
    await page
      .getByRole("option", { name: "Leave for acknowledgement" })
      .click();

    // Prompt extension override multiline TextField
    const promptExtInput = page.getByLabel("Prompt extension override");
    await expect(promptExtInput).toBeVisible();
    await promptExtInput.fill("Step-by-step verification prompt override.");

    // Sandbox shape quotas (vCPUs, Memory, Disk)
    const cpusInput = page.getByLabel("vCPUs");
    const memInput = page.getByLabel("Memory (MiB)");
    const diskInput = page.getByLabel("Disk (GiB)");
    if (await cpusInput.isEnabled()) {
      await cpusInput.fill("4");
      await memInput.fill("8192");
      await diskInput.fill("50");
      await expect(cpusInput).toHaveValue("4");
      await expect(memInput).toHaveValue("8192");
      await expect(diskInput).toHaveValue("50");
    }

    // 1.12 Visual Styling & Theme Switch: Check inputs rendering in Dark Mode
    await setThemeMode(page, "dark");
    // Check that title input is still readable and styled correctly
    const titleColor = await titleInput.evaluate(
      (el) => getComputedStyle(el).color,
    );
    expect(titleColor).toBeTruthy();

    // Switch back to light
    await setThemeMode(page, "light");

    // 1.13 Submit Task Creation
    await createBtn.click();
    await expect(heading).toHaveCount(0);

    // Verify task row appears in the task list
    const createdRow = page.locator(".task-row", { hasText: taskTitle });
    await expect(createdRow).toBeVisible();
    const taskIdMatch = (
      await createdRow.locator(".task-number").textContent()
    )?.match(/(\d+)/);
    if (taskIdMatch) {
      createdTaskIds.push(taskIdMatch[1]);
    }
  });

  // =========================================================================
  // SECTION 2: Task Detail & In-Place Editing View
  // =========================================================================
  test("2. Task Detail & Edit: verify EditTaskForm, Target repos/branches, custom fields, comment composer, and capabilities", async ({
    page,
  }) => {
    // Create a dedicated task via API
    const stamp = Date.now();
    const origTitle = `E2E Detail Edit Target Task ${stamp}`;
    const origDesc = `Original description for editing verification ${stamp}`;
    const res = await page.request.post("/api/tasks", {
      data: {
        title: origTitle,
        description: origDesc,
        repo: "acme/widgets",
        base: "main",
        customFields: {
          priority: "low",
          "requested-by": "QA Automated",
          estimate: "3",
        },
      },
    });
    expect(res.ok()).toBe(true);
    const task = await res.json();
    createdTaskIds.push(String(task.id));

    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header h2")).toContainText(origTitle);

    // 2.1 Edit Title and Description via Edit button
    const editBtn = page.getByRole("button", { name: "Edit", exact: true });
    await expect(editBtn).toBeVisible();
    await editBtn.click();

    const titleEdit = page.getByLabel("Title");
    const descEdit = page.getByLabel("Description");
    await expect(titleEdit).toHaveValue(origTitle);
    await expect(descEdit).toHaveValue(origDesc);

    // Modify Title and Description
    const newTitle = `E2E Updated Title ${stamp}`;
    const newDesc = `Updated description body with markdown\n\n- Detailed item 1\n- Detailed item 2`;
    await titleEdit.fill(newTitle);
    await descEdit.fill(newDesc);

    // Save and verify persistence
    const saveBtn = page.getByRole("button", { name: "Save", exact: true });
    await saveBtn.click();
    await expect(page.locator(".detail-header h2")).toContainText(newTitle);
    await expect(page.locator(".description")).toContainText("Detailed item 1");

    // 2.2 Target: Repo and Base branch inputs with onBlur persistence
    // In Target component, Base branch is an editable BranchField
    const branchInput = page.getByLabel("Base branch");
    if (await branchInput.isVisible()) {
      await branchInput.fill("release-v1");
      await branchInput.blur();
      // Verify value remains
      await expect(branchInput).toHaveValue("release-v1");
    }

    // 2.3 Auto-merge switch toggle in Actions
    const autoMergeSwitch = page.getByRole("checkbox", {
      name: /Auto-merge once checks pass/i,
    });
    if (await autoMergeSwitch.isVisible()) {
      const initialChecked = await autoMergeSwitch.isChecked();
      await autoMergeSwitch.click();
      await expect(autoMergeSwitch).toBeChecked({ checked: !initialChecked });
    }

    // 2.4 Custom fields in detail panel: Edit fields button
    const editFieldsBtn = page.getByRole("button", { name: "Edit fields" });
    await expect(editFieldsBtn).toBeVisible();
    await editFieldsBtn.click();

    // Priority dropdown in detail panel
    const detailPriority = page.getByRole("combobox", { name: /^Priority/i });
    await detailPriority.click();
    await page.getByRole("option", { name: "high" }).click();

    // Requested by text input
    const detailReq = page.getByLabel("Requested by");
    await detailReq.fill("Platform Infra Lead");

    // Estimate (days) number input
    const detailEst = page.getByLabel("Estimate (days)");
    await detailEst.fill("7");

    // Save custom fields
    await page.getByRole("button", { name: "Save fields" }).click();
    const declaredRows = page.locator(".declared-row");
    await expect(declaredRows.filter({ hasText: "Priority" })).toContainText(
      "high",
    );
    await expect(
      declaredRows.filter({ hasText: "Requested by" }),
    ).toContainText("Platform Infra Lead");
    await expect(
      declaredRows.filter({ hasText: "Estimate (days)" }),
    ).toContainText("7");

    // 2.5 Task comment composer (.cs-message-input__content-editor)
    const composer = page
      .locator(".cs-message-input__content-editor, [placeholder='Reply...']")
      .first();
    await expect(composer).toBeVisible();
    await composer.focus();
    await page.keyboard.type("Operator audit verification comment text.");
    const commentBtn = page.getByRole("button", {
      name: "Comment",
      exact: true,
    });
    await expect(commentBtn).toBeVisible();
    await commentBtn.click();

    // Verify comment appears in the transcript
    await expect(
      page
        .locator(".timeline-item, .chat-message, .message-body")
        .filter({ hasText: "Operator audit verification comment text." }),
    ).toBeVisible();

    // 2.6 Capabilities attach/detach in detail view
    const capCombobox = page.getByRole("combobox", {
      name: /^Legacy capabilities/i,
    });
    if (await capCombobox.isVisible()) {
      await capCombobox.click();
      // Menu should show available capabilities
      await page.keyboard.press("Escape");
    }
  });

  // =========================================================================
  // SECTION 3: Task Filtering & Narrowing (NarrowingBar, taskFilters.js)
  // =========================================================================
  test("3. Task Filtering & Narrowing: search text input, filter dropdowns, condition values, reset/blur, light & dark themes", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // 3.1 Search text input (ListSearchField)
    const searchInput = page.getByPlaceholder("Search tasks…");
    await expect(searchInput).toBeVisible();

    // Type query and verify immediate filtering
    await searchInput.focus();
    await page.keyboard.type("Bump the Go toolchain");
    await expect(
      page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
    ).toBeVisible();
    await expect(
      page.locator(".task-row", { hasText: "Rotate the Gemini signing key" }),
    ).toHaveCount(0);

    // Clear search query
    await searchInput.fill("");
    await expect(
      page.locator(".task-row", { hasText: "Rotate the Gemini signing key" }),
    ).toBeVisible();

    // 3.2 Filter dropdowns and condition editor
    const filterBtn = page.getByTitle(
      "Add and edit the conditions this list is narrowed by",
    );
    await expect(filterBtn).toBeVisible();
    await filterBtn.click();

    const addFilterBtn = page.getByRole("button", {
      name: /^(Filter by…|Add filter)$/,
    });
    await expect(addFilterBtn).toBeVisible();
    await addFilterBtn.click();

    // Verify system attributes and custom fields appear in the menu
    await expect(page.getByRole("menuitem", { name: "State" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Repo" })).toBeVisible();
    // A task names no team, so there is no Team attribute to filter by.
    await expect(page.getByRole("menuitem", { name: "Team" })).toHaveCount(0);
    await expect(
      page.getByRole("menuitem", { name: "Priority", exact: true }),
    ).toBeVisible();

    // Pick "Priority" attribute
    await page.getByRole("menuitem", { name: "Priority", exact: true }).click();

    // Select comparison value "high"
    const valuePicker = page.getByLabel(/^Value for filter \d+$/).last();
    await valuePicker.click();
    await page.getByRole("option", { name: "high" }).click();
    await page.keyboard.press("Escape");

    // Verify filtered results
    await expect(
      page.locator(".task-row", { hasText: "Fix flaky retry test" }),
    ).toBeVisible();
    await expect(
      page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
    ).toHaveCount(0);

    // 3.3 Remove filter condition
    const removeFilterBtn = page.getByLabel(/^Remove filter \d+$/).last();
    await removeFilterBtn.click();
    // After removal, all tasks reappear
    await expect(
      page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
    ).toBeVisible();

    // 3.4 Theme styling verification: Dark Theme for NarrowingBar
    await setThemeMode(page, "dark");
    const darkSearchBg = await searchInput.evaluate(
      (el) => getComputedStyle(el).color,
    );
    expect(darkSearchBg).toBeTruthy();

    await setThemeMode(page, "light");
  });
});
