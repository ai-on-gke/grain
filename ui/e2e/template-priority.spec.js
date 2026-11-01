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

// A template's own task priority (model.Template.Priority) end to end,
// from docs/test-runs/2026-11-01-recurring-composed-ui.md: set in the
// template form, opened again as itself, named in Use…'s preview, carried
// by the task it files, and cleared by emptying the field. Before this,
// the API and .grain/templates/*.yml could set a priority that the form
// neither showed nor changed, and that no preview mentioned.

test("a template's task priority is set, previewed, filed and cleared from the form", async ({
  page,
}) => {
  const name = `walk-priority-${Date.now()}`;
  let id = null;
  const bodies = [];
  page.on("request", (r) => {
    const path = new URL(r.url()).pathname;
    if (
      (r.method() === "POST" && path === "/api/templates") ||
      (r.method() === "PATCH" && path.startsWith("/api/templates/"))
    ) {
      bodies.push(JSON.parse(r.postData() || "{}"));
    }
  });
  const priorityField = () => page.getByLabel("Task priority");
  try {
    await page.goto("/processes?kind=template");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill(name);
    await page.locator('input[name="title"]').fill("Urgent widget check");
    await page
      .locator('select[name="repo"]')
      .first()
      .selectOption("acme/widgets");
    await priorityField().fill("8");
    await page.getByRole("button", { name: "Add template" }).click();
    await expect(
      page.getByRole("heading", { name: "New template" }),
    ).toHaveCount(0);
    expect(bodies.at(-1).priority).toBe(8);

    const list = await (await page.request.get("/api/templates")).json();
    const saved = list.find((t) => t.name === name);
    id = saved.id;
    expect(saved.priority).toBe(8);

    // Opened again, the form says what was saved.
    await page.goto(`/processes/templates/${id}`);
    await expect(priorityField()).toHaveValue("8");

    // Use…'s preview names it, as the template's own.
    await page.goto("/processes?kind=template");
    await page
      .locator(".process-row-template")
      .filter({ hasText: name })
      .getByRole("button", { name: /use/i })
      .click();
    const dialog = page.getByRole("dialog").last();
    const label = dialog.locator("dt", { hasText: /^Task priority$/ });
    await expect(label).toBeVisible();
    const row = label.locator("xpath=..");
    await expect(row.locator("dd")).toHaveText("8");
    await expect(row).toContainText("from the template");
    await dialog.getByRole("button", { name: /create task/i }).click();
    await expect(page).toHaveURL(/\/tasks\/\d+/);
    const taskId = page.url().match(/\/tasks\/(\d+)/)[1];
    const task = await (await page.request.get(`/api/tasks/${taskId}`)).json();
    expect((task.task || task).priority).toBe(8);

    // Emptied, the priority is cleared rather than left as it was.
    await page.goto(`/processes/templates/${id}`);
    await priorityField().fill("");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(priorityField()).toHaveCount(0);
    expect(bodies.at(-1).priority).toBe(0);
    const after = (
      await (await page.request.get("/api/templates")).json()
    ).find((t) => t.id === id);
    expect(after.priority).toBeUndefined();
  } finally {
    if (id) await page.request.delete(`/api/templates/${id}`);
  }
});
