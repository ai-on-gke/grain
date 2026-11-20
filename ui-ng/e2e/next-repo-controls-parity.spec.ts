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

// Port of the remaining classic-only repository controls to /next/
// (2026-11-20 repository walkthrough, task 7546 -> task 7696):
//   1. Native /next/ compare page (/next/repos/<o>/<n>/compare/<base>...<head>)
//      with base/head branch pickers on Branches and Compare, handling slashes,
//      %23, unknown refs (404 -> in-place warning), reload, and deep links.
//   2. Upstream repository on Agents (refusal beside the box, cleared on next
//      clean save, unsaved-edit leave-guard, persistence across reload).
//   3. Unfixable checks on Merge Queue (add, client + server refusals, remove).
//   4. Merge-queue overlap re-check panel on Merge Queue and on the re-checking
//      member's task page (standing chip, elapsed vs max age, CI run & task
//      links).
//   5. Read-only deployment & .grain/settings.yml prompt-extension layers
//      beside the editable Standing prompt extension.
//   6. Role read-only and config-repo-managed read-only states.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification", "-fake-ci"] });

const R = "/next/repos/acme/widgets";

const repoSettingsOf = async (page: Page) => {
  const res = await page.request.get("/api/repos/acme/widgets/settings");
  expect(res.ok()).toBe(true);
  return res.json();
};

const unfixableOf = async (page: Page) => {
  const res = await page.request.get(
    "/api/repos/acme/widgets/unfixable-checks",
  );
  expect(res.ok()).toBe(true);
  return res.json();
};

const deploymentSettingsOf = async (page: Page) => {
  const res = await page.request.get("/api/settings");
  expect(res.ok()).toBe(true);
  return res.json();
};

const patchConfig = (page: Page, patch: Record<string, unknown>) =>
  page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        json: { ...(await response.json()), ...patch },
      });
    },
  );

const checkNoHorizontalOverflow = async (page: Page) => {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
};

test("compare branches picker and /next/ compare page handle slashes, %23, 404, deep links, and reload @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto(`${R}/branches`);
  await expect(page.getByTestId("card-repo-compare")).toBeVisible();

  const baseInput = page.getByTestId("repo-compare-base");
  const headInput = page.getByTestId("repo-compare-head");
  await expect(baseInput).toHaveValue("main");

  // Focusing the head picker opens suggestions from the simulated GitHub repo.
  await headInput.click();
  await expect(
    page.getByRole("option", { name: "release/1.0", exact: true }),
  ).toBeVisible();
  await page.getByRole("option", { name: "release/1.0", exact: true }).click();
  await expect(headInput).toHaveValue("release/1.0");

  await page.getByTestId("repo-compare-submit").click();
  await expect(page).toHaveURL(
    /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.release\/1\.0$/,
  );
  await expect(page.getByTestId("compare-heading")).toHaveText("release/1.0");
  await expect(page.getByTestId("compare-subtitle")).toContainText(
    "main on acme/widgets",
  );
  await expect(page.getByTestId("compare-status-pill")).toBeVisible();
  await checkNoHorizontalOverflow(page);
  expect(await axe()).toEqual([]);

  // Survives a full browser reload with the slash branch intact.
  await page.reload();
  await expect(page).toHaveURL(
    /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.release\/1\.0$/,
  );
  await expect(page.getByTestId("compare-heading")).toHaveText("release/1.0");

  // Submitting a branch containing '#' from the compare page picker encodes it
  // as %23 in the URL and shows the server's 404 "nothing to compare" banner
  // in place.
  const cmpHead = page.getByTestId("compare-head-input");
  await cmpHead.fill("fix/#7696-missing");
  await page.getByTestId("compare-submit-btn").click();
  await expect(page).toHaveURL(
    /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%237696-missing$/,
  );
  await expect(page.getByTestId("compare-heading")).toHaveText(
    "fix/#7696-missing",
  );
  const errBanner = page.getByTestId("compare-error");
  await expect(errBanner).toBeVisible();
  await expect(errBanner).toContainText(
    /GitHub has nothing to compare for main\.\.\.fix\/#7696-missing/,
  );

  // Deep link + reload with %23 keeps the decoded ref name and in-place error.
  await page.reload();
  await expect(page).toHaveURL(
    /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%237696-missing$/,
  );
  await expect(page.getByTestId("compare-heading")).toHaveText(
    "fix/#7696-missing",
  );
  await expect(page.getByTestId("compare-error")).toContainText(
    /nothing to compare/i,
  );
  await checkNoHorizontalOverflow(page);
  expect(await axe()).toEqual([]);
});

