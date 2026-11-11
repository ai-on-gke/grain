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
import { alertRow, closeAlerts, openAlerts } from "./alerts.js";

// docs/test-runs/2026-11-11-deployment-operations.md, finding 1: a
// usage-limit pause's resume instant was printed as the clock time
// alone, so one lifting three days from now read "until 9:13:06 AM" --
// this morning -- in the bell and on System → Health.
//
// -agent-paused begins a real pause, which the demo ends six hours on.
// Only GET /api/config is touched, to move that same pause's end three
// days out: a provider window that long is something only a live
// provider can cause. Nothing is posted.
test.use({ demoArgs: ["-agent-paused"] });

const DAYS = 3;

test("a usage-limit pause that ends on another day names that day", async ({
  page,
}) => {
  const later = new Date(Date.now() + DAYS * 86400e3).toISOString();
  let zone = "";
  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      zone = body.timeZone || "";
      body.agentPause.until = later;
      body.agentPause.secondsRemaining = DAYS * 86400;
      for (const p of body.agentPause.pauses || []) {
        p.until = later;
        p.secondsRemaining = DAYS * 86400;
      }
      await route.fulfill({ response, json: body });
    },
  );

  await page.goto("/");
  const day = await page.evaluate(
    ([at, tz]) =>
      new Date(at).toLocaleString(undefined, {
        year: "numeric",
        month: "numeric",
        day: "numeric",
        ...(tz ? { timeZone: tz } : {}),
      }),
    [later, zone],
  );

  await openAlerts(page);
  await expect(alertRow(page, /usage limit reached/i).first()).toContainText(
    `until ${day}`,
  );
  await closeAlerts(page);

  await page.goto("/system");
  await expect(page.getByTestId("system-health")).toContainText(`until ${day}`);
});

test("a pause that ends today keeps the clock time alone", async ({ page }) => {
  await page.goto("/");
  const config = await (await page.request.get("/api/config")).json();
  expect(config.agentPause?.paused).toBe(true);
  const pause = config.agentPause.pauses?.[0] || config.agentPause;
  const [today, endsToday] = await page.evaluate(
    ([at, tz]) => {
      const opts = {
        year: "numeric",
        month: "numeric",
        day: "numeric",
        ...(tz ? { timeZone: tz } : {}),
      };
      const d = new Date().toLocaleString(undefined, opts);
      return [d, new Date(at).toLocaleString(undefined, opts) === d];
    },
    [pause.until, config.timeZone || ""],
  );
  // The demo's six-hour pause can cross midnight late in the day, and
  // then the date is rightly printed.
  test.skip(!endsToday, "the demo's pause ends tomorrow at this hour");

  await openAlerts(page);
  const row = alertRow(page, /usage limit reached/i).first();
  await expect(row).toContainText(/until \d{1,2}:\d{2}/);
  await expect(row).not.toContainText(today);
});
