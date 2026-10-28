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

// A changelist task's CLs are chips linking to
// http://cl/N, on the task view beside the pull request and on the
// task's inbox row. The demo seeds one finished changelist task with two
// CLs (pkg/demo, "Port the retry budget to the depot").
const TITLE = "Port the retry budget to the depot";

async function changelistTaskID(page) {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
  const task = list.find((t) => t.title === TITLE);
  expect(task, "the demo's changelist task").toBeTruthy();
  expect(task.changelists).toEqual(["cl/612400001", "cl/612400017"]);
  return task.id;
}

// The chip's label sits centred in it, and the chip inside its row.
async function expectCentredIn(chip, container) {
  const c = await chip.boundingBox();
  const box = await container.boundingBox();
  expect(c && box).toBeTruthy();
  expect(c.x).toBeGreaterThanOrEqual(box.x - 1);
  expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 1);
  expect(c.y).toBeGreaterThanOrEqual(box.y - 1);
  expect(c.y + c.height).toBeLessThanOrEqual(box.y + box.height + 1);
  const label = chip.locator(".MuiChip-label");
  const l = await label.boundingBox();
  expect(
    Math.abs(l.y + l.height / 2 - (c.y + c.height / 2)),
  ).toBeLessThanOrEqual(1.5);
}

for (const viewport of [
  { name: "desktop", width: 1280, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`The CL chip links to its go link on the task view (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const id = await changelistTaskID(page);
    await page.goto(`/tasks/${id}`);
    const chips = page.getByTestId("cl-chip");
    await expect(chips).toHaveCount(2);
    await expect(chips.nth(0)).toHaveAttribute("href", "http://cl/612400001");
    await expect(chips.nth(0)).toHaveAttribute("data-primary", "true");
    await expect(chips.nth(1)).toHaveAttribute("href", "http://cl/612400017");
    const freshness = page.locator(".freshness").filter({ has: chips.nth(0) });
    for (const i of [0, 1]) {
      await expectCentredIn(chips.nth(i), freshness);
    }
    // Neither chip pushes the page sideways.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test(`The inbox row shows the primary CL chip (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    const row = page.locator("li.inbox-item").filter({ hasText: TITLE });
    await expect(row).toHaveCount(1);
    const chip = row.getByTestId("cl-chip");
    await expect(chip).toHaveCount(1);
    await expect(chip).toHaveAttribute("href", "http://cl/612400001");
    await expectCentredIn(chip, row);
  });
}
