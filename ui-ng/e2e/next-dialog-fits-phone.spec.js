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

// A .g-dialog (New session, and the CI, agents, processes and inbox
// dialogs) is sized min(520px, 100vw - 32px). Its 24px padding and 1px
// border used to be added on top of that width, so on a phone the dialog
// was 50px wider than the space it was given and ran 9px off both edges
// of the screen (2026-11-13 responsive navigation walkthrough). Tagged
// @phone so the phone project runs it too; the desktop project checks it
// at 320 and 390 by resizing.

import { test, expect } from "./fixtures.js";

for (const width of [320, 390]) {
  test(
    `the New session dialog fits a ${width}px phone`,
    { tag: ["@phone"] },
    async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto("/next/sessions");
      await page
        .getByRole("region", { name: "Sessions" })
        .getByRole("button", { name: "New session" })
        .click();
      const dialog = page.getByTestId("new-session-dialog");
      await expect(dialog).toBeVisible();
      const box = await dialog.boundingBox();
      expect(
        box.x,
        "the dialog's left edge is on screen",
      ).toBeGreaterThanOrEqual(0);
      expect(
        box.x + box.width,
        "the dialog's right edge is on screen",
      ).toBeLessThanOrEqual(width);
      // The 16px margin either side that the width rule leaves.
      expect(Math.round(box.x)).toBe(16);
      expect(Math.round(box.width)).toBe(width - 32);
      // Its fields and actions are inside it, not clipped by the screen.
      for (const name of ["Cancel", "Start session"]) {
        const b = await dialog.getByRole("button", { name }).boundingBox();
        expect(b.x + b.width).toBeLessThanOrEqual(width);
      }
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toHaveCount(0);
    },
  );
}

test("a dialog keeps its 520px width on a desktop", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/sessions");
  await page
    .getByRole("region", { name: "Sessions" })
    .getByRole("button", { name: "New session" })
    .click();
  const box = await page.getByTestId("new-session-dialog").boundingBox();
  expect(Math.round(box.width)).toBe(520);
  expect(Math.round(box.x + box.width / 2)).toBe(720);
});

// The phone's More sheet had the same fault on its height: its
// calc(100dvh - 72px) cap left out its 24px of padding, so the sheet
// grew 4-8px past the top of the screen. It stops 16px under it now, and
// its last entry scrolls into reach above the compact bar.
for (const [width, height] of [
  [320, 568],
  [390, 844],
  [844, 390],
]) {
  test(
    `the More sheet stays under the top of a ${width}x${height} screen`,
    { tag: ["@phone"] },
    async ({ page }) => {
      await page.setViewportSize({ width, height });
      await page.goto("/next/inbox");
      await page.getByTestId("compact-more").click();
      const sheet = page.getByTestId("more-sheet");
      await expect(sheet).toBeVisible();
      const box = await sheet.boundingBox();
      expect(box.y, "the sheet's top is on screen").toBeGreaterThanOrEqual(0);
      const last = sheet.getByRole("button", { name: "Customize navigation" });
      await last.scrollIntoViewIfNeeded();
      const lb = await last.boundingBox();
      expect(lb.y).toBeGreaterThanOrEqual(0);
      expect(lb.y + lb.height).toBeLessThanOrEqual(box.y + box.height);
    },
  );
}
