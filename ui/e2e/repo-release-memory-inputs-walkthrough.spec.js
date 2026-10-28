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

test.describe("Repository Settings, Releases, and Memory Views Inputs Walkthrough", () => {
  const repo = "acme/widgets";

  test("1. Repository Settings & Rules: branch inputs, compare, CI mode, capabilities, prompt extension, setup command, and workflow files", async ({
    page,
  }) => {
    // Navigate to the repository settings page
    await page.goto(`/repos/${repo}`);
    await expect(page.getByRole("heading", { name: repo })).toBeVisible();

    await page.getByRole("tab", { name: "Branches" }).click();
    // 1.1 Branch addition input & validation
    const branchInput = page.getByLabel("Branch name");
    await expect(branchInput).toBeVisible();
    await expect(branchInput).toBeEmpty();

    // Try submitting with invalid branch name containing ".."
    await branchInput.fill("feature..invalid");
    await page.getByRole("button", { name: "Add branch" }).click();
    // Error is shown in global error banner / toast
    await expect(page.getByText(/invalid branch name/i)).toBeVisible();

    // Fill valid branch name and verify submission
    const testBranch = `feat-${Date.now().toString(36)}`;
    await branchInput.fill(testBranch);
    await page.getByRole("button", { name: "Add branch" }).click();
    await expect(page.getByText(testBranch)).toBeVisible();
    await expect(branchInput).toBeEmpty();

    // Test duplicate branch validation notice
    await branchInput.fill(testBranch);
    await page.getByRole("button", { name: "Add branch" }).click();
    await expect(
      page.getByText(
        new RegExp(`${testBranch} is already on this repo's list`, "i"),
      ),
    ).toBeVisible();
    await branchInput.clear();

    // 1.2 Compare branches form inputs
    const compareBase = page.getByLabel("Base branch", { exact: false });
    const compareHead = page.getByLabel("Head branch", { exact: false });
    await expect(compareBase).toBeVisible();
    await expect(compareHead).toBeVisible();

    // Test entering branches into compare inputs
    await compareBase.fill("main");
    await compareHead.fill(testBranch);
    const compareBtn = page.getByRole("button", { name: "Compare" });
    await expect(compareBtn).toBeEnabled();

    await page.getByRole("tab", { name: "CI" }).click();
    // 1.3 CI Mode selector (Grain CI vs GitHub Actions)
    const grainRadio = page.getByRole("radio", {
      name: new RegExp(`Make grain CI the gate for ${repo}`, "i"),
    });
    const githubRadio = page.getByRole("radio", {
      name: new RegExp(`Use GitHub CI for ${repo}`, "i"),
    });
    await expect(grainRadio).toBeVisible();
    await expect(githubRadio).toBeVisible();

    if (await grainRadio.isChecked()) {
      await githubRadio.click();
      await expect(githubRadio).toBeChecked();
      await grainRadio.click();
      await expect(grainRadio).toBeChecked();
    } else {
      await grainRadio.click();
      await expect(grainRadio).toBeChecked();
      await githubRadio.click();
      await expect(githubRadio).toBeChecked();
    }

    await page.getByRole("tab", { name: "Agents" }).click();
    // 1.4 Default capabilities picker and chip inputs
    const defaultCapsSelect = page.getByRole("combobox", {
      name: /default capabilities/i,
    });
    await expect(defaultCapsSelect).toBeVisible();
    // Toggle Self debug and toggle it back: every spec file a worker runs
    // shares one demo store, and repository_release_memory_walkthrough.spec.js
    // turns this same capability on and expects its chip afterwards.
    const selfDebugChip = defaultCapsSelect
      .locator(".MuiChip-root")
      .filter({ hasText: /Self debug/i });
    const option = page.getByRole("option", { name: /Self debug/i });
    const toggleSelfDebug = async () => {
      await defaultCapsSelect.click();
      await option.click();
      await page.keyboard.press("Escape");
    };
    await defaultCapsSelect.click();
    const selfDebugWasOn =
      (await option.getAttribute("aria-selected")) === "true";
    await page.keyboard.press("Escape");
    await toggleSelfDebug();
    await expect(selfDebugChip).toHaveCount(selfDebugWasOn ? 0 : 1);
    await toggleSelfDebug();
    await expect(selfDebugChip).toHaveCount(selfDebugWasOn ? 1 : 0);

    // 1.5 System prompt extension and setup command auto-save on blur
    const promptInput = page.getByLabel(
      new RegExp(`Prompt extension for ${repo}`, "i"),
    );
    await expect(promptInput).toBeVisible();
    const initialPrompt = await promptInput.inputValue();
    const testPromptText =
      "Standing instruction verification note: ensure zero-trust credentials.";
    await promptInput.fill(testPromptText);
    await promptInput.blur();

    const setupInput = page.getByLabel(
      new RegExp(`Setup command for ${repo}`, "i"),
    );
    await expect(setupInput).toBeVisible();
    const initialSetup = await setupInput.inputValue();
    const testSetupCmd = "npm test -- --bail";
    await setupInput.fill(testSetupCmd);
    await setupInput.blur();

    // Reload page to verify auto-save persistence
    await page.reload();
    await expect(
      page.getByLabel(new RegExp(`Prompt extension for ${repo}`, "i")),
    ).toHaveValue(testPromptText);
    await expect(
      page.getByLabel(new RegExp(`Setup command for ${repo}`, "i")),
    ).toHaveValue(testSetupCmd);

    await page.getByRole("tab", { name: "CI" }).click();
    // 1.6 Workflow file path and editor inputs (auto-save on blur)
    await page.getByRole("button", { name: "Add workflow" }).click();
    const workflowPathInput = page.locator('input[name="repoWorkflowPath0"]');
    const yamlEditor = page.locator('textarea[name="repoWorkflowText0"]');
    await expect(workflowPathInput).toBeVisible();
    await expect(yamlEditor).toBeVisible();

    // Enter valid workflow filename and invalid YAML to verify YAML error
    await workflowPathInput.fill("test-input-validator.yml");
    await yamlEditor.fill("invalid: [yaml: broken");
    await yamlEditor.blur();
    await expect(page.locator(".MuiAlert-filledError")).toBeVisible();

    // Correct with valid workflow YAML
    await yamlEditor.fill(
      "name: Test CI\non:\n  push:\n    branches: [main]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make test\n",
    );
    await yamlEditor.blur();
    await expect(page.locator(".MuiAlert-filledError")).not.toBeVisible({
      timeout: 10000,
    });

    // Reload page and confirm workflow file persisted
    await page.reload();
    await expect(page.locator('input[name="repoWorkflowPath0"]')).toHaveValue(
      ".grain/workflows/test-input-validator.yml",
    );

    // Clean up workflow
    const removeWorkflowBtn = page.getByRole("button", {
      name: "Remove workflow .grain/workflows/test-input-validator.yml",
    });
    await expect(removeWorkflowBtn).toBeVisible();
    page.once("dialog", (dialog) => dialog.accept());
    await removeWorkflowBtn.click();
    await expect(page.locator('input[name="repoWorkflowPath0"]')).toHaveCount(
      0,
    );

    // Put the prompt extension and setup command back for later spec files
    await page.getByRole("tab", { name: "Agents" }).click();
    const promptRestore = page.getByLabel(
      new RegExp(`Prompt extension for ${repo}`, "i"),
    );
    await promptRestore.fill(initialPrompt);
    await promptRestore.blur();
    const setupRestore = page.getByLabel(
      new RegExp(`Setup command for ${repo}`, "i"),
    );
    await setupRestore.fill(initialSetup);
    await setupRestore.blur();
    await page.reload();
    await expect(
      page.getByLabel(new RegExp(`Prompt extension for ${repo}`, "i")),
    ).toHaveValue(initialPrompt);
    await expect(
      page.getByLabel(new RegExp(`Setup command for ${repo}`, "i")),
    ).toHaveValue(initialSetup);
  });

  test("2. Releases & Tags: new release input, commit ref, release workflow settings, and deletion confirmation", async ({
    page,
    request,
  }) => {
    // 2.1 RepoReleaseWorkflowSettings inputs on RepoPage
    await page.goto(`/repos/${repo}#releases`);
    const baseBranchJobsInput = page.locator('input[name="baseBranchJobs"]');
    if (await baseBranchJobsInput.isVisible()) {
      const initialJobs = await baseBranchJobsInput.inputValue();
      const saveJobs = page.locator(
        'form:has(input[name="baseBranchJobs"]) button[type="submit"]',
      );
      await baseBranchJobsInput.fill("go, lint, unit-tests");
      await saveJobs.click();
      await page.reload();
      await expect(page.locator('input[name="baseBranchJobs"]')).toHaveValue(
        "go, lint, unit-tests",
      );
      await page.locator('input[name="baseBranchJobs"]').fill(initialJobs);
      await saveJobs.click();
      await page.reload();
      await expect(page.locator('input[name="baseBranchJobs"]')).toHaveValue(
        initialJobs,
      );
    }

    // 2.2 Navigate to Releases view
    await page.getByRole("button", { name: "Releases", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: `${repo} releases` }),
    ).toBeVisible();

    // Release name input validation
    const releaseNameInput = page.getByLabel("New release name");
    await expect(releaseNameInput).toBeVisible();

    // Test invalid release names: spaces and invalid characters
    await releaseNameInput.fill("invalid release name with spaces");
    await page.getByRole("button", { name: "Create release" }).click();
    await expect(page.getByText(/invalid release name/i)).toBeVisible();

    // Test invalid release name with '..'
    await releaseNameInput.fill("release..bad");
    await page.getByRole("button", { name: "Create release" }).click();
    await expect(page.getByText(/invalid release name/i)).toBeVisible();

    // Test valid release name creation
    const releaseTag = `rel-${Date.now().toString(36)}`;
    await releaseNameInput.fill(releaseTag);
    await page.getByRole("button", { name: "Create release" }).click();

    // Verify release picker combobox contains the release tag
    const releasePicker = page.getByRole("combobox", { name: "Release" });
    await expect(releasePicker).toBeVisible();
    await expect(releasePicker).toContainText(releaseTag);

    // 2.3 Qualification plan editor inputs
    const addPlanItemBtn = page.getByRole("button", { name: "Add item" });
    if (await addPlanItemBtn.isVisible()) {
      await addPlanItemBtn.click();
      const repeatInput = page.locator('input[type="number"][min="1"]').first();
      await expect(repeatInput).toBeVisible();
      await repeatInput.fill("3");
      await expect(repeatInput).toHaveValue("3");
    }

    // 2.4 Release deletion confirmation prompt
    const deleteResponse = page.waitForResponse(
      (r) =>
        r.url().includes(`/releases/${releaseTag}`) &&
        r.request().method() === "DELETE",
    );
    page.once("dialog", (dialog) => {
      expect(dialog.message()).toContain(`Remove release ${releaseTag}?`);
      dialog.accept();
    });
    await page.getByRole("button", { name: "Delete release" }).click();
    expect((await deleteResponse).status()).toBe(204);

    // Verify deleted release is no longer in the API
    const getRes = await request.get(
      `/api/repos/${repo}/releases/${releaseTag}`,
    );
    expect(getRes.status()).toBe(404);

    // And in the UI, if any release remains the picker won't have the deleted tag,
    // or if none remain, the empty alert appears.
    const emptyAlert = page.getByText(
      new RegExp(`${repo} has no releases yet`, "i"),
    );
    await expect(
      emptyAlert.or(releasePicker.filter({ hasNotText: releaseTag })),
    ).toBeVisible();
  });

  test("3. Repository Memory & Instructions: standing memory text, search filter, document editors, and dirty state tracking", async ({
    page,
  }) => {
    // Navigate to the team's Memory page, where MemoryPanel is hosted
    await page.goto("/settings/memory");
    await expect(
      page.getByText(
        "What grain has learned about working here, written by the memory agent rather",
      ),
    ).toBeVisible();

    // 3.1 Standing memory textarea and character count
    const repoMemoryInput = page.getByLabel(`Memory about ${repo}`);
    await expect(repoMemoryInput).toBeVisible();

    const initialValue = await repoMemoryInput.inputValue();
    const memoryNote =
      "Repository memory input verification text: verified automated input behaviors.";
    await repoMemoryInput.fill(memoryNote);

    // Verify Save button for repo memory is enabled
    const saveRepoMemoryBtn = page.getByRole("button", {
      name: `Save ${repo} memory`,
    });
    await expect(saveRepoMemoryBtn).toBeEnabled();

    // Verify Revert button restores initial content
    const revertRepoMemoryBtn = page.getByRole("button", {
      name: `Revert ${repo} memory`,
    });
    await revertRepoMemoryBtn.click();
    await expect(repoMemoryInput).toHaveValue(initialValue);

    // Re-fill and save to verify persistence
    await repoMemoryInput.fill(memoryNote);
    await saveRepoMemoryBtn.click();
    await page.reload();
    await expect(page.getByLabel(`Memory about ${repo}`)).toHaveValue(
      memoryNote,
    );

    // 3.2 Document summary and body editors in MemoryPanel
    const openDocBtn = page.getByRole("button", {
      name: /Open release-checklist/i,
    });
    if (await openDocBtn.isVisible()) {
      await openDocBtn.click();
      const summaryInput = page.getByRole("textbox", {
        name: /Summary of release-checklist/i,
      });
      const docTextInput = page.getByRole("textbox", {
        name: /Document: release-checklist/i,
      });
      await expect(summaryInput).toBeVisible();
      await expect(docTextInput).toBeVisible();

      const originalSummary = await summaryInput.inputValue();
      await summaryInput.fill("Pre-release verification steps and checklist.");
      await expect(summaryInput).toHaveValue(
        "Pre-release verification steps and checklist.",
      );

      // Revert back
      await summaryInput.fill(originalSummary);
      const closeDocBtn = page.getByRole("button", {
        name: /Close release-checklist/i,
      });
      await closeDocBtn.click();
    }

    // 3.3 Unsaved changes guard (dirty state tracking)
    await page
      .getByLabel(`Memory about ${repo}`)
      .fill("Unsaved draft text that must trigger confirmation on exit.");

    let confirmDialogShown = false;
    page.once("dialog", (dialog) => {
      confirmDialogShown = true;
      expect(dialog.message()).toContain(
        "You have unsaved changes here. Leave and discard them?",
      );
      dialog.dismiss(); // Stay on page
    });

    // Attempt to leave the Memory page from the rail
    await page.getByRole("button", { name: /^CI\b/ }).first().click();
    await expect.poll(() => confirmDialogShown).toBe(true);
    await expect(page).toHaveURL(/\/memory$/);

    // Clean up memory
    await repoMemoryInput.fill(initialValue);
    await page.getByRole("button", { name: `Save ${repo} memory` }).click();
  });
});
