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

// Regression coverage from the 2026-11-19 task intake, inbox & lifecycle
// walkthrough (docs/test-runs/2026-11-19-task-intake-inbox-lifecycle.md):
//   1. Filing a task when capability policy drops a default grant names the
//      dropped grant in the filing toast and stays within the viewport.
//   2. A task's externalRef renders in the /next/task/<id> header and
//      Details attributes as a link into /next/tasks?tab=all&externalRef=<ref>,
//      which narrows Tasks (with a clearable ref chip) and matches in search.
//   3. A queued task blocked on an open dependency shows "Blocked" (blast/
//      warning tone) as its own state oval on Tasks Backlog, All and the task
//      page header, without horizontal overflow.

test("externalRef displays on task detail, links into filtered Tasks, and blocked shows its own Blocked oval @phone @dark", async ({
  page,
}) => {
  // Demo task #11 is queued, blocked on #8, and carries externalRef "CA-204".
  // Demo task #8 is running and also carries externalRef "CA-204".
  await page.goto("tasks?tab=all");
  const row11 = page.getByTestId("row-11");
  const ovalAll = row11.locator(".status-pill").first();
  await expect(ovalAll).toHaveText("Blocked");
  await expect(ovalAll).toHaveClass(/\bblast\b/);

  // Backlog tab also shows Blocked as its own state oval.
  await page.goto("tasks");
  const ovalBacklog = page
    .getByTestId("row-11")
    .locator(".status-pill")
    .first();
  await expect(ovalBacklog).toHaveText("Blocked");
  await expect(ovalBacklog).toHaveClass(/\bblast\b/);
  const ovalBox = await ovalBacklog.boundingBox();
  expect(ovalBox).not.toBeNull();
  expect(ovalBox!.width).toBeGreaterThan(50);
  expect(ovalBox!.height).toBeGreaterThanOrEqual(20);

  // Task detail header shows Blocked oval and clickable ref link, and Details
  // attributes also link externalRef.
  await page.goto("task/11?tab=details");
  const headOval = page.getByTestId("task-state");
  await expect(headOval).toHaveText("Blocked");
  await expect(headOval).toHaveClass(/\bblast\b/);

  const headRef = page.getByTestId("task-external-ref");
  await expect(headRef).toHaveText("ref CA-204");
  await expect(page.getByTestId("task-attr-external-ref")).toHaveText("CA-204");

  // Layout check: zero horizontal overflow at both desktop and phone widths.
  const detailOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(detailOverflow).toBeLessThanOrEqual(0);

  // Following the ref link narrows /next/tasks to tasks carrying CA-204.
  await headRef.click();
  await expect(page).toHaveURL(/\/next\/tasks\?.*externalRef=CA-204/);
  const refChip = page.getByTestId("tasks-ref-chip");
  await expect(refChip).toContainText("ref: CA-204");
  await expect(page.getByTestId("row-8")).toBeVisible();
  await expect(page.getByTestId("row-11")).toBeVisible();
  await expect(page.getByTestId("row-2")).toHaveCount(0);

  const tasksOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(tasksOverflow).toBeLessThanOrEqual(0);

  // Clearing the ref chip restores the unfiltered All list.
  await refChip.click();
  await expect(refChip).toHaveCount(0);
  await expect(page.getByTestId("row-2")).toBeVisible();

  // Free-text filter box also matches externalRef.
  const search = page.getByTestId("view-filter");
  await search.fill("CA-204");
  await expect(page.getByTestId("row-8")).toBeVisible();
  await expect(page.getByTestId("row-11")).toBeVisible();
  await expect(page.getByTestId("row-2")).toHaveCount(0);
  await search.fill("");
});

test("filing a task when capability policy drops a default grant names it in the snackbar @phone @dark", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  let createdId = "";
  try {
    const putRes = await request.put("/api/capability-policy", {
      data: {
        policy: {
          ...(before?.policy ?? {}),
          rules: [
            ...((before?.policy?.rules as unknown[]) ?? []),
            {
              capability: "https://grain.dev/capabilities/github#*",
              deny: true,
            },
          ],
        },
      },
    });
    expect(putRes.status()).toBe(200);

    await page.goto("tasks");
    const newBtn = page
      .locator('[data-testid="rail-new-task"], [data-testid="phone-new-task"]')
      .locator("visible=true")
      .first();
    await newBtn.click();
    const sheet = page.getByTestId("new-task-sheet");
    await expect(sheet).toBeVisible();

    await sheet
      .getByTestId("nt-title")
      .fill("Policy dropped default toast e2e");
    const postPromise = page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
    );
    await sheet.getByTestId("new-task-submit").click();
    const postRes = await postPromise;
    expect(postRes.status()).toBe(201);
    const created = await postRes.json();
    createdId = created.id;
    expect(created.droppedDefaults?.length ?? 0).toBeGreaterThan(0);

    const snack = page.getByTestId("snackbar");
    await expect(snack).toBeVisible();
    await expect(snack).toContainText(`#${createdId}`);
    await expect(snack).toContainText("github#read-issues");

    // Confirm the snackbar with the long canonical grant URL stays within the viewport.
    const snackBox = await snack.boundingBox();
    const vp = page.viewportSize()!;
    expect(snackBox).not.toBeNull();
    expect(snackBox!.x).toBeGreaterThanOrEqual(0);
    expect(snackBox!.x + snackBox!.width).toBeLessThanOrEqual(vp.width + 1);
  } finally {
    if (createdId) {
      await request.post(`/api/tasks/${createdId}/close`, { data: {} });
    }
    await request.put("/api/capability-policy", {
      data: before ?? { policy: {} },
    });
  }
});
