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

// Regressions from the responsive navigation and live-session walkthrough
// (docs/test-runs/2026-11-15-responsive-navigation-live-sessions.md):
//
// - an old /t/<team>/... link opens its team-less page, as the classic UI
//   does, rather than falling through to the Inbox;
// - the rail's Inbox link is named "Inbox (N waiting)", not "NInbox";
// - Sessions shows no team anywhere (no "default" pill, no "Visible to
//   team" field), grain being single-tenant;
// - New session hands focus to the opened chat's composer rather than
//   dropping it on <body> while the page is still inert.

import { expect, test } from "./fixtures";

test(
  "an old /t/<team>/ link opens its team-less page, keeping its query",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("t/acme/tasks?q=dark");
    await expect(page).toHaveURL(/\/next\/tasks\?q=dark$/);
    await expect(
      page.getByRole("heading", { level: 1, name: "Tasks", exact: true }),
    ).toBeVisible();

    await page.goto("t/acme/sessions/director");
    await expect(page).toHaveURL(/\/next\/sessions\/director$/);

    await page.goto("t/acme");
    await expect(page).toHaveURL(/\/next\/inbox$/);
  },
);

test("the rail's Inbox link names its count apart from its label", async ({
  page,
}) => {
  await page.goto("tasks");
  const rail = page.getByRole("navigation", { name: "Primary destinations" });
  const badge = rail.getByTestId("rail-inbox-count");
  await expect(badge).toBeVisible();
  const n = (await badge.innerText()).trim();
  expect(Number(n)).toBeGreaterThan(0);
  const inbox = rail.getByRole("link", {
    name: `Inbox (${n} waiting)`,
    exact: true,
  });
  await expect(inbox).toBeVisible();
  await inbox.click();
  await expect(page).toHaveURL(/\/next\/inbox$/);
  await expect(inbox).toHaveAttribute("aria-current", "page");
});

for (const size of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "short phone", width: 390, height: 667 },
]) {
  test(`New session at ${size.name} shows no team and focuses the composer`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: size.width, height: size.height });
    await page.goto("sessions/director");
    const pane = page.getByTestId("chat-pane");
    await expect(pane.getByRole("heading", { name: "Director" })).toBeVisible();
    await expect(pane.getByText("default", { exact: true })).toHaveCount(0);

    await page.goto("sessions");
    await page.getByTestId("new-session").click();
    const dialog = page.getByRole("dialog", { name: "New session" });
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId("new-session-name")).toBeFocused();
    await expect(dialog.getByText(/team/i)).toHaveCount(0);
    const create = page.getByTestId("new-session-create");
    const box = await create.boundingBox();
    expect(box && box.y + box.height).toBeLessThanOrEqual(size.height);

    const title = `Walkthrough ${size.name} ${Date.now()}`;
    await page.getByTestId("new-session-name").fill(title);
    await page.getByTestId("new-session-message").fill("Hello there");
    await create.click();
    await expect(dialog).toHaveCount(0);
    await expect(pane.getByRole("heading", { name: title })).toBeVisible();
    await expect(page.getByTestId("composer")).toBeFocused();
    await expect(page.getByTestId("chat-log")).toContainText("Hello there");

    // The opened session persists across a reload and is deletable,
    // after which its deep link no longer opens a chat for it.
    const url = page.url();
    await page.reload();
    await expect(pane.getByRole("heading", { name: title })).toBeVisible();
    await page.getByTestId("delete-session").click();
    await page.getByTestId("delete-confirm").click();
    await expect(page).toHaveURL(/\/next\/sessions$/);
    await page.goto(url);
    await expect(pane.getByRole("heading", { name: title })).toHaveCount(0);
  });
}
