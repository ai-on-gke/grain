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

// A keyboard-only walk through every list page whose rows open an item
// (grain/task-1429): the task list, Boards, Templates, Suites, Schedules
// and Repos. Each row used to open only on a pointer click on the row
// itself -- Tab reached the move handle ("Move …", whose Enter picks the
// row up for reordering), the task list's checkbox and the "Use…"/"Run…"
// buttons, and never anything that opened the item; on /repos it reached
// nothing in a row at all. The item's name is a link to its own deep link
// now (ListPrimitives.jsx's RowLink), and this walks to one on each page
// with Tab alone and opens it with Enter.
//
// It runs in WebKit as well as Chromium (playwright.config.js): whether
// Tab stops on a link at all is the browser's own call, and Safari's is
// not Chromium's.

test.setTimeout(120_000);

const seeded = [];

async function seed(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  const body = await res.json();
  seeded.push(`${path}/${body.id}`);
  return body;
}

test.afterEach(async ({ page }) => {
  while (seeded.length > 0) await page.request.delete(seeded.pop());
});

// tabTo presses Tab from wherever focus is until `target` has it, the
// way somebody with no pointer gets there, and fails rather than spin if
// it never does -- which is exactly the bug: no number of Tabs reached a
// row's way in.
async function tabTo(page, target, limit = 250) {
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press("Tab");
    if (await target.evaluate((el) => el === document.activeElement)) return;
  }
  throw new Error(`Tab never reached ${target}`);
}

const escape = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// open walks to the first row link in `list` from the top of the page,
// checks it is announced by the name the row shows, opens it with Enter
// and checks the app went where its href says, in place.
async function open(page, path, list) {
  await page.goto(path);
  const link = page.locator(`${list} .row-link`).first();
  await expect(link).toBeVisible();
  const href = await link.getAttribute("href");
  const name = (await link.textContent()).trim();
  expect(name).not.toBe("");
  await expect(link).toHaveAccessibleName(name);

  // Start from the document itself, as a fresh keyboard user does.
  await page.evaluate(() => document.activeElement?.blur());
  await tabTo(page, link);
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(new RegExp(`${escape(href)}(\\?.*)?$`));
  return { href, name };
}

test("opens a task, board, template, suite, schedule and repo with Tab and Enter alone", async ({
  page,
}) => {
  const stamp = Date.now();
  await seed(page, "/api/boards", {
    name: `Keyboard board ${stamp}`,
    kind: "list",
    query: "",
  });
  const template = await seed(page, "/api/templates", {
    name: `Keyboard template ${stamp}`,
    title: "Run benchmarks",
  });
  await seed(page, "/api/suites", {
    name: `Keyboard suite ${stamp}`,
    templateIds: [template.id],
    mode: "count",
    count: 1,
  });
  await seed(page, "/api/schedules", {
    title: `Keyboard schedule ${stamp}`,
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 24 },
  });

  // A task: its own page, which is the detail pane over the list.
  const task = await open(page, "/", ".task-list");
  await expect(page).toHaveURL(/\/tasks\/[^/]+$/);
  await expect(page.locator(".detail-header")).toBeVisible();
  expect(task.href).toMatch(/^\/tasks\//);

  await open(page, "/boards", ".template-list");
  await expect(page).toHaveURL(/\/boards\/[^/]+$/);

  await open(page, "/processes?kind=template", ".process-row-template");
  await expect(page.locator("form.pane-form")).toBeVisible();

  await open(page, "/processes?kind=suite", ".process-row-suite");
  await expect(page).toHaveURL(/\/processes\/suites\/[^/]+$/);
  await expect(page.locator("form.pane-form")).toBeVisible();

  await open(page, "/processes?kind=schedule", ".process-row-schedule");
  await expect(page.locator("form.pane-form")).toBeVisible();

  await open(page, "/repos", ".repo-list");
  await expect(page).toHaveURL(/\/repos\/[^/]+\/[^/?]+/);
});

test("Enter on a task's move handle still picks it up rather than opening it", async ({
  page,
}) => {
  await page.goto("/");
  const first = page.getByRole("button", { name: /^Move task / }).first();
  await expect(first).toBeVisible();
  const id = (await first.getAttribute("aria-label")).replace("Move task ", "");
  const handle = page.getByRole("button", {
    name: `Move task ${id}`,
    exact: true,
  });
  await page.evaluate(() => document.activeElement?.blur());
  await tabTo(page, handle);
  await page.keyboard.press("Enter");
  // The same button, now saying it would put the row down again.
  await expect(
    page.getByRole("button", { name: `Stop moving task ${id}`, exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(page).toHaveURL(/\/(\?.*)?$/);
  // Escape cancels the pickup (ReorderPickup.jsx), leaving nothing moved.
  await page.keyboard.press("Escape");
  await expect(handle).toHaveAttribute("aria-pressed", "false");
});
