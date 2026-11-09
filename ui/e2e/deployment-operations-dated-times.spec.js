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

// A moment that is not today says which day it was
// (docs/test-runs/2026-11-08-deployment-operations.md).
//
// The overcommit controller's adjustments and the alerts bell's
// "Since"/"Until" used to print the clock time alone, so an adjustment
// made on 24 October, or a config-repo sync failing for three days,
// read as earlier today.
import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-observability"] });

test("an overcommit adjustment from another day carries its date", async ({
  page,
}) => {
  await page.goto("/system/sandboxes");
  // The demo's one adjustment is dated 2026-10-24 (demo_observability.go).
  await expect(page.getByTestId("overcommit-adjustments")).toContainText(
    /CPU 2\.50× → 2\.75× — .*\(.*2026.*\)/,
  );
});

test("an alert raised days ago says since which day", async ({ page }) => {
  const since = new Date(Date.now() - 3 * 86400e3).toISOString();
  // A failing config-repo sync takes a broken remote to cause; the
  // daemon's own answer is kept and only that field is added to it.
  await page.route(/\/api\/config(\?|$)/, async (route) => {
    const response = await route.fetch();
    const json = await response.json();
    json.stateRepoSyncFailure = {
      since,
      error: "push rejected",
      repo: "grain/config",
    };
    await route.fulfill({ response, json });
  });
  await page.goto("/system/health");
  await page.getByTestId("alerts-button").click();
  const row = page
    .getByTestId("alerts-panel")
    .getByTestId("alert-row")
    .filter({ hasText: "Settings are not being exported" });
  const day = await page.evaluate(
    (at) => new Date(at).toLocaleDateString(),
    since,
  );
  await expect(row).toContainText(`Since ${day}`);
});
