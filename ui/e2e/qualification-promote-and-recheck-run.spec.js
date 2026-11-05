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

// docs/test-runs/2026-11-03-repository-release-memory-ui.md, observations
// 2 and 3:
//   - The seeded merge re-check batch on acme/widgets carries a -fake-ci
//     run id ("demo-recheck-1"), so MergeBatchPanel renders "Show the run"
//     and opening /ci/demo-recheck-1 loads a real CI run page.
//   - -github-sim -qualification seeds 1.0.latest and 1.0.rc.1 in the
//     simulated bare repo and marks release 1.0 provisioned, so Promote
//     current RC reaches "promoted" rather than sticking on a missing
//     candidate branch.
test.use({
  demoArgs: [
    "-github-sim",
    "-fake-ci",
    "-qualification",
    "-reconcile",
    "200ms",
  ],
});

test.describe("Qualification demo promote and merge re-check run link", () => {
  test("merge re-check batch renders Show the run and opens a real /ci/<id> page", async ({
    page,
  }) => {
    const batchesRes = await page.request.get(
      "/api/merge-batches?repo=acme/widgets",
    );
    expect(batchesRes.ok()).toBe(true);
    const { batches } = await batchesRes.json();
    expect(batches).toHaveLength(1);
    const batch = batches[0];
    expect(batch.runId).toBe("demo-recheck-1");
    const memberTaskId = batch.members[0].task;

    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/repos/acme/widgets");

      const panel = page
        .getByRole("heading", { name: "Merge re-checks" })
        .locator("xpath=..");
      const countLabel = panel.getByText(/1 change on main/i);
      const showRun = panel.getByRole("button", { name: "Show the run" });
      await expect(countLabel).toBeVisible();
      await expect(showRun).toBeVisible();

      const [countBox, box] = await Promise.all([
        countLabel.boundingBox(),
        showRun.boundingBox(),
      ]);
      if (width === 1280) {
        expect(box.x).toBeGreaterThanOrEqual(countBox.x + countBox.width);
        expect(
          Math.abs(countBox.y + countBox.height / 2 - (box.y + box.height / 2)),
        ).toBeLessThanOrEqual(4);
      }
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
    }

    await page.setViewportSize({ width: 1280, height: 900 });
    const repoShowRun = page
      .getByRole("heading", { name: "Merge re-checks" })
      .locator("xpath=..")
      .getByRole("button", { name: "Show the run" });
    await repoShowRun.click();

    const runDialog = page.getByRole("dialog", {
      name: `CI run ${batch.runId}`,
    });
    await expect(runDialog).toBeVisible();
    await expect(runDialog).toContainText("acme/widgets");
    await expect(runDialog).toContainText(batch.ref);
    await expect(runDialog).toContainText("abcdef012345");
    await expect(runDialog).toContainText("test");
    await page.keyboard.press("Escape");
    await expect(runDialog).toHaveCount(0);

    await page.goto(`/tasks/${memberTaskId}`);
    const taskShowRun = page
      .getByText("Merge re-check", { exact: true })
      .locator("xpath=..")
      .getByRole("button", { name: "Show the run" });
    await expect(taskShowRun).toBeVisible();
    await taskShowRun.click();
    await expect(runDialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(runDialog).toHaveCount(0);

    await page.goto(`/ci/${batch.runId}`);
    await expect(page).toHaveURL(new RegExp(`/ci/${batch.runId}$`));
    await expect(runDialog).toBeVisible();
    await expect(runDialog).toContainText("acme/widgets");
    await expect(runDialog).toContainText(batch.ref);
    await expect(runDialog).toContainText("abcdef012345");
    await expect(runDialog.getByRole("alert")).toHaveCount(0);
  });

  test("-qualification seeds release 1.0 active on -github-sim and promotes 1.0.rc.1 to promoted", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/repos/acme/widgets/releases/1.0");

    const picker = page.getByRole("combobox", { name: "Release" });
    await expect(picker).toHaveValue("1.0 -- Active");

    const currentCandidate = page.locator(".candidate-current");
    await expect(currentCandidate).toContainText("1.0.rc.1 -- active");
    await expect(currentCandidate.locator(".candidate-error")).toHaveCount(0);

    const cutBtn = page.getByRole("button", { name: "Cut new RC" });
    const promoteBtn = page.getByRole("button", { name: "Promote current RC" });
    await expect(cutBtn).toBeDisabled();
    await expect(promoteBtn).toBeEnabled();

    const approveBtn = page.getByRole("button", { name: "Approve all" });
    await expect(approveBtn).toBeVisible();
    await approveBtn.click();
    await expect(approveBtn).toHaveCount(0);

    let confirmText = "";
    page.once("dialog", (dialog) => {
      confirmText = dialog.message();
      return dialog.accept();
    });
    await promoteBtn.click();
    expect(confirmText).toContain("Promote 1.0.rc.1 to 1.0?");

    await expect(currentCandidate).toContainText("1.0.rc.1 -- promoted", {
      timeout: 15_000,
    });
    await expect(currentCandidate.locator(".candidate-error")).toHaveCount(0);
    await expect(promoteBtn).toBeDisabled();
    await expect(cutBtn).toBeEnabled();
    await expect(page.getByTestId("release-action-reasons")).toContainText(
      "1.0.rc.1 is already promoted: a candidate is promoted once.",
    );
  });
});
