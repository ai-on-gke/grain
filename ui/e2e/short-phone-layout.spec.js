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

// docs/test-runs/2026-11-03-ui-fixes.md §2.4: on short phones -- 390×640
// and 360×560 -- the phone's header bar keeps its buttons on one line and
// on screen, New chat keeps a chat window and a composer above the fold,
// and the navigation drawer's foot (the verified identity, the build and
// System) is on screen without one row drawn over another.

const EMAIL =
  "grain-daemon-runner@very-long-gcp-project-id.iam.gserviceaccount.com";

function within(box, size) {
  expect(box.x).toBeGreaterThanOrEqual(-0.5);
  expect(box.y).toBeGreaterThanOrEqual(-0.5);
  expect(box.x + box.width).toBeLessThanOrEqual(size.width + 0.5);
  expect(box.y + box.height).toBeLessThanOrEqual(size.height + 0.5);
}

function apart(a, b) {
  return (
    a.y + a.height <= b.y + 0.5 ||
    b.y + b.height <= a.y + 0.5 ||
    a.x + a.width <= b.x + 0.5 ||
    b.x + b.width <= a.x + 0.5
  );
}

for (const size of [
  { width: 390, height: 640 },
  { width: 360, height: 560 },
]) {
  test.describe(`on a ${size.width}×${size.height} touch phone`, () => {
    test.use({ viewport: size, hasTouch: true, isMobile: true });

    test("the header bar keeps one line with every button on screen", async ({
      page,
    }) => {
      await page.goto("/inbox");
      const names = ["Open navigation", "Settings", "New task"];
      const boxes = [];
      for (const name of names) {
        const button = page
          .locator("header")
          .getByRole("button", { name, exact: true });
        await expect(button).toBeVisible();
        const box = await button.boundingBox();
        within(box, size);
        boxes.push(box);
      }
      const mid = (b) => b.y + b.height / 2;
      for (const box of boxes) {
        expect(Math.abs(mid(box) - mid(boxes[0]))).toBeLessThan(4);
      }
      for (let i = 1; i < boxes.length; i++) {
        expect(apart(boxes[i - 1], boxes[i])).toBe(true);
      }
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(size.width);
    });

    test("New chat keeps a chat window and a composer above the fold", async ({
      page,
    }) => {
      await page.goto("/session/new");
      const message = page.getByRole("textbox", { name: "Message" });
      await expect(message).toBeVisible();
      within(await message.boundingBox(), size);
      const chat = page.locator(".console-session-body .task-chat-window");
      expect((await chat.boundingBox()).height).toBeGreaterThanOrEqual(150);
      const send = page.getByRole("button", { name: "Send" });
      await message.tap();
      await expect(message).toBeFocused();
      await message.pressSequentially("hello from a short phone");
      await expect(send).toBeEnabled();
      // The Send row is on screen with the focused box: before the fix it
      // sat 33px under the fold at 390×640 (F3).
      await expect
        .poll(async () => {
          const box = await send.boundingBox();
          return box.y + box.height <= size.height + 0.5;
        })
        .toBe(true);
      within(await send.boundingBox(), size);
      within(await message.boundingBox(), size);
    });

    test("the drawer's foot is on screen with nothing drawn over it", async ({
      page,
    }) => {
      await page.route("**/api/config", async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        await route.fulfill({
          response: res,
          json: {
            ...body,
            authenticated: true,
            authMethod: "iap",
            actor: EMAIL,
            actorKind: "human",
          },
        });
      });
      await page.goto("/inbox");
      await page.getByRole("button", { name: "Open navigation" }).tap();
      const signedIn = page.getByTestId("phone-signed-in");
      await expect(signedIn).toBeVisible();
      // Wait out the drawer's slide-in before measuring.
      await expect
        .poll(
          async () => (await page.locator(".MuiDrawer-paper").boundingBox()).x,
        )
        .toBe(0);
      const stamp = page.getByTestId("build-stamp");
      const parts = [signedIn];
      if (await stamp.count()) parts.push(stamp);
      const boxes = [];
      for (const part of parts) {
        const box = await part.boundingBox();
        within(box, size);
        boxes.push(box);
      }
      for (let i = 1; i < boxes.length; i++) {
        expect(apart(boxes[0], boxes[i])).toBe(true);
      }
      // The email is ellipsized inside the drawer, not run off its edge.
      const paper = await page.locator(".MuiDrawer-paper").boundingBox();
      within(paper, size);
      const email = await page
        .getByTestId("phone-signed-in-email")
        .boundingBox();
      expect(email.x + email.width).toBeLessThanOrEqual(
        paper.x + paper.width + 0.5,
      );
      // The rail's last row scrolls into view above the foot, which
      // starts at System, rather than under it.
      const paperLoc = page.locator(".MuiDrawer-paper");
      const last = paperLoc.getByRole("button", { name: /^Repos/ }).first();
      await last.scrollIntoViewIfNeeded();
      const lastBox = await last.boundingBox();
      const system = await paperLoc
        .getByRole("button", { name: "System", exact: true })
        .boundingBox();
      expect(lastBox.y + lastBox.height).toBeLessThanOrEqual(system.y + 0.5);
      within(lastBox, size);
    });
  });
}
