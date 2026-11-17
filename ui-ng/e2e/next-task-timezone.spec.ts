// Copyright 2026 The Grain Authors
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

// Filing a task from /next/ carries the requester's IANA time zone in
// X-Grain-Time-Zone so the daemon records task.timeZone for paused-model
// reset notices (docs/test-plans/2026-11-16.md §5 item 4). And when a
// provider refuses with a multi-day weekly limit, both /next/telemetry and
// /next/settings/models name the weekly horizon and render the dated reset
// on the viewer's clock.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

function zoneAbbrev(zone: string, ms: number): string {
  return (
    new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "short",
    })
      .formatToParts(new Date(ms))
      .find((p) => p.type === "timeZoneName")?.value ?? ""
  );
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

async function patchWeeklyPause(page: Page, untilMs: number) {
  const untilIso = new Date(untilMs).toISOString();
  const reason = "You've hit your weekly limit · resets in 72h (UTC)";

  await page.route(/\/api\/config(\?.*)?$/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.agentPause = {
      paused: true,
      until: untilIso,
      reason,
      secondsRemaining: Math.max(1, Math.round((untilMs - Date.now()) / 1000)),
      pauses: [
        {
          connection: "claude-primary",
          connectionName: "Claude Primary",
          until: untilIso,
          reason,
          secondsRemaining: Math.max(
            1,
            Math.round((untilMs - Date.now()) / 1000),
          ),
          canLift: true,
        },
      ],
    };
    await route.fulfill({ response: res, json: body });
  });

  await page.route(/\/api\/usage(\?.*)?$/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.warnings = [
      "default model claude-opus-4-6 on Claude Primary hit its weekly limit and is paused; work falls back to gemini-3.1-pro",
    ];
    body.scopes = [
      {
        connection: "claude-primary",
        connectionName: "Claude Primary",
        model: "",
        runs: 2,
        inputTokens: 800000,
        outputTokens: 200000,
        cacheReadTokens: 0,
        cacheWriteTokens: 0,
        used: 0,
        window: "5h",
        pausedUntil: untilIso,
        pauseHorizon: "weekly",
        weeklyUsed: 1000000,
        weeklyLimit: 1000000,
        weeklyLimitSource: "learned",
        weeklyResetAt: untilIso,
        weeklyPausedUntil: untilIso,
        burnPerHour: 0,
      },
    ];
    await route.fulfill({ response: res, json: body });
  });
}

for (const tz of [
  { id: "America/Los_Angeles", accepted: ["America/Los_Angeles"] },
  { id: "Asia/Kolkata", accepted: ["Asia/Kolkata", "Asia/Calcutta"] },
]) {
  test.describe(`time zone ${tz.id}`, () => {
    test.use({ timezoneId: tz.id });

    test(`filing a task from /next/tasks sends X-Grain-Time-Zone`, async ({
      page,
    }) => {
      await page.addInitScript(() => localStorage.removeItem("grain.timeZone"));

      let sentHeader: string | null = null;
      page.on("request", (req) => {
        if (req.url().endsWith("/api/tasks") && req.method() === "POST") {
          sentHeader = req.headers()["x-grain-time-zone"] ?? null;
        }
      });

      await page.goto("/next/tasks");
      const fab = page.getByTestId("rail-new-task");
      await expect(fab).toBeVisible();
      await fab.click();
      await expect(page.getByTestId("new-task-sheet")).toBeVisible();
      await page.getByTestId("nt-title").fill(`Timezone filing (${tz.id})`);

      const respPromise = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/tasks") && res.request().method() === "POST",
      );
      await page.getByTestId("new-task-submit").click();
      const resp = await respPromise;
      expect(resp.ok()).toBe(true);

      const created = await resp.json();
      const taskId = created?.id ?? created?.task?.id;
      expect(taskId).toBeTruthy();
      expect(tz.accepted).toContain(sentHeader);
    });

    test(`weekly pause banner and reset pills name the local zone and date @phone @dark`, async ({
      page,
      axe,
    }) => {
      await page.addInitScript(() => localStorage.removeItem("grain.timeZone"));
      const untilMs = Date.now() + 72 * 3600 * 1000;
      const abbrev = escapeRegExp(zoneAbbrev(tz.id, untilMs));
      const datedResetRe = new RegExp(
        `resets [A-Z][a-z]{2} \\d{1,2}, \\d{1,2}:\\d{2}\\s[AP]M ${abbrev} \\(in 7[12]h`,
      );

      await patchWeeklyPause(page, untilMs);

      // 1. /next/telemetry
      await page.goto("/next/telemetry");
      const reset = page.getByTestId("usage-scope-reset");
      await expect(reset).toHaveCount(1);
      await expect(reset).toContainText(datedResetRe);
      await expect(reset).not.toContainText("UTC");

      await expect(
        page.getByText(
          /Weekly model quota exceeded — Claude Primary development paused/,
        ),
      ).toBeVisible();

      const telSideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(telSideways).toBe(0);
      expect(await axe()).toEqual([]);

      // 2. /next/settings/models
      await page.goto("/next/settings/models");
      const banner = page.getByTestId("models-quota-banner");
      await expect(banner).toBeVisible();
      await expect(banner).toContainText(
        "Weekly model quota exceeded — Claude Primary development paused",
      );
      const bannerReset = banner.locator(".s-num");
      await expect(bannerReset).toContainText(datedResetRe);
      await expect(bannerReset).not.toContainText("UTC");

      const modelsSideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(modelsSideways).toBe(0);
      expect(await axe()).toEqual([]);
    });
  });
}
