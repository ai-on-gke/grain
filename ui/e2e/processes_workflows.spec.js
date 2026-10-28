import { test, expect } from "./fixtures.js";

// The schedule, template and suite workflows, which were three files when
// each kind had a page of its own. They are one Processes page now
// (docs/design/settings-panel.md §5), and each kind's old path is that
// page narrowed to the kind, which is where each of these starts.

// Comprehensive UI workflows for Schedules: creating schedules with custom
// cadence/recurrence, overlap policy, base branch, capabilities, custom fields;
// triggering "Run now", toggling "Pause"/"Resume", in-place editing, and deleting.

test.describe("Schedule Management and Execution Workflows", () => {
  test("creates a schedule, executes Run now, pauses/resumes, edits, and deletes it", async ({
    page,
  }) => {
    const stamp = Date.now();
    const scheduleTitle = `Nightly Maintenance ${stamp}`;
    const updatedTitle = `${scheduleTitle} (Updated)`;

    // 1. Navigate to /schedules
    await page.goto("/processes?kind=schedule");
    await expect(
      page.getByRole("heading", { name: "Processes" }),
    ).toBeVisible();

    // 2. Click "+ New schedule" button
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toBeVisible();

    // 3. Fill in Title and Schedule details
    await page.getByLabel("Title", { exact: true }).fill(scheduleTitle);

    // 4. Select Recurrence Kind (Every N hours)
    await page.getByLabel("Repeat").click();
    await page.getByRole("option", { name: "Every N hours" }).click();
    await page.getByLabel("Every N hours").fill("6");

    // 5. Select Overlap policy (Start it anyway)
    await page.getByLabel("If the last run has not finished").click();
    await page.getByRole("option", { name: "Start it anyway" }).click();

    // 6. Enter description
    await page
      .getByLabel("Description", { exact: true })
      .fill("Automated maintenance task for periodic sanity checks.");

    // 7. Select target repo
    const repoSelect = page.locator("select[name='repo']");
    await expect(repoSelect).toBeVisible();
    // By name: other files on this worker's demo add repos of their own.
    await repoSelect.selectOption("acme/widgets");

    // 8. Click "Add schedule" button to submit
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);

    // 9. Verify schedule appears in the schedules list
    const scheduleRow = page.locator(".process-row-schedule", {
      hasText: scheduleTitle,
    });
    await expect(scheduleRow).toBeVisible();
    await expect(scheduleRow).toContainText("every 6h");
    await expect(scheduleRow).toContainText("Can overlap");

    // 10. Click schedule row to open Edit schedule overlay
    await scheduleRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();

    // 11. Click "Run now" button with confirmation dialog
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Run now" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);

    // 12. Reopen schedule and toggle Pause / Resume
    await scheduleRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();

    // Click "Pause"
    await page.getByRole("button", { name: "Pause" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
    await expect(scheduleRow).toContainText("Paused");

    // Click row again and click "Resume"
    await scheduleRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Resume" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
    await expect(
      scheduleRow.locator(".MuiChip-root", { hasText: "Paused" }),
    ).toHaveCount(0);

    // 13. Edit schedule title and Save
    await scheduleRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();
    await page.getByLabel("Title", { exact: true }).fill(updatedTitle);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);

    // Verify updated title
    const updatedRow = page.locator(".process-row-schedule", {
      hasText: updatedTitle,
    });
    await expect(updatedRow).toBeVisible();

    // 14. Delete schedule with confirmation dialog
    await updatedRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
    await expect(
      page.locator(".process-row-schedule", { hasText: updatedTitle }),
    ).toHaveCount(0);
  });
});

// Comprehensive UI workflows for Templates: creating templates with declared fields,
// using templates via "Use…" button to file tasks with field substitution,
// editing templates in place, and deleting templates.

