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

// The fields a deployment declares for itself (model.CustomField), from
// the form that files one through to the toolbar that narrows a list by
// it -- against a real server, which is the only place any of it can be
// seen whole (grain/task-274).
//
// Everything here rests on `grain demo` declaring fields at all:
// seedDemo (pkg/demo/demo.go) puts six of them in the throwaway store
// before it seeds a task, one per control the form renders, and gives
// several of the seeded tasks values for them --
// TestSeedDemoDeclaresAFieldOfEveryKind asserts on that directly. Until
// it did, a demo carried no `customFields` on GET /api/config at all,
// and every input, chip, field row and `custom.<name>` menu below was
// invisible to this suite whether it worked or not.
//
// The component tests next door (CustomFields.test.jsx,
// NarrowingBar.test.jsx and the rest) drive these same components in
// jsdom against declarations written into the test. What only this can
// answer for is the join: that what the server actually sends as a
// declaration is what those components render, that a value typed into
// a real browser survives POST /api/tasks and comes back on the row,
// and that the namespaced attribute id reaches the URL. See
// tasks.spec.js's doc comment for why the suite exists.

// The seeded task with a priority of its own that is not "high", so
// that narrowing to "high" below is a question with a visible answer
// rather than one every row happens to pass.
const MEDIUM = "Bump the Go toolchain to 1.24";

