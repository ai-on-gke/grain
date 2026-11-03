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

import { test, expect } from "./fixtures.js";

// Regressions found by the 2026-10-01 deployment-operations walkthrough
// (docs/test-runs/2026-10-01-deployment-operations.md), each driven
// through the real demo server and built frontend:
//
//   - System's open tab is in the address bar, so a reload and Back keep
//     it rather than falling back to Health;
//   - a task opened from the Metrics report is one history step, so Back
//     returns to the report rather than to the bare task list;
//   - re-testing a capability's credential drops the previous verdict, so
//     a check that never gets an answer does not leave an earlier
//     "Credential works" standing beside the error.

test("keeps the System tab across a direct link, a reload and Back", async ({
  page,
}) => {
  // The old /admin/logs is redirected, and the new path is what is kept.
  await page.goto("/admin/logs");
  await expect(
    page.getByRole("tab", { name: "Logs", selected: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/system\/logs$/);

  await page.getByRole("tab", { name: "Pipeline" }).click();
  await expect(page).toHaveURL(/\/system\/pipeline$/);
  await page.reload();
  await expect(
    page.getByRole("tab", { name: "Pipeline", selected: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/system\/pipeline$/);

  await page.goBack();
  await expect(
    page.getByRole("tab", { name: "Logs", selected: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/system\/logs$/);

  // Health is System's default and keeps the bare path.
  await page.getByRole("tab", { name: "Health" }).click();
  await expect(page).toHaveURL(/\/system$/);
});

test("returns to Metrics on Back from the oldest queued task", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByTestId("top-bar-system").click();
  await page.getByRole("tab", { name: "Metrics" }).click();
  await expect(page).toHaveURL(/\/system\/metrics$/);

  await page
    .getByRole("link", { name: /^task \d+$/ })
    .first()
    .click();
  await expect(page).toHaveURL(/\/tasks\/\d+$/);

  await page.goBack();
  await expect(page).toHaveURL(/\/system\/metrics$/);
  await expect(
    page.getByRole("heading", { name: "Metrics", exact: true }),
  ).toBeVisible();
});

test("drops a credential verdict when the re-check gets no answer", async ({
  page,
}) => {
  // GET /api/settings is the real answer: the demo wires a capability
  // checker (demoCapabilityChecks in pkg/demo/demo_scenarios.go), so
  // gemini-key is checkable. The check itself is intercepted. Its verdict
  // is the answer from Google, which issued the key, and the demo has no
  // key to send. That external boundary is the one thing stubbed here:
  // ok first, then a 502.
  let checks = 0;
  await page.route("**/api/capabilities/gemini-key/check", (route) => {
    checks += 1;
    return checks === 1
      ? route.fulfill({
          json: {
            id: "gemini-key",
            ok: true,
            detail: "The key was accepted (intercepted).",
            checkedAt: new Date().toISOString(),
          },
        })
      : route.fulfill({
          status: 502,
          json: { error: "the issuing service did not answer (intercepted)" },
        });
  });

  await page.goto("/admin/integrations");
  await page.getByRole("button", { name: /^Gemini key, / }).click();
  const testButton = page.getByRole("button", { name: "Test credential" });

  await testButton.click();
  await expect(page.getByText("Credential works")).toBeVisible();

  await testButton.click();
  await expect(
    page.getByText("the issuing service did not answer (intercepted)"),
  ).toBeVisible();
  await expect(page.getByText("Credential works")).toHaveCount(0);
});

// Regressions found by the 2026-10-04 walkthrough
// (docs/test-runs/2026-10-04-deployment-operations.md).

// A link naming no Admin tab used to open a blank pane with no tab
// selected; it opens Settings → General now, and a tab typed in another
// case is still that tab.
test("opens General for an Admin link naming no tab", async ({ page }) => {
  await page.goto("/admin/nonsense");
  await expect(
    page.getByRole("heading", { name: "General", level: 1 }),
  ).toBeVisible();
  await expect(page.getByLabel("Environment name")).toBeVisible();

  await page.goto("/admin/LOGS");
  await expect(
    page.getByRole("tab", { name: "Logs", selected: true }),
  ).toBeVisible();
});

// The Metrics report's sections were paragraphs, so heading navigation
// reached the release notes and nothing after them.
test("titles each Metrics section with a heading", async ({ page }) => {
  await page.goto("/metrics");
  for (const name of ["Throughput", "Latency", "Capacity", "Backlog"]) {
    await expect(page.getByRole("heading", { level: 3, name })).toBeVisible();
  }
});

// 2026-10-06 walkthrough: Team settings -> Models & connections -> edit
// saves only on "Save connection", and Escape, "← Models & connections"
// and Back used to drop an edit without asking. Back is the browser's
// own, within the app's history, so it goes through App's popstate guard;
// accepting it leaves the editor along with the Models & connections tab.
test("asks before leaving an edited connection, and leaves it when told to", async ({
  page,
}) => {
  const prompts = [];
  let accept = false;
  page.on("dialog", (dialog) => {
    prompts.push(dialog.message());
    return accept ? dialog.accept() : dialog.dismiss();
  });

  await page.goto("/settings/general");
  await page.getByTestId("settings-nav-models").click();
  await expect(page).toHaveURL(/\/settings\/models$/);
  await page.getByRole("button", { name: "Edit", exact: true }).first().click();
  const save = page.getByRole("button", { name: "Save connection" });
  await expect(page.getByText("No unsaved changes")).toBeVisible();

  const concurrency = page.getByLabel("Concurrency", { exact: true });
  await concurrency.fill("3");
  await expect(save).toBeEnabled();

  await page.locator(".overlay-pane-back button").last().click();
  expect(prompts).toEqual([
    "You have unsaved changes here. Leave and discard them?",
  ]);
  await expect(concurrency).toHaveValue("3");

  await page.goBack();
  await expect.poll(() => prompts.length).toBe(2);
  await expect(concurrency).toHaveValue("3");
  await expect(page).toHaveURL(/\/settings\/models$/);

  accept = true;
  await page.goBack();
  await expect.poll(() => prompts.length).toBe(3);
  await expect(save).toHaveCount(0);
  await expect(page).toHaveURL(/\/settings\/general$/);
  await expect(
    page.getByRole("heading", { name: "General", level: 1 }),
  ).toBeVisible();
});

// 2026-10-06 walkthrough: at 700 px, every other ArrowRight along the
// Admin strip left the focused tab mostly past the right edge, because
// MUI scrolls only the selected tab into view.
test("keeps the keyboard-focused System tab in view on a narrow screen", async ({
  page,
}) => {
  await page.setViewportSize({ width: 700, height: 800 });
  await page.goto("/system");
  const strip = page.getByRole("tablist", { name: "System tabs" });
  const tabs = strip.getByRole("tab");
  const count = await tabs.count();
  await page.getByRole("tab", { name: "Health" }).focus();
  for (let i = 1; i < count; i++) {
    await page.keyboard.press("ArrowRight");
    const focused = tabs.nth(i);
    await expect(focused).toBeFocused();
    await expect
      .poll(() =>
        focused.evaluate((el) => {
          const tab = el.getBoundingClientRect();
          const view = el.closest(".MuiTabs-scroller").getBoundingClientRect();
          return tab.left >= view.left - 1 && tab.right <= view.right + 1;
        }),
      )
      .toBe(true);
  }
});

// 2026-10-07 standing-work walkthrough
// (docs/test-runs/2026-10-07-standing-work-settings-forms.md).

// The Integrations capability sub-tabs were a scrolling strip, walked
// here with the keyboard. They are a list of full-width rows now: at
// 700 px and at a phone's 375 px every row fits inside the viewport, and
// a row reached with Tab opens its capability's page on Enter.
for (const width of [700, 375]) {
  test(`fits the legacy capability list and opens a row from the keyboard at ${width} px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/admin/integrations");
    const rows = page
      .getByRole("list", { name: "Legacy capabilities" })
      .locator("[data-legacy-capability-row]");
    await expect(rows.first()).toBeVisible();
    for (const row of await rows.all()) {
      const box = await row.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width + 1);
    }
    const id = await rows.first().getAttribute("data-legacy-capability-row");
    await rows.first().focus();
    await page.keyboard.press("Enter");
    await expect(
      page.locator(`[data-legacy-capability="${id}"]`),
    ).toBeVisible();
    await page.getByTestId("legacy-capabilities-back").click();
    await expect(rows.first()).toBeVisible();
  });
}

// Board → Columns closed on Escape and on Back and dropped the edit.
test("asks before leaving an edited board layout", async ({ page }) => {
  const prompts = [];
  page.on("dialog", (dialog) => {
    prompts.push(dialog.message());
    return dialog.dismiss();
  });
  await page.goto("/boards");
  await page.locator("a[href^='/boards/']").first().click();
  await page.getByRole("button", { name: "Columns", exact: true }).click();
  const title = page.getByLabel("Column 1 title");
  await title.fill("Edited in the walkthrough");

  await page.keyboard.press("Escape");
  expect(prompts).toEqual([
    "You have unsaved changes here. Leave and discard them?",
  ]);
  await expect(title).toHaveValue("Edited in the walkthrough");

  await page.goBack();
  await expect.poll(() => prompts.length).toBe(2);
  await expect(title).toHaveValue("Edited in the walkthrough");
});

// Task pane Edit and Edit fields dropped an edit silently on Escape, ← Back and browser Back.
test("asks before leaving an edited task or custom fields, and leaves when told to", async ({
  page,
}) => {
  const prompts = [];
  let accept = false;
  page.on("dialog", (dialog) => {
    prompts.push(dialog.message());
    return accept ? dialog.accept() : dialog.dismiss();
  });

  // 1. Task Edit: /tasks/2 -> Edit -> change Title
  await page.goto("/");
  await page
    .locator(".task-row")
    .filter({ hasText: "Fix flaky retry test" })
    .first()
    .click();
  await expect(page).toHaveURL(/\/tasks\/2$/);
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const title = page.getByLabel("Title");
  await title.fill("Changed task title");

  // Escape prompts and keeps the dialog and input when dismissed
  await page.keyboard.press("Escape");
  expect(prompts).toEqual([
    "You have unsaved changes here. Leave and discard them?",
  ]);
  await expect(title).toHaveValue("Changed task title");

  // ← Back prompts and keeps the edit when dismissed
  await page.getByRole("button", { name: /Back/ }).click();
  await expect.poll(() => prompts.length).toBe(2);
  await expect(title).toHaveValue("Changed task title");

  // Browser Back prompts and keeps the edit when dismissed
  await page.goBack();
  await expect.poll(() => prompts.length).toBe(3);
  await expect(title).toHaveValue("Changed task title");

  // Accept discard on Back: pane closes and URL leaves /tasks/2
  accept = true;
  await page.getByRole("button", { name: /Back/ }).click();
  await expect.poll(() => prompts.length).toBe(4);
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // 2. Custom fields: /tasks/1 -> Edit fields -> change Area
  accept = false;
  await page
    .locator(".task-row")
    .filter({ hasText: "Add a dark mode toggle" })
    .first()
    .click();
  await expect(page).toHaveURL(/\/tasks\/1$/);
  await page.getByRole("button", { name: "Edit fields", exact: true }).click();
  await page.getByLabel("Area").click();
  await page.getByRole("option", { name: "infra" }).click();

  // Escape prompts and keeps the edit when dismissed
  await page.keyboard.press("Escape");
  await expect.poll(() => prompts.length).toBe(5);
  await expect(page.getByRole("dialog")).toBeVisible();

  // ← Back prompts and keeps the edit when dismissed
  await page.getByRole("button", { name: /Back/ }).click();
  await expect.poll(() => prompts.length).toBe(6);
  await expect(page.getByRole("dialog")).toBeVisible();

  // Browser Back prompts and keeps the edit when dismissed
  await page.goBack();
  await expect.poll(() => prompts.length).toBe(7);
  await expect(page.getByRole("dialog")).toBeVisible();

  // Accept discard on Back: pane closes
  accept = true;
  await page.getByRole("button", { name: /Back/ }).click();
  await expect.poll(() => prompts.length).toBe(8);
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

// A refused template delete names every dependent, and with 25 of them
// the error banner was taller than a 375 px screen: the line saying
// what was refused sat above the top edge.
test("keeps a refusal naming 25 dependents on a phone's screen", async ({
  page,
}) => {
  const stamp = Date.now();
  const made = async (path, data) => {
    const res = await page.request.post(path, { data });
    expect(res.ok(), `POST ${path}`).toBe(true);
    return res.json();
  };
  const tmpl = await made("/api/templates", {
    name: `held-${stamp}`,
    title: `Held ${stamp}`,
  });
  const suites = [];
  for (let i = 1; i <= 25; i++) {
    suites.push(
      await made("/api/suites", {
        name: `dependent suite number ${i} with a long name ${stamp}`,
        templateIds: [tmpl.id],
        mode: "count",
        count: 1,
      }),
    );
  }

  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto(`/templates/${tmpl.id}`);
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Delete" }).click();
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("template is used by 25 suite(s)");
  const box = await alert.boundingBox();
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.y + box.height).toBeLessThanOrEqual(800);
  expect(box.height).toBeLessThan(800 / 2 + 60);

  for (const s of suites) await page.request.delete(`/api/suites/${s.id}`);
  await page.request.delete(`/api/templates/${tmpl.id}`);
});
