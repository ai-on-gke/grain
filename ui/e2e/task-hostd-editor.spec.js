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

// The task page's Hostd section (model.SetTaskHostd): editing the pin
// sends PUT /api/tasks/{id}/hostd and the page then shows the new one,
// and the section and its editor stay inside the detail column at
// desktop and phone widths.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function expectInside(inner, outer, what) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a, `${what} has no box`).not.toBeNull();
  expect(a.x, `${what} starts left of its column`).toBeGreaterThanOrEqual(
    b.x - 1,
  );
  expect(a.x + a.width, `${what} runs past its column`).toBeLessThanOrEqual(
    b.x + b.width + 1,
  );
}

test("A task's hostd pin is edited on its page", async ({ page }) => {
  test.setTimeout(90_000);
  // Task 9 is the demo's deferred task. The demo is worker-scoped, so the
  // pin comes off again whatever happens.
  try {
    for (const [viewport, pin] of [
      [DESKTOP, "gpu=yes"],
      [PHONE, "build-runner"],
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/tasks/9");
      const section = page.getByTestId("hostd-section");
      const column = section.locator("xpath=..");
      await expectInside(
        section,
        column,
        `hostd section at ${viewport.width}px`,
      );

      await section
        .getByRole("button", { name: /Pin to a hostd|Change hostd/ })
        .click();
      const input = section.getByTestId("hostd-input");
      await expect(input).toBeVisible();
      await expectInside(
        section,
        column,
        `open hostd editor at ${viewport.width}px`,
      );
      const save = section.getByRole("button", { name: "Save hostd" });
      const cancel = section.getByRole("button", { name: "Cancel" });
      // The two actions share a row.
      expect(Math.round((await save.boundingBox()).y)).toBe(
        Math.round((await cancel.boundingBox()).y),
      );

      await input.fill(pin);
      const [put] = await Promise.all([
        page.waitForRequest(
          (r) => r.method() === "PUT" && r.url().endsWith("/api/tasks/9/hostd"),
        ),
        save.click(),
      ]);
      expect(put.postDataJSON()).toEqual({ hostd: pin });
      await expect(section.getByTestId("hostd-summary")).toContainText(pin);
      await expect(
        section.getByRole("button", { name: "Use default hostd" }),
      ).toBeVisible();
    }
  } finally {
    await page.request.put("/api/tasks/9/hostd", { data: { hostd: "" } });
  }
});