test("files a task with its fields set through the New task form", async ({
  page,
}) => {
  await page.goto("/");
  const title = `E2E fields ${Date.now()}`;

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");

  // The block only a deployment with declarations has, and the declared
  // default already in the picker before anybody has chosen anything
  // (model.CustomField.Default -- a default is a form's, never a value
  // the store writes for a task nobody filled in).
  await expect(page.getByText("Fields", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Priority")).toHaveText("medium");

  await page.getByLabel("Priority").click();
  await page.getByRole("option", { name: "high" }).click();
  // The field that holds a set, filled from its declared options: two
  // chips in one box, which is the input no single-valued field has.
  // The list closes on each pick, so it is opened once per value.
  for (const area of ["backend", "docs"]) {
    await page.getByLabel("Area").click();
    await page.getByRole("option", { name: area }).click();
  }
  await page.getByLabel("Requested by").fill("Northwind");
  await page.getByLabel("Estimate (days)").fill("2");
  await page.getByLabel("Customer-facing").click();
  await page.getByRole("option", { name: "No", exact: true }).click();

  await page.getByRole("button", { name: "Create task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);

  // On the row: the one field this deployment flagged onto every row
  // (ShowOnRow), as the chip that says which field it is and what this
  // task answered.
  const row = page.locator(".task-row", { hasText: title });
  await expect(row).toBeVisible();
  await expect(row.locator(".chip-field")).toHaveText("Priorityhigh");
  // ...and only that one: a row shows what the declaration flagged, not
  // every field the task carries, however many were filled in above.
  await expect(row.locator(".chip-field")).toHaveCount(1);

  // In the task's own pane, all of them -- each under its declared
  // label, a multi-valued field written as the one line its values make.
  await row.click();
  await expect(
    page.locator(".detail-header h2", { hasText: title }),
  ).toBeVisible();
  const fields = page.locator(".declared-row");
  await expect(fields.filter({ hasText: "Priority" })).toContainText("high");
  await expect(fields.filter({ hasText: "Area" })).toContainText(
    "backend, docs",
  );
  await expect(fields.filter({ hasText: "Requested by" })).toContainText(
    "Northwind",
  );
  await expect(fields.filter({ hasText: "Estimate (days)" })).toContainText(
    "2",
  );
  // Written as a person would say it, as the editor does
  // (CustomFields.jsx's displayValue).
  await expect(fields.filter({ hasText: "Customer-facing" })).toContainText(
    "No",
  );
  // The date was left alone, and a field nobody filled in gets no row at
  // all rather than a row saying nothing ("nobody said" is the ordinary
  // answer -- model.CustomField.NormalizeValue).
  await expect(fields.filter({ hasText: "Due date" })).toHaveCount(0);
});

// The other direction through the task pane: values already on a task
// can be changed there, and the next task about the same work can start
// with the changed values. This crosses PATCH /api/tasks, the refreshed
// detail row, newTaskPath's repeated query parameters, and both the
// in-app and cold-load versions of /new -- none of which the filing test
// above reaches.
test("edits a task's fields and carries them into another task", async ({
  page,
}) => {
  const title = `E2E edit fields ${Date.now()}`;
  const seeded = await page.request.post("/api/tasks", {
    data: {
      title,
      customFields: {
        priority: "low",
        area: ["backend", "docs"],
        "requested-by": "Northwind",
      },
    },
  });
  expect(seeded.ok(), `POST /api/tasks: ${seeded.status()}`).toBe(true);
  const task = await seeded.json();

  await page.goto(`/tasks/${task.id}`);
  await expect(
    page.locator(".detail-header h2", { hasText: title }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Edit fields" }).click();
  await expect(page.getByLabel("Priority")).toHaveText("low");
  await expect(page.getByLabel("Requested by")).toHaveValue("Northwind");

  await page.getByLabel("Priority").click();
  await page.getByRole("option", { name: "high" }).click();
  await page.getByLabel("Requested by").fill("Contoso");
  await page.getByLabel("Due date").fill("2026-12-15");
  await page.getByRole("button", { name: "Save fields" }).click();

  const rows = page.locator(".declared-row");
  await expect(rows.filter({ hasText: "Priority" })).toContainText("high");
  await expect(rows.filter({ hasText: "Requested by" })).toContainText(
    "Contoso",
  );
  await expect(rows.filter({ hasText: "Due date" })).toContainText(
    "2026-12-15",
  );

  const next = page.getByRole("link", {
    name: "File a task with these fields",
  });
  const href = await next.getAttribute("href");
  expect(href).toContain("field.priority=high");
  expect(href).toContain("field.area=backend&field.area=docs");
  expect(href).toContain("field.requested-by=Contoso");
  expect(href).toContain("field.due-date=2026-12-15");

  await next.click();
  await expect(page).toHaveURL(href);
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel("Priority")).toHaveText("high");
  await expect(page.getByLabel("Requested by")).toHaveValue("Contoso");
  await expect(page.getByLabel("Due date")).toHaveValue("2026-12-15");
  const area = page
    .locator(".MuiAutocomplete-root")
    .filter({ has: page.getByLabel("Area") });
  await expect(area.getByText("backend", { exact: true })).toBeVisible();
  await expect(area.getByText("docs", { exact: true })).toBeVisible();

  // The href is the contract, not merely an in-memory shortcut: loading
  // the same address from scratch produces the same form values.
  await page.goto(href);
  await expect(page.getByLabel("Priority")).toHaveText("high");
  await expect(page.getByLabel("Requested by")).toHaveValue("Contoso");
  await expect(page.getByLabel("Due date")).toHaveValue("2026-12-15");
  await expect(area.getByText("backend", { exact: true })).toBeVisible();
  await expect(area.getByText("docs", { exact: true })).toBeVisible();
});

// Bulk edit has to send each task's complete field map even though the
// dialog changes only the ticked field: PATCH reads customFields as the
// whole declared answer. Driving two different maps through the real
// endpoint verifies that the selected value changes everywhere without
// erasing the unticked values that differ between tasks.
test("bulk edits one field without disturbing the others", async ({ page }) => {
  const stamp = Date.now();
  const seeded = [];
  for (const [n, requestedBy] of ["Northwind", "Contoso"].entries()) {
    const response = await page.request.post("/api/tasks", {
      data: {
        title: `E2E bulk fields ${n + 1} ${stamp}`,
        customFields: {
          priority: n === 0 ? "low" : "medium",
          "requested-by": requestedBy,
        },
      },
    });
    expect(response.ok(), `POST /api/tasks: ${response.status()}`).toBe(true);
    seeded.push(await response.json());
  }

  await page.goto("/");
  for (const task of seeded) {
    await page
      .locator(".task-row", { hasText: task.title })
      .getByRole("checkbox")
      .check();
  }

  await page.getByRole("button", { name: "Bulk edit…" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox", { name: /^Priority/ }).check();
  await dialog.getByRole("combobox", { name: "Priority" }).click();
  await page.getByRole("option", { name: "high" }).click();
  await dialog.getByRole("button", { name: "Apply to 2 tasks" }).click();
  await expect(page.getByText("2 selected")).toHaveCount(0);

  for (const [n, task] of seeded.entries()) {
    const row = page.locator(".task-row", { hasText: task.title });
    await expect(row.locator(".chip-field")).toHaveText("Priorityhigh");

    const response = await page.request.get(`/api/tasks/${task.id}`);
    expect(
      response.ok(),
      `GET /api/tasks/${task.id}: ${response.status()}`,
    ).toBe(true);
    const updated = await response.json();
    expect(updated.customFields).toMatchObject({
      priority: "high",
      "requested-by": n === 0 ? "Northwind" : "Contoso",
    });
  }
});

test("narrows the task list by a declared field", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".task-row", { hasText: MEDIUM })).toBeVisible();

  await page
    .getByTitle("Add and edit the conditions this list is narrowed by")
    .click();
  // "Filter by…" on a list narrowed by nothing, "Add filter" once it
  // holds a condition -- which is what the deployment's own default
  // leaves it holding.
  await page.getByRole("button", { name: /^(Filter by…|Add filter)$/ }).click();
  // The declared field in the same menu as the built-in attributes,
  // under its label rather than its namespaced id.
  await page.getByRole("menuitem", { name: "Priority" }).click();
  await expect(page.getByRole("menu")).not.toBeVisible();

  // The condition the editor just added is the last row of it.
  await page
    .getByLabel(/^Value for filter \d+$/)
    .last()
    .click();
  await page.getByRole("option", { name: "high" }).click();
  await page.keyboard.press("Escape");

  // Only the tasks that answered "high" are left, and the seeded
  // "medium" one is gone -- which is the whole of what a filter does.
  await expect(
    page.locator(".task-row", { hasText: "Fix flaky retry test" }),
  ).toBeVisible();
  await expect(page.locator(".task-row", { hasText: MEDIUM })).toHaveCount(0);

  // The question is in the URL under the namespaced id
  // (taskAttributes.js's CUSTOM_PREFIX), so the link says what the list
  // is showing...
  await expect(page).toHaveURL(/f=custom\.priority:any:high/);
  // ...and says it to a browser that has never had this list open.
  await page.goto("/?f=custom.priority:any:high");
  await expect(
    page.locator(".task-row", { hasText: "Fix flaky retry test" }),
  ).toBeVisible();
  await expect(page.locator(".task-row", { hasText: MEDIUM })).toHaveCount(0);
});

// The row a deployment that has *retired* a field is left with: the
// declaration is gone from the state repository, the value stays on the
// task that recorded it, and the pane shows it under its bare name
// rather than dropping it (CustomFields.jsx's fieldRows). seedDemo
// declares one field, records a value for it and deletes the
// declaration again, so this has something to read.
test("shows a value whose declaration has been retired", async ({ page }) => {
  await page.goto("/");
  await page
    .locator(".task-row", { hasText: "Rotate the Gemini signing key" })
    .click();
  await expect(page.locator(".detail-header h2")).toBeVisible();

  const retired = page.locator(".declared-row", { hasText: "sprint" });
  await expect(retired.locator(".declared-value")).toHaveText("2026-Q2");
  // No form offers it, because the form renders declarations and this is
  // not one any more.
  await page.getByRole("button", { name: "Edit fields" }).click();
  await expect(page.getByLabel("sprint")).toHaveCount(0);
  await expect(page.getByLabel("Priority")).toBeVisible();
});
