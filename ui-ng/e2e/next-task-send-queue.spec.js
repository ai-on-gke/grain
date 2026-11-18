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

// Task chat's send queue, from the responsive navigation and live-session
// walkthrough (docs/test-runs/2026-11-16-responsive-navigation-live-sessions.md,
// step 6): on /next/task/<id> a reply shows at once as "Sending…", the
// composer clears and stays usable while it posts, a second reply queues
// behind it as "Queued", and one the server refuses stays as "Not sent"
// with its reason until Retry sends it.
//
// The only interception is of the real POST /api/tasks/{id}/comments:
// held for a moment to make a post slow, or answered 503 once; every other
// post reaches the demo.

import { expect, test } from "./fixtures";

const COMMENTS = "**/api/tasks/*/comments";

async function apiPost(baseURL, code, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`POST ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

async function closeQuietly(baseURL, code, id) {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

for (const size of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`task chat queues replies and retries a refused one at ${size.name}`, async ({
    page,
    demoServer,
    accessCode,
  }) => {
    test.info().annotations.push({
      type: "allows-page-error",
      description: "the 503 the retry recovers from is logged by the browser",
    });
    await page.setViewportSize({ width: size.width, height: size.height });
    const { id } = await apiPost(demoServer, accessCode, "/api/tasks", {
      title: `Task send queue ${size.name} ${Date.now()}`,
      approved: true,
    });
    try {
      await apiPost(demoServer, accessCode, `/api/tasks/${id}/messages`, {
        summary: "Fix the retry too?",
        body: "Should I also fix the retry?",
      });
      await page.goto(`task/${id}`);
      const composer = page.getByTestId("task-composer");
      await expect(composer).toHaveAttribute(
        "aria-label",
        "Reply to the agent",
      );
      const thread = page.locator("g-conversation .convo-thread").first();
      const pending = page.locator("article.msg.is-pending");

      // Hold the first post until both replies are on screen.
      let release;
      const held = new Promise((r) => (release = r));
      let posts = 0;
      await page.route(COMMENTS, async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        if (++posts === 1) await held;
        return route.fallback();
      });
      await composer.fill("Yes, fix both");
      await composer.press("Control+Enter");
      await expect(composer).toBeEnabled();
      await expect(composer).toHaveValue("");
      await expect(page.getByTestId("pending-0")).toContainText("Sending…");
      await expect(page.getByTestId("pending-0")).toContainText(
        "Yes, fix both",
      );
      await composer.fill("And add a test for it");
      await composer.press("Control+Enter");
      await expect(composer).toHaveValue("");
      await expect(page.getByTestId("pending-1")).toContainText("Queued");
      await expect(page.getByTestId("pending-1")).toContainText(
        "And add a test for it",
      );
      expect(posts).toBe(1);

      // The pending bubbles sit after the thread, inside it, within its
      // width.
      const [threadBox, bubbleBox] = await Promise.all([
        thread.boundingBox(),
        page.getByTestId("pending-1").boundingBox(),
      ]);
      expect(bubbleBox.x).toBeGreaterThanOrEqual(threadBox.x - 1);
      expect(bubbleBox.x + bubbleBox.width).toBeLessThanOrEqual(
        threadBox.x + threadBox.width + 1,
      );

      release();
      await expect(pending).toHaveCount(0);
      const sent = page.locator("article.msg:not(.is-pending)");
      await expect(sent.last()).toContainText("And add a test for it");
      const texts = await sent.allInnerTexts();
      expect(texts.at(-2)).toContain("Yes, fix both");
      await page.unroute(COMMENTS);

      // A refused post stays as "Not sent" until Retry sends it.
      let refuse = true;
      await page.route(COMMENTS, (route) => {
        if (route.request().method() !== "POST" || !refuse) {
          return route.fallback();
        }
        refuse = false;
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: "the server is restarting" }),
        });
      });
      await composer.fill("Sent on the second try");
      await composer.press("Control+Enter");
      const failed = page.locator("article.msg.is-failed");
      await expect(failed).toContainText("Not sent");
      await expect(failed).toContainText("the server is restarting");
      await expect(composer).toBeEnabled();
      await failed.getByRole("button", { name: "Retry" }).click();
      await expect(composer).toBeFocused();
      await expect(pending).toHaveCount(0);
      await expect(sent.last()).toContainText("Sent on the second try");
      await page.unroute(COMMENTS);

      // Reload: what was sent is the conversation now.
      await page.reload();
      await expect(sent.last()).toContainText("Sent on the second try");

      // Closed while a reply is out: the one queued behind it stays as
      // "Not sent", readable, with Discard in place of Edit.
      let releaseLast;
      const heldLast = new Promise((r) => (releaseLast = r));
      let lastPosts = 0;
      await page.route(COMMENTS, async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        if (++lastPosts === 1) await heldLast;
        return route.fallback();
      });
      await composer.fill("Last word before it closes");
      await composer.press("Control+Enter");
      await composer.fill("Too late for this one");
      await composer.press("Control+Enter");
      await expect(page.getByTestId("pending-1")).toContainText("Queued");
      await apiPost(demoServer, accessCode, `/api/tasks/${id}/close`);
      releaseLast();
      const stuck = page.locator("article.msg.is-failed");
      await expect(stuck).toHaveCount(1);
      await expect(stuck).toContainText("Too late for this one");
      await expect(stuck).toContainText("can no longer receive this comment");
      await expect(stuck.getByRole("button", { name: "Edit" })).toHaveCount(0);
      const [stuckBox, threadBox2] = await Promise.all([
        stuck.boundingBox(),
        thread.boundingBox(),
      ]);
      expect(stuckBox.x + stuckBox.width).toBeLessThanOrEqual(
        threadBox2.x + threadBox2.width + 1,
      );
      await stuck.getByRole("button", { name: "Discard" }).click();
      await expect(pending).toHaveCount(0);
      expect(lastPosts).toBe(1);
      await page.unroute(COMMENTS);
    } finally {
      await closeQuietly(demoServer, accessCode, id);
    }
  });
}
