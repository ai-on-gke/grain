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

// A base branch no run could ever check out is refused when the task is
// filed -- a 400 the New task form shows as an alert -- instead of being
// queued and failing only once a worker picks it up (grain/task-1433).
test("an unusable base branch is refused with a visible validation message", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

  await page.getByLabel("Title").fill(`Unusable base ${Date.now()}`);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  await page.getByLabel("Base branch").fill("no such branch ..lock");

  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/tasks") &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Create task" }).click();
  const response = await created;
  expect(response.status()).toBe(400);
  expect(await response.text()).toContain("not a usable branch name");

  // The error snackbar sits outside the New task panel, which is modal
  // and so marks everything else aria-hidden while it is open.
  await expect(page.getByRole("alert", { includeHidden: true })).toContainText(
    'base "no such branch ..lock" is not a usable branch name',
  );
  // Nothing was filed, so the form stays open with what was typed.
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  await expect(page.getByLabel("Base branch")).toHaveValue(
    "no such branch ..lock",
  );
});
