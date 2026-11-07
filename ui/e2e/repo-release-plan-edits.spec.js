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

// docs/test-runs/2026-11-07-repository-release-memory-ui.md (task 3160),
// bug 1: on /repos/acme/widgets/releases the qualification plan form
// kept a blank row and a cleared Repeat on screen after Save, though the
// server stored neither, and "← acme/widgets" dropped an unsaved plan
// edit without asking. Against the real demo server: the form shows
// what a reload shows, and leaving with an edit asks first.
test.use({ demoArgs: ["-github-sim", "-qualification"] });

const ROWS = ".qualification-item-row";

async function noOverflow(page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
}

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the qualification plan shows what was saved and asks before an unsaved edit is left, on a ${name} screen`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);
    await page.goto("/repos/acme/widgets/releases");
    const save = page.getByRole("button", { name: "Save qualification plan" });
    await expect(save).toBeEnabled();
    const before = await page.locator(ROWS).count();

    // The demo's plan already names its one template (Qualify), and a
    // template may appear once, so the edit is to that row: Repeat
    // cleared, and a blank row added beside it.
    expect(before).toBeGreaterThan(0);
    const first = page.locator(ROWS).first();
    const original = await first.getByLabel("Repeat").inputValue();
    await first.getByLabel("Repeat").fill("");
    await page.getByRole("button", { name: "Add item" }).click();
    await expect(page.locator(ROWS)).toHaveCount(before + 1);

    // Leaving asks; told no, the page and the edit stay.
    let asked = "";
    page.once("dialog", (d) => {
      asked = d.message();
      d.dismiss();
    });
    await page.getByRole("button", { name: /← acme\/widgets/ }).click();
    expect(asked).toMatch(/unsaved/i);
    await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases/);
    await expect(page.locator(ROWS)).toHaveCount(before + 1);

    const put = page.waitForResponse(
      (r) =>
        r.url().endsWith("/qualification-plan") &&
        r.request().method() === "PUT",
    );
    await save.click();
    expect((await put).ok()).toBe(true);

    // The form snaps to the saved plan: the blank row gone, Repeat 1.
    await expect(page.locator(ROWS)).toHaveCount(before);
    await expect(first.getByLabel("Repeat")).toHaveValue("1");
    await noOverflow(page);

    await page.reload();
    await expect(page.locator(ROWS)).toHaveCount(before);
    await expect(first.getByLabel("Repeat")).toHaveValue("1");

    // Clean again: leaving does not ask.
    let askedAgain = false;
    page.once("dialog", (d) => {
      askedAgain = true;
      d.accept();
    });
    await page.getByRole("button", { name: /← acme\/widgets/ }).click();
    await expect(page).toHaveURL(
      /\/repos\/acme\/widgets(\/settings\/[a-z-]+)?$/,
    );
    expect(askedAgain).toBe(false);

    // Put the plan back as it was for the next viewport.
    await page.goto("/repos/acme/widgets/releases");
    await first.getByLabel("Repeat").fill(original);
    await page.getByRole("button", { name: "Save qualification plan" }).click();
    await page.reload();
    await expect(first.getByLabel("Repeat")).toHaveValue(original);
    expect(errors).toEqual([]);
  });
}
