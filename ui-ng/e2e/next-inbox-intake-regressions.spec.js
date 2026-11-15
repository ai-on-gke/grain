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

// Regressions from the 2026-11-14 task intake, Inbox and lifecycle
// walkthrough (docs/test-runs/2026-11-14-task-intake-inbox-lifecycle.md):
//
//   - a task list that has never been read is not an empty one: the
//     Inbox must not say "Nothing waiting on you", and the Tasks page
//     must say the read failed rather than show bare zero counts;
//   - a stuck task offers the control its sub-case names (Run now,
//     Cancel, Undefer), and pressing it takes the row out of the group.
//
// The demo seeds the stuck tasks (-stalled-tasks): #22 queued past its
// dispatch clock (Run now), #23 a live run past its longest clock
// (Cancel) and #24 deferred past its wake time (Undefer).
test.use({ demoArgs: ["-stalled-tasks"] });

const TASKS = /\/api\/tasks(\?.*)?$/;

async function failTaskReads(context) {
  await context.route(TASKS, (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 500,
          contentType: "application/json",
          body: '{"error":"store unavailable"}',
        })
      : route.continue(),
  );
}

test("a cold task-list failure never claims nothing is waiting, on the Inbox or Tasks", async ({
  page,
  context,
}) => {
  await failTaskReads(context);
  // No alerts either, so nothing at all reaches the queue: the case
  // where the Inbox used to read as an empty one.
  await context.route(/\/api\/notifications(\?.*)?$/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "[]" }),
  );

  await page.goto("/next/inbox");
  await expect(page.getByTestId("load-error")).toContainText(
    "Could not read tasks",
  );
  await expect(page.getByTestId("next-inbox-unread")).toBeVisible();
  await expect(page.getByTestId("next-inbox-headline")).toHaveText("Inbox");
  await expect(page.getByText("Nothing is waiting on you")).toHaveCount(0);

  await page.goto("/next/tasks");
  const banner = page.getByText("The task list could not be read");
  await expect(banner).toBeVisible();

  // Once reads come back the notice goes and the rows arrive, with no
  // reload.
  await context.unroute(TASKS);
  await expect(banner).toHaveCount(0, { timeout: 15_000 });
  await expect(
    page.getByText("Backfill the audit log retention job"),
  ).toBeVisible();
});

test("a stuck row offers the control its sub-case names, and leaves the group once pressed", async ({
  page,
  demoServer,
  accessCode,
}) => {
  await page.goto("/next/inbox");
  const stalled = page.getByTestId("inbox-group-stalled");
  const row = (title) =>
    stalled.getByTestId("queue-row").filter({ hasText: title });

  // A viewer sees the control, switched off: it needs tasks:write.
  const viewerConfig = (url) => url.pathname === "/api/config";
  await page.route(viewerConfig, async (route) => {
    const response = await route.fetch();
    const json = {
      ...(await response.json()),
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read"],
      },
    };
    await route.fulfill({ response, json });
  });
  await page.reload();
  await row("Rotate the staging signing keys").locator(".row-main").click();
  await expect(page.getByTestId("action-undefer")).toBeDisabled();
  await page.unroute(viewerConfig);
  await page.reload();

  await row("Rotate the staging signing keys").locator(".row-main").click();
  await page.getByTestId("action-undefer").click();
  await expect(row("Rotate the staging signing keys")).toHaveCount(0);

  await row("Backfill the audit log retention job")
    .locator(".row-main")
    .click();
  await page.getByTestId("action-run-now").click();
  await expect(row("Backfill the audit log retention job")).toHaveCount(0);

  // Cancel closes the task, so it asks first -- and its accept button is
  // not a second "Cancel" beside the dialog's own.
  await row("Regenerate the protobuf bindings").locator(".row-main").click();
  await expect(page.getByTestId("action-cancel")).toHaveText("Cancel task");
  await page.getByTestId("action-cancel").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("The run in flight is stopped");
  await expect(page.getByTestId("confirm-accept")).toHaveText("Cancel task");
  await page.getByTestId("confirm-accept").click();
  await expect(row("Regenerate the protobuf bindings")).toHaveCount(0);

  await page.reload();
  await expect(page.getByTestId("inbox-group-stalled")).not.toContainText(
    "Rotate the staging signing keys",
  );
  // The store kept what the buttons asked for. Looked up by title: which
  // ids the demo gives its seeds depends on the other flags it runs with.
  const res = await fetch(`${demoServer}/api/tasks?fields=list`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const stateOf = (title) => tasks.find((t) => t.title === title)?.state;
  expect(stateOf("Rotate the staging signing keys")).toBe("queued");
  expect(stateOf("Backfill the audit log retention job")).toBe("queued");
});

test("the Deferred tab's selection bar brings several tasks back at once", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const call = async (method, path, body) => {
    const res = await fetch(`${demoServer}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    expect(res.ok, `${method} ${path}: ${res.status}`).toBe(true);
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  };
  const ids = [];
  for (const n of [1, 2]) {
    const t = await call("POST", "/api/tasks", {
      title: `Deferred batch ${n} ${Date.now()}`,
      approved: true,
    });
    await call("POST", `/api/tasks/${t.id}/defer`, { deferredUntil: "" });
    ids.push(t.id);
  }
  try {
    await page.goto("/next/tasks?tab=deferred");
    for (const id of ids) {
      await page.getByLabel(`Select #${id}`, { exact: true }).check();
    }
    const bar = page.getByTestId("batch-bar");
    await expect(bar).toContainText("2 selected");
    await bar.getByTestId("batch-undefer").click();
    for (const id of ids) {
      await expect(page.getByTestId(`row-${id}`)).toHaveCount(0);
      expect((await call("GET", `/api/tasks/${id}`)).state).toBe("queued");
    }
    await expect(bar).toHaveCount(0);
  } finally {
    for (const id of ids) {
      await fetch(`${demoServer}/api/tasks/${id}/close`, {
        method: "POST",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
  }
});
