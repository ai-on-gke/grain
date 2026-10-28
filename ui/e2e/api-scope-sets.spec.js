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

// Settings → Capabilities → API scope sets (docs/agent-api.md, "Scope
// sets") against grain demo, which declares docs' own ops/triage example
// (pkg/demo). The card lists grain's built-ins and the config-repo set
// with what it resolves to, and its rows keep their chips inside the
// card at desktop and phone widths.

for (const [label, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`lists built-in and config-repo scope sets inside the card (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities#api-scope-sets");
    const triage = page.getByTestId("api-scope-set-ops/triage");
    await expect(triage).toBeVisible();
    await expect(
      triage.getByText("config repo · tables/api_scope_set.json"),
    ).toBeVisible();
    await expect(
      triage.getByText("tasks:write @ bwsalmon/grain"),
    ).toBeVisible();
    await expect(triage.getByText("tasks:admin", { exact: false })).toHaveCount(
      1, // only in "denies tasks:admin", never as a resolved chip
    );
    await expect(triage.getByText("requires a person")).toHaveCount(0);
    const builtin = page.getByTestId("api-scope-set-grain/deployment-ops");
    await expect(builtin.getByText("built-in")).toBeVisible();
    await expect(builtin.getByText("requires a person")).toBeVisible();

    // Every chip of the row sits inside the row: nothing overflows the
    // card at this width.
    const row = await triage.boundingBox();
    for (const chip of await triage.locator(".MuiChip-root").all()) {
      const box = await chip.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(row.x - 0.5);
      expect(box.x + box.width).toBeLessThanOrEqual(row.x + row.width + 0.5);
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}
