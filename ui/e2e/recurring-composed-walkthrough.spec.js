import { test, expect } from "./fixtures.js";

test.describe("Recurring and Composed Work Automation UI Walkthrough", () => {
  // Helper to create API items directly
  async function seedOne(page, path, data) {
    const res = await page.request.post(path, { data });
    expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
    return res.json();
  }

  test("1. Templates: creation, full configuration, editing, direct link, use-template, and dependency-aware delete", async ({
    page,
  }) => {
    // 1.1 Navigate to Templates list from sidebar rail
    await page.goto("/");
    await page.getByRole("button", { name: /^Processes/ }).click();
    await expect(page).toHaveURL(/\/processes$/);
    // Templates are one kind on the Processes page: narrowed to them, the
    // page is what /templates was (docs/design/settings-panel.md §5.4).
    await page
      .getByTestId("process-kind-filter")
      .getByRole("button", { name: /^Templates / })
      .click();
    await expect(page).toHaveURL(/\/processes\?kind=template$/);
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // Verify search and sort controls exist
    const searchInput = page.getByPlaceholder("Search processes…");
    await expect(searchInput).toBeVisible();
    await searchInput.fill("release");
    await expect(page.locator(".process-row-template")).toHaveCount(1);
    await searchInput.fill("");

    // 1.2 Create a fully populated template
    await page.getByRole("button", { name: "+ New template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();

    const stamp = Date.now();
    const tmplName = `qual-audit-${stamp}`;
    const tmplTitle = `Production Audit ${stamp}: Verify Security & Integration`;
    const tmplDesc = `Comprehensive verification of microservice dependencies, code standards, and integration benchmarks.`;

    await page.locator('input[name="name"]').fill(tmplName);
    await page.locator('input[name="title"]').fill(tmplTitle);
    await page.locator('textarea[name="description"]').fill(tmplDesc);

    // Target Repo & Base Branch
    const repoSelect = page.locator('select[name="repo"]').first();
    await expect(repoSelect).toBeVisible();
    await repoSelect.selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");

    // Auto-merge checkbox
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

    // Verify row appears in templates list
    const templateRow = page.locator(".process-row-template", {
      hasText: tmplName,
    });
    await expect(templateRow).toBeVisible();
    await expect(templateRow.getByText(tmplTitle)).toBeVisible();

    // 1.3 Edit the template & verify persistence across reload and direct deep-link
    await templateRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(tmplName);

    const updatedTitle = `${tmplTitle} (Updated)`;
    await page.locator('input[name="title"]').fill(updatedTitle);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toHaveCount(0);
    await expect(
      page.locator(".process-row-template", { hasText: tmplName }),
    ).toContainText(updatedTitle);

    // Deep link directly to the template
    const tmplListRes = await page.request.get("/api/templates");
    const tmplList = await tmplListRes.json();
    const createdTmpl = tmplList.find((t) => t.name === tmplName);
    expect(createdTmpl).toBeTruthy();

    await page.goto(`/templates/${createdTmpl.id}`);
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();
    await expect(page.locator('input[name="title"]')).toHaveValue(updatedTitle);
    await page.getByRole("button", { name: "Cancel" }).click();

    // 1.4 Use Template: Open "Use…" action and file a new task from the template
    const useBtn = templateRow.getByRole("button", { name: "Use…" });
    await useBtn.click();
    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).toBeVisible();

    // File task from template
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page).toHaveURL(/\/tasks\//);
    await expect(
      page.getByRole("heading", { name: updatedTitle }),
    ).toBeVisible();

    // 1.5 Dependency-aware delete protection
    // Create a schedule referencing this template (without content fields)
    const sched = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      base: "main",
      templateId: createdTmpl.id,
      recurrence: { kind: "daily", timeOfDay: "02:00" },
    });

    // Try to delete the template while referenced
    await page.goto(`/templates/${createdTmpl.id}`);
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();

    page.on("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Delete" }).click();

    // Verify error banner is shown with dependency refusal explanation
    await expect(
      page.getByText(
        /template is used by 1 schedule\(s\) \(".+"\); repoint or delete those first/i,
      ),
    ).toBeVisible();

    // Delete referencing schedule, then delete template cleanly
    const delSchedRes = await page.request.delete(`/api/schedules/${sched.id}`);
    expect(delSchedRes.ok()).toBe(true);

    await page.goto(`/templates/${createdTmpl.id}`);
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toHaveCount(0);
    await expect(
      page.locator(".process-row-template", { hasText: tmplName }),
    ).toHaveCount(0);
  });

  test("2. Suites: creation with modes, inline template creation, live composition preview, and editing", async ({
    page,
  }) => {
    await page.goto("/suites");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // 2.1 Create new suite with until_clean mode and inline template creation
    await page.getByRole("button", { name: "+ New suite" }).click();
    await expect(
      page.getByRole("heading", { name: "New suite" }),
    ).toBeVisible();

    const stamp = Date.now();
    const suiteName = `E2E Full Suite ${stamp}`;
    await page.locator('input[name="name"]').first().fill(suiteName);

    // Default mode is until_clean; set maxPasses
    await page.locator('input[name="maxPasses"]').fill("3");

    // Approval requirement & auto-merge
    await page.getByRole("checkbox", { name: /Require approval/i }).check();
    await page.getByRole("checkbox", { name: /Auto-merge/i }).check();

    // Pick seeded template: release-qualification
    await page.getByLabel("Templates").click();
    await page.getByRole("option", { name: /release-qualification/i }).click();
    await page.keyboard.press("Escape");

    // Inline template creation: click "+ New template" inside the suite editor
    await page.getByRole("button", { name: "+ New template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toBeVisible();

    const inlineTmplName = `inline-gate-${stamp}`;
    // Fill the top-most (last) form inputs
    const nameInputs = page.locator('input[name="name"]');
    await nameInputs.last().fill(inlineTmplName);
    await page
      .locator('input[name="title"]')
      .last()
      .fill(`Inline Gate ${stamp}`);

    const inlineRepoSelect = page.locator('select[name="repo"]');
    await expect(inlineRepoSelect.last()).toBeVisible();
    await inlineRepoSelect.last().selectOption("acme/widgets");
    await page.getByRole("button", { name: "Add template" }).click();

    // Back in suite overlay, verify inline template was created and added to suite
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "New suite" }),
    ).toBeVisible();

    // Verify Composition Preview heading appears
    await expect(
      page.getByRole("heading", { name: "What each pass files" }),
    ).toBeVisible();

    // Save Suite
    await page.getByRole("button", { name: "Add suite" }).click();
    await expect(page.getByRole("heading", { name: "New suite" })).toHaveCount(
      0,
    );

    const suiteRow = page.locator(".process-row-suite", { hasText: suiteName });
    await expect(suiteRow).toBeVisible();
    await expect(suiteRow).toContainText("2 templates");

    // 2.2 Edit Suite: change to count mode with 2 passes and save
    await suiteRow.click();
    await expect(
      page.getByRole("heading", { name: "Edit suite" }),
    ).toBeVisible();
    await page.getByLabel("Run mode").click();
    await page
      .getByRole("option", { name: /Run a fixed number of times/i })
      .click();
    await page.locator('input[name="count"]').fill("2");

    const updatedSuiteName = `${suiteName} (Count Mode)`;
    await page.locator('input[name="name"]').fill(updatedSuiteName);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("heading", { name: "Edit suite" })).toHaveCount(
      0,
    );

    // Verify updated suite in list
    const updatedSuiteRow = page.locator(".process-row-suite", {
      hasText: updatedSuiteName,
    });
    await expect(updatedSuiteRow).toBeVisible();
    await expect(updatedSuiteRow).toContainText("run 2×");

    // 2.3 Delete throwaway suite
    page.on("dialog", (dialog) => dialog.accept());
    await updatedSuiteRow.click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.locator(".process-row-suite", { hasText: updatedSuiteName }),
    ).toHaveCount(0);
  });

  test("3. Schedules: task-filing, suite-running, recurrence variants, overlap policies, pause/resume, run-now, and live preview", async ({
    page,
  }) => {
    await page.goto("/schedules");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();

    // 3.1 Create task-filing schedule with Daily cadence and 'allow' overlap
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toBeVisible();

    // Select template-backed
    await page.getByLabel("Template").click();
    await page.getByRole("option", { name: /release-qualification/i }).click();

    // Recurrence: Daily at 04:30
    await page.getByLabel("Repeat").click();
    await page.getByRole("option", { name: /Daily, at a time/i }).click();
    await page.locator('input[name="timeOfDay"]').fill("04:30");

    // Overlap policy: "Start it anyway" (allow)
    await page.getByLabel("If the last run has not finished").click();
    await page.getByRole("option", { name: /Start it anyway/i }).click();

    // Verify Composition Preview heading appears
    await expect(
      page.getByRole("heading", { name: "What this schedule files" }),
    ).toBeVisible();

    // Save schedule
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);

    // Verify row appears with "Can overlap" and "Template:" chips
    // The demo already has a schedule firing this template, so the new
    // one is told apart by its cadence.
    const schedRow = page
      .locator(".process-row-schedule", {
        hasText: "release-qualification",
      })
      .filter({ hasText: "daily at 04:30" });
    await expect(schedRow.first()).toBeVisible();
    await expect(schedRow.first()).toContainText("Can overlap");
    await expect(schedRow.first().locator(".process-next")).not.toHaveText("—");

    // 3.2 Create suite-running schedule with Weekly recurrence
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByLabel("Fires").click();
    await page.getByRole("option", { name: /A suite/i }).click();

    await page.getByRole("combobox", { name: "Suite" }).click();
    await page
      .getByRole("option", { name: /Release Qualification Suite/i })
      .click();

    const suiteSchedRepo = page.locator('select[name="repo"]');
    await expect(suiteSchedRepo.first()).toBeVisible();
    await suiteSchedRepo.first().selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");

    // Recurrence: Weekly on Monday at 07:00
    await page.getByLabel("Repeat").click();
    await page
      .getByRole("option", { name: /Weekly, on a day and time/i })
      .click();
    await page.getByLabel("Day of week").click();
    await page.getByRole("option", { name: /Monday/i }).click();
    await page.locator('input[name="timeOfDay"]').fill("07:00");

    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("heading", { name: "New schedule" }),
    ).toHaveCount(0);

    const suiteSchedRow = page.locator(".process-row-schedule", {
      hasText: "Release Qualification Suite",
    });
    await expect(suiteSchedRow.first()).toBeVisible();
    await expect(suiteSchedRow.first()).toContainText(
      "Suite: Release Qualification Suite",
    );
    await expect(suiteSchedRow.first()).toContainText("Mondays at 07:00");

    // 3.3 Pause and Resume a schedule
    const targetSched = schedRow.first();
    await targetSched.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();

    // Click Pause button
    await page.getByRole("button", { name: "Pause" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);

    // Verify "Paused" chip appears on row
    await expect(targetSched.getByText("Paused")).toBeVisible();

    // Resume schedule
    await targetSched.click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Resume" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
    await expect(targetSched.getByText("Paused")).toHaveCount(0);

    // 3.4 Run Now action
    page.on("dialog", (dialog) => dialog.accept());
    await targetSched.click();
    await page.getByRole("button", { name: "Run now" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);

    // Verify row reflects last ran timestamp: the Last run column is
    // filled in where it said "—".
    await expect(targetSched.locator(".process-last")).not.toHaveText("—");

    // 3.5 Delete throwaway schedule
    await targetSched.click();
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toHaveCount(0);
  });

  test("4. Suite Runs: launching, pass grouping, follow-up links, and direct URLs (/suites/runs/:id)", async ({
    page,
  }) => {
    // 4.1 Launch a suite run from Suite Row "Run…" button
    await page.goto("/suites");
    const suiteRow = page.locator(".process-row-suite", {
      hasText: "Release Qualification Suite",
    });
    await expect(suiteRow).toBeVisible();

    await suiteRow.getByRole("button", { name: "Run…" }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toBeVisible();

    const runRepo = page.locator('select[name="repo"]');
    await expect(runRepo.first()).toBeVisible();
    await runRepo.first().selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");

    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);

    // 4.2 Launch a suite run from generic "+ Run" action in Runs section header
    await page.getByRole("button", { name: "+ Run", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toBeVisible();
    await page.getByLabel("Suite", { exact: true }).click();
    await page
      .getByRole("option", { name: /Release Qualification Suite/i })
      .click();

    const genericRunRepo = page.locator('select[name="repo"]');
    await expect(genericRunRepo.first()).toBeVisible();
    await genericRunRepo.first().selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);

    // Verify suite run appears in runs list
    const runRow = page
      .locator(".suite-run-row", {
        hasText: "Release Qualification Suite",
      })
      .first();
    await expect(runRow).toBeVisible();
    await expect(runRow).toContainText("acme/widgets");

    // Click run row to open SuiteRunPane
    await runRow.click();
    await expect(page).toHaveURL(/\/processes\/suites\/runs\//);
    await expect(
      page.getByRole("heading", { name: "Release Qualification Suite" }),
    ).toBeVisible();
    await expect(page.getByRole("heading", { name: /Pass 1/i })).toBeVisible();
    await expect(page.locator(".task-row").first()).toBeVisible();

    // 4.3 Seed a multi-pass suite run with follow-up tasks to verify hierarchical link rendering
    const suiteListRes = await page.request.get("/api/suites");
    const suites = await suiteListRes.json();
    const suite = suites[0];

    // Directly create a SuiteRun record through the API
    const runRes = await page.request.post("/api/suite-runs", {
      data: {
        suiteId: suite.id,
        repo: "acme/widgets",
        base: "main",
      },
    });
    expect(runRes.ok()).toBe(true);
    const createdRun = await runRes.json();

    // Deep link directly to the suite run
    await page.goto(`/suites/runs/${createdRun.id}`);
    await expect(page.getByRole("heading", { name: suite.name })).toBeVisible();
    await expect(page.getByRole("heading", { name: /Pass 1/i })).toBeVisible();

    // Every row says its task's state to assistive technology, not just
    // in the colour of its dot: this pane is a flat list of mixed states
    // with no section headings to imply one (grain/task-1583).
    const runRows = page.locator(".task-row");
    await expect(runRows.first()).toBeVisible();
    for (let i = 0; i < (await runRows.count()); i++) {
      const badge = runRows.nth(i).locator(".badge-icon").first();
      await expect(badge).toHaveRole("img");
      await expect(badge).toHaveAccessibleName(/\S/);
    }

    // Clicking a task row navigates to the task details
    const firstTask = page.locator(".task-row").first();
    await firstTask.click();
    await expect(page).toHaveURL(/\/tasks\//);
    await expect(page.getByRole("heading", { level: 2 })).toBeVisible();

    // ...and the task's page links back to the run that filed it, naming
    // the pass and the suite rather than only a "suite" chip (found in
    // the 2026-10-13 walkthrough, step 4).
    await page.locator(".freshness .provenance-toggle").click();
    const backToRun = page.getByRole("link", {
      name: `${suite.name} run ${createdRun.id}`,
      exact: true,
    });
    await expect(backToRun).toBeVisible();
    await expect(backToRun).toHaveAttribute(
      "href",
      `/processes/suites/runs/${createdRun.id}`,
    );
    await expect(page.locator(".freshness")).toContainText(
      `pass 1 of ${suite.name} run ${createdRun.id}`,
    );
    await backToRun.click();
    await expect(page).toHaveURL(
      new RegExp(`/processes/suites/runs/${createdRun.id}$`),
    );
    await expect(page.getByRole("heading", { name: suite.name })).toBeVisible();
    await expect(page.getByRole("heading", { name: /Pass 1/i })).toBeVisible();
  });

  test("5. Repository-declared work: marked as declared, names repo and file, and refuses edit/delete up front", async ({
    page,
  }) => {
    // Any PATCH or DELETE the UI sends for a declared row is the bug this
    // test guards against (grain/task-1358): the server answers 400, and
    // before the fix the editor offered Save, Delete and Pause and only
    // found out by trying.
    const mutations = [];
    page.on("request", (req) => {
      if (
        ["PATCH", "DELETE"].includes(req.method()) &&
        /\/api\/(templates|suites|schedules)\/repo:/.test(req.url())
      )
        mutations.push(`${req.method()} ${req.url()}`);
    });
    page.on("dialog", (dialog) => dialog.accept());

    const cases = [
      {
        list: "/processes?kind=template",
        row: page.locator(".process-row-template", {
          hasText: "nightly-maintenance",
        }),
        heading: "Template",
        noun: "template",
        path: ".grain/templates/nightly.yml",
        field: 'input[name="title"]',
        url: "/processes/templates/repo:template:acme:widgets:nightly",
      },
      {
        list: "/processes?kind=suite",
        row: page.locator(".process-row-suite", {
          hasText: "Smoke Verification Suite",
        }),
        heading: "Suite",
        noun: "suite",
        path: ".grain/suites/smoke.yml",
        field: 'input[name="name"]',
        url: "/processes/suites/repo:suite:acme:widgets:smoke",
      },
      {
        list: "/processes?kind=schedule",
        row: page.locator(".process-row-schedule", {
          hasText: "Nightly Repo Maintenance Sweep",
        }),
        heading: "Schedule",
        noun: "schedule",
        path: ".grain/schedules/nightly.yml",
        field: null,
        url: "/processes/schedules/repo:schedule:acme:widgets:nightly",
      },
    ];

    for (const c of cases) {
      await page.goto(c.list);
      // The row says it is declared, and by whom; the file is its tooltip.
      const chip = c.row.locator(".declared-chip");
      await expect(chip).toBeVisible();
      await expect(chip).toHaveText("Declared by acme/widgets");
      await expect(chip).toHaveAttribute(
        "title",
        `Declared in acme/widgets, ${c.path}`,
      );
      // Rows somebody created here carry no such chip.
      await expect(page.locator(".declared-chip")).toHaveCount(1);

      await c.row.click();
      await expect(page).toHaveURL(new RegExp(`${c.url}$`));
      await expect(
        page.getByRole("heading", { name: c.heading, exact: true }),
      ).toBeVisible();
      const notice = page.locator(".declared-notice");
      await expect(notice).toContainText(
        `This ${c.noun} is declared by acme/widgets in ${c.path}, so it cannot be edited or deleted here`,
      );
      await expect(page.getByRole("button", { name: "Save" })).toBeDisabled();
      await expect(page.getByRole("button", { name: "Delete" })).toBeDisabled();
      if (c.field) {
        // The fields under the notice take no typing at all, so there is
        // no edit to submit with Enter either (2026-10-03 walkthrough).
        await expect(page.locator(c.field)).toBeDisabled();
      }

      // Reload on the deep link: the same refusal, read from the URL.
      await page.reload();
      await expect(page.locator(".declared-notice")).toContainText(c.path);
      await page.getByRole("button", { name: "Close" }).click();
      // The reload above was of the item's own address, which carries no
      // kind filter, so the list it closes onto is all of Processes.
      await expect(page).toHaveURL(/\/processes$/);
    }

    // A declared schedule cannot be paused (that is an edit to it), but
    // Run now is a firing rather than an edit, and still works.
    await page.goto("/schedules/repo:schedule:acme:widgets:nightly");
    await expect(page.getByRole("button", { name: "Pause" })).toBeDisabled();
    const run = page.waitForResponse(
      (res) =>
        res
          .url()
          .endsWith("/api/schedules/repo:schedule:acme:widgets:nightly/run") &&
        res.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Run now" }).click();
    expect((await run).ok()).toBe(true);
    await page.reload();
    await expect(
      page
        .locator(".process-row-schedule", {
          hasText: "Nightly Repo Maintenance Sweep",
        })
        .locator(".process-last"),
    ).not.toHaveText("—");

    expect(mutations).toEqual([]);

    // The server's own refusal is unchanged -- the UI now says it first.
    const refused = await page.request.delete(
      "/api/templates/repo:template:acme:widgets:nightly",
    );
    expect(refused.status()).toBe(400);
    expect(await refused.text()).toContain(
      "is declared by acme/widgets in .grain/templates/nightly.yml",
    );
  });

  test("6. Error resilience: a refused template and non-existent ID fallback", async ({
    page,
  }) => {
    // 6.1 The server refuses a template bound to a branch name that git
    // could never hold (validateBase in pkg/ui/client.go). This used to
    // be a 503 injected into POST /api/templates. The real refusal shows
    // the same thing: the form reports what the server said and the page
    // keeps working.
    await page.goto("/templates");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill("fail-template");
    await page.locator('input[name="title"]').fill("Should fail gracefully");
    const repoSelect = page.locator('select[name="repo"]');
    await expect(repoSelect).toBeVisible();
    await repoSelect.selectOption("acme/widgets");
    await page.locator('input[name="base"]').fill("release..broken");
    const created = page.waitForResponse(
      (res) =>
        res.url().endsWith("/api/templates") &&
        res.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Add template" }).click();
    expect((await created).status()).toBe(400);

    // Verify error banner is shown without crashing UI
    await expect(
      page.getByText(/"release\.\.broken" is not a usable branch name/),
    ).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(
      page.locator(".process-row-template", { hasText: "fail-template" }),
    ).toHaveCount(0);

    // 6.2 Non-existent direct links cleanly redirect/fall back
    await page.goto("/schedules/sched-non-existent-9999");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);

    await page.goto("/templates/tmpl-non-existent-9999");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);

    await page.goto("/suites/suite-non-existent-9999");
    await expect(
      page.getByRole("heading", { name: "Processes", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
  });

  test("7. Keyboard: template, suite, schedule and suite-run rows open with Enter", async ({
    page,
  }) => {
    // grain/task-1358: these rows opened only on a click -- Tab went from
    // the sort control through the drag handles and never reached one.
    // keyboard-open-rows.spec.js walks to them with Tab alone.
    const cases = [
      [
        "/templates",
        ".process-row-template",
        "release-qualification",
        /\/processes\/templates\/template-1$/,
      ],
      [
        "/suites",
        ".process-row-suite",
        "Release Qualification Suite",
        /\/processes\/suites\/suite-1$/,
      ],
      [
        "/schedules",
        ".process-row-schedule",
        "Weekly Dependency",
        /\/processes\/schedules\/sched-2$/,
      ],
    ];
    for (const [list, sel, text, url] of cases) {
      await page.goto(list);
      // The row's way in from the keyboard is the link its name is
      // (ListPrimitives.jsx's RowLink, grain/task-1429), not the row.
      const link = page.locator(sel, { hasText: text }).locator(".row-link");
      await link.focus();
      await expect(link).toBeFocused();
      await page.keyboard.press("Enter");
      await expect(page).toHaveURL(url);
      await expect(page.locator("form.pane-form")).toBeVisible();
    }

    // A run row too, from a run started through the UI.
    await page.goto("/suites");
    await page
      .locator(".process-row-suite", { hasText: "Release Qualification Suite" })
      .getByRole("button", { name: "Run…" })
      .click();
    // Named rather than left to the form's default: other specs on this
    // worker's store can add repos ahead of it.
    await page
      .locator('select[name="repo"]')
      .first()
      .selectOption("acme/widgets");
    await page.locator('input[name="base"]').first().fill("main");
    await page.getByRole("button", { name: "Run", exact: true }).click();
    // The run form is modal: until it has closed, its focus trap takes
    // focus back from anything behind it.
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);
    const runRow = page.locator(".suite-run-row").first();
    await expect(runRow).toBeVisible();
    // The runs list refreshes after a start, which can replace the row
    // between focusing it and pressing a key -- so the two are retried
    // together rather than asserted once.
    await expect(async () => {
      await runRow.locator(".row-link").press("Enter");
      await expect(page).toHaveURL(/\/processes\/suites\/runs\/\d+$/, {
        timeout: 1000,
      });
    }).toPass({ timeout: 10_000 });
    await expect(
      page.getByText("Pass 1", { exact: true }).first(),
    ).toBeVisible();

    // grain/task-1424: and the run's own task rows, which Tab never
    // reached either -- focus stayed on "← Suites".
    await expect(page.locator(".task-row").first()).toBeVisible();
    await tabToTaskRow(page);
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await expect(page.getByRole("heading", { level: 2 })).toBeVisible();
  });

  test("8. A declared row's own link opens it in a new tab, and a zoned schedule says its zone", async ({
    page,
    context,
  }) => {
    // grain/task-1533: each row's link is written percent-encoded, and a
    // declared id carries colons -- so the href a new tab, a copied link
    // or a bookmark opens was /templates/repo%3Atemplate%3A..., which
    // fell back to the list with `no template "repo%3A..."`.
    const cases = [
      {
        list: "/templates",
        name: "nightly-maintenance",
        heading: "Template",
        url: "/processes/templates/repo:template:acme:widgets:nightly",
      },
      {
        list: "/suites",
        name: "Smoke Verification Suite",
        heading: "Suite",
        url: "/processes/suites/repo:suite:acme:widgets:smoke",
      },
      {
        list: "/schedules",
        name: "Nightly Repo Maintenance Sweep",
        heading: "Schedule",
        url: "/processes/schedules/repo:schedule:acme:widgets:nightly",
      },
    ];
    for (const c of cases) {
      await page.goto(c.list);
      const href = await page
        .getByRole("link", { name: c.name, exact: true })
        .first()
        .getAttribute("href");
      expect(href).toContain("%3A");
      const tab = await context.newPage();
      await tab.goto(href);
      await expect(
        tab.getByRole("heading", { name: c.heading, exact: true }),
      ).toBeVisible();
      await expect(tab).toHaveURL(new RegExp(`${c.url}$`));
      await tab.close();
    }

    // The recurrence chip names the zone the schedule's clock reads in,
    // since "Next run" beside it is drawn in the viewer's own zone.
    const stamp = Date.now();
    await page.goto("/schedules");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    const form = page.getByRole("dialog");
    await form.getByRole("textbox", { name: "Title" }).fill(`Zoned ${stamp}`);
    await form.getByRole("combobox", { name: "Repeat" }).click();
    await page.getByRole("option", { name: "Daily, at a time" }).click();
    await form.getByRole("combobox", { name: /^Time zone/ }).click();
    await page
      .getByRole("option", { name: /^America\/New_York/ })
      .first()
      .click();
    await form.getByRole("button", { name: "Add schedule" }).click();
    const row = page.locator(".process-row-schedule", {
      hasText: `Zoned ${stamp}`,
    });
    await expect(
      row.getByText("daily at 09:00 America/New_York"),
    ).toBeVisible();
    await page.reload();
    await expect(
      row.getByText("daily at 09:00 America/New_York"),
    ).toBeVisible();

    // Throwaway: remove it again.
    page.on("dialog", (dialog) => dialog.accept());
    await row.getByRole("link").click();
    await form.getByRole("button", { name: "Delete" }).click();
    await expect(row).toHaveCount(0);
  });

  test("9. Declared editors are read-only, runs say they wait for approval, and confirms name what they do", async ({
    page,
  }) => {
    // grain/task-1628. A declared suite's editor disabled Save but left
    // "+ New template" live, which filed a real template the suite could
    // never be saved with; every field also took typing.
    await page.goto("/suites/repo:suite:acme:widgets:smoke");
    const pane = page.getByRole("dialog");
    await expect(pane.locator(".declared-notice")).toBeVisible();
    await expect(
      pane.getByRole("button", { name: "+ New template" }),
    ).toBeDisabled();
    await expect(pane.locator('input[name="maxPasses"]')).toBeDisabled();
    await expect(pane.locator('input[name="name"]')).toHaveValue(
      "Smoke Verification Suite",
    );
    // Nor do its selects take a choice from the keyboard (2026-10-11
    // walkthrough): only the pointer was kept off them, so a select
    // reached by focus opened on ArrowDown and changed what it showed.
    for (const [path, name, value] of [
      ["/suites/repo:suite:acme:widgets:smoke", "Run mode", "Run until no"],
      ["/schedules/repo:schedule:acme:widgets:nightly", "Repeat", "Every N"],
      [
        "/templates/repo:template:acme:widgets:nightly",
        "Network access",
        "Deployment default",
      ],
    ]) {
      await page.goto(path);
      const select = page.getByRole("dialog").getByRole("combobox", { name });
      await expect(select).toHaveAttribute("aria-disabled", "true");
      await select.focus();
      await page.keyboard.press("ArrowDown");
      await page.keyboard.press("Enter");
      await expect(page.getByRole("listbox")).toHaveCount(0);
      await expect(select).toContainText(value);
    }
    await page.goto("/suites/repo:suite:acme:widgets:smoke");
    await pane.getByRole("button", { name: "Close" }).click();

    // A schedule's search finds the template named on its chip.
    await page.goto("/schedules");
    await page
      .getByPlaceholder("Search processes…")
      .fill("nightly-maintenance");
    await expect(page.locator(".process-row-schedule")).toHaveCount(1);
    await expect(page.locator(".process-row-schedule")).toContainText(
      "Nightly Repo Maintenance Sweep",
    );

    // The default zone is not printed twice in the time label.
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByRole("combobox", { name: "Repeat" }).click();
    await page.getByRole("option", { name: "Daily, at a time" }).click();
    await expect(page.getByLabel(/^Time \(UTC\)/)).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();

    // Run now on a template-backed schedule names the template whose
    // task it files, not the schedule's own title.
    let confirmText = "";
    page.on("dialog", (dialog) => {
      confirmText = dialog.message();
      dialog.dismiss();
    });
    await page.goto("/schedules/repo:schedule:acme:widgets:nightly");
    await page.getByRole("button", { name: "Run now" }).click();
    await expect
      .poll(() => confirmText)
      .toContain('a task from the template "nightly-maintenance"');

    // A run whose suite requires approval says so, on its row and in its
    // own pane, rather than reading like a run whose tasks are queued.
    const stamp = Date.now();
    const created = await page.request.post("/api/suites", {
      data: {
        name: `Gated ${stamp}`,
        templateIds: ["template-1"],
        mode: "count",
        count: 1,
        requireApproval: true,
        autoMerge: false,
      },
    });
    expect(created.ok()).toBe(true);
    const suite = await created.json();
    await page.goto("/suites");
    await page
      .locator(".process-row-suite", { hasText: `Gated ${stamp}` })
      .getByRole("button", { name: /Run/ })
      .click();
    const runForm = page.getByRole("dialog");
    await runForm.getByRole("textbox", { name: "Branch" }).fill("main");
    await runForm.getByRole("button", { name: "Run", exact: true }).click();
    const runRow = page.locator(".suite-run-row", {
      hasText: `Gated ${stamp}`,
    });
    await expect(runRow).toContainText("each pass's tasks wait for approval");
    await runRow.getByRole("link").first().click();
    await expect(page).toHaveURL(/\/processes\/suites\/runs\/\d+$/);
    await page.reload();
    await expect(
      page
        .getByRole("dialog")
        .getByText("run 1× · each pass's tasks wait for approval"),
    ).toBeVisible();

    // Throwaway: the suite goes; its run stays listed, as runs do.
    const removed = await page.request.delete(`/api/suites/${suite.id}`);
    expect(removed.ok()).toBe(true);
  });

  test("10. A paused schedule does not promise its next run, and the run form says which tasks take its branch", async ({
    page,
  }) => {
    const stamp = Date.now();
    const sched = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      title: `Pausable ${stamp}`,
      recurrence: { kind: "daily", timeOfDay: "04:30" },
    });
    await page.goto(`/schedules/${sched.id}`);
    await page.getByRole("button", { name: "Pause" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await page.reload();
    const row = page.locator(".process-row-schedule", {
      hasText: `Pausable ${stamp}`,
    });
    await expect(row.getByText("Paused")).toBeVisible();
    await expect(row).toContainText("Held -- fires");
    await expect(row).not.toContainText("Next run");

    await row.click();
    await page.getByRole("button", { name: "Resume" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await page.reload();
    await expect(row.locator(".process-enabled")).toHaveText("Enabled");
    await expect(row).not.toContainText("Held -- fires");

    // A template bound to a repo of its own keeps it (docs/composition.md
    // "Placement"), so the branch box must not say every task takes it --
    // nor carry an internal issue reference.
    await page.goto("/suites");
    await page.getByRole("button", { name: "+ Run" }).click();
    const runForm = page.getByRole("dialog");
    await expect(
      runForm.getByText(/from a template not bound to a repo of its own/),
    ).toBeVisible();
    await expect(runForm).not.toContainText("agents#");
    await runForm.getByRole("button", { name: "Cancel" }).click();

    const removed = await page.request.delete(`/api/schedules/${sched.id}`);
    expect(removed.ok()).toBe(true);
  });

  test("11. A schedule on a repo-bound template saves a cadence edit, and attaching one saves too", async ({
    page,
  }) => {
    // The editor hides repo and branch once a bound template decides
    // them and sends both blank; the server refused that blank on every
    // save ("repo cannot be empty"), so none of these could be edited.
    const stamp = Date.now();
    const bound = await seedOne(page, "/api/templates", {
      name: `bound-${stamp}`,
      title: `Bound ${stamp}`,
      repo: "acme/widgets",
      base: "main",
    });
    const onTemplate = await seedOne(page, "/api/schedules", {
      templateId: bound.id,
      recurrence: { kind: "daily", timeOfDay: "04:30" },
    });
    const inline = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      title: `Inline ${stamp}`,
      recurrence: { kind: "daily", timeOfDay: "04:30" },
    });

    await page.goto(`/schedules/${onTemplate.id}`);
    await expect(
      page.getByText(/Fires against acme\/widgets on main/),
    ).toBeVisible();
    await page.locator('input[name="timeOfDay"]').fill("01:15");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await page.reload();
    await expect(
      page.locator(".process-row-schedule", { hasText: `bound-${stamp}` }),
    ).toContainText(/daily at 01:15/i);

    await page.goto(`/schedules/${inline.id}`);
    await page.getByRole("combobox", { name: "Template", exact: true }).click();
    await page.getByRole("option", { name: `bound-${stamp}` }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    const attached = (
      await (await page.request.get("/api/schedules")).json()
    ).find((s) => s.id === inline.id);
    expect(attached.templateId).toBe(bound.id);
    expect(attached.repo).toBe("acme/widgets");

    for (const id of [onTemplate.id, inline.id]) {
      const removed = await page.request.delete(`/api/schedules/${id}`);
      expect(removed.ok()).toBe(true);
    }
    const gone = await page.request.delete(`/api/templates/${bound.id}`);
    expect(gone.ok()).toBe(true);
  });

  test("12. A suite's count box shows what Save sends, a refused delete names the suite, and the preview words every row", async ({
    page,
  }) => {
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk12-${stamp}`,
      title: `Walk 12 ${stamp}`,
      networkAccess: "open",
    });

    // Switching from "until clean" to a fixed count reused one <input>
    // for both boxes: it went on showing max passes (5) while Save sent
    // count 1.
    await page.goto("/suites");
    await page.getByRole("button", { name: "+ New suite" }).click();
    await page.getByRole("textbox", { name: "Name" }).fill(`suite12-${stamp}`);
    await page.getByRole("combobox", { name: "Templates" }).click();
    await page
      .getByRole("option", { name: new RegExp(`^walk12-${stamp}`) })
      .click();
    await page.keyboard.press("Escape");
    await page.getByRole("spinbutton", { name: "Max passes" }).fill("7");
    await page.getByRole("combobox", { name: "Run mode" }).click();
    await page
      .getByRole("option", { name: "Run a fixed number of times" })
      .click();
    const countBox = page.getByRole("spinbutton", { name: "Number of times" });
    await expect(countBox).toHaveValue("1");
    await countBox.fill("2");

    // The preview names the model row and the layers in words.
    const preview = page.getByRole("dialog").last();
    await expect(preview.getByText("2 passes of these tasks")).toBeVisible();
    await expect(preview.locator("dt", { hasText: /^Model$/ })).toBeVisible();
    await expect(
      preview.getByText("the deployment's default model"),
    ).toBeVisible();
    await expect(
      preview.getByText("the branch this run targets"),
    ).toBeVisible();
    await expect(
      preview.getByText("egress (outbound internet only)"),
    ).toBeVisible();

    await page.getByRole("button", { name: "Add suite" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
    const suite = (await (await page.request.get("/api/suites")).json()).find(
      (s) => s.name === `suite12-${stamp}`,
    );
    expect(suite.mode).toBe("count");
    expect(suite.count).toBe(2);

    // Deleting the template the suite uses is refused, naming the suite.
    await page.goto(`/templates/${tmpl.id}`);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByText(`template is used by 1 suite(s) ("suite12-${stamp}")`, {
        exact: false,
      }),
    ).toBeVisible();

    expect((await page.request.delete(`/api/suites/${suite.id}`)).ok()).toBe(
      true,
    );
    expect((await page.request.delete(`/api/templates/${tmpl.id}`)).ok()).toBe(
      true,
    );
  });

  test("13. A scheduled run links to its schedule, and a template refusal names its schedule and suite at once", async ({
    page,
  }) => {
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk13-${stamp}`,
      title: `Walk 13 ${stamp}`,
    });
    const suite = await seedOne(page, "/api/suites", {
      name: `suite13-${stamp}`,
      templateIds: [tmpl.id],
      mode: "count",
      count: 1,
    });
    const taskSched = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      templateId: tmpl.id,
      recurrence: { kind: "daily", timeOfDay: "02:00" },
    });
    const suiteSched = await seedOne(page, "/api/schedules", {
      suiteId: suite.id,
      repo: "acme/widgets",
      base: "main",
      recurrence: { kind: "everyNHours", everyNHours: 6 },
    });

    // Run now on the suite schedule, through its own editor.
    await page.goto(`/schedules/${suiteSched.id}`);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Run now" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    const run = (await (await page.request.get("/api/suite-runs")).json()).find(
      (r) => r.scheduleId === suiteSched.id,
    );
    expect(run).toBeTruthy();

    // The run's row appears immediately in Suite runs on /processes without
    // a reload, says a schedule started it, and links back to that schedule.
    const marker = page
      .locator(`.suite-run-row a[href="/processes/schedules/${suiteSched.id}"]`)
      .first();
    await expect(marker).toHaveText("Scheduled");
    await marker.click();
    await expect(page).toHaveURL(
      new RegExp(`/processes/schedules/${suiteSched.id}$`),
    );
    await expect(page.getByRole("button", { name: "Run now" })).toBeVisible();

    // And the same from the run's own pane, with Back returning to it.
    await page.goto(`/suites/runs/${run.id}`);
    await page
      .getByRole("link", { name: /Scheduled/ })
      .first()
      .click();
    await expect(page).toHaveURL(
      new RegExp(`/processes/schedules/${suiteSched.id}$`),
    );
    await page.goBack();
    await expect(page).toHaveURL(
      new RegExp(`/processes/suites/runs/${run.id}$`),
    );

    // The template is held by a schedule and by a suite; one refusal
    // names both rather than the schedule alone.
    await page.goto(`/templates/${tmpl.id}`);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByText(
        `template is used by 1 schedule(s) ("Walk 13 ${stamp}") and 1 suite(s) ("suite13-${stamp}"); repoint or delete the schedules and remove it from the suites first`,
      ),
    ).toBeVisible();

    // Once the schedule is deleted, the run's mark still says where the
    // run came from; following it says the schedule is gone, as a deep
    // link to it does, rather than sitting on a pane that never opens.
    expect(
      (await page.request.delete(`/api/schedules/${suiteSched.id}`)).ok(),
    ).toBe(true);
    await page.goto(`/suites/runs/${run.id}`);
    await page
      .getByRole("link", { name: /Scheduled/ })
      .first()
      .click();
    await expect(
      page.getByText(`no schedule "${suiteSched.id}"`),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);

    for (const path of [
      `/api/schedules/${taskSched.id}`,
      `/api/suites/${suite.id}`,
      `/api/templates/${tmpl.id}`,
    ]) {
      expect((await page.request.delete(path)).ok(), path).toBe(true);
    }
  });

  test("14. Model preview names the deployment's default model", async ({
    page,
  }) => {
    // Since stage 1 of the single-tenant reset (docs/single-tenant-reset.md)
    // the default model a task with none of its own runs is the
    // deployment's, stored on the default team's row. (Another team whose
    // own default model chose nothing went with teams in 3e-iii-c2.)
    const stamp = Date.now();
    const deploymentModel = `deployment-default-${stamp}`;
    const before = await (await page.request.get("/api/deployment")).json();
    const setDeploymentModel = async (defaultModel) => {
      const res = await page.request.put("/api/deployment", {
        data: { defaultModel },
      });
      expect(res.ok(), await res.text()).toBe(true);
    };

    const tmplWithModel = await seedOne(page, "/api/templates", {
      name: `tmpl-with-${stamp}`,
      title: `Template with model ${stamp}`,
    });
    const preview = async () => {
      await page.goto("/templates");
      const row = page.locator(".process-row-template", {
        hasText: `tmpl-with-${stamp}`,
      });
      await row.getByRole("button", { name: "Use…" }).click();
      const dialog = page.getByRole("dialog").last();
      await expect(dialog.locator("dt", { hasText: /^Model$/ })).toBeVisible();
      return dialog;
    };
    const fileAndCheck = async () => {
      await page.getByRole("button", { name: "Create task" }).click();
      await expect(page).toHaveURL(/\/tasks\//);
      const match = page.url().match(/\/tasks\/([^/?#]+)/);
      expect(match).toBeTruthy();
      const res = await page.request.get(`/api/tasks/${match[1]}`);
      expect(res.ok()).toBe(true);
      // The task stores no model of its own: the default is read when it
      // is dispatched.
      expect((await res.json()).model || "").toBe("");
      return match[1];
    };
    const filed = [];
    try {
      // 1. With a deployment default model, the preview names it.
      await setDeploymentModel(deploymentModel);
      const withDefault = await preview();
      await expect(withDefault.getByText(deploymentModel)).toBeVisible();
      filed.push(await fileAndCheck());

      // 2. With none, the preview says the deployment decides.
      await setDeploymentModel("");
      const noDefault = await preview();
      await expect(
        noDefault.getByText("the deployment's default model"),
      ).toBeVisible();
      await expect(
        noDefault.getByText("the deployment decides").first(),
      ).toBeVisible();
      filed.push(await fileAndCheck());
    } finally {
      await setDeploymentModel(before.defaultModel || "");
      for (const path of [
        ...filed.map((id) => `/api/tasks/${id}`),
        `/api/templates/${tmplWithModel.id}`,
      ]) {
        await page.request.delete(path);
      }
    }
  });

  test("15. Editing a template renames its schedules straight away, not at their next firing", async ({
    page,
  }) => {
    // 2026-10-12 walkthrough: after a template edit, the schedules list,
    // the Run now confirm and a refused template delete all kept the
    // old title until the schedule next fired.
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk15-${stamp}`,
      title: `Walk 15 {{thing}} ${stamp}`,
      fields: [{ name: "thing", default: "cache" }],
    });
    const sched = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      templateId: tmpl.id,
      recurrence: { kind: "daily", timeOfDay: "03:00" },
    });
    const before = `Walk 15 cache ${stamp}`;
    const after = `Walk 15 cache ${stamp} (v2)`;
    await page.goto("/schedules");
    await expect(page.getByRole("link", { name: before })).toBeVisible();

    // Edit the template through its own editor.
    await page.goto(`/templates/${tmpl.id}`);
    await page
      .getByRole("textbox", { name: "Task title" })
      .fill(`Walk 15 {{thing}} ${stamp} (v2)`);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);

    // The schedule's row on /processes has the new title immediately (without
    // needing a reload), and its Run now confirm names it.
    await expect(page.getByRole("link", { name: after })).toBeVisible();
    await page.getByRole("link", { name: after }).click();
    let confirmText = "";
    page.once("dialog", (dialog) => {
      confirmText = dialog.message();
      dialog.dismiss();
    });
    await page.getByRole("button", { name: "Run now" }).click();
    await expect.poll(() => confirmText).toContain(`Run "${after}" now?`);

    // A refused delete of the template names the schedule the same way.
    await page.goto(`/templates/${tmpl.id}`);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Delete" }).click();
    await expect(
      page.getByText(`template is used by 1 schedule(s) ("${after}")`),
    ).toBeVisible();

    // And editing a suite on /processes renames its schedule row immediately.
    const suite = await seedOne(page, "/api/suites", {
      name: `walk15-suite-${stamp}`,
      templateIds: [tmpl.id],
      mode: "count",
      count: 1,
    });
    const suiteSched = await seedOne(page, "/api/schedules", {
      suiteId: suite.id,
      repo: "acme/widgets",
      base: "main",
      recurrence: { kind: "daily", timeOfDay: "04:00" },
    });
    await page.goto(`/suites/${suite.id}`);
    await page
      .getByRole("textbox", { name: "Name" })
      .fill(`walk15-suite-v2-${stamp}`);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
    await expect(
      page.locator(".process-row-schedule", {
        hasText: `Suite: walk15-suite-v2-${stamp}`,
      }),
    ).toBeVisible();

    for (const path of [
      `/api/schedules/${sched.id}`,
      `/api/schedules/${suiteSched.id}`,
      `/api/suites/${suite.id}`,
      `/api/templates/${tmpl.id}`,
    ]) {
      expect((await page.request.delete(path)).ok(), path).toBe(true);
    }
  });

  test("16. A branch typed with stray spaces names that branch on a schedule and on a suite run", async ({
    page,
  }) => {
    // 2026-10-13 walkthrough: the schedule preview showed " main " as
    // main, then Add refused it as "not a usable branch name"; the suite
    // Run dialog refused it too. Templates and tasks already trimmed.
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk16-${stamp}`,
      title: `Walk 16 ${stamp}`,
    });
    const suite = await seedOne(page, "/api/suites", {
      name: `walk16-suite-${stamp}`,
      templateIds: [tmpl.id],
      mode: "count",
      count: 1,
    });

    await page.goto("/schedules");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByRole("textbox", { name: "Title" }).fill(`Walk 16 ${stamp}`);
    await page.getByRole("textbox", { name: "Base branch" }).fill("  main ");
    await page.getByRole("button", { name: "Add schedule" }).click();
    await expect(
      page.getByRole("link", { name: `Walk 16 ${stamp}` }),
    ).toBeVisible();
    const scheds = await (await page.request.get("/api/schedules")).json();
    const sched = scheds.find((s) => s.title === `Walk 16 ${stamp}`);
    expect(sched.base).toBe("main");

    await page.goto("/suites");
    await page
      .locator("li", { hasText: suite.name })
      .first()
      .getByRole("button", { name: /Run/ })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("textbox", { name: "Branch" }).fill(" main  ");
    await dialog.getByRole("button", { name: "Run", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const runs = await (await page.request.get("/api/suite-runs")).json();
    const run = runs.find((r) => r.suiteId === suite.id);
    expect(run.base).toBe("main");

    for (const path of [
      `/api/schedules/${sched.id}`,
      `/api/suites/${suite.id}`,
      `/api/templates/${tmpl.id}`,
    ]) {
      expect((await page.request.delete(path)).ok(), path).toBe(true);
    }
  });

  test("17. A second tab's save from a stale copy is refused, not silently reverting the first tab's", async ({
    page,
    context,
  }) => {
    // 2026-10-13 walkthrough, step 2: each editor PATCHes its whole form,
    // so tab B saving the copy it opened put back everything tab A had
    // saved since. Now B's save is refused, B keeps its pane and its
    // input, and Reload brings in A's change.
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk17-${stamp}`,
      title: `Walk 17 ${stamp}`,
      description: "first",
    });
    const suite = await seedOne(page, "/api/suites", {
      name: `walk17-suite-${stamp}`,
      templateIds: [tmpl.id],
      mode: "until_clean",
      maxPasses: 2,
    });
    const sched = await seedOne(page, "/api/schedules", {
      repo: "acme/widgets",
      title: `Walk 17 ${stamp}`,
      overlap: "skip",
      recurrence: { kind: "weekly", weekday: "wednesday", timeOfDay: "12:00" },
    });
    const tabB = await context.newPage();
    const row = async (path, id) =>
      (await (await page.request.get(path)).json()).find((r) => r.id === id);
    const refusedThenReloaded = async (pane) => {
      const notice = tabB.getByRole("alert").filter({
        hasText: /changed by somebody else/,
      });
      await expect(notice).toBeVisible();
      await expect(
        tabB.getByRole("heading", { name: `Edit ${pane}` }),
      ).toBeVisible();
      return notice;
    };

    // Schedules: A moves the time, B (still at 12:00) sets overlap.
    await page.goto(`/schedules/${sched.id}`);
    await tabB.goto(`/schedules/${sched.id}`);
    await expect(tabB.locator('input[name="timeOfDay"]')).toHaveValue("12:00");
    await page.locator('input[name="timeOfDay"]').fill("13:15");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await tabB.getByLabel("If the last run has not finished").click();
    await tabB.getByRole("option", { name: /Start it anyway/i }).click();
    await tabB.getByRole("button", { name: "Save" }).click();
    const schedNotice = await refusedThenReloaded("schedule");
    let saved = await row("/api/schedules", sched.id);
    expect(saved.recurrence.timeOfDay).toBe("13:15");
    expect(saved.overlap).toBe("skip");
    await schedNotice.getByRole("button", { name: "Reload" }).click();
    await expect(schedNotice).toHaveCount(0);
    await expect(tabB.locator('input[name="timeOfDay"]')).toHaveValue("13:15");
    await tabB.getByLabel("If the last run has not finished").click();
    await tabB.getByRole("option", { name: /Start it anyway/i }).click();
    await tabB.getByRole("button", { name: "Save" }).click();
    await expect(tabB).toHaveURL(/\/processes(\?kind=schedule)?$/);
    saved = await row("/api/schedules", sched.id);
    expect(saved.recurrence.timeOfDay).toBe("13:15");
    expect(saved.overlap).toBe("allow");

    // Suites: A raises max passes, B ticks Require approval.
    await page.goto(`/suites/${suite.id}`);
    await tabB.goto(`/suites/${suite.id}`);
    await expect(
      tabB.getByRole("spinbutton", { name: "Max passes" }),
    ).toHaveValue("2");
    await page.getByRole("spinbutton", { name: "Max passes" }).fill("4");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
    await tabB.getByRole("checkbox", { name: /Require approval/i }).check();
    await tabB.getByRole("button", { name: "Save" }).click();
    await refusedThenReloaded("suite");
    const savedSuite = await row("/api/suites", suite.id);
    expect(savedSuite.maxPasses).toBe(4);
    expect(savedSuite.requireApproval).toBe(false);

    // Templates: A retitles, B rewrites the description.
    await page.goto(`/templates/${tmpl.id}`);
    await tabB.goto(`/templates/${tmpl.id}`);
    await expect(tabB.locator('input[name="title"]')).toHaveValue(
      `Walk 17 ${stamp}`,
    );
    await page.locator('input[name="title"]').fill(`Walk 17 ${stamp} (A)`);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);
    await tabB.locator('textarea[name="description"]').fill("second");
    await tabB.getByRole("button", { name: "Save" }).click();
    await refusedThenReloaded("template");
    // B's input is still there to copy out before reloading.
    await expect(tabB.locator('textarea[name="description"]')).toHaveValue(
      "second",
    );
    const savedTmpl = await row("/api/templates", tmpl.id);
    expect(savedTmpl.title).toBe(`Walk 17 ${stamp} (A)`);
    expect(savedTmpl.description).toBe("first");

    await tabB.close();
    for (const path of [
      `/api/schedules/${sched.id}`,
      `/api/suites/${suite.id}`,
      `/api/templates/${tmpl.id}`,
    ]) {
      expect((await page.request.delete(path)).ok(), path).toBe(true);
    }
  });

  test("18. A schedule's preview credits a template's capabilities to the template, and each list finds what its rows show", async ({
    page,
  }) => {
    // 2026-10-15 walkthrough: a template-backed schedule's preview said
    // the template's capabilities came "from the schedule", and the
    // three lists' searches ignored the cadence, branch and mode their
    // rows show.
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `walk18-${stamp}`,
      title: `Walk 18 ${stamp}`,
      repo: "acme/widgets",
      base: `walk18-${stamp}`,
      capabilities: ["gcp-key"],
    });
    const suite = await seedOne(page, "/api/suites", {
      name: `walk18-suite-${stamp}`,
      templateIds: [tmpl.id],
      mode: "count",
      count: 7,
    });
    const sched = await seedOne(page, "/api/schedules", {
      templateId: tmpl.id,
      recurrence: {
        kind: "weekly",
        weekday: "friday",
        timeOfDay: "09:15",
        timeZone: "Europe/London",
      },
    });

    await page.goto("/schedules");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByRole("combobox", { name: "Template" }).click();
    await page.getByRole("option", { name: tmpl.name }).click();
    const caps = page
      .locator("dt", { hasText: /^Capabilities$/ })
      .locator("..");
    await expect(caps).toContainText("gcp-key");
    await expect(caps).toContainText("from the template");
    await page.getByRole("button", { name: "Cancel" }).click();

    await page
      .getByPlaceholder("Search processes…")
      .fill("fridays at 09:15 europe/london");
    await expect(page.getByRole("link", { name: tmpl.title })).toBeVisible();
    await expect(
      page.getByText("Weekly Dependency & Vulnerability Audit"),
    ).toHaveCount(0);

    await page.goto("/templates");
    await page
      .getByPlaceholder("Search processes…")
      .fill(`acme/widgets @ walk18-${stamp}`);
    await expect(page.getByText(tmpl.name, { exact: true })).toBeVisible();
    await expect(page.getByText("security-audit", { exact: true })).toHaveCount(
      0,
    );

    await page.goto("/suites");
    await page.getByPlaceholder("Search processes…").fill("run 7×");
    await expect(page.locator(".process-row-suite")).toHaveCount(1);
    await expect(page.locator(".process-row-suite")).toContainText(suite.name);

    for (const path of [
      `/api/schedules/${sched.id}`,
      `/api/suites/${suite.id}`,
      `/api/templates/${tmpl.id}`,
    ]) {
      expect((await page.request.delete(path)).ok(), path).toBe(true);
    }
  });

  test("19. With its only connection disabled, the default model says so in a template's and a schedule's pickers", async ({
    page,
  }) => {
    // 2026-10-23 walkthrough: disabling the demo's one connection marked
    // every listed model "(disabled connections only)" but left
    // "Deployment default (gemini-2.5-pro)" -- the same model, and the
    // choice the form starts on -- reading as servable.
    const optionsOf = async (open) => {
      await page.goto("/processes");
      await page.getByRole("button", { name: "+ New", exact: true }).click();
      await page.getByRole("menuitem", { name: open }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("combobox", { name: "Model" }).click();
      const options = await page
        .getByRole("listbox")
        .getByRole("option")
        .allTextContents();
      await page.keyboard.press("Escape");
      await dialog.getByRole("button", { name: "Cancel" }).click();
      return options;
    };
    await page.goto("/settings/models");
    await page.getByRole("button", { name: "Disable" }).click();
    await expect(page.getByRole("button", { name: "Enable" })).toBeVisible();
    try {
      for (const kind of ["Template", "Schedule"]) {
        const options = await optionsOf(kind);
        expect(options[0]).toBe(
          "Deployment default (gemini-2.5-pro, disabled connections only)",
        );
        expect(options).toContain("gemini-2.5-pro (disabled connections only)");
      }
    } finally {
      await page.goto("/settings/models");
      await page.getByRole("button", { name: "Enable" }).click();
      await expect(page.getByRole("button", { name: "Disable" })).toBeVisible();
    }
    expect((await optionsOf("Template"))[0]).toBe(
      "Deployment default (gemini-2.5-pro)",
    );
  });

  test("20. Targeting a grain-CI repo credits the default grain-ci grant to the repo, not the schedule or template", async ({
    page,
  }) => {
    // 2026-10-24 walkthrough (task 293): on a repo whose CI Mode is grain,
    // sched-2's preview read "Capabilities: self-debug, grain-ci — from the
    // schedule", crediting the implicit grain-ci grant to a schedule that
    // does not contain it.
    expect(
      (
        await page.request.put("/api/repos/acme/widgets/ignore-github-ci", {
          data: { ignoreGitHubCI: true },
        })
      ).ok(),
    ).toBe(true);
    try {
      await page.goto("/processes/schedules/sched-2");
      const caps = page
        .locator("dt", { hasText: /^Capabilities$/ })
        .locator("..");
      await expect(caps).toContainText("self-debug");
      await expect(caps).toContainText("from the schedule");
      await expect(caps).toContainText("grain-ci");
      await expect(caps).toContainText("from the repo");

      const schedVal = await caps
        .getByText("self-debug", { exact: true })
        .boundingBox();
      const schedChip = await caps
        .getByText("from the schedule", { exact: true })
        .boundingBox();
      const repoVal = await caps
        .getByText("grain-ci", { exact: true })
        .boundingBox();
      const repoChip = await caps
        .getByText("from the repo", { exact: true })
        .boundingBox();
      expect(repoVal.y).toBeGreaterThan(schedVal.y);
      expect(Math.abs(schedVal.x - repoVal.x)).toBeLessThan(2);
      expect(
        Math.abs(schedChip.x + schedChip.width - (repoChip.x + repoChip.width)),
      ).toBeLessThan(2);
    } finally {
      expect(
        (
          await page.request.put("/api/repos/acme/widgets/ignore-github-ci", {
            data: { ignoreGitHubCI: false },
          })
        ).ok(),
      ).toBe(true);
    }
  });

  test("21. An interactive template's Use… preview credits Approval to the template, even when approvedByDefault is false", async ({
    page,
  }) => {
    const stamp = Date.now();
    const tmpl = await seedOne(page, "/api/templates", {
      name: `interactive-${stamp}`,
      title: `Interactive ${stamp}`,
      repo: "acme/widgets",
      interactive: true,
    });
    await page.route("**/api/config", async (route) => {
      const response = await route.fetch();
      const json = await response.json();
      json.approvedByDefault = false;
      await route.fulfill({ response, json });
    });
    try {
      await page.goto("/processes?kind=template");
      const row = page.locator(".process-row-template", {
        hasText: tmpl.name,
      });
      await row.getByRole("button", { name: "Use…" }).click();
      const dialog = page.getByRole("dialog").last();
      await expect(dialog.getByText(/files a live chat/)).toBeVisible();
      const approval = dialog
        .locator("dt", { hasText: /^Approval$/ })
        .locator("..");
      await expect(approval).toContainText("approved when filed");
      await expect(approval).toContainText("from the template");
      await dialog.getByRole("button", { name: "Cancel" }).click();
    } finally {
      await page.unroute("**/api/config");
      expect(
        (await page.request.delete(`/api/templates/${tmpl.id}`)).ok(),
      ).toBe(true);
    }
  });
});

// tabToTaskRow presses Tab until a task row's title link has focus --
// the row's own way in from the keyboard (grain/task-1582) -- the way
// somebody at a keyboard gets there, rather than focusing one directly.
async function tabToTaskRow(page) {
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press("Tab");
    const onRow = await page.evaluate(() =>
      Boolean(document.activeElement?.matches(".task-row .task-title")),
    );
    if (onRow) return;
  }
  throw new Error("Tab never reached a task row");
}
