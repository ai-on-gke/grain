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
// selecting a changelist task -- one with no repository -- in the Inbox,
// or opening its Changes and CI tab, asked for a diff preview the server
// can only answer with 404 ("has no repository branch to preview"),
// which logged a console error each time, and the tab then said "No
// branch changes against base yet" as though a branch existed.

const CL_TITLE = "Port the retry budget to the depot";

test("a changelist task asks for no diff preview, in the Inbox or on its Changes tab", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const res = await fetch(`${demoServer}/api/tasks?fields=list`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const cl = tasks.find((t) => t.title === CL_TITLE);
  expect(cl, "the demo seeds a changelist task").toBeTruthy();
  expect(cl.repo || "").toBe("");

  const previews = [];
  page.on("request", (r) => {
    if (r.url().includes("/diff-preview")) previews.push(r.url());
  });

  await page.goto("inbox");
  const row = page
    .getByTestId("inbox-group-submit")
    .locator("li")
    .filter({ hasText: `#${cl.id}` })
    .first();
  await row.click();
  // Submit is offered in the inspector's action area.
  await expect(
    page
      .getByTestId("next-inbox-inspector")
      .getByTestId("task-action-area")
      .getByRole("button", { name: "Submit" }),
  ).toBeVisible();

  await page.goto(`task/${cl.id}`);
  await page.getByTestId("task-tab-changes").click();
  await expect(
    page.getByText(
      "This task's change is a changelist, with no repository branch to preview.",
    ),
  ).toBeVisible();
  expect(previews).toEqual([]);
});
