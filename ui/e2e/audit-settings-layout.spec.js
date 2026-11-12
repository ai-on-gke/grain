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

// Settings → Audit over the demo's seeded rows (pkg/demo seedDemoAudit):
// config changes newest first and named by label, the team and secret
// audit beside them with their principals as stored, each table inside
// its card -- on a wide screen and on a phone, with nothing pushing the
// page sideways.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

// A demo of its own (a demoArgs no other file uses): every configuration
// write through the API adds a config_audit row, and on a worker's shared
// server the suite's other files make enough of them to push the seeded
// rows -- the operator's a day old -- off the newest the page reads,
// however many that is.
test.use({ demoArgs: ["-open=false", "-as", "audit-settings-layout"] });

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Settings → Audit fits a ${name} screen`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/audit");

    const config = page.getByTestId("config-audit-table");
    await expect(config).toBeVisible();
    // A table on a wide screen, one block per entry on a phone.
    const rows = config.locator('[data-testid^="config-audit-row-"]');
    // Find the seeded rows by what they say rather than counting them.
    const alice = rows.filter({ hasText: "max concurrent runs 4 → 6" });
    const operator = rows.filter({ hasText: "default model set to" });
    await expect(alice).toContainText("alice@example.com");
    await expect(operator).toContainText("operator (access code)");
    await expect(config).toContainText("schedule:nightly-triage");
    // Newest first: ids only grow, so they fall down the list, and alice's
    // change 40 minutes ago sits above the operator's a day ago.
    const ids = await rows.evaluateAll((els) =>
      els.map((el) => Number(el.dataset.testid.split("-").pop())),
    );
    expect(ids).toEqual([...ids].sort((a, b) => b - a));
    const aliceId = Number(
      (await alice.getAttribute("data-testid")).split("-").pop(),
    );
    const operatorId = Number(
      (await operator.getAttribute("data-testid")).split("-").pop(),
    );
    expect(ids.indexOf(aliceId)).toBeLessThan(ids.indexOf(operatorId));

    const team = page.getByTestId("team-audit-table");
    await expect(team).toContainText("push demo/gadgets");
    await expect(team).toContainText("stripe-api-key");
    const secrets = page.getByTestId("secret-audit-table");
    await expect(secrets).toContainText("github-app/private-key");
    await expect(secrets).toContainText("alice@example.com");

    // A table may scroll inside its card on a phone; the page may not.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    // Each log sits inside its card, and none needs a sideways scroll of
    // its own either: a table fits a wide card, and a phone stacks.
    for (const anchor of ["config-audit", "team-audit", "secret-audit"]) {
      const card = await page.locator(`#${anchor}`).boundingBox();
      const log = page.getByTestId(`${anchor}-table`);
      const box = await log.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(card.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(card.x + card.width + 1);
      const scrolls = await log.evaluate((el) => {
        const outer = el.tagName === "TABLE" ? el.parentElement : el;
        return outer.scrollWidth > outer.clientWidth + 1;
      });
      expect(scrolls, `${anchor} scrolls sideways`).toBe(false);
    }
    if (name === "phone") {
      await expect(config).toHaveAttribute("data-layout", "stacked");
      // The label column lines up down an entry, with its value beside it.
      const who = alice.getByText("Who", { exact: true });
      const whoBox = await who.boundingBox();
      const value = await alice.getByText("alice@example.com").boundingBox();
      expect(value.x).toBeGreaterThan(whoBox.x + whoBox.width - 1);
      expect(Math.abs(value.y - whoBox.y)).toBeLessThan(4);
    }
    if (name === "desktop") {
      // On a wide screen the config table needs no sideways scroll: its
      // Change column is on screen beside Who.
      const box = await config.boundingBox();
      const card = await page.locator("#config-audit").boundingBox();
      expect(box.width).toBeLessThanOrEqual(card.width + 1);
      const who = await alice.locator("td").nth(1).boundingBox();
      const change = await alice.locator("td").nth(3).boundingBox();
      expect(change.x).toBeGreaterThan(who.x + who.width - 1);
      expect(Math.abs(change.y - who.y)).toBeLessThan(2);
    }
  });
}
