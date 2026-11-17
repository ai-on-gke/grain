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

// One pane, one scroller (design/design/ui-design-book.design.md): the
// Inbox inspector's body scrolls, so its inline conversation thread must
// not scroll too. A short thread window used to scroll beside the body's
// own scrollbar on the same right edge, and nobody could tell which one the
// wheel would move. The body follows the end of the thread the way the
// thread's own scroller did. Expand and full screen still hand the thread
// its own scroller, with the rest of the body set aside.

async function apiPost(baseURL, code, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`POST ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

// What scrolls: every element in the inspector whose content overflows a
// scrolling box, by data-testid or class.
const scrollers = (page) =>
  page.getByTestId("next-inbox-inspector").evaluate((root) =>
    [root, ...root.querySelectorAll("*")]
      .filter((el) => {
        const oy = getComputedStyle(el).overflowY;
        return (
          (oy === "auto" || oy === "scroll") &&
          el.scrollHeight > el.clientHeight + 1
        );
      })
      .map((el) => el.className.split(" ")[0]),
  );

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 1024, height: 700 },
  { width: 390, height: 844 },
]) {
  test(`a long thread in the inspector leaves one scroller at ${viewport.width}x${viewport.height}`, async ({
    page,
    demoServer,
    accessCode,
  }) => {
    await page.setViewportSize(viewport);
    const title = `Inspector scroll ${viewport.width} ${Date.now()}`;
    const { id } = await apiPost(demoServer, accessCode, "/api/tasks", {
      title,
      approved: true,
    });
    try {
      for (let i = 1; i <= 14; i++) {
        await apiPost(demoServer, accessCode, `/api/tasks/${id}/comments`, {
          body: `Progress note ${i}: ${"the run is still working through the change. ".repeat(4)}`,
        });
      }
      await apiPost(demoServer, accessCode, `/api/tasks/${id}/messages`, {
        summary: "Ship the inspector change today?",
        body: "Last message in the thread.",
      });

      await page.goto("inbox");
      const row = page.getByTestId("queue-row").filter({ hasText: title });
      await row.locator(".row-main").click();
      const convo = page.getByTestId("inspector-conversation");
      await expect(convo.getByText("Progress note 14:")).toBeAttached();
      // Opening the task lands on the newest message, as the thread's own
      // scroller did: the body follows the end of the conversation.
      await expect(
        convo.getByText("Last message in the thread."),
      ).toBeInViewport();

      const thread = convo.locator(".convo-thread");
      await expect
        .poll(() => thread.evaluate((el) => el.scrollHeight - el.clientHeight))
        .toBeLessThanOrEqual(1);
      expect(await scrollers(page)).toEqual(["inspector-body"]);

      // While the top of the thread is on screen, the composer is pinned to
      // the bottom of the body rather than a whole thread further down.
      const body = page.locator(".inspector-body");
      await thread.evaluate((el) => el.scrollIntoView({ block: "start" }));
      const bodyBox = await body.boundingBox();
      const composerBox = await convo.locator(".convo-composer").boundingBox();
      expect(
        Math.abs(
          composerBox.y + composerBox.height - (bodyBox.y + bodyBox.height),
        ),
      ).toBeLessThanOrEqual(2);

      // Scrolled up to the top of the pane, a message that arrives (read
      // again when the row is chosen again) raises "New messages" on
      // screen, above the pinned composer, and the pill takes the reader to
      // it. Narrower, the inspector is a sheet over the queue and the row
      // cannot be chosen again; the rules that place the pill do not depend
      // on the width.
      if (viewport.width >= 1440) {
        await body.evaluate((el) => (el.scrollTop = 0));
        await page.evaluate(
          () =>
            new Promise((r) =>
              requestAnimationFrame(() => requestAnimationFrame(r)),
            ),
        );
        await apiPost(demoServer, accessCode, `/api/tasks/${id}/comments`, {
          body: "A note that arrived while reading the top.",
        });
        await row.locator(".row-main").click();
        const pill = convo.getByTestId("convo-jump");
        await expect(pill).toBeInViewport({ ratio: 1 });
        const pillBox = await pill.boundingBox();
        const dockBox = await convo.locator(".convo-composer").boundingBox();
        expect(pillBox.y + pillBox.height).toBeLessThanOrEqual(dockBox.y);
        await pill.click();
        await expect(pill).toHaveCount(0);
        await expect(
          convo.getByText("A note that arrived while reading the top."),
        ).toBeInViewport();
      }

      // Back at the end, content that renders late (an image loading in the
      // last message, say) is followed: the body stays on the end.
      await body.evaluate((el) => (el.scrollTop = el.scrollHeight));
      // Let that scroll's event land first: a reader is at the end before
      // content grows, not in the same frame as it.
      await page.evaluate(
        () =>
          new Promise((r) =>
            requestAnimationFrame(() => requestAnimationFrame(r)),
          ),
      );
      await convo
        .locator(".convo-thread .msg")
        .last()
        .evaluate((el) => {
          const late = document.createElement("div");
          late.style.height = "300px";
          late.textContent = "Late-rendered content.";
          el.append(late);
        });
      await expect(convo.locator(".convo-composer")).toBeInViewport({
        ratio: 1,
      });
      await expect
        .poll(() =>
          body.evaluate(
            (el) => el.scrollHeight - el.scrollTop - el.clientHeight,
          ),
        )
        .toBeLessThanOrEqual(2);

      // Expanded, the thread is the one scroller instead.
      await convo.getByTestId("convo-expand").click();
      await expect(convo).toHaveClass(/is-expanded/);
      await expect.poll(() => scrollers(page)).toEqual(["convo-thread"]);
      await convo.getByTestId("convo-expand").click();
      await expect(convo).not.toHaveClass(/is-expanded/);

      // Full screen, the dialog's thread scrolls and the composer sits
      // inside the dialog: the inline rules above do not reach it.
      await convo.getByTestId("convo-fullscreen").click();
      const modal = page.getByTestId("convo-modal");
      await expect(modal).toBeVisible();
      const modalThread = modal.locator(".convo-thread");
      expect(
        await modalThread.evaluate((el) => ({
          overflowY: getComputedStyle(el).overflowY,
          overflows: el.scrollHeight > el.clientHeight + 1,
        })),
      ).toEqual({ overflowY: "auto", overflows: true });
      const modalBox = await modal.boundingBox();
      const modalComposer = await modal
        .locator(".convo-composer")
        .boundingBox();
      expect(modalComposer.y + modalComposer.height).toBeLessThanOrEqual(
        modalBox.y + modalBox.height + 1,
      );
      await page.keyboard.press("Escape");
      await expect(modal).toBeHidden();
    } finally {
      await fetch(`${demoServer}/api/tasks/${id}/close`, {
        method: "POST",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
  });
}
