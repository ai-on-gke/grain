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

// A change held on a review's findings offers "Accept findings…" beside
// each acceptable findings gate on its Changes tab
// (task-changes-tab.component.ts, accept-findings-dialog.component.ts).
// The demo seeds no findings, so task 5's answer is patched with #4591's
// shape -- review 4 left findings on both review gates -- and the
// acceptance is answered here, which keeps the worker's shared demo as it
// was for the other specs.

const FINDINGS = {
  autoMerge: false,
  state: "awaiting_submit",
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
    // The page polls this route. Under load one poll's response can be
    // disposed before it is read ("Response has been disposed"): answer
    // that poll with the last patched body, so the findings never drop out
    // mid-test.
    let last = null;
    await page.route(/\/api\/tasks\/5(\?.*)?$/, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      try {
        const response = await route.fetch();
        last = { ...(await response.json()), ...FINDINGS };
      } catch {
        if (!last) return route.fallback().catch(() => {});
      }
      await route.fulfill({ status: 200, json: last }).catch(() => {});
    });
    let posted = null;
    await page.route("**/api/tasks/5/accept-findings", async (route) => {
      posted = route.request().postDataJSON();
      await route.fulfill({ status: 200, json: { task: "5", gates: [] } });
    });
    await page.setViewportSize(viewport);
    await page.goto("/next/task/5?tab=changes");

    // The action area's gates list, where each gate's own actions sit.
    const row = page
      .getByTestId("task-action-area")
      .getByTestId("merge-gates-list");
    await expect(row).toBeVisible();
    for (const gate of ["security-review", "review"]) {
      // The gate's own state pill, inside its row of the list.
      const pill = row
        .getByTestId(`merge-gate-${gate}`)
        .locator(".status-pill");
      const button = row.getByTestId(`gate-${gate}-accept-findings`);
      await expect(button).toBeVisible();
      // In the gates row, on screen, and clear of its own gate's pill.
      const b = await button.boundingBox();
      const r = await row.boundingBox();
      const p = await pill.boundingBox();
      expect(b.y).toBeGreaterThanOrEqual(r.y - 1);
      expect(b.y + b.height).toBeLessThanOrEqual(r.y + r.height + 1);
      expect(b.x).toBeGreaterThanOrEqual(r.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(r.x + r.width + 1);
      expect(b.x + b.width).toBeLessThanOrEqual(viewport.width);
      const overlaps =
        b.x < p.x + p.width &&
        p.x < b.x + b.width &&
        b.y < p.y + p.height &&
        p.y < b.y + b.height;
      expect(overlaps).toBe(false);
    }
    await expect(row.getByTestId("gate-ci-accept-findings")).toHaveCount(0);

    await row.getByTestId("gate-security-review-accept-findings").click();
    const dialog = page.getByTestId("accept-findings-dialog");
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1);
    const confirm = dialog.getByTestId("accept-findings-confirm");
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
    await page.unrouteAll({ behavior: "ignoreErrors" });
  });
}