test("Agents tab edits upstreamRepo with refusal beside the box, leave-guard, and read-only prompt-extension layers @phone @dark", async ({
  page,
  axe,
}) => {
  const beforeRepo = await repoSettingsOf(page);
  const beforeDeploy = await deploymentSettingsOf(page);
  try {
    // Seed a real deployment-wide prompt extension via /api/settings so both
    // deploymentPromptExtension and effectivePromptExtension come straight from
    // the server.
    const setDeploy = await page.request.put("/api/settings", {
      data: { promptExtension: "Deployment-wide instruction for e2e." },
    });
    expect(setDeploy.ok()).toBe(true);

    await page.goto(`${R}/agents`);

    // Deployment prompt layer is visible read-only beside the editable box.
    const deployLayer = page.getByTestId("repo-prompt-deployment-layer");
    await expect(deployLayer).toContainText(
      "Deployment-wide instruction for e2e.",
    );

    // Editing the repo prompt extension and saving updates effectivePromptExtension
    // in place without a reload.
    const promptBox = page.getByTestId("input-repo-prompt-ext");
    await promptBox.fill("Repository-specific instruction for e2e.");
    await page.getByTestId("save-repo-agents-btn").click();
    const effectiveLayer = page.getByTestId("repo-prompt-effective-layer");
    await expect(effectiveLayer).toContainText(
      "Deployment-wide instruction for e2e.",
    );
    await expect(effectiveLayer).toContainText(
      "Repository-specific instruction for e2e.",
    );

    // Upstream repository: invalid value shows refusal beside the box (not in
    // the page-wide banner), and the leave-guard protects the unsaved edit.
    const upstreamInput = page.getByTestId("input-repo-upstream-repo");
    await upstreamInput.fill("not a repo");
    await page.getByTestId("save-repo-agents-btn").click();
    const refusal = page.getByTestId("repo-upstream-repo-error");
    await expect(refusal).toContainText(/upstreamRepo:/);
    await expect(upstreamInput).toHaveAttribute("aria-invalid", "true");
    await expect(page.getByTestId("repo-error-banner")).toHaveCount(0);

    // Switching tab asks before discarding; Cancel keeps the box and refusal.
    await page.getByTestId("repo-tab-branches").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("unsaved agent settings changes");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/agents$/);
    await expect(upstreamInput).toHaveValue("not a repo");

    // Clean save clears the refusal, updates summary, and survives a reload.
    await upstreamInput.fill("upstream-org/widgets");
    await page.getByTestId("save-repo-agents-btn").click();
    await expect(refusal).toHaveCount(0);
    await expect(page.getByTestId("repo-upstream-repo-summary")).toContainText(
      "upstream-org/widgets",
    );
    await expect
      .poll(async () => (await repoSettingsOf(page)).upstreamRepo)
      .toBe("upstream-org/widgets");

    await checkNoHorizontalOverflow(page);
    expect(await axe()).toEqual([]);

    await page.reload();
    await expect(page.getByTestId("input-repo-upstream-repo")).toHaveValue(
      "upstream-org/widgets",
    );
    await expect(page.getByTestId("repo-upstream-repo-summary")).toContainText(
      "upstream-org/widgets",
    );

    // Clearing upstreamRepo stores "" and persists across reload.
    await page.getByTestId("input-repo-upstream-repo").fill("");
    await page.getByTestId("save-repo-agents-btn").click();
    await expect
      .poll(async () => (await repoSettingsOf(page)).upstreamRepo)
      .toBe("");
    await expect(page.getByTestId("repo-upstream-repo-summary")).toContainText(
      "acme/widgets",
    );

    // Verify the read-only .grain/settings.yml declared prompt layer renders
    // when the server reports declaredPromptExtension.
    await page.route(
      (url) => url.pathname === "/api/repos/acme/widgets/settings",
      async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        body.declaredPromptExtension = "Declared in .grain/settings.yml.";
        body.effectivePromptExtension = [
          body.deploymentPromptExtension,
          body.promptExtension,
          body.declaredPromptExtension,
        ]
          .filter(Boolean)
          .join("\n\n");
        await route.fulfill({ response: res, json: body });
      },
    );
    await page.reload();
    await expect(page.getByTestId("repo-prompt-declared-layer")).toContainText(
      "Declared in .grain/settings.yml.",
    );
    await expect(page.getByTestId("repo-prompt-effective-layer")).toContainText(
      "Declared in .grain/settings.yml.",
    );
  } finally {
    await page.request.put("/api/repos/acme/widgets/settings", {
      data: { upstreamRepo: beforeRepo.upstreamRepo ?? "" },
    });
    await page.request.put("/api/repos/acme/widgets/prompt-extension", {
      data: { promptExtension: beforeRepo.promptExtension ?? "" },
    });
    await page.request.put("/api/settings", {
      data: { promptExtension: beforeDeploy.promptExtension ?? "" },
    });
  }
});

