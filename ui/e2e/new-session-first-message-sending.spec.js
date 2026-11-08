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

// The 2026-11-08 responsive navigation walkthrough (task 3361): on New
// session, the first message left the box at Send and drew nowhere until
// the session had been created and its page opened -- every later message
// in a chat shows at once as a "Sending…" bubble, but this one had none,
// so a slow create read as the message having been lost. The create is
// held here so that window can be looked at, then released to the real
// server, which names the session from the message and opens it.

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [label, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`New session's first message shows as sending while the session starts (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const message = `First message held ${label} ${Date.now()}`;
    let release;
    const held = new Promise((resolve) => {
      release = resolve;
    });
    await page.route("**/api/console-sessions", async (route) => {
      if (route.request().method() === "POST") await held;
      // The test may have ended (and unrouted) while this one was held.
      await route.continue().catch(() => {});
    });
    try {
      await page.goto("/session/new");
      const box = page.getByRole("textbox", { name: "Message" });
      await expect(box).toBeFocused();
      await box.pressSequentially(message);
      await page.getByRole("button", { name: "Send", exact: true }).click();

      // While the create is held: the box is empty, and the message is in
      // the transcript, marked sending, inside the viewport.
      await expect(box).toHaveText("");
      const bubble = page.getByText(message);
      await expect(bubble).toBeVisible();
      await expect(bubble).toBeInViewport();
      await expect(page.getByText("Sending…")).toBeVisible();
      await expect(page.getByText("No messages yet")).toHaveCount(0);
      await expect(page).toHaveURL(/\/session\/new$/);

      release();
      await expect(page).not.toHaveURL(/\/session\/new$/);
      await expect(page.getByText("Sending…")).toHaveCount(0);
      // Sent once: one bubble of the operator's says it after a reload
      // (the session's own opening instructions quote its title too).
      await page.reload();
      await expect(
        page
          .getByRole("region", { name: "operator (access code)" })
          .filter({ hasText: message }),
      ).toHaveCount(1);
    } finally {
      release();
      await page.unroute("**/api/console-sessions");
      const res = await page.request.get("/api/console-sessions");
      for (const s of (await res.json()).sessions) {
        if (s.title === message) {
          await page.request.delete(`/api/console-sessions/${s.id}`);
        }
      }
    }
  });
}
