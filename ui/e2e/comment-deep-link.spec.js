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

test("an Inbox row parked on a question opens on the question, in view and focused", async ({
  page,
  request,
}) => {
  const task = await longConversation(request, "Deep link parked question");
  const asked = await request.post(`/api/tasks/${task.id}/messages`, {
    data: {
      summary: "Ship the long conversation as it is?",
      body: "A question asked after a long conversation.",
    },
  });
  expect(
    asked.ok(),
    `POST /api/tasks/${task.id}/messages: ${asked.status()}`,
  ).toBe(true);
  const question = await asked.json();
  // Oldest first, the question the task is parked on is the last of a
  // hundred-odd entries: the top of the pane does not show it.
  await page.addInitScript(() => {
    localStorage.setItem("grain.timelineOrder", "oldest");
  });

  await page.goto("/inbox");
  const row = page.locator(".inbox-item", {
    hasText: "Deep link parked question",
  });
  const title = row.getByRole("link", { name: "Deep link parked question" });
  await expect(title).toHaveAttribute(
    "href",
    `/tasks/${task.id}#comment=${question.id}`,
  );
  await title.click();
  await expect(page).toHaveURL(
    new RegExp(`/tasks/${task.id}#comment=${question.id}$`),
  );
  const entry = page.locator(`[data-comment-id="${question.id}"]`);
  await expect(entry).toContainText(
    "A question asked after a long conversation.",
  );
  await expect(entry).toBeInViewport();
  await expect(entry).toBeFocused();
  await expect(entry).toHaveClass(/comment-highlight/);
  await expect(
    page.locator(`[data-comment-id="${task.comments[0].id}"]`),
  ).not.toBeInViewport();
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

test("the linked comment takes focus, and a link naming no comment says so", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: { title: "Deep link focus", body: "x", approved: false },
  });
  const task = await filed.json();
  for (let i = 0; i < 5; i++) {
    await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: `Focus line ${i}` },
    });
  }
  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  const target = detail.comments[2];

  await page.goto(`/tasks/${task.id}#comment=${target.id}`);
  const entry = page.locator(`[data-comment-id="${target.id}"]`);
  await expect(entry).toHaveClass(/comment-highlight/);
  // For a screen reader to read it out, and for Tab to go on from it.
  await expect(entry).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(entry.getByRole("button", { name: "Copy link" })).toBeFocused();

  // A truncated or mistyped fragment is said, not silently ignored.
  for (const hash of ["#comment=abc", "#comment="]) {
    await page.goto("about:blank");
    await page.goto(`/tasks/${task.id}${hash}`);
    await expect(page.getByTestId("comment-link-notice")).toContainText(
      `This link's ${hash} does not name a comment.`,
    );
    await expect(page.locator(".comment-highlight")).toHaveCount(0);
  }

  // A comment of another task's is not this one's.
  const other = await (await request.get("/api/tasks/4")).json();
  const foreign = other.comments[0].id;
  await page.goto("about:blank");
  await page.goto(`/tasks/${task.id}#comment=${foreign}`);
  await expect(page.getByTestId("comment-link-notice")).toContainText(
    `Comment ${foreign} is not in this task's conversation.`,
  );
});

test("Copy link copies the comment's address without the Clipboard API", async ({
  page,
  request,
}) => {
  // A page served over plain HTTP from anywhere but localhost is not a
  // secure context and has no navigator.clipboard. The fallback used to
  // select a <textarea> appended to <body>, which the task pane's dialog
  // focus trap took focus straight back from: nothing was copied, and
  // the button said Copied.
  await page.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, "clipboard", {
      get: () => undefined,
      configurable: true,
    });
    window.__copied = [];
    window.addEventListener("copy", (e) => {
      window.__copied.push(e.clipboardData?.getData("text/plain") || "");
    });
  });
  const filed = await request.post("/api/tasks", {
    data: { title: "Deep link insecure copy", body: "x", approved: false },
  });
  const task = await filed.json();
  await request.post(`/api/tasks/${task.id}/comments`, {
    data: { body: "Copy my link over plain HTTP." },
  });
  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  const c = detail.comments[0];

  await page.goto(`/tasks/${task.id}`);
  const entry = page.locator(`[data-comment-id="${c.id}"]`);
  await entry.getByRole("button", { name: "Copy link" }).click();
  await expect(entry.getByRole("button", { name: "Copied" })).toBeVisible();
  const origin = new URL(page.url()).origin;
  expect(await page.evaluate(() => window.__copied)).toEqual([
    `${origin}/tasks/${task.id}#comment=${c.id}`,
  ]);
});

