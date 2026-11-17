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

// What the 2026-11-16 Processes walkthrough
// (docs/test-runs/2026-11-16-recurring-composed-ui.md) found on /next/:
// a schedule's Configuration pane printed its next and last runs as the
// wire's nanosecond RFC 3339 strings while its row formatted them, and the
// suite dialog saved a typed 0 passes as the default 3 (or 1) without a
// word. Rows also counted "1 templates" and "1 tasks".

import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-schedules", "-github-sim", "-fake-ci"] });
test.describe.configure({ mode: "serial" });

// An RFC 3339 instant as the server sends it: what a person should never
// be shown in place of a date.
const WIRE_TIME = /\d{4}-\d\d-\d\dT\d\d:\d\d/;

test("a schedule's Configuration pane formats its next and last runs", async ({
  page,
}) => {
  await page.goto("processes/schedules");
  const row = page.locator('[data-testid^="sched-row-"]', {
    hasText: "Weekly Dependency & Vulnerability Audit",
  });
  await row.click();
  const inspector = page.getByTestId("proc-inspector");
  const next = inspector.getByTestId("sched-next-run");
  await expect(next).toBeVisible();
  await expect(next).not.toHaveText(WIRE_TIME);
  // The pane and the row say the same time.
  const rowNext = (await row.innerText()).match(/next: (.*)/)?.[1]?.trim();
  expect(rowNext).toBeTruthy();
  await expect(next).toHaveText(rowNext!);

  await inspector.getByRole("button", { name: "Run now" }).click();
  const last = inspector.getByTestId("sched-last-run");
  await expect(last).toBeVisible();
  await expect(last).not.toHaveText(WIRE_TIME);
  await expect(last).toContainText("2026");

  // Persisted: the schedule's own link, loaded afresh, says the same.
  const id = (await row.getAttribute("data-testid"))!.slice(
    "sched-row-".length,
  );
  await page.goto(`processes/schedules/${encodeURIComponent(id)}`);
  await expect(inspector.getByTestId("sched-last-run")).toContainText("2026");
  await expect(inspector.getByTestId("sched-last-run")).not.toHaveText(
    WIRE_TIME,
  );
});

test("the suite dialog refuses a pass count it would otherwise replace", async ({
  page,
}) => {
  const name = `Throwaway pass-limit suite ${Date.now()}`;
  await page.goto("processes/suites");
  await page.getByTestId("proc-new-menu-btn").click();
  await page.getByTestId("proc-new-suite").click();
  const dialog = page.getByTestId("suite-dialog");
  await dialog.getByLabel("Suite name").fill(name);
  await dialog.getByRole("button", { name: "Until clean" }).click();
  const passes = dialog.getByTestId("suite-max-passes-input");

  for (const typed of ["0", "", "2.5", "101"]) {
    await passes.fill(typed);
    await dialog.getByRole("button", { name: "Save suite" }).click();
    await expect(dialog.getByTestId("proc-dialog-error")).toHaveText(
      "Maximum passes must be a whole number from 1 to 100.",
    );
    await expect(dialog).toBeVisible();
    // The field keeps what was typed rather than swapping in a default.
    await expect(passes).toHaveValue(typed);
  }
  const before = await (await page.request.get("/api/suites")).json();
  expect(before.some((s: { name: string }) => s.name === name)).toBe(false);

  await passes.fill("2");
  await dialog.getByRole("button", { name: "Save suite" }).click();
  await expect(dialog).toBeHidden();
  const saved = (await (await page.request.get("/api/suites")).json()).find(
    (s: { name: string }) => s.name === name,
  );
  expect(saved).toMatchObject({ mode: "until_clean", maxPasses: 2 });

  // One member template is "1 template", not "1 templates".
  const row = page.locator('[data-testid^="suite-row-"]', { hasText: name });
  await expect(row).toContainText("1 template");
  await expect(row).not.toContainText("1 templates");

  // Run it once: its run row counts "1 task".
  await row.click();
  const inspector = page.getByTestId("proc-inspector");
  await inspector.getByRole("button", { name: "Run suite" }).click();
  await page
    .getByTestId("run-suite-dialog")
    .getByRole("button", { name: "Start suite run" })
    .click();
  const runRow = page.locator('[data-testid^="suite-run-row-"]', {
    hasText: name,
  });
  await expect(runRow).toContainText("· 1 task");
  await expect(runRow).not.toContainText("1 tasks");

  // Clean up through the UI's own confirmation.
  await page.getByRole("button", { name: /^Suites \(/ }).click();
  await row.click();
  await inspector.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete suite" }).click();
  await expect(row).toHaveCount(0);
});
