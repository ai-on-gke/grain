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

// Each task's liveness sub-case on its row (ui.Task.Reason, orchestrator's
// taskReasonOf), the merge queue's own beside its phrase
// (ui.Task.MergeQueueReason), and the sub-case's control on a row grain
// has flagged as stuck -- in the list and in the inbox's stalled group.
//
// `grain demo` wires the sub-cases but no stall deadlines (ui.Config.Stall
// is nil there, and its seeded tasks are minutes old), so the two stuck
// rows are made by adding a stall note to the real server's list answer.
// Everything else on the page -- each task's sub-case, the merge-queue
// row's sub-case off the demo's seeded batch -- is the real server's.
//
// REASON_SHOTS, when set, is a directory to save the rows' screenshots to,
// which is how this change's own screenshots were taken.

const STUCK_QUEUED = "Fix flaky retry test";
const STUCK_RUNNING = "Bump the Go toolchain to 1.24";
const shots = process.env.REASON_SHOTS || "";

async function withStalls(page) {
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const response = await route.fetch({
      headers: { ...route.request().headers(), "if-none-match": "" },
    });
    if (response.status() !== 200) return route.fulfill({ response });
    const tasks = await response.json();
    for (const t of tasks) {
      if (t.title === STUCK_QUEUED || t.title === STUCK_RUNNING) {
        t.stall =
          "this has stood here for more than 3d, which is longer than it should take";
      }
    }
    // No ETag on the altered answer, so every poll reads it whole.
    const headers = { ...response.headers() };
    delete headers.etag;
    await route.fulfill({ status: 200, headers, json: tasks });
  });
}

function box(locator) {
  return locator.boundingBox();
}

test("a task row says its sub-case, and a stuck one offers the control that ends it", async ({
  page,
}) => {
  await withStalls(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");

  // Every open row names its sub-case, with which of the three
  // conditions it is in on hover.
  const queued = page.locator(".task-row", { hasText: STUCK_QUEUED });
  const chip = queued.locator(".task-reason");
  await expect(chip).toHaveText("ready");
  await expect(chip).toHaveAttribute(
    "title",
    /queued\/ready\nDriven by reconcileDispatch/,
  );
  await expect(
    page
      .locator(".task-row", { hasText: STUCK_RUNNING })
      .locator(".task-reason"),
  ).toHaveText("live run");

  // The stuck queued row offers Run now, after the chip and inside the
  // row, centred on the chip it belongs to.
  const control = queued.locator(".task-reason-control");
  await expect(control).toHaveText("Run now");
  const [row, chipBox, controlBox] = await Promise.all([
    box(queued),
    box(chip),
    box(control),
  ]);
  expect(controlBox.x).toBeGreaterThanOrEqual(chipBox.x + chipBox.width);
  expect(controlBox.x + controlBox.width).toBeLessThanOrEqual(
    row.x + row.width + 1,
  );
  expect(
    Math.abs(
      controlBox.y + controlBox.height / 2 - (chipBox.y + chipBox.height / 2),
    ),
  ).toBeLessThanOrEqual(2);
  // A row that is not stuck offers no control.
  await expect(
    page
      .locator(".task-row", { hasText: "Add a dark mode toggle" })
      .locator(".task-reason-control"),
  ).toHaveCount(0);
  // The stuck running row's control is Cancel.
  await expect(
    page
      .locator(".task-row", { hasText: STUCK_RUNNING })
      .locator(".task-reason-control"),
  ).toHaveText("Cancel");

  // The merge-queue row says its own sub-case beside the queue's phrase.
  const batched = page
    .locator(".task-activity-reason", { hasText: "batch run" })
    .first();
  await expect(batched).toBeVisible();
  await expect(batched.locator("..")).toHaveAttribute(
    "title",
    /batching\/batch_run\nDriven by /,
  );

  if (shots) {
    await queued.screenshot({ path: `${shots}/row-stuck-queued.png` });
    await page
      .locator(".task-row", { has: page.locator(".task-activity-reason") })
      .first()
      .screenshot({ path: `${shots}/row-merge-queue.png` });
    await page.screenshot({ path: `${shots}/list-desktop.png` });
  }
});

test("the inbox's stalled group offers each stuck task its sub-case's control, once", async ({
  page,
}) => {
  await withStalls(page);
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    const group = page.locator(".inbox-group", {
      hasText: "Look at a stuck task",
    });
    const queued = group.locator(".inbox-item", { hasText: STUCK_QUEUED });
    await expect(queued.getByRole("button", { name: "Run now" })).toHaveCount(
      1,
    );
    await expect(queued.locator(".task-reason")).toHaveText("ready");
    const running = group.locator(".inbox-item", { hasText: STUCK_RUNNING });
    await expect(running.getByRole("button", { name: "Cancel" })).toHaveCount(
      1,
    );

    // The control sits in the row's action slot, beside Open task.
    const actions = queued.locator(".inbox-actions");
    const [slot, runNow] = await Promise.all([
      box(actions),
      box(queued.getByRole("button", { name: "Run now" })),
    ]);
    expect(runNow.x).toBeGreaterThanOrEqual(slot.x - 1);
    expect(runNow.x + runNow.width).toBeLessThanOrEqual(
      slot.x + slot.width + 1,
    );

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
    if (shots) {
      await group.screenshot({
        path: `${shots}/inbox-stalled-${viewport.width < 600 ? "phone" : "desktop"}.png`,
      });
    }
  }
});
