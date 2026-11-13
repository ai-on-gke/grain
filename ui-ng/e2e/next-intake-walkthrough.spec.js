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

// What the 2026-11-13 task-intake walkthrough found missing or wrong at
// /next/ (docs/test-runs/2026-11-13-task-intake-inbox-lifecycle.md),
// against a real `grain demo`:
//
// - a changelist row's Submit follows the change system's own answer
//   (clSubmit), not merge gates a CL never has;
// - a stuck row offers the control its sub-case names, and Cancel asks
//   first with a dismiss button that is not a second "Cancel";
// - a dependency chip says on hover what it waits for and in what state;
// - a cold GET /api/tasks failure on /next/tasks says so rather than
//   showing an empty list.
//
// The seeded rows are only looked at, and the one dialog opened here is
// dismissed, so the demo store is left as the other specs expect it.

// The stuck rows are seeded only with -stalled-tasks.
test.use({ demoArgs: ["-stalled-tasks"] });

const row = (page, text) =>
  page.getByTestId("queue-row").filter({ hasText: text });

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

test("CL rows offer Submit as the change system says, with its reasons", async ({
  page,
}) => {
  await page.goto("inbox");
  const inspector = page.getByTestId("next-inbox-inspector");

  // Presubmit passed and LGTM'd: submittable.
  await row(page, "Move the depot retry budget behind a flag")
    .locator(".row-main")
    .click();
  await expect(inspector.getByTestId("action-submit")).toBeEnabled();

  // Failed presubmit and changes requested: off, saying why.
  const failed = row(page, "Port the retry budget to the depot");
  await expect(failed).toContainText(
    "Not submittable yet: review is not approved (changes requested)",
  );
  await failed.locator(".row-main").click();
  await expect(inspector.getByTestId("action-submit")).toBeDisabled();

  // Behind head: off until synced.
  await expect(
    row(page, "Sync the depot retry budget with head"),
  ).toContainText("Not submittable yet: behind head: sync it.");
});

test("a stuck row offers its sub-case's control, and Cancel asks first", async ({
  page,
}) => {
  await page.goto("inbox");
  const inspector = page.getByTestId("next-inbox-inspector");

  await row(page, "Backfill the audit log retention job")
    .locator(".row-main")
    .click();
  await expect(inspector.getByTestId("action-run-now")).toBeVisible();

  await row(page, "Rotate the staging signing keys")
    .locator(".row-main")
    .click();
  await expect(inspector.getByTestId("action-undefer")).toBeVisible();

  await row(page, "Regenerate the protobuf bindings")
    .locator(".row-main")
    .click();
  await inspector.getByTestId("action-cancel").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("The run in flight is stopped");
  await expect(dialog.getByTestId("confirm-cancel")).toHaveText(
    "Keep it running",
  );
  await dialog.getByTestId("confirm-cancel").click();
  await expect(dialog).toHaveCount(0);
  await expect(row(page, "Regenerate the protobuf bindings")).toBeVisible();
});

test("a dependency chip names the task it waits for and its state", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const stamp = Date.now();
  const blocker = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Intake blocker ${stamp}`,
    approved: true,
    noRepo: true,
  });
  const waiter = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Intake waiter ${stamp}`,
    approved: true,
    noRepo: true,
  });
  try {
    await page.goto(`task/${waiter.id}`);
    await page.getByTestId("details-dep-input").fill(blocker.id);
    await page.getByTestId("details-dep-add").click();
    await expect(page.getByTestId("details-dep-chip")).toHaveAttribute(
      "title",
      `#${blocker.id} Intake blocker ${stamp} — Waiting (blocking this task)`,
    );
    await page
      .getByRole("button", { name: `Remove dependency #${blocker.id}` })
      .click();
    await expect(page.getByTestId("details-dep-chip")).toHaveCount(0);
  } finally {
    for (const t of [waiter, blocker]) {
      await api(demoServer, accessCode, "POST", `/api/tasks/${t.id}/close`);
    }
  }
});

test("a cold task-list failure on /next/tasks says so, not an empty list", async ({
  page,
}) => {
  await page.route(/\/api\/tasks(\?.*)?$/, (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 500,
          contentType: "application/json",
          body: '{"error":"store unavailable"}',
        })
      : route.continue(),
  );
  await page.goto("tasks");
  await expect(page.getByRole("alert")).toContainText(
    "The task list could not be read: store unavailable",
  );
  await expect(page.getByText("Nothing in this tab")).toHaveCount(0);
});
