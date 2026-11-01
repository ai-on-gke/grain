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

// The task list's Bulk edit… offered every field but the numeric Task
// priority, so reprioritising a batch took one Edit per task (docs/
// test-runs/2026-11-01-task-intake-inbox-lifecycle.md, O3). It is a row
// of its own now, refusing what the task Edit form refuses. Two of the
// shared demo's queued tasks are reprioritised here, and put back.

async function listTasks(page) {
  const body = await (await page.request.get("/api/tasks")).json();
  return Array.isArray(body) ? body : body.tasks || [];
}

test("Bulk edit sets one Task priority on every selected task", async ({
  page,
}) => {
  const queued = (await listTasks(page)).filter((t) => t.state === "queued");
  expect(queued.length, "the demo's queued tasks").toBeGreaterThanOrEqual(2);
  const picked = queued.slice(0, 2);
  const before = picked.map((t) => t.priority ?? 5);

  try {
    // Disagree first, so the row has a "mixed" to say.
    await page.request.patch(`/api/tasks/${picked[0].id}`, {
      data: { priority: 3 },
    });
    await page.request.patch(`/api/tasks/${picked[1].id}`, {
      data: { priority: 4 },
    });

    await page.goto("/?state=queued");
    for (const t of picked) {
      await page
        .locator(".task-row", { hasText: t.title })
        .first()
        .getByRole("checkbox")
        .check();
    }
    await expect(page.getByText("2 selected")).toBeVisible();
    await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
    const dialog = page.getByRole("dialog");

    await dialog
      .getByRole("checkbox", { name: /^Task priority \(mixed across/ })
      .check();
    const box = dialog.getByRole("spinbutton", { name: "Task priority" });
    const apply = dialog.getByRole("button", { name: "Apply to 2 tasks" });
    await expect(box).toHaveValue("");
    await expect(apply).toBeDisabled();

    for (const typed of ["0", "2.5"]) {
      await box.fill(typed);
      await expect(box).toHaveAttribute("aria-invalid", "true");
      await expect(apply).toBeDisabled();
      const hint = dialog.getByText(/a whole number, 1 or more/);
      await expect(hint).toBeVisible();
      // The reason sits under the box it is about, inside the dialog.
      const field = await box.boundingBox();
      const under = await hint.boundingBox();
      const pane = await dialog.boundingBox();
      expect(under.y).toBeGreaterThanOrEqual(field.y + field.height - 0.5);
      expect(Math.abs(under.x - field.x)).toBeLessThan(32);
      expect(field.x + field.width).toBeLessThanOrEqual(pane.x + pane.width);
    }

    await box.fill("9");
    await expect(box).toHaveAttribute("aria-invalid", "false");
    await apply.click();
    await expect(dialog).toHaveCount(0);

    await expect
      .poll(async () => {
        const all = await listTasks(page);
        return picked.map((p) => all.find((t) => t.id === p.id)?.priority);
      })
      .toEqual([9, 9]);
    // Nothing else on the tasks was touched.
    const after = await listTasks(page);
    for (const p of picked) {
      const t = after.find((x) => x.id === p.id);
      expect(t.repo).toBe(p.repo);
      expect(t.base || "").toBe(p.base || "");
      expect(t.model || "").toBe(p.model || "");
    }
  } finally {
    for (const [i, t] of picked.entries()) {
      await page.request.patch(`/api/tasks/${t.id}`, {
        data: { priority: before[i] },
      });
    }
  }
});
