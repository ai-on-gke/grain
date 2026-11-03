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

// A CL's Submit and Sync (changelists.md §6-§7). The demo seeds an
// approved, passing CL (submittable), one whose presubmit failed and
// reviewer asked for changes (not), and an approved one behind head;
// with no edition's provider it registers its own, which answers in
// process.
const SUBMITTABLE = "Move the depot retry budget behind a flag";
const BLOCKED = "Port the retry budget to the depot";
const BEHIND = "Sync the depot retry budget with head";

async function taskTitled(page, title) {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
  return list.find((t) => t.title === title);
}

async function noOverflow(page) {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

for (const viewport of [
  { name: "desktop", width: 1280, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`An unsubmittable CL's Submit is disabled with its reasons (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const task = await taskTitled(page, BLOCKED);
    expect(task?.clSubmit?.canSubmit).toBe(false);
    await page.goto(`/tasks/${task.id}`);
    const submit = page.getByTestId("cl-submit");
    await expect(submit).toBeDisabled();
    const reasons = page.getByTestId("cl-submit-reasons");
    await expect(reasons).toContainText("review is not approved");
    await expect(reasons).toContainText("required presubmit has not passed");
    await expect(page.getByTestId("cl-sync")).toBeEnabled();
    // The reasons sit under the buttons, inside the page.
    const s = await submit.boundingBox();
    const r = await reasons.boundingBox();
    expect(r.y).toBeGreaterThanOrEqual(s.y + s.height - 1);
    expect(r.x + r.width).toBeLessThanOrEqual(viewport.width);
    await noOverflow(page);
  });

  test(`A CL behind head is badged and offers Sync (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const task = await taskTitled(page, BEHIND);
    expect(task?.clSubmit?.behind).toBe(true);
    await page.goto(`/tasks/${task.id}`);
    const badge = page.getByTestId("cl-behind-badge").first();
    await expect(badge).toContainText("behind head");
    await expect(page.getByTestId("cl-submit")).toBeDisabled();
    await expect(page.getByTestId("cl-submit-reasons")).toContainText(
      "behind head",
    );
    const sync = page.getByTestId("cl-sync");
    await expect(sync).toBeEnabled();
    const b = await badge.boundingBox();
    expect(b.x + b.width).toBeLessThanOrEqual(viewport.width);
    await noOverflow(page);
  });
}

test("The inbox offers Submit inline only on a submittable CL", async ({
  page,
}) => {
  await page.goto("/inbox");
  const submittable = page.locator(".inbox-row, li, [data-task-id]", {
    hasText: SUBMITTABLE,
  });
  await expect(
    submittable.first().getByRole("button", { name: "Submit", exact: true }),
  ).toBeVisible();
  const blocked = page
    .locator(".inbox-row, li, [data-task-id]", { hasText: BLOCKED })
    .first();
  await expect(blocked).toContainText("Not submittable yet");
  await expect(
    blocked.getByRole("button", { name: "Submit", exact: true }),
  ).toHaveCount(0);
});

// Sync is pressed on the behind CL only, which no other spec reads;
// Submit's outcomes are pinned by pkg/ui's tests rather than by closing
// a demo task other specs look at.
test("Sync answers through the provider", async ({ page }) => {
  const behind = await taskTitled(page, BEHIND);
  await page.goto(`/tasks/${behind.id}`);
  await page.getByTestId("cl-sync").click();
  await expect(page.getByText("synced to head (demo)").first()).toBeVisible();
  // The sync made a new snapshot up to date with head: the badge goes,
  // and Submit now waits on that snapshot's presubmit instead.
  await expect(page.getByTestId("cl-behind-badge")).toHaveCount(0);
  await expect(page.getByTestId("cl-submit-reasons")).toContainText(
    "snapshot 3 (not run)",
  );
  await expect(page.getByTestId("cl-submit-reasons")).not.toContainText(
    "behind head",
  );
});
