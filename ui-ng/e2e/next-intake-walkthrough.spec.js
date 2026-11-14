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
// /next/ (docs/test-runs/2026-11-13-task-intake-inbox-lifecycle-task-4726.md),
// against a real `grain demo`:
//
// - a changelist row's Submit follows the change system's own answer
//   (clSubmit), not merge gates a CL never has, and reads as a CL
//   submitted there and then rather than a pull request's approval;
// - a dependency chip says on hover what it waits for and in what state.
//
// (Stuck-row controls and a cold GET /api/tasks failure, which this
// walkthrough also found, are covered by next-inbox-intake-regressions.)
//
// The seeded rows are only looked at, so the demo store is left as the
// other specs expect it.

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

  // Presubmit passed and LGTM'd: submittable, and said to submit now.
  const ready = row(page, "Move the depot retry budget behind a flag");
  await expect(ready).toContainText("Submit this CL now");
  await expect(ready).not.toContainText("Approve the pull request");
  await ready.locator(".row-main").click();
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
