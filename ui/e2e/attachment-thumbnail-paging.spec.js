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

// A comment carrying several screenshots -- what a run attaches after
// `grain screenshot` -- draws them as a strip of thumbnails, and the
// viewer one opens pages through all of them (AttachmentLinks.jsx,
// PR #756): the arrows at its sides and the keyboard's left and right
// step, wrapping round, and a counter says which one this is. Walked at
// desktop and phone sizes in both themes
// (docs/test-runs/2026-11-07-capabilities-fencing-admission.md, item 5).

// Three solid 64x40 PNGs, one colour each, so a wrong page is a wrong
// file name rather than a picture nobody can tell apart.
const PNGS = [
  "iVBORw0KGgoAAAANSUhEUgAAAEAAAAAoCAIAAADBrGu+AAAAPUlEQVR42u3PQQkAAAgEsAt2/VMYxgw+hcEKLNO+FgEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQGBiwW7BUjEu4zs7QAAAABJRU5ErkJggg==",
  "iVBORw0KGgoAAAANSUhEUgAAAEAAAAAoCAIAAADBrGu+AAAAPUlEQVR42u3PQQkAAAgEsAtmCCMa1Qw+hcEKLDX9WgQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQELhYRLLimCvq9UQAAAABJRU5ErkJggg==",
  "iVBORw0KGgoAAAANSUhEUgAAAEAAAAAoCAIAAADBrGu+AAAAPUlEQVR42u3PQQkAAAgEsAtmErMbxgw+hcEKLNXzWgQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQELhazZXTTsc1icwAAAABJRU5ErkJggg==",
];

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

for (const size of SIZES) {
  for (const scheme of ["light", "dark"]) {
    test(`attached screenshots page in the viewer at ${size.name} (${scheme})`, async ({
      page,
      request,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: scheme });
      const stamp = `${size.name}-${scheme}-${Date.now()}`;
      const names = ["home", "settings", "inbox"].map(
        (n) => `${n}-${stamp}.png`,
      );
      const created = await request.post("/api/tasks", {
        data: { title: `E2E screenshots ${stamp}`, repo: "acme/widgets" },
      });
      expect(created.ok()).toBeTruthy();
      const task = await created.json();
      const commented = await request.post(`/api/tasks/${task.id}/comments`, {
        data: {
          body: "Screenshots from `grain screenshot`.",
          attachments: names.map((filename, i) => ({
            filename,
            contentType: "image/png",
            content: PNGS[i],
          })),
        },
      });
      expect(commented.ok()).toBeTruthy();

      await page.goto(`/tasks/${task.id}`);
      const strip = page.getByTestId("attachment-thumbnails").last();
      await expect(strip.getByRole("button")).toHaveCount(3);
      // The strip stays inside the window: thumbnails wrap rather than
      // pushing the page sideways on a phone.
      const stripBox = await strip.boundingBox();
      expect(stripBox.x).toBeGreaterThanOrEqual(0);
      expect(stripBox.x + stripBox.width).toBeLessThanOrEqual(size.width + 0.5);
      for (const name of names) {
        await expect(strip.getByRole("button", { name })).toBeVisible();
      }

      // Open the second, then page with the arrows and the keyboard.
      await strip.getByRole("button", { name: names[1] }).click();
      // The task itself is open in an overlay of its own, so the viewer is
      // the dialog holding the image's own way out.
      const dialog = page.getByRole("dialog").filter({
        has: page.getByRole("button", { name: "Close image" }),
      });
      const caption = dialog.getByText(/\d \/ 3 · /);
      const showing = async (i) => {
        await expect(caption).toContainText(`${i + 1} / 3 · ${names[i]}`);
        await expect(dialog.getByRole("img", { name: names[i] })).toBeVisible();
      };
      await showing(1);

      // The way out and the two arrows are on screen, the arrows at the
      // sides and level with the middle of the window.
      const prev = dialog.getByRole("button", { name: "Previous image" });
      const next = dialog.getByRole("button", { name: "Next image" });
      const close = dialog.getByRole("button", { name: "Close image" });
      const p = await prev.boundingBox();
      const n = await next.boundingBox();
      const c = await close.boundingBox();
      expect(p.x).toBeGreaterThanOrEqual(0);
      expect(p.x + p.width).toBeLessThan(size.width / 2);
      expect(n.x).toBeGreaterThan(size.width / 2);
      expect(n.x + n.width).toBeLessThanOrEqual(size.width + 0.5);
      for (const b of [p, n]) {
        expect(Math.abs(b.y + b.height / 2 - size.height / 2)).toBeLessThan(2);
      }
      expect(c.y).toBeGreaterThanOrEqual(0);
      expect(c.x + c.width).toBeLessThanOrEqual(size.width + 0.5);
      expect(c.y + c.height).toBeLessThan(p.y);

      await next.click();
      await showing(2);
      await page.keyboard.press("ArrowRight"); // wraps round to the first
      await showing(0);
      await page.keyboard.press("ArrowLeft"); // and back past it
      await showing(2);
      await prev.click();
      await showing(1);

      // Escape leaves the viewer, and only the viewer.
      await page.keyboard.press("Escape");
      await expect(dialog).toHaveCount(0);
      await expect(strip).toBeVisible();
      // Nothing navigated: the task is still the page.
      await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`));
    });
  }
}
