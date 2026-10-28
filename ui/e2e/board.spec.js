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

// The board (TaskBoard.jsx, grain/task-287) against a real server and a
// real browser -- see tasks.spec.js's own doc comment for what this
// suite is and what `grain demo` has already seeded by the time it runs
// (one task in every state, "Spike: websocket transport" being the
// closed one, "Replace the settings pane with a form library" the
// deferred one).
//
// The columns are a saved board now rather than a fixed entry on the
// rail (grain/task-162), so what this drives is the board `grain demo`
// seeds for exactly that: one called "Board", kanban, with no columns
// and no query of its own -- which is a board over every task drawn
// with whatever columns this browser is set to, and so is the same
// thing /board used to be.
//
// Two things here are worth a real browser rather than another jsdom
// test beside the component's own: a saved board as a route somebody
// can load cold, and a column layout that has to survive a reload
// because it lives in localStorage rather than on the server.

// boardPath is where the demo's Kanban board lives, asked of the server
// rather than assumed: a board's id is issued by the store, so nothing
// here can hard-code "board-1" and stay true if the demo seeds another
// row before it one day.
async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

test("lays the seeded tasks out in the board's columns", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("button", { name: /^Boards/ }).click();
  await page.getByText("Board", { exact: true }).click();

  await expect(page).toHaveURL(/\/boards\/[^/]+$/);
  // The board's own header says which board this is and what sort it is;
  // the pane under it is the ordinary board pane.
  const header = page.locator(".repo-page-header");
  await expect(header.getByText("Kanban", { exact: true })).toBeVisible();
  // The seeded running task, in the column that collects "running".
  const running = page.locator(".board-column", { hasText: "Running" }).first();
  const runningCard = running.locator(".board-card", {
    hasText: "Bump the Go toolchain to 1.24",
  });
  await expect(runningCard).toBeVisible();
  // The seeded blocked task sits in Waiting, wearing the blocked oval on its
  // top row with no extra "Blocked" chip, while an unblocked card in that
  // one-state column wears no badge.
  const queued = page.locator(".board-column").filter({
    has: page.locator(".board-column-title", { hasText: /^Waiting$/ }),
  });
  const blockedCard = queued.locator(".board-card", {
    hasText: "Extract pagination query helper",
  });
  const blockedBadge = blockedCard.locator(".badge");
  await expect(blockedBadge).toHaveClass(/\bbadge-blocked\b/);
  await expect(blockedBadge).toHaveAttribute("title", /^Blocked on: #\d+ \(/);
  await expect(
    blockedCard.locator(".MuiChip-root", { hasText: "Blocked" }),
  ).toHaveCount(0);
  const cardBadgeBox = await blockedBadge.boundingBox();
  const cardNumBox = await blockedCard.locator(".task-number").boundingBox();
  expect(
    Math.abs(
      cardBadgeBox.y +
        cardBadgeBox.height / 2 -
        (cardNumBox.y + cardNumBox.height / 2),
    ),
  ).toBeLessThan(3);
  const unblockedCard = queued.locator(".board-card", {
    hasText: "Add a dark mode toggle",
  });
  await expect(unblockedCard.locator(".badge")).toHaveCount(0);
  // Multi-state columns expose each card's state badge as role="img" with
  // its accessible state label, and enable move handles when >1 card is present.
  // "Asked" is one column for both asked states.
  const needsYou = page.locator(".board-column").filter({
    has: page.locator(".board-column-title", { hasText: /^Asked$/ }),
  });
  await expect(needsYou.getByRole("img").first()).toBeVisible();
  await expect(
    needsYou.getByRole("button", { name: /^Move task / }).first(),
  ).toBeEnabled();
  // Closed and deferred tasks are off the default board, and it says so
  // rather than quietly showing fewer tasks than the deployment has.
  await expect(page.getByText("Spike: websocket transport")).toHaveCount(0);
  await expect(
    page.getByText("Replace the settings pane with a form library"),
  ).toHaveCount(0);
  await expect(
    page.getByText(/in no column \(Deferred, Closed\)/),
  ).toBeVisible();
});

test("loads a Kanban board cold from its own URL", async ({ page }) => {
  await page.goto(await boardPath(page, "Board"));

  await expect(page.locator(".board-column").first()).toBeVisible();
});

// The path the two layouts used to live at went with the entries that
// led to them. A link somebody kept opens the task list rather than an
// error or a blank pane (paths.js).
test("lands a kept /board link on the task list", async ({ page }) => {
  await page.goto("/board");

  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator(".task-row").first()).toBeVisible();
  await expect(page.locator(".board-column")).toHaveCount(0);
});

