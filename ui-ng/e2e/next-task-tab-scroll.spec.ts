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

const description = Array.from(
  { length: 40 },
  (_, i) =>
    `## Section ${i + 1}\n\n${"Keep the task description readable across tab navigation. ".repeat(8)}`,
).join("\n\n");

const top = (locator: Locator) => locator.evaluate((el) => el.scrollTop);

for (const viewport of [
  { width: 1024, height: 600 },
  { width: 1024, height: 860 },
  { width: 1440, height: 500 },
  { width: 1440, height: 900 },
  { width: 1440, height: 1200 },
]) {
  test.describe(`Task inspector tabs at ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport });

    test("Back restores Runs actions and Details keeps its reading position", async ({
      page,
      demoServer,
      accessCode,
    }, testInfo) => {
      const res = await fetch(`${demoServer}/api/tasks`, {
        headers: { Authorization: `Bearer ${accessCode}` },
      });
      expect(res.ok).toBeTruthy();
      const data = await res.json();
      const tasks = (Array.isArray(data) ? data : data.tasks).filter(
        (t: { consoleSession?: unknown }) => !t.consoleSession,
      );
      const [task, other] = tasks;
      expect(task).toBeTruthy();
      expect(other).toBeTruthy();
      let prompt = description;
      for (const { id } of [task, other]) {
        await page.route(
          new RegExp(`/api/tasks/${id}(\\?.*)?$`),
          async (route) => {
            if (route.request().method() !== "GET") return route.continue();
            const headers = { ...route.request().headers() };
            delete headers["if-none-match"];
            const response = await route.fetch({ headers });
            await route.fulfill({
              response,
              headers: { ...response.headers(), etag: "" },
              json: { ...(await response.json()), description: prompt },
            });
          },
        );
      }

      await page.goto(`tasks?tab=all&task=${task.id}&pane=runs`);
      const inspector = page.getByTestId("tasks-pane");
      const taskPage = inspector.locator(".task-page");
      const body = inspector.locator(".task-tab-body");
      const actions = inspector.locator(".task-actions");
      const runs = inspector.getByTestId("task-tab-runs");
      const details = inspector.getByTestId("task-tab-details");
      await expect(runs).toHaveAttribute("aria-current", "true");
      await expect.poll(() => top(taskPage)).toBe(0);

      await details.click();
      await expect(details).toHaveAttribute("aria-current", "true");
      // Compact Details scrolls the outer page; roomy Details scrolls its body.
      const bounds = (await inspector.boundingBox())!;
      const owner = bounds.height < 700 || bounds.width < 600 ? taskPage : body;
      await owner.hover();
      await page.mouse.wheel(0, 600);
      await expect.poll(() => top(owner)).toBeGreaterThan(300);
      const readingPosition = await top(owner);
      await page.screenshot({
        path: testInfo.outputPath("details-before-back.png"),
      });

      // No upward wheel gesture or focus-induced scroll should be needed.
      await page.goBack();
      await expect(runs).toHaveAttribute("aria-current", "true");
      await expect.poll(() => top(taskPage)).toBe(0);
      await expect(actions).toBeInViewport({ ratio: 1 });
      await page.screenshot({
        path: testInfo.outputPath("runs-after-back.png"),
      });

      await page.goForward();
      await expect(details).toHaveAttribute("aria-current", "true");
      await expect
        .poll(async () => Math.abs((await top(owner)) - readingPosition))
        .toBeLessThan(2);

      if (viewport.width === 1024 && viewport.height === 600) {
        prompt += "\n\nA polled update must keep the reader in place.";
        await expect(body).toContainText("A polled update", { timeout: 20000 });
        await expect
          .poll(async () => Math.abs((await top(owner)) - readingPosition))
          .toBeLessThan(2);
      }

      // A restored reader is still keyboard-scrollable back to its controls.
      await owner.focus();
      await owner.press("Control+Home");
      await expect.poll(() => top(owner)).toBe(0);
      await expect(actions).toBeInViewport({ ratio: 1 });
      await owner.press("PageDown");
      let previous = 0;
      await expect
        .poll(
          async () => {
            const position = await top(owner);
            const stopped = position > 50 && position === previous;
            previous = position;
            return stopped;
          },
          { intervals: [100] },
        )
        .toBe(true);
      const keyboardPosition = await top(owner);

      // Keyboard tab activation uses the same restoration as browser history.
      // Keep focus() from moving the outer page to the tab before navigation.
      await runs.evaluate((el: HTMLElement) =>
        el.focus({ preventScroll: true }),
      );
      await runs.press("Enter");
      await expect(runs).toHaveAttribute("aria-current", "true");
      await expect.poll(() => top(taskPage)).toBe(0);
      await expect(actions).toBeInViewport({ ratio: 1 });
      await details.focus();
      await details.press("Enter");
      await expect(details).toHaveAttribute("aria-current", "true");
      await expect
        .poll(async () => Math.abs((await top(owner)) - keyboardPosition))
        .toBeLessThan(2);

      // Selecting another task reuses the shared component, not its scroll memory.
      await page.getByTestId(`open-${other.id}`).click();
      await expect(inspector.locator("#task-title")).toContainText(other.title);
      await expect.poll(() => top(taskPage)).toBe(0);
      await expect.poll(() => top(body)).toBe(0);
      await expect(actions).toBeInViewport({ ratio: 1 });
      expect(await page.evaluate(() => window.scrollY)).toBe(0);
    });
  });
}
