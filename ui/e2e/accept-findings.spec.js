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

import { expect, test } from "./fixtures.js";

// A change held on a review's findings offers "Accept findings…" beside
// each findings-blocked gate on its merge-gates panel (MergeGatesList.jsx,
// AcceptFindingsDialog.jsx). The demo seeds no findings, so task 5's
// answer is patched with #4591's shape -- review 4 left findings on both
// review gates -- and the acceptance is answered here, which keeps the
// worker's shared demo as it was for the other specs.

const FINDINGS = {
  autoMerge: false,
  mergeGates: [
    { id: "ci", label: "CI", state: "passed", detail: "passed" },
    {
      id: "security-review",
      label: "Security review",
      state: "findings",
      detail: "security review left findings",
      reviewTask: "4",
      overridable: true,
      acceptable: true,
    },
    {
      id: "review",
      label: "Code review",
      state: "findings",
      detail: "every-pr review left findings",
      reviewTask: "4",
      overridable: true,
      acceptable: true,
    },
    { id: "approval", label: "Approval", state: "needed" },
  ],
};

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]) {
  test(`a findings-blocked gate offers Accept findings at ${viewport.width}px`, async ({
    page,
  }) => {
    await page.route(/\/api\/tasks\/5(\?.*)?$/, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const json = await response.json();
      await route.fulfill({ response, json: { ...json, ...FINDINGS } });
    });
    let posted = null;
    await page.route("**/api/tasks/5/accept-findings", async (route) => {
      posted = route.request().postDataJSON();
      await route.fulfill({ status: 200, json: { task: "5", gates: [] } });
    });
    await page.setViewportSize(viewport);
    await page.goto("/tasks/5");

    const panel = page.getByTestId("merge-gates");
    await expect(panel).toBeVisible();
    for (const gate of ["security-review", "review"]) {
      const row = panel.getByTestId(`merge-gate-${gate}`);
      const button = row.getByTestId(`accept-findings-${gate}`);
      await expect(button).toBeVisible();
      // In its own gate's row, inside the panel, on screen.
      const b = await button.boundingBox();
      const r = await row.boundingBox();
      const p = await panel.boundingBox();
      expect(b.y).toBeGreaterThanOrEqual(r.y - 1);
      expect(b.y + b.height).toBeLessThanOrEqual(r.y + r.height + 1);
      expect(b.x).toBeGreaterThanOrEqual(p.x);
      expect(b.x + b.width).toBeLessThanOrEqual(p.x + p.width + 1);
      expect(b.x + b.width).toBeLessThanOrEqual(viewport.width);
    }
    await expect(
      panel.getByTestId("merge-gate-ci").getByText("Accept findings…"),
    ).toHaveCount(0);

    await panel.getByTestId("accept-findings-security-review").click();
    const dialog = page.getByTestId("accept-findings-dialog");
    await expect(dialog).toBeVisible();
    const confirm = dialog.getByTestId("confirm-accept-findings");
    await expect(confirm).toBeDisabled();
    await dialog
      .getByTestId("accept-findings-reason")
      .fill("merge as-is; follow-up filed");
    await confirm.click();
    await expect
      .poll(() => posted)
      .toMatchObject({
        gate: "security-review",
        reviewTask: "4",
        reason: "merge as-is; follow-up filed",
      });
  });
}
