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

// Regression scenarios from the 2026-11-19 repository, merge queue, and
// release walkthrough (docs/test-runs/2026-11-19-repository-release-memory-ui.md):
//   1. Direct and unknown release URLs (/repos/<o>/<n>/releases/<release>) stay
//      on the repository instead of misparsing <o> as a git host, falling back
//      to the newest release with an in-place explanation.
//   2. A refused repo setting's error banner clears as soon as that setting
//      saves cleanly.
//   3. Unsaved release settings and clone URL edits ask before leaving.
//   4. Touch/tap reorder on /next/repos exposes Move up / Move down / Done
//      controls within the viewport when a row is picked up.
//   5. Role-restricted viewers see the read-only explanation banner on
//      Overview, Merge queue, and Agents.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";

test("handles direct and unknown release URLs without misparsing the owner as a host @phone @dark", async ({
  page,
  axe,
}) => {
  const bad: string[] = [];
  page.on("response", (r) => {
    if (r.status() >= 400 && r.url().includes("/api/")) {
      bad.push(`${r.status()} ${r.url()}`);
    }
  });

  await page.goto(`${R}/releases/no-such-release-7306`);
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "acme/widgets",
  );
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/releases$/);

  const notice = page.getByTestId("unknown-release-notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("no-such-release-7306");
  await expect(notice).toContainText("showing 1.0 instead");

  const vw = page.viewportSize()!.width;
  const box = await notice.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(vw + 0.5);
  expect(await axe()).toEqual([]);

  // Direct URL to an existing release selects it cleanly with no warning.
  await page.goto(`${R}/releases/1.0`);
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "acme/widgets",
  );
  await expect(page.getByTestId("unknown-release-notice")).toHaveCount(0);
  await expect(page.getByTestId("release-row-1.0")).toBeVisible();
  expect(bad).toEqual([]);
});

test("clears a refused repo setting banner as soon as that setting saves cleanly", async ({
  page,
}) => {
  const before = await (
    await page.request.get("/api/repos/acme/widgets/workflows")
  ).json();
  try {
    await page.goto(`${R}/ci`);
    await page
      .getByTestId("input-new-workflow-path")
      .fill(".grain/workflows/walk-7306.yml");
    await page.getByTestId("add-repo-workflow-btn").click();
    await page.getByTestId("input-repo-workflow").fill("not: [valid: yaml");
    await page.getByTestId("save-repo-workflow-btn").click();

    const banner = page.getByTestId("repo-error-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("yaml");

    await page
      .getByTestId("input-repo-workflow")
      .fill(
        "name: walk\non:\n  pull_request: {}\njobs:\n  check:\n    steps:\n      - run: echo ok\n",
      );
    await page.getByTestId("save-repo-workflow-btn").click();
    await expect(banner).toHaveCount(0);
  } finally {
    await page.request.put("/api/repos/acme/widgets/workflows", {
      data: { workflows: before.workflows ?? [] },
    });
  }
});

test("asks before discarding unsaved release settings or clone URL edits", async ({
  page,
}) => {
  await page.goto(`${R}/releases`);
  const baseJobs = page.getByTestId("input-release-base-jobs");
  await expect(baseJobs).toBeVisible();
  await baseJobs.fill("unit, lint");

  await page.getByTestId("repo-tab-overview").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("unsaved release settings changes");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\/releases$/);
  await expect(baseJobs).toHaveValue("unit, lint");

  await page.getByTestId("repo-tab-overview").click();
  await dialog.getByRole("button", { name: "Discard" }).click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);

  const cloneUrl = page.getByTestId("input-repo-clone-url");
  await cloneUrl.fill("https://github.com/acme/other-7306.git");
  await page.getByTestId("repo-tab-ci").click();
  await expect(dialog).toContainText("unsaved repository config changes");
  await dialog.getByRole("button", { name: "Discard" }).click();
  await expect(page).toHaveURL(/\/ci$/);
});

test("offers tap Move up / Move down / Done controls on /next/repos that fit phone width @phone @dark", async ({
  page,
  axe,
}) => {
  const added = "acme/z-tap-order-7306";
  const addRes = await page.request.post("/api/repos", {
    data: { repo: added },
  });
  expect(addRes.ok()).toBe(true);
  try {
    await page.goto("/next/repos");
    await page.evaluate(() => localStorage.clear());
    await page.reload();

    const names = () => page.locator("a.repos-name").allTextContents();
    await expect.poll(names).toEqual(["acme/widgets", added]);

    await page.locator(`[data-handle="${added}"]`).click();
    const up = page.getByTestId("repos-move-up");
    const down = page.getByTestId("repos-move-down");
    const done = page.getByTestId("repos-move-done");
    await expect(up).toBeVisible();
    await expect(up).toBeEnabled();
    await expect(down).toBeDisabled();

    const vw = page.viewportSize()!.width;
    for (const btn of [up, down, done]) {
      const box = await btn.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.width).toBeGreaterThanOrEqual(24);
      expect(box!.height).toBeGreaterThanOrEqual(24);
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(vw + 0.5);
    }
    expect(await axe()).toEqual([]);

    await up.click();
    await expect.poll(names).toEqual([added, "acme/widgets"]);
    await done.click();
    await expect(up).toHaveCount(0);

    await page.reload();
    await expect.poll(names).toEqual([added, "acme/widgets"]);
  } finally {
    await page.request.delete("/api/repos/acme/z-tap-order-7306");
  }
});

test("explains why repository settings are read-only for a viewer role", async ({
  page,
}) => {
  await page.route("**/api/config", async (route) => {
    const resp = await route.fetch();
    const json = await resp.json();
    await route.fulfill({
      response: resp,
      json: {
        ...json,
        access: {
          role: "grain/viewer",
          roles: ["grain/viewer"],
          full: false,
          scopes: ["repos:read", "tasks:read", "releases:read", "ci:read"],
        },
      },
    });
  });

  for (const suffix of ["", "/merge-queue", "/agents"]) {
    await page.goto(`${R}${suffix}`);
    const banner = page.getByTestId("repo-readonly-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("grain/viewer");
  }
});
