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

test.use({ demoArgs: ["-review-questions"] });

async function taskTitled(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  return tasks.find((x) => x.title === title);
}

async function noOverflow(page) {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

for (const viewport of [
  { name: "desktop", width: 1280, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`Merge gates and explicit review-override dialog align without overflow (${viewport.name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(viewport);
    const held = await taskTitled(request, "Publish both daemon images");
    expect(held).toBeTruthy();

    await page.goto("/inbox");
    await noOverflow(page);
    const inboxGates = page.getByTestId("inbox-merge-gates").first();
    await expect(inboxGates).toBeVisible();
    await expect(inboxGates).toContainText("Code review: findings");
    const inboxBox = await inboxGates.boundingBox();
    expect(inboxBox.width).toBeGreaterThan(80);
    expect(inboxBox.x).toBeGreaterThanOrEqual(0);
    expect(inboxBox.x + inboxBox.width).toBeLessThanOrEqual(viewport.width + 1);

    await page.goto(`/tasks/${held.id}`);
    await noOverflow(page);
    const gates = page.getByTestId("merge-gates");
    await expect(gates).toBeVisible();
    await expect(page.getByTestId("merge-gate-review")).toContainText(
      "Code review: findings",
    );

    const overrideBtn = page.getByTestId("override-review-gates");
    await expect(overrideBtn).toHaveText("Merge without the code review");

    const gBox = await gates.boundingBox();
    const oBox = await overrideBtn.boundingBox();
    expect(gBox.y + gBox.height).toBeLessThanOrEqual(oBox.y + 200);
    expect(gBox.x + gBox.width).toBeLessThanOrEqual(viewport.width + 1);

    await overrideBtn.click();
    const dialog = page.getByTestId("gate-override-dialog");
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId("confirm-gate-override")).toBeDisabled();
    const dBox = await dialog.boundingBox();
    expect(dBox.width).toBeLessThanOrEqual(viewport.width + 1);
  });
}
