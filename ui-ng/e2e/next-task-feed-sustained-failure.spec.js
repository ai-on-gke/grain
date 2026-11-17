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

// One failed poll is not news (grain task 5812). The /next/ Inbox used
// to say "Could not read tasks; retrying." the moment a single GET
// /api/tasks failed -- a 503 through a sign-in proxy, a read that timed
// out while the daemon was busy -- and take it back three seconds later.
// Now a transient failure keeps the last good list and says nothing,
// and only failure that lasts (ui-ng/src/app/services/request-trouble.ts:
// three reads in a row, or thirty seconds) puts up the one banner, over
// the list it last read rather than in place of it.
//
// The cold case -- a list never read at all says so at once -- is
// next-inbox-intake-regressions.spec.js's.
test.use({ demoArgs: ["-stalled-tasks"] });

const TASKS = /\/api\/tasks(\?.*)?$/;
const ROW = "Backfill the audit log retention job";

// failTaskReads makes the next `times` GET /api/tasks (every one, when
// times is Infinity) fail with a 503, and reports how many it has failed.
async function failTaskReads(context, times) {
  const failed = { count: 0 };
  await context.route(TASKS, (route) => {
    const req = route.request();
    const isList = new URL(req.url()).pathname === "/api/tasks";
    if (req.method() !== "GET" || !isList || failed.count >= times) {
      return route.continue();
    }
    failed.count++;
    return route.fulfill({
      status: 503,
      contentType: "application/json",
      body: '{"error":"upstream request timed out"}',
    });
  });
  return failed;
}

test("a transient task-list failure shows no banner and keeps the list", async ({
  page,
  context,
}) => {
  await page.goto("/next/inbox");
  await expect(page.getByText(ROW).first()).toBeVisible();

  // Two reads fail back to back -- the feed's own and the Inbox's,
  // both asked for at once when the tab comes back into view, the way a
  // burst of task events asks for them -- and the next one succeeds.
  // Under the sustain rule that is still a transient.
  const failed = await failTaskReads(context, 2);
  const recovered = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === "/api/tasks" &&
      r.request().method() === "GET" &&
      (r.status() === 200 || r.status() === 304),
    { timeout: 45_000 },
  );
  await page.evaluate(() =>
    document.dispatchEvent(new Event("visibilitychange")),
  );
  await expect.poll(() => failed.count, { timeout: 30_000 }).toBe(2);
  await expect(page.getByTestId("load-error")).toHaveCount(0);
  await recovered;
  await expect(page.getByTestId("load-error")).toHaveCount(0);
  await expect(page.getByText(ROW).first()).toBeVisible();
});

test("sustained task-list failure shows one banner over the last good list, and clears", async ({
  page,
  context,
}) => {
  await page.goto("/next/inbox");
  await expect(page.getByText(ROW).first()).toBeVisible();

  const failed = await failTaskReads(context, Infinity);
  await expect.poll(() => failed.count, { timeout: 30_000 }).toBe(1);
  await expect(page.getByTestId("load-error")).toHaveCount(0);

  const banner = page.getByTestId("load-error");
  await expect(banner).toHaveText("Could not read tasks; retrying.", {
    timeout: 45_000,
  });
  await expect(banner).toHaveCount(1);
  expect(failed.count).toBeGreaterThanOrEqual(2);
  // The list it last read is still there under the banner.
  await expect(page.getByText(ROW).first()).toBeVisible();

  await context.unroute(TASKS);
  await expect(banner).toHaveCount(0, { timeout: 90_000 });
});
