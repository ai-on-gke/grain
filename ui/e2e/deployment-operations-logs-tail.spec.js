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

// The System Logs tab follows the tail of a journal while its reader is
// at the bottom of it (LogsPage.jsx). Found by the 2026-10-12 deployment
// operations walkthrough (docs/test-runs/2026-10-12-deployment-
// operations.md): a reader on the tail who searched and then cleared the
// search was left at the *oldest* line -- the narrowed text had pulled
// the scroll to the top -- and the next 5s poll then threw them back to
// the newest one. These need a real layout engine (scrollTop, clamping),
// which is why they are here rather than in LogsPage.test.jsx's jsdom.
test.use({ demoArgs: ["-observability"] });

function position(page) {
  return page.locator("pre.logs-view").evaluate((el) => ({
    top: Math.round(el.scrollTop),
    max: el.scrollHeight - el.clientHeight,
  }));
}

async function openLogs(page) {
  await page.goto("/system/logs");
  const view = page.locator("pre.logs-view");
  await expect(view).toContainText("grain-daemon");
  // Opens on the tail: the pane is scrolled to its newest line.
  await expect
    .poll(async () => {
      const { top, max } = await position(page);
      return max > 0 && top === max;
    })
    .toBe(true);
  return view;
}

test("clearing a search returns a reader on the tail to the newest line", async ({
  page,
}) => {
  await openLogs(page);
  const search = page.getByLabel("Search logs");
  await search.fill("no-such-log-line");
  await expect(page.locator("pre.logs-view")).toHaveText(
    "(no matching log lines)",
  );
  await search.fill("");
  await expect(page.locator("pre.logs-view")).toContainText("grain-daemon");
  // At once, not at the next poll.
  const { top, max } = await position(page);
  expect(max).toBeGreaterThan(0);
  expect(top).toBe(max);
});

test("searching and clearing leaves a reader who scrolled up where they were", async ({
  page,
}) => {
  const view = await openLogs(page);
  await view.hover();
  await page.mouse.wheel(0, -2000);
  await expect
    .poll(async () => (await position(page)).top)
    .toBeLessThan((await position(page)).max - 100);
  const search = page.getByLabel("Search logs");
  await search.fill("merge-queue");
  await expect(view).not.toContainText("dispatch:");
  await search.fill("");
  await expect(view).toContainText("dispatch:");
  // A poll lands (REFRESH_MS is 5s) without pulling them to the bottom.
  await page.waitForTimeout(5500);
  const { top, max } = await position(page);
  expect(top).toBeLessThan(max - 100);
});
