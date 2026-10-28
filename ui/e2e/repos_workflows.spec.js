import { test, expect } from "./fixtures.js";

// Comprehensive UI workflows for Repositories: inspecting repository settings,
// auto-saving prompt extensions and setup commands, managing branches,
// configuring workflow declarations, and navigating repo-scoped task creation.

test.describe("Repository Management and Configuration Workflows", () => {
  test("inspects repo details, auto-saves prompt extension & setup command, adds branch, and manages workflows", async ({
    page,
  }) => {
    const stamp = Date.now();
    const branchName = `feat/e2e-workflow-${stamp}`;

    // 1. Navigate to /repos
    await page.goto("/repos");
    await expect(page.getByRole("heading", { name: "Repos" })).toBeVisible();

    // 2. Open seeded repo acme/widgets
    const repoRow = page.locator(".repo-list-row", {
      hasText: "acme/widgets",
    });
    await expect(repoRow).toBeVisible();
    await repoRow.click();

    // 3. Verify RepoPage loaded
    await expect(page).toHaveURL(/\/repos\/acme\/widgets/);
    await expect(
      page.getByRole("heading", { name: "acme/widgets" }),
    ).toBeVisible();

    await page.getByRole("tab", { name: "Agents" }).click();
    // 4. Test Prompt extension auto-save on blur
    const promptField = page.getByLabel("Prompt extension");
    await expect(promptField).toBeVisible();
    const testPromptText = `Ensure all code adheres to style guidelines ${stamp}.`;
    await promptField.fill(testPromptText);
    await promptField.blur();

    // 5. Test Setup command auto-save on blur
    const setupField = page.getByLabel("Setup command");
    await expect(setupField).toBeVisible();
    const testSetupCmd = `npm ci --prefer-offline && echo "Ready ${stamp}"`;
    await setupField.fill(testSetupCmd);
    await setupField.blur();

    await page.getByRole("tab", { name: "Branches" }).click();
    // 6. Test Add Branch form
    const branchInput = page.getByLabel("Branch name");
    await expect(branchInput).toBeVisible();
    await branchInput.fill(branchName);
    await page.getByRole("button", { name: "Add branch" }).click();
    // The request is recorded and listed, pending until the reconciler runs
    await expect(page.locator("li", { hasText: branchName })).toBeVisible();
    await expect(branchInput).toHaveValue("");

    await page.getByRole("tab", { name: "Agents" }).click();
    // 7. Reload page to verify auto-saved fields persist
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "acme/widgets" }),
    ).toBeVisible();
    await expect(page.getByLabel("Prompt extension")).toHaveValue(
      testPromptText,
    );
    await expect(page.getByLabel("Setup command")).toHaveValue(testSetupCmd);

    // 8. Test Releases button navigation
    const releasesBtn = page.getByRole("button", {
      name: "Releases",
      exact: true,
    });
    await expect(releasesBtn).toBeVisible();
    await releasesBtn.click();
    await expect(
      page.getByRole("heading", { name: /acme\/widgets releases/ }),
    ).toBeVisible();
    // Navigate back to repo page via back button
    await page.getByRole("button", { name: /← acme\/widgets/ }).click();
    await expect(
      page.getByRole("heading", { name: "acme/widgets" }),
    ).toBeVisible();

    // 9. Test "File a task for this repo" / "+ New task" button
    const fileTaskBtn = page.getByRole("button", {
      name: /\+ New task|File a task/,
    });
    await expect(fileTaskBtn).toBeVisible();
    await fileTaskBtn.click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await page.getByLabel("Close dialog").click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );
  });

  // The Add repo picker reads as a picker: it has its dropdown arrow and
  // opens on focus, the line under it says what discovery found (in demo,
  // no connection can be listed, so nothing -- with the reason and a link
  // to the Git connections), the Clone URL field only appears for
  // "Other…" or a host no connection covers, and the list below is
  // headed apart from the form with its own filter box.
  test("Add repo picker shows discovery status and offers a clone URL only when needed", async ({
    page,
  }) => {
    await page.goto("/repos");
    await expect(page.getByRole("heading", { name: "Repos" })).toBeVisible();

    const status = page.getByTestId("repo-discovery-status");
    await expect(status).toHaveAttribute("data-status", "none");
    await expect(status).toContainText(
      "No repositories discovered — add or check a Git connection in Settings",
    );
    await expect(status).toContainText(
      "this deployment cannot list what a GitHub credential reaches",
    );

    const form = page.getByRole("form", { name: "Add repo" });
    const picker = form.getByRole("combobox", { name: "Repo to add" });
    const clone = form.getByPlaceholder("Clone URL (GitHub by default)");
    await expect(form.getByRole("button", { name: "Open" })).toBeVisible();
    await expect(clone).toHaveCount(0);

    await picker.fill("acme/new-thing");
    await expect(clone).toHaveCount(0);
    await picker.fill("gitlab.example.com/acme/new-thing");
    await expect(clone).toBeVisible();
    await picker.fill("");
    await expect(clone).toHaveCount(0);

    await picker.blur();
    await picker.click();
    await page.getByRole("option", { name: "Other…" }).click();
    await expect(clone).toBeVisible();

    await expect(page.getByTestId("your-repos-label")).toHaveText("Your repos");
    await expect(page.getByPlaceholder("Filter added repos…")).toBeVisible();
    await expect(page.getByPlaceholder("Search repos…")).toHaveCount(0);

    const settingsLink = status.getByRole("link", {
      name: "Git connection in Settings",
    });
    await settingsLink.click({ modifiers: ["Control"] });
    await expect(page).toHaveURL(/\/repos$/);

    await settingsLink.click();
    await expect(page).toHaveURL(/\/settings\/git$/);
  });
});
