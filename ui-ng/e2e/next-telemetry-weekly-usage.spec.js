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

// /next/telemetry's token usage table shows each scope's weekly quota
// beside its interval window (grain/task-5618): a connection paused on its
// provider's weekly limit with nothing run in the last 5h used to read
// "0 used" and Active-looking meters while dispatch was blocked on the
// spent week.
//
// The demo learns a weekly capacity for gemini-2.5-pro (its 7d meter); the
// weekly pause is added to the demo's own GET /api/usage answer on
// gemini-2.5-flash, since a demo is never refused by a provider. Set
// GRAIN_UI_SHOTS=<dir> to write the table at each size and theme.

import AxeBuilder from "@axe-core/playwright";
import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

const SHOTS = process.env.GRAIN_UI_SHOTS || "";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

for (const size of SIZES) {
  for (const theme of THEMES) {
    test(`Telemetry shows the weekly quota beside the interval — ${size.name}, ${theme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      const until = new Date(Date.now() + 3 * 24 * 3600 * 1000).toISOString();
      await page.route(/\/api\/usage(\?.*)?$/, async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        for (const s of body.scopes ?? []) {
          if (s.model !== "gemini-2.5-flash") continue;
          Object.assign(s, {
            used: 0,
            runs: 0,
            pausedUntil: until,
            pauseHorizon: "weekly",
            weeklyPausedUntil: until,
            weeklyResetAt: until,
            weeklyLimit: s.weeklyUsed,
            weeklyLimitSource: "learned",
          });
        }
        await route.fulfill({ response: res, json: body });
      });

      await page.goto("/next/telemetry");
      const table = page.getByTestId("usage-scopes-table");
      await table.scrollIntoViewIfNeeded();
      await expect(table).toBeVisible();
      await expect(
        table.getByRole("columnheader", { name: "Interval & Weekly Usage" }),
      ).toBeVisible();

      // The learned weekly capacity: a 7d line with its limit.
      const pro = page.getByTestId("usage-weekly-gemini-pro:gemini-2.5-pro");
      await expect(pro).toContainText("7d");
      await expect(pro).toContainText("(learned)");

      // The weekly pause: said in words, on the meter's row, not as an
      // empty interval.
      const flashRow = table.locator("tbody tr", {
        has: page.getByTestId("usage-weekly-gemini-pro:gemini-2.5-flash"),
      });
      const spent = flashRow.getByRole("status");
      await expect(spent).toContainText("Weekly limit reached");
      await expect(spent).toContainText("resets");
      await expect(flashRow).toContainText("Paused · weekly limit");

      // Each weekly line sits under its interval line, inside its cell.
      for (const row of [flashRow, table.locator("tbody tr", { has: pro })]) {
        const cell = row.locator("td").nth(4);
        const cellBox = await cell.boundingBox();
        const interval = await cell.locator("div").first().boundingBox();
        const weekly = await cell.locator(".tel-weekly-usage").boundingBox();
        expect(weekly.y).toBeGreaterThanOrEqual(interval.y + interval.height);
        for (const box of [interval, weekly]) {
          expect(box.x).toBeGreaterThanOrEqual(cellBox.x - 1);
          expect(box.x + box.width).toBeLessThanOrEqual(
            cellBox.x + cellBox.width + 1,
          );
          expect(box.y + box.height).toBeLessThanOrEqual(
            cellBox.y + cellBox.height + 1,
          );
        }
      }
      const spentBox = await spent.boundingBox();
      const flashCell = await flashRow.locator("td").nth(4).boundingBox();
      expect(spentBox.x + spentBox.width).toBeLessThanOrEqual(
        flashCell.x + flashCell.width + 1,
      );

      // The table scrolls inside its own region; the page never sideways.
      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBe(0);

      const scan = await new AxeBuilder({ page })
        .include('[data-testid="usage-scopes-table"]')
        .analyze();
      expect(
        scan.violations.filter((v) =>
          ["serious", "critical"].includes(v.impact ?? ""),
        ),
      ).toEqual([]);

      if (SHOTS) {
        fs.mkdirSync(SHOTS, { recursive: true });
        await page.waitForLoadState("networkidle").catch(() => {});
        await page.getByTestId("telemetry-usage").screenshot({
          path: path.join(SHOTS, `telemetry-weekly-${size.name}-${theme}.png`),
        });
      }
    });
  }
}
