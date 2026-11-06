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

// What the 2026-11-04 UI walkthrough (docs/test-runs/2026-11-04-ui.md)
// found by keyboard and axe, kept from coming back:
//
//   - Enter on an open SearchSelect list that matches nothing submitted
//     the form around it: a Schedule was filed from a Template picker
//     reading "No matches".
//   - A multiple SearchSelect's open list put a real, unlabelled
//     checkbox inside every role="option" (axe: label, nested-interactive).
//   - A finished report's reason chip read "question" beside its
//     "Answered in a comment" badge, in the list and the Inbox.

import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures.js";

for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  test(`Enter on a Schedule picker reading "No matches" files nothing (${width}px)`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/schedules");
    await page.getByRole("button", { name: "+ New schedule" }).click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("heading", { name: "New schedule" }),
    ).toBeVisible();
    const writes = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        new URL(req.url()).pathname === "/api/schedules"
      ) {
        writes.push(req.url());
      }
    });
    const template = dialog.getByRole("combobox", { name: /^Template/ });
    await template.focus();
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.type("zzqxv");
    await expect(page.getByText("No matches")).toBeVisible();
    await page.keyboard.press("Enter");
    // Nothing filed, the form still up, and the list still the box's.
    await page.waitForTimeout(500);
    expect(writes).toEqual([]);
    await expect(dialog).toBeVisible();
    // Escape closes the list and leaves the drawer where it is.
    await page.keyboard.press("Escape");
    await expect(page.getByText("No matches")).toBeHidden();
    await expect(dialog).toBeVisible();
  });

  test(`a multiple SearchSelect's open list is axe-clean and ticks in front of each label (${width}px)`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    const filter = page.getByRole("button", { name: /^Filter/ }).first();
    if (await filter.isVisible()) await filter.click();
    await page
      .getByRole("button", { name: /^(Filter by…|Add filter)$/ })
      .click();
    await page.getByRole("menuitem", { name: "State" }).click();
    const values = page.getByLabel(/^Value for filter \d+$/).last();
    await values.focus();
    await page.keyboard.press("ArrowDown");
    const id = await values.getAttribute("aria-controls");
    const list = page.locator(`[id="${id}"]`);
    const rows = list.getByRole("option");
    await expect(rows.first()).toBeVisible();
    // Pick one by keyboard: the row says so through aria-selected and
    // its drawn tick, and no control nests inside it.
    await page.keyboard.press("Enter");
    await expect(rows.first()).toHaveAttribute("aria-selected", "true");
    await expect(rows.first().locator(".search-select-check")).toHaveAttribute(
      "data-checked",
      "true",
    );
    await expect(list.locator("input")).toHaveCount(0);

    const axe = await new AxeBuilder({ page })
      .include(`[id="${id}"]`)
      .analyze();
    expect(axe.violations.map((v) => v.id)).toEqual([]);

    const lb = await list.boundingBox();
    expect(lb.x).toBeGreaterThanOrEqual(0);
    expect(lb.x + lb.width).toBeLessThanOrEqual(width + 0.5);
    const n = Math.min(await rows.count(), 4);
    for (let i = 0; i < n; i++) {
      const r = await rows.nth(i).boundingBox();
      const tick = await rows
        .nth(i)
        .locator(".search-select-check")
        .boundingBox();
      const text = await rows
        .nth(i)
        .locator(".MuiListItemText-primary")
        .boundingBox();
      for (const b of [tick, text]) {
        expect(b.x).toBeGreaterThanOrEqual(r.x - 0.5);
        expect(b.x + b.width).toBeLessThanOrEqual(r.x + r.width + 0.5);
        expect(b.y).toBeGreaterThanOrEqual(r.y - 0.5);
        expect(b.y + b.height).toBeLessThanOrEqual(r.y + r.height + 0.5);
      }
      expect(tick.x + tick.width).toBeLessThanOrEqual(text.x + 0.5);
      // One centreline for the tick and its label.
      expect(
        Math.abs(tick.y + tick.height / 2 - (text.y + text.height / 2)),
      ).toBeLessThanOrEqual(3);
    }
    await page.keyboard.press("Escape");
  });
}

test("a finished report's reason chip says it answered, and a parked question's still says question", async ({
  page,
}) => {
  const res = await page.request.get("/api/tasks");
  const live = await res.json();
  const asked = live.filter(
    (t) => t.state === "awaiting_reply" && !t.interactive,
  );
  const full = await Promise.all(
    asked.map(async (t) =>
      (await page.request.get(`/api/tasks/${t.id}`)).json(),
    ),
  );
  const answer = full.find(
    (t) =>
      !t.questionSummary &&
      !t.pendingSecret &&
      !t.awaitingAccess &&
      !t.turnStopped,
  );
  const question = full.find((t) => t.questionSummary);
  expect(
    answer,
    "the demo seeds a run that answered in a comment",
  ).toBeTruthy();
  expect(question, "the demo seeds a parked question").toBeTruthy();

  await page.goto("/");
  const answerRow = page
    .locator(".task-row", { hasText: answer.title })
    .first();
  const questionRow = page
    .locator(".task-row", { hasText: question.title })
    .first();
  await expect(
    answerRow.getByTitle("Answered in a comment").first(),
  ).toBeVisible();
  await expect(answerRow.locator(".task-reason")).toHaveText("answered");
  await expect(
    questionRow.getByTitle("Asked a question").first(),
  ).toBeVisible();
  await expect(questionRow.locator(".task-reason")).toHaveText("question");

  await page.goto("/inbox");
  const main = page.locator("main");
  await expect(main.getByText("Answered in a comment").first()).toBeVisible();
  await expect(main.getByText("Asked a question").first()).toBeVisible();
  const inboxAnswer = main
    .locator(".task-row", { hasText: answer.title })
    .first();
  if (await inboxAnswer.count()) {
    await expect(inboxAnswer.locator(".task-reason")).toHaveText("answered");
  }
});
