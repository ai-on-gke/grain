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

// Settings → Models → Token usage and budgets (UsageCard.jsx) over the
// demo's seeded usage (pkg/demo/demo_token_usage.go): the table of
// connections and models, the estimate and the mode, laid out inside
// their card at desktop and phone widths.

import { expect, test } from "./fixtures.js";

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Models token usage fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/models#usage");

    const card = page.getByTestId("usage-card");
    await expect(card).toBeVisible();
    const rows = page.getByTestId("usage-row");
    await expect(rows.filter({ hasText: "gemini-2.5-pro" })).toContainText(
      "4.0M",
    );
    // The learned limit is marked as learned.
    await expect(rows.filter({ hasText: "gemini-2.5-flash" })).toContainText(
      "900k~",
    );
    await expect(page.getByTestId("usage-workers")).toContainText(
      "of max_workers",
    );
    await expect(page.getByTestId("usage-advisory").first()).toContainText(
      "budget runs out in",
    );

    // The table may scroll inside its card on a phone; the page may not.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
    const cardBox = await card.boundingBox();
    const table = await page
      .getByTestId("usage-table")
      .locator("xpath=..")
      .boundingBox();
    expect(table.x).toBeGreaterThanOrEqual(cardBox.x - 1);
    expect(table.x + table.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 1,
    );
  });
}