test("Merge Queue manages unfixable checks and shows the overlap re-check panel on repo and task pages @phone @dark", async ({
  page,
  axe,
}) => {
  const beforeUnfixable = (await unfixableOf(page)).unfixableChecks ?? [];
  try {
    await page.goto(`${R}/merge-queue`);

    // 1. Unfixable checks: empty state, client refusal, add, reload, duplicate
    // refusal, server comma refusal, and remove.
    const unfixCard = page.getByTestId("card-repo-unfixable-checks");
    await expect(unfixCard).toBeVisible();
    await expect(page.getByTestId("unfixable-checks-empty")).toBeVisible();

    const input = page.getByTestId("input-unfixable-check");
    const addBtn = page.getByTestId("add-unfixable-check-btn");

    // Refuses grain CI locally.
    await input.fill("grain CI");
    await addBtn.click();
    await expect(page.getByTestId("unfixable-checks-error")).toContainText(
      /judges one exact commit/,
    );

    // Adding cla/google persists across reload.
    await input.fill("cla/google");
    await expect(page.getByTestId("unfixable-checks-error")).toHaveCount(0);
    await addBtn.click();
    await expect(page.getByTestId("unfixable-check-chip")).toContainText(
      "cla/google",
    );
    await expect
      .poll(async () => (await unfixableOf(page)).unfixableChecks)
      .toEqual(["cla/google"]);

    await page.reload();
    await expect(page.getByTestId("unfixable-check-chip")).toContainText(
      "cla/google",
    );

    // Refuses case-insensitive duplicate locally.
    await page.getByTestId("input-unfixable-check").fill("CLA/Google");
    await page.getByTestId("add-unfixable-check-btn").click();
    await expect(page.getByTestId("unfixable-checks-error")).toContainText(
      /already declares CLA\/Google unfixable/,
    );

    // Refuses comma-containing name via the real server 400.
    await page.getByTestId("input-unfixable-check").fill("one,two");
    await page.getByTestId("add-unfixable-check-btn").click();
    await expect(page.getByTestId("unfixable-checks-error")).toContainText(
      /comma/,
    );

    // 2. Overlap re-check panel on Merge Queue shows the real seeded re-check.
    const rechecks = page.getByTestId("card-repo-merge-rechecks");
    await expect(rechecks).toBeVisible();
    await expect(rechecks.getByTestId("merge-batch-standing")).toContainText(
      "Testing",
    );
    await expect(rechecks.getByTestId("merge-batch-age")).toContainText(
      /of 90m/,
    );
    await expect(rechecks.getByTestId("merge-batch-ref")).toContainText(
      /abcdef012345/,
    );
    const runLink = rechecks.getByTestId("merge-batch-run-link");
    await expect(runLink).toHaveAttribute(
      "href",
      "/next/ci/runs/demo-recheck-1",
    );

    await checkNoHorizontalOverflow(page);
    expect(await axe()).toEqual([]);

    // Remove cla/google and verify clean state.
    await page.getByTestId("remove-unfixable-check-cla/google").click();
    await expect(page.getByTestId("unfixable-checks-empty")).toBeVisible();
    await expect
      .poll(async () => (await unfixableOf(page)).unfixableChecks ?? [])
      .toEqual([]);

    // Follow the member task link to the task page and confirm the same
    // overlap re-check panel renders there too.
    await rechecks.getByTestId("merge-batch-member-task").click();
    await expect(page).toHaveURL(/\/next\/task\/\d+$/);
    const taskRecheck = page.getByTestId("task-merge-recheck");
    await expect(taskRecheck).toBeVisible();
    await expect(taskRecheck.getByTestId("merge-batch-standing")).toContainText(
      "Testing",
    );
    await expect(taskRecheck.getByTestId("merge-batch-age")).toContainText(
      /of 90m/,
    );
    await expect(
      taskRecheck.getByTestId("merge-batch-run-link"),
    ).toHaveAttribute("href", "/next/ci/runs/demo-recheck-1");

    // Follow the CI run link from the panel.
    await taskRecheck.getByTestId("merge-batch-run-link").click();
    await expect(page).toHaveURL(/\/next\/ci\/runs\/demo-recheck-1$/);
  } finally {
    await page.request.put("/api/repos/acme/widgets/unfixable-checks", {
      data: { unfixableChecks: beforeUnfixable },
    });
  }
});

