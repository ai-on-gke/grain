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

// The /next/processes/schedules detail of a release-train schedule held
// while publishing is held (docs/test-runs/2026-11-15-release-train-
// publishing-held.md, O3): the reason in words rather than its wire code,
// the overlap group it shares, and what became of each skip -- fired late
// as which task, with how many later occurrences folded in, or dropped.
//
// A demo runs no release train, so the schedule is added to the demo's own
// GET /api/schedules answer. Set GRAIN_UI_SHOTS=<dir> to write the panel
// at each size and theme.

import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

const SHOTS = process.env.GRAIN_UI_SHOTS || "";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

const train = {
  id: "sched-train-held",
  title: "Release train",
  description: "Cut an rc from the newest known-good main.",
  repo: "acme/grain",
  base: "main",
  autoMerge: false,
  capabilities: [],
  recurrence: { kind: "every_n_minutes", everyNMinutes: 20 },
  overlap: "skip",
  overlapGroup: "release-train",
  source: "acme/grain",
  type: "template",
  enabled: true,
  nextRunAt: "2026-11-15T09:20:00Z",
  createdAt: "2026-11-01T00:00:00Z",
  suppressed: {
    reason: "publishingHeld",
    taskId: "2",
    detail: "skipped: publishing held by task 2",
  },
  skips: [
    {
      occurrence: "2026-11-15T08:00:00Z",
      at: "2026-11-15T08:00:00Z",
      reason: "publishingHeld",
      summary: "skipped at 2026-11-15 08:00 UTC: publishing held by task 2",
      taskId: "2",
      outcome: "fired",
      firedTaskId: "5",
      folded: 3,
    },
    {
      occurrence: "2026-11-15T07:00:00Z",
      at: "2026-11-15T07:00:00Z",
      reason: "task",
      summary:
        "skipped at 2026-11-15 07:00 UTC: #1 from the previous firing is still open (running)",
      taskId: "1",
      outcome: "dropped",
    },
  ],
  revision: "rev-train-held",
};

for (const size of SIZES) {
  for (const theme of THEMES) {
    test(`a held schedule says why in words and what its skips became — ${size.name}, ${theme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      await page.route(/\/api\/schedules(\?.*)?$/, async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        const res = await route.fetch();
        const body = await res.json();
        (Array.isArray(body) ? body : body.schedules).unshift(train);
        await route.fulfill({ response: res, json: body });
      });

      await page.goto("/next/processes/schedules");
      await page.getByTestId(`sched-row-${train.id}`).click();

      await expect(
        page.getByText(
          "Suppressed — publishing held: skipped: publishing held by task 2",
        ),
      ).toBeVisible();
      await expect(page.getByText("(publishingHeld)")).toHaveCount(0);
      await expect(page.getByTestId("sched-overlap-group")).toHaveText(
        "release-train",
      );
      await expect(
        page.getByTestId("sched-config-pane").locator("dt", {
          hasText: "Overlap group",
        }),
      ).toBeVisible();

      const skips = page.getByTestId("sched-skip");
      await expect(skips).toHaveCount(2);
      const fired = skips.nth(0).getByTestId("sched-skip-outcome");
      await expect(fired).toHaveText(
        /→ fired\s+#5\s+·\s+3 later occurrences folded in/,
      );
      await expect(fired.getByRole("link", { name: "#5" })).toHaveAttribute(
        "href",
        "/next/task/5",
      );
      await expect(skips.nth(1).getByTestId("sched-skip-outcome")).toHaveText(
        "→ dropped",
      );

      // The outcome stays inside its row: beside the summary on a desktop,
      // under it on a phone, never squeezing it into a narrow column.
      for (let i = 0; i < 2; i++) {
        const row = await skips.nth(i).boundingBox();
        const summary = await skips
          .nth(i)
          .locator("span")
          .first()
          .boundingBox();
        const refs = await skips.nth(i).locator("span").nth(1).boundingBox();
        expect(refs.x + refs.width).toBeLessThanOrEqual(row.x + row.width);
        expect(summary.width).toBeGreaterThanOrEqual(
          Math.min(240, row.width - 24),
        );
      }
      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBe(0);

      if (SHOTS) {
        fs.mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({
          path: path.join(SHOTS, `schedule-held-${size.name}-${theme}.png`),
        });
      }
    });
  }
}

test("an event trigger says when it runs, not its placeholder nextRunAt", async ({
  page,
}) => {
  const trigger = {
    ...train,
    id: "sched-trigger-next",
    title: "On release workflow",
    recurrence: {
      kind: "github_workflow_run",
      githubWorkflowRun: { workflow: "release.yml", conclusions: ["failure"] },
    },
    nextRunAt: "2027-11-15T09:00:00Z",
    suppressed: undefined,
    skips: [],
    overlapGroup: undefined,
  };
  await page.route(/\/api\/schedules(\?.*)?$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    const body = await res.json();
    (Array.isArray(body) ? body : body.schedules).unshift(trigger);
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/processes/schedules");
  await page.getByTestId(`sched-row-${trigger.id}`).click();
  await expect(page.getByTestId("sched-skip")).toHaveCount(0);
  await expect(page.getByText(trigger.description)).toBeVisible();
  await expect(
    page
      .getByTestId("sched-config-pane")
      .locator("dt", { hasText: "Next run" }),
  ).toHaveCount(1);
  await expect(
    page.getByTestId("sched-config-pane").getByText("on workflow run"),
  ).toBeVisible();
  await expect(page.getByText("2027-11-15T09:00:00Z")).toHaveCount(0);
});
