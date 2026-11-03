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

// What the 2026-11-02 responsive navigation walkthrough found
// (docs/test-runs/2026-11-02-responsive-navigation-live-sessions.md):
// Tab through a tablet's header rail scrolled its clipped slot to the
// last wrapped row and left it there, and on a short phone New chat's
// chat window collapsed to 2px with the composer clipped inside it.

import { expect, test } from "./fixtures.js";

const TABLET = { width: 820, height: 1180 };

test("Tab through a wrapped header rail opens More and leaves the first line in place", async ({
  page,
}) => {
  await page.setViewportSize(TABLET);
  await page.goto("/processes");
  const slot = page.getByTestId("page-header-actions");
  const more = page.getByTestId("page-header-more");
  const search = page.getByRole("textbox", { name: "Search processes…" });
  await expect(more).toBeVisible();
  await expect(more).toHaveAttribute("aria-expanded", "false");
  const rail = await page.getByTestId("page-header-title").boundingBox();

  // "+ New" comes first in the slot's order but is wrapped behind
  // "more": reaching it by keyboard opens the menu around it.
  await page.getByTestId("page-header-title").click();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "+ New", exact: true }),
  ).toBeFocused();
  await expect(more).toHaveAttribute("aria-expanded", "true");

  // On past the rail's last button and into the page: the menu closes,
  // and the slot shows its first line again -- before the fix it stayed
  // scrolled to "+ Run", the search box gone from the rail.
  await page.getByRole("button", { name: "Settings" }).focus();
  await page.keyboard.press("Tab");
  await expect(more).toHaveAttribute("aria-expanded", "false");
  expect(await slot.evaluate((n) => n.scrollTop)).toBe(0);
  const box = await search.boundingBox();
  expect(
    Math.abs(box.y + box.height / 2 - (rail.y + rail.height / 2)),
  ).toBeLessThan(8);
});

test.describe("on a 320px touch phone", () => {
  test.use({
    viewport: { width: 320, height: 568 },
    hasTouch: true,
    isMobile: true,
  });

  test("New chat keeps a working chat window and composer", async ({
    page,
  }) => {
    await page.goto("/session/new");
    const message = page.getByRole("textbox", { name: "Message" });
    await expect(message).toBeVisible();
    const chat = page.locator(".console-session-body .task-chat-window");
    // Before the fix the window was 2px tall and the tap below landed on
    // the compose bar over it.
    expect((await chat.boundingBox()).height).toBeGreaterThanOrEqual(200);
    await message.tap();
    await expect(message).toBeFocused();
    await message.pressSequentially("hello from a small phone");
    await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
  });
});
