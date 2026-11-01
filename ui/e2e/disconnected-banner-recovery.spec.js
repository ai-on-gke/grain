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

// grain/task-2258, found in the 2026-11-01 deployment-operations
// walkthrough ("Standing banners"): through an outage the main poll
// backs off to one tick in ten (half a minute), and the disconnected
// banner used to wait for one of those backed-off ticks to clear, long
// after grain was answering again. The restart watch keeps asking every
// three seconds, and its first answer now resets the backoff and polls
// at once. The outage here is every /api/ request refused for longer
// than the backoff takes to reach its cap, and it ends just after one of
// the poll's own backed-off ticks (its GET /api/config) was refused, so
// that without the fix its next tick, and the banner clearing, would be
// half a minute away.
test.setTimeout(150_000);

const OUTAGE_MS = 40_000;

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the disconnected banner clears within a tick of grain answering again (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    let down = false;
    let pollRefused = null;
    await page.route(/\/api\//, (r) => {
      if (!down) return r.continue();
      if (new URL(r.request().url()).pathname === "/api/config") {
        pollRefused?.();
      }
      return r.abort("connectionrefused");
    });
    await page.goto("/system");
    const banner = page.getByTestId("disconnected-banner");
    await expect(
      page.getByRole("heading", { level: 1, name: "System" }),
    ).toBeVisible();
    await expect(banner).toHaveCount(0);

    down = true;
    await expect(banner).toBeVisible({ timeout: 20_000 });
    await expect(banner).toContainText(/cannot be reached/i);
    await page.waitForTimeout(OUTAGE_MS);
    await expect(banner).toBeVisible();
    await new Promise((resolve) => {
      pollRefused = resolve;
    });

    down = false;
    const restored = Date.now();
    await expect(banner).toHaveCount(0, { timeout: 15_000 });
    expect(Date.now() - restored).toBeLessThan(5_500);
  });
}
