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

// Settings under an external config repository (-external-config-repo),
// from the 2026-11-17 Settings walkthrough
// (docs/test-runs/2026-11-17-user-system-settings.md). demoArgs forces a
// worker of its own, so it has a file of its own.
test.use({
  demoArgs: ["-external-config-repo", "https://github.com/example/walk-config"],
});

test("Settings lock is held by the config repository", async ({ page }) => {
  await page.goto("/next/settings/general");
  await expect(page.getByTestId("open-in-config-repo-link")).toBeVisible();
  await expect(page.getByTestId("switch-settings-locked")).toBeDisabled();
  await expect(page.getByTestId("settings-lock-hint")).toContainText(
    "managed in the external config repository",
  );
});

test("Access, CLI and Audit stay usable", async ({ page }) => {
  await page.goto("/next/settings/access");
  await expect(page.getByTestId("input-principal-entry")).toBeEnabled();
  await expect(page.getByTestId("settings-config-repo-banner")).toHaveCount(0);
  await page.goto("/next/settings/cli");
  await expect(page.getByTestId("copy-cli-install")).toBeEnabled();
  await page.goto("/next/settings/audit");
  await expect(page.getByTestId("refresh-audit-btn")).toBeEnabled();
});
