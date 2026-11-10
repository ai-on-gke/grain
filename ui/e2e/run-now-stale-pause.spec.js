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

// A stale Run now request (given back by an interrupted or paused run) stays
// enabled when re-stamping overrides a retry backoff or setup suspension hold,
// and stays disabled when the task is held by a usage-limit pause or an
// operator pause that Run now cannot override. Both hints sit under Run now
// inside the actions column without overflowing desktop or phone viewports.
test("stale Run now button and hints align inside the actions column across holds", async ({
  page,
}) => {
  const created = await page.request.post("/api/tasks", {
    data: {
      title: "Stale Run now layout check",
      description: "e2e",
      approved: true,
    },
  });
  expect(created.ok()).toBeTruthy();
  const { id } = await created.json();
  try {
    let mode = "suspension";
    await page.route(`**/api/tasks/${id}`, async (route) => {
      const resp = await route.fetch();
      const body = await resp.json();
      body.state = "queued";
      body.runNowAt = "2026-11-10T05:00:00Z";
      body.runNowStale = true;
      body.attempts = [
        {
          id: `${id}-1`,
          attempt: 1,
          startedAt: "2026-11-10T05:00:02Z",
          finishedAt: "2026-11-10T05:00:05Z",
          outcome: mode === "suspension" ? "suspended" : "paused",
        },
      ];
      if (mode === "suspension") {
        body.activityBySetup = true;
        body.activity =
          "waiting: held until 2026-11-10T05:02:05Z by its suspension";
      }
      await route.fulfill({
        status: resp.status(),
        headers: resp.headers(),
        body: JSON.stringify(body),
      });
    });
    await page.route("**/api/config", async (route) => {
      const resp = await route.fetch();
      const cfg = await resp.json();
      if (mode === "usagelimit") {
        cfg.agentBackends = [{ id: "gemini", framework: "antigravity" }];
        cfg.agentPause = {
          paused: true,
          until: "2026-11-10T05:30:05Z",
          reason: "gemini: usage limit reached",
          pauses: [
            {
              connection: "gemini",
              framework: "antigravity",
              until: "2026-11-10T05:30:05Z",
              reason: "gemini: usage limit reached",
            },
          ],
        };
      }
      await route.fulfill({
        status: resp.status(),
        headers: resp.headers(),
        body: JSON.stringify(cfg),
      });
    });

    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);

      mode = "suspension";
      await page.goto(`/tasks/${id}`);
      const actions = page.locator(".actions");
      const runNow = actions.getByRole("button", {
        name: "Run now",
        exact: true,
      });
      await expect(runNow).toBeEnabled();
      const suspHint = actions.locator(".run-now-hint");
      await expect(suspHint).toContainText(
        "It is held until 2026-11-10T05:02:05Z by its suspension. Press Run now to start it now.",
      );

      mode = "usagelimit";
      await page.goto(`/tasks/${id}`);
      await expect(runNow).toBeDisabled();
      const pauseHint = actions.locator(".run-now-hint");
      const fwHint = actions.locator(".framework-paused-hint");
      await expect(pauseHint).toContainText("once the usage-limit pause ends.");
      await expect(fwHint).toBeVisible();

      const colBox = await actions.boundingBox();
      const btnBox = await runNow.boundingBox();
      const hintBox = await pauseHint.boundingBox();
      const fwBox = await fwHint.boundingBox();
      expect(hintBox.y).toBeGreaterThan(btnBox.y);
      expect(fwBox.y).toBeGreaterThan(hintBox.y);
      expect(hintBox.x).toBeGreaterThanOrEqual(colBox.x - 1);
      expect(hintBox.x + hintBox.width).toBeLessThanOrEqual(
        colBox.x + colBox.width + 1,
      );
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(1);
    }
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await page.request.post(`/api/tasks/${id}/close`, { data: {} });
  }
});
