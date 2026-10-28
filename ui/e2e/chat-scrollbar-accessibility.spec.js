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

import { expect, test } from "./fixtures.js";

// The chat's transcript scrolls with perfect-scrollbar, whose thumb is a
// tab stop of its own -- and, until grain/task-2961, a nameless <div>:
// tabbing through the Director session landed on a stop between the
// last "Copy response" and the message box that announced nothing. It
// stays in the tab order, since it is the only way a keyboard scrolls the
// transcript; it now says what it is (src/chatScrollbars.js).
test.describe("chat transcript scrollbar", () => {
  test("Tab reaches a named scrollbar that scrolls the transcript", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 600 });
    await page.goto("/session/director");
    const box = page.getByRole("textbox", { name: "Message" });
    await expect(box).toBeVisible();

    // Shift+Tab back from the composer is the stop right before it.
    await box.focus();
    await page.keyboard.press("Shift+Tab");
    const bar = page.getByRole("scrollbar", { name: "Scroll messages" });
    await expect(bar).toBeFocused();
    await expect(bar).toHaveAttribute("aria-orientation", "vertical");
    const controls = await bar.getAttribute("aria-controls");
    await expect(page.locator(`[id="${controls}"]`)).toHaveClass(
      /cs-message-list__scroll-wrapper/,
    );

    // A session opens pinned to its newest message.
    await expect(bar).toHaveAttribute("aria-valuenow", "100");
    await page.keyboard.press("Home");
    await expect(bar).toHaveAttribute("aria-valuenow", "0");
    await expect(
      page.locator(".cs-message-list__scroll-wrapper"),
    ).toHaveJSProperty("scrollTop", 0);
    await page.keyboard.press("End");
    await expect(bar).toHaveAttribute("aria-valuenow", "100");

    // No focusable element in the chat is left without a name.
    const unnamed = await page
      .locator(".task-chat-window [tabindex='0']")
      .evaluateAll((els) =>
        els
          .filter((e) => e.getBoundingClientRect().width > 0)
          .filter(
            (e) => !e.getAttribute("aria-label") && !(e.innerText || "").trim(),
          )
          .map((e) => e.className),
      );
    expect(unnamed).toEqual([]);
  });
});
