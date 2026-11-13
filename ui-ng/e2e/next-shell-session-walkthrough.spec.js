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
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Tasks");

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
