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

// A deep link to a very old comment on a long task pages back to it with
// the API's largest page (TASK_DETAIL_MAX_PAGE, 500) and asks for each
// page once (fetchEarlier's throughCommentId in ui/src/taskDetailPages.js;
// docs/test-runs/2026-11-11-task-detail-paging.md item 3).

test.setTimeout(240_000);

// 1,100 comments: the page opens on the newest 50, so the oldest is
// 1,050 back -- three pages of 500, where pages of 200 took six.
const COMMENTS = 1100;

test("a deep link to the oldest of 1,100 comments fetches three pages, each once", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: {
      title: "Long thread deep link",
      body: "A task with a very long conversation",
      approved: false,
    },
  });
  expect(filed.ok(), `POST /api/tasks: ${filed.status()}`).toBe(true);
  const task = await filed.json();
  for (let i = 0; i < COMMENTS; i += 50) {
    const batch = [];
    for (let j = i; j < Math.min(i + 50, COMMENTS); j++) {
      batch.push(
        request.post(`/api/tasks/${task.id}/comments`, {
          data: { body: `Long thread comment ${j}.` },
        }),
      );
    }
    for (const res of await Promise.all(batch)) {
      expect(res.ok(), `POST comment: ${res.status()}`).toBe(true);
    }
  }
  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  expect(detail.comments.length).toBe(COMMENTS);
  const oldest = detail.comments[0];
  const second = detail.comments[1];

  const earlier = [];
  page.on("request", (req) => {
    const u = new URL(req.url());
    if (
      req.method() === "GET" &&
      u.pathname === `/api/tasks/${task.id}` &&
      u.searchParams.has("comments_before")
    ) {
      earlier.push(u.search);
    }
  });

  // A fresh load of the link.
  const started = Date.now();
  await page.goto(`/tasks/${task.id}#comment=${oldest.id}`);
  const entry = page.locator(`[data-comment-id="${oldest.id}"]`).first();
  await expect(entry).toContainText(oldest.body, { timeout: 60_000 });
  await expect(entry).toBeInViewport();
  await expect(entry).toBeFocused();
  console.log(`fresh deep link: ${Date.now() - started} ms`);
  expect(earlier).toHaveLength(3);
  expect(new Set(earlier).size).toBe(earlier.length);
  for (const q of earlier) expect(q).toContain("comments_limit=500");

  // Followed in place from the head page, where one link fires both
  // popstate and hashchange: still three pages, none asked twice.
  await page.goto(`/tasks/${task.id}`);
  await expect(
    page.getByRole("button", { name: /^Show earlier/ }).first(),
  ).toBeVisible();
  earlier.length = 0;
  await page.evaluate((id) => {
    window.location.hash = `comment=${id}`;
  }, second.id);
  const target = page.locator(`[data-comment-id="${second.id}"]`).first();
  await expect(target).toContainText(second.body, { timeout: 60_000 });
  await expect(target).toBeInViewport();
  await expect(target).toBeFocused();
  // Give a second, doubled walk time to show itself.
  await page.waitForTimeout(500);
  expect(earlier).toHaveLength(3);
  expect(new Set(earlier).size).toBe(earlier.length);
});
