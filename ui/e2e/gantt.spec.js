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

// The timeline (GanttChart.jsx, gantt.js, grain/task-55) against a real
// server and a real browser -- see tasks.spec.js's own doc comment for
// what this suite is and what `grain demo` has already seeded by the
// time it runs (one task in every state, the running one being "Bump the
// Go toolchain to 1.24", dispatched a few minutes ago).
//
// The chart is a saved board now rather than a fixed entry on the rail
// (grain/task-162), so what this drives is the board `grain demo` seeds
// for exactly that: one called "Timeline", gantt, with no query of its
// own -- a chart over every task, which is what /gantt used to be.
//
// What is worth a real browser here rather than another jsdom test
// beside the component's own: a board as a route somebody can load cold,
// a chart drawn from timestamps a *real* server put on the wire (the
// startedAt and endedAt of ui.Task, which nothing else in the UI reads),
// and a range that has to survive a reload because it lives in
// localStorage rather than on the server.

// The two controls over the chart, by role rather than by label: MUI
// leaves a closed Select's own menu list in the DOM, labelled by the
// same element the box is, so getByLabel("Range") matches two things the
// moment that menu has been opened once.
const select = (page, name) => page.getByRole("combobox", { name });

// Where the demo's timeline board lives, asked of the server rather than
// assumed -- board.spec.js's own boardPath, and the same reason.
async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

test("draws the seeded tasks as bars against a clock", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("button", { name: /^Boards/ }).click();
  await page.locator(".template-row", { hasText: "Timeline" }).first().click();

  await expect(page).toHaveURL(/\/boards\/[^/]+$/);
  // The board's own header: its name, and the chip saying which sort of
  // board it is. Both read "Timeline" here, which is why this is a
  // containment check rather than a locator that has to pick one.
  await expect(page.locator(".repo-page-header")).toContainText("Timeline");

  // The running task, dispatched six minutes ago: its row is on the
  // chart and its bar is one the server said has not ended.
  const running = page.locator(".gantt-row", {
    hasText: "Bump the Go toolchain to 1.24",
  });
  await expect(running).toBeVisible();
  await expect(running.locator(".gantt-bar-ongoing")).toBeVisible();

  // The axis is under the toolbar with real times on it, rather than an
  // empty strip: everything above is drawn against it.
  await expect(page.locator(".gantt-tick").first()).toBeVisible();

  // The seeded closed task was filed eight days ago and closed a day
  // later, so the default seven-day range does not reach it -- and the
  // chart says so rather than quietly showing fewer tasks than the
  // deployment has.
  await expect(
    page.locator(".gantt-row", { hasText: "Spike: websocket transport" }),
  ).toHaveCount(0);
  await expect(page.getByText(/outside this range/)).toBeVisible();

  // Widen the range and it is there, with a bar that has an end on it:
  // the server's own closed_at, drawn as a stretch that stopped.
  await select(page, "Range").click();
  await page.getByRole("option", { name: "Everything" }).click();
  const done = page.locator(".gantt-row", {
    hasText: "Spike: websocket transport",
  });
  await expect(done.locator(".gantt-bar")).toBeVisible();
  await expect(done.locator(".gantt-bar-ongoing")).toHaveCount(0);

  // Back to the default, so a rerun of this suite starts where a first
  // run did (this browser remembers the range -- gantt.js).
  await select(page, "Range").click();
  await page.getByRole("option", { name: "Last 7 days" }).click();
});

test("loads a timeline board cold from its own URL", async ({ page }) => {
  await page.goto(await boardPath(page, "Timeline"));

  await expect(page.locator(".gantt-bar").first()).toBeVisible();
});

// The demo's blocked task -- a decomposed subtask that depends on another
// -- is queued, but waiting on that dependency rather than on dispatch,
// and its row says so the way its list row and its card do.
test("marks a blocked task as blocked", async ({ page }) => {
  await page.goto(await boardPath(page, "Timeline"));

  const row = page.locator(".gantt-row", {
    hasText: "Extract pagination query helper",
  });
  const badge = row.locator(".badge");
  await expect(badge).toHaveClass(/\bbadge-blocked\b/);
  await expect(badge).toHaveAttribute("title", /^Blocked on: #\d+/);
  await expect(
    row.locator(".MuiChip-root", { hasText: "Blocked" }),
  ).toHaveCount(0);
  await expect(row.locator(".gantt-bar")).toHaveAttribute(
    "aria-label",
    /^Waiting, blocked: /,
  );
  await expect(row).toHaveAttribute("title", /Blocked on: #\d+/);
});

// The path the chart used to live at went with the entry that led to it.
// A link somebody kept opens the task list rather than an error or a
// blank pane (paths.js).
test("lands a kept /gantt link on the task list", async ({ page }) => {
  await page.goto("/gantt");

  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator(".task-row").first()).toBeVisible();
  await expect(page.locator(".gantt-bar")).toHaveCount(0);
});

test("keeps the chosen range and lanes across a reload", async ({ page }) => {
  await page.goto(await boardPath(page, "Timeline"));

  await select(page, "Range").click();
  await page.getByRole("option", { name: "Last 24 hours" }).click();
  await select(page, "Lanes").click();
  await page.getByRole("option", { name: "By state" }).click();
  await expect(page.locator(".gantt-lane-title").first()).toBeVisible();

  await page.reload();

  await expect(select(page, "Range")).toHaveText("Last 24 hours");
  await expect(select(page, "Lanes")).toHaveText("By state");

  // Put the default back, so a rerun of this suite starts where a first
  // run did -- the same courtesy board.spec.js pays the column layout.
  await select(page, "Range").click();
  await page.getByRole("option", { name: "Last 7 days" }).click();
  await select(page, "Lanes").click();
  await page.getByRole("option", { name: "No lanes" }).click();
});

// A timeline row holds no control of its own, so before grain/task-1420
// it was out of the tab order entirely. Its title is the link a keyboard
// opens it by, as on a task row, and the row stays a plain listitem of
// the timeline's list (grain/task-1582).
test("opens a task from its row with the keyboard", async ({ page }) => {
  await page.goto(await boardPath(page, "Timeline"));

  const row = page.locator(".gantt-row", {
    hasText: "Bump the Go toolchain to 1.24",
  });
  await expect(row).not.toHaveAttribute("tabindex");
  const link = row.getByRole("link", { name: "Bump the Go toolchain to 1.24" });
  await link.focus();
  await expect(link).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/\/tasks\/\d+$/);
  await expect(
    page.getByRole("heading", { name: "Bump the Go toolchain to 1.24" }),
  ).toBeVisible();
});

test("opens a task from its bar", async ({ page }) => {
  await page.goto(await boardPath(page, "Timeline"));

  await page
    .locator(".gantt-row", { hasText: "Bump the Go toolchain to 1.24" })
    .click();

  await expect(
    page.getByRole("heading", { name: "Bump the Go toolchain to 1.24" }),
  ).toBeVisible();
});
