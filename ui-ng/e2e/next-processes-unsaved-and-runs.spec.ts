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

// Found in the 2026-11-17 Processes walkthrough
// (docs/test-runs/2026-11-17-recurring-composed-ui.md): the schedule,
// template and suite dialogs dropped an unsaved edit on Escape, Cancel,
// the scrim and Back without asking, where the classic panes ask first.
// The second test pins how a suite run with no base of its own reads.

import { expect, test } from "./fixtures";

test("an unsaved Processes dialog edit asks once before it is dropped, an unchanged one closes at once", async ({
  page,
  pageErrors,
}) => {
  const discard = page.getByRole("dialog", {
    name: "Discard unsaved changes?",
  });

  // Reached by the rail, so Back below is the app's own navigation.
  await page.goto("inbox");
  await page
    .getByRole("link", { name: /Processes/ })
    .first()
    .click();
  await expect(page).toHaveURL(/\/next\/processes/);
  // sched-2 is an inline schedule whose base branch is editable (sched-1
  // fires a bound template, so its repository and base are read-only).
  await page.getByTestId("sched-row-sched-2").click();

  const dlg = page.getByTestId("schedule-dialog");
  await page.getByTestId("sched-edit-btn").click();
  await expect(dlg).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dlg).toBeHidden();
  await expect(discard).toHaveCount(0);

  await page.getByTestId("sched-edit-btn").click();
  await dlg.getByTestId("sched-base-input").fill("walk-edit");
  await page.keyboard.press("Escape");
  await expect(discard).toBeVisible();
  await expect(discard).toContainText(
    "You have unsaved changes to this schedule.",
  );
  await discard.getByRole("button", { name: "Cancel" }).click();
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("sched-base-input")).toHaveValue("walk-edit");

  await dlg.getByRole("button", { name: "Cancel" }).click();
  await expect(discard).toHaveCount(1);
  await discard.getByRole("button", { name: "Cancel" }).click();
  await expect(dlg).toBeVisible();

  await page.goBack();
  await expect(discard).toHaveCount(1);
  await discard.getByRole("button", { name: "Discard" }).click();
  await expect(discard).toHaveCount(0);
  await expect(dlg).toBeHidden();

  // Nothing was saved.
  const sched = (
    (await (await page.request.get("/api/schedules")).json()) as Array<{
      id: string;
      base?: string;
    }>
  ).find((s) => s.id === "sched-2");
  expect(sched?.base).not.toBe("walk-edit");

  // Templates and suites: unchanged closes at once, changed asks.
  await page.goto("processes/templates/template-2");
  await page.getByTestId("tmpl-edit-btn").click();
  const tmpl = page.getByTestId("template-dialog");
  await expect(tmpl).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(tmpl).toBeHidden();
  await expect(discard).toHaveCount(0);
  await page.getByTestId("tmpl-edit-btn").click();
  await tmpl.getByTestId("tmpl-title-input").fill("walk edited title");
  await page.keyboard.press("Escape");
  await expect(discard).toContainText("unsaved changes to this template");
  await discard.getByRole("button", { name: "Discard" }).click();
  await expect(tmpl).toBeHidden();

  await page.goto("processes/suites/suite-1");
  await page.getByTestId("suite-edit-btn").click();
  const suite = page.getByTestId("suite-dialog");
  await expect(suite).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(suite).toBeHidden();
  await expect(discard).toHaveCount(0);
  await page.getByTestId("suite-edit-btn").click();
  await suite.getByTestId("suite-name-input").fill("walk edited suite");
  await page.mouse.click(4, 4);
  await expect(discard).toContainText("unsaved changes to this suite");
  await discard.getByRole("button", { name: "Discard" }).click();
  await expect(suite).toBeHidden();

  // A save closes without asking.
  await page.getByTestId("suite-edit-btn").click();
  await suite.getByTestId("suite-save-btn").click();
  await expect(suite).toBeHidden();
  await expect(discard).toHaveCount(0);

  expect(pageErrors).toEqual([]);
});

test("a suite run with no base reads 'default branch' on its row and pane, its status chip whole @phone", async ({
  page,
  pageErrors,
}) => {
  const res = await page.request.post("/api/suite-runs", {
    data: { suiteId: "suite-1", repo: "acme/widgets", base: "main" },
  });
  expect(res.ok(), await res.text()).toBe(true);
  const run = (await res.json()) as { id: number };

  // The server now fills in a run's base when it is started (or refuses
  // one it has no default branch for), so only a run recorded before
  // that carries an empty base. Blank it in the server's real answer to
  // get that state.
  await page.route(/\/api\/suite-runs(\/\d+)?(\?.*)?$/, async (route) => {
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const real = await route.fetch({ headers });
    const body = await real.json();
    const blank = (r: { id: number; base?: string }) =>
      r.id === run.id ? { ...r, base: "" } : r;
    await route.fulfill({
      response: real,
      json: Array.isArray(body) ? body.map(blank) : blank(body),
    });
  });

  await page.goto(`processes/suites/runs/${run.id}`);
  const pane = page.getByTestId("suite-run-inspector");
  await expect(pane).toContainText("Target: acme/widgets @ default branch");

  await page.goto("processes/suites");
  await page.getByTestId("suites-subview-runs").click();
  const row = page.getByTestId(`suite-run-row-${run.id}`);
  await expect(row).toContainText("acme/widgets @ default branch");
  const pill = row.locator(".status-pill");
  await expect(pill).toBeVisible();
  const fits = await pill.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const rowBox = el.closest(".proc-row")!.getBoundingClientRect();
    return {
      whole: el.scrollWidth <= el.clientWidth + 1,
      inside: r.left >= rowBox.left - 1 && r.right <= rowBox.right + 1,
      oneLine: r.height < 40,
    };
  });
  expect(fits).toEqual({ whole: true, inside: true, oneLine: true });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(page.viewportSize()!.width);

  expect(pageErrors).toEqual([]);
});