// The toolbar's own "Columns" button, named exactly. Not a stylistic
// preference: the board grows a *second* button whose accessible name
// contains "Columns" the moment it has anything to hide -- the "Edit
// columns" link in the "N tasks in no column (Deferred, Closed)" note
// (TaskBoard.jsx)
// -- and getByRole's name option is a case-insensitive substring match
// unless it is told otherwise, so a plain { name: "Columns" } matches both
// and fails Playwright's strict mode. Which of the two the page had when
// the click landed came down to whether the seeded tasks had arrived yet:
// green on a warm machine, red on a slower runner, and a locator that is
// exact is right whichever way that race falls.
const columnsButton = (page) =>
  page.getByRole("button", { name: "Columns", exact: true });

// COLD_LOAD_MS is the ceiling on the one assertion below that has to
// cover a page load, rather than Playwright's default five seconds --
// and it is the whole of why the test under it used to fail about once
// in a full-suite run on a loaded machine while passing every time on
// its own.
//
// Nothing else in the test needs it. Every other step is an *action* --
// a click, a fill -- and an action waits for the element it names with
// no ceiling of its own, so the slower the machine the longer this test
// simply takes; by the time the layout has been edited and saved, the
// tasks it is about have had all of that to arrive in. An `expect` is
// not that: its ceiling is fixed wall-clock time, and the one after
// `page.reload()` is left holding the app's entire cold start.
// `page.reload()` comes back at the load event, with React only then
// fetching /api/config, /api/boards and /api/tasks (App.jsx's mount
// effect) -- so a single assertion is covering that round trip, the
// render of the board pane once the boards land, and the render of its
// cards once the tasks do.
//
// Measured here against a browser given a fraction of a CPU (CDP's
// Emulation.setCPUThrottlingRate, which is what a runner with the rest
// of the suite on it amounts to): 0.2s at full speed, 1.1s at 4x,
// 1.7-3.3s at 10x, and past 5s at 20x. Those were taken while the mount
// effect still awaited /api/config before asking for anything else,
// which is two serial round trips rather than one; that is fixed
// (grain/task-235) and the numbers are a ceiling on what it costs now.
// Past that ceiling this fails looking exactly like a lost layout --
// "element(s) not found" for the card in the Archive column -- while
// what the page actually held was the loading screen, /api/boards not
// having landed either.
//
// Twenty seconds, the ceiling boards.spec.js gives its own polls for the
// same reason. It costs a passing run nothing -- an assertion that is
// going to succeed still succeeds as fast as it ever did -- and it is
// the margin, not the assertion, that this test was short of. For a
// sense of where it sits: at 25x, with the machine loaded besides, this
// wait measured 16.8s, 17.9s and 18.2s over three runs of a test that
// took three to four minutes each. Nothing an actual runner does is
// within reach of that, and a ceiling raised past it would only make a
// board that really had lost its layout take longer to say so.
const COLD_LOAD_MS = 20_000;

test("keeps an edited column layout across a reload", async ({ page }) => {
  await page.goto(await boardPath(page, "Board"));

  await columnsButton(page).click();
  await expect(page.getByText("Board columns")).toBeVisible();
  await page.getByRole("button", { name: "+ Add column" }).click();
  const title = page.getByLabel(/Column \d+ title/).last();
  await title.fill("Archive");
  await page.getByLabel("States").last().click();
  await page.getByRole("option", { name: "Closed" }).click();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Save" }).click();

  const archive = page.locator(".board-column", { hasText: "Archive" });
  // The same margin the reload below is given, for the same reason. This
  // assertion looks like a pure re-render of tasks the page already has,
  // and on a warm machine it is -- but saving a column the board did not
  // have is a write and a refetch, and on a runner with every core
  // committed the card lands after Playwright's default five seconds:
  // run 400b1a0bf401b9ab failed here, and only here, in a suite where the
  // other 124 passed. Widening the ceiling costs a green run nothing,
  // which is the whole of COLD_LOAD_MS's own argument above.
  await expect(
    archive.locator(".board-card", { hasText: "Spike: websocket transport" }),
  ).toBeVisible({ timeout: COLD_LOAD_MS });
  // A column of one card has nowhere to move its lone card to, so its
  // move handle is disabled rather than offering a pickup with no slots.
  await expect(
    archive
      .locator(".board-card", { hasText: "Spike: websocket transport" })
      .getByRole("button", { name: /^Move task / }),
  ).toBeDisabled();

  await page.reload();

  // The layout came back from localStorage, and the cards came back from
  // the server -- the whole cold start, inside one assertion, which is
  // what COLD_LOAD_MS above is for.
  await expect(
    page
      .locator(".board-column", { hasText: "Archive" })
      .locator(".board-card", { hasText: "Spike: websocket transport" }),
  ).toBeVisible({ timeout: COLD_LOAD_MS });

  // Put the default board back, so a rerun of this suite starts from the
  // same place a first run did.
  await columnsButton(page).click();
  await page.getByRole("button", { name: "Reset to default" }).click();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(
    page.locator(".board-column", { hasText: "Archive" }),
  ).toHaveCount(0);
});
