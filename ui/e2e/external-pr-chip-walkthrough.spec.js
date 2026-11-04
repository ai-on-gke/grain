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

// What external-pr-chip.spec.js leaves out, walked for
// docs/test-plans/2026-11-02.md §9 (PRs #327 and #326): the chip in dark
// as well as light at both widths, and the chip -- with the pull request
// it names -- gone once the task is moved to another repository, where
// acme/widgets#41 means nothing (model.LinkOffTarget,
// docs/test-runs/2026-11-02-github-ghes-release-train.md).
//
// No stub, as there: `grain demo -github-sim -fork-pull-request` seeds
// contrib's open pull request #41 headed fix/readme-typo.

test.use({ demoArgs: ["-github-sim", "-fork-pull-request"] });

const REF = "acme/widgets#41";

async function fileOnForkHead(page) {
  const created = await page.request.post("/api/tasks", {
    data: {
      title: `Walk the contributor's typo ${Date.now()}`,
      description: "Filed against a fork head label.",
      repo: "acme/widgets",
      base: "contrib:fix/readme-typo",
    },
  });
  expect(created.status()).toBe(201);
  const { id } = await created.json();
  return id;
}

async function expectChipInFreshnessLine(page) {
  const chip = page.getByTestId("external-pr-chip");
  await expect(chip).toHaveCount(1);
  await expect(chip).toHaveText(REF);
  const freshness = page.locator(".freshness").filter({ has: chip });
  await expect(freshness).toHaveCount(1);
  const c = await chip.boundingBox();
  const f = await freshness.boundingBox();
  expect(c && f).toBeTruthy();
  expect(c.x).toBeGreaterThan(f.x);
  expect(c.x + c.width).toBeLessThanOrEqual(f.x + f.width + 1);
  expect(f.height).toBeLessThanOrEqual(c.height + 8);
  const label = await chip.locator(".MuiChip-label").boundingBox();
  expect(
    Math.abs(label.y + label.height / 2 - (c.y + c.height / 2)),
  ).toBeLessThanOrEqual(1.5);
  // Readable in either theme: the label's colour is not the chip's own
  // background.
  const colours = await chip.evaluate((el) => {
    const label = el.querySelector(".MuiChip-label");
    return {
      text: getComputedStyle(label).color,
      background: getComputedStyle(el).backgroundColor,
    };
  });
  expect(colours.text).not.toBe(colours.background);
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`the external pull request chip sits in the header in dark mode (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.emulateMedia({ colorScheme: "dark" });
    const id = await fileOnForkHead(page);
    await page.goto(`/tasks/${id}`);
    await expectChipInFreshnessLine(page);
  });
}

test("moving a fork-label task to another repository drops its external pull request chip", async ({
  page,
}) => {
  const id = await fileOnForkHead(page);
  await page.goto(`/tasks/${id}`);
  await expect(page.getByTestId("external-pr-chip")).toHaveCount(1);

  // A task moves only onto a repository the deployment targets; the
  // worker's demo is shared, so its list is put back however this ends.
  const deployment = await (await page.request.get("/api/deployment")).json();
  const targetRepos = deployment.targetRepos || [];
  try {
    const widened = await page.request.put("/api/deployment", {
      data: { targetRepos: [...new Set([...targetRepos, "acme/gizmos"])] },
    });
    expect(widened.ok(), await widened.text()).toBe(true);

    const moved = await page.request.patch(`/api/tasks/${id}`, {
      data: { repo: "acme/gizmos" },
    });
    expect(moved.ok(), await moved.text()).toBe(true);
    const task = await (await page.request.get(`/api/tasks/${id}`)).json();
    expect(task.repo).toBe("acme/gizmos");
    expect(task.externalPullRequests).toBeUndefined();
    expect(task.description || "").not.toContain("refs/pull/41/head");

    await page.reload();
    await expect(page.locator(".freshness")).toContainText("as of just now");
    await expect(page.getByTestId("external-pr-chip")).toHaveCount(0);
  } finally {
    await page.request.put("/api/deployment", { data: { targetRepos } });
  }
});
