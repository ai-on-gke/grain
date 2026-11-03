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

// The Inbox opened cold while GET /api/tasks fails: it must not promise
// that nothing is waiting, because it has not read the tasks at all
// (2026-11-03 task intake walkthrough). Notifications and local-script
// calls are answered empty so that nothing else on the page hides the
// claim.

import { test, expect } from "./fixtures.js";

test("the Inbox does not say nothing is waiting when the task list fails to load", async ({
  page,
}) => {
  await page.route(/\/api\/tasks(\?.*)?$/, (route) =>
    route.fulfill({ status: 500, body: "stopped by the test" }),
  );
  await page.route(/\/api\/notifications(\?.*)?$/, (route) =>
    route.fulfill({ json: [] }),
  );
  await page.route(/\/api\/local-script-approvals(\?.*)?$/, (route) =>
    route.fulfill({ json: [] }),
  );
  await page.goto("/inbox");
  const main = page.locator("main.inbox");
  await expect(main.getByText(/The task list could not be read/)).toBeVisible();
  await expect(main.getByText(/Nothing is waiting on you/)).toHaveCount(0);

  // Once the list reads, the page says what it always said.
  await page.unroute(/\/api\/tasks(\?.*)?$/);
  await page.route(/\/api\/tasks(\?.*)?$/, (route) =>
    route.fulfill({ json: [] }),
  );
  await expect(main.getByText(/Nothing is waiting on you/)).toBeVisible({
    timeout: 15000,
  });
  await expect(main.getByText(/The task list could not be read/)).toHaveCount(
    0,
  );
});
