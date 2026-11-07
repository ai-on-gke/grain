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

// §4.1 of design/design/unified-grants.design.md: the task pane's Hosting
// select replaces the console capability, sits aligned beneath Admission in
// the actions column at desktop and phone widths, persists PATCH /api/tasks/:id
// with {"hosting":"console"}, and records a Hosting changed timeline event.
test("sets a task's hosting from the task pane and records the timeline event", async ({
  page,
  request,
}) => {
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E hosting setting ${Date.now()}`,
      repo: "acme/widgets",
    },
  });
  expect(created.ok()).toBeTruthy();
  const task = await created.json();

  for (const [width, height] of [
    [1440, 1000],
    [390, 844],
  ]) {
    await page.setViewportSize({ width, height });
    await page.goto(`/tasks/${task.id}`);

    const admission = page.getByRole("combobox", { name: "Admission" });
    const hosting = page.getByRole("combobox", { name: "Hosting" });
    await hosting.scrollIntoViewIfNeeded();
    await expect(admission).toBeVisible();
    await expect(hosting).toBeVisible();

    const admBox = await admission.boundingBox();
    const hostBox = await hosting.boundingBox();
    expect(Math.abs(hostBox.x - admBox.x)).toBeLessThanOrEqual(2);
    expect(Math.abs(hostBox.width - admBox.width)).toBeLessThanOrEqual(2);
    expect(hostBox.y).toBeGreaterThan(admBox.y + admBox.height);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
  }

  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`/tasks/${task.id}`);
  const hosting = page.getByRole("combobox", { name: "Hosting" });
  await expect(hosting).toHaveText(/Unattended/);
  await expect(
    page.getByText(
      "Hands the agent its prompt and runs the turn to completion on its own.",
    ),
  ).toBeVisible();

  const patched = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${task.id}`) &&
      r.request().method() === "PATCH",
  );
  await hosting.click();
  await page.getByRole("option", { name: "Interactive console" }).click();
  const res = await patched;
  expect(res.ok()).toBeTruthy();
  expect(JSON.parse(res.request().postData())).toEqual({ hosting: "console" });

  await expect(hosting).toHaveText(/Interactive console/);
  await expect(
    page.getByText(
      "Runs the agent CLI on an interactive terminal in the UI until somebody closes it.",
    ),
  ).toBeVisible();
  await expect(page.getByText("Hosting changed")).toBeVisible();
  await expect(
    page.getByText(/from Unattended to Interactive console/),
  ).toBeVisible();

  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  expect(detail.hosting).toBe("console");
});
