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

// The grain API half of the task pane's Grants picker (GrantsPicker.jsx)
// over grain demo's real /api/config, whose capability rows carry the
// agent API's presets and scopes (ui.APICapabilities, docs/agent-api.md).
// They are one "Add grant…" menu with the capability server methods,
// grouped by kind -- presets first, then scopes by area -- and every
// grant held is one section in one list (design/design/unified-grants.design.md).

async function fileTask(request, capabilities) {
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E api grants ${Date.now()}`,
      repo: "acme/widgets",
      capabilities,
    },
  });
  expect(created.ok()).toBeTruthy();
  return created.json();
}

async function openMenu(page, id) {
  const grants = page.getByTestId(`task-${id}-grants`);
  await grants.getByRole("button", { name: "Add grant…" }).click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await expect(menu).toBeVisible();
  return { grants, menu };
}

test("lists the API presets before every scope, with what each preset grants", async ({
  page,
  request,
}) => {
  const task = await fileTask(request, ["api:ci:read"]);
  await page.goto(`/tasks/${task.id}`);
  const { grants, menu } = await openMenu(page, task.id);
  await expect(grants.locator('[data-grant="api:ci:read"]')).toContainText(
    "api:ci:read",
  );

  const kinds = await menu
    .locator(".api-grant-row")
    .evaluateAll((rows) => rows.map((r) => r.dataset.apiKind));
  expect(kinds.length).toBeGreaterThan(10);
  const firstScope = kinds.indexOf("scope");
  expect(firstScope).toBeGreaterThan(0);
  // Every preset comes before the first scope, and none after it.
  expect(kinds.slice(0, firstScope).every((k) => k === "preset")).toBe(true);
  expect(kinds.slice(firstScope).every((k) => k === "scope")).toBe(true);
  // And on the page, not only in the DOM.
  const lastPreset = await menu
    .locator('.api-grant-row[data-api-kind="preset"]')
    .last()
    .boundingBox();
  const scope = await menu
    .locator('.api-grant-row[data-api-kind="scope"]')
    .first()
    .boundingBox();
  expect(lastPreset.y + lastPreset.height).toBeLessThanOrEqual(scope.y);

  // Scopes are grouped by area, in the vocabulary's order.
  const areas = await menu
    .locator(".api-scope-area")
    .evaluateAll((els) => els.map((e) => e.textContent));
  expect(areas.slice(0, 3)).toEqual([
    "Grain API scopes: tasks",
    "Grain API scopes: merge",
    "Grain API scopes: ci",
  ]);

  // A preset shows what it expands to.
  const taskAdmin = menu.locator('[data-api-grant="api:task-admin"]');
  await expect(taskAdmin.locator(".api-preset-scopes")).toContainText(
    "tasks:write",
  );
  await expect(taskAdmin.locator(".api-preset-scopes")).toContainText(
    "merge:write",
  );

  // A dangerous scope is flagged; a safe one is not.
  const force = menu.locator('[data-api-grant="api:merge:force"]');
  await force.scrollIntoViewIfNeeded();
  await expect(force.locator(".api-dangerous")).toBeVisible();
  await expect(
    menu.locator('[data-api-grant="api:tasks:read"] .api-dangerous'),
  ).toHaveCount(0);

  // The search narrows every kind at once.
  await page.getByLabel("Search grants").fill("merge:force");
  await expect(menu.locator(".api-grant-row")).toHaveCount(1);
  await page.keyboard.press("Escape");

  // The legacy Capabilities list no longer carries them.
  await page.getByRole("combobox", { name: "Legacy capabilities" }).click();
  await expect(page.getByRole("option").first()).toBeVisible();
  await expect(page.getByRole("option", { name: /^API/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
});

test("grants a scope limited to one repository", async ({ page, request }) => {
  const task = await fileTask(request, []);
  await page.goto(`/tasks/${task.id}`);
  const { grants, menu } = await openMenu(page, task.id);

  await page.getByLabel("Grain API grants apply to").click();
  await page.getByRole("option", { name: "acme/widgets" }).click();
  await expect(page.getByRole("listbox")).toHaveCount(0);
  // The pick is written to the task and read back, so the mark follows
  // the server rather than flipping at once.
  const ciWrite = menu.getByRole("menuitemcheckbox", {
    name: "ci:write",
    exact: true,
  });
  await ciWrite.click();
  await expect(ciWrite).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");

  await expect(grants.locator(".grants-held")).toContainText(
    "ci:write on acme/widgets",
  );
  await expect
    .poll(async () => {
      const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
      return detail.capabilities;
    })
    .toContain("api:ci:write@acme/widgets");
});

test("exercises Grain API picker across NewTaskOverlay, ConsoleSessionPage, ScheduleOverlay, and TemplateOverlay, plus self-debug alias", async ({
  page,
  request,
}) => {
  // 1. Verify self-debug alias and api:self-debug preset resolution via HTTP gate
  for (const cap of ["self-debug", "api:self-debug"]) {
    const t = await fileTask(request, [cap]);
    const headers = { "X-Grain-Acting-Task": String(t.id) };

    const tasksRes = await request.get("/api/tasks", { headers });
    expect(tasksRes.status()).toBe(200);

    const depRes = await request.get("/api/restart-watch", { headers });
    expect(depRes.status()).toBe(200);

    const schedRes = await request.get("/api/schedules", { headers });
    expect(schedRes.status()).toBe(403);
    expect((await schedRes.json()).error).toContain("schedules:read");

    const writeRes = await request.post("/api/tasks", {
      headers,
      data: { title: "Blocked write", repo: "acme/widgets" },
    });
    expect(writeRes.status()).toBe(403);
    expect((await writeRes.json()).error).toContain("tasks:write");
  }

  // 2. NewTaskOverlay: the API presets and scopes are in the Grants
  // section's "Add grant…" menu, presets above scopes, at desktop and
  // phone widths.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.getByRole("button", { name: "+ New task" }).click();
  const newTaskGroup = page.getByTestId("new-task-grants");
  await expect(newTaskGroup).toContainText("None granted");
  await newTaskGroup.getByRole("button", { name: "Add grant…" }).click();
  const newTaskMenu = page.getByRole("menu", { name: "Grants to add" });
  await expect(newTaskMenu).toBeVisible();
  const selfDebugRow = newTaskMenu.locator(
    '[data-api-grant="api:self-debug"] .api-preset-scopes',
  );
  await expect(selfDebugRow).toContainText("tasks:read");
  await expect(selfDebugRow).toContainText("deployment:read");

  for (const vp of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(vp);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
    const presetBox = await newTaskMenu
      .locator('.api-grant-row[data-api-kind="preset"]')
      .first()
      .boundingBox();
    const scopeBox = await newTaskMenu
      .locator('.api-grant-row[data-api-kind="scope"]')
      .first()
      .boundingBox();
    expect(presetBox.width).toBeGreaterThan(0);
    expect(scopeBox.width).toBeGreaterThan(0);
    expect(presetBox.y + presetBox.height).toBeLessThanOrEqual(scopeBox.y);
  }
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 1440, height: 900 });

  // 3. ConsoleSessionPage: open Director session and grant self-debug
  // from its GrantsPicker, which saves it on the session at once.
  await page.goto("/sessions");
  await page
    .getByRole("button", { name: /^Open session Director/ })
    .first()
    .click();
  await page
    .getByRole("button", { name: "Edit Director capabilities" })
    .click();
  const sessionGroup = page.getByTestId("session-director-grants");
  await sessionGroup.getByRole("button", { name: "Add grant…" }).click();
  const sessionMenu = page.getByRole("menu", { name: "Grants to add" });
  const sessionSelfDebug = sessionMenu.getByRole("menuitemcheckbox", {
    name: "self-debug",
    exact: true,
  });
  await sessionSelfDebug.click();
  await expect(sessionSelfDebug).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");
  await expect(
    sessionGroup.locator('.grants-held [data-grant="api:self-debug"]'),
  ).toHaveCount(1);
  await expect
    .poll(async () => {
      const { sessions } = await (
        await request.get("/api/console-sessions")
      ).json();
      return sessions.find((s) => s.id === "director").capabilities;
    })
    .toContain("api:self-debug");

  // 4. ScheduleOverlay on /processes: select api:self-debug + api:ci:write@acme/widgets, save, reload, verify
  await page.goto("/processes");
  const schedTitle = `API grant schedule ${Date.now()}`;
  await page.getByRole("button", { name: "+ New", exact: true }).click();
  const menu = page.getByTestId("new-process-kind");
  await menu.getByRole("menuitem", { name: "Schedule" }).click();
  await page.getByLabel("Title", { exact: true }).fill(schedTitle);
  await page.locator("select[name='repo']").selectOption("acme/widgets");

  const schedGroup = page.getByTestId("schedule-grants");
  await grantSelfDebugAndRepoCIWrite(page, schedGroup);
  await page.getByRole("button", { name: "Add schedule" }).click();
  await expect(page.getByRole("heading", { name: "New schedule" })).toHaveCount(
    0,
  );

  const schedules = await (await request.get("/api/schedules")).json();
  const savedSched = schedules.find((s) => s.title === schedTitle);
  expect(savedSched).toBeTruthy();
  expect([...savedSched.capabilities].sort()).toEqual([
    "api:ci:write@acme/widgets",
    "api:self-debug",
  ]);

  await page.reload();
  await page
    .locator('.process-row[data-kind="schedule"]', { hasText: schedTitle })
    .click();
  await expectSelfDebugAndRepoCIWrite(page.getByTestId("schedule-grants"));
  await page.goto("/processes");

  // 5. TemplateOverlay on /processes: select api:self-debug + api:ci:write@acme/widgets, save, reload, verify
  const tmplName = `api-grant-tmpl-${Date.now()}`;
  await page.getByRole("button", { name: "+ New", exact: true }).click();
  await menu.getByRole("menuitem", { name: "Template" }).click();
  await page.getByLabel("Name", { exact: true }).fill(tmplName);
  await page
    .getByLabel("Task title", { exact: true })
    .fill("Template with API grants");

  const tmplGroup = page.getByTestId("template-grants");
  await grantSelfDebugAndRepoCIWrite(page, tmplGroup);
  await page.getByRole("button", { name: "Add template" }).click();
  await expect(page.getByRole("heading", { name: "New template" })).toHaveCount(
    0,
  );

  const templates = await (await request.get("/api/templates")).json();
  const savedTmpl = templates.find((t) => t.name === tmplName);
  expect(savedTmpl).toBeTruthy();
  expect([...savedTmpl.capabilities].sort()).toEqual([
    "api:ci:write@acme/widgets",
    "api:self-debug",
  ]);

  await page.reload();
  await page
    .locator('.process-row[data-kind="template"]', { hasText: tmplName })
    .click();
  await expectSelfDebugAndRepoCIWrite(page.getByTestId("template-grants"));
});

// Ticks api:self-debug for every repo and api:ci:write limited to
// acme/widgets from a Grants section's "Add grant…" menu.
async function grantSelfDebugAndRepoCIWrite(page, grants) {
  await grants.getByRole("button", { name: "Add grant…" }).click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await expect(menu).toBeVisible();
  const selfDebug = menu.getByRole("menuitemcheckbox", {
    name: "self-debug",
    exact: true,
  });
  await selfDebug.click();
  await expect(selfDebug).toHaveAttribute("aria-checked", "true");
  await page.getByLabel("Grain API grants apply to").click();
  await page.getByRole("option", { name: "acme/widgets" }).click();
  await expect(page.getByRole("listbox")).toHaveCount(0);
  const ciWrite = menu.getByRole("menuitemcheckbox", {
    name: "ci:write",
    exact: true,
  });
  await ciWrite.click();
  await expect(ciWrite).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");
  await expectSelfDebugAndRepoCIWrite(grants);
}

async function expectSelfDebugAndRepoCIWrite(grants) {
  const held = grants.locator(".grants-held");
  await expect(held.locator("[data-grant]")).toHaveCount(2);
  await expect(held).toContainText("self-debug");
  await expect(held).toContainText("ci:write on acme/widgets");
}
