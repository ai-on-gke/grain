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

// Server-side closed search beside scroll paging (2026-11-07 walkthrough
// §10 item 1, docs/test-runs/2026-11-07-ui-search-scroll-resilience.md):
//
//   - typing a search must not read a page of the newest-closed list
//     (the pager briefly stayed in its old mode while the list had already
//     narrowed to the new text, and the end of the short list scrolled
//     into view -- every search read 200 unrelated rows);
//   - emptying the box must not leave the search's deep hits drawn below
//     the newest-closed pages, after a gap of every task closed between;
//   - a single `owner:any:<v>` or `externalRef:any:<v>` condition over
//     closed work is asked of the server (?owner=, ?externalRef=) rather
//     than answered by paging through every closed page until the match
//     turns up (finding F6 of the same walkthrough: 15 pages and ~10 s
//     against 3,000 closed tasks).
//
// GET /api/tasks?state=closed is answered here with three synthetic
// pages of 200 and a search that finds one task from far below them, so
// the spec runs on the shared demo without filing anything.

import { test, expect } from "./fixtures.js";

const PAGE = 200;
const PAGES = 3;
const DEEP_ID = "990001";
const DEEP_OWNER = "deepowner@example.com";
const base = Date.parse("2026-11-01T00:00:00Z");

// template is a real closed row from the demo, so the synthetic ones
// carry every field the views read.
let template = {};

function closedTask(n) {
  const at = new Date(base - n * 60_000).toISOString();
  return {
    ...template,
    id: String(900000 + n),
    title: `walk closed ${String(n).padStart(4, "0")}`,
    state: "closed",
    createdAt: at,
    endedAt: at,
    closedAt: at,
  };
}

const deepTask = () => ({
  ...closedTask(5000),
  id: DEEP_ID,
  title: "walk closed zephyrquill needle",
  externalRef: "CA-9012",
  owner: DEEP_OWNER,
});

async function routeClosed(page) {
  const res = await page.request.get("/api/tasks?state=closed&limit=1&cursor=");
  expect(res.ok()).toBe(true);
  const real = (await res.json()).tasks?.[0];
  expect(real, "the demo holds a closed task to copy").toBeTruthy();
  template = { ...real };
  delete template.externalRef;
  delete template.prMergedAt;
  delete template.consoleSession;
  template.interactive = false;
  const asked = [];
  await page.route(/\/api\/tasks\?.*state=closed/, (route) => {
    const url = new URL(route.request().url());
    const search = url.searchParams.get("search") || "";
    const cursor = url.searchParams.get("cursor") || "";
    const owner = url.searchParams.get("owner") || "";
    const externalRef = url.searchParams.get("externalRef") || "";
    asked.push({ search, cursor, owner, externalRef });
    let body;
    if (owner || externalRef) {
      // pkg/ui's taskListQuery: an owner case-insensitively, an external
      // reference exactly.
      const hit =
        (!owner || owner.toLowerCase() === DEEP_OWNER) &&
        (!externalRef || externalRef === "CA-9012");
      body = {
        tasks: hit ? [deepTask()] : [],
        nextCursor: "",
        total: hit ? 1 : 0,
      };
    } else if (search) {
      const hit = "zephyrquill needle".includes(search) || search === "ca-9012";
      body = {
        tasks: hit ? [deepTask()] : [],
        nextCursor: "",
        total: hit ? 1 : 0,
      };
    } else {
      const index = cursor ? Number(cursor.slice(1)) : 0;
      const tasks = Array.from({ length: PAGE }, (_, i) =>
        closedTask(index * PAGE + i),
      );
      body = {
        tasks,
        nextCursor: index + 1 < PAGES ? `p${index + 1}` : "",
        total: PAGE * PAGES + 1,
        merged: 0,
        sessions: 0,
      };
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  return asked;
}

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]) {
  test(`typing and clearing a closed search reads no stray page and leaves no hit out of place (${viewport.width}px)`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const asked = await routeClosed(page);
    await page.goto("/?f=state:any:closed");
    const rows = page.locator(".task-row");
    await expect(rows.filter({ hasText: "walk closed 0000" })).toHaveCount(1);
    await expect(rows.filter({ hasText: "walk closed 0199" })).toHaveCount(1);
    // The pager sits below the last row it pages after.
    const lastRow = await rows
      .filter({ hasText: "walk closed 0199" })
      .boundingBox();
    const pager = await page.locator(".closed-tasks-more").boundingBox();
    expect(pager.y).toBeGreaterThanOrEqual(lastRow.y + lastRow.height - 1);
    const before = asked.length;

    const box = page
      .locator('input[type="search"], input[placeholder*="Search" i]')
      .first();
    await box.pressSequentially("zephyrquill", { delay: 40 });
    await expect(rows.filter({ hasText: "zephyrquill" })).toHaveCount(1);
    await page.waitForTimeout(800);
    const during = asked.slice(before);
    expect(
      during.filter((a) => !a.search),
      "typing a search read a page of the newest-closed list",
    ).toEqual([]);
    expect(during.some((a) => a.search === "zephyrquill")).toBe(true);

    // The external reference finds the same task.
    await box.fill("CA-9012");
    await expect(rows.filter({ hasText: "zephyrquill" })).toHaveCount(1);

    await box.fill("");
    await expect(rows.filter({ hasText: "walk closed 0000" })).toHaveCount(1);
    await page.waitForTimeout(800);
    // Back to the first page alone: the deep hit is not drawn below it.
    await expect(rows.filter({ hasText: "zephyrquill" })).toHaveCount(0);
    expect(
      asked.slice(before).filter((a) => !a.search && a.cursor),
      "clearing the search read further pages",
    ).toEqual([]);
    await expect(rows).toHaveCount(PAGE);
  });

  for (const [condition, param, value] of [
    ["externalRef:any:CA-9012", "externalRef", "CA-9012"],
    [`owner:any:${DEEP_OWNER}`, "owner", DEEP_OWNER],
  ]) {
    test(`one ${param} condition over closed work asks the server, not every page (${viewport.width}px)`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      const asked = await routeClosed(page);
      await page.goto(
        `/?f=state:any:closed&f=${encodeURIComponent(condition)}`,
      );
      const rows = page.locator(".task-row");
      await expect(rows.filter({ hasText: "zephyrquill" })).toHaveCount(1);
      // Long enough for an observer-driven pager to have walked the pages.
      await page.waitForTimeout(1200);
      expect(
        asked.filter((a) => a[param] === value),
        `the ${param} condition was not asked of the server`,
      ).not.toEqual([]);
      expect(
        asked.filter(
          (a) => !a.search && !a.owner && !a.externalRef && a.cursor,
        ),
        "the condition paged through the newest-closed list",
      ).toEqual([]);
      // At most the newest page (the rail's counts) beside the filtered ask.
      expect(asked.length).toBeLessThanOrEqual(3);
      await expect(rows).toHaveCount(1);

      // Dropping the condition hands the pager back to the newest-closed
      // list, and the deep task is not drawn below its first page.
      await page.goto("/?f=state:any:closed");
      await expect(rows.filter({ hasText: "walk closed 0000" })).toHaveCount(1);
      await expect(rows.filter({ hasText: "zephyrquill" })).toHaveCount(0);
    });
  }
}
