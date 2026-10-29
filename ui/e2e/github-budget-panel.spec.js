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

// System → Health's GitHub budget panel (docs/github-api-rate-limits.md
// §4), over the demo's fixed hour (pkg/demo's demoGitHubCalls): the
// projection is said, the callers are listed, and nothing in it pushes
// a phone's page sideways.
for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the GitHub budget panel on Health fits a ${name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/system");
    // A phone opens System on its list of tabs; Health is one tap in.
    if (name === "phone") {
      await page
        .getByRole("list", { name: "System tabs" })
        .getByRole("button", { name: "Health" })
        .click();
    }
    const panel = page.getByTestId("health-github-budget");
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("1,650 of 5,000 calls left this hour");
    await expect(page.getByTestId("health-github-runs-out")).toContainText(
      "runs out in",
    );
    const polls = page.getByTestId("health-github-polls");
    await expect(polls).toContainText("2 of 3 tracked PRs backed off");
    await expect(polls).toContainText("poll interval median 2m 0s, max 8m 0s");
    const callers = page.getByTestId("health-github-callers");
    await expect(callers).toContainText("sync");
    await expect(callers).toContainText("review-threads");

    const box = await panel.boundingBox();
    const tableBox = await callers.boundingBox();
    expect(tableBox.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(tableBox.x + tableBox.width).toBeLessThanOrEqual(
      box.x + box.width + 1,
    );
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);
  });
}
