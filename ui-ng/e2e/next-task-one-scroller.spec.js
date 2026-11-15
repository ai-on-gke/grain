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

// One pane, one scroller on the task (task 5626). With a long thread, the
// Conversation tab of the task page and of the Tasks inspector fills its tab
// body: the thread is the only thing that scrolls, the composer sits pinned
// below it inside the pane, and wheeling the thread never moves the tab body
// or the page around it. On a phone the task scrolls as a whole instead, and
// its tab body does not scroll inside it. The long thread is served in place
// of the demo task's own comments, so the shared store is left alone.

const EXTRA = 40;

async function apiJson(baseURL, code, p) {
  const res = await fetch(`${baseURL}${p}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status} ${await res.text()}`);
  return res.json();
}

// longThread answers GET /api/tasks/{id} with EXTRA more comments ahead of
// the task's own. If-None-Match is dropped so the server sends a body.
async function longThread(page, id) {
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const body = await res.json();
    const extra = [];
    for (let i = 1; i <= EXTRA; i++) {
      extra.push({
        id: 900000 + i,
        author: "grain",
        authorKind: "automation",
        body: `Progress note ${i}: ${"the step went as planned and the next one is under way. ".repeat(3)}`,
        createdAt: new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString(),
      });
    }
    body.comments = [...extra, ...(body.comments ?? [])];
    await route.fulfill({ response: res, json: body });
  });
}

// scrolling lists the elements among sel that scroll and have something to
// scroll, by class name.
async function scrolling(page, selectors) {
  return page.evaluate((sels) => {
    const out = [];
    for (const s of sels) {
      for (const el of document.querySelectorAll(s)) {
        const oy = getComputedStyle(el).overflowY;
        if (
          (oy === "auto" || oy === "scroll") &&
          el.scrollHeight > el.clientHeight + 1
        ) {
          out.push(s);
        }
      }
    }
    return out;
  }, selectors);
}

async function openTask(baseURL, code) {
  const tasks = await apiJson(baseURL, code, "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : tasks.tasks;
  const open = list.find(
    (t) => t.state !== "closed" && !t.consoleSession && !t.interactive,
  );
  expect(open, "grain demo seeds an open task").toBeTruthy();
  return open;
}

const CANDIDATES = [".task-page", ".task-tab-body", ".convo-thread"];

async function expectThreadIsTheScroller(page, root) {
  const thread = page.locator(`${root} .convo-thread`);
  await expect(thread).toBeVisible();
  await expect
    .poll(() =>
      scrolling(
        page,
        CANDIDATES.map((c) => `${root} ${c}`),
      ),
    )
    .toEqual([`${root} .convo-thread`]);

  const box = async (s) => page.locator(`${root} ${s}`).first().boundingBox();
  const body = await box(".task-tab-body");
  const composer = await box(".convo-composer");
  const threadBox = await box(".convo-thread");
  const viewport = page.viewportSize();
  expect(composer, "the composer is shown").toBeTruthy();
  expect(composer.y + composer.height).toBeLessThanOrEqual(
    body.y + body.height + 0.5,
  );
  expect(body.y + body.height).toBeLessThanOrEqual(viewport.height + 0.5);
  expect(Math.abs(threadBox.y + threadBox.height - composer.y)).toBeLessThan(2);
  // The conversation fills the tab body rather than leaving a gap below.
  expect(body.y + body.height - (composer.y + composer.height)).toBeLessThan(
    40,
  );

  // Wheeling the thread scrolls it and nothing around it.
  const before = await thread.evaluate((el) => el.scrollTop);
  expect(before, "the thread opens at its latest message").toBeGreaterThan(0);
  await thread.hover();
  await page.mouse.wheel(0, -1500);
  await expect
    .poll(() => thread.evaluate((el) => el.scrollTop))
    .toBeLessThan(before);
  const outer = await page.evaluate(
    (r) =>
      [".task-page", ".task-tab-body"].map(
        (s) => document.querySelector(`${r} ${s}`).scrollTop,
      ),
    root,
  );
  expect(outer).toEqual([0, 0]);
}

test.describe("task conversation has one scroller", () => {
  test.beforeEach(({}, testInfo) => {
    test.skip(
      testInfo.project.name === "phone",
      "sets its own viewports, phone included",
    );
  });

  for (const vp of [
    { width: 1440, height: 900 },
    { width: 1280, height: 800 },
  ]) {
    test(`task page at ${vp.width}x${vp.height}: only the thread scrolls and the composer stays in view`, async ({
      page,
      demoServer,
      accessCode,
    }) => {
      const t = await openTask(demoServer, accessCode);
      await longThread(page, t.id);
      await page.setViewportSize(vp);
      await page.goto(`/next/tasks/${t.id}?tab=conversation`);
      await expectThreadIsTheScroller(page, "g-task-page");
    });
  }

  test("Tasks inspector at 1440x900: only the thread scrolls and the composer stays in the pane", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const t = await openTask(demoServer, accessCode);
    await longThread(page, t.id);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/next/tasks?task=${t.id}`);
    const pane = page.getByTestId("tasks-pane");
    await expect(pane).toContainText(`#${t.id}`);
    await pane.getByTestId("task-tab-conversation").click();
    await expectThreadIsTheScroller(page, '[data-testid="tasks-pane"]');
    const paneBox = await pane.boundingBox();
    const composer = await pane.locator(".convo-composer").boundingBox();
    expect(composer.y + composer.height).toBeLessThanOrEqual(
      paneBox.y + paneBox.height + 0.5,
    );
  });

  test("phone task page: the task scrolls as one and its tab body does not scroll inside it", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const t = await openTask(demoServer, accessCode);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/next/tasks/${t.id}?tab=runs`);
    await expect(page.locator(".task-tab-body")).toBeVisible();
    const tabBody = await page
      .locator(".task-tab-body")
      .evaluate((el) => getComputedStyle(el).overflowY);
    expect(tabBody).toBe("visible");
    // Whatever the task holds, its end can be scrolled into view.
    const end = await page.evaluate(() => {
      const p = document.querySelector(".task-page");
      p.scrollTop = p.scrollHeight;
      const body = document.querySelector(".task-tab-body");
      return {
        bodyBottom: body.getBoundingClientRect().bottom,
        pageBottom: p.getBoundingClientRect().bottom,
      };
    });
    expect(end.bodyBottom).toBeLessThanOrEqual(end.pageBottom + 0.5);
  });
});
