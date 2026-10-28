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

test.describe("Inputs in Templates, Schedules, and Suites Automation Walkthrough", () => {
  async function seedOne(page, path, data) {
    const res = await page.request.post(path, { data });
    expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
    return res.json();
  }

  test("1. Template Configuration: inputs, declared fields, branch validation, and persistence", async ({
    page,
  }) => {
    // 1.1 Navigate to Templates list
    await page.goto("/templates");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // 1.2 Open New template dialog
    await page.getByRole("button", { name: "+ New template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();

    const stamp = Date.now();
    const tmplName = `tmpl-inputs-${stamp}`;
    const tmplTitle = `Deploy {{service}} flag {{feature}} for {{user_id}}`;
    const tmplDesc = `Deploy service {{service}} with feature flag {{feature}} for user {{user_id}}.\nAdditional notes: {{notes}}`;

    // Name and Title text inputs (TextField)
    await page.locator('input[name="name"]').fill(tmplName);
    await page.locator('input[name="title"]').fill(tmplTitle);

    // Description multiline textarea (TextField multiline)
    await page.locator('textarea[name="description"]').fill(tmplDesc);

    // Declared template input fields (TemplateFieldsEditor)
    // Field 1: selector field with options and default
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 1 name").fill("service");
    await page.getByLabel("Input 1 label").fill("Service Name");
    await page.getByLabel("Input 1 options").fill("auth\nbilling\nweb");
    await page.getByLabel("Input 1 default").fill("auth");

    // Field 2: selector field with required flag
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 2 name").fill("feature");
    await page.getByLabel("Input 2 label").fill("Feature Flag");
    await page.getByLabel("Input 2 options").fill("dark_mode\nbeta\nv2_api");
    await page.getByLabel("Input 2 default").fill("dark_mode");

    // Field 3: text field with required flag and NO default value
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 3 name").fill("user_id");
    await page.getByLabel("Input 3 label").fill("Target User ID");
    await page
      .locator(".template-field-row")
      .nth(2)
      .getByRole("checkbox", { name: "Required" })
      .check();

    // Field 4: optional text field
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 4 name").fill("notes");
    await page.getByLabel("Input 4 label").fill("Release Notes");

    // Field 5: temporary input to verify remove button
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 5 name").fill("temp_field");
    await expect(page.locator(".template-field-row")).toHaveCount(5);
    await page
      .locator(".template-field-row")
      .nth(4)
      .getByRole("button", { name: /Remove input/ })
      .click();
    await expect(page.locator(".template-field-row")).toHaveCount(4);

    // Target Repo & Base Branch
    const repoSelect = page.locator('select[name="repo"]').first();
    await expect(repoSelect).toBeVisible();
    await repoSelect.selectOption("acme/widgets");

    // Test malformed branch validation
    await page.locator('input[name="base"]').first().fill("broken..branch");
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(page.getByText(/not a usable branch name/i)).toBeVisible();

    // Fix base branch to valid branch
    await page.locator('input[name="base"]').first().fill("main");

    // Auto-merge checkbox toggle
    const autoMergeCheckbox = page.getByRole("checkbox", {
      name: /Auto-merge/i,
    });
    if (!(await autoMergeCheckbox.isChecked())) {
      await autoMergeCheckbox.check();
    }

    // Save template
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);

    // 1.3 Verify row appears in templates list and verify persistence on reopen
    const templateRow = page.locator(".process-row-template", {
      hasText: tmplName,
    });
    await expect(templateRow).toBeVisible();

    await templateRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(tmplName);
    await expect(page.locator('input[name="title"]')).toHaveValue(tmplTitle);
    await expect(page.locator('textarea[name="description"]')).toHaveValue(
      tmplDesc,
    );
    await expect(page.locator('input[name="base"]').first()).toHaveValue(
      "main",
    );
    await expect(autoMergeCheckbox).toBeChecked();

    // Verify all 4 declared fields persisted correctly
    await expect(page.getByLabel("Input 1 name")).toHaveValue("service");
    await expect(page.getByLabel("Input 1 label")).toHaveValue("Service Name");
    await expect(page.getByLabel("Input 1 options")).toHaveValue(
      "auth\nbilling\nweb",
    );
    await expect(page.getByLabel("Input 1 default")).toHaveValue("auth");

    await expect(page.getByLabel("Input 2 name")).toHaveValue("feature");
    await expect(page.getByLabel("Input 2 label")).toHaveValue("Feature Flag");

    await expect(page.getByLabel("Input 3 name")).toHaveValue("user_id");
    await expect(
      page
        .locator(".template-field-row")
        .nth(2)
        .getByRole("checkbox", { name: "Required" }),
    ).toBeChecked();

    await expect(page.getByLabel("Input 4 name")).toHaveValue("notes");

    // Close edit modal
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toHaveCount(0);
  });

  test("2. Schedule Configuration: cadence variants, timezone, overlap policy, and validations", async ({
    page,
  }) => {
    await page.goto("/schedules");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // 2.1 Create an unfillable template (required field without default) to verify schedule validation
    const unfillableTmpl = await seedOne(page, "/api/templates", {
      name: `unfillable-${Date.now()}`,
      title: "Unfillable {{req_code}}",
      fields: [
        {
          name: "req_code",
          label: "Required Code",
          required: true,
          default: "",
        },
      ],
    });

    // 2.2 Open New schedule overlay
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toBeVisible();

    const schedTitle = `Audit Schedule ${Date.now()}`;
    const schedDesc = `Automated nightly infrastructure sweep and dependency validation.`;

    // Title and description inputs (when standalone)
    await page.locator('input[name="title"]').fill(schedTitle);
    await page.locator('textarea[name="description"]').fill(schedDesc);

    // Auto-merge checkbox toggle
    const autoMergeCheckbox = page.getByRole("checkbox", {
      name: /Auto-merge once checks pass/i,
    });
    if (!(await autoMergeCheckbox.isChecked())) {
      await autoMergeCheckbox.check();
    }

    // Exercise Recurrence cadence inputs:
    // 2.2.1 "Every N hours" (number input)
    await page.getByRole("combobox", { name: "Repeat" }).click();
    await page.getByRole("option", { name: "Every N hours" }).click();
    const everyHoursInput = page.locator('input[name="everyNHours"]');
    await expect(everyHoursInput).toBeVisible();
    await everyHoursInput.fill("12");

    // 2.2.2 "Daily, at a time" (time input)
    await page.getByRole("combobox", { name: "Repeat" }).click();
    await page.getByRole("option", { name: "Daily, at a time" }).click();
    const timeInput = page.locator('input[name="timeOfDay"]');
    await expect(timeInput).toBeVisible();
    await timeInput.fill("04:30");

    // 2.2.3 "Weekly, on a day and time" (weekday select + time input)
    await page.getByRole("combobox", { name: "Repeat" }).click();
    await page
      .getByRole("option", { name: "Weekly, on a day and time" })
      .click();
    await page.getByRole("combobox", { name: "Day of week" }).click();
    await page.getByRole("option", { name: "Wednesday" }).click();
    await page.locator('input[name="timeOfDay"]').fill("14:15");

    // 2.2.4 "Monthly, on a day and time" (dayOfMonth number input + time input)
    await page.getByRole("combobox", { name: "Repeat" }).click();
    await page
      .getByRole("option", { name: "Monthly, on a day and time" })
      .click();
    const dayOfMonthInput = page.locator('input[name="dayOfMonth"]');
    await expect(dayOfMonthInput).toBeVisible();
    await dayOfMonthInput.fill("20");
    await page.locator('input[name="timeOfDay"]').fill("08:00");

    // Set Timezone selector
    await page.getByRole("combobox", { name: "Time zone" }).click();
    await page
      .getByRole("option", { name: "America/New_York", exact: false })
      .first()
      .click();

    // Overlap policy select
    await page
      .getByRole("combobox", { name: "If the last run has not finished" })
      .click();
    await page.getByRole("option", { name: "Start it anyway" }).click();

    // Target repo & Base branch
    const repoSelect = page.locator('select[name="repo"]').first();
    await repoSelect.selectOption("acme/widgets");

    // 2.3 Verify unfillable template rejection validation:
    // Switch to template-backed task
    await page.getByRole("combobox", { name: "Template" }).click();
    await page.getByRole("option", { name: unfillableTmpl.name }).click();

    // Attempt to save -> server refuses unfillable template
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page
        .getByRole("alert")
        .getByText(/cannot fill it -- give it a default or untick Required/i),
    ).toBeVisible();

    // Detach unfillable template and make standalone schedule with valid repo/branch
    await page.getByRole("combobox", { name: "Template" }).click();
    await page
      .getByRole("option", { name: "None -- fill in the fields below" })
      .click();
    await page.locator('input[name="title"]').fill(schedTitle);

    // Test malformed branch validation
    await page.locator('input[name="base"]').fill("malformed..branch");
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(page.getByText(/not a usable branch name/i)).toBeVisible();

    // Fix base branch to main
    await page.locator('input[name="base"]').fill("main");

    // Save schedule
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);

    // 2.4 Verify persistence
    const schedRow = page.locator(".process-row-schedule", {
      hasText: schedTitle,
    });
    await expect(schedRow).toBeVisible();

    await schedRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();
    await expect(page.locator('input[name="title"]')).toHaveValue(schedTitle);
    await expect(page.locator('input[name="dayOfMonth"]')).toHaveValue("20");
    await expect(page.locator('input[name="timeOfDay"]')).toHaveValue("08:00");
    await expect(page.locator('input[name="base"]')).toHaveValue("main");
    await expect(autoMergeCheckbox).toBeChecked();

    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
  });

  test("3. Suite Configuration and Suite Runs: modes, sequence builder, bounds, and manual run triggers", async ({
    page,
  }) => {
    await page.goto("/suites");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // 3.1 Create test templates to populate the suite sequence
    const tmplA = await seedOne(page, "/api/templates", {
      name: `suite-item-a-${Date.now()}`,
      title: "Step A: Static Analysis",
    });
    const tmplB = await seedOne(page, "/api/templates", {
      name: `suite-item-b-${Date.now()}`,
      title: "Step B: Integration Benchmarks",
    });

    // 3.2 Open New suite dialog
    await page.getByRole("button", { name: "+ New suite" }).click();
    await expect(
      page.getByRole("heading", { name: "New suite" }),
    ).toBeVisible();

    const suiteName = `Suite-Inputs-${Date.now()}`;
    await page.locator('input[name="name"]').fill(suiteName);

    // 3.2.1 Attempt save with no templates selected -> validation error
    await page.getByRole("button", { name: "Add suite" }).click();
    await expect(page.getByText(/needs at least one template/i)).toBeVisible();

    // 3.2.2 Build template sequence using item selector
    const tmplSelect = page.locator("#suite-templates-label + div");
    await tmplSelect.click();
    await page.getByRole("option", { name: tmplA.name }).click();
    await page.getByRole("option", { name: tmplB.name }).click();
    await page.keyboard.press("Escape");

    // 3.2.3 Run mode selector & pass inputs
    const modeSelect = page.locator("#suite-mode-label + div");

    // Mode: until_clean (default) -> maxPasses
    await expect(page.locator('input[name="maxPasses"]')).toBeVisible();
    await page.locator('input[name="maxPasses"]').fill("6");

    // Mode: count -> Number of times
    await modeSelect.click();
    await page
      .getByRole("option", { name: "Run a fixed number of times" })
      .click();
    await expect(page.locator('input[name="count"]')).toBeVisible();
    await page.locator('input[name="count"]').fill("4");

    // Require approval & Auto-merge switches
    const approvalCheckbox = page.getByRole("checkbox", {
      name: /Require approval before each pass/i,
    });
    await approvalCheckbox.check();

    const autoMergeCheckbox = page.getByRole("checkbox", {
      name: /Auto-merge each pull request/i,
    });
    await autoMergeCheckbox.uncheck();

    // Save suite
    await page.getByRole("button", { name: "Add suite" }).click();
    await expect(page.getByRole("heading", { name: "New suite" })).toHaveCount(
      0,
    );

    // 3.3 Verify suite row & persistence
    const suiteRow = page.locator(".process-row-suite", { hasText: suiteName });
    await expect(suiteRow).toBeVisible();

    await suiteRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit suite" }),
    ).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(suiteName);
    await expect(page.locator('input[name="count"]')).toHaveValue("4");
    await expect(approvalCheckbox).toBeChecked();
    await expect(autoMergeCheckbox).not.toBeChecked();

    await page.keyboard.press("Escape");
    await expect(page.getByRole("heading", { name: "Edit suite" })).toHaveCount(
      0,
    );

    // 3.4 Exercise Suite Run manual trigger dialog (SuiteRunOverlay)
    await suiteRow.getByRole("button", { name: "Run…" }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toBeVisible();

    // Verify Suite selector shows our suite
    await expect(page.getByRole("combobox", { name: "Suite" })).toContainText(
      suiteName,
    );

    // Target repo
    const repoSelect = page.locator('select[name="repo"]').first();
    await repoSelect.selectOption("acme/widgets");

    // Test malformed base branch validation in suite run
    const branchInput = page.locator('input[name="base"]');
    await branchInput.fill("invalid..base");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(page.getByText(/not a usable branch name/i)).toBeVisible();

    // Provide valid branch and trigger suite run
    await branchInput.fill("main");
    await page.getByRole("button", { name: "Run", exact: true }).click();

    // Verifies suite run was created: overlay closes and run appears in Recent runs
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);
    const runRow = page.locator(".suite-run-row", { hasText: suiteName });
    await expect(runRow).toBeVisible();
    await runRow.click();
    await expect(page.getByRole("heading", { name: "Pass 1" })).toBeVisible();
  });

  test("4. Template Use Modal: declared field inputs, required validation, and live preview", async ({
    page,
  }) => {
    // 4.1 Create a template declaring both text and select fields
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `use-demo-${stamp}`,
      title: "Deploy {{component}} to {{env}}",
      description:
        "Trigger deployment of {{component}} into {{env}} cluster.\nOperator note: {{operator_note}}",
      fields: [
        {
          name: "component",
          label: "Component",
          options: ["auth-service", "payment-service", "gateway"],
          default: "auth-service",
        },
        {
          name: "env",
          label: "Environment",
          options: ["staging", "canary", "production"],
          default: "staging",
        },
        {
          name: "operator_note",
          label: "Operator Note",
          required: true,
          default: "",
        },
      ],
    });

    // 4.2 Navigate to templates and click "Use…"
    await page.goto("/templates");
    const tmplRow = page.locator(".process-row-template", {
      hasText: tmpl.name,
    });
    await expect(tmplRow).toBeVisible();
    await tmplRow.getByRole("button", { name: "Use…" }).click();

    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).toBeVisible();

    // 4.3 Verify TemplateFieldInputs rendering
    // Select dropdowns for options fields
    await expect(page.getByLabel("Component")).toBeVisible();
    await expect(page.getByLabel("Environment")).toBeVisible();

    // Required text field
    const noteInput = page.getByLabel("Operator Note");
    await expect(noteInput).toBeVisible();
    await expect(page.getByText("{{operator_note}}")).toBeVisible();

    // Unbound template: target repo and base branch inputs
    const repoSelect = page.locator('select[name="repo"]').first();
    await repoSelect.selectOption("acme/widgets");
    await page.locator('input[name="base"]').fill("main");

    // 4.4 Verify required validation when operator note is left empty
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByText(/is required/i)).toBeVisible();

    // 4.5 Fill required field and change select values
    await noteInput.fill("Routine security hotfix verification");

    // Change environment to production
    await page.getByLabel("Environment").click();
    await page.getByRole("option", { name: "production" }).click();

    // Verify live composition preview updates
    await expect(
      page.getByText("Deploy auth-service to production"),
    ).toBeVisible();

    // File task
    await page.getByRole("button", { name: "Create task" }).click();

    // 4.6 Verify created task opens and reflects substituted values
    await expect(page).toHaveURL(/\/tasks\/\d+/);
    await expect(
      page.getByRole("heading", { name: "Deploy auth-service to production" }),
    ).toBeVisible();
    await expect(
      page.getByText("Routine security hotfix verification"),
    ).toBeVisible();
  });
});
