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

// The task Changes tab's merge-gate row says what blocks a gate: a review
// gate held on findings shows the count and first finding, a failing CI
// gate the check that failed, each on one line inside the row, cut with
// an ellipsis when it does not fit. The demo seeds no held gates, so the
// task's detail is answered with them on top of a real task.

const FINDING =
  "3 findings: `pkg/orchestrator/mergequeue.go:412` The repair deadline is read before the lock is taken, so two cycles can both file a repair.";

async function fileTask(baseURL, code, title) {
  const res = await fetch(`${baseURL}/api/tasks`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ title, approved: true }),
  });
  if (!res.ok) throw new Error(`filing: ${res.status} ${await res.text()}`);
  return (await res.json()).id;
}

test("blocked gate chips name the first finding and the failing check @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const id = await fileTask(demoServer, accessCode, `Gate chips ${Date.now()}`);
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const detail = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...detail,
        state: "awaiting_submit",
        pullRequest: "acme/widgets#42",
        mergeGates: [
          {
            id: "ci",
            label: "CI",
            state: "failing",
            detail: "failing: Tests / ui e2e at abcdef012345",
          },
          {
            id: "review",
            label: "Code review",
            state: "findings",
            detail: FINDING,
            reviewTask: "9001",
          },
          {
            id: "approval",
            label: "Approval",
            state: "needed",
            detail: "needed",
          },
        ],
      },
    });
  });
  await page.goto(`task/${id}?tab=changes`);

  // The task page shows the gates in its action area, on every tab.
  const row = page
    .getByTestId("task-action-area")
    .getByTestId("merge-gates-list");
  const review = row.locator('[data-gate="review"]');
  await expect(review).toContainText(
    "Code review: 3 findings: `pkg/orchestrator/mergequeue.go:412`",
  );
  await expect(row.locator('[data-gate="ci"]')).toHaveText(
    /CI:\s*failing: Tests \/ ui e2e at abcdef012345/,
  );
  await expect(row.locator('[data-gate="approval"]')).toHaveText(
    /Approval:\s*needed/,
  );

  // One line, inside the row, label and detail on one baseline.
  const rowBox = await row.boundingBox();
  const box = await review.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(rowBox.x - 0.5);
  expect(box.x + box.width).toBeLessThanOrEqual(rowBox.x + rowBox.width + 0.5);
  expect(box.height).toBeLessThanOrEqual(28);
  const label = await review.locator(".gate-chip-label").boundingBox();
  const text = await review.locator(".gate-chip-detail").boundingBox();
  expect(
    Math.abs(label.y + label.height / 2 - (text.y + text.height / 2)),
  ).toBeLessThanOrEqual(1);
});
