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

// Regression coverage for the 2026-11-15 repository, merge queue, and release
// UI walkthrough (docs/test-runs/2026-11-15-repository-release-memory-ui.md):
// 1. A refused repository setting's error banner clears as soon as that
//    setting saves cleanly;
// 2. Role-restricted viewers on /next/repos/<owner>/<name> see a read-only
//    explanation naming their role and missing repos:write scope;
// 3. Unsaved workflow edits on a zero-workflow repository (and unsaved repo
//    config or release settings) ask before leaving or switching tabs;
// 4. Settings -> Repositories Remove reports POST /api/repos/plan's stranded
//    work summaries and keeps removed repositories gone across section switches;
// 5. Classic per-release links (/repos/<owner>/<name>/releases/<release>) open
//    the Releases tab and explain unknown releases in place without 400s.

import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

test(
  "refused repository setting banner clears on clean save, and unsaved workflow edits on a zero-workflow repo ask before leaving",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const capsRes = await page.request.get(
      "/api/repos/acme/widgets/capabilities",
    );
    const beforeCaps: string[] =
      (await capsRes.json())?.defaultCapabilities ?? [];

    try {
      await page.goto("repos/acme/widgets/agents");
      const capsInput = page.getByLabel(/Default capabilities/i);
      await expect(capsInput).toBeVisible();

      await capsInput.fill("nosuch-server#bad-method");
      await page.getByRole("button", { name: "Save agent settings" }).click();

      const alertBanner = page.getByRole("alert");
      await expect(alertBanner).toContainText("nosuch-server#bad-method");

      await capsInput.fill(beforeCaps.join(", "));
      await page.getByRole("button", { name: "Save agent settings" }).click();
      await expect(alertBanner).toHaveCount(0);

      // Zero-workflow repository: typing workflow YAML and switching tabs asks.
      await page.getByRole("button", { name: "CI & Workflows" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/ci$/);

      const wfEditor = page.getByLabel(/Declared workflow/i);
      await wfEditor.fill(
        "name: smoke\non: [push]\njobs:\n  smoke:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ok\n",
      );

      await page.getByRole("button", { name: "Overview" }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog).toContainText("unsaved CI workflows changes");

      const vw = page.viewportSize()!.width;
      const box = await dialog.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(15);
      expect(box!.x + box!.width).toBeLessThanOrEqual(vw - 15);

      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/ci$/);
      await expect(wfEditor).toHaveValue(/name: smoke/);

      await page.getByRole("button", { name: "Overview" }).click();
      await dialog.getByRole("button", { name: "Discard" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);

      // Unsaved Clone URL on Overview also asks before switching tabs.
      await page
        .getByLabel("Clone URL")
        .fill("https://example.com/unsaved-clone.git");
      await page.getByRole("button", { name: "Releases" }).click();
      await expect(dialog).toContainText("unsaved repository config changes");
      await dialog.getByRole("button", { name: "Discard" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/releases$/);

      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways).toBeLessThanOrEqual(0);
      expect(await axe()).toEqual([]);
    } finally {
      await page.request.put("/api/repos/acme/widgets/capabilities", {
        data: { defaultCapabilities: beforeCaps },
      });
    }
  },
);

