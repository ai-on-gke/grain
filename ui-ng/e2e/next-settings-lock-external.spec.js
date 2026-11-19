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

// From the 2026-11-18 Settings walkthrough
// (docs/test-runs/2026-11-18-user-system-settings.md): under an external
// config repository the settings lock is that repository's to set, and
// the server refuses it here with 409, so General does not offer it.
test.use({
  demoArgs: ["-external-config-repo", "https://github.com/example/walk-config"],
});

test("the settings lock is the external config repository's to set", async ({
  page,
}) => {
  await page.goto("settings/general");
  await expect(page.getByTestId("section-readonly-pill")).toBeVisible();
  await expect(page.getByTestId("switch-settings-locked")).toBeDisabled();
  await expect(page.getByTestId("settings-lock-hint")).toHaveText(
    /external config repository/,
  );
});
