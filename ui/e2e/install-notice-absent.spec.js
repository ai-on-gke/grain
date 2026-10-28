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

// The other half of install-notice.spec.js: a deployment that is not an
// unmigrated host-mode install -- the default `grain demo`, as a
// container install or a development daemon is -- shows no install
// notice anywhere.
test("any other deployment shows no install notice", async ({ page }) => {
  await page.goto("/system/health");
  await expect(page.getByTestId("system-health")).toBeVisible();
  await expect(page.getByTestId("install-notice-banner")).toHaveCount(0);
  await expect(page.getByTestId("health-install-notice")).toHaveCount(0);
  const config = await page.evaluate(async () =>
    (await fetch("/api/config")).json(),
  );
  expect(config.installNotice).toBeUndefined();
});
