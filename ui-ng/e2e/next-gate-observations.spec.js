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

// Uses the ordinary task API, with the Go API test's real persisted report
// observations as the additive sidecar. Legacy detail and controls are real.
import fs from "node:fs";
import { test, expect } from "./fixtures.js";
const fixture = JSON.parse(
  fs.readFileSync(
    new URL("./fixtures/gate-observations.json", import.meta.url),
    "utf8",
  ),
);

test("displays inert report observations and history at every size @phone @dark", async ({
  page,
}) => {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const task = tasks.find((t) => t.state !== "closed") ?? tasks[0];
  await page.route(`**/api/tasks/${task.id}?*`, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, ...fixture } });
  });
  await page.route(`**/api/tasks/${task.id}`, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, ...fixture } });
  });
  await page.goto(`/next/tasks/${task.id}`);
  const card = page.getByTestId("gate-observations");
  await expect(card).toBeVisible();
  await expect(card).toContainText("Observation only");
  await expect(card).toContainText("delivery v1 · met");
  await expect(card).toContainText("acknowledgment v1 · pending");
  await expect(card.locator("button,a,input")).toHaveCount(0);
  await card
    .getByText("Configuration, provenance and comparison history", {
      exact: true,
    })
    .click();
  await expect(card).toContainText("equivalent_milestones");
  const box = await card.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize().width + 1);
  const dir = process.env.GRAIN_UI_SHOTS;
  if (dir) {
    fs.mkdirSync(dir, { recursive: true });
    await page.screenshot({
      path: `${dir}/gate-observations-${page.viewportSize().width}-${await page.evaluate(() => document.documentElement.dataset.theme)}.png`,
      fullPage: true,
    });
  }
});
