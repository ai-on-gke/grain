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

  test(`New session carries queued follow-up sends and an in-progress draft into the created session (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const stamp = `${label}-${Date.now()}`;
    const first = `First queued-handoff ${stamp}`;
    const second = `Second queued-handoff ${stamp}`;
    const thirdDraft = `Third unsent draft ${stamp}`;
    let release;
    const held = new Promise((resolve) => {
      release = resolve;
    });
    await page.route("**/api/console-sessions", async (route) => {
      if (route.request().method() === "POST") await held;
      await route.continue().catch(() => {});
    });
    try {
      await page.goto("/session/new");
      const box = page.getByRole("textbox", { name: "Message" });
      const sendBtn = page.getByRole("button", { name: "Send", exact: true });
      await expect(box).toBeFocused();

      await box.pressSequentially(first);
      await sendBtn.click();
      await expect(box).toHaveText("");

      await box.click();
      await box.pressSequentially(second);
      await sendBtn.click();
      await expect(box).toHaveText("");

      await box.click();
      await box.pressSequentially(thirdDraft);

      // While session creation is still held: both sent messages sit in
      // order in the transcript marked Sending…, and the third draft stays
      // inside the composer within the viewport.
      const firstBubble = page.getByText(first);
      const secondBubble = page.getByText(second);
      await expect(firstBubble).toBeVisible();
      await expect(secondBubble).toBeVisible();
      await expect(page.getByText("Sending…")).toHaveCount(2);
      await expect(box).toHaveText(thirdDraft);

      const firstRect = await firstBubble.boundingBox();
      const secondRect = await secondBubble.boundingBox();
      const boxRectBefore = await box.boundingBox();
      expect(firstRect.x).toBeGreaterThanOrEqual(0);
      expect(firstRect.x + firstRect.width).toBeLessThanOrEqual(viewport.width);
      expect(secondRect.x).toBeGreaterThanOrEqual(0);
      expect(secondRect.x + secondRect.width).toBeLessThanOrEqual(
        viewport.width,
      );
      expect(firstRect.y).toBeLessThan(secondRect.y);
      expect(boxRectBefore.x).toBeGreaterThanOrEqual(0);
      expect(boxRectBefore.x + boxRectBefore.width).toBeLessThanOrEqual(
        viewport.width,
      );

      release();
      await expect(page).not.toHaveURL(/\/session\/new$/);
      await expect(page.getByText("Sending…")).toHaveCount(0);

      // On the opened session page: the unsent draft is still in the
      // composer inside the viewport, and both sent messages are visible.
      const openedBox = page.getByRole("textbox", { name: "Message" });
      await expect(openedBox).toHaveText(thirdDraft);
      const boxRectAfter = await openedBox.boundingBox();
      expect(boxRectAfter.x).toBeGreaterThanOrEqual(0);
      expect(boxRectAfter.x + boxRectAfter.width).toBeLessThanOrEqual(
        viewport.width,
      );
      await expect(
        page
          .getByRole("region", { name: "operator (access code)" })
          .filter({ hasText: first }),
      ).toHaveCount(1);
      await expect(
        page
          .getByRole("region", { name: "operator (access code)" })
          .filter({ hasText: second }),
      ).toHaveCount(1);

      // Reload confirms both messages persisted once in order on the server.
      await page.reload();
      const firstPersisted = page
        .getByRole("region", { name: "operator (access code)" })
        .filter({ hasText: first });
      const secondPersisted = page
        .getByRole("region", { name: "operator (access code)" })
        .filter({ hasText: second });
      await expect(firstPersisted).toHaveCount(1);
      await expect(secondPersisted).toHaveCount(1);
      const p1 = await firstPersisted.boundingBox();
      const p2 = await secondPersisted.boundingBox();
      expect(p1.y).toBeLessThan(p2.y);
    } finally {
      release();
      await page.unroute("**/api/console-sessions");
      const res = await page.request.get("/api/console-sessions");
      for (const s of (await res.json()).sessions) {
        if (s.title === first) {
          await page.request.delete(`/api/console-sessions/${s.id}`);
        }
      }
    }
  });
}
