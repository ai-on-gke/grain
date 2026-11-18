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
// a role without tasks:write could type into a task page's composer and
// press Send, which posted a comment the server refuses that role. The
// Inbox's reply box was already read-only for it; the task page now is
// too, and says which scope it lacks.

test("a role without tasks:write reads a task's conversation but cannot post to it", async ({
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
      title: `Viewer composer ${Date.now()}`,
      approved: false,
    }),
  });
  expect(res.ok).toBe(true);
  const { id } = await res.json();

  // The access code signs in as the deployment's owner, so the role is
  // narrowed where the UI reads it, as next-inbox.spec.js's viewer test
  // does.
  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        json: {
          ...(await response.json()),
          access: {
            role: "grain/viewer",
            roles: ["grain/viewer"],
            full: false,
            scopes: ["tasks:read"],
          },
        },
      });
    },
  );
  const posted = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes(`/api/tasks/${id}/`)) {
      posted.push(r.url());
    }
  });

  await page.goto(`task/${id}`);
  await expect(
    page.getByRole("heading", { name: new RegExp(`#${id}`) }),
  ).toBeVisible();
  const composer = page.getByPlaceholder("Comment on this task");
  await expect(composer).toBeVisible();
  await expect(composer).toBeDisabled();
  await expect(page.getByTestId("act-send")).toBeDisabled();
  await expect(page.getByTestId("reply-blocked")).toContainText(
    "does not hold tasks:write",
  );
  expect(posted).toEqual([]);
});
