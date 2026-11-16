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

// Regressions from the responsive navigation and live-session walkthrough
// (docs/test-runs/2026-11-16-responsive-navigation-live-sessions.md):
//
// - a message sent while the one before it is still being posted queues
//   behind it, rather than the second Enter being ignored: each shows at
//   once as "Sending…" or "Queued" and the composer clears;
// - a message the server refuses stays in the chat as "Not sent" with
//   Retry, which hands focus back to the composer;
// - Delete hands focus to the list page it returns to (the Director's
//   title where the chat shows, the Sessions heading on a phone), not
//   <body>.
//
// The only interception is of the real POST /api/tasks/{id}/comments:
// held for a moment to make a post slow, or answered 503 once, as a
// session host that is restarting would; every other post reaches the
// demo.

import { expect, test } from "./fixtures";

const COMMENTS = "**/api/tasks/*/comments";

async function newSession(page, title) {
  await page.goto("sessions");
  await page.getByTestId("new-session").click();
  await page.getByTestId("new-session-name").fill(title);
  await page.getByTestId("new-session-message").fill("Hello there");
  await page.getByTestId("new-session-create").click();
  const pane = page.getByTestId("chat-pane");
  await expect(pane.getByRole("heading", { name: title })).toBeVisible();
  await expect(page.getByTestId("composer")).toBeFocused();
  await expect(page.getByTestId("chat-log")).toContainText("Hello there");
}

for (const size of [
  { name: "desktop", width: 1440, height: 900, focus: "Director" },
  { name: "short phone", width: 390, height: 667, focus: "Sessions" },
]) {
  test(`queued, failed and retried sends at ${size.name}, then Delete keeps focus`, async ({
    page,
  }) => {
    test.info().annotations.push({
      type: "allows-page-error",
      description: "the 503 the retry recovers from is logged by the browser",
    });
    await page.setViewportSize({ width: size.width, height: size.height });
    await newSession(page, `Send queue ${size.name} ${Date.now()}`);
    const composer = page.getByTestId("composer");
    const log = page.getByTestId("chat-log");

    // Hold the first post until both messages are on screen.
    let release;
    const held = new Promise((r) => (release = r));
    let posts = 0;
    await page.route(COMMENTS, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      if (++posts === 1) await held;
      return route.fallback();
    });
    await composer.fill("first follow-up");
    await composer.press("Enter");
    await expect(composer).toHaveValue("");
    await composer.fill("second follow-up");
    await composer.press("Enter");
    await expect(composer).toHaveValue("");
    await expect(page.getByTestId("pending-0")).toContainText("Sending…");
    await expect(page.getByTestId("pending-0")).toContainText(
      "first follow-up",
    );
    await expect(page.getByTestId("pending-1")).toContainText("Queued");
    await expect(page.getByTestId("pending-1")).toContainText(
      "second follow-up",
    );
    release();
    await expect(log.locator("article.is-pending")).toHaveCount(0);
    const sent = log.locator("article:not(.is-pending)");
    await expect(sent.last()).toContainText("second follow-up");
    const texts = await sent.allInnerTexts();
    expect(texts.at(-2)).toContain("first follow-up");
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
        body: JSON.stringify({ error: "the session host is restarting" }),
      });
    });
    await composer.fill("sent on the second try");
    await composer.press("Enter");
    const failed = log.locator("article.is-failed");
    await expect(failed).toContainText("Not sent");
    await expect(failed).toContainText("the session host is restarting");
    await failed.getByRole("button", { name: "Retry" }).click();
    await expect(composer).toBeFocused();
    await expect(log.locator("article.is-pending")).toHaveCount(0);
    await expect(sent.last()).toContainText("sent on the second try");
    await page.unroute(COMMENTS);

    // Reload: what was sent is the conversation now.
    await page.reload();
    await expect(sent.last()).toContainText("sent on the second try");

    await page.getByTestId("delete-session").click();
    await page.getByTestId("delete-confirm").click();
    await expect(page).toHaveURL(/\/next\/sessions$/);
    await expect(
      page.getByRole("heading", { name: size.focus, exact: true }),
    ).toBeFocused();
  });
}