test.describe("Template Management and Task Instantiation Workflows", () => {
  test("creates a template with declared inputs, instantiates it into a task, edits, and deletes it", async ({
    page,
  }) => {
    const stamp = Date.now();
    const templateName = `E2E Service Template ${stamp}`;
    const taskTitlePattern =
      "Deploy service {{service_name}} to {{target_env}}";
    const descPattern = "Deploying {{service_name}} on {{target_env}} cluster.";

    // 1. Navigate to /templates
    await page.goto("/processes?kind=template");
    await expect(
      page.getByRole("heading", { name: "Processes" }),
    ).toBeVisible();

    // 2. Click "+ New template" button
    await page.getByRole("button", { name: "+ New template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();

    // 3. Fill in template properties
    await page.getByLabel("Name", { exact: true }).fill(templateName);
    await page.getByLabel("Task title", { exact: true }).fill(taskTitlePattern);
    await page.getByLabel("Description", { exact: true }).fill(descPattern);

    // 4. Add declared template input fields
    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 1 name").fill("service_name");
    await page.getByLabel("Input 1 label").fill("Service Name");
    await page.getByRole("checkbox", { name: "Required" }).first().check();

    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 2 name").fill("target_env");
    await page.getByLabel("Input 2 label").fill("Target Environment");
    await page.getByLabel("Input 2 options").fill("staging\nproduction");
    await page.getByLabel("Input 2 default").fill("staging");

    // 5. Submit template form
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);

    // 6. Verify template appears in the templates list
    const templateRow = page.locator(".process-row-template", {
      hasText: templateName,
    });
    await expect(templateRow).toBeVisible();
    await expect(templateRow).toContainText(taskTitlePattern);

    // 7. Click "Use…" button on the template row
    await templateRow.getByRole("button", { name: "Use…" }).click();
    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).toBeVisible();

    // 8. Verify the declared fields are rendered in the UseTemplateOverlay
    await expect(page.getByLabel("Service Name")).toBeVisible();
    await expect(page.getByLabel("Target Environment")).toBeVisible();

    // 9. Fill in the template fields
    await page.getByLabel("Service Name").fill("payment-gateway");
    // Target Environment is already default "staging"
    await page.getByLabel(/Target Environment/).click();
    await page.getByRole("option", { name: "production" }).click();

    // 10. Click Create task button
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).toHaveCount(0);

    // 11. Verify the task was created and filed with interpolated values
    const expectedTitle = "Deploy service payment-gateway to production";
    await expect(page.locator(".detail-header h2")).toContainText(
      expectedTitle,
    );
    await expect(page.locator(".description")).toContainText(
      "Deploying payment-gateway on production cluster.",
    );

    // 12. Go back to /templates and edit the template
    await page.goto("/processes?kind=template");
    const editRow = page.locator(".process-row-template", {
      hasText: templateName,
    });
    await editRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();

    const updatedDescPattern = descPattern + " Updated instructions.";
    await page
      .getByLabel("Description", { exact: true })
      .fill(updatedDescPattern);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toHaveCount(0);

    // 13. Reopen and delete the template
    await editRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toHaveCount(0);
    await expect(
      page.locator(".process-row-template", { hasText: templateName }),
    ).toHaveCount(0);
  });
});

// Comprehensive UI workflows for Suites: creating suites with templates,
// triggering suite runs via "Run…" button, viewing suite run progress pane,
// editing suites, and deleting suites.

