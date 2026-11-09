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

import { expect, test } from "./fixtures.js";

// grain/task-3619, found in the 2026-11-08 deployment-operations
// walkthrough (§1.3): a daemon that accepts the connection and then never
// answers -- `kill -STOP`, a deadlock, a proxy holding the socket open --
// left the shell's poll waiting on its GET /api/config forever, so the
// disconnected banner never came up and the page went on showing its last
// data as if it were live. The poll's config read now has a timeout
// (App.jsx's CONFIG_POLL_TIMEOUT_MS, ten seconds), and two of them in a
// row raise the banner. Here that read is held unanswered rather than
// refused, then answered again.
test.setTimeout(120_000);

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the disconnected banner comes up when grain stops answering, and clears when it answers (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    let silent = false;
    await page.route(/\/api\/config$/, (r) => {
      // Never fulfilled, continued nor aborted: the request stays open
      // until the page itself gives up on it.
      if (silent && r.request().method() === "GET") return;
      return r.continue();
    });
    await page.goto("/system");
    await expect(
      page.getByRole("heading", { level: 1, name: "System" }),
    ).toBeVisible();
    const banner = page.getByTestId("disconnected-banner");
    await expect(banner).toHaveCount(0);

    silent = true;
    const stopped = Date.now();
    // Two timeouts and the tick between them, about 25 seconds.
    await expect(banner).toBeVisible({ timeout: 45_000 });
    expect(Date.now() - stopped).toBeGreaterThan(15_000);
    await expect(banner).toContainText(/cannot be reached/i);
    await expect(banner).toContainText(
      /accepted the connection but sent no answer within 10 seconds/i,
    );
    // The longer sentence wraps inside the banner rather than pushing it
    // past the edge of the screen.
    const box = await banner.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 0.5);
    expect(
      await banner.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    // What was on screen is still there to read under it.
    await expect(
      page.getByRole("heading", { level: 1, name: "System" }),
    ).toBeVisible();

    silent = false;
    // However far the poll has backed off by now (ten ticks at most).
    await expect(banner).toHaveCount(0, { timeout: 45_000 });
  });
}
