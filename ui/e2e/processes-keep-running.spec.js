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

// Keep running (model.Template.KeepRunning, model.Schedule.KeepRunning)
// end to end, from docs/test-runs/2026-11-03-recurring-composed-ui.md:
// the API and a declared template's keep-running: could set it, but the
// template and schedule forms had no box for it and no preview said it.

// keepRunningRow is the composition preview's "Keep running" row in
// scope, the way template-priority.spec.js reads its Task priority row.
function keepRunningRow(scope) {
  return scope.locator("dt", { hasText: /^Keep running$/ }).locator("xpath=..");
}

test("a template's Keep running is set from the form, previewed and filed", async ({
  page,
}) => {
  const name = `walk-keep-running-${Date.now()}`;
  let id = null;
  try {
    await page.goto("/processes?kind=template");
    await page.getByRole("button", { name: "+ New template" }).click();
    await page.locator('input[name="name"]').fill(name);
    await page.locator('input[name="title"]').fill("Standing widget watch");
    await page.getByRole("checkbox", { name: "Keep running" }).check();
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname === "/api/templates",
    );
    await page.getByRole("button", { name: "Add template" }).click();
    const saved = await (await created).json();
    id = saved.id;
    expect(saved.keepRunning).toBe(true);

    // Opened again, the box says what was saved.
    await page.goto(`/processes/templates/${id}`);
    await expect(
      page.getByRole("checkbox", { name: "Keep running" }),
    ).toBeChecked();

    // Use…'s preview names it, as the template's own, and the task it
    // files carries it.
    await page.goto("/processes?kind=template");
    await page
      .locator(".process-row-template")
      .filter({ hasText: name })
      .getByRole("button", { name: /use/i })
      .click();
    const dialog = page.getByRole("dialog").last();
    const row = keepRunningRow(dialog);
    await expect(row.locator("dd .composition-value")).toHaveText("on");
    await expect(row).toContainText("from the template");
    await dialog.getByRole("button", { name: /create task/i }).click();
    await expect(page).toHaveURL(/\/tasks\/\d+/);
    const taskId = page.url().match(/\/tasks\/(\d+)/)[1];
    const task = await (await page.request.get(`/api/tasks/${taskId}`)).json();
    expect((task.task || task).keepRunning).toBe(true);
  } finally {
    if (id) await page.request.delete(`/api/templates/${id}`);
  }
});

test("a raw task schedule's Keep running is set from the form and previewed", async ({
  page,
}) => {
  const title = `Walk standing sweep ${Date.now()}`;
  let id = null;
  try {
    await page.goto("/processes?kind=schedule");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    await page.getByLabel("Title").fill(title);
    // No row until it is on: an off Keep running says nothing.
    await expect(keepRunningRow(page)).toHaveCount(0);
    await page.getByRole("checkbox", { name: "Keep running" }).check();
    const row = keepRunningRow(page);
    await expect(row.locator("dd .composition-value")).toHaveText("on");
    await expect(row).toContainText("from the schedule");
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname === "/api/schedules",
    );
    await page.getByRole("button", { name: "Add schedule" }).click();
    const saved = await (await created).json();
    id = saved.id;
    expect(saved.keepRunning).toBe(true);

    await page.goto(`/processes/schedules/${id}`);
    const box = page.getByRole("checkbox", { name: "Keep running" });
    await expect(box).toBeChecked();
    await box.uncheck();
    const patched = page.waitForResponse(
      (r) =>
        r.request().method() === "PATCH" &&
        new URL(r.url()).pathname === `/api/schedules/${id}`,
    );
    await page.getByRole("button", { name: "Save" }).click();
    expect((await (await patched).json()).keepRunning).toBeFalsy();
  } finally {
    if (id) await page.request.delete(`/api/schedules/${id}`);
  }
});
