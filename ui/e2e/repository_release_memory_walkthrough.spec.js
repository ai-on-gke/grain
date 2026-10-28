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
import { execFileSync } from "node:child_process";

test.describe("Repository, Release, and Memory UI Operator Walkthrough", () => {
  test("complete repository, release, qualification, and memory operator workflow", async ({
    page,
    request,
  }) => {
    test.setTimeout(90000);

    // -------------------------------------------------------------------------
    // 1. Repositories Index (/repos)
    // -------------------------------------------------------------------------

    // 1.1 Navigation from Rail and Direct URL
    // The repo page edits the deployment's own defaults: there has been
    // no team layer to switch away from since the single-tenant reset
    // (stage 3e-ii). This opens the Agents tab, and then the tab given.
    const deploymentWide = async (tab) => {
      await page.getByRole("tab", { name: "Agents" }).click();
      await expect(
        page.getByLabel(/Edit deployment-wide defaults/),
      ).toHaveCount(0);
      if (tab) await page.getByRole("tab", { name: tab }).click();
    };
    await page.goto("/");
    await page.getByRole("button", { name: /^Repos/ }).click();
    await expect(page).toHaveURL(/\/repos$/);
    await expect(
      page.getByRole("heading", { name: "Repos", exact: true }),
    ).toBeVisible();

    // Inspect seeded row
    const acmeRow = page
      .locator(".repo-list li")
      .filter({ hasText: "acme/widgets" })
      .first();
    await expect(acmeRow).toBeVisible();
    await expect(acmeRow.locator(".repo-list-name")).toHaveText("acme/widgets");

    // Verify header count badge
    const countLocator = page.locator(".content-header .count");
    await expect(countLocator).toBeVisible();
    await expect(countLocator).toHaveText(
      String(await page.locator(".repo-list li").count()),
    );
    const initialCount = parseInt(await countLocator.innerText(), 10);
    expect(initialCount).toBeGreaterThanOrEqual(1);

    // 1.2 Add repositories via stored configuration to test search, sorting, and responsive layout
    await request.post("/api/repos", {
      data: { repo: "acme/gadgets" },
    });
    const regRes1 = await request.put(
      "/api/repos/acme/gadgets/prompt-extension",
      {
        data: { promptExtension: "Standing instructions for gadgets." },
      },
    );
    expect(regRes1.ok()).toBeTruthy();

    const longRepo =
      "example-corporation/exceptionally-long-repository-name-for-responsive-layout";
    await request.post("/api/repos", {
      data: { repo: longRepo },
    });
    const regRes2 = await request.put(
      `/api/repos/${longRepo}/prompt-extension`,
      {
        data: { promptExtension: "Standing instructions for long repo." },
      },
    );
    expect(regRes2.ok()).toBeTruthy();

    await page.reload();
    await expect(
      page.locator(".repo-list-name").filter({ hasText: "acme/gadgets" }),
    ).toBeVisible();
    await expect(
      page.locator(".repo-list-name").filter({ hasText: longRepo }),
    ).toBeVisible();
    await expect(page.locator(".repo-list li")).toHaveCount(initialCount + 2);
    await expect(page.locator(".content-header .count")).toHaveText(
      String(initialCount + 2),
    );

    // 1.3 Search Filtering
    const searchInput = page.getByPlaceholder("Filter added repos…");
    await expect(searchInput).toBeVisible();

    // Case-insensitive match
    await searchInput.fill("WIDGETS");
    await expect(page.locator(".repo-list li")).toHaveCount(1);
    await expect(page.locator(".repo-list-name")).toHaveText("acme/widgets");

    // Non-matching query
    await searchInput.fill("nonexistent-repo-query");
    await expect(page.locator(".repo-list li")).toHaveCount(0);
    await expect(page.getByText("No repos match your search.")).toBeVisible();

    // Clear search
    await searchInput.fill("");
    await expect(page.locator(".repo-list li")).toHaveCount(initialCount + 2);

    // 1.4 Sorting Controls
    const sortSelect = page.getByRole("combobox", { name: "Sort" });
    await expect(sortSelect).toBeVisible();

    // Name (A–Z)
    await sortSelect.click();
    await page.getByRole("option", { name: /Name \(A–Z\)/i }).click();
    const namesAz = await page.locator(".repo-list-name").allInnerTexts();
    expect(namesAz).toContain("acme/gadgets");
    expect(namesAz).toContain("acme/widgets");
    expect(namesAz).toContain(longRepo);
    // Verify sorted ascending
    const sortedCopy = [...namesAz].sort((a, b) => a.localeCompare(b));
    expect(namesAz).toEqual(sortedCopy);
    // Reorder handles omitted when sorted
    await expect(page.locator("button.task-drag-handle")).toHaveCount(0);

    // Most tasks
    await sortSelect.click();
    await page.getByRole("option", { name: /Most tasks/i }).click();
    const namesTasks = await page.locator(".repo-list-name").allInnerTexts();
    expect(namesTasks).toContain("acme/widgets");
    expect(namesTasks).toContain("acme/gadgets");
    await expect(page.locator("button.task-drag-handle")).toHaveCount(0);

    // Custom order
    await sortSelect.click();
    await page.getByRole("option", { name: /Custom order/i }).click();
    await expect(page.locator("button.task-drag-handle")).toHaveCount(
      initialCount + 2,
    );

    // 1.5 Reordering via Pointer / Keyboard / Tap Affordances
    const moveHandle = page.locator("button.task-drag-handle").first();
    await moveHandle.click();
    // MovingBar banner appears
    await expect(page.getByText(/Moving 1 repo/i)).toBeVisible();

    // Drop slot button
    const moveSlotButtons = page.locator("button.reorder-slot-button");
    await expect(moveSlotButtons.first()).toBeVisible();
    await moveSlotButtons.last().click();
    await expect(page.getByText(/Moving 1 repo/i)).not.toBeVisible();

    // The same move from the keyboard: Enter on the handle, Enter on a
    // slot. The slot leaves the page as the row lands, and focus goes
    // back to the moved row's handle rather than falling to <body>.
    const keyboardHandle = page.locator("button.task-drag-handle").first();
    const keyboardHandleName = await keyboardHandle.getAttribute("aria-label");
    await keyboardHandle.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/Moving 1 repo/i)).toBeVisible();
    await moveSlotButtons.last().focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/Moving 1 repo/i)).not.toBeVisible();
    await expect(
      page.getByRole("button", { name: keyboardHandleName, exact: true }),
    ).toBeFocused();

    // Reload and verify persisted custom order in localStorage
    await page.reload();
    await expect(page.locator(".repo-list li")).toHaveCount(initialCount + 2);

    // 1.6 Responsive Layout Check (390px mobile viewport)
    await page.setViewportSize({ width: 390, height: 844 });
    const longRepoRow = page
      .locator(".repo-list li")
      .filter({ hasText: longRepo });
    await expect(longRepoRow).toBeVisible();
    // Verify no horizontal overflow
    const isOverflowing = await page.evaluate(() => {
      return (
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth
      );
    });
    expect(isOverflowing).toBeFalsy();
    await page.setViewportSize({ width: 1280, height: 800 });

    // -------------------------------------------------------------------------
    // 2. Repository Detail & Scoped Workflows (/repos/acme/widgets)
    // -------------------------------------------------------------------------

    // 2.1 Navigation & Back navigation
    await page
      .locator(".repo-list li")
      .filter({ hasText: "acme/widgets" })
      .click();
    await expect(page).toHaveURL(
      /\/repos\/acme\/widgets(\/settings\/[a-z-]+)?$/,
    );
    await expect(
      page.getByRole("heading", { name: "acme/widgets", exact: true }),
    ).toBeVisible();
    // This walkthrough edits the repo's own deployment-wide defaults,
    // which a system admin switches the page to (grain/task-3088).
    await deploymentWide();

    // Task count link navigation
    const tasksLink = page.getByRole("link", { name: /tasks? filed here/ });
    await expect(tasksLink).toBeVisible();
    await tasksLink.click();
    await expect(page).toHaveURL(
      /\/\?f=repo%3Aany%3Aacme%2Fwidgets|\/?f=repo:any:acme\/widgets/,
    );
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Browser back returns to repo page
    await page.goBack();
    await expect(page).toHaveURL(
      /\/repos\/acme\/widgets(\/settings\/[a-z-]+)?$/,
    );
    await deploymentWide();
    await expect(
      page.getByRole("heading", { name: "acme/widgets", exact: true }),
    ).toBeVisible();

    // 2.2 Task Filing with Repo Preselection, Persistence & Relationships
    await page.getByRole("button", { name: "New task", exact: true }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    // Repo preselected
    await expect(page.getByLabel(/^Repos/)).toHaveValue("acme/widgets");

    const taskTitle = `Walkthrough Task ${Date.now()}`;
    await page.getByLabel("Title").fill(taskTitle);
    await page
      .getByLabel("Description")
      .fill(
        "Verify repository scoped task filing and multi-repo relationships.",
      );

    // Add extra write repo relationship
    const writeRepoInput = page.getByLabel(/Another repo/i);
    await writeRepoInput.click();
    await page.getByRole("menuitem").getByText("acme/gadgets").click();
    await expect(page.getByLabel("Base branch in acme/gadgets")).toBeVisible();
    await page.getByLabel("Base branch in acme/gadgets").fill("candidate");

    // Add read-only repo relationship
    const readRepoInput = page.getByLabel(/Read-only repos/i);
    await readRepoInput.click();
    await page.getByRole("menuitem").getByText(longRepo).click();
    await expect(
      page.locator(".MuiChip-root").filter({ hasText: longRepo }),
    ).toBeVisible();

    // Escape with the suggestion list open closes the list, not the
    // drawer: the form and what was typed into it are still there.
    await readRepoInput.click();
    await expect(page.getByRole("menuitem").first()).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("menuitem")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await expect(page.getByLabel("Title")).toHaveValue(taskTitle);

    // Submit task
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(
      page.getByRole("heading", { name: "acme/widgets", exact: true }),
    ).toBeVisible();

    // 2.3 Branch Management and Comparison
    await page.getByRole("tab", { name: "Branches" }).click();
    const branchInput = page.getByLabel("Branch name");
    await expect(branchInput).toBeVisible();
    await branchInput.fill("task-396/operator-branch");
    await page.getByRole("button", { name: "Add branch" }).click();

    // Verify branch appears in branch list
    await expect(page.getByText("task-396/operator-branch")).toBeVisible();
    await page.reload();
    await deploymentWide("Branches");
    await expect(page.getByText("task-396/operator-branch")).toBeVisible();

    // Adding the same name again records nothing new: the form says the
    // name is already listed, and the list still holds one row for it.
    await branchInput.fill("task-396/operator-branch");
    await page.getByRole("button", { name: "Add branch" }).click();
    await expect(
      page.getByText("task-396/operator-branch is already on this repo's list"),
    ).toBeVisible();
    await page.reload();
    await deploymentWide("Branches");
    await expect(
      page.locator("li").filter({ hasText: "task-396/operator-branch" }),
    ).toHaveCount(1);

    // Compare branches
    const headInput = page.getByLabel("Head branch");
    await headInput.fill("task-396/operator-branch");
    await page.getByRole("button", { name: "Compare" }).click();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets\/compare/);
    await page.goBack();
    await expect(page).toHaveURL(
      /\/repos\/acme\/widgets(\/settings\/[a-z-]+)?$/,
    );

    // 2.4 Capabilities Configuration (CI and Default)
    // CI Capabilities
    await page.getByRole("tab", { name: "CI" }).click();
    const ciCapsSelect = page.getByRole("combobox", {
      name: "CI capabilities",
    });
    await expect(ciCapsSelect).toBeVisible();
    await ciCapsSelect.click();
    // Each pick saves on its own PUT; wait for it, or the reload below
    // can land before the save does and find nothing persisted.
    const ciCapsSaved = page.waitForResponse(
      (res) =>
        res.request().method() === "PUT" &&
        /\/api\/repos\/acme\/widgets\/ci-capabilities(\?|$)/.test(res.url()),
    );
    await page.getByRole("option", { name: /GCP key/i }).click();
    expect((await ciCapsSaved).ok()).toBeTruthy();
    await page.keyboard.press("Escape");
    await expect(page.locator(".MuiAlert-filledError")).not.toBeVisible();

    // Default Capabilities
    await page.getByRole("tab", { name: "Agents" }).click();
    const defCapsSelect = page.getByRole("combobox", {
      name: "Default capabilities",
    });
    await expect(defCapsSelect).toBeVisible();
    await defCapsSelect.click();
    const defCapsSaved = page.waitForResponse(
      (res) =>
        res.request().method() === "PUT" &&
        /\/api\/repos\/acme\/widgets\/capabilities(\?|$)/.test(res.url()),
    );
    await page.getByRole("option", { name: /Self debug/i }).click();
    expect((await defCapsSaved).ok()).toBeTruthy();
    await page.keyboard.press("Escape");
    await expect(page.locator(".MuiAlert-filledError")).not.toBeVisible();

    // Reload and verify persistence
    await page.reload();
    await deploymentWide("CI");
    await expect(
      page.locator(".MuiChip-root").filter({ hasText: /GCP key/i }),
    ).toBeVisible();
    await page.getByRole("tab", { name: "Agents" }).click();
    await expect(
      page.locator(".MuiChip-root").filter({ hasText: /Self debug/i }),
    ).toBeVisible();

    // 2.5 Prompt Extension and Setup Command
    const promptInput = page.getByLabel("Prompt extension for acme/widgets");
    await promptInput.fill(
      "Standing prompt extension for acme/widgets walkthrough.",
    );
    await promptInput.blur();

    const setupInput = page.getByLabel("Setup command for acme/widgets");
    await setupInput.fill("npm test && echo setup-done");
    await setupInput.blur();

    await page.reload();
    await deploymentWide();
    await expect(
      page.getByLabel("Prompt extension for acme/widgets"),
    ).toHaveValue("Standing prompt extension for acme/widgets walkthrough.");
    await expect(page.getByLabel("Setup command for acme/widgets")).toHaveValue(
      "npm test && echo setup-done",
    );

    // 2.6 Declarations Syncing (.grain/workflows/*.yml)
    await page.getByRole("tab", { name: "CI" }).click();
    const addWorkflowBtn = page.getByRole("button", { name: "Add workflow" });
    await addWorkflowBtn.click();

    const workflowPathInput = page.locator('input[name="repoWorkflowPath0"]');
    await expect(workflowPathInput).toBeVisible();
    await workflowPathInput.fill("e2e-test.yml");
    const workflowTextInput = page.locator(
      'textarea[name="repoWorkflowText0"]',
    );

    // Invalid YAML check
    await workflowTextInput.fill("invalid: [yaml: broken");
    await workflowTextInput.blur();
    await expect(page.locator(".MuiAlert-filledError")).toBeVisible();

    // Valid YAML
    await workflowTextInput.fill(
      "name: Test CI\non:\n  push:\n    branches: [main]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make test\n",
    );
    await workflowTextInput.blur();
    await expect(page.locator(".MuiAlert-filledError")).not.toBeVisible({
      timeout: 10000,
    });

    await page.reload();
    await deploymentWide("CI");
    await expect(page.locator('input[name="repoWorkflowPath0"]')).toHaveValue(
      ".grain/workflows/e2e-test.yml",
    );

    // Clean up workflow. The row's own Remove, which is named for the
    // file it deletes and asks first.
    const removeWorkflowBtn = page.getByRole("button", {
      name: "Remove workflow .grain/workflows/e2e-test.yml",
    });
    await expect(removeWorkflowBtn).toBeVisible();
    page.once("dialog", (dialog) => dialog.accept());
    await removeWorkflowBtn.click();
    await expect(page.locator('input[name="repoWorkflowPath0"]')).toHaveCount(
      0,
    );
    await page.reload();
    await deploymentWide("CI");
    await expect(
      page.getByText("grain declares no workflows for this repo."),
    ).toBeVisible();

    await page.getByRole("tab", { name: "Releases" }).click();
    // 2.7 Work Ownership configuration
    const defaultOnBtn = page.getByRole("button", {
      name: "the repository default: on",
    });
    await expect(defaultOnBtn).toBeVisible();
    await defaultOnBtn.click();
    await expect(page.getByText("Runs this repository's work")).toBeVisible();
    const defaultNothingBtn = page.getByRole("button", {
      name: "the repository default: say nothing",
    });
    await defaultNothingBtn.click();

    // -------------------------------------------------------------------------
    // 3. Memory UI (Settings → Memory, /settings/memory)
    // -------------------------------------------------------------------------
    await page.goto("/settings/memory");

    // Verify Deployment & Repo Memory Scopes
    await expect(
      page.getByText("This deployment", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByLabel("Memory carried by every run", { exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Memory about acme/widgets")).toBeVisible();

    // Author attribution chips
    await expect(
      page.getByText("written by the memory agent", { exact: true }).first(),
    ).toBeVisible();
    await expect(page.getByText("edited here")).toBeVisible();

    // Documents
    await expect(page.getByText("release-checklist")).toBeVisible();
    await expect(page.getByText("flaky-e2e")).toBeVisible();
    await expect(page.getByText(/written \d+ months ago/)).toBeVisible();

    // Open full document in place
    await page
      .getByText("release-checklist")
      .locator("xpath=ancestor::div[1]")
      .getByRole("button", { name: "Open" })
      .click();
    await expect(page.getByLabel("Document: release-checklist")).toContainText(
      "Tag from `main` only",
    );

    // With a second document open, each open document's fields are
    // still told apart by name rather than being two "Summary" boxes.
    await page
      .getByRole("button", {
        name: "Open flaky-e2e (acme/widgets)",
      })
      .click();
    await expect(
      page.getByRole("textbox", {
        name: "Summary of release-checklist (this deployment)",
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", {
        name: "Summary of flaky-e2e (acme/widgets)",
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Summary", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", {
        name: "Close flaky-e2e (acme/widgets)",
      })
      .click();

    // Reversible correction in repository memory
    const repoMemoryBox = page.getByLabel("Memory about acme/widgets");
    const originalMemoryText = await repoMemoryBox.inputValue();
    await repoMemoryBox.fill(
      originalMemoryText + "\nTested during operator walkthrough.",
    );
    await page
      .getByRole("button", { name: "Save acme/widgets memory" })
      .click();

    // Verify "written today" chip
    await expect(page.getByText("written today")).toBeVisible();

    // Reload and verify daemon readback
    await page.reload();
    await expect(page.getByLabel("Memory about acme/widgets")).toHaveValue(
      new RegExp("Tested during operator walkthrough."),
    );

    // Revert memory back to clean state
    await page.getByLabel("Memory about acme/widgets").fill(originalMemoryText);
    await page
      .getByRole("button", { name: "Save acme/widgets memory" })
      .click();

    // -------------------------------------------------------------------------
    // 4. Releases and Qualification (/repos/acme/widgets/releases)
    // -------------------------------------------------------------------------
    await page.goto("/repos/acme/widgets/releases");
    await expect(
      page.getByRole("heading", { name: "acme/widgets releases" }),
    ).toBeVisible();

    // 4.1 Form validation for release creation
    const releaseNameInput = page.getByLabel("New release name");
    await expect(releaseNameInput).toBeVisible();

    // Invalid release name containing spaces
    await releaseNameInput.fill("invalid release name with spaces");
    await page.getByRole("button", { name: "Create release" }).click();
    await expect(page.locator(".MuiAlert-filledError")).toBeVisible();

    // Valid release creation
    const releaseName = `rel-${Date.now()}`;
    await releaseNameInput.fill(releaseName);
    await page.getByRole("button", { name: "Create release" }).click();

    // Observe release status provisioning and candidate cutting
    // Both the Release picker ("<name> -- Provisioning") and, once its
    // status loads, the "<name> is still provisioning" hint match.
    await expect(
      page.getByText("Provisioning", { exact: false }).first(),
    ).toBeVisible();
    await expect(
      page.getByText(new RegExp(`${releaseName}\\.rc\\.1.*cutting`)).first(),
    ).toBeVisible();

    // Action buttons initially disabled
    const cutBtn = page.getByRole("button", { name: "Cut new RC" });
    const promoteBtn = page.getByRole("button", { name: "Promote current RC" });
    await expect(cutBtn).toBeDisabled();
    await expect(promoteBtn).toBeDisabled();

    // 4.2 Qualification Plan Configuration
    const autoPromoteSwitch = page.getByLabel(
      "Promote automatically once qualification succeeds",
    );
    const requireApprovalSwitch = page.getByLabel(
      "Require approval before running qualification tasks",
    );

    await expect(autoPromoteSwitch).toBeVisible();
    await expect(requireApprovalSwitch).toBeVisible();
    const autoPromoteWas = await autoPromoteSwitch.isChecked();
    const requireApprovalWas = await requireApprovalSwitch.isChecked();
    await autoPromoteSwitch.click();
    await expect(autoPromoteSwitch).toBeChecked({ checked: !autoPromoteWas });
    await requireApprovalSwitch.click();
    await expect(requireApprovalSwitch).toBeChecked({
      checked: !requireApprovalWas,
    });

    // Save qualification plan
    const savePlanBtn = page.getByRole("button", {
      name: "Save qualification plan",
    });
    await expect(savePlanBtn).toBeVisible();
    await savePlanBtn.click();

    // 4.3 External Boundary Simulation
    const port = new URL(page.url()).port;
    const helperBin =
      process.env.GRAIN_E2E_RELEASE_HELPER_BIN || "/tmp/releasehelper";

    // Every helper call below names this test's own release: left to
    // itself the helper acts on the oldest release, and another spec in
    // the same worker may have created one first.
    const own = ["-port", port, "-release", releaseName];

    // 1) Error state simulation: mark candidate error
    execFileSync(helperBin, [...own, "mark-candidate-error"]);
    await page.reload();
    await expect(
      page.getByText(/Simulated remote git branch failure/i),
    ).toBeVisible();

    // 2) Success state simulation: mark release active and candidate cut
    execFileSync(helperBin, [...own, "mark-release-provisioned"]);
    execFileSync(helperBin, [...own, "mark-candidate-cut"]);
    execFileSync(helperBin, ["-port", port, "sync-qualifications"]);

    await page.reload();
    await expect(
      page.getByText(new RegExp(`${releaseName}\\.rc\\.1.*active`)).first(),
    ).toBeVisible();
    await expect(promoteBtn).toBeEnabled();

    // -------------------------------------------------------------------------
    // 5. Release Trains Fallback Routes
    // -------------------------------------------------------------------------
    await page.goto("/repos/acme/widgets/trains");
    // Verify no unhandled exceptions / page crashes
    await expect(page.locator("body")).toBeVisible();

    await page.goto("/releasetrains");
    await expect(page.locator("body")).toBeVisible();
  });

  // Picking another release in the picker used to leave the previous
  // release's candidate -- its branch, its error and the Cut/Promote state
  // that follows from it -- on the page under the new release's name until
  // the pane's next poll (docs/test-runs/2026-10-01-repository-release-memory-ui.md).
  // The check right after the pick is deliberately not a retrying one: a
  // stale candidate that a later poll clears is exactly the bug.
  test("switching releases never shows the previous release's candidate", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    await page.goto("/repos/acme/widgets/releases");
    const nameBox = page.getByLabel("New release name");
    const create = async (name) => {
      await nameBox.fill(name);
      await page.getByRole("button", { name: "Create release" }).click();
      await expect(
        page.getByRole("combobox", { name: "Release" }),
      ).toContainText(name);
    };

    const existing = await (
      await request.get("/api/repos/acme/widgets/releases")
    ).json();
    if (existing.length === 0) await create(`older-${Date.now()}`);

    // The helper stands in for the GitHub releases reconciler, and acts on
    // the oldest release: provisioned, with its candidate cut and active.
    const port = new URL(page.url()).port;
    const helperBin =
      process.env.GRAIN_E2E_RELEASE_HELPER_BIN || "/tmp/releasehelper";
    execFileSync(helperBin, ["-port", port, "mark-release-provisioned"]);
    execFileSync(helperBin, ["-port", port, "mark-candidate-cut"]);
    const releases = await (
      await request.get("/api/repos/acme/widgets/releases")
    ).json();
    const older = releases[releases.length - 1].name;

    // A new release is the one selected once it is made, still cutting.
    const newer = `newer-${Date.now()}`;
    await create(newer);
    const currentCandidate = page.locator(".candidate-current");
    await expect(currentCandidate).toContainText(`${newer}.rc.1`);

    await page.getByRole("combobox", { name: "Release" }).click();
    await page
      .getByRole("option", { name: new RegExp(`^${older} -- `) })
      .click();
    expect(
      await page.locator(".candidate-current", { hasText: newer }).count(),
    ).toBe(0);
    expect(
      await page.getByText(`${newer}.rc.1`, { exact: false }).count(),
    ).toBe(0);

    await expect(currentCandidate).toContainText(`${older}.rc.`);
    await expect(currentCandidate).toContainText("active");
    await expect(
      page.getByRole("button", { name: "Promote current RC" }),
    ).toBeEnabled();
    // Cut new RC is off while that candidate is active, and the pane says
    // why right under it rather than leaving a greyed-out button
    // (docs/test-runs/2026-10-23-repository-release-memory-ui.md, task 273).
    const cutButton = page.getByRole("button", { name: "Cut new RC" });
    await expect(cutButton).toBeDisabled();
    const reasons = page.getByTestId("release-action-reasons");
    await expect(reasons).toContainText(
      new RegExp(
        `Cut new RC: ${older.replace(/\./g, "\\.")}\\.rc\\.\\d+ is active: a new RC is cut once it is promoted\\.`,
      ),
    );
    await expect(reasons).not.toContainText("Promote current RC:");
    const cutBox = await cutButton.boundingBox();
    const reasonsBox = await reasons.boundingBox();
    expect(reasonsBox.y).toBeGreaterThanOrEqual(cutBox.y + cutBox.height - 1);
    expect(Math.abs(reasonsBox.x - cutBox.x)).toBeLessThan(2);

    // The pick is in the address, so a reload keeps it rather than falling
    // back to the newest release, and Back steps to the release before it
    // (docs/test-runs/2026-10-04-repository-release-memory-ui.md).
    const picker = page.getByRole("combobox", { name: "Release" });
    await expect(page).toHaveURL(
      new RegExp(`/repos/acme/widgets/releases/${older}$`),
    );
    await page.reload();
    await expect(picker).toContainText(older);
    await expect(currentCandidate).toContainText(`${older}.rc.`);
    await page.goBack();
    await expect(page).toHaveURL(
      new RegExp(`/repos/acme/widgets/releases/${newer}$`),
    );
    await expect(picker).toContainText(newer);

    // A link naming a release the repo does not have opens the newest,
    // says so, and stops naming it.
    await page.goto("/repos/acme/widgets/releases/no-such-release");
    await expect(page.getByText('no release "no-such-release"')).toBeVisible();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases$/);
    await expect(picker).toContainText(newer);
  });
});
