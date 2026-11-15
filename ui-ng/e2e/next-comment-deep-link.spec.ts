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

// /next/task/<id>#comment=<n> -- the link grain writes into agent notes
// and notifications -- lands on that comment: scrolled into the
// viewport (and into the conversation's own scrolling window), focused,
// on the task page, in the Tasks pane, on a rolled-over task under its
// banner, and when the comment is further back than the first page of
// the conversation. Tagged @phone so the phone project runs it at 390px.

import type { Locator, Page } from "@playwright/test";
import { test, expect } from "./fixtures";

type Comment = { id: number };
type Detail = {
  id: string;
  title: string;
  comments?: Comment[];
  olderComments?: number;
  rolledOverTo?: string;
};

async function detail(page: Page, id: string): Promise<Detail> {
  const res = await page.request.get(`/api/tasks/${id}`);
  expect(res.ok()).toBeTruthy();
  return (await res.json()) as Detail;
}

// longestThread is the open demo task with the most comments, so the
// first of them starts out of the thread's view (it opens at the end).
async function longestThread(page: Page): Promise<Detail> {
  const res = await page.request.get("/api/tasks");
  const body = await res.json();
  const tasks = (Array.isArray(body) ? body : body.tasks) as Detail[];
  let best: Detail | null = null;
  for (const t of tasks) {
    const d = await detail(page, String(t.id));
    if ((d.comments?.length ?? 0) > (best?.comments?.length ?? 0)) best = d;
  }
  expect(best?.comments?.length ?? 0).toBeGreaterThan(3);
  return best!;
}

async function rolledOver(page: Page): Promise<Detail> {
  const res = await page.request.get("/api/tasks");
  const body = await res.json();
  const tasks = (Array.isArray(body) ? body : body.tasks) as Array<
    Detail & { rolledOverFrom?: string }
  >;
  const to = tasks.find(
    (t) =>
      t.title === "Watch the acme/widgets nightly build" && t.rolledOverFrom,
  );
  expect(to, "the demo seeds a rolled-over pair").toBeTruthy();
  const from = await detail(page, String(to!.rolledOverFrom));
  expect(from.comments?.length ?? 0).toBeGreaterThan(0);
  return from;
}

// expectInView asserts the comment's whole box is inside the viewport and
// inside the conversation thread that clips it.
async function expectInView(page: Page, comment: Locator): Promise<void> {
  await expect(comment).toBeFocused();
  const vp = page.viewportSize()!;
  const thread = page.getByRole("log", { name: "Conversation thread" });
  // Wait out the scroll: the box holds still once the comment is placed.
  await expect
    .poll(async () => {
      const a = await comment.boundingBox();
      await page.waitForTimeout(100);
      const b = await comment.boundingBox();
      return a && b && Math.abs(a.y - b.y) < 1;
    })
    .toBe(true);
  const box = (await comment.boundingBox())!;
  const tb = (await thread.boundingBox())!;
  expect(box.y, "comment top is on screen").toBeGreaterThanOrEqual(0);
  expect(box.y + box.height, "comment bottom is on screen").toBeLessThanOrEqual(
    vp.height,
  );
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vp.width);
  expect(box.y, "comment top is inside the thread").toBeGreaterThanOrEqual(
    tb.y - 1,
  );
  expect(
    box.y + box.height,
    "comment bottom is inside the thread",
  ).toBeLessThanOrEqual(tb.y + tb.height + 1);
}

test(
  "a #comment= link on the task page scrolls to and focuses that comment",
  { tag: ["@phone"] },
  async ({ page, pageErrors }) => {
    await page.goto("/next/");
    const t = await longestThread(page);
    const first = t.comments![0].id;
    await page.goto(`/next/task/${t.id}#comment=${first}`);
    const comment = page.locator(`#comment-${first}`);
    await expectInView(page, comment);

    // A link to another comment on the page already open follows it too.
    const last = t.comments![t.comments!.length - 1].id;
    await page.evaluate((id) => {
      window.location.hash = `comment=${id}`;
    }, last);
    await expectInView(page, page.locator(`#comment-${last}`));
    expect(pageErrors).toEqual([]);
  },
);

test(
  "a #comment= link opens the comment in the Tasks pane",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("/next/");
    const t = await longestThread(page);
    const first = t.comments![0].id;
    await page.goto(`/next/tasks?task=${t.id}#comment=${first}`);
    if (page.viewportSize()!.width <= 720) {
      // A phone has no pane: the link opens the task's own page there.
      await expect(page).toHaveURL(
        new RegExp(`/next/task/${t.id}\\?.*#comment=${first}$`),
      );
    }
    await expectInView(page, page.locator(`#comment-${first}`));
  },
);

test(
  "a #comment= link on a rolled-over task lands under its banner",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("/next/");
    const from = await rolledOver(page);
    const first = from.comments![0].id;
    await page.goto(`/next/task/${from.id}#comment=${first}`);
    await expect(page.getByTestId("task-rolled-over-to")).toBeVisible();
    await expectInView(page, page.locator(`#comment-${first}`));
  },
);

test(
  "a #comment= link pages back to a comment older than the first page",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("/next/");
    const t = await longestThread(page);
    const ids = t.comments!.map((c) => c.id);
    // The demo's threads fit one page, so serve the head read as if only
    // the newest comment did; the pages before it come from the server.
    const before: string[] = [];
    await page.route(new RegExp(`/api/tasks/${t.id}\\?`), async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.has("comments_before")) {
        before.push(url.search);
        return route.continue();
      }
      const res = await route.fetch({
        headers: { ...route.request().headers(), "if-none-match": "" },
      });
      const body = (await res.json()) as Detail;
      body.comments = body.comments!.slice(-1);
      body.olderComments = ids.length - 1;
      await route.fulfill({ response: res, json: body });
    });
    await page.goto(`/next/task/${t.id}#comment=${ids[0]}`);
    await expectInView(page, page.locator(`#comment-${ids[0]}`));
    expect(before).toHaveLength(1);
    expect(before[0]).toContain(`comments_before=${ids[ids.length - 1]}`);
    await expect(page.getByTestId("load-older-comments")).toHaveCount(0);
  },
);

test("a #comment= naming no comment of the task says so", async ({ page }) => {
  await page.goto("/next/");
  const t = await longestThread(page);
  await page.goto(`/next/task/${t.id}#comment=999999`);
  await expect(
    page.getByText("Comment 999999 is not in this task's conversation."),
  ).toBeVisible();
});
