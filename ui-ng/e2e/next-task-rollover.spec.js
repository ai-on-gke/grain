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

// A rolled-over durable task (durable-tasks.design.md#DT-15) banners its
// successor at the top of its page, and the successor links back. The
// demo seeds the pair as "Watch the acme/widgets nightly build"
// (pkg/demo/demo_durable.go). Tagged @phone so the phone project checks
// the banner fits the screen too.

import { test, expect } from "./fixtures.js";

const TITLE = "Watch the acme/widgets nightly build";

async function rolledOverPair(page) {
  const res = await page.request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  // The list leaves closed tasks out by default, so it is the open
  // successor that is found here, by the predecessor it names.
  const to = tasks.find((t) => t.title === TITLE && t.rolledOverFrom);
  expect(to, "the demo seeds a rolled-over pair").toBeTruthy();
  return { from: String(to.rolledOverFrom), to: String(to.id) };
}

test(
  "a rolled-over task banners its successor, which links back",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("/next/");
    const { from, to } = await rolledOverPair(page);
    const width = page.viewportSize().width;

    await page.goto(`/next/task/${from}`);
    const banner = page.getByTestId("task-rolled-over-to");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(`Rolled over to #${to}`);
    const box = await banner.boundingBox();
    expect(box.x, "the banner's left edge is on screen").toBeGreaterThanOrEqual(
      0,
    );
    expect(
      box.x + box.width,
      "the banner's right edge is on screen",
    ).toBeLessThanOrEqual(width);
    // It sits under the header, above the conversation.
    const header = await page.locator("#task-title").boundingBox();
    expect(box.y).toBeGreaterThan(header.y);
    const link = banner.getByRole("link", { name: `#${to}` });
    const lb = await link.boundingBox();
    expect(lb.x + lb.width).toBeLessThanOrEqual(box.x + box.width);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
      "the page does not scroll sideways",
    ).toBeTruthy();

    await link.click();
    await expect(page).toHaveURL(new RegExp(`/next/task/${to}`));
    const back = page.getByTestId("task-rolled-over-from");
    await expect(back).toBeVisible();
    await expect(page.getByTestId("task-rolled-over-to")).toHaveCount(0);
    await back.getByRole("link", { name: `#${from}` }).click();
    await expect(page).toHaveURL(new RegExp(`/next/task/${from}`));
  },
);
