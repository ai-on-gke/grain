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

// Found by the 2026-10-06 repository walkthrough
// (docs/test-runs/2026-10-06-repository-release-memory-ui.md).
test.describe("Repository walkthrough regressions (2026-10-06)", () => {
  // "v3.0..x" was stored as a release whose branches git can never
  // create; it is refused now, and nothing is added.
  test("refuses a release name git cannot use as a branch", async ({
    page,
  }) => {
    await page.goto("/repos/acme/widgets/releases");
    const name = page.getByLabel("New release name");
    await name.fill("v3.0..x");
    const created = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/repos/acme/widgets/releases") &&
        r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Create release" }).click();
    expect((await created).status()).toBe(400);
    await expect(page.getByText("invalid release name")).toBeVisible();
    await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases$/);
  });
});
