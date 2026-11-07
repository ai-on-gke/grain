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

// GET /api/upgrade resolves every CLI's run location, which asks grain
// hostd what it offers; behind an unreachable hostd it took five seconds,
// and Settings → Upgrade was a blank pane under its heading for all of
// them (docs/test-runs/2026-11-04-deployment-operations.md). The read is
// held in the browser until the test lets it through to the real demo
// server, so "slow" is deterministic here.
test("Settings → Upgrade shows it is reading its status until the read answers", async ({
  page,
}) => {
  let release;
  const held = new Promise((resolve) => (release = resolve));
  await page.route("**/api/upgrade", async (route) => {
    if (route.request().method() === "GET") await held;
    await route.fallback();
  });

  // The old address, as a bookmark would carry it.
  await page.goto("/system/upgrade");
  await expect(page).toHaveURL(/\/settings\/upgrade$/);
  const reading = page.getByRole("progressbar", {
    name: "Reading the upgrade status",
  });
  await expect(reading).toBeVisible();

  release();
  await expect(reading).toHaveCount(0);
  // The plain demo has no upgrader, which the pane says once it knows.
  await expect(page.getByText(/^Not available:/)).toBeVisible();
});
