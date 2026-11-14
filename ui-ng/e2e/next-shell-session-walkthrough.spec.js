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

// Regressions from the responsive navigation and live-session walkthrough
// (docs/test-runs/2026-11-13-responsive-navigation-live-sessions.md).

// An old /t/<team>/... link, saved before the single-tenant reset, opens
// the team-less page it names -- with its query -- instead of falling
// through the catch-all to Inbox. The classic UI's paths.js does the same.
test("an old /t/<team>/ link opens its team-less page", async ({ page }) => {
  await page.goto("/next/t/default/tasks");
  await expect(page).toHaveURL(/\/next\/tasks$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "Tasks", exact: true }),
  ).toBeVisible();

  await page.goto("/next/t/platform/settings/access");
  await expect(page).toHaveURL(/\/next\/settings\/access$/);

  await page.goto("/next/t/default/task/3?tab=chat");
  await expect(page).toHaveURL(/\/next\/task\/3\?tab=chat$/);
});

// Starting a session from the New session dialog lands in its chat with
// the composer focused, so the next keystroke is the next message. The
// list (/sessions) and a session (/sessions/:id) are separate routes, so
// this crosses a fresh instance of the page.
for (const size of [
  { width: 1440, height: 900 },
  { width: 390, height: 664 },
]) {
  test(
    `a new session focuses its composer at ${size.width}px`,
    { tag: ["@phone"] },
    async ({ page }) => {
      await page.setViewportSize(size);
      await page.goto("/next/sessions");
      await page
        .getByRole("region", { name: "Sessions" })
        .getByRole("button", { name: "New session" })
        .click();
      const dialog = page.getByTestId("new-session-dialog");
      await expect(page.getByTestId("new-session-name")).toBeFocused();
      const name = `Focus check ${size.width} ${Date.now() % 100000}`;
      await page.getByTestId("new-session-name").fill(name);
      await dialog.getByRole("button", { name: "Start session" }).click();
      await expect(page).toHaveURL(/\/next\/sessions\/[^/]+$/);
      const composer = page.getByTestId("composer");
      await expect(composer).toBeFocused();
      // Typing goes straight into it, and Send stays on screen.
      await page.keyboard.type("hello");
      await expect(composer).toHaveValue("hello");
      const send = await page.getByTestId("send").boundingBox();
      expect(send.y + send.height).toBeLessThanOrEqual(size.height);
      expect(send.x + send.width).toBeLessThanOrEqual(size.width);
    },
  );
}

// A viewer -- a role without write scopes -- is not offered a working
// create control or a task's composer, whose calls the server refuses:
// the shell's New task buttons are disabled with the reason (main's
// gating), and a task's composer gives way to the denial sentence.
// GET /api/config is the server's statement of the caller's access; it
// is rewritten here to a read-only role, as the demo signs everyone in
// as the operator. Everything else is the real server.
async function asViewer(page) {
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const json = await response.json();
    json.access = {
      role: "grain/viewer",
      roles: ["grain/viewer"],
      full: false,
      scopes: ["tasks:read", "sessions:read", "deployment:read"],
    };
    await route.fulfill({ response, json });
  });
}

test("a viewer cannot file or comment on a desktop", async ({ page }) => {
  await asViewer(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/tasks");
  await expect(
    page.getByRole("heading", { level: 1, name: "Tasks", exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("rail-new-task")).toBeDisabled();
  // The c shortcut does not open the New task sheet either.
  await page.locator("body").press("c");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await page.goto("/next/task/3");
  await expect(page.getByTestId("reply-blocked")).toBeVisible();
  await expect(page.getByTestId("reply-blocked")).toContainText("tasks:write");
  await expect(page.getByTestId("task-composer")).toHaveCount(0);
});

test(
  "a viewer cannot comment on a task on a phone",
  { tag: ["@phone"] },
  async ({ page }) => {
    await asViewer(page);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/next/task/3");
    const note = page.getByTestId("reply-blocked");
    await expect(note).toBeVisible();
    await expect(page.getByTestId("task-composer")).toHaveCount(0);
    const box = await note.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  },
);

test("an operator still has the composer", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/task/3");
  await expect(page.getByTestId("task-composer")).toBeVisible();
  await expect(page.getByTestId("reply-blocked")).toHaveCount(0);
  await expect(page.getByTestId("rail-new-task")).toBeEnabled();
});
