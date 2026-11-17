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

// Bulk edit's Task priority holds to the caller's role
// (docs/test-runs/2026-11-16-next-task-operations.md, F1): a person
// without tasks:admin on a selected row's repository may set at most 5
// (pkg/ui/agent_api_gate.go refuses more), so the form says so beside the
// field and keeps Apply off, where it used to send 9 and have every row
// refused. A role that holds tasks:admin everywhere is not capped.
test.use({ demoArgs: ["-open=false", "-as", "next-tasks-bulk-edit-cap"] });
test.describe.configure({ mode: "serial" });

async function api(baseURL, code, method, p, body) {
  const res = await fetch(`${baseURL}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

async function queued(demoServer, code, title) {
  const t = await api(demoServer, code, "POST", "/api/tasks", {
    title,
    description: "bulk edit priority cap",
  });
  await api(demoServer, code, "POST", `/api/tasks/${t.id}/approve`);
  return t;
}

const row = (page, id) =>
  page.locator(".task-row", { has: page.getByTestId(`open-${id}`) });
const tick = (page, id) =>
  page.getByRole("checkbox", { name: `Select #${id}`, exact: true });

// writer narrows /api/config's access to tasks:read and tasks:write, the
// shape of a role such as {scopes: [tasks:read, tasks:write]} with no
// tasks:admin.
async function writer(page) {
  await page.route("**/api/config", async (route) => {
    const upstream = await route.fetch();
    const json = await upstream.json();
    json.access = {
      role: "acme/widgets-writer",
      roles: ["acme/widgets-writer"],
      full: false,
      scopes: ["tasks:read", "tasks:write"],
    };
    await route.fulfill({ response: upstream, json });
  });
}

async function openBulkEdit(page, ids) {
  await page.goto("tasks?tab=backlog");
  for (const id of ids) {
    await expect(row(page, id)).toBeVisible();
    await tick(page, id).check();
  }
  await page.getByTestId("batch-bar").getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Bulk edit…" }).click();
  return page.getByTestId("batch-edit-dialog");
}

test("Bulk edit caps Task priority at 5 for a role without tasks:admin @phone", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const stamp = Date.now();
  const x = await queued(demoServer, accessCode, `Cap X ${stamp}`);
  const y = await queued(demoServer, accessCode, `Cap Y ${stamp}`);
  await writer(page);
  const patches = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH" && /\/api\/tasks\/[^/]+$/.test(r.url()))
      patches.push(r.postDataJSON());
  });
  const form = await openBulkEdit(page, [x.id, y.id]);
  await expect(form).toContainText("your role sets at most 5");

  const box = form.getByTestId("bulk-priority");
  const apply = form.getByTestId("batch-edit-apply");
  await box.fill("9");
  const err = form.getByTestId("bulk-priority-error");
  await expect(err).toHaveText(
    "Above 5 needs tasks:admin on every selected task's repository.",
  );
  await expect(box).toHaveAttribute("aria-invalid", "true");
  await expect(apply).toBeDisabled();
  // The message sits under the field, inside the dialog.
  const fb = await box.boundingBox();
  const eb = await err.boundingBox();
  const db = await form.boundingBox();
  expect(eb.y).toBeGreaterThanOrEqual(fb.y + fb.height - 0.5);
  expect(eb.x).toBeGreaterThanOrEqual(db.x - 0.5);
  expect(eb.x + eb.width).toBeLessThanOrEqual(db.x + db.width + 0.5);
  expect(await axe()).toEqual([]);

  // Not a whole number still says so, not the cap.
  await box.fill("2.5");
  await expect(err).toHaveText("Use a whole number, 1 or more.");

  // Within the cap it goes through and lands on both.
  await box.fill("3");
  await expect(err).toHaveCount(0);
  await expect(apply).toBeEnabled();
  await apply.click();
  await expect(page.getByText(/Edited 2 tasks/)).toBeVisible();
  expect(patches.every((p) => p.priority === 3)).toBe(true);
  for (const t of [x, y]) {
    const d = await api(demoServer, accessCode, "GET", `/api/tasks/${t.id}`);
    expect(d.priority).toBe(3);
  }
});

test("Bulk edit leaves Task priority uncapped for tasks:admin", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const stamp = Date.now();
  const x = await queued(demoServer, accessCode, `Uncapped X ${stamp}`);
  const form = await openBulkEdit(page, [x.id]);
  await expect(form).not.toContainText("your role sets at most");
  await form.getByTestId("bulk-priority").fill("9");
  await expect(form.getByTestId("bulk-priority-error")).toHaveCount(0);
  await form.getByTestId("batch-edit-apply").click();
  await expect(page.getByText(/Edited #|Edited 1 task/)).toBeVisible();
  const d = await api(demoServer, accessCode, "GET", `/api/tasks/${x.id}`);
  expect(d.priority).toBe(9);
});
