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

// Regression from the 2026-11-17 task intake, Inbox and lifecycle
// walkthrough (docs/test-runs/2026-11-17-task-intake-inbox-lifecycle.md):
// the Inbox's bulk confirm asked a person deferring proposals owned by
// someone else to tick "I am approving work owned by someone else" -- the
// dialog's default verb, which every bulk action but Approve inherited.

test("Defer all's ownership tick says deferring, not approving", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const res = await fetch(`${demoServer}/api/tasks`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      title: `Bulk verb proposal ${Date.now()}`,
      approved: false,
      owner: "bob@example.com",
    }),
  });
  expect(res.ok).toBe(true);

  await page.goto("inbox");
  await page.getByTestId("group-select-proposal").click();
  await page.getByTestId("bulk-defer").click();
  const dialog = page.getByRole("dialog", { name: "Defer all" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("I am deferring work");
  await expect(dialog).not.toContainText("approving");
  await expect(dialog.getByTestId("confirm-accept")).toBeDisabled();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
});