test("Back and Forward retrace a comment link followed in place", async ({
  page,
  request,
}) => {
  const target = await (
    await request.post("/api/tasks", {
      data: { title: "Deep link history target", body: "x", approved: false },
    })
  ).json();
  for (let i = 0; i < 40; i++) {
    await request.post(`/api/tasks/${target.id}/comments`, {
      data: { body: `History line ${i}` },
    });
  }
  const td = await (await request.get(`/api/tasks/${target.id}`)).json();
  const tc = td.comments[3].id;
  const source = await (
    await request.post("/api/tasks", {
      data: { title: "Deep link history source", body: "x", approved: false },
    })
  ).json();
  await request.post(`/api/tasks/${source.id}/comments`, {
    data: { body: `Go [there](/tasks/${target.id}#comment=${tc}).` },
  });

  await page.goto(`/tasks/${source.id}`);
  await page.evaluate(() => {
    window.__noReload = true;
  });
  await page.getByRole("link", { name: "there" }).click();
  const entry = page.locator(`[data-comment-id="${tc}"]`);
  await expect(entry).toBeInViewport();

  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/tasks/${source.id}$`));
  await expect(page.getByRole("link", { name: "there" })).toBeVisible();

  await page.goForward();
  await expect(page).toHaveURL(
    new RegExp(`/tasks/${target.id}#comment=${tc}$`),
  );
  await expect(entry).toBeInViewport();
  await expect(entry).toHaveClass(/comment-highlight/);
  expect(await page.evaluate(() => window.__noReload)).toBe(true);
});

// contrastAtPeak is the contrast of the linked comment's text against
// the highlight at its strongest (the keyframes' 0%), read off the pixels
// the page actually paints -- computed colors miss the overlay MUI's
// dark Paper draws as a background-image. The comment's most common
// pixel is the highlight; the pixel furthest from it, the text.
async function contrastAtPeak(page, el) {
  await el.evaluate((node) => {
    for (const a of node.getAnimations()) {
      a.pause();
      a.currentTime = 0;
    }
  });
  const png = (await el.screenshot({ animations: "allow" })).toString("base64");
  return page.evaluate(async (data) => {
    const img = new Image();
    img.src = `data:image/png;base64,${data}`;
    await img.decode();
    const canvas = document.createElement("canvas");
    canvas.width = img.width;
    canvas.height = img.height;
    const ctx = canvas.getContext("2d");
    ctx.drawImage(img, 0, 0);
    const px = ctx.getImageData(0, 0, img.width, img.height).data;
    const lum = (r, g, b) => {
      const f = (v) => {
        const c = v / 255;
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const counts = new Map();
    // Inset past the 2px ring at the edges.
    for (let y = 4; y < img.height - 4; y++) {
      for (let x = 4; x < img.width - 4; x++) {
        const i = (y * img.width + x) * 4;
        const k = `${px[i]},${px[i + 1]},${px[i + 2]}`;
        counts.set(k, (counts.get(k) || 0) + 1);
      }
    }
    const bg = [...counts.entries()].sort((a, b) => b[1] - a[1])[0][0];
    const lb = lum(...bg.split(",").map(Number));
    let best = 1;
    for (const k of counts.keys()) {
      const lt = lum(...k.split(",").map(Number));
      const [hi, lo] = lt > lb ? [lt, lb] : [lb, lt];
      best = Math.max(best, (hi + 0.05) / (lo + 0.05));
    }
    return best;
  }, png);
}

test("a highlighted comment's text keeps 4.5:1 contrast in light and dark", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: { title: "Deep link contrast", body: "x", approved: false },
  });
  const task = await filed.json();
  await request.post(`/api/tasks/${task.id}/comments`, {
    data: { body: "Text that has to stay readable while highlighted." },
  });
  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  const id = detail.comments[0].id;
  for (const colorScheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme });
    await page.goto("about:blank");
    await page.goto(`/tasks/${task.id}#comment=${id}`);
    const entry = page.locator(`[data-comment-id="${id}"]`);
    await expect(entry).toHaveClass(/comment-highlight/);
    expect(
      await contrastAtPeak(page, entry),
      `${colorScheme} body text on the highlight`,
    ).toBeGreaterThanOrEqual(4.5);
  }
});