test(
  "classic per-release URLs open Releases without 400s and explain unknown releases in place",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const badResponses: string[] = [];
    page.on("response", (res) => {
      if (res.url().includes("/api/") && res.status() >= 400) {
        badResponses.push(`${res.status()} ${res.url()}`);
      }
    });

    await page.goto("repos/acme/widgets/releases/1.0");
    await expect(
      page.getByRole("heading", { level: 1, name: "acme/widgets" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Releases" }),
    ).toHaveAttribute("aria-current", "page");
    expect(badResponses).toEqual([]);

    await page.goto("repos/acme/widgets/releases/nosuch-e2e-5888");
    await expect(
      page.getByRole("heading", { level: 1, name: "acme/widgets" }),
    ).toBeVisible();
    const fallbackNotice = page.getByRole("status").filter({
      hasText: 'acme/widgets has no release named "nosuch-e2e-5888"',
    });
    await expect(fallbackNotice).toBeVisible();
    await expect(fallbackNotice).toContainText("showing 1.0 instead");
    expect(badResponses).toEqual([]);

    const vw = page.viewportSize()!.width;
    const noticeBox = await fallbackNotice.boundingBox();
    expect(noticeBox).not.toBeNull();
    expect(noticeBox!.x).toBeGreaterThanOrEqual(0);
    expect(noticeBox!.x + noticeBox!.width).toBeLessThanOrEqual(vw);

    expect(await axe()).toEqual([]);
  },
);

test(
  "selecting another release after opening a per-release URL survives Releases actions that reload the tab",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const relB = "2.0-url-e2e";
    const relC = "3.0-url-e2e";
    try {
      const createRes = await page.request.post(
        "/api/repos/acme/widgets/releases",
        { data: { name: relB } },
      );
      expect(createRes.ok()).toBe(true);

      await page.goto("repos/acme/widgets/releases/1.0");
      const releasesTable = page.getByRole("table", { name: "Releases" });
      const row10 = releasesTable.getByRole("row").filter({ hasText: "1.0" });
      const rowB = releasesTable.getByRole("row").filter({ hasText: relB });

      await expect(
        row10.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "true");
      await expect(
        rowB.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "false");

      // User picks relB, then creates relC (which triggers loadReleasesTab).
      await rowB.getByRole("button", { name: "Inspect RCs" }).click();
      await expect(
        rowB.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "true");

      await page.getByLabel(/New release/i).fill(relC);
      await page.getByRole("button", { name: "Create release" }).click();
      await expect(
        releasesTable.getByRole("row").filter({ hasText: relC }),
      ).toBeVisible();
      await expect(
        rowB.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "true");
      await expect(
        row10.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "false");

      // Opening an unknown release URL, picking relB, and deleting relC keeps
      // relB selected and does not bring back the stale-release banner.
      await page.goto("repos/acme/widgets/releases/nosuch-6419-e2e");
      const fallbackNotice = page.getByRole("status").filter({
        hasText: 'acme/widgets has no release named "nosuch-6419-e2e"',
      });
      await expect(fallbackNotice).toBeVisible();

      await rowB.getByRole("button", { name: "Inspect RCs" }).click();
      await expect(fallbackNotice).toHaveCount(0);
      await expect(
        rowB.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "true");

      await releasesTable
        .getByRole("row")
        .filter({ hasText: relC })
        .getByRole("button", { name: "Delete" })
        .click();
      const dialog = page.getByRole("dialog");
      await expect(dialog).toContainText(`Remove release ${relC}?`);
      await dialog.getByRole("button", { name: "Remove release" }).click();
      await expect(
        releasesTable.getByRole("row").filter({ hasText: relC }),
      ).toHaveCount(0);

      await expect(
        rowB.getByRole("button", { name: "Inspect RCs" }),
      ).toHaveAttribute("aria-pressed", "true");
      await expect(fallbackNotice).toHaveCount(0);
      expect(await axe()).toEqual([]);
    } finally {
      await page.request
        .delete(`/api/repos/acme/widgets/releases/${relC}`)
        .catch(() => null);
      await page.request
        .delete(`/api/repos/acme/widgets/releases/${relB}`)
        .catch(() => null);
    }
  },
);

test("Settings -> Repositories Remove warns on stranded tasks and keeps removed repositories gone across section switches", async ({
  page,
  axe,
}) => {
  const tempRepo = "acme/walk-remove-5888";
  let createdTaskId = "";
  try {
    await page.request.post("/api/repos", { data: { repo: tempRepo } });
    const taskRes = await page.request.post("/api/tasks", {
      data: {
        title: "Open task in walk-remove-5888",
        description: "Verifies stranded warning on repository removal.",
        repo: tempRepo,
      },
    });
    expect(taskRes.ok()).toBe(true);
    createdTaskId = String((await taskRes.json()).id ?? "");

    await page.goto("settings/repositories");
    const row = page.getByRole("row").filter({ hasText: tempRepo });
    await expect(row).toBeVisible();

    await row.getByRole("button", { name: "Remove" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(tempRepo);
    await expect(dialog).toContainText("parked instead of dispatched");
    await expect(dialog).toContainText("Per-repo settings");

    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);

    // Switching to another Settings section and back must not resurrect tempRepo.
    await page.getByRole("link", { name: /^Git$/i }).click();
    await expect(page).toHaveURL(/\/next\/settings\/git$/);
    await page.getByRole("link", { name: /^Repositories$/i }).click();
    await expect(page).toHaveURL(/\/next\/settings\/repositories$/);
    await expect(
      page.getByRole("row").filter({ hasText: tempRepo }),
    ).toHaveCount(0);

    // Check stranded tasks now reports the parked task on tempRepo.
    await page.getByRole("button", { name: "Check stranded tasks" }).click();
    await expect(page.getByText(/parked instead of dispatched/)).toBeVisible();

    expect(await axe()).toEqual([]);
  } finally {
    if (createdTaskId) {
      await page.request
        .post(`/api/tasks/${createdTaskId}/close`, {
          data: { reason: "e2e cleanup" },
        })
        .catch(() => null);
    }
    await page.request.delete(`/api/repos/${tempRepo}`).catch(() => null);
  }
});

test(
  "viewer role sees a read-only explanation banner on /next/repos/<owner>/<name>",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    await page.route("**/api/config", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      body.access = {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["repos:read", "settings:read", "releases:read", "ci:read"],
      };
      await route.fulfill({ response, json: body });
    });

    await page.goto("repos/acme/widgets");
    const banner = page.getByText(/grain\/viewer.*repos:write/);
    await expect(banner).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Save issue import" }),
    ).toBeDisabled();

    const vw = page.viewportSize()!.width;
    const bannerBox = await banner.boundingBox();
    expect(bannerBox).not.toBeNull();
    expect(bannerBox!.x).toBeGreaterThanOrEqual(0);
    expect(bannerBox!.x + bannerBox!.width).toBeLessThanOrEqual(vw);

    expect(await axe()).toEqual([]);
  },
);
