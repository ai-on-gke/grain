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

// The -observability demo (cmd/grain/demo_observability.go) is the one
// with a sandbox host, so it is the one whose Sandboxes page renders the
// storage section these pointers sit in.
test.use({ demoArgs: ["-observability"] });

// Found by the 2026-10-05 walkthrough
// (docs/test-runs/2026-10-05-deployment-operations.md): the Sandboxes
// page sent CI history to "Settings → General", a pane and tab that do
// not hold it. Every pointer now names the Settings section that does
// (docs/design/settings-panel.md split Engine & Concurrency into Engine
// and CI), and following it finds the card it promised.
test("sends CI history to the Settings section that holds it", async ({
  page,
}) => {
  await page.goto("/system/sandboxes");
  await expect(
    page.getByText(/Settings → CI, under CI\s+history/),
  ).toBeVisible();
  await expect(page.getByText(/System → (?!Caches)/)).toHaveCount(0);

  await page.getByTestId("top-bar-settings").click();
  await page.getByTestId("settings-nav-ci").click();
  await expect(page).toHaveURL(/\/settings\/ci$/);
  for (const card of ["CI history", "CI job cache", "CI artifacts"]) {
    await expect(page.getByRole("heading", { name: card })).toBeVisible();
  }
});
