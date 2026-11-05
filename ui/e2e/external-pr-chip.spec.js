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

// A task filed against a fork head label adopts the pull request it heads
// as a model.LinkExternalPR link (pkg/forkhead). GET /api/tasks/<id>
// carries it as externalPullRequests, and the task page draws it as a
// link chip in the "as of just now" line (grain/task-2199).
//
// No stub: `grain demo -github-sim -fork-pull-request` seeds acme/widgets
// with contrib's open pull request #41 headed fix/readme-typo, resolved
// through the real forkhead.Resolve over the sim's `?head=` lookup.
test.use({ demoArgs: ["-github-sim", "-fork-pull-request"] });

const REF = "acme/widgets#41";

async function fileOnForkHead(page) {
  const created = await page.request.post("/api/tasks", {
    data: {
      title: `Fix the contributor's typo ${Date.now()}`,
      description: "Filed against a fork head label.",
      repo: "acme/widgets",
      base: "contrib:fix/readme-typo",
    },
  });
  expect(created.status()).toBe(201);
  const { id } = await created.json();
  const task = await (await page.request.get(`/api/tasks/${id}`)).json();
  expect(task.externalPullRequests).toEqual([REF]);
  return id;
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`the external pull request is a chip in the task header (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const id = await fileOnForkHead(page);
    await page.goto(`/tasks/${id}`);

    const chip = page.getByTestId("external-pr-chip");
    await expect(chip).toHaveCount(1);
    await expect(chip).toHaveText(REF);
    await expect(chip).toHaveAttribute("href", /\/acme\/widgets\/pull\/41$/);

    // Inside the freshness line, right of where it starts, and level with
    // its text rather than dropped onto a line of its own.
    const freshness = page.locator(".freshness").filter({ has: chip });
    await expect(freshness).toHaveCount(1);
    await expect(freshness).toContainText("as of just now");
    const c = await chip.boundingBox();
    const f = await freshness.boundingBox();
    expect(c && f).toBeTruthy();
    expect(c.x).toBeGreaterThan(f.x);
    expect(c.x + c.width).toBeLessThanOrEqual(f.x + f.width + 1);
    expect(c.y).toBeGreaterThanOrEqual(f.y - 4);
    expect(c.y + c.height).toBeLessThanOrEqual(f.y + f.height + 4);
    const label = await chip.locator(".MuiChip-label").boundingBox();
    expect(
      Math.abs(label.y + label.height / 2 - (c.y + c.height / 2)),
    ).toBeLessThanOrEqual(1.5);
    // One line: the header is no taller than the chip with a little slack.
    expect(f.height).toBeLessThanOrEqual(c.height + 8);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
}

test("a task on an ordinary base has no external pull request chip", async ({
  page,
}) => {
  const created = await page.request.post("/api/tasks", {
    data: { title: `Ordinary base ${Date.now()}`, repo: "acme/widgets" },
  });
  expect(created.status()).toBe(201);
  const { id } = await created.json();
  const task = await (await page.request.get(`/api/tasks/${id}`)).json();
  expect(task.externalPullRequests).toBeUndefined();
  await page.goto(`/tasks/${id}`);
  await expect(page.locator(".freshness")).toContainText("as of just now");
  await expect(page.getByTestId("external-pr-chip")).toHaveCount(0);
});
