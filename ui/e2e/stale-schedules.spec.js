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

import { readFileSync } from "node:fs";
import { test, expect } from "./fixtures.js";

// Schedules that have stopped running (model.Store.StaleSchedules,
// grain/task-4344) on System → Health and on the CI pane. The demo seeds
// every schedule fresh, so the daemon's own answer is laid under the
// fixture's staleSchedules -- the same file `screenshot.mjs --merge`
// shoots these pages with.
const fixture = JSON.parse(
  readFileSync(new URL("./stale-schedules.fixture.json", import.meta.url)),
);

async function withStaleSchedules(page) {
  await page.route(
    (u) => u.pathname === "/api/sandboxes",
    async (route) => {
      const response = await route.fetch();
      const json = { ...(await response.json()), ...fixture };
      await route.fulfill({ response, json });
    },
  );
}

// Every row inside the alert's box, and the alert inside the page: a
// reason as long as a ci_mode's is wraps rather than running off a phone.
async function expectContained(page, alert) {
  const box = await alert.boundingBox();
  const width = page.viewportSize().width;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5);
  for (const row of await alert.getByTestId("stale-schedule").all()) {
    const r = await row.boundingBox();
    expect(r.x).toBeGreaterThanOrEqual(box.x);
    expect(r.x + r.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
  }
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`System → Health lists every stale schedule (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await withStaleSchedules(page);
    await page.goto("/system/health");
    const alert = page.getByTestId("stale-schedules");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText("2 schedules have stopped running");
    const rows = alert.getByTestId("stale-schedule");
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText("Weekly docs link check");
    await expect(rows.nth(0)).toContainText("acme/gadgets");
    await expect(rows.nth(0)).toContainText("every 7d");
    await expect(rows.nth(0)).toContainText("last ran never");
    await expect(rows.nth(1)).toContainText("workflow live-agent");
    await expect(rows.nth(1)).toContainText("every 1d");
    await expect(
      rows.nth(1).getByTestId("stale-schedule-reason"),
    ).toContainText(".grain/workflows/live-agent.yml");
    // The reason sits under its schedule's line, not beside it.
    const head = await rows
      .nth(1)
      .getByTestId("stale-schedule-name")
      .boundingBox();
    const reason = await rows
      .nth(1)
      .getByTestId("stale-schedule-reason")
      .boundingBox();
    expect(reason.y).toBeGreaterThanOrEqual(head.y + head.height - 1);
    await expectContained(page, alert);
  });

  test(`the CI pane names a stale schedule above its listing (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await withStaleSchedules(page);
    await page.goto("/ci");
    const alert = page.getByTestId("stale-schedules");
    await expect(alert).toBeVisible();
    await expect(alert.getByTestId("stale-schedule")).toHaveCount(2);
    // Above the listing it explains the absence of -- here the demo's
    // "no grain CI" note, which a ci_mode that drops the cron is the
    // reason for -- rather than below it or beside it.
    const below = page.getByText(
      "Not available: this deployment does not run grain CI",
      {
        exact: false,
      },
    );
    await expect(below).toBeVisible();
    const a = await alert.boundingBox();
    const b = await below.boundingBox();
    expect(a.y + a.height).toBeLessThanOrEqual(b.y);
    await expectContained(page, alert);
  });
}

test("the CI pane narrowed to one repo shows only that repo's", async ({
  page,
}) => {
  await withStaleSchedules(page);
  await page.addInitScript(() =>
    localStorage.setItem(
      "grain.ci.narrowing",
      JSON.stringify({
        search: "",
        sort: [],
        filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
      }),
    ),
  );
  await page.goto("/ci");
  const alert = page.getByTestId("stale-schedules");
  await expect(alert).toBeVisible();
  const rows = alert.getByTestId("stale-schedule");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toHaveAttribute(
    "data-schedule",
    "workflow-acme-widgets-live-agent",
  );
});
