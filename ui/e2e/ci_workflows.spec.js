import { test, expect } from "./fixtures.js";

// Comprehensive UI workflows for Grain CI:
// Triggering CI runs from UI form with workflow discovery & cache bypass,
// inspecting active run jobs and live step output logs,
// interacting with cancellation buttons, and clearing the job cache.

test.use({ demoArgs: ["-observability"] });

test.describe("CI Execution, Inspection, and Control Workflows", () => {
  test("triggers CI run via UI buttons, expands step logs, cancels run, and clears cache", async ({
    page,
  }) => {
    // 1. Navigate to /ci
    await page.goto("/ci");
    await expect(page.getByRole("heading", { name: "CI" })).toBeVisible();

    // 2. Click "Start a run" button
    await page.getByRole("button", { name: "Start a run" }).click();

    // 3. Fill repo and branch
    const repoSelect = page.locator('select[name="repo"]');
    await expect(repoSelect).toBeVisible();
    await repoSelect.selectOption("acme/widgets");
    await page.getByLabel("Branch").fill("main");

    // 4. Click "Load workflows" button
    await page.getByRole("button", { name: "Load workflows" }).click();
    await expect(page.getByLabel("Workflow")).toBeVisible();

    // 5. Check "Bypass cache" checkbox
    await page.getByRole("checkbox", { name: "Bypass cache" }).check();

    // 6. Click "Start run" button
    await page.getByRole("button", { name: "Start run", exact: true }).click();

    // 7. Verify the run detail pane opens with "Cancel run" button and job accordion
    await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible({
      timeout: 10_000,
    });

    // 8. Inspect step outputs: test expanding and collapsing step output
    const outputToggle = page
      .getByRole("button", { name: /Show output|Hide output/ })
      .first();
    await expect(outputToggle).toBeVisible();
    await outputToggle.click();

    // 9. Cancel run via Cancel run button and dialog prompt
    page.once("dialog", (d) => d.accept("E2E manual cancellation test"));
    await page.getByRole("button", { name: "Cancel run" }).click();

    // 10. Return back to CI page
    await page.getByRole("button", { name: "Back" }).click();
    await expect(page.getByRole("heading", { name: "CI" })).toBeVisible();

    // 11. Verify the run is listed in the runs table
    const runsTable = page.locator("table").last();
    await expect(runsTable).toBeVisible();
    await expect(runsTable.getByText("acme/widgets").first()).toBeVisible();

    // 12. Test "Clear cache" button on the CI page
    const clearCacheBtn = page.getByRole("button", { name: "Clear cache" });
    await expect(clearCacheBtn).toBeVisible();
    await clearCacheBtn.click();
    await expect(page.getByText(/Job cache: 0 entries/)).toBeVisible();
  });
});
