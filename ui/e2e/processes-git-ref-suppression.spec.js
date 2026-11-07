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

test("off-target git_ref schedule shows Suppressed badge, warning, and non-overlapping row layout on /processes", async ({
  page,
}) => {
  const title = `Off-target ref watch ${Date.now()}`;
  let createdId = null;
  let originalTargetRepos = null;

  const settingsRes = await page.request.get("/api/settings");
  expect(settingsRes.ok()).toBe(true);
  const settings = await settingsRes.json();
  originalTargetRepos = settings.targetRepos;

  try {
    // Narrow targetRepos to a concrete allowlist so offlist/upstream is off-target.
    const narrowRes = await page.request.put("/api/settings", {
      data: { targetRepos: ["acme/widgets"] },
    });
    expect(narrowRes.ok()).toBe(true);

    // Retired grain/config-<team> is refused fast with HTTP 400.
    const badRes = await page.request.post("/api/schedules", {
      data: {
        title: "Forbidden config watch",
        description: "Should fail validation.",
        repo: "acme/widgets",
        recurrence: {
          kind: "git_ref",
          gitRef: { repo: "grain/config-retired", ref: "refs/heads/main" },
        },
      },
    });
    expect(badRes.status()).toBe(400);

    // Creating a schedule whose target repo is allowed ("acme/widgets") but
    // whose watched git_ref repo ("offlist/upstream") is outside targetRepos
    // succeeds with 201 and surfaces watchedRepoOffTarget suppression.
    const createRes = await page.request.post("/api/schedules", {
      data: {
        title,
        description: "Sync when upstream tag moves.",
        repo: "acme/widgets",
        recurrence: {
          kind: "git_ref",
          gitRef: { repo: "offlist/upstream", ref: "refs/tags/latest" },
        },
      },
    });
    expect(createRes.status()).toBe(201);
    const created = await createRes.json();
    createdId = created.id;
    expect(created.suppressed).toEqual({
      reason: "watchedRepoOffTarget",
      detail:
        "watches offlist/upstream, which is not on this deployment's target repo list",
    });

    await page.goto("/processes?f=kind:any:schedule");
    const rowLink = page.getByRole("link", { name: title });
    await expect(rowLink).toBeVisible();

    const row = page.locator(".process-row", { has: rowLink });
    const suppressedChip = row.locator(".MuiChip-root", {
      hasText: "Suppressed",
    });
    const detailHint = row.locator(".schedule-overdue.hint");
    const nextCell = row.locator(".process-next");

    await expect(suppressedChip).toBeVisible();
    await expect(detailHint).toContainText(
      "suppressed: watches offlist/upstream, which is not on this deployment's target repo list",
    );
    await expect(nextCell).toHaveText("suppressed");

    // Layout geometry assertions: row cells and badges must remain inside the
    // viewport and not overlap horizontally.
    const nameBox = await row.locator(".process-name").boundingBox();
    const repoBox = await row.locator(".process-repo").boundingBox();
    const nextBox = await nextCell.boundingBox();
    expect(nameBox).not.toBeNull();
    expect(repoBox).not.toBeNull();
    expect(nextBox).not.toBeNull();
    expect(nameBox.x + nameBox.width).toBeLessThanOrEqual(repoBox.x + 2);
    expect(repoBox.x + repoBox.width).toBeLessThanOrEqual(nextBox.x + 2);

    // Opening the schedule overlay explains why polling is suppressed and how to fix it.
    await rowLink.click();
    const overlayWarning = page.locator(
      ".overlay-panel .schedule-overdue, [role='dialog'] .schedule-overdue",
    );
    await expect(
      page.getByText(
        /This schedule is suppressed and is not polling its watched ref: watches offlist\/upstream, which is not on this deployment's target repo list\./,
      ),
    ).toBeVisible();
    void overlayWarning;
  } finally {
    if (createdId) {
      await page.request.delete(`/api/schedules/${createdId}`);
    }
    if (Array.isArray(originalTargetRepos)) {
      await page.request.put("/api/settings", {
        data: { targetRepos: originalTargetRepos },
      });
    }
  }
});
