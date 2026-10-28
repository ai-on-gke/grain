import { expect } from "@playwright/test";
import { test } from "./fixtures.js";

test.setTimeout(120_000);

const DESKTOP = { width: 1440, height: 900 };
const RUNNING_IMAGE = "ghcr.io/bwsalmon/grain/grain:main";

test.describe("UI Navigation, Sidebar Split-Button Intake & In-App Upgrade Dialog (§4)", () => {
  test("1. Sidebar Split-Button renders and triggers creation dropdown actions", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    await expect(aside).toBeVisible();

    // 1. Verify '+ New task' primary button and dropdown chevron trigger
    const newTaskMainBtn = aside.getByRole("button", { name: "+ New task" });
    const createOptionsBtn = aside.getByRole("button", {
      name: "Create options",
    });

    await expect(newTaskMainBtn).toBeVisible();
    await expect(createOptionsBtn).toBeVisible();
    await expect(createOptionsBtn).toHaveAttribute("aria-haspopup", "true");

    // 2. Click Chevron to open dropdown menu
    await createOptionsBtn.click();
    const menu = page.locator("#create-options-menu");
    await expect(menu).toBeVisible();

    // Check menu items
    const taskItem = page.getByRole("menuitem", { name: /New task/i });
    const sessionItem = page.getByRole("menuitem", { name: /New session/i });
    const boardItem = page.getByRole("menuitem", { name: /New board/i });

    await expect(taskItem).toBeVisible();
    await expect(sessionItem).toBeVisible();
    await expect(page.getByRole("menuitem", { name: /New team/i })).toHaveCount(
      0,
    );
    await expect(boardItem).toBeVisible();

    // 3. Click "New session" -> opens an empty chat straight away, with no
    // dialog in between: the first message is what creates the session
    // (NewSessionPage.jsx), so nothing is listed under Sessions yet.
    await sessionItem.click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page).toHaveURL(/\/session\/new$/);
    await expect(page.getByRole("heading", { name: "New chat" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible();
    await expect(page.getByLabel(/Session name/)).toBeVisible();
    await expect(page.getByLabel("Pin to sidebar")).not.toBeChecked();
    // On desktop the name and its Create session button share one row.
    const nameBox = await page.getByLabel(/Session name/).boundingBox();
    const createBox = await page
      .getByRole("button", { name: "Create session" })
      .boundingBox();
    expect(createBox.x).toBeGreaterThan(nameBox.x + nameBox.width);
    expect(Math.abs(createBox.y - nameBox.y)).toBeLessThan(8);

    // 4. Open dropdown again and click "New task"
    await createOptionsBtn.click();
    await page.getByRole("menuitem", { name: /New task/i }).click();
    const newTaskModal = page.getByRole("heading", { name: "New task" });
    await expect(newTaskModal).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(newTaskModal).not.toBeVisible();
  });

  test("2. Selecting templates from intake correctly pre-populates parameters", async ({
    page,
  }) => {
    const stamp = Date.now();
    const templateName = `Intake Template ${stamp}`;
    const taskTitle = `Intake task for service {{service}}`;
    const desc = `Intake description for {{service}} with automated setup.`;

    // 1. Create a template first via /templates
    await page.goto("/templates");
    await expect(
      page.getByRole("heading", { name: "Processes" }),
    ).toBeVisible();

    await page.getByRole("button", { name: "+ New template" }).click();
    await page.getByLabel("Name", { exact: true }).fill(templateName);
    await page.getByLabel("Task title", { exact: true }).fill(taskTitle);
    await page.getByLabel("Description", { exact: true }).fill(desc);

    await page.getByRole("button", { name: "+ Add input" }).click();
    await page.getByLabel("Input 1 name").fill("service");
    await page.getByLabel("Input 1 label").fill("Target Service");
    await page.getByRole("checkbox", { name: "Required" }).first().check();

    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).not.toBeVisible();

    // 2. Use template to file a task
    const templateRow = page.locator(".process-row-template", {
      hasText: templateName,
    });
    await expect(templateRow).toBeVisible();
    await templateRow.getByRole("button", { name: "Use…" }).click();

    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).toBeVisible();
    await expect(page.getByLabel("Target Service")).toBeVisible();
    await page.getByLabel("Target Service").fill("auth-service");

    await page.getByRole("button", { name: "Create task" }).click();
    await expect(
      page.getByRole("heading", { name: "File a task from a template" }),
    ).not.toBeVisible();

    // 3. Confirm task detail is opened with interpolated parameters
    await expect(page.locator(".detail-header h2")).toContainText(
      "Intake task for service auth-service",
    );
    await expect(page.locator(".description")).toContainText(
      "Intake description for auth-service with automated setup.",
    );
  });

  test("3. Settings -> Upgrade panel confirms active tasks before upgrade", async ({
    page,
  }) => {
    const posted = [];

    // Stubbed on purpose: a real POST /api/upgrade rebuilds or re-pulls
    // grain and restarts the deployment, and the demo wires no Upgrader.
    // The stub stands in for an upgrade status with in-flight active tasks.
    await page.route("**/api/upgrade", async (route) => {
      const method = route.request().method();
      if (method === "POST") {
        const payload = route.request().postDataJSON();
        posted.push(payload);
        await route.fulfill({
          status: 202,
          contentType: "application/json",
          body: JSON.stringify({
            enabled: true,
            mode: "image",
            running: { image: RUNNING_IMAGE },
            branch: payload.branch,
            phase: "running",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          enabled: true,
          mode: "image",
          running: { image: RUNNING_IMAGE },
          branch: "",
          phase: "",
          detail: "",
          inFlightRuns: 2,
        }),
      });
    });

    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Navigate to Settings -> Upgrade
    await page.getByTestId("top-bar-settings").click();
    await page.getByTestId("settings-nav-upgrade").click();

    // Verify the Settings -> Upgrade panel renders
    await expect(page.locator("p.upgrade-running")).toContainText(
      `Now running ${RUNNING_IMAGE}`,
    );
    await expect(page.locator(".upgrade-in-flight")).toContainText(
      "2 runs are in flight",
    );

    await page.getByLabel("Branch").fill("grain/task-1079");

    // Click "Upgrade" button
    const upgradeBtn = page
      .getByTestId("upgrade-section")
      .getByRole("button", { name: "Upgrade" });
    await expect(upgradeBtn).toBeVisible();
    await upgradeBtn.click();

    // Verify in-app confirmation modal dialog appears warning of active tasks
    const confirmDialogTitle = page.locator("#confirm-upgrade-dialog-title");
    await expect(confirmDialogTitle).toBeVisible();
    const dialog = confirmDialogTitle.locator(
      "xpath=ancestor::div[@role='dialog']",
    );
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(/2 runs are in flight/i)).toBeVisible();
    await expect(
      dialog.getByText(
        /Upgrading restarts this deployment, which discards them/i,
      ),
    ).toBeVisible();

    // Click Cancel in modal
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(confirmDialogTitle).toHaveCount(0);
    expect(posted).toHaveLength(0);

    // Click again and confirm upgrade
    await upgradeBtn.click();
    await expect(confirmDialogTitle).toBeVisible();
    const dialogActions = dialog.locator(".MuiDialogActions-root");
    await dialogActions.getByRole("button", { name: "Upgrade" }).click();
    await expect(confirmDialogTitle).toHaveCount(0);

    expect(posted).toEqual([
      { branch: "grain/task-1079", afterTasksFinish: false },
    ]);
  });

  test("4. Merged task dissolution animations are removed from TaskBoard and TaskList", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Ensure no .dissolving-task or MergedPatternRow elements exist
    await expect(page.locator(".dissolving-task")).toHaveCount(0);
    await expect(page.locator(".merged-pattern-row")).toHaveCount(0);

    // The same on the board: open the demo's seeded kanban board and wait
    // for its cards, so the absence below is asserted on a drawn board
    // rather than on whatever was on screen mid-navigation.
    await page.getByRole("button", { name: /^Boards/ }).click();
    await page.getByText("Board", { exact: true }).click();
    await expect(page).toHaveURL(/\/boards\/[^/]+$/);
    await expect(page.locator(".board-card").first()).toBeVisible();
    await expect(page.locator(".dissolving-task")).toHaveCount(0);
    await expect(page.locator(".merged-pattern-row")).toHaveCount(0);
  });

  test("5. Eigenmode icons for suites and metrics display properly in sidebar navigation", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");

    // Check Suites glyph
    const suitesBtn = aside.getByRole("button", { name: /^Processes/i });
    await expect(suitesBtn).toBeVisible();
    const suitesSvg = suitesBtn.locator("svg");
    await expect(suitesSvg).toBeVisible();

    // Metrics is a System tab now; the System glyph is on the page header rail's
    // button (docs/design/settings-panel.md §4.1).
    const metricsBtn = page.getByTestId("top-bar-system");
    await expect(metricsBtn).toBeVisible();
    const metricsSvg = metricsBtn.locator("svg");
    await expect(metricsSvg).toBeVisible();

    // Verify SVG paths match distinct eigenmode figures
    const suitesPath = await suitesSvg.locator("path").getAttribute("d");
    const metricsPath = await metricsSvg.locator("path").getAttribute("d");
    expect(suitesPath).toBeTruthy();
    expect(metricsPath).toBeTruthy();
    expect(suitesPath).not.toEqual(metricsPath);
  });
});
