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

test("walkthrough: repository ownership models and CI capability security boundaries (docs/test-plans/2026-09-23.md §5)", async ({
  page,
  request,
}) => {
  // Step 1: Navigate to repository page (/repo/bwsalmon/grain)
  await page.goto("/repo/bwsalmon/grain");
  await expect(
    page.getByRole("heading", { name: "bwsalmon/grain" }),
  ).toBeVisible();

  await page.getByRole("tab", { name: "Releases" }).click();
  // Verify Work ownership section exists
  await expect(
    page.getByRole("heading", { name: "Work ownership" }),
  ).toBeVisible();

  // Step 2: Initial state verification (unclaimed / nobody has said)
  const defaultNobodySaid = page
    .locator(".MuiChip-root")
    .filter({ hasText: "Nobody has said" });
  await expect(defaultNobodySaid.first()).toBeVisible();

  const defaultRunsChip = page
    .locator(".MuiChip-root")
    .filter({ hasText: "Runs this repository's work" });
  await expect(defaultRunsChip).toBeVisible();

  // Step 3: Transition to read-only ("off")
  const defaultOffBtn = page.getByRole("button", {
    name: "the repository default: off",
  });
  await expect(defaultOffBtn).toBeVisible();
  await defaultOffBtn.click();

  // Verify UI displays "Off, set here" and "Does not run this repository's work"
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: "Off, set here" }).first(),
  ).toBeVisible();
  await expect(
    page
      .locator(".MuiChip-root")
      .filter({ hasText: "Does not run this repository's work" }),
  ).toBeVisible();

  // Verify API reflects the off state
  let ownershipRes = await request.get(
    "/api/repos/bwsalmon/grain/work-ownership",
  );
  expect(ownershipRes.ok()).toBeTruthy();
  let ownershipData = await ownershipRes.json();
  expect(ownershipData.default).toBe("off");
  expect(ownershipData.defaultRuns).toBe(false);

  // Step 4: Transition to managed ("on")
  const defaultOnBtn = page.getByRole("button", {
    name: "the repository default: on",
  });
  await expect(defaultOnBtn).toBeVisible();
  await defaultOnBtn.click();

  // Verify UI displays "On, set here" and "Runs this repository's work"
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: "On, set here" }).first(),
  ).toBeVisible();
  await expect(
    page
      .locator(".MuiChip-root")
      .filter({ hasText: "Runs this repository's work" }),
  ).toBeVisible();

  // Verify API reflects the on state
  ownershipRes = await request.get("/api/repos/bwsalmon/grain/work-ownership");
  expect(ownershipRes.ok()).toBeTruthy();
  ownershipData = await ownershipRes.json();
  expect(ownershipData.default).toBe("on");
  expect(ownershipData.defaultRuns).toBe(true);

  // Step 5: Transition back to unclaimed / default ("say nothing")
  const defaultUnsetBtn = page.getByRole("button", {
    name: "the repository default: say nothing",
  });
  await expect(defaultUnsetBtn).toBeVisible();
  await defaultUnsetBtn.click();

  // Verify UI returns to "Nobody has said"
  await expect(
    page
      .locator(".MuiChip-root")
      .filter({ hasText: "Nobody has said" })
      .first(),
  ).toBeVisible();

  // Step 6: Exercise item-level overrides (release item)
  const releaseOffBtn = page.getByRole("button", {
    name: "release: off",
  });
  await expect(releaseOffBtn).toBeVisible();
  await releaseOffBtn.click();
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: "Not run" }),
  ).toBeVisible();
  // The chosen state is drawn filled; aria-pressed says the same to a
  // screen reader (grain/task-1532).
  await expect(releaseOffBtn).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "release: on" }),
  ).toHaveAttribute("aria-pressed", "false");

  const releaseOnBtn = page.getByRole("button", {
    name: "release: on",
  });
  await releaseOnBtn.click();
  await expect(
    page.locator(".MuiChip-root").filter({ hasText: "Runs" }).first(),
  ).toBeVisible();

  const releaseSayNothingBtn = page.getByRole("button", {
    name: "release: say nothing",
  });
  await releaseSayNothingBtn.click();

  // Step 7: Verify Read-Only / Conflicting Target restrictions
  const invalidTaskRes = await request.post("/api/tasks", {
    data: {
      title: "Task with conflicting target and read-only specification",
      repo: "bwsalmon/grain",
      reads: ["bwsalmon/grain"],
    },
  });
  expect(invalidTaskRes.status()).toBe(400);
  const invalidTaskErr = await invalidTaskRes.json();
  expect(invalidTaskErr.error).toContain(
    "it cannot also be a repo the run may only read",
  );
  // Step 8 refused a team-scoped config repo's task to somebody named
  // outside that team, and went with team roles
  // (docs/single-tenant-reset.md, 3e-iii-c2).
});
