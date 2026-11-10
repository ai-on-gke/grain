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

// Deep links to one comment in a task's conversation,
// /tasks/<id>#comment=<id> (ui/src/commentLinks.js).

test.setTimeout(120_000);

const COMMENTS = 120;

// longConversation files a task with COMMENTS comments on it and returns
// the task with its comments as the API lists them (ids included).
async function longConversation(request, title) {
  const filed = await request.post("/api/tasks", {
    data: { title, body: "A task with a long conversation", approved: false },
  });
  expect(filed.ok(), `POST /api/tasks: ${filed.status()}`).toBe(true);
  const task = await filed.json();
  for (let i = 0; i < COMMENTS; i++) {
    const posted = await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: `Comment number ${i} in a long conversation.` },
    });
    expect(posted.ok(), `POST comment ${i}: ${posted.status()}`).toBe(true);
  }
  const got = await request.get(`/api/tasks/${task.id}`);
  const detail = await got.json();
  expect(detail.comments.length).toBe(COMMENTS);
  return detail;
}

test("opening a deep link scrolls to and highlights a comment far into a long conversation", async ({
  page,
  request,
}) => {
  const task = await longConversation(request, "Deep link target task");
  // The timeline reads newest first by default, so the fifth comment
  // posted is a hundred-odd entries below the top of the pane.
  const target = task.comments[4];

  await page.goto(`/tasks/${task.id}#comment=${target.id}`);
  const entry = page.locator(`[data-comment-id="${target.id}"]`);
  await expect(entry).toContainText("Comment number 4 in a long conversation.");
  await expect(entry).toBeInViewport();
  await expect(entry).toHaveClass(/comment-highlight/);
  // Briefly: the mark goes again.
  await expect(entry).not.toHaveClass(/comment-highlight/, { timeout: 5000 });
  // The newest comment, at the top of the feed, is scrolled out of view.
  await expect(
    page.locator(`[data-comment-id="${task.comments[COMMENTS - 1].id}"]`),
  ).not.toBeInViewport();
  await expect(page.getByTestId("comment-link-notice")).toHaveCount(0);

  // Changing the hash on the page already open goes to the new comment.
  const other = task.comments[60];
  await page.evaluate((id) => {
    window.location.hash = `comment=${id}`;
  }, other.id);
  const otherEntry = page.locator(`[data-comment-id="${other.id}"]`);
  await expect(otherEntry).toBeInViewport();
  await expect(otherEntry).toHaveClass(/comment-highlight/);

  // Copy link puts the absolute address of that comment on the
  // clipboard.
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await otherEntry.getByRole("button", { name: "Copy link" }).click();
  const copied = await page.evaluate(() => navigator.clipboard.readText());
  const origin = new URL(page.url()).origin;
  expect(copied).toBe(`${origin}/tasks/${task.id}#comment=${other.id}`);
});

test("a link to a comment that is not in the task says so and stays at the top", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: { title: "Deep link missing comment", body: "x", approved: false },
  });
  const task = await filed.json();
  await page.goto(`/tasks/${task.id}#comment=987654321`);
  await expect(page.getByTestId("comment-link-notice")).toContainText(
    "Comment 987654321 is not in this task's conversation.",
  );
  await expect(page.locator(".detail-header").first()).toBeInViewport();
});

test("a comment link in a comment opens in place without reloading the page", async ({
  page,
  request,
}) => {
  const task = await longConversation(request, "Deep link linked-to task");
  const target = task.comments[2];
  const filed = await request.post("/api/tasks", {
    data: { title: "Deep link source task", body: "x", approved: false },
  });
  const source = await filed.json();
  await request.post(`/api/tasks/${source.id}/comments`, {
    data: {
      body: `See [the earlier comment](/tasks/${task.id}#comment=${target.id}).`,
    },
  });

  await page.goto(`/tasks/${source.id}`);
  // Set on this document only: a reload would lose it.
  await page.evaluate(() => {
    window.__noReload = true;
  });
  const link = page.getByRole("link", { name: "the earlier comment" });
  await expect(link).not.toHaveAttribute("target", "_blank");
  await link.click();
  await expect(page).toHaveURL(
    new RegExp(`/tasks/${task.id}#comment=${target.id}$`),
  );
  const entry = page.locator(`[data-comment-id="${target.id}"]`);
  await expect(entry).toBeInViewport();
  await expect(entry).toHaveClass(/comment-highlight/);
  expect(await page.evaluate(() => window.__noReload)).toBe(true);
});

test("a deep link into an interactive task's chat scrolls the chat to the message", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: {
      title: "Deep link chat task",
      body: "Interactive task description",
      interactive: true,
      approved: false,
    },
  });
  const task = await filed.json();
  for (let i = 0; i < 60; i++) {
    await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: `Chat line ${i}` },
    });
  }
  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  const target = detail.comments[3];

  await page.goto(`/tasks/${task.id}#comment=${target.id}`);
  // The chat bubble, not the copy folded up in the task history.
  const bubble = page.locator(
    `.task-chat-window [data-comment-id="${target.id}"]`,
  );
  await expect(bubble).toContainText("Chat line 3");
  await expect(bubble).toBeInViewport();
  await expect(bubble).toHaveClass(/comment-highlight/);
  await expect(page.locator(".task-chat-history").first()).not.toHaveAttribute(
    "open",
    "",
  );
});

for (const [name, size] of Object.entries({
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
})) {
  test(`Copy link sits beside Copy comment in the comment's header (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const filed = await request.post("/api/tasks", {
      data: { title: `Copy link layout ${name}`, body: "x", approved: false },
    });
    const task = await filed.json();
    await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: "A comment with both copy actions." },
    });
    await page.goto(`/tasks/${task.id}`);
    const item = page.locator(".timeline-item[data-comment-id]").first();
    const header = item.locator(".timeline-comment-header");
    const link = header.getByRole("button", { name: "Copy link" });
    const copy = header.getByRole("button", { name: "Copy comment" });
    await expect(link).toBeVisible();
    const h = await header.boundingBox();
    const l = await link.boundingBox();
    const c = await copy.boundingBox();
    // One row, at the header's right-hand end, link first.
    expect(Math.abs(l.y + l.height / 2 - (c.y + c.height / 2))).toBeLessThan(2);
    expect(l.x + l.width).toBeLessThanOrEqual(c.x + 1);
    expect(c.x + c.width).toBeLessThanOrEqual(h.x + h.width + 1);
    expect(l.y).toBeGreaterThanOrEqual(h.y - 1);
    expect(l.y + l.height).toBeLessThanOrEqual(h.y + h.height + 1);
    const overflow = await item.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
}