test.describe("Suite Management and Suite Execution Workflows", () => {
  test("creates a suite, triggers a run via UI buttons, views run progress, edits, and deletes it", async ({
    page,
  }) => {
    const stamp = Date.now();
    const templateName = `E2E Suite Step Template ${stamp}`;
    const suiteName = `E2E Deployment Suite ${stamp}`;

    // 1. Seed a template first so our suite has a template to pick
    const tmplRes = await page.request.post("/api/templates", {
      data: {
        name: templateName,
        title: `Run suite step ${stamp}`,
        description: "Executing suite step task.",
      },
    });
    expect(tmplRes.ok()).toBe(true);
    const tmpl = await tmplRes.json();

    // 2. Navigate to /suites
    await page.goto("/processes?kind=suite");
    await expect(
      page.getByRole("heading", { name: "Processes" }),
    ).toBeVisible();

    // 3. Click "+ New suite" button
    await page.getByRole("button", { name: "+ New suite" }).click();
    await expect(
      page.getByRole("heading", { name: "New suite" }),
    ).toBeVisible();

    // 4. Fill in Suite metadata
    await page.getByLabel("Name", { exact: true }).fill(suiteName);

    // 5. Select template
    await page.getByLabel("Templates", { exact: true }).click();
    await page.getByRole("option", { name: templateName }).click();
    await page.keyboard.press("Escape");

    // 6. Submit Suite creation form
    await page.getByRole("button", { name: "Add suite" }).click();
    await expect(page.getByRole("heading", { name: "New suite" })).toHaveCount(
      0,
    );

    // 7. Verify suite appears in the suites list with template count chip
    const suiteRow = page.locator(".process-row-suite", { hasText: suiteName });
    await expect(suiteRow).toBeVisible();
    await expect(suiteRow).toContainText("1 template");

    // 8. Click "Run…" button on the suite row
    await suiteRow.getByRole("button", { name: "Run…" }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toBeVisible();

    // 9. Fill in Repo and Branch in SuiteRunOverlay
    // By name, not position: the list is every repo grain knows of, and
    // specs running beside this one file tasks on repos (acme/innovate-app)
    // that are not on the demo's target list, which a suite run refuses.
    await page.getByLabel(/Repo/).selectOption("acme/widgets");
    await page.getByLabel("Branch").fill("main");

    // Settle the branch before reaching for Run. The field commits on
    // blur, which re-points the "What each pass files" preview: the panel
    // empties while it refetches and comes back when the answer lands.
    // Left to the click, that blur happens on Run's own mousedown, the
    // button moves before mouseup, and the click never arrives -- the
    // overlay simply stays open.
    const composed = page.waitForResponse(
      (res) =>
        res.url().includes("/composition?") &&
        new URL(res.url()).searchParams.get("base") === "main",
    );
    await page.getByLabel("Branch").blur();
    await composed;

    // 10. Click Run button to submit suite run
    const started = page.waitForResponse(
      (res) =>
        res.url().endsWith("/api/suite-runs") &&
        res.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Run", exact: true }).click();
    const startRes = await started;
    expect(
      startRes.ok(),
      `POST /api/suite-runs answered ${startRes.status()}: ${await startRes.text()}`,
    ).toBe(true);
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).not.toBeVisible();

    // 11. Verify the run appears under Recent runs on /suites
    const runRow = page
      .locator(".suite-run-row", { hasText: suiteName })
      .first();
    await expect(runRow).toBeVisible();

    // 12. Click on the run row to open SuiteRunPane
    await runRow.click();
    await expect(
      page
        .locator(".suite-run-pane, .suite-run-detail, .pane-header, h2, h3")
        .first(),
    ).toBeVisible();

    // 13. Go back to /suites and edit the suite
    await page.goto("/processes?kind=suite");
    const editSuiteRow = page.locator(".process-row-suite", {
      hasText: suiteName,
    });
    await editSuiteRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit suite" }),
    ).toBeVisible();

    const updatedSuiteName = `${suiteName} Updated`;
    await page.getByLabel("Name", { exact: true }).fill(updatedSuiteName);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("heading", { name: "Edit suite" })).toHaveCount(
      0,
    );

    // 14. Verify updated name on the row
    await expect(
      page.locator(".process-row-suite", { hasText: updatedSuiteName }),
    ).toBeVisible();

    // 15. Reopen and delete the suite
    await page
      .locator(".process-row-suite", { hasText: updatedSuiteName })
      .click();
    await expect(
      page.getByRole("heading", { name: "Edit suite" }),
    ).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByRole("heading", { name: "Edit suite" })).toHaveCount(
      0,
    );
    await expect(
      page.locator(".process-row-suite", { hasText: updatedSuiteName }),
    ).toHaveCount(0);
  });
});
