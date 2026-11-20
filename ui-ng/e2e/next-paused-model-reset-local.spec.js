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

// A paused model's reset is printed on the viewer's clock with the zone
// named and a countdown beside it -- "resets 11:15 AM PST (in 2h 15m)" --
// rather than in UTC for somebody to translate. The paused usage scope on
// Telemetry, and the dispatch banner that reads it, are laid over the
// demo's own GET /api/usage, since a demo has spent no quota. Set
// GRAIN_UI_SHOTS=<dir> to write the pages at each size and theme.

import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

const SHOTS = process.env.GRAIN_UI_SHOTS || "";
const ZONE = "America/Los_Angeles";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

// zoneAbbrev is the short name the browser gives ZONE at ms -- "PST" or
// "PDT" -- so the spec holds on either side of a daylight-saving change.
function zoneAbbrev(ms) {
  return new Intl.DateTimeFormat("en-US", {
    timeZone: ZONE,
    timeZoneName: "short",
  })
    .formatToParts(new Date(ms))
    .find((p) => p.type === "timeZoneName").value;
}

// otherLocalDay is whether a and b (ms) fall on different days in ZONE.
function otherLocalDay(a, b) {
  const day = (ms) =>
    new Date(ms).toLocaleDateString("en-US", { timeZone: ZONE });
  return day(a) !== day(b);
}

for (const size of SIZES) {
  for (const theme of THEMES) {
    test(`a paused model's reset reads on the local clock — ${size.name}, ${theme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.addInitScript(
        ([t, z]) => {
          localStorage.setItem("grain.themeMode", t);
          localStorage.setItem("grain.timeZone", z);
        },
        [theme, ZONE],
      );
      const until = Date.now() + (2 * 60 + 15) * 60 * 1000;
      await page.route(/\/api\/usage(\?.*)?$/, async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        body.scopes = [
          {
            connection: "gemini-pro",
            connectionName: "Gemini Pro",
            model: "gemini-3-pro",
            runs: 14,
            inputTokens: 1200000,
            outputTokens: 84000,
            cacheReadTokens: 0,
            cacheWriteTokens: 0,
            used: 1284000,
            window: "5h",
            limit: 1300000,
            pausedUntil: new Date(until).toISOString(),
            burnPerHour: 410000,
          },
        ];
        await route.fulfill({ response: res, json: body });
      });

      await page.goto("/next/telemetry");
      const reset = page.getByTestId("usage-scope-reset");
      await expect(reset).toHaveCount(1);
      const abbrev = zoneAbbrev(until);
      // A reset that falls on another local day is dated ("Nov 16,
      // 12:19 AM PST"), so a run late in the evening sees the date too.
      const day = otherLocalDay(Date.now(), until)
        ? "[A-Z][a-z]{2} \\d{1,2}, "
        : "";
      await expect(reset).toContainText(
        new RegExp(
          `resets ${day}\\d{1,2}:\\d{2}\\s[AP]M ${abbrev} \\(in 2h 1[45]m\\)`,
        ),
      );
      await expect(reset).not.toContainText("UTC");
      // Beside its Paused pill, on the pill's own line, so a table of
      // paused scopes grows no taller (prod-heavy-polish's height budget).
      const pill = reset.locator("xpath=..").locator(".status-pill");
      const pillBox = await pill.boundingBox();
      const resetBox = await reset.boundingBox();
      if (size.name === "desktop") {
        expect(resetBox.x).toBeGreaterThanOrEqual(pillBox.x + pillBox.width);
        expect(resetBox.y).toBeLessThan(pillBox.y + pillBox.height);
      }
      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBe(0);
      if (SHOTS) {
        fs.mkdirSync(SHOTS, { recursive: true });
        await reset.scrollIntoViewIfNeeded();
        await page.screenshot({
          path: path.join(SHOTS, `telemetry-reset-${size.name}-${theme}.png`),
        });
      }

      // The shell's dispatch banner names the same reset, on the same clock.
      await expect(page.getByText(/· resets /).first()).toContainText(
        new RegExp(
          `resets ${day}\\d{1,2}:\\d{2}\\s[AP]M ${abbrev} \\(in 2h 1[45]m\\)`,
        ),
      );
    });
  }
}