test("role read-only and config-repo read-only states lock write controls without breaking read views", async ({
  page,
}) => {
  const beforeUnfixable = (await unfixableOf(page)).unfixableChecks ?? [];
  try {
    await page.request.put("/api/repos/acme/widgets/unfixable-checks", {
      data: { unfixableChecks: ["cla/google"] },
    });

    // 1. Viewer role: repos:read + ci:read, no repos:write.
    await patchConfig(page, {
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["repos:read", "ci:read"],
      },
    });
    await page.goto(`${R}/agents`);
    await expect(page.getByTestId("agents-denied")).toBeVisible();
    await expect(page.getByTestId("input-repo-upstream-repo")).toBeDisabled();
    await expect(page.getByTestId("input-repo-prompt-ext")).toBeDisabled();
    await expect(page.getByTestId("save-repo-agents-btn")).toBeDisabled();

    await page.goto(`${R}/merge-queue`);
    await expect(page.getByTestId("unfixable-checks-denied")).toBeVisible();
    await expect(page.getByTestId("input-unfixable-check")).toBeDisabled();
    await expect(page.getByTestId("add-unfixable-check-btn")).toBeDisabled();
    await expect(
      page.getByTestId("remove-unfixable-check-cla/google"),
    ).toHaveCount(0);
    // Read-only re-check panel still renders with ci:read.
    await expect(
      page
        .getByTestId("card-repo-merge-rechecks")
        .getByTestId("merge-batch-standing"),
    ).toContainText("Testing");

    // 2. Config-repo-managed deployment: settings locked in UI.
    await page.unroute((url) => url.pathname === "/api/config");
    await patchConfig(page, { configRepoOnly: true });
    await page.goto(`${R}/agents`);
    await expect(page.getByTestId("agents-config-managed")).toBeVisible();
    await expect(page.getByTestId("input-repo-upstream-repo")).toBeDisabled();
    await expect(page.getByTestId("save-repo-agents-btn")).toBeDisabled();

    await page.goto(`${R}/merge-queue`);
    await expect(
      page.getByTestId("unfixable-checks-config-managed"),
    ).toBeVisible();
    await expect(page.getByTestId("input-unfixable-check")).toBeDisabled();
    await expect(
      page.getByTestId("remove-unfixable-check-cla/google"),
    ).toHaveCount(0);

    // Compare remains readable in config-repo-managed mode.
    await page.goto(`${R}/compare/main...release/1.0`);
    await expect(page.getByTestId("compare-heading")).toHaveText("release/1.0");
  } finally {
    await page.request.put("/api/repos/acme/widgets/unfixable-checks", {
      data: { unfixableChecks: beforeUnfixable },
    });
  }
});
