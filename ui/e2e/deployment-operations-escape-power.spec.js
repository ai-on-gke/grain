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

// Regressions found by the 2026-10-02 deployment-operations walkthrough
// (docs/test-runs/2026-10-02-deployment-operations.md), each driven
// through the real demo server and built frontend:
//
//   - leaving Settings saves the field being typed in, rather than
//     dropping it;
//   - a confirmed power action says it was requested, rather than
//     leaving the screen exactly as a dismissed confirm leaves it.
//
// Power is never sent to the demo host: /api/config is the real answer
// with the three power switches turned on, and the three POST endpoints
// are answered here.

// Settings is a view now rather than a pane Escape closes
// (docs/design/settings-panel.md §3.1); leaving it by the rail is the
// way out, and a box being typed in is saved on the way, as it was by
// Escape.
test("saves a typed setting when the rail leaves Settings", async ({
  page,
}) => {
  const name = `escape-${Date.now()}`;
  await page.goto("/settings/general");
  const field = page.getByLabel("Environment name");
  await field.fill(name);
  await page.getByRole("button", { name: /^Tasks \d+/ }).click();

  await expect(page).not.toHaveURL(/\/settings/);
  await expect(page).toHaveTitle(new RegExp(`^${name} `));
  const stored = await (await page.request.get("/api/settings")).json();
  expect(stored.environmentName).toBe(name);

  await page.goto("/settings/general");
  await expect(page.getByLabel("Environment name")).toHaveValue(name);
});

test("acknowledges an accepted power action and reports a refused one", async ({
  page,
}) => {
  await page.route(/\/api\/config$/, async (route) => {
    const response = await route.fetch();
    const config = await response.json();
    await route.fulfill({
      response,
      json: {
        ...config,
        restartEnabled: true,
        rebootEnabled: true,
        shutdownEnabled: true,
      },
    });
  });
  const posted = [];
  await page.route(/\/api\/host\/(restart|reboot|shutdown)$/, (route) => {
    const action = route.request().url().split("/").pop();
    posted.push(action);
    if (action === "restart") return route.fulfill({ json: { ok: true } });
    if (action === "reboot") return route.abort("connectionreset");
    return route.fulfill({ status: 500, json: { error: "refused by stub" } });
  });

  await page.goto("/system/power");
  const restart = page.getByRole("button", { name: "Restart grain" });
  await expect(restart).toBeVisible();

  page.once("dialog", (dialog) => dialog.dismiss());
  await restart.click();
  await expect(page.getByText(/requested\./)).toHaveCount(0);
  expect(posted).toEqual([]);

  page.once("dialog", (dialog) => dialog.accept());
  await restart.click();
  await expect(page.getByText(/^Restart requested\./)).toBeVisible();

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Reboot host" }).click();
  await expect(page.getByText(/^Reboot requested\./)).toBeVisible();
  await expect(page.getByText(/^Restart requested\./)).toHaveCount(0);

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Shut down host" }).click();
  await expect(page.getByText("refused by stub")).toBeVisible();
  await expect(page.getByText(/^Shutdown requested\./)).toHaveCount(0);
  await expect(page.getByText(/^Reboot requested\./)).toHaveCount(0);

  expect(posted).toEqual(["restart", "reboot", "shutdown"]);
});
