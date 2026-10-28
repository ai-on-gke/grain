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

// A CL's presubmit badge sits beside its cl/N chip on the task view and
// on the task's inbox row, centred on the chip's line. The demo seeds a
// changelist task in each presubmit state (pkg/demo).
const SEEDS = [
  ["Port the retry budget to the depot", "failed"],
  ["Move the depot retry budget behind a flag", "passed"],
  ["Add a TAP project for the retry budget", "running"],
  ["Document the depot retry budget", "not_run"],
  ["Trim the retry budget's BUILD deps", "unknown"],
];

async function expectBesideOnOneLine(badge, chip) {
  const b = await badge.boundingBox();
  const c = await chip.boundingBox();
  expect(b && c).toBeTruthy();
  // Vertically centred on the chip, and after it.
  expect(
    Math.abs(b.y + b.height / 2 - (c.y + c.height / 2)),
  ).toBeLessThanOrEqual(2);
  expect(b.x).toBeGreaterThanOrEqual(c.x + c.width - 1);
}

for (const viewport of [
  { name: "desktop", width: 1280, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`The task view badges the primary CL's presubmit (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const tasks = await (await page.request.get("/api/tasks")).json();
    const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
    const task = list.find((t) => t.title === SEEDS[0][0]);
    expect(task?.presubmit?.state).toBe("failed");
    await page.goto(`/tasks/${task.id}`);
    const badge = page.getByTestId("presubmit-badge");
    await expect(badge).toHaveCount(1);
    await expect(badge).toHaveAttribute("data-state", "failed");
    await expect(badge).toContainText("presubmit failed: 2 checks");
    await expectBesideOnOneLine(badge, page.getByTestId("cl-chip").first());
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test(`Inbox rows badge each presubmit state (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    for (const [title, state] of SEEDS) {
      const row = page.locator("li.inbox-item").filter({ hasText: title });
      await expect(row).toHaveCount(1);
      const badge = row.getByTestId("presubmit-badge");
      await expect(badge).toHaveAttribute("data-state", state);
      const b = await badge.boundingBox();
      const r = await row.boundingBox();
      expect(b.x + b.width).toBeLessThanOrEqual(r.x + r.width + 1);
    }
  });
}
