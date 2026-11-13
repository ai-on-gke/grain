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

// Task intake, the Tasks list and the Inbox's stuck-task group at /next/,
// over a real `grain demo` started with -stalled-tasks (so the Inbox has
// its "Look at a stuck task" rows) and the real built frontend. Each test
// pins a bug the 2026-11-13 task-intake walkthrough found
// (docs/test-runs/2026-11-13-task-intake-inbox-lifecycle.md):
//
// - filing from a stored template was refused 404 by from-template;
// - ?q= narrowed Tasks while the Filter this view box stayed empty and
//   typing in it did nothing;
// - a task feed that had already failed made /next/tasks claim an empty
//   tab with no error;
// - a stuck task's row offered none of the controls its sub-case names.

test.use({ demoArgs: ["-stalled-tasks"] });

async function apiJson(baseURL, code, method, p, body) {
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

test("files a stored template from the New task sheet with the template's priority", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const name = `Walk urgent fix ${Date.now()}`;
  const tmpl = await apiJson(demoServer, accessCode, "POST", "/api/templates", {
    name,
    title: "Urgent: walk",
    description: "Fix it fast",
    priority: 8,
  });
  let createdId = "";
  try {
    await page.goto("inbox");
    await page.getByRole("button", { name: /^New/ }).first().click();
    const sheet = page.getByRole("dialog").first();
    await sheet.getByTestId("nt-template").click();
    await page.getByRole("option", { name }).click();
    const [resp] = await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().includes("/api/tasks") && r.request().method() === "POST",
      ),
      sheet.getByTestId("new-task-submit").click(),
    ]);
    // A stored template is not the repository's: it files through POST
    // /api/tasks {templateId}, never /api/tasks/from-template.
    expect(new URL(resp.url()).pathname).toBe("/api/tasks");
    expect(resp.status()).toBe(201);
    createdId = (await resp.json()).id;
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const t = await apiJson(
      demoServer,
      accessCode,
      "GET",
      `/api/tasks/${createdId}`,
    );
    expect(t.priority).toBe(8);
    expect(t.templateId).toBe(tmpl.id);
    expect(t.title).toBe("Urgent: walk");
    // Its page says it was filed "just now", never "now ago".
    await page.goto(`task/${createdId}`);
    await expect(page.getByText(/just now by/)).toBeVisible();
    await expect(page.getByText(/\bnow ago\b/)).toHaveCount(0);
  } finally {
    if (createdId) {
      await fetch(`${demoServer}/api/tasks/${createdId}/close`, {
        method: "POST",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
    await fetch(`${demoServer}/api/templates/${tmpl.id}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("keeps the Filter this view box and ?q= in step across typing and reload", async ({
  page,
}) => {
  const box = page.getByTestId("view-filter");
  const rows = page.locator('[data-testid^="row-"]');
  await page.goto("tasks?tab=all&q=retry");
  await expect(box).toHaveValue("retry");
  await expect(rows.filter({ hasText: "Fix flaky retry test" })).toHaveCount(1);
  await expect(rows.filter({ hasText: "Bump the Go toolchain" })).toHaveCount(
    0,
  );

  await box.fill("toolchain");
  await expect(page).toHaveURL(/[?&]q=toolchain(&|$)/);
  await expect(
    rows.filter({ hasText: "Bump the Go toolchain" }).first(),
  ).toBeVisible();
  await expect(rows.filter({ hasText: "Fix flaky retry test" })).toHaveCount(0);

  await page.reload();
  await expect(box).toHaveValue("toolchain");
  await expect(
    rows.filter({ hasText: "Bump the Go toolchain" }).first(),
  ).toBeVisible();

  await box.fill("");
  await expect(page).not.toHaveURL(/[?&]q=/);
  await expect(rows.filter({ hasText: "Fix flaky retry test" })).toHaveCount(1);
  await expect(
    rows.filter({ hasText: "Bump the Go toolchain" }).first(),
  ).toBeVisible();
});

test("says the task list could not be read on a cold failure rather than showing an empty tab", async ({
  page,
}) => {
  await page.route(/\/api\/tasks(\?.*)?$/, (r) =>
    r.request().method() === "GET"
      ? r.fulfill({
          status: 500,
          contentType: "application/json",
          body: '{"error":"store unavailable"}',
        })
      : r.continue(),
  );
  await page.goto("tasks");
  await expect(
    page.getByRole("alert").filter({ hasText: "could not be read" }),
  ).toBeVisible();
  await expect(page.getByTestId("tasks-pane-empty")).toHaveCount(0);
  await expect(page.getByText("Nothing in this tab")).toHaveCount(0);
});

test("offers a stuck task's own control on its Inbox row, and the row leaves once pressed", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const tasks = await apiJson(demoServer, accessCode, "GET", "/api/tasks");
  const deferred = tasks.find(
    (t) => t.stall && t.reason?.control === "Undefer",
  );
  const queued = tasks.find((t) => t.stall && t.reason?.control === "Run now");
  const running = tasks.find((t) => t.stall && t.reason?.control === "Cancel");
  expect(
    deferred,
    "the -stalled-tasks demo seeds a stuck deferral",
  ).toBeTruthy();

  await page.goto("inbox");
  const group = page.getByTestId("inbox-group-stalled");
  const row = (t) =>
    group.getByTestId("queue-row").filter({ hasText: t.title });
  const inspector = page.getByTestId("next-inbox-inspector");

  // Each sub-case names its own control.
  for (const [t, id, label] of [
    [queued, "run-now", "Run now"],
    [running, "cancel", "Cancel"],
    [deferred, "undefer", "Undefer"],
  ]) {
    if (!t) continue;
    await row(t).click();
    await expect(inspector.getByTestId(`action-${id}`)).toHaveText(label);
  }

  // Undefer the stuck deferral: the row leaves the group and the task is
  // back in the queue.
  await row(deferred).click();
  await inspector.getByTestId("action-undefer").click();
  await expect(row(deferred)).toHaveCount(0);
  await expect
    .poll(
      async () =>
        (
          await apiJson(
            demoServer,
            accessCode,
            "GET",
            `/api/tasks/${deferred.id}`,
          )
        ).state,
    )
    .toBe("queued");
});
