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

import { expect, test } from "./fixtures.js";

// System → Health's counters a layer registered (ui.RegisterHealthCounters),
// over the demo's stand-in sign-in verifier (pkg/demo's
// demoHealthCounters): the section is named, its counters sorted with
// their counts in a right-hand column, and nothing in it pushes a
// phone's page sideways.
for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the health counters panel on Health fits a ${name}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/system");
    if (name === "phone") {
      await page
        .getByRole("list", { name: "System tabs" })
        .getByRole("button", { name: "Health" })
        .click();
    }
    const panel = page.getByTestId("health-counters");
    await expect(panel).toBeVisible();
    const section = panel.getByTestId("health-counters-section").first();
    await expect(section).toContainText("sign-in verification (demo)");
    await expect(section).toContainText("Since the daemon started.");

    const rows = section.locator("tbody tr");
    await expect(rows.first()).toContainText("proxy:admitted");
    const keys = await rows.locator("td:first-child").allTextContents();
    expect(keys).toEqual([...keys].sort());
    expect(keys).toContain("proxy:host_mismatch");

    // The count sits at the right of its row, inside the panel.
    const box = await panel.boundingBox();
    const row = await rows.first().boundingBox();
    const count = await rows.first().locator("td").nth(1).boundingBox();
    expect(count.x + count.width).toBeGreaterThan(row.x + row.width - 2);
    expect(row.x + row.width).toBeLessThanOrEqual(box.x + box.width + 1);
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);
  });
}
