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

import { test, expect, type Locator } from "./fixtures.js";

test.use({ demoArgs: ["-as", "demo-operator"] });

const paragraphs = Array.from(
  { length: 45 },
  (_, i) =>
    `Paragraph ${i}: ${"Agent details must remain readable as content grows. ".repeat(8)}`,
).join("\n\n");

async function scrollTop(owner: Locator): Promise<number> {
  return owner.evaluate((el) => el.scrollTop);
}

async function insideViewport(control: Locator, owner: Locator) {
  await expect(control).toBeInViewport();
  const box = (await control.boundingBox())!;
  const bounds = (await owner.boundingBox())!;
  expect(box.y).toBeGreaterThanOrEqual(bounds.y - 1);
  expect(box.y + box.height).toBeLessThanOrEqual(bounds.y + bounds.height + 1);
}

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 1440, height: 500 },
  { width: 1024, height: 860 },
  { width: 2048, height: 1200 },
  { width: 390, height: 844 },
  { width: 390, height: 500 },
]) {
  const phone = viewport.width <= 720;
  test.describe(`Agent details at ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport, isMobile: phone, hasTouch: phone });

    test("long Details content has one reachable scroll viewport @dark", async ({
      page,
      demoServer,
      accessCode,
    }, testInfo) => {
      const res = await fetch(`${demoServer}/api/tasks`, {
        headers: { Authorization: `Bearer ${accessCode}` },
      });
      expect(res.ok).toBeTruthy();
      const data = await res.json();
      const task = (Array.isArray(data) ? data : data.tasks).find(
        (t: { state: string; consoleSession?: unknown }) =>
          t.state === "running" && !t.consoleSession,
      );
      expect(task).toBeTruthy();
      let description = paragraphs;
      await page.route(
        new RegExp(`/api/tasks/${task.id}(\\?.*)?$`),
        async (route) => {
          if (route.request().method() !== "GET") return route.continue();
          const headers = { ...route.request().headers() };
          delete headers["if-none-match"];
          const response = await route.fetch({ headers });
          const detail = await response.json();
          await route.fulfill({
            response,
            headers: { ...response.headers(), etag: "" },
            json: { ...detail, description },
          });
        },
      );

      // Enter follows the actual Agents selection flow: inspector on desktop,
      // full task page with a return link on phones (no mobile Agents pane).
      await page.goto("agents");
      const card = page.getByRole("article", {
        name: `#${task.id} ${task.title}`,
        exact: true,
      });
      await card.press("Enter");
      await expect(page).toHaveURL(
        phone
          ? new RegExp(`/next/task/${task.id}\\?from=agents`)
          : new RegExp(`/next/agents\\?task=${task.id}`),
      );
      await page.getByRole("link", { name: "Details", exact: true }).click();
      const body = page.getByRole("region", {
        name: "Task details",
        exact: true,
      });
      const taskPage = page.locator(".task-page");
      await expect(body).toContainText("Paragraph 44:");
      const compact = phone || viewport.height < 700 || viewport.width < 1080;
      const owner = compact ? taskPage : body;
      const header = page.getByRole("group", { name: "Task actions" });
      const headerTop = (await header.boundingBox())!.y;

      // Tab from the last view link reaches the labelled content region.
      await page.getByRole("link", { name: "Details", exact: true }).focus();
      await page.keyboard.press("Tab");
      await expect(body).toBeFocused();
      // A keyboard reader can focus the scroll surface and move through it.
      await expect(owner).toHaveAttribute("tabindex", "0");
      await owner.focus();
      await owner.press("PageDown");
      await expect.poll(() => scrollTop(owner)).toBeGreaterThan(0);
      await owner.press("Control+Home");
      await expect.poll(() => scrollTop(owner)).toBe(0);

      if (phone) {
        const bounds = (await owner.boundingBox())!;
        const cdp = await page.context().newCDPSession(page);
        const x = bounds.x + bounds.width / 2;
        const y = bounds.y + bounds.height - 25;
        await cdp.send("Input.dispatchTouchEvent", {
          type: "touchStart",
          touchPoints: [{ x, y }],
        });
        for (let distance = 10; distance <= 120; distance += 10) {
          await cdp.send("Input.dispatchTouchEvent", {
            type: "touchMove",
            touchPoints: [{ x, y: y - distance }],
          });
        }
        await cdp.send("Input.dispatchTouchEvent", {
          type: "touchEnd",
          touchPoints: [],
        });
        await expect.poll(() => scrollTop(owner)).toBeGreaterThan(10);
        await cdp.detach();
        // Let native touch momentum finish before keyboard input.
        await owner.press("Control+Home");
        await expect.poll(() => scrollTop(owner)).toBe(0);
      }

      // One wheel gesture reaches the actual bottom; a nested offscreen tab
      // must not consume the gesture and strand the final controls below it.
      const bounds = (await owner.boundingBox())!;
      await page.mouse.move(
        bounds.x + bounds.width / 2,
        bounds.y + bounds.height / 2,
      );
      await page.mouse.wheel(0, 100000);
      await expect
        .poll(() =>
          owner.evaluate(
            (el) => el.scrollHeight - el.clientHeight - el.scrollTop,
          ),
        )
        .toBeLessThan(2);
      const dependency = page.getByRole("textbox", {
        name: "Add dependency task ID",
      });
      await insideViewport(dependency, owner);
      if (compact) {
        expect(
          await body.evaluate((el) => el.scrollHeight - el.clientHeight),
        ).toBeLessThan(2);
      } else {
        expect(
          await taskPage.evaluate((el) => el.scrollHeight - el.clientHeight),
        ).toBeLessThan(2);
        expect((await header.boundingBox())!.y).toBe(headerTop);
      }
      expect(await page.evaluate(() => window.scrollY)).toBe(0);
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(viewport.width);

      if (viewport.width === 1440 && viewport.height === 500) {
        // Expansion and a polled prompt update must keep the same owner.
        const before = await owner.evaluate((el) => el.scrollHeight);
        await page
          .getByRole("button", {
            name: "+ Add API scope or method grant",
            exact: true,
          })
          .click();
        await expect(
          page.getByRole("group", { name: "Grant kind" }),
        ).toBeVisible();
        await expect
          .poll(() => owner.evaluate((el) => el.scrollHeight))
          .toBeGreaterThan(before);
        description += "\n\nLate loaded details: " + paragraphs;
        await expect(body).toContainText("Late loaded details:", {
          timeout: 20000,
        });
        await owner.focus();
        await owner.press("Control+End");
        await insideViewport(dependency, owner);
      }

      await page.screenshot({
        path: testInfo.outputPath("agent-details-bottom.png"),
      });
      await page.screenshot({
        path: testInfo.outputPath("agent-details-bottom.jpg"),
        quality: 15,
      });
      // Return to the first content and actions using only the keyboard.
      await owner.focus();
      await owner.press("Control+Home");
      await expect.poll(() => scrollTop(owner)).toBe(0);
      await expect(
        page.getByRole("button", { name: "Read prompt full screen" }),
      ).toBeInViewport();
      await expect(header).toBeInViewport();
      await page.screenshot({
        path: testInfo.outputPath("agent-details-top.png"),
      });
      await page.screenshot({
        path: testInfo.outputPath("agent-details-top.jpg"),
        quality: 15,
      });

      if (phone) {
        await page.getByRole("link", { name: "← Agents", exact: true }).click();
      } else {
        await page.getByRole("button", { name: "Close the task pane" }).click();
      }
      await expect(page.getByTestId("agents-pane")).toHaveCount(0);
      await expect(card).toBeVisible();
      // Use a short window so the seeded neighbouring list actually overflows.
      // Nearby navigation and its independent scroller remain usable.
      await page.setViewportSize({ width: viewport.width, height: 500 });
      await page.goto("tasks");
      const tasksBody = page.locator(".tasks-body");
      await tasksBody.hover();
      await page.mouse.wheel(0, 400);
      await expect.poll(() => scrollTop(tasksBody)).toBeGreaterThan(0);
    });
  });
}
