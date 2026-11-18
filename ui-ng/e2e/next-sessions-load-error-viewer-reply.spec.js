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

// Two findings of the 2026-11-17 responsive-navigation and live-session
// walkthrough (docs/test-runs/2026-11-17-responsive-navigation-live-sessions.md):
//
//   - A failed GET /api/console-sessions read made the chat pane say "That
//     session does not exist." beside the load-error banner -- on
//     /sessions/director too, where the session was never read at all.
//   - A viewer without tasks:write got an enabled reply composer on the
//     task page, which the server then refused.

import { expect, test } from "./fixtures";

test("a failed sessions read reports the error, not a missing session @phone", async ({
  page,
}) => {
  await page.route("**/api/console-sessions*", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "session store unavailable" }),
    }),
  );
  for (const path of ["sessions", "sessions/director"]) {
    await page.goto(path);
    await expect(page.getByTestId("load-error")).toContainText(
      "Sessions could not be read: session store unavailable",
    );
    await expect(page.getByText("That session does not exist.")).toHaveCount(0);
    await expect(page.getByText("No sessions yet.")).toHaveCount(0);
  }

  // Once the read answers again the page recovers on its own.
  await page.unroute("**/api/console-sessions*");
  await expect(page.getByTestId("load-error")).toHaveCount(0, {
    timeout: 20_000,
  });
  await expect(
    page.getByTestId("chat-pane").getByRole("heading", { name: "Director" }),
  ).toBeVisible();
});

test("a deleted or unknown session still says it does not exist", async ({
  page,
}) => {
  await page.goto("sessions/no-such-session-walkthrough");
  await expect(page.getByText("That session does not exist.")).toBeVisible();
});

test("a viewer's task reply composer is disabled with the reason @phone", async ({
  page,
}) => {
  await page.goto("inbox");
  const tasks = await (
    await page.request.get(new URL("/api/tasks", page.url()).toString())
  ).json();
  const open = tasks.find((t) => t.state === "queued" || t.state === "running");
  // The default list leaves closed tasks out; ask for them.
  const [closed] = await (
    await page.request.get(
      new URL("/api/tasks?state=closed&limit=1", page.url()).toString(),
    )
  ).json();
  expect(open, "the demo has an open task").toBeTruthy();

  await page.goto(`task/${open.id}`);
  const composer = page.getByTestId("task-composer");
  await expect(composer).toBeEnabled();
  await expect(page.getByTestId("reply-blocked")).toHaveCount(0);

  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const json = {
        ...(await response.json()),
        access: {
          role: "grain/viewer",
          roles: ["grain/viewer"],
          full: false,
          scopes: ["tasks:read", "sessions:read", "repos:read", "ci:read"],
        },
      };
      await route.fulfill({ response, json });
    },
  );
  await page.reload();
  await expect(composer).toBeDisabled();
  await expect(page.getByTestId("act-send")).toBeDisabled();
  await expect(page.getByTestId("reply-blocked")).toContainText("tasks:write");

  // A closed task offers no composer, so there is nothing for the note to
  // explain: it must not appear under the conversation.
  expect(closed, "the demo has a closed task").toBeTruthy();
  await page.goto(`task/${closed.id}`);
  await expect(page.getByRole("heading", { level: 1 })).toContainText(
    `#${closed.id}`,
  );
  await expect(composer).toHaveCount(0);
  await expect(page.getByTestId("reply-blocked")).toHaveCount(0);
});
