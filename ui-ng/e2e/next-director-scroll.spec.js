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

test("Director drawer keeps long history scrollable and composer visible @phone", async ({
  page,
  isMobile,
}) => {
  await page.setViewportSize({ width: page.viewportSize().width, height: 500 });
  // Keep real session/task metadata, but provide deterministic long history.
  let history = Array.from({ length: 45 }, (_, i) => ({
    id: 90000 + i,
    author: "operator",
    authorKind: "human",
    body: `History message ${i}: ${"A long conversation needs a bounded scroll viewport. ".repeat(8)}`,
    createdAt: "2026-11-15T12:00:00Z",
  }));
  await page.route(/\/api\/tasks\/[^/]+(\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const detail = await response.json();
    await route.fulfill({
      response,
      json: { ...detail, comments: history },
      headers: { ...response.headers(), etag: "" },
    });
  });
  await page.goto("/next/tasks");
  const tasksBody = page.locator(".tasks-body");
  await tasksBody.hover();
  await page.mouse.wheel(0, 300);
  await expect
    .poll(() => tasksBody.evaluate((el) => el.scrollTop))
    .toBeGreaterThan(0);
  await page.getByTestId("director-trigger").click();
  const mainScroll = await tasksBody.evaluate((el) => el.scrollTop);
  const drawer = page.getByTestId("director-drawer");
  const thread = drawer.getByRole("log");
  const composer = drawer.getByTestId("task-composer");
  await expect(thread).toContainText("History message 44");
  await expect
    .poll(() => thread.evaluate((el) => el.scrollHeight - el.clientHeight))
    .toBeGreaterThan(500);
  const bounds = await drawer.boundingBox();
  const field = await composer.boundingBox();
  await expect(drawer.getByTestId("act-send")).toBeInViewport();
  expect(field.y + field.height).toBeLessThanOrEqual(bounds.y + bounds.height);
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(
    page.viewportSize().height,
  );

  // Native wheel input must move the thread, without moving the shell.
  await thread.hover();
  await page.mouse.wheel(0, -100000);
  await expect.poll(() => thread.evaluate((el) => el.scrollTop)).toBe(0);
  await expect(thread.locator(".msg").first()).toBeInViewport();

  if (isMobile) {
    const rect = await thread.boundingBox();
    const cdp = await page.context().newCDPSession(page);
    const x = rect.x + rect.width / 2;
    const y = rect.y + rect.height - 10;
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x, y }],
    });
    for (let distance = 10; distance <= 80; distance += 10) {
      await cdp.send("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [{ x, y: y - distance }],
      });
    }
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchEnd",
      touchPoints: [],
    });
    await expect
      .poll(() => thread.evaluate((el) => el.scrollTop))
      .toBeGreaterThan(10);
    await expect
      .poll(async () => {
        await thread.evaluate((el) =>
          el.scrollTo({ top: 0, behavior: "instant" }),
        );
        return thread.evaluate((el) => el.scrollTop);
      })
      .toBe(0);
    await cdp.detach();
  }

  expect(await tasksBody.evaluate((el) => el.scrollTop)).toBe(mainScroll);

  // A refreshed conversation must preserve the reader's position.
  history = [
    ...history,
    { ...history[0], id: 99999, body: "New incoming message" },
  ];
  await expect(thread).toContainText("New incoming message", {
    timeout: 20000,
  });
  await expect.poll(() => thread.evaluate((el) => el.scrollTop)).toBe(0);
  await composer.fill("The composer remains usable while reading history");
  await expect(composer).toHaveValue(
    "The composer remains usable while reading history",
  );
  await drawer.getByRole("button", { name: /New message/ }).click();
  await expect
    .poll(() =>
      thread.evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop),
    )
    .toBeLessThan(2);

  await page.setViewportSize({ width: page.viewportSize().width, height: 900 });
  await expect(composer).toBeInViewport();
  const posted = page.waitForRequest(
    (req) =>
      /\/api\/tasks\/[^/]+\/comments$/.test(req.url()) &&
      req.method() === "POST",
  );
  await composer.press("Shift+Enter");
  await expect(composer).toHaveValue(
    "The composer remains usable while reading history\n",
  );
  await composer.press("Enter");
  expect(JSON.parse((await posted).postData()).body).toContain(
    "The composer remains usable",
  );
  await page.getByTestId("director-drawer-close").click();
  await expect(page.locator("#main")).not.toHaveAttribute("inert", "");
  await tasksBody.hover();
  await page.mouse.wheel(0, -100000);
  await expect.poll(() => tasksBody.evaluate((el) => el.scrollTop)).toBe(0);
});
