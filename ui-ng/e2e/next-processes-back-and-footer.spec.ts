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

// Found in the 2026-11-20 Processes walkthrough
// (docs/test-runs/2026-11-20-recurring-composed-ui.md):
//
//   - Bug 2: cancelling the unsaved-edit question that Back raises put the
//     address bar back but left the browser's history one entry behind,
//     so the next Back skipped the page it should have reached -- and on
//     the first page of a visit tried to leave the app altogether.
//   - Bug 3: at phone width the template edit dialog's footer wrapped to
//     two rows, Save template alone on the second.
//   - Bug 4: the header New menu, opened from the keyboard, kept focus on
//     New, and the arrow keys did nothing.

import { expect, test } from "./fixtures";

test("Back asks again after a cancelled unsaved-edit question, and lands on the previous page", async ({
  page,
}) => {
  const discard = page.getByRole("dialog", {
    name: "Discard unsaved changes?",
  });
  const dlg = page.getByTestId("schedule-dialog");

  // A full load of sched-1, then an in-app step to sched-2: Back from
  // sched-2 belongs to the app, and the entry before sched-1 is outside it.
  await page.goto("processes/schedules/sched-1");
  await page.getByTestId("sched-row-sched-2").click();
  await expect(page).toHaveURL(/\/processes\/schedules\/sched-2$/);

  await page.getByTestId("sched-edit-btn").click();
  await dlg.getByTestId("sched-base-input").fill("walk-back");

  await page.evaluate(() => history.back());
  await expect(discard).toBeVisible();
  await discard.getByRole("button", { name: "Cancel" }).click();
  await expect(discard).toHaveCount(0);
  await expect(page).toHaveURL(/\/processes\/schedules\/sched-2$/);
  await expect(dlg.getByTestId("sched-base-input")).toHaveValue("walk-back");

  // The second Back is the same request as the first: it asks again
  // rather than skipping sched-1 or leaving the app.
  let leftApp = false;
  page.on("dialog", (d) => {
    leftApp = true;
    void d.dismiss();
  });
  await page.evaluate(() => history.back());
  await expect(discard).toBeVisible();
  await discard.getByRole("button", { name: "Discard" }).click();
  await expect(page).toHaveURL(/\/processes\/schedules\/sched-1$/);
  await expect(dlg).toBeHidden();
  expect(leftApp).toBe(false);
});

test("the template edit dialog's footer stays on one row at phone width", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("processes/templates/template-1");
  await page.getByTestId("tmpl-edit-btn").click();
  const tmplDlg = page.getByTestId("template-dialog");
  await expect(tmplDlg).toBeVisible();

  const newTask = tmplDlg.getByTestId("tmpl-dialog-new-task-btn");
  // Its accessible name is the whole phrase whatever it shows.
  await expect(
    tmplDlg.getByRole("button", { name: "New task from this template" }),
  ).toBeVisible();
  const boxes = await Promise.all([
    newTask.boundingBox(),
    tmplDlg.getByRole("button", { name: "Cancel" }).boundingBox(),
    tmplDlg.getByTestId("tmpl-save-btn").boundingBox(),
  ]);
  const [start, cancel, save] = boxes.map((b) => b!);
  const centre = (b: { y: number; height: number }) => b.y + b.height / 2;
  // One row: every button's middle within a few pixels of the others'.
  expect(Math.abs(centre(start) - centre(save))).toBeLessThan(6);
  expect(Math.abs(centre(cancel) - centre(save))).toBeLessThan(6);
  // Apart: New task sits clear to the left of Cancel.
  expect(start.x + start.width).toBeLessThan(cancel.x);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});

test("the header New menu opened from the keyboard moves with the arrow keys and gives focus back on Escape", async ({
  page,
}) => {
  await page.goto("processes/schedules");
  const newBtn = page.getByTestId("proc-new-menu-btn");
  await newBtn.focus();
  await page.keyboard.press("Enter");
  const menu = page.getByTestId("proc-new-menu");
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "Schedule" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(menu.getByRole("menuitem", { name: "Template" })).toBeFocused();
  await page.keyboard.press("End");
  await expect(menu.getByRole("menuitem", { name: "Suite" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(menu.getByRole("menuitem", { name: "Schedule" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu).toBeHidden();
  await expect(newBtn).toBeFocused();

  // Enter on an item opens that kind's form.
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("menuitem", { name: "Schedule" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(menu.getByRole("menuitem", { name: "Template" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/processes\/templates/);
  await expect(page.getByTestId("template-dialog")).toBeVisible();
});
