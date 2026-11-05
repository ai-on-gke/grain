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

const VIEWPORTS = [
  { label: "desktop", width: 1440, height: 900 },
  { label: "phone", width: 390, height: 844 },
];

test("task owner display, inline transfer + audit, owner filter (with me), and Proposed timeline attribution", async ({
  page,
  request,
}) => {
  // 1. Seeded subtask #11 shows run-proposed attribution with links, owner
  //    dana@example.com, and an Owner changed audit step, cleanly laid out at
  //    desktop and phone widths without overflowing.
  for (const vp of VIEWPORTS) {
    await page.setViewportSize(vp);
    await page.goto("/tasks/11");
    await expect(page.getByTestId("task-owner")).toHaveText("dana@example.com");
    await expect(
      page.getByRole("button", { name: "Change owner" }),
    ).toBeVisible();
    await expect(page.getByTestId("proposing-task-link")).toHaveText("#8");
    await expect(page.getByTestId("proposing-run-link")).toHaveText("run #1");

    const proposedMeta = page
      .locator(".timeline-transition-by")
      .filter({ hasText: "on behalf of dana@example.com" });
    await expect(proposedMeta).toHaveCount(1);
    const [propScroll, propClient] = await proposedMeta.evaluate((e) => [
      e.scrollWidth,
      e.clientWidth,
    ]);
    expect(
      propScroll,
      `${vp.label}: Proposed attribution wraps inside its timeline item`,
    ).toBeLessThanOrEqual(propClient + 1);

    const auditMeta = page
      .locator(".timeline-transition-by")
      .filter({ hasText: /from \S+ to dana@example\.com/ });
    await expect(auditMeta).toHaveCount(1);
    const [auditScroll, auditClient] = await auditMeta.evaluate((e) => [
      e.scrollWidth,
      e.clientWidth,
    ]);
    expect(
      auditScroll,
      `${vp.label}: Owner changed audit wraps inside its timeline item`,
    ).toBeLessThanOrEqual(auditClient + 1);
  }

  // 2. Seeded imported task #12 shows automation proposer attribution.
  await page.setViewportSize(VIEWPORTS[0]);
  await page.goto("/tasks/12");
  await expect(
    page
      .locator(".timeline-transition-by")
      .filter({ hasText: /^by issue-import$/ }),
  ).toHaveCount(1);

  // 3. Create a task with an explicit owner via API, transfer ownership inline
  //    on the task detail page, verify the audit entry, and filter the list by
  //    owner and owner:me.
  const title = `E2E explicit owner ${Date.now()}`;
  const created = await request.post("/api/tasks", {
    data: {
      title,
      repo: "acme/widgets",
      owner: "alice@example.com",
    },
  });
  expect(created.ok()).toBe(true);
  const task = await created.json();
  expect(task.owner).toBe("alice@example.com");

  await page.goto(`/tasks/${task.id}`);
  await expect(page.getByTestId("task-owner")).toHaveText("alice@example.com");
  await page.getByRole("button", { name: "Change owner" }).click();
  await page
    .getByRole("textbox", { name: "New owner" })
    .fill("bob@example.com");
  await page.getByRole("button", { name: "Save", exact: true }).click();

  await expect(page.getByTestId("task-owner")).toHaveText("bob@example.com");
  await expect(
    page
      .locator(".timeline-transition-by")
      .filter({ hasText: "from alice@example.com to bob@example.com" }),
  ).toHaveCount(1);

  // List view shows chip-owner and filters cleanly by f=owner:any:bob@example.com
  // and f=owner:any:me.
  await page.goto("/?f=owner:any:bob@example.com");
  const bobRows = page.locator(".task-row");
  await expect(bobRows).toHaveCount(1);
  await expect(bobRows.first()).toContainText(title);
  await expect(bobRows.first().locator(".chip-owner")).toHaveText(
    "bob@example.com",
  );

  await page.goto("/?f=owner:any:me");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await expect(page.locator(".task-row", { hasText: title })).toHaveCount(0);
});
