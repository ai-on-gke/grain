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

// The Sandboxes tab draws its panels only over a sandbox pool, which the
// observability demo has.
test.use({ demoArgs: ["-observability"] });

// Regression found by the 2026-11-01 deployment-operations walkthrough
// (docs/test-runs/2026-11-01-deployment-operations.md): System →
// Sandboxes' empty Persistent sandboxes panel told the reader to ask the
// director for "a staging environment for bwsalmon/grain on main" --
// grain's own repository, retired on 2026-10-28 and not one any
// deployment's director could stage.
test("the empty Persistent sandboxes panel's example names no retired grain repository", async ({
  page,
}) => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/system/sandboxes");
    const panel = page.getByTestId("persistent-sandboxes");
    await expect(panel).toContainText(
      "'set up a staging environment for acme/web on main'",
    );
    await expect(panel).not.toContainText("bwsalmon");
    // The example wraps inside the panel rather than running off a phone.
    const box = await panel.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 0.5);
  }
});
