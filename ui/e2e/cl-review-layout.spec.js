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

// A CL's review badge sits beside its presubmit badge, after the cl/N
// chip, on the task view and on the task's inbox row; the task view's
// actions carry Mail and Publish replies. The demo seeds a changelist
// task in each review state (pkg/demo) and registers no provider, so both
// buttons are disabled with the reason.
const SEEDS = [
  [
    "Port the retry budget to the depot",
    "changes_requested",
    "changes requested",
  ],
  ["Move the depot retry budget behind a flag", "approved", "LGTM alice"],
  ["Add a TAP project for the retry budget", "in_review", "in review"],
  ["Document the depot retry budget", "draft", "needs mailing"],
  [
    "Trim the retry budget's BUILD deps",
    "needs_approval",
    "needs approval: owners, readability",
  ],
];

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
  test(`The task view badges the primary CL's review (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const tasks = await (await page.request.get("/api/tasks")).json();
    const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
    const task = list.find((t) => t.title === SEEDS[3][0]);
    expect(task?.clReview?.needsMailing).toBe(true);
    await page.goto(`/tasks/${task.id}`);
    const badge = page.getByTestId("cl-review-badge");
    await expect(badge).toHaveCount(1);
    await expect(badge).toHaveAttribute("data-state", "draft");
    await expect(badge).toContainText("needs mailing");
    const b = await badge.boundingBox();
    const p = await page.getByTestId("presubmit-badge").boundingBox();
    // After the presubmit badge: on its line, or wrapped below it.
    if (Math.abs(b.y - p.y) <= 2) {
      expect(b.x).toBeGreaterThanOrEqual(p.x + p.width - 1);
      expect(Math.abs(b.height - p.height)).toBeLessThanOrEqual(1);
    } else {
      expect(b.y).toBeGreaterThan(p.y);
    }
    expect(b.x + b.width).toBeLessThanOrEqual(viewport.width);

    // No provider on the demo: both buttons say why they do not apply.
    const mail = page.getByTestId("cl-mail");
    const publish = page.getByTestId("cl-publish");
    await expect(mail).toBeDisabled();
    await expect(publish).toBeDisabled();
    const m = await mail.boundingBox();
    const q = await publish.boundingBox();
    expect(Math.abs(m.y - q.y)).toBeLessThanOrEqual(1);
    expect(q.x).toBeGreaterThan(m.x + m.width - 1);
    await noOverflow(page);
  });

  test(`Inbox rows badge each review state (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    for (const [title, state, label] of SEEDS) {
      const row = page.locator("li.inbox-item").filter({ hasText: title });
      await expect(row).toHaveCount(1);
      const badge = row.getByTestId("cl-review-badge");
      await expect(badge).toHaveAttribute("data-state", state);
      await expect(badge).toContainText(label);
      const b = await badge.boundingBox();
      const r = await row.boundingBox();
      expect(b.x + b.width).toBeLessThanOrEqual(r.x + r.width + 1);
    }
    await noOverflow(page);
  });
}
