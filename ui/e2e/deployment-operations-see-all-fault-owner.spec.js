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

// Regressions found by the 2026-10-29 deployment-operations walkthrough
// (docs/test-runs/2026-10-29-deployment-operations.md), driven through
// the real demo server and built frontend:
//
//   - the alerts bell's popover had no "see all" to System → Health,
//     which docs/design/settings-panel.md §4.1 says it has;
//   - the CI page dropped a shared fault's ownerTaskId, so nothing said
//     which task was assigned to fix the failure every branch was hitting.
test.use({ demoArgs: ["-observability"] });

test("the bell's See all opens System → Health and closes the popover", async ({
  page,
}) => {
  await page.goto("/inbox");
  await page.getByTestId("alerts-button").first().click();
  const panel = page.getByTestId("alerts-panel");
  await expect(panel).toBeVisible();
  // Reachable from the keyboard, after the alerts it sits below.
  const seeAll = page.getByTestId("alerts-see-all");
  await expect(seeAll).toHaveText(/See all on System → Health/i);
  await seeAll.press("Enter");
  await expect(panel).toBeHidden();
  await expect(page).toHaveURL(/\/system$/);
  await expect(
    page.getByRole("tab", { name: "Health", selected: true }),
  ).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/inbox$/);
});

test("on a phone, See all opens the Health section itself", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/inbox");
  await page.getByTestId("alerts-button").first().click();
  await page.getByTestId("alerts-see-all").click();
  await expect(page).toHaveURL(/\/system\/health$/);
});

test("a shared CI fault names and links the task assigned to fix it", async ({
  page,
}) => {
  // The demo's shared fault is three feature branches no task owns, so
  // the server resolves no owner for it (cifault.ResolveOwner). Only the
  // owner is added to the real answer; everything else on the page is
  // the demo's own.
  await page.route(/\/api\/ci\/runs(\?.*)?$/, async (route) => {
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    for (const fault of body.faults || []) {
      fault.ownerTaskId = "2";
      fault.ownerRef = "grain/task-2";
    }
    await route.fulfill({ response, json: body });
  });
  try {
    await page.goto("/ci");
    const owner = page.getByTestId("ci-fault-owner").first();
    await expect(owner).toHaveText("Assigned to task #2 (grain/task-2)");
    const link = owner.getByRole("link", { name: "task #2" });
    await expect(link).toHaveAttribute("href", "/tasks/2");
    await link.click();
    await expect(page).toHaveURL(/\/tasks\/2$/);
  } finally {
    await page.unroute(/\/api\/ci\/runs(\?.*)?$/);
  }
});
