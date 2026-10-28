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

// A task view whose reader has taken every condition off (grain/task-
// 5077, found in the 2026-10-23 saved boards walkthrough). That used to
// be written as no query at all, which is also how "nobody has asked"
// is written -- so a reload put the deployment's default back on "/" and
// a board's own saved query back on /boards/<id>, and the closed and
// deferred tasks the reader had just asked for vanished again. paths.js
// now writes an empty narrowing as "?f=none"; these check the whole
// round trip against a real server, since it is a reload -- a real GET
// that only the address bar survives -- that lost it.

const seeded = [];

test.afterEach(async ({ page }) => {
  while (seeded.length > 0) {
    await page.request.delete(`/api/boards/${seeded.pop()}`);
  }
});

const rows = (page) => page.locator(".task-row");
const chips = (page) => page.locator(".narrowing-chips .MuiChip-root");
// A running task `grain demo` seeds, which every list below shows: once
// it is on screen the list has loaded and its rows can be counted.
const running = (page) =>
  page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" });

test("keeps a board's cleared narrowing across a reload", async ({ page }) => {
  const res = await page.request.post("/api/boards", {
    data: {
      name: `E2E cleared ${Date.now()}`,
      kind: "list",
      query: "f=state:any:running",
    },
  });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();
  seeded.push(board.id);

  await page.goto(`/boards/${board.id}`);
  await expect(page).toHaveURL(
    new RegExp(`/boards/${board.id}\\?f=state:any:running$`),
  );
  await expect(running(page)).toBeVisible();
  const asked = await rows(page).count();

  await chips(page)
    .filter({ hasText: "Running" })
    .locator(".MuiChip-deleteIcon")
    .click();
  await expect(chips(page)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`/boards/${board.id}\\?f=none$`));
  await expect.poll(() => rows(page).count()).toBeGreaterThan(asked);
  const cleared = await rows(page).count();

  // The reload is the question: the board's own query must not come back.
  await page.reload();
  await expect(page).toHaveURL(new RegExp(`/boards/${board.id}\\?f=none$`));
  await expect(running(page)).toBeVisible();
  await expect(rows(page)).toHaveCount(cleared);
  await expect(chips(page)).toHaveCount(0);

  // ...and so must a link somebody was sent, opened cold.
  const shared = await page.context().newPage();
  await shared.goto(`/boards/${board.id}?f=none`);
  await expect(running(shared)).toBeVisible();
  await expect(rows(shared)).toHaveCount(cleared);
  await shared.close();
});

test("keeps the list's cleared default conditions across a reload", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/$/);
  await expect(running(page)).toBeVisible();
  const byDefault = await rows(page).count();
  // The deployment's default narrowing is on screen as chips nobody typed.
  await expect(chips(page).first()).toBeVisible();

  while ((await chips(page).count()) > 0) {
    await chips(page).first().locator(".MuiChip-deleteIcon").click();
  }
  await expect(page).toHaveURL(/\/\?f=none$/);
  await expect.poll(() => rows(page).count()).toBeGreaterThan(byDefault);
  const cleared = await rows(page).count();

  await page.reload();
  await expect(page).toHaveURL(/\/\?f=none$/);
  await expect(running(page)).toBeVisible();
  await expect(rows(page)).toHaveCount(cleared);
  await expect(chips(page)).toHaveCount(0);

  // A plain "/" is still the default.
  await page.goto("/");
  await expect(running(page)).toBeVisible();
  await expect(rows(page)).toHaveCount(byDefault);
});
